package main

import "testing"

func TestParseRoomGiftEffectsMapsOnlyPlayableGiftResources(t *testing.T) {
	payload := map[string]any{
		"code": float64(0),
		"data": map[string]any{"full_sc_resource": map[string]any{"conf_list": []any{
			map[string]any{
				"id": float64(1846), "type": float64(1),
				"web_mp4": "https://i0.hdslb.com/full.mp4", "web_mp4_json": "https://i0.hdslb.com/full.json",
				"bind_gift_ids": []any{float64(34639), float64(34640)},
			},
			map[string]any{
				"id": float64(2), "type": float64(2),
				"web_mp4": "https://i0.hdslb.com/ignored.mp4", "web_mp4_json": "https://i0.hdslb.com/ignored.json",
				"bind_gift_ids": []any{float64(1)},
			},
		}}},
	}
	effects, err := parseRoomGiftEffects(payload)
	if err != nil {
		t.Fatal(err)
	}
	if len(effects) != 2 || effects[34639].ID != 1846 || effects[34639].MP4 == "" || effects[34639].MP4JSON == "" {
		t.Fatalf("effects = %#v", effects)
	}
}

func TestParseRoomGiftEffectCatalogIndexesGuardResourcesByEffectID(t *testing.T) {
	payload := map[string]any{
		"code": float64(0),
		"data": map[string]any{"full_sc_resource": map[string]any{"conf_list": []any{
			map[string]any{
				"id": float64(9001), "type": float64(2),
				"web_mp4": "https://i0.hdslb.com/guard.mp4", "web_mp4_json": "https://i0.hdslb.com/guard.json",
			},
		}}},
	}
	catalog, err := parseRoomGiftEffectCatalog(payload)
	if err != nil {
		t.Fatal(err)
	}
	if catalog.ByID[9001].MP4 == "" || len(catalog.ByGiftID) != 0 {
		t.Fatalf("effect catalog = %#v", catalog)
	}
}

func TestEnrichRoomGiftEffectsRequiresMatchingConfiguredEffect(t *testing.T) {
	gifts := []roomGiftInfo{
		{ID: 1, EffectID: 1846},
		{ID: 2, EffectID: 9999},
		{ID: 3},
	}
	effects := map[int]giftEffectResource{
		1: {ID: 1846, MP4: "one.mp4", MP4JSON: "one.json"},
		2: {ID: 1846, MP4: "wrong.mp4", MP4JSON: "wrong.json"},
		3: {ID: 1846, MP4: "three.mp4", MP4JSON: "three.json"},
	}
	enrichRoomGiftEffects(gifts, effects)
	if gifts[0].EffectMP4 != "one.mp4" || gifts[1].EffectMP4 != "" || gifts[2].EffectID != 1846 {
		t.Fatalf("gifts = %#v", gifts)
	}
}

func TestParseGiftEffectLayoutSanitizesPackedAlphaCoordinates(t *testing.T) {
	layout, err := parseGiftEffectLayout([]byte(`{"info":{"videoW":1088,"videoH":1280,"rgbFrame":[0,0,720,1280],"aFrame":[724,0,360,640],"fps":30,"f":390,"ignored":"value"}}`))
	if err != nil {
		t.Fatal(err)
	}
	if layout.VideoWidth != 1088 || layout.RGBFrame != [4]int{0, 0, 720, 1280} || layout.AlphaFrame != [4]int{724, 0, 360, 640} || layout.Frames != 390 {
		t.Fatalf("layout = %#v", layout)
	}
}

func TestParseGiftEffectLayoutRejectsOutOfBoundsCoordinates(t *testing.T) {
	_, err := parseGiftEffectLayout([]byte(`{"info":{"videoW":1088,"videoH":1280,"rgbFrame":[0,0,1200,1280],"aFrame":[724,0,360,640],"fps":30,"f":390}}`))
	if err == nil {
		t.Fatal("expected invalid packed-alpha coordinates to be rejected")
	}
}
