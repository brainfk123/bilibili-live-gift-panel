import { AppState, MAX_LOG } from './types';

const CONFIG_ENDPOINT = '/api/config';
let cachedState: AppState | null = null;
let persistQueue = Promise.resolve();
let configMigrationRequired = false;

export const defaultState = (): AppState => ({
  roomId: '',
  attributes: [],
  rules: [],
  timerRules: [],
  formulaPresets: [],
  settings: {
    fontSize: 48,
    accentColor: '#fb7299',
    showStats: true,
    showConnection: true,
    align: 'center',
    theme: 'dark',
    giftView: 'list',
    panelOpacity: 55,
    showTutorial: true,
  },
  giftCatalog: [],
  recentGifts: [],
  stats: {},
  log: [],
});

export function loadState(): AppState {
  if (!cachedState) cachedState = defaultState();
  return cachedState;
}

export function saveState(state: AppState): Promise<void> {
  cachedState = normalizeState(state);
  const serialized = JSON.stringify(cachedState);
  persistQueue = persistQueue
    .catch(() => undefined)
    .then(() => persistStateToServer(serialized));
  return persistQueue;
}

export function resetState(): void {
  cachedState = defaultState();
  configMigrationRequired = false;
  if (typeof fetch === 'function') {
    void fetch(CONFIG_ENDPOINT, { method: 'DELETE', keepalive: true }).catch(() => undefined);
  }
}

export function consumeConfigMigrationRequired(): boolean {
  const required = configMigrationRequired;
  configMigrationRequired = false;
  return required;
}

export async function hydrateStateFromServer(): Promise<void> {
  await refreshStateFromServer();
}

export async function refreshStateFromServer(acceptState: () => boolean = () => true): Promise<AppState> {
  if (typeof fetch !== 'function') return loadState();
  await persistQueue.catch(() => undefined);
  try {
    const response = await fetch(CONFIG_ENDPOINT, { cache: 'no-store' });
    let nextState: AppState;
    if (response.status === 204) {
      nextState = defaultState();
    } else {
      if (!response.ok) throw new Error(`配置读取失败：HTTP ${response.status}`);
      nextState = normalizeState(await response.json() as Partial<AppState>);
    }
    if (acceptState() || !cachedState) cachedState = nextState;
  } catch {
    // A transient backend read failure must not erase the last visible state.
    cachedState ??= defaultState();
  }
  return cachedState;
}

async function persistStateToServer(serialized: string): Promise<void> {
  if (typeof fetch !== 'function') return;
  const response = await fetch(CONFIG_ENDPOINT, {
    method: 'PUT',
    headers: { 'Content-Type': 'application/json' },
    body: serialized,
    keepalive: true,
  });
  if (!response.ok) throw new Error(`配置保存失败：HTTP ${response.status}`);
}

function normalizeState(parsed: Partial<AppState>): AppState {
  const base = defaultState();
  const setupComplete = Boolean(parsed.roomId?.trim())
    && (parsed.attributes?.length ?? 0) > 0
    && (parsed.rules?.length ?? 0) > 0;
  if (parsed.settings?.showTutorial === undefined) configMigrationRequired = true;
  const showTutorial = parsed.settings?.showTutorial ?? !setupComplete;
  const settings = { ...base.settings, ...(parsed.settings ?? {}), showTutorial };
  settings.panelOpacity = Math.min(100, Math.max(10, Number(settings.panelOpacity) || base.settings.panelOpacity));
  return {
    ...base,
    ...parsed,
    settings,
    attributes: parsed.attributes ?? base.attributes,
    rules: parsed.rules ?? base.rules,
    timerRules: parsed.timerRules ?? base.timerRules,
    formulaPresets: parsed.formulaPresets ?? base.formulaPresets,
  };
}

export function pruneLog(log: AppState['log']): AppState['log'] {
  return log.slice(0, MAX_LOG);
}
