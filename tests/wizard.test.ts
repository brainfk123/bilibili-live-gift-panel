import { beforeEach, describe, expect, it, vi } from 'vitest';
import { mountConfig } from '../src/ui/config/config';
import { getNextWizardStep, getRoomNumberHint, getWizardChecklist, getWizardProgress } from '../src/ui/config/wizard';

vi.mock('../src/ui/brand', () => ({
  createBrandIcon: () => document.createElement('svg'),
}));

class TestElement {
  className = '';
  dataset: Record<string, string> = {};
  children: TestElement[] = [];
  parent: TestElement | null = null;
  style: Record<string, string> = {};
  textContent = '';
  value = '';
  innerHTML = '';
  placeholder = '';
  type = '';
  readOnly = false;
  onclick: (() => void) | null = null;
  classList = {
    add: (...names: string[]) => {
      const classes = new Set(this.className.split(' ').filter(Boolean));
      names.forEach((name) => classes.add(name));
      this.className = [...classes].join(' ');
    },
  };

  select(): void {}

  constructor(readonly tagName: string) {}

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
  vi.stubGlobal('document', {
    createElement: (tag: string) => new TestElement(tag),
  } as unknown as Document);
  vi.stubGlobal('localStorage', {
    getItem: (key: string) => storage.get(key) ?? null,
    setItem: (key: string, value: string) => void storage.set(key, value),
    removeItem: (key: string) => void storage.delete(key),
  });
  vi.stubGlobal('fetch', () => new Promise(() => {}));
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
    expect(root.querySelectorAll('.empty').map((empty) => empty.textContent)).toEqual(['先搜索一个观众会送的礼物。']);
    expect(root.querySelector('.manual-add-card')).toBeNull();

    const gift = root.querySelector('.list-item');
    (gift?.onclick as (() => void) | null)?.();

    const formulaTutorial = root.querySelector('.tutorial');
    expect(formulaTutorial?.querySelector('summary')?.textContent).toBe('不会写公式？看示例');
    expect((formulaTutorial as TestElement & { open?: boolean } | null)?.open).not.toBe(true);
    const limits = root.querySelectorAll('.details-card').find((details) => details.querySelector('summary')?.textContent === '可选限制');
    expect(limits).toBeDefined();
    expect((limits as TestElement & { open?: boolean } | undefined)?.open).not.toBe(true);
  });

  it('shows a compact OBS completion card with a copyable display URL', () => {
    storage.set('bilibili-live-gift-panel-v1', JSON.stringify(state('88888888', 1)));
    vi.stubGlobal('location', { origin: 'http://localhost:12450' });
    const root = new TestElement('div');
    mountConfig(root as unknown as HTMLElement);

    const card = root.querySelector('.completion-card');
    const url = card?.querySelector('input') as TestElement & { value: string; readOnly: boolean } | null;
    expect(card).not.toBeNull();
    expect(url?.value).toBe('http://localhost:12450/?mode=display');
    expect(url?.readOnly).toBe(true);
    expect(findByText(root, '复制地址')).toBeDefined();
    expect(card?.querySelectorAll('.obs-step')).toHaveLength(3);
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

    const roomInput = root.querySelector('input') as HTMLInputElement;
    roomInput.value = '2145';
    const connectButton = Array.from(root.querySelectorAll('button')).find((button) => button.textContent === '测试连接');
    expect(connectButton).toBeDefined();
    (connectButton?.onclick as (() => void) | null)?.();

    const roomStep = root.querySelector('[data-step="room"]');
    expect(roomStep?.className).toContain('is-done');
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
