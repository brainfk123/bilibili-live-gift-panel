# Third-party notices

The local answering assistant uses the following third-party works. Their full license texts are included under `third_party/licenses/`.

## llama.cpp

- Project: https://github.com/ggml-org/llama.cpp
- Pinned source: tag `b9637`, commit `aedb2a5e9ca3d4064148bbb919e0ddc0c1b70ab3`
- Copyright: Copyright (c) 2023-2026 The ggml authors
- License: MIT (`third_party/licenses/llama.cpp-MIT.txt`)

The build downloads and verifies the checksum-pinned source archive, then applies one documented Windows build compatibility change before compiling only its CPU static libraries: MinGW-w64 runtime v11 omits the `THREAD_POWER_THROTTLING_STATE` declarations that its `SetThreadInformation` API requires, so the build supplies the Windows SDK-compatible structure and constants when they are absent. This project also supplies a separate C bridge and Go Adapter; those integration files are not upstream llama.cpp files.

## Qwen3-0.6B-GGUF

- Model publisher: Qwen team, Alibaba Cloud
- Initial repository: https://modelscope.cn/models/Qwen/Qwen3-0.6B-GGUF
- License: Apache License 2.0 (`third_party/licenses/Qwen3-Apache-2.0.txt`)

The model weights are not bundled in this repository or executable. A user explicitly downloads a checksum- and signature-verified Q8_0 model from ModelScope. A future fine-tuned model must retain the Apache 2.0 notices, identify its modifications, and include its own model card.
