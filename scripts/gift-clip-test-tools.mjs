import { execFileSync } from 'node:child_process';
import { createHash } from 'node:crypto';
import { existsSync, readFileSync, statSync } from 'node:fs';
import { isAbsolute, join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';

export const GIFT_CLIP_TEST_TOOL_PROVENANCE = Object.freeze({
  ffmpegVersion: '9.0',
  sourceSha256: '7f607a00dd0d28a729d5a4811205812eef01cf6ef6155025febb6f36a9062d52',
  sourceSigningFingerprint: 'FCF986EA15E6E293A5644F10B4322F04D67658D8',
  sourceSignedTag: 'n9.0',
  sourceSignedTagCommit: 'd32b387f2b0a484599d4587d651891f0c63c4238',
  sourceSignedTagFingerprint: 'DD1EC9E8DE085C629B3E1846B18E8928B3948D64',
  toolchainLockSha256: '00936d04bc060022b87a532bac396efe69f551b7bcf7ff791f71a1236ab77e67',
  configureSha256: '2bbd2048081e7ca1d87b88509f1c0d1362dc2ec54c49dd7821e3a81e298d0886',
});

function sha256(path) {
  return createHash('sha256').update(readFileSync(path)).digest('hex');
}

function defaultRunVersion(path) {
  return execFileSync(path, ['-version'], { encoding: 'utf8', windowsHide: true, maxBuffer: 1024 * 1024 });
}

function assertExactKeys(value, keys, label) {
  if (!value || Object.getPrototypeOf(value) !== Object.prototype || Object.keys(value).sort().join(',') !== [...keys].sort().join(',')) {
    throw new Error(`${label} schema is invalid.`);
  }
}

export function verifyGiftClipTestTools(directory, options = {}) {
  const root = resolve(directory);
  const manifestPath = join(root, 'manifest.json');
  if (!existsSync(manifestPath) || !statSync(manifestPath).isFile()) throw new Error('Gift clip test-tool manifest is missing.');
  let manifest;
  try {
    manifest = JSON.parse(readFileSync(manifestPath, 'utf8'));
  } catch (error) {
    throw new Error(`Gift clip test-tool manifest is invalid JSON: ${error.message}`);
  }
  assertExactKeys(manifest, ['schema', ...Object.keys(GIFT_CLIP_TEST_TOOL_PROVENANCE), 'binaries'], 'Gift clip test-tool manifest');
  if (manifest.schema !== 1) throw new Error('Gift clip test-tool manifest schema version is invalid.');
  for (const [name, expected] of Object.entries(GIFT_CLIP_TEST_TOOL_PROVENANCE)) {
    if (manifest[name] !== expected) throw new Error(`Gift clip test-tool source provenance mismatch: ${name}.`);
  }
  assertExactKeys(manifest.binaries, ['ffmpeg', 'ffprobe'], 'Gift clip test-tool binaries');
  const runVersion = options.runVersion ?? defaultRunVersion;
  const result = {};
  for (const name of ['ffmpeg', 'ffprobe']) {
    const record = manifest.binaries[name];
    assertExactKeys(record, ['file', 'sha256', 'size'], `Gift clip test-tool ${name}`);
    const expectedFile = `${name}.exe`;
    if (record.file !== expectedFile || isAbsolute(record.file)) throw new Error(`Gift clip test-tool ${name} path is invalid.`);
    const path = join(root, record.file);
    if (!existsSync(path) || !statSync(path).isFile()) throw new Error(`Gift clip test-tool ${name} is missing.`);
    const size = statSync(path).size;
    if (!Number.isSafeInteger(record.size) || record.size <= 0 || size !== record.size) throw new Error(`Gift clip test-tool ${name} size mismatch.`);
    if (!/^[0-9a-f]{64}$/.test(record.sha256) || sha256(path) !== record.sha256) throw new Error(`Gift clip test-tool ${name} SHA-256 mismatch.`);
    const version = String(runVersion(path)).replaceAll('\r\n', '\n').split('\n')[0];
    if (!version.startsWith(`${name} version ${GIFT_CLIP_TEST_TOOL_PROVENANCE.ffmpegVersion} `)) {
      throw new Error(`Gift clip test-tool ${name} version mismatch: ${version}`);
    }
    result[name] = path;
  }
  return result;
}

if (process.argv[1] && resolve(process.argv[1]) === resolve(fileURLToPath(import.meta.url))) {
  const root = process.argv[2] || join(process.cwd(), 'dist', 'gift-clip-test-tools');
  process.stdout.write(`${JSON.stringify(verifyGiftClipTestTools(root))}\n`);
}
