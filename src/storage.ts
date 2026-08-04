import { AppState, AttributeValueMapping, MAX_LOG } from './types';
import { normalizeDisplayThemeId } from './display-themes';
import { normalizeDisplayScenes } from './display-scenes';
import { normalizeActivities } from './activities';

const CONFIG_ENDPOINT = '/api/config';
let cachedState: AppState | null = null;
let persistQueue = Promise.resolve();
let configMigrationRequired = false;

export const defaultState = (): AppState => ({
  roomId: '',
  attributes: [],
  displayScenes: [],
  activities: [],
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
    defaultDisplayThemeId: 'glass',
    showTutorial: true,
    tutorialVersion: 2,
    tutorialCompletedLessons: [],
    autoUpdate: true,
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
  if (parsed.settings?.tutorialVersion === undefined || !Array.isArray(parsed.settings?.tutorialCompletedLessons)) configMigrationRequired = true;
  if (parsed.settings?.autoUpdate === undefined) configMigrationRequired = true;
  if (parsed.settings?.defaultDisplayThemeId === undefined) configMigrationRequired = true;
  const showTutorial = parsed.settings?.showTutorial ?? !setupComplete;
  const tutorialCompletedLessons = Array.isArray(parsed.settings?.tutorialCompletedLessons)
    ? parsed.settings.tutorialCompletedLessons.filter((lesson): lesson is AppState['settings']['tutorialCompletedLessons'][number] => (
      ['room', 'attribute', 'basics', 'gift', 'rule', 'timer', 'preset', 'save', 'enable', 'output'].includes(String(lesson))
    ))
    : [];
  const settings = {
    ...base.settings,
    ...(parsed.settings ?? {}),
    showTutorial,
    tutorialVersion: 2,
    tutorialCompletedLessons: Array.from(new Set(tutorialCompletedLessons)),
  };
  settings.panelOpacity = Math.min(100, Math.max(10, Number(settings.panelOpacity) || base.settings.panelOpacity));
  settings.defaultDisplayThemeId = normalizeDisplayThemeId(settings.defaultDisplayThemeId);
  const attributes = (parsed.attributes ?? base.attributes).map((attribute) => (
    attribute.display
      ? {
        ...attribute,
        display: {
          ...attribute.display,
          themeId: normalizeDisplayThemeId(attribute.display.themeId ?? settings.defaultDisplayThemeId),
          valueMappings: normalizeValueMappings(attribute.display.valueMappings),
        },
      }
      : attribute
  ));
  const displayScenes = normalizeDisplayScenes(parsed.displayScenes, attributes, settings.defaultDisplayThemeId);
  return {
    ...base,
    ...parsed,
    settings,
    attributes,
    displayScenes,
    activities: normalizeActivities(parsed.activities, attributes, new Set(displayScenes.map((scene) => scene.id))),
    rules: parsed.rules ?? base.rules,
    timerRules: parsed.timerRules ?? base.timerRules,
    formulaPresets: parsed.formulaPresets ?? base.formulaPresets,
  };
}

function normalizeValueMappings(mappings: Array<Partial<AttributeValueMapping>> | undefined): AttributeValueMapping[] {
  const values = new Set<number>();
  const result: AttributeValueMapping[] = [];
  for (const mapping of mappings ?? []) {
    const value = Number(mapping.value);
    const label = String(mapping.label ?? '').trim().slice(0, 80);
    if (!Number.isFinite(value) || !label || values.has(value)) continue;
    values.add(value);
    const color = String(mapping.color ?? '').trim();
    const imageUrl = String(mapping.imageUrl ?? '').trim().slice(0, 2048);
    result.push({
      value,
      label,
      ...(/^#[0-9a-f]{6}$/i.test(color) ? { color } : {}),
      ...(/^(https?:\/\/|data:image\/)/i.test(imageUrl) ? { imageUrl } : {}),
    });
    if (result.length >= 50) break;
  }
  return result;
}

export function pruneLog(log: AppState['log']): AppState['log'] {
  return log.slice(0, MAX_LOG);
}
