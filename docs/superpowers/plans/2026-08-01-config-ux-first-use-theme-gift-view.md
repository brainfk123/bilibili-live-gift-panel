# 配置页首次状态、主题与礼物视图实施计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 让四步向导只在配置未完成时出现，完成后切换为紧凑顶部导航，并为配置页增加亮色主题与礼物列表/网格视图。

**Architecture:** 在现有 `mountConfig` 内增加首次状态与正常状态两个渲染分支，不新增路由或页面。`AppState.settings` 增加带默认回退的 `theme` 和 `giftView` 字段；CSS 通过配置根节点主题标记切换颜色；礼物搜索结果继续复用去重、分页和规则编辑器，只改变结果容器的 list/grid 展示。

**Tech Stack:** TypeScript + vanilla DOM；CSS variables；Vitest；Vite single-file build。

## Global Constraints

- 空配置启动时显示向导；房间、属性、规则都存在后切换正常配置模式。
- 向导不作为永久页面 tab；`重新查看入门` 只临时打开说明，不清空配置、不改变完成状态。
- 亮色主题只作用于配置页，OBS display 模式不随配置页主题改变。
- 主题选项为 `dark` 和 `light`，默认 `dark`；礼物视图选项为 `list` 和 `grid`，默认 `list`。
- 新增 `settings.theme` 和 `settings.giftView` 必须兼容没有这两个字段的旧 localStorage 数据。
- 列表和网格共用搜索、去重、已配置状态、自动捕获、手动添加和每批 50 条加载更多逻辑。
- 不改变 `GiftRule`、现有 localStorage key、公式、礼物 ID 匹配、B站连接、OBS display URL。
- 本期不实现 OBS 按属性分组、friendly display name 或其数据字段。

## File Map

- Modify: `src/types.ts` — 为 `Settings` 增加主题和礼物视图联合类型字段。
- Modify: `src/storage.ts` — 为旧配置提供 `dark/list` 默认值。
- Modify: `src/ui/config/wizard.ts` — 暴露配置完成判断所需的稳定纯函数（如需要）。
- Modify: `src/ui/config/config.ts` — 首次/正常渲染分支、顶部导航、主题切换、列表/网格礼物渲染。
- Modify: `src/ui/config/config.css` — 正常顶部导航、亮色 CSS 变量和礼物网格布局。
- Modify: `tests/wizard.test.ts` — 首次状态、正常导航、主题默认值和礼物视图回归测试。
- Modify: `tests/storage.test.ts` — 旧 settings 缺字段时的默认值测试。

---

### Task 1: 增加设置默认值并拆分首次/正常配置模式

**Files:**
- Modify: `src/types.ts:54-60`
- Modify: `src/storage.ts:1-45`
- Modify: `src/ui/config/config.ts:12-230`
- Modify: `tests/storage.test.ts`
- Modify: `tests/wizard.test.ts`
- Test: `tests/storage.test.ts`, `tests/wizard.test.ts`

**Interfaces:**
- Produces: `Settings.theme: 'dark' | 'light'`、`Settings.giftView: 'list' | 'grid'`。
- Consumes: `getWizardProgress(state).obs` 作为完成条件；保留现有 `switchTo(key)` 和 DanmakuClient 生命周期。

- [ ] **Step 1: Add failing default/mode tests**

Add storage coverage:

```ts
it('defaults new UX settings for old saved data', () => {
  localStorage.setItem(STORAGE_KEY, JSON.stringify({
    roomId: '', attributes: [], rules: [], settings: { fontSize: 48, accentColor: '#fb7299', showStats: true, showConnection: true, align: 'center' },
  }));
  const loaded = loadState();
  expect(loaded.settings.theme).toBe('dark');
  expect(loaded.settings.giftView).toBe('list');
});
```

Add wizard DOM coverage:

```ts
it('shows onboarding without permanent top navigation for incomplete setup', () => {
  const root = new TestElement('div');
  mountConfig(root as unknown as HTMLElement);
  expect(root.querySelector('.wizard-progress')).not.toBeNull();
  expect(root.querySelector('.normal-nav')).toBeNull();
});

it('shows compact top navigation after setup is complete', () => {
  storage.set('bilibili-live-gift-panel-v1', JSON.stringify(state('88888888', 1)));
  const root = new TestElement('div');
  mountConfig(root as unknown as HTMLElement);
  expect(root.querySelector('.wizard-progress')).toBeNull();
  expect(root.querySelector('.normal-nav')).not.toBeNull();
  expect(root.querySelector('.completion-home')).not.toBeNull();
});
```

Run: `npx vitest run tests/storage.test.ts tests/wizard.test.ts`

Expected: FAIL because the new settings fields and normal-mode navigation do not yet exist.

- [ ] **Step 2: Add settings fields and safe migration defaults**

Extend `Settings`:

```ts
theme: 'dark' | 'light';
giftView: 'list' | 'grid';
```

Set defaults in `defaultState()`:

```ts
settings: {
  fontSize: 48,
  accentColor: '#fb7299',
  showStats: true,
  showConnection: true,
  align: 'center',
  theme: 'dark',
  giftView: 'list',
},
```

When loading persisted state, merge settings as `{ ...defaultState().settings, ...(parsed.settings ?? {}) }` so missing fields in old JSON use `dark/list`.

- [ ] **Step 3: Split config rendering by completion state**

Add closure state:

```ts
let showOnboarding = !getWizardProgress(state).obs;

function isSetupComplete(): boolean {
  return getWizardProgress(state).obs;
}
```

Change `render()` so it always renders the brand/status header and then chooses:

```ts
if (showOnboarding) {
  renderProgress();
  renderOnboarding();
} else {
  renderNormalNav();
  renderCurrentSection();
}
```

When `save()` makes `isSetupComplete()` true, set `showOnboarding = false` before re-rendering. When incomplete setup is entered, keep `showOnboarding = true` so the next step remains visible.

Implement `renderNormalNav()` with buttons for `room`, `attributes`, `rules`, `stats`, and `settings`. It must not render `.wizard-progress` or the old onboarding card. The existing `renderMoreSettings()` button can be removed from normal mode because its destinations are now in the top nav; its buttons must remain reachable through the normal nav.

Add a low-emphasis `重新查看入门` button in normal mode. Its handler only sets `showOnboarding = true` and calls `render()`; it must not modify `state`. The completion page's return action sets `showOnboarding = false` and returns to the normal home.

Do not call `client.stop()` from navigation; the existing connection-preservation behavior remains required.

- [ ] **Step 4: Run focused tests**

Run: `npx vitest run tests/storage.test.ts tests/wizard.test.ts`

Expected: PASS, including incomplete onboarding, completed normal navigation, and old-settings fallback.

- [ ] **Step 5: Commit**

```bash
git add src/types.ts src/storage.ts src/ui/config/config.ts tests/storage.test.ts tests/wizard.test.ts
git commit -m "feat: show onboarding only during first setup"
```

### Task 2: Add configuration-only dark/light themes

**Files:**
- Modify: `src/ui/config/config.ts`
- Modify: `src/ui/config/config.css`
- Modify: `tests/wizard.test.ts`
- Test: `tests/wizard.test.ts`

**Interfaces:**
- Consumes: `state.settings.theme` and the normal/onboarding render branches from Task 1.
- Produces: `applyConfigTheme(theme)` and a theme toggle that persists through `saveState`.

- [ ] **Step 1: Add failing theme tests**

```ts
it('uses dark theme by default and can persist light theme', () => {
  const root = new TestElement('div');
  mountConfig(root as unknown as HTMLElement);
  expect(root.dataset.theme).toBe('dark');

  const toggle = findByText(root, '亮色主题');
  (toggle?.onclick as (() => void) | null)?.();
  expect(root.dataset.theme).toBe('light');
  expect(JSON.parse(storage.get('bilibili-live-gift-panel-v1')!).settings.theme).toBe('light');
});
```

Run: `npx vitest run tests/wizard.test.ts`

Expected: FAIL because the config root has no theme marker or toggle.

- [ ] **Step 2: Apply theme at the config root**

Implement:

```ts
function applyConfigTheme(theme: 'dark' | 'light'): void {
  root.dataset.theme = theme;
}
```

Call it after loading state and after every theme change. Add one compact button in the brand/header area with text `亮色主题` in dark mode and `深色主题` in light mode. Its handler toggles `state.settings.theme`, calls `save()`, and rerenders only the shell/header as needed without changing the active section or stopping the client.

The same toggle must exist during onboarding; it is not an additional onboarding step.

- [ ] **Step 3: Add CSS variables for light mode**

Keep existing dark values as `:root`/default values and add:

```css
.config-root[data-theme="light"] {
  --bg: #f4f6fb;
  --bg-soft: #ffffff;
  --card: #ffffff;
  --border: #d9deea;
  --text: #242936;
  --text-dim: #697386;
  --input-bg: #f8f9fc;
  --shadow: 0 12px 30px rgba(47, 57, 80, .08);
}

.config-root[data-theme="light"] .card,
.config-root[data-theme="light"] .onboard,
.config-root[data-theme="light"] .details-card {
  box-shadow: var(--shadow);
}
```

Every config-page surface must use variables instead of hard-coded dark colors. Do not import config CSS into display mode or set `data-theme` on the shared document body.

- [ ] **Step 4: Run focused tests and commit**

Run: `npx vitest run tests/wizard.test.ts`

Expected: PASS; display tests and OBS styles remain unchanged.

```bash
git add src/ui/config/config.ts src/ui/config/config.css tests/wizard.test.ts
git commit -m "feat: add light theme for configuration page"
```

### Task 3: Add list/grid gift browsing

**Files:**
- Modify: `src/ui/config/config.ts:360-420`
- Modify: `src/ui/config/config.css`
- Modify: `tests/wizard.test.ts`
- Test: `tests/wizard.test.ts`

**Interfaces:**
- Consumes: `state.settings.giftView`, `allGifts`, `visibleGiftCount`, `openRuleEditor(giftId, giftName, giftImg)`.
- Produces: `.gift-view-toggle`, `.gift-list` and `.gift-grid` containers with identical click behavior.

- [ ] **Step 1: Add failing view-switch tests**

```ts
it('defaults to list view and switches to grid without losing search', () => {
  const root = new TestElement('div');
  mountConfig(root as unknown as HTMLElement);
  root.querySelector('[data-step="rules"]')?.onclick?.();

  expect(root.querySelector('.gift-list')).not.toBeNull();
  const search = root.querySelector('input') as TestElement;
  search.value = '心动';
  search.oninput?.();

  findByText(root, '网格')?.onclick?.();
  expect(root.querySelector('.gift-grid')).not.toBeNull();
  expect((root.querySelector('input') as TestElement).value).toBe('心动');
  expect(JSON.parse(storage.get('bilibili-live-gift-panel-v1')!).settings.giftView).toBe('grid');
});
```

Run: `npx vitest run tests/wizard.test.ts`

Expected: FAIL because only list rows exist and no persisted view switch exists.

- [ ] **Step 2: Add the toggle and persist view choice**

Near the gift search input, render two buttons:

```ts
const viewToggle = el('div', { class: 'gift-view-toggle' });
const listButton = el('button', { class: 'btn ghost', text: '列表', type: 'button' });
const gridButton = el('button', { class: 'btn ghost', text: '网格', type: 'button' });
```

The active button gets `is-active`. Clicking a button sets `state.settings.giftView`, calls `save()`, and rerenders the rules section while preserving `search.value`. If rerendering the whole section, pass the current filter into `renderRules(filter)` so the search is not lost.

- [ ] **Step 3: Render shared gift data in two layouts**

Extract the current row construction into a function that accepts a gift and returns an element. Both layouts must use the same click handler:

```ts
row.onclick = () => openRuleEditor(g.id, g.name, g.imgBasic);
```

List layout classes:

```ts
const container = el('div', { class: 'gift-list' });
```

Grid layout classes:

```ts
const container = el('div', { class: 'gift-grid' });
```

List content includes icon, name, price/coin type, ID, and configured badge. Grid content includes icon, name, price/coin type, and configured badge. Both append the existing `gift-load-more` button and `gift-count` status after the visible batch.

Do not change `matches`, `visibleGiftCount`, search reset, recent gift merge, or ID deduplication logic.

- [ ] **Step 4: Add responsive grid CSS**

```css
.gift-grid { display:grid; grid-template-columns:repeat(auto-fill,minmax(150px,1fr)); gap:12px; }
.gift-grid .list-item { display:flex; min-height:160px; flex-direction:column; align-items:center; justify-content:center; text-align:center; }
.gift-grid .gift-img { width:64px; height:64px; }
.gift-grid .grow { min-width:0; width:100%; }
.gift-grid .name { overflow:hidden; text-overflow:ellipsis; white-space:nowrap; }
.gift-view-toggle { display:flex; gap:8px; margin:10px 0; }
.gift-view-toggle .is-active { border-color:var(--accent); color:var(--accent); }
@media (max-width:700px) { .gift-grid { grid-template-columns:repeat(2,minmax(0,1fr)); } }
```

- [ ] **Step 5: Run focused tests and commit**

Run: `npx vitest run tests/wizard.test.ts`

Expected: PASS for default list, grid persistence, search preservation, and existing load-more behavior.

```bash
git add src/ui/config/config.ts src/ui/config/config.css tests/wizard.test.ts
git commit -m "feat: add list and grid gift views"
```

### Task 4: Full verification and UI smoke checks

**Files:**
- Test: `tests/storage.test.ts`
- Test: `tests/wizard.test.ts`

- [ ] **Step 1: Run full automated checks**

```bash
npm test
npm run typecheck
npm run build
```

Expected: all tests pass, typecheck exits 0, and `dist/gift-panel.exe` is rebuilt.

- [ ] **Step 2: Verify desktop and mobile config rendering**

Start the latest EXE and capture:

```bash
npx playwright screenshot --device="Desktop Chrome" --wait-for-timeout=800 "http://localhost:12450/?mode=config" .tmp/config-normal-desktop.png
npx playwright screenshot --viewport-size="390,844" --wait-for-timeout=800 "http://localhost:12450/?mode=config" .tmp/config-normal-mobile.png
```

Inspect both screenshots for: no four-step bar after complete setup, visible compact top nav, readable light theme, and no horizontal overflow in grid view.

- [ ] **Step 3: Verify backward-compatible settings defaults**

Run: `npx vitest run tests/storage.test.ts tests/wizard.test.ts`

Expected: old JSON without `theme`/`giftView` loads as `dark/list`.

- [ ] **Step 4: Commit only intentional files and record status**

```bash
git diff --check HEAD~3..HEAD
git status --short
```

Expected: no whitespace errors; no `.superpowers/`, screenshots, or unrelated user changes are staged.

## Final Review Checklist

- [ ] Incomplete setup renders onboarding; complete setup renders only normal top navigation.
- [ ] Reopening onboarding does not alter completion state or clear data.
- [ ] Theme persists and affects config only.
- [ ] List/grid share search, pagination, status, and rule editor behavior.
- [ ] Old settings safely default to dark/list.
- [ ] Existing connection remains alive across section switching.
- [ ] OBS grouping and friendly display name remain explicitly deferred.
