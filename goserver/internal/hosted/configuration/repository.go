package configuration

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

var (
	ErrInvalidInput = errors.New("configuration: invalid input")
	ErrNotFound     = errors.New("configuration: not found")
	ErrUnavailable  = errors.New("configuration: repository unavailable")
)

// Repository owns the durable configuration state for hosted accounts.
type Repository interface {
	LoadActive(context.Context, int64) (Version, State, error)
	CompareAndSwapState(context.Context, UpdateStateCommand) (State, error)
	Activate(context.Context, ActivationCommand) (Version, State, error)
	UpsertRoomSuggestion(context.Context, RoomSuggestion) error
}

// UpdateStateCommand atomically replaces runtime only if its revision is still
// current. Account ID comes from trusted request context, never a payload.
type UpdateStateCommand struct {
	AccountID        int64
	ExpectedRevision uint64
	Runtime          RuntimeState
	UpdatedAt        time.Time
}

// ActivationCommand creates the next immutable configuration version and
// switches its account to it in the same transaction.
type ActivationCommand struct {
	AccountID        int64
	ExpectedVersion  uint64
	ExpectedRevision uint64
	Definition       Definition
	Runtime          RuntimeState
	Source           string
	MigrationJobID   *int64
	At               time.Time
}

// RoomSuggestion is an untrusted room proposal awaiting a separate runtime
// confirmation. It deliberately has no target-room or session fields.
type RoomSuggestion struct {
	AccountID   int64
	RoomID      string
	SuggestedAt time.Time
}

type sqlRepository struct {
	db *sql.DB
}

func NewRepository(db *sql.DB) Repository {
	return &sqlRepository{db: db}
}

func (repository *sqlRepository) LoadActive(ctx context.Context, accountID int64) (Version, State, error) {
	if !repository.ready() || accountID <= 0 {
		return Version{}, State{}, ErrInvalidInput
	}
	const query = "SELECT v.id, v.account_id, v.number, v.definition_json, v.source, v.created_at, s.config_version_id, s.revision, s.runtime_json, s.updated_at FROM streamer_accounts AS a JOIN account_active_config AS active ON active.account_id = a.id JOIN account_config_versions AS v ON v.account_id = active.account_id AND v.id = active.config_version_id JOIN account_runtime_state AS s ON s.account_id = a.id AND s.config_version_id = active.config_version_id WHERE a.id = ?"
	var version Version
	var state State
	var definitionJSON, runtimeJSON []byte
	err := repository.db.QueryRowContext(ctx, query, accountID).Scan(
		&version.ID, &version.AccountID, &version.Number, &definitionJSON, &version.Source, &version.CreatedAt,
		&state.ConfigVersionID, &state.Revision, &runtimeJSON, &state.UpdatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return Version{}, State{}, ErrNotFound
	}
	if err != nil || version.ID <= 0 || version.AccountID != accountID || state.ConfigVersionID != version.ID || state.Revision == 0 || !validSource(version.Source) {
		return Version{}, State{}, ErrUnavailable
	}
	if err := json.Unmarshal(definitionJSON, &version.Definition); err != nil || json.Unmarshal(runtimeJSON, &state.Runtime) != nil {
		return Version{}, State{}, ErrUnavailable
	}
	if _, err := Join(version.Definition, state.Runtime); err != nil {
		return Version{}, State{}, ErrUnavailable
	}
	state.AccountID = accountID
	return version, state, nil
}

func (repository *sqlRepository) CompareAndSwapState(ctx context.Context, command UpdateStateCommand) (State, error) {
	if !repository.ready() || command.AccountID <= 0 || command.ExpectedRevision == 0 || command.UpdatedAt.IsZero() {
		return State{}, ErrInvalidInput
	}
	runtimeJSON, err := marshalRuntime(command.Runtime)
	if err != nil {
		return State{}, ErrInvalidInput
	}
	transaction, err := repository.db.BeginTx(ctx, nil)
	if err != nil {
		return State{}, ErrUnavailable
	}
	committed := false
	defer func() {
		if !committed {
			_ = transaction.Rollback()
		}
	}()

	var state State
	err = transaction.QueryRowContext(ctx, "SELECT config_version_id, revision FROM account_runtime_state WHERE account_id = ? FOR UPDATE", command.AccountID).Scan(&state.ConfigVersionID, &state.Revision)
	if errors.Is(err, sql.ErrNoRows) {
		return State{}, ErrNotFound
	}
	if err != nil || state.ConfigVersionID <= 0 || state.Revision == 0 {
		return State{}, ErrUnavailable
	}
	if state.Revision != command.ExpectedRevision {
		return State{}, ErrRevisionConflict
	}
	nextRevision := state.Revision + 1
	if nextRevision == 0 {
		return State{}, ErrUnavailable
	}
	result, err := transaction.ExecContext(ctx, "UPDATE account_runtime_state SET runtime_json = ?, revision = ?, updated_at = ? WHERE account_id = ? AND revision = ?", runtimeJSON, nextRevision, command.UpdatedAt, command.AccountID, command.ExpectedRevision)
	if err != nil || !oneRow(result) {
		return State{}, ErrUnavailable
	}
	if err := transaction.Commit(); err != nil {
		return State{}, ErrUnavailable
	}
	committed = true
	state.AccountID = command.AccountID
	state.Revision = nextRevision
	state.Runtime = command.Runtime
	state.UpdatedAt = command.UpdatedAt
	return state, nil
}

func (repository *sqlRepository) Activate(ctx context.Context, command ActivationCommand) (Version, State, error) {
	if !repository.ready() || command.AccountID <= 0 || command.At.IsZero() || !validSource(command.Source) || command.MigrationJobID != nil && *command.MigrationJobID <= 0 {
		return Version{}, State{}, ErrInvalidInput
	}
	definitionJSON, err := marshalDefinition(command.Definition)
	if err != nil {
		return Version{}, State{}, ErrInvalidInput
	}
	runtimeJSON, err := marshalRuntime(command.Runtime)
	if err != nil {
		return Version{}, State{}, ErrInvalidInput
	}
	if _, err := Join(command.Definition, command.Runtime); err != nil {
		return Version{}, State{}, ErrInvalidInput
	}

	transaction, err := repository.db.BeginTx(ctx, nil)
	if err != nil {
		return Version{}, State{}, ErrUnavailable
	}
	committed := false
	defer func() {
		if !committed {
			_ = transaction.Rollback()
		}
	}()

	var activeID sql.NullInt64
	var activeNumber uint64
	err = transaction.QueryRowContext(ctx, "SELECT active.config_version_id, COALESCE(v.number, 0) FROM streamer_accounts AS a LEFT JOIN account_active_config AS active ON active.account_id = a.id LEFT JOIN account_config_versions AS v ON v.account_id = active.account_id AND v.id = active.config_version_id WHERE a.id = ? FOR UPDATE", command.AccountID).Scan(&activeID, &activeNumber)
	if errors.Is(err, sql.ErrNoRows) {
		return Version{}, State{}, ErrNotFound
	}
	if err != nil || activeID.Valid && activeID.Int64 <= 0 || !activeID.Valid && activeNumber != 0 {
		return Version{}, State{}, ErrUnavailable
	}
	if activeNumber != command.ExpectedVersion {
		return Version{}, State{}, ErrRevisionConflict
	}

	var currentRevision uint64
	err = transaction.QueryRowContext(ctx, "SELECT revision FROM account_runtime_state WHERE account_id = ? FOR UPDATE", command.AccountID).Scan(&currentRevision)
	if errors.Is(err, sql.ErrNoRows) {
		if activeID.Valid || command.ExpectedRevision != 0 {
			return Version{}, State{}, ErrRevisionConflict
		}
		currentRevision = 0
	} else if err != nil || currentRevision == 0 {
		return Version{}, State{}, ErrUnavailable
	} else if currentRevision != command.ExpectedRevision {
		return Version{}, State{}, ErrRevisionConflict
	}
	if activeID.Valid && command.ExpectedRevision == 0 {
		return Version{}, State{}, ErrRevisionConflict
	}

	var number uint64
	if err := transaction.QueryRowContext(ctx, "SELECT COALESCE(MAX(number), 0) + 1 FROM account_config_versions WHERE account_id = ?", command.AccountID).Scan(&number); err != nil || number == 0 {
		return Version{}, State{}, ErrUnavailable
	}
	result, err := transaction.ExecContext(ctx, "INSERT INTO account_config_versions (account_id, number, definition_json, source, created_at) VALUES (?, ?, ?, ?, ?)", command.AccountID, number, definitionJSON, command.Source, command.At)
	if err != nil {
		return Version{}, State{}, ErrUnavailable
	}
	versionID, err := result.LastInsertId()
	if err != nil || versionID <= 0 || !oneRow(result) {
		return Version{}, State{}, ErrUnavailable
	}
	nextRevision := currentRevision + 1
	if nextRevision == 0 {
		return Version{}, State{}, ErrUnavailable
	}
	result, err = transaction.ExecContext(ctx, "INSERT INTO account_runtime_state (account_id, config_version_id, revision, runtime_json, updated_at) VALUES (?, ?, ?, ?, ?) ON DUPLICATE KEY UPDATE config_version_id = VALUES(config_version_id), revision = VALUES(revision), runtime_json = VALUES(runtime_json), updated_at = VALUES(updated_at)", command.AccountID, versionID, nextRevision, runtimeJSON, command.At)
	if err != nil || !oneOrTwoRows(result) {
		return Version{}, State{}, ErrUnavailable
	}
	result, err = transaction.ExecContext(ctx, "INSERT INTO account_active_config (account_id, config_version_id, updated_at) VALUES (?, ?, ?) ON DUPLICATE KEY UPDATE config_version_id = VALUES(config_version_id), updated_at = VALUES(updated_at)", command.AccountID, versionID, command.At)
	if err != nil || !oneOrTwoRows(result) {
		return Version{}, State{}, ErrUnavailable
	}
	if command.MigrationJobID != nil {
		result, err = transaction.ExecContext(ctx, "UPDATE migration_jobs SET status = 'applied', applied_at = ? WHERE id = ? AND account_id = ? AND status IN ('previewed', 'pending')", command.At, *command.MigrationJobID, command.AccountID)
		if err != nil || !oneRow(result) {
			return Version{}, State{}, ErrUnavailable
		}
	}
	if err := transaction.Commit(); err != nil {
		return Version{}, State{}, ErrUnavailable
	}
	committed = true
	return Version{ID: versionID, AccountID: command.AccountID, Number: number, Definition: command.Definition, Source: command.Source, CreatedAt: command.At}, State{AccountID: command.AccountID, ConfigVersionID: versionID, Revision: nextRevision, Runtime: command.Runtime, UpdatedAt: command.At}, nil
}

func (repository *sqlRepository) UpsertRoomSuggestion(ctx context.Context, suggestion RoomSuggestion) error {
	if !repository.ready() || suggestion.AccountID <= 0 || !validRoomID(suggestion.RoomID) || suggestion.SuggestedAt.IsZero() {
		return ErrInvalidInput
	}
	result, err := repository.db.ExecContext(ctx, "INSERT INTO account_room_suggestions (account_id, room_id, suggested_at) VALUES (?, ?, ?) ON DUPLICATE KEY UPDATE room_id = VALUES(room_id), suggested_at = VALUES(suggested_at)", suggestion.AccountID, suggestion.RoomID, suggestion.SuggestedAt)
	if err != nil || !oneOrTwoRows(result) {
		return ErrUnavailable
	}
	return nil
}

func marshalDefinition(definition Definition) ([]byte, error) {
	return json.Marshal(definition)
}

func marshalRuntime(runtime RuntimeState) ([]byte, error) {
	return json.Marshal(runtime)
}

func validSource(source string) bool {
	return source == "manual" || source == "migration" || source == "rollback"
}

func validRoomID(roomID string) bool {
	if len(roomID) == 0 || len(roomID) > 128 {
		return false
	}
	for _, character := range roomID {
		if character < '0' || character > '9' {
			return false
		}
	}
	return true
}

func oneRow(result sql.Result) bool {
	if result == nil {
		return false
	}
	rows, err := result.RowsAffected()
	return err == nil && rows == 1
}

// MySQL reports one affected row for an insert and two for an ON DUPLICATE
// KEY UPDATE that changes the existing runtime row.
func oneOrTwoRows(result sql.Result) bool {
	if result == nil {
		return false
	}
	rows, err := result.RowsAffected()
	return err == nil && (rows == 1 || rows == 2)
}

func (repository *sqlRepository) ready() bool {
	return repository != nil && repository.db != nil
}
