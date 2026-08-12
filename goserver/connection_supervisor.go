package main

import (
	"context"
	"errors"
	"sync"
	"time"
)

const defaultConnectionGapLimit = 16

type connectionGap struct {
	StartedAt  int64  `json:"startedAt"`
	EndedAt    int64  `json:"endedAt,omitempty"`
	DurationMS int64  `json:"durationMs,omitempty"`
	Attempts   int    `json:"attempts"`
	ErrorKind  string `json:"errorKind"`
}

type connectionFailure struct {
	kind string
	err  error
}

func (failure *connectionFailure) Error() string {
	if failure == nil || failure.err == nil {
		return "connection failure"
	}
	return failure.err.Error()
}

func (failure *connectionFailure) Unwrap() error { return failure.err }

func newConnectionFailure(kind string, err error) error {
	return &connectionFailure{kind: kind, err: err}
}

func connectionFailureKind(err error) string {
	var failure *connectionFailure
	if errors.As(err, &failure) && failure.kind != "" {
		return failure.kind
	}
	return "connection"
}

func reconnectDelay(failureCount int, jitter func(time.Duration) time.Duration) time.Duration {
	base := []time.Duration{0, time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second, 16 * time.Second, 30 * time.Second}
	if failureCount < 0 {
		failureCount = 0
	}
	delay := base[min(failureCount, len(base)-1)]
	if delay == 0 || jitter == nil {
		return delay
	}
	return jitter(delay)
}

// connectionSupervisor is the sole owner that creates and starts gift sources.
// The caller cancels its context to replace a room connection.
type connectionSupervisor struct {
	sourceFactory func() giftEventSource
	wait          func(context.Context, time.Duration) bool
	now           func() time.Time
	maxGaps       int
	onGap         func([]connectionGap)
	onFailure     func(error)

	mu   sync.RWMutex
	gaps []connectionGap
}

func newConnectionSupervisor(sourceFactory func() giftEventSource) *connectionSupervisor {
	return &connectionSupervisor{
		sourceFactory: sourceFactory,
		wait: func(ctx context.Context, delay time.Duration) bool {
			timer := time.NewTimer(delay)
			defer timer.Stop()
			select {
			case <-ctx.Done():
				return false
			case <-timer.C:
				return true
			}
		},
		now:     time.Now,
		maxGaps: defaultConnectionGapLimit,
		gaps:    []connectionGap{},
	}
}

func (supervisor *connectionSupervisor) Run(ctx context.Context, roomID string, callbacks runtimeCallbacks) error {
	if supervisor.sourceFactory == nil {
		return newConnectionFailure("source", errors.New("gift source factory is unavailable"))
	}
	failureCount := 0
	var stateMu sync.Mutex
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		connected := false
		wrapped := callbacks
		previousOnState := callbacks.onState
		wrapped.onState = func(state string) {
			if state == "connected" {
				stateMu.Lock()
				connected = true
				failureCount = 0
				stateMu.Unlock()
				supervisor.closeConnectionGap(supervisor.now())
			}
			if previousOnState != nil {
				previousOnState(state)
			}
		}

		source := supervisor.sourceFactory()
		if source == nil {
			return newConnectionFailure("source", errors.New("gift source factory returned nil"))
		}
		err := source.Run(ctx, roomID, wrapped)
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err == nil {
			err = newConnectionFailure("connection", errors.New("gift source stopped unexpectedly"))
		}
		stateMu.Lock()
		wasConnected := connected
		delay := reconnectDelay(failureCount, nil)
		stateMu.Unlock()
		supervisor.recordConnectionGap(supervisor.now(), err)
		if supervisor.onFailure != nil {
			supervisor.onFailure(err)
		}
		if !supervisor.wait(ctx, delay) {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			return nil
		}
		if !wasConnected {
			stateMu.Lock()
			failureCount++
			stateMu.Unlock()
		}
	}
}

func (supervisor *connectionSupervisor) Gaps() []connectionGap {
	supervisor.mu.RLock()
	defer supervisor.mu.RUnlock()
	return append([]connectionGap(nil), supervisor.gaps...)
}

func (supervisor *connectionSupervisor) recordConnectionGap(now time.Time, err error) {
	supervisor.mu.Lock()
	if len(supervisor.gaps) == 0 || supervisor.gaps[len(supervisor.gaps)-1].EndedAt != 0 {
		supervisor.gaps = append(supervisor.gaps, connectionGap{StartedAt: now.UnixMilli(), ErrorKind: connectionFailureKind(err)})
	}
	gap := &supervisor.gaps[len(supervisor.gaps)-1]
	gap.Attempts++
	if gap.ErrorKind == "connection" {
		gap.ErrorKind = connectionFailureKind(err)
	}
	supervisor.trimGapsLocked()
	gaps := append([]connectionGap(nil), supervisor.gaps...)
	supervisor.mu.Unlock()
	supervisor.publishGaps(gaps)
}

func (supervisor *connectionSupervisor) closeConnectionGap(now time.Time) {
	supervisor.mu.Lock()
	if len(supervisor.gaps) == 0 || supervisor.gaps[len(supervisor.gaps)-1].EndedAt != 0 {
		supervisor.mu.Unlock()
		return
	}
	gap := &supervisor.gaps[len(supervisor.gaps)-1]
	gap.EndedAt = now.UnixMilli()
	gap.DurationMS = maxInt64(0, gap.EndedAt-gap.StartedAt)
	supervisor.trimGapsLocked()
	gaps := append([]connectionGap(nil), supervisor.gaps...)
	supervisor.mu.Unlock()
	supervisor.publishGaps(gaps)
}

func (supervisor *connectionSupervisor) trimGapsLocked() {
	limit := supervisor.maxGaps
	if limit <= 0 {
		limit = defaultConnectionGapLimit
	}
	if len(supervisor.gaps) > limit {
		supervisor.gaps = append([]connectionGap(nil), supervisor.gaps[len(supervisor.gaps)-limit:]...)
	}
}

func (supervisor *connectionSupervisor) publishGaps(gaps []connectionGap) {
	if supervisor.onGap != nil {
		supervisor.onGap(gaps)
	}
}
