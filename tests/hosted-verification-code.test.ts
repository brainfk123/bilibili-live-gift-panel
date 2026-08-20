import { readFileSync } from 'node:fs';
import { resolve } from 'node:path';
import { describe, expect, it, vi } from 'vitest';

import { mountVerificationCode } from '../src/hosted/verification-code';

class Element {
  children: Element[] = [];
  textContent = '';
  className = '';
  type = '';
  value = '';
  disabled = false;
  autocomplete = '';
  inputMode = '';
  attributes = new Map<string, string>();
  listeners = new Map<string, (event?: unknown) => void>();
  constructor(readonly tagName: string, readonly ownerDocument: DocumentLike) {}
  append(...nodes: Element[]) { this.children.push(...nodes); }
  replaceChildren(...nodes: Element[]) { this.children = nodes; }
  setAttribute(name: string, value: string) { this.attributes.set(name, value); }
  addEventListener(name: string, listener: (event?: unknown) => void) { this.listeners.set(name, listener); }
  removeEventListener(name: string) { this.listeners.delete(name); }
  focus() { this.ownerDocument.activeElement = this; }
}

interface DocumentLike {
  activeElement?: Element;
  createElement(tag: string): Element;
}

function fixture() {
  const document: DocumentLike = { createElement: (tag) => new Element(tag, document) };
  return { document, root: new Element('div', document) };
}

describe('hosted verification code control', () => {
  it('uses one semantic numeric input to drive six visual cells', () => {
    const { root } = fixture();
    const control = mountVerificationCode(root as unknown as HTMLElement, { label: '六位动态验证码', onComplete: vi.fn() });
    const input = root.children[0];
    expect(root.children).toHaveLength(7);
    expect(input.tagName).toBe('input');
    expect(input.inputMode).toBe('numeric');
    expect(input.autocomplete).toBe('one-time-code');
    expect(input.attributes.get('pattern')).toBe('[0-9]*');
    expect(input.attributes.get('aria-label')).toBe('六位动态验证码');
    expect(root.children.slice(1).every((cell) => cell.attributes.get('aria-hidden') === 'true')).toBe(true);
    control.dispose();
  });

  it('keeps the native input over all six cells so iOS Safari taps open the keyboard', () => {
    const css = readFileSync(resolve(import.meta.dirname, '../src/hosted/shell.css'), 'utf8');
    const inputRule = css.match(/\.hosted-code-input\s*\{([^}]*)\}/)?.[1] ?? '';
    const cellRule = css.match(/\.hosted-code-cell\s*\{([^}]*)\}/)?.[1] ?? '';
    expect(inputRule).toMatch(/inset:\s*0/);
    expect(inputRule).toMatch(/width:\s*100%/);
    expect(inputRule).toMatch(/height:\s*100%/);
    expect(inputRule).not.toMatch(/clip:/);
    expect(cellRule).toMatch(/pointer-events:\s*none/);
  });

  it('normalizes paste, completes once, respects busy, and clears securely', () => {
    const { document, root } = fixture();
    const completed = vi.fn();
    const control = mountVerificationCode(root as unknown as HTMLElement, { label: '验证码', onComplete: completed });
    const input = root.children[0];
    input.value = '1a23 4567'; input.listeners.get('input')?.();
    expect(input.value).toBe('123456');
    expect(completed).toHaveBeenCalledTimes(1);
    expect(completed).toHaveBeenCalledWith('123456');
    input.listeners.get('input')?.(); expect(completed).toHaveBeenCalledTimes(1);
    control.setBusy(true); input.value = '654321'; input.listeners.get('input')?.(); expect(completed).toHaveBeenCalledTimes(1);
    control.setBusy(false); control.clear();
    expect(input.value).toBe(''); expect(document.activeElement).toBe(input);
    expect(root.children.slice(1).every((cell) => cell.textContent === '')).toBe(true);
    input.value = '654321'; input.listeners.get('input')?.(); expect(completed).toHaveBeenCalledWith('654321');
    control.dispose();
    expect(input.value).toBe(''); expect(root.children).toHaveLength(0);
    expect(JSON.stringify(control)).not.toContain('654321');
  });

  it('shows a caret in the next empty cell while the native input is focused', () => {
    const { root } = fixture();
    const control = mountVerificationCode(root as unknown as HTMLElement, { label: '验证码', onComplete: vi.fn() });
    const input = root.children[0];
    input.listeners.get('focus')?.();
    expect(root.children.slice(1).map((cell) => cell.className)).toEqual([
      'hosted-code-cell is-active', 'hosted-code-cell', 'hosted-code-cell', 'hosted-code-cell', 'hosted-code-cell', 'hosted-code-cell',
    ]);
    input.value = '12'; input.listeners.get('input')?.();
    expect(root.children[3]?.className).toBe('hosted-code-cell is-active');
    expect(root.children[1]?.className).toBe('hosted-code-cell');
    input.listeners.get('blur')?.();
    expect(root.children.slice(1).every((cell) => cell.className === 'hosted-code-cell')).toBe(true);
    control.dispose();
  });
});
