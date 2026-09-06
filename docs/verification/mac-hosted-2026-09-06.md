# Mac Hosted 同步后验证（2026-09-06）

## 范围

起点为合并提交 `5f7ad5e`，已包含 `origin/master` 的 `659e2c2`。
此记录覆盖随后本次 Mac 修复；不复用 2026-09-02 的 PASS 来证明新代码。

## 修复及审查

- 发布就绪 CLI 以真实入口路径判断直接执行，修复 macOS `/var` 与 `/private/var` 别名导致未验证却返回成功的问题。目录别名回归测试先失败，修复后通过；workflow 拷贝闭包测试同时通过。
- `import_bundle_test.go` 与其 Windows/Linux 夹具使用相同 build constraint；macOS 保留 unsupported-platform 拒绝测试。
- Linux 部署测试按每次 build 的不可变 image ID 运行，消除并发工作区覆盖共享 tag 的问题；build/run 各有 240 秒子进程超时，finally 限时清理本次命名容器。
- 两个独立审查分别检查代码规范和设计要求；共同发现镜像 tag 竞态，规范审查另外发现同步调用超时无效。修复后复核无剩余可操作问题。

## 本机执行结果

Node 22.23.2，npm 10.9.8，Go 1.26.7 darwin/arm64，Docker server 29.5.2。
安全文件测试拒绝带符号链接的路径，因此 Go 验证使用 `TMPDIR=/private/tmp`；路径拒绝逻辑未放宽。

| 检查 | 结果 |
| --- | --- |
| `npm test -- --reporter=dot --minWorkers=2 --maxWorkers=2` | 100 文件通过；1475 passed、32 skipped |
| `npm run typecheck` | PASS |
| `npm run build:ui` | PASS |
| `npm run prepare:go-assets` | PASS |
| `npm run verify:go-linux-compile` | PASS |
| `npm run build:hosted` | PASS |
| `go -C goserver test -race -count=1 ./cmd/hosted ./internal/...` | PASS（真实临时目录） |
| `go -C goserver vet ./cmd/hosted ./internal/...` | PASS |
| `npm run test:update-api` | PASS（平台测试约束修复后） |
| `git diff --check` | PASS |

完整测试首次发现 CLI 路径回归；Go 首次发现临时目录别名拒绝及导入测试 build constraint 遗漏，上表为对应修复后的结果。原始本机日志保留于本次临时执行目录，不属于长期归档；远端 CI 必须验证本次提交后才可合并。

## VM 与后续范围

已重新确认两个关机快照存在、恢复 VM，并实测 EXE health 返回版本 0.4.10。
环境操作见 [Windows ARM UI 环境](../development/windows-arm-ui-environment.md)。

尚未声明完整 UI 对齐、媒体对齐、真实 x64 硬件验收、生产恢复演练或七天试点通过。
