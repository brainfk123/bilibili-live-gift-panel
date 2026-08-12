import { createHash, randomBytes } from 'node:crypto';
import { execFileSync, spawn } from 'node:child_process';
import { mkdir, mkdtemp, open, readFile, readdir, rename, rm, stat, writeFile } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { dirname, join, resolve } from 'node:path';
import { fileURLToPath } from 'node:url';
import { deflateRawSync } from 'node:zlib';

const version = '9.0';
const warningSize = 30_000_000;
const maximumSize = 40_000_000;
const root = join(dirname(fileURLToPath(import.meta.url)), '..');
const outputDirectory = join(root, 'goserver', 'ffmpeg');
const componentGatePath = join(root, 'dist', 'ffmpeg-component-gate.txt');

if (process.argv.includes('--self-test')) {
  await runSelfTests();
} else {
  await main();
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

  const sha256 = createHash('sha256').update(binary).digest('hex');
  const archive = writeSingleFileZip('ffmpeg.exe', binary);
  if (archive.length > maximumSize) throw new Error(`FFmpeg ZIP is ${archive.length} bytes; hard limit is ${maximumSize} bytes.`);
  if (archive.length > warningSize) console.warn(`WARNING: FFmpeg ZIP is ${archive.length} bytes, above the ${warningSize}-byte target.`);
  const manifest = {
    version,
    sha256,
    archive_sha256: createHash('sha256').update(archive).digest('hex'),
    component_gate: componentGate.toString('utf8'),
    component_gate_sha256: createHash('sha256').update(componentGate).digest('hex'),
    size: binary.length,
    authenticode,
  };
  await mkdir(outputDirectory, { recursive: true });
  await publishPairTransactionally(outputDirectory, archive, Buffer.from(`${JSON.stringify(manifest, null, 2)}\n`));
  console.log(`packaged FFmpeg ${version}: ${binary.length} bytes, ZIP ${archive.length} bytes, SHA-256 ${sha256}, authenticode=${authenticode}`);
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

async function publishPairTransactionally(directory, archive, manifest, options = {}) {
  const nonce = `${process.pid}-${randomBytes(8).toString('hex')}`;
  const lockPath = join(directory, '.ffmpeg-package.lock');
  const journalPath = join(directory, '.ffmpeg-package.transaction.json');
  const targets = [join(directory, 'ffmpeg.zip'), join(directory, 'manifest.json')];
  const newPaths = targets.map((path) => `${path}.partial-${nonce}`);
  const backupPaths = targets.map((path) => `${path}.backup-${nonce}`);
  const checkpoint = (name) => {
    if (options.crashAt === name) {
      const error = new Error(`injected package publication crash: ${name}`);
      error.simulatedCrash = true;
      throw error;
    }
    if (options.rollbackFailAt === name) throw new Error(`injected package rollback failure: ${name}`);
    if (options.failAt === name) throw new Error(`injected package publication failure: ${name}`);
  };
  const publicationMutex = await acquirePublicationMutex(directory);
  let lock;
  try {
  lock = await acquirePackageLock(lockPath, directory);
  const existed = [false, false];
  const backedUp = [false, false];
  const published = [false, false];
  let committed = false;
  let rollbackComplete = false;
  try {
    for (let index = 0; index < targets.length; index += 1) {
      checkpoint(`state-read-${index}`);
      existed[index] = await stat(targets[index]).then(() => true, (error) => error.code === 'ENOENT' ? false : Promise.reject(error));
    }
    const journal = { nonce, existed, archive_sha256: createHash('sha256').update(archive).digest('hex'), manifest_sha256: createHash('sha256').update(manifest).digest('hex') };
    await writeDurably(journalPath, Buffer.from(`${JSON.stringify(journal)}\n`));
    await writeDurably(newPaths[0], archive);
    await writeDurably(newPaths[1], manifest);
    checkpoint('new-files-durable');
    for (let index = 0; index < targets.length; index += 1) {
      if (existed[index]) {
        await rename(targets[index], backupPaths[index]);
        backedUp[index] = true;
      }
      checkpoint(`backup-${index}`);
    }
    for (let index = 0; index < targets.length; index += 1) {
      await rename(newPaths[index], targets[index]);
      published[index] = true;
      checkpoint(`publish-${index}`);
    }
    await writeDurably(`${journalPath}.committed`, Buffer.from(`${JSON.stringify({ ...journal, committed: true })}\n`));
    await rm(journalPath, { force: true });
    await rename(`${journalPath}.committed`, journalPath);
    committed = true;
  } catch (error) {
    if (!error.simulatedCrash) {
      try {
        for (let index = targets.length - 1; index >= 0; index -= 1) {
          if (published[index]) await rm(targets[index], { force: true });
          if (backedUp[index]) {
            checkpoint(`restore-${index}`);
            await rename(backupPaths[index], targets[index]);
          }
        }
        rollbackComplete = true;
      } catch (rollbackError) {
        error.cause = rollbackError;
      }
    }
    throw error;
  } finally {
    if (committed || rollbackComplete) {
      await Promise.all([...newPaths, ...backupPaths].map((path) => rm(path, { force: true })));
      await rm(journalPath, { force: true });
      await rm(`${journalPath}.committed`, { force: true });
    }
    await lock.close();
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

async function acquirePackageLock(path, directory) {
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
      await recoverPackageTransaction(directory);
      await rm(path, { force: true });
    }
  }
  throw new Error(`Timed out waiting for FFmpeg package lock: ${path}`);
}

async function acquirePublicationMutex(directory) {
  const osLockPath = join(resolve(directory), '.ffmpeg-package.oslock');
  const quotedPath = osLockPath.replaceAll("'", "''");
  const script = `$lockPath='${quotedPath}';$deadline=[DateTime]::UtcNow.AddSeconds(30);$f=$null;try{while($null-eq $f){try{$f=[IO.File]::Open($lockPath,[IO.FileMode]::OpenOrCreate,[IO.FileAccess]::ReadWrite,[IO.FileShare]::None)}catch{if([DateTime]::UtcNow-ge$deadline){exit 2};Start-Sleep -Milliseconds 10}};[Console]::Out.WriteLine('READY');[Console]::Out.Flush();[Console]::In.ReadToEnd()|Out-Null}finally{if($null-ne$f){$f.Dispose()}}`;
  const helper = spawn('powershell.exe', ['-NoProfile', '-NonInteractive', '-Command', script], { windowsHide: true, stdio: ['pipe', 'pipe', 'pipe'] });
  let stderr = '';
  helper.stderr.setEncoding('utf8');
  helper.stderr.on('data', (chunk) => { stderr += chunk; });
  await new Promise((resolveReady, reject) => {
    let stdout = '';
    const fail = (error) => reject(new Error(`Could not acquire FFmpeg package mutex: ${error.message || error}; ${stderr}`));
    helper.once('error', fail);
    helper.once('exit', (code) => { if (!stdout.includes('READY')) fail(new Error(`helper exited ${code}`)); });
    helper.stdout.setEncoding('utf8');
    helper.stdout.on('data', (chunk) => {
      stdout += chunk;
      if (stdout.includes('READY')) resolveReady();
    });
  });
  return { release: () => new Promise((resolveRelease) => {
    helper.once('exit', async () => { await rm(osLockPath, { force: true }).catch(() => {}); resolveRelease(); });
    helper.stdin.end();
  }) };
}

async function recoverPackageTransaction(directory) {
  const journalPath = join(directory, '.ffmpeg-package.transaction.json');
  let state;
  try { state = JSON.parse(await readFile(journalPath, 'utf8')); } catch (error) {
    if (error.code === 'ENOENT') return;
    throw error;
  }
  if (!/^[0-9]+-[0-9a-f]{16}$/.test(state.nonce) || !Array.isArray(state.existed) || state.existed.length !== 2 || !/^[0-9a-f]{64}$/.test(state.archive_sha256) || !/^[0-9a-f]{64}$/.test(state.manifest_sha256)) throw new Error('Invalid FFmpeg package recovery journal.');
  const targets = ['ffmpeg.zip', 'manifest.json'].map((name) => join(directory, name));
  const partials = targets.map((path) => `${path}.partial-${state.nonce}`);
  const backups = targets.map((path) => `${path}.backup-${state.nonce}`);
  const committedPairValid = state.committed && await pairMatchesJournal(targets, state);
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
  await rm(`${journalPath}.committed`, { force: true });
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
    await publishPairTransactionally(directory, Buffer.from('crash-archive'), Buffer.from('crash-manifest'), { crashAt: 'publish-0' }).catch(() => {});
    await Promise.all(Array.from({ length: 12 }, (_, index) => publishPairTransactionally(
      directory, Buffer.from(`takeover-archive-${index}`), Buffer.from(`takeover-manifest-${index}`),
    )));
    const takeoverArchive = (await readFile(join(directory, 'ffmpeg.zip'))).toString();
    const takeoverManifest = (await readFile(join(directory, 'manifest.json'))).toString();
    if (takeoverArchive.slice('takeover-archive-'.length) !== takeoverManifest.slice('takeover-manifest-'.length)) throw new Error(`concurrent stale takeover mixed generations: ${takeoverArchive}/${takeoverManifest}`);
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
