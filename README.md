# Bilibili 直播礼物属性面板

OBS 工具：观众送礼物后，按照配置的规则增加“加班时间”“积分”等数值。双击构建后的 `dist/gift-panel.exe` 会启动本地服务并自动打开内置配置向导。

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

### 公式语义

公式结果现在会直接成为属性的新值。需要累加时，请把当前属性名写进公式，例如 `早播次数+1`。
升级到赋值语义后，之前保存的规则不会自动转换，请逐条打开并检查。
