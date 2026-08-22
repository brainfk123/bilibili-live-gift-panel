import { createHash } from 'node:crypto';
import { readFile } from 'node:fs/promises';
import { dirname, join, resolve } from 'node:path';
import { fileURLToPath, pathToFileURL } from 'node:url';

export const FFMPEG_VERSION = '9.0';
export const FFMPEG_SOURCE_SHA256 = '7f607a00dd0d28a729d5a4811205812eef01cf6ef6155025febb6f36a9062d52';
export const FFMPEG_SOURCE_SIGNATURE_SHA256 = 'f9607bb4d90bbaeff196318a55547da66c9f5921dad39b1c49b7544286e4876c';
export const FFMPEG_SOURCE_DATE_EPOCH = '1785797913';
export const FFMPEG_COMPONENTS = Object.freeze([
  'AAC_ADTSTOASC_BSF', 'AC3_PARSER', 'AFORMAT_FILTER', 'ALPHAMERGE_FILTER', 'ANULL_FILTER', 'ATRIM_FILTER',
  'CROP_FILTER', 'FILE_PROTOCOL', 'FORMAT_FILTER', 'FPS_FILTER', 'GIF_DECODER', 'GIF_DEMUXER', 'GIF_PARSER',
  'H264_DECODER', 'H264_MF_ENCODER', 'H264_PARSER', 'HFLIP_FILTER', 'IMAGE2_DEMUXER',
  'IMAGE_WEBP_PIPE_DEMUXER', 'MOV_DEMUXER', 'MOV_MUXER', 'MP4_MUXER', 'NULL_FILTER', 'OVERLAY_FILTER',
  'PIPE_PROTOCOL', 'PNG_DECODER', 'ROTATE_FILTER', 'SCALE_FILTER', 'SETPTS_FILTER', 'SPLIT_FILTER',
  'TRANSPOSE_FILTER', 'TRIM_FILTER', 'VFLIP_FILTER', 'VP8_DECODER', 'VP9_SUPERFRAME_BSF',
  'WEBP_ANIM_DECODER', 'WEBP_ANIM_DEMUXER', 'WEBP_DECODER',
]);
export const FFMPEG_INFRASTRUCTURE = Object.freeze(['D3D11VA', 'MEDIAFOUNDATION']);

const requiredPackages = Object.freeze([
  'bash','coreutils','diffutils','gawk','gcc-libs','gmp','grep','libiconv','libintl','libpcre','libreadline','make','mingw-w64-ucrt-x86_64-binutils','mingw-w64-ucrt-x86_64-crt','mingw-w64-ucrt-x86_64-gcc','mingw-w64-ucrt-x86_64-gcc-libs','mingw-w64-ucrt-x86_64-gettext-runtime','mingw-w64-ucrt-x86_64-gmp','mingw-w64-ucrt-x86_64-headers','mingw-w64-ucrt-x86_64-isl','mingw-w64-ucrt-x86_64-libiconv','mingw-w64-ucrt-x86_64-libwinpthread','mingw-w64-ucrt-x86_64-mpc','mingw-w64-ucrt-x86_64-mpfr','mingw-w64-ucrt-x86_64-tzdata','mingw-w64-ucrt-x86_64-windows-default-manifest','mingw-w64-ucrt-x86_64-winpthreads','mingw-w64-ucrt-x86_64-zlib','mingw-w64-ucrt-x86_64-zstd','mpfr','msys2-runtime','nasm','ncurses','pkgconf','sed',
]);

export async function loadFFmpegPolicy(root) {
  const flags = (await readFile(join(root, 'third_party', 'ffmpeg', 'configure.flags'), 'utf8'))
    .split(/\r?\n/).map((line) => line.trim()).filter(Boolean);
  assert(flags.length > 0 && flags.every((flag) => flag.startsWith('--') && !/[\r\n]/.test(flag)), 'FFmpeg configure flags are invalid.');
  const configureBytes = Buffer.from(`${flags.join('\n')}\n`);
  let lock;
  try {
    lock = JSON.parse(await readFile(join(root, 'third_party', 'ffmpeg', 'toolchain-lock.json'), 'utf8'));
  } catch (error) {
    throw new Error(`FFmpeg toolchain lock is not valid JSON: ${error.message}`);
  }
  validateToolchainLock(lock);
  const toolchainLockBytes = canonicalToolchainLock(lock);
  return {
    schema: 1,
    version: FFMPEG_VERSION,
    sourceSha256: FFMPEG_SOURCE_SHA256,
    sourceDateEpoch: FFMPEG_SOURCE_DATE_EPOCH,
    configureFlags: flags,
    configureSha256: sha256(configureBytes),
    toolchainLock: lock,
    toolchainLockBytes,
    toolchainLockSha256: sha256(toolchainLockBytes),
    components: [...FFMPEG_COMPONENTS],
    infrastructure: [...FFMPEG_INFRASTRUCTURE],
  };
}

export function serializeFFmpegDescriptor(policy) {
  assert(policy?.schema === 1, 'FFmpeg descriptor schema is invalid.');
  assert(/^\d+\.\d+$/.test(policy.version), 'FFmpeg version is invalid.');
  assertHash(policy.sourceSha256, 'source SHA-256');
  assert(/^(?:0|[1-9]\d*)$/.test(policy.sourceDateEpoch), 'FFmpeg source epoch is invalid.');
  assertHash(policy.configureSha256, 'configure SHA-256');
  assertHash(policy.toolchainLockSha256, 'toolchain lock SHA-256');
  validateCanonicalList(policy.components, 'components');
  validateCanonicalList(policy.infrastructure, 'infrastructure');
  const descriptor = Buffer.from([
    `schema=${policy.schema}`,
    `ffmpeg_version=${policy.version}`,
    `source_sha256=${policy.sourceSha256}`,
    `source_date_epoch=${policy.sourceDateEpoch}`,
    `configure_sha256=${policy.configureSha256}`,
    `toolchain_lock_sha256=${policy.toolchainLockSha256}`,
    '[components]', ...policy.components,
    '[infrastructure]', ...policy.infrastructure,
    '',
  ].join('\n'));
  assert(descriptor.length <= 16_384, 'FFmpeg component descriptor is oversized.');
  return descriptor;
}

export function ffmpegComponentIdentity(policy) {
  const descriptor = serializeFFmpegDescriptor(policy);
  const fingerprint = sha256(descriptor);
  return {
    descriptor,
    descriptorSha256: fingerprint,
    fingerprint,
    tag: `ffmpeg-component-v1-${fingerprint}`,
  };
}

export function componentGateRecord(policy, binary) {
  assert(Buffer.isBuffer(binary) && binary.length > 0, 'FFmpeg binary is missing.');
  const binarySha256 = sha256(binary);
  return Buffer.from([
    `ffmpeg_version=${policy.version}`,
    `source_sha256=${policy.sourceSha256}`,
    `source_date_epoch=${policy.sourceDateEpoch}`,
    `configure_sha256=${policy.configureSha256}`,
    `binary_sha256=${binarySha256}`,
    `binary_size=${binary.length}`,
    `pe_authenticode_content_sha256=${authenticodeContentHash(binary)}`,
    `toolchain_lock_sha256=${policy.toolchainLockSha256}`,
    '[toolchain]',
    ...policy.toolchainLock.packages.map(({ name, version }) => `${name}=${version}`).sort(),
    `gcc_version=${policy.toolchainLock.executables.gcc}`,
    `ld_version=${policy.toolchainLock.executables.ld}`,
    `make_version=${policy.toolchainLock.executables.make}`,
    '[components]', ...policy.components,
    '[infrastructure]', ...policy.infrastructure,
    '',
  ].join('\n'));
}

export function canonicalToolchainLock(lock) {
  validateToolchainLock(lock);
  const lines = [`schema=${lock.schema}`, `source=${lock.source}`];
  for (const item of lock.packages) lines.push(`package\t${item.name}\t${item.version}\t${item.url}\t${item.sha256}\t${item.signature_url}\t${item.signature_sha256}`);
  lines.push(`gcc=${lock.executables.gcc}`, `ld=${lock.executables.ld}`, `make=${lock.executables.make}`);
  return Buffer.from(`${lines.join('\n')}\n`);
}

export function validateToolchainLock(lock) {
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
    assertHash(item.sha256, 'toolchain package SHA-256');
    assertHash(item.signature_sha256, 'toolchain package signature SHA-256');
  }
  assert([...seen].sort().join(',') === [...requiredPackages].sort().join(','), 'Toolchain package closure differs from the approved exact set.');
  assert(lock.executables && Object.getPrototypeOf(lock.executables) === Object.prototype && Object.keys(lock.executables).sort().join(',') === 'gcc,ld,make', 'Toolchain lock executable schema is invalid.');
  for (const value of Object.values(lock.executables)) assert(typeof value === 'string' && value.length > 0 && !/[\r\n]/.test(value), 'Toolchain lock executable version is invalid.');
}

export function authenticodeContentHash(binary) {
  assert(binary.length >= 0x100 && binary.readUInt16LE(0) === 0x5a4d, 'FFmpeg input is not a valid PE image.');
  const pe = binary.readUInt32LE(0x3c);
  assert(pe + 24 <= binary.length && binary.readUInt32LE(pe) === 0x00004550, 'FFmpeg input PE header is invalid.');
  const optional = pe + 24;
  const magic = binary.readUInt16LE(optional);
  const dataDirectory = optional + (magic === 0x20b ? 112 : magic === 0x10b ? 96 : -1);
  const checksum = optional + 64;
  const securityDirectory = dataDirectory + 32;
  assert(dataDirectory >= optional && securityDirectory + 8 <= binary.length, 'FFmpeg input PE optional header is invalid.');
  const certificateOffset = binary.readUInt32LE(securityDirectory);
  const certificateSize = binary.readUInt32LE(securityDirectory + 4);
  assert((certificateOffset === 0) === (certificateSize === 0) && certificateOffset <= binary.length && certificateSize <= binary.length - certificateOffset && (!certificateSize || certificateOffset + certificateSize === binary.length), 'FFmpeg input PE certificate table is invalid.');
  const normalized = Buffer.from(binary.subarray(0, certificateSize ? certificateOffset : binary.length));
  normalized.fill(0, checksum, checksum + 4);
  normalized.fill(0, securityDirectory, securityDirectory + 8);
  return sha256(normalized);
}

function validateCanonicalList(values, label) {
  assert(Array.isArray(values) && values.length > 0 && values.every((value) => typeof value === 'string' && /^[A-Z0-9_]+$/.test(value)), `FFmpeg ${label} are invalid.`);
  const sorted = [...values].sort();
  assert(new Set(values).size === values.length, `FFmpeg ${label} are duplicated.`);
  assert(values.every((value, index) => value === sorted[index]), `FFmpeg ${label} must be sorted.`);
}

function assertHash(value, label) {
  assert(typeof value === 'string' && /^[0-9a-f]{64}$/.test(value), `FFmpeg ${label} is malformed.`);
}

function sha256(value) {
  return createHash('sha256').update(value).digest('hex');
}

function assert(condition, message) {
  if (!condition) throw new Error(message);
}

async function runSelfTests() {
  const root = resolve(dirname(fileURLToPath(import.meta.url)), '..');
  const policy = await loadFFmpegPolicy(root);
  const identity = ffmpegComponentIdentity(policy);
  assert(/^ffmpeg-component-v1-[0-9a-f]{64}$/.test(identity.tag), 'FFmpeg component tag is malformed.');
  assert(identity.descriptor.equals(serializeFFmpegDescriptor(policy)), 'FFmpeg descriptor is unstable.');
  console.log(`FFmpeg policy self-tests passed (${identity.tag}).`);
}

if (process.argv[1] && import.meta.url === pathToFileURL(resolve(process.argv[1])).href) {
  if (process.argv.length === 3 && process.argv[2] === '--self-test') await runSelfTests();
  else throw new Error('Usage: node scripts/ffmpeg-policy.mjs --self-test');
}
