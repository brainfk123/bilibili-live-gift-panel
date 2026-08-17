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
] as const;

function readProjectFile(path: string): string {
  return readFileSync(resolve(projectRoot, path), 'utf8');
}

function composeDocument(): Record<string, any> {
  return parse(readProjectFile('deploy/hosted/docker-compose.yml')) as Record<string, any>;
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
});
