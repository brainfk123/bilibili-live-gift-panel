package main

import (
	"encoding/json"
	"testing"
)

func TestParseBiliGift(t *testing.T) {
	payload, _ := json.Marshal(map[string]any{
		"cmd": "SEND_GIFT",
		"data": map[string]any{
			"giftId": 33012, "giftName": "666", "num": 3, "price": 1000,
			"coin_type": "gold", "total_coin": 3000, "uname": "观众",
			"uid": 9, "timestamp": 1700000000, "rnd": "gift-rnd",
			"gift_info": map[string]any{"img_basic": "https://example.test/666.png"},
		},
	})
	gift, ok := parseBiliGift(payload)
	if !ok {
		t.Fatal("SEND_GIFT was not parsed")
	}
	if gift.GiftID != 33012 || gift.Num != 3 || gift.Price != 1000 || gift.Rnd != "gift-rnd" {
		t.Fatalf("unexpected gift: %#v", gift)
	}
}

func TestBiliPacketRoundTrip(t *testing.T) {
	payload := encodeBiliPacket(biliOpMessage, []byte(`{"cmd":"SEND_GIFT"}`))
	packets := decodeBiliPackets(payload)
	if len(packets) != 1 || packets[0].operation != biliOpMessage || string(packets[0].body) != `{"cmd":"SEND_GIFT"}` {
		t.Fatalf("unexpected packets: %#v", packets)
	}
}
