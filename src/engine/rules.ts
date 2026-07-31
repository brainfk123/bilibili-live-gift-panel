import { evalFormula } from '../formula';
import { getDayStats, todayStr } from '../format';
import { GiftEvent } from '../bilibili/messages';
import { AppState, GiftRule, LogEntry } from '../types';
import { pruneLog } from '../storage';

export interface TriggerResult {
  rule: GiftRule;
  gift: GiftEvent;
  delta: number;
  valueAfter: number;
}

export function applyGiftToState(state: AppState, gift: GiftEvent): TriggerResult[] {
  const results: TriggerResult[] = [];
  const day = getDayStats(state);
  for (const rule of state.rules) {
    if (rule.giftId !== gift.giftId) continue;
    const attr = state.attributes.find((a) => a.name === rule.attributeName);
    if (!attr) continue;
    if (rule.minPrice !== undefined && gift.price < rule.minPrice) continue;
    const triggerCount = day.ruleTriggers[rule.id] ?? 0;
    if (rule.dailyLimit !== undefined && triggerCount >= rule.dailyLimit) continue;
    const env: Record<string, number> = { price: gift.price, count: gift.num };
    for (const a of state.attributes) env[a.name] = a.value;
    let delta: number;
    try {
      delta = evalFormula(rule.formula, env);
    } catch {
      continue;
    }
    if (!Number.isFinite(delta)) continue;
    if (rule.cap !== undefined) {
      const room = rule.cap - attr.value;
      if (room <= 0) continue;
      if (delta > room) delta = room;
    }
    attr.value += delta;
    day.ruleTriggers[rule.id] = triggerCount + 1;
    const entry: LogEntry = {
      time: gift.timestamp,
      giftId: gift.giftId,
      giftName: gift.giftName,
      num: gift.num,
      uname: gift.uname,
      attributeName: attr.name,
      delta,
      valueAfter: attr.value,
      ruleId: rule.id,
    };
    state.log = pruneLog([entry, ...state.log]);
    results.push({ rule, gift, delta, valueAfter: attr.value });
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
