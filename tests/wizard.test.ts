import { beforeEach, describe, expect, it, vi } from 'vitest';
import { mountConfig } from '../src/ui/config/config';
import { formatDelta, mountDisplay } from '../src/ui/display/display';
import { getNextWizardStep, getRoomNumberHint, getWizardChecklist, getWizardProgress } from '../src/ui/config/wizard';
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

const storage = new Map<string, string>();

beforeEach(() => {
  storage.clear();
  mockedClients.length = 0;
  vi.stubGlobal('document', {
    createElement: (tag: string) => new TestElement(tag),
  } as unknown as Document);
  vi.stubGlobal('localStorage', {
    getItem: (key: string) => storage.get(key) ?? null,
    setItem: (key: string, value: string) => void storage.set(key, value),
    removeItem: (key: string) => void storage.delete(key),
  });
  vi.stubGlobal('fetch', () => new Promise(() => {}));
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

describe('configuration wizard rendering', () => {
  it('uses dark theme by default and can persist light theme', () => {
    const root = new TestElement('div');
    mountConfig(root as unknown as HTMLElement);
    expect(root.dataset.theme).toBe('dark');

    const toggle = findByText(root, '亮色主题');
    (toggle?.onclick as (() => void) | null)?.();
    expect(root.dataset.theme).toBe('light');
    expect(JSON.parse(storage.get('bilibili-live-gift-panel-v1')!).settings.theme).toBe('light');
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

  it('shows compact top navigation after setup is complete', () => {
    storage.set('bilibili-live-gift-panel-v1', JSON.stringify(state('88888888', 1)));
    const root = new TestElement('div');
    mountConfig(root as unknown as HTMLElement);
    expect(root.querySelector('.wizard-progress')).toBeNull();
    expect(root.querySelector('.normal-nav')).not.toBeNull();
    expect(root.querySelector('.completion-home')).not.toBeNull();
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
});

describe('room number hint', () => {
  it('extracts the path segment before query parameters', () => {
    expect(getRoomNumberHint('https://live.bilibili.com/88888888?live_from=1111&visit_id=x')).toEqual({
      path: '88888888',
      query: '?live_from=1111&visit_id=x',
    });
  });
});
