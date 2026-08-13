package main

import (
	"math"
	"sort"
	"strconv"
	"strings"

	"golang.org/x/text/collate"
	"golang.org/x/text/language"
)

type blindBoxLeaderboardQuery struct {
	GiftID   int
	Limit    int
	HasLimit bool
}

type blindBoxLeaderboardSummary struct {
	ViewerCount   int     `json:"viewerCount"`
	BlindBoxCount int     `json:"blindBoxCount"`
	Cost          float64 `json:"cost"`
	Value         float64 `json:"value"`
	Profit        float64 `json:"profit"`
	UnpricedCount int     `json:"unpricedCount"`
}

type blindBoxLeaderboardScope struct {
	GiftID     int    `json:"giftId"`
	GiftName   string `json:"giftName"`
	Count      int    `json:"count"`
	LastGiftAt int64  `json:"lastGiftAt"`
}

type blindBoxLeaderboardSnapshot struct {
	UpdatedAt int64                      `json:"updatedAt"`
	Summary   blindBoxLeaderboardSummary `json:"summary"`
	Viewers   []viewerContribution       `json:"viewers"`
	Scopes    []blindBoxLeaderboardScope `json:"scopes"`
}

func buildBlindBoxLeaderboard(ledger contributionLedgerState, query blindBoxLeaderboardQuery) blindBoxLeaderboardSnapshot {
	snapshot := blindBoxLeaderboardSnapshot{
		UpdatedAt: normalizeBlindBoxTimestamp(ledger.UpdatedAt),
		Viewers:   []viewerContribution{},
		Scopes:    buildBlindBoxLeaderboardScopes(ledger),
	}

	for _, viewer := range ledger.Viewers {
		projected, ok := projectBlindBoxViewer(viewer, query.GiftID)
		if !ok {
			continue
		}
		snapshot.Viewers = append(snapshot.Viewers, projected)
		snapshot.Summary.ViewerCount++
		snapshot.Summary.BlindBoxCount += projected.BlindBoxCount
		snapshot.Summary.Cost += projected.BlindBoxCost
		snapshot.Summary.Value += projected.BlindBoxValue
		snapshot.Summary.Profit += projected.BlindBoxProfit
		snapshot.Summary.UnpricedCount += projected.UnpricedBlindBoxCount
	}

	sort.SliceStable(snapshot.Viewers, func(left, right int) bool {
		return compareBlindBoxViewers(snapshot.Viewers[left], snapshot.Viewers[right]) < 0
	})

	if query.HasLimit {
		limit := max(0, query.Limit)
		if limit < len(snapshot.Viewers) {
			snapshot.Viewers = snapshot.Viewers[:limit]
		}
	}
	return snapshot
}

func projectBlindBoxViewer(viewer viewerContribution, giftID int) (viewerContribution, bool) {
	if giftID > 0 {
		for _, breakdown := range viewer.BlindBoxes {
			if breakdown.GiftID != giftID {
				continue
			}
			return projectBlindBoxBreakdown(viewer, breakdown)
		}
		return viewerContribution{}, false
	}

	projected := viewer
	projected.BlindBoxCount = normalizeBlindBoxCount(viewer.BlindBoxCount)
	if projected.BlindBoxCount == 0 {
		return viewerContribution{}, false
	}
	projected.BlindBoxCost = normalizeBlindBoxAmount(viewer.BlindBoxCost)
	projected.BlindBoxValue = normalizeBlindBoxAmount(viewer.BlindBoxValue)
	projected.BlindBoxProfit = projected.BlindBoxValue - projected.BlindBoxCost
	projected.UnpricedBlindBoxCount = normalizeBlindBoxCount(viewer.UnpricedBlindBoxCount)
	projected.LastGiftAt = normalizeBlindBoxTimestamp(viewer.LastGiftAt)
	return projected, true
}

func projectBlindBoxBreakdown(viewer viewerContribution, breakdown blindBoxContribution) (viewerContribution, bool) {
	if breakdown.GiftID <= 0 || breakdown.Count <= 0 {
		return viewerContribution{}, false
	}

	projected := viewer
	projected.BlindBoxCount = normalizeBlindBoxCount(breakdown.Count)
	if projected.BlindBoxCount == 0 {
		return viewerContribution{}, false
	}
	projected.BlindBoxCost = normalizeBlindBoxAmount(breakdown.Cost)
	projected.BlindBoxValue = normalizeBlindBoxAmount(breakdown.Value)
	projected.BlindBoxProfit = projected.BlindBoxValue - projected.BlindBoxCost
	projected.UnpricedBlindBoxCount = normalizeBlindBoxCount(breakdown.UnpricedCount)
	projected.LastGiftAt = normalizeBlindBoxTimestamp(breakdown.LastGiftAt)
	return projected, true
}

func buildBlindBoxLeaderboardScopes(ledger contributionLedgerState) []blindBoxLeaderboardScope {
	byGiftID := make(map[int]blindBoxLeaderboardScope)
	for _, viewer := range ledger.Viewers {
		for _, breakdown := range viewer.BlindBoxes {
			if breakdown.GiftID <= 0 || breakdown.Count <= 0 {
				continue
			}
			count := normalizeBlindBoxCount(breakdown.Count)
			if count == 0 {
				continue
			}
			giftName := strings.TrimSpace(breakdown.GiftName)
			if giftName == "" {
				giftName = "盲盒 " + strconv.Itoa(breakdown.GiftID)
			}
			lastGiftAt := normalizeBlindBoxTimestamp(breakdown.LastGiftAt)
			current, exists := byGiftID[breakdown.GiftID]
			if !exists {
				byGiftID[breakdown.GiftID] = blindBoxLeaderboardScope{
					GiftID: breakdown.GiftID, GiftName: giftName, Count: count, LastGiftAt: lastGiftAt,
				}
				continue
			}
			current.Count += count
			if lastGiftAt >= current.LastGiftAt {
				current.GiftName = giftName
				current.LastGiftAt = lastGiftAt
			}
			byGiftID[breakdown.GiftID] = current
		}
	}

	scopes := make([]blindBoxLeaderboardScope, 0, len(byGiftID))
	for _, scope := range byGiftID {
		scopes = append(scopes, scope)
	}
	collator := collate.New(language.Chinese)
	sort.Slice(scopes, func(left, right int) bool {
		if scopes[left].Count != scopes[right].Count {
			return scopes[left].Count > scopes[right].Count
		}
		if scopes[left].LastGiftAt != scopes[right].LastGiftAt {
			return scopes[left].LastGiftAt > scopes[right].LastGiftAt
		}
		if nameOrder := collator.CompareString(scopes[left].GiftName, scopes[right].GiftName); nameOrder != 0 {
			return nameOrder < 0
		}
		return scopes[left].GiftID < scopes[right].GiftID
	})
	return scopes
}

func compareBlindBoxViewers(left, right viewerContribution) int {
	if left.BlindBoxProfit != right.BlindBoxProfit {
		if left.BlindBoxProfit > right.BlindBoxProfit {
			return -1
		}
		return 1
	}
	if left.BlindBoxValue != right.BlindBoxValue {
		if left.BlindBoxValue > right.BlindBoxValue {
			return -1
		}
		return 1
	}
	if left.BlindBoxCount != right.BlindBoxCount {
		if left.BlindBoxCount > right.BlindBoxCount {
			return -1
		}
		return 1
	}
	if left.LastGiftAt != right.LastGiftAt {
		if left.LastGiftAt > right.LastGiftAt {
			return -1
		}
		return 1
	}
	return 0
}

func normalizeBlindBoxAmount(value float64) float64 {
	if math.IsNaN(value) || math.IsInf(value, 0) || value < 0 {
		return 0
	}
	return value
}

func normalizeBlindBoxCount(value int) int {
	return max(0, value)
}

func normalizeBlindBoxTimestamp(value int64) int64 {
	return max(int64(0), value)
}
