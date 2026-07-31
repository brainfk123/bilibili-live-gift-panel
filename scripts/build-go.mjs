import { copyFileSync, mkdirSync } from 'node:fs';
import { execSync } from 'node:child_process';
import { fileURLToPath } from 'node:url';
import { dirname, join } from 'node:path';

const root = join(dirname(fileURLToPath(import.meta.url)), '..');
const distDir = join(root, 'goserver', 'dist');
mkdirSync(distDir, { recursive: true });
copyFileSync(join(root, 'dist', 'index.html'), join(distDir, 'index.html'));

const go = process.env.GO_BIN || 'go';
const out = join(root, 'dist', 'gift-panel.exe');
try {
  execSync(`"${go}" build -ldflags "-s -w" -o "${out}" .`, {
    cwd: join(root, 'goserver'),
    stdio: 'inherit',
    env: { ...process.env, PATH: `${process.env.PATH};C:\\Program Files\\Go\\bin` },
  });
} catch {
  // retry with explicit Go path appended (winget installs may not be on PATH in this shell)
  const fallback = 'C:\\Program Files\\Go\\bin\\go.exe';
  execSync(`"${fallback}" build -ldflags "-s -w" -o "${out}" .`, {
    cwd: join(root, 'goserver'),
    stdio: 'inherit',
  });
}
console.log('built ' + out);
