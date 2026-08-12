package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type fakeBiliSocket struct {
	reads       [][]byte
	readErr     error
	writes      [][]byte
	deadlines   []time.Time
	deadlineErr error
}

func (socket *fakeBiliSocket) ReadMessage() (int, []byte, error) {
	if len(socket.reads) > 0 {
		payload := socket.reads[0]
		socket.reads = socket.reads[1:]
		return 2, payload, nil
	}
	return 0, nil, socket.readErr
}
func (socket *fakeBiliSocket) WriteMessage(_ int, payload []byte) error {
	socket.writes = append(socket.writes, append([]byte(nil), payload...))
	return nil
}
func (socket *fakeBiliSocket) SetReadDeadline(deadline time.Time) error {
	socket.deadlines = append(socket.deadlines, deadline)
	return socket.deadlineErr
}
func (*fakeBiliSocket) Close() error { return nil }

type heartbeatFailingBiliSocket struct {
	closed chan struct{}
}

func (socket *heartbeatFailingBiliSocket) ReadMessage() (int, []byte, error) {
	<-socket.closed
	return 0, nil, errors.New("socket closed")
}
func (*heartbeatFailingBiliSocket) WriteMessage(_ int, payload []byte) error {
	if len(decodeBiliPackets(payload)) == 1 && decodeBiliPackets(payload)[0].operation == biliOpHeartbeat {
		return errors.New("heartbeat write failed")
	}
	return nil
}
func (*heartbeatFailingBiliSocket) SetReadDeadline(time.Time) error { return nil }
func (socket *heartbeatFailingBiliSocket) Close() error {
	select {
	case <-socket.closed:
	default:
		close(socket.closed)
	}
	return nil
}

func TestBilibiliSourceRefreshesReadDeadlineForValidFrames(t *testing.T) {
	authReply := encodeBiliPacket(biliOpAuthReply, []byte(`{"code":0}`))
	socket := &fakeBiliSocket{reads: [][]byte{authReply}, readErr: errors.New("read stopped")}
	source := &bilibiliGiftSource{heartbeatInterval: time.Hour, readTimeout: 5 * time.Millisecond}
	err := source.runSocket(context.Background(), socket, roomInfo{}, biliSession{}, nil, nil, runtimeCallbacks{onState: func(string) {}})
	if err == nil {
		t.Fatal("runSocket returned nil after read failure")
	}
	if len(socket.deadlines) < 2 {
		t.Fatalf("deadline refreshes = %d, want initial and valid-frame refresh", len(socket.deadlines))
	}
	if socket.deadlines[1].Sub(socket.deadlines[0]) > 50*time.Millisecond {
		t.Fatalf("deadline was not refreshed from the valid frame: %v", socket.deadlines)
	}
}

func TestBilibiliSourceReturnsHeartbeatFailureOnce(t *testing.T) {
	socket := &heartbeatFailingBiliSocket{closed: make(chan struct{})}
	source := &bilibiliGiftSource{heartbeatInterval: time.Millisecond, readTimeout: time.Hour}
	err := source.runSocket(context.Background(), socket, roomInfo{}, biliSession{}, nil, nil, runtimeCallbacks{})
	if connectionFailureKind(err) != "heartbeat" {
		t.Fatalf("failure kind = %q, want heartbeat (error %v)", connectionFailureKind(err), err)
	}
}

func TestBilibiliSourceReturnsReadDeadlineFailure(t *testing.T) {
	socket := &fakeBiliSocket{deadlineErr: errors.New("deadline failed")}
	err := (&bilibiliGiftSource{}).runSocket(context.Background(), socket, roomInfo{}, biliSession{}, nil, nil, runtimeCallbacks{})
	if connectionFailureKind(err) != "deadline" {
		t.Fatalf("failure kind = %q, want deadline (error %v)", connectionFailureKind(err), err)
	}
}

func TestBilibiliDiagnosticsRecordsSafeParseOutcomes(t *testing.T) {
	logger, err := newDiagnosticLogger(filepath.Join(t.TempDir(), "runtime.log"))
	if err != nil {
		t.Fatal(err)
	}

	malformedPacket := make([]byte, biliHeaderLength)
	malformedPacket[3] = 32 // Declares a packet beyond the supplied frame.
	malformedPacket[5] = biliHeaderLength
	malformedPacket[11] = biliOpMessage
	invalidZlib := encodeBiliPacket(biliOpMessage, []byte("not-zlib"))
	invalidZlib[7] = 2
	malformedJSON := encodeBiliPacket(biliOpMessage, []byte(`{"cmd":`))
	ignoredCombo := encodeBiliPacket(biliOpMessage, []byte(`{"cmd":"COMBO_SEND","data":{"uid":123456,"uname":"private-viewer"}}`))
	validGift := encodeBiliPacket(biliOpMessage, []byte(`{"cmd":"SEND_GIFT","data":{"giftId":35801,"num":2,"blind_gift_id":35800,"timestamp":1700000000,"rnd":"diagnostic-rnd-token","uid":987654,"uname":"private-viewer","face":"https://private.example/avatar.png"}}`))
	socket := &fakeBiliSocket{reads: [][]byte{malformedPacket, invalidZlib, malformedJSON, ignoredCombo, validGift}, readErr: errors.New("stop")}
	var gifts []giftEvent

	err = (&bilibiliGiftSource{diagnostics: logger, heartbeatInterval: time.Hour}).runSocket(context.Background(), socket, roomInfo{}, biliSession{}, nil, nil, runtimeCallbacks{
		onGift: func(gift giftEvent) { gifts = append(gifts, gift) },
	})
	if err == nil {
		t.Fatal("runSocket returned nil after the fixture read ended")
	}
	if len(gifts) != 1 || gifts[0].GiftID != 35801 || gifts[0].Num != 2 || gifts[0].BlindGiftID != 35800 {
		t.Fatalf("accepted gifts = %#v", gifts)
	}

	data, readErr := os.ReadFile(logger.path)
	if readErr != nil {
		t.Fatal(readErr)
	}
	text := string(data)
	for _, expected := range []string{"packet_bounds", "decompression_failure", "malformed_envelope", "ignored_command"} {
		if !strings.Contains(text, expected) {
			t.Fatalf("diagnostics missing %q: %s", expected, text)
		}
	}
	for _, secret := range []string{"diagnostic-rnd-token", "private-viewer", "private.example/avatar.png", `{"cmd":`} {
		if strings.Contains(text, secret) {
			t.Fatalf("diagnostics leaked %q: %s", secret, text)
		}
	}
}

func TestDiagnosticHashIsShortAndStable(t *testing.T) {
	if got, want := diagnosticHash("abc"), "ba7816bf8f01"; got != want {
		t.Fatalf("diagnosticHash(abc) = %q, want %q", got, want)
	}
}

func TestParseBiliGift(t *testing.T) {
	payload, _ := json.Marshal(map[string]any{
		"cmd": "SEND_GIFT",
		"data": map[string]any{
			"giftId": 33012, "giftName": "666", "num": 3, "price": 1000,
			"coin_type": "gold", "total_coin": 3000, "uname": "观众",
			"uid": 123456789, "timestamp": 1700000000, "rnd": "gift-rnd", "face": "https://example.test/avatar.png",
			"gift_info": map[string]any{
				"img_basic": "https://example.test/666.png",
				"gif":       "https://i0.hdslb.com/666.gif", "webp": "https://i0.hdslb.com/666.webp", "effect_id": 1846,
			},
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
	if gift.AnimationGIF != "https://i0.hdslb.com/666.gif" || gift.AnimationWebP != "https://i0.hdslb.com/666.webp" || gift.EffectID != 1846 {
		t.Fatalf("animation = %#v", gift)
	}
}

func TestEnrichGiftAnimationFromRoomCatalogFillsMissingMetadata(t *testing.T) {
	gift := enrichGiftAnimationFromRoomCatalog(giftEvent{GiftID: 1, AnimationGIF: "event.gif"}, map[int]roomGiftInfo{
		1: {
			ID: 1, ImgBasic: "gift.png", AnimationGIF: "catalog.gif",
			AnimationWebP: "catalog.webp", AnimationDurationMS: 4200, EffectID: 1846,
			EffectMP4: "effect.mp4", EffectMP4JSON: "effect.json",
		},
	})
	if gift.ImgBasic != "gift.png" || gift.AnimationGIF != "event.gif" || gift.AnimationWebP != "catalog.webp" || gift.AnimationDurationMS != 4200 || gift.EffectMP4 != "effect.mp4" || gift.EffectMP4JSON != "effect.json" {
		t.Fatalf("enriched gift = %#v", gift)
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

func TestParseBiliGiftExtractsFanAndGuardMembership(t *testing.T) {
	payload, _ := json.Marshal(map[string]any{
		"cmd": "SEND_GIFT",
		"data": map[string]any{
			"giftId": 1, "giftName": "礼物", "num": 1, "uid": 1,
			"guard_level": 2,
			"medal_info":  map[string]any{"medal_name": "测试粉丝团", "medal_level": 12},
		},
	})
	gift, ok := parseBiliGift(payload)
	if !ok || gift.Membership != "admiral" {
		t.Fatalf("membership = %#v, ok=%v", gift, ok)
	}

	fanPayload, _ := json.Marshal(map[string]any{
		"cmd": "SEND_GIFT",
		"data": map[string]any{
			"giftId": 1, "giftName": "礼物", "num": 1, "uid": 1,
			"medal_info": map[string]any{"medal_name": "测试粉丝团", "medal_level": 1},
		},
	})
	fanGift, ok := parseBiliGift(fanPayload)
	if !ok || fanGift.Membership != "fan" {
		t.Fatalf("fan membership = %#v, ok=%v", fanGift, ok)
	}
}

func TestParseBiliGiftExtractsBlindBoxParent(t *testing.T) {
	payload, _ := json.Marshal(map[string]any{
		"cmd": "SEND_GIFT",
		"data": map[string]any{
			"giftId": 35801, "giftName": "心事虫虫", "num": 1, "price": 9000,
			"blind_gift": map[string]any{
				"blind_gift_id": 35800, "gift_name": "小熊虫盲盒", "original_gift_price": 6000,
			},
			"uid": 1, "timestamp": 1700000000, "rnd": "blind-rnd",
		},
	})

	gift, ok := parseBiliGift(payload)
	if !ok {
		t.Fatal("blind SEND_GIFT was not parsed")
	}
	if gift.GiftID != 35801 || gift.BlindGiftID != 35800 || gift.BlindGiftName != "小熊虫盲盒" || gift.BlindGiftPrice != 6000 {
		t.Fatalf("blind gift identity = %#v", gift)
	}
}

func TestParseBiliPaidEventParsesGuardBuy(t *testing.T) {
	payload, _ := json.Marshal(map[string]any{
		"cmd": "GUARD_BUY",
		"data": map[string]any{
			"uid": 123456789, "username": "大航海观众", "guard_level": 3,
			"num": 2, "price": 198000, "start_time": 1700000100, "order_id": "guard-order-1",
			"sender_uinfo": map[string]any{
				"uid":  123456789,
				"base": map[string]any{"name": "完整大航海观众", "face": "https://example.test/guard.png"},
			},
		},
	})
	gift, ok := parseBiliPaidEvent(payload)
	if !ok {
		t.Fatal("GUARD_BUY was not parsed")
	}
	if gift.GiftID != specialGiftGuardCaptain || gift.GiftName != "大航海·舰长" || gift.Num != 2 {
		t.Fatalf("guard identity = %#v", gift)
	}
	if gift.Price != 198000 || gift.TotalCoin != 396000 || gift.CoinType != "gold" {
		t.Fatalf("guard price = %#v", gift)
	}
	if gift.UID != 123456789 || gift.Uname != "大航海观众" || gift.Avatar != "https://example.test/guard.png" {
		t.Fatalf("guard sender = %#v", gift)
	}
	if gift.Rnd != "guard:guard-order-1" || gift.Timestamp != 1700000100 || gift.ImgBasic == "" {
		t.Fatalf("guard metadata = %#v", gift)
	}
	if gift.Membership != "captain" {
		t.Fatalf("guard membership = %q", gift.Membership)
	}
}

func TestParseBiliPaidEventParsesGuardToastAnimation(t *testing.T) {
	payload, _ := json.Marshal(map[string]any{
		"cmd": "USER_TOAST_MSG_V2",
		"data": map[string]any{
			"timestamp": 1700000101,
			"sender_uinfo": map[string]any{
				"uid":  123456789,
				"base": map[string]any{"name": "大航海观众", "face": "https://example.test/guard.png"},
			},
			"guard_info":  map[string]any{"guard_level": 3},
			"effect_info": map[string]any{"room_effect_id": 9001},
		},
	})
	gift, ok := parseBiliPaidEvent(payload)
	if !ok || !gift.AnimationOnly || gift.GiftID != specialGiftGuardCaptain || gift.EffectID != 9001 || gift.UID != 123456789 {
		t.Fatalf("guard toast = %#v, ok=%v", gift, ok)
	}
}

func TestParseBiliPaidEventMapsEveryGuardLevel(t *testing.T) {
	wants := map[int]int{
		3: specialGiftGuardCaptain,
		2: specialGiftGuardAdmiral,
		1: specialGiftGuardGovernor,
	}
	for level, wantID := range wants {
		payload, _ := json.Marshal(map[string]any{
			"cmd":  "GUARD_BUY",
			"data": map[string]any{"guard_level": level, "price": 1, "uid": 1},
		})
		gift, ok := parseBiliPaidEvent(payload)
		if !ok || gift.GiftID != wantID {
			t.Fatalf("guard level %d = %#v, ok=%v", level, gift, ok)
		}
	}
	payload := []byte(`{"cmd":"GUARD_BUY","data":{"guard_level":0,"price":1}}`)
	if _, ok := parseBiliPaidEvent(payload); ok {
		t.Fatal("unknown guard level was accepted")
	}
}

func TestParseBiliPaidEventParsesSuperChatInGoldSeedUnits(t *testing.T) {
	payload, _ := json.Marshal(map[string]any{
		"cmd": "SUPER_CHAT_MESSAGE",
		"data": map[string]any{
			"id": 987654321, "uid": "123456789", "price": 50, "ts": 1700000200,
			"message":   "测试醒目留言",
			"user_info": map[string]any{"uname": "醒目留言观众", "face": "https://example.test/sc.png"},
		},
	})
	gift, ok := parseBiliPaidEvent(payload)
	if !ok {
		t.Fatal("SUPER_CHAT_MESSAGE was not parsed")
	}
	if gift.GiftID != specialGiftSuperChat || gift.GiftName != "Super Chat" || gift.Num != 1 {
		t.Fatalf("super chat identity = %#v", gift)
	}
	if gift.Price != 50000 || gift.TotalCoin != 50000 || gift.CoinType != "gold" {
		t.Fatalf("super chat unit conversion = %#v", gift)
	}
	if gift.UID != 123456789 || gift.Uname != "醒目留言观众" || gift.Avatar != "https://example.test/sc.png" {
		t.Fatalf("super chat sender = %#v", gift)
	}
	if gift.Rnd != "super-chat:987654321" || gift.Timestamp != 1700000200 || gift.ImgBasic == "" {
		t.Fatalf("super chat metadata = %#v", gift)
	}
	if gift.Message != "测试醒目留言" {
		t.Fatalf("super chat message = %q", gift.Message)
	}
}

func TestParseBiliPaidEventDeduplicatesSuperChatLanguageVariants(t *testing.T) {
	base := map[string]any{"id": "sc-duplicate-1", "uid": 1, "price": 30, "ts": 1700000300}
	normal, _ := json.Marshal(map[string]any{"cmd": "SUPER_CHAT_MESSAGE", "data": base})
	japanese, _ := json.Marshal(map[string]any{"cmd": "SUPER_CHAT_MESSAGE_JPN:1", "data": base})
	left, leftOK := parseBiliPaidEvent(normal)
	right, rightOK := parseBiliPaidEvent(japanese)
	if !leftOK || !rightOK || left.Rnd == "" || left.Rnd != right.Rnd {
		t.Fatalf("language variants = %#v / %#v", left, right)
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
