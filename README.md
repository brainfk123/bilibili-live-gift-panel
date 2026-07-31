# Bilibili 直播礼物面板

OBS 浏览器源插件：监听直播间礼物，按可配置公式规则累加属性（如加班时间），实时面板展示。

## 构建

```
npm install
npm run fetch:catalog   # 抓取最新礼物目录（可选，构建会自动抓）
npm run build
```

产物：`dist/index.html`（单文件，无运行时依赖）。

## 使用

- **显示面板（OBS）**：浏览器源加载 `dist/index.html?mode=display`
- **配置面板（浏览器）**：打开 `dist/index.html?mode=config`
  填写房间号、创建属性、配置礼物规则、导出/导入配置。
  两个模式共享同一 localStorage。

## 注意事项

- 使用 B 站非官方 WebSocket 弹幕协议，仅供个人/私下使用，请勿公开传播
- 匿名接入，无需登录 B 站账号
- 若 OBS 的 file:// 下 localStorage 不持久，用本地静态服务加载（见下文）

## 本地静态服务（可选）

```
python -m http.server 8000 -d dist
```

OBS 加载 `http://localhost:8000/index.html?mode=display`。

## 技术栈

Vite + TypeScript + Vitest，运行时零第三方依赖（WebSocket / DecompressionStream / localStorage 均为浏览器内置）。
