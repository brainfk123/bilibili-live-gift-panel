import { getRuntimeStatus, type RuntimeConnectionState } from '../../backend';
import { buildBlindBoxLeaderboard, listBlindBoxLeaderboardScopes } from '../../blind-box-leaderboard';
import { getDisplayTheme } from '../../display-themes';
import { loadState, refreshStateFromServer } from '../../storage';
import type { AppState, ViewerContribution } from '../../types';
import { createBrandIcon } from '../brand';
import { el } from '../common';
import {
  formatCompactYuanFromGoldSeeds,
  formatSignedYuanFromGoldSeeds,
  formatYuanFromGoldSeeds,
} from '../../currency';

const MAX_RANKED_VIEWERS = 100;
const ROW_DWELL_MS = 1_400;
const EDGE_PAUSE_MS = 3_200;
const ROW_TRANSITION_MS = 700;

export interface BlindBoxScrollPosition {
  index: number;
  direction: 1 | -1;
}

export function advanceBlindBoxScroll(
  index: number,
  direction: 1 | -1,
  maxIndex: number,
): BlindBoxScrollPosition {
  if (maxIndex <= 0) return { index: 0, direction: 1 };
  let nextDirection = direction;
  let nextIndex = index + nextDirection;
  if (nextIndex < 0 || nextIndex > maxIndex) {
    nextDirection = nextDirection === 1 ? -1 : 1;
    nextIndex = index + nextDirection;
  }
  nextIndex = Math.max(0, Math.min(maxIndex, nextIndex));
  if (nextIndex === 0) nextDirection = 1;
  else if (nextIndex === maxIndex) nextDirection = -1;
  return { index: nextIndex, direction: nextDirection };
}

export function mountBlindBoxDisplay(root: HTMLElement, blindBoxGiftId?: number): void {
  let state = loadState();
  let connectionState: RuntimeConnectionState = 'idle';
  let refreshActive = false;
  let renderedSignature = '';
  let leaderboardScrollTimer: ReturnType<typeof globalThis.setTimeout> | undefined;
  let leaderboardScrollIndex = 0;
  let leaderboardScrollDirection: 1 | -1 = 1;
  let leaderboardRenderVersion = 0;
  const stack = el('div', { class: 'display-stack blind-box-stack' });
  root.replaceChildren(stack);

  function render(): void {
    stopLeaderboardScroll();
    const renderVersion = ++leaderboardRenderVersion;
    const leaderboard = buildBlindBoxLeaderboard(state.contributions, MAX_RANKED_VIEWERS, blindBoxGiftId);
    const selectedScope = listBlindBoxLeaderboardScopes(state.contributions)
      .find((scope) => scope.giftId === blindBoxGiftId);
    const scopeName = blindBoxGiftId ? selectedScope?.giftName ?? `盲盒 ${blindBoxGiftId}` : '全部盲盒';
    const appearance = state.blindBoxDisplay;
    const viewerSlots = normalizeViewerSlots(state.blindBoxDisplay.viewerSlots);
    const theme = getDisplayTheme(appearance.themeId);
    const panel = el('main', { class: 'panel blind-box-panel' });
    panel.dataset.theme = theme.id;
    panel.style.setProperty('--theme-accent', theme.id === 'glass' ? appearance.accentColor : theme.accent);
    panel.style.setProperty('--theme-surface', theme.surface);

    panel.append(el('header', { class: 'blind-box-header' }, [
      el('div', { class: 'blind-box-title' }, [
        el('span', { text: scopeName }),
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

    panel.append(el('section', { class: 'blind-box-summary', ariaLabel: `${scopeName}汇总` } as any, [
      summaryItem('盲盒数量', `${formatNumber(leaderboard.summary.blindBoxCount)} 个`),
      summaryItem('投入', formatCompactYuanFromGoldSeeds(leaderboard.summary.cost), formatYuanFromGoldSeeds(leaderboard.summary.cost)),
      summaryItem('开出', formatCompactYuanFromGoldSeeds(leaderboard.summary.value), formatYuanFromGoldSeeds(leaderboard.summary.value)),
    ]));

    if (leaderboard.viewers.length === 0) {
      const empty = el('div', { class: 'blind-box-empty' });
      empty.append(
        createBrandIcon(56, 'blind-box-empty-icon'),
        el('strong', { text: blindBoxGiftId ? `还没有${scopeName}记录` : '还没有盲盒记录' }),
        el('span', { text: blindBoxGiftId ? `收到${scopeName}后会自动统计投入、开出价值和盈亏。` : '收到盲盒礼物后会自动统计投入、开出价值和盈亏。' }),
      );
      panel.append(empty);
    } else {
      const viewport = el('div', { class: 'blind-box-ranking', ariaLabel: '观众盲盒盈亏排名' } as any);
      const list = el('ol', { class: 'blind-box-ranking-track' });
      leaderboard.viewers.forEach((viewer, index) => list.append(viewerRow(viewer, index + 1)));
      viewport.append(list);
      panel.append(viewport);
      globalThis.queueMicrotask(() => {
        if (renderVersion === leaderboardRenderVersion) startLeaderboardScroll(viewport, list, leaderboard.viewers.length, viewerSlots);
      });
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

  function stopLeaderboardScroll(): void {
    if (leaderboardScrollTimer !== undefined) {
      globalThis.clearTimeout(leaderboardScrollTimer);
      leaderboardScrollTimer = undefined;
    }
  }

  function startLeaderboardScroll(viewport: HTMLElement, track: HTMLElement, rowCount: number, viewerSlots: number): void {
    const rows = Array.from(track.querySelectorAll<HTMLElement>('.blind-box-row'));
    if (rows.length === 0) return;
    const visibleRows = Math.min(viewerSlots, rows.length);
    const rowOffsets = rows.map((row) => row.offsetTop - rows[0].offsetTop);
    const visibleHeight = rows.slice(0, visibleRows).reduce((height, row) => height + row.offsetHeight, 0);
    if (visibleHeight <= 0) {
      leaderboardScrollTimer = globalThis.setTimeout(() => startLeaderboardScroll(viewport, track, rowCount, viewerSlots), 80);
      return;
    }

    viewport.style.height = `${visibleHeight}px`;
    const maxIndex = Math.max(0, rowCount - visibleRows);
    leaderboardScrollIndex = Math.min(leaderboardScrollIndex, maxIndex);
    if (leaderboardScrollIndex === maxIndex && maxIndex > 0) leaderboardScrollDirection = -1;
    const applyPosition = (): void => {
      track.style.transform = `translateY(${-rowOffsets[leaderboardScrollIndex]}px)`;
      viewport.dataset.scrollIndex = String(leaderboardScrollIndex);
      viewport.dataset.scrollDirection = leaderboardScrollDirection > 0 ? 'down' : 'up';
    };
    applyPosition();
    if (maxIndex === 0) return;

    const scheduleNext = (delay: number): void => {
      leaderboardScrollTimer = globalThis.setTimeout(() => {
        const next = advanceBlindBoxScroll(leaderboardScrollIndex, leaderboardScrollDirection, maxIndex);
        leaderboardScrollIndex = next.index;
        leaderboardScrollDirection = next.direction;
        applyPosition();
        const atEdge = leaderboardScrollIndex === 0 || leaderboardScrollIndex === maxIndex;
        scheduleNext(atEdge ? EDGE_PAUSE_MS : ROW_DWELL_MS);
      }, delay);
    };
    track.style.transitionDuration = `${ROW_TRANSITION_MS}ms`;
    scheduleNext(leaderboardScrollIndex === 0 || leaderboardScrollIndex === maxIndex ? EDGE_PAUSE_MS : ROW_DWELL_MS);
  }

  function updateConnection(panel = stack.querySelector<HTMLElement>('.blind-box-panel')): void {
    const indicator = panel?.querySelector<HTMLElement>('.conn');
    if (!indicator) return;
    indicator.className = connectionClass(connectionState);
    indicator.style.display = state.blindBoxDisplay.showConnection ? '' : 'none';
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
    globalThis.addEventListener('beforeunload', () => {
      globalThis.clearInterval(pollTimer);
      stopLeaderboardScroll();
    }, { once: true });
  }
}

function normalizeViewerSlots(value: number | undefined): number {
  const normalized = Number(value);
  return Math.min(10, Math.max(1, Number.isFinite(normalized) ? Math.trunc(normalized) : 3));
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
  const appearance = state.blindBoxDisplay;
  const opacity = Math.min(100, Math.max(10, Number(appearance.panelOpacity) || 55)) / 100;
  const root = stack.parentElement;
  root?.style.setProperty('--accent', appearance.accentColor);
  root?.style.setProperty('--accent2', appearance.accentColor);
  root?.style.setProperty('--panel-opacity', String(opacity));
  root?.style.setProperty('--panel-surface-opacity', String(opacity * 0.62));
  stack.classList.toggle('center', appearance.align === 'center');
  stack.classList.toggle('right', appearance.align === 'right');
  panel.style.setProperty('--leaderboard-font-scale', String(Math.max(.8, Math.min(1.35, appearance.fontSize / 48))));
}

function displaySignature(state: AppState): string {
  return JSON.stringify({ contributions: state.contributions, appearance: state.blindBoxDisplay });
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
