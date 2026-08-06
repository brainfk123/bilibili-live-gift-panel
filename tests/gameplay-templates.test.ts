import { describe, expect, it } from 'vitest';
import { evalFormula } from '../src/formula';
import {
  buildGameplayTemplate,
  createDefaultTemplateInput,
  GAMEPLAY_TEMPLATES,
  validateGameplayTemplateInput,
  type TemplateIdFactory,
} from '../src/gameplay-templates';
import type { GiftInfo } from '../src/types';

function gift(id: number, name = `礼物 ${id}`): GiftInfo {
  return { id, name, price: 1000, coinType: 'gold', imgBasic: '' };
}

function ids(): TemplateIdFactory {
  let sequence = 0;
  return { next: (kind) => `${kind}-${++sequence}` };
}

function validInput(template: (typeof GAMEPLAY_TEMPLATES)[number]) {
  const input = createDefaultTemplateInput(template);
  let giftId = 100;
  for (const slot of template.giftSlots) {
    input.gifts[slot.id] = Array.from({ length: slot.minimum }, () => gift(++giftId));
  }
  return input;
}

describe('gameplay templates', () => {
  it('ships the original and activity-aware prioritized templates', () => {
    expect(GAMEPLAY_TEMPLATES.map((template) => template.id)).toEqual([
      'overtime', 'countdown', 'counter', 'goal', 'boss', 'resource', 'tug',
      'team-duel', 'gift-vote', 'combo', 'milestone', 'random-event',
    ]);
  });

  it('builds every template without hard-coded gift IDs and with valid formulas', () => {
    for (const template of GAMEPLAY_TEMPLATES) {
      const input = validInput(template);
      const result = buildGameplayTemplate(template, input, ids());
      const environment = Object.fromEntries(result.attributes.map((attribute) => [attribute.name, attribute.value]));
      environment.price = 1000;

      expect(result.attributes.length).toBeGreaterThan(0);
      expect(result.rules.map((rule) => rule.giftId)).toEqual(result.usedGifts.map((item) => item.id));
      for (const attribute of result.attributes) {
        expect(attribute.createdFromTemplateId).toBe(template.id);
        expect(attribute.display?.themeId).toBe(template.recommendedThemeId);
      }
      for (const scene of result.displayScenes) {
        expect(scene.attributeNames.every((name) => result.attributes.some((attribute) => attribute.name === name))).toBe(true);
      }
      for (const activity of result.activities) {
        expect(activity.attributeNames.every((name) => result.attributes.some((attribute) => attribute.name === name))).toBe(true);
        expect(activity.milestones.every((milestone) => activity.attributeNames.includes(milestone.attributeName))).toBe(true);
      }
      for (const rule of result.rules) expect(Number.isFinite(evalFormula(rule.formula, environment))).toBe(true);
      for (const rule of result.timerRules) {
        expect(Number.isFinite(evalFormula(rule.formula, environment))).toBe(true);
        if (rule.condition) expect(typeof evalFormula(rule.condition, environment)).toBe('number');
      }
    }
  });

  it('creates a complete overtime setup transaction', () => {
    const template = GAMEPLAY_TEMPLATES[0];
    const input = validInput(template);
    input.parameters.minutesPerYuan = 120;
    input.parameters.maxHours = 2;
    const result = buildGameplayTemplate(template, input, ids());

    expect(result.attributes[0]).toEqual(expect.objectContaining({
      name: '加班时间',
      value: 0,
      format: 'hhmmss',
      display: expect.objectContaining({ variant: 'timer', max: 7200 }),
    }));
    expect(result.rules[0].formula).toBe('MIN(加班时间+price/1000*120,7200)');
    expect(result.timerRules[0]).toEqual(expect.objectContaining({
      formulaName: '每秒自动减少',
      intervalSeconds: 1,
      condition: '加班时间>0',
      formula: 'MAX(加班时间-1,0)',
    }));
    expect(result.summary).toContain('每秒自动减少 1 秒');
  });

  it('requires each mandatory role and prevents ambiguous duplicate role assignments', () => {
    const template = GAMEPLAY_TEMPLATES.find((item) => item.id === 'tug')!;
    const input = createDefaultTemplateInput(template);
    expect(validateGameplayTemplateInput(template, input)).toEqual(expect.arrayContaining([
      '推向左侧至少选择 1 个礼物',
      '推向右侧至少选择 1 个礼物',
    ]));

    const duplicatedGift = gift(999, '同一个礼物');
    input.gifts.left = [duplicatedGift];
    input.gifts.right = [duplicatedGift];
    expect(validateGameplayTemplateInput(template, input)).toContain('同一个礼物不能同时分配给多个角色');
  });

  it('builds optional Boss healing and regeneration only when configured', () => {
    const template = GAMEPLAY_TEMPLATES.find((item) => item.id === 'boss')!;
    const input = validInput(template);
    input.gifts.heal = [gift(404, '治疗礼物')];
    input.parameters.regenEnabled = true;
    const result = buildGameplayTemplate(template, input, ids());

    expect(result.rules.some((rule) => rule.formulaName?.includes('治疗') === true)).toBe(true);
    expect(result.timerRules).toHaveLength(1);
    expect(result.attributes[0].display).toEqual(expect.objectContaining({ variant: 'health', themeId: 'rpg' }));
  });

  it('builds a team duel with an OBS scene and automatic settlement milestones', () => {
    const template = GAMEPLAY_TEMPLATES.find((item) => item.id === 'team-duel')!;
    const result = buildGameplayTemplate(template, validInput(template), ids());

    expect(result.attributes.map((attribute) => attribute.name)).toEqual(['红队', '蓝队']);
    expect(result.displayScenes).toHaveLength(1);
    expect(result.displayScenes[0].layout).toBe('versus');
    expect(result.activities[0]).toEqual(expect.objectContaining({
      status: 'not_started', resultMode: 'highest', gateRules: true,
    }));
    expect(result.activities[0].milestones).toHaveLength(2);
    expect(result.activities[0].milestones.every((milestone) => milestone.action === 'settle')).toBe(true);
  });

  it('builds a resettable combo timeout and an enum-based random event', () => {
    const combo = GAMEPLAY_TEMPLATES.find((item) => item.id === 'combo')!;
    const comboResult = buildGameplayTemplate(combo, validInput(combo), ids());
    expect(comboResult.activities[0].giftTimeout).toEqual({ seconds: 15, action: 'settle' });

    const random = GAMEPLAY_TEMPLATES.find((item) => item.id === 'random-event')!;
    const randomResult = buildGameplayTemplate(random, validInput(random), ids());
    expect(randomResult.rules[0].formula).toBe('RANDBETWEEN(1,4)');
    expect(randomResult.attributes[0].display).toEqual(expect.objectContaining({
      variant: 'enum', valueMappings: expect.arrayContaining([expect.objectContaining({ value: 1, label: '主播喝水' })]),
    }));
  });
});
