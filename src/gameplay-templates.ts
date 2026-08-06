import type {
  ActivitySession,
  Attribute,
  DisplayScene,
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
  displayScenes: DisplayScene[];
  activities: ActivitySession[];
  usedGifts: GiftInfo[];
  summary: string[];
}

type GameplayTemplateBuildDraft = Omit<GameplayTemplateBuildResult, 'displayScenes' | 'activities'> & {
  displayScenes?: DisplayScene[];
  activities?: ActivitySession[];
};

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
  build: (input: GameplayTemplateInput, ids: TemplateIdFactory) => GameplayTemplateBuildDraft;
}

export interface TemplateIdFactory {
  next: (kind: 'rule' | 'timer' | 'scene' | 'activity' | 'milestone') => string;
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
        timerRules: [timer(ids, name, '每秒自动减少', 1, `MAX(${name}-1,0)`, `${name}>0`)],
        usedGifts: uniqueGifts(input),
        summary: [`${rules.length} 个礼物按价格增加时间`, '每秒自动减少 1 秒', maximum > 0 ? `最多累计 ${maximum / 3600} 小时` : '累计时间不封顶'],
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
  {
    id: 'team-duel', version: 1, category: 'versus', title: '阵营对战', difficulty: '进阶', preview: 'tug',
    summary: '两支队伍分别累计积分，任意一方先达到目标就自动结算。',
    audiencePlay: '观众选择阵营礼物，为支持的队伍加分并争夺胜利。',
    recommendedThemeId: 'neon',
    parameters: [
      { id: 'activityName', label: '活动名称', kind: 'text', defaultValue: '红蓝阵营对战' },
      { id: 'leftName', label: '左队名称', kind: 'text', defaultValue: '红队' },
      { id: 'rightName', label: '右队名称', kind: 'text', defaultValue: '蓝队' },
      { id: 'target', label: '获胜目标', kind: 'number', defaultValue: 100, min: 1, max: 100000000, step: 1, unit: '分' },
      { id: 'points', label: '每个礼物增加', kind: 'number', defaultValue: 1, min: 0.01, max: 1000000, step: 1, unit: '分' },
      commonBroadcast,
    ],
    giftSlots: [
      { id: 'left', label: '左队礼物', description: '收到后只为左队增加积分。', minimum: 1, multiple: true },
      { id: 'right', label: '右队礼物', description: '收到后只为右队增加积分。', minimum: 1, multiple: true },
    ],
    build: (input, ids) => {
      const activityName = valueText(input, 'activityName') || '阵营对战';
      const leftName = valueText(input, 'leftName') || '红队';
      const rightName = valueText(input, 'rightName') || '蓝队';
      if (leftName === rightName) throw new Error('两支队伍的名称不能相同');
      const target = Math.max(1, valueNumber(input, 'target'));
      const points = valueNumber(input, 'points');
      const sceneId = ids.next('scene');
      const activityId = ids.next('activity');
      const attributes: Attribute[] = [leftName, rightName].map((name) => ({
        ...attributeBase(name, 0, 'suffix', '分', valueText(input, 'broadcastMessage')),
        display: { variant: 'progress', themeId: themed(input, 'neon'), title: name, min: 0, max: target },
        createdFromTemplateId: 'team-duel', createdFromTemplateVersion: 1,
      }));
      const rules = [
        ...rulesForSlot(input, 'left', leftName, '为左队加分', `MIN(${leftName}+${formulaNumber(points)},${formulaNumber(target)})`, ids),
        ...rulesForSlot(input, 'right', rightName, '为右队加分', `MIN(${rightName}+${formulaNumber(points)},${formulaNumber(target)})`, ids),
      ];
      return {
        attributes,
        rules,
        timerRules: [],
        displayScenes: [{ id: sceneId, name: `${activityName}面板`, attributeNames: [leftName, rightName], layout: 'versus', themeId: themed(input, 'neon') }],
        activities: [{
          id: activityId, name: activityName, attributeNames: [leftName, rightName], sceneId,
          status: 'not_started', resultMode: 'highest', gateRules: true,
          initialValues: { [leftName]: 0, [rightName]: 0 },
          milestones: [leftName, rightName].map((attributeName) => ({
            id: ids.next('milestone'), name: `${attributeName}达到获胜目标`, attributeName,
            comparison: 'gte', threshold: target, action: 'settle', message: `${attributeName}率先达到目标！`,
          })),
        }],
        usedGifts: uniqueGifts(input),
        summary: [`${leftName} 对战 ${rightName}`, `先达到 ${formulaNumber(target)} 分自动结算`, '创建后需要在活动会话中点击开始'],
      };
    },
  },
  {
    id: 'gift-vote', version: 1, category: 'versus', title: '礼物二选一投票', difficulty: '简单', preview: 'counter',
    summary: '两个选项分别计票，主播随时锁票并结算最高票结果。',
    audiencePlay: '观众用不同礼物投给两个选项，实时看到票数变化。',
    recommendedThemeId: 'kawaii',
    parameters: [
      { id: 'activityName', label: '投票名称', kind: 'text', defaultValue: '下一项挑战投票' },
      { id: 'leftName', label: '选项 A', kind: 'text', defaultValue: '继续挑战' },
      { id: 'rightName', label: '选项 B', kind: 'text', defaultValue: '休息一下' },
      { id: 'votes', label: '每个礼物计票', kind: 'number', defaultValue: 1, min: 0.01, max: 1000000, step: 1, unit: '票' },
      commonBroadcast,
    ],
    giftSlots: [
      { id: 'left', label: '选项 A 礼物', description: '每个礼物计入选项 A。', minimum: 1, multiple: true },
      { id: 'right', label: '选项 B 礼物', description: '每个礼物计入选项 B。', minimum: 1, multiple: true },
    ],
    build: (input, ids) => {
      const activityName = valueText(input, 'activityName') || '礼物投票';
      const leftName = valueText(input, 'leftName') || '选项 A';
      const rightName = valueText(input, 'rightName') || '选项 B';
      if (leftName === rightName) throw new Error('两个投票选项不能同名');
      const votes = valueNumber(input, 'votes');
      const sceneId = ids.next('scene');
      const attributes: Attribute[] = [leftName, rightName].map((name) => ({
        ...attributeBase(name, 0, 'suffix', '票', valueText(input, 'broadcastMessage')),
        display: { variant: 'number', themeId: themed(input, 'kawaii'), title: name, min: 0 },
        createdFromTemplateId: 'gift-vote', createdFromTemplateVersion: 1,
      }));
      const rules = [
        ...rulesForSlot(input, 'left', leftName, '投给选项 A', `${leftName}+${formulaNumber(votes)}`, ids),
        ...rulesForSlot(input, 'right', rightName, '投给选项 B', `${rightName}+${formulaNumber(votes)}`, ids),
      ];
      return {
        attributes,
        rules,
        timerRules: [],
        displayScenes: [{ id: sceneId, name: `${activityName}面板`, attributeNames: [leftName, rightName], layout: 'versus', themeId: themed(input, 'kawaii') }],
        activities: [{
          id: ids.next('activity'), name: activityName, attributeNames: [leftName, rightName], sceneId,
          status: 'not_started', resultMode: 'highest', gateRules: true,
          initialValues: { [leftName]: 0, [rightName]: 0 }, milestones: [],
        }],
        usedGifts: uniqueGifts(input),
        summary: [`${leftName} 对 ${rightName}`, '主播锁票后再确认结算', '创建后需要在活动会话中点击开始'],
      };
    },
  },
  {
    id: 'combo', version: 1, category: 'challenge', title: '限时连击挑战', difficulty: '进阶', preview: 'counter',
    summary: '每次命中礼物都会增加连击并重置倒计时，断档后自动结算。',
    audiencePlay: '观众接力投喂指定礼物，尽量把连击延续得更久。',
    recommendedThemeId: 'pixel',
    parameters: [
      { id: 'name', label: '连击名称', kind: 'text', defaultValue: '全房连击' },
      { id: 'timeout', label: '断档时间', kind: 'duration', defaultValue: 15, min: 1, max: 3600, durationUnit: 'seconds' },
      { id: 'goal', label: '提前达成目标（0 为不限）', kind: 'number', defaultValue: 50, min: 0, max: 100000000, step: 1, unit: '连击' },
      commonBroadcast,
    ],
    giftSlots: [{ id: 'combo', label: '连击礼物', description: '任意一个都会增加 1 连击并重新计时。', minimum: 1, multiple: true }],
    build: (input, ids) => {
      const name = valueText(input, 'name') || '全房连击';
      const timeout = Math.max(1, Math.round(valueNumber(input, 'timeout')));
      const goal = Math.max(0, valueNumber(input, 'goal'));
      const sceneId = ids.next('scene');
      const rules = rulesForSlot(input, 'combo', name, '延续连击', `${name}+1`, ids);
      return {
        attributes: [{
          ...attributeBase(name, 0, 'suffix', '连击', valueText(input, 'broadcastMessage')),
          display: { variant: 'number', themeId: themed(input, 'pixel'), title: name, min: 0, ...(goal > 0 ? { max: goal } : {}) },
          createdFromTemplateId: 'combo', createdFromTemplateVersion: 1,
        }],
        rules,
        timerRules: [],
        displayScenes: [{ id: sceneId, name: `${name}面板`, attributeNames: [name], layout: 'focus', themeId: themed(input, 'pixel') }],
        activities: [{
          id: ids.next('activity'), name: `${name}挑战`, attributeNames: [name], sceneId,
          status: 'not_started', resultMode: 'none', gateRules: true, initialValues: { [name]: 0 },
          milestones: goal > 0 ? [{
            id: ids.next('milestone'), name: '连击目标达成', attributeName: name,
            comparison: 'gte', threshold: goal, action: 'settle', message: `${name}达到 ${formulaNumber(goal)}！`,
          }] : [],
          giftTimeout: { seconds: timeout, action: 'settle' },
        }],
        usedGifts: uniqueGifts(input),
        summary: [`每份礼物增加 1 连击`, `${timeout} 秒没有新礼物就自动结算`, goal > 0 ? `达到 ${formulaNumber(goal)} 提前结算` : '不设置提前达成目标'],
      };
    },
  },
  {
    id: 'milestone', version: 1, category: 'goal', title: '目标冲刺赛', difficulty: '简单', preview: 'progress',
    summary: '全房推进同一个目标，达到指定数值时自动提示并结算。',
    audiencePlay: '观众共同投喂，把进度条推到目标线。',
    recommendedThemeId: 'glass',
    parameters: [
      { id: 'name', label: '目标名称', kind: 'text', defaultValue: '应援目标' },
      { id: 'target', label: '目标值', kind: 'number', defaultValue: 100, min: 1, max: 100000000, step: 1 },
      { id: 'amount', label: '每个礼物推进', kind: 'number', defaultValue: 1, min: 0.01, max: 1000000, step: 1 },
      { id: 'message', label: '达成提示', kind: 'text', defaultValue: '全房目标达成！' },
      commonBroadcast,
    ],
    giftSlots: [{ id: 'progress', label: '推进礼物', description: '每个礼物按固定数量推进。', minimum: 1, multiple: true }],
    build: (input, ids) => {
      const name = valueText(input, 'name') || '应援目标';
      const target = Math.max(1, valueNumber(input, 'target'));
      const amount = valueNumber(input, 'amount');
      const sceneId = ids.next('scene');
      const rules = rulesForSlot(input, 'progress', name, '推进目标', `MIN(${name}+${formulaNumber(amount)},${formulaNumber(target)})`, ids);
      return {
        attributes: [{
          ...attributeBase(name, 0, 'number', '', valueText(input, 'broadcastMessage')),
          display: { variant: 'progress', themeId: themed(input, 'glass'), title: name, min: 0, max: target },
          createdFromTemplateId: 'milestone', createdFromTemplateVersion: 1,
        }],
        rules,
        timerRules: [],
        displayScenes: [{ id: sceneId, name: `${name}面板`, attributeNames: [name], layout: 'focus', themeId: themed(input, 'glass') }],
        activities: [{
          id: ids.next('activity'), name: `${name}冲刺`, attributeNames: [name], sceneId,
          status: 'not_started', resultMode: 'none', gateRules: true, initialValues: { [name]: 0 },
          milestones: [{
            id: ids.next('milestone'), name: '目标达成', attributeName: name,
            comparison: 'gte', threshold: target, action: 'settle', message: valueText(input, 'message') || '目标达成！',
          }],
        }],
        usedGifts: uniqueGifts(input),
        summary: [`目标值 ${formulaNumber(target)}`, `每份礼物推进 ${formulaNumber(amount)}`, '达到目标后自动结算'],
      };
    },
  },
  {
    id: 'random-event', version: 1, category: 'challenge', title: '随机事件机', difficulty: '简单', preview: 'counter',
    summary: '礼物触发一次随机抽取，OBS 把数字结果显示成自定义事件。',
    audiencePlay: '观众用礼物抽取主播接下来要执行的随机事件。',
    recommendedThemeId: 'rpg',
    parameters: [
      { id: 'name', label: '属性名称', kind: 'text', defaultValue: '随机事件' },
      { id: 'event1', label: '事件 1', kind: 'text', defaultValue: '主播喝水' },
      { id: 'event2', label: '事件 2', kind: 'text', defaultValue: '做 10 个深蹲' },
      { id: 'event3', label: '事件 3', kind: 'text', defaultValue: '唱一句歌' },
      { id: 'event4', label: '事件 4', kind: 'text', defaultValue: '安全通过' },
      commonBroadcast,
    ],
    giftSlots: [{ id: 'draw', label: '抽取礼物', description: '每收到一个就重新抽取一次。', minimum: 1, multiple: true }],
    build: (input, ids) => {
      const name = valueText(input, 'name') || '随机事件';
      const events = [1, 2, 3, 4].map((index) => valueText(input, `event${index}`) || `事件 ${index}`);
      const colors = ['#ff6b8f', '#ffd166', '#63f3ff', '#86edac'];
      const rules = rulesForSlot(input, 'draw', name, '随机抽取', 'RANDBETWEEN(1,4)', ids);
      return {
        attributes: [{
          ...attributeBase(name, 0, 'number', '', valueText(input, 'broadcastMessage')),
          display: {
            variant: 'enum', themeId: themed(input, 'rpg'), title: name,
            valueMappings: [
              { value: 0, label: '等待抽取', color: '#d8dbe6' },
              ...events.map((label, index) => ({ value: index + 1, label, color: colors[index] })),
            ],
          },
          createdFromTemplateId: 'random-event', createdFromTemplateVersion: 1,
        }],
        rules,
        timerRules: [],
        usedGifts: uniqueGifts(input),
        summary: [`共 ${events.length} 个随机事件`, `${rules.length} 个礼物可以触发抽取`, '可在输出页继续修改文字、颜色和图片'],
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
  const result = template.build(input, ids);
  return {
    displayScenes: [],
    activities: [],
    ...result,
  };
}
