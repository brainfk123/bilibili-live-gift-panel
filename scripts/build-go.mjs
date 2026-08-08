import { copyFileSync, existsSync, mkdirSync } from 'node:fs';
import { execFileSync } from 'node:child_process';
import { fileURLToPath } from 'node:url';
import { dirname, join } from 'node:path';

execFileSync(process.execPath, [join(dirname(fileURLToPath(import.meta.url)), 'sync-assistant-help.mjs')], {
  cwd: join(dirname(fileURLToPath(import.meta.url)), '..'),
  stdio: 'inherit',
});

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
const assistantPublicKey = process.env.ASSISTANT_MANIFEST_PUBLIC_KEY_BASE64 || '';
if (assistantPublicKey) {
  const decoded = Buffer.from(assistantPublicKey, 'base64');
  if (decoded.length !== 32 || decoded.toString('base64') !== assistantPublicKey) {
    throw new Error('ASSISTANT_MANIFEST_PUBLIC_KEY_BASE64 must be a canonical 32-byte Ed25519 public key.');
  }
}
const assistantManifestURL = process.env.ASSISTANT_MANIFEST_URL || '';
if (assistantManifestURL) {
  const parsed = new URL(assistantManifestURL);
  if (parsed.protocol !== 'https:' || !/(^|\.)modelscope\.cn$/i.test(parsed.hostname)) {
    throw new Error('ASSISTANT_MANIFEST_URL must be an HTTPS URL hosted by ModelScope.');
  }
}
if (process.env.REQUIRE_ASSISTANT_TRUST === '1' && !assistantPublicKey) {
  throw new Error('A release build requires ASSISTANT_MANIFEST_PUBLIC_KEY_BASE64.');
}
const externalLinkFlags = process.env.GO_EXTERNAL_STATIC === '1'
  ? ' -linkmode external -extldflags=-static'
  : '';
const assistantKeyFlag = assistantPublicKey
  ? ` -X main.assistantManifestPublicKeyBase64=${assistantPublicKey}`
  : '';
const assistantManifestFlag = assistantManifestURL
  ? ` -X main.assistantManifestURL=${assistantManifestURL}`
  : '';
const ldflags = `-s -w -H windowsgui -X main.appVersion=${appVersion} -X main.appCommit=${appCommit}${assistantKeyFlag}${assistantManifestFlag}${externalLinkFlags}`;
const buildArgs = ['build'];
if (process.env.GO_BUILD_TAGS) {
  buildArgs.push('-tags', process.env.GO_BUILD_TAGS);
}
buildArgs.push('-ldflags', ldflags, '-o', out, '.');
const candidates = [
  process.env.GO_BIN,
  'go',
  'C:\\Program Files\\Go\\bin\\go.exe',
].filter(Boolean);
let built = false;
let lastError;
for (const go of candidates) {
  try {
    execFileSync(go, buildArgs, {
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
