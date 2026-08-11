# Gift Clip Crop UI Polish Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 将礼物动画剪裁器改为直角、L 形角标、原生抓取/方向缩放光标和调整时三分线，并把成片剪裁区域任一边限制为最多 4096px。

**Architecture:** 4096px 下限/上限与默认选区继续封装在纯计算模块 `gift-clip-crop.ts`；DOM 活动状态、键盘和生命周期继续封装在 `gift-clip-crop-editor.ts`；视觉实现留在 `.config-root` 作用域内的 `config.css`。配置页仍只跨 `giftClipAnimationKey()` / `openGiftClipStudio()` seam，`gift-clip-studio.ts` 不承接新逻辑。

**Tech Stack:** TypeScript 5.5、DOM Pointer Events、CSS、Vitest 2.1、Playwright/Chromium、Vite 5.4、Go、PowerShell。

## Global Constraints

- 已确认规格：`docs/superpowers/specs/2026-08-11-gift-clip-crop-ui-polish-design.md`。
- 动画预览面板、媒体层、裁剪视口、遮罩和裁剪框必须为直角；外层弹窗和信息条沿用现有圆角语义。
- 裁剪框采用方案 B：L 形角标、短条边标、桌面约 28px/窄屏约 32px 隐形热区，不显示圆形节点。
- 框内使用原生 `grab` / `grabbing`；四边和四角使用匹配方向的原生 resize 光标。
- 三分辅助线只在指针或键盘实际调整期间显示；`prefers-reduced-motion` 下取消过渡。
- 剪裁宽高分别限制在 `64..4096px` 且不得超出素材；大素材的完整默认值和重置值使用居中最大选区。
- `GiftClipCrop` 归一化持久化格式、`GiftClipCropEditor` 接口、`GiftClipStudioOptions`、确认回调和保存时机保持不变。
- 不为 4096px 常量、drag state 或视觉配置新增公开接口；测试通过既有函数和 DOM 可观察结果验证行为。
- `gift-clip-studio.ts` 原则上不修改；不得把几何、视觉或事件状态回流到该文件。
- 每个实现任务必须遵循 RED → GREEN → REFACTOR，并独立提交。
- 合并时保留 `E:\bilibili` 的全部未跟踪文件；不 push、不 release、不启动 EXE。

## File Structure

- Modify `src/ui/config/gift-clip-crop.ts`: 内部拥有 4096px 上限、像素区域约束、居中完整默认值和 8 向最大尺寸约束。
- Modify `src/ui/config/gift-clip-crop-editor.ts`: 创建辅助线 DOM，管理 pointer/keyboard 活动态，并在 reset/destroy 时统一清理。
- Modify `src/ui/config/config.css`: 直角动画面板、L 形角标、边标、热区、光标、三分线、焦点与 reduced-motion。
- Modify `tests/gift-clip-crop.test.ts`: 通过纯函数和现有 `GiftClipCropEditor` interface 覆盖几何上限与 DOM 生命周期；不扩张 studio 测试。
- Modify `scripts/verify-gift-clip-crop.mjs`: 通过计算样式、真实鼠标/键盘输入和截图验证视觉/交互结果。
- Do not modify `src/ui/config/gift-clip-studio.ts` or `tests/gift-clip-studio.test.ts` unless a failing stable-seam regression proves a minimal wiring correction is necessary.

---

### Task 1: 4096px 纯几何、居中默认值与重置

**Files:**
- Modify: `tests/gift-clip-crop.test.ts:1-70,264-414`
- Modify: `src/ui/config/gift-clip-crop.ts:1-137`
- Modify: `src/ui/config/gift-clip-crop-editor.ts:50-210`

**Interfaces:**
- Consumes: `defaultGiftClipCrop(): GiftClipCrop`、`giftClipCropToPixels()`、`giftClipCropFromPixels()`、`updateGiftClipCrop()`、`createGiftClipCropEditor()`。
- Produces: 现有函数在不改变类型签名的前提下保证每个剪裁维度 `<= 4096`；`GiftClipCropEditor.reset()` 返回素材相关的有界默认选区。

- [ ] **Step 1: 写出 4096px RED 几何测试**

在 `gift clip crop geometry` 中加入以下断言；不要导入最大尺寸常量：

```ts
it('centers the full default crop when either source dimension exceeds 4096px', () => {
  expect(giftClipCropToPixels(defaultGiftClipCrop(), 8192, 6000))
    .toEqual({ x: 2048, y: 952, width: 4096, height: 4096 });
  expect(giftClipCropToPixels(defaultGiftClipCrop(), 4096, 4096))
    .toEqual({ x: 0, y: 0, width: 4096, height: 4096 });
});

it('caps every resized axis at 4096px while keeping the opposite edge fixed', () => {
  const initial = giftClipCropFromPixels(
    { x: 1000, y: 1000, width: 4000, height: 4000 },
    8000,
    8000,
  );

  expect(giftClipCropToPixels(updateGiftClipCrop(initial, 'se', 1000, 1000, 8000, 8000), 8000, 8000))
    .toEqual({ x: 1000, y: 1000, width: 4096, height: 4096 });
  expect(giftClipCropToPixels(updateGiftClipCrop(initial, 'nw', -1000, -1000, 8000, 8000), 8000, 8000))
    .toEqual({ x: 904, y: 904, width: 4096, height: 4096 });
});
```

- [ ] **Step 2: 扩展 harness 并写出大素材 reset RED 测试**

把 harness 的素材尺寸改为可传入参数，并通过像素接口观察结果：

```ts
function createCropEditorHarness(
  initialCrop: GiftClipCrop = defaultGiftClipCrop(),
  sourceWidth = 640,
  sourceHeight = 360,
): CropEditorHarness {
  const stage = new CropTestElement('div');
  stage.clientWidth = 480;
  stage.clientHeight = 270;
  stage.rectWidth = 960;
  stage.rectHeight = 540;
  const changes: Array<{ crop: GiftClipCrop; pixels: GiftClipPixelRect }> = [];
  const editor = createGiftClipCropEditor({
    stage: stage as unknown as HTMLElement,
    sourceWidth,
    sourceHeight,
    initialCrop,
    receipt: cropReceiptFixture(),
    avatar: null,
    onChange: (crop, pixels) => { changes.push({ crop, pixels }); },
  });
  const layer = editor.element as unknown as CropTestElement;
  const frame = layer.querySelector('.gift-clip-crop-frame');
  if (!frame) throw new Error('crop frame was not created');
  const infoPreview = layer.querySelector('.gift-clip-crop-info-preview');
  if (!infoPreview) throw new Error('information preview was not created');
  return { editor, stage, layer, frame, infoPreview, changes };
}

it('resets an oversized source to its centered 4096px selection', () => {
  const initial = giftClipCropFromPixels({ x: 3000, y: 2000, width: 1000, height: 1000 }, 8192, 6000);
  const { editor, changes } = createCropEditorHarness(initial, 8192, 6000);

  editor.reset();

  expect(giftClipCropToPixels(editor.getCrop(), 8192, 6000))
    .toEqual({ x: 2048, y: 952, width: 4096, height: 4096 });
  expect(changes.at(-1)?.pixels)
    .toEqual({ x: 2048, y: 952, width: 4096, height: 4096 });
});
```

- [ ] **Step 3: 运行 RED 测试**

Run:

```powershell
npx vitest run tests/gift-clip-crop.test.ts
```

Expected: FAIL；完整默认值仍为 8192×6000，向外缩放仍可超过 4096px，reset 仍返回归一化完整素材。

- [ ] **Step 4: 在纯几何模块实现内部上限和居中完整值**

在 `gift-clip-crop.ts` 内部加入私有常量和私有 helper；不要 export 常量：

```ts
const MAX_GIFT_CLIP_CROP_SIZE = 4096;

function isFullGiftClipCrop(crop: GiftClipCrop): boolean {
  return crop.x === 0 && crop.y === 0 && crop.width === 1 && crop.height === 1;
}

function centeredFullPixelRect(sourceWidth: number, sourceHeight: number): GiftClipPixelRect {
  const width = Math.min(sourceWidth, MAX_GIFT_CLIP_CROP_SIZE);
  const height = Math.min(sourceHeight, MAX_GIFT_CLIP_CROP_SIZE);
  return {
    x: Math.floor((sourceWidth - width) / 2),
    y: Math.floor((sourceHeight - height) / 2),
    width,
    height,
  };
}
```

在 `giftClipCropToPixels()` 中先 normalize；完整值走 `centeredFullPixelRect()` 再走 `constrainPixelRect()`。在 `constrainPixelAxis()` 中把最大值设为 `Math.min(MAX_GIFT_CLIP_CROP_SIZE, bound)`：

```ts
const minimum = Math.min(MIN_GIFT_CLIP_SOURCE_SIZE, bound);
const maximum = Math.min(MAX_GIFT_CLIP_CROP_SIZE, bound);
const constrainedSize = clamp(roundedSize, minimum, maximum);
```

在 `updateGiftClipCrop()` 中为固定对边的缩放加入最大距离：

```ts
const maximumWidth = Math.min(MAX_GIFT_CLIP_CROP_SIZE, sourceWidth);
const maximumHeight = Math.min(MAX_GIFT_CLIP_CROP_SIZE, sourceHeight);

if (handle === 'n' || handle === 'ne' || handle === 'nw') {
  top = clamp(top + deltaY, Math.max(0, bottom - maximumHeight), bottom - minimumHeight);
}
if (handle === 's' || handle === 'se' || handle === 'sw') {
  bottom = clamp(bottom + deltaY, top + minimumHeight, Math.min(sourceHeight, top + maximumHeight));
}
if (handle === 'e' || handle === 'ne' || handle === 'se') {
  right = clamp(right + deltaX, left + minimumWidth, Math.min(sourceWidth, left + maximumWidth));
}
if (handle === 'w' || handle === 'nw' || handle === 'sw') {
  left = clamp(left + deltaX, Math.max(0, right - maximumWidth), right - minimumWidth);
}
```

- [ ] **Step 5: 让 editor reset 复用纯几何默认约束**

只改 reset 的赋值，不在 editor 复制 4096：

```ts
reset: () => {
  if (destroyed) return;
  crop = constrainGiftClipCrop(defaultGiftClipCrop(), sourceWidth, sourceHeight);
  render();
},
```

- [ ] **Step 6: 运行 GREEN 与相邻回归测试**

Run:

```powershell
npx vitest run tests/gift-clip-crop.test.ts tests/gift-clip-renderer.test.ts tests/gift-clip-studio.test.ts
npm run typecheck
git diff --check
```

Expected: 所有命令 exit 0；现有 640×360 reset 仍返回完整画面，大素材返回居中 4096px 选区。

- [ ] **Step 7: 提交 Task 1**

```powershell
git add -- src/ui/config/gift-clip-crop.ts src/ui/config/gift-clip-crop-editor.ts tests/gift-clip-crop.test.ts
git commit -m "feat: cap gift clip crops at 4096px"
```

---

### Task 2: 裁剪编辑器活动状态、辅助线与清理

**Files:**
- Modify: `tests/gift-clip-crop.test.ts:73-438`
- Modify: `src/ui/config/gift-clip-crop-editor.ts:34-214`

**Interfaces:**
- Consumes: Task 1 的有界 `constrainGiftClipCrop()` / `updateGiftClipCrop()`；现有 `GiftClipCropEditor` interface。
- Produces: `.gift-clip-crop-guides` 装饰节点、框体 `.is-adjusting` / `.is-moving` 可观察状态；不新增 export。

- [ ] **Step 1: 扩展 fake DOM 使其能观察 class 移除、keyup 与 focusout**

在 `CropTestElement` 中补齐测试所需的浏览器表面：

```ts
onkeyup: ((event: KeyboardEvent) => unknown) | null = null;
onfocusout: ((event: FocusEvent) => unknown) | null = null;

readonly classList = {
  add: (...names: string[]) => this.updateClasses(names, true),
  remove: (...names: string[]) => this.updateClasses(names, false),
  contains: (name: string) => this.className.split(/\s+/).includes(name),
};

dispatchKeyUp(key: string, target: CropTestElement = this): void {
  this.onkeyup?.({ key, target, currentTarget: this } as unknown as KeyboardEvent);
}

dispatchFocusOut(target: CropTestElement = this): void {
  this.onfocusout?.({ target, currentTarget: this } as unknown as FocusEvent);
}
```

把现有 class mutation 提取成与 studio fake 相同的 `updateClasses(names, add)` 私有 helper。

```ts
private updateClasses(names: string[], add: boolean): void {
  const classes = new Set(this.className.split(/\s+/).filter(Boolean));
  for (const name of names) {
    if (add) classes.add(name);
    else classes.delete(name);
  }
  this.className = [...classes].join(' ');
}
```

- [ ] **Step 2: 写出指针活动状态与装饰节点 RED 测试**

```ts
it('exposes an inert rule-of-thirds guide only while pointer adjustment is active', () => {
  const { frame, layer } = createCropEditorHarness();
  const guides = layer.querySelector('.gift-clip-crop-guides');
  const eastHandle = layer.querySelector('.is-e');
  if (!guides || !eastHandle) throw new Error('crop guide or east handle missing');

  expect(guides.getAttribute('aria-hidden')).toBe('true');
  expect(frame.classList.contains('is-adjusting')).toBe(false);
  frame.dispatchPointer('pointerdown', { pointerId: 11, target: frame });
  expect(frame.classList.contains('is-adjusting')).toBe(true);
  expect(frame.classList.contains('is-moving')).toBe(true);
  frame.dispatchPointer('pointerup', { pointerId: 11 });
  expect(frame.classList.contains('is-adjusting')).toBe(false);

  frame.dispatchPointer('pointerdown', { pointerId: 12, target: eastHandle });
  expect(frame.classList.contains('is-adjusting')).toBe(true);
  expect(frame.classList.contains('is-moving')).toBe(false);
  frame.dispatchPointer('pointercancel', { pointerId: 12 });
  expect(frame.classList.contains('is-adjusting')).toBe(false);
});
```

- [ ] **Step 3: 写出键盘与销毁 RED 测试**

```ts
it('shows keyboard adjustment state until keyup or focusout', () => {
  const { frame, layer } = createCropEditorHarness();
  const eastHandle = layer.querySelector('.is-e');
  if (!eastHandle) throw new Error('east handle missing');

  frame.dispatchKey('ArrowLeft', eastHandle);
  expect(frame.classList.contains('is-adjusting')).toBe(true);
  frame.dispatchKeyUp('ArrowLeft', eastHandle);
  expect(frame.classList.contains('is-adjusting')).toBe(false);

  frame.dispatchKey('ArrowRight');
  frame.dispatchFocusOut();
  expect(frame.classList.contains('is-adjusting')).toBe(false);
});
```

扩展现有 destroy 测试，断言 `onkeyup` / `onfocusout` 为 null，`.is-adjusting` / `.is-moving` 被移除。

- [ ] **Step 4: 运行 RED 测试**

Run:

```powershell
npx vitest run tests/gift-clip-crop.test.ts
```

Expected: FAIL；辅助线节点不存在，活动类不会切换，keyup/focusout handler 未安装。

- [ ] **Step 5: 创建辅助线节点并集中同步活动状态**

在 editor 内创建装饰节点并添加到 frame；不改变返回接口：

```ts
const guides = document.createElement('div');
guides.className = 'gift-clip-crop-guides';
guides.setAttribute('aria-hidden', 'true');
```

维护 `keyboardAdjusting`，由一个私有闭包统一同步类名：

```ts
let keyboardAdjusting = false;

const syncAdjustmentState = (): void => {
  const adjusting = dragState !== null || keyboardAdjusting;
  if (adjusting) frame.classList.add('is-adjusting');
  else frame.classList.remove('is-adjusting');
  if (dragState?.handle === 'move') frame.classList.add('is-moving');
  else frame.classList.remove('is-moving');
};
```

pointerdown 建立 `dragState` 后调用 `syncAdjustmentState()`；pointerup、pointercancel、lostpointercapture 把 `dragState` 清空后调用它。

- [ ] **Step 6: 安装键盘结束处理并统一 reset/destroy 清理**

只在现有 onkeydown 接受了方向键与方向轴后设置状态：

```ts
keyboardAdjusting = true;
syncAdjustmentState();
```

增加 handler：

```ts
frame.onkeyup = (event) => {
  if (!event.key.startsWith('Arrow')) return;
  keyboardAdjusting = false;
  syncAdjustmentState();
};
frame.onfocusout = () => {
  keyboardAdjusting = false;
  syncAdjustmentState();
};
```

reset/destroy 都要释放仍捕获的 pointer、清空 `dragState` 与 `keyboardAdjusting`、调用 `syncAdjustmentState()`；destroy 还要把新 handler 设为 null。不要增加 document/global listener。

- [ ] **Step 7: 运行 GREEN、类型检查与 seam 回归**

Run:

```powershell
npx vitest run tests/gift-clip-crop.test.ts tests/gift-clip-studio.test.ts
npm run typecheck
git diff --check
```

Expected: 所有命令 exit 0；`gift-clip-studio.ts` 和 `tests/gift-clip-studio.test.ts` 没有 diff。

- [ ] **Step 8: 提交 Task 2**

```powershell
git add -- src/ui/config/gift-clip-crop-editor.ts tests/gift-clip-crop.test.ts
git commit -m "feat: expose gift crop adjustment states"
```

---

### Task 3: 方案 B 直角样式、原生光标与浏览器验收

**Files:**
- Modify: `scripts/verify-gift-clip-crop.mjs:1-183`
- Modify: `src/ui/config/config.css:3620-3778`

**Interfaces:**
- Consumes: Task 2 的 `.gift-clip-crop-guides`、`.is-adjusting`、`.is-moving` 和现有 8 个 `data-handle` button。
- Produces: 在 `.config-root` 内可通过计算样式和真实输入验证的直角面板、28/32px 热区、L/短条标记、原生光标与辅助线过渡。

- [ ] **Step 1: 在 Playwright 脚本写出直角、弹窗、热区与光标 RED 断言**

在打开有效动画并取得 stage/frame 后加入计算样式检查：

```js
const visualContract = await page.evaluate(() => {
  const requireElement = (selector) => {
    const element = document.querySelector(selector);
    assertElement(element, selector);
    return element;
  };
  function assertElement(element, selector) {
    if (!(element instanceof HTMLElement)) throw new Error(`${selector} missing`);
  }
  const style = (selector, pseudo) => getComputedStyle(requireElement(selector), pseudo);
  return {
    stageRadius: style('.gift-clip-stage').borderRadius,
    viewportRadius: style('.gift-clip-crop-viewport').borderRadius,
    frameRadius: style('.gift-clip-crop-frame').borderRadius,
    frameCursor: style('.gift-clip-crop-frame').cursor,
    dialogRadius: style('.gift-clip-dialog').borderRadius,
    handleWidth: style('.gift-clip-crop-handle').width,
    handleHeight: style('.gift-clip-crop-handle').height,
    handleRadius: style('.gift-clip-crop-handle').borderRadius,
    cornerTop: style('.gift-clip-crop-handle.is-nw', '::before').borderTopWidth,
    cornerLeft: style('.gift-clip-crop-handle.is-nw', '::before').borderLeftWidth,
    edgeWidth: style('.gift-clip-crop-handle.is-n', '::before').width,
    edgeHeight: style('.gift-clip-crop-handle.is-n', '::before').height,
    guidesOpacity: style('.gift-clip-crop-guides').opacity,
  };
});

assert.equal(visualContract.stageRadius, '0px');
assert.equal(visualContract.viewportRadius, '0px');
assert.equal(visualContract.frameRadius, '0px');
assert.equal(visualContract.frameCursor, 'grab');
assert.ok(Number.parseFloat(visualContract.dialogRadius) > 0, 'desktop dialog must retain rounding');
assert.equal(visualContract.handleWidth, '28px');
assert.equal(visualContract.handleHeight, '28px');
assert.equal(visualContract.handleRadius, '0px');
assert.equal(visualContract.cornerTop, '2px');
assert.equal(visualContract.cornerLeft, '2px');
assert.equal(visualContract.edgeWidth, '14px');
assert.equal(visualContract.edgeHeight, '3px');
assert.equal(Number(visualContract.guidesOpacity), 0);

const handleCursors = await page.locator('.gift-clip-crop-handle').evaluateAll((handles) => (
  Object.fromEntries(handles.map((handle) => [handle.dataset.handle, getComputedStyle(handle).cursor]))
));
assert.deepEqual(handleCursors, {
  n: 'ns-resize', ne: 'nesw-resize', e: 'ew-resize', se: 'nwse-resize',
  s: 'ns-resize', sw: 'nesw-resize', w: 'ew-resize', nw: 'nwse-resize',
});
```

- [ ] **Step 2: 写出真实指针/键盘辅助线 RED 断言**

在改变剪裁尺寸前，用 frame 中心测试移动状态：

```js
const frameBoundsForState = await frame.boundingBox();
assert.ok(frameBoundsForState, 'frame must be measurable for state checks');
await page.mouse.move(
  frameBoundsForState.x + frameBoundsForState.width / 2,
  frameBoundsForState.y + frameBoundsForState.height / 2,
);
await page.mouse.down();
await page.waitForFunction(() => document.querySelector('.gift-clip-crop-frame')?.classList.contains('is-moving'));
assert.equal(await frame.evaluate((element) => getComputedStyle(element).cursor), 'grabbing');
await page.waitForFunction(() => Number(getComputedStyle(document.querySelector('.gift-clip-crop-guides')).opacity) > 0);
await page.mouse.up();
await page.waitForFunction(() => Number(getComputedStyle(document.querySelector('.gift-clip-crop-guides')).opacity) === 0);

await frame.focus();
await page.keyboard.down('ArrowLeft');
await page.waitForFunction(() => document.querySelector('.gift-clip-crop-frame')?.classList.contains('is-adjusting'));
await page.keyboard.up('ArrowLeft');
await page.waitForFunction(() => !document.querySelector('.gift-clip-crop-frame')?.classList.contains('is-adjusting'));
```

在切到 390×844 后断言 handle 计算宽高都是 `32px`，stage 仍为 `0px` radius。

- [ ] **Step 3: 启动专用 Vite 服务并运行 RED 浏览器验收**

在专用终端运行；只记录并最终停止本次启动的进程，不触碰其他端口或现有服务：

```powershell
npm run dev -- --host 127.0.0.1 --port 12462 --strictPort
```

在另一个终端运行：

```powershell
$env:GIFT_CLIP_UI_URL='http://127.0.0.1:12462/tests/fixtures/gift-receipts.html'
node scripts/verify-gift-clip-crop.mjs
```

Expected: FAIL at the first old rounded stage, `move` cursor, 18px circle handle, missing guide style, or activity opacity assertion. The existing save/video/overflow assertions must still execute after styling is implemented.

- [ ] **Step 4: 用非布局绘制实现直角面板和三分线**

在 `config.css` 的现有 crop block 中设置：

```css
.config-root .gift-clip-stage,
.config-root .gift-clip-canvas,
.config-root .gift-clip-video,
.config-root .gift-clip-crop-layer,
.config-root .gift-clip-crop-frame,
.config-root .gift-clip-crop-viewport {
  border-radius: 0;
}

.config-root .gift-clip-crop-frame {
  --gift-clip-crop-control: color-mix(in srgb, var(--accent) 82%, white);
  border: 0;
  outline: 0;
  box-shadow: 0 0 0 1px rgba(255,255,255,.78), 0 0 0 9999px rgba(7,7,13,.66);
  cursor: grab;
}
.config-root .gift-clip-crop-frame.is-moving { cursor: grabbing; }
.config-root .gift-clip-crop-frame:focus-visible {
  outline: 2px solid color-mix(in srgb, var(--accent) 82%, white);
  outline-offset: -3px;
}

.config-root .gift-clip-crop-guides {
  position: absolute;
  z-index: 2;
  inset: 0;
  opacity: 0;
  background:
    linear-gradient(to right, transparent calc(33.333% - .5px), rgba(255,255,255,.62) calc(33.333% - .5px), rgba(255,255,255,.62) calc(33.333% + .5px), transparent calc(33.333% + .5px), transparent calc(66.666% - .5px), rgba(255,255,255,.62) calc(66.666% - .5px), rgba(255,255,255,.62) calc(66.666% + .5px), transparent calc(66.666% + .5px)),
    linear-gradient(to bottom, transparent calc(33.333% - .5px), rgba(255,255,255,.62) calc(33.333% - .5px), rgba(255,255,255,.62) calc(33.333% + .5px), transparent calc(33.333% + .5px), transparent calc(66.666% - .5px), rgba(255,255,255,.62) calc(66.666% - .5px), rgba(255,255,255,.62) calc(66.666% + .5px), transparent calc(66.666% + .5px));
  pointer-events: none;
  transition: opacity 140ms ease;
}
.config-root .gift-clip-crop-frame.is-adjusting .gift-clip-crop-guides { opacity: .72; }
```

删除旧 frame `::after` 角标绘制和会改变 focus 边框宽度的规则。不要改变 `.gift-clip-info-bar` 的圆角。

- [ ] **Step 5: 用透明按钮和伪元素实现 28/32px 热区、L 角标与边标**

```css
.config-root .gift-clip-crop-handle {
  position: absolute;
  z-index: 3;
  width: 28px;
  height: 28px;
  margin: 0;
  border: 0;
  border-radius: 0;
  padding: 0;
  background: transparent;
  box-shadow: none;
  color: var(--gift-clip-crop-control);
  touch-action: none;
}
.config-root .gift-clip-crop-handle::before {
  position: absolute;
  box-sizing: border-box;
  content: '';
  transition: color 120ms ease, filter 120ms ease, opacity 120ms ease;
}
.config-root .gift-clip-crop-handle:hover::before,
.config-root .gift-clip-crop-handle:focus-visible::before {
  color: white;
  filter: drop-shadow(0 0 4px color-mix(in srgb, var(--accent) 72%, transparent));
}
.config-root .gift-clip-crop-handle.is-n::before,
.config-root .gift-clip-crop-handle.is-s::before {
  left: 50%;
  width: 14px;
  height: 3px;
  background: currentColor;
  transform: translateX(-50%);
}
.config-root .gift-clip-crop-handle.is-e::before,
.config-root .gift-clip-crop-handle.is-w::before {
  top: 50%;
  width: 3px;
  height: 14px;
  background: currentColor;
  transform: translateY(-50%);
}
```

为 `nw/ne/se/sw::before` 分别设置 20×20px 和对应两条 `2px solid currentColor` 边；为 n/s/e/w 放到对应边内侧。保留现有方向 cursor mapping，把位置规则改为透明热区贴合对应边角。窄屏 media query 把热区改成 32×32px，并移除旧 14px stage 圆角。

```css
.config-root .gift-clip-crop-handle.is-n::before { top: 3px; }
.config-root .gift-clip-crop-handle.is-s::before { bottom: 3px; }
.config-root .gift-clip-crop-handle.is-e::before { right: 3px; }
.config-root .gift-clip-crop-handle.is-w::before { left: 3px; }

.config-root .gift-clip-crop-handle.is-nw::before,
.config-root .gift-clip-crop-handle.is-ne::before,
.config-root .gift-clip-crop-handle.is-se::before,
.config-root .gift-clip-crop-handle.is-sw::before {
  width: 20px;
  height: 20px;
}
.config-root .gift-clip-crop-handle.is-nw::before {
  top: 4px;
  left: 4px;
  border-top: 2px solid currentColor;
  border-left: 2px solid currentColor;
}
.config-root .gift-clip-crop-handle.is-ne::before {
  top: 4px;
  right: 4px;
  border-top: 2px solid currentColor;
  border-right: 2px solid currentColor;
}
.config-root .gift-clip-crop-handle.is-se::before {
  right: 4px;
  bottom: 4px;
  border-right: 2px solid currentColor;
  border-bottom: 2px solid currentColor;
}
.config-root .gift-clip-crop-handle.is-sw::before {
  bottom: 4px;
  left: 4px;
  border-bottom: 2px solid currentColor;
  border-left: 2px solid currentColor;
}

@media (max-width: 540px) {
  .config-root .gift-clip-stage { border-radius: 0; }
  .config-root .gift-clip-crop-handle { width: 32px; height: 32px; }
}
```

- [ ] **Step 6: 增加 reduced-motion 并运行 GREEN 浏览器验收**

```css
@media (prefers-reduced-motion: reduce) {
  .config-root .gift-clip-crop-guides,
  .config-root .gift-clip-crop-handle::before {
    transition: none;
  }
}
```

Run:

```powershell
$env:GIFT_CLIP_UI_URL='http://127.0.0.1:12462/tests/fixtures/gift-receipts.html'
node scripts/verify-gift-clip-crop.mjs
npm run typecheck
npx vitest run tests/gift-clip-crop.test.ts tests/gift-clip-studio.test.ts
git diff --check
```

Expected: Playwright 输出 8 handles、512×360 saved/video、3 screenshots、0/0 overflow、0 console errors；其余命令 exit 0。人工打开 `artifacts/gift-clip-crop-desktop.png`、`gift-clip-crop-preview.png`、`gift-clip-crop-mobile.png`，确认裁剪面板直角、弹窗范围正确、没有圆形控制点、角标和边标对齐。

- [ ] **Step 7: 停止且只停止本任务启动的 Vite 进程**

在启动 Vite 的专用终端发送 Ctrl+C，并确认 12462 已释放。不要结束 12450、50844 或任何非本任务进程。

- [ ] **Step 8: 提交 Task 3**

```powershell
git add -- src/ui/config/config.css scripts/verify-gift-clip-crop.mjs
git commit -m "feat: polish gift crop controls"
```

---

### Task 4: 全量验证、审查、本地 master 合并与 EXE

**Files:**
- Verify only: all tracked files changed by Tasks 1-3
- Merge target: `E:\bilibili` local `master`
- Build artifact: `E:\bilibili\dist\gift-panel.exe`

**Interfaces:**
- Consumes: Tasks 1-3 的三个独立提交和既有配置页 seam。
- Produces: 审查通过的本地 master merge commit、通过全量验证的 Windows EXE；不产生 push 或 release。

- [ ] **Step 1: 在功能分支运行完整新鲜验证**

Run:

```powershell
npm run typecheck
npm test
npm run build:ui
go test ./...
go vet ./...
git diff --check
git status --short
```

Expected: 所有命令 exit 0；Vitest 无失败，Vite 构建成功，Go test/vet 成功，`git diff --check` 和 `git status --short` 无输出。

重新启动专用 12462 Vite 服务并再次运行：

```powershell
$env:GIFT_CLIP_UI_URL='http://127.0.0.1:12462/tests/fixtures/gift-receipts.html'
node scripts/verify-gift-clip-crop.mjs
```

Expected: 8 handles、512×360 saved/video、3 screenshots、0/0 overflow、0 console errors；随后只停止本次 Vite 进程。

- [ ] **Step 2: 使用 `superpowers:requesting-code-review` 做规格与质量双轴审查**

审查固定范围为规格提交 `3a73aab` 之后的实现提交，并逐项核对：

- 规格覆盖：直角范围、方案 B、grab/grabbing、8 向 cursor、调整时辅助线、4096px、reset、大素材居中、reduced-motion。
- seam：`giftClipAnimationKey()` / `openGiftClipStudio()` 与 `GiftClipCropEditor` interface 未扩张，`gift-clip-studio.ts` 无职责回流。
- 生命周期：pointerup/cancel/lost capture、keyup/focusout、reset/destroy 都能清理。
- 测试质量：通过公开函数和可观察 DOM 结果验证，不读取私有 drag state，不用字符串快照替代真实浏览器计算样式。

任何阻塞 finding 都必须先加能复现的失败测试、做最小修复、运行相关与全量验证，并单独提交 `fix: address gift crop UI review`；审查清零后才能合并。

- [ ] **Step 3: 确认功能分支提交序列和干净状态**

```powershell
git log --oneline 3a73aab..HEAD
git status --short
```

Expected: 至少包含 Task 1、Task 2、Task 3 三个独立提交；工作区干净。

- [ ] **Step 4: 在主仓库预检 tracked/untracked 状态并安全合并**

```powershell
$mainRepo = 'E:\bilibili'
$beforeStatus = @(git -C $mainRepo status --porcelain=v1 --untracked-files=all)
$trackedBefore = @($beforeStatus | Where-Object { -not $_.StartsWith('?? ') })
if ($trackedBefore.Count -gt 0) { throw "Main repo has tracked changes: $($trackedBefore -join '; ')" }
$untrackedBefore = @($beforeStatus | Where-Object { $_.StartsWith('?? ') })
git -C $mainRepo branch --show-current
git -C $mainRepo merge --no-ff codex/gift-clip-crop-editor -m "merge: polish gift clip crop UI"
```

Expected: branch 是 `master`，merge 成功且不覆盖未跟踪文件。若 Git 报告未跟踪路径冲突，停止并报告，不移动或删除文件。

- [ ] **Step 5: 在本地 master 重跑验证并构建 EXE**

```powershell
npm --prefix E:\bilibili run typecheck
npm --prefix E:\bilibili test
npm --prefix E:\bilibili run build:ui
go -C E:\bilibili test ./...
go -C E:\bilibili vet ./...
npm --prefix E:\bilibili run build:exe
```

Expected: 所有命令 exit 0；`E:\bilibili\dist\gift-panel.exe` 存在。不要启动 EXE。

- [ ] **Step 6: 证明未跟踪文件保持、记录 merge 与 EXE 哈希**

```powershell
$afterStatus = @(git -C $mainRepo status --porcelain=v1 --untracked-files=all)
$untrackedAfter = @($afterStatus | Where-Object { $_.StartsWith('?? ') })
$untrackedDiff = @(Compare-Object $untrackedBefore $untrackedAfter)
if ($untrackedDiff.Count -gt 0) { throw "Untracked files changed: $($untrackedDiff | Out-String)" }
git -C $mainRepo log -1 --oneline
Get-Item -LiteralPath 'E:\bilibili\dist\gift-panel.exe' | Select-Object FullName,Length,LastWriteTime
Get-FileHash -Algorithm SHA256 -LiteralPath 'E:\bilibili\dist\gift-panel.exe'
git -C $mainRepo status --short
```

Expected: untracked before/after 集合相同；HEAD 是本轮 merge commit；EXE 有非零长度和 SHA256；`git status --short` 只显示原有未跟踪文件。最终报告明确“不推送、不发布、未启动 EXE”。
