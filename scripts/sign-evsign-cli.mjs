import { spawn } from 'node:child_process';
import { readFile, writeFile } from 'node:fs/promises';
import { resolve } from 'node:path';
import { pathToFileURL } from 'node:url';

const DEFAULT_MAX_ATTEMPTS = 3;
const DEFAULT_ATTEMPT_TIMEOUT_MS = 600_000;
const DEFAULT_RETRY_DELAYS_MS = Object.freeze([15_000, 45_000]);

export async function signFileWithCliRetry(options, dependencies = {}) {
  const {
    cliPath, inputPath, key, cert, password,
    maxAttempts = DEFAULT_MAX_ATTEMPTS,
    attemptTimeoutMs = DEFAULT_ATTEMPT_TIMEOUT_MS,
    retryDelaysMs = DEFAULT_RETRY_DELAYS_MS,
  } = options;
  validateOptions({ cliPath, inputPath, key, maxAttempts, attemptTimeoutMs, retryDelaysMs });
  const original = await readFile(inputPath);
  if (original.length === 0) throw new Error('EVSign CLI input file is empty.');
  const run = dependencies.run || runCli;
  const sleep = dependencies.sleep || ((milliseconds) => new Promise((resolvePromise) => setTimeout(resolvePromise, milliseconds)));
  const log = dependencies.log || console.log;
  const args = [inputPath, '-key', key, '-sha256'];
  if (cert?.trim()) args.push('-cert', cert.trim());
  if (password?.trim()) args.push('-pwd', password.trim());

  for (let attempt = 1; attempt <= maxAttempts; attempt += 1) {
    await writeFile(inputPath, original);
    const started = Date.now();
    let result;
    try {
      result = await run(cliPath, args, attemptTimeoutMs);
    } catch (error) {
      await writeFile(inputPath, original);
      throw new Error('EVSign CLI could not be started.', { cause: error });
    }
    if (result?.exitCode === 0 && result.timedOut !== true) {
      const signed = await readFile(inputPath);
      if (signed.length === 0 || signed.equals(original)) {
        await writeFile(inputPath, original);
        throw new Error('EVSign CLI reported success without producing a signed output.');
      }
      log(`EVSign CLI attempt ${attempt}/${maxAttempts} succeeded after ${Date.now() - started} ms.`);
      return;
    }
    if (attempt === maxAttempts) {
      await writeFile(inputPath, original);
      throw new Error(`EVSign CLI failed after ${maxAttempts} attempts (${result?.timedOut ? 'timeout' : 'exit failure'}).`);
    }
    const delay = retryDelaysMs[attempt - 1];
    log(`EVSign CLI attempt ${attempt}/${maxAttempts} failed after ${Date.now() - started} ms (${result?.timedOut ? 'timeout' : 'exit failure'}); retrying in ${delay} ms.`);
    await sleep(delay);
  }
}

function runCli(cliPath, args, timeoutMs) {
  return new Promise((resolvePromise, rejectPromise) => {
    const child = spawn(cliPath, args, { stdio: 'ignore', windowsHide: true });
    let settled = false;
    let timer;
    const finish = (value) => {
      if (settled) return;
      settled = true;
      clearTimeout(timer);
      resolvePromise(value);
    };
    child.once('error', (error) => {
      if (settled) return;
      settled = true;
      clearTimeout(timer);
      rejectPromise(error);
    });
    child.once('exit', (code) => finish({ exitCode: code, timedOut: false }));
    timer = setTimeout(() => {
      child.kill();
      finish({ exitCode: null, timedOut: true });
    }, timeoutMs);
  });
}

function validateOptions({ cliPath, inputPath, key, maxAttempts, attemptTimeoutMs, retryDelaysMs }) {
  if (!cliPath || !inputPath || !key) throw new Error('EVSign CLI path, input path, and license key are required.');
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
  const inputPath = resolve(process.argv[2] || '');
  const cliPath = resolve(process.env.EVSIGN_CLI_PATH || '');
  const key = process.env.EVSIGN_KEY?.trim();
  if (!process.argv[2] || !process.env.EVSIGN_CLI_PATH || !key) throw new Error('Usage: EVSIGN_CLI_PATH=<path> EVSIGN_KEY=<secret> node scripts/sign-evsign-cli.mjs <input.exe>');
  await signFileWithCliRetry({
    cliPath, inputPath, key,
    cert: process.env.EVSIGN_CERT,
    password: process.env.EVSIGN_PASSWORD,
    maxAttempts: readIntegerEnvironment('EVSIGN_MAX_ATTEMPTS', DEFAULT_MAX_ATTEMPTS),
    attemptTimeoutMs: readIntegerEnvironment('EVSIGN_ATTEMPT_TIMEOUT_MS', DEFAULT_ATTEMPT_TIMEOUT_MS),
    retryDelaysMs: readRetryDelays(),
  });
}

if (process.argv[1] && import.meta.url === pathToFileURL(resolve(process.argv[1])).href) await main();
