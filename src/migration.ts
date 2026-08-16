import type {
  ActivitySession,
  AppState,
  Attribute,
  AttributeDisplay,
  DisplayAppearance,
  GiftInfo,
  GiftKpiPanel,
  Settings,
  SimplePlay,
} from './types';
import {
  getGameplayTemplate,
  validateGameplayTemplateInput,
  type GameplayTemplateDefinition,
  type TemplateGiftSlotDefinition,
  type TemplateParameterDefinition,
  type TemplateParameterValue,
} from './gameplay-templates';

const CONFIG_SCHEMA_VERSION = 5;

type Appearance = Pick<DisplayAppearance, 'themeId' | 'fontSize' | 'accentColor' | 'showConnection' | 'align' | 'panelOpacity'>;

export interface OnlineMigrationV1 {
  kind: 'gift-panel-online-migration';
  migrationVersion: 1;
  source: { appVersion: string; configSchemaVersion: number };
  exportedAt: string;
  payload: {
    roomSuggestion: string | null;
    definition: OnlineMigrationDefinition;
    runtime: OnlineMigrationRuntime;
  };
}

export interface OnlineMigrationDownloadAdapter {
  createBlob: (content: string) => Blob;
  createObjectURL: (blob: Blob) => string;
  click: (url: string, filename: string) => void;
  revokeObjectURL: (url: string) => void;
}

export interface OnlineMigrationDefinition {
  appearance: Pick<Settings, 'theme' | 'fontSize' | 'accentColor' | 'align' | 'panelOpacity' | 'showConnection'>;
  attributes: Array<{
    id: string;
    name: string;
    unit: Attribute['unit'];
    format: Attribute['format'];
    decimals: number;
    suffix: string;
    color?: string;
    broadcastMessage?: string;
    display?: OnlineAttributeDisplay;
  }>;
  displayScenes: Array<{
    id: string;
    name: string;
    attributeIds: string[];
    layout: AppState['displayScenes'][number]['layout'];
    themeId: AppState['displayScenes'][number]['themeId'];
    appearance?: Appearance;
  }>;
  blindBoxDisplay: Appearance;
  giftTargetPanels: Array<{
    id: string;
    name: string;
    layout: GiftKpiPanel['layout'];
    items: Array<{ giftId: number; name: string; target: number; barStyle: GiftKpiPanel['items'][number]['barStyle'] }>;
    appearance: Appearance;
  }>;
  activities: Array<{
    id: string;
    name: string;
    attributeIds: string[];
    sceneId?: string;
    resultMode: ActivitySession['resultMode'];
    gateRules: boolean;
    initialValues: Record<string, number>;
    milestones: Array<{
      id: string;
      name: string;
      attributeId: string;
      comparison: ActivitySession['milestones'][number]['comparison'];
      threshold: number;
      action: ActivitySession['milestones'][number]['action'];
      message: string;
    }>;
    giftTimeout?: { seconds: number; action: NonNullable<ActivitySession['giftTimeout']>['action'] };
  }>;
  rules: Array<{
    id: string;
    giftId: number;
    attributeId: string;
    formulaName?: string;
    condition?: string;
    formula: string;
    enabled?: boolean;
    matchGiftIds?: number[];
    minPrice?: number;
    cap?: number;
    dailyLimit?: number;
  }>;
  timerRules: Array<{
    id: string;
    attributeId: string;
    formulaName: string;
    intervalSeconds: number;
    condition?: string;
    formula: string;
    enabled: boolean;
  }>;
  formulaPresets: Array<{ id: string; name: string; context: AppState['formulaPresets'][number]['context']; formula: string; attributeId: string }>;
  simplePlay?: OnlineSimplePlay;
  gifts: Array<{
    id: number;
    name: string;
    price: number;
    coinType: GiftInfo['coinType'];
    blindBoxParentId?: number;
    blindBoxParentName?: string;
    blindBoxParentPrice?: number;
  }>;
}

export interface OnlineMigrationRuntime {
  attributeValues: Record<string, number>;
  giftTargetReceived: Array<{ panelId: string; giftId: number; received: number }>;
  activities: Array<{
    id: string;
    status: ActivitySession['status'];
    startedAtMillis?: number;
    lockedAtMillis?: number;
    settledAtMillis?: number;
    result?: { winnerAttributeId?: string; values: Record<string, number> };
    milestones: Array<{ id: string; triggeredAtMillis?: number; triggerValue?: number }>;
    giftTimeout?: { lastGiftAtMillis?: number; deadlineAtMillis?: number };
  }>;
  ruleLimits: { localDate: string; appliedCounts: Record<string, number> };
}

interface OnlineAttributeDisplay {
  variant: NonNullable<Attribute['display']>['variant'];
  themeId?: NonNullable<Attribute['display']>['themeId'];
  appearance?: Appearance;
  title?: string;
  min?: number;
  max?: number;
  lowThreshold?: number;
  leftLabel?: string;
  rightLabel?: string;
  valueMappings?: Array<{ value: number; label: string; color?: string }>;
}

interface OnlineSimplePlay {
  version: 1;
  templateId: SimplePlay['templateId'];
  templateVersion: number;
  attributeId: string;
  parameters: Record<string, string | number | boolean>;
  gifts: Record<string, number[]>;
  overtimeGiftActions?: Array<{ giftId: number; operation: NonNullable<SimplePlay['overtimeGiftActions']>[number]['operation']; seconds?: number }>;
  managedFingerprint: string;
}

const LEGACY_OVERTIME_V1_PARAMETERS = [
  { id: 'name', kind: 'text', defaultValue: '加班时间' },
  { id: 'minutesPerYuan', kind: 'number', min: 1, max: 3600, defaultValue: 60 },
  { id: 'maxHours', kind: 'number', min: 0, max: 240, defaultValue: 0 },
  { id: 'broadcastMessage', kind: 'text', defaultValue: '感谢大家的支持，欢迎投喂礼物' },
] as const;

const OVERTIME_OPERATIONS = new Set<NonNullable<SimplePlay['overtimeGiftActions']>[number]['operation']>([
  'add', 'subtract', 'double', 'halve', 'reset',
]);

export function onlineMigrationFilename(exportedAt: Date): string {
  return `gift-panel-migration-v1-${exportedAt.toISOString().slice(0, 10)}.json`;
}

export function downloadOnlineMigration(
  state: AppState,
  appVersion: string,
  exportedAt: Date,
  adapter: OnlineMigrationDownloadAdapter,
): void {
  const content = JSON.stringify(createOnlineMigration(state, appVersion, exportedAt), null, 2);
  let url: string | undefined;
  try {
    url = adapter.createObjectURL(adapter.createBlob(content));
    adapter.click(url, onlineMigrationFilename(exportedAt));
  } finally {
    if (url !== undefined) adapter.revokeObjectURL(url);
  }
}

/**
 * Creates a safe, standalone migration envelope. This is deliberately an
 * allowlist projection rather than a local backup: nothing in the result is
 * a reference to the mutable desktop state.
 */
export function createOnlineMigration(state: AppState, appVersion: string, exportedAt: Date): OnlineMigrationV1 {
  const attributeIDs = new Map<string, string>();
  const attributes = state.attributes.map((attribute, index) => {
    const id = attributeID(attribute, index);
    if (!attributeIDs.has(attribute.name)) attributeIDs.set(attribute.name, id);
    return exportAttribute(attribute, id);
  });
  const idForName = (name: string): string => attributeIDs.get(name) ?? name;
  const simplePlay = state.simplePlay ? exportSimplePlay(state.simplePlay) : undefined;
  const referencedGiftIDs = collectReferencedGiftIDs(state, simplePlay);

  return {
    kind: 'gift-panel-online-migration',
    migrationVersion: 1,
    source: { appVersion, configSchemaVersion: CONFIG_SCHEMA_VERSION },
    exportedAt: exportedAt.toISOString(),
    payload: {
      roomSuggestion: state.roomId.trim() || null,
      definition: {
        appearance: exportSettingsAppearance(state.settings),
        attributes,
        displayScenes: state.displayScenes.map((scene) => ({
          id: scene.id,
          name: scene.name,
          attributeIds: scene.attributeNames.map(idForName),
          layout: scene.layout,
          themeId: scene.themeId,
          ...(scene.appearance ? { appearance: exportAppearance(scene.appearance) } : {}),
        })),
        blindBoxDisplay: exportAppearance(state.blindBoxDisplay),
        giftTargetPanels: state.giftKpiPanels.map((panel) => exportGiftTargetPanel(panel)),
        activities: state.activities.map((activity) => exportActivityDefinition(activity, idForName)),
        rules: state.rules.map((rule) => ({
          id: rule.id,
          giftId: rule.giftId,
          attributeId: idForName(rule.attributeName),
          ...(rule.formulaName === undefined ? {} : { formulaName: rule.formulaName }),
          ...(rule.condition === undefined ? {} : { condition: rule.condition }),
          formula: rule.formula,
          ...(rule.enabled === undefined ? {} : { enabled: rule.enabled }),
          ...(rule.matchGiftIds === undefined ? {} : { matchGiftIds: [...rule.matchGiftIds] }),
          ...(rule.minPrice === undefined ? {} : { minPrice: rule.minPrice }),
          ...(rule.cap === undefined ? {} : { cap: rule.cap }),
          ...(rule.dailyLimit === undefined ? {} : { dailyLimit: rule.dailyLimit }),
        })),
        timerRules: state.timerRules.map((rule) => ({
          id: rule.id,
          attributeId: idForName(rule.attributeName),
          formulaName: rule.formulaName,
          intervalSeconds: rule.intervalSeconds,
          ...(rule.condition === undefined ? {} : { condition: rule.condition }),
          formula: rule.formula,
          enabled: rule.enabled,
        })),
        formulaPresets: state.formulaPresets.map((preset) => ({
          id: preset.id,
          name: preset.name,
          context: preset.context,
          formula: preset.formula,
          attributeId: idForName(preset.sourceAttributeName),
        })),
        ...(simplePlay ? { simplePlay } : {}),
        gifts: state.giftCatalog
          .filter((gift) => referencedGiftIDs.has(gift.id))
          .map(exportGift)
          .sort((left, right) => left.id - right.id),
      },
      runtime: {
        attributeValues: Object.fromEntries(state.attributes.map((attribute, index) => [attributeID(attribute, index), attribute.value])),
        giftTargetReceived: state.giftKpiPanels.flatMap((panel) => panel.items.map((item) => ({ panelId: panel.id, giftId: item.giftId, received: item.received }))),
        activities: state.activities.map((activity) => exportActivityRuntime(activity, idForName)),
        ruleLimits: exportRuleLimits(state, exportedAt),
      },
    },
  };
}

function attributeID(attribute: Attribute, index: number): string {
  return attribute.id?.trim() || `legacy-attribute-${index + 1}`;
}

function exportSettingsAppearance(settings: Settings): OnlineMigrationDefinition['appearance'] {
  return {
    theme: settings.theme,
    fontSize: settings.fontSize,
    accentColor: settings.accentColor,
    align: settings.align,
    panelOpacity: settings.panelOpacity,
    showConnection: settings.showConnection,
  };
}

function exportAppearance(appearance: DisplayAppearance): Appearance {
  return {
    themeId: appearance.themeId,
    fontSize: appearance.fontSize,
    accentColor: appearance.accentColor,
    showConnection: appearance.showConnection,
    align: appearance.align,
    panelOpacity: appearance.panelOpacity,
  };
}

function exportAttribute(attribute: Attribute, id: string): OnlineMigrationDefinition['attributes'][number] {
  return {
    id,
    name: attribute.name,
    unit: attribute.unit,
    format: attribute.format,
    decimals: attribute.decimals,
    suffix: attribute.suffix,
    ...(attribute.color === undefined ? {} : { color: attribute.color }),
    ...(attribute.broadcastMessage === undefined ? {} : { broadcastMessage: attribute.broadcastMessage }),
    ...(attribute.display ? { display: exportAttributeDisplay(attribute.display) } : {}),
  };
}

function exportAttributeDisplay(display: AttributeDisplay): OnlineAttributeDisplay {
  return {
    variant: display.variant,
    ...(display.themeId === undefined ? {} : { themeId: display.themeId }),
    ...(display.appearance === undefined ? {} : { appearance: exportAppearance(display.appearance) }),
    ...(display.title === undefined ? {} : { title: display.title }),
    ...(display.min === undefined ? {} : { min: display.min }),
    ...(display.max === undefined ? {} : { max: display.max }),
    ...(display.lowThreshold === undefined ? {} : { lowThreshold: display.lowThreshold }),
    ...(display.leftLabel === undefined ? {} : { leftLabel: display.leftLabel }),
    ...(display.rightLabel === undefined ? {} : { rightLabel: display.rightLabel }),
    ...(display.valueMappings === undefined ? {} : {
      valueMappings: display.valueMappings.map((mapping) => ({
        value: mapping.value,
        label: mapping.label,
        ...(mapping.color === undefined ? {} : { color: mapping.color }),
      })),
    }),
  };
}

function exportGiftTargetPanel(panel: GiftKpiPanel): OnlineMigrationDefinition['giftTargetPanels'][number] {
  return {
    id: panel.id,
    name: panel.name,
    layout: panel.layout,
    items: panel.items.map((item) => ({ giftId: item.giftId, name: item.giftName, target: item.target, barStyle: item.barStyle })),
    appearance: exportAppearance(panel.appearance),
  };
}

function exportActivityDefinition(activity: ActivitySession, idForName: (name: string) => string): OnlineMigrationDefinition['activities'][number] {
  return {
    id: activity.id,
    name: activity.name,
    attributeIds: activity.attributeNames.map(idForName),
    ...(activity.sceneId === undefined ? {} : { sceneId: activity.sceneId }),
    resultMode: activity.resultMode,
    gateRules: activity.gateRules,
    initialValues: remapNumberRecord(activity.initialValues, idForName),
    milestones: activity.milestones.map((milestone) => ({
      id: milestone.id,
      name: milestone.name,
      attributeId: idForName(milestone.attributeName),
      comparison: milestone.comparison,
      threshold: milestone.threshold,
      action: milestone.action,
      message: milestone.message,
    })),
    ...(activity.giftTimeout ? { giftTimeout: { seconds: activity.giftTimeout.seconds, action: activity.giftTimeout.action } } : {}),
  };
}

function exportActivityRuntime(activity: ActivitySession, idForName: (name: string) => string): OnlineMigrationRuntime['activities'][number] {
  return {
    id: activity.id,
    status: activity.status,
    ...(activity.startedAt === undefined ? {} : { startedAtMillis: activity.startedAt }),
    ...(activity.lockedAt === undefined ? {} : { lockedAtMillis: activity.lockedAt }),
    ...(activity.settledAt === undefined ? {} : { settledAtMillis: activity.settledAt }),
    ...(activity.result ? {
      result: {
        ...(activity.result.winnerAttributeName === undefined ? {} : { winnerAttributeId: idForName(activity.result.winnerAttributeName) }),
        values: remapNumberRecord(activity.result.values, idForName),
      },
    } : {}),
    milestones: activity.milestones.map((milestone) => ({
      id: milestone.id,
      ...(milestone.triggeredAt === undefined ? {} : { triggeredAtMillis: milestone.triggeredAt }),
      ...(milestone.triggerValue === undefined ? {} : { triggerValue: milestone.triggerValue }),
    })),
    ...(activity.giftTimeout ? {
      giftTimeout: {
        ...(activity.giftTimeout.lastGiftAt === undefined ? {} : { lastGiftAtMillis: activity.giftTimeout.lastGiftAt }),
        ...(activity.giftTimeout.deadlineAt === undefined ? {} : { deadlineAtMillis: activity.giftTimeout.deadlineAt }),
      },
    } : {}),
  };
}

function remapNumberRecord(record: Record<string, number>, idForName: (name: string) => string): Record<string, number> {
  return Object.fromEntries(Object.entries(record)
    .sort(([left], [right]) => left.localeCompare(right))
    .map(([name, value]) => [idForName(name), value]));
}

function exportSimplePlay(simplePlay: SimplePlay): OnlineSimplePlay | undefined {
  const template = getGameplayTemplate(simplePlay.templateId);
  if (template?.version === simplePlay.templateVersion) return exportCurrentSimplePlay(simplePlay, template);
  if (simplePlay.templateId === 'overtime' && simplePlay.templateVersion === 1) return exportLegacyOvertimeV1(simplePlay);
  return undefined;
}

function exportCurrentSimplePlay(simplePlay: SimplePlay, template: GameplayTemplateDefinition): OnlineSimplePlay | undefined {
  const parameters = exportTemplateParameters(template.parameters, simplePlay.parameters);
  const gifts = exportTemplateGifts(template.giftSlots, simplePlay.gifts);
  if (Object.keys(parameters).length !== template.parameters.length) return undefined;
  if (simplePlay.templateId === 'overtime' && !Number.isInteger(parameters.maxSeconds)) return undefined;
  const overtimeGiftActions = template.id === 'overtime'
    ? exportOvertimeGiftActions(simplePlay.overtimeGiftActions, gifts.overtime ?? [])
    : undefined;
  const input = {
    parameters,
    gifts: Object.fromEntries(template.giftSlots.map((slot) => [
      slot.id,
      (gifts[slot.id] ?? []).map(templateGift),
    ])),
    ...(template.id === 'overtime' ? { overtimeGiftActions: overtimeGiftActions ?? [] } : {}),
  };
  if (validateGameplayTemplateInput(template, input).length > 0) return undefined;
  return {
    version: simplePlay.version,
    templateId: simplePlay.templateId,
    templateVersion: simplePlay.templateVersion,
    attributeId: simplePlay.attributeId,
    parameters,
    gifts,
    ...(template.id === 'overtime' && simplePlay.overtimeGiftActions !== undefined ? { overtimeGiftActions: overtimeGiftActions ?? [] } : {}),
    managedFingerprint: simplePlay.managedFingerprint,
  };
}

function exportLegacyOvertimeV1(simplePlay: SimplePlay): OnlineSimplePlay | undefined {
  const parameters = Object.fromEntries(LEGACY_OVERTIME_V1_PARAMETERS.flatMap((definition) => {
    const value = parameterValueOrDefault(definition, simplePlay.parameters);
    return isValidLegacyOvertimeV1Parameter(definition, value) ? [[definition.id, value]] : [];
  }));
  const gifts = exportTemplateGifts([{ id: 'overtime', label: '', description: '', minimum: 1, multiple: true }], simplePlay.gifts);
  if (Object.keys(parameters).length !== LEGACY_OVERTIME_V1_PARAMETERS.length || gifts.overtime?.length === 0) return undefined;
  return {
    version: simplePlay.version,
    templateId: 'overtime',
    templateVersion: 1,
    attributeId: simplePlay.attributeId,
    parameters,
    gifts,
    managedFingerprint: simplePlay.managedFingerprint,
  };
}

function exportTemplateParameters(
  definitions: readonly TemplateParameterDefinition[],
  source: SimplePlay['parameters'],
): Record<string, TemplateParameterValue> {
  return Object.fromEntries(definitions.flatMap((definition) => {
    const value = parameterValueOrDefault(definition, source);
    return isValidSimplePlayParameter(definition, value) ? [[definition.id, value]] : [];
  }));
}

function parameterValueOrDefault(
  definition: Pick<TemplateParameterDefinition, 'id' | 'defaultValue'>,
  source: SimplePlay['parameters'],
): unknown {
  return Object.prototype.hasOwnProperty.call(source, definition.id)
    ? source[definition.id]
    : definition.defaultValue;
}

function exportTemplateGifts(
  slots: readonly TemplateGiftSlotDefinition[],
  source: SimplePlay['gifts'],
): Record<string, number[]> {
  const used = new Set<number>();
  return Object.fromEntries(slots.map((slot) => {
    const giftIDs: number[] = [];
    for (const giftID of source[slot.id] ?? []) {
      if (!Number.isInteger(giftID) || giftID <= 0 || used.has(giftID)) continue;
      used.add(giftID);
      giftIDs.push(giftID);
      if (!slot.multiple) break;
    }
    return [slot.id, giftIDs];
  }));
}

function templateGift(id: number): GiftInfo {
  return { id, name: String(id), price: 0, coinType: 'gold', imgBasic: '' };
}

function exportOvertimeGiftActions(
  source: SimplePlay['overtimeGiftActions'],
  giftIDs: readonly number[],
): NonNullable<SimplePlay['overtimeGiftActions']> | undefined {
  if (source === undefined) return undefined;
  const configuredGiftIDs = new Set(giftIDs);
  const seen = new Set<number>();
  return source.flatMap((action) => {
    if (!configuredGiftIDs.has(action.giftId) || seen.has(action.giftId) || !OVERTIME_OPERATIONS.has(action.operation)) return [];
    if ((action.operation === 'add' || action.operation === 'subtract')
      && (!Number.isInteger(action.seconds) || Number(action.seconds) <= 0)) return [];
    seen.add(action.giftId);
    return [{
      giftId: action.giftId,
      operation: action.operation,
      ...(action.operation === 'add' || action.operation === 'subtract' ? { seconds: Number(action.seconds) } : {}),
    }];
  });
}

function isValidSimplePlayParameter(
  definition: TemplateParameterDefinition,
  value: unknown,
): value is TemplateParameterValue {
  switch (definition.kind) {
    case 'text':
      return typeof value === 'string' && value.trim().length > 0 && value.length <= 4096 && !containsResourceReference(value);
    case 'select':
      return typeof value === 'string' && definition.options?.some((option) => option.value === value) === true;
    case 'toggle':
      return typeof value === 'boolean';
    case 'number':
    case 'duration':
      return typeof value === 'number'
        && Number.isFinite(value)
        && (definition.min === undefined || value >= definition.min)
        && (definition.max === undefined || value <= definition.max);
  }
}

function isValidLegacyOvertimeV1Parameter(
  definition: (typeof LEGACY_OVERTIME_V1_PARAMETERS)[number],
  value: unknown,
): value is TemplateParameterValue {
  if (definition.kind === 'text') return typeof value === 'string' && value.trim().length > 0 && value.length <= 4096 && !containsResourceReference(value);
  return typeof value === 'number'
    && Number.isFinite(value)
    && value >= definition.min
    && value <= definition.max;
}

function containsResourceReference(value: string): boolean {
  const schemes = value.matchAll(/[A-Za-z][A-Za-z0-9+.-]*:/g);
  for (const match of schemes) {
    const scheme = match[0].slice(0, -1);
    const remainder = value.slice((match.index ?? 0) + match[0].length);
    if (scheme !== 'PK' && scheme !== 'HP' && !/^\s/.test(remainder)) return true;
  }
  return /\/\//.test(value)
    || /\\\\/.test(value)
    || /(?:^|[\s:=[\]()"'<])(?:\/|\\|\.\.?[\\/])/.test(value)
    || /\.(?:apng|avif|bmp|gif|jpe?g|png|svg|webp|mp3|wav|ogg|m4a|mp4|m4v|mov|webm)(?:[?#\s]|$)/i.test(value);
}

function exportRuleLimits(state: AppState, exportedAt: Date): OnlineMigrationRuntime['ruleLimits'] {
  const localDate = localDateString(exportedAt);
  const day = Object.entries(state.stats)
    .filter(([, candidate]) => candidate.date === localDate)
    .sort(([left], [right]) => left.localeCompare(right))[0]?.[1];
  const currentRuleIDs = new Set(state.rules.map((rule) => rule.id));
  const appliedCounts = Object.fromEntries(Object.entries(day?.ruleTriggers ?? {})
    .filter(([ruleID, count]) => currentRuleIDs.has(ruleID) && Number.isInteger(count) && count >= 0)
    .sort(([left], [right]) => left.localeCompare(right)));
  return { localDate, appliedCounts };
}

function localDateString(date: Date): string {
  const pad = (value: number): string => String(value).padStart(2, '0');
  return `${date.getFullYear()}-${pad(date.getMonth() + 1)}-${pad(date.getDate())}`;
}

function collectReferencedGiftIDs(state: AppState, simplePlay: OnlineSimplePlay | undefined): Set<number> {
  const ids = new Set<number>();
  for (const rule of state.rules) {
    ids.add(rule.giftId);
    for (const matchedID of rule.matchGiftIds ?? []) ids.add(matchedID);
  }
  for (const panel of state.giftKpiPanels) for (const item of panel.items) ids.add(item.giftId);
  for (const giftIDs of Object.values(simplePlay?.gifts ?? {})) for (const giftID of giftIDs) ids.add(giftID);
  for (const action of simplePlay?.overtimeGiftActions ?? []) ids.add(action.giftId);
  return ids;
}

function exportGift(gift: GiftInfo): OnlineMigrationDefinition['gifts'][number] {
  return {
    id: gift.id,
    name: gift.name,
    price: gift.price,
    coinType: gift.coinType,
    ...(gift.blindBoxParentId === undefined ? {} : { blindBoxParentId: gift.blindBoxParentId }),
    ...(gift.blindBoxParentName === undefined ? {} : { blindBoxParentName: gift.blindBoxParentName }),
    ...(gift.blindBoxParentPrice === undefined ? {} : { blindBoxParentPrice: gift.blindBoxParentPrice }),
  };
}
