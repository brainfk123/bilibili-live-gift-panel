import { afterEach, describe, expect, it, vi } from 'vitest';
import {
  checkForUpdates,
  clearContributionLedger,
  clearGiftReceipts,
  giftReceiptMediaUrl,
  getBlindBoxLeaderboard,
  getBlindBoxInfo,
  getBiliAuthStatus,
  getRoomAnchorInfo,
  getRoomGiftCatalog,
  getUpdateStatus,
  logoutBiliAuth,
  pollBiliQRCodeLogin,
  resetGiftTargetProgress,
  startBiliQRCodeLogin,
  startPagePresence,
  transitionActivity,
} from '../src/backend';

const leaderboardSnapshot = {
  updatedAt: 1_700_000_000_000,
  summary: {
    viewerCount: 1,
    blindBoxCount: 2,
    cost: 18_000,
    value: 12_000,
    profit: -6_000,
    unpricedCount: 1,
  },
  viewers: [{
    key: 'uid:1',
    uid: 1,
    uname: '亏损观众',
    avatar: 'https://example.test/avatar.png',
    giftCount: 2,
    goldValue: 18_000,
    silverValue: 0,
    ruleTriggers: 0,
    attributeDeltas: { 积分: 2 },
    blindBoxCount: 2,
    blindBoxCost: 18_000,
    blindBoxValue: 12_000,
    blindBoxProfit: -6_000,
    unpricedBlindBoxCount: 1,
    blindBoxes: [{
      giftId: 35800,
      giftName: '小熊虫盲盒',
      count: 2,
      cost: 18_000,
      value: 12_000,
      profit: -6_000,
      unpricedCount: 1,
      lastGiftAt: 1_700_000_000_000,
    }],
    lastGiftAt: 1_700_000_000_000,
  }],
  scopes: [{ giftId: 35800, giftName: '小熊虫盲盒', count: 2, lastGiftAt: 1_700_000_000_000 }],
};

describe('blind box leaderboard API', () => {
  afterEach(() => vi.unstubAllGlobals());

  it('requests a scoped leaderboard in the server query order and preserves negative profit', async () => {
    const fetchMock = vi.fn(async () => Response.json({ code: 0, leaderboard: leaderboardSnapshot }));
    vi.stubGlobal('fetch', fetchMock);
    const signal = new AbortController().signal;

    await expect(getBlindBoxLeaderboard({ giftId: 35800, limit: 100, signal })).resolves.toEqual(leaderboardSnapshot);
    expect(fetchMock).toHaveBeenCalledWith('/api/blind-box/leaderboard?giftId=35800&limit=100', {
      cache: 'no-store', signal,
    });
  });

  it.each([
    ['unknown snapshot field', { ...leaderboardSnapshot, unexpected: true }],
    ['non-array viewers', { ...leaderboardSnapshot, viewers: {} }],
    ['non-array scopes', { ...leaderboardSnapshot, scopes: {} }],
    ['negative summary count', { ...leaderboardSnapshot, summary: { ...leaderboardSnapshot.summary, blindBoxCount: -1 } }],
    ['non-finite summary cost', { ...leaderboardSnapshot, summary: { ...leaderboardSnapshot.summary, cost: Number.POSITIVE_INFINITY } }],
    ['negative scope timestamp', { ...leaderboardSnapshot, scopes: [{ ...leaderboardSnapshot.scopes[0], lastGiftAt: -1 }] }],
    ['invalid scope gift id', { ...leaderboardSnapshot, scopes: [{ ...leaderboardSnapshot.scopes[0], giftId: 0 }] }],
    ['odd viewer shape', { ...leaderboardSnapshot, viewers: [{ ...leaderboardSnapshot.viewers[0], blindBoxes: {} }] }],
    ['negative viewer count', { ...leaderboardSnapshot, viewers: [{ ...leaderboardSnapshot.viewers[0], blindBoxCount: -1 }] }],
    ['non-finite viewer profit', { ...leaderboardSnapshot, viewers: [{ ...leaderboardSnapshot.viewers[0], blindBoxProfit: Number.NaN }] }],
    ['negative nested cost', { ...leaderboardSnapshot, viewers: [{ ...leaderboardSnapshot.viewers[0], blindBoxes: [{ ...leaderboardSnapshot.viewers[0].blindBoxes[0], cost: -1 }] }] }],
    ['invalid nested gift id', { ...leaderboardSnapshot, viewers: [{ ...leaderboardSnapshot.viewers[0], blindBoxes: [{ ...leaderboardSnapshot.viewers[0].blindBoxes[0], giftId: 0 }] }] }],
    ['non-finite updated at', { ...leaderboardSnapshot, updatedAt: Number.POSITIVE_INFINITY }],
    ['negative updated at', { ...leaderboardSnapshot, updatedAt: -1 }],
  ])('rejects a %s payload without partially normalizing it', async (_name, leaderboard) => {
    vi.stubGlobal('fetch', vi.fn(async () => Response.json({ code: 0, leaderboard })));

    await expect(getBlindBoxLeaderboard()).rejects.toThrow('盲盒排行榜响应无效');
  });
});

describe('gift target progress API', () => {
  afterEach(() => vi.unstubAllGlobals());

  it('clears one backend-owned target panel', async () => {
    const progress = { panelId: 'target 1', items: [{ giftId: 1, received: 0 }] };
    const fetchMock = vi.fn(async () => Response.json({ code: 0, progress }));
    vi.stubGlobal('fetch', fetchMock);

    await expect(resetGiftTargetProgress('target 1')).resolves.toEqual(progress);
    expect(fetchMock).toHaveBeenCalledWith('/api/gift-targets/progress?panelId=target%201', {
      method: 'DELETE', cache: 'no-store',
    });
  });
});

describe('contribution ledger API', () => {
  afterEach(() => vi.unstubAllGlobals());

  it('clears the backend-owned ranking data', async () => {
    const contributions = { viewers: [], updatedAt: 123 };
    const fetchMock = vi.fn(async () => Response.json({ code: 0, contributions }));
    vi.stubGlobal('fetch', fetchMock);

    await expect(clearContributionLedger()).resolves.toEqual(contributions);
    expect(fetchMock).toHaveBeenCalledWith('/api/contributions', { method: 'DELETE', cache: 'no-store' });
  });
});

describe('gift receipt API', () => {
  afterEach(() => vi.unstubAllGlobals());

  it('clears the backend-owned receipt ledger and builds same-origin media URLs', async () => {
    const giftReceipts: unknown[] = [];
    const fetchMock = vi.fn(async () => Response.json({ code: 0, giftReceipts }));
    vi.stubGlobal('fetch', fetchMock);

    await expect(clearGiftReceipts()).resolves.toEqual([]);
    expect(fetchMock).toHaveBeenCalledWith('/api/gift-receipts', { method: 'DELETE', cache: 'no-store' });
    expect(giftReceiptMediaUrl('receipt / 1', 'animation')).toBe('/api/gift-receipts/media?id=receipt%20%2F%201&kind=animation');
    expect(giftReceiptMediaUrl('receipt / 1', 'effect-video')).toBe('/api/gift-receipts/media?id=receipt%20%2F%201&kind=effect-video');
  });
});

describe('activity transition API', () => {
  afterEach(() => vi.unstubAllGlobals());

  it('returns the backend activity state and restored attribute values', async () => {
    const activity = {
      id: 'activity-1', name: '对抗', attributeNames: ['红队'], status: 'active', resultMode: 'none',
      gateRules: true, initialValues: { 红队: 0 }, milestones: [],
    };
    const fetchMock = vi.fn(async () => Response.json({ code: 0, activity, attributeValues: { 红队: 0 } }));
    vi.stubGlobal('fetch', fetchMock);

    await expect(transitionActivity('activity-1', 'start')).resolves.toEqual({ activity, attributeValues: { 红队: 0 } });
    expect(fetchMock).toHaveBeenCalledWith('/api/activities/transition', expect.objectContaining({
      method: 'POST', body: JSON.stringify({ activityId: 'activity-1', action: 'start' }),
    }));
  });
});

describe('automatic update API', () => {
  afterEach(() => vi.unstubAllGlobals());

  it('reads update status and starts a manual check', async () => {
    const status = {
      state: 'up-to-date', currentVersion: '1.0.0', latestVersion: '1.0.0', message: '当前已经是最新版本。',
      autoUpdate: true, restartRequired: false,
    };
    const fetchMock = vi.fn(async () => Response.json({ code: 0, update: status }));
    vi.stubGlobal('fetch', fetchMock);

    await expect(getUpdateStatus()).resolves.toEqual(status);
    await expect(checkForUpdates()).resolves.toEqual(status);
    expect(fetchMock).toHaveBeenNthCalledWith(1, '/api/update', { cache: 'no-store' });
    expect(fetchMock).toHaveBeenNthCalledWith(2, '/api/update/check', { cache: 'no-store', method: 'POST' });
  });
});

describe('current room gift catalog API', () => {
  afterEach(() => vi.unstubAllGlobals());

  it('returns only the gift versions exposed by the current room panel', async () => {
    const fetchMock = vi.fn(async () => Response.json({
      code: 0,
      gifts: [{ id: 35545, name: '情书', price: 5200, coinType: 'gold', imgBasic: 'current.png' }],
    }));
    vi.stubGlobal('fetch', fetchMock);

    await expect(getRoomGiftCatalog(' 24849407 ')).resolves.toEqual([
      { id: 35545, name: '情书', price: 5200, coinType: 'gold', imgBasic: 'current.png' },
    ]);
    expect(fetchMock).toHaveBeenCalledWith('/api/gifts?roomId=24849407', { cache: 'no-store' });
  });
});

describe('room broadcaster API', () => {
  afterEach(() => vi.unstubAllGlobals());

  it('normalizes the broadcaster UID, name, and avatar for the room card', async () => {
    const fetchMock = vi.fn(async () => Response.json({
      code: 0,
      roomId: '31567150',
      anchor: { uid: 32249588, uname: ' 反重力鱼 ', avatar: ' https://example.test/avatar.png ' },
    }));
    vi.stubGlobal('fetch', fetchMock);

    await expect(getRoomAnchorInfo(' 31567150 ')).resolves.toEqual({
      roomId: '31567150',
      uid: 32249588,
      uname: '反重力鱼',
      avatar: 'https://example.test/avatar.png',
    });
    expect(fetchMock).toHaveBeenCalledWith('/api/room/anchor?roomId=31567150', { cache: 'no-store' });
  });
});

describe('page presence', () => {
  afterEach(() => vi.unstubAllGlobals());

  it('opens one presence stream and reconnects after a restored page', () => {
    const opened: Array<{ url: string; close: ReturnType<typeof vi.fn> }> = [];
    const listeners = new Map<string, () => void>();
    class FakeEventSource {
      close = vi.fn();

      constructor(readonly url: string) {
        opened.push({ url, close: this.close });
      }
    }
    vi.stubGlobal('EventSource', FakeEventSource);
    vi.stubGlobal('crypto', { randomUUID: () => 'page-session-1' });
    vi.stubGlobal('addEventListener', (name: string, listener: () => void) => listeners.set(name, listener));
    vi.stubGlobal('removeEventListener', vi.fn());

    const stop = startPagePresence('config');
    expect(opened).toHaveLength(1);
    expect(opened[0].url).toBe('/api/pages/presence/stream?mode=config&id=page-session-1');

    listeners.get('pagehide')?.();
    expect(opened[0].close).toHaveBeenCalledOnce();
    listeners.get('pageshow')?.();
    expect(opened).toHaveLength(2);

    stop();
    expect(opened[1].close).toHaveBeenCalledOnce();
  });
});

describe('optional Bilibili login API', () => {
  afterEach(() => vi.unstubAllGlobals());

  it('starts and polls QR login without exposing cookies to the frontend', async () => {
    const requests: Array<{ url: string; method: string }> = [];
    vi.stubGlobal('fetch', vi.fn(async (input: string | URL | Request, init?: RequestInit) => {
      const url = String(input);
      const method = init?.method ?? 'GET';
      requests.push({ url, method });
      if (url === '/api/auth/status?room_id=31567150') {
        return Response.json({ code: 0, auth: { state: 'anonymous' } });
      }
      if (url === '/api/auth/qrcode' && method === 'POST') {
        return Response.json({ code: 0, auth: { state: 'waiting', qrImage: 'data:image/png;base64,qr', expiresAt: 123 } });
      }
      if (url === '/api/auth/qrcode?room_id=31567150') {
        return Response.json({ code: 0, auth: { state: 'logged_in', uid: 32249588, uname: '反重力鱼', avatar: 'face.jpg' } });
      }
      if (url === '/api/auth/session' && method === 'DELETE') {
        return Response.json({ code: 0, auth: { state: 'anonymous' } });
      }
      return new Response(null, { status: 404 });
    }));

    expect(await getBiliAuthStatus('31567150')).toEqual({ state: 'anonymous' });
    expect(await startBiliQRCodeLogin()).toEqual({ state: 'waiting', qrImage: 'data:image/png;base64,qr', expiresAt: 123 });
    expect(await pollBiliQRCodeLogin('31567150')).toEqual({ state: 'logged_in', uid: 32249588, uname: '反重力鱼', avatar: 'face.jpg' });
    expect(await logoutBiliAuth()).toEqual({ state: 'anonymous' });
    expect(JSON.stringify(requests)).not.toContain('SESSDATA');
    expect(requests).toEqual([
      { url: '/api/auth/status?room_id=31567150', method: 'GET' },
      { url: '/api/auth/qrcode', method: 'POST' },
      { url: '/api/auth/qrcode?room_id=31567150', method: 'GET' },
      { url: '/api/auth/session', method: 'DELETE' },
    ]);
  });
});

describe('blind box metadata API', () => {
  afterEach(() => vi.unstubAllGlobals());

  it('returns exploded gift matching metadata without exposing auth details', async () => {
    const fetchMock = vi.fn(async () => Response.json({
      code: 0,
      blindBox: {
        giftId: 35800,
        name: '小熊虫盲盒',
        gifts: [{ id: 35801, name: '心事虫虫', price: 9000, imgBasic: 'child.png', chance: '50%' }],
      },
      requiresLogin: false,
    }));
    vi.stubGlobal('fetch', fetchMock);

    const lookup = await getBlindBoxInfo(35800);
    expect(lookup.info?.giftId).toBe(35800);
    expect(lookup.info?.gifts[0].id).toBe(35801);
    expect(lookup.requiresLogin).toBe(false);
    expect(fetchMock).toHaveBeenCalledWith('/api/blind-box?giftId=35800', { cache: 'no-store' });
  });
});
