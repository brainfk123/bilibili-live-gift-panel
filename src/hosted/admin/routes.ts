export type AdminSection = 'overview' | 'accounts' | 'invitations' | 'bili-service' | 'settings';

export const adminSections: ReadonlyArray<{ id: AdminSection; label: string }> = Object.freeze([
  { id: 'overview', label: '运营总览' },
  { id: 'accounts', label: '主播账号' },
  { id: 'invitations', label: '邀请码' },
  { id: 'bili-service', label: 'B站服务账号' },
  { id: 'settings', label: '系统设置' },
]);
