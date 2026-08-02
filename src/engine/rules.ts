import { evalFormula } from '../formula';
import { getDayStats, todayStr } from '../format';
import { GiftEvent } from '../bilibili/messages';
import { AppState, GiftRule, LogEntry } from '../types';
import { pruneLog } from '../storage';
import { findGift, sameGiftIdentity } from '../gifts/catalog';

export interface TriggerResult {
  rule: GiftRule;
  gift: GiftEvent;
  delta: number;
  valueAfter: number;
}

export function applyGiftToState(state: AppState, gift: GiftEvent): TriggerResult[] {
  const results: TriggerResult[] = [];
  const day = getDayStats(state);
  const repetitions = Math.max(1, Math.floor(gift.num || 1));

  for (let occurrence = 0; occurrence < repetitions; occurrence += 1) {
    const unitGift: GiftEvent = { ...gift, num: 1, totalCoin: gift.price };
    for (const rule of state.rules) {
      const configuredGift = findGift(state, rule.giftId);
      const matchesAlias = configuredGift !== undefined && sameGiftIdentity(configuredGift, {
        name: unitGift.giftName,
        price: unitGift.price,
        coinType: unitGift.coinType,
        imgBasic: unitGift.imgBasic,
      });
      if (rule.giftId !== unitGift.giftId && !matchesAlias) continue;
      const attr = state.attributes.find((a) => a.name === rule.attributeName);
      if (!attr) continue;
      if (rule.minPrice !== undefined && unitGift.price < rule.minPrice) continue;
      const triggerCount = day.ruleTriggers[rule.id] ?? 0;
      if (rule.dailyLimit !== undefined && triggerCount >= rule.dailyLimit) continue;
      const env: Record<string, number> = { price: unitGift.price };
      for (const a of state.attributes) env[a.name] = a.value;
      let nextValue: number;
      try {
        nextValue = evalFormula(rule.formula, env);
      } catch {
        continue;
      }
      if (!Number.isFinite(nextValue)) continue;
      const before = attr.value;
      const valueAfter = rule.cap === undefined ? nextValue : Math.min(nextValue, rule.cap);
      if (!Number.isFinite(valueAfter)) continue;
      const delta = valueAfter - before;
      attr.value = valueAfter;
      day.ruleTriggers[rule.id] = triggerCount + 1;
      const entry: LogEntry = {
        time: unitGift.timestamp,
        giftId: unitGift.giftId,
        giftName: unitGift.giftName,
        num: 1,
        uname: unitGift.uname,
        avatar: unitGift.avatar,
        senderUid: unitGift.uid,
        attributeName: attr.name,
        delta,
        valueAfter: attr.value,
        ruleId: rule.id,
      };
      state.log = pruneLog([entry, ...state.log]);
      results.push({ rule, gift: unitGift, delta, valueAfter: attr.value });
    }
  }
  return results;
}

export function recordGiftTotals(state: AppState, gift: GiftEvent): void {
  const day = getDayStats(state);
  day.giftTotals[gift.giftId] = (day.giftTotals[gift.giftId] ?? 0) + gift.num;
}

export function resetTodayStats(state: AppState): void {
  state.stats[todayStr()] = { date: todayStr(), giftTotals: {}, ruleTriggers: {} };
}
