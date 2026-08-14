package main

import (
	"math"
	"reflect"
	"testing"
)

func blindBoxLeaderboardFixture() contributionLedgerState {
	return contributionLedgerState{
		UpdatedAt: 500,
		Viewers: []viewerContribution{
			{
				Key: "uid:1", UID: 1, Uname: "盈利观众", GiftCount: 4, GoldValue: 20_000,
				BlindBoxCount: 2, BlindBoxCost: 18_000, BlindBoxValue: 25_000, BlindBoxProfit: 7_000, LastGiftAt: 300,
				BlindBoxes: []blindBoxContribution{
					{GiftID: 35800, GiftName: "小熊虫盲盒", Count: 1, Cost: 9_000, Value: 12_000, Profit: 3_000, LastGiftAt: 300},
					{GiftID: 35900, GiftName: "星愿盲盒", Count: 1, Cost: 9_000, Value: 13_000, Profit: 4_000, LastGiftAt: 290},
				},
			},
			{
				Key: "uid:2", UID: 2, Uname: "亏损观众", GiftCount: 3, GoldValue: 10_000,
				BlindBoxCount: 1, BlindBoxCost: 9_000, BlindBoxValue: 4_000, BlindBoxProfit: -5_000,
				UnpricedBlindBoxCount: 1, LastGiftAt: 200,
				BlindBoxes: []blindBoxContribution{
					{GiftID: 35800, GiftName: "小熊虫盲盒", Count: 1, Cost: 9_000, Value: 4_000, Profit: -5_000, UnpricedCount: 1, LastGiftAt: 200},
				},
			},
			{Key: "uid:3", UID: 3, Uname: "普通观众", GiftCount: 9, GoldValue: 90_000, LastGiftAt: 400},
		},
	}
}

func TestBuildBlindBoxLeaderboardSummarizesAllBoxes(t *testing.T) {
	snapshot := buildBlindBoxLeaderboard(blindBoxLeaderboardFixture(), blindBoxLeaderboardQuery{})

	if got := snapshot.Summary; got != (blindBoxLeaderboardSummary{
		ViewerCount: 2, BlindBoxCount: 3, Cost: 27000,
		Value: 29000, Profit: 2000, UnpricedCount: 1,
	}) {
		t.Fatalf("summary = %#v", got)
	}
	if names := []string{snapshot.Viewers[0].Uname, snapshot.Viewers[1].Uname}; !reflect.DeepEqual(names, []string{"盈利观众", "亏损观众"}) {
		t.Fatalf("viewer order = %#v", names)
	}
	if got, want := snapshot.Scopes, []blindBoxLeaderboardScope{
		{GiftID: 35800, GiftName: "小熊虫盲盒", Count: 2, LastGiftAt: 300},
		{GiftID: 35900, GiftName: "星愿盲盒", Count: 1, LastGiftAt: 290},
	}; !reflect.DeepEqual(got, want) {
		t.Fatalf("scopes = %#v, want %#v", got, want)
	}
}

func TestBuildBlindBoxLeaderboardLimitDoesNotChangeSummaryOrScopes(t *testing.T) {
	full := buildBlindBoxLeaderboard(blindBoxLeaderboardFixture(), blindBoxLeaderboardQuery{})
	limited := buildBlindBoxLeaderboard(blindBoxLeaderboardFixture(), blindBoxLeaderboardQuery{Limit: 1, HasLimit: true})

	if len(limited.Viewers) != 1 {
		t.Fatalf("limited viewers = %#v", limited.Viewers)
	}
	if limited.Summary.ViewerCount != 2 {
		t.Fatalf("summary viewer count = %d, want 2", limited.Summary.ViewerCount)
	}
	if !reflect.DeepEqual(limited.Summary, full.Summary) {
		t.Fatalf("limited summary = %#v, full summary = %#v", limited.Summary, full.Summary)
	}
	if !reflect.DeepEqual(limited.Scopes, full.Scopes) {
		t.Fatalf("limited scopes = %#v, full scopes = %#v", limited.Scopes, full.Scopes)
	}
}

func TestBuildBlindBoxLeaderboardProjectsOneGift(t *testing.T) {
	snapshot := buildBlindBoxLeaderboard(blindBoxLeaderboardFixture(), blindBoxLeaderboardQuery{GiftID: 35800})

	if got := snapshot.Summary; got != (blindBoxLeaderboardSummary{
		ViewerCount: 2, BlindBoxCount: 2, Cost: 18000,
		Value: 16000, Profit: -2000, UnpricedCount: 1,
	}) {
		t.Fatalf("summary = %#v", got)
	}
	if got := [][2]any{{snapshot.Viewers[0].Uname, snapshot.Viewers[0].BlindBoxProfit}, {snapshot.Viewers[1].Uname, snapshot.Viewers[1].BlindBoxProfit}}; !reflect.DeepEqual(got, [][2]any{{"盈利观众", float64(3000)}, {"亏损观众", float64(-5000)}}) {
		t.Fatalf("projected viewer profits = %#v", got)
	}
}

func TestBuildBlindBoxLeaderboardHandlesUnpricedAndEmptyRows(t *testing.T) {
	ledger := contributionLedgerState{Viewers: []viewerContribution{
		{Uname: "无价格", BlindBoxCount: 2, BlindBoxCost: 0, BlindBoxValue: 600, BlindBoxProfit: 999, UnpricedBlindBoxCount: 2, BlindBoxes: []blindBoxContribution{{GiftID: 10, GiftName: "无价格盲盒", Count: 2, Value: 600, UnpricedCount: 2}}},
		{Uname: "空行", BlindBoxCount: 0, BlindBoxes: []blindBoxContribution{{GiftID: 11, GiftName: "空盲盒", Count: 0}}},
	}}

	snapshot := buildBlindBoxLeaderboard(ledger, blindBoxLeaderboardQuery{})
	if got := snapshot.Summary; got != (blindBoxLeaderboardSummary{ViewerCount: 1, BlindBoxCount: 2, Value: 600, Profit: 600, UnpricedCount: 2}) {
		t.Fatalf("summary = %#v", got)
	}
	if len(snapshot.Viewers) != 1 || snapshot.Viewers[0].BlindBoxProfit != 600 {
		t.Fatalf("viewers = %#v", snapshot.Viewers)
	}
	if got, want := snapshot.Scopes, []blindBoxLeaderboardScope{{GiftID: 10, GiftName: "无价格盲盒", Count: 2}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("scopes = %#v, want %#v", got, want)
	}

	empty := buildBlindBoxLeaderboard(contributionLedgerState{}, blindBoxLeaderboardQuery{})
	if empty.Viewers == nil || empty.Scopes == nil || len(empty.Viewers) != 0 || len(empty.Scopes) != 0 {
		t.Fatalf("empty snapshot = %#v", empty)
	}
}

func TestBuildBlindBoxLeaderboardSortsViewersStably(t *testing.T) {
	ledger := contributionLedgerState{Viewers: []viewerContribution{
		{Uname: "first", BlindBoxCount: 1, BlindBoxCost: 100, BlindBoxValue: 200, LastGiftAt: 10},
		{Uname: "second", BlindBoxCount: 1, BlindBoxCost: 100, BlindBoxValue: 200, LastGiftAt: 10},
		{Uname: "newer", BlindBoxCount: 1, BlindBoxCost: 100, BlindBoxValue: 200, LastGiftAt: 11},
		{Uname: "more", BlindBoxCount: 2, BlindBoxCost: 200, BlindBoxValue: 400, LastGiftAt: 1},
		{Uname: "value", BlindBoxCount: 1, BlindBoxCost: 100, BlindBoxValue: 300, LastGiftAt: 1},
		{Uname: "profit", BlindBoxCount: 1, BlindBoxCost: 100, BlindBoxValue: 400, LastGiftAt: 1},
	}}

	snapshot := buildBlindBoxLeaderboard(ledger, blindBoxLeaderboardQuery{})
	if got := []string{snapshot.Viewers[0].Uname, snapshot.Viewers[1].Uname, snapshot.Viewers[2].Uname, snapshot.Viewers[3].Uname, snapshot.Viewers[4].Uname, snapshot.Viewers[5].Uname}; !reflect.DeepEqual(got, []string{"profit", "more", "value", "newer", "first", "second"}) {
		t.Fatalf("viewer order = %#v", got)
	}
}

func TestBuildBlindBoxLeaderboardSortsScopesWithChineseCollation(t *testing.T) {
	ledger := contributionLedgerState{Viewers: []viewerContribution{
		{BlindBoxes: []blindBoxContribution{
			{GiftID: 3, GiftName: "阿", Count: 1, LastGiftAt: 5},
			{GiftID: 2, GiftName: "八", Count: 1, LastGiftAt: 5},
			{GiftID: 4, GiftName: "同名", Count: 1, LastGiftAt: 5},
			{GiftID: 1, GiftName: "同名", Count: 1, LastGiftAt: 5},
		}},
	}}

	snapshot := buildBlindBoxLeaderboard(ledger, blindBoxLeaderboardQuery{})
	if got := []int{snapshot.Scopes[0].GiftID, snapshot.Scopes[1].GiftID, snapshot.Scopes[2].GiftID, snapshot.Scopes[3].GiftID}; !reflect.DeepEqual(got, []int{3, 2, 1, 4}) {
		t.Fatalf("scope order = %#v", got)
	}
}

func TestBuildBlindBoxLeaderboardUsesLatestScopeName(t *testing.T) {
	ledger := contributionLedgerState{Viewers: []viewerContribution{
		{BlindBoxes: []blindBoxContribution{{GiftID: 9, GiftName: "旧名字", Count: 1, LastGiftAt: 10}}},
		{BlindBoxes: []blindBoxContribution{{GiftID: 9, GiftName: "新名字", Count: 2, LastGiftAt: 20}}},
	}}

	snapshot := buildBlindBoxLeaderboard(ledger, blindBoxLeaderboardQuery{})
	if got, want := snapshot.Scopes, []blindBoxLeaderboardScope{{GiftID: 9, GiftName: "新名字", Count: 3, LastGiftAt: 20}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("scopes = %#v, want %#v", got, want)
	}
}

func TestBuildBlindBoxLeaderboardNormalizesInvalidNumbers(t *testing.T) {
	ledger := contributionLedgerState{UpdatedAt: -1, Viewers: []viewerContribution{
		{Uname: "坏数据", BlindBoxCount: -2, BlindBoxCost: math.Inf(1), BlindBoxValue: math.NaN(), UnpricedBlindBoxCount: -1, LastGiftAt: -1, BlindBoxes: []blindBoxContribution{
			{GiftID: -1, GiftName: "无效", Count: 1, Cost: 1, Value: 1, LastGiftAt: 1},
			{GiftID: 8, GiftName: "  ", Count: 2, Cost: math.Inf(-1), Value: math.NaN(), UnpricedCount: -2, LastGiftAt: -1},
		}},
		{Uname: "有效", BlindBoxCount: 1, BlindBoxCost: 100, BlindBoxValue: 20, BlindBoxProfit: math.Inf(1), LastGiftAt: 7},
	}}

	snapshot := buildBlindBoxLeaderboard(ledger, blindBoxLeaderboardQuery{})
	if snapshot.UpdatedAt != 0 {
		t.Fatalf("updatedAt = %d, want 0", snapshot.UpdatedAt)
	}
	if got, want := snapshot.Summary, (blindBoxLeaderboardSummary{ViewerCount: 1, BlindBoxCount: 1, Cost: 100, Value: 20, Profit: -80}); got != want {
		t.Fatalf("summary = %#v, want %#v", got, want)
	}
	if got, want := snapshot.Scopes, []blindBoxLeaderboardScope{{GiftID: 8, GiftName: "盲盒 8", Count: 2}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("scopes = %#v, want %#v", got, want)
	}
}
