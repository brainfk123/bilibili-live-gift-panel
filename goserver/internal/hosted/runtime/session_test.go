package runtime

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"regexp"
	"slices"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/go-sql-driver/mysql"
)

func TestSessionRepositoryStartsIdentityAndActiveGuardInOneTransaction(t *testing.T) {
	database, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	now := time.Date(2026, 8, 17, 2, 0, 0, 0, time.UTC)
	fence := OwnerFence{AccountID: 7, Token: ownerToken(0x51), Epoch: 1}
	mock.ExpectBegin()
	expectOwnershipAccountLock(mock, 7, true)
	expectOwnerFence(mock, fence, true)
	expectNoOpenSession(mock, 7)
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO live_sessions (account_id, room_id, config_version_id, started_at) SELECT a.id, ?, v.id, ? FROM streamer_accounts AS a JOIN account_config_versions AS v ON v.account_id = a.id AND v.id = ? WHERE a.id = ? AND a.disabled_at IS NULL")).
		WithArgs("42", now, int64(31), int64(7)).WillReturnResult(sqlmock.NewResult(81, 1))
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO runtime_session_identities (live_session_id, account_id) SELECT id, account_id FROM live_sessions WHERE id = ? AND account_id = ?")).
		WithArgs(int64(81), int64(7)).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO runtime_active_session_guards (account_id, live_session_id) SELECT account_id, id FROM live_sessions WHERE id = ? AND account_id = ? AND ended_at IS NULL")).
		WithArgs(int64(81), int64(7)).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	session, err := NewSessionRepository(database).StartSession(context.Background(), StartSessionCommand{Owner: fence, AccountID: 7, RoomID: "42", ConfigVersionID: 31, StartedAt: now, Reconcile: true})
	if err != nil {
		t.Fatal(err)
	}
	if session.ID != 81 || session.AccountID != 7 || session.RoomID != "42" || session.ConfigVersionID != 31 || !session.StartedAt.Equal(now) {
		t.Fatalf("StartSession() = %#v", session)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestSessionRepositoryLinksExecutionToOpenBusinessBroadcast(t *testing.T) {
	database, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	now := time.Date(2026, 8, 26, 2, 0, 0, 0, time.UTC)
	fence := OwnerFence{AccountID: 7, Token: ownerToken(0x55), Epoch: 3}
	mock.ExpectBegin()
	expectOwnershipAccountLock(mock, 7, true)
	expectOwnerFence(mock, fence, true)
	expectNoOpenSession(mock, 7)
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO live_sessions (broadcast_session_id, account_id, room_id, config_version_id, started_at) SELECT b.id, a.id, ?, v.id, ? FROM streamer_accounts AS a JOIN account_config_versions AS v ON v.account_id = a.id AND v.id = ? JOIN broadcast_sessions AS b ON b.id = ? AND b.room_id = ? AND b.ended_at IS NULL WHERE a.id = ? AND a.disabled_at IS NULL")).
		WithArgs("42", now, int64(31), int64(99), "42", int64(7)).WillReturnResult(sqlmock.NewResult(81, 1))
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO runtime_session_identities (live_session_id, account_id) SELECT id, account_id FROM live_sessions WHERE id = ? AND account_id = ?")).
		WithArgs(int64(81), int64(7)).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO runtime_active_session_guards (account_id, live_session_id) SELECT account_id, id FROM live_sessions WHERE id = ? AND account_id = ? AND ended_at IS NULL")).
		WithArgs(int64(81), int64(7)).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	session, err := NewSessionRepository(database).StartSession(context.Background(), StartSessionCommand{Owner: fence, AccountID: 7, RoomID: "42", BroadcastSessionID: 99, ConfigVersionID: 31, StartedAt: now, Reconcile: true})
	if err != nil {
		t.Fatal(err)
	}
	if session.BroadcastSessionID != 99 {
		t.Fatalf("broadcast session ID = %d, want 99", session.BroadcastSessionID)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestSessionRepositoryListsOnlyEnabledAccountsForCanonicalRoom(t *testing.T) {
	database, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	mock.ExpectQuery(regexp.QuoteMeta("SELECT r.account_id FROM account_runtime_rooms AS r JOIN streamer_accounts AS a ON a.id = r.account_id AND a.disabled_at IS NULL WHERE r.room_id = ? ORDER BY r.account_id")).
		WithArgs("42").WillReturnRows(sqlmock.NewRows([]string{"account_id"}).AddRow(int64(7)).AddRow(int64(8)))
	accounts, err := NewSessionRepository(database).EnabledAccountsForRoom(context.Background(), "42")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(accounts, []int64{7, 8}) {
		t.Fatalf("accounts = %v, want [7 8]", accounts)
	}
}

func TestSessionRepositoryPersistsTargetOnlyUnderExactUnexpiredOwnerFence(t *testing.T) {
	fence := OwnerFence{AccountID: 7, Token: ownerToken(0x41), Epoch: 3}
	now := time.Date(2026, 8, 17, 4, 0, 0, 123456789, time.UTC)
	database, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	mock.ExpectBegin()
	expectOwnershipAccountLock(mock, 7, true)
	expectOwnerFence(mock, fence, true)
	mock.ExpectExec("INSERT INTO account_runtime_rooms").WithArgs(int64(7), "42", now.UTC().Truncate(time.Microsecond)).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	command := PersistTargetRoomCommand{Owner: fence, RoomID: "42", UpdatedAt: now}
	if err := NewSessionRepository(database).PersistTargetRoom(context.Background(), command); err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestSessionRepositoryRejectsStaleOwnerBeforePersistStartOrEndMutation(t *testing.T) {
	fence := OwnerFence{AccountID: 7, Token: ownerToken(0x42), Epoch: 8}
	now := time.Date(2026, 8, 17, 4, 5, 0, 0, time.UTC)
	for _, operation := range []string{"persist", "start", "end"} {
		t.Run(operation, func(t *testing.T) {
			database, mock, err := sqlmock.New()
			if err != nil {
				t.Fatal(err)
			}
			defer database.Close()
			mock.ExpectBegin()
			if operation == "end" {
				expectEndAccountLock(mock, 7)
			} else {
				expectOwnershipAccountLock(mock, 7, true)
			}
			expectOwnerFence(mock, fence, false)
			mock.ExpectRollback()
			repository := NewSessionRepository(database)
			switch operation {
			case "persist":
				err = repository.PersistTargetRoom(context.Background(), PersistTargetRoomCommand{Owner: fence, RoomID: "42", UpdatedAt: now})
			case "start":
				_, err = repository.StartSession(context.Background(), StartSessionCommand{Owner: fence, AccountID: 7, RoomID: "42", ConfigVersionID: 31, StartedAt: now})
			case "end":
				err = repository.EndSession(context.Background(), EndSessionCommand{Owner: fence, AccountID: 7, SessionID: 81, EndedAt: now})
			}
			if !errors.Is(err, ErrOwnershipConflict) {
				t.Fatalf("%s error = %v, want ownership conflict", operation, err)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestSessionRepositoryStartWithoutTakeoverNeverReconcilesAnotherOpenLifecycle(t *testing.T) {
	fence := OwnerFence{AccountID: 7, Token: ownerToken(0x43), Epoch: 2}
	now := time.Date(2026, 8, 17, 4, 10, 0, 0, time.UTC)
	database, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	mock.ExpectBegin()
	expectOwnershipAccountLock(mock, 7, true)
	expectOwnerFence(mock, fence, true)
	mock.ExpectExec("INSERT INTO live_sessions").WillReturnError(errors.New("active guard remains"))
	mock.ExpectRollback()
	_, err = NewSessionRepository(database).StartSession(context.Background(), StartSessionCommand{Owner: fence, AccountID: 7, RoomID: "42", ConfigVersionID: 31, StartedAt: now, Reconcile: false})
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("StartSession error = %v, want unavailable", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestSessionRepositoryRollsBackWhenIdentityOrGuardIsNotExactlyOneRow(t *testing.T) {
	for _, failedWrite := range []string{"identity", "guard"} {
		t.Run(failedWrite, func(t *testing.T) {
			database, mock, err := sqlmock.New()
			if err != nil {
				t.Fatal(err)
			}
			defer database.Close()
			now := time.Date(2026, 8, 17, 2, 5, 0, 0, time.UTC)
			fence := OwnerFence{AccountID: 7, Token: ownerToken(0x52), Epoch: 1}
			mock.ExpectBegin()
			expectOwnershipAccountLock(mock, 7, true)
			expectOwnerFence(mock, fence, true)
			expectNoOpenSession(mock, 7)
			mock.ExpectExec("INSERT INTO live_sessions").WillReturnResult(sqlmock.NewResult(82, 1))
			identity := mock.ExpectExec("INSERT INTO runtime_session_identities").WillReturnResult(sqlmock.NewResult(0, 1))
			if failedWrite == "identity" {
				identity.WillReturnResult(sqlmock.NewResult(0, 0))
			} else {
				mock.ExpectExec("INSERT INTO runtime_active_session_guards").WillReturnResult(sqlmock.NewResult(0, 0))
			}
			mock.ExpectRollback()
			_, err = NewSessionRepository(database).StartSession(context.Background(), StartSessionCommand{Owner: fence, AccountID: 7, RoomID: "42", ConfigVersionID: 31, StartedAt: now, Reconcile: true})
			if !errors.Is(err, ErrUnavailable) {
				t.Fatalf("StartSession() error = %v", err)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestSessionRepositoryEndsExactTenantGuardAndLifecycleRow(t *testing.T) {
	database, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	now := time.Date(2026, 8, 17, 2, 10, 0, 0, time.UTC)
	fence := OwnerFence{AccountID: 7, Token: ownerToken(0x53), Epoch: 1}
	mock.ExpectBegin()
	expectEndAccountLock(mock, 7)
	expectOwnerFence(mock, fence, true)
	mock.ExpectExec(regexp.QuoteMeta("DELETE FROM runtime_active_session_guards WHERE account_id = ? AND live_session_id = ?")).
		WithArgs(int64(7), int64(81)).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(regexp.QuoteMeta("UPDATE live_sessions SET ended_at = ? WHERE id = ? AND account_id = ? AND ended_at IS NULL")).
		WithArgs(now, int64(81), int64(7)).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	if err := NewSessionRepository(database).EndSession(context.Background(), EndSessionCommand{Owner: fence, AccountID: 7, SessionID: 81, EndedAt: now}); err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestSessionRepositoryReconcilesPersistedOpenLifecycleBeforeRestartStart(t *testing.T) {
	database, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	now := time.Date(2026, 8, 17, 3, 0, 0, 0, time.UTC)
	fence := OwnerFence{AccountID: 7, Token: ownerToken(0x54), Epoch: 2}
	mock.ExpectBegin()
	expectOwnershipAccountLock(mock, 7, true)
	expectOwnerFence(mock, fence, true)
	mock.ExpectQuery(regexp.QuoteMeta("SELECT id FROM live_sessions WHERE account_id = ? AND ended_at IS NULL ORDER BY id LIMIT 2 FOR UPDATE")).
		WithArgs(int64(7)).WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(int64(70)))
	mock.ExpectExec(regexp.QuoteMeta("DELETE FROM runtime_active_session_guards WHERE account_id = ? AND live_session_id = ?")).
		WithArgs(int64(7), int64(70)).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(regexp.QuoteMeta("UPDATE live_sessions SET ended_at = ? WHERE id = ? AND account_id = ? AND ended_at IS NULL")).
		WithArgs(now, int64(70), int64(7)).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("INSERT INTO live_sessions").WillReturnResult(sqlmock.NewResult(81, 1))
	mock.ExpectExec("INSERT INTO runtime_session_identities").WithArgs(int64(81), int64(7)).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("INSERT INTO runtime_active_session_guards").WithArgs(int64(81), int64(7)).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	session, err := NewSessionRepository(database).StartSession(context.Background(), StartSessionCommand{Owner: fence, AccountID: 7, RoomID: "42", ConfigVersionID: 31, StartedAt: now, Reconcile: true})
	if err != nil || session.ID != 81 {
		t.Fatalf("restart StartSession() = %#v, %v", session, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestManagerRestartReconcilesPersistedOpenSessionBeforeStartingTarget(t *testing.T) {
	database, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	now := time.Date(2026, 8, 17, 3, 2, 0, 0, time.UTC)
	token := ownerToken(0x55)
	bootstrapToken := ownerToken(0x99)
	fence := OwnerFence{AccountID: 7, Token: token, Epoch: 2}
	mock.ExpectBegin()
	expectOwnershipAccountLock(mock, 7, true)
	mock.ExpectQuery("SELECT owner_token, fencing_epoch").WithArgs(int64(7)).WillReturnRows(sqlmock.NewRows([]string{"owner_token", "fencing_epoch", "expired"}).AddRow(bootstrapToken[:], uint64(1), true))
	mock.ExpectExec("UPDATE runtime_account_owners SET owner_token").WithArgs(token[:], defaultOwnerTTL.Microseconds(), int64(7), bootstrapToken[:], uint64(1)).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	mock.ExpectQuery("SELECT room_id FROM account_runtime_rooms").WithArgs(int64(7)).WillReturnRows(sqlmock.NewRows([]string{"room_id"}).AddRow("42"))
	mock.ExpectBegin()
	expectOwnershipAccountLock(mock, 7, true)
	expectOwnerFence(mock, fence, true)
	mock.ExpectQuery("SELECT id FROM live_sessions").WithArgs(int64(7)).WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(int64(70)))
	mock.ExpectExec("DELETE FROM runtime_active_session_guards").WithArgs(int64(7), int64(70)).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("UPDATE live_sessions SET ended_at").WithArgs(now, int64(70), int64(7)).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("INSERT INTO live_sessions").WillReturnResult(sqlmock.NewResult(81, 1))
	mock.ExpectExec("INSERT INTO runtime_session_identities").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("INSERT INTO runtime_active_session_guards").WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	log := &operationLog{}
	sources := newOrderedRoomSources(log, map[string]string{"42": "42"})
	manager, err := NewManager(Dependencies{Sessions: NewSessionRepository(database), Configuration: fakeConfiguration{}, Migration: orderedMigration{log: log}, RoomSources: sources}, Options{Now: func() time.Time { return now }, OwnerToken: token})
	if err != nil {
		t.Fatal(err)
	}
	lease, err := manager.Acquire(context.Background(), 7, LeaseConfig)
	if err != nil {
		t.Fatal(err)
	}
	if status, err := manager.Status(context.Background(), 7); err != nil || status.SessionID != 81 || status.RoomID != "42" || status.State != StateActive {
		t.Fatalf("restart status = %#v, %v", status, err)
	}
	lease.Release()
	mock.ExpectBegin()
	expectEndAccountLock(mock, 7)
	expectOwnerFence(mock, fence, true)
	mock.ExpectExec("DELETE FROM runtime_active_session_guards").WithArgs(int64(7), int64(81)).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("UPDATE live_sessions SET ended_at").WithArgs(now, int64(81), int64(7)).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	mock.ExpectBegin()
	expectEndAccountLock(mock, 7)
	mock.ExpectExec("UPDATE runtime_account_owners SET expires_at = UTC_TIMESTAMP").WithArgs(int64(7), token[:], uint64(2)).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	if err := manager.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestSessionRepositoryStartCommitErrorAcceptsOnlyExactCommittedSessionAndGuard(t *testing.T) {
	now := time.Date(2026, 8, 17, 3, 5, 0, 123456789, time.UTC)
	normalized := now.UTC().Truncate(time.Microsecond)
	fence := OwnerFence{AccountID: 7, Token: ownerToken(0x56), Epoch: 4}
	for _, test := range []struct {
		name        string
		verifiedRow *sqlmock.Rows
		wantSuccess bool
	}{
		{name: "exact", verifiedRow: sqlmock.NewRows([]string{"id", "account_id", "room_id", "config_version_id", "started_at", "live_session_id"}).AddRow(81, 7, "42", 31, normalized, 81), wantSuccess: true},
		{name: "other tenant", verifiedRow: sqlmock.NewRows([]string{"id", "account_id", "room_id", "config_version_id", "started_at", "live_session_id"}).AddRow(81, 8, "42", 31, normalized, 81)},
		{name: "other session shape", verifiedRow: sqlmock.NewRows([]string{"id", "account_id", "room_id", "config_version_id", "started_at", "live_session_id"}).AddRow(81, 7, "43", 31, normalized, 81)},
		{name: "other exact start time", verifiedRow: sqlmock.NewRows([]string{"id", "account_id", "room_id", "config_version_id", "started_at", "live_session_id"}).AddRow(81, 7, "42", 31, normalized.Add(time.Microsecond), 81)},
	} {
		t.Run(test.name, func(t *testing.T) {
			database, mock, err := sqlmock.New()
			if err != nil {
				t.Fatal(err)
			}
			defer database.Close()
			mock.ExpectBegin()
			expectOwnershipAccountLock(mock, 7, true)
			expectOwnerFence(mock, fence, true)
			expectNoOpenSession(mock, 7)
			mock.ExpectExec("INSERT INTO live_sessions").WillReturnResult(sqlmock.NewResult(81, 1))
			mock.ExpectExec("INSERT INTO runtime_session_identities").WillReturnResult(sqlmock.NewResult(0, 1))
			mock.ExpectExec("INSERT INTO runtime_active_session_guards").WillReturnResult(sqlmock.NewResult(0, 1))
			mock.ExpectCommit().WillReturnError(errors.New("ambiguous commit"))
			mock.ExpectQuery("SELECT s.id, s.account_id, s.room_id, s.config_version_id, s.started_at, g.live_session_id FROM live_sessions AS s JOIN runtime_active_session_guards AS g").
				WithArgs(int64(81), int64(7), "42", int64(31), normalized, fence.Token[:], fence.Epoch).WillReturnRows(test.verifiedRow)

			session, err := NewSessionRepository(database).StartSession(context.Background(), StartSessionCommand{Owner: fence, AccountID: 7, RoomID: "42", ConfigVersionID: 31, StartedAt: now, Reconcile: true})
			if test.wantSuccess {
				if err != nil || session.ID != 81 || session.AccountID != 7 {
					t.Fatalf("StartSession() = %#v, %v", session, err)
				}
			} else if !errors.Is(err, ErrUnavailable) {
				t.Fatalf("StartSession() error = %v, want unavailable", err)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestSessionRepositoryAmbiguousCommitVerificationHasIndependentBoundedContext(t *testing.T) {
	repository := &SessionRepository{verificationTimeout: 250 * time.Millisecond}
	caller, cancelCaller := context.WithCancel(context.Background())
	cancelCaller()
	verification, cancelVerification := repository.verificationContext()
	defer cancelVerification()
	if caller.Err() == nil {
		t.Fatal("caller context was not cancelled")
	}
	if verification.Err() != nil {
		t.Fatalf("verification inherited caller cancellation: %v", verification.Err())
	}
	deadline, ok := verification.Deadline()
	if !ok || time.Until(deadline) <= 0 || time.Until(deadline) > time.Second {
		t.Fatalf("verification deadline = %v, %v", deadline, ok)
	}
}

func TestSessionRepositoryEndIsIdempotentOnlyForExactEndedSessionWithoutGuard(t *testing.T) {
	now := time.Date(2026, 8, 17, 3, 10, 0, 0, time.UTC)
	fence := OwnerFence{AccountID: 7, Token: ownerToken(0x57), Epoch: 5}
	for _, test := range []struct {
		name        string
		verifiedRow *sqlmock.Rows
		wantSuccess bool
	}{
		{name: "exact ended", verifiedRow: sqlmock.NewRows([]string{"ended_at", "guard_absent", "owner_active"}).AddRow(now, true, true), wantSuccess: true},
		{name: "still active", verifiedRow: sqlmock.NewRows([]string{"ended_at", "guard_absent", "owner_active"}).AddRow(now, false, true)},
		{name: "other end time", verifiedRow: sqlmock.NewRows([]string{"ended_at", "guard_absent", "owner_active"}).AddRow(now.Add(time.Microsecond), true, true)},
		{name: "other tenant or session", verifiedRow: sqlmock.NewRows([]string{"ended_at", "guard_absent", "owner_active"})},
	} {
		t.Run(test.name, func(t *testing.T) {
			database, mock, err := sqlmock.New()
			if err != nil {
				t.Fatal(err)
			}
			defer database.Close()
			mock.ExpectBegin()
			expectEndAccountLock(mock, 7)
			expectOwnerFence(mock, fence, true)
			mock.ExpectExec("DELETE FROM runtime_active_session_guards").WithArgs(int64(7), int64(81)).WillReturnResult(sqlmock.NewResult(0, 0))
			mock.ExpectQuery("SELECT ended_at, NOT EXISTS").WithArgs(int64(7), int64(81), int64(7), fence.Token[:], fence.Epoch, int64(81), int64(7), now).WillReturnRows(test.verifiedRow)
			mock.ExpectRollback()

			err = NewSessionRepository(database).EndSession(context.Background(), EndSessionCommand{Owner: fence, AccountID: 7, SessionID: 81, EndedAt: now})
			if test.wantSuccess {
				if err != nil {
					t.Fatalf("EndSession() error = %v", err)
				}
			} else if !errors.Is(err, ErrUnavailable) {
				t.Fatalf("EndSession() error = %v, want unavailable", err)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestSessionRepositoryEndCommitErrorVerifiesExactEndedSessionAndMissingGuard(t *testing.T) {
	database, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	now := time.Date(2026, 8, 17, 3, 15, 0, 0, time.UTC)
	fence := OwnerFence{AccountID: 7, Token: ownerToken(0x58), Epoch: 6}
	mock.ExpectBegin()
	expectEndAccountLock(mock, 7)
	expectOwnerFence(mock, fence, true)
	mock.ExpectExec("DELETE FROM runtime_active_session_guards").WithArgs(int64(7), int64(81)).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec("UPDATE live_sessions SET ended_at").WithArgs(now, int64(81), int64(7)).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit().WillReturnError(errors.New("ambiguous commit"))
	mock.ExpectQuery("SELECT ended_at, NOT EXISTS").WithArgs(int64(7), int64(81), int64(7), fence.Token[:], fence.Epoch, int64(81), int64(7), now).WillReturnRows(sqlmock.NewRows([]string{"ended_at", "guard_absent", "owner_active"}).AddRow(now, true, true))

	if err := NewSessionRepository(database).EndSession(context.Background(), EndSessionCommand{Owner: fence, AccountID: 7, SessionID: 81, EndedAt: now}); err != nil {
		t.Fatalf("EndSession() error = %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func expectStartAccountLock(mock sqlmock.Sqlmock, accountID int64) {
	mock.ExpectQuery(regexp.QuoteMeta("SELECT id FROM streamer_accounts WHERE id = ? AND disabled_at IS NULL FOR UPDATE")).
		WithArgs(accountID).WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(accountID))
}

func expectEndAccountLock(mock sqlmock.Sqlmock, accountID int64) {
	mock.ExpectQuery(regexp.QuoteMeta("SELECT id FROM streamer_accounts WHERE id = ? FOR UPDATE")).
		WithArgs(accountID).WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(accountID))
}

func expectNoOpenSession(mock sqlmock.Sqlmock, accountID int64) {
	mock.ExpectQuery(regexp.QuoteMeta("SELECT id FROM live_sessions WHERE account_id = ? AND ended_at IS NULL ORDER BY id LIMIT 2 FOR UPDATE")).
		WithArgs(accountID).WillReturnRows(sqlmock.NewRows([]string{"id"}))
}

func expectOwnerFence(mock sqlmock.Sqlmock, fence OwnerFence, active bool) {
	query := mock.ExpectQuery(regexp.QuoteMeta("SELECT expires_at > UTC_TIMESTAMP(6) FROM runtime_account_owners WHERE account_id = ? AND owner_token = ? AND fencing_epoch = ? FOR UPDATE")).
		WithArgs(fence.AccountID, fence.Token[:], fence.Epoch)
	if active {
		query.WillReturnRows(sqlmock.NewRows([]string{"active"}).AddRow(true))
	} else {
		query.WillReturnRows(sqlmock.NewRows([]string{"active"}))
	}
}

func TestSessionRepositoryAggregateAndDedupeRequireTrustedSessionTenantPair(t *testing.T) {
	database, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	now := time.Date(2026, 8, 17, 2, 20, 0, 0, time.UTC)
	fence := OwnerFence{AccountID: 7, Token: ownerToken(0x61), Epoch: 9}
	aggregate := json.RawMessage(`{"health":9}`)
	mock.ExpectBegin()
	expectEndAccountLock(mock, 7)
	expectOwnerFence(mock, fence, true)
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO runtime_session_aggregates (live_session_id, account_id, aggregate_json, updated_at) SELECT live_session_id, account_id, ?, ? FROM runtime_session_identities WHERE live_session_id = ? AND account_id = ? ON DUPLICATE KEY UPDATE aggregate_json = VALUES(aggregate_json), updated_at = VALUES(updated_at)")).
		WithArgs(aggregate, now, int64(81), int64(7)).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	repository := NewSessionRepository(database)
	if err := repository.WriteAggregate(context.Background(), AggregateCommand{Owner: fence, AccountID: 7, SessionID: 81, AggregateJSON: aggregate, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}

	hash := sha256.Sum256([]byte("stable-event"))
	expires := now.Add(24 * time.Hour)
	mock.ExpectBegin()
	expectEndAccountLock(mock, 7)
	expectOwnerFence(mock, fence, true)
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO runtime_event_dedup_receipts (account_id, live_session_id, event_hash, created_at, expires_at) SELECT account_id, live_session_id, ?, ?, ? FROM runtime_session_identities WHERE live_session_id = ? AND account_id = ?")).
		WithArgs(hash[:], now, expires, int64(81), int64(7)).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	inserted, err := repository.RecordStableEvent(context.Background(), StableEventCommand{Owner: fence, AccountID: 7, SessionID: 81, EventHash: hash, CreatedAt: now, ExpiresAt: expires})
	if err != nil || !inserted {
		t.Fatalf("RecordStableEvent() = %v, %v", inserted, err)
	}
	mock.ExpectBegin()
	expectEndAccountLock(mock, 7)
	expectOwnerFence(mock, fence, true)
	mock.ExpectExec("INSERT INTO runtime_event_dedup_receipts").WillReturnError(&mysql.MySQLError{Number: 1062, Message: "duplicate"})
	mock.ExpectRollback()
	inserted, err = repository.RecordStableEvent(context.Background(), StableEventCommand{Owner: fence, AccountID: 7, SessionID: 81, EventHash: hash, CreatedAt: now, ExpiresAt: expires})
	if err != nil || inserted {
		t.Fatalf("duplicate RecordStableEvent() = %v, %v", inserted, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestSessionRepositoryStaleOwnerCannotWriteAggregateOrDedupe(t *testing.T) {
	fence := OwnerFence{AccountID: 7, Token: ownerToken(0x62), Epoch: 10}
	now := time.Date(2026, 8, 17, 2, 25, 0, 0, time.UTC)
	for _, operation := range []string{"aggregate", "dedupe"} {
		t.Run(operation, func(t *testing.T) {
			database, mock, err := sqlmock.New()
			if err != nil {
				t.Fatal(err)
			}
			defer database.Close()
			mock.ExpectBegin()
			expectEndAccountLock(mock, 7)
			expectOwnerFence(mock, fence, false)
			mock.ExpectRollback()
			repository := NewSessionRepository(database)
			if operation == "aggregate" {
				err = repository.WriteAggregate(context.Background(), AggregateCommand{Owner: fence, AccountID: 7, SessionID: 81, AggregateJSON: json.RawMessage(`{"health":9}`), UpdatedAt: now})
			} else {
				_, err = repository.RecordStableEvent(context.Background(), StableEventCommand{Owner: fence, AccountID: 7, SessionID: 81, EventHash: sha256.Sum256([]byte("stale")), CreatedAt: now, ExpiresAt: now.Add(time.Hour)})
			}
			if !errors.Is(err, ErrOwnershipConflict) {
				t.Fatalf("%s error = %v, want ownership conflict", operation, err)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}
