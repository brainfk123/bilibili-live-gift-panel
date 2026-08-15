import { readFileSync } from 'node:fs';
import { describe, expect, it } from 'vitest';

const html = readFileSync(new URL('../website/index.html', import.meta.url), 'utf8');
const nginx = readFileSync(new URL('../website/nginx.conf.example', import.meta.url), 'utf8');
const websiteReadme = readFileSync(new URL('../website/README.md', import.meta.url), 'utf8');

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

  it('documents non-destructive site activation', () => {
    expect(websiteReadme).not.toContain('sudo rm /etc/nginx/sites-enabled/default');
  });
});
