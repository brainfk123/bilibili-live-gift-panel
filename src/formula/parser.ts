import { err } from './errors';
import { Token, tokenize } from './tokenizer';

export type AstNode =
  | { kind: 'num'; value: number }
  | { kind: 'var'; name: string }
  | { kind: 'unary'; op: string; operand: AstNode }
  | { kind: 'binary'; op: string; left: AstNode; right: AstNode }
  | { kind: 'call'; name: string; args: AstNode[] };

const COMPARE = new Set(['>', '>=', '<', '<=', '=']);

export function parse(input: string): AstNode {
  const tokens = tokenize(input);
  let idx = 0;

  const peek = (): Token => tokens[idx];
  const next = (): Token => tokens[idx++];
  const isOp = (v: string): boolean => peek().type === 'op' && peek().value === v;

  function expectParen(v: string): void {
    const t = peek();
    if (t.type !== 'paren' || t.value !== v) throw err(`缺少 "${v}"`, t.pos);
    next();
  }

  function parseExpr(): AstNode {
    let left = parseAdditive();
    const t = peek();
    if (t.type === 'op' && COMPARE.has(t.value)) {
      next();
      const right = parseAdditive();
      left = { kind: 'binary', op: t.value, left, right };
    }
    return left;
  }

  function parseAdditive(): AstNode {
    let left = parseMultiplicative();
    while (isOp('+') || isOp('-')) {
      const op = next().value;
      left = { kind: 'binary', op, left, right: parseMultiplicative() };
    }
    return left;
  }

  function parseMultiplicative(): AstNode {
    let left = parseUnary();
    while (isOp('*') || isOp('/')) {
      const op = next().value;
      left = { kind: 'binary', op, left, right: parseUnary() };
    }
    return left;
  }

  function parseUnary(): AstNode {
    if (isOp('-')) {
      next();
      return { kind: 'unary', op: '-', operand: parseUnary() };
    }
    return parsePrimary();
  }

  function parsePrimary(): AstNode {
    const t = peek();
    if (t.type === 'number') {
      next();
      return { kind: 'num', value: Number(t.value) };
    }
    if (t.type === 'ident') {
      next();
      if (peek().type === 'paren' && peek().value === '(') {
        next();
        const args: AstNode[] = [];
        if (peek().type !== 'paren') {
          args.push(parseExpr());
          while (isOp(',')) {
            next();
            args.push(parseExpr());
          }
        }
        expectParen(')');
        return { kind: 'call', name: t.value, args };
      }
      return { kind: 'var', name: t.value };
    }
    if (t.type === 'paren' && t.value === '(') {
      next();
      const node = parseExpr();
      expectParen(')');
      return node;
    }
    throw err('表达式不合法', t.pos);
  }

  const result = parseExpr();
  if (peek().type !== 'eof') {
    throw err(`多余的内容 "${peek().value}"`, peek().pos);
  }
  return result;
}
