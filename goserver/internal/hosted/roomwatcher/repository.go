package roomwatcher

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"slices"
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
// state event and empty reference snapshot can share this transaction.
func (repository *sqlRepository) SyncReferences(ctx context.Context, references []Reference, terminal []Transition) ([]Event, error) {
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

	// Reference changes also allocate outbox sequences. Take the singleton tail
	// before any room/reference lock so every writer uses one lock order.
	tail, err := lockOutboxTail(ctx, transaction, true)
	if err != nil {
		return nil, err
	}
	rows, err := transaction.QueryContext(ctx, "SELECT account_id, room_id FROM room_monitor_references ORDER BY room_id, account_id FOR UPDATE")
	if err != nil {
		return nil, ErrRepositoryUnavailable
	}
	formerReferences := make([]Reference, 0)
	for rows.Next() {
		var accountID int64
		var roomID string
		if err := rows.Scan(&accountID, &roomID); err != nil || accountID <= 0 {
			_ = rows.Close()
			return nil, ErrRepositoryUnavailable
		}
		canonical, canonicalErr := canonicalRoomID(roomID)
		if canonicalErr != nil || canonical != roomID {
			_ = rows.Close()
			return nil, ErrRepositoryUnavailable
		}
		formerReferences = append(formerReferences, Reference{AccountID: accountID, RoomID: roomID})
	}
	if err := rows.Close(); err != nil {
		return nil, ErrRepositoryUnavailable
	}
	if err := rows.Err(); err != nil {
		return nil, ErrRepositoryUnavailable
	}

	referenceEvents := changedReferenceSnapshots(formerReferences, normalized)
	rooms := make(map[string]struct{}, len(formerReferences)+len(normalized))
	for _, reference := range formerReferences {
		rooms[reference.RoomID] = struct{}{}
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
	persisted := make([]Event, 0, len(referenceEvents)+len(terminal))
	for _, snapshot := range referenceEvents {
		durable, err := repository.recordReferenceEvent(ctx, transaction, &tail, snapshot)
		if err != nil {
			return nil, err
		}
		persisted = append(persisted, durable)
	}
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
func (repository *sqlRepository) RecordTransition(ctx context.Context, transition Transition) (Event, error) {
	if !repository.ready() || ctx == nil || !validTransition(transition) {
		return Event{}, ErrInvalidInput
	}
	roomID, err := canonicalRoomID(transition.RoomID)
	if err != nil {
		return Event{}, ErrInvalidInput
	}
	transition.RoomID = roomID
	transition.ConfirmedAt = databaseTime(transition.ConfirmedAt)
	if transition.GraceUntil != nil {
		value := databaseTime(*transition.GraceUntil)
		transition.GraceUntil = &value
	}

	transaction, err := repository.db.BeginTx(ctx, nil)
	if err != nil {
		return Event{}, ErrRepositoryUnavailable
	}
	committed := false
	defer func() {
		if !committed {
			_ = transaction.Rollback()
		}
	}()
	tail, err := lockOutboxTail(ctx, transaction, true)
	if err != nil {
		return Event{}, err
	}
	durable, err := repository.recordTransition(ctx, transaction, &tail, transition)
	if err != nil {
		return Event{}, err
	}
	if err := storeOutboxTail(ctx, transaction, tail); err != nil {
		return Event{}, err
	}
	if err := transaction.Commit(); err != nil {
		return Event{}, ErrRepositoryUnavailable
	}
	committed = true
	return durable, nil
}

func (repository *sqlRepository) recordTransition(ctx context.Context, transaction *sql.Tx, tail *outboxTail, transition Transition) (Event, error) {
	if tail == nil || tail.nextSequence == 0 || tail.nextSequence == ^uint64(0) {
		return Event{}, ErrRepositoryUnavailable
	}
	var storedState string
	var storedGrace sql.NullTime
	var broadcastSessionID sql.NullInt64
	var leaseEpoch uint64
	err := transaction.QueryRowContext(ctx, "SELECT state, grace_until, broadcast_session_id, lease_epoch FROM room_monitor_states WHERE room_id = ? FOR UPDATE", transition.RoomID).Scan(&storedState, &storedGrace, &broadcastSessionID, &leaseEpoch)
	if errors.Is(err, sql.ErrNoRows) {
		return Event{}, ErrTransitionConflict
	}
	if err != nil || !validState(State(storedState)) || leaseEpoch == 0 {
		return Event{}, ErrRepositoryUnavailable
	}
	if (State(storedState) == StateOffline && (storedGrace.Valid || broadcastSessionID.Valid)) ||
		(State(storedState) == StateLive && (storedGrace.Valid || !broadcastSessionID.Valid)) ||
		(State(storedState) == StateGrace && (!storedGrace.Valid || !broadcastSessionID.Valid)) {
		return Event{}, ErrRepositoryUnavailable
	}
	if State(storedState) != transition.From || leaseEpoch == ^uint64(0) {
		return Event{}, ErrTransitionConflict
	}
	nextEpoch := leaseEpoch + 1

	var nextBroadcast any
	var nextGrace any
	switch transition.To {
	case StateLive:
		if transition.From == StateOffline {
			result, err := transaction.ExecContext(ctx, "INSERT INTO broadcast_sessions (room_id, started_at) VALUES (?, ?)", transition.RoomID, transition.ConfirmedAt)
			if err != nil || !oneRow(result) {
				return Event{}, ErrRepositoryUnavailable
			}
			broadcastSessionID.Int64, err = result.LastInsertId()
			if err != nil || broadcastSessionID.Int64 <= 0 {
				return Event{}, ErrRepositoryUnavailable
			}
			broadcastSessionID.Valid = true
		} else if transition.From != StateGrace || !broadcastSessionID.Valid || transition.NewBroadcast {
			return Event{}, ErrTransitionConflict
		}
		nextBroadcast = broadcastSessionID.Int64
	case StateGrace:
		if transition.From != StateLive || !broadcastSessionID.Valid || transition.NewBroadcast || transition.GraceUntil == nil || !transition.GraceUntil.After(transition.ConfirmedAt) {
			return Event{}, ErrTransitionConflict
		}
		nextBroadcast = broadcastSessionID.Int64
		nextGrace = *transition.GraceUntil
	case StateOffline:
		if (transition.From != StateLive && transition.From != StateGrace) || !broadcastSessionID.Valid || transition.NewBroadcast {
			return Event{}, ErrTransitionConflict
		}
		result, err := transaction.ExecContext(ctx, "UPDATE broadcast_sessions SET ended_at = ? WHERE id = ? AND ended_at IS NULL", transition.ConfirmedAt, broadcastSessionID.Int64)
		if err != nil || !oneRow(result) {
			return Event{}, ErrTransitionConflict
		}
	default:
		return Event{}, ErrTransitionConflict
	}

	result, err := transaction.ExecContext(ctx, "UPDATE room_monitor_states SET state = ?, grace_until = NULL, broadcast_session_id = ?, lease_epoch = ?, updated_at = ? WHERE room_id = ? AND lease_epoch = ?", transition.To, nextBroadcast, nextEpoch, transition.ConfirmedAt, transition.RoomID, leaseEpoch)
	if transition.To == StateGrace {
		result, err = transaction.ExecContext(ctx, "UPDATE room_monitor_states SET state = ?, grace_until = ?, broadcast_session_id = ?, lease_epoch = ?, updated_at = ? WHERE room_id = ? AND lease_epoch = ?", transition.To, nextGrace, nextBroadcast, nextEpoch, transition.ConfirmedAt, transition.RoomID, leaseEpoch)
	}
	if err != nil || !oneRow(result) {
		return Event{}, ErrTransitionConflict
	}
	sequence := tail.nextSequence
	result, err = transaction.ExecContext(ctx, "INSERT INTO room_monitor_transitions (sequence, event_kind, room_id, lease_epoch, from_state, to_state, confirmed_at, grace_until, new_broadcast, account_ids_json) VALUES (?, 'room_state_changed', ?, ?, ?, ?, ?, ?, ?, NULL)", sequence, transition.RoomID, nextEpoch, transition.From, transition.To, transition.ConfirmedAt, nextGrace, transition.NewBroadcast)
	if err != nil || !oneRow(result) {
		return Event{}, ErrRepositoryUnavailable
	}
	tail.nextSequence++
	tail.dirty = true
	transition.LeaseEpoch = nextEpoch
	return Event{Sequence: sequence, RoomStateChanged: &transition}, nil
}

func (repository *sqlRepository) recordReferenceEvent(ctx context.Context, transaction *sql.Tx, tail *outboxTail, snapshot RoomReferencesChanged) (Event, error) {
	if tail == nil || tail.nextSequence == 0 || tail.nextSequence == ^uint64(0) || !validReferencesChanged(snapshot) {
		return Event{}, ErrRepositoryUnavailable
	}
	payload, err := json.Marshal(snapshot.AccountIDs)
	if err != nil {
		return Event{}, ErrRepositoryUnavailable
	}
	sequence := tail.nextSequence
	result, err := transaction.ExecContext(ctx, "INSERT INTO room_monitor_transitions (sequence, event_kind, room_id, lease_epoch, from_state, to_state, confirmed_at, grace_until, new_broadcast, account_ids_json) VALUES (?, 'room_references_changed', ?, NULL, NULL, NULL, NULL, NULL, NULL, ?)", sequence, snapshot.RoomID, payload)
	if err != nil || !oneRow(result) {
		return Event{}, ErrRepositoryUnavailable
	}
	tail.nextSequence++
	tail.dirty = true
	copy := snapshot
	copy.AccountIDs = append([]int64(nil), snapshot.AccountIDs...)
	return Event{Sequence: sequence, RoomReferencesChanged: &copy}, nil
}

// ReplayEvents fetches the ordered, durable outbox after a consumer's
// cursor. The caller chooses the bounded batch size.
func (repository *sqlRepository) ReplayEvents(ctx context.Context, afterSequence uint64, limit int) ([]Event, error) {
	if !repository.ready() || ctx == nil || limit <= 0 || limit > MaxReplayLimit {
		return nil, ErrInvalidInput
	}
	rows, err := repository.db.QueryContext(ctx, "SELECT sequence, event_kind, room_id, lease_epoch, from_state, to_state, confirmed_at, grace_until, new_broadcast, account_ids_json FROM room_monitor_transitions WHERE sequence > ? ORDER BY sequence LIMIT ?", afterSequence, limit)
	if err != nil {
		return nil, ErrRepositoryUnavailable
	}
	defer rows.Close()
	events := make([]Event, 0, limit)
	previousSequence := afterSequence
	for rows.Next() {
		var sequence uint64
		var eventKind, roomID string
		var leaseEpoch sql.Null[uint64]
		var fromState, toState sql.NullString
		var confirmedAt, graceUntil sql.NullTime
		var newBroadcast sql.NullBool
		var accountIDsJSON []byte
		if err := rows.Scan(&sequence, &eventKind, &roomID, &leaseEpoch, &fromState, &toState, &confirmedAt, &graceUntil, &newBroadcast, &accountIDsJSON); err != nil || sequence <= previousSequence {
			return nil, ErrRepositoryUnavailable
		}
		event := Event{Sequence: sequence}
		switch eventKind {
		case "room_state_changed":
			if !leaseEpoch.Valid || leaseEpoch.V == 0 || !fromState.Valid || !toState.Valid || !confirmedAt.Valid || !newBroadcast.Valid || accountIDsJSON != nil {
				return nil, ErrRepositoryUnavailable
			}
			transition := Transition{RoomID: roomID, LeaseEpoch: leaseEpoch.V, From: State(fromState.String), To: State(toState.String), ConfirmedAt: databaseTime(confirmedAt.Time), NewBroadcast: newBroadcast.Bool}
			if graceUntil.Valid {
				value := databaseTime(graceUntil.Time)
				transition.GraceUntil = &value
			}
			event.RoomStateChanged = &transition
		case "room_references_changed":
			if leaseEpoch.Valid || fromState.Valid || toState.Valid || confirmedAt.Valid || graceUntil.Valid || newBroadcast.Valid || accountIDsJSON == nil {
				return nil, ErrRepositoryUnavailable
			}
			var accountIDs []int64
			if err := json.Unmarshal(accountIDsJSON, &accountIDs); err != nil {
				return nil, ErrRepositoryUnavailable
			}
			event.RoomReferencesChanged = &RoomReferencesChanged{RoomID: roomID, AccountIDs: accountIDs}
		default:
			return nil, ErrRepositoryUnavailable
		}
		if !validEvent(event) {
			return nil, ErrRepositoryUnavailable
		}
		events = append(events, event)
		previousSequence = sequence
	}
	if err := rows.Err(); err != nil {
		return nil, ErrRepositoryUnavailable
	}
	return events, nil
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
	switch transition.To {
	case StateLive:
		return transition.GraceUntil == nil && (transition.From == StateOffline || transition.From == StateGrace) && transition.NewBroadcast == (transition.From == StateOffline)
	case StateGrace:
		return transition.From == StateLive && transition.GraceUntil != nil && transition.GraceUntil.After(transition.ConfirmedAt) && !transition.NewBroadcast
	case StateOffline:
		return transition.GraceUntil == nil && (transition.From == StateLive || transition.From == StateGrace) && !transition.NewBroadcast
	}
	return false
}

func validDurableTransition(transition Transition) bool {
	if transition.Sequence != 0 || transition.LeaseEpoch == 0 {
		return false
	}
	transition.LeaseEpoch = 0
	return validTransition(transition)
}

func validReferencesChanged(snapshot RoomReferencesChanged) bool {
	canonical, err := canonicalRoomID(snapshot.RoomID)
	if err != nil || canonical != snapshot.RoomID {
		return false
	}
	var previous int64
	for _, accountID := range snapshot.AccountIDs {
		if accountID <= previous {
			return false
		}
		previous = accountID
	}
	return true
}

func validEvent(event Event) bool {
	if event.Sequence == 0 || (event.RoomStateChanged == nil) == (event.RoomReferencesChanged == nil) {
		return false
	}
	if event.RoomStateChanged != nil {
		return validDurableTransition(*event.RoomStateChanged)
	}
	return validReferencesChanged(*event.RoomReferencesChanged)
}

// changedReferenceSnapshots compares two normalized snapshots and returns
// removals before additions. Both groups are canonical-room sorted, and every
// payload contains the complete next account set for that room.
func changedReferenceSnapshots(former, next []Reference) []RoomReferencesChanged {
	formerByRoom := referencesByRoom(former)
	nextByRoom := referencesByRoom(next)
	rooms := make(map[string]struct{}, len(formerByRoom)+len(nextByRoom))
	for roomID := range formerByRoom {
		rooms[roomID] = struct{}{}
	}
	for roomID := range nextByRoom {
		rooms[roomID] = struct{}{}
	}
	removed, added := make([]RoomReferencesChanged, 0), make([]RoomReferencesChanged, 0)
	for roomID := range rooms {
		before, after := formerByRoom[roomID], nextByRoom[roomID]
		if slices.Equal(before, after) {
			continue
		}
		snapshot := RoomReferencesChanged{RoomID: roomID, AccountIDs: append([]int64{}, after...)}
		if containsRemovedAccount(before, after) {
			removed = append(removed, snapshot)
		} else {
			added = append(added, snapshot)
		}
	}
	sort.Slice(removed, func(left, right int) bool { return removed[left].RoomID < removed[right].RoomID })
	sort.Slice(added, func(left, right int) bool { return added[left].RoomID < added[right].RoomID })
	return append(removed, added...)
}

func referencesByRoom(references []Reference) map[string][]int64 {
	result := make(map[string][]int64)
	for _, reference := range references {
		result[reference.RoomID] = append(result[reference.RoomID], reference.AccountID)
	}
	return result
}

func containsRemovedAccount(former, next []int64) bool {
	nextSet := make(map[int64]struct{}, len(next))
	for _, accountID := range next {
		nextSet[accountID] = struct{}{}
	}
	for _, accountID := range former {
		if _, exists := nextSet[accountID]; !exists {
			return true
		}
	}
	return false
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
