package migration

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"bilibili-live-gift-panel/internal/hosted/configuration"
)

var (
	ErrInvalidInput = errors.New("migration: invalid input")
	ErrUnavailable  = errors.New("migration: unavailable")
	ErrPreviewLimit = errors.New("migration: preview limit reached")
)

// Preview deliberately contains no account ID, normalized configuration,
// runtime, raw upload, or canonical JSON.
type Preview struct {
	ID             int64     `json:"id"`
	ExpiresAt      time.Time `json:"expiresAt"`
	Reused         bool      `json:"reused"`
	Counts         Counts    `json:"counts"`
	Warnings       []string  `json:"warnings,omitempty"`
	Ignored        []string  `json:"ignored,omitempty"`
	RoomSuggestion string    `json:"roomSuggestion,omitempty"`
}

type previewCommand struct {
	AccountID            int64
	Definition           configuration.Definition
	Runtime              configuration.RuntimeState
	RoomSuggestion       string
	Source               Source
	Counts               Counts
	Report               Report
	Hash                 [sha256.Size]byte
	CreatedAt, ExpiresAt time.Time
}
type storedPreview struct {
	ID, AccountID int64
	ExpiresAt     time.Time
	Reused        bool
}

// Repository is intentionally narrow: the migration package owns preview
// transaction and quota semantics without exposing a raw-upload repository.
type Repository interface {
	Preview(context.Context, previewCommand) (storedPreview, error)
}

type Service struct {
	repository Repository
	now        func() time.Time
}

func NewService(repository Repository, now func() time.Time) *Service {
	if now == nil {
		now = time.Now
	}
	return &Service{repository: repository, now: now}
}

func (service *Service) Preview(ctx context.Context, accountID int64, envelope Envelope) (Preview, error) {
	if service == nil || service.repository == nil || service.now == nil || accountID <= 0 {
		return Preview{}, ErrInvalidInput
	}
	definition, runtime, _, hash, err := freshCanonical(envelope.Definition, envelope.Runtime)
	if err != nil {
		return Preview{}, ErrInvalidInput
	}
	now := service.now().UTC()
	command := previewCommand{AccountID: accountID, Definition: definition, Runtime: runtime, RoomSuggestion: envelope.RoomSuggestion, Source: envelope.Source, Counts: countDefinition(definition), Report: envelope.Report, Hash: hash, CreatedAt: now, ExpiresAt: now.Add(24 * time.Hour)}
	if command.Source.AppVersion == "" || command.Source.ConfigurationSchemaVersion < 1 || command.Source.ConfigurationSchemaVersion > configurationSchemaVersion {
		return Preview{}, ErrInvalidInput
	}
	stored, err := service.repository.Preview(ctx, command)
	if err != nil {
		return Preview{}, err
	}
	if stored.ID <= 0 || stored.AccountID != accountID || !stored.ExpiresAt.After(now) || stored.ExpiresAt.After(command.ExpiresAt) {
		return Preview{}, ErrUnavailable
	}
	return Preview{ID: stored.ID, ExpiresAt: stored.ExpiresAt, Reused: stored.Reused, Counts: command.Counts, Warnings: append([]string(nil), command.Report.Warnings...), Ignored: append([]string(nil), command.Report.Ignored...), RoomSuggestion: command.RoomSuggestion}, nil
}

func freshCanonical(definition configuration.Definition, runtime configuration.RuntimeState) (configuration.Definition, configuration.RuntimeState, []byte, [sha256.Size]byte, error) {
	snapshot, err := configuration.Join(definition, runtime)
	if err != nil {
		return configuration.Definition{}, configuration.RuntimeState{}, nil, [sha256.Size]byte{}, err
	}
	definition, runtime, err = configuration.Split(snapshot)
	if err != nil {
		return configuration.Definition{}, configuration.RuntimeState{}, nil, [sha256.Size]byte{}, err
	}
	canonical, err := json.Marshal(struct {
		Definition configuration.Definition   `json:"definition"`
		Runtime    configuration.RuntimeState `json:"runtime"`
	}{definition, runtime})
	if err != nil {
		return configuration.Definition{}, configuration.RuntimeState{}, nil, [sha256.Size]byte{}, err
	}
	return definition, runtime, canonical, sha256.Sum256(canonical), nil
}
func countDefinition(definition configuration.Definition) Counts {
	result := Counts{Attributes: len(definition.Attributes), Rules: len(definition.Rules), Activities: len(definition.Activities), GiftTargetPanels: len(definition.GiftTargetPanels)}
	for _, panel := range definition.GiftTargetPanels {
		result.GiftTargetItems += len(panel.Items)
	}
	return result
}

type sqlRepository struct{ db *sql.DB }

func NewRepository(db *sql.DB) Repository { return &sqlRepository{db: db} }

const previewBaseQuery = "SELECT COALESCE(v.number, 0), COALESCE(s.revision, 0) FROM streamer_accounts AS a LEFT JOIN account_active_config AS active ON active.account_id = a.id LEFT JOIN account_config_versions AS v ON v.account_id = active.account_id AND v.id = active.config_version_id LEFT JOIN account_runtime_state AS s ON s.account_id = a.id AND s.config_version_id = active.config_version_id WHERE a.id = ? FOR UPDATE"

// Only a still-previewed draft may be reused or refreshed. Applied, pending,
// cancelled, rolled-back, and expiry-marked rows are historical records and
// are never overwritten merely because a client uploads matching bytes.
const previewExistingQuery = "SELECT id, status, expires_at FROM migration_jobs WHERE account_id = ? AND request_hash = ? FOR UPDATE"
const previewQuotaQuery = "SELECT COUNT(*) FROM migration_jobs WHERE account_id = ? AND created_at >= ? AND created_at < ? AND status = 'previewed'"
const previewInsertQuery = "INSERT INTO migration_jobs (account_id, request_hash, status, base_config_version_number, base_state_revision, definition_json, runtime_json, room_suggestion, source_app_version, source_schema_version, report_json, created_at, expires_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)"
const previewRefreshQuery = "UPDATE migration_jobs SET status = ?, base_config_version_number = ?, base_state_revision = ?, definition_json = ?, runtime_json = ?, room_suggestion = ?, source_app_version = ?, source_schema_version = ?, report_json = ?, created_at = ?, expires_at = ?, applied_at = NULL, cancelled_at = NULL, rolled_back_at = NULL WHERE id = ? AND account_id = ?"

func (repository *sqlRepository) Preview(ctx context.Context, command previewCommand) (storedPreview, error) {
	if repository == nil || repository.db == nil || command.AccountID <= 0 || command.CreatedAt.IsZero() || !command.ExpiresAt.Equal(command.CreatedAt.Add(24*time.Hour)) {
		return storedPreview{}, ErrInvalidInput
	}
	definitionJSON, err := json.Marshal(command.Definition)
	if err != nil {
		return storedPreview{}, ErrInvalidInput
	}
	runtimeJSON, err := json.Marshal(command.Runtime)
	if err != nil {
		return storedPreview{}, ErrInvalidInput
	}
	reportJSON, err := json.Marshal(command.Report)
	if err != nil {
		return storedPreview{}, ErrInvalidInput
	}
	transaction, err := repository.db.BeginTx(ctx, nil)
	if err != nil {
		return storedPreview{}, ErrUnavailable
	}
	committed := false
	defer func() {
		if !committed {
			_ = transaction.Rollback()
		}
	}()
	var baseVersion, baseRevision uint64
	if err := transaction.QueryRowContext(ctx, previewBaseQuery, command.AccountID).Scan(&baseVersion, &baseRevision); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return storedPreview{}, ErrUnavailable
		}
		return storedPreview{}, ErrUnavailable
	}
	var existingID int64
	var existingStatus string
	var existingExpiry time.Time
	err = transaction.QueryRowContext(ctx, previewExistingQuery, command.AccountID, command.Hash[:]).Scan(&existingID, &existingStatus, &existingExpiry)
	if err == nil && (existingID <= 0 || existingStatus != "previewed") {
		return storedPreview{}, ErrUnavailable
	}
	if err == nil && existingExpiry.After(command.CreatedAt) {
		if err := transaction.Commit(); err != nil {
			return storedPreview{}, ErrUnavailable
		}
		committed = true
		return storedPreview{ID: existingID, AccountID: command.AccountID, ExpiresAt: existingExpiry, Reused: true}, nil
	}
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return storedPreview{}, ErrUnavailable
	}
	start := command.CreatedAt.Truncate(24 * time.Hour)
	end := start.Add(24 * time.Hour)
	var successful int
	if err := transaction.QueryRowContext(ctx, previewQuotaQuery, command.AccountID, start, end).Scan(&successful); err != nil {
		return storedPreview{}, ErrUnavailable
	}
	if successful >= 5 {
		return storedPreview{}, ErrPreviewLimit
	}
	if existingID > 0 {
		result, err := transaction.ExecContext(ctx, previewRefreshQuery, "previewed", baseVersion, baseRevision, definitionJSON, runtimeJSON, nullableRoom(command.RoomSuggestion), command.Source.AppVersion, command.Source.ConfigurationSchemaVersion, reportJSON, command.CreatedAt, command.ExpiresAt, existingID, command.AccountID)
		if err != nil || !exactlyOne(result) {
			return storedPreview{}, ErrUnavailable
		}
	} else {
		result, err := transaction.ExecContext(ctx, previewInsertQuery, command.AccountID, command.Hash[:], "previewed", baseVersion, baseRevision, definitionJSON, runtimeJSON, nullableRoom(command.RoomSuggestion), command.Source.AppVersion, command.Source.ConfigurationSchemaVersion, reportJSON, command.CreatedAt, command.ExpiresAt)
		if err != nil {
			return storedPreview{}, ErrUnavailable
		}
		id, err := result.LastInsertId()
		if err != nil || id <= 0 || !exactlyOne(result) {
			return storedPreview{}, ErrUnavailable
		}
		existingID = id
	}
	if err := transaction.Commit(); err != nil {
		return storedPreview{}, ErrUnavailable
	}
	committed = true
	return storedPreview{ID: existingID, AccountID: command.AccountID, ExpiresAt: command.ExpiresAt}, nil
}
func nullableRoom(value string) any {
	if value == "" {
		return nil
	}
	return value
}
func exactlyOne(result sql.Result) bool {
	if result == nil {
		return false
	}
	rows, err := result.RowsAffected()
	return err == nil && rows == 1
}
