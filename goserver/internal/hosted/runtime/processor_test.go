package runtime

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"bilibili-live-gift-panel/internal/gameplay"
	"bilibili-live-gift-panel/internal/hosted/configuration"
	"bilibili-live-gift-panel/internal/hosted/roomsource"
)

func TestProcessorPublishesOnlyAfterCommit(t *testing.T) {
	order := []string{}
	repository := &fakeProcessorRepository{
		version: processorVersionFixture(),
		state:   processorStateFixture(),
		commit: func(context.Context, configuration.RuntimeEventCommand) (configuration.RuntimeEventResult, error) {
			order = append(order, "commit")
			return configuration.RuntimeEventResult{Revision: 2}, nil
		},
	}
	publisher := publisherFunc(func(DisplaySnapshot) { order = append(order, "publish") })
	processor, err := NewProcessor(context.Background(), repository, publisher, gameplay.Engine{}, processorBindingFixture(), ProcessorOptions{Now: processorNow})
	if err != nil {
		t.Fatal(err)
	}
	if err := processor.Accept(giftEventFixture("event-1", 123, "secret", "https://secret")); err != nil {
		t.Fatal(err)
	}
	if len(order) != 2 || order[0] != "commit" || order[1] != "publish" {
		t.Fatalf("operation order = %v, want [commit publish]", order)
	}
}

func TestProcessorCommitFailureDoesNotPublishOrUpdateViewers(t *testing.T) {
	repository := processorRepositoryFixture()
	repository.commit = func(context.Context, configuration.RuntimeEventCommand) (configuration.RuntimeEventResult, error) {
		return configuration.RuntimeEventResult{}, configuration.ErrRevisionConflict
	}
	published := 0
	processor := newProcessorForTest(t, repository, publisherFunc(func(DisplaySnapshot) { published++ }))
	err := processor.Accept(giftEventFixture("event-1", 123, "secret", "https://secret"))
	if !errors.Is(err, configuration.ErrRevisionConflict) {
		t.Fatalf("Accept() error = %v, want revision conflict", err)
	}
	if published != 0 {
		t.Fatalf("published = %d, want 0", published)
	}
	if rows := processor.Viewers(); len(rows) != 0 {
		t.Fatalf("viewers changed before commit: %#v", rows)
	}
}

func TestProcessorStableDuplicateHasNoSecondEffect(t *testing.T) {
	repository := processorRepositoryFixture()
	calls := 0
	repository.commit = func(_ context.Context, command configuration.RuntimeEventCommand) (configuration.RuntimeEventResult, error) {
		calls++
		if command.StableEventHash == nil {
			t.Fatal("stable event hash is nil")
		}
		if calls == 2 {
			return configuration.RuntimeEventResult{Revision: 2, Duplicate: true}, nil
		}
		return configuration.RuntimeEventResult{Revision: 2}, nil
	}
	published := 0
	processor := newProcessorForTest(t, repository, publisherFunc(func(DisplaySnapshot) { published++ }))
	event := giftEventFixture("event-1", 123, "secret", "https://secret")
	if err := processor.Accept(event); err != nil {
		t.Fatal(err)
	}
	if err := processor.Accept(event); err != nil {
		t.Fatal(err)
	}
	if published != 1 || len(processor.Viewers()) != 1 || processor.Viewers()[0].Gifts != 1 {
		t.Fatalf("duplicate effects: published=%d viewers=%#v", published, processor.Viewers())
	}
}

func TestProcessorCrossSessionStableDuplicateHasNoEffectAndNeverBuffers(t *testing.T) {
	repository := processorRepositoryFixture()
	commits := 0
	repository.commit = func(_ context.Context, command configuration.RuntimeEventCommand) (configuration.RuntimeEventResult, error) {
		commits++
		return configuration.RuntimeEventResult{Revision: command.ExpectedRevision, Duplicate: true}, nil
	}
	published := 0
	processor := newProcessorForTest(t, repository, publisherFunc(func(DisplaySnapshot) { published++ }))
	if err := processor.Accept(giftEventFixture("old-session-event", 123, "secret", "avatar")); err != nil {
		t.Fatal(err)
	}
	status := processor.Status()
	if commits != 1 || published != 0 || len(processor.Viewers()) != 0 || status.Buffered != 0 || status.Degraded || status.Rejecting {
		t.Fatalf("cross-session duplicate effects: commits=%d published=%d viewers=%#v status=%+v", commits, published, processor.Viewers(), status)
	}
	if score := processor.RuntimeState().AttributeValues["score"]; score != 0 {
		t.Fatalf("cross-session duplicate score = %v, want unchanged 0", score)
	}
}

func TestProcessorDuplicateAtNextRevisionDoesNotInstallConcurrentStateOrPublish(t *testing.T) {
	repository := processorRepositoryFixture()
	loadCalls := 0
	repository.load = func(context.Context, int64) (configuration.Version, configuration.State, error) {
		loadCalls++
		state := processorStateFixture()
		if loadCalls > 1 {
			state.Revision = 2
			state.Runtime.AttributeValues["score"] = 7
		}
		return processorVersionFixture(), state, nil
	}
	repository.commit = func(_ context.Context, command configuration.RuntimeEventCommand) (configuration.RuntimeEventResult, error) {
		return configuration.RuntimeEventResult{Revision: command.ExpectedRevision + 1, Duplicate: true}, nil
	}
	published := 0
	processor := newProcessorForTest(t, repository, publisherFunc(func(DisplaySnapshot) { published++ }))
	if err := processor.Accept(giftEventFixture("already-committed", 123, "secret", "avatar")); err != nil {
		t.Fatal(err)
	}
	if value := processor.RuntimeState().AttributeValues["score"]; value != 7 {
		t.Fatalf("duplicate runtime score = %v, want authoritative 7 instead of locally computed 1", value)
	}
	if published != 0 || len(processor.Viewers()) != 0 {
		t.Fatalf("duplicate side effects: published=%d viewers=%#v", published, processor.Viewers())
	}
}

func TestProcessorDuplicateAtNextRevisionReloadsCanonicalStateBeforeNextEvent(t *testing.T) {
	repository := processorRepositoryFixture()
	loadCalls := 0
	repository.load = func(context.Context, int64) (configuration.Version, configuration.State, error) {
		loadCalls++
		state := processorStateFixture()
		if loadCalls > 1 {
			state.Revision = 2
			state.Runtime.AttributeValues["score"] = 7
		}
		return processorVersionFixture(), state, nil
	}
	commitCalls := 0
	repository.commit = func(_ context.Context, command configuration.RuntimeEventCommand) (configuration.RuntimeEventResult, error) {
		commitCalls++
		if commitCalls == 1 {
			return configuration.RuntimeEventResult{Revision: 2, Duplicate: true}, nil
		}
		if command.ExpectedRevision != 2 {
			t.Fatalf("next event expected revision = %d, want reloaded 2", command.ExpectedRevision)
		}
		return configuration.RuntimeEventResult{Revision: 3}, nil
	}
	published := 0
	processor := newProcessorForTest(t, repository, publisherFunc(func(DisplaySnapshot) { published++ }))
	if err := processor.Accept(giftEventFixture("already-committed", 123, "secret", "avatar")); err != nil {
		t.Fatal(err)
	}
	if score := processor.RuntimeState().AttributeValues["score"]; score != 7 {
		t.Fatalf("canonical reloaded score = %v, want 7", score)
	}
	if published != 0 || len(processor.Viewers()) != 0 {
		t.Fatalf("duplicate reload produced side effects: published=%d viewers=%#v", published, processor.Viewers())
	}
	if err := processor.Accept(giftEventFixture("next-event", 456, "next", "avatar-two")); err != nil {
		t.Fatal(err)
	}
	if score := processor.RuntimeState().AttributeValues["score"]; score != 8 || published != 1 {
		t.Fatalf("next event after reload: score=%v published=%d", score, published)
	}
}

func TestProcessorPersistsOnlySHA256StableIDAndIdentityFreeAggregateDelta(t *testing.T) {
	repository := processorRepositoryFixture()
	commands := []configuration.RuntimeEventCommand{}
	repository.commit = func(_ context.Context, command configuration.RuntimeEventCommand) (configuration.RuntimeEventResult, error) {
		commands = append(commands, command)
		return configuration.RuntimeEventResult{Revision: command.ExpectedRevision + 1}, nil
	}
	processor := newProcessorForTest(t, repository, publisherFunc(func(DisplaySnapshot) {}))
	for _, id := range []string{"event-1", "event-2"} {
		if err := processor.Accept(giftEventFixture(id, 987654321, "secret", "https://secret")); err != nil {
			t.Fatal(err)
		}
	}
	if len(commands) != 2 {
		t.Fatalf("commands = %d", len(commands))
	}
	wantHash := sha256.Sum256([]byte("event-1"))
	if commands[0].StableEventHash == nil || *commands[0].StableEventHash != wantHash {
		t.Fatalf("stable hash = %x, want %x", commands[0].StableEventHash, wantHash)
	}
	wantDelta := configuration.RuntimeAggregate{EventCount: 1, GiftCount: 1, GiftCoin: 1000}
	if got := commands[1].AggregateDelta; got != wantDelta {
		t.Fatalf("second aggregate delta = %#v, want %#v", got, wantDelta)
	}
}

func TestProcessorRejectsWrongRoomWithoutRepositoryWork(t *testing.T) {
	repository := processorRepositoryFixture()
	repository.commit = func(context.Context, configuration.RuntimeEventCommand) (configuration.RuntimeEventResult, error) {
		t.Fatal("repository called for wrong room")
		return configuration.RuntimeEventResult{}, nil
	}
	processor := newProcessorForTest(t, repository, publisherFunc(func(DisplaySnapshot) { t.Fatal("published wrong-room event") }))
	event := giftEventFixture("event-1", 123, "secret", "https://secret")
	event.RoomID = "84"
	if err := processor.Accept(event); !errors.Is(err, ErrWrongRoom) {
		t.Fatalf("Accept() error = %v, want ErrWrongRoom", err)
	}
}

func TestProcessorRepositoryAndAlertArgumentsExcludeViewerIdentityAndRawEvent(t *testing.T) {
	repository := processorRepositoryFixture()
	var serialized string
	repository.commit = func(_ context.Context, command configuration.RuntimeEventCommand) (configuration.RuntimeEventResult, error) {
		encoded, err := json.Marshal(command)
		if err != nil {
			t.Fatal(err)
		}
		serialized = string(encoded)
		return configuration.RuntimeEventResult{Revision: 2}, nil
	}
	processor := newProcessorForTest(t, repository, publisherFunc(func(DisplaySnapshot) {}))
	if err := processor.Accept(giftEventFixture("stable-private", 987654321, "secret", "https://secret")); err != nil {
		t.Fatal(err)
	}
	for _, private := range []string{"987654321", "secret", "https://secret", "stable-private", "SEND_GIFT"} {
		if strings.Contains(serialized, private) {
			t.Fatalf("repository argument contains private/raw value %q: %s", private, serialized)
		}
	}
}

func TestProcessorSerializesConcurrentAccepts(t *testing.T) {
	repository := processorRepositoryFixture()
	var mu sync.Mutex
	revisions := []uint64{}
	repository.commit = func(_ context.Context, command configuration.RuntimeEventCommand) (configuration.RuntimeEventResult, error) {
		mu.Lock()
		defer mu.Unlock()
		revisions = append(revisions, command.ExpectedRevision)
		return configuration.RuntimeEventResult{Revision: command.ExpectedRevision + 1}, nil
	}
	processor := newProcessorForTest(t, repository, publisherFunc(func(DisplaySnapshot) {}))
	start := make(chan struct{})
	done := make(chan error, 2)
	for index := 0; index < 2; index++ {
		index := index
		go func() {
			<-start
			done <- processor.Accept(giftEventFixture("event-"+string(rune('a'+index)), int64(index+1), "viewer", "avatar"))
		}()
	}
	close(start)
	for index := 0; index < 2; index++ {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
	if len(revisions) != 2 || revisions[0] != 1 || revisions[1] != 2 {
		t.Fatalf("expected revisions = %v, want [1 2]", revisions)
	}
}

func TestProcessorSerializesPublishOrderWithConcurrentAccepts(t *testing.T) {
	repository := processorRepositoryFixture()
	secondCommitStarted := make(chan struct{})
	repository.commit = func(_ context.Context, command configuration.RuntimeEventCommand) (configuration.RuntimeEventResult, error) {
		if command.ExpectedRevision == 2 {
			close(secondCommitStarted)
		}
		return configuration.RuntimeEventResult{Revision: command.ExpectedRevision + 1}, nil
	}
	firstPublishStarted := make(chan struct{})
	releaseFirstPublish := make(chan struct{})
	var mu sync.Mutex
	published := []uint64{}
	processor := newProcessorForTest(t, repository, publisherFunc(func(snapshot DisplaySnapshot) {
		if snapshot.Revision == 2 {
			close(firstPublishStarted)
			<-releaseFirstPublish
		}
		mu.Lock()
		published = append(published, snapshot.Revision)
		mu.Unlock()
	}))
	firstDone := make(chan error, 1)
	go func() { firstDone <- processor.Accept(giftEventFixture("event-1", 1, "viewer", "avatar")) }()
	<-firstPublishStarted
	secondDone := make(chan error, 1)
	go func() { secondDone <- processor.Accept(giftEventFixture("event-2", 2, "viewer", "avatar")) }()
	secondFinished := false
	select {
	case <-secondCommitStarted:
		if err := <-secondDone; err != nil {
			t.Fatal(err)
		}
		secondFinished = true
	case <-time.After(50 * time.Millisecond):
	}
	close(releaseFirstPublish)
	if err := <-firstDone; err != nil {
		t.Fatal(err)
	}
	if !secondFinished {
		if err := <-secondDone; err != nil {
			t.Fatal(err)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if !reflect.DeepEqual(published, []uint64{2, 3}) {
		t.Fatalf("publish order = %v, want [2 3]", published)
	}
}

func TestProcessorFailureIsIsolatedFromAnotherAccount(t *testing.T) {
	failingRepository := processorRepositoryFixture()
	failingRepository.commit = func(context.Context, configuration.RuntimeEventCommand) (configuration.RuntimeEventResult, error) {
		return configuration.RuntimeEventResult{}, configuration.ErrUnavailable
	}
	timers := &manualTimerFactory{created: make(chan *manualTimer, 1)}
	failing := newProcessorWithOptionsForTest(t, failingRepository, publisherFunc(func(DisplaySnapshot) { t.Fatal("failing account published") }), ProcessorOptions{Now: processorNow, NewRetryTimer: timers.New})

	healthyRepository := processorRepositoryFixture()
	healthyRepository.version.AccountID = 8
	healthyRepository.state.AccountID = 8
	healthyRepository.commit = func(_ context.Context, command configuration.RuntimeEventCommand) (configuration.RuntimeEventResult, error) {
		return configuration.RuntimeEventResult{Revision: command.ExpectedRevision + 1}, nil
	}
	binding := processorBindingFixture()
	binding.Owner.AccountID = 8
	binding.Session.AccountID = 8
	published := 0
	healthy, err := NewProcessor(context.Background(), healthyRepository, publisherFunc(func(DisplaySnapshot) { published++ }), gameplay.Engine{}, binding, ProcessorOptions{Now: processorNow})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = healthy.Close(context.Background()) })

	if err := failing.Accept(giftEventFixture("failing", 1, "viewer", "avatar")); err != nil {
		t.Fatal(err)
	}
	if err := healthy.Accept(giftEventFixture("healthy", 2, "viewer", "avatar")); err != nil {
		t.Fatal(err)
	}
	if published != 1 || healthy.Status().Degraded || !failing.Status().Degraded {
		t.Fatalf("isolated status: published=%d healthy=%#v failing=%#v", published, healthy.Status(), failing.Status())
	}
}

func TestProcessorConnectionHealthUpdateDoesNotWaitForBlockedPersistence(t *testing.T) {
	repository := processorRepositoryFixture()
	commitStarted := make(chan struct{})
	releaseCommit := make(chan struct{})
	repository.commit = func(_ context.Context, command configuration.RuntimeEventCommand) (configuration.RuntimeEventResult, error) {
		close(commitStarted)
		<-releaseCommit
		return configuration.RuntimeEventResult{Revision: command.ExpectedRevision + 1}, nil
	}
	processor := newProcessorForTest(t, repository, publisherFunc(func(DisplaySnapshot) {}))
	accepted := make(chan error, 1)
	go func() { accepted <- processor.Accept(giftEventFixture("event-1", 1, "viewer", "avatar")) }()
	<-commitStarted
	healthUpdated := make(chan struct{})
	go func() {
		processor.SetConnectionHealthy(false)
		close(healthUpdated)
	}()
	select {
	case <-healthUpdated:
	case <-time.After(time.Second):
		close(releaseCommit)
		t.Fatal("connection health update blocked behind persistence")
	}
	close(releaseCommit)
	if err := <-accepted; err != nil {
		t.Fatal(err)
	}
	if processor.Status().ConnectionHealthy {
		t.Fatal("connection health update was lost")
	}
}

func TestProcessorOwnershipLossCallbackIsNonblockingAndExactlyOnce(t *testing.T) {
	repository := processorRepositoryFixture()
	repository.commit = func(_ context.Context, command configuration.RuntimeEventCommand) (configuration.RuntimeEventResult, error) {
		return configuration.RuntimeEventResult{Revision: command.ExpectedRevision + 1}, nil
	}
	processor := newProcessorForTest(t, repository, publisherFunc(func(DisplaySnapshot) {}))
	started := make(chan struct{}, 2)
	release := make(chan struct{})
	var calls atomic.Int64
	processor.SetOwnershipLost(func() {
		calls.Add(1)
		started <- struct{}{}
		<-release
	})
	returned := make(chan struct{})
	go func() {
		processor.notifyOwnershipLost()
		processor.notifyOwnershipLost()
		close(returned)
	}()
	select {
	case <-returned:
	case <-time.After(time.Second):
		t.Fatal("ownership-loss notification blocked on callback")
	}
	<-started
	select {
	case <-started:
		t.Fatal("ownership-loss callback ran more than once")
	case <-time.After(50 * time.Millisecond):
	}
	if calls.Load() != 1 {
		t.Fatalf("ownership-loss callback calls = %d, want 1", calls.Load())
	}
	close(release)
}

func TestProcessorRetryOwnershipLossCallbackDoesNotWaitForBlockingAlert(t *testing.T) {
	repository := processorRepositoryFixture()
	attempts := 0
	repository.commit = func(context.Context, configuration.RuntimeEventCommand) (configuration.RuntimeEventResult, error) {
		attempts++
		if attempts == 1 {
			return configuration.RuntimeEventResult{}, configuration.ErrUnavailable
		}
		return configuration.RuntimeEventResult{}, configuration.ErrOwnership
	}
	timers := &manualTimerFactory{created: make(chan *manualTimer, 1)}
	alertStarted := make(chan struct{})
	releaseAlert := make(chan struct{})
	processor := newProcessorWithOptionsForTest(t, repository, publisherFunc(func(DisplaySnapshot) {}), ProcessorOptions{Now: processorNow, NewRetryTimer: timers.New, Alert: func(status ProcessorStatus) {
		if status.Rejecting {
			close(alertStarted)
			<-releaseAlert
		}
	}})
	ownershipLost := make(chan struct{})
	processor.SetOwnershipLost(func() { close(ownershipLost) })
	if err := processor.Accept(giftEventFixture("event-1", 1, "viewer", "avatar")); err != nil {
		t.Fatal(err)
	}
	(<-timers.created).Fire()
	<-alertStarted
	select {
	case <-ownershipLost:
	case <-time.After(100 * time.Millisecond):
		close(releaseAlert)
		t.Fatal("ownership-loss callback waited for blocking alert")
	}
	close(releaseAlert)
}

func TestProcessorBuffersTransientFailureAndRetriesWithInjectedTimer(t *testing.T) {
	repository := processorRepositoryFixture()
	attempts := 0
	repository.commit = func(_ context.Context, command configuration.RuntimeEventCommand) (configuration.RuntimeEventResult, error) {
		attempts++
		if attempts == 1 {
			return configuration.RuntimeEventResult{}, configuration.ErrUnavailable
		}
		return configuration.RuntimeEventResult{Revision: command.ExpectedRevision + 1}, nil
	}
	timers := &manualTimerFactory{created: make(chan *manualTimer, 2)}
	published := make(chan DisplaySnapshot, 1)
	alerts := []ProcessorStatus{}
	processor := newProcessorWithOptionsForTest(t, repository, publisherFunc(func(snapshot DisplaySnapshot) { published <- snapshot }), ProcessorOptions{
		Now: processorNow, NewRetryTimer: timers.New,
		Alert: func(status ProcessorStatus) { alerts = append(alerts, status) },
	})
	if err := processor.Accept(giftEventFixture("event-1", 123, "secret", "https://secret")); err != nil {
		t.Fatalf("Accept() buffered transient error = %v", err)
	}
	if status := processor.Status(); !status.Degraded || status.Buffered != 1 || !status.ConnectionHealthy || status.Rejecting {
		t.Fatalf("degraded status = %#v", status)
	}
	if len(alerts) != 1 || !alerts[0].Degraded {
		t.Fatalf("alerts = %#v", alerts)
	}
	timer := <-timers.created
	if timer.delay != time.Second {
		t.Fatalf("retry delay = %v, want 1s", timer.delay)
	}
	timer.Fire()
	select {
	case snapshot := <-published:
		if snapshot.Revision != 2 {
			t.Fatalf("published revision = %d", snapshot.Revision)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("retry did not publish")
	}
	if status := processor.Status(); status.Degraded || status.Buffered != 0 || !status.ConnectionHealthy {
		t.Fatalf("recovered status = %#v", status)
	}
}

func TestProcessorRejectsWhenOutageBufferReachesFiveHundred(t *testing.T) {
	repository := processorRepositoryFixture()
	repository.commit = func(context.Context, configuration.RuntimeEventCommand) (configuration.RuntimeEventResult, error) {
		return configuration.RuntimeEventResult{}, configuration.ErrUnavailable
	}
	timers := &manualTimerFactory{created: make(chan *manualTimer, 1)}
	processor := newProcessorWithOptionsForTest(t, repository, publisherFunc(func(DisplaySnapshot) {}), ProcessorOptions{Now: processorNow, NewRetryTimer: timers.New})
	for index := 0; index < 500; index++ {
		event := giftEventFixture("event-"+strings.Repeat("x", index%31)+string(rune(index+1)), int64(index+1), "viewer", "avatar")
		if err := processor.Accept(event); err != nil {
			t.Fatalf("Accept(%d) = %v", index, err)
		}
	}
	if err := processor.Accept(giftEventFixture("overflow", 999, "viewer", "avatar")); !errors.Is(err, ErrPersistenceUnavailable) {
		t.Fatalf("overflow error = %v, want ErrPersistenceUnavailable", err)
	}
	if status := processor.Status(); status.Buffered != 500 || !status.Degraded || !status.Rejecting || !status.ConnectionHealthy {
		t.Fatalf("overflow status = %#v", status)
	}
}

func TestProcessorRejectsNewMutationAtSixtySecondOutageAge(t *testing.T) {
	repository := processorRepositoryFixture()
	repository.commit = func(context.Context, configuration.RuntimeEventCommand) (configuration.RuntimeEventResult, error) {
		return configuration.RuntimeEventResult{}, configuration.ErrUnavailable
	}
	now := processorNow()
	timers := &manualTimerFactory{created: make(chan *manualTimer, 1)}
	processor := newProcessorWithOptionsForTest(t, repository, publisherFunc(func(DisplaySnapshot) {}), ProcessorOptions{Now: func() time.Time { return now }, NewRetryTimer: timers.New})
	if err := processor.Accept(giftEventFixture("event-1", 1, "viewer", "avatar")); err != nil {
		t.Fatal(err)
	}
	now = now.Add(60 * time.Second)
	if err := processor.Accept(giftEventFixture("event-2", 2, "viewer", "avatar")); !errors.Is(err, ErrPersistenceUnavailable) {
		t.Fatalf("aged buffer error = %v, want ErrPersistenceUnavailable", err)
	}
}

func TestProcessorRetryDropsRawBufferAtSixtySecondsAndRejectsFurtherMutations(t *testing.T) {
	repository := processorRepositoryFixture()
	attempts := 0
	repository.commit = func(context.Context, configuration.RuntimeEventCommand) (configuration.RuntimeEventResult, error) {
		attempts++
		return configuration.RuntimeEventResult{}, configuration.ErrUnavailable
	}
	now := processorNow()
	timers := &manualTimerFactory{created: make(chan *manualTimer, 1)}
	alerts := make(chan ProcessorStatus, 2)
	processor := newProcessorWithOptionsForTest(t, repository, publisherFunc(func(DisplaySnapshot) {}), ProcessorOptions{Now: func() time.Time { return now }, NewRetryTimer: timers.New, Alert: func(status ProcessorStatus) { alerts <- status }})
	if err := processor.Accept(giftEventFixture("event-1", 1, "viewer", "avatar")); err != nil {
		t.Fatal(err)
	}
	<-alerts
	now = now.Add(60 * time.Second)
	(<-timers.created).Fire()
	expired := <-alerts
	if expired.Buffered != 0 || !expired.Rejecting || !expired.Degraded {
		t.Fatalf("expired status = %#v", expired)
	}
	if err := processor.Accept(giftEventFixture("event-2", 2, "viewer", "avatar")); !errors.Is(err, ErrPersistenceUnavailable) {
		t.Fatalf("Accept after expiry = %v", err)
	}
	if attempts != 1 {
		t.Fatalf("repository attempts = %d, want 1", attempts)
	}
}

func TestProcessorRetryBackoffIsCappedAtSixtySecondRetentionDeadline(t *testing.T) {
	repository := processorRepositoryFixture()
	repository.commit = func(context.Context, configuration.RuntimeEventCommand) (configuration.RuntimeEventResult, error) {
		return configuration.RuntimeEventResult{}, configuration.ErrUnavailable
	}
	now := processorNow()
	timers := &manualTimerFactory{created: make(chan *manualTimer, 1)}
	processor := newProcessorWithOptionsForTest(t, repository, publisherFunc(func(DisplaySnapshot) {}), ProcessorOptions{Now: func() time.Time { return now }, NewRetryTimer: timers.New})
	if err := processor.Accept(giftEventFixture("event-1", 1, "viewer", "avatar")); err != nil {
		t.Fatal(err)
	}
	for index, want := range []time.Duration{time.Second, 2 * time.Second, 4 * time.Second, 8 * time.Second, 16 * time.Second, 29 * time.Second} {
		timer := <-timers.created
		if timer.delay != want {
			t.Fatalf("retry %d delay = %v, want %v", index+1, timer.delay, want)
		}
		if index < 5 {
			now = now.Add(timer.delay)
			timer.Fire()
		}
	}
}

func TestProcessorDoesNotBufferNonGameplayTrafficDuringOutage(t *testing.T) {
	repository := processorRepositoryFixture()
	repository.commit = func(context.Context, configuration.RuntimeEventCommand) (configuration.RuntimeEventResult, error) {
		return configuration.RuntimeEventResult{}, configuration.ErrUnavailable
	}
	timers := &manualTimerFactory{created: make(chan *manualTimer, 1)}
	processor := newProcessorWithOptionsForTest(t, repository, publisherFunc(func(DisplaySnapshot) {}), ProcessorOptions{Now: processorNow, NewRetryTimer: timers.New})
	if err := processor.Accept(giftEventFixture("event-1", 1, "viewer", "avatar")); err != nil {
		t.Fatal(err)
	}
	for index := 0; index < 600; index++ {
		if err := processor.Accept(roomsource.Event{RoomID: "42", Type: "DANMU_MSG", Data: []byte(`{"cmd":"DANMU_MSG"}`)}); err != nil {
			t.Fatalf("chat event %d = %v", index, err)
		}
	}
	if status := processor.Status(); status.Buffered != 1 || status.Rejecting {
		t.Fatalf("chat consumed persistence buffer: %#v", status)
	}
}

func TestProcessorPermanentRetryFailureStaysDegradedAndRejecting(t *testing.T) {
	repository := processorRepositoryFixture()
	attempts := 0
	repository.commit = func(context.Context, configuration.RuntimeEventCommand) (configuration.RuntimeEventResult, error) {
		attempts++
		if attempts == 1 {
			return configuration.RuntimeEventResult{}, configuration.ErrUnavailable
		}
		return configuration.RuntimeEventResult{}, configuration.ErrRevisionConflict
	}
	timers := &manualTimerFactory{created: make(chan *manualTimer, 1)}
	alerts := make(chan ProcessorStatus, 2)
	processor := newProcessorWithOptionsForTest(t, repository, publisherFunc(func(DisplaySnapshot) {}), ProcessorOptions{Now: processorNow, NewRetryTimer: timers.New, Alert: func(status ProcessorStatus) { alerts <- status }})
	if err := processor.Accept(giftEventFixture("event-1", 1, "viewer", "avatar")); err != nil {
		t.Fatal(err)
	}
	<-alerts
	(<-timers.created).Fire()
	terminal := <-alerts
	if terminal.Buffered != 0 || !terminal.Degraded || !terminal.Rejecting {
		t.Fatalf("terminal status = %#v", terminal)
	}
	if err := processor.Accept(giftEventFixture("event-2", 2, "viewer", "avatar")); !errors.Is(err, ErrPersistenceUnavailable) {
		t.Fatalf("Accept after terminal failure = %v", err)
	}
}

func TestProcessorCloseWaitsForBufferedRetryToCommit(t *testing.T) {
	repository := processorRepositoryFixture()
	attempts := 0
	repository.commit = func(_ context.Context, command configuration.RuntimeEventCommand) (configuration.RuntimeEventResult, error) {
		attempts++
		if attempts == 1 {
			return configuration.RuntimeEventResult{}, configuration.ErrUnavailable
		}
		return configuration.RuntimeEventResult{Revision: command.ExpectedRevision + 1}, nil
	}
	timers := &manualTimerFactory{created: make(chan *manualTimer, 1)}
	processor := newProcessorWithOptionsForTest(t, repository, publisherFunc(func(DisplaySnapshot) {}), ProcessorOptions{Now: processorNow, NewRetryTimer: timers.New})
	if err := processor.Accept(giftEventFixture("event-1", 1, "viewer", "avatar")); err != nil {
		t.Fatal(err)
	}
	closed := make(chan error, 1)
	go func() { closed <- processor.Close(context.Background()) }()
	select {
	case err := <-closed:
		t.Fatalf("Close returned before buffered retry: %v", err)
	default:
	}
	(<-timers.created).Fire()
	if err := <-closed; err != nil {
		t.Fatal(err)
	}
}

func TestProcessorCloseDeadlineDiscardsMemoryBufferAndAllowsLifecycleRetry(t *testing.T) {
	repository := processorRepositoryFixture()
	repository.commit = func(context.Context, configuration.RuntimeEventCommand) (configuration.RuntimeEventResult, error) {
		return configuration.RuntimeEventResult{}, configuration.ErrUnavailable
	}
	timers := &manualTimerFactory{created: make(chan *manualTimer, 1)}
	processor := newProcessorWithOptionsForTest(t, repository, publisherFunc(func(DisplaySnapshot) {}), ProcessorOptions{Now: processorNow, NewRetryTimer: timers.New})
	if err := processor.Accept(giftEventFixture("event-1", 1, "viewer", "avatar")); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := processor.Close(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("first Close() = %v, want canceled", err)
	}
	closed := make(chan error, 1)
	go func() { closed <- processor.Close(context.Background()) }()
	select {
	case err := <-closed:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("second Close hung after shutdown deadline")
	}
	if status := processor.Status(); status.Buffered != 0 || !status.Rejecting {
		t.Fatalf("forced-close status = %#v", status)
	}
}

func TestProcessorProcessLocalDuplicateNeverPersistsFingerprint(t *testing.T) {
	repository := processorRepositoryFixture()
	calls := 0
	commands := []configuration.RuntimeEventCommand{}
	repository.commit = func(_ context.Context, command configuration.RuntimeEventCommand) (configuration.RuntimeEventResult, error) {
		calls++
		if command.StableEventHash != nil {
			t.Fatal("ID-less event persisted a fingerprint")
		}
		commands = append(commands, command)
		return configuration.RuntimeEventResult{Revision: command.ExpectedRevision + 1}, nil
	}
	processor := newProcessorForTest(t, repository, publisherFunc(func(DisplaySnapshot) {}))
	viewerOne := giftEventFixture("", 1, "viewer-one", "avatar-one")
	viewerOne.Data = []byte(`{"rawSourcePrivate":"source-private"}`)
	viewerOne.Metadata = map[string]string{"source": "primary"}
	if err := processor.Accept(viewerOne); err != nil {
		t.Fatal(err)
	}
	viewerTwo := cloneProcessorEvent(viewerOne)
	viewerTwo.Viewer = roomsource.Viewer{UID: 2, Uname: "viewer-two", Avatar: "avatar-two", Ephemeral: true}
	if err := processor.Accept(viewerTwo); err != nil {
		t.Fatal(err)
	}
	if err := processor.Accept(cloneProcessorEvent(viewerTwo)); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatalf("repository calls = %d, want two distinct viewers and one suppressed exact replay", calls)
	}
	rows := processor.Viewers()
	if len(rows) != 2 || rows[0].Gifts != 1 || rows[1].Gifts != 1 {
		t.Fatalf("viewer rows = %#v, want one gift for each viewer", rows)
	}
	encoded, err := json.Marshal(commands)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"viewer-one", "avatar-one", "viewer-two", "avatar-two", "primary", "source-private"} {
		if strings.Contains(string(encoded), secret) {
			t.Fatalf("repository command leaked ID-less fingerprint input %q: %s", secret, encoded)
		}
	}
}

func TestProcessorRestartRestoresRuntimeButStartsWithEmptyViewerLedger(t *testing.T) {
	repository := processorRepositoryFixture()
	repository.state.Runtime.AttributeValues["score"] = 9
	repository.commit = func(_ context.Context, command configuration.RuntimeEventCommand) (configuration.RuntimeEventResult, error) {
		return configuration.RuntimeEventResult{Revision: command.ExpectedRevision + 1}, nil
	}
	processor := newProcessorForTest(t, repository, publisherFunc(func(DisplaySnapshot) {}))
	if rows := processor.Viewers(); len(rows) != 0 {
		t.Fatalf("restored viewer rows = %#v, want empty", rows)
	}
	if err := processor.Accept(giftEventFixture("event-after-restart", 123, "secret", "avatar")); err != nil {
		t.Fatal(err)
	}
	if value := processor.RuntimeState().AttributeValues["score"]; value != 10 {
		t.Fatalf("restored score after gift = %v, want 10", value)
	}
}

func TestPublisherScopesAndDetachesSnapshots(t *testing.T) {
	publisher := NewPublisher()
	subscription, err := publisher.Subscribe(7)
	if err != nil {
		t.Fatal(err)
	}
	defer subscription.Cancel()
	publisher.Publish(DisplaySnapshot{AccountID: 8, Revision: 1})
	select {
	case snapshot := <-subscription.Events():
		t.Fatalf("received another account snapshot: %#v", snapshot)
	default:
	}
	original := DisplaySnapshot{AccountID: 7, Revision: 2, Runtime: processorStateFixture().Runtime, Viewers: []ViewerRow{{UID: 123, Name: "secret"}}}
	publisher.Publish(original)
	original.Runtime.AttributeValues["score"] = 99
	original.Viewers[0].Name = "mutated"
	received := <-subscription.Events()
	if received.Runtime.AttributeValues["score"] != 0 || received.Viewers[0].Name != "secret" {
		t.Fatalf("published snapshot aliases caller: %#v", received)
	}
	latest, ok := publisher.Latest(7)
	if !ok || latest.Runtime.AttributeValues["score"] != 0 || latest.Viewers[0].Name != "secret" {
		t.Fatalf("Latest() = %#v, %v", latest, ok)
	}
}

func TestProcessorCloseClearsSessionViewerSnapshotsFromPublisherAndQueuedSubscribers(t *testing.T) {
	repository := processorRepositoryFixture()
	repository.commit = func(_ context.Context, command configuration.RuntimeEventCommand) (configuration.RuntimeEventResult, error) {
		return configuration.RuntimeEventResult{Revision: command.ExpectedRevision + 1}, nil
	}
	publisher := NewPublisher()
	subscription, err := publisher.Subscribe(7)
	if err != nil {
		t.Fatal(err)
	}
	defer subscription.Cancel()
	processor := newProcessorForTest(t, repository, publisher)
	if err := processor.Accept(giftEventFixture("event-1", 123, "secret", "https://secret")); err != nil {
		t.Fatal(err)
	}
	if err := processor.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if rows := processor.Viewers(); len(rows) != 0 {
		t.Fatalf("processor retained viewer PII after close: %#v", rows)
	}
	if snapshot, ok := publisher.Latest(7); ok {
		t.Fatalf("latest retained closed-session viewer snapshot: %#v", snapshot)
	}
	select {
	case snapshot := <-subscription.Events():
		t.Fatalf("subscriber retained closed-session viewer snapshot: %#v", snapshot)
	default:
	}
	newBinding := processorBindingFixture()
	newBinding.Session.ID++
	newProcessor, err := NewProcessor(context.Background(), repository, publisher, gameplay.Engine{}, newBinding, ProcessorOptions{Now: processorNow})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = newProcessor.Close(context.Background()) })
	if snapshot, ok := publisher.Latest(7); ok {
		t.Fatalf("new session observed prior viewer snapshot before its first event: %#v", snapshot)
	}
}

func TestPublisherClearRemovesOnlyExactSessionSnapshots(t *testing.T) {
	publisher := NewPublisher()
	subscription, err := publisher.Subscribe(7)
	if err != nil {
		t.Fatal(err)
	}
	defer subscription.Cancel()
	publisher.Publish(DisplaySnapshot{AccountID: 7, LiveSessionID: 81, Revision: 2, Viewers: []ViewerRow{{UID: 123, Name: "old-secret", Avatar: "old-avatar"}}})
	publisher.Publish(DisplaySnapshot{AccountID: 7, LiveSessionID: 82, Revision: 3, Viewers: []ViewerRow{{UID: 456, Name: "current", Avatar: "current-avatar"}}})
	publisher.Clear(7, 81)
	latest, ok := publisher.Latest(7)
	if !ok || latest.LiveSessionID != 82 || latest.Viewers[0].Name != "current" {
		t.Fatalf("Clear exact session removed current latest: %#v, %v", latest, ok)
	}
	queued := <-subscription.Events()
	if queued.LiveSessionID != 82 || queued.Viewers[0].UID != 456 {
		t.Fatalf("subscriber queue after exact-session clear = %#v", queued)
	}
	select {
	case extra := <-subscription.Events():
		t.Fatalf("subscriber retained extra snapshot: %#v", extra)
	default:
	}
}

func TestGameplayGiftTransformsPaidEventsWithoutViewerIdentity(t *testing.T) {
	tests := []struct {
		name   string
		event  roomsource.Event
		want   gameplay.Gift
		wantOK bool
	}{
		{
			name:   "guard captain maps Bili level 3 to gameplay rank 2",
			event:  roomsource.Event{Type: "GUARD_BUY", Data: []byte(`{"opaque":"secret"}`), Paid: &roomsource.PaidEvent{Count: 2, UnitPrice: 198000, GuardLevel: 3, OccurredAtMillis: 1786896000000}},
			want:   gameplay.Gift{GiftID: 1_900_000_001, Count: 2, Price: 198000, IdentityRank: 2, OccurredAtMillis: 1786896000000},
			wantOK: true,
		},
		{
			name:   "guard admiral maps Bili level 2 to gameplay rank 3",
			event:  roomsource.Event{Type: "GUARD_BUY", Paid: &roomsource.PaidEvent{Count: 1, UnitPrice: 1998000, GuardLevel: 2, OccurredAtMillis: 1786896000000}},
			want:   gameplay.Gift{GiftID: 1_900_000_002, Count: 1, Price: 1998000, IdentityRank: 3, OccurredAtMillis: 1786896000000},
			wantOK: true,
		},
		{
			name:   "guard governor maps Bili level 1 to gameplay rank 4",
			event:  roomsource.Event{Type: "GUARD_BUY", Paid: &roomsource.PaidEvent{Count: 1, UnitPrice: 19998000, GuardLevel: 1, OccurredAtMillis: 1786896000000}},
			want:   gameplay.Gift{GiftID: 1_900_000_003, Count: 1, Price: 19998000, IdentityRank: 4, OccurredAtMillis: 1786896000000},
			wantOK: true,
		},
		{
			name:   "nested blind gift and sender captain",
			event:  roomsource.Event{Type: "SEND_GIFT", Paid: &roomsource.PaidEvent{GiftID: 31036, BlindGiftID: 31037, Count: 2, UnitPrice: 1000, GuardLevel: 3, HasFanMedal: true, OccurredAtMillis: 1786896000000}},
			want:   gameplay.Gift{GiftID: 31036, BlindGiftID: 31037, Count: 2, Price: 1000, IdentityRank: 2, OccurredAtMillis: 1786896000000},
			wantOK: true,
		},
		{
			name:   "normal gift medal maps to fan rank",
			event:  roomsource.Event{Type: "SEND_GIFT", Paid: &roomsource.PaidEvent{GiftID: 1, Count: 1, UnitPrice: 1000, HasFanMedal: true, OccurredAtMillis: 1786896000000}},
			want:   gameplay.Gift{GiftID: 1, Count: 1, Price: 1000, IdentityRank: 1, OccurredAtMillis: 1786896000000},
			wantOK: true,
		},
		{
			name:   "super chat yuan price and fan medal",
			event:  roomsource.Event{Type: "SUPER_CHAT_MESSAGE", Paid: &roomsource.PaidEvent{Count: 1, UnitPrice: 30000, OccurredAtMillis: 1786896000000}},
			want:   gameplay.Gift{GiftID: 1_900_000_004, Count: 1, Price: 30000, OccurredAtMillis: 1786896000000},
			wantOK: true,
		},
		{
			name:   "super chat nested medal maps to fan rank",
			event:  roomsource.Event{Type: "SUPER_CHAT_MESSAGE_JPN", Paid: &roomsource.PaidEvent{Count: 1, UnitPrice: 50000, HasFanMedal: true, OccurredAtMillis: 1786896000000}},
			want:   gameplay.Gift{GiftID: 1_900_000_004, Count: 1, Price: 50000, IdentityRank: 1, OccurredAtMillis: 1786896000000},
			wantOK: true,
		},
		{
			name:  "unknown guard level fails closed",
			event: roomsource.Event{Type: "SEND_GIFT", Paid: &roomsource.PaidEvent{GiftID: 1, Count: 1, UnitPrice: 1000, GuardLevel: 9}},
		},
		{
			name:  "unknown paid command fails closed",
			event: roomsource.Event{Type: "MYSTERY_BUY", Paid: &roomsource.PaidEvent{GiftID: 1, Count: 1, UnitPrice: 1000}},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, ok := gameplayGift(test.event)
			if ok != test.wantOK || !reflect.DeepEqual(got, test.want) {
				t.Fatalf("gameplayGift() = %#v, %v, want %#v, %v", got, ok, test.want, test.wantOK)
			}
			encoded, _ := json.Marshal(got)
			if strings.Contains(string(encoded), "987654321") || strings.Contains(string(encoded), "secret") {
				t.Fatalf("gameplay gift contains viewer identity: %s", encoded)
			}
		})
	}
}

type fakeProcessorRepository struct {
	version configuration.Version
	state   configuration.State
	load    func(context.Context, int64) (configuration.Version, configuration.State, error)
	commit  func(context.Context, configuration.RuntimeEventCommand) (configuration.RuntimeEventResult, error)
}

func processorRepositoryFixture() *fakeProcessorRepository {
	return &fakeProcessorRepository{version: processorVersionFixture(), state: processorStateFixture()}
}

func newProcessorForTest(t *testing.T, repository ProcessorRepository, publisher SnapshotPublisher) *Processor {
	return newProcessorWithOptionsForTest(t, repository, publisher, ProcessorOptions{Now: processorNow})
}

func newProcessorWithOptionsForTest(t *testing.T, repository ProcessorRepository, publisher SnapshotPublisher, options ProcessorOptions) *Processor {
	t.Helper()
	processor, err := NewProcessor(context.Background(), repository, publisher, gameplay.Engine{}, processorBindingFixture(), options)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_ = processor.Close(ctx)
	})
	return processor
}

func (repository *fakeProcessorRepository) LoadActive(ctx context.Context, accountID int64) (configuration.Version, configuration.State, error) {
	if repository.load != nil {
		return repository.load(ctx, accountID)
	}
	return repository.version, repository.state, nil
}

func (repository *fakeProcessorRepository) CommitRuntimeEvent(ctx context.Context, command configuration.RuntimeEventCommand) (configuration.RuntimeEventResult, error) {
	return repository.commit(ctx, command)
}

type publisherFunc func(DisplaySnapshot)

func (publish publisherFunc) Publish(snapshot DisplaySnapshot) { publish(snapshot) }

func processorBindingFixture() ProcessorBinding {
	return ProcessorBinding{
		Owner:   OwnerFence{AccountID: 7, Token: ownerToken(0x77), Epoch: 3},
		Session: Session{ID: 81, AccountID: 7, RoomID: "42", ConfigVersionID: 51, StartedAt: processorNow()},
	}
}

func processorVersionFixture() configuration.Version {
	return configuration.Version{
		ID: 51, AccountID: 7, Number: 1, Source: "manual", CreatedAt: processorNow(),
		Definition: configuration.Definition{
			Attributes: []configuration.AttributeDefinition{{ID: "score", Name: "score"}},
			Rules:      []gameplay.Rule{{ID: "score", GiftID: 1, AttributeID: "score", Formula: "score+1"}},
			Gifts:      []configuration.GiftDefinition{{ID: 1, Name: "rose", Price: 1000, CoinType: "gold"}},
		},
	}
}

func processorStateFixture() configuration.State {
	return configuration.State{
		AccountID: 7, ConfigVersionID: 51, Revision: 1, UpdatedAt: processorNow(),
		Runtime: configuration.RuntimeState{
			AttributeValues: map[string]float64{"score": 0},
			RuleLimits:      gameplay.RuleLimitState{LocalDate: "2026-08-17", AppliedCounts: map[string]int{}},
		},
	}
}

func giftEventFixture(id string, uid int64, uname, avatar string) roomsource.Event {
	return roomsource.Event{
		ID: id, RoomID: "42", Type: "SEND_GIFT",
		Data:   []byte(`{"cmd":"SEND_GIFT","data":{"giftId":1,"num":1,"price":1000,"timestamp":1786896000}}`),
		Viewer: roomsource.Viewer{UID: uid, Uname: uname, Avatar: avatar, Ephemeral: true},
		Paid:   &roomsource.PaidEvent{GiftID: 1, Count: 1, UnitPrice: 1000, OccurredAtMillis: 1786896000000},
	}
}

func processorNow() time.Time { return time.Date(2026, 8, 17, 12, 0, 0, 0, time.UTC) }
