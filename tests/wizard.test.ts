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
