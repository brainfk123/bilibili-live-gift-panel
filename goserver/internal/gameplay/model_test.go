package gameplay

import (
	"encoding/json"
	"math"
	"reflect"
	"regexp"
	"strings"
	"testing"
)

func TestPublicModelExcludesViewerAndCredentialFields(t *testing.T) {
	t.Parallel()

	forbidden := regexp.MustCompile(`(?i)uid|uname|nickname|avatar|cookie|token|receipt|contribution|log`)
	for _, value := range []any{Snapshot{}, Gift{}, Effect{}, TargetNotice{}, ActivityNotice{}, Transition{}} {
		typ := reflect.TypeOf(value)
		for i := 0; i < typ.NumField(); i++ {
			field := typ.Field(i)
			if forbidden.MatchString(field.Name + " " + field.Tag.Get("json")) {
				t.Fatalf("%s exposes forbidden field %s", typ.Name(), field.Name)
			}
		}
	}
}

func TestEffectJSONShapeExpressesAttributeTargetAndActivityChanges(t *testing.T) {
	t.Parallel()

	want := Effect{
		RuleID:        "gift-health",
		AttributeName: "Health",
		Delta:         3,
		ValueAfter:    45,
		TriggerName:   "Rose",
		Target:        &TargetNotice{PanelID: "goals", GiftID: 1, Received: 4, Target: 10},
		Activity:      &ActivityNotice{ActivityID: "round", Action: "lock", Status: "locked", MilestoneID: "health-cap"},
	}

	encoded, err := json.Marshal(want)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	if strings.Contains(string(encoded), "attributeId") || !strings.Contains(string(encoded), `"attributeName":"Health"`) {
		t.Fatalf("Effect JSON shape = %s, want attributeName without attributeId", encoded)
	}
	var got Effect
	if err := json.Unmarshal(encoded, &got); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Effect JSON round trip = %#v, want %#v", got, want)
	}
}

func TestSnapshotJSONRoundTrip(t *testing.T) {
	t.Parallel()

	want, err := Normalize(Snapshot{
		RoomID:     "123",
		Attributes: []Attribute{{ID: "health", Name: "Health", Value: 42, Unit: "hp"}},
		Rules:      []Rule{{ID: "gift-health", GiftID: 1, AttributeID: "health", Formula: "count * 2"}},
		TimerRules: []TimerRule{{ID: "drain", AttributeID: "health", IntervalSeconds: 60, Formula: "-1", Enabled: true}},
		Activities: []Activity{{
			ID: "round", Name: "Round", AttributeIDs: []string{"health"}, Status: "active",
			InitialValues: map[string]float64{"health": 100},
		}},
		GiftTargetPanels: []GiftTargetPanel{{
			ID: "goals", Name: "Goals", Items: []GiftTargetItem{{GiftID: 1, Target: 10, Received: 2}},
		}},
		Gifts:      []GiftInfo{{ID: 1, Name: "Rose", Price: 1}},
		SimplePlay: &SimplePlay{TemplateID: "survival", Parameters: map[string]any{"difficulty": "normal"}, Gifts: map[string][]int{"heal": {1}}},
	})
	if err != nil {
		t.Fatalf("Normalize() error = %v", err)
	}

	encoded, err := json.Marshal(want)
	if err != nil {
		t.Fatalf("json.Marshal() error = %v", err)
	}
	var decoded Snapshot
	if err := json.Unmarshal(encoded, &decoded); err != nil {
		t.Fatalf("json.Unmarshal() error = %v", err)
	}
	got, err := Normalize(decoded)
	if err != nil {
		t.Fatalf("Normalize(decoded) error = %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("JSON round trip = %#v, want %#v", got, want)
	}
}

func TestNormalizeRejectsBoundsAndInvalidValues(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		snapshot Snapshot
	}{
		{name: "blank attribute id", snapshot: Snapshot{Attributes: []Attribute{{ID: " "}}}},
		{name: "duplicate rule id", snapshot: Snapshot{Rules: []Rule{{ID: "same"}, {ID: "same"}}}},
		{name: "non finite attribute value", snapshot: Snapshot{Attributes: []Attribute{{ID: "health", Value: math.Inf(1)}}}},
		{name: "too many attributes", snapshot: Snapshot{Attributes: make([]Attribute, 201)}},
		{name: "too many rules", snapshot: Snapshot{Rules: make([]Rule, 501)}},
		{name: "too many timers", snapshot: Snapshot{TimerRules: make([]TimerRule, 101)}},
		{name: "too many activities", snapshot: Snapshot{Activities: make([]Activity, 101)}},
		{name: "too many panels", snapshot: Snapshot{GiftTargetPanels: make([]GiftTargetPanel, 101)}},
		{name: "too many panel items", snapshot: Snapshot{GiftTargetPanels: []GiftTargetPanel{{ID: "goals", Items: make([]GiftTargetItem, 201)}}}},
		{name: "too long string", snapshot: Snapshot{RoomID: strings.Repeat("x", 4097)}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := Normalize(test.snapshot); err == nil {
				t.Fatal("Normalize() error = nil, want validation error")
			}
		})
	}
}

func TestNormalizeDeepCopiesMapsAndSlices(t *testing.T) {
	t.Parallel()

	input := Snapshot{
		Attributes:       []Attribute{{ID: "health", Name: "Health", Value: 42}},
		Activities:       []Activity{{ID: "round", AttributeIDs: []string{"health"}, InitialValues: map[string]float64{"health": 42}}},
		GiftTargetPanels: []GiftTargetPanel{{ID: "goals", Items: []GiftTargetItem{{GiftID: 1, Target: 10, Received: 2}}}},
		SimplePlay:       &SimplePlay{Parameters: map[string]any{"nested": map[string]any{"value": "before"}}, Gifts: map[string][]int{"heal": {1}}},
	}

	got, err := Normalize(input)
	if err != nil {
		t.Fatalf("Normalize() error = %v", err)
	}
	input.Attributes[0].Value = 0
	input.Activities[0].AttributeIDs[0] = "changed"
	input.Activities[0].InitialValues["health"] = 0
	input.GiftTargetPanels[0].Items[0].Received = 0
	input.SimplePlay.Parameters["nested"].(map[string]any)["value"] = "after"
	input.SimplePlay.Gifts["heal"][0] = 2

	if got.Attributes[0].Value != 42 || got.Activities[0].AttributeIDs[0] != "health" || got.Activities[0].InitialValues["health"] != 42 || got.GiftTargetPanels[0].Items[0].Received != 2 || got.SimplePlay.Parameters["nested"].(map[string]any)["value"] != "before" || got.SimplePlay.Gifts["heal"][0] != 1 {
		t.Fatalf("Normalize() returned an aliased copy: %#v", got)
	}
}

func TestNormalizeRejectsCyclicOrExcessiveSimplePlayParameters(t *testing.T) {
	t.Parallel()

	deep := map[string]any{}
	current := deep
	for range 1024 {
		next := map[string]any{}
		current["next"] = next
		current = next
	}
	if _, err := Normalize(Snapshot{SimplePlay: &SimplePlay{Parameters: deep}}); err == nil || !strings.Contains(err.Error(), "maximum nesting depth") {
		t.Fatalf("Normalize(deep parameters) error = %v, want maximum nesting depth error", err)
	}

	cycle := map[string]any{}
	cycle["self"] = cycle
	if _, err := Normalize(Snapshot{SimplePlay: &SimplePlay{Parameters: cycle}}); err == nil || !strings.Contains(err.Error(), "cycle") {
		t.Fatalf("Normalize(cyclic parameters) error = %v, want cycle error", err)
	}
}

func TestNormalizeValidatesDynamicCycleBeforeStaticTraversal(t *testing.T) {
	t.Parallel()

	cycle := map[string]any{}
	cycle["self"] = cycle
	_, err := Normalize(Snapshot{SimplePlay: &SimplePlay{Parameters: cycle}})
	if err == nil || err.Error() != "simplePlay.parameters contains a cycle" {
		t.Fatalf("Normalize(cyclic parameters) error = %v, want bounded dynamic validation error", err)
	}
}

func TestNormalizePreservesConcreteDynamicParameterTypes(t *testing.T) {
	t.Parallel()

	const exactInt64 int64 = 9_007_199_254_740_993
	input := Snapshot{SimplePlay: &SimplePlay{Parameters: map[string]any{
		"exact":  exactInt64,
		"labels": map[string]string{"before": "value"},
		"ids":    []int{1, 2},
		"nested": []any{map[string][]int{"scores": {3, 4}}},
	}}}

	got, err := Normalize(input)
	if err != nil {
		t.Fatalf("Normalize() error = %v", err)
	}
	if value, ok := got.SimplePlay.Parameters["exact"].(int64); !ok || value != exactInt64 {
		t.Fatalf("exact parameter = %#v (%T), want int64 %d", got.SimplePlay.Parameters["exact"], got.SimplePlay.Parameters["exact"], exactInt64)
	}
	if labels, ok := got.SimplePlay.Parameters["labels"].(map[string]string); !ok || labels["before"] != "value" {
		t.Fatalf("labels parameter = %#v (%T), want map[string]string", got.SimplePlay.Parameters["labels"], got.SimplePlay.Parameters["labels"])
	}
	if ids, ok := got.SimplePlay.Parameters["ids"].([]int); !ok || !reflect.DeepEqual(ids, []int{1, 2}) {
		t.Fatalf("ids parameter = %#v (%T), want []int{1, 2}", got.SimplePlay.Parameters["ids"], got.SimplePlay.Parameters["ids"])
	}
	if nested, ok := got.SimplePlay.Parameters["nested"].([]any); !ok || !reflect.DeepEqual(nested, []any{map[string][]int{"scores": {3, 4}}}) {
		t.Fatalf("nested parameter = %#v (%T), want nested typed map and slice", got.SimplePlay.Parameters["nested"], got.SimplePlay.Parameters["nested"])
	}

	input.SimplePlay.Parameters["labels"].(map[string]string)["before"] = "changed"
	input.SimplePlay.Parameters["ids"].([]int)[0] = 9
	input.SimplePlay.Parameters["nested"].([]any)[0].(map[string][]int)["scores"][0] = 9
	if got.SimplePlay.Parameters["labels"].(map[string]string)["before"] != "value" || got.SimplePlay.Parameters["ids"].([]int)[0] != 1 || got.SimplePlay.Parameters["nested"].([]any)[0].(map[string][]int)["scores"][0] != 3 {
		t.Fatalf("Normalize() retained aliases in dynamic parameters: %#v", got.SimplePlay.Parameters)
	}
}

func TestNormalizeRejectsUnsupportedDynamicParameterTypes(t *testing.T) {
	t.Parallel()

	_, err := Normalize(Snapshot{SimplePlay: &SimplePlay{Parameters: map[string]any{"bad": make(chan int)}}})
	if err == nil || !strings.Contains(err.Error(), "unsupported dynamic type") {
		t.Fatalf("Normalize() error = %v, want unsupported dynamic type error", err)
	}
}

func TestNormalizeDetachesModelPointers(t *testing.T) {
	t.Parallel()

	minimum, enabled, seconds := 1.0, true, 30
	input := Snapshot{
		Attributes: []Attribute{{ID: "health", Display: &Display{Min: &minimum}}},
		Rules:      []Rule{{ID: "gift-health", Enabled: &enabled, MinPrice: &minimum}},
		Activities: []Activity{{ID: "round", GiftTimeout: &GiftTimeout{Seconds: seconds}}},
		SimplePlay: &SimplePlay{OvertimeGiftActions: []OvertimeGiftAction{{GiftID: 1, Seconds: &seconds}}},
	}

	got, err := Normalize(input)
	if err != nil {
		t.Fatalf("Normalize() error = %v", err)
	}
	minimum, enabled, seconds = 99, false, 99
	if *got.Attributes[0].Display.Min != 1 || !*got.Rules[0].Enabled || *got.Rules[0].MinPrice != 1 || got.Activities[0].GiftTimeout.Seconds != 30 || *got.SimplePlay.OvertimeGiftActions[0].Seconds != 30 {
		t.Fatalf("Normalize() retained a model pointer alias: %#v", got)
	}
}
