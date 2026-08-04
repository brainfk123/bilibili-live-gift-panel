import { AppState, AttributeValueMapping, MAX_LOG } from './types';
import { normalizeDisplayThemeId } from './display-themes';
import { normalizeDisplayScenes } from './display-scenes';
import { normalizeActivities } from './activities';
import { normalizeTrainingTopicIds } from './training';

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
    trainingCompletedTopics: [],
    autoUpdate: true,
  },
  giftCatalog: [],
  recentGifts: [],
  stats: {},
  log: [],
  contributions: { viewers: [] },
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
  if (!Array.isArray(parsed.settings?.trainingCompletedTopics)) configMigrationRequired = true;
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
    trainingCompletedTopics: normalizeTrainingTopicIds(parsed.settings?.trainingCompletedTopics),
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
    contributions: normalizeContributionLedger(parsed.contributions),
  };
}

function normalizeContributionLedger(ledger: Partial<AppState['contributions']> | undefined): AppState['contributions'] {
  const seen = new Set<string>();
  const viewers: AppState['contributions']['viewers'] = [];
  for (const raw of Array.isArray(ledger?.viewers) ? ledger.viewers : []) {
    const uid = Number(raw.uid);
    const uname = String(raw.uname ?? '').trim().slice(0, 80) || '匿名观众';
    const key = String(raw.key ?? '').trim() || (Number.isFinite(uid) && uid > 0
      ? `uid:${Math.trunc(uid)}`
      : `name:${uname.toLocaleLowerCase()}`);
    if (!key || seen.has(key)) continue;
    seen.add(key);
    const attributeDeltas = Object.fromEntries(Object.entries(raw.attributeDeltas ?? {})
      .map(([name, value]) => [String(name).trim().slice(0, 80), Number(value)] as const)
      .filter(([name, value]) => Boolean(name) && Number.isFinite(value)));
    const blindBoxCost = nonNegativeNumber(raw.blindBoxCost);
    const blindBoxValue = nonNegativeNumber(raw.blindBoxValue);
    viewers.push({
      key,
      ...(Number.isFinite(uid) && uid > 0 ? { uid: Math.trunc(uid) } : {}),
      uname,
      ...(String(raw.avatar ?? '').trim() ? { avatar: String(raw.avatar).trim().slice(0, 2048) } : {}),
      giftCount: nonNegativeInteger(raw.giftCount),
      goldValue: nonNegativeNumber(raw.goldValue),
      silverValue: nonNegativeNumber(raw.silverValue),
      ruleTriggers: nonNegativeInteger(raw.ruleTriggers),
      attributeDeltas,
      blindBoxCount: nonNegativeInteger(raw.blindBoxCount),
      blindBoxCost,
      blindBoxValue,
      blindBoxProfit: blindBoxValue - blindBoxCost,
      ...(nonNegativeInteger(raw.unpricedBlindBoxCount) > 0
        ? { unpricedBlindBoxCount: nonNegativeInteger(raw.unpricedBlindBoxCount) }
        : {}),
      lastGiftAt: nonNegativeNumber(raw.lastGiftAt),
    });
    if (viewers.length >= 2000) break;
  }
  viewers.sort((left, right) => right.lastGiftAt - left.lastGiftAt);
  const updatedAt = nonNegativeNumber(ledger?.updatedAt);
  return { viewers, ...(updatedAt > 0 ? { updatedAt } : {}) };
}

function nonNegativeNumber(value: unknown): number {
  const normalized = Number(value);
  return Number.isFinite(normalized) ? Math.max(0, normalized) : 0;
}

function nonNegativeInteger(value: unknown): number {
  return Math.trunc(nonNegativeNumber(value));
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
