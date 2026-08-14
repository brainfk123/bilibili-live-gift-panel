import {
  AppState,
  AttributeValueMapping,
  MAX_GIFT_RECEIPTS,
  MAX_LOG,
  type OvertimeGiftAction,
  type SimplePlay,
} from './types';
import { normalizeDisplayThemeId } from './display-themes';
import { normalizeDisplayScenes } from './display-scenes';
import { normalizeActivities } from './activities';
import { normalizeTrainingTopicIds } from './training';
import {
  normalizeBlindBoxDisplayAppearance,
  normalizeDisplayAppearance,
  normalizeGiftTargetLayout,
} from './output-config';
import { giftTargetPanelConfig, mergeGiftTargetPanelConfigs, type GiftTargetPanelConfig } from './gift-targets';
import { normalizeGiftClipCrop } from './ui/config/gift-clip-crop';

const CONFIG_ENDPOINT = '/api/config';
const CONFIG_BACKUP_SCHEMA_VERSION = 5;
const STATE_FIELD_KEYS = [
  'roomId',
  'attributes',
  'displayScenes',
  'blindBoxDisplay',
  'giftKpiPanels',
  'activities',
  'rules',
  'timerRules',
  'formulaPresets',
  'settings',
  'simplePlay',
  'giftCatalog',
  'recentGifts',
  'stats',
  'log',
  'contributions',
] as const satisfies ReadonlyArray<keyof AppState>;
type StateFieldKey = typeof STATE_FIELD_KEYS[number];
type StateFieldSnapshots = Record<StateFieldKey, string>;
type ConfigBackupFields = Pick<AppState,
  | 'roomId'
  | 'attributes'
  | 'displayScenes'
  | 'blindBoxDisplay'
  | 'activities'
  | 'rules'
  | 'timerRules'
  | 'formulaPresets'
  | 'settings'
  | 'simplePlay'
  | 'giftCatalog'> & { giftKpiPanels: GiftTargetPanelConfig[] };

export type ConfigBackup = ConfigBackupFields & { schemaVersion: number };

let cachedState: AppState | null = null;
let cachedStateRevision = 0;
let persistQueue = Promise.resolve();
let configMigrationRequired = false;
let persistedFieldSnapshots: Partial<StateFieldSnapshots> = {};
const forcePersistFields = new Set<StateFieldKey>();

export const defaultState = (): AppState => ({
  roomId: '',
  attributes: [],
  displayScenes: [],
  blindBoxDisplay: {
    themeId: 'glass',
    fontSize: 48,
    accentColor: '#fb7299',
    showConnection: true,
    align: 'center',
    panelOpacity: 55,
    viewerSlots: 3,
  },
  giftKpiPanels: [],
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
    tutorialVersion: 3,
    tutorialCompletedLessons: [],
    tutorialReplayMode: false,
    trainingCompletedTopics: [],
    lastSeenChangelogVersion: '',
    autoUpdate: true,
    configExperience: 'simple',
    giftClipCrops: {},
  },
  giftCatalog: [],
  recentGifts: [],
  stats: {},
  log: [],
  giftReceipts: [],
  contributions: { viewers: [] },
});

export function clearRoomScopedRecords(state: AppState): AppState {
  return {
    ...state,
    giftKpiPanels: state.giftKpiPanels.map((panel) => ({ ...panel, items: panel.items.map((item) => ({ ...item, received: 0 })) })),
    recentGifts: [],
    stats: {},
    log: [],
    giftReceipts: [],
    contributions: { viewers: [], updatedAt: Date.now() },
  };
}

export function loadState(): AppState {
  return cachedState ?? publishCachedState(defaultState());
}

export function saveState(state: AppState): Promise<void> {
  const nextState = publishCachedState(normalizeState(state));
  const snapshots = snapshotStateFields(nextState);
  persistQueue = persistQueue
    .catch(() => undefined)
    .then(() => persistStateToServer(snapshots));
  return persistQueue;
}

export function saveStateTransaction(state: AppState): Promise<AppState> {
  const candidate = normalizeState(state);
  const snapshots = snapshotStateFields(candidate);
  const startingRevision = cachedStateRevision;
  const transaction = persistQueue
    .catch(() => undefined)
    .then(async () => {
      if (cachedStateRevision !== startingRevision) {
        throw new Error('配置在保存期间发生变化，请重试');
      }
      await persistStateToServer(snapshots);
      if (cachedStateRevision !== startingRevision) {
        throw new Error('配置在保存期间发生变化，请重试');
      }
      publishCachedState(candidate);
      return candidate;
    });
  persistQueue = transaction.then(
    () => undefined,
    () => undefined,
  );
  return transaction;
}

/**
 * Serializes a server-owned mutation with local persistence and adopts only
 * the normalized state returned by that command.
 */
export function commitAuthoritativeStateMutation(
  mutation: () => Promise<AppState>,
): Promise<AppState> {
  const transaction = persistQueue
    .catch(() => undefined)
    .then(async () => {
      const authoritative = normalizeState(await mutation());
      persistedFieldSnapshots = snapshotStateFields(authoritative);
      forcePersistFields.clear();
      return publishCachedState(authoritative);
    });
  persistQueue = transaction.then(
    () => undefined,
    () => undefined,
  );
  return transaction;
}

export function resetState(): Promise<void> {
  publishCachedState(defaultState());
  configMigrationRequired = false;
  persistQueue = persistQueue
    .catch(() => undefined)
    .then(async () => {
      if (typeof fetch === 'function') {
        let response: Response;
        try {
          response = await fetch(CONFIG_ENDPOINT, { method: 'DELETE', keepalive: true });
        } catch {
          throw new Error('恢复默认失败，请重试或先导出运行日志。');
        }
        if (!response.ok) throw new Error('恢复默认失败，请重试或先导出运行日志。');
      }
      persistedFieldSnapshots = {};
      forcePersistFields.clear();
    });
  return persistQueue;
}

export function consumeConfigMigrationRequired(): boolean {
  const required = configMigrationRequired;
  configMigrationRequired = false;
  return required;
}

export async function hydrateStateFromServer(): Promise<void> {
  await refreshStateFromServer();
}

export async function refreshStateFromServer(
  acceptState: () => boolean = () => true,
  options: { throwOnError?: boolean } = {},
): Promise<AppState> {
  if (typeof fetch !== 'function') return loadState();
  await persistQueue.catch(() => undefined);
  try {
    const response = await fetch(CONFIG_ENDPOINT, { cache: 'no-store' });
    let nextState: AppState;
    let hasPersistedState = true;
    if (response.status === 204) {
      nextState = defaultState();
      hasPersistedState = false;
    } else {
      if (!response.ok) throw new Error(`配置读取失败：HTTP ${response.status}`);
      nextState = normalizeState(await response.json() as Partial<AppState>);
    }
    if (acceptState() || !cachedState) {
      publishCachedState(nextState);
      persistedFieldSnapshots = hasPersistedState ? snapshotStateFields(nextState) : {};
    }
  } catch (error) {
    // A transient backend read failure must not erase the last visible state.
    if (!cachedState) publishCachedState(defaultState());
    if (options.throwOnError) throw error;
  }
  return cachedState ?? publishCachedState(defaultState());
}

function publishCachedState(state: AppState): AppState {
  cachedState = state;
  cachedStateRevision += 1;
  return state;
}

async function persistStateToServer(snapshots: StateFieldSnapshots): Promise<void> {
  if (typeof fetch !== 'function') return;
  const changedFields = STATE_FIELD_KEYS.filter((key) => (
    forcePersistFields.has(key) || persistedFieldSnapshots[key] !== snapshots[key]
  ));
  if (changedFields.length === 0) return;
  const patch: Record<string, unknown> = {};
  for (const key of changedFields) patch[key] = JSON.parse(snapshots[key]) as unknown;
  const serialized = JSON.stringify(patch);
  const keepalive = new TextEncoder().encode(serialized).byteLength <= 64 * 1024;
  const response = await fetch(CONFIG_ENDPOINT, {
    method: 'PATCH',
    headers: { 'Content-Type': 'application/json' },
    body: serialized,
    ...(keepalive ? { keepalive: true } : {}),
  });
  if (!response.ok) {
    let detail = '';
    try {
      const payload = await response.json() as { message?: unknown };
      if (typeof payload.message === 'string') detail = payload.message.trim();
    } catch {
      // Fall back to the status code when the response is not JSON.
    }
    throw new Error(detail ? `配置保存失败：${detail}` : `配置保存失败：HTTP ${response.status}`);
  }
  for (const key of changedFields) {
    persistedFieldSnapshots[key] = snapshots[key];
    forcePersistFields.delete(key);
  }
}

function snapshotStateFields(state: AppState): StateFieldSnapshots {
  const snapshots = {} as StateFieldSnapshots;
  for (const key of STATE_FIELD_KEYS) {
    snapshots[key] = JSON.stringify((key === 'giftKpiPanels'
      ? state.giftKpiPanels.map(giftTargetPanelConfig)
      : state[key]) ?? null);
  }
  return snapshots;
}

export function createConfigBackup(state: AppState): ConfigBackup {
  const referencedGiftIds = new Set<number>();
  for (const rule of state.rules) {
    referencedGiftIds.add(rule.giftId);
    for (const giftId of rule.matchGiftIds ?? []) referencedGiftIds.add(giftId);
  }
  for (const panel of state.giftKpiPanels) for (const item of panel.items) referencedGiftIds.add(item.giftId);
  for (const giftIds of Object.values(state.simplePlay?.gifts ?? {})) {
    for (const giftId of giftIds) referencedGiftIds.add(giftId);
  }
  for (const action of state.simplePlay?.overtimeGiftActions ?? []) referencedGiftIds.add(action.giftId);
  const catalogById = new Map<number, AppState['giftCatalog'][number]>();
  for (const gift of [...state.giftCatalog, ...state.recentGifts]) {
    if (referencedGiftIds.has(gift.id)) catalogById.set(gift.id, gift);
  }
  return {
    schemaVersion: CONFIG_BACKUP_SCHEMA_VERSION,
    roomId: state.roomId,
    attributes: state.attributes,
    displayScenes: state.displayScenes,
    blindBoxDisplay: state.blindBoxDisplay,
    giftKpiPanels: state.giftKpiPanels.map(giftTargetPanelConfig),
    activities: state.activities,
    rules: state.rules,
    timerRules: state.timerRules,
    formulaPresets: state.formulaPresets,
    settings: state.settings,
    simplePlay: state.simplePlay,
    giftCatalog: Array.from(catalogById.values()),
  };
}

export function mergeConfigBackup(current: AppState, input: unknown): AppState {
  if (!isObjectRecord(input)) throw new Error('配置文件格式不正确');
  const schemaVersion = input.schemaVersion === undefined ? 0 : Number(input.schemaVersion);
  if (!Number.isInteger(schemaVersion) || schemaVersion < 0) throw new Error('配置文件版本无效');
  if (schemaVersion > CONFIG_BACKUP_SCHEMA_VERSION) {
    throw new Error('配置来自更新版本，请先更新程序再导入');
  }
  for (const key of ['attributes', 'displayScenes', 'giftKpiPanels', 'activities', 'rules', 'timerRules', 'formulaPresets', 'giftCatalog'] as const) {
    if (input[key] !== undefined && !Array.isArray(input[key])) throw new Error(`配置字段 ${key} 格式不正确`);
  }
  if (input.settings !== undefined && !isObjectRecord(input.settings)) throw new Error('配置字段 settings 格式不正确');
  if (input.simplePlay !== undefined && !isObjectRecord(input.simplePlay)) throw new Error('配置字段 simplePlay 格式不正确');
  if (input.blindBoxDisplay !== undefined && !isObjectRecord(input.blindBoxDisplay)) throw new Error('配置字段 blindBoxDisplay 格式不正确');
  if (input.roomId !== undefined && typeof input.roomId !== 'string') throw new Error('配置字段 roomId 格式不正确');

  const parsed = input as Partial<AppState>;
  const isLegacyBackup = schemaVersion < CONFIG_BACKUP_SCHEMA_VERSION;
  const importedCatalog = Array.isArray(parsed.giftCatalog)
    ? parsed.giftCatalog.filter((gift) => (
      isObjectRecord(gift)
      && Number.isFinite(Number(gift.id))
      && typeof gift.name === 'string'
      && Number.isFinite(Number(gift.price))
    ))
    : [];
  const catalogById = new Map(current.giftCatalog.map((gift) => [gift.id, gift]));
  for (const gift of importedCatalog) catalogById.set(Number(gift.id), gift);

  return normalizeState({
    ...current,
    ...(parsed.roomId !== undefined ? { roomId: parsed.roomId } : {}),
    ...(parsed.attributes !== undefined ? { attributes: parsed.attributes } : {}),
    ...(parsed.displayScenes !== undefined ? { displayScenes: parsed.displayScenes } : {}),
    ...(parsed.blindBoxDisplay !== undefined ? { blindBoxDisplay: parsed.blindBoxDisplay } : {}),
    ...(parsed.giftKpiPanels !== undefined ? {
      giftKpiPanels: mergeGiftTargetPanelConfigs(current.giftKpiPanels, parsed.giftKpiPanels),
    } : {}),
    ...(parsed.activities !== undefined ? { activities: parsed.activities } : {}),
    ...(parsed.rules !== undefined ? { rules: parsed.rules } : {}),
    ...(parsed.timerRules !== undefined ? { timerRules: parsed.timerRules } : {}),
    ...(parsed.formulaPresets !== undefined ? { formulaPresets: parsed.formulaPresets } : {}),
    settings: {
      ...current.settings,
      ...(parsed.settings ?? {}),
      ...(isLegacyBackup && parsed.settings?.configExperience === undefined
        ? { configExperience: 'advanced' as const }
        : {}),
    },
    ...(parsed.simplePlay !== undefined
      ? { simplePlay: parsed.simplePlay }
      : isLegacyBackup || parsed.settings?.configExperience === 'advanced'
        ? { simplePlay: undefined }
        : {}),
    giftCatalog: Array.from(catalogById.values()),
    // Runtime/history data intentionally stays local and is never imported.
    recentGifts: current.recentGifts,
    stats: current.stats,
    log: current.log,
    giftReceipts: current.giftReceipts,
    contributions: current.contributions,
  });
}

function isObjectRecord(value: unknown): value is Record<string, unknown> {
  return value !== null && typeof value === 'object' && !Array.isArray(value);
}

function normalizeState(parsed: Partial<AppState>): AppState {
  const base = defaultState();
  const setupComplete = Boolean(parsed.roomId?.trim())
    && (parsed.attributes?.length ?? 0) > 0
    && (parsed.rules?.length ?? 0) > 0;
  if (parsed.settings?.showTutorial === undefined) markSettingsMigrationRequired();
  if (parsed.settings?.tutorialVersion === undefined || !Array.isArray(parsed.settings?.tutorialCompletedLessons)) markSettingsMigrationRequired();
  if (parsed.settings?.tutorialReplayMode === undefined) markSettingsMigrationRequired();
  if (!Array.isArray(parsed.settings?.trainingCompletedTopics)) markSettingsMigrationRequired();
  if (parsed.settings?.autoUpdate === undefined) markSettingsMigrationRequired();
  if (parsed.settings?.defaultDisplayThemeId === undefined) markSettingsMigrationRequired();
  if (parsed.settings?.configExperience === undefined) markSettingsMigrationRequired();
  const showTutorial = parsed.settings?.showTutorial ?? !setupComplete;
  const tutorialCompletedLessons = Array.isArray(parsed.settings?.tutorialCompletedLessons)
    ? parsed.settings.tutorialCompletedLessons.filter((lesson): lesson is AppState['settings']['tutorialCompletedLessons'][number] => (
      ['room', 'attribute', 'template', 'basics', 'gift', 'rule', 'preset', 'timer', 'appearance', 'save', 'enable', 'output'].includes(String(lesson))
    ))
    : [];
  const tutorialReplayMode = parsed.settings?.tutorialReplayMode === undefined
    ? showTutorial && setupComplete && tutorialCompletedLessons.length === 0
    : parsed.settings.tutorialReplayMode === true;
  const legacyPlacementSettingsKey = ['giftClip', 'Placements'].join('');
  const parsedSettings = { ...(parsed.settings ?? {}) } as Partial<AppState['settings']> & Record<string, unknown>;
  delete parsedSettings[legacyPlacementSettingsKey];
  const settings: AppState['settings'] = {
    ...base.settings,
    ...parsedSettings,
    showTutorial,
    tutorialVersion: 3,
    tutorialCompletedLessons: Array.from(new Set(tutorialCompletedLessons)),
    tutorialReplayMode,
    trainingCompletedTopics: normalizeTrainingTopicIds(parsed.settings?.trainingCompletedTopics),
    configExperience: parsed.settings?.configExperience === 'simple' ? 'simple' : 'advanced',
    giftClipCrops: normalizeGiftClipCrops(parsedSettings.giftClipCrops),
  };
  settings.panelOpacity = Math.min(100, Math.max(10, Number(settings.panelOpacity) || base.settings.panelOpacity));
  settings.defaultDisplayThemeId = normalizeDisplayThemeId(settings.defaultDisplayThemeId);
  if (parsed.blindBoxDisplay === undefined || parsed.blindBoxDisplay.viewerSlots === undefined) {
    markDisplayAppearanceMigrationRequired('blindBoxDisplay');
  }
  const blindBoxDisplay = normalizeBlindBoxDisplayAppearance(parsed.blindBoxDisplay, settings);
  const giftKpiPanels = normalizeGiftKpiPanels(parsed.giftKpiPanels, settings);
  const attributes = (parsed.attributes ?? base.attributes).map((attribute) => (
    attribute.display
      ? {
        ...attribute,
        display: {
          ...attribute.display,
          themeId: normalizeDisplayThemeId(attribute.display.themeId ?? settings.defaultDisplayThemeId),
          appearance: normalizeDisplayAppearance(attribute.display.appearance, settings, attribute.display.themeId),
          valueMappings: normalizeValueMappings(attribute.display.valueMappings),
        },
      }
      : attribute
  ));
  if (attributes.some((attribute, index) => attribute.display && !(parsed.attributes?.[index]?.display?.appearance))) {
    markDisplayAppearanceMigrationRequired('attributes');
  }
  const displayScenes = normalizeDisplayScenes(parsed.displayScenes, attributes, settings.defaultDisplayThemeId);
  for (const scene of displayScenes) {
    const source = parsed.displayScenes?.find((candidate) => candidate.id === scene.id);
    scene.appearance = normalizeDisplayAppearance(source?.appearance, settings, scene.themeId);
    if (!source?.appearance) markDisplayAppearanceMigrationRequired('displayScenes');
  }
  return {
    ...base,
    ...parsed,
    settings,
    attributes,
    displayScenes,
    blindBoxDisplay,
    giftKpiPanels,
    activities: normalizeActivities(parsed.activities, attributes, new Set(displayScenes.map((scene) => scene.id))),
    rules: parsed.rules ?? base.rules,
    timerRules: parsed.timerRules ?? base.timerRules,
    formulaPresets: parsed.formulaPresets ?? base.formulaPresets,
    simplePlay: normalizeSimplePlay(parsed.simplePlay, new Set(attributes.flatMap((attribute) => attribute.id ? [attribute.id] : []))),
    giftReceipts: (Array.isArray(parsed.giftReceipts) ? parsed.giftReceipts : []).slice(0, MAX_GIFT_RECEIPTS),
    contributions: normalizeContributionLedger(parsed.contributions),
  };
}

function normalizeGiftClipCrops(value: unknown): AppState['settings']['giftClipCrops'] {
  if (!isObjectRecord(value)) return {};
  const crops: AppState['settings']['giftClipCrops'] = {};
  for (const [rawKey, raw] of Object.entries(value)) {
    const key = rawKey.trim();
    if (!key || Array.from(key).length > 160 || isReservedGiftClipCropKey(key)) continue;
    crops[key] = normalizeGiftClipCrop(raw);
    if (Object.keys(crops).length === 200) break;
  }
  return crops;
}

function isReservedGiftClipCropKey(key: string): boolean {
  return key === '__proto__' || key === 'constructor' || key === 'prototype';
}

function normalizeSimplePlay(input: AppState['simplePlay'] | undefined, attributeIds: ReadonlySet<string>): SimplePlay | undefined {
  if (!isObjectRecord(input)
    || input.version !== 1
    || !['overtime', 'counter', 'goal'].includes(String(input.templateId))
    || !Number.isInteger(input.templateVersion)
    || Number(input.templateVersion) < 1
    || typeof input.attributeId !== 'string'
    || !input.attributeId.trim()
    || !attributeIds.has(input.attributeId.trim())
    || !isObjectRecord(input.parameters)
    || !isObjectRecord(input.gifts)
    || typeof input.managedFingerprint !== 'string'
    || !input.managedFingerprint.trim()) return undefined;

  const parameters: SimplePlay['parameters'] = {};
  for (const [key, value] of Object.entries(input.parameters)) {
    if (!key || !['string', 'number', 'boolean'].includes(typeof value)) continue;
    if (typeof value === 'number' && !Number.isFinite(value)) continue;
    parameters[key] = value as string | number | boolean;
  }
  const gifts: SimplePlay['gifts'] = {};
  for (const [slotId, giftIds] of Object.entries(input.gifts)) {
    if (!slotId || !Array.isArray(giftIds)) continue;
    gifts[slotId] = Array.from(new Set(giftIds
      .map(Number)
      .filter((giftId) => Number.isInteger(giftId) && giftId > 0)));
  }
  const overtimeGiftActions: SimplePlay['overtimeGiftActions'] = Array.isArray(input.overtimeGiftActions)
    ? input.overtimeGiftActions.flatMap((candidate): OvertimeGiftAction[] => {
      if (!isObjectRecord(candidate)) return [];
      const giftId = Number(candidate.giftId);
      const operation = String(candidate.operation).trim().toLowerCase();
      if (!Number.isInteger(giftId)
        || giftId <= 0
        || !['add', 'subtract', 'double', 'halve', 'reset'].includes(operation)) return [];
      if (operation !== 'add' && operation !== 'subtract') {
        return [{ giftId, operation: operation as OvertimeGiftAction['operation'] }];
      }
      const seconds = Number(candidate.seconds);
      if (!Number.isInteger(seconds) || seconds <= 0) return [];
      return [{ giftId, operation, seconds } as OvertimeGiftAction];
    })
    : undefined;

  return {
    version: 1,
    templateId: input.templateId as SimplePlay['templateId'],
    templateVersion: Number(input.templateVersion),
    attributeId: input.attributeId.trim(),
    parameters,
    gifts,
    ...(overtimeGiftActions === undefined ? {} : { overtimeGiftActions }),
    managedFingerprint: input.managedFingerprint.trim(),
  };
}

function normalizeGiftKpiPanels(input: AppState['giftKpiPanels'] | undefined, settings: AppState['settings']): AppState['giftKpiPanels'] {
  const ids = new Set<string>();
  return (Array.isArray(input) ? input : []).flatMap((candidate) => {
    const id = String(candidate?.id ?? '').trim();
    const name = String(candidate?.name ?? '').trim();
    if (!id || !name || ids.has(id)) return [];
    ids.add(id);
    const giftIds = new Set<number>();
    const items = (Array.isArray(candidate.items) ? candidate.items : []).flatMap((item) => {
      const giftId = Math.round(Number(item?.giftId));
      if (giftId <= 0 || giftIds.has(giftId)) return [];
      giftIds.add(giftId);
      return [{
        giftId,
        giftName: String(item.giftName ?? `礼物 ${giftId}`).trim() || `礼物 ${giftId}`,
        imageUrl: String(item.imageUrl ?? '').trim(),
        target: Math.max(1, Math.round(Number(item.target) || 1)),
        received: Math.max(0, Math.round(Number(item.received) || 0)),
        barStyle: ['resource', 'health'].includes(item.barStyle) ? item.barStyle : 'progress',
      }];
    }).slice(0, 12);
    if (items.length === 0) return [];
    return [{
      id,
      name,
      layout: normalizeGiftTargetLayout(candidate.layout),
      items,
      appearance: normalizeDisplayAppearance(candidate.appearance, settings),
    }];
  });
}

function markDisplayAppearanceMigrationRequired(field: StateFieldKey): void {
  configMigrationRequired = true;
  forcePersistFields.add(field);
}

function markSettingsMigrationRequired(): void {
  configMigrationRequired = true;
  forcePersistFields.add('settings');
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
    const blindBoxes = normalizeBlindBoxContributions(raw.blindBoxes);
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
      ...(blindBoxes.length > 0 ? { blindBoxes } : {}),
      lastGiftAt: nonNegativeNumber(raw.lastGiftAt),
    });
    if (viewers.length >= 2000) break;
  }
  viewers.sort((left, right) => right.lastGiftAt - left.lastGiftAt);
  const updatedAt = nonNegativeNumber(ledger?.updatedAt);
  return { viewers, ...(updatedAt > 0 ? { updatedAt } : {}) };
}

function normalizeBlindBoxContributions(
  input: AppState['contributions']['viewers'][number]['blindBoxes'],
): NonNullable<AppState['contributions']['viewers'][number]['blindBoxes']> {
  const byGiftId = new Map<number, NonNullable<AppState['contributions']['viewers'][number]['blindBoxes']>[number]>();
  for (const raw of Array.isArray(input) ? input : []) {
    const giftId = nonNegativeInteger(raw?.giftId);
    if (giftId <= 0) continue;
    const giftName = String(raw?.giftName ?? '').trim().slice(0, 80) || `盲盒 ${giftId}`;
    const count = nonNegativeInteger(raw?.count);
    const cost = nonNegativeNumber(raw?.cost);
    const value = nonNegativeNumber(raw?.value);
    const unpricedCount = nonNegativeInteger(raw?.unpricedCount);
    const lastGiftAt = nonNegativeNumber(raw?.lastGiftAt);
    const current = byGiftId.get(giftId);
    if (current) {
      current.count += count;
      current.cost += cost;
      current.value += value;
      current.profit = current.value - current.cost;
      current.unpricedCount = nonNegativeInteger(current.unpricedCount) + unpricedCount;
      if (lastGiftAt >= current.lastGiftAt) {
        current.giftName = giftName;
        current.lastGiftAt = lastGiftAt;
      }
      continue;
    }
    byGiftId.set(giftId, {
      giftId,
      giftName,
      count,
      cost,
      value,
      profit: value - cost,
      ...(unpricedCount > 0 ? { unpricedCount } : {}),
      lastGiftAt,
    });
    if (byGiftId.size >= 200) break;
  }
  return Array.from(byGiftId.values());
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
