import type { AppState, Attribute, GiftInfo, GiftRule, TimerRule } from '../../types';
import {
  maintainAttributeEditLease,
  type AttributeEditLeaseOptions,
  type AttributeEditLeaseSession,
} from './attribute-edit-lease';

const SESSION_ENDPOINT = '/api/attribute-edits/session';
const SUBMIT_ENDPOINT = '/api/attribute-edits';
const TOKEN_PATTERN = /^[A-Za-z0-9_-]{24}$/;

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
  const response = await post(options.fetchImpl ?? fetch, SESSION_ENDPOINT, payload);
  const session = await readPreparedSession(response);
  return {
    ...session,
    lease: maintainAttributeEditLease(session.attributeId, session.token, options),
  };
}

export async function submitAttributeEdit(
  input: AttributeEditInput,
  options: Pick<AttributeEditApiOptions, 'fetchImpl'> = {},
): Promise<SubmittedAttributeEdit> {
  validateEditInput(input);
  const response = await post(options.fetchImpl ?? fetch, SUBMIT_ENDPOINT, input);
  return readSubmittedEdit(response);
}

async function post(fetchImpl: typeof fetch, endpoint: string, payload: unknown): Promise<Response> {
  try {
    return await fetchImpl(endpoint, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(payload),
    });
  } catch {
    throw new Error('属性编辑请求失败');
  }
}

async function readPreparedSession(response: Response): Promise<Omit<PreparedAttributeEditSession, 'lease'>> {
  const payload = await readSuccessPayload(response);
  if (
    !isRecord(payload)
    || typeof payload.attributeId !== 'string'
    || !payload.attributeId.trim()
    || typeof payload.token !== 'string'
    || !TOKEN_PATTERN.test(payload.token)
    || typeof payload.expiresAt !== 'string'
    || !payload.expiresAt.trim()
    || !isAppState(payload.state)
  ) throw new Error('属性编辑响应无效');
  return {
    attributeId: payload.attributeId,
    token: payload.token,
    expiresAt: payload.expiresAt,
    state: payload.state,
  };
}

async function readSubmittedEdit(response: Response): Promise<SubmittedAttributeEdit> {
  const payload = await readSuccessPayload(response);
  if (!isRecord(payload) || !isRecord(payload.target) || !isAppState(payload.state)) {
    throw new Error('属性编辑响应无效');
  }
  const { id, name, created } = payload.target;
  if (typeof id !== 'string' || !id.trim() || typeof name !== 'string' || !name.trim() || typeof created !== 'boolean') {
    throw new Error('属性编辑响应无效');
  }
  return { target: { id, name, created }, state: payload.state };
}

async function readSuccessPayload(response: Response): Promise<unknown> {
  if (!response.ok) throw new Error('属性编辑请求失败');
  try {
    const payload = await response.json() as unknown;
    if (!isRecord(payload) || payload.code !== 0) throw new Error('属性编辑响应无效');
    return payload;
  } catch (error) {
    if (error instanceof Error && error.message === '属性编辑响应无效') throw error;
    throw new Error('属性编辑响应无效');
  }
}

function validateSessionTarget(target: AttributeEditSessionTarget): Record<string, string> {
  if (!isRecord(target)) throw new Error('属性编辑目标无效');
  const { attributeId, legacyName } = target as Record<string, unknown>;
  if (typeof attributeId === 'string' && attributeId.trim() && legacyName === undefined) return { attributeId };
  if (typeof legacyName === 'string' && legacyName.trim() && attributeId === undefined) return { legacyName };
  throw new Error('属性编辑目标无效');
}

function validateEditInput(input: AttributeEditInput): void {
  if (!isRecord(input) || !isRecord(input.target)) throw new Error('属性编辑目标无效');
  const { target } = input;
  if (target.kind === 'new' && Object.keys(target).length === 1) return;
  if (
    target.kind === 'existing'
    && typeof target.attributeId === 'string'
    && target.attributeId.trim()
    && typeof target.leaseToken === 'string'
    && TOKEN_PATTERN.test(target.leaseToken)
    && Object.keys(target).length === 3
  ) return;
  throw new Error('属性编辑目标无效');
}

function isAppState(value: unknown): value is AppState {
  if (!isRecord(value) || typeof value.roomId !== 'string') return false;
  const arrays = [
    value.attributes, value.displayScenes, value.giftKpiPanels, value.activities,
    value.rules, value.timerRules, value.formulaPresets, value.giftCatalog,
    value.recentGifts, value.log, value.giftReceipts,
  ];
  return arrays.every(Array.isArray)
    && isRecord(value.blindBoxDisplay)
    && isRecord(value.settings)
    && isRecord(value.stats)
    && isRecord(value.contributions)
    && (value.simplePlay === undefined || isRecord(value.simplePlay));
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value);
}
