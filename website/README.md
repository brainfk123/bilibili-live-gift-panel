# Lighthouse 网站部署

这个目录是项目官网的独立静态页面，不会覆盖根目录中的 Vite 应用入口。页面为静态内容，不启用托管应用或 MySQL。

## 域名与 DNS

备案审核部署期间，`bilibililive.cn`、`www.bilibililive.cn` 和 `app.bilibililive.cn` 均解析到 `124.220.60.152`。

## Ubuntu / Debian 部署

先在 Lighthouse 防火墙中放通 TCP 端口 `80` 和 `443`。SSH 端口 `22` 建议只允许自己的出口 IP 访问。

登录服务器后执行：

```bash
sudo apt update
sudo apt install -y nginx
sudo install -d -o "$USER" -g "$USER" /var/www/gift-panel
```

把本目录中的 `index.html` 上传到：

```text
/var/www/gift-panel/index.html
```

把 `nginx.conf.example` 上传并改名为：

```text
/etc/nginx/sites-available/gift-panel
```

启用站点：

```bash
sudo ln -s /etc/nginx/sites-available/gift-panel /etc/nginx/sites-enabled/gift-panel
sudo rm /etc/nginx/sites-enabled/default
sudo nginx -t
sudo systemctl enable --now nginx
sudo systemctl reload nginx
```

访问 `http://bilibililive.cn/healthz`，返回 `ok` 说明 Nginx 已正常工作。

## 配置 HTTPS

域名已解析、ICP备案已生效且 HTTP 可以访问后，可使用 Certbot 申请证书：

```bash
sudo apt install -y certbot python3-certbot-nginx
sudo certbot --nginx -d bilibililive.cn -d www.bilibililive.cn -d app.bilibililive.cn
sudo certbot renew --dry-run
```

也可以在腾讯云申请证书并按照腾讯云 Nginx 证书文档手动配置。

## 腾讯云远程操作方式

- **OrcaTerm**：腾讯云网页终端，适合第一次登录、上传文件和手工排查。
- **自动化助手 TAT**：不需要 SSH 密码即可执行 Shell/PowerShell，控制台和 API 都支持 Lighthouse。
- **Lighthouse API**：管理实例启停、重启、防火墙、快照和实例信息；它不负责执行操作系统内部命令。

第一次部署建议使用 OrcaTerm。后续自动发布可改用 TAT `RunCommand`，或者使用 SSH/SCP + GitHub Actions。

调用 TAT 时不要把腾讯云 `SecretId`、`SecretKey` 写进仓库、网页或客户端。为自动部署创建单独的 CAM 子用户，只授予目标 Lighthouse 和 TAT 所需的最小权限。
