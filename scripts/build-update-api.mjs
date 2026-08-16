import { execFileSync } from 'node:child_process';
import { mkdirSync, statSync } from 'node:fs';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';

const root = join(dirname(fileURLToPath(import.meta.url)), '..');
mkdirSync(join(root, 'dist'), { recursive: true });
const localBuild = process.env.GIFT_PANEL_LOCAL_BUILD === '1';

function reviewedCommit() {
  if (localBuild) return 'local';
  const commit = execFileSync('git', ['-C', root, 'rev-parse', '--verify', 'HEAD'], { encoding: 'utf8' }).trim();
  if (!/^[0-9a-f]{40}$/.test(commit)) throw new Error('deployment build requires an exact reviewed Git commit');
  const dirty = execFileSync('git', ['-C', root, 'status', '--porcelain', '--untracked-files=no'], { encoding: 'utf8' });
  if (dirty !== '') throw new Error('deployment build requires a clean reviewed Git commit; set GIFT_PANEL_LOCAL_BUILD=1 only for local non-deployment builds');
  return commit;
}

const commit = reviewedCommit();
const targets = [
  { command: './cmd/server', output: 'gift-panel-update-api-linux-amd64', ldflags: '-s -w' },
  { command: './cmd/mirror', output: 'gift-panel-release-mirror-linux-amd64', ldflags: `-s -w -X main.buildCommit=${commit}` },
];

for (const target of targets) {
  const output = join(root, 'dist', target.output);
  execFileSync('go', ['build', '-trimpath', '-buildvcs=false', '-ldflags', target.ldflags, '-o', output, target.command], {
    cwd: join(root, 'updateapi'),
    env: { ...process.env, GOOS: 'linux', GOARCH: 'amd64', CGO_ENABLED: '0' },
    stdio: 'inherit',
  });

  console.log(`${output} ${statSync(output).size}`);
}
