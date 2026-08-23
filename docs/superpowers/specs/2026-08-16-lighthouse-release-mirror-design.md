# Lighthouse 国内发布镜像设计

**状态：** 已确认设计，待实施计划

**日期：** 2026-08-16

**基线：** `765f28f`

## 1. 背景与目标

现有 Release 工作流能够完成测试、构建、EVSign 签名、GitHub Release 发布和发布资产校验，但 GitHub 托管运行器连续两次在首次成功写入前连接腾讯云 COS 超时。GitHub Release `v0.4.4` 已发布，COS 存储桶仍为空。

本次将国内镜像职责从 GitHub Actions 迁移到中国大陆 Lighthouse。GitHub Release 与国内镜像解耦：GitHub Release 成功即视为发布成功；Lighthouse 在最多约五分钟内异步发现、校验并镜像正式版本。镜像失败时，客户端继续通过现有 GitHub 回退更新，COS 稳定通道保持上一有效版本。

本次包含：

1. Lighthouse 上的定时 Release 轮询与单次镜像程序。
2. GitHub Release 资产的下载、恢复、边界校验与本地状态管理。
3. 复用现有 COS 原子 publisher，将版本对象写入并最后推进稳定通道。
4. 独立 CAM 写入身份、systemd 服务/timer、日志与部署验收。
5. 从 GitHub Release 工作流移除 COS 写入密钥和直连镜像步骤。

本次不包含公网 webhook、腾讯云 TAT 远程触发、CDN、新公网端口、管理页面、用户通知系统或新的客户端更新协议。

## 2. 方案选择

已比较三种方案：

1. **Lighthouse 定时轮询（采用）**：没有公网写接口，GitHub 不再保存 COS 写入密钥，也不依赖 GitHub 到腾讯云 API 的实时链路；代价是最多约五分钟延迟。
2. **GitHub 调用 Lighthouse webhook（不采用）**：延迟低，但需要公网 POST 接口、共享 HMAC 密钥、防重放和额外限流。
3. **GitHub 调用腾讯云 TAT（不采用）**：无需公网 webhook，但 GitHub 仍需保存腾讯云控制凭证，并继续依赖跨境 API 链路。

## 3. 总体架构

```text
GitHub Actions
  └─ 测试、构建、签名、校验、发布 GitHub Release
       └─ 不连接 COS，不持有 COS 写入密钥

Lighthouse systemd timer（每 5 分钟）
  └─ 启动 gift-panel-release-mirror 单次服务
       └─ 查询 GitHub 最新正式 Release
            └─ 下载并校验四个必需资产
                 └─ 调用现有 COS publisher
                      ├─ releases/vX.Y.Z/* 先写入并回读校验
                      └─ channels/stable/latest.json 最后推进

国内更新 API
  └─ 继续使用独立只读凭证读取私有 COS，并签发短时下载 URL
```

镜像程序与现有公网更新 API 是两个独立进程、两个 systemd 单元和两个 CAM 身份。镜像程序不监听端口；更新 API 的公网 HTTP 契约不变。

## 4. 组件边界

### 4.1 GitHub Release 客户端

只访问仓库 `brainfk123/bilibili-live-gift-panel` 的公共 GitHub API。轮询最新已发布正式 Release，拒绝 draft、prerelease 和非规范 `vMAJOR.MINOR.PATCH` 标签。请求使用 ETag/`If-None-Match`，无需 GitHub Token。

只有镜像成功，或确认相同/更高规范版本已经由现有 publisher 完整验证后，才持久化新 ETag。网络失败、资产失败或 COS 失败不得提交 ETag，以便下一次 timer 自动重试。

### 4.2 Release 下载与验证

每个候选 Release 必须至少包含以下四个名称唯一的资产；镜像程序只选择这四个，忽略 FFmpeg 源码等其他发布附件：

```text
gift-panel-windows-x64.exe
gift-panel-windows-x64.exe.sha256
gift-panel-update.json
gift-panel-changelog.json
```

下载约束：

- 初始 URL 必须是 HTTPS、固定 GitHub 仓库、精确标签和精确资产名。
- 重定向只允许 HTTPS 和明确列出的 GitHub 官方托管域名；解析到回环、链路本地、私网或其他非公网目标时拒绝。
- 为每个资产设置明确的大小上限、连接超时、整体 deadline 和有限重试。
- EXE 使用持久化 `.part` 文件和安全的 Range/If-Range 续传；服务器不支持可信续传时从零重新下载。
- 最终文件必须是普通文件，写入镜像服务专用状态目录，不跟随符号链接。

校验顺序：

1. 校验 Release tag、draft、prerelease 和 publication timestamp。
2. 校验四个资产名称、唯一性、声明大小和 URL 归属。
3. 严格解析 checksum 文件，必须是 EXE 的 64 位 SHA-256。
4. 校验 EXE 实际大小和 SHA-256。
5. 严格解析 `gift-panel-update.json`，验证 tag、资产名、GitHub 下载 URL、size 和 digest 与实际文件完全一致。
6. 严格解析 changelog，要求 `schemaVersion = 1`、至少一个 release，且包含本次规范版本。

Linux 镜像服务不新增独立 Authenticode 信任实现。GitHub Release 工作流仍在发布前验证 Authenticode 状态与精确发布者 Subject；镜像端以发布资产、checksum 和严格 fallback manifest 的一致性作为传输校验。Windows 客户端安装前仍执行最终 SHA-256 与 Authenticode 发布者验证。

### 4.3 COS 发布适配

镜像程序复用现有 `updateapi/internal/publish` 和 `updateapi/internal/cosstore`：

- 版本对象写入 `releases/vX.Y.Z/`，已存在对象必须与候选内容完全一致。
- 任一版本对象写入或回读失败时，不更新稳定通道。
- `channels/stable/latest.json` 最后写入并精确回读。
- 相同版本重试是幂等操作；低于或等于已验证稳定版本时返回 `stable unchanged`，不得降级。
- 发布结果不输出 COS 签名 URL、密钥或响应正文。

### 4.4 本地状态

状态目录仅由镜像服务用户读写，至少保存：最近成功 ETag、tag、EXE SHA-256、publication timestamp 和完成时间。状态文件采用临时文件、fsync 和原子 rename；损坏状态必须被视为无缓存并重新核验，不能阻止后续恢复。

`.part` 下载与最终校验文件存放在同一专用状态目录。成功发布后可删除本次完成文件，仅保留受限状态；失败时只保留可安全续传且与 URL/ETag/size 绑定的部分文件。

## 5. 调度、权限与硬化

新增：

- `gift-panel-release-mirror.service`：`Type=oneshot`，每次只处理最新正式 Release。
- `gift-panel-release-mirror.timer`：每五分钟触发，`Persistent=true`，服务器离线后恢复时补一次检查。
- 独立系统用户 `gift-panel-mirror`，无 shell、无 home、无 root 权限。
- 独立 `/etc/gift-panel-release-mirror.env`，root 所有、权限 `0600`，仅包含 bucket、region 和镜像写入凭证。

服务使用 systemd 安全约束，包括 `NoNewPrivileges`、`PrivateTmp`、`ProtectSystem=strict`、`ProtectHome`、空 capability bounding set、限制地址族和仅允许专用 `StateDirectory` 写入。oneshot 未结束时，timer 不启动第二个并发实例。

CAM 身份分离：

1. `lighthouse-cos-publisher`：仅允许目标 bucket 的 `releases/*` 与 `channels/stable/latest.json` 所需 Head/Get/Put，不允许 Delete、列出其他 bucket 或修改 bucket 配置。
2. `lighthouse-update-api`：保持现有只读 Get 权限，不增加写权限。

部署验证完成后，从 GitHub `release` Environment 移除 COS 写入 secrets，并停用旧 `github-cos-uploader` 密钥。删除密钥属于独立云端高影响操作，执行时必须再次确认。

## 6. 失败恢复与运维

- GitHub API、DNS、连接或下载失败：本次退出失败，COS 不变，五分钟后重试。
- Release 元数据或资产校验失败：拒绝发布并记录安全摘要；同一 ETag 不标记成功，因此持续可见，直到发布资产被修复或出现更新版本。
- COS 失败：依赖现有 publisher 的稳定通道最后写入与幂等恢复；下一次轮询重新验证并重试。
- 日志只记录 tag、阶段、耗时和安全错误摘要，不记录凭证、完整下载 URL、查询参数、资产正文或 COS 响应正文。
- 不新增公网健康接口。状态通过 `systemctl status`、受限 journal 和 root/服务用户可读的本地状态文件检查。
- 回滚前先停止 timer，恢复已审核的 `channels/stable/latest.json` 私有备份，不删除或覆盖 `releases/*` 对象。问题处理完成后单次验证，再重新启用 timer。

GitHub Release 成功与国内镜像成功是两个独立状态。镜像失败不撤销或标红已完成的 GitHub Release；国内客户端在 COS 未更新时继续看到上一稳定版本，并保留 GitHub 回退。

## 7. 工作流变更

Release 工作流继续负责：

1. 测试、构建和签名。
2. 验证 Authenticode、SHA-256、size、fallback manifest 和 changelog。
3. 创建或修复 GitHub Release。

Release 工作流删除：

- `Mirror release to Tencent COS` 步骤。
- `COS_RELEASE_SECRET_ID` 与 `COS_RELEASE_SECRET_KEY` 的运行时使用。
- `UPDATE_PUBLISHER_TOOL_SHA` 及其独立 publisher checkout/验证步骤。
- 任何腾讯云写入或 TAT 调用。

Lighthouse 镜像二进制必须从经过审查的精确 Git commit 构建，并通过本地 SHA-256、版本化安装目录和受控 `current` 符号链接部署。服务器部署的代码身份不再借用 GitHub Environment 中的 publisher pin；具体构建、审查和回滚步骤在实施计划中给出。

## 8. 测试策略

### 8.1 Go 单元与集成测试

使用本地 HTTP 测试服务器覆盖：

- ETag 首次获取、成功提交和 304 无操作。
- GitHub/API/下载超时与失败后不提交 ETag。
- draft、prerelease、非规范 tag、重复或缺失资产拒绝。
- 非 HTTPS、错误仓库/标签/文件名、非 GitHub 重定向、私网/回环目标拒绝。
- size 上限、checksum 格式、SHA 不匹配、fallback manifest 不一致和 changelog 缺版本拒绝。
- 完整下载、可信续传、服务器忽略 Range 后从零下载、损坏 `.part` 恢复。
- 损坏状态文件重新核验与原子状态写入。

使用现有伪 Store 覆盖：

- 所有校验通过后才调用 publisher。
- 版本对象失败时 stable 不变。
- 相同版本幂等修复。
- 内容冲突拒绝。
- 旧版本不降级稳定通道。

### 8.2 部署与工作流测试

- 检查 oneshot 服务、五分钟 timer、`Persistent=true`、专用用户、`StateDirectory` 和 systemd 硬化项。
- 检查独立环境文件只列出变量名，文档要求 root `0600`，且不复用只读 API 环境文件。
- 检查 Release 工作流不再映射 COS secrets、不执行 COS/TAT/webhook 调用，并仍完成发布资产验证。
- 运行 updateapi race 测试、完整 Vitest/typecheck、Linux amd64 交叉构建和 `git diff --check`。

## 9. 部署与验收

部署按以下顺序进行：

1. 构建并安装镜像二进制、service 和 timer，但不启用 timer。
2. 创建 `lighthouse-cos-publisher` CAM 身份，验证允许的 Head/Get/Put 和明确拒绝的 Delete/越界访问。
3. 通过批准的秘密通道安装 root-owned `0600` 环境文件。
4. 运行 dry-run：只查询、下载和完成所有本地校验，不连接 COS 写入路径。
5. 单次正式执行，补发已经存在的 GitHub Release `v0.4.4`。
6. 验证 COS 版本对象、`channels/stable/latest.json`、国内 latest API 和 changelog API 均一致为 `v0.4.4`；日志中不得出现签名 URL。
7. 启用 timer，验证一次 304 无更新和一次受控网络失败后的自动重试。
8. 验证新的 GitHub Release 工作流不再尝试 COS 镜像。
9. 移除 GitHub COS 写入 secrets，并在再次确认后停用旧上传密钥。

任何生产安装前先备份现有 systemd、Nginx 和更新服务部署文件。配置验证、单次 dry-run 或本地健康检查失败时，不启用 timer、不替换现有更新 API，也不修改 COS stable。

## 10. 完成标准

- `v0.4.4` 由 Lighthouse 成功镜像，COS 和国内 API 返回一致的 tag、size 与 SHA-256。
- 每五分钟轮询能够在无更新时安全无操作，在暂时失败后自动恢复。
- GitHub Release 工作流成功不依赖腾讯云网络或 COS 凭证。
- GitHub 不再保存 COS 写入 secrets，旧上传密钥停用。
- Lighthouse 镜像写入身份与更新 API 只读身份完全分离。
- 任一下载、校验或版本对象失败都不会推进稳定通道。
- 没有新增公网端口、webhook、CDN、管理页面或客户端密钥。
- 全部 Go、race、TypeScript、typecheck、构建、工作流和部署资产测试通过。
