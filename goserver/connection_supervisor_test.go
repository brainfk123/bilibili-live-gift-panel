package main

import (
	"context"
	"errors"
	"slices"
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
	supervisor.jitter = func(delay time.Duration) time.Duration { return delay }
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

func TestReconnectUsesFullBoundedScheduleAfterStableConnection(t *testing.T) {
	var attempts atomic.Int32
	connected := make(chan struct{})
	source := giftEventSourceFunc(func(ctx context.Context, _ string, callbacks runtimeCallbacks) error {
		switch attempts.Add(1) {
		case 1:
			return errors.New("initial failure")
		case 2:
			callbacks.onState("connected")
			return newConnectionFailure("read", errors.New("lost after connected"))
		case 8:
			return errors.New("retry failure")
		case 9:
			close(connected)
			<-ctx.Done()
			return ctx.Err()
		default:
			return errors.New("retry failure")
		}
	})
	var mu sync.Mutex
	var delays, jitterInputs []time.Duration
	supervisor := newConnectionSupervisor(func() giftEventSource { return source })
	supervisor.jitter = func(delay time.Duration) time.Duration {
		mu.Lock()
		jitterInputs = append(jitterInputs, delay)
		mu.Unlock()
		return delay
	}
	supervisor.wait = func(ctx context.Context, delay time.Duration) bool {
		mu.Lock()
		delays = append(delays, delay)
		mu.Unlock()
		return ctx.Err() == nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- supervisor.Run(ctx, "room-a", runtimeCallbacks{}) }()
	<-connected
	cancel()
	<-done
	mu.Lock()
	defer mu.Unlock()
	wantDelays := []time.Duration{0, 0, time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second, 16 * time.Second, 30 * time.Second}
	if !slices.Equal(delays, wantDelays) {
		t.Fatalf("delays = %v, want %v", delays, wantDelays)
	}
	wantJitter := []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second, 16 * time.Second, 30 * time.Second}
	if !slices.Equal(jitterInputs, wantJitter) {
		t.Fatalf("jitter inputs = %v, want %v", jitterInputs, wantJitter)
	}
}

func TestSupervisorDefaultJitterUsesInjectedEntropyWithinBounds(t *testing.T) {
	supervisor := newConnectionSupervisorWithEntropy(nil, func() uint64 { return 0 })
	got := supervisor.jitter(10 * time.Second)
	if got != 9*time.Second {
		t.Fatalf("default jitter with zero entropy = %s, want 9s", got)
	}
	if got <= 0 || got > 30*time.Second {
		t.Fatalf("default jitter = %s, outside safe range", got)
	}
}

func TestReconnectDelayClampsJitterToBoundedRange(t *testing.T) {
	if got := reconnectDelay(6, func(time.Duration) time.Duration { return time.Hour }); got != 30*time.Second {
		t.Fatalf("positive jitter delay = %s, want 30s cap", got)
	}
	if got := reconnectDelay(1, func(time.Duration) time.Duration { return -time.Second }); got != 0 {
		t.Fatalf("negative jitter delay = %s, want 0", got)
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

func TestReconnectRetriesHeartbeatFailureExactlyOnce(t *testing.T) {
	var attempts atomic.Int32
	retryStarted := make(chan struct{})
	source := giftEventSourceFunc(func(ctx context.Context, _ string, callbacks runtimeCallbacks) error {
		if attempts.Add(1) == 1 {
			return newConnectionFailure("heartbeat", errors.New("heartbeat write failed"))
		}
		close(retryStarted)
		<-ctx.Done()
		return ctx.Err()
	})
	supervisor := newConnectionSupervisor(func() giftEventSource { return source })
	supervisor.wait = func(ctx context.Context, _ time.Duration) bool { return ctx.Err() == nil }
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- supervisor.Run(ctx, "room-a", runtimeCallbacks{}) }()
	<-retryStarted
	if got := attempts.Load(); got != 2 {
		t.Fatalf("source attempts = %d, want one retry after heartbeat failure", got)
	}
	cancel()
	<-done
}

func TestReconnectRestartsAfterRealSocketHeartbeatFailure(t *testing.T) {
	var factories atomic.Int32
	restarted := make(chan struct{})
	supervisor := newConnectionSupervisor(func() giftEventSource {
		if factories.Add(1) == 1 {
			return giftEventSourceFunc(func(ctx context.Context, _ string, _ runtimeCallbacks) error {
				return (&bilibiliGiftSource{heartbeatInterval: time.Millisecond, readTimeout: time.Hour}).runSocket(
					ctx,
					&heartbeatFailingBiliSocket{closed: make(chan struct{})},
					roomInfo{}, biliSession{}, nil, nil, runtimeCallbacks{},
				)
			})
		}
		return giftEventSourceFunc(func(ctx context.Context, _ string, _ runtimeCallbacks) error {
			close(restarted)
			<-ctx.Done()
			return ctx.Err()
		})
	})
	supervisor.jitter = func(time.Duration) time.Duration { return 0 }
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- supervisor.Run(ctx, "room-a", runtimeCallbacks{}) }()
	<-restarted
	if got := factories.Load(); got != 2 {
		t.Fatalf("source factory calls = %d, want one restart after heartbeat failure", got)
	}
	cancel()
	<-done
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

func TestConnectionGapRetainsNewestSixteenAndLatestSafeCategory(t *testing.T) {
	supervisor := newConnectionSupervisor(nil)
	for attempt := 0; attempt < 17; attempt++ {
		at := time.UnixMilli(int64(1000 + attempt*100))
		supervisor.recordConnectionGap(at, newConnectionFailure("read", errors.New("first")))
		supervisor.recordConnectionGap(at.Add(time.Millisecond), newConnectionFailure("heartbeat", errors.New("latest")))
		supervisor.closeConnectionGap(at.Add(2 * time.Millisecond))
	}
	gaps := supervisor.Gaps()
	if len(gaps) != 16 || gaps[0].StartedAt != 1100 || gaps[15].StartedAt != 2600 {
		t.Fatalf("retained gaps = %#v", gaps)
	}
	if gaps[0].Attempts != 2 || gaps[0].ErrorKind != "heartbeat" {
		t.Fatalf("multi-attempt gap = %#v, want latest heartbeat category", gaps[0])
	}
}

type giftEventSourceFunc func(context.Context, string, runtimeCallbacks) error

func (fn giftEventSourceFunc) Run(ctx context.Context, roomID string, callbacks runtimeCallbacks) error {
	return fn(ctx, roomID, callbacks)
}
