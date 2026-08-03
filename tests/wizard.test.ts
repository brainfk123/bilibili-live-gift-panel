import { beforeEach, describe, expect, it, vi } from 'vitest';
import { readFileSync } from 'node:fs';
import { mountConfig } from '../src/ui/config/config';
import { formatDelta, mountDisplay } from '../src/ui/display/display';
import { getNextWizardStep, getRoomNumberHint, getTutorialStep, getWizardChecklist, getWizardProgress } from '../src/ui/config/wizard';
import { defaultState, loadState, resetState, saveState } from '../src/storage';
import { builtinCatalog } from '../src/gifts/catalog';
import type { GiftEvent } from '../src/bilibili/messages';

vi.mock('../src/ui/brand', () => ({
  createBrandIcon: (size = 40, className = 'brand-icon') => {
    const icon = document.createElement('svg');
    icon.setAttribute('width', String(size));
    icon.setAttribute('height', String(size));
    icon.setAttribute('class', className);
    return icon;
  },
}));

const mockedClients = vi.hoisted(() => [] as Array<{
  options: { onState?: (state: string) => void; onGift?: (event: GiftEvent) => void };
  stop: ReturnType<typeof vi.fn>;
}>);

let mockedRuntimeState: 'idle' | 'connecting' | 'connected' | 'reconnecting' | 'error' = 'idle';

vi.mock('../src/bilibili/client', () => ({
  DanmakuClient: class {
    constructor(readonly options: { onState?: (state: string) => void; onGift?: (event: GiftEvent) => void }) {
      mockedClients.push(this);
    }

    stop = vi.fn();

    start(): Promise<void> {
      return Promise.resolve();
    }
  },
}));

class TestElement {
  className = '';
  dataset: Record<string, string> = {};
  children: TestElement[] = [];
  parent: TestElement | null = null;
  textContent = '';
  value = '';
  innerHTML = '';
  placeholder = '';
  type = '';
  readOnly = false;
  selectedIndex = 0;
  onclick: (() => void) | null = null;
  style: {
    [name: string]: string | ((property: string, value: string) => void);
    setProperty: (name: string, value: string) => void;
  };
  attributes: Record<string, string> = {};
  classList = {
    add: (...names: string[]) => {
      const classes = new Set(this.className.split(' ').filter(Boolean));
      names.forEach((name) => classes.add(name));
      this.className = [...classes].join(' ');
    },
    remove: (...names: string[]) => {
      const classes = new Set(this.className.split(' ').filter(Boolean));
      names.forEach((name) => classes.delete(name));
      this.className = [...classes].join(' ');
    },
    toggle: (name: string, force?: boolean) => {
      const has = this.className.split(' ').includes(name);
      const next = force ?? !has;
      if (next) this.classList.add(name);
      else this.classList.remove(name);
      return next;
    },
  };

  select(): void {}

  focus(): void {}

  constructor(readonly tagName: string) {
    this.style = {
      setProperty: (name: string, value: string) => {
        this.style[name] = value;
      },
    };
  }

  append(...children: (TestElement | string)[]): void {
    for (const child of children) {
      if (typeof child === 'string') continue;
      child.parent = this;
      this.children.push(child);
    }
  }

  replaceChildren(...children: TestElement[]): void {
    this.children = [];
    this.append(...children);
  }

  replaceWith(next: TestElement): void {
    if (!this.parent) return;
    const index = this.parent.children.indexOf(this);
    if (index < 0) return;
    next.parent = this.parent;
    this.parent.children[index] = next;
  }

  remove(): void {
    if (!this.parent) return;
    const index = this.parent.children.indexOf(this);
    if (index >= 0) this.parent.children.splice(index, 1);
    this.parent = null;
  }

  setAttribute(name: string, value: string): void {
    if (name === 'class') this.className = value;
    if (name.startsWith('data-')) this.dataset[name.slice(5)] = value;
    this.attributes[name] = value;
  }

  getAttribute(name: string): string | null {
    return this.attributes[name] ?? null;
  }

  removeAttribute(name: string): void {
    delete this.attributes[name];
  }

  querySelector(selector: string): TestElement | null {
    return this.querySelectorAll(selector)[0] ?? null;
  }

  querySelectorAll(selector: string): TestElement[] {
    const matches = (element: TestElement): boolean => {
      if (selector.startsWith('.')) return element.className.split(' ').includes(selector.slice(1));
      if (selector.startsWith('[data-step="') && selector.endsWith('"]')) {
        return element.dataset.step === selector.slice(12, -2);
      }
      return element.tagName === selector;
    };
    const found: TestElement[] = [];
    const visit = (element: TestElement): void => {
      for (const child of element.children) {
        if (matches(child)) found.push(child);
        visit(child);
      }
    };
    visit(this);
    return found;
  }
}

function allElements(root: TestElement): TestElement[] {
  return [root, ...root.children.flatMap((child) => allElements(child))];
}

function textOf(element: TestElement): string {
  return element.textContent + element.children.map((child) => textOf(child)).join('');
}

function findByText(root: TestElement, text: string): TestElement | undefined {
  return allElements(root).find((element) => element.textContent === text);
}

function cssVariable(block: string, name: string): string {
  return block.match(new RegExp(`${name}:\\s*([^;]+);`))?.[1].trim() ?? '';
}

function relativeLuminance(hex: string): number {
  const channels = [1, 3, 5].map((index) => parseInt(hex.slice(index, index + 2), 16) / 255);
  const linear = channels.map((channel) => channel <= 0.04045
    ? channel / 12.92
    : ((channel + 0.055) / 1.055) ** 2.4);
  return 0.2126 * linear[0] + 0.7152 * linear[1] + 0.0722 * linear[2];
}

function contrastRatio(first: string, second: string): number {
  const firstLuminance = relativeLuminance(first);
  const secondLuminance = relativeLuminance(second);
  const lighter = Math.max(firstLuminance, secondLuminance);
  const darker = Math.min(firstLuminance, secondLuminance);
  return (lighter + 0.05) / (darker + 0.05);
}

const storage = {
  clear: () => resetState(),
  set: (_key: string, value: string) => saveState(JSON.parse(value)),
  get: (_key: string) => JSON.stringify(loadState()),
};

beforeEach(() => {
  mockedClients.length = 0;
  mockedRuntimeState = 'idle';
  vi.stubGlobal('document', {
    createElement: (tag: string) => new TestElement(tag),
  } as unknown as Document);
  vi.stubGlobal('fetch', vi.fn(async (input: string | URL | Request, init?: RequestInit) => {
    const url = String(input);
    if (url.includes('/api/runtime')) {
      return new Response(JSON.stringify({
        code: 0,
        runtime: { state: mockedRuntimeState, roomId: mockedRuntimeState === 'idle' ? '' : '31567150' },
      }), { status: 200, headers: { 'Content-Type': 'application/json' } });
    }
    if (url.includes('/api/auth/status')) {
      return new Response(JSON.stringify({ code: 0, auth: { state: 'anonymous' } }), {
        status: 200,
        headers: { 'Content-Type': 'application/json' },
      });
    }
    if (url.includes('/api/formula/preview')) {
      const body = JSON.parse(String(init?.body ?? '{}')) as { attributeValue?: number };
      return new Response(JSON.stringify({ code: 0, result: (body.attributeValue ?? 0) + 1 }), {
        status: 200,
        headers: { 'Content-Type': 'application/json' },
      });
    }
    return new Response(null, { status: 204 });
  }));
  storage.clear();
  vi.stubGlobal('location', { origin: 'http://localhost:12450', reload: () => {} });
});

const state = (roomId = '', rules = 0) => ({
  roomId,
  attributes: [{ name: '加班时间', value: 0, unit: 'seconds', format: 'hhmmss', decimals: 0, suffix: '' }],
  rules: Array.from({ length: rules }, (_, i) => ({
    id: `r-${i}`, giftId: 1, attributeName: '加班时间', formula: '60',
  })),
} as any);

describe('wizard progress', () => {
  it('starts with no default attributes', () => {
    expect(defaultState().attributes).toEqual([]);
  });

  it('models the four tutorial phases from real interaction state', () => {
    const empty = { attributes: [], rules: [] } as any;
    const configured = state('88888888', 1);

    expect(getTutorialStep(empty, false, false)).toBe('room');
    expect(getTutorialStep(empty, true, false)).toBe('attributes');
    expect(getTutorialStep(empty, true, true)).toBe('rules');
    expect(getTutorialStep(configured, true, false)).toBe('obs');
  });

  it('starts at room setup', () => {
    expect(getWizardProgress(state())).toEqual({ room: false, attributes: true, rules: false, obs: false });
    expect(getNextWizardStep(getWizardProgress(state()))).toBe('room');
  });

  it('moves to rules after a room is configured', () => {
    const progress = getWizardProgress(state('88888888'));
    expect(progress.room).toBe(true);
    expect(getNextWizardStep(progress)).toBe('rules');
  });

  it('targets OBS for the completed OBS checklist step', () => {
    const progress = getWizardProgress(state('88888888', 1));
    expect(getWizardChecklist(progress)[3]).toEqual({ label: '在 OBS 中显示', target: 'obs', done: true });
  });

  it('uses the configured room as the first active task after room setup', () => {
    const progress = getWizardProgress(state('88888888'));
    expect(getNextWizardStep(progress)).toBe('rules');
  });

  it('is ready for OBS after a rule exists', () => {
    const progress = getWizardProgress(state('88888888', 1));
    expect(progress).toEqual({ room: true, attributes: true, rules: true, obs: true });
    expect(getNextWizardStep(progress)).toBeNull();
  });
});

describe.skip('legacy configuration wizard rendering', () => {
  it('defaults to list view and switches to grid without losing search', () => {
    const root = new TestElement('div');
    mountConfig(root as unknown as HTMLElement);
    root.querySelector('[data-step="rules"]')?.onclick?.();

    expect(root.querySelector('.gift-list')).not.toBeNull();
    const search = root.querySelector('input') as TestElement;
    search.value = '心动';
    (search as TestElement & { oninput?: () => void }).oninput?.();

    findByText(root, '网格')?.onclick?.();
    expect(root.querySelector('.gift-grid')).not.toBeNull();
    expect((root.querySelector('input') as TestElement).value).toBe('心动');
    expect(JSON.parse(storage.get('bilibili-live-gift-panel-v1')!).settings.giftView).toBe('grid');
  });

  it('migrates saved states without a gift view to list view', () => {
    storage.set('bilibili-live-gift-panel-v1', JSON.stringify({ ...state(), settings: { theme: 'dark' } }));
    const root = new TestElement('div');
    mountConfig(root as unknown as HTMLElement);
    root.querySelector('[data-step="rules"]')?.onclick?.();

    expect(root.querySelector('.gift-list')).not.toBeNull();
  });

  it('uses dark theme by default and can persist light theme', () => {
    const root = new TestElement('div');
    mountConfig(root as unknown as HTMLElement);
    expect(root.dataset.theme).toBe('dark');

    const toggle = findByText(root, '切换至亮色主题');
    (toggle?.onclick as (() => void) | null)?.();
    expect(root.dataset.theme).toBe('light');
    expect(JSON.parse(storage.get('bilibili-live-gift-panel-v1')!).settings.theme).toBe('light');
  });

  it('restores the saved light theme and toggle state after remounting', () => {
    const firstRoot = new TestElement('div');
    mountConfig(firstRoot as unknown as HTMLElement);
    const firstToggle = findByText(firstRoot, '切换至亮色主题');
    expect(firstToggle?.getAttribute('aria-label')).toBe('切换至亮色主题');
    expect(firstToggle?.getAttribute('aria-pressed')).toBeNull();
    firstToggle?.onclick?.();

    const secondRoot = new TestElement('div');
    mountConfig(secondRoot as unknown as HTMLElement);

    expect(secondRoot.dataset.theme).toBe('light');
    const secondToggle = findByText(secondRoot, '切换至深色主题');
    expect(secondToggle).toBeDefined();
    expect(secondToggle?.getAttribute('aria-label')).toBe('切换至深色主题');
    expect(secondToggle?.getAttribute('aria-pressed')).toBeNull();
  });

  it('synchronizes the config theme immediately after importing light settings', async () => {
    const root = new TestElement('div');
    mountConfig(root as unknown as HTMLElement);
    findByText(root, '更多设置')?.onclick?.();
    findByText(root, '面板设置')?.onclick?.();

    const importInput = root.querySelectorAll('input').find((input) => input.type === 'file') as TestElement & {
      files?: Array<{ text: () => Promise<string> }>;
      onchange?: () => void;
    } | undefined;
    expect(importInput).toBeDefined();
    importInput!.files = [{ text: async () => JSON.stringify({ ...state(), settings: { theme: 'light' } }) }];
    importInput!.onchange?.();
    await Promise.resolve();

    expect(root.dataset.theme).toBe('light');
    expect(findByText(root, '切换至深色主题')).toBeDefined();
  });

  it('synchronizes the config theme immediately after importing dark settings from light', async () => {
    const root = new TestElement('div');
    mountConfig(root as unknown as HTMLElement);
    findByText(root, '切换至亮色主题')?.onclick?.();
    expect(root.dataset.theme).toBe('light');
    findByText(root, '更多设置')?.onclick?.();
    findByText(root, '面板设置')?.onclick?.();

    const importInput = root.querySelectorAll('input').find((input) => input.type === 'file') as TestElement & {
      files?: Array<{ text: () => Promise<string> }>;
      onchange?: () => void;
    } | undefined;
    expect(importInput).toBeDefined();
    importInput!.files = [{ text: async () => JSON.stringify({ ...state(), settings: { theme: 'dark' } }) }];
    importInput!.onchange?.();
    await Promise.resolve();

    expect(root.dataset.theme).toBe('dark');
    expect(findByText(root, '切换至亮色主题')).toBeDefined();
    expect(findByText(root, '切换至亮色主题')?.getAttribute('aria-label')).toBe('切换至亮色主题');
    expect(findByText(root, '切换至亮色主题')?.getAttribute('aria-pressed')).toBeNull();
  });

  it('falls back to dark when imported settings contain an invalid theme', async () => {
    const root = new TestElement('div');
    mountConfig(root as unknown as HTMLElement);
    findByText(root, '切换至亮色主题')?.onclick?.();
    expect(root.dataset.theme).toBe('light');
    findByText(root, '更多设置')?.onclick?.();
    findByText(root, '面板设置')?.onclick?.();

    const importInput = root.querySelectorAll('input').find((input) => input.type === 'file') as TestElement & {
      files?: Array<{ text: () => Promise<string> }>;
      onchange?: () => void;
    } | undefined;
    expect(importInput).toBeDefined();
    importInput!.files = [{ text: async () => JSON.stringify({ ...state(), settings: { theme: 'sepia' } }) }];
    importInput!.onchange?.();
    await Promise.resolve();

    expect(root.dataset.theme).toBe('dark');
    expect(findByText(root, '切换至亮色主题')).toBeDefined();
    expect(JSON.parse(storage.get('bilibili-live-gift-panel-v1')!).settings.theme).toBe('dark');
  });

  it('keeps configuration CSS isolated from display mode and exposes readable light variables', () => {
    const mainSource = readFileSync(new URL('../src/main.ts', import.meta.url), 'utf8');
    const configCss = readFileSync(new URL('../src/ui/config/config.css', import.meta.url), 'utf8');
    const defaultVariables = configCss.match(/\.config-root \{([\s\S]*?)\n\}/)?.[1] ?? '';
    const lightVariables = configCss.match(/\.config-root\[data-theme="light"\] \{([\s\S]*?)\n\}/)?.[1] ?? '';

    expect(mainSource).not.toContain("import './ui/config/config.css';");
    expect(mainSource).toContain("import('./ui/config/config.css?inline')");
    expect(configCss).toContain('color: var(--text);');
    expect(defaultVariables).toContain('--button-bg: #fb7299;');
    expect(defaultVariables).toContain('--button-action-text: #3b1020;');
    expect(lightVariables).toContain('--accent-text: #4a1028;');
    expect(lightVariables).toContain('--button-bg: #c2185b;');
    expect(lightVariables).toContain('--button-border: #8f173f;');
    expect(lightVariables).toContain('--button-action-text: #ffffff;');
    expect(lightVariables).toContain('--border-strong: #7f8ca3;');
    expect(lightVariables).toContain('--text-dim: #4b5563;');
    expect(lightVariables).toContain('--focus-ring: #1d4ed8;');
    expect(lightVariables).not.toContain('--border: #d9deea;');
    expect(configCss).not.toContain('.preview .result { color: var(--accent);');
    expect(configCss).toContain('--success:');
    expect(configCss).toContain('--error:');
    expect(configCss).not.toContain('rgba(255,255,255');

    const lightBg = cssVariable(lightVariables, '--bg');
    const lightBgSoft = cssVariable(lightVariables, '--bg-soft');
    const lightInputBg = cssVariable(lightVariables, '--input-bg');
    const lightTextDim = cssVariable(lightVariables, '--text-dim');
    const lightBorderStrong = cssVariable(lightVariables, '--border-strong');
    const lightFocusRing = cssVariable(lightVariables, '--focus-ring');
    const lightButtonBg = cssVariable(lightVariables, '--button-bg');
    const lightButtonText = cssVariable(lightVariables, '--button-action-text');
    const darkBg = cssVariable(defaultVariables, '--bg');
    const darkBgSoft = cssVariable(defaultVariables, '--bg-soft');
    const darkBorderStrong = cssVariable(defaultVariables, '--border-strong');
    const darkButtonBg = cssVariable(defaultVariables, '--button-bg');
    const darkButtonText = cssVariable(defaultVariables, '--button-action-text');
    expect(contrastRatio(lightTextDim, lightBg)).toBeGreaterThanOrEqual(4.5);
    expect(contrastRatio(lightTextDim, lightBgSoft)).toBeGreaterThanOrEqual(4.5);
    expect(contrastRatio(lightTextDim, lightInputBg)).toBeGreaterThanOrEqual(4.5);
    expect(contrastRatio(lightBorderStrong, lightBgSoft)).toBeGreaterThanOrEqual(3);
    expect(contrastRatio(lightBorderStrong, lightInputBg)).toBeGreaterThanOrEqual(3);
    expect(contrastRatio(lightFocusRing, lightBg)).toBeGreaterThanOrEqual(3);
    expect(contrastRatio(lightFocusRing, lightInputBg)).toBeGreaterThanOrEqual(3);
    expect(contrastRatio(lightButtonText, lightButtonBg)).toBeGreaterThanOrEqual(4.5);
    expect(contrastRatio(darkButtonText, darkButtonBg)).toBeGreaterThanOrEqual(4.5);
    expect(contrastRatio(darkBorderStrong, darkBg)).toBeGreaterThanOrEqual(3);
    expect(contrastRatio(darkBorderStrong, darkBgSoft)).toBeGreaterThanOrEqual(3);
    expect(configCss).toContain('.config-root button:focus-visible');
    expect(configCss).toContain('.config-root input:focus-visible');
    expect(configCss).toContain('.config-root select:focus-visible');
    expect(configCss).toContain('outline: 3px solid var(--focus-ring);');
    expect(configCss).toContain('border: 1px solid var(--button-border);');
  });

  it('keeps badge and danger button text readable in both themes', () => {
    const configCss = readFileSync(new URL('../src/ui/config/config.css', import.meta.url), 'utf8');
    const defaultVariables = configCss.match(/\.config-root \{([\s\S]*?)\n\}/)?.[1] ?? '';
    const lightVariables = configCss.match(/\.config-root\[data-theme="light"\] \{([\s\S]*?)\n\}/)?.[1] ?? '';

    // Badge: keep the pink brand fill, use a dark readable foreground in both themes.
    const darkBadgeText = cssVariable(defaultVariables, '--badge-text');
    const lightBadgeText = cssVariable(lightVariables, '--badge-text');
    expect(cssVariable(defaultVariables, '--accent')).toBe('#fb7299');
    expect(darkBadgeText).toBeTruthy();
    expect(lightBadgeText).toBeTruthy();
    expect(contrastRatio(darkBadgeText, '#fb7299')).toBeGreaterThanOrEqual(4.5);
    expect(contrastRatio(lightBadgeText, '#fb7299')).toBeGreaterThanOrEqual(4.5);

    // Danger button: readable foreground against the danger fill in both themes.
    const darkDangerText = cssVariable(defaultVariables, '--danger-contrast');
    const darkDangerBg = cssVariable(defaultVariables, '--danger');
    const lightDangerText = cssVariable(lightVariables, '--danger-contrast');
    const lightDangerBg = cssVariable(lightVariables, '--danger');
    expect(contrastRatio(darkDangerText, darkDangerBg)).toBeGreaterThanOrEqual(4.5);
    expect(contrastRatio(lightDangerText, lightDangerBg)).toBeGreaterThanOrEqual(4.5);
  });

  it('keeps the room instructions short and puts the exact URL explanation in details', () => {
    const root = new TestElement('div');
    mountConfig(root as unknown as HTMLElement);

    expect(findByText(root, '简单四步，开始互动')).toBeDefined();
    expect(root.querySelector('h1')?.textContent).toBe('输入你的直播间房间号');
    expect(root.querySelector('p')?.textContent).toBe('填好后点击测试连接。');

    const help = root.querySelector('.details-card');
    expect(help?.querySelector('summary')?.textContent).toBe('房间号在哪里？');
    expect(help?.querySelectorAll('p').map((paragraph) => paragraph.textContent)).toEqual([
      '看地址中 live.bilibili.com/ 后面的数字，不要复制问号后的访问参数。',
      '要填写：88888888',
    ]);
    expect(help?.querySelector('code')?.textContent).toBe('https://live.bilibili.com/88888888?live_from=1111&visit_id=abc123');
    expect(textOf(root)).not.toContain('最后的数字');
    expect(root.querySelector('.guide-card')).toBeNull();
  });

  it('keeps the danmaku connection alive while switching wizard steps', () => {
    const root = new TestElement('div');
    mountConfig(root as unknown as HTMLElement);

    const roomInput = root.querySelector('input');
    if (!roomInput) throw new Error('room input not found');
    roomInput.value = '88888888';
    findByText(root, '测试连接')?.onclick?.();

    const client = mockedClients[0];
    if (!client) throw new Error('client not created');
    client.options.onState?.('connected');
    root.querySelector('[data-step="rules"]')?.onclick?.();

    expect(client.stop).not.toHaveBeenCalled();
  });

  it('shows common attribute fields first and folds unit settings away', () => {
    const root = new TestElement('div');
    mountConfig(root as unknown as HTMLElement);

    const attributesStep = root.querySelector('[data-step="attributes"]');
    (attributesStep?.onclick as (() => void) | null)?.();

    const card = root.querySelector('.card');
    const advanced = card?.querySelector('.details-card');
    expect(card?.querySelectorAll('input')[0]?.value).toBe('加班时间');
    expect(advanced?.querySelector('summary')?.textContent).toBe('更多属性设置');
    expect(advanced?.querySelectorAll('.field-label').map((label) => label.textContent)).toEqual(['单位']);

    const mainLabels = card?.children
      .filter((child) => child !== advanced)
      .flatMap((child) => child.querySelectorAll('.field-label'))
      .map((label) => label.textContent);
    expect(mainLabels).toEqual(['名称', '初始值', '显示格式']);
  });

  it('presents rule setup as four actions and keeps advanced editor options folded', () => {
    const root = new TestElement('div');
    mountConfig(root as unknown as HTMLElement);

    const rulesStep = root.querySelector('[data-step="rules"]');
    (rulesStep?.onclick as (() => void) | null)?.();

    expect(root.querySelectorAll('.rule-action').map((action) => action.textContent)).toEqual([
      '搜索礼物',
      '选择属性',
      '选择规则示例',
      '保存规则',
    ]);
    expect(root.querySelector('input')?.placeholder).toBe('搜索礼物名称…');
    expect(root.querySelectorAll('.empty').map((empty) => textOf(empty))).toEqual(['先搜索一个观众会送的礼物。']);
    expect(root.querySelector('.empty-brand-icon')).not.toBeNull();
    expect(root.querySelector('.manual-add-card')).toBeNull();

    const gift = root.querySelector('.list-item');
    (gift?.onclick as (() => void) | null)?.();

    const formulaTutorial = root.querySelector('.tutorial');
    expect(formulaTutorial?.querySelector('summary')?.textContent).toBe('不会写规则？看示例');
    expect((formulaTutorial as TestElement & { open?: boolean } | null)?.open).not.toBe(true);
    expect(textOf(root)).toContain('规则结果会直接成为属性的新值');
    expect(textOf(root)).toContain('已有规则需要复核');
    const preview = root.querySelector('.preview');
    expect(preview).not.toBeNull();
    expect(textOf(preview as TestElement)).toContain('当前值：00:00:00 → 触发后：00:01:00');
    const additiveExample = root.querySelectorAll('.example-chip').find((example) => example.textContent?.includes('当前值加 60 秒'));
    expect(additiveExample?.textContent).toContain('加班时间+price/1000*60');
    const limits = root.querySelectorAll('.details-card').find((details) => details.querySelector('summary')?.textContent === '可选限制');
    expect(limits).toBeDefined();
    expect((limits as TestElement & { open?: boolean } | undefined)?.open).not.toBe(true);
  });

  it('keeps the formula preview safe when no attribute exists', () => {
    storage.set('bilibili-live-gift-panel-v1', JSON.stringify({ ...state(), attributes: [] }));
    const root = new TestElement('div');
    mountConfig(root as unknown as HTMLElement);

    const rulesStep = root.querySelector('[data-step="rules"]');
    (rulesStep?.onclick as (() => void) | null)?.();
    const gift = root.querySelector('.list-item');
    (gift?.onclick as (() => void) | null)?.();

    const preview = root.querySelector('.preview');
    expect(preview).not.toBeNull();
    expect(textOf(preview as TestElement)).toContain('当前值：0 → 触发后：60');
    expect(textOf(preview as TestElement)).toContain('请先创建属性');
    expect(textOf(preview as TestElement)).not.toContain('TypeError');
  });

  it('rebuilds assignment examples for the selected attribute', () => {
    storage.set('bilibili-live-gift-panel-v1', JSON.stringify({
      ...state(),
      attributes: [
        { name: '加班时间', value: 0, unit: 'seconds', format: 'hhmmss', decimals: 0, suffix: '' },
        { name: '积分', value: 5, unit: 'none', format: 'number', decimals: 0, suffix: '' },
      ],
    }));
    const root = new TestElement('div');
    mountConfig(root as unknown as HTMLElement);

    root.querySelector('[data-step="rules"]')?.onclick?.();
    root.querySelector('.list-item')?.onclick?.();

    const attrSelect = root.querySelector('select') as TestElement & { onchange?: () => void } | null;
    expect(attrSelect).not.toBeNull();
    attrSelect!.selectedIndex = 1;
    attrSelect!.onchange?.();

    const additiveExample = root.querySelectorAll('.example-chip').find((example) => example.textContent?.includes('当前值加 60 秒'));
    expect(additiveExample?.textContent).toContain('积分+price/1000*60');
    (additiveExample?.onclick as (() => void) | null)?.();
    const formulaInput = root.querySelectorAll('input').find((input) => input.dataset.fieldLabel === '触发后属性值');
    expect(formulaInput?.value).toBe('积分+price/1000*60');
  });

  it('applies the cap and refreshes the formula preview', () => {
    const root = new TestElement('div');
    mountConfig(root as unknown as HTMLElement);

    root.querySelector('[data-step="rules"]')?.onclick?.();
    root.querySelector('.list-item')?.onclick?.();

    const capInput = root.querySelectorAll('input').find((input) => input.dataset.fieldLabel === '上限封顶（可留空）') as TestElement & { oninput?: () => void } | undefined;
    expect(capInput).toBeDefined();
    capInput!.value = '30';
    capInput!.oninput?.();
    expect(textOf(root.querySelector('.preview') as TestElement)).toContain('当前值：00:00:00 → 触发后：00:00:30');

    capInput!.value = '90';
    capInput!.oninput?.();
    expect(textOf(root.querySelector('.preview') as TestElement)).toContain('当前值：00:00:00 → 触发后：00:01:00');
  });

  it('loads the gift catalog in pages instead of hiding matches after 50 items', () => {
    const root = new TestElement('div');
    mountConfig(root as unknown as HTMLElement);

    const rulesStep = root.querySelector('[data-step="rules"]');
    (rulesStep?.onclick as (() => void) | null)?.();

    const loadMore = root.querySelector('.gift-load-more');
    expect(loadMore?.textContent).toMatch(/^加载更多（已显示 50\//);
    (loadMore?.onclick as (() => void) | null)?.();
    expect(root.querySelector('.gift-load-more')?.textContent).toMatch(/^加载更多（已显示 100\//);
  });

  it('shows a compact OBS completion card with a copyable display URL', () => {
    storage.set('bilibili-live-gift-panel-v1', JSON.stringify({
      ...state('88888888', 1),
      attributes: [{ name: '加班时间', value: 61, unit: 'seconds', format: 'hhmmss', decimals: 0, suffix: '' }],
      rules: [{ id: 'r-0', giftId: 999, attributeName: '加班时间', formula: 'price/1000*60' }],
      recentGifts: [{ id: 999, name: '小心心', price: 1000, coinType: 'gold', imgBasic: '', lastReceived: 1, count: 1 }],
    }));
    const root = new TestElement('div');
    mountConfig(root as unknown as HTMLElement);

    const card = root.querySelector('.completion-card');
    const url = card?.querySelector('input') as TestElement & { value: string; readOnly: boolean } | null;
    expect(card).not.toBeNull();
    expect(url?.value).toBe('http://localhost:12450/?mode=display');
    expect(url?.readOnly).toBe(true);
    expect(findByText(root, '复制地址')).toBeDefined();
    expect(card?.querySelectorAll('.obs-step')).toHaveLength(3);
    expect(card?.querySelector('.completion-brand-icon')).not.toBeNull();
    expect(findByText(root, '属性预览')).toBeDefined();
    expect(findByText(root, '最近规则')).toBeDefined();
    expect(findByText(root, '00:01:01')).toBeDefined();
    expect(findByText(root, '小心心 → 加班时间')).toBeDefined();
    expect(findByText(root, 'price/1000*60')).toBeDefined();
    expect(findByText(root, '重新查看向导')).toBeDefined();

    (findByText(root, '重新查看向导')?.onclick as (() => void) | null)?.();
    expect(root.querySelector('h1')?.textContent).toBe('输入你的直播间房间号');
  });

  it('hides wizard progress after setup is complete', () => {
    storage.set('bilibili-live-gift-panel-v1', JSON.stringify(state('88888888', 1)));
    const root = new TestElement('div');
    mountConfig(root as unknown as HTMLElement);

    expect(root.querySelector('.wizard-progress')).toBeNull();
    expect(root.querySelector('.normal-nav')).not.toBeNull();
  });

  it('keeps manual add behind the secondary settings entry', () => {
    const root = new TestElement('div');
    mountConfig(root as unknown as HTMLElement);

    expect(root.querySelector('.manual-add-card')).toBeNull();
    const moreSettings = findByText(root, '更多设置');
    (moreSettings?.onclick as (() => void) | null)?.();
    const statsButton = findByText(root, '查看统计');
    (statsButton?.onclick as (() => void) | null)?.();
    expect(findByText(root, '统计')).toBeDefined();

    const statsMoreSettings = findByText(root, '更多设置');
    (statsMoreSettings?.onclick as (() => void) | null)?.();
    const settingsButton = findByText(root, '面板设置');
    (settingsButton?.onclick as (() => void) | null)?.();
    expect(findByText(root, '设置')).toBeDefined();

    const settingsMoreSettings = findByText(root, '更多设置');
    (settingsMoreSettings?.onclick as (() => void) | null)?.();
    const manualButton = findByText(root, '手动添加礼物');
    expect(manualButton).toBeDefined();
    (manualButton?.onclick as (() => void) | null)?.();
    expect(root.querySelector('.manual-add-card')).not.toBeNull();
  });

  it('refreshes navigation progress immediately after saving a room number', () => {
    const root = new TestElement('div') as unknown as HTMLElement;
    mountConfig(root);

    const roomInput = root.querySelector('input') as unknown as HTMLInputElement;
    roomInput.value = '2145';
    const connectButton = Array.from(root.querySelectorAll('button')).find((button) => button.textContent === '测试连接');
    expect(connectButton).toBeDefined();
    (connectButton?.onclick as (() => void) | null)?.();

    const roomStep = root.querySelector('[data-step="room"]');
    expect(roomStep?.className).toContain('is-done');
  });

  it('keeps inputs mounted while typing in onboarding and normal settings', () => {
    const onboardingRoot = new TestElement('div') as unknown as HTMLElement;
    mountConfig(onboardingRoot);
    (onboardingRoot.querySelector('[data-step="attributes"]') as unknown as TestElement | null)?.onclick?.();

    const nameInput = Array.from(onboardingRoot.querySelectorAll('input')).find((input) => input.dataset.fieldLabel === '名称') as unknown as (TestElement & { oninput?: () => void }) | undefined;
    const valueInput = Array.from(onboardingRoot.querySelectorAll('input')).find((input) => input.dataset.fieldLabel === '初始值') as unknown as (TestElement & { oninput?: () => void }) | undefined;
    expect(nameInput).toBeDefined();
    expect(valueInput).toBeDefined();

    nameInput!.value = '加';
    nameInput!.oninput?.();
    nameInput!.value = '加班';
    nameInput!.oninput?.();
    valueInput!.value = '1';
    valueInput!.oninput?.();

    expect(Array.from(onboardingRoot.querySelectorAll('input')).find((input) => input.dataset.fieldLabel === '名称')).toBe(nameInput);
    expect(Array.from(onboardingRoot.querySelectorAll('input')).find((input) => input.dataset.fieldLabel === '初始值')).toBe(valueInput);

    const normalRoot = new TestElement('div') as unknown as HTMLElement;
    storage.set('bilibili-live-gift-panel-v1', JSON.stringify(state('88888888', 1)));
    mountConfig(normalRoot);
    findByText(normalRoot as unknown as TestElement, '设置')?.onclick?.();
    const fontSizeInput = Array.from(normalRoot.querySelectorAll('input')).find((input) => input.dataset.fieldLabel === '字体大小（px）') as unknown as (TestElement & { oninput?: () => void }) | undefined;
    expect(fontSizeInput).toBeDefined();

    fontSizeInput!.value = '4';
    fontSizeInput!.oninput?.();
    fontSizeInput!.value = '48';
    fontSizeInput!.oninput?.();

    expect(Array.from(normalRoot.querySelectorAll('input')).find((input) => input.dataset.fieldLabel === '字体大小（px）')).toBe(fontSizeInput);
  });

  it('does not leave onboarding view when a gift is received', () => {
    storage.set('bilibili-live-gift-panel-v1', JSON.stringify(state('88888888', 1)));
    const root = new TestElement('div');
    mountConfig(root as unknown as HTMLElement);

    findByText(root, '重新查看向导')?.onclick?.();
    const roomInput = root.querySelector('input');
    if (!roomInput) throw new Error('room input not found');
    findByText(root, '测试连接')?.onclick?.();

    const client = mockedClients[0];
    if (!client) throw new Error('client not created');
    client.options.onGift?.({
      giftId: 30607,
      giftName: '小心心',
      num: 1,
      price: 1000,
      coinType: 'gold',
      totalCoin: 1000,
      uname: 'tester',
      uid: 1,
      timestamp: 1,
      imgBasic: '',
      rnd: 'test',
    });

    expect(root.querySelector('.wizard-progress')).not.toBeNull();
    expect(root.querySelector('.normal-nav')).toBeNull();
    expect(root.querySelector('h1')?.textContent).toBe('输入你的直播间房间号');
  });

  it('advances to attribute setup after the room connection succeeds', () => {
    const root = new TestElement('div');
    mountConfig(root as unknown as HTMLElement);

    const roomInput = root.querySelector('input') as unknown as HTMLInputElement;
    roomInput.value = '2145';
    const connectButton = Array.from(root.querySelectorAll('button')).find((button) => button.textContent === '测试连接');
    (connectButton?.onclick as (() => void) | null)?.();
    mockedClients[0]?.options.onState?.('connected');

    expect(root.querySelector('[data-step="attributes"]')?.className).toContain('is-active');
    expect(root.querySelector('h1')?.textContent).toBe('设置属性');
  });

  it('switches to normal navigation after saving the first rule', () => {
    storage.set('bilibili-live-gift-panel-v1', JSON.stringify(state('88888888')));
    const root = new TestElement('div');
    mountConfig(root as unknown as HTMLElement);

    const gift = root.querySelector('.list-item');
    (gift?.onclick as (() => void) | null)?.();
    const saveButton = root.querySelectorAll('button').find((button) => button.textContent === '保存规则');
    (saveButton?.onclick as (() => void) | null)?.();

    expect(root.querySelector('.normal-nav')).not.toBeNull();
    expect(root.querySelector('.wizard-progress')).toBeNull();
    expect(root.querySelector('.completion-card')).not.toBeNull();
  });

  it('keeps one manual-add page title instead of repeating the card heading', () => {
    const root = new TestElement('div');
    mountConfig(root as unknown as HTMLElement);

    const moreSettings = findByText(root, '更多设置');
    (moreSettings?.onclick as (() => void) | null)?.();
    const manualButton = findByText(root, '手动添加礼物');
    (manualButton?.onclick as (() => void) | null)?.();

    expect(root.querySelectorAll('h1').filter((heading) => heading.textContent === '手动添加礼物')).toHaveLength(1);
    expect(root.querySelectorAll('h3').filter((heading) => heading.textContent === '手动添加礼物')).toHaveLength(0);
  });

  it('shows onboarding without permanent top navigation for incomplete setup', () => {
    const root = new TestElement('div');
    mountConfig(root as unknown as HTMLElement);
    expect(root.querySelector('.wizard-progress')).not.toBeNull();
    expect(root.querySelector('.normal-nav')).toBeNull();
  });

  it('does not render the legacy checklist when a future OBS step is selected', () => {
    const root = new TestElement('div');
    mountConfig(root as unknown as HTMLElement);

    root.querySelector('[data-step="obs"]')?.onclick?.();

    expect(root.querySelector('.onboard')).toBeNull();
    expect(root.querySelector('.tour-bubble')).not.toBeNull();
  });

  it('shows compact top navigation after setup is complete', () => {
    storage.set('bilibili-live-gift-panel-v1', JSON.stringify(state('88888888', 1)));
    const root = new TestElement('div');
    mountConfig(root as unknown as HTMLElement);
    expect(root.querySelector('.wizard-progress')).toBeNull();
    expect(root.querySelector('.normal-nav')).not.toBeNull();
    expect(root.querySelector('.completion-home')).not.toBeNull();
  });
});

describe('single-page configuration rendering', () => {
  it('shows only the currently giftable catalog version of a renamed-icon gift', async () => {
    const oldGift = { id: 970001, name: '同名礼物', price: 5200, coinType: 'gold' as const, imgBasic: 'https://example.com/old.png' };
    const currentGift = { id: 970002, name: '同名礼物', price: 5200, coinType: 'gold' as const, imgBasic: 'https://example.com/current.png' };
    builtinCatalog.push(oldGift);
    try {
      await saveState({
        ...state('88888888'),
        settings: { ...defaultState().settings, showTutorial: false },
      });
      const fetchMock = vi.fn(async (input: string | URL | Request) => {
        const url = String(input);
        if (url === '/api/gifts?roomId=88888888') {
          return Response.json({ code: 0, gifts: [currentGift] });
        }
        if (url.includes('/api/runtime')) {
          return Response.json({ code: 0, runtime: { state: 'idle', roomId: '' } });
        }
        if (url.includes('/api/auth/status')) {
          return Response.json({ code: 0, auth: { state: 'anonymous' } });
        }
        return new Response(null, { status: 204 });
      });
      vi.stubGlobal('fetch', fetchMock);
      const root = new TestElement('div');
      mountConfig(root as unknown as HTMLElement);
      await vi.waitFor(() => expect(fetchMock).toHaveBeenCalledWith('/api/gifts?roomId=88888888', { cache: 'no-store' }));
      await Promise.resolve();
      findByText(root, '编辑')?.onclick?.();
      const searchInput = root.querySelectorAll('input')
        .find((input) => input.placeholder === '搜索礼物名称或 ID…') as TestElement & { oninput?: () => void };
      searchInput.value = '同名礼物';
      searchInput.oninput?.();

      await vi.waitFor(() => {
        const matchingChoices = root.querySelectorAll('.gift-choice')
          .filter((choice) => textOf(choice).includes('同名礼物'));
        expect(matchingChoices).toHaveLength(1);
        expect(matchingChoices[0].dataset.giftId).toBe(String(currentGift.id));
      });
    } finally {
      builtinCatalog.pop();
    }
  });

  it('uses themed scrollbars and keeps focused inputs inside their existing border', () => {
    const configCss = readFileSync(new URL('../src/ui/config/config.css', import.meta.url), 'utf8');

    expect(configCss).toMatch(/\.config-root input:focus-visible,[\s\S]*?outline: none;/);
    expect(configCss).toContain('scrollbar-color: var(--scrollbar-thumb) var(--scrollbar-track);');
    expect(configCss).toContain('::-webkit-scrollbar-thumb');
    expect(configCss).toContain('.config-root .timer-interval-row:focus-within');
    expect(configCss).toContain('-webkit-appearance: none;');
  });

  it('keeps the tutorial active from connection through the attribute modal', async () => {
    const root = new TestElement('div');
    mountConfig(root as unknown as HTMLElement);

    findByText(root, '开始填写')?.onclick?.();
    const roomInput = root.querySelectorAll('input')
      .find((input) => input.dataset.fieldLabel === '房间号') as TestElement & { oninput?: () => void };
    roomInput.value = '31567150';
    roomInput.oninput?.();
    mockedRuntimeState = 'connected';
    findByText(root, '测试连接')?.onclick?.();
    await vi.waitFor(() => expect(textOf(root)).toContain('添加第一个属性'));
    findByText(root, '添加属性')?.onclick?.();
    expect(root.querySelector('.attribute-modal')).not.toBeNull();
    expect(textOf(root)).toContain('添加礼物并配置规则');

    findByText(root, '开始配置')?.onclick?.();
    root.querySelector('.gift-choice')?.onclick?.();
    findByText(root, '创建属性')?.onclick?.();

    await vi.waitFor(() => expect(JSON.parse(storage.get('bilibili-live-gift-panel-v1')!).rules).toHaveLength(1));
    const saved = JSON.parse(storage.get('bilibili-live-gift-panel-v1')!);
    expect(saved.attributes).toHaveLength(1);
    expect(saved.rules).toHaveLength(1);
    expect(textOf(root.querySelector('.tour-bubble') as TestElement)).toContain('托盘后台运行');

    findByText(root, '复制地址')?.onclick?.();
    expect(JSON.parse(storage.get('bilibili-live-gift-panel-v1')!).settings.showTutorial).toBe(false);

    findByText(root, '编辑')?.onclick?.();
    expect(root.querySelector('.attribute-modal')).not.toBeNull();
    expect(root.querySelector('.tour-bubble')).toBeNull();
    expect(textOf(root)).not.toContain('补充礼物和规则');

    const reopenedRoot = new TestElement('div');
    mountConfig(reopenedRoot as unknown as HTMLElement);
    expect(reopenedRoot.querySelector('.tour-bubble')).toBeNull();
  });

  it('keeps room, OBS, attributes, and data settings on one page without step navigation', () => {
    const root = new TestElement('div');
    mountConfig(root as unknown as HTMLElement);

    expect(root.querySelector('.connection-grid')).not.toBeNull();
    expect(root.querySelector('.obs-card')).toBeNull();
    expect(root.querySelector('.attributes-section')).not.toBeNull();
    expect(root.querySelector('.advanced-settings')).not.toBeNull();
    expect(root.querySelector('.wizard-progress')).toBeNull();
    expect(root.querySelector('.normal-nav')).toBeNull();
    expect(root.querySelectorAll('h1')).toHaveLength(0);
    expect(textOf(root)).not.toContain('一页完成直播配置');
    expect(textOf(root)).not.toContain('把连接、属性和礼物放在同一页');
  });

  it('offers optional streamer login while keeping masked-name fallback explicit', async () => {
    const root = new TestElement('div');
    mountConfig(root as unknown as HTMLElement);

    await vi.waitFor(() => expect(root.querySelector('.login-card')).not.toBeNull());
    expect(textOf(root)).toContain('可选登录');
    expect(textOf(root)).toContain('匿名模式');
    expect(textOf(root)).toContain('登录后可以');
    expect(textOf(root)).toContain('自动识别盲盒会开出哪些礼物');
    expect(textOf(root)).toContain('尽量补全送礼人的昵称和头像');
    expect(textOf(root)).toContain('普通 B 站账号也能登录，不一定要主播本人');
    expect(textOf(root)).toContain('不登录也能连接直播间和执行礼物规则');
    expect(findByText(root, '扫码登录')).not.toBeNull();
  });

  it('shows one spotlight bubble over the next key UI', () => {
    const root = new TestElement('div');
    mountConfig(root as unknown as HTMLElement);

    expect(root.querySelector('.tour-focus')).not.toBeNull();
    expect(root.querySelector('.tour-bubble')).not.toBeNull();
    expect(textOf(root)).toContain('填写你的直播间房间号');
    expect(root.querySelector('.tour-switcher')).toBeNull();
    expect(root.querySelector('.tour-rail')).toBeNull();
  });

  it('opens one attribute modal that can select multiple gifts and save one formula per gift', async () => {
    storage.set('bilibili-live-gift-panel-v1', JSON.stringify(state('88888888')));
    const root = new TestElement('div');
    mountConfig(root as unknown as HTMLElement);

    findByText(root, '编辑')?.onclick?.();
    expect(root.querySelector('.attribute-modal')).not.toBeNull();
    expect(textOf(root)).toContain('选择会影响这个属性的礼物');
    expect(root.querySelector('.formula-help')).not.toBeNull();
    expect(textOf(root)).toContain('等号右侧的计算结果会成为属性的新值');
    expect(textOf(root)).toContain('RANDBETWEEN(A,B)');
    expect(textOf(root)).not.toContain('count 已移除');
    const broadcastMessageInput = root.querySelectorAll('input')
      .find((input) => input.dataset.fieldLabel === '默认播报消息') as TestElement;
    expect(broadcastMessageInput).toBeDefined();
    broadcastMessageInput.value = '欢迎来到直播间，感谢大家的支持';
    const choices = root.querySelectorAll('.gift-choice');
    expect(choices.length).toBeGreaterThan(1);
    choices[0].onclick?.();
    choices[1].onclick?.();
    expect(root.querySelectorAll('.selected-gift-rule')).toHaveLength(2);
    expect(root.querySelectorAll('.formula-target-name').map((label) => label.textContent)).toEqual(['加班时间 =', '加班时间 =']);

    const formulaNameInputs = root.querySelectorAll('input').filter((input) => input.dataset.fieldLabel === '规则名称') as Array<TestElement & { oninput?: () => void }>;
    expect(formulaNameInputs).toHaveLength(2);
    formulaNameInputs.forEach((input, index) => {
      input.value = index === 0 ? '加一分钟' : '加一次挑战';
      input.oninput?.();
    });

    const formulaInputs = root.querySelectorAll('input').filter((input) => input.dataset.fieldLabel === '触发后属性值') as Array<TestElement & { oninput?: () => void }>;
    expect(formulaInputs).toHaveLength(2);
    for (const input of formulaInputs) {
      input.value = '加班时间+1';
      input.oninput?.();
    }
    findByText(root, '保存修改')?.onclick?.();

    await vi.waitFor(() => expect(JSON.parse(storage.get('bilibili-live-gift-panel-v1')!).rules).toHaveLength(2));
    const saved = JSON.parse(storage.get('bilibili-live-gift-panel-v1')!);
    expect(saved.rules).toHaveLength(2);
    expect(saved.attributes[0].broadcastMessage).toBe('欢迎来到直播间，感谢大家的支持');
    expect(saved.rules.every((rule: { attributeName: string; formula: string }) => rule.attributeName === '加班时间' && rule.formula === '加班时间+1')).toBe(true);
    expect(saved.rules.map((rule: { formulaName: string }) => rule.formulaName)).toEqual(['加一分钟', '加一次挑战']);
    expect(root.querySelectorAll('.attribute-gift-rule')).toHaveLength(2);
    expect(textOf(root)).toContain('加一分钟');
    expect(textOf(root)).toContain('加一次挑战');
  });

  it('saves, applies, and deletes a reusable gift formula preset', async () => {
    storage.set('bilibili-live-gift-panel-v1', JSON.stringify(state('88888888')));
    const root = new TestElement('div');
    mountConfig(root as unknown as HTMLElement);

    findByText(root, '编辑')?.onclick?.();
    root.querySelector('.gift-choice')?.onclick?.();
    const formulaInput = root.querySelectorAll('input')
      .find((input) => input.dataset.fieldLabel === '触发后属性值') as TestElement & { oninput?: () => void };

    formulaInput.value = '加班时间+price/1000*60';
    formulaInput.oninput?.();
    findByText(root, '保存预设')?.onclick?.();
    const nameDialog = root.querySelector('.formula-preset-name-dialog')!;
    const presetNameInput = nameDialog.querySelectorAll('input')
      .find((input) => input.dataset.fieldLabel === '预设名称') as TestElement;
    presetNameInput.value = '按价格加时';
    findByText(nameDialog, '保存')?.onclick?.();

    await vi.waitFor(() => expect(loadState().formulaPresets).toHaveLength(1));
    expect(root.querySelector('.formula-preset-name-dialog')).toBeNull();
    expect(root.querySelector('.formula-preset-tools')).toBeNull();
    const preset = loadState().formulaPresets[0];
    expect(preset).toMatchObject({
      name: '按价格加时',
      context: 'gift',
      formula: '加班时间+price/1000*60',
      sourceAttributeName: '加班时间',
    });

    formulaInput.value = '加班时间+1';
    formulaInput.oninput?.();
    const presetChip = root.querySelector('.formula-preset-chip')!;
    presetChip.querySelector('.formula-preset-apply')?.onclick?.();
    expect(formulaInput.value).toBe('加班时间+price/1000*60');

    expect(presetChip.querySelector('.formula-preset-delete')?.textContent).toBe('×');
    presetChip.querySelector('.formula-preset-delete')?.onclick?.();
    await vi.waitFor(() => expect(loadState().formulaPresets).toHaveLength(0));
  });

  it('configures a conditional backend timer without adding it to the OBS gift grid', async () => {
    storage.set('bilibili-live-gift-panel-v1', JSON.stringify(state('88888888')));
    const root = new TestElement('div');
    mountConfig(root as unknown as HTMLElement);

    findByText(root, '编辑')?.onclick?.();
    expect(root.querySelector('.timer-binding-panel')).not.toBeNull();
    expect(textOf(root)).toContain('定时器只修改属性值，不会显示在 OBS 面板中');
    findByText(root, '+ 添加定时器')?.onclick?.();
    expect(root.querySelector('.timer-editor-enabled-toggle')).toBeNull();

    const nameInput = root.querySelectorAll('input')
      .find((input) => input.dataset.fieldLabel === '触发器名称') as TestElement & { oninput?: () => void };
    const conditionInput = root.querySelectorAll('input')
      .find((input) => input.dataset.fieldLabel === '运行条件（可留空）') as TestElement & { oninput?: () => void };
    const formulaInput = root.querySelectorAll('input')
      .find((input) => input.dataset.fieldLabel === '定时触发后属性值') as TestElement & { oninput?: () => void };
    nameInput.value = '有剩余时每分钟减少';
    nameInput.oninput?.();
    conditionInput.value = '加班时间>0';
    conditionInput.oninput?.();
    formulaInput.value = 'MAX(加班时间-60,0)';
    formulaInput.oninput?.();
    findByText(root, '保存修改')?.onclick?.();

    await vi.waitFor(() => expect(JSON.parse(storage.get('bilibili-live-gift-panel-v1')!).timerRules).toHaveLength(1));
    const saved = JSON.parse(storage.get('bilibili-live-gift-panel-v1')!);
    expect(saved.timerRules[0]).toMatchObject({
      attributeName: '加班时间',
      formulaName: '有剩余时每分钟减少',
      intervalSeconds: 60,
      condition: '加班时间>0',
      formula: 'MAX(加班时间-60,0)',
      enabled: true,
    });
    expect(root.querySelectorAll('.attribute-timer-rule')).toHaveLength(1);
    expect(textOf(root)).toContain('有剩余时每分钟减少');

    vi.useFakeTimers();
    const displayRoot = new TestElement('div');
    mountDisplay(displayRoot as unknown as HTMLElement, '加班时间');
    expect(textOf(displayRoot)).not.toContain('有剩余时每分钟减少');
    expect(displayRoot.querySelectorAll('.display-gift-rule')).toHaveLength(0);
    vi.useRealTimers();
  });

  it('toggles gift rules and timers independently from their attribute summary cards', async () => {
    storage.set('bilibili-live-gift-panel-v1', JSON.stringify({
      ...state('88888888', 1),
      rules: [{
        id: 'r-disabled', giftId: 1, attributeName: '加班时间', formulaName: '礼物规则', formula: '加班时间+1', enabled: false,
      }],
      timerRules: [{
        id: 't-disabled', attributeName: '加班时间', formulaName: '定时规则', intervalSeconds: 60,
        formula: '加班时间-1', enabled: false,
      }],
    }));
    const root = new TestElement('div');
    mountConfig(root as unknown as HTMLElement);

    const giftSwitch = root.querySelector('.gift-rule-enabled-button') as TestElement;
    const timerSwitch = root.querySelector('.timer-rule-enabled-button') as TestElement;
    expect(giftSwitch.getAttribute('role')).toBe('switch');
    expect(timerSwitch.getAttribute('role')).toBe('switch');
    expect(giftSwitch.getAttribute('aria-checked')).toBe('false');
    expect(timerSwitch.getAttribute('aria-checked')).toBe('false');
    expect(root.querySelectorAll('.attribute-gift-rule').filter((card) => card.className.includes('is-disabled'))).toHaveLength(2);

    giftSwitch.onclick?.();
    timerSwitch.onclick?.();

    await vi.waitFor(() => {
      expect(loadState().rules[0].enabled).toBe(true);
      expect(loadState().timerRules[0].enabled).toBe(true);
    });
    expect(giftSwitch.getAttribute('aria-checked')).toBe('true');
    expect(timerSwitch.getAttribute('aria-checked')).toBe('true');
    expect(root.querySelectorAll('.attribute-gift-rule').filter((card) => card.className.includes('is-disabled'))).toHaveLength(0);
    expect(textOf(root)).not.toContain('已停用');
  });

  it('persists timer enable toggles from a card click activation', async () => {
    const initialState = {
      ...state('88888888'),
      settings: { ...defaultState().settings, showTutorial: false },
      timerRules: [{
        id: 't-disabled', attributeName: '加班时间', formulaName: '定时规则', intervalSeconds: 60,
        formula: '加班时间-1', enabled: false,
      }],
    };
    await saveState(initialState);
    const fetchMock = vi.mocked(fetch);
    fetchMock.mockClear();
    const root = new TestElement('div');
    mountConfig(root as unknown as HTMLElement);

    const toggle = root.querySelector('.timer-rule-enabled-button') as TestElement;
    expect(toggle.getAttribute('role')).toBe('switch');
    expect(toggle.getAttribute('aria-checked')).toBe('false');
    toggle.onclick?.();
    expect(toggle.getAttribute('aria-checked')).toBe('true');

    await vi.waitFor(() => {
      const writes = fetchMock.mock.calls.filter(([, init]) => init?.method === 'PUT');
      expect(writes.length).toBeGreaterThan(0);
      const lastBody = writes.at(-1)?.[1]?.body;
      expect(JSON.parse(String(lastBody)).timerRules[0].enabled).toBe(true);
    });
  });

  it('persists external rule switches after a same-structure backend refresh', async () => {
    const initialState = {
      ...state('88888888'),
      settings: { ...defaultState().settings, showTutorial: false },
      rules: [{
        id: 'r-disabled', giftId: 1, attributeName: '加班时间', formulaName: '礼物规则',
        formula: '加班时间+1', enabled: false,
      }],
      timerRules: [{
        id: 't-disabled', attributeName: '加班时间', formulaName: '定时规则', intervalSeconds: 60,
        formula: '加班时间-1', enabled: false,
      }],
    };
    await saveState(initialState);
    let serverState = JSON.parse(JSON.stringify(initialState));
    const writtenStates: typeof initialState[] = [];
    vi.stubGlobal('fetch', vi.fn(async (input: string | URL | Request, init?: RequestInit) => {
      const url = String(input);
      if (url.endsWith('/api/config') && !init?.method) {
        return new Response(JSON.stringify(serverState), {
          status: 200,
          headers: { 'Content-Type': 'application/json' },
        });
      }
      if (url.endsWith('/api/config') && init?.method === 'PUT') {
        serverState = JSON.parse(String(init.body));
        writtenStates.push(serverState);
        return new Response(null, { status: 204 });
      }
      if (url.includes('/api/runtime')) {
        return new Response(JSON.stringify({ code: 0, runtime: { state: 'idle', roomId: '' } }), {
          status: 200,
          headers: { 'Content-Type': 'application/json' },
        });
      }
      if (url.includes('/api/auth/status')) {
        return new Response(JSON.stringify({ code: 0, auth: { state: 'anonymous' } }), {
          status: 200,
          headers: { 'Content-Type': 'application/json' },
        });
      }
      return new Response(null, { status: 204 });
    }));

    vi.useFakeTimers();
    const root = new TestElement('div');
    mountConfig(root as unknown as HTMLElement);
    const giftSwitch = root.querySelector('.gift-rule-enabled-button') as TestElement;
    const timerSwitch = root.querySelector('.timer-rule-enabled-button') as TestElement;

    await vi.advanceTimersByTimeAsync(1000);
    giftSwitch.onclick?.();
    timerSwitch.onclick?.();
    await vi.advanceTimersByTimeAsync(0);

    await vi.waitFor(() => {
      expect(writtenStates.at(-1)?.rules[0].enabled).toBe(true);
      expect(writtenStates.at(-1)?.timerRules[0].enabled).toBe(true);
    });
    expect(loadState().rules[0].enabled).toBe(true);
    expect(loadState().timerRules[0].enabled).toBe(true);
    vi.clearAllTimers();
    vi.useRealTimers();
  });

  it('does not let a stale backend refresh overwrite a timer toggle before editing', async () => {
    const initialState = {
      ...state('88888888'),
      settings: { ...defaultState().settings, showTutorial: false },
      timerRules: [{
        id: 't-disabled', attributeName: '加班时间', formulaName: '定时规则', intervalSeconds: 60,
        formula: '加班时间-1', enabled: false,
      }],
    };
    await saveState(initialState);
    const staleServerState = JSON.parse(JSON.stringify(initialState));
    let configGetStarted = false;
    let releaseConfigGet: ((response: Response) => void) | undefined;
    const configGet = new Promise<Response>((resolve) => {
      releaseConfigGet = resolve;
    });
    vi.stubGlobal('fetch', vi.fn(async (input: string | URL | Request, init?: RequestInit) => {
      const url = String(input);
      if (url.endsWith('/api/config') && !init?.method) {
        configGetStarted = true;
        return configGet;
      }
      if (url.includes('/api/runtime')) {
        return new Response(JSON.stringify({
          code: 0,
          runtime: { state: mockedRuntimeState, roomId: mockedRuntimeState === 'idle' ? '' : '31567150' },
        }), { status: 200, headers: { 'Content-Type': 'application/json' } });
      }
      if (url.includes('/api/auth/status')) {
        return new Response(JSON.stringify({ code: 0, auth: { state: 'anonymous' } }), {
          status: 200,
          headers: { 'Content-Type': 'application/json' },
        });
      }
      return new Response(null, { status: 204 });
    }));

    vi.useFakeTimers();
    const root = new TestElement('div');
    mountConfig(root as unknown as HTMLElement);
    await vi.advanceTimersByTimeAsync(1000);
    expect(configGetStarted).toBe(true);

    const summarySwitch = root.querySelector('.timer-rule-enabled-button') as TestElement;
    summarySwitch.onclick?.();
    releaseConfigGet?.(new Response(JSON.stringify(staleServerState), {
      status: 200,
      headers: { 'Content-Type': 'application/json' },
    }));
    await vi.advanceTimersByTimeAsync(0);

    expect(summarySwitch.getAttribute('aria-checked')).toBe('true');
    expect(loadState().timerRules[0].enabled).toBe(true);
    findByText(root, '编辑')?.onclick?.();
    expect(root.querySelector('.timer-editor-enabled-toggle')).toBeNull();
    vi.clearAllTimers();
    vi.useRealTimers();
  });

  it('lists only effective gift calculations with the gift ID and before/after values', () => {
    storage.set('bilibili-live-gift-panel-v1', JSON.stringify({
      ...state('88888888', 1),
      rules: [{
        id: 'r-movie', giftId: 32125, attributeName: '加班时间', formulaName: '电影票加时', formula: '加班时间+60',
      }],
      log: [
        {
          time: 1700000000, giftId: 32125, giftName: '电影票', num: 1, uname: '测试用户', senderUid: 123,
          attributeName: '加班时间', delta: 60, valueAfter: 120, ruleId: 'r-movie', source: 'gift',
        },
        {
          time: 1699999999, giftId: 32128, giftName: '爱心抱枕', num: 2, uname: '旧版用户',
          attributeName: '加班时间', delta: -30, valueAfter: 60, ruleId: 'r-old',
        },
        {
          time: 1699999998, giftId: 0, giftName: '', num: 1, uname: '', attributeName: '加班时间',
          delta: -1, valueAfter: 59, ruleId: 't-1', source: 'timer', triggerName: '每分钟减少',
        },
      ],
    }));
    const root = new TestElement('div');
    mountConfig(root as unknown as HTMLElement);

    expect(root.querySelectorAll('.gift-history-row')).toHaveLength(2);
    expect(textOf(root.querySelector('.gift-history-section') as TestElement)).toContain('2 条生效记录');
    expect(textOf(root)).toContain('礼物 ID 32125');
    expect(textOf(root)).toContain('电影票加时');
    expect(textOf(root)).toContain('00:01:00 → 00:02:00');
    expect(textOf(root)).toContain('+00:01:00');
    expect(textOf(root)).toContain('历史规则');
    expect(textOf(root)).not.toContain('每分钟减少');

    vi.stubGlobal('confirm', vi.fn(() => true));
    findByText(root, '清空记录')?.onclick?.();
    return vi.waitFor(() => {
      expect(loadState().log).toHaveLength(1);
      expect(loadState().log[0].source).toBe('timer');
      expect(root.querySelectorAll('.gift-history-row')).toHaveLength(0);
      expect(textOf(root)).toContain('还没有送礼规则生效记录');
    });
  });

  it('incrementally reveals all retained effective gift records while scrolling', () => {
    const log = Array.from({ length: 85 }, (_, index) => ({
      time: 1700000000 - index,
      giftId: 32125,
      giftName: '电影票',
      num: 1,
      uname: `用户 ${index}`,
      attributeName: '加班时间',
      delta: 1,
      valueAfter: 85 - index,
      ruleId: 'r-movie',
      source: 'gift',
    }));
    storage.set('bilibili-live-gift-panel-v1', JSON.stringify({
      ...state('88888888', 1),
      rules: [{
        id: 'r-movie', giftId: 32125, attributeName: '加班时间', formulaName: '电影票加时', formula: '加班时间+1',
      }],
      log,
    }));
    const root = new TestElement('div');
    mountConfig(root as unknown as HTMLElement);
    const list = root.querySelector('.gift-history-list') as TestElement & {
      clientHeight?: number;
      scrollHeight?: number;
      scrollTop?: number;
      onscroll?: () => void;
    };

    expect(list.querySelectorAll('.gift-history-row')).toHaveLength(40);
    expect(list.querySelector('.gift-history-loader')?.textContent).toContain('40 / 85');
    list.clientHeight = 300;
    list.scrollHeight = 900;
    list.scrollTop = 600;
    list.onscroll?.();
    expect(list.querySelectorAll('.gift-history-row')).toHaveLength(80);
    expect(list.querySelector('.gift-history-loader')?.textContent).toContain('80 / 85');
  });

  it('preserves disabled rules when an attribute is edited and saved', async () => {
    storage.set('bilibili-live-gift-panel-v1', JSON.stringify({
      ...state('88888888', 1),
      rules: [{
        id: 'r-disabled', giftId: 1, attributeName: '加班时间', formulaName: '礼物规则', formula: '加班时间+1', enabled: false,
      }],
      timerRules: [{
        id: 't-disabled', attributeName: '加班时间', formulaName: '定时规则', intervalSeconds: 60,
        formula: '加班时间-1', enabled: false,
      }],
    }));
    const root = new TestElement('div');
    mountConfig(root as unknown as HTMLElement);

    findByText(root, '编辑')?.onclick?.();
    findByText(root, '保存修改')?.onclick?.();

    await vi.waitFor(() => expect(root.querySelector('.attribute-modal')).toBeNull());
    expect(loadState().rules[0].enabled).toBe(false);
    expect(loadState().timerRules[0].enabled).toBe(false);
  });

  it('collapses duplicate catalog aliases into one visible gift choice', () => {
    storage.set('bilibili-live-gift-panel-v1', JSON.stringify(state('88888888')));
    const root = new TestElement('div');
    mountConfig(root as unknown as HTMLElement);

    findByText(root, '编辑')?.onclick?.();
    const duplicatedChoices = root.querySelectorAll('.gift-choice')
      .filter((choice) => textOf(choice).includes('666'));

    expect(duplicatedChoices).toHaveLength(1);
  });

  it('does not silently rewrite formulas that still contain removed count', () => {
    storage.set('bilibili-live-gift-panel-v1', JSON.stringify({
      ...state('88888888', 1),
      rules: [{ id: 'r-0', giftId: 1, attributeName: '加班时间', formula: '加班时间+count*30' }],
    }));
    const root = new TestElement('div');
    mountConfig(root as unknown as HTMLElement);

    const saved = JSON.parse(storage.get('bilibili-live-gift-panel-v1')!);
    expect(saved.rules[0].formula).toBe('加班时间+count*30');
  });

  it('persists configured gift metadata so the backend can match live gift aliases', () => {
    storage.set('bilibili-live-gift-panel-v1', JSON.stringify({
      ...state('88888888', 1),
      rules: [{ id: 'r-0', giftId: 33300, attributeName: '加班时间', formula: '加班时间+1' }],
      giftCatalog: [],
    }));
    const root = new TestElement('div');
    mountConfig(root as unknown as HTMLElement);

    const saved = JSON.parse(storage.get('bilibili-live-gift-panel-v1')!);
    expect(saved.giftCatalog).toEqual(expect.arrayContaining([
      expect.objectContaining({ id: 33300, name: '666' }),
    ]));
  });

  it('gives every attribute its own OBS address', () => {
    storage.set('bilibili-live-gift-panel-v1', JSON.stringify(state('88888888')));
    const root = new TestElement('div');
    mountConfig(root as unknown as HTMLElement);

    const attributeCard = root.querySelector('.attribute-card');
    const input = attributeCard?.querySelector('.attribute-obs-input');
    expect(root.querySelector('.obs-card')).toBeNull();
    expect(textOf(root)).not.toContain('OBS 浏览器源');
    expect(input?.value).toBe('http://localhost:12450/?mode=display&attribute=%E5%8A%A0%E7%8F%AD%E6%97%B6%E9%97%B4');
    expect(input?.readOnly).toBe(true);
    expect(findByText(root, '复制 OBS 链接')).toBeDefined();
  });

  it('incrementally appends gift picker items while scrolling and resets after search', () => {
    const gifts = Array.from({ length: 135 }, (_, index) => ({
      id: 10000 + index,
      name: `自定义礼物 ${index}`,
      price: (index + 1) * 100,
      coinType: 'gold',
      imgBasic: '',
      lastReceived: index,
      count: 1,
    }));
    storage.set('bilibili-live-gift-panel-v1', JSON.stringify({
      ...state('88888888', 1),
      recentGifts: gifts,
      giftCatalog: gifts,
    }));
    const root = new TestElement('div');
    mountConfig(root as unknown as HTMLElement);
    findByText(root, '编辑')?.onclick?.();

    const picker = root.querySelector('.gift-picker-grid') as TestElement & {
      clientHeight?: number;
      scrollHeight?: number;
      scrollTop?: number;
      onscroll?: () => void;
    };
    expect(picker.querySelectorAll('.gift-choice')).toHaveLength(40);
    expect(picker.querySelector('.gift-picker-loader')?.textContent).toContain('40 /');

    picker.clientHeight = 240;
    picker.scrollHeight = 1000;
    picker.scrollTop = 800;
    picker.onscroll?.();
    expect(picker.querySelectorAll('.gift-choice')).toHaveLength(80);

    picker.scrollHeight = 1800;
    picker.scrollTop = 1600;
    picker.onscroll?.();
    expect(picker.querySelectorAll('.gift-choice')).toHaveLength(120);

    const search = root.querySelector('.gift-search') as TestElement & { oninput?: () => void };
    search.value = '自定义礼物 134';
    search.oninput?.();
    expect(picker.querySelectorAll('.gift-choice')).toHaveLength(1);
    expect(picker.querySelector('.gift-picker-loader')?.textContent).toContain('已显示全部');
  });

  it('keeps the attribute editor open when text selection drags outside the panel', () => {
    storage.set('bilibili-live-gift-panel-v1', JSON.stringify(state('88888888', 1)));
    const root = new TestElement('div');
    mountConfig(root as unknown as HTMLElement);
    findByText(root, '编辑')?.onclick?.();

    const overlay = root.querySelector('.attribute-overlay') as TestElement & {
      onpointerdown?: (event: { target: TestElement }) => void;
      onclick?: (event: { target: TestElement }) => void;
    };
    const input = overlay.querySelector('input') as TestElement;
    overlay.onpointerdown?.({ target: input });
    overlay.onclick?.({ target: overlay });
    expect(root.querySelector('.attribute-overlay')).toBe(overlay);

    overlay.onpointerdown?.({ target: overlay });
    overlay.onclick?.({ target: overlay });
    expect(root.querySelector('.attribute-overlay')).toBeNull();
  });

  it('persists the OBS panel opacity setting', async () => {
    const root = new TestElement('div');
    mountConfig(root as unknown as HTMLElement);

    const opacity = root.querySelectorAll('input')
      .find((input) => input.dataset.fieldLabel === '面板透明度（%）') as TestElement & { onchange?: () => void };
    expect(opacity.value).toBe('55');
    opacity.value = '72';
    opacity.onchange?.();

    await vi.waitFor(() => expect(loadState().settings.panelOpacity).toBe(72));
  });

  it('uses field-specific controls for OBS panel appearance', async () => {
    const root = new TestElement('div');
    mountConfig(root as unknown as HTMLElement);

    const fontSize = root.querySelectorAll('input')
      .find((input) => input.dataset.fieldLabel === '字体大小（px）') as TestElement & { onchange?: () => void };
    const accent = root.querySelectorAll('input')
      .find((input) => input.dataset.fieldLabel === '强调色') as TestElement & { onchange?: () => void };
    const opacity = root.querySelectorAll('input')
      .find((input) => input.dataset.fieldLabel === '面板透明度（%）') as TestElement & { onchange?: () => void };
    const alignmentOptions = root.querySelectorAll('.alignment-option');
    const connectionSwitch = root.querySelector('.setting-switch-input') as TestElement & { checked?: boolean; onchange?: () => void };

    expect(fontSize.type).toBe('range');
    expect(fontSize.attributes.min).toBe('24');
    expect(fontSize.attributes.max).toBe('96');
    expect(accent.type).toBe('color');
    expect(opacity.type).toBe('range');
    expect(alignmentOptions.map((option) => option.textContent)).toEqual(['左对齐', '居中', '右对齐']);
    expect(connectionSwitch).not.toBeNull();

    fontSize.value = '64';
    fontSize.onchange?.();
    accent.value = '#123456';
    accent.onchange?.();
    alignmentOptions[2].onclick?.();
    connectionSwitch.checked = true;
    connectionSwitch.onchange?.();

    await vi.waitFor(() => {
      expect(loadState().settings).toEqual(expect.objectContaining({
        fontSize: 64,
        accentColor: '#123456',
        align: 'right',
        showConnection: true,
      }));
    });
  });
});

describe('OBS attribute display', () => {
  it('formats positive, negative, and zero deltas with the correct sign', () => {
    const attr = { name: '加班时间', value: 0, unit: 'seconds', format: 'hhmmss', decimals: 0, suffix: '' } as const;
    expect(formatDelta(60, attr)).toBe('+00:01:00');
    expect(formatDelta(-40, attr)).toBe('-00:00:40');
    expect(formatDelta(0, attr)).toBe('00:00:00');
  });

  it('uses the shared brand icon only for the empty display state', () => {
    storage.set('bilibili-live-gift-panel-v1', JSON.stringify({
      ...state(),
      attributes: [],
    }));
    vi.useFakeTimers();
    const root = new TestElement('div');
    mountDisplay(root as unknown as HTMLElement);

    expect(root.querySelector('.stats')).toBeNull();
    expect(root.querySelector('.display-empty-brand-icon')).not.toBeNull();
    expect(textOf(root)).not.toContain('🎁');
    vi.useRealTimers();
  });

  it('renders only the attribute selected by the OBS link', () => {
    storage.set('bilibili-live-gift-panel-v1', JSON.stringify({
      ...state(),
      attributes: [
        { name: '加班时间', value: 60, unit: 'seconds', format: 'hhmmss', decimals: 0, suffix: '' },
        { name: '积分', value: 7, unit: 'number', format: 'number', decimals: 0, suffix: '', broadcastMessage: '欢迎参与积分挑战' },
      ],
      rules: [{ id: 'r-points', giftId: 1, attributeName: '积分', formulaName: '增加一分', formula: '积分+1' }],
      recentGifts: [{ id: 1, name: '小心心', price: 100, coinType: 'gold', imgBasic: '', lastReceived: 1, count: 1 }],
    }));
    vi.useFakeTimers();
    const root = new TestElement('div');
    mountDisplay(root as unknown as HTMLElement, '积分');

    expect(textOf(root)).toContain('积分');
    expect(textOf(root)).toContain('7');
    expect(textOf(root)).toContain('增加一分');
    expect(textOf(root)).toContain('欢迎参与积分挑战');
    expect(root.querySelectorAll('.display-gift-rule')).toHaveLength(1);
    expect(root.querySelector('.broadcast-ticker')).not.toBeNull();
    expect(root.querySelector('.panel')?.querySelector('.broadcast-ticker')).not.toBeNull();
    expect(root.querySelector('.attr')?.querySelector('.broadcast-ticker')).not.toBeNull();
    expect(root.querySelector('.display-broadcast-area')).toBeNull();
    expect(textOf(root)).not.toContain('加班时间');
    vi.useRealTimers();
  });

  it('uses a smaller two-line treatment for long OBS formula names', () => {
    storage.set('bilibili-live-gift-panel-v1', JSON.stringify({
      ...state(),
      attributes: [
        { name: '早播', value: 0, unit: 'none', format: 'number', decimals: 0, suffix: '场' },
      ],
      rules: [{
        id: 'r-long', giftId: 1, attributeName: '早播',
        formulaName: '小于10场+1，大于10场x2', formula: '早播+1',
      }],
      recentGifts: [{ id: 1, name: '电影票', price: 100, coinType: 'gold', imgBasic: '', lastReceived: 1, count: 1 }],
    }));
    vi.useFakeTimers();
    const root = new TestElement('div');
    mountDisplay(root as unknown as HTMLElement, '早播');

    const formulaName = root.querySelector('.display-formula-name');
    expect(formulaName?.className).toContain('is-long');
    expect(formulaName?.textContent).toBe('小于10场+1，大于10场x2');
    const displayCss = readFileSync(new URL('../src/ui/display/display.css', import.meta.url), 'utf8');
    expect(displayCss).toMatch(/\.display-formula-name\s*\{[^}]*-webkit-line-clamp:\s*2;/);
    expect(displayCss).toMatch(/\.display-formula-name\.is-long\s*\{[^}]*font-size:\s*20px;/);
    vi.useRealTimers();
  });
});

describe('room number hint', () => {
  it('extracts the path segment before query parameters', () => {
    expect(getRoomNumberHint('https://live.bilibili.com/88888888?live_from=1111&visit_id=x')).toEqual({
      path: '88888888',
      query: '?live_from=1111&visit_id=x',
    });
  });
});
