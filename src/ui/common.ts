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

interface FloatingDetailController {
  lockDirection: () => void;
  releaseDirection: () => void;
}

const floatingDetailControllers = new WeakMap<HTMLElement, FloatingDetailController>();

export function setFloatingDetailGuideExpanded(card: HTMLElement, expanded: boolean): void {
  card.classList.toggle('is-guide-expanded', expanded);
  const controller = floatingDetailControllers.get(card);
  if (expanded) controller?.lockDirection();
  else controller?.releaseDirection();
}

export function bindFloatingDetailCard(
  card: HTMLElement,
  cover: HTMLElement,
  options: { panelWidth?: number; estimatedPanelHeight?: number; onPointerLeave?: () => void } = {},
): void {
  const panelWidth = options.panelWidth ?? 560;
  const estimatedPanelHeight = options.estimatedPanelHeight ?? 420;
  card.style.setProperty('--hover-detail-panel-width', `${panelWidth}px`);
  let lockedAbove: boolean | null = card.className.split(/\s+/).includes('is-detail-persisted')
    ? card.className.split(/\s+/).includes('is-detail-above')
    : null;

  const position = (lockDirection = false): void => {
    if (typeof card.getBoundingClientRect !== 'function') return;
    const rect = card.getBoundingClientRect();
    const viewportWidth = globalThis.innerWidth || 1024;
    const viewportHeight = globalThis.innerHeight || 768;
    const viewportPadding = 16;
    const actualPanelWidth = Math.min(panelWidth, Math.max(280, viewportWidth - viewportPadding * 2));
    // Keep the compact card and expanded panel on the same horizontal axis,
    // then clamp only when the centered panel would leave the viewport.
    const preferredLeft = rect.left + rect.width / 2 - actualPanelWidth / 2;
    const absoluteLeft = Math.min(
      Math.max(preferredLeft, viewportPadding),
      Math.max(viewportPadding, viewportWidth - actualPanelWidth - viewportPadding),
    );
    card.style.setProperty('--hover-detail-offset-x', `${absoluteLeft - rect.left}px`);
    const coverRect = cover.getBoundingClientRect();
    if (coverRect.height > 0) card.style.setProperty('--hover-detail-cover-height', `${coverRect.height}px`);
    const verticalThreshold = Math.min(estimatedPanelHeight, viewportHeight * .52);
    const shouldOpenAbove = viewportHeight - rect.bottom < verticalThreshold && rect.top > verticalThreshold;
    if (lockedAbove === null) {
      card.classList.toggle('is-detail-above', shouldOpenAbove);
      if (lockDirection) lockedAbove = shouldOpenAbove;
    } else {
      card.classList.toggle('is-detail-above', lockedAbove);
    }
  };

  const keepsDetailOpen = (): boolean => card.className.split(/\s+/).some((className) => (
    className === 'is-guide-expanded' || className === 'is-detail-persisted'
  ));

  const releaseDirection = (): void => {
    if (!keepsDetailOpen()) lockedAbove = null;
  };

  const resetTilt = (): void => {
    card.style.setProperty('--hover-card-rotate-x', '0deg');
    card.style.setProperty('--hover-card-rotate-y', '0deg');
    card.style.setProperty('--hover-card-glow-x', '50%');
    card.style.setProperty('--hover-card-glow-y', '20%');
  };

  card.onpointerenter = () => position(true);
  card.onpointerdown = (event) => {
    if (event.pointerType === 'mouse') card.classList.add('is-pointer-focus');
  };
  card.onkeydown = () => card.classList.remove('is-pointer-focus');
  card.onfocus = () => position(true);
  (card as any).onfocusin = () => position(true);
  (card as any).onfocusout = (event: FocusEvent) => {
    if (!(event.relatedTarget instanceof Node) || !card.contains(event.relatedTarget)) {
      card.classList.remove('is-pointer-focus');
      releaseDirection();
    }
  };
  card.onpointermove = (event) => {
    if (event.pointerType === 'touch') return;
    if (typeof globalThis.matchMedia === 'function' && globalThis.matchMedia('(prefers-reduced-motion: reduce)').matches) return;
    const expandedSurface = card.querySelector<HTMLElement>('.hover-detail-panel-inner');
    const surface = expandedSurface
      && typeof globalThis.getComputedStyle === 'function'
      && globalThis.getComputedStyle(expandedSurface).visibility === 'visible'
      ? expandedSurface
      : cover;
    const rect = surface.getBoundingClientRect();
    if (rect.width <= 0 || rect.height <= 0) return;
    const horizontal = Math.max(-.5, Math.min(.5, (event.clientX - rect.left) / rect.width - .5));
    const vertical = Math.max(-.5, Math.min(.5, (event.clientY - rect.top) / rect.height - .5));
    card.style.setProperty('--hover-card-rotate-x', `${(-vertical * 8).toFixed(2)}deg`);
    card.style.setProperty('--hover-card-rotate-y', `${(horizontal * 10).toFixed(2)}deg`);
    card.style.setProperty('--hover-card-glow-x', `${((horizontal + .5) * 100).toFixed(1)}%`);
    card.style.setProperty('--hover-card-glow-y', `${((vertical + .5) * 100).toFixed(1)}%`);
  };
  card.onpointerleave = () => {
    resetTilt();
    options.onPointerLeave?.();
    releaseDirection();
  };
  cover.onclick = () => {
    if (typeof globalThis.matchMedia === 'function' && globalThis.matchMedia('(hover: none)').matches) {
      position();
      card.focus();
    }
  };
  floatingDetailControllers.set(card, {
    lockDirection: () => position(true),
    releaseDirection,
  });
  resetTilt();
  if (card.className.split(/\s+/).includes('is-detail-persisted')) {
    card.classList.add('is-detail-restoring');
    const finishRestore = (): void => card.classList.remove('is-detail-restoring');
    globalThis.queueMicrotask(() => {
      position();
      if (typeof globalThis.requestAnimationFrame === 'function') {
        globalThis.requestAnimationFrame(() => globalThis.requestAnimationFrame(finishRestore));
      } else {
        globalThis.setTimeout(finishRestore, 0);
      }
    });
  }
}
