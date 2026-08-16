import { execFileSync } from 'node:child_process';
import { appendFileSync, copyFileSync, mkdirSync, mkdtempSync, rmSync, statSync } from 'node:fs';
import { dirname, join } from 'node:path';
import { tmpdir } from 'node:os';
import { fileURLToPath } from 'node:url';

const root = join(dirname(fileURLToPath(import.meta.url)), '..');
const localBuild = process.env.GIFT_PANEL_LOCAL_BUILD === '1';
const targets = [
  { command: './cmd/server', output: 'gift-panel-update-api-linux-amd64', ldflags: '-s -w' },
  { command: './cmd/mirror', output: 'gift-panel-release-mirror-linux-amd64', ldflags: (commit) => `-s -w -X main.buildCommit=${commit}` },
];

function git(args) {
  return execFileSync('git', ['-C', root, ...args], { encoding: 'utf8' }).trim();
}

function reviewedCommit() {
  const commit = git(['rev-parse', '--verify', 'HEAD']);
  if (!/^[0-9a-f]{40}$/.test(commit)) throw new Error('deployment build requires an exact reviewed Git commit');
  if (git(['status', '--porcelain', '--untracked-files=no']) !== '') throw new Error('deployment build requires a clean reviewed Git commit');
  return commit;
}

function build(sourceRoot, outputRoot, commit) {
  for (const target of targets) {
    const output = join(outputRoot, target.output);
    mkdirSync(dirname(output), { recursive: true });
    const ldflags = typeof target.ldflags === 'function' ? target.ldflags(commit) : target.ldflags;
    execFileSync('go', ['build', '-trimpath', '-buildvcs=false', '-ldflags', ldflags, '-o', output, target.command], {
      cwd: join(sourceRoot, 'updateapi'),
      env: { ...process.env, GOOS: 'linux', GOARCH: 'amd64', CGO_ENABLED: '0' },
      stdio: 'inherit',
    });
  }
}

function buildDeployment(commit) {
  const temporaryRoot = mkdtempSync(join(tmpdir(), 'gift-panel-update-api-snapshot-'));
  try {
    const archive = join(temporaryRoot, 'reviewed.tar');
    const snapshot = join(temporaryRoot, 'snapshot');
    mkdirSync(snapshot);
    execFileSync('git', ['-C', root, 'archive', '--format=tar', `--output=${archive}`, commit]);
    execFileSync('tar', ['-xf', archive, '-C', snapshot]);

    if (process.env.GIFT_PANEL_BUILD_TEST_MUTATE_TRACKED === '1') {
      appendFileSync(join(root, 'scripts', 'build-update-api.mjs'), '\n// deterministic build mutation\n');
    }

    const snapshotOutput = join(temporaryRoot, 'dist');
    build(snapshot, snapshotOutput, commit);
    if (reviewedCommit() !== commit) throw new Error('deployment source changed during build');

    const destination = join(root, 'dist');
    mkdirSync(destination, { recursive: true });
    for (const target of targets) {
      const source = join(snapshotOutput, target.output);
      const output = join(destination, target.output);
      copyFileSync(source, output);
      console.log(`${output} ${statSync(output).size}`);
    }
  } finally {
    rmSync(temporaryRoot, { recursive: true, force: true });
  }
}

function buildLocal() {
  const output = join(root, 'dist', 'local');
  build(root, output, 'local');
  for (const target of targets) {
    const artifact = join(output, target.output);
    console.log(`${artifact} ${statSync(artifact).size}`);
  }
}

if (localBuild) buildLocal();
else buildDeployment(reviewedCommit());
