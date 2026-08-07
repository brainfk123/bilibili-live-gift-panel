import type { AppState, Attribute, DisplayScene, DisplaySceneLayout, DisplayThemeId } from './types';
import type { ObsOutputTarget } from './obs-outputs';
import { normalizeDisplayThemeId } from './display-themes';
import { normalizeDisplaySceneLayout } from './output-config';

export { normalizeDisplaySceneLayout } from './output-config';

export const MAX_DISPLAY_SCENE_ATTRIBUTES = 12;

export interface ResolvedDisplayTarget {
  attributes: Attribute[];
  scene?: DisplayScene;
  layout: DisplaySceneLayout;
  missingLabel?: string;
}

export const DISPLAY_SCENE_LAYOUTS: ReadonlyArray<{
  id: DisplaySceneLayout;
  name: string;
  description: string;
}> = [
  { id: 'stack', name: '纵向清单', description: '适合倒计时、流程状态和窄画布' },
  { id: 'grid', name: '信息网格', description: '适合多资源、多指标同时监控' },
  { id: 'focus', name: '主角聚焦', description: '第一项突出显示，适合 Boss 和主目标' },
  { id: 'versus', name: '双方对抗', description: '前两项左右对峙，适合阵营和投票' },
  { id: 'dashboard', name: '主辅仪表盘', description: '核心指标居左，其余状态排列在右' },
];

export function displaySceneLayoutName(layout: DisplaySceneLayout): string {
  return DISPLAY_SCENE_LAYOUTS.find((candidate) => candidate.id === layout)?.name ?? '纵向清单';
}

export function normalizeDisplayScenes(
  scenes: readonly Partial<DisplayScene>[] | undefined,
  attributes: readonly Attribute[],
  fallbackThemeId: DisplayThemeId,
): DisplayScene[] {
  const availableNames = new Set(attributes.map((attribute) => attribute.name));
  const ids = new Set<string>();
  const result: DisplayScene[] = [];
  for (const candidate of scenes ?? []) {
    const id = String(candidate.id ?? '').trim();
    const name = String(candidate.name ?? '').trim();
    if (!id || !name || ids.has(id)) continue;
    const attributeNames = Array.from(new Set((candidate.attributeNames ?? [])
      .map((attributeName) => String(attributeName).trim())
      .filter((attributeName) => availableNames.has(attributeName))))
      .slice(0, MAX_DISPLAY_SCENE_ATTRIBUTES);
    if (attributeNames.length === 0) continue;
    ids.add(id);
    result.push({
      id,
      name,
      attributeNames,
      layout: normalizeDisplaySceneLayout(candidate.layout),
      themeId: normalizeDisplayThemeId(candidate.themeId ?? fallbackThemeId),
      ...(candidate.appearance ? { appearance: { ...candidate.appearance } } : {}),
    });
  }
  return result;
}

export function resolveDisplayTarget(state: AppState, target?: ObsOutputTarget): ResolvedDisplayTarget {
  if (target?.kind === 'scene') {
    const scene = state.displayScenes.find((candidate) => candidate.id === target.sceneId);
    if (!scene) return { attributes: [], layout: 'stack', missingLabel: `找不到组合面板“${target.sceneId}”` };
    const byName = new Map(state.attributes.map((attribute) => [attribute.name, attribute]));
    return {
      scene,
      layout: scene.layout,
      attributes: scene.attributeNames.flatMap((name) => {
        const attribute = byName.get(name);
        return attribute ? [attribute] : [];
      }),
    };
  }
  if (target?.kind === 'attribute') {
    const attribute = state.attributes.find((candidate) => candidate.name === target.attributeName);
    return attribute
      ? { attributes: [attribute], layout: 'stack' }
      : { attributes: [], layout: 'stack', missingLabel: `找不到属性“${target.attributeName}”` };
  }
  return { attributes: state.attributes, layout: 'stack' };
}

export function createDisplaySceneId(): string {
  const uuid = globalThis.crypto?.randomUUID?.();
  return uuid ? `scene-${uuid}` : `scene-${Date.now()}-${Math.random().toString(36).slice(2, 9)}`;
}
