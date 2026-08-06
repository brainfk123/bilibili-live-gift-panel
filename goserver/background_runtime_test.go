package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

type fakeGiftSource struct {
	started chan string
	events  chan giftEvent
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
