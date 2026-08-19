import { execFileSync } from 'node:child_process';
import {
  chmodSync,
  copyFileSync,
  lstatSync,
  mkdirSync,
  mkdtempSync,
  readdirSync,
  rmSync,
  utimesSync,
} from 'node:fs';
import { tmpdir } from 'node:os';
import { randomUUID } from 'node:crypto';
import { dirname, join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

const scriptPath = fileURLToPath(import.meta.url);
const defaultProjectRoot = resolve(dirname(scriptPath), '..');
const normalizedTime = new Date(0);
const minimumBuildx = [0, 13, 0];
const minimumBuildKit = [0, 13, 0];

function copyDeterministic(source, destination, include = () => true, relative = '') {
  const sourcePath = relative ? join(source, relative) : source;
  const destinationPath = relative ? join(destination, relative) : destination;
  const stats = lstatSync(sourcePath);
  if (stats.isSymbolicLink()) throw new Error(`Hosted build inputs cannot contain symlinks: ${sourcePath}`);
  if (stats.isDirectory()) {
    mkdirSync(destinationPath, { recursive: true, mode: 0o755 });
    for (const entry of readdirSync(sourcePath).sort()) {
      copyDeterministic(source, destination, include, relative ? join(relative, entry) : entry);
    }
    chmodSync(destinationPath, 0o755);
    utimesSync(destinationPath, normalizedTime, normalizedTime);
    return;
  }
  if (!stats.isFile()) throw new Error(`Unsupported hosted build input: ${sourcePath}`);
  if (!include(relative.replaceAll('\\', '/'))) return;
  mkdirSync(dirname(destinationPath), { recursive: true, mode: 0o755 });
  copyFileSync(sourcePath, destinationPath);
  chmodSync(destinationPath, 0o644);
  utimesSync(destinationPath, normalizedTime, normalizedTime);
}

function copyProjectPath(projectRoot, contextRoot, path, include) {
  copyDeterministic(join(projectRoot, path), join(contextRoot, path), include);
}

function prepareHostedContext(projectRoot, contextRoot) {
  for (const path of [
    'package.json',
    'package-lock.json',
    'hosted.html',
    'obs.html',
    'vite.hosted.config.ts',
    'src/gameplay-templates.ts',
    'src/display-themes.ts',
    'src/format.ts',
    'src/types.ts',
    'src/duration.ts',
    'src/gift-rule-operations.ts',
    'goserver/go.mod',
    'goserver/go.sum',
    'goserver/cmd/hosted/main.go',
    'deploy/hosted/Dockerfile',
  ]) {
    copyProjectPath(projectRoot, contextRoot, path);
  }
  copyProjectPath(projectRoot, contextRoot, 'src/hosted');
  copyProjectPath(projectRoot, contextRoot, 'goserver/internal', (path) =>
    (path.endsWith('.go') && !path.endsWith('_test.go')) || path.endsWith('.sql'));
}

function outputText(value) {
  if (typeof value === 'string') return value;
  if (value == null) return '';
  return value.toString('utf8');
}

function versionAtLeast(actual, minimum) {
  for (let index = 0; index < minimum.length; index += 1) {
    if (actual[index] > minimum[index]) return true;
    if (actual[index] < minimum[index]) return false;
  }
  return true;
}

function requireVersion(output, pattern, minimum, name) {
  const match = output.match(pattern);
  const actual = match?.slice(1, 4).map((part) => Number.parseInt(part, 10));
  if (!actual || actual.some((part) => !Number.isSafeInteger(part)) || !versionAtLeast(actual, minimum)) {
    throw new Error(`${name} ${minimum.join('.')} or newer is required for reproducible hosted images`);
  }
}

function runDocker(run, args, options = {}) {
  return run('docker', args, {
    encoding: 'utf8',
    windowsHide: true,
    ...options,
  });
}

function ensureBuildCapabilities(run) {
  let buildxOutput;
  try {
    buildxOutput = outputText(runDocker(run, ['buildx', 'version'], { stdio: ['ignore', 'pipe', 'pipe'] }));
  } catch {
    throw new Error(`Docker Buildx ${minimumBuildx.join('.')} or newer is required for reproducible hosted images`);
  }
  requireVersion(buildxOutput, /\bv?(\d+)\.(\d+)\.(\d+)\b/, minimumBuildx, 'Docker Buildx');

  let builderOutput;
  try {
    builderOutput = outputText(runDocker(run, ['buildx', 'inspect', '--bootstrap'], { stdio: ['ignore', 'pipe', 'pipe'] }));
  } catch {
    throw new Error(`BuildKit ${minimumBuildKit.join('.')} or newer is required for reproducible hosted images`);
  }
  requireVersion(builderOutput, /BuildKit(?:\s+version)?:\s*v?(\d+)\.(\d+)\.(\d+)\b/i, minimumBuildKit, 'BuildKit');
}

function buildHostedImage(projectRoot, run, tag, noCache) {
  const contextRoot = mkdtempSync(join(tmpdir(), 'gift-panel-hosted-context-'));
  const archivePath = join(contextRoot, 'hosted-image.tar');
  const exporterPath = archivePath.replaceAll('\\', '/');
  try {
    prepareHostedContext(projectRoot, contextRoot);
    const args = [
      'buildx', 'build', '--output', `type=docker,dest=${exporterPath},rewrite-timestamp=true`, '--provenance=false', '--sbom=false',
      '--platform', 'linux/amd64', '--build-arg', 'SOURCE_DATE_EPOCH=0',
    ];
    if (noCache) args.push('--no-cache', '--pull');
    args.push('--file', 'deploy/hosted/Dockerfile', '--tag', tag, '.');
    runDocker(run, args, {
      cwd: contextRoot,
      env: { ...process.env, DOCKER_BUILDKIT: '1', SOURCE_DATE_EPOCH: '0' },
      stdio: 'inherit',
    });
    runDocker(run, ['load', '--input', archivePath], { stdio: 'inherit' });
  } finally {
    rmSync(contextRoot, { recursive: true, force: true });
  }
}

export function buildHostedServer(options = {}) {
  const projectRoot = resolve(options.projectRoot ?? defaultProjectRoot);
  const run = options.run ?? execFileSync;
  ensureBuildCapabilities(run);
  buildHostedImage(projectRoot, run, 'gift-panel-hosted:test', false);
}

export function verifyHostedReproducibility(options = {}) {
  const projectRoot = resolve(options.projectRoot ?? defaultProjectRoot);
  const run = options.run ?? execFileSync;
  const nonce = options.nonce ?? randomUUID();
  if (!/^[a-z0-9][a-z0-9_.-]{0,63}$/i.test(nonce)) {
    throw new Error('Hosted reproducibility nonce is invalid');
  }
  ensureBuildCapabilities(run);
  const tags = [`gift-panel-hosted:repro-${nonce}-a`, `gift-panel-hosted:repro-${nonce}-b`];
  const createdTags = [];
  const imageIDs = [];
  try {
    for (const tag of tags) {
      buildHostedImage(projectRoot, run, tag, true);
      createdTags.push(tag);
      const imageID = outputText(runDocker(run, ['image', 'inspect', '--format', '{{.Id}}', tag], {
        stdio: ['ignore', 'pipe', 'pipe'],
      })).trim();
      if (!/^sha256:[0-9a-f]+$/i.test(imageID)) {
        throw new Error('Docker returned an invalid hosted image ID');
      }
      imageIDs.push(imageID);
    }
    if (imageIDs[0] !== imageIDs[1]) {
      throw new Error(`Hosted image is not reproducible: ${imageIDs[0]} != ${imageIDs[1]}`);
    }
    return imageIDs[0];
  } finally {
    for (const tag of createdTags.reverse()) {
      try {
        runDocker(run, ['image', 'rm', tag], { stdio: 'ignore' });
      } catch {
        // The exact one-shot verification tag may already have been removed.
      }
    }
  }
}

if (process.argv[1] && resolve(process.argv[1]) === resolve(scriptPath)) {
  if (process.argv[2] === '--verify-reproducible') {
    const imageID = verifyHostedReproducibility();
    process.stdout.write(`Hosted image reproducibility verified: ${imageID}\n`);
  } else {
    buildHostedServer();
  }
}
