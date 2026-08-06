import { readFile, rename, rm, writeFile } from 'node:fs/promises';
import { basename, resolve } from 'node:path';

const [inputArg, outputArg = inputArg] = process.argv.slice(2);
if (!inputArg) {
  throw new Error('Usage: node scripts/sign-evsign.mjs <input.exe> [output.exe]');
}

const key = process.env.EVSIGN_KEY?.trim();
if (!key) {
  throw new Error('EVSIGN_KEY is required. Store the EV Sign license UUID in GitHub Actions Secrets.');
}

const inputPath = resolve(inputArg);
const outputPath = resolve(outputArg);
const temporaryPath = `${outputPath}.signing`;
const endpoint = process.env.EVSIGN_ENDPOINT?.trim() || 'https://api.evsign.cn/v1';
const headers = {
  'Content-Type': 'application/octet-stream',
  'X-Key': key,
  'X-Action': 'api-sign',
  'X-Algorithm': 'sha256',
  'X-File-Name': encodeURIComponent(basename(outputPath)),
  'X-Timestamp': process.env.EVSIGN_TIMESTAMP?.trim() || 'auto',
  'X-Append': 'no',
};

const optionalHeaders = [
  ['X-Cert', process.env.EVSIGN_CERT],
  ['X-Password', process.env.EVSIGN_PASSWORD],
];
for (const [name, value] of optionalHeaders) {
  if (value?.trim()) headers[name] = value.trim();
}

const source = await readFile(inputPath);
const controller = new AbortController();
const timeout = setTimeout(() => controller.abort(), 15 * 60 * 1000);

try {
  const response = await fetch(endpoint, {
    method: 'POST',
    headers,
    body: source,
    signal: controller.signal,
  });

  if (response.status !== 200) {
    const message = (await response.text()).trim();
    throw new Error(`EV Sign failed with HTTP ${response.status}${message ? `: ${message}` : ''}`);
  }

  const signed = Buffer.from(await response.arrayBuffer());
  if (signed.length === 0) {
    throw new Error('EV Sign returned an empty file.');
  }

  await writeFile(temporaryPath, signed);
  await rm(outputPath, { force: true });
  await rename(temporaryPath, outputPath);
  console.log(`Signed ${basename(outputPath)} via EV Sign (${signed.length} bytes).`);
} finally {
  clearTimeout(timeout);
  await rm(temporaryPath, { force: true });
}
