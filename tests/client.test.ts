import { describe, it, expect, vi } from 'vitest';
import { DanmakuClient, WsLike } from '../src/bilibili/client';
import { decodePackets, decodeText, encodeJson } from '../src/bilibili/protocol';

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
  closeFromServer(code = 1006) { this.onclose?.({ code }); }
}

function giftPacket(): ArrayBuffer {
  const gift = {
    cmd: 'SEND_GIFT',
    data: { giftId: 30607, giftName: '辣条', num: 1, price: 20, coin_type: 'gold', uname: 'u', uid: 1, timestamp: 1700000000, gift_info: { img_basic: '' }, rnd: 'r1' },
  };
  return encodeJson(5, gift).buffer as ArrayBuffer;
}

describe('DanmakuClient', () => {
  it('sends auth packet on open', () => {
    const fake = new FakeWs();
    const client = new DanmakuClient({ roomId: 2145, wsFactory: () => fake });
    client.start();
    fake.open();
    expect(fake.sent.length).toBe(1);
    const auth = JSON.parse(decodeText(decodePackets(new Uint8Array(fake.sent[0]))[0].body));
    expect(auth.roomid).toBe(2145);
    expect(auth.uid).toBe(0);
    expect(auth.protover).toBe(2);
  });

  it('dispatches gift event', () => {
    const fake = new FakeWs();
    const onGift = vi.fn();
    const client = new DanmakuClient({ roomId: 2145, wsFactory: () => fake, onGift });
    client.start();
    fake.open();
    fake.message(giftPacket());
    expect(onGift).toHaveBeenCalledTimes(1);
    const ev = onGift.mock.calls[0][0];
    expect(ev.giftName).toBe('辣条');
    expect(ev.giftId).toBe(30607);
  });

  it('reconnects on close with backoff', () => {
    vi.useFakeTimers();
    const factory = vi.fn(() => new FakeWs());
    const onState = vi.fn();
    const client = new DanmakuClient({ roomId: 1, wsFactory: factory, onState });
    client.start();
    const first = factory.mock.results[0].value as FakeWs;
    first.open();
    first.closeFromServer();
    vi.advanceTimersByTime(2000);
    expect(factory).toHaveBeenCalledTimes(2);
    vi.useRealTimers();
  });

  it('stop prevents reconnect', () => {
    vi.useFakeTimers();
    const factory = vi.fn(() => new FakeWs());
    const client = new DanmakuClient({ roomId: 1, wsFactory: factory });
    client.start();
    const first = factory.mock.results[0].value as FakeWs;
    first.open();
    client.stop();
    first.closeFromServer();
    vi.advanceTimersByTime(60000);
    expect(factory).toHaveBeenCalledTimes(1);
    vi.useRealTimers();
  });
});
