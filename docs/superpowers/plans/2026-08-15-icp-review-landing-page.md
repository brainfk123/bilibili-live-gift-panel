# ICP Review Landing Page Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Publish a truthful static review landing page for `bilibililive.cn`, `www.bilibililive.cn`, and `app.bilibililive.cn` on the Shanghai Lighthouse instance.

**Architecture:** Reuse the repository's standalone `website/index.html` and serve it through a single Nginx virtual host on `bilibili-live-pilot`. Keep the page static and disconnected from MySQL and the local Go application; expose only the landing page and `/healthz`, then add HTTPS after HTTP verification.

**Tech Stack:** Static HTML/CSS, Nginx on Ubuntu 24.04, Vitest contract tests, Tencent Cloud Lighthouse, Certbot/Let's Encrypt.

## Global Constraints

- Operate only on Shanghai instance `bilibili-live-pilot` (`lhins-j4cqq4ao`, public IPv4 `124.220.60.152`); never access or modify the Beijing instance.
- Page brand is `礼物互动工坊`.
- Browser title is exactly `礼物互动工坊｜直播互动工具应用` and contains the备案 service name `直播互动工具应用`.
- The page must describe the available local open-source edition and show the invitation-only web edition as `建设中` without an active login control.
- The page must state that it does not provide network livestreaming, audio/video uploads, forum comments, news information, or transactions.
- The page must state that the project is not affiliated with or authorized by Bilibili.
- Display Guangdong subject ICP number `粤ICP备2026116328号` and link it to `https://beian.miit.gov.cn/`.
- Do not add analytics, advertisements, forms, third-party scripts, cookies, database access, or public application ports.
- Preserve unrelated and untracked workspace files. Commit only formal product files changed by each task.

---

### Task 1: Lock the Landing-Page Content Contract

**Files:**
- Create: `tests/website.test.ts`
- Modify: `website/index.html`

**Interfaces:**
- Consumes: The existing standalone HTML page at `website/index.html`.
- Produces: A static page whose metadata, visible copy, links, and privacy boundary are enforced by `tests/website.test.ts`.

- [ ] **Step 1: Write the failing page contract test**

Create `tests/website.test.ts` with:

```ts
import { readFileSync } from 'node:fs';
import { describe, expect, it } from 'vitest';

const html = readFileSync(new URL('../website/index.html', import.meta.url), 'utf8');

describe('public website contract', () => {
  it('matches the approved brand and ICP filing copy', () => {
    expect(html).toContain('<title>礼物互动工坊｜直播互动工具应用</title>');
    expect(html).toContain('礼物互动工坊');
    expect(html).toContain('直播互动工具应用');
    expect(html).toContain('本地开源版');
    expect(html).toContain('受邀网页版');
    expect(html).toContain('建设中');
    expect(html).toContain('粤ICP备2026116328号');
    expect(html).toContain('https://beian.miit.gov.cn/');
  });

  it('states the service boundary and does not collect data', () => {
    expect(html).toContain('不提供网络直播、音视频上传、论坛评论、新闻资讯或交易服务');
    expect(html).toContain('与哔哩哔哩官方无隶属或授权关系');
    expect(html).not.toMatch(/<form\b/i);
    expect(html).not.toMatch(/<script\b/i);
    expect(html).not.toMatch(/登录|注册/);
  });

  it('keeps the public source and release links', () => {
    expect(html).toContain('https://github.com/brainfk123/bilibili-live-gift-panel');
    expect(html).toContain('https://github.com/brainfk123/bilibili-live-gift-panel/releases/latest');
  });
});
```

- [ ] **Step 2: Run the targeted test and verify it fails**

Run:

```powershell
npx vitest run tests/website.test.ts
```

Expected: FAIL because the current page uses the old title, old brand, placeholder ICP number, and local-only copy.

- [ ] **Step 3: Update the page metadata and visible content**

In `website/index.html`:

- Change the document title to:

```html
<title>礼物互动工坊｜直播互动工具应用</title>
```

- Change the description to:

```html
<meta
  name="description"
  content="礼物互动工坊是面向 B 站直播场景的非营利开源互动工具，提供本地开源版，并筹备受邀网页版。"
/>
```

- Use `礼物互动工坊` as the header brand.
- Label the current release action `下载本地开源版` and retain the existing GitHub release URL.
- Add a non-clickable status element containing `受邀网页版 · 建设中`.
- Replace the feature cards with `礼物互动规则`、`OBS 数据展示`、`开源可验证`.
- Add these exact statements near the end of the main content:

```html
<p>本项目由个人维护，为非营利开源项目，与哔哩哔哩官方无隶属或授权关系。</p>
<p>本站不提供网络直播、音视频上传、论坛评论、新闻资讯或交易服务。</p>
```

- Replace the placeholder footer link with:

```html
<a href="https://beian.miit.gov.cn/" rel="nofollow">粤ICP备2026116328号</a>
```

- Keep the page free of forms and scripts.

- [ ] **Step 4: Run the targeted test and verify it passes**

Run:

```powershell
npx vitest run tests/website.test.ts
```

Expected: 3 tests pass.

- [ ] **Step 5: Run the full unit test suite**

Run:

```powershell
npm test
```

Expected: all Vitest tests pass.

- [ ] **Step 6: Commit the page contract and implementation**

```powershell
git add -- tests/website.test.ts website/index.html
git commit -m "feat: update ICP review landing page"
```

---

### Task 2: Lock the Nginx Hosting Contract

**Files:**
- Modify: `tests/website.test.ts`
- Modify: `website/nginx.conf.example`
- Modify: `website/README.md`

**Interfaces:**
- Consumes: Static page from Task 1 and the existing example Nginx configuration.
- Produces: A deployable Nginx virtual host for the three approved domains, `/healthz`, 404 handling, and security headers.

- [ ] **Step 1: Add a failing Nginx contract test**

Append to `tests/website.test.ts`:

```ts
const nginx = readFileSync(new URL('../website/nginx.conf.example', import.meta.url), 'utf8');

describe('public website nginx contract', () => {
  it('serves all approved domains and the health endpoint', () => {
    expect(nginx).toContain('server_name bilibililive.cn www.bilibililive.cn app.bilibililive.cn;');
    expect(nginx).toContain('root /var/www/gift-panel;');
    expect(nginx).toContain('location = /healthz');
    expect(nginx).toContain('return 200 "ok\\n";');
    expect(nginx).toContain('try_files $uri $uri/ =404;');
  });

  it('keeps the agreed security headers and no active application proxy', () => {
    expect(nginx).toContain('X-Content-Type-Options "nosniff"');
    expect(nginx).toContain('X-Frame-Options "DENY"');
    expect(nginx).toContain('Referrer-Policy "no-referrer"');
    expect(nginx).toContain('Permissions-Policy "camera=(), microphone=(), geolocation=()"');
    expect(nginx).not.toMatch(/^\s*proxy_pass\s+/m);
  });
});
```

- [ ] **Step 2: Run the targeted test and verify the new case fails**

Run:

```powershell
npx vitest run tests/website.test.ts
```

Expected: page tests pass and the domain assertion fails because the configuration still contains example domains.

- [ ] **Step 3: Update the Nginx virtual host**

In `website/nginx.conf.example`, set:

```nginx
server_name bilibililive.cn www.bilibililive.cn app.bilibililive.cn;
```

Keep the current static root, `/healthz`, 404 behavior, security headers, and commented future `/api/` example. Do not enable `proxy_pass`.

- [ ] **Step 4: Update deployment documentation**

In `website/README.md`:

- Remove instructions to replace the ICP number and example domains because the repository now contains the approved values.
- State that all three domains point to `124.220.60.152` during the review deployment.
- State that the page is static and does not enable the hosted application or MySQL.
- Keep the Nginx installation, `/healthz`, HTTPS, and Tencent Cloud terminal guidance.

- [ ] **Step 5: Run targeted and full tests**

Run:

```powershell
npx vitest run tests/website.test.ts
npm test
```

Expected: all tests pass.

- [ ] **Step 6: Commit the hosting contract**

```powershell
git add -- tests/website.test.ts website/nginx.conf.example website/README.md
git commit -m "chore: configure landing page domains"
```

---

### Task 3: Verify the Static Page Locally

**Files:**
- Modify: `docs/operations/2026-08-15-tencent-cloud-pilot-bootstrap-result.md`

**Interfaces:**
- Consumes: The final static page and Nginx configuration from Tasks 1-2.
- Produces: Local visual and contract evidence recorded in the existing pilot operation record.

- [ ] **Step 1: Start a local static server**

Run from the repository root:

```powershell
python -m http.server 4173 --directory website
```

Expected: the process listens on `127.0.0.1:4173` or all local interfaces without an error.

- [ ] **Step 2: Inspect desktop and mobile layouts**

Open `http://127.0.0.1:4173/` in the in-app browser. Verify at a desktop viewport and a mobile viewport that the title, hero, status, feature cards, legal statements, and footer are readable with no horizontal overflow.

- [ ] **Step 3: Check the page without executing third-party code**

Confirm in the browser that the page loads with no console errors, no form controls, no login control, and no requests other than the local HTML document and browser-internal resources.

- [ ] **Step 4: Record local verification**

Append a `Landing page local verification` section to `docs/operations/2026-08-15-tencent-cloud-pilot-bootstrap-result.md` containing the test command, pass count, desktop/mobile result, exact title, and confirmation that the page has no script or form.

- [ ] **Step 5: Commit the verification record**

```powershell
git add -- docs/operations/2026-08-15-tencent-cloud-pilot-bootstrap-result.md
git commit -m "docs: record landing page verification"
```

---

### Task 4: Deploy HTTP to the Shanghai Lighthouse Instance

**Files:**
- Create locally but do not commit: `.native/deploy/icp-review-site.tar.gz`
- Create remotely: `/var/www/gift-panel/index.html`
- Create remotely: `/etc/nginx/sites-available/gift-panel`
- Modify remotely: `/etc/nginx/sites-enabled/gift-panel`

**Interfaces:**
- Consumes: Verified `website/index.html`, `website/nginx.conf.example`, and the existing Tencent Cloud OrcaTerm session for `lhins-j4cqq4ao`.
- Produces: Public HTTP landing page and `GET /healthz` on `124.220.60.152` for all three domains.

- [ ] **Step 1: Build a local deployment archive**

Run:

```powershell
New-Item -ItemType Directory -Force '.native\deploy' | Out-Null
tar -czf '.native\deploy\icp-review-site.tar.gz' -C website index.html nginx.conf.example
Get-FileHash '.native\deploy\icp-review-site.tar.gz' -Algorithm SHA256
```

Expected: the archive exists and PowerShell prints a SHA-256 digest. Do not add the archive to Git.

- [ ] **Step 2: Reconfirm the target instance before mutation**

In Tencent Cloud, open OrcaTerm only from instance `bilibili-live-pilot` in Shanghai and run:

```bash
hostnamectl
ip -brief address
test "$(hostname)" != "OpenCode-fRh0"
```

Expected: Ubuntu 24.04 Shanghai pilot identity and no command failure. Stop immediately if the Beijing instance name or its known address appears.

- [ ] **Step 3: Upload and verify the deployment archive**

Upload `.native/deploy/icp-review-site.tar.gz` through OrcaTerm to `/tmp/icp-review-site.tar.gz`, then run:

```bash
sha256sum /tmp/icp-review-site.tar.gz
```

Expected: the digest exactly matches Step 1.

- [ ] **Step 4: Inspect the pre-deployment HTTP baseline**

Run:

```bash
sudo ss -ltnp
sudo systemctl is-active nginx || true
sudo test -e /etc/nginx/sites-available/gift-panel && sudo sed -n '1,220p' /etc/nginx/sites-available/gift-panel || true
```

Expected: the command records whether Nginx or a previous site exists without modifying it.

- [ ] **Step 5: Install Nginx and stage the files**

Run:

```bash
sudo apt-get update
sudo DEBIAN_FRONTEND=noninteractive apt-get install -y nginx
sudo install -d -m 0755 /tmp/icp-review-site /var/www/gift-panel
sudo tar -xzf /tmp/icp-review-site.tar.gz -C /tmp/icp-review-site
```

Expected: `nginx -v` succeeds and both staged files exist under `/tmp/icp-review-site`.

- [ ] **Step 6: Create recoverable backups and install the files**

Run:

```bash
stamp="$(date +%Y%m%d-%H%M%S)"
sudo test ! -e /var/www/gift-panel/index.html || sudo cp -a /var/www/gift-panel/index.html "/var/www/gift-panel/index.html.backup-$stamp"
sudo test ! -e /etc/nginx/sites-available/gift-panel || sudo cp -a /etc/nginx/sites-available/gift-panel "/etc/nginx/sites-available/gift-panel.backup-$stamp"
sudo install -o root -g root -m 0644 /tmp/icp-review-site/index.html /var/www/gift-panel/index.html
sudo install -o root -g root -m 0644 /tmp/icp-review-site/nginx.conf.example /etc/nginx/sites-available/gift-panel
sudo test -e /etc/nginx/sites-enabled/gift-panel || sudo ln -s /etc/nginx/sites-available/gift-panel /etc/nginx/sites-enabled/gift-panel
```

Expected: the live files are root-owned and the enabled site is a symlink to the installed configuration.

- [ ] **Step 7: Validate before reload**

Run:

```bash
sudo nginx -t
curl --silent --show-error --resolve bilibililive.cn:80:127.0.0.1 http://bilibililive.cn/ | grep -F '礼物互动工坊｜直播互动工具应用'
```

Expected: `nginx -t` succeeds. If the local curl cannot connect because Nginx is not running yet, continue only after the configuration test succeeds.

- [ ] **Step 8: Enable Nginx and verify local HTTP**

Run:

```bash
sudo systemctl enable --now nginx
sudo systemctl reload nginx
curl --fail --silent --show-error --resolve bilibililive.cn:80:127.0.0.1 http://bilibililive.cn/healthz
curl --fail --silent --show-error --resolve bilibililive.cn:80:127.0.0.1 http://bilibililive.cn/ | grep -F '粤ICP备2026116328号'
```

Expected: health check prints `ok` and the homepage contains the ICP number.

---

### Task 5: Enable HTTPS and Complete Public Verification

**Files:**
- Modify remotely: Nginx configuration generated by Certbot.
- Create: `docs/operations/2026-08-15-icp-review-landing-deployment-result.md`

**Interfaces:**
- Consumes: Working HTTP site from Task 4 and existing DNS A records for all three domains.
- Produces: HTTPS service when certificate issuance succeeds, plus a deployment and rollback record.

- [ ] **Step 1: Verify public HTTP from outside the server**

Open and inspect:

```text
http://bilibililive.cn/
http://www.bilibililive.cn/
http://app.bilibililive.cn/
http://bilibililive.cn/healthz
```

Expected: all three homepages show the approved page and `/healthz` returns `ok`.

- [ ] **Step 2: Add the Lighthouse HTTPS firewall rule**

In the Shanghai instance firewall, add inbound TCP `443` from `0.0.0.0/0` with remark `Web服务HTTPS`. Do not add port `3306` or an application port.

- [ ] **Step 3: Install Certbot and request one certificate**

Run in OrcaTerm:

```bash
sudo apt-get install -y certbot python3-certbot-nginx
sudo certbot --nginx -d bilibililive.cn -d www.bilibililive.cn -d app.bilibililive.cn
```

The user enters the certificate contact email and accepts the Let's Encrypt terms in OrcaTerm. Select HTTP-to-HTTPS redirection when prompted.

Expected: Certbot reports a successfully installed certificate. If issuance fails, keep the already verified HTTP service, capture the exact failure, and do not make unrelated DNS or proxy changes.

- [ ] **Step 4: Verify HTTPS and redirect behavior when the certificate succeeds**

Run:

```bash
curl --fail --silent --show-error https://bilibililive.cn/healthz
curl --head --silent --show-error http://bilibililive.cn/
sudo certbot renew --dry-run
```

Expected: HTTPS health check prints `ok`, HTTP returns a redirect to HTTPS, and renewal dry-run succeeds.

- [ ] **Step 5: Verify public content and listener boundaries**

From outside the server, inspect all three HTTPS URLs. On the server run:

```bash
sudo ss -ltnp
sudo nginx -t
```

Expected: the site is reachable on 80/443, SSH remains available on 22, and no public listener exists on 3306 or an application port.

- [ ] **Step 6: Write the deployment result record**

Create `docs/operations/2026-08-15-icp-review-landing-deployment-result.md` containing:

- Shanghai instance identity.
- Deployed file paths and SHA-256 digest.
- Nginx configuration test result.
- Public HTTP checks for all three domains.
- HTTPS certificate and redirect result, or the exact recorded reason HTTP remains active.
- Listener inspection result.
- Backup filenames created in Task 4 and the rollback command required to restore them.
- Confirmation that no database credentials, Bilibili cookies, invitation codes, or personal data were recorded.

- [ ] **Step 7: Commit the deployment record**

```powershell
git add -- docs/operations/2026-08-15-icp-review-landing-deployment-result.md
git commit -m "docs: record ICP landing page deployment"
```

---

## Final Verification

Run locally:

```powershell
npx vitest run tests/website.test.ts
npm test
git diff --check
git status --short
```

Verify publicly:

```text
https://bilibililive.cn/
https://www.bilibililive.cn/
https://app.bilibililive.cn/
https://bilibililive.cn/healthz
```

Expected: tests pass; the public pages show the approved brand,备案-consistent title and copy, correct ICP link, no data-collection controls, and the health endpoint returns `ok`.
