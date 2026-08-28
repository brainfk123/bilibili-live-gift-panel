package runtime

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"sync"
	"time"

	"bilibili-live-gift-panel/internal/hosted/roomsource"
	"github.com/go-sql-driver/mysql"
)

var ErrNoTargetRoom = errors.New("runtime: target room not configured")

type Session struct {
	ID                 int64
	BroadcastSessionID int64
	AccountID          int64
	RoomID             string
	ConfigVersionID    int64
	StartedAt          time.Time
}

type StartSessionCommand struct {
	Owner              OwnerFence
	AccountID          int64
	RoomID             string
	BroadcastSessionID int64
	ConfigVersionID    int64
	StartedAt          time.Time
	Reconcile          bool
}

type EndSessionCommand struct {
	Owner     OwnerFence
	AccountID int64
	SessionID int64
	EndedAt   time.Time
}

type ReconcileSessionCommand struct {
	LostOwner OwnerFence
	AccountID int64
	SessionID int64
	EndedAt   time.Time
}

type PersistTargetRoomCommand struct {
	Owner     OwnerFence
	RoomID    string
	UpdatedAt time.Time
}

type PersistDisabledTargetRoomCommand struct {
	AccountID int64
	RoomID    string
	UpdatedAt time.Time
}

type RoomMutationID [16]byte

type RoomMutationPhase string

const (
	RoomMutationTargetPersisted  RoomMutationPhase = "target_persisted"
	RoomMutationReferencesSynced RoomMutationPhase = "references_synced"
	RoomMutationAudited          RoomMutationPhase = "audited"
)

// RoomMutationResult is the immutable canonical pair captured by the
// transaction that persisted a room mutation.
type RoomMutationResult struct {
	MutationID       RoomMutationID
	AccountID        int64
	DesiredCanonical string
	OldCanonical     string
	NewCanonical     string
	Phase            RoomMutationPhase
}

type AggregateCommand struct {
	Owner         OwnerFence
	AccountID     int64
	SessionID     int64
	AggregateJSON json.RawMessage
	UpdatedAt     time.Time
}

type StableEventCommand struct {
	Owner     OwnerFence
	AccountID int64
	SessionID int64
	EventHash [32]byte
	CreatedAt time.Time
	ExpiresAt time.Time
}

type SessionRepository struct {
	db                  *sql.DB
	verificationTimeout time.Duration
	newMutationID       func() (RoomMutationID, error)
}

func NewSessionRepository(database *sql.DB) *SessionRepository {
	return &SessionRepository{db: database, verificationTimeout: 2 * time.Second, newMutationID: randomRoomMutationID}
}

func randomRoomMutationID() (RoomMutationID, error) {
	var mutationID RoomMutationID
	_, err := rand.Read(mutationID[:])
	return mutationID, err
}

func (repository *SessionRepository) AccountEnabled(ctx context.Context, accountID int64) (bool, error) {
	if !repository.ready() || ctx == nil || accountID <= 0 {
		return false, ErrInvalidInput
	}
	var enabled bool
	err := repository.db.QueryRowContext(ctx, "SELECT disabled_at IS NULL FROM streamer_accounts WHERE id = ?", accountID).Scan(&enabled)
	if errors.Is(err, sql.ErrNoRows) {
		return false, ErrAccountNotFound
	}
	if err != nil {
		return false, ErrUnavailable
	}
	return enabled, nil
}

func (repository *SessionRepository) TargetRoom(ctx context.Context, accountID int64) (string, error) {
	if !repository.ready() || ctx == nil || accountID <= 0 {
		return "", ErrInvalidInput
	}
	var roomID string
	err := repository.db.QueryRowContext(ctx, "SELECT room_id FROM account_runtime_rooms WHERE account_id = ?", accountID).Scan(&roomID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", ErrNoTargetRoom
	}
	if err != nil || !validRoomID(roomID) {
		return "", ErrUnavailable
	}
	return roomID, nil
}

// OpenBroadcastSession returns the durable, still-open business broadcast for
// a room. It is used only after a roomwatcher transition has committed; the
// subsequent StartSession transaction rechecks the exact ID before linking an
// account execution to it.
func (repository *SessionRepository) OpenBroadcastSession(ctx context.Context, roomID string) (int64, error) {
	if !repository.ready() || ctx == nil || !validRoomID(roomID) {
		return 0, ErrInvalidInput
	}
	var broadcastSessionID int64
	err := repository.db.QueryRowContext(ctx, "SELECT id FROM broadcast_sessions WHERE room_id = ? AND ended_at IS NULL", roomID).Scan(&broadcastSessionID)
	if err != nil || broadcastSessionID <= 0 {
		return 0, ErrUnavailable
	}
	return broadcastSessionID, nil
}

func (repository *SessionRepository) PersistTargetRoom(ctx context.Context, command PersistTargetRoomCommand) error {
	_, err := repository.MutateTargetRoom(ctx, command)
	return err
}

func (repository *SessionRepository) MutateTargetRoom(ctx context.Context, command PersistTargetRoomCommand) (RoomMutationResult, error) {
	if !repository.ready() || ctx == nil || !validOwnerFence(command.Owner) || !validRoomID(command.RoomID) || command.UpdatedAt.IsZero() {
		return RoomMutationResult{}, ErrInvalidInput
	}
	command.UpdatedAt = normalizeDatabaseTime(command.UpdatedAt)
	transaction, err := repository.db.BeginTx(ctx, nil)
	if err != nil {
		return RoomMutationResult{}, ErrUnavailable
	}
	finished := false
	defer func() {
		if !finished {
			_ = transaction.Rollback()
		}
	}()
	if err := lockEnabledOwnerAccount(ctx, transaction, command.Owner.AccountID); err != nil {
		return RoomMutationResult{}, err
	}
	if err := validateOwnerFence(ctx, transaction, command.Owner); err != nil {
		return RoomMutationResult{}, err
	}
	receipt, err := repository.persistRoomMutation(ctx, transaction, command.Owner.AccountID, command.RoomID, command.UpdatedAt)
	if err != nil {
		return RoomMutationResult{}, err
	}
	if err := transaction.Commit(); err != nil {
		finished = true
		if verified, ok := repository.verifyRoomMutation(receipt.MutationID); ok {
			return verified, nil
		}
		return RoomMutationResult{}, ErrUnavailable
	}
	finished = true
	return receipt, nil
}

// PersistDisabledTargetRoom is the non-owning administrative path. The
// account row lock serializes the exact old/new result and rejects a target
// overwrite until every durable active-session guard has been cleaned up.
func (repository *SessionRepository) PersistDisabledTargetRoom(ctx context.Context, command PersistDisabledTargetRoomCommand) (RoomMutationResult, error) {
	if !repository.ready() || ctx == nil || command.AccountID <= 0 || !validRoomID(command.RoomID) || command.UpdatedAt.IsZero() {
		return RoomMutationResult{}, ErrInvalidInput
	}
	command.UpdatedAt = normalizeDatabaseTime(command.UpdatedAt)
	transaction, err := repository.db.BeginTx(ctx, nil)
	if err != nil {
		return RoomMutationResult{}, ErrUnavailable
	}
	finished := false
	defer func() {
		if !finished {
			_ = transaction.Rollback()
		}
	}()
	var enabled bool
	err = transaction.QueryRowContext(ctx, "SELECT disabled_at IS NULL FROM streamer_accounts WHERE id = ? FOR UPDATE", command.AccountID).Scan(&enabled)
	if errors.Is(err, sql.ErrNoRows) {
		return RoomMutationResult{}, ErrAccountNotFound
	}
	if err != nil || enabled {
		return RoomMutationResult{}, ErrUnavailable
	}
	var activeGuards int
	if err := transaction.QueryRowContext(ctx, "SELECT COUNT(*) FROM runtime_active_session_guards WHERE account_id = ?", command.AccountID).Scan(&activeGuards); err != nil || activeGuards != 0 {
		return RoomMutationResult{}, ErrUnavailable
	}
	receipt, err := repository.persistRoomMutation(ctx, transaction, command.AccountID, command.RoomID, command.UpdatedAt)
	if err != nil {
		return RoomMutationResult{}, err
	}
	if err := transaction.Commit(); err != nil {
		finished = true
		if verified, ok := repository.verifyRoomMutation(receipt.MutationID); ok {
			return verified, nil
		}
		return RoomMutationResult{}, ErrUnavailable
	}
	finished = true
	return receipt, nil
}

func (repository *SessionRepository) persistRoomMutation(ctx context.Context, transaction *sql.Tx, accountID int64, roomID string, updatedAt time.Time) (RoomMutationResult, error) {
	current, err := targetRoomUnderAccountLock(ctx, transaction, accountID)
	if err != nil {
		return RoomMutationResult{}, err
	}
	latest, found, err := loadLatestRoomMutation(ctx, transaction, accountID)
	if err != nil {
		return RoomMutationResult{}, err
	}
	if found && current == roomID && latest.NewCanonical == roomID {
		return latest, nil
	}
	if found && latest.Phase != RoomMutationAudited {
		return RoomMutationResult{}, ErrUnavailable
	}
	newMutationID := repository.newMutationID
	if newMutationID == nil {
		newMutationID = randomRoomMutationID
	}
	mutationID, err := newMutationID()
	if err != nil || mutationID == (RoomMutationID{}) {
		return RoomMutationResult{}, ErrUnavailable
	}
	result, err := transaction.ExecContext(ctx, "INSERT INTO account_runtime_rooms (account_id, room_id, updated_at) VALUES (?, ?, ?) ON DUPLICATE KEY UPDATE room_id = VALUES(room_id), updated_at = VALUES(updated_at)", accountID, roomID, updatedAt)
	if err != nil || !atLeastOneAffected(result) {
		return RoomMutationResult{}, ErrUnavailable
	}
	var old any
	if current != "" {
		old = current
	}
	result, err = transaction.ExecContext(ctx, "INSERT INTO room_mutation_receipts (mutation_id, account_id, desired_room_id, old_room_id, new_room_id, phase, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)", mutationID[:], accountID, roomID, old, roomID, string(RoomMutationTargetPersisted), updatedAt, updatedAt)
	if err != nil || !oneAffected(result) {
		return RoomMutationResult{}, ErrUnavailable
	}
	return RoomMutationResult{MutationID: mutationID, AccountID: accountID, DesiredCanonical: roomID, OldCanonical: current, NewCanonical: roomID, Phase: RoomMutationTargetPersisted}, nil
}

func loadLatestRoomMutation(ctx context.Context, queryer queryRower, accountID int64) (RoomMutationResult, bool, error) {
	result, err := scanRoomMutation(queryer.QueryRowContext(ctx, "SELECT mutation_id, account_id, desired_room_id, old_room_id, new_room_id, phase FROM room_mutation_receipts WHERE account_id = ? ORDER BY created_at DESC, id DESC LIMIT 1 FOR UPDATE", accountID))
	if errors.Is(err, sql.ErrNoRows) {
		return RoomMutationResult{}, false, nil
	}
	if err != nil {
		return RoomMutationResult{}, false, ErrUnavailable
	}
	return result, true, nil
}

func scanRoomMutation(row *sql.Row) (RoomMutationResult, error) {
	var result RoomMutationResult
	var mutationID []byte
	var old sql.NullString
	var phase string
	if err := row.Scan(&mutationID, &result.AccountID, &result.DesiredCanonical, &old, &result.NewCanonical, &phase); err != nil {
		return RoomMutationResult{}, err
	}
	if len(mutationID) != len(result.MutationID) {
		return RoomMutationResult{}, ErrUnavailable
	}
	copy(result.MutationID[:], mutationID)
	if old.Valid {
		result.OldCanonical = old.String
	}
	result.Phase = RoomMutationPhase(phase)
	if result.AccountID <= 0 || !validRoomID(result.DesiredCanonical) || result.OldCanonical != "" && !validRoomID(result.OldCanonical) || !validRoomID(result.NewCanonical) || roomMutationPhaseRank(result.Phase) == 0 {
		return RoomMutationResult{}, ErrUnavailable
	}
	return result, nil
}

func (repository *SessionRepository) roomMutation(ctx context.Context, queryer queryRower, mutationID RoomMutationID, forUpdate bool) (RoomMutationResult, error) {
	query := "SELECT mutation_id, account_id, desired_room_id, old_room_id, new_room_id, phase FROM room_mutation_receipts WHERE mutation_id = ?"
	if forUpdate {
		query += " FOR UPDATE"
	}
	result, err := scanRoomMutation(queryer.QueryRowContext(ctx, query, mutationID[:]))
	if err != nil {
		return RoomMutationResult{}, ErrUnavailable
	}
	return result, nil
}

func (repository *SessionRepository) verifyRoomMutation(mutationID RoomMutationID) (RoomMutationResult, bool) {
	ctx, cancel := repository.verificationContext()
	defer cancel()
	result, err := repository.roomMutation(ctx, repository.db, mutationID, false)
	return result, err == nil
}

func (repository *SessionRepository) AdvanceRoomMutation(ctx context.Context, mutationID RoomMutationID, expected, next RoomMutationPhase, updatedAt time.Time) (RoomMutationResult, error) {
	if !repository.ready() || ctx == nil || mutationID == (RoomMutationID{}) || roomMutationPhaseRank(expected) == 0 || roomMutationPhaseRank(next) != roomMutationPhaseRank(expected)+1 || updatedAt.IsZero() {
		return RoomMutationResult{}, ErrInvalidInput
	}
	updatedAt = normalizeDatabaseTime(updatedAt)
	transaction, err := repository.db.BeginTx(ctx, nil)
	if err != nil {
		return RoomMutationResult{}, ErrUnavailable
	}
	finished := false
	defer func() {
		if !finished {
			_ = transaction.Rollback()
		}
	}()
	receipt, err := repository.roomMutation(ctx, transaction, mutationID, true)
	if err != nil {
		return RoomMutationResult{}, err
	}
	if roomMutationPhaseRank(receipt.Phase) >= roomMutationPhaseRank(next) {
		if err := transaction.Commit(); err != nil {
			finished = true
			if verified, ok := repository.verifyRoomMutation(mutationID); ok && roomMutationPhaseRank(verified.Phase) >= roomMutationPhaseRank(next) {
				return verified, nil
			}
			return RoomMutationResult{}, ErrUnavailable
		}
		finished = true
		return receipt, nil
	}
	if receipt.Phase != expected {
		return RoomMutationResult{}, ErrUnavailable
	}
	result, err := transaction.ExecContext(ctx, "UPDATE room_mutation_receipts SET phase = ?, updated_at = ? WHERE mutation_id = ? AND phase = ?", string(next), updatedAt, mutationID[:], string(expected))
	if err != nil || !oneAffected(result) {
		return RoomMutationResult{}, ErrUnavailable
	}
	receipt.Phase = next
	if err := transaction.Commit(); err != nil {
		finished = true
		if verified, ok := repository.verifyRoomMutation(mutationID); ok && roomMutationPhaseRank(verified.Phase) >= roomMutationPhaseRank(next) {
			return verified, nil
		}
		return RoomMutationResult{}, ErrUnavailable
	}
	finished = true
	return receipt, nil
}

func roomMutationPhaseRank(phase RoomMutationPhase) int {
	switch phase {
	case RoomMutationTargetPersisted:
		return 1
	case RoomMutationReferencesSynced:
		return 2
	case RoomMutationAudited:
		return 3
	default:
		return 0
	}
}

func targetRoomUnderAccountLock(ctx context.Context, transaction *sql.Tx, accountID int64) (string, error) {
	var roomID string
	err := transaction.QueryRowContext(ctx, "SELECT room_id FROM account_runtime_rooms WHERE account_id = ?", accountID).Scan(&roomID)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil || !validRoomID(roomID) {
		return "", ErrUnavailable
	}
	return roomID, nil
}

func (repository *SessionRepository) StartSession(ctx context.Context, command StartSessionCommand) (Session, error) {
	if !repository.ready() || ctx == nil || !validOwnerFence(command.Owner) || command.Owner.AccountID != command.AccountID || command.BroadcastSessionID < 0 || command.ConfigVersionID <= 0 || !validRoomID(command.RoomID) || command.StartedAt.IsZero() {
		return Session{}, ErrInvalidInput
	}
	command.StartedAt = normalizeDatabaseTime(command.StartedAt)
	transaction, err := repository.db.BeginTx(ctx, nil)
	if err != nil {
		return Session{}, ErrUnavailable
	}
	finished := false
	defer func() {
		if !finished {
			_ = transaction.Rollback()
		}
	}()
	if err := lockEnabledOwnerAccount(ctx, transaction, command.AccountID); err != nil {
		return Session{}, err
	}
	if err := validateOwnerFence(ctx, transaction, command.Owner); err != nil {
		return Session{}, err
	}
	if command.Reconcile {
		if err := reconcileOpenSession(ctx, transaction, command.AccountID, command.StartedAt); err != nil {
			return Session{}, err
		}
	}
	insert := "INSERT INTO live_sessions (account_id, room_id, config_version_id, started_at) SELECT a.id, ?, v.id, ? FROM streamer_accounts AS a JOIN account_config_versions AS v ON v.account_id = a.id AND v.id = ? WHERE a.id = ? AND a.disabled_at IS NULL"
	arguments := []any{command.RoomID, command.StartedAt, command.ConfigVersionID, command.AccountID}
	if command.BroadcastSessionID > 0 {
		insert = "INSERT INTO live_sessions (broadcast_session_id, account_id, room_id, config_version_id, started_at) SELECT b.id, a.id, ?, v.id, ? FROM streamer_accounts AS a JOIN account_config_versions AS v ON v.account_id = a.id AND v.id = ? JOIN broadcast_sessions AS b ON b.id = ? AND b.room_id = ? AND b.ended_at IS NULL WHERE a.id = ? AND a.disabled_at IS NULL"
		arguments = []any{command.RoomID, command.StartedAt, command.ConfigVersionID, command.BroadcastSessionID, command.RoomID, command.AccountID}
	}
	result, err := transaction.ExecContext(ctx, insert, arguments...)
	if err != nil || !oneAffected(result) {
		return Session{}, ErrUnavailable
	}
	sessionID, err := result.LastInsertId()
	if err != nil || sessionID <= 0 {
		return Session{}, ErrUnavailable
	}
	result, err = transaction.ExecContext(ctx, "INSERT INTO runtime_session_identities (live_session_id, account_id) SELECT id, account_id FROM live_sessions WHERE id = ? AND account_id = ?", sessionID, command.AccountID)
	if err != nil || !oneAffected(result) {
		return Session{}, ErrUnavailable
	}
	result, err = transaction.ExecContext(ctx, "INSERT INTO runtime_active_session_guards (account_id, live_session_id) SELECT account_id, id FROM live_sessions WHERE id = ? AND account_id = ? AND ended_at IS NULL", sessionID, command.AccountID)
	if err != nil || !oneAffected(result) {
		return Session{}, ErrUnavailable
	}
	session := Session{ID: sessionID, BroadcastSessionID: command.BroadcastSessionID, AccountID: command.AccountID, RoomID: command.RoomID, ConfigVersionID: command.ConfigVersionID, StartedAt: command.StartedAt}
	if err := transaction.Commit(); err != nil {
		finished = true
		if repository.verifyStartedSession(session, command.Owner) {
			return session, nil
		}
		return Session{}, ErrUnavailable
	}
	finished = true
	return session, nil
}

func (repository *SessionRepository) EndSession(ctx context.Context, command EndSessionCommand) error {
	if !repository.ready() || ctx == nil || !validOwnerFence(command.Owner) || command.Owner.AccountID != command.AccountID || command.SessionID <= 0 || command.EndedAt.IsZero() {
		return ErrInvalidInput
	}
	command.EndedAt = normalizeDatabaseTime(command.EndedAt)
	transaction, err := repository.db.BeginTx(ctx, nil)
	if err != nil {
		return ErrUnavailable
	}
	finished := false
	defer func() {
		if !finished {
			_ = transaction.Rollback()
		}
	}()
	var lockedAccountID int64
	if err := transaction.QueryRowContext(ctx, "SELECT id FROM streamer_accounts WHERE id = ? FOR UPDATE", command.AccountID).Scan(&lockedAccountID); err != nil || lockedAccountID != command.AccountID {
		return ErrUnavailable
	}
	if err := validateOwnerFence(ctx, transaction, command.Owner); err != nil {
		return err
	}
	result, err := transaction.ExecContext(ctx, "DELETE FROM runtime_active_session_guards WHERE account_id = ? AND live_session_id = ?", command.AccountID, command.SessionID)
	if err != nil {
		return ErrUnavailable
	}
	if !oneAffected(result) {
		if repository.verifyEndedSession(ctx, transaction, command) {
			if err := transaction.Rollback(); err != nil {
				return ErrUnavailable
			}
			finished = true
			return nil
		}
		return ErrUnavailable
	}
	result, err = transaction.ExecContext(ctx, "UPDATE live_sessions SET ended_at = ? WHERE id = ? AND account_id = ? AND ended_at IS NULL", command.EndedAt, command.SessionID, command.AccountID)
	if err != nil || !oneAffected(result) {
		return ErrUnavailable
	}
	if err := transaction.Commit(); err != nil {
		finished = true
		if repository.verifyEndedSessionAfterCommit(command) {
			return nil
		}
		return ErrUnavailable
	}
	finished = true
	return nil
}

// ReconcileSession closes one exact execution after LostOwner has been
// rejected by ownership fencing. It is intentionally narrower than the normal
// owner-scoped EndSession path: the transaction locks the account owner row,
// refuses to act while the lost fence is still active, and binds every session
// mutation to the same account/session pair.
func (repository *SessionRepository) ReconcileSession(ctx context.Context, command ReconcileSessionCommand) error {
	if !repository.ready() || ctx == nil || !validOwnerFence(command.LostOwner) || command.LostOwner.AccountID != command.AccountID || command.SessionID <= 0 || command.EndedAt.IsZero() {
		return ErrInvalidInput
	}
	command.EndedAt = normalizeDatabaseTime(command.EndedAt)
	transaction, err := repository.db.BeginTx(ctx, nil)
	if err != nil {
		return ErrUnavailable
	}
	finished := false
	defer func() {
		if !finished {
			_ = transaction.Rollback()
		}
	}()
	if err := lockOwnerAccount(ctx, transaction, command.AccountID); err != nil {
		return err
	}
	var lostOwnerActive bool
	err = transaction.QueryRowContext(ctx, "SELECT owner_token = ? AND fencing_epoch = ? AND expires_at > UTC_TIMESTAMP(6) FROM runtime_account_owners WHERE account_id = ? FOR UPDATE", command.LostOwner.Token[:], command.LostOwner.Epoch, command.AccountID).Scan(&lostOwnerActive)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return ErrUnavailable
	}
	if lostOwnerActive {
		return ErrOwnershipConflict
	}
	var endedAt sql.NullTime
	if err := transaction.QueryRowContext(ctx, "SELECT ended_at FROM live_sessions WHERE id = ? AND account_id = ? FOR UPDATE", command.SessionID, command.AccountID).Scan(&endedAt); err != nil {
		return ErrUnavailable
	}
	result, err := transaction.ExecContext(ctx, "DELETE FROM runtime_active_session_guards WHERE account_id = ? AND live_session_id = ?", command.AccountID, command.SessionID)
	if err != nil {
		return ErrUnavailable
	}
	guardRows, err := result.RowsAffected()
	if err != nil || guardRows < 0 || guardRows > 1 || !endedAt.Valid && guardRows != 1 {
		return ErrUnavailable
	}
	if !endedAt.Valid {
		result, err = transaction.ExecContext(ctx, "UPDATE live_sessions SET ended_at = ? WHERE id = ? AND account_id = ? AND ended_at IS NULL", command.EndedAt, command.SessionID, command.AccountID)
		if err != nil || !oneAffected(result) {
			return ErrUnavailable
		}
	}
	if err := transaction.Commit(); err != nil {
		finished = true
		if repository.verifyReconciledSession(command) {
			return nil
		}
		return ErrUnavailable
	}
	finished = true
	return nil
}

type queryRower interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func reconcileOpenSession(ctx context.Context, transaction *sql.Tx, accountID int64, endedAt time.Time) error {
	rows, err := transaction.QueryContext(ctx, "SELECT id FROM live_sessions WHERE account_id = ? AND ended_at IS NULL ORDER BY id LIMIT 2 FOR UPDATE", accountID)
	if err != nil {
		return ErrUnavailable
	}
	var openSessionIDs []int64
	for rows.Next() {
		var sessionID int64
		if err := rows.Scan(&sessionID); err != nil || sessionID <= 0 {
			_ = rows.Close()
			return ErrUnavailable
		}
		openSessionIDs = append(openSessionIDs, sessionID)
	}
	if err := rows.Close(); err != nil || len(openSessionIDs) > 1 {
		return ErrUnavailable
	}
	if len(openSessionIDs) == 0 {
		return nil
	}
	sessionID := openSessionIDs[0]
	result, err := transaction.ExecContext(ctx, "DELETE FROM runtime_active_session_guards WHERE account_id = ? AND live_session_id = ?", accountID, sessionID)
	if err != nil || !oneAffected(result) {
		return ErrUnavailable
	}
	result, err = transaction.ExecContext(ctx, "UPDATE live_sessions SET ended_at = ? WHERE id = ? AND account_id = ? AND ended_at IS NULL", endedAt, sessionID, accountID)
	if err != nil || !oneAffected(result) {
		return ErrUnavailable
	}
	return nil
}

func (repository *SessionRepository) verifyStartedSession(expected Session, owner OwnerFence) bool {
	ctx, cancel := repository.verificationContext()
	defer cancel()
	var actual Session
	var guardSessionID int64
	err := repository.db.QueryRowContext(ctx, "SELECT s.id, s.account_id, s.room_id, s.config_version_id, s.started_at, g.live_session_id FROM live_sessions AS s JOIN runtime_active_session_guards AS g ON g.account_id = s.account_id AND g.live_session_id = s.id JOIN runtime_account_owners AS o ON o.account_id = s.account_id WHERE s.id = ? AND s.account_id = ? AND s.room_id = ? AND s.config_version_id = ? AND s.started_at = ? AND s.ended_at IS NULL AND o.owner_token = ? AND o.fencing_epoch = ? AND o.expires_at > UTC_TIMESTAMP(6)", expected.ID, expected.AccountID, expected.RoomID, expected.ConfigVersionID, expected.StartedAt, owner.Token[:], owner.Epoch).
		Scan(&actual.ID, &actual.AccountID, &actual.RoomID, &actual.ConfigVersionID, &actual.StartedAt, &guardSessionID)
	return err == nil && actual.ID == expected.ID && actual.AccountID == expected.AccountID && actual.RoomID == expected.RoomID && actual.ConfigVersionID == expected.ConfigVersionID && actual.StartedAt.Equal(expected.StartedAt) && guardSessionID == expected.ID
}

func (repository *SessionRepository) verifyEndedSession(ctx context.Context, queryer queryRower, command EndSessionCommand) bool {
	var endedAt time.Time
	var guardAbsent, ownerActive bool
	err := queryer.QueryRowContext(ctx, "SELECT ended_at, NOT EXISTS (SELECT 1 FROM runtime_active_session_guards WHERE account_id = ? AND live_session_id = ?), EXISTS (SELECT 1 FROM runtime_account_owners WHERE account_id = ? AND owner_token = ? AND fencing_epoch = ? AND expires_at > UTC_TIMESTAMP(6)) FROM live_sessions WHERE id = ? AND account_id = ? AND ended_at = ?", command.AccountID, command.SessionID, command.AccountID, command.Owner.Token[:], command.Owner.Epoch, command.SessionID, command.AccountID, command.EndedAt).Scan(&endedAt, &guardAbsent, &ownerActive)
	return err == nil && endedAt.Equal(command.EndedAt) && guardAbsent && ownerActive
}

func (repository *SessionRepository) verifyEndedSessionAfterCommit(command EndSessionCommand) bool {
	ctx, cancel := repository.verificationContext()
	defer cancel()
	return repository.verifyEndedSession(ctx, repository.db, command)
}

func (repository *SessionRepository) verifyReconciledSession(command ReconcileSessionCommand) bool {
	ctx, cancel := repository.verificationContext()
	defer cancel()
	var endedAt time.Time
	var guardAbsent bool
	err := repository.db.QueryRowContext(ctx, "SELECT ended_at, NOT EXISTS (SELECT 1 FROM runtime_active_session_guards WHERE account_id = ? AND live_session_id = ?) FROM live_sessions WHERE id = ? AND account_id = ? AND ended_at IS NOT NULL", command.AccountID, command.SessionID, command.SessionID, command.AccountID).Scan(&endedAt, &guardAbsent)
	return err == nil && !endedAt.IsZero() && guardAbsent
}

func (repository *SessionRepository) verificationContext() (context.Context, context.CancelFunc) {
	timeout := repository.verificationTimeout
	if timeout <= 0 {
		timeout = 2 * time.Second
	}
	return context.WithTimeout(context.Background(), timeout)
}

func (repository *SessionRepository) PendingMigration(ctx context.Context, accountID int64) (int64, bool, error) {
	if !repository.ready() || ctx == nil || accountID <= 0 {
		return 0, false, ErrInvalidInput
	}
	var jobID int64
	err := repository.db.QueryRowContext(ctx, "SELECT id FROM migration_jobs WHERE account_id = ? AND status = 'pending' ORDER BY id LIMIT 1", accountID).Scan(&jobID)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil || jobID <= 0 {
		return 0, false, ErrUnavailable
	}
	return jobID, true, nil
}

func (repository *SessionRepository) WriteAggregate(ctx context.Context, command AggregateCommand) error {
	if !repository.ready() || ctx == nil || !validOwnerFence(command.Owner) || command.Owner.AccountID != command.AccountID || command.SessionID <= 0 || command.UpdatedAt.IsZero() || len(command.AggregateJSON) == 0 || !json.Valid(command.AggregateJSON) {
		return ErrInvalidInput
	}
	transaction, err := repository.db.BeginTx(ctx, nil)
	if err != nil {
		return ErrUnavailable
	}
	committed := false
	defer func() {
		if !committed {
			_ = transaction.Rollback()
		}
	}()
	if err := lockOwnerAccount(ctx, transaction, command.AccountID); err != nil {
		return err
	}
	if err := validateOwnerFence(ctx, transaction, command.Owner); err != nil {
		return err
	}
	result, err := transaction.ExecContext(ctx, "INSERT INTO runtime_session_aggregates (live_session_id, account_id, aggregate_json, updated_at) SELECT live_session_id, account_id, ?, ? FROM runtime_session_identities WHERE live_session_id = ? AND account_id = ? ON DUPLICATE KEY UPDATE aggregate_json = VALUES(aggregate_json), updated_at = VALUES(updated_at)", command.AggregateJSON, command.UpdatedAt, command.SessionID, command.AccountID)
	if err != nil || !atLeastOneAffected(result) {
		return ErrUnavailable
	}
	if err := transaction.Commit(); err != nil {
		return ErrUnavailable
	}
	committed = true
	return nil
}

func (repository *SessionRepository) RecordStableEvent(ctx context.Context, command StableEventCommand) (bool, error) {
	if !repository.ready() || ctx == nil || !validOwnerFence(command.Owner) || command.Owner.AccountID != command.AccountID || command.SessionID <= 0 || command.EventHash == ([32]byte{}) || command.CreatedAt.IsZero() || !command.ExpiresAt.After(command.CreatedAt) {
		return false, ErrInvalidInput
	}
	transaction, err := repository.db.BeginTx(ctx, nil)
	if err != nil {
		return false, ErrUnavailable
	}
	committed := false
	defer func() {
		if !committed {
			_ = transaction.Rollback()
		}
	}()
	if err := lockOwnerAccount(ctx, transaction, command.AccountID); err != nil {
		return false, err
	}
	if err := validateOwnerFence(ctx, transaction, command.Owner); err != nil {
		return false, err
	}
	result, err := transaction.ExecContext(ctx, "INSERT INTO runtime_event_dedup_receipts (account_id, live_session_id, event_hash, created_at, expires_at) SELECT account_id, live_session_id, ?, ?, ? FROM runtime_session_identities WHERE live_session_id = ? AND account_id = ?", command.EventHash[:], command.CreatedAt, command.ExpiresAt, command.SessionID, command.AccountID)
	if err != nil {
		var mysqlError *mysql.MySQLError
		if errors.As(err, &mysqlError) && mysqlError.Number == 1062 {
			return false, nil
		}
		return false, ErrUnavailable
	}
	if !oneAffected(result) {
		return false, ErrUnavailable
	}
	if err := transaction.Commit(); err != nil {
		return false, ErrUnavailable
	}
	committed = true
	return true, nil
}

func (repository *SessionRepository) ready() bool { return repository != nil && repository.db != nil }

func validateOwnerFence(ctx context.Context, transaction *sql.Tx, fence OwnerFence) error {
	var active bool
	err := transaction.QueryRowContext(ctx, "SELECT expires_at > UTC_TIMESTAMP(6) FROM runtime_account_owners WHERE account_id = ? AND owner_token = ? AND fencing_epoch = ? FOR UPDATE", fence.AccountID, fence.Token[:], fence.Epoch).Scan(&active)
	if errors.Is(err, sql.ErrNoRows) || err == nil && !active {
		return ErrOwnershipConflict
	}
	if err != nil {
		return ErrUnavailable
	}
	return nil
}

func lockOwnerAccount(ctx context.Context, transaction *sql.Tx, accountID int64) error {
	var lockedAccountID int64
	if err := transaction.QueryRowContext(ctx, "SELECT id FROM streamer_accounts WHERE id = ? FOR UPDATE", accountID).Scan(&lockedAccountID); err != nil || lockedAccountID != accountID {
		return ErrUnavailable
	}
	return nil
}

func normalizeDatabaseTime(value time.Time) time.Time {
	return value.UTC().Truncate(time.Microsecond)
}

func oneAffected(result sql.Result) bool {
	if result == nil {
		return false
	}
	rows, err := result.RowsAffected()
	return err == nil && rows == 1
}

func atLeastOneAffected(result sql.Result) bool {
	if result == nil {
		return false
	}
	rows, err := result.RowsAffected()
	return err == nil && rows >= 1
}

func (manager *Manager) startPersisted(ctx context.Context, account *accountRuntime) error {
	roomID, err := manager.dependencies.Sessions.TargetRoom(ctx, account.accountID)
	if err != nil {
		if err == ErrNoTargetRoom {
			return nil
		}
		return ErrUnavailable
	}
	return manager.startRoom(ctx, account, roomID, false, true, 0)
}

func (manager *Manager) startRoom(ctx context.Context, account *accountRuntime, roomID string, persist, activate bool, broadcastSessionID int64) error {
	account.mu.Lock()
	owner := account.owner
	reconcile := account.reconcile
	expectedCurrent := account.current
	account.mu.Unlock()
	if !validOwnerFence(owner) {
		return ErrOwnershipConflict
	}
	if persist {
		if err := manager.dependencies.Sessions.PersistTargetRoom(ctx, PersistTargetRoomCommand{Owner: owner, RoomID: roomID, UpdatedAt: manager.now()}); err != nil {
			return err
		}
	}
	if !activate {
		return nil
	}
	if expectedCurrent != nil {
		return ErrUnavailable
	}
	version, _, err := manager.dependencies.Configuration.LoadActive(ctx, account.accountID)
	if err != nil || version.ID <= 0 {
		return ErrUnavailable
	}
	active := &activeSession{account: account, owner: owner, events: make(chan roomsource.Event, 256), workerDone: make(chan struct{}), admitting: true, sourceHealthy: true}
	subscription, err := manager.dependencies.RoomSources.SubscribeCanonical(ctx, roomID, account.accountID, sessionSink{active: active})
	if err != nil {
		return ErrUnavailable
	}
	canonical := subscription.RoomID()
	if !validRoomID(canonical) || canonical != roomID {
		subscription.Cancel()
		_ = subscription.Wait(ctx)
		return ErrUnavailable
	}
	session, err := manager.dependencies.Sessions.StartSession(ctx, StartSessionCommand{Owner: owner, AccountID: account.accountID, RoomID: canonical, BroadcastSessionID: broadcastSessionID, ConfigVersionID: version.ID, StartedAt: manager.now(), Reconcile: reconcile})
	if err != nil {
		subscription.Cancel()
		_ = subscription.Wait(ctx)
		if errors.Is(err, ErrOwnershipConflict) {
			return ErrOwnershipConflict
		}
		return ErrUnavailable
	}
	active.admissionMu.Lock()
	active.session, active.subscription, active.cleanupPhase = session, subscription, cleanupPhaseStarted
	active.admissionMu.Unlock()
	transitionOwned := broadcastSessionID > 0
	if transitionOwned {
		account.mu.Lock()
		account.transitionPending = active
		account.mu.Unlock()
	}
	processor, err := manager.processorFactory.New(manager.processing, owner, session)
	if err != nil {
		if transitionOwned {
			return ErrUnavailable
		}
		subscription.Cancel()
		_ = subscription.Wait(ctx)
		endErr := manager.dependencies.Sessions.EndSession(ctx, EndSessionCommand{Owner: owner, AccountID: account.accountID, SessionID: session.ID, EndedAt: normalizeDatabaseTime(manager.now())})
		if errors.Is(err, ErrOwnershipConflict) || errors.Is(endErr, ErrOwnershipConflict) {
			return ErrOwnershipConflict
		}
		return ErrUnavailable
	}
	if reporter, ok := processor.(ownershipLossReporter); ok {
		reporter.SetOwnershipLost(func() { manager.handleProcessOwnershipConflict(account, active) })
	}
	active.admissionMu.Lock()
	active.processor = processor
	active.processor.SetConnectionHealthy(active.sourceHealthy)
	active.admissionMu.Unlock()
	if manager.beforeSessionPublish != nil {
		manager.beforeSessionPublish()
	}
	active.admissionMu.Lock()
	account.mu.Lock()
	if account.stale || account.owner != owner {
		account.mu.Unlock()
		active.admissionMu.Unlock()
		if transitionOwned {
			return ErrOwnershipConflict
		}
		manager.discardUnpublishedSession(ctx, active)
		return ErrOwnershipConflict
	}
	if account.current != expectedCurrent {
		account.mu.Unlock()
		active.admissionMu.Unlock()
		if transitionOwned {
			return ErrUnavailable
		}
		manager.discardUnpublishedSession(ctx, active)
		return ErrUnavailable
	}
	if account.shutting {
		if transitionOwned {
			account.mu.Unlock()
			active.admissionMu.Unlock()
			return ErrClosed
		}
		active.admitting = false
		active.subscription.Cancel()
		account.current = active
		account.reconcile = false
		account.degraded = false
		account.mu.Unlock()
		active.admissionMu.Unlock()
		return ErrClosed
	}
	if account.disabled {
		account.mu.Unlock()
		active.admissionMu.Unlock()
		if transitionOwned {
			return ErrAccountDisabled
		}
		manager.discardUnpublishedSession(ctx, active)
		return ErrAccountDisabled
	}
	account.current = active
	if account.transitionPending == active {
		account.transitionPending = nil
	}
	account.reconcile = false
	account.degraded = false
	account.sourceDegraded = !active.sourceHealthy
	active.workerStarted = true
	go active.run(manager, account)
	account.mu.Unlock()
	active.admissionMu.Unlock()
	return nil
}

func (manager *Manager) discardUnpublishedSession(ctx context.Context, active *activeSession) {
	active.admissionMu.Lock()
	active.admitting = false
	active.subscription.Cancel()
	active.admissionMu.Unlock()
	_ = active.subscription.Wait(ctx)
	if active.processor != nil {
		_ = active.processor.Close(ctx)
	}
	active.admissionMu.Lock()
	active.eventsClosed = true
	active.drained = true
	active.admissionMu.Unlock()
}

type sessionSink struct{ active *activeSession }

func (sink sessionSink) OnEvent(event roomsource.Event) {
	if sink.active == nil {
		return
	}
	sink.active.admissionMu.Lock()
	defer sink.active.admissionMu.Unlock()
	if !sink.active.admitting {
		return
	}
	sink.active.sourceHealthy = true
	if sink.active.processor != nil {
		sink.active.processor.SetConnectionHealthy(true)
	}
	sink.active.account.mu.Lock()
	sink.active.account.sourceDegraded = false
	sink.active.account.mu.Unlock()
	select {
	case sink.active.events <- event:
	default:
		sink.active.account.mu.Lock()
		sink.active.account.degraded = true
		sink.active.account.mu.Unlock()
	}
}
func (sink sessionSink) OnError(error) {
	if sink.active == nil || sink.active.account == nil {
		return
	}
	sink.active.admissionMu.Lock()
	sink.active.sourceHealthy = false
	if sink.active.processor != nil {
		sink.active.processor.SetConnectionHealthy(false)
	}
	sink.active.account.mu.Lock()
	sink.active.account.sourceDegraded = true
	sink.active.account.mu.Unlock()
	sink.active.admissionMu.Unlock()
}

func (active *activeSession) run(manager *Manager, account *accountRuntime) {
	var workerDoneOnce sync.Once
	signalWorkerDone := func() { workerDoneOnce.Do(func() { close(active.workerDone) }) }
	defer signalWorkerDone()
	for event := range active.events {
		if err := active.processor.Accept(event); err != nil {
			if errors.Is(err, ErrOwnershipConflict) {
				signalWorkerDone()
				manager.handleProcessOwnershipConflict(account, active)
				return
			}
			if errors.Is(err, ErrPersistenceUnavailable) {
				continue
			}
			account.mu.Lock()
			account.degraded = true
			account.mu.Unlock()
		}
	}
}

func (manager *Manager) handleProcessOwnershipConflict(account *accountRuntime, active *activeSession) {
	account.mu.Lock()
	if account.current != active || account.stale {
		account.mu.Unlock()
		return
	}
	releaseFence := OwnerFence{}
	if validOwnerFence(account.owner) && account.owner != active.owner {
		releaseFence = account.owner
	}
	expectedOwner := account.owner
	account.mu.Unlock()
	manager.beginStaleCleanup(account, active, expectedOwner, releaseFence)
}

func (manager *Manager) closeCurrent(ctx context.Context, account *accountRuntime) error {
	return manager.closeCurrentToPhase(ctx, account, cleanupPhaseFinalized, OwnerFence{})
}

func (manager *Manager) closeCurrentTerminal(ctx context.Context, account *accountRuntime, releaseFence OwnerFence) error {
	return manager.closeCurrentToPhase(ctx, account, cleanupPhaseReleased, releaseFence)
}

func (manager *Manager) closeCurrentToPhase(ctx context.Context, account *accountRuntime, target durableCleanupPhase, releaseFence OwnerFence) error {
	account.mu.Lock()
	active := account.current
	if active == nil {
		active = account.transitionPending
	}
	account.mu.Unlock()
	if active == nil {
		return nil
	}
	if err := manager.advanceSessionCleanup(ctx, account, active, target, releaseFence); err != nil {
		return err
	}
	account.mu.Lock()
	if account.current == active {
		account.current = nil
	}
	if account.transitionPending == active {
		account.transitionPending = nil
	}
	account.mu.Unlock()
	return nil
}

func (manager *Manager) advanceSessionCleanup(ctx context.Context, account *accountRuntime, active *activeSession, target durableCleanupPhase, releaseFence OwnerFence) error {
	if ctx == nil || active == nil || target < cleanupPhaseStarted || target > cleanupPhaseReleased {
		return ErrInvalidInput
	}
	active.admissionMu.Lock()
	if active.cleanupPhase == 0 && active.session.ID > 0 {
		active.cleanupPhase = cleanupPhaseStarted
	}
	if validOwnerFence(releaseFence) && (!active.needsReconcile || releaseFence != active.owner) {
		active.cleanupFence = releaseFence
		active.needsReconcile = false
	}
	phase := active.cleanupPhase
	active.admissionMu.Unlock()
	if phase < cleanupPhaseStarted {
		return ErrUnavailable
	}
	if phase == cleanupPhaseStarted {
		if err := manager.drainActive(ctx, active); err != nil {
			return err
		}
		if err := manager.endActiveSession(ctx, account, active); err != nil {
			return err
		}
		active.admissionMu.Lock()
		active.cleanupPhase = cleanupPhaseEnded
		phase = active.cleanupPhase
		active.admissionMu.Unlock()
	}
	if target >= cleanupPhaseFinalized && phase == cleanupPhaseEnded {
		if finalizer, ok := active.processor.(sessionPublisherFinalizer); ok {
			finalizer.FinalizeSession()
		}
		active.admissionMu.Lock()
		active.cleanupPhase = cleanupPhaseFinalized
		phase = active.cleanupPhase
		active.admissionMu.Unlock()
	}
	if target >= cleanupPhaseReleased && phase == cleanupPhaseFinalized {
		active.admissionMu.Lock()
		needsReconcile := active.needsReconcile
		fence := active.cleanupFence
		if !validOwnerFence(fence) {
			fence = active.owner
		}
		active.admissionMu.Unlock()
		if !needsReconcile {
			if err := manager.releaseOwner(ctx, account, fence); err != nil {
				return err
			}
		} else {
			account.mu.Lock()
			if account.owner == active.owner {
				account.owner = OwnerFence{}
				account.reconcile = false
			}
			account.mu.Unlock()
		}
		active.admissionMu.Lock()
		active.cleanupPhase = cleanupPhaseReleased
		active.admissionMu.Unlock()
	}
	return nil
}

func (manager *Manager) drainActive(ctx context.Context, active *activeSession) error {
	active.admissionMu.Lock()
	if !active.drained {
		active.admitting = false
		active.subscription.Cancel()
	}
	active.admissionMu.Unlock()
	active.admissionMu.Lock()
	drained := active.drained
	eventsClosed := active.eventsClosed
	workerStarted := active.workerStarted
	active.admissionMu.Unlock()
	if !eventsClosed {
		select {
		case <-active.subscription.Done():
		default:
			if err := active.subscription.Wait(ctx); err != nil {
				if ctx.Err() != nil {
					return ctx.Err()
				}
				return ErrUnavailable
			}
		}
		close(active.events)
		active.admissionMu.Lock()
		active.eventsClosed = true
		active.admissionMu.Unlock()
	}
	if !workerStarted {
		active.admissionMu.Lock()
		active.drained = true
		active.admissionMu.Unlock()
		drained = true
	}
	if !drained {
		select {
		case <-active.workerDone:
			active.admissionMu.Lock()
			active.drained = true
			active.admissionMu.Unlock()
			break
		default:
		}
		active.admissionMu.Lock()
		drained = active.drained
		active.admissionMu.Unlock()
	}
	if !drained {
		select {
		case <-active.workerDone:
		case <-ctx.Done():
			return ctx.Err()
		}
		active.admissionMu.Lock()
		active.drained = true
		active.admissionMu.Unlock()
	}
	active.admissionMu.Lock()
	processorClosed := active.processorClosed
	active.admissionMu.Unlock()
	if !processorClosed && active.processor != nil {
		if err := active.processor.Close(ctx); err != nil {
			return err
		}
		active.admissionMu.Lock()
		active.processorClosed = true
		active.admissionMu.Unlock()
	}
	return nil
}

func (manager *Manager) endActiveSession(ctx context.Context, account *accountRuntime, active *activeSession) error {
	active.admissionMu.Lock()
	if active.endedAt.IsZero() {
		active.endedAt = normalizeDatabaseTime(manager.now())
	}
	endedAt := active.endedAt
	needsReconcile := active.needsReconcile
	owner := active.cleanupFence
	if !validOwnerFence(owner) {
		owner = active.owner
	}
	active.admissionMu.Unlock()
	if !needsReconcile {
		err := manager.dependencies.Sessions.EndSession(ctx, EndSessionCommand{Owner: owner, AccountID: account.accountID, SessionID: active.session.ID, EndedAt: endedAt})
		if err == nil {
			return nil
		}
		if !errors.Is(err, ErrOwnershipConflict) {
			return err
		}
		active.admissionMu.Lock()
		active.needsReconcile = true
		active.cleanupFence = OwnerFence{}
		active.admissionMu.Unlock()
	}
	reconciler, ok := manager.dependencies.Sessions.(lostOwnershipSessionStore)
	if !ok {
		return ErrUnavailable
	}
	return reconciler.ReconcileSession(ctx, ReconcileSessionCommand{LostOwner: active.owner, AccountID: account.accountID, SessionID: active.session.ID, EndedAt: endedAt})
}
