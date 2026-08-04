import type {
  ActivityResultMode,
  ActivitySession,
  ActivityStatus,
  ActivityMilestone,
  AppState,
  Attribute,
} from './types';

export const MAX_ACTIVITY_ATTRIBUTES = 12;

export type ActivityTransitionAction = 'start' | 'lock' | 'settle' | 'reset';

const activityStatuses = new Set<ActivityStatus>(['not_started', 'active', 'locked', 'settled']);
const resultModes = new Set<ActivityResultMode>(['none', 'highest', 'lowest']);

export function normalizeActivities(
  activities: readonly Partial<ActivitySession>[] | undefined,
  attributes: readonly Attribute[],
  sceneIds: ReadonlySet<string>,
): ActivitySession[] {
  const attributeByName = new Map(attributes.map((attribute) => [attribute.name, attribute]));
  const ids = new Set<string>();
  const result: ActivitySession[] = [];
  for (const candidate of activities ?? []) {
    const id = String(candidate.id ?? '').trim();
    const name = String(candidate.name ?? '').trim();
    if (!id || !name || ids.has(id)) continue;
    const attributeNames = Array.from(new Set((candidate.attributeNames ?? [])
      .map((attributeName) => String(attributeName).trim())
      .filter((attributeName) => attributeByName.has(attributeName))))
      .slice(0, MAX_ACTIVITY_ATTRIBUTES);
    if (attributeNames.length === 0) continue;
    ids.add(id);
    const initialValues = Object.fromEntries(attributeNames.map((attributeName) => [
      attributeName,
      finiteNumber(candidate.initialValues?.[attributeName], attributeByName.get(attributeName)?.value ?? 0),
    ]));
    const resultValues = Object.fromEntries(attributeNames.flatMap((attributeName) => {
      const value = candidate.result?.values?.[attributeName];
      return Number.isFinite(value) ? [[attributeName, Number(value)]] : [];
    }));
    const winnerAttributeName = attributeNames.includes(candidate.result?.winnerAttributeName ?? '')
      ? candidate.result?.winnerAttributeName
      : undefined;
    const status = activityStatuses.has(candidate.status as ActivityStatus)
      ? candidate.status as ActivityStatus
      : 'not_started';
    const resultMode = resultModes.has(candidate.resultMode as ActivityResultMode)
      ? candidate.resultMode as ActivityResultMode
      : 'none';
    const sceneId = String(candidate.sceneId ?? '').trim();
    const timeoutSeconds = Math.floor(Number(candidate.giftTimeout?.seconds));
    const timeoutAction = candidate.giftTimeout?.action === 'settle' || candidate.giftTimeout?.action === 'reset'
      ? candidate.giftTimeout.action
      : 'lock';
    const timeoutLastGiftAt = Number(candidate.giftTimeout?.lastGiftAt);
    const timeoutDeadlineAt = Number(candidate.giftTimeout?.deadlineAt);
    const milestoneIds = new Set<string>();
    const milestones: ActivityMilestone[] = [];
    for (const milestone of candidate.milestones ?? []) {
      const milestoneId = String(milestone.id ?? '').trim();
      const milestoneName = String(milestone.name ?? '').trim();
      const attributeName = String(milestone.attributeName ?? '').trim();
      if (!milestoneId || !milestoneName || milestoneIds.has(milestoneId) || !attributeNames.includes(attributeName)) continue;
      milestoneIds.add(milestoneId);
      const threshold = finiteNumber(milestone.threshold, 0);
      const triggeredAt = Number(milestone.triggeredAt);
      const triggerValue = Number(milestone.triggerValue);
      milestones.push({
        id: milestoneId,
        name: milestoneName,
        attributeName,
        comparison: milestone.comparison === 'lte' ? 'lte' : 'gte',
        threshold,
        action: milestone.action === 'lock' || milestone.action === 'settle' ? milestone.action : 'announce',
        message: String(milestone.message ?? '').trim().slice(0, 120),
        ...(Number.isFinite(triggeredAt) && triggeredAt > 0 ? { triggeredAt } : {}),
        ...(Number.isFinite(triggerValue) ? { triggerValue } : {}),
      });
    }
    result.push({
      id,
      name,
      attributeNames,
      ...(sceneId && sceneIds.has(sceneId) ? { sceneId } : {}),
      status,
      resultMode,
      gateRules: candidate.gateRules === true,
      initialValues,
      milestones,
      ...(Number.isFinite(timeoutSeconds) && timeoutSeconds >= 1 && timeoutSeconds <= 86_400 ? {
        giftTimeout: {
          seconds: timeoutSeconds,
          action: timeoutAction,
          ...(status === 'active' && Number.isFinite(timeoutLastGiftAt) && timeoutLastGiftAt > 0 ? { lastGiftAt: timeoutLastGiftAt } : {}),
          ...(status === 'active' && Number.isFinite(timeoutDeadlineAt) && timeoutDeadlineAt > 0 ? { deadlineAt: timeoutDeadlineAt } : {}),
        },
      } : {}),
      ...finiteTimestamp(candidate.startedAt, 'startedAt'),
      ...finiteTimestamp(candidate.lockedAt, 'lockedAt'),
      ...finiteTimestamp(candidate.settledAt, 'settledAt'),
      ...(Object.keys(resultValues).length > 0 || winnerAttributeName
        ? { result: { values: resultValues, ...(winnerAttributeName ? { winnerAttributeName } : {}) } }
        : {}),
    });
  }
  return result;
}

export function createActivityId(): string {
  const uuid = globalThis.crypto?.randomUUID?.();
  return uuid ? `activity-${uuid}` : `activity-${Date.now()}-${Math.random().toString(36).slice(2, 9)}`;
}

export function createActivityMilestoneId(): string {
  const uuid = globalThis.crypto?.randomUUID?.();
  return uuid ? `milestone-${uuid}` : `milestone-${Date.now()}-${Math.random().toString(36).slice(2, 9)}`;
}

export function activityStatusLabel(status: ActivityStatus): string {
  if (status === 'active') return '进行中';
  if (status === 'locked') return '已锁定';
  if (status === 'settled') return '已结算';
  return '未开始';
}

export function activityForScene(state: AppState, sceneId: string | undefined): ActivitySession | undefined {
  if (!sceneId) return undefined;
  return state.activities.find((activity) => activity.sceneId === sceneId);
}

function finiteNumber(value: unknown, fallback: number): number {
  const result = Number(value);
  return Number.isFinite(result) ? result : fallback;
}

function finiteTimestamp<K extends 'startedAt' | 'lockedAt' | 'settledAt'>(value: unknown, key: K): Partial<Pick<ActivitySession, K>> {
  const timestamp = Number(value);
  return Number.isFinite(timestamp) && timestamp > 0 ? { [key]: timestamp } as Pick<ActivitySession, K> : {};
}
