export type HostedUserPage = 'overview' | 'attributes' | 'activities' | 'targets' | 'obs' | 'data';

export interface HostedUserPageDefinition {
  readonly id: HostedUserPage;
  readonly label: string;
  readonly description: string;
}

export const HOSTED_USER_PAGES: readonly HostedUserPageDefinition[] = Object.freeze([
  { id: 'overview', label: '概览', description: '直播间、运行状态与待处理事项' },
  { id: 'attributes', label: '属性玩法', description: '属性、礼物规则与定时规则' },
  { id: 'activities', label: '活动会话', description: '进行中的活动与阶段结果' },
  { id: 'targets', label: '礼物目标', description: '礼物目标面板与完成进度' },
  { id: 'obs', label: 'OBS 面板', description: '在线输出定义与浏览器源' },
  { id: 'data', label: '数据中心', description: '场次、趋势与观众贡献' },
]);

const pageIDs = new Set<HostedUserPage>(HOSTED_USER_PAGES.map((page) => page.id));

export function parseHostedUserPage(search: string): HostedUserPage {
  const value = new URLSearchParams(search).get('workspace');
  return value && pageIDs.has(value as HostedUserPage) ? value as HostedUserPage : 'overview';
}

export function hostedUserPageSearch(search: string, page: HostedUserPage): string {
  const parameters = new URLSearchParams(search);
  parameters.set('workspace', page);
  const serialized = parameters.toString();
  return serialized ? `?${serialized}` : '';
}

export function hostedUserPageDefinition(page: HostedUserPage): HostedUserPageDefinition {
  return HOSTED_USER_PAGES.find((candidate) => candidate.id === page) ?? HOSTED_USER_PAGES[0]!;
}
