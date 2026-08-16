import { execFileSync, spawnSync, type SpawnSyncReturns } from 'node:child_process';
import { appendFileSync, cpSync, existsSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs';
import { join } from 'node:path';
import { tmpdir } from 'node:os';
import { describe, expect, it } from 'vitest';

function deploymentAsset(name: string): string {
  return readFileSync(new URL(`../deploy/update-api/${name}`, import.meta.url), 'utf8');
}

type Unit = Map<string, Map<string, string[]>>;

function parseUnit(contents: string): Unit {
  const sections: Unit = new Map();
  let section: Map<string, string[]> | undefined;
  for (const rawLine of contents.replaceAll('\r\n', '\n').split('\n')) {
    const line = rawLine.trim();
    if (!line || line.startsWith('#') || line.startsWith(';')) continue;
    const header = line.match(/^\[([^\]]+)\]$/);
    if (header) {
      section = new Map();
      sections.set(header[1], section);
      continue;
    }
    const separator = line.indexOf('=');
    if (separator > 0 && section) {
      const key = line.slice(0, separator);
      section.set(key, [...(section.get(key) ?? []), line.slice(separator + 1)]);
    }
  }
  return sections;
}

function sectionValue(unit: Unit, section: string, key: string): string | undefined {
  return unit.get(section)?.get(key)?.at(-1);
}

function sectionValues(unit: Unit, section: string, key: string): string[] {
  return unit.get(section)?.get(key) ?? [];
}

function build(root: string, environment: NodeJS.ProcessEnv = {}): SpawnSyncReturns<string> {
  return spawnSync(process.execPath, [join(root, 'scripts', 'build-update-api.mjs')], {
    cwd: root,
    encoding: 'utf8',
    timeout: 120_000,
    env: { ...process.env, ...environment },
  });
}

function goMetadata(binary: string): string {
  return execFileSync('go', ['version', '-m', binary], { encoding: 'utf8' });
}

function copyUpdateAPI(destination: string): void {
  cpSync(new URL('../updateapi', import.meta.url), destination, {
    recursive: true,
    filter: (source) => !source.includes('.test-gocache'),
  });
}

function reviewedBuildRoot(prefix: string): string {
  const root = mkdtempSync(join(tmpdir(), prefix));
  mkdirSync(join(root, 'scripts'));
  cpSync(new URL('../scripts/build-update-api.mjs', import.meta.url), join(root, 'scripts', 'build-update-api.mjs'));
  copyUpdateAPI(join(root, 'updateapi'));
  execFileSync('git', ['init', '--quiet'], { cwd: root });
  execFileSync('git', ['config', 'user.email', 'test@example.invalid'], { cwd: root });
  execFileSync('git', ['config', 'user.name', 'Deployment test'], { cwd: root });
  execFileSync('git', ['add', 'scripts', 'updateapi'], { cwd: root });
  execFileSync('git', ['commit', '--quiet', '-m', 'reviewed build input'], { cwd: root });
  return root;
}

function nginxAccessLogFormat(nginx: string): string {
  const format = nginx.match(/log_format gift_panel_update_api_safe ([\s\S]*?);/);
  expect(format, 'missing dedicated update API access log format').not.toBeNull();
  return format![0];
}

describe('update API deployment assets', () => {
  it('builds both Linux amd64 binaries from a reviewed clean commit', () => {
    const temporaryRoot = reviewedBuildRoot('gift-panel-update-api-build-');
    try {
      const reviewedCommit = execFileSync('git', ['rev-parse', 'HEAD'], { cwd: temporaryRoot, encoding: 'utf8' }).trim();
      const result = build(temporaryRoot);

      expect(result.status, result.stderr).toBe(0);
      for (const name of ['gift-panel-update-api-linux-amd64', 'gift-panel-release-mirror-linux-amd64']) {
        const binary = join(temporaryRoot, 'dist', name);
        expect(existsSync(binary), `${name} is missing`).toBe(true);
        expect(goMetadata(binary)).toMatch(/\bmod\s+github.com\/brainfk123\/bilibili-live-gift-panel\/updateapi\b[\s\S]*\bGOARCH=amd64\b[\s\S]*\bGOOS=linux\b/);
      }
      const mirror = join(temporaryRoot, 'dist', 'gift-panel-release-mirror-linux-amd64');
      expect(goMetadata(join(temporaryRoot, 'dist', 'gift-panel-update-api-linux-amd64'))).toContain('path\tgithub.com/brainfk123/bilibili-live-gift-panel/updateapi/cmd/server');
      expect(goMetadata(mirror)).toContain('path\tgithub.com/brainfk123/bilibili-live-gift-panel/updateapi/cmd/mirror');
      expect(goMetadata(mirror)).toContain('dep\tgithub.com/tencentyun/cos-go-sdk-v5\tv0.7.75');
      expect(readFileSync(mirror).includes(Buffer.from(reviewedCommit))).toBe(true);
    } finally {
      rmSync(temporaryRoot, { recursive: true, force: true });
    }
  });

  it('refuses deployment builds without a clean reviewed identity but permits explicit local builds', () => {
    const temporaryRoot = mkdtempSync(join(tmpdir(), 'gift-panel-update-api-local-build-'));
    try {
      mkdirSync(join(temporaryRoot, 'scripts'));
      cpSync(new URL('../scripts/build-update-api.mjs', import.meta.url), join(temporaryRoot, 'scripts', 'build-update-api.mjs'));
      copyUpdateAPI(join(temporaryRoot, 'updateapi'));

      const production = build(temporaryRoot);
      expect(production.status).not.toBe(0);
      expect(`${production.stdout}\n${production.stderr}`).toMatch(/reviewed.*commit|identity|git/i);

      const local = build(temporaryRoot, { GIFT_PANEL_LOCAL_BUILD: '1' });
      expect(local.status, local.stderr).toBe(0);
      expect(existsSync(join(temporaryRoot, 'dist', 'gift-panel-release-mirror-linux-amd64'))).toBe(false);
      expect(existsSync(join(temporaryRoot, 'dist', 'local', 'gift-panel-release-mirror-linux-amd64'))).toBe(true);
    } finally {
      rmSync(temporaryRoot, { recursive: true, force: true });
    }
  });

  it('rejects tracked source changes and excludes untracked Go files from a deployment artifact', () => {
    const temporaryRoot = reviewedBuildRoot('gift-panel-update-api-snapshot-');
    try {
      appendFileSync(join(temporaryRoot, 'updateapi', 'cmd', 'mirror', 'untracked.go'), '\npackage main\nfunc init() { buildCommit = "UNTRACKED-MIRROR-CODE" }\n');
      const snapshot = build(temporaryRoot);
      expect(snapshot.status, snapshot.stderr).toBe(0);
      expect(readFileSync(join(temporaryRoot, 'dist', 'gift-panel-release-mirror-linux-amd64')).includes(Buffer.from('UNTRACKED-MIRROR-CODE'))).toBe(false);

      appendFileSync(join(temporaryRoot, 'updateapi', 'cmd', 'mirror', 'main.go'), '\n// tracked mutation\n');
      const dirty = build(temporaryRoot);
      expect(dirty.status).not.toBe(0);
      expect(`${dirty.stdout}\n${dirty.stderr}`).toMatch(/clean reviewed Git commit/i);
    } finally {
      rmSync(temporaryRoot, { recursive: true, force: true });
    }
  });

  it('fails deployment publication when HEAD changes after its snapshot is taken', () => {
    const temporaryRoot = reviewedBuildRoot('gift-panel-update-api-race-');
    try {
      const mutation = join(temporaryRoot, 'updateapi', 'cmd', 'mirror', 'main.go');
      const result = build(temporaryRoot, { GIFT_PANEL_BUILD_TEST_MUTATE_TRACKED_PATH: mutation });
      expect(result.status).not.toBe(0);
      expect(`${result.stdout}\n${result.stderr}`).toMatch(/changed during build|clean reviewed Git commit/i);
    } finally {
      rmSync(temporaryRoot, { recursive: true, force: true });
    }
  });

  it('archives a reviewed checkout larger than Node default command buffering', () => {
    const temporaryRoot = reviewedBuildRoot('gift-panel-update-api-large-archive-');
    try {
      writeFileSync(join(temporaryRoot, 'updateapi', 'build-fixture.bin'), Buffer.alloc(1024 * 1024 + 1, 7));
      execFileSync('git', ['add', 'updateapi/build-fixture.bin'], { cwd: temporaryRoot });
      execFileSync('git', ['commit', '--quiet', '-m', 'large reviewed input'], { cwd: temporaryRoot });
      const result = build(temporaryRoot);
      expect(result.status, result.stderr).toBe(0);
      expect(existsSync(join(temporaryRoot, 'dist', 'gift-panel-release-mirror-linux-amd64'))).toBe(true);
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
    expect(nginx.match(/client_max_body_size 16k;/g)).toHaveLength(2);
    expect(nginx).toMatch(/location = \/api\/v1\/releases\/latest/);
    expect(nginx).toMatch(/location = \/api\/v1\/changelog/);
    expect(nginx).toMatch(/location = \/healthz[\s\S]*allow 127\.0\.0\.1;[\s\S]*allow ::1;[\s\S]*deny all;/);
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
    expect(nginx.match(/add_header X-Request-ID \$request_id always;/g)).toHaveLength(4);
    expect(nginx.match(/return 405 '\{"code":"method_not_allowed","message":"Method not allowed","request_id":"\$request_id"\}';/g)).toHaveLength(2);
    expect(nginx).toContain(`return 429 '{"code":"rate_limited","message":"Too many requests","request_id":"$request_id"}';`);
    expect(nginx).toContain(`return 404 '{"code":"not_found","message":"Not found","request_id":"$request_id"}';`);
    expect(nginx).toContain('return 301 https://${PUBLIC_DOMAIN}$request_uri;');
    expect(nginx).not.toContain('return 301 https://$host$request_uri;');

    const notFoundLocation = nginx.slice(nginx.lastIndexOf('location / {'));
    for (const header of ['X-Content-Type-Options', 'Referrer-Policy', 'X-Frame-Options', 'Permissions-Policy', 'Content-Security-Policy']) {
      expect(notFoundLocation).toContain(`add_header ${header}`);
    }
  });

  it('runs the API under a hardened service account and retains logs for one week', () => {
    const service = deploymentAsset('gift-panel-update-api.service');
    const logrotate = deploymentAsset('logrotate.conf');
    const journal = deploymentAsset('journald.conf');

    expect(service).toContain('User=gift-panel-update');
    expect(service).toContain('NoNewPrivileges=true');
    expect(service).toContain('ProtectSystem=strict');
    expect(service).toContain('LogNamespace=gift-panel-update-api');
    expect(logrotate).toContain('rotate 7');
    expect(logrotate).toContain('daily');
    expect(journal).toContain('SystemMaxUse=64M');
    expect(journal).toContain('RuntimeMaxUse=64M');
    expect(journal).toContain('MaxRetentionSec=7day');
  });

  it('runs the release mirror as a separate hardened oneshot without a socket', () => {
    const service = parseUnit(deploymentAsset('gift-panel-release-mirror.service'));
    const journal = parseUnit(deploymentAsset('journald.conf'));

    expect(sectionValue(service, 'Service', 'Type')).toBe('oneshot');
    expect(sectionValue(service, 'Service', 'User')).toBe('gift-panel-mirror');
    expect(sectionValue(service, 'Service', 'Group')).toBe('gift-panel-mirror');
    expect(sectionValue(service, 'Service', 'EnvironmentFile')).toBe('/etc/gift-panel-release-mirror.env');
    expect(sectionValue(service, 'Service', 'ExecStart')).toBe('/opt/gift-panel-release-mirror/current/gift-panel-release-mirror');
    expect(sectionValue(service, 'Service', 'StateDirectory')).toBe('gift-panel-release-mirror');
    expect(sectionValue(service, 'Service', 'StateDirectoryMode')).toBe('0700');
    for (const key of ['NoNewPrivileges', 'PrivateTmp', 'ProtectSystem', 'ProtectHome']) {
      expect(sectionValue(service, 'Service', key), key).toBe(key === 'ProtectSystem' ? 'strict' : 'true');
    }
    expect(sectionValue(service, 'Service', 'CapabilityBoundingSet')).toBe('');
    expect(sectionValue(service, 'Service', 'RestrictAddressFamilies')).toBe('AF_UNIX AF_INET AF_INET6');
    expect(sectionValue(service, 'Service', 'LogNamespace')).toBe('gift-panel-release-mirror');
    expect(sectionValues(service, 'Service', 'ExecStart')).toEqual(['/opt/gift-panel-release-mirror/current/gift-panel-release-mirror']);
    expect(sectionValue(service, 'Socket', 'ListenStream')).toBeUndefined();
    expect(sectionValue(service, 'Service', 'ListenStream')).toBeUndefined();
    expect(sectionValue(journal, 'Journal', 'MaxRetentionSec')).toBe('7day');
  });

  it('schedules the mirror after boot and then after each completed invocation', () => {
    const timer = parseUnit(deploymentAsset('gift-panel-release-mirror.timer'));

    expect(sectionValue(timer, 'Timer', 'OnBootSec')).toBe('1min');
    expect(sectionValue(timer, 'Timer', 'OnUnitInactiveSec')).toBe('5min');
    expect(sectionValue(timer, 'Timer', 'Persistent')).toBe('true');
    expect(sectionValue(timer, 'Timer', 'Unit')).toBe('gift-panel-release-mirror.service');
    expect(sectionValue(timer, 'Install', 'WantedBy')).toBe('timers.target');
  });

  it('keeps mirror credentials separate and documents validation before timer enablement', () => {
    const environment = deploymentAsset('gift-panel-release-mirror.env.example');
    const readme = deploymentAsset('README.md');
    const variables = environment.split(/\r?\n/).filter((line) => line && !line.startsWith('#')).map((line) => line.split('=', 2));

    expect(variables).toEqual([['COS_BUCKET', ''], ['COS_REGION', ''], ['COS_SECRET_ID', ''], ['COS_SECRET_KEY', '']]);
    expect(readme).toContain('useradd --system --user-group --home-dir /nonexistent --shell /usr/sbin/nologin gift-panel-mirror');
    expect(readme).toContain('install -o root -g root -m 0600 /secure/gift-panel-release-mirror.env /etc/gift-panel-release-mirror.env');
    expect(readme).toContain('sha256sum -c');
    expect(readme).toContain('RELEASE_ID="${REVIEWED_COMMIT:?set the reviewed 40-hex commit}"');
    expect(readme).toContain('test "$RELEASE_ID" = "$(dist/gift-panel-release-mirror-linux-amd64 --build-commit)"');
    expect(readme).toContain('sha256sum -c -');
    expect(readme).toContain('readlink -f /opt/gift-panel-release-mirror/current/gift-panel-release-mirror');
    expect(readme).toContain('gift-panel-release-mirror.service.d/dry-run.conf');
    expect(readme).toContain('ExecStart=/opt/gift-panel-release-mirror/current/gift-panel-release-mirror --dry-run');
    expect(readme).toContain('systemctl start gift-panel-release-mirror.service');
    expect(readme.indexOf('systemctl enable --now gift-panel-release-mirror.timer')).toBeGreaterThan(readme.indexOf('gift-panel-release-mirror --dry-run'));
    expect(readme).toContain('ln -sfn /opt/gift-panel-release-mirror/releases/"$RELEASE_ID" /opt/gift-panel-release-mirror/current');
    expect(readme).toContain('Head/Get/Put');
    expect(readme).toContain('no Delete, list, bucket configuration, or other prefixes');
    expect(readme).toContain('systemctl stop gift-panel-release-mirror.service');
    expect(readme).toContain('systemctl is-active --quiet gift-panel-release-mirror.service');
    expect(readme).toContain('channels/stable/latest.json');
    expect(readme).toContain('state.json');
    expect(readme).toContain('Do not delete immutable release objects');
    expect(readme.indexOf('systemctl disable --now gift-panel-release-mirror.timer')).toBeLessThan(readme.indexOf('ln -sfn /opt/gift-panel-release-mirror/releases/PREVIOUS_RELEASE_ID'));
    expect(readme.indexOf('systemctl stop gift-panel-release-mirror.service')).toBeLessThan(readme.indexOf('ln -sfn /opt/gift-panel-release-mirror/releases/PREVIOUS_RELEASE_ID'));
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
