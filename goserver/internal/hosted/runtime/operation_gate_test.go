package runtime

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestOperationGateRejectsCanceledAcquireWithoutTakingOwnership(t *testing.T) {
	var gate operationGate
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := gate.Acquire(canceled); !errors.Is(err, context.Canceled) {
		t.Fatalf("Acquire(canceled) error = %v, want context canceled", err)
	}
	fresh, cancelFresh := context.WithTimeout(context.Background(), time.Second)
	defer cancelFresh()
	permit, err := gate.Acquire(fresh)
	if err != nil {
		t.Fatalf("canceled acquire leaked gate ownership: %v", err)
	}
	_ = permit.Release()
}

func TestOperationGateHandsOffQueuedWaitersInArrivalOrder(t *testing.T) {
	var gate operationGate
	owner, err := gate.Acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	acquired := make(chan int, 3)
	release := make(chan struct{})
	for waiter := 0; waiter < 3; waiter++ {
		go func(waiter int) {
			permit, err := gate.Acquire(context.Background())
			if err != nil {
				acquired <- -1
				return
			}
			acquired <- waiter
			<-release
			_ = permit.Release()
		}(waiter)
		waitForOperationGateQueue(t, &gate, waiter+1)
	}
	_ = owner.Release()
	for want := 0; want < 3; want++ {
		select {
		case got := <-acquired:
			if got != want {
				t.Fatalf("gate acquisition order = %d at position %d", got, want)
			}
		case <-time.After(time.Second):
			t.Fatalf("queued waiter %d did not acquire", want)
		}
		release <- struct{}{}
	}
}

func TestOperationGateCanceledWaiterDoesNotConsumeLaterRelease(t *testing.T) {
	var gate operationGate
	owner, err := gate.Acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	waitContext, cancelWait := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() { _, err := gate.Acquire(waitContext); result <- err }()
	waitForOperationGateQueue(t, &gate, 1)
	cancelWait()
	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatalf("queued Acquire error = %v, want context canceled", err)
	}
	_ = owner.Release()
	fresh, cancelFresh := context.WithTimeout(context.Background(), time.Second)
	defer cancelFresh()
	permit, err := gate.Acquire(fresh)
	if err != nil {
		t.Fatalf("canceled waiter consumed the later release: %v", err)
	}
	_ = permit.Release()
}

func TestOperationGateCancelReleaseRaceNeverLeaksOwnership(t *testing.T) {
	for iteration := 0; iteration < 100; iteration++ {
		var gate operationGate
		owner, err := gate.Acquire(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		waitContext, cancelWait := context.WithCancel(context.Background())
		type acquireResult struct {
			permit *operationPermit
			err    error
		}
		result := make(chan acquireResult, 1)
		go func() { permit, err := gate.Acquire(waitContext); result <- acquireResult{permit: permit, err: err} }()
		waitForOperationGateQueue(t, &gate, 1)
		canceled := make(chan struct{})
		released := make(chan struct{})
		go func() {
			cancelWait()
			close(canceled)
		}()
		go func() {
			_ = owner.Release()
			close(released)
		}()
		got := <-result
		err = got.err
		<-canceled
		<-released
		if err == nil {
			_ = got.permit.Release()
		} else if !errors.Is(err, context.Canceled) {
			t.Fatalf("iteration %d Acquire error = %v", iteration, err)
		}
		fresh, cancelFresh := context.WithTimeout(context.Background(), time.Second)
		freshPermit, err := gate.Acquire(fresh)
		if err != nil {
			cancelFresh()
			t.Fatalf("iteration %d leaked ownership: %v", iteration, err)
		}
		cancelFresh()
		_ = freshPermit.Release()
	}
}

func TestOperationGateRejectsUnbalancedRelease(t *testing.T) {
	var gate operationGate
	permit, err := gate.Acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := permit.Release(); err != nil {
		t.Fatal(err)
	}
	if err := permit.Release(); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("second Release error = %v, want unavailable", err)
	}

}

func TestOperationGateDoubleReleaseCannotWakeSecondQueuedWaiter(t *testing.T) {
	var gate operationGate
	owner, err := gate.Acquire(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	type acquiredPermit struct {
		name   string
		permit *operationPermit
	}
	acquired := make(chan acquiredPermit, 2)
	for index, name := range []string{"B", "C"} {
		go func(name string) {
			permit, err := gate.Acquire(context.Background())
			if err != nil {
				acquired <- acquiredPermit{name: "error"}
				return
			}
			acquired <- acquiredPermit{name: name, permit: permit}
		}(name)
		waitForOperationGateQueue(t, &gate, index+1)
	}
	if err := owner.Release(); err != nil {
		t.Fatal(err)
	}
	first := <-acquired
	if first.name != "B" {
		t.Fatalf("first acquired waiter = %q, want B", first.name)
	}
	if err := owner.Release(); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("stale second Release error = %v, want unavailable", err)
	}
	select {
	case next := <-acquired:
		t.Fatalf("second waiter %q entered before B released", next.name)
	case <-time.After(20 * time.Millisecond):
	}
	if err := first.permit.Release(); err != nil {
		t.Fatal(err)
	}
	second := <-acquired
	if second.name != "C" {
		t.Fatalf("second acquired waiter = %q, want C", second.name)
	}
	if err := second.permit.Release(); err != nil {
		t.Fatal(err)
	}
}

func waitForOperationGateQueue(t *testing.T, gate *operationGate, want int) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for {
		gate.mu.Lock()
		got := len(gate.waiters)
		gate.mu.Unlock()
		if got == want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("queued waiters = %d, want %d", got, want)
		}
		time.Sleep(time.Millisecond)
	}
}
