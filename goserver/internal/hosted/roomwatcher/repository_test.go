package roomwatcher

import (
	"context"
	"errors"
	"regexp"
	"slices"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
)

// This test fails if a reference replacement and its complete per-room
// snapshots do not share the transition outbox allocator and transaction.
func TestRepositorySyncReferencesWritesOnlyChangedSnapshotsToSharedOutbox(t *testing.T) {
	repository, mock, closeDB := newMockRepository(t)
	defer closeDB()

	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta("SELECT next_sequence FROM room_monitor_outbox_tail WHERE id = 1 FOR UPDATE")).
		WillReturnRows(sqlmock.NewRows([]string{"next_sequence"}).AddRow(uint64(40)))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT account_id, room_id FROM room_monitor_references ORDER BY room_id, account_id FOR UPDATE")).
		WillReturnRows(sqlmock.NewRows([]string{"account_id", "room_id"}).AddRow(int64(1), "7").AddRow(int64(2), "7").AddRow(int64(3), "9"))
	for _, roomID := range []string{"7", "8", "9"} {
		mock.ExpectExec(regexp.QuoteMeta("INSERT INTO room_monitor_states (room_id) VALUES (?) ON DUPLICATE KEY UPDATE room_id = VALUES(room_id)")).
			WithArgs(roomID).WillReturnResult(sqlmock.NewResult(0, 1))
	}
	mock.ExpectExec(regexp.QuoteMeta("DELETE FROM room_monitor_references")).WillReturnResult(sqlmock.NewResult(0, 3))
	for _, reference := range []Reference{{AccountID: 2, RoomID: "7"}, {AccountID: 1, RoomID: "8"}, {AccountID: 3, RoomID: "9"}} {
		mock.ExpectExec(regexp.QuoteMeta("INSERT INTO room_monitor_references (account_id, room_id) VALUES (?, ?)")).
			WithArgs(reference.AccountID, reference.RoomID).WillReturnResult(sqlmock.NewResult(0, 1))
	}
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO room_monitor_transitions (sequence, event_kind, room_id, lease_epoch, from_state, to_state, confirmed_at, grace_until, new_broadcast, account_ids_json) VALUES (?, 'room_references_changed', ?, NULL, NULL, NULL, NULL, NULL, NULL, ?)")).
		WithArgs(uint64(40), "7", []byte("[2]")).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO room_monitor_transitions (sequence, event_kind, room_id, lease_epoch, from_state, to_state, confirmed_at, grace_until, new_broadcast, account_ids_json) VALUES (?, 'room_references_changed', ?, NULL, NULL, NULL, NULL, NULL, NULL, ?)")).
		WithArgs(uint64(41), "8", []byte("[1]")).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(regexp.QuoteMeta("UPDATE room_monitor_outbox_tail SET next_sequence = ? WHERE id = 1 AND next_sequence = ?")).
		WithArgs(uint64(42), uint64(40)).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	got, err := repository.SyncReferences(context.Background(), []Reference{{AccountID: 3, RoomID: "9"}, {AccountID: 1, RoomID: "8"}, {AccountID: 2, RoomID: "7"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Sequence != 40 || got[0].RoomReferencesChanged == nil || !slices.Equal(got[0].RoomReferencesChanged.AccountIDs, []int64{2}) || got[1].Sequence != 41 || got[1].RoomReferencesChanged == nil || !slices.Equal(got[1].RoomReferencesChanged.AccountIDs, []int64{1}) {
		t.Fatalf("SyncReferences() events = %#v", got)
	}
	assertRepositoryExpectations(t, mock)
}

// This test fails if replay accepts a row with both payload shapes populated.
func TestRepositoryReplayEventsRejectsMultiplePayloads(t *testing.T) {
	repository, mock, closeDB := newMockRepository(t)
	defer closeDB()
	confirmedAt := time.Date(2026, 8, 26, 12, 1, 0, 0, time.UTC)
	mock.ExpectQuery(regexp.QuoteMeta("SELECT sequence, event_kind, room_id, lease_epoch, from_state, to_state, confirmed_at, grace_until, new_broadcast, account_ids_json FROM room_monitor_transitions WHERE sequence > ? ORDER BY sequence LIMIT ?")).
		WithArgs(uint64(0), 1).
		WillReturnRows(sqlmock.NewRows([]string{"sequence", "event_kind", "room_id", "lease_epoch", "from_state", "to_state", "confirmed_at", "grace_until", "new_broadcast", "account_ids_json"}).
			AddRow(uint64(42), "room_state_changed", "7", uint64(8), "offline", "live", confirmedAt, nil, true, []byte("[1]")))
	if _, err := repository.ReplayEvents(context.Background(), 0, 1); !errors.Is(err, ErrRepositoryUnavailable) {
		t.Fatalf("ReplayEvents() error = %v, want ErrRepositoryUnavailable", err)
	}
	assertRepositoryExpectations(t, mock)
}

// This test fails if a reference snapshot can leave an old room transition
// unlocked while the snapshot is being replaced.
func TestRepositorySyncReferencesLocksFormerAndNextRoomsBeforeReplacingSnapshot(t *testing.T) {
	repository, mock, closeDB := newMockRepository(t)
	defer closeDB()

	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta("SELECT next_sequence FROM room_monitor_outbox_tail WHERE id = 1 FOR UPDATE")).
		WillReturnRows(sqlmock.NewRows([]string{"next_sequence"}).AddRow(uint64(42)))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT account_id, room_id FROM room_monitor_references ORDER BY room_id, account_id FOR UPDATE")).
		WillReturnRows(sqlmock.NewRows([]string{"account_id", "room_id"}).AddRow(int64(1), "7"))
	for _, roomID := range []string{"7", "8"} {
		mock.ExpectExec(regexp.QuoteMeta("INSERT INTO room_monitor_states (room_id) VALUES (?) ON DUPLICATE KEY UPDATE room_id = VALUES(room_id)")).
			WithArgs(roomID).
			WillReturnResult(sqlmock.NewResult(0, 1))
	}
	mock.ExpectExec(regexp.QuoteMeta("DELETE FROM room_monitor_references")).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO room_monitor_references (account_id, room_id) VALUES (?, ?)")).
		WithArgs(int64(1), "7").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO room_monitor_references (account_id, room_id) VALUES (?, ?)")).
		WithArgs(int64(2), "8").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO room_monitor_transitions (sequence, event_kind, room_id, lease_epoch, from_state, to_state, confirmed_at, grace_until, new_broadcast, account_ids_json) VALUES (?, 'room_references_changed', ?, NULL, NULL, NULL, NULL, NULL, NULL, ?)")).
		WithArgs(uint64(42), "8", []byte("[2]")).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(regexp.QuoteMeta("UPDATE room_monitor_outbox_tail SET next_sequence = ? WHERE id = 1 AND next_sequence = ?")).
		WithArgs(uint64(43), uint64(42)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	if _, err := repository.SyncReferences(context.Background(), []Reference{{AccountID: 2, RoomID: "8"}, {AccountID: 1, RoomID: "7"}}, nil); err != nil {
		t.Fatalf("SyncReferences() error = %v", err)
	}
	assertRepositoryExpectations(t, mock)
}

// This test fails if an offline-to-live transition is committed without a
// locked monitor state, a distinct business broadcast, or durable fencing.
func TestRepositoryRecordTransitionCreatesBusinessBroadcastAndDurableOutbox(t *testing.T) {
	repository, mock, closeDB := newMockRepository(t)
	defer closeDB()
	confirmedAt := time.Date(2026, 8, 25, 12, 0, 0, 123456000, time.UTC)

	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta("SELECT next_sequence FROM room_monitor_outbox_tail WHERE id = 1 FOR UPDATE")).
		WillReturnRows(sqlmock.NewRows([]string{"next_sequence"}).AddRow(uint64(42)))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT state, grace_until, broadcast_session_id, lease_epoch FROM room_monitor_states WHERE room_id = ? FOR UPDATE")).
		WithArgs("7").
		WillReturnRows(sqlmock.NewRows([]string{"state", "grace_until", "broadcast_session_id", "lease_epoch"}).AddRow("offline", nil, nil, uint64(7)))
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO broadcast_sessions (room_id, started_at) VALUES (?, ?)")).
		WithArgs("7", confirmedAt).
		WillReturnResult(sqlmock.NewResult(99, 1))
	mock.ExpectExec(regexp.QuoteMeta("UPDATE room_monitor_states SET state = ?, grace_until = NULL, broadcast_session_id = ?, lease_epoch = ?, updated_at = ? WHERE room_id = ? AND lease_epoch = ?")).
		WithArgs(StateLive, int64(99), uint64(8), confirmedAt, "7", uint64(7)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO room_monitor_transitions (sequence, event_kind, room_id, lease_epoch, from_state, to_state, confirmed_at, grace_until, new_broadcast, account_ids_json) VALUES (?, 'room_state_changed', ?, ?, ?, ?, ?, ?, ?, NULL)")).
		WithArgs(uint64(42), "7", uint64(8), StateOffline, StateLive, confirmedAt, nil, true).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(regexp.QuoteMeta("UPDATE room_monitor_outbox_tail SET next_sequence = ? WHERE id = 1 AND next_sequence = ?")).
		WithArgs(uint64(43), uint64(42)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	got, err := repository.RecordTransition(context.Background(), Transition{RoomID: "7", From: StateOffline, To: StateLive, ConfirmedAt: confirmedAt, NewBroadcast: true})
	if err != nil {
		t.Fatalf("RecordTransition() error = %v", err)
	}
	if got.Sequence != 42 || got.RoomStateChanged == nil || got.RoomStateChanged.LeaseEpoch != 8 || !got.RoomStateChanged.NewBroadcast || got.RoomStateChanged.To != StateLive {
		t.Fatalf("RecordTransition() = %#v, want durable live transition", got)
	}
	assertRepositoryExpectations(t, mock)
}

// This test fails if a last-reference removal can commit independently from
// its terminal state/broadcast close and leave a recoverable active room.
func TestRepositorySyncReferencesCommitsFinalReferenceRemovalAndTerminalTransitionTogether(t *testing.T) {
	repository, mock, closeDB := newMockRepository(t)
	defer closeDB()
	confirmedAt := time.Date(2026, 8, 25, 12, 2, 0, 0, time.UTC)

	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta("SELECT next_sequence FROM room_monitor_outbox_tail WHERE id = 1 FOR UPDATE")).
		WillReturnRows(sqlmock.NewRows([]string{"next_sequence"}).AddRow(uint64(42)))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT account_id, room_id FROM room_monitor_references ORDER BY room_id, account_id FOR UPDATE")).
		WillReturnRows(sqlmock.NewRows([]string{"account_id", "room_id"}).AddRow(int64(1), "7"))
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO room_monitor_states (room_id) VALUES (?) ON DUPLICATE KEY UPDATE room_id = VALUES(room_id)")).
		WithArgs("7").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(regexp.QuoteMeta("DELETE FROM room_monitor_references")).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO room_monitor_transitions (sequence, event_kind, room_id, lease_epoch, from_state, to_state, confirmed_at, grace_until, new_broadcast, account_ids_json) VALUES (?, 'room_references_changed', ?, NULL, NULL, NULL, NULL, NULL, NULL, ?)")).
		WithArgs(uint64(42), "7", []byte("[]")).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(regexp.QuoteMeta("SELECT state, grace_until, broadcast_session_id, lease_epoch FROM room_monitor_states WHERE room_id = ? FOR UPDATE")).
		WithArgs("7").
		WillReturnRows(sqlmock.NewRows([]string{"state", "grace_until", "broadcast_session_id", "lease_epoch"}).AddRow("live", nil, int64(99), uint64(7)))
	mock.ExpectExec(regexp.QuoteMeta("UPDATE broadcast_sessions SET ended_at = ? WHERE id = ? AND ended_at IS NULL")).
		WithArgs(confirmedAt, int64(99)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(regexp.QuoteMeta("UPDATE room_monitor_states SET state = ?, grace_until = NULL, broadcast_session_id = ?, lease_epoch = ?, updated_at = ? WHERE room_id = ? AND lease_epoch = ?")).
		WithArgs(StateOffline, nil, uint64(8), confirmedAt, "7", uint64(7)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO room_monitor_transitions (sequence, event_kind, room_id, lease_epoch, from_state, to_state, confirmed_at, grace_until, new_broadcast, account_ids_json) VALUES (?, 'room_state_changed', ?, ?, ?, ?, ?, ?, ?, NULL)")).
		WithArgs(uint64(43), "7", uint64(8), StateLive, StateOffline, confirmedAt, nil, false).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(regexp.QuoteMeta("UPDATE room_monitor_outbox_tail SET next_sequence = ? WHERE id = 1 AND next_sequence = ?")).
		WithArgs(uint64(44), uint64(42)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	got, err := repository.SyncReferences(context.Background(), nil, []Transition{{RoomID: "7", From: StateLive, To: StateOffline, ConfirmedAt: confirmedAt}})
	if err != nil {
		t.Fatalf("SyncReferences() error = %v; SQL expectations = %v", err, mock.ExpectationsWereMet())
	}
	if len(got) != 2 || got[0].Sequence != 42 || got[0].RoomReferencesChanged == nil || len(got[0].RoomReferencesChanged.AccountIDs) != 0 || got[1].Sequence != 43 || got[1].RoomStateChanged == nil || got[1].RoomStateChanged.LeaseEpoch != 8 || got[1].RoomStateChanged.To != StateOffline {
		t.Fatalf("SyncReferences() durable terminal transitions = %#v, want fenced offline transition", got)
	}
	assertRepositoryExpectations(t, mock)
}

// This test fails if a caller can label an offline-to-live boundary as a
// recovery and thereby suppress the business-session boundary downstream.
func TestRepositoryRejectsLiveBoundaryWithWrongBroadcastMarker(t *testing.T) {
	repository, mock, closeDB := newMockRepository(t)
	defer closeDB()
	transition := Transition{RoomID: "7", From: StateOffline, To: StateLive, ConfirmedAt: time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)}

	if _, err := repository.RecordTransition(context.Background(), transition); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("RecordTransition() error = %v, want ErrInvalidInput", err)
	}
	assertRepositoryExpectations(t, mock)
}

// This test fails if a caller can attach a grace deadline to a state that is
// already live or terminal, allowing an impossible durable receipt to escape.
func TestRepositoryRejectsGraceDeadlineOutsideGraceBeforeTransaction(t *testing.T) {
	confirmedAt := time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC)
	graceUntil := confirmedAt.Add(time.Minute)
	for _, transition := range []Transition{
		{RoomID: "7", From: StateOffline, To: StateLive, ConfirmedAt: confirmedAt, GraceUntil: &graceUntil, NewBroadcast: true},
		{RoomID: "7", From: StateLive, To: StateOffline, ConfirmedAt: confirmedAt, GraceUntil: &graceUntil},
	} {
		repository, mock, closeDB := newMockRepository(t)
		if _, err := repository.RecordTransition(context.Background(), transition); !errors.Is(err, ErrInvalidInput) {
			closeDB()
			t.Fatalf("RecordTransition(%s -> %s) error = %v, want ErrInvalidInput", transition.From, transition.To, err)
		}
		assertRepositoryExpectations(t, mock)
		closeDB()
	}
}

func TestRepositorySyncReferencesRejectsTerminalGraceDeadlineBeforeTransaction(t *testing.T) {
	repository, mock, closeDB := newMockRepository(t)
	defer closeDB()
	confirmedAt := time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC)
	graceUntil := confirmedAt.Add(time.Minute)
	if _, err := repository.SyncReferences(context.Background(), nil, []Transition{{RoomID: "7", From: StateLive, To: StateOffline, ConfirmedAt: confirmedAt, GraceUntil: &graceUntil}}); !errors.Is(err, ErrInvalidInput) {
		t.Fatalf("SyncReferences() error = %v, want ErrInvalidInput", err)
	}
	assertRepositoryExpectations(t, mock)
}

// This test fails if consumers cannot recover every coalesced notification in
// the same global order in which the transaction persisted it.
func TestRepositoryReplayEventsReturnsMixedDurableSequenceOrder(t *testing.T) {
	repository, mock, closeDB := newMockRepository(t)
	defer closeDB()
	confirmedAt := time.Date(2026, 8, 25, 12, 1, 0, 0, time.UTC)
	mock.ExpectQuery(regexp.QuoteMeta("SELECT sequence, event_kind, room_id, lease_epoch, from_state, to_state, confirmed_at, grace_until, new_broadcast, account_ids_json FROM room_monitor_transitions WHERE sequence > ? ORDER BY sequence LIMIT ?")).
		WithArgs(uint64(40), 2).
		WillReturnRows(sqlmock.NewRows([]string{"sequence", "event_kind", "room_id", "lease_epoch", "from_state", "to_state", "confirmed_at", "grace_until", "new_broadcast", "account_ids_json"}).
			AddRow(uint64(41), "room_references_changed", "7", nil, nil, nil, nil, nil, nil, []byte("[1,2]")).
			AddRow(uint64(42), "room_state_changed", "7", uint64(9), "live", "grace", confirmedAt.Add(time.Minute), confirmedAt.Add(11*time.Minute), false, nil))

	got, err := repository.ReplayEvents(context.Background(), 40, 2)
	if err != nil {
		t.Fatalf("ReplayEvents() error = %v", err)
	}
	if len(got) != 2 || got[0].Sequence != 41 || got[0].RoomReferencesChanged == nil || !slices.Equal(got[0].RoomReferencesChanged.AccountIDs, []int64{1, 2}) || got[1].Sequence != 42 || got[1].RoomStateChanged == nil || got[1].RoomStateChanged.To != StateGrace || got[1].RoomStateChanged.GraceUntil == nil {
		t.Fatalf("ReplayEvents() = %#v, want ordered mixed events", got)
	}
	assertRepositoryExpectations(t, mock)
}

// This test fails if a corrupt outbox row can publish a transition shape that
// could never be returned as an immediate durable RecordTransition receipt.
func TestRepositoryReplayEventsRejectsImpossibleGraceShape(t *testing.T) {
	repository, mock, closeDB := newMockRepository(t)
	defer closeDB()
	confirmedAt := time.Date(2026, 8, 26, 12, 1, 0, 0, time.UTC)
	mock.ExpectQuery(regexp.QuoteMeta("SELECT sequence, event_kind, room_id, lease_epoch, from_state, to_state, confirmed_at, grace_until, new_broadcast, account_ids_json FROM room_monitor_transitions WHERE sequence > ? ORDER BY sequence LIMIT ?")).
		WithArgs(uint64(0), 1).
		WillReturnRows(sqlmock.NewRows([]string{"sequence", "event_kind", "room_id", "lease_epoch", "from_state", "to_state", "confirmed_at", "grace_until", "new_broadcast", "account_ids_json"}).
			AddRow(uint64(42), "room_state_changed", "7", uint64(8), "offline", "live", confirmedAt, confirmedAt.Add(time.Minute), true, nil))
	if _, err := repository.ReplayEvents(context.Background(), 0, 1); !errors.Is(err, ErrRepositoryUnavailable) {
		t.Fatalf("ReplayEvents() error = %v, want ErrRepositoryUnavailable", err)
	}
	assertRepositoryExpectations(t, mock)
}

// This test fails if untrusted cursors can request an allocation beyond the
// bounded replay contract before repository code validates the limit.
func TestRepositoryReplayEventsRejectsOutOfRangeLimitBeforeQuery(t *testing.T) {
	repository, mock, closeDB := newMockRepository(t)
	defer closeDB()
	for _, limit := range []int{0, MaxReplayLimit + 1, int(^uint(0) >> 1)} {
		if _, err := repository.ReplayEvents(context.Background(), 0, limit); !errors.Is(err, ErrInvalidInput) {
			t.Fatalf("ReplayEvents(limit=%d) error = %v, want ErrInvalidInput", limit, err)
		}
	}
	assertRepositoryExpectations(t, mock)
}

// This test fails if restart recovery loses the reference set or the persisted
// grace deadline needed to resume one shared watcher per canonical room.
func TestRepositoryLoadRecoverableGroupsReferencesByRoom(t *testing.T) {
	repository, mock, closeDB := newMockRepository(t)
	defer closeDB()
	graceUntil := time.Date(2026, 8, 25, 12, 10, 0, 0, time.UTC)
	mock.ExpectQuery(regexp.QuoteMeta("SELECT s.room_id, s.state, s.grace_until, s.lease_epoch, r.account_id FROM room_monitor_states AS s JOIN room_monitor_references AS r ON r.room_id = s.room_id WHERE s.state IN ('live', 'grace') ORDER BY s.room_id, r.account_id")).
		WillReturnRows(sqlmock.NewRows([]string{"room_id", "state", "grace_until", "lease_epoch", "account_id"}).
			AddRow("7", "grace", graceUntil, uint64(9), int64(1)).
			AddRow("7", "grace", graceUntil, uint64(9), int64(2)))

	got, err := repository.LoadRecoverable(context.Background())
	if err != nil {
		t.Fatalf("LoadRecoverable() error = %v", err)
	}
	if len(got) != 1 || got[0].RoomID != "7" || got[0].State != StateGrace || got[0].GraceUntil == nil || !got[0].GraceUntil.Equal(graceUntil) || len(got[0].References) != 2 {
		t.Fatalf("LoadRecoverable() = %#v, want one grace watcher with both references", got)
	}
	assertRepositoryExpectations(t, mock)
}

func newMockRepository(t *testing.T) (*sqlRepository, sqlmock.Sqlmock, func()) {
	t.Helper()
	db, mock, err := sqlmock.New()
	if err != nil {
		t.Fatal(err)
	}
	return NewRepository(db), mock, func() { _ = db.Close() }
}

func assertRepositoryExpectations(t *testing.T, mock sqlmock.Sqlmock) {
	t.Helper()
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}
