import { createHash, generateKeyPairSync, sign as signBytes } from 'node:crypto';
import { readFile } from 'node:fs/promises';
import { describe, expect, it } from 'vitest';
import {
  exchangeTencentSession,
  formatPublisherSummary,
  publishTrustPolicy,
  publisherTargets,
  type PublisherAdapters,
} from '../scripts/publish-trust-policy.mjs';

const task1SPKI = new URL('../goserver/testdata/update-trust/root-epoch-1-spki.der', import.meta.url);
const task1Policy = new URL('../goserver/testdata/update-trust/policy-epoch-1.json', import.meta.url);

async function verifiedEnvelope(overrides: { policy?: Buffer; spki?: Buffer; expectedPreviousEpoch?: number } = {}) {
  const policy = overrides.policy ?? Buffer.from((await readFile(task1Policy, 'utf8')).trimEnd());
  const spki = overrides.spki ?? await readFile(task1SPKI);
  const expectedPreviousEpoch = overrides.expectedPreviousEpoch ?? 0;
  const policySHA256 = sha256(policy);
  const audit = Buffer.from(JSON.stringify({
    keyId: 'task1-test-key',
    epoch: 1,
    policySha256: policySHA256,
    requestId: 'task1-test-request',
    utc: '2029-01-02T03:04:05Z',
    ciActor: 'task1-test-approver',
  }));
  const auditSHA256 = sha256(audit);
  return {
    schemaVersion: 2,
    verification: {
      epoch: 1,
      expectedPreviousEpoch,
      spkiSha256: sha256(spki),
    },
    commit: {
      schemaVersion: 1,
      policy: { name: 'policy.json', length: policy.length, sha256: policySHA256 },
      audit: { name: 'audit.json', length: audit.length, sha256: auditSHA256 },
    },
    policy: {
      name: 'policy.json', length: policy.length, sha256: policySHA256,
      bytesBase64: policy.toString('base64'),
    },
    audit: {
      name: 'audit.json', length: audit.length, sha256: auditSHA256,
      bytesBase64: audit.toString('base64'),
    },
  };
}

function sha256(bytes: Uint8Array): string {
  return createHash('sha256').update(bytes).digest('hex');
}

async function testOptions(overrides: Record<string, unknown> = {}) {
  const spki = await readFile(task1SPKI);
  return {
    mode: 'dry-run' as const,
    policyPath: 'must-not-be-read/policy.json',
    auditPath: 'must-not-be-read/audit.json',
    reviewedSPKIPath: task1SPKI,
    expectedSPKISHA256: sha256(spki),
    expectedPreviousEpoch: 0,
    advanceDiscovery: false,
    now: new Date('2029-01-02T03:04:05Z'),
    ...overrides,
  };
}

async function fakeAdapters(envelope?: Awaited<ReturnType<typeof verifiedEnvelope>>) {
	envelope ??= await verifiedEnvelope();
  const operations: string[] = [];
  const cosObjects = new Map<string, Buffer>();
  const githubAssets = new Map<string, Buffer>();
  const pointerWrites: string[] = [];
  const adapters: PublisherAdapters = {
    process: {
      run: async (command, args) => {
        operations.push(`process:${command}:${args[0] ?? ''}`);
        return { code: 0, stdout: `${JSON.stringify(envelope)}\n`, stderr: '' };
      },
    },
    files: { readFile },
    cos: {
      putImmutable: async (key, bytes) => {
        operations.push(`cos:put-immutable:${key}`);
        if (cosObjects.has(key)) throw new Error('immutable exists');
        cosObjects.set(key, Buffer.from(bytes));
      },
      read: async (key) => {
        operations.push(`cos:read:${key}`);
        const bytes = cosObjects.get(key);
        return bytes ? { bytes: Buffer.from(bytes), version: `cos-${sha256(bytes)}` } : null;
      },
      compareAndSwapPointer: async (key, bytes) => {
        operations.push(`cos:pointer:${key}`);
        pointerWrites.push(`cos:${key}`);
        cosObjects.set(key, Buffer.from(bytes));
      },
    },
    github: {
      publishImmutableRelease: async ({ tag, assets }) => {
        operations.push(`github:publish:${tag}`);
        for (const asset of assets) githubAssets.set(`${tag}/${asset.name}`, Buffer.from(asset.bytes));
      },
      downloadReleaseAsset: async (tag, name) => {
        operations.push(`github:download:${tag}/${name}`);
        const bytes = githubAssets.get(`${tag}/${name}`);
        if (!bytes) throw new Error('asset missing');
        return Buffer.from(bytes);
      },
      readPointer: async (ref, path) => {
        const bytes = githubAssets.get(`${ref}/${path}`);
        return bytes ? { bytes: Buffer.from(bytes), version: `github-${sha256(bytes)}` } : null;
      },
      compareAndSwapPointer: async ({ ref, path, bytes }) => {
        operations.push(`github:pointer:${ref}:${path}`);
        pointerWrites.push(`github:${ref}:${path}`);
        githubAssets.set(`${ref}/${path}`, Buffer.from(bytes));
      },
    },
  };
  return { adapters, cosObjects, githubAssets, operations, pointerWrites };
}

describe('protected publisher-policy transaction', () => {
  it('validates the Task 1 test-only root and prints only fixed targets and hashes in dry-run', async () => {
    const envelope = await verifiedEnvelope();
    const { adapters, operations } = await fakeAdapters(envelope);
    const summary = await publishTrustPolicy(await testOptions(), adapters);
    const output = formatPublisherSummary(summary);
    const targets = publisherTargets(1);

    expect(summary).toMatchObject({
      epoch: 1,
      policySHA256: envelope.policy.sha256,
      auditSHA256: envelope.audit.sha256,
      ...targets,
    });
    expect(operations).toHaveLength(1);
    expect(output).not.toContain(envelope.policy.bytesBase64);
    expect(output).not.toContain('must-not-be-read');
    expect(output).not.toContain('MEUC');
    expect(output).toBe(`${JSON.stringify(summary)}\n`);
  });

  it('independently rejects a machine envelope whose policy was signed by another root', async () => {
    const { privateKey } = generateKeyPairSync('ec', { namedCurve: 'P-256' });
    const original = JSON.parse((await readFile(task1Policy, 'utf8')).trimEnd()) as {
      signed: Record<string, unknown>;
      signatures: Array<{ algorithm: string; signature: string }>;
    };
    original.signatures[0]!.signature = signBytes(
      'sha256', Buffer.from(JSON.stringify(original.signed)), privateKey,
    ).toString('base64');
    const envelope = await verifiedEnvelope({ policy: Buffer.from(JSON.stringify(original)) });
    const { adapters, operations } = await fakeAdapters(envelope);

    await expect(publishTrustPolicy(await testOptions({
      reviewedSPKIPath: task1SPKI,
      expectedSPKISHA256: sha256(await readFile(task1SPKI)),
    }), adapters)).rejects.toThrow('publisher policy validation failed');
    expect(operations).toHaveLength(1);
  });

  it('does not advance either pointer when immutable COS publication fails', async () => {
    const { adapters, pointerWrites } = await fakeAdapters();
    adapters.cos.putImmutable = async () => { throw new Error('injected immutable failure'); };

    await expect(publishTrustPolicy(await testOptions({ mode: 'publish', advanceDiscovery: true }), adapters))
      .rejects.toThrow('publisher policy publication failed');
    expect(pointerWrites).toEqual([]);
  });

  it('does not advance either pointer when an immutable readback differs', async () => {
    const { adapters, pointerWrites } = await fakeAdapters();
    const originalRead = adapters.cos.read;
    adapters.cos.read = async (key) => {
      const result = await originalRead(key);
      if (key.startsWith('trust/publisher/epochs/') && result) return { ...result, bytes: Buffer.from('mismatch') };
      return result;
    };

    await expect(publishTrustPolicy(await testOptions({ mode: 'publish', advanceDiscovery: true }), adapters))
      .rejects.toThrow('publisher policy publication failed');
    expect(pointerWrites).toEqual([]);
  });

  it('publishes both immutable copies and re-verifies them before explicitly advancing discovery', async () => {
    const { adapters, operations, pointerWrites } = await fakeAdapters();
    const summary = await publishTrustPolicy(await testOptions({ mode: 'publish', advanceDiscovery: true }), adapters);
    const targets = publisherTargets(1);

    expect(pointerWrites).toEqual([
      `cos:${targets.cosPointerKey}`,
      `github:${targets.githubPointerRef}:${targets.githubPointerPath}`,
    ]);
    const lastImmutableRead = Math.max(
      operations.lastIndexOf(`cos:read:${targets.cosImmutableKey}`),
      operations.lastIndexOf(`github:download:${targets.githubReleaseTag}/${targets.githubAuditAsset}`),
    );
    expect(lastImmutableRead).toBeLessThan(operations.indexOf(`cos:pointer:${targets.cosPointerKey}`));
    expect(summary.advanceDiscovery).toBe(true);
  });

  it('rejects pointer advancement unless both discovery sources still hold the exact previous epoch', async () => {
    const { adapters, pointerWrites } = await fakeAdapters();
    await publishTrustPolicy(await testOptions({ mode: 'publish-immutable' }), adapters);
    const readCOS = adapters.cos.read;
    adapters.cos.read = async (key) => key === publisherTargets(1).cosPointerKey
      ? { bytes: Buffer.from((await readFile(task1Policy, 'utf8')).trimEnd()), version: 'unexpected-existing-pointer' }
      : readCOS(key);

    await expect(publishTrustPolicy(await testOptions({ mode: 'advance-discovery', advanceDiscovery: true }), adapters))
      .rejects.toThrow('publisher policy publication failed');
    expect(pointerWrites).toEqual([]);
  });

  it('never reopens raw bundle paths after the captured Go verification handoff', async () => {
    const { adapters } = await fakeAdapters();
    const reads: Array<string | URL> = [];
    adapters.files.readFile = async (path) => {
      reads.push(path);
      return readFile(path);
    };

    await publishTrustPolicy(await testOptions(), adapters);

    expect(reads).toEqual([task1SPKI]);
  });
});

describe('publisher Tencent session exchange', () => {
  it('exports and masks all three temporary STS values without a static or tokenless fallback', async () => {
    const requests: string[] = [];
    const writes: string[] = [];
    const masks: string[] = [];
    const environment = {
      ACTIONS_ID_TOKEN_REQUEST_URL: 'https://github-actions.example.test/oidc',
      ACTIONS_ID_TOKEN_REQUEST_TOKEN: 'github-request-token',
      PUBLISHER_TENCENT_OIDC_AUDIENCE: 'publisher-rotation',
      PUBLISHER_TENCENT_ROLE_ARN: 'qcs::cam::uin/123456789:roleName/publisher-kms',
      PUBLISHER_TENCENT_OIDC_PROVIDER_ID: 'github-actions-provider',
      GITHUB_OUTPUT: 'captured-output',
      GITHUB_RUN_ID: '1234',
      GITHUB_RUN_ATTEMPT: '2',
    };
    const result = await exchangeTencentSession(environment, {
      fetch: async (input) => {
        const url = String(input);
        requests.push(url);
        if (requests.length === 1) return new Response(JSON.stringify({ value: 'github-oidc-token' }), { status: 200 });
        return new Response(JSON.stringify({ Response: {
          Credentials: { TmpSecretId: 'temporary-id', TmpSecretKey: 'temporary-key', Token: 'temporary-token' },
          RequestId: 'sts-request-id',
        } }), { status: 200 });
      },
      appendFile: async (_path, data) => { writes.push(String(data)); },
      mask: (value) => { masks.push(value); },
    });

    expect(requests).toEqual([
      'https://github-actions.example.test/oidc?audience=publisher-rotation',
      'https://sts.tencentcloudapi.com',
    ]);
    expect(writes.join('')).toContain('secret-id<<PUBLISHER_EOF\ntemporary-id');
    expect(writes.join('')).toContain('secret-key<<PUBLISHER_EOF\ntemporary-key');
    expect(writes.join('')).toContain('session-token<<PUBLISHER_EOF\ntemporary-token');
    expect(masks).toEqual(['temporary-id', 'temporary-key', 'temporary-token']);
    expect(result).toEqual({ requestId: 'sts-request-id' });
  });
});
