package migration

import (
	"context"
	"database/sql"
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
	mock.ExpectQuery(regexp.QuoteMeta(previewExistingQuery)).WithArgs(int64(7), command.Hash[:]).WillReturnRows(sqlmock.NewRows([]string{"id", "status", "expires_at"}).AddRow(3, "previewed", now.Add(time.Hour)))
	mock.ExpectCommit()

	stored, err := NewRepository(database).Preview(context.Background(), command)
	if err != nil {
		t.Fatal(err)
	}
	if stored.ID != 3 || !stored.Reused {
		t.Fatalf("expected reusable preview, got %#v", stored)
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
	mock.ExpectQuery(regexp.QuoteMeta(previewExistingQuery)).WithArgs(int64(7), command.Hash[:]).WillReturnRows(sqlmock.NewRows([]string{"id", "status", "expires_at"}).AddRow(3, "previewed", now.Add(-time.Second)))
	mock.ExpectQuery(regexp.QuoteMeta(previewQuotaQuery)).WithArgs(int64(7), now.Truncate(24*time.Hour), now.Truncate(24*time.Hour).Add(24*time.Hour)).WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(2))
	mock.ExpectExec(regexp.QuoteMeta(previewRefreshQuery)).WithArgs("previewed", uint64(8), uint64(12), sqlmock.AnyArg(), sqlmock.AnyArg(), "12345", "0.4.4", 5, sqlmock.AnyArg(), now, now.Add(24*time.Hour), int64(3), int64(7)).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	stored, err := NewRepository(database).Preview(context.Background(), command)
	if err != nil {
		t.Fatal(err)
	}
	if stored.ID != 3 || stored.Reused || !stored.ExpiresAt.Equal(now.Add(24*time.Hour)) {
		t.Fatalf("unexpected refreshed preview: %#v", stored)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestSQLRepositoryNeverRefreshesAppliedOrRolledBackJob(t *testing.T) {
	database, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	now := time.Date(2026, 8, 16, 9, 30, 0, 0, time.UTC)
	command := previewCommandForTest(t, now)
	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta(previewBaseQuery)).WithArgs(int64(7)).WillReturnRows(sqlmock.NewRows([]string{"number", "revision"}).AddRow(8, 12))
	mock.ExpectQuery(regexp.QuoteMeta(previewExistingQuery)).WithArgs(int64(7), command.Hash[:]).WillReturnRows(sqlmock.NewRows([]string{"id", "status", "expires_at"}).AddRow(3, "applied", now.Add(-time.Hour)))
	mock.ExpectRollback()

	if _, err := NewRepository(database).Preview(context.Background(), command); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("got %v, want unavailable", err)
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
