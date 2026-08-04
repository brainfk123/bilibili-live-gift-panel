import type {
  Attribute,
  DisplayThemeId,
  GiftInfo,
  GiftRule,
  TimerRule,
} from './types';

export type GameplayTemplateCategory = 'timer' | 'goal' | 'challenge' | 'survival' | 'versus';
export type TemplateParameterKind = 'text' | 'number' | 'duration' | 'select' | 'toggle';
export type TemplateParameterValue = string | number | boolean;

export interface TemplateParameterOption {
  value: string;
  label: string;
}

export interface TemplateParameterDefinition {
  id: string;
  label: string;
  kind: TemplateParameterKind;
  defaultValue: TemplateParameterValue;
  description?: string;
  min?: number;
  max?: number;
  step?: number;
  unit?: string;
  durationUnit?: 'seconds' | 'minutes';
  options?: TemplateParameterOption[];
}

export interface TemplateGiftSlotDefinition {
  id: string;
  label: string;
  description: string;
  minimum: number;
  multiple: boolean;
}

export interface GameplayTemplateInput {
  parameters: Record<string, TemplateParameterValue>;
  gifts: Record<string, GiftInfo[]>;
  displayThemeId?: DisplayThemeId;
}

export interface GameplayTemplateBuildResult {
  attributes: Attribute[];
  rules: GiftRule[];
  timerRules: TimerRule[];
  usedGifts: GiftInfo[];
  summary: string[];
}

export interface GameplayTemplateDefinition {
  id: string;
  version: number;
  category: GameplayTemplateCategory;
  title: string;
  summary: string;
  audiencePlay: string;
  difficulty: '简单' | '进阶';
  preview: 'timer' | 'counter' | 'progress' | 'health' | 'resource' | 'tug';
  recommendedThemeId: DisplayThemeId;
  parameters: readonly TemplateParameterDefinition[];
  giftSlots: readonly TemplateGiftSlotDefinition[];
  build: (input: GameplayTemplateInput, ids: TemplateIdFactory) => GameplayTemplateBuildResult;
}

export interface TemplateIdFactory {
  next: (kind: 'rule' | 'timer') => string;
}

function valueText(input: GameplayTemplateInput, id: string): string {
  return String(input.parameters[id] ?? '').trim();
}

function valueNumber(input: GameplayTemplateInput, id: string): number {
  const value = Number(input.parameters[id]);
  return Number.isFinite(value) ? value : 0;
}

function valueBoolean(input: GameplayTemplateInput, id: string): boolean {
  return input.parameters[id] === true;
}

function formulaNumber(value: number): string {
  return String(Math.round(value * 1_000_000) / 1_000_000);
}

function themed(input: GameplayTemplateInput, fallback: DisplayThemeId): DisplayThemeId {
  return input.displayThemeId ?? fallback;
}

function uniqueGifts(input: GameplayTemplateInput): GiftInfo[] {
  const byId = new Map<number, GiftInfo>();
  for (const gifts of Object.values(input.gifts)) {
    for (const gift of gifts) byId.set(gift.id, gift);
  }
  return Array.from(byId.values());
}

function rulesForSlot(
  input: GameplayTemplateInput,
  slotId: string,
  attributeName: string,
  label: string,
  formula: string,
  ids: TemplateIdFactory,
): GiftRule[] {
  return (input.gifts[slotId] ?? []).map((gift) => ({
    id: ids.next('rule'),
    giftId: gift.id,
    attributeName,
    formulaName: `${gift.name}·${label}`,
    formula,
    enabled: true,
  }));
}

function timer(
  ids: TemplateIdFactory,
  attributeName: string,
  formulaName: string,
  intervalSeconds: number,
  formula: string,
  condition = '',
): TimerRule {
  return {
    id: ids.next('timer'),
    attributeName,
    formulaName,
    intervalSeconds: Math.max(1, Math.round(intervalSeconds)),
    condition,
    formula,
    enabled: true,
  };
}

function attributeBase(
  name: string,
  value: number,
  format: Attribute['format'],
  suffix: string,
  broadcastMessage: string,
): Pick<Attribute, 'name' | 'value' | 'unit' | 'format' | 'decimals' | 'suffix' | 'broadcastMessage'> {
  return {
    name,
    value,
    unit: format === 'hhmmss' ? 'seconds' : 'none',
    format,
    decimals: 0,
    suffix,
    broadcastMessage,
  };
}

const commonBroadcast: TemplateParameterDefinition = {
  id: 'broadcastMessage',
  label: '默认播报消息',
  kind: 'text',
  defaultValue: '感谢大家的支持，欢迎投喂礼物',
  description: '没有新礼物时在 OBS 面板底部滚动显示。',
};

const TEMPLATES: readonly GameplayTemplateDefinition[] = [
  {
    id: 'overtime', version: 1, category: 'timer', title: '加班机', difficulty: '简单', preview: 'timer',
    summary: '礼物增加加班时间，后台按固定速度自动减少。',
    audiencePlay: '观众投喂礼物增加直播时长。',
    recommendedThemeId: 'glass',
    parameters: [
      { id: 'name', label: '属性名称', kind: 'text', defaultValue: '加班时间' },
      { id: 'minutesPerYuan', label: '每 1 元增加', kind: 'duration', defaultValue: 60, min: 1, max: 3600, durationUnit: 'minutes' },
      { id: 'maxHours', label: '最多累计（0 为不限）', kind: 'number', defaultValue: 0, min: 0, max: 240, step: 0.5, unit: '小时' },
      commonBroadcast,
    ],
    giftSlots: [{ id: 'overtime', label: '加时礼物', description: '这些礼物会按价格增加加班时间。', minimum: 1, multiple: true }],
    build: (input, ids) => {
      const name = valueText(input, 'name') || '加班时间';
      const perYuan = Math.max(1, valueNumber(input, 'minutesPerYuan'));
      const maximum = Math.max(0, valueNumber(input, 'maxHours') * 3600);
      const raw = `${name}+price/1000*${formulaNumber(perYuan)}`;
      const formula = maximum > 0 ? `MIN(${raw},${formulaNumber(maximum)})` : raw;
      const rules = rulesForSlot(input, 'overtime', name, '按价格加时', formula, ids);
      return {
        attributes: [{
          ...attributeBase(name, 0, 'hhmmss', '', valueText(input, 'broadcastMessage')),
          display: { variant: 'timer', themeId: themed(input, 'glass'), title: name, min: 0, ...(maximum > 0 ? { max: maximum } : {}) },
          createdFromTemplateId: 'overtime', createdFromTemplateVersion: 1,
        }],
        rules,
        timerRules: [timer(ids, name, '每分钟自动减少', 60, `MAX(${name}-60,0)`, `${name}>0`)],
        usedGifts: uniqueGifts(input),
        summary: [`${rules.length} 个礼物按价格增加时间`, '每分钟自动减少 1 分钟', maximum > 0 ? `最多累计 ${maximum / 3600} 小时` : '累计时间不封顶'],
      };
    },
  },
  {
    id: 'countdown', version: 1, category: 'timer', title: '倒计时续命', difficulty: '简单', preview: 'timer',
    summary: '从一段已有时间开始倒数，礼物为挑战续时。',
    audiencePlay: '倒计时归零前，观众可以用礼物续命。',
    recommendedThemeId: 'minimal',
    parameters: [
      { id: 'name', label: '属性名称', kind: 'text', defaultValue: '剩余时间' },
      { id: 'initialSeconds', label: '初始时长', kind: 'duration', defaultValue: 1800, min: 10, max: 86400, durationUnit: 'minutes' },
      { id: 'growthMode', label: '续时方式', kind: 'select', defaultValue: 'fixed', options: [{ value: 'fixed', label: '每个礼物固定续时' }, { value: 'price', label: '按礼物价格续时' }] },
      { id: 'addSeconds', label: '每个 / 每元续时', kind: 'duration', defaultValue: 60, min: 1, max: 3600, durationUnit: 'minutes' },
      { id: 'maxSeconds', label: '最长时长', kind: 'duration', defaultValue: 7200, min: 60, max: 86400, durationUnit: 'minutes' },
      commonBroadcast,
    ],
    giftSlots: [{ id: 'extend', label: '续时礼物', description: '收到后为当前倒计时补充时间。', minimum: 1, multiple: true }],
    build: (input, ids) => {
      const name = valueText(input, 'name') || '剩余时间';
      const initial = Math.max(0, valueNumber(input, 'initialSeconds'));
      const add = Math.max(1, valueNumber(input, 'addSeconds'));
      const maximum = Math.max(initial, valueNumber(input, 'maxSeconds'));
      const increment = valueText(input, 'growthMode') === 'price' ? `price/1000*${formulaNumber(add)}` : formulaNumber(add);
      const formula = `MIN(${name}+${increment},${formulaNumber(maximum)})`;
      const rules = rulesForSlot(input, 'extend', name, '续时', formula, ids);
      return {
        attributes: [{
          ...attributeBase(name, initial, 'hhmmss', '', valueText(input, 'broadcastMessage')),
          display: { variant: 'timer', themeId: themed(input, 'minimal'), title: name, min: 0, max: maximum, lowThreshold: 60 },
          createdFromTemplateId: 'countdown', createdFromTemplateVersion: 1,
        }],
        rules,
        timerRules: [timer(ids, name, '每秒倒计时', 1, `MAX(${name}-1,0)`, `${name}>0`)],
        usedGifts: uniqueGifts(input),
        summary: [`从 ${Math.round(initial / 60)} 分钟开始倒数`, `${rules.length} 个礼物负责续时`, `最长 ${Math.round(maximum / 60)} 分钟`],
      };
    },
  },
  {
    id: 'counter', version: 1, category: 'challenge', title: '礼物计数器', difficulty: '简单', preview: 'counter',
    summary: '指定礼物让挑战、复活或点单次数增加。',
    audiencePlay: '观众每投喂一个礼物，就增加一次主播要完成的内容。',
    recommendedThemeId: 'pixel',
    parameters: [
      { id: 'name', label: '计数名称', kind: 'text', defaultValue: '挑战次数' },
      { id: 'suffix', label: '单位', kind: 'select', defaultValue: '次', options: [{ value: '次', label: '次' }, { value: '局', label: '局' }, { value: '个', label: '个' }, { value: '组', label: '组' }, { value: '分', label: '分' }] },
      { id: 'amount', label: '每个礼物增加', kind: 'number', defaultValue: 1, min: 0.01, max: 100000, step: 1 },
      { id: 'cap', label: '最多累计（0 为不限）', kind: 'number', defaultValue: 0, min: 0, max: 1000000, step: 1 },
      commonBroadcast,
    ],
    giftSlots: [{ id: 'count', label: '计数礼物', description: '每收到一个，就增加设定的数量。', minimum: 1, multiple: true }],
    build: (input, ids) => {
      const name = valueText(input, 'name') || '挑战次数';
      const amount = valueNumber(input, 'amount');
      const cap = Math.max(0, valueNumber(input, 'cap'));
      const raw = `${name}+${formulaNumber(amount)}`;
      const formula = cap > 0 ? `MIN(${raw},${formulaNumber(cap)})` : raw;
      const rules = rulesForSlot(input, 'count', name, '增加计数', formula, ids);
      return {
        attributes: [{
          ...attributeBase(name, 0, 'suffix', valueText(input, 'suffix') || '次', valueText(input, 'broadcastMessage')),
          display: { variant: 'number', themeId: themed(input, 'pixel'), title: name, min: 0, ...(cap > 0 ? { max: cap } : {}) },
          createdFromTemplateId: 'counter', createdFromTemplateVersion: 1,
        }],
        rules, timerRules: [], usedGifts: uniqueGifts(input),
        summary: [`${rules.length} 个礼物每个增加 ${formulaNumber(amount)}`, cap > 0 ? `最多累计 ${cap}` : '计数不封顶'],
      };
    },
  },
  {
    id: 'goal', version: 1, category: 'goal', title: '目标进度', difficulty: '简单', preview: 'progress',
    summary: '礼物按价格推进一个有上限的共同目标。',
    audiencePlay: '全房观众一起投喂，让进度条达到目标。',
    recommendedThemeId: 'kawaii',
    parameters: [
      { id: 'name', label: '目标名称', kind: 'text', defaultValue: '目标进度' },
      { id: 'target', label: '目标值', kind: 'number', defaultValue: 100, min: 1, max: 100000000, step: 1 },
      { id: 'perYuan', label: '每 1 元推进', kind: 'number', defaultValue: 1, min: 0.01, max: 100000, step: 1 },
      commonBroadcast,
    ],
    giftSlots: [{ id: 'progress', label: '推进礼物', description: '按礼物价格折算并推进目标。', minimum: 1, multiple: true }],
    build: (input, ids) => {
      const name = valueText(input, 'name') || '目标进度';
      const target = Math.max(1, valueNumber(input, 'target'));
      const perYuan = Math.max(0, valueNumber(input, 'perYuan'));
      const formula = `MIN(${name}+price/1000*${formulaNumber(perYuan)},${formulaNumber(target)})`;
      const rules = rulesForSlot(input, 'progress', name, '推进目标', formula, ids);
      return {
        attributes: [{
          ...attributeBase(name, 0, 'number', '', valueText(input, 'broadcastMessage')),
          display: { variant: 'progress', themeId: themed(input, 'kawaii'), title: name, min: 0, max: target },
          createdFromTemplateId: 'goal', createdFromTemplateVersion: 1,
        }],
        rules, timerRules: [], usedGifts: uniqueGifts(input),
        summary: [`目标值 ${formulaNumber(target)}`, `${rules.length} 个礼物按价格推进`, '达到目标后停止增长'],
      };
    },
  },
  {
    id: 'boss', version: 1, category: 'challenge', title: 'Boss 挑战', difficulty: '进阶', preview: 'health',
    summary: '观众共同攻击 Boss，也可以选择治疗礼物。',
    audiencePlay: '不同礼物造成伤害或治疗，全房一起把 Boss 血量打到 0。',
    recommendedThemeId: 'rpg',
    parameters: [
      { id: 'name', label: '属性名称', kind: 'text', defaultValue: 'Boss血量' },
      { id: 'bossName', label: 'Boss 名称', kind: 'text', defaultValue: '最终 Boss' },
      { id: 'maxHealth', label: '最大生命', kind: 'number', defaultValue: 1000, min: 1, max: 100000000, step: 100 },
      { id: 'attack', label: '普通攻击伤害', kind: 'number', defaultValue: 50, min: 0, max: 100000000, step: 10 },
      { id: 'heavy', label: '重击伤害', kind: 'number', defaultValue: 200, min: 0, max: 100000000, step: 10 },
      { id: 'heal', label: '治疗量', kind: 'number', defaultValue: 100, min: 0, max: 100000000, step: 10 },
      { id: 'regenEnabled', label: 'Boss 自动回血', kind: 'toggle', defaultValue: false },
      { id: 'regenInterval', label: '回血间隔', kind: 'duration', defaultValue: 10, min: 1, max: 3600, durationUnit: 'seconds' },
      { id: 'regenAmount', label: '每次回血', kind: 'number', defaultValue: 10, min: 0, max: 100000000, step: 1 },
      commonBroadcast,
    ],
    giftSlots: [
      { id: 'attack', label: '普通攻击', description: '至少选择一个，造成固定伤害。', minimum: 1, multiple: true },
      { id: 'heavy', label: '重击', description: '可选，造成更多伤害。', minimum: 0, multiple: true },
      { id: 'heal', label: '治疗', description: '可选，为 Boss 恢复生命。', minimum: 0, multiple: true },
    ],
    build: (input, ids) => {
      const name = valueText(input, 'name') || 'Boss血量';
      const title = valueText(input, 'bossName') || '最终 Boss';
      const maximum = Math.max(1, valueNumber(input, 'maxHealth'));
      const rules = [
        ...rulesForSlot(input, 'attack', name, '普通攻击', `MAX(${name}-${formulaNumber(valueNumber(input, 'attack'))},0)`, ids),
        ...rulesForSlot(input, 'heavy', name, '重击', `MAX(${name}-${formulaNumber(valueNumber(input, 'heavy'))},0)`, ids),
        ...rulesForSlot(input, 'heal', name, '治疗', `MIN(${name}+${formulaNumber(valueNumber(input, 'heal'))},${formulaNumber(maximum)})`, ids),
      ];
      const timerRules = valueBoolean(input, 'regenEnabled') && valueNumber(input, 'regenAmount') > 0
        ? [timer(ids, name, 'Boss 自动回血', valueNumber(input, 'regenInterval'), `MIN(${name}+${formulaNumber(valueNumber(input, 'regenAmount'))},${formulaNumber(maximum)})`, `${name}>0`)]
        : [];
      return {
        attributes: [{
          ...attributeBase(name, maximum, 'number', '', valueText(input, 'broadcastMessage')),
          display: { variant: 'health', themeId: themed(input, 'rpg'), title, min: 0, max: maximum, lowThreshold: maximum * 0.2 },
          createdFromTemplateId: 'boss', createdFromTemplateVersion: 1,
        }],
        rules, timerRules, usedGifts: uniqueGifts(input),
        summary: [`${title}：${formulaNumber(maximum)} 点生命`, `${rules.length} 条攻击 / 治疗规则`, timerRules.length > 0 ? 'Boss 会自动回血' : 'Boss 不会自动回血'],
      };
    },
  },
  {
    id: 'resource', version: 1, category: 'survival', title: '生存资源条', difficulty: '进阶', preview: 'resource',
    summary: '氧气、能量或饥饿会随时间消耗，礼物负责补给或干扰。',
    audiencePlay: '观众用补给礼物帮助主播维持资源，也可以用干扰礼物增加压力。',
    recommendedThemeId: 'neon',
    parameters: [
      { id: 'name', label: '资源名称', kind: 'text', defaultValue: '氧气' },
      { id: 'maximum', label: '资源上限', kind: 'number', defaultValue: 100, min: 1, max: 100000000, step: 10 },
      { id: 'consumeInterval', label: '自然消耗间隔', kind: 'duration', defaultValue: 5, min: 1, max: 3600, durationUnit: 'seconds' },
      { id: 'consumeAmount', label: '每次自然消耗', kind: 'number', defaultValue: 1, min: 0, max: 100000000, step: 1 },
      { id: 'smallSupply', label: '小补给增加', kind: 'number', defaultValue: 10, min: 0, max: 100000000, step: 1 },
      { id: 'largeSupply', label: '大补给增加', kind: 'number', defaultValue: 30, min: 0, max: 100000000, step: 1 },
      { id: 'interference', label: '干扰扣除', kind: 'number', defaultValue: 10, min: 0, max: 100000000, step: 1 },
      commonBroadcast,
    ],
    giftSlots: [
      { id: 'small', label: '小补给', description: '至少选择一个，恢复少量资源。', minimum: 1, multiple: true },
      { id: 'large', label: '大补给', description: '可选，恢复更多资源。', minimum: 0, multiple: true },
      { id: 'interference', label: '干扰', description: '可选，直接扣除资源。', minimum: 0, multiple: true },
    ],
    build: (input, ids) => {
      const name = valueText(input, 'name') || '氧气';
      const maximum = Math.max(1, valueNumber(input, 'maximum'));
      const rules = [
        ...rulesForSlot(input, 'small', name, '小补给', `MIN(${name}+${formulaNumber(valueNumber(input, 'smallSupply'))},${formulaNumber(maximum)})`, ids),
        ...rulesForSlot(input, 'large', name, '大补给', `MIN(${name}+${formulaNumber(valueNumber(input, 'largeSupply'))},${formulaNumber(maximum)})`, ids),
        ...rulesForSlot(input, 'interference', name, '干扰', `MAX(${name}-${formulaNumber(valueNumber(input, 'interference'))},0)`, ids),
      ];
      return {
        attributes: [{
          ...attributeBase(name, maximum, 'number', '', valueText(input, 'broadcastMessage')),
          display: { variant: 'resource', themeId: themed(input, 'neon'), title: name, min: 0, max: maximum, lowThreshold: maximum * 0.2 },
          createdFromTemplateId: 'resource', createdFromTemplateVersion: 1,
        }],
        rules,
        timerRules: [timer(ids, name, '自然消耗', valueNumber(input, 'consumeInterval'), `MAX(${name}-${formulaNumber(valueNumber(input, 'consumeAmount'))},0)`, `${name}>0`)],
        usedGifts: uniqueGifts(input),
        summary: [`${name}上限 ${formulaNumber(maximum)}`, `每 ${formulaNumber(valueNumber(input, 'consumeInterval'))} 秒自然减少`, `${rules.length} 条补给 / 干扰规则`],
      };
    },
  },
  {
    id: 'tug', version: 1, category: 'versus', title: '双向拉扯条', difficulty: '进阶', preview: 'tug',
    summary: '两组礼物把同一个 0–100 的局势推向不同方向。',
    audiencePlay: '观众选择左右两种礼物，实时改变主播接下来要做的选择。',
    recommendedThemeId: 'neon',
    parameters: [
      { id: 'name', label: '属性名称', kind: 'text', defaultValue: '局势' },
      { id: 'leftLabel', label: '左侧名称', kind: 'text', defaultValue: '继续挑战' },
      { id: 'rightLabel', label: '右侧名称', kind: 'text', defaultValue: '结束挑战' },
      { id: 'initial', label: '初始位置', kind: 'number', defaultValue: 50, min: 0, max: 100, step: 1 },
      { id: 'leftAmount', label: '左侧每个推动', kind: 'number', defaultValue: 10, min: 0, max: 100, step: 1 },
      { id: 'rightAmount', label: '右侧每个推动', kind: 'number', defaultValue: 10, min: 0, max: 100, step: 1 },
      commonBroadcast,
    ],
    giftSlots: [
      { id: 'left', label: '推向左侧', description: '收到后让局势值减少。', minimum: 1, multiple: true },
      { id: 'right', label: '推向右侧', description: '收到后让局势值增加。', minimum: 1, multiple: true },
    ],
    build: (input, ids) => {
      const name = valueText(input, 'name') || '局势';
      const leftLabel = valueText(input, 'leftLabel') || '左侧';
      const rightLabel = valueText(input, 'rightLabel') || '右侧';
      const rules = [
        ...rulesForSlot(input, 'left', name, leftLabel, `MAX(${name}-${formulaNumber(valueNumber(input, 'leftAmount'))},0)`, ids),
        ...rulesForSlot(input, 'right', name, rightLabel, `MIN(${name}+${formulaNumber(valueNumber(input, 'rightAmount'))},100)`, ids),
      ];
      return {
        attributes: [{
          ...attributeBase(name, Math.min(100, Math.max(0, valueNumber(input, 'initial'))), 'number', '', valueText(input, 'broadcastMessage')),
          display: { variant: 'tug', themeId: themed(input, 'neon'), title: name, min: 0, max: 100, leftLabel, rightLabel },
          createdFromTemplateId: 'tug', createdFromTemplateVersion: 1,
        }],
        rules, timerRules: [], usedGifts: uniqueGifts(input),
        summary: [`${leftLabel} ↔ ${rightLabel}`, '初始位置 50%', `${rules.length} 条左右推动规则`],
      };
    },
  },
] as const;

export const GAMEPLAY_TEMPLATES: readonly GameplayTemplateDefinition[] = TEMPLATES;

export function getGameplayTemplate(id: string): GameplayTemplateDefinition | undefined {
  return GAMEPLAY_TEMPLATES.find((template) => template.id === id);
}

export function createDefaultTemplateInput(template: GameplayTemplateDefinition): GameplayTemplateInput {
  return {
    parameters: Object.fromEntries(template.parameters.map((parameter) => [parameter.id, parameter.defaultValue])),
    gifts: Object.fromEntries(template.giftSlots.map((slot) => [slot.id, []])),
    displayThemeId: template.recommendedThemeId,
  };
}

export function validateGameplayTemplateInput(
  template: GameplayTemplateDefinition,
  input: GameplayTemplateInput,
): string[] {
  const errors: string[] = [];
  for (const parameter of template.parameters) {
    const value = input.parameters[parameter.id];
    if (parameter.kind === 'text' && String(value ?? '').trim() === '') errors.push(`请填写${parameter.label}`);
    if (parameter.kind === 'number' || parameter.kind === 'duration') {
      const numeric = Number(value);
      if (!Number.isFinite(numeric)) errors.push(`${parameter.label}必须是数字`);
      else if (parameter.min !== undefined && numeric < parameter.min) errors.push(`${parameter.label}不能小于${parameter.min}`);
      else if (parameter.max !== undefined && numeric > parameter.max) errors.push(`${parameter.label}不能大于${parameter.max}`);
    }
  }
  const usedGiftIds = new Map<number, string>();
  for (const slot of template.giftSlots) {
    const gifts = input.gifts[slot.id] ?? [];
    if (gifts.length < slot.minimum) errors.push(`${slot.label}至少选择 ${slot.minimum} 个礼物`);
    if (!slot.multiple && gifts.length > 1) errors.push(`${slot.label}只能选择 1 个礼物`);
    for (const gift of gifts) {
      const previousSlot = usedGiftIds.get(gift.id);
      if (previousSlot && previousSlot !== slot.id) errors.push(`${gift.name}不能同时分配给多个角色`);
      usedGiftIds.set(gift.id, slot.id);
    }
  }
  return Array.from(new Set(errors));
}

function defaultIdFactory(): TemplateIdFactory {
  let sequence = 0;
  return {
    next: (kind) => {
      sequence += 1;
      const uuid = globalThis.crypto?.randomUUID?.();
      return uuid ? `${kind}-${uuid}` : `${kind}-template-${Date.now()}-${sequence}`;
    },
  };
}

export function buildGameplayTemplate(
  template: GameplayTemplateDefinition,
  input: GameplayTemplateInput,
  ids: TemplateIdFactory = defaultIdFactory(),
): GameplayTemplateBuildResult {
  const errors = validateGameplayTemplateInput(template, input);
  if (errors.length > 0) throw new Error(errors[0]);
  return template.build(input, ids);
}

