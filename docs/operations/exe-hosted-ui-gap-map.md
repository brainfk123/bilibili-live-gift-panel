# EXE → Hosted 工作区差距（2026-09-06）

## 基准和已完成工作

参考 EXE 为正式 v0.4.10；Hosted 代码起点为 `7abf973`（随后已随 PR #4 合入 master）。当前阶段按维护者决定，在同一个 macOS Chromium 中比较两端，不比较浏览器平台差异。

已实际运行 Windows ARM 中的 EXE，使用固定空夹具采集六工作区 × 三视口。该批证据只覆盖 `empty` 状态，不等于 requirements.json 中全部状态通过；OBS 空配置仍有内置盲盒排行榜入口，不能把此入口删去以求“全空”。

参考源：`src/ui/config/config-route.ts`、`src/ui/config/config.ts`；Hosted 当前入口：`src/hosted/main.ts`、`configuration.ts`、`room.ts`、`api.ts`；服务端定义/状态：`goserver/internal/hosted/configuration/model.go`。

## 六工作区映射

| 工作区 | EXE 已确认界面 | Hosted 当前能力 | 待实现/验证 |
| --- | --- | --- | --- |
| 概览 overview | 四个统计/跳转卡片、直播间连接、主播登录状态、顶栏设置和训练入口 | 账号页、直播间控制、配置/迁移/邀请码导航 | 六工作区导航、卡片统计、连接与账号布局、设置入口；保留 Hosted 登录和运行租约语义 |
| 属性 attributes | 属性卡片、添加/编辑、礼物与定时规则 | definition/runtime JSON 编辑；属性、规则、定时器 DTO 和版本冲突处理 | 可视化创建/编辑/删除、公式验证、规则编辑、保存冲突与未保存草稿；禁止删除关联玩法时静默丢依赖 |
| 活动 activities | 活动会话；创建依赖已有属性；开始/锁定/结算 | definition/runtime 含活动、里程碑和超时状态 | 活动工作台、关联选择、受控状态转换、结果展示；需要验证服务端转换接口与并发保存的语义 |
| 礼物目标 gift-targets | 目标卡片、礼物选择、数量进度和清零 | giftTargetPanels 定义、giftTargetReceived 状态 | 目标编辑、礼物检索、进度展示、确认清零及并发修订处理 |
| OBS obs | 单属性、目标、排行榜和组合面板分组；复制/外观/组合编辑 | OBS 展示和凭据服务；管理员发放 OBS 凭据 | 主播侧输出目录与预览、账号隔离的链接生命周期、撤销/重置确认、外观和组合编辑 |
| 数据中心 analytics | 贡献/盲盒排行榜、送礼历史、筛选、观众明细和媒体入口 | 现有客户端没有对应主播数据中心视图或完整查询方法 | 查询/分页契约、数据保留边界、筛选/明细/清空确认；媒体采集/导出另行验证 |

JSON 编辑器提供底层配置能力，但没有完成上述可视化内容、状态和交互。当前表来自真实 EXE 截图及 Hosted 源码核对；尚未宣称已完成两端截图并排验收。

## 实施顺序和验收门

1. 环境适配：PR #4 已完成本机验证、独立审查和远端 Linux/Windows CI 后合并。
2. 基线：先固定空夹具与 18 张截图；继续添加 populated、editing、validation-error、overlay、focus-visible、loading/error 等可复现状态，以及完整操作步骤。不存在的状态应记录证据及适用性，不能伪造截图。
3. 迁移：按概览 → 属性 → 活动 → 礼物目标 → OBS → 数据中心实施。每个工作区完成后，使用同一夹具和三视口直接比较 EXE/Hosted；保留原有鉴权、CSRF、状态修订冲突、页面离开清理和内存草稿规则。
4. 数据和媒体：玩法单元导出、服务端独立依赖推导、预览/应用/失败恢复、七天回滚及媒体输出证据；已有单元测试不替代迁移端到端验证。
5. 上线：真实 Bilibili 连接、备份恢复、七天试点及适用的真实 x64 硬件验证；需要真实环境和时间窗口，不在本机 UI 截图中标记通过。

## 重跑当前空状态基线

先按 [VM 环境说明](../development/windows-arm-ui-environment.md) 启动固定 EXE。使用本工作区的锁定依赖：

```bash
npm ci
node scripts/capture-exe-default-baseline.mjs --seed
# 配置与夹具已经一致时，只读采集：
node scripts/capture-exe-default-baseline.mjs
```

`--seed` 只用于开发 VM：脚本在覆盖前检查无房间和业务数据，并在本地输出目录保存 `before-seed.json`，保留原有外观偏好。存在场景、预设、统计、观众或剪裁数据时拒绝覆盖。

PNG 与本次 manifest 写入忽略的 `acceptance/exe-hosted-ui/captures/0.4.10/<run>/`；manifest 包含夹具/脚本/图片 SHA-256、实际浏览器版本、源码提交和相对图片路径。预期 EXE 摘要和参考快照与本次自动验证明确区分：脚本验证 health 版本，运行文件 hash 与快照恢复应在 VM 中单独核验。

## 当前未完成的证据

- Hosted 对应状态的真实渲染截图及并排对比。
- populated、编辑/验证错误、焦点、弹窗、加载/故障、只读/禁用、活动转换等状态。
- 与最终 Hosted 模型一致的有数据夹具和可复跑交互步骤。
- 2026-09-06 npm audit 仍报告 7 项（4 moderate、2 high、1 critical）；Vite/Vitest 修复建议涉及主版本升级，应独立回归。
