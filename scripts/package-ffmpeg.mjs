import { createHash, randomBytes } from 'node:crypto';
import { execFileSync } from 'node:child_process';
import { mkdir, mkdtemp, readFile, rename, rm, writeFile } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { dirname, join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';
import { deflateRawSync } from 'node:zlib';

const version = '9.0';
const warningSize = 30_000_000;
const maximumSize = 40_000_000;
const root = join(dirname(fileURLToPath(import.meta.url)), '..');
const outputDirectory = join(root, 'goserver', 'ffmpeg');
const manifestPath = join(outputDirectory, 'manifest.json');
const zipPath = join(outputDirectory, 'ffmpeg.zip');

const input = readArgument('--input');
if (!input) throw new Error('Usage: node scripts/package-ffmpeg.mjs --input <ffmpeg.exe>');

const binary = await readFile(resolve(input));
if (binary.length === 0) throw new Error('FFmpeg input is empty.');
const authenticode = process.env.FFMPEG_AUTHENTICODE === 'true';
const appVersion = (process.env.APP_VERSION || 'dev').replace(/^v/, '');
if (appVersion !== 'dev' && !authenticode) {
  throw new Error('Release FFmpeg packaging requires FFMPEG_AUTHENTICODE=true after signing the inner executable.');
}
if (authenticode) await verifyAuthenticode(binary);

const manifest = {
  version,
  sha256: createHash('sha256').update(binary).digest('hex'),
  size: binary.length,
  authenticode,
};
const archive = writeSingleFileZip('ffmpeg.exe', binary);
if (archive.length > maximumSize) {
  throw new Error(`FFmpeg ZIP is ${archive.length} bytes; hard limit is ${maximumSize} bytes.`);
}
if (archive.length > warningSize) {
  console.warn(`WARNING: FFmpeg ZIP is ${archive.length} bytes, above the ${warningSize}-byte target.`);
}

await mkdir(outputDirectory, { recursive: true });
await writePairAtomically([
  [zipPath, archive],
  [manifestPath, Buffer.from(`${JSON.stringify(manifest, null, 2)}\n`)],
]);
console.log(`packaged FFmpeg ${version}: ${binary.length} bytes, ZIP ${archive.length} bytes, SHA-256 ${manifest.sha256}, authenticode=${authenticode}`);

function readArgument(name) {
  const index = process.argv.indexOf(name);
  if (index < 0) return undefined;
  const value = process.argv[index + 1];
  if (!value || value.startsWith('--')) throw new Error(`${name} requires a value.`);
  return value;
}

async function verifyAuthenticode(contents) {
  const temporaryRoot = await mkdtemp(join(tmpdir(), 'gift-panel-ffmpeg-signature-'));
  const path = join(temporaryRoot, 'ffmpeg.exe');
  let status;
  try {
    await writeFile(path, contents, { flag: 'wx' });
    status = execFileSync('powershell.exe', [
      '-NoProfile', '-NonInteractive', '-Command',
      "& { param([string]$path) Import-Module (Join-Path $env:WINDIR 'System32\\WindowsPowerShell\\v1.0\\Modules\\Microsoft.PowerShell.Security'); (Get-AuthenticodeSignature -LiteralPath $path).Status.ToString() }",
      path,
    ], { encoding: 'utf8', windowsHide: true }).trim();
  } catch (error) {
    throw new Error(`Could not verify FFmpeg Authenticode signature: ${error.message}`);
  } finally {
    await rm(temporaryRoot, { recursive: true, force: true });
  }
  if (status !== 'Valid') throw new Error(`FFmpeg Authenticode signature status is ${status || 'missing'}, expected Valid.`);
}

function writeSingleFileZip(name, contents) {
  const nameBytes = Buffer.from(name, 'utf8');
  const compressed = deflateRawSync(contents, { level: 9 });
  const checksum = crc32(contents);
  const local = Buffer.alloc(30 + nameBytes.length);
  local.writeUInt32LE(0x04034b50, 0);
  local.writeUInt16LE(20, 4);
  local.writeUInt16LE(0x0800, 6);
  local.writeUInt16LE(8, 8);
  local.writeUInt32LE(checksum, 14);
  local.writeUInt32LE(compressed.length, 18);
  local.writeUInt32LE(contents.length, 22);
  local.writeUInt16LE(nameBytes.length, 26);
  nameBytes.copy(local, 30);

  const central = Buffer.alloc(46 + nameBytes.length);
  central.writeUInt32LE(0x02014b50, 0);
  central.writeUInt16LE((3 << 8) | 20, 4);
  central.writeUInt16LE(20, 6);
  central.writeUInt16LE(0x0800, 8);
  central.writeUInt16LE(8, 10);
  central.writeUInt32LE(checksum, 16);
  central.writeUInt32LE(compressed.length, 20);
  central.writeUInt32LE(contents.length, 24);
  central.writeUInt16LE(nameBytes.length, 28);
  central.writeUInt32LE((0o100755 << 16) >>> 0, 38);
  nameBytes.copy(central, 46);

  const end = Buffer.alloc(22);
  end.writeUInt32LE(0x06054b50, 0);
  end.writeUInt16LE(1, 8);
  end.writeUInt16LE(1, 10);
  end.writeUInt32LE(central.length, 12);
  end.writeUInt32LE(local.length + compressed.length, 16);
  return Buffer.concat([local, compressed, central, end]);
}

function crc32(contents) {
  let crc = 0xffffffff;
  for (const byte of contents) {
    crc ^= byte;
    for (let bit = 0; bit < 8; bit += 1) crc = (crc >>> 1) ^ (0xedb88320 & -(crc & 1));
  }
  return (crc ^ 0xffffffff) >>> 0;
}

async function writePairAtomically(files) {
  const nonce = `${process.pid}-${randomBytes(8).toString('hex')}`;
  const temporary = files.map(([path, contents]) => [`${path}.partial-${nonce}`, path, contents]);
  try {
    for (const [path, , contents] of temporary) await writeFile(path, contents, { flag: 'wx' });
    for (const [path, target] of temporary) {
      await rm(target, { force: true });
      await rename(path, target);
    }
  } finally {
    await Promise.all(temporary.map(([path]) => rm(path, { force: true })));
  }
}
