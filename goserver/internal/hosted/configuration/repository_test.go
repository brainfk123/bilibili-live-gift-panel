package configuration

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"database/sql/driver"
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
