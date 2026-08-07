import type { TutorialLesson } from '../../types';

export const CONFIG_PAGES = [
  { id: 'overview', label: '概览', description: '直播间与账号', icon: 'overview' },
  { id: 'attributes', label: '属性玩法', description: '属性、礼物与定时规则', icon: 'attributes' },
  { id: 'activities', label: '活动会话', description: '开始、锁定与结算', icon: 'activities' },
  { id: 'kpi', label: '礼物目标', description: '目标数量与进度', icon: 'kpi' },
  { id: 'obs', label: 'OBS 面板', description: '组合画面与输出', icon: 'obs' },
  { id: 'data', label: '数据中心', description: '排行榜与生效记录', icon: 'data' },
] as const;

export type ConfigPageId = (typeof CONFIG_PAGES)[number]['id'];
export type ConfigPageIcon = (typeof CONFIG_PAGES)[number]['icon'];

export const CONFIG_PAGE_SECTION_SELECTORS: Record<ConfigPageId, readonly string[]> = {
  overview: ['.overview-dashboard', '.connection-grid'],
  attributes: ['.attributes-section'],
  activities: ['.activity-workspace-section'],
  kpi: ['.gift-kpi-config-section'],
  obs: ['.obs-panel-hub', '.display-scenes-section'],
  data: ['.contribution-section', '.gift-history-section'],
};

const CONFIG_PAGE_IDS = new Set<string>(CONFIG_PAGES.map((page) => page.id));

export function parseConfigPage(search: string | undefined): ConfigPageId {
  const page = new URLSearchParams(search ?? '').get('page');
  return page && CONFIG_PAGE_IDS.has(page) ? page as ConfigPageId : 'overview';
}

export function configPageSearch(search: string | undefined, page: ConfigPageId): string {
  const params = new URLSearchParams(search ?? '');
  params.set('mode', 'config');
  params.set('page', page);
  return `?${params.toString()}`;
}

export function configPageDefinition(page: ConfigPageId): (typeof CONFIG_PAGES)[number] {
  return CONFIG_PAGES.find((candidate) => candidate.id === page) ?? CONFIG_PAGES[0];
}

export function configPageForTutorialLesson(lesson: TutorialLesson): ConfigPageId {
  return lesson === 'room' ? 'overview' : 'attributes';
}

export function configPageForSelector(selector: string): ConfigPageId | undefined {
  return (Object.entries(CONFIG_PAGE_SECTION_SELECTORS) as [ConfigPageId, readonly string[]][])
    .find(([, selectors]) => selectors.includes(selector))?.[0];
}
