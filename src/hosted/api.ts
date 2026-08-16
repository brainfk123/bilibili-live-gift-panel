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

export type HostedConfigurationDefinition = Record<string, unknown>;
export type HostedConfigurationRuntime = Record<string, unknown>;

export interface HostedConfiguration {
  definition: HostedConfigurationDefinition;
  runtime: HostedConfigurationRuntime;
  version: number;
  revision: number;
}

export interface MigrationCounts {
  attributes: number;
  rules: number;
  activities: number;
  giftTargetPanels: number;
  giftTargetItems: number;
}

export interface MigrationSource {
  appVersion: string;
  configurationSchemaVersion: number;
}

export interface MigrationPreview {
  id: number;
  expiresAt: string;
  reused: boolean;
  counts: MigrationCounts;
  warnings?: string[];
  ignored?: string[];
  roomSuggestion?: string;
  source: MigrationSource;
}

export interface MigrationJob {
  id: number;
  status: 'previewed' | 'pending' | 'applied' | 'cancelled' | 'rolled_back' | 'expired';
  expiresAt?: string;
  rollbackExpiresAt?: string;
}

const stableErrors = new Set([
  'authentication_failed', 'authentication_required', 'invalid_request', 'operation_failed',
  'quota_exhausted', 'rate_limited', 'recent_totp_required', 'request_rejected',
  'temporarily_unavailable', 'verification_pending', 'revision_conflict', 'not_found',
  'proof_rejected', 'expired', 'operation_conflict',
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

function safeObject(value: unknown): value is Record<string, unknown> {
  return object(value) !== undefined;
}

function nonEmptyStringArray(value: unknown): value is string[] {
  return Array.isArray(value) && value.every((item) => typeof item === 'string' && item.length <= 4096);
}

function finite(value: unknown): value is number { return typeof value === 'number' && Number.isFinite(value); }
function strings(value: unknown): value is string[] { return Array.isArray(value) && value.every((item) => typeof item === 'string'); }
function integers(value: unknown): value is number[] { return Array.isArray(value) && value.every((item) => number(item)); }
function arrayOf(value: unknown, valid: (item: unknown) => boolean): boolean { return Array.isArray(value) && value.every(valid); }
function numberMap(value: unknown, integer = false): boolean { const item = object(value); return item !== undefined && Object.values(item).every((entry) => integer ? number(entry) : finite(entry)); }
function optional(value: unknown, valid: (item: unknown) => boolean): boolean { return value === undefined || valid(value); }
function display(value: unknown): boolean {
  const item = object(value); return item !== undefined && exactKeys(item, ['variant'], ['themeId', 'title', 'min', 'max', 'lowThreshold', 'leftLabel', 'rightLabel', 'valueMappings']) && string(item.variant) && optional(item.themeId, string) && optional(item.title, string) && optional(item.min, finite) && optional(item.max, finite) && optional(item.lowThreshold, finite) && optional(item.leftLabel, string) && optional(item.rightLabel, string) && optional(item.valueMappings, (items) => arrayOf(items, (entry) => { const mapping = object(entry); return mapping !== undefined && exactKeys(mapping, ['value', 'label'], ['color']) && finite(mapping.value) && string(mapping.label) && optional(mapping.color, string); }));
}
function attributeDefinition(value: unknown): boolean {
  const item = object(value); return item !== undefined && exactKeys(item, ['id', 'name', 'unit', 'format', 'decimals', 'suffix'], ['color', 'broadcastMessage', 'display']) && string(item.id) && string(item.name) && string(item.unit) && string(item.format) && Number.isSafeInteger(item.decimals) && string(item.suffix) && optional(item.color, string) && optional(item.broadcastMessage, string) && optional(item.display, display);
}
function definition(value: unknown): value is HostedConfigurationDefinition {
  const item = object(value);
  if (!item || !exactKeys(item, ['attributes', 'displayScenes', 'giftTargetPanels', 'activities', 'rules', 'timerRules', 'formulaPresets', 'gifts'], ['simplePlay']) || !arrayOf(item.attributes, attributeDefinition)) return false;
  const scene = (entry: unknown): boolean => { const row = object(entry); return row !== undefined && exactKeys(row, ['id', 'name', 'attributeIds', 'layout', 'themeId']) && string(row.id) && string(row.name) && strings(row.attributeIds) && string(row.layout) && string(row.themeId); };
  const panel = (entry: unknown): boolean => { const row = object(entry); return row !== undefined && exactKeys(row, ['id', 'name', 'layout', 'items']) && string(row.id) && string(row.name) && string(row.layout) && arrayOf(row.items, (child) => { const item = object(child); return item !== undefined && exactKeys(item, ['giftId', 'target', 'barStyle'], ['name']) && number(item.giftId) && number(item.target) && string(item.barStyle) && optional(item.name, string); }); };
  const milestone = (entry: unknown): boolean => { const row = object(entry); return row !== undefined && exactKeys(row, ['id', 'name', 'attributeId', 'comparison', 'threshold', 'action', 'message']) && string(row.id) && string(row.name) && string(row.attributeId) && string(row.comparison) && finite(row.threshold) && string(row.action) && string(row.message); };
  const activity = (entry: unknown): boolean => { const row = object(entry); return row !== undefined && exactKeys(row, ['id', 'name', 'attributeIds', 'resultMode', 'gateRules', 'initialValues', 'milestones'], ['sceneId', 'giftTimeout']) && string(row.id) && string(row.name) && strings(row.attributeIds) && string(row.resultMode) && typeof row.gateRules === 'boolean' && numberMap(row.initialValues) && arrayOf(row.milestones, milestone) && optional(row.sceneId, string) && optional(row.giftTimeout, (timeout) => { const item = object(timeout); return item !== undefined && exactKeys(item, ['seconds', 'action']) && number(item.seconds) && string(item.action); }); };
  const rule = (entry: unknown): boolean => { const row = object(entry); return row !== undefined && exactKeys(row, ['id', 'giftId', 'attributeId', 'formula'], ['formulaName', 'condition', 'enabled', 'matchGiftIds', 'minPrice', 'cap', 'dailyLimit']) && string(row.id) && number(row.giftId) && string(row.attributeId) && string(row.formula) && optional(row.formulaName, string) && optional(row.condition, string) && optional(row.enabled, (enabled) => typeof enabled === 'boolean') && optional(row.matchGiftIds, integers) && optional(row.minPrice, finite) && optional(row.cap, finite) && optional(row.dailyLimit, number); };
  const timerRule = (entry: unknown): boolean => { const row = object(entry); return row !== undefined && exactKeys(row, ['id', 'attributeId', 'formulaName', 'intervalSeconds', 'formula', 'enabled'], ['condition']) && string(row.id) && string(row.attributeId) && string(row.formulaName) && number(row.intervalSeconds) && string(row.formula) && typeof row.enabled === 'boolean' && optional(row.condition, string); };
  const preset = (entry: unknown): boolean => { const row = object(entry); return row !== undefined && exactKeys(row, ['id', 'name', 'context', 'formula', 'attributeId']) && string(row.id) && string(row.name) && string(row.context) && string(row.formula) && string(row.attributeId); };
  const gift = (entry: unknown): boolean => { const row = object(entry); return row !== undefined && exactKeys(row, ['id', 'name', 'price', 'coinType'], ['blindBoxParentId', 'blindBoxParentName', 'blindBoxParentPrice']) && number(row.id) && string(row.name) && finite(row.price) && string(row.coinType) && optional(row.blindBoxParentId, number) && optional(row.blindBoxParentName, string) && optional(row.blindBoxParentPrice, finite); };
  const simplePlay = (entry: unknown): boolean => { const row = object(entry); return row !== undefined && exactKeys(row, ['version', 'templateId', 'templateVersion', 'attributeId', 'parameters', 'gifts', 'managedFingerprint'], ['overtimeGiftActions']) && Number.isSafeInteger(row.version) && string(row.templateId) && Number.isSafeInteger(row.templateVersion) && string(row.attributeId) && safeObject(row.parameters) && safeObject(row.gifts) && string(row.managedFingerprint) && Object.values(row.gifts).every(integers) && optional(row.overtimeGiftActions, (actions) => arrayOf(actions, (action) => { const item = object(action); return item !== undefined && exactKeys(item, ['giftId', 'operation'], ['seconds']) && number(item.giftId) && string(item.operation) && optional(item.seconds, number); })); };
  return arrayOf(item.displayScenes, scene) && arrayOf(item.giftTargetPanels, panel) && arrayOf(item.activities, activity) && arrayOf(item.rules, rule) && arrayOf(item.timerRules, timerRule) && arrayOf(item.formulaPresets, preset) && arrayOf(item.gifts, gift) && optional(item.simplePlay, simplePlay);
}
function runtime(value: unknown): value is HostedConfigurationRuntime {
  const item = object(value);
  const activity = (entry: unknown): boolean => { const row = object(entry); return row !== undefined && exactKeys(row, ['id', 'status', 'milestones'], ['startedAtMillis', 'lockedAtMillis', 'settledAtMillis', 'result', 'giftTimeout']) && string(row.id) && string(row.status) && arrayOf(row.milestones, (milestone) => { const item = object(milestone); return item !== undefined && exactKeys(item, ['id'], ['triggeredAtMillis', 'triggerValue']) && string(item.id) && optional(item.triggeredAtMillis, (time) => typeof time === 'number' && Number.isSafeInteger(time)) && optional(item.triggerValue, finite); }) && optional(row.startedAtMillis, (time) => typeof time === 'number' && Number.isSafeInteger(time)) && optional(row.lockedAtMillis, (time) => typeof time === 'number' && Number.isSafeInteger(time)) && optional(row.settledAtMillis, (time) => typeof time === 'number' && Number.isSafeInteger(time)) && optional(row.result, (result) => { const item = object(result); return item !== undefined && exactKeys(item, ['values'], ['winnerAttributeId']) && numberMap(item.values) && optional(item.winnerAttributeId, string); }) && optional(row.giftTimeout, (timeout) => { const item = object(timeout); return item !== undefined && exactKeys(item, ['lastGiftAtMillis', 'deadlineAtMillis']) && typeof item.lastGiftAtMillis === 'number' && Number.isSafeInteger(item.lastGiftAtMillis) && typeof item.deadlineAtMillis === 'number' && Number.isSafeInteger(item.deadlineAtMillis); }); };
  return item !== undefined && exactKeys(item, ['attributeValues', 'giftTargetReceived', 'activities', 'ruleLimits']) && numberMap(item.attributeValues) && arrayOf(item.giftTargetReceived, (entry) => { const row = object(entry); return row !== undefined && exactKeys(row, ['panelId', 'giftId', 'received']) && string(row.panelId) && number(row.giftId) && number(row.received); }) && arrayOf(item.activities, activity) && (() => { const limits = object(item.ruleLimits); return limits !== undefined && exactKeys(limits, ['localDate', 'appliedCounts']) && string(limits.localDate) && numberMap(limits.appliedCounts, true); })();
}

function configuration(value: unknown): HostedConfiguration | undefined {
  const item = object(value);
  if (!item || !exactKeys(item, ['definition', 'runtime', 'version', 'revision']) || !safeObject(item.definition) || !safeObject(item.runtime) || !number(item.version) || !number(item.revision)) return undefined;
  if (!definition(item.definition) || !runtime(item.runtime)) return undefined;
  return { definition: item.definition, runtime: item.runtime, version: item.version, revision: item.revision };
}

function migrationCounts(value: unknown): MigrationCounts | undefined {
  const item = object(value);
  return item && exactKeys(item, ['attributes', 'rules', 'activities', 'giftTargetPanels', 'giftTargetItems']) && number(item.attributes) && number(item.rules) && number(item.activities) && number(item.giftTargetPanels) && number(item.giftTargetItems)
    ? item as unknown as MigrationCounts : undefined;
}

function migrationSource(value: unknown): MigrationSource | undefined {
  const item = object(value);
  return item && exactKeys(item, ['appVersion', 'configurationSchemaVersion']) && string(item.appVersion) && number(item.configurationSchemaVersion)
    ? item as unknown as MigrationSource : undefined;
}

function migrationPreview(value: unknown): MigrationPreview | undefined {
  const item = object(value);
  const counts = migrationCounts(item?.counts); const source = migrationSource(item?.source);
  if (!item || !exactKeys(item, ['id', 'expiresAt', 'reused', 'counts', 'source'], ['warnings', 'ignored', 'roomSuggestion']) || !number(item.id) || item.id === 0 || !instant(item.expiresAt) || typeof item.reused !== 'boolean' || !counts || !source || (item.warnings !== undefined && !nonEmptyStringArray(item.warnings)) || (item.ignored !== undefined && !nonEmptyStringArray(item.ignored)) || (item.roomSuggestion !== undefined && !string(item.roomSuggestion))) return undefined;
  return { id: item.id, expiresAt: item.expiresAt, reused: item.reused, counts, source, ...(item.warnings ? { warnings: item.warnings } : {}), ...(item.ignored ? { ignored: item.ignored } : {}), ...(item.roomSuggestion ? { roomSuggestion: item.roomSuggestion } : {}) };
}

function migrationJob(value: unknown): MigrationJob | undefined {
  const item = object(value);
  if (!item || !exactKeys(item, ['id', 'status'], ['expiresAt', 'rollbackExpiresAt']) || !number(item.id) || item.id === 0 || !string(item.status) || !['previewed', 'pending', 'applied', 'cancelled', 'rolled_back', 'expired'].includes(item.status) || (item.expiresAt !== undefined && !instant(item.expiresAt)) || (item.rollbackExpiresAt !== undefined && !instant(item.rollbackExpiresAt))) return undefined;
  return item as unknown as MigrationJob;
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

  private async requestRawJSON(path: string, expectedStatus: number | readonly number[], body: string): Promise<{ status: number; data: unknown }> {
    const response = await this.fetcher(path, {
      method: 'POST', credentials: 'same-origin', headers: { Accept: 'application/json', 'Content-Type': 'application/json', 'X-CSRF-Token': this.csrfToken }, body,
    });
    const data = await HostedAPI.readJSON(response);
    const errorBody = object(data);
    if (errorBody && exactKeys(errorBody, ['error']) && string(errorBody.error) && stableErrors.has(errorBody.error)) throw new HostedAPIError(errorBody.error, response.status);
    if (!(Array.isArray(expectedStatus) ? expectedStatus : [expectedStatus]).includes(response.status)) throw new HostedAPIError('invalid_response', response.status);
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

  async loadConfiguration(): Promise<HostedConfiguration> {
    const result = configuration((await this.request('/api/configuration', 'GET', 200)).data);
    if (!result) throw new HostedAPIError('invalid_response', 200);
    return result;
  }
  async saveConfigurationDefinition(expectedVersion: number, definition: HostedConfigurationDefinition): Promise<{ version: number; revision: number }> {
    const data = object((await this.request('/api/configuration/definition', 'PUT', 200, { expectedVersion, definition })).data);
    if (!data || !exactKeys(data, ['version', 'revision']) || !number(data.version) || !number(data.revision)) throw new HostedAPIError('invalid_response', 200);
    return { version: data.version, revision: data.revision };
  }
  async saveConfigurationRuntime(expectedRevision: number, runtime: HostedConfigurationRuntime): Promise<{ revision: number }> {
    const data = object((await this.request('/api/configuration/state', 'PUT', 200, { expectedRevision, runtime })).data);
    if (!data || !exactKeys(data, ['revision']) || !number(data.revision)) throw new HostedAPIError('invalid_response', 200);
    return { revision: data.revision };
  }
  async suggestRoom(roomId: string): Promise<void> { await this.request('/api/configuration/room-suggestion', 'PUT', 204, { roomId }); }

  async previewMigration(rawJSON: string): Promise<MigrationPreview> {
    const result = migrationPreview((await this.requestRawJSON('/api/migrations/preview', 201, rawJSON)).data);
    if (!result) throw new HostedAPIError('invalid_response', 201);
    return result;
  }
  async getMigration(id: number): Promise<MigrationJob> { return this.requireMigrationJob((await this.request(`/api/migrations/${id}`, 'GET', 200)).data); }
  async applyMigration(id: number, challengeId: string, keepRoomSuggestion: boolean): Promise<MigrationJob> { return this.requireMigrationJob((await this.request(`/api/migrations/${id}/apply`, 'POST', 200, { challengeId, keepRoomSuggestion })).data); }
  async cancelMigration(id: number): Promise<MigrationJob> { return this.requireMigrationJob((await this.request(`/api/migrations/${id}`, 'DELETE', 200)).data); }
  async rollbackMigration(id: number, challengeId: string): Promise<MigrationJob> { return this.requireMigrationJob((await this.request(`/api/migrations/${id}/rollback`, 'POST', 200, { challengeId })).data); }
  private requireMigrationJob(value: unknown): MigrationJob {
    const result = migrationJob(value);
    if (!result) throw new HostedAPIError('invalid_response', 200);
    return result;
  }

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
