import { afterEach, describe, expect, it, vi } from 'vitest';
import {
  getBiliAuthStatus,
  logoutBiliAuth,
  pollBiliQRCodeLogin,
  startBiliQRCodeLogin,
  startPagePresence,
} from '../src/backend';

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
