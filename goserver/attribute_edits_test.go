package main

import (
	"context"
	"errors"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestRewriteFormulaIdentifierPreservesFormattingAndSubstrings(t *testing.T) {
	cases := []struct{ input, want string }{
		{"积分 + MAX(积分, 积分2)", "能量 + MAX(能量, 积分2)"},
		{"IF(积分>=10,积分,0)", "IF(能量>=10,能量,0)"},
		{"RANDOMCHOICE( 积分 , 1 )", "RANDOMCHOICE( 能量 , 1 )"},
	}
	for _, tc := range cases {
		got, err := rewriteFormulaIdentifier(tc.input, "积分", "能量")
		if err != nil {
			t.Fatal(err)
		}
		if got != tc.want {
			t.Fatalf("rewrite %q = %q, want %q", tc.input, got, tc.want)
		}
	}
}

func TestRewriteFormulaIdentifierReturnsTokenizerErrorWithoutOutput(t *testing.T) {
	got, err := rewriteFormulaIdentifier("积分 +", "积分", "能量")
	if err == nil {
		t.Fatal("expected parser error")
	}
	if got != "" {
		t.Fatalf("output = %q, want no partial output", got)
	}
}

func TestRewriteAttributeReferencesRenamesConfigurationButNotHistory(t *testing.T) {
	state := attributeEditFixtureState()
	if err := rewriteAttributeReferences(&state, "积分", "能量"); err != nil {
		t.Fatal(err)
	}
	if state.Rules[0].AttributeName != "能量" || state.Rules[0].Formula != "能量 + 1" || state.Rules[2].Condition != "能量 >= 0" {
		t.Fatalf("gift rule = %#v", state.Rules[0])
	}
	if state.TimerRules[0].AttributeName != "能量" || state.TimerRules[0].Formula != "能量 + 1" || state.TimerRules[2].Condition != "能量 >= 0" {
		t.Fatalf("timer rule = %#v", state.TimerRules[0])
	}
	if state.DisplayScenes[0].AttributeNames[0] != "能量" {
		t.Fatalf("scene names = %#v", state.DisplayScenes[0].AttributeNames)
	}
	activity := state.Activities[0]
	if activity.AttributeNames[0] != "能量" || activity.InitialValues["能量"] != 1 || activity.Result.WinnerAttributeName != "能量" || activity.Result.Values["能量"] != 1 || activity.Milestones[0].AttributeName != "能量" {
		t.Fatalf("activity = %#v", activity)
	}
	if state.FormulaPresets[0].SourceAttributeName != "能量" || state.FormulaPresets[0].Formula != "能量 + 3" {
		t.Fatalf("preset = %#v", state.FormulaPresets[0])
	}
	if state.Log[0].AttributeName != "积分" || state.GiftReceipts[0].Effects[0].AttributeName != "积分" || state.Contributions.Viewers[0].AttributeDeltas["积分"] != 4 {
		t.Fatalf("history was rewritten: log=%#v receipts=%#v contributions=%#v", state.Log, state.GiftReceipts, state.Contributions)
	}
}

func TestConfigStoreApplyAttributeEditPreservesPeersAndLastWriteWins(t *testing.T) {
	store := attributeEditFixtureStore(t)
	if _, err := store.applyAttributeEdit(existingAttributeEdit("attribute-a", "能量", 10), fixedAttributeID); err != nil {
		t.Fatal(err)
	}
	second, err := store.applyAttributeEdit(existingAttributeEdit("attribute-a", "热度", 20), fixedAttributeID)
	if err != nil {
		t.Fatal(err)
	}
	if second.State.findAttribute("热度").Value != 20 {
		t.Fatal("last target write did not win")
	}
	if second.State.findAttribute("B").Value != 2 {
		t.Fatal("peer was overwritten")
	}
}

func TestConfigStoreApplyAttributeEditMergesAtTargetAnchorsAndPreservesProvenance(t *testing.T) {
	store := attributeEditFixtureStore(t)
	result, err := store.applyAttributeEdit(existingAttributeEdit("attribute-a", "能量", 10), fixedAttributeID)
	if err != nil {
		t.Fatal(err)
	}
	if result.Created || result.ID != "attribute-a" || result.Name != "能量" {
		t.Fatalf("result = %#v", result)
	}
	if got := result.State.Attributes; len(got) != 2 || got[0].ID != "attribute-a" || got[0].Name != "能量" || got[0].Color != "#112233" || got[0].CreatedFromTemplateID != "template-a" || got[0].CreatedFromTemplateVersion != 7 || got[1].ID != "attribute-b" || got[1].Name != "B" {
		t.Fatalf("attributes = %#v", got)
	}
	if got := result.State.Rules; len(got) != 3 || got[0].ID != "gift-a-2" || got[0].AttributeName != "能量" || got[1].ID != "gift-a-3" || got[2].ID != "gift-b" || got[2].AttributeName != "B" {
		t.Fatalf("gift rules = %#v", got)
	}
	if got := result.State.TimerRules; len(got) != 3 || got[0].ID != "timer-a-2" || got[0].AttributeName != "能量" || got[1].ID != "timer-a-3" || got[2].ID != "timer-b" || got[2].AttributeName != "B" {
		t.Fatalf("timer rules = %#v", got)
	}
	if got := result.State.GiftCatalog; len(got) != 3 || got[0].ID != 1 || got[0].Name != "更新礼物" || got[1].ID != 2 || got[1].Name != "同伴礼物" || got[2].ID != 3 {
		t.Fatalf("gift catalog = %#v", got)
	}
}

func TestConfigStoreApplyAttributeEditCreatesWithGeneratedIDAndAppends(t *testing.T) {
	store := attributeEditFixtureStore(t)
	command := existingAttributeEdit("", "新属性", 3)
	command.Target = attributeEditTarget{Kind: "new"}
	command.Attribute = attributeState{Name: "新属性", Value: 3, Color: "#abcdef"}
	command.GiftRules = []giftRule{{ID: "gift-new", GiftID: 8, AttributeName: "新属性", Formula: "新属性 + 1"}}
	command.TimerRules = nil
	command.GiftCatalogUpserts = nil
	result, err := store.applyAttributeEdit(command, fixedAttributeID)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Created || result.ID != "generated-attribute" || result.State.Attributes[2].ID != "generated-attribute" || result.State.Attributes[2].Name != "新属性" {
		t.Fatalf("new result = %#v", result)
	}
}

func TestConfigStoreApplyAttributeEditRejectsInvalidAggregateWithoutPersisting(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*attributeEditCommand)
		isWant func(error) bool
	}{
		{"invalid formula", func(command *attributeEditCommand) { command.GiftRules[0].Formula = "@" }, isAttributeEditInputError},
		{"duplicate name", func(command *attributeEditCommand) { command.Attribute.Name = "B" }, isAttributeEditConflictError},
		{"wrong target rule", func(command *attributeEditCommand) { command.GiftRules[0].AttributeName = "B" }, isAttributeEditInputError},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := attributeEditFixtureStore(t)
			command := existingAttributeEdit("attribute-a", "能量", 10)
			tc.mutate(&command)
			_, err := store.applyAttributeEdit(command, fixedAttributeID)
			if err == nil {
				t.Fatal("expected error")
			}
			if !tc.isWant(err) {
				t.Fatalf("error type = %T", err)
			}
			state, readErr := store.readState()
			if readErr != nil {
				t.Fatal(readErr)
			}
			if state.findAttribute("积分") == nil || state.findAttribute("能量") != nil {
				t.Fatalf("invalid edit persisted: %#v", state.Attributes)
			}
		})
	}
}

func TestConfigStoreApplyAttributeEditRejectsAmbiguousStoredAttributeIDs(t *testing.T) {
	store := attributeEditFixtureStore(t)
	state, err := store.readState()
	if err != nil {
		t.Fatal(err)
	}
	state.Attributes[1].ID = "attribute-a"
	if err := store.replaceState(state); err != nil {
		t.Fatal(err)
	}
	_, err = store.applyAttributeEdit(existingAttributeEdit("attribute-a", "能量", 10), fixedAttributeID)
	if !isAttributeEditConflictError(err) {
		t.Fatalf("error type = %T, want attributeEditConflictError", err)
	}
}

func TestConfigStoreApplyAttributeEditDoesNotExposePartialStateAfterDurabilityFailure(t *testing.T) {
	store := attributeEditFixtureStore(t)
	store.writeAtomically = func(string, []byte) error { return errors.New("injected write failure") }
	if _, err := store.applyAttributeEdit(existingAttributeEdit("attribute-a", "能量", 10), fixedAttributeID); err == nil {
		t.Fatal("expected write failure")
	}
	store.writeAtomically = nil
	state, err := store.readState()
	if err != nil {
		t.Fatal(err)
	}
	if state.findAttribute("积分") == nil || state.findAttribute("能量") != nil || state.Rules[0].AttributeName != "积分" {
		t.Fatalf("partial edit visible after failed durability: %#v", state)
	}
}

func TestConfigStoreApplyAttributeEditRejectsSubmittedPeerRuleIDCollisionsWithoutDeletingPeers(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*attributeEditCommand)
	}{
		{
			name: "gift rule",
			mutate: func(command *attributeEditCommand) {
				command.GiftRules[0].ID = "gift-b"
			},
		},
		{
			name: "timer rule",
			mutate: func(command *attributeEditCommand) {
				command.TimerRules[0].ID = "timer-b"
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := attributeEditFixtureStore(t)
			command := existingAttributeEdit("attribute-a", "能量", 10)
			tc.mutate(&command)
			_, err := store.applyAttributeEdit(command, fixedAttributeID)
			if !isAttributeEditConflictError(err) {
				t.Fatalf("error type = %T, want attributeEditConflictError", err)
			}
			state, err := store.readState()
			if err != nil {
				t.Fatal(err)
			}
			if state.findAttribute("积分") == nil || state.findAttribute("能量") != nil || state.Rules[1].ID != "gift-b" || state.TimerRules[1].ID != "timer-b" {
				t.Fatalf("peer was changed after rejected command: rules=%#v timers=%#v", state.Rules, state.TimerRules)
			}
		})
	}
}

func TestConfigStoreApplyAttributeEditRejectsAmbiguousLiveRuleIDs(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*appState)
	}{
		{
			name: "duplicate gift ID",
			mutate: func(state *appState) {
				state.Rules[2].ID = "gift-b"
			},
		},
		{
			name: "empty gift ID",
			mutate: func(state *appState) {
				state.Rules[1].ID = ""
			},
		},
		{
			name: "duplicate timer ID",
			mutate: func(state *appState) {
				state.TimerRules[2].ID = "timer-b"
			},
		},
		{
			name: "empty timer ID",
			mutate: func(state *appState) {
				state.TimerRules[1].ID = ""
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := attributeEditFixtureStore(t)
			state, err := store.readState()
			if err != nil {
				t.Fatal(err)
			}
			tc.mutate(&state)
			if err := store.replaceState(state); err != nil {
				t.Fatal(err)
			}
			_, err = store.applyAttributeEdit(existingAttributeEdit("attribute-a", "能量", 10), fixedAttributeID)
			if !isAttributeEditConflictError(err) {
				t.Fatalf("error type = %T, want attributeEditConflictError", err)
			}
		})
	}
}

func TestConfigStoreApplyAttributeEditKeepsMultiplePeerRuleOrderAndReplacesTargetRules(t *testing.T) {
	store := attributeEditFixtureStore(t)
	if _, err := store.updateState(func(state *appState) error {
		state.Rules = append(state.Rules, giftRule{ID: "gift-b-2", GiftID: 10, AttributeName: "B", FormulaName: "peer two", Formula: "B + 2"})
		state.TimerRules = append(state.TimerRules, timerRule{ID: "timer-b-2", AttributeName: "B", FormulaName: "timer peer two", IntervalSeconds: 3, Formula: "B + 2", Enabled: true})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	first, err := store.applyAttributeEdit(existingAttributeEdit("attribute-a", "能量", 10), fixedAttributeID)
	if err != nil {
		t.Fatal(err)
	}
	assertGiftRuleIDs(t, first.State.Rules, "gift-a-2", "gift-a-3", "gift-b", "gift-b-2")
	assertTimerRuleIDs(t, first.State.TimerRules, "timer-a-2", "timer-a-3", "timer-b", "timer-b-2")

	secondCommand := existingAttributeEdit("attribute-a", "热度", 20)
	secondCommand.GiftRules = []giftRule{{ID: "gift-final", GiftID: 4, AttributeName: "热度", FormulaName: "final target", Formula: "热度 + 7"}}
	secondCommand.TimerRules = []timerRule{{ID: "timer-final", AttributeName: "热度", FormulaName: "final timer", IntervalSeconds: 7, Formula: "热度 + 7", Enabled: true}}
	second, err := store.applyAttributeEdit(secondCommand, fixedAttributeID)
	if err != nil {
		t.Fatal(err)
	}
	assertGiftRuleIDs(t, second.State.Rules, "gift-final", "gift-b", "gift-b-2")
	assertTimerRuleIDs(t, second.State.TimerRules, "timer-final", "timer-b", "timer-b-2")
	if second.State.Rules[0].AttributeName != "热度" || second.State.Rules[0].Formula != "热度 + 7" || second.State.TimerRules[0].AttributeName != "热度" || second.State.TimerRules[0].Formula != "热度 + 7" {
		t.Fatalf("target rules were not replaced by the second command: rules=%#v timers=%#v", second.State.Rules, second.State.TimerRules)
	}
}

func assertGiftRuleIDs(t *testing.T, rules []giftRule, want ...string) {
	t.Helper()
	if len(rules) != len(want) {
		t.Fatalf("gift rule count = %d, want %d: %#v", len(rules), len(want), rules)
	}
	for index, id := range want {
		if rules[index].ID != id {
			t.Fatalf("gift rule IDs = %#v, want %#v", rules, want)
		}
	}
}

func assertTimerRuleIDs(t *testing.T, rules []timerRule, want ...string) {
	t.Helper()
	if len(rules) != len(want) {
		t.Fatalf("timer rule count = %d, want %d: %#v", len(rules), len(want), rules)
	}
	for index, id := range want {
		if rules[index].ID != id {
			t.Fatalf("timer rule IDs = %#v, want %#v", rules, want)
		}
	}
}

func isAttributeEditInputError(err error) bool {
	var target *attributeEditInputError
	return errors.As(err, &target)
}

func isAttributeEditConflictError(err error) bool {
	var target *attributeEditConflictError
	return errors.As(err, &target)
}

func isAttributeEditNotFoundError(err error) bool {
	var target *attributeEditNotFoundError
	return errors.As(err, &target)
}

func fixedAttributeID() (string, error) { return "generated-attribute", nil }

func TestAttributeEditSessionBackfillsLegacyIDBeforeLease(t *testing.T) {
	store := attributeEditLegacyFixtureStore(t, "积分")
	service := newAttributeEditService(store, newDefaultAttributeEditLeaseCoordinator(), fixedAttributeID)
	got, err := service.Prepare(attributeEditSessionRequest{LegacyName: "积分"})
	if err != nil {
		t.Fatal(err)
	}
	if got.AttributeID != "generated-attribute" || !service.leases.Has(got.AttributeID, got.Token) {
		t.Fatalf("unexpected session: %#v", got)
	}
	state, err := store.readState()
	if err != nil {
		t.Fatal(err)
	}
	if state.Attributes[0].ID != "generated-attribute" {
		t.Fatal("ID was not persisted first")
	}
}

func TestAttributeEditSessionRejectsAmbiguousOrMissingLegacyName(t *testing.T) {
	for _, name := range []string{"missing", "积分"} {
		t.Run(name, func(t *testing.T) {
			store := attributeEditLegacyFixtureStore(t, "积分")
			if name == "积分" {
				if _, err := store.updateState(func(state *appState) error {
					state.Attributes = append(state.Attributes, attributeState{Name: "积分"})
					return nil
				}); err != nil {
					t.Fatal(err)
				}
			}
			_, err := newAttributeEditService(store, newDefaultAttributeEditLeaseCoordinator(), fixedAttributeID).Prepare(attributeEditSessionRequest{LegacyName: name})
			if !isAttributeEditNotFoundError(err) {
				t.Fatalf("error=%T, want not found", err)
			}
		})
	}
}

func TestAttributeEditSessionDoesNotCreateLeaseWhenIDPersistenceOrTokenGenerationFails(t *testing.T) {
	store := attributeEditLegacyFixtureStore(t, "积分")
	store.writeAtomically = func(string, []byte) error { return errors.New("injected persistence failure") }
	service := newAttributeEditService(store, newDefaultAttributeEditLeaseCoordinator(), fixedAttributeID)
	if _, err := service.Prepare(attributeEditSessionRequest{LegacyName: "积分"}); err == nil {
		t.Fatal("expected persistence error")
	}
	state, err := store.readState()
	if err != nil {
		t.Fatal(err)
	}
	if state.Attributes[0].ID != "" {
		t.Fatal("failed persistence changed live state")
	}

	store = attributeEditLegacyFixtureStore(t, "积分")
	leases := newAttributeEditLeaseCoordinator(attributeEditLeaseTTL, timeNowForAttributeEditTest, func() (string, error) { return "", errors.New("injected token failure") })
	service = newAttributeEditService(store, leases, fixedAttributeID)
	if _, err := service.Prepare(attributeEditSessionRequest{LegacyName: "积分"}); err == nil {
		t.Fatal("expected token error")
	}
	state, err = store.readState()
	if err != nil {
		t.Fatal(err)
	}
	if state.Attributes[0].ID != "generated-attribute" || leases.IsFrozen("generated-attribute") {
		t.Fatal("persisted ID must precede a failed lease creation")
	}
}

func TestAttributeEditSubmitHoldsLiveLeaseUntilItAcquiresStoreLock(t *testing.T) {
	store := attributeEditFixtureStore(t)
	leases := newAttributeEditLeaseCoordinator(15*time.Second, timeNowForAttributeEditTest, func() (string, error) { return "AAAAAAAAAAAAAAAAAAAAAAAA", nil })
	token, _, err := leases.Create("attribute-a")
	if err != nil {
		t.Fatal(err)
	}
	command := existingAttributeEdit("attribute-a", "能量", 10)
	command.Target.LeaseToken = token
	service := newAttributeEditService(store, leases, fixedAttributeID)
	attemptedStoreLock := make(chan struct{})
	acquiredStoreLock := make(chan struct{})
	store.beforeAttributeEditStoreLock = func() { close(attemptedStoreLock) }
	store.afterAttributeEditStoreLock = func() { close(acquiredStoreLock) }
	enteredPersistence := make(chan struct{})
	releasePersistence := make(chan struct{})
	var releasePersistenceOnce sync.Once
	var submitWait sync.WaitGroup
	t.Cleanup(func() {
		releasePersistenceOnce.Do(func() { close(releasePersistence) })
		submitWait.Wait()
	})
	store.writeAtomically = func(string, []byte) error {
		close(enteredPersistence)
		<-releasePersistence
		return errors.New("injected write failure")
	}

	store.mu.Lock()
	storeLocked := true
	t.Cleanup(func() {
		if storeLocked {
			store.mu.Unlock()
		}
	})
	done := make(chan error, 1)
	submitWait.Add(1)
	go func() {
		defer submitWait.Done()
		_, err := service.Submit(command)
		done <- err
	}()
	<-attemptedStoreLock
	store.mu.Unlock()
	storeLocked = false
	<-acquiredStoreLock
	<-enteredPersistence
	releasePersistenceOnce.Do(func() { close(releasePersistence) })
	if err := <-done; err == nil {
		t.Fatal("expected injected write failure")
	}
	if !leases.Has("attribute-a", token) {
		t.Fatal("lease was not retained through the store-lock wait")
	}
}

func TestAttributeEditSubmitRejectsLeaseThatExpiresAtStoreWriteBoundary(t *testing.T) {
	var nowUnix atomic.Int64
	nowUnix.Store(100)
	nowCalls := 0
	liveChecked := make(chan struct{})
	leases := newAttributeEditLeaseCoordinator(15*time.Second, func() time.Time {
		nowCalls++
		if nowCalls == 2 { // Create consumes the first clock read.
			close(liveChecked)
		}
		return time.Unix(nowUnix.Load(), 0)
	}, func() (string, error) { return "AAAAAAAAAAAAAAAAAAAAAAAA", nil })
	store := attributeEditFixtureStore(t)
	token, _, err := leases.Create("attribute-a")
	if err != nil {
		t.Fatal(err)
	}
	command := existingAttributeEdit("attribute-a", "能量", 10)
	command.Target.LeaseToken = token
	service := newAttributeEditService(store, leases, fixedAttributeID)

	store.mu.Lock()
	done := make(chan error, 1)
	go func() {
		_, err := service.Submit(command)
		done <- err
	}()
	<-liveChecked
	nowUnix.Add(15)
	store.mu.Unlock()
	err = <-done
	var leaseLost *attributeEditLeaseLostError
	if !errors.As(err, &leaseLost) {
		t.Fatalf("error=%v, want lease lost", err)
	}
	state, err := store.readState()
	if err != nil {
		t.Fatal(err)
	}
	if state.findAttribute("能量") != nil {
		t.Fatal("expired lease persisted an edit")
	}
}

func TestAttributeEditPrepareKeepsBackfillLeaseAndStateCoherent(t *testing.T) {
	store := attributeEditLegacyFixtureStore(t, "积分")
	leases := newAttributeEditLeaseCoordinator(15*time.Second, timeNowForAttributeEditTest, func() (string, error) { return "AAAAAAAAAAAAAAAAAAAAAAAA", nil })
	service := newAttributeEditService(store, leases, fixedAttributeID)
	enteredCreate := make(chan struct{})
	resumeCreate := make(chan struct{})
	var resumeCreateOnce sync.Once
	t.Cleanup(func() { resumeCreateOnce.Do(func() { close(resumeCreate) }) })
	service.beforeLeaseCreate = func() {
		close(enteredCreate)
		<-resumeCreate
	}
	prepared := make(chan struct {
		session attributeEditSession
		err     error
	}, 1)
	go func() {
		session, err := service.Prepare(attributeEditSessionRequest{LegacyName: "积分"})
		prepared <- struct {
			session attributeEditSession
			err     error
		}{session, err}
	}()
	<-enteredCreate
	deleted := make(chan error, 1)
	go func() {
		_, err := store.updateState(func(state *appState) error {
			state.Attributes = nil
			return nil
		})
		deleted <- err
	}()
	resumeCreateOnce.Do(func() { close(resumeCreate) })
	result := <-prepared
	if result.err != nil || result.session.AttributeID != "generated-attribute" || !leases.Has(result.session.AttributeID, result.session.Token) {
		t.Fatalf("session=%#v err=%v", result.session, result.err)
	}
	if result.session.State.Attributes[0].ID != "generated-attribute" {
		t.Fatal("returned state did not contain the persisted ID")
	}
	if err := <-deleted; err != nil {
		t.Fatal(err)
	}
}

func TestAttributeEditSubmitCannotDeadlockWithBackgroundFreezeCheck(t *testing.T) {
	store := attributeEditFixtureStore(t)
	leases := newAttributeEditLeaseCoordinator(15*time.Second, timeNowForAttributeEditTest, func() (string, error) { return "AAAAAAAAAAAAAAAAAAAAAAAA", nil })
	token, _, err := leases.Create("attribute-a")
	if err != nil {
		t.Fatal(err)
	}
	service := newAttributeEditService(store, leases, fixedAttributeID)
	claimStarted := make(chan struct{})
	allowSubmit := make(chan struct{})
	var allowSubmitOnce sync.Once
	var submitWait sync.WaitGroup
	t.Cleanup(func() {
		allowSubmitOnce.Do(func() { close(allowSubmit) })
		submitWait.Wait()
	})
	leases.afterBegin = func() {
		close(claimStarted)
		<-allowSubmit
	}
	command := existingAttributeEdit("attribute-a", "能量", 10)
	command.Target.LeaseToken = token
	submitted := make(chan error, 1)
	submitWait.Add(1)
	go func() {
		defer submitWait.Done()
		_, err := service.Submit(command)
		submitted <- err
	}()
	<-claimStarted

	backgroundHasStoreLock := make(chan struct{})
	backgroundDone := make(chan error, 1)
	go func() {
		_, err := store.updateState(func(state *appState) error {
			close(backgroundHasStoreLock)
			if !leases.IsFrozen("attribute-a") {
				return errors.New("background lost the live freeze")
			}
			return nil
		})
		backgroundDone <- err
	}()
	<-backgroundHasStoreLock
	if err := <-backgroundDone; err != nil {
		t.Fatal(err)
	}
	allowSubmitOnce.Do(func() { close(allowSubmit) })
	if err := <-submitted; err != nil {
		t.Fatal(err)
	}
}

func TestAttributeEditExpiredClaimCannotBeResurrectedWhileStoreIsBlocked(t *testing.T) {
	now := time.Unix(100, 0)
	store := attributeEditFixtureStore(t)
	leases := newAttributeEditLeaseCoordinator(15*time.Second, func() time.Time { return now }, func() (string, error) { return "AAAAAAAAAAAAAAAAAAAAAAAA", nil })
	token, _, err := leases.Create("attribute-a")
	if err != nil {
		t.Fatal(err)
	}
	first, ok := leases.Begin("attribute-a", token)
	if !ok || !first.Live() {
		t.Fatal("first claim was not live")
	}
	service := newAttributeEditService(store, leases, fixedAttributeID)
	secondStarted := make(chan struct{})
	leases.afterBegin = func() { close(secondStarted) }
	command := existingAttributeEdit("attribute-a", "能量", 10)
	command.Target.LeaseToken = token
	store.mu.Lock()
	submitted := make(chan error, 1)
	go func() {
		_, err := service.Submit(command)
		submitted <- err
	}()
	<-secondStarted
	now = now.Add(15 * time.Second)
	if first.Live() || leases.Has("attribute-a", token) {
		store.mu.Unlock()
		t.Fatal("expired claim remained live")
	}
	if _, ok := leases.Renew("attribute-a", token); ok {
		store.mu.Unlock()
		t.Fatal("Renew resurrected an expired claimed lease")
	}
	if _, ok := leases.Begin("attribute-a", token); ok {
		store.mu.Unlock()
		t.Fatal("Begin resurrected an expired claimed lease")
	}
	store.mu.Unlock()
	err = <-submitted
	var lost *attributeEditLeaseLostError
	if !errors.As(err, &lost) {
		t.Fatalf("submit error=%v, want lease lost", err)
	}
	released := make(chan bool, 1)
	releaseMarked := make(chan struct{})
	leases.afterReleaseMarked = func() { close(releaseMarked) }
	go func() { released <- leases.Release("attribute-a", token) }()
	<-releaseMarked
	first.Finish()
	if ok := <-released; !ok || leases.Has("attribute-a", token) {
		t.Fatal("expired lease cleanup did not converge")
	}
	state, err := store.readState()
	if err != nil {
		t.Fatal(err)
	}
	if state.findAttribute("能量") != nil {
		t.Fatal("expired claim persisted an edit")
	}
}

func TestAttributeEditPreservesConcurrentGiftPeerUpdate(t *testing.T) {
	store := attributeEditFixtureStore(t)
	state, err := store.readState()
	if err != nil {
		t.Fatal(err)
	}
	state.RoomID = "room-a"
	state.Rules[1] = giftRule{ID: "gift-b", GiftID: 99, AttributeName: "B", Formula: "B+1"}
	if err := store.replaceState(state); err != nil {
		t.Fatal(err)
	}
	leases := newAttributeEditLeaseCoordinator(attributeEditLeaseTTL, timeNowForAttributeEditTest, func() (string, error) { return "AAAAAAAAAAAAAAAAAAAAAAAA", nil })
	token, _, err := leases.Create("attribute-a")
	if err != nil {
		t.Fatal(err)
	}
	enteredBeforeStore := make(chan struct{})
	releaseSubmit := make(chan struct{})
	var releaseSubmitOnce sync.Once
	var submitWait sync.WaitGroup
	t.Cleanup(func() {
		releaseSubmitOnce.Do(func() { close(releaseSubmit) })
		submitWait.Wait()
	})
	leases.afterBegin = func() {
		close(enteredBeforeStore)
		<-releaseSubmit
	}
	service := newAttributeEditService(store, leases, fixedAttributeID)
	command := existingAttributeEdit("attribute-a", "能量", 10)
	command.Target.LeaseToken = token
	submitted := make(chan struct {
		result attributeEditResult
		err    error
	}, 1)
	submitWait.Add(1)
	go func() {
		defer submitWait.Done()
		result, submitErr := service.Submit(command)
		submitted <- struct {
			result attributeEditResult
			err    error
		}{result, submitErr}
	}()
	<-enteredBeforeStore

	runtime := newBackgroundRuntime(store, nil)
	runtime.setAttributeFreezeChecker(leases)
	if err := runtime.processInboxRecord(context.Background(), giftInboxRecord{
		IngestionID: "gift-peer-race", RoomID: "room-a",
		Gift: giftEvent{GiftID: 99, GiftName: "peer", Num: 1, Price: 1, CoinType: "gold", UID: 1, Uname: "viewer", Timestamp: 100, Rnd: "gift-peer-race"},
	}); err != nil {
		t.Fatal(err)
	}
	releaseSubmitOnce.Do(func() { close(releaseSubmit) })
	result := <-submitted
	if result.err != nil {
		t.Fatal(result.err)
	}
	assertAtomicAttributeEditPeerState(t, result.result.State, "能量", 10, 3)
	persisted, err := store.readState()
	if err != nil {
		t.Fatal(err)
	}
	assertAtomicAttributeEditPeerState(t, persisted, "能量", 10, 3)
}

func TestAttributeEditPreservesConcurrentTimerPeerUpdate(t *testing.T) {
	store := attributeEditFixtureStore(t)
	leases := newAttributeEditLeaseCoordinator(attributeEditLeaseTTL, timeNowForAttributeEditTest, func() (string, error) { return "AAAAAAAAAAAAAAAAAAAAAAAA", nil })
	token, _, err := leases.Create("attribute-a")
	if err != nil {
		t.Fatal(err)
	}
	enteredBeforeStore := make(chan struct{})
	releaseSubmit := make(chan struct{})
	var releaseSubmitOnce sync.Once
	var submitWait sync.WaitGroup
	t.Cleanup(func() {
		releaseSubmitOnce.Do(func() { close(releaseSubmit) })
		submitWait.Wait()
	})
	leases.afterBegin = func() {
		close(enteredBeforeStore)
		<-releaseSubmit
	}
	service := newAttributeEditService(store, leases, fixedAttributeID)
	command := existingAttributeEdit("attribute-a", "能量", 10)
	command.Target.LeaseToken = token
	submitted := make(chan struct {
		result attributeEditResult
		err    error
	}, 1)
	submitWait.Add(1)
	go func() {
		defer submitWait.Done()
		result, submitErr := service.Submit(command)
		submitted <- struct {
			result attributeEditResult
			err    error
		}{result, submitErr}
	}()
	<-enteredBeforeStore

	runtime := newBackgroundRuntime(store, nil)
	runtime.setAttributeFreezeChecker(leases)
	startedAt := time.Unix(1_700_000_000, 0)
	runtime.handleTimerTick(startedAt)
	runtime.handleTimerTick(startedAt.Add(time.Minute))
	releaseSubmitOnce.Do(func() { close(releaseSubmit) })
	result := <-submitted
	if result.err != nil {
		t.Fatal(result.err)
	}
	// A was frozen during the due tick: its submitted value is not a timer catch-up.
	assertAtomicAttributeEditPeerState(t, result.result.State, "能量", 10, 3)
	persisted, err := store.readState()
	if err != nil {
		t.Fatal(err)
	}
	assertAtomicAttributeEditPeerState(t, persisted, "能量", 10, 3)
}

func TestAttributeEditSameTargetLaterValidSaveWinsAndKeepsPeerUpdate(t *testing.T) {
	store := attributeEditFixtureStore(t)
	tokens := []string{"AAAAAAAAAAAAAAAAAAAAAAAA", "BBBBBBBBBBBBBBBBBBBBBBBB"}
	leases := newAttributeEditLeaseCoordinator(attributeEditLeaseTTL, timeNowForAttributeEditTest, func() (string, error) {
		token := tokens[0]
		tokens = tokens[1:]
		return token, nil
	})
	firstToken, _, err := leases.Create("attribute-a")
	if err != nil {
		t.Fatal(err)
	}
	secondToken, _, err := leases.Create("attribute-a")
	if err != nil {
		t.Fatal(err)
	}
	firstLockAcquired := make(chan struct{})
	firstHoldsStoreLock := make(chan struct{})
	secondLockAttempted := make(chan struct{})
	allowSecondStoreLock := make(chan struct{})
	secondLockAcquired := make(chan struct{})
	var firstStoreRelease atomic.Bool
	var beforeLockCount atomic.Int32
	var afterLockCount atomic.Int32
	lockOrderViolation := make(chan error, 1)
	store.beforeAttributeEditStoreLock = func() {
		if beforeLockCount.Add(1) == 2 {
			close(secondLockAttempted)
			<-allowSecondStoreLock
		}
	}
	store.afterAttributeEditStoreLock = func() {
		if afterLockCount.Add(1) == 1 {
			close(firstLockAcquired)
			return
		}
		if !firstStoreRelease.Load() {
			lockOrderViolation <- errors.New("second command acquired configStore.mu before first release")
		}
		close(secondLockAcquired)
	}
	enteredFirstPersist := make(chan struct{})
	releaseFirstPersist := make(chan struct{})
	var releaseFirstOnce sync.Once
	var releaseSecondOnce sync.Once
	var persistOnce sync.Once
	var submitWait sync.WaitGroup
	t.Cleanup(func() {
		releaseFirstOnce.Do(func() { close(releaseFirstPersist) })
		releaseSecondOnce.Do(func() { close(allowSecondStoreLock) })
		submitWait.Wait()
	})
	store.writeAtomically = func(path string, data []byte) error {
		persistOnce.Do(func() {
			close(enteredFirstPersist)
			close(firstHoldsStoreLock)
			<-releaseFirstPersist
		})
		return writeFileAtomically(path, data)
	}
	service := newAttributeEditService(store, leases, fixedAttributeID)
	firstCommand := existingAttributeEdit("attribute-a", "第一次", 10)
	firstCommand.Target.LeaseToken = firstToken
	firstCommand.GiftRules = []giftRule{
		{ID: "gift-first-1", GiftID: 41, AttributeName: "第一次", FormulaName: "first one", Formula: "第一次+11"},
		{ID: "gift-first-2", GiftID: 42, AttributeName: "第一次", FormulaName: "first two", Formula: "第一次+12"},
	}
	firstCommand.TimerRules = []timerRule{
		{ID: "timer-first-1", AttributeName: "第一次", FormulaName: "first timer one", IntervalSeconds: 41, Formula: "第一次+21", Enabled: true},
		{ID: "timer-first-2", AttributeName: "第一次", FormulaName: "first timer two", IntervalSeconds: 42, Formula: "第一次+22", Enabled: true},
	}
	secondCommand := existingAttributeEdit("attribute-a", "第二次", 20)
	secondCommand.Target.LeaseToken = secondToken
	secondCommand.GiftRules = []giftRule{
		{ID: "gift-second-1", GiftID: 51, AttributeName: "第二次", FormulaName: "second one", Formula: "第二次+31"},
		{ID: "gift-second-2", GiftID: 52, AttributeName: "第二次", FormulaName: "second two", Formula: "第二次+32"},
	}
	secondCommand.TimerRules = []timerRule{
		{ID: "timer-second-1", AttributeName: "第二次", FormulaName: "second timer one", IntervalSeconds: 51, Formula: "第二次+41", Enabled: true},
		{ID: "timer-second-2", AttributeName: "第二次", FormulaName: "second timer two", IntervalSeconds: 52, Formula: "第二次+42", Enabled: true},
	}
	firstDone := make(chan struct {
		result attributeEditResult
		err    error
	}, 1)
	submitWait.Add(1)
	go func() {
		defer submitWait.Done()
		result, submitErr := service.Submit(firstCommand)
		firstDone <- struct {
			result attributeEditResult
			err    error
		}{result, submitErr}
	}()
	<-firstLockAcquired
	<-enteredFirstPersist
	<-firstHoldsStoreLock
	secondDone := make(chan struct {
		result attributeEditResult
		err    error
	}, 1)
	submitWait.Add(1)
	go func() {
		defer submitWait.Done()
		result, submitErr := service.Submit(secondCommand)
		secondDone <- struct {
			result attributeEditResult
			err    error
		}{result, submitErr}
	}()
	<-secondLockAttempted
	firstStoreRelease.Store(true)
	releaseFirstOnce.Do(func() { close(releaseFirstPersist) })
	first := <-firstDone
	if first.err != nil {
		t.Fatal(first.err)
	}
	if first.result.Name != "第一次" || first.result.State.findAttribute("第一次").Value != 10 {
		t.Fatalf("first result = %#v", first.result)
	}
	assertAtomicAttributeEditOwnedRules(t, first.result.State, []string{"gift-first-1", "gift-first-2", "gift-b"}, []string{"第一次+11", "第一次+12", "B + 1"}, []string{"timer-first-1", "timer-first-2", "timer-b"}, []string{"第一次+21", "第一次+22", "B + 1"})
	if _, err := store.updateState(func(state *appState) error {
		state.findAttribute("B").Value = 3
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	releaseSecondOnce.Do(func() { close(allowSecondStoreLock) })
	<-secondLockAcquired
	select {
	case err := <-lockOrderViolation:
		t.Fatal(err)
	default:
	}
	second := <-secondDone
	if second.err != nil {
		t.Fatal(second.err)
	}
	assertAtomicAttributeEditPeerState(t, second.result.State, "第二次", 20, 3)
	assertAtomicAttributeEditOwnedRules(t, second.result.State, []string{"gift-second-1", "gift-second-2", "gift-b"}, []string{"第二次+31", "第二次+32", "B + 1"}, []string{"timer-second-1", "timer-second-2", "timer-b"}, []string{"第二次+41", "第二次+42", "B + 1"})
	persisted, err := store.readState()
	if err != nil {
		t.Fatal(err)
	}
	assertAtomicAttributeEditPeerState(t, persisted, "第二次", 20, 3)
	assertAtomicAttributeEditOwnedRules(t, persisted, []string{"gift-second-1", "gift-second-2", "gift-b"}, []string{"第二次+31", "第二次+32", "B + 1"}, []string{"timer-second-1", "timer-second-2", "timer-b"}, []string{"第二次+41", "第二次+42", "B + 1"})
}

func TestAttributeEditSameTargetInvalidLaterSaveLeavesFirstAuthoritative(t *testing.T) {
	store := attributeEditFixtureStore(t)
	tokens := []string{"AAAAAAAAAAAAAAAAAAAAAAAA", "BBBBBBBBBBBBBBBBBBBBBBBB"}
	leases := newAttributeEditLeaseCoordinator(attributeEditLeaseTTL, timeNowForAttributeEditTest, func() (string, error) {
		token := tokens[0]
		tokens = tokens[1:]
		return token, nil
	})
	firstToken, _, err := leases.Create("attribute-a")
	if err != nil {
		t.Fatal(err)
	}
	secondToken, _, err := leases.Create("attribute-a")
	if err != nil {
		t.Fatal(err)
	}
	service := newAttributeEditService(store, leases, fixedAttributeID)
	first := existingAttributeEdit("attribute-a", "第一次", 10)
	first.Target.LeaseToken = firstToken
	first.GiftRules = []giftRule{{ID: "gift-first-only", GiftID: 61, AttributeName: "第一次", Formula: "第一次+51"}}
	first.TimerRules = []timerRule{{ID: "timer-first-only", AttributeName: "第一次", IntervalSeconds: 61, Formula: "第一次+61", Enabled: true}}
	if _, err := service.Submit(first); err != nil {
		t.Fatal(err)
	}
	invalid := existingAttributeEdit("attribute-a", "B", 20)
	invalid.Target.LeaseToken = secondToken
	invalid.GiftRules = []giftRule{{ID: "gift-invalid-later", GiftID: 62, AttributeName: "B", Formula: "B+71"}}
	invalid.TimerRules = []timerRule{{ID: "timer-invalid-later", AttributeName: "B", IntervalSeconds: 62, Formula: "B+81", Enabled: true}}
	if _, err := service.Submit(invalid); !isAttributeEditConflictError(err) {
		t.Fatalf("invalid later save error = %v, want name conflict", err)
	}
	persisted, err := store.readState()
	if err != nil {
		t.Fatal(err)
	}
	assertAtomicAttributeEditPeerState(t, persisted, "第一次", 10, 2)
	assertAtomicAttributeEditOwnedRules(t, persisted, []string{"gift-first-only", "gift-b"}, []string{"第一次+51", "B + 1"}, []string{"timer-first-only", "timer-b"}, []string{"第一次+61", "B + 1"})
}

func TestAttributeEditSubmitWriteFailureLeavesNoPartialStateAndAllowsLaterSave(t *testing.T) {
	store := attributeEditFixtureStore(t)
	leases := newAttributeEditLeaseCoordinator(attributeEditLeaseTTL, timeNowForAttributeEditTest, func() (string, error) { return "AAAAAAAAAAAAAAAAAAAAAAAA", nil })
	token, _, err := leases.Create("attribute-a")
	if err != nil {
		t.Fatal(err)
	}
	service := newAttributeEditService(store, leases, fixedAttributeID)
	command := existingAttributeEdit("attribute-a", "故障保存", 10)
	command.Target.LeaseToken = token
	store.writeAtomically = func(string, []byte) error { return errors.New("injected submit persistence failure") }
	if _, err := service.Submit(command); err == nil {
		t.Fatal("expected service submit persistence failure")
	}
	state, err := store.readState()
	if err != nil {
		t.Fatal(err)
	}
	if state.findAttribute("故障保存") != nil || !leases.Has("attribute-a", token) {
		t.Fatalf("failure exposed partial state or leaked live claim: attributes=%#v lease=%v", state.Attributes, leases.Has("attribute-a", token))
	}
	store.writeAtomically = nil
	result, err := service.Submit(command)
	if err != nil {
		t.Fatal(err)
	}
	if result.Name != "故障保存" || result.State.findAttribute("故障保存").Value != 10 {
		t.Fatalf("later submit did not recover service/store usability: %#v", result)
	}
	persisted, err := store.readState()
	if err != nil {
		t.Fatal(err)
	}
	if persisted.findAttribute("故障保存").Value != 10 {
		t.Fatalf("later authoritative state not persisted: %#v", persisted.Attributes)
	}
}

func TestAttributeEditLeaseFreezesOnlyTargetAndNewIDRemainsAddressable(t *testing.T) {
	now := time.Unix(100, 0)
	store := attributeEditFixtureStore(t)
	state, err := store.readState()
	if err != nil {
		t.Fatal(err)
	}
	state.RoomID = "room-a"
	state.Rules[0] = giftRule{ID: "gift-a-1", GiftID: 99, AttributeName: "积分", Formula: "积分+1"}
	state.Rules[1] = giftRule{ID: "gift-b", GiftID: 99, AttributeName: "B", Formula: "B+1"}
	if err := store.replaceState(state); err != nil {
		t.Fatal(err)
	}
	leases := newAttributeEditLeaseCoordinator(attributeEditLeaseTTL, func() time.Time { return now }, func() (string, error) { return "AAAAAAAAAAAAAAAAAAAAAAAA", nil })
	token, _, err := leases.Create("attribute-a")
	if err != nil {
		t.Fatal(err)
	}
	runtime := newBackgroundRuntime(store, nil)
	runtime.setAttributeFreezeChecker(leases)
	if err := runtime.processInboxRecord(context.Background(), giftInboxRecord{
		IngestionID: "frozen-target-gift", RoomID: "room-a", Gift: giftEvent{GiftID: 99, Num: 1, Price: 1, CoinType: "gold", UID: 1, Uname: "viewer", Timestamp: 100, Rnd: "frozen-target-gift"},
	}); err != nil {
		t.Fatal(err)
	}
	startedAt := time.Unix(1_700_000_000, 0)
	runtime.handleTimerTick(startedAt)
	runtime.handleTimerTick(startedAt.Add(time.Minute))
	frozen, err := store.readState()
	if err != nil {
		t.Fatal(err)
	}
	if frozen.findAttribute("积分").Value != 1 || frozen.findAttribute("B").Value != 4 {
		t.Fatalf("target-only freeze state = %#v", frozen.Attributes)
	}
	if !leases.Release("attribute-a", token) {
		t.Fatal("release failed")
	}
	runtime.handleTimerTick(startedAt.Add(time.Minute + time.Millisecond))
	beforeNextDue, err := store.readState()
	if err != nil {
		t.Fatal(err)
	}
	if beforeNextDue.findAttribute("积分").Value != 1 || beforeNextDue.findAttribute("B").Value != 4 {
		t.Fatalf("thawed target caught up before next due tick: %#v", beforeNextDue.Attributes)
	}
	if err := runtime.processInboxRecord(context.Background(), giftInboxRecord{
		IngestionID: "released-target-gift", RoomID: "room-a", Gift: giftEvent{GiftID: 99, Num: 1, Price: 1, CoinType: "gold", UID: 1, Uname: "viewer", Timestamp: 101, Rnd: "released-target-gift"},
	}); err != nil {
		t.Fatal(err)
	}
	unfrozen, err := store.readState()
	if err != nil {
		t.Fatal(err)
	}
	if unfrozen.findAttribute("积分").Value != 2 || unfrozen.findAttribute("B").Value != 5 {
		t.Fatalf("released target did not resume = %#v", unfrozen.Attributes)
	}
	expiredToken, _, err := leases.Create("attribute-a")
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(attributeEditLeaseTTL)
	if leases.Has("attribute-a", expiredToken) || leases.IsFrozen("attribute-a") {
		t.Fatal("expired token kept target frozen")
	}
	stale := existingAttributeEdit("attribute-a", "过期", 10)
	stale.Target.LeaseToken = expiredToken
	if _, err := newAttributeEditService(store, leases, fixedAttributeID).Submit(stale); err == nil {
		t.Fatal("expired token submitted")
	}
	created := existingAttributeEdit("", "新属性", 7)
	created.Target = attributeEditTarget{Kind: "new"}
	created.GiftRules = nil
	created.TimerRules = nil
	created.GiftCatalogUpserts = nil
	result, err := newAttributeEditService(store, leases, fixedAttributeID).Submit(created)
	if err != nil || result.ID != "generated-attribute" || !result.Created {
		t.Fatalf("new result=%#v err=%v", result, err)
	}
	prepared, err := newAttributeEditService(store, leases, fixedAttributeID).Prepare(attributeEditSessionRequest{AttributeID: result.ID})
	if err != nil || prepared.AttributeID != result.ID {
		t.Fatalf("new stable ID was not addressable: session=%#v err=%v", prepared, err)
	}
}

func assertAtomicAttributeEditPeerState(t *testing.T, state appState, targetName string, targetValue, peerValue float64) {
	t.Helper()
	if len(state.Attributes) != 2 || state.Attributes[0].ID != "attribute-a" || state.Attributes[0].Name != targetName || state.Attributes[0].Value != targetValue || state.Attributes[1].ID != "attribute-b" || state.Attributes[1].Name != "B" || state.Attributes[1].Value != peerValue {
		t.Fatalf("state = %#v", state.Attributes)
	}
}

func assertAtomicAttributeEditOwnedRules(t *testing.T, state appState, giftIDs, giftFormulas, timerIDs, timerFormulas []string) {
	t.Helper()
	assertGiftRuleIDs(t, state.Rules, giftIDs...)
	assertTimerRuleIDs(t, state.TimerRules, timerIDs...)
	for index, want := range giftFormulas {
		if state.Rules[index].Formula != want {
			t.Fatalf("gift rule[%d] formula=%q want=%q rules=%#v", index, state.Rules[index].Formula, want, state.Rules)
		}
	}
	for index, want := range timerFormulas {
		if state.TimerRules[index].Formula != want {
			t.Fatalf("timer rule[%d] formula=%q want=%q rules=%#v", index, state.TimerRules[index].Formula, want, state.TimerRules)
		}
	}
}

func attributeEditFixtureStore(t *testing.T) *configStore {
	t.Helper()
	store := &configStore{path: filepath.Join(t.TempDir(), "config.json")}
	if err := store.replaceState(attributeEditFixtureState()); err != nil {
		t.Fatal(err)
	}
	return store
}

func attributeEditLegacyFixtureStore(t *testing.T, name string) *configStore {
	t.Helper()
	store := &configStore{path: filepath.Join(t.TempDir(), "config.json")}
	state := defaultAppState()
	state.Attributes = []attributeState{{Name: name, Value: 1, Color: "#112233"}}
	if err := store.replaceState(state); err != nil {
		t.Fatal(err)
	}
	return store
}

func timeNowForAttributeEditTest() time.Time { return time.Unix(100, 0) }

func existingAttributeEdit(id, name string, value float64) attributeEditCommand {
	return attributeEditCommand{
		Target:    attributeEditTarget{Kind: "existing", AttributeID: id},
		Attribute: attributeState{ID: "client-controlled", Name: name, Value: value, Color: "#ffffff", CreatedFromTemplateID: "client-template", CreatedFromTemplateVersion: 99},
		GiftRules: []giftRule{
			{ID: "gift-a-2", GiftID: 2, AttributeName: name, FormulaName: "target updated", Condition: name + " >= 0", Formula: name + " + 5"},
			{ID: "gift-a-3", GiftID: 3, AttributeName: name, FormulaName: "target new", Formula: name + " + 6"},
		},
		TimerRules: []timerRule{
			{ID: "timer-a-2", AttributeName: name, FormulaName: "timer updated", IntervalSeconds: 5, Condition: name + " >= 0", Formula: name + " + 5", Enabled: true},
			{ID: "timer-a-3", AttributeName: name, FormulaName: "timer new", IntervalSeconds: 6, Formula: name + " + 6", Enabled: true},
		},
		GiftCatalogUpserts: []giftInfo{{ID: 1, Name: "更新礼物"}, {ID: 3, Name: "新增礼物"}},
	}
}

func attributeEditFixtureState() appState {
	state := defaultAppState()
	state.Attributes = []attributeState{
		{ID: "attribute-a", Name: "积分", Value: 1, Color: "#112233", CreatedFromTemplateID: "template-a", CreatedFromTemplateVersion: 7},
		{ID: "attribute-b", Name: "B", Value: 2, Color: "#445566"},
	}
	state.Rules = []giftRule{
		{ID: "gift-a-1", GiftID: 1, AttributeName: "积分", FormulaName: "target removed", Formula: "积分 + 1"},
		{ID: "gift-b", GiftID: 9, AttributeName: "B", FormulaName: "peer", Formula: "B + 1"},
		{ID: "gift-a-2", GiftID: 2, AttributeName: "积分", FormulaName: "target kept", Condition: "积分 >= 0", Formula: "积分 + 2"},
	}
	state.TimerRules = []timerRule{
		{ID: "timer-a-1", AttributeName: "积分", FormulaName: "timer removed", IntervalSeconds: 1, Formula: "积分 + 1", Enabled: true},
		{ID: "timer-b", AttributeName: "B", FormulaName: "timer peer", IntervalSeconds: 1, Formula: "B + 1", Enabled: true},
		{ID: "timer-a-2", AttributeName: "积分", FormulaName: "timer kept", IntervalSeconds: 2, Condition: "积分 >= 0", Formula: "积分 + 2", Enabled: true},
	}
	state.DisplayScenes = []displaySceneState{{ID: "scene-1", Name: "scene", AttributeNames: []string{"积分", "B"}, Layout: "grid", ThemeID: "glass"}}
	state.Activities = []activitySessionState{{
		ID: "activity-1", Name: "activity", AttributeNames: []string{"积分", "B"}, Status: "active", ResultMode: "highest",
		InitialValues: map[string]float64{"积分": 1, "B": 2},
		Milestones:    []activityMilestoneState{{ID: "milestone-1", Name: "milestone", AttributeName: "积分", Comparison: "gte", Threshold: 9, Action: "announce"}},
		Result:        &activityResultState{WinnerAttributeName: "积分", Values: map[string]float64{"积分": 1, "B": 2}},
	}}
	state.FormulaPresets = []formulaPreset{{ID: "preset-1", Name: "preset", Context: "gift", Formula: "积分 + 3", SourceAttributeName: "积分"}}
	state.GiftCatalog = []giftInfo{{ID: 1, Name: "旧礼物"}, {ID: 2, Name: "同伴礼物"}}
	state.Log = []logEntry{{AttributeName: "积分"}}
	state.GiftReceipts = []giftReceipt{{Effects: []giftReceiptEffect{{AttributeName: "积分"}}}}
	state.Contributions = contributionLedgerState{Viewers: []viewerContribution{{AttributeDeltas: map[string]float64{"积分": 4}}}}
	return state
}
