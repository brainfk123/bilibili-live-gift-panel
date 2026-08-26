package biligateway

import (
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestRequestLimiterRejectsAtomicallyWithoutChargingEarlierScopes(t *testing.T) {
	clock := time.Date(2026, 8, 17, 10, 0, 0, 0, time.UTC)
	limiter := newRequestLimiter(func() time.Time { return clock })

	for range 20 {
		if !limiter.Allow(1, "room_info") {
			t.Fatal("initial endpoint capacity was rejected")
		}
	}
	for range 10 {
		if limiter.Allow(1, "room_info") {
			t.Fatal("exhausted endpoint was allowed")
		}
	}
	for range 10 {
		if !limiter.Allow(1, "gift_catalog") {
			t.Fatal("endpoint rejection polluted the account or global scope")
		}
	}
}

func TestDedicatedProbeBudgetStaysAtTwentyPerMinuteWithoutChargingNormalRequests(t *testing.T) {
	clock := time.Date(2026, 8, 26, 13, 0, 0, 0, time.UTC)
	budget := newProbeLimiter(func() time.Time { return clock })
	if available := budget.Available(); available != 20 {
		t.Fatalf("initial available probe budget = %d, want 20", available)
	}
	for range 20 {
		if !budget.Allow() {
			t.Fatal("initial probe budget rejected before 20 requests")
		}
	}
	if budget.Allow() || budget.Available() != 0 {
		t.Fatalf("exhausted probe budget allowed or reported tokens: %d", budget.Available())
	}
	clock = clock.Add(30 * time.Second)
	if available := budget.Available(); available != 10 {
		t.Fatalf("30-second refill = %d, want 10", available)
	}
	normal := newRequestLimiter(func() time.Time { return clock })
	for range 20 {
		if !normal.Allow(1, "room_info") {
			t.Fatal("dedicated probe usage charged normal room_info budget")
		}
	}
}

func TestProbeBudgetSharesExistingGlobalSixtyWithoutRaisingTotalEgress(t *testing.T) {
	clock := time.Date(2026, 8, 26, 13, 30, 0, 0, time.UTC)
	global := newGlobalRequestBudget(func() time.Time { return clock })
	probes := newProbeLimiterWithGlobal(func() time.Time { return clock }, global)
	normal := newRequestLimiterWithGlobal(func() time.Time { return clock }, global)
	for range 20 {
		if !probes.Allow() {
			t.Fatal("probe sub-budget rejected before 20")
		}
	}
	for index := 0; index < 40; index++ {
		accountID := int64(1 + index/20)
		if !normal.Allow(accountID, "normal_"+strconv.Itoa(index)) {
			t.Fatalf("normal request %d rejected before shared global 60", index+1)
		}
	}
	if normal.Allow(3, "sixty_first") || probes.Allow() {
		t.Fatal("shared probe+normal egress exceeded the existing global 60/min budget")
	}
}

func TestRequestLimiterAccountRejectionDoesNotChargeGlobalScope(t *testing.T) {
	clock := time.Date(2026, 8, 17, 10, 0, 0, 0, time.UTC)
	limiter := newRequestLimiter(func() time.Time { return clock })

	for index := 0; index < 30; index++ {
		if !limiter.Allow(1, "account_one_"+strconv.Itoa(index)) {
			t.Fatal("initial account capacity was rejected")
		}
	}
	for index := 0; index < 10; index++ {
		if limiter.Allow(1, "rejected_"+strconv.Itoa(index)) {
			t.Fatal("exhausted account was allowed")
		}
	}
	for index := 0; index < 30; index++ {
		if !limiter.Allow(2, "account_two_"+strconv.Itoa(index)) {
			t.Fatal("account rejection polluted the global scope")
		}
	}
}

func TestRequestLimiterRefillsTokensContinuously(t *testing.T) {
	clock := time.Date(2026, 8, 17, 10, 0, 0, 0, time.UTC)
	limiter := newRequestLimiter(func() time.Time { return clock })

	for range 20 {
		if !limiter.Allow(1, "room_info") {
			t.Fatal("initial endpoint capacity was rejected")
		}
	}
	if limiter.Allow(1, "room_info") {
		t.Fatal("exhausted endpoint was allowed")
	}

	clock = clock.Add(3 * time.Second)
	if !limiter.Allow(1, "room_info") {
		t.Fatal("one continuously refilled endpoint token was rejected")
	}
	if limiter.Allow(1, "room_info") {
		t.Fatal("limiter refilled more than one endpoint token in three seconds")
	}
}

func TestRequestLimiterDoesNotTreatZeroClockAsUninitialized(t *testing.T) {
	limiter := newRequestLimiter(func() time.Time { return time.Time{} })
	for range 20 {
		if !limiter.Allow(1, "room_info") {
			t.Fatal("initial endpoint capacity was rejected")
		}
	}
	if limiter.Allow(1, "room_info") {
		t.Fatal("zero-valued clock reset the exhausted endpoint bucket")
	}
}

func TestRequestLimiterConcurrentRequestsDoNotExceedEndpointCapacity(t *testing.T) {
	clock := time.Date(2026, 8, 17, 10, 0, 0, 0, time.UTC)
	limiter := newRequestLimiter(func() time.Time { return clock })
	start := make(chan struct{})
	results := make(chan bool, 40)
	var group sync.WaitGroup
	for index := 0; index < 40; index++ {
		group.Add(1)
		go func() {
			defer group.Done()
			<-start
			results <- limiter.Allow(1, "open_room")
		}()
	}
	close(start)
	group.Wait()
	close(results)

	allowed := 0
	for result := range results {
		if result {
			allowed++
		}
	}
	if allowed != 20 {
		t.Fatalf("allowed requests = %d, want 20", allowed)
	}
}

func TestRequestLimiterCleansUpStaleAccountBuckets(t *testing.T) {
	clock := time.Date(2026, 8, 17, 10, 0, 0, 0, time.UTC)
	limiter := newRequestLimiter(func() time.Time { return clock })
	for accountID := int64(1); accountID <= 100; accountID++ {
		if !limiter.Allow(accountID, "room_info_"+strconv.FormatInt(accountID, 10)) {
			t.Fatalf("account %d initial request rejected", accountID)
		}
		clock = clock.Add(time.Second)
	}
	if got := accountBucketCount(limiter); got != 100 {
		t.Fatalf("account bucket count = %d, want 100", got)
	}

	clock = clock.Add(10*time.Minute + time.Nanosecond)
	if !limiter.Allow(101, "room_info") {
		t.Fatal("request after cleanup interval rejected")
	}
	if got := accountBucketCount(limiter); got != 1 {
		t.Fatalf("account bucket count after cleanup = %d, want 1", got)
	}
}

func accountBucketCount(limiter *requestLimiter) int {
	limiter.mu.Lock()
	defer limiter.mu.Unlock()
	count := 0
	for key := range limiter.buckets {
		if strings.HasPrefix(key, "account:") {
			count++
		}
	}
	return count
}

func TestRequestLimiterFailsClosedWithoutTrustedScope(t *testing.T) {
	limiter := newRequestLimiter(time.Now)
	for _, request := range []struct {
		accountID int64
		endpoint  string
	}{
		{accountID: 0, endpoint: "room_info"},
		{accountID: -1, endpoint: "room_info"},
		{accountID: 1, endpoint: ""},
	} {
		if limiter.Allow(request.accountID, request.endpoint) {
			t.Fatalf("untrusted scope was allowed: %+v", request)
		}
	}
}

func TestEgressBreakerHalfOpenRiskReopensAndResetsSuccessfulProbes(t *testing.T) {
	clock := time.Date(2026, 8, 17, 10, 0, 0, 0, time.UTC)
	breaker := newEgressBreaker(func() time.Time { return clock })
	for index := 0; index < 10; index++ {
		breaker.RecordRisk(int64(index%3 + 1))
	}

	clock = clock.Add(2 * time.Minute)
	if !breaker.Allow(4) {
		t.Fatal("first half-open probe rejected")
	}
	breaker.RecordSuccess()
	if !breaker.Allow(4) {
		t.Fatal("second half-open probe rejected")
	}
	breaker.RecordRisk(4)
	if breaker.Allow(5) {
		t.Fatal("risk failure released an immediate half-open probe")
	}

	clock = clock.Add(2 * time.Minute)
	for probe := 1; probe <= 2; probe++ {
		if !breaker.Allow(5) {
			t.Fatalf("successful probe %d rejected", probe)
		}
		breaker.RecordSuccess()
	}
	if breaker.openedUntil.IsZero() {
		t.Fatal("risk failure did not reset the successful probe count")
	}
	if !breaker.Allow(5) {
		t.Fatal("third successful probe rejected")
	}
	breaker.RecordSuccess()
	if !breaker.openedUntil.IsZero() {
		t.Fatal("breaker did not close after three post-risk successes")
	}
}
