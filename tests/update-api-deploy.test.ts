import { execFileSync, spawnSync, type SpawnSyncReturns } from 'node:child_process';
import { appendFileSync, cpSync, existsSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs';
import { join } from 'node:path';
import { tmpdir } from 'node:os';
import { describe, expect, it } from 'vitest';

function deploymentAsset(name: string): string {
  return readFileSync(new URL(`../deploy/update-api/${name}`, import.meta.url), 'utf8').replaceAll('\r\n', '\n');
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

function resolveBashBinary(platform: NodeJS.Platform, environment: NodeJS.ProcessEnv, pathExists: (path: string) => boolean = existsSync): string {
  if (environment.BASH_BIN) return environment.BASH_BIN;
  if (platform !== 'win32') return '/usr/bin/bash';
  const candidates = [
    'C:\\Program Files\\Git\\usr\\bin\\bash.exe',
    'C:\\Program Files\\Git\\bin\\bash.exe',
  ];
  const binary = candidates.find(pathExists);
  if (!binary) throw new Error('Git Bash was not found; set BASH_BIN explicitly.');
  return binary;
}

describe('deployment Bash selection', () => {
  it('uses the exact Linux Bash path without probing Windows candidates', () => {
    expect(resolveBashBinary('linux', {}, () => { throw new Error('should not probe Git Bash on Linux'); }))
      .toBe('/usr/bin/bash');
  });

  it.each([
    ['preferred usr path', 'C:\\Program Files\\Git\\usr\\bin\\bash.exe'],
    ['legacy bin path when usr is absent', 'C:\\Program Files\\Git\\bin\\bash.exe'],
  ])('uses the existing Windows Git Bash %s', (_name, available) => {
    expect(resolveBashBinary('win32', {}, (candidate) => candidate === available)).toBe(available);
  });

  it('honors an explicit BASH_BIN override without probing defaults', () => {
    expect(resolveBashBinary('win32', { BASH_BIN: 'D:\\tools\\bash.exe' }, () => { throw new Error('should not probe an override'); }))
      .toBe('D:\\tools\\bash.exe');
  });
});

function runBash(script: string, cwd: string, environment: NodeJS.ProcessEnv = {}): SpawnSyncReturns<string> {
  const env = { ...process.env, ...environment };
  const binary = resolveBashBinary(process.platform, env);
  if (process.platform === 'win32' && !environment.BASH_BIN && existsSync('C:\\Program Files\\Git\\usr\\bin')) {
    env.PATH = `C:\\Program Files\\Git\\usr\\bin${env.PATH ? `;${env.PATH}` : ''}`;
  }
  return spawnSync(binary, ['-c', script], {
    cwd,
    encoding: 'utf8',
    env,
  });
}

function quiesceContract(readme: string): string {
  const block = shellBlocksAfter(readme, '## Release mirror (separate service)')
    .find((candidate) => candidate.includes('mirror_quiesce()') && !candidate.includes('ACCOUNT_RECORD='));
  expect(block, 'missing standalone fail-closed quiesce preflight').toBeDefined();
  const start = block!.indexOf('mirror_systemctl_value()');
  const call = block!.lastIndexOf('\nmirror_quiesce');
  expect(start).toBeGreaterThanOrEqual(0);
  expect(call).toBeGreaterThan(start);
  return `set -euo pipefail\n${block!.slice(start, call + '\nmirror_quiesce'.length)}\nprintf quiesce-ok\n`;
}

function verifyQuiesceContract(readme: string): void {
  const root = mkdtempSync(join(tmpdir(), 'gift-panel-quiesce-'));
  const log = posixPath(join(root, 'systemctl.log'));
  const fakeSystemd = `
sudo() { "$@"; }
systemctl() {
  printf '%s\\n' "$*" >> "$SYSTEMCTL_LOG"
  case "$1" in
    show)
      property=\${2#--property=}; unit=$4
      if test "\${FAIL_QUERY:-}" = "$property:$unit"; then return 1; fi
      case "$property:$unit" in
        LoadState:gift-panel-release-mirror.timer) printf '%s\\n' "\${TIMER_LOAD:-loaded}" ;;
        ActiveState:gift-panel-release-mirror.timer) printf '%s\\n' "\${TIMER_ACTIVE:-inactive}" ;;
        UnitFileState:gift-panel-release-mirror.timer) printf '%s' "\${TIMER_UNIT_FILE-disabled}" ;;
        LoadState:gift-panel-release-mirror.service) printf '%s\\n' "\${SERVICE_LOAD:-loaded}" ;;
        ActiveState:gift-panel-release-mirror.service) printf '%s\\n' "\${SERVICE_ACTIVE:-inactive}" ;;
        *) return 1 ;;
      esac ;;
    disable) test "\${FAIL_DISABLE:-0}" != 1 ;;
    stop) test "\${FAIL_STOP:-0}" != 1 ;;
    *) return 1 ;;
  esac
}
`;
  const scenarios: Array<{ name: string; environment: NodeJS.ProcessEnv; status: number; reaches: boolean }> = [
    { name: 'loaded-disabled-inactive', environment: {}, status: 0, reaches: true },
    { name: 'fresh-host-not-found', environment: { TIMER_LOAD: 'not-found', TIMER_ACTIVE: 'inactive', TIMER_UNIT_FILE: '', SERVICE_LOAD: 'not-found', SERVICE_ACTIVE: 'inactive' }, status: 0, reaches: true },
    { name: 'query-failure', environment: { FAIL_QUERY: 'LoadState:gift-panel-release-mirror.timer' }, status: 1, reaches: false },
    { name: 'unknown-load', environment: { TIMER_LOAD: 'masked' }, status: 1, reaches: false },
    { name: 'active-timer', environment: { TIMER_ACTIVE: 'active' }, status: 1, reaches: false },
    { name: 'enabled-timer', environment: { TIMER_UNIT_FILE: 'enabled' }, status: 1, reaches: false },
    { name: 'active-service', environment: { SERVICE_ACTIVE: 'activating' }, status: 1, reaches: false },
    { name: 'disable-failure', environment: { FAIL_DISABLE: '1' }, status: 1, reaches: false },
    { name: 'stop-failure', environment: { FAIL_STOP: '1' }, status: 1, reaches: false },
  ];
  try {
    for (const scenario of scenarios) {
      writeFileSync(join(root, 'systemctl.log'), '');
      const result = runBash(`${fakeSystemd}\n${quiesceContract(readme)}`, root, { SYSTEMCTL_LOG: log, ...scenario.environment });
      expect(result.status, `${scenario.name}: ${result.stderr}`).toBe(scenario.status);
      expect(result.stdout.includes('quiesce-ok'), scenario.name).toBe(scenario.reaches);
      const calls = readFileSync(join(root, 'systemctl.log'), 'utf8').split(/\r?\n/).filter(Boolean);
      expect(calls.some((call) => call.startsWith('start ') || call.startsWith('install ')), `${scenario.name}: no start/install`).toBe(false);
    }
  } finally {
    rmSync(root, { recursive: true, force: true });
  }
}

function dryRunTail(name: 'install' | 'rollback', block: string): string {
  const helperStart = block.indexOf('mirror_systemctl_value()');
  const invocation = block.indexOf('BEFORE_INVOCATION=');
  const segmentStart = block.lastIndexOf('\nmirror_verify_quiesced\n', invocation) + 1;
  const quiesceCall = block.indexOf('\nmirror_quiesce\n', helperStart);
  const definitionsEnd = quiesceCall >= 0 && quiesceCall < segmentStart ? quiesceCall + 1 : segmentStart;
  expect(helperStart, 'missing state verifier definitions').toBeGreaterThanOrEqual(0);
  expect(invocation, 'missing pre-start invocation identity').toBeGreaterThanOrEqual(0);
  expect(segmentStart, 'missing pre-start quiescence call').toBeGreaterThan(0);
  const tail = block.slice(segmentStart);
  const validation = tail.indexOf('test "$cleanup_rc" -eq 0');
  const trapRemoval = tail.indexOf('\ntrap - EXIT INT TERM', validation);
  expect(validation, 'missing explicit cleanup validation').toBeGreaterThanOrEqual(0);
  expect(trapRemoval, 'missing final trap removal').toBeGreaterThanOrEqual(0);
  const binary = name === 'install'
    ? 'FINAL_BINARY=/opt/gift-panel-release-mirror/releases/test/gift-panel-release-mirror'
    : 'PREVIOUS_BINARY=/opt/gift-panel-release-mirror/releases/previous/gift-panel-release-mirror';
  const definitions = block.slice(helperStart, definitionsEnd);
  return `set -euo pipefail\n${binary}\n${definitions}\n${tail.slice(0, trapRemoval + '\ntrap - EXIT INT TERM'.length)}`;
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
      case "$2" in
      --property=LoadState)
        test "\${FAIL_STATE_SHOW:-0}" != 1 || return 1
        printf '%s\\n' "\${LOAD_STATE:-loaded}" ;;
      --property=ActiveState) printf '%s\\n' "\${ACTIVE_STATE:-inactive}" ;;
      --property=UnitFileState) printf '%s\\n' "\${UNIT_FILE_STATE:-disabled}" ;;
      --property=InvocationID)
        starts=$(cat "$START_COUNT" 2>/dev/null || printf 0)
        if test "\${SAME_INVOCATION:-0}" = 1 || test "$starts" = 0; then printf 'before\\n'; else printf 'invocation-%s\\n' "$starts"; fi ;;
      --property=Result)
        test "\${FAIL_RESULT_SHOW:-0}" != 1 || return 1
        printf '%s\\n' "\${RESULT_VALUE:-success}" ;;
      --property=DropInPaths)
        test "\${FAIL_DROPIN_SHOW:-0}" != 1 || return 1
        if test "\${ACTIVE_DROPINS:-0}" = 1 || test -e "$DROPIN"; then printf '%s\\n' "$DROPIN"; fi ;;
      *) return 1 ;;
      esac ;;
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
    const script = dryRunTail(name, block).replaceAll('/run/systemd/system', runRoot);
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
      { name: 'state-query-error', environment: { FAIL_STATE_SHOW: '1' }, status: 1, starts: [], timer: false },
      { name: 'unknown-state', environment: { LOAD_STATE: 'masked' }, status: 1, starts: [], timer: false },
      { name: 'active-state', environment: { ACTIVE_STATE: 'active' }, status: 1, starts: [], timer: false },
      { name: 'enabled-state', environment: { UNIT_FILE_STATE: 'enabled' }, status: 1, starts: [], timer: false },
      { name: 'same-invocation', environment: { SAME_INVOCATION: '1' }, status: 1, starts: ['dry-run'], timer: false },
      { name: 'interrupt', environment: { SEND_SIGNAL: 'INT' }, status: 130, starts: ['dry-run'], timer: false },
      { name: 'terminate', environment: { SEND_SIGNAL: 'TERM' }, status: 143, starts: ['dry-run'], timer: false },
    ];
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

function injectPostSnapshotTrackedMutation(root: string): void {
  const scriptPath = join(root, 'scripts', 'build-update-api.mjs');
  const source = readFileSync(scriptPath, 'utf8');
  const archiveExtraction = "    execFileSync('tar', ['-xf', archive, '-C', snapshot]);";
  expect(source.match(new RegExp(archiveExtraction.replace(/[.*+?^${}()|[\]\\]/g, '\\$&'), 'g'))).toHaveLength(1);
  const mutated = source
    .replace("import { copyFileSync,", "import { appendFileSync, copyFileSync,")
    .replace(archiveExtraction, `${archiveExtraction}\n    appendFileSync(join(root, 'scripts', 'build-update-api.mjs'), '\\n// test-side post-snapshot mutation\\n');`);
  writeFileSync(scriptPath, mutated);
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
      injectPostSnapshotTrackedMutation(temporaryRoot);
      execFileSync('git', ['add', 'scripts/build-update-api.mjs'], { cwd: temporaryRoot });
      execFileSync('git', ['commit', '--quiet', '-m', 'test-side race fixture'], { cwd: temporaryRoot });
      const result = build(temporaryRoot);
      expect(result.status).not.toBe(0);
      expect(`${result.stdout}\n${result.stderr}`).toMatch(/changed during build|clean reviewed Git commit/i);
    } finally {
      rmSync(temporaryRoot, { recursive: true, force: true });
    }
  });

  it('keeps deployment race injection out of the production build script', () => {
    const source = readFileSync(new URL('../scripts/build-update-api.mjs', import.meta.url), 'utf8');

    expect(source).not.toContain('GIFT_PANEL_BUILD_TEST_MUTATE_TRACKED');
    expect(source).not.toContain('deterministic build mutation');
    expect(source).not.toMatch(/appendFileSync\s*\(/);
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

  it('schedules the mirror after timer activation and then after each completed invocation', () => {
    const timer = parseUnit(deploymentAsset('gift-panel-release-mirror.timer'));

    expect(sectionValue(timer, 'Timer', 'OnActiveSec')).toBe('1min');
    expect(sectionValue(timer, 'Timer', 'OnBootSec')).toBeUndefined();
    expect(sectionValue(timer, 'Timer', 'OnUnitInactiveSec')).toBe('5min');
    expect(sectionValue(timer, 'Timer', 'Persistent')).toBe('true');
    expect(sectionValue(timer, 'Timer', 'Unit')).toBe('gift-panel-release-mirror.service');
    for (const [key, value] of Object.entries({ OnActiveSec: '1min', OnUnitInactiveSec: '5min', Persistent: 'true', Unit: 'gift-panel-release-mirror.service' })) {
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
    expect(readme).toContain('test "$RELEASE_ID" = "$("$FINAL_BINARY" --build-commit)"');
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
    expect(readme).toContain('ExecStart=%s --dry-run');
    expect(readme).toContain('systemctl start gift-panel-release-mirror.service');
    expect(readme.indexOf('systemctl enable --now gift-panel-release-mirror.timer')).toBeGreaterThan(readme.indexOf('gift-panel-release-mirror --dry-run'));
    expect(readme).not.toContain('ln -sfn /opt/gift-panel-release-mirror');
    expect(readme).toContain('sudo mv -Tf -- "$CURRENT_TMP" /opt/gift-panel-release-mirror/current');
    expect(readme).toContain('Head/Get/Put');
    expect(readme).toContain('no Delete, list, bucket configuration, or other prefixes');
    expect(readme).toContain('systemctl stop gift-panel-release-mirror.service');
    expect(readme).toContain('systemctl show --property="$property" --value "$unit"');
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
    expect(readme).toContain('PREVIOUS_SIDECAR="$PREVIOUS_RELEASE/gift-panel-release-mirror.reviewed"');
    expect(readme).toContain('test "$PREVIOUS_RELEASE_ID" = "$("$PREVIOUS_BINARY" --build-commit)"');
    expect(readme).toContain('printf \'%s  %s\\n\' "$PREVIOUS_SHA256" "$PREVIOUS_BINARY" | sha256sum -c -');
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

  it('stages and verifies an install while quiesced before atomic publication and pointer switch', () => {
    const readme = deploymentAsset('README.md');
    const blocks = shellBlocksAfter(readme, '## Release mirror (separate service)');
    const install = blocks.find((block) => block.includes('STAGE_DIR='))!;
    const dryRunBlock = blocks.find((block) => block.includes('FINAL_BINARY') && block.includes('DROPIN='))!;
    expect(install, 'missing staged install script').toBeDefined();
    expect(dryRunBlock, 'missing exact-version dry-run script').toBeDefined();

    const quiesce = install.indexOf('mirror_quiesce');
    const stage = install.indexOf('STAGE_DIR=$(sudo mktemp -d');
    const finalCheck = install.indexOf('if sudo test -e "$FINAL_RELEASE"; then');
    const checksum = install.indexOf('sha256sum -c -');
    const identity = install.indexOf('go version -m "$STAGED_BINARY"');
    const publish = install.indexOf('sudo mv -T -- "$STAGE_DIR" "$FINAL_RELEASE"');
    const dryRun = dryRunBlock.indexOf("ExecStart=%s --dry-run");
    const pointer = dryRunBlock.indexOf('sudo mv -Tf -- "$CURRENT_TMP" /opt/gift-panel-release-mirror/current');

    for (const [name, index] of Object.entries({ quiesce, stage, finalCheck, checksum, identity, publish, dryRun, pointer })) {
      expect(index, `missing ${name} gate`).toBeGreaterThanOrEqual(0);
    }
    expect(quiesce).toBeLessThan(stage);
    expect(install.lastIndexOf('\nmirror_verify_quiesced\n')).toBeGreaterThan(publish);
    expect(stage).toBeLessThan(finalCheck);
    expect(finalCheck).toBeLessThan(publish);
    expect(checksum).toBeLessThan(publish);
    expect(identity).toBeLessThan(publish);
    expect(readme.indexOf('sudo mv -T -- "$STAGE_DIR" "$FINAL_RELEASE"')).toBeLessThan(readme.indexOf('ExecStart=%s --dry-run'));
    expect(dryRun).toBeLessThan(pointer);
    for (const evidence of [
      'sudo chmod 0755 "$STAGE_DIR"',
      'wc -l < "$STAGE_DIR/gift-panel-release-mirror.reviewed"',
      'sed -n \'1p\' "$STAGE_DIR/gift-panel-release-mirror.reviewed"',
    ]) {
      const evidenceIndex = install.indexOf(evidence);
      expect(evidenceIndex, `missing staged evidence: ${evidence}`).toBeGreaterThanOrEqual(0);
      expect(evidenceIndex).toBeLessThan(publish);
    }
    expect(install).toContain('cmp -s -- "$STAGED_BINARY" "$FINAL_BINARY"');
    expect(install).toContain('gift-panel-release-mirror.reviewed');
  });

  it('executes fail-closed mirror quiescence for fresh, failed, unknown, active, and enabled states', () => {
    verifyQuiesceContract(deploymentAsset('README.md'));
  });

  it('revalidates quiescence and invocation identity at every start and current-switch boundary', () => {
    const readme = deploymentAsset('README.md');
    const blocks = shellBlocksAfter(readme, '## Release mirror (separate service)');
    const setup = blocks.find((block) => block.includes('ACCOUNT_RECORD=') && block.includes('STAGE_DIR='))!;
    const installDryRun = blocks.find((block) => block.includes('FINAL_BINARY') && block.includes('DROPIN='))!;
    const real = blocks.find((block) => block.includes('real oneshot invocation') || (block.includes('systemctl start gift-panel-release-mirror.service') && !block.includes('DROPIN=')))!;
    const rollback = blocks.find((block) => block.includes('PREVIOUS_SIDECAR'))!;

    expect(setup.indexOf('mirror_quiesce')).toBeLessThan(setup.indexOf('ACCOUNT_RECORD='));
    for (const block of [installDryRun, real, rollback]) {
      expect(block, 'missing independently executable state-gated block').toBeDefined();
      expect(block).toContain('mirror_verify_quiesced');
      expect(block).toContain('InvocationID');
    }
    expect(installDryRun.indexOf('mirror_verify_quiesced')).toBeLessThan(installDryRun.indexOf('DROPIN='));
    const installPointer = installDryRun.indexOf('sudo mv -Tf -- "$CURRENT_TMP"');
    expect(installDryRun.slice(0, installPointer).lastIndexOf('mirror_verify_quiesced')).toBeGreaterThan(installDryRun.indexOf('DROPIN='));
    expect(real.indexOf('mirror_verify_quiesced')).toBeLessThan(real.indexOf('systemctl start gift-panel-release-mirror.service'));
    expect(rollback.indexOf('mirror_quiesce')).toBeLessThan(rollback.indexOf('PREVIOUS_SIDECAR='));
    const rollbackPointer = rollback.indexOf('sudo mv -Tf -- "$CURRENT_TMP"');
    expect(rollback.slice(0, rollbackPointer).lastIndexOf('mirror_verify_quiesced')).toBeGreaterThan(rollback.indexOf('DROPIN='));
  });

  it('requires separate confirmations around the real mirror and timer production gates', () => {
    const readme = deploymentAsset('README.md');
    const install = shellBlocksAfter(readme, '## Release mirror (separate service)').find((block) => block.includes('FINAL_BINARY'))!;
    expect(install).not.toContain('systemctl start gift-panel-release-mirror.service\nsudo systemctl enable');
    expect(install).not.toContain('enable --now gift-panel-release-mirror.timer');
    expect(readme).toContain('Stop here and obtain independent operator confirmation before the real oneshot');
    expect(readme).toContain('releases/v0.4.4/');
    for (const object of ['gift-panel-windows-x64.exe', 'gift-panel-windows-x64.exe.sha256', 'gift-panel-changelog.json', 'release.json']) {
      expect(readme).toContain(`releases/v0.4.4/${object}`);
    }
    expect(readme).toContain('channels/stable/latest.json');
    expect(readme).toContain('signed download URL is redacted');
    expect(readme).toContain('127.0.0.1:12450');
    expect(readme).toContain('Stop here and obtain a separate operator confirmation before enabling the timer');
  });

  it('requires separate action-time confirmations before transfer, setup, and secret installation', () => {
    const readme = deploymentAsset('README.md');
    const transferGate = readme.indexOf('STOP: obtain separate action-time operator confirmation before production transfer');
    const transfer = readme.indexOf('Transfer both the verified normal artifact');
    const setupGate = readme.indexOf('STOP: obtain separate action-time operator confirmation before service-user, version, and unit installation');
    const useradd = readme.indexOf('useradd --system --user-group --home-dir /nonexistent --shell /usr/sbin/nologin gift-panel-mirror');
    const unitInstall = readme.indexOf('deploy/update-api/gift-panel-release-mirror.service /etc/systemd/system/gift-panel-release-mirror.service');
    const secretGate = readme.indexOf('STOP: obtain separate action-time operator confirmation before installing the secret environment file');
    const secretInstall = readme.indexOf('install -o root -g root -m 0600 /secure/gift-panel-release-mirror.env /etc/gift-panel-release-mirror.env');

    for (const [name, index] of Object.entries({ transferGate, transfer, setupGate, useradd, unitInstall, secretGate, secretInstall })) {
      expect(index, `missing ${name}`).toBeGreaterThanOrEqual(0);
    }
    expect(transferGate).toBeLessThan(transfer);
    expect(transfer).toBeLessThan(setupGate);
    expect(setupGate).toBeLessThan(useradd);
    expect(setupGate).toBeLessThan(unitInstall);
    expect(unitInstall).toBeLessThan(secretGate);
    expect(secretGate).toBeLessThan(secretInstall);
  });

  it('preverifies rollback sidecar and binary before dry-run and atomic current switch', () => {
    const readme = deploymentAsset('README.md');
    const rollback = shellBlocksAfter(readme, '## Release mirror (separate service)').find((block) => block.includes('PREVIOUS_SIDECAR'))!;
    expect(rollback, 'missing reviewed rollback script').toBeDefined();

    const quiesce = rollback.indexOf('sudo systemctl disable --now gift-panel-release-mirror.timer');
    const sidecar = rollback.indexOf('PREVIOUS_SIDECAR="$PREVIOUS_RELEASE/gift-panel-release-mirror.reviewed"');
    const checksum = rollback.indexOf('sha256sum -c -');
    const identity = rollback.indexOf('go version -m "$PREVIOUS_BINARY"');
    const dryRun = rollback.indexOf('ExecStart=%s --dry-run');
    const pointer = rollback.indexOf('sudo mv -Tf -- "$CURRENT_TMP" /opt/gift-panel-release-mirror/current');

    for (const [name, index] of Object.entries({ quiesce, sidecar, checksum, identity, dryRun, pointer })) {
      expect(index, `missing rollback ${name} gate`).toBeGreaterThanOrEqual(0);
    }
    expect(quiesce).toBeLessThan(sidecar);
    expect(sidecar).toBeLessThan(checksum);
    expect(checksum).toBeLessThan(dryRun);
    expect(identity).toBeLessThan(dryRun);
    expect(dryRun).toBeLessThan(pointer);
    expect(rollback).toContain('test "$(readlink -f -- "$PREVIOUS_RELEASE")" = "$PREVIOUS_RELEASE"');
    expect(rollback).not.toContain('enable --now gift-panel-release-mirror.timer');
  });

  it('executes the transferred sidecar checks before deployment side effects', () => {
    const readme = deploymentAsset('README.md');
    const install = shellBlocksAfter(readme, '## Release mirror (separate service)').find((block) => block.includes('if ! getent passwd gift-panel-mirror'))!;
    const sidecarChecks = install.slice(0, install.indexOf('mirror_systemctl_value()'));
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
    const account = install.slice(install.indexOf('if ! getent passwd gift-panel-mirror'), install.indexOf('\nRELEASE_ROOT='));
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
      case "\${ACCOUNT_MODE}" in
        wrong-group-record-gid) printf 'gift-panel-mirror:x:996:\\n' ;;
        wrong-group-record-name) printf 'other-primary:x:995:\\n' ;;
        *) printf 'gift-panel-mirror:x:995:\\n' ;;
      esac ;;
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
        'wrong-group-record-name',
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
    const installBlock = blocks.find((block) => block.includes('FINAL_BINARY'));
    expect(blocks).toHaveLength(2);
    expect(installBlock, 'missing install dry-run script').toBeDefined();
    verifyDryRunScript('install', installBlock!, ['dry-run'], false);
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

  it('documents Lighthouse-owned mirroring and retires obsolete GitHub COS setup only after acceptance', () => {
    const readme = deploymentAsset('README.md');

    for (const forbidden of [
      /COS_RELEASE_SECRET_ID/i,
      /COS_RELEASE_SECRET_KEY/i,
      /UPDATE_PUBLISHER_TOOL_SHA/i,
      /_update-publisher-tool/i,
      /Check out update publisher tooling/i,
      /Mirror release to Tencent COS/i,
      /test-cos-connectivity/i,
    ]) {
      expect(readme).not.toMatch(forbidden);
    }
    expect(readme).toContain(
      'Preferred GitHub Actions variables: `UPDATE_API_BASE_URL`, `EVSIGN_ACTIVE_PROFILE`, `EVSIGN_SIGNER_PROFILES_JSON`.',
    );
    expect(readme).toContain('Legacy fallback variables: required `EVSIGN_EXPECTED_SUBJECT` and optional `EVSIGN_CERT`.');
    expect(readme).toContain('Changing `EVSIGN_ACTIVE_PROFILE` switches the certificate selection mode and exact Subject together.');
    expect(readme).toContain('GitHub Actions secrets: `EVSIGN_KEY`, `EVSIGN_PASSWORD`.');
    expect(readme).toContain('every five minutes');
    expect(readme).toContain('lighthouse-cos-publisher');
    expect(readme).toContain('name/cos:HeadObject');
    expect(readme).toContain('name/cos:GetObject');
    expect(readme).toContain('name/cos:PutObject');
    expect(readme).toContain('/etc/gift-panel-release-mirror.env');
    expect(readme).toContain('production acceptance');
    expect(readme).toContain('separate explicit confirmation');
    expect(readme).toContain('obsolete GitHub Environment COS secrets and variables');
    expect(readme).toContain('github-cos-uploader');
    expect(readme).toMatch(/create a replacement.*lighthouse-cos-publisher.*key/is);
    expect(readme).toMatch(/verify.*revoke the old key/is);
  });
});
