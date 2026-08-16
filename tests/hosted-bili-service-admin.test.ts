import { describe, expect, it, vi } from 'vitest';
import { HostedAPI, HostedAPIError } from '../src/hosted/api';
import { biliServiceStatusText, createBiliServiceFlow } from '../src/hosted/admin';

function json(body: unknown, status = 200): Response { return new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } }); }

describe('controlled Bilibili service credential', () => {
  it('uses only the exact administrator status DTO and never accepts account or credential fields', async () => {
    const status = { version: 3, health: 'healthy', lastVerifiedAt: '2030-01-01T00:00:00Z' };
    const connect = async (body: unknown) => HostedAPI.connect(async (input) => input === '/api/bootstrap' ? json({ csrfToken: 'csrf' }) : json(body));
    await expect((await connect(status)).biliServiceStatus()).resolves.toEqual(status);
    await expect((await connect({ version: 0, health: 'missing' })).biliServiceStatus()).resolves.toEqual({ version: 0, health: 'missing' });
    await expect((await connect({ version: 0, health: 'unavailable' })).biliServiceStatus()).resolves.toEqual({ version: 0, health: 'unavailable' });
    await expect((await connect({ version: 0, health: 'healthy' })).biliServiceStatus()).rejects.toMatchObject({ code: 'invalid_response' });
    await expect((await connect({ version: 3, health: 'healthy' })).biliServiceStatus()).rejects.toMatchObject({ code: 'invalid_response' });
    await expect((await connect({ version: 0, health: 'missing', lastVerifiedAt: '2030-01-01T00:00:00Z' })).biliServiceStatus()).rejects.toMatchObject({ code: 'invalid_response' });
    await expect((await connect({ ...status, accountId: 7 })).biliServiceStatus()).rejects.toMatchObject({ code: 'invalid_response' });
    await expect((await connect({ ...status, health: 'degraded' })).biliServiceStatus()).rejects.toMatchObject({ code: 'invalid_response' });
  });

  it('renders missing and unavailable service states without a synthetic verification time', () => {
    expect(biliServiceStatusText({ version: 0, health: 'missing' })).toBe('服务账号未配置');
    expect(biliServiceStatusText({ version: 0, health: 'unavailable' })).toBe('服务账号状态暂不可用');
    expect(biliServiceStatusText({ version: 3, health: 'healthy', lastVerifiedAt: '2030-01-01T00:00:00Z' })).toContain('最近验证 2030-01-01T00:00:00Z');
  });

  it('starts a QR challenge and replaces only with its opaque challenge ID', async () => {
    const requests: Array<[RequestInfo | URL, RequestInit | undefined]> = [];
    const api = await HostedAPI.connect(async (input, init) => {
      requests.push([input, init]);
      if (input === '/api/bootstrap') return json({ csrfToken: 'csrf' });
      if (input === '/api/admin/bili-service/challenge') return json({ challengeId: 'service-challenge', qrImage: 'data:image/png;base64,qr', expiresAt: '2030-01-01T00:00:00Z' }, 201);
      return new Response(null, { status: 204 });
    });
    const challenge = await api.beginBiliServiceChallenge();
    await api.replaceBiliServiceCredential(challenge.challengeId);
    expect(requests[1]).toEqual(['/api/admin/bili-service/challenge', expect.objectContaining({ method: 'POST', headers: expect.not.objectContaining({ 'Content-Type': expect.anything() }) })]);
    expect(requests[2]).toEqual(['/api/admin/bili-service/replace', expect.objectContaining({ method: 'POST', body: JSON.stringify({ challengeId: 'service-challenge' }) })]);
    expect(JSON.stringify(requests)).not.toContain('SESSDATA');
  });

  it('uses recent TOTP only after the server requires it and never stores a Cookie in view state', async () => {
    const calls: string[] = [];
    const flow = createBiliServiceFlow({
      beginBiliServiceChallenge: async () => ({ challengeId: 'challenge', qrImage: 'qr', expiresAt: '2030-01-01T00:00:00Z' }),
      replaceBiliServiceCredential: async () => { calls.push('replace'); if (calls.length === 1) throw new HostedAPIError('recent_totp_required', 403); },
      verifyRecentTOTP: async () => { calls.push('totp'); },
    });
    await flow.begin();
    await flow.replace('123456');
    expect(calls).toEqual(['replace', 'totp', 'replace']);
    expect(JSON.stringify(flow.state())).not.toContain('SESSDATA');
  });

  it('single-flights service challenge and replace operations, exposes busy state, and drops a late challenge', async () => {
    let release!: (value: { challengeId: string; qrImage: string; expiresAt: string }) => void;
    const pending = new Promise<{ challengeId: string; qrImage: string; expiresAt: string }>((resolve) => { release = resolve; });
    const api = { beginBiliServiceChallenge: vi.fn(() => pending), replaceBiliServiceCredential: vi.fn(async () => undefined), verifyRecentTOTP: vi.fn(async () => undefined) };
    const flow = createBiliServiceFlow(api); const first = flow.begin(); const duplicate = flow.begin();
    expect(flow.state()).toEqual({ busy: true }); await vi.waitFor(() => expect(api.beginBiliServiceChallenge).toHaveBeenCalledTimes(1)); release({ challengeId: 'service', qrImage: 'qr', expiresAt: '2030-01-01T00:00:00Z' });
    await expect(first).resolves.toMatchObject({ challengeId: 'service' }); await expect(duplicate).resolves.toMatchObject({ challengeId: 'service' }); expect(flow.state()).toEqual(expect.objectContaining({ busy: false, challenge: expect.objectContaining({ qrImage: 'qr' }) }));
    const replace = flow.replace('123456'); const replaceDuplicate = flow.replace('123456'); await Promise.all([replace, replaceDuplicate]); expect(api.replaceBiliServiceCredential).toHaveBeenCalledTimes(1); expect(flow.state()).toEqual({ busy: false });
    const late = createBiliServiceFlow(api); const waiting = late.begin(); late.dispose(); await expect(waiting).rejects.toMatchObject({ code: 'operation_failed' }); expect(late.state()).toEqual({ busy: false });
  });

  it('does not treat an untyped error as a recent-TOTP challenge', async () => {
    const api = { beginBiliServiceChallenge: async () => ({ challengeId: 'challenge', qrImage: 'qr', expiresAt: '2030-01-01T00:00:00Z' }), replaceBiliServiceCredential: async () => { const error = new Error('untrusted'); Object.assign(error, { code: 'recent_totp_required' }); throw error; }, verifyRecentTOTP: vi.fn(async () => undefined) };
    const flow = createBiliServiceFlow(api); await flow.begin(); await expect(flow.replace('123456')).rejects.toThrow('untrusted'); expect(api.verifyRecentTOTP).not.toHaveBeenCalled();
  });
});
