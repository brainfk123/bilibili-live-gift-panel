import { execFileSync } from 'node:child_process';
import { mkdirSync, statSync } from 'node:fs';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';

const root = join(dirname(fileURLToPath(import.meta.url)), '..');
mkdirSync(join(root, 'dist'), { recursive: true });
const output = join(root, 'dist', 'gift-panel-update-api-linux-amd64');

execFileSync('go', ['build', '-trimpath', '-ldflags=-s -w', '-o', output, './cmd/server'], {
  cwd: join(root, 'updateapi'),
  env: { ...process.env, GOOS: 'linux', GOARCH: 'amd64', CGO_ENABLED: '0' },
  stdio: 'inherit',
});

console.log(`${output} ${statSync(output).size}`);
