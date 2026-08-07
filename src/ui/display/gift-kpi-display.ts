import { getRuntimeStatus, type RuntimeConnectionState } from '../../backend';
import { getDisplayTheme } from '../../display-themes';
import { refreshStateFromServer } from '../../storage';
import type { GiftKpiPanel } from '../../types';
import { el } from '../common';

export function mountGiftKpiDisplay(root: HTMLElement, panelId?: string): void {
  const stack = el('div', { class: 'display-stack gift-kpi-stack' });
  let connectionState: RuntimeConnectionState = 'connecting';
  let refreshActive = false;
  root.replaceChildren(stack);

  const render = (panel?: GiftKpiPanel): void => {
    if (!panel) {
      stack.replaceChildren(el('main', { class: 'panel gift-kpi-panel' }, [el('div', { class: 'display-empty', text: '找不到礼物 KPI 面板' })]));
      return;
    }
    const appearance = panel.appearance;
    const theme = getDisplayTheme(appearance.themeId);
    root.style.setProperty('--accent', appearance.accentColor);
    root.style.setProperty('--panel-opacity', String(Math.max(10, appearance.panelOpacity) / 100));
    root.style.setProperty('--kpi-font-scale', String(Math.max(.8, Math.min(1.35, appearance.fontSize / 48))));
    stack.className = `display-stack gift-kpi-stack is-${panel.layout}${appearance.align === 'center' ? ' center' : appearance.align === 'right' ? ' right' : ''}`;
    const card = el('main', { class: `panel gift-kpi-panel is-${panel.layout}` });
    card.dataset.theme = theme.id;
    card.style.setProperty('--theme-accent', theme.id === 'glass' ? appearance.accentColor : theme.accent);
    card.append(el('header', { class: 'gift-kpi-header' }, [
      el('div', {}, [el('span', { text: '礼物 KPI' }), el('h1', { text: panel.name })]),
      el('strong', { text: `${panel.items.filter((item) => item.received >= item.target).length} / ${panel.items.length} 达成` }),
    ]));
    const list = el('div', { class: 'gift-kpi-list' });
    for (const item of panel.items) {
      const progress = Math.min(100, item.received / Math.max(1, item.target) * 100);
      const image = item.imageUrl
        ? el('img', { alt: '', referrerPolicy: 'no-referrer', src: item.imageUrl })
        : el('span', { class: 'gift-kpi-image-placeholder', text: '🎁', ariaHidden: 'true' });
      list.append(el('article', { class: `gift-kpi-item is-${item.barStyle}${progress >= 100 ? ' is-complete' : ''}`, style: `--kpi-progress:${progress}%` }, [
        image,
        el('div', { class: 'gift-kpi-copy' }, [
          el('div', { class: 'gift-kpi-item-head' }, [el('strong', { text: item.giftName }), el('span', { text: `${item.received} / ${item.target}` })]),
          el('div', { class: 'gift-kpi-meter' }, [el('i')]),
        ]),
      ]));
    }
    const connection = el('div', { class: connectionClass(connectionState) });
    connection.style.display = appearance.showConnection ? '' : 'none';
    card.append(list, connection);
    stack.replaceChildren(card);
  };

  const refresh = async (): Promise<void> => {
    if (refreshActive) return;
    refreshActive = true;
    try {
      const [state, runtime] = await Promise.all([
        refreshStateFromServer(),
        getRuntimeStatus().catch(() => ({ state: 'error' as const })),
      ]);
      connectionState = runtime.state;
      render(state.giftKpiPanels.find((panel) => panel.id === panelId));
    } finally {
      refreshActive = false;
    }
  };
  void refresh();
  const pollTimer = globalThis.setInterval(() => void refresh(), 750);
  if (typeof globalThis.addEventListener === 'function') {
    globalThis.addEventListener('beforeunload', () => globalThis.clearInterval(pollTimer), { once: true });
  }
}

function connectionClass(state: RuntimeConnectionState): string {
  if (state === 'connected') return 'conn connected';
  if (state === 'connecting' || state === 'reconnecting') return 'conn reconnecting';
  return 'conn error';
}
