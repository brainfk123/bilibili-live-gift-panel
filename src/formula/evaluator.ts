import { err } from './errors';
import { AstNode } from './parser';

export function evaluate(node: AstNode, env: Record<string, number>): number {
  switch (node.kind) {
    case 'num':
      return node.value;
    case 'var': {
      const v = env[node.name];
      if (v === undefined) throw err(`变量 "${node.name}" 未定义`, 0);
      return v;
    }
    case 'unary':
      return -evaluate(node.operand, env);
    case 'binary': {
      const l = evaluate(node.left, env);
      const r = evaluate(node.right, env);
      switch (node.op) {
        case '+': return l + r;
        case '-': return l - r;
        case '*': return l * r;
        case '/':
          if (r === 0) throw err('除数为零', 0);
          return l / r;
        case '>': return l > r ? 1 : 0;
        case '>=': return l >= r ? 1 : 0;
        case '<': return l < r ? 1 : 0;
        case '<=': return l <= r ? 1 : 0;
        case '=': return l === r ? 1 : 0;
        default:
          throw err(`未知运算符 ${node.op}`, 0);
      }
    }
    case 'call':
      return callFunction(node.name, node.args, env);
  }
}

function callFunction(name: string, args: AstNode[], env: Record<string, number>): number {
  const fn = name.toUpperCase();
  switch (fn) {
    case 'IF': {
      if (args.length !== 3) throw err('IF 需要 3 个参数', 0);
      const cond = evaluate(args[0], env);
      return evaluate(cond !== 0 ? args[1] : args[2], env);
    }
    case 'MAX':
    case 'MIN': {
      if (args.length === 0) throw err(`${fn} 至少需要 1 个参数`, 0);
      const vals = args.map((a) => evaluate(a, env));
      return fn === 'MAX' ? Math.max(...vals) : Math.min(...vals);
    }
    case 'ROUND': {
      if (args.length < 1 || args.length > 2) throw err('ROUND 需要 1-2 个参数', 0);
      const x = evaluate(args[0], env);
      const digits = args.length === 2 ? evaluate(args[1], env) : 0;
      const p = Math.pow(10, digits);
      return Math.round(x * p) / p;
    }
    case 'ABS':
      if (args.length !== 1) throw err('ABS 需要 1 个参数', 0);
      return Math.abs(evaluate(args[0], env));
    case 'RAND':
      if (args.length !== 0) throw err('RAND 不需要参数', 0);
      return Math.random();
    case 'RANDBETWEEN': {
      if (args.length !== 2) throw err('RANDBETWEEN 需要 2 个参数', 0);
      const lo = Math.ceil(evaluate(args[0], env));
      const hi = Math.floor(evaluate(args[1], env));
      if (hi < lo) throw err('RANDBETWEEN 最小值不能大于最大值', 0);
      return Math.floor(Math.random() * (hi - lo + 1)) + lo;
    }
    default:
      throw err(`未知函数 "${name}"`, 0);
  }
}
