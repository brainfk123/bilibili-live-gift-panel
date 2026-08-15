# ICP Review Landing Deployment Result

Date: 2026-08-15 (Asia/Shanghai)

## Scope and instance identity

- Tencent Cloud Lighthouse instance ID: `lhins-j4cqq4ao`
- Instance name: `bilibili-live-pilot`
- Region: Shanghai (`ap-shanghai`)
- Public IPv4: `124.220.60.152`
- The Beijing instance `OpenCode-fRh0` was not accessed or modified.
- This deployment serves only the static review landing page and its `/healthz` endpoint. It does not deploy an application service, database, login, invitation, or Bilibili connection.

## Deployed files and integrity

- Deployment archive: `/tmp/icp-review-site.tar.gz`
- SHA-256: `ab7b8641e6717a95401d7c598420aacaec0750c0c4953d996517b0ff2a562e00`
- Landing page: `/var/www/gift-panel/index.html` (`root:root`, mode `0644`, 8240 bytes)
- Nginx site configuration: `/etc/nginx/sites-available/gift-panel` (`root:root`, mode `0644`, 2103 bytes)
- Enabled-site link: `/etc/nginx/sites-enabled/gift-panel` -> `/etc/nginx/sites-available/gift-panel`

The archive digest was verified on the Shanghai server with `sha256sum` and independently matched the local deployment archive.

## HTTP public verification

Before certificate issuance, the three public HTTP homepages were checked and each returned the same approved review landing page:

- `http://bilibililive.cn/`
- `http://www.bilibililive.cn/`
- `http://app.bilibililive.cn/`

`http://bilibililive.cn/healthz` returned `ok`. After Certbot installed the certificate, `http://bilibililive.cn/` returned `HTTP/1.1 301 Moved Permanently` with `Location: https://bilibililive.cn/`.

## Nginx validation

`sudo nginx -t` reported that the configuration syntax is OK and that the configuration test is successful.

## HTTPS issuance and verification

- `certbot` and `python3-certbot-nginx` were installed on the Shanghai instance.
- Certbot successfully received and installed certificate `bilibililive.cn` into the `gift-panel` Nginx site.
- Certificate key type: ECDSA.
- Certificate domains: `bilibililive.cn`, `www.bilibililive.cn`, and `app.bilibililive.cn`.
- Certificate path: `/etc/letsencrypt/live/bilibililive.cn/fullchain.pem`.
- Certificate expiry: `2026-11-13 02:50:13+00:00` (89 days reported at issuance).
- `curl --fail --silent --show-error https://bilibililive.cn/healthz` returned `ok`.
- `sudo certbot renew --dry-run` completed successfully for the certificate.

No certificate private-key contents or certificate contact information are recorded here.

## Public HTTPS content verification

The following public pages were inspected in a browser:

- `https://bilibililive.cn/`
- `https://www.bilibililive.cn/`
- `https://app.bilibililive.cn/`

All three displayed the title `礼物互动工坊｜直播互动工具应用`, the approved brand content, and `粤ICP备2026116328号`. The browser client blocked direct display of the plain-text health endpoint; the authoritative server-side HTTPS `curl` check above returned `ok`.

## Listener boundary

Final `sudo ss -ltnp` inspection showed public listeners only on:

- TCP 22: `sshd`
- TCP 80: `nginx`
- TCP 443: `nginx`

All remaining observed resolver/container runtime listeners were loopback-bound. No public listener existed on TCP 3306 or an application port. The Shanghai Lighthouse firewall has an inbound allow rule for TCP 443 from `0.0.0.0/0`, with remark `Web服务HTTPS`; no database or application-port firewall rule was added.

## Backup and rollback

Task 4 confirmed that `/var/www/gift-panel` and `/etc/nginx/sites-available/gift-panel` did not exist before this deployment. Consequently, no pre-existing target backup files exist and there is no prior site configuration or page to restore.

To stop serving this newly introduced site while preserving unrelated Nginx sites, use:

```bash
sudo unlink /etc/nginx/sites-enabled/gift-panel
sudo nginx -t && sudo systemctl reload nginx
```

Do not delete the certificate until confirming no other enabled Nginx site references it. If the certificate is exclusively used by this removed site and certificate removal is explicitly desired, use:

```bash
sudo certbot delete --cert-name bilibililive.cn
```

The deployed page and disabled site configuration are intentionally retained unless an explicitly approved cleanup is required; there was no prior target file to replace.

## Data-handling confirmation

This record contains no database credentials, Bilibili cookies, invitation codes, certificate contact email, certificate private-key contents, or ICP registrant personal data.
