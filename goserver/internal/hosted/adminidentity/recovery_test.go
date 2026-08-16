package adminidentity

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"strings"
	"testing"
	"time"

	"bilibili-live-gift-panel/internal/hosted/identity"
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

func TestSendRecoveryRequiresRecentTOTPAndNeverEmailsPassword(t *testing.T) {
	now := time.Date(2026, 8, 16, 11, 0, 0, 0, time.UTC)
	clock := now
	repository := initializedMemoryRepository(t, now)
	verifier := &memoryVerifier{verification: identity.Verification{UID: "32249588", CompletedAt: now}}
	sender := &MemorySender{}
	service := newTestServiceWithClock(t, repository, verifier, sender, func() time.Time { return clock })
	login, err := service.VerifyLogin(context.Background(), "login-proof", "123456")
	if err != nil {
		t.Fatal(err)
	}

	result, err := service.SendRecovery(context.Background(), login.Token)
	if err != nil {
		t.Fatalf("SendRecovery() error = %v", err)
	}
	if len(result.RecoveryPassword) != 20 {
		t.Fatalf("RecoveryPassword length = %d", len(result.RecoveryPassword))
	}
	messages := sender.Messages()
	if len(messages) != 1 || len(messages[0].Attachments) != 1 {
		t.Fatalf("messages = %#v", messages)
	}
	encodedMessage := messages[0].Text + string(messages[0].Attachments[0].Data)
	if strings.Contains(encodedMessage, result.RecoveryPassword) {
		t.Fatal("recovery email exposed archive password")
	}

	clock = now.Add(RecentTOTPWindow + time.Second)
	if _, err := service.SendRecovery(context.Background(), login.Token); !errors.Is(err, ErrRecentTOTPRequired) {
		t.Fatalf("SendRecovery(stale TOTP) error = %v", err)
	}
	if got := len(sender.Messages()); got != 1 {
		t.Fatalf("stale TOTP sent %d messages, want still one", got)
	}
}

func TestCompleteRecoveryRequiresFreshMatchingProofAndAtomicallyRotatesEverything(t *testing.T) {
	now := time.Date(2026, 8, 16, 11, 10, 0, 0, time.UTC)
	repository := initializedMemoryRepository(t, now)
	oldCode := "old-recovery-code"
	oldHash := sha256.Sum256([]byte(oldCode))
	repository.activeCodes[oldHash] = struct{}{}
	verifier := &memoryVerifier{verification: identity.Verification{UID: "32249588", CompletedAt: now}}
	sender := &MemorySender{}
	service := newTestService(t, repository, verifier, sender, now)
	login, err := service.VerifyLogin(context.Background(), "login-proof", "123456")
	if err != nil {
		t.Fatal(err)
	}

	result, err := service.CompleteRecovery(context.Background(), "recovery-proof", oldCode)
	if err != nil {
		t.Fatalf("CompleteRecovery() error = %v", err)
	}
	if result.TOTPURI == "" || len(result.RecoveryPassword) != 20 {
		t.Fatalf("CompleteRecovery() = %#v", result)
	}

	repository.mu.Lock()
	_, consumed := repository.usedCodes[oldHash]
	_, stillActive := repository.activeCodes[oldHash]
	activeCount := len(repository.activeCodes)
	newEpoch := repository.identity.CredentialEpoch
	rotatedSecret := bytes.Clone(repository.identity.TOTPSecretCiphertext)
	repository.mu.Unlock()
	if !consumed || stillActive || activeCount != RecoveryCodeCount || newEpoch != 2 {
		t.Fatalf("recovery state consumed=%v oldActive=%v active=%d epoch=%d", consumed, stillActive, activeCount, newEpoch)
	}
	if bytes.Equal(rotatedSecret, initializedMemoryRepository(t, now).identity.TOTPSecretCiphertext) {
		t.Fatal("recovery did not rotate the TOTP ciphertext")
	}
	if err := service.RequireRecentTOTP(context.Background(), login.Token); !errors.Is(err, ErrAuthenticationFailed) {
		t.Fatalf("old session after recovery error = %v, want ErrAuthenticationFailed", err)
	}
	if len(sender.Messages()) != 1 || strings.Contains(sender.Messages()[0].Text, result.RecoveryPassword) {
		t.Fatal("recovery delivery missing or included its password")
	}
}

func TestCompleteRecoveryRejectsMismatchedOrStaleBilibiliProofWithoutConsumingCode(t *testing.T) {
	now := time.Date(2026, 8, 16, 11, 20, 0, 0, time.UTC)
	tests := []identity.Verification{
		{UID: "11111111", CompletedAt: now},
		{UID: "32249588", CompletedAt: now.Add(-5*time.Minute - time.Second)},
	}
	for _, proof := range tests {
		t.Run(proof.UID+proof.CompletedAt.String(), func(t *testing.T) {
			repository := initializedMemoryRepository(t, now)
			code := "still-active-code"
			hash := sha256.Sum256([]byte(code))
			repository.activeCodes[hash] = struct{}{}
			service := newTestService(t, repository, &memoryVerifier{verification: proof}, &MemorySender{}, now)
			if _, err := service.CompleteRecovery(context.Background(), "bad-proof", code); !errors.Is(err, ErrAuthenticationFailed) {
				t.Fatalf("CompleteRecovery() error = %v", err)
			}
			repository.mu.Lock()
			_, active := repository.activeCodes[hash]
			epoch := repository.identity.CredentialEpoch
			repository.mu.Unlock()
			if !active || epoch != 1 {
				t.Fatalf("failed proof changed recovery state: active=%v epoch=%d", active, epoch)
			}
		})
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
