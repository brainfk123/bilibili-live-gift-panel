import type { FormulaPreset, FormulaPresetContext } from './types';

export interface FormulaPresetDraft {
  name: string;
  context: FormulaPresetContext;
  formula: string;
  sourceAttributeName: string;
}

export interface FormulaPresetSaveResult {
  presets: FormulaPreset[];
  preset: FormulaPreset;
  created: boolean;
}

export function saveFormulaPreset(
  presets: readonly FormulaPreset[],
  draft: FormulaPresetDraft,
): FormulaPresetSaveResult {
  const normalized = normalizeDraft(draft);
  const existingIndex = presets.findIndex((preset) => (
    preset.context === normalized.context
      && normalizeName(preset.name) === normalizeName(normalized.name)
  ));
  const created = existingIndex < 0;
  const preset: FormulaPreset = {
    id: created ? createPresetId() : presets[existingIndex].id,
    ...normalized,
  };
  const next = presets.map((item) => ({ ...item }));
  if (created) next.push(preset);
  else next[existingIndex] = preset;
  return { presets: next, preset, created };
}

export function applyFormulaPreset(preset: FormulaPreset, targetAttributeName: string): string {
  const target = targetAttributeName.trim();
  if (!target) throw new Error('目标属性名不能为空');
  return replaceFormulaVariable(preset.formula, preset.sourceAttributeName, target);
}

export function replaceFormulaVariable(formula: string, from: string, to: string): string {
  if (!from || from === to) return formula;
  const escaped = from.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
  return formula.replace(new RegExp(`(?<![\\p{L}\\p{N}_])${escaped}(?![\\p{L}\\p{N}_])`, 'gu'), to);
}

function normalizeDraft(draft: FormulaPresetDraft): FormulaPresetDraft {
  const name = draft.name.trim();
  const formula = draft.formula.trim();
  const sourceAttributeName = draft.sourceAttributeName.trim();
  if (!name) throw new Error('预设名称不能为空');
  if (!formula) throw new Error('预设公式不能为空');
  if (!sourceAttributeName) throw new Error('预设来源属性不能为空');
  return { name, context: draft.context, formula, sourceAttributeName };
}

function normalizeName(name: string): string {
  return name.trim().toLocaleLowerCase();
}

function createPresetId(): string {
  const uuid = globalThis.crypto?.randomUUID?.();
  if (uuid) return `formula-preset-${uuid}`;
  return `formula-preset-${Date.now()}-${Math.random().toString(36).slice(2, 10)}`;
}
