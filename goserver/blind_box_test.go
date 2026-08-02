package main

import "testing"

func TestParseBlindBoxResponse(t *testing.T) {
	info, requiresLogin, err := parseBlindBoxResponse(map[string]any{
		"code": float64(0),
		"data": map[string]any{
			"blind_gift_name": "测试盲盒",
			"gifts": []any{
				map[string]any{"gift_id": float64(101), "gift_name": "子礼物 A", "price": float64(1000), "gift_img": "a.png", "chance": "50%"},
				map[string]any{"gift_id": float64(101), "gift_name": "重复 A", "price": float64(1000)},
				map[string]any{"gift_id": float64(102), "gift_name": "子礼物 B", "price": float64(2000)},
			},
		},
	}, 999)
	if err != nil || requiresLogin || info == nil {
		t.Fatalf("blind box parse = info=%#v requiresLogin=%v err=%v", info, requiresLogin, err)
	}
	if info.GiftID != 999 || info.Name != "测试盲盒" || len(info.Gifts) != 2 {
		t.Fatalf("blind box info = %#v", info)
	}
	if info.Gifts[0].ID != 101 || info.Gifts[0].Chance != "50%" {
		t.Fatalf("first child gift = %#v", info.Gifts[0])
	}
}

func TestParseBlindBoxResponseReportsLoginRequirement(t *testing.T) {
	info, requiresLogin, err := parseBlindBoxResponse(map[string]any{
		"code":    float64(-101),
		"message": "账号未登录",
	}, 35800)
	if err != nil || info != nil || !requiresLogin {
		t.Fatalf("login requirement = info=%#v requiresLogin=%v err=%v", info, requiresLogin, err)
	}
}
