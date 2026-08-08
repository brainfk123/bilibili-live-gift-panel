# 严格小于 1B 参数的本地项目答疑助手模型调研

- 调研截止：2026-08-08
- 目标：为中文项目用户答疑助手筛选可本地部署、可做 RAG 与微调的模型。
- 纳入标准：模型总参数必须严格小于 1,000,000,000；不能只按型号中的“0.8B / 1B”判断。
- 来源范围：仅使用模型开发者的模型卡、技术报告、官方仓库/文档和方法原论文。公开评测只按各来源原样记录；不同发布方的提示词、样本、解码和评分设置不同，不能把跨来源分数当作同一排行榜。

## 结论

如果现在为本项目做第一版中文答疑助手，建议采用以下顺序：

1. **默认 MVP：Qwen3-0.6B + RAG，关闭思考模式。** 它是纯文本、预训练后再后训练的对话模型，总参数 0.6B、上下文 32,768、Apache 2.0，支持 100 多种语言和方言；官方列出的本地生态包括 Ollama、LM Studio、MLX-LM 和 llama.cpp。对短促、可引用项目文档的答疑，成熟运行时和纯文本路径比名义上的超长上下文更实用。[Qwen3-0.6B 模型卡][qwen3-card]
2. **质量/长上下文挑战者：Qwen3.5-0.8B。** 该官方模型仓库的 Hugging Face API 在 `safetensors.total` 中给出完整检查点计数 **873,438,784**，所以连同视觉组件仍严格小于 1B；模型卡把语言模型标为 0.8B，原生上下文为 262,144，支持 201 种语言和方言、Apache 2.0。它更新、覆盖更广，但需要较新的运行时，且是带视觉编码器的统一模型；正式选型前必须用同一批中文项目题与 Qwen3-0.6B 做端到端 A/B。[Qwen3.5 仓库元数据 API][qwen35-api] [Qwen3.5 模型卡][qwen35-card]
3. **更低资源的 Apache 2.0 备选：Granite-4.0-H-350M。** 实际为 340M、32K，上下文和中文均在官方支持范围内；IBM 明确把问答、RAG、抽取、函数调用和多语言对话列为用途，并提供官方 GGUF。[Granite 模型卡][granite-card] [Granite GGUF][granite-gguf]
4. **极致端侧备选：LFM2.5-350M / 230M。** 二者都有指令模型、Base 模型、32K、中文及多种官方部署格式；230M 的官方 4-bit 测试在 Raspberry Pi 5 为 293 MB / 42 tok/s，在 Galaxy S25 Ultra 为 375 MB / 213 tok/s（2K 输入场景）。但 LFM Open License v1.0 对年收入达到 1,000 万美元的实体不许可商业使用，商业产品不能把它当作 Apache 2.0 等价物。[LFM2.5-350M 模型卡][lfm25-350-card] [LFM2.5-230M 模型卡][lfm25-230-card] [LFM 许可证][lfm-license]
5. **Qwen2.5-0.5B-Instruct 是稳定基线，不是首选终点。** 它准确为 0.49B、32K 输入/8K 生成、29 种以上语言（含中文）、Apache 2.0，运行时要求低于新架构，适合做回退对照。[Qwen2.5-0.5B-Instruct 模型卡][qwen25-card]

SmolLM2-360M-Instruct 虽然小且开放，但官方明确说它主要理解和生成英语；不应作为中文答疑默认模型。[SmolLM2 模型卡][smollm2-card]

如果任务被进一步收窄为分类、路由、字段抽取或固定格式改写，可另测 **Gemma 3 270M IT**。它严格小于 1B，并且为专项微调而设计，但 Google 明确说它不面向复杂对话，因此不应与通用答疑模型混为一类。[Gemma 3 270M 发布说明][gemma270-blog]

## 1. 严格参数门槛核验

| 模型 | 阶段/直接可对话 | 参数证据 | 严格小于 1B | 上下文 | 语言 | 许可证 |
| --- | --- | ---: | :---: | ---: | --- | --- |
| **Qwen3.5-0.8B** | 后训练指令模型；另有 Base | 完整检查点 **873,438,784**；语言模型标称 0.8B | 是 | 262,144 原生 | 201 种语言/方言，含中文 | Apache 2.0 |
| **Qwen3-0.6B** | 预训练 + 后训练；直接对话，可思考/非思考 | 0.6B，总参数；非嵌入 0.44B | 是 | 32,768 | 100+（技术报告为 119），含中文 | Apache 2.0 |
| **Qwen2.5-0.5B-Instruct** | 预训练 + 后训练指令模型；另有 Base | **0.49B**；非嵌入 0.36B | 是 | 32,768；最多生成 8,192 | 29+，含中文 | Apache 2.0 |
| **Granite-4.0-H-350M** | 指令模型；另有 Base | **340M** | 是 | 32K | 12 种，含中文 | Apache 2.0 |
| **LFM2.5-350M** | 通用指令模型；另有 Base | 350M | 是 | 32,768 | 9 种，含中文 | LFM Open License v1.0，有商业收入门槛 |
| **LFM2.5-230M** | 通用指令模型；另有 Base | 230M | 是 | 32,768 | 10 种，含中文 | LFM Open License v1.0，有商业收入门槛 |
| **Gemma 3 270M IT** | 指令模型；另有预训练版 | 170M 嵌入 + 100M Transformer = **270M** | 是 | 32K | 训练数据覆盖 140+ 语言 | Gemma Terms |
| **SmolLM2-360M-Instruct** | Base 经 SFT + DPO | 360M | 是 | 8,192（配置文件） | 主要为英语 | Apache 2.0 |
| **Llama 3.2 1B Instruct** | 有预训练版和指令版 | **1.23B** | **否** | 128K；官方量化版 8K | 官方支持 8 种语言，不含中文 | Llama 3.2 Community License |
| **Gemma 3 1B IT** | 有预训练版和指令版 | 302M 嵌入 + 698M 非嵌入 = **1,000M** | **否**（等于而非小于） | 32K | 训练数据覆盖 140+ 语言；1B 为纯文本 | Gemma Terms |

表中事实分别来自各官方模型卡/配置和技术报告。[Qwen3.5][qwen35-card] [Qwen3][qwen3-card] [Qwen2.5][qwen25-card] [Granite][granite-card] [LFM2.5-350M][lfm25-350-card] [LFM2.5-230M][lfm25-230-card] [Gemma 3 270M][gemma270-card] [SmolLM2 配置][smollm2-config] [Llama 3.2][llama32-card] [Gemma 3 技术报告][gemma3-report]

两个容易误判的结论：

- **Llama 3.2 “1B”不是亚 1B。** Meta 模型卡给出的真实规模是 1.23B；量化只改变权重表示和内存，不改变参数数量。[Llama 3.2 模型卡][llama32-card]
- **Gemma 3 “1B”也不按亚 1B 纳入。** Google 技术报告公开的是 302M 嵌入参数与 698M 非嵌入参数，按官方披露口径合计 1,000M；严格不等式不成立。这里不用架构尺寸自行反推“精确 tensor 元素数”，因为参数共享、辅助张量等计数约定会改变结果，且那不是 Google 对该检查点的精确参数声明。[Gemma 3 技术报告][gemma3-report]

两款排除项仍有部署参考价值：Meta 的 Llama 3.2 官方量化变体把上下文限制为 8K；Google 的 Gemma 3 QAT 说明给出的 **1B 权重加载显存**为 BF16 约 2 GB、int4 约 0.5 GB（不含随上下文增长的 KV cache）。Gemma 不是 Apache 2.0，而受包含分发要求与禁止用途条款的 Gemma Terms 约束。[Llama 3.2 模型卡][llama32-card] [Gemma 3 QAT][gemma3-qat] [Gemma Terms][gemma-terms]

## 2. 候选逐项判断

### 2.1 Qwen3-0.6B：最稳妥的默认生成器

官方模型卡确认它已经完成预训练与后训练，不是仅供续写的 Base 模型；支持思考和非思考模式、32,768 上下文、Apache 2.0，并列出 Transformers、SGLang、vLLM 及多种本地应用支持。项目答疑通常要求短、确定、可引用，因此建议默认 `enable_thinking=false`，只在确实需要多步推理的后台任务打开思考模式。模型卡还警告可能发生无休止重复，并建议在该现象出现时设置 `presence_penalty=1.5`，这应进入回归测试。[Qwen3-0.6B 模型卡][qwen3-card]

官方速度表的 NVIDIA H20 96GB / Transformers / batch 1 / 生成 2,048 token 场景中，输入长度 1 时 BF16 为 58.57 tok/s、1,394 MB，GPTQ-Int8 为 26.56 tok/s、986 MB；到 30,720 token 输入时两者分别占 4,755 MB 与 4,347 MB。这不是消费级 CPU 结论，但清楚展示了长上下文 KV cache 会吞掉量化节省的一部分。[Qwen3 官方速度表][qwen3-speed]

Qwen 官方个体模型卡没有给出 0.6B 的独立完整数值评测表，因此不能据此宣称它在所有亚 1B 模型中第一。Qwen3 技术报告只支持“0.6B 是该系列最小规格、全系列扩展到 119 种语言且使用 Apache 2.0”等系列结论。[Qwen3 技术报告][qwen3-report]

### 2.2 Qwen3.5-0.8B：最值得并行验证的新模型

这是一个后训练的统一视觉-语言模型。官方仓库给出语言模型 0.8B、原生 262,144 上下文、201 种语言/方言；该仓库 API 的 `safetensors.total` 为 873,438,784，解决了“视觉编码器会不会把总量推过 1B”的疑问。[Qwen3.5 模型卡][qwen35-card] [Qwen3.5 仓库元数据 API][qwen35-api]

官方文本评测中，0.8B 非思考模式为 MMLU-Pro 29.7、C-Eval 46.4、IFEval 52.1；思考模式 C-Eval 50.5、LongBench v2 26.1。它有长窗口，但长上下文分数仍提醒我们：**能装入文档不等于能可靠利用全部文档**，RAG 仍应先检索再给少量证据。[Qwen3.5 评测表][qwen35-eval]

部署侧的现实限制是：官方示例要求较新的 Transformers、SGLang 或 vLLM；vLLM 示例使用 nightly/主分支，并提供 `--language-model-only` 跳过视觉编码器和多模态 profiling。它适合作为 Qwen3-0.6B 的挑战者，而不是未经项目集评测就直接替换。[Qwen3.5 部署说明][qwen35-card]

### 2.3 Qwen2.5-0.5B-Instruct：兼容性基线

该指令模型准确为 0.49B，支持中文在内的 29 种以上语言，32,768 总上下文、8,192 最大生成长度，Apache 2.0。官方同设置评测给出 MMLU-Pro 15.0、MATH 34.4、GSM8K 49.6、HumanEval 35.4、IFEval strict-prompt 27.9；这些数字适合与其前代做纵向比较，不应直接与其他厂商模型卡横比。[Qwen2.5 模型卡][qwen25-card] [Qwen2.5 官方评测][qwen25-eval]

官方 A100 80GB / Transformers / batch 1 / 生成 2,048 token 测试中，输入长度 1 时 BF16 为 47.40 tok/s、0.97 GB，GPTQ-Int4 为 50.60 tok/s、0.48 GB；30,720 token 输入时两者分别占 2.34 GB 与 1.85 GB。该数据同样不应外推到普通 CPU，但可作为部署容量上界测试的起点。[Qwen2.5 官方速度表][qwen25-speed]

如果答疑大量涉及代码、JSON 或公式，可额外试验 **Qwen2.5-Coder-0.5B-Instruct**：官方规格同样是 0.49B、32K、Apache 2.0，并提供 Base/指令版本和官方 GGUF。但它是代码专用模型，不应仅因“Coder”名称就替代通用中文模型；应在项目代码解释题上单独 A/B。[Qwen2.5-Coder 规格][qwen25-coder]

### 2.4 Granite-4.0-H-350M：开放许可证下的低资源候选

IBM 的 H 版本是 4 个注意力层 + 28 个 Mamba2 层的混合模型，实际 340M、32K、支持中文，Apache 2.0。官方直接列出 RAG、问答、摘要、抽取、代码和函数调用用途，并提供 Base 与指令模型，适合把“直接答疑”和“继续训练/适配”分开。[Granite 模型卡][granite-card]

同一模型卡报告 H-350M 的 MMLU 36.21、IFEval 平均 61.63、MMMLU 27.95、MGSM 16.16，并提醒其指令数据仍以英语为主、多语言效果可能弱于英语。官方 GGUF 可直接用于 llama.cpp，因此它是低资源中文 A/B 候选，但必须用真实中文语料验证。[Granite 模型卡][granite-card] [Granite GGUF][granite-gguf]

### 2.5 LFM2.5-350M / 230M：速度强，许可证需先审

LFM2.5-350M 是 350M 通用指令模型，另有 Base；官方列出 32,768 上下文、中文等 9 种语言、GGUF/ONNX/MLX/OpenVINO，并报告低于 1 GB 内存、AMD CPU 313 tok/s、Snapdragon Gen4 188 tok/s。官方评测给出 IFEval 76.96、IFBench 40.69、Multi-IF 44.92，同时明确不推荐知识密集和编程任务。[LFM2.5-350M 模型卡][lfm25-350-card]

LFM2.5-230M 另有 Base 与指令版，支持中文等 10 种语言、32,768 上下文。官方评测为 IFEval 71.71、IFBench 38.40、Multi-IF 37.70，并明确不推荐高强度推理、代码和创作；它更像检索结果的轻量抽取/改写器，而不是独立知识助手。[LFM2.5-230M 模型卡][lfm25-230-card]

两者的主要产品风险是许可证：LFM Open License v1.0 将商业使用定义得较广，并规定年收入达到 1,000 万美元或以上的实体不在许可范围内。若未来用途、公司规模或分发方式不确定，优先 Apache 2.0 候选。[LFM 许可证][lfm-license]

### 2.6 SmolLM2-360M-Instruct：英语实验基线

SmolLM2-360M-Instruct 由 360M Base 经 SFT 和 DPO 得到，配置最大位置为 8,192，Apache 2.0。官方零样本/少样本表报告 IFEval 41.0、MT-Bench 3.66、MMLU cloze 32.8、GSM8K 5-shot 7.43；官方同时明确模型主要理解和生成英语，可能产生不准确、不一致或带偏见的内容。[SmolLM2 模型卡][smollm2-card] [SmolLM2 配置][smollm2-config]

官方仓库提供 Transformers.js、CPU 运行方式和多种 ONNX 文件：FP16 725 MB、int8 365 MB、q4f16 273 MB 等。它适合验证浏览器/极低资源链路，但不适合作为中文产品默认值。[SmolLM2 ONNX][smollm2-onnx]

### 2.7 Gemma 3 270M IT：专项微调器，不是通用答疑主模型

Google 官方发布说明把 270M 拆为 170M 嵌入与 100M Transformer 参数，并同时发布预训练与指令版本。官方模型卡给出 32K、140+ 语言以及 IT 的 IFEval 51.2、HellaSwag 37.7、PIQA 66.2；Google 还提供 INT4 QAT 检查点，Pixel 9 Pro SoC 的内部测试中 25 次对话耗电 0.75%。[Gemma 3 270M 模型卡][gemma270-card] [Gemma 3 270M 发布说明][gemma270-blog]

它的价值是把任务收窄后微调成分类、抽取、路由或结构化输出器。Google 明确说明它不为复杂对话设计，因此即便项目微调成功，也不应假设它能胜任开放式、多轮、知识密集的用户答疑；许可证仍是 Gemma Terms。[Gemma 3 270M 发布说明][gemma270-blog] [Gemma Terms][gemma-terms]

## 3. RAG 与微调怎样分工

### 3.1 先做 RAG

项目知识（界面名称、配置步骤、字段语义、版本差异、已知问题）会持续变化，也要求指出出处。RAG 原论文的核心动机正是：纯参数模型难以精确访问、更新知识并提供来源；外部非参数记忆可以生成更具体、更有事实性的答案。[RAG 原论文][rag-paper]

因此第一版应：

1. 只索引版本化的官方项目文档、FAQ、错误码和发布说明；每个片段保存文档标题、版本、章节和链接。
2. 检索少量最相关片段后再生成，并要求逐条引用；证据不足或互相冲突时回答“不确定/需要确认版本”，不能让模型补全缺失事实。
3. 把界面字段名、公式、命令和错误码视为精确字符串；答案生成后做引用存在性和关键字校验。
4. 长上下文只作为容纳检索证据的上限，不把整仓库直接塞进提示词。亚 1B 模型容量有限，过多无关上下文会放大检索错误和指令遗忘风险；这是需要项目测试验证的工程判断，而不是模型卡承诺。

RAG 的局限也必须承认：检索漏召回、切分断句、版本混杂或召回了错误页面时，生成器仍可能给出错误答案；“带检索”不等于“保证事实正确”。所以拒答率、引用支持率和旧版本冲突题必须成为验收指标。

### 3.2 微调只负责稳定行为，不负责记住易变事实

微调适合固化：中文语气、答案结构、何时追问版本/操作系统、如何引用、何时拒答、如何把自然语言映射为项目中的固定术语。它不适合把频繁变化的功能说明和版本事实烘进权重，因为更新与溯源困难。

优先用指令模型做 LoRA/QLoRA，而不是直接让 Base 模型对话。LoRA 冻结原权重、只训练低秩矩阵，可显著减少可训练参数；QLoRA 再把冻结底模量化到 4-bit 并训练 LoRA，从而降低微调内存。[LoRA 原论文][lora-paper] [QLoRA 原论文][qlora-paper]

推荐顺序是：

1. 零微调 RAG 基线；
2. 用少量高质量项目对话做 LoRA SFT，重点加入“证据不足拒答、引用、澄清问题”和真实失败样例；
3. 再测试 RAG + LoRA 是否同时提高事实正确率与格式稳定性；
4. 只有在有持续预训练语料、回归集和遗忘评测时才考虑 Base 模型继续预训练。

LFM2.5 官方卡列出了 CPT、LoRA SFT、DPO 和 GRPO 路径；Granite 也明确定位于小模型专项微调。可微调不代表微调后自动可靠，数据污染、灾难性遗忘和过度迎合仍需要回归测试。[LFM2.5 微调说明][lfm25-230-card] [Granite 模型卡][granite-card]

## 4. 建议的同条件验收

不要按公开榜单直接定型。至少让 Qwen3-0.6B、Qwen3.5-0.8B、Granite-4.0-H-350M 在完全相同的检索结果、提示词、量化等级、最大输出长度和硬件上回答同一套中文项目题；许可证允许时再加入 LFM2.5-350M。

测试集建议覆盖：

- 精确事实：字段含义、按钮路径、默认值、单位、公式；
- 多步操作：安装、首次配置、OBS 接入和排障；
- 版本题：当前版本与旧版文档冲突；
- 无答案题：文档没有写、用户前提错误、要求猜测；
- 语言题：口语、省略、错别字、中英混杂；
- 安全题：提示注入要求忽略证据或伪造引用。

验收至少记录：答案正确率、引用支持率、应拒答时的拒答率、漏答率、中文术语一致性、首 token 延迟、生成速度、峰值内存和长对话退化。BF16/FP16 作为质量基线，再比较 Q8/Q4；量化后的模型仍有同样数量的参数，只是存储精度不同。

最终建议不是“找一个榜单最高的小模型”，而是：

> **Qwen3-0.6B 非思考模式做首个可部署基线；Qwen3.5-0.8B 做同题挑战者；用 RAG 管事实和引用，用 LoRA 管语气、格式与拒答。若内存更紧，先试 Granite-4.0-H-350M；LFM2.5 只有在许可证确认后再进入商业候选。**

## 一手来源

[qwen35-api]: https://huggingface.co/api/models/Qwen/Qwen3.5-0.8B
[qwen35-card]: https://huggingface.co/Qwen/Qwen3.5-0.8B
[qwen35-eval]: https://huggingface.co/Qwen/Qwen3.5-0.8B#benchmark-results
[qwen3-card]: https://huggingface.co/Qwen/Qwen3-0.6B
[qwen3-report]: https://arxiv.org/abs/2505.09388
[qwen3-speed]: https://qwen.readthedocs.io/en/stable/getting_started/speed_benchmark.html#qwen3-0-6b-transformers
[qwen25-card]: https://huggingface.co/Qwen/Qwen2.5-0.5B-Instruct
[qwen25-eval]: https://qwenlm.github.io/blog/qwen2.5-llm/#qwen25-05b15b-instruct-performance
[qwen25-coder]: https://qwenlm.github.io/blog/qwen2.5-coder-family/#diverse-rich-model-sizes
[qwen25-speed]: https://qwen.readthedocs.io/en/v2.5/benchmark/speed_benchmark.html
[granite-card]: https://huggingface.co/ibm-granite/granite-4.0-h-350m
[granite-gguf]: https://huggingface.co/ibm-granite/granite-4.0-h-350m-GGUF
[lfm25-350-card]: https://huggingface.co/LiquidAI/LFM2.5-350M
[lfm25-230-card]: https://huggingface.co/LiquidAI/LFM2.5-230M
[lfm-license]: https://huggingface.co/LiquidAI/LFM2.5-350M/blob/main/LICENSE
[smollm2-card]: https://huggingface.co/HuggingFaceTB/SmolLM2-360M-Instruct
[smollm2-config]: https://huggingface.co/HuggingFaceTB/SmolLM2-360M-Instruct/blob/main/config.json
[smollm2-onnx]: https://huggingface.co/HuggingFaceTB/SmolLM2-360M-Instruct/tree/main/onnx
[llama32-card]: https://github.com/meta-llama/llama-models/blob/main/models/llama3_2/MODEL_CARD.md
[gemma3-report]: https://arxiv.org/abs/2503.19786
[gemma3-qat]: https://developers.googleblog.com/en/gemma-3-quantized-aware-trained-state-of-the-art-ai-to-consumer-gpus/
[gemma270-card]: https://huggingface.co/google/gemma-3-270m-it
[gemma270-blog]: https://developers.googleblog.com/introducing-gemma-3-270m/
[gemma-terms]: https://ai.google.dev/gemma/terms
[rag-paper]: https://arxiv.org/abs/2005.11401
[lora-paper]: https://arxiv.org/abs/2106.09685
[qlora-paper]: https://arxiv.org/abs/2305.14314
