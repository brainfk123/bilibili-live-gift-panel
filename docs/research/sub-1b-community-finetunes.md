# 亚 1B 社区微调模型：本地中文答疑 / RAG 候选调研

- 调研截止：2026-08-08
- 目标：寻找可替代或启发本项目 `Qwen3-0.6B / Qwen3.5-0.8B + 帮助库 RAG` 的社区微调模型。
- 范围：Hugging Face、ModelScope、模型作者 GitHub/模型卡；只采用公开一手资料作为事实依据。
- 门槛：总参数严格小于 1B；基础模型、训练数据/方法和许可证至少可追溯；优先考虑中文、短回答、证据约束、拒答、GGUF 和 CPU 本地部署。

## 结论

**社区里确实有大量基于 Qwen3-0.6B、Qwen3.5-0.8B 和 Qwen2.5-0.5B 的微调模型，但截至本次调研，没有一个同时满足“中文、通用项目答疑、严格基于给定帮助条目、许可证与训练数据清晰、GGUF 可直接发布、可信项目级评测”的成品。**

最有价值的不是直接换成某个热门微调，而是两点：

1. **窄任务微调对 0.6B 确实可能产生巨大提升。** Distil Labs 的银行意图模型把 Qwen3-0.6B 在其封闭测试上的工具调用准确率从 48.7% 提到 90.9%；其 Python 文档模型把自报 LLM-as-judge 准确率从 0.55 提到 0.76。两者都是 Apache 2.0，且有 GGUF/llama.cpp 路径。[银行模型与评测][distil-banking-gguf] [Localdoc 模型卡][distil-localdoc]
2. **通用或思维链微调不等于事实更可靠。** GeoLLM-Qwen3.5-0.8B 的领域 QA 指标提升，但其自报 hallucination pass rate 反而从 66.7% 降到 40.0%；另一个 SFT+DPO 模型也明确记录了重复循环、算术错误和格式幻觉。[GeoLLM 模型卡][geollm] [SFT+DPO 模型卡][sparx]

因此，产品路线不应是“下载一个社区通用微调直接替换”，而应是：

- 继续以本项目冻结的 140 题/黄金集评测基础模型；
- 可下载 3 个社区模型做**实验对照**，但不直接进入发布清单；
- 真正发布的模型应使用本项目帮助条目和人工审核失败样本，训练“短回答、只依证据、缺证拒答、无多余思考/格式说明”，再转换为 Q8_0 GGUF；
- 先要求它在本项目冻结集上超过基础模型，而不是相信跨领域公开分数。

## 1. 参数口径与基础模型

| 基础模型 | 真实/官方参数口径 | 中文与许可证 | 本项目意义 |
| --- | ---: | --- | --- |
| Qwen3-0.6B | 0.6B，总参数；非嵌入 0.44B | 100+ 语言/方言；Apache 2.0 | 当前成熟 GGUF/llama.cpp 基线；社区衍生最多 |
| Qwen3.5-0.8B | 官方完整仓库 `safetensors.total` 为 873,438,784；社区文本模型常标 0.8B/0.9B | 201 种语言/方言；Apache 2.0 | 能力较新，但本项目已观察到思考循环和输出不稳定 |
| Qwen2.5-0.5B-Instruct | 0.49B；非嵌入 0.36B | 29+ 语言，含中文；Apache 2.0 | 运行成熟、无 Qwen3 混合思考机制，适合作为稳定对照 |

参数与能力口径来自官方模型卡及仓库元数据。[Qwen3-0.6B][qwen3] [Qwen3.5-0.8B][qwen35] [Qwen3.5 API][qwen35-api] [Qwen2.5-0.5B-Instruct][qwen25]

## 2. 最值得实测的候选

这里的“值得实测”表示可回答一个明确实验问题，不表示可以直接随产品发布。

| 优先级 | 模型 | 训练与证据 | 活跃度/可信度快照 | GGUF | 许可证 | 对本项目的判断 |
| ---: | --- | --- | --- | --- | --- | --- |
| 1 | [Jackrong/Qwen3.5-0.8B-Claude-4.6-Opus-Reasoning-Distilled][jackrong] | 在 Qwen3.5-0.8B 上用 LoRA SFT 训练 2,516 条过滤后的推理样本；模型卡声称专门减少 Qwen3.5 的重复思考，标注中英韩 | 837 次月下载、10 赞；无任务级公开评测 | 目前模型树只列 MLX 4/6-bit，没有可见 GGUF；需自行转换 | 页面标 Apache 2.0，但正文又称仅供学习/演示和学术研究，存在用途表述冲突 | **只做本地冒烟实验。** 最直接检验“社区微调能否解决不退出 `think`”的问题；无可信中文/RAG评测，不可据卡片宣传直接发布 |
| 2 | [PursuitOfDataScience/Qwen3.5-0.8B-thinking][qwen35-thinking] | Qwen3.5-0.8B-Base 上 SFT；约 50 万 CoT 数据中过滤后使用 244,997 条；1 epoch、4K 上下文；GSM8K 自报 58.23%→62.40% | 43 次月下载；公开逐 checkpoint GSM8K 曲线和训练参数 | 未见 GGUF，需自行转换 | Apache 2.0 | **思考终止对照。** 训练格式强制 `<think>... </think>` 后再给最终答案，可测终止率；数据和评测为英语数学，不能推断中文项目答疑提升 |
| 3 | [cnmoro/Qwen2.5-0.5B-Rag-Thinking][qwen25-rag] | 明确面向“基于提供上下文的基础 RAG 问答”，但模型卡只给葡萄牙语示例，没有训练数据、训练参数或评测 | 源模型 9 次月下载、7 赞；已有 3 个量化仓库，但卡片不完整 | 有作者 Q8_0 和社区 GGUF | 页面标 MIT；同时仍需履行 Qwen2.5 基础权重的 Apache 2.0 notices | **RAG 行为对照。** 架构简单、Q8_0 可直接测“是否更少脱离上下文”；葡语专训与文档不完整使其不能成为发布候选 |
| 4 | [dphn/Dolphin3.0-Qwen2.5-0.5B][dolphin] | Qwen2.5-0.5B 上的通用指令微调；列出 13 个数据集来源，ChatML，社区较成熟；没有已完成的模型评测 | 246 次月下载、26 赞；Dolphin 团队维护，已有 12 个量化仓库 | 至少 12 个社区量化，含 Q4_K_M/Q8_0 | Apache 2.0 | **非思考通用对照。** 可判断问题源自 Qwen3/3.5 思考机制还是单纯容量不足；主要英语、偏“uncensored”，不符合严格拒答产品目标 |
| 5 | [distil-labs/distil-qwen3-0.6b-voice-assistant-banking-gguf][distil-banking-gguf] | 77 条人工种子多轮会话，经 120B 教师扩增到数千条；14 个封闭意图；自报工具调用准确率 48.7%→90.9% | GGUF 13 次月下载；公开演示仓库约 70 星/13 fork，训练与部署链较完整 | 作者提供 F16 GGUF（约 1.1GB）；可再量化为 Q8_0 | Apache 2.0 | **训练方案对照，而非直接答疑模型。** 它证明 0.6B 在小型封闭分类/抽取任务上能可靠得多，建议复用“人工种子→教师扩增→验证器过滤→冻结集”方法 |

### 2.1 Qwen3.5 推理蒸馏模型

`Jackrong` 模型是本次最贴近当前故障的实验对象：模型作者声称用固定的 `<think>…</think>` 结构和响应侧训练，降低 Qwen3.5 在简单问题上过渡语、重复和循环；训练集共 2,516 条，页面显示 837 次月下载、10 个赞，且标注支持中文。[模型卡][jackrong]

但它没有公开任务级评测；训练样本主要来自 Claude/Opus 推理蒸馏数据，数据来源与再发布边界需要逐项复核；正文的“academic research / learning only”又与 Apache 2.0 元数据冲突。它适合回答一个狭窄问题：**相同 140 题、相同 RAG 和 4K 输出预算下，是否能稳定关闭 `</think>` 并产生可见答案？** 若不能，就没有继续评估事实质量的价值。

`PursuitOfDataScience` 版本公开了更完整的训练参数和逐 checkpoint GSM8K 曲线，最终比带 `<think>` 的 Base 提升 4.17 个百分点；它也是英语数学模型，硬编码思考前缀，最大训练序列仅 4,096。它可作为第二个终止率对照，但并不天然适合短客服回答。[模型卡与评测][qwen35-thinking]

### 2.2 Qwen2.5 RAG 与通用指令对照

`Qwen2.5-0.5B-Rag-Thinking` 的目标描述与本项目最接近：系统提示要求始终以提供上下文为依据，示例也把上下文和问题明确分隔；作者提供 Q8_0 GGUF，下载成本低。[模型卡][qwen25-rag] [量化列表][qwen25-rag-quants]

它的问题同样明显：葡萄牙语专训、严格依赖自定义模板、没有训练集和独立测试集说明，示例答案还出现了只回答部分证据的倾向。可用它做 20–40 题快速对照，不应投入完整产品集前就更改现有下载清单。

`Dolphin3.0-Qwen2.5-0.5B` 来自更成熟的 Dolphin 社区，列出 OpenCoder、AgentInstruct、Hermes function calling、Tulu、SmolTalk 等 13 个来源，并有 12 个社区量化；但模型卡的评测仍是 `TBD`，且其产品理念强调由系统拥有者自行负责对齐。[模型卡][dolphin] [量化列表][dolphin-quants] 它适合作为“无 thinking 的老架构通用模型”控制组，不适合默认拒答严格的用户助手。

### 2.3 最接近本项目形态的成功案例

Distil Labs 的银行助手不是问答生成器，而是受限的意图分类/槽位抽取模型。它仍非常重要，因为其范围与本项目的“十几个帮助类别、短回答/安全动作”比数学通用榜单更接近：

- 77 条人工多轮种子，覆盖 14 个操作和口语/ASR 噪声；
- 120B 教师扩增到数千条，再做任务蒸馏；
- Qwen3-0.6B Base 在作者测试上为 48.7%，微调后为 90.9%，教师为 87.5%；
- 模型明确保留 `intent_unclear`，说明“不能判断就拒绝/路由”本身也应成为训练类别；
- 作者提供完整演示仓库和 GGUF/llama.cpp 用法。[模型卡][distil-banking-gguf] [GitHub 仓库][distil-banking-github]

这些数字是作者自报，且模型卡同时提醒 90.9% 仍代表约十分之一调用错误；不能把它换算成本项目问答正确率。但其数据工程方法比下载通用微调更值得复用。

## 3. 值得借鉴、但不应直接试用的模型

### 3.1 Distil-Localdoc-Qwen3-0.6B

该模型用 28 个 Python 函数/类作为种子，经 GPT-OSS-120B 扩增为 10,000 条合成样本，专项生成 Google 风格 docstring。作者在 250 条留出集上用 LLM-as-a-judge 评测，Qwen3-0.6B Base 为 0.55、微调版为 0.76，接近教师 0.81；模型为 Apache 2.0，并直接提供 GGUF。[模型卡][distil-localdoc]

它不能回答本项目问题，但再次说明：**亚 1B 的提升来自任务收窄、样本验证和输出格式固定，而不是把更多通用推理语料塞进去。**

### 3.2 AIguard-0.6B-PII-Chinese

这是基于 Qwen3-0.6B 的中文 token-classification 模型，支持 21 类 PII，作者报告实体级 F1 96.29%，Apache 2.0。[模型卡][aiguard] 它不是生成式助手，不能替代答疑模型；但本项目“没解决”反馈 JSON 的脱敏状态未来若需要本地自动检测，它是可单独评估的专项模型。

### 3.3 GeoLLM-Qwen3.5-0.8B

该模型对西澳地质问答做 LoRA，Apache 2.0。作者报告 QA ROUGE-L 从 0.1420 提到 0.1697、BERTScore 从 0.8120 提到 0.8447，但 hallucination pass rate 从 66.7% 降到 40.0%。[模型卡][geollm] 这是本项目最应重视的反例：**相似度分数上升可以与事实可靠性下降同时发生。** 所以验收必须单列“每条事实是否被检索片段支持”，不能只看 BLEU/ROUGE/BERTScore。

## 4. 明确排除项

| 模型/类别 | 排除原因 |
| --- | --- |
| `theneuralmaze/Qwen3-0.6B-Full-Finetuning-No-Thinking` 及其 GGUF | 名称很贴近需求且有现成量化，但原模型卡几乎全是 `[More Information Needed]`：无训练数据、用途、评测、许可证，违反可追溯门槛 |
| `andresnowak/Qwen3-0.6B-instruction-finetuned` | 训练混合约 157 万样本、参数与多项评测较完整，但仓库未声明许可证；且作者自述存在截断数据和左填充训练问题，不进入可发布候选 |
| `sparx3/Qwen3.5-0.8B-SFT-DPO` | 5k Alpaca + 2k UltraFeedback、LoRA SFT+DPO、代码公开；但英语为主，只用 10 条由另一个模型生成的测试，作者明确记录重复循环、事实/格式幻觉，正好命中当前故障 |
| `Qwen3.5-0.8B-thinking` 类数学 CoT 微调 | 即使数学分数上升，也通常需要 1K–4K 甚至更多输出 token；与本项目 384 token 短回答、低端 CPU 延迟目标冲突 |
| `abliterated / heretic / uncensored` 变体 | 目标是移除拒答或安全对齐，与“越界固定拒答、提示注入不泄漏”目标相反 |
| ModelScope `burtenshaw/qwen3-0.6b-fineproofs-sft` | 仅约 29.82MB/4.59M 参数，实际更像适配器；模型卡未披露训练数据、评测和许可证，不能作为完整模型制品 |
| ModelScope `spps1916/news_bot_lora` | 给出 Qwen2.5-0.5B、3k 自有新闻 + 3k Magpie 中文数据和 LoRA 命令，但模型卡大量 `More Information Needed`，用途偏情绪化新闻回复且许可证不清 |
| 纯量化/重打包仓库 | GGUF、AWQ、GPTQ、MLX 转换不会提升模型能力；必须沿模型树追溯到真正的微调源模型再评估 |

相关一手页面：[No-Thinking GGUF][no-think-gguf] [andresnowak 模型卡][andresnowak] [SFT+DPO 模型卡][sparx] [ModelScope fineproof][modelscope-fineproof] [ModelScope news bot][modelscope-news]

## 5. 建议的实验顺序

### 5.1 先做低成本门控冒烟

不要一次下载并跑完整 140 题。每个社区候选先跑同一组 24 题：

- 6 条直接使用说明；
- 4 条口语/错别字；
- 4 条缺少证据应拒答；
- 4 条提示注入；
- 3 条上一问题干扰；
- 3 条要求严格 3 步、无多余括号/标签的格式题。

先淘汰以下任一情况：可见答案率 < 100%、`</think>` 终止率 < 100%、提示注入通过率 < 95%、引用外事实 > 0、平均输出超过 384 token、中文术语一致性明显差于现有 Qwen3-0.6B。

建议顺序：

1. `Jackrong Qwen3.5 reasoning distilled`：只测循环是否缓解；需要先从 BF16 转 Q8_0。
2. `Qwen2.5-0.5B-Rag-Thinking Q8_0`：直接验证上下文约束，但预期中文迁移有限。
3. `Dolphin3.0-Qwen2.5-0.5B Q8_0`：作为无思考通用控制组。
4. `Pursuit Qwen3.5 thinking`：只有前三者不能解释故障时再测，避免高 token 成本。

银行模型不跑项目 QA；只复现其少量公开样例和 GGUF 加载，用于验证训练/转换链路。

### 5.2 真正值得发布的是项目自有微调

建议把社区案例转化为下面的数据设计：

- 200–500 条人工种子，围绕现有 `HelpEntry`、状态摘要、拒答和安全动作；
- 让较强教师对每条种子生成多种口语改写和候选答案；
- 用程序验证答案中的项目实体必须存在于检索条目，禁止生成链接、命令、房间号、用户名或日志；
- 人工审核后扩展到 3k–10k 条，而不是直接收集海量通用 CoT；
- 加入明确的 `out_of_scope`、`insufficient_evidence`、`prompt_injection` 和 `topic_switch` 样本；
- SFT 先训练最终答案，不训练长思维链；必要时只蒸馏一个很短的内部判定结构，并确保推理端不输出；
- 冻结训练/验证/测试集，事实正确率、拒答率、引用支持率和上一问题串扰分别计分。

发布门槛继续沿用项目方案：相对基础模型至少 +5 个百分点、项目事实正确率 ≥90%、该拒答时正确率 ≥95%、无隐私或越权回归。社区模型最多作为初始化或训练方法参考，不能绕过这套门槛。

## 一手来源

[qwen3]: https://huggingface.co/Qwen/Qwen3-0.6B
[qwen35]: https://huggingface.co/Qwen/Qwen3.5-0.8B
[qwen35-api]: https://huggingface.co/api/models/Qwen/Qwen3.5-0.8B
[qwen25]: https://huggingface.co/Qwen/Qwen2.5-0.5B-Instruct
[jackrong]: https://huggingface.co/Jackrong/Qwen3.5-0.8B-Claude-4.6-Opus-Reasoning-Distilled
[qwen35-thinking]: https://huggingface.co/PursuitOfDataScience/Qwen3.5-0.8B-thinking
[qwen25-rag]: https://huggingface.co/cnmoro/Qwen2.5-0.5B-Rag-Thinking
[qwen25-rag-quants]: https://huggingface.co/models?other=base_model%3Aquantized%3Acnmoro%2FQwen2.5-0.5B-Rag-Thinking
[dolphin]: https://huggingface.co/dphn/Dolphin3.0-Qwen2.5-0.5B
[dolphin-quants]: https://huggingface.co/models?other=base_model%3Aquantized%3Adphn%2FDolphin3.0-Qwen2.5-0.5B
[distil-banking-gguf]: https://huggingface.co/distil-labs/distil-qwen3-0.6b-voice-assistant-banking-gguf
[distil-banking-github]: https://github.com/distil-labs/distil-voice-assistant-banking
[distil-localdoc]: https://huggingface.co/distil-labs/Distil-Localdoc-Qwen3-0.6B
[aiguard]: https://huggingface.co/ZJUICSR/AIguard-pii-detection-fast
[geollm]: https://huggingface.co/AshkanTaghipour/GeoLLM-Qwen3.5-0.8B
[no-think-gguf]: https://huggingface.co/mradermacher/Qwen3-0.6B-Full-Finetuning-No-Thinking-i1-GGUF
[andresnowak]: https://huggingface.co/andresnowak/Qwen3-0.6B-instruction-finetuned
[sparx]: https://huggingface.co/sparx3/Qwen3.5-0.8B-SFT-DPO
[sparx-github]: https://github.com/bsparx/NLPPHase2
[modelscope-fineproof]: https://modelscope.cn/models/burtenshaw/qwen3-0.6b-fineproofs-sft
[modelscope-news]: https://modelscope.cn/models/spps1916/news_bot_lora
