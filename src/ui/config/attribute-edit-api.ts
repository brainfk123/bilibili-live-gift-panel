import type { AppState, Attribute, GiftInfo, GiftRule, TimerRule } from '../../types';
import {
  isAppState,
  maintainAttributeEditLease,
  parsePreparedAttributeEditSession,
  type AttributeEditLeaseOptions,
  type AttributeEditLeaseSession,
} from './attribute-edit-lease';

const SESSION_ENDPOINT = '/api/attribute-edits/session';
const SUBMIT_ENDPOINT = '/api/attribute-edits';
const TOKEN_PATTERN = /^[A-Za-z0-9_-]{24}$/;
const DEFAULT_REQUEST_TIMEOUT_MS = 4_000;

export type AttributeEditSessionTarget =
  | { attributeId: string }
  | { legacyName: string };

export type AttributeEditTarget =
  | { kind: 'existing'; attributeId: string; leaseToken: string }
  | { kind: 'new' };

export interface AttributeEditInput {
  target: AttributeEditTarget;
  attribute: Attribute;
  giftRules: GiftRule[];
  timerRules: TimerRule[];
  giftCatalogUpserts: GiftInfo[];
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
  );
  const session = parsePreparedAttributeEditSession(response, payload.attributeId);
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
): Promise<unknown> {
  const controller = new AbortController();
  const timer = setTimeout(() => controller.abort(), timeoutMs);
  try {
    let response: Response;
    try {
      response = await fetchImpl(endpoint, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(payload),
        signal: controller.signal,
      });
    } catch {
      throw new Error('属性编辑请求失败');
    }
    if (controller.signal.aborted || !response.ok) throw new Error('属性编辑请求失败');
    try {
      const body = await response.json() as unknown;
      if (controller.signal.aborted) throw new Error('属性编辑请求失败');
      return body;
    } catch (error) {
      if (error instanceof Error && error.message === '属性编辑请求失败') throw error;
      if (controller.signal.aborted) throw new Error('属性编辑请求失败');
      throw new Error('属性编辑响应无效');
    }
  } finally {
    clearTimeout(timer);
  }
}

function readSubmittedEdit(payload: unknown): SubmittedAttributeEdit {
  if (!isRecord(payload) || !hasExactKeys(payload, ['code', 'target', 'state']) || payload.code !== 0
    || !isRecord(payload.target) || !hasExactKeys(payload.target, ['id', 'name', 'created'])
    || !isAppState(payload.state)) {
    throw new Error('属性编辑响应无效');
  }
  const { id, name, created } = payload.target;
  if (typeof id !== 'string' || !id.trim() || typeof name !== 'string' || !name.trim() || typeof created !== 'boolean') {
    throw new Error('属性编辑响应无效');
  }
  return { target: { id, name, created }, state: payload.state };
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
    || !isRecord(input.attribute) || !Array.isArray(input.giftRules)
    || !Array.isArray(input.timerRules) || !Array.isArray(input.giftCatalogUpserts)) {
    throw new Error('属性编辑请求无效');
  }
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value);
}

function hasExactKeys(value: Record<string, unknown>, keys: string[]): boolean {
  return Object.keys(value).length === keys.length && keys.every((key) => Object.prototype.hasOwnProperty.call(value, key));
}
