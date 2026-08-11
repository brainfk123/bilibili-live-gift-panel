# Gift Clip FFmpeg Export Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 用内嵌的精简 FFmpeg CLI 取代实时 Canvas/MediaRecorder 导出，使礼物动画按输入时间戳稳定生成恒定 30 FPS、H.264 MP4，并在 Windows 上优先硬件编码、自动回退软件模式。

**Architecture:** 前端继续通过稳定的 `openGiftClipStudio(...)` seam 协调剪裁 UI，但把 DOM view、静态图层生成和任务 API 分到独立模块；Go 后端新增可信素材解析、FFmpeg 适配、单并发任务队列和 HTTP 任务接口。FFmpeg 8.1.2 的已签名最小 CLI 以 ZIP/DEFLATE 嵌入 Go EXE，首次使用校验 SHA-256 后原子解压到版本化本地缓存。

**Tech Stack:** TypeScript 5.5、Vitest 2.1、Canvas 2D、Playwright/Chromium、Go 1.26、FFmpeg 8.1.2、Windows Media Foundation `h264_mf`、PowerShell、Vite 5.4。

## Global Constraints

- 已确认规格：`docs/superpowers/specs/2026-08-11-gift-clip-ffmpeg-export-design.md`。
- 输出只能是 H.264 MP4、恒定 30 FPS；输入 GIF/WebP/完整特效按自身 timestamp/delay 自适应，不做运动插帧。
- `h264_mf` 的编码器输入像素格式使用官方建议的 `nv12`；验收解码后的 H.264 仍必须报告标准 4:2:0 `yuv420p`。
- 输出宽高各为 64–4096 的偶数；剪裁 `x`/`y` 仍保留 1 px 精度，最大面积为 16,777,216 像素。
- 平均目标码率为 `2,000,000 × pixels / 2,073,600`，四舍五入到 50,000 bit/s，夹在 150,000–16,000,000 bit/s；peak 为 1.5 倍，VBV buffer 为 2 倍。
- Windows 先运行 `h264_mf -hw_encoding 1`，编码器失败只回退一次 `h264_mf -hw_encoding 0`；取消、坏输入、完整性或磁盘错误不得回退。
- FFmpeg 固定为 8.1.2，源码归档 `https://ffmpeg.org/releases/ffmpeg-8.1.2.tar.xz`，SHA-256 `464beb5e7bf0c311e68b45ae2f04e9cc2af88851abb4082231742a74d97b524c`，发布签名 key fingerprint `FCF986EA15E6E293A5644F10B4322F04D67658D8`。
- FFmpeg 构建保持 LGPL：不得启用 `--enable-gpl` 或 `--enable-nonfree`；关闭网络、音频、字幕、ffplay 和 ffprobe。
- 内层 `ffmpeg.exe` 先 Authenticode 签名再计算 hash/ZIP；外层 `gift-panel.exe` 最后签名。禁止 UPX。
- FFmpeg ZIP 目标 `<= 30,000,000 bytes`；`> 40,000,000 bytes` 时构建必须失败。
- 配置页外部 seam `openGiftClipStudio(options): GiftClipStudioController`、`giftClipAnimationKey()`、剪裁确认回调和持久化时机保持稳定。
- `gift-clip-studio.ts` 必须缩为协调器；不得容纳 HTTP 实现、PNG 编码、FFmpeg 状态解析或重复的 DOM 构造细节。
- 前端不能提交媒体 URL/本地路径；Go 只按 receipt ID 重新解析已允许的 Bilibili HTTPS 素材。
- FFmpeg 只能以绝对可执行路径和参数数组启动，不经过 shell；子进程由 Windows Job Object 托管。
- 每个实现任务必须遵循 RED → GREEN → REFACTOR 并独立提交；测试与 build 必须顺序运行，不能并发改写 `dist/index.html`。
- 保留工作树里现有 `package-lock.json` 状态和未跟踪诊断文件，除非某一任务明确接管它们；不得 push 或发布，直到用户另行授权最终发布动作。

## File Structure

### Frontend

- Modify `src/ui/config/gift-clip-crop.ts`: 保证像素宽高为偶数，同时保持 1 px 位置移动和 64–4096 边界。
- Create `src/ui/config/gift-clip-export-api.ts`: 创建/查询/取消导出任务和构造视频 URL；拥有轮询、AbortSignal 和稳定错误消息。
- Create `src/ui/config/gift-clip-export-layers.ts`: 一次性生成背景 PNG 和透明信息栏 PNG。
- Create `src/ui/config/gift-clip-animation-key.ts`: 独立保存跨 signed URL 稳定的剪裁 key 算法。
- Create `src/ui/config/gift-clip-studio-controller.ts`: 协调 media、crop、job 与 view 生命周期。
- Create `src/ui/config/gift-clip-studio-view.ts`: 只创建弹窗 DOM、暴露元素引用并渲染状态。
- Create `src/ui/config/gift-clip-download.ts`: MP4 文件名清洗和浏览器下载触发。
- Modify `src/ui/config/gift-clip-renderer.ts`: 把动态预览、背景绘制和信息栏绘制拆成可复用函数。
- Modify `src/ui/config/gift-clip-studio.ts`: 只协调 media session、crop editor、export layers/API 和 view；保持公开 seam。
- Delete `src/ui/config/gift-clip-recorder.ts`: 完成接入后移除 MediaRecorder 路径。
- Modify/Create matching tests under `tests/` for every module above.

### Go backend

- Create `goserver/gift_clip_profile.go`: 输出尺寸、帧数、时长和码率纯契约。
- Create `goserver/gift_clip_source.go`: receipt 查找、可信媒体下载、GIF/WebP 元数据和 packed-alpha layout 解析。
- Create `goserver/gift_clip_ffmpeg.go`: filter graph/参数、进度解析、错误分类和硬件/软件回退。
- Create `goserver/gift_clip_process_windows.go`: Windows Job Object 子进程执行。
- Create `goserver/gift_clip_process_other.go`: 非 Windows 测试/开发兼容执行器，强制软件模式。
- Create `goserver/gift_clip_payload.go`: embed manifest/ZIP、hash 校验、并发安全原子解压。
- Create `goserver/gift_clip_jobs.go`: 单 worker FIFO、任务状态、取消、TTL 和目录生命周期。
- Create `goserver/gift_clip_http.go`: multipart POST、状态 GET、视频 GET、DELETE。
- Modify `goserver/main.go`: 构造依赖、注册路由，并在退出时关闭任务管理器。
- Create matching `*_test.go` files; tests inject resolver、runner、clock and temp roots rather than touching user cache.

### FFmpeg build and release

- Create `scripts/build-ffmpeg.ps1`: 下载、验签、校验源码 hash，并用 MSYS2/MinGW 构建固定最小组件集。
- Create `scripts/package-ffmpeg.mjs`: 对已签名内层 EXE 计算 manifest 和 ZIP/DEFLATE。
- Create `scripts/verify-ffmpeg.mjs`: 检查组件白名单、协议、三类 fixture 解码和大小门限。
- Create `third_party/ffmpeg/configure.flags`, `third_party/ffmpeg/NOTICE.md`, `third_party/ffmpeg/COPYING.LGPLv2.1`。
- Create generated `goserver/ffmpeg/manifest.json` and `goserver/ffmpeg/ffmpeg.zip`。
- Modify `scripts/build-go.mjs`, `package.json`, `.github/workflows/release.yml`, `README.md`。

---

### Task 1: 偶数像素剪裁契约

**Files:**
- Modify: `tests/gift-clip-crop.test.ts`
- Modify: `src/ui/config/gift-clip-crop.ts`

**Interfaces:**
- Consumes: `GiftClipPixelRect`、`giftClipCropToPixels()`、`giftClipCropFromPixels()`、`updateGiftClipCrop()`。
- Produces: 原签名不变；所有可输出 rect 的 `width`/`height` 都是 64–4096 的偶数，`x`/`y` 仍可逐像素移动。

- [ ] **Step 1: 写出偶数宽高与 1 px 位置 RED 测试**

在 `gift clip crop geometry` 中加入：

```ts
it('normalizes output dimensions to even pixels without quantizing position', () => {
  const normalized = giftClipCropFromPixels(
    { x: 101, y: 53, width: 641, height: 359 },
    1200,
    800,
  );
  expect(giftClipCropToPixels(normalized, 1200, 800))
    .toEqual({ x: 101, y: 53, width: 640, height: 358 });

  const moved = updateGiftClipCrop(normalized, 'move', 1, 1, 1200, 800);
  expect(giftClipCropToPixels(moved, 1200, 800))
    .toEqual({ x: 102, y: 54, width: 640, height: 358 });
});

it('keeps the opposite resize edge fixed while dropping an odd pixel', () => {
  const initial = giftClipCropFromPixels(
    { x: 100, y: 100, width: 640, height: 360 },
    1200,
    800,
  );
  expect(giftClipCropToPixels(updateGiftClipCrop(initial, 'w', -1, 0, 1200, 800), 1200, 800))
    .toEqual({ x: 100, y: 100, width: 640, height: 360 });
  expect(giftClipCropToPixels(updateGiftClipCrop(initial, 'e', 1, 0, 1200, 800), 1200, 800))
    .toEqual({ x: 100, y: 100, width: 640, height: 360 });
});
```

- [ ] **Step 2: 运行 RED 测试**

Run:

```powershell
npx vitest run tests/gift-clip-crop.test.ts
```

Expected: FAIL；当前 `constrainPixelAxis()` 保留 641×359，resize 也会产生奇数尺寸。

- [ ] **Step 3: 在像素约束层统一向下对齐偶数**

在 `constrainPixelAxis()` 中加入私有 helper，最小值和最大值已经是偶数：

```ts
function evenPixelSize(value: number, minimum: number, maximum: number): number {
  const constrained = clamp(value, minimum, maximum);
  return constrained % 2 === 0 ? constrained : constrained - 1;
}

const constrainedSize = evenPixelSize(roundedSize, minimum, maximum);
```

在 `updateGiftClipCrop()` 返回前，根据活动边把奇数差值从活动边收回，保持对边固定：

```ts
if ((right - left) % 2 !== 0) {
  if (handle === 'w' || handle === 'nw' || handle === 'sw') left += 1;
  else right -= 1;
}
if ((bottom - top) % 2 !== 0) {
  if (handle === 'n' || handle === 'ne' || handle === 'nw') top += 1;
  else bottom -= 1;
}
```

- [ ] **Step 4: 更新受影响的既有奇数边界断言**

只把输出尺寸期望改成偶数；不得改变 `x`/`y` 的 1 px 断言、4096 上限或 64 下限。增加循环保护：

```ts
for (const handle of ['n', 'ne', 'e', 'se', 's', 'sw', 'w', 'nw'] as const) {
  const pixels = giftClipCropToPixels(updateGiftClipCrop(initial, handle, 17, 19, 1200, 800), 1200, 800);
  expect(pixels.width % 2).toBe(0);
  expect(pixels.height % 2).toBe(0);
}
```

- [ ] **Step 5: 运行 GREEN 与相邻回归**

```powershell
npx vitest run tests/gift-clip-crop.test.ts tests/gift-clip-renderer.test.ts tests/gift-clip-studio.test.ts
npm run typecheck
git diff --check
```

Expected: 全部 exit 0。

- [ ] **Step 6: 提交 Task 1**

```powershell
git add -- src/ui/config/gift-clip-crop.ts tests/gift-clip-crop.test.ts
git commit -m "fix: keep gift clip output dimensions even"
```

---

### Task 2: Go 输出 profile、帧数和码率纯函数

**Files:**
- Create: `goserver/gift_clip_profile_test.go`
- Create: `goserver/gift_clip_profile.go`

**Interfaces:**
- Produces:
  - `type giftClipCrop struct { X, Y, Width, Height int }`
  - `type giftClipOutputProfile struct { Width, Height, FPS, Frames int; Duration time.Duration; AverageBitrate, PeakBitrate, VBVBuffer int64 }`
  - `func newGiftClipOutputProfile(crop giftClipCrop, sourceWidth, sourceHeight int, duration time.Duration) (giftClipOutputProfile, error)`

- [ ] **Step 1: 写出尺寸、时长和码率 RED 表驱动测试**

```go
func TestNewGiftClipOutputProfileUsesEvenBoundsAndThirtyFPS(t *testing.T) {
	profile, err := newGiftClipOutputProfile(
		giftClipCrop{X: 101, Y: 53, Width: 960, Height: 540},
		1920, 1080, 2200*time.Millisecond,
	)
	if err != nil { t.Fatal(err) }
	if profile.FPS != 30 || profile.Frames != 66 || profile.Duration != 2200*time.Millisecond {
		t.Fatalf("profile = %#v", profile)
	}
}

func TestGiftClipBitrateScalesWithPixelArea(t *testing.T) {
	tests := []struct{ width, height int; average int64 }{
		{512, 360, 200_000}, {640, 360, 200_000}, {960, 540, 500_000},
		{1280, 720, 900_000}, {1920, 1080, 2_000_000},
		{2560, 1440, 3_550_000}, {3840, 2160, 8_000_000},
		{4096, 4096, 16_000_000},
	}
	for _, test := range tests {
		profile, err := newGiftClipOutputProfile(
			giftClipCrop{Width: test.width, Height: test.height},
			test.width, test.height, 3*time.Second,
		)
		if err != nil { t.Fatal(err) }
		if profile.AverageBitrate != test.average || profile.PeakBitrate != test.average*3/2 || profile.VBVBuffer != test.average*2 {
			t.Fatalf("%dx%d profile = %#v", test.width, test.height, profile)
		}
	}
}
```

另写拒绝用例：宽/高为 63、4097、奇数；负坐标；越过 source 边界；duration 小于 1 秒或大于 15 秒。

- [ ] **Step 2: 运行 RED 测试**

```powershell
Push-Location goserver
go test ./... -run "Test(NewGiftClipOutputProfile|GiftClipBitrate)" -count=1
Pop-Location
```

Expected: FAIL，类型和函数尚不存在。

- [ ] **Step 3: 实现最小 profile 契约**

```go
const (
	giftClipFPS = 30
	minGiftClipDimension = 64
	maxGiftClipDimension = 4096
	minGiftClipBitrate int64 = 150_000
	maxGiftClipBitrate int64 = 16_000_000
)

func giftClipAverageBitrate(width, height int) int64 {
	baselinePixels := int64(1920 * 1080)
	numerator := int64(2_000_000) * int64(width) * int64(height)
	rounded := ((numerator + 25_000*baselinePixels) / (50_000 * baselinePixels)) * 50_000
	return minInt64(maxGiftClipBitrate, maxInt64(minGiftClipBitrate, rounded))
}

func giftClipFrameCount(duration time.Duration) int {
	return int((duration.Nanoseconds()*giftClipFPS + int64(time.Second) - 1) / int64(time.Second))
}
```

`newGiftClipOutputProfile()` 必须先验证 crop，再计算 `Frames`；使用 ceiling 实现 `n/30 < duration` 的末帧规则。

- [ ] **Step 4: 运行 GREEN 和全量 Go 回归**

```powershell
Push-Location goserver
gofmt -w gift_clip_profile.go gift_clip_profile_test.go
go test ./... -count=1
Pop-Location
git diff --check
```

- [ ] **Step 5: 提交 Task 2**

```powershell
git add -- goserver/gift_clip_profile.go goserver/gift_clip_profile_test.go
git commit -m "feat: define gift clip output profile"
```

---

### Task 3: 可信礼物素材解析与输入时间信息

**Files:**
- Create: `goserver/gift_clip_source_test.go`
- Create: `goserver/gift_clip_source.go`
- Modify: `goserver/gift_receipts.go`

**Interfaces:**
- Consumes: `giftReceiptAPI.fetchMedia()`、`parseGiftEffectLayout()`、`normalizeGiftAnimationDuration()`。
- Produces:
  - `type giftClipSourceKind string` with `giftClipSourceGIF`, `giftClipSourceWebP`, `giftClipSourceEffect`
  - `type giftClipSource struct { Kind giftClipSourceKind; Path string; VisualWidth, VisualHeight int; Duration time.Duration; Layout *giftEffectLayout }`
  - `type giftClipSourceResolver interface { Resolve(context.Context, string, string) (giftClipSource, error) }`
  - `func newGiftClipSourceResolver(store *configStore, media *giftReceiptAPI) giftClipSourceResolver`

- [ ] **Step 1: 写出 GIF/WebP 元数据 RED 测试**

使用小型 fixture byte slices 验证 canvas 和循环时长：

```go
func TestGiftClipGIFInfoUsesFrameDelays(t *testing.T) {
	width, height, cycle, err := giftClipGIFInfo(twoFrameGIF(t, 120, 80, []int{4, 7}))
	if err != nil { t.Fatal(err) }
	if width != 120 || height != 80 || cycle != 110*time.Millisecond {
		t.Fatalf("info = %dx%d %s", width, height, cycle)
	}
}

func TestGiftClipWebPInfoUsesANMFDelays(t *testing.T) {
	width, height, cycle, err := giftClipWebPInfo(animatedWebPHeader(320, 180, 40, 70))
	if err != nil { t.Fatal(err) }
	if width != 320 || height != 180 || cycle != 110*time.Millisecond {
		t.Fatalf("info = %dx%d %s", width, height, cycle)
	}
}
```

fixture helper 要写入合法 RIFF/VP8X/ANIM/ANMF chunk 长度与 padding；WebP delay 小于 10 ms 时按 10 ms 计算。

- [ ] **Step 2: 写出 receipt 安全解析和完整特效回退 RED 测试**

构造临时 `configStore`、fake `giftMediaHTTPClient` 和一个 receipt：

```go
source, err := resolver.Resolve(context.Background(), receipt.ID, t.TempDir())
if err != nil { t.Fatal(err) }
if source.Kind != giftClipSourceEffect || source.Layout == nil || source.VisualWidth != 720 || source.VisualHeight != 1280 {
	t.Fatalf("source = %#v", source)
}
```

再让 effect MP4 返回坏签名但 GIF 返回成功，断言回退为 `giftClipSourceGIF`；不存在的 receipt、非 Bilibili URL、超限 body 和损坏 WebP 必须返回错误且不在 temp dir 留文件。

- [ ] **Step 3: 运行 RED 测试**

```powershell
Push-Location goserver
go test ./... -run "TestGiftClip(GIF|WebP|Source)" -count=1
Pop-Location
```

- [ ] **Step 4: 实现格式元数据 parser**

GIF 使用标准库：

```go
func giftClipGIFInfo(data []byte) (int, int, time.Duration, error) {
	animation, err := gif.DecodeAll(bytes.NewReader(data))
	if err != nil { return 0, 0, 0, err }
	var cycle time.Duration
	for _, delay := range animation.Delay {
		cycle += time.Duration(maxInt(1, delay)) * 10 * time.Millisecond
	}
	return animation.Config.Width, animation.Config.Height, cycle, nil
}
```

WebP parser 只接受 `RIFF....WEBP`，遍历 chunk；从 `VP8X` 的 24-bit little-endian canvas minus-one 读取尺寸，从每个 `ANMF` payload offset 12–14 读取毫秒 delay，并验证 chunk length/padding 不越界。

- [ ] **Step 5: 实现 resolver 和原子素材写入**

```go
type receiptGiftClipSourceResolver struct {
	store *configStore
	media *giftReceiptAPI
}

func (resolver *receiptGiftClipSourceResolver) Resolve(ctx context.Context, receiptID, taskDir string) (giftClipSource, error) {
	receipt, err := resolver.findReceipt(receiptID)
	if err != nil { return giftClipSource{}, err }
	if receipt.Animation != nil && receipt.Animation.MP4 != "" && receipt.Animation.MP4JSON != "" {
		if source, effectErr := resolver.resolveEffect(ctx, *receipt, taskDir); effectErr == nil {
			return source, nil
		}
	}
	return resolver.resolveShortAnimation(ctx, *receipt, taskDir)
}
```

`resolveEffect()` 同时下载并验证 MP4/layout，duration 为规范化 `frames/fps`；短动画 duration 优先 receipt `DurationMS`，值为 0 时使用解码 cycle，再缺失时使用 3 秒。文件先写 `*.partial`，`Sync`、关闭后 `Rename`；任一错误清除本次创建的文件。

- [ ] **Step 6: 运行 GREEN、安全回归和 race test**

```powershell
Push-Location goserver
gofmt -w gift_clip_source.go gift_clip_source_test.go gift_receipts.go
go test ./... -count=1
go test -race ./... -run "TestGiftClip" -count=1
Pop-Location
git diff --check
```

- [ ] **Step 7: 提交 Task 3**

```powershell
git add -- goserver/gift_clip_source.go goserver/gift_clip_source_test.go goserver/gift_receipts.go
git commit -m "feat: resolve trusted gift clip sources"
```

---

### Task 4: FFmpeg filter graph、参数与进度契约

**Files:**
- Create: `goserver/gift_clip_ffmpeg_test.go`
- Create: `goserver/gift_clip_ffmpeg.go`

**Interfaces:**
- Consumes: Task 2 `giftClipOutputProfile`、Task 3 `giftClipSource`。
- Produces:
  - `type giftClipEncoderMode string` with `giftClipEncoderHardware`, `giftClipEncoderSoftware`
  - `type giftClipEncodeRequest struct { Source giftClipSource; Crop giftClipCrop; Profile giftClipOutputProfile; BackgroundPath, OverlayPath, OutputPath string }`
  - `func buildGiftClipFFmpegArgs(giftClipEncodeRequest, giftClipEncoderMode) ([]string, error)`
  - `type giftClipProgressParser` with `Consume(line string) (float64, bool)`
  - `func shouldRetryGiftClipSoftware(error, string) bool`

- [ ] **Step 1: 写出短动画和 packed-alpha 参数 RED 测试**

```go
func TestBuildGiftClipFFmpegArgsCreatesDeterministicShortAnimationTimeline(t *testing.T) {
	request := giftClipEncodeFixture(giftClipSource{Kind: giftClipSourceWebP, Path: `C:\task\source.webp`, Duration: 2200*time.Millisecond})
	args, err := buildGiftClipFFmpegArgs(request, giftClipEncoderHardware)
	if err != nil { t.Fatal(err) }
	joined := strings.Join(args, " ")
	for _, want := range []string{
		"-stream_loop -1", "-f webp", "crop=960:540:101:53", "fps=30",
		"-c:v h264_mf", "-hw_encoding 1", "-rate_control pc_vbr",
		"-b:v 500000", "-maxrate 750000", "-bufsize 1000000",
		"-pix_fmt nv12", "-fps_mode cfr", "-movflags +faststart", "-progress pipe:1",
	} {
		if !strings.Contains(joined, want) { t.Fatalf("missing %q in %s", want, joined) }
	}
}
```

完整特效测试断言 filter graph 包含两个 layout crop、`scale`、`format=gray`、`alphamerge`，再执行用户 crop；参数中不能出现 `http://`、`https://`、shell quote wrapper 或音频 map。

- [ ] **Step 2: 写出进度和回退分类 RED 测试**

```go
func TestGiftClipProgressParserIsMonotonicAndClamped(t *testing.T) {
	parser := newGiftClipProgressParser(2 * time.Second)
	got := []float64{}
	for _, line := range []string{"out_time_us=500000", "out_time_us=400000", "out_time_us=2500000", "progress=end"} {
		if value, ok := parser.Consume(line); ok { got = append(got, value) }
	}
	if !reflect.DeepEqual(got, []float64{0.25, 0.25, 1, 1}) { t.Fatalf("progress = %#v", got) }
}
```

表驱动断言：hardware `exit status 1` + encoder initialization stderr 可回退；`context.Canceled`、`No space left on device`、`Invalid data found`、payload hash failure 不回退。

- [ ] **Step 3: 运行 RED 测试**

```powershell
Push-Location goserver
go test ./... -run "Test(BuildGiftClipFFmpegArgs|GiftClipProgress|ShouldRetryGiftClip)" -count=1
Pop-Location
```

- [ ] **Step 4: 实现两条 filter graph**

短动画核心 graph：

```go
dynamic := fmt.Sprintf("[0:v]setpts=PTS-STARTPTS,crop=%d:%d:%d:%d,format=rgba,fps=%d[anim]",
	request.Crop.Width, request.Crop.Height, request.Crop.X, request.Crop.Y, giftClipFPS)
compose := "[1:v]format=rgba[bg];[bg][anim]overlay=0:0:format=auto:shortest=1[mid];" +
	"[2:v]format=rgba[ol];[mid][ol]overlay=0:0:format=auto:shortest=1,fps=30,format=nv12[out]"
```

完整特效核心 graph：

```go
effect := fmt.Sprintf(
	"[0:v]split=2[rgb0][alpha0];"+
		"[rgb0]crop=%d:%d:%d:%d,scale=%d:%d[rg];"+
		"[alpha0]crop=%d:%d:%d:%d,scale=%d:%d,format=gray[a];"+
		"[rg][a]alphamerge,crop=%d:%d:%d:%d,setpts=PTS-STARTPTS,fps=30[anim]",
	rgbW, rgbH, rgbX, rgbY, visualW, visualH,
	alphaW, alphaH, alphaX, alphaY, visualW, visualH,
	request.Crop.Width, request.Crop.Height, request.Crop.X, request.Crop.Y,
)
```

动态素材在 input 前固定使用 `-stream_loop -1`；GIF/WebP 同时使用 `-ignore_loop 1`，由外层 stream loop 统一循环。静态 PNG inputs 固定使用例如
`-f image2 -loop 1 -framerate 30 -i C:\task\background.png` 的绝对路径参数；输出时长用不带 locale 的秒数字符串，并加例如
`-an -map [out] -t 2.2 -y C:\task\output.mp4`。

- [ ] **Step 5: 实现进度 parser 和错误分类**

只解析 `out_time_us=` 和 `progress=end`。`shouldRetryGiftClipSoftware()` 先排除 `context.Canceled`、磁盘空间、输入损坏和 `errGiftClipPayloadIntegrity`，其余硬件编码器启动/运行失败允许一次回退；调用者负责只调用一次。

- [ ] **Step 6: 运行 GREEN 与全量 Go 测试**

```powershell
Push-Location goserver
gofmt -w gift_clip_ffmpeg.go gift_clip_ffmpeg_test.go
go test ./... -count=1
Pop-Location
git diff --check
```

- [ ] **Step 7: 提交 Task 4**

```powershell
git add -- goserver/gift_clip_ffmpeg.go goserver/gift_clip_ffmpeg_test.go
git commit -m "feat: build deterministic gift clip ffmpeg commands"
```

---

### Task 5: 可复现的最小 FFmpeg payload 与本地缓存

**Files:**
- Create: `scripts/build-ffmpeg.ps1`
- Create: `scripts/package-ffmpeg.mjs`
- Create: `scripts/verify-ffmpeg.mjs`
- Create: `third_party/ffmpeg/configure.flags`
- Create: `third_party/ffmpeg/NOTICE.md`
- Create: `third_party/ffmpeg/COPYING.LGPLv2.1`
- Create: `goserver/ffmpeg/manifest.json`
- Create: `goserver/ffmpeg/ffmpeg.zip`
- Create: `goserver/gift_clip_payload_test.go`
- Create: `goserver/gift_clip_payload.go`
- Modify: `package.json`
- Modify: `scripts/build-go.mjs`

**Interfaces:**
- Produces:
  - `type giftClipFFmpegManifest struct { Version, SHA256 string; Size int64; Authenticode bool }`
  - `type giftClipPayload struct { Archive []byte; Manifest giftClipFFmpegManifest; CacheRoot string }`
  - `func embeddedGiftClipPayload(cacheRoot string) (*giftClipPayload, error)`
  - `func (payload *giftClipPayload) Prepare(context.Context) (string, error)` returning an absolute verified `ffmpeg.exe` path.
  - `func defaultGiftClipCacheRoot() string`，只返回当前应用专用 LocalAppData 子目录。

- [ ] **Step 1: 写出 payload 校验、原子重建和并发 RED 测试**

测试用一个小的 `MZ...fixture` byte slice创建单文件 ZIP 和匹配 manifest，不依赖仓库真实二进制：

```go
func TestGiftClipPayloadPrepareReusesOnlyHashVerifiedCache(t *testing.T) {
	root := t.TempDir()
	binary := []byte("MZ\x90\x00fixture-ffmpeg")
	payload := newTestGiftClipPayload(t, root, binary)

	first, err := payload.Prepare(context.Background())
	if err != nil { t.Fatal(err) }
	if !filepath.IsAbs(first) { t.Fatalf("path is not absolute: %s", first) }
	if err := os.WriteFile(first, []byte("corrupt"), 0o700); err != nil { t.Fatal(err) }
	second, err := payload.Prepare(context.Background())
	if err != nil { t.Fatal(err) }
	got, _ := os.ReadFile(second)
	if !bytes.Equal(got, binary) { t.Fatalf("cache was not rebuilt: %q", got) }
}
```

再用 16 个 goroutine 同时 `Prepare()`，断言都返回同一路径、没有 `.partial-*` 遗留。归档解压后 hash 与 manifest 不同必须返回 `errGiftClipPayloadIntegrity`，且不能留下可执行文件。

- [ ] **Step 2: 运行 RED 测试**

```powershell
Push-Location goserver
go test ./... -run "TestGiftClipPayload" -count=1
Pop-Location
```

- [ ] **Step 3: 实现 embed、校验和原子缓存**

```go
//go:embed ffmpeg/ffmpeg.zip ffmpeg/manifest.json
var giftClipFFmpegFS embed.FS

var errGiftClipPayloadIntegrity = errors.New("gift clip ffmpeg payload integrity failure")

func (payload *giftClipPayload) Prepare(ctx context.Context) (string, error) {
	payload.mu.Lock()
	defer payload.mu.Unlock()
	if err := ctx.Err(); err != nil { return "", err }
	targetDir := filepath.Join(payload.CacheRoot, payload.Manifest.Version+"-"+payload.Manifest.SHA256[:12])
	target := filepath.Join(targetDir, "ffmpeg.exe")
	if giftClipFileMatches(target, payload.Manifest) { return filepath.Abs(target) }
	return payload.extractAtomically(targetDir, target)
}
```

`extractAtomically()` 只接受 ZIP 中 basename 恰为 `ffmpeg.exe` 的一个普通文件，拒绝目录、绝对路径、`..`、symlink 和额外 entry；写入同目录随机 `.partial-*`，校验 size/hash 后 `Rename`。当前 manifest 之外的缓存目录不删除。

- [ ] **Step 4: 固定 FFmpeg 8.1.2 来源和最小 configure flags**

`scripts/build-ffmpeg.ps1` 必须下载 tarball、`.asc` 和 `ffmpeg-devel.asc`，验证 tarball SHA-256 和 GPG fingerprint，再从 MSYS2 UCRT64 shell 调用 configure/make。`third_party/ffmpeg/configure.flags` 内容固定为：

```text
--disable-autodetect
--disable-debug
--disable-doc
--disable-network
--disable-programs
--disable-everything
--enable-ffmpeg
--enable-avcodec
--enable-avfilter
--enable-avformat
--enable-swscale
--enable-mediafoundation
--enable-d3d11va
--enable-protocol=file,pipe
--enable-demuxer=gif,image_webp_pipe,mov,image2
--enable-decoder=gif,webp,png,h264
--enable-parser=h264
--enable-encoder=h264_mf
--enable-muxer=mp4
--enable-filter=crop,scale,format,split,alphamerge,overlay,fps,setpts
--enable-zlib
--enable-static
--disable-shared
--target-os=mingw32
--arch=x86_64
--extra-cflags=-Os
--extra-ldflags=-static
```

脚本在 configure 后检查 `config.h` 不含 `CONFIG_GPL 1` / `CONFIG_NONFREE 1`，确认 `CONFIG_MEDIAFOUNDATION 1`、`CONFIG_D3D11VA 1` 与 `CONFIG_H264_MF_ENCODER 1`，并在 Windows/MSYS2 下构建实际目标 `ffmpeg.exe`；随后保存 `ffmpeg -buildconf` 到 `dist/ffmpeg-build-config.txt`。`d3d11va` 只作为 FFmpeg 8.1.2 `h264_mf` 编译所需基础设施，不授权增加额外 codec/hwaccel。若实测完整特效 fixture 不是 H.264，停止本任务并回到规格评审；不能自行加入 HEVC/VP9。

- [ ] **Step 5: 实现签名后打包脚本和大小门限**

`scripts/package-ffmpeg.mjs` 读取 `--input`，计算 hash/size，并用 Node `deflateRawSync()` + 单 entry ZIP writer 生成归档；manifest 从实际文件生成，不手填 hash：

```js
const binary = await readFile(input);
const manifest = {
  version: '8.1.2',
  sha256: createHash('sha256').update(binary).digest('hex'),
  size: binary.length,
  authenticode: process.env.FFMPEG_AUTHENTICODE === 'true',
};
await writeFile(manifestPath, `${JSON.stringify(manifest, null, 2)}\n`);
await writeSingleFileZip(zipPath, 'ffmpeg.exe', binary);
```

ZIP `> 40_000_000` bytes 立即失败，`> 30_000_000` bytes 输出明确 warning。release 模式要求 `FFMPEG_AUTHENTICODE=true`；dev 模式允许 false，但 `scripts/build-go.mjs` 在 `APP_VERSION !== 'dev'` 时拒绝未签名 manifest。

- [ ] **Step 6: 加入构建与组件验证命令**

`package.json` 加：

```json
{
  "build:ffmpeg": "powershell -NoProfile -ExecutionPolicy Bypass -File scripts/build-ffmpeg.ps1",
  "package:ffmpeg": "node scripts/package-ffmpeg.mjs",
  "verify:ffmpeg": "node scripts/verify-ffmpeg.mjs"
}
```

`verify-ffmpeg.mjs` 对解压出的 CLI 运行 `-version`、`-buildconf`、`-protocols`、`-demuxers`、`-decoders`、`-encoders`、`-filters`、`-muxers`，断言：版本 8.1.2；无网络协议/GPL/nonfree；显式产品组件仅含本任务白名单，并允许 FFmpeg 为这些组件自动选择的必要基础设施组件；含实际命名为 `image_webp_pipe` 的 animated WebP demuxer 与 `h264_mf`。然后用 GIF/WebP/packed-alpha fixtures 运行短解码/合成 smoke test。自动选择项必须来自构建输出并在验证脚本中逐项记录/允许，不能借此扩大 codec、协议、网络、音频、字幕、GPL 或 nonfree 范围。

- [ ] **Step 7: 构建开发 payload 并运行 GREEN**

本地无签名 secret 时生成 `authenticode:false` 的 dev payload；真正 release 在 Task 13 先签内层再重新打包：

```powershell
npm run build:ffmpeg
$env:FFMPEG_AUTHENTICODE='false'
node scripts/package-ffmpeg.mjs --input dist/ffmpeg/ffmpeg.exe
Remove-Item Env:FFMPEG_AUTHENTICODE
npm run verify:ffmpeg
Push-Location goserver
gofmt -w gift_clip_payload.go gift_clip_payload_test.go
go test ./... -run "TestGiftClipPayload" -count=1
Pop-Location
npm run build:exe
git diff --check
```

Expected: 全部 exit 0；ZIP `<= 40,000,000` decimal bytes，本地 `APP_VERSION=dev` EXE 可构建。

- [ ] **Step 8: 提交 Task 5**

```powershell
git add -- package.json scripts/build-go.mjs scripts/build-ffmpeg.ps1 scripts/package-ffmpeg.mjs scripts/verify-ffmpeg.mjs third_party/ffmpeg goserver/ffmpeg goserver/gift_clip_payload.go goserver/gift_clip_payload_test.go
git commit -m "build: embed minimal ffmpeg runtime"
```

---

### Task 6: FFmpeg 执行、Windows Job Object 与单次软件回退

**Files:**
- Create: `goserver/gift_clip_process_test.go`
- Create: `goserver/gift_clip_process_windows.go`
- Create: `goserver/gift_clip_process_other.go`
- Modify: `goserver/gift_clip_ffmpeg.go`
- Modify: `goserver/gift_clip_ffmpeg_test.go`

**Interfaces:**
- Consumes: `giftClipPayload.Prepare()`、`buildGiftClipFFmpegArgs()`、`giftClipProgressParser`。
- Produces:
  - `type giftClipEncodingUpdate struct { Progress float64; Mode giftClipEncoderMode; Retrying bool }`
  - `type giftClipEncoder interface { Encode(context.Context, giftClipEncodeRequest, func(giftClipEncodingUpdate)) error }`
  - `type giftClipProcessRunner interface { Run(context.Context, string, []string, io.Writer, io.Writer) error }`
  - `type giftClipFFmpegEncoderOptions struct { ForceSoftware bool }`
  - `func newGiftClipFFmpegEncoder(*giftClipPayload, giftClipProcessRunner, *diagnosticLogger, giftClipFFmpegEncoderOptions) giftClipEncoder`

- [ ] **Step 1: 写出回退、缓存和取消 RED 测试**

fake runner 记录 mode 参数，并在第一次返回硬件初始化 stderr：

```go
func TestGiftClipEncoderRetriesSoftwareOnceAndCachesTheDecision(t *testing.T) {
	runner := &fakeGiftClipRunner{results: []fakeRunResult{
		{stderr: "Error initializing h264_mf hardware encoder", err: errors.New("exit 1")},
		{}, {},
	}}
	encoder := newGiftClipFFmpegEncoder(testPayload(t), runner, nil, giftClipFFmpegEncoderOptions{})
	updates := []giftClipEncodingUpdate{}
	if err := encoder.Encode(context.Background(), giftClipEncodeFixture(), func(update giftClipEncodingUpdate) { updates = append(updates, update) }); err != nil { t.Fatal(err) }
	if err := encoder.Encode(context.Background(), giftClipEncodeFixture(), nil); err != nil { t.Fatal(err) }
	if got := runner.hardwareFlags(); !reflect.DeepEqual(got, []string{"1", "0", "0"}) { t.Fatalf("flags = %#v", got) }
	if !slices.ContainsFunc(updates, func(update giftClipEncodingUpdate) bool { return update.Retrying && update.Mode == giftClipEncoderSoftware }) {
		t.Fatalf("updates = %#v", updates)
	}
}
```

另写用例：context cancel 只运行一次；`ENOSPC`/坏输入只运行一次；软件失败不进行第三次；stdout 的 `out_time_us` 触发单调 update。

- [ ] **Step 2: 写出 Windows 进程树 RED 测试**

在 `//go:build windows` 测试中启动一个会再启动子进程的 helper test process，把 context 取消后轮询两个 PID；必须都无法通过 `OpenProcess(PROCESS_QUERY_LIMITED_INFORMATION)` 打开。测试只终止自身创建并记录的 PID。

- [ ] **Step 3: 运行 RED 测试**

```powershell
Push-Location goserver
go test ./... -run "TestGiftClip(Encoder|WindowsProcess)" -count=1
Pop-Location
```

- [ ] **Step 4: 实现 encoder 两次尝试和会话缓存**

```go
func (encoder *giftClipFFmpegEncoder) Encode(ctx context.Context, request giftClipEncodeRequest, notify func(giftClipEncodingUpdate)) error {
	path, err := encoder.payload.Prepare(ctx)
	if err != nil { return err }
	mode := encoder.initialMode()
	err, stderr := encoder.runAttempt(ctx, path, request, mode, notify)
	if err == nil { return nil }
	if mode != giftClipEncoderHardware || !shouldRetryGiftClipSoftware(err, stderr) { return err }
	encoder.rememberSoftwareMode()
	if notify != nil { notify(giftClipEncodingUpdate{Mode: giftClipEncoderSoftware, Retrying: true}) }
	err, _ = encoder.runAttempt(ctx, path, request, giftClipEncoderSoftware, notify)
	return err
}
```

`ForceSoftware` 为 true 时初始 mode 直接为 software，用于 CI 和确定性 E2E；默认 false。
`runAttempt()` 使用 `bufio.Scanner` 解析 stdout progress；stderr 只保留最多 32 KiB 尾部并写入清洗后的 diagnostic event，不记录完整绝对路径。

- [ ] **Step 5: 实现 Windows Job Object runner**

用 `kernel32.dll` 的 `CreateJobObjectW`、`SetInformationJobObject`、`AssignProcessToJobObject` 和 `TerminateJobObject`：

```go
limit := jobObjectExtendedLimitInformation{}
limit.BasicLimitInformation.LimitFlags = jobObjectLimitKillOnJobClose
// SetInformationJobObject(JobObjectExtendedLimitInformation, &limit)
```

`exec.Command(path, args...)` 的 `path` 必须先经 `filepath.Abs()`；启动后立即按 PID `OpenProcess` 并 assign 到 job。context cancel 调用 `TerminateJobObject`，随后 `Wait()`，最后关闭 process/job handles。非 Windows runner 使用 `exec.CommandContext` 且 `newGiftClipFFmpegEncoder` 默认软件模式。

- [ ] **Step 6: 运行 GREEN、race 和全量 Go 回归**

```powershell
Push-Location goserver
gofmt -w gift_clip_ffmpeg.go gift_clip_ffmpeg_test.go gift_clip_process_test.go gift_clip_process_windows.go gift_clip_process_other.go
go test ./... -count=1
go test -race ./... -run "TestGiftClipEncoder" -count=1
Pop-Location
git diff --check
```

- [ ] **Step 7: 提交 Task 6**

```powershell
git add -- goserver/gift_clip_ffmpeg.go goserver/gift_clip_ffmpeg_test.go goserver/gift_clip_process_test.go goserver/gift_clip_process_windows.go goserver/gift_clip_process_other.go
git commit -m "feat: run gift clip ffmpeg with safe fallback"
```

---

### Task 7: 单并发任务队列、取消和清理

**Files:**
- Create: `goserver/gift_clip_jobs_test.go`
- Create: `goserver/gift_clip_jobs.go`

**Interfaces:**
- Consumes: `giftClipSourceResolver`、`giftClipEncoder`、`newGiftClipOutputProfile()`。
- Produces:
  - `type giftClipJobState string` with `queued`, `encoding`, `retrying`, `ready`, `failed`, `cancelled`
  - `type giftClipJobSnapshot struct { ID string; State giftClipJobState; Progress float64; Message string; Width, Height, FPS int }`
  - `func newGiftClipJobManager(root string, resolver giftClipSourceResolver, encoder giftClipEncoder, logger *diagnosticLogger) *giftClipJobManager`
  - methods `Create(ctx, receiptID string, crop giftClipCrop, background, overlay []byte) (giftClipJobSnapshot, error)`, `Snapshot(id string)`, `VideoPath(id string)`, `Cancel(id string)`, `Close()`.
  - `func defaultGiftClipTaskRoot() string`，只返回当前应用专用 LocalAppData 子目录。

- [ ] **Step 1: 写出 FIFO 单并发 RED 测试**

blocking fake encoder 用 channel 记录开始顺序：

```go
first, err := manager.Create(context.Background(), "receipt-1", crop, pngA, pngB)
if err != nil { t.Fatal(err) }
second, err := manager.Create(context.Background(), "receipt-2", crop, pngA, pngB)
if err != nil { t.Fatal(err) }
if got := <-encoder.started; got != first.ID { t.Fatalf("first started = %s", got) }
assertNoValue(t, encoder.started, 100*time.Millisecond)
encoder.finish <- nil
if got := <-encoder.started; got != second.ID { t.Fatalf("second started = %s", got) }
```

- [ ] **Step 2: 写出状态、取消、TTL 与退出清理 RED 测试**

覆盖：progress 单调；retrying message 为“已切换兼容编码模式。”；queued cancel 不启动 encoder；running cancel 收到 context cancellation；ready `VideoPath()` 有效；close 取消全部任务。注入 fake clock 后把 ready 推进 30 分钟、failed/cancelled 推进 5 分钟并调用 `Sweep()`，断言任务目录删除。

- [ ] **Step 3: 运行 RED 测试**

```powershell
Push-Location goserver
go test ./... -run "TestGiftClipJob" -count=1
Pop-Location
```

- [ ] **Step 4: 实现有界任务对象和单 worker**

```go
const (
	giftClipReadyTTL = 30 * time.Minute
	giftClipTerminalTTL = 5 * time.Minute
)

type giftClipJob struct {
	id string
	state giftClipJobState
	progress float64
	message string
	dir string
	outputPath string
	cancel context.CancelFunc
	finishedAt time.Time
}
```

manager 用 mutex 保护 map/queue；只启动一个 worker goroutine。ID 使用 `crypto/rand` 18 bytes + base64url。`Create()` 只使用请求 context 完成输入接收；排队后派生 manager 自己的 context，HTTP 请求结束不得取消任务。它在专用 root 下创建目录，校验两张 PNG 的 decode config 均等于 crop 宽高且像素数不超过 16,777,216，再原子写入。background 必须是 opaque PNG，overlay 必须允许 alpha。

- [ ] **Step 5: 实现状态映射和可恢复清理**

encoding update 只允许 progress 增长；retrying 可把 progress 重置为 0 并设置固定提示。失败把内部错误映射为规格中的稳定中文消息，日志只记录 task ID、阶段、mode、exit class。`Cancel()` 幂等；`Close()` 停止 sweeper、取消 worker、等待 goroutine，然后只删除 manager 本次创建的任务目录。

- [ ] **Step 6: 运行 GREEN、race 和全量 Go 回归**

```powershell
Push-Location goserver
gofmt -w gift_clip_jobs.go gift_clip_jobs_test.go
go test ./... -count=1
go test -race ./... -run "TestGiftClipJob" -count=1
Pop-Location
git diff --check
```

- [ ] **Step 7: 提交 Task 7**

```powershell
git add -- goserver/gift_clip_jobs.go goserver/gift_clip_jobs_test.go
git commit -m "feat: queue gift clip export jobs"
```

---

### Task 8: 礼物剪辑 HTTP API 与主程序生命周期

**Files:**
- Create: `goserver/gift_clip_http_test.go`
- Create: `goserver/gift_clip_http.go`
- Modify: `goserver/main.go`
- Modify: `goserver/main_test.go`

**Interfaces:**
- Consumes: Task 7 `giftClipJobManager` methods。
- Produces: `POST /api/gift-clips`、`GET /api/gift-clips/{id}`、`GET /api/gift-clips/{id}/video`、`DELETE /api/gift-clips/{id}`。

- [ ] **Step 1: 写出 multipart 创建与状态 RED 测试**

构造 `metadata`、`background`、`overlay` 三个 part：

```go
metadata := `{"receiptId":"receipt-1","crop":{"x":100,"y":50,"width":960,"height":540},"version":1}`
request := newGiftClipMultipartRequest(t, metadata, validPNG(t, 960, 540, false), validPNG(t, 960, 540, true))
response := httptest.NewRecorder()
api.ServeHTTP(response, request)
if response.Code != http.StatusAccepted { t.Fatalf("status = %d body=%s", response.Code, response.Body.String()) }
```

断言 JSON 含 opaque id、`queued`、960×540、30 FPS。GET 状态反映 manager snapshot。

- [ ] **Step 2: 写出安全、视频与取消 RED 测试**

覆盖：非 POST；cross-site Origin/Sec-Fetch-Site；body 超过 32 MiB；缺 part；重复 part；坏 PNG；尺寸不符；奇数尺寸；额外 JSON 字段；猜测 ID；非 ready 视频；ready MP4 的 `Content-Type: video/mp4`、`Cache-Control: no-store`、安全 `Content-Disposition`；DELETE 幂等。

- [ ] **Step 3: 运行 RED 测试**

```powershell
Push-Location goserver
go test ./... -run "TestGiftClipHTTP" -count=1
Pop-Location
```

- [ ] **Step 4: 实现严格 router 和 multipart parser**

```go
const maxGiftClipRequestBytes int64 = 32 << 20

type giftClipCreateMetadata struct {
	ReceiptID string       `json:"receiptId"`
	Crop      giftClipCrop `json:"crop"`
	Version   int          `json:"version"`
}
```

JSON decoder 调用 `DisallowUnknownFields()`；version 只接受 1。用 `http.MaxBytesReader` 和 `multipart.Reader` 顺序读取 part，但按 name 放入 map并拒绝重复/未知 part。所有 mutation 复用 same-origin 检查。path router 对 ID 做 base64url 字符集和固定长度校验，不把文件路径拼自 URL。

- [ ] **Step 5: 注册依赖并绑定退出**

`main()` 在 config store/diagnostics 就绪后创建：

```go
giftMedia := newGiftReceiptAPI(store, nil)
giftPayload, err := embeddedGiftClipPayload(defaultGiftClipCacheRoot())
if err != nil { showStartupError(err.Error()); return }
giftEncoder := newGiftClipFFmpegEncoder(giftPayload, newGiftClipProcessRunner(), diagnostics, giftClipFFmpegEncoderOptions{})
giftClips := newGiftClipJobManager(defaultGiftClipTaskRoot(), newGiftClipSourceResolver(store, giftMedia), giftEncoder, diagnostics)
defer giftClips.Close()
```

路由：

```go
mux.Handle("/api/gift-clips", newGiftClipHTTPHandler(giftClips))
mux.Handle("/api/gift-clips/", newGiftClipHTTPHandler(giftClips))
```

现有 `/api/gift-receipts/media` 继续使用同一个 `giftMedia`。应用退出顺序为：先取消 runtime，再 `giftClips.Close()`，再关闭 HTTP server；`Close()` 必须在 updater 安装前完成。

- [ ] **Step 6: 运行 GREEN、race 与全量 Go 回归**

```powershell
Push-Location goserver
gofmt -w gift_clip_http.go gift_clip_http_test.go main.go main_test.go
go test ./... -count=1
go test -race ./... -run "TestGiftClipHTTP" -count=1
Pop-Location
git diff --check
```

- [ ] **Step 7: 提交 Task 8**

```powershell
git add -- goserver/gift_clip_http.go goserver/gift_clip_http_test.go goserver/main.go goserver/main_test.go
git commit -m "feat: expose gift clip export jobs"
```

---

### Task 9: 前端导出任务 API 与可取消轮询

**Files:**
- Create: `tests/gift-clip-export-api.test.ts`
- Create: `src/ui/config/gift-clip-export-api.ts`

**Interfaces:**
- Produces:
  - `type GiftClipJobState = 'queued' | 'encoding' | 'retrying' | 'ready' | 'failed' | 'cancelled'`
  - `interface GiftClipJobSnapshot { id: string; state: GiftClipJobState; progress: number; message?: string; output: { width: number; height: number; fps: 30 } }`
  - `createGiftClipJob(input, signal): Promise<GiftClipJobSnapshot>`
  - `getGiftClipJob(id, signal): Promise<GiftClipJobSnapshot>`
  - `cancelGiftClipJob(id): Promise<void>`
  - `giftClipJobVideoURL(id): string`
  - `waitForGiftClipJob(id, options): Promise<GiftClipJobSnapshot>`

- [ ] **Step 1: 写出 multipart 与响应校验 RED 测试**

```ts
it('creates a versioned multipart export without sending media URLs', async () => {
  const fetchMock = vi.fn(async () => Response.json({
    id: 'job_abc', state: 'queued', progress: 0,
    output: { width: 960, height: 540, fps: 30 },
  }, { status: 202 }));
  vi.stubGlobal('fetch', fetchMock);

  const snapshot = await createGiftClipJob({
    receiptId: 'receipt-1',
    crop: { x: 101, y: 53, width: 960, height: 540 },
    background: new Blob(['bg'], { type: 'image/png' }),
    overlay: new Blob(['overlay'], { type: 'image/png' }),
  });

  const [url, init] = fetchMock.mock.calls[0];
  expect(url).toBe('/api/gift-clips');
  expect(init?.method).toBe('POST');
  expect(init?.body).toBeInstanceOf(FormData);
  expect(JSON.stringify(await snapshot)).not.toContain('hdslb.com');
});
```

响应 state、progress、output 尺寸或 fps 不合法时必须拒绝稳定错误，不把任意后端 payload 当成功。

- [ ] **Step 2: 写出轮询、兼容提示、Abort 和 DELETE RED 测试**

用注入 `sleep: () => Promise.resolve()` 依次返回 queued、encoding、retrying、ready，断言每次编码尝试内 progress 单调；进入 retrying 时允许从已有值归零，并传递“已切换兼容编码模式。”。AbortSignal 触发后不得再 GET；`cancelGiftClipJob()` 使用 DELETE，404/410 视为已清理成功。

- [ ] **Step 3: 运行 RED 测试**

```powershell
npx vitest run tests/gift-clip-export-api.test.ts
```

- [ ] **Step 4: 实现严格 API client**

```ts
export async function createGiftClipJob(input: GiftClipCreateInput, signal?: AbortSignal): Promise<GiftClipJobSnapshot> {
  const form = new FormData();
  form.set('metadata', JSON.stringify({ receiptId: input.receiptId, crop: input.crop, version: 1 }));
  form.set('background', input.background, 'background.png');
  form.set('overlay', input.overlay, 'overlay.png');
  return requestGiftClipJob('/api/gift-clips', { method: 'POST', body: form, signal }, 202);
}

export function giftClipJobVideoURL(id: string): string {
  return `/api/gift-clips/${encodeURIComponent(id)}/video`;
}
```

`waitForGiftClipJob()` 默认每 250 ms 查询；ready resolve；failed/cancelled throw snapshot message；AbortError 原样传播。禁止设置 multipart `Content-Type`，让浏览器生成 boundary。

- [ ] **Step 5: 运行 GREEN 与全量前端回归**

```powershell
npx vitest run tests/gift-clip-export-api.test.ts tests/backend.test.ts
npm run typecheck
npm test -- --reporter=dot
git diff --check
```

- [ ] **Step 6: 提交 Task 9**

```powershell
git add -- src/ui/config/gift-clip-export-api.ts tests/gift-clip-export-api.test.ts
git commit -m "feat: add gift clip export client"
```

---

### Task 10: 静态背景与信息栏 PNG 图层

**Files:**
- Create: `tests/gift-clip-export-layers.test.ts`
- Create: `src/ui/config/gift-clip-export-layers.ts`
- Modify: `src/ui/config/gift-clip-renderer.ts`
- Modify: `tests/gift-clip-renderer.test.ts`

**Interfaces:**
- Consumes: `GiftReceipt`、`GiftClipPixelRect`、`GIFT_CLIP_INFO_BAR_DESIGN`。
- Produces:
  - `drawGiftClipBackground(context, width, height): void`
  - `drawGiftClipInfoOverlay(context, receipt, avatar, width, height): void`
  - `createGiftClipExportLayers(options): Promise<{ background: Blob; overlay: Blob }>`

- [ ] **Step 1: 写出 renderer 分层 RED 测试**

扩展 fake 2D context 的调用记录：

```ts
it('draws background and information overlay as independent layers', () => {
  const background = recordingContext(960, 540);
  drawGiftClipBackground(background.context, 960, 540);
  expect(background.calls.some((call) => call.name === 'fillRect')).toBe(true);
  expect(background.calls.some((call) => call.name === 'fillText')).toBe(false);

  const overlay = recordingContext(960, 540);
  drawGiftClipInfoOverlay(overlay.context, receiptFixture(), null, 960, 540);
  expect(overlay.calls.some((call) => call.name === 'clearRect')).toBe(true);
  expect(overlay.calls.some((call) => call.name === 'fillText')).toBe(true);
  expect(overlay.calls.filter((call) => call.name === 'createLinearGradient')).toHaveLength(1);
});
```

现有 `drawGiftClipOutputFrame()` 测试继续通过，证明视觉组合未变。

- [ ] **Step 2: 写出 PNG 生成、尺寸和失败 RED 测试**

注入 fake document/canvas，记录 `width`、`height`、首次 canvas 是否绘制背景，第二个是否保留透明 clear；`toBlob()` 返回 null 时拒绝“视频图层生成失败，请重试。”。

```ts
await expect(createGiftClipExportLayers({
  width: 960, height: 540, receipt: receiptFixture(), avatar: null, document: fakeDocument,
})).resolves.toEqual({ background: expect.any(Blob), overlay: expect.any(Blob) });
expect(createdCanvases.map(({ width, height }) => [width, height])).toEqual([[960, 540], [960, 540]]);
```

- [ ] **Step 3: 运行 RED 测试**

```powershell
npx vitest run tests/gift-clip-renderer.test.ts tests/gift-clip-export-layers.test.ts
```

- [ ] **Step 4: 提取绘图函数并实现 Canvas-to-PNG**

```ts
function canvasPNG(canvas: HTMLCanvasElement): Promise<Blob> {
  return new Promise((resolve, reject) => {
    canvas.toBlob((blob) => {
      if (blob?.type === 'image/png' && blob.size > 0) resolve(blob);
      else reject(new Error('视频图层生成失败，请重试。'));
    }, 'image/png');
  });
}

export async function createGiftClipExportLayers(options: GiftClipExportLayerOptions) {
  const backgroundCanvas = options.document.createElement('canvas');
  const overlayCanvas = options.document.createElement('canvas');
  for (const canvas of [backgroundCanvas, overlayCanvas]) {
    canvas.width = options.width;
    canvas.height = options.height;
  }
  drawGiftClipBackground(requireContext(backgroundCanvas), options.width, options.height);
  drawGiftClipInfoOverlay(requireContext(overlayCanvas), options.receipt, options.avatar, options.width, options.height);
  const [background, overlay] = await Promise.all([canvasPNG(backgroundCanvas), canvasPNG(overlayCanvas)]);
  backgroundCanvas.width = overlayCanvas.width = 0;
  backgroundCanvas.height = overlayCanvas.height = 0;
  return { background, overlay };
}
```

使用 `try/finally` 保证失败时也释放 backing stores。

- [ ] **Step 5: 运行 GREEN 与视觉相邻回归**

```powershell
npx vitest run tests/gift-clip-renderer.test.ts tests/gift-clip-export-layers.test.ts tests/gift-clip-studio.test.ts
npm run typecheck
git diff --check
```

- [ ] **Step 6: 提交 Task 10**

```powershell
git add -- src/ui/config/gift-clip-renderer.ts src/ui/config/gift-clip-export-layers.ts tests/gift-clip-renderer.test.ts tests/gift-clip-export-layers.test.ts
git commit -m "refactor: split gift clip export layers"
```

---

### Task 11: 拆分 Studio façade、controller 与 DOM view

**Files:**
- Create: `src/ui/config/gift-clip-animation-key.ts`
- Create: `src/ui/config/gift-clip-studio-controller.ts`
- Create: `src/ui/config/gift-clip-studio-view.ts`
- Modify: `src/ui/config/gift-clip-studio.ts`
- Modify: `tests/gift-clip-studio.test.ts`
- Create: `tests/gift-clip-studio-view.test.ts`

**Interfaces:**
- Produces stable façade exports from `gift-clip-studio.ts`:
  - `giftClipAnimationKey(receipt): string`
  - `openGiftClipStudio(options): GiftClipStudioController`
  - `GiftClipStudioController { close(): void }`
- Internal `createGiftClipStudioView(host, receipt): GiftClipStudioView` owns DOM creation/rendering only.

- [ ] **Step 1: 写出 façade 体积和 re-export RED 测试**

```ts
it('keeps the public studio module as a small stable facade', () => {
  const source = readFileSync(new URL('../src/ui/config/gift-clip-studio.ts', import.meta.url), 'utf8');
  expect(source.split(/\r?\n/).length).toBeLessThanOrEqual(20);
  expect(source).toContain("from './gift-clip-animation-key'");
  expect(source).toContain("from './gift-clip-studio-controller'");
});
```

保留所有既有 `giftClipAnimationKey()` 和 `openGiftClipStudio()` 行为测试，不改 import path。

- [ ] **Step 2: 写出 view DOM/状态 RED 测试**

对 `createGiftClipStudioView()` 断言：圆角 dialog class、直角 stage class、source canvas、临时 recording canvas、video、progress、status、四个 action button；`showEncoding()`、`showReady()`、`showFailure()` 只改变显示/禁用/text，不加载媒体、不 fetch、不注册 global listener。

- [ ] **Step 3: 运行 RED 测试**

```powershell
npx vitest run tests/gift-clip-studio.test.ts tests/gift-clip-studio-view.test.ts
```

- [ ] **Step 4: 移动 key 和 controller，不改变行为**

`gift-clip-studio.ts` 最终只包含：

```ts
export { giftClipAnimationKey } from './gift-clip-animation-key';
export { openGiftClipStudio } from './gift-clip-studio-controller';
export type { GiftClipStudioController } from './gift-clip-studio-controller';
```

把现有 hash 函数原样移动到 `gift-clip-animation-key.ts`，把 controller 逻辑移动到 `gift-clip-studio-controller.ts`。不要在此任务替换 MediaRecorder；这是结构性等价提交。

- [ ] **Step 5: 抽取纯 DOM view**

`GiftClipStudioView` 暴露 controller 需要的元素和状态函数：

```ts
export interface GiftClipStudioView {
  readonly overlay: HTMLElement;
  readonly stage: HTMLElement;
  readonly sourceCanvas: HTMLCanvasElement;
  readonly recordingCanvas: HTMLCanvasElement;
  readonly preview: HTMLVideoElement;
  readonly sourceMediaHost: HTMLElement;
  readonly closeButton: HTMLButtonElement;
  readonly resetButton: HTMLButtonElement;
  readonly confirmButton: HTMLButtonElement;
  readonly reeditButton: HTMLButtonElement;
  readonly saveButton: HTMLButtonElement;
  setStageSize(width: number, height: number): void;
  showLoading(): void;
  showEditing(message: string): void;
  showEncoding(message: string, progress: number): void;
  showReady(message: string, saveLabel: string): void;
  showFailure(message: string, retryLabel: string): void;
  destroy(): void;
}
```

`recordingCanvas` 只为 Task 11 的结构性等价迁移临时保留；Task 12 删除 MediaRecorder 时一并从 view 移除。
view 不拥有 AbortController、media session、crop、job ID 或 preview URL。controller 绑定 button callbacks，并在 close 时清空。

- [ ] **Step 6: 运行 GREEN、全量前端和 build 回归**

```powershell
npx vitest run tests/gift-clip-studio.test.ts tests/gift-clip-studio-view.test.ts tests/config-gift-history.test.ts
npm run typecheck
npm test -- --reporter=dot
npm run build:ui
git diff --check
```

- [ ] **Step 7: 提交 Task 11**

```powershell
git add -- src/ui/config/gift-clip-animation-key.ts src/ui/config/gift-clip-studio-controller.ts src/ui/config/gift-clip-studio-view.ts src/ui/config/gift-clip-studio.ts tests/gift-clip-studio.test.ts tests/gift-clip-studio-view.test.ts
git commit -m "refactor: split gift clip studio modules"
```

---

### Task 12: Studio 接入后端导出并删除 MediaRecorder

**Files:**
- Create: `src/ui/config/gift-clip-download.ts`
- Create: `tests/gift-clip-download.test.ts`
- Modify: `src/ui/config/gift-clip-studio-controller.ts`
- Modify: `src/ui/config/gift-clip-studio-view.ts`
- Modify: `tests/gift-clip-studio.test.ts`
- Delete: `src/ui/config/gift-clip-recorder.ts`
- Delete: `tests/gift-clip-recorder.test.ts`

**Interfaces:**
- Consumes: Task 9 export API、Task 10 layer generator、Task 11 view。
- Produces: `openGiftClipStudio()` 的用户流程改为 async FFmpeg job；保存链接固定 `.mp4`；关闭/重新剪裁/重试都会 DELETE 当前 job。

- [ ] **Step 1: 把 studio mocks 从 recorder 改成 export API/layers 并写 RED**

```ts
vi.mock('../src/ui/config/gift-clip-export-api', () => ({
  createGiftClipJob: studioMocks.createJob,
  waitForGiftClipJob: studioMocks.waitForJob,
  cancelGiftClipJob: studioMocks.cancelJob,
  giftClipJobVideoURL: (id: string) => `/api/gift-clips/${id}/video`,
}));
vi.mock('../src/ui/config/gift-clip-export-layers', () => ({
  createGiftClipExportLayers: studioMocks.createLayers,
}));
```

点击确认后断言 `createLayers({width:640,height:360,...})`、`createJob({receiptId,crop,background,overlay})`，progress UI 接收 queued→encoding→retrying→ready，预览 src 为 `/api/gift-clips/job-1/video`，保存按钮为“保存 MP4”。

- [ ] **Step 2: 写出取消、重新剪裁、失败和 stale completion RED 测试**

覆盖：close 在 create/poll 任意阶段 abort 并 DELETE；ready 后 re-edit 先 DELETE 再复用现有 media session；retrying 显示“已切换兼容编码模式。”；旧 job 在 transition token 改变后 resolve 不得覆盖新 UI；failed 后“重试”重新加载源；`onCropConfirmed` 仍只在确认时调用一次。

- [ ] **Step 3: 运行 RED 测试**

```powershell
npx vitest run tests/gift-clip-studio.test.ts tests/gift-clip-download.test.ts
```

- [ ] **Step 4: 移动文件名和下载 helper**

从 recorder 移动并收窄：

```ts
export function sanitizeGiftClipFilename(
  receipt: Pick<GiftReceipt, 'giftName' | 'uname' | 'time'>,
): string {
  // 保留现有字符清洗和时间格式，只固定 `${safe}-${date}.mp4`。
}

export function triggerGiftClipDownload(url: string, filename: string, targetDocument: Document = document): void {
  const link = targetDocument.createElement('a');
  link.href = url;
  link.download = filename;
  targetDocument.body.append(link);
  link.click();
  link.remove();
}
```

把原 recorder 文件里的清洗测试原样迁移并把 extension 期望固定为 MP4。

- [ ] **Step 5: 实现 job 生命周期协调**

controller 维护：

```ts
let exportAbort: AbortController | null = null;
let exportJobId = '';
let exportTask: Promise<void> | null = null;

const cancelExport = async (): Promise<void> => {
  exportAbort?.abort();
  exportAbort = null;
  const id = exportJobId;
  exportJobId = '';
  if (id) await cancelGiftClipJob(id).catch(() => undefined);
};
```

确认流程先 `giftClipCropToPixels()`，调用 `onCropConfirmed`，停止浏览器预览，生成静态 PNG，再 POST/轮询。progress callback 只在 transition token 当前时更新 view。ready 后设置 `preview.src` 和 aspect ratio；不创建 Blob URL、不保留 recording canvas。

- [ ] **Step 6: 删除 MediaRecorder surface 并验证源码扫描**

删除 recorder 文件/测试和 view 的 `.gift-clip-recording-canvas`。新增回归：

```ts
for (const path of ['gift-clip-studio-controller.ts', 'gift-clip-studio-view.ts']) {
  const source = readFileSync(new URL(`../src/ui/config/${path}`, import.meta.url), 'utf8');
  expect(source).not.toMatch(/MediaRecorder|captureStream|requestAnimationFrame\(draw.*record/i);
}
```

浏览器编辑预览仍可用 `requestAnimationFrame`；该扫描只禁止实时录制路径。

- [ ] **Step 7: 运行 GREEN 与全量前端验证**

```powershell
npx vitest run tests/gift-clip-studio.test.ts tests/gift-clip-studio-view.test.ts tests/gift-clip-download.test.ts tests/gift-clip-export-api.test.ts tests/gift-clip-export-layers.test.ts
npm run typecheck
npm test -- --reporter=dot
npm run build:ui
git diff --check
```

- [ ] **Step 8: 提交 Task 12**

```powershell
git add -- src/ui/config/gift-clip-studio-controller.ts src/ui/config/gift-clip-studio-view.ts src/ui/config/gift-clip-download.ts tests/gift-clip-studio.test.ts tests/gift-clip-download.test.ts
git rm -- src/ui/config/gift-clip-recorder.ts tests/gift-clip-recorder.test.ts
git commit -m "feat: export gift clips through ffmpeg jobs"
```

---

### Task 13: 真实素材、卡顿回归、构建许可与 Release 流程

**Files:**
- Create: `scripts/generate-gift-clip-fixtures.ps1`
- Create: `scripts/verify-gift-clip-export.mjs`
- Add: `scripts/diagnose-gift-clip-stutter.mjs`
- Add: `tests/fixtures/gift-clip-stutter.html`
- Create: `tests/fixtures/gift-clip-export.html`
- Add: `tests/fixtures/gift-clip-media/input-10fps.gif`
- Add: `tests/fixtures/gift-clip-media/input-20fps.webp`
- Add: `tests/fixtures/gift-clip-media/packed-alpha-24fps.mp4`
- Add: `tests/fixtures/gift-clip-media/packed-alpha-layout.json`
- Create: `goserver/gift_clip_e2e_test.go`
- Modify: `vite.config.ts`
- Modify: `.github/workflows/release.yml`
- Modify: `README.md`
- Modify: `package.json`

**Interfaces:**
- Consumes: 完整 HTTP handler、真实最小 FFmpeg payload 和前端 Studio。
- Produces: 可重复的 GIF/WebP/packed-alpha 验收、180 ms UI stall 帧序列回归、双层签名 release pipeline 和 LGPL release assets。

- [ ] **Step 1: 写 fixture 生成脚本并生成三类输入**

`generate-gift-clip-fixtures.ps1` 要求 `FFMPEG_FULL_BIN` 或使用已知本地 full FFmpeg，执行固定命令：

```powershell
$ffmpeg = @($env:FFMPEG_FULL_BIN, 'D:\Program Files\ffmpeg\bin\ffmpeg.exe') |
  Where-Object { $_ -and (Test-Path -LiteralPath $_) } |
  Select-Object -First 1
if (-not $ffmpeg) { throw 'Set FFMPEG_FULL_BIN to a full FFmpeg executable.' }
& $ffmpeg -f lavfi -i 'testsrc2=size=320x180:rate=10:duration=2' -loop 0 -y tests/fixtures/gift-clip-media/input-10fps.gif
& $ffmpeg -f lavfi -i 'testsrc2=size=320x180:rate=20:duration=2' -c:v libwebp_anim -loop 0 -y tests/fixtures/gift-clip-media/input-20fps.webp
& $ffmpeg -f lavfi -i 'testsrc2=size=320x180:rate=24:duration=2' -f lavfi -i 'testsrc2=size=320x180:rate=24:duration=2' -filter_complex '[0:v][1:v]hstack=inputs=2,format=yuv420p' -c:v libx264 -an -y tests/fixtures/gift-clip-media/packed-alpha-24fps.mp4
```

layout 固定为：

```json
{"videoWidth":640,"videoHeight":180,"rgbFrame":[0,0,320,180],"alphaFrame":[320,0,320,180],"fps":24,"frames":48}
```

生成脚本记录 full FFmpeg version；fixtures 均小于 1 MiB。若 full FFmpeg 缺 `libwebp_anim`/`libx264`，脚本明确失败，不改用在线素材。

- [ ] **Step 2: 写真实 encoder RED/验收测试**

`gift_clip_e2e_test.go` 仅在 `GIFT_CLIP_FFMPEG_E2E=1` 时运行；先写测试引用尚不存在的
`newGiftClipHarnessServer(root, resolver, encoder)`，并使用 `embeddedGiftClipPayload(t.TempDir())`、
`giftClipFFmpegEncoderOptions{ForceSoftware: true}` 对三类 fixture 编码。每个输出通过 `FFPROBE_BIN -v error -show_streams -show_format -of json` 断言 codec `h264`、30 FPS、尺寸、时长、无 audio；码率允许目标上下 35% 内容相关浮动。

再用 full `ffmpeg -i output -f framemd5 -` 取得解码帧 hash，断言 10 FPS 输入在 30 FPS 输出中按时间重复、20/24 FPS 按 timestamp 采样且总帧数为 60。

- [ ] **Step 3: 写浏览器 stall 对比验收**

先运行 Step 2 的 Go 测试，Expected: FAIL，`newGiftClipHarnessServer` 尚不存在。随后实现
`newGiftClipHarnessServer(root, resolver, encoder) (*http.Server, net.Listener, error)`。
`TestGiftClipHarnessServer` 在设置 `GIFT_CLIP_HARNESS_PORT_FILE` 时启动真实 handler、fixture resolver 和软件 encoder，并只绑定 `127.0.0.1` 动态端口；Node 脚本读取端口后设置 `VITE_API_PROXY_TARGET` 启动专用 Vite。
Harness 同时为固定 receipt 提供 `/api/gift-receipts/media` 的 animation/avatar/effect-video/effect-layout 响应，
因此浏览器编辑预览和后端 resolver 使用同一组 fixture，不注入生产后门。

`gift-clip-export.html` 直接从 Vite 导入 `openGiftClipStudio`，创建固定 receipt 和 host，并把 controller
挂到测试页自己的 cleanup callback；它不复制 studio DOM 或导出逻辑。

`vite.config.ts` 改为：

```ts
server: {
  proxy: {
    '/api': process.env.VITE_API_PROXY_TARGET || 'http://localhost:12450',
  },
},
```

Playwright 在 `gift-clip-export.html` 分别生成无阻塞和确认后立即 busy-loop 180 ms 的两个 MP4，下载后用 framemd5 比较完整 frame hash sequence：

```js
await page.getByRole('button', { name: '确定剪裁并生成' }).click();
if (stall) await page.evaluate(() => {
  const until = performance.now() + 180;
  while (performance.now() < until) { /* intentional UI stall */ }
});
await page.waitForFunction(() => document.querySelector('.gift-clip-status')?.textContent?.includes('MP4 已生成'));
assert.deepEqual(await frameHashes(stalledPath), await frameHashes(baselinePath));
```

脚本还断言 0 console errors、0 overflow，截图编辑/encoding/ready 三态。它只终止自己启动并记录 PID 的 Go/Vite 进程树，不触碰 12450–12459 已有用户进程。

- [ ] **Step 4: 运行真实 RED/GREEN 验收**

实现 harness、页面和验证脚本后运行 GREEN；若出现产品缺陷，先把失败缩成对应模块的最小单元测试，再写最小修复，修复文件与回归测试一起纳入本任务提交：

```powershell
$env:GIFT_CLIP_FFMPEG_E2E='1'
$env:FFPROBE_BIN='D:\Program Files\ffmpeg\bin\ffprobe.exe'
Push-Location goserver
go test ./... -run "TestGiftClip(E2E|HarnessServer)" -count=1
Pop-Location
node scripts/verify-gift-clip-export.mjs
Remove-Item Env:GIFT_CLIP_FFMPEG_E2E
Remove-Item Env:FFPROBE_BIN
```

- [ ] **Step 5: 把 release workflow 改为内外层签名顺序**

在 `release.yml` 的 UI build 后增加：

1. `msys2/setup-msys2@v2` 安装 `mingw-w64-ucrt-x86_64-toolchain make nasm pkgconf`。
2. `npm run build:ffmpeg` 并 `npm run verify:ffmpeg`。
3. `node scripts/sign-evsign.mjs dist/ffmpeg/ffmpeg.exe`。
4. `Get-AuthenticodeSignature` 验证内层为 Valid。
5. 设置 `FFMPEG_AUTHENTICODE=true` 后 `node scripts/package-ffmpeg.mjs --input dist/ffmpeg/ffmpeg.exe`。
6. `npm run build:exe`。
7. 对外层 EXE 签名并验证。

随后运行真实 E2E。Release assets 除应用 EXE/hash/update/changelog 外，还上传：`ffmpeg-8.1.2.tar.xz`、`ffmpeg-8.1.2.tar.xz.asc`、`ffmpeg-build-config.txt`、`third_party/ffmpeg/NOTICE.md`、`third_party/ffmpeg/COPYING.LGPLv2.1`。本任务只修改 workflow，不打 tag、不 push、不实际发布。

- [ ] **Step 6: 更新脚本与用户文档**

`package.json` 加：

```json
{
  "verify:gift-clip-export": "node scripts/verify-gift-clip-export.mjs"
}
```

README 写清：输出固定 30 FPS MP4；输入帧率自适应；Windows 优先硬件、自动兼容回退；首次导出会在 LocalAppData 校验并准备内嵌编码组件；不需要用户安装 FFmpeg；许可证和源码入口。

- [ ] **Step 7: 顺序运行最终验证**

禁止并行：

```powershell
npm run typecheck
npm test -- --reporter=dot
Push-Location goserver
go test ./... -count=1
go test -race ./... -run "TestGiftClip" -count=1
Pop-Location
npm run build:ui
npm run verify:ffmpeg
$env:GIFT_CLIP_FFMPEG_E2E='1'
$env:FFPROBE_BIN='D:\Program Files\ffmpeg\bin\ffprobe.exe'
npm run verify:gift-clip-export
Remove-Item Env:GIFT_CLIP_FFMPEG_E2E
Remove-Item Env:FFPROBE_BIN
npm run build:exe
git diff --check
git status --short
```

Expected: TypeScript、全部 Vitest、全部 Go、race、Vite、FFmpeg 组件/fixture、Playwright stall、EXE build 全部 exit 0；没有控制台错误；stall/no-stall framemd5 完全相同；`dist/gift-panel.exe` 存在；原有未跟踪文件除本任务明确纳入者外仍存在。

- [ ] **Step 8: 提交 Task 13**

```powershell
git add -- package.json vite.config.ts README.md .github/workflows/release.yml scripts/generate-gift-clip-fixtures.ps1 scripts/verify-gift-clip-export.mjs scripts/diagnose-gift-clip-stutter.mjs tests/fixtures/gift-clip-stutter.html tests/fixtures/gift-clip-export.html tests/fixtures/gift-clip-media goserver/gift_clip_e2e_test.go
git commit -m "test: verify deterministic gift clip exports"
```

---

## Final Review and Local Integration Gate

完成 13 个任务后，先使用 `superpowers:requesting-code-review` 对“规格符合性”和“代码质量”分别审阅；任何修复必须新写回归测试并单独提交。然后重新执行 Task 13 Step 7 的完整顺序验证。

只有完整验证通过后才能使用 `superpowers:finishing-a-development-branch`。合并到本地 `master`、构建最终 EXE 或发版属于独立授权动作；执行前再次核对主仓库未跟踪文件清单，合并后逐项比对，禁止 push 或 GitHub Release，除非用户在该阶段明确授权。
