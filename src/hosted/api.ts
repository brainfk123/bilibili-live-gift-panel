export type Fetcher = (input: RequestInfo | URL, init?: RequestInit) => Promise<Response>;

export interface Challenge {
  challengeId: string;
  qrImage: string;
  expiresAt: string;
}

export type PollResult =
  | { status: 'pending'; expiresAt: string }
  | { status: 'verified'; expiresAt: string }
  | { status: 'registration_required'; registrationIntent: string; expiresAt: string }
  | { status: 'expired' };

export interface InvitationRecord {
  id: number;
  codeHint: string;
  status: 'active' | 'revoked' | 'used' | 'expired';
  createdAt: string;
  expiresAt: string;
  revokedAt?: string;
  usedAt?: string;
}

export interface InvitationList {
  remainingQuota: number;
  invitations: InvitationRecord[];
}

export interface GeneratedInvitation extends InvitationRecord {
  code: string;
  remainingQuota?: number;
}

export interface ManagedAccount {
  accountId: number;
  status: 'active' | 'disabled';
}

export interface RecoveryPreparation {
  totpUri: string;
  recoveryPassword: string;
  handoffToken: string;
}

const stableErrors = new Set([
  'authentication_failed', 'authentication_required', 'invalid_request', 'operation_failed',
  'quota_exhausted', 'rate_limited', 'recent_totp_required', 'request_rejected',
  'temporarily_unavailable', 'verification_pending',
]);

export class HostedAPIError extends Error {
  constructor(readonly code: string, readonly status: number) {
    super(`Hosted request failed (${code}).`);
    this.name = 'HostedAPIError';
  }
}

function object(value: unknown): Record<string, unknown> | undefined {
  return typeof value === 'object' && value !== null && !Array.isArray(value)
    ? value as Record<string, unknown>
    : undefined;
}

function string(value: unknown): value is string {
  return typeof value === 'string' && value.length > 0;
}

function instant(value: unknown): value is string {
  return string(value) && Number.isFinite(Date.parse(value));
}

function number(value: unknown): value is number {
  return typeof value === 'number' && Number.isSafeInteger(value) && value >= 0;
}

function exactKeys(value: Record<string, unknown>, required: string[], optional: string[] = []): boolean {
  const allowed = new Set([...required, ...optional]);
  return required.every((key) => Object.hasOwn(value, key)) && Object.keys(value).every((key) => allowed.has(key));
}

function challenge(value: unknown): Challenge | undefined {
  const item = object(value);
  return item && exactKeys(item, ['challengeId', 'qrImage', 'expiresAt']) && string(item.challengeId) && string(item.qrImage) && instant(item.expiresAt)
    ? { challengeId: item.challengeId, qrImage: item.qrImage, expiresAt: item.expiresAt }
    : undefined;
}

function invitation(value: unknown, extraOptional: string[] = []): InvitationRecord | undefined {
  const item = object(value);
  if (!item || !exactKeys(item, ['id', 'codeHint', 'status', 'createdAt', 'expiresAt'], ['revokedAt', 'usedAt', ...extraOptional]) || !number(item.id) || item.id <= 0 || typeof item.codeHint !== 'string' || !/^\*{4}[A-Za-z0-9_-]{4}$/.test(item.codeHint) || !string(item.status) ||
    !['active', 'revoked', 'used', 'expired'].includes(item.status) || !instant(item.createdAt) || !instant(item.expiresAt)) return undefined;
  if (item.revokedAt !== undefined && !instant(item.revokedAt)) return undefined;
  if (item.usedAt !== undefined && !instant(item.usedAt)) return undefined;
  return item as unknown as InvitationRecord;
}

export class HostedAPI {
  private constructor(private readonly fetcher: Fetcher, private readonly csrfToken: string) {}

  static async connect(fetcher: Fetcher = globalThis.fetch.bind(globalThis)): Promise<HostedAPI> {
    const response = await fetcher('/api/bootstrap', {
      credentials: 'same-origin', headers: { Accept: 'application/json' }, method: 'GET',
    });
    const data = await HostedAPI.readJSON(response);
    const bootstrap = object(data);
    if (!response.ok || !bootstrap || Object.keys(bootstrap).length !== 1 || !string(bootstrap.csrfToken)) {
      throw new HostedAPIError('invalid_response', response.status);
    }
    return new HostedAPI(fetcher, bootstrap.csrfToken);
  }

  private static async readJSON(response: Response): Promise<unknown> {
    const mediaType = response.headers.get('Content-Type')?.split(';', 1)[0]?.trim().toLowerCase();
    if (mediaType !== 'application/json') throw new HostedAPIError('invalid_response', response.status);
    try { return await response.json(); } catch { throw new HostedAPIError('invalid_response', response.status); }
  }

  private async request(path: string, method: string, expectedStatus: number | readonly number[], body?: unknown): Promise<{ status: number; data: unknown }> {
    const mutation = method !== 'GET';
    const headers: Record<string, string> = { Accept: 'application/json' };
    if (mutation) headers['X-CSRF-Token'] = this.csrfToken;
    if (body !== undefined) headers['Content-Type'] = 'application/json';
    const response = await this.fetcher(path, {
      method, credentials: 'same-origin', headers,
      ...(body === undefined ? {} : { body: JSON.stringify(body) }),
    });
    const expected = Array.isArray(expectedStatus) ? expectedStatus : [expectedStatus];
    if (response.status === 204) {
      if (!expected.includes(response.status)) throw new HostedAPIError('invalid_response', response.status);
      return { status: response.status, data: undefined };
    }
    const data = await HostedAPI.readJSON(response);
    const errorBody = object(data);
    const error = errorBody?.error;
    if (errorBody && exactKeys(errorBody, ['error']) && string(error) && stableErrors.has(error)) {
      throw new HostedAPIError(error, response.status);
    }
    if (!expected.includes(response.status)) {
      throw new HostedAPIError('invalid_response', response.status);
    }
    return { status: response.status, data };
  }

  private requireChallenge(value: unknown): Challenge {
    const result = challenge(value);
    if (!result) throw new HostedAPIError('invalid_response', 200);
    return result;
  }

  async beginLogin(): Promise<Challenge> { return this.requireChallenge((await this.request('/api/auth/bili/challenges', 'POST', 201)).data); }
  async cancelLogin(id: string): Promise<void> { await this.request(`/api/auth/bili/challenges/${encodeURIComponent(id)}`, 'DELETE', 204); }
  async createSession(challengeId: string): Promise<void> { await this.request('/api/auth/session', 'POST', 204, { challengeId }); }
  async logout(): Promise<void> { await this.request('/api/auth/session', 'DELETE', 204); }
  async session(): Promise<boolean> {
    const data = object((await this.request('/api/auth/session', 'GET', 200)).data);
    if (!data || !exactKeys(data, ['authenticated']) || data.authenticated !== true) throw new HostedAPIError('invalid_response', 200);
    return true;
  }
  async pollLogin(id: string): Promise<PollResult> {
    try {
      const response = await this.request(`/api/auth/bili/challenges/${encodeURIComponent(id)}`, 'GET', [200, 410]);
      const data = object(response.data);
      if (!data || !string(data.status)) throw new HostedAPIError('invalid_response', 200);
      if (response.status === 410) {
        if (exactKeys(data, ['status']) && data.status === 'expired') return { status: 'expired' };
        throw new HostedAPIError('invalid_response', response.status);
      }
      if (data.status === 'pending' && exactKeys(data, ['status', 'expiresAt']) && instant(data.expiresAt)) return data as PollResult;
      if (data.status === 'verified' && exactKeys(data, ['status', 'expiresAt']) && instant(data.expiresAt)) return data as PollResult;
      if (data.status === 'registration_required' && exactKeys(data, ['status', 'registrationIntent', 'expiresAt']) && string(data.registrationIntent) && instant(data.expiresAt)) return data as PollResult;
      throw new HostedAPIError('invalid_response', 200);
    } catch (error) { throw error; }
  }

  async redeemInvitation(code: string, registrationIntent: string): Promise<void> { await this.request('/api/auth/registration', 'POST', 204, { code, registrationIntent }); }
  async listInvitations(): Promise<InvitationList> {
    const data = object((await this.request('/api/invitations', 'GET', 200)).data);
    const rows = Array.isArray(data?.invitations) ? data.invitations.map((item) => invitation(item)) : [];
    if (!data || !exactKeys(data, ['remainingQuota', 'invitations']) || !Array.isArray(data.invitations) || !number(data.remainingQuota) || rows.some((row) => !row)) throw new HostedAPIError('invalid_response', 200);
    return { remainingQuota: data.remainingQuota, invitations: rows as InvitationRecord[] };
  }
  async generateInvitation(admin = false): Promise<GeneratedInvitation> {
    const data = object((await this.request(admin ? '/api/admin/invitations' : '/api/invitations', 'POST', 201, {})).data);
    const record = invitation(data, ['code', 'remainingQuota']);
    if (!data || !exactKeys(data, ['id', 'codeHint', 'status', 'createdAt', 'expiresAt', 'code'], ['revokedAt', 'usedAt', 'remainingQuota']) || !record || !string(data.code) || (data.remainingQuota !== undefined && !number(data.remainingQuota))) throw new HostedAPIError('invalid_response', 200);
    const remainingQuota = data.remainingQuota === undefined ? (admin ? undefined : 0) : data.remainingQuota as number;
    return { ...record, code: data.code, ...(remainingQuota === undefined ? {} : { remainingQuota }) };
  }
  async revokeInvitation(id: number): Promise<void> { await this.request(`/api/invitations/${id}`, 'DELETE', 204); }

  async beginAdminProof(): Promise<Challenge> { return this.requireChallenge((await this.request('/api/admin/auth/bili/challenges', 'POST', 201)).data); }
  async cancelAdminProof(id: string): Promise<void> { await this.request(`/api/admin/auth/bili/challenges/${encodeURIComponent(id)}`, 'DELETE', 204); }
  async adminLogin(challengeId: string, totp: string): Promise<void> { await this.request('/api/admin/session', 'POST', 204, { challengeId, totp }); }
  async verifyRecentTOTP(totp: string): Promise<void> { await this.request('/api/admin/totp', 'POST', 204, { totp }); }
  async sendRecoveryArchive(): Promise<{ recoveryPassword: string }> {
    const data = object((await this.request('/api/admin/recovery/archive', 'POST', 200, {})).data);
    if (!data || Object.keys(data).length !== 1 || !string(data.recoveryPassword) || data.recoveryPassword.length !== 20) throw new HostedAPIError('invalid_response', 200);
    return { recoveryPassword: data.recoveryPassword };
  }
  async prepareRecovery(challengeId: string, recoveryCode: string): Promise<RecoveryPreparation> {
    const data = object((await this.request('/api/admin/recovery/prepare', 'POST', 200, { challengeId, recoveryCode })).data);
    if (!data || !exactKeys(data, ['totpUri', 'recoveryPassword', 'handoffToken']) || !string(data.totpUri) || !data.totpUri.startsWith('otpauth://') || !string(data.recoveryPassword) || data.recoveryPassword.length !== 20 || !string(data.handoffToken)) throw new HostedAPIError('invalid_response', 200);
    return { totpUri: data.totpUri, recoveryPassword: data.recoveryPassword, handoffToken: data.handoffToken };
  }
  async confirmRecovery(handoffToken: string, totp: string): Promise<void> { await this.request('/api/admin/recovery/confirm', 'POST', 204, { handoffToken, totp }); }
  async adjustQuota(accountId: number, remainingQuota: number, reason: string): Promise<void> {
    const data = object((await this.request(`/api/admin/accounts/${accountId}/invitation-quota`, 'POST', 200, { remainingQuota, reason })).data);
    if (!data || !exactKeys(data, ['accountId', 'remainingQuota']) || data.accountId !== accountId || data.remainingQuota !== remainingQuota) throw new HostedAPIError('invalid_response', 200);
  }
  async disableAccount(accountId: number, reason: string): Promise<ManagedAccount> { return this.accountMutation(accountId, 'disable', ['disabled'], { reason }); }
  async enableAccount(accountId: number, reason: string): Promise<ManagedAccount> { return this.accountMutation(accountId, 'enable', ['active'], { reason }); }
  async rebindAccount(accountId: number, challengeId: string, reason: string): Promise<ManagedAccount> { return this.accountMutation(accountId, 'rebind', ['active', 'disabled'], { challengeId, reason }); }
  private async accountMutation(accountId: number, action: string, expectedAccountStatuses: readonly ManagedAccount['status'][], body: unknown): Promise<ManagedAccount> {
    const data = object((await this.request(`/api/admin/accounts/${accountId}/${action}`, 'POST', 200, body)).data);
    const status = expectedAccountStatuses.find((expected) => data?.status === expected);
    if (!data || !exactKeys(data, ['accountId', 'status']) || data.accountId !== accountId || !status) throw new HostedAPIError('invalid_response', 200);
    return { accountId, status };
  }
}
