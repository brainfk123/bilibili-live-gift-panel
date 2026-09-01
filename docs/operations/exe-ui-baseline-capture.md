# EXE 与 Hosted UI 基线采集

每次验收先填写 EXE 版本与提交、Hosted 提交、干净 VM 快照标识、夹具 SHA-256、浏览器版本和 100% 缩放。对每个工作区、状态、交互和视口记录操作步骤、EXE 截图 SHA-256、Hosted 截图 SHA-256，以及直接并排对比结论。

## 范围

合同定义六个工作区：overview、attributes、activities、gift-targets、obs、analytics；三个视口：desktop-1440x900、narrow-1024x768、mobile-390x844。每一项均按合同中的全部状态和交互采集。截图仅可位于 `acceptance/exe-hosted-ui/captures/<exe-version>/`；不得记录绝对路径、localhost URL 或带 token 的 URL。

## 操作顺序

1. 从干净 VM 快照启动 EXE，加载已记录哈希的夹具，确认浏览器为记录版本并保持 100% 缩放。
2. 在每个视口打开每个工作区，逐状态执行合同中的交互步骤；为 EXE 截图并记录 SHA-256。
3. 使用相同夹具、视口、状态和交互步骤打开 Hosted，截图并记录 SHA-256。
4. 将同一状态、同一视口的 EXE 与 Hosted 截图直接并排比较，逐项检查结构、层级、间距、控件、状态、响应式布局和交互；记录结论与证据路径。
5. 明确记录所有接受的 Hosted 独有内容及其理由，不能以此替代 EXE 功能或界面内容。

只对齐外壳、标题栏或部分卡片不算完成。
每个相关状态和视口都必须直接对比 EXE 与 Hosted 截图。
临时比较图默认保留本地；被验收记录引用的基准、状态清单和生成规则才进入版本控制。
