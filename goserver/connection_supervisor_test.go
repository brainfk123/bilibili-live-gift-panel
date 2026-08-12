package main

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type scriptedConnectionSource struct {
	mu       sync.Mutex
	attempts int
	failures int
}

func (source *scriptedConnectionSource) Run(ctx context.Context, _ string, callbacks runtimeCallbacks) error {
	source.mu.Lock()
	source.attempts++
	attempt := source.attempts
	source.mu.Unlock()
	if attempt <= source.failures {
		return errors.New("dial failed")
	}
	callbacks.onState("connected")
	<-ctx.Done()
	return ctx.Err()
}

func TestReconnectAttemptsImmediatelyThenUsesBoundedBackoff(t *testing.T) {
	source := &scriptedConnectionSource{failures: 3}
	var delays []time.Duration
	connected := make(chan struct{})
	supervisor := newConnectionSupervisor(func() giftEventSource { return source })
	supervisor.wait = func(ctx context.Context, delay time.Duration) bool {
		delays = append(delays, delay)
		return ctx.Err() == nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- supervisor.Run(ctx, "room-a", runtimeCallbacks{onState: func(state string) {
			if state == "connected" {
				close(connected)
			}
		}})
	}()
	<-connected
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("Run error = %v, want context cancellation", err)
	}
	if len(delays) != 3 || delays[0] != 0 || delays[1] != time.Second || delays[2] != 2*time.Second {
		t.Fatalf("reconnect delays = %v, want [0s 1s 2s]", delays)
	}
	for _, delay := range delays {
		if delay > 30*time.Second {
			t.Fatalf("delay = %s, exceeds 30s", delay)
		}
	}
}

func TestReconnectResetsBackoffAfterConnected(t *testing.T) {
	if got := reconnectDelay(100, nil); got != 30*time.Second {
		t.Fatalf("capped delay = %s, want 30s", got)
	}

	var calls int
	connected := make(chan struct{})
	source := giftEventSourceFunc(func(ctx context.Context, _ string, callbacks runtimeCallbacks) error {
		calls++
		switch calls {
		case 1:
			return errors.New("initial failure")
		case 2:
			callbacks.onState("connected")
			<-connected
			return errors.New("read failed")
		default:
			<-ctx.Done()
			return ctx.Err()
		}
	})
	var delayMu sync.Mutex
	var delays []time.Duration
	delayRecorded := make(chan struct{}, 2)
	supervisor := newConnectionSupervisor(func() giftEventSource { return source })
	supervisor.wait = func(ctx context.Context, delay time.Duration) bool {
		delayMu.Lock()
		delays = append(delays, delay)
		delayMu.Unlock()
		select {
		case delayRecorded <- struct{}{}:
		default:
		}
		return ctx.Err() == nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- supervisor.Run(ctx, "room-a", runtimeCallbacks{onState: func(state string) {
			if state == "connected" {
				close(connected)
			}
		}})
	}()
	<-delayRecorded
	<-delayRecorded
	cancel()
	<-done
	delayMu.Lock()
	defer delayMu.Unlock()
	if len(delays) < 2 || delays[0] != 0 || delays[1] != 0 {
		t.Fatalf("delays after connected reset = %v, want [0s 0s]", delays)
	}
}

func TestReconnectCancelStopsOldAttemptLoop(t *testing.T) {
	source := &scriptedConnectionSource{failures: 100}
	startedWait := make(chan struct{})
	releaseWait := make(chan struct{})
	supervisor := newConnectionSupervisor(func() giftEventSource { return source })
	supervisor.wait = func(ctx context.Context, _ time.Duration) bool {
		close(startedWait)
		select {
		case <-ctx.Done():
			return false
		case <-releaseWait:
			return true
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- supervisor.Run(ctx, "room-a", runtimeCallbacks{}) }()
	<-startedWait
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("Run error = %v, want context cancellation", err)
	}
	source.mu.Lock()
	attempts := source.attempts
	source.mu.Unlock()
	close(releaseWait)
	time.Sleep(10 * time.Millisecond)
	source.mu.Lock()
	defer source.mu.Unlock()
	if source.attempts != attempts {
		t.Fatalf("old loop made a new attempt after cancellation: %d -> %d", attempts, source.attempts)
	}
}

func TestConnectionGapRecordsSafeCategoryAndBoundsHistory(t *testing.T) {
	var nowMS atomic.Int64
	nowMS.Store(1000)
	thirdConnected := make(chan struct{})
	var attempts atomic.Int32
	supervisor := newConnectionSupervisor(func() giftEventSource {
		return giftEventSourceFunc(func(ctx context.Context, _ string, callbacks runtimeCallbacks) error {
			attempt := attempts.Add(1)
			switch attempt {
			case 1:
				callbacks.onState("connected")
				nowMS.Store(1100)
				return newConnectionFailure("read", errors.New("remote details must not be exposed"))
			case 2:
				nowMS.Store(1300)
				callbacks.onState("connected")
				nowMS.Store(1400)
				return newConnectionFailure("heartbeat", errors.New("secret"))
			default:
				nowMS.Store(1600)
				callbacks.onState("connected")
				close(thirdConnected)
				<-ctx.Done()
				return ctx.Err()
			}
		})
	})
	supervisor.now = func() time.Time { return time.UnixMilli(nowMS.Load()) }
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- supervisor.Run(ctx, "room-a", runtimeCallbacks{}) }()
	<-thirdConnected
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("Run error = %v, want context cancellation", err)
	}
	gaps := supervisor.Gaps()
	if len(gaps) != 2 {
		t.Fatalf("gaps = %#v, want two gaps", gaps)
	}
	if gaps[0].StartedAt != 1100 || gaps[0].EndedAt != 1300 || gaps[0].DurationMS != 200 || gaps[0].Attempts != 1 || gaps[0].ErrorKind != "read" {
		t.Fatalf("first gap = %#v", gaps[0])
	}
	if gaps[1].StartedAt != 1400 || gaps[1].EndedAt != 1600 || gaps[1].DurationMS != 200 || gaps[1].Attempts != 1 || gaps[1].ErrorKind != "heartbeat" {
		t.Fatalf("second gap = %#v", gaps[1])
	}
}

type giftEventSourceFunc func(context.Context, string, runtimeCallbacks) error

func (fn giftEventSourceFunc) Run(ctx context.Context, roomID string, callbacks runtimeCallbacks) error {
	return fn(ctx, roomID, callbacks)
}
