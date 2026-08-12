package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type fakeGiftSource struct {
	started chan string
	events  chan giftEvent
}

func TestBackgroundRuntimeReplaysDurableInboxOnce(t *testing.T) {
	root := t.TempDir()
	store := &configStore{path: filepath.Join(root, "config.json")}
	state := defaultAppState()
	state.Attributes = []attributeState{{Name: "积分", Value: 0}}
	state.Rules = []giftRule{{ID: "r1", GiftID: 1, AttributeName: "积分", Formula: "积分+1"}}
	if err := store.replaceState(state); err != nil {
		t.Fatal(err)
	}

	firstInbox, err := openGiftInbox(root)
	if err != nil {
		t.Fatal(err)
	}
	firstRuntime := newBackgroundRuntime(store, nil)
	firstRuntime.inbox = firstInbox
	firstRuntime.acceptGift(context.Background(), "room", "SEND_GIFT", giftEvent{
		GiftID: 1, GiftName: "礼物", Num: 1, Timestamp: time.Now().Unix(), Rnd: "restart-rnd",
	})
	if health := firstInbox.Health(); health.PendingCount != 1 {
		t.Fatalf("pending before restart = %d, want 1", health.PendingCount)
	}
	canceledContext, cancelFirst := context.WithCancel(context.Background())
	cancelFirst()
	firstRuntime.Run(canceledContext)
	if health := firstInbox.Health(); health.PendingCount != 1 {
		t.Fatalf("pending after cancellation before consumption = %d, want 1", health.PendingCount)
	}

	secondInbox, err := openGiftInbox(root)
	if err != nil {
		t.Fatal(err)
	}
	secondRuntime := newBackgroundRuntime(store, nil)
	secondRuntime.inbox = secondInbox
	cancelSecond, secondDone := startBackgroundRuntimeForTest(secondRuntime)
	waitForInboxPendingCount(t, secondInbox, 0)
	cancelSecond()
	<-secondDone
	assertRuntimeAttributeValue(t, store, "积分", 1)

	thirdInbox, err := openGiftInbox(root)
	if err != nil {
		t.Fatal(err)
	}
	thirdRuntime := newBackgroundRuntime(store, nil)
	thirdRuntime.inbox = thirdInbox
	cancelThird, thirdDone := startBackgroundRuntimeForTest(thirdRuntime)
	waitForInboxPendingCount(t, thirdInbox, 0)
	cancelThird()
	<-thirdDone
	assertRuntimeAttributeValue(t, store, "积分", 1)

	duplicateInbox, err := openGiftInbox(root)
	if err != nil {
		t.Fatal(err)
	}
	duplicateRuntime := newBackgroundRuntime(store, nil)
	duplicateRuntime.inbox = duplicateInbox
	duplicateRuntime.inboxRetryDelay = time.Hour
	for range 2 {
		duplicateRuntime.acceptGift(context.Background(), "room", "SEND_GIFT", giftEvent{
			GiftID: 1, GiftName: "礼物", Num: 1, Timestamp: time.Now().Unix(), Rnd: "durable-duplicate-rnd",
		})
	}
	writes := 0
	store.writeAtomically = func(path string, data []byte) error {
		if filepath.Base(path) == "events.log" {
			writes++
			if writes == 2 {
				return errors.New("injected duplicate settlement failure")
			}
		}
		return writeFileAtomically(path, data)
	}
	cancelDuplicate, duplicateDone := startBackgroundRuntimeForTest(duplicateRuntime)
	waitForRuntimeError(t, duplicateRuntime, "injected duplicate settlement failure")
	cancelDuplicate()
	<-duplicateDone
	if health := duplicateInbox.Health(); health.PendingCount != 1 {
		t.Fatalf("pending after prepared duplicate failure = %d, want 1", health.PendingCount)
	}

	recoveredStore := &configStore{path: filepath.Join(root, "config.json")}
	recoveredInbox, err := openGiftInbox(root)
	if err != nil {
		t.Fatal(err)
	}
	recoveredRuntime := newBackgroundRuntime(recoveredStore, nil)
	recoveredRuntime.inbox = recoveredInbox
	cancelRecovered, recoveredDone := startBackgroundRuntimeForTest(recoveredRuntime)
	waitForInboxPendingCount(t, recoveredInbox, 0)
	cancelRecovered()
	<-recoveredDone
	assertRuntimeAttributeValue(t, recoveredStore, "积分", 2)
	recoveredState, err := recoveredStore.readState()
	if err != nil {
		t.Fatal(err)
	}
	if len(recoveredState.AppliedIngressIDs) != 3 {
		t.Fatalf("applied ingestion IDs = %d, want 3", len(recoveredState.AppliedIngressIDs))
	}
	if _, exists := recoveredState.RecentSourceGiftKeys["durable-duplicate-rnd"]; !exists {
		t.Fatalf("recent source keys = %#v", recoveredState.RecentSourceGiftKeys)
	}
}

type blockingUserProfileResolver struct {
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func (resolver *blockingUserProfileResolver) Resolve(_ context.Context, uid int64) (userProfile, error) {
	resolver.once.Do(func() { close(resolver.started) })
	<-resolver.release
	return userProfile{UID: uid, Name: "完整昵称"}, nil
}

func TestBackgroundRuntimeIngressDoesNotWaitForProfile(t *testing.T) {
	root := t.TempDir()
	store := &configStore{path: filepath.Join(root, "config.json")}
	state := defaultAppState()
	state.Attributes = []attributeState{{Name: "积分", Value: 0}}
	state.Rules = []giftRule{{ID: "r1", GiftID: 1, AttributeName: "积分", Formula: "积分+1"}}
	if err := store.replaceState(state); err != nil {
		t.Fatal(err)
	}
	inbox, err := openGiftInbox(root)
	if err != nil {
		t.Fatal(err)
	}
	runtime := newBackgroundRuntime(store, nil)
	runtime.inbox = inbox
	resolver := &blockingUserProfileResolver{started: make(chan struct{}), release: make(chan struct{})}
	runtime.profileResolver = resolver
	cancel, done := startBackgroundRuntimeForTest(runtime)
	defer func() {
		cancel()
		<-done
	}()

	runtime.acceptGift(context.Background(), "room", "SEND_GIFT", giftEvent{
		GiftID: 1, GiftName: "礼物", Num: 1, UID: 42, Uname: "字***", Timestamp: time.Now().Unix(), Rnd: "slow-000",
	})
	select {
	case <-resolver.started:
	case <-time.After(time.Second):
		t.Fatal("profile resolver did not block the inbox consumer")
	}

	const additional = 300
	accepted := make(chan struct{})
	go func() {
		defer close(accepted)
		for index := 1; index <= additional; index++ {
			runtime.acceptGift(context.Background(), "room", "SEND_GIFT", giftEvent{
				GiftID: 1, GiftName: "礼物", Num: 1, Timestamp: time.Now().Unix(), Rnd: "slow-" + leftPadThree(index),
			})
		}
	}()
	select {
	case <-accepted:
	case <-time.After(30 * time.Second):
		t.Fatal("durable gift acceptance waited for the blocked profile resolver")
	}
	if health := inbox.Health(); health.PendingCount != additional+1 {
		t.Fatalf("pending while profile is blocked = %d, want %d", health.PendingCount, additional+1)
	}

	close(resolver.release)
	waitForInboxPendingCount(t, inbox, 0)
	assertRuntimeAttributeValue(t, store, "积分", additional+1)
	updated, err := store.readState()
	if err != nil {
		t.Fatal(err)
	}
	if len(updated.Log) == 0 || updated.Log[0].EventID != "slow-300:积分" {
		t.Fatalf("newest ordered log entry = %#v", updated.Log)
	}
}

type capacityFailureInbox struct{}

func (*capacityFailureInbox) Accept(string, string, giftEvent) (giftInboxRecord, error) {
	return giftInboxRecord{}, errGiftInboxCapacity
}

func (*capacityFailureInbox) Next() (giftInboxRecord, bool, error) {
	return giftInboxRecord{}, false, nil
}

func (*capacityFailureInbox) Acknowledge(string) error { return nil }
func (*capacityFailureInbox) Release(string) error     { return nil }
func (*capacityFailureInbox) Close() error             { return nil }
func (*capacityFailureInbox) Health() giftInboxHealth  { return giftInboxHealth{CapacityError: true} }

func TestBackgroundRuntimeReportsInboxCapacityWithoutVolatileApply(t *testing.T) {
	store := &configStore{path: filepath.Join(t.TempDir(), "config.json")}
	state := defaultAppState()
	state.Attributes = []attributeState{{Name: "积分", Value: 0}}
	state.Rules = []giftRule{{ID: "r1", GiftID: 1, AttributeName: "积分", Formula: "积分+1"}}
	if err := store.replaceState(state); err != nil {
		t.Fatal(err)
	}
	runtime := newBackgroundRuntime(store, nil)
	runtime.inbox = &capacityFailureInbox{}

	runtime.acceptGift(context.Background(), "room", "SEND_GIFT", giftEvent{GiftID: 1, GiftName: "礼物", Num: 1})

	if got := runtime.Status().LastError; got != errGiftInboxCapacity.Error() {
		t.Fatalf("runtime error = %q, want %q", got, errGiftInboxCapacity.Error())
	}
	assertRuntimeAttributeValue(t, store, "积分", 0)
}

func TestBackgroundRuntimeTransactionRecoveryFailurePreservesInboxOrder(t *testing.T) {
	root := t.TempDir()
	store := &configStore{path: filepath.Join(root, "config.json")}
	state := defaultAppState()
	state.Attributes = []attributeState{{Name: "积分", Value: 0}}
	state.Rules = []giftRule{{ID: "r1", GiftID: 1, AttributeName: "积分", Formula: "积分+1"}}
	if err := store.replaceState(state); err != nil {
		t.Fatal(err)
	}
	inbox, err := openGiftInbox(root)
	if err != nil {
		t.Fatal(err)
	}
	for index := range 2 {
		if _, err := inbox.Accept("room", "SEND_GIFT", giftEvent{
			GiftID: 1, GiftName: "礼物", Num: 1, Timestamp: time.Now().Unix(), Rnd: "blocked-" + leftPadThree(index),
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(store.stateTransactionPath(), []byte("not-json\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runtime := newBackgroundRuntime(store, nil)
	runtime.inbox = inbox
	runtime.inboxRetryDelay = time.Hour
	cancel, done := startBackgroundRuntimeForTest(runtime)
	waitForRuntimeError(t, runtime, "读取状态事务失败")
	cancel()
	<-done

	if health := inbox.Health(); health.PendingCount != 2 {
		t.Fatalf("pending after transaction recovery failure = %d, want 2", health.PendingCount)
	}
	if err := os.Remove(store.stateTransactionPath()); err != nil {
		t.Fatal(err)
	}
	assertRuntimeAttributeValue(t, &configStore{path: filepath.Join(root, "config.json")}, "积分", 0)
}

func leftPadThree(value int) string {
	return fmt.Sprintf("%03d", value)
}

func startBackgroundRuntimeForTest(runtime *backgroundRuntime) (context.CancelFunc, <-chan struct{}) {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		runtime.Run(ctx)
	}()
	return cancel, done
}

func waitForInboxPendingCount(t *testing.T, inbox runtimeGiftInbox, want int) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if inbox.Health().PendingCount == want {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("pending count = %d, want %d", inbox.Health().PendingCount, want)
}

func waitForRuntimeError(t *testing.T, runtime *backgroundRuntime, want string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(runtime.Status().LastError, want) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("runtime status = %#v, want error containing %q", runtime.Status(), want)
}

func assertRuntimeAttributeValue(t *testing.T, store *configStore, name string, want float64) {
	t.Helper()
	state, err := store.readState()
	if err != nil {
		t.Fatal(err)
	}
	attribute := state.findAttribute(name)
	if attribute == nil || attribute.Value != want {
		t.Fatalf("attribute %q = %#v, want %v", name, attribute, want)
	}
}

func (f *fakeGiftSource) Run(ctx context.Context, roomID string, callbacks runtimeCallbacks) error {
	callbacks.onState("connected")
	f.started <- roomID
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case gift := <-f.events:
			callbacks.onGift(gift)
		}
	}
}

func TestBackgroundRuntimeProcessesGiftWithoutDisplayPage(t *testing.T) {
	store := &configStore{path: filepath.Join(t.TempDir(), "config.json")}
	state := defaultAppState()
	state.RoomID = "31567150"
	state.Attributes = []attributeState{{Name: "加班时间", Value: 0, Unit: "seconds", Format: "hhmmss"}}
	state.Rules = []giftRule{{ID: "r1", GiftID: 33300, AttributeName: "加班时间", FormulaName: "每个加一分钟", Formula: "加班时间+60"}}
	state.GiftCatalog = []giftInfo{{ID: 33300, Name: "666", Price: 1000, CoinType: "gold"}}
	if err := store.replaceState(state); err != nil {
		t.Fatal(err)
	}

	source := &fakeGiftSource{started: make(chan string, 1), events: make(chan giftEvent, 1)}
	runtime := newBackgroundRuntime(store, func() giftEventSource { return source })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go runtime.Run(ctx)

	select {
	case roomID := <-source.started:
		if roomID != "31567150" {
			t.Fatalf("connected room = %s", roomID)
		}
	case <-time.After(time.Second):
		t.Fatal("background runtime did not connect")
	}

	source.events <- giftEvent{GiftID: 33012, GiftName: "666", Num: 1, Price: 1000, CoinType: "gold", Timestamp: 1700000000, Rnd: "gift-1"}
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		updated, err := store.readState()
		if err == nil && len(updated.Attributes) == 1 && updated.Attributes[0].Value == 60 {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("gift did not update the attribute in the disk configuration")
}

func TestBackgroundRuntimePersistsBlindBoxMappingsFromRoomCatalog(t *testing.T) {
	store := &configStore{path: filepath.Join(t.TempDir(), "config.json")}
	if err := store.replaceState(defaultAppState()); err != nil {
		t.Fatal(err)
	}
	runtime := newBackgroundRuntime(store, nil)
	runtime.mergeBlindBoxGiftCatalog([]roomGiftInfo{
		{ID: 35800, Name: "小熊虫盲盒", Price: 9000, CoinType: "gold", BlindBoxParent: true},
		{ID: 35801, Name: "心事虫虫", Price: 12000, CoinType: "gold", BlindBoxParentID: 35800, BlindBoxParentName: "小熊虫盲盒", BlindBoxParentPrice: 9000},
		{ID: 31164, Name: "粉丝团灯牌", Price: 100, CoinType: "gold"},
	})

	state, err := store.readState()
	if err != nil {
		t.Fatal(err)
	}
	if len(state.GiftCatalog) != 2 {
		t.Fatalf("blind box catalog = %#v", state.GiftCatalog)
	}
	child := state.findGift(35801)
	if child == nil || child.BlindBoxParentID != 35800 || child.BlindBoxParentPrice != 9000 {
		t.Fatalf("blind box child mapping = %#v", child)
	}
}

func TestBackgroundRuntimeAttachesGuardAnimationInEitherEventOrder(t *testing.T) {
	for _, animationFirst := range []bool{false, true} {
		name := "purchase-first"
		if animationFirst {
			name = "animation-first"
		}
		t.Run(name, func(t *testing.T) {
			store := &configStore{path: filepath.Join(t.TempDir(), "config.json")}
			state := defaultAppState()
			state.RoomID = "31567150"
			if err := store.replaceState(state); err != nil {
				t.Fatal(err)
			}
			runtime := newBackgroundRuntime(store, nil)
			purchase := giftEvent{
				GiftID: specialGiftGuardCaptain, GiftName: "大航海·舰长", Num: 1,
				UID: 42, Uname: "舰长观众", Avatar: "https://example.test/avatar.png",
				Timestamp: 1700000000, Rnd: "guard-order",
			}
			animation := giftEvent{
				GiftID: specialGiftGuardCaptain, UID: 42, Timestamp: 1700000001,
				Membership: "captain", EffectID: 9001,
				EffectMP4: "https://i0.hdslb.com/guard.mp4", EffectMP4JSON: "https://i0.hdslb.com/guard.json",
				AnimationOnly: true,
			}
			if animationFirst {
				runtime.handleGift(animation)
				runtime.handleGift(purchase)
			} else {
				runtime.handleGift(purchase)
				runtime.handleGift(animation)
			}
			updated, err := store.readState()
			if err != nil {
				t.Fatal(err)
			}
			if len(updated.GiftReceipts) != 1 || updated.GiftReceipts[0].Animation == nil || updated.GiftReceipts[0].Animation.EffectID != 9001 {
				t.Fatalf("guard receipt = %#v", updated.GiftReceipts)
			}
		})
	}
}

func TestSameGiftIdentityKeepsMatchingAfterIconRevision(t *testing.T) {
	configured := giftInfo{ID: 970001, Name: "情书", Price: 5200, CoinType: "gold", ImgBasic: "old.png"}
	event := giftEvent{GiftID: 970002, GiftName: "情书", Price: 5200, CoinType: "gold", ImgBasic: "current.png"}
	if !sameGiftIdentity(configured, event) {
		t.Fatal("icon-only gift revision should remain a runtime alias")
	}
}

func TestApplyGiftEventAggregatesBatchForAttributeBroadcast(t *testing.T) {
	state := defaultAppState()
	state.Attributes = []attributeState{{Name: "加班时间", Value: 0, Unit: "seconds", Format: "hhmmss"}}
	state.Rules = []giftRule{{ID: "r1", GiftID: 33300, AttributeName: "加班时间", FormulaName: "每个加一分钟", Formula: "加班时间+60"}}
	state.GiftCatalog = []giftInfo{{ID: 33300, Name: "666", Price: 1000, CoinType: "gold"}}

	applyGiftEvent(&state, giftEvent{
		GiftID: 33300, GiftName: "666", Num: 3, Price: 1000, CoinType: "gold",
		Uname: "昵称很长的观众", Avatar: "https://example.test/avatar.png", UID: 123456789,
		Timestamp: 1700000000, Rnd: "gift-batch-1",
	})

	if state.Attributes[0].Value != 180 {
		t.Fatalf("attribute value = %v", state.Attributes[0].Value)
	}
	if len(state.Log) != 1 {
		t.Fatalf("log entries = %d, want 1: %#v", len(state.Log), state.Log)
	}
	entry := state.Log[0]
	if entry.Num != 3 || entry.Delta != 180 || entry.ValueAfter != 180 {
		t.Fatalf("aggregated log = %#v", entry)
	}
	if entry.TriggerName != "每个加一分钟" {
		t.Fatalf("trigger name = %q", entry.TriggerName)
	}
	if entry.Avatar != "https://example.test/avatar.png" || entry.SenderUID != 123456789 || entry.EventID == "" || entry.Source != "gift" {
		t.Fatalf("broadcast metadata = %#v", entry)
	}
}

func TestApplyGiftEventUpdatesGiftKPIWithoutRules(t *testing.T) {
	state := defaultAppState()
	state.GiftKPIPanels = []giftKPIPanelState{{
		ID: "kpi-1", Name: "本场礼物目标", Layout: "grid",
		Items:      []giftKPIItemState{{GiftID: 33300, GiftName: "666", Target: 10, BarStyle: "progress"}},
		Appearance: displayAppearanceState{ThemeID: "glass", FontSize: 48, AccentColor: "#fb7299", Align: "center", PanelOpacity: 55},
	}}

	applyGiftEvent(&state, giftEvent{GiftID: 33300, GiftName: "666", Num: 3, Timestamp: 1700000000})

	if got := state.GiftKPIPanels[0].Items[0].Received; got != 3 {
		t.Fatalf("received = %d, want 3", got)
	}
	if len(state.Rules) != 0 || len(state.Attributes) != 0 {
		t.Fatalf("gift KPI should not create rules or attributes: state=%#v", state)
	}
}

func TestApplyGiftEventUpdatesBlindBoxParentAndRewardKPI(t *testing.T) {
	state := defaultAppState()
	state.GiftKPIPanels = []giftKPIPanelState{{
		ID: "kpi-blind-box", Name: "盲盒礼物目标", Layout: "grid",
		Items: []giftKPIItemState{
			{GiftID: 35800, GiftName: "小熊虫盲盒", Target: 10, BarStyle: "progress"},
			{GiftID: 35801, GiftName: "心事虫虫", Target: 10, BarStyle: "progress"},
		},
		Appearance: displayAppearanceState{ThemeID: "glass", FontSize: 48, AccentColor: "#fb7299", Align: "center", PanelOpacity: 55},
	}}

	applyGiftEvent(&state, giftEvent{
		GiftID: 35801, BlindGiftID: 35800, GiftName: "心事虫虫", BlindGiftName: "小熊虫盲盒",
		Num: 2, Price: 12000, BlindGiftPrice: 9000, CoinType: "gold", Timestamp: 1700000000,
	})

	parentReceived := state.GiftKPIPanels[0].Items[0].Received
	rewardReceived := state.GiftKPIPanels[0].Items[1].Received
	if parentReceived != 2 || rewardReceived != 2 {
		t.Fatalf("blind box KPI received parent=%d reward=%d, want parent=2 reward=2", parentReceived, rewardReceived)
	}
}

func TestApplyGiftEventUpdatesBlindBoxParentKPIFromCatalog(t *testing.T) {
	state := defaultAppState()
	state.GiftCatalog = []giftInfo{
		{ID: 35800, Name: "小熊虫盲盒", Price: 9000, CoinType: "gold"},
		{ID: 35801, Name: "心事虫虫", Price: 12000, CoinType: "gold", BlindBoxParentID: 35800, BlindBoxParentName: "小熊虫盲盒", BlindBoxParentPrice: 9000},
	}
	state.GiftKPIPanels = []giftKPIPanelState{{
		ID: "kpi-blind-box", Name: "盲盒礼物目标", Layout: "grid",
		Items:      []giftKPIItemState{{GiftID: 35800, GiftName: "小熊虫盲盒", Target: 10, BarStyle: "progress"}},
		Appearance: displayAppearanceState{ThemeID: "glass", FontSize: 48, AccentColor: "#fb7299", Align: "center", PanelOpacity: 55},
	}}

	applyGiftEvent(&state, giftEvent{
		GiftID: 35801, GiftName: "心事虫虫", Num: 3, Price: 12000, CoinType: "gold", Timestamp: 1700000000,
	})

	if got := state.GiftKPIPanels[0].Items[0].Received; got != 3 {
		t.Fatalf("catalog-mapped blind box KPI received = %d, want 3", got)
	}
}

func TestApplyGiftEventUpdatesEveryMatchingAttribute(t *testing.T) {
	state := defaultAppState()
	state.Attributes = []attributeState{
		{Name: "早播", Value: 0, Unit: "none", Format: "number"},
		{Name: "积分", Value: 10, Unit: "none", Format: "number"},
	}
	state.Rules = []giftRule{
		{ID: "r-early", GiftID: 33300, AttributeName: "早播", Formula: "早播+1"},
		{ID: "r-score", GiftID: 33300, AttributeName: "积分", Formula: "积分+2"},
	}
	state.GiftCatalog = []giftInfo{{ID: 33300, Name: "666", Price: 1000, CoinType: "gold"}}

	applyGiftEvent(&state, giftEvent{
		GiftID: 33300, GiftName: "666", Num: 1, Price: 1000, CoinType: "gold",
		Timestamp: 1700000000, Rnd: "multi-attribute-gift-1",
	})

	if state.Attributes[0].Value != 1 || state.Attributes[1].Value != 12 {
		t.Fatalf("matching attributes = %#v, want early=1 and score=12", state.Attributes)
	}
	if len(state.Log) != 2 {
		t.Fatalf("log entries = %d, want 2: %#v", len(state.Log), state.Log)
	}
}

func TestApplyGiftEventUsesPaidEventAmountForRulesAndContributions(t *testing.T) {
	state := defaultAppState()
	state.Attributes = []attributeState{{Name: "加班时间", Value: 0, Unit: "seconds", Format: "hhmmss"}}
	state.Rules = []giftRule{{
		ID: "super-chat-time", GiftID: specialGiftSuperChat, AttributeName: "加班时间",
		FormulaName: "每元增加一秒", Formula: "加班时间+price/1000",
	}}
	state.GiftCatalog = []giftInfo{{ID: specialGiftSuperChat, Name: "Super Chat", Price: 30000, CoinType: "gold"}}

	applyGiftEvent(&state, giftEvent{
		GiftID: specialGiftSuperChat, GiftName: "Super Chat", Num: 1,
		Price: 50000, TotalCoin: 50000, CoinType: "gold",
		UID: 123, Uname: "醒目留言观众", Timestamp: 1700000400, Rnd: "super-chat:runtime-1",
	})

	if state.Attributes[0].Value != 50 || len(state.Log) != 1 {
		t.Fatalf("super chat rule result: attribute=%#v log=%#v", state.Attributes, state.Log)
	}
	if len(state.Contributions.Viewers) != 1 || state.Contributions.Viewers[0].GoldValue != 50000 {
		t.Fatalf("super chat contribution = %#v", state.Contributions)
	}
}

func TestApplyGiftEventRepeatsGuardRuleForPurchasedQuantity(t *testing.T) {
	state := defaultAppState()
	state.Attributes = []attributeState{{Name: "舰长月数", Value: 0, Unit: "none", Format: "number"}}
	state.Rules = []giftRule{{
		ID: "captain-months", GiftID: specialGiftGuardCaptain, AttributeName: "舰长月数",
		Formula: "舰长月数+1",
	}}

	applyGiftEvent(&state, giftEvent{
		GiftID: specialGiftGuardCaptain, GiftName: "大航海·舰长", Num: 3,
		Price: 198000, TotalCoin: 594000, CoinType: "gold",
		UID: 456, Uname: "大航海观众", Timestamp: 1700000500, Rnd: "guard:runtime-1",
	})

	if state.Attributes[0].Value != 3 || len(state.Log) != 1 || state.Log[0].Num != 3 {
		t.Fatalf("guard rule result: attribute=%#v log=%#v", state.Attributes, state.Log)
	}
}

func TestApplyGiftEventMatchesBlindBoxParentFromEvent(t *testing.T) {
	state := defaultAppState()
	state.Attributes = []attributeState{{Name: "加班时间", Value: 0, Unit: "seconds", Format: "hhmmss"}}
	state.Rules = []giftRule{{ID: "blind-rule", GiftID: 35800, AttributeName: "加班时间", Formula: "加班时间+60"}}
	state.GiftCatalog = []giftInfo{{ID: 35800, Name: "小熊虫盲盒", Price: 9000, CoinType: "gold"}}

	applyGiftEvent(&state, giftEvent{
		GiftID: 35801, BlindGiftID: 35800, GiftName: "心事虫虫", Num: 1, Price: 9000,
		Timestamp: 1700000000, Rnd: "blind-parent-event",
	})

	if state.Attributes[0].Value != 60 || len(state.Log) != 1 {
		t.Fatalf("blind box event did not trigger parent rule: attribute=%v log=%#v", state.Attributes[0].Value, state.Log)
	}
}

func TestApplyGiftEventSkipsDisabledGiftRule(t *testing.T) {
	disabled := false
	state := defaultAppState()
	state.Attributes = []attributeState{{Name: "加班时间", Value: 120, Unit: "seconds", Format: "hhmmss"}}
	state.Rules = []giftRule{{
		ID: "r1", GiftID: 33300, AttributeName: "加班时间", Formula: "加班时间+60", Enabled: &disabled,
	}}

	applyGiftEvent(&state, giftEvent{
		GiftID: 33300, GiftName: "666", Num: 1, Price: 1000, CoinType: "gold",
		Timestamp: 1700000000, Rnd: "disabled-gift-rule",
	})

	if state.Attributes[0].Value != 120 {
		t.Fatalf("disabled rule changed attribute value to %v", state.Attributes[0].Value)
	}
	if len(state.Log) != 0 {
		t.Fatalf("disabled rule created log entries: %#v", state.Log)
	}
}

type fakeUserProfileResolver struct {
	profile userProfile
	calls   int
}

func (resolver *fakeUserProfileResolver) Resolve(_ context.Context, _ int64) (userProfile, error) {
	resolver.calls++
	return resolver.profile, nil
}

func TestBackgroundRuntimeEnrichesMaskedSenderFromAnonymousProfile(t *testing.T) {
	store := &configStore{path: filepath.Join(t.TempDir(), "config.json")}
	state := defaultAppState()
	state.Attributes = []attributeState{{Name: "早播", Value: 0, Unit: "none", Format: "number"}}
	state.Rules = []giftRule{{ID: "r1", GiftID: 1, AttributeName: "早播", Formula: "早播+1"}}
	if err := store.replaceState(state); err != nil {
		t.Fatal(err)
	}
	profiles := &fakeUserProfileResolver{profile: userProfile{
		UID: 123456789, Name: "完整昵称", Avatar: "https://example.test/full-avatar.png",
	}}
	runtime := newBackgroundRuntime(store, nil)
	runtime.profileResolver = profiles

	runtime.handleGift(giftEvent{
		GiftID: 1, GiftName: "人气票", Num: 1, Price: 100, CoinType: "gold",
		UID: 123456789, Uname: "字***", Timestamp: 1700000000, Rnd: "masked-gift-1",
	})

	updated, err := store.readState()
	if err != nil {
		t.Fatal(err)
	}
	if len(updated.Log) != 1 {
		t.Fatalf("log = %#v", updated.Log)
	}
	entry := updated.Log[0]
	if entry.SenderUID != 123456789 || entry.Uname != "完整昵称" || entry.Avatar != "https://example.test/full-avatar.png" {
		t.Fatalf("enriched log = %#v", entry)
	}
	if profiles.calls != 1 {
		t.Fatalf("profile resolver calls = %d", profiles.calls)
	}
}

func TestBilibiliUserProfileResolverQueriesAnonymousCardAndCaches(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.URL.Query().Get("mid") != "123456789" {
			t.Errorf("mid = %q", r.URL.Query().Get("mid"))
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"code":0,"message":"OK","data":{"card":{"mid":"123456789","name":"完整昵称","face":"https://example.test/avatar.png"}}}`))
	}))
	defer server.Close()

	resolver := newBilibiliUserProfileResolver(server.Client(), server.URL)
	for range 2 {
		profile, err := resolver.Resolve(context.Background(), 123456789)
		if err != nil {
			t.Fatal(err)
		}
		if profile.Name != "完整昵称" || profile.Avatar != "https://example.test/avatar.png" {
			t.Fatalf("profile = %#v", profile)
		}
	}
	if requests.Load() != 1 {
		t.Fatalf("anonymous profile requests = %d, want 1", requests.Load())
	}
}

func TestBilibiliUserProfileResolverCachesFailures(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		http.Error(w, "rate limited", http.StatusTooManyRequests)
	}))
	defer server.Close()

	resolver := newBilibiliUserProfileResolver(server.Client(), server.URL)
	for range 2 {
		if _, err := resolver.Resolve(context.Background(), 123456789); err == nil {
			t.Fatal("rate-limited profile request unexpectedly succeeded")
		}
	}
	if requests.Load() != 1 {
		t.Fatalf("failed profile requests = %d, want 1", requests.Load())
	}
}

func TestBackgroundRuntimeProcessesTimerWithoutRoomOrDisplayPage(t *testing.T) {
	store := &configStore{path: filepath.Join(t.TempDir(), "config.json")}
	state := defaultAppState()
	state.Attributes = []attributeState{{Name: "加班时间", Value: 120, Unit: "seconds", Format: "hhmmss"}}
	state.TimerRules = []timerRule{{
		ID: "timer-1", AttributeName: "加班时间", FormulaName: "每分钟减少",
		IntervalSeconds: 60, Formula: "MAX(加班时间-60,0)", Enabled: true,
	}}
	if err := store.replaceState(state); err != nil {
		t.Fatal(err)
	}

	ticks := make(chan time.Time, 2)
	runtime := newBackgroundRuntime(store, nil)
	runtime.timerTicks = ticks
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go runtime.Run(ctx)

	startedAt := time.Unix(1700000000, 0)
	ticks <- startedAt
	ticks <- startedAt.Add(60 * time.Second)

	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		updated, err := store.readState()
		if err == nil && len(updated.Attributes) == 1 && updated.Attributes[0].Value == 60 {
			if len(updated.Log) == 0 || updated.Log[0].Source != "timer" || updated.Log[0].RuleID != "timer-1" {
				t.Fatalf("timer log = %#v", updated.Log)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("timer did not update the attribute while room and display were absent")
}

func TestTimerConditionSkipsOnlyTheCurrentOccurrence(t *testing.T) {
	state := defaultAppState()
	state.Attributes = []attributeState{{Name: "加班时间", Value: 0, Unit: "seconds", Format: "hhmmss"}}
	state.TimerRules = []timerRule{{
		ID: "timer-1", AttributeName: "加班时间", FormulaName: "大于零时减少",
		IntervalSeconds: 60, Condition: "加班时间>0", Formula: "MAX(加班时间-60,0)", Enabled: true,
	}}

	if applied := applyTimerRules(&state, []string{"timer-1"}, time.Unix(1700000000, 0)); applied != 0 {
		t.Fatalf("condition-false timer applied %d times", applied)
	}
	if state.Attributes[0].Value != 0 || len(state.Log) != 0 {
		t.Fatalf("condition-false timer changed state: attribute=%v log=%#v", state.Attributes[0].Value, state.Log)
	}

	state.Attributes[0].Value = 120
	if applied := applyTimerRules(&state, []string{"timer-1"}, time.Unix(1700000060, 0)); applied != 1 {
		t.Fatalf("condition-true timer applied %d times", applied)
	}
	if state.Attributes[0].Value != 60 || len(state.Log) != 1 || state.Log[0].Source != "timer" {
		t.Fatalf("condition-true timer state: attribute=%v log=%#v", state.Attributes[0].Value, state.Log)
	}
}

func TestTimerScheduleWaitsForAFullFirstInterval(t *testing.T) {
	runtime := newBackgroundRuntime(nil, nil)
	state := defaultAppState()
	state.TimerRules = []timerRule{{ID: "timer-1", IntervalSeconds: 60, Enabled: true}}
	startedAt := time.Unix(1700000000, 0)

	if due := runtime.dueTimerRuleIDs(state, startedAt); len(due) != 0 {
		t.Fatalf("new timer was due immediately: %v", due)
	}
	if due := runtime.dueTimerRuleIDs(state, startedAt.Add(59*time.Second)); len(due) != 0 {
		t.Fatalf("new timer was due before one full interval: %v", due)
	}
	if due := runtime.dueTimerRuleIDs(state, startedAt.Add(60*time.Second)); len(due) != 1 || due[0] != "timer-1" {
		t.Fatalf("timer due after one interval = %v", due)
	}
}

func TestTimerConfigChangeRestartsScheduleFromReenable(t *testing.T) {
	runtime := newBackgroundRuntime(nil, nil)
	state := defaultAppState()
	state.TimerRules = []timerRule{{ID: "timer-1", IntervalSeconds: 60, Enabled: true}}
	startedAt := time.Unix(1700000000, 0)

	if due := runtime.dueTimerRuleIDs(state, startedAt); len(due) != 0 {
		t.Fatalf("timer was due immediately: %v", due)
	}
	if due := runtime.dueTimerRuleIDs(state, startedAt.Add(30*time.Second)); len(due) != 0 {
		t.Fatalf("timer was due before its original interval: %v", due)
	}

	runtime.NotifyTimerConfigChanged()
	if due := runtime.dueTimerRuleIDs(state, startedAt.Add(30*time.Second)); len(due) != 0 {
		t.Fatalf("timer was due immediately after re-enable: %v", due)
	}
	if due := runtime.dueTimerRuleIDs(state, startedAt.Add(60*time.Second)); len(due) != 0 {
		t.Fatalf("timer reused its old schedule after re-enable: %v", due)
	}
	if due := runtime.dueTimerRuleIDs(state, startedAt.Add(90*time.Second)); len(due) != 1 || due[0] != "timer-1" {
		t.Fatalf("timer due after restarted interval = %v", due)
	}
}
