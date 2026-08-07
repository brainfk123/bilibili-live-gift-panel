package main

import (
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	maxContributionViewers     = 2000
	maxContributionNameRunes   = 80
	maxContributionAvatarBytes = 2048
)

type giftContributionOutcome struct {
	RuleTriggers int
	Changes      []logEntry
}

func enrichBlindBoxGiftFromCatalog(state appState, gift giftEvent) giftEvent {
	if gift.BlindGiftID > 0 {
		return gift
	}
	child := state.findGift(gift.GiftID)
	if child == nil || child.BlindBoxParentID <= 0 {
		return gift
	}
	gift.BlindGiftID = child.BlindBoxParentID
	gift.BlindGiftName = child.BlindBoxParentName
	gift.BlindGiftPrice = child.BlindBoxParentPrice
	return gift
}

func recordGiftContribution(state *appState, gift giftEvent, outcome giftContributionOutcome) {
	normalizeContributionLedger(&state.Contributions)
	key := contributionIdentityKey(gift)
	index := findContributionViewer(state.Contributions.Viewers, key, gift)
	viewer := viewerContribution{
		Key:             key,
		AttributeDeltas: map[string]float64{},
	}
	if index >= 0 {
		viewer = state.Contributions.Viewers[index]
		if viewer.AttributeDeltas == nil {
			viewer.AttributeDeltas = map[string]float64{}
		}
		state.Contributions.Viewers = append(state.Contributions.Viewers[:index], state.Contributions.Viewers[index+1:]...)
	}
	if gift.UID > 0 {
		viewer.UID = gift.UID
		viewer.Key = fmt.Sprintf("uid:%d", gift.UID)
	}
	if name := truncateContributionText(strings.TrimSpace(gift.Uname), maxContributionNameRunes); name != "" {
		viewer.Uname = name
	}
	if avatar := strings.TrimSpace(gift.Avatar); avatar != "" {
		if len(avatar) > maxContributionAvatarBytes {
			avatar = avatar[:maxContributionAvatarBytes]
		}
		viewer.Avatar = avatar
	}
	if viewer.Uname == "" {
		viewer.Uname = "匿名观众"
	}

	count := maxInt(1, gift.Num)
	viewer.GiftCount += count
	viewer.RuleTriggers += maxInt(0, outcome.RuleTriggers)
	for _, change := range outcome.Changes {
		viewer.AttributeDeltas[change.AttributeName] += change.Delta
	}

	paidValue := giftPaidValue(*state, gift, count)
	if strings.EqualFold(strings.TrimSpace(gift.CoinType), "silver") {
		viewer.SilverValue += paidValue
	} else {
		viewer.GoldValue += paidValue
	}
	if gift.BlindGiftID > 0 {
		cost, priced := blindBoxCost(*state, gift, count)
		value := blindBoxOutputValue(*state, gift, count)
		viewer.BlindBoxCount += count
		viewer.BlindBoxCost += cost
		viewer.BlindBoxValue += value
		viewer.BlindBoxProfit = viewer.BlindBoxValue - viewer.BlindBoxCost
		if !priced {
			viewer.UnpricedBlindBoxCount += count
		}
		recordBlindBoxBreakdown(state, &viewer, gift, count, cost, value, priced)
	}

	viewer.LastGiftAt = normalizeGiftTimestamp(gift.Timestamp)
	state.Contributions.Viewers = append([]viewerContribution{viewer}, state.Contributions.Viewers...)
	if len(state.Contributions.Viewers) > maxContributionViewers {
		state.Contributions.Viewers = state.Contributions.Viewers[:maxContributionViewers]
	}
	updatedAt := viewer.LastGiftAt
	if updatedAt <= state.Contributions.UpdatedAt {
		updatedAt = state.Contributions.UpdatedAt + 1
	}
	state.Contributions.UpdatedAt = updatedAt
}

func recordBlindBoxBreakdown(state *appState, viewer *viewerContribution, gift giftEvent, count int, cost, value float64, priced bool) {
	if gift.BlindGiftID <= 0 {
		return
	}
	index := -1
	for candidate := range viewer.BlindBoxes {
		if viewer.BlindBoxes[candidate].GiftID == gift.BlindGiftID {
			index = candidate
			break
		}
	}
	if index < 0 {
		viewer.BlindBoxes = append(viewer.BlindBoxes, blindBoxContribution{GiftID: gift.BlindGiftID})
		index = len(viewer.BlindBoxes) - 1
	}
	breakdown := &viewer.BlindBoxes[index]
	name := strings.TrimSpace(gift.BlindGiftName)
	if name == "" {
		if parent := state.findGift(gift.BlindGiftID); parent != nil {
			name = strings.TrimSpace(parent.Name)
		}
	}
	if name != "" {
		breakdown.GiftName = truncateContributionText(name, maxContributionNameRunes)
	}
	if breakdown.GiftName == "" {
		breakdown.GiftName = fmt.Sprintf("盲盒 %d", gift.BlindGiftID)
	}
	breakdown.Count += count
	breakdown.Cost += cost
	breakdown.Value += value
	breakdown.Profit = breakdown.Value - breakdown.Cost
	if !priced {
		breakdown.UnpricedCount += count
	}
	breakdown.LastGiftAt = normalizeGiftTimestamp(gift.Timestamp)
}

func contributionIdentityKey(gift giftEvent) string {
	if gift.UID > 0 {
		return fmt.Sprintf("uid:%d", gift.UID)
	}
	name := strings.ToLower(strings.TrimSpace(gift.Uname))
	if name == "" {
		name = "anonymous"
	}
	return "name:" + truncateContributionText(name, maxContributionNameRunes)
}

func findContributionViewer(viewers []viewerContribution, key string, gift giftEvent) int {
	for index := range viewers {
		if viewers[index].Key == key || (gift.UID > 0 && viewers[index].UID == gift.UID) {
			return index
		}
	}
	if gift.UID <= 0 || strings.TrimSpace(gift.Uname) == "" {
		return -1
	}
	for index := range viewers {
		if viewers[index].UID == 0 && strings.EqualFold(strings.TrimSpace(viewers[index].Uname), strings.TrimSpace(gift.Uname)) {
			return index
		}
	}
	return -1
}

func giftPaidValue(state appState, gift giftEvent, count int) float64 {
	if gift.BlindGiftID > 0 {
		if value, priced := blindBoxCost(state, gift, count); priced {
			return value
		}
	}
	if gift.TotalCoin > 0 {
		return gift.TotalCoin
	}
	unitPrice := gift.Price
	if unitPrice <= 0 {
		if catalogGift := state.findGift(gift.GiftID); catalogGift != nil {
			unitPrice = catalogGift.Price
		}
	}
	return maxFloat(0, unitPrice) * float64(count)
}

func blindBoxCost(state appState, gift giftEvent, count int) (float64, bool) {
	if gift.BlindGiftPrice > 0 {
		return gift.BlindGiftPrice * float64(count), true
	}
	if parent := state.findGift(gift.BlindGiftID); parent != nil && parent.Price > 0 {
		return parent.Price * float64(count), true
	}
	if gift.TotalCoin > 0 {
		return gift.TotalCoin, true
	}
	return 0, false
}

func blindBoxOutputValue(state appState, gift giftEvent, count int) float64 {
	unitPrice := gift.Price
	if unitPrice <= 0 {
		if output := state.findGift(gift.GiftID); output != nil {
			unitPrice = output.Price
		}
	}
	return maxFloat(0, unitPrice) * float64(count)
}

func normalizeContributionLedger(ledger *contributionLedgerState) {
	if ledger.Viewers == nil {
		ledger.Viewers = []viewerContribution{}
	}
	seen := make(map[string]struct{}, len(ledger.Viewers))
	normalized := make([]viewerContribution, 0, minInt(len(ledger.Viewers), maxContributionViewers))
	for _, viewer := range ledger.Viewers {
		viewer.Key = strings.TrimSpace(viewer.Key)
		if viewer.Key == "" {
			if viewer.UID > 0 {
				viewer.Key = fmt.Sprintf("uid:%d", viewer.UID)
			} else {
				viewer.Key = "name:" + strings.ToLower(strings.TrimSpace(viewer.Uname))
			}
		}
		if viewer.Key == "name:" || viewer.Key == "" {
			continue
		}
		if _, exists := seen[viewer.Key]; exists {
			continue
		}
		seen[viewer.Key] = struct{}{}
		viewer.Uname = truncateContributionText(strings.TrimSpace(viewer.Uname), maxContributionNameRunes)
		if viewer.Uname == "" {
			viewer.Uname = "匿名观众"
		}
		viewer.Avatar = strings.TrimSpace(viewer.Avatar)
		if len(viewer.Avatar) > maxContributionAvatarBytes {
			viewer.Avatar = viewer.Avatar[:maxContributionAvatarBytes]
		}
		viewer.GiftCount = maxInt(0, viewer.GiftCount)
		viewer.RuleTriggers = maxInt(0, viewer.RuleTriggers)
		viewer.BlindBoxCount = maxInt(0, viewer.BlindBoxCount)
		viewer.UnpricedBlindBoxCount = maxInt(0, viewer.UnpricedBlindBoxCount)
		viewer.GoldValue = maxFloat(0, viewer.GoldValue)
		viewer.SilverValue = maxFloat(0, viewer.SilverValue)
		viewer.BlindBoxCost = maxFloat(0, viewer.BlindBoxCost)
		viewer.BlindBoxValue = maxFloat(0, viewer.BlindBoxValue)
		viewer.BlindBoxProfit = viewer.BlindBoxValue - viewer.BlindBoxCost
		viewer.BlindBoxes = normalizeBlindBoxBreakdowns(viewer.BlindBoxes)
		if viewer.AttributeDeltas == nil {
			viewer.AttributeDeltas = map[string]float64{}
		}
		normalized = append(normalized, viewer)
		if len(normalized) >= maxContributionViewers {
			break
		}
	}
	sort.SliceStable(normalized, func(left, right int) bool {
		return normalized[left].LastGiftAt > normalized[right].LastGiftAt
	})
	ledger.Viewers = normalized
	ledger.UpdatedAt = maxInt64(0, ledger.UpdatedAt)
}

func normalizeBlindBoxBreakdowns(breakdowns []blindBoxContribution) []blindBoxContribution {
	normalized := make([]blindBoxContribution, 0, len(breakdowns))
	byGiftID := make(map[int]int, len(breakdowns))
	for _, breakdown := range breakdowns {
		if breakdown.GiftID <= 0 {
			continue
		}
		breakdown.GiftName = truncateContributionText(strings.TrimSpace(breakdown.GiftName), maxContributionNameRunes)
		if breakdown.GiftName == "" {
			breakdown.GiftName = fmt.Sprintf("盲盒 %d", breakdown.GiftID)
		}
		breakdown.Count = maxInt(0, breakdown.Count)
		breakdown.Cost = maxFloat(0, breakdown.Cost)
		breakdown.Value = maxFloat(0, breakdown.Value)
		breakdown.Profit = breakdown.Value - breakdown.Cost
		breakdown.UnpricedCount = maxInt(0, breakdown.UnpricedCount)
		breakdown.LastGiftAt = maxInt64(0, breakdown.LastGiftAt)
		if index, exists := byGiftID[breakdown.GiftID]; exists {
			current := &normalized[index]
			current.Count += breakdown.Count
			current.Cost += breakdown.Cost
			current.Value += breakdown.Value
			current.Profit = current.Value - current.Cost
			current.UnpricedCount += breakdown.UnpricedCount
			if breakdown.LastGiftAt >= current.LastGiftAt {
				current.GiftName = breakdown.GiftName
				current.LastGiftAt = breakdown.LastGiftAt
			}
			continue
		}
		byGiftID[breakdown.GiftID] = len(normalized)
		normalized = append(normalized, breakdown)
	}
	return normalized
}

func handleContributionLedger(store *configStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet:
			state, err := store.readState()
			if err != nil {
				writeJSON(w, http.StatusInternalServerError, map[string]any{"code": -1, "message": err.Error()})
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"code": 0, "contributions": state.Contributions})
		case http.MethodDelete:
			state, err := store.updateState(func(state *appState) error {
				state.Contributions = contributionLedgerState{
					Viewers:   []viewerContribution{},
					UpdatedAt: time.Now().UnixMilli(),
				}
				return nil
			})
			if err != nil {
				writeJSON(w, http.StatusInternalServerError, map[string]any{"code": -1, "message": err.Error()})
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"code": 0, "contributions": state.Contributions})
		default:
			w.Header().Set("Allow", "GET, DELETE")
			writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"code": -1, "message": "不支持的请求方法"})
		}
	}
}

func normalizeGiftTimestamp(timestamp int64) int64 {
	if timestamp <= 0 {
		return time.Now().UnixMilli()
	}
	if timestamp < 1_000_000_000_000 {
		return timestamp * 1000
	}
	return timestamp
}

func truncateContributionText(value string, limit int) string {
	if utf8.RuneCountInString(value) <= limit {
		return value
	}
	runes := []rune(value)
	return string(runes[:limit])
}

func maxFloat(left, right float64) float64 {
	if left > right {
		return left
	}
	return right
}

func minInt(left, right int) int {
	if left < right {
		return left
	}
	return right
}

func maxInt64(left, right int64) int64 {
	if left > right {
		return left
	}
	return right
}
