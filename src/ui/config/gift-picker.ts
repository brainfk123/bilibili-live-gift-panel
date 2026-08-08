import type { AppState, GiftInfo } from '../../types';
import { builtinCatalog, findGift, giftDisplayKey, matchesGiftSearch, sortGiftsByUsage } from '../../gifts/catalog';
import { giftPriceDescription, isSpecialEventGift } from '../../gifts/special-events';
import { el } from '../common';

export type GiftAvailability = 'special' | 'listed' | 'observed' | 'historical';

export interface GiftPickerCatalog {
  gifts: GiftInfo[];
  availabilityById: Map<number, GiftAvailability>;
  hasLiveListingStatus: boolean;
}

export interface GiftPickerOptions {
  catalog: GiftPickerCatalog;
  isSelected: (gift: GiftInfo) => boolean;
  onToggle: (gift: GiftInfo, selected: boolean) => void;
  onSelectionChange?: () => void;
  isDisabled?: (gift: GiftInfo) => boolean;
  emptyMessage?: string;
  searchClassName?: string;
  gridClassName?: string;
  batchSize?: number;
}

export interface GiftPicker {
  search: HTMLInputElement;
  grid: HTMLElement;
  focus: () => void;
  refresh: () => void;
  refreshSelection: () => void;
  setCatalog: (catalog: GiftPickerCatalog) => void;
}

export function buildGiftPickerCatalog(state: AppState, roomGiftCatalog: GiftInfo[]): GiftPickerCatalog {
  const configuredGifts = state.rules
    .map((rule) => findGift(state, rule.giftId))
    .filter((gift): gift is GiftInfo => gift !== undefined);
  const hasLiveListingStatus = roomGiftCatalog.some((gift) => typeof gift.listed === 'boolean');
  const candidates: Array<{ gift: GiftInfo; availability: GiftAvailability }> = [
    ...roomGiftCatalog.map((gift) => ({
      gift,
      availability: (hasLiveListingStatus && gift.listed === false ? 'historical' : 'listed') as GiftAvailability,
    })),
    ...state.recentGifts.map((gift) => ({
      gift,
      availability: (gift.count > 0 || gift.lastReceived > 0 ? 'observed' : 'historical') as GiftAvailability,
    })),
    ...state.giftCatalog.map((gift) => ({ gift, availability: 'historical' as GiftAvailability })),
    ...configuredGifts.map((gift) => ({ gift, availability: 'historical' as GiftAvailability })),
    ...builtinCatalog.map((gift) => ({
      gift,
      availability: (isSpecialEventGift(gift) ? 'special' : 'historical') as GiftAvailability,
    })),
  ];
  const priority: Record<GiftAvailability, number> = { special: 4, listed: 3, observed: 2, historical: 1 };
  const byDisplayKey = new Map<string, { gift: GiftInfo; availability: GiftAvailability }>();
  for (const candidate of candidates) {
    const key = giftDisplayKey(candidate.gift);
    const existing = byDisplayKey.get(key);
    if (!existing || priority[candidate.availability] > priority[existing.availability]) {
      byDisplayKey.set(key, {
        ...candidate,
        gift: candidate.gift.requiresLogin || existing?.gift.requiresLogin
          ? { ...candidate.gift, requiresLogin: true }
          : candidate.gift,
      });
    } else if (candidate.gift.requiresLogin && !existing.gift.requiresLogin) {
      byDisplayKey.set(key, { ...existing, gift: { ...existing.gift, requiresLogin: true } });
    }
  }
  const merged = Array.from(byDisplayKey.values());
  const availabilityById = new Map(merged.map(({ gift, availability }) => [gift.id, availability]));
  const gifts = sortGiftsByUsage(merged.map(({ gift }) => gift), configuredGifts, state.recentGifts)
    .sort((left, right) => Number(isSpecialEventGift(right)) - Number(isSpecialEventGift(left)));
  return { gifts, availabilityById, hasLiveListingStatus };
}

export function createGiftLoginBadge(gift: Pick<GiftInfo, 'requiresLogin'>): HTMLElement | null {
  if (!gift.requiresLogin) return null;
  return el('span', {
    class: 'gift-login-status',
    text: '需登录',
    title: '自动识别这类礼物需要登录 B 站账号',
  });
}

export function filterGiftPickerGifts(catalog: GiftPickerCatalog, query: string): GiftInfo[] {
  return catalog.gifts.filter((gift) => (query.length > 0
    || !catalog.hasLiveListingStatus
    || catalog.availabilityById.get(gift.id) !== 'historical')
    && matchesGiftSearch(gift, query));
}

export function createGiftPicker(options: GiftPickerOptions): GiftPicker {
  let catalog = options.catalog;
  let filteredGifts: GiftInfo[] = [];
  let visibleGiftCount = 0;
  let loader: HTMLButtonElement | null = null;
  const buttons = new Map<number, { gift: GiftInfo; button: HTMLButtonElement }>();
  const batchSize = options.batchSize ?? 40;
  const searchClassName = ['field-input', 'gift-search', options.searchClassName].filter(Boolean).join(' ');
  const gridClassName = ['gift-picker-grid', options.gridClassName].filter(Boolean).join(' ');
  const search = el('input', { class: searchClassName, placeholder: '搜索礼物名称或 ID…' }) as HTMLInputElement;
  const grid = el('div', { class: gridClassName });

  const refreshSelection = (): void => {
    for (const { gift, button } of buttons.values()) {
      const selected = options.isSelected(gift);
      button.classList.toggle('is-selected', selected);
      const check = button.querySelector('.gift-choice-check');
      if (check) check.textContent = selected ? '✓' : '+';
      button.setAttribute('aria-pressed', String(selected));
      button.disabled = options.isDisabled?.(gift) ?? false;
    }
  };

  const createChoice = (gift: GiftInfo, showAllStatuses: boolean): HTMLButtonElement => {
    const availability = catalog.availabilityById.get(gift.id) ?? 'historical';
    const showStatus = showAllStatuses || availability === 'observed' || availability === 'special';
    const selected = options.isSelected(gift);
    const loginBadge = createGiftLoginBadge(gift);
    const statusLabel: Record<GiftAvailability, string> = {
      special: '直播事件',
      listed: '已上架',
      observed: '直播中收到过',
      historical: '历史礼物',
    };
    const button = el('button', {
      class: `gift-choice${selected ? ' is-selected' : ''}`,
      type: 'button',
      ariaPressed: String(selected),
      disabled: options.isDisabled?.(gift) ?? false,
    } as any) as HTMLButtonElement;
    button.dataset.giftId = String(gift.id);
    button.append(
      el('img', { class: 'gift-choice-image', alt: '', src: gift.imgBasic || transparentPixel(), referrerPolicy: 'no-referrer' }),
      el('span', { class: 'gift-choice-copy' }, [
        el('strong', { text: gift.name }),
        el('span', { class: 'gift-choice-meta' }, [
          el('small', { text: giftPriceDescription(gift) }),
          ...(showStatus ? [el('span', {
            class: `gift-listing-status is-${availability}`,
            text: statusLabel[availability],
          })] : []),
          ...(loginBadge ? [loginBadge] : []),
        ]),
      ]),
      el('span', { class: 'gift-choice-check', text: selected ? '✓' : '+' }),
    );
    button.onclick = () => {
      options.onToggle(gift, !options.isSelected(gift));
      refreshSelection();
      options.onSelectionChange?.();
    };
    buttons.set(gift.id, { gift, button });
    return button;
  };

  const updateLoader = (): void => {
    loader?.remove();
    const complete = visibleGiftCount >= filteredGifts.length;
    loader = el('button', {
      class: `gift-picker-loader${complete ? ' is-complete' : ''}`,
      type: 'button',
      disabled: complete,
      text: complete
        ? `已显示全部 ${filteredGifts.length} 个礼物`
        : `继续下滑加载 · ${visibleGiftCount} / ${filteredGifts.length}`,
    }) as HTMLButtonElement;
    if (!complete) loader.onclick = appendBatch;
    grid.append(loader);
  };

  function appendBatch(): void {
    if (visibleGiftCount >= filteredGifts.length) return;
    loader?.remove();
    const nextCount = Math.min(filteredGifts.length, visibleGiftCount + batchSize);
    const showAllStatuses = search.value.trim().length > 0;
    for (const gift of filteredGifts.slice(visibleGiftCount, nextCount)) {
      grid.append(createChoice(gift, showAllStatuses));
    }
    visibleGiftCount = nextCount;
    updateLoader();
  }

  const refresh = (): void => {
    filteredGifts = filterGiftPickerGifts(catalog, search.value.trim());
    visibleGiftCount = 0;
    loader = null;
    buttons.clear();
    grid.replaceChildren();
    grid.scrollTop = 0;
    if (filteredGifts.length === 0) {
      grid.append(el('div', { class: 'picker-empty', text: options.emptyMessage ?? '没有匹配的礼物。' }));
      return;
    }
    appendBatch();
  };

  search.oninput = refresh;
  grid.onscroll = () => {
    const distanceToBottom = grid.scrollHeight - grid.scrollTop - grid.clientHeight;
    if (distanceToBottom <= 80) appendBatch();
  };
  refresh();

  return {
    search,
    grid,
    focus: () => search.focus(),
    refresh,
    refreshSelection,
    setCatalog: (nextCatalog) => {
      catalog = nextCatalog;
      refresh();
    },
  };
}

function transparentPixel(): string {
  return 'data:image/gif;base64,R0lGODlhAQABAIAAAAAAAP///yH5BAEAAAAALAAAAAABAAEAAAIBRAA7';
}
