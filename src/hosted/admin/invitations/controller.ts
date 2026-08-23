import type { AdminInvitationQuery, AdminInvitationRecord, HostedAPI } from '../../api';

export type InvitationNotice = { kind: 'success' | 'error'; message: string; code?: string };

export interface InvitationInventorySnapshot {
  rows: AdminInvitationRecord[];
  loading: boolean;
  creating: boolean;
  error?: string;
  notice?: InvitationNotice;
}

export interface InvitationClipboardPort { writeText(value: string): Promise<void>; }
export type InvitationSharePort = (value: string) => Promise<void>;

type InvitationInventoryAPI = Pick<HostedAPI, 'adminInvitations'> & Partial<Pick<HostedAPI, 'createAdminInvitations' | 'revokeAdminInvitation'>>;

export interface InvitationInventoryController {
  reload(query?: AdminInvitationQuery): Promise<void>;
  create(count: number, validity: '7d' | '30d' | 'permanent'): Promise<void>;
  revoke(id: number): Promise<void>;
  copy(id: number): Promise<void>;
  share(id: number): Promise<void>;
  clearNotice(): void;
  dispose(): void;
}

const clone = (row: AdminInvitationRecord): AdminInvitationRecord => ({ ...row });

export function createInvitationInventoryController(
  api: InvitationInventoryAPI,
  render: (snapshot: InvitationInventorySnapshot) => void,
  clipboard?: InvitationClipboardPort,
  sharePort?: InvitationSharePort,
): InvitationInventoryController {
  let rows: AdminInvitationRecord[] = [];
  let loading = false;
  let creating = false;
  let error: string | undefined;
  let notice: InvitationNotice | undefined;
  let generation = 0;
  let disposed = false;

  const publish = (): void => render({ rows: rows.map(clone), loading, creating, ...(error ? { error } : {}), ...(notice ? { notice: { ...notice } } : {}) });
  const rowFor = (id: number): AdminInvitationRecord | undefined => rows.find((row) => row.id === id);

  return {
    async reload(query = {}): Promise<void> {
      const current = ++generation;
      loading = true;
      error = undefined;
      publish();
      try {
        const page = await api.adminInvitations({ ...query });
        if (disposed || current !== generation) return;
        rows = page.invitations.map(clone);
      } catch {
        if (disposed || current !== generation) return;
        error = '邀请码列表加载失败，请重试';
      } finally {
        if (!disposed && current === generation) {
          loading = false;
          publish();
        }
      }
    },

    async create(count, validity): Promise<void> {
      if (!api.createAdminInvitations || creating || disposed) return;
      creating = true;
      error = undefined;
      publish();
      try {
        const created = await api.createAdminInvitations(count, validity);
        if (disposed) return;
        const createdIds = new Set(created.map((row) => row.id));
        rows = [...created.map(clone), ...rows.filter((row) => !createdIds.has(row.id))];
        const codes = created.flatMap((row) => row.code ? [row.code] : []);
        let copied = false;
        if (codes.length && clipboard) {
          try { await clipboard.writeText(codes.join('\n')); copied = true; } catch { /* The snapshot exposes a selectable fallback. */ }
        }
        const validityLabel = validity === 'permanent' ? '永久' : validity === '7d' ? '7 天' : '30 天';
        notice = copied
          ? { kind: 'success', message: `已创建 ${created.length} 个邀请码，已复制，有效期 ${validityLabel}` }
          : { kind: 'success', message: `已创建 ${created.length} 个邀请码，有效期 ${validityLabel}；请手动复制`, ...(codes.length ? { code: codes.join('\n') } : {}) };
      } catch {
        if (!disposed) notice = { kind: 'error', message: '邀请码创建失败，请重试' };
      } finally {
        if (!disposed) {
          creating = false;
          publish();
        }
      }
    },

    async revoke(id): Promise<void> {
      if (!api.revokeAdminInvitation || disposed) return;
      try {
        await api.revokeAdminInvitation(id);
        if (disposed) return;
        rows = rows.map((row) => row.id === id ? { ...row, status: 'revoked', code: undefined } : row);
        notice = { kind: 'success', message: '邀请码已作废' };
        publish();
      } catch {
        if (!disposed) {
          notice = { kind: 'error', message: '邀请码作废失败，请重试' };
          publish();
        }
      }
    },

    async copy(id): Promise<void> {
      const row = rowFor(id);
      if (!row?.code || !clipboard || disposed) return;
      try {
        await clipboard.writeText(row.code);
        if (!disposed) {
          notice = { kind: 'success', message: '邀请码已复制' };
          publish();
        }
      } catch {
        if (!disposed) {
          notice = { kind: 'error', message: '复制失败，请长按或拖选后复制', code: row.code };
          publish();
        }
      }
    },

    async share(id): Promise<void> {
      const row = rowFor(id);
      if (!row?.code || disposed) return;
      try {
        if (sharePort) await sharePort(row.code);
        else if (clipboard) await clipboard.writeText(row.code);
        if (!disposed) {
          notice = { kind: 'success', message: sharePort ? '邀请码已分享' : '邀请码已复制' };
          publish();
        }
      } catch (reason) {
        if (disposed || (reason instanceof DOMException && reason.name === 'AbortError')) return;
        notice = { kind: 'error', message: '分享失败，请重试' };
        publish();
      }
    },

    clearNotice(): void { if (!disposed) { notice = undefined; publish(); } },
    dispose(): void { disposed = true; generation++; rows = []; },
  };
}
