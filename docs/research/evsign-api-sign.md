# EV Sign API 签名服务 — 官方文档研究结论

> 研究日期：2026-08-06
> 来源范围：仅使用 EV Sign 官方文档 wiki.evsign.cn 及其直接链接的官方页面（未接触任何密钥、私有代码或个人数据）。
>
> 来源页面：
> - https://wiki.evsign.cn/api-sign （API 签名主文档）
> - https://wiki.evsign.cn/cli （API 页面直接链接的 CLI 秒签文档）
> - https://wiki.evsign.cn/faq （常见问题，文档中心导航链接）
> - https://wiki.evsign.cn/start （基础知识，文档中心导航链接）
> - https://wiki.evsign.cn/types （证书类型说明，文档中心导航链接）

## 1. 鉴权字段（请求头）

来源：https://wiki.evsign.cn/api-sign

| 请求头 | 必填 | 说明 | 缺省值 |
|---|---|---|---|
| `X-Key` | 必须 | 许可证编号（UUID 格式 `XXXXXXXX-XXXX-XXXX-XXXX-XXXXXXXXXXXX`） | — |
| `X-Action` | 必须 | 值必须为 `api-sign` | — |
| `X-File-Name` | 否 | 文件名，需先 URL 编码再传递 | 随机名称 |
| `X-Algorithm` | 否 | 签名摘要算法：`sha1` / `sha256` / `sha384` / `sha512` | `sha256` |
| `X-Cert` | 否 | 指定签名证书 ID | 默认证书 |
| `X-Timestamp` | 否 | 指定时间戳服务器 | `auto` |
| `X-Password` | 否 | 签名密码（证书无密码可留空） | 空 |
| `X-Append` | 否 | 追加签名 `yes` / `no` | `no` |

## 2. API 地址与请求方法

来源：https://wiki.evsign.cn/api-sign

- 接口地址：`https://api.evsign.cn/v1`
- 请求方法：`POST`
- 请求体：完整文件二进制流（**不是 form-data**）；官方示例使用 `Content-Type: application/octet-stream`

## 3. 签名任务提交方式

来源：https://wiki.evsign.cn/api-sign

- 单次同步请求完成“上传 → 签名 → 返回已签名文件”，无任务 ID、无异步任务提交接口。
- 一次性把整个文件作为请求体 POST 到 `/v1`，成功时响应体直接就是签名后的文件。
- 官方提醒：API 需先上传完整文件、再下载已签名文件，对网络要求较高，大文件签名耗时较长；官方建议大文件改用 CLI（秒签，https://wiki.evsign.cn/cli）。

## 4. 文件上传 / 下载方式

来源：https://wiki.evsign.cn/api-sign

- 上传：原始二进制流作为请求体（`--data-binary @文件` / `data=f` / `req.write(buffer)`），不是 multipart/form-data。
- 下载：HTTP `200` 时响应体即签名后的文件（二进制流），需自行保存为文件。
- 失败时（非 200）：响应体为纯文本错误信息，应读取并输出给用户。

## 5. 轮询状态

来源：https://wiki.evsign.cn/api-sign（全文检索，未发现任何轮询/任务状态接口）

- **文档不存在轮询接口**：API 是同步请求，一次 POST 阻塞到签名完成并直接返回文件。
- 调用方无需（也无法）轮询；等待响应即可。超时/中断时按失败处理并重试整次请求。

## 6. 错误处理

来源：https://wiki.evsign.cn/api-sign

- `200` = 成功（响应体为签名后文件）。
- 其他状态码 = 失败，响应体为纯文本错误信息（如无效 X-Key、证书/密码错误等）。
- 官方示例的统一模式：`status_code == 200` 写文件，否则打印 `response.text`。
- CLI 版本（https://wiki.evsign.cn/cli）返回码：`0` = 成功，`1` = 签名失败（查看错误输出）；CLI 退出码可作为 GitHub Actions 步骤判定的参考。

## 7. 限流 / 安全注意事项

- **官方文档未公布限流配额**（api-sign、cli、faq、types 等页面均无限流说明）。建议调用方自行做保守的重试退避（如指数退避）与并发控制，避免短时间大量提交。
- 密钥安全：`X-Key` 是许可证 UUID，等同签名凭证；应只存于 GitHub Actions Secrets（如 `EVSIGN_KEY`），不要写入仓库、日志或 workflow 明文。
- 传输安全：接口为 HTTPS（`https://api.evsign.cn/v1`），不要在非加密通道传递 X-Key 或 X-Password。
- `X-Password`（签名密码）如证书有密码则必须提供；建议同样以 Secret 注入，避免明文出现。
- 大文件注意：单请求全量上传，大文件耗时/易超时，官方建议大文件用 CLI（https://wiki.evsign.cn/cli）；CDN 缓存问题见 https://wiki.evsign.cn/faq（更新文件后需刷新 CDN 缓存，否则用户下载到旧文件）。
- 证书吊销说明见 https://wiki.evsign.cn/types：共享证书（标准/高稳）吊销频率较高，吊销后 24 小时内替换新证书——长链路自动化需留意证书更换窗口。

## 8. 适合 GitHub Actions 的完整调用序列

依据官方文档的同步请求模型设计。密钥只通过 `secrets.EVSIGN_KEY` 注入，不落明文。

```yaml
name: evsign-sign

on:
  workflow_dispatch:
  release:
    types: [published]

jobs:
  sign:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4

      # 1. 构建/准备待签名文件（示例占位）
      - name: Build artifact
        run: |
          # 在此构建你的安装包，产出 out/Setup_v1.0.0.exe
          mkdir -p out
          cp some-installer.exe out/Setup_v1.0.0.exe

      # 2. 上传签名文件到 API 并保存签名结果
      #    序列：URL 编码文件名 → POST 二进制流 → 200 保存 / 非 200 打印错误并失败
      - name: Sign via EV Sign API
        env:
          EVSIGN_KEY: ${{ secrets.EVSIGN_KEY }}
          # 证书有密码时再加一个 Secret，无密码可省略 X-Password
          # EVSIGN_PASSWORD: ${{ secrets.EVSIGN_PASSWORD }}
        run: |
          set -euo pipefail
          src="out/Setup_v1.0.0.exe"
          dst="out/Setup_v1.0.0_signed.exe"

          # 文件名需 URL 编码后放入 X-File-Name
          encoded=$(python3 -c "import urllib.parse,sys; print(urllib.parse.quote(sys.argv[1]))" "$src")

          args=(
            -sS -X POST "https://api.evsign.cn/v1"
            -H "X-Key: ${EVSIGN_KEY}"
            -H "X-Action: api-sign"
            -H "X-Algorithm: sha256"
            -H "X-File-Name: ${encoded}"
            -H "Content-Type: application/octet-stream"
            --data-binary "@${src}"
            -o "${dst}"
            -w "%{http_code}"
          )

          code=$(curl "${args[@]}")
          if [ "$code" != "200" ]; then
            echo "签名失败 (HTTP $code)："
            cat "${dst}"   # 非 200 时响应体为纯文本错误信息
            exit 1
          fi
          echo "签名成功：${dst}"

      # 3. （可选）上传签名结果到 Release / artifact
      - name: Upload signed artifact
        uses: actions/upload-artifact@v4
        with:
          name: signed-installer
          path: out/Setup_v1.0.0_signed.exe
```

要点：
- 整个签名是一个同步请求，无轮询步骤；workflow 内等待 curl 返回即可。
- 失败判定：HTTP 非 200 → 读取响应体文本并 `exit 1`，让 job 失败（与官方错误处理一致）。
- 建议为 curl 增加 `--retry 3 --retry-delay 5`（官方未给限流值，重试需保守）。
- 大文件场景官方建议改用 CLI（https://wiki.evsign.cn/cli）：workflow 中下载 `https://mc.evsign.cn/evsign-client-cli-linux-latest` 后执行 `evsign-client "文件" -key $EVSIGN_KEY`，退出码 0/1 判断成败。

## 9. 关键结论清单

1. 鉴权：`X-Key`（许可证 UUID）+ `X-Action: api-sign` 两个必填头；其余头（算法、证书、时间戳、密码、追加签名、文件名）均可选。
2. 地址与方法：`POST https://api.evsign.cn/v1`，请求体为文件二进制流（非 form-data）。
3. 提交方式：同步单请求，无任务 ID、无异步接口；响应即结果。
4. 上传/下载：上传 = 原始二进制流；下载 = 200 响应体直接存文件；失败 = 纯文本错误。
5. 轮询：官方文档没有轮询接口，无需也不支持轮询。
6. 错误处理：200 成功，其余失败且响应体为文本错误；CLI 返回码 0/1。
7. 限流：官方文档未公布限流/配额，需自做保守重试与并发控制；X-Key 视为敏感凭证，仅经 Secrets 注入。
8. GitHub Actions：build →（可选 artifact）→ 单次 POST 签名 → 按状态码判定 → 上传签名产物；大文件走官方 CLI。

## 10. 本项目的签名重试与 FFmpeg 组件复用

本项目的 `scripts/sign-evsign.mjs` 使用自管 HTTPS deadline，避免 Node `fetch` 内部约 5 分钟的 headers timeout 早于外层中止计时器。默认策略固定为：

- 最多 3 次；每次总 deadline 10 分钟；第 2、3 次前等待 15 秒、45 秒。
- 仅重试 DNS/连接/TLS 中断、超时、HTTP 408、429 与 5xx。
- 其他 4xx、空响应、超限响应和本地文件错误立即失败。
- 每次上传原始未签名字节；完整 200 响应先写唯一临时文件，再原子替换目标。
- 日志不输出许可证、密码、证书选择器、请求头、响应正文或文件内容。

EV Sign 公开 API 文档没有幂等键或签名任务查询接口。因此服务端已经完成、客户端却丢失响应时，重试可能产生额外签名记录；客户端只能保证本地产物原子性，不能宣称服务端 exactly-once。

固定 FFmpeg 采用不可变组件 Release：当前标签为 `ffmpeg-component-v2-<64位指纹>`。v2 指纹覆盖源码、SOURCE_DATE_EPOCH、configure、工具链锁、组件策略以及完整预期 Authenticode Subject 的 SHA-256；更换签名证书主体必然产生新的组件标签和缓存未命中。缓存命中时跳过 MSYS2、FFmpeg 编译和内层签名；下载的所有组件资产必须先通过 GitHub artifact attestation、SHA-256 闭包、严格 manifest、组件门禁、Authenticode 精确签名者和真实 FFmpeg 运行面验证。旧 `ffmpeg-component-v1-*` 组件保持不可变，仅用于历史审计，不会被 v2 工作流复用。

组件 Release 不允许覆盖或 `--clobber`。已存在但不完整、校验失败或证明失败时，发布立即停止并要求人工恢复；不得静默改走重新构建。只有 GitHub API 对精确组件标签返回 404 才能进入首次构建/签名路径。
