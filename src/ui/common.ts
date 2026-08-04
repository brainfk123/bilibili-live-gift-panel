export function el<K extends keyof HTMLElementTagNameMap, C extends (HTMLElement | string)[] = (HTMLElement | string)[]>(
  tag: K,
  props: Omit<Partial<HTMLElementTagNameMap[K]>, 'children' | 'style'> & { class?: string; text?: string; style?: string; children?: C } = {} as any,
  children?: C,
): HTMLElementTagNameMap[K] {
  const node = document.createElement(tag);
  if (props.class) node.className = props.class;
  if (props.text != null) node.textContent = props.text;
  for (const key of Object.keys(props)) {
    if (key === 'class' || key === 'text' || key === 'children') continue;
    if (key === 'style') {
      node.setAttribute('style', (props as any).style);
      continue;
    }
    (node as any)[key] = (props as any)[key];
  }
  const kids = props.children ?? children ?? [];
  for (const child of kids) {
    node.append(child as any);
  }
  return node;
}

export function inputField(label: string, value: string): HTMLInputElement {
  const input = el('input', { class: 'field-input', value }) as HTMLInputElement;
  input.dataset.fieldLabel = label;
  return input;
}

export function fieldControl(input: HTMLInputElement | HTMLSelectElement): HTMLLabelElement {
  const wrap = el('label', { class: 'field' });
  wrap.append(
    el('span', { class: 'field-label', text: input.dataset.fieldLabel ?? '' }),
    input,
  );
  return wrap;
}

export function toast(message: string, root: HTMLElement): void {
  const t = el('div', { class: 'toast', text: message });
  root.append(t);
  setTimeout(() => t.classList.add('show'), 10);
  setTimeout(() => t.remove(), 2500);
}
