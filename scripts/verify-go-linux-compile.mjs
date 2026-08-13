import { spawnSync } from 'node:child_process';
import { mkdtempSync, rmSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

const repositoryRoot = resolve(fileURLToPath(new URL('..', import.meta.url)));
const compileRoot = mkdtempSync(join(tmpdir(), 'gift-panel-linux-compile-'));
const testBinary = join(compileRoot, 'goserver-linux.test');

try {
  const result = spawnSync('go', ['test', '-c', '-o', testBinary, '.'], {
    cwd: join(repositoryRoot, 'goserver'),
    env: { ...process.env, GOOS: 'linux', GOARCH: 'amd64', CGO_ENABLED: '0' },
    stdio: 'inherit',
  });
  if (result.error) throw result.error;
  if (result.status !== 0) {
    throw new Error(`GOOS=linux compile-only gate failed with exit code ${result.status ?? 'unknown'}`);
  }
  console.log('Verified GOOS=linux GOARCH=amd64 compile-only gate.');
} finally {
  rmSync(compileRoot, { recursive: true, force: true });
}
