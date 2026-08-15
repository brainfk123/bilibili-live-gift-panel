# Personal ICP Homepage Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the public-facing product landing page with a minimal personal-use record page and deploy the verified file to the existing Tencent Cloud Lighthouse Nginx site.

**Architecture:** Keep the existing static Nginx deployment and replace only `website/index.html`. Protect the intended filing copy with a Vitest contract, then upload the verified file after making an explicit server-side backup; do not change DNS, Nginx, HTTPS, firewall, or health-check configuration.

**Tech Stack:** Static HTML/CSS, Vitest 2, Nginx on Ubuntu 24.04, Tencent Cloud OrcaTerm, HTTPS via the existing Certbot certificate.

## Global Constraints

- The page must express only personal development, personal records, learning, and personal use.
- The page must not expose download, source-code, update, invitation, web-version, login, registration, form, script, or online-service entry points.
- The footer must show `粤ICP备2026116328号` and link it to `https://beian.miit.gov.cn/`.
- Preserve the existing dark visual tone in a single-screen centered layout.
- Do not modify the three domains, Nginx configuration, HTTPS certificate, firewall, or `/healthz` behavior.
- Preserve unrelated and untracked workspace changes.

---

### Task 1: Personal-use page contract and static page

**Files:**
- Modify: `tests/website.test.ts`
- Modify: `website/index.html`

**Interfaces:**
- Consumes: the existing `website/index.html` static entry point and Vitest file-content contract.
- Produces: one script-free HTML page whose only hyperlink target is `https://beian.miit.gov.cn/`.

- [ ] **Step 1: Replace the public landing-page assertions with a failing personal-page contract**

In `tests/website.test.ts`, replace the three tests inside `describe('public website contract', ...)` with:

```ts
describe('personal ICP homepage contract', () => {
  it('presents only the approved personal-use copy', () => {
    expect(html).toContain('<title>礼物互动工坊｜个人项目记录</title>');
    expect(html).toContain('这是我的个人项目记录页面，用于记录本人开发和自用的直播互动工具。');
    expect(html).toContain('本网站仅供本人学习、记录和个人使用。');
    expect(html).toContain('粤ICP备2026116328号');
  });

  it('keeps the ICP link as the only page destination', () => {
    const hrefs = [...html.matchAll(/href="([^"]+)"/g)].map((match) => match[1]);
    expect(hrefs).toEqual(['https://beian.miit.gov.cn/']);
  });

  it('does not advertise or expose public-facing capabilities', () => {
    expect(html).not.toMatch(/下载|更新日志|GitHub|源代码|源码|受邀|网页版|建设中|服务|注册|登录|企业|团体|论坛|经营|销售|交易/);
    expect(html).not.toMatch(/<form\b/i);
    expect(html).not.toMatch(/<script\b/i);
  });
});
```

- [ ] **Step 2: Run the focused test and verify RED**

Run:

```powershell
npx vitest run tests/website.test.ts
```

Expected: FAIL because the current page still has the old title, public download/source links, web-version wording, and multiple hyperlink destinations.

- [ ] **Step 3: Replace `website/index.html` with the minimal static implementation**

Use this complete HTML document:

```html
<!doctype html>
<html lang="zh-CN">
  <head>
    <meta charset="utf-8" />
    <meta name="viewport" content="width=device-width, initial-scale=1" />
    <meta name="description" content="礼物互动工坊是我的个人项目记录页面，仅用于本人学习、记录和个人使用。" />
    <meta name="color-scheme" content="dark" />
    <title>礼物互动工坊｜个人项目记录</title>
    <style>
      :root { color-scheme: dark; font-family: Inter, ui-sans-serif, system-ui, -apple-system, BlinkMacSystemFont, "Segoe UI", "Microsoft YaHei", sans-serif; color: #f8fafc; background: #090b12; }
      * { box-sizing: border-box; }
      body { min-width: 320px; min-height: 100vh; margin: 0; background: radial-gradient(circle at 20% 15%, rgba(251, 114, 153, .18), transparent 30rem), radial-gradient(circle at 80% 20%, rgba(255, 173, 102, .12), transparent 28rem), #090b12; }
      .page { width: min(760px, calc(100% - 40px)); min-height: 100vh; margin: 0 auto; display: flex; flex-direction: column; }
      main { flex: 1; display: grid; place-items: center; padding: 72px 0; }
      .card { width: 100%; padding: clamp(32px, 7vw, 64px); border: 1px solid rgba(255,255,255,.1); border-radius: 28px; background: linear-gradient(150deg, rgba(26,30,43,.94), rgba(12,14,22,.92)); box-shadow: 0 32px 90px rgba(0,0,0,.3); }
      .eyebrow { display: inline-flex; padding: 7px 11px; border: 1px solid rgba(251,114,153,.35); border-radius: 999px; color: #ffb0c7; background: rgba(251,114,153,.08); font-size: 13px; }
      h1 { margin: 24px 0 20px; font-size: clamp(42px, 9vw, 72px); line-height: 1.05; letter-spacing: -.05em; color: transparent; background: linear-gradient(105deg, #fff 5%, #fb7299 55%, #ffad66 100%); background-clip: text; -webkit-background-clip: text; }
      p { margin: 0; color: #aeb6c7; font-size: clamp(16px, 2.6vw, 19px); line-height: 1.8; }
      p + p { margin-top: 12px; }
      footer { padding: 24px 0 32px; border-top: 1px solid rgba(255,255,255,.08); color: #747f93; font-size: 13px; text-align: center; }
      footer a { color: inherit; text-decoration: none; }
      footer a:hover { color: #fff; }
    </style>
  </head>
  <body>
    <div class="page">
      <main>
        <section class="card" aria-labelledby="page-title">
          <span class="eyebrow">个人项目记录</span>
          <h1 id="page-title">礼物互动工坊</h1>
          <p>这是我的个人项目记录页面，用于记录本人开发和自用的直播互动工具。</p>
          <p>本网站仅供本人学习、记录和个人使用。</p>
        </section>
      </main>
      <footer>
        <a href="https://beian.miit.gov.cn/" rel="nofollow">粤ICP备2026116328号</a>
      </footer>
    </div>
  </body>
</html>
```

- [ ] **Step 4: Run the focused test and verify GREEN**

Run:

```powershell
npx vitest run tests/website.test.ts
```

Expected: `6 passed`, `0 failed`.

- [ ] **Step 5: Run the full regression and diff checks**

Run:

```powershell
npm test
git diff --check -- tests/website.test.ts website/index.html
```

Expected: all Vitest files pass and `git diff --check` exits `0` with no output.

- [ ] **Step 6: Commit only the page and its contract**

```powershell
git add -- tests/website.test.ts website/index.html
git diff --cached --name-only
git commit -m "fix: make homepage personal-use only"
```

Expected staged paths: only `tests/website.test.ts` and `website/index.html`.

### Task 2: Safe Lighthouse replacement and public verification

**Files:**
- Deploy local: `website/index.html`
- Replace remote: `/var/www/gift-panel/index.html`
- Preserve remote backup: `/var/www/gift-panel/index.html.pre-personal-20260815`

**Interfaces:**
- Consumes: the tested `website/index.html` from Task 1 and the existing OrcaTerm session for Lighthouse instance `lhins-j4cqq4ao` in `ap-shanghai`.
- Produces: the same three HTTPS domain routes serving identical personal-use HTML while `/healthz` remains `ok`.

- [ ] **Step 1: Record the local artifact digest**

Run:

```powershell
Get-FileHash -Algorithm SHA256 website/index.html
```

Record the exact digest from the command output for comparison on the server.

- [ ] **Step 2: Upload without replacing the live file**

Use OrcaTerm file upload to place the tested local file at:

```text
/tmp/personal-icp-index.html
```

- [ ] **Step 3: Verify the uploaded file and create an explicit rollback copy**

Run on the Shanghai server:

```bash
sha256sum /tmp/personal-icp-index.html
sudo test -f /var/www/gift-panel/index.html
sudo test ! -e /var/www/gift-panel/index.html.pre-personal-20260815
sudo cp --preserve=mode,ownership,timestamps /var/www/gift-panel/index.html /var/www/gift-panel/index.html.pre-personal-20260815
```

Expected: the server digest matches Step 1; both `test` commands exit `0`; the backup command produces no error.

- [ ] **Step 4: Install only the page and validate Nginx**

Run on the Shanghai server:

```bash
sudo install -m 0644 /tmp/personal-icp-index.html /var/www/gift-panel/index.html
sudo nginx -t
```

Expected: Nginx reports syntax successful and test successful. No Nginx reload is required for a static-file-only replacement.

- [ ] **Step 5: Verify local server behavior before public acceptance**

Run on the Shanghai server:

```bash
curl -fsS https://bilibililive.cn/ | grep -F '本网站仅供本人学习、记录和个人使用。'
curl -fsS https://www.bilibililive.cn/ | grep -F '粤ICP备2026116328号'
curl -fsS https://app.bilibililive.cn/ | grep -F '个人项目记录'
curl -fsS https://bilibililive.cn/healthz
```

Expected: the first three commands print their matching personal-page text and the health check prints `ok`.

- [ ] **Step 6: Verify the public page and prohibited-copy absence from the local workstation**

Run:

```powershell
$domains = 'bilibililive.cn','www.bilibililive.cn','app.bilibililive.cn'
foreach ($domain in $domains) {
  $body = (Invoke-WebRequest -UseBasicParsing "https://$domain/").Content
  if ($body -notmatch '本网站仅供本人学习、记录和个人使用。') { throw "personal copy missing: $domain" }
  if ($body -match '下载|更新日志|GitHub|源代码|源码|受邀|网页版|建设中|服务|注册|登录|企业|团体|论坛|经营|销售|交易') { throw "prohibited copy found: $domain" }
}
(Invoke-WebRequest -UseBasicParsing 'https://bilibililive.cn/healthz').Content
```

Expected: no exception and final output `ok`.

- [ ] **Step 7: Visually inspect desktop and mobile layouts**

Open `https://bilibililive.cn/` at desktop width and a mobile viewport near `390 × 844`. Confirm there is no horizontal overflow, the two personal-use sentences are fully visible, no extra link or button appears, and the ICP link remains visible at the bottom.

- [ ] **Step 8: Keep the rollback boundary explicit**

If any acceptance check fails, restore only the prior page:

```bash
sudo cp --preserve=mode,ownership,timestamps /var/www/gift-panel/index.html.pre-personal-20260815 /var/www/gift-panel/index.html
```

Do not change Nginx, DNS, HTTPS, firewall, or any other server state while resolving a page-content failure.
