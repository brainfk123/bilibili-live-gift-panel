import { GAMEPLAY_TEMPLATES, type TemplateGiftSlotDefinition, type TemplateParameterDefinition } from '../gameplay-templates';
import { validHostedRoomID } from './room-id';

export type Fetcher = (input: RequestInfo | URL, init?: RequestInit) => Promise<Response>;

const obsPublicIDPattern = /^[A-Za-z0-9_-]{43}$/;

export interface Challenge {
  challengeId: string;
  qrImage: string;
  verificationUrl?: string;
  expiresAt: string;
}

export interface EmailLoginChallenge { challengeId: string; expiresAt: string }
export type BiliServiceStatus =
  | { version: number; health: 'healthy'; maskedUid?: string; lastVerifiedAt: string; lastReplacedAt?:string }
  | { version: 0; health: 'missing' | 'unavailable' };

export type PollResult =
  | { status: 'pending'; expiresAt: string }
  | { status: 'scanned'; expiresAt: string }
  | { status: 'verified'; expiresAt: string }
  | { status: 'registration_required'; registrationIntent: string; expiresAt: string }
  | { status: 'expired' };

export type BiliServiceChallengeStage = 'pending' | 'scanned' | 'verified';

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

export interface OBSCredentialAccess {
  publicId: string;
  url: string;
}

export type AdminAccountStatus = 'active' | 'disabled';
export type AdminAttentionKind = 'missing_room' | 'missing_obs';
export interface AdminEvent { type: string; text: string; accountId?: number; createdAt: string }
export interface AdminAttentionItem { kind: AdminAttentionKind; accountId: number; text: string; priority: number }
export interface AdminOverview { totalAccounts: number; activeAccounts: number; disabledAccounts: number; missingRooms: number; missingObs: number; attention: AdminAttentionItem[]; recentEvents: AdminEvent[] }
export interface AdminAccountSummary { id: number; status: AdminAccountStatus; roomId?: string; invitationQuota: number; hasObs: boolean; createdAt: string; updatedAt: string }
export interface AdminAccountDetail extends AdminAccountSummary { obsUrl?: string; recentEvents: AdminEvent[] }
export interface AdminAccountPage { items: AdminAccountSummary[]; nextCursor?: string }
export type AdminBatchAction = 'enable' | 'disable' | 'set_invitation_quota';
export interface AdminBatchResult { accountId: number; status: 'succeeded' | 'failed'; accountStatus?: AdminAccountStatus; error?: string }
export type AdminInvitationStatus='active'|'used'|'revoked'|'expired';
export interface AdminInvitationRecord { id:number;code?:string;codeHint:string;status:AdminInvitationStatus;createdAt:string;expiresAt:string|null;usedByAccountId?:number }
export interface AdminInvitationPage { invitations:AdminInvitationRecord[];nextCursor?:string }
export interface AdminInvitationQuery { query?:string;status?:AdminInvitationStatus;sort?:'status'|'created_at';direction?:'asc'|'desc';cursor?:string;limit?:number }
export interface AdminSettings{maskedEmail:string;sessionExpiresAt:string;totpEnabled:boolean;recoveryGeneratedAt:string|null;serviceHealth:string}
export interface AdminDiagnostic{database:string;biliService:string;checkedAt:string}
export interface AdminDeviceSession {
  id: string;
  deviceLabel: string;
  clientNetwork: string;
  createdAt: string;
  lastSeenAt: string;
  expiresAt: string;
  current: boolean;
}
export interface AdminLoginEvent {
  result: 'success' | 'failure';
  deviceLabel: string;
  clientNetwork: string;
  occurredAt: string;
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

export type MigrationCompatibilityStatus = 'complete' | 'partial' | 'incompatible';
export type MigrationConflictChoice = 'replace' | 'keep_both' | 'skip';

export interface MigrationCompatibility {
  status: MigrationCompatibilityStatus;
  reasonCodes: string[];
}

export interface MigrationUnit {
  id: string;
  kind: string;
  name: string;
  attributeIds: string[];
  ruleIds: string[];
  timerRuleIds: string[];
  formulaPresetIds: string[];
  activityIds: string[];
  displaySceneIds: string[];
  giftTargetPanelIds: string[];
  giftIds: number[];
  cropPresetIds: string[];
  compatibility: MigrationCompatibility;
  selected: boolean;
}

export interface MigrationGroupReason { kind: string; referenceId: string }
export interface MigrationGroup { id: string; unitIds: string[]; reasons: MigrationGroupReason[] }
export interface MigrationConflict { id: string; importedUnitIds: string[]; hostedUnitIds: string[]; suggestedNames: Record<string, string> }
export interface MigrationSelection {
  unitIds: string[];
  conflictChoices: Record<string, MigrationConflictChoice>;
  includeGeneralSettings: boolean;
  includeRoomSuggestion: boolean;
}
export interface MigrationGeneralSettings { configurationMode: string }
export interface MigrationOBSLink { outputId: string; name: string; url: string }

export interface MigrationPreview {
  id: number;
  expiresAt: string;
  reused: boolean;
  counts: MigrationCounts;
  warnings?: string[];
  ignored?: string[];
  roomSuggestion?: string;
  source: MigrationSource;
  units: MigrationUnit[];
  groups: MigrationGroup[];
  conflicts: MigrationConflict[];
  selection: MigrationSelection;
  generalSettings: MigrationGeneralSettings;
  canConfirm: boolean;
}

export interface MigrationJob {
  id: number;
  status: 'previewed' | 'pending' | 'applied' | 'cancelled' | 'rolled_back' | 'expired';
  expiresAt?: string;
  rollbackExpiresAt?: string;
  obsLinks?: MigrationOBSLink[];
  obsReissueRequired?: boolean;
}
export interface MigrationHistoryJob { id: number; status: MigrationJob['status']; createdAt: string; appliedAt?: string; expiresAt?: string; rollbackExpiresAt?: string }

const stableErrors = new Set([
  'authentication_failed', 'authentication_required', 'invalid_request', 'operation_failed',
  'quota_exhausted', 'rate_limited', 'recent_totp_required', 'request_rejected',
  'temporarily_unavailable', 'verification_pending', 'revision_conflict', 'not_found',
  'proof_rejected', 'expired', 'operation_conflict',
  'credential_unavailable', 'account_disabled',
  'current_session', 'session_not_found',
  'shutting_down',
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

function utcRFC3339(value: unknown): value is string {
  return typeof value === 'string'
    && /^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d{1,9})?Z$/.test(value)
    && Number.isFinite(Date.parse(value));
}

function redactedClientNetwork(value: unknown): value is string {
  if (typeof value !== 'string' || value.length > 64) return false;
  if (value === '—') return true;
  const ipv4Mask = /^(?:(?:25[0-5]|2[0-4]\d|1\d\d|[1-9]?\d)\.){3}\*$/;
  const ipv6Mask = /^(?:[0-9a-f]{1,4}:){1,7}:\*$/i;
  return ipv4Mask.test(value) || ipv6Mask.test(value);
}

function deviceLabel(value: unknown): value is string {
  if (typeof value !== 'string' || value.length === 0 || value.length > 80) return false;
  const [device, browser, extra] = value.split(' · ');
  const devices = new Set(['iPhone', 'iPad', 'Android', 'Windows', 'macOS', 'Linux', '其他设备']);
  const browsers = new Set(['Edge', 'Firefox', 'Chrome', 'Safari', '其他浏览器']);
  return extra === undefined && devices.has(device) && browsers.has(browser);
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
function text(value: unknown): value is string { return typeof value === 'string'; }
function strings(value: unknown): value is string[] { return Array.isArray(value) && value.every((item) => typeof item === 'string'); }
function integers(value: unknown): value is number[] { return Array.isArray(value) && value.every((item) => number(item)); }
function arrayOf(value: unknown, valid: (item: unknown) => boolean): boolean { return Array.isArray(value) && value.every(valid); }
function numberMap(value: unknown, integer = false): boolean { const item = object(value); return item !== undefined && Object.values(item).every((entry) => integer ? number(entry) : finite(entry)); }
function optional(value: unknown, valid: (item: unknown) => boolean): boolean { return value === undefined || valid(value); }
function display(value: unknown): boolean {
  const item = object(value); return item !== undefined && exactKeys(item, ['variant'], ['themeId', 'title', 'min', 'max', 'lowThreshold', 'leftLabel', 'rightLabel', 'valueMappings']) && string(item.variant) && optional(item.themeId, string) && optional(item.title, string) && optional(item.min, finite) && optional(item.max, finite) && optional(item.lowThreshold, finite) && optional(item.leftLabel, string) && optional(item.rightLabel, string) && optional(item.valueMappings, (items) => arrayOf(items, (entry) => { const mapping = object(entry); return mapping !== undefined && exactKeys(mapping, ['value', 'label'], ['color']) && finite(mapping.value) && string(mapping.label) && optional(mapping.color, string); }));
}
function attributeDefinition(value: unknown): boolean {
  const item = object(value); return item !== undefined && exactKeys(item, ['id', 'name', 'unit', 'format', 'decimals', 'suffix'], ['color', 'broadcastMessage', 'display']) && string(item.id) && text(item.name) && text(item.unit) && text(item.format) && Number.isSafeInteger(item.decimals) && text(item.suffix) && optional(item.color, text) && optional(item.broadcastMessage, text) && optional(item.display, display);
}
const legacyOvertime = {
  parameters: [
    { id: 'name', kind: 'text', min: undefined, max: undefined, options: undefined, integer: false },
    { id: 'minutesPerYuan', kind: 'number', min: 1, max: 3600, options: undefined, integer: false },
    { id: 'maxHours', kind: 'number', min: 0, max: 240, options: undefined, integer: false },
    { id: 'broadcastMessage', kind: 'text', min: undefined, max: undefined, options: undefined, integer: false },
  ] as const,
  giftSlots: [{ id: 'overtime', minimum: 1, multiple: true }] as const,
};
type SimpleParameterDefinition = Pick<TemplateParameterDefinition, 'id' | 'kind' | 'min' | 'max' | 'options'> & { integer: boolean };
function simpleDescriptor(id: string, version: number): { parameters: readonly SimpleParameterDefinition[]; giftSlots: readonly Pick<TemplateGiftSlotDefinition, 'id' | 'minimum' | 'multiple'>[] } | undefined {
  if (id === 'overtime' && version === 1) return legacyOvertime;
  const template = GAMEPLAY_TEMPLATES.find((candidate) => candidate.id === id && candidate.version === version);
  return template && { ...template, parameters: template.parameters.map((parameter) => ({ ...parameter, integer: id === 'overtime' && version === 2 && parameter.id === 'maxSeconds' })) };
}
const simpleSchemePattern = /[A-Za-z][A-Za-z0-9+.-]*:/g;
const simpleDrivePattern = /[A-Za-z]:[\\/]/;
const simpleMediaPattern = /\.(apng|avif|bmp|gif|jpe?g|png|svg|webp|mp3|wav|ogg|m4a|mp4|m4v|mov|webm)\b/i;
function safeSimpleText(value: string): boolean {
  if (!value.trim() || Array.from(value).length > 4096 || value.includes('//') || value.includes('\\\\') || simpleDrivePattern.test(value) || simpleMediaPattern.test(value)) return false;
  for (const match of value.matchAll(simpleSchemePattern)) { const scheme = match[0].slice(0, -1); const remainder = value.slice((match.index ?? 0) + match[0].length); if (['http', 'https', 'data', 'file', 'blob', 'javascript', 'vbscript'].includes(scheme.toLowerCase()) || (scheme !== 'PK' && scheme !== 'HP' && (!remainder || !/^\s/u.test(remainder)))) return false; }
  const runes = Array.from(value); for (let index = 0; index < runes.length; index += 1) { if (runes[index] !== '/' && runes[index] !== '\\') continue; if (index + 1 >= runes.length || /^\s$/u.test(runes[index + 1])) continue; if (index === 0 || /^\s$/u.test(runes[index - 1]) || /[\p{P}\p{S}]/u.test(runes[index - 1])) return false; }
  return true;
}
function validParameter(value: unknown, parameter: SimpleParameterDefinition): boolean {
  if (parameter.kind === 'text') return typeof value === 'string' && safeSimpleText(value);
  if (parameter.kind === 'toggle') return typeof value === 'boolean';
  if (parameter.kind === 'select') return typeof value === 'string' && parameter.options?.some((option) => option.value === value) === true;
  return finite(value) && (parameter.min === undefined || value >= parameter.min) && (parameter.max === undefined || value <= parameter.max) && (!parameter.integer || Number.isInteger(value));
}
function validSimplePlay(value: unknown, catalog: ReadonlySet<number>): boolean {
  const item = object(value);
  if (!item || !exactKeys(item, ['version', 'templateId', 'templateVersion', 'attributeId', 'parameters', 'gifts', 'managedFingerprint'], ['overtimeGiftActions']) || !Number.isSafeInteger(item.version) || item.version !== 1 || !string(item.templateId) || !Number.isSafeInteger(item.templateVersion) || !string(item.attributeId) || !string(item.managedFingerprint)) return false;
  const template = simpleDescriptor(item.templateId, item.templateVersion as number); const parameters = object(item.parameters); const gifts = object(item.gifts);
  if (!template || !parameters || !gifts || !exactKeys(parameters, template.parameters.map((parameter) => parameter.id), []) || !exactKeys(gifts, template.giftSlots.map((slot) => slot.id), [])) return false;
  if (!template.parameters.every((parameter) => validParameter(parameters[parameter.id], parameter))) return false;
  const assigned = new Set<number>(); for (const slot of template.giftSlots) { const slotGifts = gifts[slot.id]; if (!integers(slotGifts) || slotGifts.length < slot.minimum || (!slot.multiple && slotGifts.length > 1)) return false; for (const giftID of slotGifts) { if (giftID <= 0 || !catalog.has(giftID) || assigned.has(giftID)) return false; assigned.add(giftID); } }
  if (item.overtimeGiftActions === undefined) return true;
  if (item.templateId !== 'overtime' || item.templateVersion !== 2) return false;
  const actionIDs = new Set<number>();
  return arrayOf(item.overtimeGiftActions, (action) => {
    const row = object(action); if (!row || !exactKeys(row, ['giftId', 'operation'], ['seconds']) || !number(row.giftId) || !string(row.operation) || !assigned.has(row.giftId) || actionIDs.has(row.giftId)) return false; actionIDs.add(row.giftId);
    if (row.operation === 'add' || row.operation === 'subtract') return Object.hasOwn(row, 'seconds') && number(row.seconds) && row.seconds > 0;
    return ['double', 'halve', 'reset'].includes(row.operation) && row.seconds === undefined;
  });
}
function definition(value: unknown): value is HostedConfigurationDefinition {
  const item = object(value);
  if (!item || !exactKeys(item, ['attributes', 'displayScenes', 'giftTargetPanels', 'activities', 'rules', 'timerRules', 'formulaPresets', 'gifts'], ['simplePlay']) || !arrayOf(item.attributes, attributeDefinition)) return false;
  const scene = (entry: unknown): boolean => { const row = object(entry); return row !== undefined && exactKeys(row, ['id', 'name', 'attributeIds', 'layout', 'themeId']) && string(row.id) && text(row.name) && strings(row.attributeIds) && text(row.layout) && text(row.themeId); };
  const panel = (entry: unknown): boolean => { const row = object(entry); return row !== undefined && exactKeys(row, ['id', 'name', 'layout', 'items']) && string(row.id) && text(row.name) && text(row.layout) && arrayOf(row.items, (child) => { const item = object(child); return item !== undefined && exactKeys(item, ['giftId', 'target', 'barStyle'], ['name']) && number(item.giftId) && number(item.target) && text(item.barStyle) && optional(item.name, string); }); };
  const milestone = (entry: unknown): boolean => { const row = object(entry); return row !== undefined && exactKeys(row, ['id', 'name', 'attributeId', 'comparison', 'threshold', 'action', 'message']) && string(row.id) && text(row.name) && text(row.attributeId) && text(row.comparison) && finite(row.threshold) && text(row.action) && text(row.message); };
  const activity = (entry: unknown): boolean => { const row = object(entry); return row !== undefined && exactKeys(row, ['id', 'name', 'attributeIds', 'resultMode', 'gateRules', 'initialValues', 'milestones'], ['sceneId', 'giftTimeout']) && string(row.id) && text(row.name) && strings(row.attributeIds) && text(row.resultMode) && typeof row.gateRules === 'boolean' && numberMap(row.initialValues) && arrayOf(row.milestones, milestone) && optional(row.sceneId, string) && optional(row.giftTimeout, (timeout) => { const item = object(timeout); return item !== undefined && exactKeys(item, ['seconds', 'action']) && number(item.seconds) && text(item.action); }); };
  const rule = (entry: unknown): boolean => { const row = object(entry); return row !== undefined && exactKeys(row, ['id', 'giftId', 'attributeId', 'formula'], ['formulaName', 'condition', 'enabled', 'matchGiftIds', 'minPrice', 'cap', 'dailyLimit']) && string(row.id) && number(row.giftId) && text(row.attributeId) && text(row.formula) && optional(row.formulaName, string) && optional(row.condition, string) && optional(row.enabled, (enabled) => typeof enabled === 'boolean') && optional(row.matchGiftIds, integers) && optional(row.minPrice, finite) && optional(row.cap, finite) && optional(row.dailyLimit, number); };
  const timerRule = (entry: unknown): boolean => { const row = object(entry); return row !== undefined && exactKeys(row, ['id', 'attributeId', 'formulaName', 'intervalSeconds', 'formula', 'enabled'], ['condition']) && string(row.id) && text(row.attributeId) && text(row.formulaName) && number(row.intervalSeconds) && text(row.formula) && typeof row.enabled === 'boolean' && optional(row.condition, string); };
  const preset = (entry: unknown): boolean => { const row = object(entry); return row !== undefined && exactKeys(row, ['id', 'name', 'context', 'formula', 'attributeId']) && string(row.id) && text(row.name) && text(row.context) && text(row.formula) && text(row.attributeId); };
  const gift = (entry: unknown): boolean => { const row = object(entry); return row !== undefined && exactKeys(row, ['id', 'name', 'price', 'coinType'], ['blindBoxParentId', 'blindBoxParentName', 'blindBoxParentPrice']) && number(row.id) && text(row.name) && finite(row.price) && text(row.coinType) && optional(row.blindBoxParentId, number) && optional(row.blindBoxParentName, string) && optional(row.blindBoxParentPrice, finite); };
  if (!arrayOf(item.gifts, gift)) return false;
  const catalog = new Set((item.gifts as unknown[]).map((entry) => (entry as { id: number }).id)); const simplePlay = (entry: unknown): boolean => validSimplePlay(entry, catalog);
  return arrayOf(item.displayScenes, scene) && arrayOf(item.giftTargetPanels, panel) && arrayOf(item.activities, activity) && arrayOf(item.rules, rule) && arrayOf(item.timerRules, timerRule) && arrayOf(item.formulaPresets, preset) && optional(item.simplePlay, simplePlay);
}
function runtime(value: unknown): value is HostedConfigurationRuntime {
  const item = object(value);
  const activity = (entry: unknown): boolean => { const row = object(entry); return row !== undefined && exactKeys(row, ['id', 'status', 'milestones'], ['startedAtMillis', 'lockedAtMillis', 'settledAtMillis', 'result', 'giftTimeout']) && string(row.id) && text(row.status) && arrayOf(row.milestones, (milestone) => { const item = object(milestone); return item !== undefined && exactKeys(item, ['id'], ['triggeredAtMillis', 'triggerValue']) && string(item.id) && optional(item.triggeredAtMillis, (time) => typeof time === 'number' && Number.isSafeInteger(time)) && optional(item.triggerValue, finite); }) && optional(row.startedAtMillis, (time) => typeof time === 'number' && Number.isSafeInteger(time)) && optional(row.lockedAtMillis, (time) => typeof time === 'number' && Number.isSafeInteger(time)) && optional(row.settledAtMillis, (time) => typeof time === 'number' && Number.isSafeInteger(time)) && optional(row.result, (result) => { const item = object(result); return item !== undefined && exactKeys(item, ['values'], ['winnerAttributeId']) && numberMap(item.values) && optional(item.winnerAttributeId, text); }) && optional(row.giftTimeout, (timeout) => { const item = object(timeout); return item !== undefined && (exactKeys(item, []) || (exactKeys(item, ['lastGiftAtMillis', 'deadlineAtMillis']) && typeof item.lastGiftAtMillis === 'number' && Number.isSafeInteger(item.lastGiftAtMillis) && typeof item.deadlineAtMillis === 'number' && Number.isSafeInteger(item.deadlineAtMillis))); }); };
  return item !== undefined && exactKeys(item, ['attributeValues', 'giftTargetReceived', 'activities', 'ruleLimits']) && numberMap(item.attributeValues) && arrayOf(item.giftTargetReceived, (entry) => { const row = object(entry); return row !== undefined && exactKeys(row, ['panelId', 'giftId', 'received']) && string(row.panelId) && number(row.giftId) && number(row.received); }) && arrayOf(item.activities, activity) && (() => { const limits = object(item.ruleLimits); return limits !== undefined && exactKeys(limits, ['localDate', 'appliedCounts']) && text(limits.localDate) && numberMap(limits.appliedCounts, true); })();
}

export function isHostedConfigurationDefinition(value: unknown): value is HostedConfigurationDefinition { return definition(value); }
export function isHostedConfigurationRuntime(value: unknown): value is HostedConfigurationRuntime { return runtime(value); }

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

const safeMapKeys = (value: Record<string, unknown>): boolean => Object.keys(value).every((key) => key !== '__proto__' && key !== 'prototype' && key !== 'constructor');

function migrationCompatibility(value: unknown): MigrationCompatibility | undefined {
  const item = object(value);
  if (!item || !exactKeys(item, ['status'], ['reasonCodes']) || !['complete', 'partial', 'incompatible'].includes(String(item.status)) || (item.reasonCodes !== undefined && !nonEmptyStringArray(item.reasonCodes))) return undefined;
  return { status: item.status as MigrationCompatibilityStatus, reasonCodes: item.reasonCodes ? [...item.reasonCodes as string[]] : [] };
}

function migrationUnit(value: unknown): MigrationUnit | undefined {
  const item = object(value); const compatibility = migrationCompatibility(item?.compatibility);
  const required = ['id', 'kind', 'name', 'attributeIds', 'ruleIds', 'timerRuleIds', 'formulaPresetIds', 'activityIds', 'displaySceneIds', 'giftTargetPanelIds', 'giftIds', 'cropPresetIds', 'compatibility', 'selected'];
  if (!item || !exactKeys(item, required) || !string(item.id) || !string(item.kind) || !text(item.name) || !strings(item.attributeIds) || !strings(item.ruleIds) || !strings(item.timerRuleIds) || !strings(item.formulaPresetIds) || !strings(item.activityIds) || !strings(item.displaySceneIds) || !strings(item.giftTargetPanelIds) || !integers(item.giftIds) || !strings(item.cropPresetIds) || !compatibility || typeof item.selected !== 'boolean') return undefined;
  return { ...item, compatibility } as unknown as MigrationUnit;
}

function migrationGroup(value: unknown): MigrationGroup | undefined {
  const item = object(value);
  if (!item || !exactKeys(item, ['id', 'unitIds', 'reasons']) || !string(item.id) || !strings(item.unitIds) || !Array.isArray(item.reasons)) return undefined;
  const reasons = item.reasons.map((value) => { const reason = object(value); return reason && exactKeys(reason, ['kind', 'referenceId']) && string(reason.kind) && string(reason.referenceId) ? reason as unknown as MigrationGroupReason : undefined; });
  return reasons.some((reason) => !reason) ? undefined : { id: item.id, unitIds: [...item.unitIds], reasons: reasons as MigrationGroupReason[] };
}

function migrationSelection(value: unknown): MigrationSelection | undefined {
  const item = object(value); const choices = object(item?.conflictChoices ?? {}); const unitIDs = item?.unitIds === null ? [] : item?.unitIds;
  if (!item || !exactKeys(item, ['unitIds', 'includeGeneralSettings', 'includeRoomSuggestion'], ['conflictChoices']) || !strings(unitIDs) || !choices || !safeMapKeys(choices) || !Object.values(choices).every((choice) => choice === 'replace' || choice === 'keep_both' || choice === 'skip') || typeof item.includeGeneralSettings !== 'boolean' || typeof item.includeRoomSuggestion !== 'boolean') return undefined;
  return { unitIds: [...unitIDs], conflictChoices: { ...choices } as Record<string, MigrationConflictChoice>, includeGeneralSettings: item.includeGeneralSettings, includeRoomSuggestion: item.includeRoomSuggestion };
}

function migrationConflict(value: unknown): MigrationConflict | undefined {
  const item = object(value); const names = object(item?.suggestedNames ?? {});
  if (!item || !exactKeys(item, ['id', 'importedUnitIds', 'hostedUnitIds'], ['suggestedNames']) || !string(item.id) || !strings(item.importedUnitIds) || !strings(item.hostedUnitIds) || !names || !safeMapKeys(names) || !Object.values(names).every(text)) return undefined;
  return { id: item.id, importedUnitIds: [...item.importedUnitIds], hostedUnitIds: [...item.hostedUnitIds], suggestedNames: { ...names } as Record<string, string> };
}

function migrationGeneralSettings(value: unknown): MigrationGeneralSettings | undefined {
  const item = object(value);
  return item && exactKeys(item, ['configurationMode']) && text(item.configurationMode) ? { configurationMode: item.configurationMode } : undefined;
}

function validMigrationOBSSelector(value: string): boolean {
  if (/^(?:attribute|gift-target):[A-Za-z0-9_-]{1,128}$/.test(value)) return true;
  const scene = /^scene:[A-Za-z0-9_-]{1,128}:([A-Za-z0-9_-]{1,128}(?:,[A-Za-z0-9_-]{1,128})*)$/.exec(value);
  if (!scene) return false;
  const attributeIDs = scene[1]!.split(',');
  return new Set(attributeIDs).size === attributeIDs.length;
}

function migrationOBSLink(value: unknown): MigrationOBSLink | undefined {
  const item = object(value);
  if (!item || !exactKeys(item, ['outputId', 'name', 'url']) || !string(item.outputId) || !text(item.name) || !string(item.url)) return undefined;
  try { const parsed = new URL(item.url); const query=[...parsed.searchParams.keys()]; const output=parsed.searchParams.getAll('output'); const fragment=new URLSearchParams(parsed.hash.slice(1)); const token=fragment.get('token'); const currentOrigin=typeof location==='undefined'?undefined:location.origin; if (parsed.protocol !== 'https:' || parsed.username || parsed.password || !parsed.host || !/^\/obs\/[A-Za-z0-9_-]{43}$/.test(parsed.pathname) || query.length!==1 || query[0]!=='output' || output.length!==1 || item.outputId!==output[0] || !validMigrationOBSSelector(output[0]!) || [...fragment.keys()].length!==1 || !token || token.length>512 || (currentOrigin && parsed.origin!==currentOrigin)) return undefined; }
  catch { return undefined; }
  return item as unknown as MigrationOBSLink;
}

function migrationPreview(value: unknown): MigrationPreview | undefined {
  const item = object(value);
  const counts = migrationCounts(item?.counts); const source = migrationSource(item?.source); const selection = migrationSelection(item?.selection); const generalSettings = migrationGeneralSettings(item?.generalSettings);
  if (!item || !exactKeys(item, ['id', 'expiresAt', 'reused', 'counts', 'source', 'selection', 'generalSettings', 'canConfirm'], ['warnings', 'ignored', 'roomSuggestion', 'units', 'groups', 'conflicts']) || !number(item.id) || item.id === 0 || !instant(item.expiresAt) || typeof item.reused !== 'boolean' || !counts || !source || !selection || !generalSettings || typeof item.canConfirm !== 'boolean' || (item.warnings !== undefined && !nonEmptyStringArray(item.warnings)) || (item.ignored !== undefined && !nonEmptyStringArray(item.ignored)) || (item.roomSuggestion !== undefined && !string(item.roomSuggestion)) || (item.units !== undefined && !Array.isArray(item.units)) || (item.groups !== undefined && !Array.isArray(item.groups)) || (item.conflicts !== undefined && !Array.isArray(item.conflicts))) return undefined;
  const units = (item.units ?? []).map(migrationUnit); const groups = (item.groups ?? []).map(migrationGroup); const conflicts = (item.conflicts ?? []).map(migrationConflict);
  if (units.some((entry) => !entry) || groups.some((entry) => !entry) || conflicts.some((entry) => !entry)) return undefined;
  return { id: item.id, expiresAt: item.expiresAt, reused: item.reused, counts, source, units: units as MigrationUnit[], groups: groups as MigrationGroup[], conflicts: conflicts as MigrationConflict[], selection, generalSettings, canConfirm: item.canConfirm, ...(item.warnings ? { warnings: item.warnings } : {}), ...(item.ignored ? { ignored: item.ignored } : {}), ...(item.roomSuggestion ? { roomSuggestion: item.roomSuggestion } : {}) };
}

function migrationJob(value: unknown): MigrationJob | undefined {
  const item = object(value);
  if (!item || !exactKeys(item, ['id', 'status'], ['expiresAt', 'rollbackExpiresAt', 'obsLinks', 'obsReissueRequired']) || !number(item.id) || item.id === 0 || !string(item.status) || !['previewed', 'pending', 'applied', 'cancelled', 'rolled_back', 'expired'].includes(item.status) || (item.expiresAt !== undefined && !instant(item.expiresAt)) || (item.rollbackExpiresAt !== undefined && !instant(item.rollbackExpiresAt)) || (item.obsLinks !== undefined && !Array.isArray(item.obsLinks)) || (item.obsReissueRequired !== undefined && typeof item.obsReissueRequired !== 'boolean')) return undefined;
  const obsLinks = (item.obsLinks ?? []).map(migrationOBSLink); if (obsLinks.some((entry) => !entry)) return undefined;
  return { ...item, ...(item.obsLinks === undefined ? {} : { obsLinks: obsLinks as MigrationOBSLink[] }) } as unknown as MigrationJob;
}

function challenge(value: unknown): Challenge | undefined {
  const item = object(value);
  return item && exactKeys(item, ['challengeId', 'qrImage', 'expiresAt'], ['verificationUrl']) && string(item.challengeId) && string(item.qrImage) && instant(item.expiresAt)
    && (item.verificationUrl === undefined || validBilibiliVerificationURL(item.verificationUrl))
    ? { challengeId: item.challengeId, qrImage: item.qrImage, ...(item.verificationUrl ? { verificationUrl: item.verificationUrl } : {}), expiresAt: item.expiresAt }
    : undefined;
}

function validBilibiliVerificationURL(value: unknown): value is string {
  if (typeof value !== 'string' || value.length === 0 || value.length > 2048) return false;
  let parsed: URL;
  try { parsed = new URL(value); } catch { return false; }
  if (parsed.protocol !== 'https:' || parsed.username !== '' || parsed.password !== '' || parsed.port !== '' || parsed.hash !== '') return false;
  const currentMobilePath = parsed.hostname === 'account.bilibili.com' && parsed.pathname === '/h5/account-h5/auth/scan-web';
  const allowedPath = (parsed.hostname === 'passport.bilibili.com' && parsed.pathname === '/h5-app/passport/login/scan')
    || (parsed.hostname === 'account.bilibili.com' && parsed.pathname === '/scan') || currentMobilePath;
  if (!allowedPath) return false;
  const keys = [...parsed.searchParams.keys()];
  if (keys.some((key) => key !== 'qrcode_key' && key !== 'navhide' && !(currentMobilePath && (key === 'callback' || key === 'from')))) return false;
  const qrKeys = parsed.searchParams.getAll('qrcode_key');
  if (qrKeys.length !== 1 || qrKeys[0].length === 0 || qrKeys[0].length > 512) return false;
  const navhide = parsed.searchParams.getAll('navhide');
  if (currentMobilePath) {
    const callback = parsed.searchParams.getAll('callback');
    const from = parsed.searchParams.getAll('from');
    return navhide.length === 1 && navhide[0] === '1' && callback.length === 1 && callback[0] === 'close'
      && (from.length === 0 || (from.length === 1 && from[0] === ''));
  }
  return navhide.length === 0 || (navhide.length === 1 && navhide[0] === '1');
}

function invitation(value: unknown, extraOptional: string[] = []): InvitationRecord | undefined {
  const item = object(value);
  if (!item || !exactKeys(item, ['id', 'codeHint', 'status', 'createdAt', 'expiresAt'], ['revokedAt', 'usedAt', ...extraOptional]) || !number(item.id) || item.id <= 0 || typeof item.codeHint !== 'string' || !/^\*{4}[A-Za-z0-9_-]{4}$/.test(item.codeHint) || !string(item.status) ||
    !['active', 'revoked', 'used', 'expired'].includes(item.status) || !instant(item.createdAt) || !instant(item.expiresAt)) return undefined;
  if (item.revokedAt !== undefined && !instant(item.revokedAt)) return undefined;
  if (item.usedAt !== undefined && !instant(item.usedAt)) return undefined;
  return item as unknown as InvitationRecord;
}

function adminEvent(value: unknown): AdminEvent | undefined {
  const item=object(value); if(!item||!exactKeys(item,['type','text','createdAt'],['accountId'])||!string(item.type)||!string(item.text)||!instant(item.createdAt)||!optional(item.accountId,(id)=>number(id)&&id>0)) return undefined; return item as unknown as AdminEvent;
}
function adminAccount(value: unknown, extraOptional: string[] = []): AdminAccountSummary | undefined {
  const item=object(value);if(!item||!exactKeys(item,['id','status','invitationQuota','hasObs','createdAt','updatedAt'],['roomId',...extraOptional])||!number(item.id)||item.id<=0||(item.status!=='active'&&item.status!=='disabled')||!number(item.invitationQuota)||typeof item.hasObs!=='boolean'||!instant(item.createdAt)||!instant(item.updatedAt)||!optional(item.roomId,(room)=>typeof room==='string'&&/^[1-9][0-9]{0,19}$/.test(room)))return undefined;return item as unknown as AdminAccountSummary;
}
function adminInvitation(value:unknown):AdminInvitationRecord|undefined{const item=object(value);if(!item||!exactKeys(item,['id','codeHint','status','createdAt','expiresAt'],['code','usedByAccountId'])||!number(item.id)||item.id<=0||typeof item.codeHint!=='string'||!/^\*{4}[A-Za-z0-9]{4}$/.test(item.codeHint)||!string(item.status)||!['active','used','revoked','expired'].includes(item.status)||!instant(item.createdAt)||(item.expiresAt!==null&&!instant(item.expiresAt))||!optional(item.usedByAccountId,(id)=>number(id)&&id>0))return undefined;if(item.status==='active'){if(typeof item.code!=='string'||!/^[A-Za-z0-9]{8}$/.test(item.code))return undefined}else if(item.code!==undefined)return undefined;return item as unknown as AdminInvitationRecord}
function adminDeviceSession(value: unknown): AdminDeviceSession | undefined {
  const item = object(value);
  if (!item
    || !exactKeys(item, ['id', 'deviceLabel', 'clientNetwork', 'createdAt', 'lastSeenAt', 'expiresAt', 'current'])
    || typeof item.id !== 'string'
    || !/^[0-9a-f]{32}$/.test(item.id)
    || !deviceLabel(item.deviceLabel)
    || !redactedClientNetwork(item.clientNetwork)
    || !utcRFC3339(item.createdAt)
    || !utcRFC3339(item.lastSeenAt)
    || !utcRFC3339(item.expiresAt)
    || typeof item.current !== 'boolean') return undefined;
  return item as unknown as AdminDeviceSession;
}
function adminLoginEvent(value: unknown): AdminLoginEvent | undefined {
  const item = object(value);
  if (!item
    || !exactKeys(item, ['result', 'deviceLabel', 'clientNetwork', 'occurredAt'])
    || (item.result !== 'success' && item.result !== 'failure')
    || !deviceLabel(item.deviceLabel)
    || !redactedClientNetwork(item.clientNetwork)
    || !utcRFC3339(item.occurredAt)) return undefined;
  return item as unknown as AdminLoginEvent;
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

  private async request(path: string, method: string, expectedStatus: number | readonly number[], body?: unknown, extraHeaders:Record<string,string>={}, signal?: AbortSignal): Promise<{ status: number; data: unknown }> {
    const mutation = method !== 'GET';
    const headers: Record<string, string> = { Accept: 'application/json' };
    if (mutation) headers['X-CSRF-Token'] = this.csrfToken;
    if (body !== undefined) headers['Content-Type'] = 'application/json';
    Object.assign(headers,extraHeaders);
    const response = await this.fetcher(path, {
      method, credentials: 'same-origin', headers,
      ...(body === undefined ? {} : { body: JSON.stringify(body) }),
      ...(signal ? { signal } : {}),
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

  private async requestRawJSON(path: string, expectedStatus: number | readonly number[], body: string, signal?: AbortSignal): Promise<{ status: number; data: unknown }> {
    const response = await this.fetcher(path, {
      method: 'POST', credentials: 'same-origin', headers: { Accept: 'application/json', 'Content-Type': 'application/json', 'X-CSRF-Token': this.csrfToken }, body, ...(signal ? { signal } : {}),
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

  async beginLogin(signal?: AbortSignal): Promise<Challenge> { return this.requireChallenge((await this.request('/api/auth/bili/challenges', 'POST', 201, undefined, {}, signal)).data); }
  async cancelLogin(id: string, signal?: AbortSignal): Promise<void> { await this.request(`/api/auth/bili/challenges/${encodeURIComponent(id)}`, 'DELETE', 204, undefined, {}, signal); }
  async createSession(challengeId: string): Promise<void> { await this.request('/api/auth/session', 'POST', 204, { challengeId }); }
  async logout(): Promise<void> { await this.request('/api/auth/session', 'DELETE', 204); }
  async session(signal?: AbortSignal): Promise<{ accountScope: string }> {
    const data = object((await this.request('/api/auth/session', 'GET', 200, undefined, {}, signal)).data);
    if (!data || !exactKeys(data, ['authenticated', 'accountScope']) || data.authenticated !== true || typeof data.accountScope !== 'string' || !/^[A-Za-z0-9_-]{43}$/.test(data.accountScope)) throw new HostedAPIError('invalid_response', 200);
    return { accountScope: data.accountScope };
  }
  async pollLogin(id: string, signal?: AbortSignal): Promise<PollResult> {
    try {
      const response = await this.request(`/api/auth/bili/challenges/${encodeURIComponent(id)}`, 'GET', [200, 410], undefined, {}, signal);
      const data = object(response.data);
      if (!data || !string(data.status)) throw new HostedAPIError('invalid_response', 200);
      if (response.status === 410) {
        if (exactKeys(data, ['status']) && data.status === 'expired') return { status: 'expired' };
        throw new HostedAPIError('invalid_response', response.status);
      }
      if (data.status === 'pending' && exactKeys(data, ['status', 'expiresAt']) && instant(data.expiresAt)) return data as PollResult;
      if (data.status === 'scanned' && exactKeys(data, ['status', 'expiresAt']) && instant(data.expiresAt)) return data as PollResult;
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
    if (!isHostedConfigurationDefinition(definition)) throw new HostedAPIError('invalid_request', 400);
    const data = object((await this.request('/api/configuration/definition', 'PUT', 200, { expectedVersion, definition })).data);
    if (!data || !exactKeys(data, ['version', 'revision']) || !number(data.version) || !number(data.revision)) throw new HostedAPIError('invalid_response', 200);
    return { version: data.version, revision: data.revision };
  }
  async saveConfigurationRuntime(expectedRevision: number, runtime: HostedConfigurationRuntime): Promise<{ revision: number }> {
    if (!isHostedConfigurationRuntime(runtime)) throw new HostedAPIError('invalid_request', 400);
    const data = object((await this.request('/api/configuration/state', 'PUT', 200, { expectedRevision, runtime })).data);
    if (!data || !exactKeys(data, ['revision']) || !number(data.revision)) throw new HostedAPIError('invalid_response', 200);
    return { revision: data.revision };
  }
  async suggestRoom(roomId: string): Promise<void> { await this.request('/api/configuration/room-suggestion', 'PUT', 204, { roomId }); }
  async setRuntimeRoom(roomId: string): Promise<void> {
    if (!validHostedRoomID(roomId)) throw new HostedAPIError('invalid_request', 0);
    await this.request('/api/runtime/room', 'PUT', 204, { roomId });
  }

  async previewMigration(rawJSON: string, signal?: AbortSignal): Promise<MigrationPreview> {
    const result = migrationPreview((await this.requestRawJSON('/api/migrations/preview', 201, rawJSON, signal)).data);
    if (!result) throw new HostedAPIError('invalid_response', 201);
    return result;
  }
  async selectMigration(id: number, selection: MigrationSelection, signal?: AbortSignal): Promise<MigrationPreview> {
    const result = migrationPreview((await this.request(`/api/migrations/${id}/selection`, 'PUT', 200, selection, {}, signal)).data);
    if (!result) throw new HostedAPIError('invalid_response', 200);
    return result;
  }
  async getMigration(id: number, signal?: AbortSignal): Promise<MigrationJob> { return this.requireMigrationJob((await this.request(`/api/migrations/${id}`, 'GET', 200, undefined, {}, signal)).data); }
  async migrationHistory(signal?: AbortSignal): Promise<MigrationHistoryJob[]> { const data = object((await this.request('/api/migrations', 'GET', 200, undefined, {}, signal)).data); if (!data || !exactKeys(data, ['jobs']) || !Array.isArray(data.jobs) || data.jobs.length > 20) throw new HostedAPIError('invalid_response', 200); const jobs = data.jobs.map((value) => { const item=object(value); if(!item||!exactKeys(item,['id','status','createdAt'],['appliedAt','expiresAt','rollbackExpiresAt'])||!number(item.id)||item.id===0||!string(item.status)||!['previewed','pending','applied','cancelled','rolled_back','expired'].includes(item.status)||!instant(item.createdAt)||!optional(item.appliedAt,instant)||!optional(item.expiresAt,instant)||!optional(item.rollbackExpiresAt,instant))return undefined; return item as unknown as MigrationHistoryJob; }); if(jobs.some((item)=>!item))throw new HostedAPIError('invalid_response',200); return jobs as MigrationHistoryJob[]; }
  async applyMigration(id: number, challengeId: string, selection: MigrationSelection, signal?: AbortSignal): Promise<MigrationJob> { return this.requireMigrationJob((await this.request(`/api/migrations/${id}/apply`, 'POST', 200, { challengeId, selection }, {}, signal)).data); }
  async cancelMigration(id: number, signal?: AbortSignal): Promise<MigrationJob> { return this.requireMigrationJob((await this.request(`/api/migrations/${id}`, 'DELETE', 200, undefined, {}, signal)).data); }
  async rollbackMigration(id: number, challengeId: string, signal?: AbortSignal): Promise<MigrationJob> { return this.requireMigrationJob((await this.request(`/api/migrations/${id}/rollback`, 'POST', 200, { challengeId }, {}, signal)).data); }
  async reissueMigrationOBS(id:number, challengeId:string, signal?:AbortSignal):Promise<MigrationJob>{return this.requireMigrationJob((await this.request(`/api/migrations/${id}/obs-links`,'POST',200,{challengeId},{},signal)).data)}
  private requireMigrationJob(value: unknown): MigrationJob {
    const result = migrationJob(value);
    if (!result) throw new HostedAPIError('invalid_response', 200);
    return result;
  }

  async beginAdminEmailLogin(): Promise<EmailLoginChallenge> {
    const data = object((await this.request('/api/admin/auth/email/challenges', 'POST', 201, {})).data);
    if (!data || !exactKeys(data, ['challengeId', 'expiresAt']) || !string(data.challengeId) || !instant(data.expiresAt)) throw new HostedAPIError('invalid_response', 201);
    return { challengeId: data.challengeId, expiresAt: data.expiresAt };
  }
  async adminSession(): Promise<void> { await this.request('/api/admin/session', 'GET', 204); }
  async adminLogout(): Promise<void> { await this.request('/api/admin/session', 'DELETE', 204); }
  async adminEmailLogin(challengeId: string, emailCode: string): Promise<void> { await this.request('/api/admin/session/email', 'POST', 204, { challengeId, emailCode }); }
  async adminOverview(): Promise<AdminOverview> {
    const data=object((await this.request('/api/admin/overview','GET',200)).data);if(!data||!exactKeys(data,['totalAccounts','activeAccounts','disabledAccounts','missingRooms','missingObs','attention','recentEvents'])||!number(data.totalAccounts)||!number(data.activeAccounts)||!number(data.disabledAccounts)||!number(data.missingRooms)||!number(data.missingObs)||!Array.isArray(data.attention)||!Array.isArray(data.recentEvents))throw new HostedAPIError('invalid_response',200);
    const attention=data.attention.map((value)=>{const item=object(value);return item&&exactKeys(item,['kind','accountId','text','priority'])&&(item.kind==='missing_room'||item.kind==='missing_obs')&&number(item.accountId)&&item.accountId>0&&string(item.text)&&number(item.priority)?item as unknown as AdminAttentionItem:undefined;});const events=data.recentEvents.map(adminEvent);if(attention.some((item)=>!item)||events.some((item)=>!item))throw new HostedAPIError('invalid_response',200);return {...data,attention,recentEvents:events} as AdminOverview;
  }
  async adminAccounts(query: { query?: string; status?: AdminAccountStatus; attention?: AdminAttentionKind; cursor?: string; limit?: number } = {}): Promise<AdminAccountPage> {
    const parameters=new URLSearchParams();for(const [key,value] of Object.entries(query)){if(value!==undefined&&value!=='')parameters.set(key,String(value))}const suffix=parameters.size?`?${parameters}`:'';const data=object((await this.request(`/api/admin/accounts${suffix}`,'GET',200)).data);if(!data||!exactKeys(data,['items'],['nextCursor'])||!Array.isArray(data.items)||!optional(data.nextCursor,string))throw new HostedAPIError('invalid_response',200);const items=data.items.map((value)=>adminAccount(value));if(items.some((item)=>!item))throw new HostedAPIError('invalid_response',200);return {items:items as AdminAccountSummary[],...(data.nextCursor?{nextCursor:data.nextCursor as string}:{})};
  }
  async adminAccount(id:number):Promise<AdminAccountDetail>{if(!Number.isSafeInteger(id)||id<=0)throw new HostedAPIError('invalid_request',0);const data=object((await this.request(`/api/admin/accounts/${id}`,'GET',200)).data);const summary=adminAccount(data,['recentEvents','obsUrl']);if(!data||!summary||!Array.isArray(data.recentEvents)||!optional(data.obsUrl,(url)=>{try{const parsed=new URL(url as string);return parsed.protocol==='https:'&&!parsed.search&&!parsed.hash&&/^\/obs\/[A-Za-z0-9_-]{43}$/.test(parsed.pathname)}catch{return false}}))throw new HostedAPIError('invalid_response',200);const events=data.recentEvents.map(adminEvent);if(events.some((event)=>!event))throw new HostedAPIError('invalid_response',200);return {...summary,...(data.obsUrl?{obsUrl:data.obsUrl as string}:{}),recentEvents:events as AdminEvent[]};}
  async adminBatch(accountIds:number[],action:AdminBatchAction,reason:string,remainingQuota?:number):Promise<AdminBatchResult[]>{const data=object((await this.request('/api/admin/accounts/batch','POST',200,{accountIds,action,reason,...(remainingQuota===undefined?{}:{remainingQuota})})).data);if(!data||!exactKeys(data,['results'])||!Array.isArray(data.results))throw new HostedAPIError('invalid_response',200);const results=data.results.map((value)=>{const item=object(value);if(!item||!exactKeys(item,['accountId','status'],['accountStatus','error'])||!number(item.accountId)||item.accountId<=0||(item.status!=='succeeded'&&item.status!=='failed')||!optional(item.accountStatus,(status)=>status==='active'||status==='disabled')||!optional(item.error,string))return undefined;return item as unknown as AdminBatchResult});if(results.some((item)=>!item)||results.length!==accountIds.length||results.some((item,index)=>item?.accountId!==accountIds[index]))throw new HostedAPIError('invalid_response',200);return results as AdminBatchResult[];}
  async adminInvitations(query:AdminInvitationQuery={}):Promise<AdminInvitationPage>{const parameters=new URLSearchParams();for(const [key,value] of Object.entries(query)){if(value!==undefined&&value!=='')parameters.set(key,String(value))}const data=object((await this.request(`/api/admin/invitations${parameters.size?`?${parameters}`:''}`,'GET',200)).data);if(!data||!exactKeys(data,['invitations'],['nextCursor'])||!Array.isArray(data.invitations)||!optional(data.nextCursor,string))throw new HostedAPIError('invalid_response',200);const invitations=data.invitations.map(adminInvitation);if(invitations.some((item)=>!item))throw new HostedAPIError('invalid_response',200);return{invitations:invitations as AdminInvitationRecord[],...(data.nextCursor?{nextCursor:data.nextCursor as string}:{})}}
  async createAdminInvitations(count:number,validity:'7d'|'30d'|'permanent'):Promise<AdminInvitationRecord[]>{const data=object((await this.request('/api/admin/invitations','POST',201,{count,validity})).data);if(!data||!exactKeys(data,['invitations'])||!Array.isArray(data.invitations))throw new HostedAPIError('invalid_response',201);const invitations=data.invitations.map(adminInvitation);if(invitations.length!==count||invitations.some((item)=>!item||item?.status!=='active'))throw new HostedAPIError('invalid_response',201);return invitations as AdminInvitationRecord[]}
  async revokeAdminInvitation(id:number):Promise<void>{if(!Number.isSafeInteger(id)||id<=0)throw new HostedAPIError('invalid_request',0);await this.request(`/api/admin/invitations/${id}`,'DELETE',204)}
  async updateAdminRoom(id:number,roomId:string):Promise<AdminAccountDetail>{const data=await this.request(`/api/admin/accounts/${id}/room`,'PUT',200,{roomId});return this.parseAdminAccountDetail(data.data);}
  private parseAdminAccountDetail(value:unknown):AdminAccountDetail{const data=object(value);const summary=adminAccount(data,['recentEvents','obsUrl']);if(!data||!summary||!Array.isArray(data.recentEvents))throw new HostedAPIError('invalid_response',200);const events=data.recentEvents.map(adminEvent);if(events.some((event)=>!event))throw new HostedAPIError('invalid_response',200);return {...summary,...(typeof data.obsUrl==='string'?{obsUrl:data.obsUrl}:{}),recentEvents:events as AdminEvent[]};}
  async biliServiceStatus(): Promise<BiliServiceStatus> {
    const data = object((await this.request('/api/admin/bili-service/status', 'GET', 200)).data);
    if (!data || !number(data.version) || !string(data.health)) throw new HostedAPIError('invalid_response', 200);
    if (data.health === 'healthy' && data.version > 0 && exactKeys(data, ['version', 'health', 'lastVerifiedAt'],['maskedUid','lastReplacedAt']) && instant(data.lastVerifiedAt)&&optional(data.maskedUid,string)&&optional(data.lastReplacedAt,instant)) {
      return { version: data.version, health: 'healthy', lastVerifiedAt: data.lastVerifiedAt,...(data.maskedUid?{maskedUid:data.maskedUid as string}:{}),...(data.lastReplacedAt?{lastReplacedAt:data.lastReplacedAt as string}:{}) };
    }
    if ((data.health === 'missing' || data.health === 'unavailable') && data.version === 0 && exactKeys(data, ['version', 'health'])) {
      return { version: 0, health: data.health };
    }
    throw new HostedAPIError('invalid_response', 200);
  }
  async beginBiliServiceChallenge(): Promise<Challenge> { return this.requireChallenge((await this.request('/api/admin/bili-service/challenge', 'POST', 201)).data); }
  async pollBiliServiceChallenge(id: string): Promise<{ status: BiliServiceChallengeStage }> {
    const value = object((await this.request(
      `/api/admin/bili-service/challenge/${encodeURIComponent(id)}`, 'GET', 200,
    )).data);
    if (!value || !exactKeys(value, ['status']) || typeof value.status !== 'string'
      || !['pending', 'scanned', 'verified'].includes(String(value.status))) {
      throw new HostedAPIError('invalid_response', 200);
    }
    return value as { status: BiliServiceChallengeStage };
  }
  async cancelBiliServiceChallenge(id: string): Promise<void> {
    if (!string(id) || id.length > 256) throw new HostedAPIError('invalid_request', 400);
    await this.request(`/api/admin/bili-service/challenge/${encodeURIComponent(id)}`, 'DELETE', 204);
  }
  async checkBiliService():Promise<BiliServiceStatus>{const data=object((await this.request('/api/admin/bili-service/check','POST',200)).data);if(!data||!number(data.version)||!string(data.health))throw new HostedAPIError('invalid_response',200);return this.parseBiliStatus(data)}
  private parseBiliStatus(data:Record<string,unknown>):BiliServiceStatus{if(data.health==='healthy'&&number(data.version)&&data.version>0&&instant(data.lastVerifiedAt)&&exactKeys(data,['version','health','lastVerifiedAt'],['maskedUid','lastReplacedAt']))return{version:data.version,health:'healthy',lastVerifiedAt:data.lastVerifiedAt,...(typeof data.maskedUid==='string'?{maskedUid:data.maskedUid}:{}),...(typeof data.lastReplacedAt==='string'?{lastReplacedAt:data.lastReplacedAt}:{})};if((data.health==='missing'||data.health==='unavailable')&&data.version===0&&exactKeys(data,['version','health']))return{version:0,health:data.health};throw new HostedAPIError('invalid_response',200)}
  async authorizeAdminOperation(totp:string,purpose:'bili_service_replace'|'admin_email_change'|'recovery_regenerate',target:string):Promise<string>{const data=object((await this.request('/api/admin/operation-authorizations','POST',201,{totp,purpose,target})).data);if(!data||!exactKeys(data,['authorizationToken'])||!string(data.authorizationToken))throw new HostedAPIError('invalid_response',201);return data.authorizationToken}
  async replaceBiliServiceCredential(challengeId: string,authorizationToken?:string): Promise<void> {
    if (!string(challengeId) || challengeId.length > 256) throw new HostedAPIError('invalid_request', 400);
    await this.request('/api/admin/bili-service/replace', 'POST', 204, { challengeId },authorizationToken?{'X-Admin-Authorization':authorizationToken}:{});
  }
  async adminSettings():Promise<AdminSettings>{const data=object((await this.request('/api/admin/settings','GET',200)).data);if(!data||!exactKeys(data,['maskedEmail','sessionExpiresAt','totpEnabled','recoveryGeneratedAt','serviceHealth'])||!string(data.maskedEmail)||!instant(data.sessionExpiresAt)||typeof data.totpEnabled!=='boolean'||(data.recoveryGeneratedAt!==null&&!instant(data.recoveryGeneratedAt))||!string(data.serviceHealth))throw new HostedAPIError('invalid_response',200);return data as unknown as AdminSettings}
  async adminSessions(): Promise<AdminDeviceSession[]> {
    const data = object((await this.request('/api/admin/sessions', 'GET', 200)).data);
    if (!data || !exactKeys(data, ['sessions']) || !Array.isArray(data.sessions)) throw new HostedAPIError('invalid_response', 200);
    const sessions = data.sessions.map(adminDeviceSession);
    if (sessions.some((session) => !session)) throw new HostedAPIError('invalid_response', 200);
    return sessions as AdminDeviceSession[];
  }
  async revokeAdminSession(id: string): Promise<void> {
    if (!/^[0-9a-f]{32}$/.test(id)) throw new HostedAPIError('invalid_request', 0);
    await this.request(`/api/admin/sessions/${id}`, 'DELETE', 204);
  }
  async revokeOtherAdminSessions():Promise<void>{await this.request('/api/admin/sessions/revoke-others','POST',204)}
  async adminLoginEvents(limit = 20): Promise<AdminLoginEvent[]> {
    if (!Number.isSafeInteger(limit) || limit < 1 || limit > 50) throw new HostedAPIError('invalid_request', 0);
    const data = object((await this.request(`/api/admin/login-events?limit=${limit}`, 'GET', 200)).data);
    if (!data || !exactKeys(data, ['events']) || !Array.isArray(data.events)) throw new HostedAPIError('invalid_response', 200);
    const events = data.events.map(adminLoginEvent);
    if (events.some((event) => !event)) throw new HostedAPIError('invalid_response', 200);
    return events as AdminLoginEvent[];
  }
  async adminEvents():Promise<AdminEvent[]>{const data=object((await this.request('/api/admin/events','GET',200)).data);if(!data||!exactKeys(data,['events'])||!Array.isArray(data.events))throw new HostedAPIError('invalid_response',200);const events=data.events.map(adminEvent);if(events.some((event)=>!event))throw new HostedAPIError('invalid_response',200);return events as AdminEvent[]}
  async adminDiagnostics():Promise<AdminDiagnostic>{const data=object((await this.request('/api/admin/diagnostics','GET',200)).data);if(!data||!exactKeys(data,['database','biliService','checkedAt'])||!string(data.database)||!string(data.biliService)||!instant(data.checkedAt))throw new HostedAPIError('invalid_response',200);return data as unknown as AdminDiagnostic}
  async issueOBSCredential(accountId: number): Promise<OBSCredentialAccess> {
    if (!Number.isSafeInteger(accountId) || accountId <= 0) throw new HostedAPIError('invalid_request', 0);
    const data = object((await this.request(`/api/admin/accounts/${accountId}/obs-credential`, 'POST', 201, {})).data);
    if (!data || !exactKeys(data, ['publicId', 'url']) || typeof data.publicId !== 'string' || !obsPublicIDPattern.test(data.publicId) || typeof data.url !== 'string') {
      throw new HostedAPIError('invalid_response', 201);
    }
    let parsed: URL;
    try { parsed = new URL(data.url); } catch { throw new HostedAPIError('invalid_response', 201); }
    if (parsed.protocol !== 'https:' || parsed.username !== '' || parsed.password !== '' || parsed.search !== '' || parsed.pathname !== `/obs/${data.publicId}` || !/^#token=[A-Za-z0-9_%~-]+$/.test(parsed.hash)) {
      throw new HostedAPIError('invalid_response', 201);
    }
    return { publicId: data.publicId, url: data.url };
  }
  async sendRecoveryArchive(): Promise<{ recoveryPassword: string }> {
    const data = object((await this.request('/api/admin/recovery/archive', 'POST', 200, {})).data);
    if (!data || Object.keys(data).length !== 1 || !string(data.recoveryPassword) || data.recoveryPassword.length !== 20) throw new HostedAPIError('invalid_response', 200);
    return { recoveryPassword: data.recoveryPassword };
  }
  async prepareRecovery(recoveryCode: string): Promise<RecoveryPreparation> {
    const data = object((await this.request('/api/admin/recovery/prepare', 'POST', 200, { recoveryCode })).data);
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
  private async accountMutation(accountId: number, action: string, expectedAccountStatuses: readonly ManagedAccount['status'][], body: unknown): Promise<ManagedAccount> {
    const data = object((await this.request(`/api/admin/accounts/${accountId}/${action}`, 'POST', 200, body)).data);
    const status = expectedAccountStatuses.find((expected) => data?.status === expected);
    if (!data || !exactKeys(data, ['accountId', 'status']) || data.accountId !== accountId || !status) throw new HostedAPIError('invalid_response', 200);
    return { accountId, status };
  }
}
