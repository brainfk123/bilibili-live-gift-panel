import type { AppState, Attribute, DisplayScene, DisplaySceneLayout, DisplayThemeId } from './types';
import { normalizeDisplayThemeId } from './display-themes';

export const MAX_DISPLAY_SCENE_ATTRIBUTES = 12;

export interface DisplayTarget {
  attributeName?: string;
  sceneId?: string;
  view?: 'blind-box';
}

export interface ResolvedDisplayTarget {
  attributes: Attribute[];
  scene?: DisplayScene;
  layout: DisplaySceneLayout;
  missingLabel?: string;
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
      layout: candidate.layout === 'grid' ? 'grid' : 'stack',
      themeId: normalizeDisplayThemeId(candidate.themeId ?? fallbackThemeId),
      ...(candidate.appearance ? { appearance: { ...candidate.appearance } } : {}),
    });
  }
  return result;
}

export function resolveDisplayTarget(state: AppState, target: DisplayTarget = {}): ResolvedDisplayTarget {
  if (target.sceneId) {
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
  if (target.attributeName) {
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

export function displaySceneUrl(origin: string, sceneId: string): string {
  return `${origin}/?mode=display&scene=${encodeURIComponent(sceneId)}`;
}

export function blindBoxDisplayUrl(origin: string): string {
  return `${origin}/?mode=display&view=blind-box`;
}
