package configuration

import (
	"context"
	"database/sql"
	"errors"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
)

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
	mock.ExpectQuery(regexp.QuoteMeta("SELECT a.active_config_version_id, COALESCE(v.number, 0) FROM streamer_accounts AS a LEFT JOIN account_config_versions AS v ON v.id = a.active_config_version_id WHERE a.id = ? FOR UPDATE")).
		WithArgs(int64(7)).
		WillReturnRows(sqlmock.NewRows([]string{"active_config_version_id", "number"}).AddRow(nil, uint64(0)))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT revision FROM account_runtime_state WHERE account_id = ? FOR UPDATE")).
		WithArgs(int64(7)).
		WillReturnError(sql.ErrNoRows)
	mock.ExpectQuery(regexp.QuoteMeta("SELECT COALESCE(MAX(number), 0) + 1 FROM account_config_versions WHERE account_id = ?")).
		WithArgs(int64(7)).
		WillReturnRows(sqlmock.NewRows([]string{"number"}).AddRow(uint64(1)))
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO account_config_versions (account_id, number, definition_json, source, created_at) VALUES (?, ?, ?, ?, ?)")).
		WithArgs(int64(7), uint64(1), sqlmock.AnyArg(), "migration", now).
		WillReturnResult(sqlmock.NewResult(51, 1))
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO account_runtime_state (account_id, config_version_id, revision, runtime_json, updated_at) VALUES (?, ?, ?, ?, ?) ON DUPLICATE KEY UPDATE config_version_id = VALUES(config_version_id), revision = VALUES(revision), runtime_json = VALUES(runtime_json), updated_at = VALUES(updated_at)")).
		WithArgs(int64(7), int64(51), uint64(1), sqlmock.AnyArg(), now).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(regexp.QuoteMeta("UPDATE streamer_accounts SET active_config_version_id = ?, updated_at = ? WHERE id = ?")).
		WithArgs(int64(51), now, int64(7)).
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
	mock.ExpectQuery(regexp.QuoteMeta("SELECT a.active_config_version_id, COALESCE(v.number, 0) FROM streamer_accounts AS a LEFT JOIN account_config_versions AS v ON v.id = a.active_config_version_id WHERE a.id = ? FOR UPDATE")).
		WithArgs(int64(7)).
		WillReturnRows(sqlmock.NewRows([]string{"active_config_version_id", "number"}).AddRow(int64(51), uint64(2)))
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
	mock.ExpectQuery(regexp.QuoteMeta("SELECT a.active_config_version_id, COALESCE(v.number, 0) FROM streamer_accounts AS a LEFT JOIN account_config_versions AS v ON v.id = a.active_config_version_id WHERE a.id = ? FOR UPDATE")).
		WithArgs(int64(7)).
		WillReturnRows(sqlmock.NewRows([]string{"active_config_version_id", "number"}).AddRow(int64(50), uint64(2)))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT revision FROM account_runtime_state WHERE account_id = ? FOR UPDATE")).
		WithArgs(int64(7)).
		WillReturnRows(sqlmock.NewRows([]string{"revision"}).AddRow(uint64(4)))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT COALESCE(MAX(number), 0) + 1 FROM account_config_versions WHERE account_id = ?")).
		WithArgs(int64(7)).
		WillReturnRows(sqlmock.NewRows([]string{"number"}).AddRow(uint64(3)))
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO account_config_versions (account_id, number, definition_json, source, created_at) VALUES (?, ?, ?, ?, ?)")).
		WithArgs(int64(7), uint64(3), sqlmock.AnyArg(), "manual", now).
		WillReturnResult(sqlmock.NewResult(51, 1))
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO account_runtime_state (account_id, config_version_id, revision, runtime_json, updated_at) VALUES (?, ?, ?, ?, ?) ON DUPLICATE KEY UPDATE config_version_id = VALUES(config_version_id), revision = VALUES(revision), runtime_json = VALUES(runtime_json), updated_at = VALUES(updated_at)")).
		WithArgs(int64(7), int64(51), uint64(5), sqlmock.AnyArg(), now).
		WillReturnResult(sqlmock.NewResult(0, 2))
	mock.ExpectExec(regexp.QuoteMeta("UPDATE streamer_accounts SET active_config_version_id = ?, updated_at = ? WHERE id = ?")).
		WithArgs(int64(51), now, int64(7)).
		WillReturnResult(sqlmock.NewResult(0, 1))
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

	mock.ExpectQuery(regexp.QuoteMeta("SELECT v.id, v.account_id, v.number, v.definition_json, v.source, v.created_at, s.config_version_id, s.revision, s.runtime_json, s.updated_at FROM streamer_accounts AS a JOIN account_config_versions AS v ON v.id = a.active_config_version_id JOIN account_runtime_state AS s ON s.account_id = a.id WHERE a.id = ?")).
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

func newMockRepository(t *testing.T) (Repository, sqlmock.Sqlmock, func()) {
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
