# 答疑助手原生构建与模型发布

普通 `npm run build` 和 `go test ./...` 不需要 C/C++ 工具链，此时 Go 使用明确的不可用 Adapter。正式发布必须使用 `llamacpp` build tag，将 CPU 推理运行时静态链接到 `gift-panel.exe`。

## 固定依赖

- llama.cpp tag：`b9637`
- commit：`aedb2a5e9ca3d4064148bbb919e0ddc0c1b70ab3`
- 官方源码归档 SHA-256：`762283319feb3de30886dc850d42f0e426b06600e7f9639d34e06506597309ca`
- Windows Release 工具链：MinGW-w64 13.2.0、CMake 3.31.8、Ninja 1.12.1

构建脚本关闭动态 backend、OpenMP、BLAS、llamafile、CUDA、HIP、Vulkan、SYCL 和 RPC，也关闭宿主机特定优化。推理使用同一个 `GGML_SCHED_PRIO_LOW` 线程池处理提示词和生成，Adapter 拒绝超过 4 个线程的配置。

固定的 MinGW-w64 13.2.0 制品使用 runtime v11，该版本提供 `SetThreadInformation` 和 `ThreadPowerThrottling`，但遗漏了对应的 `THREAD_POWER_THROTTLING_STATE` 声明。构建脚本在校验上游归档后确定性补入与 Windows SDK ABI 一致的结构和常量；此修改记录在构建元数据和第三方修改说明中。

## 本地 Windows x64 构建

确保 `gcc`、`g++`、`cmake`、`ninja` 和 `objdump` 位于 `PATH`，然后运行：

```powershell
npm run build:ui
npm run build:llama
$env:ASSISTANT_MANIFEST_PUBLIC_KEY_BASE64 = '<32-byte Ed25519 public key in Base64>'
npm run build:exe:native
npm run verify:exe-static -- dist/gift-panel.exe
```

下载的已校验源码和静态库只写入被 Git 忽略的 `.native/llama.cpp/`。`verify:exe-static` 会拒绝 `libgcc`、`libstdc++`、`libwinpthread`、OpenMP、llama 或 ggml DLL 依赖。

帮助库以 `src/data/help-content.json` 为唯一权威源。修改后运行 `npm run sync:assistant-help` 并提交派生文件；CI 使用 `npm run check:assistant-help` 阻止两份内容漂移。

## 签名模型清单

清单签名私钥不得生成或保存到仓库。复制 `assistant-model-manifest.example.json` 到仓库外或一个被忽略的临时位置，填写 ModelScope 的固定 revision、真实文件大小和 SHA-256。示例故意不可直接签名或部署。

签名命令只从环境变量或外部 PEM 文件读取 Ed25519 私钥。推荐使用口令加密的 PKCS#8 文件，并通过临时环境变量提供口令：

```powershell
$env:ASSISTANT_MANIFEST_PRIVATE_KEY_FILE = 'D:\secure\assistant-manifest-ed25519.pem'
$env:ASSISTANT_MANIFEST_PRIVATE_KEY_PASSPHRASE = '<从密码管理器读取，不要写入仓库>'
npm run sign:assistant-manifest -- unsigned-manifest.json signed-manifest.json
$env:ASSISTANT_MANIFEST_PRIVATE_KEY_PASSPHRASE = $null
```

本项目开发机使用 `%LocalAppData%\BilibiliLiveGiftPanel\secrets` 中的加密私钥和当前 Windows 用户的 DPAPI 凭据，因此日常签名可直接运行：

```powershell
npm run sign:assistant-manifest:local -- unsigned-manifest.json signed-manifest.json
```

DPAPI 文件不会解密为磁盘上的明文私钥或口令；脚本只在当前进程中短暂设置环境变量并在退出时清理。仓库外的 `E:\BilibiliLiveGiftPanel-PrivateBackup` 保存同一份加密 PKCS#8 备份。计划更换开发机时，应在旧机器可用时将备份重新绑定到新机器的凭据；如果旧 Windows 用户配置已经丢失，还需要事先保存在密码管理器中的恢复口令。

命令输出 raw 32-byte 公钥的 Base64 表示。将它配置为 GitHub Actions 仓库变量 `ASSISTANT_MANIFEST_PUBLIC_KEY_BASE64`；私钥只放在受控发布环境。可用 `ASSISTANT_MANIFEST_URL` 覆盖应用默认清单地址，但构建只接受 ModelScope 的 HTTPS 地址。

GitHub Actions 不接触私钥。仓库的 `Verify assistant model manifest` 手动工作流只从 ModelScope 下载已发布清单，并使用仓库变量 `ASSISTANT_MANIFEST_PUBLIC_KEY_BASE64` 验签；正式 EXE Release 也会执行同一检查。模型和签名清单仍由发布者在本地上传。

首版生产清单已经固定到 Qwen 官方制品：

- repository：`Qwen/Qwen3-0.6B-GGUF`
- revision：`6abe20cd0aed577f4d0b267935868ecae190aee9`
- file：`Qwen3-0.6B-Q8_0.gguf`
- size：`639446688`
- SHA-256：`9465e63a22add5354d9bb4b99e90117043c7124007664907259bd16d043bb031`

ModelScope 发布使用官方 `modelscope-hub` 客户端，并在本机交互式登录，Token 不得写入命令历史或聊天：

```powershell
.\.tmp\ms-hub-venv\Scripts\ms.exe login
.\.tmp\ms-hub-venv\Scripts\ms.exe create brainfk/bilibili-gift-panel-assistant-qwen3-0.6b --repo-type model --visibility public --license apache-2.0 --chinese-name "直播礼物面板答疑助手" --description "bilibili-live-gift-panel 本地答疑助手的签名模型清单" --exist-ok
.\.tmp\ms-hub-venv\Scripts\ms.exe upload brainfk/bilibili-gift-panel-assistant-qwen3-0.6b .tmp\assistant-model-upload --repo-type model --commit-message "publish signed Qwen3 0.6B Q8_0 manifest"
npm run verify:assistant-manifest -- https://modelscope.cn/models/brainfk/bilibili-gift-panel-assistant-qwen3-0.6b/resolve/master/manifest.json
```

`.tmp\assistant-model-upload` 只包含公开模型卡和签名 `manifest.json`，不得包含 `manifest.unsigned.json`、私钥、DPAPI 文件或 ModelScope Token。

开发机迁移时复制口令加密的 PKCS#8 文件，并从密码管理器取得恢复口令即可。Windows DPAPI 副本只能作为当前 Windows 用户配置下的便利凭据，不能作为唯一的跨机器恢复手段。GitHub 只保存公钥，不保存私钥或恢复口令。

Release 工作流设置 `REQUIRE_ASSISTANT_TRUST=1`，因此没有生产公钥时会直接失败，不会产出一个表面支持下载、实际无法验签的版本。签名覆盖 envelope 中 `payload` 的原始 UTF-8 JSON 字节，后端在解析业务字段前先验证签名。

## 仍需发布者提供的外部制品

- ModelScope 仓库 `brainfk/bilibili-gift-panel-assistant-qwen3-0.6b` 中的签名清单。
- Qwen 官方 Q8_0 文件的固定 revision、真实大小和 SHA-256；后续微调版使用相同字段。
- 仅由发布者保管的 Ed25519 私钥，以及与之配对的仓库公钥变量。

初始模型和后续微调模型都不进入 Git 仓库或 EXE。模型更新与程序发布相互独立，但新清单必须保持既有签名密钥或通过新的程序版本轮换公钥。

## 发布前真实模型验收

在参考机器（Windows x64、16GB 内存、4 核 CPU）设置 `ASSISTANT_TEST_MODEL` 后运行：

```powershell
$env:ASSISTANT_TEST_MODEL = 'D:\models\Qwen3-0.6B-Q8_0.gguf'
go -C goserver test -tags llamacpp ./assistant/llamacpp -run TestRealQwenSmoke -v
```

测试会强制检查冷加载不超过 15 秒、热态首 token 不超过 5 秒、生成速度不低于 5 个 token piece/s，并确认中文回答非空且没有 `<think>` 泄漏。发布者还需用任务管理器或性能采集工具确认相对未加载状态的额外峰值工作集不超过 1.5GB，并在直播事件与 OBS 同时运行的压力场景下确认事件无丢失、配置页保持响应。低于参考配置只允许尝试，不作为答疑助手性能承诺。
