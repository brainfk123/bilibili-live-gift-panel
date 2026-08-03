import { describe, it, expect, vi } from 'vitest';
import { applyGiftToState, recordGiftTotals, resetTodayStats } from '../src/engine/rules';
import { Engine } from '../src/engine';
import { defaultState as createDefaultState } from '../src/storage';
import { GiftEvent } from '../src/bilibili/messages';
import { RoomInfo, WsLike } from '../src/bilibili/client';
import { encodeJson } from '../src/bilibili/protocol';
import { MAX_LOG } from '../src/types';

const fakeRoomInfo: RoomInfo = {
  roomId: 1,
  buvid: 'buvid-test',
  token: 'token-test',
  hostList: [{ host: 'chat.test.bilibili.com', wss_port: 2245 }],
};

function defaultState(): ReturnType<typeof createDefaultState> {
  const state = createDefaultState();
  state.attributes.push({ name: '加班时间', value: 0, unit: 'seconds', format: 'hhmmss', decimals: 0, suffix: '' });
  return state;
}

function makeGift(overrides: Partial<GiftEvent>): GiftEvent {
  return {
    giftId: 30607,
    giftName: '辣条',
    num: 1,
    price: 20,
    coinType: 'gold',
    totalCoin: 20,
    uname: 'user',
    uid: 1,
    timestamp: 1700000000,
    imgBasic: '',
    rnd: `r-${Math.random()}`,
    ...overrides,
  };
}

describe('applyGiftToState', () => {
  it('assigns formula result as the attribute value', () => {
    const s = defaultState();
    s.attributes[0].value = 100;
    s.rules.push({ id: 'r1', giftId: 30607, attributeName: '加班时间', formula: 'price/1000*60' });
    const rs = applyGiftToState(s, makeGift({ price: 1000 }));
    expect(rs).toHaveLength(1);
    expect(rs[0].delta).toBe(-40);
    expect(rs[0].valueAfter).toBe(60);
    expect(s.attributes[0].value).toBe(60);
  });

  it.each([
    [0, 1],
    [9, 10],
    [10, 20],
    [20, 40],
  ])('supports conditional increment then doubling: %i to %i', (before, expected) => {
    const s = defaultState();
    s.attributes[0] = { name: '早播次数', value: before, unit: 'none', format: 'number', decimals: 0, suffix: '' };
    s.rules.push({ id: 'r1', giftId: 32251, attributeName: '早播次数', formula: 'IF(早播次数<10,早播次数+1,早播次数*2)' });
    const results = applyGiftToState(s, makeGift({ giftId: 32251 }));
    expect(results[0].valueAfter).toBe(expected);
    expect(s.attributes[0].value).toBe(expected);
  });

  it('respects minPrice threshold', () => {
    const s = defaultState();
    s.rules.push({ id: 'r1', giftId: 30607, attributeName: '加班时间', formula: '10', minPrice: 100 });
    expect(applyGiftToState(s, makeGift({ price: 20 }))).toHaveLength(0);
    expect(applyGiftToState(s, makeGift({ price: 150 }))).toHaveLength(1);
  });

  it('clamps the assigned result to cap', () => {
    const s = defaultState();
    s.attributes[0].value = 90;
    s.rules.push({ id: 'r1', giftId: 30607, attributeName: '加班时间', formula: '150', cap: 100 });
    const rs = applyGiftToState(s, makeGift({}));
    expect(rs[0].delta).toBe(10);
    expect(rs[0].valueAfter).toBe(100);
    expect(s.attributes[0].value).toBe(100);
  });

  it('assigns a value below the cap even when the current value is at the cap', () => {
    const s = defaultState();
    s.attributes[0].value = 100;
    s.rules.push({ id: 'r1', giftId: 30607, attributeName: '加班时间', formula: '50', cap: 100 });
    const rs = applyGiftToState(s, makeGift({}));
    expect(rs[0].valueAfter).toBe(50);
    expect(s.attributes[0].value).toBe(50);
  });

  it('respects dailyLimit while executing a batch one gift at a time', () => {
    const s = defaultState();
    s.rules.push({ id: 'r1', giftId: 30607, attributeName: '加班时间', formula: '加班时间+1', dailyLimit: 2 });
    const rs = applyGiftToState(s, makeGift({ num: 3 }));
    expect(rs).toHaveLength(2);
    expect(s.attributes[0].value).toBe(2);
    const day = s.stats[Object.keys(s.stats)[0]];
    expect(day.ruleTriggers.r1).toBe(2);
  });

  it('ignores non-matching gift', () => {
    const s = defaultState();
    s.rules.push({ id: 'r1', giftId: 30607, attributeName: '加班时间', formula: '10' });
    expect(applyGiftToState(s, makeGift({ giftId: 999 }))).toHaveLength(0);
  });

  it('matches visually identical catalog aliases with different gift IDs', () => {
    const s = defaultState();
    s.rules.push({ id: 'r-alias', giftId: 33300, attributeName: '加班时间', formula: '加班时间+60' });

    const results = applyGiftToState(s, makeGift({
      giftId: 33012,
      giftName: '666',
      price: 1000,
      imgBasic: '',
    }));

    expect(results).toHaveLength(1);
    expect(s.attributes[0].value).toBe(60);
  });

  it('keeps matching a gift rule after only its icon and ID change', () => {
    const s = defaultState();
    s.attributes.push({ name: '加班时间', value: 0, unit: 'seconds', format: 'hhmmss', decimals: 0, suffix: '' });
    s.giftCatalog.push({ id: 970001, name: '情书', price: 5200, coinType: 'gold', imgBasic: 'old.png' });
    s.rules.push({ id: 'r-letter', giftId: 970001, attributeName: '加班时间', formula: '加班时间+60' });

    const results = applyGiftToState(s, makeGift({
      giftId: 970002,
      giftName: '情书',
      price: 5200,
      imgBasic: 'current.png',
    }));

    expect(results).toHaveLength(1);
    expect(s.attributes[0].value).toBe(60);
  });

  it('writes log entry', () => {
    const s = defaultState();
    s.attributes[0].value = 100;
    s.rules.push({ id: 'r1', giftId: 30607, attributeName: '加班时间', formula: '60' });
    applyGiftToState(s, makeGift({}));
    expect(s.log).toHaveLength(1);
    expect(s.log[0].delta).toBe(-40);
    expect(s.log[0].valueAfter).toBe(60);
    expect(s.log[0].attributeName).toBe('加班时间');
  });

  it('executes a batch sequentially with the current attribute value', () => {
    const s = defaultState();
    s.attributes[0].value = 5;
    s.rules.push({ id: 'r1', giftId: 30607, attributeName: '加班时间', formula: '加班时间+1' });
    const rs = applyGiftToState(s, makeGift({ num: 3 }));
    expect(rs).toHaveLength(3);
    expect(rs.map((result) => result.delta)).toEqual([1, 1, 1]);
    expect(rs.map((result) => result.valueAfter)).toEqual([6, 7, 8]);
    expect(rs.every((result) => result.gift.num === 1)).toBe(true);
    expect(s.attributes[0].value).toBe(8);
  });

  it('does not expose count as a formula variable', () => {
    const s = defaultState();
    s.rules.push({ id: 'r1', giftId: 30607, attributeName: '加班时间', formula: '加班时间+count' });
    expect(applyGiftToState(s, makeGift({ num: 3 }))).toHaveLength(0);
    expect(s.attributes[0].value).toBe(0);
  });

  it('lets one gift affect multiple attributes on every individual occurrence', () => {
    const s = defaultState();
    s.attributes.push({ name: '积分', value: 10, unit: 'none', format: 'number', decimals: 0, suffix: '' });
    s.rules.push(
      { id: 'r1', giftId: 30607, attributeName: '加班时间', formula: '加班时间+60' },
      { id: 'r2', giftId: 30607, attributeName: '积分', formula: '积分+1' },
    );

    const rs = applyGiftToState(s, makeGift({ num: 2 }));

    expect(rs).toHaveLength(4);
    expect(s.attributes[0].value).toBe(120);
    expect(s.attributes[1].value).toBe(12);
  });

  it('skips rule when formula errors or yields non-finite value', () => {
    const s = defaultState();
    s.rules.push({ id: 'r1', giftId: 30607, attributeName: '加班时间', formula: 'price/0' });
    expect(applyGiftToState(s, makeGift({}))).toHaveLength(0);
  });

  it('prunes log to MAX_LOG entries', () => {
    const s = defaultState();
    s.rules.push({ id: 'r1', giftId: 30607, attributeName: '加班时间', formula: '1' });
    for (let i = 0; i < MAX_LOG + 5; i++) {
      applyGiftToState(s, makeGift({}));
    }
    expect(s.log).toHaveLength(MAX_LOG);
  });

  it('counts each rule trigger in daily stats', () => {
    const s = defaultState();
    s.rules.push({ id: 'r1', giftId: 30607, attributeName: '加班时间', formula: '1' });
    applyGiftToState(s, makeGift({}));
    applyGiftToState(s, makeGift({}));
    const day = s.stats[Object.keys(s.stats)[0]];
    expect(day.ruleTriggers['r1']).toBe(2);
  });

  it('reports valueAfter in result', () => {
    const s = defaultState();
    s.attributes[0].value = 10;
    s.rules.push({ id: 'r1', giftId: 30607, attributeName: '加班时间', formula: '5' });
    const rs = applyGiftToState(s, makeGift({}));
    expect(rs[0].valueAfter).toBe(5);
  });
});

describe('recordGiftTotals', () => {
  it('accumulates today totals', () => {
    const s = defaultState();
    recordGiftTotals(s, makeGift({ giftId: 30607, num: 2 }));
    recordGiftTotals(s, makeGift({ giftId: 30607, num: 3 }));
    const day = s.stats[Object.keys(s.stats)[0]];
    expect(day.giftTotals[30607]).toBe(5);
  });
});

describe('resetTodayStats', () => {
  it('resets today stats', () => {
    const s = defaultState();
    recordGiftTotals(s, makeGift({ giftId: 30607, num: 5 }));
    resetTodayStats(s);
    const day = s.stats[Object.keys(s.stats)[0]];
    expect(day.giftTotals[30607]).toBeUndefined();
    expect(day.ruleTriggers).toEqual({});
  });
});

class FakeWs implements WsLike {
  binaryType = 'arraybuffer';
  onopen: (() => void) | null = null;
  onmessage: ((ev: { data: ArrayBuffer }) => void) | null = null;
  onclose: ((ev: { code: number }) => void) | null = null;
  onerror: ((ev: unknown) => void) | null = null;
  sent: ArrayBuffer[] = [];
  close = vi.fn();
  send(data: ArrayBuffer) { this.sent.push(data); }
  open() { this.onopen?.(); }
  message(data: ArrayBuffer) { this.onmessage?.({ data }); }
}

describe('Engine', () => {
  it('dedupes same rnd within 60s window', () => {
    vi.useFakeTimers();
    try {
      const s = defaultState();
      s.roomId = '1';
      s.rules.push({ id: 'r1', giftId: 30607, attributeName: '加班时间', formula: '10' });
      const onTrigger = vi.fn();
      const engine = new Engine(s, onTrigger, () => new FakeWs());
      const gift = makeGift({ rnd: 'same-rnd' });
      engine.handleGift(gift);
      engine.handleGift(gift);
      engine.handleGift(gift);
      expect(onTrigger).toHaveBeenCalledTimes(1);
      expect(s.attributes[0].value).toBe(10);
      vi.advanceTimersByTime(61000);
      engine.handleGift(gift);
      expect(onTrigger).toHaveBeenCalledTimes(2);
      expect(s.attributes[0].value).toBe(10);
    } finally {
      vi.useRealTimers();
    }
  });

  it('wires gift events from client into rules', async () => {
    const s = defaultState();
    s.roomId = '1';
    s.rules.push({ id: 'r1', giftId: 30607, attributeName: '加班时间', formula: '10' });
    const fake = new FakeWs();
    const onTrigger = vi.fn();
    const engine = new Engine(s, onTrigger, () => fake, async () => fakeRoomInfo);
    await engine.start();
    fake.open();
    fake.message(encodeGiftMessage());
    expect(s.attributes[0].value).toBe(10);
    expect(onTrigger).toHaveBeenCalledTimes(1);
    expect(s.recentGifts).toHaveLength(1);
    engine.stop();
  });

  it('sets roomId-less start as no-op', () => {
    const s = defaultState();
    const factory = vi.fn(() => new FakeWs());
    const engine = new Engine(s, undefined, factory);
    engine.start();
    expect(factory).not.toHaveBeenCalled();
  });

  it('propagates connection state to listener', async () => {
    const s = defaultState();
    s.roomId = '1';
    const fake = new FakeWs();
    const engine = new Engine(s, undefined, () => fake, async () => fakeRoomInfo);
    const onState = vi.fn();
    engine.setStateListener(onState);
    await engine.start();
    expect(onState).toHaveBeenCalledWith('connecting');
    fake.open();
    expect(onState).toHaveBeenCalledWith('connected');
    engine.stop();
  });

  it('handleSc records totals for gift', () => {
    const s = defaultState();
    const engine = new Engine(s);
    engine.handleSc({ id: 123, price: 30, message: 'hi', uname: 'u', uid: 1, giftId: 999, giftName: 'SC' });
    const day = s.stats[Object.keys(s.stats)[0]];
    expect(day.giftTotals[999]).toBe(1);
  });
});

function encodeGiftMessage(): ArrayBuffer {
  return encodeJson(5, {
    cmd: 'SEND_GIFT',
    data: { giftId: 30607, giftName: '辣条', num: 1, price: 20, coin_type: 'gold', uname: 'u', uid: 1, timestamp: 1700000000, gift_info: { img_basic: '' }, rnd: 'r9' },
  }).buffer as ArrayBuffer;
}
