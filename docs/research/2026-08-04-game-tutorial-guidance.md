# 经典游戏教程引导与“加班时间”属性配置教学研究

调研时间：2026-08-04
调研范围：经典游戏如何教授复杂系统，以及这些机制如何映射到本项目的属性编辑、礼物规则、定时器、预设、OBS 输出和后台运行。
来源约束：只使用游戏官方访谈、开发者本人 GDC 演讲、官方开发日志、官方游戏指南和官方补丁/支持文档；未采用二手博客、媒体总结或玩家攻略。

## 结论摘要

推荐把当前教程从“四个气泡指路”升级为一套 **真实任务主线 + 安全训练场 + 可随时重开的帮助中心**：用户不是先读完说明，而是在真实配置里完成一个可用的“加班时间”属性；每一步只出现当前需要的控件，规则和定时器先在模拟器里验证，再保存到后台。这个组合分别吸收了《Super Mario Bros.》的行动式教学、《Plants vs. Zombies》的分散教学、《Portal》的低压力演练、《Factorio》的按需解锁与统一帮助入口，以及《FFXIV》《World of Warcraft》《Civilization》的分角色、可跳过和状态驱动引导。[Nintendo 官方访谈][src-mario] [PvZ 开发者演讲][src-pvz-session] [Portal 开发者演讲][src-portal-session] [Factorio 官方开发日志][src-factorio]

最重要的产品改动方向不是“再写更多说明”，而是把一个超长属性弹窗拆成同一配置页内的四个工作区：**概览、礼物规则、定时器、输出与预览**。礼物目录只在“添加礼物”抽屉中出现；一次只展开一条规则；预设贴着规则编辑器；启用开关只保留在外部运行状态卡片，避免内外两个真相。[当前属性弹窗组装代码][repo-editor-assembly] [当前外部启用开关][repo-enable-switches] [Factorio 对统一教学入口和避免百科膨胀的说明][src-factorio]

教程完成条件必须检查“结果是否成立”，不能检查“用户是否严格点过指定按钮”。例如属性名可以不是“加班时间”、礼物可以不是教程推荐礼物、规则也可以是任意能通过预览的合法规则；只要最终状态满足目标就应进入下一步。2K 的官方支持文档记录过《Civilization VI》教程因用户偏离规定步骤而卡死，这正是本项目应避免的反例。[Civilization 官方支持记录][src-civ-strict]

## 1. 当前产品上下文

### 1.1 现有教程的覆盖范围

当前教程只有 `连接房间 → 添加属性 → 配置礼物规则 → 复制 OBS 链接` 四种状态，气泡分别聚焦房间输入框、添加按钮、礼物搜索框和 OBS 复制按钮；气泡会监听滚动和窗口尺寸变化并跟随目标元素。[当前教程状态机][repo-wizard] [当前气泡文案与定位][repo-spotlight]

这个方案能告诉用户“下一处点哪里”，但没有在属性编辑器内部逐项教授：属性值与显示格式的关系、赋值型规则的含义、预览、启用状态、定时器条件、规则预设等都没有自己的教学状态。[当前气泡仅有四类步骤][repo-spotlight] [当前规则完整说明][repo-rule-help]

### 1.2 复杂度集中点

当前单个属性弹窗同时包含属性名称、当前值、显示格式、后缀、默认播报消息、定时器、礼物搜索和瀑布加载、手动礼物、每个礼物的规则编辑、盲盒状态、规则预设、计算预览及最终统一保存。[属性基础字段][repo-basics] [定时器编辑器][repo-timers] [礼物与规则编辑器][repo-gift-rules] [弹窗最终组装顺序][repo-editor-assembly]

这与仓库已有 UX 规格中的原目标发生了偏离：原规格要求首次使用时“只突出当前步骤的一个主卡片”“每步只保留一个主要动作”“详细说明默认折叠”，并明确提出把复杂功能放入高级入口。[现有向导视觉规格][repo-ux-spec]

### 1.3 教学必须讲清的真实运行模型

礼物规则和定时器由 Go 后台各自的运行循环执行，而不是由配置页或 OBS 页面执行；因此“关闭页面仍运行”不是一句附加提示，而是用户理解产品心智模型的关键一课。[后台礼物与定时器循环][repo-background] [当前弹窗后台说明][repo-editor-assembly]

每个属性卡片拥有自己的 OBS 链接；礼物规则和定时器的启用开关已经位于属性卡片中。这意味着教程应先让用户保存规则，再带用户回到属性卡片学习“运行状态”和“输出链接”，而不应在编辑器内部复制第二套开关。[当前 OBS 专属链接][repo-obs-link] [当前外部启用开关][repo-enable-switches]

## 2. 七个经典案例

### 2.1 《Super Mario Bros.》World 1-1：让关卡本身成为说明书

**具体机制。** Nintendo 的开发者访谈说明，团队会先做较有趣但困难的中后期内容，再把更容易理解的元素移到前面；World 1-1 还通过第一根水管把蘑菇弹回玩家方向，为没能正常接住蘑菇的玩家提供第二次机会。开发者明确表示，他们希望没有攻略、也不读说明书的初学者能自然熟悉世界规则并投入游戏。[Nintendo 官方访谈][src-mario]

**为何有效。** 玩家通过移动、碰撞和获得蘑菇完成学习，教学动作就是正常玩法；水管形成的第二路径让一次操作失误不会破坏学习目标。本项目可以对应为：先给出“加班时间 + 60 秒”的可操作模板，让用户通过一次模拟触发看到 `00:00:00 → 00:01:00`，而不是先阅读变量表；即使用户改了属性名或错过推荐按钮，也应通过合法状态继续。[Nintendo 官方访谈][src-mario]

**局限。** 隐式环境教学适合可直接观察的动作，不足以单独解释“等号右侧结果会替换属性值”这类抽象语义；本项目仍需在用户第一次编辑规则时给出一句及时解释和可视化前后值，而不是试图完全无文字化。[Nintendo 官方访谈显示其教学对象是可在关卡中自然体验的世界规则][src-mario] [当前项目的赋值语义说明][repo-rule-help]

**可复用模式。** `默认可行路径 + 容错路径 + 做完即懂`。推荐把模板按钮、结构化规则和模拟触发作为第一条路径，把原始表达式与完整帮助作为第二层，而不是一开始只给空输入框。[Nintendo 官方访谈][src-mario]

### 2.2 《Portal》：在真正考验前安排低压力演练

**具体机制。** Valve 的 GDC 复盘把 Incineration Station 称为最终战机制的训练位置，并总结其有效原因包括训练位置合适、玩家在低压力下学得更好；同一演讲反复强调尽早、频繁观察试玩者，并按玩家实际需要调整玩法和提示。[Portal GDC 幻灯片：试玩][src-portal-playtest] [Portal GDC 幻灯片：低压力训练][src-portal-training]

**为何有效。** 训练和真实挑战使用相同动作，但失败代价低，用户可以把注意力放在因果关系而不是紧张局面上。本项目对应的关键能力是“模拟收到 1 个礼物”和“模拟执行 1 次定时器”：使用真实后台解析与计算，但只写入临时预览，不改变正式属性值。[Portal GDC 幻灯片：低压力训练][src-portal-training]

**局限。** Valve 的复盘还记录，过高强度会让玩家忽略信息并疏远喜欢慢节奏解谜的玩家，过于复杂的最终谜题也会拖坏节奏。本项目不应要求主播真的开播、送礼或等待一分钟来证明规则正确，也不应在同一教学步骤同时引入价格变量、条件、随机函数和定时器。[Portal GDC 幻灯片：高强度与复杂度问题][src-portal-limit]

**可复用模式。** `先模拟、再上线；先观察前后值、再保存`。任何会影响真实直播数值的能力，都应先在同一计算引擎驱动的安全训练场中完成一次。[Portal GDC 幻灯片：低压力训练][src-portal-training]

### 2.3 《Plants vs. Zombies》：把复杂策略拆成持续七小时的隐形教程

**具体机制。** 设计师 George Fan 在 GDC 官方演讲中总结了十条教学方法：把教程融入游戏、让玩家做而不是读、提供安全环境、分散教授机制、至少让玩家实际做一次、减少文字、尽量不打断流程、根据玩家行为调整消息、避免提示噪音、用视觉和既有常识教学。[GDC 官方会话说明][src-pvz-session] [开发者完整幻灯片总结][src-pvz-recap]

**为何有效。** 这些方法把认知负担分配到用户真正需要某功能的时刻，并用一次成功操作建立记忆；例如开发者明确主张不要一开始教完全部机制，要让玩家先玩会已有“玩具”再加入新内容。[PvZ 幻灯片：分散教学][src-pvz-spread]

**局限。** 开发者同时警告提示不能“喊狼来了”式地制造噪音，并要求给探索留空间；因此本项目不能让每次编辑属性都重新弹出教程，也不能用一串同权重气泡覆盖用户正在进行的工作。[PvZ 幻灯片：自适应与避免噪音][src-pvz-adaptive]

**可复用模式。** `一句话 → 做一次 → 成功反馈 → 稍后再教下一件事`。气泡正文应限制为当前目标、原因和动作三件事，完整语法放入可回看的帮助中心。[PvZ 幻灯片：少量文字][src-pvz-words] [PvZ 幻灯片：分散教学][src-pvz-spread]

### 2.4 《Factorio》Tips & Tricks：按前置条件解锁、统一入口、可反复查看

**具体机制。** Factorio 官方开发日志回顾了旧方案的问题：提示启动时弹出、只能向前、关闭后不能正常重开、图片尺寸不一致；后续版本加入前后浏览、热键重开、索引、搜索和“标为已读”。新版还用实时模拟代替静态截图，并把 mini-tutorial 合并到同一 Tips 入口。[Factorio 官方开发日志][src-factorio]

**具体机制。** 新提示会在满足依赖或用户执行相关动作后解锁和推荐，例如研究铁路后才出现火车教程；官方把这种“只从必要提示开始，随进度解锁复杂内容”视为重要改进。[Factorio 官方开发日志][src-factorio]

**为何有效。** 用户只需记住一个帮助入口，又能在产生问题的时刻得到对应内容；实时模拟比易过期截图更接近真实操作。本项目应保留“显示教程”，但升级为有目录、搜索、完成状态和“重新练习”按钮的训练中心，气泡只负责把当前任务连接到真实 UI。[Factorio 官方开发日志][src-factorio]

**局限。** Factorio 团队明确不希望 Tips 变成游戏百科，只收录确实难懂、需要视觉展示、跨多个物件或难以在局部解释的内容。本项目不应给每个普通字段都写一篇教程；名称和当前值使用字段内说明即可，把训练关卡留给规则、条件、后台运行与 OBS 这类跨系统概念。[Factorio 官方开发日志][src-factorio]

**可复用模式。** `一个帮助中心 + 条件解锁 + 可重开模拟 + 控制教学范围`。[Factorio 官方开发日志][src-factorio]

### 2.5 《FINAL FANTASY XIV》Hall of the Novice：按角色拆分、可重复、完成后给实际收益

**具体机制。** Hall of the Novice 按 Tank、Healer、DPS 角色提供独立训练；已完成训练可以重新执行，部分训练完成后会给早期有用装备。后续战术训练又把常见团队机制、敌人攻击和效果标记拆成练习，并配套简短战斗指南。[FFXIV 官方 UI 指南][src-ffxiv-hall] [FFXIV 7.1 官方补丁说明][src-ffxiv-tactical]

**为何有效。** 训练内容根据用户当前角色过滤，用户只练与自己责任有关的技能；可重复意味着“忘了”不需要重置整个角色。映射到本项目时，礼物规则、定时器和 OBS 输出应是三个独立训练模块；完成教程的奖励不是徽章，而是一套已经可直播使用的真实配置。[FFXIV 官方 UI 指南][src-ffxiv-hall]

**局限。** Hall of the Novice 需要达到条件后进入专门入口，说明独立训练场适合复杂练习，但不能替代主流程中的即时提示。本项目应同时保留字段旁的短提示和训练中心，不能把所有解释都藏进另一个页面。[FFXIV 官方 UI 指南][src-ffxiv-hall] [Factorio 对局部提示与集中教程分工的说明][src-factorio]

**可复用模式。** `按任务分课程 + 可重练 + 完成后直接获得可用成果`。[FFXIV 官方 UI 指南][src-ffxiv-hall]

### 2.6 《World of Warcraft》Exile’s Reach 与回归玩家体验：按用户状态减噪

**具体机制。** Exile’s Reach 通过逐渐增加难度的任务教授移动、NPC 交互、背包和职业能力，并针对不同职业安排不同技能练习；新手还会自动进入 Newcomer Chat，由符合条件的老玩家提供帮助。[WoW 官方 Exile’s Reach 介绍][src-wow-exile]

**具体机制。** 回归玩家体验会暂时隐藏旧任务，让用户专注重新熟悉角色；Assisted Highlight 用柔和高亮提示下一项推荐技能。该体验可以随时离开，也能以后从教程入口重新进入。[WoW 官方回归玩家指南][src-wow-returning]

**为何有效。** 新用户、回归用户和熟练用户看到的教学密度不同；高亮提示的是当前下一步，而不是一次解释整套职业。本项目应把“空配置首次创建”“已有配置但主动求助”“只想查询某功能”分成三种模式，编辑已有属性时默认不自动重放第三步教程。[WoW 官方 Exile’s Reach 介绍][src-wow-exile] [WoW 官方回归玩家指南][src-wow-returning]

**局限。** 高度线性的起始体验不适合所有老玩家，所以官方允许跳过、离开并以后重进。本项目同样必须保留“跳过本节”“退出教程但保留改动”“从帮助中心继续”，不能用教程锁死属性编辑器。[WoW 官方回归玩家指南][src-wow-returning]

**可复用模式。** `按用户状态分流 + 只高亮下一动作 + 随时退出和恢复`。[WoW 官方回归玩家指南][src-wow-returning]

### 2.7 《Civilization》教程与顾问：给一条推荐路线，但不夺走选择权

**具体机制。** Civilization VII 的官方新手指南说明，首次游戏默认开启教程消息和顾问建议；指南提供一条逐步路线来避免大量战略选项造成压倒感，但强调顾问只给建议、不下命令。系统会在结束回合前提醒仍可执行的动作。[Civilization VII 官方新手指南][src-civ-guide]

**为何有效。** 用户得到一个“现在照做就能前进”的推荐方案，同时仍能选择不同领袖、科技或战略。本项目可以默认推荐“加班时间、0 秒、HH:MM:SS、每 1000 金瓜子 +60 秒”，但任意能通过校验的其他配置都应被接受。[Civilization VII 官方新手指南][src-civ-guide]

**局限。** 官方补丁记录表明教程出现顺序、能否最小化和能否在异常状态下跳过都需要专门修复；更直接的官方支持记录还显示，严格要求按指定步骤操作会使偏离路径的用户卡住。教程状态机必须由配置结果驱动，并为目标元素不存在、用户提前完成和用户返回上一步提供恢复路径。[Civilization VII 官方补丁说明][src-civ-patch] [Civilization VI 官方支持记录][src-civ-strict]

**可复用模式。** `推荐而非强迫 + 结果驱动完成 + 任意时刻可最小化`。[Civilization VII 官方新手指南][src-civ-guide] [Civilization VII 官方补丁说明][src-civ-patch]

## 3. 可操作的通用原则

| 原则 | 设计要求 | 本项目直接应用 |
| --- | --- | --- |
| 1. 先做后讲 | 第一屏给一个能成功的小动作，解释紧贴动作之后，而不是先看轮播说明。[Mario][src-mario] [PvZ][src-pvz-safe] | 用户先用模板完成一次 `0 → 60` 模拟，再解释当前属性名、`price` 和等号语义。 |
| 2. 每步只教一个新概念 | 旧能力先练熟，再按依赖解锁新能力。[PvZ][src-pvz-spread] [Factorio][src-factorio] | 属性基础、礼物、规则、启用、定时器、预设、OBS 分步出现，不能同时展开。 |
| 3. 使用低风险训练场 | 真正挑战前用相同机制进行低压力演练。[Portal][src-portal-training] | 增加“模拟收到 1 个礼物”“模拟执行 1 次定时器”，预览不写正式数据。 |
| 4. 完成条件看结果，不看固定点击路径 | 推荐路线可以明确，但用户保留替代选择；严格脚本会卡死。[Civilization 指南][src-civ-guide] [Civilization 支持记录][src-civ-strict] | 检查连接、合法属性、合法规则和启用状态；不要求固定礼物、固定名字或固定模板。 |
| 5. 提示要短、视觉化、贴近目标 | 少字、被动提示和视觉演示比长段落更不打断流程。[PvZ][src-pvz-words] [Factorio][src-factorio] | 气泡只写“目的 + 动作 + 成功标准”；完整语法放训练中心，预览直接展示前后值。 |
| 6. 教程必须可回看、搜索、重练 | Factorio 从不可重开改成索引、搜索、已读和实时模拟；FFXIV 训练可重复。[Factorio][src-factorio] [FFXIV][src-ffxiv-hall] | “显示教程”改为训练中心：课程目录、完成状态、搜索、重新练习和重置进度。 |
| 7. 按用户状态和上下文分流 | 新手、回归者和熟练者不应承受同样密度；提示应在相关动作发生后出现。[WoW][src-wow-returning] [Factorio][src-factorio] | 空配置启动主线；已有配置只显示可选帮助；编辑既有属性不自动重播创建教程。 |
| 8. 把复杂系统拆成任务课程 | 角色/职责不同就拆成不同训练，完成后给真实可用收益。[FFXIV][src-ffxiv-hall] | “礼物规则”“定时器”“OBS 输出”是独立课程，结课产物就是用户的真实加班机配置。 |
| 9. 提示不能成为噪音 | 只教授用户真实不理解且难以局部解释的机制，避免百科化和重复弹出。[PvZ][src-pvz-adaptive] [Factorio][src-factorio] | 普通文本字段只用字段说明；规则语义、条件、后台运行和 OBS 才使用完整课程。 |
| 10. 观察真实新手并反复修正 | Portal 团队强调早测、常测，并按玩家实际需要调整。[Portal][src-portal-playtest] | 用首次成功率、每步停留时间、返回次数和放弃点验证教程，而不是只验证按钮可点击。 |

## 4. “加班时间”完整教学任务链

### 4.1 总体形态

教程命名建议为 **“加班机训练任务”**。它使用真实配置而不是一次性演示数据：用户完成后即可把这个属性用于直播；所有会改变正式数值的规则先在安全预览中运行一次，符合 Portal 的低压力训练和 FFXIV 的“训练完成即获得可用成果”模式。[Portal][src-portal-training] [FFXIV][src-ffxiv-hall]

任务分为三个检查点：

1. **核心可用**：连接房间、建立属性、选择礼物、规则通过模拟并启用。
2. **进阶自动化**：配置定时器、运行条件与预设。
3. **可以开播**：预览 OBS、复制链接并理解后台运行。

用户可以在任一检查点退出，下次从未完成的任务继续；已经合法完成的后续状态也应被自动识别，不能要求重做。[Factorio 的已读/依赖系统][src-factorio] [WoW 的随时退出和重进][src-wow-returning] [Civilization 的路径容错反例][src-civ-strict]

### 4.2 分步设计

| 关卡 | 当前只显示的内容 | 用户动作 | 通过条件 | 必须讲清的含义 | 证据来源 |
| --- | --- | --- | --- | --- | --- |
| 0. 连接直播间 | 房间号、测试连接、房间号位置提示 | 填房间号并连接 | 后台真实状态为 `connected`，不是仅输入非空 | 后台只有知道房间号才能接收礼物；后续页面可关闭 | [WoW 用真实任务教授基础][src-wow-exile] [当前后台模型][repo-background] |
| 1. 创建“加班时间” | 名称、当前值、显示格式三项；默认推荐 `加班时间 / 0 / HH:MM:SS` | 确认或修改并查看预览 | 存在名称唯一、值合法的属性 | 属性是会被规则改写的数值；当前值是起点；显示格式只改变显示，不改变规则本身 | [Mario 的默认路径与容错][src-mario] [当前属性模型][repo-types] |
| 2. 选择一个礼物 | 已上架礼物网格、搜索、一个“为什么只显示这些礼物”入口 | 选择任意一个礼物 | 草稿中至少有一个礼物 | 每个礼物可有独立规则；一次连送会按单个礼物重复执行 | [PvZ 的逐次解锁][src-pvz-spread] [当前礼物规则 UI][repo-gift-rules] |
| 3. 设置礼物规则 | 规则名称、初学者结构化规则、右侧前后值；默认“按价格增加时间” | 选择“每 1000 金瓜子 +60 秒”，或填写任意合法规则 | 后台预览解析成功 | 左边是新值，右边必须写当前属性名才能累加；`price` 是单个礼物价格 | [Mario 的行动式教学][src-mario] [当前赋值语义说明][repo-rule-help] |
| 4. 模拟并启用 | “模拟收到 1 个”按钮、`触发前 → 触发后`、该礼物的预计变化 | 模拟一次、保存，再在属性卡片打开开关 | 模拟成功、正式规则存在且 `enabled` | 预览不改正式值；开关决定后台是否执行；关闭规则也应从 OBS 规则卡片消失 | [Portal 的低压力训练][src-portal-training] [当前外部开关][repo-enable-switches] |
| 5. 配置定时器与条件 | 独立定时器课程：名称、间隔、条件、触发后值、模拟一次 | 使用推荐 `每 1 分钟`、条件 `加班时间 > 0`、结果 `MAX(加班时间-60,0)` | 条件和规则均通过后台预览，定时器保存并在外部打开 | 条件为假会跳过；`MAX(...,0)` 防止负数；定时器不依赖礼物且不显示在 OBS 规则区 | [Factorio 的依赖解锁][src-factorio] [当前定时器实现][repo-timers] |
| 6. 保存与复用预设 | 当前规则旁的“保存预设”、命名框、另一条规则上的预设列表 | 把当前礼物规则存为“按价格加时”，并应用到另一个礼物或预览草稿 | 预设存在且一次应用后仍能通过预览 | 预设保存计算方法，不保存礼物；礼物规则预设与定时器预设属于不同类别 | [Factorio 的统一索引与复用入口][src-factorio] [当前预设模型与 UI][repo-presets] |
| 7. 输出与后台运行 | OBS 实时预览、默认播报消息、每属性链接、复制按钮、托盘示意 | 预览面板，复制链接，并确认“我知道可以关闭页面” | 链接已复制或预览已打开；教程完成状态持久化 | 一个链接只显示一个属性；OBS 只负责显示；礼物和定时器由托盘后台计算；关闭配置页和 OBS 页面不停止后台 | [WoW 的流程衔接与可回看][src-wow-returning] [当前 OBS 链接][repo-obs-link] [当前后台模型][repo-background] |

### 4.3 规则教学的具体交互

首次规则不应直接给一个完全自由的文本框。推荐先显示结构化句子：

> 收到 1 个「当前礼物」后，把「加班时间」在当前值上增加 `[60]` 秒。

下方同步显示只读展开结果：

```text
加班时间 = 加班时间 + 60
预览：00:00:00 → 00:01:00
```

用户点“高级规则”后才看到可编辑表达式、`price`、`IF`、`MIN/MAX`、随机函数和其他属性引用。这种先让用户完成一次再展开抽象能力的顺序，来自 PvZ 的“做一次、分散教学”和 Factorio 的“复杂内容按依赖解锁”。[PvZ][src-pvz-spread] [Factorio][src-factorio]

预览必须由与正式执行相同的后台解析器计算，并明确显示“这是模拟，不会修改直播数值”。这样才能真正复用 Portal 的训练逻辑，而不是制造一个与生产行为可能不一致的装饰性示例。[Portal][src-portal-training] [当前后台预览入口与规则帮助][repo-rule-help]

### 4.4 启用开关教学

启用状态不在编辑器里重复出现。保存规则后，教程自动回到属性卡片并聚焦唯一的开关：先关闭后模拟，显示“规则已停用，本次不会执行”；再打开后模拟，显示真实变化。这个“看见不同结果”的短练习比解释“启用是什么意思”更符合 Mario、PvZ 和 Portal 的行动式教学。[Mario][src-mario] [PvZ][src-pvz-safe] [Portal][src-portal-training]

通过条件读取持久化后的 `enabled` 状态；不能只因为用户点击过开关就算完成，以免保存失败或后台状态不同步时产生假完成。[Civilization 的严格路径反例][src-civ-strict] [当前外部开关持久化位置][repo-enable-switches]

### 4.5 定时器与条件教学

定时器课程应先把属性临时预览为 120 秒，再连续演示两次：`120 → 60`、`60 → 0`；第三次显示“条件 `加班时间 > 0` 不满足，本次跳过”。用户由结果直接理解间隔、条件和下限三件事，不需要先读比较运算符表。[Portal 的低压力演练][src-portal-training] [PvZ 的做一次原则][src-pvz-recap]

完整语法仍保留在“告诉我更多”中；普通用户只需修改分钟数和最低值，熟练用户可以切换高级表达式。该分层符合 Civilization 的顾问建议而非强制命令，以及 Factorio 只为真正复杂机制提供深度 Tips 的范围控制。[Civilization 指南][src-civ-guide] [Factorio][src-factorio]

### 4.6 预设教学

预设不应在用户尚未写出一条成功规则前出现。第一条规则预览成功后才解锁“保存预设”；保存后立即在第二条临时规则中展示一键应用，让用户看到它的复用价值。没有第二条规则时，只提示“以后给别的礼物设置时可以复用”，不强迫用户为了教程污染配置。[Factorio 的依赖解锁与控制提示范围][src-factorio] [Civilization 的建议而非命令][src-civ-guide]

### 4.7 OBS 与后台运行教学

最后一步应把三个概念并排展示：`配置页 = 修改设置`、`托盘后台 = 接收礼物并计算`、`OBS 链接 = 只显示一个属性`。用户先在内嵌预览看到结果，再复制链接；完成卡明确写“现在可以关闭配置页，需要修改时单击托盘图标”。该心智模型由当前后台架构直接支持。[当前后台循环][repo-background] [当前 OBS 专属链接][repo-obs-link]

教程不能要求检测 OBS 是否真的粘贴成功，因为应用无法可靠观察 OBS 内部配置；通过条件应是“已复制链接或主动打开本地预览”，并把 OBS 操作保留为三行清单。这符合 Civilization 的推荐路线而非强制路径，也避免 Portal 所示的高压力真实环境教学。[Civilization 指南][src-civ-guide] [Portal][src-portal-limit]

## 5. 属性编辑面板的信息架构拆分

### 5.1 推荐结构：同一配置页内的“属性工作台”

保持用户要求的单个配置 tab，不打开新浏览器 tab。点击属性卡片后，在当前页面切换到一个全宽工作台，而不是打开包含全部功能的长模态窗口：

```text
属性与礼物规则
└─ 加班时间工作台
   ├─ 概览
   │  ├─ 属性名称 / 当前值 / 显示格式
   │  └─ 当前运行状态与快捷调整
   ├─ 礼物规则
   │  ├─ 已绑定礼物与启用开关
   │  ├─ 添加礼物（打开独立选择抽屉）
   │  └─ 单条规则编辑器 + 模拟预览 + 预设
   ├─ 定时器
   │  ├─ 定时器列表与启用开关
   │  └─ 单条定时器编辑器 + 条件模拟
   └─ 输出与预览
      ├─ OBS 实时预览 / 默认播报消息
      ├─ 每属性 OBS 链接
      └─ 后台运行说明
```

这个结构把“一个统一求助入口”和“按前置条件逐步暴露复杂度”结合起来，分别对应 Factorio 的 Tips 索引与 mini-tutorial 合并，以及 FFXIV 的按任务课程拆分。[Factorio][src-factorio] [FFXIV][src-ffxiv-hall]

### 5.2 字段迁移建议

| 当前内容 | 新位置 | 展示策略 | 依据 |
| --- | --- | --- | --- |
| 属性名称、当前值、显示格式、后缀 | 概览 | 常用三项常驻；只有选择“数字 + 后缀”才显示后缀 | [PvZ 的分散教学][src-pvz-spread] |
| 默认播报消息 | 输出与预览 | 与实际播报预览放一起，不再混在属性基础字段 | [Portal 的环境/结果应和操作结合][src-portal-slides] |
| 礼物目录、搜索、历史状态、手动 ID | “添加礼物”抽屉 | 默认只展示当前可选礼物；历史/手动添加属于高级展开 | [Factorio 的范围控制][src-factorio] |
| 多个已选礼物 | 礼物规则列表 | 网格只做摘要；一次只展开一条规则 | [PvZ 的每次一个新概念][src-pvz-spread] |
| 规则名称、表达式、示例、预览 | 单条规则编辑器 | 初学者结构化模式优先；高级表达式按需展开；预览固定可见 | [Mario][src-mario] [Portal][src-portal-training] |
| 规则预设 | 当前规则标题旁 | 首次成功预览后才解锁；预设列表可搜索、可删除 | [Factorio][src-factorio] |
| 定时器 | 独立“定时器”工作区 | 列表和单条编辑分离；条件结果在模拟器显示 | [FFXIV][src-ffxiv-hall] [Factorio][src-factorio] |
| 礼物/定时器启用开关 | 属性工作台摘要卡和外部属性卡片 | 只有一套开关；编辑器不再重复 | [PvZ 的避免噪音][src-pvz-adaptive] [当前外部开关][repo-enable-switches] |
| OBS 链接、默认播报、后台说明 | 输出与预览 | 先预览，后复制；后台、配置页、OBS 三者职责并列 | [WoW 的连续任务流][src-wow-exile] [当前后台模型][repo-background] |

### 5.3 编辑与保存模型

每个工作区保存自己的合法改动，切换工作区不丢草稿；关闭工作台时，如果仍有非法或未保存内容，只提示对应区域并允许“保留草稿后退出”。把全部内容压到一个最终“保存修改”按钮会让任何中途退出都具有过高失败成本，不符合 Portal 的低压力练习和 WoW 的随时退出/重进设计。[Portal][src-portal-training] [WoW][src-wow-returning]

教程进度和产品数据应分开：产品状态决定任务是否已经完成，`tutorialVersion / completedLessons / activeLesson` 只记录用户想不想继续看、看到了哪里。这样已有合法配置不会因教程版本变化被迫重做，编辑既有属性也不会因为“当前缺少某条规则”而突然重放创建教程。[Factorio 的依赖与已读状态][src-factorio] [Civilization 的顺序/跳过问题][src-civ-patch]

## 6. 推荐方案与不推荐方案

### 6.1 推荐方案

1. **采用“真实任务主线 + 可选进阶课程”。** 主线完成后直接得到可直播的加班机；定时器、预设、盲盒、手动礼物和高级表达式作为已解锁课程随时补学。[FFXIV][src-ffxiv-hall] [Factorio][src-factorio]
2. **保留现有聚焦气泡，但让它只承担当前一步。** 气泡跟随滚动的实现可以继续使用；正文只说明一个目标，任务卡负责显示总进度和返回课程。[PvZ][src-pvz-words] [当前跟随滚动实现][repo-spotlight]
3. **增加真实计算引擎驱动的训练场。** 礼物与定时器都提供单次模拟，显示条件、前值、后值和变化量，确认后才写正式配置。[Portal][src-portal-training]
4. **提供结构化初学者模式与高级表达式模式。** 初学者先选择“增加、减少、设为、按价格、设置上下限”等动作；高级用户再直接编辑表达式。[Mario][src-mario] [Civilization][src-civ-guide]
5. **教程按状态自动跨步，但不自动弹回。** 用户提前完成合法配置时直接标记完成；只有主动启动教程的会话才显示气泡；“编辑”现有属性默认不触发创建教程。[WoW][src-wow-returning] [Civilization][src-civ-strict]
6. **建立单一训练中心。** 课程可搜索、可重开、可标记完成，并提供“规则为什么没触发”“定时器为什么跳过”“OBS 为什么没有变化”等故障课程；普通字段继续使用局部说明，不把训练中心做成百科。[Factorio][src-factorio]
7. **把变动记录作为教学反馈。** 模拟结束显示解释，正式触发后把对应记录高亮，让用户能验证礼物、规则和数值变化是否一致；这相当于 WoW 的即时技能提示与 Portal 的试玩观察闭环。[WoW Assisted Highlight][src-wow-returning] [Portal][src-portal-playtest]

### 6.2 不推荐方案

1. **不推荐在进入弹窗前播放一轮只读气泡。** 用户在尚未接触真实控件时很难记住字段关系；PvZ 明确主张做胜于读，并把教学分散到上下文中。[PvZ][src-pvz-safe]
2. **不推荐继续向单个长模态窗口追加说明卡。** 这会让教学变成第二套复杂 UI；Factorio 的经验是合并求助渠道、按依赖解锁，并避免把 Tips 做成百科。[Factorio][src-factorio]
3. **不推荐强制用户选择固定礼物、固定属性名或固定表达式。** 可以提供推荐答案，但任何合法结果都应通过；Civilization 官方支持记录证明严格路径会产生卡死风险。[Civilization 指南][src-civ-guide] [Civilization 支持记录][src-civ-strict]
4. **不推荐依赖真实观众送礼或真实等待定时器来验收。** 真实场景压力高、不可控且反馈慢，应先使用相同机制的安全模拟。[Portal][src-portal-training]
5. **不推荐编辑已有属性时自动重播创建课程。** 教程应根据用户状态分流、可退出和可重进，不能打断熟练用户当前任务。[WoW][src-wow-returning] [PvZ][src-pvz-adaptive]
6. **不推荐用大量徽章、经验值或剧情包装掩盖复杂度。** FFXIV 的奖励有效是因为它本身对后续游戏有用；本项目最有价值的“奖励”是可工作的配置、可验证的预览和明确的“可以开播”状态。[FFXIV][src-ffxiv-hall]
7. **不推荐让教程完成只依赖一个 `showTutorial` 布尔值。** 需要区分用户是否想看、各课程完成证据、教程版本和当前会话；Factorio 的依赖/已读系统和 Civilization 的顺序问题都表明教程状态必须比显示开关更细。[Factorio][src-factorio] [Civilization 补丁说明][src-civ-patch] [当前单一设置字段][repo-tutorial-setting]

## 7. 推荐落地顺序

1. **先拆属性工作台，再写新教程。** 如果底层信息架构仍是一个超长弹窗，教程只能不断遮挡和解释复杂度；先完成四工作区与单条编辑器，才能让每关对应稳定目标。[Factorio 的统一入口与范围控制][src-factorio] [现有 UX 规格的单一主动作原则][repo-ux-spec]
2. **第二步实现模拟器与状态验收。** 礼物和定时器都调用正式后台预览；建立结果驱动的任务判定和恢复机制。[Portal][src-portal-training] [Civilization][src-civ-strict]
3. **第三步实现主线七关。** 先覆盖属性基础、单礼物规则、预览、启用、定时器、预设、OBS 与后台；每关通过真实新手测试后再开放下一关。[PvZ][src-pvz-spread] [Portal][src-portal-playtest]
4. **最后补充训练中心和进阶支线。** 包括多礼物、盲盒、手动礼物、跨属性引用、随机函数、不同显示格式、播报消息与故障排查；只收录实际观察到用户会困惑的主题。[Factorio][src-factorio]

## 8. 建议验收标准

- 从空配置开始，用户不阅读 README 也能完成一套真实可用的“加班时间”配置；这延续了 Mario 让初学者在游戏本身自然学会的目标。[Nintendo 官方访谈][src-mario]
- 每个教学步骤只引入一个新概念，并且在任何时刻只有一个高亮目标和一个主要动作。[PvZ][src-pvz-spread] [现有 UX 规格][repo-ux-spec]
- 用户可以改名、换礼物、改规则或提前完成某一步，教程仍能从状态判断并继续；没有“必须按指定顺序点击”的死路。[Civilization 官方支持记录][src-civ-strict]
- 礼物规则与定时器在保存前都能安全模拟，预览与正式后台使用同一计算语义。[Portal][src-portal-training] [当前后台模型][repo-background]
- 教程可以跳过、最小化、继续、重置和按课程重练；编辑既有属性不会自动重播首次创建课程。[WoW][src-wow-returning] [Factorio][src-factorio]
- 用户完成最后一步后能准确回答：配置页、托盘后台、OBS 页面分别负责什么；关闭配置页和 OBS 后仍知道后台会继续运行。[当前后台循环][repo-background] [当前 OBS 链接][repo-obs-link]
- 训练中心有索引和搜索，但普通字段没有冗余教程，且只为真实困惑点新增课程。[Factorio][src-factorio]

## 一手来源

### 游戏与开发者资料

[src-mario]: https://iwataasks.nintendo.com/interviews/wii/nsmb/1/4/ "Nintendo Iwata Asks — New Super Mario Bros., World 1-1 level design"
[src-portal-session]: https://gdcvault.com/play/197/A-PORTAL-Post-Mortem-Integrating "GDC Vault — A Portal Post-Mortem: Integrating Writing and Design"
[src-portal-slides]: https://media.gdcvault.com/gdc08/slides/S6422i1.pdf "Valve — Integrating Narrative and Design: A Portal Post-Mortem"
[src-portal-playtest]: https://media.gdcvault.com/gdc08/slides/S6422i1.pdf#page=14 "Portal GDC slides, playtesting section"
[src-portal-training]: https://media.gdcvault.com/gdc08/slides/S6422i1.pdf#page=31 "Portal GDC slides, Incineration Station training"
[src-portal-limit]: https://media.gdcvault.com/gdc08/slides/S6422i1.pdf#page=42 "Portal GDC slides, high-intensity and complexity limits"
[src-pvz-session]: https://gdcvault.com/play/1015327/How-I-Got-My-Mom "GDC Vault — How I Got My Mom to Play Through Plants vs. Zombies"
[src-pvz-recap]: https://media.gdcvault.com/gdc2012/slides/Design%20Track/Fan_George_How%20I%20Got.pdf#page=199 "Plants vs. Zombies GDC slides, ten-tip recap"
[src-pvz-safe]: https://media.gdcvault.com/gdc2012/slides/Design%20Track/Fan_George_How%20I%20Got.pdf#page=35 "Plants vs. Zombies GDC slides, do rather than read and safe environment"
[src-pvz-spread]: https://media.gdcvault.com/gdc2012/slides/Design%20Track/Fan_George_How%20I%20Got.pdf#page=48 "Plants vs. Zombies GDC slides, spread teaching over time"
[src-pvz-words]: https://media.gdcvault.com/gdc2012/slides/Design%20Track/Fan_George_How%20I%20Got.pdf#page=87 "Plants vs. Zombies GDC slides, use fewer words"
[src-pvz-adaptive]: https://media.gdcvault.com/gdc2012/slides/Design%20Track/Fan_George_How%20I%20Got.pdf#page=128 "Plants vs. Zombies GDC slides, adaptive messages and avoiding noise"
[src-factorio]: https://www.factorio.com/blog/post/fff-361 "Factorio Friday Facts #361 — Tips and tricks"
[src-ffxiv-hall]: https://na.finalfantasyxiv.com/uiguide/party/party-how/party_practice.html "FINAL FANTASY XIV official UI guide — Hall of the Novice"
[src-ffxiv-tactical]: https://na.finalfantasyxiv.com/lodestone/topics/detail/4ae80e9471306053afa281e8704dd0ed13ce530a/ "FINAL FANTASY XIV Patch 7.1 Notes — tactical training"
[src-wow-exile]: https://worldofwarcraft.blizzard.com/en-us/news/23380363/shadowlands-adventure-awaits-in-the-new-starting-experience "World of Warcraft official — Exile's Reach"
[src-wow-returning]: https://worldofwarcraft.blizzard.com/en-us/news/24263354/te-damos-la-bienvenida-a-tu-hogar-gu%C3%ADa-para-jugadores-que-regresan "World of Warcraft official — Returning Player Guide"
[src-civ-guide]: https://civilization.2k.com/fr-FR/civ-vii/news/civilization-vii-beginners-guide/ "Civilization VII official beginner guide"
[src-civ-patch]: https://support.civilization.com/hc/en-us/articles/43845922146451-Civilization-VII-Patch-Notes-August-19-2025 "Civilization VII official patch notes — tutorial ordering/minimization"
[src-civ-strict]: https://support.civilization.com/hc/en-us/community/posts/38001388388243-Unable-to-Proceed-in-Tutorial "Civilization official support — tutorial can get stuck after deviation"

### 本项目当前实现与 UX 资料（固定到调研时提交 `8a47cd9`）

[repo-wizard]: https://github.com/brainfk123/bilibili-live-gift-panel/blob/8a47cd9f39363e2db8fa27571f8dedd2488edfd0/src/ui/config/wizard.ts#L1-L51 "当前四步教程状态机"
[repo-spotlight]: https://github.com/brainfk123/bilibili-live-gift-panel/blob/8a47cd9f39363e2db8fa27571f8dedd2488edfd0/src/ui/config/spotlight-guide.ts#L22-L145 "当前气泡文案、定位和滚动跟随"
[repo-basics]: https://github.com/brainfk123/bilibili-live-gift-panel/blob/8a47cd9f39363e2db8fa27571f8dedd2488edfd0/src/ui/config/config.ts#L899-L945 "当前属性基础字段"
[repo-timers]: https://github.com/brainfk123/bilibili-live-gift-panel/blob/8a47cd9f39363e2db8fa27571f8dedd2488edfd0/src/ui/config/config.ts#L1071-L1260 "当前定时器编辑器"
[repo-gift-rules]: https://github.com/brainfk123/bilibili-live-gift-panel/blob/8a47cd9f39363e2db8fa27571f8dedd2488edfd0/src/ui/config/config.ts#L1261-L1573 "当前礼物选择与规则编辑器"
[repo-editor-assembly]: https://github.com/brainfk123/bilibili-live-gift-panel/blob/8a47cd9f39363e2db8fa27571f8dedd2488edfd0/src/ui/config/config.ts#L1581-L1610 "当前属性弹窗组装与统一保存"
[repo-enable-switches]: https://github.com/brainfk123/bilibili-live-gift-panel/blob/8a47cd9f39363e2db8fa27571f8dedd2488edfd0/src/ui/config/config.ts#L553-L670 "当前属性卡片规则和定时器启用开关"
[repo-obs-link]: https://github.com/brainfk123/bilibili-live-gift-panel/blob/8a47cd9f39363e2db8fa27571f8dedd2488edfd0/src/ui/config/config.ts#L673-L700 "当前每属性 OBS 专属链接"
[repo-presets]: https://github.com/brainfk123/bilibili-live-gift-panel/blob/8a47cd9f39363e2db8fa27571f8dedd2488edfd0/src/ui/config/config.ts#L952-L1068 "当前规则预设 UI"
[repo-rule-help]: https://github.com/brainfk123/bilibili-live-gift-panel/blob/8a47cd9f39363e2db8fa27571f8dedd2488edfd0/src/ui/config/config.ts#L2089-L2128 "当前规则完整说明"
[repo-types]: https://github.com/brainfk123/bilibili-live-gift-panel/blob/8a47cd9f39363e2db8fa27571f8dedd2488edfd0/src/types.ts#L1-L48 "当前属性、礼物规则、定时器和预设模型"
[repo-tutorial-setting]: https://github.com/brainfk123/bilibili-live-gift-panel/blob/8a47cd9f39363e2db8fa27571f8dedd2488edfd0/src/types.ts#L100-L110 "当前 showTutorial 设置"
[repo-background]: https://github.com/brainfk123/bilibili-live-gift-panel/blob/8a47cd9f39363e2db8fa27571f8dedd2488edfd0/goserver/background_runtime.go#L70-L176 "后台连接、礼物和定时器运行循环"
[repo-ux-spec]: https://github.com/brainfk123/bilibili-live-gift-panel/blob/8a47cd9f39363e2db8fa27571f8dedd2488edfd0/docs/superpowers/specs/2026-08-01-config-wizard-visual-design.md#L19-L31 "已有首次向导信息架构目标"
