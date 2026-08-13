# 礼物动画 FFmpeg 导出设计

日期：2026-08-11
状态：已确认，待实施计划

## 背景

当前礼物动画剪裁编辑器通过 `requestAnimationFrame` 实时绘制 Canvas，再用
`canvas.captureStream(30)` 和 `MediaRecorder` 录制。只要 UI 主线程在录制期间发生阻塞，
成片就会写入连续重复帧，并在阻塞结束后跳帧。诊断样例中的一次 180 ms 阻塞已经稳定复现
5–6 个相邻冻结采样以及明显画面跳变，说明卡顿存在于输出文件本身，而不是预览播放器。

本设计把视频生成迁移到 Go 后端调用的独立、精简 FFmpeg CLI。编码不再依赖浏览器的实时
时钟或 Canvas 捕获节奏，而是从素材时间戳构造确定性时间线，再统一输出恒定 30 FPS 的 MP4。

## 目标

- 即使页面主线程短暂阻塞，输出动画仍保持确定性的 30 FPS 时间线，不产生由阻塞导致的冻结或跳跃。
- GIF、动画 WebP、完整礼物特效三类输入都按各自原始时间信息自适应读取。
- 输出为 H.264 MP4；Windows 优先硬件编码，失败时自动切换软件兼容模式。
- 以 1920×1080 平均 2 Mbps 为质量基准，分辨率降低时码率按像素面积同步降低。
- 单边输出尺寸支持 64–4096 px，宽高均为偶数，保留剪裁位置 1 px 移动精度。
- 最终仍只分发一个离线 `gift-panel.exe`，不要求用户安装 FFmpeg。
- 保持配置页调用 `openGiftClipStudio(...)` 的外部 seam 稳定，并继续拆分
  `gift-clip-studio.ts`，不把任务、API、编码或打包逻辑重新堆回该文件。

## 非目标

- 不做运动插帧或光流补帧。
- 不保留 MediaRecorder 作为导出回退。
- 不加入音频、字幕、网络输入协议或通用转码 UI。
- 不提供可配置输出帧率、编码器或手动码率选项。
- 不使用 libav 静态链接，也不使用 UPX 压缩可执行文件。
- 不改变已经确认的剪裁框交互、直角剪裁预览或圆角弹窗外观。

## 总体架构

导出分为四个边界清晰的单元：

1. **Studio 协调层（前端）**：维护“编辑、生成中、预览、失败”状态，调用独立模块，
   但不实现媒体解析、图像合成、HTTP 轮询或下载细节。
2. **导出素材层（前端）**：基于已确认剪裁和礼物信息生成两张静态 PNG：与尺寸相关的背景图，
   以及包含头像和赠礼文字的信息栏叠加图。动态礼物素材不经过逐帧 Canvas 导出。
3. **礼物剪辑任务服务（Go）**：根据 receipt ID 解析服务端已经信任的本地/缓存素材和特效布局，
   校验请求，排队并监督 FFmpeg 子进程，报告进度、取消任务、清理临时文件。
4. **FFmpeg 适配器（Go）**：负责可执行文件准备、参数和 filter graph 构造、进度解析、
   硬件到软件的单次回退、错误分类。调用使用绝对路径和参数数组，绝不经过 shell。

配置页继续只依赖 `openGiftClipStudio(options): GiftClipStudioController`，返回控制器仍只暴露
`close()`。实现时应把导出 API 客户端、静态素材生成、任务轮询和 UI 状态分别放入聚焦模块；
`gift-clip-studio.ts` 只负责组合这些模块。现有 `giftClipAnimationKey` 和剪裁保存行为保持兼容。

## 前后端协议

### 创建任务

`POST /api/gift-clips`

请求使用 `multipart/form-data`，字段为：

- `metadata`：JSON，包含 receipt ID、规范化剪裁矩形和客户端请求版本。
- `background`：与输出宽高完全一致的 PNG。
- `overlay`：与输出宽高完全一致、带透明通道的 PNG。

规范化剪裁使用源素材像素坐标 `{x, y, width, height}`。`x`、`y` 可为任意整数像素；
`width`、`height` 必须是 64–4096 范围内的偶数。后端不接受客户端传来的任意媒体路径或 URL，
而是通过 receipt ID 从现有礼物回执存储中重新解析可信素材及特效布局。PNG 必须通过签名、
尺寸、解码后像素数和请求体大小校验。

成功返回 `202 Accepted`：

```json
{
  "id": "opaque-job-id",
  "state": "queued",
  "output": { "width": 960, "height": 540, "fps": 30 }
}
```

### 查询、读取和取消

- `GET /api/gift-clips/{id}` 返回 `queued | encoding | retrying | ready | failed | cancelled`
  以及 0–1 的实际编码进度。`retrying` 同时返回“已切换兼容编码模式”的用户可见提示。
- `GET /api/gift-clips/{id}/video` 仅在 `ready` 时返回 `video/mp4`，使用安全的下载文件名。
- `DELETE /api/gift-clips/{id}` 取消排队或运行中的任务，终止整个 FFmpeg 进程树并清理任务目录；
  重复取消是幂等的。

任务 ID 不可猜测，并且任务只属于当前本地应用实例。服务一次只执行一个编码任务，其他任务
按创建顺序排队，从而限制 CPU、GPU、磁盘和内存峰值。前端关闭弹窗、重新剪裁或显式取消时
都必须发送取消请求；网络中断后，服务端也会通过 TTL 回收无人读取的结果。应用退出时终止
运行中的子进程并清理未完成任务。

## 确定性媒体时间线

输出固定为 CFR 30 FPS。第 `n` 帧的逻辑时间严格为 `n / 30` 秒，生成速度和页面/进程的
墙上时间均不参与帧选择。输出时长限制在 1–15 秒：完整特效使用规范化后的
`frames / fps`；短动画优先使用有效的 receipt `animation.durationMs`，缺失时使用解码所得的
单次循环时长，仍无法取得时使用 3 秒默认值。末帧边界采用 `frameTime < duration`，避免额外
多生成一帧。

输入按素材自身时间信息自适应：

- **单帧 GIF**：严格解析确认只有一个 image descriptor 后，以 `gif` decoder + `image2`
  静态输入按 30 FPS 循环；不能用这一分支打开多帧 GIF。
- **多帧 GIF**：在任务目录的可信副本中严格插入或改写唯一 `NETSCAPE2.0` loop count 为 0，
  保留原始帧数据、delay、disposal、画布和调色板，再由 GIF demuxer 使用 `-ignore_loop 0`
  按原始时间戳无限循环。禁止 `-stream_loop`。
- **静态 WebP**：严格确认没有 `ANIM`/`ANMF` 后，使用 `webp_pipe` 静态循环。
- **动画 WebP**：严格要求 VP8X animation flag、唯一合法 `ANIM` 和至少一个 `ANMF`，只在
  任务副本中把 `ANIM.loop_count` 改为 0，再由 FFmpeg 9.0 `webp_anim` demuxer/decoder 和
  `-ignore_loop 0` 按容器 duration/timestamp 无限循环。原始下载文件不修改。
- **完整特效**：使用已验证布局中的 `fps`、`frames`、RGB 区域与 alpha 区域；持续时间为
  `frames / fps` 并沿用 1–15 秒规范化规则。FFmpeg 分别裁出 RGB 和 alpha 平面，缩放到
  相同尺寸后合成透明动态画面。

FFmpeg 的时间轴/`fps` 过滤把输入重采样为 30 FPS：低帧率输入按时间戳重复必要帧，
高帧率输入按时间戳丢弃必要帧。禁止运动插帧。相同输入、剪裁和图层必须得到相同帧选择，
与机器编码耗时无关。

禁止使用会缓存整个动画周期的 `loop` video filter。16 MiB 素材大小门限和 4096×4096
画布上限保持不变；播放链路的内存复杂度必须与动画帧数/周期长度无关，只允许保存有界源
文件数据、解码器当前画布/帧和固定数量的合成缓冲区。GIF/WebP 规范化必须严格校验容器边界、
扩展块/分块唯一性和终止符，歧义或畸形输入直接失败；通过同目录 partial + 原子无替换安装
生成任务副本，取消或失败时只清理本任务拥有的文件。

## 合成与输出

FFmpeg filter graph 依次完成：

1. 解码可信动态素材并恢复其时间戳；完整特效先重建 alpha。
2. 按源像素坐标剪裁动态画面；剪裁位置保留 1 px 精度。
3. 将静态背景 PNG 循环到目标时长。
4. 以 alpha 叠加动态礼物画面，再叠加前端生成的信息栏 PNG。
5. 规范化到 `yuv420p`、CFR 30 FPS 和精确的偶数输出尺寸。
6. 用 H.264 写入 MP4，并启用 fast-start 元数据布局。

背景和信息栏的像素表现继续由现有前端 renderer/design token 产生，避免在 Go 或 FFmpeg 命令中
复制字体度量、渐变和头像回退规则。两张 PNG 是每个任务仅生成一次的静态输入；不生成或传输
逐帧 PNG 序列。

## 帧率、尺寸与码率

- 输出帧率：固定 30 FPS。
- 输出宽高：各 64–4096 px，且各自为偶数。
- 最大输出面积：`4096 × 4096 = 16,777,216` 像素。
- H.264 码率单位使用十进制 bit/s。

平均目标码率按像素面积线性缩放：

```text
raw = 2,000,000 × (outputWidth × outputHeight) / (1920 × 1080)
rounded = floor((raw + 25,000) / 50,000) × 50,000
average = clamp(rounded, 150,000, 16,000,000)
peak = average × 1.5
vbvBuffer = average × 2
```

也就是说，平均码率四舍五入到最近的 50 kbps，最低 150 kbps，最高 16 Mbps；
峰值目标为平均码率的 1.5 倍，VBV buffer 为平均码率的 2 倍。硬件和软件路径使用同一套目标。
VBR 的实际文件平均值允许因内容复杂度和编码器实现而合理浮动，但参数和边界必须一致。

代表值：

| 分辨率 | 平均目标码率 |
| --- | ---: |
| 512×360 | 200 kbps |
| 640×360 | 200 kbps |
| 960×540 | 500 kbps |
| 1280×720 | 900 kbps |
| 1920×1080 | 2 Mbps |
| 2560×1440 | 3.55 Mbps |
| 3840×2160 | 8 Mbps |
| 4096×4096 | 16 Mbps（封顶） |

## 编码器选择与回退

Windows 首次尝试 `h264_mf` 且启用 `-hw_encoding 1`。FFmpeg 进程返回编码器不可用、初始化失败
或硬件编码失败时，任务保留相同输入、filter graph、30 FPS、尺寸和码率目标，重新运行一次
`h264_mf -hw_encoding 0`。进入第二次尝试时状态改为 `retrying`，前端显示
“已切换兼容编码模式”。

取消、输入损坏、尺寸/协议校验失败、FFmpeg 完整性失败或磁盘写入失败不触发软件重试。
单次应用会话缓存硬件编码可用性：一旦确认当前环境需要软件模式，后续任务可直接使用软件模式；
应用重启后重新探测。软件模式仍失败时任务终止，不回退 MediaRecorder。

FFmpeg 使用 `-progress pipe:1 -nostats` 输出机器可读进度。服务用 `out_time` 除以目标时长计算
0–1 进度，保证单调、不超过 1；硬件尝试失败并切换时进度可回到 0，但状态明确变为 `retrying`。

## FFmpeg 精简、嵌入与许可

发布物使用官方签名的 FFmpeg 9.0 release tarball 从源码构建独立 `ffmpeg.exe`。配置以
`--disable-everything` 为基础，只开启：

- `ffmpeg` 程序本身；不构建 `ffplay`、`ffprobe`。
- 本地文件和必要 pipe 协议；关闭全部网络协议。
- MP4/MOV、GIF、`webp_anim`、`image_webp_pipe`、image2 等本功能实际需要的 demuxer。
- H.264、GIF、`webp_anim`、静态 WebP、PNG 等必要 decoder；完整特效已确认是 H.264，
  不启用 HEVC/VP9。
- GIF/H.264 parser；GIF parser 是多帧 GIF 正确解码的必要组件。
- `h264_mf` encoder 和 MP4 muxer。
- crop、scale、format、split、alpha 合成、overlay、fps、setpts 等实际 filter graph 需要的 filter；
  不启用或使用缓存整个周期的 `loop` filter。
- Windows Media Foundation 及构建所需依赖；不启用 GPL 或 nonfree 组件，不含音频、字幕和网络功能。

具体开关必须由可复现构建脚本显式列出，并以 GIF、动画 WebP、完整特效三类 fixture 验证，
不得仅凭组件名称推断功能存在。FFmpeg 9.0 同时启用动画 `webp_anim` 与静态
`image_webp_pipe` 路径，并在 `--disable-autodetect` 下显式启用 `mediafoundation` 与
`d3d11va` 以满足 `h264_mf` 的编译
依赖；`d3d11va` 不授权额外 codec/hwaccel，Windows/MSYS2 构建目标为 `ffmpeg.exe`。组件验证
允许 FFmpeg 为显式白名单自动选择的必要基础设施项，但
必须逐项记录，且不得扩大 codec、协议、网络、音频、字幕、GPL 或 nonfree 范围。若实际素材
证明无需某个视频 decoder，应从最终配置移除。

打包顺序：

1. 构建最小 FFmpeg CLI。
2. 对内层 `ffmpeg.exe` 进行 Authenticode 签名。
3. 计算已签名文件 SHA-256，并生成包含版本、hash 和大小的 manifest。
4. 使用标准 ZIP/DEFLATE 压缩已签名文件；不使用 UPX。
5. 将 ZIP 和 manifest 通过 Go embed 放入 `gift-panel.exe`。
6. 构建并签名外层应用 EXE。

首次导出时，Go 服务把 FFmpeg 解压到 `%LOCALAPPDATA%` 下按版本和 hash 命名的应用缓存目录，
使用临时文件、校验 SHA-256 后再原子改名。后续导出复用 hash 正确的缓存；缓存缺失或损坏时
自动重建。进程始终从这个已验证的绝对路径启动。本期只管理当前 manifest 对应的缓存路径，
不主动删除无法由当前 manifest 确认归属的旧文件或目录。

压缩后的 FFmpeg 目标不超过 30 MB；超过 40 MB 时停止发布并复查构建配置，不能用额外可执行
压缩器掩盖组件膨胀。发布材料同时包含 FFmpeg 精确源码归档/commit、完整构建配置与脚本、
LGPL 文本、版权声明和修改说明，以满足再分发义务。

## 生命周期、清理与错误呈现

每个任务使用独立、不可预测名称的临时目录，只包含后端解析的可信源素材副本（需要时）、
背景 PNG、叠加 PNG、输出 MP4 及必要日志；绝不生成逐帧图片序列，也不接受用户路径。
任务 ready 后保留结果供当前弹窗预览和下载，以下任一事件发生即清理：取消、关闭/重新剪裁、
结果被释放、TTL 到期或应用退出。

子进程使用 Windows Job Object 托管，确保取消和退出不会残留 FFmpeg。
日志保留经过清洗的退出码、阶段、编码模式和有限 stderr 尾部，不记录用户令牌、任意本地路径
或完整请求内容。

用户错误使用稳定中文消息并保留“重试/重新剪裁”路径：

- 素材损坏或不支持：`礼物动画素材无法解码，请重试或更换素材。`
- 尺寸无效：明确显示允许的 64–4096 偶数像素范围。
- 磁盘空间不足：`磁盘空间不足，无法生成视频。`
- FFmpeg 完整性失败：`视频编码组件校验失败，请重启程序后重试。`
- 硬件回退：非错误提示 `已切换兼容编码模式。`
- 最终编码失败：`视频生成失败，请重试。`，诊断日志记录可操作的内部原因。

## 测试策略

实现全程采用 TDD，每个任务先写失败测试，再写最小实现，并独立提交。测试覆盖：

### 前端单元测试

- 输出宽高规范化为 64–4096 范围内的偶数，剪裁位置仍以 1 px 移动。
- 码率公式、50 kbps 舍入、150 kbps 下限和 16 Mbps 上限的边界与表格样例。
- 静态背景/overlay PNG 尺寸、透明通道和现有 renderer 视觉数据一致。
- Studio 状态在 queued、encoding、retrying、ready、failed、cancelled 间正确迁移。
- 关闭、重新剪裁和取消都会清除轮询并调用 DELETE。
- `openGiftClipStudio(...)` seam 与配置页调用保持不变，并设置文件职责/体积回归保护，防止逻辑回填到大文件。

### Go 单元与集成测试

- receipt ID 到可信素材的解析，不接受 URL、路径穿越或伪造布局。
- multipart 大小、PNG 签名/尺寸/解码像素数、剪裁范围和偶数尺寸校验。
- 任务单并发、FIFO 排队、取消幂等、TTL 与退出清理。
- FFmpeg 参数数组和 filter graph 对 GIF、动画 WebP、完整 RGB/alpha 特效正确。
- 30 FPS 帧数与时间边界；输入低帧率重复、高帧率丢帧，不做插帧。
- 码率、peak、VBV 参数在硬件/软件路径保持一致。
- 注入硬件初始化失败后只重试一次软件模式；取消、坏素材、磁盘失败不误重试。
- `-progress` 解析、单调进度、错误分类和用户消息映射。
- ZIP 解压、SHA-256 校验、损坏缓存重建、原子替换和并发首次准备。
- 子进程树在取消和应用退出时被终止，任务临时目录最终为空。

### 端到端与成片验证

- fixtures 至少包含 GIF、动画 WebP、packed-alpha 完整特效，以及不同原始 FPS/帧延迟。
- 用 FFprobe/解码结果断言 MP4 为 H.264、`yuv420p`、精确偶数分辨率、恒定 30 FPS、正确时长。
- 在导出期间向页面注入 180 ms 主线程阻塞，成片帧序列与无阻塞基准相同，不出现现有方案的冻结/跳帧。
- 验证剪裁、背景、透明特效、头像和信息栏合成结果；抽取代表帧做像素或截图比对。
- Playwright 覆盖剪裁、生成、兼容模式提示、预览、保存、重新剪裁、关闭/取消及错误恢复。
- 常规 CI 固定运行软件模式；Windows 专用环境另跑硬件路径与回退测试。
- 顺序执行 TypeScript 检查、Vitest、Go tests、UI build、Playwright 和 EXE 构建，避免构建并发改写
  `dist/index.html` 干扰“真实 dist 未被测试触碰”的断言。

## 完成标准

- 所有既有剪裁编辑器行为与配置页 seam 保持兼容。
- 所有新增和既有测试通过，且没有未解释的跳过或控制台错误。
- 180 ms 页面阻塞诊断不再改变输出帧序列。
- 三类素材均生成固定 30 FPS、H.264 MP4，并满足尺寸和码率契约。
- 硬件失败可见地回退到软件模式，取消不留进程或临时文件。
- 单个离线 EXE 能在无系统 FFmpeg 的干净 Windows 环境首次解压、校验、编码并复用缓存。
- 发布材料包含可复现的最小 FFmpeg 构建信息和 LGPL 合规文件。
- 精简 FFmpeg ZIP 不超过 30 MB 目标；若超过 40 MB，发布流程明确失败并要求复查。

## 实施边界

后续实施计划应拆成小任务，每个任务独立提交。建议顺序是：纯函数契约（尺寸/码率）、
FFmpeg 参数和时间线、嵌入运行时、任务服务/API、前端 API 与静态图层、Studio 接入与大文件拆分、
端到端卡顿回归、构建/许可/发布验证。任何实现中发现会改变上述用户可见行为或发布形态的事项，
必须先回到设计评审，不能在代码中默认扩大范围。
