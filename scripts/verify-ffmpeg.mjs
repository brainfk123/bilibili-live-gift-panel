import { createHash } from 'node:crypto';
import { execFileSync } from 'node:child_process';
import { chmod, mkdtemp, readFile, rm, writeFile } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { deflateRawSync, inflateRawSync } from 'node:zlib';

const maximumSize = 40_000_000;
const warningSize = 30_000_000;
const sourceSha256 = '7f607a00dd0d28a729d5a4811205812eef01cf6ef6155025febb6f36a9062d52';
const sourceDateEpoch = '1785797913';
const exactComponents = [
  'AAC_ADTSTOASC_BSF', 'AC3_PARSER', 'AFORMAT_FILTER', 'ALPHAMERGE_FILTER', 'ANULL_FILTER', 'ATRIM_FILTER',
  'CROP_FILTER', 'FILE_PROTOCOL', 'FORMAT_FILTER', 'FPS_FILTER', 'GIF_DECODER', 'GIF_DEMUXER', 'GIF_PARSER',
  'H264_DECODER', 'H264_MF_ENCODER', 'H264_PARSER', 'HFLIP_FILTER', 'IMAGE2_DEMUXER',
  'IMAGE_WEBP_PIPE_DEMUXER', 'MOV_DEMUXER', 'MOV_MUXER', 'MP4_MUXER', 'NULL_FILTER', 'OVERLAY_FILTER',
  'PIPE_PROTOCOL', 'PNG_DECODER', 'ROTATE_FILTER', 'SCALE_FILTER', 'SETPTS_FILTER', 'SPLIT_FILTER',
  'TRANSPOSE_FILTER', 'TRIM_FILTER', 'VFLIP_FILTER', 'VP8_DECODER', 'VP9_SUPERFRAME_BSF',
  'WEBP_ANIM_DECODER', 'WEBP_ANIM_DEMUXER', 'WEBP_DECODER',
];
const root = join(dirname(fileURLToPath(import.meta.url)), '..');

if (process.argv.includes('--self-test')) await runSelfTests();
else await main();

async function main() {
  const payloadOnly = process.argv.includes('--payload-only');
  const payloadDirectory = join(root, 'goserver', 'ffmpeg');
  const manifest = parseManifest(await readFile(join(payloadDirectory, 'manifest.json'), 'utf8'));
  const archive = await readFile(join(payloadDirectory, 'ffmpeg.zip'));
  validateManifest(manifest, archive);
  if (archive.length > warningSize) console.warn(`WARNING: FFmpeg ZIP exceeds the ${warningSize}-byte target: ${archive.length}`);
  const binary = readSingleFileZip(archive, 'ffmpeg.exe', manifest.size);
  assert(createHash('sha256').update(binary).digest('hex') === manifest.sha256, 'Binary SHA-256 does not match manifest.');
  const expectedGate = await componentGateRecord(manifest.sha256, manifest.size, binary);
  validateComponentGate(expectedGate, Buffer.from(manifest.component_gate, 'utf8'), manifest);
  if ((process.env.APP_VERSION || 'dev').replace(/^v/, '') !== 'dev' && manifest.authenticode !== true) {
    throw new Error('Release verification requires an Authenticode-signed inner FFmpeg payload.');
  }

  const temporaryRoot = await mkdtemp(join(tmpdir(), 'gift-panel-ffmpeg-verify-'));
  try {
    const executable = join(temporaryRoot, 'ffmpeg.exe');
    await writeFile(executable, binary, { flag: 'wx' });
    await chmod(executable, 0o700);
    if (manifest.authenticode) verifyAuthenticode(executable);
    verifyRuntimeSurface(executable, await configureFlags());
    if (!payloadOnly) runGoVerification(executable);
    console.log(`verified FFmpeg ${manifest.version}: binary ${manifest.size} bytes, ZIP ${archive.length} bytes, SHA-256 ${manifest.sha256}, authenticode=${manifest.authenticode}`);
  } finally {
    await rm(temporaryRoot, { recursive: true, force: true });
  }
}

function verifyRuntimeSurface(executable, flags) {
  const version = run(executable, ['-version']);
  assert(/^ffmpeg version 9\.0(?:\s|$)/m.test(version), 'FFmpeg version is not exactly 9.0.');
  const buildconf = run(executable, ['-buildconf']);
  const normalizedBuildconf = buildconf.replaceAll("'", '');
  assert(!/--enable-(?:gpl|nonfree)\b/i.test(buildconf), 'GPL or nonfree support is enabled.');
  for (const flag of flags) assert(normalizedBuildconf.includes(flag), `Build configuration is missing ${flag}.`);
  const protocols = parseProtocols(run(executable, ['-hide_banner', '-protocols']));
  assertExactSet(protocols, new Set(['file', 'pipe']), 'protocol');
  assertExactSet(parseComponents(run(executable, ['-hide_banner', '-demuxers']), 'demuxer'), new Set(['3g2', '3gp', 'gif', 'image2', 'm4a', 'mj2', 'mov', 'mp4', 'webp_anim', 'webp_pipe']), 'demuxer');
  assertExactSet(parseComponents(run(executable, ['-hide_banner', '-decoders']), 'decoder'), new Set(['gif', 'h264', 'png', 'vp8', 'webp', 'webp_anim']), 'decoder');
  assertExactSet(parseComponents(run(executable, ['-hide_banner', '-encoders']), 'encoder'), new Set(['h264_mf']), 'encoder');
  const filters = parseComponents(run(executable, ['-hide_banner', '-filters']), 'filter');
  assertExactSet(filters, new Set(['abuffer', 'abuffersink', 'aformat', 'alphamerge', 'anull', 'atrim', 'buffer', 'buffersink', 'crop', 'format', 'fps', 'hflip', 'null', 'overlay', 'rotate', 'scale', 'setpts', 'split', 'transpose', 'trim', 'vflip']), 'filter');
  assert(!filters.has('loop'), 'The cycle-caching loop filter is enabled.');
  assertExactSet(parseComponents(run(executable, ['-hide_banner', '-muxers']), 'muxer'), new Set(['mov', 'mp4']), 'muxer');
  assertExactSet(parseSimpleList(run(executable, ['-hide_banner', '-bsfs']), 'Bitstream filters:'), new Set(['aac_adtstoasc', 'vp9_superframe']), 'bitstream filter');
  assertExactSet(parseSimpleList(run(executable, ['-hide_banner', '-hwaccels']), 'Hardware acceleration methods:'), new Set(['d3d11va']), 'hardware acceleration method');
  assertExactSet(parseDevices(run(executable, ['-hide_banner', '-devices'])), new Set(), 'device');
}

function parseManifest(text) {
  const allowed = new Set(['version', 'sha256', 'archive_sha256', 'component_gate', 'component_gate_sha256', 'size', 'authenticode']);
  let offset = 0;
  const skipSpace = () => { while (/\s/.test(text[offset] || '')) offset += 1; };
  const expect = (character) => { skipSpace(); assert(text[offset] === character, `Manifest expected ${character}.`); offset += 1; };
  const readString = () => {
    skipSpace();
    assert(text[offset] === '"', 'Manifest key/value must be a JSON string.');
    const start = offset;
    offset += 1;
    for (; offset < text.length; offset += 1) {
      if (text[offset] === '\\') { offset += 1; continue; }
      if (text[offset] === '"') { offset += 1; return JSON.parse(text.slice(start, offset)); }
    }
    throw new Error('Manifest string is unterminated.');
  };
  const result = {};
  const seen = new Set();
  expect('{');
  skipSpace();
  while (text[offset] !== '}') {
    const key = readString();
    assert(allowed.has(key), `Manifest field ${key} is unknown.`);
    assert(!seen.has(key), `Manifest field ${key} is duplicated.`);
    seen.add(key);
    expect(':');
    skipSpace();
    if (text[offset] === '"') result[key] = readString();
    else {
      const match = text.slice(offset).match(/^(?:true|false|-?(?:0|[1-9]\d*))\b/);
      assert(match, `Manifest value for ${key} is invalid.`);
      result[key] = JSON.parse(match[0]);
      offset += match[0].length;
    }
    skipSpace();
    if (text[offset] === ',') {
      offset += 1;
      skipSpace();
      assert(text[offset] !== '}', 'Manifest has a trailing comma.');
      continue;
    }
    break;
  }
  expect('}');
  skipSpace();
  assert(offset === text.length, 'Manifest has trailing JSON or bytes.');
  assert(seen.size === allowed.size, 'Manifest is missing required fields.');
  return result;
}

function validateManifest(value, archive) {
  const keys = Object.keys(value).sort().join(',');
  assert(keys === 'archive_sha256,authenticode,component_gate,component_gate_sha256,sha256,size,version', `Unexpected manifest schema: ${keys}`);
  assert(value.version === '9.0', `Manifest version is ${value.version}.`);
  for (const [name, hash] of [['binary', value.sha256], ['archive', value.archive_sha256], ['component gate', value.component_gate_sha256]]) {
    assert(/^[0-9a-f]{64}$/.test(hash), `Manifest ${name} SHA-256 is malformed.`);
  }
  assert(Number.isSafeInteger(value.size) && value.size > 0 && value.size <= maximumSize, 'Manifest size is invalid.');
  assert(typeof value.authenticode === 'boolean', 'Manifest authenticode field is invalid.');
  assert(typeof value.component_gate === 'string' && value.component_gate.length > 0 && value.component_gate.length <= 16_384, 'Manifest component gate record is invalid.');
  assert(archive.length <= maximumSize, `FFmpeg ZIP exceeds the ${maximumSize}-byte hard limit: ${archive.length}`);
  assert(createHash('sha256').update(archive).digest('hex') === value.archive_sha256, 'Archive SHA-256 does not match manifest.');
}

function readSingleFileZip(buffer, expectedName, expectedSize) {
  assert(buffer.length >= 52 && buffer.length <= maximumSize, 'ZIP size is invalid.');
  const endOffset = buffer.length - 22;
  assert(buffer.readUInt32LE(endOffset) === 0x06054b50, 'ZIP has trailing bytes or no end record.');
  assert(buffer.readUInt16LE(endOffset + 4) === 0 && buffer.readUInt16LE(endOffset + 6) === 0, 'ZIP must use one disk.');
  assert(buffer.readUInt16LE(endOffset + 8) === 1 && buffer.readUInt16LE(endOffset + 10) === 1, 'ZIP must contain exactly one entry.');
  assert(buffer.readUInt16LE(endOffset + 20) === 0, 'ZIP comments are not allowed.');
  const centralSize = buffer.readUInt32LE(endOffset + 12);
  const centralOffset = buffer.readUInt32LE(endOffset + 16);
  assert(centralOffset <= endOffset && centralSize <= endOffset - centralOffset && centralOffset + centralSize === endOffset && centralSize >= 46, 'ZIP central directory boundaries are invalid.');
  assert(buffer.readUInt32LE(centralOffset) === 0x02014b50, 'ZIP central entry is invalid.');
  const centralNameLength = buffer.readUInt16LE(centralOffset + 28);
  const centralExtraLength = buffer.readUInt16LE(centralOffset + 30);
  const centralCommentLength = buffer.readUInt16LE(centralOffset + 32);
  assert(46 + centralNameLength + centralExtraLength + centralCommentLength === centralSize, 'ZIP has extra central-directory data.');
  assert(buffer.readUInt8(centralOffset + 5) === 3 && centralExtraLength === 0 && centralCommentLength === 0 && buffer.readUInt16LE(centralOffset + 34) === 0, 'ZIP central metadata is invalid.');
  const centralName = buffer.subarray(centralOffset + 46, centralOffset + 46 + centralNameLength).toString('utf8');
  assert(centralName === expectedName, `ZIP entry is ${centralName}, expected ${expectedName}.`);
  assert(buffer.readUInt16LE(centralOffset + 8) === 0x0800 && buffer.readUInt16LE(centralOffset + 10) === 8, 'ZIP entry flags or method are invalid.');
  assert((buffer.readUInt32LE(centralOffset + 38) >>> 16 & 0o170000) === 0o100000 && buffer.readUInt32LE(centralOffset + 42) === 0, 'ZIP entry is not a regular root file.');
  const compressedSize = buffer.readUInt32LE(centralOffset + 20);
  const uncompressedSize = buffer.readUInt32LE(centralOffset + 24);
  assert(compressedSize <= maximumSize && uncompressedSize <= maximumSize && uncompressedSize === expectedSize, 'ZIP declared sizes exceed policy or manifest.');
  assert(buffer.readUInt32LE(0) === 0x04034b50 && buffer.readUInt16LE(6) === 0x0800 && buffer.readUInt16LE(8) === 8, 'ZIP local metadata is invalid.');
  const localNameLength = buffer.readUInt16LE(26);
  const localExtraLength = buffer.readUInt16LE(28);
  assert(localExtraLength === 0, 'ZIP local extras are not allowed.');
  const dataOffset = 30 + localNameLength;
  assert(dataOffset <= centralOffset && compressedSize <= centralOffset - dataOffset && dataOffset + compressedSize === centralOffset, 'ZIP compressed-data boundary is invalid.');
  const localName = buffer.subarray(30, dataOffset).toString('utf8');
  assert(localName === expectedName && localName === centralName, 'ZIP local and central names differ.');
  for (const [local, central] of [[6, 8], [8, 10], [14, 16], [18, 20], [22, 24]]) {
    const width = local < 14 ? 2 : 4;
    const left = width === 2 ? buffer.readUInt16LE(local) : buffer.readUInt32LE(local);
    const right = width === 2 ? buffer.readUInt16LE(centralOffset + central) : buffer.readUInt32LE(centralOffset + central);
    assert(left === right, 'ZIP local and central metadata differ.');
  }
  let contents;
  try {
    contents = inflateRawSync(buffer.subarray(dataOffset, centralOffset), { maxOutputLength: maximumSize });
  } catch (error) {
    throw new Error(`ZIP inflation exceeded policy or failed: ${error.message}`);
  }
  assert(contents.length === uncompressedSize, 'ZIP inflated size is invalid.');
  const checksum = crc32(contents);
  assert(checksum === buffer.readUInt32LE(14) && checksum === buffer.readUInt32LE(centralOffset + 16), 'ZIP CRC-32 is invalid.');
  return contents;
}

async function componentGateRecord(binarySha256, binarySize, binary) {
  const flags = await configureFlags();
  const configureHash = createHash('sha256').update(`${flags.join('\n')}\n`).digest('hex');
  const { lock, bytes: toolchainLockBytes } = await readToolchainLock();
  return Buffer.from([
    'ffmpeg_version=9.0', `source_sha256=${sourceSha256}`, `source_date_epoch=${sourceDateEpoch}`,
    `configure_sha256=${configureHash}`, `binary_sha256=${binarySha256}`, `binary_size=${binarySize}`,
    `pe_authenticode_content_sha256=${authenticodeContentHash(binary)}`,
    `toolchain_lock_sha256=${createHash('sha256').update(toolchainLockBytes).digest('hex')}`,
    '[toolchain]',
    ...lock.packages.map(({ name, version }) => `${name}=${version}`).sort(),
    `gcc_version=${lock.executables.gcc}`, `ld_version=${lock.executables.ld}`, `make_version=${lock.executables.make}`,
    '[components]', ...exactComponents, '[infrastructure]', 'D3D11VA', 'MEDIAFOUNDATION', '',
  ].join('\n'));
}

async function readToolchainLock() {
  const bytes = await readFile(join(root, 'third_party', 'ffmpeg', 'toolchain-lock.json'));
  let lock;
  try { lock = JSON.parse(bytes.toString('utf8')); } catch { throw new Error('FFmpeg toolchain lock is not valid JSON.'); }
  validateToolchainLock(lock);
  return { lock, bytes: canonicalToolchainLock(lock) };
}

function canonicalToolchainLock(lock) {
  validateToolchainLock(lock);
  const lines = [`schema=${lock.schema}`, `source=${lock.source}`];
  for (const item of lock.packages) lines.push(`package\t${item.name}\t${item.version}\t${item.url}\t${item.sha256}\t${item.signature_url}\t${item.signature_sha256}`);
  lines.push(`gcc=${lock.executables.gcc}`, `ld=${lock.executables.ld}`, `make=${lock.executables.make}`);
  return Buffer.from(`${lines.join('\n')}\n`);
}

function validateToolchainLock(lock) {
  assert(lock && Object.getPrototypeOf(lock) === Object.prototype, 'Toolchain lock must be one object.');
  assert(Object.keys(lock).sort().join(',') === 'executables,packages,schema,source', 'Toolchain lock schema is unexpected.');
  assert(lock.schema === 1 && lock.source === 'https://repo.msys2.org', 'Toolchain lock source/schema is invalid.');
  assert(Array.isArray(lock.packages) && lock.packages.length > 0, 'Toolchain lock packages are missing.');
  const seen = new Set();
  for (const item of lock.packages) {
    assert(item && Object.getPrototypeOf(item) === Object.prototype && Object.keys(item).sort().join(',') === 'name,sha256,signature_sha256,signature_url,url,version', 'Toolchain lock package schema is invalid.');
    assert(/^[a-z0-9][a-z0-9+._-]*$/.test(item.name) && /^[0-9A-Za-z][0-9A-Za-z.+_~-]*$/.test(item.version) && !seen.has(item.name), 'Toolchain lock package identity is invalid or duplicated.');
    seen.add(item.name);
    assert(/^https:\/\/repo\.msys2\.org\/(?:msys\/x86_64|mingw\/ucrt64)\/[A-Za-z0-9+._~-]+\.pkg\.tar\.zst$/.test(item.url) && item.signature_url === `${item.url}.sig`, 'Toolchain lock package URL is invalid.');
    assert(/^[0-9a-f]{64}$/.test(item.sha256) && /^[0-9a-f]{64}$/.test(item.signature_sha256), 'Toolchain lock package hash is invalid.');
  }
  const requiredPackages = ['bash','coreutils','diffutils','gawk','gcc-libs','gmp','grep','libiconv','libintl','libpcre','libreadline','make','mingw-w64-ucrt-x86_64-binutils','mingw-w64-ucrt-x86_64-crt','mingw-w64-ucrt-x86_64-gcc','mingw-w64-ucrt-x86_64-gcc-libs','mingw-w64-ucrt-x86_64-gettext-runtime','mingw-w64-ucrt-x86_64-gmp','mingw-w64-ucrt-x86_64-headers','mingw-w64-ucrt-x86_64-isl','mingw-w64-ucrt-x86_64-libiconv','mingw-w64-ucrt-x86_64-libwinpthread','mingw-w64-ucrt-x86_64-mpc','mingw-w64-ucrt-x86_64-mpfr','mingw-w64-ucrt-x86_64-tzdata','mingw-w64-ucrt-x86_64-windows-default-manifest','mingw-w64-ucrt-x86_64-winpthreads','mingw-w64-ucrt-x86_64-zlib','mingw-w64-ucrt-x86_64-zstd','mpfr','msys2-runtime','nasm','ncurses','pkgconf','sed'];
  assert([...seen].sort().join(',') === requiredPackages.sort().join(','), 'Toolchain package closure differs from the approved exact set.');
  assert(lock.executables && Object.getPrototypeOf(lock.executables) === Object.prototype && Object.keys(lock.executables).sort().join(',') === 'gcc,ld,make', 'Toolchain lock executable schema is invalid.');
  for (const value of Object.values(lock.executables)) assert(typeof value === 'string' && value.length > 0 && !/[\r\n]/.test(value), 'Toolchain lock executable version is invalid.');
}

function validateComponentGate(expected, actual, manifest) {
  assert(Buffer.isBuffer(actual), 'FFmpeg component gate record is absent.');
  assert(actual.equals(expected), 'FFmpeg component gate record is mismatched or stale.');
  assert(createHash('sha256').update(actual).digest('hex') === manifest.component_gate_sha256, 'FFmpeg component gate record hash does not match manifest.');
}

async function configureFlags() {
  return (await readFile(join(root, 'third_party', 'ffmpeg', 'configure.flags'), 'utf8')).split(/\r?\n/).map((line) => line.trim()).filter(Boolean);
}

function run(executable, arguments_) {
  try { return execFileSync(executable, arguments_, { encoding: 'utf8', windowsHide: true, maxBuffer: 16 * 1024 * 1024 }); }
  catch (error) { throw new Error(`FFmpeg command failed: ${arguments_.join(' ')}\n${error.stdout || ''}${error.stderr || ''}`); }
}

function runGoVerification(executable) {
  try {
    const output = execFileSync('go', ['test', './...', '-run', '^(TestBuildGiftClipFFmpegArgsSelectsBoundedPlaybackInput|TestGiftClipPayloadFFmpegProductionArgvSmoke)$', '-count=1'], {
      cwd: join(root, 'goserver'), encoding: 'utf8', env: { ...process.env, GIFT_CLIP_FFMPEG_SMOKE_EXE: executable }, windowsHide: true, maxBuffer: 32 * 1024 * 1024,
    });
    if (output.trim()) console.log(output.trim());
  } catch (error) {
    throw new Error(`Static bounded-argv or production FFmpeg smoke tests failed:\n${error.stdout || ''}${error.stderr || ''}`);
  }
}

function verifyAuthenticode(path) {
  const status = execFileSync('powershell.exe', ['-NoProfile', '-NonInteractive', '-Command', "& { param([string]$path) Import-Module (Join-Path $env:WINDIR 'System32\\WindowsPowerShell\\v1.0\\Modules\\Microsoft.PowerShell.Security'); (Get-AuthenticodeSignature -LiteralPath $path).Status.ToString() }", path], { encoding: 'utf8', windowsHide: true }).trim();
  assert(status === 'Valid', `FFmpeg Authenticode signature status is ${status || 'missing'}, expected Valid.`);
}

function parseProtocols(output) { const result = new Set(); for (const line of output.split(/\r?\n/)) { const value = line.trim(); if (value && !value.endsWith(':') && !value.startsWith('Supported file protocols')) result.add(value); } return result; }
function parseComponents(output, kind) {
  const patterns = { demuxer: /^\s*D\s+([^\s,]+(?:,[^\s,]+)*)\s/, muxer: /^\s*E\s+([^\s,]+(?:,[^\s,]+)*)\s/, decoder: /^\s*[VAS][A-Z.]{5}\s+([^\s]+)\s/, encoder: /^\s*[VAS][A-Z.]{5}\s+([^\s]+)\s/, filter: /^\s*[TS.]{2}\s+([^\s]+)\s/ };
  const result = new Set(); for (const line of output.split(/\r?\n/)) { const match = line.match(patterns[kind]); if (match) for (const name of match[1].split(',')) if (name !== '=') result.add(name); } return result;
}
function parseSimpleList(output, header) { const result = new Set(); for (const line of output.split(/\r?\n/)) { const value = line.trim(); if (value && value !== header) result.add(value); } return result; }
function parseDevices(output) { const result = new Set(); for (const line of output.split(/\r?\n/)) { const match = line.match(/^\s*[D.][E.]\s+([^\s]+)\s/); if (match && match[1] !== '=') result.add(match[1]); } return result; }
function assertExactSet(actual, expected, label) { const unexpected = [...actual].filter((name) => !expected.has(name)); const missing = [...expected].filter((name) => !actual.has(name)); assert(!unexpected.length && !missing.length, `${label} whitelist mismatch; unexpected=[${unexpected}], missing=[${missing}].`); }
function crc32(contents) { let crc = 0xffffffff; for (const byte of contents) { crc ^= byte; for (let bit = 0; bit < 8; bit += 1) crc = (crc >>> 1) ^ (0xedb88320 & -(crc & 1)); } return (crc ^ 0xffffffff) >>> 0; }
function authenticodeContentHash(binary) {
  assert(binary.length >= 0x100 && binary.readUInt16LE(0) === 0x5a4d, 'FFmpeg input is not a valid PE image.');
  const pe = binary.readUInt32LE(0x3c); assert(pe + 24 <= binary.length && binary.readUInt32LE(pe) === 0x00004550, 'FFmpeg input PE header is invalid.');
  const optional = pe + 24; const magic = binary.readUInt16LE(optional); const dataDirectory = optional + (magic === 0x20b ? 112 : magic === 0x10b ? 96 : -1);
  const checksum = optional + 64; const securityDirectory = dataDirectory + 32; assert(dataDirectory >= optional && securityDirectory + 8 <= binary.length, 'FFmpeg input PE optional header is invalid.');
  const certificateOffset = binary.readUInt32LE(securityDirectory); const certificateSize = binary.readUInt32LE(securityDirectory + 4);
  assert((certificateOffset === 0) === (certificateSize === 0) && certificateOffset <= binary.length && certificateSize <= binary.length - certificateOffset && (!certificateSize || certificateOffset + certificateSize === binary.length), 'FFmpeg input PE certificate table is invalid.');
  const normalized = Buffer.from(binary.subarray(0, certificateSize ? certificateOffset : binary.length)); normalized.fill(0, checksum, checksum + 4); normalized.fill(0, securityDirectory, securityDirectory + 8);
  return createHash('sha256').update(normalized).digest('hex');
}
function assert(condition, message) { if (!condition) throw new Error(message); }

async function runSelfTests() {
  const lockFixture = JSON.parse(await readFile(join(root, 'third_party', 'ffmpeg', 'toolchain-lock.json'), 'utf8'));
  validateToolchainLock(lockFixture);
  const crlfFixture = JSON.parse(JSON.stringify(lockFixture, null, 2).replaceAll('\n', '\r\n'));
  assert(canonicalToolchainLock(lockFixture).equals(canonicalToolchainLock(crlfFixture)), 'Toolchain canonical hash depends on line endings.');
  assertThrows(() => validateToolchainLock({ ...lockFixture, extra: true }), 'toolchain lock unknown field');
  assertThrows(() => validateToolchainLock({ ...lockFixture, packages: [...lockFixture.packages, lockFixture.packages[0]] }), 'toolchain lock duplicate package');
  assertThrows(() => validateToolchainLock({ ...lockFixture, packages: [{ ...lockFixture.packages[0], sha256: 'bad' }, ...lockFixture.packages.slice(1)] }), 'toolchain lock package hash');
  assertThrows(() => validateToolchainLock({ ...lockFixture, packages: lockFixture.packages.slice(1) }), 'toolchain lock missing closure package');
  const body = Buffer.from('MZ-zip-test');
  const valid = testZip(body);
  assert(readSingleFileZip(valid, 'ffmpeg.exe', body.length).equals(body), 'valid strict ZIP self-test failed.');
  const central = valid.length - 22 - 46 - 'ffmpeg.exe'.length;
  for (const [name, mutate, expectedSize] of [
    ['huge declared size', (data) => { data.writeUInt32LE(maximumSize + 1, 22); data.writeUInt32LE(maximumSize + 1, central + 24); }, maximumSize + 1],
    ['compressed bounds', (data) => { data.writeUInt32LE(maximumSize, 18); data.writeUInt32LE(maximumSize, central + 20); }, body.length],
  ]) {
    const data = Buffer.from(valid); mutate(data); assertThrows(() => readSingleFileZip(data, 'ffmpeg.exe', expectedSize), name);
  }
  const bomb = testZip(Buffer.alloc(maximumSize + 1));
  const bombCentral = bomb.length - 22 - 46 - 'ffmpeg.exe'.length;
  bomb.writeUInt32LE(maximumSize, 22); bomb.writeUInt32LE(maximumSize, bombCentral + 24);
  assertThrows(() => readSingleFileZip(bomb, 'ffmpeg.exe', maximumSize), 'expansion bomb');
  const baseManifest = `{"version":"9.0","sha256":"${'0'.repeat(64)}","archive_sha256":"${'1'.repeat(64)}","component_gate":"gate\\n","component_gate_sha256":"${'2'.repeat(64)}","size":1,"authenticode":false}`;
  for (const invalid of [`${baseManifest}{}`, baseManifest.replace('"size":1', '"size":1,"size":1'), baseManifest.replace('"size":1', '"size":1,"extra":1'), baseManifest.replace(/}$/, ',}')]) assertThrows(() => parseManifest(invalid), 'strict manifest');
  const expectedGate = Buffer.from('expected component gate\n');
  const gateManifest = { component_gate_sha256: createHash('sha256').update(expectedGate).digest('hex') };
  assertThrows(() => validateComponentGate(expectedGate, undefined, gateManifest), 'missing component gate record');
  assertThrows(() => validateComponentGate(expectedGate, Buffer.from('stale component gate\n'), gateManifest), 'stale component gate record');
  validateComponentGate(expectedGate, expectedGate, gateManifest);
  console.log('verifier adversarial self-tests passed');
}

function testZip(contents) {
  const name = Buffer.from('ffmpeg.exe'); const compressed = deflateRawSync(contents, { level: 9 }); const checksum = crc32(contents);
  const local = Buffer.alloc(30 + name.length); local.writeUInt32LE(0x04034b50); local.writeUInt16LE(20, 4); local.writeUInt16LE(0x800, 6); local.writeUInt16LE(8, 8); local.writeUInt32LE(checksum, 14); local.writeUInt32LE(compressed.length, 18); local.writeUInt32LE(contents.length, 22); local.writeUInt16LE(name.length, 26); name.copy(local, 30);
  const central = Buffer.alloc(46 + name.length); central.writeUInt32LE(0x02014b50); central.writeUInt16LE(0x314, 4); central.writeUInt16LE(20, 6); central.writeUInt16LE(0x800, 8); central.writeUInt16LE(8, 10); central.writeUInt32LE(checksum, 16); central.writeUInt32LE(compressed.length, 20); central.writeUInt32LE(contents.length, 24); central.writeUInt16LE(name.length, 28); central.writeUInt32LE((0o100755 << 16) >>> 0, 38); name.copy(central, 46);
  const end = Buffer.alloc(22); end.writeUInt32LE(0x06054b50); end.writeUInt16LE(1, 8); end.writeUInt16LE(1, 10); end.writeUInt32LE(central.length, 12); end.writeUInt32LE(local.length + compressed.length, 16); return Buffer.concat([local, compressed, central, end]);
}
function assertThrows(callback, label) { try { callback(); } catch { return; } throw new Error(`${label} was accepted`); }
