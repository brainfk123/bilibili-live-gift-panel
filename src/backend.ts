import type { ActivitySession, ContributionLedger, GiftInfo } from './types';
import type { ActivityTransitionAction } from './activities';

export type RuntimeConnectionState = 'idle' | 'connecting' | 'connected' | 'reconnecting' | 'error';

export interface RuntimeStatus {
  state: RuntimeConnectionState;
  roomId: string;
  lastError?: string;
  lastGiftAt?: number;
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

export function startPagePresence(mode: PagePresenceMode): () => void {
  const EventSourceConstructor = globalThis.EventSource;
  if (typeof EventSourceConstructor !== 'function') return () => {};
  const sessionID = globalThis.crypto?.randomUUID?.() ?? `${Date.now()}-${Math.random().toString(36).slice(2)}`;
  const url = `/api/pages/presence/stream?mode=${mode}&id=${encodeURIComponent(sessionID)}`;
  let source: EventSource | undefined;
  const connect = (): void => {
    if (!source) source = new EventSourceConstructor(url);
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

interface UpdateResponse {
  code: number;
  update?: UpdateStatus;
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

export interface ActivityTransitionResult {
  activity: ActivitySession;
  attributeValues: Record<string, number>;
}

export interface BlindBoxLookup {
  info: import('./types').BlindBoxInfo | null;
  requiresLogin: boolean;
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
