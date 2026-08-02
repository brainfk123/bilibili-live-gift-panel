import { beforeEach, describe, expect, it, vi } from 'vitest';
import { readFileSync } from 'node:fs';
import { mountConfig } from '../src/ui/config/config';
import { formatDelta, mountDisplay } from '../src/ui/display/display';
import { getNextWizardStep, getRoomNumberHint, getTutorialStep, getWizardChecklist, getWizardProgress } from '../src/ui/config/wizard';
import { defaultState, loadState, resetState, saveState } from '../src/storage';
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
      '选择公式示例',
      '保存规则',
    ]);
    expect(root.querySelector('input')?.placeholder).toBe('搜索礼物名称…');
    expect(root.querySelectorAll('.empty').map((empty) => textOf(empty))).toEqual(['先搜索一个观众会送的礼物。']);
    expect(root.querySelector('.empty-brand-icon')).not.toBeNull();
    expect(root.querySelector('.manual-add-card')).toBeNull();

    const gift = root.querySelector('.list-item');
    (gift?.onclick as (() => void) | null)?.();

    const formulaTutorial = root.querySelector('.tutorial');
    expect(formulaTutorial?.querySelector('summary')?.textContent).toBe('不会写公式？看示例');
    expect((formulaTutorial as TestElement & { open?: boolean } | null)?.open).not.toBe(true);
    expect(textOf(root)).toContain('公式结果会直接成为属性的新值');
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
    expect(textOf(root)).toContain('添加礼物并配置公式');

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
    expect(textOf(root)).not.toContain('补充礼物和公式');

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
    const choices = root.querySelectorAll('.gift-choice');
    expect(choices.length).toBeGreaterThan(1);
    choices[0].onclick?.();
    choices[1].onclick?.();
    expect(root.querySelectorAll('.selected-gift-rule')).toHaveLength(2);
    expect(root.querySelectorAll('.formula-target-name').map((label) => label.textContent)).toEqual(['加班时间 =', '加班时间 =']);

    const formulaNameInputs = root.querySelectorAll('input').filter((input) => input.dataset.fieldLabel === '公式名称') as Array<TestElement & { oninput?: () => void }>;
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
    expect(saved.rules.every((rule: { attributeName: string; formula: string }) => rule.attributeName === '加班时间' && rule.formula === '加班时间+1')).toBe(true);
    expect(saved.rules.map((rule: { formulaName: string }) => rule.formulaName)).toEqual(['加一分钟', '加一次挑战']);
    expect(root.querySelectorAll('.attribute-gift-rule')).toHaveLength(2);
    expect(textOf(root)).toContain('加一分钟');
    expect(textOf(root)).toContain('加一次挑战');
  });

  it('saves, applies, and deletes a reusable gift formula preset', async () => {
    storage.set('bilibili-live-gift-panel-v1', JSON.stringify(state('88888888')));
    vi.stubGlobal('confirm', vi.fn(() => true));
    const root = new TestElement('div');
    mountConfig(root as unknown as HTMLElement);

    findByText(root, '编辑')?.onclick?.();
    root.querySelector('.gift-choice')?.onclick?.();
    const formulaInput = root.querySelectorAll('input')
      .find((input) => input.dataset.fieldLabel === '触发后属性值') as TestElement & { oninput?: () => void };
    const presetNameInput = root.querySelectorAll('input')
      .find((input) => input.dataset.fieldLabel === '预设名称') as TestElement;
    const presetTools = root.querySelector('.formula-preset-tools')!;
    const presetSelect = presetTools.querySelector('.formula-preset-select') as TestElement & { onchange?: () => void };

    formulaInput.value = '加班时间+price/1000*60';
    formulaInput.oninput?.();
    presetNameInput.value = '按价格加时';
    findByText(presetTools, '保存当前公式')?.onclick?.();

    await vi.waitFor(() => expect(loadState().formulaPresets).toHaveLength(1));
    const preset = loadState().formulaPresets[0];
    expect(preset).toMatchObject({
      name: '按价格加时',
      context: 'gift',
      formula: '加班时间+price/1000*60',
      sourceAttributeName: '加班时间',
    });

    formulaInput.value = '加班时间+1';
    formulaInput.oninput?.();
    presetSelect.value = preset.id;
    presetSelect.onchange?.();
    findByText(presetTools, '应用')?.onclick?.();
    expect(formulaInput.value).toBe('加班时间+price/1000*60');

    findByText(presetTools, '删除')?.onclick?.();
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
});

describe('display branding', () => {
  it('formats positive, negative, and zero deltas with the correct sign', () => {
    const attr = { name: '加班时间', value: 0, unit: 'seconds', format: 'hhmmss', decimals: 0, suffix: '' } as const;
    expect(formatDelta(60, attr)).toBe('+00:01:00');
    expect(formatDelta(-40, attr)).toBe('-00:00:40');
    expect(formatDelta(0, attr)).toBe('00:00:00');
  });

  it('uses the shared brand icon for gift stats and the empty display state', () => {
    storage.set('bilibili-live-gift-panel-v1', JSON.stringify({
      ...state(),
      attributes: [],
    }));
    vi.useFakeTimers();
    const root = new TestElement('div');
    mountDisplay(root as unknown as HTMLElement);

    expect(root.querySelector('.stats-brand-icon')).not.toBeNull();
    expect(root.querySelector('.display-empty-brand-icon')).not.toBeNull();
    expect(textOf(root)).not.toContain('🎁');
    vi.useRealTimers();
  });

  it('renders only the attribute selected by the OBS link', () => {
    storage.set('bilibili-live-gift-panel-v1', JSON.stringify({
      ...state(),
      attributes: [
        { name: '加班时间', value: 60, unit: 'seconds', format: 'hhmmss', decimals: 0, suffix: '' },
        { name: '积分', value: 7, unit: 'number', format: 'number', decimals: 0, suffix: '' },
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
    expect(textOf(root)).not.toContain('加班时间');
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
