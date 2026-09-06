# Windows ARM EXE UI 开发环境

## 已建立的环境

2026-09-02 建立，2026-09-06 重新核对快照并恢复服务。

- Parallels Desktop / Tools：27.0.1 (58670)。
- Windows 11 Pro ARM64：26200.9168；按维护者决定保留未激活状态。
- VM 名称：`Windows 11`；6 个 vCPU、12 GiB RAM；共享网络（NAT）。
- 宿主目录、来宾目录、共享个人目录、剪贴板、云盘、应用、位置、打印机、摄像头和手柄自动共享已关闭。
- 纯系统快照：`clean-windows11-arm-2026-09-02`，ID `adf18737-fa95-4f90-9def-b8c64087e2e8`。
- EXE 快照：`exe-ui-baseline-0.4.10`，ID `f7d18a0e-09d5-4add-b022-de7fa9c2b074`。
- 两个快照均在 Windows 正常关机后建立。回退会丢失快照之后的 VM 改动，先另建快照保留当前工作。

UI 参考版本固定为 GitHub Release v0.4.10，即使开发分支已经升级至 0.4.13 也不自动替换参考。EXE 位于来宾 `C:\UIBaseline\v0.4.10`，自动更新关闭。

EXE SHA-256：`12649c86fc8492dbd6c75ff62df22e37116adb269a24af4c9f1167bf6f283b53`。
2026-09-02 已同时核对 GitHub asset digest、发布 checksum、VM 内文件 hash 和 Authenticode（Valid；NaisNet Technology Co., Ltd.）。

## 启动与访问

先查看状态，关机时 `start`，挂起时 `resume`；已经运行时无需重复启动：

```bash
prlctl list -a
prlctl start 'Windows 11'
# 如果状态为 suspended，改用：prlctl resume 'Windows 11'
prlctl exec 'Windows 11' --current-user powershell.exe -NoProfile -NonInteractive -Command 'Start-Process C:\UIBaseline\v0.4.10\gift-panel-windows-x64.exe'
```

在 macOS 浏览器访问 `http://10.211.55.3:12451/?mode=config`。

EXE 在来宾监听 `127.0.0.1:12450`；Windows portproxy 监听 `10.211.55.3:12451` 并转发到该端口。防火墙规则 `Codex-EXE-UI-HostOnly-12451` 仅允许源地址 `10.211.55.2/32`（Mac 宿主）。没有配置浏览器 CDP 入站规则。

挂起/重启后打不开页面时，先核对 VM 状态、EXE 进程、IP 和监听端口：

```bash
prlsrvctl net info Shared
prlctl exec 'Windows 11' ipconfig.exe
prlctl exec 'Windows 11' netsh.exe interface portproxy show v4tov4
prlctl exec 'Windows 11' netsh.exe advfirewall firewall show rule name=Codex-EXE-UI-HostOnly-12451 verbose
curl --fail --max-time 10 http://10.211.55.3:12451/health
```

IP 由 DHCP 分配，地址变化时应重新限定转发地址及宿主源地址；不要扩大到全部网卡或整个网段。此入口具备配置修改能力，仅用于无真实账户的开发 VM。

## UI 采集边界

维护者已决定当前阶段忽略浏览器平台差异：EXE 在 Windows 运行，EXE 和 Hosted 页面均由 macOS 同一个浏览器版本采集。Playwright 固定 viewport 和 `deviceScaleFactor: 1`：1440×900、1024×768、390×844。VM 桌面分辨率会随窗口变化，不能作为固定浏览器 viewport 的证据。

2026-09-02 的 macOS 浏览器为 Chromium 151.0.7922.34；每次采集重新记录实际版本。页面有持续连接，应等待具体页面元素和状态就绪；不要以 `networkidle` 作为唯一完成条件。

基线快照包含空配置，未填写房间号、未登录 Bilibili。夹具清单位于来宾 EXE 目录的 `empty-fixture.sha256.txt`，其 SHA-256 为 `0b22a219e4961015d70f63c535703348b2313d9d9a9941b9fe17bc4203af4956`。有数据状态和故障状态仍须另外建立确定性夹具。

截图步骤见 [采集合同](../operations/exe-ui-baseline-capture.md)。环境可运行、单页截图成功不代表六工作区完整对齐，也不证明真实 x64 GPU、OBS 或硬件编码通过。
