package runtime

import (
	"context"
	"sync"
)

// operationGate is a context-aware FIFO mutex. Its zero value is ready for
// use. A canceled waiter either leaves the queue before handoff or immediately
// hands an already-granted turn to the next waiter.
type operationGate struct {
	mu      sync.Mutex
	held    bool
	waiters []*operationGateWaiter
}

type operationGateWaiter struct {
	ready   chan struct{}
	granted bool
}

func (gate *operationGate) Acquire(ctx context.Context) error {
	if ctx == nil {
		return ErrInvalidInput
	}
	if err := ctx.Err(); err != nil {
		return err
	}

	waiter := &operationGateWaiter{ready: make(chan struct{})}
	gate.mu.Lock()
	if !gate.held && len(gate.waiters) == 0 {
		gate.held = true
		gate.mu.Unlock()
		if err := ctx.Err(); err != nil {
			gate.Release()
			return err
		}
		return nil
	}
	gate.waiters = append(gate.waiters, waiter)
	gate.mu.Unlock()

	select {
	case <-waiter.ready:
		if err := ctx.Err(); err != nil {
			gate.Release()
			return err
		}
		return nil
	case <-ctx.Done():
		gate.mu.Lock()
		if waiter.granted {
			gate.mu.Unlock()
			gate.Release()
			return ctx.Err()
		}
		for index, queued := range gate.waiters {
			if queued == waiter {
				gate.waiters = append(gate.waiters[:index], gate.waiters[index+1:]...)
				break
			}
		}
		gate.mu.Unlock()
		return ctx.Err()
	}
}

func (gate *operationGate) Release() {
	gate.mu.Lock()
	defer gate.mu.Unlock()
	if !gate.held {
		panic("runtime: release of unlocked operation gate")
	}
	if len(gate.waiters) == 0 {
		gate.held = false
		return
	}
	waiter := gate.waiters[0]
	gate.waiters = gate.waiters[1:]
	waiter.granted = true
	close(waiter.ready)
}
