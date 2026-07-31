import { AppState, MAX_LOG, STORAGE_KEY } from './types';

export const defaultState = (): AppState => ({
  roomId: '',
  attributes: [
    { name: '加班时间', value: 0, unit: 'seconds', format: 'hhmmss', decimals: 0, suffix: '' },
  ],
  rules: [],
  settings: {
    fontSize: 48,
    accentColor: '#fb7299',
    showStats: true,
    showConnection: true,
    align: 'left',
  },
  giftCatalog: [],
  recentGifts: [],
  stats: {},
  log: [],
});

export function loadState(): AppState {
  try {
    const raw = localStorage.getItem(STORAGE_KEY);
    if (!raw) return defaultState();
    const parsed = JSON.parse(raw) as Partial<AppState>;
    const base = defaultState();
    return {
      ...base,
      ...parsed,
      settings: { ...base.settings, ...(parsed.settings ?? {}) },
      attributes: parsed.attributes ?? base.attributes,
      rules: parsed.rules ?? [],
    };
  } catch {
    return defaultState();
  }
}

export function saveState(state: AppState): void {
  localStorage.setItem(STORAGE_KEY, JSON.stringify(state));
}

export function resetState(): void {
  localStorage.removeItem(STORAGE_KEY);
}

export function pruneLog(log: AppState['log']): AppState['log'] {
  return log.slice(-MAX_LOG);
}
