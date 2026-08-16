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
	initialized bool
	tokens      float64
	updated     time.Time
	lastSeen    time.Time
}

const (
	globalBucketCapacity   = 60
	accountBucketCapacity  = 30
	endpointBucketCapacity = 20
	accountBucketRetention = 10 * time.Minute
)

type requestLimiter struct {
	mu          sync.Mutex
	now         func() time.Time
	buckets     map[string]rateBucket
	lastCleanup time.Time
}

func newRequestLimiter(now func() time.Time) *requestLimiter {
	if now == nil {
		now = time.Now
	}
	return &requestLimiter{now: now, buckets: make(map[string]rateBucket)}
}

// Allow atomically charges the global, trusted-account, and endpoint scopes.
// If any scope is empty, none of the three scopes loses a token.
func (limiter *requestLimiter) Allow(accountID int64, endpoint string) bool {
	if limiter == nil || accountID <= 0 || endpoint == "" {
		return false
	}
	limiter.mu.Lock()
	defer limiter.mu.Unlock()
	now := limiter.now()
	limiter.cleanupStaleAccounts(now)

	scopes := [...]struct {
		key      string
		capacity float64
	}{
		{key: "global", capacity: globalBucketCapacity},
		{key: "account:" + integerText(accountID), capacity: accountBucketCapacity},
		{key: "endpoint:" + endpoint, capacity: endpointBucketCapacity},
	}
	var candidates [len(scopes)]rateBucket
	allowed := true
	for index, scope := range scopes {
		candidate := refillBucket(limiter.buckets[scope.key], now, scope.capacity)
		candidate.lastSeen = now
		candidates[index] = candidate
		if candidate.tokens < 1 {
			allowed = false
		}
	}
	for index, scope := range scopes {
		candidate := candidates[index]
		if allowed {
			candidate.tokens--
		}
		limiter.buckets[scope.key] = candidate
	}
	return allowed
}

func refillBucket(bucket rateBucket, now time.Time, capacity float64) rateBucket {
	if !bucket.initialized {
		return rateBucket{initialized: true, tokens: capacity, updated: now, lastSeen: now}
	}
	if elapsed := now.Sub(bucket.updated); elapsed > 0 {
		bucket.tokens += elapsed.Seconds() * capacity / time.Minute.Seconds()
		if bucket.tokens > capacity {
			bucket.tokens = capacity
		}
		bucket.updated = now
	}
	return bucket
}

func (limiter *requestLimiter) cleanupStaleAccounts(now time.Time) {
	if !limiter.lastCleanup.IsZero() && now.Sub(limiter.lastCleanup) < accountBucketRetention {
		return
	}
	for key, bucket := range limiter.buckets {
		if len(key) >= len("account:") && key[:len("account:")] == "account:" && !now.Before(bucket.lastSeen.Add(accountBucketRetention)) {
			delete(limiter.buckets, key)
		}
	}
	limiter.lastCleanup = now
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
	if breaker == nil || accountID <= 0 {
		return
	}
	breaker.mu.Lock()
	defer breaker.mu.Unlock()
	now := breaker.now()
	if breaker.halfOpen {
		breaker.openedUntil = now.Add(2 * time.Minute)
		breaker.halfOpen = false
		breaker.successes = 0
		breaker.risks = []riskSample{{accountID: accountID, at: now}}
		return
	}
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

// RecordFailure reopens a failed half-open probe for a fresh two-minute hold
// without treating an isolated transport failure as correlated egress risk.
func (breaker *egressBreaker) RecordFailure() {
	if breaker == nil {
		return
	}
	breaker.mu.Lock()
	defer breaker.mu.Unlock()
	if breaker.halfOpen {
		breaker.halfOpen = false
		breaker.openedUntil = breaker.now().Add(2 * time.Minute)
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
