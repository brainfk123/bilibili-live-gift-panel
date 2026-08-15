import type { AppState, Attribute, GiftInfo, GiftRule, TimerRule } from '../../types';
import {
  bestEffortReleasePreparedAttributeEditLease,
  isAttribute,
  isAttributeEditGiftCatalogUpsert,
  isGiftRule,
  isTimerRule,
  maintainAttributeEditLease,
  parseAppState,
  parsePreparedAttributeEditSession,
  type AttributeEditLeaseOptions,
  type AttributeEditLeaseSession,
} from './attribute-edit-lease';

const SESSION_ENDPOINT = '/api/attribute-edits/session';
const SUBMIT_ENDPOINT = '/api/attribute-edits';
const TOKEN_PATTERN = /^[A-Za-z0-9_-]{24}$/;
const DEFAULT_REQUEST_TIMEOUT_MS = 4_000;

class OwnedRequestTimeout {}

export type AttributeEditSessionTarget =
  | { attributeId: string }
  | { legacyName: string };

export type AttributeEditTarget =
  | { kind: 'existing'; attributeId: string; leaseToken: string }
  | { kind: 'new' };

export type AttributeEditGiftCatalogUpsert = Pick<GiftInfo, 'id' | 'name' | 'price' | 'coinType' | 'imgBasic'>
  & Partial<Pick<GiftInfo,
    | 'gif'
    | 'webp'
    | 'animationDurationMs'
    | 'effectId'
    | 'effectMp4'
    | 'effectMp4Json'
    | 'blindBoxParentId'
    | 'blindBoxParentName'
    | 'blindBoxParentPrice'>>;

export interface AttributeEditInput {
  target: AttributeEditTarget;
  attribute: Attribute;
  giftRules: GiftRule[];
  timerRules: TimerRule[];
  giftCatalogUpserts: AttributeEditGiftCatalogUpsert[];
}

export interface PreparedAttributeEditSession {
  attributeId: string;
  token: string;
  expiresAt: string;
  state: AppState;
  lease: AttributeEditLeaseSession;
}

export interface SubmittedAttributeEdit {
  target: { id: string; name: string; created: boolean };
  state: AppState;
}

export type AttributeEditApiOptions = AttributeEditLeaseOptions;

export async function prepareAttributeEditSession(
  target: AttributeEditSessionTarget,
  options: AttributeEditApiOptions = {},
): Promise<PreparedAttributeEditSession> {
  const payload = validateSessionTarget(target);
  const response = await postJSON(
    options.fetchImpl ?? fetch,
    SESSION_ENDPOINT,
    payload,
    options.requestTimeoutMs ?? DEFAULT_REQUEST_TIMEOUT_MS,
    (latePayload) => bestEffortReleasePreparedAttributeEditLease(latePayload, options),
  );
  let session;
  try {
    session = parsePreparedAttributeEditSession(response, payload.attributeId);
  } catch (error) {
    await bestEffortReleasePreparedAttributeEditLease(response, options);
    throw error;
  }
  return {
    ...session,
    lease: maintainAttributeEditLease(session.attributeId, session.token, options),
  };
}

export async function submitAttributeEdit(
  input: AttributeEditInput,
  options: Pick<AttributeEditApiOptions, 'fetchImpl' | 'requestTimeoutMs'> = {},
): Promise<SubmittedAttributeEdit> {
  validateEditInput(input);
  const response = await postJSON(
    options.fetchImpl ?? fetch,
    SUBMIT_ENDPOINT,
    input,
    options.requestTimeoutMs ?? DEFAULT_REQUEST_TIMEOUT_MS,
  );
  return readSubmittedEdit(response);
}

async function postJSON(
  fetchImpl: typeof fetch,
  endpoint: string,
  payload: unknown,
  timeoutMs: number,
  onLatePayload?: (payload: unknown) => void | Promise<void>,
): Promise<unknown> {
  const controller = new AbortController();
  const timeout = new OwnedRequestTimeout();
  let expired = false;
  let timer: ReturnType<typeof setTimeout>;
  const timeoutPromise = new Promise<never>((_resolve, reject) => {
    timer = setTimeout(() => {
      expired = true;
      controller.abort(timeout);
      reject(timeout);
    }, timeoutMs);
  });
  const request = (async (): Promise<unknown> => {
    let response: Response;
    try {
      response = await Promise.resolve().then(() => fetchImpl(endpoint, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(payload),
        signal: controller.signal,
      }));
    } catch {
      throw new Error('属性编辑请求失败');
    }
    if (!response.ok) throw new Error('属性编辑请求失败');
    let responsePayload: unknown;
    try {
      responsePayload = await response.json() as unknown;
    } catch {
      throw new Error('属性编辑响应无效');
    }
    if (expired && onLatePayload) await onLatePayload(responsePayload);
    return responsePayload;
  })();
  void request.catch(() => undefined);
  try {
    try {
      return await Promise.race([request, timeoutPromise]);
    } catch (error) {
      if (error !== timeout) throw error;
      throw new Error('属性编辑请求失败');
    }
  } finally {
    clearTimeout(timer!);
  }
}

function readSubmittedEdit(payload: unknown): SubmittedAttributeEdit {
  if (!isRecord(payload) || !hasExactKeys(payload, ['code', 'target', 'state']) || payload.code !== 0
    || !isRecord(payload.target) || !hasExactKeys(payload.target, ['id', 'name', 'created'])) {
    throw new Error('属性编辑响应无效');
  }
  const { id, name, created } = payload.target;
  if (typeof id !== 'string' || !id.trim() || typeof name !== 'string' || !name.trim() || typeof created !== 'boolean') {
    throw new Error('属性编辑响应无效');
  }
  return { target: { id, name, created }, state: parseAppState(payload.state) };
}

function validateSessionTarget(target: AttributeEditSessionTarget): Record<string, string> {
  if (!isRecord(target)) throw new Error('属性编辑目标无效');
  const { attributeId, legacyName } = target as Record<string, unknown>;
  if (typeof attributeId === 'string' && attributeId.trim() && hasExactKeys(target, ['attributeId'])) return { attributeId };
  if (typeof legacyName === 'string' && legacyName.trim() && hasExactKeys(target, ['legacyName'])) return { legacyName };
  throw new Error('属性编辑目标无效');
}

function validateEditInput(input: AttributeEditInput): void {
  if (!isRecord(input) || !isRecord(input.target)) throw new Error('属性编辑目标无效');
  const { target } = input;
  const validTarget = target.kind === 'new' && hasExactKeys(target, ['kind']) || (
    target.kind === 'existing'
    && typeof target.attributeId === 'string'
    && target.attributeId.trim()
    && typeof target.leaseToken === 'string'
    && TOKEN_PATTERN.test(target.leaseToken)
    && hasExactKeys(target, ['kind', 'attributeId', 'leaseToken'])
  );
  if (!validTarget) throw new Error('属性编辑目标无效');
  if (!hasExactKeys(input, ['target', 'attribute', 'giftRules', 'timerRules', 'giftCatalogUpserts'])
    || !isAttribute(input.attribute) || !isArrayOf(input.giftRules, isGiftRule)
    || !isArrayOf(input.timerRules, isTimerRule)
    || !isArrayOf(input.giftCatalogUpserts, isAttributeEditGiftCatalogUpsert)) {
    throw new Error('属性编辑请求无效');
  }
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value);
}

function hasExactKeys(value: Record<string, unknown>, keys: string[]): boolean {
  return Object.keys(value).length === keys.length && keys.every((key) => Object.prototype.hasOwnProperty.call(value, key));
}

function isArrayOf(value: unknown, predicate: (member: unknown) => boolean): boolean {
  return Array.isArray(value) && value.every(predicate);
}
