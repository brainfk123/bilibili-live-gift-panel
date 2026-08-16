package adminidentity

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/base64"
	"fmt"
	"mime/multipart"
	"mime/quotedprintable"
	"net"
	"net/mail"
	"net/smtp"
	"strings"
	"sync"
	"time"
)

type Attachment struct {
	Filename    string
	ContentType string
	Data        []byte
}

type Message struct {
	To          string
	Subject     string
	Text        string
	Attachments []Attachment
}

type MailSender interface {
	Send(context.Context, Message) error
}

type MemorySender struct {
	mu       sync.Mutex
	messages []Message
	Err      error
}

func (sender *MemorySender) Send(_ context.Context, message Message) error {
	if sender == nil {
		return ErrUnavailable
	}
	sender.mu.Lock()
	defer sender.mu.Unlock()
	if sender.Err != nil {
		return ErrUnavailable
	}
	sender.messages = append(sender.messages, cloneMessage(message))
	return nil
}

func (sender *MemorySender) Messages() []Message {
	if sender == nil {
		return nil
	}
	sender.mu.Lock()
	defer sender.mu.Unlock()
	result := make([]Message, len(sender.messages))
	for index, message := range sender.messages {
		result[index] = cloneMessage(message)
	}
	return result
}

type SMTPConfig struct {
	Address                string
	Host                   string
	Username               string
	Password               string
	From                   string
	Mode                   SMTPMode
	AllowInsecureLocalhost bool
	Timeout                time.Duration
}

type SMTPMode string

const (
	SMTPModeImplicitTLS       SMTPMode = "implicit_tls"
	SMTPModeSTARTTLS          SMTPMode = "starttls"
	SMTPModeInsecureLocalhost SMTPMode = "insecure_localhost"
	defaultSMTPTimeout                 = 15 * time.Second
)

type SMTPSender struct {
	config SMTPConfig
}

func NewSMTPSender(config SMTPConfig) (*SMTPSender, error) {
	addressHost, _, splitErr := net.SplitHostPort(config.Address)
	validMode := config.Mode == SMTPModeImplicitTLS || config.Mode == SMTPModeSTARTTLS || config.Mode == SMTPModeInsecureLocalhost
	if !validAddress(config.From) || splitErr != nil || strings.TrimSpace(config.Host) == "" || strings.ContainsAny(config.Host, "\r\n") || (config.Username == "") != (config.Password == "") || !validMode {
		return nil, ErrInvalidInput
	}
	if config.Timeout == 0 {
		config.Timeout = defaultSMTPTimeout
	}
	if config.Timeout <= 0 || config.Timeout > time.Minute {
		return nil, ErrInvalidInput
	}
	if config.Mode == SMTPModeInsecureLocalhost {
		ip := net.ParseIP(addressHost)
		if !config.AllowInsecureLocalhost || ((ip == nil || !ip.IsLoopback()) && !strings.EqualFold(addressHost, "localhost")) || config.Username != "" {
			return nil, ErrInvalidInput
		}
	}
	return &SMTPSender{config: config}, nil
}

func (sender *SMTPSender) Send(ctx context.Context, message Message) error {
	if sender == nil || ctx == nil || ctx.Err() != nil {
		return ErrUnavailable
	}
	wire, err := composeSMTPMessage(sender.config.From, message, "")
	if err != nil {
		return err
	}
	deadline := time.Now().Add(sender.config.Timeout)
	if contextDeadline, ok := ctx.Deadline(); ok && contextDeadline.Before(deadline) {
		deadline = contextDeadline
	}
	dialer := net.Dialer{Timeout: time.Until(deadline)}
	rawConnection, err := dialer.DialContext(ctx, "tcp", sender.config.Address)
	if err != nil {
		return ErrUnavailable
	}
	defer rawConnection.Close()
	_ = rawConnection.SetDeadline(deadline)
	closed := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			_ = rawConnection.Close()
		case <-closed:
		}
	}()
	defer close(closed)

	var connection net.Conn = rawConnection
	tlsActive := false
	if sender.config.Mode == SMTPModeImplicitTLS {
		tlsConnection := tls.Client(connection, &tls.Config{ServerName: sender.config.Host, MinVersion: tls.VersionTLS12})
		if err := tlsConnection.HandshakeContext(ctx); err != nil {
			return ErrUnavailable
		}
		connection = tlsConnection
		tlsActive = true
	}
	client, err := smtp.NewClient(connection, sender.config.Host)
	if err != nil {
		return ErrUnavailable
	}
	defer client.Close()
	if sender.config.Mode == SMTPModeSTARTTLS {
		if supported, _ := client.Extension("STARTTLS"); !supported {
			return ErrUnavailable
		}
		if err := client.StartTLS(&tls.Config{ServerName: sender.config.Host, MinVersion: tls.VersionTLS12}); err != nil {
			return ErrUnavailable
		}
		tlsActive = true
	}
	if sender.config.Username != "" {
		if !tlsActive || client.Auth(smtp.PlainAuth("", sender.config.Username, sender.config.Password, sender.config.Host)) != nil {
			return ErrUnavailable
		}
	}
	if err := client.Mail(sender.config.From); err != nil {
		return ErrUnavailable
	}
	if err := client.Rcpt(message.To); err != nil {
		return ErrUnavailable
	}
	data, err := client.Data()
	if err != nil {
		return ErrUnavailable
	}
	if _, err := data.Write(wire); err != nil {
		_ = data.Close()
		return ErrUnavailable
	}
	if err := data.Close(); err != nil {
		return ErrUnavailable
	}
	if err := client.Quit(); err != nil {
		return ErrUnavailable
	}
	return nil
}

func composeSMTPMessage(from string, message Message, boundary string) ([]byte, error) {
	if !validAddress(from) || !validAddress(message.To) || !validHeader(message.Subject) || message.Subject == "" || len(message.Text) > 1<<20 || len(message.Attachments) == 0 || len(message.Attachments) > 4 {
		return nil, ErrInvalidInput
	}
	for _, attachment := range message.Attachments {
		if !validHeader(attachment.Filename) || attachment.Filename == "" || !validHeader(attachment.ContentType) || attachment.ContentType == "" || len(attachment.Data) == 0 || len(attachment.Data) > 4<<20 {
			return nil, ErrInvalidInput
		}
	}

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if boundary != "" {
		if err := writer.SetBoundary(boundary); err != nil {
			return nil, ErrInvalidInput
		}
	}
	fmt.Fprintf(&body, "From: %s\r\nTo: %s\r\nSubject: %s\r\nMIME-Version: 1.0\r\nContent-Type: multipart/mixed; boundary=%q\r\n\r\n", from, message.To, message.Subject, writer.Boundary())
	textHeader := make(map[string][]string)
	textHeader["Content-Type"] = []string{"text/plain; charset=utf-8"}
	textHeader["Content-Transfer-Encoding"] = []string{"quoted-printable"}
	part, err := writer.CreatePart(textHeader)
	if err != nil {
		return nil, ErrUnavailable
	}
	quoted := quotedprintable.NewWriter(part)
	if _, err := quoted.Write([]byte(message.Text)); err != nil {
		return nil, ErrUnavailable
	}
	if err := quoted.Close(); err != nil {
		return nil, ErrUnavailable
	}
	for _, attachment := range message.Attachments {
		header := make(map[string][]string)
		header["Content-Type"] = []string{attachment.ContentType}
		header["Content-Disposition"] = []string{fmt.Sprintf("attachment; filename=%q", attachment.Filename)}
		header["Content-Transfer-Encoding"] = []string{"base64"}
		part, err := writer.CreatePart(header)
		if err != nil {
			return nil, ErrUnavailable
		}
		encoded := base64.StdEncoding.EncodeToString(attachment.Data)
		for len(encoded) > 76 {
			_, _ = fmt.Fprintf(part, "%s\r\n", encoded[:76])
			encoded = encoded[76:]
		}
		_, _ = fmt.Fprintf(part, "%s\r\n", encoded)
	}
	if err := writer.Close(); err != nil {
		return nil, ErrUnavailable
	}
	return body.Bytes(), nil
}

func validAddress(value string) bool {
	if !validHeader(value) || value == "" {
		return false
	}
	parsed, err := mail.ParseAddress(value)
	return err == nil && parsed.Address == value
}

func validHeader(value string) bool {
	return !strings.ContainsAny(value, "\r\n")
}

func cloneMessage(message Message) Message {
	copy := message
	copy.Attachments = make([]Attachment, len(message.Attachments))
	for index, attachment := range message.Attachments {
		copy.Attachments[index] = attachment
		copy.Attachments[index].Data = bytes.Clone(attachment.Data)
	}
	return copy
}
