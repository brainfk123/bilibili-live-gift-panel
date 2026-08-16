package configuration

import (
	"bytes"
	"encoding/json"
	"reflect"
	"testing"

	"bilibili-live-gift-panel/internal/gameplay"
)

func TestSplitSeparatesDefinitionFromMutableState(t *testing.T) {
	snapshot := fixtureSnapshot()

	definition, state, err := Split(snapshot)
	if err != nil {
		t.Fatalf("Split() error = %v", err)
	}
	definitionJSON, err := json.Marshal(definition)
	if err != nil {
		t.Fatalf("marshal definition: %v", err)
	}
	stateJSON, err := json.Marshal(state)
	if err != nil {
		t.Fatalf("marshal runtime state: %v", err)
	}
	if bytes.Contains(definitionJSON, []byte(`"value":42`)) {
		t.Fatalf("attribute value leaked into definition: %s", definitionJSON)
	}
	if bytes.Contains(definitionJSON, []byte(`"received":2`)) {
		t.Fatalf("gift target progress leaked into definition: %s", definitionJSON)
	}
	if bytes.Contains(stateJSON, []byte(`"formula"`)) {
		t.Fatalf("formula leaked into runtime state: %s", stateJSON)
	}
	got, err := Join(definition, state)
	if err != nil {
		t.Fatalf("Join() error = %v", err)
	}
	if want := storageProjection(snapshot); !reflect.DeepEqual(got, want) {
		t.Fatalf("Join(Split(snapshot)) = %#v, want %#v", got, want)
	}
}

func TestSplitCopiesDefinitionAndRuntimeIndependently(t *testing.T) {
	snapshot := fixtureSnapshot()
	definition, state, err := Split(snapshot)
	if err != nil {
		t.Fatalf("Split() error = %v", err)
	}

	snapshot.Attributes[0].Value = 0
	snapshot.Rules[0].Formula = "changed"
	snapshot.Activities[0].Milestones[0].TriggeredAtMillis = 0

	if definition.Rules[0].Formula != "count * 2" {
		t.Fatalf("definition aliases snapshot rule formula: %#v", definition.Rules)
	}
	if state.AttributeValues["health"] != 42 || state.Activities[0].Milestones[0].TriggeredAtMillis != 1_700_000_001_000 {
		t.Fatalf("runtime state aliases snapshot: %#v", state)
	}
}

func TestJoinRejectsMissingOrUnexpectedRuntimeEntries(t *testing.T) {
	definition, runtime, err := Split(fixtureSnapshot())
	if err != nil {
		t.Fatalf("Split() error = %v", err)
	}

	missing := runtime
	delete(missing.AttributeValues, "health")
	if _, err := Join(definition, missing); err == nil {
		t.Fatal("Join() accepted missing attribute runtime value")
	}

	extra := runtime
	extra.AttributeValues["not-configured"] = 1
	if _, err := Join(definition, extra); err == nil {
		t.Fatal("Join() accepted unexpected attribute runtime value")
	}
}

func TestDefaultRuntimeCompletesIdleStateForDefinition(t *testing.T) {
	definition, _, err := Split(fixtureSnapshot())
	if err != nil {
		t.Fatalf("Split() error = %v", err)
	}

	runtime := DefaultRuntime(definition)
	got, err := Join(definition, runtime)
	if err != nil {
		t.Fatalf("Join(DefaultRuntime()) error = %v", err)
	}
	if _, err := gameplay.Normalize(got); err != nil {
		t.Fatalf("Normalize(Join(DefaultRuntime())) error = %v", err)
	}
	if got.Attributes[0].Value != 0 || got.GiftTargetPanels[0].Items[0].Received != 0 || got.Activities[0].Status != "not_started" {
		t.Fatalf("default runtime = %#v, want zero and idle values", got)
	}
}

func storageProjection(snapshot gameplay.Snapshot) gameplay.Snapshot {
	snapshot.RoomID = ""
	for index := range snapshot.Gifts {
		snapshot.Gifts[index].ImageURL = ""
	}
	for panelIndex := range snapshot.GiftTargetPanels {
		for itemIndex := range snapshot.GiftTargetPanels[panelIndex].Items {
			snapshot.GiftTargetPanels[panelIndex].Items[itemIndex].ImageURL = ""
		}
	}
	return snapshot
}

func fixtureSnapshot() gameplay.Snapshot {
	minimum := 0.0
	enabled := true
	triggerValue := 42.0
	return gameplay.Snapshot{
		RoomID:           "12345",
		Attributes:       []gameplay.Attribute{{ID: "health", Name: "Health", Value: 42, Unit: "hp", Display: &gameplay.Display{Min: &minimum}}},
		DisplayScenes:    []gameplay.DisplayScene{{ID: "main", Name: "Main", AttributeIDs: []string{"health"}, Layout: "single", ThemeID: "dark"}},
		GiftTargetPanels: []gameplay.GiftTargetPanel{{ID: "goals", Name: "Goals", Layout: "single", Items: []gameplay.GiftTargetItem{{GiftID: 1, Name: "Rose", ImageURL: "https://example.test/target.png", Target: 10, Received: 2, BarStyle: "default"}}}},
		Activities: []gameplay.Activity{{
			ID: "round", Name: "Round", AttributeIDs: []string{"health"}, Status: "active", ResultMode: "highest", GateRules: true,
			InitialValues:   map[string]float64{"health": 100},
			Milestones:      []gameplay.Milestone{{ID: "health-cap", Name: "Health cap", AttributeID: "health", Comparison: "gte", Threshold: 40, Action: "lock", TriggeredAtMillis: 1_700_000_001_000, TriggerValue: &triggerValue}},
			GiftTimeout:     &gameplay.GiftTimeout{Seconds: 60, Action: "lock", LastGiftAtMillis: 1_700_000_002_000, DeadlineAtMillis: 1_700_000_062_000},
			StartedAtMillis: 1_700_000_000_000,
		}},
		Rules:          []gameplay.Rule{{ID: "gift-health", GiftID: 1, AttributeID: "health", Formula: "count * 2", Enabled: &enabled}},
		TimerRules:     []gameplay.TimerRule{{ID: "drain", AttributeID: "health", FormulaName: "drain", IntervalSeconds: 60, Formula: "-1", Enabled: true}},
		FormulaPresets: []gameplay.FormulaPreset{{ID: "drain", Name: "Drain", Context: "timer", Formula: "-1", AttributeID: "health"}},
		SimplePlay:     &gameplay.SimplePlay{Version: 1, TemplateID: "survival", TemplateVersion: 1, AttributeID: "health", Parameters: map[string]any{"difficulty": "normal"}, Gifts: map[string][]int{"heal": {1}}, ManagedFingerprint: "fingerprint"},
		Gifts:          []gameplay.GiftInfo{{ID: 1, Name: "Rose", Price: 1, CoinType: "gold", ImageURL: "https://example.test/gift.png"}},
		RuleLimits:     gameplay.RuleLimitState{LocalDate: "2026-08-16", AppliedCounts: map[string]int{"gift-health": 2}},
	}
}
