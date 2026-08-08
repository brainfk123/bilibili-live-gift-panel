import { createPublicKey, verify } from 'node:crypto';
import { readFile } from 'node:fs/promises';
import { resolve } from 'node:path';

const maximumManifestBytes = 256 * 1024;
const allowedHosts = [
  'modelscope.cn',
  'modelscope.oss-cn-beijing.aliyuncs.com',
  'modelscope.oss-cn-hangzhou.aliyuncs.com',
  'modelscope.oss-cn-shanghai.aliyuncs.com',
];

const [source] = process.argv.slice(2);
if (!source) {
  throw new Error('Usage: node scripts/verify-assistant-manifest.mjs <signed-manifest.json|https-url>');
}

const publicKeyBase64 = process.env.ASSISTANT_MANIFEST_PUBLIC_KEY_BASE64?.trim();
if (!publicKeyBase64) {
  throw new Error('ASSISTANT_MANIFEST_PUBLIC_KEY_BASE64 is required.');
}
const publicKeyBytes = Buffer.from(publicKeyBase64, 'base64');
if (publicKeyBytes.length !== 32 || publicKeyBytes.toString('base64') !== publicKeyBase64) {
  throw new Error('ASSISTANT_MANIFEST_PUBLIC_KEY_BASE64 is not a canonical 32-byte Ed25519 public key.');
}
const publicKey = createPublicKey({
  key: { kty: 'OKP', crv: 'Ed25519', x: publicKeyBytes.toString('base64url') },
  format: 'jwk',
});

const bytes = /^https:/i.test(source) ? await fetchManifest(source) : await readLocalManifest(source);
let envelope;
try {
  envelope = JSON.parse(bytes.toString('utf8'));
} catch {
  throw new Error('Signed manifest is not valid JSON.');
}
if (!envelope || Array.isArray(envelope) || typeof envelope !== 'object') {
  throw new Error('Signed manifest envelope must be an object.');
}
if (!envelope.payload || Array.isArray(envelope.payload) || typeof envelope.payload !== 'object') {
  throw new Error('Signed manifest payload must be an object.');
}
if (typeof envelope.signature !== 'string') {
  throw new Error('Signed manifest signature must be Base64 text.');
}
const signature = Buffer.from(envelope.signature, 'base64');
if (signature.length !== 64 || signature.toString('base64') !== envelope.signature) {
  throw new Error('Signed manifest signature is not canonical Ed25519 Base64.');
}
const payload = Buffer.from(JSON.stringify(envelope.payload), 'utf8');
if (!verify(null, payload, publicKey, signature)) {
  throw new Error('Assistant model manifest signature is invalid.');
}

console.log(
  `verified assistant manifest: ${envelope.payload.version} ` +
    `(${envelope.payload.repository}@${envelope.payload.revision})`,
);

async function readLocalManifest(value) {
  const data = await readFile(resolve(value));
  if (data.length > maximumManifestBytes) {
    throw new Error('Signed manifest exceeds the 256 KiB limit.');
  }
  return data;
}

async function fetchManifest(value) {
  let current = new URL(value);
  for (let redirects = 0; redirects <= 10; redirects += 1) {
    validateRemoteURL(current);
    const response = await fetch(current, {
      redirect: 'manual',
      signal: AbortSignal.timeout(30_000),
      headers: { Accept: 'application/json' },
    });
    if ([301, 302, 303, 307, 308].includes(response.status)) {
      const location = response.headers.get('location');
      if (!location) throw new Error(`Manifest redirect ${response.status} has no Location header.`);
      current = new URL(location, current);
      continue;
    }
    if (!response.ok) throw new Error(`Fetching assistant manifest failed: HTTP ${response.status}.`);
    const declaredLength = Number(response.headers.get('content-length'));
    if (Number.isFinite(declaredLength) && declaredLength > maximumManifestBytes) {
      throw new Error('Signed manifest exceeds the 256 KiB limit.');
    }
    const data = Buffer.from(await response.arrayBuffer());
    if (data.length > maximumManifestBytes) {
      throw new Error('Signed manifest exceeds the 256 KiB limit.');
    }
    return data;
  }
  throw new Error('Assistant manifest exceeded the 10 redirect limit.');
}

function validateRemoteURL(value) {
  if (value.protocol !== 'https:' || value.username || value.password) {
    throw new Error('Assistant manifest must use an HTTPS URL without credentials.');
  }
  const hostname = value.hostname.toLowerCase();
  if (!allowedHosts.some((host) => hostname === host || hostname.endsWith(`.${host}`))) {
    throw new Error(`Assistant manifest host is not allowed: ${hostname}.`);
  }
}
