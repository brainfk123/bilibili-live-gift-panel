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

func TestRoomMonitorMigrationSeparatesBroadcastFromRuntimeSession(t *testing.T) {
	migrations, err := readMigrations(migrationFiles)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range migrations {
		if item.version != "0013_room_monitoring_and_broadcast_sessions" {
			continue
		}
		sql := strings.Join(strings.Fields(string(item.contents)), " ")
		for _, fragment := range []string{
			"CREATE TABLE IF NOT EXISTS room_monitor_states",
			"CREATE TABLE IF NOT EXISTS room_monitor_references",
			"CREATE TABLE IF NOT EXISTS broadcast_sessions",
			"CREATE TABLE IF NOT EXISTS room_monitor_transitions",
			"CREATE TABLE IF NOT EXISTS room_monitor_outbox_tail",
			"ALTER TABLE live_sessions ADD COLUMN broadcast_session_id",
			"UNIQUE KEY uq_broadcast_sessions_open_room (open_room_id)",
			"(state = 'offline' AND broadcast_session_id IS NULL AND grace_until IS NULL)",
			"(state = 'live' AND broadcast_session_id IS NOT NULL AND grace_until IS NULL)",
			"(state = 'grace' AND broadcast_session_id IS NOT NULL AND grace_until IS NOT NULL)",
		} {
			if !strings.Contains(sql, fragment) {
				t.Fatalf("0013 migration missing %q", fragment)
			}
		}
		return
	}
	t.Fatal("production migrations do not include 0013_room_monitoring_and_broadcast_sessions")
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

func TestSplitStatementsIgnoresSemicolonsInCommentsAndQuotedValues(t *testing.T) {
	contents := []byte("-- state rows exist; reference rows follow\nSET @value := 'left;right';\n/* keep; together */ SELECT 2;")
	statements := splitStatements(contents)
	if len(statements) != 2 {
		t.Fatalf("splitStatements() returned %d statements, want 2: %#v", len(statements), statements)
	}
	if !strings.Contains(statements[0], "SET @value := 'left;right'") {
		t.Fatalf("first statement split inside comment or quoted value: %q", statements[0])
	}
	if !strings.Contains(statements[1], "SELECT 2") {
		t.Fatalf("second statement = %q, want SELECT 2", statements[1])
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

func TestProductionAdminEmailIdentityMigrationMakesLegacyUIDNullable(t *testing.T) {
	migrations, err := readMigrations(migrationFiles)
	if err != nil {
		t.Fatal(err)
	}
	wantChecksums := map[string]string{
		"0001_foundation":                             "d49617d9a14c87bd9d1526b99ec2399422da047baff7b89b6832c9adfc8c0031",
		"0002_identity_and_invitations":               "fddd12bb00938fad7b94c7cc903e13900f12d583f3e426c603fdd7b4cb2a8a6a",
		"0003_admin_handoffs":                         "3e44ef4db3db1dc48d9161df002a9e19578241218b804b3b5db08c157cb78b89",
		"0004_configuration_and_migration":            "8fb8fc88b8040b9806ea15612fdb62dd1fc12f764efcf23d623937779dc64f7c",
		"0005_runtime_and_obs":                        "aaeb739b1fd17b3751d733d36fd8e06c1320f5393ff77341bbac6fbd2a7bc9c2",
		"0006_runtime_invariants":                     "aef0529f46dc18d47d6770b71fe5f6d16ddb8d23c622f660a1718b4a2d4038b3",
		"0007_runtime_ownership":                      "a56cd452a649bd928b5a48a3e8ffacca15fd889eab5161fb7bc96d782be9caa4",
		"0008_runtime_dedupe_cleanup_index":           "7d69109e076d085e0988e0c196df8480d1dcd8a03e60495f6302e382ea94e064",
		"0012_admin_session_inventory":                "e707f3edc1a7d49ffd4a636ad0e14bc579599d194aef37e5c9fe7ee19afa9a03",
		"0013_room_monitoring_and_broadcast_sessions": "08160c85b38570ae490959a7356b77a97958bfa1a17a84407b388db8496606f8",
	}
	if len(migrations) != 15 {
		t.Fatalf("migration count = %d, want 15", len(migrations))
	}
	var emailIdentity migration
	for _, item := range migrations {
		if checksum, ok := wantChecksums[item.version]; ok {
			if item.checksum != checksum {
				t.Fatalf("published migration %s checksum=%s want=%s", item.version, item.checksum, checksum)
			}
			delete(wantChecksums, item.version)
		}
		if item.version == "0009_admin_email_identity" {
			emailIdentity = item
		}
	}
	if len(wantChecksums) != 0 {
		t.Fatalf("published migrations missing: %v", wantChecksums)
	}
	if emailIdentity.version == "" {
		t.Fatal("production migrations do not include 0009_admin_email_identity")
	}
	statements := splitStatements(emailIdentity.contents)
	if len(statements) != 1 {
		t.Fatalf("0009 statements = %d, want one atomic ALTER TABLE", len(statements))
	}
	got := strings.Join(strings.Fields(statements[0]), " ")
	want := "ALTER TABLE admin_identity MODIFY COLUMN uid_ciphertext VARBINARY(512) NULL, MODIFY COLUMN uid_lookup BINARY(32) NULL"
	if got != want {
		t.Fatalf("0009 statement = %q, want %q", got, want)
	}
}

func TestRoomEventOutboxUsesForwardMigrationWithoutChangingPublished0013(t *testing.T) {
	migrations, err := readMigrations(migrationFiles)
	if err != nil {
		t.Fatal(err)
	}
	var base, forward migration
	for _, item := range migrations {
		switch item.version {
		case "0013_room_monitoring_and_broadcast_sessions":
			base = item
		case "0014_room_event_outbox":
			forward = item
		}
	}
	if base.version == "" || forward.version == "" {
		t.Fatalf("room event migrations = base:%q forward:%q", base.version, forward.version)
	}
	if strings.Contains(string(base.contents), "event_kind") || strings.Contains(string(base.contents), "account_ids_json") {
		t.Fatal("published 0013 was rewritten with room event columns")
	}
	if base.checksum != "08160c85b38570ae490959a7356b77a97958bfa1a17a84407b388db8496606f8" {
		t.Fatalf("published 0013 checksum = %s", base.checksum)
	}
	normalized := strings.Join(strings.Fields(string(forward.contents)), " ")
	for _, required := range []string{
		"ALTER TABLE room_monitor_transitions ADD COLUMN event_kind",
		"ADD COLUMN account_ids_json JSON NULL",
		"UPDATE room_monitor_transitions SET event_kind = 'room_state_changed'",
		"MODIFY COLUMN event_kind VARCHAR(32)",
		"MODIFY COLUMN lease_epoch BIGINT UNSIGNED NULL",
		"ADD CONSTRAINT chk_room_monitor_transitions_payload CHECK",
	} {
		if !strings.Contains(normalized, required) {
			t.Fatalf("0014 migration missing %q", required)
		}
	}
}

func TestRoomMutationReceiptsUseForward0015WithoutChangingPublishedRoomMigrations(t *testing.T) {
	migrations, err := readMigrations(migrationFiles)
	if err != nil {
		t.Fatal(err)
	}
	wantChecksums := map[string]string{
		"0013_room_monitoring_and_broadcast_sessions": "08160c85b38570ae490959a7356b77a97958bfa1a17a84407b388db8496606f8",
		"0014_room_event_outbox":                      "1528536798d20952766b705fb1bedf030a5361248633f09b05e7b901d96abc70",
	}
	var receipts migration
	for _, item := range migrations {
		if want, ok := wantChecksums[item.version]; ok {
			if item.checksum != want {
				t.Fatalf("published %s checksum = %s, want %s", item.version, item.checksum, want)
			}
			delete(wantChecksums, item.version)
		}
		if item.version == "0015_room_mutation_receipts" {
			receipts = item
		}
	}
	if len(wantChecksums) != 0 || receipts.version == "" {
		t.Fatalf("migration coverage missing published=%v receipts=%q", wantChecksums, receipts.version)
	}
	normalized := strings.Join(strings.Fields(string(receipts.contents)), " ")
	for _, required := range []string{
		"CREATE TABLE IF NOT EXISTS room_mutation_receipts",
		"mutation_id BINARY(16) NOT NULL",
		"desired_room_id VARCHAR(20)",
		"old_room_id VARCHAR(20)",
		"new_room_id VARCHAR(20)",
		"phase VARCHAR(32)",
		"audit_event_id BIGINT UNSIGNED NULL",
		"UNIQUE KEY uq_room_mutation_receipts_active_account",
	} {
		if !strings.Contains(normalized, required) {
			t.Fatalf("0015 missing %q", required)
		}
	}
}

func TestProductionSessionInventoryMigrationIsForwardOnlyAndPrivacyBounded(t *testing.T) {
	migrations, err := readMigrations(migrationFiles)
	if err != nil {
		t.Fatal(err)
	}
	var sqlText string
	for _, item := range migrations {
		if item.version == "0012_admin_session_inventory" {
			sqlText = strings.Join(strings.Fields(string(item.contents)), " ")
			break
		}
	}
	if sqlText == "" {
		t.Fatal("0012_admin_session_inventory migration is missing")
	}
	for _, required := range []string{
		"public_id BINARY(16)",
		"device_label VARCHAR(80)",
		"client_network VARCHAR(64)",
		"last_seen_at DATETIME(6)",
		"UUID_TO_BIN(UUID())",
		"其他设备 · 其他浏览器",
		"CREATE TABLE IF NOT EXISTS admin_login_events",
		"CHECK (result IN ('success', 'failure'))",
		"KEY idx_admin_login_events_occurred (occurred_at DESC, id DESC)",
	} {
		if !strings.Contains(sqlText, required) {
			t.Fatalf("0012 migration missing %q", required)
		}
	}
}

func TestRecoverableAdministratorInvitationMigrationIsNullableAndIdempotent(t *testing.T) {
	migrations, err := readMigrations(migrationFiles)
	if err != nil {
		t.Fatal(err)
	}
	var sqlText string
	for _, migration := range migrations {
		if migration.version == "0011_recoverable_admin_invitations" {
			sqlText = strings.Join(strings.Fields(string(migration.contents)), " ")
		}
	}
	if sqlText == "" {
		t.Fatal("0011 migration is missing")
	}
	for _, required := range []string{"information_schema.columns", "ALTER TABLE invitations ADD COLUMN code_ciphertext VARBINARY(512) NULL", "WHEN 'exact' THEN 'DO 0'", "invitation_ciphertext_column_definition_mismatch", "MODIFY COLUMN expires_at TIMESTAMP(6) NULL"} {
		if !strings.Contains(sqlText, required) {
			t.Fatalf("0011 missing %q", required)
		}
	}
}

func TestProductionMigrationsIncludeSingleUseAdministratorOperationAuthorizations(t *testing.T) {
	migrations, err := readMigrations(migrationFiles)
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range migrations {
		if item.version != "0010_admin_operation_authorizations" {
			continue
		}
		normalized := strings.Join(strings.Fields(string(item.contents)), " ")
		for _, required := range []string{
			"CREATE TABLE IF NOT EXISTS admin_operation_authorizations",
			"token_hash BINARY(32) NOT NULL",
			"session_id BIGINT UNSIGNED NOT NULL",
			"credential_epoch BIGINT UNSIGNED NOT NULL",
			"purpose VARCHAR(64) NOT NULL",
			"target VARCHAR(256) NOT NULL",
			"totp_step TIMESTAMP(6) NOT NULL",
			"consumed_at TIMESTAMP(6) NULL",
			"UNIQUE KEY uq_admin_operation_authorizations_token_hash (token_hash)",
			"UNIQUE KEY uq_admin_operation_authorizations_totp_step (credential_epoch, totp_step)",
			"FOREIGN KEY (session_id) REFERENCES site_sessions (id)",
		} {
			if !strings.Contains(normalized, required) {
				t.Fatalf("0010 migration missing %q", required)
			}
		}
		return
	}
	t.Fatal("production migrations do not include 0010_admin_operation_authorizations")
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
			"CREATE TABLE IF NOT EXISTS account_config_versions",
			"CREATE TABLE IF NOT EXISTS account_active_config",
			"CREATE TABLE IF NOT EXISTS account_runtime_state",
			"CREATE TABLE IF NOT EXISTS account_room_suggestions",
			"CREATE TABLE IF NOT EXISTS migration_jobs",
			"CREATE TABLE IF NOT EXISTS live_sessions",
			"UNIQUE KEY uq_account_config_versions_account_number (account_id, number)",
			"UNIQUE KEY uq_account_config_versions_account_id (account_id, id)",
			"PRIMARY KEY (account_id)",
			"base_config_version_number BIGINT UNSIGNED NOT NULL DEFAULT 0",
			"base_state_revision BIGINT UNSIGNED NOT NULL DEFAULT 0",
			"keep_room_suggestion TINYINT NOT NULL DEFAULT 0",
			"applied_config_version_id BIGINT UNSIGNED NULL",
			"CHECK (keep_room_suggestion IN (0, 1))",
			"CHECK (JSON_VALID(definition_json))",
			"CHECK (JSON_VALID(runtime_json))",
			"KEY idx_migration_jobs_hash (account_id, request_hash)",
			"active_request_hash BINARY(32) GENERATED ALWAYS AS (CASE WHEN status IN ('previewed', 'pending') THEN request_hash ELSE NULL END) STORED",
			"UNIQUE KEY uq_migration_jobs_account_active_hash (account_id, active_request_hash)",
			"KEY idx_migration_jobs_status_expiry (status, expires_at)",
			"FOREIGN KEY (account_id, config_version_id) REFERENCES account_config_versions (account_id, id)",
			"FOREIGN KEY (account_id, rollback_config_version_id) REFERENCES account_config_versions (account_id, id)",
			"FOREIGN KEY (account_id, applied_config_version_id) REFERENCES account_config_versions (account_id, id)",
		} {
			if !strings.Contains(sql, required) {
				t.Fatalf("0004 migration missing %q", required)
			}
		}
		if strings.Contains(sql, "UNIQUE KEY uq_migration_jobs_account_hash (account_id, request_hash)") {
			t.Fatal("0004 migration permanently deduplicates terminal migration generations")
		}
		if strings.Contains(sql, "ALTER TABLE") || strings.Contains(sql, "target_room") || strings.Contains(sql, "INSERT INTO live_sessions") {
			t.Fatalf("0004 migration violates pending room suggestion boundary: %s", sql)
		}
		for _, statement := range splitStatements(item.contents) {
			if !strings.HasPrefix(strings.TrimSpace(statement), "CREATE TABLE IF NOT EXISTS") {
				t.Fatalf("0004 migration has a partial-retry unsafe statement: %s", statement)
			}
		}
		return
	}
	t.Fatal("production migrations do not include 0004_configuration_and_migration")
}

func TestPublishedRuntimeMigrationChecksumsRemainImmutable(t *testing.T) {
	migrations, err := readMigrations(migrationFiles)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"0004_configuration_and_migration":  "8fb8fc88b8040b9806ea15612fdb62dd1fc12f764efcf23d623937779dc64f7c",
		"0005_runtime_and_obs":              "aaeb739b1fd17b3751d733d36fd8e06c1320f5393ff77341bbac6fbd2a7bc9c2",
		"0007_runtime_ownership":            "a56cd452a649bd928b5a48a3e8ffacca15fd889eab5161fb7bc96d782be9caa4",
		"0008_runtime_dedupe_cleanup_index": "7d69109e076d085e0988e0c196df8480d1dcd8a03e60495f6302e382ea94e064",
	}
	for _, item := range migrations {
		checksum, exists := want[item.version]
		if !exists {
			continue
		}
		if item.checksum != checksum {
			t.Fatalf("published migration %s checksum=%s want=%s", item.version, item.checksum, checksum)
		}
		delete(want, item.version)
	}
	if len(want) != 0 {
		t.Fatalf("published migrations missing: %v", want)
	}
}

func TestProductionRuntimeInvariantMigrationUsesOneCanonicalCreateOnlySchema(t *testing.T) {
	migrations, err := readMigrations(migrationFiles)
	if err != nil {
		t.Fatal(err)
	}
	var runtimeMigration migration
	for _, item := range migrations {
		if item.version == "0006_runtime_invariants" {
			runtimeMigration = item
			break
		}
	}
	if runtimeMigration.version == "" {
		t.Fatal("production migrations do not include 0006_runtime_invariants")
	}
	runtimeSQL := strings.Join(strings.Fields(string(runtimeMigration.contents)), " ")
	for _, required := range []string{
		"CREATE TABLE IF NOT EXISTS runtime_session_identities",
		"PRIMARY KEY (live_session_id)",
		"UNIQUE KEY uq_runtime_session_identities_session_account (live_session_id, account_id)",
		"FOREIGN KEY (live_session_id) REFERENCES live_sessions (id)",
		"CREATE TABLE IF NOT EXISTS runtime_active_session_guards",
		"PRIMARY KEY (account_id)",
		"UNIQUE KEY uq_runtime_active_session_guards_session (live_session_id)",
		"FOREIGN KEY (live_session_id, account_id) REFERENCES runtime_session_identities (live_session_id, account_id)",
		"CREATE TABLE IF NOT EXISTS runtime_session_aggregates",
		"KEY idx_runtime_session_aggregates_session_account (live_session_id, account_id)",
		"CONSTRAINT fk_runtime_session_aggregates_identity FOREIGN KEY (live_session_id, account_id) REFERENCES runtime_session_identities (live_session_id, account_id)",
		"CREATE TABLE IF NOT EXISTS runtime_event_dedup_receipts",
		"CREATE TABLE IF NOT EXISTS runtime_event_dedup_receipts ( account_id BIGINT UNSIGNED NOT NULL, live_session_id BIGINT UNSIGNED NOT NULL",
		"KEY idx_runtime_event_dedup_receipts_session_account (live_session_id, account_id)",
		"CONSTRAINT fk_runtime_event_dedup_receipts_identity FOREIGN KEY (live_session_id, account_id) REFERENCES runtime_session_identities (live_session_id, account_id)",
		"INSERT INTO runtime_session_identities (live_session_id, account_id) SELECT source.live_session_id, source.source_account_id FROM ( SELECT id AS live_session_id, account_id AS source_account_id FROM live_sessions ) AS source ON DUPLICATE KEY UPDATE account_id = IF(runtime_session_identities.account_id = source.source_account_id, runtime_session_identities.account_id, NULL)",
		"INSERT INTO runtime_active_session_guards (account_id, live_session_id) SELECT source.source_account_id, source.live_session_id FROM ( SELECT account_id AS source_account_id, id AS live_session_id FROM live_sessions WHERE ended_at IS NULL ) AS source ON DUPLICATE KEY UPDATE live_session_id = IF(runtime_active_session_guards.account_id = source.source_account_id AND runtime_active_session_guards.live_session_id = source.live_session_id, runtime_active_session_guards.live_session_id, NULL)",
	} {
		if !strings.Contains(runtimeSQL, required) {
			t.Fatalf("0006 runtime schema missing %q", required)
		}
	}
	for _, forbidden := range []string{"ALTER TABLE", "session_status", "runtime_sessions", "ended_at TIMESTAMP", "CREATE TABLE IF NOT EXISTS live_session_runtime"} {
		if strings.Contains(runtimeSQL, forbidden) {
			t.Fatalf("0006 contains unsafe or ambiguous legacy seam %q", forbidden)
		}
	}
	if count := strings.Count(runtimeSQL, "ended_at"); count != 1 {
		t.Fatalf("0006 ended_at references=%d want only the live_sessions backfill predicate", count)
	}
	statements := splitStatements(runtimeMigration.contents)
	if len(statements) != 6 {
		t.Fatalf("0006 statements=%d want four idempotent CREATEs and two idempotent backfills", len(statements))
	}
	for index, statement := range statements {
		trimmed := strings.TrimSpace(statement)
		if strings.HasPrefix(trimmed, "--") {
			if index := strings.Index(trimmed, "CREATE TABLE"); index >= 0 {
				trimmed = trimmed[index:]
			}
		}
		wantPrefix := "CREATE TABLE IF NOT EXISTS"
		if index >= 4 {
			wantPrefix = "INSERT INTO runtime_"
		}
		if trimmed != "" && !strings.HasPrefix(trimmed, wantPrefix) {
			t.Fatalf("0006 statement %d has a partial-retry unsafe shape: %s", index, trimmed)
		}
	}
}

func TestProductionRuntimeDedupeCleanupIndexMigrationIsCrashRetrySafe(t *testing.T) {
	migrations, err := readMigrations(migrationFiles)
	if err != nil {
		t.Fatal(err)
	}
	var cleanupIndex migration
	for _, item := range migrations {
		if item.version == "0008_runtime_dedupe_cleanup_index" {
			cleanupIndex = item
			break
		}
	}
	if cleanupIndex.version == "" {
		t.Fatal("production migrations do not include 0008_runtime_dedupe_cleanup_index")
	}
	normalized := strings.Join(strings.Fields(string(cleanupIndex.contents)), " ")
	for _, required := range []string{
		"FROM information_schema.statistics",
		"table_schema = DATABASE()",
		"table_name = 'runtime_event_dedup_receipts'",
		"index_name = 'idx_runtime_event_dedup_receipts_account_expiry'",
		"COUNT(*) = 3",
		"SEQ_IN_INDEX = 1 AND COLUMN_NAME = 'account_id'",
		"SEQ_IN_INDEX = 2 AND COLUMN_NAME = 'expires_at'",
		"SEQ_IN_INDEX = 3 AND COLUMN_NAME = 'event_hash'",
		"SUM(CASE WHEN COLLATION = 'A' THEN 1 ELSE 0 END) = 3",
		"CREATE INDEX idx_runtime_event_dedup_receipts_account_expiry ON runtime_event_dedup_receipts (account_id, expires_at, event_hash)",
		"SELECT * FROM information_schema.runtime_dedupe_cleanup_index_definition_mismatch",
	} {
		if !strings.Contains(normalized, required) {
			t.Fatalf("0008 cleanup index migration missing %q", required)
		}
	}
	if strings.Contains(normalized, "CREATE INDEX IF NOT EXISTS") {
		t.Fatal("0008 uses unsupported MySQL CREATE INDEX IF NOT EXISTS syntax")
	}
	statements := splitStatements(cleanupIndex.contents)
	if len(statements) != 5 {
		t.Fatalf("0008 statements=%d want state check, SQL selection, prepare, execute, deallocate", len(statements))
	}
	prefixes := []string{"SET @runtime_dedupe_cleanup_index_state", "SET @runtime_dedupe_cleanup_index_sql", "PREPARE runtime_dedupe_cleanup_index_stmt", "EXECUTE runtime_dedupe_cleanup_index_stmt", "DEALLOCATE PREPARE runtime_dedupe_cleanup_index_stmt"}
	for index, prefix := range prefixes {
		if !strings.HasPrefix(strings.TrimSpace(statements[index]), prefix) {
			t.Fatalf("0008 statement %d = %q, want prefix %q", index, statements[index], prefix)
		}
	}
}

func TestProductionRuntimeOwnershipMigrationCreatesImmutableFencingLease(t *testing.T) {
	migrations, err := readMigrations(migrationFiles)
	if err != nil {
		t.Fatal(err)
	}
	var ownershipMigration migration
	for _, item := range migrations {
		if item.version == "0007_runtime_ownership" {
			ownershipMigration = item
			break
		}
	}
	if ownershipMigration.version == "" {
		t.Fatal("production migrations do not include 0007_runtime_ownership")
	}
	ownershipSQL := strings.Join(strings.Fields(string(ownershipMigration.contents)), " ")
	for _, required := range []string{
		"CREATE TABLE IF NOT EXISTS runtime_account_owners",
		"account_id BIGINT UNSIGNED NOT NULL",
		"owner_token BINARY(32) NOT NULL",
		"fencing_epoch BIGINT UNSIGNED NOT NULL",
		"expires_at TIMESTAMP(6) NOT NULL",
		"PRIMARY KEY (account_id)",
		"KEY idx_runtime_account_owners_expiry (expires_at, account_id)",
		"FOREIGN KEY (account_id) REFERENCES streamer_accounts (id)",
		"CHECK (fencing_epoch > 0)",
		"INSERT IGNORE INTO runtime_account_owners",
		"FROM live_sessions",
		"WHERE ended_at IS NULL",
	} {
		if !strings.Contains(ownershipSQL, required) {
			t.Fatalf("0007 ownership schema missing %q", required)
		}
	}
	statements := splitStatements(ownershipMigration.contents)
	if len(statements) != 2 || !strings.Contains(statements[0], "CREATE TABLE IF NOT EXISTS") || !strings.Contains(statements[1], "INSERT IGNORE") {
		t.Fatalf("0007 statements=%d want idempotent CREATE plus open-session bootstrap", len(statements))
	}
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
