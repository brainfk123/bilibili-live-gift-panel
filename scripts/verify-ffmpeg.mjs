import { createHash } from 'node:crypto';
import { execFileSync } from 'node:child_process';
import { chmod, mkdtemp, readFile, rm, writeFile } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { inflateRawSync } from 'node:zlib';

const root = join(dirname(fileURLToPath(import.meta.url)), '..');
const payloadDirectory = join(root, 'goserver', 'ffmpeg');
const manifest = JSON.parse(await readFile(join(payloadDirectory, 'manifest.json'), 'utf8'));
const archive = await readFile(join(payloadDirectory, 'ffmpeg.zip'));
const binary = readSingleFileZip(archive, 'ffmpeg.exe');
validateManifest(manifest, binary);
if (archive.length > 40_000_000) throw new Error(`FFmpeg ZIP exceeds the 40000000-byte hard limit: ${archive.length}`);
if (archive.length > 30_000_000) console.warn(`WARNING: FFmpeg ZIP exceeds the 30000000-byte target: ${archive.length}`);
if ((process.env.APP_VERSION || 'dev').replace(/^v/, '') !== 'dev' && manifest.authenticode !== true) {
  throw new Error('Release verification requires an Authenticode-signed inner FFmpeg payload.');
}

const temporaryRoot = await mkdtemp(join(tmpdir(), 'gift-panel-ffmpeg-verify-'));
try {
  const executable = join(temporaryRoot, 'ffmpeg.exe');
  await writeFile(executable, binary, { flag: 'wx' });
  await chmod(executable, 0o700);
  if (manifest.authenticode) verifyAuthenticode(executable);

  const version = run(executable, ['-version']);
  assert(/^ffmpeg version 9\.0(?:\s|$)/m.test(version), 'FFmpeg version is not exactly 9.0.');
  const buildconf = run(executable, ['-buildconf']);
  const normalizedBuildconf = buildconf.replaceAll("'", '');
  assert(!/--enable-(?:gpl|nonfree)\b/i.test(buildconf), 'GPL or nonfree support is enabled.');
  for (const flag of await configureFlags()) assert(normalizedBuildconf.includes(flag), `Build configuration is missing ${flag}.`);

  const protocols = parseProtocols(run(executable, ['-hide_banner', '-protocols']));
  assertExactSet(protocols, new Set(['file', 'pipe']), 'protocol');
  for (const protocol of ['http', 'https', 'tcp', 'udp', 'tls', 'ftp', 'rtmp', 'rtsp', 'srt']) {
    assert(!protocols.has(protocol), `Network protocol ${protocol} is enabled.`);
  }

  assertExactSet(parseComponents(run(executable, ['-hide_banner', '-demuxers']), 'demuxer'), new Set([
    '3g2', '3gp', 'gif', 'image2', 'm4a', 'mj2', 'mov', 'mp4', 'webp_anim', 'webp_pipe',
  ]), 'demuxer');
  assertExactSet(parseComponents(run(executable, ['-hide_banner', '-decoders']), 'decoder'), new Set([
    'gif', 'h264', 'png', 'vp8', 'webp', 'webp_anim',
  ]), 'decoder');
  assertExactSet(parseComponents(run(executable, ['-hide_banner', '-encoders']), 'encoder'), new Set(['h264_mf']), 'encoder');
  const filters = parseComponents(run(executable, ['-hide_banner', '-filters']), 'filter');
  assertExactSet(filters, new Set([
    'abuffer', 'abuffersink', 'aformat', 'alphamerge', 'anull', 'atrim', 'buffer', 'buffersink',
    'crop', 'format', 'fps', 'hflip', 'null', 'overlay', 'rotate', 'scale', 'setpts', 'split',
    'transpose', 'trim', 'vflip',
  ]), 'filter');
  assert(!filters.has('loop'), 'The cycle-caching loop filter is enabled.');
  assertExactSet(parseComponents(run(executable, ['-hide_banner', '-muxers']), 'muxer'), new Set(['mov', 'mp4']), 'muxer');
  assertExactSet(parseSimpleList(run(executable, ['-hide_banner', '-bsfs']), 'Bitstream filters:'), new Set([
    'aac_adtstoasc', 'vp9_superframe',
  ]), 'bitstream filter');
  assertExactSet(parseSimpleList(run(executable, ['-hide_banner', '-hwaccels']), 'Hardware acceleration methods:'), new Set([
    'd3d11va',
  ]), 'hardware acceleration method');
  assertExactSet(parseDevices(run(executable, ['-hide_banner', '-devices'])), new Set(), 'device');

  runProductionSmoke(executable);
  console.log(`verified FFmpeg ${manifest.version}: binary ${manifest.size} bytes, ZIP ${archive.length} bytes, SHA-256 ${manifest.sha256}, authenticode=${manifest.authenticode}`);
} finally {
  await rm(temporaryRoot, { recursive: true, force: true });
}

function validateManifest(value, contents) {
  const keys = Object.keys(value).sort().join(',');
  assert(keys === 'authenticode,sha256,size,version', `Unexpected manifest schema: ${keys}`);
  assert(value.version === '9.0', `Manifest version is ${value.version}.`);
  assert(/^[0-9a-f]{64}$/.test(value.sha256), 'Manifest SHA-256 is malformed.');
  assert(Number.isSafeInteger(value.size) && value.size > 0, 'Manifest size is invalid.');
  assert(typeof value.authenticode === 'boolean', 'Manifest authenticode field is invalid.');
  assert(contents.length === value.size, `Binary size ${contents.length} does not match manifest ${value.size}.`);
  assert(createHash('sha256').update(contents).digest('hex') === value.sha256, 'Binary SHA-256 does not match manifest.');
}

function verifyAuthenticode(path) {
  let status;
  try {
    status = execFileSync('powershell.exe', [
      '-NoProfile', '-NonInteractive', '-Command',
      "& { param([string]$path) Import-Module (Join-Path $env:WINDIR 'System32\\WindowsPowerShell\\v1.0\\Modules\\Microsoft.PowerShell.Security'); (Get-AuthenticodeSignature -LiteralPath $path).Status.ToString() }",
      path,
    ], { encoding: 'utf8', windowsHide: true }).trim();
  } catch (error) {
    throw new Error(`Could not verify FFmpeg Authenticode signature: ${error.message}`);
  }
  assert(status === 'Valid', `FFmpeg Authenticode signature status is ${status || 'missing'}, expected Valid.`);
}

function readSingleFileZip(buffer, expectedName) {
  assert(buffer.length >= 52, 'ZIP is truncated.');
  const endOffset = buffer.length - 22;
  assert(buffer.readUInt32LE(endOffset) === 0x06054b50, 'ZIP has a comment, trailing bytes, or no end record.');
  assert(buffer.readUInt16LE(endOffset + 8) === 1 && buffer.readUInt16LE(endOffset + 10) === 1, 'ZIP must contain exactly one entry.');
  assert(buffer.readUInt16LE(endOffset + 20) === 0, 'ZIP comments are not allowed.');
  const centralSize = buffer.readUInt32LE(endOffset + 12);
  const centralOffset = buffer.readUInt32LE(endOffset + 16);
  assert(centralOffset + centralSize === endOffset, 'ZIP central directory boundaries are invalid.');
  assert(buffer.readUInt32LE(centralOffset) === 0x02014b50, 'ZIP central entry is invalid.');
  const centralNameLength = buffer.readUInt16LE(centralOffset + 28);
  const centralExtraLength = buffer.readUInt16LE(centralOffset + 30);
  const centralCommentLength = buffer.readUInt16LE(centralOffset + 32);
  assert(46 + centralNameLength + centralExtraLength + centralCommentLength === centralSize, 'ZIP has extra central-directory data.');
  assert(centralExtraLength === 0 && centralCommentLength === 0, 'ZIP entry extras and comments are not allowed.');
  const centralName = buffer.subarray(centralOffset + 46, centralOffset + 46 + centralNameLength).toString('utf8');
  assert(centralName === expectedName, `ZIP entry is ${centralName}, expected ${expectedName}.`);
  assert(buffer.readUInt8(centralOffset + 5) === 3, 'ZIP entry was not created with Unix mode metadata.');
  assert(buffer.readUInt16LE(centralOffset + 8) === 0x0800, 'ZIP central flags are not the strict UTF-8-only value.');
  assert(buffer.readUInt16LE(centralOffset + 10) === 8, 'ZIP entry is not DEFLATE-compressed.');
  assert((buffer.readUInt32LE(centralOffset + 38) >>> 16 & 0o170000) === 0o100000, 'ZIP entry is not a regular file.');
  assert(buffer.readUInt32LE(centralOffset + 42) === 0, 'ZIP local entry is not at offset zero.');

  assert(buffer.readUInt32LE(0) === 0x04034b50, 'ZIP local entry is invalid.');
  assert(buffer.readUInt16LE(6) === 0x0800, 'ZIP local flags are not the strict UTF-8-only value.');
  const localNameLength = buffer.readUInt16LE(26);
  const localExtraLength = buffer.readUInt16LE(28);
  assert(localExtraLength === 0, 'ZIP local extras are not allowed.');
  const localName = buffer.subarray(30, 30 + localNameLength).toString('utf8');
  assert(localName === expectedName, 'ZIP local and central names differ.');
  const compressedSize = buffer.readUInt32LE(18);
  const uncompressedSize = buffer.readUInt32LE(22);
  const dataOffset = 30 + localNameLength;
  assert(dataOffset + compressedSize === centralOffset, 'ZIP compressed-data boundary is invalid.');
  assert(compressedSize === buffer.readUInt32LE(centralOffset + 20), 'ZIP compressed sizes differ.');
  assert(uncompressedSize === buffer.readUInt32LE(centralOffset + 24), 'ZIP uncompressed sizes differ.');
  const contents = inflateRawSync(buffer.subarray(dataOffset, centralOffset));
  assert(contents.length === uncompressedSize, 'ZIP inflated size is invalid.');
  const checksum = crc32(contents);
  assert(checksum === buffer.readUInt32LE(14) && checksum === buffer.readUInt32LE(centralOffset + 16), 'ZIP CRC-32 is invalid.');
  return contents;
}

async function configureFlags() {
  return (await readFile(join(root, 'third_party', 'ffmpeg', 'configure.flags'), 'utf8'))
    .split(/\r?\n/).map((line) => line.trim()).filter(Boolean);
}

function run(executable, arguments_) {
  try {
    return execFileSync(executable, arguments_, { encoding: 'utf8', windowsHide: true, maxBuffer: 16 * 1024 * 1024 });
  } catch (error) {
    const output = `${error.stdout || ''}${error.stderr || ''}`.trim();
    throw new Error(`FFmpeg command failed: ${arguments_.join(' ')}${output ? `\n${output}` : ''}`);
  }
}

function runProductionSmoke(executable) {
  try {
    const output = execFileSync('go', ['test', './...', '-run', '^TestGiftClipPayloadFFmpegProductionArgvSmoke$', '-count=1'], {
      cwd: join(root, 'goserver'),
      encoding: 'utf8',
      env: { ...process.env, GIFT_CLIP_FFMPEG_SMOKE_EXE: executable },
      windowsHide: true,
      maxBuffer: 32 * 1024 * 1024,
    });
    if (output.trim()) console.log(output.trim());
  } catch (error) {
    const output = `${error.stdout || ''}${error.stderr || ''}`.trim();
    throw new Error(`Production-argv FFmpeg smoke tests failed${output ? `:\n${output}` : '.'}`);
  }
}

function parseProtocols(output) {
  const protocols = new Set();
  for (const line of output.split(/\r?\n/)) {
    const value = line.trim();
    if (value && !value.endsWith(':') && !value.startsWith('Supported file protocols')) protocols.add(value);
  }
  return protocols;
}

function parseComponents(output, kind) {
  const patterns = {
    demuxer: /^\s*D\s+([^\s,]+(?:,[^\s,]+)*)\s/,
    muxer: /^\s*E\s+([^\s,]+(?:,[^\s,]+)*)\s/,
    decoder: /^\s*[VAS][A-Z.]{5}\s+([^\s]+)\s/,
    encoder: /^\s*[VAS][A-Z.]{5}\s+([^\s]+)\s/,
    filter: /^\s*[TS.]{2}\s+([^\s]+)\s/,
  };
  const names = new Set();
  for (const line of output.split(/\r?\n/)) {
    const match = line.match(patterns[kind]);
    if (match) for (const name of match[1].split(',')) if (name !== '=') names.add(name);
  }
  return names;
}

function parseSimpleList(output, header) {
  const names = new Set();
  for (const line of output.split(/\r?\n/)) {
    const value = line.trim();
    if (value && value !== header) names.add(value);
  }
  return names;
}

function parseDevices(output) {
  const names = new Set();
  for (const line of output.split(/\r?\n/)) {
    const match = line.match(/^\s*[D.][E.]\s+([^\s]+)\s/);
    if (match && match[1] !== '=') names.add(match[1]);
  }
  return names;
}

function assertExactSet(actual, expected, label) {
  const unexpected = [...actual].filter((name) => !expected.has(name));
  const missing = [...expected].filter((name) => !actual.has(name));
  assert(unexpected.length === 0 && missing.length === 0, `${label} whitelist mismatch; unexpected=[${unexpected}], missing=[${missing}].`);
}

function crc32(contents) {
  let crc = 0xffffffff;
  for (const byte of contents) {
    crc ^= byte;
    for (let bit = 0; bit < 8; bit += 1) crc = (crc >>> 1) ^ (0xedb88320 & -(crc & 1));
  }
  return (crc ^ 0xffffffff) >>> 0;
}

function assert(condition, message) {
  if (!condition) throw new Error(message);
}
