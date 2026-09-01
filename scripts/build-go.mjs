import { createHash, createPublicKey } from 'node:crypto';
import { execFileSync, spawnSync } from 'node:child_process';
import { existsSync, readFileSync } from 'node:fs';
import { fileURLToPath } from 'node:url';
import { dirname, join, resolve } from 'node:path';
import { mirrorUiAssets } from './ui-assets.mjs';
import { resolveUpdateAPIBaseURLHex } from './update-api-build-config.mjs';

const root = join(dirname(fileURLToPath(import.meta.url)), '..');
const reviewedUpdatePublishers = new Set([
  'NaisNet Technology Co., Ltd.',
  'RushRush Network Technology Ltd',
]);

function decodeCanonicalBase64(name, value) {
  if (!value || value !== value.trim()) {
    throw new Error(`${name} must be canonical Base64`);
  }
  const decoded = Buffer.from(value, 'base64');
  if (decoded.length === 0 || decoded.toString('base64') !== value) {
    throw new Error(`${name} must be canonical Base64`);
  }
  return decoded;
}

function resolveUpdateTrust(environment) {
  const rootSPKIBase64 = environment.APP_UPDATE_TRUST_ROOT_SPKI_B64 || '';
  const bootstrapPolicyBase64 = environment.APP_UPDATE_TRUST_BOOTSTRAP_POLICY_B64 || '';
  const required = environment.APP_UPDATE_TRUST_REQUIRED === '1';
  const supplied = rootSPKIBase64 !== '' || bootstrapPolicyBase64 !== '';
  if (required && (!rootSPKIBase64 || !bootstrapPolicyBase64)) {
    throw new Error('update trust root and bootstrap policy are required');
  }
  if (!supplied) {
    return { rootSPKIBase64: '', bootstrapPolicyBase64: '', trustDigests: null };
  }
  if (!rootSPKIBase64 || !bootstrapPolicyBase64) {
    throw new Error('update trust root and bootstrap policy must be supplied together');
  }

  let rootSPKI;
  try {
    rootSPKI = decodeCanonicalBase64('APP_UPDATE_TRUST_ROOT_SPKI_B64', rootSPKIBase64);
    const publicKey = createPublicKey({ key: rootSPKI, format: 'der', type: 'spki' });
    const jwk = publicKey.export({ format: 'jwk' });
    if (publicKey.asymmetricKeyType !== 'ec' || jwk.crv !== 'P-256') throw new Error('not P-256');
  } catch {
    throw new Error('APP_UPDATE_TRUST_ROOT_SPKI_B64 must be canonical Base64 P-256 SPKI');
  }
  const bootstrapPolicy = decodeCanonicalBase64('APP_UPDATE_TRUST_BOOTSTRAP_POLICY_B64', bootstrapPolicyBase64);
  return {
    rootSPKIBase64,
    bootstrapPolicyBase64,
    trustDigests: {
      rootSPKISHA256: createHash('sha256').update(rootSPKI).digest('hex'),
      bootstrapPolicySHA256: createHash('sha256').update(bootstrapPolicy).digest('hex'),
    },
  };
}

function relaySanitizedGoOutput(result, trustInputs, writeStdout = (value) => process.stdout.write(value), writeStderr = (value) => process.stderr.write(value)) {
  const redact = (value) => {
    let output = value || '';
    for (const input of [trustInputs?.rootSPKIBase64, trustInputs?.bootstrapPolicyBase64]) {
      if (input) output = output.split(input).join('[redacted update trust input]');
    }
    return output;
  };
  const stdout = redact(result.stdout);
  const stderr = redact(result.stderr);
  if (stdout) writeStdout(stdout);
  if (stderr) writeStderr(stderr);
}

export function runGoCompilerCandidates(options) {
  const candidates = options?.candidates;
  if (!Array.isArray(candidates) || candidates.length === 0 || !Array.isArray(options.args) || typeof options.cwd !== 'string') {
    throw new Error('Go compiler configuration is invalid.');
  }
  const spawn = options.spawn || spawnSync;
  let lastError;
  for (const go of candidates) {
    const result = spawn(go, options.args, {
      cwd: options.cwd,
      encoding: 'utf8',
      maxBuffer: 32 * 1024 * 1024,
    });
    relaySanitizedGoOutput(result, options.trustInputs, options.writeStdout, options.writeStderr);
    if (!result.error && result.status === 0) return;
    lastError = result;
  }
  const status = Number.isInteger(lastError?.status) ? ` (exit ${lastError.status})` : '';
  throw new Error(`Go compiler failed${status}; inspect the compiler output above.`);
}

export async function resolveGoLdflags(environment = process.env) {
  const appVersion = (environment.APP_VERSION || 'dev').replace(/^v/, '');
  const appCommit = environment.APP_COMMIT || 'local';
  for (const [label, value] of [['APP_VERSION', appVersion], ['APP_COMMIT', appCommit]]) {
    if (!/^[0-9A-Za-z.+-]+$/.test(value)) throw new Error(`${label} contains unsupported characters`);
  }
  const updateAPIBaseURLHex = resolveUpdateAPIBaseURLHex(appVersion, environment.APP_UPDATE_API_URL);
  const updateExpectedPublisher = (environment.APP_UPDATE_PUBLISHER || '').trim();
  if (appVersion !== 'dev' && !updateExpectedPublisher) {
    throw new Error('Release build requires APP_UPDATE_PUBLISHER.');
  }
  if (appVersion !== 'dev' && !reviewedUpdatePublishers.has(updateExpectedPublisher)) {
    throw new Error('APP_UPDATE_PUBLISHER is not a reviewed update publisher.');
  }
  const updateExpectedPublisherHex = Buffer.from(updateExpectedPublisher, 'utf8').toString('hex');
  const updateTrust = resolveUpdateTrust(environment);
  return {
    appVersion,
    appCommit,
    trustDigests: updateTrust.trustDigests,
    trustInputs: updateTrust.trustDigests ? {
      rootSPKIBase64: updateTrust.rootSPKIBase64,
      bootstrapPolicyBase64: updateTrust.bootstrapPolicyBase64,
    } : null,
    ldflags: `-s -w -H windowsgui -X main.appVersion=${appVersion} -X main.appCommit=${appCommit} -X main.updateAPIBaseURLHex=${updateAPIBaseURLHex} -X main.updateExpectedPublisherHex=${updateExpectedPublisherHex} -X main.updateTrustRootSPKIBase64=${updateTrust.rootSPKIBase64} -X main.updateTrustBootstrapPolicyBase64=${updateTrust.bootstrapPolicyBase64}`,
  };
}

async function runBuild() {
  const build = await resolveGoLdflags(process.env);
  if (build.appVersion !== 'dev') {
    const manifestPath = join(root, 'goserver', 'ffmpeg', 'manifest.json');
    let manifest;
    try {
      manifest = JSON.parse(readFileSync(manifestPath, 'utf8'));
    } catch (error) {
      throw new Error(`Release build requires a readable embedded FFmpeg manifest: ${error.message}`);
    }
    if (manifest.authenticode !== true) {
      throw new Error('Release build requires an Authenticode-signed embedded FFmpeg payload.');
    }
  }
  execFileSync(process.execPath, [join(root, 'scripts', 'verify-ffmpeg.mjs'), ...(build.appVersion === 'dev' ? ['--payload-only'] : [])], {
    cwd: root,
    stdio: 'inherit',
    env: process.env,
  });
  const resource = join(root, 'goserver', 'rsrc_windows_amd64.syso');
  if (!existsSync(resource)) {
    throw new Error('Windows icon resource is missing. Run go install github.com/tc-hib/go-winres@latest and go-winres make in goserver.');
  }

  const distDir = join(root, 'goserver', 'dist');
  const uiManifest = mirrorUiAssets(join(root, 'dist'), distDir);
  console.log(`embedded ${uiManifest.files.length} UI assets (manifest v${uiManifest.version})`);
  if (build.trustDigests) {
    console.log(`embedded update trust root SHA-256 ${build.trustDigests.rootSPKISHA256}`);
    console.log(`embedded update trust bootstrap policy SHA-256 ${build.trustDigests.bootstrapPolicySHA256}`);
  }

  const out = join(root, 'dist', 'gift-panel.exe');
  const candidates = [process.env.GO_BIN, 'go', 'C:\\Program Files\\Go\\bin\\go.exe'].filter(Boolean);
  runGoCompilerCandidates({
    candidates,
    args: ['build', '-ldflags', build.ldflags, '-o', out, '.'],
    cwd: join(root, 'goserver'),
    trustInputs: build.trustInputs,
  });
  console.log(`built ${out} (${build.appVersion}, ${build.appCommit})`);
}

if (process.argv[1] && resolve(process.argv[1]) === fileURLToPath(import.meta.url)) {
  runBuild().catch((error) => {
    console.error(error.message);
    process.exitCode = 1;
  });
}
