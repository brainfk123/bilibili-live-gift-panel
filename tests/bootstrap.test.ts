import { describe, expect, it, vi } from 'vitest';
import { startApp } from '../src/runtime/bootstrap';

class FakeClassList {
  private readonly values = new Set<string>();

  add(...names: string[]): void {
    names.forEach((name) => this.values.add(name));
  }

  contains(name: string): boolean {
    return this.values.has(name);
  }
}

class FakeElement {
  readonly classList = new FakeClassList();
  readonly dataset: Record<string, string> = {};
  readonly children: FakeElement[] = [];
  id = '';
  textContent = '';

  constructor(readonly tagName: string) {}

  append(...children: FakeElement[]): void {
    this.children.push(...children);
  }
}

class FakeDocument {
  readonly body = new FakeElement('body');
  readonly head = new FakeElement('head');
  readonly app = new FakeElement('div');

  createElement(tagName: string): FakeElement {
    return new FakeElement(tagName);
  }

  getElementById(id: string): FakeElement | null {
    return id === 'app' ? this.app : this.head.children.find((child) => child.id === id) ?? null;
  }
}

describe('application bootstrap', () => {
  it('does not load or create configuration styles in display mode', async () => {
    const document = new FakeDocument();
    const loadConfigStyles = vi.fn(async () => '.config-root { color: red; }');
    const mountDisplay = vi.fn();
    const mountConfig = vi.fn();

    await startApp({
      document: document as unknown as Document,
      search: '?mode=display',
      installFavicon: vi.fn(),
      loadConfigStyles,
      mountDisplay,
      mountConfig,
    });

    expect(loadConfigStyles).not.toHaveBeenCalled();
    expect(document.head.children.filter((child) => child.tagName === 'style')).toHaveLength(0);
    expect(mountDisplay).toHaveBeenCalledWith(document.app, undefined);
    expect(mountConfig).not.toHaveBeenCalled();
    expect(document.body.classList.contains('display-mode')).toBe(true);
  });

  it('passes the selected attribute to display mode', async () => {
    const document = new FakeDocument();
    const mountDisplay = vi.fn();

    await startApp({
      document: document as unknown as Document,
      search: '?mode=display&attribute=%E5%8A%A0%E7%8F%AD%E6%97%B6%E9%97%B4',
      installFavicon: vi.fn(),
      loadConfigStyles: vi.fn(async () => ''),
      mountDisplay,
      mountConfig: vi.fn(),
    });

    expect(mountDisplay).toHaveBeenCalledWith(document.app, { kind: 'attribute', attributeName: '加班时间' });
  });

  it('passes a selected combination scene to display mode', async () => {
    const document = new FakeDocument();
    const mountDisplay = vi.fn();

    await startApp({
      document: document as unknown as Document,
      search: '?mode=display&scene=scene-boss',
      installFavicon: vi.fn(),
      loadConfigStyles: vi.fn(async () => ''),
      mountDisplay,
      mountConfig: vi.fn(),
    });

    expect(mountDisplay).toHaveBeenCalledWith(document.app, { kind: 'scene', sceneId: 'scene-boss' });
  });

  it('passes the blind-box leaderboard view to display mode', async () => {
    const document = new FakeDocument();
    const mountDisplay = vi.fn();

    await startApp({
      document: document as unknown as Document,
      search: '?mode=display&view=blind-box',
      installFavicon: vi.fn(),
      loadConfigStyles: vi.fn(async () => ''),
      mountDisplay,
      mountConfig: vi.fn(),
    });

    expect(mountDisplay).toHaveBeenCalledWith(document.app, { kind: 'blind-box' });
  });

  it('passes the selected blind-box gift to display mode', async () => {
    const document = new FakeDocument();
    const mountDisplay = vi.fn();

    await startApp({
      document: document as unknown as Document,
      search: '?mode=display&view=blind-box&blindBox=35800',
      installFavicon: vi.fn(),
      loadConfigStyles: vi.fn(async () => ''),
      mountDisplay,
      mountConfig: vi.fn(),
    });

    expect(mountDisplay).toHaveBeenCalledWith(document.app, { kind: 'blind-box', blindBoxGiftId: 35800 });
  });

  it('passes a selected gift KPI panel to display mode', async () => {
    const document = new FakeDocument();
    const mountDisplay = vi.fn();

    await startApp({
      document: document as unknown as Document,
      search: '?mode=display&view=gift-kpi&panel=kpi-1',
      installFavicon: vi.fn(),
      loadConfigStyles: vi.fn(async () => ''),
      mountDisplay,
      mountConfig: vi.fn(),
    });

    expect(mountDisplay).toHaveBeenCalledWith(document.app, { kind: 'gift-target', panelId: 'kpi-1' });
  });

  it('injects configuration styles before mounting config mode', async () => {
    const document = new FakeDocument();
    const cssText = '.config-root { color: red; }';
    const loadConfigStyles = vi.fn(async () => cssText);
    const mountConfig = vi.fn(() => {
      expect(document.head.children.filter((child) => child.tagName === 'style')).toHaveLength(1);
    });

    await startApp({
      document: document as unknown as Document,
      search: '?mode=config',
      installFavicon: vi.fn(),
      loadConfigStyles,
      mountDisplay: vi.fn(),
      mountConfig,
    });

    const style = document.head.children.find((child) => child.tagName === 'style');
    expect(loadConfigStyles).toHaveBeenCalledOnce();
    expect(style?.id).toBe('bilibili-config-style');
    expect(style?.textContent).toBe(cssText);
    expect(style?.dataset.mode).toBe('config');
    expect(mountConfig).toHaveBeenCalledWith(document.app);
    expect(document.app.classList.contains('config-root')).toBe(true);
    expect(document.body.classList.contains('config-mode')).toBe(true);
  });
});
