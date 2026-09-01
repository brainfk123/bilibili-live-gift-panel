import { randomBytes } from 'node:crypto';
import { readFile, rename, rm, writeFile } from 'node:fs/promises';
import { request as httpsRequest } from 'node:https';
import { basename, resolve } from 'node:path';
import { pathToFileURL } from 'node:url';

const DEFAULT_MAX_ATTEMPTS = 3;
const DEFAULT_ATTEMPT_TIMEOUT_MS = 600_000;
const DEFAULT_RETRY_DELAYS_MS = Object.freeze([15_000, 45_000]);
const SIGNATURE_GROWTH_LIMIT = 4 * 1024 * 1024;
const RETRYABLE_NETWORK_CODES = new Set(['ECONNABORTED', 'ECONNRESET', 'ECONNREFUSED', 'EHOSTUNREACH', 'ENETDOWN', 'ENETUNREACH', 'ENOTFOUND', 'EAI_AGAIN', 'EPIPE', 'ETIMEDOUT']);
const IDENTITY_KEYS = new Set(['country', 'organization', 'organizationId']);
const SIGNER_PROFILES = Object.freeze({
  stable: Object.freeze({
    certificateEnv: 'EVSIGN_CERTIFICATE',
    expectedIdentityEnv: 'EVSIGN_PUBLISHER_IDENTITY',
    identity: Object.freeze({ country: 'CN', organization: 'NaisNet Technology Co., Ltd.', organizationId: '91210103MA7CJ3C094' }),
  }),
  bridge: Object.freeze({
    certificateEnv: 'EVSIGN_BRIDGE_CERTIFICATE',
    expectedIdentityEnv: 'EVSIGN_BRIDGE_PUBLISHER_IDENTITY',
    identity: Object.freeze({ country: 'CN', organization: 'RushRush Network Technology Ltd', organizationId: '91450900MADM3GLG5P' }),
  }),
});

export function resolveEVSignSignerProfile(profileName, environment = process.env) {
  const profile = SIGNER_PROFILES[profileName];
  if (!profile) throw new Error('unknown EVSign signer profile.');
  for (const [otherName, other] of Object.entries(SIGNER_PROFILES)) {
    if (otherName === profileName) continue;
    if ((environment[other.certificateEnv]?.trim() || '') !== '' || (environment[other.expectedIdentityEnv]?.trim() || '') !== '') {
      throw new Error('cross-profile EVSign configuration is not allowed.');
    }
  }

  const certificate = environment[profile.certificateEnv];
  const identityJSON = environment[profile.expectedIdentityEnv];
  if (!validProfileString(certificate, 1024) || !validProfileString(identityJSON, 4096)) {
    throw new Error(`${profileName} EVSign profile is not configured.`);
  }
  let identity;
  try {
    identity = JSON.parse(identityJSON);
  } catch {
    throw new Error(`${profileName} EVSign publisher identity is invalid.`);
  }
  if (!identity || typeof identity !== 'object' || Array.isArray(identity) || Object.keys(identity).length !== IDENTITY_KEYS.size || Object.keys(identity).some((key) => !IDENTITY_KEYS.has(key))) {
    throw new Error(`${profileName} EVSign publisher identity is invalid.`);
  }
  if (Object.entries(profile.identity).some(([key, value]) => identity[key] !== value)) {
    throw new Error(`${profileName} EVSign publisher identity is not the reviewed identity.`);
  }
  return { schema: 2, profile: profileName, certificate, identity: { ...profile.identity } };
}

function validProfileString(value, maximumBytes) {
  return typeof value === 'string' && value.length > 0 && value === value.trim() && !/[\r\n]/.test(value) && Buffer.byteLength(value, 'utf8') <= maximumBytes;
}

export async function requestSignedBytes(source, { endpoint, headers, attemptTimeoutMs, maximumResponseBytes }) {
  const url = new URL(endpoint);
  if (url.protocol !== 'https:') throw new Error('EV Sign endpoint must use HTTPS.');
  return new Promise((resolvePromise, rejectPromise) => {
    let settled = false;
    let deadline;
    const finish = (callback, value) => {
      if (settled) return;
      settled = true;
      clearTimeout(deadline);
      callback(value);
    };
    const request = httpsRequest(url, { method: 'POST', headers }, (response) => {
      const statusCode = response.statusCode || 0;
      if (statusCode !== 200) {
        response.resume();
        const error = new Error(`EV Sign failed with HTTP ${statusCode}.`);
        error.statusCode = statusCode;
        finish(rejectPromise, error);
        return;
      }
      const chunks = [];
      let length = 0;
      response.on('data', (chunk) => {
        length += chunk.length;
        if (length > maximumResponseBytes) {
          const error = integrityError('EV Sign response exceeds the signed-file size limit.');
          response.destroy(error);
          return;
        }
        chunks.push(chunk);
      });
      response.on('end', () => finish(resolvePromise, Buffer.concat(chunks, length)));
      response.on('error', (error) => finish(rejectPromise, error));
    });
    request.on('error', (error) => finish(rejectPromise, error));
    deadline = setTimeout(() => {
      const error = new Error(`EV Sign request timed out after ${attemptTimeoutMs} ms.`);
      error.code = 'ETIMEDOUT';
      request.destroy(error);
    }, attemptTimeoutMs);
    request.end(source);
  });
}

export async function signFileWithRetry(options, dependencies = {}) {
  const { inputPath, outputPath, endpoint, headers, maxAttempts = DEFAULT_MAX_ATTEMPTS, attemptTimeoutMs = DEFAULT_ATTEMPT_TIMEOUT_MS, retryDelaysMs = DEFAULT_RETRY_DELAYS_MS } = options;
  validateRetryPolicy(maxAttempts, attemptTimeoutMs, retryDelaysMs);
  const request = dependencies.request || ((source) => requestSignedBytes(source, { endpoint, headers, attemptTimeoutMs, maximumResponseBytes: source.length + SIGNATURE_GROWTH_LIMIT }));
  const sleep = dependencies.sleep || ((milliseconds) => new Promise((resolvePromise) => setTimeout(resolvePromise, milliseconds)));
  const log = dependencies.log || console.log;
  const source = await readFile(inputPath);
  if (source.length === 0) throw new Error('EV Sign input file is empty.');

  for (let attempt = 1; attempt <= maxAttempts; attempt += 1) {
    const started = Date.now();
    try {
      const signed = await request(source, { endpoint, headers, attemptTimeoutMs, maximumResponseBytes: source.length + SIGNATURE_GROWTH_LIMIT });
      if (!Buffer.isBuffer(signed) || signed.length === 0) throw integrityError('EV Sign returned an empty file.');
      if (signed.length > source.length + SIGNATURE_GROWTH_LIMIT) throw integrityError('EV Sign response exceeds the signed-file size limit.');
      const temporaryPath = `${outputPath}.signing-${process.pid}-${randomBytes(8).toString('hex')}`;
      try {
        await writeFile(temporaryPath, signed, { flag: 'wx' });
        await rename(temporaryPath, outputPath);
      } finally {
        await rm(temporaryPath, { force: true });
      }
      log(`EV Sign attempt ${attempt}/${maxAttempts} succeeded after ${Date.now() - started} ms.`);
      return;
    } catch (error) {
      const category = signingErrorCategory(error);
      const retryable = category === 'timeout' || category === 'transport' || category === 'http-retryable';
      const safeStatus = Number.isInteger(error?.statusCode) ? ` HTTP ${error.statusCode}` : '';
      if (!retryable) throw new Error(`EV Sign failed (${category}${safeStatus}): ${safeErrorMessage(error)}`, { cause: error });
      if (attempt === maxAttempts) throw new Error(`EV Sign failed after ${maxAttempts} attempts (${category}${safeStatus}).`, { cause: error });
      const delay = retryDelaysMs[attempt - 1];
      log(`EV Sign attempt ${attempt}/${maxAttempts} failed after ${Date.now() - started} ms (${category}${safeStatus}); retrying in ${delay} ms.`);
      await sleep(delay);
    }
  }
}

export async function signWithProfile(options, dependencies = {}) {
  const environment = options.environment || process.env;
  const signerProfile = resolveEVSignSignerProfile(options.profile, environment);
  const key = environment.EVSIGN_KEY?.trim();
  if (!key) throw new Error('EVSIGN_KEY is required. Store the EV Sign license UUID in GitHub Actions Secrets.');
  const inputPath = resolve(options.inputPath);
  const outputPath = resolve(options.outputPath || options.inputPath);
  const endpoint = environment.EVSIGN_ENDPOINT?.trim() || 'https://api.evsign.cn/v1';
  const headers = {
    'Content-Type': 'application/octet-stream',
    'X-Key': key,
    'X-Action': 'api-sign',
    'X-Algorithm': 'sha256',
    'X-File-Name': encodeURIComponent(basename(outputPath)),
    'X-Timestamp': environment.EVSIGN_TIMESTAMP?.trim() || 'auto',
    'X-Append': 'no',
    'X-Cert': signerProfile.certificate,
  };
  if (environment.EVSIGN_PASSWORD?.trim()) headers['X-Password'] = environment.EVSIGN_PASSWORD.trim();
  await signFileWithRetry({
    inputPath,
    outputPath,
    endpoint,
    headers,
    maxAttempts: readIntegerEnvironmentFrom(environment, 'EVSIGN_MAX_ATTEMPTS', DEFAULT_MAX_ATTEMPTS),
    attemptTimeoutMs: readIntegerEnvironmentFrom(environment, 'EVSIGN_ATTEMPT_TIMEOUT_MS', DEFAULT_ATTEMPT_TIMEOUT_MS),
    retryDelaysMs: readRetryDelaysFrom(environment),
  }, dependencies);
}

function signingErrorCategory(error) {
  if (error?.integrityFailure) return 'integrity';
  if (error?.code === 'ETIMEDOUT' || error?.name === 'AbortError') return 'timeout';
  if (Number.isInteger(error?.statusCode)) {
    if (error.statusCode === 408 || error.statusCode === 429 || (error.statusCode >= 500 && error.statusCode <= 599)) return 'http-retryable';
    return 'http-terminal';
  }
  if (RETRYABLE_NETWORK_CODES.has(error?.code)) return 'transport';
  return 'local';
}

function safeErrorMessage(error) {
  if (Number.isInteger(error?.statusCode)) return `HTTP ${error.statusCode}`;
  if (error?.integrityFailure) return error.message;
  if (RETRYABLE_NETWORK_CODES.has(error?.code) || error?.code === 'ETIMEDOUT') return error.code;
  return 'local signing operation failed';
}

function integrityError(message) {
  const error = new Error(message);
  error.integrityFailure = true;
  return error;
}

function validateRetryPolicy(maxAttempts, attemptTimeoutMs, retryDelaysMs) {
  if (!Number.isInteger(maxAttempts) || maxAttempts < 1 || maxAttempts > 5) throw new Error('EVSIGN_MAX_ATTEMPTS must be an integer from 1 to 5.');
  if (!Number.isInteger(attemptTimeoutMs) || attemptTimeoutMs < 1_000 || attemptTimeoutMs > 1_800_000) throw new Error('EVSIGN_ATTEMPT_TIMEOUT_MS is outside the allowed range.');
  if (!Array.isArray(retryDelaysMs) || retryDelaysMs.length !== maxAttempts - 1 || retryDelaysMs.some((value) => !Number.isInteger(value) || value < 0 || value > 300_000)) throw new Error('EVSIGN_RETRY_DELAYS_MS does not match the retry policy.');
}

function readIntegerEnvironment(name, fallback) {
  return readIntegerEnvironmentFrom(process.env, name, fallback);
}

function readIntegerEnvironmentFrom(environment, name, fallback) {
  const value = environment[name]?.trim();
  if (!value) return fallback;
  if (!/^\d+$/.test(value)) throw new Error(`${name} must contain one decimal integer.`);
  return Number(value);
}

function readRetryDelays() {
  return readRetryDelaysFrom(process.env);
}

function readRetryDelaysFrom(environment) {
  const value = environment.EVSIGN_RETRY_DELAYS_MS?.trim();
  if (!value) return [...DEFAULT_RETRY_DELAYS_MS];
  if (!/^\d+(?:,\d+)*$/.test(value)) throw new Error('EVSIGN_RETRY_DELAYS_MS must be comma-separated decimal integers.');
  return value.split(',').map(Number);
}

async function main() {
  const args = process.argv.slice(2);
  if (args[0] === '--resolve-profile') {
    if (args.length !== 2) throw new Error('Usage: node scripts/sign-evsign.mjs --resolve-profile stable|bridge');
    const resolved = resolveEVSignSignerProfile(args[1], process.env);
    console.log(JSON.stringify({ schema: resolved.schema, profile: resolved.profile, certificateConfigured: true, identity: resolved.identity }));
    return;
  }
  const explicitProfile = args[0] === '--profile';
  const profileName = explicitProfile ? args[1] : 'stable';
  const inputArg = explicitProfile ? args[2] : args[0];
  const outputArg = (explicitProfile ? args[3] : args[1]) || inputArg;
  if (explicitProfile && (args.length < 3 || args.length > 4)) throw new Error('Usage: node scripts/sign-evsign.mjs --profile stable|bridge <input.exe> [output.exe]');
  if (!inputArg) throw new Error('Usage: node scripts/sign-evsign.mjs <input.exe> [output.exe]');
  await signWithProfile({ profile: profileName, environment: process.env, inputPath: inputArg, outputPath: outputArg });
  const signed = await readFile(outputPath);
  console.log(`Signed ${basename(outputPath)} via EV Sign (${signed.length} bytes).`);
}

if (process.argv[1] && import.meta.url === pathToFileURL(resolve(process.argv[1])).href) await main();
