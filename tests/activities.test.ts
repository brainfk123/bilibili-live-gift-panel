import { describe, expect, it, vi } from 'vitest';
import { activityForScene, activityStatusLabel, createActivityId, createActivityMilestoneId, normalizeActivities } from '../src/activities';
import { defaultState } from '../src/storage';

describe('activity session model', () => {
  it('normalizes references, values, timestamps, and invalid status', () => {
    const attributes = [
      { name: '红队', value: 3, unit: 'none', format: 'number', decimals: 0, suffix: '' },
      { name: '蓝队', value: 7, unit: 'none', format: 'number', decimals: 0, suffix: '' },
    ] as const;
    const activities = normalizeActivities([{
      id: ' match ', name: ' 对抗 ', attributeNames: ['红队', '不存在', '蓝队', '红队'],
      sceneId: 'scene-match', status: 'broken' as any, resultMode: 'highest', gateRules: true,
      initialValues: { 红队: 0, 蓝队: Number.NaN }, startedAt: -1,
      milestones: [{
        id: ' target ', name: ' 达标 ', attributeName: '红队', comparison: 'gte', threshold: 10,
        action: 'settle', message: ' 达成！ ', triggeredAt: 100, triggerValue: 12,
      }, {
        id: 'invalid', name: '无效', attributeName: '不存在', comparison: 'gte', threshold: 1, action: 'announce', message: '',
      }],
      giftTimeout: { seconds: 30, action: 'settle', lastGiftAt: 90, deadlineAt: 120 },
      result: { winnerAttributeName: '不存在', values: { 红队: 4, 不存在: 10 } },
    }], attributes as any, new Set(['scene-match']));

    expect(activities).toEqual([expect.objectContaining({
      id: 'match', name: '对抗', attributeNames: ['红队', '蓝队'], sceneId: 'scene-match',
      status: 'not_started', resultMode: 'highest', gateRules: true, initialValues: { 红队: 0, 蓝队: 7 },
      milestones: [{
        id: 'target', name: '达标', attributeName: '红队', comparison: 'gte', threshold: 10,
        action: 'settle', message: '达成！', triggeredAt: 100, triggerValue: 12,
      }],
      giftTimeout: { seconds: 30, action: 'settle' },
      result: { values: { 红队: 4 } },
    })]);
    expect(activities[0].startedAt).toBeUndefined();
  });

  it('finds the activity linked to a combination scene', () => {
    const state = defaultState();
    state.activities = [{
      id: 'activity-1', name: '比赛', attributeNames: ['A'], sceneId: 'scene-1', status: 'active',
      resultMode: 'none', gateRules: false, initialValues: { A: 0 }, milestones: [],
    }];
    expect(activityForScene(state, 'scene-1')?.name).toBe('比赛');
    expect(activityStatusLabel('locked')).toBe('已锁定');
  });

  it('creates stable prefixed IDs', () => {
    vi.stubGlobal('crypto', { randomUUID: () => 'uuid' });
    expect(createActivityId()).toBe('activity-uuid');
    expect(createActivityMilestoneId()).toBe('milestone-uuid');
    vi.unstubAllGlobals();
  });
});
