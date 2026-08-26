package mysqlstore

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"path"
	"slices"
	"strings"
	"time"

	"github.com/go-sql-driver/mysql"
)

const (
	migrationLockName    = "gift_panel_schema"
	migrationLockSeconds = 30

	schemaMigrationsDDL = `CREATE TABLE IF NOT EXISTS schema_migrations (
    version VARCHAR(255) NOT NULL PRIMARY KEY,
    checksum CHAR(64) NOT NULL,
    applied_at TIMESTAMP(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6)
) ENGINE=InnoDB`
)

//go:embed migrations/*.sql
var migrationFiles embed.FS

type migration struct {
	version  string
	contents []byte
	checksum string
}

// Store owns the hosted service's MySQL connection pool.
type Store struct {
	db *sql.DB
}

// Open creates and verifies a MySQL connection pool. Returned errors are
// deliberately generic so a malformed DSN is never reflected to logs.
func Open(ctx context.Context, dsn string) (*Store, error) {
	if strings.TrimSpace(dsn) == "" {
		return nil, errors.New("open mysql: configuration is empty")
	}
	normalizedDSN, err := normalizeMySQLDSN(dsn)
	if err != nil {
		return nil, errors.New("open mysql: invalid configuration")
	}
	db, err := sql.Open("mysql", normalizedDSN)
	if err != nil {
		return nil, errors.New("open mysql: invalid configuration")
	}
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, errors.New("open mysql: database unavailable")
	}
	return &Store{db: db}, nil
}

func normalizeMySQLDSN(dsn string) (string, error) {
	config, err := mysql.ParseDSN(dsn)
	if err != nil {
		return "", err
	}
	config.ParseTime = true
	config.Loc = time.UTC
	if config.Params == nil {
		config.Params = make(map[string]string)
	}
	// Loc controls how the driver scans date/time bytes. The session variable
	// additionally fixes MySQL TIMESTAMP conversion at the connection boundary.
	config.Params["time_zone"] = "'+00:00'"
	return config.FormatDSN(), nil
}

// Close closes the underlying connection pool.
func (store *Store) Close() error {
	if store == nil || store.db == nil {
		return nil
	}
	return store.db.Close()
}

// Database returns a borrowed reference to the Store-owned connection pool for
// composition of SQL-backed hosted modules. Callers must never close it; Store
// remains the sole owner and closes the pool during process shutdown.
func (store *Store) Database() *sql.DB {
	if store == nil {
		return nil
	}
	return store.db
}

// Health reports whether MySQL is reachable without returning database error
// details to the HTTP layer.
func (store *Store) Health(ctx context.Context) error {
	if store == nil || store.db == nil {
		return errors.New("mysql store is not initialized")
	}
	return store.db.PingContext(ctx)
}

// Migrate applies embedded migrations while holding a connection-scoped MySQL
// advisory lock. MySQL DDL implicitly commits, so migrations intentionally use
// idempotent statements and record the checksum only after every statement has
// succeeded; this function does not claim whole-file transactional atomicity.
func (store *Store) Migrate(ctx context.Context) (resultErr error) {
	return store.migrate(ctx, migrationFiles)
}

// migrate is the test seam for supplying a fixed migration filesystem.
// Production callers must use Migrate, which always uses migrationFiles.
func (store *Store) migrate(ctx context.Context, fileSystem fs.FS) (resultErr error) {
	if store == nil || store.db == nil {
		return errors.New("migrate mysql: store is not initialized")
	}
	migrations, err := readMigrations(fileSystem)
	if err != nil {
		return fmt.Errorf("read migrations: %w", err)
	}

	connection, err := store.db.Conn(ctx)
	if err != nil {
		return fmt.Errorf("acquire migration connection: %w", err)
	}
	defer connection.Close()

	if err := acquireMigrationLock(ctx, connection); err != nil {
		return err
	}
	defer func() {
		releaseCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := releaseMigrationLock(releaseCtx, connection); err != nil {
			resultErr = errors.Join(resultErr, err)
		}
	}()

	if _, err := connection.ExecContext(ctx, schemaMigrationsDDL); err != nil {
		return fmt.Errorf("create schema migrations table: %w", err)
	}

	for _, item := range migrations {
		var appliedChecksum string
		err := connection.QueryRowContext(ctx,
			"SELECT checksum FROM schema_migrations WHERE version = ?",
			item.version,
		).Scan(&appliedChecksum)
		switch {
		case err == nil:
			if appliedChecksum != item.checksum {
				return fmt.Errorf("migration %s checksum mismatch", item.version)
			}
			continue
		case errors.Is(err, sql.ErrNoRows):
		case err != nil:
			return fmt.Errorf("read migration %s: %w", item.version, err)
		}

		for _, statement := range splitStatements(item.contents) {
			if _, err := connection.ExecContext(ctx, statement); err != nil {
				return fmt.Errorf("apply migration %s: %w", item.version, err)
			}
		}
		if _, err := connection.ExecContext(ctx,
			"INSERT INTO schema_migrations (version, checksum) VALUES (?, ?)",
			item.version,
			item.checksum,
		); err != nil {
			return fmt.Errorf("record migration %s: %w", item.version, err)
		}
	}
	return nil
}

func acquireMigrationLock(ctx context.Context, connection *sql.Conn) error {
	var acquired sql.NullInt64
	if err := connection.QueryRowContext(ctx,
		"SELECT GET_LOCK(?, ?)",
		migrationLockName,
		migrationLockSeconds,
	).Scan(&acquired); err != nil {
		return fmt.Errorf("acquire migration lock: %w", err)
	}
	if !acquired.Valid || acquired.Int64 != 1 {
		return errors.New("acquire migration lock: timed out")
	}
	return nil
}

func releaseMigrationLock(ctx context.Context, connection *sql.Conn) error {
	var released sql.NullInt64
	if err := connection.QueryRowContext(ctx,
		"SELECT RELEASE_LOCK(?)",
		migrationLockName,
	).Scan(&released); err != nil {
		return fmt.Errorf("release migration lock: %w", err)
	}
	if !released.Valid || released.Int64 != 1 {
		return errors.New("release migration lock: lock was not held")
	}
	return nil
}

func readMigrations(fileSystem fs.FS) ([]migration, error) {
	names, err := fs.Glob(fileSystem, "migrations/*.sql")
	if err != nil {
		return nil, err
	}
	slices.Sort(names)
	result := make([]migration, 0, len(names))
	for _, name := range names {
		contents, err := fs.ReadFile(fileSystem, name)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", name, err)
		}
		version := strings.TrimSuffix(path.Base(name), path.Ext(name))
		if version == "" {
			return nil, fmt.Errorf("migration %s has an empty version", name)
		}
		checksum := fmt.Sprintf("%x", sha256.Sum256(contents))
		result = append(result, migration{version: version, contents: contents, checksum: checksum})
	}
	return result, nil
}

func splitStatements(contents []byte) []string {
	statements := make([]string, 0)
	var statement strings.Builder
	var quote byte
	lineComment := false
	blockComment := false
	flush := func() {
		if value := strings.TrimSpace(statement.String()); value != "" {
			statements = append(statements, value)
		}
		statement.Reset()
	}
	for index := 0; index < len(contents); index++ {
		current := contents[index]
		next := byte(0)
		if index+1 < len(contents) {
			next = contents[index+1]
		}
		if lineComment {
			statement.WriteByte(current)
			if current == '\n' {
				lineComment = false
			}
			continue
		}
		if blockComment {
			statement.WriteByte(current)
			if current == '*' && next == '/' {
				statement.WriteByte(next)
				index++
				blockComment = false
			}
			continue
		}
		if quote != 0 {
			statement.WriteByte(current)
			if current == '\\' && next != 0 {
				statement.WriteByte(next)
				index++
				continue
			}
			if current == quote {
				if next == quote {
					statement.WriteByte(next)
					index++
				} else {
					quote = 0
				}
			}
			continue
		}
		switch {
		case current == '-' && next == '-' && (index+2 == len(contents) || isSQLCommentSpace(contents[index+2])):
			statement.WriteByte(current)
			statement.WriteByte(next)
			index++
			lineComment = true
		case current == '#':
			statement.WriteByte(current)
			lineComment = true
		case current == '/' && next == '*':
			statement.WriteByte(current)
			statement.WriteByte(next)
			index++
			blockComment = true
		case current == '\'' || current == '"' || current == '`':
			statement.WriteByte(current)
			quote = current
		case current == ';':
			flush()
		default:
			statement.WriteByte(current)
		}
	}
	flush()
	return statements
}

func isSQLCommentSpace(value byte) bool {
	return value == ' ' || value == '\t' || value == '\r' || value == '\n'
}
