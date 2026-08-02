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
			"uid": 123456789, "timestamp": 1700000000, "rnd": "gift-rnd", "face": "https://example.test/avatar.png",
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
	if gift.Avatar != "https://example.test/avatar.png" {
		t.Fatalf("avatar = %q", gift.Avatar)
	}
	if gift.UID != 123456789 {
		t.Fatalf("uid = %d", gift.UID)
	}
}

func TestParseBiliGiftUsesSenderUinfoWhenLegacyIdentityIsMasked(t *testing.T) {
	payload, _ := json.Marshal(map[string]any{
		"cmd": "SEND_GIFT",
		"data": map[string]any{
			"giftId": 33988, "giftName": "人气票", "num": 1,
			"uid": 0, "uname": "反***", "face": "https://example.test/masked-avatar.png",
			"timestamp": 1700000000, "rnd": "masked-gift-rnd",
			"sender_uinfo": map[string]any{
				"uid": 123456789,
				"base": map[string]any{
					"name": "完整昵称", "face": "https://example.test/full-avatar.png",
				},
			},
		},
	})
	gift, ok := parseBiliGift(payload)
	if !ok {
		t.Fatal("SEND_GIFT with sender_uinfo was not parsed")
	}
	if gift.UID != 123456789 {
		t.Fatalf("uid = %d, want sender_uinfo uid", gift.UID)
	}
	if gift.Uname != "完整昵称" {
		t.Fatalf("uname = %q, want sender_uinfo name", gift.Uname)
	}
	if gift.Avatar != "https://example.test/full-avatar.png" {
		t.Fatalf("avatar = %q, want sender_uinfo avatar", gift.Avatar)
	}
}

func TestBiliPacketRoundTrip(t *testing.T) {
	payload := encodeBiliPacket(biliOpMessage, []byte(`{"cmd":"SEND_GIFT"}`))
	packets := decodeBiliPackets(payload)
	if len(packets) != 1 || packets[0].operation != biliOpMessage || string(packets[0].body) != `{"cmd":"SEND_GIFT"}` {
		t.Fatalf("unexpected packets: %#v", packets)
	}
}

func TestBuildBiliAuthPayloadUsesOptionalLoginSession(t *testing.T) {
	payload := buildBiliAuthPayload(roomInfo{RoomID: 31567150, Buvid: "anonymous-buvid"}, biliSession{
		UID: 32249588, Buvid: "logged-in-buvid", CookieHeader: "SESSDATA=secret",
	})
	var auth map[string]any
	if err := json.Unmarshal(payload, &auth); err != nil {
		t.Fatal(err)
	}
	if auth["uid"] != float64(32249588) {
		t.Fatalf("uid = %#v", auth["uid"])
	}
	if auth["buvid"] != "logged-in-buvid" {
		t.Fatalf("buvid = %#v", auth["buvid"])
	}

	guestPayload := buildBiliAuthPayload(roomInfo{RoomID: 31567150, Buvid: "anonymous-buvid"}, biliSession{})
	if err := json.Unmarshal(guestPayload, &auth); err != nil {
		t.Fatal(err)
	}
	if auth["uid"] != float64(0) || auth["buvid"] != "anonymous-buvid" {
		t.Fatalf("guest auth = %#v", auth)
	}
}

func TestSessionForRoomInfoKeepsValidNonOwnerLogin(t *testing.T) {
	session := biliSession{UID: 32249588, CookieHeader: "SESSDATA=secret", Buvid: "login-buvid"}
	ownerRoom := roomInfo{RoomID: 31567150, AnchorUID: 32249588}
	if actual := sessionForRoomInfo(ownerRoom, session); actual.UID != session.UID {
		t.Fatalf("owner session = %#v", actual)
	}

	otherRoom := roomInfo{RoomID: 31567150, AnchorUID: 999}
	if actual := sessionForRoomInfo(otherRoom, session); actual.UID != session.UID || actual.CookieHeader != session.CookieHeader {
		t.Fatalf("valid non-owner login must remain authenticated: %#v", actual)
	}
}
