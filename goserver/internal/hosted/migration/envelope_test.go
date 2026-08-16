package migration

import (
	"bytes"
	"encoding/json"
	"strconv"
	"strings"
	"testing"
)

func TestDecodeRejectsInvalidEnvelopeBoundaries(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(map[string]any)
	}{
		{"wrong kind", func(document map[string]any) { document["kind"] = "backup" }},
		{"wrong migration version", func(document map[string]any) { document["migrationVersion"] = 2 }},
		{"newer configuration schema", func(document map[string]any) { document["source"].(map[string]any)["configSchemaVersion"] = 6 }},
		{"too many attributes", func(document map[string]any) {
			document["payload"].(map[string]any)["definition"].(map[string]any)["attributes"] = repeat(attributeWire(), 201)
		}},
		{"too many rules", func(document map[string]any) {
			document["payload"].(map[string]any)["definition"].(map[string]any)["rules"] = repeat(ruleWire(), 501)
		}},
		{"too many activities", func(document map[string]any) {
			document["payload"].(map[string]any)["definition"].(map[string]any)["activities"] = repeat(map[string]any{}, 101)
		}},
		{"too many panels", func(document map[string]any) {
			document["payload"].(map[string]any)["definition"].(map[string]any)["giftTargetPanels"] = repeat(map[string]any{}, 101)
		}},
		{"too many panel items", func(document map[string]any) {
			document["payload"].(map[string]any)["definition"].(map[string]any)["giftTargetPanels"] = []any{map[string]any{"id": "panel", "items": repeat(map[string]any{}, 201)}}
		}},
		{"long string", func(document map[string]any) {
			document["payload"].(map[string]any)["definition"].(map[string]any)["attributes"].([]any)[0].(map[string]any)["name"] = strings.Repeat("界", 4097)
		}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			document := validEnvelopeWire()
			test.mutate(document)
			if _, _, err := Decode(jsonReader(t, document), 2<<20); err == nil {
				t.Fatal("Decode accepted an invalid migration envelope")
			}
		})
	}
}

func TestDecodeRejectsOversizeDepthSecondValueAndNonFiniteNumbers(t *testing.T) {
	oversize := bytes.NewReader(append(validEnvelopeBytes(t), bytes.Repeat([]byte(" "), 2<<20)...))
	if _, _, err := Decode(oversize, 2<<20); err == nil {
		t.Fatal("Decode accepted more than 2 MiB")
	}
	if _, _, err := Decode(bytes.NewReader(validEnvelopeBytes(t)), 32); err == nil {
		t.Fatal("Decode ignored the caller's smaller byte limit")
	}

	deep := strings.Repeat("[", 33) + strings.Repeat("]", 33)
	if _, _, err := Decode(strings.NewReader(deep), 2<<20); err == nil {
		t.Fatal("Decode accepted JSON deeper than 32 levels")
	}

	second := append(validEnvelopeBytes(t), []byte(` {"kind":"gift-panel-online-migration"}`)...)
	if _, _, err := Decode(bytes.NewReader(second), 2<<20); err == nil {
		t.Fatal("Decode accepted a second JSON value")
	}

	nonFinite := strings.Replace(string(validEnvelopeBytes(t)), `"value":1`, `"value":1e999999`, 1)
	if _, _, err := Decode(strings.NewReader(nonFinite), 2<<20); err == nil {
		t.Fatal("Decode accepted a non-finite number")
	}
}

func TestDecodeRejectsMissingOrMalformedKnownFields(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(map[string]any)
	}{
		{"missing runtime", func(document map[string]any) { delete(document["payload"].(map[string]any), "runtime") }},
		{"missing definition", func(document map[string]any) { delete(document["payload"].(map[string]any), "definition") }},
		{"room suggestion number", func(document map[string]any) { document["payload"].(map[string]any)["roomSuggestion"] = 12345 }},
		{"attribute values wrong type", func(document map[string]any) {
			document["payload"].(map[string]any)["runtime"].(map[string]any)["attributeValues"] = []any{}
		}},
		{"missing payload", func(document map[string]any) { delete(document, "payload") }},
		{"null payload", func(document map[string]any) { document["payload"] = nil }},
		{"null definition", func(document map[string]any) { document["payload"].(map[string]any)["definition"] = nil }},
		{"null runtime", func(document map[string]any) { document["payload"].(map[string]any)["runtime"] = nil }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			document := validEnvelopeWire()
			test.mutate(document)
			if _, _, err := Decode(jsonReader(t, document), 2<<20); err != ErrInvalidEnvelope {
				t.Fatalf("got %v, want stable invalid envelope", err)
			}
		})
	}
}

func TestDecodeFiltersUnknownAndSensitiveFieldsBeforeHashing(t *testing.T) {
	document := validEnvelopeWire()
	payload := document["payload"].(map[string]any)
	payload["cookie"] = "secret"
	payload["futureField"] = "ignored"
	definition := payload["definition"].(map[string]any)
	definition["appearance"] = map[string]any{"theme": "dark", "cookie": "secret"}
	definition["gifts"] = []any{map[string]any{"id": 1, "name": "gift", "price": 1, "coinType": "gold", "imageUrl": "https://attacker.invalid/a.png"}}
	definition["timerRules"] = []any{map[string]any{"id": "tick", "attributeId": "health", "formulaName": "tick", "intervalSeconds": 1, "formula": "1", "enabled": true, "futureRuleField": true}}

	envelope, report, err := Decode(jsonReader(t, document), 2<<20)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(envelope.CanonicalJSON), "secret") || strings.Contains(string(envelope.CanonicalJSON), "attacker.invalid") {
		t.Fatal("filtered input reached canonical stored representation")
	}
	for _, pointer := range []string{"/payload/cookie", "/payload/futureField", "/payload/definition/appearance", "/payload/definition/gifts/0/imageUrl", "/payload/definition/timerRules/0/futureRuleField"} {
		if !contains(report.Ignored, pointer) {
			t.Fatalf("missing ignored pointer %q: %#v", pointer, report.Ignored)
		}
	}
	if len(report.Warnings) == 0 {
		t.Fatal("sensitive stripping did not produce a safe warning")
	}
}

func TestDecodeCanonicalHashIgnoresFieldOrderAndUnknownFields(t *testing.T) {
	first := validEnvelopeWire()
	second := validEnvelopeWire()
	second["ignored"] = true
	firstEnvelope, _, err := Decode(jsonReader(t, first), 2<<20)
	if err != nil {
		t.Fatal(err)
	}
	secondEnvelope, _, err := Decode(jsonReader(t, second), 2<<20)
	if err != nil {
		t.Fatal(err)
	}
	if firstEnvelope.Hash != secondEnvelope.Hash {
		t.Fatalf("hash changed after ignored input: %x != %x", firstEnvelope.Hash, secondEnvelope.Hash)
	}
}

func TestDecodeFreshAllowlistsSimplePlayWithoutSavingCraftedValues(t *testing.T) {
	document := validEnvelopeWire()
	definition := document["payload"].(map[string]any)["definition"].(map[string]any)
	definition["gifts"] = []any{map[string]any{"id": 1, "name": "gift", "price": 1, "coinType": "gold"}}
	definition["simplePlay"] = map[string]any{
		"version": 1, "templateId": "overtime", "templateVersion": 2, "attributeId": "health", "managedFingerprint": "managed",
		"parameters":          map[string]any{"name": "safe", "maxSeconds": 60, "broadcastMessage": "thanks", "cookie": "secret", "token": "secret-token", "path": "C:\\secret"},
		"gifts":               map[string]any{"overtime": []any{1}, "unknownSlot": []any{1}},
		"overtimeGiftActions": []any{map[string]any{"giftId": 1, "operation": "add", "seconds": 60}, map[string]any{"giftId": 1, "operation": "shell", "seconds": 60}},
	}
	envelope, report, err := Decode(jsonReader(t, document), 2<<20)
	if err != nil {
		t.Fatal(err)
	}
	if envelope.Definition.SimplePlay == nil {
		t.Fatal("valid allowlisted simple play was removed")
	}
	if _, exists := envelope.Definition.SimplePlay.Parameters["cookie"]; exists {
		t.Fatal("crafted parameter reached definition")
	}
	if strings.Contains(string(envelope.CanonicalJSON), "secret") || strings.Contains(string(envelope.CanonicalJSON), "C:\\secret") || strings.Contains(string(envelope.CanonicalJSON), "unknownSlot") || strings.Contains(string(envelope.CanonicalJSON), "shell") {
		t.Fatal("crafted simple play value reached canonical JSON")
	}
	for _, pointer := range []string{"/payload/definition/simplePlay/parameters/cookie", "/payload/definition/simplePlay/parameters/token", "/payload/definition/simplePlay/parameters/path", "/payload/definition/simplePlay/gifts/unknownSlot", "/payload/definition/simplePlay/overtimeGiftActions/1/operation"} {
		if !contains(report.Ignored, pointer) {
			t.Fatalf("missing ignored pointer %q: %#v", pointer, report.Ignored)
		}
	}
}

func TestDecodeMirrorsAllDesktopSimplePlayVersions(t *testing.T) {
	tests := []struct {
		id         string
		version    int
		parameters map[string]any
		slots      map[string][]any
	}{
		{"overtime", 1, map[string]any{"name": "n", "minutesPerYuan": 60, "maxHours": 0, "broadcastMessage": "b"}, map[string][]any{"overtime": {1}}},
		{"overtime", 2, map[string]any{"name": "n", "maxSeconds": 60, "broadcastMessage": "b"}, map[string][]any{"overtime": {1}}},
		{"countdown", 1, map[string]any{"name": "n", "initialSeconds": 1800, "growthMode": "fixed", "addSeconds": 60, "maxSeconds": 7200, "broadcastMessage": "b"}, map[string][]any{"extend": {1}}},
		{"counter", 1, map[string]any{"name": "n", "suffix": "次", "amount": 1, "cap": 0, "broadcastMessage": "b"}, map[string][]any{"count": {1}}},
		{"goal", 1, map[string]any{"name": "n", "target": 100, "perYuan": 1, "broadcastMessage": "b"}, map[string][]any{"progress": {1}}},
		{"boss", 1, map[string]any{"name": "n", "bossName": "b", "maxHealth": 1000, "attack": 50, "heavy": 200, "heal": 100, "regenEnabled": false, "regenInterval": 10, "regenAmount": 10, "broadcastMessage": "b"}, map[string][]any{"attack": {1}, "heavy": {2}, "heal": {3}}},
		{"resource", 1, map[string]any{"name": "n", "maximum": 100, "consumeInterval": 5, "consumeAmount": 1, "smallSupply": 10, "largeSupply": 30, "interference": 10, "broadcastMessage": "b"}, map[string][]any{"small": {1}, "large": {2}, "interference": {3}}},
		{"tug", 1, map[string]any{"name": "n", "leftLabel": "l", "rightLabel": "r", "initial": 50, "leftAmount": 10, "rightAmount": 10, "broadcastMessage": "b"}, map[string][]any{"left": {1}, "right": {2}}},
		{"team-duel", 1, map[string]any{"activityName": "a", "leftName": "l", "rightName": "r", "target": 100, "points": 1, "broadcastMessage": "b"}, map[string][]any{"left": {1}, "right": {2}}},
		{"gift-vote", 1, map[string]any{"activityName": "a", "leftName": "l", "rightName": "r", "votes": 1, "broadcastMessage": "b"}, map[string][]any{"left": {1}, "right": {2}}},
		{"combo", 1, map[string]any{"name": "n", "timeout": 15, "goal": 50, "broadcastMessage": "b"}, map[string][]any{"combo": {1}}},
		{"milestone", 1, map[string]any{"name": "n", "target": 100, "amount": 1, "message": "m", "broadcastMessage": "b"}, map[string][]any{"progress": {1}}},
		{"random-event", 1, map[string]any{"name": "n", "event1": "a", "event2": "b", "event3": "c", "event4": "d", "broadcastMessage": "b"}, map[string][]any{"draw": {1}}},
	}
	for _, test := range tests {
		t.Run(test.id+"-v"+strconv.Itoa(test.version), func(t *testing.T) {
			document := validEnvelopeWire()
			definition := document["payload"].(map[string]any)["definition"].(map[string]any)
			definition["gifts"] = []any{map[string]any{"id": 1, "name": "one", "price": 1, "coinType": "gold"}, map[string]any{"id": 2, "name": "two", "price": 1, "coinType": "gold"}, map[string]any{"id": 3, "name": "three", "price": 1, "coinType": "gold"}}
			simple := map[string]any{"version": 1, "templateId": test.id, "templateVersion": test.version, "attributeId": "health", "parameters": test.parameters, "gifts": test.slots, "managedFingerprint": "managed"}
			if test.id == "overtime" && test.version == 2 {
				simple["overtimeGiftActions"] = []any{map[string]any{"giftId": 1, "operation": "add", "seconds": 60}}
			}
			definition["simplePlay"] = simple
			envelope, _, err := Decode(jsonReader(t, document), 2<<20)
			if err != nil {
				t.Fatal(err)
			}
			if envelope.Definition.SimplePlay == nil || envelope.Definition.SimplePlay.TemplateID != test.id || envelope.Definition.SimplePlay.TemplateVersion != test.version {
				t.Fatalf("simple play not retained: %#v", envelope.Definition.SimplePlay)
			}
			if len(envelope.Definition.SimplePlay.Parameters) != len(test.parameters) || len(envelope.Definition.SimplePlay.Gifts) != len(test.slots) {
				t.Fatalf("simple play projection mismatch: %#v", envelope.Definition.SimplePlay)
			}
		})
	}
}

func TestDecodeSimplePlayDefaultsOnlyMissingAndFailsClosedForPresentInvalid(t *testing.T) {
	base := func(parameters map[string]any) map[string]any {
		document := validEnvelopeWire()
		definition := document["payload"].(map[string]any)["definition"].(map[string]any)
		definition["gifts"] = []any{map[string]any{"id": 1, "name": "gift", "price": 1, "coinType": "gold"}}
		definition["simplePlay"] = map[string]any{"version": 1, "templateId": "counter", "templateVersion": 1, "attributeId": "health", "parameters": parameters, "gifts": map[string]any{"count": []any{1}}, "managedFingerprint": "managed"}
		return document
	}
	envelope, _, err := Decode(jsonReader(t, base(map[string]any{})), 2<<20)
	if err != nil || envelope.Definition.SimplePlay == nil || envelope.Definition.SimplePlay.Parameters["amount"] != float64(1) {
		t.Fatalf("missing parameter did not receive desktop default: %#v, %v", envelope.Definition.SimplePlay, err)
	}
	for _, parameters := range []map[string]any{{"name": ""}, {"amount": "wrong"}, {"cap": nil}} {
		envelope, report, err := Decode(jsonReader(t, base(parameters)), 2<<20)
		if err != nil {
			t.Fatal(err)
		}
		if envelope.Definition.SimplePlay != nil {
			t.Fatalf("present invalid parameter was defaulted: %#v", envelope.Definition.SimplePlay)
		}
		if len(report.Warnings) == 0 {
			t.Fatal("removed invalid simple play has no warning")
		}
	}
}

func TestSimplePlayTextResourceParity(t *testing.T) {
	for _, value := range []string{"10/20", "A/B", "PK: boss", "HP: 10"} {
		if !safeSimpleText(value) {
			t.Fatalf("safe desktop text rejected: %q", value)
		}
	}
	for _, value := range []string{"ipfs:cid", "s3:bucket", "content:thing", "https://host/path", "C:\\avatar.png", "/root/path", " ./relative", "avatar.png"} {
		if safeSimpleText(value) {
			t.Fatalf("resource-like desktop text accepted: %q", value)
		}
	}
}

func TestDecodeSimplePlayRejectsCrossSlotGiftReuse(t *testing.T) {
	document := validEnvelopeWire()
	definition := document["payload"].(map[string]any)["definition"].(map[string]any)
	definition["gifts"] = []any{map[string]any{"id": 1, "name": "gift", "price": 1, "coinType": "gold"}}
	definition["simplePlay"] = map[string]any{"version": 1, "templateId": "tug", "templateVersion": 1, "attributeId": "health", "managedFingerprint": "managed", "parameters": map[string]any{"name": "n", "leftLabel": "l", "rightLabel": "r", "initial": 50, "leftAmount": 10, "rightAmount": 10, "broadcastMessage": "b"}, "gifts": map[string]any{"left": []any{1}, "right": []any{1}}}
	envelope, report, err := Decode(jsonReader(t, document), 2<<20)
	if err != nil {
		t.Fatal(err)
	}
	if envelope.Definition.SimplePlay != nil {
		t.Fatal("cross-slot duplicate retained simple play")
	}
	if !contains(report.Ignored, "/payload/definition/simplePlay/gifts/right") {
		t.Fatalf("duplicate slot was not reported: %#v", report.Ignored)
	}
}

func TestDecodeSimplePlayPreservesDesktopSlotPrecedence(t *testing.T) {
	document := validEnvelopeWire()
	definition := document["payload"].(map[string]any)["definition"].(map[string]any)
	definition["gifts"] = []any{map[string]any{"id": 1, "name": "gift", "price": 1, "coinType": "gold"}}
	definition["simplePlay"] = map[string]any{
		"version": 1, "templateId": "resource", "templateVersion": 1, "attributeId": "health", "managedFingerprint": "managed",
		"parameters": map[string]any{"name": "n", "maximum": 100, "consumeInterval": 5, "consumeAmount": 1, "smallSupply": 10, "largeSupply": 30, "interference": 10, "broadcastMessage": "b"},
		"gifts":      map[string]any{"small": []any{1}, "interference": []any{1}, "unknownSlot": []any{1}},
	}
	envelope, report, err := Decode(jsonReader(t, document), 2<<20)
	if err != nil {
		t.Fatal(err)
	}
	if envelope.Definition.SimplePlay == nil {
		t.Fatal("later duplicate caused the desktop-first slot to be removed")
	}
	if got := envelope.Definition.SimplePlay.Gifts["small"]; len(got) != 1 || got[0] != 1 {
		t.Fatalf("first declared slot did not retain gift: %#v", envelope.Definition.SimplePlay.Gifts)
	}
	if got := envelope.Definition.SimplePlay.Gifts["interference"]; len(got) != 0 {
		t.Fatalf("later declared slot retained duplicate: %#v", envelope.Definition.SimplePlay.Gifts)
	}
	for _, pointer := range []string{
		"/payload/definition/simplePlay/gifts/interference/0",
		"/payload/definition/simplePlay/gifts/unknownSlot",
	} {
		if !contains(report.Ignored, pointer) {
			t.Fatalf("missing ignored pointer %q: %#v", pointer, report.Ignored)
		}
	}
}

func validEnvelopeWire() map[string]any {
	return map[string]any{
		"kind": "gift-panel-online-migration", "migrationVersion": 1,
		"source":     map[string]any{"appVersion": "0.4.4", "configSchemaVersion": 5},
		"exportedAt": "2026-08-16T00:00:00Z",
		"payload": map[string]any{
			"roomSuggestion": "12345",
			"definition": map[string]any{
				"attributes": []any{attributeWire()}, "displayScenes": []any{}, "giftTargetPanels": []any{},
				"activities": []any{}, "rules": []any{}, "timerRules": []any{}, "formulaPresets": []any{}, "gifts": []any{},
			},
			"runtime": map[string]any{
				"attributeValues": map[string]any{"health": 1}, "giftTargetReceived": []any{}, "activities": []any{},
				"ruleLimits": map[string]any{"localDate": "2026-08-16", "appliedCounts": map[string]any{}},
			},
		},
	}
}

func attributeWire() map[string]any {
	return map[string]any{"id": "health", "name": "Health", "unit": "", "format": "number", "decimals": 0, "suffix": "", "value": 1}
}

func ruleWire() map[string]any {
	return map[string]any{"id": "rule", "giftId": 1, "attributeId": "health", "formula": "1"}
}

func repeat(value map[string]any, count int) []any {
	values := make([]any, count)
	for index := range values {
		clone := make(map[string]any, len(value))
		for key, item := range value {
			clone[key] = item
		}
		values[index] = clone
	}
	return values
}

func jsonReader(t *testing.T, value any) *bytes.Reader {
	t.Helper()
	return bytes.NewReader(mustJSON(t, value))
}
func validEnvelopeBytes(t *testing.T) []byte { t.Helper(); return mustJSON(t, validEnvelopeWire()) }
func mustJSON(t *testing.T, value any) []byte {
	t.Helper()
	result, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return result
}
func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
