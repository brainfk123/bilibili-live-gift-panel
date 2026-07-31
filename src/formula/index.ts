import { evaluate } from './evaluator';
import { AstNode, parse } from './parser';

export { FormulaError } from './errors';

export function evalFormula(input: string, env: Record<string, number>): number {
  return evaluate(parse(input), env);
}

export function collectVars(input: string): string[] {
  const ast = parse(input);
  const vars = new Set<string>();
  const walk = (n: AstNode): void => {
    if (n.kind === 'var') vars.add(n.name);
    else if (n.kind === 'call') n.args.forEach(walk);
    else if (n.kind === 'binary') { walk(n.left); walk(n.right); }
    else if (n.kind === 'unary') walk(n.operand);
  };
  walk(ast);
  return [...vars];
}
