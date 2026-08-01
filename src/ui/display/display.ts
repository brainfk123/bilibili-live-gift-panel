import { loadState, saveState } from '../../storage';
import { el } from '../common';
import { createBrandIcon } from '../brand';
import { Engine, TriggerResult } from '../../engine';
import { formatValue, todayStr } from '../../format';
import { ConnState } from '../../bilibili/client';

export function mountDisplay(root: HTMLElement): void {
  const state = loadState();
  const panel = el('div', { class: 'panel' });
  const attrEls = new Map<string, HTMLElement>();
  const toastWrap = el('div', { class: 'toast-wrap' });
  root.append(panel, toastWrap);

  function renderAttrs(): void {
    panel.replaceChildren();
    attrEls.clear();
    if (state.attributes.length === 0) {
      const empty = el('div', { class: 'display-empty' });
      empty.append(
        createBrandIcon(64, 'display-empty-brand-icon'),
        el('div', { text: '还没有可显示的属性' }),
      );
      panel.append(empty);
    }
    for (const attr of state.attributes) {
      const nameEl = el('div', { class: 'attr-name', text: attr.name });
      const valueEl = el('div', { class: 'attr-value' });
      const block = el('div', { class: 'attr' }, [nameEl, valueEl]);
      attrEls.set(attr.name, block);
      panel.append(block);
    }
    const conn = el('div', { class: 'conn' });
    panel.append(conn);
    updateAll();
  }

  function updateAll(): void {
    root.style.setProperty('--accent', state.settings.accentColor);
    root.style.setProperty('--accent2', state.settings.accentColor);
    panel.classList.toggle('center', state.settings.align === 'center');
    panel.classList.toggle('right', state.settings.align === 'right');
    for (const attr of state.attributes) {
      const block = attrEls.get(attr.name);
      if (!block) continue;
      const valueEl = block.querySelector('.attr-value') as HTMLElement;
      if (valueEl) {
        valueEl.style.fontSize = `${state.settings.fontSize}px`;
        valueEl.textContent = formatValue(attr.value, attr);
      }
    }
    const stats = panel.querySelector('.stats') as HTMLElement | null;
    if (state.settings.showStats) {
      if (!stats) {
        const s = el('div', { class: 'stats' });
        panel.append(s);
        updateStats(s);
      }
    } else if (stats) {
      stats.remove();
    }
  }

  function updateStats(statsEl: HTMLElement): void {
    const day = state.stats[todayStr()];
    const giftTotal = day ? Object.values(day.giftTotals).reduce((a, b) => a + b, 0) : 0;
    const giftStat = el('span', { class: 'gift-stat' });
    giftStat.append(
      createBrandIcon(16, 'stats-brand-icon'),
      el('span', { text: `礼物 ${giftTotal}` }),
    );
    statsEl.replaceChildren(giftStat);
  }

  const engine = new Engine(state, (r: TriggerResult) => {
    const block = attrEls.get(r.rule.attributeName);
    if (block) {
      block.classList.remove('flash');
      void block.offsetWidth;
      block.classList.add('flash');
      setTimeout(() => block.classList.remove('flash'), 700);
    }
    const gi = state.recentGifts.find((g) => g.id === r.gift.giftId);
    const img = el('img');
    img.src = gi?.imgBasic || 'data:image/gif;base64,R0lGODlhAQABAIAAAAAAAP///yH5BAEAAAAALAAAAAABAAEAAAIBRAA7';
    const attr = state.attributes.find((a) => a.name === r.rule.attributeName);
    const toast = el('div', { class: 'gift-toast' }, [
      img,
      el('div', { class: 'grow' }, [
        el('div', { class: 'gt-name', text: `${r.gift.uname} 送出 ${r.gift.giftName}` }),
        el('div', { class: 'gt-delta', text: attr ? `${r.rule.attributeName} +${formatValue(r.delta, attr)}` : `+${r.delta}` }),
      ]),
    ]);
    toastWrap.append(toast);
    setTimeout(() => toast.remove(), 3200);
    saveState(state);
  });

  engine.setStateListener((s: ConnState) => {
    const conn = panel.querySelector('.conn') as HTMLElement | null;
    if (!conn) return;
    conn.className = 'conn';
    if (s === 'connected') conn.classList.add('connected');
    else if (s === 'connecting' || s === 'reconnecting') conn.classList.add('reconnecting');
    else conn.classList.add('error');
    if (!state.settings.showConnection) conn.style.display = 'none';
    else conn.style.display = '';
  });

  renderAttrs();
  engine.start();
  setInterval(() => {
    const statsEl = panel.querySelector('.stats') as HTMLElement | null;
    if (state.settings.showStats && statsEl) updateStats(statsEl);
  }, 30000);
}
