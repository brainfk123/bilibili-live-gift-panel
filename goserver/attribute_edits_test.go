package main

import (
	"errors"
	"path/filepath"
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
	enteredPersistence := make(chan struct{})
	releasePersistence := make(chan struct{})
	store.writeAtomically = func(string, []byte) error {
		close(enteredPersistence)
		<-releasePersistence
		return errors.New("injected write failure")
	}

	store.mu.Lock()
	done := make(chan error, 1)
	go func() {
		_, err := service.Submit(command)
		done <- err
	}()
	select {
	case <-enteredPersistence:
		store.mu.Unlock()
		close(releasePersistence)
		<-done
		t.Fatal("submit reached persistence before acquiring store lock")
	case <-time.After(50 * time.Millisecond):
		store.mu.Unlock()
	}
	<-enteredPersistence
	close(releasePersistence)
	if err := <-done; err == nil {
		t.Fatal("expected injected write failure")
	}
	if !leases.Has("attribute-a", token) {
		t.Fatal("lease was not retained through the store-lock wait")
	}
}

func TestAttributeEditSubmitRejectsLeaseThatExpiresAtStoreWriteBoundary(t *testing.T) {
	now := time.Unix(100, 0)
	nowCalls := 0
	liveChecked := make(chan struct{})
	leases := newAttributeEditLeaseCoordinator(15*time.Second, func() time.Time {
		nowCalls++
		if nowCalls == 2 { // Create consumes the first clock read.
			close(liveChecked)
		}
		return now
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
	now = now.Add(15 * time.Second)
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
	select {
	case err := <-deleted:
		t.Fatalf("delete interleaved before lease/session completion: %v", err)
	case <-time.After(30 * time.Millisecond):
	}
	close(resumeCreate)
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
	leases.afterBegin = func() {
		close(claimStarted)
		<-allowSubmit
	}
	command := existingAttributeEdit("attribute-a", "能量", 10)
	command.Target.LeaseToken = token
	submitted := make(chan error, 1)
	go func() {
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
	select {
	case err := <-backgroundDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(200 * time.Millisecond):
		close(allowSubmit)
		t.Fatal("background store->lease check deadlocked with submit")
	}
	close(allowSubmit)
	if err := <-submitted; err != nil {
		t.Fatal(err)
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
