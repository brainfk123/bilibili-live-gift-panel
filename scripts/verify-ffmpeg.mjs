import { createHash } from 'node:crypto';
import { execFileSync } from 'node:child_process';
import { chmod, mkdtemp, readFile, rm, writeFile } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';
import { deflateRawSync, inflateRawSync } from 'node:zlib';
import {
  canonicalToolchainLock,
  componentGateRecord,
  ffmpegComponentIdentity,
  loadFFmpegPolicy,
  validateToolchainLock,
} from './ffmpeg-policy.mjs';

const maximumSize = 40_000_000;
const warningSize = 30_000_000;
const root = join(dirname(fileURLToPath(import.meta.url)), '..');

if (process.argv.includes('--self-test')) await runSelfTests();
else await main();

async function main() {
  const policy = await loadFFmpegPolicy(root);
  const identity = ffmpegComponentIdentity(policy);
  const payloadOnly = process.argv.includes('--payload-only');
  const payloadDirectory = join(root, 'goserver', 'ffmpeg');
  const manifest = parseManifest(await readFile(join(payloadDirectory, 'manifest.json'), 'utf8'));
  const archive = await readFile(join(payloadDirectory, 'ffmpeg.zip'));
  validateManifest(manifest, archive, identity);
  if (archive.length > warningSize) console.warn(`WARNING: FFmpeg ZIP exceeds the ${warningSize}-byte target: ${archive.length}`);
  const binary = readSingleFileZip(archive, 'ffmpeg.exe', manifest.size);
  assert(createHash('sha256').update(binary).digest('hex') === manifest.sha256, 'Binary SHA-256 does not match manifest.');
  const expectedGate = componentGateRecord(policy, binary);
  validateComponentGate(expectedGate, Buffer.from(manifest.component_gate, 'utf8'), manifest);
  if ((process.env.APP_VERSION || 'dev').replace(/^v/, '') !== 'dev' && manifest.authenticode !== true) {
    throw new Error('Release verification requires an Authenticode-signed inner FFmpeg payload.');
  }

  const temporaryRoot = await mkdtemp(join(tmpdir(), 'gift-panel-ffmpeg-verify-'));
  try {
    const executable = join(temporaryRoot, 'ffmpeg.exe');
    await writeFile(executable, binary, { flag: 'wx' });
    await chmod(executable, 0o700);
    if (manifest.authenticode) verifyAuthenticode(executable, manifest.signer_subject);
    verifyRuntimeSurface(executable, policy.configureFlags);
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
  const allowed = new Set(['schema', 'component_fingerprint', 'descriptor', 'descriptor_sha256', 'version', 'sha256', 'archive_sha256', 'component_gate', 'component_gate_sha256', 'size', 'authenticode', 'signer_subject', 'source_release_commit']);
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

function validateManifest(value, archive, identity) {
  const keys = Object.keys(value).sort().join(',');
  assert(keys === 'archive_sha256,authenticode,component_fingerprint,component_gate,component_gate_sha256,descriptor,descriptor_sha256,schema,sha256,signer_subject,size,source_release_commit,version', `Unexpected manifest schema: ${keys}`);
  assert(value.schema === 1, 'Manifest schema version is invalid.');
  assert(value.version === '9.0', `Manifest version is ${value.version}.`);
  for (const [name, hash] of [['component fingerprint', value.component_fingerprint], ['descriptor', value.descriptor_sha256], ['binary', value.sha256], ['archive', value.archive_sha256], ['component gate', value.component_gate_sha256]]) {
    assert(/^[0-9a-f]{64}$/.test(hash), `Manifest ${name} SHA-256 is malformed.`);
  }
  assert(typeof value.descriptor === 'string' && Buffer.byteLength(value.descriptor) > 0 && Buffer.byteLength(value.descriptor) <= 16_384, 'Manifest descriptor is invalid.');
  assert(createHash('sha256').update(value.descriptor).digest('hex') === value.descriptor_sha256, 'Manifest descriptor SHA-256 does not match descriptor.');
  assert(value.component_fingerprint === identity.fingerprint && value.descriptor_sha256 === identity.descriptorSha256 && value.descriptor === identity.descriptor.toString('utf8'), 'Manifest component identity does not match local policy.');
  assert(Number.isSafeInteger(value.size) && value.size > 0 && value.size <= maximumSize, 'Manifest size is invalid.');
  assert(typeof value.authenticode === 'boolean', 'Manifest authenticode field is invalid.');
  assert(typeof value.signer_subject === 'string' && !/[\r\n]/.test(value.signer_subject), 'Manifest signer subject is invalid.');
  assert(typeof value.source_release_commit === 'string' && /^[0-9a-f]{40}$/.test(value.source_release_commit), 'Manifest source release commit is invalid.');
  if (value.authenticode) {
    assert(value.signer_subject.length > 0 && value.source_release_commit !== '0'.repeat(40), 'Signed manifest metadata is invalid.');
    assert(value.signer_subject === process.env.EVSIGN_EXPECTED_SUBJECT?.trim(), 'Manifest signer subject does not match EVSIGN_EXPECTED_SUBJECT.');
  } else {
    assert(value.signer_subject === '' && value.source_release_commit === '0'.repeat(40), 'Unsigned development manifest metadata is invalid.');
  }
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

function validateComponentGate(expected, actual, manifest) {
  assert(Buffer.isBuffer(actual), 'FFmpeg component gate record is absent.');
  assert(actual.equals(expected), 'FFmpeg component gate record is mismatched or stale.');
  assert(createHash('sha256').update(actual).digest('hex') === manifest.component_gate_sha256, 'FFmpeg component gate record hash does not match manifest.');
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

function verifyAuthenticode(path, expectedSubject) {
  const output = execFileSync('powershell.exe', ['-NoProfile', '-NonInteractive', '-Command', "& { param([string]$path) Import-Module (Join-Path $env:WINDIR 'System32\\WindowsPowerShell\\v1.0\\Modules\\Microsoft.PowerShell.Security'); $signature = Get-AuthenticodeSignature -LiteralPath $path; @{ status = $signature.Status.ToString(); subject = if ($null -eq $signature.SignerCertificate) { '' } else { $signature.SignerCertificate.Subject } } | ConvertTo-Json -Compress }", path], { encoding: 'utf8', windowsHide: true }).trim();
  let signature;
  try { signature = JSON.parse(output); } catch { throw new Error('FFmpeg Authenticode verification returned malformed output.'); }
  assert(signature.status === 'Valid', `FFmpeg Authenticode signature status is ${signature.status || 'missing'}, expected Valid.`);
  assert(signature.subject === expectedSubject, 'FFmpeg Authenticode signer subject does not match manifest.');
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
function assert(condition, message) { if (!condition) throw new Error(message); }

async function runSelfTests() {
  const policy = await loadFFmpegPolicy(root);
  const identity = ffmpegComponentIdentity(policy);
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
  const baseManifest = JSON.stringify({ schema: 1, component_fingerprint: identity.fingerprint, descriptor: identity.descriptor.toString('utf8'), descriptor_sha256: identity.descriptorSha256, version: '9.0', sha256: '0'.repeat(64), archive_sha256: '1'.repeat(64), component_gate: 'gate\n', component_gate_sha256: '2'.repeat(64), size: 1, authenticode: false, signer_subject: '', source_release_commit: '0'.repeat(40) });
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
