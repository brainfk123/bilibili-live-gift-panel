package mysqlstore

import (
	"context"
	"database/sql"
	"errors"
	"regexp"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/DATA-DOG/go-sqlmock"
)

const (
	lockName    = "gift_panel_schema"
	lockSeconds = 30
)

func TestReadMigrationsSortsFilesByName(t *testing.T) {
	files := fstest.MapFS{
		"migrations/0002_second.sql": {Data: []byte("SELECT 2")},
		"migrations/0001_first.sql":  {Data: []byte("SELECT 1")},
		"migrations/notes.txt":       {Data: []byte("ignored")},
	}

	migrations, err := readMigrations(files)
	if err != nil {
		t.Fatalf("readMigrations() error = %v", err)
	}
	if len(migrations) != 2 || migrations[0].version != "0001_first" || migrations[1].version != "0002_second" {
		t.Fatalf("migration order = %#v, want 0001_first then 0002_second", migrations)
	}
}

func TestProductionMigrationsIncludeIdentitySchema(t *testing.T) {
	migrations, err := readMigrations(migrationFiles)
	if err != nil {
		t.Fatalf("readMigrations() error = %v", err)
	}
	for _, item := range migrations {
		if item.version == "0002_identity_and_invitations" {
			return
		}
	}
	t.Fatal("production migrations do not include 0002_identity_and_invitations")
}

func TestMigrateAppliesUnseenMigrationAndRecordsChecksum(t *testing.T) {
	store, mock, closeDB := newMockStore(t)
	defer closeDB()
	files, migration := foundationFixture(t)

	expectLock(mock)
	expectSchemaTable(mock)
	mock.ExpectQuery(regexp.QuoteMeta("SELECT checksum FROM schema_migrations WHERE version = ?")).
		WithArgs(migration.version).
		WillReturnError(sql.ErrNoRows)
	mock.ExpectExec("(?s)CREATE TABLE IF NOT EXISTS schema_migrations").
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec("(?s)CREATE TABLE IF NOT EXISTS service_health_markers").
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO schema_migrations (version, checksum) VALUES (?, ?)")).
		WithArgs(migration.version, migration.checksum).
		WillReturnResult(sqlmock.NewResult(1, 1))
	expectUnlock(mock)

	if err := store.migrate(context.Background(), files); err != nil {
		t.Fatalf("migrate() error = %v", err)
	}
	assertExpectations(t, mock)
}

func TestMigrateSkipsAlreadyAppliedMigrationWithMatchingChecksum(t *testing.T) {
	store, mock, closeDB := newMockStore(t)
	defer closeDB()
	files, migration := foundationFixture(t)

	expectLock(mock)
	expectSchemaTable(mock)
	mock.ExpectQuery(regexp.QuoteMeta("SELECT checksum FROM schema_migrations WHERE version = ?")).
		WithArgs(migration.version).
		WillReturnRows(sqlmock.NewRows([]string{"checksum"}).AddRow(migration.checksum))
	expectUnlock(mock)

	if err := store.migrate(context.Background(), files); err != nil {
		t.Fatalf("migrate() error = %v", err)
	}
	assertExpectations(t, mock)
}

func TestMigrateRejectsChangedChecksumAndReleasesLock(t *testing.T) {
	store, mock, closeDB := newMockStore(t)
	defer closeDB()
	files, migration := foundationFixture(t)

	expectLock(mock)
	expectSchemaTable(mock)
	mock.ExpectQuery(regexp.QuoteMeta("SELECT checksum FROM schema_migrations WHERE version = ?")).
		WithArgs(migration.version).
		WillReturnRows(sqlmock.NewRows([]string{"checksum"}).AddRow(strings.Repeat("0", 64)))
	expectUnlock(mock)

	err := store.migrate(context.Background(), files)
	if err == nil || !strings.Contains(err.Error(), "checksum") {
		t.Fatalf("migrate() error = %v, want checksum mismatch", err)
	}
	assertExpectations(t, mock)
}

func TestMigrateReleasesLockWhenMigrationApplicationFails(t *testing.T) {
	store, mock, closeDB := newMockStore(t)
	defer closeDB()
	files, migration := foundationFixture(t)

	expectLock(mock)
	expectSchemaTable(mock)
	mock.ExpectQuery(regexp.QuoteMeta("SELECT checksum FROM schema_migrations WHERE version = ?")).
		WithArgs(migration.version).
		WillReturnError(sql.ErrNoRows)
	mock.ExpectExec("(?s)CREATE TABLE IF NOT EXISTS schema_migrations").
		WillReturnError(errors.New("apply failed"))
	expectUnlock(mock)

	err := store.migrate(context.Background(), files)
	if err == nil || !strings.Contains(err.Error(), "apply migration") {
		t.Fatalf("migrate() error = %v, want application failure", err)
	}
	assertExpectations(t, mock)
}

func TestOpenDoesNotExposeDSNOnFailure(t *testing.T) {
	dsn := "super-secret-dsn-without-required-shape"
	_, err := Open(context.Background(), dsn)
	if err == nil {
		t.Fatal("Open() error = nil, want invalid DSN error")
	}
	if strings.Contains(err.Error(), dsn) {
		t.Fatalf("Open() error exposed DSN: %v", err)
	}
}

func newMockStore(t *testing.T) (*Store, sqlmock.Sqlmock, func()) {
	t.Helper()
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("sqlmock.New() error = %v", err)
	}
	return &Store{db: db}, mock, func() { _ = db.Close() }
}

func foundationFixture(t *testing.T) (fstest.MapFS, migration) {
	t.Helper()
	files := fstest.MapFS{
		"migrations/0001_foundation.sql": {Data: []byte(`CREATE TABLE IF NOT EXISTS schema_migrations (
    version VARCHAR(255) NOT NULL PRIMARY KEY,
    checksum CHAR(64) NOT NULL,
    applied_at TIMESTAMP(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6)
) ENGINE=InnoDB;

CREATE TABLE IF NOT EXISTS service_health_markers (
    marker_name VARCHAR(64) NOT NULL PRIMARY KEY,
    updated_at TIMESTAMP(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6) ON UPDATE CURRENT_TIMESTAMP(6)
) ENGINE=InnoDB;
`)},
	}
	migrations, err := readMigrations(files)
	if err != nil {
		t.Fatalf("readMigrations() error = %v", err)
	}
	if len(migrations) != 1 {
		t.Fatalf("fixture migration count = %d, want 1", len(migrations))
	}
	return files, migrations[0]
}

func expectLock(mock sqlmock.Sqlmock) {
	mock.ExpectQuery(regexp.QuoteMeta("SELECT GET_LOCK(?, ?)")).
		WithArgs(lockName, lockSeconds).
		WillReturnRows(sqlmock.NewRows([]string{"GET_LOCK"}).AddRow(1))
}

func expectUnlock(mock sqlmock.Sqlmock) {
	mock.ExpectQuery(regexp.QuoteMeta("SELECT RELEASE_LOCK(?)")).
		WithArgs(lockName).
		WillReturnRows(sqlmock.NewRows([]string{"RELEASE_LOCK"}).AddRow(1))
}

func expectSchemaTable(mock sqlmock.Sqlmock) {
	mock.ExpectExec("(?s)CREATE TABLE IF NOT EXISTS schema_migrations").
		WillReturnResult(sqlmock.NewResult(0, 0))
}

func assertExpectations(t *testing.T, mock sqlmock.Sqlmock) {
	t.Helper()
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("SQL expectations: %v", err)
	}
}
