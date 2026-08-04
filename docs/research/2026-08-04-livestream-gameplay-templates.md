# 直播互动玩法模板调研

- 调研时间：2026-08-04
- 调研目标：为“Bilibili 直播礼物属性面板”选择一批主播可以直接套用的玩法模板，并明确哪些玩法能用当前数值引擎实现、哪些需要先扩展数据模型。
- 来源原则：事实性结论只采用官方平台文档、官方产品文档和本项目源码；根据这些机制形成的模板方案均明确标为“本项目推论”。

## 结论摘要

首批不应追求模板数量，而应先做 **7 个内容模板、4 个行为骨架**：

1. 加班机
2. 倒计时续命
3. 礼物计数器
4. 目标进度
5. Boss 挑战
6. 生存资源条
7. 双向拉扯条

它们底层只需要四类行为：

- **累计**：收到礼物后增加或减少数值；
- **倒计时 / 补给**：定时减少，礼物增加；
- **血量消耗**：从上限向 0 减少，也可恢复；
- **双向竞争**：不同礼物把同一个有限区间的数值推向两端。

以上 7 个模板均能复用当前的数值属性、礼物规则、`price`、条件公式和定时器。为了让“目标、Boss、资源、拉扯”真正像玩法而不只是一个数字，首批开发还应增加一个通用的 **进度条 / 血条 OBS 外观**。[当前属性与规则模型](../../src/types.ts) [当前公式引擎](../../goserver/formula.go) [当前后台执行逻辑](../../goserver/background_runtime.go)

**两队对战、礼物投票、连击、随机事件、里程碑解锁、抽奖和点歌 / 任务队列不宜直接塞进当前属性模板。** 它们分别需要多属性合并展示、活动状态、相对事件计时、枚举文本、一遍性动作、参与者集合或有序列表。官方 Twitch 投票 / 预测也明确包含选项、持续时间、状态和参与数据；Mix It Up 的游戏队列则包含加入、离开、顺序、随机选择和清空等操作，它们都不是单个数值属性。[Twitch Polls 与 Predictions API](https://dev.twitch.tv/docs/api/reference) [Mix It Up Game Queue Action](https://dev.site.mixitupapp.com/docs/features/actions/game-queue-action)

## 1. 官方产品中反复出现的互动结构

### 1.1 持久数值与计数器

Streamer.bot 把跨动作、跨重启的数据放入持久全局变量，也提供每用户变量用于个人计数和排行榜；全局变量的一等修改方式包括赋值、增加和减少。[Streamer.bot Variables](https://docs.streamer.bot/guide/variables) [Streamer.bot Set Global Variable](https://docs.streamer.bot/api/sub-actions/core/globals/global-set/)

Mix It Up 的 Counter Action 同样把操作分为更新、设值和重置，并允许其他动作引用计数器。[Mix It Up Counter Action](https://wiki.mixitup.bot/en/actions/counter-action)

**本项目推论：** 礼物计数、死亡次数、挑战次数、复活次数、积分和加班时间不是不同的底层系统，都是“有名字、初始值、显示格式和触发规则的持久数值”。因此内容模板应该共享同一个累计行为骨架。

### 1.2 目标与分段里程碑

Mix It Up 的 Goal Widget 支持单段或多段目标、固定增量、按事件金额乘倍率、动态增减和分段完成命令；目标可以绑定计数器，也可以自己保存进度。[Mix It Up Goal Widget](https://dev.site.mixitupapp.com/docs/features/overlays/goal)

Twitch Community Goals 的官方说明强调短期目标和里程碑目标，目标进度会在频道与浏览器源中近实时展示，达到目标后仍可继续增长，直到主播结束或更换目标。[Twitch Community Goals](https://help.twitch.tv/s/article/creator-goals)

StreamElements 的官方文档也把 goals 列为 OBS overlay 的常见组成，并提供无需编码即可套用的现成 Overlay Gallery；其 Widget Data 允许主播查看、修正和清除目标进度。[StreamElements Overlays](https://docs.streamelements.com/overlays) [StreamElements Overlay Gallery](https://support.streamelements.com/hc/en-us/articles/10474664619666-Introducing-Overlays-Gallery-and-My-Overlays) [StreamElements Widget Data](https://support.streamelements.com/hc/en-us/articles/10474424314642-Widget-Data-Overview)

**本项目推论：** “目标进度”应成为独立内容模板，但首版只做单目标与封顶；多段里程碑除了展示节点，还需要“到达阈值只触发一次”的动作系统，不应假装当前公式已经完整支持。

### 1.3 Boss、血量和正负贡献

Mix It Up 的 Stream Boss Widget 以生命值为核心，事件既可以造成固定伤害，也可以按金额乘倍率造成伤害；它还支持治疗、击败后换 Boss、过量伤害与新 Boss 生命值等状态。[Mix It Up Stream Boss Widget](https://dev.site.mixitupapp.com/docs/features/overlays/stream-boss)

**本项目推论：** 当前版本可以先提供“全房共同打一个 Boss”的简化模板：Boss 只有名称、最大生命和当前生命，礼物分别造成伤害或治疗。不要在首版承诺“最后一击观众成为新 Boss”，因为当前状态只保存全局数值，没有 Boss 所有者、每用户伤害或击败事件。

### 1.4 倒计时、补给和限时冲刺

SAMMI 的 Variable Transition 可以让一个数值在给定时长内从起点过渡到终点，并把简单倒计时作为 OBS 使用示例。[SAMMI Variables](https://sammi.solutions/docs/commands/variables)

Twitch Hype Train 的官方事件模型包含当前等级、当前进度、下一等级目标、开始时间与到期时间，并在贡献发生时更新进度。[Twitch EventSub Hype Train](https://dev.twitch.tv/docs/eventsub/eventsub-subscription-types/)

**本项目推论：** 加班机、倒计时续命、氧气 / 饥饿 / 能量资源条都可以由“定时消耗 + 礼物补给 + 0 和最大值边界”实现。真正的 Hype Train 还包含动态等级和到期延长，首版可借鉴视觉和节奏，但不要使用“Hype Train”作为功能承诺。

### 1.5 投票、预测和多人对抗

Twitch Polls 包含 2–5 个选项、投票数、持续时间和活动 / 完成等状态；Predictions 包含 2–10 个结果、独立参与人数、投入点数、锁定、取消和结算状态。[Twitch API Reference](https://dev.twitch.tv/docs/api/reference) [Twitch Predictions](https://dev.twitch.tv/docs/api/predictions)

Mix It Up 的 Poll Widget 会把投票、预测、竞猜或答题的多个选项显示成一组进度条。[Mix It Up Poll Widget](https://dev.site.mixitupapp.com/docs/features/overlays/poll)

**本项目推论：** 两个礼物分别给 A、B 属性加分，只能称为“礼物阵营对抗”或“礼物票选”，不能等同于平台投票或预测。要做完整玩法，至少需要活动开始 / 结束状态、多个选项的同屏展示、锁定与结算；若要限制每人一次，还需要可靠的用户身份和参与记录。

### 1.6 随机事件与转盘

Streamer.bot 的 Action 可以在随机组中按权重选择一个子动作；SAMMI 提供随机数用于随机图片、媒体或观众选择。[Streamer.bot Actions](https://docs.streamer.bot/guide/core/actions) [SAMMI Random](https://sammi.solutions/docs/commands/number-random)

Mix It Up 的 Wheel Widget 把随机结果做成有名称、有颜色、有概率的结果项，并允许每个结果触发独立命令；概率还可以在多次抽取之间变化。[Mix It Up Wheel Widget](https://dev.site.mixitupapp.com/docs/features/overlays/wheel)

**本项目推论：** 当前 `RANDBETWEEN` 只能得到数字，无法把 `1` 映射为“唱一首歌”、把 `2` 映射为“接受惩罚”，也没有转盘动画和结果动作。因此可以先在高级规则中保留随机数，但不应把“随机转盘”作为首批完整模板。

### 1.7 队列、点歌和抽奖名单

Mix It Up 的 Game Queue 支持用户加入 / 离开、插队、查询位置、取队首、随机选择、清空和启停队列。[Mix It Up Game Queue Action](https://dev.site.mixitupapp.com/docs/features/actions/game-queue-action)

StreamElements 的 Song Request 会保存有序媒体队列，可设置待审核列表、长度与内容过滤、每用户权限或积分成本、付费请求优先级和投票跳过。[StreamElements Song Request](https://docs.streamelements.com/chatbot/modules/songrequest)

**本项目推论：** 点歌、上车、连麦、任务队列与抽奖名单需要保存“谁提交了什么、处于什么位置或状态”，不能用一个队列长度数字代替。它们应属于未来的“列表型玩法”，而不是属性模板。

## 2. 当前产品能力边界

当前产品已经具备：

- 多个持久数值属性，支持数字、后缀与时分秒显示；
- 一个礼物可以对多个属性执行规则，一个属性也可由多个礼物影响；
- 每个礼物按单个重复执行，规则可读取 `price` 与其他属性；
- `+ - * /`、比较、`IF`、`MIN`、`MAX`、`ROUND`、`ABS`、`RAND`、`RANDBETWEEN`；
- 固定间隔定时器与条件；
- 每个属性独立 OBS 链接、礼物规则卡片和礼物播报；
- 规则启停、模拟预览与生效记录。

这些能力分别由[数据模型](../../src/types.ts)、[后端公式引擎](../../goserver/formula.go)、[后台礼物与定时器循环](../../goserver/background_runtime.go)和[OBS 单属性面板](../../src/ui/display/display.ts)实现。

当前没有：

- 字符串 / 枚举状态及“数字 → 文案 / 图片”的映射；
- 有序列表、参与者集合和每用户数值；
- 一次性阈值动作、阶段状态机或手动结算流程；
- “最后一次礼物后 N 秒”的可重置计时器；
- 多个属性合成一个 OBS 场景的正式模型；
- 规则执行后的媒体、音效、场景切换等动作。

因此，模板系统必须显示真实能力等级：**可直接创建、需要简化、暂未支持**，不能只靠漂亮预览掩盖功能缺口。

## 3. 候选模板评估

| 模板 | 观众实际怎么玩 | 当前可实现度 | 主要缺口 | 建议 |
| --- | --- | --- | --- | --- |
| 加班机 | 礼物增加直播时长，后台每分钟消耗 | 完整 | 需要模板入口和更好的计时皮肤 | 首批 |
| 倒计时续命 | 开局有剩余时间，礼物续时，归零结束挑战 | 完整 | 可选归零提醒 / 动画 | 首批 |
| 礼物计数器 | 指定礼物让挑战、复活或点单次数 +1 | 完整 | 无 | 首批 |
| 目标进度 | 礼物按个数或价格推进到目标 | 计算完整 | 进度条、目标值与达成态 | 首批，补通用进度条 |
| Boss 挑战 | 不同礼物造成不同伤害或治疗 | 简化版完整 | Boss 所有者、伤害榜、击败后换 Boss | 首批做“全房 Boss” |
| 生存资源条 | 氧气 / 饥饿 / 能量随时间减少，礼物补给 | 完整 | 资源条皮肤、归零提示 | 首批 |
| 双向拉扯条 | 支持礼物把数值推向右，干扰礼物推向左 | 计算完整 | 中点 / 阵营视觉 | 首批，单属性 0–100 |
| 两队对战 | 两组礼物分别给两队加分 | 部分 | 两属性同屏、比赛状态、结算 | 第二阶段 |
| 礼物票选 | 每种礼物代表一个选项 | 部分 | 多选项同屏、结束 / 锁定、用户去重 | 第二阶段，名称不能写“预测” |
| 连击挑战 | 一段时间内连续送礼提高连击，超时清零 | 不可靠 | 礼物后可重置的超时计时器 | 先扩展事件相对计时 |
| 随机事件 / 转盘 | 礼物触发有名字和概率的结果 | 只有随机数字 | 枚举结果、权重、动画、结果动作 | 第二阶段 |
| 里程碑解锁 | 到 25%、50%、100% 分别触发内容 | 只能显示进度 | 每个阈值只执行一次、动作系统 | 第二阶段 |
| 抽奖池 / 抽奖名单 | 礼物获得资格，最后抽出观众 | 只能累计资格总数 | 参与者、每人次数、去重、开奖 | 列表型玩法阶段 |
| 点歌 / 任务 / 上车队列 | 观众提交内容，主播审核并按顺序处理 | 不适合 | 字符串、有序队列、审核、用户权限 | 独立模块，不做属性模板 |

## 4. 首批 7 个模板的具体配置

所有模板都不写死礼物 ID，而是提供 **礼物角色槽位**。主播在创建时选择当前房间可赠送的礼物，例如“续时礼物”“普通攻击礼物”“补给礼物”。模板负责生成规则，不负责替主播决定用哪一种礼物。

### 4.1 加班机

- 属性：`加班时间`，初始 `0`，格式 `HH:MM:SS`。
- 礼物槽位：一个或多个“加时礼物”。
- 默认规则：`加班时间 + price/1000 * 每元增加秒数`。
- 定时器：每 60 秒执行 `MAX(加班时间-60,0)`，条件 `加班时间>0`。
- 主播参数：每 1 元增加多少分钟、可选最大加班时长、默认播报。
- OBS：计时数字 + 礼物规则卡片 + 播报。

### 4.2 倒计时续命

- 属性：`剩余时间`，默认 `30:00`。
- 礼物槽位：“固定续时”“按价格续时”，可任选其一或都选。
- 定时器：每秒 `MAX(剩余时间-1,0)`，条件 `剩余时间>0`。
- 主播参数：初始时长、每个 / 每元续时、最大时长。
- OBS：大号倒计时；低于 60 秒时可使用强调色。
- 与加班机的区别：加班机从 0 累积待履行时间；续命模式从一个已有时长持续倒数。

### 4.3 礼物计数器

- 属性：默认 `挑战次数`，初始 `0`，后缀由主播选择，例如“次 / 局 / 个”。
- 礼物槽位：“计数礼物”，可选择任意数量。
- 默认规则：`挑战次数+1`；可改为每个增加 N。
- 定时器：无。
- 主播参数：属性名、单位、每个礼物增加数量、是否封顶。
- 适用内容：死亡次数、复活次数、俯卧撑、下一局挑战、点单数量。

### 4.4 目标进度

- 属性：`目标进度`，初始 `0`。
- 礼物槽位：“推进目标的礼物”。
- 默认规则：`MIN(目标进度+price/1000*每元进度,目标值)`。
- 定时器：无，或可选“每天 / 每场手动清零”；首版不承诺自动按日历重置。
- 主播参数：目标标题、目标值、固定或按价格推进、达成后的显示文案。
- OBS：`当前 / 目标`、百分比和进度条。

### 4.5 Boss 挑战（简化版）

- 属性：`Boss血量`，初始值等于最大血量。
- 礼物槽位：“普通攻击”“重击”“治疗”，治疗可不选。
- 伤害规则：`MAX(Boss血量-伤害,0)` 或按价格折算伤害。
- 治疗规则：`MIN(Boss血量+治疗量,最大血量)`。
- 可选定时器：Boss 每 N 秒回复 M 点生命。
- 主播参数：Boss 名称、最大生命、各槽位伤害 / 治疗值、是否自动回血。
- OBS：Boss 名称、当前 / 最大生命、血条、最近攻击播报。
- 首版不含：Boss 所有者、伤害榜、最后一击换 Boss。

### 4.6 生存资源条

- 属性：默认 `氧气`，初始值等于上限。
- 礼物槽位：“小补给”“大补给”“干扰”，干扰可不选。
- 定时器：每 N 秒 `MAX(氧气-消耗量,0)`。
- 补给规则：`MIN(氧气+补给量,上限)`。
- 干扰规则：`MAX(氧气-扣除量,0)`。
- 主播参数：资源名称、上限、自然消耗速度、各礼物变化量。
- OBS：资源名称、数值、进度条；低值时改变颜色。
- 可换皮为：饥饿、理智、护盾、燃料、温度、耐力。

### 4.7 双向拉扯条

- 属性：默认 `局势`，初始 `50`，范围 `0–100`。
- 礼物槽位：“推向左侧”“推向右侧”。
- 左侧规则：`MAX(局势-变化量,0)`。
- 右侧规则：`MIN(局势+变化量,100)`。
- 定时器：无，或可选每 N 秒回到中间一点。
- 主播参数：左右两侧名称、礼物、每个 / 每元变化量、颜色。
- OBS：中点刻度、左右名称和双色填充。
- 适用内容：继续 / 下播、天使 / 恶魔、困难 / 简单路线、两种惩罚选择。

## 5. 模板库信息架构

### 5.1 入口

“添加属性”后先出现两个明确选项：

1. **从玩法模板创建**（推荐）
2. **创建空白属性**（高级）

模板不是属性编辑器顶部的一条横幅，也不应把所有字段重新塞回同一个面板。它是创建前的向导，完成后生成普通属性、礼物规则和定时器，主播仍可在现有工作台逐项编辑。

### 5.2 分类

- **计时直播**：加班机、倒计时续命；
- **目标挑战**：礼物计数器、目标进度、Boss；
- **生存互动**：资源条、双向拉扯；
- **多人玩法**：两队对战、礼物票选（标记“需要后续能力”）；
- **随机与队列**：转盘、抽奖、点歌（只展示规划时应标记“暂未支持”，不要给不可用的创建按钮）。

### 5.3 模板卡片必须回答四件事

- 观众怎么玩：例如“礼物补充氧气，氧气会随时间减少”；
- 需要选择几个礼物：例如“至少 1 个，推荐 2 个”；
- 会自动变化吗：例如“每 5 秒减少”；
- OBS 长什么样：使用真实小尺寸预览，而不是通用插画。

卡片可以显示“简单 / 进阶”和“可直接使用 / 需要扩展”状态，但不要暴露公式。

### 5.4 创建向导

推荐固定为四步：

1. **选择玩法**：模板卡片与实时预览；
2. **填写核心参数**：只显示名称、初始 / 目标 / 上限、速度等 3–6 个字段；
3. **分配礼物角色**：为每个槽位选择一个或多个礼物；
4. **确认并创建**：用自然语言列出生成结果，例如“人气票：每个 +10 秒；每秒自动 -1 秒”。

参数应使用匹配类型的控件：时间输入、数字步进、滑块、开关、颜色选择器和礼物选择器。高级公式只放在创建完成后的普通编辑器中。

## 6. 模板技术模型建议

模板内容与底层行为应分离。建议使用静态、带版本的模板定义：

```ts
interface GameplayTemplate {
  id: string;
  version: number;
  category: 'timer' | 'goal' | 'challenge' | 'survival' | 'versus';
  title: string;
  summary: string;
  preview: 'timer' | 'counter' | 'progress' | 'health' | 'resource' | 'tug';
  parameters: TemplateParameter[];
  giftSlots: TemplateGiftSlot[];
  build(input: TemplateInput): {
    attributes: Attribute[];
    rules: GiftRule[];
    timerRules: TimerRule[];
  };
}
```

`giftSlots` 使用角色而不是 ID，例如：

```ts
{
  id: 'attack',
  label: '普通攻击礼物',
  minimum: 1,
  multiple: true,
  rule: 'MAX(Boss血量-$damage,0)'
}
```

创建应是事务式的：先在内存中完成名称冲突、礼物选择、公式预览和定时器校验，全部通过后一次保存，取消时不留下半成品。

首版建议 **“生成后即脱离模板”**：模板只负责创建，之后属性与规则按普通配置编辑，不做实时继承或双向同步。可选保存 `createdFromTemplateId` 与版本用于诊断和未来迁移，但不能因为模板升级覆盖主播的自定义修改。

## 7. 开发优先级

### P0：模板库首版

1. 建立模板注册表与四步创建向导；
2. 实现礼物角色槽位，不写死礼物 ID；
3. 实现首批 7 个内容模板；
4. 增加通用 `progress / health / resource / tug` OBS 外观；
5. 创建前复用正式后台公式预览，创建后仍可普通编辑；
6. 保证取消不写配置，确认后一次保存。

### P1：场景型玩法

1. 多属性组合 OBS 面板；
2. 活动会话：未开始、进行中、锁定、已结算；
3. 一次性里程碑动作；
4. 礼物后可重置的超时计时器；
5. 枚举结果与“值 → 文案 / 图片 / 颜色”映射。

完成后可正式加入两队对战、礼物票选、连击、里程碑和随机事件。

### P2：用户与列表型玩法

1. 每用户持久状态与贡献统计；
2. 有序列表、去重、审核和手动处理；
3. 抽选与结算记录；
4. 用户输入文本与内容安全限制。

完成后再考虑抽奖名单、点歌、任务队列、上车和个人排行榜。Streamer.bot 将每用户变量用于个人计数 / 排行榜，Mix It Up 与 StreamElements 的队列也都保存用户与条目状态，说明这是一套独立数据能力，不应作为数值属性的小补丁。[Streamer.bot Variables](https://docs.streamer.bot/guide/variables) [Mix It Up Game Queue Action](https://dev.site.mixitupapp.com/docs/features/actions/game-queue-action) [StreamElements Song Request](https://docs.streamelements.com/chatbot/modules/songrequest)

## 8. 最终建议

第一版模板库建议以 **“开播 3 分钟内配置完成”** 为验收目标：主播从卡片理解玩法，填写少量参数，为角色选择礼物，看到真实 OBS 预览，然后一次创建。不要把模板做成“自动填了一部分属性编辑表单”，否则只是把现在复杂的编辑器换了一个入口。

首批 7 个模板已经足够覆盖直播中最常见的四种因果：**送礼累计、时间流逝、共同打目标、双方拉扯**。它们既能利用现有后台，也为通用进度条和后续场景型玩法建立稳定基础。

## 官方来源

- [哔哩哔哩开放平台：直播能力与互动玩法入口](https://open.bilibili.com/doc)
- [Twitch API Reference：Polls、Predictions、Channel Points](https://dev.twitch.tv/docs/api/reference)
- [Twitch Predictions](https://dev.twitch.tv/docs/api/predictions)
- [Twitch EventSub：Goals 与 Hype Train](https://dev.twitch.tv/docs/eventsub/eventsub-subscription-types/)
- [Twitch Community Goals](https://help.twitch.tv/s/article/creator-goals)
- [StreamElements Overlays](https://docs.streamelements.com/overlays)
- [StreamElements Overlay Gallery](https://support.streamelements.com/hc/en-us/articles/10474664619666-Introducing-Overlays-Gallery-and-My-Overlays)
- [StreamElements Widget Data](https://support.streamelements.com/hc/en-us/articles/10474424314642-Widget-Data-Overview)
- [StreamElements Song Request](https://docs.streamelements.com/chatbot/modules/songrequest)
- [Streamer.bot Variables](https://docs.streamer.bot/guide/variables)
- [Streamer.bot Actions](https://docs.streamer.bot/guide/core/actions)
- [Streamer.bot Set Global Variable](https://docs.streamer.bot/api/sub-actions/core/globals/global-set/)
- [Mix It Up Counter Action](https://wiki.mixitup.bot/en/actions/counter-action)
- [Mix It Up Goal Widget](https://dev.site.mixitupapp.com/docs/features/overlays/goal)
- [Mix It Up Poll Widget](https://dev.site.mixitupapp.com/docs/features/overlays/poll)
- [Mix It Up Stream Boss Widget](https://dev.site.mixitupapp.com/docs/features/overlays/stream-boss)
- [Mix It Up Wheel Widget](https://dev.site.mixitupapp.com/docs/features/overlays/wheel)
- [Mix It Up Game Queue Action](https://dev.site.mixitupapp.com/docs/features/actions/game-queue-action)
- [SAMMI Variables](https://sammi.solutions/docs/commands/variables)
- [SAMMI Random](https://sammi.solutions/docs/commands/number-random)
