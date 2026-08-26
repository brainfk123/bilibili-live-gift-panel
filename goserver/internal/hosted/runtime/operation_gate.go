package runtime

import (
	"context"
	"sync"
)

// operationGate is a context-aware FIFO mutex. Its zero value is ready for
// use. A canceled waiter either leaves the queue before handoff or immediately
// hands an already-granted turn to the next waiter.
type operationGate struct {
	mu         sync.Mutex
	held       bool
	generation uint64
	owner      uint64
	waiters    []*operationGateWaiter
}

type operationGateWaiter struct {
	ready      chan struct{}
	granted    bool
	generation uint64
}

type operationPermit struct {
	gate       *operationGate
	generation uint64
	released   bool
	mu         sync.Mutex
}

func (gate *operationGate) Acquire(ctx context.Context) (*operationPermit, error) {
	if ctx == nil {
		return nil, ErrInvalidInput
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}

	waiter := &operationGateWaiter{ready: make(chan struct{})}
	gate.mu.Lock()
	if !gate.held && len(gate.waiters) == 0 {
		gate.generation++
		gate.held = true
		gate.owner = gate.generation
		permit := &operationPermit{gate: gate, generation: gate.owner}
		gate.mu.Unlock()
		if err := ctx.Err(); err != nil {
			_ = permit.Release()
			return nil, err
		}
		return permit, nil
	}
	gate.waiters = append(gate.waiters, waiter)
	gate.mu.Unlock()

	select {
	case <-waiter.ready:
		permit := &operationPermit{gate: gate, generation: waiter.generation}
		if err := ctx.Err(); err != nil {
			_ = permit.Release()
			return nil, err
		}
		return permit, nil
	case <-ctx.Done():
		gate.mu.Lock()
		if waiter.granted {
			gate.mu.Unlock()
			_ = (&operationPermit{gate: gate, generation: waiter.generation}).Release()
			return nil, ctx.Err()
		}
		for index, queued := range gate.waiters {
			if queued == waiter {
				gate.waiters = append(gate.waiters[:index], gate.waiters[index+1:]...)
				break
			}
		}
		gate.mu.Unlock()
		return nil, ctx.Err()
	}
}

func (permit *operationPermit) Release() error {
	if permit == nil || permit.gate == nil {
		return ErrInvalidInput
	}
	permit.mu.Lock()
	defer permit.mu.Unlock()
	if permit.released {
		return ErrUnavailable
	}
	gate := permit.gate
	gate.mu.Lock()
	defer gate.mu.Unlock()
	if !gate.held || gate.owner != permit.generation {
		return ErrUnavailable
	}
	permit.released = true
	if len(gate.waiters) == 0 {
		gate.held = false
		gate.owner = 0
		return nil
	}
	waiter := gate.waiters[0]
	gate.waiters = gate.waiters[1:]
	gate.generation++
	gate.owner = gate.generation
	waiter.granted = true
	waiter.generation = gate.owner
	close(waiter.ready)
	return nil
}
