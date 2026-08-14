import type { AppState } from '../../types';

const ENDPOINT = '/api/attribute-edit-lease';
const SESSION_ENDPOINT = '/api/attribute-edits/session';
const DEFAULT_HEARTBEAT_MS = 5_000;
const DEFAULT_RETRY_MS = 1_000;
const DEFAULT_REQUEST_TIMEOUT_MS = 4_000;

export type AttributeEditLeaseHealth = 'healthy' | 'retrying';

export interface AttributeEditLeaseSession {
  readonly attributeId: string;
  readonly token: string;
  release(): Promise<void>;
}

export interface AttributeEditLeaseOptions {
  fetchImpl?: typeof fetch;
  heartbeatMs?: number;
  retryMs?: number;
  requestTimeoutMs?: number;
  onHealthChange?: (health: AttributeEditLeaseHealth) => void;
}

type LeasePayload = { code?: unknown; token?: unknown; attributeId?: unknown };

export async function acquireAttributeEditLease(
  attributeId: string,
  options: AttributeEditLeaseOptions = {},
): Promise<AttributeEditLeaseSession> {
  const fetchImpl = options.fetchImpl ?? fetch;
  const payload = await requestLeasePayloadWithTimeout(
    fetchImpl,
    'POST',
    { attributeId },
    options.requestTimeoutMs ?? DEFAULT_REQUEST_TIMEOUT_MS,
  );
  const token = readLeaseToken(payload);

  return createLeaseSession(attributeId, token, fetchImpl, options);
}

/** Starts heartbeats for a token returned by an atomic edit session. */
export function maintainAttributeEditLease(
  attributeId: string,
  token: string,
  options: AttributeEditLeaseOptions = {},
): AttributeEditLeaseSession {
  if (!attributeId.trim() || !/^[A-Za-z0-9_-]{24}$/.test(token)) throw new Error('属性编辑租约响应无效');
  return createLeaseSession(attributeId, token, options.fetchImpl ?? fetch, options, true);
}

function createLeaseSession(
  attributeId: string,
  token: string,
  fetchImpl: typeof fetch,
  options: AttributeEditLeaseOptions,
  reacquireThroughSession = false,
): AttributeEditLeaseSession {
  const heartbeatMs = options.heartbeatMs ?? DEFAULT_HEARTBEAT_MS;
  const retryMs = options.retryMs ?? DEFAULT_RETRY_MS;
  const requestTimeoutMs = options.requestTimeoutMs ?? DEFAULT_REQUEST_TIMEOUT_MS;
  let currentToken = token;
  let released = false;
  let renewalPromise: Promise<void> | undefined;
  let retryTimer: ReturnType<typeof setTimeout> | undefined;
  let activeRequest: { controller: AbortController; timer: ReturnType<typeof setTimeout> } | undefined;
  let releasePromise: Promise<void> | undefined;
  let health: AttributeEditLeaseHealth = 'healthy';

  const reportHealth = (next: AttributeEditLeaseHealth, force = false): void => {
    if (!force && health === next) return;
    health = next;
    options.onHealthChange?.(next);
  };

  const clearRetry = (): void => {
    if (retryTimer === undefined) return;
    clearTimeout(retryTimer);
    retryTimer = undefined;
  };

  const abortActiveRequest = (): void => {
    if (!activeRequest) return;
    const request = activeRequest;
    activeRequest = undefined;
    clearTimeout(request.timer);
    request.controller.abort();
  };

  const requestRenewal = async (
    method: 'POST' | 'PUT',
    payload: Record<string, string>,
    endpoint = ENDPOINT,
  ): Promise<{ response: Response; payload?: LeasePayload }> => {
    const controller = new AbortController();
    const timer = setTimeout(() => controller.abort(), requestTimeoutMs);
    const ownedRequest = { controller, timer };
    activeRequest = ownedRequest;
    try {
      const response = await requestLease(fetchImpl, method, payload, false, controller.signal, endpoint);
      return {
        response,
        ...(response.status === 404 ? {} : { payload: await readSuccessPayload(response) }),
      };
    } finally {
      clearTimeout(timer);
      if (activeRequest === ownedRequest) activeRequest = undefined;
    }
  };

  const renew = (): Promise<void> => {
    if (released) return Promise.resolve();
    if (renewalPromise) return renewalPromise;
    const running = (async (): Promise<void> => {
      try {
        const { response } = await requestRenewal('PUT', { attributeId, token: currentToken });
        if (response.status === 404) {
          if (released) return;
          reportHealth('retrying');
          const reacquired = await requestRenewal(
            'POST',
            { attributeId },
            reacquireThroughSession ? SESSION_ENDPOINT : ENDPOINT,
          );
          const replacementToken = reacquireThroughSession
            ? parsePreparedAttributeEditSession(reacquired.payload, attributeId).token
            : readLeaseToken(reacquired.payload ?? {});
          if (released) {
            if (replacementToken !== currentToken) {
              await requestLeaseWithTimeout(
                fetchImpl,
                'DELETE',
                { attributeId, token: replacementToken },
                requestTimeoutMs,
                true,
              ).catch(() => undefined);
            }
            return;
          }
          currentToken = replacementToken;
        }
        if (released) return;
        clearRetry();
        reportHealth('healthy', true);
      } catch {
        if (released) return;
        reportHealth('retrying');
        if (retryTimer === undefined) {
          retryTimer = setTimeout(() => {
            retryTimer = undefined;
            void renew();
          }, retryMs);
        }
      }
    })();
    let tracked: Promise<void>;
    tracked = running.finally(() => {
      if (renewalPromise === tracked) renewalPromise = undefined;
    });
    renewalPromise = tracked;
    return tracked;
  };

  const heartbeatTimer = setInterval(() => { void renew(); }, heartbeatMs);
  const beforeUnload = (): void => { void release(); };
  globalThis.addEventListener?.('beforeunload', beforeUnload);

  const release = (): Promise<void> => {
    if (releasePromise) return releasePromise;
    released = true;
    const pendingRenewal = renewalPromise;
    clearInterval(heartbeatTimer);
    clearRetry();
    abortActiveRequest();
    globalThis.removeEventListener?.('beforeunload', beforeUnload);
    const deleteCurrent = requestLeaseWithTimeout(
      fetchImpl, 'DELETE', { attributeId, token: currentToken }, requestTimeoutMs, true,
    ).catch(() => undefined);
    releasePromise = Promise.allSettled([deleteCurrent, pendingRenewal ?? Promise.resolve()]).then(() => undefined);
    return releasePromise;
  };

  return {
    attributeId,
    get token() { return currentToken; },
    release,
  };
}

async function requestLease(
  fetchImpl: typeof fetch,
  method: 'POST' | 'PUT' | 'DELETE',
  payload: Record<string, string>,
  keepalive = false,
  signal?: AbortSignal,
  endpoint = ENDPOINT,
): Promise<Response> {
  return fetchImpl(endpoint, {
    method,
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify(payload),
    ...(keepalive ? { keepalive: true } : {}),
    ...(signal ? { signal } : {}),
  });
}

async function requestLeaseWithTimeout(
  fetchImpl: typeof fetch,
  method: 'POST' | 'PUT' | 'DELETE',
  payload: Record<string, string>,
  timeoutMs: number,
  keepalive = false,
): Promise<Response> {
  const controller = new AbortController();
  const timer = setTimeout(() => controller.abort(), timeoutMs);
  try {
    return await requestLease(fetchImpl, method, payload, keepalive, controller.signal);
  } finally {
    clearTimeout(timer);
  }
}

async function requestLeasePayloadWithTimeout(
  fetchImpl: typeof fetch,
  method: 'POST' | 'PUT',
  payload: Record<string, string>,
  timeoutMs: number,
): Promise<LeasePayload> {
  const controller = new AbortController();
  const timer = setTimeout(() => controller.abort(), timeoutMs);
  try {
    const response = await requestLease(fetchImpl, method, payload, false, controller.signal);
    return await readSuccessPayload(response);
  } finally {
    clearTimeout(timer);
  }
}

function readLeaseToken(payload: LeasePayload): string {
  if (typeof payload.token !== 'string' || !/^[A-Za-z0-9_-]{24}$/.test(payload.token)) {
    throw new Error('属性编辑租约响应无效');
  }
  return payload.token;
}

async function readSuccessPayload(response: Response): Promise<LeasePayload> {
  if (!response.ok) throw new Error('属性编辑租约请求失败');
  try {
    const payload = await response.json() as LeasePayload;
    if (!payload || payload.code !== 0) throw new Error('属性编辑租约响应无效');
    return payload;
  } catch (error) {
    if (error instanceof Error && error.message === '属性编辑租约响应无效') throw error;
    throw new Error('属性编辑租约响应无效');
  }
}

export interface ParsedPreparedAttributeEditSession {
  attributeId: string;
  token: string;
  expiresAt: string;
  state: AppState;
}

export function parsePreparedAttributeEditSession(
  value: unknown,
  expectedAttributeId?: string,
): ParsedPreparedAttributeEditSession {
  if (!isRecord(value) || !hasExactKeys(value, ['code', 'attributeId', 'token', 'expiresAt', 'state'])) {
    throw new Error('属性编辑响应无效');
  }
  const { code, attributeId, token, expiresAt, state } = value;
  if (
    code !== 0
    || typeof attributeId !== 'string'
    || !attributeId.trim()
    || (expectedAttributeId !== undefined && attributeId !== expectedAttributeId)
    || typeof token !== 'string'
    || !/^[A-Za-z0-9_-]{24}$/.test(token)
    || !isRFC3339Timestamp(expiresAt)
    || !isAppState(state)
  ) throw new Error('属性编辑响应无效');
  return { attributeId, token, expiresAt, state };
}

export function isAppState(value: unknown): value is AppState {
  if (!isRecord(value) || !hasExactKeys(value, [
    'roomId', 'attributes', 'displayScenes', 'blindBoxDisplay', 'giftKpiPanels',
    'activities', 'rules', 'timerRules', 'formulaPresets', 'settings', 'giftCatalog',
    'recentGifts', 'stats', 'log', 'giftReceipts', 'contributions', 'simplePlay',
  ], ['simplePlay'])) return false;
  return typeof value.roomId === 'string'
    && isArrayOf(value.attributes, isAttribute)
    && isArrayOf(value.displayScenes, isDisplayScene)
    && isDisplayAppearance(value.blindBoxDisplay, true)
    && isArrayOf(value.giftKpiPanels, isGiftKpiPanel)
    && isArrayOf(value.activities, isActivity)
    && isArrayOf(value.rules, isGiftRule)
    && isArrayOf(value.timerRules, isTimerRule)
    && isArrayOf(value.formulaPresets, isFormulaPreset)
    && isSettings(value.settings)
    && isArrayOf(value.giftCatalog, isGiftInfo)
    && isArrayOf(value.recentGifts, isRecentGift)
    && isDayStatsMap(value.stats)
    && isArrayOf(value.log, isLogEntry)
    && isArrayOf(value.giftReceipts, isGiftReceipt)
    && isContributionLedger(value.contributions)
    && (value.simplePlay === undefined || isSimplePlay(value.simplePlay));
}

function isAttribute(value: unknown): boolean {
  if (!isRecord(value)) return false;
  return optionalString(value.id)
    && requiredString(value.name)
    && finiteNumber(value.value)
    && oneOf(value.unit, ['seconds', 'none'])
    && oneOf(value.format, ['hhmmss', 'number', 'suffix'])
    && finiteNumber(value.decimals)
    && typeof value.suffix === 'string'
    && optionalString(value.color)
    && optionalString(value.broadcastMessage)
    && (value.display === undefined || isAttributeDisplay(value.display))
    && optionalString(value.createdFromTemplateId)
    && optionalNumber(value.createdFromTemplateVersion);
}

function isAttributeDisplay(value: unknown): boolean {
  if (!isRecord(value) || !oneOf(value.variant, ['number', 'timer', 'progress', 'health', 'resource', 'tug', 'enum'])) return false;
  return optionalOneOf(value.themeId, ['minimal', 'glass', 'rpg', 'pixel', 'neon', 'kawaii'])
    && (value.appearance === undefined || isDisplayAppearance(value.appearance))
    && optionalString(value.title)
    && optionalNumber(value.min) && optionalNumber(value.max) && optionalNumber(value.lowThreshold)
    && optionalString(value.leftLabel) && optionalString(value.rightLabel)
    && (value.valueMappings === undefined || isArrayOf(value.valueMappings, isValueMapping));
}

function isValueMapping(value: unknown): boolean {
  return isRecord(value) && finiteNumber(value.value) && requiredString(value.label)
    && optionalString(value.color) && optionalString(value.imageUrl);
}

function isDisplayAppearance(value: unknown, viewerSlots = false): boolean {
  return isRecord(value)
    && oneOf(value.themeId, ['minimal', 'glass', 'rpg', 'pixel', 'neon', 'kawaii'])
    && finiteNumber(value.fontSize) && typeof value.accentColor === 'string'
    && typeof value.showConnection === 'boolean' && oneOf(value.align, ['left', 'center', 'right'])
    && finiteNumber(value.panelOpacity) && (!viewerSlots || finiteNumber(value.viewerSlots));
}

function isDisplayScene(value: unknown): boolean {
  return isRecord(value) && requiredString(value.id) && requiredString(value.name)
    && isArrayOf(value.attributeNames, requiredString)
    && oneOf(value.layout, ['stack', 'grid', 'focus', 'versus', 'dashboard'])
    && oneOf(value.themeId, ['minimal', 'glass', 'rpg', 'pixel', 'neon', 'kawaii'])
    && (value.appearance === undefined || isDisplayAppearance(value.appearance));
}

function isGiftKpiPanel(value: unknown): boolean {
  return isRecord(value) && requiredString(value.id) && requiredString(value.name)
    && oneOf(value.layout, ['stack', 'grid', 'dashboard'])
    && isArrayOf(value.items, isGiftKpiItem) && isDisplayAppearance(value.appearance);
}

function isGiftKpiItem(value: unknown): boolean {
  return isRecord(value) && finiteNumber(value.giftId) && requiredString(value.giftName)
    && typeof value.imageUrl === 'string' && finiteNumber(value.target) && finiteNumber(value.received)
    && oneOf(value.barStyle, ['progress', 'resource', 'health']);
}

function isActivity(value: unknown): boolean {
  if (!isRecord(value)) return false;
  return requiredString(value.id) && requiredString(value.name)
    && isArrayOf(value.attributeNames, requiredString) && optionalString(value.sceneId)
    && oneOf(value.status, ['not_started', 'active', 'locked', 'settled'])
    && oneOf(value.resultMode, ['none', 'highest', 'lowest']) && typeof value.gateRules === 'boolean'
    && isNumberMap(value.initialValues) && isArrayOf(value.milestones, isActivityMilestone)
    && (value.giftTimeout === undefined || isActivityTimeout(value.giftTimeout))
    && optionalNumber(value.startedAt) && optionalNumber(value.lockedAt) && optionalNumber(value.settledAt)
    && (value.result === undefined || isActivityResult(value.result));
}

function isActivityMilestone(value: unknown): boolean {
  return isRecord(value) && requiredString(value.id) && requiredString(value.name)
    && requiredString(value.attributeName) && oneOf(value.comparison, ['gte', 'lte'])
    && finiteNumber(value.threshold) && oneOf(value.action, ['announce', 'lock', 'settle'])
    && typeof value.message === 'string' && optionalNumber(value.triggeredAt) && optionalNumber(value.triggerValue);
}

function isActivityTimeout(value: unknown): boolean {
  return isRecord(value) && finiteNumber(value.seconds) && oneOf(value.action, ['lock', 'settle', 'reset'])
    && optionalNumber(value.lastGiftAt) && optionalNumber(value.deadlineAt);
}

function isActivityResult(value: unknown): boolean {
  return isRecord(value) && optionalString(value.winnerAttributeName) && isNumberMap(value.values);
}

function isGiftRule(value: unknown): boolean {
  return isRecord(value) && requiredString(value.id) && finiteNumber(value.giftId)
    && requiredString(value.attributeName) && typeof value.formula === 'string'
    && optionalString(value.formulaName) && optionalString(value.condition)
    && optionalBoolean(value.enabled)
    && (value.matchGiftIds === undefined || isArrayOf(value.matchGiftIds, finiteNumber))
    && optionalNumber(value.minPrice) && optionalNumber(value.cap) && optionalNumber(value.dailyLimit);
}

function isTimerRule(value: unknown): boolean {
  return isRecord(value) && requiredString(value.id) && requiredString(value.attributeName)
    && typeof value.formulaName === 'string' && finiteNumber(value.intervalSeconds)
    && optionalString(value.condition) && typeof value.formula === 'string' && typeof value.enabled === 'boolean';
}

function isFormulaPreset(value: unknown): boolean {
  return isRecord(value) && requiredString(value.id) && requiredString(value.name)
    && oneOf(value.context, ['gift', 'timer']) && typeof value.formula === 'string'
    && requiredString(value.sourceAttributeName);
}

function isSettings(value: unknown): boolean {
  return isRecord(value) && finiteNumber(value.fontSize) && typeof value.accentColor === 'string'
    && typeof value.showStats === 'boolean' && typeof value.showConnection === 'boolean'
    && oneOf(value.align, ['left', 'center', 'right']) && oneOf(value.theme, ['dark', 'light'])
    && oneOf(value.giftView, ['list', 'grid']) && finiteNumber(value.panelOpacity)
    && oneOf(value.defaultDisplayThemeId, ['minimal', 'glass', 'rpg', 'pixel', 'neon', 'kawaii'])
    && typeof value.showTutorial === 'boolean' && finiteNumber(value.tutorialVersion)
    && isArrayOf(value.tutorialCompletedLessons, (entry) => oneOf(entry, [
      'room', 'attribute', 'template', 'basics', 'gift', 'rule', 'preset', 'timer', 'appearance', 'save', 'enable', 'output',
    ])) && typeof value.tutorialReplayMode === 'boolean'
    && optionalString(value.tutorialTargetAttributeId) && isArrayOf(value.trainingCompletedTopics, (entry) => oneOf(entry, [
      'multi-gift', 'blind-box', 'manual-gift', 'advanced-rule', 'cross-attribute', 'display-format',
      'broadcast-output', 'combined-scenes', 'activity-session', 'contribution-ranking', 'rule-no-effect',
      'timer-skipped', 'obs-no-change',
    ]))
    && typeof value.lastSeenChangelogVersion === 'string' && typeof value.autoUpdate === 'boolean'
    && oneOf(value.configExperience, ['simple', 'advanced']) && isCropMap(value.giftClipCrops);
}

function isCropMap(value: unknown): boolean {
  return isRecord(value) && Object.values(value).every((crop) => isRecord(crop)
    && finiteNumber(crop.x) && finiteNumber(crop.y) && finiteNumber(crop.width) && finiteNumber(crop.height));
}

function isGiftInfo(value: unknown): boolean {
  return isRecord(value) && finiteNumber(value.id) && requiredString(value.name) && finiteNumber(value.price)
    && oneOf(value.coinType, ['gold', 'silver']) && typeof value.imgBasic === 'string'
    && optionalString(value.gif) && optionalString(value.webp) && optionalNumber(value.animationDurationMs)
    && optionalNumber(value.effectId) && optionalString(value.effectMp4) && optionalString(value.effectMp4Json)
    && optionalBoolean(value.listed) && optionalBoolean(value.requiresLogin)
    && (value.specialEvent === undefined || oneOf(value.specialEvent, ['guard-captain', 'guard-admiral', 'guard-governor', 'super-chat']))
    && optionalNumber(value.blindBoxParentId) && optionalString(value.blindBoxParentName)
    && optionalNumber(value.blindBoxParentPrice);
}

function isRecentGift(value: unknown): boolean {
  return isGiftInfo(value) && isRecord(value) && finiteNumber(value.lastReceived) && finiteNumber(value.count);
}

function isDayStatsMap(value: unknown): boolean {
  return isRecord(value) && Object.values(value).every((day) => isRecord(day) && typeof day.date === 'string'
    && isNumberMap(day.giftTotals) && isNumberMap(day.ruleTriggers));
}

function isLogEntry(value: unknown): boolean {
  return isRecord(value) && finiteNumber(value.time) && finiteNumber(value.giftId)
    && typeof value.giftName === 'string' && finiteNumber(value.num) && typeof value.uname === 'string'
    && optionalString(value.avatar) && optionalNumber(value.senderUid) && typeof value.attributeName === 'string'
    && finiteNumber(value.delta) && finiteNumber(value.valueAfter) && typeof value.ruleId === 'string'
    && (value.source === undefined || oneOf(value.source, ['gift', 'timer']))
    && optionalString(value.triggerName) && optionalString(value.eventId);
}

function isGiftReceipt(value: unknown): boolean {
  return isRecord(value) && requiredString(value.id) && finiteNumber(value.time) && finiteNumber(value.giftId)
    && typeof value.giftName === 'string' && finiteNumber(value.num) && finiteNumber(value.price)
    && finiteNumber(value.totalCoin) && typeof value.coinType === 'string' && typeof value.uname === 'string'
    && optionalString(value.avatar) && optionalNumber(value.senderUid)
    && (value.membership === undefined || oneOf(value.membership, ['fan', 'captain', 'admiral', 'governor']))
    && optionalString(value.message) && optionalString(value.imgBasic)
    && (value.animation === undefined || isReceiptAnimation(value.animation))
    && isArrayOf(value.effects, isReceiptEffect);
}

function isReceiptAnimation(value: unknown): boolean {
  return isRecord(value) && optionalString(value.gif) && optionalString(value.webp)
    && finiteNumber(value.durationMs) && optionalNumber(value.effectId)
    && optionalString(value.mp4) && optionalString(value.mp4Json);
}

function isReceiptEffect(value: unknown): boolean {
  return isRecord(value) && requiredString(value.attributeName) && finiteNumber(value.delta)
    && finiteNumber(value.valueAfter) && requiredString(value.ruleId) && optionalString(value.triggerName);
}

function isContributionLedger(value: unknown): boolean {
  return isRecord(value) && isArrayOf(value.viewers, isViewerContribution) && optionalNumber(value.updatedAt);
}

function isViewerContribution(value: unknown): boolean {
  return isRecord(value) && requiredString(value.key) && optionalNumber(value.uid) && typeof value.uname === 'string'
    && optionalString(value.avatar) && finiteNumber(value.giftCount) && finiteNumber(value.goldValue)
    && finiteNumber(value.silverValue) && finiteNumber(value.ruleTriggers) && isNumberMap(value.attributeDeltas)
    && finiteNumber(value.blindBoxCount) && finiteNumber(value.blindBoxCost) && finiteNumber(value.blindBoxValue)
    && finiteNumber(value.blindBoxProfit) && optionalNumber(value.unpricedBlindBoxCount)
    && (value.blindBoxes === undefined || isArrayOf(value.blindBoxes, isBlindBoxContribution))
    && finiteNumber(value.lastGiftAt);
}

function isBlindBoxContribution(value: unknown): boolean {
  return isRecord(value) && finiteNumber(value.giftId) && typeof value.giftName === 'string'
    && finiteNumber(value.count) && finiteNumber(value.cost) && finiteNumber(value.value)
    && finiteNumber(value.profit) && optionalNumber(value.unpricedCount) && finiteNumber(value.lastGiftAt);
}

function isSimplePlay(value: unknown): boolean {
  return isRecord(value) && value.version === 1 && oneOf(value.templateId, ['overtime', 'counter', 'goal'])
    && finiteNumber(value.templateVersion) && requiredString(value.attributeId)
    && isPrimitiveMap(value.parameters) && isNumberArrayMap(value.gifts)
    && (value.overtimeGiftActions === undefined || isArrayOf(value.overtimeGiftActions, isOvertimeGiftAction))
    && requiredString(value.managedFingerprint);
}

function isOvertimeGiftAction(value: unknown): boolean {
  return isRecord(value) && finiteNumber(value.giftId)
    && oneOf(value.operation, ['add', 'subtract', 'double', 'halve', 'reset']) && optionalNumber(value.seconds);
}

function isRFC3339Timestamp(value: unknown): value is string {
  if (typeof value !== 'string') return false;
  const match = /^(\d{4})-(\d{2})-(\d{2})T(\d{2}):(\d{2}):(\d{2})(?:\.\d{1,9})?(?:Z|[+-](\d{2}):(\d{2}))$/.exec(value);
  if (!match) return false;
  const [year, month, day, hour, minute, second, offsetHour = 0, offsetMinute = 0] = match.slice(1).map(Number);
  if (month < 1 || month > 12 || hour > 23 || minute > 59 || second > 59 || offsetHour > 23 || offsetMinute > 59) {
    return false;
  }
  const leapYear = year % 4 === 0 && (year % 100 !== 0 || year % 400 === 0);
  const daysInMonth = [31, leapYear ? 29 : 28, 31, 30, 31, 30, 31, 31, 30, 31, 30, 31][month - 1];
  return day >= 1 && day <= daysInMonth && Number.isFinite(Date.parse(value));
}

function hasExactKeys(value: Record<string, unknown>, allowed: string[], optional: string[] = []): boolean {
  const allowedSet = new Set(allowed);
  return Object.keys(value).every((key) => allowedSet.has(key))
    && allowed.every((key) => optional.includes(key) || Object.prototype.hasOwnProperty.call(value, key));
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value);
}

function isArrayOf(value: unknown, predicate: (member: unknown) => boolean): boolean {
  return Array.isArray(value) && value.every(predicate);
}

function requiredString(value: unknown): value is string { return typeof value === 'string'; }
function optionalString(value: unknown): boolean { return value === undefined || typeof value === 'string'; }
function finiteNumber(value: unknown): value is number { return typeof value === 'number' && Number.isFinite(value); }
function optionalNumber(value: unknown): boolean { return value === undefined || finiteNumber(value); }
function optionalBoolean(value: unknown): boolean { return value === undefined || typeof value === 'boolean'; }
function oneOf(value: unknown, choices: readonly unknown[]): boolean { return choices.includes(value); }
function optionalOneOf(value: unknown, choices: readonly unknown[]): boolean { return value === undefined || oneOf(value, choices); }
function isNumberMap(value: unknown): boolean { return isRecord(value) && Object.values(value).every(finiteNumber); }
function isNumberArrayMap(value: unknown): boolean { return isRecord(value) && Object.values(value).every((entry) => isArrayOf(entry, finiteNumber)); }
function isPrimitiveMap(value: unknown): boolean {
  return isRecord(value) && Object.values(value).every((entry) => ['string', 'number', 'boolean'].includes(typeof entry)
    && (typeof entry !== 'number' || Number.isFinite(entry)));
}
