package runtime

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
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

type PersistTargetRoomCommand struct {
	Owner     OwnerFence
	RoomID    string
	UpdatedAt time.Time
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
}

func NewSessionRepository(database *sql.DB) *SessionRepository {
	return &SessionRepository{db: database, verificationTimeout: 2 * time.Second}
}

func (repository *SessionRepository) AccountEnabled(ctx context.Context, accountID int64) (bool, error) {
	if !repository.ready() || ctx == nil || accountID <= 0 {
		return false, ErrInvalidInput
	}
	var enabled bool
	err := repository.db.QueryRowContext(ctx, "SELECT disabled_at IS NULL FROM streamer_accounts WHERE id = ?", accountID).Scan(&enabled)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
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

func (repository *SessionRepository) EnabledAccountsForRoom(ctx context.Context, roomID string) ([]int64, error) {
	if !repository.ready() || ctx == nil || !validRoomID(roomID) {
		return nil, ErrInvalidInput
	}
	rows, err := repository.db.QueryContext(ctx, "SELECT r.account_id FROM account_runtime_rooms AS r JOIN streamer_accounts AS a ON a.id = r.account_id AND a.disabled_at IS NULL WHERE r.room_id = ? ORDER BY r.account_id", roomID)
	if err != nil {
		return nil, ErrUnavailable
	}
	defer rows.Close()
	accounts := make([]int64, 0)
	for rows.Next() {
		var accountID int64
		if err := rows.Scan(&accountID); err != nil || accountID <= 0 {
			return nil, ErrUnavailable
		}
		accounts = append(accounts, accountID)
	}
	if err := rows.Err(); err != nil {
		return nil, ErrUnavailable
	}
	return accounts, nil
}

func (repository *SessionRepository) PersistTargetRoom(ctx context.Context, command PersistTargetRoomCommand) error {
	if !repository.ready() || ctx == nil || !validOwnerFence(command.Owner) || !validRoomID(command.RoomID) || command.UpdatedAt.IsZero() {
		return ErrInvalidInput
	}
	command.UpdatedAt = normalizeDatabaseTime(command.UpdatedAt)
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
	if err := lockEnabledOwnerAccount(ctx, transaction, command.Owner.AccountID); err != nil {
		return err
	}
	if err := validateOwnerFence(ctx, transaction, command.Owner); err != nil {
		return err
	}
	result, err := transaction.ExecContext(ctx, "INSERT INTO account_runtime_rooms (account_id, room_id, updated_at) VALUES (?, ?, ?) ON DUPLICATE KEY UPDATE room_id = VALUES(room_id), updated_at = VALUES(updated_at)", command.Owner.AccountID, command.RoomID, command.UpdatedAt)
	if err != nil || !atLeastOneAffected(result) {
		return ErrUnavailable
	}
	if err := transaction.Commit(); err != nil {
		finished = true
		return ErrUnavailable
	}
	finished = true
	return nil
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
	active.session, active.subscription = session, subscription
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
	defer close(active.workerDone)
	for event := range active.events {
		if err := active.processor.Accept(event); err != nil {
			if errors.Is(err, ErrOwnershipConflict) {
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
	account.mu.Lock()
	active := account.current
	if active == nil {
		active = account.transitionPending
	}
	account.mu.Unlock()
	if active == nil {
		return nil
	}
	if err := manager.stopActive(ctx, account, active); err != nil {
		if errors.Is(err, ErrOwnershipConflict) {
			manager.completeDrainedOwnershipConflict(account, active)
		}
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

func (manager *Manager) completeDrainedOwnershipConflict(account *accountRuntime, active *activeSession) {
	account.mu.Lock()
	defer account.mu.Unlock()
	if account.stale || account.current != active {
		return
	}
	account.stale = true
	account.leases = make(map[uint64]LeaseKind)
	account.cancelIdleLocked()
	account.current = nil
	account.owner = OwnerFence{}
	account.reconcile = false
	account.stale = false
	account.degraded = false
}

func (manager *Manager) stopActive(ctx context.Context, account *accountRuntime, active *activeSession) error {
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
	active.admissionMu.Lock()
	if active.endedAt.IsZero() {
		active.endedAt = normalizeDatabaseTime(manager.now())
	}
	endedAt := active.endedAt
	active.admissionMu.Unlock()
	account.mu.Lock()
	if account.stale {
		account.mu.Unlock()
		return ErrOwnershipConflict
	}
	owner := active.owner
	account.mu.Unlock()
	if err := manager.dependencies.Sessions.EndSession(ctx, EndSessionCommand{Owner: owner, AccountID: account.accountID, SessionID: active.session.ID, EndedAt: endedAt}); err != nil {
		return err
	}
	if finalizer, ok := active.processor.(sessionPublisherFinalizer); ok {
		finalizer.FinalizeSession()
	}
	return nil
}
