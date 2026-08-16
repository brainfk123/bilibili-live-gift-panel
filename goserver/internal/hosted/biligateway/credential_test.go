package biligateway

import (
	"bytes"
	"context"
	"database/sql/driver"
	"errors"
	"regexp"
	"strings"
	"testing"
	"time"

	"bilibili-live-gift-panel/internal/hosted/security"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestCredentialStoreReplaceSealsCookieAndAuditsInOneTransaction(t *testing.T) {
	database, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	keys := testCredentialKeys(t)
	now := time.Date(2026, 8, 17, 8, 0, 0, 0, time.UTC)
	store := NewCredentialStore(database, keys, func() time.Time { return now })
	secret := []byte("SESSDATA=service-cookie; bili_jct=private-csrf")
	ciphertext := &capturedValue{}
	audit := &capturedValue{}

	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta(activeCredentialQuery)).WillReturnRows(sqlmock.NewRows([]string{"id", "credential_version"}))
	mock.ExpectExec(regexp.QuoteMeta(insertCredentialQuery)).WithArgs(int64(1), ciphertext, now).WillReturnResult(sqlmock.NewResult(9, 1))
	mock.ExpectExec(regexp.QuoteMeta(insertCredentialAuditQuery)).WithArgs(audit, now).WillReturnResult(sqlmock.NewResult(10, 1))
	mock.ExpectCommit()

	credential, err := store.Replace(context.Background(), secret)
	if err != nil {
		if expectationErr := mock.ExpectationsWereMet(); expectationErr != nil {
			t.Fatalf("Replace() error = %v; SQL = %v", err, expectationErr)
		}
		t.Fatalf("Replace() error = %v", err)
	}
	if credential.Version != 1 {
		t.Fatalf("credential version = %d, want 1", credential.Version)
	}
	if bytes.Contains(ciphertext.value, secret) || bytes.Contains(audit.value, secret) || strings.Contains(string(ciphertext.value), "service-cookie") || strings.Contains(string(audit.value), "private-csrf") {
		t.Fatalf("SQL arguments exposed cookie: ciphertext=%q audit=%q", ciphertext.value, audit.value)
	}
	if len(credential.Cookie) != 0 {
		t.Fatalf("Replace() returned plaintext cookie = %q", credential.Cookie)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestCredentialStoreLoadHidesDecryptionFailure(t *testing.T) {
	database, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	store := NewCredentialStore(database, testCredentialKeys(t), time.Now)
	privateCiphertext := []byte("not-a-valid-ciphertext-private")
	mock.ExpectQuery(regexp.QuoteMeta(loadCredentialQuery)).WillReturnRows(sqlmock.NewRows([]string{"credential_version", "cookie_ciphertext", "created_at"}).AddRow(int64(3), privateCiphertext, time.Now()))

	_, err = store.Load(context.Background())
	if !errors.Is(err, ErrCredentialUnavailable) {
		t.Fatalf("Load() error = %v, want credential unavailable", err)
	}
	if strings.Contains(err.Error(), string(privateCiphertext)) || strings.Contains(strings.ToLower(err.Error()), "cipher") || strings.Contains(strings.ToLower(err.Error()), "key") {
		t.Fatalf("Load() exposed cryptographic detail: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

type capturedValue struct{ value []byte }

func (value *capturedValue) Match(actual driver.Value) bool {
	switch raw := actual.(type) {
	case []byte:
		value.value = append(value.value[:0], raw...)
	case string:
		value.value = append(value.value[:0], raw...)
	default:
		return false
	}
	return true
}

func testCredentialKeys(t *testing.T) security.Keyring {
	t.Helper()
	keys, err := security.NewKeyring(1, bytes.Repeat([]byte{0x61}, 32), bytes.Repeat([]byte{0x62}, 32))
	if err != nil {
		t.Fatal(err)
	}
	return keys
}
