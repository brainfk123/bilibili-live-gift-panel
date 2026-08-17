import { createHash } from 'node:crypto';
import { existsSync, readFileSync, readdirSync, statSync } from 'node:fs';
import { resolve } from 'node:path';
import { fileURLToPath } from 'node:url';
import { parse } from 'yaml';
import { describe, expect, it } from 'vitest';

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

function readProjectFile(path: string): string {
  return readFileSync(resolve(projectRoot, path), 'utf8');
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
    expect(app.healthcheck?.test?.[0]).toBe('CMD-SHELL');
    expect(app.depends_on?.mysql?.condition).toBe('service_healthy');

    expect(mysql.mem_limit).toBe('1280m');
    expect(mysql.command).toEqual(expect.arrayContaining([
      '--skip-name-resolve',
      '--character-set-server=utf8mb4',
      '--innodb-buffer-pool-size=536870912',
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
    expect(compose.services.app.secrets).toEqual(expect.arrayContaining([
      'mysql_dsn', 'encryption_key', 'hmac_key', 'admin_csrf_token', 'smtp_password',
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

    expect(calls).toHaveLength(6);
    expect(manifests[1]).toEqual(manifests[0]);
    const buildCalls = calls.filter((call) => call[1][0] === 'buildx' && call[1][1] === 'build');
    expect(buildCalls).toHaveLength(2);
    expect(buildCalls[0][0]).toBe('docker');
    expect(buildCalls[0][1]).toEqual([
      'buildx', 'build', '--output', 'type=docker,rewrite-timestamp=true', '--provenance=false', '--sbom=false',
      '--platform', 'linux/amd64', '--build-arg', 'SOURCE_DATE_EPOCH=0',
      '--file', 'deploy/hosted/Dockerfile', '--tag', 'gift-panel-hosted:test', '.',
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
      expect(args).toContain('type=docker,rewrite-timestamp=true');
      expect(args).toContain(`gift-panel-hosted:repro-contract-${index === 0 ? 'a' : 'b'}`);
    }
    expect(calls.filter((args) => args[0] === 'image' && args[1] === 'rm')).toEqual([
      ['image', 'rm', 'gift-panel-hosted:repro-contract-b'],
      ['image', 'rm', 'gift-panel-hosted:repro-contract-a'],
    ]);
  });

  it('uses a credential-free systemd Compose lifecycle with a bounded stop', () => {
    if (!existsSync(resolve(projectRoot, 'deploy/hosted/gift-panel-hosted.service'))) {
      expect(existsSync(resolve(projectRoot, 'deploy/hosted/gift-panel-hosted.service'))).toBe(true);
      return;
    }
    const service = readProjectFile('deploy/hosted/gift-panel-hosted.service');
    expect(service).toContain('EnvironmentFile=/etc/gift-panel-hosted/env');
    expect(service).toContain('docker compose up -d --remove-orphans');
    expect(service).toContain('docker compose stop -t 30');
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
    expect(config).toContain('map $uri $hosted_route');
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
    for (const endpoint of ['/api/admin/bili-service/challenge', '/api/admin/bili-service/replace']) {
      expect(nginxRegexLocationMatches(auth, endpoint), endpoint).toBe(true);
    }
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
    expect(logrotate).toMatch(/^\s*rotate 30\s*$/m);
    expect(logrotate).toMatch(/kill -USR1/);
    expect(journald).toMatch(/^\[Journal\]$/m);
    expect(journald).toMatch(/^MaxRetentionSec=30day$/m);
    expect(`${logrotate}\n${journald}`).not.toMatch(/(?:PASSWORD|TOKEN|AUTHORIZATION|COOKIE|PRIVATE[_ -]?KEY)\s*=/i);
  });
});
