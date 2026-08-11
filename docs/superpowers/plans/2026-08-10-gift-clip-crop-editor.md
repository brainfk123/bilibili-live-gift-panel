# 礼物动画剪裁编辑器 Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 将礼物动画回放改为带 8 个控制点的剪裁编辑器，按剪裁区域原始像素生成视频，并把现有 967 行工作室拆成职责清晰的深模块。

**Architecture:** 配置页继续只通过 `giftClipAnimationKey()` 和 `openGiftClipStudio()` 使用礼物动画工作室。工作室内部拆为纯剪裁几何、媒体会话、Canvas 渲染、录制下载、DOM 剪裁编辑器五个模块；`gift-clip-studio.ts` 仅编排加载、编辑、录制、预览和清理状态。剪裁数据以归一化坐标持久化，录制时再转换为素材原始像素区域。

**Tech Stack:** TypeScript 5、DOM Pointer Events、Canvas 2D、MediaRecorder、gifuct-js、Vitest 2、Go、Playwright、Vite。

## Global Constraints

- 剪裁框默认覆盖完整素材，包含上、右上、右、右下、下、左下、左、左上 8 个控制点。
- 剪裁框始终限制在素材内部；剪裁宽高均不得小于原始素材的 64px。
- 素材原始宽度或高度任一小于 64px 时，显示“动画尺寸过小，无法制作回放”，不创建剪裁编辑器或录制器。
- 成片宽高必须等于剪裁区域对应的原始素材像素，不固定为 480×480，不设置最长边上限，也不额外缩放。
- 信息条所有尺寸只按 `outputWidth / 480` 缩放，不根据输出高度二次缩小；信息条保持半透明紫粉渐变。
- 完整特效使用 RGB 与 Alpha 合成后的真实画面宽高，不使用双通道打包视频宽高，也不预先缩小到 480px。
- 剪裁预设保存为 `settings.giftClipCrops`，最多 200 条；旧 `giftClipPlacements` 不迁移、不继续使用。
- 配置分片格式从 10 升到 11；版本 10 可升级，版本高于 11 必须继续拒绝读取。
- 关闭弹窗、重新剪裁、中止或失败时释放 RAF、MediaRecorder 流、媒体元素、Blob URL、临时 Canvas 与 Pointer Events。
- 配置页仍只调用稳定的工作室外部 seam；拆分后的内部模块不得在配置页形成新的调用面。
- 验证通过后提交当前分支、合并到本地 `master` 并构建 Windows EXE；不得清理用户文件、推送或发布。

---

## File Structure

- Create `src/ui/config/gift-clip-crop.ts`: 归一化剪裁模型、原始像素换算、8 个控制点、移动/缩放与边界约束。
- Create `src/ui/config/gift-clip-media.ts`: 礼物动画/头像加载、GIF 解码、完整特效合成、短动画回退和媒体资源释放。
- Create `src/ui/config/gift-clip-renderer.ts`: 素材预览、剪裁成片绘制、信息条布局与 Canvas 尺寸管理。
- Create `src/ui/config/gift-clip-recorder.ts`: MediaRecorder 格式选择、录制循环、中止、流释放、文件名和下载。
- Create `src/ui/config/gift-clip-crop-editor.ts`: DOM 遮罩、剪裁框、8 个控制点、Pointer Events 和信息条占位预览。
- Modify `src/ui/config/gift-clip-studio.ts`: 保留稳定外部 seam，缩减为对话框状态编排。
- Modify `src/ui/config/config.ts`: 从 `giftClipCrops` 读取和保存当前动画剪裁预设。
- Modify `src/ui/config/config.css`: 素材比例舞台、遮罩、剪裁框、控制点、占位信息条和响应式布局。
- Modify `src/types.ts`: 先加入 `GiftClipCrop` / `giftClipCrops`，最终接线时删除旧定位类型。
- Modify `src/storage.ts`: 前端默认值、归一化、最终旧字段忽略和 200 条上限。
- Modify `goserver/state.go`: Go 剪裁状态、默认值和归一化。
- Modify `goserver/state_shards.go`: 配置分片版本升级到 11。
- Create `tests/gift-clip-crop.test.ts`: 纯几何、8 个控制点、最小尺寸和像素换算。
- Create `tests/gift-clip-media.test.ts`: 从原测试迁移 GIF/完整特效解析测试并锁定真实合成尺寸。
- Create `tests/gift-clip-renderer.test.ts`: 输出尺寸、源区域绘制和信息条宽度缩放。
- Create `tests/gift-clip-recorder.test.ts`: 从原测试迁移录制格式、资源释放、文件名和下载测试。
- Modify `tests/gift-clip-studio.test.ts`: 保留稳定动画键、时长和工作室编排行为测试。
- Modify `tests/storage.test.ts`: `giftClipCrops` 默认值、归一化、上限和旧字段忽略。
- Modify `goserver/config_store_test.go`: Go 配置保存、克隆、归一化与 schema 升级测试。
- Modify `tests/fixtures/gift-receipts.ts`: 提供大于 64px 的本地动画/头像素材和可观察的保存状态。
- Create `scripts/verify-gift-clip-crop.mjs`: Playwright 桌面/移动端交互、视频尺寸和截图验收。

---

### Task 1: 剪裁领域模型与前端配置持久化

**Files:**
- Create: `src/ui/config/gift-clip-crop.ts`
- Create: `tests/gift-clip-crop.test.ts`
- Modify: `src/types.ts:340-365`
- Modify: `src/storage.ts:80-105, 340-420`
- Modify: `tests/storage.test.ts:15-110`

**Interfaces:**
- Consumes: 原始素材宽高与 Pointer Events 换算出的原始像素位移。
- Produces: `GiftClipCrop`, `GiftClipPixelRect`, `GiftClipCropHandle`, `defaultGiftClipCrop()`, `normalizeGiftClipCrop()`, `constrainGiftClipCrop()`, `giftClipCropToPixels()`, `giftClipCropFromPixels()`, `updateGiftClipCrop()`, `isGiftClipSourceSizeSupported()`；过渡提交暂时保留旧定位字段，保证当前工作室仍可运行。

- [ ] **Step 1: 写出剪裁几何和存储的失败测试**

```ts
// tests/gift-clip-crop.test.ts
import { describe, expect, it } from 'vitest';
import {
  defaultGiftClipCrop,
  giftClipCropFromPixels,
  giftClipCropToPixels,
  isGiftClipSourceSizeSupported,
  normalizeGiftClipCrop,
  updateGiftClipCrop,
  type GiftClipCropHandle,
} from '../src/ui/config/gift-clip-crop';

describe('gift clip crop geometry', () => {
  const initial = giftClipCropFromPixels({ x: 100, y: 75, width: 200, height: 150 }, 400, 300);
  const expected: Record<GiftClipCropHandle, { x: number; y: number; width: number; height: number }> = {
    move: { x: 140, y: 105, width: 200, height: 150 },
    n: { x: 100, y: 105, width: 200, height: 120 },
    ne: { x: 100, y: 105, width: 240, height: 120 },
    e: { x: 100, y: 75, width: 240, height: 150 },
    se: { x: 100, y: 75, width: 240, height: 180 },
    s: { x: 100, y: 75, width: 200, height: 180 },
    sw: { x: 140, y: 75, width: 160, height: 180 },
    w: { x: 140, y: 75, width: 160, height: 150 },
    nw: { x: 140, y: 105, width: 160, height: 120 },
  };

  it.each(Object.keys(expected) as GiftClipCropHandle[])('updates %s without changing unrelated edges', (handle) => {
    expect(giftClipCropToPixels(updateGiftClipCrop(initial, handle, 40, 30, 400, 300), 400, 300))
      .toEqual(expected[handle]);
  });

  it('defaults damaged values to the full source and clamps valid values inside it', () => {
    expect(defaultGiftClipCrop()).toEqual({ x: 0, y: 0, width: 1, height: 1 });
    expect(normalizeGiftClipCrop({ x: Number.NaN, y: 0, width: 1, height: 1 })).toEqual(defaultGiftClipCrop());
    expect(giftClipCropToPixels({ x: .9, y: .9, width: .5, height: .5 }, 400, 300))
      .toEqual({ x: 200, y: 150, width: 200, height: 150 });
  });

  it('keeps move and resize operations in bounds with a 64px minimum', () => {
    const tiny = giftClipCropFromPixels({ x: 100, y: 100, width: 80, height: 80 }, 400, 300);
    expect(giftClipCropToPixels(updateGiftClipCrop(tiny, 'se', -999, -999, 400, 300), 400, 300))
      .toEqual({ x: 100, y: 100, width: 64, height: 64 });
    expect(giftClipCropToPixels(updateGiftClipCrop(tiny, 'move', 999, 999, 400, 300), 400, 300))
      .toEqual({ x: 320, y: 220, width: 80, height: 80 });
  });

  it('rejects a source when either original dimension is under 64px', () => {
    expect(isGiftClipSourceSizeSupported(64, 64)).toBe(true);
    expect(isGiftClipSourceSizeSupported(63, 640)).toBe(false);
    expect(isGiftClipSourceSizeSupported(640, 63)).toBe(false);
  });
});
```

```ts
// tests/storage.test.ts additions/replacements
expect(defaultState().settings.giftClipCrops).toEqual({});

serverState.settings.giftClipCrops = {
  'effect:1': { x: .1, y: .2, width: .6, height: .7 },
  'media:clamped': { x: .9, y: -.2, width: .5, height: 2 },
  'media:invalid': { x: Number.NaN, y: 0, width: 1, height: 1 },
};
expect(loadState().settings.giftClipCrops).toEqual({
  'effect:1': { x: .1, y: .2, width: .6, height: .7 },
  'media:clamped': { x: .5, y: 0, width: .5, height: 1 },
  'media:invalid': { x: 0, y: 0, width: 1, height: 1 },
});

serverState.settings.giftClipCrops = Object.fromEntries(
  Array.from({ length: 205 }, (_, index) => [`effect:${index}`, { x: 0, y: 0, width: 1, height: 1 }]),
);
vi.stubGlobal('fetch', vi.fn(async () => Response.json(serverState)));
await hydrateStateFromServer();
expect(Object.keys(loadState().settings.giftClipCrops)).toHaveLength(200);
```

- [ ] **Step 2: 运行测试并确认缺少新类型、模块和字段**

Run: `npm test -- tests/gift-clip-crop.test.ts tests/storage.test.ts`

Expected: FAIL，错误包含无法解析 `gift-clip-crop` 或 `giftClipCrops` 不存在。

- [ ] **Step 3: 实现纯几何模块和前端持久化**

```ts
// src/types.ts
export interface GiftClipCrop {
  x: number;
  y: number;
  width: number;
  height: number;
}

export interface Settings {
  // existing fields remain unchanged
  giftClipCrops: Record<string, GiftClipCrop>;
  giftClipPlacements: Record<string, GiftClipPlacement>; // Task 7 完成切换后删除
}
```

```ts
// src/ui/config/gift-clip-crop.ts
import type { GiftClipCrop } from '../../types';

export const MIN_GIFT_CLIP_SOURCE_SIZE = 64;
export type GiftClipCropHandle = 'move' | 'n' | 'ne' | 'e' | 'se' | 's' | 'sw' | 'w' | 'nw';
export interface GiftClipPixelRect { x: number; y: number; width: number; height: number }

export const defaultGiftClipCrop = (): GiftClipCrop => ({ x: 0, y: 0, width: 1, height: 1 });

export function isGiftClipSourceSizeSupported(width: number, height: number): boolean {
  return Number.isInteger(width) && Number.isInteger(height)
    && width >= MIN_GIFT_CLIP_SOURCE_SIZE && height >= MIN_GIFT_CLIP_SOURCE_SIZE;
}

export function normalizeGiftClipCrop(value: unknown): GiftClipCrop {
  if (!value || typeof value !== 'object') return defaultGiftClipCrop();
  const candidate = value as Partial<GiftClipCrop>;
  const numbers = [candidate.x, candidate.y, candidate.width, candidate.height].map(Number);
  if (numbers.some((number) => !Number.isFinite(number)) || numbers[2] <= 0 || numbers[3] <= 0) {
    return defaultGiftClipCrop();
  }
  const width = Math.min(1, numbers[2]);
  const height = Math.min(1, numbers[3]);
  return {
    x: Math.min(1 - width, Math.max(0, numbers[0])),
    y: Math.min(1 - height, Math.max(0, numbers[1])),
    width,
    height,
  };
}
```

Implement `giftClipCropToPixels()` by rounding left/top/right/bottom edges, then shifting or expanding the rectangle inside `sourceWidth × sourceHeight` until both dimensions are at least 64px. Implement `giftClipCropFromPixels()` as the inverse normalized conversion. Implement `updateGiftClipCrop()` in pixel space: `move` shifts all four edges; `n/ne/nw` change top; `s/se/sw` change bottom; `e/ne/se` change right; `w/nw/sw` change left; clamp moving rectangles as a whole and clamp resized edges against the opposite edge minus 64px.

```ts
// src/storage.ts settings construction during the compatibility phase
const parsedSettings = parsed.settings ?? {};
const settings: AppState['settings'] = {
  ...base.settings,
  ...parsedSettings,
  giftClipCrops: normalizeGiftClipCrops(parsedSettings.giftClipCrops),
  giftClipPlacements: normalizeGiftClipPlacements(parsedSettings.giftClipPlacements),
  // existing normalized settings remain here
};
```

`normalizeGiftClipCrops()` must accept only non-empty keys up to 160 characters, keep at most 200 entries, and call `normalizeGiftClipCrop()` for every retained value. Add default `giftClipCrops: {}` next to the temporary `giftClipPlacements: {}`; Task 7 removes the old default and normalizer after the working studio has switched.

- [ ] **Step 4: 运行定向测试和类型检查**

Run: `npm test -- tests/gift-clip-crop.test.ts tests/storage.test.ts`

Expected: PASS。

Run: `npm run typecheck`

Expected: PASS；兼容字段保证现有工作室调用点仍可编译。

- [ ] **Step 5: 提交剪裁模型**

```powershell
git add src/types.ts src/storage.ts src/ui/config/gift-clip-crop.ts tests/gift-clip-crop.test.ts tests/storage.test.ts
git commit -m "feat: add normalized gift clip crops"
```

---

### Task 2: Go 配置 schema 11 与剪裁归一化

**Files:**
- Modify: `goserver/state.go:328-355, 450-565, 719-735`
- Modify: `goserver/state_shards.go:13-17`
- Modify: `goserver/config_store_test.go:159-185, 250-315`

**Interfaces:**
- Consumes: 前端 PATCH 中的 `settings.giftClipCrops`。
- Produces: `giftClipCropState`, `settingsState.GiftClipCrops`, `normalizeGiftClipCrops()` 与 `stateShardSchemaVersion = 11`；旧 Go 定位字段暂时保留到 Task 7 的原子切换。

- [ ] **Step 1: 把旧定位测试改成剪裁保存、损坏修复和版本升级测试**

```go
func TestConfigStorePersistsGiftClipCrops(t *testing.T) {
    store := &configStore{path: filepath.Join(t.TempDir(), "config.json")}
    patch := httptest.NewRecorder()
    store.handle(patch, httptest.NewRequest(http.MethodPatch, "/api/config", strings.NewReader(`{
        "settings":{"giftClipCrops":{
            "effect:99":{"x":0.1,"y":0.2,"width":0.6,"height":0.7},
            "media:clamped":{"x":0.9,"y":-1,"width":0.5,"height":2},
            "media:invalid":{"x":0,"y":0,"width":0,"height":1}
        }}
    }`)))
    if patch.Code != http.StatusOK { t.Fatalf("PATCH status = %d, body = %s", patch.Code, patch.Body.String()) }

    state, err := store.readState()
    if err != nil { t.Fatal(err) }
    if got := state.Settings.GiftClipCrops["effect:99"]; got != (giftClipCropState{X: .1, Y: .2, Width: .6, Height: .7}) {
        t.Fatalf("saved crop = %#v", got)
    }
    if got := state.Settings.GiftClipCrops["media:clamped"]; got != (giftClipCropState{X: .5, Y: 0, Width: .5, Height: 1}) {
        t.Fatalf("clamped crop = %#v", got)
    }
    if got := state.Settings.GiftClipCrops["media:invalid"]; got != (giftClipCropState{X: 0, Y: 0, Width: 1, Height: 1}) {
        t.Fatalf("repaired crop = %#v", got)
    }
    clone, err := cloneAppState(state)
    if err != nil { t.Fatal(err) }
    if got := clone.Settings.GiftClipCrops["effect:99"]; got != state.Settings.GiftClipCrops["effect:99"] {
        t.Fatalf("cloned crop = %#v, want %#v", got, state.Settings.GiftClipCrops["effect:99"])
    }
}

func TestStateShardVersionTenUpgradesToEleven(t *testing.T) {
    store := &configStore{path: filepath.Join(t.TempDir(), "config.json")}
    if err := os.WriteFile(store.path, []byte(`{"schemaVersion":10,"settings":{"theme":"light"}}`), 0o600); err != nil { t.Fatal(err) }
    if err := store.migrateLegacy(); err != nil { t.Fatal(err) }
    data, err := os.ReadFile(store.path)
    if err != nil { t.Fatal(err) }
    var metadata struct { SchemaVersion int `json:"schemaVersion"` }
    if err := json.Unmarshal(data, &metadata); err != nil { t.Fatal(err) }
    if metadata.SchemaVersion != 11 { t.Fatalf("schemaVersion = %d, want 11", metadata.SchemaVersion) }
}

func TestNormalizeGiftClipCropsRejectsNonFiniteAndLimitsCount(t *testing.T) {
    input := make(map[string]giftClipCropState, 205)
    input["invalid"] = giftClipCropState{X: math.NaN(), Y: 0, Width: 1, Height: 1}
    for index := 0; index < 204; index++ {
        input[fmt.Sprintf("effect:%d", index)] = giftClipCropState{X: 0, Y: 0, Width: 1, Height: 1}
    }
    got := normalizeGiftClipCrops(input)
    if len(got) != 200 { t.Fatalf("crop count = %d, want 200", len(got)) }
    if crop, exists := got["invalid"]; exists && crop != fullGiftClipCrop() {
        t.Fatalf("non-finite crop = %#v", crop)
    }
}
```

- [ ] **Step 2: 运行 Go 测试并确认旧结构不满足新断言**

Run: `go test ./goserver -run "GiftClipCrops|VersionTen" -count=1`

Expected: FAIL，错误包含 `GiftClipCrops undefined` 或 schema 仍为 10。

- [ ] **Step 3: 实现 Go 状态与版本升级**

```go
type giftClipCropState struct {
    X      float64 `json:"x"`
    Y      float64 `json:"y"`
    Width  float64 `json:"width"`
    Height float64 `json:"height"`
}

type settingsState struct {
    // existing fields remain unchanged
    GiftClipCrops map[string]giftClipCropState `json:"giftClipCrops"`
    GiftClipPlacements map[string]giftClipPlacementState `json:"giftClipPlacements"` // Task 7 删除
}
```

Add `GiftClipCrops` defaults and normalization beside the temporary `GiftClipPlacements` compatibility path. `normalizeGiftClipCrops()` must trim keys, reject empty or over-160-character keys, stop at 200 entries, reject NaN/Inf by restoring the full crop, restore non-positive width/height to the full crop, clamp width/height to 1, and clamp x/y to `0..1-width` / `0..1-height`.

```go
const stateShardSchemaVersion = 11

func fullGiftClipCrop() giftClipCropState {
    return giftClipCropState{X: 0, Y: 0, Width: 1, Height: 1}
}
```

Version-10 files without `giftClipCrops` receive the empty default map and are rewritten at version 11 by existing shard migration. Keep normalizing the old placement map during this compatibility commit so the current gift clip studio remains usable; Task 7 removes the old Go field and its normalizer together with the frontend cutover.

- [ ] **Step 4: 运行 Go 定向测试和完整 Go 测试**

Run: `go test ./goserver -run "GiftClipCrops|VersionTen|StateShard" -count=1`

Expected: PASS。

Run: `go test ./...`

Expected: PASS。

- [ ] **Step 5: 提交后端 schema**

```powershell
git add goserver/state.go goserver/state_shards.go goserver/config_store_test.go
git commit -m "feat: persist gift clip crops"
```

---

### Task 3: 提取礼物动画媒体会话模块

**Files:**
- Create: `src/ui/config/gift-clip-media.ts`
- Create: `tests/gift-clip-media.test.ts`

**Interfaces:**
- Consumes: `GiftReceipt`、现有 `giftReceiptMediaUrl()` 同源媒体端点和一个隐藏媒体宿主元素。
- Produces: `GiftClipVisual`, `GiftClipMediaSession`, `loadGiftClipMediaSession()`, `normalizeGiftClipDuration()`；会话统一暴露素材尺寸、时长、来源标签、头像、逐帧读取、重播、暂停和释放。

- [ ] **Step 1: 先把媒体行为测试迁移到新接口并增加真实合成尺寸断言**

```ts
// tests/gift-clip-media.test.ts
import { describe, expect, it } from 'vitest';
import {
  giftEffectDurationMs,
  giftEffectVisualSize,
  giftGifFrameIndex,
  normalizeGiftClipDuration,
  normalizeGiftEffectLayout,
} from '../src/ui/config/gift-clip-media';

describe('gift clip media', () => {
  const layout = normalizeGiftEffectLayout({
    videoWidth: 1088,
    videoHeight: 1280,
    rgbFrame: [0, 0, 720, 1280],
    alphaFrame: [724, 0, 360, 640],
    fps: 30,
    frames: 390,
  });

  it('uses the RGB composite dimensions without a 480px pre-scale', () => {
    expect(giftEffectVisualSize(layout)).toEqual({ width: 720, height: 1280 });
    expect(giftEffectDurationMs(layout)).toBe(13_000);
  });

  it('selects deterministic GIF frames across loops', () => {
    expect([0, 219, 220, 500, 660].map((time) => giftGifFrameIndex([220, 220, 220], time)))
      .toEqual([0, 0, 1, 2, 0]);
  });

  it('clamps missing and abnormal durations', () => {
    expect([undefined, 200, 2200, 60_000].map(normalizeGiftClipDuration)).toEqual([3000, 1000, 2200, 15_000]);
  });

  it('rejects packed-alpha coordinates outside the video', () => {
    expect(() => normalizeGiftEffectLayout({ ...layout, rgbFrame: [0, 0, 1200, 1280] }))
      .toThrow('礼物特效坐标无效');
  });
});
```

- [ ] **Step 2: 运行媒体测试并确认新模块尚不存在**

Run: `npm test -- tests/gift-clip-media.test.ts`

Expected: FAIL，错误包含无法解析 `gift-clip-media`。

- [ ] **Step 3: 提取媒体实现并建立小接口**

```ts
// src/ui/config/gift-clip-media.ts
export interface GiftClipVisual {
  source: CanvasImageSource;
  width: number;
  height: number;
}

export interface GiftClipMediaSession {
  readonly width: number;
  readonly height: number;
  readonly durationMs: number;
  readonly sourceLabel: '完整特效' | '短动画回退' | '短动画';
  readonly avatar: HTMLImageElement | null;
  visualAt(elapsedMs: number): GiftClipVisual | null;
  restart(): Promise<void>;
  pause(): void;
  dispose(): void;
}

export async function loadGiftClipMediaSession(
  receipt: GiftReceipt,
  sourceMediaHost: HTMLElement,
): Promise<GiftClipMediaSession>;
```

Implement full-effect validation/compositing, GIF parsing/frame disposal, animated WebP/image loading, avatar fallback, duration normalization and Blob URL registries behind this interface, using the current working implementation as the behavioral reference. A session must own every image, video, object URL and temporary effect Canvas it creates. `dispose()` is idempotent. Full effects allocate the RGB, Alpha and composite Canvas at `rgbFrame[2] × rgbFrame[3]`; do not carry `fitGiftEffectFrame()` into the new module, so no 480px pre-scale remains. If complete-effect loading fails and GIF/WebP exists, return `sourceLabel: '短动画回退'`; otherwise propagate the complete-effect error. The old in-file implementation remains active only until Task 7 atomically switches the gift clip studio to this module and deletes the duplicate code.

- [ ] **Step 4: 运行媒体测试、现有工作室测试和类型检查**

Run: `npm test -- tests/gift-clip-media.test.ts tests/gift-clip-studio.test.ts`

Expected: PASS。

Run: `npm run typecheck`

Expected: PASS。

- [ ] **Step 5: 提交媒体模块拆分**

```powershell
git add src/ui/config/gift-clip-media.ts tests/gift-clip-media.test.ts
git commit -m "refactor: extract gift clip media session"
```

---

### Task 4: 提取按剪裁区域绘制的 Canvas 渲染模块

**Files:**
- Create: `src/ui/config/gift-clip-renderer.ts`
- Create: `tests/gift-clip-renderer.test.ts`

**Interfaces:**
- Consumes: `GiftClipVisual`、`GiftClipPixelRect`、`GiftReceipt` 和头像。
- Produces: `drawGiftClipSourcePreview()`, `prepareGiftClipOutputCanvas()`, `drawGiftClipOutputFrame()`, `giftClipInfoBarLayout()`。

- [ ] **Step 1: 写出输出像素、源剪裁和只按宽度缩放的失败测试**

```ts
// tests/gift-clip-renderer.test.ts
import { describe, expect, it, vi } from 'vitest';
import type { GiftReceipt } from '../src/types';
import {
  drawGiftClipOutputFrame,
  giftClipInfoBarLayout,
  prepareGiftClipOutputCanvas,
} from '../src/ui/config/gift-clip-renderer';

function createGiftClipContextStub(options: { width: number; height: number; drawImage: ReturnType<typeof vi.fn> }): CanvasRenderingContext2D {
  const gradient = { addColorStop: vi.fn() };
  return {
    canvas: { width: options.width, height: options.height },
    createLinearGradient: vi.fn(() => gradient),
    createRadialGradient: vi.fn(() => gradient),
    fillRect: vi.fn(),
    clearRect: vi.fn(),
    drawImage: options.drawImage,
    save: vi.fn(),
    restore: vi.fn(),
    beginPath: vi.fn(),
    roundRect: vi.fn(),
    arc: vi.fn(),
    clip: vi.fn(),
    fill: vi.fn(),
    stroke: vi.fn(),
    fillText: vi.fn(),
    measureText: vi.fn((text: string) => ({ width: text.length * 8 })),
  } as unknown as CanvasRenderingContext2D;
}

const receiptFixture = (): GiftReceipt => ({
  id: 'receipt-1', time: 1_700_000_000, giftId: 1, giftName: '测试礼物', num: 1,
  price: 100, totalCoin: 100, coinType: 'gold', uname: '测试观众', effects: [],
});

describe('gift clip renderer', () => {
  it('sizes the recording canvas to the original crop pixels', () => {
    const canvas = { width: 480, height: 480 } as HTMLCanvasElement;
    prepareGiftClipOutputCanvas(canvas, { x: 64, y: 32, width: 512, height: 256 });
    expect({ width: canvas.width, height: canvas.height }).toEqual({ width: 512, height: 256 });
  });

  it('scales the information bar from output width and ignores height', () => {
    expect(giftClipInfoBarLayout(960, 240)).toEqual(expect.objectContaining({ scale: 2, height: 180 }));
    expect(giftClipInfoBarLayout(960, 960)).toEqual(expect.objectContaining({ scale: 2, height: 180 }));
  });

  it('draws the selected source pixels to matching output pixels', () => {
    const drawImage = vi.fn();
    const context = createGiftClipContextStub({ width: 320, height: 180, drawImage });
    const visual = { source: {} as CanvasImageSource, width: 640, height: 360 };
    drawGiftClipOutputFrame(context, receiptFixture(), visual, null, { x: 80, y: 40, width: 320, height: 180 });
    expect(drawImage).toHaveBeenCalledWith(visual.source, 80, 40, 320, 180, 0, 0, 320, 180);
  });
});
```

- [ ] **Step 2: 运行渲染测试并确认新模块尚不存在**

Run: `npm test -- tests/gift-clip-renderer.test.ts`

Expected: FAIL，错误包含无法解析 `gift-clip-renderer`。

- [ ] **Step 3: 实现统一渲染模块**

```ts
// src/ui/config/gift-clip-renderer.ts
export function prepareGiftClipOutputCanvas(canvas: HTMLCanvasElement, crop: GiftClipPixelRect): void {
  canvas.width = crop.width;
  canvas.height = crop.height;
}

export function giftClipInfoBarLayout(outputWidth: number, outputHeight: number) {
  const scale = outputWidth / 480;
  return {
    scale,
    x: 18 * scale,
    y: outputHeight - 110 * scale,
    width: 444 * scale,
    height: 90 * scale,
    radius: 22 * scale,
    avatarX: 67 * scale,
    avatarY: outputHeight - 65 * scale,
    avatarRadius: 30 * scale,
    textX: 114 * scale,
    nameY: outputHeight - 71 * scale,
    giftY: outputHeight - 44 * scale,
  };
}
```

`drawGiftClipSourcePreview()` fills the preview Canvas with the existing dark purple/pink background and draws the full source at `(0, 0, sourceWidth, sourceHeight)`. `drawGiftClipOutputFrame()` fills the output-sized background, calls `drawImage(visual.source, crop.x, crop.y, crop.width, crop.height, 0, 0, crop.width, crop.height)`, then draws the existing translucent information bar with every baseline dimension multiplied by `outputWidth / 480`. It must not read a fixed `CLIP_SIZE` and must allow negative information-bar y coordinates to clip naturally on short output regions.

Implement `roundedRect()` and `truncateCanvasText()` privately in this module. Keep the preparation label outside `drawGiftClipOutputFrame()` so it cannot enter recorded frames. The old in-file renderer remains active only until Task 7 replaces it, after which the duplicate drawing functions and their source-string tests are deleted.

- [ ] **Step 4: 运行渲染、工作室和媒体测试**

Run: `npm test -- tests/gift-clip-renderer.test.ts tests/gift-clip-studio.test.ts tests/gift-clip-media.test.ts`

Expected: PASS。

- [ ] **Step 5: 提交渲染模块拆分**

```powershell
git add src/ui/config/gift-clip-renderer.ts tests/gift-clip-renderer.test.ts
git commit -m "refactor: extract gift clip renderer"
```

---

### Task 5: 提取可中止的 Canvas 录制模块

**Files:**
- Create: `src/ui/config/gift-clip-recorder.ts`
- Create: `tests/gift-clip-recorder.test.ts`

**Interfaces:**
- Consumes: 输出 Canvas、录制时长、逐帧回调、进度回调和 `AbortSignal`。
- Produces: `recordGiftClipCanvas()`, `GiftClipRecording`, `sanitizeGiftClipFilename()`, `triggerGiftClipDownload()`；MediaRecorder 和流生命周期完全留在模块内部。

- [ ] **Step 1: 迁移格式/下载测试并增加异常释放测试**

```ts
// tests/gift-clip-recorder.test.ts
import { afterEach, describe, expect, it, vi } from 'vitest';
import {
  recordGiftClipCanvas,
  sanitizeGiftClipFilename,
  selectGiftClipRecorder,
  stopGiftClipStream,
  triggerGiftClipDownload,
} from '../src/ui/config/gift-clip-recorder';

afterEach(() => vi.unstubAllGlobals());

it('prefers MP4 and falls back to WebM when MP4 construction fails', () => {
  class FakeRecorder {
    static isTypeSupported = vi.fn(() => true);
    mimeType: string;
    constructor(_stream: MediaStream, options?: MediaRecorderOptions) {
      this.mimeType = options?.mimeType ?? '';
      if (this.mimeType.includes('mp4')) throw new Error('MP4 unavailable');
    }
  }
  expect(selectGiftClipRecorder({} as MediaStream, FakeRecorder as unknown as typeof MediaRecorder).extension).toBe('webm');
});

it('stops the capture stream when drawing throws', async () => {
  const stop = vi.fn();
  class FakeRecorder {
    static isTypeSupported = vi.fn(() => true);
    state: RecordingState = 'inactive';
    mimeType = 'video/webm';
    ondataavailable: ((event: BlobEvent) => void) | null = null;
    onerror: (() => void) | null = null;
    onstop: (() => void) | null = null;
    start(): void { this.state = 'recording'; }
    stop(): void { this.state = 'inactive'; this.onstop?.(); }
  }
  vi.stubGlobal('MediaRecorder', FakeRecorder);
  const canvas = { captureStream: () => ({ getTracks: () => [{ stop }] }) } as unknown as HTMLCanvasElement;
  await expect(recordGiftClipCanvas({
    canvas,
    durationMs: 1000,
    drawFrame: () => { throw new Error('draw failed'); },
    onProgress: vi.fn(),
    signal: new AbortController().signal,
  })).rejects.toThrow('draw failed');
  expect(stop).toHaveBeenCalledOnce();
});
```

Retain the existing exact filename, missing MediaRecorder, all-track cleanup and temporary-anchor assertions in this new file.

- [ ] **Step 2: 运行录制测试并确认新模块尚不存在**

Run: `npm test -- tests/gift-clip-recorder.test.ts`

Expected: FAIL，错误包含无法解析 `gift-clip-recorder`。

- [ ] **Step 3: 实现录制深模块**

```ts
// src/ui/config/gift-clip-recorder.ts
export interface GiftClipRecording {
  blob: Blob;
  mimeType: string;
  extension: 'mp4' | 'webm';
}

export async function recordGiftClipCanvas(options: {
  canvas: HTMLCanvasElement;
  durationMs: number;
  drawFrame: (elapsedMs: number) => void;
  onProgress: (progress: number) => void;
  signal: AbortSignal;
}): Promise<GiftClipRecording>;
```

Inside `recordGiftClipCanvas()`: verify `captureStream`, create a 30fps stream, select MP4/WebM with the existing ordered format list, call `drawFrame(0)` before `recorder.start(250)`, drive frames with RAF until `durationMs`, report a normalized `0..1` progress, stop and await the recorder, reject zero-byte output, and return the Blob plus selected metadata. Attach one abort listener that cancels RAF and stops the recorder; in `finally`, remove that listener, cancel any remaining RAF and stop every stream track. Preserve existing Chinese error messages.

Implement format selection, filename sanitation, stream cleanup and download functions in this module using the current gift clip studio behavior as the reference. Do not expose MediaRecorder state to the studio. The duplicate in-file recorder remains active only until Task 7 switches the gift clip studio and deletes it.

- [ ] **Step 4: 运行录制测试和类型检查**

Run: `npm test -- tests/gift-clip-recorder.test.ts tests/gift-clip-studio.test.ts`

Expected: PASS。

Run: `npm run typecheck`

Expected: PASS。

- [ ] **Step 5: 提交录制模块拆分**

```powershell
git add src/ui/config/gift-clip-recorder.ts tests/gift-clip-recorder.test.ts
git commit -m "refactor: extract gift clip recorder"
```

---

### Task 6: DOM 剪裁编辑器与 8 个控制点

**Files:**
- Create: `src/ui/config/gift-clip-crop-editor.ts`
- Modify: `src/ui/config/config.css:3596-3681`
- Test: `tests/gift-clip-crop.test.ts`

**Interfaces:**
- Consumes: 舞台元素、素材原始尺寸、初始 `GiftClipCrop` 和一个不可交互的信息条预览节点。
- Produces: `createGiftClipCropEditor()` 返回 `GiftClipCropEditor`，供工作室读取当前剪裁、复位和销毁。

- [ ] **Step 1: 补充显示坐标转换测试**

```ts
// tests/gift-clip-crop.test.ts addition
it('converts display pointer deltas to original source pixels', () => {
  expect(giftClipDisplayDeltaToSource(48, 24, { width: 480, height: 270 }, 640, 360))
    .toEqual({ x: 64, y: 32 });
});
```

- [ ] **Step 2: 运行测试并确认显示换算函数尚不存在**

Run: `npm test -- tests/gift-clip-crop.test.ts`

Expected: FAIL，错误包含 `giftClipDisplayDeltaToSource` 不存在。

- [ ] **Step 3: 实现 DOM 编辑器和精确 Pointer Events 生命周期**

```ts
// src/ui/config/gift-clip-crop-editor.ts
export interface GiftClipCropEditor {
  readonly element: HTMLElement;
  getCrop(): GiftClipCrop;
  reset(): void;
  destroy(): void;
}

export function createGiftClipCropEditor(options: {
  stage: HTMLElement;
  sourceWidth: number;
  sourceHeight: number;
  initialCrop: GiftClipCrop;
  infoPreview: HTMLElement;
  onChange: (crop: GiftClipCrop, pixels: GiftClipPixelRect) => void;
}): GiftClipCropEditor;
```

Create `.gift-clip-crop-layer` and `.gift-clip-crop-frame`; the frame uses percentage `left/top/width/height`, an outside `box-shadow: 0 0 0 9999px rgba(7, 7, 13, .66)`, and a nested `.gift-clip-crop-viewport` with `overflow: hidden`. Append eight `<button type="button" class="gift-clip-crop-handle is-n" data-handle="n" aria-label="调整上边">` controls for `n/ne/e/se/s/sw/w/nw`. Append `infoPreview` inside the viewport with `pointer-events: none` and scale its 480px baseline from the crop frame's displayed width.

On pointer down, use the target handle or `move`; store pointer id, client origin and initial crop. Convert client deltas with `giftClipDisplayDeltaToSource()`, call `updateGiftClipCrop()`, render percentages and notify `onChange`. Capture/release the pointer on the frame. `destroy()` must release a captured pointer when present, clear `onpointerdown/move/up/cancel`, and remove the layer. `reset()` restores `{x:0,y:0,width:1,height:1}`.

Add CSS cursors for all handles, 18px desktop hit targets, 24px mobile hit targets, a visible accent/white border, corner marks, full-stage touch isolation, and a mobile full-screen dialog. The stage uses `aspect-ratio: var(--gift-clip-source-width) / var(--gift-clip-source-height)` and stays within both available width and viewport height.

- [ ] **Step 4: 运行几何测试、样式构建和类型检查**

Run: `npm test -- tests/gift-clip-crop.test.ts`

Expected: PASS。

Run: `npm run build:ui`

Expected: PASS，CSS 和新模块可被 Vite 解析。

Run: `npm run typecheck`

Expected: PASS。

- [ ] **Step 5: 提交剪裁编辑器**

```powershell
git add src/ui/config/gift-clip-crop-editor.ts src/ui/config/gift-clip-crop.ts src/ui/config/config.css tests/gift-clip-crop.test.ts
git commit -m "feat: add gift clip crop controls"
```

---

### Task 7: 用新模块重写工作室编排并接入配置页

**Files:**
- Modify: `src/ui/config/gift-clip-studio.ts:1-967`
- Modify: `src/ui/config/config.ts:2723-2742`
- Modify: `src/types.ts:340-365`
- Modify: `src/storage.ts:90-105, 340-420`
- Modify: `goserver/state.go:328-355, 550-735`
- Modify: `goserver/config_store_test.go:159-185`
- Modify: `tests/gift-clip-studio.test.ts`
- Modify: `tests/storage.test.ts`

**Interfaces:**
- Consumes: Tasks 1–6 的剪裁、媒体、渲染、录制和 DOM 编辑器接口。
- Produces: 保持 `giftClipAnimationKey()` 和 `openGiftClipStudio()` 外部 seam；`GiftClipStudioOptions` 改为 `initialCrop` / `onCropConfirmed`。

- [ ] **Step 1: 先把工作室测试改成新配置和小尺寸门禁**

```ts
// tests/gift-clip-studio.test.ts
import {
  giftClipAnimationKey,
} from '../src/ui/config/gift-clip-studio';
import { normalizeGiftClipDuration } from '../src/ui/config/gift-clip-media';
import { defaultState } from '../src/storage';

it('keeps a stable crop key for signed versions of the same animation URL', () => {
  expect(giftClipAnimationKey({ giftId: 1, animation: { gif: 'https://i0.hdslb.com/a.gif?token=one' } }))
    .toBe(giftClipAnimationKey({ giftId: 2, animation: { gif: 'https://i0.hdslb.com/a.gif?token=two' } }));
});

it('keeps loading copy outside the recorded renderer', () => {
  const source = readFileSync(new URL('../src/ui/config/gift-clip-studio.ts', import.meta.url), 'utf8');
  expect(source).toContain('正在读取礼物动画');
  const renderer = readFileSync(new URL('../src/ui/config/gift-clip-renderer.ts', import.meta.url), 'utf8');
  expect(renderer).not.toContain('正在准备礼物动画');
});

it('drops the legacy placement field after the crop cutover', async () => {
  const state = defaultState();
  expect(state.settings.giftClipCrops).toEqual({});
  expect((state.settings as unknown as Record<string, unknown>).giftClipPlacements).toBeUndefined();
});
```

Delete old `constrainGiftClipPlacement`, `giftClipCoverRect` and `giftClipPlacedCoverRect` tests because Task 1 replaces that behavior at the new geometry seam.

- [ ] **Step 2: 运行工作室和类型检查并确认旧接线失败**

Run: `npm test -- tests/gift-clip-studio.test.ts tests/storage.test.ts`

Expected: FAIL until the old placement fields and old renderer are removed.

Run: `npm run typecheck`

Expected: FAIL at the remaining `giftClipPlacements`, `initialPlacement` and `onPlacementConfirmed` references.

- [ ] **Step 3: 实现加载→剪裁→录制→预览状态流**

```ts
interface GiftClipStudioOptions {
  host: HTMLElement;
  receipt: GiftReceipt;
  initialCrop?: GiftClipCrop;
  onCropConfirmed?: (crop: GiftClipCrop) => void;
  onError?: (message: string) => void;
}
```

The rewritten `openGiftClipStudio()` must execute this exact sequence:

1. Build the dialog, source preview Canvas, hidden recording Canvas, preview video, hidden source-media host, status/progress and `恢复完整画面` / `确定剪裁并生成` / `重新剪裁` / `保存视频` buttons.
2. Call `loadGiftClipMediaSession(receipt, sourceMediaHost)` and retain the session until retry/close.
3. If `isGiftClipSourceSizeSupported(session.width, session.height)` is false, show `动画尺寸过小，无法制作回放（${width} × ${height}）`, hide generation actions, and leave only close/retry where retry reloads the source.
4. Set source preview Canvas to the session's original width/height, set stage aspect-ratio CSS variables, start one RAF loop that calls `session.visualAt(elapsed)` and `drawGiftClipSourcePreview()`.
5. Create the information preview DOM and `createGiftClipCropEditor()` from `initialCrop`. Display `剪裁 ${pixel.width} × ${pixel.height} · 成片按原始像素输出` on every change.
6. On reset call `editor.reset()`. On confirm, read the normalized crop, call `onCropConfirmed({...crop})`, stop the editor preview loop, destroy the editor, convert to pixels, and size the recording Canvas with `prepareGiftClipOutputCanvas()`.
7. Restart the media session and call `recordGiftClipCanvas()`; each frame reads `session.visualAt(elapsed)` and calls `drawGiftClipOutputFrame()` with the confirmed pixel crop. Progress maps `0..1` to the existing `<progress max="100">`.
8. Create the preview Blob URL, set the preview video's CSS aspect ratio to `${crop.width} / ${crop.height}`, and show the selected MP4/WebM format, byte size and exact dimensions.
9. `重新剪裁` aborts an active recording, revokes the old preview URL, reloads the same saved/current crop into a fresh editor and never auto-saves a new crop before confirmation.
10. `close()` aborts recording, cancels preview RAF, destroys the editor, disposes the media session, revokes preview URL, clears video source and removes global key/overlay handlers exactly once.

`gift-clip-studio.ts` must no longer contain GIF parsing, full-effect pixel loops, Canvas information-bar drawing, MediaRecorder format probing or pointer geometry.

- [ ] **Step 4: 接入 `giftClipCrops` 并删除旧字段调用点**

```ts
// src/ui/config/config.ts
const cropKey = giftClipAnimationKey(entry);
openGiftClipStudio({
  host: root,
  receipt: entry,
  initialCrop: state.settings.giftClipCrops[cropKey],
  onCropConfirmed: (crop) => {
    const crops = { ...state.settings.giftClipCrops };
    delete crops[cropKey];
    crops[cropKey] = crop;
    state.settings.giftClipCrops = Object.fromEntries(Object.entries(crops).slice(-200));
    save();
  },
  onError: (message) => toast(message, root),
});
```

In the same cutover, delete `GiftClipPlacement`, `settings.giftClipPlacements`, `normalizeGiftClipPlacements()` and their defaults from TypeScript and Go. Build frontend settings with an explicit legacy omission so an old server payload cannot reintroduce the key at runtime:

```ts
const parsedSettingsWithLegacy = (parsed.settings ?? {}) as Partial<AppState['settings']> & { giftClipPlacements?: unknown };
const { giftClipPlacements: _ignoredGiftClipPlacements, ...parsedSettings } = parsedSettingsWithLegacy;
const settings: AppState['settings'] = {
  ...base.settings,
  ...parsedSettings,
  giftClipCrops: normalizeGiftClipCrops(parsedSettings.giftClipCrops),
  // existing normalized settings remain here
};
```

Remove `GiftClipPlacements` and `normalizeGiftClipPlacements()` from `goserver/state.go`; keep `GiftClipCrops` as the only serialized field. Update the Go config-store test to assert that a legacy `giftClipPlacements` input is ignored after a read/write cycle.

Run: `rg -n "GiftClipPlacement|giftClipPlacements|initialPlacement|onPlacementConfirmed|constrainGiftClipPlacement|giftClipPlacedCoverRect" src goserver tests`

Expected: no matches。

- [ ] **Step 5: 运行全部 TypeScript 测试和类型检查**

Run: `npm test`

Expected: PASS。

Run: `npm run typecheck`

Expected: PASS。

- [ ] **Step 6: 提交工作室编排接线**

```powershell
git add src/ui/config/gift-clip-studio.ts src/ui/config/config.ts src/types.ts src/storage.ts goserver/state.go goserver/config_store_test.go tests/gift-clip-studio.test.ts tests/storage.test.ts
git commit -m "feat: generate clips from cropped source pixels"
```

---

### Task 8: Playwright 交互、视频尺寸与响应式截图验收

**Files:**
- Modify: `tests/fixtures/gift-receipts.ts`
- Create: `scripts/verify-gift-clip-crop.mjs`
- Output: `artifacts/gift-clip-crop-desktop.png`
- Output: `artifacts/gift-clip-crop-mobile.png`
- Output: `artifacts/gift-clip-crop-preview.png`

**Interfaces:**
- Consumes: Vite 提供的 `tests/fixtures/gift-receipts.html` 与工作室 DOM seam。
- Produces: 可重复执行的浏览器验收脚本、三张截图和无控制台错误的检查结果。

- [ ] **Step 1: 扩展本地夹具到 640×360 并记录保存的剪裁**

```ts
// tests/fixtures/gift-receipts.ts
const animationSVG = `
<svg xmlns="http://www.w3.org/2000/svg" width="640" height="360" viewBox="0 0 640 360">
  <defs><linearGradient id="sky" x1="0" y1="0" x2="1" y2="1"><stop stop-color="#172d68"/><stop offset="1" stop-color="#e44f91"/></linearGradient></defs>
  <rect width="640" height="360" fill="url(#sky)"/>
  <circle cx="460" cy="150" r="72" fill="#ffd8e8"><animate attributeName="cy" values="140;175;140" dur="1.2s" repeatCount="indefinite"/></circle>
</svg>`;
const tinyAnimationSVG = `<svg xmlns="http://www.w3.org/2000/svg" width="63" height="120"><rect width="63" height="120" fill="#fb7299"/></svg>`;

if (requestURL.pathname === '/api/gift-receipts/media' && requestURL.searchParams.get('kind') === 'animation') {
  if (requestURL.searchParams.get('id') === 'tiny-animation') return nativeFetch(dataURL(tinyAnimationSVG));
  return nativeFetch(dataURL(animationSVG));
}
```

Add a `tiny-animation` receipt whose animation URL is non-empty, and expose the mutable fixture state as `window.__giftReceiptFixtureState = state` so Playwright can assert the normalized crop saved through `/api/config` PATCH without reading implementation-private objects.

- [ ] **Step 2: 编写浏览器脚本并先验证它会发现未完成交互**

```js
// scripts/verify-gift-clip-crop.mjs
import { mkdir } from 'node:fs/promises';
import { createRequire } from 'node:module';
import { resolve } from 'node:path';
const require = createRequire(import.meta.url);
const { chromium } = require('playwright');

const baseURL = process.env.GIFT_CLIP_UI_URL ?? 'http://127.0.0.1:12462/tests/fixtures/gift-receipts.html';
const artifactDir = resolve(process.cwd(), 'artifacts');
await mkdir(artifactDir, { recursive: true });
const browser = await chromium.launch({ headless: true });
const page = await browser.newPage({ viewport: { width: 1440, height: 1000 }, deviceScaleFactor: 1 });
const errors = [];
page.on('console', (message) => { if (message.type() === 'error') errors.push(message.text()); });
page.on('pageerror', (error) => errors.push(error.message));
await page.goto(baseURL);
await page.getByRole('button', { name: '制作回放' }).first().click();
await page.locator('.gift-clip-crop-frame').waitFor();
await page.screenshot({ path: resolve(artifactDir, 'gift-clip-crop-desktop.png') });
```

Continue this same script with exact assertions: eight handles exist; the initial status contains `640 × 360`; drag the west handle right by exactly 96 CSS pixels on a 480px-wide stage and assert status becomes `剪裁 512 × 360`; drag the frame beyond the lower-right corner and assert its bounding box remains inside the stage; click `确定剪裁并生成`; wait for the preview video and assert `videoWidth === 512` and `videoHeight === 360`; capture `gift-clip-crop-preview.png`; close/reopen and assert the crop status remains `512 × 360`; make an unconfirmed handle change, close, reopen again and assert it still reads `512 × 360`; click `恢复完整画面` and assert `640 × 360`, then close without confirming so the saved 512×360 crop remains; open the `tiny-animation` row and assert the dialog shows `动画尺寸过小，无法制作回放（63 × 120）`, has no `确定剪裁并生成` button and no crop frame; switch viewport to `390 × 844`, reopen the valid animation and capture `gift-clip-crop-mobile.png`; assert document/dialog horizontal overflow is at most 1px; assert `errors.length === 0`; close browser in `finally`.

Run with the pre-Task-8 script before completing selectors/assertions.

Expected: FAIL at the first missing selector or dimension assertion, demonstrating that the browser check is active.

- [ ] **Step 3: 完成夹具与脚本直到桌面、成片和移动端全部通过**

Start Vite in a dedicated terminal:

```powershell
npm run dev -- --host 127.0.0.1 --port 12462
```

Run:

```powershell
$env:GIFT_CLIP_UI_URL='http://127.0.0.1:12462/tests/fixtures/gift-receipts.html'
node scripts/verify-gift-clip-crop.mjs
```

Expected: 输出包含 `handles: 8`、`savedCrop: 512 × 360`、`video: 512 × 360`、`screenshots: 3`，且三张截图存在。停止专用 Vite 进程，不影响用户已有的 12450/50844 服务。

- [ ] **Step 4: 使用 Playwright 截图做人工视觉复核**

Inspect `artifacts/gift-clip-crop-desktop.png`, `artifacts/gift-clip-crop-preview.png`, and `artifacts/gift-clip-crop-mobile.png`. Confirm the outside mask is uniform, all control points are visible, the information placeholder stays inside the crop viewport, the translucent bar does not fully cover animation content, desktop actions align cleanly, and the mobile dialog has no clipped controls. If a visual defect is found, change only `config.css` or crop-editor DOM structure, rerun the script and replace all three screenshots.

- [ ] **Step 5: 提交浏览器验收与视觉修正**

```powershell
git add tests/fixtures/gift-receipts.ts scripts/verify-gift-clip-crop.mjs src/ui/config/config.css src/ui/config/gift-clip-crop-editor.ts
git commit -m "test: verify gift clip crop workflow"
```

Do not commit `artifacts/` unless it is already tracked by repository policy.

---

### Task 9: 完整回归、代码审查、合并主干和本地 EXE

**Files:**
- Verify: all files changed by Tasks 1–8
- Build output: `E:\bilibili\dist\gift-panel.exe`

**Interfaces:**
- Consumes: 当前功能分支上的全部实现和验收结果。
- Produces: 通过审查的提交、本地 `master` 合并提交和可校验的 Windows EXE；不推送、不发布。

- [ ] **Step 1: 运行完整自动化回归**

Run:

```powershell
npm run typecheck
npm test
go test ./...
go vet ./...
npm run build:ui
git diff --check
```

Expected: every command exits 0; Vitest and Go output contain no failed tests; `git diff --check` prints nothing。

- [ ] **Step 2: 使用 `superpowers:requesting-code-review` 审查规格符合性和模块 seam**

Reviewer must compare the branch against `docs/superpowers/specs/2026-08-10-gift-clip-crop-editor-design.md` and specifically verify: no old placement path remains; `gift-clip-studio.ts` only orchestrates; media/renderer/recorder resources are released; output dimensions are exact; the information bar ignores height; source/crop minimum is 64px; configuration schema is 11. Resolve every blocking finding and rerun Step 1.

- [ ] **Step 3: 确认功能分支干净并记录最终提交**

Run:

```powershell
git status --short
git log -8 --oneline
```

Expected: no tracked or untracked implementation files remain outside commits; ignored screenshots may remain only under ignored `artifacts/`。

- [ ] **Step 4: 在主仓库安全合并，不触碰用户未跟踪文件**

Run:

```powershell
git -C E:\bilibili status --short
git -C E:\bilibili branch --show-current
```

Expected: branch is `master`. Preserve every existing untracked file. If tracked changes overlap this feature, stop and report the exact files instead of cleaning or resetting them. Otherwise run:

```powershell
git -C E:\bilibili merge --no-ff codex/gift-clip-positioning -m "merge: add gift clip crop editor"
```

Expected: merge succeeds without removing or staging unrelated files。

- [ ] **Step 5: 从合并后的本地 master 构建 EXE**

Run:

```powershell
npm run build
Get-Item E:\bilibili\dist\gift-panel.exe | Select-Object FullName,Length,LastWriteTime
Get-FileHash E:\bilibili\dist\gift-panel.exe -Algorithm SHA256
```

Use `E:\bilibili` as the working directory for `npm run build`.

Expected: build exits 0, EXE exists with non-zero size, and SHA-256 is printed. Do not start the EXE because an existing instance may be running and version-aware single-instance behavior can close it.

- [ ] **Step 6: 最终交付报告**

Report: feature-branch commit range, local master merge hash, test counts, Playwright screenshot paths, EXE absolute path, byte size and SHA-256. State explicitly that nothing was pushed or released and that user untracked files were preserved.
