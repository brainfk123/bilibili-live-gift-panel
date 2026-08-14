import { spawnSync } from 'node:child_process';
import { cpSync, existsSync, mkdirSync, mkdtempSync, readFileSync, rmSync } from 'node:fs';
import { join } from 'node:path';
import { tmpdir } from 'node:os';
import { describe, expect, it } from 'vitest';

function deploymentAsset(name: string): string {
  return readFileSync(new URL(`../deploy/update-api/${name}`, import.meta.url), 'utf8');
}

function nginxAccessLogFormat(nginx: string): string {
  const format = nginx.match(/log_format gift_panel_update_api_safe ([\s\S]*?);/);
  expect(format, 'missing dedicated update API access log format').not.toBeNull();
  return format![0];
}

describe('update API deployment assets', () => {
  it('builds the Linux binary when a clean checkout has no dist directory', () => {
    const temporaryRoot = mkdtempSync(join(tmpdir(), 'gift-panel-update-api-build-'));
    try {
      mkdirSync(join(temporaryRoot, 'scripts'));
      cpSync(new URL('../scripts/build-update-api.mjs', import.meta.url), join(temporaryRoot, 'scripts', 'build-update-api.mjs'));
      cpSync(new URL('../updateapi', import.meta.url), join(temporaryRoot, 'updateapi'), { recursive: true });

      const result = spawnSync(process.execPath, [join(temporaryRoot, 'scripts', 'build-update-api.mjs')], {
        encoding: 'utf8',
        timeout: 120_000,
      });

      expect(result.status, result.stderr).toBe(0);
      expect(existsSync(join(temporaryRoot, 'dist', 'gift-panel-update-api-linux-amd64'))).toBe(true);
      expect(result.stdout).toMatch(/gift-panel-update-api-linux-amd64 \d+/);
      expect(readFileSync(new URL('../scripts/build-update-api.mjs', import.meta.url), 'utf8'))
        .toMatch(/mkdirSync\(join\(root, 'dist'\), \{ recursive: true \}\);/);
    } finally {
      rmSync(temporaryRoot, { recursive: true, force: true });
    }
  });

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
    expect(nginx).toContain('default_type application/json;');
    expect(nginx).not.toContain('add_header Content-Type');
    expect(nginx).toContain('X-Frame-Options DENY');
    expect(nginx).toContain('Content-Security-Policy');
    expect(nginx).toContain("style-src 'unsafe-inline'");
    expect(nginx).toMatch(/location = \/\s*\{[\s\S]*try_files \/index\.html =404;/);
    expect(nginx).toMatch(/location @rate_limited \{[\s\S]*X-Frame-Options DENY/);

    const accessLogFormat = nginxAccessLogFormat(nginx);
    expect(accessLogFormat).toContain('$uri');
    expect(accessLogFormat).not.toMatch(/\$request(?:[\s'";]|$)/);
    expect(accessLogFormat).not.toContain('$request_uri');
    expect(accessLogFormat).not.toContain('$args');

    expect(nginx.match(/\$request_method !~ \^\(GET\|HEAD\)\$/g)).toHaveLength(2);
    expect(nginx.match(/add_header Allow "GET, HEAD" always;/g)).toHaveLength(2);
    expect(nginx.match(/return 405 '\{"code":"method_not_allowed","message":"Method not allowed"\}';/g)).toHaveLength(2);
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

  it('documents the private COS gate and direct loopback health check', () => {
    const readme = deploymentAsset('README.md');
    const environment = deploymentAsset('gift-panel-update-api.env.example');
    const service = deploymentAsset('gift-panel-update-api.service');

    expect(readme).toContain('bucket private');
    expect(readme).toContain('COS Versioning state must be `Disabled`');
    expect(readme).toContain('previously enabled or suspended');
    expect(readme).toContain('new never-versioned production bucket');
    expect(readme).toContain('redesigned immutable mechanism');
    expect(readme).not.toContain('Versioning must not be Enabled');
    expect(readme).not.toContain('If versioning is Suspended, run controlled staging verification before production');
    expect(readme).toContain("curl --fail --silent --show-error http://127.0.0.1:12450/healthz | grep -Fx 'ok'");
    expect(readme).not.toContain('http://127.0.0.1/healthz');
    expect(environment).not.toContain('UPDATE_API_LISTEN=');
    expect(service).toContain('Environment=UPDATE_API_LISTEN=127.0.0.1:12450');
  });

  it('documents protected-environment ownership and rotation of the publisher tool pin', () => {
    const readme = deploymentAsset('README.md');

    expect(readme).toContain('UPDATE_PUBLISHER_TOOL_SHA');
    expect(readme).toContain('exact 40-hex commit SHA');
    expect(readme).toContain('protected GitHub Environment `release`');
    expect(readme).toContain('review the candidate commit');
    expect(readme).toContain('update the environment variable');
    expect(readme).toMatch(/rerun the Release workflow/i);
    expect(readme).toContain('never expose the pin as a workflow-dispatch input');
  });
});
