# 公式结果赋值语义实施计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 将礼物规则公式的结果改为目标属性的新值，并更新规则引擎、公式编辑器、测试和迁移提醒。

**Architecture:** 保持 `GiftRule`、localStorage、日志和事件协议结构不变，只改变 `src/engine/rules.ts` 的赋值计算：公式结果先经过封顶得到 `valueAfter`，再用 `valueAfter - before` 生成日志 delta。配置页同步把公式说明、预览和示例改成赋值语义；已有规则不自动转换，页面明确提醒复核。

**Tech Stack:** TypeScript + vanilla DOM；Vitest；Vite single-file build。

## Global Constraints

- 公式计算结果直接写入目标属性，不再执行 `before + formulaResult`。
- 目标用例公式必须是 `IF(早播次数<10,早播次数+1,早播次数*2)`。
- `TriggerResult.delta` 和 `LogEntry.delta` 继续表示 `valueAfter - before`。
- 封顶对公式结果执行 `min(nextValue, cap)`；不再把封顶解释为可增加的剩余空间。
- 最低价格、当日限次、礼物去重、统计和日志结构保持不变。
- 不增加“增加/赋值”切换控件；已有规则直接按新赋值语义解释。
- 不改变 `GiftRule` JSON 字段、localStorage key、B站协议、礼物匹配和 `?mode=config`/`?mode=display`。
- 用户可见文案必须说明公式结果会直接成为属性新值，需要累加时在公式中引用属性名。

## File Map

- Modify: `src/engine/rules.ts` — 公式结果到属性目标值的转换。
- Modify: `tests/engine.test.ts` — 赋值、翻倍、封顶、日志和限制条件测试。
- Modify: `src/ui/config/config.ts` — 赋值语义文案、预览和公式示例。
- Modify: `tests/wizard.test.ts` — 公式编辑器赋值提示回归测试。
- Modify: `README.md` — 已有规则需要复核的迁移提醒。

---

### Task 1: 将规则引擎改为目标值赋值

**Files:**
- Modify: `src/engine/rules.ts:14-55`
- Modify: `tests/engine.test.ts:34-131`
- Test: `tests/engine.test.ts`

**Interfaces:**
- Consumes: `evalFormula(formula, env)`、`GiftRule.cap`、`AppState.attributes`。
- Produces: `TriggerResult.valueAfter` 和 `TriggerResult.delta`，其中 `delta = valueAfter - before`；不改变返回类型字段。

- [ ] **Step 1: Replace delta-oriented tests with failing assignment expectations**

Update the first rule test to assert direct assignment:

```ts
it('assigns formula result as the attribute value', () => {
  const s = defaultState();
  s.attributes[0].value = 100;
  s.rules.push({ id: 'r1', giftId: 30607, attributeName: '加班时间', formula: 'price/1000*60' });
  const rs = applyGiftToState(s, makeGift({ price: 1000 }));
  expect(rs[0].delta).toBe(-40);
  expect(rs[0].valueAfter).toBe(60);
  expect(s.attributes[0].value).toBe(60);
});
```

Add the target use case:

```ts
it('supports conditional increment then doubling through the current value', () => {
  const s = defaultState();
  s.attributes[0] = { name: '早播次数', value: 9, unit: 'none', format: 'number', decimals: 0, suffix: '' };
  s.rules.push({ id: 'r1', giftId: 32251, attributeName: '早播次数', formula: 'IF(早播次数<10,早播次数+1,早播次数*2)' });
  const first = applyGiftToState(s, makeGift({ giftId: 32251 }));
  expect(first[0].valueAfter).toBe(10);
  s.attributes[0].value = 10;
  const second = applyGiftToState(s, makeGift({ giftId: 32251, rnd: 'second' }));
  expect(second[0].valueAfter).toBe(20);
});
```

Change the cap test to prove the cap applies to the assigned result:

```ts
it('clamps the assigned result to cap', () => {
  const s = defaultState();
  s.attributes[0].value = 90;
  s.rules.push({ id: 'r1', giftId: 30607, attributeName: '加班时间', formula: '150', cap: 100 });
  const rs = applyGiftToState(s, makeGift({}));
  expect(rs[0].delta).toBe(10);
  expect(rs[0].valueAfter).toBe(100);
});
```

Change the “cap already reached” test to assert that assignment can still lower a value when the formula result is below the cap:

```ts
it('assigns a value below the cap even when the current value is at the cap', () => {
  const s = defaultState();
  s.attributes[0].value = 100;
  s.rules.push({ id: 'r1', giftId: 30607, attributeName: '加班时间', formula: '50', cap: 100 });
  const rs = applyGiftToState(s, makeGift({}));
  expect(rs[0].valueAfter).toBe(50);
  expect(s.attributes[0].value).toBe(50);
});
```

Update existing formula tests that use `formula: '10'` so their expected values represent direct assignment. Keep minPrice, dailyLimit, dedupe, log pruning, and trigger-count assertions intact, changing only expected attribute values/deltas where the old addition assumption appears.

- [ ] **Step 2: Run the engine tests and verify the old implementation fails**

Run: `npx vitest run tests/engine.test.ts`

Expected: FAIL in direct assignment, target use case, and updated cap expectations because the current implementation adds the formula result.

- [ ] **Step 3: Implement direct assignment in `applyGiftToState`**

Replace the current delta block:

```ts
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
```

with:

```ts
let nextValue: number;
try {
  nextValue = evalFormula(rule.formula, env);
} catch {
  continue;
}
if (!Number.isFinite(nextValue)) continue;
const before = attr.value;
const valueAfter = rule.cap === undefined ? nextValue : Math.min(nextValue, rule.cap);
if (!Number.isFinite(valueAfter)) continue;
const delta = valueAfter - before;
attr.value = valueAfter;
```

Keep the existing daily-limit check before evaluation, and keep the existing log/result construction after this block. `valueAfter` in the log and result must be `attr.value` after assignment.

- [ ] **Step 4: Run the focused engine tests**

Run: `npx vitest run tests/engine.test.ts`

Expected: all engine tests pass, including direct assignment, target use case, cap, daily limit, dedupe, and log delta assertions.

- [ ] **Step 5: Commit**

```bash
git add src/engine/rules.ts tests/engine.test.ts
git commit -m "feat: assign formula results to attributes"
```

### Task 2: Update formula editor semantics and examples

**Files:**
- Modify: `src/ui/config/config.ts:438-520`
- Modify: `tests/wizard.test.ts:259-286`
- Test: `tests/wizard.test.ts`

**Interfaces:**
- Consumes: existing `evalFormula`, `formatValue`, attribute variable environment, and rule editor.
- Produces: formula editor that previews the assigned target value and teaches current-value references.

- [ ] **Step 1: Add a failing UI regression assertion**

Extend the rule editor test with:

```ts
expect(textOf(root)).toContain('公式结果会直接成为属性的新值');
expect(textOf(root)).toContain('触发后');
```

Add a test after opening the editor that verifies the default formula examples contain the selected attribute name when an additive example is shown.

Run: `npx vitest run tests/wizard.test.ts`

Expected: FAIL because the current editor still labels the field as `公式`, previews only a generic result, and has old “加/增加” examples.

- [ ] **Step 2: Update editor labels and default formula**

Use these values:

```ts
const formulaInput = inputField('触发后属性值', 'price/1000*60');
formulaInput.classList.add('formula');
formulaInput.placeholder = '例如 早播次数+price/1000*60';
```

Immediately above the input, add a visible but compact hint:

```ts
el('div', {
  class: 'hint assignment-hint',
  text: '公式结果会直接成为属性的新值。需要累加时，请在公式中写出当前属性名，例如：早播次数+1。',
});
```

- [ ] **Step 3: Change preview wording and generate assignment-aware examples**

In `updatePreview`, keep the existing environment and formula validation, but render:

```ts
const before = target?.value ?? 0;
preview.append(
  el('div', { text: `当前值：${formatValue(before, target)} → 触发后：` }),
  el('span', { class: 'result', text: target ? formatValue(result, target) : String(result) }),
);
```

Use examples whose formulas are valid assignments:

```ts
const targetName = () => state.attributes[attrSelect.selectedIndex]?.name ?? '当前值';
const examples: [string, string][] = [
  ['当前值加 60 秒', `${targetName()}+price/1000*60`],
  ['当前值随机加 10~60 秒', `${targetName()}+RANDBETWEEN(10,60)`],
  ['满 100 元设置为当前值+5分钟', `IF(price>=100000,${targetName()}+300,${targetName()})`],
  ['按礼物数量累加，每个30秒', `${targetName()}+count*30`],
  ['1 元设置为对应积分', 'ROUND(price/1000)'],
];
```

The selected attribute name must be captured when rendering the editor so Chinese names are inserted into examples. Keep the existing click-to-fill behavior and formula error display.

Update tutorial labels:

- `当前值` means the selected attribute’s value before this gift.
- `公式结果` means the value after this gift.
- `加班时间按秒计算` remains only as a unit note, not as a delta instruction.

- [ ] **Step 4: Run the focused UI tests**

Run: `npx vitest run tests/wizard.test.ts`

Expected: all wizard tests pass and the new assignment hint/preview assertions are green.

- [ ] **Step 5: Commit**

```bash
git add src/ui/config/config.ts tests/wizard.test.ts
git commit -m "feat: teach assignment-based formula editing"
```

### Task 3: Add migration warning and update user-facing documentation

**Files:**
- Modify: `README.md`
- Modify: `src/ui/config/config.ts` if the warning is not already placed in Task 2.
- Test: `tests/wizard.test.ts`

**Interfaces:**
- Consumes: assignment hint from Task 2.
- Produces: explicit warning that saved formulas use the new global assignment semantics.

- [ ] **Step 1: Add a regression assertion for the migration warning**

In the rule editor test, assert:

```ts
expect(textOf(root)).toContain('已有规则需要复核');
```

Run: `npx vitest run tests/wizard.test.ts`

Expected: FAIL until the warning is rendered.

- [ ] **Step 2: Add the warning to the editor and README**

Render a compact warning in the formula tutorial or immediately below the assignment hint:

```ts
el('div', {
  class: 'hint warning',
  text: '本版本已改为赋值语义，之前保存的公式请重新检查。',
});
```

Add a matching README section:

```md
### 公式语义

公式结果现在会直接成为属性的新值。需要累加时，请把当前属性名写进公式，例如 `早播次数+1`。
升级到赋值语义后，之前保存的规则不会自动转换，请逐条打开并检查。
```

- [ ] **Step 3: Run focused tests and commit**

Run: `npx vitest run tests/wizard.test.ts`

Expected: PASS.

```bash
git add README.md src/ui/config/config.ts tests/wizard.test.ts
git commit -m "docs: warn about assignment formula migration"
```

### Task 4: Full verification and use-case validation

**Files:**
- Test: `tests/formula.test.ts`
- Test: `tests/engine.test.ts`
- Test: `tests/wizard.test.ts`

- [ ] **Step 1: Run the complete validation suite**

Run:

```bash
npm test
npm run typecheck
npm run build
```

Expected: all tests pass, typecheck exits 0, and `dist/index.html`/`dist/gift-panel.exe` are rebuilt.

- [ ] **Step 2: Validate the concrete room/gift formula**

Run a deterministic formula check with the project evaluator:

```bash
npx tsx -e "import { evalFormula } from './src/formula/index.ts'; for (const value of [0,9,10,20]) { const next=evalFormula('IF(早播次数<10,早播次数+1,早播次数*2)', {price:15000,count:1,早播次数:value}); console.log(value+' -> '+next); }"
```

Expected output:

```text
0 -> 1
9 -> 10
10 -> 20
20 -> 40
```

- [ ] **Step 3: Verify the gift is available in the snapshot**

Run:

```bash
node -e "const a=require('./src/data/gift-catalog.json'); console.log(a.find(g=>g.id===32251))"
```

Expected: an entry named `心动盲盒` with ID `32251`.

- [ ] **Step 4: Commit any test-only adjustments and report**

```bash
git status --short
git diff --check HEAD~3..HEAD
```

Expected: no whitespace errors; only intentional user-requested formula semantics files are changed by this plan.

## Final Review Checklist

- [ ] No `before + formulaResult` remains in rule application.
- [ ] Formula result is assigned directly and cap clamps the target value.
- [ ] `delta` equals new value minus old value in results and logs.
- [ ] The target use case produces 1, 10, 20, 40 for 0, 9, 10, 20.
- [ ] UI says “触发后” and explains current-value references.
- [ ] Existing-rule migration warning is visible in the editor and README.
- [ ] `npm test`, `npm run typecheck`, and `npm run build` pass.
