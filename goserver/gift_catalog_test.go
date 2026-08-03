package main

import "testing"

func TestBuildCurrentRoomGiftCatalogKeepsOnlyPanelVersions(t *testing.T) {
	configPayload := map[string]any{
		"code": float64(0),
		"data": map[string]any{"list": []any{
			map[string]any{"id": float64(31044), "name": "情书", "price": float64(5200), "coin_type": "gold", "img_basic": "old.png"},
			map[string]any{"id": float64(35545), "name": "情书", "price": float64(5200), "coin_type": "gold", "img_basic": "current.png"},
			map[string]any{"id": float64(35800), "name": "小熊虫盲盒", "price": float64(9000), "coin_type": "gold", "img_basic": "box.png"},
		}},
	}
	panelPayload := map[string]any{
		"code": float64(0),
		"data": map[string]any{
			"room_gift_list": map[string]any{
				"gold_list": []any{
					map[string]any{"gift_id": float64(35545)},
					map[string]any{"gift_id": float64(35545)},
				},
				"silver_list": []any{},
			},
			"tab_list": []any{
				map[string]any{"list": []any{map[string]any{"gift_id": float64(35800)}}},
			},
		},
	}

	gifts, err := buildCurrentRoomGiftCatalog(configPayload, panelPayload)
	if err != nil {
		t.Fatal(err)
	}
	if len(gifts) != 2 {
		t.Fatalf("current gifts = %#v", gifts)
	}
	if gifts[0].ID != 35545 || gifts[0].ImgBasic != "current.png" {
		t.Fatalf("current same-name version = %#v", gifts[0])
	}
	if gifts[1].ID != 35800 {
		t.Fatalf("tab gift = %#v", gifts[1])
	}
}

func TestParseRoomGiftContext(t *testing.T) {
	context, err := parseRoomGiftContext(map[string]any{
		"code": float64(0),
		"data": map[string]any{"room_id": float64(24849407), "area_id": float64(9), "parent_area_id": float64(2)},
	})
	if err != nil {
		t.Fatal(err)
	}
	if context.RoomID != 24849407 || context.AreaID != 9 || context.ParentAreaID != 2 {
		t.Fatalf("room gift context = %#v", context)
	}
}
