import { describe, expect, it, vi } from 'vitest';

import { createInvitationInventoryController } from '../src/hosted/admin/invitations/controller';

const active = {
  id: 1,
  code: 'ABCDEFGH',
  codeHint: '****EFGH',
  status: 'active' as const,
  createdAt: '2026-08-23T00:00:00Z',
  expiresAt: null,
};

describe('administrator invitation inventory view state', () => {
  it('preserves the last successful rows when a refresh fails', async () => {
    const api = { adminInvitations: vi.fn() };
    const render = vi.fn();
    api.adminInvitations.mockResolvedValueOnce({ invitations: [active] });
    const controller = createInvitationInventoryController(api, render);

    await controller.reload();
    api.adminInvitations.mockRejectedValueOnce(new Error('offline'));
    await controller.reload();

    expect(render.mock.lastCall?.[0].rows).toEqual([active]);
    expect(render.mock.lastCall?.[0].error).toBe('邀请码列表加载失败，请重试');
  });

  it('does not reload or clear rows when copy and share are used', async () => {
    const api = { adminInvitations: vi.fn().mockResolvedValue({ invitations: [active] }) };
    const render = vi.fn();
    const clipboard = { writeText: vi.fn().mockResolvedValue(undefined) };
    const share = vi.fn().mockResolvedValue(undefined);
    const controller = createInvitationInventoryController(api, render, clipboard, share);

    await controller.reload();
    await controller.copy(active.id);
    await controller.share(active.id);

    expect(api.adminInvitations).toHaveBeenCalledTimes(1);
    expect(render.mock.lastCall?.[0].rows).toEqual([active]);
  });

  it('fences a stale reload response and keeps cloned inventory rows', async () => {
    let first!: (value: { invitations: typeof active[] }) => void;
    let second!: (value: { invitations: typeof active[] }) => void;
    const api = { adminInvitations: vi.fn()
      .mockImplementationOnce(() => new Promise((resolve) => { first = resolve; }))
      .mockImplementationOnce(() => new Promise((resolve) => { second = resolve; })) };
    const render = vi.fn();
    const controller = createInvitationInventoryController(api, render);
    const current = { ...active, id: 2, code: 'JKLMNPQR', codeHint: '****NPQR' };

    const oldLoad = controller.reload();
    const currentLoad = controller.reload();
    second({ invitations: [current] });
    await currentLoad;
    current.code = 'ABCDEFGH';
    first({ invitations: [active] });
    await oldLoad;

    expect(render.mock.lastCall?.[0].rows).toEqual([{ ...active, id: 2, code: 'JKLMNPQR', codeHint: '****NPQR' }]);
  });

  it('replaces only a revoked row and keeps that local state through a failed refresh', async () => {
    const api = {
      adminInvitations: vi.fn().mockResolvedValueOnce({ invitations: [active] }).mockRejectedValueOnce(new Error('offline')),
      revokeAdminInvitation: vi.fn().mockResolvedValue(undefined),
    };
    const render = vi.fn();
    const controller = createInvitationInventoryController(api, render);

    await controller.reload();
    await controller.revoke(active.id);
    await controller.reload();

    expect(render.mock.lastCall?.[0].rows).toEqual([{ ...active, code: undefined, status: 'revoked' }]);
    expect(render.mock.lastCall?.[0].error).toBe('邀请码列表加载失败，请重试');
  });

  it('merges newly created rows and reports the copied validity without reloading', async () => {
    const created = { ...active, id: 3, code: 'JKLMNPQR', codeHint: '****NPQR' };
    const api = { adminInvitations: vi.fn().mockResolvedValue({ invitations: [active] }), createAdminInvitations: vi.fn().mockResolvedValue([created]) };
    const render = vi.fn();
    const clipboard = { writeText: vi.fn().mockResolvedValue(undefined) };
    const controller = createInvitationInventoryController(api, render, clipboard);

    await controller.reload();
    await controller.create(1, '30d');

    expect(api.adminInvitations).toHaveBeenCalledTimes(1);
    expect(render.mock.lastCall?.[0].rows).toEqual([created, active]);
    expect(render.mock.lastCall?.[0].notice).toEqual({ kind: 'success', message: '已创建 1 个邀请码，已复制，有效期 30 天' });
  });
});
