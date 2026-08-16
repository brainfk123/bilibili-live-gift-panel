package roomsource

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"sync"
	"time"

	"bilibili-live-gift-panel/internal/hosted/biligateway"
)

var (
	ErrInvalidSubscription    = errors.New("invalid_subscription")
	ErrInvalidRoom            = biligateway.ErrInvalidRoom
	ErrSubscriberBackpressure = errors.New("subscriber_backpressure")
)

// Sink callbacks must return promptly and be safe for OnError to run
// concurrently with an in-flight OnEvent. Manager isolates subscribers, but
// Go cannot forcibly cancel arbitrary callback code that does not return.
type Sink interface {
	OnEvent(Event)
	OnError(error)
}

type Subscription interface {
	Cancel()
	Done() <-chan struct{}
	Wait(context.Context) error
}

type Options struct {
	Now      func() time.Time
	NewTimer func(time.Duration) Timer
	Jitter   func() float64
}

type Manager struct {
	gateway biligateway.Gateway

	mu            sync.Mutex
	rooms         map[string]*roomSource
	closed        bool
	nextSub       uint64
	now           func() time.Time
	newTimer      func(time.Duration) Timer
	backoff       biligateway.RetryBackoff
	launchInitial func(func())
	closing       chan struct{}
	done          chan struct{}
	subscriberWG  sync.WaitGroup
	sourceWG      sync.WaitGroup
}

type roomSource struct {
	manager      *Manager
	roomID       string
	accountID    int64
	ctx          context.Context
	cancel       context.CancelFunc
	connection   *managedConnection
	subscribers  map[uint64]*subscriber
	ready        chan struct{}
	closeDone    chan struct{}
	closeOnce    sync.Once
	opens        sync.WaitGroup
	workers      sync.WaitGroup
	openErr      error
	closed       bool
	generation   uint64
	attempts     int
	healthySince time.Time
}

type subscriber struct {
	source     *roomSource
	id         uint64
	sink       Sink
	events     chan Event
	stopCh     chan struct{}
	workerDone chan struct{}
	errorDone  chan struct{}
	done       chan struct{}
	once       sync.Once
}

type managedConnection struct {
	biligateway.Connection
	once sync.Once
}

func NewManager(gateway biligateway.Gateway, options Options) *Manager {
	if options.Now == nil {
		options.Now = time.Now
	}
	if options.NewTimer == nil {
		options.NewTimer = newSystemTimer
	}
	return &Manager{
		gateway: gateway, rooms: make(map[string]*roomSource), now: options.Now,
		newTimer: options.NewTimer, backoff: biligateway.NewRetryBackoff(options.Jitter),
		launchInitial: func(worker func()) { go worker() },
		closing:       make(chan struct{}), done: make(chan struct{}),
	}
}

func (manager *Manager) Subscribe(ctx context.Context, roomID string, accountID int64, sink Sink) (Subscription, error) {
	if manager == nil || manager.gateway == nil || ctx == nil || accountID <= 0 || sink == nil {
		return nil, ErrInvalidSubscription
	}
	info, err := manager.gateway.RoomInfo(biligateway.WithAccount(ctx, accountID), roomID)
	if err != nil {
		return nil, err
	}
	canonical, err := canonicalRoomID(info.CanonicalRoomID)
	if err != nil {
		return nil, err
	}

	var source *roomSource
	var subscription *subscriber
	created := false
	for {
		manager.mu.Lock()
		if manager.closed {
			manager.mu.Unlock()
			return nil, ErrInvalidSubscription
		}
		source = manager.rooms[canonical]
		if source != nil && source.closed {
			closeDone := source.closeDone
			manager.mu.Unlock()
			select {
			case <-closeDone:
				continue
			case <-manager.closing:
				return nil, ErrInvalidSubscription
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		created = source == nil
		if created {
			sourceContext, cancel := context.WithCancel(context.Background())
			source = &roomSource{
				manager: manager, roomID: canonical, accountID: accountID, ctx: sourceContext, cancel: cancel,
				subscribers: make(map[uint64]*subscriber), ready: make(chan struct{}), closeDone: make(chan struct{}),
			}
			manager.rooms[canonical] = source
			manager.sourceWG.Add(1)
			source.workers.Add(1)
		}
		manager.nextSub++
		subscription = &subscriber{
			source: source, id: manager.nextSub, sink: sink,
			events: make(chan Event, 256), stopCh: make(chan struct{}),
			workerDone: make(chan struct{}), errorDone: make(chan struct{}), done: make(chan struct{}),
		}
		source.subscribers[subscription.id] = subscription
		manager.subscriberWG.Add(1)
		manager.mu.Unlock()
		break
	}

	go subscription.run()
	go func() {
		select {
		case <-ctx.Done():
			subscription.Cancel()
		case <-subscription.done:
		}
	}()
	if created {
		manager.launchInitial(func() { manager.openInitial(source, accountID) })
	}
	select {
	case <-source.ready:
		manager.mu.Lock()
		openErr := source.openErr
		manager.mu.Unlock()
		if openErr != nil {
			subscription.stop(nil)
			return nil, openErr
		}
		return subscription, nil
	case <-manager.closing:
		subscription.Cancel()
		return nil, ErrInvalidSubscription
	case <-ctx.Done():
		subscription.Cancel()
		return nil, ctx.Err()
	}
}

func (manager *Manager) openInitial(source *roomSource, accountID int64) {
	defer source.workers.Done()
	generation, ok := source.beginOpen()
	if !ok {
		manager.mu.Lock()
		source.openErr = ErrInvalidSubscription
		close(source.ready)
		manager.mu.Unlock()
		return
	}
	defer source.opens.Done()
	connection, openErr := manager.gateway.OpenRoom(
		biligateway.WithAccount(source.ctx, accountID), source.roomID,
		func(event biligateway.Event) { source.accept(generation, event) },
	)
	if openErr == nil && connection == nil {
		openErr = biligateway.ErrEgressUnavailable
	}
	startSupervisor := false
	manager.mu.Lock()
	if openErr != nil {
		if source.generation == generation {
			source.generation++
		}
		source.openErr = openErr
		source.startClosingLocked()
		for id, subscription := range source.subscribers {
			delete(source.subscribers, id)
			subscription.stop(nil)
		}
	} else {
		wrapped := &managedConnection{Connection: connection}
		if source.closed {
			source.connection = wrapped
			source.openErr = ErrInvalidSubscription
		} else {
			source.connection = wrapped
			startSupervisor = true
		}
	}
	close(source.ready)
	manager.mu.Unlock()
	if startSupervisor {
		source.supervise(source.connection)
	}
}

func (source *roomSource) accept(generation uint64, input biligateway.Event) {
	event := eventFromGateway(source.roomID, input)
	manager := source.manager
	var backpressured []*subscriber
	manager.mu.Lock()
	if source.closed || source.generation != generation {
		manager.mu.Unlock()
		return
	}
	now := manager.now()
	if source.healthySince.IsZero() {
		source.healthySince = now
	} else if !now.Before(source.healthySince.Add(2 * time.Minute)) {
		source.attempts = 0
	}
	for id, subscription := range source.subscribers {
		select {
		case subscription.events <- cloneEvent(event):
		default:
			delete(source.subscribers, id)
			backpressured = append(backpressured, subscription)
		}
	}
	if len(source.subscribers) == 0 {
		source.startClosingLocked()
	}
	manager.mu.Unlock()
	for _, subscription := range backpressured {
		subscription.stop(ErrSubscriberBackpressure)
	}
}

func (subscription *subscriber) run() {
	defer close(subscription.workerDone)
	for {
		select {
		case <-subscription.stopCh:
			return
		default:
		}
		select {
		case event := <-subscription.events:
			select {
			case <-subscription.stopCh:
				return
			default:
			}
			subscription.sink.OnEvent(event)
		case <-subscription.stopCh:
			return
		}
	}
}

func (subscription *subscriber) stop(err error) {
	subscription.once.Do(func() {
		close(subscription.stopCh)
		if err != nil {
			go func() {
				defer close(subscription.errorDone)
				subscription.sink.OnError(err)
			}()
		} else {
			close(subscription.errorDone)
		}
		go func() {
			<-subscription.workerDone
			<-subscription.errorDone
			close(subscription.done)
			subscription.source.manager.subscriberWG.Done()
		}()
	})
}

func (subscription *subscriber) Done() <-chan struct{} {
	if subscription == nil {
		return nil
	}
	return subscription.done
}

func (subscription *subscriber) Wait(ctx context.Context) error {
	if subscription == nil || ctx == nil {
		return ErrInvalidSubscription
	}
	select {
	case <-subscription.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (source *roomSource) beginOpen() (uint64, bool) {
	manager := source.manager
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if source.closed || source.ctx.Err() != nil || len(source.subscribers) == 0 {
		return 0, false
	}
	source.opens.Add(1)
	source.generation++
	return source.generation, true
}

func (subscription *subscriber) Cancel() {
	if subscription == nil || subscription.source == nil {
		return
	}
	manager := subscription.source.manager
	manager.mu.Lock()
	if _, exists := subscription.source.subscribers[subscription.id]; exists {
		delete(subscription.source.subscribers, subscription.id)
		subscription.stop(nil)
	}
	if len(subscription.source.subscribers) == 0 && !subscription.source.closed {
		subscription.source.startClosingLocked()
	}
	manager.mu.Unlock()
}

// startClosingLocked stops admission and starts the one lifecycle closer. The
// canonical registry entry remains as a tombstone until finishClosing removes
// it after every in-flight open and the active connection close complete.
func (source *roomSource) startClosingLocked() {
	if source.closed {
		return
	}
	source.closed = true
	source.generation++
	source.cancel()
	source.closeOnce.Do(func() { go source.finishClosing() })
}

func (source *roomSource) finishClosing() {
	source.opens.Wait()
	source.workers.Wait()
	manager := source.manager
	manager.mu.Lock()
	connection := source.connection
	source.connection = nil
	manager.mu.Unlock()
	if connection != nil {
		connection.close()
	}
	manager.mu.Lock()
	if manager.rooms[source.roomID] == source {
		delete(manager.rooms, source.roomID)
	}
	close(source.closeDone)
	manager.mu.Unlock()
	manager.sourceWG.Done()
}

func (manager *Manager) Close() {
	if manager == nil {
		return
	}
	manager.mu.Lock()
	if manager.closed {
		manager.mu.Unlock()
		return
	}
	manager.closed = true
	close(manager.closing)
	for _, source := range manager.rooms {
		for _, subscription := range source.subscribers {
			subscription.stop(nil)
		}
		source.subscribers = nil
		source.startClosingLocked()
	}
	manager.mu.Unlock()
	go func() {
		manager.subscriberWG.Wait()
		manager.sourceWG.Wait()
		close(manager.done)
	}()
}

func (manager *Manager) Done() <-chan struct{} {
	if manager == nil {
		return nil
	}
	return manager.done
}

func (manager *Manager) Wait(ctx context.Context) error {
	if manager == nil || ctx == nil {
		return ErrInvalidSubscription
	}
	select {
	case <-manager.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (connection *managedConnection) close() {
	if connection == nil || connection.Connection == nil {
		return
	}
	connection.once.Do(func() { _ = connection.Connection.Close() })
}

func canonicalRoomID(input string) (string, error) {
	numeric, err := strconv.ParseUint(strings.TrimSpace(input), 10, 64)
	if err != nil || numeric == 0 {
		return "", ErrInvalidRoom
	}
	return strconv.FormatUint(numeric, 10), nil
}
