# 国内软件更新 API 设计

**状态：** 已确认设计，待实施计划
**日期：** 2026-08-14
**基线：** `30c7170`

## 1. 目标与范围

为 `Bilibili 直播礼物属性面板` 增加中国大陆更新镜像。Windows 客户端优先从已备案域名查询版本并从腾讯云 COS 下载更新；国内链路失败时继续使用现有 GitHub Release 更新源。

本次包含：

1. Lighthouse 上的极简备案页、HTTPS 入口和版本化更新 API。
2. COS 私有桶中的版本化安装包、SHA-256、更新日志和稳定通道元数据。
3. GitHub Actions 发布后自动同步 COS。
4. 客户端国内源优先、GitHub 回退，以及下载后的 SHA-256 与 Authenticode 验证。
5. 限流、日志、健康检查、部署与回滚。

本次不包含：

- 宣传页、下载列表、用户账号、设备注册、遥测或第三方上传。
- CDN。首版由客户端直接下载 COS 私有桶对象。
- beta/nightly 多通道、增量更新、多平台或多架构。
- 自研清单签名协议或 TUF；如果未来威胁模型要求清单密钥轮换和抗回滚，再独立设计。

## 2. 总体架构

```text
Windows 客户端
   ├─ 首选：国内更新 API
   └─ 回退：GitHub Release 静态清单

已备案域名 → Nginx
   ├─ /                         极简备案页
   ├─ /api/v1/releases/latest  反向代理 Go 更新服务
   ├─ /api/v1/changelog        反向代理 Go 更新服务
   └─ 其他路径                  JSON 404

Go 更新服务（127.0.0.1:12450）
   └─ 读取 COS 通道元数据、校验并生成短时下载地址

COS 私有桶
   ├─ releases/vX.Y.Z/...
   └─ channels/stable/latest.json

GitHub Actions
   └─ 构建、签名、校验、发布 GitHub Release、同步 COS
```

Nginx 是唯一公网入口。Go 服务只监听回环地址，不直接开放端口。COS 是国内发布文件和通道元数据的权威存储；Lighthouse 不代理安装包内容。

## 3. 公网 HTTP 契约

### 3.1 根路径与备案信息

`GET /` 返回极简 HTML，不提供产品介绍、下载按钮、用户信息或其他页面导航。页面只包含“软件更新服务”和位于底部中央的真实 ICP 备案号；备案号链接到 `https://beian.miit.gov.cn/`。

页面设置 `noindex,nofollow`，但不以 robots 设置代替备案展示。部署时必须提供已备案域名和该主体的真实备案号；在二者缺失时不得切换生产域名流量。

### 3.2 最新稳定版本

```text
GET /api/v1/releases/latest
HEAD /api/v1/releases/latest
```

成功响应保持客户端现有 GitHub Release 兼容形状：

```json
{
  "tag_name": "v0.4.4",
  "draft": false,
  "prerelease": false,
  "assets": [
    {
      "name": "gift-panel-windows-x64.exe",
      "browser_download_url": "https://private-cos-object?...",
      "size": 12345678,
      "digest": "sha256:0123456789abcdef..."
    }
  ]
}
```

约束：

- `tag_name` 必须是 `vMAJOR.MINOR.PATCH` 正式版本。
- 首版只允许一个名为 `gift-panel-windows-x64.exe` 的 asset。
- `size` 必须大于 0 且不超过客户端现有的 256 MiB 上限。
- `digest` 必须是 64 位十六进制 SHA-256，并带 `sha256:` 前缀。
- 下载地址只允许指向配置的 COS bucket、region 和 `releases/vX.Y.Z/` 前缀。
- COS 地址有效期固定为 10 分钟；响应设置 `Cache-Control: private, no-store`。

接口不接收平台、架构、对象路径或文件名参数，避免首版形成任意对象签名入口。

### 3.3 更新日志

```text
GET /api/v1/changelog
HEAD /api/v1/changelog
```

返回与现有 `gift-panel-changelog.json` 相同的原始文档：

```json
{
  "schemaVersion": 1,
  "releases": []
}
```

响应可使用 `ETag` 和最多 5 分钟的公共缓存。客户端本地 Go 服务继续把该文档转换为现有 `/api/changelog` UI 响应，不修改前端格式。

### 3.4 其他请求

- 未定义路径返回 JSON `404`。
- 已定义资源上的非 `GET/HEAD` 请求返回 JSON `405` 并设置 `Allow: GET, HEAD`。
- 公网不暴露服务端调试、指标、目录列表或 COS 对象列表。
- `/healthz` 只允许 `127.0.0.1` 和 `::1`，成功返回 `200 ok`。

## 4. 发布数据流

现有 GitHub Actions 在完成测试、构建、EVSign 签名和 GitHub Release 发布后同步国内镜像：

1. 校验 EXE Authenticode 状态有效。
2. 计算 EXE SHA-256 和文件大小。
3. 上传不可变对象：

   ```text
   releases/vX.Y.Z/gift-panel-windows-x64.exe
   releases/vX.Y.Z/gift-panel-windows-x64.exe.sha256
   releases/vX.Y.Z/gift-panel-changelog.json
   releases/vX.Y.Z/release.json
   ```

4. 从 COS 重新读取或查询已上传对象，确认大小与 SHA-256 一致。
5. 最后覆盖 `channels/stable/latest.json`，把该写入作为国内版本正式可见的发布开关。
6. 调用国内最新版本接口，验证 tag、size 和 digest 与本次发布一致，但不在 CI 日志输出临时下载 URL。

任何前置步骤失败都不得更新 `channels/stable/latest.json`。GitHub Release 已成功而 COS 同步失败时，工作流标记失败；现有客户端仍可从 GitHub 更新，国内源保持上一有效版本。

COS 对象名包含精确版本，已发布的版本化对象禁止覆盖。修复同一 Git tag 时只有构建产物 SHA-256 与既有对象一致才允许重复执行；产物变化必须发布新版本。

## 5. 更新服务内部边界

新增独立 Go 更新服务，不与桌面应用进程或用户本地 HTTP 服务共用运行时。核心模块分为：

- `ReleaseStore`：读取私有 COS 中的稳定通道和更新日志，不负责 HTTP。
- `ReleaseValidator`：校验版本、对象前缀、文件名、大小和 SHA-256。
- `DownloadSigner`：只为验证后的固定 release asset 生成 10 分钟 COS 预签名 GET URL。
- HTTP handlers：实现第 3 节契约、稳定错误和缓存头。

服务缓存最后一次验证成功的 `latest.json`。正常缓存时间为 60 秒；COS 临时读取失败时可以继续使用进程内最后有效元数据并重新签发下载地址。服务刚启动且没有有效元数据时返回 `503`，由客户端回退 GitHub。

应用日志不得记录 COS 密钥、签名后的完整 URL、响应 JSON 或内部文件路径。

## 6. 客户端行为

默认更新源按以下顺序配置：

1. 国内更新 API，使用现有 `githubRelease` JSON 解析器，但不发送 GitHub 专用请求头。
2. 现有 GitHub `gift-panel-update.json`。

客户端继续比较所有成功响应中的稳定 SemVer，选择最高版本。同一最高版本的国内资源下载或校验失败时，尝试 GitHub 候选资源。

下载保持现有约束：

- 最多 256 MiB。
- 响应长度存在时必须与清单 size 一致。
- 落盘后必须匹配清单 SHA-256。
- 安装前使用 Windows Authenticode 信任验证检查签名链，并核对构建时配置的预期发布者名称；该名称必须与 Release 工作流验证通过的证书 Subject 一致，验证失败绝不替换当前 EXE。
- 更新替换仍使用 `.new`、`.old` 和失败回滚流程。

自动检查周期保持 6 小时，不增加设备 ID、账号、请求签名或客户端密钥。开源客户端中不存在可安全保密的通用 API 密钥，因此首版使用只读公开接口与服务端限流。

更新日志读取同样采用国内 URL 优先、GitHub URL 回退；任一源失败时继续保留客户端已有的本地或内存缓存。

## 7. 权限与密钥

使用两个独立的腾讯云 CAM 子账号或等价的独立凭证：

1. **CI 写入凭证**：仅允许向指定 bucket 的 `releases/*` 和 `channels/stable/latest.json` 写入及执行发布校验所需的最小读取操作。
2. **Lighthouse 读取凭证**：仅允许读取同一 bucket 的 `channels/stable/*`、更新日志和 `releases/*`，用于读取元数据及签发 GET URL；不得写入、删除或列出其他 bucket。

凭证不得写入 Git、构建产物、网页或客户端：

- CI 凭证保存在 GitHub Actions Secrets。
- Lighthouse 凭证保存在 root 所有、权限 `0600` 的 systemd EnvironmentFile。
- 服务进程使用专用低权限系统用户，不以 root 运行。
- 凭证轮换后通过健康检查和签名下载测试验证，再撤销旧凭证。

COS bucket 保持私有读写，关闭匿名访问。桌面 HTTP 客户端不依赖浏览器 CORS，因此首版不配置宽泛 CORS。

## 8. Nginx 与 HTTPS

- TCP 80 只用于 ACME 验证和重定向到 HTTPS。
- TCP 443 提供备案页和 API。
- TLS 使用已备案域名的有效证书，并配置自动续期和续期后 reload。
- API 限流为每来源 IP 平均 10 次/分钟、burst 20；超过限制返回 JSON `429`。
- 请求体上限保持极小；API 不读取请求体。
- 保留 `X-Content-Type-Options: nosniff`、`Referrer-Policy: no-referrer`、`X-Frame-Options: DENY` 和禁止框架嵌入的 CSP。
- 根备案页和 JSON 响应使用明确的 `Content-Type`。
- Nginx access log 不包含响应体；日志轮转保留 7 天。应用日志使用 systemd journal 的大小/时间上限。

## 9. 错误处理

API 使用稳定、无内部细节的 JSON 错误：

```json
{
  "code": "release_unavailable",
  "message": "更新信息暂时不可用",
  "request_id": "..."
}
```

- 清单不存在或首次启动无法读取：`503 release_unavailable`。
- 清单格式、对象路径、大小或 digest 无效：`503 release_invalid`，内部日志记录原因。
- 签名失败：`503 download_unavailable`。
- 超过限流：`429 rate_limited`。
- 未定义路径：`404 not_found`。
- 不支持的方法：`405 method_not_allowed`。

临时上游错误不得清空最后有效元数据。客户端遇到超时、非 200、JSON 无效、下载失败、大小不符、SHA-256 不符或 Authenticode 无效时，记录面向用户的稳定错误并尝试 GitHub 候选；两个来源均失败才进入最终错误状态。

## 10. CDN 决策

首版不启用 CDN，链路为：

```text
Lighthouse API → COS 私有桶预签名 URL → 客户端直连 COS
```

安装包流量不经过 Lighthouse。COS 预签名 URL 不能通过替换 hostname 直接用于 CDN；未来接入 CDN 必须同时设计私有 COS 回源鉴权和面向客户端的 CDN URL 鉴权，并重新评估缓存、刷新和费用。

满足任一条件时再启动独立 CDN 灰度设计：

- 月下载流量接近 500 GB；
- 新版本发布峰值持续超过 100 Mbps；
- 多地域真实用户持续报告直连 COS 下载慢；
- 监控显示下载失败率或耗时达到产品不可接受水平。

前两个数值是工程评估触发点，不是腾讯云官方门槛。启用前必须对比 `CDN 下行 + COS 回源 + 请求费用` 与直连 COS 成本，并以真实地区、运营商和文件大小进行灰度测试。

## 11. 测试与验收

### Go 更新服务

- 清单合法性、版本号、固定文件名、对象前缀、大小上限和 SHA-256 校验。
- 正常读取、60 秒缓存、COS 失败时使用最后有效元数据、冷启动失败。
- 预签名 URL bucket/region/path 和 10 分钟有效期。
- GET、HEAD、404、405、429、503、Content-Type、Cache-Control 和错误隐私。
- 更新日志 schema、ETag 和缓存行为。

### 客户端

- 国内源排在 GitHub 前面。
- 国内源成功、国内源失败回退 GitHub、同版本国内下载失败回退 GitHub。
- 国内源版本低于 GitHub 时选择 GitHub 高版本。
- 大小、SHA-256、最大文件限制和重定向行为。
- Authenticode 有效、无签名、错误发布者、损坏签名和验证失败不安装。
- 国内与 GitHub 更新日志的优先级和缓存回退。

### 发布工作流

- 使用测试 bucket/prefix 验证不可变上传、哈希回读和 latest 最后写入。
- 任一步骤失败时 stable 指针不变化。
- 重跑相同产物幂等；相同版本不同 hash 被拒绝。
- 端到端验证国内 API 返回本次 tag、size、digest，并能下载和校验 EXE。
- CI 日志和 artifact 不包含腾讯云凭证或临时下载 URL。

### 部署验收

- Nginx 配置检查、systemd 自动启动和非 root 运行。
- HTTP 跳转 HTTPS，证书链和自动续期演练成功。
- 根路径只有极简备案页，备案号和链接准确。
- 公网无法访问 `/healthz`、服务监听端口或目录列表。
- 限流按预期返回 429，正常客户端检查不被误伤。
- 从至少两个中国大陆网络环境验证清单和真实安装包下载。

## 12. 部署与回滚

部署前备份现有 Nginx 配置和 `/var/www/gift-panel`。更新服务使用带版本号的二进制路径和稳定 symlink；systemd 切换新版本后执行本机健康检查与公网 API 合约检查，成功后才完成部署。

回滚时恢复旧 symlink 和 Nginx 配置，不修改 COS 已发布的不可变对象。若最新通道元数据有误，将 `channels/stable/latest.json` 恢复为上一个已验证版本；客户端随后读取旧版本并保持现有已安装版本，不执行降级。

## 13. 完成标准

- 已备案域名通过 HTTPS 提供极简备案页和两个版本化 API。
- Lighthouse 不代理安装包，不暴露 Go 监听端口、健康检查或凭证。
- GitHub Actions 能自动、原子地把已签名版本发布到私有 COS。
- 客户端优先使用国内更新源，所有失败路径均能回退 GitHub。
- 下载同时通过 size、SHA-256 和 Authenticode 发布者验证。
- 未定义路径、错误、限流、缓存和日志行为符合本设计。
- 首版不启用 CDN，并保留明确的后续评估触发条件。
- 全部单元、集成、发布和部署验收通过，可从上一版本配置与 stable 元数据快速回滚。
