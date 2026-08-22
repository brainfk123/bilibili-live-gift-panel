import type { ActivitySession, ContributionLedger, GiftInfo, GiftReceipt, ViewerContribution } from './types';
import type { GiftUserIdentity } from './gift-rule-conditions';
import type { ActivityTransitionAction } from './activities';
import { normalizeChangelogReleases, type ChangelogRelease } from './changelog';
import type { GiftTargetProgressSnapshot } from './gift-targets';

export type RuntimeConnectionState = 'idle' | 'connecting' | 'connected' | 'reconnecting' | 'error';

export interface RuntimeConnectionGap {
  startedAt: number;
  endedAt?: number;
  durationMs?: number;
  attempts: number;
  errorKind?: string;
}

export interface RuntimeInboxHealth {
  pendingCount: number;
  pendingBytes?: number;
  oldestPendingAt?: number;
  capacityError?: boolean;
}

export interface RuntimeStatus {
  state: RuntimeConnectionState;
  roomId: string;
  lastError?: string;
  lastGiftAt?: number;
  lastFrameAt?: number;
  gaps?: RuntimeConnectionGap[];
  reconnectAttempts?: number;
  inbox?: RuntimeInboxHealth;
  transactionPending?: boolean;
  ingestionErrorKind?: string;
}

export type UpdateState = 'idle' | 'disabled' | 'development' | 'unsupported' | 'checking' | 'downloading' | 'ready' | 'up-to-date' | 'error';

export interface UpdateStatus {
  state: UpdateState;
  currentVersion: string;
  latestVersion?: string;
  message: string;
  progress?: number;
  lastCheckedAt?: number;
  autoUpdate: boolean;
  restartRequired: boolean;
}

export type PagePresenceMode = 'config' | 'display';

export interface PagePresenceCallbacks {
  onUnavailable?: () => void;
  onReady?: (version: string) => void;
}

export type BiliAuthState = 'anonymous' | 'waiting' | 'scanned' | 'logged_in' | 'expired' | 'error';

export interface BiliAuthStatus {
  state: BiliAuthState;
  uid?: number;
  uname?: string;
  avatar?: string;
  roomId?: string;
  isRoomOwner?: boolean;
  qrImage?: string;
  expiresAt?: number;
  message?: string;
}

export interface RoomAnchorInfo {
  roomId: string;
  uid: number;
  uname?: string;
  avatar?: string;
}

export function startPagePresence(mode: PagePresenceMode, callbacks: PagePresenceCallbacks = {}): () => void {
  const EventSourceConstructor = globalThis.EventSource;
  if (typeof EventSourceConstructor !== 'function') return () => {};
  const sessionID = globalThis.crypto?.randomUUID?.() ?? `${Date.now()}-${Math.random().toString(36).slice(2)}`;
  const url = `/api/pages/presence/stream?mode=${mode}&id=${encodeURIComponent(sessionID)}`;
  let source: EventSource | undefined;
  const connect = (): void => {
    if (source) return;
    source = new EventSourceConstructor(url);
    source.onerror = () => callbacks.onUnavailable?.();
    source.addEventListener('ready', (event) => {
      try {
        const payload = JSON.parse((event as MessageEvent<string>).data) as { version?: unknown };
        if (typeof payload.version === 'string' && payload.version.trim()) callbacks.onReady?.(payload.version.trim());
      } catch {
        // A malformed readiness event must not break EventSource reconnection.
      }
    });
  };
  const disconnect = (): void => {
    source?.close();
    source = undefined;
  };
  connect();
  globalThis.addEventListener?.('pagehide', disconnect);
  globalThis.addEventListener?.('pageshow', connect);
  return () => {
    disconnect();
    globalThis.removeEventListener?.('pagehide', disconnect);
    globalThis.removeEventListener?.('pageshow', connect);
  };
}

interface RuntimeResponse {
  code: number;
  runtime: RuntimeStatus;
}

interface FormulaPreviewResponse {
  code: number;
  result?: number;
  message?: string;
}

interface GiftRulePreviewResponse {
  code: number;
  triggered?: boolean;
  result?: number;
  message?: string;
}

interface BiliAuthResponse {
  code: number;
  auth?: BiliAuthStatus;
  message?: string;
}

interface BlindBoxResponse {
  code: number;
  blindBox?: import('./types').BlindBoxInfo | null;
  requiresLogin?: boolean;
  message?: string;
}

interface RoomGiftCatalogResponse {
  code: number;
  gifts?: GiftInfo[];
  message?: string;
}

interface RoomAnchorResponse {
  code: number;
  roomId?: string;
  anchor?: {
    uid?: number;
    uname?: string;
    avatar?: string;
  };
  message?: string;
}

interface UpdateResponse {
  code: number;
  update?: UpdateStatus;
  message?: string;
}

interface ChangelogResponse {
  releases?: unknown;
  message?: string;
}

interface ActivityTransitionResponse {
  code: number;
  activity?: ActivitySession;
  attributeValues?: Record<string, number>;
  message?: string;
}

interface ContributionResponse {
  code: number;
  contributions?: ContributionLedger;
  message?: string;
}

interface GiftTargetProgressResponse {
  code: number;
  progress?: GiftTargetProgressSnapshot;
  message?: string;
}

interface GiftReceiptResponse {
  code: number;
  giftReceipts?: GiftReceipt[];
  message?: string;
}

export interface BlindBoxLeaderboardSummary {
  viewerCount: number;
  blindBoxCount: number;
  cost: number;
  value: number;
  profit: number;
  unpricedCount: number;
}

export interface BlindBoxLeaderboardScope {
  giftId: number;
  giftName: string;
  count: number;
  lastGiftAt: number;
}

export interface BlindBoxLeaderboardSnapshot {
  updatedAt: number;
  summary: BlindBoxLeaderboardSummary;
  viewers: ViewerContribution[];
  scopes: BlindBoxLeaderboardScope[];
}

export interface ActivityTransitionResult {
  activity: ActivitySession;
  attributeValues: Record<string, number>;
}

export interface BlindBoxLookup {
  info: import('./types').BlindBoxInfo | null;
  requiresLogin: boolean;
}

export async function getBlindBoxLeaderboard(options: {
  giftId?: number;
  limit?: number;
  signal?: AbortSignal;
} = {}): Promise<BlindBoxLeaderboardSnapshot> {
  const query: string[] = [];
  if (options.giftId !== undefined) query.push(`giftId=${encodeURIComponent(String(options.giftId))}`);
  if (options.limit !== undefined) query.push(`limit=${encodeURIComponent(String(options.limit))}`);
  const path = `/api/blind-box/leaderboard${query.length > 0 ? `?${query.join('&')}` : ''}`;
  const response = await fetch(path, { cache: 'no-store', signal: options.signal });
  let payload: unknown;
  try {
    payload = await response.json();
  } catch (error) {
    if (options.signal?.aborted) {
      if ('reason' in options.signal) throw options.signal.reason;
      throw error;
    }
    throw invalidBlindBoxLeaderboardResponse();
  }
  if (!response.ok || !isBlindBoxLeaderboardEnvelope(payload)) throw invalidBlindBoxLeaderboardResponse();
  return payload.leaderboard;
}

function isBlindBoxLeaderboardEnvelope(value: unknown): value is { code: 0; leaderboard: BlindBoxLeaderboardSnapshot } {
  return hasExactKeys(value, ['code', 'leaderboard'])
    && value.code === 0
    && isBlindBoxLeaderboardSnapshot(value.leaderboard);
}

function isBlindBoxLeaderboardSnapshot(value: unknown): value is BlindBoxLeaderboardSnapshot {
  return hasExactKeys(value, ['updatedAt', 'summary', 'viewers', 'scopes'])
    && isNonNegativeInteger(value.updatedAt)
    && isBlindBoxLeaderboardSummary(value.summary)
    && Array.isArray(value.viewers)
    && value.viewers.every(isViewerContribution)
    && Array.isArray(value.scopes)
    && value.scopes.every(isBlindBoxLeaderboardScope);
}

function isBlindBoxLeaderboardSummary(value: unknown): value is BlindBoxLeaderboardSummary {
  return hasExactKeys(value, ['viewerCount', 'blindBoxCount', 'cost', 'value', 'profit', 'unpricedCount'])
    && isNonNegativeInteger(value.viewerCount)
    && isNonNegativeInteger(value.blindBoxCount)
    && isNonNegativeNumber(value.cost)
    && isNonNegativeNumber(value.value)
    && isFiniteNumber(value.profit)
    && isNonNegativeInteger(value.unpricedCount);
}

function isBlindBoxLeaderboardScope(value: unknown): value is BlindBoxLeaderboardScope {
  return hasExactKeys(value, ['giftId', 'giftName', 'count', 'lastGiftAt'])
    && isPositiveInteger(value.giftId)
    && typeof value.giftName === 'string'
    && isNonNegativeInteger(value.count)
    && isNonNegativeInteger(value.lastGiftAt);
}

function isViewerContribution(value: unknown): value is ViewerContribution {
  if (!hasExactKeys(value, [
    'key', 'uname', 'giftCount', 'goldValue', 'silverValue', 'ruleTriggers',
    'attributeDeltas', 'blindBoxCount', 'blindBoxCost', 'blindBoxValue', 'blindBoxProfit', 'lastGiftAt',
  ], ['uid', 'avatar', 'unpricedBlindBoxCount', 'blindBoxes'])) return false;
  return typeof value.key === 'string'
    && (value.uid === undefined || isPositiveInteger(value.uid))
    && typeof value.uname === 'string'
    && (value.avatar === undefined || typeof value.avatar === 'string')
    && isNonNegativeInteger(value.giftCount)
    && isNonNegativeNumber(value.goldValue)
    && isNonNegativeNumber(value.silverValue)
    && isNonNegativeInteger(value.ruleTriggers)
    && isAttributeDeltas(value.attributeDeltas)
    && isNonNegativeInteger(value.blindBoxCount)
    && isNonNegativeNumber(value.blindBoxCost)
    && isNonNegativeNumber(value.blindBoxValue)
    && isFiniteNumber(value.blindBoxProfit)
    && (value.unpricedBlindBoxCount === undefined || isNonNegativeInteger(value.unpricedBlindBoxCount))
    && (value.blindBoxes === undefined || (Array.isArray(value.blindBoxes) && value.blindBoxes.every(isBlindBoxContribution)))
    && isNonNegativeInteger(value.lastGiftAt);
}

function isBlindBoxContribution(value: unknown): boolean {
  return hasExactKeys(value, ['giftId', 'giftName', 'count', 'cost', 'value', 'profit', 'lastGiftAt'], ['unpricedCount'])
    && isPositiveInteger(value.giftId)
    && typeof value.giftName === 'string'
    && isNonNegativeInteger(value.count)
    && isNonNegativeNumber(value.cost)
    && isNonNegativeNumber(value.value)
    && isFiniteNumber(value.profit)
    && (value.unpricedCount === undefined || isNonNegativeInteger(value.unpricedCount))
    && isNonNegativeInteger(value.lastGiftAt);
}

function isAttributeDeltas(value: unknown): value is Record<string, number> {
  return isPlainObject(value) && Object.values(value).every(isFiniteNumber);
}

function hasExactKeys(
  value: unknown,
  required: readonly string[],
  optional: readonly string[] = [],
): value is Record<string, unknown> {
  if (!isPlainObject(value)) return false;
  const allowed = new Set([...required, ...optional]);
  return required.every((key) => Object.hasOwn(value, key))
    && Object.keys(value).every((key) => allowed.has(key));
}

function isPlainObject(value: unknown): value is Record<string, unknown> {
  if (typeof value !== 'object' || value === null) return false;
  const prototype = Object.getPrototypeOf(value);
  return prototype === Object.prototype || prototype === null;
}

function isFiniteNumber(value: unknown): value is number {
  return typeof value === 'number' && Number.isFinite(value);
}

function isNonNegativeNumber(value: unknown): value is number {
  return isFiniteNumber(value) && value >= 0;
}

function isNonNegativeInteger(value: unknown): value is number {
  return isNonNegativeNumber(value) && Number.isInteger(value);
}

function isPositiveInteger(value: unknown): value is number {
  return isNonNegativeInteger(value) && value > 0;
}

function invalidBlindBoxLeaderboardResponse(): Error {
  return new Error('盲盒排行榜响应无效');
}

export async function getRuntimeStatus(): Promise<RuntimeStatus> {
  const response = await fetch('/api/runtime', { cache: 'no-store' });
  if (!response.ok) throw new Error(`后台状态读取失败：HTTP ${response.status}`);
  const payload = await response.json() as RuntimeResponse;
  if (payload.code !== 0 || !payload.runtime) throw new Error('后台状态响应无效');
  return payload.runtime;
}

async function requestBiliAuth(path: string, init?: RequestInit): Promise<BiliAuthStatus> {
  const response = await fetch(path, { cache: 'no-store', ...init });
  const payload = await response.json() as BiliAuthResponse;
  if (!response.ok || payload.code !== 0 || !payload.auth) {
    throw new Error(payload.message || `B 站登录请求失败：HTTP ${response.status}`);
  }
  return payload.auth;
}

function authRoomPath(path: string, roomId = ''): string {
  const normalized = roomId.trim();
  return normalized ? `${path}?room_id=${encodeURIComponent(normalized)}` : path;
}

export function getBiliAuthStatus(roomId = ''): Promise<BiliAuthStatus> {
  return requestBiliAuth(authRoomPath('/api/auth/status', roomId));
}

export function startBiliQRCodeLogin(): Promise<BiliAuthStatus> {
  return requestBiliAuth('/api/auth/qrcode', { method: 'POST' });
}

export function pollBiliQRCodeLogin(roomId = ''): Promise<BiliAuthStatus> {
  return requestBiliAuth(authRoomPath('/api/auth/qrcode', roomId));
}

export function logoutBiliAuth(): Promise<BiliAuthStatus> {
  return requestBiliAuth('/api/auth/session', { method: 'DELETE' });
}

export async function previewFormula(
  formula: string,
  attributeName: string,
  attributeValue: number,
  context: 'gift' | 'timer' = 'gift',
  giftPrice?: number,
): Promise<number> {
  const response = await fetch('/api/formula/preview', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ formula, attributeName, attributeValue, context, giftPrice }),
  });
  const payload = await response.json() as FormulaPreviewResponse;
  if (!response.ok || payload.code !== 0 || typeof payload.result !== 'number') {
    throw new Error(payload.message || `规则计算失败：HTTP ${response.status}`);
  }
  return payload.result;
}

export interface GiftRulePreview {
  triggered: boolean;
  result: number;
}

export async function previewGiftRule(options: {
  condition?: string;
  formula: string;
  attributeName: string;
  attributeValue: number;
  giftPrice?: number;
  userIdentity?: GiftUserIdentity;
}): Promise<GiftRulePreview> {
  const response = await fetch('/api/formula/preview', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({
      condition: options.condition ?? '',
      formula: options.formula,
      attributeName: options.attributeName,
      attributeValue: options.attributeValue,
      context: 'gift',
      giftPrice: options.giftPrice,
      userIdentity: options.userIdentity ?? 0,
    }),
  });
  const payload = await response.json() as GiftRulePreviewResponse;
  if (!response.ok || payload.code !== 0 || typeof payload.triggered !== 'boolean' || !isFiniteNumber(payload.result)) {
    throw new Error(payload.message || `规则计算失败：HTTP ${response.status}`);
  }
  return { triggered: payload.triggered, result: payload.result };
}

async function requestFormulaValidation(body: Record<string, unknown>): Promise<void> {
  const response = await fetch('/api/formula/preview', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ ...body, validateOnly: true }),
  });
  const payload = await response.json() as FormulaPreviewResponse;
  if (!response.ok || payload.code !== 0) {
    throw new Error(payload.message || `规则校验失败：HTTP ${response.status}`);
  }
}

export function validateFormula(
  formula: string,
  attributeName: string,
  attributeValue: number,
  context: 'gift' | 'timer' = 'gift',
  giftPrice?: number,
): Promise<void> {
  return requestFormulaValidation({ formula, attributeName, attributeValue, context, giftPrice });
}

export function validateGiftRule(options: {
  condition?: string;
  formula: string;
  attributeName: string;
  attributeValue: number;
  giftPrice?: number;
}): Promise<void> {
  return requestFormulaValidation({
    condition: options.condition ?? '',
    formula: options.formula,
    attributeName: options.attributeName,
    attributeValue: options.attributeValue,
    context: 'gift',
    giftPrice: options.giftPrice,
  });
}

export async function getBlindBoxInfo(giftId: number): Promise<BlindBoxLookup> {
  const response = await fetch(`/api/blind-box?giftId=${encodeURIComponent(String(giftId))}`, { cache: 'no-store' });
  const payload = await response.json() as BlindBoxResponse;
  if (!response.ok || payload.code !== 0) {
    throw new Error(payload.message || `盲盒信息读取失败：HTTP ${response.status}`);
  }
  return {
    info: payload.blindBox ?? null,
    requiresLogin: payload.requiresLogin === true,
  };
}

export async function getRoomGiftCatalog(roomId: string): Promise<GiftInfo[]> {
  const normalized = roomId.trim();
  if (!normalized) return [];
  const response = await fetch(`/api/gifts?roomId=${encodeURIComponent(normalized)}`, { cache: 'no-store' });
  const payload = await response.json() as RoomGiftCatalogResponse;
  if (!response.ok || payload.code !== 0 || !Array.isArray(payload.gifts)) {
    throw new Error(payload.message || `当前礼物目录读取失败：HTTP ${response.status}`);
  }
  return payload.gifts;
}

export async function getRoomAnchorInfo(roomId: string): Promise<RoomAnchorInfo | null> {
  const normalized = roomId.trim();
  if (!normalized) return null;
  const response = await fetch(`/api/room/anchor?roomId=${encodeURIComponent(normalized)}`, { cache: 'no-store' });
  const payload = await response.json() as RoomAnchorResponse;
  const uid = Number(payload.anchor?.uid);
  if (!response.ok || payload.code !== 0 || !Number.isFinite(uid) || uid <= 0) {
    throw new Error(payload.message || `主播信息读取失败：HTTP ${response.status}`);
  }
  return {
    roomId: String(payload.roomId ?? normalized),
    uid: Math.trunc(uid),
    ...(payload.anchor?.uname?.trim() ? { uname: payload.anchor.uname.trim() } : {}),
    ...(payload.anchor?.avatar?.trim() ? { avatar: payload.anchor.avatar.trim() } : {}),
  };
}

async function requestUpdateStatus(path: string, init?: RequestInit): Promise<UpdateStatus> {
  const response = await fetch(path, { cache: 'no-store', ...init });
  const payload = await response.json() as UpdateResponse;
  if (!response.ok || payload.code !== 0 || !payload.update) {
    throw new Error(payload.message || `更新状态读取失败：HTTP ${response.status}`);
  }
  return payload.update;
}

export function getUpdateStatus(): Promise<UpdateStatus> {
  return requestUpdateStatus('/api/update');
}

export function checkForUpdates(): Promise<UpdateStatus> {
  return requestUpdateStatus('/api/update/check', { method: 'POST' });
}

export async function getHostedChangelog(): Promise<ChangelogRelease[]> {
  const response = await fetch('/api/changelog', { cache: 'no-store' });
  const payload = await response.json() as ChangelogResponse;
  const releases = normalizeChangelogReleases(payload);
  if (!response.ok || releases.length === 0) {
    throw new Error(payload.message || `在线更新日志读取失败：HTTP ${response.status}`);
  }
  return releases;
}

export async function transitionActivity(activityId: string, action: ActivityTransitionAction): Promise<ActivityTransitionResult> {
  const response = await fetch('/api/activities/transition', {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ activityId, action }),
  });
  const payload = await response.json() as ActivityTransitionResponse;
  if (!response.ok || payload.code !== 0 || !payload.activity) {
    throw new Error(payload.message || `活动操作失败：HTTP ${response.status}`);
  }
  return { activity: payload.activity, attributeValues: payload.attributeValues ?? {} };
}

export async function clearContributionLedger(): Promise<ContributionLedger> {
  const response = await fetch('/api/contributions', { method: 'DELETE', cache: 'no-store' });
  const payload = await response.json() as ContributionResponse;
  if (!response.ok || payload.code !== 0 || !payload.contributions) {
    throw new Error(payload.message || `排行榜清空失败：HTTP ${response.status}`);
  }
  return payload.contributions;
}

export async function clearGiftReceipts(): Promise<GiftReceipt[]> {
  const response = await fetch('/api/gift-receipts', { method: 'DELETE', cache: 'no-store' });
  const payload = await response.json() as GiftReceiptResponse;
  if (!response.ok || payload.code !== 0 || !Array.isArray(payload.giftReceipts)) {
    throw new Error(payload.message || `送礼记录清空失败：HTTP ${response.status}`);
  }
  return payload.giftReceipts;
}

export type GiftReceiptMediaKind = 'animation' | 'avatar' | 'effect-video' | 'effect-layout';

export function giftReceiptMediaUrl(receiptId: string, kind: GiftReceiptMediaKind): string {
  return `/api/gift-receipts/media?id=${encodeURIComponent(receiptId)}&kind=${kind}`;
}

export async function resetGiftTargetProgress(panelId: string): Promise<GiftTargetProgressSnapshot> {
  const response = await fetch(`/api/gift-targets/progress?panelId=${encodeURIComponent(panelId)}`, {
    method: 'DELETE',
    cache: 'no-store',
  });
  const payload = await response.json() as GiftTargetProgressResponse;
  if (!response.ok || payload.code !== 0 || !payload.progress) {
    throw new Error(payload.message || `礼物目标清零失败：HTTP ${response.status}`);
  }
  return payload.progress;
}
