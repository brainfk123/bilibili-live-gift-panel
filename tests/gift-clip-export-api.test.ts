import { afterEach, describe, expect, it, vi } from 'vitest';
import {
  cancelGiftClipJob,
  createGiftClipJob,
  getGiftClipJob,
  giftClipJobVideoURL,
  waitForGiftClipJob,
} from '../src/ui/config/gift-clip-export-api';

const jobID = 'AQIDBAUGBwgJCgsMDQ4PEBES';

function snapshot(overrides: Record<string, unknown> = {}) {
  return {
    id: jobID,
    state: 'queued',
    progress: 0,
    output: { width: 960, height: 540, fps: 30 },
    ...overrides,
  };
}

describe('gift clip export API', () => {
  afterEach(() => {
    vi.useRealTimers();
    vi.unstubAllGlobals();
  });

  it('creates a versioned multipart export without sending media URLs', async () => {
    const fetchMock = vi.fn(async (_input: RequestInfo | URL, _init?: RequestInit) => Response.json(snapshot(), { status: 202 }));
    vi.stubGlobal('fetch', fetchMock);

    await expect(createGiftClipJob({
      receiptId: 'receipt-1',
      crop: { x: 101, y: 53, width: 960, height: 540 },
      background: new Blob(['bg'], { type: 'image/png' }),
      overlay: new Blob(['overlay'], { type: 'image/png' }),
    })).resolves.toEqual(snapshot());

    const [url, init] = fetchMock.mock.calls[0];
    expect(url).toBe('/api/gift-clips');
    expect(init?.method).toBe('POST');
    expect(init?.body).toBeInstanceOf(FormData);
    expect(init?.headers).toBeUndefined();
    const body = init?.body as FormData;
    expect(body.get('metadata')).toBe(JSON.stringify({ receiptId: 'receipt-1', crop: { x: 101, y: 53, width: 960, height: 540 }, version: 1 }));
    expect(body.get('background')).toBeInstanceOf(File);
    expect(body.get('overlay')).toBeInstanceOf(File);
    expect(JSON.stringify([...body.entries()])).not.toContain('hdslb.com');
  });

  it.each([
    ['wrong status', () => Response.json(snapshot(), { status: 200 })],
    ['HTML response', () => new Response('<html>private-secret</html>', { status: 202, headers: { 'content-type': 'text/html' } })],
    ['malformed JSON', () => new Response('{', { status: 202, headers: { 'content-type': 'application/json' } })],
    ['unknown state', () => Response.json(snapshot({ state: 'unknown' }), { status: 202 })],
    ['non-finite progress', () => Response.json(snapshot({ progress: '0.5' }), { status: 202 })],
    ['out of range progress', () => Response.json(snapshot({ progress: 1.1 }), { status: 202 })],
    ['wrong dimensions', () => Response.json(snapshot({ output: { width: 960.5, height: 540, fps: 30 } }), { status: 202 })],
    ['wrong fps', () => Response.json(snapshot({ output: { width: 960, height: 540, fps: 60 } }), { status: 202 })],
    ['extra field', () => Response.json(snapshot({ extra: true }), { status: 202 })],
  ])('rejects a %s create response without exposing its body', async (_name, response) => {
    vi.stubGlobal('fetch', vi.fn(response));

    await expect(createGiftClipJob({
      receiptId: 'receipt-1', crop: { x: 0, y: 0, width: 960, height: 540 },
      background: new Blob(['bg']), overlay: new Blob(['overlay']),
    })).rejects.toThrow('Invalid gift clip job response.');
  });

  it('gets only a valid opaque job ID and builds a same-origin video URL', async () => {
    const fetchMock = vi.fn(async () => Response.json(snapshot({ state: 'encoding', progress: .5 })));
    vi.stubGlobal('fetch', fetchMock);

    await expect(getGiftClipJob(jobID)).resolves.toEqual(snapshot({ state: 'encoding', progress: .5 }));
    expect(fetchMock).toHaveBeenCalledWith(`/api/gift-clips/${jobID}`, { cache: 'no-store', signal: undefined });
    expect(giftClipJobVideoURL(jobID)).toBe(`/api/gift-clips/${jobID}/video`);
    expect(() => getGiftClipJob('job_abc')).toThrow('Invalid gift clip job ID.');
    expect(() => giftClipJobVideoURL('../secret')).toThrow('Invalid gift clip job ID.');
  });

  it('maps Task 8’s exact flat HTTP snapshot into the public output contract', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => Response.json({
      id: jobID, state: 'encoding', progress: .5, message: '正在编码。', width: 960, height: 540, fps: 30,
    })));

    await expect(getGiftClipJob(jobID)).resolves.toEqual({
      ...snapshot({ state: 'encoding', progress: .5 }), message: '正在编码。',
    });
  });

  it('treats missing deletes as already cancelled and rejects other delete failures', async () => {
    const fetchMock = vi.fn()
      .mockResolvedValueOnce(new Response(null, { status: 404 }))
      .mockResolvedValueOnce(new Response(null, { status: 410 }))
      .mockResolvedValueOnce(new Response('<html>secret</html>', { status: 500 }));
    vi.stubGlobal('fetch', fetchMock);

    await expect(cancelGiftClipJob(jobID)).resolves.toBeUndefined();
    await expect(cancelGiftClipJob(jobID)).resolves.toBeUndefined();
    await expect(cancelGiftClipJob(jobID)).rejects.toThrow('Unable to cancel gift clip job.');
    expect(fetchMock).toHaveBeenNthCalledWith(1, `/api/gift-clips/${jobID}`, { method: 'DELETE' });
  });

  it('polls through compatibility retry mode with progress reset and resolves ready', async () => {
    const states = [
      snapshot({ state: 'queued', progress: 0 }),
      snapshot({ state: 'encoding', progress: .6 }),
      snapshot({ state: 'retrying', progress: .6, message: 'server detail' }),
      snapshot({ state: 'encoding', progress: .2 }),
      snapshot({ state: 'ready', progress: 1 }),
    ];
    vi.stubGlobal('fetch', vi.fn(async () => Response.json(states.shift())));
    const seen: Array<{ state: string; progress: number; message?: string }> = [];
    const sleep = vi.fn(() => Promise.resolve());

    await expect(waitForGiftClipJob(jobID, {
      intervalMs: 7,
      sleep,
      onSnapshot: (value) => seen.push(value),
    })).resolves.toEqual(snapshot({ state: 'ready', progress: 1 }));

    expect(sleep).toHaveBeenCalledTimes(4);
    expect(sleep).toHaveBeenCalledWith(7, undefined);
    expect(seen.map(({ state, progress, message }) => ({ state, progress, message }))).toEqual([
      { state: 'queued', progress: 0, message: undefined },
      { state: 'encoding', progress: .6, message: undefined },
      { state: 'retrying', progress: 0, message: '已切换兼容编码模式。' },
      { state: 'encoding', progress: .2, message: undefined },
      { state: 'ready', progress: 1, message: undefined },
    ]);
  });

  it('does not make another GET after aborting during its injected wait', async () => {
    const controller = new AbortController();
    const fetchMock = vi.fn(async () => Response.json(snapshot()));
    vi.stubGlobal('fetch', fetchMock);
    const abortError = new DOMException('cancelled', 'AbortError');
    const sleep = vi.fn(async () => { controller.abort(abortError); });

    await expect(waitForGiftClipJob(jobID, { signal: controller.signal, sleep })).rejects.toBe(abortError);
    expect(fetchMock).toHaveBeenCalledOnce();
  });

  it('preserves the abort reason when aborting while the response body is being read', async () => {
    const controller = new AbortController();
    const abortError = new DOMException('stop body read', 'AbortError');
    const bodyAbortError = new DOMException('body reader error', 'AbortError');
    let bodyReadStarted!: () => void;
    const started = new Promise<void>((resolve) => { bodyReadStarted = resolve; });
    vi.stubGlobal('fetch', vi.fn(async () => ({
      status: 200,
      headers: new Headers({ 'content-type': 'application/json' }),
      json: () => new Promise((_resolve, reject) => {
        bodyReadStarted();
        controller.signal.addEventListener('abort', () => reject(bodyAbortError), { once: true });
      }),
    } as Response)));

    const request = getGiftClipJob(jobID, controller.signal);
    await started;
    controller.abort(abortError);

    await expect(request).rejects.toBe(abortError);
  });

  it('polls beyond 120 snapshots by default and only times out at an explicit attempt limit', async () => {
    let defaultAttempt = 0;
    const fetchMock = vi.fn(async () => Response.json(
      snapshot(defaultAttempt++ < 121 ? { state: 'queued' } : { state: 'ready', progress: 1 }),
    ));
    vi.stubGlobal('fetch', fetchMock);

    await expect(waitForGiftClipJob(jobID, { sleep: () => Promise.resolve() }))
      .resolves.toEqual(snapshot({ state: 'ready', progress: 1 }));
    expect(fetchMock).toHaveBeenCalledTimes(122);

    fetchMock.mockClear();
    defaultAttempt = 0;
    await expect(waitForGiftClipJob(jobID, { sleep: () => Promise.resolve(), maxAttempts: 2 }))
      .rejects.toThrow('Gift clip job polling timed out.');
    expect(fetchMock).toHaveBeenCalledTimes(2);
  });

  it('removes the default sleep abort listener after both timeout and abort', async () => {
    vi.useFakeTimers();
    const completedController = new AbortController();
    const completedAdd = vi.spyOn(completedController.signal, 'addEventListener');
    const completedRemove = vi.spyOn(completedController.signal, 'removeEventListener');
    let attempt = 0;
    vi.stubGlobal('fetch', vi.fn(async () => Response.json(
      snapshot(attempt++ < 3 ? { state: 'queued' } : { state: 'ready', progress: 1 }),
    )));

    const completed = waitForGiftClipJob(jobID, { intervalMs: 10, signal: completedController.signal });
    await vi.runAllTimersAsync();
    await expect(completed).resolves.toEqual(snapshot({ state: 'ready', progress: 1 }));
    expect(completedAdd).toHaveBeenCalledTimes(3);
    expect(completedRemove).toHaveBeenCalledTimes(3);

    const abortedController = new AbortController();
    const abortedAdd = vi.spyOn(abortedController.signal, 'addEventListener');
    const abortedRemove = vi.spyOn(abortedController.signal, 'removeEventListener');
    const abortError = new DOMException('stop timer', 'AbortError');
    vi.stubGlobal('fetch', vi.fn(async () => Response.json(snapshot())));
    const aborted = waitForGiftClipJob(jobID, { intervalMs: 10, signal: abortedController.signal });
    for (let turn = 0; turn < 10 && abortedAdd.mock.calls.length === 0; turn += 1) await Promise.resolve();
    expect(abortedAdd).toHaveBeenCalledTimes(1);
    abortedController.abort(abortError);

    await expect(aborted).rejects.toBe(abortError);
    expect(abortedRemove).toHaveBeenCalledTimes(1);
    expect(vi.getTimerCount()).toBe(0);
  });

  it('preserves a terminal snapshot message and rejects decreasing progress within an attempt', async () => {
    const failed = snapshot({ state: 'failed', progress: .6, message: '磁盘空间不足。' });
    vi.stubGlobal('fetch', vi.fn(async () => Response.json(failed)));
    await expect(waitForGiftClipJob(jobID, { sleep: () => Promise.resolve() }))
      .rejects.toThrow('磁盘空间不足。');

    vi.stubGlobal('fetch', vi.fn()
      .mockResolvedValueOnce(Response.json(snapshot({ state: 'encoding', progress: .6 })))
      .mockResolvedValueOnce(Response.json(snapshot({ state: 'encoding', progress: .5 }))));
    await expect(waitForGiftClipJob(jobID, { sleep: () => Promise.resolve() }))
      .rejects.toThrow('Gift clip job progress regressed.');
  });

  it('preserves a terminal message even when a backend failure reports reset progress', async () => {
    vi.stubGlobal('fetch', vi.fn()
      .mockResolvedValueOnce(Response.json(snapshot({ state: 'encoding', progress: .6 })))
      .mockResolvedValueOnce(Response.json(snapshot({ state: 'failed', progress: 0, message: '兼容编码也失败。' }))));

    await expect(waitForGiftClipJob(jobID, { sleep: () => Promise.resolve() }))
      .rejects.toThrow('兼容编码也失败。');
  });
});
