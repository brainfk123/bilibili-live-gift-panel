import { createPrivateKey, createPublicKey, sign } from 'node:crypto';
import { existsSync, readFileSync, writeFileSync } from 'node:fs';
import { resolve } from 'node:path';

const [inputArgument, outputArgument] = process.argv.slice(2);
if (!inputArgument || !outputArgument) {
  throw new Error(
    'Usage: node scripts/sign-assistant-manifest.mjs <unsigned-manifest.json> <signed-manifest.json>',
  );
}
const inputPath = resolve(inputArgument);
const outputPath = resolve(outputArgument);
if (inputPath === outputPath) {
  throw new Error('Refusing to overwrite the unsigned source manifest.');
}

const inlineKey = process.env.ASSISTANT_MANIFEST_PRIVATE_KEY;
const keyFile = process.env.ASSISTANT_MANIFEST_PRIVATE_KEY_FILE;
if (inlineKey && keyFile) {
  throw new Error('Set only one of ASSISTANT_MANIFEST_PRIVATE_KEY or ASSISTANT_MANIFEST_PRIVATE_KEY_FILE.');
}
if (!inlineKey && !keyFile) {
  throw new Error('An Ed25519 private key must be supplied through the environment or an external file.');
}
if (keyFile && !existsSync(resolve(keyFile))) {
  throw new Error(`Private key file does not exist: ${resolve(keyFile)}`);
}

const privateKeyPem = inlineKey || readFileSync(resolve(keyFile), 'utf8');
const privateKeyPassphrase = process.env.ASSISTANT_MANIFEST_PRIVATE_KEY_PASSPHRASE;
const privateKey = createPrivateKey({
  key: privateKeyPem,
  format: 'pem',
  passphrase: privateKeyPassphrase || undefined,
});
if (privateKey.asymmetricKeyType !== 'ed25519') {
  throw new Error(`Expected an Ed25519 private key, got ${privateKey.asymmetricKeyType || 'unknown'}.`);
}

const publicJwk = createPublicKey(privateKey).export({ format: 'jwk' });
const publicKeyBase64 = Buffer.from(publicJwk.x, 'base64url').toString('base64');
const expectedPublicKeyBase64 = process.env.ASSISTANT_MANIFEST_PUBLIC_KEY_BASE64?.trim();
if (expectedPublicKeyBase64) {
  const expectedPublicKey = Buffer.from(expectedPublicKeyBase64, 'base64');
  if (
    expectedPublicKey.length !== 32 ||
    expectedPublicKey.toString('base64') !== expectedPublicKeyBase64
  ) {
    throw new Error('ASSISTANT_MANIFEST_PUBLIC_KEY_BASE64 is not a canonical 32-byte Ed25519 public key.');
  }
  if (expectedPublicKeyBase64 !== publicKeyBase64) {
    throw new Error('The signing private key does not match ASSISTANT_MANIFEST_PUBLIC_KEY_BASE64.');
  }
}

const manifest = JSON.parse(readFileSync(inputPath, 'utf8'));
validateManifestShape(manifest);
const payload = Buffer.from(JSON.stringify(manifest), 'utf8');
const signature = sign(null, payload, privateKey).toString('base64');
const envelope = `{"payload":${payload.toString('utf8')},"signature":${JSON.stringify(signature)}}\n`;
writeFileSync(outputPath, envelope, { encoding: 'utf8', flag: 'wx' });

console.log(`signed assistant manifest: ${outputPath}`);
console.log(`ASSISTANT_MANIFEST_PUBLIC_KEY_BASE64=${publicKeyBase64}`);

function validateManifestShape(value) {
  const requiredStrings = [
    'modelId',
    'version',
    'repository',
    'revision',
    'file',
    'sha256',
    'architecture',
    'quantization',
  ];
  if (!value || Array.isArray(value) || typeof value !== 'object' || value.schemaVersion !== 1) {
    throw new Error('Manifest must be a schemaVersion 1 JSON object.');
  }
  for (const field of requiredStrings) {
    if (typeof value[field] !== 'string' || value[field].trim() === '') {
      throw new Error(`Manifest field ${field} must be a non-empty string.`);
    }
  }
  if (!Number.isSafeInteger(value.sizeBytes) || value.sizeBytes <= 0 || value.sizeBytes > 2 ** 30) {
    throw new Error('Manifest sizeBytes must be a positive integer no larger than 1 GiB.');
  }
  if (!/^[a-f0-9]{64}$/i.test(value.sha256)) {
    throw new Error('Manifest sha256 must contain exactly 64 hexadecimal characters.');
  }
  if (!/^[0-9A-Za-z][0-9A-Za-z._-]{0,63}$/.test(value.version)) {
    throw new Error('Manifest version contains unsupported characters.');
  }
  if (['main', 'master', 'latest', 'head'].includes(value.revision.toLowerCase())) {
    throw new Error('Manifest revision must be immutable, not a floating branch.');
  }
  if (value.file.includes('/') || value.file.includes('\\') || value.file === '.' || value.file === '..') {
    throw new Error('Manifest file must be a basename, not a path.');
  }
  if (value.architecture.toLowerCase() !== 'qwen3' || value.quantization.toUpperCase() !== 'Q8_0') {
    throw new Error('Only a Qwen3 Q8_0 model can be signed for this application.');
  }
}
