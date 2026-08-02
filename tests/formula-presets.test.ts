import { describe, expect, it } from 'vitest';
import { applyFormulaPreset, replaceFormulaVariable, saveFormulaPreset } from '../src/formula-presets';
import type { FormulaPreset } from '../src/types';

const giftPreset: FormulaPreset = {
  id: 'gift-1',
  name: '按价格加时',
  context: 'gift',
  formula: '加班时间+price/1000*60',
  sourceAttributeName: '加班时间',
};

describe('formula presets', () => {
  it('saves a new preset and updates a same-name preset in the same context', () => {
    const created = saveFormulaPreset([], {
      name: ' 按价格加时 ',
      context: 'gift',
      formula: ' 加班时间+price/1000*60 ',
      sourceAttributeName: ' 加班时间 ',
    });

    expect(created.created).toBe(true);
    expect(created.preset).toMatchObject({
      name: giftPreset.name,
      context: giftPreset.context,
      formula: giftPreset.formula,
      sourceAttributeName: giftPreset.sourceAttributeName,
    });
    expect(created.preset.id).not.toBe('');

    const updated = saveFormulaPreset(created.presets, {
      name: '按价格加时',
      context: 'gift',
      formula: '加班时间+price/1000*30',
      sourceAttributeName: '加班时间',
    });

    expect(updated.created).toBe(false);
    expect(updated.presets).toHaveLength(1);
    expect(updated.preset.id).toBe(created.preset.id);
    expect(updated.preset.formula).toBe('加班时间+price/1000*30');
  });

  it('allows the same preset name in gift and timer contexts', () => {
    const timer = saveFormulaPreset([giftPreset], {
      name: giftPreset.name,
      context: 'timer',
      formula: 'MAX(加班时间-60,0)',
      sourceAttributeName: '加班时间',
    });

    expect(timer.presets).toHaveLength(2);
    expect(timer.preset.context).toBe('timer');
  });

  it('applies a preset to another attribute without replacing partial identifiers', () => {
    const preset: FormulaPreset = {
      ...giftPreset,
      formula: '加班时间+加班时间上限+price',
    };

    expect(applyFormulaPreset(preset, '欢乐值')).toBe('欢乐值+加班时间上限+price');
    expect(replaceFormulaVariable('积分+总积分+积分_2', '积分', '能量')).toBe('能量+总积分+积分_2');
  });

  it('rejects empty preset fields', () => {
    expect(() => saveFormulaPreset([], {
      name: ' ',
      context: 'gift',
      formula: '积分+1',
      sourceAttributeName: '积分',
    })).toThrow('预设名称不能为空');
  });
});
