# 配置向导视觉重排与品牌图标实施计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 将配置页改成面向小白的单主流程向导，精简重复说明，并为页面与 Go EXE 增加统一的礼物盒+时钟品牌图标。

**Architecture:** 保留现有 `?mode=config`、`?mode=display`、localStorage 数据和规则引擎。配置页新增纯函数向导状态模块，由 vanilla TypeScript 渲染顶部品牌栏、四步进度和当前步骤；统计、设置、手动添加等低频功能收进次级入口。页面使用内联 SVG 品牌图标，Go EXE 使用同图形生成的 PNG/Windows 资源文件，不增加主播端运行时依赖。

**Tech Stack:** Vite + TypeScript + vanilla DOM/CSS；Go `embed` 静态页面；`go-winres` Windows 资源生成；Vitest。

## Global Constraints

- 不改变现有 `?mode=config` 和 `?mode=display` URL。
- 不改变 localStorage 数据结构、公式语法、礼物规则行为和 Go 代理 API。
- 首次使用向导一次只突出一个当前任务，详细说明默认收起。
- 房间号说明必须使用：URL 中 `live.bilibili.com/` 后、查询参数 `?` 前的路径数字；示例使用 `88888888`。
- 页面主色使用 `#fb7299`，礼物/时间高光使用 `#ffad66`，成功色使用 `#4ade80`。
- 页面 logo、favicon、空状态图标和 EXE 图标使用同一“礼物盒 + 小时钟”图形，不以 emoji 作为主要品牌标识。
- Go EXE 继续内嵌单 HTML，主播无需安装 Node、Python、Go 或 Bun。
- 每个任务完成后运行任务内指定测试，并提交独立 commit。

## File Map

- Create: `src/ui/config/wizard.ts` — 向导步骤和完成状态的纯函数。
- Create: `tests/wizard.test.ts` — 向导状态与房间号说明测试。
- Create: `src/ui/brand.ts` — SVG 品牌图形和 favicon 安装函数。
- Modify: `src/main.ts` — 启动时安装 favicon，并按模式挂载页面。
- Modify: `src/ui/config/config.ts` — 使用向导 shell、步骤页面、折叠说明和完成状态。
- Modify: `src/ui/config/config.css` — 新布局、进度条、卡片、折叠说明和移动端样式。
- Create: `assets/brand.svg` — canonical SVG 设计源文件。
- Create: `assets/brand.png` — 由 canonical SVG 导出的 Windows 图标源图。
- Create: `goserver/winres/winres.json` — EXE 图标、版本信息和 DPI manifest 配置。
- Generate: `goserver/rsrc_windows_amd64.syso` — `go-winres make` 生成的 Windows 资源对象。
- Modify: `scripts/build-go.mjs` — 生成/检查 Windows 资源后再构建 Go EXE。
- Modify: `README.md` — 仅保留开发者构建说明，主播使用说明以应用内向导为准。

---

### Task 1: 提取向导状态模型并补测试

**Files:**
- Create: `src/ui/config/wizard.ts`
- Create: `tests/wizard.test.ts`
- Test: `tests/wizard.test.ts`

**Interfaces:**
- Consumes: `AppState` from `src/types.ts`。
- Produces: `WizardStep`, `WizardProgress`, `getWizardProgress()`, `getNextWizardStep()` 和 `getRoomNumberHint()`，供配置页和测试使用。

- [ ] **Step 1: Write the failing tests**

```ts
import { describe, expect, it } from 'vitest';
import { getNextWizardStep, getRoomNumberHint, getWizardProgress } from '../src/ui/config/wizard';

const state = (roomId = '', rules = 0) => ({
  roomId,
  attributes: [{ name: '加班时间', value: 0, unit: 'seconds', format: 'hhmmss', decimals: 0, suffix: '' }],
  rules: Array.from({ length: rules }, (_, i) => ({
    id: `r-${i}`, giftId: 1, attributeName: '加班时间', formula: '60',
  })),
} as any);

describe('wizard progress', () => {
  it('starts at room setup', () => {
    expect(getWizardProgress(state())).toEqual({ room: false, attributes: true, rules: false, obs: false });
    expect(getNextWizardStep(getWizardProgress(state()))).toBe('room');
  });

  it('moves to rules after a room is configured', () => {
    const progress = getWizardProgress(state('24849407'));
    expect(progress.room).toBe(true);
    expect(getNextWizardStep(progress)).toBe('rules');
  });

  it('is ready for OBS after a rule exists', () => {
    const progress = getWizardProgress(state('24849407', 1));
    expect(progress).toEqual({ room: true, attributes: true, rules: true, obs: true });
    expect(getNextWizardStep(progress)).toBeNull();
  });
});

describe('room number hint', () => {
  it('extracts the path segment before query parameters', () => {
    expect(getRoomNumberHint('https://live.bilibili.com/88888888?live_from=1111&visit_id=x')).toEqual({
      path: '88888888',
      query: '?live_from=1111&visit_id=x',
    });
  });
});
```

- [ ] **Step 2: Run the focused test and verify it fails**

Run: `npx vitest run tests/wizard.test.ts`

Expected: FAIL because `src/ui/config/wizard.ts` does not exist.

- [ ] **Step 3: Implement the minimal state module**

```ts
import type { AppState } from '../../types';

export type WizardStep = 'room' | 'attributes' | 'rules' | 'obs';

export interface WizardProgress {
  room: boolean;
  attributes: boolean;
  rules: boolean;
  obs: boolean;
}

export function getWizardProgress(state: Pick<AppState, 'roomId' | 'attributes' | 'rules'>): WizardProgress {
  const room = state.roomId.trim().length > 0;
  const attributes = state.attributes.length > 0;
  const rules = state.rules.length > 0;
  return { room, attributes, rules, obs: room && attributes && rules };
}

export function getNextWizardStep(progress: WizardProgress): WizardStep | null {
  if (!progress.room) return 'room';
  if (!progress.attributes) return 'attributes';
  if (!progress.rules) return 'rules';
  return null;
}

export function getRoomNumberHint(rawUrl: string): { path: string; query: string } | null {
  try {
    const url = new URL(rawUrl);
    const match = url.pathname.match(/\/([^/]+)\/?$/);
    if (!match || !/^\d+$/.test(match[1])) return null;
    return { path: match[1], query: url.search };
  } catch {
    return null;
  }
}
```

- [ ] **Step 4: Run focused and existing tests**

Run: `npx vitest run tests/wizard.test.ts tests/storage.test.ts`

Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add src/ui/config/wizard.ts tests/wizard.test.ts
git commit -m "feat: add testable configuration wizard state"
```

### Task 2: Add the shared brand mark and favicon

**Files:**
- Create: `assets/brand.svg`
- Create: `src/ui/brand.ts`
- Modify: `src/main.ts`
- Test: `npm run typecheck`

**Interfaces:**
- Produces: `createBrandIcon(size, className)`, `installFavicon()` and `BRAND_SVG`.
- Consumers: configuration header, display empty state, and application bootstrap.

- [ ] **Step 1: Add the canonical SVG asset**

Create `assets/brand.svg` with a transparent 64x64 canvas. Use a rounded pink gift box, an orange clock face overlapping its upper-right corner, and no text. The visual primitives must be equivalent to:

```svg
<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 64 64">
  <defs>
    <linearGradient id="gift" x1="8" y1="8" x2="54" y2="58" gradientUnits="userSpaceOnUse">
      <stop stop-color="#fb7299"/><stop offset="1" stop-color="#e95683"/>
    </linearGradient>
  </defs>
  <rect x="8" y="22" width="39" height="34" rx="7" fill="url(#gift)"/>
  <path d="M8 24h39M27.5 22v34" stroke="#fff" stroke-opacity=".8" stroke-width="4"/>
  <path d="M27 22c-9 0-14-3-13-8 1-5 9-4 13 8Zm1 0c0-12 7-16 11-12 4 4-1 9-11 12Z" fill="#ffad66"/>
  <circle cx="47" cy="17" r="12" fill="#ffad66" stroke="#171923" stroke-width="3"/>
  <path d="M47 10v8l5 3" fill="none" stroke="#171923" stroke-linecap="round" stroke-width="2.5"/>
</svg>
```

- [ ] **Step 2: Implement DOM icon creation and favicon installation**

`src/ui/brand.ts` must import the canonical SVG source and construct a real SVG element without an external runtime dependency. Do not duplicate the SVG markup in TypeScript:

```ts
import brandSvg from '../../assets/brand.svg?raw';

export const BRAND_SVG = brandSvg;

export function createBrandIcon(size = 40, className = 'brand-icon'): SVGSVGElement {
  const template = document.createElement('template');
  template.innerHTML = BRAND_SVG.trim();
  const icon = template.content.firstElementChild as SVGSVGElement;
  icon.setAttribute('width', String(size));
  icon.setAttribute('height', String(size));
  icon.setAttribute('class', className);
  icon.setAttribute('aria-hidden', 'true');
  return icon;
}

export function installFavicon(): void {
  if (document.getElementById('blive-brand-favicon')) return;
  const link = document.createElement('link');
  link.id = 'blive-brand-favicon';
  link.rel = 'icon';
  link.type = 'image/svg+xml';
  link.href = `data:image/svg+xml,${encodeURIComponent(BRAND_SVG)}`;
  document.head.append(link);
}
```

- [ ] **Step 3: Install branding at app bootstrap**

Call `installFavicon()` once at the start of `src/main.ts`, before mode-specific mounting. The function must be idempotent if `main.ts` is evaluated more than once in a development reload.

- [ ] **Step 4: Run typecheck and build**

Run: `npm run typecheck && npm run build`

Expected: PASS, and `dist/index.html` contains the inlined application with no external brand asset request.

- [ ] **Step 5: Commit**

```bash
git add assets/brand.svg src/ui/brand.ts src/main.ts dist/index.html
git commit -m "feat: add shared gift timer branding"
```

### Task 3: Replace the config shell with the wizard layout

**Files:**
- Modify: `src/ui/config/config.ts:10-146`
- Modify: `src/ui/config/config.css:19-151`
- Test: `tests/wizard.test.ts`

**Interfaces:**
- Consumes: `getWizardProgress`, `getNextWizardStep`, `WizardStep`, `createBrandIcon`.
- Produces: `renderWizardHeader()`, `renderProgress()`, and a config shell with one focused content area.

- [ ] **Step 1: Add a shell-state regression test**

Extend `tests/wizard.test.ts` with:

```ts
it('uses the configured room as the first active task after room setup', () => {
  const progress = getWizardProgress(state('88888888'));
  expect(getNextWizardStep(progress)).toBe('rules');
});
```

Run: `npx vitest run tests/wizard.test.ts`

Expected: PASS; this test records the approved example and prevents regressions in the next-task logic.

- [ ] **Step 2: Implement the new shell**

In `mountConfig`, replace the wide sidebar-first shell with:

```ts
const shell = el('div', { class: 'wizard-shell' });
const header = el('header', { class: 'app-header' });
const brand = el('div', { class: 'app-brand' });
brand.append(createBrandIcon(40), el('div', { class: 'app-brand-copy' }, [
  el('strong', { text: '直播礼物面板' }),
  el('span', { text: '简单三步，开始互动' }),
]));
const status = el('div', { class: 'app-status' });
header.append(brand, status);

const content = el('main', { class: 'wizard-content' });
shell.append(header, content);
root.replaceChildren(shell);
```

Keep `current`, `state`, `client`, and `switchTo()` in the closure. `switchTo()` must stop the existing `DanmakuClient`, update the current step, and re-render without changing stored data.

- [ ] **Step 3: Render the four-step progress header**

Use the exact step labels `连接房间`, `设置属性`, `绑定礼物`, `放进 OBS`. Each item must have `data-step`, a numeric circle, and classes `is-active`/`is-done`. Clicking a step calls `switchTo()`; it must not duplicate explanatory copy.

- [ ] **Step 4: Add focused CSS and remove conflicting sidebar styles**

Implement these layout rules in `config.css`:

```css
.config-root { min-height:100vh; background:radial-gradient(circle at 80% 0%,rgba(251,114,153,.12),transparent 32%),linear-gradient(135deg,#12131a,#1b1d28); }
.wizard-shell { width:min(100% - 48px, 960px); margin:0 auto; padding:24px 0 48px; }
.app-header { display:flex; align-items:center; justify-content:space-between; gap:16px; margin-bottom:28px; }
.app-brand { display:flex; align-items:center; gap:11px; }
.app-brand-copy strong, .app-brand-copy span { display:block; }
.app-brand-copy strong { font-size:16px; }
.app-brand-copy span { margin-top:3px; color:var(--text-dim); font-size:12px; }
.wizard-content { min-width:0; }
.wizard-progress { display:grid; grid-template-columns:repeat(4,1fr); gap:9px; margin-bottom:34px; }
.wizard-progress-item { border-bottom:2px solid var(--border); padding:10px 12px; color:var(--text-dim); cursor:pointer; }
.wizard-progress-item.is-active { border-color:var(--accent); color:var(--text); }
.wizard-progress-item.is-done { border-color:#4ade80; color:#bff4ca; }
.wizard-main-title { margin-bottom:9px; font-size:30px; letter-spacing:-.04em; }
.wizard-subtitle { margin-bottom:24px; color:var(--text-dim); font-size:13px; }
```

- [ ] **Step 5: Run UI build and existing tests**

Run: `npm test && npm run typecheck && npm run build`

Expected: PASS and no config CSS selector applies the old 200px sidebar layout.

- [ ] **Step 6: Commit**

```bash
git add src/ui/config/config.ts src/ui/config/config.css tests/wizard.test.ts
git commit -m "feat: make configuration page wizard-first"
```

### Task 4: Implement concise step content and advanced sections

**Files:**
- Modify: `src/ui/config/config.ts`
- Modify: `src/ui/config/config.css`
- Test: `tests/wizard.test.ts`

**Interfaces:**
- Consumes: the wizard shell from Task 3 and existing storage/rule functions.
- Produces: room, attributes, rules, OBS-completion content with no duplicate permanent descriptions.

- [ ] **Step 1: Replace the room hint copy**

The only always-visible room copy is:

```ts
el('h1', { class: 'wizard-main-title', text: '输入你的直播间房间号' });
el('p', { class: 'wizard-subtitle', text: '填好后点击测试连接。' });
```

The expandable help must render this exact semantic content, using the user-approved example:

```ts
const roomHelp = el('details', { class: 'details-card' });
roomHelp.append(
  el('summary', { text: '房间号在哪里？' }),
  el('p', { text: '看地址中 live.bilibili.com/ 后面的数字，不要复制问号后的访问参数。' }),
  el('code', { text: 'https://live.bilibili.com/88888888?live_from=1111&visit_id=abc123' }),
  el('p', { text: '要填写：88888888' }),
);
```

Do not say “最后的数字”。

- [ ] **Step 2: Make attributes beginner-safe**

Show the default `加班时间` card and only the fields `名称`, `初始值`, and `显示格式` in the main card. Put unit/advanced controls behind `details` titled `更多属性设置`. Keep the existing update and delete behavior unchanged.

- [ ] **Step 3: Make rules a four-action flow**

Render the rules page in this order:

```ts
const actions = ['搜索礼物', '选择属性', '选择公式示例', '保存规则'];
```

The gift search remains the first interactive control. The formula tutorial is closed by default and uses the existing examples; the optional `minPrice`, `cap`, and `dailyLimit` fields move into a closed `可选限制` details block. The no-rule empty state is exactly one sentence: `先搜索一个观众会送的礼物。`

- [ ] **Step 4: Add the OBS completion card**

When `getWizardProgress(state).obs` is true, render a compact completion card with:

```ts
const obsUrl = `${location.origin}/?mode=display`;
```

The card contains a readonly URL, a `复制地址` button, and at most three short lines describing OBS browser source setup. Keep the existing clipboard fallback for browsers that deny `navigator.clipboard`.

- [ ] **Step 5: Move low-frequency features behind a secondary entry**

Add a compact `更多设置` control that opens the existing stats/settings/manual-add views. These views must remain reachable and preserve their current behavior, but they must not be visible in the first-run main flow.

- [ ] **Step 6: Add responsive behavior**

Add:

```css
@media (max-width: 700px) {
  .wizard-shell { width:min(100% - 28px, 960px); padding-top:16px; }
  .wizard-progress { grid-template-columns:repeat(2,1fr); }
  .wizard-main-title { font-size:25px; }
  .wizard-workspace { grid-template-columns:1fr; }
  .input-row, .ready-url { flex-direction:column; }
}
```

- [ ] **Step 7: Run tests, typecheck, build, and screenshot smoke check**

Run:

```bash
npm test
npm run typecheck
npm run build
npx playwright screenshot --device="Desktop Chrome" --wait-for-timeout=1000 "http://localhost:12450/?mode=config" .tmp/config-wizard.png
```

Expected: all tests pass, the build produces `dist/gift-panel.exe`, and the screenshot shows one active task with no repeated room-number paragraphs. For the room hint, verify visually that `88888888` is highlighted before `?`.

- [ ] **Step 8: Commit**

```bash
git add src/ui/config/config.ts src/ui/config/config.css
git commit -m "feat: simplify first-run configuration guidance"
```

### Task 5: Generate and embed the Windows EXE icon

**Files:**
- Create: `assets/brand.png` from `assets/brand.svg` at 256x256 with transparent background.
- Create: `goserver/winres/winres.json`
- Generate: `goserver/rsrc_windows_amd64.syso`
- Modify: `scripts/build-go.mjs`
- Modify: `goserver/go.mod` only if the resource tool needs a pinned module version.

**Interfaces:**
- Consumes: `assets/brand.png` and the existing Go server.
- Produces: Windows resource object automatically embedded by `go build`.

- [ ] **Step 1: Rasterize the canonical SVG and install the resource generator**

Use the existing Chromium installation to rasterize the same SVG with a transparent background:

```bash
npx playwright screenshot --omit-background --viewport-size="256,256" "file:///E:/bilibili/assets/brand.svg" "E:/bilibili/assets/brand.png"
```

The result must be a 256x256 PNG with transparency; do not redraw a different logo for the EXE.

Run once on the build machine:

```bash
go install github.com/tc-hib/go-winres@latest
```

Create `goserver/winres/winres.json` with an icon resource and GUI manifest:

```json
{
  "RT_GROUP_ICON": {
    "APP": {
      "0000": "../../assets/brand.png"
    }
  },
  "RT_MANIFEST": {
    "#1": {
      "0409": {
        "description": "Bilibili Live Gift Panel",
        "minimum-os": "win10",
        "execution-level": "as invoker",
        "dpi-awareness": "per monitor v2",
        "use-common-controls-v6": true
      }
    }
  }
}
```

- [ ] **Step 2: Generate the Go resource object**

Run from `goserver`:

```bash
go-winres make
```

Expected: `goserver/rsrc_windows_amd64.syso` exists and uses `assets/brand.png` for the application icon.

- [ ] **Step 3: Make the build script validate and include the resource**

Before `go build`, `scripts/build-go.mjs` must check for `goserver/rsrc_windows_amd64.syso` and fail with this actionable message if absent:

```text
Windows icon resource is missing. Run `go install github.com/tc-hib/go-winres@latest` and `go-winres make` in goserver.
```

Do not silently produce an unbranded EXE after the branding task is complete.

- [ ] **Step 4: Build and inspect the EXE**

Run from the repository root:

```bash
npm run build
```

Then, with `goserver` as the working directory, run `go test ./...`.

Expected: `dist/gift-panel.exe` builds, is still self-contained, and Windows file properties/taskbar show the gift+clock icon and application name.

- [ ] **Step 5: Commit**

```bash
git add assets/brand.png goserver/winres goserver/rsrc_windows_amd64.syso scripts/build-go.mjs
git commit -m "feat: embed gift timer icon in Windows executable"
```

### Task 6: Final documentation and verification

**Files:**
- Modify: `README.md`
- Modify: `.gitignore` to ignore `.superpowers/` and generated `goserver/dist/` if not already covered.

- [ ] **Step 1: Keep README developer-focused**

Replace long主播 instructions with a short note that the EXE opens the in-app guide automatically. Keep exact developer commands:

```bash
npm install
npm run build
npm test
npm run typecheck
```

Then run `go test ./...` with `goserver` as the working directory.

- [ ] **Step 2: Verify generated visual artifacts are not accidentally committed**

Run: `git status --short`

Expected: no `.superpowers/` brainstorming screens or temporary screenshots staged for the product.

- [ ] **Step 3: Run the complete verification gate**

Run:

```bash
npm test
npm run typecheck
npm run build
```

Then run `go test ./...` with `goserver` as the working directory.

Expected: all commands exit with code 0; the final `dist/gift-panel.exe` exists and is a single-file Windows executable.

- [ ] **Step 4: Commit documentation and cleanup**

```bash
git add README.md .gitignore
git commit -m "docs: document wizard-first user experience"
```

## Final Review Checklist

- [ ] Spec coverage: all sections of `docs/superpowers/specs/2026-08-01-config-wizard-visual-design.md` map to Tasks 1-6.
- [ ] No stale phrase “最后的数字” remains in application copy.
- [ ] No repeated room-number explanation remains on the first screen.
- [ ] The `88888888` example is used consistently.
- [ ] Existing formula, gift, storage, and display tests remain green.
- [ ] `dist/gift-panel.exe` carries the same icon shown in the page header/favicon.
