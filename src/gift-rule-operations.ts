import type { OvertimeGiftOperation } from './types';

export type QuickGiftOperation =
  | OvertimeGiftOperation
  | 'price'
  | 'priceSubtract'
  | 'set'
  | 'randomRange'
  | 'advanced';

type AmountQuickGiftOperation = Exclude<QuickGiftOperation, 'randomRange'>;

export type QuickGiftRuleDraft =
  | { operation: 'randomRange'; rangeMin: number; rangeMax: number; maximum?: number }
  | { operation: AmountQuickGiftOperation; amount: number; maximum?: number };

type QuickGiftRuleDraftWithoutMaximum =
  | { operation: 'randomRange'; rangeMin: number; rangeMax: number }
  | { operation: AmountQuickGiftOperation; amount: number };

export const QUICK_GIFT_OPERATION_GROUPS: Array<{
  label: string;
  operations: QuickGiftOperation[];
}> = [
  { label: '常用变化', operations: ['add', 'subtract', 'double', 'halve', 'set', 'reset'] },
  { label: '按礼物价格', operations: ['price', 'priceSubtract'] },
  { label: '更多玩法', operations: ['randomRange'] },
  { label: '高级', operations: ['advanced'] },
];

function splitTopLevelArguments(value: string): [string, string] | null {
  let depth = 0;
  for (let index = 0; index < value.length; index += 1) {
    const char = value[index];
    if (char === '(') depth += 1;
    else if (char === ')') depth -= 1;
    else if (char === ',' && depth === 0) return [value.slice(0, index), value.slice(index + 1)];
  }
  return null;
}

function unwrapMaximum(formula: string): { formula: string; maximum?: number } {
  if (!formula.startsWith('MIN(') || !formula.endsWith(')')) return { formula };
  const args = splitTopLevelArguments(formula.slice(4, -1));
  if (!args) return { formula };
  const maximum = Number(args[1]);
  return Number.isFinite(maximum) ? { formula: args[0], maximum } : { formula };
}

function numberAfterPrefix(formula: string, prefix: string, suffix = ''): number | null {
  if (!formula.startsWith(prefix) || !formula.endsWith(suffix)) return null;
  const raw = formula.slice(prefix.length, suffix ? -suffix.length : undefined);
  const value = Number(raw);
  return raw.length > 0 && Number.isFinite(value) ? value : null;
}

function integerPair(value: string): [number, number] | null {
  const args = splitTopLevelArguments(value);
  if (!args) return null;
  const [rawMin, rawMax] = args;
  const rangeMin = Number(rawMin);
  const rangeMax = Number(rawMax);
  return rawMin.length > 0
    && rawMax.length > 0
    && Number.isInteger(rangeMin)
    && Number.isInteger(rangeMax)
    && rangeMin <= rangeMax
    ? [rangeMin, rangeMax]
    : null;
}

function randomRangeAfterPrefix(formula: string, prefix: string, suffix = ''): [number, number] | null {
  if (!formula.startsWith(prefix) || !formula.endsWith(suffix)) return null;
  const raw = formula.slice(prefix.length, suffix ? -suffix.length : undefined);
  return integerPair(raw);
}

function canonicalRandomRange(formula: string, attributeName: string): [number, number] | null {
  if (!formula.startsWith('MAX(') || !formula.endsWith(')')) return null;
  const args = splitTopLevelArguments(formula.slice(4, -1));
  if (!args || args[1] !== '0') return null;
  return randomRangeAfterPrefix(args[0], `${attributeName}+RANDBETWEEN(`, ')');
}

export function validateQuickGiftRuleDraft(draft: QuickGiftRuleDraft): string | null {
  if (draft.operation !== 'randomRange') return null;
  if (!Number.isInteger(draft.rangeMin) || !Number.isInteger(draft.rangeMax)) {
    return '随机范围必须使用整数';
  }
  if (draft.rangeMin > draft.rangeMax) return '随机范围的最小变化不能大于最大变化';
  if (draft.maximum !== undefined && draft.maximum < 0) return '随机范围的上限不能小于 0';
  return null;
}

export function detectQuickGiftRule(formula: string, attributeName: string): QuickGiftRuleDraft {
  const unwrapped = unwrapMaximum(formula.replace(/\s+/g, ''));
  const compact = unwrapped.formula;
  const withMaximum = (draft: QuickGiftRuleDraftWithoutMaximum): QuickGiftRuleDraft => ({
    ...draft,
    ...(unwrapped.maximum === undefined ? {} : { maximum: unwrapped.maximum }),
  });
  if (compact === '0') return withMaximum({ operation: 'reset', amount: 0 });
  if (compact === `${attributeName}*2`) return withMaximum({ operation: 'double', amount: 2 });
  if (compact === `MAX(FLOOR(${attributeName}/2),0)`) return withMaximum({ operation: 'halve', amount: 2 });

  const randomRange = canonicalRandomRange(compact, attributeName);
  if (randomRange) return withMaximum({ operation: 'randomRange', rangeMin: randomRange[0], rangeMax: randomRange[1] });

  const legacyRandomSubtract = numberAfterPrefix(compact, `MAX(${attributeName}-RANDBETWEEN(1,`, '),0)');
  if (legacyRandomSubtract !== null && Number.isInteger(legacyRandomSubtract) && legacyRandomSubtract >= 1) {
    return withMaximum({ operation: 'randomRange', rangeMin: -legacyRandomSubtract, rangeMax: -1 });
  }

  const legacyRandomAdd = numberAfterPrefix(compact, `${attributeName}+RANDBETWEEN(1,`, ')');
  if (legacyRandomAdd !== null && Number.isInteger(legacyRandomAdd) && legacyRandomAdd >= 1) {
    return withMaximum({ operation: 'randomRange', rangeMin: 1, rangeMax: legacyRandomAdd });
  }

  const priceAdd = numberAfterPrefix(compact, `${attributeName}+price/1000*`);
  if (priceAdd !== null) return withMaximum({ operation: 'price', amount: priceAdd });

  const priceSubtract = numberAfterPrefix(compact, `MAX(${attributeName}-price/1000*`, ',0)');
  if (priceSubtract !== null) return withMaximum({ operation: 'priceSubtract', amount: priceSubtract });

  const subtract = numberAfterPrefix(compact, `MAX(${attributeName}-`, ',0)');
  if (subtract !== null) return withMaximum({ operation: 'subtract', amount: subtract });

  const add = numberAfterPrefix(compact, `${attributeName}+`);
  if (add !== null) return withMaximum({ operation: 'add', amount: add });

  const fixed = Number(compact);
  if (compact.length > 0 && Number.isFinite(fixed)) return withMaximum({ operation: 'set', amount: fixed });
  return withMaximum({ operation: 'advanced', amount: 60 });
}

export function buildQuickGiftFormula(
  draft: QuickGiftRuleDraft,
  attributeName: string,
): string | null {
  if (validateQuickGiftRuleDraft(draft) !== null) return null;
  const safeAmount = 'amount' in draft && Number.isFinite(draft.amount) ? draft.amount : 0;
  let formula: string | null;
  switch (draft.operation) {
    case 'add': formula = `${attributeName}+${safeAmount}`; break;
    case 'subtract': formula = `MAX(${attributeName}-${safeAmount},0)`; break;
    case 'double': formula = `${attributeName}*2`; break;
    case 'halve': formula = `MAX(FLOOR(${attributeName}/2),0)`; break;
    case 'price': formula = `${attributeName}+price/1000*${safeAmount}`; break;
    case 'priceSubtract': formula = `MAX(${attributeName}-price/1000*${safeAmount},0)`; break;
    case 'set': formula = String(safeAmount); break;
    case 'reset': formula = '0'; break;
    case 'randomRange': formula = `MAX(${attributeName}+RANDBETWEEN(${draft.rangeMin},${draft.rangeMax}),0)`; break;
    case 'advanced': formula = null; break;
  }
  return formula !== null
    && quickGiftOperationSupportsMaximum(draft.operation)
    && draft.maximum !== undefined
    && Number.isFinite(draft.maximum)
    ? `MIN(${formula},${draft.maximum})`
    : formula;
}

export function buildOvertimeGiftFormula(
  operation: OvertimeGiftOperation,
  attributeName: string,
  seconds = 60,
  maximum?: number,
): string {
  return buildQuickGiftFormula({ operation, amount: seconds, maximum }, attributeName) ?? '0';
}

export function quickGiftOperationLabel(operation: QuickGiftOperation, attributeName: string): string {
  const labels: Record<QuickGiftOperation, string> = {
    add: `让“${attributeName}”增加`,
    subtract: `让“${attributeName}”减少（最低为 0）`,
    double: `让“${attributeName}”翻倍`,
    halve: `让“${attributeName}”减半（向下取整）`,
    price: `每 1 元让“${attributeName}”增加`,
    priceSubtract: `每 1 元让“${attributeName}”减少（最低为 0）`,
    set: `把“${attributeName}”设为`,
    reset: `把“${attributeName}”清零`,
    randomRange: `让“${attributeName}”随机变化（最低为 0）`,
    advanced: '使用下方高级规则',
  };
  return labels[operation];
}

export function quickGiftOperationUsesAmount(operation: QuickGiftOperation): boolean {
  return !['reset', 'double', 'halve', 'randomRange', 'advanced'].includes(operation);
}

export function quickGiftOperationUsesRange(operation: QuickGiftOperation): boolean {
  return operation === 'randomRange';
}

export function quickGiftOperationUnit(operation: QuickGiftOperation, formattedAsTime: boolean): string {
  if (operation === 'advanced') return '在下方直接输入表达式';
  if (!quickGiftOperationUsesAmount(operation)) return '无需填写数值';
  return formattedAsTime ? '秒' : '单位';
}

export function quickGiftOperationSupportsMaximum(operation: QuickGiftOperation): boolean {
  return operation === 'add' || operation === 'double' || operation === 'price' || operation === 'randomRange';
}
