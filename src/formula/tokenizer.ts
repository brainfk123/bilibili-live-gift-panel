import { err } from './errors';

export type TokenType = 'number' | 'ident' | 'op' | 'paren' | 'eof';

export interface Token {
  type: TokenType;
  value: string;
  pos: number;
}

const OP_SINGLE = new Set(['+', '-', '*', '/', '>', '<', '=']);
const OP_TWO = new Set(['>=', '<=']);

export function tokenize(input: string): Token[] {
  const tokens: Token[] = [];
  let i = 0;
  while (i < input.length) {
    const ch = input[i];
    if (ch === ' ' || ch === '\t') {
      i++;
      continue;
    }
    if (/[0-9]/.test(ch) || (ch === '.' && /[0-9]/.test(input[i + 1] ?? ''))) {
      const start = i;
      while (i < input.length && /[0-9.]/.test(input[i])) i++;
      tokens.push({ type: 'number', value: input.slice(start, i), pos: start });
      continue;
    }
    if (/[\p{L}_]/u.test(ch)) {
      const start = i;
      while (i < input.length && /[\p{L}\p{N}_]/u.test(input[i])) i++;
      tokens.push({ type: 'ident', value: input.slice(start, i), pos: start });
      continue;
    }
    const two = input.slice(i, i + 2);
    if (OP_TWO.has(two)) {
      tokens.push({ type: 'op', value: two, pos: i });
      i += 2;
      continue;
    }
    if (OP_SINGLE.has(ch)) {
      tokens.push({ type: 'op', value: ch, pos: i });
      i++;
      continue;
    }
    if (ch === '(' || ch === ')') {
      tokens.push({ type: 'paren', value: ch, pos: i });
      i++;
      continue;
    }
    if (ch === ',') {
      tokens.push({ type: 'op', value: ',', pos: i });
      i++;
      continue;
    }
    throw err(`无法识别的字符 "${ch}"`, i);
  }
  tokens.push({ type: 'eof', value: '', pos: input.length });
  return tokens;
}
