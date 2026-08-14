import { readFileSync } from 'node:fs';
import { describe, expect, it } from 'vitest';

function deploymentAsset(name: string): string {
  return readFileSync(new URL(`../deploy/update-api/${name}`, import.meta.url), 'utf8');
}

describe('update API deployment assets', () => {
  it('keeps the public page minimal and unindexed', () => {
    const index = deploymentAsset('index.html.template');

    expect(index).toContain('name="robots" content="noindex,nofollow"');
    expect(index).toContain('https://beian.miit.gov.cn/');
    expect(index).toContain('${ICP_NUMBER}');
  });

  it('limits the public proxy surface and returns hardened errors', () => {
    const nginx = deploymentAsset('nginx.conf.template');

    expect(nginx).toContain('rate=10r/m');
    expect(nginx).toContain('burst=20');
    expect(nginx).toMatch(/location = \/api\/v1\/releases\/latest/);
    expect(nginx).toMatch(/location = \/api\/v1\/changelog/);
    expect(nginx).toMatch(/location = \/healthz[\s\S]*allow 127\.0\.0\.1;[\s\S]*deny all;/);
    expect(nginx).toContain('error_page 429 = @rate_limited');
    expect(nginx).toContain('Content-Type application/json');
    expect(nginx).toContain('X-Frame-Options DENY');
    expect(nginx).toContain('Content-Security-Policy');
    expect(nginx).toMatch(/location = \/\s*\{[\s\S]*try_files \/index\.html =404;/);
    expect(nginx).not.toContain('$http_referer');
    expect(nginx).toMatch(/location @rate_limited \{[\s\S]*X-Frame-Options DENY/);
  });

  it('runs the API under a hardened service account and retains logs for one week', () => {
    const service = deploymentAsset('gift-panel-update-api.service');
    const logrotate = deploymentAsset('logrotate.conf');

    expect(service).toContain('User=gift-panel-update');
    expect(service).toContain('NoNewPrivileges=true');
    expect(service).toContain('ProtectSystem=strict');
    expect(logrotate).toContain('rotate 7');
    expect(logrotate).toContain('daily');
  });
});
