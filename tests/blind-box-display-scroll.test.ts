import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { advanceBlindBoxScroll, mountBlindBoxDisplay } from '../src/ui/display/blind-box-display';
import type { BlindBoxLeaderboardSnapshot } from '../src/backend';
import type { BlindBoxLeaderboardResource } from '../src/blind-box-leaderboard-resource';
import { defaultState, resetState, saveState } from '../src/storage';

vi.mock('../src/ui/brand', () => ({
  createBrandIcon: (_size = 40, className = 'brand-icon') => {
    const icon = document.createElement('svg');
    icon.className = className;
    return icon;
  },
}));

class TestElement {
  className = '';
  dataset: Record<string, string> = {};
  children: TestElement[] = [];
  parentElement: TestElement | null = null;
  textContent = '';
  style: Record<string, string | ((name: string, value: string) => void)> = {
    setProperty: (name: string, value: string) => { this.style[name] = value; },
  };
  classList = {
    add: (...names: string[]) => { this.className = [...new Set([...this.className.split(' ').filter(Boolean), ...names])].join(' '); },
    remove: (...names: string[]) => { this.className = this.className.split(' ').filter((name) => !names.includes(name)).join(' '); },
    toggle: (name: string, force?: boolean) => {
      const enabled = force ?? !this.className.split(' ').includes(name);
      if (enabled) this.classList.add(name); else this.classList.remove(name);
      return enabled;
    },
  };

  constructor(readonly tagName: string) {}

  append(...children: (TestElement | string)[]): void {
    for (const child of children) {
      if (typeof child === 'string') continue;
      child.parentElement = this;
      this.children.push(child);
    }
  }

  replaceChildren(...children: TestElement[]): void {
    this.children = [];
    this.append(...children);
  }

  setAttribute(): void {}

  querySelector(selector: string): TestElement | null {
    return this.querySelectorAll(selector)[0] ?? null;
  }

  querySelectorAll(selector: string): TestElement[] {
    const result: TestElement[] = [];
    const matches = (element: TestElement) => selector.startsWith('.')
      ? element.className.split(' ').includes(selector.slice(1))
      : element.tagName === selector;
    const visit = (element: TestElement): void => {
      for (const child of element.children) {
        if (matches(child)) result.push(child);
        visit(child);
      }
    };
    visit(this);
    return result;
  }
}

function textOf(element: TestElement): string {
  return element.textContent + element.children.map(textOf).join('');
}

function snapshot(name = '服务端盲盒', viewerName = '服务端观众', updatedAt = 10): BlindBoxLeaderboardSnapshot {
  return {
    updatedAt,
    summary: { viewerCount: 1, blindBoxCount: 2, cost: 18_000, value: 24_000, profit: 6_000, unpricedCount: 0 },
    viewers: [{
      key: `uid:${viewerName}`, uid: 2, uname: viewerName, avatar: '', giftCount: 2, goldValue: 18_000, silverValue: 0,
      ruleTriggers: 0, attributeDeltas: {}, blindBoxCount: 2, blindBoxCost: 18_000, blindBoxValue: 24_000,
      blindBoxProfit: 6_000, lastGiftAt: 10,
    }],
    scopes: [{ giftId: 35800, giftName: name, count: 2, lastGiftAt: 10 }],
  };
}

function resourceWith(next: BlindBoxLeaderboardSnapshot): BlindBoxLeaderboardResource {
  return {
    refresh: vi.fn(async () => ({ status: 'applied' as const, snapshot: next })),
    current: vi.fn(() => next),
    clear: vi.fn(),
    cancel: vi.fn(),
  };
}

function deferred<T>(): { promise: Promise<T>; resolve(value: T): void } {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((finish) => { resolve = finish; });
  return { promise, resolve };
}

async function flush(): Promise<void> {
  await Promise.resolve();
  await Promise.resolve();
}

beforeEach(async () => {
  vi.stubGlobal('document', { createElement: (tag: string) => new TestElement(tag) } as unknown as Document);
  vi.stubGlobal('fetch', vi.fn(async (input: string | URL | Request) => {
    if (String(input).includes('/api/runtime')) return Response.json({ code: 0, runtime: { state: 'connected', roomId: '' } });
    return new Response(null, { status: 204 });
  }));
  await resetState();
});

afterEach(() => {
  vi.useRealTimers();
  vi.unstubAllGlobals();
});

describe('blind-box leaderboard ping-pong scrolling', () => {
  it('walks to the bottom and reverses back to the top', () => {
    let position = { index: 0, direction: 1 as 1 | -1 };
    const visited = [position.index];
    for (let step = 0; step < 6; step += 1) {
      position = advanceBlindBoxScroll(position.index, position.direction, 3);
      visited.push(position.index);
    }
    expect(visited).toEqual([0, 1, 2, 3, 2, 1, 0]);
    expect(position.direction).toBe(1);
  });

  it('stays still when every viewer already fits in the viewport', () => {
    expect(advanceBlindBoxScroll(0, -1, 0)).toEqual({ index: 0, direction: 1 });
  });
});

describe('blind-box display authoritative snapshots', () => {
  it('renders the backend snapshot instead of contradictory local contribution rows', async () => {
    vi.useFakeTimers();
    const local = defaultState();
    local.contributions = {
      updatedAt: 1,
      viewers: [{
        key: 'uid:local', uid: 1, uname: '本地伪造观众', avatar: '', giftCount: 99, goldValue: 999_000, silverValue: 0,
        ruleTriggers: 0, attributeDeltas: {}, blindBoxCount: 99, blindBoxCost: 999_000, blindBoxValue: 0,
        blindBoxProfit: -999_000, lastGiftAt: 1,
      }],
    };
    await saveState(local);
    const fakeResource = resourceWith(snapshot());
    const root = new TestElement('div');

    mountBlindBoxDisplay(root as unknown as HTMLElement, 35800, {
      createLeaderboardResource: () => fakeResource,
    });
    await vi.advanceTimersByTimeAsync(750);

    expect(textOf(root)).toContain('服务端盲盒');
    expect(textOf(root)).toContain('服务端观众');
    expect(textOf(root)).not.toContain('本地伪造观众');
  });

  it('keeps successful rows on refresh failure and clears the error after a later success', async () => {
    vi.useFakeTimers();
    const first = snapshot('第一批服务端盲盒', '第一位服务端观众', 10);
    const second = snapshot('第二批服务端盲盒', '第二位服务端观众', 20);
    const resource: BlindBoxLeaderboardResource = {
      refresh: vi.fn()
        .mockResolvedValueOnce({ status: 'applied', snapshot: first })
        .mockResolvedValueOnce({ status: 'failed', error: new Error('offline'), snapshot: first })
        .mockResolvedValueOnce({ status: 'applied', snapshot: second }),
      current: vi.fn(() => first), clear: vi.fn(), cancel: vi.fn(),
    };
    const root = new TestElement('div');

    mountBlindBoxDisplay(root as unknown as HTMLElement, 35800, { createLeaderboardResource: () => resource });
    await vi.advanceTimersByTimeAsync(1);
    expect(textOf(root)).toContain('第一位服务端观众');
    expect(root.querySelector('.conn')?.className).toContain('connected');

    await vi.advanceTimersByTimeAsync(750);
    expect(textOf(root)).toContain('第一位服务端观众');
    expect(root.querySelector('.conn')?.className).toContain('error');

    await vi.advanceTimersByTimeAsync(750);
    expect(textOf(root)).toContain('第二位服务端观众');
    expect(root.querySelector('.conn')?.className).toContain('connected');
  });

  it('ignores a late stale refresh after the resource has moved to newer data', async () => {
    vi.useFakeTimers();
    const late = deferred<{ status: 'stale' }>();
    const current = snapshot('当前 scope', '当前服务端观众', 20);
    const resource: BlindBoxLeaderboardResource = {
      refresh: vi.fn()
        .mockImplementationOnce(() => late.promise)
        .mockResolvedValueOnce({ status: 'applied', snapshot: current }),
      current: vi.fn(() => current), clear: vi.fn(), cancel: vi.fn(),
    };
    const root = new TestElement('div');

    mountBlindBoxDisplay(root as unknown as HTMLElement, 35800, { createLeaderboardResource: () => resource });
    await vi.advanceTimersByTimeAsync(750);
    late.resolve({ status: 'stale' });
    await flush();

    expect(textOf(root)).toContain('当前服务端观众');
  });

  it('cancels the resource and scroll timers on beforeunload', async () => {
    vi.useFakeTimers();
    let beforeUnload: (() => void) | undefined;
    vi.stubGlobal('addEventListener', vi.fn((event: string, listener: () => void) => {
      if (event === 'beforeunload') beforeUnload = listener;
    }));
    const resource = resourceWith(snapshot());
    const root = new TestElement('div');

    mountBlindBoxDisplay(root as unknown as HTMLElement, 35800, { createLeaderboardResource: () => resource });
    await flush();
    beforeUnload?.();

    expect(resource.cancel).toHaveBeenCalledOnce();
    expect(vi.getTimerCount()).toBe(0);
  });
});
