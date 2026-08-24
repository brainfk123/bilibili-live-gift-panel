import { describe, expect, it } from 'vitest';

import { HostedAPI } from '../src/hosted/api';

const json = (body: unknown, status = 200) => new Response(JSON.stringify(body), {
  status,
  headers: { 'Content-Type': 'application/json' },
});

const device = {
  id: '00112233445566778899aabbccddeeff',
  deviceLabel: 'iPhone · Safari',
  clientNetwork: '203.0.113.*',
  createdAt: '2026-08-23T08:00:00Z',
  lastSeenAt: '2026-08-23T08:10:00Z',
  expiresAt: '2026-09-22T08:00:00Z',
  current: true,
};

const login = {
  result: 'success',
  deviceLabel: 'iPhone · Safari',
  clientNetwork: '203.0.113.*',
  occurredAt: '2026-08-23T08:10:00Z',
};

describe('administrator settings API', () => {
  it('accepts only the redacted settings projection', async () => {
    const value = {
      maskedEmail: 'o***@example.com',
      sessionExpiresAt: '2026-09-22T00:00:00Z',
      totpEnabled: true,
      recoveryGeneratedAt: '2026-08-23T00:00:00Z',
      serviceHealth: 'healthy',
    };
    const api = await HostedAPI.connect(async (input) => input === '/api/bootstrap'
      ? json({ csrfToken: 'csrf' })
      : json(value));

    await expect(api.adminSettings()).resolves.toEqual(value);
  });

  it('rejects secrets and raw diagnostics', async () => {
    for (const value of [
      {
        maskedEmail: 'o***@example.com',
        sessionExpiresAt: '2026-09-22T00:00:00Z',
        totpEnabled: true,
        recoveryGeneratedAt: null,
        serviceHealth: 'healthy',
        smtpPassword: 'secret',
      },
      { database: 'ok', biliService: 'healthy', checkedAt: '2026-08-23T00:00:00Z', logs: ['raw'] },
    ]) {
      const api = await HostedAPI.connect(async (input) => input === '/api/bootstrap'
        ? json({ csrfToken: 'csrf' })
        : json(value));
      if ('database' in value) await expect(api.adminDiagnostics()).rejects.toMatchObject({ code: 'invalid_response' });
      else await expect(api.adminSettings()).rejects.toMatchObject({ code: 'invalid_response' });
    }
  });

  it('accepts only redacted device sessions and login records', async () => {
    const api = await HostedAPI.connect(async (input) => {
      if (input === '/api/bootstrap') return json({ csrfToken: 'csrf' });
      if (input === '/api/admin/sessions') return json({ sessions: [device] });
      return json({ events: [login] });
    });

    await expect(api.adminSessions()).resolves.toEqual([device]);
    await expect(api.adminLoginEvents()).resolves.toEqual([login]);
  });

  it('rejects extra secrets, full networks, invalid identifiers and non-RFC3339 timestamps', async () => {
    const invalidSessions = [
      { ...device, token: 'secret' },
      { ...device, clientNetwork: '203.0.113.42' },
      { ...device, clientNetwork: 'credential*' },
      { ...device, deviceLabel: 'Mozilla/5.0 raw-UA · Cookie=secret' },
      { ...device, id: '00112233445566778899AABBCCDDEEFF' },
      { ...device, lastSeenAt: 'August 23, 2026 08:10:00' },
    ];
    const invalidEvents = [
      { ...login, emailCode: '123456' },
      { ...login, clientNetwork: '2001:db8::1' },
      { ...login, result: 'unknown' },
      { ...login, occurredAt: '2026-08-23' },
    ];

    for (const session of invalidSessions) {
      const api = await HostedAPI.connect(async (input) => input === '/api/bootstrap'
        ? json({ csrfToken: 'csrf' })
        : json({ sessions: [session] }));
      await expect(api.adminSessions()).rejects.toMatchObject({ code: 'invalid_response' });
    }
    for (const event of invalidEvents) {
      const api = await HostedAPI.connect(async (input) => input === '/api/bootstrap'
        ? json({ csrfToken: 'csrf' })
        : json({ events: [event] }));
      await expect(api.adminLoginEvents()).rejects.toMatchObject({ code: 'invalid_response' });
    }
  });

  it('uses the bounded login limit and a CSRF-protected DELETE for one device', async () => {
    const requests: Array<{ input: string; init?: RequestInit }> = [];
    const api = await HostedAPI.connect(async (input, init) => {
      if (input === '/api/bootstrap') return json({ csrfToken: 'csrf' });
      requests.push({ input: String(input), init });
      if (String(input).startsWith('/api/admin/login-events')) return json({ events: [] });
      return new Response(null, { status: 204 });
    });

    await api.adminLoginEvents(12);
    await api.revokeAdminSession(device.id);

    expect(requests[0].input).toBe('/api/admin/login-events?limit=12');
    expect(requests[1]).toMatchObject({
      input: `/api/admin/sessions/${device.id}`,
      init: {
        method: 'DELETE',
        credentials: 'same-origin',
        headers: { Accept: 'application/json', 'X-CSRF-Token': 'csrf' },
      },
    });
  });
});
