# Mac 开发与 Windows 兼容性工作流

本页是 Hosted 优先开发的维护者入口。批准的设计基线为提交 [`5a0bbfb`](https://github.com/brainfk123/bilibili-live-gift-panel/commit/5a0bbfb)；它是设计依据，不代表当前部署状态。

## Mac prerequisites

在 Mac checkout 中安装与仓库锁定版本相容的 Node.js、Go 和 Git。首次准备依赖：

```bash
npm ci
```

不要把凭据写入仓库、镜像或 snapshot。Hosted 本地运行所需的环境变量从受保护的本机配置注入，并保持在 checkout 之外。

## Daily Hosted loop

日常开发以 Hosted 页面和服务为目标，在 Mac checkout 中完成修改：

```bash
npm test
npm run typecheck
npm run build:hosted
go -C goserver test ./...
```

先用最小测试复现问题，再修复并重复这些检查。`npm run build:hosted` 只证明 Hosted 前端可以构建；它不证明 Windows EXE、驱动或硬件编码路径可用。

## What runs in pull-request CI

Pull request CI 在干净 checkout 上重复前端测试、类型检查、Hosted 构建和 Go 测试，并按工作流定义执行 Windows 相关检查。CI 是合并前的自动回归门，不等同于在目标设备上进行交互式验收。具体发布、签名和资产校验仍以 [README 的发布与自动更新说明](../../README.md#发布与自动更新) 为准。

## When Windows x64 runs

Windows x64 只在需要验证真实目标行为时运行：例如 Windows 专用 API、x64 驱动/显卡、硬件编码、EXE 启动、OBS 路由或媒体导出。Mac 上的交叉编译不替代这些检查。

## Downloading and reproducing a Windows failure

从 CI 下载失败 job 的构建产物，在隔离的 Windows 检查环境中复现。反馈必须包含：

- 触发构建的 commit SHA；
- Windows runner 镜像（或真实机器的 Windows 版本、架构）；
- 相关 artifact 的 SHA-256；
- 失败测试/命令及完整错误证据；
- 已覆盖与未覆盖的边界（尤其是 ARM、x64、驱动和硬件编码）；
- 重跑结果（通过、仍失败或未能复现）。

修复在 Mac checkout 中完成，提交后由 CI 重新构建、重新上传和复验；不要在 VM 或下载的 artifact 中直接编辑代码。无法复现时也要回传上述证据，而不是只报告“本地正常”。

## Windows 11 ARM snapshot rules

Windows 11 ARM snapshot 可用于检查安装、启动、窗口交互、Hosted 页面、文件路径和软件编码等 ARM 行为。snapshot 必须注明系统架构、镜像版本、测试 commit 和测试范围，不包含发布凭据。

ARM snapshot 的结果**不作为 x64 驱动、显卡或硬件编码证据**。交叉编译不等于 Windows 验收；通过 ARM snapshot 也不表示真实 x64 目标已经通过。

## Temporary real x64 acceptance

涉及 x64 专属行为时，在临时、受控的真实 Windows x64 机器上执行最小验收：记录机器/系统架构、驱动和编码器选择、artifact SHA-256、测试输入、输出及日志。验收完成后删除临时副本和敏感日志，保留可审计的非敏感结果。若失败，按本页的失败证据格式反馈，回 Mac 修复并等待 CI 重建。

## EXE release remains separate

本工作流不发布或签名 EXE。版本标签、Authenticode、FFmpeg 资产、GitHub Release 和自动更新以 [README 的发布与自动更新说明](../../README.md#发布与自动更新) 及其链接的发布工作流为唯一依据；Hosted 服务器操作以[国内更新 API 部署说明](../../deploy/update-api/README.md)为准。

## Retirement-stage changes

任何 EXE 功能迁移或退休阶段变更都必须逐项对齐 Hosted 的功能、状态、交互和媒体行为，并同时保留 Windows 证据。先更新设计/验收记录，再在 Mac checkout 修改并由 CI 构建；没有真实 x64 证据的项目不得标记为 Windows 完成。迁移不会改变发布或部署 runbook 的职责边界。
