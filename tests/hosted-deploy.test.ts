import { createHash } from 'node:crypto';
import { spawnSync } from 'node:child_process';
import { chmodSync, existsSync, mkdirSync, mkdtempSync, readFileSync, readdirSync, rmSync, statSync, utimesSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';
import { parse } from 'yaml';
import { describe, expect, it } from 'vitest';
import { resolveValidatedBashBinary } from './bash-test-runtime';

const projectRoot = fileURLToPath(new URL('..', import.meta.url));
const deploymentFiles = [
  'deploy/hosted/Dockerfile',
  'deploy/hosted/docker-compose.yml',
  'deploy/hosted/gift-panel-hosted.service',
  'deploy/hosted/env.example',
  'scripts/build-hosted.mjs',
  'scripts/test-hosted-mysql.mjs',
] as const;
const ingressFiles = [
  'deploy/hosted/nginx.conf.template',
  'deploy/hosted/logrotate.conf',
  'deploy/hosted/journald.conf',
] as const;
const backupFiles = [
  'deploy/hosted/backup.sh',
  'deploy/hosted/archive-logs.sh',
  'deploy/hosted/restore-drill.sh',
  'deploy/hosted/backup.service',
  'deploy/hosted/backup.timer',
  'deploy/hosted/archive-logs.service',
  'deploy/hosted/archive-logs.timer',
  'deploy/hosted/cos-lifecycle.json',
] as const;
const operationsFiles = [
  'deploy/hosted/README.md',
  'deploy/hosted/health-check.sh',
  'docs/operations/hosted-pilot-checklist.md',
] as const;

function readProjectFile(path: string): string {
  return readFileSync(resolve(projectRoot, path), 'utf8');
}

function createHostedHealthRoot(root: string): string {
  const cacheRoot = join(root, '.cache');
  mkdirSync(cacheRoot, { recursive: true });
  return mkdtempSync(join(cacheRoot, 'hosted-health-'));
}

function expectSafeAdministratorInitialization(initialization: string): void {
  expect(initialization).toContain('docker compose exec app /usr/local/bin/hosted-entrypoint admin init --email <recovery-email>');
  expect(initialization).not.toContain('--uid');
  expect(initialization).toContain('TOTP URI, recovery package password, and confirmation token');
  expect(initialization).toContain("read -r -s -p 'Confirmation token: ' HANDOFF_TOKEN");
  expect(initialization).toContain("read -r -s -p 'Current TOTP code: ' TOTP_CODE");
  expect(initialization).toContain(`printf '{"handoffToken":"%s","totp":"%s"}' "$HANDOFF_TOKEN" "$TOTP_CODE"`);
  expect(initialization).toContain('--request POST');
  expect(initialization).toContain('--header "Origin: $HOSTED_ADMIN_ALLOWED_ORIGIN"');
  expect(initialization).toContain('--header "X-CSRF-Token: $HOSTED_ADMIN_CSRF_TOKEN"');
  expect(initialization).toContain("--header 'Content-Type: application/json'");
  expect(initialization).toContain('http://127.0.0.1:12500/api/admin/recovery/confirm');
  expect(initialization).not.toMatch(/\b(?:HANDOFF_TOKEN|TOTP_CODE)\s*=/);
  expect(initialization).not.toMatch(/"(?:handoffToken|totp)"\s*:\s*"(?!%s")[^"]+"/);
}

function composeDocument(): Record<string, any> {
  return parse(readProjectFile('deploy/hosted/docker-compose.yml')) as Record<string, any>;
}

function nginxBlock(config: string, signature: RegExp): string {
  const match = signature.exec(config);
  if (!match) return '';
  let open = -1;
  const matchEnd = match.index + match[0].length;
  for (let index = match.index; index < matchEnd; index += 1) {
    if (config[index] === '{' && !/[0-9]/.test(config[index + 1] ?? '')) {
      open = index;
      break;
    }
  }
  if (open < 0) open = config.indexOf('{', matchEnd);
  if (open < 0) return '';
  let depth = 0;
  for (let index = open; index < config.length; index += 1) {
    if (config[index] === '{') depth += 1;
    if (config[index] === '}') {
      depth -= 1;
      if (depth === 0) return config.slice(match.index, index + 1);
    }
  }
  return '';
}

function nginxRegexLocationMatches(block: string, path: string): boolean {
  const match = block.match(/^location ~ (?:"([^"]+)"|(\S+))\s*\{/);
  const source = match?.[1] ?? match?.[2];
  return source ? new RegExp(source).test(path) : false;
}

function nginxLocationDecision(block: string, method: string): 'proxy' | number | 'none' {
  const guard = block.match(/if \(\$request_method != ([A-Z]+)\)\s*\{\s*return ([0-9]{3});\s*\}/);
  if (guard && method !== guard[1]) return Number(guard[2]);
  return block.includes('proxy_pass ') ? 'proxy' : 'none';
}

function nginxMapValue(block: string, input: string): string {
  let fallback = '';
  const bodyStart = block.indexOf('{');
  const bodyEnd = block.lastIndexOf('}');
  for (const rawLine of block.slice(bodyStart + 1, bodyEnd).split(/\r?\n/)) {
    const line = rawLine.replace(/#.*$/, '').trim();
    const directive = line.match(/^("[^"]*"|\S+)\s+("[^"]*"|\S+);$/);
    if (!directive) continue;
    const key = directive[1].replace(/^"|"$/g, '');
    const value = directive[2].replace(/^"|"$/g, '');
    if (key === 'default') fallback = value;
    else if (key.startsWith('~') && new RegExp(key.slice(1)).test(input)) return value;
    else if (key === input) return value;
  }
  return fallback;
}

function contextManifest(root: string, current = ''): string[] {
  const result: string[] = [];
  for (const entry of readdirSync(resolve(root, current), { withFileTypes: true }).sort((left, right) => left.name.localeCompare(right.name))) {
    const relative = current ? `${current}/${entry.name}` : entry.name;
    if (entry.isDirectory()) result.push(...contextManifest(root, relative));
    else {
      const path = resolve(root, relative);
      const stats = statSync(path);
      result.push(`${relative}|${stats.mtimeMs}|${createHash('sha256').update(readFileSync(path)).digest('hex')}`);
    }
  }
  return result;
}

function unresolvedRelativeTypeScriptImports(root: string): string[] {
  const unresolved: string[] = [];
  for (const manifestEntry of contextManifest(root)) {
    const relative = manifestEntry.slice(0, manifestEntry.indexOf('|'));
    if (!relative.endsWith('.ts')) continue;
    const source = readFileSync(resolve(root, relative), 'utf8');
    const specifiers = [
      ...source.matchAll(/\bfrom\s+['"]([^'"]+)['"]/g),
      ...source.matchAll(/\bimport\s+['"]([^'"]+)['"]/g),
      ...source.matchAll(/\bimport\(\s*['"]([^'"]+)['"]\s*\)/g),
    ].map((match) => match[1]).filter((specifier) => specifier.startsWith('.'));
    for (const specifier of specifiers) {
      const target = resolve(root, relative, '..', specifier);
      const candidates = [target, `${target}.ts`, `${target}.json`, resolve(target, 'index.ts')];
      if (!candidates.some((candidate) => existsSync(candidate))) {
        unresolved.push(`${relative} -> ${specifier}`);
      }
    }
  }
  return unresolved.sort();
}

function bashPath(path: string): string {
  return path.replace(/^([A-Za-z]):/, (_match, drive: string) => `/${drive.toLowerCase()}`).replaceAll('\\', '/');
}

function fakeBackupTools(root: string): { bin: string; calls: string; cos: string } {
  const bin = join(root, 'bin');
  const calls = join(root, 'calls.log');
  const cos = join(root, 'cos');
  mkdirSync(bin, { recursive: true });
  mkdirSync(cos, { recursive: true });
  const executable = (name: string, source: string): void => {
    const path = join(bin, name);
    writeFileSync(path, `#!/usr/bin/env bash\nset -euo pipefail\n${source}`, 'utf8');
    chmodSync(path, 0o755);
  };
  executable('flock', 'exit "${FAKE_FLOCK_EXIT:-0}"\n');
  executable('timeout', String.raw`
printf 'timeout\t%s\n' "$*" >>"$FAKE_CALLS"
if [[ -v FAKE_TIMEOUT_MATCH && -n "$FAKE_TIMEOUT_MATCH" && "$*" == *"$FAKE_TIMEOUT_MATCH"* ]]; then
  exit 124
fi
[[ "$1" == --foreground ]]
shift 2
exec "$@"
`);
  executable('hosted-date', String.raw`
if [[ -v FAKE_DATE_CALLS ]]; then printf '%s\n' "$*" >>"$FAKE_DATE_CALLS"; fi
if [[ "$FAKE_BACKUP_CALENDAR" == 1 ]]; then
  case "$*" in
    '-u +%Y%m%dT%H%M%SZ %u %d %F') printf '%s\n' '20261101T010203Z 7 01 2026-11-01'; exit 0;;
    '-u -d 2026-10-30 + 1 day +%F') printf '%s\n' '2026-10-31'; exit 0;;
    '-u -d 2026-10-31 + 1 day +%F') printf '%s\n' '2026-11-01'; exit 0;;
    '-u -d 2026-11-01 + 1 day +%F') printf '%s\n' '2026-11-02'; exit 0;;
    '-u -d 2026-10-25 + 7 days +%F') printf '%s\n' '2026-11-01'; exit 0;;
    '-u -d 2026-11-01 + 7 days +%F') printf '%s\n' '2026-11-08'; exit 0;;
    '-u -d 2026-10-01 + 1 month +%F') printf '%s\n' '2026-11-01'; exit 0;;
    '-u -d 2026-11-01 + 1 month +%F') printf '%s\n' '2026-12-01'; exit 0;;
  esac
fi
if [[ "$FAKE_ARCHIVE_CALENDAR" == 1 ]]; then
  case "$*" in
    '+%Z %z') printf '%s\n' 'CST +0800'; exit 0;;
    '-u +%Y%m%dT%H%M%SZ %s') printf '%s\n' '20260817T010203Z 1786928523'; exit 0;;
    '-u -d @1783990923 +%F') printf '%s\n' '2026-07-14'; exit 0;;
    '-u -d @1784250123 +%F') printf '%s\n' '2026-07-17'; exit 0;;
    '-u -d 2026-07-14 + 1 day +%F') printf '%s\n' '2026-07-15'; exit 0;;
    '-u -d 2026-07-15 + 1 day +%F') printf '%s\n' '2026-07-16'; exit 0;;
    '-u -d 2026-07-16 + 1 day +%F') printf '%s\n' '2026-07-17'; exit 0;;
    '-u -d 2026-07-17 + 1 day +%F') printf '%s\n' '2026-07-18'; exit 0;;
  esac
fi
if [[ -v FAKE_SPOOL_CALENDAR && "$FAKE_SPOOL_CALENDAR" == 1 ]]; then
  case "$*" in
    '-u +%F') printf '%s\n' "$FAKE_SPOOL_TODAY"; exit 0;;
    '-u +%Y-%m-%dT%H:%M:%SZ') printf '%s\n' "$FAKE_SPOOL_NOW"; exit 0;;
    '-u -d 2026-08-17 - 1 day +%F') printf '%s\n' '2026-08-16'; exit 0;;
    '-u -d 2026-08-15 + 1 day +%F') printf '%s\n' '2026-08-16'; exit 0;;
    '-u -d 2026-08-16 + 1 day +%F') printf '%s\n' '2026-08-17'; exit 0;;
  esac
fi
exec /usr/bin/date "$@"
`);
  executable('journalctl', String.raw`
printf 'journalctl\t%s\n' "$*" >>"$FAKE_CALLS"
printf '%s\n' '{"MESSAGE":"synthetic hosted application event","SYSLOG_IDENTIFIER":"gift-panel-hosted-app"}'
`);
executable('docker', String.raw`
printf 'docker\t%s\n' "$*" >>"$FAKE_CALLS"
if [[ -v FAKE_DOCKER_FAIL_MATCH && -n "$FAKE_DOCKER_FAIL_MATCH" && "$*" == *"$FAKE_DOCKER_FAIL_MATCH"* ]]; then
  exit 41
fi
if [[ "$*" == 'compose -p gift-panel-hosted -f '*' ps -q --all app' ]]; then
  exists=1
  [[ -v FAKE_APP_EXISTS ]] && exists="$FAKE_APP_EXISTS"
  if [[ "$exists" == 1 ]]; then printf '%s\n' "$FAKE_APP_CONTAINER_ID"; fi
elif [[ "$*" == 'compose -p gift-panel-hosted -f '*' ps -q app' ]]; then
  running=1
  [[ -v FAKE_APP_RUNNING ]] && running="$FAKE_APP_RUNNING"
  if [[ "$running" == 1 ]]; then printf '%s\n' "$FAKE_APP_CONTAINER_ID"; fi
elif [[ "$1" == inspect ]]; then
  [[ "$2" == --format && "$3" == '{{.Created}}' && "$4" == "$FAKE_APP_CONTAINER_ID" ]]
  printf '%s\n' "$FAKE_APP_CONTAINER_CREATED"
elif [[ "$1" == logs ]]; then
  [[ "$*" == *"$FAKE_APP_CONTAINER_ID" ]]
  [[ -v FAKE_APP_LOG_CONTENT ]] && printf '%s' "$FAKE_APP_LOG_CONTENT"
elif [[ "$*" == *"compose -p "*" up -d "* && "$FAKE_FAIL_UP" == 1 ]]; then
  exit 41
elif [[ "$*" == *"volume ls"* ]]; then
  if [[ "$FAKE_UNSAFE_VOLUME" == 1 ]]; then
    printf '%s\n' 'production-volume'
  else
    for argument in "$@"; do
      case "$argument" in label=com.docker.compose.project=*) printf '%s-mysql-data\n' "$(printf '%s' "$argument" | sed 's/^.*=//')";; esac
    done
  fi
elif [[ "$*" == *"SELECT COUNT(*) FROM schema_migrations"* ]]; then
  printf '%s\n' 7
elif [[ "$*" == *"information_schema.tables"* ]]; then
  printf '%s\n' 24
elif [[ "$*" == *"invitation_quotas WHERE"* ]]; then
  printf '%s\n' 0
elif [[ "$*" == *"compose -f"*" exec -T mysql"* ]]; then
  printf '%s\n' 'CREATE TABLE synthetic_backup(id BIGINT);'
else
  cat >/dev/null || true
fi
`);
  executable('zstd', String.raw`
output=''
input=''
while [[ $# -gt 0 ]]; do
  case "$1" in
    -o) output=$2; shift 2;;
    --output=*) output="\${1#--output=}"; shift;;
    --quiet|--ultra|--decompress|-d|-q) shift;;
    -T*|-19|--threads=*) shift;;
    --*) shift;;
    *) input=$1; shift;;
  esac
done
cp -- "$input" "$output"
`);
  executable('age', String.raw`
output=''
input=''
while [[ $# -gt 0 ]]; do
  case "$1" in
    --output) output=$2; shift 2;;
    --recipients-file|--identity) shift 2;;
    --encrypt|--decrypt) shift;;
    *) input=$1; shift;;
  esac
done
cp -- "$input" "$output"
`);
  executable('coscli', String.raw`
printf 'coscli\t%s\n' "$*" >>"$FAKE_CALLS"
[[ "$1" == cp ]]
process_log_disabled=false
for argument in "$@"; do
  [[ "$argument" == '--process-log=false' ]] && process_log_disabled=true
done
if [[ -v FAKE_PROCESS_LOG_ROOT && -n "$FAKE_PROCESS_LOG_ROOT" && "$process_log_disabled" == false ]]; then
  mkdir -p -- "$FAKE_PROCESS_LOG_ROOT/coscli_output"
fi
source_path=$2
destination=$3
if [[ "$FAKE_COS_FAIL_SHA" == 1 && "$destination" == *.sha256 ]]; then
  exit 42
fi
if [[ -v FAKE_COS_FAIL_DAY && -n "$FAKE_COS_FAIL_DAY" && "$destination" == *"$FAKE_COS_FAIL_DAY"* ]]; then
  exit 43
fi
if [[ "$source_path" == cos://* ]]; then
  stored="$FAKE_COS/$(printf '%s' "$source_path" | sed 's#^cos://##')"
  cp -- "$stored" "$destination"
else
  stored="$FAKE_COS/$(printf '%s' "$destination" | sed 's#^cos://##')"
  mkdir -p -- "$(dirname -- "$stored")"
  cp -- "$source_path" "$stored"
fi
`);
  return { bin, calls, cos };
}

function runBash(script: string, environment: NodeJS.ProcessEnv, args: string[] = []) {
  const command = resolveValidatedBashBinary(process.platform, { ...process.env, ...environment });
  return spawnSync(command, [bashPath(resolve(projectRoot, script)), ...args], {
    cwd: projectRoot,
    env: environment,
    encoding: 'utf8',
    windowsHide: true,
  });
}

describe('hosted single-host deployment contract', () => {
  it('ships every Task 1 deployment and build artifact', () => {
    expect(deploymentFiles.map((path) => existsSync(resolve(projectRoot, path))))
      .toEqual(deploymentFiles.map(() => true));
    const packageJSON = JSON.parse(readProjectFile('package.json')) as { scripts?: Record<string, string> };
    expect(packageJSON.scripts?.['build:hosted-server']).toBe('node scripts/build-hosted.mjs');
    expect(packageJSON.scripts?.['verify:hosted-server-repro']).toBe('node scripts/build-hosted.mjs --verify-reproducible');
    expect(packageJSON.scripts?.['test:hosted-mysql']).toBe('node scripts/test-hosted-mysql.mjs');
  });

  it('runs the real MySQL gate without exposing its DSN or password', async () => {
    const moduleURL = new URL('../scripts/test-hosted-mysql.mjs', import.meta.url).href;
    const { runHostedMySQLTests } = await import(`${moduleURL}?privacy=${Date.now()}`) as {
      runHostedMySQLTests: (options: {
        execute: (command: string, args: string[], options: { env: NodeJS.ProcessEnv }) => { status: number | null };
        environment: NodeJS.ProcessEnv;
      }) => void;
    };
    const calls: Array<{ command: string; args: string[]; env: NodeJS.ProcessEnv }> = [];
    expect(() => runHostedMySQLTests({
      environment: {
        RUNNER_TEST_MARKER: 'kept',
        HOSTED_MYSQL_TEST_DSN: 'must-not-reach-docker',
        HOSTED_MYSQL_TEST_REQUIRED: 'must-not-reach-docker',
        HOSTED_MYSQL_TEST_ROOT_PASSWORD: 'must-not-reach-go',
        hosted_mysql_test_dsn: 'lowercase-must-not-reach-docker',
        Hosted_MySql_Test_Required: 'mixed-must-not-reach-docker',
        hosted_mysql_test_root_password: 'lowercase-must-not-reach-go',
      },
      execute(command, args, options) {
        calls.push({ command, args, env: options.env });
        return { status: command === 'go' ? 1 : 0 };
      },
    })).toThrow('hosted MySQL integration tests failed');

    expect(calls.map(({ command, args }) => [command, ...args])).toEqual([
      ['docker', 'compose', '-p', 'gift-panel-hosted-test', '-f', 'deploy/hosted/docker-compose.test.yml', 'up', '-d', '--wait', '--wait-timeout', '120'],
      ['go', '-C', 'goserver', 'test', '-tags=integration', './internal/hosted/store/mysqlstore', '-run', '^TestIntegration', '-count=1'],
      ['docker', 'compose', '-p', 'gift-panel-hosted-test', '-f', 'deploy/hosted/docker-compose.test.yml', 'down', '--volumes', '--remove-orphans'],
    ]);
    const goEnvironment = calls[1].env;
    expect(goEnvironment.HOSTED_MYSQL_TEST_REQUIRED).toBe('1');
    expect(goEnvironment.HOSTED_MYSQL_TEST_DSN).toMatch(/^root:[^@]+@tcp\(127\.0\.0\.1:13306\)\/\?multiStatements=false&parseTime=true$/);
    expect(goEnvironment.HOSTED_MYSQL_TEST_ROOT_PASSWORD).toBeUndefined();
    expect(goEnvironment.RUNNER_TEST_MARKER).toBe('kept');
    expect(calls[0].env.HOSTED_MYSQL_TEST_DSN).toBeUndefined();
    expect(calls[0].env.HOSTED_MYSQL_TEST_REQUIRED).toBeUndefined();
    expect(calls[2].env.HOSTED_MYSQL_TEST_DSN).toBeUndefined();
    expect(calls[2].env.HOSTED_MYSQL_TEST_REQUIRED).toBeUndefined();
    expect(calls[0].env.RUNNER_TEST_MARKER).toBe('kept');
    expect(Object.keys(calls[0].env).filter((key) => key.toUpperCase() === 'HOSTED_MYSQL_TEST_DSN')).toEqual([]);
    expect(Object.keys(calls[0].env).filter((key) => key.toUpperCase() === 'HOSTED_MYSQL_TEST_REQUIRED')).toEqual([]);
    expect(Object.keys(calls[0].env).filter((key) => key.toUpperCase() === 'HOSTED_MYSQL_TEST_ROOT_PASSWORD')).toEqual(['HOSTED_MYSQL_TEST_ROOT_PASSWORD']);
    expect(Object.keys(goEnvironment).filter((key) => key.toUpperCase() === 'HOSTED_MYSQL_TEST_DSN')).toEqual(['HOSTED_MYSQL_TEST_DSN']);
    expect(Object.keys(goEnvironment).filter((key) => key.toUpperCase() === 'HOSTED_MYSQL_TEST_REQUIRED')).toEqual(['HOSTED_MYSQL_TEST_REQUIRED']);
    expect(Object.keys(goEnvironment).filter((key) => key.toUpperCase() === 'HOSTED_MYSQL_TEST_ROOT_PASSWORD')).toEqual([]);

    const packageSource = readProjectFile('package.json');
    expect(packageSource).not.toContain('HOSTED_MYSQL_TEST_DSN');
    expect(packageSource).not.toContain('gift-panel-root-test-only');
  });

  it.each([
    ['startup nonzero', 0, 'status'],
    ['startup throw', 0, 'throw'],
    ['teardown nonzero', 2, 'status'],
    ['teardown throw', 2, 'throw'],
  ] as const)('always attempts exact private teardown after %s', async (_name, failureIndex, failureMode) => {
    const moduleURL = new URL('../scripts/test-hosted-mysql.mjs', import.meta.url).href;
    const { runHostedMySQLTests } = await import(`${moduleURL}?failure=${failureIndex}-${failureMode}-${Date.now()}`) as {
      runHostedMySQLTests: (options: {
        execute: (command: string, args: string[], options: { env: NodeJS.ProcessEnv }) => { status: number | null };
        environment: NodeJS.ProcessEnv;
      }) => void;
    };
    const calls: string[][] = [];
    expect(() => runHostedMySQLTests({
      environment: {},
      execute(command, args) {
        const index = calls.length;
        calls.push([command, ...args]);
        if (index === failureIndex && failureMode === 'throw') throw new Error('secret-bearing child failure');
        return { status: index === failureIndex ? 1 : 0 };
      },
    })).toThrow(/^hosted MySQL integration tests failed$/);
    expect(calls.at(-1)).toEqual([
      'docker', 'compose', '-p', 'gift-panel-hosted-test', '-f', 'deploy/hosted/docker-compose.test.yml',
      'down', '--volumes', '--remove-orphans',
    ]);
  });

  it('keeps MySQL private and publishes only the application loopback port', () => {
    if (!existsSync(resolve(projectRoot, 'deploy/hosted/docker-compose.yml'))) {
      expect(existsSync(resolve(projectRoot, 'deploy/hosted/docker-compose.yml'))).toBe(true);
      return;
    }
    const compose = composeDocument();
    const app = compose.services?.app;
    const mysql = compose.services?.mysql;

    expect(Object.keys(compose.services ?? {}).sort()).toEqual(['app', 'mysql']);
    expect(app?.ports).toEqual(['127.0.0.1:12500:12500']);
    expect(mysql?.ports).toBeUndefined();
    expect(app?.networks?.hosted_internal).toEqual({});
    expect(app?.networks?.hosted_egress?.gw_priority).toBe(1);
    expect(mysql?.networks).toEqual(['hosted_internal']);
    expect(compose.networks?.hosted_internal?.internal).toBe(true);
    expect(compose.networks?.hosted_egress?.internal).not.toBe(true);
    expect(app?.logging).toEqual({
      driver: 'local',
      options: { 'max-size': '512m', 'max-file': '4', compress: 'true' },
    });
    expect(512 * 1024 * 1024 * 4).toBeGreaterThan(256 * 1024 * 1024);
    expect(mysql?.logging).toEqual({
      driver: 'local',
      options: { 'max-size': '20m', 'max-file': '5', compress: 'true' },
    });
    expect(readProjectFile('deploy/hosted/env.example')).toContain('Docker Compose 2.33.1+');
    expect(mysql?.image).toMatch(/^mysql:8\.4\.\d+@sha256:[0-9a-f]{64}$/);
    expect(app?.image).not.toMatch(/:latest(?:@|$)/);
  });

  it('runs both services with bounded resources, health checks, and hardened application privileges', () => {
    if (!existsSync(resolve(projectRoot, 'deploy/hosted/docker-compose.yml'))) {
      expect(existsSync(resolve(projectRoot, 'deploy/hosted/docker-compose.yml'))).toBe(true);
      return;
    }
    const { services, volumes } = composeDocument();
    const app = services.app;
    const mysql = services.mysql;

    expect(app.user).toBe('65532:65532');
    expect(app.read_only).toBe(true);
    expect(app.cap_drop).toEqual(['ALL']);
    expect(app.security_opt).toContain('no-new-privileges:true');
    expect(app.tmpfs).toEqual(['/tmp:rw,noexec,nosuid,nodev,size=64m']);
    expect(app.mem_limit).toBe('1g');
    expect(app.environment?.GOMEMLIMIT).toBe('768MiB');
    expect(app.environment?.HOSTED_LOG_FILE).toBe('/var/log/gift-panel-hosted/app.log');
    expect(app.volumes).toContainEqual({
      type: 'bind',
      source: '/var/log/gift-panel-hosted/app.log',
      target: '/var/log/gift-panel-hosted/app.log',
    });
    expect(app.healthcheck?.test?.[0]).toBe('CMD-SHELL');
    expect(app.depends_on?.mysql?.condition).toBe('service_healthy');

    expect(mysql.mem_limit).toBe('1280m');
    expect(mysql.command).toEqual(expect.arrayContaining([
      '--skip-name-resolve',
      '--character-set-server=utf8mb4',
      '--innodb-buffer-pool-size=536870912',
      '--log-bin-trust-function-creators=1',
    ]));
    expect(mysql.healthcheck?.test?.[0]).toBe('CMD-SHELL');
    expect(mysql.volumes).toContain('hosted_mysql_data:/var/lib/mysql');
    expect(volumes?.hosted_mysql_data).toEqual({});
  });

  it('mounts external secret files without literal credentials in Compose or the example environment', () => {
    if (!existsSync(resolve(projectRoot, 'deploy/hosted/docker-compose.yml'))) {
      expect(existsSync(resolve(projectRoot, 'deploy/hosted/docker-compose.yml'))).toBe(true);
      return;
    }
    const compose = composeDocument();
    const requiredSecrets = [
      'mysql_root_password', 'mysql_password', 'mysql_dsn', 'encryption_key',
      'hmac_key', 'admin_csrf_token', 'smtp_password',
    ];
    expect(Object.keys(compose.secrets ?? {}).sort()).toEqual([...requiredSecrets].sort());
    for (const secret of requiredSecrets) {
      expect(compose.secrets[secret]?.file).toMatch(/^\$\{HOSTED_[A-Z0-9_]+_FILE:\?/);
    }
    expect(compose.services.app.secrets).toBeUndefined();
    expect(compose.services.app.volumes).toEqual(expect.arrayContaining([
      {
        type: 'bind',
        source: '${HOSTED_MYSQL_DSN_FILE:?set HOSTED_MYSQL_DSN_FILE}',
        target: '/run/secrets/mysql_dsn',
        read_only: true,
      },
      {
        type: 'bind',
        source: '${HOSTED_ENCRYPTION_KEY_FILE:?set HOSTED_ENCRYPTION_KEY_FILE}',
        target: '/run/secrets/encryption_key',
        read_only: true,
      },
      {
        type: 'bind',
        source: '${HOSTED_HMAC_KEY_FILE:?set HOSTED_HMAC_KEY_FILE}',
        target: '/run/secrets/hmac_key',
        read_only: true,
      },
      {
        type: 'bind',
        source: '${HOSTED_ADMIN_CSRF_TOKEN_FILE:?set HOSTED_ADMIN_CSRF_TOKEN_FILE}',
        target: '/run/secrets/admin_csrf_token',
        read_only: true,
      },
      {
        type: 'bind',
        source: '${HOSTED_SMTP_PASSWORD_FILE:?set HOSTED_SMTP_PASSWORD_FILE}',
        target: '/run/secrets/smtp_password',
        read_only: true,
      },
    ]));
    expect(compose.services.mysql.secrets).toEqual(expect.arrayContaining(['mysql_root_password', 'mysql_password']));

    const rawCompose = readProjectFile('deploy/hosted/docker-compose.yml');
    expect(rawCompose).not.toMatch(/^\s*(?:MYSQL_PASSWORD|MYSQL_ROOT_PASSWORD|HOSTED_MYSQL_DSN|HOSTED_ADMIN_CSRF_TOKEN|HOSTED_SMTP_PASSWORD):\s/m);
    const example = readProjectFile('deploy/hosted/env.example');
    expect(example).not.toMatch(/(?:PASSWORD|TOKEN|KEY|DSN)=(?!\/|\$|$).+/);
  });

  it('uses digest-pinned build images and copies only hosted production artifacts into the final image', () => {
    if (!existsSync(resolve(projectRoot, 'deploy/hosted/Dockerfile'))) {
      expect(existsSync(resolve(projectRoot, 'deploy/hosted/Dockerfile'))).toBe(true);
      return;
    }
    const dockerfile = readProjectFile('deploy/hosted/Dockerfile');
    const images = [...dockerfile.matchAll(/^FROM\s+(\S+)/gm)].map((match) => match[1]);
    expect(images.length).toBeGreaterThanOrEqual(3);
    for (const image of images) {
      expect(image).toMatch(/^[^\s:]+(?::[^\s@]+)?@sha256:[0-9a-f]{64}$/);
      expect(image).not.toMatch(/:latest(?:@|$)/);
    }
    expect(dockerfile).toMatch(/^# syntax=docker\/dockerfile:1\.7@sha256:[0-9a-f]{64}$/m);
    expect(dockerfile).toContain('npm ci && npm run build:hosted');
    const uiStage = dockerfile.slice(0, dockerfile.indexOf('\nFROM ', dockerfile.indexOf('\nFROM ') + 1));
    expect(uiStage.indexOf('COPY src/hosted')).toBeLessThan(uiStage.indexOf('RUN npm ci && npm run build:hosted'));
    for (const dependency of ['types.ts', 'duration.ts', 'gift-rule-operations.ts']) {
      expect(uiStage).toMatch(new RegExp(`^COPY [^\\n]*src/${dependency.replace(/[.*+?^${}()|[\\]\\]/g, '\\$&')}[^\\n]*$`, 'm'));
    }
    expect(uiStage.match(/npm run build:hosted/g)).toHaveLength(1);
    expect(dockerfile).toContain("CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags='-s -w'");
    expect(dockerfile).toMatch(/^USER 65532:65532$/m);
    expect(dockerfile).toMatch(/COPY --from=server-build \/out\/gift-panel-hosted \/usr\/local\/bin\/gift-panel-hosted/);
    expect(dockerfile).toMatch(/COPY --from=ui-build \/build\/goserver\/cmd\/hosted\/dist \/srv\/gift-panel-hosted\/ui/);
    const finalStage = dockerfile.slice(dockerfile.lastIndexOf('\nFROM ') + 1);
    expect(finalStage).not.toMatch(/^COPY (?!--from=)/m);
    expect(dockerfile).not.toMatch(/^\s*(?:COPY|ADD)\s+\.\s/m);
    expect(dockerfile).not.toMatch(/(?:\.git|local\.config|ffmpeg|gift-panel\.exe)/i);
  });

  it('builds from a minimal deterministic context and an exact local test tag', async () => {
    const moduleURL = new URL('../scripts/build-hosted.mjs', import.meta.url).href;
    const module = await import(/* @vite-ignore */ moduleURL).catch(() => undefined) as undefined | {
      buildHostedServer(options: { projectRoot: string; run: (...args: any[]) => unknown }): void;
    };
    expect(module).toBeDefined();
    if (!module) return;

    const calls: any[][] = [];
    const manifests: string[][] = [];
    let buildKitInspectCount = 0;
    const run = (...args: any[]): string => {
      calls.push(args);
      const commandArgs = args[1] as string[];
      if (commandArgs.join(' ') === 'buildx version') return 'github.com/docker/buildx v0.13.1';
      if (commandArgs.join(' ') === 'buildx inspect --bootstrap') {
        return buildKitInspectCount++ === 0 ? 'Name: default\nBuildKit: v0.13.2' : 'Name: default\nBuildKit version: v0.13.2';
      }
      if (commandArgs[0] === 'load' && commandArgs[1] === '--input') return '';
      if (commandArgs[0] !== 'buildx' || commandArgs[1] !== 'build') throw new Error(`unexpected docker call: ${commandArgs.join(' ')}`);
      const context = args[2].cwd as string;
      manifests.push(contextManifest(context));
      expect(existsSync(resolve(context, 'package-lock.json'))).toBe(true);
      expect(existsSync(resolve(context, 'goserver/internal/hosted/store/mysqlstore/migrations/0001_foundation.sql'))).toBe(true);
      expect(existsSync(resolve(context, 'src/hosted/main.ts'))).toBe(true);
      expect(unresolvedRelativeTypeScriptImports(context)).toEqual([]);
      expect(existsSync(resolve(context, '.git'))).toBe(false);
      expect(existsSync(resolve(context, 'src/main.ts'))).toBe(false);
      expect(existsSync(resolve(context, 'goserver/ffmpeg'))).toBe(false);
      expect(existsSync(resolve(context, 'dist/gift-panel.exe'))).toBe(false);
      return '';
    };
    const build = (): void => module.buildHostedServer({
      projectRoot,
      run,
    });
    build();
    build();

    expect(calls).toHaveLength(8);
    expect(manifests[1]).toEqual(manifests[0]);
    const buildCalls = calls.filter((call) => call[1][0] === 'buildx' && call[1][1] === 'build');
    expect(buildCalls).toHaveLength(2);
    expect(buildCalls[0][0]).toBe('docker');
    expect(buildCalls[0][1].slice(0, 3)).toEqual(['buildx', 'build', '--output']);
    expect(buildCalls[0][1][3]).toMatch(/^type=docker,dest=.+hosted-image\.tar,rewrite-timestamp=true$/);
    expect(buildCalls[0][1].slice(4)).toEqual([
      '--provenance=false', '--sbom=false', '--platform', 'linux/amd64', '--build-arg', 'SOURCE_DATE_EPOCH=0',
      '--file', 'deploy/hosted/Dockerfile', '--tag', 'gift-panel-hosted:test', '.',
    ]);
    expect(calls.filter((call) => call[1][0] === 'load').map((call) => call[1].slice(0, 2))).toEqual([
      ['load', '--input'], ['load', '--input'],
    ]);
  });

  it('fails before building when Buildx or BuildKit lacks timestamp rewrite support', async () => {
    const moduleURL = new URL('../scripts/build-hosted.mjs', import.meta.url).href;
    const module = await import(/* @vite-ignore */ moduleURL) as {
      buildHostedServer(options: { projectRoot: string; run: (...args: any[]) => unknown }): void;
    };
    const oldBuildxCalls: string[][] = [];
    expect(() => module.buildHostedServer({
      projectRoot,
      run: (_command: string, args: string[]) => {
        oldBuildxCalls.push(args);
        return 'github.com/docker/buildx v0.12.1';
      },
    })).toThrow(/Buildx 0\.13\.0 or newer/);
    expect(oldBuildxCalls).toEqual([['buildx', 'version']]);

    const oldBuildKitCalls: string[][] = [];
    expect(() => module.buildHostedServer({
      projectRoot,
      run: (_command: string, args: string[]) => {
        oldBuildKitCalls.push(args);
        if (args[1] === 'version') return 'github.com/docker/buildx v0.13.1';
        return 'Name: default\nBuildKit: v0.12.5';
      },
    })).toThrow(/BuildKit 0\.13\.0 or newer/);
    expect(oldBuildKitCalls).toEqual([['buildx', 'version'], ['buildx', 'inspect', '--bootstrap']]);

    const invalidBuildKitCalls: string[][] = [];
    expect(() => module.buildHostedServer({
      projectRoot,
      run: (_command: string, args: string[]) => {
        invalidBuildKitCalls.push(args);
        if (args[1] === 'version') return 'github.com/docker/buildx v0.13.1';
        return 'Name: default\nBuildKit version: unavailable';
      },
    })).toThrow(/BuildKit 0\.13\.0 or newer/);
    expect(invalidBuildKitCalls).toEqual([['buildx', 'version'], ['buildx', 'inspect', '--bootstrap']]);
  });

  it('runs two independent no-cache builds and rejects different image IDs', async () => {
    const moduleURL = new URL('../scripts/build-hosted.mjs', import.meta.url).href;
    const module = await import(/* @vite-ignore */ moduleURL) as {
      verifyHostedReproducibility?: (options: { projectRoot: string; nonce: string; run: (...args: any[]) => unknown }) => string;
    };
    expect(typeof module.verifyHostedReproducibility).toBe('function');
    if (!module.verifyHostedReproducibility) return;

    const calls: string[][] = [];
    const buildContexts: string[] = [];
    let inspectCount = 0;
    expect(() => module.verifyHostedReproducibility?.({
      projectRoot,
      nonce: 'contract',
      run: (_command: string, args: string[], options: { cwd?: string }) => {
        calls.push(args);
        if (args.join(' ') === 'buildx version') return 'github.com/docker/buildx v0.13.1';
        if (args.join(' ') === 'buildx inspect --bootstrap') return 'Name: default\nBuildKit: v0.13.2';
        if (args[0] === 'buildx' && args[1] === 'build') {
          buildContexts.push(options.cwd ?? '');
          return '';
        }
        if (args[0] === 'load' && args[1] === '--input') return '';
        if (args[0] === 'image' && args[1] === 'inspect') {
          return inspectCount++ === 0 ? `sha256:${'1'.repeat(64)}` : `sha256:${'2'.repeat(64)}`;
        }
        if (args[0] === 'image' && args[1] === 'rm') return '';
        throw new Error(`unexpected docker call: ${args.join(' ')}`);
      },
    })).toThrow(/not reproducible/);

    const builds = calls.filter((args) => args[0] === 'buildx' && args[1] === 'build');
    expect(builds).toHaveLength(2);
    expect(buildContexts).toHaveLength(2);
    expect(buildContexts[0]).not.toBe(buildContexts[1]);
    for (const [index, args] of builds.entries()) {
      expect(args).toContain('--no-cache');
      expect(args).toContain('--pull');
      expect(args.some((arg) => /^type=docker,dest=.+hosted-image\.tar,rewrite-timestamp=true$/.test(arg))).toBe(true);
      expect(args).toContain(`gift-panel-hosted:repro-contract-${index === 0 ? 'a' : 'b'}`);
    }
    expect(calls.filter((args) => args[0] === 'image' && args[1] === 'rm')).toEqual([
      ['image', 'rm', 'gift-panel-hosted:repro-contract-b'],
      ['image', 'rm', 'gift-panel-hosted:repro-contract-a'],
    ]);
  });

  it('prepares the exact host application log before a credential-free Compose lifecycle', () => {
    if (!existsSync(resolve(projectRoot, 'deploy/hosted/gift-panel-hosted.service'))) {
      expect(existsSync(resolve(projectRoot, 'deploy/hosted/gift-panel-hosted.service'))).toBe(true);
      return;
    }
    const service = readProjectFile('deploy/hosted/gift-panel-hosted.service');
    expect(service).toContain('EnvironmentFile=/etc/gift-panel-hosted/env');
    expect(service).toContain('ExecStartPre=/usr/bin/install -d -o root -g root -m 0750 /var/log/gift-panel-hosted');
    expect(service).toContain('ExecStartPre=/usr/bin/test ! -L /var/log/gift-panel-hosted/app.log');
    expect(service).toContain('ExecStartPre=/usr/bin/test ! -e /var/log/gift-panel-hosted/app.log -o -f /var/log/gift-panel-hosted/app.log');
    expect(service).toContain('ExecStartPre=/usr/bin/touch /var/log/gift-panel-hosted/app.log');
    expect(service).toContain('ExecStartPre=/usr/bin/chown 65532:65532 /var/log/gift-panel-hosted/app.log');
    expect(service).toContain('ExecStartPre=/usr/bin/chmod 0640 /var/log/gift-panel-hosted/app.log');
    expect(service).toContain('ExecStart=/usr/bin/docker compose up -d --remove-orphans');
    expect(service).toContain('ExecStop=/usr/bin/docker compose down --remove-orphans');
    expect(service).not.toMatch(/Environment=.*(?:PASSWORD|TOKEN|KEY|DSN)=/i);
    expect(readProjectFile('deploy/hosted/env.example')).toContain(
      'docker compose --env-file deploy/hosted/env.example -f deploy/hosted/docker-compose.yml config',
    );
  });

  it('ships the Task 2 ingress and 30-day hot-log policies', () => {
    expect(ingressFiles.map((path) => existsSync(resolve(projectRoot, path))))
      .toEqual(ingressFiles.map(() => true));
  });

  it('renders a bounded TLS ingress with the required browser security policy', () => {
    if (!ingressFiles.every((path) => existsSync(resolve(projectRoot, path)))) return;
    const template = readProjectFile('deploy/hosted/nginx.conf.template');
    const rendered = template
      .replaceAll('{{ONLINE_DOMAIN}}', 'hosted.example.invalid')
      .replaceAll('{{TLS_CERTIFICATE}}', '/etc/ssl/hosted/fullchain.pem')
      .replaceAll('{{TLS_CERTIFICATE_KEY}}', '/etc/ssl/hosted/privkey.pem');
    expect(rendered).not.toMatch(/\{\{[^}]+\}\}/);

    const plainHTTP = nginxBlock(rendered, /server\s*\{\s*listen 80;/);
    const https = nginxBlock(rendered, /server\s*\{\s*listen 443 ssl;/);
    expect(plainHTTP).toContain('return 308 https://hosted.example.invalid$request_uri;');
    expect(plainHTTP).not.toContain('https://$host');
    expect(https).toContain('ssl_protocols TLSv1.2 TLSv1.3;');
    expect(rendered).toContain('server_tokens off;');
    expect(https).toContain('client_max_body_size 2m;');
    expect(https).toContain('add_header Strict-Transport-Security "max-age=15552000" always;');
    expect(rendered).not.toMatch(/includeSubDomains/i);
    expect(https).toContain("add_header Content-Security-Policy \"default-src 'self'; object-src 'none'; base-uri 'none'; frame-ancestors 'none';");
    expect(https).toContain("img-src 'self' data:");
    expect(https).toContain("style-src 'self'; style-src-attr 'unsafe-inline'");
    expect(https).toContain('add_header X-Content-Type-Options "nosniff" always;');
    expect(https).toContain('add_header Referrer-Policy "no-referrer" always;');
    expect(https).toContain('add_header Permissions-Policy "camera=(), microphone=(), geolocation=()" always;');
  });

  it('keeps private endpoints private and disables buffering only on exact SSE routes', () => {
    if (!existsSync(resolve(projectRoot, 'deploy/hosted/nginx.conf.template'))) return;
    const config = readProjectFile('deploy/hosted/nginx.conf.template');
    const health = nginxBlock(config, /location = \/healthz\s*/);
    const internalExact = nginxBlock(config, /location = \/internal\s*/);
    const internalTree = nginxBlock(config, /location \^~ \/internal\/\s*/);
    expect(health).toMatch(/return 404;/);
    expect(internalExact).toMatch(/return 404;/);
    expect(internalTree).toMatch(/return 404;/);
    for (const privateBlock of [health, internalExact, internalTree]) {
      expect(privateBlock).not.toContain('proxy_pass');
    }
    expect(config).not.toMatch(/(?:3306|docker\.sock|\/var\/run\/docker)/i);
    expect(config.match(/proxy_pass http:\/\/hosted_app;/g)?.length).toBeGreaterThan(0);
    expect(config).toMatch(/upstream hosted_app\s*\{[\s\S]*?server 127\.0\.0\.1:12500;/);

    const runtimeEvents = nginxBlock(config, /location = \/api\/runtime\/events\s*/);
    const obsEvents = nginxBlock(config, /location ~ "\^\/obs\/\[A-Za-z0-9_-\]\{43\}\/events\$"\s*/);
    expect(runtimeEvents).toContain('proxy_buffering off;');
    expect(obsEvents).toContain('proxy_buffering off;');
    expect(config.match(/proxy_buffering off;/g)).toHaveLength(2);
    expect(runtimeEvents).not.toContain('limit_req ');
    expect(obsEvents).not.toContain('limit_req ');
    for (const [route, block] of [['/api/runtime/events', runtimeEvents], ['/obs/:publicID/events', obsEvents]] as const) {
      expect(nginxLocationDecision(block, 'GET'), `${route} GET`).toBe('proxy');
      for (const method of ['POST', 'HEAD', 'DELETE']) {
        expect(nginxLocationDecision(block, method), `${route} ${method}`).toBe(405);
      }
      expect(block).not.toContain('rewrite ');
      expect(block).not.toContain('add_header ');
    }
    const ssePathMap = nginxBlock(config, /map \$uri \$hosted_sse_path\s*/);
    const nonGETMap = nginxBlock(config, /map \$request_method \$hosted_non_get\s*/);
    const allowMap = nginxBlock(config, /map "\$hosted_sse_path:\$hosted_non_get:\$status" \$hosted_allow\s*/);
    const allowFor = (path: string, method: string, status: number): string => nginxMapValue(
      allowMap,
      `${nginxMapValue(ssePathMap, path)}:${nginxMapValue(nonGETMap, method)}:${status}`,
    );
    for (const path of ['/api/runtime/events', `/obs/${'A'.repeat(43)}/events`]) {
      for (const method of ['POST', 'HEAD', 'DELETE']) expect(allowFor(path, method, 405), `${path} ${method}`).toBe('GET');
      expect(allowFor(path, 'GET', 200), `${path} GET`).toBe('');
      expect(allowFor(path, 'GET', 405), `${path} GET 405`).toBe('');
    }
    expect(allowFor('/api/configuration', 'POST', 405)).toBe('');
    const https = nginxBlock(config, /server\s*\{\s*listen 443 ssl;/);
    expect(https).toContain('add_header Allow $hosted_allow always;');
    expect(https).toContain('add_header Strict-Transport-Security "max-age=15552000" always;');
    expect(https).toContain('add_header Content-Security-Policy ');
    const fallback = nginxBlock(config, /location \/\s*\{/);
    expect(fallback).toMatch(/return 404;/);
    expect(fallback).not.toContain('proxy_pass');
  });

  it('logs only the approved normalized request metadata', () => {
    if (!existsSync(resolve(projectRoot, 'deploy/hosted/nginx.conf.template'))) return;
    const config = readProjectFile('deploy/hosted/nginx.conf.template');
    const format = config.match(/log_format hosted_json escape=json[\s\S]*?;/)?.[0] ?? '';
    for (const field of ['$request_id', '$status', '$request_time', '$request_method', '$hosted_route', '$remote_addr', '$http_user_agent']) {
      expect(format).toContain(field);
    }
    expect(format).not.toMatch(/\$(?:request_uri|request(?![_A-Za-z0-9])|args|query_string|cookie_|http_cookie|http_authorization|upstream_http_)/);
    const routeMap = nginxBlock(config, /map \$uri \$hosted_route\s*/);
    expect(routeMap).not.toBe('');
    expect(nginxMapValue(routeMap, '/api/admin/bili-service/challenge')).toBe('admin_bili_service_challenge');
    expect(nginxMapValue(routeMap, '/api/admin/bili-service/challenge/proof')).toBe('admin_bili_service_challenge');
    expect(nginxMapValue(routeMap, '/api/admin/bili-service/challenge/proof/extra')).not.toBe('admin_bili_service_challenge');
    expect(config).toMatch(/access_log \/var\/log\/nginx\/gift-panel-hosted\.access\.log hosted_json;/);
    expect(config).toContain('error_log /var/log/nginx/gift-panel-hosted.error.log crit;');
  });

  it('uses separate request buckets while SSE relies only on the per-IP connection cap', () => {
    if (!existsSync(resolve(projectRoot, 'deploy/hosted/nginx.conf.template'))) return;
    const config = readProjectFile('deploy/hosted/nginx.conf.template');
    expect(config).toContain('limit_req_zone $binary_remote_addr zone=hosted_auth:10m rate=10r/m;');
    expect(config).toContain('limit_req_zone $binary_remote_addr zone=hosted_account:10m rate=30r/m;');
    expect(config).toContain('limit_req_zone $binary_remote_addr zone=hosted_api:10m rate=120r/m;');
    expect(config).toContain('limit_conn_zone $binary_remote_addr zone=hosted_connections:10m;');
    expect(config).toContain('limit_conn hosted_connections 20;');
    expect(config).toContain('limit_req_status 429;');
    expect(config).toContain('limit_conn_status 429;');

    const auth = nginxBlock(config, /location ~ \^\/api\/\(\?:auth\/bili\/challenges/);
    const exchange = nginxBlock(config, /location ~ "\^\/obs\/\[A-Za-z0-9_-\]\{43\}\/exchange\$"/);
    const account = nginxBlock(config, /location ~ \^\/api\/\(\?:auth\/registration/);
    const api = nginxBlock(config, /location \/api\/\s*/);
    expect(auth).toContain('limit_req zone=hosted_auth burst=5 nodelay;');
    for (const endpoint of ['/api/admin/bili-service/challenge', '/api/admin/bili-service/replace', '/api/admin/auth/email/challenges', '/api/admin/session/email']) {
      expect(nginxRegexLocationMatches(auth, endpoint), endpoint).toBe(true);
    }
    for (const endpoint of ['/api/admin/auth/bili/challenges', '/api/admin/auth/bili/challenges/legacy-proof']) {
      expect(nginxRegexLocationMatches(auth, endpoint), endpoint).toBe(false);
    }
    expect(nginxRegexLocationMatches(auth, '/api/auth/bili/challenges')).toBe(true);
    expect(nginxRegexLocationMatches(auth, '/api/admin/bili-service/challenge/proof')).toBe(true);
    expect(nginxRegexLocationMatches(auth, '/api/admin/bili-service/challenge/proof/extra')).toBe(false);
    expect(nginxRegexLocationMatches(auth, '/api/admin/bili-service/status')).toBe(false);
    expect(nginxRegexLocationMatches(account, '/api/admin/bili-service/status')).toBe(false);
    expect('/api/admin/bili-service/status'.startsWith('/api/')).toBe(true);
    expect(exchange).toContain('limit_req zone=hosted_auth burst=5 nodelay;');
    expect(account).toContain('limit_req zone=hosted_account burst=10 nodelay;');
    expect(api).toContain('limit_req zone=hosted_api burst=30 nodelay;');
  });

  it('retains hot Nginx and journal logs for 30 days without embedding secrets', () => {
    if (!ingressFiles.every((path) => existsSync(resolve(projectRoot, path)))) return;
    const logrotate = readProjectFile('deploy/hosted/logrotate.conf');
    const journald = readProjectFile('deploy/hosted/journald.conf');
    expect(logrotate).toMatch(/\/var\/log\/nginx\/gift-panel-hosted\.access\.log/);
    expect(logrotate).toMatch(/\/var\/log\/nginx\/gift-panel-hosted\.error\.log/);
    expect(logrotate).toMatch(/^\s*daily\s*$/m);
    expect(logrotate).toMatch(/^\s*rotate 35\s*$/m);
    expect(logrotate).toMatch(/kill -USR1/);
    expect(logrotate).toMatch(/\/var\/log\/gift-panel-hosted\/app\.log\s*\{/);
    expect(logrotate).toMatch(/\/var\/log\/gift-panel-hosted\/app\.log\s*\{[^}]*copytruncate/s);
    expect(readProjectFile('goserver/cmd/hosted/main.go')).toContain('hostedLogMaxBytes = 256 * 1024 * 1024');
    expect(36 * 256 * 1024 * 1024).toBeLessThanOrEqual(10 * 1024 * 1024 * 1024);
    expect(journald).toMatch(/^\[Journal\]$/m);
    expect(logrotate).toMatch(/^\s*dateext\s*$/m);
    expect(logrotate).toMatch(/^\s*dateformat -%Y%m%d\s*$/m);
    expect(logrotate).not.toMatch(/^\s*notifempty\s*$/m);
    expect(journald).toMatch(/^MaxRetentionSec=35day$/m);
    expect(`${logrotate}\n${journald}`).not.toMatch(/(?:PASSWORD|TOKEN|AUTHORIZATION|COOKIE|PRIVATE[_ -]?KEY)\s*=/i);
  });

  it('ships fail-closed encrypted backup, archive, and restore artifacts', () => {
    expect(backupFiles.map((path) => existsSync(resolve(projectRoot, path))))
      .toEqual(backupFiles.map(() => true));
  });

  it('keeps backup and log artifacts secret, unique, checksummed, and append-only', () => {
    if (!backupFiles.every((path) => existsSync(resolve(projectRoot, path)))) return;
    const backup = readProjectFile('deploy/hosted/backup.sh');
    const archive = readProjectFile('deploy/hosted/archive-logs.sh');
    for (const source of [backup, archive]) {
      expect(source).toMatch(/^set -euo pipefail$/m);
      expect(source).toMatch(/^umask 077$/m);
      expect(source).toMatch(/\bflock\b/);
      expect(source).toMatch(/mktemp -d/);
      expect(source).toMatch(/trap ['"]cleanup['"] EXIT/);
      expect(source).toMatch(/date_bin=.*\$\{HOSTED_DATE_BIN:-date\}/);
      expect(source).toMatch(/"\$date_bin" -u ['"]?\+%Y%m%dT%H%M%SZ/);
      expect(source).toMatch(/random_suffix/);
      expect(source).toMatch(/sha256sum/);
      expect(source).toMatch(/\.complete/);
      expect(source).toMatch(/age[^\n]*--recipients-file/);
      expect(source).toMatch(/--endpoint cos\.ap-hongkong\.myqcloud\.com/);
      const cosCopies = source.split(/\r?\n/).filter((line) => line.includes('"$cos_bin" cp '));
      expect(cosCopies.length).toBeGreaterThan(0);
      for (const copy of cosCopies) {
        expect(copy).toContain('--disable-log');
        expect(copy).toContain('--process-log=false');
        expect(copy).toContain('--forbid-overwrite');
      }
      expect(source).not.toMatch(/age[^\n]*(?:--identity|-i\s)/);
      expect(source).not.toMatch(/(?:coscli|COS_BIN)[^\n]*(?:\brm\b|\bdelete\b)/i);
      expect(source).not.toMatch(/(?:MYSQL_PWD|PASSWORD)=[^"'$\n][^\n]*/i);
      const markerUpload = source.lastIndexOf('.complete');
      expect(markerUpload).toBeGreaterThan(source.lastIndexOf('.sha256'));
    }
    expect(backup).toContain('--single-transaction');
    expect(backup).toContain('--quick');
    expect(backup).toContain('--routines');
    expect(backup).toContain('--events');
    expect(backup).toContain('--hex-blob');
    expect(backup).toContain('hosted/backups/$period/');
    expect(backup.match(/\+%Y%m%dT%H%M%SZ %u %d %F/g)).toHaveLength(1);
    expect(backup).not.toMatch(/AGE_(?:IDENTITY|SECRET)|--decrypt/);

    expect(archive).toMatch(/-mmin \+43200/);
    expect(archive).not.toMatch(/-mtime \+30/);
    expect(archive).toMatch(/\.tar\.zst\.age/);
    expect(archive).toMatch(/manifest/);
    expect(archive).toContain('-name "gift-panel-hosted.access.log-${day_compact}.gz"');
    expect(archive).toMatch(/app_log_root=.*\$\{HOSTED_APP_LOG_ROOT:-\/var\/log\/gift-panel-hosted\}/);
    expect(archive).toContain('-name "app.log-${day_compact}.gz"');
    expect(archive).toMatch(/checkpoint/);
    expect(archive).toMatch(/app_rotation_date_host_local/);
    expect(archive).toMatch(/nginx_rotation_date_host_local/);
    expect(archive).toMatch(/host_timezone_observed/);
    expect(archive).not.toMatch(/archive_day_utc/);
    expect(archive).toMatch(/delivery=at-least-once/);
    expect(archive).toMatch(/while \[\[ "\$archive_day"/);
    expect(archive).not.toMatch(/journalctl|docker\s+logs|--unit|CONTAINER_TAG=|SYSLOG_IDENTIFIER=/);
    expect(archive).not.toContain("-name '*.journal~'");
    expect(archive).not.toContain('/var/log/journal');
    expect(archive).not.toMatch(/(?:cat|sed|head|tail)\s+[^\n]*\.log/);
    for (const source of [backup, archive, readProjectFile('deploy/hosted/restore-drill.sh')]) {
      expect(source).toMatch(/timeout_bin=.*HOSTED_TIMEOUT_BIN/);
      expect(source).toContain('--foreground');
    }

    const utcDayStart = Date.parse('2026-07-17T00:00:00Z');
    const asiaShanghaiLocalDayStart = Date.parse('2026-07-17T00:00:00+08:00');
    const documentedNginxHostTimezone = 'Asia/Shanghai';
    expect(documentedNginxHostTimezone).toBe('Asia/Shanghai');
    expect(new Date(asiaShanghaiLocalDayStart).toISOString()).toBe('2026-07-16T16:00:00.000Z');
    expect(asiaShanghaiLocalDayStart).toBe(utcDayStart - 8 * 60 * 60 * 1000);
    expect(asiaShanghaiLocalDayStart).not.toBe(utcDayStart);
  });

  it('defines lifecycle deletion separately from write-only production upload scripts', () => {
    if (!existsSync(resolve(projectRoot, 'deploy/hosted/cos-lifecycle.json'))) return;
    const lifecycle = JSON.parse(readProjectFile('deploy/hosted/cos-lifecycle.json')) as { Rules?: Array<Record<string, any>> };
    expect(lifecycle.Rules).toEqual([
      { ID: 'hosted-backup-daily', Status: 'Enabled', Filter: { Prefix: 'hosted/backups/daily/' }, Expiration: { Days: 7 } },
      { ID: 'hosted-backup-weekly', Status: 'Enabled', Filter: { Prefix: 'hosted/backups/weekly/' }, Expiration: { Days: 28 } },
      { ID: 'hosted-backup-monthly', Status: 'Enabled', Filter: { Prefix: 'hosted/backups/monthly/' }, Expiration: { Days: 183 } },
      { ID: 'hosted-log-archives', Status: 'Enabled', Filter: { Prefix: 'hosted/logs/' }, Expiration: { Days: 190 } },
      {
        ID: 'hosted-abort-incomplete-multipart',
        Status: 'Enabled',
        Filter: { Prefix: 'hosted/' },
        AbortIncompleteMultipartUpload: { DaysAfterInitiation: 1 },
      },
    ]);
    const uploadScripts = `${readProjectFile('deploy/hosted/backup.sh')}\n${readProjectFile('deploy/hosted/archive-logs.sh')}`;
    expect(uploadScripts).not.toMatch(/(?:lifecycle|delete-object|remove-object|\bcoscli\s+rm\b)/i);
  });

  it('schedules hardened single-instance backup and archive jobs without credentials', () => {
    if (!backupFiles.every((path) => existsSync(resolve(projectRoot, path)))) return;
    const backupService = readProjectFile('deploy/hosted/backup.service');
    const archiveService = readProjectFile('deploy/hosted/archive-logs.service');
    for (const [source, script] of [[backupService, 'backup.sh'], [archiveService, 'archive-logs.sh']] as const) {
      expect(source).toContain('Type=oneshot');
      expect(source).toContain('WorkingDirectory=/opt/gift-panel-hosted/current/deploy/hosted');
      expect(source).toContain(`ExecStart=/usr/bin/bash /opt/gift-panel-hosted/current/deploy/hosted/${script}`);
      expect(source).toContain('EnvironmentFile=/etc/gift-panel-hosted/env');
      expect(source).toContain('EnvironmentFile=/etc/gift-panel-hosted/backup.env');
      expect(source).toContain('ProtectSystem=strict');
      expect(source).toContain('PrivateTmp=true');
      expect(source).not.toMatch(/Environment=.*(?:PASSWORD|TOKEN|KEY|DSN)=/i);
    }
    expect(readProjectFile('deploy/hosted/backup.sh')).toContain(
      'HOSTED_COMPOSE_FILE:-/opt/gift-panel-hosted/current/deploy/hosted/docker-compose.yml',
    );
    expect(archiveService).toContain('StateDirectory=gift-panel-hosted-log-archive');
    expect(archiveService).toContain('StateDirectoryMode=0700');
    expect(archiveService).toContain('ReadWritePaths=/run/lock /var/lib/gift-panel-hosted-log-archive');
    expect(archiveService).toContain('ReadOnlyPaths=/var/log/gift-panel-hosted /var/log/nginx');
    expect(archiveService).toContain('Restart=on-failure');
    expect(archiveService).toContain('RestartSec=15min');
    expect(backupService).toContain('StateDirectory=gift-panel-hosted-backup');
    expect(backupService).toContain('StateDirectoryMode=0700');
    expect(backupService).toContain('Restart=on-failure');
    expect(backupService).toContain('RestartSec=15min');
    for (const source of [backupService, archiveService]) {
      expect(source).toMatch(/^TimeoutStartSec=\d+(?:min|s)$/m);
      expect(source).toMatch(/^TimeoutStopSec=\d+(?:min|s)$/m);
    }
    const backupTimer = readProjectFile('deploy/hosted/backup.timer');
    const archiveTimer = readProjectFile('deploy/hosted/archive-logs.timer');
    expect(backupTimer).toContain('OnCalendar=*-*-* 03:00:00 UTC');
    expect(archiveTimer).toContain('OnCalendar=*-*-* 04:00:00 UTC');
    for (const timer of [backupTimer, archiveTimer]) {
      expect(timer).toContain('Persistent=true');
      expect(timer).toMatch(/RandomizedDelaySec=\d+/);
    }
  });

  it('bounds restore cleanup to an explicitly resolved gift-panel-restore project and volume', () => {
    if (!existsSync(resolve(projectRoot, 'deploy/hosted/restore-drill.sh'))) return;
    const restore = readProjectFile('deploy/hosted/restore-drill.sh');
    expect(restore).toMatch(/^set -euo pipefail$/m);
    expect(restore).toMatch(/^umask 077$/m);
    expect(restore).toMatch(/mktemp -d/);
    expect(restore).toMatch(/chmod 0700 -- "\$work_dir"/);
    expect(restore).toMatch(/trap ['"]cleanup['"] EXIT/);
    expect(restore).toMatch(/age[^\n]*--decrypt[^\n]*--identity/);
    expect(restore).toMatch(/sha256sum[^\n]*--check/);
    expect(restore).toMatch(/--endpoint cos\.ap-hongkong\.myqcloud\.com/);
    const restoreCosCopies = restore.split(/\r?\n/).filter((line) => line.includes('"$cos_bin" cp '));
    expect(restoreCosCopies.length).toBeGreaterThan(0);
    for (const copy of restoreCosCopies) {
      expect(copy).toContain('--disable-log');
      expect(copy).toContain('--process-log=false');
      expect(copy).toContain('--forbid-overwrite');
    }
    expect(restore).toMatch(/gift-panel-restore-/);
    expect(restore).toMatch(/docker[^\n]*volume[^\n]*label=com\.docker\.compose\.project/);
    expect(restore).toMatch(/resolved_volume/);
    expect(restore).toMatch(/"\$resolved_volume" != "\$volume_name"/);
    expect(restore).toMatch(/down[^\n]*--volumes[^\n]*--remove-orphans/);
    expect(restore).toMatch(/\.complete/);
    expect(restore).toMatch(/completed_utc/);
    expect(restore).toMatch(/RPO/);
    expect(restore).toMatch(/RTO/);
    expect(restore).toMatch(/schema_migrations/);
    expect(restore).not.toMatch(/(?:rm\s+-rf|docker\s+(?:system|volume)\s+prune)/);
  });

  it('runs synthetic encrypted backups with unique keys, completion last, and exact temp cleanup', () => {
    const root = mkdtempSync(join(projectRoot, '.gift-panel-backup-contract-'));
    try {
      const fake = fakeBackupTools(root);
      const config = join(root, 'cos.yaml');
      const recipient = join(root, 'recipient.txt');
      const compose = join(root, 'compose.yml');
      const temporary = join(root, 'temporary');
      const state = join(root, 'state');
      mkdirSync(temporary);
      mkdirSync(state);
      writeFileSync(config, 'synthetic config without credentials\n');
      writeFileSync(recipient, 'age1syntheticrecipient\n');
      writeFileSync(compose, 'services: {}\n');
      const environment = {
        ...process.env,
        PATH: `${bashPath(fake.bin)}:/usr/bin:/bin`,
        FAKE_CALLS: bashPath(fake.calls),
        FAKE_COS: bashPath(fake.cos),
        FAKE_UNSAFE_VOLUME: '',
        FAKE_FAIL_UP: '',
        FAKE_COS_FAIL_SHA: '',
        FAKE_BACKUP_CALENDAR: '1',
        FAKE_ARCHIVE_CALENDAR: '',
        TMPDIR: bashPath(temporary),
        HOSTED_DOCKER_BIN: 'docker',
        HOSTED_DATE_BIN: 'hosted-date',
        HOSTED_COS_BIN: 'coscli',
        HOSTED_ZSTD_BIN: 'zstd',
        HOSTED_AGE_BIN: 'age',
        HOSTED_COMPOSE_FILE: bashPath(compose),
        HOSTED_COS_CONFIG_FILE: bashPath(config),
        HOSTED_COS_BUCKET: 'synthetic-backups-1250000000',
        HOSTED_COS_REGION: 'ap-hongkong',
        HOSTED_AGE_RECIPIENT_FILE: bashPath(recipient),
        HOSTED_BACKUP_LOCK_FILE: bashPath(join(root, 'backup.lock')),
        HOSTED_BACKUP_STATE_ROOT: bashPath(state),
      };
      const first = runBash('deploy/hosted/backup.sh', environment);
      const second = runBash('deploy/hosted/backup.sh', environment);
      expect(first.status, first.stderr).toBe(0);
      expect(second.status, second.stderr).toBe(0);
      const uploads = readFileSync(fake.calls, 'utf8').split(/\r?\n/).filter((line) => line.startsWith('coscli\t'));
      const daily = uploads.filter((line) => line.includes('/hosted/backups/daily/'));
      const weekly = uploads.filter((line) => line.includes('/hosted/backups/weekly/'));
      const monthly = uploads.filter((line) => line.includes('/hosted/backups/monthly/'));
      expect(daily).toHaveLength(3);
      expect(weekly).toHaveLength(3);
      expect(monthly).toHaveLength(3);
      expect(daily[0]).toMatch(/gift-panel-20261101-20261101T010203Z-[0-9a-f]{16}\.sql\.zst\.age/);
      for (let index = 0; index < daily.length; index += 3) {
        expect(daily[index]).not.toContain('.sha256');
        expect(daily[index + 1]).toContain('.sha256');
        expect(daily[index + 2]).toContain('.complete');
      }
      expect(readdirSync(temporary)).toEqual([]);
      expect(readFileSync(join(state, 'daily.next'), 'utf8')).toBe('2026-11-02\n');
      expect(readFileSync(join(state, 'weekly.next'), 'utf8')).toBe('2026-11-08\n');
      expect(readFileSync(join(state, 'monthly.next'), 'utf8')).toBe('2026-12-01\n');
      expect(readFileSync(fake.calls, 'utf8')).not.toMatch(/age1syntheticrecipient|synthetic-operator-identity-not-a-key/);

      writeFileSync(fake.calls, '');
      writeFileSync(join(state, 'daily.next'), '2026-11-01\n');
      const failedChecksumUpload = runBash('deploy/hosted/backup.sh', { ...environment, FAKE_COS_FAIL_SHA: '1' });
      expect(failedChecksumUpload.status).not.toBe(0);
      const failedCalls = readFileSync(fake.calls, 'utf8');
      expect(failedCalls).toContain('.sha256');
      expect(failedCalls).not.toContain('.complete');
      expect(readFileSync(join(state, 'daily.next'), 'utf8')).toBe('2026-11-01\n');
      expect(readdirSync(temporary)).toEqual([]);

      writeFileSync(fake.calls, '');
      const timedOut = runBash('deploy/hosted/backup.sh', {
        ...environment,
        FAKE_TIMEOUT_MATCH: 'docker compose',
        HOSTED_TIMEOUT_BIN: bashPath(join(fake.bin, 'timeout')),
      });
      expect(timedOut.status).toBe(124);
      expect(readFileSync(join(state, 'daily.next'), 'utf8')).toBe('2026-11-01\n');
      expect(readFileSync(fake.calls, 'utf8')).not.toContain('coscli\t');
    } finally {
      rmSync(root, { recursive: true, force: true });
    }
  });

  it('backfills independent backup periods and advances only each completed period checkpoint', () => {
    const root = mkdtempSync(join(projectRoot, '.gift-panel-backup-checkpoint-contract-'));
    try {
      const fake = fakeBackupTools(root);
      const config = join(root, 'cos.yaml');
      const recipient = join(root, 'recipient.txt');
      const compose = join(root, 'compose.yml');
      const temporary = join(root, 'temporary');
      const state = join(root, 'state');
      for (const directory of [temporary, state]) mkdirSync(directory);
      writeFileSync(config, 'synthetic config\n');
      writeFileSync(recipient, 'age1syntheticrecipient\n');
      writeFileSync(compose, 'services: {}\n');
      writeFileSync(join(state, 'daily.next'), '2026-10-30\n');
      writeFileSync(join(state, 'weekly.next'), '2026-10-25\n');
      writeFileSync(join(state, 'monthly.next'), '2026-10-01\n');
      const environment = {
        ...process.env,
        PATH: `${bashPath(fake.bin)}:/usr/bin:/bin`,
        FAKE_CALLS: bashPath(fake.calls),
        FAKE_COS: bashPath(fake.cos),
        FAKE_COS_FAIL_SHA: '',
        FAKE_COS_FAIL_DAY: 'weekly/gift-panel-20261025-',
        FAKE_BACKUP_CALENDAR: '1',
        FAKE_ARCHIVE_CALENDAR: '',
        TMPDIR: bashPath(temporary),
        HOSTED_DOCKER_BIN: 'docker',
        HOSTED_DATE_BIN: 'hosted-date',
        HOSTED_COS_BIN: 'coscli',
        HOSTED_ZSTD_BIN: 'zstd',
        HOSTED_AGE_BIN: 'age',
        HOSTED_COMPOSE_FILE: bashPath(compose),
        HOSTED_COS_CONFIG_FILE: bashPath(config),
        HOSTED_COS_BUCKET: 'synthetic-backups-1250000000',
        HOSTED_COS_REGION: 'ap-hongkong',
        HOSTED_AGE_RECIPIENT_FILE: bashPath(recipient),
        HOSTED_BACKUP_LOCK_FILE: bashPath(join(root, 'backup.lock')),
        HOSTED_BACKUP_STATE_ROOT: bashPath(state),
      };

      const interrupted = runBash('deploy/hosted/backup.sh', environment);
      expect(interrupted.status).not.toBe(0);
      expect(readFileSync(join(state, 'daily.next'), 'utf8')).toBe('2026-11-02\n');
      expect(readFileSync(join(state, 'weekly.next'), 'utf8')).toBe('2026-10-25\n');
      expect(readFileSync(join(state, 'monthly.next'), 'utf8')).toBe('2026-12-01\n');
      const interruptedCalls = readFileSync(fake.calls, 'utf8');
      for (const day of ['20261030', '20261031', '20261101']) expect(interruptedCalls).toContain(`daily/gift-panel-${day}-`);
      expect(interruptedCalls).toContain('weekly/gift-panel-20261025-');
      for (const day of ['20261001', '20261101']) expect(interruptedCalls).toContain(`monthly/gift-panel-${day}-`);

      writeFileSync(fake.calls, '');
      const retried = runBash('deploy/hosted/backup.sh', { ...environment, FAKE_COS_FAIL_DAY: '' });
      expect(retried.status, retried.stderr).toBe(0);
      expect(readFileSync(join(state, 'daily.next'), 'utf8')).toBe('2026-11-02\n');
      expect(readFileSync(join(state, 'weekly.next'), 'utf8')).toBe('2026-11-08\n');
      expect(readFileSync(join(state, 'monthly.next'), 'utf8')).toBe('2026-12-01\n');
      const retriedCalls = readFileSync(fake.calls, 'utf8');
      expect(retriedCalls).not.toContain('/daily/');
      expect(retriedCalls).not.toContain('/monthly/');
      for (const day of ['20261025', '20261101']) expect(retriedCalls).toContain(`weekly/gift-panel-${day}-`);
      expect(readdirSync(temporary)).toEqual([]);

      writeFileSync(fake.calls, '');
      writeFileSync(join(state, 'daily.next'), '2026-10-25\n');
      const staleCheckpoint = runBash('deploy/hosted/backup.sh', { ...environment, FAKE_COS_FAIL_DAY: '' });
      expect(staleCheckpoint.status).not.toBe(0);
      expect(readFileSync(join(state, 'daily.next'), 'utf8')).toBe('2026-10-25\n');
      expect(readFileSync(fake.calls, 'utf8')).not.toContain('/hosted/backups/');

      writeFileSync(fake.calls, '');
      writeFileSync(join(state, 'daily.next'), '2026-11-03\n');
      const futureCheckpoint = runBash('deploy/hosted/backup.sh', { ...environment, FAKE_COS_FAIL_DAY: '' });
      expect(futureCheckpoint.status).not.toBe(0);
      expect(readFileSync(join(state, 'daily.next'), 'utf8')).toBe('2026-11-03\n');
      expect(readFileSync(fake.calls, 'utf8')).not.toContain('/hosted/backups/');

      writeFileSync(fake.calls, '');
      writeFileSync(join(state, 'daily.next'), '2026-11-02\n');
      writeFileSync(join(state, 'weekly.next'), '2026-10-26\n');
      const wrongWeeklyCadence = runBash('deploy/hosted/backup.sh', { ...environment, FAKE_COS_FAIL_DAY: '' });
      expect(wrongWeeklyCadence.status).not.toBe(0);
      expect(readFileSync(join(state, 'weekly.next'), 'utf8')).toBe('2026-10-26\n');
      expect(readFileSync(fake.calls, 'utf8')).not.toContain('/hosted/backups/');

      writeFileSync(fake.calls, '');
      writeFileSync(join(state, 'weekly.next'), '2026-11-08\n');
      writeFileSync(join(state, 'monthly.next'), '2026-10-02\n');
      const wrongMonthlyCadence = runBash('deploy/hosted/backup.sh', { ...environment, FAKE_COS_FAIL_DAY: '' });
      expect(wrongMonthlyCadence.status).not.toBe(0);
      expect(readFileSync(join(state, 'monthly.next'), 'utf8')).toBe('2026-10-02\n');
      expect(readFileSync(fake.calls, 'utf8')).not.toContain('/hosted/backups/');
    } finally {
      rmSync(root, { recursive: true, force: true });
    }
  });

  it('archives only closed rotated logs older than 30 days and removes only its temp tree', () => {
    const root = mkdtempSync(join(projectRoot, '.gift-panel-log-contract-'));
    try {
      const fake = fakeBackupTools(root);
      const config = join(root, 'cos.yaml');
      const recipient = join(root, 'recipient.txt');
      const nginxLogs = join(root, 'nginx');
      const appLogs = join(root, 'app');
      const temporary = join(root, 'temporary');
      const state = join(root, 'state');
      for (const directory of [nginxLogs, appLogs, temporary, state]) mkdirSync(directory);
      writeFileSync(config, 'synthetic config\n');
      writeFileSync(recipient, 'age1syntheticrecipient\n');
      const oldNginx = join(nginxLogs, 'gift-panel-hosted.access.log-20260717.gz');
      const oldNginxError = join(nginxLogs, 'gift-panel-hosted.error.log-20260717.gz');
      const unrelatedNginx = join(nginxLogs, 'other-vhost.access.log-20260601.gz');
      const unrelatedJournal = join(appLogs, 'system.journal~');
      const active = join(nginxLogs, 'gift-panel-hosted.access.log');
      const recent = join(appLogs, 'app.log-20260816.gz');
      const rotatedApp = join(appLogs, 'app.log-20260717.gz');
      for (const path of [oldNginx, oldNginxError, unrelatedNginx, unrelatedJournal, active, recent, rotatedApp]) writeFileSync(path, `synthetic:${path}\n`);
      const oldDate = new Date(Date.now() - 32 * 24 * 60 * 60 * 1000);
      utimesSync(oldNginx, oldDate, oldDate);
      utimesSync(oldNginxError, oldDate, oldDate);
      utimesSync(unrelatedNginx, oldDate, oldDate);
      utimesSync(unrelatedJournal, oldDate, oldDate);
      utimesSync(rotatedApp, oldDate, oldDate);
      const result = runBash('deploy/hosted/archive-logs.sh', {
        ...process.env,
        PATH: `${bashPath(fake.bin)}:/usr/bin:/bin`,
        FAKE_CALLS: bashPath(fake.calls),
        FAKE_COS: bashPath(fake.cos),
        FAKE_UNSAFE_VOLUME: '',
        FAKE_FAIL_UP: '',
        FAKE_COS_FAIL_SHA: '',
        FAKE_BACKUP_CALENDAR: '',
        FAKE_ARCHIVE_CALENDAR: '1',
        FAKE_PROCESS_LOG_ROOT: bashPath(root),
        TMPDIR: bashPath(temporary),
        HOSTED_COS_BIN: 'coscli',
        HOSTED_ZSTD_BIN: 'zstd',
        HOSTED_AGE_BIN: 'age',
        HOSTED_DATE_BIN: 'hosted-date',
        HOSTED_COS_CONFIG_FILE: bashPath(config),
        HOSTED_COS_BUCKET: 'synthetic-backups-1250000000',
        HOSTED_COS_REGION: 'ap-hongkong',
        HOSTED_AGE_RECIPIENT_FILE: bashPath(recipient),
        HOSTED_NGINX_LOG_ROOT: bashPath(nginxLogs),
        HOSTED_APP_LOG_ROOT: bashPath(appLogs),
        HOSTED_ARCHIVE_LOCK_FILE: bashPath(join(root, 'archive.lock')),
        HOSTED_ARCHIVE_STATE_ROOT: bashPath(state),
      });
      expect(result.status, result.stderr).toBe(0);
      expect(result.stdout).toContain('files=3');
      expect(readFileSync(fake.calls, 'utf8')).not.toContain('journalctl');
      expect(readFileSync(join(state, 'next-day'), 'utf8')).toBe('2026-07-18\n');
      expect(existsSync(join(root, 'coscli_output'))).toBe(false);
      expect(readFileSync(fake.calls, 'utf8')).toMatch(/hosted\/logs\/gift-panel-logs-20260717-[0-9]{8}T[0-9]{6}Z-[0-9a-f]{16}\.tar\.zst\.age/);
      expect(existsSync(oldNginx)).toBe(true);
      expect(existsSync(oldNginxError)).toBe(true);
      expect(existsSync(unrelatedNginx)).toBe(true);
      expect(existsSync(unrelatedJournal)).toBe(true);
      expect(existsSync(active)).toBe(true);
      expect(existsSync(recent)).toBe(true);
      expect(existsSync(rotatedApp)).toBe(true);
      expect(readdirSync(temporary)).toEqual([]);

      rmSync(oldNginx);
      rmSync(oldNginxError);
      rmSync(rotatedApp);
      writeFileSync(join(state, 'next-day'), '2026-07-17\n');
      writeFileSync(fake.calls, '');
      const empty = runBash('deploy/hosted/archive-logs.sh', {
        ...process.env,
        PATH: `${bashPath(fake.bin)}:/usr/bin:/bin`,
        FAKE_CALLS: bashPath(fake.calls),
        FAKE_COS: bashPath(fake.cos),
        FAKE_COS_FAIL_SHA: '',
        FAKE_ARCHIVE_CALENDAR: '1',
        FAKE_BACKUP_CALENDAR: '',
        TMPDIR: bashPath(temporary),
        HOSTED_COS_BIN: 'coscli',
        HOSTED_ZSTD_BIN: 'zstd',
        HOSTED_AGE_BIN: 'age',
        HOSTED_DATE_BIN: 'hosted-date',
        HOSTED_COS_CONFIG_FILE: bashPath(config),
        HOSTED_COS_BUCKET: 'synthetic-backups-1250000000',
        HOSTED_COS_REGION: 'ap-hongkong',
        HOSTED_AGE_RECIPIENT_FILE: bashPath(recipient),
        HOSTED_NGINX_LOG_ROOT: bashPath(nginxLogs),
        HOSTED_APP_LOG_ROOT: bashPath(appLogs),
        HOSTED_ARCHIVE_LOCK_FILE: bashPath(join(root, 'archive.lock')),
        HOSTED_ARCHIVE_STATE_ROOT: bashPath(state),
      });
      expect(empty.status, empty.stderr).toBe(0);
      expect(empty.stdout).toContain('log_archive_skipped_empty_day=2026-07-17');
      expect(readFileSync(join(state, 'next-day'), 'utf8')).toBe('2026-07-18\n');
      expect(readFileSync(fake.calls, 'utf8')).not.toContain('coscli');

      writeFileSync(oldNginx, 'synthetic restored closed access log\n');
      writeFileSync(oldNginxError, 'synthetic restored closed error log\n');
      writeFileSync(rotatedApp, 'synthetic restored closed app log\n');
      utimesSync(oldNginx, oldDate, oldDate);
      utimesSync(oldNginxError, oldDate, oldDate);
      utimesSync(rotatedApp, oldDate, oldDate);
      rmSync(oldNginxError);
      writeFileSync(join(state, 'next-day'), '2026-07-17\n');
      writeFileSync(fake.calls, '');
      const incomplete = runBash('deploy/hosted/archive-logs.sh', {
        ...process.env,
        PATH: `${bashPath(fake.bin)}:/usr/bin:/bin`,
        FAKE_CALLS: bashPath(fake.calls),
        FAKE_COS: bashPath(fake.cos),
        FAKE_COS_FAIL_SHA: '',
        FAKE_ARCHIVE_CALENDAR: '1',
        FAKE_BACKUP_CALENDAR: '',
        TMPDIR: bashPath(temporary),
        HOSTED_COS_BIN: 'coscli',
        HOSTED_ZSTD_BIN: 'zstd',
        HOSTED_AGE_BIN: 'age',
        HOSTED_DATE_BIN: 'hosted-date',
        HOSTED_COS_CONFIG_FILE: bashPath(config),
        HOSTED_COS_BUCKET: 'synthetic-backups-1250000000',
        HOSTED_COS_REGION: 'ap-hongkong',
        HOSTED_AGE_RECIPIENT_FILE: bashPath(recipient),
        HOSTED_NGINX_LOG_ROOT: bashPath(nginxLogs),
        HOSTED_APP_LOG_ROOT: bashPath(appLogs),
        HOSTED_ARCHIVE_LOCK_FILE: bashPath(join(root, 'archive.lock')),
        HOSTED_ARCHIVE_STATE_ROOT: bashPath(state),
      });
      expect(incomplete.status).not.toBe(0);
      expect(incomplete.stderr).toContain('closed hosted rotation set is incomplete');
      expect(readFileSync(join(state, 'next-day'), 'utf8')).toBe('2026-07-17\n');
      expect(readFileSync(fake.calls, 'utf8')).not.toContain('.complete');
      writeFileSync(oldNginxError, 'synthetic restored closed error log\n');
      utimesSync(oldNginxError, oldDate, oldDate);

      writeFileSync(join(state, 'next-day'), '2026-07-17\n');
      writeFileSync(fake.calls, '');
      const timedOut = runBash('deploy/hosted/archive-logs.sh', {
        ...process.env,
        PATH: `${bashPath(fake.bin)}:/usr/bin:/bin`,
        FAKE_CALLS: bashPath(fake.calls),
        FAKE_COS: bashPath(fake.cos),
        FAKE_COS_FAIL_SHA: '',
        FAKE_ARCHIVE_CALENDAR: '1',
        FAKE_BACKUP_CALENDAR: '',
        FAKE_TIMEOUT_MATCH: 'zstd',
        HOSTED_TIMEOUT_BIN: bashPath(join(fake.bin, 'timeout')),
        TMPDIR: bashPath(temporary),
        HOSTED_COS_BIN: 'coscli',
        HOSTED_ZSTD_BIN: 'zstd',
        HOSTED_AGE_BIN: 'age',
        HOSTED_DATE_BIN: 'hosted-date',
        HOSTED_COS_CONFIG_FILE: bashPath(config),
        HOSTED_COS_BUCKET: 'synthetic-backups-1250000000',
        HOSTED_COS_REGION: 'ap-hongkong',
        HOSTED_AGE_RECIPIENT_FILE: bashPath(recipient),
        HOSTED_NGINX_LOG_ROOT: bashPath(nginxLogs),
        HOSTED_APP_LOG_ROOT: bashPath(appLogs),
        HOSTED_ARCHIVE_LOCK_FILE: bashPath(join(root, 'archive.lock')),
        HOSTED_ARCHIVE_STATE_ROOT: bashPath(state),
      });
      expect(timedOut.status).toBe(124);
      expect(readFileSync(join(state, 'next-day'), 'utf8')).toBe('2026-07-17\n');
      expect(existsSync(rotatedApp)).toBe(true);
      expect(readFileSync(fake.calls, 'utf8')).not.toContain('.complete');
    } finally {
      rmSync(root, { recursive: true, force: true });
    }
  });

  it('checkpoints each completed host-local rotation date, retries interruptions, and does not reprocess checkpointed dates', () => {
    const root = mkdtempSync(join(projectRoot, '.gift-panel-log-checkpoint-contract-'));
    try {
      const fake = fakeBackupTools(root);
      const config = join(root, 'cos.yaml');
      const recipient = join(root, 'recipient.txt');
      const nginxLogs = join(root, 'nginx');
      const appLogs = join(root, 'app');
      const temporary = join(root, 'temporary');
      const state = join(root, 'state');
      for (const directory of [nginxLogs, appLogs, temporary, state]) mkdirSync(directory);
      writeFileSync(config, 'synthetic config\n');
      writeFileSync(recipient, 'age1syntheticrecipient\n');
      for (const day of ['2026-07-14', '2026-07-15', '2026-07-16', '2026-07-17']) {
        const rotated = join(appLogs, `app.log-${day.replaceAll('-', '')}.gz`);
        writeFileSync(rotated, `${day} synthetic app log\n`);
        const oldDate = new Date(Date.now() - 32 * 24 * 60 * 60 * 1000);
        utimesSync(rotated, oldDate, oldDate);
        for (const kind of ['access', 'error']) {
          const nginxRotation = join(nginxLogs, `gift-panel-hosted.${kind}.log-${day.replaceAll('-', '')}.gz`);
          writeFileSync(nginxRotation, `${day} synthetic Nginx ${kind} log\n`);
          utimesSync(nginxRotation, oldDate, oldDate);
        }
      }
      const environment = {
        ...process.env,
        PATH: `${bashPath(fake.bin)}:/usr/bin:/bin`,
        FAKE_CALLS: bashPath(fake.calls),
        FAKE_COS: bashPath(fake.cos),
        FAKE_COS_FAIL_SHA: '',
        FAKE_COS_FAIL_DAY: '20260715',
        FAKE_ARCHIVE_CALENDAR: '1',
        FAKE_BACKUP_CALENDAR: '',
        FAKE_PROCESS_LOG_ROOT: bashPath(root),
        TMPDIR: bashPath(temporary),
        HOSTED_COS_BIN: 'coscli',
        HOSTED_ZSTD_BIN: 'zstd',
        HOSTED_AGE_BIN: 'age',
        HOSTED_DATE_BIN: 'hosted-date',
        HOSTED_COS_CONFIG_FILE: bashPath(config),
        HOSTED_COS_BUCKET: 'synthetic-backups-1250000000',
        HOSTED_COS_REGION: 'ap-hongkong',
        HOSTED_AGE_RECIPIENT_FILE: bashPath(recipient),
        HOSTED_NGINX_LOG_ROOT: bashPath(nginxLogs),
        HOSTED_APP_LOG_ROOT: bashPath(appLogs),
        HOSTED_ARCHIVE_LOCK_FILE: bashPath(join(root, 'archive.lock')),
        HOSTED_ARCHIVE_STATE_ROOT: bashPath(state),
      };

      const interrupted = runBash('deploy/hosted/archive-logs.sh', environment);
      expect(interrupted.status).not.toBe(0);
      expect(readFileSync(join(state, 'next-day'), 'utf8')).toBe('2026-07-15\n');
      const interruptedCalls = readFileSync(fake.calls, 'utf8');
      expect(interruptedCalls).toContain('gift-panel-logs-20260714-');
      expect(interruptedCalls).toContain('gift-panel-logs-20260715-');
      expect(interruptedCalls).not.toContain('gift-panel-logs-20260716-');
      expect(existsSync(join(appLogs, 'app.log-20260714.gz'))).toBe(true);
      expect(existsSync(join(appLogs, 'app.log-20260715.gz'))).toBe(true);
      expect(readdirSync(temporary)).toEqual([]);

      writeFileSync(fake.calls, '');
      const retried = runBash('deploy/hosted/archive-logs.sh', { ...environment, FAKE_COS_FAIL_DAY: '' });
      expect(retried.status, retried.stderr).toBe(0);
      expect(readFileSync(join(state, 'next-day'), 'utf8')).toBe('2026-07-18\n');
      const retryCalls = readFileSync(fake.calls, 'utf8');
      expect(retryCalls).not.toContain('gift-panel-logs-20260714-');
      for (const day of ['20260715', '20260716', '20260717']) {
        expect(retryCalls).toContain(`gift-panel-logs-${day}-`);
      }
      expect(readdirSync(appLogs)).toHaveLength(4);
      expect(readdirSync(temporary)).toEqual([]);

      writeFileSync(fake.calls, '');
      const idempotent = runBash('deploy/hosted/archive-logs.sh', { ...environment, FAKE_COS_FAIL_DAY: '' });
      expect(idempotent.status, idempotent.stderr).toBe(0);
      expect(readFileSync(fake.calls, 'utf8')).toBe('');
      expect(existsSync(join(root, 'coscli_output'))).toBe(false);
      expect(readdirSync(temporary)).toEqual([]);

      writeFileSync(join(state, 'next-day'), '2026-07-13\n');
      writeFileSync(fake.calls, '');
      const retentionGap = runBash('deploy/hosted/archive-logs.sh', { ...environment, FAKE_COS_FAIL_DAY: '' });
      expect(retentionGap.status).not.toBe(0);
      expect(retentionGap.stderr).toContain('archive checkpoint is older than retained logs');
      expect(readFileSync(fake.calls, 'utf8')).toBe('');
    } finally {
      rmSync(root, { recursive: true, force: true });
    }
  });

  it('restores a selected checksum-valid artifact and tears down only its resolved prefixed volume', () => {
    const root = mkdtempSync(join(projectRoot, '.gift-panel-restore-contract-'));
    try {
      const fake = fakeBackupTools(root);
      const config = join(root, 'cos.yaml');
      const identity = join(root, 'identity.txt');
      const reports = join(root, 'reports');
      const temporary = join(root, 'temporary');
      mkdirSync(reports);
      mkdirSync(temporary);
      writeFileSync(config, 'synthetic operator config\n');
      writeFileSync(identity, 'synthetic-operator-identity-not-a-key\n');
      const object = 'cos://synthetic-backups-1250000000/hosted/backups/daily/gift-panel-20260817-20260817T010203Z-0123456789abcdef.sql.zst.age';
      const stored = join(fake.cos, object.slice('cos://'.length));
      mkdirSync(resolve(stored, '..'), { recursive: true });
      const payload = Buffer.from('encrypted synthetic SQL');
      writeFileSync(stored, payload);
      const artifactHash = createHash('sha256').update(payload).digest('hex');
      const artifactName = object.split('/').at(-1) ?? '';
      const validChecksum = `${artifactHash}  ${artifactName}\n`;
      const validCompletion = [
        `artifact=${artifactName}`,
        'period=daily',
        'scheduled_utc=2026-08-17',
        'delivery=at-least-once',
        'completed_utc=20260817T010203Z',
        '',
      ].join('\n');
      writeFileSync(`${stored}.sha256`, validChecksum);
      writeFileSync(`${stored}.complete`, validCompletion);
      const environment = {
        ...process.env,
        PATH: `${bashPath(fake.bin)}:/usr/bin:/bin`,
        FAKE_CALLS: bashPath(fake.calls),
        FAKE_COS: bashPath(fake.cos),
        FAKE_UNSAFE_VOLUME: '',
        FAKE_FAIL_UP: '',
        FAKE_COS_FAIL_SHA: '',
        FAKE_BACKUP_CALENDAR: '',
        FAKE_ARCHIVE_CALENDAR: '',
        TMPDIR: bashPath(temporary),
        HOSTED_DOCKER_BIN: 'docker',
        HOSTED_COS_BIN: 'coscli',
        HOSTED_ZSTD_BIN: 'zstd',
        HOSTED_AGE_BIN: 'age',
        HOSTED_COS_CONFIG_FILE: bashPath(config),
        HOSTED_COS_REGION: 'ap-hongkong',
        HOSTED_AGE_IDENTITY_FILE: bashPath(identity),
        HOSTED_RESTORE_OBJECT: object,
        HOSTED_RESTORE_REPORT_ROOT: bashPath(reports),
      };
      const result = runBash('deploy/hosted/restore-drill.sh', environment);
      expect(result.status, result.stderr).toBe(0);
      const calls = readFileSync(fake.calls, 'utf8');
      expect(calls).toMatch(/docker\tvolume ls --filter label=com\.docker\.compose\.project=gift-panel-restore-/);
      expect(calls).toMatch(/docker\tcompose -p gift-panel-restore-[^\s]+ -f [^\s]+ down --volumes --remove-orphans/);
      expect(readdirSync(reports)).toHaveLength(1);
      const successfulReport = readdirSync(reports)[0];
      expect(readFileSync(join(reports, successfulReport), 'utf8')).toMatch(/RPO_timestamp_utc=20260817T010203Z\nRTO_seconds=\d+/);
      expect(readdirSync(temporary)).toEqual([]);

      for (const malformedChecksum of [
        `${validChecksum}${validChecksum}`,
        `${artifactHash}  ./${artifactName}\n`,
        `${'g'.repeat(64)}  ${artifactName}\n`,
      ]) {
        writeFileSync(fake.calls, '');
        writeFileSync(`${stored}.sha256`, malformedChecksum);
        const malformed = runBash('deploy/hosted/restore-drill.sh', environment);
        expect(malformed.status).not.toBe(0);
        expect(readFileSync(fake.calls, 'utf8')).not.toContain('docker\t');
        expect(readdirSync(reports)).toEqual([successfulReport]);
        expect(readdirSync(temporary)).toEqual([]);
      }
      writeFileSync(`${stored}.sha256`, validChecksum);

      writeFileSync(fake.calls, '');
      const partialStartup = runBash('deploy/hosted/restore-drill.sh', { ...environment, FAKE_FAIL_UP: '1' });
      expect(partialStartup.status).not.toBe(0);
      expect(readFileSync(fake.calls, 'utf8')).toContain('down --volumes --remove-orphans');
      expect(readdirSync(temporary)).toEqual([]);

      writeFileSync(fake.calls, '');
      rmSync(`${stored}.complete`);
      const incomplete = runBash('deploy/hosted/restore-drill.sh', environment);
      expect(incomplete.status).not.toBe(0);
      expect(readFileSync(fake.calls, 'utf8')).not.toContain('docker\t');
      expect(readdirSync(reports)).toEqual([successfulReport]);
      expect(readdirSync(temporary)).toEqual([]);
      writeFileSync(`${stored}.complete`, validCompletion);

      writeFileSync(fake.calls, '');
      const unsafe = runBash('deploy/hosted/restore-drill.sh', { ...environment, FAKE_UNSAFE_VOLUME: '1' });
      expect(unsafe.status).not.toBe(0);
      const unsafeCalls = readFileSync(fake.calls, 'utf8');
      expect(unsafeCalls).not.toContain('down --volumes --remove-orphans');
      expect(unsafeCalls.match(/docker\tvolume ls/g)).toHaveLength(2);
      expect(readdirSync(reports)).toEqual([successfulReport]);
      const remediationDirectories = readdirSync(temporary);
      expect(remediationDirectories).toHaveLength(1);
      const remediationDirectory = join(temporary, remediationDirectories[0]);
      expect(readdirSync(remediationDirectory)).toContain('docker-compose.yml');
      expect(unsafe.stderr).toContain(`restore cleanup failed; retained remediation directory: ${bashPath(remediationDirectory)}`);
      expect(unsafe.stderr).toContain('disposable restore project may still be running');
    } finally {
      rmSync(root, { recursive: true, force: true });
    }
  });
});

describe('hosted operations runbook and private monitoring', () => {
  const procedureHeadings = [
    'Provisioning',
    'DNS and TLS',
    'Firewall',
    'Secrets file modes',
    'Administrator initialization',
    'Bilibili service credential',
    'Deploy',
    'Database migration',
    'Canary check',
    'Rollback',
    'Application key rotation',
    'HMAC key rotation',
    'Encryption key rotation',
    'SMTP rotation',
    'COS rotation',
    'TLS rotation',
    'Backup restore',
    'Service-account compromise',
    'Bilibili breaker',
    'Disk full',
    'Account disable',
    'Server decommission',
  ];

  it('ships the operations runbook, health check, and pilot checklist', () => {
    expect(operationsFiles.map((path) => existsSync(resolve(projectRoot, path))))
      .toEqual(operationsFiles.map(() => true));
  });

  it('documents executable deploy, rollback, rotation, and incident procedures', () => {
    const readme = readProjectFile('deploy/hosted/README.md');
    for (const heading of procedureHeadings) {
      expect(readme, heading).toContain(heading);
    }
    expect(readme).toContain('image digest');
    expect(readme).toContain('migration head');
    expect(readme).toContain('backup object');
    expect(readme).toContain('checksum');
    expect(readme).toContain('health output');
    expect(readme).toContain('smoke-test');
    expect(readme).toContain('previous digest');
    expect(readme).toMatch(/never reverses? an applied schema destructively/i);
    expect(readme).toContain('http://127.0.0.1:12500/healthz');
    expect(readme).toContain('http://127.0.0.1:12500/internal/metrics');
    expect(readme).not.toContain('proxy_pass');
    expect(readme).not.toMatch(/(?:PASSWORD|TOKEN|COOKIE|PRIVATE[_ -]?KEY)\s*=\s*\S+/i);
  });

  it('documents email-only administrator initialization and private token plus TOTP confirmation', () => {
    const readme = readProjectFile('deploy/hosted/README.md');
    const initialization = readme.match(/^## Administrator initialization\s*$([\s\S]*?)(?=^## )/m)?.[1] ?? '';

    expectSafeAdministratorInitialization(initialization);
  });

  it.each([
    ['an unquoted TOTP assignment', 'TOTP_CODE=123456'],
    ['an unquoted handoff-token assignment', 'HANDOFF_TOKEN=literal-token'],
    ['a literal TOTP in a confirmation payload', `printf '{"totp":"123456"}'`],
    ['a literal handoff token in a confirmation payload', `printf '{"handoffToken":"literal-token"}'`],
  ])('rejects %s even when the required variable flow remains', (_name, unsafeFragment) => {
    const readme = readProjectFile('deploy/hosted/README.md');
    const initialization = readme.match(/^## Administrator initialization\s*$([\s\S]*?)(?=^## )/m)?.[1] ?? '';

    expect(() => expectSafeAdministratorInitialization(`${initialization}\n${unsafeFragment}`)).toThrow();
  });

  it('keeps health-check private, fail-closed, and free of secret output', () => {
    const script = readProjectFile('deploy/hosted/health-check.sh');
    expect(script).toMatch(/^set -euo pipefail$/m);
    expect(script).toMatch(/^umask 077$/m);
    expect(script).toContain('127.0.0.1:12500/healthz');
    expect(script).not.toContain('/internal/metrics');
    expect(script).toMatch(/df_bin=.*HOSTED_DF_BIN/);
    expect(script).toMatch(/docker_bin=.*HOSTED_DOCKER_BIN/);
    expect(script).toMatch(/openssl_bin=.*HOSTED_OPENSSL_BIN/);
    expect(script).toMatch(/curl_bin=.*HOSTED_CURL_BIN/);
    expect(script).toContain('HOSTED_BACKUP_STATE_ROOT');
    expect(script).toContain('HOSTED_TLS_CERTIFICATE');
    expect(script).toContain('HOSTED_ARCHIVE_STATE_ROOT');
    expect(script).toMatch(/exit 10/);
    expect(script).toMatch(/exit 11/);
    expect(script).toMatch(/exit 12/);
    expect(script).toMatch(/exit 13/);
    expect(script).toMatch(/exit 14/);
    expect(script).toMatch(/exit 15/);
    expect(script).not.toMatch(/(?:PASSWORD|TOKEN|COOKIE|PRIVATE[_ -]?KEY)\s*=/);
    expect(script).not.toMatch(/echo[^\n]*(?:MYSQL_PWD|DSN|COOKIE|TOKEN)/i);
  });

  it('records seven-day go/no-go evidence without tenant identifiers', () => {
    const checklist = readProjectFile('docs/operations/hosted-pilot-checklist.md');
    expect(checklist).toContain('China Telecom');
    expect(checklist).toContain('China Unicom');
    expect(checklist).toContain('China Mobile');
    expect(checklist).toContain('p95');
    expect(checklist).toContain('500 ms');
    expect(checklist).toContain('70%');
    expect(checklist).toContain('80%');
    expect(checklist).toContain('90 days');
    expect(checklist).toContain('go/no-go');
    expect(checklist).not.toMatch(/\b(?:uid|cookie|nickname)\b/i);
  });

  it('creates a health-check temporary root when the project cache parent is fresh', () => {
    const freshProjectRoot = mkdtempSync(join(tmpdir(), 'hosted-health-project-'));
    try {
      const cacheRoot = join(freshProjectRoot, '.cache');
      expect(existsSync(cacheRoot)).toBe(false);
      const root = createHostedHealthRoot(freshProjectRoot);
      expect(existsSync(cacheRoot)).toBe(true);
      expect(root.startsWith(`${cacheRoot}\\`) || root.startsWith(`${cacheRoot}/`)).toBe(true);
    } finally {
      rmSync(freshProjectRoot, { recursive: true, force: true });
    }
  });

  it('returns stable health-check codes for loopback, disk, compose, backup, cert, and archive failures', () => {
    const root = createHostedHealthRoot(projectRoot);
    try {
      const fake = fakeBackupTools(root);
      const backupState = join(root, 'backup-state');
      const archiveState = join(root, 'archive-state');
      const certFile = join(root, 'tls.crt');
      mkdirSync(backupState, { recursive: true });
      mkdirSync(archiveState, { recursive: true });
      writeFileSync(join(backupState, 'daily.next'), '2026-08-19\n');
      writeFileSync(join(archiveState, 'next-day'), '2026-07-18\n');
      writeFileSync(certFile, 'placeholder-cert\n');
      const composeFile = resolve(projectRoot, 'deploy/hosted/docker-compose.yml');
      writeFileSync(join(fake.bin, 'hosted-curl'), `#!/usr/bin/env bash
set -euo pipefail
printf 'curl\\t%s\\n' "$*" >>"$FAKE_CALLS"
[[ "\${FAKE_HEALTH_FAIL:-}" == 1 ]] && exit 22
printf '%s\\n' '{"status":"ok"}'
`, 'utf8');
      writeFileSync(join(fake.bin, 'hosted-df'), `#!/usr/bin/env bash
set -euo pipefail
printf '%s\\n' 'Filesystem 1024-blocks Used Available Capacity Mounted on'
printf '%s\\n' "/dev/sda1 100 80 20 \${FAKE_DISK_PERCENT:-10}% /"
`, 'utf8');
      writeFileSync(join(fake.bin, 'hosted-docker'), `#!/usr/bin/env bash
set -euo pipefail
printf 'docker\\t%s\\n' "$*" >>"$FAKE_CALLS"
[[ "\${FAKE_COMPOSE_FAIL:-}" == 1 ]] && exit 1
printf '%s\\n' 'mysql healthy'
printf '%s\\n' 'app healthy'
`, 'utf8');
      writeFileSync(join(fake.bin, 'hosted-openssl'), `#!/usr/bin/env bash
set -euo pipefail
printf 'openssl\\t%s\\n' "$*" >>"$FAKE_CALLS"
printf '%s\\n' "notAfter=\${FAKE_CERT_NOT_AFTER:-Aug 18 00:00:00 2027 GMT}"
`, 'utf8');
      writeFileSync(join(fake.bin, 'hosted-health-date'), `#!/usr/bin/env bash
set -euo pipefail
case "$*" in
  '-u +%F') printf '%s\\n' '2026-08-18'; exit 0;;
  '-u +%s') printf '%s\\n' '1787001600'; exit 0;;
  '-u -d 2026-08-18 - 34 days +%F') printf '%s\\n' '2026-07-15'; exit 0;;
  '-u -d Aug 18 00:00:00 2027 GMT +%s') printf '%s\\n' '1818547200'; exit 0;;
  '-u -d Aug 19 00:00:00 2026 GMT +%s') printf '%s\\n' '1787088000'; exit 0;;
esac
exit 4
`, 'utf8');
      chmodSync(join(fake.bin, 'hosted-curl'), 0o755);
      chmodSync(join(fake.bin, 'hosted-df'), 0o755);
      chmodSync(join(fake.bin, 'hosted-docker'), 0o755);
      chmodSync(join(fake.bin, 'hosted-openssl'), 0o755);
      chmodSync(join(fake.bin, 'hosted-health-date'), 0o755);

      const environment: NodeJS.ProcessEnv = {
        PATH: `${bashPath(fake.bin)}:/usr/bin:/bin`,
        FAKE_CALLS: bashPath(fake.calls),
        HOSTED_CURL_BIN: bashPath(join(fake.bin, 'hosted-curl')),
        HOSTED_DF_BIN: bashPath(join(fake.bin, 'hosted-df')),
        HOSTED_DOCKER_BIN: bashPath(join(fake.bin, 'hosted-docker')),
        HOSTED_OPENSSL_BIN: bashPath(join(fake.bin, 'hosted-openssl')),
        HOSTED_DATE_BIN: bashPath(join(fake.bin, 'hosted-health-date')),
        HOSTED_COMPOSE_FILE: bashPath(composeFile),
        HOSTED_BACKUP_STATE_ROOT: bashPath(backupState),
        HOSTED_TLS_CERTIFICATE: bashPath(certFile),
        HOSTED_ARCHIVE_STATE_ROOT: bashPath(archiveState),
        HOSTED_HEALTH_URL: 'http://127.0.0.1:12500/healthz',
        HOSTED_DISK_PATH: '/',
      };
      writeFileSync(fake.calls, '');
      const healthy = runBash('deploy/hosted/health-check.sh', environment);
      expect(healthy.status, healthy.stderr).toBe(0);
      expect(healthy.stdout).not.toMatch(/password|cookie|token|dsn/i);

      const healthFail = runBash('deploy/hosted/health-check.sh', { ...environment, FAKE_HEALTH_FAIL: '1' });
      expect(healthFail.status).toBe(10);
      const diskFail = runBash('deploy/hosted/health-check.sh', { ...environment, FAKE_DISK_PERCENT: '90' });
      expect(diskFail.status).toBe(11);
      const composeFail = runBash('deploy/hosted/health-check.sh', { ...environment, FAKE_COMPOSE_FAIL: '1' });
      expect(composeFail.status).toBe(12);
      writeFileSync(join(backupState, 'daily.next'), '2026-07-01\n');
      const backupFail = runBash('deploy/hosted/health-check.sh', environment);
      expect(backupFail.status).toBe(13);
      writeFileSync(join(backupState, 'daily.next'), '2026-08-19\n');
      const certFail = runBash('deploy/hosted/health-check.sh', {
        ...environment,
        FAKE_CERT_NOT_AFTER: 'Aug 19 00:00:00 2026 GMT',
      });
      expect(certFail.status).toBe(14);
      writeFileSync(join(archiveState, 'next-day'), '2026-06-01\n');
      const archiveFail = runBash('deploy/hosted/health-check.sh', environment);
      expect(archiveFail.status).toBe(15);
    } finally {
      rmSync(root, { recursive: true, force: true });
    }
  });
});
