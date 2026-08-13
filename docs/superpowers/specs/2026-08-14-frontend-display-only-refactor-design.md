# 前端纯显示化重构设计

**状态：** 已确认设计，待实施计划  
**日期：** 2026-08-14  
**基线：** `2d92c7a`（v0.4.3 发布准备分支的已验证 HEAD）

## 1. 目标与范围

本次重构只完成三项工作：

1. 用 Go 测试补齐旧 TypeScript 规则引擎和公式求值器承载的语义安全网。
2. 删除没有运行时调用方的 TypeScript 规则引擎与公式求值实现，只保留编辑草稿引用检测所需的 `collectVars` 解析链。
3. 把盲盒排行榜的汇总、过滤、排序和分项统计迁移到 Go；前端只读取权威快照并渲染。

本轮明确不包含：

- 配置导入事务化与 `mergeConfigBackup` 收窄；它需要独立的服务端导入协议。
- 展示页 750ms 轮询改 SSE；它需要多订阅者广播、重连和 OBS CEF 降级设计。
- 礼物剪裁工作室、玩法模板、公式预设、训练中心、简单模式、主题、场景、格式化和广播队列。
- 非盲盒的“礼物贡献”和“规则命中”列表展示排序；它们只是当前权威台账的单页 view-model，不生成持久化状态，也不跨客户端复用。

## 2. 架构原则

Go HTTP 接口继续作为唯一权威 external seam。前端 UI 不直接 `fetch`，只通过 `src/backend.ts` 的 adapter 访问服务端。

盲盒排行榜采用一个深 Go 模块：调用方只提交贡献台账和查询选项，模块内部完成数据清洗、scope 聚合、单 scope 投影、完整 summary、稳定排序及行数裁剪。删除这个模块时，复杂度会重新散落到多个前端调用点，因此该 seam 具有实际深度与 locality。

前端允许保留的逻辑仅限：

- 数值、金额、时间与主题格式化；
- 从服务端已经排好序的快照中选择当前 scope、限制可见区域、渲染和滚动；
- “礼物贡献”和“规则命中”两个非盲盒标签页对单个权威台账快照进行本地展示排序；
- 编辑草稿引用检测 `collectVars`。

前端不得重新计算盲盒 summary、viewer 排名、scope 计数或单盲盒分项值。

## 3. 阶段 0：语义安全网

阶段 0 只增加或强化 Go 测试，不删除 TypeScript 代码。旧测试语义按下表归档：

| 旧 TS 语义 | Go 侧验收 |
|---|---|
| `num` 按购买数量逐次执行 | 批量事件执行次数、规则次数、属性变化均逐次累计 |
| `minPrice` | 价格低于门槛不触发，达到门槛触发 |
| `dailyLimit` | 同一规则到达日上限后停止，批量事件不能越界 |
| `cap` | 上升不超过 cap；已在 cap 时仍允许负向公式下降 |
| 公式错误 | 单条坏公式不修改状态、不产生成功日志，后台继续处理后续事件 |
| 礼物别名 | 当前 `sameGiftIdentity` 的 catalog 语义保持 |
| 日志裁剪 | 礼物日志与定时日志只保留最新 200 条，顺序不反转 |
| 日统计 | 礼物总数、规则次数和当天桶正确累计；新日期初始化新桶且不破坏历史桶 |
| 公式求值 | 补齐除零、未知变量、移除的 `count`、尾随垃圾、缺括号、`MAX`、`RAND`/`RANDBETWEEN` 范围、IF false 分支和嵌套函数 |

以下旧结构不做逐行移植：

- 前端 `Engine` 的 WebSocket、房间和连接状态已由 Go background supervisor、durable inbox 与 runtime health 取代。
- `resetTodayStats` 已由 Go 的按日期分桶模型取代。测试目标是日期桶语义，不恢复同名 helper。
- `pruneLog` 的客户端缓存实现不是权威 seam；只验证服务端持久化后的日志上限与顺序。

进入阶段 1 前，Go 全量测试与现有前端全量测试必须同时通过。

## 4. 阶段 1：删除 TypeScript 死代码

删除：

- `src/engine/index.ts`
- `src/engine/rules.ts`
- `src/formula/evaluator.ts`
- `tests/engine.test.ts`
- `tests/formula.test.ts` 中只针对 `evalFormula` 的用例

调整：

- `src/formula/index.ts` 只导出 `collectVars` 与 `FormulaError`。
- `tests/gameplay-templates.test.ts` 不再运行已删除的 TS evaluator。模板公式的可执行语义由阶段 0 的 Go evaluator 测试承担；该测试只保留模板结构、变量集合和预期字段检查。

保留：

- `src/formula/tokenizer.ts`
- `src/formula/parser.ts`
- `src/formula/errors.ts`
- `collectVars` 测试
- `src/simple-play.ts` 的草稿引用守卫
- `src/gifts/catalog.ts` 的运行时 `sameGiftIdentity`

阶段完成时，`src/` 和仍保留的前端测试中不得引用 `evalFormula`、`applyGiftToState`、`recordGiftTotals` 或 `resetTodayStats`。

## 5. 阶段 2.1：Go 盲盒排行榜模块

### 5.1 内部 interface

新增 `goserver/blind_box_leaderboard.go`，核心 interface 为：

```go
type blindBoxLeaderboardQuery struct {
    GiftID   int
    Limit    int
    HasLimit bool
}

func buildBlindBoxLeaderboard(
    ledger contributionLedgerState,
    query blindBoxLeaderboardQuery,
) blindBoxLeaderboardSnapshot
```

`GiftID == 0` 表示全部盲盒；正数表示单一盲盒。`HasLimit == false` 返回全部排名行；`HasLimit == true` 时 `Limit >= 0`，只裁剪 `viewers`，不裁剪 summary 或 scopes。贡献台账最多 2000 位观众，因此模块无需新增分页状态或缓存层。

模块返回：

```json
{
  "updatedAt": 0,
  "summary": {
    "viewerCount": 0,
    "blindBoxCount": 0,
    "cost": 0,
    "value": 0,
    "profit": 0,
    "unpricedCount": 0
  },
  "viewers": [],
  "scopes": []
}
```

`viewers` 复用现有 `viewerContribution` JSON 形状，但单盲盒查询必须把 blind-box 字段投影为该 scope 的 count/cost/value/profit/unpricedCount/lastGiftAt；礼物总数、规则次数等非盲盒字段保持原台账值，前端不得用这些字段推导单 scope 排名。

### 5.2 精确语义

- 无任何盲盒记录的 viewer 从排行榜排除。
- summary 基于裁剪前的全部 eligible viewers 计算。
- viewer 排序依次为 profit、value、count、lastGiftAt 降序；完全相同时使用稳定排序，保持台账中的原始顺序。
- scope 按总 count、最新 lastGiftAt 降序，再按 `zh-CN` 名称升序；名称相等时按 giftId 升序，保证确定性。
- 中文名称比较使用 Go 的 Unicode 中文 collation 实现，不以 UTF-8 字节序替代现有 `localeCompare('zh-CN')` 语义。若引入 `golang.org/x/text/collate`，版本必须精确写入 `go.mod`/`go.sum` 并接受依赖审计。
- 同 giftId 的名称取最新 `lastGiftAt` 对应名称；时间相等时后出现的合法名称获胜，与当前 TS 行为一致。
- 空名称回退为 `盲盒 <giftId>`。
- 非有限数或负 count 不得污染汇总；归一为零并从 eligibility 判断中排除。

### 5.3 HTTP interface

新增：

```text
GET /api/blind-box/leaderboard?giftId=<positive integer>&limit=<0..2000>
```

- `giftId` 可省略；省略表示全部盲盒。
- `limit` 可省略；省略返回全部行，`0` 返回空 `viewers` 但完整 summary/scopes。
- 重复参数、空值、负数、非十进制整数和越界 limit 返回 HTTP 400 与稳定中文消息。
- 非 GET 返回 405，并设置 `Allow: GET`。
- 配置读取失败返回 500；客户端只看到稳定消息，内部 cause 写诊断日志，不回传本地路径。
- 成功返回 `{"code":0,"leaderboard":<snapshot>}`，并设置 `Cache-Control: no-store`。

handler 从现有 `configStore.readState()` 读取一次一致快照，不新增第二份存储或后台缓存。

## 6. 前端 adapter 与消费者

### 6.1 `src/backend.ts`

新增单一 adapter：

```ts
getBlindBoxLeaderboard(options?: {
  giftId?: number;
  limit?: number;
  signal?: AbortSignal;
}): Promise<BlindBoxLeaderboardSnapshot>
```

adapter 负责构造查询参数、检查响应 code、严格校验 plain-object 形状、有限数值、合法 viewer/scope 列表和 `updatedAt`。UI 不直接了解 URL 或 wire envelope。

### 6.2 OBS 盲盒展示页

`blind-box-display.ts` 不再 import `buildBlindBoxLeaderboard`。每个刷新周期继续读取配置/外观状态，同时通过 backend adapter 获取 `limit=100` 的排行榜快照。

- 初始请求期间显示现有空态框架，不把旧本地聚合当 fallback。
- 请求失败保留最后一次成功快照、连接状态显示 error，并在下一次 750ms 周期重试。
- 使用单飞请求与递增 generation；旧请求完成后不得覆盖更新的 scope 或更新的 `updatedAt`。
- 页面销毁时 abort 未完成请求。
- 滚动、主题、金额格式化、viewerSlots 和动画仍属于显示模块。

### 6.3 配置页贡献排行榜

配置页的“礼物贡献”和“规则命中”标签继续从权威台账快照做本地展示排序。只有“盲盒盈亏”标签及 scope 菜单使用服务端排行榜快照。

- 打开区域时请求未限定 scope 的完整 snapshot；scope 菜单直接使用其 `scopes`。
- 用户切换 scope 后请求新的 snapshot；请求期间保留旧内容并显示 loading 状态，不伪造新 scope 汇总。
- generation 与 AbortController 阻止快速切换造成旧响应覆盖。
- 清空贡献台账成功后重新请求排行榜，不在前端手工清空或重算 summary。
- 排行榜接口失败时保留最后成功内容并显示可重试错误；不回退 TS 聚合。

迁移完成后删除：

- `src/blind-box-leaderboard.ts`
- `tests/blind-box-leaderboard.test.ts`

其语义测试迁入 Go；前端只保留 adapter 校验、异步状态、stale-response、失败重试和渲染契约测试。

## 7. 错误处理与一致性

- 服务端快照的 `updatedAt` 来自同一次贡献台账读取，用于前端判定新旧数据，而不是客户端时钟。
- HTTP 请求失败不会清空最后成功排行榜，也不会从 `state.contributions` 临时重建盲盒结果。
- scope 已不存在时，服务端对指定 giftId 返回空 summary/viewers；前端在下一次无 scope 快照中把选择恢复为“全部盲盒”。
- 清空台账、快速切换 scope 和轮询刷新必须通过 generation/abort 测试，证明旧响应无法回写。
- handler 不接受客户端上传 contribution ledger，避免形成第二个权威入口。

## 8. 测试策略

### Go

- 规则/公式语义安全网覆盖第 3 节全部条目。
- 排行榜纯函数覆盖：全局汇总、limit 不影响 summary、单 gift scope、无价盲盒、空榜、负值/非有限值清洗、稳定 viewer 排序、中文 scope 排序、同时间名称选择。
- handler 覆盖：合法/省略参数、重复参数、0 limit、越界/负数/非整数、405、store failure、no-store、错误隐私。
- 全量与 race 测试必须通过。

### TypeScript

- `backend.ts` adapter 覆盖 URL、AbortSignal、严格响应解析与错误。
- OBS 展示测试覆盖 loading、成功、失败保留、重试、旧响应抑制、销毁 abort。
- 配置页测试覆盖 scope 菜单、快速切换、清空后刷新、失败不回退客户端聚合。
- 架构测试禁止 display/config 重新 import 或定义盲盒聚合函数，禁止 UI 直接访问新 URL。

### 集成验收

- `npm run typecheck`
- `npm test -- --reporter=dot`
- `go test ./... -count=1 -timeout=300s`
- `go test -race ./... -count=1 -timeout=300s`
- `npm run build:ui`
- 全仓搜索确认 TS 无 `evalFormula`、`applyGiftToState`、`recordGiftTotals`、`resetTodayStats`、`buildBlindBoxLeaderboard` 运行时消费。
- 使用真实本地礼物流验证属性值、礼物目标、盲盒全局/单 scope、活动迁移和定时规则行为不变；OBS 页面无控制台错误。

## 9. 提交与回滚边界

按以下独立提交推进，每个提交都必须通过其聚焦测试：

1. 补齐 Go 规则与公式语义安全网。
2. 删除 TS engine/evaluator 并调整前端测试。
3. 新增 Go 排行榜纯模块、HTTP handler 和 Go 测试；前端仍使用旧实现。
4. 新增前端 adapter、迁移两个消费者并删除 TS 排行榜模块。
5. 完整集成门禁与文档收尾。

任何阶段可独立回滚。阶段 3 的加法提交在前端切换前不改变用户行为；前端切换提交若失败，可回滚到旧实现而不删除 Go 模块。不得将配置导入或 SSE 改动混入上述提交。

## 10. 完成标准

- Go 侧逐条承接旧 TS 规则与公式语义，阶段 0 映射无空项。
- TypeScript 运行时代码不再包含公式求值器、规则引擎或盲盒权威聚合。
- OBS 盲盒展示页与配置页盲盒标签只消费服务端 snapshot。
- summary、scope 和 viewer 排名在 Go 中只有一份实现。
- 所有聚焦、全量、race、类型检查和 UI 构建门禁通过。
- 真实礼物流行为与重构前一致，且不引入配置导入或 SSE 行为变化。
