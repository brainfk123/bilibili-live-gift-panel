# Bilibili 直播礼物属性面板

OBS 工具：观众送礼物后，按照配置的规则把结果写入“加班时间”“积分”等属性。双击构建后的 `dist/gift-panel.exe` 会启动本地服务并自动打开内置配置向导。

## 开发构建

需要 Node.js 和 Go。在项目根目录运行：

```bash
npm install
npm run build
npm test
npm run typecheck
```

Go 服务测试需要以 `goserver` 为工作目录运行：

```bash
cd goserver
go test ./...
```

`npm run build` 会构建前端并编译本地服务，产物为 `dist/gift-panel.exe`。EXE 已内嵌前端页面和代理服务，主播电脑不需要安装 Node.js 或 Go。

普通构建不包含答疑模型的原生推理运行时。Windows x64 正式构建、固定工具链、模型清单签名和静态依赖验证见 [答疑助手原生构建说明](docs/assistant-native-build.md)。

## 发布与自动更新

正式版本使用 `vMAJOR.MINOR.PATCH` 标签发布，且标签必须与 `package.json` 中的版本一致。例如：

```powershell
git tag v0.1.0
git push origin v0.1.0
```

发布标签时，GitHub Actions 会运行测试、构建 `gift-panel-windows-x64.exe`、生成 SHA-256 文件与更新清单，并创建 GitHub Release。

正式 EXE 会在配置页和 OBS 面板全部关闭的空闲状态下读取 GitHub Latest Release 的静态更新清单，不占用 GitHub API 额度；之后每 6 小时最多自动检查一次。程序会静默下载、校验 SHA-256、替换 EXE 并重新启动后台服务。更新后的首次启动只显示系统通知，不会自动打开配置页面。配置页面的“外观与数据 → 程序更新”中可以关闭自动更新或手动检查更新。

本地 `npm run build` 生成的是 `dev` 版本，不会访问在线更新接口。如需在本地构建指定版本，可设置 `APP_VERSION` 和 `APP_COMMIT` 环境变量。发布失败后可在 GitHub Actions 手动运行 Release 工作流并填写已有标签，以重新构建和修复 GitHub Release。

### 更新日志写作规则

- 只记录用户能直接感知的重要新功能和 Bug 修复；排版微调、内部重构、字段变化等细节不写入更新日志。
- 新功能使用简单直白的小白说明，讲清楚“能做什么”和“从哪里开始用”；仅在确实有助于理解时配截图或动画。
- Bug 修复只写一句结果描述，不展开技术原因和实现细节。
- GitHub Release 保留所有版本记录；程序内置副本只保留最新版本。

### 规则语义

规则结果现在会直接成为属性的新值。需要累加时，请把当前属性名写进规则，例如 `早播次数+1`。
升级到赋值语义后，之前保存的规则不会自动转换，请逐条检查，必要时删除后重建。
