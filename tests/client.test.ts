import { describe, it, expect, vi } from 'vitest';
import { DanmakuClient, RoomInfo, WsLike } from '../src/bilibili/client';
import { OP_MESSAGE, decodePackets, decodeText, encodeJson, encodePacket } from '../src/bilibili/protocol';

async function deflate(bytes: Uint8Array): Promise<Uint8Array> {
  const copy = new Uint8Array(bytes);
  const stream = new Blob([copy]).stream().pipeThrough(new CompressionStream('deflate'));
  const buf = await new Response(stream).arrayBuffer();
  return new Uint8Array(buf);
}

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

const fakeRoomInfo: RoomInfo = {
  roomId: 2145,
  buvid: 'buvid-test',
  token: 'token-test',
  hostList: [{ host: 'chat.test.bilibili.com', wss_port: 2245 }],
};

function makeClient(extra: any = {}): { client: DanmakuClient; factory: ReturnType<typeof vi.fn>; } {
  const factory = vi.fn(() => new FakeWs());
  const client = new DanmakuClient({
    roomId: 2145,
    roomInfoFetcher: async () => fakeRoomInfo,
    wsFactory: factory,
    ...extra,
  });
  return { client, factory };
}

describe('DanmakuClient', () => {
  it('fetches room info and sends auth with key on open', async () => {
    const { client, factory } = makeClient();
    await client.start();
    const fake = factory.mock.results[0].value as FakeWs;
    fake.open();
    expect(fake.sent.length).toBe(1);
    const auth = JSON.parse(decodeText(decodePackets(new Uint8Array(fake.sent[0]))[0].body));
    expect(auth.roomid).toBe(2145);
    expect(auth.uid).toBe(0);
    expect(auth.protover).toBe(2);
    expect(auth.buvid).toBe('buvid-test');
    expect(auth.key).toBe('token-test');
  });

  it('connects to the host from room info', async () => {
    const { client, factory } = makeClient();
    await client.start();
    expect(factory.mock.calls[0][0]).toBe('wss://chat.test.bilibili.com:2245/sub');
  });

  it('emits error state when room info fetch fails', async () => {
    const onState = vi.fn();
    const client = new DanmakuClient({
      roomId: 2145,
      roomInfoFetcher: async () => { throw new Error('fetch failed'); },
      onState,
    });
    await client.start();
    expect(onState).toHaveBeenCalledWith('error');
  });

  it('dispatches gift event', async () => {
    const onGift = vi.fn();
    const { client, factory } = makeClient({ onGift });
    await client.start();
    const fake = factory.mock.results[0].value as FakeWs;
    fake.open();
    fake.message(giftPacket());
    expect(onGift).toHaveBeenCalledTimes(1);
    const ev = onGift.mock.calls[0][0];
    expect(ev.giftName).toBe('辣条');
    expect(ev.giftId).toBe(30607);
  });

  it('dispatches gift from a protover=2 zlib frame', async () => {
    const onGift = vi.fn();
    const { client, factory } = makeClient({ onGift });
    await client.start();
    const fake = factory.mock.results[0].value as FakeWs;
    fake.open();
    const gift = {
      cmd: 'SEND_GIFT',
      data: { giftId: 30607, giftName: '辣条', num: 1, price: 20, coin_type: 'gold', uname: 'u', uid: 1, timestamp: 1700000000, gift_info: { img_basic: '' }, rnd: 'r1' },
    };
    const compressed = await deflate(encodeJson(OP_MESSAGE, gift));
    const frame = encodePacket(OP_MESSAGE, compressed, 2).buffer as ArrayBuffer;
    fake.message(frame);
    await vi.waitFor(() => expect(onGift).toHaveBeenCalledTimes(1));
    const ev = onGift.mock.calls[0][0];
    expect(ev.giftName).toBe('辣条');
    expect(ev.giftId).toBe(30607);
  });

  it('skips a corrupt protover=2 frame and still dispatches the rest of the batch', async () => {
    const onGift = vi.fn();
    const { client, factory } = makeClient({ onGift });
    await client.start();
    const fake = factory.mock.results[0].value as FakeWs;
    fake.open();
    const corrupt = encodePacket(OP_MESSAGE, new Uint8Array([1, 5, 0, 0, 0, 65, 65, 65, 65, 65]), 2);
    const valid = new Uint8Array(giftPacket());
    const combined = new Uint8Array(corrupt.length + valid.length);
    combined.set(corrupt, 0);
    combined.set(valid, corrupt.length);
    fake.message(combined.buffer as ArrayBuffer);
    await vi.waitFor(() => expect(onGift).toHaveBeenCalledTimes(1));
  });

  it('reconnects on close with backoff', async () => {
    vi.useFakeTimers();
    const factory = vi.fn(() => new FakeWs());
    const onState = vi.fn();
    const client = new DanmakuClient({ roomId: 1, roomInfoFetcher: async () => fakeRoomInfo, wsFactory: factory, onState });
    const startPromise = client.start();
    await vi.advanceTimersByTimeAsync(0);
    await startPromise;
    const first = factory.mock.results[0].value as FakeWs;
    first.open();
    first.closeFromServer();
    await vi.advanceTimersByTimeAsync(2000);
    expect(factory).toHaveBeenCalledTimes(2);
    vi.useRealTimers();
  });

  it('stop prevents reconnect', async () => {
    vi.useFakeTimers();
    const factory = vi.fn(() => new FakeWs());
    const client = new DanmakuClient({ roomId: 1, roomInfoFetcher: async () => fakeRoomInfo, wsFactory: factory });
    const startPromise = client.start();
    await vi.advanceTimersByTimeAsync(0);
    await startPromise;
    const first = factory.mock.results[0].value as FakeWs;
    first.open();
    client.stop();
    first.closeFromServer();
    await vi.advanceTimersByTimeAsync(60000);
    expect(factory).toHaveBeenCalledTimes(1);
    vi.useRealTimers();
  });
});
