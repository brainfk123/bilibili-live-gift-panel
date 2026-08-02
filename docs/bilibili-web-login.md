# B 站主播可选网页登录方案研究

> 调研日期：2026-08-02
>
> 范围：不申请哔哩哔哩开放平台开发者资格，使用 B 站现有网页扫码登录能力，为本地直播礼物面板提供可选主播登录态。
>
> 证据范围：B 站第一方网页、第一方接口的实际响应、当前线上前端脚本。没有使用二手教程作为结论依据。

## 结论摘要

推荐实现“由本地后端管理的网页扫码登录”：后端请求 B 站二维码接口并轮询，使用独立 Cookie Jar 接收登录 Cookie；前端只显示二维码、登录进度和已登录账号，永远接触不到 Cookie。凭证在 Windows 上使用当前用户作用域的 DPAPI 加密后落盘。

这个方案不需要开放平台开发者资格，操作也接近 B 站网页自身的扫码登录。不过它调用的是 B 站网页内部接口，不是面向第三方承诺稳定的开放 API，因此必须接受接口、风控参数和前端协议随时变化的维护成本。

登录态有明确价值，但不能把它简化成“给 WebSocket 填入主播 UID 后，旧版 `SEND_GIFT.uname` 就一定变完整”：

- B 站直播间当前前端会用登录用户 UID 建立弹幕连接；主播账号还会获得查看匿名用户资料的权限。
- 当前前端的礼物处理优先使用 `sender_uinfo.uid` 和 `sender_uinfo.base.name`，再回退到旧字段 `uid`、`uname`。
- 匿名资料协议还包含 `anon_uid`、`anon_key_ver`、`uinfo_cipher` 和 `masked`。官方前端会结合房间匿名密钥处理这些字段，并把结果注入 `uid`、昵称。
- 因此推荐范围应包含：主播身份校验、带登录态取得房间用户信息与匿名密钥、现代 `sender_uinfo` 解析/解密、旧字段回退。只改 WebSocket 鉴权参数是不完整的。
- 尚未用真实主播账号完成“同一礼物、匿名连接与主播连接”的 A/B 抓包，所以不能承诺登录后所有房间、所有礼物都会返回未脱敏昵称。未恢复资料时，应直接显示 B 站给出的 `反***` 等脱敏值，不能丢弃礼物事件。

## 一手证据

### 1. 网页扫码登录流程

B 站当前[网页登录页](https://passport.bilibili.com/login)使用以下接口：

- `GET https://passport.bilibili.com/x/passport-login/web/qrcode/generate`
- `GET https://passport.bilibili.com/x/passport-login/web/qrcode/poll?qrcode_key=...`

2026-08-02 对生成接口的匿名实际请求返回 `code: 0`，`data` 中包含二维码 URL 和 `qrcode_key`。对尚未扫码的 key 轮询返回：

```json
{
  "code": 0,
  "data": {
    "url": "",
    "refresh_token": "",
    "timestamp": 0,
    "code": 86101,
    "message": "未扫码"
  }
}
```

当前[登录页前端脚本](https://s1.hdslb.com/bfs/static/2233-monorepo/passport/static/js/async/476.c21ae863.js)显示：

- 二维码生成成功后开始轮询，间隔约 2 秒。
- `86101`：未扫码，继续轮询。
- `86090`：已扫码，等待手机确认，继续轮询。
- `86038`：二维码失效，停止轮询并允许刷新。
- `0`：登录成功，停止轮询；成功数据包含 `url`、`refresh_token` 和 `timestamp`，随后进入网页凭证设置流程。

实现时应复用同一个 HTTP Cookie Jar 完成生成与轮询，以便接收服务端在成功响应中设置的 Cookie。二维码图片只需由前端根据 `data.url` 本地生成，不应把二维码内容上传给任何第三方二维码服务。

### 2. 登录状态、刷新和退出

登录状态可以通过第一方接口检查：

```text
GET https://api.bilibili.com/x/web-interface/nav
```

2026-08-02 的匿名实际响应为 `code: -101`、`data.isLogin: false`。登录后应只以该接口的实际结果为准，并读取 `data.mid`、昵称和头像用于账号展示；不要仅凭本地存在 Cookie 就宣称“已登录”。接口来源同时可在[当前首页前端脚本](https://s1.hdslb.com/bfs/static/shanks/laputa-home/assets/index-8d3bb9df.js)中确认。

Cookie 刷新检查使用：

```text
GET https://passport.bilibili.com/x/passport-login/web/cookie/info
```

未登录时实际返回 `code: -101`。当前首页脚本在登录成功且支持 token 刷新时调用它；当 `data.refresh` 为真，脚本加载 B 站的 WASM 并进入 `https://www.bilibili.com/correspond/1/...` 刷新流程。登录页成功处理也会使用 `refresh_token`、`timestamp` 和 `correspond/0/...`。

这说明生命周期由服务端 Cookie 属性、`nav` 结果和 `cookie/info` 共同驱动。公开一手材料没有给出可以安全硬编码的“固定有效天数”，所以实现不应假设 SESSDATA 永远是某个固定期限：

1. 启动时调用 `nav`。
2. 建立或重建直播连接前再次检查。
3. 后台低频检查，例如每 30–60 分钟一次；遇到 `-101` 或鉴权错误立即标记失效。
4. 第一版可以在失效时要求重新扫码。若以后实现自动刷新，应完整复现并测试当前 `cookie/info`/`correspond`/`refresh_token` 流程，而不是自行延长 Cookie。

当前首页脚本的退出行为是：读取 Cookie 中的 `bili_jct`，以表单字段 `biliCSRF` POST 到：

```text
POST https://passport.bilibili.com/login/exit/v2
Content-Type: application/x-www-form-urlencoded
```

本地“退出登录”应先调用该接口，再清空内存 Cookie Jar、加密凭证文件、`refresh_token`、用户资料和匿名解密缓存。如果服务端退出失败，也应允许用户删除本机凭证，但必须提示“B 站服务端会话未确认注销”。

### 3. 直播 WebSocket 鉴权

当前[直播播放器脚本](https://s1.hdslb.com/bfs/static/bilibili-live-player/room-player.a9e3517e.prod.min.js)会请求：

```text
GET https://api.live.bilibili.com/xlive/web-room/v1/index/getDanmuInfo?id={roomId}&type=0
```

当前网页请求同时启用了 WBI 和风险验证处理。得到 `token` 和 `host_list` 后，播放器构造的连接参数包括：

```text
rid      = 房间真实 ID
uid      = 当前登录用户 ID，未登录为 0
protover = 3
platform = web
type     = 2
key      = getDanmuInfo 返回的 token
```

浏览器会自动把适用于 `.bilibili.com` 的 Cookie 带到相关 HTTPS/WSS 请求；原生 Go 客户端需要显式复用同一个 Cookie Jar，并在允许的请求中发送对应 Cookie。不要把 `SESSDATA` 放进 WebSocket 认证 JSON，也不要把任何登录 Cookie发给非 B 站域名。

实际匿名请求还显示：只带普通 User-Agent、Referer 甚至 `buvid3`/`buvid4`，`getDanmuInfo` 仍可能返回 `-352`。因此登录不是风控的万能绕过方式；实现仍需保持 B 站签发的设备 Cookie、正确请求上下文、受控退避和明确的风控错误状态。

### 4. `SEND_GIFT` 脱敏与主播权限

当前[直播间业务前端脚本](https://s1.hdslb.com/bfs/static/blive/blfe-live-room/static/js/app.a2aac83143f4e373b80d.js)提供了比旧 `uname`/`uid` 字段更完整的证据：

- 权限常量包含 `ANONYMOUS_VIEW_PROFILE = 100`。
- 当前用户是主播（`baseInfoUser.isAnchor`）时，前端直接把 `ANONYMOUS_VIEW_PROFILE` 加入用户权限。
- 房间开启匿名模式、查看者不是本人且没有该权限时，前端的显示函数会把昵称变为“第一个字符 + `***`”。这与实际看到的 `反***` 一致。
- 通用匿名用户结构包含 `anon_uid`、`anon_key_ver`、`uinfo_cipher`、`masked`。
- 处理器会使用房间信息里的 `room_anonymous.anon_key` 和 `anon_key_ver` 处理匿名资料，并在成功时写回 `uid` 与 `base.name`。
- 礼物组合消息处理优先读取 `sender_uinfo.uid`、`sender_uinfo.base.name`，没有时才使用旧的 `uid`、`uname`。

房间用户上下文由同一脚本中的第一方接口取得：

```text
GET https://api.live.bilibili.com/xlive/web-room/v1/index/getInfoByUser?room_id={roomId}
```

这意味着登录态最重要的作用不仅是 WebSocket 的 `uid`，而是让 B 站识别“该登录用户就是这个房间的主播”，进而返回主播权限及匿名资料处理所需上下文。推荐在启用登录态前比较：

```text
nav.data.mid == 房间主播 UID
```

若不相等，应显示“登录账号不是当前房间主播”，并且不要声称可以解除脱敏。

### 5. 匿名主页查询仍可作为补充，不应成为主链路

2026-08-02 对用户 `32249588` 做了两类匿名实际请求：

- 无稳定浏览器标识的直接请求可能收到 `-352` 风控。
- 先通过 B 站第一方指纹接口取得 B 站自己签发的 `buvid3`/`buvid4`，再请求 `https://api.bilibili.com/x/web-interface/card?mid=32249588`，实际返回 `code: 0`、完整昵称“反重力鱼”和头像。

因此公开主页补全并不必然需要登录，但它受设备 Cookie、风控和接口策略影响，不能按每份礼物同步请求。更高效的顺序应是：

1. 优先使用已解析的 `sender_uinfo`。
2. 再使用事件旧字段中未脱敏的昵称和头像。
3. 有真实 UID 时，使用同一个 Cookie Jar 异步查询主页，并按 UID 长时间缓存成功结果、短时间缓存失败结果。
4. 查询失败、只拿到头像或昵称仍脱敏时，保留原始脱敏昵称；礼物规则照常执行。

## 方案比较

| 方案 | 主播操作 | 安全性 | 生命周期/退出 | 对解除脱敏的帮助 | 结论 |
| --- | --- | --- | --- | --- | --- |
| 本地后端网页扫码登录 | 点击登录、手机扫码确认 | 可把 Cookie 限制在后端并加密存储 | 可用 `nav`、`cookie/info`、`exit/v2` 管理 | 能建立主播身份，并为官方 `sender_uinfo`/匿名权限流程提供条件 | 推荐 |
| 打开系统浏览器登录后自动导入 Cookie | 操作少 | 需要读取浏览器 Cookie 数据库/系统密钥，权限过大 | 多浏览器差异大，退出关系不清晰 | 可能有效 | 不推荐 |
| 用户手工粘贴 Cookie/SESSDATA | 实现简单但体验差 | 极易粘贴到日志、截图或错误位置 | 缺少完整 Cookie Jar 和刷新状态 | 不稳定 | 不推荐 |
| 内嵌账号密码/短信验证码登录 | 输入步骤多 | 应用接触密码、手机号和验证码，风险明显增大 | 风控、验证码流程复杂 | 与扫码结果相近 | 排除 |
| 开放平台 OAuth/直播开放平台 | 标准化 | 官方授权边界清晰 | 官方 token 生命周期 | 取决于开放能力 | 本需求已明确排除开发者资格路径 |

## 推荐实现设计

### 登录状态机

```text
未登录
  → 生成二维码
  → 等待扫码（86101）
  → 等待确认（86090）
  → 登录成功（0）
  → nav 校验登录账号
  → 校验账号是否为当前房间主播
  → 持久化加密 Cookie Jar

任意等待状态 → 二维码过期（86038）→ 允许刷新
已登录 → nav=-101/鉴权失败 → 凭证失效 → 重新扫码
已登录 → 用户退出 → exit/v2 → 清除本地凭证
```

### 后端职责

- 生成二维码、按约 2 秒间隔轮询，离开登录界面或二维码过期后立即取消。
- 维护一个只用于 B 站域名的 Cookie Jar；Cookie 域名、Secure、Expires/Max-Age 由服务端响应决定。
- 保存轮询成功返回的 `refresh_token` 和 `timestamp`，但第一版不必冒险实现未经验证的自动刷新。
- 使用 `nav` 获取登录状态和 `mid`，使用房间信息验证主播身份。
- 用登录 Cookie 请求 `getInfoByUser`、`getDanmuInfo`，WebSocket 认证使用真实主播 UID。
- 解析现代 `sender_uinfo`；房间匿名资料按当前官方前端协议处理。任何解析失败都回退到原始脱敏值。
- 前端 API 只能获得 `{ loggedIn, mid, name, face, isRoomOwner, expiresState }` 等状态，不得返回原始 Cookie 或 refresh token。

### 本地存储与日志安全

- Windows 上使用 DPAPI `CurrentUser` 作用域加密完整凭证包；配置文件只保存加密 blob 和版本号。
- 限制文件 ACL 为当前 Windows 用户；写入使用临时文件加原子替换，避免半写入。
- 日志统一脱敏这些字段：`SESSDATA`、`bili_jct`、`DedeUserID`、`refresh_token`、完整 `Cookie`/`Set-Cookie`、二维码 URL、`qrcode_key`。
- 错误日志只记录接口名、B 站业务码、HTTP 状态、重试次数和 trace id，不记录请求头与响应原文。
- 本地服务只监听 `127.0.0.1`。登录/退出本地接口需要校验同源和一次性 CSRF nonce，避免恶意网页调用本机服务发起登录或退出。
- 二维码过期、页面关闭或用户取消时，取消轮询并删除内存中的 key；不要无限轮询。
- 不把 Cookie 交给 OBS 页面。OBS 页面仍只订阅后端计算后的属性和播报数据。

## 验收建议

在真正宣称“登录可解除脱敏”前，需要主播自愿扫码后做一次受控 A/B 测试：

1. 同一房间同时建立匿名连接和主播登录连接。
2. 发送一份测试礼物，保存两边的字段名与“是否为空/是否脱敏”，但不要保存 Cookie 或私密字段内容。
3. 比较 `uid`、`uname`、`sender_uinfo`、`anon_uid`、`anon_key_ver`、`uinfo_cipher`、`masked`。
4. 验证登录连接能取得主播身份、`ANONYMOUS_VIEW_PROFILE` 所需上下文和房间匿名密钥，并按官方流程恢复 `sender_uinfo`。
5. 退出后重连，确认服务不再发送登录 Cookie，且回到脱敏显示。
6. 重启 EXE，确认 DPAPI 凭证只能由同一 Windows 用户解密；配置页面和日志中搜索不到 Cookie、refresh token 和二维码 key。

如果 A/B 测试显示 B 站仍只下发脱敏资料，应保留登录功能用于降低主页查询风控和稳定鉴权，但 UI 必须明确显示“B 站未提供完整昵称”，不能尝试绕过 B 站的隐私策略。

## 已知限制

- 本次没有让真实主播账号扫码，因此没有记录成功登录响应的具体 `Set-Cookie` 属性，也没有做真实主播连接与匿名连接的礼物消息 A/B。结论已明确区分“源码支持的能力”与“仍需实测的结果”。
- 网页内部接口和静态脚本没有第三方稳定性承诺；文件 hash、字段和风控都可能变化。
- 用户公开主页可访问不等于所有观看者都有权查看匿名送礼者的完整资料。应用应遵循 B 站返回的权限和脱敏结果。
- B 站[隐私政策](https://www.bilibili.com/blackboard/privacy-policy.html)明确 Cookie 用于识别注册用户身份；对本工具而言，登录 Cookie 应按账号凭证而非普通配置处理。

## 第一方来源

- [B 站网页登录页](https://passport.bilibili.com/login)
- [登录页当前扫码与 token 处理脚本](https://s1.hdslb.com/bfs/static/2233-monorepo/passport/static/js/async/476.c21ae863.js)
- [二维码生成接口](https://passport.bilibili.com/x/passport-login/web/qrcode/generate)
- [登录状态接口](https://api.bilibili.com/x/web-interface/nav)
- [Cookie 刷新状态接口](https://passport.bilibili.com/x/passport-login/web/cookie/info)
- [首页当前登录状态、刷新和退出脚本](https://s1.hdslb.com/bfs/static/shanks/laputa-home/assets/index-8d3bb9df.js)
- [B 站直播间](https://live.bilibili.com/31567150)
- [直播播放器当前 WebSocket 脚本](https://s1.hdslb.com/bfs/static/bilibili-live-player/room-player.a9e3517e.prod.min.js)
- [直播间当前礼物与匿名用户处理脚本](https://s1.hdslb.com/bfs/static/blive/blfe-live-room/static/js/app.a2aac83143f4e373b80d.js)
- [用户公开资料接口样本](https://api.bilibili.com/x/web-interface/card?mid=32249588)
- [哔哩哔哩隐私政策](https://www.bilibili.com/blackboard/privacy-policy.html)
