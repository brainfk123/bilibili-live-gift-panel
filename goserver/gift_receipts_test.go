package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

type giftMediaClientFunc func(*http.Request) (*http.Response, error)

func (function giftMediaClientFunc) Do(request *http.Request) (*http.Response, error) {
	return function(request)
}

func TestApplyGiftEventRecordsUnmatchedGiftWithAnimation(t *testing.T) {
	state := defaultAppState()
	state.RoomID = "100"
	applyGiftEvent(&state, giftEvent{
		GiftID: 1, GiftName: "心动盲盒", Num: 2, Price: 100, TotalCoin: 200, CoinType: "gold",
		Uname: "观众", UID: 42, Avatar: "https://i0.hdslb.com/avatar.png", ImgBasic: "gift.png",
		AnimationGIF: "https://i0.hdslb.com/gift.gif", AnimationWebP: "https://i0.hdslb.com/gift.webp",
		AnimationDurationMS: 2500, Timestamp: 1700000000, Rnd: "receipt-1",
		EffectID: 1846, EffectMP4: "https://i0.hdslb.com/full.mp4", EffectMP4JSON: "https://i0.hdslb.com/full.json",
	})

	if len(state.Log) != 0 {
		t.Fatalf("unmatched gift should not create rule log: %#v", state.Log)
	}
	if len(state.GiftReceipts) != 1 {
		t.Fatalf("gift receipts = %#v", state.GiftReceipts)
	}
	receipt := state.GiftReceipts[0]
	if receipt.Num != 2 || receipt.TotalCoin != 200 || len(receipt.Effects) != 0 {
		t.Fatalf("unmatched receipt = %#v", receipt)
	}
	if receipt.Animation == nil || receipt.Animation.GIF == "" || receipt.Animation.WebP == "" || receipt.Animation.DurationMS != 2500 || receipt.Animation.EffectID != 1846 || receipt.Animation.MP4 == "" || receipt.Animation.MP4JSON == "" {
		t.Fatalf("animation metadata = %#v", receipt.Animation)
	}
}

func TestGiftReceiptRejectsIconOnlyAnimation(t *testing.T) {
	state := defaultAppState()
	state.RoomID = "100"
	applyGiftEvent(&state, giftEvent{
		GiftID: 31164, GiftName: "粉丝团灯牌", Num: 1,
		AnimationGIF: "https://i0.hdslb.com/icon.gif", AnimationWebP: "https://i0.hdslb.com/icon.webp",
		AnimationDurationMS: 3000, Timestamp: 1700000000, Rnd: "icon-only",
	})
	if len(state.GiftReceipts) != 1 || state.GiftReceipts[0].Animation != nil {
		t.Fatalf("icon-only gift must not expose replay animation: %#v", state.GiftReceipts)
	}
}

func TestGiftReceiptPreservesMembershipAndSuperChatMessage(t *testing.T) {
	state := defaultAppState()
	state.RoomID = "100"
	applyGiftEvent(&state, giftEvent{
		GiftID: specialGiftSuperChat, GiftName: "Super Chat", Num: 1,
		Membership: "captain", Message: "今天也要加油", Timestamp: 1700000000, Rnd: "sc-message",
	})
	if len(state.GiftReceipts) != 1 || state.GiftReceipts[0].Membership != "captain" || state.GiftReceipts[0].Message != "今天也要加油" {
		t.Fatalf("receipt identity/message = %#v", state.GiftReceipts)
	}
}

func TestAttachGiftAnimationToMatchingGuardReceipt(t *testing.T) {
	state := defaultAppState()
	state.RoomID = "100"
	applyGiftEvent(&state, giftEvent{
		GiftID: specialGiftGuardCaptain, GiftName: "大航海·舰长", Num: 1,
		UID: 42, Timestamp: 1700000000, Rnd: "guard-order",
	})
	attached := attachGiftAnimationToReceipt(&state, giftEvent{
		GiftID: specialGiftGuardCaptain, UID: 42, Timestamp: 1700000001,
		Membership: "captain", EffectID: 9001,
		EffectMP4: "https://i0.hdslb.com/guard.mp4", EffectMP4JSON: "https://i0.hdslb.com/guard.json",
	})
	if !attached || state.GiftReceipts[0].Animation == nil || state.GiftReceipts[0].Animation.EffectID != 9001 {
		t.Fatalf("guard animation was not attached: %#v", state.GiftReceipts)
	}
}

func TestGiftReceiptCollectsMultipleRuleResultsAndDeduplicates(t *testing.T) {
	state := defaultAppState()
	state.RoomID = "100"
	state.Attributes = []attributeState{
		{Name: "加班", Value: 0, Format: "number"},
		{Name: "爱心", Value: 0, Format: "number"},
	}
	state.Rules = []giftRule{
		{ID: "rule-a", GiftID: 1, AttributeName: "加班", Formula: "加班+1"},
		{ID: "rule-b", GiftID: 1, AttributeName: "爱心", Formula: "爱心+2"},
	}
	event := giftEvent{GiftID: 1, GiftName: "礼物", Num: 1, Timestamp: 1700000000, Rnd: "same-event"}
	applyGiftEvent(&state, event)
	applyGiftEvent(&state, event)

	if len(state.GiftReceipts) != 1 {
		t.Fatalf("deduplicated receipts = %#v", state.GiftReceipts)
	}
	if len(state.GiftReceipts[0].Effects) != 2 {
		t.Fatalf("receipt effects = %#v", state.GiftReceipts[0].Effects)
	}
}

func TestGiftReceiptsKeepNewestTwoHundred(t *testing.T) {
	state := defaultAppState()
	state.RoomID = "100"
	for index := 0; index < maxGiftReceiptEntries+5; index++ {
		applyGiftEvent(&state, giftEvent{
			GiftID: index + 1, GiftName: "礼物", Num: 1, Timestamp: int64(1700000000 + index), Rnd: "event-" + strconv.Itoa(index),
		})
	}
	if len(state.GiftReceipts) != maxGiftReceiptEntries {
		t.Fatalf("receipt count = %d", len(state.GiftReceipts))
	}
	if state.GiftReceipts[0].GiftID != maxGiftReceiptEntries+5 || state.GiftReceipts[len(state.GiftReceipts)-1].GiftID != 6 {
		t.Fatalf("receipt bounds = first %d last %d", state.GiftReceipts[0].GiftID, state.GiftReceipts[len(state.GiftReceipts)-1].GiftID)
	}
}

func TestLegacyRuleLogsMigrateIntoOneGiftReceipt(t *testing.T) {
	entries := []logEntry{
		{Time: 10, GiftID: 1, GiftName: "礼物", Num: 1, Uname: "观众", AttributeName: "A", Delta: 1, ValueAfter: 1, RuleID: "a", EventID: "rnd:A"},
		{Time: 10, GiftID: 1, GiftName: "礼物", Num: 1, Uname: "观众", AttributeName: "B", Delta: 2, ValueAfter: 2, RuleID: "b", EventID: "rnd:B"},
	}
	receipts := migrateGiftReceiptsFromLog(entries)
	if len(receipts) != 1 || len(receipts[0].Effects) != 2 {
		t.Fatalf("migrated receipts = %#v", receipts)
	}
}

func TestStateShardVersionSixMigratesGiftReceipts(t *testing.T) {
	directory := t.TempDir()
	store := &configStore{path: filepath.Join(directory, "config.json")}
	if err := os.WriteFile(store.path, []byte(`{"schemaVersion":6,"roomId":"100","attributes":[],"rules":[]}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(store.historyPath(), []byte(`{"schemaVersion":6,"stats":{},"contributions":{"viewers":[]},"giftTargetProgress":{}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	entry := logEntry{Time: 10, GiftID: 1, GiftName: "礼物", Num: 1, Uname: "观众", AttributeName: "A", Delta: 1, ValueAfter: 1, RuleID: "a"}
	encoded, _ := json.Marshal(entry)
	if err := os.WriteFile(store.eventLogPath(), append(encoded, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	state, err := store.readState()
	if err != nil {
		t.Fatal(err)
	}
	if len(state.GiftReceipts) != 1 || state.GiftReceipts[0].GiftID != 1 {
		t.Fatalf("migrated shard receipts = %#v", state.GiftReceipts)
	}
}

func TestRoomSwitchClearsGiftReceiptsAndSameRoomPutPreservesThem(t *testing.T) {
	store := &configStore{path: filepath.Join(t.TempDir(), "config.json")}
	initial := defaultAppState()
	initial.RoomID = "100"
	initial.GiftReceipts = []giftReceipt{{ID: "one", GiftID: 1, Effects: []giftReceiptEffect{}}}
	if err := store.replaceState(initial); err != nil {
		t.Fatal(err)
	}
	sameRoom := defaultAppState()
	sameRoom.RoomID = "100"
	replaced, err := store.replaceClientState(sameRoom)
	if err != nil || len(replaced.State.GiftReceipts) != 1 {
		t.Fatalf("same-room receipts = %#v, err = %v", replaced.State.GiftReceipts, err)
	}
	otherRoom := defaultAppState()
	otherRoom.RoomID = "200"
	replaced, err = store.replaceClientState(otherRoom)
	if err != nil || len(replaced.State.GiftReceipts) != 0 {
		t.Fatalf("switched-room receipts = %#v, err = %v", replaced.State.GiftReceipts, err)
	}
}

func TestGiftReceiptMediaFallsBackFromGIFToWebP(t *testing.T) {
	store, receipt := giftReceiptMediaTestStore(t)
	calls := 0
	client := giftMediaClientFunc(func(request *http.Request) (*http.Response, error) {
		calls++
		if strings.HasSuffix(request.URL.Path, ".gif") {
			return mediaResponse(request, http.StatusNotFound, "text/plain", "missing"), nil
		}
		return mediaResponse(request, http.StatusOK, "image/webp", "webp-data"), nil
	})
	api := newGiftReceiptAPI(store, client)
	response := httptest.NewRecorder()
	api.handleMedia(response, httptest.NewRequest(http.MethodGet, "/api/gift-receipts/media?id="+url.QueryEscape(receipt.ID)+"&kind=animation", nil))
	if response.Code != http.StatusOK || response.Body.String() != "webp-data" || calls != 2 {
		t.Fatalf("fallback status=%d body=%q calls=%d", response.Code, response.Body.String(), calls)
	}
}

func TestGiftReceiptMediaServesVideoAndSanitizedEffectLayout(t *testing.T) {
	store, receipt := giftReceiptMediaTestStore(t)
	client := giftMediaClientFunc(func(request *http.Request) (*http.Response, error) {
		if strings.HasSuffix(request.URL.Path, ".mp4") {
			return mediaResponse(request, http.StatusOK, "video/mp4", "packed-video"), nil
		}
		return mediaResponse(request, http.StatusOK, "application/json", `{"info":{"videoW":1088,"videoH":1280,"rgbFrame":[0,0,720,1280],"aFrame":[724,0,360,640],"fps":30,"f":390,"private":"discarded"}}`), nil
	})
	api := newGiftReceiptAPI(store, client)
	videoResponse := httptest.NewRecorder()
	api.handleMedia(videoResponse, httptest.NewRequest(http.MethodGet, "/api/gift-receipts/media?id="+receipt.ID+"&kind=effect-video", nil))
	if videoResponse.Code != http.StatusOK || videoResponse.Header().Get("Content-Type") != "video/mp4" || videoResponse.Body.String() != "packed-video" {
		t.Fatalf("video status=%d type=%q body=%q", videoResponse.Code, videoResponse.Header().Get("Content-Type"), videoResponse.Body.String())
	}
	layoutResponse := httptest.NewRecorder()
	api.handleMedia(layoutResponse, httptest.NewRequest(http.MethodGet, "/api/gift-receipts/media?id="+receipt.ID+"&kind=effect-layout", nil))
	if layoutResponse.Code != http.StatusOK || layoutResponse.Header().Get("Content-Type") != "application/json" || strings.Contains(layoutResponse.Body.String(), "private") {
		t.Fatalf("layout status=%d type=%q body=%q", layoutResponse.Code, layoutResponse.Header().Get("Content-Type"), layoutResponse.Body.String())
	}
	var layout giftEffectLayout
	if err := json.Unmarshal(layoutResponse.Body.Bytes(), &layout); err != nil || layout.Frames != 390 || layout.AlphaFrame[0] != 724 {
		t.Fatalf("layout = %#v err=%v", layout, err)
	}
}

func TestGiftReceiptMediaAcceptsVerifiedBilibiliMIMEAliases(t *testing.T) {
	store, receipt := giftReceiptMediaTestStore(t)
	mp4 := "\x00\x00\x00\x18ftypisom\x00\x00\x02\x00isomiso2"
	client := giftMediaClientFunc(func(request *http.Request) (*http.Response, error) {
		if strings.HasSuffix(request.URL.Path, ".mp4") {
			return mediaResponse(request, http.StatusOK, "audio/mp4", mp4), nil
		}
		return mediaResponse(request, http.StatusOK, "json", `{"info":{"videoW":1088,"videoH":1280,"rgbFrame":[0,0,720,1280],"aFrame":[724,0,360,640],"fps":30,"f":75}}`), nil
	})
	api := newGiftReceiptAPI(store, client)

	videoResponse := httptest.NewRecorder()
	api.handleMedia(videoResponse, httptest.NewRequest(http.MethodGet, "/api/gift-receipts/media?id="+receipt.ID+"&kind=effect-video", nil))
	if videoResponse.Code != http.StatusOK || videoResponse.Header().Get("Content-Type") != "video/mp4" || videoResponse.Body.String() != mp4 {
		t.Fatalf("video status=%d type=%q body=%q", videoResponse.Code, videoResponse.Header().Get("Content-Type"), videoResponse.Body.String())
	}

	layoutResponse := httptest.NewRecorder()
	api.handleMedia(layoutResponse, httptest.NewRequest(http.MethodGet, "/api/gift-receipts/media?id="+receipt.ID+"&kind=effect-layout", nil))
	if layoutResponse.Code != http.StatusOK || layoutResponse.Header().Get("Content-Type") != "application/json" {
		t.Fatalf("layout status=%d type=%q body=%q", layoutResponse.Code, layoutResponse.Header().Get("Content-Type"), layoutResponse.Body.String())
	}
}

func TestGiftReceiptMediaRejectsForgedBilibiliMIMEAliases(t *testing.T) {
	tests := []struct {
		name        string
		contentType string
		body        string
		kind        string
	}{
		{name: "audio mp4 without ftyp", contentType: "audio/mp4", body: "not-an-mp4", kind: "effect-video"},
		{name: "json alias without json", contentType: "json", body: "not-json", kind: "effect-layout"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store, receipt := giftReceiptMediaTestStore(t)
			client := giftMediaClientFunc(func(request *http.Request) (*http.Response, error) {
				return mediaResponse(request, http.StatusOK, test.contentType, test.body), nil
			})
			api := newGiftReceiptAPI(store, client)
			response := httptest.NewRecorder()
			api.handleMedia(response, httptest.NewRequest(http.MethodGet, "/api/gift-receipts/media?id="+receipt.ID+"&kind="+test.kind, nil))
			if response.Code != http.StatusBadGateway {
				t.Fatalf("status=%d type=%q body=%q", response.Code, response.Header().Get("Content-Type"), response.Body.String())
			}
		})
	}
}

func TestGiftReceiptMediaRejectsInvalidEffectLayout(t *testing.T) {
	store, receipt := giftReceiptMediaTestStore(t)
	client := giftMediaClientFunc(func(request *http.Request) (*http.Response, error) {
		return mediaResponse(request, http.StatusOK, "application/json", `{"info":{"videoW":1088,"videoH":1280,"rgbFrame":[0,0,1200,1280],"aFrame":[724,0,360,640],"fps":30,"f":390}}`), nil
	})
	api := newGiftReceiptAPI(store, client)
	response := httptest.NewRecorder()
	api.handleMedia(response, httptest.NewRequest(http.MethodGet, "/api/gift-receipts/media?id="+receipt.ID+"&kind=effect-layout", nil))
	if response.Code != http.StatusBadGateway {
		t.Fatalf("status=%d body=%q", response.Code, response.Body.String())
	}
}

func TestGiftReceiptMediaRejectsUnsafeResponses(t *testing.T) {
	tests := []struct {
		name       string
		animation  string
		client     giftMediaClientFunc
		wantCalled bool
	}{
		{name: "http", animation: "http://i0.hdslb.com/gift.gif", client: successfulGIFClient(), wantCalled: false},
		{name: "foreign host", animation: "https://example.com/gift.gif", client: successfulGIFClient(), wantCalled: false},
		{name: "foreign redirect", animation: "https://i0.hdslb.com/gift.gif", client: func(request *http.Request) (*http.Response, error) {
			response := mediaResponse(request, http.StatusOK, "image/gif", "gif")
			response.Request.URL, _ = url.Parse("https://example.com/redirect.gif")
			return response, nil
		}, wantCalled: true},
		{name: "wrong mime", animation: "https://i0.hdslb.com/gift.gif", client: func(request *http.Request) (*http.Response, error) {
			return mediaResponse(request, http.StatusOK, "text/html", "no"), nil
		}, wantCalled: true},
		{name: "oversized", animation: "https://i0.hdslb.com/gift.gif", client: func(request *http.Request) (*http.Response, error) {
			response := mediaResponse(request, http.StatusOK, "image/gif", "")
			response.ContentLength = maxGiftAnimationBytes + 1
			return response, nil
		}, wantCalled: true},
		{name: "cancelled", animation: "https://i0.hdslb.com/gift.gif", client: func(request *http.Request) (*http.Response, error) {
			<-request.Context().Done()
			return nil, request.Context().Err()
		}, wantCalled: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store, receipt := giftReceiptMediaTestStore(t)
			receipt.Animation.GIF = test.animation
			receipt.Animation.WebP = ""
			state, _ := store.readState()
			state.GiftReceipts[0] = receipt
			if err := store.replaceState(state); err != nil {
				t.Fatal(err)
			}
			called := false
			client := giftMediaClientFunc(func(request *http.Request) (*http.Response, error) {
				called = true
				return test.client.Do(request)
			})
			api := newGiftReceiptAPI(store, client)
			request := httptest.NewRequest(http.MethodGet, "/api/gift-receipts/media?id="+receipt.ID+"&kind=animation", nil)
			if test.name == "cancelled" {
				ctx, cancel := context.WithCancel(request.Context())
				cancel()
				request = request.WithContext(ctx)
			}
			response := httptest.NewRecorder()
			api.handleMedia(response, request)
			if response.Code != http.StatusBadGateway || called != test.wantCalled {
				t.Fatalf("status=%d called=%v body=%q", response.Code, called, response.Body.String())
			}
		})
	}
}

func TestGiftReceiptClearRequiresSameOrigin(t *testing.T) {
	store, _ := giftReceiptMediaTestStore(t)
	api := newGiftReceiptAPI(store, successfulGIFClient())
	crossSite := httptest.NewRequest(http.MethodDelete, "/api/gift-receipts", nil)
	crossSite.Header.Set("Sec-Fetch-Site", "cross-site")
	rejected := httptest.NewRecorder()
	api.handleReceipts(rejected, crossSite)
	if rejected.Code != http.StatusForbidden {
		t.Fatalf("cross-site status = %d", rejected.Code)
	}
	allowed := httptest.NewRecorder()
	api.handleReceipts(allowed, httptest.NewRequest(http.MethodDelete, "/api/gift-receipts", nil))
	if allowed.Code != http.StatusOK {
		t.Fatalf("clear status = %d body=%s", allowed.Code, allowed.Body.String())
	}
	state, _ := store.readState()
	if len(state.GiftReceipts) != 0 {
		t.Fatalf("receipts after clear = %#v", state.GiftReceipts)
	}
}

func giftReceiptMediaTestStore(t *testing.T) (*configStore, giftReceipt) {
	t.Helper()
	store := &configStore{path: filepath.Join(t.TempDir(), "config.json")}
	receipt := giftReceipt{
		ID: "receipt", GiftID: 1, GiftName: "礼物", Effects: []giftReceiptEffect{},
		Avatar: "https://i0.hdslb.com/avatar.png",
		Animation: &giftReceiptAnimation{
			GIF: "https://i0.hdslb.com/gift.gif", WebP: "https://i0.hdslb.com/gift.webp", DurationMS: 3000,
			EffectID: 1846, MP4: "https://i0.hdslb.com/full.mp4", MP4JSON: "https://i0.hdslb.com/full.json",
		},
	}
	state := defaultAppState()
	state.GiftReceipts = []giftReceipt{receipt}
	if err := store.replaceState(state); err != nil {
		t.Fatal(err)
	}
	return store, receipt
}

func successfulGIFClient() giftMediaClientFunc {
	return func(request *http.Request) (*http.Response, error) {
		return mediaResponse(request, http.StatusOK, "image/gif", "gif"), nil
	}
}

func mediaResponse(request *http.Request, status int, contentType, body string) *http.Response {
	return &http.Response{
		StatusCode:    status,
		Header:        http.Header{"Content-Type": []string{contentType}},
		Body:          io.NopCloser(strings.NewReader(body)),
		ContentLength: int64(len(body)),
		Request:       request,
	}
}
