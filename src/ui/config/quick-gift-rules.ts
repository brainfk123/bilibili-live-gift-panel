export type QuickGiftOperation =
  | 'add'
  | 'subtract'
  | 'price'
  | 'priceSubtract'
  | 'set'
  | 'reset'
  | 'random'
  | 'randomSubtract'
  | 'advanced';

export interface QuickGiftRuleDraft {
  operation: QuickGiftOperation;
  amount: number;
  maximum?: number;
}

export const QUICK_GIFT_OPERATION_GROUPS: Array<{
  label: string;
  operations: QuickGiftOperation[];
}> = [
  { label: '常用变化', operations: ['add', 'subtract', 'set', 'reset'] },
  { label: '按礼物价格', operations: ['price', 'priceSubtract'] },
  { label: '更多玩法', operations: ['random', 'randomSubtract'] },
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

export function detectQuickGiftRule(formula: string, attributeName: string): QuickGiftRuleDraft {
  const unwrapped = unwrapMaximum(formula.replace(/\s+/g, ''));
  const compact = unwrapped.formula;
  const withMaximum = (draft: Omit<QuickGiftRuleDraft, 'maximum'>): QuickGiftRuleDraft => ({
    ...draft,
    ...(unwrapped.maximum === undefined ? {} : { maximum: unwrapped.maximum }),
  });
  if (compact === '0') return withMaximum({ operation: 'reset', amount: 0 });

  const priceAdd = numberAfterPrefix(compact, `${attributeName}+price/1000*`);
  if (priceAdd !== null) return withMaximum({ operation: 'price', amount: priceAdd });

  const priceSubtract = numberAfterPrefix(compact, `MAX(${attributeName}-price/1000*`, ',0)');
  if (priceSubtract !== null) return withMaximum({ operation: 'priceSubtract', amount: priceSubtract });

  const subtract = numberAfterPrefix(compact, `MAX(${attributeName}-`, ',0)');
  if (subtract !== null) return withMaximum({ operation: 'subtract', amount: subtract });

  const randomSubtract = numberAfterPrefix(compact, `MAX(${attributeName}-RANDBETWEEN(1,`, '),0)');
  if (randomSubtract !== null) return withMaximum({ operation: 'randomSubtract', amount: randomSubtract });

  const random = numberAfterPrefix(compact, `${attributeName}+RANDBETWEEN(1,`, ')');
  if (random !== null) return withMaximum({ operation: 'random', amount: random });

  const add = numberAfterPrefix(compact, `${attributeName}+`);
  if (add !== null) return withMaximum({ operation: 'add', amount: add });

  const fixed = Number(compact);
  if (compact.length > 0 && Number.isFinite(fixed)) return withMaximum({ operation: 'set', amount: fixed });
  return withMaximum({ operation: 'advanced', amount: 60 });
}

export function buildQuickGiftFormula(
  operation: QuickGiftOperation,
  attributeName: string,
  amount: number,
  maximum?: number,
): string | null {
  const safeAmount = Number.isFinite(amount) ? amount : 0;
  let formula: string | null;
  switch (operation) {
    case 'add': formula = `${attributeName}+${safeAmount}`; break;
    case 'subtract': formula = `MAX(${attributeName}-${safeAmount},0)`; break;
    case 'price': formula = `${attributeName}+price/1000*${safeAmount}`; break;
    case 'priceSubtract': formula = `MAX(${attributeName}-price/1000*${safeAmount},0)`; break;
    case 'set': formula = String(safeAmount); break;
    case 'reset': formula = '0'; break;
    case 'random': formula = `${attributeName}+RANDBETWEEN(1,${safeAmount})`; break;
    case 'randomSubtract': formula = `MAX(${attributeName}-RANDBETWEEN(1,${safeAmount}),0)`; break;
    case 'advanced': formula = null; break;
  }
  return formula !== null
    && quickGiftOperationSupportsMaximum(operation)
    && maximum !== undefined
    && Number.isFinite(maximum)
    ? `MIN(${formula},${maximum})`
    : formula;
}

export function quickGiftOperationLabel(operation: QuickGiftOperation, attributeName: string): string {
  const labels: Record<QuickGiftOperation, string> = {
    add: `让“${attributeName}”增加`,
    subtract: `让“${attributeName}”减少（最低为 0）`,
    price: `每 1 元让“${attributeName}”增加`,
    priceSubtract: `每 1 元让“${attributeName}”减少（最低为 0）`,
    set: `把“${attributeName}”设为`,
    reset: `把“${attributeName}”清零`,
    random: `让“${attributeName}”随机增加 1 到`,
    randomSubtract: `让“${attributeName}”随机减少 1 到（最低为 0）`,
    advanced: '使用下方高级规则',
  };
  return labels[operation];
}

export function quickGiftOperationUsesAmount(operation: QuickGiftOperation): boolean {
  return operation !== 'reset' && operation !== 'advanced';
}

export function quickGiftOperationUnit(operation: QuickGiftOperation, formattedAsTime: boolean): string {
  if (operation === 'advanced') return '在下方直接输入表达式';
  if (operation === 'reset') return '无需填写数值';
  return formattedAsTime ? '秒' : '单位';
}

export function quickGiftOperationSupportsMaximum(operation: QuickGiftOperation): boolean {
  return operation === 'add' || operation === 'price' || operation === 'random';
}
