import { getRuntimeStatus, RuntimeConnectionState } from '../../backend';
import { formatValue } from '../../format';
import { findGift } from '../../gifts/catalog';
import { loadState, refreshStateFromServer } from '../../storage';
import { AppState, Attribute, LogEntry } from '../../types';
import { createBrandIcon } from '../brand';
import { el } from '../common';

const DEFAULT_BROADCAST_MESSAGE = '感谢大家的支持，欢迎投喂礼物';
const GIFT_BROADCAST_DURATION = 5500;
const BROADCAST_TRANSITION_DURATION = 450;

export function formatDelta(delta: number, attr?: Attribute): string {
  const sign = delta > 0 ? '+' : delta < 0 ? '-' : '';
  const value = attr ? formatValue(Math.abs(delta), attr) : String(Math.abs(delta));
  return `${sign}${value}`;
}

export function calculateFittedFontSize(
  preferredFontSize: number,
  availableWidth: number,
  contentWidth: number,
  minimumFontSize = 14,
): number {
  if (availableWidth <= 0 || contentWidth <= availableWidth) return preferredFontSize;
  return Math.max(minimumFontSize, Math.floor(preferredFontSize * availableWidth / contentWidth));
}

export function mountDisplay(root: HTMLElement, selectedAttributeName?: string): void {
  let state = loadState();
  let connectionState: RuntimeConnectionState = 'idle';
  let lastLogKey = state.log[0] ? logKey(state.log[0]) : '';
  let stateRefreshActive = false;
  const stack = el('div', { class: 'display-stack' });
  const panel = el('div', { class: 'panel' });
  const attrEls = new Map<string, HTMLElement>();
  const broadcastStages = new Map<string, HTMLElement>();
  const broadcastReturnTimers = new Map<string, ReturnType<typeof globalThis.setTimeout>>();
  stack.append(panel);
  root.replaceChildren(stack);

  const visibleAttributes = (): Attribute[] => selectedAttributeName
    ? state.attributes.filter((attribute) => attribute.name === selectedAttributeName)
    : state.attributes;

  function clearBroadcastReturnTimer(attributeName: string): void {
    const timer = broadcastReturnTimers.get(attributeName);
    if (timer !== undefined) globalThis.clearTimeout(timer);
    broadcastReturnTimers.delete(attributeName);
  }

  function renderAttrs(): void {
    for (const attributeName of broadcastReturnTimers.keys()) clearBroadcastReturnTimer(attributeName);
    panel.replaceChildren();
    attrEls.clear();
    broadcastStages.clear();
    const attributes = visibleAttributes();
    if (attributes.length === 0) {
      const empty = el('div', { class: 'display-empty' });
      empty.append(
        createBrandIcon(64, 'display-empty-brand-icon'),
        el('div', { text: selectedAttributeName ? `找不到属性“${selectedAttributeName}”` : '还没有可显示的属性' }),
      );
      panel.append(empty);
    }
    for (const attr of attributes) {
      const summary = el('div', { class: 'attr-summary' }, [
        el('div', { class: 'attr-name', text: attr.name, title: attr.name }),
        el('div', { class: 'attr-value' }),
      ]);
      const giftRules = el('div', { class: 'display-gift-rules' });
      const rules = state.rules.filter((rule) => rule.attributeName === attr.name && rule.enabled !== false);
      if (rules.length === 0) {
        giftRules.append(el('div', { class: 'display-rule-empty', text: '还没有配置礼物规则' }));
      } else {
        for (const rule of rules) {
          const gift = findGift(state, rule.giftId);
          const formulaName = rule.formulaName?.trim() || '未命名规则';
          const formulaNameClass = Array.from(formulaName).length > 7
            ? 'display-formula-name is-long'
            : 'display-formula-name';
          const image = el('img', { class: 'display-gift-image', alt: gift?.name ?? '' }) as HTMLImageElement;
          image.src = gift?.imgBasic || transparentPixel();
          giftRules.append(el('div', { class: 'display-gift-rule' }, [
            image,
            el('div', { class: 'display-gift-copy' }, [
              el('span', { class: 'display-gift-name', text: gift?.name ?? `礼物 ${rule.giftId}` }),
              el('strong', { class: formulaNameClass, text: formulaName, title: formulaName }),
            ]),
          ]));
        }
      }
      const stage = el('div', { class: 'broadcast-stage' }, [createDefaultBroadcast(attr)]);
      const ticker = el('div', { class: 'broadcast-ticker' }, [stage]);
      const block = el('section', { class: 'attr' }, [summary, giftRules, ticker]);
      broadcastStages.set(attr.name, stage);
      attrEls.set(attr.name, block);
      panel.append(block);
    }
    panel.append(el('div', { class: 'conn' }));
    updateAll();
  }

  function updateAll(): void {
    const opacity = Math.min(100, Math.max(10, Number(state.settings.panelOpacity) || 55)) / 100;
    root.style.setProperty('--accent', state.settings.accentColor);
    root.style.setProperty('--accent2', state.settings.accentColor);
    root.style.setProperty('--panel-opacity', String(opacity));
    root.style.setProperty('--panel-surface-opacity', String(opacity * 0.62));
    stack.classList.toggle('center', state.settings.align === 'center');
    stack.classList.toggle('right', state.settings.align === 'right');
    for (const attr of visibleAttributes()) {
      const block = attrEls.get(attr.name);
      if (!block) continue;
      const valueEl = block.querySelector('.attr-value') as HTMLElement | null;
      if (valueEl) {
        valueEl.style.fontSize = `${state.settings.fontSize}px`;
        valueEl.textContent = formatValue(attr.value, attr);
        valueEl.title = valueEl.textContent;
        const fittedFontSize = calculateFittedFontSize(
          state.settings.fontSize,
          valueEl.clientWidth,
          valueEl.scrollWidth,
        );
        valueEl.style.fontSize = `${fittedFontSize}px`;
        valueEl.dataset.fittedFontSize = String(fittedFontSize);
      }
    }
    updateConnection();
  }

  function updateConnection(): void {
    const conn = panel.querySelector('.conn') as HTMLElement | null;
    if (!conn) return;
    conn.className = 'conn';
    if (connectionState === 'connected') conn.classList.add('connected');
    else if (connectionState === 'connecting' || connectionState === 'reconnecting') conn.classList.add('reconnecting');
    else conn.classList.add('error');
    conn.style.display = state.settings.showConnection ? '' : 'none';
  }

  function swapBroadcastContent(attributeName: string, next: HTMLElement): void {
    const stage = broadcastStages.get(attributeName);
    if (!stage) return;
    const previous = stage.children[stage.children.length - 1] as HTMLElement | undefined;
    next.classList.add('is-entering');
    stage.append(next);
    previous?.classList.add('is-leaving');
    globalThis.setTimeout(() => {
      previous?.remove();
      next.classList.remove('is-entering');
    }, BROADCAST_TRANSITION_DURATION);
  }

  function showGiftBroadcast(entry: LogEntry): void {
    if (selectedAttributeName && entry.attributeName !== selectedAttributeName) return;
    const block = attrEls.get(entry.attributeName);
    const attribute = state.attributes.find((item) => item.name === entry.attributeName);
    if (!block || !attribute) return;
    block.classList.remove('flash');
    void block.offsetWidth;
    block.classList.add('flash');
    globalThis.setTimeout(() => block.classList.remove('flash'), 700);
    clearBroadcastReturnTimer(entry.attributeName);
    swapBroadcastContent(entry.attributeName, createGiftBroadcast(entry, attribute));
    const timer = globalThis.setTimeout(() => {
      const latestAttribute = state.attributes.find((item) => item.name === entry.attributeName);
      if (latestAttribute) swapBroadcastContent(entry.attributeName, createDefaultBroadcast(latestAttribute));
      broadcastReturnTimers.delete(entry.attributeName);
    }, GIFT_BROADCAST_DURATION);
    broadcastReturnTimers.set(entry.attributeName, timer);
  }

  async function refreshState(): Promise<void> {
    if (stateRefreshActive) return;
    stateRefreshActive = true;
    try {
      const structure = displayStructureSignature(state);
      state = await refreshStateFromServer();
      if (displayStructureSignature(state) !== structure) renderAttrs();
      else updateAll();
      const newEntries = entriesSince(state.log, lastLogKey);
      lastLogKey = state.log[0] ? logKey(state.log[0]) : lastLogKey;
      for (const entry of newEntries.reverse()) {
        if (entry.source !== 'timer') showGiftBroadcast(entry);
      }
    } finally {
      stateRefreshActive = false;
    }
  }

  async function refreshRuntime(): Promise<void> {
    try {
      connectionState = (await getRuntimeStatus()).state;
    } catch {
      connectionState = 'error';
    }
    updateConnection();
  }

  renderAttrs();
  void refreshRuntime();
  const pollTimer = globalThis.setInterval(() => {
    void refreshState();
    void refreshRuntime();
  }, 750);
  if (typeof globalThis.addEventListener === 'function') {
    globalThis.addEventListener('beforeunload', () => {
      globalThis.clearInterval(pollTimer);
      for (const attributeName of broadcastReturnTimers.keys()) clearBroadcastReturnTimer(attributeName);
    }, { once: true });
  }
}

function createDefaultBroadcast(attribute: Attribute): HTMLElement {
  const message = attribute.broadcastMessage?.trim() || DEFAULT_BROADCAST_MESSAGE;
  return el('div', { class: 'broadcast-content broadcast-default' }, [
    el('div', { class: 'broadcast-marquee' }, [
      el('span', { class: 'broadcast-marquee-text', text: message, title: message }),
    ]),
  ]);
}

function createGiftBroadcast(entry: LogEntry, attribute: Attribute): HTMLElement {
  const avatar = el('img', {
    class: 'broadcast-avatar',
    alt: entry.uname ? `${entry.uname}的头像` : '用户头像',
    referrerPolicy: 'no-referrer',
  }) as HTMLImageElement;
  avatar.src = entry.avatar || transparentPixel();
  const nickname = entry.uname?.trim() || '匿名观众';
  const delta = formatDelta(entry.delta, attribute);
  return el('div', { class: 'broadcast-content broadcast-gift' }, [
    avatar,
    el('strong', { class: 'broadcast-user-name', text: nickname, title: nickname }),
    el('span', { class: 'broadcast-action', text: '投喂了' }),
    el('span', { class: 'broadcast-gift-name', text: `${entry.giftName || '礼物'}x${Math.max(1, entry.num || 1)}` }),
    el('span', { class: 'broadcast-attribute-name', text: entry.attributeName, title: entry.attributeName }),
    el('strong', { class: 'broadcast-delta', text: delta, title: delta }),
  ]);
}

function entriesSince(log: LogEntry[], previousKey: string): LogEntry[] {
  const entries: LogEntry[] = [];
  for (const entry of log) {
    if (previousKey && logKey(entry) === previousKey) break;
    entries.push(entry);
  }
  return entries;
}

function displayStructureSignature(state: AppState): string {
  return JSON.stringify({
    attributes: state.attributes.map(({ value: _value, ...attribute }) => attribute),
    rules: state.rules,
    giftCatalog: state.giftCatalog,
    settings: state.settings,
  });
}

function logKey(entry: LogEntry): string {
  return entry.eventId || `${entry.time}:${entry.ruleId}:${entry.attributeName}:${entry.valueAfter}`;
}

function transparentPixel(): string {
  return 'data:image/gif;base64,R0lGODlhAQABAIAAAAAAAP///yH5BAEAAAAALAAAAAABAAEAAAIBRAA7';
}
