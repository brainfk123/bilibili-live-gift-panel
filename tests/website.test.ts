import { readFileSync } from 'node:fs';
import { describe, expect, it } from 'vitest';

const html = readFileSync(new URL('../website/index.html', import.meta.url), 'utf8');
const nginx = readFileSync(new URL('../website/nginx.conf.example', import.meta.url), 'utf8');

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
