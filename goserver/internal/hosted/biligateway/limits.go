package biligateway

import (
	"errors"
	"sync"
	"time"
)

var (
	ErrEgressUnavailable    = errors.New("egress_unavailable")
	ErrRiskRejected         = errors.New("risk_rejected")
	ErrAccountScopeRequired = errors.New("account_scope_required")
	ErrRateLimited          = errors.New("rate_limited")
)

type rateBucket struct {
	started time.Time
	count   int
}
type requestLimiter struct {
	mu      sync.Mutex
	now     func() time.Time
	buckets map[string]rateBucket
}

func newRequestLimiter(now func() time.Time) *requestLimiter {
	if now == nil {
		now = time.Now
	}
	return &requestLimiter{now: now, buckets: make(map[string]rateBucket)}
}

// Allow applies all three bounded token windows. A missing trusted account ID
// is fail-closed at the caller before this function is reached.
func (limiter *requestLimiter) Allow(accountID int64, endpoint string) bool {
	if limiter == nil || accountID <= 0 || endpoint == "" {
		return false
	}
	limiter.mu.Lock()
	defer limiter.mu.Unlock()
	now := limiter.now()
	for _, key := range []string{"global", "account:" + integerText(accountID), "endpoint:" + endpoint} {
		bucket := limiter.buckets[key]
		if bucket.started.IsZero() || !now.Before(bucket.started.Add(time.Minute)) {
			bucket = rateBucket{started: now}
		}
		limit := 60
		switch {
		case key == "global":
			limit = 60
		case len(key) >= len("account:") && key[:len("account:")] == "account:":
			limit = 30
		case len(key) >= len("endpoint:") && key[:len("endpoint:")] == "endpoint:":
			limit = 20
		}
		if bucket.count >= limit {
			return false
		}
		bucket.count++
		limiter.buckets[key] = bucket
	}
	return true
}

type riskSample struct {
	accountID int64
	at        time.Time
}
type egressBreaker struct {
	mu          sync.Mutex
	now         func() time.Time
	risks       []riskSample
	openedUntil time.Time
	halfOpen    bool
	successes   int
}

func newEgressBreaker(now func() time.Time) *egressBreaker {
	if now == nil {
		now = time.Now
	}
	return &egressBreaker{now: now}
}
func (breaker *egressBreaker) RecordRisk(accountID int64) {
	if breaker == nil {
		return
	}
	breaker.mu.Lock()
	defer breaker.mu.Unlock()
	now := breaker.now()
	cutoff := now.Add(-time.Minute)
	kept := breaker.risks[:0]
	accounts := map[int64]struct{}{}
	for _, sample := range breaker.risks {
		if !sample.at.Before(cutoff) {
			kept = append(kept, sample)
			accounts[sample.accountID] = struct{}{}
		}
	}
	breaker.risks = append(kept, riskSample{accountID: accountID, at: now})
	accounts[accountID] = struct{}{}
	if len(breaker.risks) >= 10 && len(accounts) >= 3 {
		breaker.openedUntil = now.Add(2 * time.Minute)
		breaker.halfOpen = false
		breaker.successes = 0
	}
}
func (breaker *egressBreaker) Allow(int64) bool {
	if breaker == nil {
		return false
	}
	breaker.mu.Lock()
	defer breaker.mu.Unlock()
	now := breaker.now()
	if breaker.openedUntil.IsZero() {
		return true
	}
	if now.Before(breaker.openedUntil) {
		return false
	}
	if breaker.halfOpen {
		return false
	}
	breaker.halfOpen = true
	return true
}
func (breaker *egressBreaker) RecordSuccess() {
	if breaker == nil {
		return
	}
	breaker.mu.Lock()
	defer breaker.mu.Unlock()
	if !breaker.halfOpen {
		return
	}
	breaker.successes++
	if breaker.successes >= 3 {
		breaker.openedUntil = time.Time{}
		breaker.halfOpen = false
		breaker.successes = 0
		breaker.risks = nil
		return
	}
	// Each half-open probe is independent. A successful probe below the
	// close threshold releases exactly one subsequent probe.
	breaker.halfOpen = false
}

// RecordFailure releases a half-open probe without treating an isolated
// transport failure as correlated egress risk. The breaker remains open until
// a later probe succeeds three times or correlated risk reopens its window.
func (breaker *egressBreaker) RecordFailure() {
	if breaker == nil {
		return
	}
	breaker.mu.Lock()
	defer breaker.mu.Unlock()
	if breaker.halfOpen {
		breaker.halfOpen = false
	}
	breaker.successes = 0
}
func (breaker *egressBreaker) Open() bool {
	if breaker == nil {
		return true
	}
	breaker.mu.Lock()
	defer breaker.mu.Unlock()
	return !breaker.openedUntil.IsZero() && breaker.now().Before(breaker.openedUntil)
}
