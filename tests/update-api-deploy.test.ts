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
      section = sections.get(header[1]) ?? new Map();
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

function shellBlocksAfter(readme: string, heading: string): string[] {
  const section = readme.slice(readme.indexOf(heading));
  return [...section.matchAll(/```sh\r?\n([\s\S]*?)```/g)].map((match) => match[1]);
}

function latestVerifier(readme: string): string {
  const match = readme.match(/curl --fail --silent --show-error "https:\/\/\$PUBLIC_DOMAIN\/api\/v1\/releases\/latest" \| node -e '\r?\n([\s\S]*?)\r?\n' "\$APPROVED_TAG" "\$APPROVED_SHA256" "\$APPROVED_SIZE"/);
  expect(match, 'missing safe streaming latest-release verifier').not.toBeNull();
  return match![1];
}

function verifyLatest(readme: string, body: string, tag = 'v1.2.3', sha256 = 'a'.repeat(64), size = '42'): SpawnSyncReturns<string> {
  return spawnSync(process.execPath, ['-e', latestVerifier(readme), tag, sha256, size], { input: body, encoding: 'utf8' });
}

function posixPath(path: string): string {
  return path.replaceAll('\\', '/').replace(/^([A-Za-z]):/, (_match, drive: string) => `/${drive.toLowerCase()}`);
}

function runBash(script: string, cwd: string, environment: NodeJS.ProcessEnv = {}): SpawnSyncReturns<string> {
  return spawnSync('C:\\Program Files\\Git\\bin\\bash.exe', ['-c', script], {
    cwd,
    encoding: 'utf8',
    env: { ...process.env, ...environment },
  });
}

function dryRunTail(block: string): string {
  const start = block.indexOf('DROPIN=');
  expect(start, 'missing dry-run drop-in definition').toBeGreaterThanOrEqual(0);
  return `set -euo pipefail\n${block.slice(start)}`;
}

function verifyDryRunScript(name: 'install' | 'rollback', block: string, successStarts: string[], enablesTimer: boolean): void {
  const root = mkdtempSync(join(tmpdir(), `gift-panel-${name}-dry-run-`));
  const runRoot = '$PWD/run';
  const fakeSystemd = `
sudo() { "$@"; }
install() { last=\${!#}; mkdir -p "$last"; }
tee() { cat > "$1"; }
journalctl() { printf 'journal\\n' >> "$SYSTEMCTL_LOG"; }
rm() {
  last=\${!#}
  removes=$(cat "$REMOVE_COUNT" 2>/dev/null || printf 0); removes=$((removes + 1)); printf '%s' "$removes" > "$REMOVE_COUNT"
  if test "$last" = "$DROPIN" && test "\${FAIL_REMOVE_AT:-0}" = "$removes"; then return 1; fi
  command rm "$@"
}
systemctl() {
  printf '%s\\n' "$*" >> "$SYSTEMCTL_LOG"
  case "$1" in
    daemon-reload)
      reloads=$(cat "$RELOAD_COUNT" 2>/dev/null || printf 0); reloads=$((reloads + 1)); printf '%s' "$reloads" > "$RELOAD_COUNT"
      test "\${FAIL_RELOAD_AT:-0}" != "$reloads" ;;
    show)
      if test "$3" = Result; then
        test "\${FAIL_RESULT_SHOW:-0}" != 1 || return 1
        printf '%s\\n' "\${RESULT_VALUE:-success}"
      else
        test "\${FAIL_DROPIN_SHOW:-0}" != 1 || return 1
        if test "\${ACTIVE_DROPINS:-0}" = 1 || test -e "$DROPIN"; then printf '%s\\n' "$DROPIN"; fi
      fi ;;
    start)
      starts=$(cat "$START_COUNT" 2>/dev/null || printf 0); starts=$((starts + 1)); printf '%s' "$starts" > "$START_COUNT"
      if test -e "$DROPIN"; then printf 'dry-run\\n' >> "$START_LOG"; else printf 'normal\\n' >> "$START_LOG"; fi
      if test -n "\${SEND_SIGNAL:-}"; then kill -s "$SEND_SIGNAL" "$$"; fi
      test "\${FAIL_START_AT:-0}" != "$starts" ;;
    *) return 0 ;;
  esac
}
`;
  try {
    const script = dryRunTail(block).replaceAll('/run/systemd/system', runRoot);
    const scenarios: Array<{
      name: string;
      environment: NodeJS.ProcessEnv;
      status: number;
      starts: string[];
      timer: boolean;
      reloads?: number;
    }> = [
      { name: 'success', environment: {}, status: 0, starts: successStarts, timer: enablesTimer },
      { name: 'initial-reload-error', environment: { FAIL_RELOAD_AT: '1' }, status: 1, starts: [], timer: false, reloads: 2 },
      { name: 'dry-run-start-error', environment: { FAIL_START_AT: '1' }, status: 1, starts: ['dry-run'], timer: false },
      { name: 'validation-error', environment: { RESULT_VALUE: 'failed' }, status: 1, starts: ['dry-run'], timer: false },
      { name: 'result-show-error', environment: { FAIL_RESULT_SHOW: '1' }, status: 1, starts: ['dry-run'], timer: false },
      { name: 'removal-error', environment: { FAIL_REMOVE_AT: '1' }, status: 1, starts: ['dry-run'], timer: false },
      { name: 'cleanup-reload-error', environment: { FAIL_RELOAD_AT: '2' }, status: 1, starts: ['dry-run'], timer: false, reloads: 3 },
      { name: 'active-dropin', environment: { ACTIVE_DROPINS: '1' }, status: 1, starts: ['dry-run'], timer: false },
      { name: 'dropin-show-error', environment: { FAIL_DROPIN_SHOW: '1' }, status: 1, starts: ['dry-run'], timer: false },
      { name: 'interrupt', environment: { SEND_SIGNAL: 'INT' }, status: 130, starts: ['dry-run'], timer: false },
      { name: 'terminate', environment: { SEND_SIGNAL: 'TERM' }, status: 143, starts: ['dry-run'], timer: false },
    ];
    if (name === 'install') {
      scenarios.push({ name: 'normal-start-error', environment: { FAIL_START_AT: '2' }, status: 1, starts: ['dry-run', 'normal'], timer: false });
    }

    for (const scenario of scenarios) {
      writeFileSync(join(root, 'systemctl.log'), '');
      writeFileSync(join(root, 'start.log'), '');
      writeFileSync(join(root, 'start-count'), '0');
      writeFileSync(join(root, 'reload-count'), '0');
      writeFileSync(join(root, 'remove-count'), '0');
      const result = runBash(`${fakeSystemd}\n${script}`, root, { SYSTEMCTL_LOG: 'systemctl.log', START_LOG: 'start.log', START_COUNT: 'start-count', RELOAD_COUNT: 'reload-count', REMOVE_COUNT: 'remove-count', ...scenario.environment });
      expect(result.status, `${name}:${scenario.name}: ${result.stderr}`).toBe(scenario.status);
      expect(existsSync(join(root, 'run', 'gift-panel-release-mirror.service.d', 'dry-run.conf'))).toBe(false);
      const callLines = readFileSync(join(root, 'systemctl.log'), 'utf8').split(/\r?\n/).filter(Boolean);
      const starts = readFileSync(join(root, 'start.log'), 'utf8').trim().split(/\r?\n/).filter(Boolean);
      expect(starts, `${name}:${scenario.name}: exact start order`).toEqual(scenario.starts);
      expect(callLines.filter((call) => call === 'start gift-panel-release-mirror.service'), `${name}:${scenario.name}: all starts logged`).toHaveLength(scenario.starts.length);
      expect(callLines.filter((call) => call === 'enable --now gift-panel-release-mirror.timer'), `${name}:${scenario.name}: timer enablement`).toEqual(scenario.timer ? ['enable --now gift-panel-release-mirror.timer'] : []);
      if (scenario.reloads !== undefined) {
        expect(Number(readFileSync(join(root, 'reload-count'), 'utf8')), `${name}:${scenario.name}: reload attempts`).toBe(scenario.reloads);
      }
    }
  } finally {
    rmSync(root, { recursive: true, force: true });
  }
}

function build(root: string, environment: NodeJS.ProcessEnv = {}): SpawnSyncReturns<string> {
  return spawnSync(process.execPath, [join(root, 'scripts', 'build-update-api.mjs')], {
    cwd: root,
    encoding: 'utf8',
    timeout: 120_000,
    env: { ...process.env, GOCACHE: join(tmpdir(), 'gift-panel-update-api-vitest-gocache'), ...environment },
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
      const result = build(temporaryRoot, { GIFT_PANEL_BUILD_TEST_MUTATE_TRACKED: '1' });
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
    for (const [key, value] of Object.entries({ Type: 'oneshot', User: 'gift-panel-mirror', Group: 'gift-panel-mirror', EnvironmentFile: '/etc/gift-panel-release-mirror.env', ExecStart: '/opt/gift-panel-release-mirror/current/gift-panel-release-mirror', StateDirectory: 'gift-panel-release-mirror', StateDirectoryMode: '0700', NoNewPrivileges: 'true', PrivateTmp: 'true', ProtectSystem: 'strict', ProtectHome: 'true', ProtectKernelTunables: 'true', ProtectControlGroups: 'true', RestrictSUIDSGID: 'true', CapabilityBoundingSet: '', LockPersonality: 'true', MemoryDenyWriteExecute: 'true', RestrictAddressFamilies: 'AF_UNIX AF_INET AF_INET6', LogNamespace: 'gift-panel-release-mirror', UMask: '0077' })) {
      expect(sectionValues(service, 'Service', key)).toEqual([value]);
    }
    const repeated = parseUnit('[Service]\nUser=gift-panel-mirror\n[Service]\nUser=unexpected\n');
    expect(sectionValues(repeated, 'Service', 'User')).toEqual(['gift-panel-mirror', 'unexpected']);
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
    for (const [key, value] of Object.entries({ OnBootSec: '1min', OnUnitInactiveSec: '5min', Persistent: 'true', Unit: 'gift-panel-release-mirror.service' })) {
      expect(sectionValues(timer, 'Timer', key)).toEqual([value]);
    }
    expect(sectionValue(timer, 'Install', 'WantedBy')).toBe('timers.target');
  });

  it('keeps mirror credentials separate and documents validation before timer enablement', () => {
    const environment = deploymentAsset('gift-panel-release-mirror.env.example');
    const readme = deploymentAsset('README.md');
    const variables = environment.split(/\r?\n/).filter((line) => line && !line.startsWith('#')).map((line) => line.split('=', 2));

    expect(variables).toEqual([['COS_BUCKET', ''], ['COS_REGION', ''], ['COS_SECRET_ID', ''], ['COS_SECRET_KEY', '']]);
    expect(readme).toContain('if ! getent passwd gift-panel-mirror >/dev/null; then');
    expect(readme).toContain('useradd --system --user-group --home-dir /nonexistent --shell /usr/sbin/nologin gift-panel-mirror');
    expect(readme).not.toContain('ACCOUNT_UID');
    expect(readme).toContain('ACCOUNT_GID');
    expect(readme).toContain('GROUP_RECORD=$(getent group "$ACCOUNT_GID")');
    expect(readme).toContain('test "$(id -gn gift-panel-mirror)" = gift-panel-mirror');
    expect(readme).toContain('install -o root -g root -m 0600 /secure/gift-panel-release-mirror.env /etc/gift-panel-release-mirror.env');
    expect(readme).toContain('sha256sum -c');
    expect(readme).toContain('RELEASE_ID="${REVIEWED_COMMIT:?set the reviewed 40-hex commit}"');
    expect(readme).toContain('set -euo pipefail');
    expect(readme).toContain('gift-panel-release-mirror.reviewed');
    expect(readme).toContain('RELEASE_ID=$(sed -n \'1p\' gift-panel-release-mirror.reviewed)');
    expect(readme).toContain('test "$RELEASE_ID" = "$(dist/gift-panel-release-mirror-linux-amd64 --build-commit)"');
    expect(readme).toContain('sha256sum -c -');
    expect(readme).toContain('test "$RELEASE_ID" = "$(/opt/gift-panel-release-mirror/releases/"$RELEASE_ID"/gift-panel-release-mirror --build-commit)"');
    expect(readme).toContain('readlink -f /opt/gift-panel-release-mirror/current/gift-panel-release-mirror');
    expect(readme).toContain('test "$RELEASE_ID" = "$(/opt/gift-panel-release-mirror/current/gift-panel-release-mirror --build-commit)"');
    expect(readme).toContain('systemd must create `StateDirectory` first');
    expect(readme).toContain('gift-panel-release-mirror.service.d/dry-run.conf');
    expect(readme).toContain('/run/systemd/system/gift-panel-release-mirror.service.d/dry-run.conf');
    expect(readme).toContain('on_int()');
    expect(readme).toContain('on_term()');
    expect(readme).toContain('finish_dry_run 130');
    expect(readme).toContain('finish_dry_run 143');
    expect(readme).toContain('test ! -e "$DROPIN"');
    expect(readme).toContain('Result --value gift-panel-release-mirror.service');
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
    expect(readme).toContain('https://$PUBLIC_DOMAIN/api/v1/releases/latest');
    expect(readme).toContain('tag_name');
    expect(readme).toContain('gift-panel-windows-x64.exe');
    expect(readme).toContain('do not re-enable timer');
    expect(readme).toContain('trap on_term TERM');
    expect(readme).toContain('test ! -e "$DROPIN"');
    expect(readme).toContain('Only after this rollback dry-run succeeds');
    expect(readme).toContain('Do not delete immutable release objects');
    expect(readme.indexOf('systemctl disable --now gift-panel-release-mirror.timer')).toBeLessThan(readme.indexOf('ln -sfn /opt/gift-panel-release-mirror/releases/PREVIOUS_RELEASE_ID'));
    expect(readme.indexOf('systemctl stop gift-panel-release-mirror.service')).toBeLessThan(readme.indexOf('ln -sfn /opt/gift-panel-release-mirror/releases/PREVIOUS_RELEASE_ID'));
    for (const block of shellBlocksAfter(readme, '## Release mirror (separate service)')) {
      expect(block.trimStart().startsWith('set -euo pipefail')).toBe(true);
    }
  });

  it('streams only safe public latest-release fields through the executable rollback verifier', () => {
    const readme = deploymentAsset('README.md');
    const signedURL = 'https://download.example.invalid/opaque?never-log-this';
    const good = JSON.stringify({ tag_name: 'v1.2.3', assets: [{ name: 'gift-panel-windows-x64.exe', browser_download_url: signedURL, size: 42, digest: `sha256:${'a'.repeat(64)}` }] });
    const result = verifyLatest(readme, good);

    expect(result.status, result.stderr).toBe(0);
    expect(result.stdout).toBe('');
    expect(result.stderr).toBe('');
    expect(`${result.stdout}${result.stderr}`).not.toContain(signedURL);

    const wrongSchema = verifyLatest(readme, JSON.stringify({ tagName: 'v1.2.3', asset: { sha256: 'a'.repeat(64), size: 42 }, browser_download_url: signedURL }));
    expect(wrongSchema.status).not.toBe(0);
    expect(`${wrongSchema.stdout}${wrongSchema.stderr}`).not.toContain(signedURL);

    const duplicateAsset = verifyLatest(readme, JSON.stringify({ tag_name: 'v1.2.3', assets: [{ name: 'gift-panel-windows-x64.exe', browser_download_url: signedURL, size: 42, digest: `sha256:${'a'.repeat(64)}` }, { name: 'gift-panel-windows-x64.exe', browser_download_url: signedURL, size: 42, digest: `sha256:${'a'.repeat(64)}` }] }));
    expect(duplicateAsset.status).not.toBe(0);
    expect(`${duplicateAsset.stdout}${duplicateAsset.stderr}`).not.toContain(signedURL);

    const nonIntegerEvidence = verifyLatest(readme, good, 'v1.2.3', 'a'.repeat(64), '42.0');
    expect(nonIntegerEvidence.status).not.toBe(0);
  });

  it('documents distinct fail-closed signal cleanup for both mirror dry-runs', () => {
    const readme = deploymentAsset('README.md');
    const blocks = shellBlocksAfter(readme, '## Release mirror (separate service)').filter((block) => block.includes('dry-run'));

    expect(blocks).toHaveLength(2);
    for (const block of blocks) {
      expect(block).toContain('trap - EXIT INT TERM');
      expect(block).toContain('set +e');
      expect(block).toContain('DropInPaths');
      expect(block).toContain('finish_dry_run 130');
      expect(block).toContain('finish_dry_run 143');
    }
  });

  it('executes the transferred sidecar checks before deployment side effects', () => {
    const readme = deploymentAsset('README.md');
    const install = shellBlocksAfter(readme, '## Release mirror (separate service)').find((block) => block.includes('if ! getent passwd gift-panel-mirror'))!;
    const sidecarChecks = install.slice(0, install.indexOf('if ! getent passwd gift-panel-mirror'));
    const root = mkdtempSync(join(tmpdir(), 'gift-panel-sidecar-'));
    try {
      writeFileSync(join(root, 'gift-panel-release-mirror.reviewed'), `${'a'.repeat(40)}\n${'b'.repeat(64)}\n`);
      const good = runBash(`${sidecarChecks}\nprintf sidecar-ok\n`, root);
      expect(good.status, good.stderr).toBe(0);
      expect(good.stdout).toContain('sidecar-ok');

      writeFileSync(join(root, 'gift-panel-release-mirror.reviewed'), `${'a'.repeat(40)}\n`);
      const short = runBash(`${sidecarChecks}\nprintf must-not-reach\n`, root);
      expect(short.status).not.toBe(0);
      expect(short.stdout).not.toContain('must-not-reach');

      writeFileSync(join(root, 'gift-panel-release-mirror.reviewed'), `local\n${'b'.repeat(64)}\n`);
      const local = runBash(`${sidecarChecks}\nprintf must-not-reach\n`, root);
      expect(local.status).not.toBe(0);
      expect(local.stdout).not.toContain('must-not-reach');
    } finally {
      rmSync(root, { recursive: true, force: true });
    }
  });

  it('accepts idempotent private-group accounts and fails each incompatible account gate independently', () => {
    const readme = deploymentAsset('README.md');
    const install = shellBlocksAfter(readme, '## Release mirror (separate service)').find((block) => block.includes('ACCOUNT_RECORD='))!;
    const account = install.slice(install.indexOf('if ! getent passwd gift-panel-mirror'), install.indexOf('sudo install -d'));
    const root = mkdtempSync(join(tmpdir(), 'gift-panel-account-'));
    const log = posixPath(join(root, 'account.log'));
    const fakeAccounts = `
sudo() { "$@"; }
getent() {
  case "$1:$2" in
    passwd:gift-panel-mirror)
      if { test "\${ACCOUNT_MODE}" = absent && test "\${ACCOUNT_CREATED:-0}" != 1; } || test "\${ACCOUNT_MODE}" = existing-group-without-user; then return 2; fi
      case "\${ACCOUNT_MODE}" in
        wrong-passwd-username) printf 'other-mirror:x:999:995::/nonexistent:/usr/sbin/nologin\\n' ;;
        wrong-shell) printf 'gift-panel-mirror:x:999:995::/nonexistent:/bin/bash\\n' ;;
        wrong-home) printf 'gift-panel-mirror:x:999:995::/home/mirror:/usr/sbin/nologin\\n' ;;
        *) printf 'gift-panel-mirror:x:999:995::/nonexistent:/usr/sbin/nologin\\n' ;;
      esac ;;
    group:gift-panel-mirror)
      if { test "\${ACCOUNT_MODE}" = absent && test "\${ACCOUNT_CREATED:-0}" != 1; } || test "\${ACCOUNT_MODE}" = missing-group; then return 2; fi
      printf 'gift-panel-mirror:x:995:\\n' ;;
    group:995)
      if test "\${ACCOUNT_MODE}" = missing-group; then return 2; fi
      if test "\${ACCOUNT_MODE}" = wrong-group-record-gid; then printf 'gift-panel-mirror:x:996:\\n'; else printf 'gift-panel-mirror:x:995:\\n'; fi ;;
    *) return 2 ;;
  esac
}
id() {
  test "$1" = -gn && test "$2" = gift-panel-mirror
  if test "\${ACCOUNT_MODE}" = wrong-id-group; then printf 'other-primary\\n'; else printf 'gift-panel-mirror\\n'; fi
}
useradd() { ACCOUNT_CREATED=1; printf 'useradd\\n' >> "$ACCOUNT_LOG"; }
`;
    try {
      for (const mode of ['absent', 'compatible']) {
        writeFileSync(join(root, 'account.log'), '');
        const result = runBash(`set -euo pipefail\n${fakeAccounts}\n${account}`, root, { ACCOUNT_MODE: mode, ACCOUNT_LOG: log });
        expect(result.status, result.stderr).toBe(0);
        const calls = readFileSync(join(root, 'account.log'), 'utf8').split(/\r?\n/).filter(Boolean);
        expect(calls, `${mode}: exact account mutations`).toEqual(mode === 'absent' ? ['useradd'] : []);
      }
      for (const mode of [
        'wrong-passwd-username',
        'wrong-group-record-gid',
        'wrong-id-group',
        'existing-group-without-user',
        'wrong-shell',
        'wrong-home',
        'missing-group',
      ]) {
        writeFileSync(join(root, 'account.log'), '');
        const incompatible = runBash(`set -euo pipefail\n${fakeAccounts}\n${account}\nprintf must-not-reach\n`, root, { ACCOUNT_MODE: mode, ACCOUNT_LOG: log });
        expect(incompatible.status, mode).not.toBe(0);
        expect(incompatible.stdout, mode).not.toContain('must-not-reach');
        expect(readFileSync(join(root, 'account.log'), 'utf8'), `${mode}: no account mutation`).toBe('');
      }
    } finally {
      rmSync(root, { recursive: true, force: true });
    }
  });

  it('executes the install dry-run across exact start, cleanup, signal, and publication boundaries', () => {
    const readme = deploymentAsset('README.md');
    const blocks = shellBlocksAfter(readme, '## Release mirror (separate service)').filter((block) => block.includes('dry-run'));
    const installBlock = blocks.find((block) => block.includes('enable --now gift-panel-release-mirror.timer'));
    expect(blocks).toHaveLength(2);
    expect(installBlock, 'missing install dry-run script').toBeDefined();
    verifyDryRunScript('install', installBlock!, ['dry-run', 'normal'], true);
  });

  it('executes the rollback dry-run across exact start, cleanup, and signal boundaries', () => {
    const readme = deploymentAsset('README.md');
    const blocks = shellBlocksAfter(readme, '## Release mirror (separate service)').filter((block) => block.includes('dry-run'));
    const rollbackBlock = blocks.find((block) => block.includes('Only after this rollback dry-run succeeds'));
    expect(blocks).toHaveLength(2);
    expect(rollbackBlock, 'missing rollback dry-run script').toBeDefined();
    verifyDryRunScript('rollback', rollbackBlock!, ['dry-run'], false);
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
