//go:build integration

package mysqlstore

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"testing/fstest"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/go-sql-driver/mysql"

	"bilibili-live-gift-panel/internal/hosted/adminidentity"
	"bilibili-live-gift-panel/internal/hosted/configuration"
	"bilibili-live-gift-panel/internal/hosted/identity"
	"bilibili-live-gift-panel/internal/hosted/invitation"
	"bilibili-live-gift-panel/internal/hosted/security"
)

const (
	testComposeProject = "gift-panel-hosted-test"
	testMySQLPort      = "13306"
)

var testSchemaSequence atomic.Uint64

func TestIntegrationRealMySQLSessionInventoryMigration(t *testing.T) {
	dsn := integrationDSN(t)

	t.Run("schema constraints", func(t *testing.T) {
		store := freshIntegrationStore(t, dsn, true)
		ctx := integrationContext(t)
		db := store.Database()
		for _, column := range []struct {
			name, dataType string
		}{
			{"public_id", "binary"},
			{"device_label", "varchar"},
			{"client_network", "varchar"},
			{"last_seen_at", "datetime"},
		} {
			var dataType, nullable string
			if err := db.QueryRowContext(ctx, `SELECT DATA_TYPE, IS_NULLABLE FROM information_schema.columns WHERE table_schema = DATABASE() AND table_name = 'site_sessions' AND column_name = ?`, column.name).Scan(&dataType, &nullable); err != nil {
				t.Fatalf("read site_sessions.%s: %v", column.name, err)
			}
			if dataType != column.dataType || nullable != "NO" {
				t.Fatalf("site_sessions.%s type=%s nullable=%s, want %s/NO", column.name, dataType, nullable, column.dataType)
			}
		}
		var uniquePublicID int
		if err := db.QueryRowContext(ctx, `SELECT COUNT(*) FROM information_schema.statistics WHERE table_schema = DATABASE() AND table_name = 'site_sessions' AND index_name = 'uq_site_sessions_public_id' AND non_unique = 0`).Scan(&uniquePublicID); err != nil || uniquePublicID != 1 {
			t.Fatalf("unique public ID index count=%d error=%v", uniquePublicID, err)
		}
		var retentionColumns string
		if err := db.QueryRowContext(ctx, `SELECT GROUP_CONCAT(CONCAT(column_name, ':', collation) ORDER BY seq_in_index SEPARATOR ',') FROM information_schema.statistics WHERE table_schema = DATABASE() AND table_name = 'admin_login_events' AND index_name = 'idx_admin_login_events_occurred'`).Scan(&retentionColumns); err != nil || retentionColumns != "occurred_at:D,id:D" {
			t.Fatalf("login retention index=%q error=%v", retentionColumns, err)
		}
		var createTable, tableName string
		if err := db.QueryRowContext(ctx, "SHOW CREATE TABLE admin_login_events").Scan(&tableName, &createTable); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(createTable, "`result` in (_utf8mb4'success',_utf8mb4'failure')") {
			t.Fatalf("admin_login_events result CHECK missing: %s", createTable)
		}
	})

	t.Run("legacy rows are backfilled and future account sessions keep safe defaults", func(t *testing.T) {
		store := freshIntegrationStore(t, dsn, false)
		ctx := integrationContext(t)
		prior := fstest.MapFS{}
		migrations, err := readMigrations(migrationFiles)
		if err != nil {
			t.Fatal(err)
		}
		for _, item := range migrations {
			if item.version != "0012_admin_session_inventory" {
				prior["migrations/"+item.version+".sql"] = &fstest.MapFile{Data: bytes.Clone(item.contents)}
			}
		}
		if err := store.migrate(ctx, prior); err != nil {
			t.Fatalf("migrate through 0011: %v", err)
		}
		db := store.Database()
		account := insertAccount(t, ctx, db)
		created := time.Date(2026, 8, 23, 11, 0, 0, 0, time.UTC)
		if _, err := db.ExecContext(ctx, "INSERT INTO site_sessions (account_id, token_hash, credential_epoch, created_at, expires_at) VALUES (?, ?, 1, ?, ?)", account, bytesOf(0x71), created, created.Add(time.Hour)); err != nil {
			t.Fatal(err)
		}
		if err := store.Migrate(ctx); err != nil {
			t.Fatalf("apply 0012: %v", err)
		}
		var publicID, deviceLabel, clientNetwork string
		var lastSeen time.Time
		if err := db.QueryRowContext(ctx, "SELECT HEX(public_id), device_label, client_network, last_seen_at FROM site_sessions WHERE token_hash = ?", bytesOf(0x71)).Scan(&publicID, &deviceLabel, &clientNetwork, &lastSeen); err != nil {
			t.Fatal(err)
		}
		if len(publicID) != 32 || deviceLabel != "其他设备 · 其他浏览器" || clientNetwork != "—" || !lastSeen.Equal(created) {
			t.Fatalf("backfill publicID=%q device=%q network=%q lastSeen=%s", publicID, deviceLabel, clientNetwork, lastSeen)
		}
		if _, err := db.ExecContext(ctx, "INSERT INTO site_sessions (account_id, token_hash, credential_epoch, created_at, expires_at) VALUES (?, ?, 1, ?, ?)", account, bytesOf(0x72), created.Add(time.Minute), created.Add(2*time.Hour)); err != nil {
			t.Fatalf("post-migration account session defaults: %v", err)
		}
		var generatedPublicID string
		if err := db.QueryRowContext(ctx, "SELECT HEX(public_id) FROM site_sessions WHERE token_hash = ?", bytesOf(0x72)).Scan(&generatedPublicID); err != nil || len(generatedPublicID) != 32 || generatedPublicID == publicID {
			t.Fatalf("generated public ID=%q error=%v", generatedPublicID, err)
		}
	})

	t.Run("repository operations enforce touch revoke and retention boundaries", func(t *testing.T) {
		store := freshIntegrationStore(t, dsn, true)
		ctx := integrationContext(t)
		db := store.Database()
		now := time.Date(2026, 8, 23, 12, 0, 0, 0, time.UTC)
		if _, err := db.ExecContext(ctx, "INSERT INTO admin_identity (id, credential_epoch, email_ciphertext, created_at, updated_at) VALUES (1, 1, ?, ?, ?)", []byte("encrypted-email"), now, now); err != nil {
			t.Fatal(err)
		}
		repository := adminidentity.NewRepository(db)
		firstHash := bytesOf(0x81)
		first, err := repository.CreateAdminSession(ctx, adminidentity.EmailLoginSessionAttempt{ExpectedCredentialEpoch: 1, TokenHash: firstHash, CreatedAt: now, ExpiresAt: now.Add(time.Hour)}, adminidentity.ClientSummary{DeviceLabel: "iPhone · Safari", ClientNetwork: "203.0.113.*"})
		if err != nil {
			t.Fatal(err)
		}
		second, err := repository.CreateAdminSession(ctx, adminidentity.EmailLoginSessionAttempt{ExpectedCredentialEpoch: 1, TokenHash: bytesOf(0x82), CreatedAt: now, ExpiresAt: now.Add(time.Hour)}, adminidentity.ClientSummary{DeviceLabel: "Windows · Edge", ClientNetwork: "198.51.100.*"})
		if err != nil {
			t.Fatal(err)
		}
		if err := repository.TouchAdminSession(ctx, firstHash, now.Add(4*time.Minute)); err != nil {
			t.Fatal(err)
		}
		var lastSeen time.Time
		if err := db.QueryRowContext(ctx, "SELECT last_seen_at FROM site_sessions WHERE public_id = UNHEX(?)", first.PublicID).Scan(&lastSeen); err != nil || !lastSeen.Equal(now) {
			t.Fatalf("throttled lastSeen=%s error=%v", lastSeen, err)
		}
		if err := repository.TouchAdminSession(ctx, firstHash, now.Add(6*time.Minute)); err != nil {
			t.Fatal(err)
		}
		if err := db.QueryRowContext(ctx, "SELECT last_seen_at FROM site_sessions WHERE public_id = UNHEX(?)", first.PublicID).Scan(&lastSeen); err != nil || !lastSeen.Equal(now.Add(6*time.Minute)) {
			t.Fatalf("advanced lastSeen=%s error=%v", lastSeen, err)
		}
		if err := repository.RevokeAdminSession(ctx, firstHash, first.PublicID, now.Add(7*time.Minute)); !errors.Is(err, adminidentity.ErrCurrentAdminSession) {
			t.Fatalf("current revoke error=%v", err)
		}
		if err := repository.RevokeAdminSession(ctx, firstHash, second.PublicID, now.Add(7*time.Minute)); err != nil {
			t.Fatalf("target revoke: %v", err)
		}
		var firstRevokedAt time.Time
		if err := db.QueryRowContext(ctx, "SELECT revoked_at FROM site_sessions WHERE public_id = UNHEX(?)", second.PublicID).Scan(&firstRevokedAt); err != nil {
			t.Fatalf("read first revocation: %v", err)
		}
		if err := repository.RevokeAdminSession(ctx, firstHash, second.PublicID, now.Add(8*time.Minute)); err != nil {
			t.Fatalf("idempotent target revoke retry: %v", err)
		}
		var retriedRevokedAt time.Time
		if err := db.QueryRowContext(ctx, "SELECT revoked_at FROM site_sessions WHERE public_id = UNHEX(?)", second.PublicID).Scan(&retriedRevokedAt); err != nil {
			t.Fatalf("read retried revocation: %v", err)
		}
		if !firstRevokedAt.Equal(now.Add(7*time.Minute)) || !retriedRevokedAt.Equal(firstRevokedAt) {
			t.Fatalf("revoked_at first=%v retry=%v", firstRevokedAt, retriedRevokedAt)
		}
		sessions, err := repository.ListAdminSessions(ctx, firstHash, now.Add(7*time.Minute))
		if err != nil || len(sessions) != 1 || !sessions[0].Current || sessions[0].PublicID != first.PublicID {
			t.Fatalf("sessions=%#v error=%v", sessions, err)
		}
		for index := 0; index < 105; index++ {
			if err := repository.RecordAdminLoginEvent(ctx, adminidentity.AdministratorLoginEvent{Result: "failure", DeviceLabel: "Android · Chrome", ClientNetwork: "2001:db8:abcd:1234::*", OccurredAt: now.Add(time.Duration(index) * time.Second)}); err != nil {
				t.Fatalf("record login event %d: %v", index, err)
			}
		}
		var retained int
		if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM admin_login_events").Scan(&retained); err != nil || retained != 100 {
			t.Fatalf("retained login events=%d error=%v", retained, err)
		}
		events, err := repository.ListAdminLoginEvents(ctx, 50)
		if err != nil || len(events) != 50 {
			t.Fatalf("events=%d error=%v", len(events), err)
		}
		if !events[0].OccurredAt.Equal(now.Add(104 * time.Second)) {
			t.Fatalf("newest event=%v", events[0].OccurredAt)
		}
	})
}

const invitationContentionQuery = `SELECT COUNT(DISTINCT trx.trx_id)
FROM information_schema.innodb_trx AS trx
JOIN performance_schema.data_locks AS requested
  ON requested.engine_lock_id = trx.trx_requested_lock_id
JOIN performance_schema.data_lock_waits AS waits
  ON waits.requesting_engine_lock_id = requested.engine_lock_id
 AND waits.engine = requested.engine
WHERE trx.trx_state = 'LOCK WAIT'
  AND requested.lock_status = 'WAITING'
  AND requested.object_schema = DATABASE()
  AND requested.object_name = 'invitations'`

func TestIntegrationHarnessContract(t *testing.T) {
	root := integrationRepositoryRoot(t)
	compose := readIntegrationFile(t, filepath.Join(root, "deploy", "hosted", "docker-compose.test.yml"))
	for _, required := range []string{
		"127.0.0.1:" + testMySQLPort + ":3306",
		"hosted_mysql_test_data",
		"HOSTED_MYSQL_TEST_ROOT_PASSWORD",
		"mysqladmin ping",
	} {
		if !strings.Contains(compose, required) {
			t.Errorf("docker-compose.test.yml missing %q", required)
		}
	}
	for _, forbidden := range []string{"0.0.0.0:", "\"3306:3306\"", "restart:", "external:"} {
		if strings.Contains(compose, forbidden) {
			t.Errorf("docker-compose.test.yml contains forbidden %q", forbidden)
		}
	}
	assertExactStrings(t, "MySQL published ports", composeSequence(compose, "    ports:"), []string{"127.0.0.1:" + testMySQLPort + ":3306"})
	assertExactStrings(t, "MySQL volume mounts", composeSequence(compose, "    volumes:"), []string{"hosted_mysql_test_data:/var/lib/mysql"})
	assertExactStrings(t, "top-level volumes", composeMapKeys(compose, "volumes:"), []string{"hosted_mysql_test_data"})

	var manifest struct {
		Scripts map[string]string `json:"scripts"`
	}
	if err := json.Unmarshal([]byte(readIntegrationFile(t, filepath.Join(root, "package.json"))), &manifest); err != nil {
		t.Fatalf("parse package.json: %v", err)
	}
	runner := manifest.Scripts["test:hosted-mysql"]
	if runner != "node scripts/test-hosted-mysql.mjs" {
		t.Fatalf("test:hosted-mysql=%q, want private Node runner", runner)
	}
	runnerSource := readIntegrationFile(t, filepath.Join(root, "scripts", "test-hosted-mysql.mjs"))
	for _, required := range []string{
		"try", "finally", testComposeProject, "docker-compose.test.yml", "'up', '-d', '--wait'",
		"HOSTED_MYSQL_TEST_REQUIRED", "HOSTED_MYSQL_TEST_DSN", "-tags=integration",
		"'-run', '^TestIntegration'", "./internal/hosted/store/mysqlstore", "'down', '--volumes', '--remove-orphans'",
	} {
		if !strings.Contains(runnerSource, required) {
			t.Errorf("private MySQL runner missing %q", required)
		}
	}
	packageSource := readIntegrationFile(t, filepath.Join(root, "package.json"))
	for _, forbidden := range []string{"HOSTED_MYSQL_TEST_DSN", "gift-panel-root-test-only", "tcp(127.0.0.1:13306)"} {
		if strings.Contains(packageSource, forbidden) {
			t.Errorf("package.json exposes private runner value %q", forbidden)
		}
	}
}

func TestIntegrationDSNAcceptsOnlyLiteralLoopbackTCP(t *testing.T) {
	tests := []struct {
		address string
		want    bool
	}{
		{address: "127.0.0.1:13306", want: true},
		{address: "[::1]:13306", want: true},
		{address: "localhost:13306", want: false},
		{address: "0.0.0.0:13306", want: false},
		{address: "192.0.2.1:3306", want: false},
		{address: "127.0.0.1", want: false},
	}
	for _, test := range tests {
		t.Run(test.address, func(t *testing.T) {
			if got := loopbackMySQLAddress(test.address); got != test.want {
				t.Fatalf("loopbackMySQLAddress(%q)=%v, want %v", test.address, got, test.want)
			}
		})
	}
}

func TestIntegrationComposeSafetyParserPreservesAdditionalEntries(t *testing.T) {
	fixture := "services:\n  mysql:\n    ports:\n      - \"127.0.0.1:13306:3306\"\n      - \"3306:3306\"\n    volumes:\n      - hosted_mysql_test_data:/var/lib/mysql\n      - hosted_mysql_data:/production\nvolumes:\n  hosted_mysql_test_data: {}\n  hosted_mysql_data: {}\n"
	assertExactStrings(t, "fixture ports", composeSequence(fixture, "    ports:"), []string{"127.0.0.1:13306:3306", "3306:3306"})
	assertExactStrings(t, "fixture mounts", composeSequence(fixture, "    volumes:"), []string{"hosted_mysql_test_data:/var/lib/mysql", "hosted_mysql_data:/production"})
	assertExactStrings(t, "fixture volumes", composeMapKeys(fixture, "volumes:"), []string{"hosted_mysql_test_data", "hosted_mysql_data"})
}

func TestIntegrationSchemaCleanupIsBoundedAndAccumulatesFailures(t *testing.T) {
	storeDatabase, storeMock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	adminDatabase, adminMock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	storeCloseErr := errors.New("store close failed")
	dropErr := errors.New("drop failed")
	adminCloseErr := errors.New("admin close failed")
	storeMock.ExpectClose().WillReturnError(storeCloseErr)
	adminMock.ExpectExec(regexp.QuoteMeta("DROP DATABASE `gift_panel_it_1_1`")).WillReturnError(dropErr)
	adminMock.ExpectClose().WillReturnError(adminCloseErr)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	err = cleanupIntegrationSchema(ctx, &Store{db: storeDatabase}, adminDatabase, "gift_panel_it_1_1")
	for _, want := range []error{storeCloseErr, dropErr, adminCloseErr} {
		if !errors.Is(err, want) {
			t.Fatalf("cleanup error=%v, want joined %v", err, want)
		}
	}
	if err := storeMock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
	if err := adminMock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}

	unboundedDatabase, unboundedMock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	unboundedMock.ExpectClose()
	err = cleanupIntegrationSchema(context.Background(), nil, unboundedDatabase, "gift_panel_it_1_2")
	if err == nil || !strings.Contains(err.Error(), "bounded context") {
		t.Fatalf("unbounded cleanup error=%v", err)
	}
	if err := unboundedMock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestIntegrationContentionEvidenceWaitsForTwoActiveTransactions(t *testing.T) {
	database, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	mock.ExpectQuery(regexp.QuoteMeta(invitationContentionQuery)).WillReturnRows(sqlmock.NewRows([]string{"waiters"}).AddRow(1))
	mock.ExpectQuery(regexp.QuoteMeta(invitationContentionQuery)).WillReturnRows(sqlmock.NewRows([]string{"waiters"}).AddRow(2))
	polls := make(chan time.Time, 1)
	polls <- time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := waitForInvitationContention(ctx, database, 2, polls); err != nil {
		t.Fatal(err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

func TestIntegrationRealMySQLMigrationAndTenantContracts(t *testing.T) {
	dsn := integrationDSN(t)

	t.Run("embedded migrations apply twice", func(t *testing.T) {
		store := freshIntegrationStore(t, dsn, false)
		ctx := integrationContext(t)
		if err := store.Migrate(ctx); err != nil {
			t.Fatalf("first Migrate: %v", err)
		}
		if err := store.Migrate(ctx); err != nil {
			t.Fatalf("second Migrate: %v", err)
		}
		migrations, err := readMigrations(migrationFiles)
		if err != nil {
			t.Fatal(err)
		}
		var count int
		if err := store.Database().QueryRowContext(ctx, "SELECT COUNT(*) FROM schema_migrations").Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != len(migrations) {
			t.Fatalf("applied migrations=%d, want %d", count, len(migrations))
		}
	})

	t.Run("changed checksum is rejected", func(t *testing.T) {
		store := freshIntegrationStore(t, dsn, true)
		ctx := integrationContext(t)
		if _, err := store.Database().ExecContext(ctx, "UPDATE schema_migrations SET checksum = REPEAT('0', 64) WHERE version = '0001_foundation'"); err != nil {
			t.Fatal(err)
		}
		err := store.Migrate(ctx)
		if err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
			t.Fatalf("Migrate error=%v, want checksum mismatch", err)
		}
	})

	t.Run("partial migration replay is idempotent", func(t *testing.T) {
		store := freshIntegrationStore(t, dsn, true)
		ctx := integrationContext(t)
		if _, err := store.Database().ExecContext(ctx, "DELETE FROM schema_migrations WHERE version = '0008_runtime_dedupe_cleanup_index'"); err != nil {
			t.Fatal(err)
		}
		if err := store.Migrate(ctx); err != nil {
			t.Fatalf("replay Migrate: %v", err)
		}
		var count int
		if err := store.Database().QueryRowContext(ctx, "SELECT COUNT(*) FROM schema_migrations WHERE version = '0008_runtime_dedupe_cleanup_index'").Scan(&count); err != nil {
			t.Fatal(err)
		}
		if count != 1 {
			t.Fatalf("replayed migration rows=%d, want 1", count)
		}
	})

	t.Run("identity and invitation uniqueness guards", func(t *testing.T) {
		store := freshIntegrationStore(t, dsn, true)
		ctx := integrationContext(t)
		db := store.Database()
		first := insertAccount(t, ctx, db)
		second := insertAccount(t, ctx, db)
		lookup := bytesOf(0x11)
		if _, err := db.ExecContext(ctx, "INSERT INTO bili_uid_bindings (account_id, uid_ciphertext, uid_lookup) VALUES (?, ?, ?)", first, []byte("first"), lookup); err != nil {
			t.Fatal(err)
		}
		assertDuplicate(t, execError(ctx, db, "INSERT INTO bili_uid_bindings (account_id, uid_ciphertext, uid_lookup) VALUES (?, ?, ?)", second, []byte("second"), lookup))
		assertDuplicate(t, execError(ctx, db, "INSERT INTO bili_uid_bindings (account_id, uid_ciphertext, uid_lookup) VALUES (?, ?, ?)", first, []byte("other"), bytesOf(0x12)))

		if _, err := db.ExecContext(ctx, "INSERT INTO invitations (code_hash, code_hint, creator_account_id, expires_at) VALUES (?, 'abcd', ?, UTC_TIMESTAMP(6) + INTERVAL 1 HOUR)", bytesOf(0x21), first); err != nil {
			t.Fatal(err)
		}
		assertDuplicate(t, execError(ctx, db, "INSERT INTO invitations (code_hash, code_hint, creator_account_id, expires_at) VALUES (?, 'efgh', ?, UTC_TIMESTAMP(6) + INTERVAL 1 HOUR)", bytesOf(0x21), second))
	})

	t.Run("quota active credential and foreign key guards", func(t *testing.T) {
		store := freshIntegrationStore(t, dsn, true)
		ctx := integrationContext(t)
		db := store.Database()
		account := insertAccount(t, ctx, db)
		if err := execError(ctx, db, "INSERT INTO invitation_quotas (account_id, remaining_quota) VALUES (?, ?)", account, -1); err == nil {
			t.Fatal("negative invitation quota unexpectedly committed")
		}
		if _, err := db.ExecContext(ctx, "INSERT INTO obs_credentials (account_id, public_id, token_hash, credential_epoch) VALUES (?, ?, ?, 1)", account, strings.Repeat("a", 43), bytesOf(0x31)); err != nil {
			t.Fatal(err)
		}
		assertDuplicate(t, execError(ctx, db, "INSERT INTO obs_credentials (account_id, public_id, token_hash, credential_epoch) VALUES (?, ?, ?, 1)", account, strings.Repeat("b", 43), bytesOf(0x32)))
		assertMySQLError(t, execError(ctx, db, "INSERT INTO bili_uid_bindings (account_id, uid_ciphertext, uid_lookup) VALUES (?, ?, ?)", account+1000000, []byte("missing"), bytesOf(0x33)), 1452)

		otherAccount := insertAccount(t, ctx, db)
		result, err := db.ExecContext(ctx, "INSERT INTO account_config_versions (account_id, number, definition_json, source) VALUES (?, 1, JSON_OBJECT(), 'manual')", otherAccount)
		if err != nil {
			t.Fatal(err)
		}
		otherVersion, err := result.LastInsertId()
		if err != nil {
			t.Fatal(err)
		}
		assertMySQLError(t, execError(ctx, db, "INSERT INTO account_active_config (account_id, config_version_id) VALUES (?, ?)", account, otherVersion), 1452)
	})

	t.Run("stale configuration revision is rejected", func(t *testing.T) {
		store := freshIntegrationStore(t, dsn, true)
		ctx := integrationContext(t)
		db := store.Database()
		account := insertAccount(t, ctx, db)
		result, err := db.ExecContext(ctx, "INSERT INTO account_config_versions (account_id, number, definition_json, source) VALUES (?, 1, JSON_OBJECT(), 'manual')", account)
		if err != nil {
			t.Fatal(err)
		}
		version, err := result.LastInsertId()
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.ExecContext(ctx, "INSERT INTO account_runtime_state (account_id, config_version_id, revision, runtime_json) VALUES (?, ?, 1, JSON_OBJECT())", account, version); err != nil {
			t.Fatal(err)
		}
		repository := configuration.NewRepository(db)
		command := configuration.UpdateStateCommand{AccountID: account, ExpectedRevision: 1, Runtime: configuration.RuntimeState{}, UpdatedAt: time.Now().UTC()}
		if state, err := repository.CompareAndSwapState(ctx, command); err != nil || state.Revision != 2 {
			t.Fatalf("first CompareAndSwapState state=%+v err=%v", state, err)
		}
		if _, err := repository.CompareAndSwapState(ctx, command); !errors.Is(err, configuration.ErrRevisionConflict) {
			t.Fatalf("stale CompareAndSwapState error=%v, want ErrRevisionConflict", err)
		}
	})

	t.Run("concurrent invitation redemption has one commit", func(t *testing.T) {
		store := freshIntegrationStore(t, dsn, true)
		ctx := integrationContext(t)
		db := store.Database()
		creator := insertAccount(t, ctx, db)
		now := time.Now().UTC()
		code := "one-integration-code"
		codeHash := sha256.Sum256([]byte(code))
		result, err := db.ExecContext(ctx, "INSERT INTO invitations (code_hash, code_hint, creator_account_id, created_at, expires_at) VALUES (?, 'race', ?, ?, ?)", codeHash[:], creator, now, now.Add(time.Hour))
		if err != nil {
			t.Fatal(err)
		}
		invitationID, err := result.LastInsertId()
		if err != nil {
			t.Fatal(err)
		}
		keys, err := security.NewKeyring(1, bytes.Repeat([]byte{0x31}, 32), bytes.Repeat([]byte{0x72}, 32))
		if err != nil {
			t.Fatal(err)
		}
		blockingTransaction, err := db.BeginTx(ctx, nil)
		if err != nil {
			t.Fatal(err)
		}
		var lockedInvitationID int64
		if err := blockingTransaction.QueryRowContext(ctx, "SELECT id FROM invitations WHERE id = ? FOR UPDATE", invitationID).Scan(&lockedInvitationID); err != nil {
			t.Fatalf("lock invitation row: %v", errors.Join(err, blockingTransaction.Rollback()))
		}
		if lockedInvitationID != invitationID {
			t.Fatalf("locked invitation=%d, want %d: %v", lockedInvitationID, invitationID, blockingTransaction.Rollback())
		}
		first := newIntegrationReservation(0x51, now.Add(5*time.Minute))
		second := newIntegrationReservation(0x52, now.Add(5*time.Minute))
		intents := &integrationIntentSource{reservations: map[string]*integrationReservation{"intent-one": first, "intent-two": second}}
		service, err := invitation.NewService(db, keys, intents, invitation.ServiceOptions{Now: func() time.Time { return now }})
		if err != nil {
			t.Fatal(err)
		}
		start := make(chan struct{})
		results := make(chan error, 2)
		var workers sync.WaitGroup
		for _, intent := range []string{"intent-one", "intent-two"} {
			workers.Add(1)
			go func(intent string) {
				defer workers.Done()
				<-start
				_, err := service.Redeem(ctx, code, intent)
				results <- err
			}(intent)
		}
		close(start)
		contentionCtx, cancelContention := context.WithTimeout(ctx, 10*time.Second)
		poller := time.NewTicker(10 * time.Millisecond)
		contentionErr := waitForInvitationContention(contentionCtx, db, 2, poller.C)
		poller.Stop()
		cancelContention()
		releaseErr := blockingTransaction.Commit()
		if releaseErr != nil {
			releaseErr = errors.Join(releaseErr, blockingTransaction.Rollback())
		}
		workers.Wait()
		close(results)
		if contentionErr != nil || releaseErr != nil {
			t.Fatalf("prove concurrent redemption overlap: %v", errors.Join(contentionErr, releaseErr))
		}
		commits := 0
		for err := range results {
			if err == nil {
				commits++
			} else if !errors.Is(err, invitation.ErrInvitationInvalid) {
				t.Fatalf("redemption worker: %v", err)
			}
		}
		if commits != 1 {
			t.Fatalf("redemption commits=%d, want 1", commits)
		}
		var status string
		var invited int64
		if err := db.QueryRowContext(ctx, "SELECT status, invited_account_id FROM invitations WHERE id = ?", invitationID).Scan(&status, &invited); err != nil {
			t.Fatal(err)
		}
		if status != "used" || invited <= 0 {
			t.Fatalf("stored redemption status=%q invited=%d", status, invited)
		}
		var createdAccounts, sessions, audits int
		if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM streamer_accounts").Scan(&createdAccounts); err != nil {
			t.Fatal(err)
		}
		if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM site_sessions").Scan(&sessions); err != nil {
			t.Fatal(err)
		}
		if err := db.QueryRowContext(ctx, "SELECT COUNT(*) FROM audit_events WHERE event_type = 'invitation_redeemed'").Scan(&audits); err != nil {
			t.Fatal(err)
		}
		if createdAccounts != 2 || sessions != 1 || audits != 1 {
			t.Fatalf("durable redemption accounts=%d sessions=%d audits=%d, want 2/1/1", createdAccounts, sessions, audits)
		}
		if committed := first.committedCount() + second.committedCount(); committed != 1 {
			t.Fatalf("reservation commits=%d, want 1", committed)
		}
	})
}

type integrationIntentSource struct {
	mu           sync.Mutex
	reservations map[string]*integrationReservation
}

func (source *integrationIntentSource) ReserveRegistrationIntent(token string) (identity.RegistrationIntentReservation, error) {
	source.mu.Lock()
	defer source.mu.Unlock()
	reservation := source.reservations[token]
	if reservation == nil || !reservation.reserve() {
		return nil, identity.ErrRegistrationIntentInvalid
	}
	return reservation, nil
}

type integrationReservation struct {
	mu        sync.Mutex
	uid       identity.EncryptedUID
	expiresAt time.Time
	reserved  bool
	valid     bool
	committed bool
}

func newIntegrationReservation(marker byte, expiresAt time.Time) *integrationReservation {
	return &integrationReservation{uid: identity.EncryptedUID{Ciphertext: []byte{marker}, Lookup: bytesOf(marker)}, expiresAt: expiresAt, valid: true}
}

func (reservation *integrationReservation) Identity() (identity.EncryptedUID, time.Time, bool) {
	reservation.mu.Lock()
	defer reservation.mu.Unlock()
	return identity.EncryptedUID{Ciphertext: bytes.Clone(reservation.uid.Ciphertext), Lookup: bytes.Clone(reservation.uid.Lookup)}, reservation.expiresAt, reservation.valid && !reservation.committed
}

func (reservation *integrationReservation) Valid() bool {
	reservation.mu.Lock()
	defer reservation.mu.Unlock()
	return reservation.valid && !reservation.committed
}

func (reservation *integrationReservation) Commit() {
	reservation.mu.Lock()
	reservation.committed = true
	reservation.mu.Unlock()
}

func (reservation *integrationReservation) Abort() {
	reservation.mu.Lock()
	if !reservation.committed {
		reservation.reserved = false
	}
	reservation.mu.Unlock()
}

func (reservation *integrationReservation) reserve() bool {
	reservation.mu.Lock()
	defer reservation.mu.Unlock()
	if reservation.reserved || reservation.committed || !reservation.valid {
		return false
	}
	reservation.reserved = true
	return true
}

func (reservation *integrationReservation) committedCount() int {
	reservation.mu.Lock()
	defer reservation.mu.Unlock()
	if reservation.committed {
		return 1
	}
	return 0
}

func integrationDSN(t *testing.T) string {
	t.Helper()
	dsn := strings.TrimSpace(os.Getenv("HOSTED_MYSQL_TEST_DSN"))
	if dsn == "" {
		message := "real MySQL gate blocked: set HOSTED_MYSQL_TEST_DSN or run npm run test:hosted-mysql"
		if os.Getenv("HOSTED_MYSQL_TEST_REQUIRED") == "1" {
			t.Fatal(message)
		}
		t.Skip(message)
	}
	config, err := mysql.ParseDSN(dsn)
	if err != nil {
		t.Fatal("HOSTED_MYSQL_TEST_DSN is invalid")
	}
	if config.Net != "tcp" || !loopbackMySQLAddress(config.Addr) {
		t.Fatalf("HOSTED_MYSQL_TEST_DSN must use loopback TCP, got network=%q address=%q", config.Net, config.Addr)
	}
	return dsn
}

func loopbackMySQLAddress(address string) bool {
	host, port, err := net.SplitHostPort(address)
	if err != nil || port == "" {
		return false
	}
	return host == "127.0.0.1" || host == "::1"
}

func freshIntegrationStore(t *testing.T, dsn string, migrate bool) *Store {
	t.Helper()
	config, err := mysql.ParseDSN(dsn)
	if err != nil {
		t.Fatal(err)
	}
	config.DBName = ""
	admin, err := sql.Open("mysql", config.FormatDSN())
	if err != nil {
		t.Fatal(err)
	}
	ctx := integrationContext(t)
	schema := fmt.Sprintf("gift_panel_it_%d_%d", os.Getpid(), testSchemaSequence.Add(1))
	if _, err := admin.ExecContext(ctx, "CREATE DATABASE `"+schema+"` CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci"); err != nil {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		cleanupErr := cleanupIntegrationSchema(cleanupCtx, nil, admin, schema)
		cancel()
		t.Fatalf("create isolated schema: %v", errors.Join(err, cleanupErr))
	}
	config.DBName = schema
	store, err := Open(ctx, config.FormatDSN())
	if err != nil {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		cleanupErr := cleanupIntegrationSchema(cleanupCtx, nil, admin, schema)
		cancel()
		t.Fatal(errors.Join(err, cleanupErr))
	}
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		if err := cleanupIntegrationSchema(cleanupCtx, store, admin, schema); err != nil {
			t.Errorf("cleanup isolated schema: %v", err)
		}
	})
	if migrate {
		if err := store.Migrate(ctx); err != nil {
			t.Fatalf("Migrate: %v", err)
		}
	}
	return store
}

func cleanupIntegrationSchema(ctx context.Context, store *Store, admin *sql.DB, schema string) error {
	var result error
	if store != nil {
		result = errors.Join(result, store.Close())
	}
	if admin == nil {
		return result
	}
	if _, bounded := ctx.Deadline(); !bounded {
		result = errors.Join(result, errors.New("cleanup isolated schema requires a bounded context"))
	} else if schema != "" {
		_, err := admin.ExecContext(ctx, "DROP DATABASE `"+schema+"`")
		result = errors.Join(result, err)
	}
	result = errors.Join(result, admin.Close())
	return result
}

func waitForInvitationContention(ctx context.Context, database *sql.DB, wanted int, polls <-chan time.Time) error {
	if database == nil || wanted <= 0 {
		return errors.New("wait for invitation contention: invalid input")
	}
	if _, bounded := ctx.Deadline(); !bounded {
		return errors.New("wait for invitation contention requires a bounded context")
	}
	lastCount := 0
	for {
		if err := database.QueryRowContext(ctx, invitationContentionQuery).Scan(&lastCount); err != nil {
			return fmt.Errorf("read invitation contention evidence: %w", err)
		}
		if lastCount >= wanted {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("wait for invitation contention: observed %d of %d active waiters: %w", lastCount, wanted, ctx.Err())
		case <-polls:
		}
	}
}

func integrationContext(t *testing.T) context.Context {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func insertAccount(t *testing.T, ctx context.Context, db *sql.DB) int64 {
	t.Helper()
	result, err := db.ExecContext(ctx, "INSERT INTO streamer_accounts () VALUES ()")
	if err != nil {
		t.Fatal(err)
	}
	id, err := result.LastInsertId()
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func execError(ctx context.Context, db *sql.DB, query string, args ...any) error {
	_, err := db.ExecContext(ctx, query, args...)
	return err
}

func assertDuplicate(t *testing.T, err error) {
	t.Helper()
	assertMySQLError(t, err, 1062)
}

func assertMySQLError(t *testing.T, err error, number uint16) {
	t.Helper()
	var mysqlError *mysql.MySQLError
	if !errors.As(err, &mysqlError) || mysqlError.Number != number {
		t.Fatalf("error=%v, want MySQL error %d", err, number)
	}
}

func bytesOf(value byte) []byte {
	return bytes.Repeat([]byte{value}, sha256.Size)
}

func integrationRepositoryRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", "..", "..", "..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func readIntegrationFile(t *testing.T, name string) string {
	t.Helper()
	contents, err := os.ReadFile(name)
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return string(contents)
}

func composeSequence(contents, header string) []string {
	lines := strings.Split(strings.ReplaceAll(contents, "\r\n", "\n"), "\n")
	headerIndent := len(header) - len(strings.TrimLeft(header, " "))
	for index, line := range lines {
		if line != header {
			continue
		}
		var values []string
		for _, candidate := range lines[index+1:] {
			if strings.TrimSpace(candidate) == "" {
				continue
			}
			indent := len(candidate) - len(strings.TrimLeft(candidate, " "))
			if indent <= headerIndent {
				break
			}
			trimmed := strings.TrimSpace(candidate)
			if indent == headerIndent+2 && strings.HasPrefix(trimmed, "- ") {
				values = append(values, strings.Trim(strings.TrimSpace(strings.TrimPrefix(trimmed, "- ")), "\"'"))
			}
		}
		return values
	}
	return nil
}

func composeMapKeys(contents, header string) []string {
	lines := strings.Split(strings.ReplaceAll(contents, "\r\n", "\n"), "\n")
	headerIndent := len(header) - len(strings.TrimLeft(header, " "))
	for index, line := range lines {
		if line != header {
			continue
		}
		var keys []string
		for _, candidate := range lines[index+1:] {
			if strings.TrimSpace(candidate) == "" {
				continue
			}
			indent := len(candidate) - len(strings.TrimLeft(candidate, " "))
			if indent <= headerIndent {
				break
			}
			if indent != headerIndent+2 {
				continue
			}
			key, _, ok := strings.Cut(strings.TrimSpace(candidate), ":")
			if ok {
				keys = append(keys, strings.TrimSpace(key))
			}
		}
		return keys
	}
	return nil
}

func assertExactStrings(t *testing.T, label string, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s=%q, want exactly %q", label, got, want)
	}
	for index := range want {
		if got[index] != want[index] {
			t.Fatalf("%s=%q, want exactly %q", label, got, want)
		}
	}
}
