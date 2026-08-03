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

## 发布与自动更新

正式版本使用 `vMAJOR.MINOR.PATCH` 标签发布，且标签必须与 `package.json` 中的版本一致。例如：

```powershell
git tag v0.1.0
git push origin v0.1.0
```

GitHub Actions 会把每次推送的分支或标签同步到独立的 GitCode 仓库。发布标签时还会运行测试、构建 `gift-panel-windows-x64.exe`、生成 SHA-256 文件并创建 GitHub Release，再将同一版本和附件发布到 GitCode。GitCode 同步失败不会阻断 GitHub 正常发布。

正式 EXE 启动时及每 6 小时会在后台检查最新正式 Release：优先访问 GitCode，连接失败或尚无发行版时自动回退 GitHub。程序会静默下载并校验 SHA-256，在用户从托盘退出后台程序后替换 EXE。配置页面的“外观与数据 → 程序更新”中可以关闭自动下载或手动检查更新。

本地 `npm run build` 生成的是 `dev` 版本，不会访问在线更新接口。如需在本地构建指定版本，可设置 `APP_VERSION` 和 `APP_COMMIT` 环境变量。发布失败后可在 GitHub Actions 手动运行 Release 工作流并填写已有标签，以重新构建和修复 Release 镜像。

### 规则语义

规则结果现在会直接成为属性的新值。需要累加时，请把当前属性名写进规则，例如 `早播次数+1`。
升级到赋值语义后，之前保存的规则不会自动转换，请逐条检查，必要时删除后重建。
