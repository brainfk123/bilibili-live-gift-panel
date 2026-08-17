package configuration

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"encoding/json"
	"errors"
	"reflect"
	"time"

	"github.com/go-sql-driver/mysql"
)

var (
	ErrInvalidInput = errors.New("configuration: invalid input")
	ErrNotFound     = errors.New("configuration: not found")
	ErrUnavailable  = errors.New("configuration: repository unavailable")
	ErrOwnership    = errors.New("configuration: runtime ownership conflict")
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

// RuntimeEventCommand contains only trusted tenancy/fencing values and
// identity-free gameplay data. Viewer identity and raw room events must never
// cross this repository boundary.
type RuntimeEventCommand struct {
	AccountID        int64
	LiveSessionID    int64
	ConfigVersionID  int64
	OwnerToken       [32]byte
	OwnerEpoch       uint64
	ExpectedRevision uint64
	Runtime          RuntimeState
	AggregateDelta   RuntimeAggregate
	StableEventHash  *[32]byte
	UpdatedAt        time.Time
}

// RuntimeAggregate is the identity-free durable summary of gameplay events.
// Keeping this type at the repository boundary prevents callers from smuggling
// viewer identity or raw room-event data into the aggregate JSON column.
type RuntimeAggregate struct {
	EventCount int     `json:"eventCount"`
	GiftCount  int     `json:"giftCount"`
	GiftCoin   float64 `json:"giftCoin,omitempty"`
}

type RuntimeEventResult struct {
	Revision  uint64
	Duplicate bool
}

type sqlRepository struct {
	db *sql.DB
}

func NewRepository(db *sql.DB) *sqlRepository {
	return &sqlRepository{db: db}
}

// CommitRuntimeEvent atomically validates the captured owner fence and trusted
// session identity, records stable-ID dedupe, replaces identity-free runtime
// state, and updates the identity-free session aggregate.
func (repository *sqlRepository) CommitRuntimeEvent(ctx context.Context, command RuntimeEventCommand) (RuntimeEventResult, error) {
	if !repository.ready() || ctx == nil || command.AccountID <= 0 || command.LiveSessionID <= 0 || command.ConfigVersionID <= 0 || command.OwnerToken == ([32]byte{}) || command.OwnerEpoch == 0 || command.ExpectedRevision == 0 || command.UpdatedAt.IsZero() || command.AggregateDelta.EventCount <= 0 || command.AggregateDelta.GiftCount <= 0 || command.AggregateDelta.GiftCoin < 0 {
		return RuntimeEventResult{}, ErrInvalidInput
	}
	runtimeJSON, err := marshalRuntime(command.Runtime)
	if err != nil {
		return RuntimeEventResult{}, ErrInvalidInput
	}
	command.UpdatedAt = databaseTime(command.UpdatedAt)

	transaction, err := repository.db.BeginTx(ctx, nil)
	if err != nil {
		return RuntimeEventResult{}, ErrUnavailable
	}
	finished := false
	defer func() {
		if !finished {
			_ = transaction.Rollback()
		}
	}()

	var enabled bool
	if err := transaction.QueryRowContext(ctx, "SELECT disabled_at IS NULL FROM streamer_accounts WHERE id = ? FOR UPDATE", command.AccountID).Scan(&enabled); err != nil || !enabled {
		return RuntimeEventResult{}, ErrUnavailable
	}
	cleanup, err := transaction.ExecContext(ctx, "DELETE FROM runtime_event_dedup_receipts WHERE account_id = ? AND expires_at <= UTC_TIMESTAMP(6) ORDER BY expires_at, event_hash LIMIT 100", command.AccountID)
	if err != nil || !atMostRows(cleanup, 100) {
		return RuntimeEventResult{}, ErrUnavailable
	}
	var ownerToken []byte
	var ownerEpoch uint64
	var current bool
	if err := transaction.QueryRowContext(ctx, "SELECT owner_token, fencing_epoch, expires_at > UTC_TIMESTAMP(6) FROM runtime_account_owners WHERE account_id = ? FOR UPDATE", command.AccountID).Scan(&ownerToken, &ownerEpoch, &current); err != nil {
		return RuntimeEventResult{}, ErrUnavailable
	}
	if len(ownerToken) != len(command.OwnerToken) || subtle.ConstantTimeCompare(ownerToken, command.OwnerToken[:]) != 1 || ownerEpoch != command.OwnerEpoch || !current {
		return RuntimeEventResult{}, ErrOwnership
	}

	var configVersionID int64
	var revision uint64
	if err := transaction.QueryRowContext(ctx, "SELECT config_version_id, revision FROM account_runtime_state WHERE account_id = ? FOR UPDATE", command.AccountID).Scan(&configVersionID, &revision); err != nil {
		return RuntimeEventResult{}, ErrUnavailable
	}
	if configVersionID != command.ConfigVersionID {
		return RuntimeEventResult{}, ErrRevisionConflict
	}
	var sessionAccountID int64
	if err := transaction.QueryRowContext(ctx, "SELECT i.account_id FROM runtime_session_identities AS i JOIN live_sessions AS l ON l.id = i.live_session_id AND l.account_id = i.account_id JOIN runtime_active_session_guards AS g ON g.account_id = i.account_id AND g.live_session_id = i.live_session_id WHERE i.live_session_id = ? AND i.account_id = ? AND l.ended_at IS NULL FOR UPDATE", command.LiveSessionID, command.AccountID).Scan(&sessionAccountID); err != nil || sessionAccountID != command.AccountID {
		return RuntimeEventResult{}, ErrUnavailable
	}

	if command.StableEventHash != nil {
		result, err := transaction.ExecContext(ctx, "DELETE FROM runtime_event_dedup_receipts WHERE account_id = ? AND event_hash = ? AND expires_at <= UTC_TIMESTAMP(6)", command.AccountID, command.StableEventHash[:])
		if err != nil || !zeroOrOneRow(result) {
			return RuntimeEventResult{}, ErrUnavailable
		}
		result, err = transaction.ExecContext(ctx, "INSERT INTO runtime_event_dedup_receipts (event_hash, live_session_id, account_id, created_at, expires_at) VALUES (?, ?, ?, UTC_TIMESTAMP(6), TIMESTAMPADD(HOUR, 24, UTC_TIMESTAMP(6)))", command.StableEventHash[:], command.LiveSessionID, command.AccountID)
		if err != nil {
			var mysqlError *mysql.MySQLError
			if !errors.As(err, &mysqlError) || mysqlError.Number != 1062 {
				return RuntimeEventResult{}, ErrUnavailable
			}
			if !scanReusableRuntimeReceipt(transaction.QueryRowContext(ctx, reusableRuntimeReceiptForUpdateQuery, command.AccountID, command.StableEventHash[:])) {
				return RuntimeEventResult{}, ErrUnavailable
			}
			if err := transaction.Commit(); err != nil {
				finished = true
				if repository.verifyReusableRuntimeReceipt(command) {
					return RuntimeEventResult{Revision: revision, Duplicate: true}, nil
				}
				return RuntimeEventResult{}, ErrUnavailable
			}
			finished = true
			return RuntimeEventResult{Revision: revision, Duplicate: true}, nil
		}
		rows, err := result.RowsAffected()
		if err != nil || rows != 1 {
			return RuntimeEventResult{}, ErrUnavailable
		}
	}
	if revision != command.ExpectedRevision {
		return RuntimeEventResult{}, ErrRevisionConflict
	}

	var aggregate RuntimeAggregate
	var storedAggregate []byte
	err = transaction.QueryRowContext(ctx, "SELECT aggregate_json FROM runtime_session_aggregates WHERE live_session_id = ? AND account_id = ? FOR UPDATE", command.LiveSessionID, command.AccountID).Scan(&storedAggregate)
	if err == nil {
		if json.Unmarshal(storedAggregate, &aggregate) != nil || aggregate.EventCount < 0 || aggregate.GiftCount < 0 || aggregate.GiftCoin < 0 {
			return RuntimeEventResult{}, ErrUnavailable
		}
	} else if !errors.Is(err, sql.ErrNoRows) {
		return RuntimeEventResult{}, ErrUnavailable
	}
	nextAggregate := RuntimeAggregate{
		EventCount: aggregate.EventCount + command.AggregateDelta.EventCount,
		GiftCount:  aggregate.GiftCount + command.AggregateDelta.GiftCount,
		GiftCoin:   aggregate.GiftCoin + command.AggregateDelta.GiftCoin,
	}
	if nextAggregate.EventCount < aggregate.EventCount || nextAggregate.GiftCount < aggregate.GiftCount {
		return RuntimeEventResult{}, ErrUnavailable
	}
	aggregateJSON, err := json.Marshal(nextAggregate)
	if err != nil {
		return RuntimeEventResult{}, ErrInvalidInput
	}

	nextRevision := revision + 1
	if nextRevision == 0 {
		return RuntimeEventResult{}, ErrUnavailable
	}
	result, err := transaction.ExecContext(ctx, "UPDATE account_runtime_state SET runtime_json = ?, revision = ?, updated_at = ? WHERE account_id = ? AND config_version_id = ? AND revision = ?", runtimeJSON, nextRevision, command.UpdatedAt, command.AccountID, command.ConfigVersionID, revision)
	if err != nil || !oneRow(result) {
		return RuntimeEventResult{}, ErrUnavailable
	}
	result, err = transaction.ExecContext(ctx, "INSERT INTO runtime_session_aggregates (live_session_id, account_id, aggregate_json, updated_at) VALUES (?, ?, ?, ?) ON DUPLICATE KEY UPDATE aggregate_json = VALUES(aggregate_json), updated_at = VALUES(updated_at)", command.LiveSessionID, command.AccountID, aggregateJSON, command.UpdatedAt)
	if err != nil || !oneOrTwoRows(result) {
		return RuntimeEventResult{}, ErrUnavailable
	}
	if err := transaction.Commit(); err != nil {
		finished = true
		if repository.verifyCommittedRuntimeEvent(command, runtimeJSON, aggregateJSON, nextRevision) {
			return RuntimeEventResult{Revision: nextRevision}, nil
		}
		return RuntimeEventResult{}, ErrUnavailable
	}
	finished = true
	return RuntimeEventResult{Revision: nextRevision}, nil
}

func (repository *sqlRepository) verifyCommittedRuntimeEvent(command RuntimeEventCommand, runtimeJSON, aggregateJSON []byte, revision uint64) bool {
	verificationContext, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	const query = "SELECT s.revision, s.runtime_json, a.aggregate_json, o.owner_token, o.fencing_epoch, o.expires_at > UTC_TIMESTAMP(6) FROM account_runtime_state AS s JOIN runtime_session_aggregates AS a ON a.account_id = s.account_id JOIN runtime_session_identities AS i ON i.account_id = a.account_id AND i.live_session_id = a.live_session_id JOIN runtime_account_owners AS o ON o.account_id = s.account_id WHERE s.account_id = ? AND s.config_version_id = ? AND i.live_session_id = ? AND i.account_id = ?"
	var storedRevision uint64
	var storedRuntime, storedAggregate, storedOwner []byte
	var storedEpoch uint64
	var current bool
	if err := repository.db.QueryRowContext(verificationContext, query, command.AccountID, command.ConfigVersionID, command.LiveSessionID, command.AccountID).Scan(&storedRevision, &storedRuntime, &storedAggregate, &storedOwner, &storedEpoch, &current); err != nil {
		return false
	}
	if storedRevision != revision || !jsonSemanticallyEqual(storedRuntime, runtimeJSON) || !jsonSemanticallyEqual(storedAggregate, aggregateJSON) || len(storedOwner) != len(command.OwnerToken) || subtle.ConstantTimeCompare(storedOwner, command.OwnerToken[:]) != 1 || storedEpoch != command.OwnerEpoch || !current {
		return false
	}
	if command.StableEventHash == nil {
		return true
	}
	return scanCurrentRuntimeReceipt(repository.db.QueryRowContext(verificationContext, runtimeReceiptQuery, command.AccountID, command.StableEventHash[:]), command.LiveSessionID)
}

const runtimeReceiptQuery = "SELECT live_session_id, created_at, expires_at, expires_at > UTC_TIMESTAMP(6), TIMESTAMPDIFF(MICROSECOND, created_at, expires_at) = 86400000000 FROM runtime_event_dedup_receipts WHERE account_id = ? AND event_hash = ?"
const reusableRuntimeReceiptQuery = "SELECT expires_at > UTC_TIMESTAMP(6) FROM runtime_event_dedup_receipts WHERE account_id = ? AND event_hash = ?"
const reusableRuntimeReceiptForUpdateQuery = reusableRuntimeReceiptQuery + " FOR UPDATE"

type runtimeReceiptScanner interface {
	Scan(...any) error
}

func scanCurrentRuntimeReceipt(row runtimeReceiptScanner, liveSessionID int64) bool {
	var storedLiveSessionID int64
	var createdAt, expiresAt time.Time
	var current, exactTTL bool
	if row.Scan(&storedLiveSessionID, &createdAt, &expiresAt, &current, &exactTTL) != nil {
		return false
	}
	return storedLiveSessionID == liveSessionID && !createdAt.IsZero() && expiresAt.After(createdAt) && current && exactTTL
}

func scanReusableRuntimeReceipt(row runtimeReceiptScanner) bool {
	var current bool
	return row.Scan(&current) == nil && current
}

func (repository *sqlRepository) verifyReusableRuntimeReceipt(command RuntimeEventCommand) bool {
	if command.StableEventHash == nil {
		return false
	}
	verificationContext, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	return scanReusableRuntimeReceipt(repository.db.QueryRowContext(verificationContext, reusableRuntimeReceiptQuery, command.AccountID, command.StableEventHash[:]))
}

func jsonSemanticallyEqual(left, right []byte) bool {
	var leftValue, rightValue any
	if json.Unmarshal(left, &leftValue) != nil || json.Unmarshal(right, &rightValue) != nil {
		return false
	}
	return reflect.DeepEqual(leftValue, rightValue)
}

func databaseTime(value time.Time) time.Time {
	if value.IsZero() {
		return value
	}
	return value.UTC().Truncate(time.Microsecond)
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
	if err != nil || !zeroOneOrTwoRows(result) {
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

func zeroOrOneRow(result sql.Result) bool {
	if result == nil {
		return false
	}
	rows, err := result.RowsAffected()
	return err == nil && (rows == 0 || rows == 1)
}

func atMostRows(result sql.Result, maximum int64) bool {
	if result == nil || maximum < 0 {
		return false
	}
	rows, err := result.RowsAffected()
	return err == nil && rows >= 0 && rows <= maximum
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

// MySQL reports zero rows for an idempotent ON DUPLICATE KEY UPDATE where the
// stored room suggestion already has the supplied values.
func zeroOneOrTwoRows(result sql.Result) bool {
	if result == nil {
		return false
	}
	rows, err := result.RowsAffected()
	return err == nil && (rows == 0 || rows == 1 || rows == 2)
}

func (repository *sqlRepository) ready() bool {
	return repository != nil && repository.db != nil
}
