# Mac 开发与 Windows 兼容性工作流

本页是 Hosted 优先开发的维护者入口。批准的设计基线为提交 [`5a0bbfb`](https://github.com/brainfk123/bilibili-live-gift-panel/commit/5a0bbfb)；它是设计依据，不代表当前部署状态。

## Mac prerequisites

在 Mac checkout 中安装与仓库锁定版本相容的 Node.js、Go 和 Git。部署契约测试使用 Bash 4.2+，Apple 自带的 `/bin/bash` 不满足要求；安装 Homebrew Bash，并在运行测试前显式选择它：

```bash
brew install bash
export BASH_BIN="$(brew --prefix)/bin/bash"
npm ci
```

不要把凭据写入仓库、镜像或 snapshot。Hosted 本地运行所需的环境变量从受保护的本机配置注入，并保持在 checkout 之外。

## Daily Hosted loop

日常开发以 Hosted 页面和服务为目标，在 Mac checkout 中完成修改：

```bash
npm test
npm run typecheck
npm run build:ui
npm run prepare:go-assets
npm run verify:go-linux-compile
npm run build:hosted
go -C goserver test -race -count=1 ./cmd/hosted ./internal/...
go -C goserver vet ./cmd/hosted ./internal/...
npm run test:update-api
```

先用最小测试复现问题，再修复并重复这些检查。`verify:go-linux-compile` 只验证最终 Linux 编译边界；Mac 日常循环不运行包含 Windows-only 根包的 `goserver` 全量 runtime test。`npm run build:hosted` 只证明 Hosted 前端可以构建；它不证明 Windows EXE、驱动或硬件编码路径可用。

## Apple Silicon fresh-checkout acceptance

这是一份真实 Apple Silicon fresh-checkout cutover 清单。在用户的 Apple Silicon Mac 上实际执行并保存结果前，它仍是 **external evidence**，本分支不能把它标记为已通过。Linux Hosted 主线 CI 仍是必需的远程门禁，本方案不新增永久 macOS CI job。

- [ ] 从受信任远程创建全新的 checkout，记录 commit SHA，且不复用旧 `node_modules`、Go build cache 或生成资产。
- [ ] 记录 `uname -m` 输出为 `arm64`，以及 macOS、Xcode command-line tools、Git、Node.js 和 Go 版本。
- [ ] 执行 `brew install bash`，导出 `BASH_BIN="$(brew --prefix)/bin/bash"`，并记录所选 Bash 版本不低于 4.2。
- [ ] 在 fresh checkout 中执行 `npm ci`，保存锁文件一致性和退出码证据。
- [ ] 按 Daily Hosted loop 的原始顺序执行全部命令，保存每条命令、退出码和非敏感日志指针。
- [ ] 确认测试后没有意外 tracked 修改，且构建产物未混入凭据、本机绝对路径或缓存。
- [ ] 把 commit、机器架构、工具版本、结果摘要和完整证据位置写入可审计 acceptance record；失败项回传 Mac 修复循环。

## What runs in pull-request CI

Pull request CI 在干净 checkout 上重复前端测试、类型检查、Hosted 构建和 Go 测试，并按工作流定义执行 Windows 相关检查。CI 是合并前的自动回归门，不等同于在目标设备上进行交互式验收。具体发布、签名和资产校验仍以 [README 的发布与自动更新说明](../../README.md#发布与自动更新) 为准。

## When Windows x64 runs

Windows x64 只在需要验证真实目标行为时运行：例如 Windows 专用 API、x64 驱动/显卡、硬件编码、EXE 启动、OBS 路由或媒体导出。Mac 上的交叉编译不替代这些检查。

## desktop-high-risk release approval evidence

`desktop-high-risk` 变更通过 Windows compatibility job 后，维护者在批准 protected `release` Environment 之前还必须完成下列人工证据记录。GitHub Environment 的外部配置不能由本分支证明，因此每次批准都要重新确认 required Environment reviewers remain enabled。

- [ ] PR/run URL：指向被审批的 PR 和完成的 CI run。
- [ ] Windows x64 artifact/evidence SHA-256：记录下载产物及对应证据文件的哈希。
- [ ] smoke routes：列出已执行的 EXE 启动、页面、OBS、媒体或更新路径及结果。
- [ ] real GPU/OBS/driver evidence required：明确 yes/no；若为 yes，附上非敏感 evidence pointer，若为 no，记录不适用理由。
- [ ] approver and approval date：记录批准人和带时区日期。
- [ ] required Environment reviewers remain enabled：记录审批时的确认结果及可审计指针。

证据记录本身必须提供一个稳定的 evidence pointer。**no release approval occurs without the evidence pointer**；任一字段缺失时不得批准 protected `release` Environment，也不得把自动 Windows 冒烟当作真实 GPU、OBS 或驱动证据。

## Downloading and reproducing a Windows failure

先记录失败 job 的日志、commit SHA、runner 镜像和失败命令。仅当 artifact 成功上传时才下载构建产物并核对其 SHA-256，再在隔离的 Windows 检查环境中复现。反馈必须包含：

- 触发构建的 commit SHA；
- Windows runner 镜像（或真实机器的 Windows 版本、架构）；
- 相关 artifact 的 SHA-256（仅当 artifact 成功上传时）；
- 失败测试/命令及完整错误证据；
- 已覆盖与未覆盖的边界（尤其是 ARM、x64、驱动和硬件编码）；
- 重跑结果（通过、仍失败或未能复现）；无 artifact 时，必须在干净 Windows x64 或 CI 中重新运行失败命令复现，不能假设存在可下载的 artifact。

修复在 Mac checkout 中完成，提交后由 CI 重新构建、重新上传和复验；不要在 VM 或下载的 artifact 中直接编辑代码。无法复现时也要回传上述证据，而不是只报告“本地正常”。

## Windows 11 ARM snapshot rules

Windows 11 ARM snapshot 可用于检查安装、启动、窗口交互、Hosted 页面、文件路径和软件编码等 ARM 行为。snapshot 必须注明系统架构、镜像版本、测试 commit 和测试范围，不包含发布凭据。

ARM snapshot 的结果**不作为 x64 驱动、显卡或硬件编码证据**。交叉编译不等于 Windows 验收；通过 ARM snapshot 也不表示真实 x64 目标已经通过。

## Temporary real x64 acceptance

涉及 x64 专属行为时，在临时、受控的真实 Windows x64 机器上执行最小验收：记录机器/系统架构、驱动和编码器选择、artifact SHA-256、测试输入、输出及日志。验收完成后删除临时副本和敏感日志，保留可审计的非敏感结果。若失败，按本页的失败证据格式反馈，回 Mac 修复并等待 CI 重建。

## EXE release remains separate

本工作流不发布或签名 EXE。版本标签、Authenticode、FFmpeg 资产、GitHub Release 和自动更新以 [README 的发布与自动更新说明](../../README.md#发布与自动更新) 及其链接的发布工作流为唯一依据；Hosted 服务器操作以 [Hosted 部署 runbook](../../deploy/hosted/README.md) 为准。EXE 更新基础设施（而非 Hosted 部署）另见[更新 API 部署说明](../../deploy/update-api/README.md)。

## Retirement-stage changes

任何 EXE 功能迁移或退休阶段变更都必须逐项对齐 Hosted 的功能、状态、交互和媒体行为，并同时保留 Windows 证据。先更新设计/验收记录，再在 Mac checkout 修改并由 CI 构建；没有真实 x64 证据的项目不得标记为 Windows 完成。迁移不会改变发布或部署 runbook 的职责边界。
