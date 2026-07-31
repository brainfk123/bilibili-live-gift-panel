# Bilibili 直播礼物属性面板插件 — 实施计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 构建一个单 HTML 文件的 OBS 浏览器源插件：匿名监听 B 站直播间礼物/SC，按 Excel 风格公式规则累加多属性（如加班时间），以精美面板实时展示，各主播独立配置。

**Architecture:** 纯前端单文件（Vite+TS 构建，`vite-plugin-singlefile` 内联）。浏览器 WebSocket 直连 `wss://broadcastlv.chat.bilibili.com/sub`（匿名 uid=0，无登录），解析二进制帧（zlib 批量包用 `DecompressionStream('deflate')` 解压），SEND_GIFT/SC 事件进规则引擎 → 公式求值 → 条件校验 → 属性累加，localStorage 持久化。URL 参数 `?mode=display`（OBS 用）与 `?mode=config`（浏览器编辑）共用同一份状态。

**Tech Stack:** Vite 5 + TypeScript 5 + Vitest 2 + vite-plugin-singlefile 2。零运行时第三方依赖（仅浏览器内置 WebSocket/DecompressionStream/localStorage）。

## Global Constraints

- 运行时零第三方依赖；全部逻辑用浏览器内置能力实现
- 属性值用 JS `Number`（IEEE 754 double）
- 公式引擎禁止使用 `eval`/`new Function`
- 礼物按 `giftId` 匹配（非名称）
- 时间类属性按**秒**存储，显示 `HH:MM:SS`（超 24h 显示天数）
- 时间同步接口不调 `getDanmuInfo`（风控 -352），弹幕服务器地址硬编码为 `wss://broadcastlv.chat.bilibili.com/sub`
- 仅私下分发，不公开传播（非官方协议）
- 代码注释/文档用英文，UI 文案用中文
- 每个任务提交一次 git commit

---

### Task 1: 项目脚手架

**Files:**
- Create: `package.json`
- Create: `tsconfig.json`
- Create: `vite.config.ts`
- Create: `index.html`
- Create: `src/vite-env.d.ts`
- Create: `src/types.ts`
- Create: `src/storage.ts`
- Test: `tests/storage.test.ts`

**Interfaces:**
- Produces: `AppState` / `Attribute` / `GiftRule` / `Settings` 类型（全项目共享）；`loadState()` / `saveState(state)` / `resetState()`；Vite 构建产出单 HTML。

- [ ] **Step 1: 创建 package.json**

```json
{
  "name": "bilibili-live-gift-panel",
  "private": true,
  "version": "0.1.0",
  "type": "module",
  "scripts": {
    "dev": "vite",
    "build": "vite build",
    "fetch:catalog": "node scripts/fetch-gift-catalog.mjs",
    "test": "vitest run",
    "typecheck": "tsc --noEmit"
  },
  "devDependencies": {
    "typescript": "^5.5.0",
    "vite": "^5.4.0",
    "vite-plugin-singlefile": "^2.0.3",
    "vitest": "^2.1.0"
  }
}
```

- [ ] **Step 2: 创建 tsconfig.json**

```json
{
  "compilerOptions": {
    "target": "ES2022",
    "module": "ESNext",
    "moduleResolution": "bundler",
    "lib": ["ES2022", "DOM", "DOM.Iterable"],
    "strict": true,
    "noUnusedLocals": true,
    "noUnusedParameters": true,
    "noFallthroughCasesInSwitch": true,
    "skipLibCheck": true,
    "isolatedModules": true,
    "esModuleInterop": true,
    "resolveJsonModule": true,
    "types": ["vite/client"]
  },
  "include": ["src", "tests", "vite.config.ts"]
}
```

- [ ] **Step 3: 创建 vite.config.ts**

```ts
import { defineConfig } from 'vitest/config';
import { viteSingleFile } from 'vite-plugin-singlefile';

export default defineConfig({
  plugins: [viteSingleFile()],
  base: './',
  build: {
    target: 'es2022',
    cssCodeSplit: false,
  },
  test: {
    environment: 'node',
    include: ['tests/**/*.test.ts'],
  },
});
```

- [ ] **Step 4: 创建 index.html**

```html
<!DOCTYPE html>
<html lang="zh-CN">
<head>
  <meta charset="UTF-8">
  <meta name="viewport" content="width=device-width, initial-scale=1.0">
  <title>Bilibili Live Gift Panel</title>
</head>
<body>
  <div id="app"></div>
  <script type="module" src="/src/main.ts"></script>
</body>
</html>
```

- [ ] **Step 5: 创建 src/vite-env.d.ts**

```ts
/// <reference types="vite/client" />
```

- [ ] **Step 6: 创建 src/types.ts（全项目共享类型）**

```ts
export type DisplayFormat = 'hhmmss' | 'number' | 'suffix';

export interface Attribute {
  name: string;
  value: number;
  unit: 'seconds' | 'none';
  format: DisplayFormat;
  decimals: number;
  suffix: string;
  color?: string;
}

export interface GiftRule {
  id: string;
  giftId: number;
  attributeName: string;
  formula: string;
  minPrice?: number;
  cap?: number;
  dailyLimit?: number;
}

export interface GiftInfo {
  id: number;
  name: string;
  price: number;
  coinType: 'gold' | 'silver';
  imgBasic: string;
}

export interface RecentGift extends GiftInfo {
  lastReceived: number;
  count: number;
}

export interface DayStats {
  date: string;
  giftTotals: Record<number, number>;
  ruleTriggers: Record<string, number>;
}

export interface LogEntry {
  time: number;
  giftId: number;
  giftName: string;
  num: number;
  uname: string;
  attributeName: string;
  delta: number;
  valueAfter: number;
  ruleId: string;
}

export interface Settings {
  fontSize: number;
  accentColor: string;
  showStats: boolean;
  showConnection: boolean;
  align: 'left' | 'center' | 'right';
}

export interface AppState {
  roomId: string;
  attributes: Attribute[];
  rules: GiftRule[];
  settings: Settings;
  giftCatalog: GiftInfo[];
  recentGifts: RecentGift[];
  stats: Record<string, DayStats>;
  log: LogEntry[];
}

export const STORAGE_KEY = 'bilibili-live-gift-panel-v1';
export const MAX_LOG = 200;
```

- [ ] **Step 7: 创建 src/storage.ts**

```ts
import { AppState, MAX_LOG, STORAGE_KEY } from './types';

export const defaultState = (): AppState => ({
  roomId: '',
  attributes: [
    { name: '加班时间', value: 0, unit: 'seconds', format: 'hhmmss', decimals: 0, suffix: '' },
  ],
  rules: [],
  settings: {
    fontSize: 48,
    accentColor: '#fb7299',
    showStats: true,
    showConnection: true,
    align: 'left',
  },
  giftCatalog: [],
  recentGifts: [],
  stats: {},
  log: [],
});

export function loadState(): AppState {
  try {
    const raw = localStorage.getItem(STORAGE_KEY);
    if (!raw) return defaultState();
    const parsed = JSON.parse(raw) as Partial<AppState>;
    const base = defaultState();
    return {
      ...base,
      ...parsed,
      settings: { ...base.settings, ...(parsed.settings ?? {}) },
      attributes: parsed.attributes ?? base.attributes,
      rules: parsed.rules ?? [],
    };
  } catch {
    return defaultState();
  }
}

export function saveState(state: AppState): void {
  localStorage.setItem(STORAGE_KEY, JSON.stringify(state));
}

export function resetState(): void {
  localStorage.removeItem(STORAGE_KEY);
}

export function pruneLog(log: AppState['log']): AppState['log'] {
  return log.slice(-MAX_LOG);
}
```

- [ ] **Step 8: 编写测试 tests/storage.test.ts**

```ts
import { describe, it, expect, beforeEach, vi } from 'vitest';
import { defaultState, loadState, saveState, resetState } from '../src/storage';

const mem = new Map<string, string>();
vi.stubGlobal('localStorage', {
  getItem: (k: string) => mem.get(k) ?? null,
  setItem: (k: string, v: string) => void mem.set(k, v),
  removeItem: (k: string) => void mem.delete(k),
});

beforeEach(() => mem.clear());

describe('storage', () => {
  it('loads default state when empty', () => {
    const s = loadState();
    expect(s.attributes[0].name).toBe('加班时间');
    expect(s.rules).toEqual([]);
  });

  it('round-trips state through save/load', () => {
    const s = defaultState();
    s.roomId = '2145';
    s.attributes[0].value = 3600;
    s.rules.push({ id: 'r1', giftId: 30607, attributeName: '加班时间', formula: 'price/1000*60' });
    saveState(s);
    const loaded = loadState();
    expect(loaded.roomId).toBe('2145');
    expect(loaded.attributes[0].value).toBe(3600);
    expect(loaded.rules).toHaveLength(1);
  });

  it('resetState removes stored state', () => {
    saveState(defaultState());
    resetState();
    expect(loadState().roomId).toBe('');
  });
});
```

- [ ] **Step 9: 运行测试确认通过**

Run: `npm test`
Expected: 3 tests PASS

- [ ] **Step 10: 提交**

```bash
git add -A
git commit -m "chore: scaffold vite+ts project with storage layer"
```

---

### Task 2: 公式引擎（tokenizer + parser + evaluator）

**Files:**
- Create: `src/formula/errors.ts`
- Create: `src/formula/tokenizer.ts`
- Create: `src/formula/parser.ts`
- Create: `src/formula/evaluator.ts`
- Create: `src/formula/index.ts`
- Test: `tests/formula.test.ts`

**Interfaces:**
- Consumes: 无（纯函数）
- Produces:
  - `tokenize(input: string): Token[]`
  - `parse(input: string): AstNode`
  - `evaluate(node: AstNode, env: Record<string, number>): number`
  - `evalFormula(input: string, env: Record<string, number>): number`
  - `collectVars(input: string): string[]`（配置面板实时预览用）

语法（Excel 风格，面向非编程用户）：
- 数字：`1`、`2.5`、`.5`
- 变量：`price`（礼物单价瓜子）、`count`（本次数量）、属性名（可含中文，如 `加班时间`）
- 运算符：`+ - * /`，比较 `> >= < <= =`，括号，逗号分隔参数
- 函数：`IF(条件,a,b)`、`MAX(a,b,...)`、`MIN(a,b,...)`、`ROUND(x,位数)`、`ABS(x)`、`RAND()`（[0,1) 随机）、`RANDBETWEEN(最小,最大)`（区间随机整数）
- 比较结果转数字：真=1、假=0

- [ ] **Step 1: 创建 src/formula/errors.ts**

```ts
export class FormulaError extends Error {
  constructor(message: string, public readonly pos: number) {
    super(message);
    this.name = 'FormulaError';
  }
}

export function err(msg: string, pos: number): FormulaError {
  return new FormulaError(`${msg}（位置 ${pos + 1}）`, pos);
}
```

- [ ] **Step 2: 创建 src/formula/tokenizer.ts**

```ts
import { err, FormulaError } from './errors';

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
```

- [ ] **Step 3: 创建 src/formula/parser.ts**

```ts
import { err, FormulaError } from './errors';
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
      const pos = next().pos;
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
```

- [ ] **Step 4: 创建 src/formula/evaluator.ts**

```ts
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
```

- [ ] **Step 5: 创建 src/formula/index.ts**

```ts
import { evaluate } from './evaluator';
import { parse } from './parser';

export { FormulaError } from './errors';

export function evalFormula(input: string, env: Record<string, number>): number {
  return evaluate(parse(input), env);
}

export function collectVars(input: string): string[] {
  const ast = parse(input);
  const vars = new Set<string>();
  const walk = (n: any): void => {
    if (n.kind === 'var') vars.add(n.name);
    else if (n.kind === 'call') n.args.forEach(walk);
    else if (n.kind === 'binary') { walk(n.left); walk(n.right); }
    else if (n.kind === 'unary') walk(n.operand);
  };
  walk(ast);
  return [...vars];
}
```

- [ ] **Step 6: 编写测试 tests/formula.test.ts**

```ts
import { describe, it, expect } from 'vitest';
import { evalFormula, collectVars, FormulaError } from '../src/formula';

const env = { price: 1000, count: 3, 加班时间: 100 };

describe('formula basic arithmetic', () => {
  it('adds', () => expect(evalFormula('1+2', env)).toBe(3));
  it('subtracts', () => expect(evalFormula('5-2', env)).toBe(3));
  it('multiplies', () => expect(evalFormula('3*4', env)).toBe(12));
  it('divides', () => expect(evalFormula('price/1000', env)).toBe(1));
  it('respects precedence', () => expect(evalFormula('2+3*4', env)).toBe(14));
  it('respects parentheses', () => expect(evalFormula('(2+3)*4', env)).toBe(20));
  it('handles unary minus', () => expect(evalFormula('-3+5', env)).toBe(2));
  it('uses variables', () => expect(evalFormula('price/1000*60', env)).toBe(60));
  it('uses chinese attribute name', () => expect(evalFormula('加班时间+50', env)).toBe(150));
  it('uses count', () => expect(evalFormula('count*5', env)).toBe(15));
});

describe('formula functions', () => {
  it('IF true branch', () => expect(evalFormula('IF(price>=1000, 10, 1)', env)).toBe(10));
  it('IF false branch', () => expect(evalFormula('IF(price>1000, 10, 1)', env)).toBe(1));
  it('MAX', () => expect(evalFormula('MAX(1,5,3)', env)).toBe(5));
  it('MIN', () => expect(evalFormula('MIN(1,5,3)', env)).toBe(1));
  it('ROUND', () => expect(evalFormula('ROUND(1.567, 2)', env)).toBe(1.57));
  it('ABS', () => expect(evalFormula('ABS(-7)', env)).toBe(7));
  it('RAND in [0,1)', () => {
    for (let i = 0; i < 100; i++) {
      const v = evalFormula('RAND()', env);
      expect(v).toBeGreaterThanOrEqual(0);
      expect(v).toBeLessThan(1);
    }
  });
  it('RANDBETWEEN in range', () => {
    for (let i = 0; i < 200; i++) {
      const v = evalFormula('RANDBETWEEN(10, 60)', env);
      expect(v).toBeGreaterThanOrEqual(10);
      expect(v).toBeLessThanOrEqual(60);
      expect(Number.isInteger(v)).toBe(true);
    }
  });
  it('nested functions', () => expect(evalFormula('MAX(IF(price>500, 100, 0), 50)', env)).toBe(100));
});

describe('formula errors', () => {
  it('division by zero throws', () => expect(() => evalFormula('1/0', env)).toThrow(/除数为零/));
  it('unknown variable throws', () => expect(() => evalFormula('foo+1', env)).toThrow(/未定义/));
  it('missing paren throws', () => expect(() => evalFormula('(1+2', env)).toThrow(/缺少/));
  it('trailing garbage throws', () => expect(() => evalFormula('1+2 abc', env)).toThrow(/多余/));
  it('collectVars finds variables', () => expect(collectVars('price/1000*count').sort()).toEqual(['count', 'price']));
});
```

- [ ] **Step 7: 运行测试确认通过**

Run: `npm test`
Expected: 全部 PASS（formula 子套件）

- [ ] **Step 8: 运行 typecheck**

Run: `npm run typecheck`
Expected: 无错误

- [ ] **Step 9: 提交**

```bash
git add -A
git commit -m "feat: excel-style formula engine"
```

---

### Task 3: B站弹幕二进制协议（编解码 + zlib）

**Files:**
- Create: `src/bilibili/protocol.ts`
- Test: `tests/protocol.test.ts`

**Interfaces:**
- Produces:
  - `encodePacket(op: number, body: Uint8Array, protover?: number): Uint8Array`
  - `encodeJson(op: number, obj: unknown, protover?: number): Uint8Array`
  - `decodePackets(data: Uint8Array): Packet[]`，`Packet = { protover, op, body: Uint8Array }`
  - `inflate(body: Uint8Array): Promise<Uint8Array>`（zlib 解压）
  - `decodeText(body: Uint8Array): string`
  - 操作码常量：`OP_AUTH=7`、`OP_AUTH_REPLY=8`、`OP_HEARTBEAT=2`、`OP_MESSAGE=5`

协议格式（16 字节头 + 负载，大端）：
- bytes 0-3: 总长度（头+体）
- bytes 4-5: 头长度 = 16
- bytes 6-7: 协议版本（0=明文，2=zlib 压缩批量）
- bytes 8-11: 操作码
- bytes 12-15: 序号（=1）

- [ ] **Step 1: 创建 src/bilibili/protocol.ts**

```ts
export const OP_AUTH = 7;
export const OP_AUTH_REPLY = 8;
export const OP_HEARTBEAT = 2;
export const OP_MESSAGE = 5;

export interface Packet {
  protover: number;
  op: number;
  body: Uint8Array;
}

const HEADER_LEN = 16;

export function encodePacket(op: number, body: Uint8Array, protover = 0): Uint8Array {
  const totalLen = HEADER_LEN + body.length;
  const buf = new ArrayBuffer(totalLen);
  const view = new DataView(buf);
  view.setUint32(0, totalLen);
  view.setUint16(4, HEADER_LEN);
  view.setUint16(6, protover);
  view.setUint32(8, op);
  view.setUint32(12, 1);
  new Uint8Array(buf, HEADER_LEN).set(body);
  return new Uint8Array(buf);
}

export function encodeJson(op: number, obj: unknown, protover = 0): Uint8Array {
  return encodePacket(op, new TextEncoder().encode(JSON.stringify(obj)), protover);
}

export function decodePackets(data: Uint8Array): Packet[] {
  const packets: Packet[] = [];
  let offset = 0;
  const view = new DataView(data.buffer, data.byteOffset, data.byteLength);
  while (offset + HEADER_LEN <= data.length) {
    const totalLen = view.getUint32(offset);
    const headerLen = view.getUint16(offset + 4);
    const protover = view.getUint16(offset + 6);
    const op = view.getUint32(offset + 8);
    if (totalLen < headerLen || offset + totalLen > data.length) break;
    const body = data.slice(offset + headerLen, offset + totalLen);
    packets.push({ protover, op, body });
    offset += totalLen;
  }
  return packets;
}

export function decodeText(body: Uint8Array): string {
  return new TextDecoder().decode(body);
}

export async function inflate(body: Uint8Array): Promise<Uint8Array> {
  const ds = new DecompressionStream('deflate');
  const stream = new Blob([body]).stream().pipeThrough(ds);
  const buf = await new Response(stream).arrayBuffer();
  return new Uint8Array(buf);
}
```

- [ ] **Step 2: 编写测试 tests/protocol.test.ts**

```ts
import { describe, it, expect } from 'vitest';
import { deflateSync } from 'node:zlib';
import { encodePacket, encodeJson, decodePackets, decodeText, inflate, OP_AUTH, OP_MESSAGE } from '../src/bilibili/protocol';

describe('protocol encode/decode', () => {
  it('round-trips a simple packet', () => {
    const body = new TextEncoder().encode('hello');
    const data = encodePacket(OP_AUTH, body);
    const packets = decodePackets(data);
    expect(packets).toHaveLength(1);
    expect(packets[0].op).toBe(OP_AUTH);
    expect(decodeText(packets[0].body)).toBe('hello');
  });

  it('decodes multiple concatenated packets', () => {
    const a = encodePacket(1, new TextEncoder().encode('a'));
    const b = encodePacket(2, new TextEncoder().encode('b'));
    const merged = new Uint8Array(a.length + b.length);
    merged.set(a);
    merged.set(b, a.length);
    const packets = decodePackets(merged);
    expect(packets).toHaveLength(2);
    expect(decodeText(packets[0].body)).toBe('a');
    expect(decodeText(packets[1].body)).toBe('b');
  });

  it('encodes JSON body', () => {
    const data = encodeJson(OP_MESSAGE, { cmd: 'TEST', data: 1 });
    const packets = decodePackets(data);
    expect(JSON.parse(decodeText(packets[0].body))).toEqual({ cmd: 'TEST', data: 1 });
  });

  it('inflates zlib data', async () => {
    const raw = new TextEncoder().encode(JSON.stringify({ hello: 'world' }));
    const compressed = new Uint8Array(deflateSync(raw));
    const out = await inflate(compressed);
    expect(decodeText(out)).toBe(JSON.stringify({ hello: 'world' }));
  });
});
```

- [ ] **Step 3: 运行测试确认通过**

Run: `npx vitest run tests/protocol.test.ts`
Expected: 4 tests PASS

- [ ] **Step 4: 提交**

```bash
git add -A
git commit -m "feat: bilibili danmaku binary protocol codec"
```

---

### Task 4: 事件解析（SEND_GIFT / SC）+ 礼物/属性格式化

**Files:**
- Create: `src/bilibili/messages.ts`
- Create: `src/format.ts`
- Test: `tests/messages.test.ts`

**Interfaces:**
- Produces:
  - `GiftEvent { giftId, giftName, num, price, coinType, totalCoin, uname, uid, timestamp, imgBasic, rnd }`
  - `ScEvent { id, price, message, uname, uid, giftId, giftName }`
  - `parseGift(data: any): GiftEvent`
  - `parseSc(data: any): ScEvent`
  - `formatValue(value: number, attr: Attribute): string`（HH:MM:SS / 千分位数字 / 数字+后缀）
  - `todayStr(): string`（YYYY-MM-DD）
  - `getDayStats(state: AppState): DayStats`

SEND_GIFT 关键字段：`giftId, giftName, num, price, coin_type, total_coin, uname, uid, timestamp, gift_info.img_basic, rnd`。
SC 关键字段：`id, price, message, user_info.uname, user_info.uid, gift.gift_id, gift.gift_name`。

- [ ] **Step 1: 创建 src/bilibili/messages.ts**

```ts
export interface GiftEvent {
  giftId: number;
  giftName: string;
  num: number;
  price: number;
  coinType: 'gold' | 'silver';
  totalCoin: number;
  uname: string;
  uid: number;
  timestamp: number;
  imgBasic: string;
  rnd: string;
}

export interface ScEvent {
  id: number;
  price: number;
  message: string;
  uname: string;
  uid: number;
  giftId: number;
  giftName: string;
}

export function parseGift(data: any): GiftEvent {
  return {
    giftId: data.giftId ?? 0,
    giftName: data.giftName ?? '',
    num: data.num ?? 1,
    price: data.price ?? 0,
    coinType: data.coin_type === 'gold' ? 'gold' : 'silver',
    totalCoin: data.total_coin ?? 0,
    uname: data.uname ?? '',
    uid: data.uid ?? 0,
    timestamp: data.timestamp ?? Math.floor(Date.now() / 1000),
    imgBasic: data.gift_info?.img_basic ?? '',
    rnd: String(data.rnd ?? `${data.timestamp ?? ''}-${data.uid ?? ''}-${data.giftId ?? ''}`),
  };
}

export function parseSc(data: any): ScEvent {
  return {
    id: data.id ?? 0,
    price: data.price ?? 0,
    message: data.message ?? '',
    uname: data.user_info?.uname ?? '',
    uid: data.uid ?? 0,
    giftId: data.gift?.gift_id ?? 0,
    giftName: data.gift?.gift_name ?? '醒目留言',
  };
}
```

- [ ] **Step 2: 创建 src/format.ts**

```ts
import { Attribute, AppState, DayStats } from './types';

export function formatValue(value: number, attr: Attribute): string {
  switch (attr.format) {
    case 'hhmmss':
      return formatSeconds(value);
    case 'number':
      return formatNumber(value, attr.decimals);
    case 'suffix':
      return `${formatNumber(value, attr.decimals)} ${attr.suffix}`.trim();
    default:
      return String(value);
  }
}

export function formatSeconds(totalSeconds: number): string {
  const s = Math.max(0, Math.floor(totalSeconds));
  const days = Math.floor(s / 86400);
  const hours = Math.floor((s % 86400) / 3600);
  const minutes = Math.floor((s % 3600) / 60);
  const seconds = s % 60;
  const pad = (n: number) => String(n).padStart(2, '0');
  const hms = `${pad(hours)}:${pad(minutes)}:${pad(seconds)}`;
  return days > 0 ? `${days}天 ${hms}` : hms;
}

export function formatNumber(value: number, decimals: number): string {
  return value.toLocaleString('zh-CN', {
    minimumFractionDigits: decimals,
    maximumFractionDigits: decimals,
  });
}

export function todayStr(): string {
  const d = new Date();
  const pad = (n: number) => String(n).padStart(2, '0');
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}`;
}

export function getDayStats(state: AppState): DayStats {
  const date = todayStr();
  let stats = state.stats[date];
  if (!stats) {
    stats = { date, giftTotals: {}, ruleTriggers: {} };
    state.stats[date] = stats;
  }
  return stats;
}
```

- [ ] **Step 3: 编写测试 tests/messages.test.ts**

```ts
import { describe, it, expect } from 'vitest';
import { parseGift, parseSc } from '../src/bilibili/messages';
import { formatValue, formatSeconds, formatNumber } from '../src/format';
import { Attribute } from '../src/types';

describe('parseGift', () => {
  it('extracts core fields', () => {
    const ev = parseGift({
      giftId: 30607,
      giftName: '辣条',
      num: 2,
      price: 20,
      coin_type: 'gold',
      total_coin: 40,
      uname: 'user',
      uid: 123,
      timestamp: 1700000000,
      gift_info: { img_basic: 'https://img' },
      rnd: 'x1',
    });
    expect(ev.giftId).toBe(30607);
    expect(ev.num).toBe(2);
    expect(ev.price).toBe(20);
    expect(ev.coinType).toBe('gold');
    expect(ev.imgBasic).toBe('https://img');
    expect(ev.rnd).toBe('x1');
  });

  it('handles missing optional fields', () => {
    const ev = parseGift({ giftId: 1, uname: 'u' });
    expect(ev.num).toBe(1);
    expect(ev.imgBasic).toBe('');
    expect(ev.rnd.length).toBeGreaterThan(0);
  });
});

describe('parseSc', () => {
  it('extracts fields', () => {
    const ev = parseSc({
      id: 42,
      price: 30,
      message: '你好',
      uid: 9,
      user_info: { uname: 'rich' },
      gift: { gift_id: 1223, gift_name: '醒目留言' },
    });
    expect(ev.id).toBe(42);
    expect(ev.message).toBe('你好');
    expect(ev.uname).toBe('rich');
    expect(ev.giftId).toBe(1223);
  });
});

describe('formatValue', () => {
  const hhmmss: Attribute = { name: 't', value: 0, unit: 'seconds', format: 'hhmmss', decimals: 0, suffix: '' };
  const num: Attribute = { name: 'n', value: 0, unit: 'none', format: 'number', decimals: 2, suffix: '' };
  const suffix: Attribute = { name: 's', value: 0, unit: 'none', format: 'suffix', decimals: 0, suffix: '次' };

  it('formats hhmmss', () => expect(formatValue(90305, hhmmss)).toBe('25:05:05'));
  it('formats hhmmss with days', () => expect(formatValue(3600 * 30 + 125, hhmmss)).toBe('1天 01:00:02'));
  it('formats number with decimals', () => expect(formatValue(1234.5, num)).toBe('1,234.50'));
  it('formats suffix', () => expect(formatValue(125, suffix)).toBe('125 次'));
  it('formatSeconds pads zeros', () => expect(formatSeconds(65)).toBe('00:01:05'));
  it('formatNumber thousands', () => expect(formatNumber(1234567, 0)).toBe('1,234,567'));
});
```

- [ ] **Step 4: 运行测试确认通过**

Run: `npx vitest run tests/messages.test.ts`
Expected: 全部 PASS

- [ ] **Step 5: 提交**

```bash
git add -A
git commit -m "feat: event parsing and value formatting"
```

---

### Task 5: WebSocket 客户端（匿名直连/认证/心跳/重连）

**Files:**
- Create: `src/bilibili/client.ts`
- Test: `tests/client.test.ts`

**Interfaces:**
- Produces:
  - `type ConnState = 'idle' | 'connecting' | 'connected' | 'reconnecting' | 'error'`
  - `interface DanmakuClientOptions { roomId: number; wsFactory?: (url: string) => WsLike; onMessage(cmd, data): void; onGift(ev: GiftEvent): void; onSc(ev: ScEvent): void; onState(s: ConnState): void }`
  - `class DanmakuClient { start(): void; stop(): void }`
  - `WsLike`（可注入假 socket 便于测试）

连接流程：open → 发认证包 `{uid:0, roomid, protover:2, platform:'web', buvid:随机, type:2}` → 30s 心跳 → 收到 op=5 消息解析 cmd → SEND_GIFT/SUPER_CHAT_MESSAGE 分发 → 断线指数退避重连（1s,2s,4s...最大30s）。

- [ ] **Step 1: 创建 src/bilibili/client.ts**

```ts
import { OP_AUTH, OP_AUTH_REPLY, OP_MESSAGE, OP_HEARTBEAT, Packet, decodePackets, decodeText, encodeJson, encodePacket, inflate } from './protocol';
import { GiftEvent, ScEvent, parseGift, parseSc } from './messages';

export type ConnState = 'idle' | 'connecting' | 'connected' | 'reconnecting' | 'error';

export interface WsLike {
  binaryType: string;
  onopen: (() => void) | null;
  onmessage: ((ev: { data: ArrayBuffer }) => void) | null;
  onclose: ((ev: { code: number }) => void) | null;
  onerror: ((ev: unknown) => void) | null;
  send: (data: ArrayBuffer) => void;
  close: () => void;
}

export interface DanmakuClientOptions {
  roomId: number;
  wsFactory?: (url: string) => WsLike;
  onMessage?: (cmd: string, data: any) => void;
  onGift?: (ev: GiftEvent) => void;
  onSc?: (ev: ScEvent) => void;
  onState?: (s: ConnState) => void;
}

const WS_URL = 'wss://broadcastlv.chat.bilibili.com/sub';
const HEARTBEAT_MS = 30000;

function randomBuvid(): string {
  let s = 'XY';
  for (let i = 0; i < 32; i++) s += Math.floor(Math.random() * 16).toString(16);
  return s;
}

export class DanmakuClient {
  private ws: WsLike | null = null;
  private heartbeatTimer: ReturnType<typeof setInterval> | null = null;
  private reconnectTimer: ReturnType<typeof setTimeout> | null = null;
  private attempt = 0;
  private stopped = false;

  constructor(private readonly opts: DanmakuClientOptions) {}

  start(): void {
    this.stopped = false;
    this.connect();
  }

  stop(): void {
    this.stopped = true;
    if (this.heartbeatTimer) clearInterval(this.heartbeatTimer);
    if (this.reconnectTimer) clearTimeout(this.reconnectTimer);
    this.ws?.close();
    this.ws = null;
  }

  private connect(): void {
    const ws = this.opts.wsFactory ? this.opts.wsFactory(WS_URL) : this.createWs(WS_URL);
    this.ws = ws;
    ws.binaryType = 'arraybuffer';
    ws.onopen = () => this.onOpen();
    ws.onmessage = (ev) => void this.onMessage(ev);
    ws.onclose = () => this.onClose();
    ws.onerror = () => { /* close 事件会触发重连 */ };
    this.opts.onState?.('connecting');
  }

  private createWs(url: string): WsLike {
    return new WebSocket(url) as unknown as WsLike;
  }

  private onOpen(): void {
    this.attempt = 0;
    this.opts.onState?.('connected');
    this.ws?.send(encodeJson(OP_AUTH, {
      uid: 0,
      roomid: this.opts.roomId,
      protover: 2,
      platform: 'web',
      buvid: randomBuvid(),
      type: 2,
    }));
    this.startHeartbeat();
  }

  private startHeartbeat(): void {
    if (this.heartbeatTimer) clearInterval(this.heartbeatTimer);
    this.heartbeatTimer = setInterval(() => {
      this.ws?.send(encodePacket(OP_HEARTBEAT, new Uint8Array(0)));
    }, HEARTBEAT_MS);
  }

  private async onMessage(ev: { data: ArrayBuffer }): Promise<void> {
    const packets = decodePackets(new Uint8Array(ev.data));
    for (const p of packets) {
      await this.handlePacket(p);
    }
  }

  private async handlePacket(p: Packet): Promise<void> {
    if (p.op === OP_AUTH_REPLY) {
      try {
        const reply = JSON.parse(decodeText(p.body));
        if (reply.code !== 0) {
          // 认证失败（如房间号错误），关闭连接触发重连兜底
          this.ws?.close();
        }
      } catch {
        /* ignore */
      }
      return;
    }
    if (p.op !== OP_MESSAGE) return;
    let bodies: Uint8Array[];
    if (p.protover === 2) {
      const inflated = await inflate(p.body);
      bodies = decodePackets(inflated).map((x) => x.body);
    } else {
      bodies = [p.body];
    }
    for (const body of bodies) {
      this.dispatchJson(body);
    }
  }

  private dispatchJson(body: Uint8Array): void {
    let parsed: any;
    try {
      parsed = JSON.parse(decodeText(body));
    } catch {
      return;
    }
    const cmd = parsed?.cmd;
    const data = parsed?.data;
    if (typeof cmd === 'string') this.opts.onMessage?.(cmd, data);
    if (cmd === 'SEND_GIFT') this.opts.onGift?.(parseGift(data ?? {}));
    if (cmd === 'SUPER_CHAT_MESSAGE') this.opts.onSc?.(parseSc(data ?? {}));
  }

  private onClose(): void {
    if (this.heartbeatTimer) clearInterval(this.heartbeatTimer);
    if (this.stopped) return;
    this.attempt++;
    const delay = Math.min(30000, Math.pow(2, this.attempt) * 1000);
    this.opts.onState?.('reconnecting');
    this.reconnectTimer = setTimeout(() => this.connect(), delay);
  }
}
```

- [ ] **Step 2: 编写测试 tests/client.test.ts（注入假 socket）**

```ts
import { describe, it, expect, vi } from 'vitest';
import { DanmakuClient, WsLike } from '../src/bilibili/client';
import { decodePackets, decodeText, encodeJson } from '../src/bilibili/protocol';

class FakeWs implements WsLike {
  binaryType = 'arraybuffer';
  onopen: (() => void) | null = null;
  onmessage: ((ev: { data: ArrayBuffer }) => void) | null = null;
  onclose: ((ev: { code: number }) => void) | null = null;
  onerror: ((ev: unknown) => void) | null = null;
  sent: ArrayBuffer[] = [];
  close = vi.fn();
  send(data: ArrayBuffer) { this.sent.push(data); }
  open() { this.onopen?.(); }
  message(data: ArrayBuffer) { this.onmessage?.({ data }); }
  closeFromServer(code = 1006) { this.onclose?.({ code }); }
}

function giftPacket(): ArrayBuffer {
  const gift = {
    cmd: 'SEND_GIFT',
    data: { giftId: 30607, giftName: '辣条', num: 1, price: 20, coin_type: 'gold', uname: 'u', uid: 1, timestamp: 1700000000, gift_info: { img_basic: '' }, rnd: 'r1' },
  };
  return encodeJson(5, gift).buffer as ArrayBuffer;
}

describe('DanmakuClient', () => {
  it('sends auth packet on open', () => {
    const fake = new FakeWs();
    const client = new DanmakuClient({ roomId: 2145, wsFactory: () => fake });
    client.start();
    fake.open();
    expect(fake.sent.length).toBe(1);
    const auth = JSON.parse(decodeText(decodePackets(new Uint8Array(fake.sent[0]))[0].body));
    expect(auth.roomid).toBe(2145);
    expect(auth.uid).toBe(0);
    expect(auth.protover).toBe(2);
  });

  it('dispatches gift event', () => {
    const fake = new FakeWs();
    const onGift = vi.fn();
    const client = new DanmakuClient({ roomId: 2145, wsFactory: () => fake, onGift });
    client.start();
    fake.open();
    fake.message(giftPacket());
    expect(onGift).toHaveBeenCalledTimes(1);
    const ev = onGift.mock.calls[0][0];
    expect(ev.giftName).toBe('辣条');
    expect(ev.giftId).toBe(30607);
  });

  it('reconnects on close with backoff', () => {
    vi.useFakeTimers();
    const factory = vi.fn(() => new FakeWs());
    const onState = vi.fn();
    const client = new DanmakuClient({ roomId: 1, wsFactory: factory, onState });
    client.start();
    const first = factory.mock.results[0].value as FakeWs;
    first.open();
    first.closeFromServer();
    vi.advanceTimersByTime(2000);
    expect(factory).toHaveBeenCalledTimes(2);
    vi.useRealTimers();
  });

  it('stop prevents reconnect', () => {
    vi.useFakeTimers();
    const factory = vi.fn(() => new FakeWs());
    const client = new DanmakuClient({ roomId: 1, wsFactory: factory });
    client.start();
    const first = factory.mock.results[0].value as FakeWs;
    first.open();
    client.stop();
    first.closeFromServer();
    vi.advanceTimersByTime(60000);
    expect(factory).toHaveBeenCalledTimes(1);
    vi.useRealTimers();
  });
});
```

- [ ] **Step 3: 运行测试确认通过**

Run: `npx vitest run tests/client.test.ts`
Expected: 全部 PASS

- [ ] **Step 4: 运行 typecheck**

Run: `npm run typecheck`
Expected: 无错误

- [ ] **Step 5: 提交**

```bash
git add -A
git commit -m "feat: websocket danmaku client with auth/heartbeat/reconnect"
```

---

### Task 6: 规则引擎 + 应用编排

**Files:**
- Create: `src/engine/rules.ts`
- Create: `src/engine/index.ts`
- Test: `tests/engine.test.ts`

**Interfaces:**
- Consumes: `GiftEvent`、`AppState`、`evalFormula`、`todayStr`、`getDayStats`
- Produces:
  - `interface TriggerResult { rule: GiftRule; gift: GiftEvent; delta: number; valueAfter: number }`
  - `applyGiftToState(state: AppState, gift: GiftEvent): TriggerResult[]`（纯函数，直接改 state，返回触发结果；含去重、条件校验、累加、日志）
  - `class Engine { constructor(state: AppState, onTrigger?: (r: TriggerResult) => void); handleGift(ev: GiftEvent): void; handleSc(ev: ScEvent): void; setConnected(s: ConnState): void; onTrigger 供 UI 播放动画 }`

规则执行顺序（每条匹配规则独立执行）：
1. minPrice 门槛：`gift.price < minPrice` → 跳过
2. dailyLimit：当天该规则触发次数 >= dailyLimit → 跳过
3. 公式求值（env = { price, count, 各属性名: 当前值 }）→ delta
4. cap 封顶：`value + delta > cap` → delta 收敛到 cap - value，若 <=0 → 跳过
5. 累加、记录统计与日志
6. 去重：同一 `rnd` 60 秒内只处理一次

- [ ] **Step 1: 创建 src/engine/rules.ts**

```ts
import { evalFormula } from '../formula';
import { getDayStats, todayStr } from '../format';
import { GiftEvent } from '../bilibili/messages';
import { AppState, GiftRule, LogEntry, MAX_LOG, pruneLog } from '../types';

export interface TriggerResult {
  rule: GiftRule;
  gift: GiftEvent;
  delta: number;
  valueAfter: number;
}

export function applyGiftToState(state: AppState, gift: GiftEvent): TriggerResult[] {
  const results: TriggerResult[] = [];
  const day = getDayStats(state);
  for (const rule of state.rules) {
    if (rule.giftId !== gift.giftId) continue;
    const attr = state.attributes.find((a) => a.name === rule.attributeName);
    if (!attr) continue;
    if (rule.minPrice !== undefined && gift.price < rule.minPrice) continue;
    const triggerCount = day.ruleTriggers[rule.id] ?? 0;
    if (rule.dailyLimit !== undefined && triggerCount >= rule.dailyLimit) continue;
    const env: Record<string, number> = { price: gift.price, count: gift.num };
    for (const a of state.attributes) env[a.name] = a.value;
    let delta: number;
    try {
      delta = evalFormula(rule.formula, env);
    } catch {
      continue;
    }
    if (!Number.isFinite(delta)) continue;
    if (rule.cap !== undefined) {
      const room = rule.cap - attr.value;
      if (room <= 0) continue;
      if (delta > room) delta = room;
    }
    attr.value += delta;
    day.ruleTriggers[rule.id] = triggerCount + 1;
    const entry: LogEntry = {
      time: gift.timestamp,
      giftId: gift.giftId,
      giftName: gift.giftName,
      num: gift.num,
      uname: gift.uname,
      attributeName: attr.name,
      delta,
      valueAfter: attr.value,
      ruleId: rule.id,
    };
    state.log = pruneLog([entry, ...state.log]);
    results.push({ rule, gift, delta, valueAfter: attr.value });
  }
  return results;
}

export function recordGiftTotals(state: AppState, gift: GiftEvent): void {
  const day = getDayStats(state);
  day.giftTotals[gift.giftId] = (day.giftTotals[gift.giftId] ?? 0) + gift.num;
}

export function resetTodayStats(state: AppState): void {
  state.stats[todayStr()] = { date: todayStr(), giftTotals: {}, ruleTriggers: {} };
}
```

- [ ] **Step 2: 创建 src/engine/index.ts**

```ts
import { ScEvent } from '../bilibili/messages';
import { DanmakuClient, ConnState } from '../bilibili/client';
import { AppState } from '../types';
import { upsertRecentGift } from '../gifts/catalog';
import { applyGiftToState, recordGiftTotals, TriggerResult } from './rules';

const DEDUP_WINDOW_MS = 60000;

export class Engine {
  private client: DanmakuClient;
  private seen = new Map<string, number>();
  private onState?: (s: ConnState) => void;

  constructor(
    private readonly state: AppState,
    private readonly onTrigger?: (r: TriggerResult) => void,
    wsFactory?: (url: string) => any,
  ) {
    this.client = new DanmakuClient({
      roomId: Number(state.roomId),
      wsFactory,
      onGift: (ev) => this.handleGift(ev),
      onSc: (ev) => this.handleSc(ev),
      onState: (s) => this.onState?.(s),
    });
  }

  setStateListener(fn: (s: ConnState) => void): void {
    this.onState = fn;
  }

  start(): void {
    if (!this.state.roomId) return;
    this.client.start();
  }

  stop(): void {
    this.client.stop();
  }

  handleGift(ev: any): void {
    if (this.isDuplicate(ev.rnd)) return;
    upsertRecentGift(this.state, ev);
    recordGiftTotals(this.state, ev);
    const results = applyGiftToState(this.state, ev);
    for (const r of results) this.onTrigger?.(r);
  }

  handleSc(ev: ScEvent): void {
    recordGiftTotals(this.state, {
      giftId: ev.giftId,
      giftName: ev.giftName,
      num: 1,
      price: ev.price,
      coinType: 'gold',
      totalCoin: ev.price,
      uname: ev.uname,
      uid: ev.uid,
      timestamp: Math.floor(Date.now() / 1000),
      imgBasic: '',
      rnd: `sc-${ev.id}`,
    });
  }

  private isDuplicate(key: string): boolean {
    const now = Date.now();
    if (!key) return false;
    const last = this.seen.get(key);
    this.seen.set(key, now);
    if (this.seen.size > 500) {
      for (const [k, t] of this.seen) {
        if (now - t > DEDUP_WINDOW_MS) this.seen.delete(k);
      }
    }
    return last !== undefined && now - last < DEDUP_WINDOW_MS;
  }
}
```

- [ ] **Step 3: 编写测试 tests/engine.test.ts**

```ts
import { describe, it, expect } from 'vitest';
import { applyGiftToState, recordGiftTotals } from '../src/engine/rules';
import { defaultState } from '../src/storage';
import { GiftEvent } from '../src/bilibili/messages';

function makeGift(overrides: Partial<GiftEvent>): GiftEvent {
  return {
    giftId: 30607,
    giftName: '辣条',
    num: 1,
    price: 20,
    coinType: 'gold',
    totalCoin: 20,
    uname: 'user',
    uid: 1,
    timestamp: 1700000000,
    imgBasic: '',
    rnd: `r-${Math.random()}`,
    ...overrides,
  };
}

describe('applyGiftToState', () => {
  it('applies formula delta', () => {
    const s = defaultState();
    s.attributes[0].value = 100;
    s.rules.push({ id: 'r1', giftId: 30607, attributeName: '加班时间', formula: 'price/1000*60' });
    const rs = applyGiftToState(s, makeGift({ price: 1000 }));
    expect(rs).toHaveLength(1);
    expect(rs[0].delta).toBe(60);
    expect(s.attributes[0].value).toBe(160);
  });

  it('respects minPrice threshold', () => {
    const s = defaultState();
    s.rules.push({ id: 'r1', giftId: 30607, attributeName: '加班时间', formula: '10', minPrice: 100 });
    expect(applyGiftToState(s, makeGift({ price: 20 }))).toHaveLength(0);
    expect(applyGiftToState(s, makeGift({ price: 150 }))).toHaveLength(1);
  });

  it('respects cap', () => {
    const s = defaultState();
    s.attributes[0].value = 90;
    s.rules.push({ id: 'r1', giftId: 30607, attributeName: '加班时间', formula: '100', cap: 100 });
    const rs = applyGiftToState(s, makeGift({}));
    expect(rs[0].delta).toBe(10);
    expect(s.attributes[0].value).toBe(100);
  });

  it('skips when cap already reached', () => {
    const s = defaultState();
    s.attributes[0].value = 100;
    s.rules.push({ id: 'r1', giftId: 30607, attributeName: '加班时间', formula: '100', cap: 100 });
    expect(applyGiftToState(s, makeGift({}))).toHaveLength(0);
  });

  it('respects dailyLimit', () => {
    const s = defaultState();
    s.rules.push({ id: 'r1', giftId: 30607, attributeName: '加班时间', formula: '10', dailyLimit: 2 });
    applyGiftToState(s, makeGift({}));
    applyGiftToState(s, makeGift({}));
    expect(applyGiftToState(s, makeGift({}))).toHaveLength(0);
    expect(s.attributes[0].value).toBe(20);
  });

  it('ignores non-matching gift', () => {
    const s = defaultState();
    s.rules.push({ id: 'r1', giftId: 30607, attributeName: '加班时间', formula: '10' });
    expect(applyGiftToState(s, makeGift({ giftId: 999 }))).toHaveLength(0);
  });

  it('writes log entry', () => {
    const s = defaultState();
    s.rules.push({ id: 'r1', giftId: 30607, attributeName: '加班时间', formula: '10' });
    applyGiftToState(s, makeGift({}));
    expect(s.log).toHaveLength(1);
    expect(s.log[0].delta).toBe(10);
    expect(s.log[0].attributeName).toBe('加班时间');
  });
});

describe('recordGiftTotals', () => {
  it('accumulates today totals', () => {
    const s = defaultState();
    recordGiftTotals(s, makeGift({ giftId: 30607, num: 2 }));
    recordGiftTotals(s, makeGift({ giftId: 30607, num: 3 }));
    const day = s.stats[Object.keys(s.stats)[0]];
    expect(day.giftTotals[30607]).toBe(5);
  });
});
```

- [ ] **Step 4: 运行测试确认通过**

Run: `npx vitest run tests/engine.test.ts`
Expected: 全部 PASS

- [ ] **Step 5: 运行 typecheck**

Run: `npm run typecheck`
Expected: 无错误

- [ ] **Step 6: 提交**

```bash
git add -A
git commit -m "feat: rule engine and app orchestrator"
```

---

### Task 7: 礼物目录抓取脚本 + 目录模块

**Files:**
- Create: `scripts/fetch-gift-catalog.mjs`
- Create: `src/gifts/catalog.ts`
- Test: `tests/catalog.test.ts`

**Interfaces:**
- Produces:
  - `scripts/fetch-gift-catalog.mjs`：抓取 `https://api.live.bilibili.com/xlive/web-room/v1/giftPanel/giftConfig?platform=pc&room_id=1&area_id=1&biz_code=live`，提取 `{id, name, price, coinType, imgBasic}`，写入 `src/data/gift-catalog.json`（构建期执行，运行时不需要网络）
  - `loadBuiltinCatalog(): GiftInfo[]`（import 构建期 JSON）
  - `upsertRecentGift(state, gift: GiftEvent): void`（自动捕获：目录没有的礼物记入 recentGifts）
  - `findGift(state, giftId): GiftInfo | undefined`（查目录+最近礼物）

- [ ] **Step 1: 创建 scripts/fetch-gift-catalog.mjs**

```js
import { writeFileSync, mkdirSync } from 'node:fs';
import { dirname } from 'node:path';
import { fileURLToPath } from 'node:url';

const URL = 'https://api.live.bilibili.com/xlive/web-room/v1/giftPanel/giftConfig?platform=pc&room_id=1&area_id=1&biz_code=live';
const OUT = fileURLToPath(new URL('../src/data/gift-catalog.json', import.meta.url));

async function main() {
  let list = [];
  try {
    const res = await fetch(URL, {
      headers: { 'User-Agent': 'Mozilla/5.0', Referer: 'https://live.bilibili.com/' },
    });
    const json = await res.json();
    list = (json?.data?.list ?? []).map((g) => ({
      id: g.id,
      name: g.name,
      price: g.price,
      coinType: g.coin_type === 'gold' ? 'gold' : 'silver',
      imgBasic: g.img_basic ?? '',
    }));
  } catch (err) {
    console.error('fetch gift catalog failed:', err.message);
    process.exitCode = 1;
  }
  mkdirSync(dirname(OUT), { recursive: true });
  writeFileSync(OUT, JSON.stringify(list, null, 2), 'utf-8');
  console.log(`gift catalog: ${list.length} gifts -> ${OUT}`);
}

main();
```

- [ ] **Step 2: 创建 src/gifts/catalog.ts**

```ts
import catalog from '../data/gift-catalog.json';
import { GiftEvent } from '../bilibili/messages';
import { AppState, GiftInfo, RecentGift } from '../types';

export const builtinCatalog: GiftInfo[] = catalog as GiftInfo[];

export function loadBuiltinCatalog(): GiftInfo[] {
  return builtinCatalog;
}

export function findGift(state: AppState, giftId: number): GiftInfo | undefined {
  const inCatalog = builtinCatalog.find((g) => g.id === giftId);
  if (inCatalog) return inCatalog;
  return state.recentGifts.find((g) => g.id === giftId);
}

export function upsertRecentGift(state: AppState, gift: GiftEvent): void {
  const existing = state.recentGifts.find((g) => g.id === gift.giftId);
  if (existing) {
    existing.count += gift.num;
    existing.lastReceived = gift.timestamp;
  } else {
    const recent: RecentGift = {
      id: gift.giftId,
      name: gift.giftName,
      price: gift.price,
      coinType: gift.coinType,
      imgBasic: gift.imgBasic,
      lastReceived: gift.timestamp,
      count: gift.num,
    };
    state.recentGifts.unshift(recent);
    state.recentGifts = state.recentGifts.slice(0, 100);
  }
}
```

- [ ] **Step 3: 创建 src/data/gift-catalog.json（占位，构建脚本会覆盖）**

```json
[]
```

- [ ] **Step 4: 编写测试 tests/catalog.test.ts**

```ts
import { describe, it, expect } from 'vitest';
import { upsertRecentGift, findGift } from '../src/gifts/catalog';
import { defaultState } from '../src/storage';
import { GiftEvent } from '../src/bilibili/messages';

function makeGift(id: number, name = '礼物'): GiftEvent {
  return { giftId: id, giftName: name, num: 1, price: 10, coinType: 'gold', totalCoin: 10, uname: 'u', uid: 1, timestamp: 1700000000, imgBasic: '', rnd: `x-${id}` };
}

describe('catalog', () => {
  it('upserts new gift to recent', () => {
    const s = defaultState();
    upsertRecentGift(s, makeGift(999));
    expect(s.recentGifts).toHaveLength(1);
    expect(s.recentGifts[0].id).toBe(999);
  });

  it('accumulates count on repeated gift', () => {
    const s = defaultState();
    upsertRecentGift(s, makeGift(999));
    upsertRecentGift(s, makeGift(999));
    expect(s.recentGifts[0].count).toBe(2);
  });

  it('finds gift in recent', () => {
    const s = defaultState();
    upsertRecentGift(s, makeGift(999));
    expect(findGift(s, 999)?.id).toBe(999);
  });
});
```

- [ ] **Step 5: 运行测试确认通过**

Run: `npx vitest run tests/catalog.test.ts`
Expected: 全部 PASS

- [ ] **Step 6: 验证抓取脚本能运行**

Run: `npm run fetch:catalog`
Expected: 输出 `gift catalog: N gifts -> src\data\gift-catalog.json`，且文件包含真实礼物

- [ ] **Step 7: 提交（先加一条常见礼物的真实数据以便离线使用）**

Run: 确认 `src/data/gift-catalog.json` 有内容后：

```bash
git add -A
git commit -m "feat: build-time gift catalog fetch and recent-gift capture"
```

---

### Task 8: 配置面板 UI（?mode=config）

**Files:**
- Create: `src/ui/config/config.css`
- Create: `src/ui/config/config.ts`
- Create: `src/ui/common.ts`（DOM 辅助函数）

**Interfaces:**
- Consumes: `loadState/saveState`、`types`、`gifts/catalog`、`format`、`formula`
- Produces: `mountConfig(root: HTMLElement): void`（侧边栏导航：房间/属性/规则/统计/设置）

视觉要求（规格 8.0）：现代深色主题、侧边栏导航、卡片化表单、公式编辑高亮+实时预览、空状态引导、导出/导入 JSON。

- [ ] **Step 1: 创建 src/ui/common.ts**

```ts
export function el<K extends keyof HTMLElementTagNameMap>(
  tag: K,
  props: Partial<HTMLElementTagNameMap[K]> & { class?: string; text?: string; children?: (HTMLElement | string)[] } = {},
  children?: (HTMLElement | string)[],
): HTMLElementTagNameMap[K] {
  const node = document.createElement(tag);
  if (props.class) node.className = props.class;
  if (props.text != null) node.textContent = props.text;
  for (const key of Object.keys(props)) {
    if (key === 'class' || key === 'text' || key === 'children') continue;
    (node as any)[key] = (props as any)[key];
  }
  const kids = props.children ?? children ?? [];
  for (const child of kids) {
    node.append(child as any);
  }
  return node;
}

export function inputField(label: string, value: string): HTMLInputElement {
  const wrap = el('label', { class: 'field' });
  wrap.append(el('span', { class: 'field-label', text: label }));
  const input = el('input', { class: 'field-input', value }) as HTMLInputElement;
  wrap.append(input);
  return input;
}

export function toast(message: string, root: HTMLElement): void {
  const t = el('div', { class: 'toast', text: message });
  root.append(t);
  setTimeout(() => t.classList.add('show'), 10);
  setTimeout(() => t.remove(), 2500);
}
```

- [ ] **Step 2: 创建 src/ui/config/config.css（完整样式，深色主题）**

```css
* { box-sizing: border-box; margin: 0; padding: 0; }
:root {
  --bg: #14151a;
  --bg-soft: #1c1e26;
  --card: #22242e;
  --border: #2e3140;
  --text: #e6e8ee;
  --text-dim: #9aa0b0;
  --accent: #fb7299;
  --radius: 12px;
}
html, body { height: 100%; }
body {
  background: var(--bg);
  color: var(--text);
  font-family: "Segoe UI", "PingFang SC", "Microsoft YaHei", system-ui, sans-serif;
  font-size: 14px;
}
#app { display: flex; min-height: 100vh; }

.sidebar {
  width: 200px; flex-shrink: 0;
  background: var(--bg-soft);
  border-right: 1px solid var(--border);
  padding: 16px 8px;
}
.sidebar-title { font-size: 16px; font-weight: 700; padding: 4px 12px 16px; }
.nav-item {
  display: block; width: 100%; text-align: left;
  padding: 10px 12px; border: none; background: none; color: var(--text-dim);
  border-radius: 8px; cursor: pointer; font-size: 14px;
}
.nav-item:hover { background: var(--card); color: var(--text); }
.nav-item.active { background: var(--accent); color: #fff; }

.content { flex: 1; padding: 24px; max-width: 900px; }
.section-title { font-size: 20px; font-weight: 700; margin-bottom: 16px; }

.card {
  background: var(--card); border: 1px solid var(--border);
  border-radius: var(--radius); padding: 16px; margin-bottom: 16px;
}
.card h3 { font-size: 15px; margin-bottom: 12px; }

.field { display: flex; flex-direction: column; gap: 6px; margin-bottom: 12px; }
.field-label { color: var(--text-dim); font-size: 13px; }
.field-input, select.field-input {
  background: var(--bg); color: var(--text);
  border: 1px solid var(--border); border-radius: 8px;
  padding: 9px 12px; font-size: 14px; outline: none;
}
.field-input:focus { border-color: var(--accent); }
.field-input.formula { font-family: "Cascadia Code", Consolas, monospace; }

.btn {
  background: var(--accent); color: #fff; border: none;
  border-radius: 8px; padding: 9px 18px; font-size: 14px; cursor: pointer;
}
.btn:hover { filter: brightness(1.1); }
.btn.ghost {
  background: transparent; color: var(--text); border: 1px solid var(--border);
}
.btn.danger { background: #e5484d; }

.list-item {
  display: flex; align-items: center; gap: 10px;
  background: var(--bg); border: 1px solid var(--border);
  border-radius: 8px; padding: 10px 12px; margin-bottom: 8px;
}
.gift-img { width: 36px; height: 36px; object-fit: contain; border-radius: 6px; background: #000; }
.list-item .grow { flex: 1; }
.list-item .name { font-weight: 600; }
.list-item .sub { color: var(--text-dim); font-size: 12px; }

.badge {
  background: var(--accent); color: #fff; border-radius: 999px;
  padding: 2px 8px; font-size: 12px;
}
.badge.unset { background: #e5a50a; }

.preview {
  margin-top: 8px; padding: 10px 12px;
  background: var(--bg); border: 1px solid var(--border);
  border-radius: 8px; font-size: 14px;
}
.preview .result { color: var(--accent); font-weight: 700; }
.preview .error { color: #ff6b6b; }
.preview .hint { color: var(--text-dim); font-size: 12px; margin-top: 4px; }

.row { display: flex; gap: 10px; align-items: center; flex-wrap: wrap; }
.gap { margin-top: 16px; }
.log-item { font-size: 13px; color: var(--text-dim); padding: 6px 0; border-bottom: 1px dashed var(--border); }
.log-item b { color: var(--text); }
.toast {
  position: fixed; right: 20px; bottom: 20px;
  background: var(--card); border: 1px solid var(--border);
  padding: 10px 16px; border-radius: 8px; opacity: 0; transform: translateY(8px);
  transition: all .2s;
}
.toast.show { opacity: 1; transform: none; }
.muted { color: var(--text-dim); }
.empty { padding: 24px; text-align: center; color: var(--text-dim); }
```

- [ ] **Step 3: 创建 src/ui/config/config.ts（配置面板主逻辑）**

```ts
import { AppState, GiftRule, STORAGE_KEY } from '../../types';
import { loadState, saveState } from '../../storage';
import { el, inputField, toast } from '../common';
import { builtinCatalog, findGift, upsertRecentGift } from '../../gifts/catalog';
import { evalFormula, collectVars, FormulaError } from '../../formula';
import { formatValue, todayStr } from '../../format';
import { parseGift } from '../../bilibili/messages';
import { DanmakuClient, ConnState } from '../../bilibili/client';

export function mountConfig(root: HTMLElement): void {
  let state = loadState();
  let current = 'room';
  const content = el('div', { class: 'content' });
  const sidebar = el('div', { class: 'sidebar' });
  sidebar.append(el('div', { class: 'sidebar-title', text: '直播礼物面板' }));
  const nav = [
    ['room', '房间设置'],
    ['attributes', '属性管理'],
    ['rules', '礼物规则'],
    ['stats', '统计'],
    ['settings', '设置'],
  ] as const;
  const navItems: Record<string, HTMLButtonElement> = {};
  for (const [key, label] of nav) {
    const item = el('button', { class: 'nav-item', text: label }) as HTMLButtonElement;
    item.onclick = () => switchTo(key);
    navItems[key] = item;
    sidebar.append(item);
  }
  root.append(sidebar, content);

  function switchTo(key: string): void {
    current = key;
    for (const [k, item] of Object.entries(navItems)) item.classList.toggle('active', k === key);
    render();
  }

  function render(): void {
    content.replaceChildren();
    if (current === 'room') renderRoom();
    else if (current === 'attributes') renderAttributes();
    else if (current === 'rules') renderRules();
    else if (current === 'stats') renderStats();
    else renderSettings();
  }

  function save(): void {
    saveState(state);
  }

  function renderRoom(): void {
    content.append(el('div', { class: 'section-title', text: '房间设置' }));
    const card = el('div', { class: 'card' });
    const roomInput = inputField('直播间房间号（live.bilibili.com/xxxx 中的数字）', state.roomId);
    roomInput.placeholder = '例如 2145';
    const row = el('div', { class: 'row gap' });
    const statusText = el('span', { text: '未连接' });
    const connectBtn = el('button', { class: 'btn', text: '测试连接' }) as HTMLButtonElement;
    let client: DanmakuClient | null = null;
    connectBtn.onclick = () => {
      const roomId = roomInput.value.trim();
      if (!roomId) { toast('请输入房间号', root); return; }
      state.roomId = roomId;
      save();
      client?.stop();
      client = new DanmakuClient({
        roomId: Number(roomId),
        onState: (s: ConnState) => {
          statusText.textContent = s === 'connected' ? '已连接' : s === 'connecting' ? '连接中…' : s === 'reconnecting' ? '重连中…' : '连接失败';
        },
        onGift: (ev) => {
          upsertRecentGift(state, ev);
          save();
        },
      });
      client.start();
    };
    row.append(connectBtn, statusText);
    card.append(roomInput, row);
    content.append(card);
  }

  function renderAttributes(): void {
    content.append(el('div', { class: 'section-title', text: '属性管理' }));
    const addBtn = el('button', { class: 'btn', text: '+ 新增属性' });
    addBtn.onclick = () => {
      state.attributes.push({ name: `属性${state.attributes.length + 1}`, value: 0, unit: 'seconds', format: 'hhmmss', decimals: 0, suffix: '' });
      save();
      render();
    };
    content.append(addBtn, el('div', { class: 'gap' }));
    for (let i = 0; i < state.attributes.length; i++) {
      const a = state.attributes[i];
      const card = el('div', { class: 'card' });
      const nameInput = inputField('名称', a.name);
      nameInput.oninput = () => { a.name = nameInput.value; save(); };
      const unitSelect = el('select', { class: 'field-input' }) as HTMLSelectElement;
      unitSelect.innerHTML = `<option value="seconds">秒（时间类）</option><option value="none">无单位（数值类）</option>`;
      unitSelect.value = a.unit;
      unitSelect.onchange = () => { a.unit = unitSelect.value as any; save(); render(); };
      const fmtSelect = el('select', { class: 'field-input' }) as HTMLSelectElement;
      fmtSelect.innerHTML = `<option value="hhmmss">HH:MM:SS 计时器</option><option value="number">纯数字</option><option value="suffix">数字+后缀</option>`;
      fmtSelect.value = a.format;
      fmtSelect.onchange = () => { a.format = fmtSelect.value as any; save(); render(); };
      const preview = el('div', { class: 'preview' });
      const updatePreview = () => {
        preview.replaceChildren(el('span', { text: `当前值：` }), el('span', { class: 'result', text: formatValue(a.value, a) }));
      };
      updatePreview();
      const valueInput = inputField('手动调整当前值', String(a.value));
      valueInput.oninput = () => {
        const v = Number(valueInput.value);
        if (Number.isFinite(v)) { a.value = v; save(); updatePreview(); }
      };
      const resetBtn = el('button', { class: 'btn ghost', text: '清零' });
      resetBtn.onclick = () => { a.value = 0; valueInput.value = '0'; save(); updatePreview(); };
      const delBtn = el('button', { class: 'btn danger', text: '删除' });
      delBtn.onclick = () => {
        state.attributes.splice(i, 1);
        state.rules = state.rules.filter((r) => r.attributeName !== a.name);
        save();
        render();
      };
      card.append(
        nameInput,
        el('div', { class: 'field', children: [el('span', { class: 'field-label', text: '单位' }), unitSelect] }),
        el('div', { class: 'field', children: [el('span', { class: 'field-label', text: '显示格式' }), fmtSelect] }),
        valueInput, preview,
        el('div', { class: 'row' }, [resetBtn, delBtn]),
      );
      content.append(card);
    }
  }

  function renderRules(): void {
    content.append(el('div', { class: 'section-title', text: '礼物规则' }));
    if (state.rules.length > 0) {
      for (const rule of state.rules) {
        const gi = findGift(state, rule.giftId);
        const item = el('div', { class: 'list-item' });
        const img = el('img', { class: 'gift-img' }) as HTMLImageElement;
        img.src = gi?.imgBasic || 'data:image/gif;base64,R0lGODlhAQABAIAAAAAAAP///yH5BAEAAAAALAAAAAABAAEAAAIBRAA7';
        const del = el('button', { class: 'btn danger', text: '删除' });
        del.onclick = () => {
          state.rules = state.rules.filter((r) => r.id !== rule.id);
          save();
          render();
        };
        item.append(img,
          el('div', { class: 'grow' }, [
            el('div', { class: 'name', text: `${gi?.name ?? rule.giftId} → ${rule.attributeName}` }),
            el('div', { class: 'sub', text: `${rule.formula}` }),
          ]),
          del);
        content.append(item);
      }
    } else {
      content.append(el('div', { class: 'empty', text: '还没有规则。在下方搜索礼物并创建规则，或等待观众送出礼物后自动捕获。' }));
    }

    const addCard = el('div', { class: 'card' });
    addCard.append(el('h3', { text: '新增规则' }));
    const search = el('input', { class: 'field-input' }) as HTMLInputElement;
    search.placeholder = '搜索礼物名称…';
    const giftList = el('div', {});
    const allGifts = [...state.recentGifts, ...builtinCatalog];
    function renderGiftList(filter: string): void {
      giftList.replaceChildren();
      const list = allGifts.filter((g) => g.name.includes(filter) || String(g.id).includes(filter)).slice(0, 50);
      if (list.length === 0) giftList.append(el('div', { class: 'empty', text: '没有匹配的礼物' }));
      for (const g of list) {
        const row = el('div', { class: 'list-item' });
        const img = el('img', { class: 'gift-img' }) as HTMLImageElement;
        img.src = g.imgBasic || 'data:image/gif;base64,R0lGODlhAQABAIAAAAAAAP///yH5BAEAAAAALAAAAAABAAEAAAIBRAA7';
        const configured = state.rules.some((r) => r.giftId === g.id);
        row.append(img,
          el('div', { class: 'grow' }, [
            el('div', { class: 'name', text: g.name }),
            el('div', { class: 'sub', text: `ID ${g.id}` }),
          ]),
          configured ? el('span', { class: 'badge', text: '已设置' }) : el('span', { class: 'badge unset', text: '未配置' }));
        row.onclick = () => openRuleEditor(g.id, g.name, g.imgBasic);
        giftList.append(row);
      }
    }
    renderGiftList('');
    search.oninput = () => renderGiftList(search.value.trim());
    addCard.append(search, giftList);
    content.append(addCard);

    const manualCard = el('div', { class: 'card' });
    manualCard.append(el('h3', { text: '手动添加礼物' }));
    const gidInput = inputField('礼物 ID', '');
    const gnameInput = inputField('礼物名称（用于显示）', '');
    const giconInput = inputField('图标 URL（可选）', '');
    const addBtn = el('button', { class: 'btn', text: '添加到目录并建规则' });
    addBtn.onclick = () => {
      const gid = Number(gidInput.value.trim());
      if (!gid) { toast('请输入礼物 ID', root); return; }
      const name = gnameInput.value.trim() || `礼物${gid}`;
      upsertRecentGift(state, parseGift({ giftId: gid, giftName: name, gift_info: { img_basic: giconInput.value.trim() } }));
      save();
      openRuleEditor(gid, name, giconInput.value.trim());
    };
    manualCard.append(gidInput, gnameInput, giconInput, addBtn);
    content.append(manualCard);
  }

  function openRuleEditor(giftId: number, giftName: string, giftImg: string): void {
    const overlay = el('div', { class: 'overlay' });
    const card = el('div', { class: 'card' });
    card.append(el('h3', { text: `配置规则：${giftName}` }));
    const attrSelect = el('select', { class: 'field-input' }) as HTMLSelectElement;
    attrSelect.innerHTML = state.attributes.map((a) => `<option>${a.name}</option>`).join('');
    const formulaInput = inputField('公式（price=单价  count=数量）', 'price/1000*60');
    formulaInput.classList.add('formula');
    formulaInput.placeholder = '例如 price/1000*60 或 RANDBETWEEN(10,60)';
    const preview = el('div', { class: 'preview' });
    const minInput = inputField('最低门槛 price≥（可留空）', '');
    const capInput = inputField('上限封顶（可留空）', '');
    const limitInput = inputField('当日限次（可留空）', '');
    function updatePreview(): void {
      const formula = formulaInput.value.trim();
      preview.replaceChildren();
      try {
        const env: Record<string, number> = { price: 1000, count: 1 };
        for (const a of state.attributes) env[a.name] = a.value;
        const vars = collectVars(formula);
        const missing = vars.filter((v) => v !== 'price' && v !== 'count' && !state.attributes.some((a) => a.name === v));
        const result = evalFormula(formula, env);
        const target = state.attributes[attrSelect.selectedIndex];
        preview.append(el('div', { text: `示例：单价1000、数量1 时，结果为：` }),
          el('span', { class: 'result', text: target ? formatValue(result, target) : String(result) }));
        if (missing.length > 0) preview.append(el('div', { class: 'hint error', text: `未定义变量：${missing.join('、')}` }));
        if (vars.length === 0) preview.append(el('div', { class: 'hint', text: '提示：可使用变量 price（礼物单价）、count（数量），以及属性名' }));
      } catch (e) {
        const msg = e instanceof FormulaError ? e.message : String(e);
        preview.append(el('div', { class: 'error', text: msg }));
      }
    }
    formulaInput.oninput = updatePreview;
    attrSelect.onchange = updatePreview;
    updatePreview();
    const saveBtn = el('button', { class: 'btn', text: '保存规则' });
    saveBtn.onclick = () => {
      const formula = formulaInput.value.trim();
      if (!formula) { toast('请填写公式', root); return; }
      try {
        evalFormula(formula, { price: 1000, count: 1 });
      } catch {
        toast('公式有误，无法保存', root);
        return;
      }
      const attrName = state.attributes[attrSelect.selectedIndex]?.name;
      if (!attrName) { toast('请先创建属性', root); return; }
      const rule: GiftRule = {
        id: `r-${Date.now()}-${Math.random().toString(36).slice(2, 6)}`,
        giftId,
        attributeName: attrName,
        formula,
        minPrice: minInput.value ? Number(minInput.value) : undefined,
        cap: capInput.value ? Number(capInput.value) : undefined,
        dailyLimit: limitInput.value ? Number(limitInput.value) : undefined,
      };
      state.rules.push(rule);
      save();
      overlay.remove();
      render();
      toast('规则已保存', root);
    };
    const cancelBtn = el('button', { class: 'btn ghost', text: '取消' });
    cancelBtn.onclick = () => overlay.remove();
    card.append(attrSelect, formulaInput, preview, minInput, capInput, limitInput, el('div', { class: 'row gap' }, [saveBtn, cancelBtn]));
    overlay.append(card);
    overlay.onclick = (e) => { if (e.target === overlay) overlay.remove(); };
    root.append(overlay);
  }

  function renderStats(): void {
    content.append(el('div', { class: 'section-title', text: '今日统计' }));
    const day = state.stats[todayStr()];
    const card = el('div', { class: 'card' });
    if (day && Object.keys(day.giftTotals).length > 0) {
      for (const [gid, cnt] of Object.entries(day.giftTotals)) {
        const g = findGift(state, Number(gid));
        card.append(el('div', { class: 'list-item' }, [
          el('span', { text: `${g?.name ?? gid} x${cnt}` }),
        ]));
      }
    } else {
      card.append(el('div', { class: 'empty', text: '今天还没有礼物' }));
    }
    content.append(card);
    const logCard = el('div', { class: 'card' });
    logCard.append(el('h3', { text: '属性变动日志' }));
    if (state.log.length === 0) logCard.append(el('div', { class: 'empty', text: '暂无变动记录' }));
    for (const e of state.log) {
      logCard.append(el('div', { class: 'log-item' }, [
        el('span', { text: `${new Date(e.time * 1000).toLocaleString('zh-CN')} ` }),
        el('b', { text: `${e.uname} 送出 ${e.giftName} ` }),
        el('span', { text: `${e.attributeName} ${e.delta > 0 ? '+' : ''}${e.delta} → ${e.valueAfter}` }),
      ]));
    }
    content.append(logCard);
  }

  function renderSettings(): void {
    content.append(el('div', { class: 'section-title', text: '设置' }));
    const card = el('div', { class: 'card' });
    const fontSize = inputField('字体大小（px）', String(state.settings.fontSize));
    fontSize.oninput = () => { state.settings.fontSize = Number(fontSize.value) || 48; save(); };
    const accent = inputField('强调色（十六进制）', state.settings.accentColor);
    accent.oninput = () => { state.settings.accentColor = accent.value; save(); };
    const align = el('select', { class: 'field-input' }) as HTMLSelectElement;
    align.innerHTML = `<option value="left">左对齐</option><option value="center">居中</option><option value="right">右对齐</option>`;
    align.value = state.settings.align;
    align.onchange = () => { state.settings.align = align.value as any; save(); };
    const showStats = el('input', { type: 'checkbox' }) as HTMLInputElement;
    showStats.checked = state.settings.showStats;
    showStats.onchange = () => { state.settings.showStats = showStats.checked; save(); };
    const showConn = el('input', { type: 'checkbox' }) as HTMLInputElement;
    showConn.checked = state.settings.showConnection;
    showConn.onchange = () => { state.settings.showConnection = showConn.checked; save(); };
    card.append(
      fontSize,
      accent,
      el('div', { class: 'field', children: [el('span', { class: 'field-label', text: '对齐' }), align] }),
      el('div', { class: 'field', children: [el('label', { text: '显示今日统计' }), showStats] }),
      el('div', { class: 'field', children: [el('label', { text: '显示连接状态' }), showConn] }),
    );
    content.append(card);

    const dataCard = el('div', { class: 'card' });
    dataCard.append(el('h3', { text: '数据管理' }));
    const exportBtn = el('button', { class: 'btn', text: '导出配置' });
    exportBtn.onclick = () => {
      const blob = new Blob([JSON.stringify(state, null, 2)], { type: 'application/json' });
      const url = URL.createObjectURL(blob);
      const a = el('a', { href: url, download: `gift-panel-config-${new Date().toISOString().slice(0, 10)}.json` }) as HTMLAnchorElement;
      a.click();
      URL.revokeObjectURL(url);
    };
    const importInput = el('input', { type: 'file', accept: '.json' }) as HTMLInputElement;
    importInput.style.display = 'none';
    importInput.onchange = () => {
      const file = importInput.files?.[0];
      if (!file) return;
      file.text().then((text) => {
        try {
          const parsed = JSON.parse(text) as AppState;
          state = { ...state, ...parsed };
          save();
          render();
          toast('配置已导入', root);
        } catch {
          toast('文件解析失败', root);
        }
      });
    };
    const importBtn = el('button', { class: 'btn ghost', text: '导入配置' });
    importBtn.onclick = () => importInput.click();
    const resetBtn = el('button', { class: 'btn danger', text: '恢复默认' });
    resetBtn.onclick = () => {
      if (confirm('确定恢复默认设置？当前配置将被清除。')) {
        localStorage.removeItem(STORAGE_KEY);
        location.reload();
      }
    };
    dataCard.append(exportBtn, importBtn, importInput, resetBtn);
    content.append(dataCard);
  }

  render();
}
```

- [ ] **Step 4: 在 config.css 末尾补充 overlay 样式**

```css
.overlay {
  position: fixed; inset: 0; background: rgba(0,0,0,.55);
  display: flex; align-items: flex-start; justify-content: center;
  padding: 40px 16px; z-index: 100; overflow: auto;
}
.overlay .card { width: 100%; max-width: 480px; }
```

- [ ] **Step 5: 运行 typecheck**

Run: `npm run typecheck`
Expected: 无错误（若 `children` 数组被 common.ts 类型拒收，调整为泛型 `(HTMLElement|string)[]`）

- [ ] **Step 6: 提交**

```bash
git add -A
git commit -m "feat: config panel UI"
```

---

### Task 9: 显示面板 UI（?mode=display）+ main.ts 接线

**Files:**
- Create: `src/ui/display/display.css`
- Create: `src/ui/display/display.ts`
- Create: `src/main.ts`
- Test: 手动验证（OBS 加载）

**Interfaces:**
- Consumes: `Engine`、`loadState/saveState`、`formatValue`、`gifts/catalog`（自动捕获）、`types`
- Produces: `mountDisplay(root: HTMLElement): void`；`main.ts` 按 URL 参数分流到 display/config

视觉要求（规格 8.0）：深色半透明毛玻璃卡片、渐变强调色、tabular-nums 数字、触发动画（淡入放大+光晕）、连接呼吸灯（绿/黄/红）。

- [ ] **Step 1: 创建 src/ui/display/display.css**

```css
* { box-sizing: border-box; margin: 0; padding: 0; }
html, body { width: 100%; height: 100%; overflow: hidden; background: transparent; }
#app { width: 100%; height: 100%; }

.panel {
  position: absolute; top: 20px; left: 20px;
  display: flex; flex-direction: column; gap: 12px;
  background: rgba(14, 15, 20, 0.55);
  backdrop-filter: blur(14px);
  -webkit-backdrop-filter: blur(14px);
  border: 1px solid rgba(255, 255, 255, 0.10);
  border-radius: 18px;
  padding: 18px 22px;
  box-shadow: 0 12px 40px rgba(0, 0, 0, 0.45);
  max-width: 480px;
  transition: all 0.3s ease;
}
.panel.center { left: 50%; transform: translateX(-50%); }
.panel.right { left: auto; right: 20px; }

.attr { display: flex; flex-direction: column; gap: 4px; }
.attr-name {
  font-size: 15px; font-weight: 600; letter-spacing: 0.06em;
  color: rgba(255, 255, 255, 0.75);
  text-transform: uppercase;
}
.attr-value {
  font-size: 44px; font-weight: 700; line-height: 1.15;
  font-variant-numeric: tabular-nums;
  background: linear-gradient(135deg, var(--accent), var(--accent2, #ffd166));
  -webkit-background-clip: text;
  background-clip: text;
  color: transparent;
  transition: transform 0.15s ease;
}
.attr.flash .attr-value { animation: flash 0.6s ease; }
@keyframes flash {
  0% { transform: scale(1.18); filter: brightness(1.4); }
  100% { transform: scale(1); filter: brightness(1); }
}

.stats { display: flex; gap: 16px; font-size: 13px; color: rgba(255,255,255,0.65); }
.stats span { display: flex; align-items: center; gap: 4px; }

.conn {
  position: absolute; top: 10px; right: 12px;
  width: 10px; height: 10px; border-radius: 50%;
  background: #4ade80;
  box-shadow: 0 0 8px #4ade80;
  animation: breathe 2s ease-in-out infinite;
}
.conn.connecting { background: #facc15; box-shadow: 0 0 8px #facc15; }
.conn.reconnecting { background: #facc15; box-shadow: 0 0 8px #facc15; animation-duration: 0.6s; }
.conn.error { background: #f87171; box-shadow: 0 0 8px #f87171; animation: none; }
@keyframes breathe { 0%,100% { opacity: 1; } 50% { opacity: 0.35; } }

.toast-wrap { position: fixed; left: 50%; bottom: 60px; transform: translateX(-50%); display: flex; flex-direction: column; gap: 8px; pointer-events: none; z-index: 10; }
.gift-toast {
  display: flex; align-items: center; gap: 10px;
  background: rgba(14, 15, 20, 0.85);
  border: 1px solid rgba(255,255,255,0.14);
  border-radius: 12px; padding: 10px 16px;
  backdrop-filter: blur(10px);
  animation: toastIn 0.4s ease, toastOut 0.4s ease 2.6s forwards;
  box-shadow: 0 8px 24px rgba(0,0,0,0.4);
}
.gift-toast img { width: 36px; height: 36px; object-fit: contain; }
.gift-toast .grow { flex: 1; }
.gift-toast .gt-name { font-weight: 600; color: #fff; }
.gift-toast .gt-delta { color: var(--accent); font-weight: 700; font-variant-numeric: tabular-nums; }
@keyframes toastIn { from { opacity: 0; transform: translateY(16px) scale(0.9); } to { opacity: 1; transform: none; } }
@keyframes toastOut { to { opacity: 0; transform: translateY(8px); } }
```

- [ ] **Step 2: 创建 src/ui/display/display.ts**

```ts
import { AppState } from '../../types';
import { loadState, saveState } from '../../storage';
import { el } from '../common';
import { Engine, TriggerResult } from '../../engine';
import { formatValue, todayStr } from '../../format';
import { ConnState } from '../../bilibili/client';

export function mountDisplay(root: HTMLElement): void {
  const state = loadState();
  const panel = el('div', { class: 'panel' });
  const attrEls = new Map<string, HTMLElement>();
  const toastWrap = el('div', { class: 'toast-wrap' });
  root.append(panel, toastWrap);

  function renderAttrs(): void {
    panel.replaceChildren();
    for (const attr of state.attributes) {
      const nameEl = el('div', { class: 'attr-name', text: attr.name });
      const valueEl = el('div', { class: 'attr-value' });
      const block = el('div', { class: 'attr' }, [nameEl, valueEl]);
      attrEls.set(attr.name, block);
      panel.append(block);
    }
    const conn = el('div', { class: 'conn' });
    panel.append(conn);
    updateAll();
  }

  function updateAll(): void {
    panel.style.setProperty('--accent', state.settings.accentColor);
    panel.style.setProperty('--accent2', state.settings.accentColor);
    panel.classList.toggle('center', state.settings.align === 'center');
    panel.classList.toggle('right', state.settings.align === 'right');
    for (const attr of state.attributes) {
      const block = attrEls.get(attr.name);
      if (!block) continue;
      const valueEl = block.querySelector('.attr-value') as HTMLElement;
      if (valueEl) {
        valueEl.style.fontSize = `${state.settings.fontSize}px`;
        valueEl.textContent = formatValue(attr.value, attr);
      }
    }
    const stats = panel.querySelector('.stats') as HTMLElement | null;
    if (state.settings.showStats) {
      if (!stats) {
        const s = el('div', { class: 'stats' });
        panel.append(s);
        updateStats(s);
      }
    } else if (stats) {
      stats.remove();
    }
  }

  function updateStats(statsEl: HTMLElement): void {
    const day = state.stats[todayStr()];
    const giftTotal = day ? Object.values(day.giftTotals).reduce((a, b) => a + b, 0) : 0;
    statsEl.replaceChildren(
      el('span', { text: `🎁 礼物 ${giftTotal}` }),
    );
  }

  const engine = new Engine(state, (r: TriggerResult) => {
    const block = attrEls.get(r.attributeName);
    if (block) {
      block.classList.remove('flash');
      void block.offsetWidth;
      block.classList.add('flash');
      setTimeout(() => block.classList.remove('flash'), 700);
    }
    const gi = state.recentGifts.find((g) => g.id === r.gift.giftId);
    const img = el('img') as HTMLImageElement;
    img.src = gi?.imgBasic || 'data:image/gif;base64,R0lGODlhAQABAIAAAAAAAP///yH5BAEAAAAALAAAAAABAAEAAAIBRAA7';
    const attr = state.attributes.find((a) => a.name === r.attributeName);
    const toast = el('div', { class: 'gift-toast' }, [
      img,
      el('div', { class: 'grow' }, [
        el('div', { class: 'gt-name', text: `${r.gift.uname} 送出 ${r.gift.giftName}` }),
        el('div', { class: 'gt-delta', text: attr ? `${r.attributeName} +${formatValue(r.delta, attr)}` : `+${r.delta}` }),
      ]),
    ]);
    toastWrap.append(toast);
    setTimeout(() => toast.remove(), 3200);
    saveState(state);
  });

  engine.setStateListener((s: ConnState) => {
    const conn = panel.querySelector('.conn') as HTMLElement | null;
    if (!conn) return;
    conn.className = 'conn';
    if (s === 'connected') conn.classList.add('connected');
    else if (s === 'connecting' || s === 'reconnecting') conn.classList.add('reconnecting');
    else conn.classList.add('error');
    if (!state.settings.showConnection) conn.style.display = 'none';
    else conn.style.display = '';
  });

  renderAttrs();
  engine.start();
  setInterval(() => {
    const statsEl = panel.querySelector('.stats') as HTMLElement | null;
    if (state.settings.showStats && statsEl) updateStats(statsEl);
  }, 30000);
}
```

- [ ] **Step 3: 创建 src/main.ts**

```ts
import './ui/config/config.css';
import './ui/display/display.css';
import { mountDisplay } from './ui/display/display';
import { mountConfig } from './ui/config/config';

const root = document.getElementById('app')!;
const params = new URLSearchParams(location.search);
const mode = params.get('mode') ?? 'display';
if (mode === 'config') {
  mountConfig(root);
} else {
  mountDisplay(root);
}
```

- [ ] **Step 4: 修正 config.css 的 root 选择器冲突**

`config.css` 中 `#app { display: flex }` 与 display 模式冲突。在 config.css 里改成 `.config-root` 作用域，或 main.ts 给 root 加类名。采用：main.ts 中 `if (mode === 'config') root.classList.add('config-root');`，并在 config.css 中将 `#app` 选择器改为 `.config-root`。

- [ ] **Step 5: 运行 typecheck**

Run: `npm run typecheck`
Expected: 无错误

- [ ] **Step 6: 构建并验证单文件产物**

Run: `npm run build`
Expected: `dist/index.html` 为单个 HTML 文件（含内联 JS/CSS），可双击用浏览器打开显示面板

- [ ] **Step 7: 提交**

```bash
git add -A
git commit -m "feat: display panel and main entry wiring"
```

---

### Task 10: 端到端验证与收尾

**Files:**
- Modify: `README.md`（创建）

**Interfaces:**
- Consumes: 全部模块

- [ ] **Step 1: 全量测试**

Run: `npm test`
Expected: 全部 PASS

- [ ] **Step 2: typecheck**

Run: `npm run typecheck`
Expected: 无错误

- [ ] **Step 3: 真实房间手动验证（核心验收）**

打开浏览器：`dist/index.html?mode=config`，填写主播房间号，点测试连接；用另一账号给该房间送一个已配规则的礼物，确认：
1. 配置面板出现"已连接"，该礼物出现在"最近收到"列表
2. `dist/index.html?mode=display` 属性值按公式累加，触发动画+toast 正常
3. OBS 中添加浏览器源加载 display 页，`file://` 路径下 localStorage 是否持久化（重启 OBS 后属性值仍在）

- [ ] **Step 3a（风险处置）：若 file:// 下 localStorage 不持久**

在 README 记录替代方案：在本地起一个静态服务（如 `npx serve dist` 或 `python -m http.server 8000 -d dist`），OBS 浏览器源加载 `http://localhost:8000/index.html?mode=display`。配置面板用同一地址 `?mode=config`（同源共享 localStorage）。

- [ ] **Step 4: 创建 README.md**

```markdown
# Bilibili 直播礼物面板

OBS 浏览器源插件：监听直播间礼物，按可配置公式规则累加属性（如加班时间），实时面板展示。

## 构建

```
npm install
npm run fetch:catalog   # 抓取最新礼物目录（可选，构建会自动抓）
npm run build
```

产物：`dist/index.html`（单文件，无运行时依赖）。

## 使用

- **显示面板（OBS）**：浏览器源加载 `dist/index.html?mode=display`
- **配置面板（浏览器）**：打开 `dist/index.html?mode=config`
  填写房间号、创建属性、配置礼物规则、导出/导入配置。
  两个模式共享同一 localStorage。

## 注意事项

- 使用 B 站非官方 WebSocket 弹幕协议，仅供个人/私下使用，请勿公开传播
- 匿名接入，无需登录 B 站账号
- 若 OBS 的 file:// 下 localStorage 不持久，用本地静态服务加载（见下文）

## 本地静态服务（可选）

```
python -m http.server 8000 -d dist
```

OBS 加载 `http://localhost:8000/index.html?mode=display`。

## 技术栈

Vite + TypeScript + Vitest，运行时零第三方依赖（WebSocket / DecompressionStream / localStorage 均为浏览器内置）。
```

- [ ] **Step 5: 最终提交**

```bash
git add -A
git commit -m "docs: add README"
```

- [ ] **Step 6: 交付总结**

汇总：单文件产物路径、使用方式、已知风险（file:// localStorage、协议变动、合规提醒）。
