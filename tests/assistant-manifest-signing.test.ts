import { execFileSync } from 'node:child_process';
import { generateKeyPairSync } from 'node:crypto';
import { mkdtempSync, readFileSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join, resolve } from 'node:path';
import { describe, expect, it } from 'vitest';

const signer = resolve('scripts/sign-assistant-manifest.mjs');
const verifier = resolve('scripts/verify-assistant-manifest.mjs');

describe('assistant model manifest signing', () => {
  it('signs and verifies an encrypted Ed25519 key without exposing it to CI', () => {
    const directory = mkdtempSync(join(tmpdir(), 'assistant-manifest-'));
    const passphrase = 'test-only-recovery-passphrase';
    const { privateKey, publicKey } = generateKeyPairSync('ed25519');
    const privatePem = privateKey.export({
      type: 'pkcs8',
      format: 'pem',
      cipher: 'aes-256-cbc',
      passphrase,
    });
    const publicJwk = publicKey.export({ format: 'jwk' });
    const publicKeyBase64 = Buffer.from(publicJwk.x!, 'base64url').toString('base64');
    const keyPath = join(directory, 'private.pem');
    const unsignedPath = join(directory, 'unsigned.json');
    const signedPath = join(directory, 'manifest.json');
    writeFileSync(keyPath, privatePem);
    writeFileSync(unsignedPath, JSON.stringify(validManifest()));

    execFileSync(process.execPath, [signer, unsignedPath, signedPath], {
      env: {
        ...process.env,
        ASSISTANT_MANIFEST_PRIVATE_KEY_FILE: keyPath,
        ASSISTANT_MANIFEST_PRIVATE_KEY_PASSPHRASE: passphrase,
        ASSISTANT_MANIFEST_PUBLIC_KEY_BASE64: publicKeyBase64,
      },
    });
    expect(() =>
      execFileSync(process.execPath, [verifier, signedPath], {
        env: { ...process.env, ASSISTANT_MANIFEST_PUBLIC_KEY_BASE64: publicKeyBase64 },
      }),
    ).not.toThrow();

    const envelope = JSON.parse(readFileSync(signedPath, 'utf8'));
    envelope.payload.version = 'tampered';
    writeFileSync(signedPath, JSON.stringify(envelope));
    expect(() =>
      execFileSync(process.execPath, [verifier, signedPath], {
        env: { ...process.env, ASSISTANT_MANIFEST_PUBLIC_KEY_BASE64: publicKeyBase64 },
        stdio: 'pipe',
      }),
    ).toThrow();
  });

  it('refuses a signing key that does not match the configured application key', () => {
    const directory = mkdtempSync(join(tmpdir(), 'assistant-manifest-'));
    const { privateKey } = generateKeyPairSync('ed25519');
    const { publicKey: otherPublicKey } = generateKeyPairSync('ed25519');
    const otherPublicJwk = otherPublicKey.export({ format: 'jwk' });
    const keyPath = join(directory, 'private.pem');
    const unsignedPath = join(directory, 'unsigned.json');
    const signedPath = join(directory, 'manifest.json');
    writeFileSync(keyPath, privateKey.export({ type: 'pkcs8', format: 'pem' }));
    writeFileSync(unsignedPath, JSON.stringify(validManifest()));

    expect(() =>
      execFileSync(process.execPath, [signer, unsignedPath, signedPath], {
        env: {
          ...process.env,
          ASSISTANT_MANIFEST_PRIVATE_KEY_FILE: keyPath,
          ASSISTANT_MANIFEST_PUBLIC_KEY_BASE64: Buffer.from(
            otherPublicJwk.x!,
            'base64url',
          ).toString('base64'),
        },
        stdio: 'pipe',
      }),
    ).toThrow();
  });
});

function validManifest() {
  return {
    schemaVersion: 1,
    modelId: 'Qwen3-0.6B-GGUF-Q8_0',
    version: '0.1.0',
    repository: 'Qwen/Qwen3-0.6B-GGUF',
    revision: '0123456789abcdef',
    file: 'Qwen3-0.6B-Q8_0.gguf',
    sizeBytes: 670_000_000,
    sha256: 'a'.repeat(64),
    architecture: 'qwen3',
    quantization: 'Q8_0',
    minAppVersion: '0.3.0',
  };
}
