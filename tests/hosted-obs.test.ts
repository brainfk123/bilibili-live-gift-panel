import { readFileSync } from 'node:fs';
import { join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { describe, expect, it } from 'vitest';

import { HostedAPI } from '../src/hosted/api';
import { encodeOBSOutputSelector, parseOBSLocation, parseOBSOutputSelector, postOBSToken, startOBSPage } from '../src/hosted/obs/main';
import { computeOBSLayout, parseOBSDisplaySnapshot, renderOBSSnapshot, type OBSDisplaySnapshot } from '../src/hosted/obs/view';

const projectRoot = fileURLToPath(new URL('..', import.meta.url));
const publicID = 'AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA';

describe('hosted OBS fragment exchange', () => {
  it('posts the token in the body, clears the hash immediately, then creates EventSource', async () => {
    const order: string[] = [];
    let resolveExchange: (() => void) | undefined;
    const exchangePending = new Promise<void>((resolve) => { resolveExchange = resolve; });
    const started = startOBSPage({
      location: { pathname: `/obs/${publicID}`, search: '?theme=glass', hash: '#token=long%2Dsecret' },
      replaceState: (_state, _title, cleanURL) => { order.push(`replace:${cleanURL}`); },
      exchange: (id, token) => { order.push(`exchange:${id}:${token}`); return exchangePending; },
      createEventSource: (path) => { order.push(`events:${path}`); return { close: () => undefined }; },
      onSnapshot: () => undefined,
      onError: () => undefined,
    });

    expect(order).toEqual([
      `replace:/obs/${publicID}?theme=glass`,
	  `exchange:${publicID}:long-secret`,
    ]);
    resolveExchange?.();
    const connection = await started;
    expect(order).toEqual([
      `replace:/obs/${publicID}?theme=glass`,
	  `exchange:${publicID}:long-secret`,
      `events:/obs/${publicID}/events`,
    ]);
    connection.close();
  });

  it('reconnects with the scoped cookie when no fragment token remains', async () => {
    const calls: string[] = [];
    await startOBSPage({
      location: { pathname: `/obs/${publicID}`, search: '', hash: '' },
      replaceState: () => { throw new Error('must not rewrite a clean reconnect URL'); },
      exchange: async () => { throw new Error('must not exchange without a fragment'); },
      createEventSource: (path) => { calls.push(path); return { close: () => undefined }; },
      onSnapshot: () => undefined,
      onError: () => undefined,
    });
    expect(calls).toEqual([`/obs/${publicID}/events`]);
  });

  it('rejects malformed routes and fragments without starting exchange or SSE', async () => {
    expect(parseOBSLocation({ pathname: `/obs/${publicID}/events`, search: '', hash: '#token=secret' })).toBeUndefined();
    expect(parseOBSLocation({ pathname: `/obs/${publicID}`, search: '', hash: '#token=a&extra=b' })).toBeUndefined();
  });

  it('accepts only narrow migration output selectors and carries them to the event stream', async () => {
    const scene = 'eyJraW5kIjoic2NlbmUiLCJpZCI6IuS4u-WcuuaZrzrwn5SlIiwiYXR0cmlidXRlcyI6WyLnp6_liIY68J-UpSIsIueUn-WRveWAvC_kuIrpmZAiXX0';
    const target = 'eyJraW5kIjoiZ2lmdC10YXJnZXQiLCJpZCI6Iuebruaghy_pmLbmrrU65LiAIn0';
    expect(parseOBSLocation({ pathname: `/obs/${publicID}`, search: `?output=${scene}`, hash: '' })?.output)
      .toEqual({ kind: 'scene', id: '主场景:🔥', attributeIds: ['积分:🔥', '生命值/上限'] });
    expect(parseOBSLocation({ pathname: `/obs/${publicID}`, search: `?output=${scene}&extra=1`, hash: '' })).toBeUndefined();
    const paths: string[] = [];
    await startOBSPage({
      location: { pathname: `/obs/${publicID}`, search: `?output=${target}`, hash: '' }, replaceState: () => undefined,
      exchange: async () => undefined, createEventSource: (path) => { paths.push(path); return { close: () => undefined }; }, onSnapshot: () => undefined, onError: () => undefined,
    });
    expect(paths).toEqual([`/obs/${publicID}/events?output=${target}`]);
  });

  it('shares canonical base64url JSON selector fixtures and rejects noncanonical or duplicate scenes', () => {
    expect(encodeOBSOutputSelector({ kind: 'attribute', id: '积分:🔥', attributeIds: ['积分:🔥'] })).toBe('eyJraW5kIjoiYXR0cmlidXRlIiwiaWQiOiLnp6_liIY68J-UpSJ9');
    expect(encodeOBSOutputSelector({ kind: 'gift-target', id: '目标/阶段:一', attributeIds: [] })).toBe('eyJraW5kIjoiZ2lmdC10YXJnZXQiLCJpZCI6Iuebruaghy_pmLbmrrU65LiAIn0');
    expect(encodeOBSOutputSelector({ kind: 'scene', id: '主场景:🔥', attributeIds: ['积分:🔥', '生命值/上限'] })).toBe('eyJraW5kIjoic2NlbmUiLCJpZCI6IuS4u-WcuuaZrzrwn5SlIiwiYXR0cmlidXRlcyI6WyLnp6_liIY68J-UpSIsIueUn-WRveWAvC_kuIrpmZAiXX0');
    expect(encodeOBSOutputSelector({ kind: 'attribute', id: '<>&\u2028"\n', attributeIds: ['<>&\u2028"\n'] })).toBe('eyJraW5kIjoiYXR0cmlidXRlIiwiaWQiOiI8PibigKhcIlxuIn0');
    expect(parseOBSOutputSelector('eyJpZCI6InNjb3JlIiwia2luZCI6ImF0dHJpYnV0ZSJ9')).toBeUndefined();
    expect(parseOBSOutputSelector('eyJraW5kIjoic2NlbmUiLCJpZCI6Im1haW4iLCJhdHRyaWJ1dGVzIjpbInNjb3JlIiwic2NvcmUiXX0')).toBeUndefined();
    const long = `长:${'界'.repeat(2_000)}`;
    expect(parseOBSOutputSelector(encodeOBSOutputSelector({ kind: 'attribute', id: long, attributeIds: [long] })!)?.id).toBe(long);
    const oversized = 'x'.repeat(70 * 1024);
    expect(encodeOBSOutputSelector({ kind: 'attribute', id: oversized, attributeIds: [oversized] })).toBeUndefined();
    const boundary = 'x'.repeat(46_052);
    expect(encodeOBSOutputSelector({ kind: 'attribute', id: boundary, attributeIds: [boundary] })).toHaveLength(60 * 1024);
    const aboveBoundary = `${boundary}x`;
    expect(encodeOBSOutputSelector({ kind: 'attribute', id: aboveBoundary, attributeIds: [aboveBoundary] })).toBeUndefined();
  });

  it('synchronously clears every non-empty fragment before validating it', async () => {
	const order: string[] = [];
	const started = startOBSPage({
	  location: { pathname: `/obs/${publicID}`, search: '?theme=glass', hash: '#token=a&extra=b' },
	  replaceState: (_state, _title, cleanURL) => { order.push(`replace:${cleanURL}`); },
	  exchange: async () => { order.push('exchange'); },
	  createEventSource: () => { order.push('events'); return { close: () => undefined }; },
	  onSnapshot: () => undefined,
	  onError: () => { order.push('error'); },
	});
	const rejected = expect(started).rejects.toThrow('invalid_obs_location');
	expect(order).toEqual([`replace:/obs/${publicID}?theme=glass`, 'error']);
	await rejected;
	expect(order).toEqual([`replace:/obs/${publicID}?theme=glass`, 'error']);
  });

  it('captures the token before replaceState mutates the live Location object', async () => {
	const order: string[] = [];
	const location = { pathname: `/obs/${publicID}`, search: '', hash: '#token=live-secret' };
	const source = await startOBSPage({
	  location,
	  replaceState: () => { order.push('replace'); location.hash = ''; },
	  exchange: async (_id, token) => { order.push(`exchange:${token}`); },
	  createEventSource: () => { order.push('events'); return { close: () => undefined }; },
	  onSnapshot: () => undefined,
	  onError: () => { order.push('error'); },
	});
	expect(order).toEqual(['replace', 'exchange:live-secret', 'events']);
	source.close();
  });

  it('uses the exact exchange route and a strict token-only JSON DTO', async () => {
    const calls: Array<{ input: string; init?: RequestInit }> = [];
    await postOBSToken(publicID, 'body-only-secret', async (input, init) => {
      calls.push({ input: String(input), init });
      return new Response(null, { status: 204 });
    });
    expect(calls).toEqual([{
      input: `/obs/${publicID}/exchange`,
      init: {
        method: 'POST', credentials: 'same-origin', headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ token: 'body-only-secret' }),
      },
    }]);
    expect(calls[0]?.input).not.toContain('body-only-secret');
  });

  it('parses the one-time administrator create/reset response exactly', async () => {
    const requests: string[] = [];
    const api = await HostedAPI.connect(async (input, init) => {
      requests.push(`${init?.method} ${String(input)} ${String(init?.body ?? '')}`);
      if (String(input) === '/api/bootstrap') {
        return Response.json({ csrfToken: 'csrf' });
      }
      return Response.json({ publicId: publicID, url: `https://host.example/obs/${publicID}#token=once` }, { status: 201 });
    });
    await expect(api.issueOBSCredential(41)).resolves.toEqual({
      publicId: publicID,
      url: `https://host.example/obs/${publicID}#token=once`,
    });
    expect(requests[1]).toBe('POST /api/admin/accounts/41/obs-credential {}');
  });
});

describe('hosted OBS read-only rendering', () => {
  it('renders display snapshots with formatting and theme only', () => {
    const document = new FakeDocument();
    const root = document.createElement('div') as unknown as HTMLElement;
    const snapshot: OBSDisplaySnapshot = {
      accountId: 41,
      liveSessionId: 70,
      revision: 2,
	  runtime: { attributeValues: { hp: 1234.5, timer: 61 }, giftTargetReceived: [], activities: [], ruleLimits: { localDate: '2026-08-17', appliedCounts: {} } },
      viewers: [{ uid: 1, name: '观众', avatar: '', gifts: 3, giftCoin: 30 }],
    };
    renderOBSSnapshot(root, snapshot, { theme: 'glass', viewportWidth: 1280, viewportHeight: 720 });
    const rendered = textContent(root as unknown as FakeElement);
    expect(rendered).toContain('hp');
    expect(rendered).toContain('1,234.5');
    expect(rendered).toContain('观众');
    expect((root as unknown as FakeElement).attributes.get('data-theme')).toBe('glass');
  });

  it('rejects malformed nested runtime and effect DTOs', () => {
	const valid = {
	  accountId: 41,
	  liveSessionId: 70,
	  revision: 2,
	  runtime: { attributeValues: { hp: 10 }, giftTargetReceived: [], activities: [], ruleLimits: { localDate: '2026-08-17', appliedCounts: {} } },
	  effects: [],
	  viewers: [],
	};
	expect(parseOBSDisplaySnapshot(valid)).toBeDefined();
	for (const malformed of [
	  { ...valid, runtime: { ...valid.runtime, giftTargetReceived: [{ panelId: 5, giftId: 1, received: 2 }] } },
	  { ...valid, runtime: { ...valid.runtime, activities: [{ id: 'a', status: 'active', milestones: [{ id: 7 }] }] } },
	  { ...valid, runtime: { ...valid.runtime, ruleLimits: { localDate: '2026-08-17', appliedCounts: { rule: 'many' } } } },
	  { ...valid, effects: [{ delta: 1, valueAfter: 2, unexpected: true }] },
	]) expect(parseOBSDisplaySnapshot(malformed)).toBeUndefined();
  });

  it('applies the migrated appearance selected for each Hosted OBS output', () => {
    const document = new FakeDocument();
    const snapshot: OBSDisplaySnapshot = {
      accountId: 41,
      liveSessionId: 70,
      revision: 2,
      runtime: { attributeValues: { health: 42 }, giftTargetReceived: [], activities: [], ruleLimits: { localDate: '2026-08-29', appliedCounts: {} } },
      presentation: {
        appearance: { theme: 'light', fontSize: 36, accentColor: '#3366ff', align: 'left', panelOpacity: 72, showConnection: false },
        attributeAppearances: { health: { themeId: 'neon', fontSize: 40, accentColor: '#ff3366', showConnection: true, align: 'center', panelOpacity: 80 } },
        sceneAppearances: { 'main-scene': { themeId: 'pixel', fontSize: 44, accentColor: '#00cc88', showConnection: false, align: 'right', panelOpacity: 66 } },
        giftTargetAppearances: { 'gift-goal': { themeId: 'minimal', fontSize: 30, accentColor: '#ffaa00', showConnection: false, align: 'left', panelOpacity: 70 } },
        blindBoxDisplay: { themeId: 'kawaii', fontSize: 32, accentColor: '#cc55ff', showConnection: true, align: 'center', panelOpacity: 75 },
      },
    };
    expect(parseOBSDisplaySnapshot(snapshot)).toBeDefined();

    const sceneRoot = document.createElement('div') as unknown as HTMLElement;
    renderOBSSnapshot(sceneRoot, snapshot, { output: { kind: 'scene', id: 'main-scene', attributeIds: ['health'] } });
    const scene = sceneRoot as unknown as FakeElement;
    expect(scene.attributes.get('data-theme')).toBe('pixel');
    expect(scene.attributes.get('data-align')).toBe('right');
    expect(scene.attributes.get('style:--obs-theme-accent')).toBe('#00cc88');
    expect(scene.attributes.get('style:--obs-font-size')).toBe('44px');
    expect(scene.attributes.get('style:--obs-panel-opacity')).toBe('66%');
    expect(textContent(scene)).not.toContain('连接正常');

    const attributeRoot = document.createElement('div') as unknown as HTMLElement;
    renderOBSSnapshot(attributeRoot, snapshot, { output: { kind: 'attribute', id: 'health', attributeIds: ['health'] } });
    expect((attributeRoot as unknown as FakeElement).attributes.get('data-theme')).toBe('neon');
    expect(textContent(attributeRoot as unknown as FakeElement)).toContain('连接正常');

  });

  it('clears migrated appearance when a later snapshot no longer carries presentation', () => {
    const document = new FakeDocument();
    const root = document.createElement('div') as unknown as HTMLElement;
    const runtime = { attributeValues: { health: 42 }, giftTargetReceived: [], activities: [], ruleLimits: { localDate: '2026-08-29', appliedCounts: {} } };
    renderOBSSnapshot(root, {
      accountId: 41,
      liveSessionId: 70,
      revision: 2,
      runtime,
      presentation: {
        appearance: { theme: 'light', fontSize: 36, accentColor: '#3366ff', align: 'right', panelOpacity: 72, showConnection: false },
      },
    });
    const rendered = root as unknown as FakeElement;
    expect(rendered.attributes.get('data-color-scheme')).toBe('light');
    expect(rendered.attributes.get('style:--obs-font-size')).toBe('36px');

    renderOBSSnapshot(root, { accountId: 41, liveSessionId: 70, revision: 3, runtime });

    expect(rendered.attributes.has('data-color-scheme')).toBe(false);
    expect(rendered.attributes.has('data-align')).toBe(false);
    expect(rendered.attributes.has('style:--obs-font-size')).toBe(false);
    expect(rendered.attributes.has('style:--obs-panel-opacity')).toBe(false);
    expect(rendered.attributes.has('style:--obs-align')).toBe(false);
  });

  it('has a visible light color-scheme contract for migrated global appearance', () => {
    const css = readFileSync(join(projectRoot, 'src/hosted/obs/obs.css'), 'utf8');

    expect(css).toMatch(/#hosted-obs-app\[data-color-scheme="light"\]/);
    expect(css).toMatch(/--obs-text-color:\s*#172033/);
    expect(css).toMatch(/--obs-card-surface:\s*#f7f9fc/);
  });

  it('filters attribute, scene, and gift-target selectors to their intended output', () => {
    const document = new FakeDocument();
    const snapshot: OBSDisplaySnapshot = { accountId: 41, liveSessionId: 70, revision: 2, runtime: { attributeValues: { hp: 10, timer: 20 }, giftTargetReceived: [{ panelId: 'goals', giftId: 1, received: 3 }, { panelId: 'other', giftId: 2, received: 9 }], activities: [], ruleLimits: { localDate: '', appliedCounts: {} } } };
    for (const [output, included, excluded] of [
      [{ kind: 'attribute' as const, id: 'hp', attributeIds: ['hp'] }, 'hp', 'timer'],
      [{ kind: 'scene' as const, id: 'main', attributeIds: ['timer'] }, 'timer', 'hp'],
      [{ kind: 'gift-target' as const, id: 'goals', attributeIds: [] }, '礼物 1', '礼物 2'],
    ] as const) {
      const root = document.createElement('div') as unknown as HTMLElement;
      renderOBSSnapshot(root, snapshot, { output });
      const rendered = textContent(root as unknown as FakeElement);
      expect(rendered).toContain(included); expect(rendered).not.toContain(excluded);
    }
  });

  it.each([
    [1920, 1080],
    [1280, 720],
  ])('keeps the %d×%d fixture within its horizontal width budget', (width, height) => {
    const layout = computeOBSLayout(width, height, 8);
    expect(layout.contentWidth).toBeLessThanOrEqual(width);
    expect(layout.columns).toBeGreaterThan(0);
    expect(layout.cardWidth).toBeGreaterThan(0);
    expect(layout.columns * layout.cardWidth + (layout.columns - 1) * layout.gap + layout.gutter * 2).toBeLessThanOrEqual(width);
  });

  it('imports no desktop, admin, migration, or configuration modules', () => {
    const source = [
      'src/hosted/obs/main.ts',
      'src/hosted/obs/view.ts',
    ].map((file) => readFileSync(join(projectRoot, file), 'utf8')).join('\n');
    expect(source).not.toMatch(/gift-clip|autoUpdate|electron|ffmpeg|hosted\/admin|hosted\/migration|hosted\/configuration|\.\.\/admin|\.\.\/migration|\.\.\/configuration/i);
    expect(source).not.toMatch(/from ['"]\.\.\/api['"]/);
    expect(source).not.toMatch(/localStorage|sessionStorage|document\.cookie/i);
  });
});

class FakeElement {
  readonly children: FakeElement[] = [];
  readonly attributes = new Map<string, string>();
  readonly style = {
    setProperty: (name: string, value: string) => { this.attributes.set(`style:${name}`, value); },
    removeProperty: (name: string) => { this.attributes.delete(`style:${name}`); },
  };
  className = '';
  textContent = '';
  constructor(readonly tagName: string, readonly ownerDocument: FakeDocument) {}
  append(...children: FakeElement[]): void { this.children.push(...children); }
  replaceChildren(...children: FakeElement[]): void { this.children.splice(0, this.children.length, ...children); }
  setAttribute(name: string, value: string): void { this.attributes.set(name, value); }
  removeAttribute(name: string): void { this.attributes.delete(name); }
}

class FakeDocument {
  createElement(tagName: string): FakeElement { return new FakeElement(tagName, this); }
}

function textContent(element: FakeElement): string {
  return [element.textContent, ...element.children.map(textContent)].join(' ');
}
