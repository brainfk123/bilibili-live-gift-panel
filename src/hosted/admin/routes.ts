export type AdminSection = 'overview' | 'accounts' | 'invitations' | 'bili-service' | 'obs' | 'security';

export const adminSections: ReadonlyArray<{ id: AdminSection; label: string }> = Object.freeze([
  { id: 'overview', label: '总览' },
  { id: 'accounts', label: '账号' },
  { id: 'invitations', label: '邀请' },
  { id: 'bili-service', label: '服务账号' },
  { id: 'obs', label: 'OBS' },
  { id: 'security', label: '安全与恢复' },
]);
