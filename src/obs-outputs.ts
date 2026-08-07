import { getDisplayTheme } from './display-themes';
import { displaySceneLayoutName } from './display-scenes';
import { formatValue } from './format';
import type { AppState, Attribute } from './types';

export type ObsOutputTarget =
  | { kind: 'attribute'; attributeName: string }
  | { kind: 'scene'; sceneId: string }
  | { kind: 'blind-box'; blindBoxGiftId?: number }
  | { kind: 'gift-target'; panelId: string };

export type ObsOutputCatalogKind = 'attribute' | 'scene' | 'target' | 'leaderboard';
export type ObsOutputCatalogGroupId = 'attributes' | 'scenes' | 'gift-targets' | 'leaderboards';

export interface ObsOutputCatalogItem {
  id: string;
  kind: ObsOutputCatalogKind;
  target: ObsOutputTarget;
  title: string;
  meta: string;
  imageUrl?: string;
}

export interface ObsOutputCatalogGroup {
  id: ObsOutputCatalogGroupId;
  title: string;
  description: string;
  emptyText?: string;
  emptyActionLabel?: string;
  items: ObsOutputCatalogItem[];
}

export interface ObsOutputCatalogOptions {
  blindBoxLoginEnabled: boolean;
}

export function parseObsOutputTarget(search: string | undefined): ObsOutputTarget | undefined {
  const params = new URLSearchParams(search ?? '');
  const view = params.get('view');
  if (view === 'blind-box') {
    const requestedGiftId = Number(params.get('blindBox'));
    return {
      kind: 'blind-box',
      ...(Number.isInteger(requestedGiftId) && requestedGiftId > 0 ? { blindBoxGiftId: requestedGiftId } : {}),
    };
  }
  if (view === 'gift-kpi') {
    const panelId = params.get('panel')?.trim();
    return panelId ? { kind: 'gift-target', panelId } : undefined;
  }
  const sceneId = params.get('scene')?.trim();
  if (sceneId) return { kind: 'scene', sceneId };
  const attributeName = params.get('attribute')?.trim();
  if (attributeName) return { kind: 'attribute', attributeName };
  return undefined;
}

export function obsOutputUrl(origin: string, target?: ObsOutputTarget): string {
  const base = `${origin.replace(/\/$/, '')}/?mode=display`;
  if (!target) return base;
  switch (target.kind) {
    case 'attribute':
      return `${base}&attribute=${encodeURIComponent(target.attributeName)}`;
    case 'scene':
      return `${base}&scene=${encodeURIComponent(target.sceneId)}`;
    case 'blind-box':
      return `${base}&view=blind-box${target.blindBoxGiftId ? `&blindBox=${encodeURIComponent(String(target.blindBoxGiftId))}` : ''}`;
    case 'gift-target':
      return `${base}&view=gift-kpi&panel=${encodeURIComponent(target.panelId)}`;
  }
}

export function attributeDisplayUrl(origin: string, attributeName: string): string {
  return obsOutputUrl(origin, { kind: 'attribute', attributeName });
}

export function displaySceneUrl(origin: string, sceneId: string): string {
  return obsOutputUrl(origin, { kind: 'scene', sceneId });
}

export function blindBoxDisplayUrl(origin: string, blindBoxGiftId?: number): string {
  return obsOutputUrl(origin, { kind: 'blind-box', ...(blindBoxGiftId ? { blindBoxGiftId } : {}) });
}

export function giftKpiDisplayUrl(origin: string, panelId: string): string {
  return obsOutputUrl(origin, { kind: 'gift-target', panelId });
}

export function buildObsOutputCatalog(state: AppState, options: ObsOutputCatalogOptions): ObsOutputCatalogGroup[] {
  return [
    {
      id: 'attributes',
      title: '单属性面板',
      description: '每个属性自动拥有一个独立链接。',
      emptyText: '创建属性后会自动生成单属性 OBS 链接。',
      emptyActionLabel: '前往创建属性',
      items: state.attributes.map((attribute) => ({
        id: `attribute:${attribute.name}`,
        kind: 'attribute',
        target: { kind: 'attribute', attributeName: attribute.name },
        title: attribute.display?.title?.trim() || attribute.name,
        meta: `${attributeOutputFormatLabel(attribute)} · 当前 ${formatValue(attribute.value, attribute)}`,
      })),
    },
    {
      id: 'scenes',
      title: '组合面板',
      description: '把多个属性排进同一个 OBS 画面。',
      emptyText: '还没有组合面板。',
      emptyActionLabel: '新建组合面板',
      items: state.displayScenes.map((scene) => ({
        id: `scene:${scene.id}`,
        kind: 'scene',
        target: { kind: 'scene', sceneId: scene.id },
        title: scene.name,
        meta: `${displaySceneLayoutName(scene.layout)} · ${getDisplayTheme(scene.themeId).name} · ${scene.attributeNames.length} 个属性`,
      })),
    },
    {
      id: 'gift-targets',
      title: '礼物目标面板',
      description: '直接显示指定礼物的目标完成度。',
      emptyText: '还没有礼物目标面板。',
      emptyActionLabel: '前往创建礼物目标',
      items: state.giftKpiPanels.map((panel) => ({
        id: `gift-target:${panel.id}`,
        kind: 'target',
        target: { kind: 'gift-target', panelId: panel.id },
        title: panel.name,
        meta: `${panel.items.length} 个礼物 · ${giftTargetLayoutLabel(panel.layout)}`,
        imageUrl: panel.items.find((item) => item.imageUrl)?.imageUrl,
      })),
    },
    {
      id: 'leaderboards',
      title: '排行榜面板',
      description: '盲盒盈亏榜可在数据中心选择统计范围。',
      items: [{
        id: 'leaderboard:blind-box',
        kind: 'leaderboard',
        target: { kind: 'blind-box' },
        title: '盲盒盈亏榜',
        meta: `${options.blindBoxLoginEnabled ? '登录能力已开启' : '依赖登录识别盲盒'} · 显示 ${state.blindBoxDisplay.viewerSlots} 个观众`,
      }],
    },
  ];
}

export function obsOutputCount(catalog: readonly ObsOutputCatalogGroup[]): number {
  return catalog.reduce((count, group) => count + group.items.length, 0);
}

export function attributeOutputFormatLabel(attribute: Attribute): string {
  if (attribute.format === 'hhmmss') return '计时器';
  if (attribute.format === 'suffix') return `数字${attribute.suffix ? ` · ${attribute.suffix}` : ' + 后缀'}`;
  return '纯数字';
}

function giftTargetLayoutLabel(layout: AppState['giftKpiPanels'][number]['layout']): string {
  if (layout === 'grid') return '信息网格';
  if (layout === 'dashboard') return '主辅仪表盘';
  return '纵向清单';
}
