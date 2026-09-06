import { createHash } from 'node:crypto';
import {
  copyFileSync,
  existsSync,
  mkdirSync,
  readdirSync,
  readFileSync,
  rmSync,
  statSync,
  writeFileSync,
} from 'node:fs';
import { dirname, join, resolve } from 'node:path';

export const UI_ASSET_MANIFEST = 'ui-assets.json';
const NON_UI_DIST_ROOTS = new Set([
  'ffmpeg',
  'ffmpeg-component-download',
  'ffmpeg-component-publish',
  'ffmpeg-component-published',
  'ffmpeg-component',
  'ffmpeg-component-release.json',
  'ffmpeg-source',
  'ffmpeg-windows-x64.exe',
  'ffmpeg-windows-x64.exe.sha256',
  'gift-clip-test-tools',
  'gift-clip-test-tools.zip',
  'msys2-toolchain',
  'msys2-toolchain-root',
  'standalone-component-manifest.json',
  'standalone-ffmpeg',
  'release-ffmpeg-sealed',
  'gift-panel.exe',
  'gift-panel-windows-x64.exe',
  'gift-panel-windows-x64.exe.sha256',
  'gift-panel-update.json',
  'gift-panel-changelog.json',
  'ffmpeg-build-config.txt',
  'ffmpeg-component-gate.txt',
]);

function listAssetPaths(root, current = '') {
  const directory = join(root, current);
  const paths = [];
  for (const entry of readdirSync(directory, { withFileTypes: true }).sort((a, b) => a.name.localeCompare(b.name))) {
    const relativePath = current ? join(current, entry.name) : entry.name;
    if (!current && NON_UI_DIST_ROOTS.has(entry.name)) continue;
    if (entry.name === UI_ASSET_MANIFEST && current === '') continue;
    if (entry.isSymbolicLink()) throw new Error(`UI asset symlinks are not supported: ${relativePath}`);
    if (entry.isDirectory()) paths.push(...listAssetPaths(root, relativePath));
    else if (entry.isFile()) paths.push(relativePath.replaceAll('\\', '/'));
    else throw new Error(`Unsupported UI asset entry: ${relativePath}`);
  }
  return paths.sort();
}

function assertSourceDirectory(sourceDir) {
  const source = resolve(sourceDir);
  if (!existsSync(source) || !statSync(source).isDirectory()) throw new Error(`UI dist directory is missing: ${source}`);
  const indexPath = join(source, 'index.html');
  if (!existsSync(indexPath) || !statSync(indexPath).isFile()) throw new Error(`UI dist index.html is missing: ${indexPath}`);
  if (existsSync(join(source, UI_ASSET_MANIFEST))) throw new Error(`Reserved UI asset manifest exists in source dist: ${UI_ASSET_MANIFEST}`);
  return source;
}

function recordAsset(root, relativePath) {
  const filePath = join(root, relativePath);
  const bytes = readFileSync(filePath);
  return {
    path: relativePath,
    size: bytes.length,
    sha256: createHash('sha256').update(bytes).digest('hex'),
  };
}

export function createUiAssetManifest(sourceDir) {
  const source = assertSourceDirectory(sourceDir);
  return {
    version: 1,
    files: listAssetPaths(source).map((relativePath) => recordAsset(source, relativePath)),
  };
}

function validateManifestShape(manifest) {
  if (!manifest || Object.getPrototypeOf(manifest) !== Object.prototype || manifest.version !== 1 || !Array.isArray(manifest.files)) {
    throw new Error('UI asset manifest schema is invalid.');
  }
  let previous = '';
  const paths = new Set();
  for (const entry of manifest.files) {
    if (!entry || Object.getPrototypeOf(entry) !== Object.prototype || Object.keys(entry).sort().join(',') !== 'path,sha256,size') {
      throw new Error('UI asset manifest entry schema is invalid.');
    }
    if (!/^(?:[A-Za-z0-9._-]+\/)*[A-Za-z0-9._-]+$/.test(entry.path) || entry.path === UI_ASSET_MANIFEST || entry.path < previous || paths.has(entry.path)) {
      throw new Error(`UI asset manifest path is invalid: ${entry.path}`);
    }
    if (!Number.isSafeInteger(entry.size) || entry.size < 0 || !/^[0-9a-f]{64}$/.test(entry.sha256)) {
      throw new Error(`UI asset manifest record is invalid: ${entry.path}`);
    }
    previous = entry.path;
    paths.add(entry.path);
  }
}

export function verifyUiAssetManifest(targetDir, manifest = JSON.parse(readFileSync(join(targetDir, UI_ASSET_MANIFEST), 'utf8'))) {
  const target = resolve(targetDir);
  validateManifestShape(manifest);
  const actualPaths = listAssetPaths(target);
  const expectedPaths = manifest.files.map((entry) => entry.path);
  if (actualPaths.length !== expectedPaths.length || actualPaths.some((path, index) => path !== expectedPaths[index])) {
    throw new Error('UI asset manifest does not describe the complete copied dist tree.');
  }
  for (const entry of manifest.files) {
    const actual = recordAsset(target, entry.path);
    if (actual.size !== entry.size || actual.sha256 !== entry.sha256) throw new Error(`UI asset manifest hash mismatch: ${entry.path}`);
  }
  return manifest;
}

export function mirrorUiAssets(sourceDir, targetDir) {
  const source = assertSourceDirectory(sourceDir);
  const target = resolve(targetDir);
  if (source === target) throw new Error('UI source and embedded target directories must differ.');
  rmSync(target, { recursive: true, force: true });
  mkdirSync(target, { recursive: true });
  for (const relativePath of listAssetPaths(source)) {
    const destination = join(target, relativePath);
    mkdirSync(dirname(destination), { recursive: true });
    copyFileSync(join(source, relativePath), destination);
  }
  const manifest = createUiAssetManifest(source);
  writeFileSync(join(target, UI_ASSET_MANIFEST), `${JSON.stringify(manifest, null, 2)}\n`, 'utf8');
  return verifyUiAssetManifest(target, manifest);
}
