package configuration

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"database/sql/driver"
	"encoding/hex"
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"testing"
	"time"

	"bilibili-live-gift-panel/internal/gameplay"
	"github.com/DATA-DOG/go-sqlmock"
	"github.com/go-sql-driver/mysql"
)

func TestRepositoryCommitRuntimeEventCleansBoundedExpiredReceiptsReusesExpiredHashAndPersistsStateAggregate(t *testing.T) {
	repository, mock, closeDB := newMockRepository(t)
	defer closeDB()
	now := time.Date(2026, 8, 17, 12, 0, 0, 123456000, time.UTC)
	hash := sha256.Sum256([]byte("stable-event"))
	runtime := processorRuntimeFixture()

	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta("SELECT disabled_at IS NULL FROM streamer_accounts WHERE id = ? FOR UPDATE")).
		WithArgs(int64(7)).WillReturnRows(sqlmock.NewRows([]string{"enabled"}).AddRow(true))
	mock.ExpectExec(regexp.QuoteMeta("DELETE FROM runtime_event_dedup_receipts WHERE account_id = ? AND expires_at <= UTC_TIMESTAMP(6) ORDER BY expires_at, event_hash LIMIT 100")).
		WithArgs(int64(7)).WillReturnResult(sqlmock.NewResult(0, 100))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT owner_token, fencing_epoch, expires_at > UTC_TIMESTAMP(6) FROM runtime_account_owners WHERE account_id = ? FOR UPDATE")).
		WithArgs(int64(7)).WillReturnRows(sqlmock.NewRows([]string{"owner_token", "fencing_epoch", "current"}).AddRow(bytes.Repeat([]byte{0x77}, 32), uint64(3), true))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT config_version_id, revision FROM account_runtime_state WHERE account_id = ? FOR UPDATE")).
		WithArgs(int64(7)).WillReturnRows(sqlmock.NewRows([]string{"config_version_id", "revision"}).AddRow(int64(51), uint64(4)))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT i.account_id FROM runtime_session_identities AS i JOIN live_sessions AS l ON l.id = i.live_session_id AND l.account_id = i.account_id JOIN runtime_active_session_guards AS g ON g.account_id = i.account_id AND g.live_session_id = i.live_session_id WHERE i.live_session_id = ? AND i.account_id = ? AND l.ended_at IS NULL FOR UPDATE")).
		WithArgs(int64(81), int64(7)).WillReturnRows(sqlmock.NewRows([]string{"account_id"}).AddRow(int64(7)))
	mock.ExpectExec(regexp.QuoteMeta("DELETE FROM runtime_event_dedup_receipts WHERE account_id = ? AND event_hash = ? AND expires_at <= UTC_TIMESTAMP(6)")).
		WithArgs(int64(7), hash[:]).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO runtime_event_dedup_receipts (event_hash, live_session_id, account_id, created_at, expires_at) VALUES (?, ?, ?, UTC_TIMESTAMP(6), TIMESTAMPADD(HOUR, 24, UTC_TIMESTAMP(6)))")).
		WithArgs(hash[:], int64(81), int64(7)).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT aggregate_json FROM runtime_session_aggregates WHERE live_session_id = ? AND account_id = ? FOR UPDATE")).
		WithArgs(int64(81), int64(7)).WillReturnError(sql.ErrNoRows)
	mock.ExpectExec(regexp.QuoteMeta("UPDATE account_runtime_state SET runtime_json = ?, revision = ?, updated_at = ? WHERE account_id = ? AND config_version_id = ? AND revision = ?")).
		WithArgs(sqlmock.AnyArg(), uint64(5), now, int64(7), int64(51), uint64(4)).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO runtime_session_aggregates (live_session_id, account_id, aggregate_json, updated_at) VALUES (?, ?, ?, ?) ON DUPLICATE KEY UPDATE aggregate_json = VALUES(aggregate_json), updated_at = VALUES(updated_at)")).
		WithArgs(int64(81), int64(7), json.RawMessage(`{"eventCount":1,"giftCount":2}`), now).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	result, err := repository.CommitRuntimeEvent(context.Background(), RuntimeEventCommand{
		AccountID: 7, LiveSessionID: 81, ConfigVersionID: 51, OwnerToken: [32]byte{0: 0x77, 1: 0x77, 2: 0x77, 3: 0x77, 4: 0x77, 5: 0x77, 6: 0x77, 7: 0x77, 8: 0x77, 9: 0x77, 10: 0x77, 11: 0x77, 12: 0x77, 13: 0x77, 14: 0x77, 15: 0x77, 16: 0x77, 17: 0x77, 18: 0x77, 19: 0x77, 20: 0x77, 21: 0x77, 22: 0x77, 23: 0x77, 24: 0x77, 25: 0x77, 26: 0x77, 27: 0x77, 28: 0x77, 29: 0x77, 30: 0x77, 31: 0x77}, OwnerEpoch: 3,
		ExpectedRevision: 4, Runtime: runtime, AggregateDelta: RuntimeAggregate{EventCount: 1, GiftCount: 2}, StableEventHash: &hash,
		UpdatedAt: now,
	})
	if err != nil || result.Duplicate || result.Revision != 5 {
		t.Fatalf("CommitRuntimeEvent() = %#v, %v", result, err)
	}
	assertSQLMock(t, mock)
}

func TestRepositoryCommitRuntimeEventRollsBackWhenExpiredReceiptCleanupFailsWithoutLeakingDetails(t *testing.T) {
	repository, mock, closeDB := newMockRepository(t)
	defer closeDB()
	owner := [32]byte{}
	for index := range owner {
		owner[index] = 0x77
	}
	now := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)

	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta("SELECT disabled_at IS NULL FROM streamer_accounts WHERE id = ? FOR UPDATE")).
		WithArgs(int64(7)).WillReturnRows(sqlmock.NewRows([]string{"enabled"}).AddRow(true))
	mock.ExpectExec(regexp.QuoteMeta("DELETE FROM runtime_event_dedup_receipts WHERE account_id = ? AND expires_at <= UTC_TIMESTAMP(6) ORDER BY expires_at, event_hash LIMIT 100")).
		WithArgs(int64(7)).WillReturnError(errors.New("cleanup-private-detail"))
	mock.ExpectRollback()

	_, err := repository.CommitRuntimeEvent(context.Background(), RuntimeEventCommand{AccountID: 7, LiveSessionID: 81, ConfigVersionID: 51, OwnerToken: owner, OwnerEpoch: 3, ExpectedRevision: 4, Runtime: processorRuntimeFixture(), AggregateDelta: RuntimeAggregate{EventCount: 1, GiftCount: 1}, UpdatedAt: now})
	if !errors.Is(err, ErrUnavailable) || strings.Contains(err.Error(), "cleanup-private-detail") {
		t.Fatalf("CommitRuntimeEvent() error = %v, want private-detail-free unavailable", err)
	}
	assertSQLMock(t, mock)
}

func TestRepositoryCommitRuntimeEventCrossSessionDuplicateCommitsWithoutStateOrAggregateMutation(t *testing.T) {
	repository, mock, closeDB := newMockRepository(t)
	defer closeDB()
	now := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	hash := sha256.Sum256([]byte("stable-event"))
	owner := [32]byte{}
	for index := range owner {
		owner[index] = 0x77
	}

	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta("SELECT disabled_at IS NULL FROM streamer_accounts WHERE id = ? FOR UPDATE")).WithArgs(int64(7)).WillReturnRows(sqlmock.NewRows([]string{"enabled"}).AddRow(true))
	mock.ExpectExec(regexp.QuoteMeta("DELETE FROM runtime_event_dedup_receipts WHERE account_id = ? AND expires_at <= UTC_TIMESTAMP(6) ORDER BY expires_at, event_hash LIMIT 100")).WithArgs(int64(7)).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT owner_token, fencing_epoch, expires_at > UTC_TIMESTAMP(6) FROM runtime_account_owners WHERE account_id = ? FOR UPDATE")).WithArgs(int64(7)).WillReturnRows(sqlmock.NewRows([]string{"owner_token", "fencing_epoch", "current"}).AddRow(owner[:], uint64(3), true))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT config_version_id, revision FROM account_runtime_state WHERE account_id = ? FOR UPDATE")).WithArgs(int64(7)).WillReturnRows(sqlmock.NewRows([]string{"config_version_id", "revision"}).AddRow(int64(51), uint64(4)))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT i.account_id FROM runtime_session_identities AS i JOIN live_sessions AS l ON l.id = i.live_session_id AND l.account_id = i.account_id JOIN runtime_active_session_guards AS g ON g.account_id = i.account_id AND g.live_session_id = i.live_session_id WHERE i.live_session_id = ? AND i.account_id = ? AND l.ended_at IS NULL FOR UPDATE")).WithArgs(int64(81), int64(7)).WillReturnRows(sqlmock.NewRows([]string{"account_id"}).AddRow(int64(7)))
	mock.ExpectExec(regexp.QuoteMeta("DELETE FROM runtime_event_dedup_receipts WHERE account_id = ? AND event_hash = ? AND expires_at <= UTC_TIMESTAMP(6)")).WithArgs(int64(7), hash[:]).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO runtime_event_dedup_receipts (event_hash, live_session_id, account_id, created_at, expires_at) VALUES (?, ?, ?, UTC_TIMESTAMP(6), TIMESTAMPADD(HOUR, 24, UTC_TIMESTAMP(6)))")).WithArgs(hash[:], int64(81), int64(7)).WillReturnError(&mysql.MySQLError{Number: 1062, Message: "duplicate"})
	mock.ExpectQuery(regexp.QuoteMeta("SELECT expires_at > UTC_TIMESTAMP(6) FROM runtime_event_dedup_receipts WHERE account_id = ? AND event_hash = ? FOR UPDATE")).WithArgs(int64(7), hash[:]).WillReturnRows(sqlmock.NewRows([]string{"current"}).AddRow(true))
	mock.ExpectCommit()

	result, err := repository.CommitRuntimeEvent(context.Background(), RuntimeEventCommand{AccountID: 7, LiveSessionID: 81, ConfigVersionID: 51, OwnerToken: owner, OwnerEpoch: 3, ExpectedRevision: 4, Runtime: processorRuntimeFixture(), AggregateDelta: RuntimeAggregate{EventCount: 1, GiftCount: 1}, StableEventHash: &hash, UpdatedAt: now})
	if err != nil || !result.Duplicate || result.Revision != 4 {
		t.Fatalf("CommitRuntimeEvent() = %#v, %v", result, err)
	}
	assertSQLMock(t, mock)
}

func TestRepositoryCommitRuntimeEventVerifiesAmbiguousCurrentDuplicateReceipt(t *testing.T) {
	repository, mock, closeDB := newMockRepository(t)
	defer closeDB()
	now := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	hash := sha256.Sum256([]byte("stable-event"))
	owner := bytes.Repeat([]byte{0x77}, 32)
	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta("SELECT disabled_at IS NULL FROM streamer_accounts WHERE id = ? FOR UPDATE")).WithArgs(int64(7)).WillReturnRows(sqlmock.NewRows([]string{"enabled"}).AddRow(true))
	mock.ExpectExec(regexp.QuoteMeta("DELETE FROM runtime_event_dedup_receipts WHERE account_id = ? AND expires_at <= UTC_TIMESTAMP(6) ORDER BY expires_at, event_hash LIMIT 100")).WithArgs(int64(7)).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT owner_token, fencing_epoch, expires_at > UTC_TIMESTAMP(6) FROM runtime_account_owners WHERE account_id = ? FOR UPDATE")).WithArgs(int64(7)).WillReturnRows(sqlmock.NewRows([]string{"owner_token", "fencing_epoch", "current"}).AddRow(owner, uint64(3), true))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT config_version_id, revision FROM account_runtime_state WHERE account_id = ? FOR UPDATE")).WithArgs(int64(7)).WillReturnRows(sqlmock.NewRows([]string{"config_version_id", "revision"}).AddRow(int64(51), uint64(4)))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT i.account_id FROM runtime_session_identities AS i JOIN live_sessions AS l ON l.id = i.live_session_id AND l.account_id = i.account_id JOIN runtime_active_session_guards AS g ON g.account_id = i.account_id AND g.live_session_id = i.live_session_id WHERE i.live_session_id = ? AND i.account_id = ? AND l.ended_at IS NULL FOR UPDATE")).WithArgs(int64(81), int64(7)).WillReturnRows(sqlmock.NewRows([]string{"account_id"}).AddRow(int64(7)))
	mock.ExpectExec(regexp.QuoteMeta("DELETE FROM runtime_event_dedup_receipts WHERE account_id = ? AND event_hash = ? AND expires_at <= UTC_TIMESTAMP(6)")).WithArgs(int64(7), hash[:]).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO runtime_event_dedup_receipts (event_hash, live_session_id, account_id, created_at, expires_at) VALUES (?, ?, ?, UTC_TIMESTAMP(6), TIMESTAMPADD(HOUR, 24, UTC_TIMESTAMP(6)))")).WithArgs(hash[:], int64(81), int64(7)).WillReturnError(&mysql.MySQLError{Number: 1062, Message: "duplicate"})
	receiptQuery := regexp.QuoteMeta("SELECT expires_at > UTC_TIMESTAMP(6) FROM runtime_event_dedup_receipts WHERE account_id = ? AND event_hash = ? FOR UPDATE")
	mock.ExpectQuery(receiptQuery).WithArgs(int64(7), hash[:]).WillReturnRows(sqlmock.NewRows([]string{"current"}).AddRow(true))
	mock.ExpectCommit().WillReturnError(errors.New("ambiguous duplicate commit"))
	verifyQuery := regexp.QuoteMeta("SELECT expires_at > UTC_TIMESTAMP(6) FROM runtime_event_dedup_receipts WHERE account_id = ? AND event_hash = ?")
	mock.ExpectQuery(verifyQuery).WithArgs(int64(7), hash[:]).WillReturnRows(sqlmock.NewRows([]string{"current"}).AddRow(true))
	var token [32]byte
	copy(token[:], owner)
	result, err := repository.CommitRuntimeEvent(context.Background(), RuntimeEventCommand{AccountID: 7, LiveSessionID: 81, ConfigVersionID: 51, OwnerToken: token, OwnerEpoch: 3, ExpectedRevision: 4, Runtime: processorRuntimeFixture(), AggregateDelta: RuntimeAggregate{EventCount: 1, GiftCount: 1}, StableEventHash: &hash, UpdatedAt: now})
	if err != nil || !result.Duplicate || result.Revision != 4 {
		t.Fatalf("CommitRuntimeEvent() = %#v, %v", result, err)
	}
	assertSQLMock(t, mock)
}

func TestRepositoryCommitRuntimeEventVerifiesAmbiguousIDLessCommitWithoutRetryingMutation(t *testing.T) {
	repository, mock, closeDB := newMockRepository(t)
	defer closeDB()
	now := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	owner := [32]byte{}
	for index := range owner {
		owner[index] = 0x77
	}
	runtime := processorRuntimeFixture()
	aggregate := RuntimeAggregate{EventCount: 1, GiftCount: 2}
	aggregateJSON := json.RawMessage(`{"eventCount":1,"giftCount":2}`)

	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta("SELECT disabled_at IS NULL FROM streamer_accounts WHERE id = ? FOR UPDATE")).WithArgs(int64(7)).WillReturnRows(sqlmock.NewRows([]string{"enabled"}).AddRow(true))
	mock.ExpectExec(regexp.QuoteMeta("DELETE FROM runtime_event_dedup_receipts WHERE account_id = ? AND expires_at <= UTC_TIMESTAMP(6) ORDER BY expires_at, event_hash LIMIT 100")).WithArgs(int64(7)).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT owner_token, fencing_epoch, expires_at > UTC_TIMESTAMP(6) FROM runtime_account_owners WHERE account_id = ? FOR UPDATE")).WithArgs(int64(7)).WillReturnRows(sqlmock.NewRows([]string{"owner_token", "fencing_epoch", "current"}).AddRow(owner[:], uint64(3), true))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT config_version_id, revision FROM account_runtime_state WHERE account_id = ? FOR UPDATE")).WithArgs(int64(7)).WillReturnRows(sqlmock.NewRows([]string{"config_version_id", "revision"}).AddRow(int64(51), uint64(4)))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT i.account_id FROM runtime_session_identities AS i JOIN live_sessions AS l ON l.id = i.live_session_id AND l.account_id = i.account_id JOIN runtime_active_session_guards AS g ON g.account_id = i.account_id AND g.live_session_id = i.live_session_id WHERE i.live_session_id = ? AND i.account_id = ? AND l.ended_at IS NULL FOR UPDATE")).WithArgs(int64(81), int64(7)).WillReturnRows(sqlmock.NewRows([]string{"account_id"}).AddRow(int64(7)))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT aggregate_json FROM runtime_session_aggregates WHERE live_session_id = ? AND account_id = ? FOR UPDATE")).WithArgs(int64(81), int64(7)).WillReturnError(sql.ErrNoRows)
	mock.ExpectExec(regexp.QuoteMeta("UPDATE account_runtime_state SET runtime_json = ?, revision = ?, updated_at = ? WHERE account_id = ? AND config_version_id = ? AND revision = ?")).WithArgs(sqlmock.AnyArg(), uint64(5), now, int64(7), int64(51), uint64(4)).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO runtime_session_aggregates (live_session_id, account_id, aggregate_json, updated_at) VALUES (?, ?, ?, ?) ON DUPLICATE KEY UPDATE aggregate_json = VALUES(aggregate_json), updated_at = VALUES(updated_at)")).WithArgs(int64(81), int64(7), aggregateJSON, now).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit().WillReturnError(errors.New("ambiguous commit"))
	mysqlRuntimeJSON := []byte(`{"ruleLimits":{"appliedCounts":{"score":1},"localDate":"2026-08-17"},"activities":[],"giftTargetReceived":[],"attributeValues":{"score":1}}`)
	mysqlAggregateJSON := []byte(`{ "giftCount": 2, "eventCount": 1 }`)
	mock.ExpectQuery(regexp.QuoteMeta("SELECT s.revision, s.runtime_json, a.aggregate_json, o.owner_token, o.fencing_epoch, o.expires_at > UTC_TIMESTAMP(6) FROM account_runtime_state AS s JOIN runtime_session_aggregates AS a ON a.account_id = s.account_id JOIN runtime_session_identities AS i ON i.account_id = a.account_id AND i.live_session_id = a.live_session_id JOIN runtime_account_owners AS o ON o.account_id = s.account_id WHERE s.account_id = ? AND s.config_version_id = ? AND i.live_session_id = ? AND i.account_id = ?")).WithArgs(int64(7), int64(51), int64(81), int64(7)).WillReturnRows(sqlmock.NewRows([]string{"revision", "runtime_json", "aggregate_json", "owner_token", "fencing_epoch", "current"}).AddRow(uint64(5), mysqlRuntimeJSON, mysqlAggregateJSON, owner[:], uint64(3), true))

	result, err := repository.CommitRuntimeEvent(context.Background(), RuntimeEventCommand{AccountID: 7, LiveSessionID: 81, ConfigVersionID: 51, OwnerToken: owner, OwnerEpoch: 3, ExpectedRevision: 4, Runtime: runtime, AggregateDelta: aggregate, UpdatedAt: now})
	if err != nil || result.Duplicate || result.Revision != 5 {
		t.Fatalf("CommitRuntimeEvent() = %#v, %v; SQL = %v", result, err, mock.ExpectationsWereMet())
	}
	assertSQLMock(t, mock)
}

func TestRepositoryCommitRuntimeEventRejectsAmbiguousInsertedReceiptOwnedByPriorSession(t *testing.T) {
	repository, mock, closeDB := newMockRepository(t)
	defer closeDB()
	now := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	owner := bytes.Repeat([]byte{0x77}, 32)
	hash := sha256.Sum256([]byte("new-session-event"))
	runtime := processorRuntimeFixture()
	aggregate := RuntimeAggregate{EventCount: 1, GiftCount: 1}

	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta("SELECT disabled_at IS NULL FROM streamer_accounts WHERE id = ? FOR UPDATE")).WithArgs(int64(7)).WillReturnRows(sqlmock.NewRows([]string{"enabled"}).AddRow(true))
	mock.ExpectExec(regexp.QuoteMeta("DELETE FROM runtime_event_dedup_receipts WHERE account_id = ? AND expires_at <= UTC_TIMESTAMP(6) ORDER BY expires_at, event_hash LIMIT 100")).WithArgs(int64(7)).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT owner_token, fencing_epoch, expires_at > UTC_TIMESTAMP(6) FROM runtime_account_owners WHERE account_id = ? FOR UPDATE")).WithArgs(int64(7)).WillReturnRows(sqlmock.NewRows([]string{"owner_token", "fencing_epoch", "current"}).AddRow(owner, uint64(3), true))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT config_version_id, revision FROM account_runtime_state WHERE account_id = ? FOR UPDATE")).WithArgs(int64(7)).WillReturnRows(sqlmock.NewRows([]string{"config_version_id", "revision"}).AddRow(int64(51), uint64(4)))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT i.account_id FROM runtime_session_identities AS i JOIN live_sessions AS l ON l.id = i.live_session_id AND l.account_id = i.account_id JOIN runtime_active_session_guards AS g ON g.account_id = i.account_id AND g.live_session_id = i.live_session_id WHERE i.live_session_id = ? AND i.account_id = ? AND l.ended_at IS NULL FOR UPDATE")).WithArgs(int64(81), int64(7)).WillReturnRows(sqlmock.NewRows([]string{"account_id"}).AddRow(int64(7)))
	mock.ExpectExec(regexp.QuoteMeta("DELETE FROM runtime_event_dedup_receipts WHERE account_id = ? AND event_hash = ? AND expires_at <= UTC_TIMESTAMP(6)")).WithArgs(int64(7), hash[:]).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO runtime_event_dedup_receipts (event_hash, live_session_id, account_id, created_at, expires_at) VALUES (?, ?, ?, UTC_TIMESTAMP(6), TIMESTAMPADD(HOUR, 24, UTC_TIMESTAMP(6)))")).WithArgs(hash[:], int64(81), int64(7)).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT aggregate_json FROM runtime_session_aggregates WHERE live_session_id = ? AND account_id = ? FOR UPDATE")).WithArgs(int64(81), int64(7)).WillReturnError(sql.ErrNoRows)
	mock.ExpectExec(regexp.QuoteMeta("UPDATE account_runtime_state SET runtime_json = ?, revision = ?, updated_at = ? WHERE account_id = ? AND config_version_id = ? AND revision = ?")).WithArgs(sqlmock.AnyArg(), uint64(5), now, int64(7), int64(51), uint64(4)).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO runtime_session_aggregates (live_session_id, account_id, aggregate_json, updated_at) VALUES (?, ?, ?, ?) ON DUPLICATE KEY UPDATE aggregate_json = VALUES(aggregate_json), updated_at = VALUES(updated_at)")).WithArgs(int64(81), int64(7), json.RawMessage(`{"eventCount":1,"giftCount":1}`), now).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit().WillReturnError(errors.New("ambiguous inserted commit"))
	mysqlRuntimeJSON := []byte(`{"ruleLimits":{"appliedCounts":{"score":1},"localDate":"2026-08-17"},"activities":[],"giftTargetReceived":[],"attributeValues":{"score":1}}`)
	mock.ExpectQuery(regexp.QuoteMeta("SELECT s.revision, s.runtime_json, a.aggregate_json, o.owner_token, o.fencing_epoch, o.expires_at > UTC_TIMESTAMP(6) FROM account_runtime_state AS s JOIN runtime_session_aggregates AS a ON a.account_id = s.account_id JOIN runtime_session_identities AS i ON i.account_id = a.account_id AND i.live_session_id = a.live_session_id JOIN runtime_account_owners AS o ON o.account_id = s.account_id WHERE s.account_id = ? AND s.config_version_id = ? AND i.live_session_id = ? AND i.account_id = ?")).WithArgs(int64(7), int64(51), int64(81), int64(7)).WillReturnRows(sqlmock.NewRows([]string{"revision", "runtime_json", "aggregate_json", "owner_token", "fencing_epoch", "current"}).AddRow(uint64(5), mysqlRuntimeJSON, []byte(`{"eventCount":1,"giftCount":1}`), owner, uint64(3), true))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT live_session_id, created_at, expires_at, expires_at > UTC_TIMESTAMP(6), TIMESTAMPDIFF(MICROSECOND, created_at, expires_at) = 86400000000 FROM runtime_event_dedup_receipts WHERE account_id = ? AND event_hash = ?")).WithArgs(int64(7), hash[:]).WillReturnRows(sqlmock.NewRows([]string{"live_session_id", "created_at", "expires_at", "current", "exact_ttl"}).AddRow(int64(80), now, now.Add(24*time.Hour), true, true))

	var token [32]byte
	copy(token[:], owner)
	_, err := repository.CommitRuntimeEvent(context.Background(), RuntimeEventCommand{AccountID: 7, LiveSessionID: 81, ConfigVersionID: 51, OwnerToken: token, OwnerEpoch: 3, ExpectedRevision: 4, Runtime: runtime, AggregateDelta: aggregate, StableEventHash: &hash, UpdatedAt: now})
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("CommitRuntimeEvent() error = %v, want unavailable for prior-session inserted receipt", err)
	}
	assertSQLMock(t, mock)
}

func TestRepositoryCommitRuntimeEventRejectsEndedOrSupersededSession(t *testing.T) {
	repository, mock, closeDB := newMockRepository(t)
	defer closeDB()
	owner := bytes.Repeat([]byte{0x77}, 32)
	now := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta("SELECT disabled_at IS NULL FROM streamer_accounts WHERE id = ? FOR UPDATE")).WithArgs(int64(7)).WillReturnRows(sqlmock.NewRows([]string{"enabled"}).AddRow(true))
	mock.ExpectExec(regexp.QuoteMeta("DELETE FROM runtime_event_dedup_receipts WHERE account_id = ? AND expires_at <= UTC_TIMESTAMP(6) ORDER BY expires_at, event_hash LIMIT 100")).WithArgs(int64(7)).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT owner_token, fencing_epoch, expires_at > UTC_TIMESTAMP(6) FROM runtime_account_owners WHERE account_id = ? FOR UPDATE")).WithArgs(int64(7)).WillReturnRows(sqlmock.NewRows([]string{"owner_token", "fencing_epoch", "current"}).AddRow(owner, uint64(3), true))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT config_version_id, revision FROM account_runtime_state WHERE account_id = ? FOR UPDATE")).WithArgs(int64(7)).WillReturnRows(sqlmock.NewRows([]string{"config_version_id", "revision"}).AddRow(int64(51), uint64(4)))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT i.account_id FROM runtime_session_identities AS i JOIN live_sessions AS l ON l.id = i.live_session_id AND l.account_id = i.account_id JOIN runtime_active_session_guards AS g ON g.account_id = i.account_id AND g.live_session_id = i.live_session_id WHERE i.live_session_id = ? AND i.account_id = ? AND l.ended_at IS NULL FOR UPDATE")).WithArgs(int64(81), int64(7)).WillReturnError(sql.ErrNoRows)
	mock.ExpectRollback()
	var token [32]byte
	copy(token[:], owner)
	_, err := repository.CommitRuntimeEvent(context.Background(), RuntimeEventCommand{AccountID: 7, LiveSessionID: 81, ConfigVersionID: 51, OwnerToken: token, OwnerEpoch: 3, ExpectedRevision: 4, Runtime: processorRuntimeFixture(), AggregateDelta: RuntimeAggregate{EventCount: 1, GiftCount: 1}, UpdatedAt: now})
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("CommitRuntimeEvent() error = %v, want unavailable", err)
	}
	assertSQLMock(t, mock)
}

func TestRepositoryCommitRuntimeEventMergesAggregateDeltaWithExistingSessionTotals(t *testing.T) {
	repository, mock, closeDB := newMockRepository(t)
	defer closeDB()
	now := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	owner := bytes.Repeat([]byte{0x77}, 32)
	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta("SELECT disabled_at IS NULL FROM streamer_accounts WHERE id = ? FOR UPDATE")).WithArgs(int64(7)).WillReturnRows(sqlmock.NewRows([]string{"enabled"}).AddRow(true))
	mock.ExpectExec(regexp.QuoteMeta("DELETE FROM runtime_event_dedup_receipts WHERE account_id = ? AND expires_at <= UTC_TIMESTAMP(6) ORDER BY expires_at, event_hash LIMIT 100")).WithArgs(int64(7)).WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT owner_token, fencing_epoch, expires_at > UTC_TIMESTAMP(6) FROM runtime_account_owners WHERE account_id = ? FOR UPDATE")).WithArgs(int64(7)).WillReturnRows(sqlmock.NewRows([]string{"owner_token", "fencing_epoch", "current"}).AddRow(owner, uint64(3), true))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT config_version_id, revision FROM account_runtime_state WHERE account_id = ? FOR UPDATE")).WithArgs(int64(7)).WillReturnRows(sqlmock.NewRows([]string{"config_version_id", "revision"}).AddRow(int64(51), uint64(4)))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT i.account_id FROM runtime_session_identities AS i JOIN live_sessions AS l ON l.id = i.live_session_id AND l.account_id = i.account_id JOIN runtime_active_session_guards AS g ON g.account_id = i.account_id AND g.live_session_id = i.live_session_id WHERE i.live_session_id = ? AND i.account_id = ? AND l.ended_at IS NULL FOR UPDATE")).WithArgs(int64(81), int64(7)).WillReturnRows(sqlmock.NewRows([]string{"account_id"}).AddRow(int64(7)))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT aggregate_json FROM runtime_session_aggregates WHERE live_session_id = ? AND account_id = ? FOR UPDATE")).WithArgs(int64(81), int64(7)).WillReturnRows(sqlmock.NewRows([]string{"aggregate_json"}).AddRow([]byte(`{"eventCount":4,"giftCount":5,"giftCoin":6000}`)))
	mock.ExpectExec(regexp.QuoteMeta("UPDATE account_runtime_state SET runtime_json = ?, revision = ?, updated_at = ? WHERE account_id = ? AND config_version_id = ? AND revision = ?")).WithArgs(sqlmock.AnyArg(), uint64(5), now, int64(7), int64(51), uint64(4)).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO runtime_session_aggregates (live_session_id, account_id, aggregate_json, updated_at) VALUES (?, ?, ?, ?) ON DUPLICATE KEY UPDATE aggregate_json = VALUES(aggregate_json), updated_at = VALUES(updated_at)")).WithArgs(int64(81), int64(7), json.RawMessage(`{"eventCount":5,"giftCount":7,"giftCoin":8000}`), now).WillReturnResult(sqlmock.NewResult(0, 2))
	mock.ExpectCommit()
	var token [32]byte
	copy(token[:], owner)
	result, err := repository.CommitRuntimeEvent(context.Background(), RuntimeEventCommand{AccountID: 7, LiveSessionID: 81, ConfigVersionID: 51, OwnerToken: token, OwnerEpoch: 3, ExpectedRevision: 4, Runtime: processorRuntimeFixture(), AggregateDelta: RuntimeAggregate{EventCount: 1, GiftCount: 2, GiftCoin: 2000}, UpdatedAt: now})
	if err != nil || result.Revision != 5 {
		t.Fatalf("CommitRuntimeEvent() = %#v, %v", result, err)
	}
	assertSQLMock(t, mock)
}

func processorRuntimeFixture() RuntimeState {
	return RuntimeState{AttributeValues: map[string]float64{"score": 1}, GiftTargetReceived: []GiftTargetRuntimeState{}, Activities: []ActivityRuntimeState{}, RuleLimits: gameplay.RuleLimitState{LocalDate: "2026-08-17", AppliedCounts: map[string]int{"score": 1}}}
}

func TestRepositoryActivateCreatesAndActivatesVersionAtomically(t *testing.T) {
	repository, mock, closeDB := newMockRepository(t)
	defer closeDB()
	definition, runtime, err := Split(fixtureSnapshot())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC)
	migrationJobID := int64(33)

	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta("SELECT active.config_version_id, COALESCE(v.number, 0) FROM streamer_accounts AS a LEFT JOIN account_active_config AS active ON active.account_id = a.id LEFT JOIN account_config_versions AS v ON v.account_id = active.account_id AND v.id = active.config_version_id WHERE a.id = ? FOR UPDATE")).
		WithArgs(int64(7)).
		WillReturnRows(sqlmock.NewRows([]string{"config_version_id", "number"}).AddRow(nil, uint64(0)))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT revision FROM account_runtime_state WHERE account_id = ? FOR UPDATE")).
		WithArgs(int64(7)).
		WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery(regexp.QuoteMeta("SELECT COALESCE(MAX(number), 0) + 1 FROM account_config_versions WHERE account_id = ?")).
		WithArgs(int64(7)).
		WillReturnRows(sqlmock.NewRows([]string{"number"}).AddRow(uint64(1)))
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO account_config_versions (account_id, number, definition_json, source, created_at) VALUES (?, ?, ?, ?, ?)")).
		WithArgs(int64(7), uint64(1), jsonWithoutImageURL{}, "migration", now).
		WillReturnResult(sqlmock.NewResult(51, 1))
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO account_runtime_state (account_id, config_version_id, revision, runtime_json, updated_at) VALUES (?, ?, ?, ?, ?) ON DUPLICATE KEY UPDATE config_version_id = VALUES(config_version_id), revision = VALUES(revision), runtime_json = VALUES(runtime_json), updated_at = VALUES(updated_at)")).
		WithArgs(int64(7), int64(51), uint64(1), sqlmock.AnyArg(), now).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO account_active_config (account_id, config_version_id, updated_at) VALUES (?, ?, ?) ON DUPLICATE KEY UPDATE config_version_id = VALUES(config_version_id), updated_at = VALUES(updated_at)")).
		WithArgs(int64(7), int64(51), now).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(regexp.QuoteMeta("UPDATE migration_jobs SET status = 'applied', applied_at = ? WHERE id = ? AND account_id = ? AND status IN ('previewed', 'pending')")).
		WithArgs(now, migrationJobID, int64(7)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	version, state, err := repository.Activate(context.Background(), ActivationCommand{
		AccountID: 7, ExpectedVersion: 0, ExpectedRevision: 0, Definition: definition, Runtime: runtime,
		Source: "migration", MigrationJobID: &migrationJobID, At: now,
	})
	if err != nil {
		t.Fatalf("Activate() error = %v", err)
	}
	if version.ID != 51 || version.AccountID != 7 || version.Number != 1 || version.Source != "migration" || !version.CreatedAt.Equal(now) {
		t.Fatalf("version = %#v", version)
	}
	if state.AccountID != 7 || state.ConfigVersionID != 51 || state.Revision != 1 || !state.UpdatedAt.Equal(now) {
		t.Fatalf("state = %#v", state)
	}
	assertSQLMock(t, mock)
}

func TestRepositoryActivateBarrierPersistsBoundaryAndRollbackMaterialAtomically(t *testing.T) {
	repository, mock, closeDB := newMockRepository(t)
	defer closeDB()
	now := time.Date(2026, 8, 29, 12, 0, 0, 0, time.UTC)
	definition, runtime, err := Split(fixtureSnapshot())
	if err != nil {
		t.Fatal(err)
	}
	definition.MigrationHash = ""
	canonical, err := json.Marshal(struct {
		Definition Definition   `json:"definition"`
		Runtime    RuntimeState `json:"runtime"`
	}{definition, runtime})
	if err != nil {
		t.Fatal(err)
	}
	seal := sha256.Sum256(canonical)
	stagedDefinition := definition
	stagedDefinition.MigrationHash = hex.EncodeToString(seal[:])
	stagedDefinitionJSON, _ := json.Marshal(stagedDefinition)
	runtimeJSON, _ := json.Marshal(runtime)
	oldRuntimeJSON := []byte(`{"attributeValues":{"health":3}}`)
	owner := [32]byte{1}

	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta(barrierAccountQuery)).WithArgs(int64(7)).
		WillReturnRows(sqlmock.NewRows([]string{"config_version_id", "number", "revision", "runtime_json"}).AddRow(int64(50), uint64(2), uint64(4), oldRuntimeJSON))
	mock.ExpectQuery(regexp.QuoteMeta(barrierOwnerQuery)).WithArgs(int64(7), owner[:], uint64(9)).WillReturnRows(sqlmock.NewRows([]string{"active"}).AddRow(true))
	mock.ExpectQuery(regexp.QuoteMeta(barrierJobQuery)).WithArgs(int64(19), int64(7)).
		WillReturnRows(sqlmock.NewRows([]string{"status", "expires_at", "base_config_version_number", "base_state_revision", "keep_room_suggestion", "definition_json", "runtime_json", "rollback_config_version_id", "rollback_runtime_json", "rollback_expires_at", "applied_config_version_id", "room_suggestion"}).
			AddRow("pending", now.Add(time.Hour), uint64(2), uint64(4), uint8(1), stagedDefinitionJSON, runtimeJSON, nil, nil, nil, nil, "12345"))
	mock.ExpectQuery(regexp.QuoteMeta(barrierNowQuery)).WillReturnRows(sqlmock.NewRows([]string{"now"}).AddRow(now))
	mock.ExpectQuery(regexp.QuoteMeta(barrierNextVersionQuery)).WithArgs(int64(7)).WillReturnRows(sqlmock.NewRows([]string{"number"}).AddRow(uint64(3)))
	mock.ExpectExec(regexp.QuoteMeta(barrierInsertVersionQuery)).WithArgs(int64(7), uint64(3), jsonWithoutMigrationHash{}, "migration", now).WillReturnResult(sqlmock.NewResult(51, 1))
	mock.ExpectExec(regexp.QuoteMeta(barrierUpsertRuntimeQuery)).WithArgs(int64(7), int64(51), uint64(5), sqlmock.AnyArg(), now).WillReturnResult(sqlmock.NewResult(0, 2))
	mock.ExpectExec(regexp.QuoteMeta(barrierUpsertActiveQuery)).WithArgs(int64(7), int64(51), now).WillReturnResult(sqlmock.NewResult(0, 2))
	mock.ExpectExec(regexp.QuoteMeta(barrierUpsertRoomSuggestionQuery)).WithArgs(int64(7), "12345", now).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(regexp.QuoteMeta(barrierApplyJobQuery)).WithArgs(int64(50), sqlmock.AnyArg(), now.Add(7*24*time.Hour), int64(51), now, int64(19), int64(7)).WillReturnResult(sqlmock.NewResult(0, 1))
	want := Boundary{AccountID: 7, MigrationJobID: 19, Operation: BarrierMigrationApply, OldConfigVersionID: 50, NewConfigVersionID: 51, BroadcastSessionID: 91, LiveSessionID: 81, LastOldRevision: 4, FirstNewRevision: 5, AppliedAt: now}
	mock.ExpectExec(regexp.QuoteMeta(barrierInsertAuditQuery)).WithArgs("configuration_barrier", int64(7), int64(7), boundaryJSONMatcher{want: want}, now).WillReturnResult(sqlmock.NewResult(101, 1))
	mock.ExpectCommit()

	got, err := repository.ActivateBarrier(context.Background(), BarrierCommand{
		AccountID: 7, ExpectedConfigVersionID: 50, ExpectedVersion: 2, ExpectedRevision: 4,
		Definition: definition, Runtime: runtime, Operation: BarrierMigrationApply, MigrationJobID: 19, IntegritySeal: seal,
		KeepRoomSuggestion: true, RoomSuggestion: "12345", OwnerToken: owner, OwnerEpoch: 9, LiveSessionID: 81, BroadcastSessionID: 91, At: now,
	})
	if err != nil {
		t.Fatalf("ActivateBarrier() error = %v", err)
	}
	if got != want {
		t.Fatalf("ActivateBarrier() = %#v, want %#v", got, want)
	}
	assertSQLMock(t, mock)
}

func TestRepositoryActivateBarrierEmptyAccountCreatesRollbackBaseWithoutChangingInitialBoundary(t *testing.T) {
	repository, mock, closeDB := newMockRepository(t)
	defer closeDB()
	now := time.Date(2026, 8, 29, 12, 10, 0, 0, time.UTC)
	definition, runtime, err := Split(fixtureSnapshot())
	if err != nil {
		t.Fatal(err)
	}
	definition.MigrationHash = ""
	canonical, _ := json.Marshal(struct {
		Definition Definition   `json:"definition"`
		Runtime    RuntimeState `json:"runtime"`
	}{definition, runtime})
	seal := sha256.Sum256(canonical)
	staged := definition
	staged.MigrationHash = hex.EncodeToString(seal[:])
	stagedJSON, _ := json.Marshal(staged)
	runtimeJSON, _ := json.Marshal(runtime)
	emptyDefinition, emptyRuntime, err := Normalize(Definition{}, DefaultRuntime(Definition{}))
	if err != nil {
		t.Fatal(err)
	}
	emptyDefinitionJSON, _ := json.Marshal(emptyDefinition)
	emptyRuntimeJSON, _ := json.Marshal(emptyRuntime)

	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta(barrierAccountQuery)).WithArgs(int64(7)).
		WillReturnRows(sqlmock.NewRows([]string{"config_version_id", "number", "revision", "runtime_json"}).AddRow(nil, uint64(0), uint64(0), nil))
	mock.ExpectQuery(regexp.QuoteMeta(barrierJobQuery)).WithArgs(int64(19), int64(7)).
		WillReturnRows(sqlmock.NewRows([]string{"status", "expires_at", "base_config_version_number", "base_state_revision", "keep_room_suggestion", "definition_json", "runtime_json", "rollback_config_version_id", "rollback_runtime_json", "rollback_expires_at", "applied_config_version_id", "room_suggestion"}).
			AddRow("pending", now.Add(time.Hour), uint64(0), uint64(0), uint8(0), stagedJSON, runtimeJSON, nil, nil, nil, nil, nil))
	mock.ExpectQuery(regexp.QuoteMeta(barrierNowQuery)).WillReturnRows(sqlmock.NewRows([]string{"now"}).AddRow(now))
	mock.ExpectQuery(regexp.QuoteMeta(barrierNextVersionQuery)).WithArgs(int64(7)).WillReturnRows(sqlmock.NewRows([]string{"number"}).AddRow(uint64(1)))
	mock.ExpectExec(regexp.QuoteMeta(barrierInsertVersionQuery)).WithArgs(int64(7), uint64(1), emptyDefinitionJSON, "migration", now).WillReturnResult(sqlmock.NewResult(50, 1))
	mock.ExpectExec(regexp.QuoteMeta(barrierInsertVersionQuery)).WithArgs(int64(7), uint64(2), jsonWithoutMigrationHash{}, "migration", now).WillReturnResult(sqlmock.NewResult(51, 1))
	mock.ExpectExec(regexp.QuoteMeta(barrierUpsertRuntimeQuery)).WithArgs(int64(7), int64(51), uint64(1), sqlmock.AnyArg(), now).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(regexp.QuoteMeta(barrierUpsertActiveQuery)).WithArgs(int64(7), int64(51), now).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(regexp.QuoteMeta(barrierApplyJobQuery)).WithArgs(int64(50), emptyRuntimeJSON, now.Add(7*24*time.Hour), int64(51), now, int64(19), int64(7)).WillReturnResult(sqlmock.NewResult(0, 1))
	want := Boundary{AccountID: 7, MigrationJobID: 19, Operation: BarrierMigrationApply, OldConfigVersionID: 0, NewConfigVersionID: 51, LastOldRevision: 0, FirstNewRevision: 1, AppliedAt: now}
	mock.ExpectExec(regexp.QuoteMeta(barrierInsertAuditQuery)).WithArgs("configuration_barrier", int64(7), int64(7), boundaryJSONMatcher{want: want}, now).WillReturnResult(sqlmock.NewResult(104, 1))
	mock.ExpectCommit()

	got, err := repository.ActivateBarrier(context.Background(), BarrierCommand{
		AccountID: 7, ExpectedConfigVersionID: 0, ExpectedVersion: 0, ExpectedRevision: 0,
		Definition: definition, Runtime: runtime, Operation: BarrierMigrationApply, MigrationJobID: 19, IntegritySeal: seal, At: now,
	})
	if err != nil {
		t.Fatalf("ActivateBarrier(empty account) error = %v", err)
	}
	if got != want {
		t.Fatalf("ActivateBarrier(empty account) = %#v, want %#v", got, want)
	}
	assertSQLMock(t, mock)
}

func TestRepositoryActivateBarrierRollbackCreatesNewVersionWithoutChangingHistory(t *testing.T) {
	repository, mock, closeDB := newMockRepository(t)
	defer closeDB()
	now := time.Date(2026, 8, 29, 12, 15, 0, 0, time.UTC)
	rollbackDefinition, rollbackRuntime, err := Split(fixtureSnapshot())
	if err != nil {
		t.Fatal(err)
	}
	rollbackDefinition.MigrationHash = ""
	rollbackDefinitionJSON, _ := json.Marshal(rollbackDefinition)
	rollbackRuntimeJSON, _ := json.Marshal(rollbackRuntime)
	currentRuntimeJSON := []byte(`{"attributeValues":{"health":9}}`)
	owner := [32]byte{2}

	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta(barrierAccountQuery)).WithArgs(int64(7)).
		WillReturnRows(sqlmock.NewRows([]string{"config_version_id", "number", "revision", "runtime_json"}).AddRow(int64(51), uint64(3), uint64(5), currentRuntimeJSON))
	mock.ExpectQuery(regexp.QuoteMeta(barrierOwnerQuery)).WithArgs(int64(7), owner[:], uint64(9)).WillReturnRows(sqlmock.NewRows([]string{"active"}).AddRow(true))
	mock.ExpectQuery(regexp.QuoteMeta(barrierJobQuery)).WithArgs(int64(19), int64(7)).
		WillReturnRows(sqlmock.NewRows([]string{"status", "expires_at", "base_config_version_number", "base_state_revision", "keep_room_suggestion", "definition_json", "runtime_json", "rollback_config_version_id", "rollback_runtime_json", "rollback_expires_at", "applied_config_version_id", "room_suggestion"}).
			AddRow("applied", now.Add(time.Hour), uint64(2), uint64(4), uint8(0), []byte(`{"attributes":[]}`), []byte(`{"attributeValues":{}}`), int64(50), rollbackRuntimeJSON, now.Add(7*24*time.Hour), int64(51), nil))
	mock.ExpectQuery(regexp.QuoteMeta(barrierNowQuery)).WillReturnRows(sqlmock.NewRows([]string{"now"}).AddRow(now))
	mock.ExpectQuery(regexp.QuoteMeta(barrierRollbackDefinitionQuery)).WithArgs(int64(7), int64(50)).WillReturnRows(sqlmock.NewRows([]string{"definition_json"}).AddRow(rollbackDefinitionJSON))
	mock.ExpectQuery(regexp.QuoteMeta(barrierNextVersionQuery)).WithArgs(int64(7)).WillReturnRows(sqlmock.NewRows([]string{"number"}).AddRow(uint64(4)))
	mock.ExpectExec(regexp.QuoteMeta(barrierInsertVersionQuery)).WithArgs(int64(7), uint64(4), jsonWithoutMigrationHash{}, "rollback", now).WillReturnResult(sqlmock.NewResult(52, 1))
	mock.ExpectExec(regexp.QuoteMeta(barrierUpsertRuntimeQuery)).WithArgs(int64(7), int64(52), uint64(6), sqlmock.AnyArg(), now).WillReturnResult(sqlmock.NewResult(0, 2))
	mock.ExpectExec(regexp.QuoteMeta(barrierUpsertActiveQuery)).WithArgs(int64(7), int64(52), now).WillReturnResult(sqlmock.NewResult(0, 2))
	mock.ExpectExec(regexp.QuoteMeta(barrierRollbackJobQuery)).WithArgs(now, int64(19), int64(7)).WillReturnResult(sqlmock.NewResult(0, 1))
	want := Boundary{AccountID: 7, MigrationJobID: 19, Operation: BarrierMigrationRollback, OldConfigVersionID: 51, NewConfigVersionID: 52, BroadcastSessionID: 91, LiveSessionID: 81, LastOldRevision: 5, FirstNewRevision: 6, AppliedAt: now}
	mock.ExpectExec(regexp.QuoteMeta(barrierInsertAuditQuery)).WithArgs("configuration_barrier", int64(7), int64(7), boundaryJSONMatcher{want: want}, now).WillReturnResult(sqlmock.NewResult(102, 1))
	mock.ExpectCommit()

	got, err := repository.ActivateBarrier(context.Background(), BarrierCommand{
		AccountID: 7, ExpectedConfigVersionID: 51, ExpectedVersion: 3, ExpectedRevision: 5,
		Definition: rollbackDefinition, Runtime: rollbackRuntime, Operation: BarrierMigrationRollback, MigrationJobID: 19,
		OwnerToken: owner, OwnerEpoch: 9, LiveSessionID: 81, BroadcastSessionID: 91, At: now,
	})
	if err != nil {
		t.Fatalf("ActivateBarrier(rollback) error = %v", err)
	}
	if got != want {
		t.Fatalf("ActivateBarrier(rollback) = %#v, want %#v", got, want)
	}
	assertSQLMock(t, mock)
}

func TestRepositoryActivateBarrierRollbackToEmptyBootstrapCreatesNewVersion(t *testing.T) {
	repository, mock, closeDB := newMockRepository(t)
	defer closeDB()
	now := time.Date(2026, 8, 29, 12, 18, 0, 0, time.UTC)
	emptyDefinition, emptyRuntime, err := Normalize(Definition{}, DefaultRuntime(Definition{}))
	if err != nil {
		t.Fatal(err)
	}
	emptyDefinitionJSON, _ := json.Marshal(emptyDefinition)
	emptyRuntimeJSON, _ := json.Marshal(emptyRuntime)
	currentRuntimeJSON := []byte(`{"attributeValues":{"exe":9},"giftTargetReceived":[],"activities":[]}`)

	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta(barrierAccountQuery)).WithArgs(int64(7)).
		WillReturnRows(sqlmock.NewRows([]string{"config_version_id", "number", "revision", "runtime_json"}).AddRow(int64(51), uint64(2), uint64(1), currentRuntimeJSON))
	mock.ExpectQuery(regexp.QuoteMeta(barrierJobQuery)).WithArgs(int64(19), int64(7)).
		WillReturnRows(sqlmock.NewRows([]string{"status", "expires_at", "base_config_version_number", "base_state_revision", "keep_room_suggestion", "definition_json", "runtime_json", "rollback_config_version_id", "rollback_runtime_json", "rollback_expires_at", "applied_config_version_id", "room_suggestion"}).
			AddRow("applied", now.Add(time.Hour), uint64(0), uint64(0), uint8(0), []byte(`{"attributes":[]}`), currentRuntimeJSON, int64(50), emptyRuntimeJSON, now.Add(7*24*time.Hour), int64(51), nil))
	mock.ExpectQuery(regexp.QuoteMeta(barrierNowQuery)).WillReturnRows(sqlmock.NewRows([]string{"now"}).AddRow(now))
	mock.ExpectQuery(regexp.QuoteMeta(barrierRollbackDefinitionQuery)).WithArgs(int64(7), int64(50)).WillReturnRows(sqlmock.NewRows([]string{"definition_json"}).AddRow(emptyDefinitionJSON))
	mock.ExpectQuery(regexp.QuoteMeta(barrierNextVersionQuery)).WithArgs(int64(7)).WillReturnRows(sqlmock.NewRows([]string{"number"}).AddRow(uint64(3)))
	mock.ExpectExec(regexp.QuoteMeta(barrierInsertVersionQuery)).WithArgs(int64(7), uint64(3), emptyDefinitionJSON, "rollback", now).WillReturnResult(sqlmock.NewResult(52, 1))
	mock.ExpectExec(regexp.QuoteMeta(barrierUpsertRuntimeQuery)).WithArgs(int64(7), int64(52), uint64(2), emptyRuntimeJSON, now).WillReturnResult(sqlmock.NewResult(0, 2))
	mock.ExpectExec(regexp.QuoteMeta(barrierUpsertActiveQuery)).WithArgs(int64(7), int64(52), now).WillReturnResult(sqlmock.NewResult(0, 2))
	mock.ExpectExec(regexp.QuoteMeta(barrierRollbackJobQuery)).WithArgs(now, int64(19), int64(7)).WillReturnResult(sqlmock.NewResult(0, 1))
	want := Boundary{AccountID: 7, MigrationJobID: 19, Operation: BarrierMigrationRollback, OldConfigVersionID: 51, NewConfigVersionID: 52, LastOldRevision: 1, FirstNewRevision: 2, AppliedAt: now}
	mock.ExpectExec(regexp.QuoteMeta(barrierInsertAuditQuery)).WithArgs("configuration_barrier", int64(7), int64(7), boundaryJSONMatcher{want: want}, now).WillReturnResult(sqlmock.NewResult(105, 1))
	mock.ExpectCommit()

	got, err := repository.ActivateBarrier(context.Background(), BarrierCommand{
		AccountID: 7, ExpectedConfigVersionID: 51, ExpectedVersion: 2, ExpectedRevision: 1,
		Definition: emptyDefinition, Runtime: emptyRuntime, Operation: BarrierMigrationRollback, MigrationJobID: 19, At: now,
	})
	if err != nil {
		t.Fatalf("ActivateBarrier(empty rollback) error = %v", err)
	}
	if got != want {
		t.Fatalf("ActivateBarrier(empty rollback) = %#v, want %#v", got, want)
	}
	assertSQLMock(t, mock)
}

func TestRepositoryActivateBarrierReturnsPersistedBoundaryForCompletedOperation(t *testing.T) {
	repository, mock, closeDB := newMockRepository(t)
	defer closeDB()
	now := time.Date(2026, 8, 29, 12, 30, 0, 0, time.UTC)
	definition, runtime, err := Split(fixtureSnapshot())
	if err != nil {
		t.Fatal(err)
	}
	definition.MigrationHash = ""
	canonical, _ := json.Marshal(struct {
		Definition Definition   `json:"definition"`
		Runtime    RuntimeState `json:"runtime"`
	}{definition, runtime})
	seal := sha256.Sum256(canonical)
	staged := definition
	staged.MigrationHash = hex.EncodeToString(seal[:])
	stagedJSON, _ := json.Marshal(staged)
	runtimeJSON, _ := json.Marshal(runtime)
	owner := [32]byte{3}
	want := Boundary{AccountID: 7, MigrationJobID: 19, Operation: BarrierMigrationApply, OldConfigVersionID: 50, NewConfigVersionID: 51, BroadcastSessionID: 91, LiveSessionID: 81, LastOldRevision: 4, FirstNewRevision: 5, AppliedAt: now.Add(-time.Minute)}
	wantJSON, _ := json.Marshal(want)

	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta(barrierAccountQuery)).WithArgs(int64(7)).
		WillReturnRows(sqlmock.NewRows([]string{"config_version_id", "number", "revision", "runtime_json"}).AddRow(int64(51), uint64(3), uint64(5), runtimeJSON))
	mock.ExpectQuery(regexp.QuoteMeta(barrierOwnerQuery)).WithArgs(int64(7), owner[:], uint64(9)).WillReturnRows(sqlmock.NewRows([]string{"active"}).AddRow(true))
	mock.ExpectQuery(regexp.QuoteMeta(barrierJobQuery)).WithArgs(int64(19), int64(7)).
		WillReturnRows(sqlmock.NewRows([]string{"status", "expires_at", "base_config_version_number", "base_state_revision", "keep_room_suggestion", "definition_json", "runtime_json", "rollback_config_version_id", "rollback_runtime_json", "rollback_expires_at", "applied_config_version_id", "room_suggestion"}).
			AddRow("applied", now.Add(time.Hour), uint64(2), uint64(4), uint8(0), stagedJSON, runtimeJSON, int64(50), runtimeJSON, now.Add(7*24*time.Hour), int64(51), nil))
	mock.ExpectQuery(regexp.QuoteMeta(barrierNowQuery)).WillReturnRows(sqlmock.NewRows([]string{"now"}).AddRow(now))
	mock.ExpectQuery(regexp.QuoteMeta(barrierBoundaryQuery)).WithArgs(int64(7), string(BarrierMigrationApply), int64(19)).WillReturnRows(sqlmock.NewRows([]string{"event_data"}).AddRow(wantJSON))
	mock.ExpectCommit()

	got, err := repository.ActivateBarrier(context.Background(), BarrierCommand{
		AccountID: 7, ExpectedConfigVersionID: 51, ExpectedVersion: 3, ExpectedRevision: 5,
		Definition: definition, Runtime: runtime, Operation: BarrierMigrationApply, MigrationJobID: 19, IntegritySeal: seal,
		OwnerToken: owner, OwnerEpoch: 9, LiveSessionID: 81, BroadcastSessionID: 91, At: now,
	})
	if err != nil {
		t.Fatalf("idempotent ActivateBarrier() error = %v", err)
	}
	if got != want {
		t.Fatalf("idempotent ActivateBarrier() = %#v, want %#v", got, want)
	}
	assertSQLMock(t, mock)
}

func TestRepositoryActivateBarrierReturnsOriginalBoundaryAcrossLaterSession(t *testing.T) {
	repository, mock, closeDB := newMockRepository(t)
	defer closeDB()
	now := time.Date(2026, 8, 29, 12, 32, 0, 0, time.UTC)
	definition, runtime, err := Split(fixtureSnapshot())
	if err != nil {
		t.Fatal(err)
	}
	definition.MigrationHash = ""
	canonical, _ := json.Marshal(struct {
		Definition Definition   `json:"definition"`
		Runtime    RuntimeState `json:"runtime"`
	}{definition, runtime})
	seal := sha256.Sum256(canonical)
	staged := definition
	staged.MigrationHash = hex.EncodeToString(seal[:])
	stagedJSON, _ := json.Marshal(staged)
	runtimeJSON, _ := json.Marshal(runtime)
	owner := [32]byte{3, 2}
	want := Boundary{AccountID: 7, MigrationJobID: 19, Operation: BarrierMigrationApply, OldConfigVersionID: 50, NewConfigVersionID: 51, BroadcastSessionID: 91, LiveSessionID: 81, LastOldRevision: 4, FirstNewRevision: 5, AppliedAt: now.Add(-time.Minute)}
	wantJSON, _ := json.Marshal(want)

	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta(barrierAccountQuery)).WithArgs(int64(7)).
		WillReturnRows(sqlmock.NewRows([]string{"config_version_id", "number", "revision", "runtime_json"}).AddRow(int64(51), uint64(3), uint64(7), runtimeJSON))
	mock.ExpectQuery(regexp.QuoteMeta(barrierOwnerQuery)).WithArgs(int64(7), owner[:], uint64(10)).WillReturnRows(sqlmock.NewRows([]string{"active"}).AddRow(true))
	mock.ExpectQuery(regexp.QuoteMeta(barrierJobQuery)).WithArgs(int64(19), int64(7)).
		WillReturnRows(sqlmock.NewRows([]string{"status", "expires_at", "base_config_version_number", "base_state_revision", "keep_room_suggestion", "definition_json", "runtime_json", "rollback_config_version_id", "rollback_runtime_json", "rollback_expires_at", "applied_config_version_id", "room_suggestion"}).
			AddRow("applied", now.Add(time.Hour), uint64(2), uint64(4), uint8(0), stagedJSON, runtimeJSON, int64(50), runtimeJSON, now.Add(7*24*time.Hour), int64(51), nil))
	mock.ExpectQuery(regexp.QuoteMeta(barrierNowQuery)).WillReturnRows(sqlmock.NewRows([]string{"now"}).AddRow(now))
	mock.ExpectQuery(regexp.QuoteMeta(barrierBoundaryQuery)).WithArgs(int64(7), string(BarrierMigrationApply), int64(19)).WillReturnRows(sqlmock.NewRows([]string{"event_data"}).AddRow(wantJSON))
	mock.ExpectCommit()

	got, err := repository.ActivateBarrier(context.Background(), BarrierCommand{
		AccountID: 7, ExpectedConfigVersionID: 51, ExpectedVersion: 3, ExpectedRevision: 7,
		Definition: definition, Runtime: runtime, Operation: BarrierMigrationApply, MigrationJobID: 19, IntegritySeal: seal,
		OwnerToken: owner, OwnerEpoch: 10, LiveSessionID: 82, BroadcastSessionID: 92, At: now,
	})
	if err != nil {
		t.Fatalf("cross-session idempotent ActivateBarrier() error = %v", err)
	}
	if got != want {
		t.Fatalf("cross-session idempotent ActivateBarrier() = %#v, want original %#v", got, want)
	}
	assertSQLMock(t, mock)
}

func TestRepositoryActivateBarrierRecoversPersistedBoundaryAfterAmbiguousCommit(t *testing.T) {
	repository, mock, closeDB := newMockRepository(t)
	defer closeDB()
	now := time.Date(2026, 8, 29, 12, 35, 0, 0, time.UTC)
	definition, runtime, err := Split(fixtureSnapshot())
	if err != nil {
		t.Fatal(err)
	}
	definition.MigrationHash = ""
	canonical, _ := json.Marshal(struct {
		Definition Definition   `json:"definition"`
		Runtime    RuntimeState `json:"runtime"`
	}{definition, runtime})
	seal := sha256.Sum256(canonical)
	staged := definition
	staged.MigrationHash = hex.EncodeToString(seal[:])
	stagedJSON, _ := json.Marshal(staged)
	runtimeJSON, _ := json.Marshal(runtime)
	owner := [32]byte{5}
	want := Boundary{AccountID: 7, MigrationJobID: 19, Operation: BarrierMigrationApply, OldConfigVersionID: 50, NewConfigVersionID: 51, BroadcastSessionID: 91, LiveSessionID: 81, LastOldRevision: 4, FirstNewRevision: 5, AppliedAt: now.Add(-time.Minute)}
	wantJSON, _ := json.Marshal(want)

	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta(barrierAccountQuery)).WithArgs(int64(7)).
		WillReturnRows(sqlmock.NewRows([]string{"config_version_id", "number", "revision", "runtime_json"}).AddRow(int64(51), uint64(3), uint64(5), runtimeJSON))
	mock.ExpectQuery(regexp.QuoteMeta(barrierOwnerQuery)).WithArgs(int64(7), owner[:], uint64(9)).WillReturnRows(sqlmock.NewRows([]string{"active"}).AddRow(true))
	mock.ExpectQuery(regexp.QuoteMeta(barrierJobQuery)).WithArgs(int64(19), int64(7)).
		WillReturnRows(sqlmock.NewRows([]string{"status", "expires_at", "base_config_version_number", "base_state_revision", "keep_room_suggestion", "definition_json", "runtime_json", "rollback_config_version_id", "rollback_runtime_json", "rollback_expires_at", "applied_config_version_id", "room_suggestion"}).
			AddRow("applied", now.Add(time.Hour), uint64(2), uint64(4), uint8(0), stagedJSON, runtimeJSON, int64(50), runtimeJSON, now.Add(7*24*time.Hour), int64(51), nil))
	mock.ExpectQuery(regexp.QuoteMeta(barrierNowQuery)).WillReturnRows(sqlmock.NewRows([]string{"now"}).AddRow(now))
	mock.ExpectQuery(regexp.QuoteMeta(barrierBoundaryQuery)).WithArgs(int64(7), string(BarrierMigrationApply), int64(19)).WillReturnRows(sqlmock.NewRows([]string{"event_data"}).AddRow(wantJSON))
	mock.ExpectCommit()

	got, err := repository.ActivateBarrier(context.Background(), BarrierCommand{
		AccountID: 7, ExpectedConfigVersionID: 50, ExpectedVersion: 2, ExpectedRevision: 4,
		Definition: definition, Runtime: runtime, Operation: BarrierMigrationApply, MigrationJobID: 19, IntegritySeal: seal,
		OwnerToken: owner, OwnerEpoch: 9, LiveSessionID: 81, BroadcastSessionID: 91, At: now,
	})
	if err != nil {
		t.Fatalf("ambiguous-commit recovery error = %v", err)
	}
	if got != want {
		t.Fatalf("ambiguous-commit recovery = %#v, want %#v", got, want)
	}
	assertSQLMock(t, mock)
}

func TestRepositoryActivateBarrierVerifiesDurableBoundaryAfterCommitReturnsAmbiguousError(t *testing.T) {
	repository, mock, closeDB := newMockRepository(t)
	defer closeDB()
	command, want := expectAmbiguousBarrierApply(t, mock)
	wantJSON, _ := json.Marshal(want)
	mock.ExpectQuery(regexp.QuoteMeta(barrierCommitVerificationStateQueryForTest)).WithArgs(int64(19), int64(7)).
		WillReturnRows(sqlmock.NewRows([]string{"status", "config_version_id", "revision"}).AddRow("applied", int64(51), uint64(5)))
	mock.ExpectQuery(regexp.QuoteMeta(barrierBoundaryQuery)).WithArgs(int64(7), string(BarrierMigrationApply), int64(19)).
		WillReturnRows(sqlmock.NewRows([]string{"event_data"}).AddRow(wantJSON))

	got, err := repository.ActivateBarrier(context.Background(), command)
	if err != nil {
		t.Fatalf("ActivateBarrier(ambiguous commit) error = %v", err)
	}
	if got != want {
		t.Fatalf("ActivateBarrier(ambiguous commit) = %#v, want %#v", got, want)
	}
	assertSQLMock(t, mock)
}

func TestRepositoryActivateBarrierReturnsCommitUncertainWhenFreshVerificationCannotResolve(t *testing.T) {
	repository, mock, closeDB := newMockRepository(t)
	defer closeDB()
	command, _ := expectAmbiguousBarrierApply(t, mock)
	mock.ExpectQuery(regexp.QuoteMeta(barrierCommitVerificationStateQueryForTest)).WithArgs(int64(19), int64(7)).
		WillReturnError(errors.New("verification unavailable"))

	_, err := repository.ActivateBarrier(context.Background(), command)
	if !errors.Is(err, ErrCommitUncertain) {
		t.Fatalf("ActivateBarrier(unresolved commit) error = %v, want ErrCommitUncertain", err)
	}
	assertSQLMock(t, mock)
}

func TestRepositoryActivateBarrierRollsBackConfigurationWhenBoundaryAuditFails(t *testing.T) {
	repository, mock, closeDB := newMockRepository(t)
	defer closeDB()
	now := time.Date(2026, 8, 29, 12, 40, 0, 0, time.UTC)
	definition, runtime, err := Split(fixtureSnapshot())
	if err != nil {
		t.Fatal(err)
	}
	definition.MigrationHash = ""
	canonical, _ := json.Marshal(struct {
		Definition Definition   `json:"definition"`
		Runtime    RuntimeState `json:"runtime"`
	}{definition, runtime})
	seal := sha256.Sum256(canonical)
	staged := definition
	staged.MigrationHash = hex.EncodeToString(seal[:])
	stagedJSON, _ := json.Marshal(staged)
	runtimeJSON, _ := json.Marshal(runtime)
	owner := [32]byte{4}

	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta(barrierAccountQuery)).WithArgs(int64(7)).WillReturnRows(sqlmock.NewRows([]string{"config_version_id", "number", "revision", "runtime_json"}).AddRow(int64(50), uint64(2), uint64(4), runtimeJSON))
	mock.ExpectQuery(regexp.QuoteMeta(barrierOwnerQuery)).WithArgs(int64(7), owner[:], uint64(9)).WillReturnRows(sqlmock.NewRows([]string{"active"}).AddRow(true))
	mock.ExpectQuery(regexp.QuoteMeta(barrierJobQuery)).WithArgs(int64(19), int64(7)).WillReturnRows(sqlmock.NewRows([]string{"status", "expires_at", "base_config_version_number", "base_state_revision", "keep_room_suggestion", "definition_json", "runtime_json", "rollback_config_version_id", "rollback_runtime_json", "rollback_expires_at", "applied_config_version_id", "room_suggestion"}).AddRow("pending", now.Add(time.Hour), uint64(2), uint64(4), uint8(0), stagedJSON, runtimeJSON, nil, nil, nil, nil, nil))
	mock.ExpectQuery(regexp.QuoteMeta(barrierNowQuery)).WillReturnRows(sqlmock.NewRows([]string{"now"}).AddRow(now))
	mock.ExpectQuery(regexp.QuoteMeta(barrierNextVersionQuery)).WithArgs(int64(7)).WillReturnRows(sqlmock.NewRows([]string{"number"}).AddRow(uint64(3)))
	mock.ExpectExec(regexp.QuoteMeta(barrierInsertVersionQuery)).WithArgs(int64(7), uint64(3), sqlmock.AnyArg(), "migration", now).WillReturnResult(sqlmock.NewResult(51, 1))
	mock.ExpectExec(regexp.QuoteMeta(barrierUpsertRuntimeQuery)).WithArgs(int64(7), int64(51), uint64(5), sqlmock.AnyArg(), now).WillReturnResult(sqlmock.NewResult(0, 2))
	mock.ExpectExec(regexp.QuoteMeta(barrierUpsertActiveQuery)).WithArgs(int64(7), int64(51), now).WillReturnResult(sqlmock.NewResult(0, 2))
	mock.ExpectExec(regexp.QuoteMeta(barrierApplyJobQuery)).WithArgs(int64(50), sqlmock.AnyArg(), now.Add(7*24*time.Hour), int64(51), now, int64(19), int64(7)).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(regexp.QuoteMeta(barrierInsertAuditQuery)).WithArgs("configuration_barrier", int64(7), int64(7), sqlmock.AnyArg(), now).WillReturnError(errors.New("audit unavailable"))
	mock.ExpectRollback()

	_, err = repository.ActivateBarrier(context.Background(), BarrierCommand{
		AccountID: 7, ExpectedConfigVersionID: 50, ExpectedVersion: 2, ExpectedRevision: 4,
		Definition: definition, Runtime: runtime, Operation: BarrierMigrationApply, MigrationJobID: 19, IntegritySeal: seal, OwnerToken: owner, OwnerEpoch: 9, LiveSessionID: 81, BroadcastSessionID: 91, At: now,
	})
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("ActivateBarrier() error = %v, want ErrUnavailable", err)
	}
	assertSQLMock(t, mock)
}

func TestRepositoryActivateBarrierRejectsStaleLiveOwnerBeforeLifecycleWrites(t *testing.T) {
	repository, mock, closeDB := newMockRepository(t)
	defer closeDB()
	now := time.Date(2026, 8, 29, 12, 45, 0, 0, time.UTC)
	definition, runtime, err := Split(fixtureSnapshot())
	if err != nil {
		t.Fatal(err)
	}
	definition.MigrationHash = ""
	canonical, _ := json.Marshal(struct {
		Definition Definition   `json:"definition"`
		Runtime    RuntimeState `json:"runtime"`
	}{definition, runtime})
	seal := sha256.Sum256(canonical)
	owner := [32]byte{1, 2, 3}

	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta(barrierAccountQuery)).WithArgs(int64(7)).WillReturnRows(sqlmock.NewRows([]string{"config_version_id", "number", "revision", "runtime_json"}).AddRow(int64(50), uint64(2), uint64(4), []byte(`{"attributeValues":{"health":3}}`)))
	mock.ExpectQuery(regexp.QuoteMeta(barrierOwnerQuery)).WithArgs(int64(7), owner[:], uint64(9)).WillReturnRows(sqlmock.NewRows([]string{"active"}).AddRow(false))
	mock.ExpectRollback()

	_, err = repository.ActivateBarrier(context.Background(), BarrierCommand{
		AccountID: 7, ExpectedConfigVersionID: 50, ExpectedVersion: 2, ExpectedRevision: 4,
		Definition: definition, Runtime: runtime, Operation: BarrierMigrationApply, MigrationJobID: 19, IntegritySeal: seal,
		OwnerToken: owner, OwnerEpoch: 9, LiveSessionID: 81, BroadcastSessionID: 91, At: now,
	})
	if !errors.Is(err, ErrOwnership) {
		t.Fatalf("ActivateBarrier(stale owner) error = %v, want ErrOwnership", err)
	}
	assertSQLMock(t, mock)
}

func TestRepositoryActivateBarrierRejectsStaleOwnerForPendingAfterSession(t *testing.T) {
	repository, mock, closeDB := newMockRepository(t)
	defer closeDB()
	now := time.Date(2026, 8, 29, 12, 46, 0, 0, time.UTC)
	definition, runtime, err := Split(fixtureSnapshot())
	if err != nil {
		t.Fatal(err)
	}
	definition.MigrationHash = ""
	canonical, _ := json.Marshal(struct {
		Definition Definition   `json:"definition"`
		Runtime    RuntimeState `json:"runtime"`
	}{definition, runtime})
	seal := sha256.Sum256(canonical)
	owner := [32]byte{4, 5, 6}

	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta(barrierAccountQuery)).WithArgs(int64(7)).WillReturnRows(sqlmock.NewRows([]string{"config_version_id", "number", "revision", "runtime_json"}).AddRow(int64(50), uint64(2), uint64(4), []byte(`{"attributeValues":{"health":3}}`)))
	mock.ExpectQuery(regexp.QuoteMeta(barrierOwnerQuery)).WithArgs(int64(7), owner[:], uint64(10)).WillReturnRows(sqlmock.NewRows([]string{"active"}).AddRow(false))
	mock.ExpectRollback()

	_, err = repository.ActivateBarrier(context.Background(), BarrierCommand{
		AccountID: 7, ExpectedConfigVersionID: 50, ExpectedVersion: 2, ExpectedRevision: 4,
		Definition: definition, Runtime: runtime, Operation: BarrierMigrationApply, MigrationJobID: 19, IntegritySeal: seal,
		OwnerToken: owner, OwnerEpoch: 10, At: now,
	})
	if !errors.Is(err, ErrOwnership) {
		t.Fatalf("ActivateBarrier(stale post-session owner) error = %v, want ErrOwnership", err)
	}
	assertSQLMock(t, mock)
}

func TestRepositoryActivateRollsBackWhenExpectedVersionDoesNotMatch(t *testing.T) {
	repository, mock, closeDB := newMockRepository(t)
	defer closeDB()
	definition, runtime, err := Split(fixtureSnapshot())
	if err != nil {
		t.Fatal(err)
	}

	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta("SELECT active.config_version_id, COALESCE(v.number, 0) FROM streamer_accounts AS a LEFT JOIN account_active_config AS active ON active.account_id = a.id LEFT JOIN account_config_versions AS v ON v.account_id = active.account_id AND v.id = active.config_version_id WHERE a.id = ? FOR UPDATE")).
		WithArgs(int64(7)).
		WillReturnRows(sqlmock.NewRows([]string{"config_version_id", "number"}).AddRow(int64(51), uint64(2)))
	mock.ExpectRollback()

	_, _, err = repository.Activate(context.Background(), ActivationCommand{
		AccountID: 7, ExpectedVersion: 1, ExpectedRevision: 0, Definition: definition, Runtime: runtime, Source: "manual", At: time.Now().UTC(),
	})
	if !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("Activate() error = %v, want ErrRevisionConflict", err)
	}
	assertSQLMock(t, mock)
}

func TestRepositoryActivateAcceptsExistingRuntimeUpsertResult(t *testing.T) {
	repository, mock, closeDB := newMockRepository(t)
	defer closeDB()
	definition, runtime, err := Split(fixtureSnapshot())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 16, 12, 2, 0, 0, time.UTC)

	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta("SELECT active.config_version_id, COALESCE(v.number, 0) FROM streamer_accounts AS a LEFT JOIN account_active_config AS active ON active.account_id = a.id LEFT JOIN account_config_versions AS v ON v.account_id = active.account_id AND v.id = active.config_version_id WHERE a.id = ? FOR UPDATE")).
		WithArgs(int64(7)).
		WillReturnRows(sqlmock.NewRows([]string{"config_version_id", "number"}).AddRow(int64(50), uint64(2)))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT revision FROM account_runtime_state WHERE account_id = ? FOR UPDATE")).
		WithArgs(int64(7)).
		WillReturnRows(sqlmock.NewRows([]string{"revision"}).AddRow(uint64(4)))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT COALESCE(MAX(number), 0) + 1 FROM account_config_versions WHERE account_id = ?")).
		WithArgs(int64(7)).
		WillReturnRows(sqlmock.NewRows([]string{"number"}).AddRow(uint64(3)))
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO account_config_versions (account_id, number, definition_json, source, created_at) VALUES (?, ?, ?, ?, ?)")).
		WithArgs(int64(7), uint64(3), jsonWithoutImageURL{}, "manual", now).
		WillReturnResult(sqlmock.NewResult(51, 1))
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO account_runtime_state (account_id, config_version_id, revision, runtime_json, updated_at) VALUES (?, ?, ?, ?, ?) ON DUPLICATE KEY UPDATE config_version_id = VALUES(config_version_id), revision = VALUES(revision), runtime_json = VALUES(runtime_json), updated_at = VALUES(updated_at)")).
		WithArgs(int64(7), int64(51), uint64(5), sqlmock.AnyArg(), now).
		WillReturnResult(sqlmock.NewResult(0, 2))
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO account_active_config (account_id, config_version_id, updated_at) VALUES (?, ?, ?) ON DUPLICATE KEY UPDATE config_version_id = VALUES(config_version_id), updated_at = VALUES(updated_at)")).
		WithArgs(int64(7), int64(51), now).
		WillReturnResult(sqlmock.NewResult(0, 2))
	mock.ExpectCommit()

	version, state, err := repository.Activate(context.Background(), ActivationCommand{AccountID: 7, ExpectedVersion: 2, ExpectedRevision: 4, Definition: definition, Runtime: runtime, Source: "manual", At: now})
	if err != nil {
		t.Fatalf("Activate() error = %v", err)
	}
	if version.Number != 3 || state.Revision != 5 || state.ConfigVersionID != 51 {
		t.Fatalf("Activate() = (%#v, %#v), want version 3 and state revision 5", version, state)
	}
	assertSQLMock(t, mock)
}

func TestRepositoryCompareAndSwapStateIncrementsOneRevision(t *testing.T) {
	repository, mock, closeDB := newMockRepository(t)
	defer closeDB()
	_, runtime, err := Split(fixtureSnapshot())
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 8, 16, 12, 1, 0, 0, time.UTC)

	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta("SELECT config_version_id, revision FROM account_runtime_state WHERE account_id = ? FOR UPDATE")).
		WithArgs(int64(7)).
		WillReturnRows(sqlmock.NewRows([]string{"config_version_id", "revision"}).AddRow(int64(51), uint64(4)))
	mock.ExpectExec(regexp.QuoteMeta("UPDATE account_runtime_state SET runtime_json = ?, revision = ?, updated_at = ? WHERE account_id = ? AND revision = ?")).
		WithArgs(sqlmock.AnyArg(), uint64(5), now, int64(7), uint64(4)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	state, err := repository.CompareAndSwapState(context.Background(), UpdateStateCommand{AccountID: 7, ExpectedRevision: 4, Runtime: runtime, UpdatedAt: now})
	if err != nil {
		t.Fatalf("CompareAndSwapState() error = %v", err)
	}
	if state.AccountID != 7 || state.ConfigVersionID != 51 || state.Revision != 5 || !state.UpdatedAt.Equal(now) {
		t.Fatalf("state = %#v", state)
	}
	assertSQLMock(t, mock)
}

func TestRepositoryCompareAndSwapStateRollsBackConflict(t *testing.T) {
	repository, mock, closeDB := newMockRepository(t)
	defer closeDB()
	_, runtime, err := Split(fixtureSnapshot())
	if err != nil {
		t.Fatal(err)
	}

	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta("SELECT config_version_id, revision FROM account_runtime_state WHERE account_id = ? FOR UPDATE")).
		WithArgs(int64(7)).
		WillReturnRows(sqlmock.NewRows([]string{"config_version_id", "revision"}).AddRow(int64(51), uint64(5)))
	mock.ExpectRollback()

	_, err = repository.CompareAndSwapState(context.Background(), UpdateStateCommand{AccountID: 7, ExpectedRevision: 4, Runtime: runtime, UpdatedAt: time.Now().UTC()})
	if !errors.Is(err, ErrRevisionConflict) {
		t.Fatalf("CompareAndSwapState() error = %v, want ErrRevisionConflict", err)
	}
	assertSQLMock(t, mock)
}

func TestRepositoryLoadActiveDecodesVersionAndRuntime(t *testing.T) {
	repository, mock, closeDB := newMockRepository(t)
	defer closeDB()
	definition, runtime, err := Split(fixtureSnapshot())
	if err != nil {
		t.Fatal(err)
	}
	definitionJSON, err := marshalDefinition(definition)
	if err != nil {
		t.Fatal(err)
	}
	runtimeJSON, err := marshalRuntime(runtime)
	if err != nil {
		t.Fatal(err)
	}
	createdAt := time.Date(2026, 8, 16, 12, 0, 0, 0, time.UTC)
	updatedAt := createdAt.Add(time.Minute)

	mock.ExpectQuery(regexp.QuoteMeta("SELECT v.id, v.account_id, v.number, v.definition_json, v.source, v.created_at, s.config_version_id, s.revision, s.runtime_json, s.updated_at FROM streamer_accounts AS a JOIN account_active_config AS active ON active.account_id = a.id JOIN account_config_versions AS v ON v.account_id = active.account_id AND v.id = active.config_version_id JOIN account_runtime_state AS s ON s.account_id = a.id AND s.config_version_id = active.config_version_id WHERE a.id = ?")).
		WithArgs(int64(7)).
		WillReturnRows(sqlmock.NewRows([]string{"id", "account_id", "number", "definition_json", "source", "created_at", "config_version_id", "revision", "runtime_json", "updated_at"}).AddRow(int64(51), int64(7), uint64(1), definitionJSON, "manual", createdAt, int64(51), uint64(4), runtimeJSON, updatedAt))

	version, state, err := repository.LoadActive(context.Background(), 7)
	if err != nil {
		t.Fatalf("LoadActive() error = %v", err)
	}
	if version.ID != 51 || version.AccountID != 7 || version.Number != 1 || version.Source != "manual" || !version.CreatedAt.Equal(createdAt) {
		t.Fatalf("version = %#v", version)
	}
	if state.AccountID != 7 || state.ConfigVersionID != 51 || state.Revision != 4 || !state.UpdatedAt.Equal(updatedAt) {
		t.Fatalf("state = %#v", state)
	}
	assertSQLMock(t, mock)
}

func TestRepositoryRejectsUnknownVersionSourceBeforeDatabaseWork(t *testing.T) {
	repository, mock, closeDB := newMockRepository(t)
	defer closeDB()
	definition, runtime, err := Split(fixtureSnapshot())
	if err != nil {
		t.Fatal(err)
	}

	_, _, err = repository.Activate(context.Background(), ActivationCommand{AccountID: 7, Definition: definition, Runtime: runtime, Source: "import", At: time.Now().UTC()})
	if !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("Activate() error = %v, want ErrInvalidInput", err)
	}
	assertSQLMock(t, mock)
}

func TestRepositoryUpsertRoomSuggestionDoesNotTouchConfigurationOrSessions(t *testing.T) {
	repository, mock, closeDB := newMockRepository(t)
	defer closeDB()
	now := time.Date(2026, 8, 16, 12, 3, 0, 0, time.UTC)

	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO account_room_suggestions (account_id, room_id, suggested_at) VALUES (?, ?, ?) ON DUPLICATE KEY UPDATE room_id = VALUES(room_id), suggested_at = VALUES(suggested_at)")).
		WithArgs(int64(7), "12345", now).
		WillReturnResult(sqlmock.NewResult(0, 2))

	err := repository.UpsertRoomSuggestion(context.Background(), RoomSuggestion{AccountID: 7, RoomID: "12345", SuggestedAt: now})
	if err != nil {
		t.Fatalf("UpsertRoomSuggestion() error = %v", err)
	}
	assertSQLMock(t, mock)
}

func TestRepositoryUpsertRoomSuggestionAcceptsSameValueNoop(t *testing.T) {
	repository, mock, closeDB := newMockRepository(t)
	defer closeDB()
	now := time.Date(2026, 8, 16, 12, 4, 0, 0, time.UTC)
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO account_room_suggestions (account_id, room_id, suggested_at) VALUES (?, ?, ?) ON DUPLICATE KEY UPDATE room_id = VALUES(room_id), suggested_at = VALUES(suggested_at)")).
		WithArgs(int64(7), "12345", now).
		WillReturnResult(sqlmock.NewResult(0, 0))

	if err := repository.UpsertRoomSuggestion(context.Background(), RoomSuggestion{AccountID: 7, RoomID: "12345", SuggestedAt: now}); err != nil {
		t.Fatalf("UpsertRoomSuggestion() error = %v", err)
	}
	assertSQLMock(t, mock)
}

const barrierCommitVerificationStateQueryForTest = "SELECT j.status, active.config_version_id, COALESCE(s.revision, 0) FROM migration_jobs AS j LEFT JOIN account_active_config AS active ON active.account_id = j.account_id LEFT JOIN account_runtime_state AS s ON s.account_id = j.account_id AND s.config_version_id = active.config_version_id WHERE j.id = ? AND j.account_id = ?"

func expectAmbiguousBarrierApply(t *testing.T, mock sqlmock.Sqlmock) (BarrierCommand, Boundary) {
	t.Helper()
	now := time.Date(2026, 8, 29, 13, 0, 0, 0, time.UTC)
	definition, runtime, err := Split(fixtureSnapshot())
	if err != nil {
		t.Fatal(err)
	}
	definition.MigrationHash = ""
	canonical, err := json.Marshal(struct {
		Definition Definition   `json:"definition"`
		Runtime    RuntimeState `json:"runtime"`
	}{definition, runtime})
	if err != nil {
		t.Fatal(err)
	}
	seal := sha256.Sum256(canonical)
	staged := definition
	staged.MigrationHash = hex.EncodeToString(seal[:])
	stagedJSON, _ := json.Marshal(staged)
	runtimeJSON, _ := json.Marshal(runtime)
	owner := [32]byte{8}
	want := Boundary{AccountID: 7, MigrationJobID: 19, Operation: BarrierMigrationApply, OldConfigVersionID: 50, NewConfigVersionID: 51, BroadcastSessionID: 91, LiveSessionID: 81, LastOldRevision: 4, FirstNewRevision: 5, AppliedAt: now}

	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta(barrierAccountQuery)).WithArgs(int64(7)).
		WillReturnRows(sqlmock.NewRows([]string{"config_version_id", "number", "revision", "runtime_json"}).AddRow(int64(50), uint64(2), uint64(4), runtimeJSON))
	mock.ExpectQuery(regexp.QuoteMeta(barrierOwnerQuery)).WithArgs(int64(7), owner[:], uint64(9)).WillReturnRows(sqlmock.NewRows([]string{"active"}).AddRow(true))
	mock.ExpectQuery(regexp.QuoteMeta(barrierJobQuery)).WithArgs(int64(19), int64(7)).
		WillReturnRows(sqlmock.NewRows([]string{"status", "expires_at", "base_config_version_number", "base_state_revision", "keep_room_suggestion", "definition_json", "runtime_json", "rollback_config_version_id", "rollback_runtime_json", "rollback_expires_at", "applied_config_version_id", "room_suggestion"}).
			AddRow("pending", now.Add(time.Hour), uint64(2), uint64(4), uint8(0), stagedJSON, runtimeJSON, nil, nil, nil, nil, nil))
	mock.ExpectQuery(regexp.QuoteMeta(barrierNowQuery)).WillReturnRows(sqlmock.NewRows([]string{"now"}).AddRow(now))
	mock.ExpectQuery(regexp.QuoteMeta(barrierNextVersionQuery)).WithArgs(int64(7)).WillReturnRows(sqlmock.NewRows([]string{"number"}).AddRow(uint64(3)))
	mock.ExpectExec(regexp.QuoteMeta(barrierInsertVersionQuery)).WithArgs(int64(7), uint64(3), jsonWithoutMigrationHash{}, "migration", now).WillReturnResult(sqlmock.NewResult(51, 1))
	mock.ExpectExec(regexp.QuoteMeta(barrierUpsertRuntimeQuery)).WithArgs(int64(7), int64(51), uint64(5), sqlmock.AnyArg(), now).WillReturnResult(sqlmock.NewResult(0, 2))
	mock.ExpectExec(regexp.QuoteMeta(barrierUpsertActiveQuery)).WithArgs(int64(7), int64(51), now).WillReturnResult(sqlmock.NewResult(0, 2))
	mock.ExpectExec(regexp.QuoteMeta(barrierApplyJobQuery)).WithArgs(int64(50), sqlmock.AnyArg(), now.Add(7*24*time.Hour), int64(51), now, int64(19), int64(7)).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(regexp.QuoteMeta(barrierInsertAuditQuery)).WithArgs("configuration_barrier", int64(7), int64(7), boundaryJSONMatcher{want: want}, now).WillReturnResult(sqlmock.NewResult(103, 1))
	mock.ExpectCommit().WillReturnError(errors.New("ambiguous commit"))

	return BarrierCommand{
		AccountID: 7, ExpectedConfigVersionID: 50, ExpectedVersion: 2, ExpectedRevision: 4,
		Definition: definition, Runtime: runtime, Operation: BarrierMigrationApply, MigrationJobID: 19, IntegritySeal: seal,
		OwnerToken: owner, OwnerEpoch: 9, LiveSessionID: 81, BroadcastSessionID: 91, At: now,
	}, want
}

func newMockRepository(t *testing.T) (*sqlRepository, sqlmock.Sqlmock, func()) {
	t.Helper()
	database, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	return NewRepository(database), mock, func() { _ = database.Close() }
}

func assertSQLMock(t *testing.T, mock sqlmock.Sqlmock) {
	t.Helper()
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("SQL expectations: %v", err)
	}
}

type jsonWithoutImageURL struct{}

func (jsonWithoutImageURL) Match(value driver.Value) bool {
	encoded, ok := value.([]byte)
	return ok && !bytes.Contains(encoded, []byte(`"imageUrl"`))
}

type jsonWithoutMigrationHash struct{}

func (jsonWithoutMigrationHash) Match(value driver.Value) bool {
	encoded, ok := value.([]byte)
	if !ok {
		return false
	}
	var decoded map[string]any
	if json.Unmarshal(encoded, &decoded) != nil {
		return false
	}
	_, present := decoded["migrationHash"]
	return !present
}

type boundaryJSONMatcher struct{ want Boundary }

func (matcher boundaryJSONMatcher) Match(value driver.Value) bool {
	encoded, ok := value.([]byte)
	if !ok {
		return false
	}
	var got Boundary
	return json.Unmarshal(encoded, &got) == nil && got == matcher.want
}
