import { copyFileSync, existsSync, mkdirSync } from 'node:fs';
import { execFileSync } from 'node:child_process';
import { fileURLToPath } from 'node:url';
import { dirname, join } from 'node:path';

const root = join(dirname(fileURLToPath(import.meta.url)), '..');
const resource = join(root, 'goserver', 'rsrc_windows_amd64.syso');
if (!existsSync(resource)) {
  throw new Error(
    'Windows icon resource is missing. Run go install github.com/tc-hib/go-winres@latest and go-winres make in goserver.',
  );
}

const distDir = join(root, 'goserver', 'dist');
mkdirSync(distDir, { recursive: true });
copyFileSync(join(root, 'dist', 'index.html'), join(distDir, 'index.html'));

const out = join(root, 'dist', 'gift-panel.exe');
const appVersion = (process.env.APP_VERSION || 'dev').replace(/^v/, '');
const appCommit = process.env.APP_COMMIT || 'local';
for (const [label, value] of [['APP_VERSION', appVersion], ['APP_COMMIT', appCommit]]) {
  if (!/^[0-9A-Za-z.+-]+$/.test(value)) throw new Error(`${label} contains unsupported characters`);
}
const ldflags = `-s -w -H windowsgui -X main.appVersion=${appVersion} -X main.appCommit=${appCommit}`;
const candidates = [
  process.env.GO_BIN,
  'go',
  'C:\\Program Files\\Go\\bin\\go.exe',
].filter(Boolean);
let built = false;
let lastError;
for (const go of candidates) {
  try {
    execFileSync(go, ['build', '-ldflags', ldflags, '-o', out, '.'], {
      cwd: join(root, 'goserver'),
      stdio: 'inherit',
    });
    built = true;
    break;
  } catch (error) {
    lastError = error;
  }
}
if (!built) {
  throw lastError ?? new Error('Go compiler not found. Install Go or set GO_BIN.');
}
console.log(`built ${out} (${appVersion}, ${appCommit})`);
