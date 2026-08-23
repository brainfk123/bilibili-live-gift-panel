package invitation

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"database/sql/driver"
	"errors"
	"regexp"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"bilibili-live-gift-panel/internal/hosted/identity"
	"bilibili-live-gift-panel/internal/hosted/security"
	"github.com/DATA-DOG/go-sqlmock"
	"github.com/go-sql-driver/mysql"
)

func TestAdministratorInvitationGenerationUsesSessionOnly(t *testing.T) {
	database, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	authorizedAt := time.Date(2026, 8, 21, 14, 0, 0, 0, time.UTC)
	clock := &invitationTimeSequence{values: []time.Time{authorizedAt}}
	authorizer := &invitationSensitiveAuthorizer{}
	keys := fixedInvitationKeys(t)
	service, err := NewService(database, keys, &fakeIntentSource{}, ServiceOptions{Now: clock.Now, Administrator: authorizer})
	if err != nil {
		t.Fatal(err)
	}

	mock.ExpectBegin()
	ciphertext := &ciphertextCapture{}
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO invitations (code_hash, code_ciphertext, code_hint, creator_admin_identity_id, status, created_at, expires_at) VALUES (?, ?, ?, 1, 'active', ?, ?)")).
		WithArgs(sqlmock.AnyArg(), ciphertext, fourCharacterHint{}, authorizedAt, authorizedAt.Add(7*24*time.Hour)).
		WillReturnResult(sqlmock.NewResult(72, 1))
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO audit_events (event_type, actor_admin_identity_id, event_data, created_at) VALUES (?, 1, ?, ?)")).
		WithArgs("invitation_generated", secretFreeJSON{}, authorizedAt).
		WillReturnResult(sqlmock.NewResult(92, 1))
	mock.ExpectCommit()

	generated, err := service.Generate(context.Background(), "administrator-session", ActorAdministrator)
	if err != nil || generated.ID != 72 || generated.Code == "" {
		t.Fatalf("Generate(admin) = %#v, %v", generated, err)
	}
	opened, err := keys.Open("invitation_code_ciphertext", ciphertext.value)
	if err != nil || string(opened) != generated.Code || bytes.Contains(ciphertext.value, []byte(generated.Code)) {
		t.Fatalf("stored invitation ciphertext was not purpose-separated encryption")
	}
	if matched, _ := regexp.MatchString(`^[ABCDEFGHJKLMNPQRSTUVWXYZ23456789]{8}$`, generated.Code); !matched {
		t.Fatalf("generated code = %q", generated.Code)
	}
	if authorizer.authorizeCalls != 1 || authorizer.renewCalls != 0 {
		t.Fatalf("session calls=%d sensitive renew calls=%d", authorizer.authorizeCalls, authorizer.renewCalls)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestQuotaAdjustmentIgnoresObsoleteTOTPRenewal(t *testing.T) {
	database, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	now := time.Date(2026, 8, 21, 14, 10, 0, 0, time.UTC)
	authorizer := &invitationSensitiveAuthorizer{renewErr: errors.New("obsolete renewal failure")}
	service, err := NewService(database, fixedInvitationKeys(t), &fakeIntentSource{}, ServiceOptions{Now: fixedNow(now), Administrator: authorizer})
	if err != nil {
		t.Fatal(err)
	}

	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta("SELECT credential_epoch FROM streamer_accounts WHERE id = ? FOR UPDATE")).
		WillReturnRows(sqlmock.NewRows([]string{"credential_epoch"}).AddRow(int64(3)))
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO invitation_quotas (account_id, remaining_quota, updated_at) VALUES (?, 0, CURRENT_TIMESTAMP(6)) ON DUPLICATE KEY UPDATE account_id = account_id")).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT remaining_quota FROM invitation_quotas WHERE account_id = ? FOR UPDATE")).
		WillReturnRows(sqlmock.NewRows([]string{"remaining_quota"}).AddRow(uint64(2)))
	mock.ExpectExec(regexp.QuoteMeta("UPDATE invitation_quotas SET remaining_quota = ?, updated_at = ? WHERE account_id = ?")).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO invitation_quota_events (account_id, actor_admin_identity_id, quota_delta, quota_after, reason, created_at) VALUES (?, 1, ?, ?, ?, ?)")).
		WillReturnResult(sqlmock.NewResult(95, 1))
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO audit_events (event_type, actor_admin_identity_id, target_account_id, event_data, created_at) VALUES (?, 1, ?, ?, ?)")).
		WillReturnResult(sqlmock.NewResult(96, 1))
	mock.ExpectCommit()

	if _, err := service.AdjustQuota(context.Background(), "administrator-session", 41, 5, "support grant"); err != nil {
		t.Fatalf("AdjustQuota() error = %v", err)
	}
	if authorizer.renewCalls != 0 {
		t.Fatalf("RenewRecentTOTP calls = %d, want 0", authorizer.renewCalls)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestAdministratorInventoryDecryptsOnlyActiveCodesAndSupportsPermanentBatch(t *testing.T) {
	now := time.Date(2026, 8, 23, 10, 0, 0, 0, time.UTC)
	keys := fixedInvitationKeys(t)
	cipher, err := keys.Seal("invitation_code_ciphertext", []byte("ABCDEFGH"))
	if err != nil {
		t.Fatal(err)
	}
	t.Run("list", func(t *testing.T) {
		database, mock, err := sqlmock.New()
		if err != nil {
			t.Fatal(err)
		}
		defer database.Close()
		authorizer := &invitationSensitiveAuthorizer{}
		service, _ := NewService(database, keys, &fakeIntentSource{}, ServiceOptions{Now: fixedNow(now), Administrator: authorizer})
		mock.ExpectQuery("SELECT id,code_ciphertext.*FROM invitations").WithArgs(51).WillReturnRows(sqlmock.NewRows([]string{"id", "cipher", "hint", "status", "created", "expires", "used"}).AddRow(1, cipher, "EFGH", StatusActive, now, now.Add(7*24*time.Hour), 0).AddRow(2, nil, "WXYZ", StatusUsed, now.Add(-time.Hour), now, 52))
		page, err := service.ListAdministrator(context.Background(), "admin", AdminInvitationQuery{})
		if err != nil {
			t.Fatal(err)
		}
		if len(page.Invitations) != 2 || page.Invitations[0].Code != "ABCDEFGH" || page.Invitations[1].Code != "" || page.Invitations[1].CodeHint != "****WXYZ" || page.Invitations[1].UsedByAccountID != 52 {
			t.Fatalf("page=%#v", page)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatal(err)
		}
	})
	t.Run("permanent batch", func(t *testing.T) {
		database, mock, err := sqlmock.New()
		if err != nil {
			t.Fatal(err)
		}
		defer database.Close()
		service, _ := NewService(database, keys, &fakeIntentSource{}, ServiceOptions{Now: fixedNow(now), Administrator: &invitationSensitiveAuthorizer{}})
		mock.ExpectBegin()
		for index := range 2 {
			mock.ExpectExec("INSERT INTO invitations").WithArgs(sqlmock.AnyArg(), sqlmock.AnyArg(), fourCharacterHint{}, now, nil).WillReturnResult(sqlmock.NewResult(int64(71+index), 1))
		}
		mock.ExpectCommit()
		items, err := service.GenerateAdministratorBatch(context.Background(), "admin", 2, "permanent")
		if err != nil {
			t.Fatal(err)
		}
		if len(items) != 2 || items[0].ExpiresAt != nil || items[0].Code == items[1].Code {
			t.Fatalf("items=%#v", items)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatal(err)
		}
	})
}

func TestGenerateDeductsQuotaAtCreation(t *testing.T) {
	now := time.Date(2026, 8, 16, 18, 0, 0, 0, time.UTC)
	database, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	keys := fixedInvitationKeys(t)
	authorizer := &invitationSensitiveAuthorizer{}
	service, err := NewService(database, keys, &fakeIntentSource{}, ServiceOptions{Now: fixedNow(now), Administrator: authorizer})
	if err != nil {
		t.Fatal(err)
	}
	sessionHash, _ := keys.HashToken("site_session", []byte("streamer-session"))

	mock.ExpectBegin()
	expectStreamerAuthorization(mock, sessionHash, 41, 3, now)
	mock.ExpectQuery(regexp.QuoteMeta("SELECT remaining_quota FROM invitation_quotas WHERE account_id = ? FOR UPDATE")).
		WithArgs(int64(41)).WillReturnRows(sqlmock.NewRows([]string{"remaining_quota"}).AddRow(uint64(2)))
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO invitations (code_hash, code_ciphertext, code_hint, creator_account_id, status, created_at, expires_at) VALUES (?, ?, ?, ?, 'active', ?, ?)")).
		WithArgs(hashOnlyArgument{forbidden: "streamer-session"}, sqlmock.AnyArg(), fourCharacterHint{}, int64(41), now, now.Add(7*24*time.Hour)).
		WillReturnResult(sqlmock.NewResult(71, 1))
	mock.ExpectExec(regexp.QuoteMeta("UPDATE invitation_quotas SET remaining_quota = ?, updated_at = ? WHERE account_id = ?")).
		WithArgs(uint64(1), now, int64(41)).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO invitation_quota_events (account_id, invitation_id, quota_delta, quota_after, reason, created_at) VALUES (?, ?, ?, ?, ?, ?)")).
		WithArgs(int64(41), int64(71), int64(-1), uint64(1), "invitation_generated", now).
		WillReturnResult(sqlmock.NewResult(81, 1))
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO audit_events (event_type, actor_account_id, event_data, created_at) VALUES (?, ?, ?, ?)")).
		WithArgs("invitation_generated", int64(41), secretFreeJSON{}, now).
		WillReturnResult(sqlmock.NewResult(91, 1))
	mock.ExpectCommit()

	generated, err := service.Generate(context.Background(), "streamer-session", ActorStreamer)
	if err != nil {
		t.Fatalf("Generate() error = %v", err)
	}
	if generated.ID != 71 || generated.Code == "" || len(generated.CodeHint) != 8 || generated.CodeHint[:4] != "****" || generated.Status != StatusActive || generated.RemainingQuota != 1 {
		t.Fatalf("Generate() = %#v", generated)
	}
	if matched, err := regexp.MatchString(`^[A-Za-z0-9]{8}$`, generated.Code); err != nil || !matched {
		t.Fatalf("generated code = %q, want exactly 8 ASCII letters or digits", generated.Code)
	}
	if generated.CodeHint != "****"+generated.Code[4:] {
		t.Fatalf("CodeHint = %q, want masked final four characters", generated.CodeHint)
	}
	if !generated.ExpiresAt.Equal(now.Add(7 * 24 * time.Hour)) {
		t.Fatalf("ExpiresAt=%v", generated.ExpiresAt)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestAdminGenerationDoesNotConsumeQuota(t *testing.T) {
	now := time.Date(2026, 8, 16, 18, 10, 0, 0, time.UTC)
	database, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	keys := fixedInvitationKeys(t)
	authorizer := &invitationSensitiveAuthorizer{}
	service, err := NewService(database, keys, &fakeIntentSource{}, ServiceOptions{Now: fixedNow(now), Administrator: authorizer})
	if err != nil {
		t.Fatal(err)
	}
	mock.ExpectBegin()
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO invitations (code_hash, code_ciphertext, code_hint, creator_admin_identity_id, status, created_at, expires_at) VALUES (?, ?, ?, 1, 'active', ?, ?)")).
		WithArgs(hashOnlyArgument{forbidden: "admin-session"}, sqlmock.AnyArg(), fourCharacterHint{}, now, now.Add(7*24*time.Hour)).
		WillReturnResult(sqlmock.NewResult(72, 1))
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO audit_events (event_type, actor_admin_identity_id, event_data, created_at) VALUES (?, 1, ?, ?)")).
		WithArgs("invitation_generated", secretFreeJSON{}, now).WillReturnResult(sqlmock.NewResult(92, 1))
	mock.ExpectCommit()

	generated, err := service.Generate(context.Background(), "admin-session", ActorAdministrator)
	if err != nil || generated.ID != 72 || generated.Code == "" {
		t.Fatalf("Generate(admin) = %#v, %v", generated, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestRevokeAndExpireNeverRefundQuota(t *testing.T) {
	now := time.Date(2026, 8, 16, 18, 20, 0, 0, time.UTC)
	t.Run("revoke", func(t *testing.T) {
		database, mock, err := sqlmock.New()
		if err != nil {
			t.Fatal(err)
		}
		defer database.Close()
		keys := fixedInvitationKeys(t)
		service, _ := NewService(database, keys, &fakeIntentSource{}, ServiceOptions{Now: fixedNow(now)})
		sessionHash, _ := keys.HashToken("site_session", []byte("streamer-session"))
		mock.ExpectBegin()
		expectStreamerAuthorization(mock, sessionHash, 41, 3, now)
		mock.ExpectQuery(regexp.QuoteMeta("SELECT status, expires_at FROM invitations WHERE id = ? AND creator_account_id = ? FOR UPDATE")).
			WithArgs(int64(73), int64(41)).WillReturnRows(sqlmock.NewRows([]string{"status", "expires_at"}).AddRow(StatusActive, now.Add(time.Hour)))
		mock.ExpectExec(regexp.QuoteMeta("UPDATE invitations SET status = 'revoked', revoked_at = ?, code_ciphertext = NULL WHERE id = ? AND status = 'active'")).
			WithArgs(now, int64(73)).WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectExec(regexp.QuoteMeta("INSERT INTO audit_events (event_type, actor_account_id, event_data, created_at) VALUES (?, ?, ?, ?)")).
			WithArgs("invitation_revoked", int64(41), secretFreeJSON{}, now).WillReturnResult(sqlmock.NewResult(93, 1))
		mock.ExpectCommit()
		if err := service.Revoke(context.Background(), "streamer-session", 73); err != nil {
			t.Fatal(err)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("expire at boundary", func(t *testing.T) {
		database, mock, err := sqlmock.New()
		if err != nil {
			t.Fatal(err)
		}
		defer database.Close()
		service, _ := NewService(database, fixedInvitationKeys(t), &fakeIntentSource{}, ServiceOptions{Now: fixedNow(now)})
		mock.ExpectBegin()
		mock.ExpectQuery(regexp.QuoteMeta("SELECT status, expires_at FROM invitations WHERE id = ? FOR UPDATE")).
			WithArgs(int64(74)).WillReturnRows(sqlmock.NewRows([]string{"status", "expires_at"}).AddRow(StatusActive, now))
		mock.ExpectExec(regexp.QuoteMeta("UPDATE invitations SET status = 'expired', code_ciphertext = NULL WHERE id = ? AND status = 'active'")).
			WithArgs(int64(74)).WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectExec(regexp.QuoteMeta("INSERT INTO audit_events (event_type, event_data, created_at) VALUES (?, ?, ?)")).
			WithArgs("invitation_expired", secretFreeJSON{}, now).WillReturnResult(sqlmock.NewResult(94, 1))
		mock.ExpectCommit()
		if err := service.Expire(context.Background(), 74); err != nil {
			t.Fatal(err)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatal(err)
		}
	})
}

func TestRevokedInvitationRemainsListable(t *testing.T) {
	now := time.Date(2026, 8, 16, 18, 30, 0, 0, time.UTC)
	database, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	keys := fixedInvitationKeys(t)
	service, _ := NewService(database, keys, &fakeIntentSource{}, ServiceOptions{Now: fixedNow(now)})
	sessionHash, _ := keys.HashToken("site_session", []byte("streamer-session"))
	mock.ExpectBegin()
	expectStreamerAuthorization(mock, sessionHash, 41, 3, now)
	mock.ExpectExec(regexp.QuoteMeta("UPDATE invitations SET status = 'expired', code_ciphertext = NULL WHERE creator_account_id = ? AND status = 'active' AND expires_at <= ?")).
		WithArgs(int64(41), now).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT remaining_quota FROM invitation_quotas WHERE account_id = ?")).
		WithArgs(int64(41)).WillReturnRows(sqlmock.NewRows([]string{"remaining_quota"}).AddRow(uint64(1)))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT id, code_hint, status, created_at, expires_at, revoked_at, used_at FROM invitations WHERE creator_account_id = ? ORDER BY id DESC")).
		WithArgs(int64(41)).WillReturnRows(sqlmock.NewRows([]string{"id", "code_hint", "status", "created_at", "expires_at", "revoked_at", "used_at"}).
		AddRow(int64(73), "Ab_9", StatusRevoked, now.Add(-time.Hour), now.Add(time.Hour), now.Add(-time.Minute), nil))
	mock.ExpectCommit()

	result, err := service.List(context.Background(), "streamer-session")
	if err != nil || result.RemainingQuota != 1 || len(result.Invitations) != 1 {
		t.Fatalf("List() = %#v, %v", result, err)
	}
	if item := result.Invitations[0]; item.Status != StatusRevoked || item.CodeHint != "****Ab_9" {
		t.Fatalf("listed invitation = %#v", item)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestAdjustQuotaCommitsAuditAndRejectsSignedDeltaOverflow(t *testing.T) {
	now := time.Date(2026, 8, 16, 18, 40, 0, 0, time.UTC)
	t.Run("atomic adjustment", func(t *testing.T) {
		database, mock, err := sqlmock.New()
		if err != nil {
			t.Fatal(err)
		}
		defer database.Close()
		keys := fixedInvitationKeys(t)
		clock := &invitationTimeSequence{values: []time.Time{now}}
		authorizer := &invitationSensitiveAuthorizer{}
		service, _ := NewService(database, keys, &fakeIntentSource{}, ServiceOptions{Now: clock.Now, Administrator: authorizer})
		mock.ExpectBegin()
		mock.ExpectQuery(regexp.QuoteMeta("SELECT credential_epoch FROM streamer_accounts WHERE id = ? FOR UPDATE")).
			WithArgs(int64(41)).WillReturnRows(sqlmock.NewRows([]string{"credential_epoch"}).AddRow(int64(3)))
		mock.ExpectExec(regexp.QuoteMeta("INSERT INTO invitation_quotas (account_id, remaining_quota, updated_at) VALUES (?, 0, CURRENT_TIMESTAMP(6)) ON DUPLICATE KEY UPDATE account_id = account_id")).
			WithArgs(int64(41)).WillReturnResult(sqlmock.NewResult(0, 0))
		mock.ExpectQuery(regexp.QuoteMeta("SELECT remaining_quota FROM invitation_quotas WHERE account_id = ? FOR UPDATE")).
			WithArgs(int64(41)).WillReturnRows(sqlmock.NewRows([]string{"remaining_quota"}).AddRow(uint64(2)))
		mock.ExpectExec(regexp.QuoteMeta("UPDATE invitation_quotas SET remaining_quota = ?, updated_at = ? WHERE account_id = ?")).
			WithArgs(uint64(5), now, int64(41)).WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectExec(regexp.QuoteMeta("INSERT INTO invitation_quota_events (account_id, actor_admin_identity_id, quota_delta, quota_after, reason, created_at) VALUES (?, 1, ?, ?, ?, ?)")).
			WithArgs(int64(41), int64(3), uint64(5), "support grant", now).WillReturnResult(sqlmock.NewResult(95, 1))
		mock.ExpectExec(regexp.QuoteMeta("INSERT INTO audit_events (event_type, actor_admin_identity_id, target_account_id, event_data, created_at) VALUES (?, 1, ?, ?, ?)")).
			WithArgs("invitation_quota_adjusted", int64(41), secretFreeJSON{}, now).WillReturnResult(sqlmock.NewResult(96, 1))
		mock.ExpectCommit()
		quota, err := service.AdjustQuota(context.Background(), "admin-session", 41, 5, " support grant ")
		if err != nil || quota.AccountID != 41 || quota.RemainingQuota != 5 {
			t.Fatalf("AdjustQuota() = %#v, %v", quota, err)
		}
		if authorizer.authorizeCalls != 1 || authorizer.renewCalls != 0 {
			t.Fatalf("session calls=%d sensitive renew calls=%d", authorizer.authorizeCalls, authorizer.renewCalls)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("overflow rolls back", func(t *testing.T) {
		database, mock, err := sqlmock.New()
		if err != nil {
			t.Fatal(err)
		}
		defer database.Close()
		keys := fixedInvitationKeys(t)
		authorizer := &invitationSensitiveAuthorizer{}
		service, _ := NewService(database, keys, &fakeIntentSource{}, ServiceOptions{Now: fixedNow(now), Administrator: authorizer})
		mock.ExpectBegin()
		mock.ExpectQuery(regexp.QuoteMeta("SELECT credential_epoch FROM streamer_accounts WHERE id = ? FOR UPDATE")).
			WithArgs(int64(41)).WillReturnRows(sqlmock.NewRows([]string{"credential_epoch"}).AddRow(int64(3)))
		mock.ExpectExec(regexp.QuoteMeta("INSERT INTO invitation_quotas (account_id, remaining_quota, updated_at) VALUES (?, 0, CURRENT_TIMESTAMP(6)) ON DUPLICATE KEY UPDATE account_id = account_id")).
			WithArgs(int64(41)).WillReturnResult(sqlmock.NewResult(0, 0))
		mock.ExpectQuery(regexp.QuoteMeta("SELECT remaining_quota FROM invitation_quotas WHERE account_id = ? FOR UPDATE")).
			WithArgs(int64(41)).WillReturnRows(sqlmock.NewRows([]string{"remaining_quota"}).AddRow("18446744073709551615"))
		mock.ExpectRollback()
		_, err = service.AdjustQuota(context.Background(), "admin-session", 41, 0, "support_revoke")
		if !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("AdjustQuota(overflow) error=%v", err)
		}
		if authorizer.renewCalls != 0 {
			t.Fatalf("overflow renewed recent TOTP %d times", authorizer.renewCalls)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatal(err)
		}
	})
}

func TestRedeemCommitsAccountBindingQuotaSessionInviteAndIntentTogether(t *testing.T) {
	now := time.Date(2026, 8, 16, 18, 50, 0, 0, time.UTC)
	database, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	reservation := newFakeReservation(now.Add(5 * time.Minute))
	intents := &fakeIntentSource{reservations: map[string]*fakeReservation{"registration-intent": reservation}}
	service, _ := NewService(database, fixedInvitationKeys(t), intents, ServiceOptions{Now: fixedNow(now)})
	codeHash := sha256.Sum256([]byte("complete-invitation-code"))

	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta("SELECT id, status, expires_at FROM invitations WHERE code_hash = ? FOR UPDATE")).
		WithArgs(codeHash[:]).WillReturnRows(sqlmock.NewRows([]string{"id", "status", "expires_at"}).AddRow(int64(75), StatusActive, now.Add(time.Hour)))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT account_id FROM bili_uid_bindings WHERE uid_lookup = ? LIMIT 1 FOR UPDATE")).
		WithArgs(reservation.uid.Lookup).WillReturnError(sql.ErrNoRows)
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO streamer_accounts (credential_epoch, created_at, updated_at) VALUES (1, ?, ?)")).
		WithArgs(now, now).WillReturnResult(sqlmock.NewResult(51, 1))
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO bili_uid_bindings (account_id, uid_ciphertext, uid_lookup, bound_at) VALUES (?, ?, ?, ?)")).
		WithArgs(int64(51), reservation.uid.Ciphertext, reservation.uid.Lookup, now).WillReturnResult(sqlmock.NewResult(61, 1))
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO invitation_quotas (account_id, remaining_quota, updated_at) VALUES (?, 0, ?)")).
		WithArgs(int64(51), now).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO site_sessions (account_id, token_hash, credential_epoch, created_at, expires_at) VALUES (?, ?, 1, ?, ?)")).
		WithArgs(int64(51), hashOnlyArgument{forbidden: "complete-invitation-code"}, now, now.Add(24*time.Hour)).WillReturnResult(sqlmock.NewResult(101, 1))
	mock.ExpectExec(regexp.QuoteMeta("UPDATE invitations SET status = 'used', used_at = ?, invited_account_id = ?, code_ciphertext = NULL WHERE id = ? AND status = 'active'")).
		WithArgs(now, int64(51), int64(75)).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO audit_events (event_type, target_account_id, event_data, created_at) VALUES (?, ?, ?, ?)")).
		WithArgs("invitation_redeemed", int64(51), secretFreeJSON{forbidden: []string{"complete-invitation-code", "90000030"}}, now).
		WillReturnResult(sqlmock.NewResult(102, 1))
	mock.ExpectCommit()

	session, err := service.Redeem(context.Background(), "complete-invitation-code", "registration-intent")
	if err != nil || session.Token == "" || session.AccountID != 51 || !session.ExpiresAt.Equal(now.Add(24*time.Hour)) {
		t.Fatalf("Redeem() = %#v, %v", session, err)
	}
	if !reservation.committed || reservation.aborted {
		t.Fatalf("reservation committed=%v aborted=%v", reservation.committed, reservation.aborted)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestRedeemDatabaseFailureAbortsIntentForRetry(t *testing.T) {
	now := time.Date(2026, 8, 16, 19, 0, 0, 0, time.UTC)
	database, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	reservation := newFakeReservation(now.Add(5 * time.Minute))
	intents := &fakeIntentSource{reservations: map[string]*fakeReservation{"registration-intent": reservation}}
	service, _ := NewService(database, fixedInvitationKeys(t), intents, ServiceOptions{Now: fixedNow(now)})
	codeHash := sha256.Sum256([]byte("complete-invitation-code"))
	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta("SELECT id, status, expires_at FROM invitations WHERE code_hash = ? FOR UPDATE")).
		WithArgs(codeHash[:]).WillReturnRows(sqlmock.NewRows([]string{"id", "status", "expires_at"}).AddRow(int64(75), StatusActive, now.Add(time.Hour)))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT account_id FROM bili_uid_bindings WHERE uid_lookup = ? LIMIT 1 FOR UPDATE")).
		WithArgs(reservation.uid.Lookup).WillReturnError(errors.New("private database failure"))
	mock.ExpectRollback()

	_, err = service.Redeem(context.Background(), "complete-invitation-code", "registration-intent")
	if !errors.Is(err, ErrUnavailable) || !reservation.aborted || reservation.committed {
		t.Fatalf("Redeem() error=%v committed=%v aborted=%v", err, reservation.committed, reservation.aborted)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestRedeemRollsBackAndRestoresIntentAcrossFailureMatrix(t *testing.T) {
	now := time.Date(2026, 8, 16, 19, 5, 0, 0, time.UTC)

	t.Run("revoked and expired invitations", func(t *testing.T) {
		for _, status := range []string{StatusRevoked, StatusExpired} {
			t.Run(status, func(t *testing.T) {
				database, mock, err := sqlmock.New()
				if err != nil {
					t.Fatal(err)
				}
				defer database.Close()
				reservation := newFakeReservation(now.Add(5 * time.Minute))
				service, _ := NewService(database, fixedInvitationKeys(t), &fakeIntentSource{reservations: map[string]*fakeReservation{"registration-intent": reservation}}, ServiceOptions{Now: fixedNow(now)})
				codeHash := sha256.Sum256([]byte("invalid-invitation-code"))
				mock.ExpectBegin()
				mock.ExpectQuery(regexp.QuoteMeta("SELECT id, status, expires_at FROM invitations WHERE code_hash = ? FOR UPDATE")).
					WithArgs(codeHash[:]).WillReturnRows(sqlmock.NewRows([]string{"id", "status", "expires_at"}).AddRow(int64(75), status, now.Add(time.Hour)))
				mock.ExpectQuery(regexp.QuoteMeta("SELECT account_id FROM bili_uid_bindings WHERE uid_lookup = ? LIMIT 1 FOR UPDATE")).
					WithArgs(reservation.uid.Lookup).WillReturnError(sql.ErrNoRows)
				mock.ExpectRollback()

				_, err = service.Redeem(context.Background(), "invalid-invitation-code", "registration-intent")
				committed, aborted := reservation.outcome()
				if !errors.Is(err, ErrInvitationInvalid) || committed || !aborted {
					t.Fatalf("Redeem(%s) error=%v committed=%v aborted=%v", status, err, committed, aborted)
				}
				if err := mock.ExpectationsWereMet(); err != nil {
					t.Fatal(err)
				}
			})
		}
	})

	t.Run("existing UID", func(t *testing.T) {
		database, mock, err := sqlmock.New()
		if err != nil {
			t.Fatal(err)
		}
		defer database.Close()
		reservation := newFakeReservation(now.Add(5 * time.Minute))
		service, _ := NewService(database, fixedInvitationKeys(t), &fakeIntentSource{reservations: map[string]*fakeReservation{"registration-intent": reservation}}, ServiceOptions{Now: fixedNow(now)})
		codeHash := sha256.Sum256([]byte("complete-invitation-code"))
		mock.ExpectBegin()
		mock.ExpectQuery(regexp.QuoteMeta("SELECT id, status, expires_at FROM invitations WHERE code_hash = ? FOR UPDATE")).
			WithArgs(codeHash[:]).WillReturnRows(sqlmock.NewRows([]string{"id", "status", "expires_at"}).AddRow(int64(75), StatusActive, now.Add(time.Hour)))
		mock.ExpectQuery(regexp.QuoteMeta("SELECT account_id FROM bili_uid_bindings WHERE uid_lookup = ? LIMIT 1 FOR UPDATE")).
			WithArgs(reservation.uid.Lookup).WillReturnRows(sqlmock.NewRows([]string{"account_id"}).AddRow(int64(99)))
		mock.ExpectRollback()

		_, err = service.Redeem(context.Background(), "complete-invitation-code", "registration-intent")
		committed, aborted := reservation.outcome()
		if !errors.Is(err, ErrInvitationInvalid) || committed || !aborted {
			t.Fatalf("Redeem(existing UID) error=%v committed=%v aborted=%v", err, committed, aborted)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("duplicate UID detected by unique constraint", func(t *testing.T) {
		database, mock, err := sqlmock.New()
		if err != nil {
			t.Fatal(err)
		}
		defer database.Close()
		reservation := newFakeReservation(now.Add(5 * time.Minute))
		service, _ := NewService(database, fixedInvitationKeys(t), &fakeIntentSource{reservations: map[string]*fakeReservation{"registration-intent": reservation}}, ServiceOptions{Now: fixedNow(now)})
		codeHash := sha256.Sum256([]byte("complete-invitation-code"))
		mock.ExpectBegin()
		mock.ExpectQuery(regexp.QuoteMeta("SELECT id, status, expires_at FROM invitations WHERE code_hash = ? FOR UPDATE")).
			WithArgs(codeHash[:]).WillReturnRows(sqlmock.NewRows([]string{"id", "status", "expires_at"}).AddRow(int64(75), StatusActive, now.Add(time.Hour)))
		mock.ExpectQuery(regexp.QuoteMeta("SELECT account_id FROM bili_uid_bindings WHERE uid_lookup = ? LIMIT 1 FOR UPDATE")).
			WithArgs(reservation.uid.Lookup).WillReturnError(sql.ErrNoRows)
		mock.ExpectExec(regexp.QuoteMeta("INSERT INTO streamer_accounts (credential_epoch, created_at, updated_at) VALUES (1, ?, ?)")).
			WithArgs(now, now).WillReturnResult(sqlmock.NewResult(51, 1))
		mock.ExpectExec(regexp.QuoteMeta("INSERT INTO bili_uid_bindings (account_id, uid_ciphertext, uid_lookup, bound_at) VALUES (?, ?, ?, ?)")).
			WithArgs(int64(51), reservation.uid.Ciphertext, reservation.uid.Lookup, now).
			WillReturnError(&mysql.MySQLError{Number: 1062, Message: "private duplicate detail"})
		mock.ExpectRollback()

		_, err = service.Redeem(context.Background(), "complete-invitation-code", "registration-intent")
		committed, aborted := reservation.outcome()
		if !errors.Is(err, ErrInvitationInvalid) || committed || !aborted {
			t.Fatalf("Redeem(duplicate UID) error=%v committed=%v aborted=%v", err, committed, aborted)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatal(err)
		}
	})

	for _, test := range []struct {
		name         string
		auditError   error
		commitError  error
		advanceClock bool
		want         error
	}{
		{name: "audit write fails", auditError: errors.New("private audit failure"), want: ErrUnavailable},
		{name: "SQL commit fails", commitError: errors.New("private commit failure"), want: ErrUnavailable},
		{name: "intent reaches absolute expiry before commit", advanceClock: true, want: ErrInvitationInvalid},
	} {
		t.Run(test.name, func(t *testing.T) {
			database, mock, err := sqlmock.New()
			if err != nil {
				t.Fatal(err)
			}
			defer database.Close()
			reservation := newFakeReservation(now.Add(5 * time.Minute))
			clock := fixedNow(now)
			if test.advanceClock {
				clock = steppedClock(now, now.Add(5*time.Minute))
			}
			service, _ := NewService(database, fixedInvitationKeys(t), &fakeIntentSource{reservations: map[string]*fakeReservation{"registration-intent": reservation}}, ServiceOptions{Now: clock})
			expectRedeemThroughAudit(mock, reservation, now, "complete-invitation-code", test.auditError)
			switch {
			case test.auditError != nil:
				mock.ExpectRollback()
			case test.advanceClock:
				mock.ExpectRollback()
			default:
				mock.ExpectCommit().WillReturnError(test.commitError)
			}

			_, err = service.Redeem(context.Background(), "complete-invitation-code", "registration-intent")
			committed, aborted := reservation.outcome()
			if !errors.Is(err, test.want) || committed || !aborted {
				t.Fatalf("Redeem() error=%v committed=%v aborted=%v", err, committed, aborted)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestTransactionExpiryChecksUseFreshTimeAfterRelevantLocks(t *testing.T) {
	before := time.Date(2026, 8, 16, 21, 0, 0, 0, time.UTC)
	after := before.Add(10 * time.Minute)

	t.Run("generate validates session after quota lock", func(t *testing.T) {
		database, mock, err := sqlmock.New()
		if err != nil {
			t.Fatal(err)
		}
		defer database.Close()
		marker := &atomic.Bool{}
		keys := fixedInvitationKeys(t)
		service, _ := NewService(database, keys, &fakeIntentSource{}, ServiceOptions{Now: lockAwareClock(marker, before, after)})
		sessionHash, _ := keys.HashToken("site_session", []byte("streamer-session"))
		mock.ExpectBegin()
		mock.ExpectQuery(regexp.QuoteMeta("SELECT account_id FROM site_sessions WHERE token_hash = ? AND account_id IS NOT NULL LIMIT 1")).
			WithArgs(sessionHash).WillReturnRows(sqlmock.NewRows([]string{"account_id"}).AddRow(int64(41)))
		mock.ExpectQuery(regexp.QuoteMeta("SELECT credential_epoch, disabled_at FROM streamer_accounts WHERE id = ? FOR UPDATE")).
			WithArgs(int64(41)).WillReturnRows(sqlmock.NewRows([]string{"credential_epoch", "disabled_at"}).AddRow(int64(3), nil))
		mock.ExpectQuery(regexp.QuoteMeta("SELECT id, credential_epoch, expires_at, revoked_at FROM site_sessions WHERE account_id = ? AND token_hash = ? FOR UPDATE")).
			WithArgs(int64(41), sessionHash).WillReturnRows(sqlmock.NewRows([]string{"id", "credential_epoch", "expires_at", "revoked_at"}).AddRow(int64(11), int64(3), before.Add(5*time.Minute), nil))
		mock.ExpectQuery(regexp.QuoteMeta("SELECT remaining_quota FROM invitation_quotas WHERE account_id = ? FOR UPDATE")).
			WithArgs(markingArgument{value: int64(41), marker: marker}).WillReturnRows(sqlmock.NewRows([]string{"remaining_quota"}).AddRow(uint64(1)))
		mock.ExpectRollback()

		_, err = service.Generate(context.Background(), "streamer-session", ActorStreamer)
		if !errors.Is(err, ErrAuthentication) {
			t.Fatalf("Generate() error=%v", err)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("missing quota does not bypass fresh session validation", func(t *testing.T) {
		database, mock, err := sqlmock.New()
		if err != nil {
			t.Fatal(err)
		}
		defer database.Close()
		marker := &atomic.Bool{}
		keys := fixedInvitationKeys(t)
		service, _ := NewService(database, keys, &fakeIntentSource{}, ServiceOptions{Now: lockAwareClock(marker, before, after)})
		sessionHash, _ := keys.HashToken("site_session", []byte("streamer-session"))
		mock.ExpectBegin()
		mock.ExpectQuery(regexp.QuoteMeta("SELECT account_id FROM site_sessions WHERE token_hash = ? AND account_id IS NOT NULL LIMIT 1")).
			WithArgs(sessionHash).WillReturnRows(sqlmock.NewRows([]string{"account_id"}).AddRow(int64(41)))
		mock.ExpectQuery(regexp.QuoteMeta("SELECT credential_epoch, disabled_at FROM streamer_accounts WHERE id = ? FOR UPDATE")).
			WithArgs(int64(41)).WillReturnRows(sqlmock.NewRows([]string{"credential_epoch", "disabled_at"}).AddRow(int64(3), nil))
		mock.ExpectQuery(regexp.QuoteMeta("SELECT id, credential_epoch, expires_at, revoked_at FROM site_sessions WHERE account_id = ? AND token_hash = ? FOR UPDATE")).
			WithArgs(int64(41), sessionHash).WillReturnRows(sqlmock.NewRows([]string{"id", "credential_epoch", "expires_at", "revoked_at"}).AddRow(int64(11), int64(3), before.Add(5*time.Minute), nil))
		mock.ExpectQuery(regexp.QuoteMeta("SELECT remaining_quota FROM invitation_quotas WHERE account_id = ? FOR UPDATE")).
			WithArgs(markingArgument{value: int64(41), marker: marker}).WillReturnError(sql.ErrNoRows)
		mock.ExpectRollback()

		_, err = service.Generate(context.Background(), "streamer-session", ActorStreamer)
		if !errors.Is(err, ErrAuthentication) {
			t.Fatalf("Generate(missing quota) error=%v", err)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("quota adjustment does not consult recent TOTP renewal", func(t *testing.T) {
		database, mock, err := sqlmock.New()
		if err != nil {
			t.Fatal(err)
		}
		defer database.Close()
		marker := &atomic.Bool{}
		keys := fixedInvitationKeys(t)
		authorizer := &invitationSensitiveAuthorizer{renewErr: security.ErrSensitiveRecentTOTPRequired}
		service, _ := NewService(database, keys, &fakeIntentSource{}, ServiceOptions{Now: lockAwareClock(marker, before, after), Administrator: authorizer})
		mock.ExpectBegin()
		mock.ExpectQuery(regexp.QuoteMeta("SELECT credential_epoch FROM streamer_accounts WHERE id = ? FOR UPDATE")).
			WithArgs(int64(41)).WillReturnRows(sqlmock.NewRows([]string{"credential_epoch"}).AddRow(int64(3)))
		mock.ExpectExec(regexp.QuoteMeta("INSERT INTO invitation_quotas (account_id, remaining_quota, updated_at) VALUES (?, 0, CURRENT_TIMESTAMP(6)) ON DUPLICATE KEY UPDATE account_id = account_id")).
			WithArgs(int64(41)).WillReturnResult(sqlmock.NewResult(0, 0))
		mock.ExpectQuery(regexp.QuoteMeta("SELECT remaining_quota FROM invitation_quotas WHERE account_id = ? FOR UPDATE")).
			WithArgs(markingArgument{value: int64(41), marker: marker}).WillReturnRows(sqlmock.NewRows([]string{"remaining_quota"}).AddRow(uint64(2)))
		mock.ExpectExec(regexp.QuoteMeta("UPDATE invitation_quotas SET remaining_quota = ?, updated_at = ? WHERE account_id = ?")).
			WithArgs(uint64(5), before, int64(41)).WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectExec(regexp.QuoteMeta("INSERT INTO invitation_quota_events (account_id, actor_admin_identity_id, quota_delta, quota_after, reason, created_at) VALUES (?, 1, ?, ?, ?, ?)")).
			WithArgs(int64(41), int64(3), uint64(5), "support grant", before).WillReturnResult(sqlmock.NewResult(95, 1))
		mock.ExpectExec(regexp.QuoteMeta("INSERT INTO audit_events (event_type, actor_admin_identity_id, target_account_id, event_data, created_at) VALUES (?, 1, ?, ?, ?)")).
			WithArgs("invitation_quota_adjusted", int64(41), secretFreeJSON{}, before).WillReturnResult(sqlmock.NewResult(96, 1))
		mock.ExpectCommit()

		_, err = service.AdjustQuota(context.Background(), "admin-session", 41, 5, "support grant")
		if err != nil {
			t.Fatalf("AdjustQuota() error=%v", err)
		}
		if authorizer.renewCalls != 0 {
			t.Fatalf("renew calls=%d, want zero", authorizer.renewCalls)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("missing target account fails after transaction authorization without renewal", func(t *testing.T) {
		database, mock, err := sqlmock.New()
		if err != nil {
			t.Fatal(err)
		}
		defer database.Close()
		marker := &atomic.Bool{}
		keys := fixedInvitationKeys(t)
		authorizer := &invitationSensitiveAuthorizer{}
		service, _ := NewService(database, keys, &fakeIntentSource{}, ServiceOptions{Now: lockAwareClock(marker, before, after), Administrator: authorizer})
		mock.ExpectBegin()
		mock.ExpectQuery(regexp.QuoteMeta("SELECT credential_epoch FROM streamer_accounts WHERE id = ? FOR UPDATE")).
			WithArgs(markingArgument{value: int64(41), marker: marker}).WillReturnError(sql.ErrNoRows)
		mock.ExpectRollback()

		_, err = service.AdjustQuota(context.Background(), "admin-session", 41, 5, "support grant")
		if !errors.Is(err, ErrInvitationInvalid) {
			t.Fatalf("AdjustQuota(missing target) error=%v, want ErrInvitationInvalid", err)
		}
		if authorizer.renewCalls != 0 {
			t.Fatalf("missing target renewed %d times", authorizer.renewCalls)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("revoke validates invitation after its row lock", func(t *testing.T) {
		database, mock, err := sqlmock.New()
		if err != nil {
			t.Fatal(err)
		}
		defer database.Close()
		marker := &atomic.Bool{}
		keys := fixedInvitationKeys(t)
		service, _ := NewService(database, keys, &fakeIntentSource{}, ServiceOptions{Now: lockAwareClock(marker, before, after)})
		sessionHash, _ := keys.HashToken("site_session", []byte("streamer-session"))
		mock.ExpectBegin()
		expectStreamerAuthorization(mock, sessionHash, 41, 3, after)
		mock.ExpectQuery(regexp.QuoteMeta("SELECT status, expires_at FROM invitations WHERE id = ? AND creator_account_id = ? FOR UPDATE")).
			WithArgs(markingArgument{value: int64(73), marker: marker}, int64(41)).
			WillReturnRows(sqlmock.NewRows([]string{"status", "expires_at"}).AddRow(StatusActive, before.Add(5*time.Minute)))
		mock.ExpectRollback()

		err = service.Revoke(context.Background(), "streamer-session", 73)
		if !errors.Is(err, ErrInvitationInvalid) {
			t.Fatalf("Revoke() error=%v", err)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("missing invitation does not bypass fresh session validation", func(t *testing.T) {
		database, mock, err := sqlmock.New()
		if err != nil {
			t.Fatal(err)
		}
		defer database.Close()
		marker := &atomic.Bool{}
		keys := fixedInvitationKeys(t)
		service, _ := NewService(database, keys, &fakeIntentSource{}, ServiceOptions{Now: lockAwareClock(marker, before, after)})
		sessionHash, _ := keys.HashToken("site_session", []byte("streamer-session"))
		mock.ExpectBegin()
		expectStreamerAuthorization(mock, sessionHash, 41, 3, before.Add(-55*time.Minute))
		mock.ExpectQuery(regexp.QuoteMeta("SELECT status, expires_at FROM invitations WHERE id = ? AND creator_account_id = ? FOR UPDATE")).
			WithArgs(markingArgument{value: int64(73), marker: marker}, int64(41)).WillReturnError(sql.ErrNoRows)
		mock.ExpectRollback()

		err = service.Revoke(context.Background(), "streamer-session", 73)
		if !errors.Is(err, ErrAuthentication) {
			t.Fatalf("Revoke(missing invitation) error=%v", err)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("expire observes boundary reached after invitation lock", func(t *testing.T) {
		database, mock, err := sqlmock.New()
		if err != nil {
			t.Fatal(err)
		}
		defer database.Close()
		marker := &atomic.Bool{}
		service, _ := NewService(database, fixedInvitationKeys(t), &fakeIntentSource{}, ServiceOptions{Now: lockAwareClock(marker, before, after)})
		mock.ExpectBegin()
		mock.ExpectQuery(regexp.QuoteMeta("SELECT status, expires_at FROM invitations WHERE id = ? FOR UPDATE")).
			WithArgs(markingArgument{value: int64(74), marker: marker}).
			WillReturnRows(sqlmock.NewRows([]string{"status", "expires_at"}).AddRow(StatusActive, after))
		mock.ExpectExec(regexp.QuoteMeta("UPDATE invitations SET status = 'expired', code_ciphertext = NULL WHERE id = ? AND status = 'active'")).
			WithArgs(int64(74)).WillReturnResult(sqlmock.NewResult(0, 1))
		mock.ExpectExec(regexp.QuoteMeta("INSERT INTO audit_events (event_type, event_data, created_at) VALUES (?, ?, ?)")).
			WithArgs("invitation_expired", secretFreeJSON{}, after).WillReturnResult(sqlmock.NewResult(94, 1))
		mock.ExpectCommit()

		if err := service.Expire(context.Background(), 74); err != nil {
			t.Fatalf("Expire() error=%v", err)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("list validates session after session row lock", func(t *testing.T) {
		database, mock, err := sqlmock.New()
		if err != nil {
			t.Fatal(err)
		}
		defer database.Close()
		marker := &atomic.Bool{}
		keys := fixedInvitationKeys(t)
		service, _ := NewService(database, keys, &fakeIntentSource{}, ServiceOptions{Now: lockAwareClock(marker, before, after)})
		sessionHash, _ := keys.HashToken("site_session", []byte("streamer-session"))
		mock.ExpectBegin()
		mock.ExpectQuery(regexp.QuoteMeta("SELECT account_id FROM site_sessions WHERE token_hash = ? AND account_id IS NOT NULL LIMIT 1")).
			WithArgs(sessionHash).WillReturnRows(sqlmock.NewRows([]string{"account_id"}).AddRow(int64(41)))
		mock.ExpectQuery(regexp.QuoteMeta("SELECT credential_epoch, disabled_at FROM streamer_accounts WHERE id = ? FOR UPDATE")).
			WithArgs(int64(41)).WillReturnRows(sqlmock.NewRows([]string{"credential_epoch", "disabled_at"}).AddRow(int64(3), nil))
		mock.ExpectQuery(regexp.QuoteMeta("SELECT id, credential_epoch, expires_at, revoked_at FROM site_sessions WHERE account_id = ? AND token_hash = ? FOR UPDATE")).
			WithArgs(int64(41), markingArgument{value: sessionHash, marker: marker}).
			WillReturnRows(sqlmock.NewRows([]string{"id", "credential_epoch", "expires_at", "revoked_at"}).AddRow(int64(11), int64(3), before.Add(5*time.Minute), nil))
		mock.ExpectRollback()

		_, err = service.List(context.Background(), "streamer-session")
		if !errors.Is(err, ErrAuthentication) {
			t.Fatalf("List() error=%v", err)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("redeem validates invitation and intent after UID lock", func(t *testing.T) {
		database, mock, err := sqlmock.New()
		if err != nil {
			t.Fatal(err)
		}
		defer database.Close()
		marker := &atomic.Bool{}
		reservation := newFakeReservation(after.Add(time.Hour))
		service, _ := NewService(database, fixedInvitationKeys(t), &fakeIntentSource{reservations: map[string]*fakeReservation{"registration-intent": reservation}}, ServiceOptions{Now: lockAwareClock(marker, before, after)})
		codeHash := sha256.Sum256([]byte("complete-invitation-code"))
		mock.ExpectBegin()
		mock.ExpectQuery(regexp.QuoteMeta("SELECT id, status, expires_at FROM invitations WHERE code_hash = ? FOR UPDATE")).
			WithArgs(codeHash[:]).WillReturnRows(sqlmock.NewRows([]string{"id", "status", "expires_at"}).AddRow(int64(75), StatusActive, before.Add(5*time.Minute)))
		mock.ExpectQuery(regexp.QuoteMeta("SELECT account_id FROM bili_uid_bindings WHERE uid_lookup = ? LIMIT 1 FOR UPDATE")).
			WithArgs(markingArgument{value: reservation.uid.Lookup, marker: marker}).WillReturnError(sql.ErrNoRows)
		mock.ExpectRollback()

		_, err = service.Redeem(context.Background(), "complete-invitation-code", "registration-intent")
		committed, aborted := reservation.outcome()
		if !errors.Is(err, ErrInvitationInvalid) || committed || !aborted {
			t.Fatalf("Redeem() error=%v committed=%v aborted=%v", err, committed, aborted)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatal(err)
		}
	})
}

func TestRedeemRechecksInvitationExpiryImmediatelyBeforeCommit(t *testing.T) {
	lockedAt := time.Date(2026, 8, 16, 21, 30, 0, 0, time.UTC)
	commitAt := lockedAt.Add(10 * time.Minute)
	database, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	reservation := newFakeReservation(commitAt.Add(time.Hour))
	service, _ := NewService(database, fixedInvitationKeys(t), &fakeIntentSource{reservations: map[string]*fakeReservation{"registration-intent": reservation}}, ServiceOptions{Now: steppedClock(lockedAt, commitAt)})
	expectRedeemThroughAuditWithExpiry(mock, reservation, lockedAt, "complete-invitation-code", lockedAt.Add(5*time.Minute), nil)
	mock.ExpectRollback()

	_, err = service.Redeem(context.Background(), "complete-invitation-code", "registration-intent")
	committed, aborted := reservation.outcome()
	if !errors.Is(err, ErrInvitationInvalid) || committed || !aborted {
		t.Fatalf("Redeem() error=%v committed=%v aborted=%v", err, committed, aborted)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestConcurrentRedeemHasExactlyOneWinner(t *testing.T) {
	now := time.Date(2026, 8, 16, 19, 10, 0, 0, time.UTC)
	database, mock, err := sqlmock.New(sqlmock.MonitorPingsOption(true))
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	mock.MatchExpectationsInOrder(false)
	first := newFakeReservation(now.Add(5 * time.Minute))
	second := newFakeReservation(now.Add(5 * time.Minute))
	intents := &fakeIntentSource{reservations: map[string]*fakeReservation{"intent-one": first, "intent-two": second}}
	service, _ := NewService(database, fixedInvitationKeys(t), intents, ServiceOptions{Now: fixedNow(now)})
	codeHash := sha256.Sum256([]byte("one-code"))

	mock.ExpectBegin()
	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta("SELECT id, status, expires_at FROM invitations WHERE code_hash = ? FOR UPDATE")).WithArgs(codeHash[:]).
		WillReturnRows(sqlmock.NewRows([]string{"id", "status", "expires_at"}).AddRow(int64(88), StatusActive, now.Add(time.Hour)))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT id, status, expires_at FROM invitations WHERE code_hash = ? FOR UPDATE")).WithArgs(codeHash[:]).
		WillReturnRows(sqlmock.NewRows([]string{"id", "status", "expires_at"}).AddRow(int64(88), StatusUsed, now.Add(time.Hour)))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT account_id FROM bili_uid_bindings WHERE uid_lookup = ? LIMIT 1 FOR UPDATE")).
		WithArgs(first.uid.Lookup).WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery(regexp.QuoteMeta("SELECT account_id FROM bili_uid_bindings WHERE uid_lookup = ? LIMIT 1 FOR UPDATE")).
		WithArgs(first.uid.Lookup).WillReturnError(sql.ErrNoRows)
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO streamer_accounts (credential_epoch, created_at, updated_at) VALUES (1, ?, ?)")).
		WithArgs(now, now).WillReturnResult(sqlmock.NewResult(52, 1))
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO bili_uid_bindings (account_id, uid_ciphertext, uid_lookup, bound_at) VALUES (?, ?, ?, ?)")).
		WithArgs(int64(52), first.uid.Ciphertext, first.uid.Lookup, now).WillReturnResult(sqlmock.NewResult(62, 1))
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO invitation_quotas (account_id, remaining_quota, updated_at) VALUES (?, 0, ?)")).
		WithArgs(int64(52), now).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO site_sessions (account_id, token_hash, credential_epoch, created_at, expires_at) VALUES (?, ?, 1, ?, ?)")).
		WithArgs(int64(52), sqlmock.AnyArg(), now, now.Add(24*time.Hour)).WillReturnResult(sqlmock.NewResult(103, 1))
	mock.ExpectExec(regexp.QuoteMeta("UPDATE invitations SET status = 'used', used_at = ?, invited_account_id = ?, code_ciphertext = NULL WHERE id = ? AND status = 'active'")).
		WithArgs(now, int64(52), int64(88)).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO audit_events (event_type, target_account_id, event_data, created_at) VALUES (?, ?, ?, ?)")).
		WithArgs("invitation_redeemed", int64(52), sqlmock.AnyArg(), now).WillReturnResult(sqlmock.NewResult(104, 1))
	mock.ExpectCommit()
	mock.ExpectRollback()

	type outcome struct{ err error }
	outcomes := make(chan outcome, 2)
	for _, intent := range []string{"intent-one", "intent-two"} {
		go func(intent string) {
			_, err := service.Redeem(context.Background(), "one-code", intent)
			outcomes <- outcome{err: err}
		}(intent)
	}
	winners := 0
	for range 2 {
		result := <-outcomes
		if result.err == nil {
			winners++
		} else if !errors.Is(result.err, ErrInvitationInvalid) {
			t.Fatalf("loser error=%v", result.err)
		}
	}
	if winners != 1 {
		t.Fatalf("redeem winners=%d", winners)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestInsertAuditMapsNamedActorAndTargetWithoutPositionalAmbiguity(t *testing.T) {
	now := time.Date(2026, 8, 16, 22, 0, 0, 0, time.UTC)
	tests := []struct {
		name  string
		event auditEvent
		query string
		args  []driver.Value
	}{
		{
			name:  "streamer actor",
			event: auditEvent{eventType: "invitation_generated", actor: streamerAuditActor(41), invitationID: 71},
			query: "INSERT INTO audit_events (event_type, actor_account_id, event_data, created_at) VALUES (?, ?, ?, ?)",
			args:  []driver.Value{"invitation_generated", int64(41), secretFreeJSON{}, now},
		},
		{
			name:  "administrator actor and streamer target",
			event: auditEvent{eventType: "invitation_quota_adjusted", actor: administratorAuditActor(), targetAccountID: 51},
			query: "INSERT INTO audit_events (event_type, actor_admin_identity_id, target_account_id, event_data, created_at) VALUES (?, 1, ?, ?, ?)",
			args:  []driver.Value{"invitation_quota_adjusted", int64(51), secretFreeJSON{}, now},
		},
		{
			name:  "streamer target without actor",
			event: auditEvent{eventType: "invitation_redeemed", targetAccountID: 61, invitationID: 75},
			query: "INSERT INTO audit_events (event_type, target_account_id, event_data, created_at) VALUES (?, ?, ?, ?)",
			args:  []driver.Value{"invitation_redeemed", int64(61), secretFreeJSON{}, now},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			database, mock, err := sqlmock.New()
			if err != nil {
				t.Fatal(err)
			}
			defer database.Close()
			mock.ExpectBegin()
			transaction, err := database.BeginTx(context.Background(), nil)
			if err != nil {
				t.Fatal(err)
			}
			expectation := mock.ExpectExec(regexp.QuoteMeta(test.query))
			expectation.WithArgs(test.args...).WillReturnResult(sqlmock.NewResult(1, 1))
			mock.ExpectCommit()
			if err := insertAudit(context.Background(), transaction, test.event, now); err != nil {
				t.Fatalf("insertAudit() error=%v", err)
			}
			if err := transaction.Commit(); err != nil {
				t.Fatal(err)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func expectStreamerAuthorization(mock sqlmock.Sqlmock, tokenHash []byte, accountID, epoch int64, now time.Time) {
	mock.ExpectQuery(regexp.QuoteMeta("SELECT account_id FROM site_sessions WHERE token_hash = ? AND account_id IS NOT NULL LIMIT 1")).
		WithArgs(tokenHash).WillReturnRows(sqlmock.NewRows([]string{"account_id"}).AddRow(accountID))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT credential_epoch, disabled_at FROM streamer_accounts WHERE id = ? FOR UPDATE")).
		WithArgs(accountID).WillReturnRows(sqlmock.NewRows([]string{"credential_epoch", "disabled_at"}).AddRow(epoch, nil))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT id, credential_epoch, expires_at, revoked_at FROM site_sessions WHERE account_id = ? AND token_hash = ? FOR UPDATE")).
		WithArgs(accountID, tokenHash).WillReturnRows(sqlmock.NewRows([]string{"id", "credential_epoch", "expires_at", "revoked_at"}).
		AddRow(int64(11), epoch, now.Add(time.Hour), nil))
}

func fixedInvitationKeys(t *testing.T) security.Keyring {
	t.Helper()
	keys, err := security.NewKeyring(1, bytes.Repeat([]byte{0x31}, 32), bytes.Repeat([]byte{0x72}, 32))
	if err != nil {
		t.Fatal(err)
	}
	return keys
}

func fixedNow(value time.Time) func() time.Time { return func() time.Time { return value } }

func steppedClock(values ...time.Time) func() time.Time {
	var mu sync.Mutex
	index := 0
	return func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		value := values[index]
		if index < len(values)-1 {
			index++
		}
		return value
	}
}

func expectRedeemThroughAudit(mock sqlmock.Sqlmock, reservation *fakeReservation, now time.Time, code string, auditError error) {
	expectRedeemThroughAuditWithExpiry(mock, reservation, now, code, now.Add(time.Hour), auditError)
}

func expectRedeemThroughAuditWithExpiry(mock sqlmock.Sqlmock, reservation *fakeReservation, now time.Time, code string, invitationExpiresAt time.Time, auditError error) {
	codeHash := sha256.Sum256([]byte(code))
	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta("SELECT id, status, expires_at FROM invitations WHERE code_hash = ? FOR UPDATE")).
		WithArgs(codeHash[:]).WillReturnRows(sqlmock.NewRows([]string{"id", "status", "expires_at"}).AddRow(int64(75), StatusActive, invitationExpiresAt))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT account_id FROM bili_uid_bindings WHERE uid_lookup = ? LIMIT 1 FOR UPDATE")).
		WithArgs(reservation.uid.Lookup).WillReturnError(sql.ErrNoRows)
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO streamer_accounts (credential_epoch, created_at, updated_at) VALUES (1, ?, ?)")).
		WithArgs(now, now).WillReturnResult(sqlmock.NewResult(51, 1))
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO bili_uid_bindings (account_id, uid_ciphertext, uid_lookup, bound_at) VALUES (?, ?, ?, ?)")).
		WithArgs(int64(51), reservation.uid.Ciphertext, reservation.uid.Lookup, now).WillReturnResult(sqlmock.NewResult(61, 1))
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO invitation_quotas (account_id, remaining_quota, updated_at) VALUES (?, 0, ?)")).
		WithArgs(int64(51), now).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO site_sessions (account_id, token_hash, credential_epoch, created_at, expires_at) VALUES (?, ?, 1, ?, ?)")).
		WithArgs(int64(51), hashOnlyArgument{forbidden: code}, now, now.Add(24*time.Hour)).WillReturnResult(sqlmock.NewResult(101, 1))
	mock.ExpectExec(regexp.QuoteMeta("UPDATE invitations SET status = 'used', used_at = ?, invited_account_id = ?, code_ciphertext = NULL WHERE id = ? AND status = 'active'")).
		WithArgs(now, int64(51), int64(75)).WillReturnResult(sqlmock.NewResult(0, 1))
	audit := mock.ExpectExec(regexp.QuoteMeta("INSERT INTO audit_events (event_type, target_account_id, event_data, created_at) VALUES (?, ?, ?, ?)")).
		WithArgs("invitation_redeemed", int64(51), secretFreeJSON{forbidden: []string{code, "encrypted-uid"}}, now)
	if auditError != nil {
		audit.WillReturnError(auditError)
	} else {
		audit.WillReturnResult(sqlmock.NewResult(102, 1))
	}
}

func lockAwareClock(marker *atomic.Bool, before, after time.Time) func() time.Time {
	return func() time.Time {
		if marker.Load() {
			return after
		}
		return before
	}
}

type markingArgument struct {
	value  driver.Value
	marker *atomic.Bool
}

func (argument markingArgument) Match(value driver.Value) bool {
	matched := false
	switch want := argument.value.(type) {
	case []byte:
		got, ok := value.([]byte)
		matched = ok && bytes.Equal(got, want)
	default:
		matched = value == want
	}
	if matched {
		argument.marker.Store(true)
	}
	return matched
}

type invitationSensitiveAuthorizer struct {
	writeMarkers   bool
	authorizeErr   error
	renewErr       error
	authorizeCalls int
	renewCalls     int
	authorizedAt   time.Time
	renewedAt      time.Time
	authorizeTx    *sql.Tx
	renewTx        *sql.Tx
}

type ciphertextCapture struct{ value []byte }

func (capture *ciphertextCapture) Match(value driver.Value) bool {
	bytes, ok := value.([]byte)
	if !ok {
		return false
	}
	capture.value = append([]byte(nil), bytes...)
	return len(bytes) > 0
}

func (authorizer *invitationSensitiveAuthorizer) RequireSession(_ context.Context, _ string) error {
	authorizer.authorizeCalls++
	return authorizer.authorizeErr
}

func (authorizer *invitationSensitiveAuthorizer) AuthorizeRecentTOTP(ctx context.Context, transaction *sql.Tx, _ string, now time.Time) (security.SensitiveSession, error) {
	authorizer.authorizeCalls++
	authorizer.authorizedAt = now
	authorizer.authorizeTx = transaction
	if authorizer.authorizeErr != nil {
		return security.SensitiveSession{}, authorizer.authorizeErr
	}
	if authorizer.writeMarkers {
		if _, err := transaction.ExecContext(ctx, "sensitive_authorize"); err != nil {
			return security.SensitiveSession{}, err
		}
	}
	return security.SensitiveSession{}, nil
}

func (authorizer *invitationSensitiveAuthorizer) RenewRecentTOTP(ctx context.Context, transaction *sql.Tx, _ security.SensitiveSession, now time.Time) error {
	authorizer.renewCalls++
	authorizer.renewedAt = now
	authorizer.renewTx = transaction
	if authorizer.writeMarkers {
		if _, err := transaction.ExecContext(ctx, "sensitive_renew"); err != nil {
			return err
		}
	}
	return authorizer.renewErr
}

type invitationTimeSequence struct {
	values []time.Time
	index  int
}

func (sequence *invitationTimeSequence) Now() time.Time {
	if sequence.index >= len(sequence.values) {
		return sequence.values[len(sequence.values)-1]
	}
	value := sequence.values[sequence.index]
	sequence.index++
	return value
}

type fakeIntentSource struct {
	mu           sync.Mutex
	reservations map[string]*fakeReservation
}

func (source *fakeIntentSource) ReserveRegistrationIntent(token string) (identity.RegistrationIntentReservation, error) {
	source.mu.Lock()
	defer source.mu.Unlock()
	reservation := source.reservations[token]
	if reservation == nil || !reservation.tryReserve() {
		return nil, identity.ErrRegistrationIntentInvalid
	}
	return reservation, nil
}

type fakeReservation struct {
	mu        sync.Mutex
	uid       identity.EncryptedUID
	expiresAt time.Time
	reserved  bool
	valid     bool
	committed bool
	aborted   bool
}

func newFakeReservation(expiresAt time.Time) *fakeReservation {
	return &fakeReservation{
		uid:       identity.EncryptedUID{Ciphertext: []byte("encrypted-uid"), Lookup: bytes.Repeat([]byte{0x77}, sha256.Size)},
		expiresAt: expiresAt, valid: true,
	}
}

func (reservation *fakeReservation) Identity() (identity.EncryptedUID, time.Time, bool) {
	reservation.mu.Lock()
	defer reservation.mu.Unlock()
	return identity.EncryptedUID{Ciphertext: bytes.Clone(reservation.uid.Ciphertext), Lookup: bytes.Clone(reservation.uid.Lookup)}, reservation.expiresAt, reservation.valid && !reservation.committed
}

func (reservation *fakeReservation) Valid() bool {
	reservation.mu.Lock()
	defer reservation.mu.Unlock()
	return reservation.valid && !reservation.committed
}

func (reservation *fakeReservation) Commit() {
	reservation.mu.Lock()
	reservation.committed = true
	reservation.mu.Unlock()
}

func (reservation *fakeReservation) Abort() {
	reservation.mu.Lock()
	if !reservation.committed {
		reservation.aborted = true
		reservation.reserved = false
	}
	reservation.mu.Unlock()
}

func (reservation *fakeReservation) tryReserve() bool {
	reservation.mu.Lock()
	defer reservation.mu.Unlock()
	if reservation.reserved || reservation.committed || !reservation.valid {
		return false
	}
	reservation.reserved = true
	return true
}

func (reservation *fakeReservation) outcome() (bool, bool) {
	reservation.mu.Lock()
	defer reservation.mu.Unlock()
	return reservation.committed, reservation.aborted
}

type hashOnlyArgument struct{ forbidden string }

func (argument hashOnlyArgument) Match(value driver.Value) bool {
	bytesValue, ok := value.([]byte)
	return ok && len(bytesValue) == sha256.Size && !bytes.Contains(bytesValue, []byte(argument.forbidden))
}

type fourCharacterHint struct{}

func (fourCharacterHint) Match(value driver.Value) bool {
	text, ok := value.(string)
	return ok && len(text) == 4
}

type secretFreeJSON struct{ forbidden []string }

func (argument secretFreeJSON) Match(value driver.Value) bool {
	var encoded []byte
	switch typed := value.(type) {
	case []byte:
		encoded = typed
	case string:
		encoded = []byte(typed)
	default:
		return false
	}
	if len(encoded) < 2 || encoded[0] != '{' {
		return false
	}
	for _, forbidden := range argument.forbidden {
		if bytes.Contains(encoded, []byte(forbidden)) {
			return false
		}
	}
	return true
}
