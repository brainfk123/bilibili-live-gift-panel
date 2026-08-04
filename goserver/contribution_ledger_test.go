package main

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

func TestContributionLedgerAggregatesAllGiftsAndEffectiveRules(t *testing.T) {
	state := defaultAppState()
	state.Attributes = []attributeState{{Name: "加班时间", Value: 0, Unit: "seconds", Format: "hhmmss"}}
	state.Rules = []giftRule{{ID: "add-time", GiftID: 33300, AttributeName: "加班时间", Formula: "加班时间+60"}}
	state.GiftCatalog = []giftInfo{{ID: 33300, Name: "666", Price: 1000, CoinType: "gold"}}

	applyGiftEvent(&state, giftEvent{
		GiftID: 33300, GiftName: "666", Num: 2, Price: 1000, CoinType: "gold",
		UID: 123, Uname: "旧昵称", Timestamp: 1_700_000_000, Rnd: "contribution-1",
	})
	applyGiftEvent(&state, giftEvent{
		GiftID: 999, GiftName: "其他礼物", Num: 1, Price: 500, CoinType: "gold",
		UID: 123, Uname: "新昵称", Avatar: "https://example.test/avatar.png", Timestamp: 1_700_000_001, Rnd: "contribution-2",
	})

	if len(state.Contributions.Viewers) != 1 {
		t.Fatalf("viewers = %#v", state.Contributions.Viewers)
	}
	viewer := state.Contributions.Viewers[0]
	if viewer.Key != "uid:123" || viewer.UID != 123 || viewer.Uname != "新昵称" || viewer.Avatar == "" {
		t.Fatalf("identity = %#v", viewer)
	}
	if viewer.GiftCount != 3 || viewer.GoldValue != 2500 {
		t.Fatalf("gift totals = %#v", viewer)
	}
	if viewer.RuleTriggers != 2 || viewer.AttributeDeltas["加班时间"] != 120 {
		t.Fatalf("rule totals = %#v", viewer)
	}
}

func TestContributionLedgerComputesBlindBoxProfitFromParentCost(t *testing.T) {
	state := defaultAppState()
	state.GiftCatalog = []giftInfo{
		{ID: 35800, Name: "小熊虫盲盒", Price: 9000, CoinType: "gold"},
		{ID: 35801, Name: "稀有礼物", Price: 12000, CoinType: "gold"},
	}

	applyGiftEvent(&state, giftEvent{
		GiftID: 35801, BlindGiftID: 35800, GiftName: "稀有礼物", Num: 2, Price: 12000, CoinType: "gold",
		UID: 456, Uname: "盲盒观众", Timestamp: 1_700_000_100, Rnd: "blind-profit-1",
	})

	viewer := state.Contributions.Viewers[0]
	if viewer.GoldValue != 18000 || viewer.BlindBoxCount != 2 {
		t.Fatalf("blind box contribution = %#v", viewer)
	}
	if viewer.BlindBoxCost != 18000 || viewer.BlindBoxValue != 24000 || viewer.BlindBoxProfit != 6000 {
		t.Fatalf("blind box profit = %#v", viewer)
	}
	if viewer.UnpricedBlindBoxCount != 0 {
		t.Fatalf("priced blind box marked unpriced: %#v", viewer)
	}
}

func TestContributionLedgerUsesMaskedNameWhenUIDIsMissing(t *testing.T) {
	state := defaultAppState()
	for index := range 2 {
		applyGiftEvent(&state, giftEvent{
			GiftID: 1, GiftName: "人气票", Num: 1, Price: 100, CoinType: "gold",
			Uname: "反***", Timestamp: 1_700_000_200 + int64(index), Rnd: "masked",
		})
	}
	if len(state.Contributions.Viewers) != 1 || state.Contributions.Viewers[0].GiftCount != 2 {
		t.Fatalf("masked contribution = %#v", state.Contributions.Viewers)
	}
}

func TestConfigPutPreservesNewerBackendContributionLedger(t *testing.T) {
	store := &configStore{path: filepath.Join(t.TempDir(), "config.json")}
	state := defaultAppState()
	state.Contributions = contributionLedgerState{
		UpdatedAt: 200,
		Viewers:   []viewerContribution{{Key: "uid:1", UID: 1, Uname: "后台观众", GiftCount: 3, AttributeDeltas: map[string]float64{}}},
	}
	if err := store.replaceState(state); err != nil {
		t.Fatal(err)
	}

	payload := `{"roomId":"31567150","contributions":{"updatedAt":100,"viewers":[{"key":"uid:2","uid":2,"uname":"旧页面","giftCount":99}]}}`
	response := httptest.NewRecorder()
	store.handle(response, httptest.NewRequest(http.MethodPut, "/api/config", strings.NewReader(payload)))
	if response.Code != http.StatusOK {
		t.Fatalf("PUT status = %d, body = %s", response.Code, response.Body.String())
	}
	updated, err := store.readState()
	if err != nil {
		t.Fatal(err)
	}
	if len(updated.Contributions.Viewers) != 1 || updated.Contributions.Viewers[0].UID != 1 {
		t.Fatalf("newer backend ledger was overwritten: %#v", updated.Contributions)
	}
}

func TestContributionLedgerHandlerClearsOnlyRankingData(t *testing.T) {
	store := &configStore{path: filepath.Join(t.TempDir(), "config.json")}
	state := defaultAppState()
	state.Attributes = []attributeState{{Name: "积分", Value: 7}}
	state.Log = []logEntry{{Time: 1, AttributeName: "积分", Delta: 1, ValueAfter: 7}}
	state.Contributions.Viewers = []viewerContribution{{Key: "uid:1", UID: 1, Uname: "观众", GiftCount: 1, AttributeDeltas: map[string]float64{}}}
	if err := store.replaceState(state); err != nil {
		t.Fatal(err)
	}

	response := httptest.NewRecorder()
	handleContributionLedger(store)(response, httptest.NewRequest(http.MethodDelete, "/api/contributions", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("DELETE status = %d, body = %s", response.Code, response.Body.String())
	}
	updated, err := store.readState()
	if err != nil {
		t.Fatal(err)
	}
	if len(updated.Contributions.Viewers) != 0 || updated.Contributions.UpdatedAt <= 0 {
		t.Fatalf("contributions were not cleared: %#v", updated.Contributions)
	}
	if updated.Attributes[0].Value != 7 || len(updated.Log) != 1 {
		t.Fatalf("clearing ranking changed runtime state: %#v", updated)
	}
}
