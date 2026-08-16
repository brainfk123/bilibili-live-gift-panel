package mysqlstore

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"testing"
	"testing/fstest"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/go-sql-driver/mysql"
)

const (
	lockName    = "gift_panel_schema"
	lockSeconds = 30
)

func TestStoreDatabaseReturnsBorrowedPoolOwnedByStore(t *testing.T) {
	database, mock, err := sqlmock.New(sqlmock.MonitorPingsOption(true))
	if err != nil {
		t.Fatal(err)
	}
	store := &Store{db: database}
	if borrowed := store.Database(); borrowed != database {
		t.Fatalf("Database() = %p, want owned pool %p", borrowed, database)
	}
	mock.ExpectClose()
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
	if (*Store)(nil).Database() != nil {
		t.Fatal("nil Store returned a database")
	}
}

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

func TestProductionMigrationsIncludeRetryableAdministratorHandoffs(t *testing.T) {
	migrations, err := readMigrations(migrationFiles)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range migrations {
		if item.version != "0003_admin_handoffs" {
			continue
		}
		sql := string(item.contents)
		for _, required := range []string{"admin_credential_handoffs", "admin_handoff_recovery_codes", "pending_initialization_guard", "pending_recovery_admin_guard", "reserved_recovery_code_id", "token_hash", "expires_at"} {
			if !strings.Contains(sql, required) {
				t.Fatalf("0003 migration missing %q", required)
			}
		}
		return
	}
	t.Fatal("production migrations do not include 0003_admin_handoffs")
}

func TestProductionMigrationsIncludeVersionedConfigurationStorage(t *testing.T) {
	migrations, err := readMigrations(migrationFiles)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range migrations {
		if item.version != "0004_configuration_and_migration" {
			continue
		}
		sql := strings.Join(strings.Fields(string(item.contents)), " ")
		for _, required := range []string{
			"ALTER TABLE streamer_accounts ADD COLUMN active_config_version_id BIGINT UNSIGNED NULL",
			"CREATE TABLE IF NOT EXISTS account_config_versions",
			"CREATE TABLE IF NOT EXISTS account_runtime_state",
			"CREATE TABLE IF NOT EXISTS account_room_suggestions",
			"CREATE TABLE IF NOT EXISTS migration_jobs",
			"CREATE TABLE IF NOT EXISTS live_sessions",
			"UNIQUE KEY uq_account_config_versions_account_number (account_id, number)",
			"PRIMARY KEY (account_id)",
			"CHECK (JSON_VALID(definition_json))",
			"CHECK (JSON_VALID(runtime_json))",
			"KEY idx_migration_jobs_hash (account_id, request_hash)",
			"KEY idx_migration_jobs_status_expiry (status, expires_at)",
		} {
			if !strings.Contains(sql, required) {
				t.Fatalf("0004 migration missing %q", required)
			}
		}
		if strings.Contains(sql, "target_room") || strings.Contains(sql, "INSERT INTO live_sessions") {
			t.Fatalf("0004 migration violates pending room suggestion boundary: %s", sql)
		}
		return
	}
	t.Fatal("production migrations do not include 0004_configuration_and_migration")
}

func TestIdentityMigrationTerminalInvitationsCannotAlsoBeRevoked(t *testing.T) {
	migrations, err := readMigrations(migrationFiles)
	if err != nil {
		t.Fatalf("readMigrations() error = %v", err)
	}
	var identitySQL string
	for _, item := range migrations {
		if item.version == "0002_identity_and_invitations" {
			identitySQL = strings.Join(strings.Fields(string(item.contents)), " ")
			break
		}
	}
	if identitySQL == "" {
		t.Fatal("production migrations do not include 0002_identity_and_invitations")
	}
	for _, requiredBranch := range []string{
		"status = 'expired' AND revoked_at IS NULL AND used_at IS NULL AND invited_account_id IS NULL",
		"status = 'used' AND revoked_at IS NULL AND used_at IS NOT NULL AND invited_account_id IS NOT NULL",
	} {
		if !strings.Contains(identitySQL, requiredBranch) {
			t.Fatalf("terminal invitation CHECK missing invariant %q", requiredBranch)
		}
	}
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

func TestNormalizeMySQLDSNForcesParsedUTCTimeContract(t *testing.T) {
	input := "hosted-user:private-password@tcp(127.0.0.1:3306)/gift_panel?loc=Local&parseTime=false&time_zone=%27%2B08%3A00%27"
	normalized, err := normalizeMySQLDSN(input)
	if err != nil {
		t.Fatalf("normalizeMySQLDSN() error = %v", err)
	}
	config, err := mysql.ParseDSN(normalized)
	if err != nil {
		t.Fatalf("mysql.ParseDSN(normalized) error = %v", err)
	}
	if !config.ParseTime {
		t.Fatal("normalized DSN does not enable parseTime")
	}
	if config.Loc != time.UTC {
		t.Fatalf("normalized DSN location = %v, want UTC", config.Loc)
	}
	if got := config.Params["time_zone"]; got != "'+00:00'" {
		t.Fatalf("normalized DSN session time_zone = %q, want '+00:00'", got)
	}
	if config.User != "hosted-user" || config.Passwd != "private-password" || config.DBName != "gift_panel" {
		t.Fatal("normalization did not preserve connection identity")
	}

	store, mock, closeDB := newMockStore(t)
	defer closeDB()
	want := time.Date(2026, 8, 16, 10, 11, 12, 123456000, time.UTC)
	mock.ExpectQuery(regexp.QuoteMeta("SELECT created_at, disabled_at FROM scan_contract")).
		WillReturnRows(sqlmock.NewRows([]string{"created_at", "disabled_at"}).AddRow(want, want))
	var createdAt time.Time
	var disabledAt sql.NullTime
	if err := store.db.QueryRowContext(context.Background(), "SELECT created_at, disabled_at FROM scan_contract").Scan(&createdAt, &disabledAt); err != nil {
		t.Fatalf("Scan(time.Time, sql.NullTime) error = %v", err)
	}
	if !createdAt.Equal(want) || createdAt.Location() != time.UTC || !disabledAt.Valid || !disabledAt.Time.Equal(want) || disabledAt.Time.Location() != time.UTC {
		t.Fatalf("time Scan = (%v, %#v), want UTC values %v", createdAt, disabledAt, want)
	}
	assertExpectations(t, mock)
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
	originalContents := []byte(`CREATE TABLE IF NOT EXISTS schema_migrations (
    version VARCHAR(255) NOT NULL PRIMARY KEY,
    checksum CHAR(64) NOT NULL,
    applied_at TIMESTAMP(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6)
) ENGINE=InnoDB;

CREATE TABLE IF NOT EXISTS service_health_markers (
    marker_name VARCHAR(64) NOT NULL PRIMARY KEY,
    updated_at TIMESTAMP(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6) ON UPDATE CURRENT_TIMESTAMP(6)
) ENGINE=InnoDB;
`)
	files := fstest.MapFS{
		"migrations/0001_foundation.sql": {Data: originalContents},
	}
	migrations, err := readMigrations(files)
	if err != nil {
		t.Fatalf("readMigrations() error = %v", err)
	}
	if len(migrations) != 1 {
		t.Fatalf("fixture migration count = %d, want 1", len(migrations))
	}
	wantChecksum := fmt.Sprintf("%x", sha256.Sum256(originalContents))
	if migrations[0].checksum != wantChecksum {
		t.Fatalf("fixture checksum = %q, want independent SHA-256 %q", migrations[0].checksum, wantChecksum)
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
