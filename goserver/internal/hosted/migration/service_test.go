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

type recordingPreviewRepository struct {
	command previewCommand
	result  storedPreview
	err     error
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
