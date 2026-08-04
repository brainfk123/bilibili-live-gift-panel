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
  it('ships the seven prioritized templates', () => {
    expect(GAMEPLAY_TEMPLATES.map((template) => template.id)).toEqual([
      'overtime', 'countdown', 'counter', 'goal', 'boss', 'resource', 'tug',
    ]);
  });

  it('builds every template without hard-coded gift IDs and with valid formulas', () => {
    for (const template of GAMEPLAY_TEMPLATES) {
      const input = validInput(template);
      const result = buildGameplayTemplate(template, input, ids());
      const attribute = result.attributes[0];
      const environment = { [attribute.name]: attribute.value, price: 1000 };

      expect(result.attributes).toHaveLength(1);
      expect(result.rules.map((rule) => rule.giftId)).toEqual(result.usedGifts.map((item) => item.id));
      expect(attribute.createdFromTemplateId).toBe(template.id);
      expect(attribute.display?.themeId).toBe(template.recommendedThemeId);
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
      intervalSeconds: 60,
      condition: '加班时间>0',
      formula: 'MAX(加班时间-60,0)',
    }));
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
});
