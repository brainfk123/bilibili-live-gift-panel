package main

import "testing"

func TestBuildCurrentRoomGiftCatalogMarksPanelVersionsAsListed(t *testing.T) {
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
	if len(gifts) != 3 {
		t.Fatalf("room gifts = %#v", gifts)
	}
	if gifts[0].ID != 35545 || gifts[0].ImgBasic != "current.png" || !gifts[0].Listed {
		t.Fatalf("current same-name version = %#v", gifts[0])
	}
	if gifts[1].ID != 35800 || !gifts[1].Listed {
		t.Fatalf("tab gift = %#v", gifts[1])
	}
	if gifts[2].ID != 31044 || gifts[2].Listed {
		t.Fatalf("unlisted same-name version = %#v", gifts[2])
	}
}

func TestMarkListedBlindBoxChildrenMarksRewardsAsListed(t *testing.T) {
	configPayload := map[string]any{
		"code": float64(0),
		"data": map[string]any{"list": []any{
			map[string]any{"id": float64(35800), "name": "小熊虫盲盒", "price": float64(9000), "coin_type": "gold", "img_basic": "box.png", "gift_type": float64(6)},
			map[string]any{"id": float64(35801), "name": "心事虫虫", "price": float64(9000), "coin_type": "gold", "img_basic": "child.png"},
			map[string]any{"id": float64(31044), "name": "旧礼物", "price": float64(100), "coin_type": "gold", "img_basic": "old.png"},
		}},
	}
	panelPayload := map[string]any{
		"code": float64(0),
		"data": map[string]any{
			"room_gift_list": map[string]any{
				"gold_list":   []any{map[string]any{"gift_id": float64(35800)}},
				"silver_list": []any{},
			},
			"tab_list": []any{},
		},
	}

	gifts, err := buildCurrentRoomGiftCatalog(configPayload, panelPayload)
	if err != nil {
		t.Fatal(err)
	}
	lookups := []int{}
	markListedBlindBoxChildren(gifts, func(giftID int) (*blindBoxInfo, bool, error) {
		lookups = append(lookups, giftID)
		return &blindBoxInfo{GiftID: giftID, Gifts: []blindBoxGift{{ID: 35801, Name: "心事虫虫"}}}, false, nil
	})

	if len(lookups) != 1 || lookups[0] != 35800 {
		t.Fatalf("blind box lookups = %#v", lookups)
	}
	if !gifts[0].Listed || !gifts[1].Listed {
		t.Fatalf("listed blind box catalog = %#v", gifts)
	}
	if gifts[2].Listed {
		t.Fatalf("unrelated gift should remain unlisted = %#v", gifts[2])
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
