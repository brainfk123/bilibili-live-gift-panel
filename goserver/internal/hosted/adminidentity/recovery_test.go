package adminidentity

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"
)

func TestRecoveryArchiveUsesPinnedScryptAndAuthenticatedEncryption(t *testing.T) {
	material, err := buildRecoveryPackage(&sequenceReader{})
	if err != nil {
		t.Fatalf("buildRecoveryPackage() error = %v", err)
	}
	if len(material.Codes) != RecoveryCodeCount || len(material.Password) != 20 {
		t.Fatalf("material codes=%d password length=%d", len(material.Codes), len(material.Password))
	}
	for _, code := range material.Codes {
		raw, err := base64.RawURLEncoding.DecodeString(code)
		if err != nil || len(raw) != RecoveryCodeBytes {
			t.Fatalf("recovery code %q decodes to %d bytes, err=%v", code, len(raw), err)
		}
		if bytes.Contains(material.Archive, []byte(code)) {
			t.Fatalf("archive exposed recovery code %q", code)
		}
	}

	parameters, codes, err := openRecoveryArchive(material.Archive, material.Password)
	if err != nil {
		t.Fatalf("openRecoveryArchive() error = %v", err)
	}
	if parameters.N != 32768 || parameters.R != 8 || parameters.P != 1 || parameters.KeyLength != 32 {
		t.Fatalf("scrypt parameters = %#v", parameters)
	}
	if strings.Join(codes, ",") != strings.Join(material.Codes, ",") {
		t.Fatalf("opened codes = %v, want %v", codes, material.Codes)
	}
	if _, _, err := openRecoveryArchive(material.Archive, "wrong-password-value"); !errors.Is(err, ErrArchiveAuthentication) {
		t.Fatalf("wrong password error = %v, want ErrArchiveAuthentication", err)
	}

	tampered := bytes.Clone(material.Archive)
	tampered[len(tampered)-1] ^= 0x01
	if _, _, err := openRecoveryArchive(tampered, material.Password); !errors.Is(err, ErrArchiveAuthentication) {
		t.Fatalf("tampered archive error = %v, want ErrArchiveAuthentication", err)
	}
}

func TestDecryptRecoveryArchiveReturnsExactlyTenCodes(t *testing.T) {
	material, err := buildRecoveryPackage(&sequenceReader{})
	if err != nil {
		t.Fatal(err)
	}
	codes, err := DecryptRecoveryArchive(material.Archive, material.Password)
	if err != nil {
		t.Fatalf("DecryptRecoveryArchive() error = %v", err)
	}
	if len(codes) != RecoveryCodeCount {
		t.Fatalf("codes = %d, want %d", len(codes), RecoveryCodeCount)
	}
	for index := range codes {
		if codes[index] != material.Codes[index] {
			t.Fatalf("code %d differs", index)
		}
	}
}

func TestSendRecoveryRequiresRecentTOTPAndNeverEmailsPassword(t *testing.T) {
	now := time.Date(2026, 8, 16, 11, 0, 0, 0, time.UTC)
	clock := now
	repository := initializedMemoryRepository(t, now)
	sender := &MemorySender{}
	service := newTestServiceWithClock(t, repository, sender, func() time.Time { return clock })
	login := emailLoginForTest(t, service, sender)
	if err := service.VerifyRecentTOTP(context.Background(), login.Token, "123456"); err != nil {
		t.Fatalf("VerifyRecentTOTP() error = %v", err)
	}

	result, err := service.SendRecovery(context.Background(), login.Token)
	if err != nil {
		t.Fatalf("SendRecovery() error = %v", err)
	}
	if len(result.RecoveryPassword) != 20 {
		t.Fatalf("RecoveryPassword length = %d", len(result.RecoveryPassword))
	}
	messages := sender.Messages()
	if len(messages) != 2 || len(messages[1].Attachments) != 1 {
		t.Fatalf("messages = %#v", messages)
	}
	encodedMessage := messages[1].Text + string(messages[1].Attachments[0].Data)
	if strings.Contains(encodedMessage, result.RecoveryPassword) {
		t.Fatal("recovery email exposed archive password")
	}

	clock = now.Add(RecentTOTPWindow + time.Second)
	if _, err := service.SendRecovery(context.Background(), login.Token); !errors.Is(err, ErrRecentTOTPRequired) {
		t.Fatalf("SendRecovery(stale TOTP) error = %v", err)
	}
	if got := len(sender.Messages()); got != 2 {
		t.Fatalf("stale TOTP sent %d messages, want still two", got)
	}
}

func TestPrepareConfirmRecoveryIsRetryableAndDefersCredentialMutation(t *testing.T) {
	now := time.Date(2026, 8, 16, 11, 15, 0, 0, time.UTC)
	repository := initializedMemoryRepository(t, now)
	oldCode := "reserved-old-recovery-code"
	oldHash := sha256.Sum256([]byte(oldCode))
	repository.activeCodes[oldHash] = struct{}{}
	sender := &MemorySender{Err: ErrUnavailable}
	service := newTestService(t, repository, sender, now)
	if _, err := service.PrepareRecovery(context.Background(), oldCode); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("SMTP-failed prepare error=%v", err)
	}
	repository.mu.Lock()
	if repository.identity.CredentialEpoch != 1 || len(repository.activeCodes) != 1 {
		t.Fatal("prepare failure mutated active credentials")
	}
	var stableArchive []byte
	for _, handoff := range repository.handoffs {
		if handoff.Kind == HandoffRecovery && handoff.State == HandoffPending {
			stableArchive = bytes.Clone(handoff.Archive)
		}
	}
	repository.mu.Unlock()
	if len(stableArchive) == 0 {
		t.Fatal("SMTP failure did not leave committed retryable handoff")
	}
	sender.Err = nil
	prepared, err := service.PrepareRecovery(context.Background(), oldCode)
	if err != nil {
		t.Fatalf("retry PrepareRecovery() error=%v", err)
	}
	if prepared.HandoffToken == "" || prepared.TOTPURI == "" || len(prepared.RecoveryPassword) != 20 {
		t.Fatalf("prepared=%#v", prepared)
	}
	if messages := sender.Messages(); len(messages) != 1 || !bytes.Equal(messages[0].Attachments[0].Data, stableArchive) {
		t.Fatal("retry did not send the stable committed attachment")
	}
	if err := service.ConfirmHandoff(context.Background(), prepared.HandoffToken, "000000"); !errors.Is(err, ErrAuthenticationFailed) {
		t.Fatalf("wrong pending TOTP error=%v", err)
	}
	if err := service.ConfirmHandoff(context.Background(), prepared.HandoffToken, "123456"); err != nil {
		t.Fatalf("ConfirmHandoff() error=%v", err)
	}
	if err := service.ConfirmHandoff(context.Background(), prepared.HandoffToken, "123456"); err != nil {
		t.Fatalf("idempotent ConfirmHandoff() error=%v", err)
	}
	repository.mu.Lock()
	_, consumed := repository.usedCodes[oldHash]
	activeCount, epoch := len(repository.activeCodes), repository.identity.CredentialEpoch
	var secretRetained bool
	for _, handoff := range repository.handoffs {
		if bytes.Equal(handoff.TokenHash, func() []byte {
			hash, _ := service.keys.HashToken("admin_handoff_token", []byte(prepared.HandoffToken))
			return hash
		}()) {
			secretRetained = len(handoff.TOTPSecretCiphertext) != 0 || len(handoff.PasswordCiphertext) != 0 || len(handoff.Archive) != 0
		}
	}
	repository.mu.Unlock()
	if !consumed || activeCount != RecoveryCodeCount || epoch != 2 || secretRetained {
		t.Fatalf("confirmed state consumed=%v active=%d epoch=%d secretRetained=%v", consumed, activeCount, epoch, secretRetained)
	}
}

func TestConcurrentRecoveryPreparationAcrossInstancesAllowsOnePendingAdministratorHandoff(t *testing.T) {
	now := time.Date(2026, 8, 16, 11, 17, 0, 0, time.UTC)
	repository := initializedMemoryRepository(t, now)
	codes := []string{"first-valid-recovery-code", "second-valid-recovery-code"}
	for _, code := range codes {
		hash := sha256.Sum256([]byte(code))
		repository.activeCodes[hash] = struct{}{}
	}
	senders := []*MemorySender{{}, {}}
	services := []*Service{
		newTestService(t, repository, senders[0], now),
		newTestService(t, repository, senders[1], now),
	}
	start := make(chan struct{})
	results := make(chan error, len(services))
	for index := range services {
		go func(index int) {
			<-start
			_, err := services[index].PrepareRecovery(context.Background(), codes[index])
			results <- err
		}(index)
	}
	close(start)
	successes, rejected := 0, 0
	for range services {
		switch err := <-results; {
		case err == nil:
			successes++
		case errors.Is(err, ErrAuthenticationFailed):
			rejected++
		default:
			t.Fatalf("PrepareRecovery() error=%v", err)
		}
	}
	repository.mu.Lock()
	pending := 0
	for _, handoff := range repository.handoffs {
		if handoff.Kind == HandoffRecovery && handoff.State == HandoffPending {
			pending++
		}
	}
	repository.mu.Unlock()
	if successes != 1 || rejected != 1 || pending != 1 || len(senders[0].Messages())+len(senders[1].Messages()) != 1 {
		t.Fatalf("successes=%d rejected=%d pending=%d messages=%d", successes, rejected, pending, len(senders[0].Messages())+len(senders[1].Messages()))
	}
}

func TestExistingRecoveryHandoffRequiresMatchingRequestHash(t *testing.T) {
	now := time.Date(2026, 8, 16, 11, 18, 0, 0, time.UTC)
	repository := initializedMemoryRepository(t, now)
	codeHash := sha256.Sum256([]byte("same-code"))
	repository.activeCodes[codeHash] = struct{}{}
	record := sqlHandoffRecord(now, HandoffRecovery)
	record.EmailCiphertext = bytes.Clone(repository.identity.EmailCiphertext)
	first, err := repository.PrepareRecoveryHandoff(context.Background(), repository.identity.CredentialEpoch, codeHash[:], record)
	if err != nil {
		t.Fatal(err)
	}
	record.RequestHash = bytes.Repeat([]byte{0x7f}, sha256.Size)
	second, err := repository.PrepareRecoveryHandoff(context.Background(), repository.identity.CredentialEpoch, codeHash[:], record)
	if !errors.Is(err, ErrAuthenticationFailed) || second.ID != 0 || first.ID == 0 {
		t.Fatalf("mismatched retry handoff=%#v error=%v", second, err)
	}
}

func TestExpiredRecoveryHandoffIsErasedBeforeDifferentCodeCanPrepare(t *testing.T) {
	now := time.Date(2026, 8, 16, 11, 19, 0, 0, time.UTC)
	repository := initializedMemoryRepository(t, now)
	oldCodeHash := sha256.Sum256([]byte("expired-code"))
	newCodeHash := sha256.Sum256([]byte("new-code"))
	repository.activeCodes[oldCodeHash] = struct{}{}
	repository.activeCodes[newCodeHash] = struct{}{}
	expired := sqlHandoffRecord(now.Add(-time.Hour), HandoffRecovery)
	expired.EmailCiphertext = bytes.Clone(repository.identity.EmailCiphertext)
	expired.ExpiresAt = now.Add(-time.Minute)
	oldHandoff, err := repository.PrepareRecoveryHandoff(context.Background(), repository.identity.CredentialEpoch, oldCodeHash[:], expired)
	if err != nil {
		t.Fatal(err)
	}
	fresh := sqlHandoffRecord(now, HandoffRecovery)
	fresh.EmailCiphertext = bytes.Clone(repository.identity.EmailCiphertext)
	fresh.RequestHash = bytes.Repeat([]byte{0x55}, sha256.Size)
	newHandoff, err := repository.PrepareRecoveryHandoff(context.Background(), repository.identity.CredentialEpoch, newCodeHash[:], fresh)
	if err != nil {
		t.Fatalf("fresh PrepareRecoveryHandoff() error=%v", err)
	}
	repository.mu.Lock()
	_, oldStillPresent := repository.handoffs[oldHandoff.ID]
	repository.mu.Unlock()
	if oldStillPresent || newHandoff.ID == oldHandoff.ID || len(newHandoff.Archive) == 0 {
		t.Fatalf("oldPresent=%v oldID=%d new=%#v", oldStillPresent, oldHandoff.ID, newHandoff)
	}
}

func TestSMTPMessageContainsOnlySafeHeadersAndEncryptedAttachment(t *testing.T) {
	message := Message{
		To: "owner@example.com", Subject: "Gift Panel recovery", Text: "Encrypted archive attached.",
		Attachments: []Attachment{{Filename: "gift-panel-recovery.bin", ContentType: "application/octet-stream", Data: []byte{0, 1, 2, 3, 4, 5}}},
	}
	wire, err := composeSMTPMessage("noreply@example.com", message, "boundary-for-test")
	if err != nil {
		t.Fatalf("composeSMTPMessage() error = %v", err)
	}
	text := string(wire)
	for _, want := range []string{"From: noreply@example.com", "To: owner@example.com", "Content-Disposition: attachment", base64.StdEncoding.EncodeToString(message.Attachments[0].Data)} {
		if !strings.Contains(text, want) {
			t.Fatalf("SMTP message missing %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "archive-password") {
		t.Fatal("SMTP message unexpectedly contains a password")
	}

	message.Subject = "bad\r\nBcc: attacker@example.com"
	if _, err := composeSMTPMessage("noreply@example.com", message, "boundary-for-test"); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("header injection error = %v, want ErrInvalidInput", err)
	}
}

func TestSMTPTransportModeIsExplicitAndPlaintextIsLocalhostOnly(t *testing.T) {
	base := SMTPConfig{Address: "smtp.example.com:587", Host: "smtp.example.com", From: "owner@example.com"}
	if _, err := NewSMTPSender(base); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("missing transport mode error = %v", err)
	}
	base.Mode = SMTPModeInsecureLocalhost
	base.AllowInsecureLocalhost = true
	if _, err := NewSMTPSender(base); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("remote plaintext error = %v", err)
	}
	base.Address, base.Host = "127.0.0.1:1025", "localhost"
	if _, err := NewSMTPSender(base); err != nil {
		t.Fatalf("explicit localhost development SMTP error = %v", err)
	}
	base.Mode, base.AllowInsecureLocalhost = SMTPModeSTARTTLS, false
	if _, err := NewSMTPSender(base); err != nil {
		t.Fatalf("STARTTLS config error = %v", err)
	}
	base.Mode = SMTPModeImplicitTLS
	if _, err := NewSMTPSender(base); err != nil {
		t.Fatalf("implicit TLS config error = %v", err)
	}
}

func TestSMTPMessageAllowsPlainTextAdministratorLoginWithoutAttachment(t *testing.T) {
	wire, err := composeSMTPMessage("noreply@example.com", Message{
		To: "owner@example.com", Subject: "Administrator login code", Text: "Your code is 123456.",
	}, "boundary-for-test")
	if err != nil {
		t.Fatalf("composeSMTPMessage() error = %v", err)
	}
	text := string(wire)
	for _, want := range []string{"From: noreply@example.com", "To: owner@example.com", "Administrator login code", "Your code is 123456."} {
		if !strings.Contains(text, want) {
			t.Fatalf("SMTP message missing %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "Content-Disposition: attachment") {
		t.Fatal("plain-text administrator login message unexpectedly contains an attachment")
	}
}

func TestSMTPSTARTTLSRefusesDowngradeBeforeAuthentication(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	commands := make(chan string, 4)
	serverDone := make(chan struct{})
	go func() {
		defer close(serverDone)
		connection, acceptErr := listener.Accept()
		if acceptErr != nil {
			return
		}
		defer connection.Close()
		_, _ = fmt.Fprint(connection, "220 localhost ESMTP\r\n")
		line, _ := bufio.NewReader(connection).ReadString('\n')
		commands <- line
		_, _ = fmt.Fprint(connection, "250-localhost\r\n250 AUTH PLAIN\r\n")
		line, _ = bufio.NewReader(connection).ReadString('\n')
		commands <- line
	}()
	sender, err := NewSMTPSender(SMTPConfig{Address: listener.Addr().String(), Host: "localhost", From: "sender@example.com", Username: "user", Password: "password", Mode: SMTPModeSTARTTLS, Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	err = sender.Send(context.Background(), Message{To: "owner@example.com", Subject: "Recovery", Text: "encrypted", Attachments: []Attachment{{Filename: "codes.gpra", ContentType: "application/octet-stream", Data: []byte("ciphertext")}}})
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("downgrade error=%v", err)
	}
	<-serverDone
	close(commands)
	for command := range commands {
		if strings.HasPrefix(strings.ToUpper(command), "AUTH ") {
			t.Fatalf("AUTH sent before TLS: %q", command)
		}
	}
}

func TestSMTPContextCancellationClosesBlockedConnection(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	accepted := make(chan net.Conn, 1)
	go func() {
		connection, acceptErr := listener.Accept()
		if acceptErr == nil {
			accepted <- connection
		}
	}()
	sender, err := NewSMTPSender(SMTPConfig{Address: listener.Addr().String(), Host: "localhost", From: "sender@example.com", Mode: SMTPModeInsecureLocalhost, AllowInsecureLocalhost: true, Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	started := time.Now()
	err = sender.Send(ctx, Message{To: "owner@example.com", Subject: "Recovery", Text: "encrypted", Attachments: []Attachment{{Filename: "codes.gpra", ContentType: "application/octet-stream", Data: []byte("ciphertext")}}})
	if !errors.Is(err, ErrUnavailable) || time.Since(started) > 500*time.Millisecond {
		t.Fatalf("cancel error=%v elapsed=%s", err, time.Since(started))
	}
	select {
	case connection := <-accepted:
		_ = connection.Close()
	case <-time.After(time.Second):
		t.Fatal("SMTP server never accepted connection")
	}
}

func TestSMTPImplicitTLSNeverFallsBackToPlaintext(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	serverDone := make(chan struct{})
	go func() {
		defer close(serverDone)
		connection, acceptErr := listener.Accept()
		if acceptErr != nil {
			return
		}
		defer connection.Close()
		_, _ = fmt.Fprint(connection, "220 plaintext-is-not-tls\r\n")
	}()
	sender, err := NewSMTPSender(SMTPConfig{Address: listener.Addr().String(), Host: "localhost", From: "sender@example.com", Mode: SMTPModeImplicitTLS, Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	err = sender.Send(context.Background(), Message{To: "owner@example.com", Subject: "Recovery", Text: "encrypted", Attachments: []Attachment{{Filename: "codes.gpra", ContentType: "application/octet-stream", Data: []byte("ciphertext")}}})
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("implicit TLS downgrade error=%v", err)
	}
	<-serverDone
}
