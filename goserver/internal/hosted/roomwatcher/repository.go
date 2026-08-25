package roomwatcher

import (
	"context"
	"database/sql"
	"errors"
	"sort"
	"time"
)

var (
	// ErrRepositoryUnavailable deliberately hides driver and storage details.
	ErrRepositoryUnavailable = errors.New("roomwatcher: repository unavailable")
	// ErrTransitionConflict means the stored state changed before this writer
	// acquired its room row lock.
	ErrTransitionConflict = errors.New("roomwatcher: transition conflict")
)

// RecoverableRoom is the persisted state needed to restore one shared watcher
// after a process restart. Offline rooms are intentionally omitted.
type RecoverableRoom struct {
	RoomID     string
	State      State
	GraceUntil *time.Time
	LeaseEpoch uint64
	References []Reference
}

// sqlRepository is the MySQL implementation of the Manager repository port.
// It uses one transaction and a monitor-state row lock for every transition.
type sqlRepository struct {
	db *sql.DB
}

// NewRepository creates a room watcher repository over the Store-owned pool.
func NewRepository(db *sql.DB) *sqlRepository {
	return &sqlRepository{db: db}
}

var _ Repository = (*sqlRepository)(nil)

// SyncReferences atomically replaces the enabled account-to-room snapshot.
// State rows are retained after the final reference disappears so the terminal
// transition can be recorded by Manager before this method's next snapshot.
func (repository *sqlRepository) SyncReferences(ctx context.Context, references []Reference, terminal []Transition) ([]Transition, error) {
	if !repository.ready() || ctx == nil {
		return nil, ErrInvalidInput
	}
	normalized, _, err := normalizeReferences(references)
	if err != nil {
		return nil, ErrInvalidInput
	}
	terminal, err = normalizeTerminalTransitions(terminal)
	if err != nil {
		return nil, ErrInvalidInput
	}

	transaction, err := repository.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, ErrRepositoryUnavailable
	}
	committed := false
	defer func() {
		if !committed {
			_ = transaction.Rollback()
		}
	}()

	tail, err := lockOutboxTail(ctx, transaction, len(terminal) != 0)
	if err != nil {
		return nil, err
	}
	rows, err := transaction.QueryContext(ctx, "SELECT DISTINCT room_id FROM room_monitor_references FOR UPDATE")
	if err != nil {
		return nil, ErrRepositoryUnavailable
	}
	formerRooms := make([]string, 0)
	for rows.Next() {
		var roomID string
		if err := rows.Scan(&roomID); err != nil {
			_ = rows.Close()
			return nil, ErrRepositoryUnavailable
		}
		formerRooms = append(formerRooms, roomID)
	}
	if err := rows.Close(); err != nil {
		return nil, ErrRepositoryUnavailable
	}
	if err := rows.Err(); err != nil {
		return nil, ErrRepositoryUnavailable
	}

	rooms := make(map[string]struct{}, len(formerRooms)+len(normalized))
	for _, roomID := range formerRooms {
		rooms[roomID] = struct{}{}
	}
	for _, reference := range normalized {
		rooms[reference.RoomID] = struct{}{}
	}
	roomIDs := make([]string, 0, len(rooms))
	for roomID := range rooms {
		roomIDs = append(roomIDs, roomID)
	}
	sort.Strings(roomIDs)
	for _, roomID := range roomIDs {
		result, err := transaction.ExecContext(ctx, "INSERT INTO room_monitor_states (room_id) VALUES (?) ON DUPLICATE KEY UPDATE room_id = VALUES(room_id)", roomID)
		if err != nil || !zeroOneOrTwoRows(result) {
			return nil, ErrRepositoryUnavailable
		}
	}
	if _, err := transaction.ExecContext(ctx, "DELETE FROM room_monitor_references"); err != nil {
		return nil, ErrRepositoryUnavailable
	}
	for _, reference := range normalized {
		result, err := transaction.ExecContext(ctx, "INSERT INTO room_monitor_references (account_id, room_id) VALUES (?, ?)", reference.AccountID, reference.RoomID)
		if err != nil || !oneRow(result) {
			return nil, ErrRepositoryUnavailable
		}
	}
	persisted := make([]Transition, 0, len(terminal))
	for _, transition := range terminal {
		durable, err := repository.recordTransition(ctx, transaction, &tail, transition)
		if err != nil {
			return nil, err
		}
		persisted = append(persisted, durable)
	}
	if err := storeOutboxTail(ctx, transaction, tail); err != nil {
		return nil, err
	}
	if err := transaction.Commit(); err != nil {
		return nil, ErrRepositoryUnavailable
	}
	committed = true
	return persisted, nil
}

// RecordTransition applies one validated state-machine boundary, changes the
// business broadcast when needed, and inserts a replayable outbox record in
// the same transaction.
func (repository *sqlRepository) RecordTransition(ctx context.Context, transition Transition) (Transition, error) {
	if !repository.ready() || ctx == nil || !validTransition(transition) {
		return Transition{}, ErrInvalidInput
	}
	roomID, err := canonicalRoomID(transition.RoomID)
	if err != nil {
		return Transition{}, ErrInvalidInput
	}
	transition.RoomID = roomID
	transition.ConfirmedAt = databaseTime(transition.ConfirmedAt)
	if transition.GraceUntil != nil {
		value := databaseTime(*transition.GraceUntil)
		transition.GraceUntil = &value
	}

	transaction, err := repository.db.BeginTx(ctx, nil)
	if err != nil {
		return Transition{}, ErrRepositoryUnavailable
	}
	committed := false
	defer func() {
		if !committed {
			_ = transaction.Rollback()
		}
	}()
	tail, err := lockOutboxTail(ctx, transaction, true)
	if err != nil {
		return Transition{}, err
	}
	durable, err := repository.recordTransition(ctx, transaction, &tail, transition)
	if err != nil {
		return Transition{}, err
	}
	if err := storeOutboxTail(ctx, transaction, tail); err != nil {
		return Transition{}, err
	}
	if err := transaction.Commit(); err != nil {
		return Transition{}, ErrRepositoryUnavailable
	}
	committed = true
	return durable, nil
}

func (repository *sqlRepository) recordTransition(ctx context.Context, transaction *sql.Tx, tail *outboxTail, transition Transition) (Transition, error) {
	if tail == nil || tail.nextSequence == 0 || tail.nextSequence == ^uint64(0) {
		return Transition{}, ErrRepositoryUnavailable
	}
	var storedState string
	var storedGrace sql.NullTime
	var broadcastSessionID sql.NullInt64
	var leaseEpoch uint64
	err := transaction.QueryRowContext(ctx, "SELECT state, grace_until, broadcast_session_id, lease_epoch FROM room_monitor_states WHERE room_id = ? FOR UPDATE", transition.RoomID).Scan(&storedState, &storedGrace, &broadcastSessionID, &leaseEpoch)
	if errors.Is(err, sql.ErrNoRows) {
		return Transition{}, ErrTransitionConflict
	}
	if err != nil || !validState(State(storedState)) || leaseEpoch == 0 {
		return Transition{}, ErrRepositoryUnavailable
	}
	if (State(storedState) == StateOffline && (storedGrace.Valid || broadcastSessionID.Valid)) ||
		(State(storedState) == StateLive && (storedGrace.Valid || !broadcastSessionID.Valid)) ||
		(State(storedState) == StateGrace && (!storedGrace.Valid || !broadcastSessionID.Valid)) {
		return Transition{}, ErrRepositoryUnavailable
	}
	if State(storedState) != transition.From || leaseEpoch == ^uint64(0) {
		return Transition{}, ErrTransitionConflict
	}
	nextEpoch := leaseEpoch + 1

	var nextBroadcast any
	var nextGrace any
	switch transition.To {
	case StateLive:
		if transition.From == StateOffline {
			result, err := transaction.ExecContext(ctx, "INSERT INTO broadcast_sessions (room_id, started_at) VALUES (?, ?)", transition.RoomID, transition.ConfirmedAt)
			if err != nil || !oneRow(result) {
				return Transition{}, ErrRepositoryUnavailable
			}
			broadcastSessionID.Int64, err = result.LastInsertId()
			if err != nil || broadcastSessionID.Int64 <= 0 {
				return Transition{}, ErrRepositoryUnavailable
			}
			broadcastSessionID.Valid = true
		} else if transition.From != StateGrace || !broadcastSessionID.Valid || transition.NewBroadcast {
			return Transition{}, ErrTransitionConflict
		}
		nextBroadcast = broadcastSessionID.Int64
	case StateGrace:
		if transition.From != StateLive || !broadcastSessionID.Valid || transition.NewBroadcast || transition.GraceUntil == nil || !transition.GraceUntil.After(transition.ConfirmedAt) {
			return Transition{}, ErrTransitionConflict
		}
		nextBroadcast = broadcastSessionID.Int64
		nextGrace = *transition.GraceUntil
	case StateOffline:
		if (transition.From != StateLive && transition.From != StateGrace) || !broadcastSessionID.Valid || transition.NewBroadcast {
			return Transition{}, ErrTransitionConflict
		}
		result, err := transaction.ExecContext(ctx, "UPDATE broadcast_sessions SET ended_at = ? WHERE id = ? AND ended_at IS NULL", transition.ConfirmedAt, broadcastSessionID.Int64)
		if err != nil || !oneRow(result) {
			return Transition{}, ErrTransitionConflict
		}
	default:
		return Transition{}, ErrTransitionConflict
	}

	result, err := transaction.ExecContext(ctx, "UPDATE room_monitor_states SET state = ?, grace_until = NULL, broadcast_session_id = ?, lease_epoch = ?, updated_at = ? WHERE room_id = ? AND lease_epoch = ?", transition.To, nextBroadcast, nextEpoch, transition.ConfirmedAt, transition.RoomID, leaseEpoch)
	if transition.To == StateGrace {
		result, err = transaction.ExecContext(ctx, "UPDATE room_monitor_states SET state = ?, grace_until = ?, broadcast_session_id = ?, lease_epoch = ?, updated_at = ? WHERE room_id = ? AND lease_epoch = ?", transition.To, nextGrace, nextBroadcast, nextEpoch, transition.ConfirmedAt, transition.RoomID, leaseEpoch)
	}
	if err != nil || !oneRow(result) {
		return Transition{}, ErrTransitionConflict
	}
	sequence := tail.nextSequence
	result, err = transaction.ExecContext(ctx, "INSERT INTO room_monitor_transitions (sequence, room_id, lease_epoch, from_state, to_state, confirmed_at, grace_until, new_broadcast) VALUES (?, ?, ?, ?, ?, ?, ?, ?)", sequence, transition.RoomID, nextEpoch, transition.From, transition.To, transition.ConfirmedAt, nextGrace, transition.NewBroadcast)
	if err != nil || !oneRow(result) {
		return Transition{}, ErrRepositoryUnavailable
	}
	tail.nextSequence++
	tail.dirty = true
	transition.Sequence = sequence
	transition.LeaseEpoch = nextEpoch
	return transition, nil
}

// ReplayTransitions fetches the ordered, durable outbox after a consumer's
// cursor. The caller chooses the bounded batch size.
func (repository *sqlRepository) ReplayTransitions(ctx context.Context, afterSequence uint64, limit int) ([]Transition, error) {
	if !repository.ready() || ctx == nil || limit <= 0 || limit > MaxReplayLimit {
		return nil, ErrInvalidInput
	}
	rows, err := repository.db.QueryContext(ctx, "SELECT sequence, room_id, lease_epoch, from_state, to_state, confirmed_at, grace_until, new_broadcast FROM room_monitor_transitions WHERE sequence > ? ORDER BY sequence LIMIT ?", afterSequence, limit)
	if err != nil {
		return nil, ErrRepositoryUnavailable
	}
	defer rows.Close()
	transitions := make([]Transition, 0, limit)
	for rows.Next() {
		var transition Transition
		var graceUntil sql.NullTime
		if err := rows.Scan(&transition.Sequence, &transition.RoomID, &transition.LeaseEpoch, &transition.From, &transition.To, &transition.ConfirmedAt, &graceUntil, &transition.NewBroadcast); err != nil || transition.Sequence == 0 || transition.LeaseEpoch == 0 || !validState(transition.From) || !validState(transition.To) || transition.ConfirmedAt.IsZero() {
			return nil, ErrRepositoryUnavailable
		}
		transition.ConfirmedAt = databaseTime(transition.ConfirmedAt)
		if graceUntil.Valid {
			value := databaseTime(graceUntil.Time)
			transition.GraceUntil = &value
		}
		transitions = append(transitions, transition)
	}
	if err := rows.Err(); err != nil {
		return nil, ErrRepositoryUnavailable
	}
	return transitions, nil
}

// LoadRecoverable restores active and grace rooms with their shared account
// references. An offline room has no live work to recover.
func (repository *sqlRepository) LoadRecoverable(ctx context.Context) ([]RecoverableRoom, error) {
	if !repository.ready() || ctx == nil {
		return nil, ErrInvalidInput
	}
	rows, err := repository.db.QueryContext(ctx, "SELECT s.room_id, s.state, s.grace_until, s.lease_epoch, r.account_id FROM room_monitor_states AS s JOIN room_monitor_references AS r ON r.room_id = s.room_id WHERE s.state IN ('live', 'grace') ORDER BY s.room_id, r.account_id")
	if err != nil {
		return nil, ErrRepositoryUnavailable
	}
	defer rows.Close()
	rooms := make([]RecoverableRoom, 0)
	for rows.Next() {
		var roomID, stateValue string
		var graceUntil sql.NullTime
		var leaseEpoch uint64
		var accountID int64
		if err := rows.Scan(&roomID, &stateValue, &graceUntil, &leaseEpoch, &accountID); err != nil || accountID <= 0 || leaseEpoch == 0 || !validState(State(stateValue)) {
			return nil, ErrRepositoryUnavailable
		}
		if len(rooms) == 0 || rooms[len(rooms)-1].RoomID != roomID {
			room := RecoverableRoom{RoomID: roomID, State: State(stateValue), LeaseEpoch: leaseEpoch}
			if graceUntil.Valid {
				value := databaseTime(graceUntil.Time)
				room.GraceUntil = &value
			}
			if room.State == StateGrace && (room.GraceUntil == nil || room.GraceUntil.IsZero()) {
				return nil, ErrRepositoryUnavailable
			}
			rooms = append(rooms, room)
		} else if rooms[len(rooms)-1].State != State(stateValue) || rooms[len(rooms)-1].LeaseEpoch != leaseEpoch {
			return nil, ErrRepositoryUnavailable
		}
		index := len(rooms) - 1
		rooms[index].References = append(rooms[index].References, Reference{AccountID: accountID, RoomID: roomID})
	}
	if err := rows.Err(); err != nil {
		return nil, ErrRepositoryUnavailable
	}
	return rooms, nil
}

func (repository *sqlRepository) ready() bool {
	return repository != nil && repository.db != nil
}

func validTransition(transition Transition) bool {
	if transition.RoomID == "" || !validState(transition.From) || !validState(transition.To) || transition.From == transition.To || transition.ConfirmedAt.IsZero() || transition.Sequence != 0 || transition.LeaseEpoch != 0 {
		return false
	}
	if transition.To == StateGrace && transition.GraceUntil == nil {
		return false
	}
	return transition.NewBroadcast == (transition.From == StateOffline && transition.To == StateLive)
}

func normalizeTerminalTransitions(transitions []Transition) ([]Transition, error) {
	normalized := make([]Transition, len(transitions))
	seen := make(map[string]struct{}, len(transitions))
	for index, transition := range transitions {
		if !validTransition(transition) || transition.To != StateOffline || transition.NewBroadcast {
			return nil, ErrInvalidInput
		}
		roomID, err := canonicalRoomID(transition.RoomID)
		if err != nil {
			return nil, ErrInvalidInput
		}
		if _, exists := seen[roomID]; exists {
			return nil, ErrInvalidInput
		}
		seen[roomID] = struct{}{}
		transition.RoomID = roomID
		transition.ConfirmedAt = databaseTime(transition.ConfirmedAt)
		normalized[index] = transition
	}
	sort.Slice(normalized, func(left, right int) bool { return normalized[left].RoomID < normalized[right].RoomID })
	return normalized, nil
}

type outboxTail struct {
	nextSequence     uint64
	expectedSequence uint64
	dirty            bool
}

func lockOutboxTail(ctx context.Context, transaction *sql.Tx, needed bool) (outboxTail, error) {
	if !needed {
		return outboxTail{}, nil
	}
	var tail outboxTail
	err := transaction.QueryRowContext(ctx, "SELECT next_sequence FROM room_monitor_outbox_tail WHERE id = 1 FOR UPDATE").Scan(&tail.nextSequence)
	if errors.Is(err, sql.ErrNoRows) || err != nil || tail.nextSequence == 0 {
		return outboxTail{}, ErrRepositoryUnavailable
	}
	tail.expectedSequence = tail.nextSequence
	return tail, nil
}

func storeOutboxTail(ctx context.Context, transaction *sql.Tx, tail outboxTail) error {
	if !tail.dirty {
		return nil
	}
	result, err := transaction.ExecContext(ctx, "UPDATE room_monitor_outbox_tail SET next_sequence = ? WHERE id = 1 AND next_sequence = ?", tail.nextSequence, tail.expectedSequence)
	if err != nil || !oneRow(result) {
		return ErrRepositoryUnavailable
	}
	return nil
}

func validState(state State) bool {
	return state == StateOffline || state == StateLive || state == StateGrace
}

func databaseTime(value time.Time) time.Time {
	return value.UTC().Truncate(time.Microsecond)
}

func oneRow(result sql.Result) bool {
	if result == nil {
		return false
	}
	rows, err := result.RowsAffected()
	return err == nil && rows == 1
}

func zeroOneOrTwoRows(result sql.Result) bool {
	if result == nil {
		return false
	}
	rows, err := result.RowsAffected()
	return err == nil && rows >= 0 && rows <= 2
}
