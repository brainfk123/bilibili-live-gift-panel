import { describe, expect, it } from 'vitest';
import {
  getSimplePlayAttribute,
  isSimplePlayConfigurationIntact,
  planSimplePlayTransition,
  simplePlayDraftFromState,
  type SimplePlayDraft,
} from '../src/simple-play';
import { defaultState } from '../src/storage';
import type { AppState, SimplePlayTemplateId } from '../src/types';

function stateWithCatalog(): AppState {
  const state = defaultState();
  state.giftCatalog = [1, 2, 3, 4, 5].map((id) => ({
    id,
    name: `礼物 ${id}`,
    price: 1000,
    coinType: 'gold' as const,
    imgBasic: '',
  }));
  return state;
}

function draft(templateId: SimplePlayTemplateId): SimplePlayDraft {
  switch (templateId) {
    case 'overtime': return {
      templateId,
      parameters: { name: '加班时间', maxSeconds: 3600, broadcastMessage: '继续加油' },
      gifts: { overtime: [1, 2] },
      overtimeGiftActions: [
        { giftId: 1, operation: 'add', seconds: 60 },
        { giftId: 2, operation: 'halve' },
      ],
    };
    case 'counter': return {
      templateId,
      parameters: { name: '挑战次数', suffix: '次', amount: 1, cap: 0, broadcastMessage: '继续挑战' },
      gifts: { count: [1] },
    };
    case 'goal': return {
      templateId,
      parameters: { name: '应援目标', target: 100, perYuan: 1, broadcastMessage: '一起冲刺' },
      gifts: { progress: [1] },
    };
  }
}

describe('simple play transitions', () => {
  it.each(['overtime', 'counter', 'goal'] as const)('creates the %s play by appending managed artifacts', (templateId) => {
    const state = stateWithCatalog();
    state.settings.configExperience = 'advanced';
    state.attributes = [{ id: 'advanced-score', name: '积分', value: 7, unit: 'none', format: 'number', decimals: 0, suffix: '' }];
    state.rules = [{ id: 'advanced-rule', giftId: 5, attributeName: '积分', formula: '积分+1' }];

    const plan = planSimplePlayTransition(state, draft(templateId));

    expect(plan.impact.kind).toBe('create');
    expect(plan.nextState.attributes[0]).toEqual(state.attributes[0]);
    expect(plan.nextState.rules).toContainEqual(state.rules[0]);
    expect(plan.nextState.settings.configExperience).toBe('simple');
    expect(plan.nextState.simplePlay).toEqual(expect.objectContaining({ version: 1, templateId }));
    expect(getSimplePlayAttribute(plan.nextState)?.createdFromTemplateId).toBe(templateId);
    expect(isSimplePlayConfigurationIntact(plan.nextState)).toBe(true);
    expect(simplePlayDraftFromState(plan.nextState)?.gifts).toEqual(draft(templateId).gifts);
  });

  it('adjusts the same template while preserving id and clamping the live value to the new cap', () => {
    const created = planSimplePlayTransition(stateWithCatalog(), draft('overtime')).nextState;
    const managed = getSimplePlayAttribute(created)!;
    managed.value = 900;
    const unrelatedRule = { id: 'advanced-rule', giftId: 5, attributeName: '积分', formula: '积分+1' };
    created.rules.push(unrelatedRule);

    const nextDraft = draft('overtime');
    nextDraft.parameters.maxSeconds = 300;
    nextDraft.overtimeGiftActions = [{ giftId: 1, operation: 'double' }, { giftId: 2, operation: 'reset' }];
    const plan = planSimplePlayTransition(created, nextDraft);

    expect(plan.impact.kind).toBe('adjust');
    expect(plan.impact.attributesUpdated).toBe(1);
    expect(getSimplePlayAttribute(plan.nextState)).toEqual(expect.objectContaining({ id: managed.id, value: 300 }));
    expect(plan.nextState.rules).toContainEqual(unrelatedRule);
    expect(plan.nextState.rules.filter((rule) => rule.attributeName === '加班时间').map((rule) => rule.formula)).toEqual([
      'MIN(加班时间*2,300)',
      '0',
    ]);
    expect(isSimplePlayConfigurationIntact(plan.nextState)).toBe(true);
  });

  it('keeps advanced references, the OBS name, and paused state while adjusting the same play', () => {
    const created = planSimplePlayTransition(stateWithCatalog(), draft('overtime')).nextState;
    const managed = getSimplePlayAttribute(created)!;
    created.rules.forEach((rule) => { rule.enabled = false; });
    created.timerRules.forEach((rule) => { rule.enabled = false; });
    created.attributes.unshift({ id: 'score', name: '积分', value: 3, unit: 'none', format: 'number', decimals: 0, suffix: '' });
    const dependentRule = { id: 'advanced-dependent', giftId: 5, attributeName: '积分', formula: `积分+${managed.name}`, enabled: true };
    created.rules.push(dependentRule);
    created.displayScenes.push({ id: 'scene', name: '组合', attributeNames: [managed.name, '积分'], layout: 'grid', themeId: 'glass' });
    created.formulaPresets.push({ id: 'preset', name: '高级预设', context: 'gift', sourceAttributeName: '积分', formula: `积分+${managed.name}` });

    const nextDraft = draft('overtime');
    nextDraft.parameters.name = '会破坏旧链接的新名称';
    const plan = planSimplePlayTransition(created, nextDraft);

    expect(getSimplePlayAttribute(plan.nextState)?.name).toBe('加班时间');
    expect(plan.nextState.rules).toContainEqual(dependentRule);
    expect(plan.nextState.displayScenes[0].attributeNames).toEqual(['加班时间', '积分']);
    expect(plan.nextState.formulaPresets[0].id).toBe('preset');
    expect(plan.nextState.rules.filter((rule) => rule.id.startsWith('rule-simple-')).every((rule) => rule.enabled === false)).toBe(true);
    expect(plan.nextState.timerRules.every((rule) => rule.enabled === false)).toBe(true);
  });

  it('replaces across templates and cleans the old managed attribute reference graph only', () => {
    const created = planSimplePlayTransition(stateWithCatalog(), draft('overtime')).nextState;
    const managed = getSimplePlayAttribute(created)!;
    const unrelatedRule = { id: 'keep-rule', giftId: 5, attributeName: '积分', formula: '积分+1' };
    created.attributes.unshift({ id: 'advanced-score', name: '积分', value: 10, unit: 'none', format: 'number', decimals: 0, suffix: '' });
    created.rules.push(
      unrelatedRule,
      { id: 'dependent-rule', giftId: 4, attributeName: '积分', formula: `积分+${managed.name}` },
    );
    created.displayScenes.push({
      id: 'mixed-scene', name: '组合面板', attributeNames: [managed.name, '积分'], layout: 'grid', themeId: 'glass',
    });
    created.activities.push({
      id: 'mixed-activity', name: '组合活动', attributeNames: [managed.name, '积分'], sceneId: 'mixed-scene',
      status: 'not_started', resultMode: 'none', gateRules: false,
      initialValues: { [managed.name]: 0, 积分: 10 },
      milestones: [{
        id: 'old-milestone', name: '旧目标', attributeName: managed.name,
        comparison: 'gte', threshold: 100, action: 'announce', message: '达成',
      }],
    });
    created.formulaPresets.push({
      id: 'old-preset', name: '旧预设', context: 'gift', formula: `${managed.name}+1`, sourceAttributeName: managed.name,
    });

    const plan = planSimplePlayTransition(created, draft('counter'));

    expect(plan.impact.kind).toBe('replace');
    expect(plan.nextState.attributes.some((attribute) => attribute.id === managed.id)).toBe(false);
    expect(getSimplePlayAttribute(plan.nextState)?.name).toBe('挑战次数');
    expect(plan.nextState.rules).toContainEqual(unrelatedRule);
    expect(plan.nextState.rules.some((rule) => rule.id === 'dependent-rule')).toBe(false);
    expect(plan.nextState.displayScenes.find((scene) => scene.id === 'mixed-scene')?.attributeNames).toEqual(['积分']);
    expect(plan.nextState.activities.find((activity) => activity.id === 'mixed-activity')).toEqual(expect.objectContaining({
      attributeNames: ['积分'],
      initialValues: { 积分: 10 },
      milestones: [],
    }));
    expect(plan.nextState.formulaPresets.some((preset) => preset.id === 'old-preset')).toBe(false);
    expect(isSimplePlayConfigurationIntact(plan.nextState)).toBe(true);
  });

  it('detects advanced edits to managed formulas without treating live value changes as drift', () => {
    const state = planSimplePlayTransition(stateWithCatalog(), draft('counter')).nextState;
    getSimplePlayAttribute(state)!.value = 12;
    expect(isSimplePlayConfigurationIntact(state)).toBe(true);
    state.rules.forEach((rule) => { rule.enabled = false; });
    state.timerRules.forEach((rule) => { rule.enabled = false; });
    expect(isSimplePlayConfigurationIntact(state)).toBe(true);
    state.rules.find((rule) => rule.attributeName === '挑战次数')!.formula = '挑战次数+99';
    expect(isSimplePlayConfigurationIntact(state)).toBe(false);
  });
});
