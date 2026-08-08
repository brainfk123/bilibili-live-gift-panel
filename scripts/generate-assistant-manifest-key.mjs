import { generateKeyPairSync } from 'node:crypto';
import { existsSync, mkdirSync, writeFileSync } from 'node:fs';
import { resolve } from 'node:path';

const [outputDirectoryArgument] = process.argv.slice(2);
if (!outputDirectoryArgument) {
  throw new Error('Usage: node scripts/generate-assistant-manifest-key.mjs <output-directory>');
}
const passphrase = process.env.ASSISTANT_MANIFEST_PRIVATE_KEY_PASSPHRASE;
if (!passphrase || Buffer.byteLength(passphrase, 'utf8') < 24) {
  throw new Error('ASSISTANT_MANIFEST_PRIVATE_KEY_PASSPHRASE must contain at least 24 UTF-8 bytes.');
}

const outputDirectory = resolve(outputDirectoryArgument);
const privateKeyPath = resolve(outputDirectory, 'assistant-manifest-ed25519.private.pem');
const publicKeyPath = resolve(outputDirectory, 'assistant-manifest-ed25519.public.pem');
const publicKeyBase64Path = resolve(outputDirectory, 'assistant-manifest-ed25519.public-base64.txt');
for (const path of [privateKeyPath, publicKeyPath, publicKeyBase64Path]) {
  if (existsSync(path)) throw new Error(`Refusing to replace existing key material: ${path}`);
}

mkdirSync(
  outputDirectory,
  process.platform === 'win32' ? { recursive: true } : { recursive: true, mode: 0o700 },
);
const { privateKey, publicKey } = generateKeyPairSync('ed25519');
const encryptedPrivateKey = privateKey.export({
  type: 'pkcs8',
  format: 'pem',
  cipher: 'aes-256-cbc',
  passphrase,
});
const publicKeyPem = publicKey.export({ type: 'spki', format: 'pem' });
const publicJwk = publicKey.export({ format: 'jwk' });
const publicKeyBase64 = Buffer.from(publicJwk.x, 'base64url').toString('base64');

const privateWriteOptions = process.platform === 'win32' ? { flag: 'wx' } : { flag: 'wx', mode: 0o600 };
const publicWriteOptions = process.platform === 'win32' ? { flag: 'wx' } : { flag: 'wx', mode: 0o644 };
writeFileSync(privateKeyPath, encryptedPrivateKey, privateWriteOptions);
writeFileSync(publicKeyPath, publicKeyPem, publicWriteOptions);
writeFileSync(publicKeyBase64Path, `${publicKeyBase64}\n`, publicWriteOptions);

console.log(`generated encrypted Ed25519 key: ${privateKeyPath}`);
console.log(`generated Ed25519 public key: ${publicKeyPath}`);
console.log(`ASSISTANT_MANIFEST_PUBLIC_KEY_BASE64=${publicKeyBase64}`);
