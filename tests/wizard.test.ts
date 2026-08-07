import { beforeEach, describe, expect, it, vi } from 'vitest';
import { readFileSync } from 'node:fs';
import { mountConfig } from '../src/ui/config/config';
import { formatDelta, mountDisplay, resolveAttributeValuePresentation } from '../src/ui/display/display';
import {
  getNextWizardStep,
  getRoomNumberHint,
  getTutorialLesson,
  getTutorialStep,
  getWizardChecklist,
  getWizardProgress,
  markTutorialLessonComplete,
  resetTutorialProgress,
  TUTORIAL_LESSONS,
  type TutorialEditorProgress,
} from '../src/ui/config/wizard';
import { defaultState, loadState, resetState, saveState } from '../src/storage';
import { builtinCatalog } from '../src/gifts/catalog';
import type { GiftEvent } from '../src/bilibili/messages';
import type { Attribute } from '../src/types';

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
const nativeSetInterval = globalThis.setInterval.bind(globalThis);
const nativeSetTimeout = globalThis.setTimeout.bind(globalThis);
const nativeClearInterval = globalThis.clearInterval.bind(globalThis);

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

beforeEach(async () => {
  mockedClients.length = 0;
  mockedRuntimeState = 'idle';
  vi.stubGlobal('setInterval', vi.fn((handler: TimerHandler, timeout?: number, ...args: unknown[]) => (
    (timeout ?? 0) >= 1000 ? 0 : nativeSetInterval(handler, timeout, ...args)
  )));
  vi.stubGlobal('clearInterval', vi.fn((id?: number) => {
    if (id) nativeClearInterval(id);
  }));
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
    if (url.includes('/api/room/anchor')) {
      return Response.json({
        code: 0,
        roomId: '31567150',
        anchor: { uid: 32249588, uname: '测试主播', avatar: 'https://example.test/anchor.png' },
      });
    }
    if (url.includes('/api/formula/preview')) {
      const body = JSON.parse(String(init?.body ?? '{}')) as { attributeValue?: number };
      return new Response(JSON.stringify({ code: 0, result: (body.attributeValue ?? 0) + 1 }), {
        status: 200,
        headers: { 'Content-Type': 'application/json' },
      });
    }
    if (url === '/api/update' || url === '/api/update/check') {
      return Response.json({
        code: 0,
        update: {
          state: 'development', currentVersion: 'dev', message: '开发版本不会检查 GitHub 更新。',
          autoUpdate: true, restartRequired: false,
        },
      }, { status: url.endsWith('/check') ? 202 : 200 });
    }
    return new Response(null, { status: 204 });
  }));
  storage.clear();
  await saveState(defaultState());
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

  it('derives all overtime training lessons from real product state', () => {
    const tutorialState = defaultState();
    expect(getTutorialLesson(tutorialState, false)).toBe('room');
    expect(getTutorialLesson(tutorialState, true)).toBe('attribute');

    const template: TutorialEditorProgress = { open: false, templateOpen: true, isNew: true };
    expect(getTutorialLesson(tutorialState, true, template)).toBe('template');
    const editor: TutorialEditorProgress = { open: true, isNew: true };
    expect(getTutorialLesson(tutorialState, true, editor)).toBe('basics');
    editor.basicsConfigured = true;
    expect(getTutorialLesson(tutorialState, true, editor)).toBe('gift');
    editor.giftCount = 1;
    expect(getTutorialLesson(tutorialState, true, editor)).toBe('rule');
    editor.giftPreviewed = true;
    expect(getTutorialLesson(tutorialState, true, editor)).toBe('preset');

    tutorialState.formulaPresets.push({
      id: 'preset-1', name: '每元加时', context: 'gift', formula: '加班时间+price/1000*60', sourceAttributeName: '加班时间',
    });
    expect(getTutorialLesson(tutorialState, true, editor)).toBe('timer');
    editor.timerPreviewed = true;
    expect(getTutorialLesson(tutorialState, true, editor)).toBe('appearance');
    editor.outputPreviewed = true;
    expect(getTutorialLesson(tutorialState, true, editor)).toBe('save');

    tutorialState.attributes.push({
      name: '加班时间', value: 0, unit: 'seconds', format: 'hhmmss', decimals: 0, suffix: '', broadcastMessage: '',
    });
    tutorialState.rules.push({
      id: 'rule-1', giftId: 1, attributeName: '加班时间', formulaName: '加时', formula: '加班时间+60', enabled: false,
    });
    expect(getTutorialLesson(tutorialState, true, { open: false })).toBe('timer');
    tutorialState.timerRules.push({
      id: 'timer-1', attributeName: '加班时间', formulaName: '自动减少', intervalSeconds: 60,
      condition: '加班时间>0', formula: 'MAX(加班时间-60,0)', enabled: true,
    });
    expect(getTutorialLesson(tutorialState, true, { open: false })).toBe('enable');
    tutorialState.rules[0].enabled = true;
    expect(getTutorialLesson(tutorialState, true, { open: false })).toBe('output');
    markTutorialLessonComplete(tutorialState.settings, 'output');
    expect(getTutorialLesson(tutorialState, true, { open: false })).toBeNull();
  });

  it('replays lessons independently from an existing complete configuration', () => {
    const tutorialState = defaultState();
    tutorialState.attributes.push({
      name: '加班时间', value: 0, unit: 'seconds', format: 'hhmmss', decimals: 0, suffix: '',
    });
    tutorialState.rules.push({
      id: 'rule-1', giftId: 1, attributeName: '加班时间', formulaName: '加时', formula: '加班时间+60', enabled: true,
    });
    tutorialState.timerRules.push({
      id: 'timer-1', attributeName: '加班时间', formulaName: '自动减少', intervalSeconds: 1,
      condition: '加班时间>0', formula: 'MAX(加班时间-1,0)', enabled: true,
    });
    tutorialState.formulaPresets.push({
      id: 'preset-1', name: '每元加时', context: 'gift', formula: '加班时间+price/1000*60', sourceAttributeName: '加班时间',
    });

    expect(getTutorialLesson(tutorialState, true)).toBe('output');
    resetTutorialProgress(tutorialState.settings);

    expect(tutorialState.settings.tutorialReplayMode).toBe(true);
    expect(getTutorialLesson(tutorialState, true)).toBe('attribute');
    expect(getTutorialLesson(tutorialState, true, { open: false, templateOpen: true, isNew: true })).toBe('template');
    markTutorialLessonComplete(tutorialState.settings, 'output');
    expect(tutorialState.settings.tutorialReplayMode).toBe(false);
  });
});

describe('gameplay template wizard integration', () => {
  it('opens the template library for a normal add-attribute action', async () => {
    const configured = defaultState();
    configured.settings.showTutorial = false;
    await saveState(configured);
    const root = new TestElement('div');

    mountConfig(root as unknown as HTMLElement);
    findByText(root, '+ 添加属性')?.onclick?.();

    expect(root.querySelector('.template-wizard-overlay')).not.toBeNull();
    expect(root.querySelectorAll('.gameplay-template-card')).toHaveLength(13);
    expect(textOf(root.querySelector('.template-wizard') as TestElement)).toContain('从空白创建');
  });

  it('opens the complete attribute workbench from the blank creation card', async () => {
    const configured = defaultState();
    configured.settings.showTutorial = false;
    await saveState(configured);
    const root = new TestElement('div');

    mountConfig(root as unknown as HTMLElement);
    findByText(root, '+ 添加属性')?.onclick?.();
    const blank = root.querySelectorAll('.gameplay-template-card')
      .find((card) => textOf(card).includes('从空白创建'));
    blank?.onclick?.();

    expect(root.querySelector('.template-wizard-overlay')).toBeNull();
    expect(root.querySelector('.attribute-workbench')).not.toBeNull();
    expect(textOf(root.querySelector('.attribute-workbench') as TestElement)).toContain('创建互动属性');
  });

  it('validates and saves a complete overtime template as one configuration', async () => {
    const configured = defaultState();
    configured.settings.showTutorial = false;
    await saveState(configured);
    const root = new TestElement('div');
    mountConfig(root as unknown as HTMLElement);
    findByText(root, '+ 添加属性')?.onclick?.();

    const overtime = root.querySelectorAll('.gameplay-template-card')
      .find((card) => textOf(card).includes('加班机'));
    overtime?.onclick?.();
    findByText(root, '下一步')?.onclick?.();
    expect(textOf(root.querySelector('.template-wizard') as TestElement)).toContain('每 1 元增加');
    findByText(root, '下一步')?.onclick?.();
    expect(root.querySelector('.template-gift-choice')).not.toBeNull();
    root.querySelector('.template-gift-choice')?.onclick?.();
    expect(root.querySelector('.template-gift-choice')?.className).toContain('is-selected');
    findByText(root, '下一步')?.onclick?.();
    expect(textOf(root.querySelector('.template-wizard') as TestElement)).toContain('将创建“加班时间”');
    root.querySelector('.template-wizard-actions')?.querySelectorAll('button').at(-1)?.onclick?.();

    await vi.waitFor(() => expect(loadState().attributes).toHaveLength(1));
    expect(loadState().attributes[0].createdFromTemplateId).toBe('overtime');
    expect(loadState().rules).toHaveLength(1);
    expect(loadState().timerRules).toHaveLength(1);
    expect(root.querySelector('.template-wizard-overlay')).toBeNull();
  });

  it('creates a team duel with two attributes, one scene, and one gated activity transactionally', async () => {
    const configured = defaultState();
    configured.settings.showTutorial = false;
    await saveState(configured);
    const root = new TestElement('div');
    mountConfig(root as unknown as HTMLElement);
    findByText(root, '+ 添加属性')?.onclick?.();

    root.querySelectorAll('.gameplay-template-card')
      .find((card) => textOf(card).includes('阵营对战'))?.onclick?.();
    findByText(root, '下一步')?.onclick?.();
    findByText(root, '下一步')?.onclick?.();
    root.querySelectorAll('.template-gift-choice')[0]?.onclick?.();
    root.querySelectorAll('.template-gift-slot')
      .find((slot) => textOf(slot).includes('右队礼物'))?.onclick?.();
    root.querySelectorAll('.template-gift-choice')[1]?.onclick?.();
    findByText(root, '下一步')?.onclick?.();

    expect(textOf(root.querySelector('.template-wizard') as TestElement)).toContain('将创建 2 个属性');
    expect(textOf(root.querySelector('.template-wizard') as TestElement)).toContain('1 个活动会话');
    root.querySelector('.template-wizard-actions')?.querySelectorAll('button').at(-1)?.onclick?.();

    await vi.waitFor(() => expect(loadState().activities).toHaveLength(1));
    expect(loadState().attributes.map((attribute) => attribute.name)).toEqual(['红队', '蓝队']);
    expect(loadState().displayScenes).toHaveLength(1);
    expect(loadState().activities[0]).toEqual(expect.objectContaining({
      status: 'not_started', resultMode: 'highest', gateRules: true,
    }));
    expect(loadState().activities[0].milestones).toHaveLength(2);
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

    const toggle = root.querySelector('.config-theme-toggle');
    expect(toggle?.getAttribute('aria-label')).toBe('切换至亮色主题');
    (toggle?.onclick as (() => void) | null)?.();
    expect(root.dataset.theme).toBe('light');
    expect(JSON.parse(storage.get('bilibili-live-gift-panel-v1')!).settings.theme).toBe('light');
  });

  it('restores the saved light theme and toggle state after remounting', () => {
    const firstRoot = new TestElement('div');
    mountConfig(firstRoot as unknown as HTMLElement);
    const firstToggle = firstRoot.querySelector('.config-theme-toggle');
    expect(firstToggle?.getAttribute('aria-label')).toBe('切换至亮色主题');
    expect(firstToggle?.getAttribute('aria-pressed')).toBeNull();
    firstToggle?.onclick?.();

    const secondRoot = new TestElement('div');
    mountConfig(secondRoot as unknown as HTMLElement);

    expect(secondRoot.dataset.theme).toBe('light');
    const secondToggle = secondRoot.querySelector('.config-theme-toggle');
    expect(secondToggle).not.toBeNull();
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
    expect(root.querySelector('.config-theme-toggle')?.getAttribute('aria-label')).toBe('切换至深色主题');
  });

  it('synchronizes the config theme immediately after importing dark settings from light', async () => {
    const root = new TestElement('div');
    mountConfig(root as unknown as HTMLElement);
    root.querySelector('.config-theme-toggle')?.onclick?.();
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
    const themeToggle = root.querySelector('.config-theme-toggle');
    expect(themeToggle).not.toBeNull();
    expect(themeToggle?.getAttribute('aria-label')).toBe('切换至亮色主题');
    expect(themeToggle?.getAttribute('aria-pressed')).toBeNull();
  });

  it('falls back to dark when imported settings contain an invalid theme', async () => {
    const root = new TestElement('div');
    mountConfig(root as unknown as HTMLElement);
    root.querySelector('.config-theme-toggle')?.onclick?.();
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
    expect(root.querySelector('.config-theme-toggle')?.getAttribute('aria-label')).toBe('切换至亮色主题');
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
    expect(root.querySelector('p')?.textContent).toBe('填好后点击连接。');

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
    findByText(root, '连接')?.onclick?.();

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
    const connectButton = Array.from(root.querySelectorAll('button')).find((button) => button.textContent === '连接');
    expect(connectButton).toBeDefined();
    (connectButton?.onclick as (() => void) | null)?.();

    const roomStep = root.querySelector('[data-step="room"]');
    expect(roomStep?.className).toContain('is-done');
  });

  it('confirms a room switch and clears only room-scoped records', async () => {
    const configured = defaultState();
    configured.settings.showTutorial = false;
    configured.roomId = '100';
    configured.attributes = [{ name: '积分', value: 7, unit: 'none', format: 'number', decimals: 0, suffix: '' }];
    configured.rules = [{ id: 'r1', giftId: 1, attributeName: '积分', formula: '积分+1' }];
    configured.giftCatalog = [{ id: 1, name: '测试礼物', price: 100, coinType: 'gold', imgBasic: '' }];
    configured.recentGifts = [{ ...configured.giftCatalog[0], lastReceived: 1, count: 1 }];
    configured.stats = { today: { date: 'today', giftTotals: { 1: 1 }, ruleTriggers: { r1: 1 } } };
    configured.log = [{ time: 1, giftId: 1, giftName: '测试礼物', num: 1, uname: '观众', attributeName: '积分', delta: 1, valueAfter: 7, ruleId: 'r1' }];
    configured.contributions = { viewers: [{ key: 'uid:1', uid: 1, uname: '观众', giftCount: 1, goldValue: 100, silverValue: 0, ruleTriggers: 1, attributeDeltas: { 积分: 1 }, blindBoxCount: 0, blindBoxCost: 0, blindBoxValue: 0, blindBoxProfit: 0, lastGiftAt: 1 }], updatedAt: 1 };
    await saveState(configured);
    const confirmSwitch = vi.fn(() => false);
    vi.stubGlobal('confirm', confirmSwitch);
    const root = new TestElement('div');
    mountConfig(root as unknown as HTMLElement);

    const input = root.querySelectorAll('input').find((item) => item.dataset.fieldLabel === '房间号') as TestElement;
    input.value = '200';
    await (findByText(root, '连接')?.onclick as (() => Promise<void>) | undefined)?.();
    expect(confirmSwitch).toHaveBeenCalledWith(expect.stringContaining('切换后会清空最近礼物'));
    expect(loadState().roomId).toBe('100');
    expect(loadState().log).toHaveLength(1);
    expect(input.value).toBe('100');

    confirmSwitch.mockReturnValue(true);
    input.value = '200';
    await (findByText(root, '连接')?.onclick as (() => Promise<void>) | undefined)?.();
    expect(loadState().roomId).toBe('200');
    expect(loadState().recentGifts).toEqual([]);
    expect(loadState().stats).toEqual({});
    expect(loadState().log).toEqual([]);
    expect(loadState().contributions.viewers).toEqual([]);
    expect(loadState().attributes).toHaveLength(1);
    expect(loadState().rules).toHaveLength(1);
    expect(loadState().giftCatalog).toHaveLength(1);
  });

  it('shows the broadcaster identity resolved from the configured room', async () => {
    const configured = defaultState();
    configured.settings.showTutorial = false;
    configured.roomId = '31567150';
    await saveState(configured);
    const root = new TestElement('div');

    mountConfig(root as unknown as HTMLElement);

    await vi.waitFor(() => expect(textOf(root)).toContain('测试主播'));
    expect(textOf(root)).toContain('主播 UID 32249588');
    expect((root.querySelector('.room-anchor-avatar') as any)?.src).toBe('https://example.test/anchor.png');
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
    findByText(root, '连接')?.onclick?.();

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
    const connectButton = Array.from(root.querySelectorAll('button')).find((button) => button.textContent === '连接');
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
    const currentGift = { id: 970002, name: '同名礼物', price: 5200, coinType: 'gold' as const, imgBasic: 'https://example.com/current.png', listed: true };
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

  it('shows listed and observed gifts by default while keeping historical gifts search-only', async () => {
    const listedGift = {
      id: 980001, name: '状态测试礼物 A', price: 100, coinType: 'gold' as const, imgBasic: '', listed: true,
    };
    const historicalGift = {
      id: 980002, name: '状态测试礼物 B', price: 200, coinType: 'gold' as const, imgBasic: '', listed: false,
    };
    const observedCatalogGift = {
      id: 980003, name: '状态测试礼物 C', price: 300, coinType: 'gold' as const, imgBasic: '', listed: false,
    };
    const manualGift = {
      id: 980004, name: '状态测试礼物 D', price: 400, coinType: 'gold' as const, imgBasic: '',
    };
    await saveState({
      ...state('88888888'),
      settings: { ...defaultState().settings, showTutorial: false },
      recentGifts: [
        {
          id: observedCatalogGift.id,
          name: observedCatalogGift.name,
          price: observedCatalogGift.price,
          coinType: observedCatalogGift.coinType,
          imgBasic: observedCatalogGift.imgBasic,
          lastReceived: 1700000000,
          count: 1,
        },
        { ...manualGift, lastReceived: 0, count: 0 },
      ],
    });
    const fetchMock = vi.fn(async (input: string | URL | Request) => {
      const url = String(input);
      if (url === '/api/gifts?roomId=88888888') {
        return Response.json({ code: 0, gifts: [listedGift, historicalGift, observedCatalogGift] });
      }
      if (url.includes('/api/runtime')) return Response.json({ code: 0, runtime: { state: 'idle', roomId: '' } });
      if (url.includes('/api/auth/status')) return Response.json({ code: 0, auth: { state: 'anonymous' } });
      return new Response(null, { status: 204 });
    });
    vi.stubGlobal('fetch', fetchMock);
    const root = new TestElement('div');
    mountConfig(root as unknown as HTMLElement);
    await vi.waitFor(() => expect(fetchMock).toHaveBeenCalledWith('/api/gifts?roomId=88888888', { cache: 'no-store' }));
    await Promise.resolve();
    findByText(root, '编辑')?.onclick?.();

    let choices: TestElement[] = [];
    await vi.waitFor(() => {
      choices = root.querySelectorAll('.gift-choice')
        .filter((choice) => textOf(choice).includes('状态测试礼物'));
      const visibleIds = choices.map((choice) => choice.dataset.giftId);
      expect(visibleIds).toContain(String(listedGift.id));
      expect(visibleIds).toContain(String(observedCatalogGift.id));
      expect(visibleIds).not.toContain(String(historicalGift.id));
      expect(visibleIds).not.toContain(String(manualGift.id));
    });
    const defaultById = new Map(choices.map((choice) => [choice.dataset.giftId, choice]));
    expect(defaultById.has(String(listedGift.id))).toBe(true);
    expect(defaultById.has(String(observedCatalogGift.id))).toBe(true);
    expect(defaultById.has(String(historicalGift.id))).toBe(false);
    expect(defaultById.has(String(manualGift.id))).toBe(false);
    expect(defaultById.get(String(listedGift.id))?.querySelector('.gift-listing-status')).toBeNull();
    expect(textOf(defaultById.get(String(observedCatalogGift.id))?.querySelector('.gift-listing-status')!))
      .toBe('直播中收到过');

    const searchInput = root.querySelector('.gift-search') as TestElement & { oninput?: () => void };
    searchInput.value = '状态测试礼物';
    searchInput.oninput?.();
    choices = root.querySelectorAll('.gift-choice')
      .filter((choice) => textOf(choice).includes('状态测试礼物'));
    expect(choices).toHaveLength(4);
    const searchedById = new Map(choices.map((choice) => [choice.dataset.giftId, choice]));
    expect(textOf(searchedById.get(String(listedGift.id))?.querySelector('.gift-listing-status')!)).toBe('已上架');
    expect(textOf(searchedById.get(String(observedCatalogGift.id))?.querySelector('.gift-listing-status')!))
      .toBe('直播中收到过');
    expect(textOf(searchedById.get(String(historicalGift.id))?.querySelector('.gift-listing-status')!)).toBe('历史礼物');
    expect(textOf(searchedById.get(String(manualGift.id))?.querySelector('.gift-listing-status')!)).toBe('历史礼物');
  });

  it('uses themed scrollbars and keeps focused inputs inside their existing border', () => {
    const configCss = readFileSync(new URL('../src/ui/config/config.css', import.meta.url), 'utf8');

    expect(configCss).toMatch(/\.config-root input:focus-visible,[\s\S]*?outline: none;/);
    expect(configCss).toContain('scrollbar-color: var(--scrollbar-thumb) var(--scrollbar-track);');
    expect(configCss).toContain('::-webkit-scrollbar-thumb');
    expect(configCss).toContain('.config-root .timer-interval-row:focus-within');
    expect(configCss).toContain('-webkit-appearance: none;');
  });

  it('keeps dense gift rows and separates advanced rule controls', () => {
    const configCss = readFileSync(new URL('../src/ui/config/config.css', import.meta.url), 'utf8');

    expect(configCss).toMatch(/\.gift-picker-drawer \.gift-picker-grid \{[^}]*align-content: start;/);
    expect(configCss).toMatch(/\.gift-choice \{[^}]*height: 60px;/);
    expect(configCss).toMatch(/\.rule-advanced-settings \.formula-examples \{[^}]*margin: 12px 13px 10px;/);
    expect(configCss).toMatch(/\.formula-help \{[^}]*margin: 16px 0 14px;/);
  });

  it('separates template rule cards from the timer explanation', () => {
    const configCss = readFileSync(new URL('../src/ui/config/config.css', import.meta.url), 'utf8');

    expect(configCss).toMatch(/\.template-result-no-timer \{[^}]*margin-top: 12px;/);
  });

  it('keeps tutorial spotlights above template and attribute workspaces', () => {
    const configCss = readFileSync(new URL('../src/ui/config/config.css', import.meta.url), 'utf8');

    expect(configCss).toMatch(/\.config-root \.tour-prototype \{[\s\S]*?z-index: 130;/);
    expect(configCss).toContain('.config-root .tour-prototype.is-modal-step { z-index: 170; }');
    expect(configCss).toContain('.config-root .tour-prototype.is-modal-step .tour-focus { z-index: 171; }');
    expect(configCss).toContain('.config-root .tour-prototype.is-modal-step .tour-target-outline { z-index: 172; }');
    expect(configCss).toContain('.config-root .tour-prototype.is-modal-step .tour-bubble { z-index: 173; }');
    expect(configCss).toMatch(/\.config-root \.tour-focus \{[\s\S]*?border: 0;[\s\S]*?box-shadow: 0 0 0 100vmax[\s\S]*?transition: none;/);
    expect(configCss).toMatch(/\.config-root \.tour-target-outline \{[\s\S]*?border: 2px solid var\(--accent\);[\s\S]*?box-shadow: none;[\s\S]*?transition: none;/);
    expect(configCss).toMatch(/\.config-root \.hover-detail-card\.is-guide-expanded,[\s\S]*?\.config-root \.hover-detail-card\.is-guide-expanded \.hover-detail-panel \{[\s\S]*?transition: none !important;/);
    expect(configCss).toContain('.config-root .template-wizard-overlay { z-index: 145;');
    expect(configCss).toMatch(/\.config-root \.overlay\.attribute-overlay \{[\s\S]*?z-index: 148;/);
  });

  it('locks non-target cards while the tutorial owns an expanded attribute', () => {
    const configCss = readFileSync(new URL('../src/ui/config/config.css', import.meta.url), 'utf8');
    const guideSource = readFileSync(new URL('../src/ui/config/spotlight-guide.ts', import.meta.url), 'utf8');

    expect(guideSource).toMatch(/frame\.classList\.toggle\(\s*'is-card-detail-step',\s*context\.lesson === 'enable' \|\| context\.lesson === 'output',?\s*\)/);
    expect(configCss).toMatch(/\.config-root:has\(\.tour-prototype\.is-card-detail-step\)[\s\S]*?\.hover-detail-card:not\(\.is-guide-expanded\) \{[\s\S]*?pointer-events: none;[\s\S]*?transform: none !important;/);
    expect(configCss).toMatch(/\.config-root:has\(\.tour-prototype\.is-card-detail-step\)[\s\S]*?\.hover-detail-card:not\(\.is-guide-expanded\) \.hover-detail-panel \{[\s\S]*?opacity: 0 !important;[\s\S]*?visibility: hidden !important;[\s\S]*?pointer-events: none !important;/);
  });

  it('keeps expanded cards centered and scene editor fieldsets aligned', () => {
    const configCss = readFileSync(new URL('../src/ui/config/config.css', import.meta.url), 'utf8');
    const configSource = readFileSync(new URL('../src/ui/config/config.ts', import.meta.url), 'utf8');

    expect(configCss).toContain('.config-root .attribute-card.is-detail-persisted > .attribute-card-title');
    expect(configCss).toContain('justify-content: center;');
    expect(configCss).toMatch(/\.display-theme-setting\.display-scene-theme-control \{[^}]*padding: 16px;/);
    expect(configCss).toContain('.config-root .display-theme-setting > .field-label');
    expect(configSource).toContain("return el('div', { class: 'field setting-control display-theme-setting', role: 'group'");
    expect(configCss).toMatch(/\.display-scene-card\.is-detail-persisted \.display-scene-preview \{[\s\S]*?overflow: hidden;[\s\S]*?border-radius: 15px;/);
  });

  it('runs the game-style overtime tutorial through every workspace', async () => {
    const root = new TestElement('div');
    mountConfig(root as unknown as HTMLElement);

    const roomInput = root.querySelectorAll('input')
      .find((input) => input.dataset.fieldLabel === '房间号') as TestElement & { oninput?: () => void };
    roomInput.value = '31567150';
    roomInput.oninput?.();
    mockedRuntimeState = 'connected';
    await findByText(root, '连接')?.onclick?.();
    expect(textOf(root)).toContain('打开属性创建中心');
    (root.querySelector('.guide-attribute-add') as TestElement | null)?.onclick?.();
    expect(root.querySelector('.template-wizard-overlay')).not.toBeNull();
    expect(textOf(root)).toContain('从空白创建，完整练习一次');
    (root.querySelector('.guide-blank-template') as TestElement | null)?.onclick?.();
    expect(root.querySelector('.attribute-modal')).not.toBeNull();
    expect(textOf(root)).toContain('套用加班机模板');

    findByText(root, '使用加班机模板')?.onclick?.();
    expect((root.querySelector('.workbench-lesson-card') as TestElement & { hidden?: boolean }).hidden).toBe(true);
    (root.querySelector('.guide-add-gift') as TestElement | null)?.onclick?.();
    root.querySelector('.gift-choice')?.onclick?.();
    root.querySelector('.guide-confirm-gifts')?.onclick?.();
    (root.querySelector('.guide-rule-simulator') as TestElement | null)?.onclick?.();
    await vi.waitFor(() => expect(textOf(root.querySelector('.formula-preview')!)).toContain('已模拟 1 个'));
    expect(textOf(root.querySelector('.tour-bubble') as TestElement)).toContain('决定礼物如何改变时间');
    expect(textOf(root.querySelector('.tour-bubble') as TestElement)).toContain('核对预览里的原值和新值');
    const rulePreviewConfirm = root.querySelector('.guide-rule-preview-confirm') as TestElement | null;
    expect(rulePreviewConfirm).not.toBeNull();
    rulePreviewConfirm?.onclick?.();
    await vi.waitFor(() => expect(textOf(root)).toContain('把这条规则保存为预设'));
    const advancedRule = root.querySelector('.rule-advanced-settings') as TestElement & { open?: boolean };
    expect(advancedRule.open).toBe(true);
    expect(advancedRule.querySelector('.guide-save-preset')).not.toBeNull();
    (root.querySelector('.guide-save-preset') as TestElement | null)?.onclick?.();
    const presetNameDialog = root.querySelector('.formula-preset-name-dialog') as TestElement | null;
    expect(presetNameDialog).not.toBeNull();
    expect(presetNameDialog?.querySelector('.guide-preset-confirm')).not.toBeNull();
    expect(textOf(root.querySelector('.tour-bubble') as TestElement)).toContain('输入预设名称');
    await findByText(root, '保存')?.onclick?.();
    await vi.waitFor(() => expect(textOf(root)).toContain('让时间自动减少'));
    const activeWorkspaceTab = root.querySelectorAll('.attribute-workbench-tab')
      .find((tab) => tab.className.split(' ').includes('is-active'));
    expect(activeWorkspaceTab).toBeDefined();
    expect(textOf(activeWorkspaceTab!)).toContain('定时器');
    expect(textOf(root.querySelector('.formula-preview')!)).toContain('已模拟 1 个');

    findByText(root, '+ 添加定时器')?.onclick?.();
    const timerEditor = root.querySelector('.timer-rule-editor') as TestElement;
    (root.querySelector('.guide-timer-simulator') as TestElement | null)?.onclick?.();
    await vi.waitFor(() => expect(textOf(timerEditor.querySelector('.formula-preview')!)).toContain('已模拟执行'));
    expect(textOf(root.querySelector('.tour-bubble') as TestElement)).toContain('让时间自动减少');
    expect(root.querySelector('.guide-timer-preview-confirm')).not.toBeNull();
    (root.querySelector('.guide-timer-preview-confirm') as TestElement | null)?.onclick?.();
    await vi.waitFor(() => expect(textOf(root)).toContain('检查 OBS 中会显示什么'));
    expect(root.querySelector('.guide-output-confirm')).not.toBeNull();
    (root.querySelector('.guide-output-confirm') as TestElement | null)?.onclick?.();
    await vi.waitFor(() => expect(textOf(root)).toContain('保存并交给后台校验'));
    (root.querySelector('.guide-attribute-save') as TestElement | null)?.onclick?.();

    await vi.waitFor(() => expect(JSON.parse(storage.get('bilibili-live-gift-panel-v1')!).rules).toHaveLength(1));
    const saved = JSON.parse(storage.get('bilibili-live-gift-panel-v1')!);
    expect(saved.attributes).toHaveLength(1);
    expect(saved.rules).toHaveLength(1);
    expect(saved.timerRules).toHaveLength(1);
    expect(saved.formulaPresets).toHaveLength(1);
    expect(saved.rules[0].enabled).toBe(false);

    expect(root.querySelector('.attribute-card')?.className.split(' ')).toContain('is-guide-expanded');
    expect(root.querySelector('.tour-prototype')?.className.split(' ')).toContain('is-card-detail-step');
    expect(root.querySelector('.guide-attribute-detail')).not.toBeNull();
    (root.querySelector('.guide-rule-toggle') as TestElement | null)?.onclick?.();
    expect(textOf(root.querySelector('.tour-bubble') as TestElement)).toContain('托盘后台会继续收礼');

    await (root.querySelector('.guide-obs-copy') as TestElement | null)?.onclick?.();
    await vi.waitFor(() => expect(JSON.parse(storage.get('bilibili-live-gift-panel-v1')!).settings.showTutorial).toBe(false));
    expect(root.querySelector('.attribute-card')?.className.split(' ')).not.toContain('is-guide-expanded');

    findByText(root, '编辑')?.onclick?.();
    expect(root.querySelector('.attribute-modal')).not.toBeNull();
    expect(root.querySelector('.tour-bubble')).toBeNull();
    expect(textOf(root)).not.toContain('补充礼物和规则');

    const reopenedRoot = new TestElement('div');
    mountConfig(reopenedRoot as unknown as HTMLElement);
    expect(reopenedRoot.querySelector('.tour-bubble')).toBeNull();
  });

  it('provides a searchable training center without stacking it over the spotlight guide', async () => {
    const root = new TestElement('div');
    mountConfig(root as unknown as HTMLElement);

    expect(root.querySelector('.tour-bubble')).not.toBeNull();
    (root.querySelector('.training-toggle') as TestElement | null)?.onclick?.();

    const center = root.querySelector('.training-center') as TestElement;
    expect(center).not.toBeNull();
    expect(root.querySelector('.tour-bubble')).toBeNull();
    expect(textOf(center)).toContain('实战主线');
    expect(textOf(center)).toContain('进阶玩法');
    expect(textOf(center)).toContain('排查问题');
    expect(textOf(center)).toContain('正确处理盲盒礼物');

    findByText(center, '排查问题')?.onclick?.();
    expect(textOf(center)).toContain('礼物到了但数值没变化');
    expect(textOf(center)).not.toContain('多个礼物影响同一属性');

    const search = center.querySelector('.training-center-search') as TestElement & { oninput?: () => void };
    search.value = 'OBS 没更新';
    search.oninput?.();
    expect(textOf(center)).toContain('OBS 面板没有更新');
    expect(textOf(center)).not.toContain('定时器为什么没有运行');

    const obsCourse = center.querySelectorAll('.training-center-course')
      .find((course) => textOf(course).includes('OBS 面板没有更新'));
    obsCourse?.onclick?.();
    findByText(center, '标记已掌握')?.onclick?.();
    expect(findByText(center, '标为未掌握')).toBeDefined();
    expect(textOf(center.querySelector('.training-center-summary') as TestElement)).toContain('专题 1/13');
    await vi.waitFor(() => expect(loadState().settings.trainingCompletedTopics).toContain('obs-no-change'));

    (center.querySelector('.modal-close') as TestElement | null)?.onclick?.();
    expect(root.querySelector('.training-center')).toBeNull();
    expect(root.querySelector('.tour-bubble')).not.toBeNull();
  });

  it('collapses guided attribute details after resetting the main tutorial', async () => {
    const configured = defaultState();
    configured.roomId = '31567150';
    configured.attributes.push({
      name: '加班时间', value: 0, unit: 'seconds', format: 'hhmmss', decimals: 0, suffix: '', broadcastMessage: '',
    });
    configured.rules.push({
      id: 'rule-1', giftId: 1, attributeName: '加班时间', formulaName: '加时', formula: '加班时间+60', enabled: true,
    });
    configured.timerRules.push({
      id: 'timer-1', attributeName: '加班时间', formulaName: '自动减少', intervalSeconds: 1,
      condition: '加班时间>0', formula: 'MAX(加班时间-1,0)', enabled: true,
    });
    configured.formulaPresets.push({
      id: 'preset-1', name: '每元加时', context: 'gift', formula: '加班时间+price/1000*60', sourceAttributeName: '加班时间',
    });
    configured.settings.showTutorial = false;
    await saveState(configured);
    mockedRuntimeState = 'connected';
    const root = new TestElement('div');

    mountConfig(root as unknown as HTMLElement);
    await vi.waitFor(() => expect(textOf(root)).toContain('已连接'));
    expect(root.querySelector('.attribute-card')?.className.split(' ')).not.toContain('is-guide-expanded');

    (root.querySelector('.training-toggle') as TestElement | null)?.onclick?.();
    const center = root.querySelector('.training-center') as TestElement;
    findByText(center, '重置主线进度')?.onclick?.();

    expect(root.querySelector('.training-center')).toBeNull();
    expect(textOf(root.querySelector('.tour-bubble') as TestElement)).toContain('打开属性创建中心');
    expect(root.querySelector('.attribute-card')?.className.split(' ')).not.toContain('is-guide-expanded');
    expect(loadState().settings.tutorialReplayMode).toBe(true);
  });

  it('replays the enable lesson on the overtime training attribute instead of the first card', async () => {
    const configured = defaultState();
    configured.roomId = '31567150';
    configured.attributes.push(
      { name: '挑战次数', value: 10, unit: 'none', format: 'suffix', decimals: 0, suffix: '次' },
      { name: '红队', value: 0, unit: 'none', format: 'suffix', decimals: 0, suffix: '分' },
      { name: '加班时间', value: 0, unit: 'seconds', format: 'hhmmss', decimals: 0, suffix: '' },
    );
    configured.rules.push(
      { id: 'rule-count', giftId: 1, attributeName: '挑战次数', formulaName: '增加计数', formula: '挑战次数+1', enabled: true },
      { id: 'rule-overtime', giftId: 2, attributeName: '加班时间', formulaName: '加时', formula: '加班时间+60', enabled: false },
    );
    configured.settings.showTutorial = false;
    configured.settings.tutorialCompletedLessons = TUTORIAL_LESSONS
      .map((lesson) => lesson.id)
      .filter((lesson) => lesson !== 'enable' && lesson !== 'output');
    await saveState(configured);
    mockedRuntimeState = 'connected';
    const root = new TestElement('div');

    mountConfig(root as unknown as HTMLElement);
    await vi.waitFor(() => expect(textOf(root)).toContain('已连接'));
    (root.querySelector('.training-toggle') as TestElement | null)?.onclick?.();
    const center = root.querySelector('.training-center') as TestElement;
    center.querySelectorAll('.training-center-course')
      .find((course) => textOf(course).includes('展开卡片并启用'))
      ?.onclick?.();
    findByText(center, '重新练习这一关')?.onclick?.();

    const guideCard = root.querySelector('.guide-attribute-card') as TestElement | null;
    expect(guideCard).not.toBeNull();
    expect(textOf(guideCard!)).toContain('加班时间');
    expect(textOf(guideCard!)).not.toContain('挑战次数');
    const saved = loadState();
    const target = saved.attributes.find((attribute) => attribute.name === '加班时间');
    expect(target?.id).toBeTruthy();
    expect(saved.settings.tutorialTargetAttributeId).toBe(target?.id);

    (root.querySelector('.guide-rule-toggle') as TestElement | null)?.onclick?.();
    await vi.waitFor(() => {
      expect(textOf(root.querySelector('.tour-bubble') as TestElement)).toContain('把面板放进 OBS');
    });
  });

  it('restarts from attribute creation when a late tutorial lesson has no overtime card', async () => {
    const configured = defaultState();
    configured.roomId = '31567150';
    configured.attributes.push({
      name: '挑战次数', value: 10, unit: 'none', format: 'suffix', decimals: 0, suffix: '次',
    });
    configured.rules.push({
      id: 'rule-count', giftId: 1, attributeName: '挑战次数', formulaName: '增加计数', formula: '挑战次数+1', enabled: true,
    });
    configured.settings.showTutorial = false;
    configured.settings.tutorialCompletedLessons = TUTORIAL_LESSONS.map((lesson) => lesson.id);
    await saveState(configured);
    mockedRuntimeState = 'connected';
    const root = new TestElement('div');

    mountConfig(root as unknown as HTMLElement);
    await vi.waitFor(() => expect(textOf(root)).toContain('已连接'));
    (root.querySelector('.training-toggle') as TestElement | null)?.onclick?.();
    const center = root.querySelector('.training-center') as TestElement;
    center.querySelectorAll('.training-center-course')
      .find((course) => textOf(course).includes('展开卡片并启用'))
      ?.onclick?.();
    findByText(center, '重新练习这一关')?.onclick?.();

    expect(textOf(root.querySelector('.tour-bubble') as TestElement)).toContain('打开属性创建中心');
    expect(root.querySelector('.guide-attribute-card')).toBeNull();
    expect(loadState().settings.tutorialReplayMode).toBe(true);
    expect(loadState().settings.tutorialCompletedLessons).toEqual([]);
  });

  it('keeps live workspaces on one page and opens program settings from the header', () => {
    const root = new TestElement('div');
    mountConfig(root as unknown as HTMLElement);

    expect(root.querySelector('.connection-grid')).not.toBeNull();
    expect(root.querySelector('.obs-card')).toBeNull();
    expect(root.querySelector('.attributes-section')).not.toBeNull();
    expect(root.querySelector('.advanced-settings')).toBeNull();
    expect(root.querySelector('.data-settings-card')).toBeNull();
    expect(root.querySelector('.wizard-progress')).toBeNull();
    expect(root.querySelector('.normal-nav')).toBeNull();
    expect(root.querySelectorAll('h1')).toHaveLength(0);
    expect(textOf(root)).not.toContain('一页完成直播配置');
    expect(textOf(root)).not.toContain('把连接、属性和礼物放在同一页');

    const settingsButton = root.querySelector('.program-settings-toggle');
    expect(settingsButton?.getAttribute('aria-label')).toBe('程序与数据');
    settingsButton?.onclick?.();
    expect(root.querySelector('.program-settings-dialog')).not.toBeNull();
    expect(root.querySelector('.data-settings-card')).not.toBeNull();
    expect(textOf(root.querySelector('.program-settings-dialog') as TestElement)).toContain('配置与数据');
    expect(textOf(root.querySelector('.program-settings-dialog') as TestElement)).not.toContain('OBS 面板外观');

    (root.querySelector('.program-settings-close') as TestElement | null)?.onclick?.();
    expect(root.querySelector('.program-settings-dialog')).toBeNull();
  });

  it('shows automatic update status and supports a manual update check', async () => {
    const fetchMock = vi.fn(async (input: string | URL | Request) => {
      const url = String(input);
      if (url === '/api/update/check') {
        return Response.json({
          code: 0,
          update: {
            state: 'up-to-date', currentVersion: '1.0.0', latestVersion: '1.0.0', message: '当前已经是最新版本。',
            lastCheckedAt: 1785729600, autoUpdate: true, restartRequired: false,
          },
        }, { status: 202 });
      }
      if (url === '/api/update') {
        return Response.json({
          code: 0,
          update: {
            state: 'idle', currentVersion: '1.0.0', message: '尚未检查更新。', autoUpdate: true, restartRequired: false,
          },
        });
      }
      if (url.includes('/api/runtime')) return Response.json({ code: 0, runtime: { state: 'idle', roomId: '' } });
      if (url.includes('/api/auth/status')) return Response.json({ code: 0, auth: { state: 'anonymous' } });
      return new Response(null, { status: 204 });
    });
    vi.stubGlobal('fetch', fetchMock);
    const root = new TestElement('div');
    mountConfig(root as unknown as HTMLElement);
    (root.querySelector('.program-settings-toggle') as TestElement | null)?.onclick?.();

    expect(root.querySelector('.data-settings-card')?.querySelector('.update-settings-card')).not.toBeNull();
    const diagnosticLogLink = findByText(root, '导出运行日志') as (TestElement & { href?: string; download?: string }) | undefined;
    expect(diagnosticLogLink?.href).toBe('/api/diagnostics/log');
    expect(diagnosticLogLink?.download).toContain('gift-panel-runtime-');
    expect(textOf(root.querySelector('.diagnostic-log-note') as TestElement)).toContain('不包含 Cookie 或登录凭据');
    await vi.waitFor(() => expect(textOf(root.querySelector('.update-settings-card') as TestElement)).toContain('v1.0.0'));
    const autoUpdateInput = root.querySelector('.update-auto-switch')?.querySelector('input') as (TestElement & { checked?: boolean }) | null;
    expect(autoUpdateInput?.checked).toBe(true);
    const checkButton = findByText(root, '检查更新');
    expect(checkButton).toBeDefined();
    checkButton?.onclick?.();

    await vi.waitFor(() => expect(root.querySelector('.update-settings-card')?.dataset.updateState).toBe('up-to-date'));
    expect(textOf(root.querySelector('.update-settings-card') as TestElement)).toContain('当前已经是最新版本。');
    expect(fetchMock).toHaveBeenCalledWith('/api/update/check', { cache: 'no-store', method: 'POST' });
  });

  it('opens a visual changelog manually and remembers the latest viewed version', async () => {
    const root = new TestElement('div');
    mountConfig(root as unknown as HTMLElement);

    const changelogButton = root.querySelector('.changelog-toggle');
    expect(changelogButton).not.toBeNull();
    expect(changelogButton?.getAttribute('aria-label')).toBe('更新日志');
    changelogButton?.onclick?.();

    const dialog = root.querySelector('.changelog-dialog');
    expect(dialog).not.toBeNull();
    expect(textOf(dialog!)).toContain('这次更新了什么？');
    expect(textOf(dialog!)).toContain('新增礼物目标面板');
    expect(root.querySelectorAll('.changelog-visual')).toHaveLength(1);
    (root.querySelector('.changelog-close') as TestElement | null)?.onclick?.();

    await vi.waitFor(() => expect(loadState().settings.lastSeenChangelogVersion).toBe('0.2.3'));
    expect(root.querySelector('.changelog-dialog')).toBeNull();
  });

  it('shows the installed version changelog only once', async () => {
    const fetchMock = vi.fn(async (input: string | URL | Request) => {
      const url = String(input);
      if (url === '/api/update') {
        return Response.json({
          code: 0,
          update: {
            state: 'up-to-date', currentVersion: '0.2.3', latestVersion: '0.2.3',
            message: '当前已经是最新版本。', autoUpdate: true, restartRequired: false,
          },
        });
      }
      if (url.includes('/api/runtime')) return Response.json({ code: 0, runtime: { state: 'idle', roomId: '' } });
      if (url.includes('/api/auth/status')) return Response.json({ code: 0, auth: { state: 'anonymous' } });
      return new Response(null, { status: 204 });
    });
    vi.stubGlobal('fetch', fetchMock);
    const configured = defaultState();
    configured.settings.showTutorial = false;
    storage.set('bilibili-live-gift-panel-v1', JSON.stringify(configured));

    const firstRoot = new TestElement('div');
    mountConfig(firstRoot as unknown as HTMLElement);
    await vi.waitFor(() => expect(firstRoot.querySelector('.changelog-dialog')).not.toBeNull());
    (firstRoot.querySelector('.changelog-close') as TestElement | null)?.onclick?.();
    await vi.waitFor(() => expect(loadState().settings.lastSeenChangelogVersion).toBe('0.2.3'));

    const secondRoot = new TestElement('div');
    mountConfig(secondRoot as unknown as HTMLElement);
    await new Promise((resolve) => nativeSetTimeout(resolve, 25));
    expect(secondRoot.querySelector('.changelog-dialog')).toBeNull();
  });

  it('offers optional streamer login while keeping masked-name fallback explicit', async () => {
    const root = new TestElement('div');
    mountConfig(root as unknown as HTMLElement);

    await vi.waitFor(() => expect(root.querySelector('.login-card')).not.toBeNull());
    expect(textOf(root)).toContain('可选登录');
    expect(textOf(root)).toContain('匿名模式');
    expect(textOf(root)).toContain('登录后可以');
    expect(textOf(root)).toContain('自动识别盲盒及实际开出的礼物');
    expect(textOf(root)).toContain('驱动 OBS 盈亏榜');
    expect(textOf(root)).toContain('尽量补全送礼人的昵称和头像');
    expect(textOf(root)).toContain('普通 B 站账号即可，不一定要主播本人');
    expect(textOf(root)).toContain('不登录仍能连接直播间和执行礼物规则');
    expect(textOf(root)).toContain('盲盒盈亏榜依赖登录');
    expect(findByText(root, '扫码登录')).not.toBeNull();
  });

  it('shows one spotlight bubble over the next key UI', () => {
    const root = new TestElement('div');
    mountConfig(root as unknown as HTMLElement);

    expect(root.querySelector('.tour-focus')).not.toBeNull();
    expect(root.querySelector('.tour-target-outline')).not.toBeNull();
    expect(root.querySelector('.tour-bubble')).not.toBeNull();
    expect(textOf(root)).toContain('填写你的直播间房间号');
    expect(root.querySelector('.tour-switcher')).toBeNull();
    expect(root.querySelector('.tour-rail')).toBeNull();
  });

  it('requires the highlighted UI to be operated instead of proxying it from the bubble', () => {
    const root = new TestElement('div');
    mountConfig(root as unknown as HTMLElement);

    const bubble = root.querySelector('.tour-bubble') as TestElement;
    expect(bubble).not.toBeNull();
    expect(bubble.querySelector('.tour-bubble-action')).toBeNull();
    expect(bubble.querySelectorAll('button')).toHaveLength(2);
    expect(textOf(bubble)).toContain('先观察');
    expect(textOf(bubble)).toContain('亲手填写房间号');
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
    const editorFooter = root.querySelector('.attribute-workbench-actions') as TestElement & { hidden?: boolean };
    const giftDrawer = root.querySelector('.gift-picker-drawer') as TestElement & { hidden?: boolean };
    findByText(root, '+ 添加礼物')?.onclick?.();
    expect(giftDrawer.hidden).toBe(false);
    expect(editorFooter.hidden).toBe(true);
    giftDrawer.querySelector('.gift-choice')?.onclick?.();
    expect(textOf(giftDrawer)).toContain('确认选择（1）');
    findByText(giftDrawer, '取消')?.onclick?.();
    expect(giftDrawer.hidden).toBe(true);
    expect(editorFooter.hidden).toBe(false);
    expect(root.querySelectorAll('.selected-gift-rule')).toHaveLength(0);

    findByText(root, '+ 添加礼物')?.onclick?.();
    const choices = giftDrawer.querySelectorAll('.gift-choice');
    expect(choices.length).toBeGreaterThan(1);
    choices[0].onclick?.();
    choices[1].onclick?.();
    root.querySelector('.guide-confirm-gifts')?.onclick?.();
    expect(giftDrawer.hidden).toBe(true);
    expect(editorFooter.hidden).toBe(false);
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

  it('offers common beginner rule actions with a safe optional upper limit', () => {
    storage.set('bilibili-live-gift-panel-v1', JSON.stringify(state('88888888')));
    const root = new TestElement('div');
    mountConfig(root as unknown as HTMLElement);

    findByText(root, '编辑')?.onclick?.();
    findByText(root, '+ 添加礼物')?.onclick?.();
    root.querySelector('.gift-choice')?.onclick?.();
    root.querySelector('.guide-confirm-gifts')?.onclick?.();

    const operation = root.querySelector('.quick-rule-operation') as TestElement & { onchange?: () => void };
    const formula = root.querySelectorAll('input')
      .find((input) => input.dataset.fieldLabel === '触发后属性值') as TestElement;
    expect(textOf(operation)).toContain('让“加班时间”减少（最低为 0）');
    expect(textOf(operation)).toContain('把“加班时间”清零');
    expect(textOf(operation)).toContain('每 1 元让“加班时间”减少（最低为 0）');
    expect(textOf(operation)).toContain('让“加班时间”随机减少 1 到（最低为 0）');

    operation.value = 'subtract';
    operation.onchange?.();
    expect(formula.value).toBe('MAX(加班时间-60,0)');
    operation.value = 'priceSubtract';
    operation.onchange?.();
    expect(formula.value).toBe('MAX(加班时间-price/1000*60,0)');
    operation.value = 'reset';
    operation.onchange?.();
    expect(formula.value).toBe('0');

    operation.value = 'add';
    operation.onchange?.();
    const limit = root.querySelector('.quick-rule-limit')!;
    const toggle = limit.querySelector('.setting-switch-input') as TestElement & { checked?: boolean; onchange?: () => void };
    const maximum = root.querySelectorAll('input')
      .find((input) => input.dataset.fieldLabel === '最高不超过') as TestElement & { oninput?: () => void };
    toggle.checked = true;
    maximum.value = '3600';
    maximum.oninput?.();
    expect(formula.value).toBe('MIN(加班时间+60,3600)');
  });

  it('advances the editable value when a gift rule is simulated without saving live state', async () => {
    storage.set('bilibili-live-gift-panel-v1', JSON.stringify({
      ...state('88888888', 1),
      rules: [{
        id: 'r-preview', giftId: 1, attributeName: '加班时间',
        formulaName: '模拟加一', formula: '加班时间+1', enabled: true,
      }],
    }));
    const root = new TestElement('div');
    mountConfig(root as unknown as HTMLElement);

    findByText(root, '编辑')?.onclick?.();
    const giftRulesTab = root.querySelectorAll('.attribute-workbench-tab')
      .find((tab) => textOf(tab).includes('礼物规则'));
    giftRulesTab?.onclick?.();

    const currentValue = root.querySelectorAll('input')
      .find((input) => input.dataset.fieldLabel === '当前值') as TestElement;
    const simulate = findByText(root, '模拟收到 1 个');
    expect(currentValue.value).toBe('0');
    expect(simulate).toBeDefined();

    simulate?.onclick?.();

    await vi.waitFor(() => expect(currentValue.value).toBe('1'));
    expect(textOf(root.querySelector('.formula-preview')!)).toContain('已模拟 1 个');
    await new Promise((resolve) => nativeSetTimeout(resolve, 60));
    expect(textOf(root.querySelector('.formula-preview')!)).toContain('已模拟 1 个');
    expect(loadState().attributes[0].value).toBe(0);
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
    mountDisplay(displayRoot as unknown as HTMLElement, { attributeName: '加班时间' });
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
      const writes = fetchMock.mock.calls.filter(([, init]) => init?.method === 'PATCH');
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
      if (url.endsWith('/api/config') && init?.method === 'PATCH') {
        const patch = JSON.parse(String(init.body));
        serverState = { ...serverState, ...patch };
        writtenStates.push(JSON.parse(JSON.stringify(serverState)));
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

  it('preserves every floating card while a backend timer updates only runtime values and timer logs', async () => {
    const initialState = defaultState();
    initialState.settings.showTutorial = false;
    initialState.attributes = [
      { name: '加班时间', value: 10, unit: 'seconds', format: 'hhmmss', decimals: 0, suffix: '' },
      { name: '挑战次数', value: 2, unit: 'none', format: 'suffix', decimals: 0, suffix: '次' },
    ];
    initialState.timerRules = [{
      id: 'timer-overtime', attributeName: '加班时间', formulaName: '每秒减少', intervalSeconds: 1,
      formula: 'MAX(加班时间-1,0)', condition: '加班时间>0', enabled: true,
    }];
    initialState.activities = [{
      id: 'activity-1', name: '测试活动', attributeNames: ['加班时间', '挑战次数'], status: 'active',
      resultMode: 'highest', gateRules: false, initialValues: { 加班时间: 10, 挑战次数: 2 }, milestones: [],
    }];
    initialState.displayScenes = [{
      id: 'scene-1', name: '测试组合面板', attributeNames: ['加班时间', '挑战次数'], layout: 'grid', themeId: 'glass',
    }];
    await saveState(initialState);

    let serverState = JSON.parse(JSON.stringify(initialState));
    vi.stubGlobal('fetch', vi.fn(async (input: string | URL | Request) => {
      const url = String(input);
      if (url.endsWith('/api/config')) {
        return Response.json(serverState);
      }
      if (url.includes('/api/runtime')) {
        return Response.json({ code: 0, runtime: { state: 'idle', roomId: '' } });
      }
      if (url.includes('/api/auth/status')) {
        return Response.json({ code: 0, auth: { state: 'anonymous' } });
      }
      return new Response(null, { status: 204 });
    }));

    vi.useFakeTimers();
    const root = new TestElement('div');
    mountConfig(root as unknown as HTMLElement);
    const cardsBefore = root.querySelectorAll('.hover-detail-card');
    expect(cardsBefore).toHaveLength(4);

    serverState = JSON.parse(JSON.stringify(initialState));
    serverState.attributes[0].value = 9;
    serverState.log = [{
      time: 1, giftId: 0, giftName: '', num: 1, uname: '', attributeName: '加班时间',
      delta: -1, valueAfter: 9, ruleId: 'timer-overtime', source: 'timer', triggerName: '每秒减少',
    }];
    await vi.advanceTimersByTimeAsync(1000);
    await vi.advanceTimersByTimeAsync(0);

    const cardsAfter = root.querySelectorAll('.hover-detail-card');
    expect(cardsAfter.every((card, index) => card === cardsBefore[index])).toBe(true);
    expect(root.querySelector('.attribute-current-value')?.textContent).toBe('00:00:09');
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

  it('deletes an activity after a backend refresh replaces live values', async () => {
    const initialState = defaultState();
    initialState.settings.showTutorial = false;
    initialState.attributes = [
      { name: '红队', value: 0, unit: 'none', format: 'suffix', decimals: 0, suffix: '分' },
      { name: '蓝队', value: 0, unit: 'none', format: 'suffix', decimals: 0, suffix: '分' },
    ];
    initialState.activities = [{
      id: 'activity-delete', name: '红蓝阵营对战', attributeNames: ['红队', '蓝队'], status: 'not_started',
      resultMode: 'highest', gateRules: true, initialValues: { 红队: 0, 蓝队: 0 }, milestones: [],
    }];
    let serverState = JSON.parse(JSON.stringify(initialState));
    let pollConfig: (() => void) | undefined;
    vi.stubGlobal('setInterval', vi.fn((handler: TimerHandler, timeout?: number) => {
      if (timeout === 1000) pollConfig = handler as () => void;
      return 0;
    }));
    vi.stubGlobal('confirm', vi.fn(() => true));
    vi.stubGlobal('fetch', vi.fn(async (input: string | URL | Request, init?: RequestInit) => {
      const url = String(input);
      if (url.endsWith('/api/config') && init?.method === 'PATCH') {
        serverState = { ...serverState, ...JSON.parse(String(init.body ?? '{}')) };
        return Response.json({ code: 0 });
      }
      if (url.endsWith('/api/config')) return Response.json(serverState);
      if (url.includes('/api/runtime')) {
        return Response.json({ code: 0, runtime: { state: 'idle', roomId: '' } });
      }
      if (url.includes('/api/auth/status')) {
        return Response.json({ code: 0, auth: { state: 'anonymous' } });
      }
      return new Response(null, { status: 204 });
    }));
    await saveState(initialState);

    const root = new TestElement('div');
    mountConfig(root as unknown as HTMLElement);
    serverState = JSON.parse(JSON.stringify(initialState));
    serverState.attributes[0].value = 1;
    pollConfig?.();
    await new Promise((resolve) => nativeSetTimeout(resolve, 0));

    const activityCard = root.querySelector('.activity-card');
    const deleteActivity = findByText(activityCard ?? root, '删除')!;
    deleteActivity.onclick?.();
    expect(deleteActivity.textContent).toBe('确定');
    expect(serverState.activities).toHaveLength(1);
    deleteActivity.onclick?.();
    await new Promise((resolve) => nativeSetTimeout(resolve, 0));

    expect(serverState.activities).toEqual([]);
    expect(root.querySelector('.activity-card')).toBeNull();
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

    const confirmMock = vi.fn(() => true);
    vi.stubGlobal('confirm', confirmMock);
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

    expect(list.parent?.className).toContain('gift-history-list-frame');

    expect(list.querySelectorAll('.gift-history-row')).toHaveLength(40);
    expect(list.querySelector('.gift-history-loader')?.textContent).toContain('40 / 85');
    list.clientHeight = 300;
    list.scrollHeight = 900;
    list.scrollTop = 600;
    list.onscroll?.();
    expect(list.querySelectorAll('.gift-history-row')).toHaveLength(80);
    expect(list.querySelector('.gift-history-loader')?.textContent).toContain('80 / 85');
  });

  it('renders backend-owned contribution, rule-hit, and blind-box rankings', () => {
    storage.set('bilibili-live-gift-panel-v1', JSON.stringify({
      ...state('88888888', 1),
      giftCatalog: [
        { id: 990001, name: '心动盲盒', price: 9000, coinType: 'gold', imgBasic: 'https://example.com/heart-box.png' },
        { id: 990002, name: '小熊虫盲盒', price: 9000, coinType: 'gold', imgBasic: 'https://example.com/bear-box.png' },
      ],
      contributions: {
        updatedAt: 200,
        viewers: [
          {
            key: 'uid:1', uid: 1, uname: '盈利观众', giftCount: 5, goldValue: 20000, silverValue: 0,
            ruleTriggers: 3, attributeDeltas: { 加班时间: 180 }, blindBoxCount: 2,
            blindBoxCost: 18000, blindBoxValue: 24000, blindBoxProfit: 6000, lastGiftAt: 200,
            blindBoxes: [{
              giftId: 990001, giftName: '心动盲盒', count: 2, cost: 18000, value: 24000,
              profit: 6000, lastGiftAt: 200,
            }],
          },
          {
            key: 'name:反***', uname: '反***', giftCount: 2, goldValue: 10000, silverValue: 0,
            ruleTriggers: 0, attributeDeltas: {}, blindBoxCount: 1,
            blindBoxCost: 9000, blindBoxValue: 4000, blindBoxProfit: -5000, lastGiftAt: 100,
            blindBoxes: [{
              giftId: 990002, giftName: '小熊虫盲盒', count: 1, cost: 9000, value: 4000,
              profit: -5000, lastGiftAt: 100,
            }],
          },
        ],
      },
    }));
    const root = new TestElement('div');
    mountConfig(root as unknown as HTMLElement);

    expect(findByText(root, '复制 OBS 链接')).toBeDefined();
    expect(root.querySelectorAll('.contribution-row')).toHaveLength(2);
    expect(textOf(root.querySelector('.contribution-section') as TestElement)).toContain('20,000');
    const tabs = root.querySelectorAll('.contribution-tab');
    expect(tabs).toHaveLength(3);
    tabs[1].onclick?.();
    expect(root.querySelectorAll('.contribution-row')).toHaveLength(1);
    expect(textOf(root.querySelector('.contribution-list-host') as TestElement)).toContain('3 次规则命中');
    tabs[2].onclick?.();
    const blindText = textOf(root.querySelector('.contribution-list-host') as TestElement);
    expect(blindText).toContain('+6,000');
    expect(blindText).toContain('-5,000');
    const scopeOptions = root.querySelectorAll('.blind-box-scope-option');
    expect(scopeOptions).toHaveLength(3);
    expect(scopeOptions.map((option) => textOf(option))).toEqual([
      expect.stringContaining('全部盲盒'),
      expect.stringContaining('心动盲盒'),
      expect.stringContaining('小熊虫盲盒'),
    ]);
    const scopeImages = root.querySelectorAll('.blind-box-scope-option-image') as Array<TestElement & { src?: string }>;
    expect(scopeImages.map((image) => image.src)).toEqual([
      'https://example.com/heart-box.png',
      'https://example.com/bear-box.png',
    ]);
    scopeOptions[1].onclick?.();
    expect(root.querySelectorAll('.contribution-row')).toHaveLength(1);
    expect(textOf(root.querySelector('.blind-box-scope-bar') as TestElement)).toContain('心动盲盒 · 1 位观众 · 2 个');
    expect(textOf(root.querySelector('.blind-box-scope-trigger') as TestElement)).toContain('心动盲盒');
    expect(textOf(root.querySelector('.contribution-list-host') as TestElement)).toContain('+6,000');
    expect(textOf(root.querySelector('.contribution-list-host') as TestElement)).not.toContain('-5,000');
  });

  it('reserves enough horizontal space for the complete blind-box scope name', () => {
    const configCss = readFileSync(new URL('../src/ui/config/config.css', import.meta.url), 'utf8');

    expect(configCss).toMatch(/\.blind-box-scope-bar\s*\{[^}]*grid-template-columns:\s*max-content minmax\(0, 1fr\);/);
    expect(configCss).toMatch(/\.blind-box-scope-field\s*\{[^}]*grid-template-columns:\s*auto max-content;[^}]*width:\s*max-content;/);
    expect(configCss).toMatch(/\.blind-box-scope-picker\s*\{[^}]*position:\s*relative;[^}]*width:\s*max-content;/);
    expect(configCss).toMatch(/\.blind-box-scope-trigger\s*\{[^}]*grid-template-columns:\s*36px minmax\(0, 1fr\) 18px;/);
    expect(configCss).toMatch(/\.blind-box-scope-menu\s*\{[^}]*width:\s*max-content;[^}]*min-width:\s*100%;/);
  });

  it('configures 1–10 visible viewer slots for the blind-box OBS leaderboard', async () => {
    const configured = defaultState();
    configured.settings.showTutorial = false;
    storage.set('bilibili-live-gift-panel-v1', JSON.stringify(configured));
    const root = new TestElement('div');
    mountConfig(root as unknown as HTMLElement);

    root.querySelector('.contribution-obs-appearance')?.onclick?.();
    const range = root.querySelector('.blind-box-viewer-slots-range') as TestElement & { oninput?: () => void };
    expect(range.value).toBe('3');
    range.value = '8';
    range.oninput?.();
    expect(textOf(root.querySelector('.blind-box-viewer-slots-control') as TestElement)).toContain('8 个');
    findByText(root, '保存外观')?.onclick?.();
    await Promise.resolve();
    await Promise.resolve();

    expect(loadState().blindBoxDisplay.viewerSlots).toBe(8);
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

  it('renders compact 3D attribute covers with details reserved for hover or keyboard focus', () => {
    storage.set('bilibili-live-gift-panel-v1', JSON.stringify(state('88888888')));
    const root = new TestElement('div');
    mountConfig(root as unknown as HTMLElement);

    const card = root.querySelector('.attribute-card');
    const cover = card?.querySelector('.attribute-card-title');
    const details = card?.querySelector('.attribute-card-details');
    expect(card?.querySelector('.attribute-card-visual')).not.toBeNull();
    expect(cover?.querySelector('.attribute-current-value')).not.toBeNull();
    expect(cover?.querySelector('.attribute-actions')).toBeNull();
    expect(details?.querySelector('.attribute-actions')).not.toBeNull();
    expect(details?.querySelector('.attribute-formulas')).not.toBeNull();
    expect(details?.querySelector('.attribute-obs-row')).not.toBeNull();
    expect(details?.querySelector('.hover-detail-cover')).toBeNull();
    expect(card?.querySelector('.attribute-expand-hint')).toBeNull();

    const interactiveCover = cover as TestElement & {
      getBoundingClientRect?: () => { left: number; top: number; width: number; height: number };
    };
    const interactiveCard = card as TestElement & {
      getBoundingClientRect?: () => { left: number; top: number; right: number; bottom: number; width: number; height: number };
      onpointerenter?: () => void;
      onpointerleave?: () => void;
      onpointermove?: (event: { pointerType: string; clientX: number; clientY: number }) => void;
      onpointerdown?: (event: { pointerType: string }) => void;
      onkeydown?: () => void;
    };
    interactiveCover.getBoundingClientRect = () => ({ left: 100, top: 100, width: 200, height: 100 });
    interactiveCard.getBoundingClientRect = () => ({ left: 400, top: 200, right: 600, bottom: 300, width: 200, height: 100 });
    interactiveCard.onpointerenter?.();
    expect(card?.style['--hover-detail-offset-x']).toBe('-180px');
    interactiveCard.onpointermove?.({ pointerType: 'mouse', clientX: 280, clientY: 110 });
    expect(card?.style['--hover-card-rotate-x']).toBe('3.20deg');
    expect(card?.style['--hover-card-rotate-y']).toBe('4.00deg');
    interactiveCard.onpointerdown?.({ pointerType: 'mouse' });
    expect(card?.className.split(' ')).toContain('is-pointer-focus');
    interactiveCard.onkeydown?.();
    expect(card?.className.split(' ')).not.toContain('is-pointer-focus');

    interactiveCard.onpointerleave?.();
    interactiveCard.getBoundingClientRect = () => ({ left: 400, top: 600, right: 600, bottom: 700, width: 200, height: 100 });
    interactiveCard.onpointerenter?.();
    expect(card?.className.split(' ')).toContain('is-detail-above');
    card?.classList.add('is-guide-expanded');
    interactiveCard.onpointerleave?.();
    interactiveCard.getBoundingClientRect = () => ({ left: 400, top: 100, right: 600, bottom: 200, width: 200, height: 100 });
    interactiveCard.onpointerenter?.();
    expect(card?.className.split(' ')).toContain('is-detail-above');

    const configCss = readFileSync(new URL('../src/ui/config/config.css', import.meta.url), 'utf8');
    expect(configCss).toContain('perspective(1100px)');
    expect(configCss).toContain('@media (hover: hover)');
    expect(configCss).toContain('.config-root .hover-detail-card:hover .hover-detail-panel');
    expect(configCss).toContain('.config-root .hover-detail-card:not(.is-pointer-focus):focus-within .hover-detail-panel');
    expect(configCss).toMatch(/\.config-root \.hover-detail-panel \{[\s\S]*?position: absolute;/);
    expect(configCss).toMatch(/\.config-root \.hover-detail-panel \{[\s\S]*?transform-origin: 50% top;/);
    expect(configCss).toMatch(/\.config-root \.hover-detail-card\.is-detail-above \.hover-detail-panel \{[\s\S]*?transform-origin: 50% bottom;/);
    expect(configCss).toContain('.config-root .hover-detail-card.is-detail-restoring');
    expect(configCss).toMatch(/\.config-root \.hover-detail-card\.is-detail-restoring[\s\S]*?transition: none !important;/);
    expect(configCss).not.toMatch(/\.config-root \.attribute-list \{[^}]*perspective:/);
    expect(configCss).not.toMatch(/\.config-root \.activity-card-list \{[^}]*perspective:/);
    expect(configCss).not.toMatch(/\.config-root \.display-scene-list \{[^}]*perspective:/);
    expect(configCss).toContain('rotateX(var(--hover-card-rotate-x)) rotateY(var(--hover-card-rotate-y))');
    expect(configCss).toContain('@media (prefers-reduced-motion: reduce)');
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

  it('deletes an attribute only after clicking the delete button twice', async () => {
    storage.set('bilibili-live-gift-panel-v1', JSON.stringify(state('88888888', 1)));
    const confirmMock = vi.fn(() => true);
    vi.stubGlobal('confirm', confirmMock);
    const root = new TestElement('div');
    mountConfig(root as unknown as HTMLElement);

    const card = root.querySelector('.attribute-card')!;
    const deleteAttribute = findByText(card, '删除')!;
    deleteAttribute.onclick?.();
    expect(deleteAttribute.textContent).toBe('确定');
    expect(loadState().attributes).toHaveLength(1);

    deleteAttribute.onclick?.();

    await vi.waitFor(() => expect(loadState().attributes).toHaveLength(0));
    expect(confirmMock).not.toHaveBeenCalled();
  });

  it('does not render OBS appearance as a global setting', () => {
    const root = new TestElement('div');
    mountConfig(root as unknown as HTMLElement);

    const opacity = root.querySelectorAll('input')
      .find((input) => input.dataset.fieldLabel === '面板透明度（%）');
    expect(opacity).toBeUndefined();
    expect(findByText(root, 'OBS 面板外观')).toBeUndefined();
  });

  it('persists field-specific appearance on an attribute OBS panel', async () => {
    storage.set('bilibili-live-gift-panel-v1', JSON.stringify(state('88888888', 1)));
    const root = new TestElement('div');
    mountConfig(root as unknown as HTMLElement);
    findByText(root, '编辑')?.onclick?.();

    const fontSize = root.querySelectorAll('input')
      .find((input) => input.dataset.fieldLabel === '字体大小（px）') as TestElement & { oninput?: () => void };
    const accent = root.querySelectorAll('input')
      .find((input) => input.dataset.fieldLabel === '强调色') as TestElement & { oninput?: () => void };
    const opacity = root.querySelectorAll('input')
      .find((input) => input.dataset.fieldLabel === '面板透明度（%）') as TestElement & { oninput?: () => void };
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
    fontSize.oninput?.();
    accent.value = '#123456';
    accent.oninput?.();
    opacity.value = '72';
    opacity.oninput?.();
    alignmentOptions[2].onclick?.();
    connectionSwitch.checked = true;
    connectionSwitch.onchange?.();
    findByText(root, '保存修改')?.onclick?.();

    await vi.waitFor(() => {
      expect(loadState().attributes[0].display?.appearance).toEqual(expect.objectContaining({
        fontSize: 64,
        accentColor: '#123456',
        align: 'right',
        panelOpacity: 72,
        showConnection: true,
      }));
    });
  });
});

describe('OBS combination scene configuration', () => {
  it('creates a two-attribute scene and exposes its dedicated OBS link', async () => {
    const configured = defaultState();
    configured.settings.showTutorial = false;
    configured.attributes = [
      { name: '生命值', value: 100, unit: 'none', format: 'number', decimals: 0, suffix: '' },
      { name: '能量', value: 50, unit: 'none', format: 'number', decimals: 0, suffix: '' },
    ];
    await saveState(configured);
    const root = new TestElement('div');
    mountConfig(root as unknown as HTMLElement);

    findByText(root, '+ 新建组合面板')?.onclick?.();
    expect(root.querySelector('.display-scene-dialog')).not.toBeNull();
    expect(root.querySelectorAll('.display-scene-attribute-option').filter((item) => item.className.includes('is-selected'))).toHaveLength(2);
    findByText(root, '创建组合面板')?.onclick?.();

    await vi.waitFor(() => {
      expect(loadState().displayScenes).toHaveLength(1);
      expect(root.querySelector('.display-scene-url')).not.toBeNull();
    });
    expect(loadState().displayScenes[0]).toEqual(expect.objectContaining({
      name: '组合面板 1', attributeNames: ['生命值', '能量'], layout: 'grid',
      appearance: expect.objectContaining({ themeId: 'glass', fontSize: 48, panelOpacity: 55 }),
    }));
    const url = root.querySelector('.display-scene-url') as TestElement;
    expect(url.value).toContain('?mode=display&scene=scene-');
    const card = root.querySelector('.display-scene-card');
    expect(card?.className.split(' ')).toContain('hover-detail-card');
    expect(card?.querySelector('.display-scene-card-cover')).not.toBeNull();
    expect(card?.querySelector('.hover-detail-panel')).not.toBeNull();
  });

  it('deletes a combination scene and clears its activity link in the same save', async () => {
    const configured = defaultState();
    configured.settings.showTutorial = false;
    configured.attributes = [
      { name: '积分', value: 0, unit: 'none', format: 'number', decimals: 0, suffix: '' },
      { name: '能量', value: 0, unit: 'none', format: 'number', decimals: 0, suffix: '' },
    ];
    configured.displayScenes = [{
      id: 'scene-score', name: '积分面板', attributeNames: ['积分', '能量'], layout: 'stack', themeId: 'glass',
    }];
    configured.activities = [{
      id: 'activity-score', name: '积分活动', attributeNames: ['积分'], sceneId: 'scene-score',
      status: 'not_started', resultMode: 'highest', gateRules: false, initialValues: { 积分: 0 }, milestones: [],
    }];
    await saveState(configured);
    const confirmMock = vi.fn(() => true);
    vi.stubGlobal('confirm', confirmMock);
    const root = new TestElement('div');
    mountConfig(root as unknown as HTMLElement);

    const card = root.querySelector('.display-scene-card')!;
    const deleteScene = findByText(card, '删除')!;
    deleteScene.onclick?.();
    expect(deleteScene.textContent).toBe('确定');
    expect(loadState().displayScenes).toHaveLength(1);
    deleteScene.onclick?.();

    await vi.waitFor(() => expect(loadState().displayScenes).toHaveLength(0));
    expect(loadState().activities[0].sceneId).toBeUndefined();
    expect(confirmMock).not.toHaveBeenCalled();
  });
});

describe('activity session configuration', () => {
  it('creates a gated activity from existing attributes', async () => {
    const configured = defaultState();
    configured.settings.showTutorial = false;
    configured.attributes = [
      { name: '红队', value: 0, unit: 'none', format: 'number', decimals: 0, suffix: '' },
      { name: '蓝队', value: 0, unit: 'none', format: 'number', decimals: 0, suffix: '' },
    ];
    await saveState(configured);
    const root = new TestElement('div');
    mountConfig(root as unknown as HTMLElement);

    findByText(root, '+ 新建活动')?.onclick?.();
    expect(root.querySelector('.activity-editor-dialog')).not.toBeNull();
    expect(root.querySelectorAll('.activity-attribute-option').filter((item) => item.className.includes('is-selected'))).toHaveLength(2);
    findByText(root, '创建活动')?.onclick?.();

    await vi.waitFor(() => expect(loadState().activities).toHaveLength(1));
    expect(loadState().activities[0]).toEqual(expect.objectContaining({
      name: '活动 1', attributeNames: ['红队', '蓝队'], status: 'not_started', resultMode: 'highest', gateRules: true,
      initialValues: { 红队: 0, 蓝队: 0 }, milestones: [],
    }));
    await vi.waitFor(() => expect(root.querySelector('.activity-card')).not.toBeNull());
    const card = root.querySelector('.activity-card');
    expect(card?.className.split(' ')).toContain('hover-detail-card');
    expect(card?.querySelector('.activity-card-cover')).not.toBeNull();
    expect(card?.querySelector('.hover-detail-panel')).not.toBeNull();
  });

  it('keeps the delete action available after an activity is settled', async () => {
    const configured = defaultState();
    configured.settings.showTutorial = false;
    configured.attributes = [
      { name: '红队', value: 12, unit: 'none', format: 'number', decimals: 0, suffix: '票' },
    ];
    configured.activities = [{
      id: 'activity-settled', name: '已结束活动', attributeNames: ['红队'], status: 'settled',
      resultMode: 'highest', gateRules: true, initialValues: { 红队: 0 }, milestones: [],
      result: { winnerAttributeName: '红队', values: { 红队: 12 } },
    }];
    await saveState(configured);
    const root = new TestElement('div');
    root.dataset.expandedActivityId = 'activity-settled';
    root.dataset.expandedActivitySide = 'above';
    mountConfig(root as unknown as HTMLElement);

    await vi.waitFor(() => expect(root.querySelector('.activity-card')).not.toBeNull());
    const card = root.querySelector('.activity-card')!;
    expect(findByText(card, '重新准备')).toBeDefined();
    expect(findByText(card, '删除')).toBeDefined();
    expect(card.className.split(' ')).toContain('is-detail-persisted');
    expect(card.className.split(' ')).toContain('is-detail-restoring');
    expect(card.className.split(' ')).toContain('is-detail-above');
    (card as TestElement & { onpointerleave?: () => void }).onpointerleave?.();
    expect(card.className.split(' ')).not.toContain('is-detail-persisted');
    expect(root.dataset.expandedActivityId).toBeUndefined();
    expect(root.dataset.expandedActivitySide).toBeUndefined();
  });

  it('deletes an activity only after the configuration save succeeds', async () => {
    const configured = defaultState();
    configured.settings.showTutorial = false;
    configured.attributes = [{ name: '积分', value: 0, unit: 'none', format: 'number', decimals: 0, suffix: '' }];
    configured.activities = [{
      id: 'activity-score', name: '积分活动', attributeNames: ['积分'], status: 'not_started',
      resultMode: 'highest', gateRules: false, initialValues: { 积分: 0 }, milestones: [],
    }];
    await saveState(configured);
    const confirmMock = vi.fn(() => true);
    vi.stubGlobal('confirm', confirmMock);
    const root = new TestElement('div');
    mountConfig(root as unknown as HTMLElement);

    const card = root.querySelector('.activity-card')!;
    const deleteActivity = findByText(card, '删除')!;
    deleteActivity.onclick?.();
    expect(deleteActivity.textContent).toBe('确定');
    expect(loadState().activities).toHaveLength(1);
    deleteActivity.onclick?.();

    await vi.waitFor(() => expect(loadState().activities).toHaveLength(0));
    expect(confirmMock).not.toHaveBeenCalled();
  });

  it('rerenders only the activity section and restores its open detail without replaying transitions', async () => {
    const configured = defaultState();
    configured.settings.showTutorial = false;
    configured.attributes = [
      { name: '红队', value: 0, unit: 'none', format: 'suffix', decimals: 0, suffix: '分' },
      { name: '蓝队', value: 0, unit: 'none', format: 'suffix', decimals: 0, suffix: '分' },
    ];
    configured.displayScenes = [{
      id: 'scene-versus', name: '红蓝面板', attributeNames: ['红队', '蓝队'], layout: 'grid', themeId: 'neon',
    }];
    configured.activities = [{
      id: 'activity-versus', name: '红蓝对战', attributeNames: ['红队', '蓝队'], sceneId: 'scene-versus',
      status: 'not_started', resultMode: 'highest', gateRules: true, initialValues: { 红队: 0, 蓝队: 0 }, milestones: [],
    }];
    await saveState(configured);
    vi.stubGlobal('fetch', vi.fn(async (input: string | URL | Request) => {
      const url = String(input);
      if (url.endsWith('/api/activities/transition')) {
        return Response.json({
          code: 0,
          activity: { ...configured.activities[0], status: 'active', startedAt: 1 },
          attributeValues: { 红队: 0, 蓝队: 0 },
        });
      }
      if (url.includes('/api/runtime')) {
        return Response.json({ code: 0, runtime: { state: 'idle', roomId: '' } });
      }
      if (url.includes('/api/auth/status')) {
        return Response.json({ code: 0, auth: { state: 'anonymous' } });
      }
      return new Response(null, { status: 204 });
    }));

    const root = new TestElement('div');
    mountConfig(root as unknown as HTMLElement);
    const attributeCard = root.querySelector('.attribute-card');
    const sceneCard = root.querySelector('.display-scene-card');
    const activityCard = root.querySelector('.activity-card') as TestElement & {
      onpointerdown?: (event: { pointerType: string }) => void;
    };
    activityCard.onpointerdown?.({ pointerType: 'mouse' });
    findByText(activityCard, '开始活动')?.onclick?.();
    expect(activityCard.className.split(' ')).toContain('is-detail-restoring');

    await vi.waitFor(() => expect(findByText(root, '锁定结果')).toBeDefined());
    expect(root.querySelector('.attribute-card')).toBe(attributeCard);
    expect(root.querySelector('.display-scene-card')).toBe(sceneCard);
    const nextActivityCard = root.querySelector('.activity-card');
    expect(nextActivityCard).not.toBe(activityCard);
    expect(nextActivityCard?.className.split(' ')).toContain('is-detail-persisted');
  });
});

describe('OBS attribute display', () => {
  it('formats positive, negative, and zero deltas with the correct sign', () => {
    const attr = { name: '加班时间', value: 0, unit: 'seconds', format: 'hhmmss', decimals: 0, suffix: '' } as const;
    expect(formatDelta(60, attr)).toBe('+00:01:00');
    expect(formatDelta(-40, attr)).toBe('-00:00:40');
    expect(formatDelta(0, attr)).toBe('00:00:00');
  });

  it('maps an enum value to OBS text, color, and image while retaining numeric state', () => {
    const attribute: Attribute = {
      name: '比赛结果', value: 1, unit: 'none', format: 'number', decimals: 0, suffix: '',
      display: {
        variant: 'enum', themeId: 'neon', valueMappings: [
          { value: 1, label: '红队胜', color: '#ff3366', imageUrl: 'https://example.com/red.png' },
        ],
      },
    };
    expect(resolveAttributeValuePresentation(attribute)).toEqual({
      text: '红队胜', color: '#ff3366', imageUrl: 'https://example.com/red.png',
    });
    expect(attribute.value).toBe(1);
  });

  it('renders enum text and image in the OBS value block', () => {
    storage.set('bilibili-live-gift-panel-v1', JSON.stringify({
      ...state(),
      attributes: [{
        name: '比赛结果', value: 1, unit: 'none', format: 'number', decimals: 0, suffix: '',
        display: {
          variant: 'enum', themeId: 'neon', valueMappings: [
            { value: 1, label: '红队胜', color: '#ff3366', imageUrl: 'https://example.com/red.png' },
          ],
        },
      }],
      rules: [],
    }));
    vi.useFakeTimers();
    const root = new TestElement('div');
    mountDisplay(root as unknown as HTMLElement, { attributeName: '比赛结果' });

    expect(textOf(root)).toContain('红队胜');
    expect(root.querySelector('.attr-value')?.className).toContain('is-enum-mapped');
    expect((root.querySelector('.attr-enum-image') as any)?.src).toBe('https://example.com/red.png');
    vi.useRealTimers();
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
    mountDisplay(root as unknown as HTMLElement, { attributeName: '积分' });

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

  it('renders a saved combination scene in its selected order and grid layout', () => {
    storage.set('bilibili-live-gift-panel-v1', JSON.stringify({
      ...state(),
      settings: { ...defaultState().settings, defaultDisplayThemeId: 'glass' },
      attributes: [
        { name: '生命值', value: 80, unit: 'none', format: 'number', decimals: 0, suffix: '' },
        { name: '能量', value: 45, unit: 'none', format: 'number', decimals: 0, suffix: '' },
        { name: '隐藏属性', value: 9, unit: 'none', format: 'number', decimals: 0, suffix: '' },
      ],
      displayScenes: [{
        id: 'scene-status', name: '战斗状态', attributeNames: ['能量', '生命值'], layout: 'grid', themeId: 'neon',
      }],
      rules: [],
    }));
    vi.useFakeTimers();
    const root = new TestElement('div');
    mountDisplay(root as unknown as HTMLElement, { sceneId: 'scene-status' });

    expect(root.querySelector('.display-stack')?.className).toContain('is-scene-wide');
    expect(root.querySelector('.panel')?.className).toContain('scene-layout-grid');
    expect(root.querySelector('.panel')?.dataset.theme).toBe('neon');
    expect(root.querySelectorAll('.attr')).toHaveLength(2);
    expect(root.querySelectorAll('.broadcast-ticker')).toHaveLength(1);
    expect(root.querySelector('.panel')?.querySelector('.broadcast-ticker')).not.toBeNull();
    expect(root.querySelector('.attr')?.querySelector('.broadcast-ticker')).toBeNull();
    expect(textOf(root)).toContain('战斗状态');
    expect(textOf(root)).toContain('能量');
    expect(textOf(root)).toContain('生命值');
    expect(textOf(root)).not.toContain('隐藏属性');
    vi.useRealTimers();
  });

  it('shows the linked activity state and settlement result in a combination scene', () => {
    storage.set('bilibili-live-gift-panel-v1', JSON.stringify({
      ...state(),
      settings: { ...defaultState().settings, defaultDisplayThemeId: 'glass' },
      attributes: [
        { name: '红队', value: 12, unit: 'none', format: 'number', decimals: 0, suffix: '票' },
        { name: '蓝队', value: 18, unit: 'none', format: 'number', decimals: 0, suffix: '票' },
      ],
      displayScenes: [{ id: 'scene-match', name: '阵营对抗', attributeNames: ['红队', '蓝队'], layout: 'grid', themeId: 'neon' }],
      activities: [{
        id: 'activity-match', name: '阵营对抗', attributeNames: ['红队', '蓝队'], sceneId: 'scene-match',
        status: 'settled', resultMode: 'highest', gateRules: true, initialValues: { 红队: 0, 蓝队: 0 },
        milestones: [{
          id: 'milestone-win', name: '目标达成', attributeName: '蓝队', comparison: 'gte', threshold: 18,
          action: 'settle', message: '蓝队率先达标！', triggeredAt: 100, triggerValue: 18,
        }],
        giftTimeout: { seconds: 30, action: 'settle', lastGiftAt: Date.now() - 1_000, deadlineAt: Date.now() + 29_000 },
        result: { winnerAttributeName: '蓝队', values: { 红队: 12, 蓝队: 18 } },
      }],
      rules: [],
    }));
    vi.useFakeTimers();
    const root = new TestElement('div');
    mountDisplay(root as unknown as HTMLElement, { sceneId: 'scene-match' });

    expect(textOf(root)).toContain('已结算');
    expect(textOf(root)).toContain('本局胜出');
    expect(textOf(root)).toContain('蓝队');
    expect(textOf(root)).toContain('蓝队率先达标！');
    expect(textOf(root)).toContain('送礼后倒计时');
    vi.useRealTimers();
  });

  it('renders the selected theme and gameplay meter for a progress attribute', () => {
    storage.set('bilibili-live-gift-panel-v1', JSON.stringify({
      ...state(),
      settings: { ...defaultState().settings, defaultDisplayThemeId: 'glass' },
      attributes: [{
        name: '应援目标', value: 42, unit: 'none', format: 'suffix', decimals: 0, suffix: '%',
        display: { variant: 'progress', themeId: 'neon', title: '本场应援', min: 0, max: 100 },
      }],
      rules: [],
    }));
    vi.useFakeTimers();
    const root = new TestElement('div');
    mountDisplay(root as unknown as HTMLElement, { attributeName: '应援目标' });

    expect(root.querySelector('.panel')?.dataset.theme).toBe('neon');
    expect(root.querySelector('.attr')?.dataset.variant).toBe('progress');
    expect(root.querySelector('.attr-meter-progress')).not.toBeNull();
    expect(textOf(root)).toContain('本场应援');
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
    mountDisplay(root as unknown as HTMLElement, { attributeName: '早播' });

    const formulaName = root.querySelector('.display-formula-name');
    expect(formulaName?.className).toContain('is-long');
    expect(formulaName?.textContent).toBe('小于10场+1，大于10场x2');
    const displayCss = readFileSync(new URL('../src/ui/display/display.css', import.meta.url), 'utf8');
    expect(displayCss).toMatch(/\.display-formula-name\s*\{[^}]*-webkit-line-clamp:\s*2;/);
    expect(displayCss).toMatch(/\.display-formula-name\.is-long\s*\{[^}]*font-size:\s*20px;/);
    vi.useRealTimers();
  });

  it('keeps KPI gift choices at their content height inside the scrolling editor', () => {
    const configCss = readFileSync(new URL('../src/ui/config/config.css', import.meta.url), 'utf8');
    const configSource = readFileSync(new URL('../src/ui/config/config.ts', import.meta.url), 'utf8');
    const activitySource = readFileSync(new URL('../src/ui/config/activity-workspace.ts', import.meta.url), 'utf8');
    expect(configCss).toMatch(/\.gift-kpi-editor-body\s*\{[^}]*grid-auto-rows:\s*max-content;[^}]*align-content:\s*start;/);
    expect(configCss).toMatch(/\.gift-kpi-editor-items\s*\{[^}]*grid-template-columns:\s*repeat\(2,/);
    expect(configSource.match(/createGiftPickerChoice\(gift,/g)).toHaveLength(2);
    expect(configSource).toContain("class: 'gift-picker-grid gift-kpi-picker-grid'");
    expect(configSource).toContain("class: 'gift-kpi-card-cover summary-card-cover hover-detail-cover'");
    expect(configSource).toContain("gift-kpi-card-visual summary-card-visual");
    expect(configSource).toContain("gift-kpi-card-preview-image summary-card-cover-image");
    expect(configSource).toContain("gift-kpi-card-cover-copy summary-card-cover-copy");
    expect(configSource).toContain("attribute-card-title summary-card-cover hover-detail-cover");
    expect(configSource).toContain("attribute-card-visual summary-card-visual");
    expect(configSource).toContain("attribute-cover-image summary-card-cover-image");
    expect(configSource).toContain("display-scene-card-cover-copy summary-card-cover-copy");
    expect(activitySource).toContain("activity-card-cover summary-card-cover hover-detail-cover");
    expect(activitySource).toContain("activity-card-cover-copy summary-card-cover-copy");
    expect(configSource).toContain('const previewItems = panel.items.slice(0, 3);');
    expect(configSource).toContain('panelWidth: 480, estimatedPanelHeight: 430');
    expect(configCss).not.toMatch(/\.gift-kpi-config-card\s*\{[^}]*--kpi-card-visual-depth:/);
    expect(configCss).not.toMatch(/\.gift-kpi-card-visual\s*\{/);
    expect(configCss).toMatch(/\.summary-card-visual::before\s*\{[^}]*translateZ\(var\(--card-visual-surface-depth\)\)/);
    expect(configCss).toMatch(/\.summary-card-cover-image\s*\{[^}]*translate\(-50%, -50%\) translateZ\(var\(--card-visual-depth\)\)/);
    expect(configCss).toMatch(/\.gift-kpi-card-detail-content\s*\{[^}]*gap:\s*8px;[^}]*padding:\s*12px 14px;/);
    expect(configCss).toMatch(/\.gift-kpi-config-items\s*\{[^}]*margin-top:\s*0;/);
    expect(configCss).toMatch(/\.gift-kpi-config-card\s*\{[^}]*--hover-detail-cover-open-padding-inline:\s*clamp\(14px, 8%, 40px\);/);
    expect(configCss).toMatch(/\.hover-detail-card\.is-detail-persisted\s*>\s*\.hover-detail-cover[\s\S]*?padding-right:\s*var\(--hover-detail-cover-open-padding-inline, 0px\);[\s\S]*?padding-left:\s*var\(--hover-detail-cover-open-padding-inline,/);
  });

  it('fills attribute detail cards symmetrically with gift rules', () => {
    const configCss = readFileSync(new URL('../src/ui/config/config.css', import.meta.url), 'utf8');
    expect(configCss).toMatch(/\.attribute-formulas\s*\{[^}]*grid-template-columns:\s*repeat\(2, minmax\(0, 1fr\)\);/);
    expect(configCss).toMatch(/\.hover-detail-card\s*\{[^}]*--card-visual-surface-depth:\s*0px;[^}]*--card-detail-icon-depth:\s*0px;/);
    expect(configCss).toMatch(/\.hover-detail-card\.is-detail-persisted\s*\{[^}]*--card-visual-surface-depth:\s*8px;[^}]*--card-detail-icon-depth:\s*10px;/);
    expect(configCss).toMatch(/\.summary-card-visual::before\s*\{[^}]*translateZ\(var\(--card-visual-surface-depth\)\)/);
    expect(configCss).toMatch(/\.summary-card-cover-image\s*\{[^}]*translate\(-50%, -50%\) translateZ\(var\(--card-visual-depth\)\)/);
    expect(configCss).toMatch(/\.summary-card-cover-image\s*\{[^}]*drop-shadow\(0 3px 4px/);
    expect(configCss).toMatch(/\.summary-card-visual\.has-multiple \.summary-card-cover-image:nth-child\(2\)\s*\{[^}]*translateZ\(calc\(var\(--card-visual-depth\) \+ 6px\)\)/);
    expect(configCss).toMatch(/\.summary-card-visual\.has-multiple \.summary-card-cover-image:nth-child\(3\)\s*\{[^}]*translateZ\(calc\(var\(--card-visual-depth\) \+ 12px\)\)/);
    expect(configCss).toMatch(/\.attribute-gift-image\s*\{[^}]*translateZ\(var\(--card-detail-icon-depth\)\)/);
    expect(configCss).toMatch(/\.attribute-gift-image\s*\{[^}]*drop-shadow\(0 3px 4px/);
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
