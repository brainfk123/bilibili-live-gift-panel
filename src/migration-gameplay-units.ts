import { collectVars } from './formula';
import type { OnlineMigrationCropPreset, OnlineMigrationDefinition, OnlineMigrationRuntime } from './migration';

export type GameplayUnitKind = 'simple-play' | 'activity' | 'attribute' | 'gift-target';

export interface GameplayUnitDeclaration {
  id: string;
  kind: GameplayUnitKind;
  name: string;
  attributeIds: string[];
  ruleIds: string[];
  timerRuleIds: string[];
  formulaPresetIds: string[];
  activityIds: string[];
  displaySceneIds: string[];
  giftTargetPanelIds: string[];
  giftIds: number[];
  cropPresetIds: string[];
}

export interface GameplayGroupReason {
  kind: 'shared-attribute' | 'shared-scene' | 'shared-crop-preset';
  referenceId: string;
}

export interface GameplayGroupDeclaration {
  id: string;
  unitIds: string[];
  reasons: GameplayGroupReason[];
}

export interface GameplayDependencyDeclaration {
  algorithmVersion: 1;
  units: GameplayUnitDeclaration[];
  groups: GameplayGroupDeclaration[];
}

export interface GameplayUnitDerivationInput {
  definition: OnlineMigrationDefinition;
  runtime: OnlineMigrationRuntime;
  cropPresets?: readonly OnlineMigrationCropPreset[];
}

interface UnitDraft {
  id: string;
  kind: GameplayUnitKind;
  name: string;
  primaryAttributeIds: string[];
  activityIds: string[];
  giftTargetPanelIds: string[];
  simplePlay: boolean;
}

/**
 * Builds the EXE's advisory gameplay declaration from the same allowlisted
 * projection that is serialized. Hosted must independently derive its own
 * declaration and never trust this result as authorization to apply data.
 */
export function deriveGameplayUnits(input: GameplayUnitDerivationInput): GameplayDependencyDeclaration {
  const { definition } = input;
  const attributesByID = new Map(definition.attributes.map((attribute) => [attribute.id, attribute]));
  const attributesByName = new Map<string, string>();
  for (const attribute of definition.attributes) {
    if (!attributesByName.has(attribute.name)) attributesByName.set(attribute.name, attribute.id);
  }
  const claimed = new Set<string>();
  const drafts: UnitDraft[] = [];

  if (definition.simplePlay && attributesByID.has(definition.simplePlay.attributeId)) {
    const attributeID = definition.simplePlay.attributeId;
    claimed.add(attributeID);
    drafts.push({
      id: `simple-play:${attributeID}`,
      kind: 'simple-play',
      name: simplePlayName(definition.simplePlay),
      primaryAttributeIds: [attributeID],
      activityIds: [],
      giftTargetPanelIds: [],
      simplePlay: true,
    });
  }

  for (const activity of definition.activities) {
    const attributeIds = uniqueSorted(activity.attributeIds.filter((id) => attributesByID.has(id)));
    for (const attributeID of attributeIds) claimed.add(attributeID);
    drafts.push({
      id: `activity:${activity.id}`,
      kind: 'activity',
      name: activity.name,
      primaryAttributeIds: attributeIds,
      activityIds: [activity.id],
      giftTargetPanelIds: [],
      simplePlay: false,
    });
  }

  for (const attribute of definition.attributes) {
    if (claimed.has(attribute.id)) continue;
    drafts.push({
      id: `attribute:${attribute.id}`,
      kind: 'attribute',
      name: attribute.name,
      primaryAttributeIds: [attribute.id],
      activityIds: [],
      giftTargetPanelIds: [],
      simplePlay: false,
    });
  }

  for (const panel of definition.giftTargetPanels) {
    drafts.push({
      id: `gift-target:${panel.id}`,
      kind: 'gift-target',
      name: panel.name,
      primaryAttributeIds: [],
      activityIds: [],
      giftTargetPanelIds: [panel.id],
      simplePlay: false,
    });
  }

  const units = drafts.map((draft) => materializeUnit(draft, input, attributesByName))
    .sort((left, right) => compareCodeUnits(left.id, right.id));
  return {
    algorithmVersion: 1,
    units,
    groups: connectedGroups(units),
  };
}

function materializeUnit(
  draft: UnitDraft,
  input: GameplayUnitDerivationInput,
  attributesByName: ReadonlyMap<string, string>,
): GameplayUnitDeclaration {
  const { definition } = input;
  const rules = definition.rules.filter((rule) => draft.primaryAttributeIds.includes(rule.attributeId));
  const timerRules = definition.timerRules.filter((rule) => draft.primaryAttributeIds.includes(rule.attributeId));
  const formulaPresets = definition.formulaPresets.filter((preset) => draft.primaryAttributeIds.includes(preset.attributeId));
  const dependencyAttributes = new Set(draft.primaryAttributeIds);
  for (const formula of [
    ...rules.flatMap((rule) => [rule.formula, rule.condition]),
    ...timerRules.flatMap((rule) => [rule.formula, rule.condition]),
    ...formulaPresets.map((preset) => preset.formula),
  ]) {
    for (const attributeID of formulaAttributeIDs(formula, attributesByName)) dependencyAttributes.add(attributeID);
  }

  const activitySceneIDs = new Set(definition.activities
    .filter((activity) => draft.activityIds.includes(activity.id))
    .flatMap((activity) => activity.sceneId ? [activity.sceneId] : []));
  const displayScenes = definition.displayScenes.filter((scene) => (
    activitySceneIDs.has(scene.id)
    || scene.attributeIds.some((attributeID) => dependencyAttributes.has(attributeID))
  ));
  const targetPanels = definition.giftTargetPanels.filter((panel) => draft.giftTargetPanelIds.includes(panel.id));
  const giftIDs = new Set<number>();
  for (const rule of rules) {
    giftIDs.add(rule.giftId);
    for (const giftID of rule.matchGiftIds ?? []) giftIDs.add(giftID);
  }
  if (draft.simplePlay) {
    for (const ids of Object.values(definition.simplePlay?.gifts ?? {})) for (const giftID of ids) giftIDs.add(giftID);
    for (const action of definition.simplePlay?.overtimeGiftActions ?? []) giftIDs.add(action.giftId);
  }
  for (const panel of targetPanels) for (const item of panel.items) giftIDs.add(item.giftId);

  const effectIDs = new Set(definition.gifts
    .filter((gift) => giftIDs.has(gift.id) && gift.effectId !== undefined)
    .map((gift) => Number(gift.effectId)));
  const cropPresetIds = (input.cropPresets ?? []).flatMap((preset) => {
    const match = /^(gift|effect):([1-9]\d*)$/.exec(preset.id);
    if (!match) return [];
    const id = Number(match[2]);
    return (match[1] === 'gift' ? giftIDs.has(id) : effectIDs.has(id)) ? [preset.id] : [];
  });

  return {
    id: draft.id,
    kind: draft.kind,
    name: draft.name,
    attributeIds: uniqueSorted(dependencyAttributes),
    ruleIds: uniqueSorted(rules.map((rule) => rule.id)),
    timerRuleIds: uniqueSorted(timerRules.map((rule) => rule.id)),
    formulaPresetIds: uniqueSorted(formulaPresets.map((preset) => preset.id)),
    activityIds: uniqueSorted(draft.activityIds),
    displaySceneIds: uniqueSorted(displayScenes.map((scene) => scene.id)),
    giftTargetPanelIds: uniqueSorted(draft.giftTargetPanelIds),
    giftIds: [...giftIDs].sort((left, right) => left - right),
    cropPresetIds: uniqueSorted(cropPresetIds),
  };
}

function simplePlayName(simplePlay: NonNullable<OnlineMigrationDefinition['simplePlay']>): string {
  const name = simplePlay.parameters.name;
  return typeof name === 'string' && name.trim() ? name.trim() : simplePlay.templateId;
}

function formulaAttributeIDs(formula: string | undefined, attributesByName: ReadonlyMap<string, string>): string[] {
  if (!formula) return [];
  let names: string[];
  try {
    names = collectVars(formula);
  } catch {
    names = formula.match(/[\p{L}_][\p{L}\p{N}_]*/gu) ?? [];
  }
  return uniqueSorted(names.flatMap((name) => {
    const attributeID = attributesByName.get(name);
    return attributeID ? [attributeID] : [];
  }));
}

function connectedGroups(units: readonly GameplayUnitDeclaration[]): GameplayGroupDeclaration[] {
  const adjacency = new Map(units.map((unit) => [unit.id, new Set<string>()]));
  connectShared(units, adjacency, (unit) => unit.attributeIds);
  connectShared(units, adjacency, (unit) => unit.displaySceneIds);
  connectShared(units, adjacency, (unit) => unit.cropPresetIds);

  const byID = new Map(units.map((unit) => [unit.id, unit]));
  const visited = new Set<string>();
  const groups: GameplayGroupDeclaration[] = [];
  for (const unit of units) {
    if (visited.has(unit.id) || adjacency.get(unit.id)?.size === 0) continue;
    const pending = [unit.id];
    const unitIds: string[] = [];
    while (pending.length > 0) {
      const current = pending.pop()!;
      if (visited.has(current)) continue;
      visited.add(current);
      unitIds.push(current);
      for (const neighbor of adjacency.get(current) ?? []) pending.push(neighbor);
    }
    unitIds.sort();
    const groupUnits = unitIds.map((id) => byID.get(id)!).filter(Boolean);
    const groupReasons = connectingReasons(groupUnits);
    groups.push({ id: `group:${stableHash(unitIds.join('\n'))}`, unitIds, reasons: groupReasons });
  }
  return groups.sort((left, right) => compareCodeUnits(left.id, right.id));
}

function connectShared(
  units: readonly GameplayUnitDeclaration[],
  adjacency: Map<string, Set<string>>,
  references: (unit: GameplayUnitDeclaration) => readonly string[],
): void {
  const owners = new Map<string, string[]>();
  for (const unit of units) {
    for (const referenceID of references(unit)) {
      const ids = owners.get(referenceID) ?? [];
      ids.push(unit.id);
      owners.set(referenceID, ids);
    }
  }
  for (const unitIDs of owners.values()) {
    const uniqueUnitIDs = uniqueSorted(unitIDs);
    if (uniqueUnitIDs.length < 2) continue;
    for (let index = 1; index < uniqueUnitIDs.length; index += 1) {
      adjacency.get(uniqueUnitIDs[0]!)?.add(uniqueUnitIDs[index]!);
      adjacency.get(uniqueUnitIDs[index]!)?.add(uniqueUnitIDs[0]!);
    }
  }
}

function connectingReasons(units: readonly GameplayUnitDeclaration[]): GameplayGroupReason[] {
  const parent = new Map(units.map((unit) => [unit.id, unit.id]));
  const find = (id: string): string => {
    const current = parent.get(id)!;
    if (current === id) return id;
    const root = find(current);
    parent.set(id, root);
    return root;
  };
  const candidates = [
    ...reasonCandidates(units, 'shared-attribute', (unit) => unit.attributeIds),
    ...reasonCandidates(units, 'shared-scene', (unit) => unit.displaySceneIds),
    ...reasonCandidates(units, 'shared-crop-preset', (unit) => unit.cropPresetIds),
  ];
  const selected: GameplayGroupReason[] = [];
  for (const candidate of candidates) {
    const roots = uniqueSorted(candidate.unitIds.map(find));
    if (roots.length < 2) continue;
    const root = roots[0]!;
    for (const other of roots.slice(1)) parent.set(other, root);
    selected.push({ kind: candidate.kind, referenceId: candidate.referenceId });
  }
  return selected;
}

function reasonCandidates(
  units: readonly GameplayUnitDeclaration[],
  kind: GameplayGroupReason['kind'],
  references: (unit: GameplayUnitDeclaration) => readonly string[],
): Array<GameplayGroupReason & { unitIds: string[] }> {
  const owners = new Map<string, string[]>();
  for (const unit of units) {
    for (const referenceID of new Set(references(unit))) {
      owners.set(referenceID, [...(owners.get(referenceID) ?? []), unit.id]);
    }
  }
  return [...owners.entries()]
    .filter(([, unitIds]) => new Set(unitIds).size > 1)
    .sort(([left], [right]) => compareCodeUnits(left, right))
    .map(([referenceId, unitIds]) => ({ kind, referenceId, unitIds: uniqueSorted(unitIds) }));
}

function stableHash(value: string): string {
  let hash = 0x811c9dc5;
  for (let index = 0; index < value.length; index += 1) {
    hash ^= value.charCodeAt(index);
    hash = Math.imul(hash, 0x01000193);
  }
  return (hash >>> 0).toString(16).padStart(8, '0');
}

function uniqueSorted(values: Iterable<string>): string[] {
  return [...new Set(values)].sort(compareCodeUnits);
}

export function compareCodeUnits(left: string, right: string): number {
  return left < right ? -1 : left > right ? 1 : 0;
}
