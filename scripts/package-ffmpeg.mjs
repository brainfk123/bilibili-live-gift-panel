import { createHash, randomBytes } from 'node:crypto';
import { execFile, execFileSync } from 'node:child_process';
import { link, mkdir, mkdtemp, open, readFile, readdir, rename, rm, stat, writeFile } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { dirname, join, resolve } from 'node:path';
import { fileURLToPath, pathToFileURL } from 'node:url';
import { deflateRawSync } from 'node:zlib';
import { ffmpegComponentIdentity, loadFFmpegPolicy } from './ffmpeg-policy.mjs';

const version = '9.0';
const warningSize = 30_000_000;
const maximumSize = 40_000_000;
const root = join(dirname(fileURLToPath(import.meta.url)), '..');
const outputDirectory = join(root, 'goserver', 'ffmpeg');
const componentGatePath = join(root, 'dist', 'ffmpeg-component-gate.txt');
const ownerLivenessCache = new Map();

if (process.argv[1] && import.meta.url === pathToFileURL(resolve(process.argv[1])).href) {
  if (process.argv.includes('--publish-worker')) {
    const directory = resolve(readArgument('--directory'));
    const archive = await readFile(resolve(readArgument('--archive')));
    const manifest = await readFile(resolve(readArgument('--manifest')));
    const options = JSON.parse(Buffer.from(readArgument('--options'), 'base64url').toString('utf8'));
    await publishPairInWorker(directory, archive, manifest, options);
  } else if (process.argv.includes('--self-test')) {
    await runSelfTests();
  } else {
    await main();
  }
}

async function main() {
  const input = readArgument('--input');
  if (!input) throw new Error('Usage: node scripts/package-ffmpeg.mjs --input <ffmpeg.exe>');
  const binary = await readFile(resolve(input));
  let componentGate = await readFile(componentGatePath);
  validatePackageInputs(binary, componentGate);
  const authenticode = process.env.FFMPEG_AUTHENTICODE === 'true';
  const appVersion = (process.env.APP_VERSION || 'dev').replace(/^v/, '');
  if (appVersion !== 'dev' && !authenticode) {
    throw new Error('Release FFmpeg packaging requires FFMPEG_AUTHENTICODE=true after signing the inner executable.');
  }
  if (authenticode) await verifyAuthenticode(binary);
  componentGate = bindBuildRecordToBinary(componentGate, binary, authenticode);
  const identity = ffmpegComponentIdentity(await loadFFmpegPolicy(root));

  const sha256 = createHash('sha256').update(binary).digest('hex');
  const archive = writeSingleFileZip('ffmpeg.exe', binary);
  if (archive.length > maximumSize) throw new Error(`FFmpeg ZIP is ${archive.length} bytes; hard limit is ${maximumSize} bytes.`);
  if (archive.length > warningSize) console.warn(`WARNING: FFmpeg ZIP is ${archive.length} bytes, above the ${warningSize}-byte target.`);
  const signerSubject = authenticode ? process.env.EVSIGN_EXPECTED_SUBJECT?.trim() || '' : '';
  const sourceReleaseCommit = authenticode ? process.env.APP_COMMIT?.trim() || '' : '0'.repeat(40);
  const manifest = buildPackageManifest({ identity, binary, archive, componentGate, authenticode, signerSubject, sourceReleaseCommit });
  await mkdir(outputDirectory, { recursive: true });
  await publishPairTransactionally(outputDirectory, archive, Buffer.from(`${JSON.stringify(manifest, null, 2)}\n`));
  console.log(`packaged FFmpeg ${version}: ${binary.length} bytes, ZIP ${archive.length} bytes, SHA-256 ${sha256}, authenticode=${authenticode}`);
}

export function buildPackageManifest({ identity, binary, archive, componentGate, authenticode, signerSubject, sourceReleaseCommit }) {
  if (!identity || !Buffer.isBuffer(identity.descriptor) || !/^[0-9a-f]{64}$/.test(identity.descriptorSha256) || identity.fingerprint !== identity.descriptorSha256) {
    throw new Error('FFmpeg component identity is invalid.');
  }
  if (!Buffer.isBuffer(binary) || binary.length === 0 || !Buffer.isBuffer(archive) || archive.length === 0 || !Buffer.isBuffer(componentGate) || componentGate.length === 0) {
    throw new Error('FFmpeg package inputs are invalid.');
  }
  if (!/^[0-9a-f]{40}$/.test(sourceReleaseCommit)) throw new Error('FFmpeg source release commit is invalid.');
  if (authenticode) {
    if (typeof signerSubject !== 'string' || signerSubject.length === 0 || /[\r\n]/.test(signerSubject)) throw new Error('FFmpeg signer subject is invalid.');
    if (/^0{40}$/.test(sourceReleaseCommit)) throw new Error('Signed FFmpeg source release commit is invalid.');
  } else if (signerSubject !== '' || sourceReleaseCommit !== '0'.repeat(40)) {
    throw new Error('Unsigned development FFmpeg metadata is invalid.');
  }
  return {
    schema: 1,
    component_fingerprint: identity.fingerprint,
    descriptor: identity.descriptor.toString('utf8'),
    descriptor_sha256: identity.descriptorSha256,
    version,
    sha256: createHash('sha256').update(binary).digest('hex'),
    archive_sha256: createHash('sha256').update(archive).digest('hex'),
    component_gate: componentGate.toString('utf8'),
    component_gate_sha256: createHash('sha256').update(componentGate).digest('hex'),
    size: binary.length,
    authenticode,
    signer_subject: signerSubject,
    source_release_commit: sourceReleaseCommit,
  };
}

function validatePackageInputs(binary, componentGate) {
  if (binary.length === 0) throw new Error('FFmpeg input is empty.');
  if (binary.length > maximumSize) throw new Error(`FFmpeg binary is ${binary.length} bytes; hard limit is ${maximumSize} bytes.`);
  if (componentGate.length === 0 || componentGate.length > 16_384) throw new Error('FFmpeg component gate record is empty or oversized.');
}

function validateBuildRecord(record, binary) {
  const text = record.toString('utf8');
  const expectedHash = createHash('sha256').update(binary).digest('hex');
  const hash = text.match(/^binary_sha256=([0-9a-f]{64})$/m)?.[1];
  const size = text.match(/^binary_size=([1-9][0-9]*)$/m)?.[1];
  const peContentHash = text.match(/^pe_authenticode_content_sha256=([0-9a-f]{64})$/m)?.[1];
  if (hash !== expectedHash || size !== String(binary.length) || peContentHash !== authenticodeContentHash(binary)) throw new Error('FFmpeg component gate binary hash/size/PE-content mismatch.');
}

function bindBuildRecordToBinary(record, binary, signed) {
  try {
    validateBuildRecord(record, binary);
    return record;
  } catch (error) {
    if (!signed) throw error;
  }
  const text = record.toString('utf8');
  const recordedContentHash = text.match(/^pe_authenticode_content_sha256=([0-9a-f]{64})$/m)?.[1];
  if (!/^binary_sha256=[0-9a-f]{64}$/m.test(text) || !/^binary_size=[1-9][0-9]*$/m.test(text) || recordedContentHash !== authenticodeContentHash(binary)) throw new Error('FFmpeg signed binary does not derive from the recorded PE image.');
  return Buffer.from(text
    .replace(/^binary_sha256=[0-9a-f]{64}$/m, `binary_sha256=${createHash('sha256').update(binary).digest('hex')}`)
    .replace(/^binary_size=[1-9][0-9]*$/m, `binary_size=${binary.length}`));
}

function authenticodeContentHash(binary) {
  if (binary.length < 0x100 || binary.readUInt16LE(0) !== 0x5a4d) throw new Error('FFmpeg input is not a valid PE image.');
  const pe = binary.readUInt32LE(0x3c);
  if (pe + 24 > binary.length || binary.readUInt32LE(pe) !== 0x00004550) throw new Error('FFmpeg input PE header is invalid.');
  const optional = pe + 24;
  const magic = binary.readUInt16LE(optional);
  const dataDirectory = optional + (magic === 0x20b ? 112 : magic === 0x10b ? 96 : -1);
  const checksum = optional + 64;
  const securityDirectory = dataDirectory + 8 * 4;
  if (dataDirectory < optional || securityDirectory + 8 > binary.length) throw new Error('FFmpeg input PE optional header is invalid.');
  const certificateOffset = binary.readUInt32LE(securityDirectory);
  const certificateSize = binary.readUInt32LE(securityDirectory + 4);
  if ((certificateOffset === 0) !== (certificateSize === 0) || certificateOffset > binary.length || certificateSize > binary.length - certificateOffset || (certificateSize && certificateOffset + certificateSize !== binary.length)) throw new Error('FFmpeg input PE certificate table is invalid.');
  const end = certificateSize ? certificateOffset : binary.length;
  const normalized = Buffer.from(binary.subarray(0, end));
  normalized.fill(0, checksum, checksum + 4);
  normalized.fill(0, securityDirectory, securityDirectory + 8);
  return createHash('sha256').update(normalized).digest('hex');
}

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

export async function publishPairTransactionally(directory, archive, manifest, options = {}) {
  const inputs = await mkdtemp(join(tmpdir(), 'gift-panel-ffmpeg-publish-'));
  const archivePath = join(inputs, 'archive.new');
  const manifestPath = join(inputs, 'manifest.new');
  try {
    await Promise.all([writeDurably(archivePath, archive), writeDurably(manifestPath, manifest)]);
    await new Promise((resolveWorker, rejectWorker) => {
      const child = execFile(process.execPath, [fileURLToPath(import.meta.url), '--publish-worker', '--directory', resolve(directory), '--archive', archivePath, '--manifest', manifestPath, '--options', Buffer.from(JSON.stringify(options)).toString('base64url')], { windowsHide: true }, (error, stdout, stderr) => {
        if (error) rejectWorker(new Error(`FFmpeg pair publication worker failed: ${error.message}; ${stderr}`));
        else resolveWorker(stdout);
      });
      child.stdin?.end();
    });
  } finally {
    await rm(inputs, { recursive: true, force: true });
  }
}

async function publishPairInWorker(directory, archive, manifest, options = {}) {
  const nonce = `${process.pid}-${randomBytes(8).toString('hex')}`;
  const lockPath = join(directory, '.ffmpeg-package.lock');
  const journalPath = join(directory, '.ffmpeg-package.transaction.json');
  const targets = [join(directory, 'ffmpeg.zip'), join(directory, 'manifest.json')];
  const newPaths = targets.map((path) => `${path}.partial-${nonce}`);
  const backupPaths = targets.map((path) => `${path}.backup-${nonce}`);
  let publicationMutex;
  const checkpoint = async (name) => {
    publicationMutex?.assertHeld();
    if (options.killMutexAt === name) process.exit(86);
    if (options.crashAt === name) {
      const error = new Error(`injected package publication crash: ${name}`);
      error.simulatedCrash = true;
      throw error;
    }
    if (options.rollbackFailAt === name) throw new Error(`injected package rollback failure: ${name}`);
    if (options.failAt === name) throw new Error(`injected package publication failure: ${name}`);
  };
  publicationMutex = await acquirePublicationMutex(directory);
  let lock;
  try {
  publicationMutex.assertHeld();
  await cleanupStaleLockCandidates(directory);
  await recoverPackageTransaction(directory);
  publicationMutex.assertHeld();
  lock = await acquirePackageLock(lockPath);
  const existed = [false, false];
  const backedUp = [false, false];
  const published = [false, false];
  let committed = false;
  let rollbackComplete = false;
  try {
    for (let index = 0; index < targets.length; index += 1) {
      await checkpoint(`state-read-${index}`);
      existed[index] = await stat(targets[index]).then(() => true, (error) => error.code === 'ENOENT' ? false : Promise.reject(error));
    }
    const journal = { schema: 1, phase: 'prepared', nonce, existed, archive_sha256: createHash('sha256').update(archive).digest('hex'), manifest_sha256: createHash('sha256').update(manifest).digest('hex') };
    publicationMutex.assertHeld();
    await writeJournalAtomically(journalPath, journal);
    publicationMutex.assertHeld();
    await writeDurably(newPaths[0], archive);
    await writeDurably(newPaths[1], manifest);
    await checkpoint('new-files-durable');
    for (let index = 0; index < targets.length; index += 1) {
      if (existed[index]) {
        await rename(targets[index], backupPaths[index]);
        backedUp[index] = true;
      }
      await checkpoint(`backup-${index}`);
    }
    for (let index = 0; index < targets.length; index += 1) {
      await rename(newPaths[index], targets[index]);
      published[index] = true;
      await checkpoint(`publish-${index}`);
    }
    await checkpoint('before-commit-journal');
    await writeJournalAtomically(journalPath, { ...journal, phase: 'committed' });
    await checkpoint('after-commit-journal');
    committed = true;
  } catch (error) {
    if (!error.simulatedCrash) {
      try {
        if (!publicationMutex.isHeld()) {
          await publicationMutex.release();
          publicationMutex = await acquirePublicationMutex(directory);
          await recoverPackageTransaction(directory);
          rollbackComplete = true;
        } else {
          for (let index = targets.length - 1; index >= 0; index -= 1) {
            if (published[index]) await rm(targets[index], { force: true });
            if (backedUp[index]) {
              await checkpoint(`restore-${index}`);
              await rename(backupPaths[index], targets[index]);
            }
          }
          rollbackComplete = true;
        }
      } catch (rollbackError) {
        error.cause = rollbackError;
      }
    }
    throw error;
  } finally {
    if (committed || rollbackComplete) {
      await Promise.all([...newPaths, ...backupPaths].map((path) => rm(path, { force: true })));
      await rm(journalPath, { force: true });
      await cleanupJournalTemps(dirname(journalPath));
    }
    if (lock) await lock.close();
    if (committed || rollbackComplete) await rm(lockPath, { force: true });
    else await writeFile(lockPath, JSON.stringify({ pid: 0 }), { flag: 'w' });
  }
  } finally {
    await publicationMutex.release();
  }
}

async function writeDurably(path, contents) {
  const file = await open(path, 'wx', 0o600);
  try {
    await file.writeFile(contents);
    await file.sync();
  } finally {
    await file.close();
  }
}

async function writeJournalAtomically(path, state) {
  const temporary = `${path}.partial-${process.pid}-${randomBytes(8).toString('hex')}`;
  try {
    await writeDurably(temporary, Buffer.from(`${JSON.stringify(state)}\n`));
    if (process.platform === 'win32') {
      const script = "Add-Type -TypeDefinition 'using System;using System.Runtime.InteropServices;public static class AtomicMove{[DllImport(\"kernel32.dll\",SetLastError=true,CharSet=CharSet.Unicode)]public static extern bool MoveFileEx(string a,string b,int f);}';if(-not[AtomicMove]::MoveFileEx($env:FFMPEG_JOURNAL_TEMP,$env:FFMPEG_JOURNAL_PATH,9)){throw [ComponentModel.Win32Exception]::new([Runtime.InteropServices.Marshal]::GetLastWin32Error())}";
      execFileSync('powershell.exe', ['-NoProfile', '-NonInteractive', '-Command', script], { windowsHide: true, env: { ...process.env, FFMPEG_JOURNAL_TEMP: temporary, FFMPEG_JOURNAL_PATH: path }, stdio: 'pipe' });
    } else await rename(temporary, path);
    const canonical = await open(path, 'r+');
    try { await canonical.sync(); } finally { await canonical.close(); }
    await syncDirectoryBestEffort(dirname(path));
  } finally {
    await rm(temporary, { force: true });
  }
}

async function syncDirectoryBestEffort(directory) {
  if (process.platform === 'win32') return;
  const handle = await open(directory, 'r');
  try { await handle.sync(); } finally { await handle.close(); }
}

async function acquirePackageLock(path) {
  for (let attempt = 0; attempt < 200; attempt += 1) {
    try {
      const lock = await open(path, 'wx', 0o600);
      await lock.writeFile(`${JSON.stringify({ pid: process.pid })}\n`);
      await lock.sync();
      return lock;
    } catch (error) {
      if (error.code !== 'EEXIST') throw error;
      // The directory-keyed OS mutex proves no cooperating publisher is live;
      // any pre-existing file lock is therefore an abandoned transaction.
      await rm(path, { force: true });
    }
  }
  throw new Error(`Timed out waiting for FFmpeg package lock: ${path}`);
}

async function acquirePublicationMutex(directory) {
  const osLockPath = join(resolve(directory), '.ffmpeg-package.oslock');
  const takeoverPath = `${osLockPath}.takeover`;
  const token = `${process.pid}-${randomBytes(16).toString('hex')}`;
  const started = processStartIdentity(process.pid);
  const candidatePath = `${osLockPath}.candidate-${token}`;
  await writeDurably(candidatePath, Buffer.from(`${JSON.stringify({ pid: process.pid, started, token })}\n`));
  try {
    for (let attempt = 0; attempt < 3000; attempt += 1) {
      try {
        await link(candidatePath, osLockPath);
        let held = true;
        return {
          isHeld: () => held,
          assertHeld: () => { if (!held) throw new Error('FFmpeg package publication lock is not held.'); },
          release: async () => {
            if (!held) return;
            held = false;
            const owner = await readLockOwner(osLockPath);
            if (owner?.token === token) await rm(osLockPath, { force: true, maxRetries: 20, retryDelay: 10 });
          },
        };
      } catch (error) {
        if (error.code !== 'EEXIST') throw error;
      }
      const observed = await readLockOwner(osLockPath);
      if (!observed) continue;
      if (isLockOwnerAlive(observed)) {
        await delay(10);
        continue;
      }
      try {
        await link(candidatePath, takeoverPath);
      } catch (error) {
        if (error.code !== 'EEXIST') throw error;
        const claimant = await readLockOwner(takeoverPath);
        if (!claimant) continue;
        if (isLockOwnerAlive(claimant)) {
          await delay(10);
          continue;
        }
        const unchangedClaimant = await readLockOwner(takeoverPath);
        if (unchangedClaimant?.token === claimant.token && unchangedClaimant.started === claimant.started && !isLockOwnerAlive(unchangedClaimant)) await rm(takeoverPath, { force: true, maxRetries: 20, retryDelay: 10 });
        continue;
      }
      try {
        const current = await readLockOwner(osLockPath);
        if (current?.token === observed.token && current.started === observed.started && !isLockOwnerAlive(current)) await rm(osLockPath, { force: true, maxRetries: 20, retryDelay: 10 });
      } finally {
        const claimant = await readLockOwner(takeoverPath);
        if (claimant?.token === token) await rm(takeoverPath, { force: true, maxRetries: 20, retryDelay: 10 });
      }
    }
    throw new Error(`Timed out waiting for FFmpeg package publication lock: ${osLockPath}`);
  } finally {
    await rm(candidatePath, { force: true, maxRetries: 20, retryDelay: 10 });
  }
}

async function readLockOwner(path) {
  try {
    const value = JSON.parse(await readFile(path, 'utf8'));
    if (value && Object.getPrototypeOf(value) === Object.prototype && Object.keys(value).sort().join(',') === 'pid,started,token' && Number.isSafeInteger(value.pid) && value.pid > 0 && /^[0-9]{1,20}$/.test(value.started) && /^[0-9]+-[0-9a-f]{32}$/.test(value.token)) return value;
    throw new Error(`FFmpeg package publication lock has an invalid owner record: ${path}`);
  } catch (error) {
    if (error.code === 'ENOENT') return undefined;
    throw error;
  }
}

function processStartIdentity(pid) {
  const script = 'try{$p=Get-CimInstance Win32_Process -Filter ("ProcessId="+$env:FFMPEG_LOCK_PID) -ErrorAction Stop;if($null-eq$p){exit 3};[Console]::Write($p.CreationDate.ToUniversalTime().Ticks)}catch{Write-Error $_;exit 4}';
  try {
    const value = execFileSync('powershell.exe', ['-NoProfile', '-NonInteractive', '-Command', script], { windowsHide: true, env: { ...process.env, FFMPEG_LOCK_PID: String(pid) }, encoding: 'utf8', stdio: ['ignore', 'pipe', 'pipe'] }).trim();
    if (!/^[0-9]{1,20}$/.test(value)) throw new Error('Invalid process start identity.');
    return value;
  } catch (error) {
    if (error.status === 3) return undefined;
    throw new Error(`Could not verify FFmpeg package lock owner process ${pid}: ${error.message}`);
  }
}

function isLockOwnerAlive(owner) {
  try { process.kill(owner.pid, 0); } catch (error) {
    if (error.code === 'ESRCH') return false;
    if (error.code !== 'EPERM') throw error;
  }
  const key = `${owner.pid}:${owner.started}`;
  const cached = ownerLivenessCache.get(key);
  if (cached && Date.now() - cached.checkedAt < 250) return cached.alive;
  const alive = processStartIdentity(owner.pid) === owner.started;
  ownerLivenessCache.set(key, { alive, checkedAt: Date.now() });
  return alive;
}

function delay(milliseconds) { return new Promise((resolveDelay) => setTimeout(resolveDelay, milliseconds)); }

async function cleanupStaleLockCandidates(directory, options = {}) {
  for (const name of await readdir(directory)) {
    const token = name.match(/^\.ffmpeg-package\.oslock\.candidate-([0-9]+-[0-9a-f]{32})$/)?.[1];
    if (!token) continue;
    const path = join(directory, name);
    await options.beforeCandidateRead?.(path);
    const owner = await readLockOwner(path);
    if (!owner) continue;
    if (owner?.token !== token) throw new Error(`FFmpeg package lock candidate identity is invalid: ${path}`);
    if (!isLockOwnerAlive(owner)) await rm(path, { force: true, maxRetries: 20, retryDelay: 10 });
  }
}

async function recoverPackageTransaction(directory) {
  const journalPath = join(directory, '.ffmpeg-package.transaction.json');
  let state;
  try { state = JSON.parse(await readFile(journalPath, 'utf8')); } catch (error) {
    if (error.code === 'ENOENT') return recoverWithoutUsableJournal(directory, false);
    return recoverWithoutUsableJournal(directory, true);
  }
  if (state.schema !== 1 || !['prepared', 'committed'].includes(state.phase) || !/^[0-9]+-[0-9a-f]{16}$/.test(state.nonce) || !Array.isArray(state.existed) || state.existed.length !== 2 || !state.existed.every((value) => typeof value === 'boolean') || !/^[0-9a-f]{64}$/.test(state.archive_sha256) || !/^[0-9a-f]{64}$/.test(state.manifest_sha256)) return recoverWithoutUsableJournal(directory, true);
  const targets = ['ffmpeg.zip', 'manifest.json'].map((name) => join(directory, name));
  const partials = targets.map((path) => `${path}.partial-${state.nonce}`);
  const backups = targets.map((path) => `${path}.backup-${state.nonce}`);
  const committedPairValid = state.phase === 'committed' && await pairMatchesJournal(targets, state);
  if (!committedPairValid) {
    for (let index = targets.length - 1; index >= 0; index -= 1) {
      const backupExists = await stat(backups[index]).then(() => true, () => false);
      if (backupExists) {
        await rm(targets[index], { force: true });
        await rename(backups[index], targets[index]);
      } else if (!state.existed[index]) await rm(targets[index], { force: true });
    }
  }
  await Promise.all([...partials, ...backups].map((path) => rm(path, { force: true })));
  await rm(journalPath, { force: true });
  await cleanupJournalTemps(directory);
}

async function recoverWithoutUsableJournal(directory, canonicalExists) {
  const targets = ['ffmpeg.zip', 'manifest.json'].map((name) => join(directory, name));
  const entries = await readdir(directory);
  const hasTransactionEvidence = canonicalExists || entries.some((name) => /^(?:ffmpeg\.zip|manifest\.json)\.(?:partial|backup)-[0-9]+-[0-9a-f]{16}$|^\.ffmpeg-package\.transaction\.json\.partial-[0-9]+-[0-9a-f]{16}$/.test(name));
  if (!hasTransactionEvidence) return;
  const nonces = new Set(entries.map((name) => name.match(/^(?:ffmpeg\.zip|manifest\.json)\.backup-([0-9]+-[0-9a-f]{16})$/)?.[1]).filter(Boolean));
  const candidates = [];
  const currentIdentity = await pairIdentity(targets);
  if (currentIdentity) candidates.push({ nonce: undefined, paths: targets, identity: currentIdentity });
  for (const nonce of nonces) {
    const paths = targets.map((target) => join(directory, `${target.endsWith('ffmpeg.zip') ? 'ffmpeg.zip' : 'manifest.json'}.backup-${nonce}`));
    const candidate = [];
    for (let index = 0; index < 2; index += 1) candidate.push(await stat(paths[index]).then(() => paths[index], () => targets[index]));
    const identity = await pairIdentity(candidate);
    if (identity) candidates.push({ nonce, paths: candidate, identity });
  }
  const identities = new Map(candidates.map((candidate) => [candidate.identity, candidate]));
  if (identities.size !== 1) throw new Error('Invalid or ambiguous FFmpeg package recovery journal; recovery evidence was preserved.');
  const selected = currentIdentity ? candidates.find((candidate) => candidate.paths === targets) : [...identities.values()][0];
  for (let index = 0; index < 2; index += 1) {
    if (selected.paths[index] !== targets[index]) {
      await rm(targets[index], { force: true });
      await rename(selected.paths[index], targets[index]);
    }
  }
  if (canonicalExists) await rm(join(directory, '.ffmpeg-package.transaction.json'), { force: true });
  await cleanupOwnedTransactionEvidence(directory);
}

async function pairHasArchiveIdentity(paths) {
  return Boolean(await pairIdentity(paths));
}

async function pairIdentity(paths) {
  try {
    const [archive, manifestBytes] = await Promise.all(paths.map((path) => readFile(path)));
    const manifest = JSON.parse(manifestBytes.toString('utf8'));
    const exactKeys = 'archive_sha256,authenticode,component_gate,component_gate_sha256,sha256,size,version';
    if (Object.getPrototypeOf(manifest) !== Object.prototype || Object.keys(manifest).sort().join(',') !== exactKeys || manifest.version !== version || typeof manifest.component_gate !== 'string' || !/^[0-9a-f]{64}$/.test(manifest.sha256) || !/^[0-9a-f]{64}$/.test(manifest.archive_sha256) || !/^[0-9a-f]{64}$/.test(manifest.component_gate_sha256) || !Number.isSafeInteger(manifest.size) || manifest.size <= 0 || typeof manifest.authenticode !== 'boolean') return undefined;
    if (createHash('sha256').update(archive).digest('hex') !== manifest.archive_sha256 || createHash('sha256').update(manifest.component_gate).digest('hex') !== manifest.component_gate_sha256) return undefined;
    return `${manifest.archive_sha256}:${createHash('sha256').update(manifestBytes).digest('hex')}`;
  } catch { return undefined; }
}

async function cleanupJournalTemps(directory) {
  const entries = await readdir(directory);
  await Promise.all(entries.filter((name) => /^\.ffmpeg-package\.transaction\.json\.partial-[0-9]+-[0-9a-f]{16}$/.test(name)).map((name) => rm(join(directory, name), { force: true })));
}

async function cleanupOwnedTransactionEvidence(directory) {
  const entries = await readdir(directory);
  const owned = /^(?:ffmpeg\.zip|manifest\.json)\.(?:partial|backup)-[0-9]+-[0-9a-f]{16}$|^\.ffmpeg-package\.transaction\.json(?:\.partial-[0-9]+-[0-9a-f]{16})?$/;
  await Promise.all(entries.filter((name) => owned.test(name)).map((name) => rm(join(directory, name), { force: true })));
}

async function pairMatchesJournal(targets, state) {
  try {
    const [archive, manifest] = await Promise.all(targets.map((path) => readFile(path)));
    return createHash('sha256').update(archive).digest('hex') === state.archive_sha256 && createHash('sha256').update(manifest).digest('hex') === state.manifest_sha256;
  } catch { return false; }
}

async function runSelfTests() {
  const directory = await mkdtemp(join(tmpdir(), 'gift-panel-ffmpeg-package-test-'));
  try {
    const overLimit = Buffer.alloc(maximumSize + 1);
    assertThrows(() => validatePackageInputs(overLimit, Buffer.from('gate\n')), 'oversize binary', 'hard limit');
    assertThrows(() => validateBuildRecord(Buffer.from('stale\n'), Buffer.from('binary')), 'stale build record', 'mismatch');
    const unsignedPE = testPE();
    const signedPE = testSignedPE(unsignedPE);
    const unsignedRecord = Buffer.from(`binary_sha256=${createHash('sha256').update(unsignedPE).digest('hex')}\nbinary_size=${unsignedPE.length}\npe_authenticode_content_sha256=${authenticodeContentHash(unsignedPE)}\n`);
    assertThrows(() => bindBuildRecordToBinary(unsignedRecord, signedPE, false), 'unsigned stale build record', 'mismatch');
    const refreshed = bindBuildRecordToBinary(unsignedRecord, signedPE, true).toString();
    if (!refreshed.includes(`binary_sha256=${createHash('sha256').update(signedPE).digest('hex')}\nbinary_size=${signedPE.length}\n`)) throw new Error('signed build record was not refreshed');
    const differentPE = Buffer.from(signedPE); differentPE[400] ^= 1;
    assertThrows(() => bindBuildRecordToBinary(unsignedRecord, differentPE, true), 'different signed image', 'does not derive');
    const retained = testBoundPair('retained');
    await writeFile(join(directory, 'ffmpeg.zip'), retained.archive);
    await writeFile(join(directory, 'manifest.json'), retained.manifest);
    await writeFile(join(directory, '.ffmpeg-package.lock'), '{"pid":0}');
    await writeFile(join(directory, '.ffmpeg-package.transaction.json'), '{"torn":');
    await writeFile(join(directory, '.ffmpeg-package.transaction.json.partial-123-0123456789abcdef'), '{"phase":"prepared"');
    const replacement = testBoundPair('replacement');
    await publishPairTransactionally(directory, replacement.archive, replacement.manifest);
    assertBuffer(await readFile(join(directory, 'ffmpeg.zip')), replacement.archive, 'torn journal one-run recovery archive');
    assertBuffer(await readFile(join(directory, 'manifest.json')), replacement.manifest, 'torn journal one-run recovery manifest');
    if ((await readdir(directory)).some((name) => name.startsWith('.ffmpeg-package.transaction.json'))) throw new Error('torn journal recovery left journal evidence after proving a valid pair');
    const markerlessRetained = testBoundPair('markerless-retained');
    await writeFile(join(directory, 'ffmpeg.zip'), markerlessRetained.archive);
    await writeFile(join(directory, 'manifest.json'), markerlessRetained.manifest);
    await writeFile(join(directory, '.ffmpeg-package.transaction.json'), '{"torn":');
    await writeFile(join(directory, '.ffmpeg-package.transaction.json.partial-234-0123456789abcdef'), '{"phase":"prepared"');
    const markerlessReplacement = testBoundPair('markerless-replacement');
    await publishPairTransactionally(directory, markerlessReplacement.archive, markerlessReplacement.manifest);
    assertBuffer(await readFile(join(directory, 'ffmpeg.zip')), markerlessReplacement.archive, 'markerless evidence recovery archive');
    if ((await readdir(directory)).some((name) => name.startsWith('.ffmpeg-package.transaction.json'))) throw new Error('markerless recovery left journal evidence');
    const recoverable = testBoundPair('recoverable-backup');
    await writeFile(join(directory, 'ffmpeg.zip'), Buffer.from('mixed-current'));
    await writeFile(join(directory, 'manifest.json'), Buffer.from('{invalid'));
    await writeFile(join(directory, 'ffmpeg.zip.backup-456-0123456789abcdef'), recoverable.archive);
    await writeFile(join(directory, 'manifest.json.backup-456-0123456789abcdef'), recoverable.manifest);
    await writeFile(join(directory, '.ffmpeg-package.lock'), '{"pid":0}');
    await writeFile(join(directory, '.ffmpeg-package.transaction.json'), '{"torn":');
    const afterBackupRecovery = testBoundPair('after-backup-recovery');
    await publishPairTransactionally(directory, afterBackupRecovery.archive, afterBackupRecovery.manifest);
    assertBuffer(await readFile(join(directory, 'ffmpeg.zip')), afterBackupRecovery.archive, 'unique backup recovery converged in one run');
    const validCurrent = testBoundPair('valid-current');
    const validAlternative = testBoundPair('valid-alternative');
    await writeFile(join(directory, 'ffmpeg.zip'), validCurrent.archive);
    await writeFile(join(directory, 'manifest.json'), validCurrent.manifest);
    await writeFile(join(directory, 'ffmpeg.zip.backup-567-0123456789abcdef'), validAlternative.archive);
    await writeFile(join(directory, 'manifest.json.backup-567-0123456789abcdef'), validAlternative.manifest);
    await writeFile(join(directory, '.ffmpeg-package.lock'), '{"pid":0}');
    await writeFile(join(directory, '.ffmpeg-package.transaction.json'), '{"torn":');
    await publishPairTransactionally(directory, replacement.archive, replacement.manifest).then(
      () => { throw new Error('valid current plus alternative backup recovery unexpectedly succeeded'); },
      (error) => { if (!String(error.message).includes('ambiguous')) throw error; },
    );
    assertBuffer(await readFile(join(directory, 'ffmpeg.zip')), validCurrent.archive, 'ambiguous current preserved');
    assertBuffer(await readFile(join(directory, 'ffmpeg.zip.backup-567-0123456789abcdef')), validAlternative.archive, 'ambiguous backup preserved');
    await rm(join(directory, 'ffmpeg.zip.backup-567-0123456789abcdef'));
    await rm(join(directory, 'manifest.json.backup-567-0123456789abcdef'));
    await rm(join(directory, '.ffmpeg-package.lock'), { force: true });
    await rm(join(directory, '.ffmpeg-package.transaction.json'), { force: true });
    const ambiguousOne = testBoundPair('ambiguous-one');
    const ambiguousTwo = testBoundPair('ambiguous-two');
    await writeFile(join(directory, 'ffmpeg.zip'), Buffer.from('mixed-current'));
    await writeFile(join(directory, 'manifest.json'), Buffer.from('{invalid'));
    for (const [nonce, pair] of [['789-0123456789abcdef', ambiguousOne], ['790-fedcba9876543210', ambiguousTwo]]) {
      await writeFile(join(directory, `ffmpeg.zip.backup-${nonce}`), pair.archive);
      await writeFile(join(directory, `manifest.json.backup-${nonce}`), pair.manifest);
    }
    await writeFile(join(directory, '.ffmpeg-package.lock'), '{"pid":0}');
    await writeFile(join(directory, '.ffmpeg-package.transaction.json'), '{"torn":');
    await publishPairTransactionally(directory, replacement.archive, replacement.manifest).then(
      () => { throw new Error('ambiguous corrupt journal recovery unexpectedly succeeded'); },
      (error) => { if (!String(error.message).includes('ambiguous')) throw error; },
    );
    for (const name of await readdir(directory)) if (/^(?:ffmpeg\.zip|manifest\.json)\.backup-(?:789-0123456789abcdef|790-fedcba9876543210)$/.test(name)) await rm(join(directory, name));
    await rm(join(directory, '.ffmpeg-package.lock'), { force: true });
    await rm(join(directory, '.ffmpeg-package.transaction.json'), { force: true });
    const oldArchive = Buffer.from('old-archive');
    const oldManifest = Buffer.from('old-manifest');
    for (const failAt of ['state-read-0', 'new-files-durable', 'backup-0', 'backup-1', 'publish-0', 'publish-1']) {
      await writeFile(join(directory, 'ffmpeg.zip'), oldArchive);
      await writeFile(join(directory, 'manifest.json'), oldManifest);
      await publishPairTransactionally(directory, Buffer.from('new-archive'), Buffer.from('new-manifest'), { failAt }).then(
        () => { throw new Error(`failure ${failAt} was not injected`); },
        () => {},
      );
      assertBuffer(await readFile(join(directory, 'ffmpeg.zip')), oldArchive, `${failAt} archive rollback`);
      assertBuffer(await readFile(join(directory, 'manifest.json')), oldManifest, `${failAt} manifest rollback`);
      const leftovers = (await readdir(directory)).filter((name) => name.includes('.partial-') || name.includes('.backup-') || name.startsWith('.ffmpeg-package.'));
      if (leftovers.length) throw new Error(`${failAt} left transaction files: ${leftovers}`);
    }
    for (const crashAt of ['new-files-durable', 'backup-0', 'backup-1', 'publish-0', 'publish-1']) {
      await writeFile(join(directory, 'ffmpeg.zip'), oldArchive);
      await writeFile(join(directory, 'manifest.json'), oldManifest);
      await publishPairTransactionally(directory, Buffer.from('crash-archive'), Buffer.from('crash-manifest'), { crashAt }).catch(() => {});
      await publishPairTransactionally(directory, Buffer.from('recovered-archive'), Buffer.from('recovered-manifest'));
      assertBuffer(await readFile(join(directory, 'ffmpeg.zip')), Buffer.from('recovered-archive'), `${crashAt} recovery archive`);
      assertBuffer(await readFile(join(directory, 'manifest.json')), Buffer.from('recovered-manifest'), `${crashAt} recovery manifest`);
    }
    for (const crashAt of ['before-commit-journal', 'after-commit-journal']) {
      await writeFile(join(directory, 'ffmpeg.zip'), oldArchive);
      await writeFile(join(directory, 'manifest.json'), oldManifest);
      await publishPairTransactionally(directory, Buffer.from('finalize-archive'), Buffer.from('finalize-manifest'), { crashAt }).then(
        () => { throw new Error(`finalization crash ${crashAt} was not injected`); }, () => {},
      );
      await publishPairTransactionally(directory, Buffer.from('next-archive'), Buffer.from('next-manifest'));
      assertBuffer(await readFile(join(directory, 'ffmpeg.zip')), Buffer.from('next-archive'), `${crashAt} one-run archive`);
      assertBuffer(await readFile(join(directory, 'manifest.json')), Buffer.from('next-manifest'), `${crashAt} one-run manifest`);
    }
    await publishPairTransactionally(directory, Buffer.from('crash-archive'), Buffer.from('crash-manifest'), { crashAt: 'publish-0' }).catch(() => {});
    await Promise.all(Array.from({ length: 12 }, (_, index) => publishPairTransactionally(
      directory, Buffer.from(`takeover-archive-${index}`), Buffer.from(`takeover-manifest-${index}`),
    )));
    const takeoverArchive = (await readFile(join(directory, 'ffmpeg.zip'))).toString();
    const takeoverManifest = (await readFile(join(directory, 'manifest.json'))).toString();
    if (takeoverArchive.slice('takeover-archive-'.length) !== takeoverManifest.slice('takeover-manifest-'.length)) throw new Error(`concurrent stale takeover mixed generations: ${takeoverArchive}/${takeoverManifest}`);
    for (const killMutexAt of ['state-read-0', 'new-files-durable', 'backup-0', 'backup-1', 'publish-0', 'publish-1', 'before-commit-journal', 'after-commit-journal']) {
      const beforeLoss = testBoundPair(`before-loss-${killMutexAt}`);
      await writeFile(join(directory, 'ffmpeg.zip'), beforeLoss.archive);
      await writeFile(join(directory, 'manifest.json'), beforeLoss.manifest);
      const interrupted = testBoundPair(`interrupted-${killMutexAt}`);
      await publishPairTransactionally(directory, interrupted.archive, interrupted.manifest, { killMutexAt }).then(
        () => { throw new Error(`mutex loss ${killMutexAt} unexpectedly succeeded`); }, () => {},
      );
      const afterLoss = testBoundPair(`after-loss-${killMutexAt}`);
      await publishPairTransactionally(directory, afterLoss.archive, afterLoss.manifest);
      assertBuffer(await readFile(join(directory, 'ffmpeg.zip')), afterLoss.archive, `${killMutexAt} successor archive`);
      assertBuffer(await readFile(join(directory, 'manifest.json')), afterLoss.manifest, `${killMutexAt} successor manifest`);
    }
    const orphanToken = `2147483647-${'b'.repeat(32)}`;
    const orphanCandidate = join(directory, `.ffmpeg-package.oslock.candidate-${orphanToken}`);
    await writeFile(orphanCandidate, `${JSON.stringify({ pid: 2147483647, started: '1', token: orphanToken })}\n`);
    await cleanupStaleLockCandidates(directory, { beforeCandidateRead: async (path) => { if (path === orphanCandidate) await rm(path); } });
    if (await stat(orphanCandidate).then(() => true, () => false)) throw new Error('disappeared candidate race fixture still exists');
    await writeFile(orphanCandidate, `${JSON.stringify({ pid: 2147483647, started: '1', token: orphanToken })}\n`);
    const afterOrphan = testBoundPair('after-orphan-candidate');
    await publishPairTransactionally(directory, afterOrphan.archive, afterOrphan.manifest);
    if (await stat(orphanCandidate).then(() => true, () => false)) throw new Error('stale publication lock candidate was not cleaned');
    await writeFile(join(directory, '.ffmpeg-package.oslock'), `${JSON.stringify({ pid: process.pid, started: '1', token: `999-${'a'.repeat(32)}` })}\n`);
    const afterPIDReuse = testBoundPair('after-pid-reuse');
    await publishPairTransactionally(directory, afterPIDReuse.archive, afterPIDReuse.manifest);
    assertBuffer(await readFile(join(directory, 'ffmpeg.zip')), afterPIDReuse.archive, 'PID reuse stale-lock recovery');
    await mkdir(join(directory, '.ffmpeg-package.oslock'));
    await publishPairTransactionally(directory, replacement.archive, replacement.manifest).then(
      () => { throw new Error('unreadable publication lock unexpectedly succeeded'); },
      () => {},
    );
    if (!(await stat(join(directory, '.ffmpeg-package.oslock'))).isDirectory()) throw new Error('unreadable publication lock was deleted');
    await rm(join(directory, '.ffmpeg-package.oslock'), { recursive: true });
    await writeFile(join(directory, 'ffmpeg.zip'), Buffer.from('invalid-current-archive'));
    await writeFile(join(directory, 'manifest.json'), Buffer.from('{invalid-current'));
    await writeFile(join(directory, '.ffmpeg-package.lock'), '{"pid":0}');
    await writeFile(join(directory, '.ffmpeg-package.transaction.json'), '{invalid');
    await publishPairTransactionally(directory, Buffer.from('invalid-journal-archive'), Buffer.from('invalid-journal-manifest')).then(
      () => { throw new Error('invalid recovery journal was accepted'); }, () => {},
    );
    await rm(join(directory, '.ffmpeg-package.lock'), { force: true });
    await rm(join(directory, '.ffmpeg-package.transaction.json'), { force: true });
    await publishPairTransactionally(directory, Buffer.from('post-error-archive'), Buffer.from('post-error-manifest'));
    await writeFile(join(directory, 'ffmpeg.zip'), oldArchive);
    await writeFile(join(directory, 'manifest.json'), oldManifest);
    await publishPairTransactionally(directory, Buffer.from('failed-archive'), Buffer.from('failed-manifest'), { failAt: 'publish-0', rollbackFailAt: 'restore-1' }).catch(() => {});
    await publishPairTransactionally(directory, Buffer.from('recovered-archive'), Buffer.from('recovered-manifest'));
    assertBuffer(await readFile(join(directory, 'ffmpeg.zip')), Buffer.from('recovered-archive'), 'rollback rename recovery archive');
    assertBuffer(await readFile(join(directory, 'manifest.json')), Buffer.from('recovered-manifest'), 'rollback rename recovery manifest');
    await rm(join(directory, 'ffmpeg.zip'), { force: true });
    await rm(join(directory, 'manifest.json'), { force: true });
    await publishPairTransactionally(directory, Buffer.from('new-archive'), Buffer.from('new-manifest'), { failAt: 'publish-0' }).catch(() => {});
    const absent = await Promise.all(['ffmpeg.zip', 'manifest.json'].map((name) => stat(join(directory, name)).then(() => false, (error) => error.code === 'ENOENT')));
    if (!absent.every(Boolean)) throw new Error('failed empty publication did not restore pair absence');
    await Promise.all(Array.from({ length: 12 }, (_, index) => publishPairTransactionally(
      directory, Buffer.from(`archive-${index}`), Buffer.from(`manifest-${index}`),
    )));
    const archive = (await readFile(join(directory, 'ffmpeg.zip'))).toString();
    const manifest = (await readFile(join(directory, 'manifest.json'))).toString();
    if (archive.slice(8) !== manifest.slice(9)) throw new Error(`concurrent publication mixed generations: ${archive}/${manifest}`);
    console.log('package transaction self-tests passed');
  } finally {
    await rm(directory, { recursive: true, force: true, maxRetries: 20, retryDelay: 50 });
  }
}

function assertBuffer(actual, expected, label) {
  if (!actual.equals(expected)) throw new Error(`${label} failed`);
}

function assertThrows(callback, label, expectedMessage) {
  try { callback(); } catch (error) {
    if (String(error.message).includes(expectedMessage)) return;
    throw new Error(`${label} failed for the wrong reason: ${error.message}`);
  }
  throw new Error(`${label} was accepted`);
}

function testPE() {
  const binary = Buffer.alloc(512); binary.writeUInt16LE(0x5a4d); binary.writeUInt32LE(0x80, 0x3c); binary.writeUInt32LE(0x00004550, 0x80); binary.writeUInt16LE(0x20b, 0x98); binary[400] = 0x5a; return binary;
}
function testSignedPE(unsigned) {
  const signed = Buffer.concat([unsigned, Buffer.alloc(16, 0xa5)]); const securityDirectory = 0x98 + 112 + 32; signed.writeUInt32LE(unsigned.length, securityDirectory); signed.writeUInt32LE(16, securityDirectory + 4); signed.writeUInt32LE(0x12345678, 0x98 + 64); return signed;
}
function testBoundPair(generation) {
  const archive = Buffer.from(`bound-archive-${generation}`);
  const componentGate = `test-gate-${generation}\n`;
  const manifest = Buffer.from(`${JSON.stringify({
    version,
    sha256: createHash('sha256').update(`binary-${generation}`).digest('hex'),
    archive_sha256: createHash('sha256').update(archive).digest('hex'),
    component_gate: componentGate,
    component_gate_sha256: createHash('sha256').update(componentGate).digest('hex'),
    size: 1,
    authenticode: false,
  })}\n`);
  return { archive, manifest };
}
