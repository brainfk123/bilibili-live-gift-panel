import { getRuntimeStatus, type RuntimeConnectionState } from '../../backend';
import { buildBlindBoxLeaderboard } from '../../blind-box-leaderboard';
import { getDisplayTheme } from '../../display-themes';
import { loadState, refreshStateFromServer } from '../../storage';
import type { AppState, ViewerContribution } from '../../types';
import { createBrandIcon } from '../brand';
import { el } from '../common';
import {
  formatCompactYuanFromGoldSeeds,
  formatSignedYuanFromGoldSeeds,
  formatYuanFromGoldSeeds,
} from './currency';

const MAX_VISIBLE_VIEWERS = 10;

export function mountBlindBoxDisplay(root: HTMLElement): void {
  let state = loadState();
  let connectionState: RuntimeConnectionState = 'idle';
  let refreshActive = false;
  let renderedSignature = '';
  const stack = el('div', { class: 'display-stack blind-box-stack' });
  root.replaceChildren(stack);

  function render(): void {
    const leaderboard = buildBlindBoxLeaderboard(state.contributions, MAX_VISIBLE_VIEWERS);
    const theme = getDisplayTheme(state.settings.defaultDisplayThemeId);
    const panel = el('main', { class: 'panel blind-box-panel' });
    panel.dataset.theme = theme.id;
    panel.style.setProperty('--theme-accent', theme.id === 'glass' ? state.settings.accentColor : theme.accent);
    panel.style.setProperty('--theme-surface', theme.surface);

    panel.append(el('header', { class: 'blind-box-header' }, [
      el('div', { class: 'blind-box-title' }, [
        el('span', { text: '直播盲盒统计' }),
        el('h1', { text: '盲盒盈亏榜' }),
      ]),
      el('div', { class: 'blind-box-profit' }, [
        el('span', { text: '观众净盈亏' }),
        el('strong', {
          class: `is-${numberTone(leaderboard.summary.profit)}`,
          text: formatSignedYuanFromGoldSeeds(leaderboard.summary.profit),
          title: formatSignedYuanFromGoldSeeds(leaderboard.summary.profit),
        }),
      ]),
    ]));

    panel.append(el('section', { class: 'blind-box-summary', ariaLabel: '全场盲盒汇总' } as any, [
      summaryItem('开盒', `${formatNumber(leaderboard.summary.blindBoxCount)} 个`),
      summaryItem('投入', formatCompactYuanFromGoldSeeds(leaderboard.summary.cost), formatYuanFromGoldSeeds(leaderboard.summary.cost)),
      summaryItem('开出', formatCompactYuanFromGoldSeeds(leaderboard.summary.value), formatYuanFromGoldSeeds(leaderboard.summary.value)),
    ]));

    if (leaderboard.viewers.length === 0) {
      const empty = el('div', { class: 'blind-box-empty' });
      empty.append(
        createBrandIcon(56, 'blind-box-empty-icon'),
        el('strong', { text: '还没有盲盒记录' }),
        el('span', { text: '收到盲盒礼物后会自动统计投入、开出价值和盈亏。' }),
      );
      panel.append(empty);
    } else {
      const list = el('ol', { class: 'blind-box-ranking', ariaLabel: '观众盲盒盈亏排名' } as any);
      leaderboard.viewers.forEach((viewer, index) => list.append(viewerRow(viewer, index + 1)));
      panel.append(list);
    }

    panel.append(el('footer', { class: 'blind-box-footer' }, [
      el('span', { text: '金额均以元显示 · 净盈亏 = 开出价值 − 盲盒成本' }),
      ...(leaderboard.summary.unpricedCount > 0
        ? [el('strong', {
          class: 'blind-box-warning',
          text: `${formatNumber(leaderboard.summary.unpricedCount)} 个盲盒缺少成本价`,
        })]
        : []),
    ]));
    panel.append(el('div', { class: connectionClass(connectionState) }));
    stack.replaceChildren(panel);
    applyAppearance(stack, panel, state);
    updateConnection(panel);
  }

  function updateConnection(panel = stack.querySelector<HTMLElement>('.blind-box-panel')): void {
    const indicator = panel?.querySelector<HTMLElement>('.conn');
    if (!indicator) return;
    indicator.className = connectionClass(connectionState);
    indicator.style.display = state.settings.showConnection ? '' : 'none';
  }

  async function refreshState(): Promise<void> {
    if (refreshActive) return;
    refreshActive = true;
    try {
      state = await refreshStateFromServer();
      const signature = displaySignature(state);
      if (signature !== renderedSignature) {
        renderedSignature = signature;
        render();
      }
    } finally {
      refreshActive = false;
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

  renderedSignature = displaySignature(state);
  render();
  void refreshRuntime();
  const pollTimer = globalThis.setInterval(() => {
    void refreshState();
    void refreshRuntime();
  }, 750);
  if (typeof globalThis.addEventListener === 'function') {
    globalThis.addEventListener('beforeunload', () => globalThis.clearInterval(pollTimer), { once: true });
  }
}

function viewerRow(viewer: ViewerContribution, rank: number): HTMLElement {
  const avatar = el('img', {
    class: 'blind-box-avatar',
    alt: viewer.uname ? `${viewer.uname}的头像` : '用户头像',
    referrerPolicy: 'no-referrer',
  }) as HTMLImageElement;
  avatar.src = viewer.avatar || transparentPixel();
  const profit = formatSignedYuanFromGoldSeeds(viewer.blindBoxProfit);
  return el('li', { class: 'blind-box-row' }, [
    el('strong', { class: `blind-box-rank is-${Math.min(rank, 4)}`, text: String(rank) }),
    avatar,
    el('div', { class: 'blind-box-person' }, [
      el('strong', { text: viewer.uname || '匿名观众', title: viewer.uname || '匿名观众' }),
      el('span', { text: `${formatNumber(viewer.blindBoxCount)} 个盲盒` }),
    ]),
    el('div', { class: 'blind-box-values' }, [
      el('span', {
        text: `${formatCompactYuanFromGoldSeeds(viewer.blindBoxCost)} → ${formatCompactYuanFromGoldSeeds(viewer.blindBoxValue)}`,
        title: `投入 ${formatYuanFromGoldSeeds(viewer.blindBoxCost)}，开出 ${formatYuanFromGoldSeeds(viewer.blindBoxValue)}`,
      }),
      ...(viewer.unpricedBlindBoxCount
        ? [el('small', { text: `${formatNumber(viewer.unpricedBlindBoxCount)} 个未计成本` })]
        : []),
    ]),
    el('strong', {
      class: `blind-box-row-profit is-${numberTone(viewer.blindBoxProfit)}`,
      text: profit,
      title: profit,
    }),
  ]);
}

function summaryItem(label: string, value: string, title = value): HTMLElement {
  return el('div', { class: 'blind-box-summary-item' }, [
    el('span', { text: label }),
    el('strong', { text: value, title }),
  ]);
}

function applyAppearance(stack: HTMLElement, panel: HTMLElement, state: AppState): void {
  const opacity = Math.min(100, Math.max(10, Number(state.settings.panelOpacity) || 55)) / 100;
  const root = stack.parentElement;
  root?.style.setProperty('--accent', state.settings.accentColor);
  root?.style.setProperty('--accent2', state.settings.accentColor);
  root?.style.setProperty('--panel-opacity', String(opacity));
  root?.style.setProperty('--panel-surface-opacity', String(opacity * 0.62));
  stack.classList.toggle('center', state.settings.align === 'center');
  stack.classList.toggle('right', state.settings.align === 'right');
  panel.style.setProperty('--leaderboard-font-scale', String(Math.max(.8, Math.min(1.35, state.settings.fontSize / 48))));
}

function displaySignature(state: AppState): string {
  return JSON.stringify({ contributions: state.contributions, settings: state.settings });
}

function connectionClass(state: RuntimeConnectionState): string {
  if (state === 'connected') return 'conn connected';
  if (state === 'connecting' || state === 'reconnecting') return 'conn reconnecting';
  return 'conn error';
}

function numberTone(value: number): 'positive' | 'negative' | 'neutral' {
  return value > 0 ? 'positive' : value < 0 ? 'negative' : 'neutral';
}

function formatNumber(value: number): string {
  return Number(value || 0).toLocaleString('zh-CN', { maximumFractionDigits: 2 });
}

function transparentPixel(): string {
  return 'data:image/gif;base64,R0lGODlhAQABAIAAAAAAAP///yH5BAEAAAAALAAAAAABAAEAAAIBRAA7';
}
