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
	for _, value := range []any{Snapshot{}, Gift{}, Effect{}, Transition{}} {
		typ := reflect.TypeOf(value)
		for i := 0; i < typ.NumField(); i++ {
			field := typ.Field(i)
			if forbidden.MatchString(field.Name + " " + field.Tag.Get("json")) {
				t.Fatalf("%s exposes forbidden field %s", typ.Name(), field.Name)
			}
		}
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
