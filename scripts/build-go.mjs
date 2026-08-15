import { existsSync, readFileSync } from 'node:fs';
import { execFileSync } from 'node:child_process';
import { fileURLToPath } from 'node:url';
import { dirname, join } from 'node:path';
import { mirrorUiAssets } from './ui-assets.mjs';
import { resolveUpdateAPIBaseURLHex } from './update-api-build-config.mjs';

const root = join(dirname(fileURLToPath(import.meta.url)), '..');
const appVersion = (process.env.APP_VERSION || 'dev').replace(/^v/, '');
const appCommit = process.env.APP_COMMIT || 'local';
for (const [label, value] of [['APP_VERSION', appVersion], ['APP_COMMIT', appCommit]]) {
  if (!/^[0-9A-Za-z.+-]+$/.test(value)) throw new Error(`${label} contains unsupported characters`);
}
const updateAPIBaseURLHex = resolveUpdateAPIBaseURLHex(appVersion, process.env.APP_UPDATE_API_URL);
const updateExpectedPublisher = (process.env.APP_UPDATE_PUBLISHER || '').trim();
if (appVersion !== 'dev' && !updateExpectedPublisher) {
  throw new Error('Release build requires APP_UPDATE_PUBLISHER.');
}
const updateExpectedPublisherHex = Buffer.from(updateExpectedPublisher, 'utf8').toString('hex');
if (appVersion !== 'dev') {
  const manifestPath = join(root, 'goserver', 'ffmpeg', 'manifest.json');
  let manifest;
  try {
    manifest = JSON.parse(readFileSync(manifestPath, 'utf8'));
  } catch (error) {
    throw new Error(`Release build requires a readable embedded FFmpeg manifest: ${error.message}`);
  }
  if (manifest.authenticode !== true) {
    throw new Error('Release build requires an Authenticode-signed embedded FFmpeg payload.');
  }
}
execFileSync(process.execPath, [join(root, 'scripts', 'verify-ffmpeg.mjs'), ...(appVersion === 'dev' ? ['--payload-only'] : [])], {
  cwd: root,
  stdio: 'inherit',
  env: process.env,
});
const resource = join(root, 'goserver', 'rsrc_windows_amd64.syso');
if (!existsSync(resource)) {
  throw new Error(
    'Windows icon resource is missing. Run go install github.com/tc-hib/go-winres@latest and go-winres make in goserver.',
  );
}

const distDir = join(root, 'goserver', 'dist');
const uiManifest = mirrorUiAssets(join(root, 'dist'), distDir);
console.log(`embedded ${uiManifest.files.length} UI assets (manifest v${uiManifest.version})`);

const out = join(root, 'dist', 'gift-panel.exe');
const ldflags = `-s -w -H windowsgui -X main.appVersion=${appVersion} -X main.appCommit=${appCommit} -X main.updateAPIBaseURLHex=${updateAPIBaseURLHex} -X main.updateExpectedPublisherHex=${updateExpectedPublisherHex}`;
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
