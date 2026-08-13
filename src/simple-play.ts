import {
  buildGameplayTemplate,
  createDefaultTemplateInput,
  getGameplayTemplate,
  type GameplayTemplateInput,
  type TemplateIdFactory,
} from './gameplay-templates';
import { collectVars } from './formula';
import type {
  ActivitySession,
  AppState,
  Attribute,
  DisplayScene,
  DisplayThemeId,
  FormulaPreset,
  GiftInfo,
  GiftRule,
  OvertimeGiftAction,
  SimplePlay,
  SimplePlayTemplateId,
  TimerRule,
} from './types';

export interface SimplePlayDraft {
  templateId: SimplePlayTemplateId;
  parameters: Record<string, string | number | boolean>;
  gifts: Record<string, number[]>;
  overtimeGiftActions?: OvertimeGiftAction[];
  displayThemeId?: DisplayThemeId;
}

export interface SimplePlayTransitionImpact {
  kind: 'create' | 'adjust' | 'replace';
  attributesAdded: number;
  attributesUpdated: number;
  attributesRemoved: number;
  rulesAdded: number;
  rulesRemoved: number;
  timerRulesAdded: number;
  timerRulesRemoved: number;
  displayScenesUpdated: number;
  displayScenesRemoved: number;
  activitiesUpdated: number;
  activitiesRemoved: number;
  formulaPresetsRemoved: number;
  total: number;
}

export interface SimplePlayTransitionPlan {
  nextState: AppState;
  summary: string[];
  impact: SimplePlayTransitionImpact;
}

interface ManagedCleanup {
  attributes: Attribute[];
  rules: GiftRule[];
  timerRules: TimerRule[];
  displayScenes: DisplayScene[];
  activities: ActivitySession[];
  formulaPresets: FormulaPreset[];
  counts: Omit<SimplePlayTransitionImpact,
    | 'kind'
    | 'attributesAdded'
    | 'attributesUpdated'
    | 'rulesAdded'
    | 'timerRulesAdded'
    | 'total'>;
}

const SIMPLE_TEMPLATE_IDS = new Set<SimplePlayTemplateId>(['overtime', 'counter', 'goal']);

export function getSimplePlayAttribute(state: AppState): Attribute | undefined {
  const attributeId = state.simplePlay?.attributeId;
  return attributeId ? state.attributes.find((attribute) => attribute.id === attributeId) : undefined;
}

export function simplePlayDraftFromState(state: AppState): SimplePlayDraft | undefined {
  const simplePlay = state.simplePlay;
  if (!simplePlay) return undefined;
  const attribute = getSimplePlayAttribute(state);
  const parameters = { ...simplePlay.parameters };
  if (attribute) parameters.name = attribute.name;
  return {
    templateId: simplePlay.templateId,
    parameters,
    gifts: Object.fromEntries(Object.entries(simplePlay.gifts).map(([slot, giftIds]) => [slot, [...giftIds]])),
    ...(simplePlay.overtimeGiftActions ? {
      overtimeGiftActions: simplePlay.overtimeGiftActions.map((action) => ({ ...action })),
    } : {}),
    ...(attribute?.display?.themeId ? { displayThemeId: attribute.display.themeId } : {}),
  };
}

export function calculateSimplePlayManagedFingerprint(state: AppState, attributeId: string): string {
  const attribute = state.attributes.find((candidate) => candidate.id === attributeId);
  if (!attribute) return '';
  const attributeName = attribute.name;
  const managed = {
    attribute: {
      ...attribute,
      value: undefined,
      ...(attribute.display ? {
        display: {
          ...attribute.display,
          appearance: undefined,
          valueMappings: attribute.display.valueMappings?.length ? attribute.display.valueMappings : undefined,
        },
      } : {}),
    },
    rules: state.rules
      .filter((rule) => referencesAttribute(attributeName, rule.attributeName, rule.formula, rule.condition))
      .map((rule) => ({ ...rule, enabled: undefined })),
    timerRules: state.timerRules
      .filter((rule) => referencesAttribute(attributeName, rule.attributeName, rule.formula, rule.condition))
      .map((rule) => ({ ...rule, enabled: undefined })),
    displayScenes: state.displayScenes.filter((scene) => scene.attributeNames.includes(attributeName)),
    activities: state.activities.filter((activity) => activityReferencesAttribute(activity, attributeName)),
    formulaPresets: state.formulaPresets.filter((preset) => referencesAttribute(
      attributeName,
      preset.sourceAttributeName,
      preset.formula,
    )),
  };
  const serialized = JSON.stringify(canonicalize(managed));
  let hash = 0x811c9dc5;
  for (let index = 0; index < serialized.length; index += 1) {
    hash ^= serialized.charCodeAt(index);
    hash = Math.imul(hash, 0x01000193) >>> 0;
  }
  return `simple-play-v1-${hash.toString(16).padStart(8, '0')}`;
}

export function isSimplePlayConfigurationIntact(state: AppState): boolean {
  const simplePlay = state.simplePlay;
  return Boolean(simplePlay)
    && calculateSimplePlayManagedFingerprint(state, simplePlay!.attributeId) === simplePlay!.managedFingerprint;
}

export function planSimplePlayTransition(state: AppState, draft: SimplePlayDraft): SimplePlayTransitionPlan {
  if (!SIMPLE_TEMPLATE_IDS.has(draft.templateId)) throw new Error('不支持的简单玩法模板');
  const template = getGameplayTemplate(draft.templateId);
  if (!template) throw new Error('找不到简单玩法模板');

  const previousPlay = state.simplePlay;
  const previousAttribute = getSimplePlayAttribute(state);
  const kind: SimplePlayTransitionImpact['kind'] = !previousPlay || !previousAttribute
    ? 'create'
    : previousPlay.templateId === draft.templateId ? 'adjust' : 'replace';
  const cleanup = previousAttribute && previousPlay
    ? kind === 'adjust'
      ? cleanSimpleArtifactsForAdjustment(state, previousAttribute, previousPlay)
      : cleanManagedReferences(state, previousAttribute)
    : emptyManagedCleanup(state);

  const normalizedDraft = normalizeDraft(draft);
  if (kind === 'adjust' && previousAttribute) {
    // Attribute display URLs are name based. Keep the existing name while the
    // same simple play is adjusted so an OBS browser source never goes stale.
    normalizedDraft.parameters.name = previousAttribute.name;
  }
  const buildInput = toTemplateInput(state, normalizedDraft);
  const ids = createPureIdFactory(cleanup, draft.templateId);
  const result = buildGameplayTemplate(template, buildInput, ids);
  if (result.attributes.length !== 1) throw new Error('简单玩法模板必须只生成一个属性');

  const builtAttribute = result.attributes[0];
  if (cleanup.attributes.some((attribute) => attribute.name === builtAttribute.name)) {
    throw new Error(`属性名称“${builtAttribute.name}”已存在`);
  }
  const attributeId = kind === 'adjust' && previousAttribute?.id
    ? previousAttribute.id
    : nextAvailableId(`attribute-simple-${draft.templateId}`, cleanup.attributes.flatMap((attribute) => attribute.id ? [attribute.id] : []));
  const maximum = builtAttribute.display?.max;
  const currentValue = kind === 'adjust' && previousAttribute
    ? previousAttribute.value
    : builtAttribute.value;
  const nextValue = Number.isFinite(maximum) ? Math.min(currentValue, maximum!) : currentValue;
  const nextAttribute: Attribute = {
    ...builtAttribute,
    id: attributeId,
    value: nextValue,
    ...(kind === 'adjust' && previousAttribute?.display?.appearance && builtAttribute.display
      ? { display: { ...builtAttribute.display, appearance: { ...previousAttribute.display.appearance } } }
      : {}),
  };
  const attributes = [...cleanup.attributes];
  const insertionIndex = previousAttribute
    ? Math.min(state.attributes.indexOf(previousAttribute), attributes.length)
    : attributes.length;
  attributes.splice(Math.max(0, insertionIndex), 0, nextAttribute);

  const wasEnabled = kind === 'adjust' && previousPlay && previousAttribute
    ? simpleArtifactsEnabled(state, previousAttribute, previousPlay)
    : true;
  const nextRules = result.rules.map((rule) => ({ ...rule, enabled: wasEnabled }));
  const nextTimerRules = result.timerRules.map((rule) => ({ ...rule, enabled: wasEnabled }));
  const provisional: AppState = {
    ...state,
    attributes,
    rules: [...cleanup.rules, ...nextRules],
    timerRules: [...cleanup.timerRules, ...nextTimerRules],
    displayScenes: [...cleanup.displayScenes, ...result.displayScenes],
    activities: [...cleanup.activities, ...result.activities],
    formulaPresets: cleanup.formulaPresets,
    settings: { ...state.settings, configExperience: 'simple' },
    simplePlay: undefined,
  };
  const simplePlay: SimplePlay = {
    version: 1,
    templateId: draft.templateId,
    templateVersion: template.version,
    attributeId,
    parameters: normalizedDraft.parameters,
    gifts: normalizedDraft.gifts,
    ...(normalizedDraft.overtimeGiftActions ? {
      overtimeGiftActions: normalizedDraft.overtimeGiftActions,
    } : {}),
    managedFingerprint: calculateSimplePlayManagedFingerprint(provisional, attributeId),
  };
  const nextState: AppState = { ...provisional, simplePlay };
  const partialImpact = {
    kind,
    attributesAdded: kind === 'adjust' ? 0 : 1,
    attributesUpdated: kind === 'adjust' ? 1 : 0,
    attributesRemoved: kind === 'replace' ? 1 : 0,
    rulesAdded: result.rules.length,
    rulesRemoved: cleanup.counts.rulesRemoved,
    timerRulesAdded: result.timerRules.length,
    timerRulesRemoved: cleanup.counts.timerRulesRemoved,
    displayScenesUpdated: cleanup.counts.displayScenesUpdated,
    displayScenesRemoved: cleanup.counts.displayScenesRemoved,
    activitiesUpdated: cleanup.counts.activitiesUpdated,
    activitiesRemoved: cleanup.counts.activitiesRemoved,
    formulaPresetsRemoved: cleanup.counts.formulaPresetsRemoved,
  };
  const impact: SimplePlayTransitionImpact = {
    ...partialImpact,
    total: Object.entries(partialImpact)
      .filter(([key]) => key !== 'kind')
      .reduce((sum, [, value]) => sum + Number(value), 0),
  };
  const transitionSummary = kind === 'create'
    ? `已创建“${template.title}”`
    : kind === 'adjust'
      ? `已调整“${template.title}”并保留当前数值`
      : `已将原简单玩法替换为“${template.title}”`;
  return {
    nextState,
    summary: [transitionSummary, ...result.summary],
    impact,
  };
}

function simpleArtifactIdPrefixes(play: SimplePlay): { rule: string; timer: string } {
  return {
    rule: `rule-simple-${play.templateId}`,
    timer: `timer-simple-${play.templateId}`,
  };
}

function isGeneratedId(id: string, prefix: string): boolean {
  return id === prefix || id.startsWith(`${prefix}-`);
}

export function isSimplePlayManagedRule(rule: GiftRule, play: SimplePlay): boolean {
  return isGeneratedId(rule.id, simpleArtifactIdPrefixes(play).rule);
}

export function isSimplePlayManagedTimer(rule: TimerRule, play: SimplePlay): boolean {
  return isGeneratedId(rule.id, simpleArtifactIdPrefixes(play).timer);
}

function simpleArtifactsEnabled(state: AppState, attribute: Attribute, play: SimplePlay): boolean {
  const prefixes = simpleArtifactIdPrefixes(play);
  const rules = state.rules.filter((rule) => (
    rule.attributeName === attribute.name && isGeneratedId(rule.id, prefixes.rule)
  ));
  const timers = state.timerRules.filter((rule) => (
    rule.attributeName === attribute.name && isGeneratedId(rule.id, prefixes.timer)
  ));
  return [...rules, ...timers].every((item) => item.enabled !== false);
}

function cleanSimpleArtifactsForAdjustment(
  state: AppState,
  attribute: Attribute,
  play: SimplePlay,
): ManagedCleanup {
  const prefixes = simpleArtifactIdPrefixes(play);
  const rules = state.rules.filter((rule) => !(
    rule.attributeName === attribute.name && isGeneratedId(rule.id, prefixes.rule)
  ));
  const timerRules = state.timerRules.filter((rule) => !(
    rule.attributeName === attribute.name && isGeneratedId(rule.id, prefixes.timer)
  ));
  return {
    attributes: state.attributes.filter((candidate) => candidate !== attribute),
    rules,
    timerRules,
    displayScenes: state.displayScenes,
    activities: state.activities,
    formulaPresets: state.formulaPresets,
    counts: {
      attributesRemoved: 1,
      rulesRemoved: state.rules.length - rules.length,
      timerRulesRemoved: state.timerRules.length - timerRules.length,
      displayScenesUpdated: 0,
      displayScenesRemoved: 0,
      activitiesUpdated: 0,
      activitiesRemoved: 0,
      formulaPresetsRemoved: 0,
    },
  };
}

function normalizeDraft(draft: SimplePlayDraft): SimplePlayDraft {
  const template = getGameplayTemplate(draft.templateId);
  if (!template) throw new Error('找不到简单玩法模板');
  const defaults = createDefaultTemplateInput(template);
  const parameters = Object.fromEntries(template.parameters.map((definition) => [
    definition.id,
    draft.parameters[definition.id] ?? defaults.parameters[definition.id],
  ]));
  const gifts = Object.fromEntries(template.giftSlots.map((slot) => [
    slot.id,
    Array.from(new Set((draft.gifts[slot.id] ?? [])
      .map(Number)
      .filter((giftId) => Number.isInteger(giftId) && giftId > 0))),
  ]));
  if (draft.templateId !== 'overtime') {
    return { templateId: draft.templateId, parameters, gifts, ...(draft.displayThemeId ? { displayThemeId: draft.displayThemeId } : {}) };
  }
  const maximum = Number(parameters.maxSeconds);
  if (!Number.isInteger(maximum) || maximum < 0) throw new Error('最多累计必须是非负整数秒数');
  const selectedIds = new Set(gifts.overtime ?? []);
  const actionsByGift = new Map<number, OvertimeGiftAction>();
  for (const action of draft.overtimeGiftActions ?? []) {
    if (!selectedIds.has(action.giftId) || actionsByGift.has(action.giftId)) continue;
    if (action.operation === 'add' || action.operation === 'subtract') {
      const seconds = Number(action.seconds);
      if (!Number.isInteger(seconds) || seconds <= 0) throw new Error('礼物秒数必须是正整数');
      actionsByGift.set(action.giftId, { giftId: action.giftId, operation: action.operation, seconds });
    } else {
      actionsByGift.set(action.giftId, { giftId: action.giftId, operation: action.operation });
    }
  }
  const overtimeGiftActions = Array.from(selectedIds, (giftId) => (
    actionsByGift.get(giftId) ?? { giftId, operation: 'add' as const, seconds: 60 }
  ));
  return {
    templateId: draft.templateId,
    parameters,
    gifts,
    overtimeGiftActions,
    ...(draft.displayThemeId ? { displayThemeId: draft.displayThemeId } : {}),
  };
}

function toTemplateInput(state: AppState, draft: SimplePlayDraft): GameplayTemplateInput {
  const giftsById = new Map<number, GiftInfo>();
  for (const gift of [...state.giftCatalog, ...state.recentGifts]) giftsById.set(gift.id, gift);
  const gifts = Object.fromEntries(Object.entries(draft.gifts).map(([slotId, giftIds]) => [
    slotId,
    giftIds.map((giftId) => giftsById.get(giftId) ?? ({
      id: giftId,
      name: `礼物 ${giftId}`,
      price: 0,
      coinType: 'gold',
      imgBasic: '',
    } satisfies GiftInfo)),
  ]));
  return {
    parameters: draft.parameters,
    gifts,
    ...(draft.overtimeGiftActions ? { overtimeGiftActions: draft.overtimeGiftActions } : {}),
    ...(draft.displayThemeId ? { displayThemeId: draft.displayThemeId } : {}),
  };
}

function createPureIdFactory(cleanup: ManagedCleanup, templateId: SimplePlayTemplateId): TemplateIdFactory {
  const used = new Set<string>([
    ...cleanup.rules.map((rule) => rule.id),
    ...cleanup.timerRules.map((rule) => rule.id),
    ...cleanup.displayScenes.map((scene) => scene.id),
    ...cleanup.activities.map((activity) => activity.id),
    ...cleanup.activities.flatMap((activity) => activity.milestones.map((milestone) => milestone.id)),
  ]);
  return {
    next: (kind) => {
      const id = nextAvailableId(`${kind}-simple-${templateId}`, used);
      used.add(id);
      return id;
    },
  };
}

function nextAvailableId(base: string, existing: Iterable<string>): string {
  const used = existing instanceof Set ? existing : new Set(existing);
  if (!used.has(base)) return base;
  let suffix = 2;
  while (used.has(`${base}-${suffix}`)) suffix += 1;
  return `${base}-${suffix}`;
}

function emptyManagedCleanup(state: AppState): ManagedCleanup {
  return {
    attributes: state.attributes,
    rules: state.rules,
    timerRules: state.timerRules,
    displayScenes: state.displayScenes,
    activities: state.activities,
    formulaPresets: state.formulaPresets,
    counts: {
      attributesRemoved: 0,
      rulesRemoved: 0,
      timerRulesRemoved: 0,
      displayScenesUpdated: 0,
      displayScenesRemoved: 0,
      activitiesUpdated: 0,
      activitiesRemoved: 0,
      formulaPresetsRemoved: 0,
    },
  };
}

function cleanManagedReferences(state: AppState, attribute: Attribute): ManagedCleanup {
  const name = attribute.name;
  const attributes = state.attributes.filter((candidate) => candidate !== attribute);
  const rules = state.rules.filter((rule) => !referencesAttribute(name, rule.attributeName, rule.formula, rule.condition));
  const timerRules = state.timerRules.filter((rule) => !referencesAttribute(name, rule.attributeName, rule.formula, rule.condition));

  let displayScenesUpdated = 0;
  let displayScenesRemoved = 0;
  const removedSceneIds = new Set<string>();
  const displayScenes = state.displayScenes.flatMap((scene): DisplayScene[] => {
    if (!scene.attributeNames.includes(name)) return [scene];
    const attributeNames = scene.attributeNames.filter((attributeName) => attributeName !== name);
    if (attributeNames.length === 0) {
      displayScenesRemoved += 1;
      removedSceneIds.add(scene.id);
      return [];
    }
    displayScenesUpdated += 1;
    return [{ ...scene, attributeNames }];
  });

  let activitiesUpdated = 0;
  let activitiesRemoved = 0;
  const activities = state.activities.flatMap((activity): ActivitySession[] => {
    const references = activityReferencesAttribute(activity, name);
    const sceneRemoved = Boolean(activity.sceneId && removedSceneIds.has(activity.sceneId));
    if (!references && !sceneRemoved) return [activity];
    const attributeNames = activity.attributeNames.filter((attributeName) => attributeName !== name);
    if (references && attributeNames.length === 0) {
      activitiesRemoved += 1;
      return [];
    }
    activitiesUpdated += 1;
    const initialValues = omitRecordKey(activity.initialValues, name);
    const values = activity.result ? omitRecordKey(activity.result.values, name) : undefined;
    return [{
      ...activity,
      attributeNames,
      initialValues,
      milestones: activity.milestones.filter((milestone) => milestone.attributeName !== name),
      ...(sceneRemoved ? { sceneId: undefined } : {}),
      ...(activity.result ? {
        result: {
          ...activity.result,
          values: values ?? {},
          ...(activity.result.winnerAttributeName === name ? { winnerAttributeName: undefined } : {}),
        },
      } : {}),
    }];
  });
  const formulaPresets = state.formulaPresets.filter((preset) => !referencesAttribute(
    name,
    preset.sourceAttributeName,
    preset.formula,
  ));

  return {
    attributes,
    rules,
    timerRules,
    displayScenes,
    activities,
    formulaPresets,
    counts: {
      attributesRemoved: 1,
      rulesRemoved: state.rules.length - rules.length,
      timerRulesRemoved: state.timerRules.length - timerRules.length,
      displayScenesUpdated,
      displayScenesRemoved,
      activitiesUpdated,
      activitiesRemoved,
      formulaPresetsRemoved: state.formulaPresets.length - formulaPresets.length,
    },
  };
}

function referencesAttribute(attributeName: string, targetName: string | undefined, ...formulas: Array<string | undefined>): boolean {
  return targetName === attributeName || formulas.some((formula) => formulaReferencesAttribute(formula, attributeName));
}

function formulaReferencesAttribute(formula: string | undefined, attributeName: string): boolean {
  if (!formula) return false;
  try {
    return collectVars(formula).includes(attributeName);
  } catch {
    const identifiers: string[] = formula.match(/[\p{L}_][\p{L}\p{N}_]*/gu) ?? [];
    return identifiers.includes(attributeName);
  }
}

function activityReferencesAttribute(activity: ActivitySession, attributeName: string): boolean {
  return activity.attributeNames.includes(attributeName)
    || activity.milestones.some((milestone) => milestone.attributeName === attributeName)
    || Object.hasOwn(activity.initialValues, attributeName)
    || Boolean(activity.result && (
      activity.result.winnerAttributeName === attributeName
      || Object.hasOwn(activity.result.values, attributeName)
    ));
}

function omitRecordKey(values: Record<string, number>, key: string): Record<string, number> {
  return Object.fromEntries(Object.entries(values).filter(([name]) => name !== key));
}

function canonicalize(value: unknown): unknown {
  if (Array.isArray(value)) return value.map(canonicalize);
  if (value === null || typeof value !== 'object') return value;
  return Object.fromEntries(Object.entries(value as Record<string, unknown>)
    .filter(([, child]) => child !== undefined)
    .sort(([left], [right]) => left < right ? -1 : left > right ? 1 : 0)
    .map(([key, child]) => [key, canonicalize(child)]));
}
