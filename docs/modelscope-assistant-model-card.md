---
license: apache-2.0
frameworks:
  - GGUF
tasks:
  - text-generation
---

# Bilibili Live Gift Panel 答疑助手模型

该仓库为 [bilibili-live-gift-panel](https://github.com/brainfk123/bilibili-live-gift-panel) 的本地答疑助手提供独立更新制品。应用只在用户明确确认后下载模型，推理和对话均在本机完成。

## 当前版本

首版仅发布签名更新清单，模型权重直接引用 Qwen 官方 ModelScope 仓库，不重复上传：

| 字段 | 值 |
| --- | --- |
| 模型 | `Qwen/Qwen3-0.6B-GGUF` |
| 文件 | `Qwen3-0.6B-Q8_0.gguf` |
| 固定 revision | `6abe20cd0aed577f4d0b267935868ecae190aee9` |
| 大小 | `639446688` bytes |
| SHA-256 | `9465e63a22add5354d9bb4b99e90117043c7124007664907259bd16d043bb031` |
| 架构 / 量化 | `qwen3` / `Q8_0` |

`manifest.json` 使用 Ed25519 签名。应用先验证签名、固定 revision、文件大小、SHA-256、GGUF 架构和量化信息，验证成功后才加载模型。清单更新是人工发布，不会触发应用自动下载。

## 使用范围

答疑助手只依据应用内置帮助条目回答安装、登录、直播间连接、属性、礼物规则、定时器、OBS、盲盒、统计、更新及故障排查问题。检索低相关或超出项目范围时会拒答，不允许模型使用预训练记忆补充项目操作。

## 验证摘要

- Windows x64、CPU-only llama.cpp、4 线程、4096 context。
- 实际 GGUF 校验为 GGUF V3、Qwen3、Q8_0。
- 已验证中文生成、`/no_think` 无非空思考内容、请求取消和模型卸载路径。
- 本地参考机冷加载约 0.27 秒，热态首 token 约 0.20 秒；不同硬件结果会有差异。

## 后续微调版本

后续 LoRA SFT 版本只有在冻结评测集满足发布门槛时才会替换当前基础模型。仓库届时会增加 GGUF、模型卡更新、许可证和评测摘要；人工审核、脱敏后的训练数据默认不公开。

## 许可证

Qwen3 模型使用 Apache License 2.0。llama.cpp 使用 MIT License。应用分发包同时包含第三方许可证与修改说明。
