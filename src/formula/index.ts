import { AstNode, parse } from './parser';

export { FormulaError } from './errors';

export function collectVars(input: string): string[] {
  const ast = parse(input);
  const vars = new Set<string>();
  const walk = (node: AstNode): void => {
    if (node.kind === 'var') vars.add(node.name);
    else if (node.kind === 'call') node.args.forEach(walk);
    else if (node.kind === 'binary') { walk(node.left); walk(node.right); }
    else if (node.kind === 'unary') walk(node.operand);
  };
  walk(ast);
  return [...vars];
}
