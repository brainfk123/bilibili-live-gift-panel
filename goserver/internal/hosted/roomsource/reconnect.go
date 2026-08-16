package roomsource

import (
	"time"

	"bilibili-live-gift-panel/internal/hosted/biligateway"
)

// Timer is the cancelable reconnect-timer seam. Tests inject manually fired
// timers; production uses time.Timer.
type Timer interface {
	C() <-chan time.Time
	Stop() bool
}

type systemTimer struct{ timer *time.Timer }

func newSystemTimer(delay time.Duration) Timer { return systemTimer{timer: time.NewTimer(delay)} }
func (timer systemTimer) C() <-chan time.Time  { return timer.timer.C }
func (timer systemTimer) Stop() bool           { return timer.timer.Stop() }

func (source *roomSource) supervise(connection *managedConnection) {
	for connection != nil && connection.Connection != nil {
		select {
		case <-source.ctx.Done():
			return
		case <-connection.Done():
		}
		cause := connection.Err()

		manager := source.manager
		manager.mu.Lock()
		if source.closed || source.connection != connection || len(source.subscribers) == 0 {
			manager.mu.Unlock()
			return
		}
		source.connection = nil
		source.generation++
		source.healthySince = time.Time{}
		manager.mu.Unlock()
		connection = source.reconnect(cause)
	}
}

func (source *roomSource) reconnect(cause error) *managedConnection {
	delay := source.nextReconnectDelay(cause)
	for {
		if !source.wait(delay) {
			return nil
		}
		if source.manager.gateway.Status().EgressOpen {
			delay = source.currentReconnectDelay()
			continue
		}
		generation, ok := source.beginOpen()
		if !ok {
			return nil
		}
		connection, err := source.manager.gateway.OpenRoom(
			biligateway.WithAccount(source.ctx, source.accountID), source.roomID,
			func(event biligateway.Event) { source.accept(generation, event) },
		)
		if err == nil && connection == nil {
			err = biligateway.ErrEgressUnavailable
		}
		if err != nil {
			manager := source.manager
			manager.mu.Lock()
			if source.generation == generation {
				source.generation++
			}
			closed := source.closed
			manager.mu.Unlock()
			source.opens.Done()
			if closed {
				return nil
			}
			delay = source.nextReconnectDelay(err)
			continue
		}
		wrapped := &managedConnection{Connection: connection}
		manager := source.manager
		manager.mu.Lock()
		if source.closed || len(source.subscribers) == 0 || source.ctx.Err() != nil || source.generation != generation {
			source.connection = wrapped
			manager.mu.Unlock()
			source.opens.Done()
			return nil
		}
		source.connection = wrapped
		source.healthySince = time.Time{}
		manager.mu.Unlock()
		source.opens.Done()
		return wrapped
	}
}

func (source *roomSource) nextReconnectDelay(err error) time.Duration {
	manager := source.manager
	manager.mu.Lock()
	attempt := source.attempts
	source.attempts++
	manager.mu.Unlock()
	delay := manager.backoff(attempt)
	if retryAfter, ok := biligateway.RetryAfter(err); ok && retryAfter > delay {
		delay = retryAfter
	}
	return delay
}

func (source *roomSource) currentReconnectDelay() time.Duration {
	manager := source.manager
	manager.mu.Lock()
	attempt := source.attempts - 1
	manager.mu.Unlock()
	if attempt < 0 {
		attempt = 0
	}
	return manager.backoff(attempt)
}

func (source *roomSource) wait(delay time.Duration) bool {
	timer := source.manager.newTimer(delay)
	select {
	case <-source.ctx.Done():
		timer.Stop()
		return false
	case <-timer.C():
		timer.Stop()
		return true
	}
}
