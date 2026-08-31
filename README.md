# Bilibili 直播礼物属性面板

OBS 工具：观众送礼物后，按照配置的规则把结果写入“加班时间”“积分”等属性。双击构建后的 `dist/gift-panel.exe` 会启动本地服务并自动打开内置配置向导。

## 开发构建

需要 Node.js 和 Go。在项目根目录运行：

```bash
npm install
npm run build
npm test
npm run typecheck
```

Go 服务测试需要以 `goserver` 为工作目录运行：

```bash
cd goserver
go test ./...
```

`npm run build` 会构建前端并编译本地服务，产物为 `dist/gift-panel.exe`。EXE 已内嵌前端页面和代理服务，主播电脑不需要安装 Node.js 或 Go。

Mac 开发、Hosted 优先日常循环、CI 失败复现以及 Windows ARM/x64 验收边界见 [Mac 与 Windows 兼容性工作流](docs/development/mac-hosted-workflow.md)。

EXE 退役必须按 [EXE 退役证据清单](docs/operations/exe-retirement-checklist.md) 逐阶段验收；TypeScript exporter v2 与 Hosted decoder compatibility 需先由 gameplay-unit migration 证据证明，不能仅凭 CI 进入 Stage B。

## 礼物动画回放

送礼记录中的 GIF、动态 WebP 和完整礼物特效都可以剪裁并导出为固定 30 FPS 的 H.264 MP4。输入素材不必预先转换成 30 FPS：程序会根据 GIF、WebP 或特效视频自身的帧时间进行采样，保持两秒素材等时间轴上的动作顺序。

Windows 导出会优先使用硬件编码；设备或驱动不兼容时会自动切换到软件兼容模式。首次导出时，程序会在当前用户的 LocalAppData 缓存中校验并准备 EXE 内嵌的最小 FFmpeg 编码组件，用户不需要安装 FFmpeg，也不需要配置 PATH。

内嵌组件基于未经修改的 FFmpeg 9.0 最小构建，遵循 LGPL 2.1 或更高版本。发布材料同时提供[再分发说明](third_party/ffmpeg/NOTICE.md)、[LGPL 2.1 许可证](third_party/ffmpeg/COPYING.LGPLv2.1)、[对应的 FFmpeg 9.0 源码](https://ffmpeg.org/releases/ffmpeg-9.0.tar.xz)及其签名。

## 发布与自动更新

正式版本使用 `vMAJOR.MINOR.PATCH` 标签发布，且标签必须与 `package.json` 中的版本一致。例如：

```powershell
git tag v0.1.0
git push origin v0.1.0
```

发布标签时，GitHub Actions 会从该标签的精确 checkout 运行 TypeScript、后端及国内更新工具测试，构建并验证 `gift-panel-windows-x64.exe` 的 Authenticode 签名，然后创建和复验 GitHub Release。GitHub Release 验证成功就是 Release job 的终点；GitHub 不保存或使用任何 COS 凭证，也不会调用 COS publisher、TAT 或 webhook。

Lighthouse 上独立的 oneshot 服务每五分钟从公开 GitHub Release 异步检查一次。它会下载并复验同一个已签名 EXE、SHA-256、fallback manifest 与更新日志，再把完整版本镜像到私有腾讯云 COS。镜像服务保持 `releases/` 对象不可变，并只在所有版本对象验证成功后更新 `channels/stable/latest.json`；stable 按规范 SemVer 单调递增，相同或更旧标签只 repair/复验版本对象，绝不会把 latest 降级。GitHub Release 发布不等待这次异步镜像。

手动运行 Release 工作流时，会根据该标签的 GitHub Release 状态选择唯一分支：

- GitHub Release 不存在：走正常首次发布，运行测试、构建、签名、上传并复验 GitHub Release。
- 已存在完整、非 draft、非 prerelease 的 GitHub Release：走 repair，仅下载已发布的 EXE、checksum、`gift-panel-update.json` 与 changelog；除复验签名发布者和 SHA-256 外，还会核对 fallback manifest 的标签、EXE 名称、size、digest 与该标签的 GitHub 下载 URL；不会重新生成 manifest、重新构建、重签或上传 GitHub 资产。
- GitHub Release 不完整，包括 draft/prerelease、缺少任一上述资产、fallback manifest 与下载资产不一致或其他资产校验失败：立即安全失败并要求人工恢复；不会尝试重新构建或使用 `--clobber` 覆盖资产。

Release job 绑定受保护的 GitHub Environment `release`；应为它配置必要的审批/分支规则。推荐配置 GitHub Actions variables `UPDATE_API_BASE_URL`、`EVSIGN_ACTIVE_PROFILE`、`EVSIGN_SIGNER_PROFILES_JSON`，以及 secrets `EVSIGN_KEY`、`EVSIGN_PASSWORD`。每个签名 profile 会原子绑定 EVSign 的显式证书选择值或服务商默认证书模式，以及完整 Authenticode Subject；所有签名和发布后复验仍严格匹配该 Subject，绝不会自动接受陌生证书。旧的 `EVSIGN_CERT`、`EVSIGN_EXPECTED_SUBJECT` 仅在两个 profile 变量都不存在时作为兼容回退，其中 `EVSIGN_CERT` 可省略以使用服务商默认证书。COS 凭证只安装在 Lighthouse 的独立镜像服务环境中，不进入 GitHub Actions。profile 格式、轮换步骤及镜像服务的安装、五分钟定时器、最小权限、dry-run、验证、备份与回滚见[国内更新 API 部署说明](deploy/update-api/README.md)。

如果 Lighthouse 镜像在 stable promotion 之前失败，旧的 stable 清单保证不变。写入新 stable 后若 exact readback 失败，publisher 会尝试恢复并验证写入前的 stable；只有恢复验证成功时才能确认旧版本已经恢复。没有旧指针或恢复失败会报告 `stable promotion outcome is indeterminate`，此时操作员必须先检查 `channels/stable/latest.json`，按部署说明恢复已验证备份并验证 API，再重跑镜像 oneshot。COS 的单对象覆盖不提供跨请求原子事务，因此不得把 post-PUT 错误描述为“stable 保证未变化”。无论镜像何时完成，已经创建的 GitHub Release 都独立可用，也是客户端的更新回退源。

正式 EXE 会在配置页和 OBS 面板全部关闭的空闲状态下优先读取国内更新 API；国内元数据或同版本下载失败时自动回退到 GitHub Release 静态清单与下载地址，不占用 GitHub API 额度。之后每 6 小时最多自动检查一次。程序会静默下载、校验 SHA-256 与签名发布者、替换 EXE 并重新启动后台服务。更新后的首次启动只显示系统通知，不会自动打开配置页面。配置页面的“外观与数据 → 程序更新”中可以关闭自动更新或手动检查更新。

本地 `npm run build` 生成的是 `dev` 版本，不会访问在线更新接口。如需在本地构建指定版本，可设置 `APP_VERSION` 和 `APP_COMMIT` 环境变量。发布失败后可以在 GitHub Actions 手动运行 Release 工作流并填写已有标签；工作流会严格按上面的三种状态处理，不会自动覆盖不完整的 GitHub Release。

### 更新日志写作规则

- 只记录用户能直接感知的重要新功能和 Bug 修复；排版微调、内部重构、字段变化等细节不写入更新日志。
- 新功能使用简单直白的小白说明，讲清楚“能做什么”和“从哪里开始用”；仅在确实有助于理解时配截图或动画。
- Bug 修复只写一句结果描述，不展开技术原因和实现细节。
- GitHub Release 保留所有版本记录；程序内置副本只保留最新版本。

### 规则语义

规则结果现在会直接成为属性的新值。需要累加时，请把当前属性名写进规则，例如 `早播次数+1`。
升级到赋值语义后，之前保存的规则不会自动转换，请逐条检查，必要时删除后重建。
