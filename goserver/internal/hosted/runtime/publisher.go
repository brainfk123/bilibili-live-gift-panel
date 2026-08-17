package runtime

import (
	"encoding/json"
	"sync"

	"bilibili-live-gift-panel/internal/gameplay"
	"bilibili-live-gift-panel/internal/hosted/configuration"
)

// DisplaySnapshot is published only after its matching durable transaction
// commits. Viewer rows are ephemeral and are never repository input.
type DisplaySnapshot struct {
	AccountID     int64                      `json:"accountId"`
	LiveSessionID int64                      `json:"liveSessionId"`
	Revision      uint64                     `json:"revision"`
	Runtime       configuration.RuntimeState `json:"runtime"`
	Effects       []gameplay.Effect          `json:"effects,omitempty"`
	Viewers       []ViewerRow                `json:"viewers,omitempty"`
}

type SnapshotPublisher interface {
	Publish(DisplaySnapshot)
}

type sessionSnapshotCleaner interface {
	Clear(accountID, liveSessionID int64)
}

type Publisher struct {
	mu          sync.Mutex
	latest      map[int64]DisplaySnapshot
	subscribers map[int64]map[uint64]*PublisherSubscription
	nextID      uint64
}

type PublisherSubscription struct {
	publisher *Publisher
	accountID int64
	id        uint64
	events    chan DisplaySnapshot
	once      sync.Once
}

func NewPublisher() *Publisher {
	return &Publisher{latest: make(map[int64]DisplaySnapshot), subscribers: make(map[int64]map[uint64]*PublisherSubscription)}
}

func (publisher *Publisher) Publish(snapshot DisplaySnapshot) {
	if publisher == nil || snapshot.AccountID <= 0 {
		return
	}
	detached := cloneDisplaySnapshot(snapshot)
	publisher.mu.Lock()
	defer publisher.mu.Unlock()
	publisher.latest[snapshot.AccountID] = detached
	for _, subscription := range publisher.subscribers[snapshot.AccountID] {
		select {
		case subscription.events <- cloneDisplaySnapshot(detached):
		default:
		}
	}
}

func (publisher *Publisher) Subscribe(accountID int64) (*PublisherSubscription, error) {
	if publisher == nil || accountID <= 0 {
		return nil, ErrInvalidInput
	}
	publisher.mu.Lock()
	defer publisher.mu.Unlock()
	publisher.nextID++
	if publisher.nextID == 0 {
		return nil, ErrUnavailable
	}
	subscription := &PublisherSubscription{publisher: publisher, accountID: accountID, id: publisher.nextID, events: make(chan DisplaySnapshot, 16)}
	if publisher.subscribers[accountID] == nil {
		publisher.subscribers[accountID] = make(map[uint64]*PublisherSubscription)
	}
	publisher.subscribers[accountID][subscription.id] = subscription
	return subscription, nil
}

func (publisher *Publisher) Latest(accountID int64) (DisplaySnapshot, bool) {
	if publisher == nil || accountID <= 0 {
		return DisplaySnapshot{}, false
	}
	publisher.mu.Lock()
	defer publisher.mu.Unlock()
	snapshot, ok := publisher.latest[accountID]
	return cloneDisplaySnapshot(snapshot), ok
}

// Clear removes only snapshots owned by the exact ended session and wipes
// ephemeral viewer identity before releasing publisher-owned references.
func (publisher *Publisher) Clear(accountID, liveSessionID int64) {
	if publisher == nil || accountID <= 0 || liveSessionID <= 0 {
		return
	}
	publisher.mu.Lock()
	defer publisher.mu.Unlock()
	if snapshot, ok := publisher.latest[accountID]; ok && snapshot.LiveSessionID == liveSessionID {
		wipeDisplaySnapshot(&snapshot)
		delete(publisher.latest, accountID)
	}
	for _, subscription := range publisher.subscribers[accountID] {
		retained := make([]DisplaySnapshot, 0, len(subscription.events))
	drain:
		for {
			select {
			case snapshot := <-subscription.events:
				if snapshot.LiveSessionID == liveSessionID {
					wipeDisplaySnapshot(&snapshot)
					continue
				}
				retained = append(retained, snapshot)
			default:
				break drain
			}
		}
		for _, snapshot := range retained {
			subscription.events <- snapshot
		}
	}
}

func (subscription *PublisherSubscription) Events() <-chan DisplaySnapshot {
	if subscription == nil {
		return nil
	}
	return subscription.events
}

func (subscription *PublisherSubscription) Cancel() {
	if subscription == nil || subscription.publisher == nil {
		return
	}
	subscription.once.Do(func() {
		subscription.publisher.mu.Lock()
		defer subscription.publisher.mu.Unlock()
		delete(subscription.publisher.subscribers[subscription.accountID], subscription.id)
		close(subscription.events)
	})
}

func cloneDisplaySnapshot(snapshot DisplaySnapshot) DisplaySnapshot {
	encoded, err := json.Marshal(snapshot)
	if err != nil {
		return DisplaySnapshot{}
	}
	var detached DisplaySnapshot
	if json.Unmarshal(encoded, &detached) != nil {
		return DisplaySnapshot{}
	}
	return detached
}

func wipeDisplaySnapshot(snapshot *DisplaySnapshot) {
	if snapshot == nil {
		return
	}
	for index := range snapshot.Viewers {
		snapshot.Viewers[index] = ViewerRow{}
	}
	snapshot.Viewers = nil
}
