# 礼物视频 FFmpeg 隐藏启动与三倍码率设计

## 目标

修复 Windows 上导出礼物视频时 FFmpeg 控制台窗口弹出的问题，并将所有输出分辨率的目标码率整体提高三倍。输入帧率继续自适应，输出仍固定为 30 FPS；硬件编码失败后的软件回退、进程树取消和诊断行为保持不变。

## Windows 进程启动

FFmpeg 子进程继续以 suspended 状态创建，以便在恢复主线程前加入现有 Job Object。创建标志从单独的 `CREATE_SUSPENDED` 改为：

```text
CREATE_SUSPENDED | CREATE_NO_WINDOW
```

`CREATE_NO_WINDOW` 从源头禁止控制台窗口创建，不使用仅隐藏窗口的 `HideWindow`，避免窗口短暂闪烁。现有安全命名管道、输出复制、取消、强制终止和句柄所有权不变。

## 码率曲线

码率仍按裁剪后像素面积相对于 1920×1080 线性缩放，并保持 150 kbps 对齐，确保每个既有 50 kbps 对齐值恰好提高三倍。整套曲线提高三倍：

- 1920×1080 平均码率：2 Mbps → 6 Mbps。
- 最低平均码率：150 kbps → 450 kbps。
- 最高平均码率：16 Mbps → 48 Mbps。
- 峰值码率继续为平均码率的 1.5 倍。
- VBV buffer 继续为平均码率的 2 倍。

示例：1080p 使用平均 6 Mbps、峰值 9 Mbps、VBV 12 Mbps；4096×4096 的平均码率受 48 Mbps 上限约束。

FFmpeg 保持 `h264_mf` 的 `pc_vbr` 码率控制，并追加通用参数 `-compression_level 75`。在 FFmpeg 9.0 中，该参数映射到 Media Foundation 的 `AVEncCommonQualityVsSpeed`；对固定 GIF 和 packed-alpha 样本的探测显示，它在可忽略的耗时增量下提高了 SSIM。不得使用 `-quality`：微软文档说明它在受码率约束时无效，且探测产物与未设置时位级相同。

## 兼容性与范围

- 不改变输出 H.264 编码器选择、`pc_vbr`、硬件优先或软件回退策略。
- 不改变 30 FPS 输出、1–15 秒时长、64–4096 偶数像素范围或输入动画分类。
- 不改变内嵌 FFmpeg 的来源、组件集合、签名、许可证或打包方式。
- 不增加用户配置项；这是统一的质量基线调整。

## 验证

采用 TDD 分两个独立周期完成：

1. Windows 进程创建测试先证明旧实现缺少 `CREATE_NO_WINDOW`，再锁定组合标志，同时回归 suspended 启动、Job Object、取消和进程树清理。
2. Profile 测试先证明旧码率曲线不符合三倍值，再锁定代表性分辨率、450 kbps 下限、48 Mbps 上限，以及平均/峰值/VBV 的比例。FFmpeg argv 测试继续核对三项参数原样传入。

最终运行完整 Go 与 race 测试、前端测试、类型检查、UI/EXE 构建、内嵌 FFmpeg 验证和真实视频导出。实际产物须满足：

- 导出期间不出现 FFmpeg 控制台窗口。
- 输出仍为 H.264、30 FPS、预期分辨率和时长、无音轨。
- Profile 与 FFmpeg argv 精确锁定新的平均/峰值/VBV 目标。短、低熵 `pc_vbr` 产物不能以目标码率导出的最小字节数作为正确性条件：它是 VBR 目标而非填充保证。真实 E2E 保留既有上限预算、非平凡尺寸、H.264/yuv420p/30 FPS/帧数/时长/无音轨，以及既有感知质量和确定性门槛。
- EV 签名和发布资产校验保持有效。
