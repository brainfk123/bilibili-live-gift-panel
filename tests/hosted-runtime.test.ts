import { describe, expect, it } from 'vitest';
import { readFileSync } from 'node:fs';

import { HostedAPI } from '../src/hosted/api';
import { validHostedRoomID } from '../src/hosted/room-id';
import { mountRoomControls, validRuntimeRoomID, type RoomRuntimePresence } from '../src/hosted/room';
import { createHostedApplicationLifecycle, createHostedRuntimePresence, type HostedRuntimeViewState, type RuntimeEventSourceLike } from '../src/hosted/runtime';

describe('hosted runtime presence', () => {
  it('opens one config SSE and reports pending, connected, degraded, and jittered reconnecting states', () => {
    const sources: FakeEventSource[] = [];
    const timers: Array<{ callback: () => void; delay: number; cleared: boolean }> = [];
    const states: HostedRuntimeViewState[] = [];
    const presence = createHostedRuntimePresence({
      createEventSource: (path) => {
        expect(path).toBe('/api/runtime/events');
        const source = new FakeEventSource();
        sources.push(source);
        return source;
      },
      setTimer: (callback, delay) => {
        const timer = { callback, delay, cleared: false };
        timers.push(timer);
        return timer;
      },
      clearTimer: (timer) => { (timer as { cleared: boolean }).cleared = true; },
      random: () => 0,
    });
    const unsubscribe = presence.subscribe((state) => states.push(state));

    expect(sources).toHaveLength(1);
    expect(states.at(-1)).toEqual({ connection: 'pending' });
    sources[0]!.emit('status', {
      state: 'active', roomId: '42', sessionId: 81, leases: 1, configLease: true,
      obsLease: false, degraded: false, connectionHealthy: true,
    });
    expect(states.at(-1)).toEqual({ connection: 'connected', roomID: '42', runtimeState: 'active' });
    sources[0]!.emit('degraded', { degraded: true });
    expect(states.at(-1)).toEqual({ connection: 'degraded', roomID: '42', runtimeState: 'active' });

    sources[0]!.fail();
    expect(sources[0]!.closed).toBe(true);
    expect(states.at(-1)).toEqual({ connection: 'reconnecting', roomID: '42', runtimeState: 'active' });
    expect(timers).toHaveLength(1);
    expect(timers[0]!.delay).toBe(800);
    timers[0]!.callback();
    expect(sources).toHaveLength(2);
    sources[1]!.emit('status', {
      state: 'active', roomId: '42', sessionId: 82, leases: 1, configLease: true,
      obsLease: false, degraded: false, connectionHealthy: true,
    });
    expect(states.at(-1)).toEqual({ connection: 'connected', roomID: '42', runtimeState: 'active' });

    unsubscribe();
    presence.dispose();
    expect(sources[1]!.closed).toBe(true);
  });

  it('does not create a config SSE when pagehide wins a pending session continuation', async () => {
    const lifecycle = createHostedApplicationLifecycle();
    const sources: FakeEventSource[] = [];
    let resolveSession: (() => void) | undefined;
    const session = new Promise<void>((resolve) => { resolveSession = resolve; });
    void session.then(() => lifecycle.run(() => {
      createHostedRuntimePresence({
        createEventSource: () => {
          const source = new FakeEventSource();
          sources.push(source);
          return source;
        },
        setTimer: () => 1,
        clearTimer: () => undefined,
        random: () => 0,
      });
    }));

    lifecycle.dispose();
    resolveSession?.();
    await session;
    await Promise.resolve();

    expect(sources).toEqual([]);
  });
});

describe('hosted room selection', () => {
  it('shares one uint64 room validator across UI, API, and SSE boundaries', () => {
    const cases = [
      ['0', false],
      ['042', false],
      ['18446744073709551615', true],
      ['18446744073709551616', false],
    ] as const;
    for (const [roomID, valid] of cases) {
      expect(validHostedRoomID(roomID), roomID).toBe(valid);
      expect(validRuntimeRoomID(roomID), roomID).toBe(valid);
      let source: FakeEventSource | undefined;
      const presence = createHostedRuntimePresence({
        createEventSource: () => { source = new FakeEventSource(); return source; },
        setTimer: () => 1,
        clearTimer: () => undefined,
        random: () => 0,
      });
      source!.emit('status', {
        state: 'active', roomId: roomID, sessionId: 1, leases: 1,
        configLease: true, obsLease: false, degraded: false, connectionHealthy: true,
      });
      expect(presence.state().connection, `SSE ${roomID}`).toBe(valid ? 'connected' : 'reconnecting');
      presence.dispose();
    }
    for (const file of ['api.ts', 'room.ts', 'runtime.ts']) {
      const source = readFileSync(new URL(`../src/hosted/${file}`, import.meta.url), 'utf8');
      expect(source, file).toContain("from './room-id'");
      expect(source, file).not.toMatch(/BigInt\(|18_446_744_073_709_551_615n/);
    }
  });

  it('accepts an arbitrary valid room, confirms switches, and exposes no manual start or stop action', async () => {
    const document = new FakeDocument();
    const root = document.createElement('div') as unknown as HTMLElement;
    const calls: string[] = [];
    const presence = new FakeRoomPresence({ connection: 'connected', roomID: '42', runtimeState: 'active' });
    let confirmed = false;
    const view = mountRoomControls(root, {
      setRuntimeRoom: async (roomID) => { calls.push(roomID); },
    }, presence, { confirm: () => confirmed });
    const input = find(root, 'input');
    const button = find(root, 'button');
    input.value = '18446744073709551615';

    button.click();
    await Promise.resolve();
    expect(calls).toEqual([]);
    confirmed = true;
    button.click();
    await Promise.resolve();
    await Promise.resolve();
    expect(calls).toEqual(['18446744073709551615']);
    expect(presence.refreshCalls).toBe(1);
    expect(textContent(root)).toContain('在线配置页和 OBS 展示页都离线 10 分钟后');
    expect(all(root, 'button').map((item) => item.textContent).join(' ')).not.toMatch(/start|stop|启动|停止/i);

    for (const [state, label] of [
      [{ connection: 'pending' as const }, '正在连接'],
      [{ connection: 'connected' as const, roomID: '42', runtimeState: 'active' }, '已连接'],
      [{ connection: 'degraded' as const, roomID: '42', runtimeState: 'degraded' }, '服务降级'],
      [{ connection: 'reconnecting' as const, roomID: '42', runtimeState: 'active' }, '正在重连'],
    ] as const) {
      presence.emit(state);
      expect(textContent(root)).toContain(label);
    }
    view.dispose();
  });

  it('uses only the exact runtime room PUT request', async () => {
    const requests: Array<{ path: string; init?: RequestInit }> = [];
    const api = await HostedAPI.connect(async (input, init) => {
      const path = String(input);
      requests.push({ path, init });
      if (path === '/api/bootstrap') return Response.json({ csrfToken: 'csrf' });
      return new Response(null, { status: 204 });
    });

    await api.setRuntimeRoom('18446744073709551615');

    expect(requests[1]).toEqual({
      path: '/api/runtime/room',
      init: {
        method: 'PUT', credentials: 'same-origin',
        headers: { Accept: 'application/json', 'X-CSRF-Token': 'csrf', 'Content-Type': 'application/json' },
        body: JSON.stringify({ roomId: '18446744073709551615' }),
      },
    });
    expect(requests.map((request) => request.path).join(' ')).not.toMatch(/start|stop/i);

    for (const invalid of ['0', '042', '18446744073709551616']) {
      await expect(api.setRuntimeRoom(invalid)).rejects.toMatchObject({ code: 'invalid_request' });
    }
    expect(requests).toHaveLength(2);
  });

  it('blocks a cold-start room mutation until authoritative status can require confirmation', async () => {
    const document = new FakeDocument();
    const root = document.createElement('div') as unknown as HTMLElement;
    const calls: string[] = [];
    const presence = new FakeRoomPresence({ connection: 'pending' });
    let confirms = 0;
    mountRoomControls(root, { setRuntimeRoom: async (roomID) => { calls.push(roomID); } }, presence, {
      confirm: () => { confirms += 1; return false; },
    });
    const input = find(root, 'input');
    const button = find(root, 'button');
    input.value = '84';

    button.click();
    await Promise.resolve();
    expect(calls).toEqual([]);
    expect(button.disabled).toBe(true);

    presence.emit({ connection: 'connected', roomID: '42', runtimeState: 'active' });
    expect(button.disabled).toBe(false);
    button.click();
    await Promise.resolve();
    expect(confirms).toBe(1);
    expect(calls).toEqual([]);
  });

  it('disables stale room mutation while reconnecting and confirms against the fresh room', async () => {
    const document = new FakeDocument();
    const root = document.createElement('div') as unknown as HTMLElement;
    const calls: string[] = [];
    const confirmations: string[] = [];
    const presence = new FakeRoomPresence({ connection: 'connected', roomID: '42', runtimeState: 'active' });
    mountRoomControls(root, { setRuntimeRoom: async (roomID) => { calls.push(roomID); } }, presence, {
      confirm: (message) => { confirmations.push(message); return false; },
    });
    const input = find(root, 'input');
    const button = find(root, 'button');
    input.value = '84';

    presence.emit({ connection: 'reconnecting', roomID: '42', runtimeState: 'active' });
    expect(button.disabled).toBe(true);
    button.click();
    await Promise.resolve();
    expect(confirmations).toEqual([]);
    expect(calls).toEqual([]);

    presence.emit({ connection: 'connected', roomID: '43', runtimeState: 'active' });
    expect(button.disabled).toBe(false);
    button.click();
    await Promise.resolve();
    expect(confirmations).toEqual(['确认从直播间 43 切换到 84？']);
    expect(calls).toEqual([]);
  });

  it('renders authoritative idle, disabled, and shutdown states without claiming an active connection', () => {
    const document = new FakeDocument();
    const root = document.createElement('div') as unknown as HTMLElement;
    const presence = new FakeRoomPresence({ connection: 'connected', runtimeState: 'idle' });
    mountRoomControls(root, { setRuntimeRoom: async () => undefined }, presence);

    expect(textContent(root)).toContain('等待选择直播间');
    presence.emit({ connection: 'degraded', runtimeState: 'disabled' });
    expect(textContent(root)).toContain('账号已停用');
    presence.emit({ connection: 'connected', runtimeState: 'shutting_down' });
    expect(textContent(root)).toContain('服务正在关闭');
    expect(textContent(root)).not.toContain('运行状态：已连接');
  });

  it('routes invitation logout through the guarded signed-out transition', () => {
    const main = readFileSync(new URL('../src/hosted/main.ts', import.meta.url), 'utf8');
    expect(main).toContain('const requested = ++sessionRequestGeneration;');
    expect(main).toContain('if (requested === sessionRequestGeneration) applicationLifecycle.run(() => showAccount(api, accountScope));');
    expect(main).toContain('sessionRequestGeneration += 1;');
    expect(main).toContain('authenticatedAccountScope = undefined;');
    expect(main).toMatch(/mountInvitationView\(root, api, undefined, \(\) => returnToAccount\(api\), \(\) => returnToSignedOut\(api\)\)/);
    expect(main).toMatch(/mountInvitationView\(root, api, intent, \(\) => returnToAccount\(api\)\)/);
    expect(main).not.toContain('onSignedIn: () => showAccount(api)');
    expect(main).toContain('onExit: () => returnToSignedOut(api)');
  });
});

class FakeEventSource implements RuntimeEventSourceLike {
  readonly listeners = new Map<string, Array<(event: MessageEvent<string>) => void>>();
  onerror: ((event: Event) => void) | null = null;
  closed = false;
  addEventListener(type: string, listener: (event: MessageEvent<string>) => void): void {
    const listeners = this.listeners.get(type) ?? [];
    listeners.push(listener);
    this.listeners.set(type, listeners);
  }
  emit(type: string, value: unknown): void {
    for (const listener of this.listeners.get(type) ?? []) listener({ data: JSON.stringify(value) } as MessageEvent<string>);
  }
  fail(): void { this.onerror?.({} as Event); }
  close(): void { this.closed = true; }
}

class FakeRoomPresence implements RoomRuntimePresence {
  private listener: ((state: HostedRuntimeViewState) => void) | undefined;
  refreshCalls = 0;
  constructor(private currentState: HostedRuntimeViewState) {}
  state(): HostedRuntimeViewState { return this.currentState; }
  subscribe(listener: (state: HostedRuntimeViewState) => void): () => void {
    this.listener = listener;
    listener(this.currentState);
    return () => { this.listener = undefined; };
  }
  refresh(): void { this.refreshCalls += 1; }
  emit(state: HostedRuntimeViewState): void {
    this.currentState = state;
    this.listener?.(state);
  }
}

class FakeElement {
  readonly children: FakeElement[] = [];
  readonly attributes = new Map<string, string>();
  textContent = '';
  value = '';
  type = '';
  inputMode = '';
  disabled = false;
  private readonly listeners = new Map<string, Array<() => void>>();
  constructor(readonly tagName: string, readonly ownerDocument: FakeDocument) {}
  append(...children: FakeElement[]): void { this.children.push(...children); }
  replaceChildren(...children: FakeElement[]): void { this.children.splice(0, this.children.length, ...children); }
  setAttribute(name: string, value: string): void { this.attributes.set(name, value); }
  addEventListener(type: string, listener: () => void): void {
    const listeners = this.listeners.get(type) ?? [];
    listeners.push(listener);
    this.listeners.set(type, listeners);
  }
  click(): void { for (const listener of this.listeners.get('click') ?? []) listener(); }
}

class FakeDocument {
  readonly defaultView = undefined;
  createElement(tagName: string): FakeElement { return new FakeElement(tagName, this); }
}

function all(root: HTMLElement, tagName: string): FakeElement[] {
  const element = root as unknown as FakeElement;
  return [...(element.tagName === tagName ? [element] : []), ...element.children.flatMap((child) => all(child as unknown as HTMLElement, tagName))];
}

function find(root: HTMLElement, tagName: string): FakeElement {
  const element = all(root, tagName)[0];
  if (!element) throw new Error(`missing ${tagName}`);
  return element;
}

function textContent(root: HTMLElement): string {
  const element = root as unknown as FakeElement;
  return [element.textContent, ...element.children.map((child) => textContent(child as unknown as HTMLElement))].join(' ');
}
