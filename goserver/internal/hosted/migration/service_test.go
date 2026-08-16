package migration

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestPreviewRecanonicalizesAndDoesNotExposeConfiguration(t *testing.T) {
	now := time.Date(2026, 8, 16, 9, 30, 0, 0, time.UTC)
	envelope := decodedEnvelope(t)
	envelope.Hash = [32]byte{}
	repository := &recordingPreviewRepository{result: storedPreview{ID: 8, AccountID: 7, ExpiresAt: now.Add(24 * time.Hour)}}
	service := NewService(repository, func() time.Time { return now })

	preview, err := service.Preview(context.Background(), 7, envelope)
	if err != nil {
		t.Fatal(err)
	}
	if preview.ID != 8 || !preview.ExpiresAt.Equal(now.Add(24*time.Hour)) {
		t.Fatalf("unexpected preview: %#v", preview)
	}
	if repository.command.AccountID != 7 || repository.command.Hash == ([32]byte{}) {
		t.Fatalf("preview did not use a fresh account-owned hash: %#v", repository.command)
	}
	if repository.command.Definition.Attributes[0].ID != "health" || repository.command.Runtime.AttributeValues["health"] != 1 {
		t.Fatal("normalized configuration was not sent to persistence seam")
	}
}

func TestPreviewRejectsRepositoryOwnershipViolation(t *testing.T) {
	now := time.Date(2026, 8, 16, 9, 30, 0, 0, time.UTC)
	service := NewService(&recordingPreviewRepository{result: storedPreview{ID: 8, AccountID: 9, ExpiresAt: now.Add(24 * time.Hour)}}, func() time.Time { return now })
	if _, err := service.Preview(context.Background(), 7, decodedEnvelope(t)); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("got %v, want unavailable", err)
	}
}

func TestServiceApplyUsesOnlyTheAuthenticatedOwnerAndKeepsSuggestionExplicit(t *testing.T) {
	repository := &recordingLifecycleRepository{recordingPreviewRepository: recordingPreviewRepository{}, applyResult: storedJob{ID: 19, AccountID: 7, Status: jobApplied}}
	service := NewService(repository, time.Now)

	result, err := service.Apply(context.Background(), 7, 19, true)
	if err != nil {
		t.Fatal(err)
	}
	if result.ID != 19 || result.Status != "applied" {
		t.Fatalf("Apply() = %#v", result)
	}
	if repository.apply.AccountID != 7 || repository.apply.JobID != 19 || !repository.apply.KeepRoomSuggestion {
		t.Fatalf("Apply() command = %#v", repository.apply)
	}
	if _, err := service.Apply(context.Background(), 0, 19, false); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("Apply() with invalid owner error = %v", err)
	}
}

func TestSQLRepositoryReusesUnexpiredHashWithoutQuotaConsumption(t *testing.T) {
	database, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	now := time.Date(2026, 8, 16, 9, 30, 0, 0, time.UTC)
	command := previewCommandForTest(t, now)
	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta(previewBaseQuery)).WithArgs(int64(7)).WillReturnRows(sqlmock.NewRows([]string{"number", "revision"}).AddRow(4, 9))
	expectPreviewNow(mock, now)
	mock.ExpectQuery(regexp.QuoteMeta(previewExistingQuery)).WithArgs(int64(7), command.Hash[:]).WillReturnRows(activePreviewRows(3, "previewed", now.Add(time.Hour), command))
	mock.ExpectCommit()

	stored, err := NewRepository(database).Preview(context.Background(), command)
	if err != nil {
		t.Fatal(err)
	}
	if stored.ID != 3 || !stored.Reused {
		t.Fatalf("expected reusable preview, got %#v", stored)
	}
	if stored.RoomSuggestion != "persisted-room" || stored.Source.AppVersion != "persisted-app" || stored.Source.ConfigurationSchemaVersion != 4 || stored.Report.Counts != command.Counts || stored.Hash != command.Hash {
		t.Fatalf("reuse did not return persisted preview metadata: %#v", stored)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestSQLRepositoryUsesDatabaseUTCForNewGenerationAcrossMidnight(t *testing.T) {
	database, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	databaseNow := time.Date(2026, 8, 17, 0, 0, 1, 0, time.UTC)
	command := previewCommandForTest(t, databaseNow.Add(-48*time.Hour))
	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta(previewBaseQuery)).WithArgs(int64(7)).WillReturnRows(sqlmock.NewRows([]string{"number", "revision"}).AddRow(0, 0))
	expectPreviewNow(mock, databaseNow)
	mock.ExpectQuery(regexp.QuoteMeta(previewExistingQuery)).WithArgs(int64(7), command.Hash[:]).WillReturnError(sql.ErrNoRows)
	start := time.Date(2026, 8, 17, 0, 0, 0, 0, time.UTC)
	mock.ExpectQuery(regexp.QuoteMeta(previewQuotaQuery)).WithArgs(int64(7), start, start.Add(24*time.Hour)).WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
	mock.ExpectExec(regexp.QuoteMeta(previewInsertQuery)).WithArgs(int64(7), command.Hash[:], "previewed", uint64(0), uint64(0), sqlmock.AnyArg(), sqlmock.AnyArg(), "12345", "0.4.4", 5, sqlmock.AnyArg(), databaseNow, databaseNow.Add(24*time.Hour)).WillReturnResult(sqlmock.NewResult(46, 1))
	mock.ExpectCommit()
	stored, err := NewRepository(database).Preview(context.Background(), command)
	if err != nil {
		t.Fatal(err)
	}
	if !stored.ExpiresAt.Equal(databaseNow.Add(24 * time.Hour)) {
		t.Fatalf("expiry = %v, want database-time deadline", stored.ExpiresAt)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestSQLRepositoryCreatesAtQuotaBoundaryUnderAccountLock(t *testing.T) {
	database, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	now := time.Date(2026, 8, 16, 9, 30, 0, 0, time.UTC)
	command := previewCommandForTest(t, now)
	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta(previewBaseQuery)).WithArgs(int64(7)).WillReturnRows(sqlmock.NewRows([]string{"number", "revision"}).AddRow(0, 0))
	expectPreviewNow(mock, now)
	mock.ExpectQuery(regexp.QuoteMeta(previewExistingQuery)).WithArgs(int64(7), command.Hash[:]).WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery(regexp.QuoteMeta(previewQuotaQuery)).WithArgs(int64(7), now.Truncate(24*time.Hour), now.Truncate(24*time.Hour).Add(24*time.Hour)).WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(4))
	mock.ExpectExec(regexp.QuoteMeta(previewInsertQuery)).WithArgs(int64(7), command.Hash[:], "previewed", uint64(0), uint64(0), sqlmock.AnyArg(), sqlmock.AnyArg(), "12345", "0.4.4", 5, sqlmock.AnyArg(), now, now.Add(24*time.Hour)).WillReturnResult(sqlmock.NewResult(42, 1))
	mock.ExpectCommit()

	stored, err := NewRepository(database).Preview(context.Background(), command)
	if err != nil {
		t.Fatal(err)
	}
	if stored.ID != 42 || stored.Reused {
		t.Fatalf("expected a new preview, got %#v", stored)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestSQLRepositoryRejectsSixthSuccessfulPreviewAndRollsBack(t *testing.T) {
	database, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	now := time.Date(2026, 8, 16, 9, 30, 0, 0, time.UTC)
	command := previewCommandForTest(t, now)
	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta(previewBaseQuery)).WithArgs(int64(7)).WillReturnRows(sqlmock.NewRows([]string{"number", "revision"}).AddRow(0, 0))
	expectPreviewNow(mock, now)
	mock.ExpectQuery(regexp.QuoteMeta(previewExistingQuery)).WithArgs(int64(7), command.Hash[:]).WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery(regexp.QuoteMeta(previewQuotaQuery)).WithArgs(int64(7), now.Truncate(24*time.Hour), now.Truncate(24*time.Hour).Add(24*time.Hour)).WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(5))
	mock.ExpectRollback()

	if _, err := NewRepository(database).Preview(context.Background(), command); !errors.Is(err, ErrPreviewLimit) {
		t.Fatalf("got %v, want preview limit", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestSQLRepositoryRefreshesExpiredPreviewWithFreshBaseSnapshot(t *testing.T) {
	database, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	now := time.Date(2026, 8, 16, 9, 30, 0, 0, time.UTC)
	command := previewCommandForTest(t, now)
	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta(previewBaseQuery)).WithArgs(int64(7)).WillReturnRows(sqlmock.NewRows([]string{"number", "revision"}).AddRow(8, 12))
	expectPreviewNow(mock, now)
	mock.ExpectQuery(regexp.QuoteMeta(previewExistingQuery)).WithArgs(int64(7), command.Hash[:]).WillReturnRows(activePreviewRows(3, "previewed", now.Add(-time.Second), command))
	mock.ExpectQuery(regexp.QuoteMeta(previewQuotaQuery)).WithArgs(int64(7), now.Truncate(24*time.Hour), now.Truncate(24*time.Hour).Add(24*time.Hour)).WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(2))
	mock.ExpectExec(regexp.QuoteMeta(previewExpireQuery)).WithArgs(int64(3), int64(7), now).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(regexp.QuoteMeta(previewInsertQuery)).WithArgs(int64(7), command.Hash[:], "previewed", uint64(8), uint64(12), sqlmock.AnyArg(), sqlmock.AnyArg(), "12345", "0.4.4", 5, sqlmock.AnyArg(), now, now.Add(24*time.Hour)).WillReturnResult(sqlmock.NewResult(44, 1))
	mock.ExpectCommit()

	stored, err := NewRepository(database).Preview(context.Background(), command)
	if err != nil {
		t.Fatal(err)
	}
	if stored.ID != 44 || stored.Reused || !stored.ExpiresAt.Equal(now.Add(24*time.Hour)) {
		t.Fatalf("unexpected refreshed preview: %#v", stored)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestSQLRepositoryCreatesNewGenerationAfterTerminalHistory(t *testing.T) {
	database, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	now := time.Date(2026, 8, 16, 9, 30, 0, 0, time.UTC)
	command := previewCommandForTest(t, now)
	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta(previewBaseQuery)).WithArgs(int64(7)).WillReturnRows(sqlmock.NewRows([]string{"number", "revision"}).AddRow(8, 12))
	expectPreviewNow(mock, now)
	mock.ExpectQuery(regexp.QuoteMeta(previewExistingQuery)).WithArgs(int64(7), command.Hash[:]).WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery(regexp.QuoteMeta(previewQuotaQuery)).WithArgs(int64(7), now.Truncate(24*time.Hour), now.Truncate(24*time.Hour).Add(24*time.Hour)).WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(2))
	mock.ExpectExec(regexp.QuoteMeta(previewInsertQuery)).WithArgs(int64(7), command.Hash[:], "previewed", uint64(8), uint64(12), sqlmock.AnyArg(), sqlmock.AnyArg(), "12345", "0.4.4", 5, sqlmock.AnyArg(), now, now.Add(24*time.Hour)).WillReturnResult(sqlmock.NewResult(45, 1))
	mock.ExpectCommit()

	if _, err := NewRepository(database).Preview(context.Background(), command); err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestSQLRepositoryAppliesInactivePreviewInOneTransaction(t *testing.T) {
	database, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	now := time.Date(2026, 8, 17, 9, 30, 0, 0, time.UTC)
	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta(lifecycleJobQuery)).WithArgs(int64(19), int64(7)).WillReturnRows(sqlmock.NewRows([]string{"status", "expires_at", "keep_room_suggestion", "rollback_config_version_id", "rollback_runtime_json", "rollback_expires_at", "applied_config_version_id", "definition_json", "runtime_json", "room_suggestion"}).AddRow(jobPreviewed, now.Add(time.Hour), 0, nil, nil, nil, nil, []byte(`{"attributes":[]}`), []byte(`{"attributeValues":{}}`), "12345"))
	expectPreviewNow(mock, now)
	mock.ExpectQuery(regexp.QuoteMeta(lifecycleAccountQuery)).WithArgs(int64(7)).WillReturnRows(sqlmock.NewRows([]string{"config_version_id", "number", "revision", "runtime_json"}).AddRow(nil, 0, 0, nil))
	mock.ExpectQuery(regexp.QuoteMeta(lifecycleBaseQuery)).WithArgs(int64(19), int64(7)).WillReturnRows(sqlmock.NewRows([]string{"base_config_version_number", "base_state_revision"}).AddRow(0, 0))
	mock.ExpectQuery(regexp.QuoteMeta(lifecycleOpenSessionQuery)).WithArgs(int64(7)).WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery(regexp.QuoteMeta(lifecycleNextVersionQuery)).WithArgs(int64(7)).WillReturnRows(sqlmock.NewRows([]string{"number"}).AddRow(1))
	mock.ExpectExec(regexp.QuoteMeta(lifecycleInsertVersionQuery)).WithArgs(int64(7), uint64(1), sqlmock.AnyArg(), "migration", now).WillReturnResult(sqlmock.NewResult(88, 1))
	mock.ExpectExec(regexp.QuoteMeta(lifecycleUpsertRuntimeQuery)).WithArgs(int64(7), int64(88), uint64(1), sqlmock.AnyArg(), now).WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec(regexp.QuoteMeta(lifecycleUpsertActiveQuery)).WithArgs(int64(7), int64(88), now).WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO account_room_suggestions (account_id, room_id, suggested_at) VALUES (?, ?, ?) ON DUPLICATE KEY UPDATE room_id = VALUES(room_id), suggested_at = VALUES(suggested_at)")).WithArgs(int64(7), "12345", now).WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec(regexp.QuoteMeta("UPDATE migration_jobs SET keep_room_suggestion = ?, rollback_config_version_id = ?, rollback_runtime_json = ?, rollback_expires_at = ?, applied_config_version_id = ?, status = 'applied', applied_at = ? WHERE id = ? AND account_id = ? AND status IN ('previewed', 'pending')")).WithArgs(true, nil, nil, now.Add(7*24*time.Hour), int64(88), now, int64(19), int64(7)).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	job, err := NewRepository(database).(lifecycleRepository).Apply(context.Background(), applyCommand{AccountID: 7, JobID: 19, KeepRoomSuggestion: true})
	if err != nil {
		t.Fatal(err)
	}
	if job.Status != jobApplied || !job.RollbackExpiresAt.Equal(now.Add(7*24*time.Hour)) {
		t.Fatalf("job = %#v", job)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestSQLRepositoryStagesLiveSessionWithoutChangingConfiguration(t *testing.T) {
	database, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	now := time.Date(2026, 8, 17, 10, 0, 0, 0, time.UTC)
	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta(lifecycleJobQuery)).WithArgs(int64(19), int64(7)).WillReturnRows(sqlmock.NewRows([]string{"status", "expires_at", "keep_room_suggestion", "rollback_config_version_id", "rollback_runtime_json", "rollback_expires_at", "applied_config_version_id", "definition_json", "runtime_json", "room_suggestion"}).AddRow(jobPreviewed, now.Add(time.Hour), 0, nil, nil, nil, nil, []byte(`{"attributes":[]}`), []byte(`{"attributeValues":{}}`), "12345"))
	expectPreviewNow(mock, now)
	mock.ExpectQuery(regexp.QuoteMeta(lifecycleAccountQuery)).WithArgs(int64(7)).WillReturnRows(sqlmock.NewRows([]string{"config_version_id", "number", "revision", "runtime_json"}).AddRow(88, 3, 6, []byte(`{"attributeValues":{"health":1}}`)))
	mock.ExpectQuery(regexp.QuoteMeta(lifecycleBaseQuery)).WithArgs(int64(19), int64(7)).WillReturnRows(sqlmock.NewRows([]string{"base_config_version_number", "base_state_revision"}).AddRow(3, 6))
	mock.ExpectQuery(regexp.QuoteMeta(lifecycleOpenSessionQuery)).WithArgs(int64(7)).WillReturnRows(sqlmock.NewRows([]string{"id"}).AddRow(12))
	mock.ExpectExec(regexp.QuoteMeta("UPDATE migration_jobs SET status = 'pending', keep_room_suggestion = ? WHERE id = ? AND account_id = ? AND status = 'previewed'")).WithArgs(true, int64(19), int64(7)).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	job, err := NewRepository(database).(lifecycleRepository).Apply(context.Background(), applyCommand{AccountID: 7, JobID: 19, KeepRoomSuggestion: true})
	if err != nil {
		t.Fatal(err)
	}
	if job.Status != jobPending {
		t.Fatalf("job = %#v", job)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestSQLRepositoryAppliesPendingJobAfterSessionWithPersistedRoomDecision(t *testing.T) {
	database, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	now := time.Date(2026, 8, 17, 10, 15, 0, 0, time.UTC)
	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta(lifecycleJobQuery)).WithArgs(int64(19), int64(7)).WillReturnRows(sqlmock.NewRows([]string{"status", "expires_at", "keep_room_suggestion", "rollback_config_version_id", "rollback_runtime_json", "rollback_expires_at", "applied_config_version_id", "definition_json", "runtime_json", "room_suggestion"}).AddRow(jobPending, now.Add(time.Hour), 1, nil, nil, nil, nil, []byte(`{"attributes":[]}`), []byte(`{"attributeValues":{}}`), "12345"))
	expectPreviewNow(mock, now)
	mock.ExpectQuery(regexp.QuoteMeta(lifecycleAccountQuery)).WithArgs(int64(7)).WillReturnRows(sqlmock.NewRows([]string{"config_version_id", "number", "revision", "runtime_json"}).AddRow(88, 3, 6, []byte(`{"attributeValues":{"health":1}}`)))
	mock.ExpectQuery(regexp.QuoteMeta(lifecycleBaseQuery)).WithArgs(int64(19), int64(7)).WillReturnRows(sqlmock.NewRows([]string{"base_config_version_number", "base_state_revision"}).AddRow(3, 6))
	mock.ExpectQuery(regexp.QuoteMeta(lifecycleOpenSessionQuery)).WithArgs(int64(7)).WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery(regexp.QuoteMeta(lifecycleNextVersionQuery)).WithArgs(int64(7)).WillReturnRows(sqlmock.NewRows([]string{"number"}).AddRow(4))
	mock.ExpectExec(regexp.QuoteMeta(lifecycleInsertVersionQuery)).WithArgs(int64(7), uint64(4), sqlmock.AnyArg(), "migration", now).WillReturnResult(sqlmock.NewResult(99, 1))
	mock.ExpectExec(regexp.QuoteMeta(lifecycleUpsertRuntimeQuery)).WithArgs(int64(7), int64(99), uint64(7), sqlmock.AnyArg(), now).WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec(regexp.QuoteMeta(lifecycleUpsertActiveQuery)).WithArgs(int64(7), int64(99), now).WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO account_room_suggestions (account_id, room_id, suggested_at) VALUES (?, ?, ?) ON DUPLICATE KEY UPDATE room_id = VALUES(room_id), suggested_at = VALUES(suggested_at)")).WithArgs(int64(7), "12345", now).WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec(regexp.QuoteMeta("UPDATE migration_jobs SET keep_room_suggestion = ?, rollback_config_version_id = ?, rollback_runtime_json = ?, rollback_expires_at = ?, applied_config_version_id = ?, status = 'applied', applied_at = ? WHERE id = ? AND account_id = ? AND status IN ('previewed', 'pending')")).WithArgs(true, int64(88), sqlmock.AnyArg(), now.Add(7*24*time.Hour), int64(99), now, int64(19), int64(7)).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	job, err := NewRepository(database).(lifecycleRepository).ApplyPendingAfterSession(context.Background(), 7, 19)
	if err != nil {
		t.Fatal(err)
	}
	if job.Status != jobApplied {
		t.Fatalf("job = %#v", job)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestSQLRepositoryRollbackRestoresSavedStateOnlyWhenAppliedVersionIsCurrent(t *testing.T) {
	database, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	now := time.Date(2026, 8, 17, 10, 30, 0, 0, time.UTC)
	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta(lifecycleJobQuery)).WithArgs(int64(19), int64(7)).WillReturnRows(sqlmock.NewRows([]string{"status", "expires_at", "keep_room_suggestion", "rollback_config_version_id", "rollback_runtime_json", "rollback_expires_at", "applied_config_version_id", "definition_json", "runtime_json", "room_suggestion"}).AddRow(jobApplied, now.Add(time.Hour), 0, 77, []byte(`{"attributeValues":{"health":1}}`), now.Add(7*24*time.Hour), 88, []byte(`{"attributes":[]}`), []byte(`{"attributeValues":{"health":5}}`), nil))
	expectPreviewNow(mock, now)
	mock.ExpectQuery(regexp.QuoteMeta(lifecycleAccountQuery)).WithArgs(int64(7)).WillReturnRows(sqlmock.NewRows([]string{"config_version_id", "number", "revision", "runtime_json"}).AddRow(88, 4, 8, []byte(`{"attributeValues":{"health":5}}`)))
	mock.ExpectQuery(regexp.QuoteMeta(lifecycleOpenSessionQuery)).WithArgs(int64(7)).WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery(regexp.QuoteMeta("SELECT definition_json FROM account_config_versions WHERE account_id = ? AND id = ?")).WithArgs(int64(7), int64(77)).WillReturnRows(sqlmock.NewRows([]string{"definition_json"}).AddRow([]byte(`{"attributes":[]}`)))
	mock.ExpectQuery(regexp.QuoteMeta(lifecycleNextVersionQuery)).WithArgs(int64(7)).WillReturnRows(sqlmock.NewRows([]string{"number"}).AddRow(5))
	mock.ExpectExec(regexp.QuoteMeta(lifecycleInsertVersionQuery)).WithArgs(int64(7), uint64(5), sqlmock.AnyArg(), "rollback", now).WillReturnResult(sqlmock.NewResult(99, 1))
	mock.ExpectExec(regexp.QuoteMeta(lifecycleUpsertRuntimeQuery)).WithArgs(int64(7), int64(99), uint64(9), sqlmock.AnyArg(), now).WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec(regexp.QuoteMeta(lifecycleUpsertActiveQuery)).WithArgs(int64(7), int64(99), now).WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec(regexp.QuoteMeta("UPDATE migration_jobs SET status = 'rolled_back', rolled_back_at = ? WHERE id = ? AND account_id = ? AND status = 'applied'")).WithArgs(now, int64(19), int64(7)).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	job, err := NewRepository(database).(lifecycleRepository).Rollback(context.Background(), 7, 19)
	if err != nil {
		t.Fatal(err)
	}
	if job.Status != jobRolledBack {
		t.Fatalf("job = %#v", job)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

type recordingPreviewRepository struct {
	command previewCommand
	result  storedPreview
	err     error
}

type recordingLifecycleRepository struct {
	recordingPreviewRepository
	apply       applyCommand
	applyResult storedJob
	applyErr    error
}

func (repository *recordingLifecycleRepository) Apply(_ context.Context, command applyCommand) (storedJob, error) {
	repository.apply = command
	return repository.applyResult, repository.applyErr
}
func (repository *recordingLifecycleRepository) ApplyPendingAfterSession(context.Context, int64, int64) (storedJob, error) {
	return storedJob{}, ErrUnavailable
}
func (repository *recordingLifecycleRepository) Cancel(context.Context, int64, int64) (storedJob, error) {
	return storedJob{}, ErrUnavailable
}
func (repository *recordingLifecycleRepository) Rollback(context.Context, int64, int64) (storedJob, error) {
	return storedJob{}, ErrUnavailable
}
func (repository *recordingLifecycleRepository) Get(context.Context, int64, int64) (storedJob, error) {
	return storedJob{}, ErrUnavailable
}

func (repository *recordingPreviewRepository) Preview(_ context.Context, command previewCommand) (storedPreview, error) {
	repository.command = command
	return repository.result, repository.err
}

func decodedEnvelope(t *testing.T) Envelope {
	t.Helper()
	result, _, err := Decode(jsonReader(t, validEnvelopeWire()), 2<<20)
	if err != nil {
		t.Fatal(err)
	}
	return result
}
func previewCommandForTest(t *testing.T, now time.Time) previewCommand {
	t.Helper()
	envelope := decodedEnvelope(t)
	return previewCommand{AccountID: 7, Definition: envelope.Definition, Runtime: envelope.Runtime, RoomSuggestion: envelope.RoomSuggestion, Source: envelope.Source, Counts: envelope.Counts, Report: Report{}, Hash: envelope.Hash, CreatedAt: now, ExpiresAt: now.Add(24 * time.Hour)}
}

func expectPreviewNow(mock sqlmock.Sqlmock, now time.Time) {
	mock.ExpectQuery(regexp.QuoteMeta(previewDatabaseNowQuery)).WillReturnRows(sqlmock.NewRows([]string{"now"}).AddRow(now))
}
func activePreviewRows(id int64, status string, expiry time.Time, command previewCommand) *sqlmock.Rows {
	report, _ := json.Marshal(Report{Counts: command.Counts, Warnings: []string{"persisted"}, Ignored: []string{"/persisted"}})
	return sqlmock.NewRows([]string{"id", "status", "expires_at", "request_hash", "source_app_version", "source_schema_version", "room_suggestion", "report_json"}).AddRow(id, status, expiry, command.Hash[:], "persisted-app", 4, "persisted-room", report)
}
