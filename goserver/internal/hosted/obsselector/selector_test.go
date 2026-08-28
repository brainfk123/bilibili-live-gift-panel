package obsselector

import (
	"strings"
	"testing"
)

func TestCanonicalSelectorFixturesRoundTripUnicodeAndColonIDs(t *testing.T) {
	fixtures := []struct {
		selector Selector
		encoded  string
	}{
		{Selector{Kind: "attribute", ID: "积分:🔥"}, "eyJraW5kIjoiYXR0cmlidXRlIiwiaWQiOiLnp6_liIY68J-UpSJ9"},
		{Selector{Kind: "gift-target", ID: "目标/阶段:一"}, "eyJraW5kIjoiZ2lmdC10YXJnZXQiLCJpZCI6Iuebruaghy_pmLbmrrU65LiAIn0"},
		{Selector{Kind: "scene", ID: "主场景:🔥", Attributes: []string{"积分:🔥", "生命值/上限"}}, "eyJraW5kIjoic2NlbmUiLCJpZCI6IuS4u-WcuuaZrzrwn5SlIiwiYXR0cmlidXRlcyI6WyLnp6_liIY68J-UpSIsIueUn-WRveWAvC_kuIrpmZAiXX0"},
		{Selector{Kind: "attribute", ID: "<>&\u2028\"\n"}, "eyJraW5kIjoiYXR0cmlidXRlIiwiaWQiOiI8PibigKhcIlxuIn0"},
	}
	for _, fixture := range fixtures {
		encoded, err := Encode(fixture.selector)
		if err != nil || encoded != fixture.encoded {
			t.Fatalf("Encode(%#v)=%q error=%v, want %q", fixture.selector, encoded, err, fixture.encoded)
		}
		decoded, err := Decode(fixture.encoded)
		if err != nil || decoded.Kind != fixture.selector.Kind || decoded.ID != fixture.selector.ID || strings.Join(decoded.Attributes, "\x00") != strings.Join(fixture.selector.Attributes, "\x00") {
			t.Fatalf("Decode(%q)=%#v error=%v", fixture.encoded, decoded, err)
		}
	}
	long := Selector{Kind: "attribute", ID: strings.Repeat("界:", 2_000)}
	encoded, err := Encode(long)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := Decode(encoded)
	if err != nil || decoded.ID != long.ID {
		t.Fatalf("long Decode() length=%d error=%v", len(decoded.ID), err)
	}
}

func TestCanonicalSelectorRejectsDuplicateMalformedAndOversizeValues(t *testing.T) {
	for _, selector := range []Selector{
		{Kind: "scene", ID: "main", Attributes: []string{"score", "score"}},
		{Kind: "scene", ID: "main"},
		{Kind: "attribute", ID: ""},
		{Kind: "unknown", ID: "score"},
	} {
		if _, err := Encode(selector); err == nil {
			t.Fatalf("Encode(%#v) accepted invalid selector", selector)
		}
	}
	if _, err := Encode(Selector{Kind: "attribute", ID: strings.Repeat("x", MaxEncodedLength)}); err == nil {
		t.Fatal("Encode() accepted an oversize canonical selector")
	}
	for _, encoded := range []string{
		"eyJpZCI6InNjb3JlIiwia2luZCI6ImF0dHJpYnV0ZSJ9",
		"eyJraW5kIjoiYXR0cmlidXRlIiwiaWQiOiJzY29yZSIsImV4dHJhIjp0cnVlfQ",
		"not+base64url",
		strings.Repeat("A", MaxEncodedLength+1),
	} {
		if _, err := Decode(encoded); err == nil {
			t.Fatalf("Decode(%q) accepted malformed selector", encoded[:min(len(encoded), 80)])
		}
	}
}
