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
	if err := gate.Acquire(canceled); !errors.Is(err, context.Canceled) {
		t.Fatalf("Acquire(canceled) error = %v, want context canceled", err)
	}
	fresh, cancelFresh := context.WithTimeout(context.Background(), time.Second)
	defer cancelFresh()
	if err := gate.Acquire(fresh); err != nil {
		t.Fatalf("canceled acquire leaked gate ownership: %v", err)
	}
	gate.Release()
}

func TestOperationGateHandsOffQueuedWaitersInArrivalOrder(t *testing.T) {
	var gate operationGate
	if err := gate.Acquire(context.Background()); err != nil {
		t.Fatal(err)
	}
	acquired := make(chan int, 3)
	release := make(chan struct{})
	for waiter := 0; waiter < 3; waiter++ {
		go func(waiter int) {
			if err := gate.Acquire(context.Background()); err != nil {
				acquired <- -1
				return
			}
			acquired <- waiter
			<-release
			gate.Release()
		}(waiter)
		waitForOperationGateQueue(t, &gate, waiter+1)
	}
	gate.Release()
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
	if err := gate.Acquire(context.Background()); err != nil {
		t.Fatal(err)
	}
	waitContext, cancelWait := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() { result <- gate.Acquire(waitContext) }()
	waitForOperationGateQueue(t, &gate, 1)
	cancelWait()
	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatalf("queued Acquire error = %v, want context canceled", err)
	}
	gate.Release()
	fresh, cancelFresh := context.WithTimeout(context.Background(), time.Second)
	defer cancelFresh()
	if err := gate.Acquire(fresh); err != nil {
		t.Fatalf("canceled waiter consumed the later release: %v", err)
	}
	gate.Release()
}

func TestOperationGateCancelReleaseRaceNeverLeaksOwnership(t *testing.T) {
	for iteration := 0; iteration < 100; iteration++ {
		var gate operationGate
		if err := gate.Acquire(context.Background()); err != nil {
			t.Fatal(err)
		}
		waitContext, cancelWait := context.WithCancel(context.Background())
		result := make(chan error, 1)
		go func() { result <- gate.Acquire(waitContext) }()
		waitForOperationGateQueue(t, &gate, 1)
		canceled := make(chan struct{})
		released := make(chan struct{})
		go func() {
			cancelWait()
			close(canceled)
		}()
		go func() {
			gate.Release()
			close(released)
		}()
		err := <-result
		<-canceled
		<-released
		if err == nil {
			gate.Release()
		} else if !errors.Is(err, context.Canceled) {
			t.Fatalf("iteration %d Acquire error = %v", iteration, err)
		}
		fresh, cancelFresh := context.WithTimeout(context.Background(), time.Second)
		if err := gate.Acquire(fresh); err != nil {
			cancelFresh()
			t.Fatalf("iteration %d leaked ownership: %v", iteration, err)
		}
		cancelFresh()
		gate.Release()
	}
}

func TestOperationGateRejectsUnbalancedRelease(t *testing.T) {
	var gate operationGate
	if err := gate.Acquire(context.Background()); err != nil {
		t.Fatal(err)
	}
	gate.Release()
	defer func() {
		if recover() == nil {
			t.Fatal("second Release did not reject unbalanced ownership")
		}
	}()
	gate.Release()
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
