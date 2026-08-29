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
const SIGNER_PROFILE_KEYS = new Set(['name', 'cert', 'subject']);

export function resolveEVSignSignerProfile(environment = process.env) {
  const profilesJSON = environment.EVSIGN_SIGNER_PROFILES_JSON?.trim() || '';
  const activeProfile = environment.EVSIGN_ACTIVE_PROFILE?.trim() || '';
  if ((profilesJSON === '') !== (activeProfile === '')) {
    throw new Error('EVSIGN_SIGNER_PROFILES_JSON and EVSIGN_ACTIVE_PROFILE must be configured together.');
  }

  if (!profilesJSON) {
    const cert = environment.EVSIGN_CERT?.trim() || '';
    const subject = environment.EVSIGN_EXPECTED_SUBJECT?.trim() || '';
    if (!subject) throw new Error('EVSIGN_EXPECTED_SUBJECT is required when signer profiles are not configured.');
    validateProfileValue('cert', cert, 1024, true);
    validateProfileValue('subject', subject, 4096, false);
    return { schema: 1, source: 'legacy', profile: 'legacy', cert, subject };
  }

  if (!/^[a-z0-9][a-z0-9._-]{0,63}$/.test(activeProfile)) {
    throw new Error('EVSIGN_ACTIVE_PROFILE is invalid.');
  }

  let profiles;
  try {
    profiles = JSON.parse(profilesJSON);
  } catch {
    throw new Error('EVSIGN_SIGNER_PROFILES_JSON is not valid JSON.');
  }
  if (!Array.isArray(profiles) || profiles.length < 1 || profiles.length > 16) {
    throw new Error('EVSIGN_SIGNER_PROFILES_JSON must contain an array of 1 to 16 profiles.');
  }

  const names = new Set();
  let selected;
  for (const profile of profiles) {
    if (!profile || typeof profile !== 'object' || Array.isArray(profile)) {
      throw new Error('Each EVSign signer profile must be an object.');
    }
    const keys = Object.keys(profile);
    if (keys.some((key) => !SIGNER_PROFILE_KEYS.has(key)) || keys.length !== SIGNER_PROFILE_KEYS.size) {
      throw new Error('EVSign signer profile contains missing or unknown properties.');
    }
    const { name, cert, subject } = profile;
    const normalizedCert = cert === null ? '' : cert;
    if (typeof name !== 'string' || !/^[a-z0-9][a-z0-9._-]{0,63}$/.test(name)) {
      throw new Error('EVSign signer profile name is invalid.');
    }
    if (names.has(name)) throw new Error('EVSign signer profile name is duplicated.');
    names.add(name);
    validateProfileValue('cert', normalizedCert, 1024, true);
    validateProfileValue('subject', subject, 4096, false);
    if (name === activeProfile) selected = { schema: 1, source: 'profiles', profile: name, cert: normalizedCert, subject };
  }
  if (!selected) throw new Error('The active EVSign signer profile does not exist.');
  return selected;
}

function validateProfileValue(name, value, maximumBytes, allowEmpty) {
  if (typeof value !== 'string' || value !== value.trim() || (!allowEmpty && value.length === 0) || /[\r\n]/.test(value) || Buffer.byteLength(value, 'utf8') > maximumBytes) {
    throw new Error(`EVSign signer profile ${name} is invalid.`);
  }
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
  const value = process.env[name]?.trim();
  if (!value) return fallback;
  if (!/^\d+$/.test(value)) throw new Error(`${name} must contain one decimal integer.`);
  return Number(value);
}

function readRetryDelays() {
  const value = process.env.EVSIGN_RETRY_DELAYS_MS?.trim();
  if (!value) return [...DEFAULT_RETRY_DELAYS_MS];
  if (!/^\d+(?:,\d+)*$/.test(value)) throw new Error('EVSIGN_RETRY_DELAYS_MS must be comma-separated decimal integers.');
  return value.split(',').map(Number);
}

async function main() {
  const [inputArg, outputArg = inputArg] = process.argv.slice(2);
  if (inputArg === '--resolve-profile') {
    if (process.argv.length !== 3) throw new Error('Usage: node scripts/sign-evsign.mjs --resolve-profile');
    console.log(JSON.stringify(resolveEVSignSignerProfile(process.env)));
    return;
  }
  if (!inputArg) throw new Error('Usage: node scripts/sign-evsign.mjs <input.exe> [output.exe]');
  const key = process.env.EVSIGN_KEY?.trim();
  if (!key) throw new Error('EVSIGN_KEY is required. Store the EV Sign license UUID in GitHub Actions Secrets.');
  const inputPath = resolve(inputArg);
  const outputPath = resolve(outputArg);
  const endpoint = process.env.EVSIGN_ENDPOINT?.trim() || 'https://api.evsign.cn/v1';
  const headers = { 'Content-Type': 'application/octet-stream', 'X-Key': key, 'X-Action': 'api-sign', 'X-Algorithm': 'sha256', 'X-File-Name': encodeURIComponent(basename(outputPath)), 'X-Timestamp': process.env.EVSIGN_TIMESTAMP?.trim() || 'auto', 'X-Append': 'no' };
  for (const [name, value] of [['X-Cert', process.env.EVSIGN_CERT], ['X-Password', process.env.EVSIGN_PASSWORD]]) if (value?.trim()) headers[name] = value.trim();
  await signFileWithRetry({ inputPath, outputPath, endpoint, headers, maxAttempts: readIntegerEnvironment('EVSIGN_MAX_ATTEMPTS', DEFAULT_MAX_ATTEMPTS), attemptTimeoutMs: readIntegerEnvironment('EVSIGN_ATTEMPT_TIMEOUT_MS', DEFAULT_ATTEMPT_TIMEOUT_MS), retryDelaysMs: readRetryDelays() });
  const signed = await readFile(outputPath);
  console.log(`Signed ${basename(outputPath)} via EV Sign (${signed.length} bytes).`);
}

if (process.argv[1] && import.meta.url === pathToFileURL(resolve(process.argv[1])).href) await main();
