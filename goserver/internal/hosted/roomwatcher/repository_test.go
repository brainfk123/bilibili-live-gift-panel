package roomwatcher

import (
	"context"
	"errors"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
)

// This test fails if a reference snapshot can leave an old room transition
// unlocked while the snapshot is being replaced.
func TestRepositorySyncReferencesLocksFormerAndNextRoomsBeforeReplacingSnapshot(t *testing.T) {
	repository, mock, closeDB := newMockRepository(t)
	defer closeDB()

	mock.ExpectBegin()
	mock.ExpectQuery(regexp.QuoteMeta("SELECT DISTINCT room_id FROM room_monitor_references FOR UPDATE")).
		WillReturnRows(sqlmock.NewRows([]string{"room_id"}).AddRow("7"))
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
	mock.ExpectCommit()

	if err := repository.SyncReferences(context.Background(), []Reference{{AccountID: 2, RoomID: "8"}, {AccountID: 1, RoomID: "7"}}); err != nil {
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
	mock.ExpectQuery(regexp.QuoteMeta("SELECT state, grace_until, broadcast_session_id, lease_epoch FROM room_monitor_states WHERE room_id = ? FOR UPDATE")).
		WithArgs("7").
		WillReturnRows(sqlmock.NewRows([]string{"state", "grace_until", "broadcast_session_id", "lease_epoch"}).AddRow("offline", nil, nil, uint64(7)))
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO broadcast_sessions (room_id, started_at) VALUES (?, ?)")).
		WithArgs("7", confirmedAt).
		WillReturnResult(sqlmock.NewResult(99, 1))
	mock.ExpectExec(regexp.QuoteMeta("UPDATE room_monitor_states SET state = ?, grace_until = NULL, broadcast_session_id = ?, lease_epoch = ?, updated_at = ? WHERE room_id = ? AND lease_epoch = ?")).
		WithArgs(StateLive, int64(99), uint64(8), confirmedAt, "7", uint64(7)).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(regexp.QuoteMeta("INSERT INTO room_monitor_transitions (room_id, lease_epoch, from_state, to_state, confirmed_at, grace_until, new_broadcast) VALUES (?, ?, ?, ?, ?, ?, ?)")).
		WithArgs("7", uint64(8), StateOffline, StateLive, confirmedAt, nil, true).
		WillReturnResult(sqlmock.NewResult(42, 1))
	mock.ExpectCommit()

	got, err := repository.RecordTransition(context.Background(), Transition{RoomID: "7", From: StateOffline, To: StateLive, ConfirmedAt: confirmedAt, NewBroadcast: true})
	if err != nil {
		t.Fatalf("RecordTransition() error = %v", err)
	}
	if got.Sequence != 42 || got.LeaseEpoch != 8 || !got.NewBroadcast || got.To != StateLive {
		t.Fatalf("RecordTransition() = %#v, want durable live transition", got)
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

// This test fails if consumers cannot recover every coalesced notification in
// the same global order in which the transaction persisted it.
func TestRepositoryReplayTransitionsReturnsDurableSequenceOrder(t *testing.T) {
	repository, mock, closeDB := newMockRepository(t)
	defer closeDB()
	confirmedAt := time.Date(2026, 8, 25, 12, 1, 0, 0, time.UTC)
	mock.ExpectQuery(regexp.QuoteMeta("SELECT sequence, room_id, lease_epoch, from_state, to_state, confirmed_at, grace_until, new_broadcast FROM room_monitor_transitions WHERE sequence > ? ORDER BY sequence LIMIT ?")).
		WithArgs(uint64(40), 2).
		WillReturnRows(sqlmock.NewRows([]string{"sequence", "room_id", "lease_epoch", "from_state", "to_state", "confirmed_at", "grace_until", "new_broadcast"}).
			AddRow(uint64(41), "7", uint64(8), "offline", "live", confirmedAt, nil, true).
			AddRow(uint64(42), "7", uint64(9), "live", "grace", confirmedAt.Add(time.Minute), confirmedAt.Add(11*time.Minute), false))

	got, err := repository.ReplayTransitions(context.Background(), 40, 2)
	if err != nil {
		t.Fatalf("ReplayTransitions() error = %v", err)
	}
	if len(got) != 2 || got[0].Sequence != 41 || got[1].Sequence != 42 || got[1].To != StateGrace {
		t.Fatalf("ReplayTransitions() = %#v, want ordered durable transitions", got)
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
