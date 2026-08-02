import { getRuntimeStatus, RuntimeConnectionState } from '../../backend';
import { findGift } from '../../gifts/catalog';
import { formatValue, todayStr } from '../../format';
import { loadState, refreshStateFromServer } from '../../storage';
import { AppState, Attribute, LogEntry } from '../../types';
import { createBrandIcon } from '../brand';
import { el } from '../common';

export function formatDelta(delta: number, attr?: Attribute): string {
  const sign = delta > 0 ? '+' : delta < 0 ? '-' : '';
  const value = attr ? formatValue(Math.abs(delta), attr) : String(Math.abs(delta));
  return `${sign}${value}`;
}

export function mountDisplay(root: HTMLElement, selectedAttributeName?: string): void {
  let state = loadState();
  let connectionState: RuntimeConnectionState = 'idle';
  let lastLogKey = state.log[0] ? logKey(state.log[0]) : '';
  let stateRefreshActive = false;
  const panel = el('div', { class: 'panel' });
  const attrEls = new Map<string, HTMLElement>();
  const toastWrap = el('div', { class: 'toast-wrap' });
  root.replaceChildren(panel, toastWrap);

  const visibleAttributes = (): Attribute[] => selectedAttributeName
    ? state.attributes.filter((attribute) => attribute.name === selectedAttributeName)
    : state.attributes;

  function renderAttrs(): void {
    panel.replaceChildren();
    attrEls.clear();
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
        el('div', { class: 'attr-name', text: attr.name }),
        el('div', { class: 'attr-value' }),
      ]);
      const giftRules = el('div', { class: 'display-gift-rules' });
      const rules = state.rules.filter((rule) => rule.attributeName === attr.name);
      if (rules.length === 0) {
        giftRules.append(el('div', { class: 'display-rule-empty', text: '还没有配置礼物公式' }));
      } else {
        for (const rule of rules) {
          const gift = findGift(state, rule.giftId);
          const image = el('img', { class: 'display-gift-image', alt: gift?.name ?? '' }) as HTMLImageElement;
          image.src = gift?.imgBasic || transparentPixel();
          giftRules.append(el('div', { class: 'display-gift-rule' }, [
            image,
            el('div', { class: 'display-gift-copy' }, [
              el('span', { class: 'display-gift-name', text: gift?.name ?? `礼物 ${rule.giftId}` }),
              el('strong', { class: 'display-formula-name', text: rule.formulaName?.trim() || '未命名公式' }),
            ]),
          ]));
        }
      }
      const block = el('section', { class: 'attr' }, [summary, giftRules]);
      attrEls.set(attr.name, block);
      panel.append(block);
    }
    panel.append(el('div', { class: 'conn' }));
    updateAll();
  }

  function updateAll(): void {
    root.style.setProperty('--accent', state.settings.accentColor);
    root.style.setProperty('--accent2', state.settings.accentColor);
    panel.classList.toggle('center', state.settings.align === 'center');
    panel.classList.toggle('right', state.settings.align === 'right');
    for (const attr of visibleAttributes()) {
      const block = attrEls.get(attr.name);
      if (!block) continue;
      const valueEl = block.querySelector('.attr-value') as HTMLElement | null;
      if (valueEl) {
        valueEl.style.fontSize = `${state.settings.fontSize}px`;
        valueEl.textContent = formatValue(attr.value, attr);
      }
    }
    updateConnection();
    const stats = panel.querySelector('.stats') as HTMLElement | null;
    if (state.settings.showStats) {
      const nextStats = stats ?? el('div', { class: 'stats' });
      if (!stats) panel.append(nextStats);
      updateStats(nextStats);
    } else {
      stats?.remove();
    }
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

  function updateStats(statsEl: HTMLElement): void {
    const day = state.stats[todayStr()];
    const giftTotal = day ? Object.values(day.giftTotals).reduce((sum, value) => sum + value, 0) : 0;
    const giftStat = el('span', { class: 'gift-stat' });
    giftStat.append(createBrandIcon(16, 'stats-brand-icon'), el('span', { text: `礼物 ${giftTotal}` }));
    statsEl.replaceChildren(giftStat);
  }

  function showGiftChange(entry: LogEntry): void {
    if (selectedAttributeName && entry.attributeName !== selectedAttributeName) return;
    const block = attrEls.get(entry.attributeName);
    if (block) {
      block.classList.remove('flash');
      void block.offsetWidth;
      block.classList.add('flash');
      globalThis.setTimeout(() => block.classList.remove('flash'), 700);
    }
    const gift = findGift(state, entry.giftId);
    const image = el('img') as HTMLImageElement;
    image.src = gift?.imgBasic || transparentPixel();
    const attribute = state.attributes.find((item) => item.name === entry.attributeName);
    const notification = el('div', { class: 'gift-toast' }, [
      image,
      el('div', { class: 'grow' }, [
        el('div', { class: 'gt-name', text: `${entry.uname} 送出 ${entry.giftName}` }),
        el('div', { class: 'gt-delta', text: `${entry.attributeName} ${formatDelta(entry.delta, attribute)}` }),
      ]),
    ]);
    toastWrap.append(notification);
    globalThis.setTimeout(() => notification.remove(), 3200);
  }

  async function refreshState(): Promise<void> {
    if (stateRefreshActive) return;
    stateRefreshActive = true;
    try {
      const structure = displayStructureSignature(state);
      state = await refreshStateFromServer();
      if (displayStructureSignature(state) !== structure) renderAttrs();
      else updateAll();
      const latest = state.log[0];
      if (latest && logKey(latest) !== lastLogKey) {
        lastLogKey = logKey(latest);
        if (latest.source !== 'timer') showGiftChange(latest);
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
    globalThis.addEventListener('beforeunload', () => globalThis.clearInterval(pollTimer), { once: true });
  }
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
  return `${entry.time}:${entry.ruleId}:${entry.valueAfter}`;
}

function transparentPixel(): string {
  return 'data:image/gif;base64,R0lGODlhAQABAIAAAAAAAP///yH5BAEAAAAALAAAAAABAAEAAAIBRAA7';
}
