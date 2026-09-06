package runtime

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"bilibili-live-gift-panel/internal/gameplay"
	"bilibili-live-gift-panel/internal/hosted/biligateway"
	"bilibili-live-gift-panel/internal/hosted/configuration"
	"bilibili-live-gift-panel/internal/hosted/roomsource"
)

func TestHostedCapacitySharesSevenRoomsAcrossTenIsolatedAccounts(t *testing.T) {
	now := time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC)
	targets := map[int64]string{
		1: "100", 2: "100", 3: "100",
		4: "200", 5: "200",
		6: "300", 7: "400", 8: "500", 9: "600", 10: "700",
	}
	sessions := newCapacitySessionStore(targets, now)
	repository := newCapacityRepository(targets, now)
	gateway := newCapacityGateway()
	sources := roomsource.NewManager(gateway, roomsource.Options{Now: func() time.Time { return now }, NewTimer: newCapacityRoomTimer})
	publisher := NewPublisher()
	processorFactory, err := NewProcessorFactory(repository, publisher, ProcessorOptions{Now: func() time.Time { return now }, NewRetryTimer: newCapacityTimer})
	if err != nil {
		t.Fatal(err)
	}
	manager, err := NewManager(
		Dependencies{Sessions: sessions, Configuration: repository, Migration: fakeMigration{}, RoomSources: sources},
		Options{
			Now: func() time.Time { return now }, NewTimer: newCapacityTimer,
			NewHeartbeatTimer: newCapacityTimer, NewShutdownTimer: newCapacityTimer,
			ProcessorFactory: processorFactory, OwnerToken: ownerToken(0xc6),
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		shutdownContext, cancelShutdown := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancelShutdown()
		if err := manager.Shutdown(shutdownContext); err != nil {
			t.Errorf("shutdown: %v", err)
		}
	})

	leases := make([]ConnectionLease, 0, len(targets))
	for accountID := int64(1); accountID <= 10; accountID++ {
		lease, acquireErr := manager.Acquire(context.Background(), accountID, LeaseConfig)
		if acquireErr != nil {
			t.Fatalf("acquire account %d: %v", accountID, acquireErr)
		}
		leases = append(leases, lease)
	}
	defer func() {
		for _, lease := range leases {
			lease.Release()
		}
	}()

	if got := gateway.openCount(); got != 7 {
		t.Fatalf("upstream room opens = %d, want exactly 7 for 10 accounts", got)
	}
	if got := gateway.openCountFor("100"); got != 1 {
		t.Fatalf("three accounts sharing room 100 opened %d upstreams, want 1", got)
	}

	for _, roomID := range []string{"100", "200", "300", "400", "500", "600", "700"} {
		for index := 1; index <= 100; index++ {
			gateway.emit(roomID, biligateway.Event{Type: "message", Data: []byte(fmt.Sprintf(
				`{"cmd":"SEND_GIFT","data":{"rnd":"%s-%03d","giftId":1,"num":1,"price":1000,"timestamp":1786896000,"uid":987654321,"uname":"viewer-secret","face":"https://secret-avatar"}}`,
				roomID, index,
			))})
		}
	}
	waitContext, cancelWait := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelWait()
	for accountID := int64(1); accountID <= 10; accountID++ {
		select {
		case <-repository.done(accountID):
		case <-waitContext.Done():
			t.Fatalf("account %d did not receive all room events: %v", accountID, waitContext.Err())
		}
	}

	for accountID := int64(1); accountID <= 10; accountID++ {
		commands, state := repository.capture(accountID)
		if len(commands) != 100 {
			t.Fatalf("account %d persisted %d events, want 100", accountID, len(commands))
		}
		for index, command := range commands {
			if command.AccountID != accountID || command.LiveSessionID != accountID || command.ConfigVersionID != 1000+accountID {
				t.Fatalf("account %d command %d crossed tenant/session/config scope: %+v", accountID, index, command)
			}
			if command.ExpectedRevision != uint64(index+1) {
				t.Fatalf("account %d command %d revision = %d, want %d", accountID, index, command.ExpectedRevision, index+1)
			}
		}
		if got := state.Runtime.AttributeValues["score"]; got != 100 {
			t.Fatalf("account %d score = %v, want 100", accountID, got)
		}
		status, statusErr := manager.Status(context.Background(), accountID)
		if statusErr != nil {
			t.Fatal(statusErr)
		}
		if status.State != StateActive || status.RoomID != targets[accountID] || status.PersistenceBuffered != 0 {
			t.Fatalf("account %d status not active, isolated, and drained: %+v", accountID, status)
		}
		account, accountErr := manager.accountExisting(accountID)
		if accountErr != nil {
			t.Fatal(accountErr)
		}
		account.mu.Lock()
		queueDepth, queueCapacity := len(account.current.events), cap(account.current.events)
		account.mu.Unlock()
		if queueCapacity != 256 || queueDepth > queueCapacity {
			t.Fatalf("account %d runtime queue depth/capacity = %d/%d, want a bounded 256-event queue", accountID, queueDepth, queueCapacity)
		}
		snapshot := waitForCapacitySnapshot(t, publisher, accountID)
		if snapshot.AccountID != accountID || snapshot.LiveSessionID != accountID || len(snapshot.Viewers) != 1 || snapshot.Viewers[0].Name != "viewer-secret" || snapshot.Viewers[0].Gifts != 100 {
			t.Fatalf("account %d process-local display snapshot missing isolated viewer: %+v", accountID, snapshot)
		}
	}

	durable, err := json.Marshal(repository.allCommands())
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"viewer-secret", "secret-avatar", "987654321"} {
		if strings.Contains(string(durable), secret) {
			t.Fatalf("durable runtime commands captured viewer identity %q", secret)
		}
	}
}

func waitForCapacitySnapshot(t *testing.T, publisher *Publisher, accountID int64) DisplaySnapshot {
	t.Helper()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for {
		snapshot, ok := publisher.Latest(accountID)
		if ok && len(snapshot.Viewers) == 1 && snapshot.Viewers[0].Gifts == 100 {
			return snapshot
		}
		select {
		case <-deadline.C:
			t.Fatalf("account %d process-local display snapshot did not reach 100 gifts: %+v", accountID, snapshot)
		case <-ticker.C:
		}
	}
}

type capacityTimer struct{ events chan time.Time }

func newCapacityTimer(time.Duration) Timer { return &capacityTimer{events: make(chan time.Time)} }
func newCapacityRoomTimer(time.Duration) roomsource.Timer {
	return &capacityTimer{events: make(chan time.Time)}
}
func (timer *capacityTimer) C() <-chan time.Time { return timer.events }
func (*capacityTimer) Stop() bool                { return true }

type capacitySessionStore struct {
	mu       sync.Mutex
	targets  map[int64]string
	now      time.Time
	owners   map[int64]OwnerFence
	sessions map[int64]Session
}

func newCapacitySessionStore(targets map[int64]string, now time.Time) *capacitySessionStore {
	return &capacitySessionStore{targets: targets, now: now, owners: make(map[int64]OwnerFence), sessions: make(map[int64]Session)}
}

func (store *capacitySessionStore) ClaimOwnership(_ context.Context, accountID int64, token OwnerToken, _ time.Duration) (OwnerClaim, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	fence := OwnerFence{AccountID: accountID, Token: token, Epoch: 1}
	store.owners[accountID] = fence
	return OwnerClaim{Fence: fence}, nil
}

func (store *capacitySessionStore) RenewOwnership(_ context.Context, fence OwnerFence, _ time.Duration) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.owners[fence.AccountID] != fence {
		return ErrOwnershipConflict
	}
	return nil
}

func (store *capacitySessionStore) ReleaseOwnership(_ context.Context, fence OwnerFence) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	if current := store.owners[fence.AccountID]; current != (OwnerFence{}) && current != fence {
		return ErrOwnershipConflict
	}
	delete(store.owners, fence.AccountID)
	return nil
}

func (store *capacitySessionStore) TargetRoom(_ context.Context, accountID int64) (string, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	roomID, ok := store.targets[accountID]
	if !ok {
		return "", ErrNoTargetRoom
	}
	return roomID, nil
}

func (store *capacitySessionStore) PersistTargetRoom(_ context.Context, command PersistTargetRoomCommand) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.owners[command.Owner.AccountID] != command.Owner {
		return ErrOwnershipConflict
	}
	store.targets[command.Owner.AccountID] = command.RoomID
	return nil
}

func (store *capacitySessionStore) StartSession(_ context.Context, command StartSessionCommand) (Session, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.owners[command.AccountID] != command.Owner {
		return Session{}, ErrOwnershipConflict
	}
	session := Session{ID: command.AccountID, AccountID: command.AccountID, RoomID: command.RoomID, ConfigVersionID: command.ConfigVersionID, StartedAt: store.now}
	store.sessions[command.AccountID] = session
	return session, nil
}

func (store *capacitySessionStore) EndSession(_ context.Context, command EndSessionCommand) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.owners[command.AccountID] != command.Owner {
		return ErrOwnershipConflict
	}
	delete(store.sessions, command.AccountID)
	return nil
}

func (store *capacitySessionStore) ReconcileSession(_ context.Context, command ReconcileSessionCommand) error {
	store.mu.Lock()
	defer store.mu.Unlock()
	if store.owners[command.AccountID] == command.LostOwner {
		return ErrOwnershipConflict
	}
	if session, ok := store.sessions[command.AccountID]; ok && session.ID == command.SessionID {
		delete(store.sessions, command.AccountID)
	}
	return nil
}

func (*capacitySessionStore) PendingMigration(context.Context, int64) (int64, bool, error) {
	return 0, false, nil
}

type capacityRepository struct {
	mu       sync.Mutex
	versions map[int64]configuration.Version
	states   map[int64]configuration.State
	commands map[int64][]configuration.RuntimeEventCommand
	dones    map[int64]chan struct{}
}

func newCapacityRepository(targets map[int64]string, now time.Time) *capacityRepository {
	repository := &capacityRepository{
		versions: make(map[int64]configuration.Version), states: make(map[int64]configuration.State),
		commands: make(map[int64][]configuration.RuntimeEventCommand), dones: make(map[int64]chan struct{}),
	}
	for accountID := range targets {
		repository.versions[accountID] = configuration.Version{
			ID: 1000 + accountID, AccountID: accountID, Number: 1, Source: "capacity", CreatedAt: now,
			Definition: configuration.Definition{
				Attributes: []configuration.AttributeDefinition{{ID: "score", Name: "score"}},
				Rules:      []gameplay.Rule{{ID: "score", GiftID: 1, AttributeID: "score", Formula: "score+1"}},
				Gifts:      []configuration.GiftDefinition{{ID: 1, Name: "rose", Price: 1000, CoinType: "gold"}},
			},
		}
		repository.states[accountID] = configuration.State{
			AccountID: accountID, ConfigVersionID: 1000 + accountID, Revision: 1, UpdatedAt: now,
			Runtime: configuration.RuntimeState{AttributeValues: map[string]float64{"score": 0}, RuleLimits: gameplay.RuleLimitState{LocalDate: "2026-08-17", AppliedCounts: map[string]int{}}},
		}
		repository.dones[accountID] = make(chan struct{})
	}
	return repository
}

func (repository *capacityRepository) LoadActive(_ context.Context, accountID int64) (configuration.Version, configuration.State, error) {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	version, ok := repository.versions[accountID]
	if !ok {
		return configuration.Version{}, configuration.State{}, ErrUnavailable
	}
	return version, cloneCapacityState(repository.states[accountID]), nil
}

func (repository *capacityRepository) CommitRuntimeEvent(_ context.Context, command configuration.RuntimeEventCommand) (configuration.RuntimeEventResult, error) {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	state, ok := repository.states[command.AccountID]
	if !ok || command.ConfigVersionID != state.ConfigVersionID || command.ExpectedRevision != state.Revision {
		return configuration.RuntimeEventResult{}, configuration.ErrRevisionConflict
	}
	result := configuration.RuntimeEventResult{Revision: state.Revision + 1}
	state.Revision = result.Revision
	state.Runtime = cloneCapacityRuntime(command.Runtime)
	state.UpdatedAt = command.UpdatedAt
	repository.states[command.AccountID] = state
	repository.commands[command.AccountID] = append(repository.commands[command.AccountID], command)
	if len(repository.commands[command.AccountID]) == 100 {
		close(repository.dones[command.AccountID])
	}
	return result, nil
}

func (repository *capacityRepository) done(accountID int64) <-chan struct{} {
	return repository.dones[accountID]
}

func (repository *capacityRepository) capture(accountID int64) ([]configuration.RuntimeEventCommand, configuration.State) {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	return append([]configuration.RuntimeEventCommand(nil), repository.commands[accountID]...), cloneCapacityState(repository.states[accountID])
}

func (repository *capacityRepository) allCommands() []configuration.RuntimeEventCommand {
	repository.mu.Lock()
	defer repository.mu.Unlock()
	commands := make([]configuration.RuntimeEventCommand, 0, 1000)
	for accountID := int64(1); accountID <= 10; accountID++ {
		commands = append(commands, repository.commands[accountID]...)
	}
	return commands
}

func cloneCapacityState(state configuration.State) configuration.State {
	state.Runtime = cloneCapacityRuntime(state.Runtime)
	return state
}

func cloneCapacityRuntime(state configuration.RuntimeState) configuration.RuntimeState {
	attributes := make(map[string]float64, len(state.AttributeValues))
	for key, value := range state.AttributeValues {
		attributes[key] = value
	}
	counts := make(map[string]int, len(state.RuleLimits.AppliedCounts))
	for key, value := range state.RuleLimits.AppliedCounts {
		counts[key] = value
	}
	state.AttributeValues = attributes
	state.RuleLimits.AppliedCounts = counts
	return state
}

type capacityGateway struct {
	mu          sync.Mutex
	connections map[string]*capacityConnection
	opens       map[string]int
}

func newCapacityGateway() *capacityGateway {
	return &capacityGateway{connections: make(map[string]*capacityConnection), opens: make(map[string]int)}
}

func (*capacityGateway) RoomInfo(_ context.Context, roomID string) (biligateway.RoomInfo, error) {
	return biligateway.RoomInfo{RoomID: roomID, CanonicalRoomID: roomID}, nil
}
func (*capacityGateway) GiftCatalog(context.Context, string) ([]gameplay.GiftInfo, error) {
	return nil, nil
}
func (gateway *capacityGateway) OpenRoom(_ context.Context, roomID string, sink biligateway.Sink) (biligateway.Connection, error) {
	gateway.mu.Lock()
	defer gateway.mu.Unlock()
	connection := &capacityConnection{sink: sink, done: make(chan struct{})}
	gateway.connections[roomID] = connection
	gateway.opens[roomID]++
	return connection, nil
}
func (*capacityGateway) Status() biligateway.Status { return biligateway.Status{} }

func (gateway *capacityGateway) emit(roomID string, event biligateway.Event) {
	gateway.mu.Lock()
	connection := gateway.connections[roomID]
	gateway.mu.Unlock()
	if connection == nil {
		panic("capacity gateway emitted before open: " + roomID)
	}
	connection.sink(event)
}

func (gateway *capacityGateway) openCount() int {
	gateway.mu.Lock()
	defer gateway.mu.Unlock()
	total := 0
	for _, count := range gateway.opens {
		total += count
	}
	return total
}
func (gateway *capacityGateway) openCountFor(roomID string) int {
	gateway.mu.Lock()
	defer gateway.mu.Unlock()
	return gateway.opens[roomID]
}

type capacityConnection struct {
	mu     sync.Mutex
	sink   biligateway.Sink
	done   chan struct{}
	closed bool
}

func (connection *capacityConnection) Close() error {
	connection.mu.Lock()
	defer connection.mu.Unlock()
	if !connection.closed {
		connection.closed = true
		close(connection.done)
	}
	return nil
}
func (connection *capacityConnection) Done() <-chan struct{} { return connection.done }
func (*capacityConnection) Err() error                       { return nil }
