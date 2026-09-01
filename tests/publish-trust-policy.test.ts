import { createHash, generateKeyPairSync, sign as signBytes, type KeyObject } from 'node:crypto';
import { readFile } from 'node:fs/promises';
import { describe, expect, it } from 'vitest';
import {
  createCOSPublisherAdapter,
  createGitHubPublisherAdapter,
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
  const epoch = (JSON.parse(policy.toString('utf8')) as { signed: { epoch: number } }).signed.epoch;
  const policySHA256 = sha256(policy);
  const audit = Buffer.from(JSON.stringify({
    keyId: 'task1-test-key',
    epoch,
    policySha256: policySHA256,
    requestId: 'task1-test-request',
    utc: '2029-01-02T03:04:05Z',
    ciActor: 'task1-test-approver',
  }));
  const auditSHA256 = sha256(audit);
  return {
    schemaVersion: 2,
    verification: {
      epoch,
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

function makeSignedPolicy(privateKey: KeyObject, epoch: number, expiresAt: string) {
  const signed = {
    epoch,
    expiresAt,
    publishers: [{
      id: 'naisnet-primary', role: 'primary', country: 'CN',
      organization: 'NaisNet Technology Co., Ltd.', organizationId: '91210103MA7CJ3C094',
      allowedChannel: 'stable', allowedTags: ['v0.4.12'],
    }],
  };
  const signature = signBytes('sha256', Buffer.from(JSON.stringify(signed)), privateKey).toString('base64');
  return Buffer.from(JSON.stringify({ signed, signatures: [{ algorithm: 'ecdsa-p256-sha256', signature }] }));
}

function sha256(bytes: Uint8Array): string {
  return createHash('sha256').update(bytes).digest('hex');
}

function gitVersion(bytes: Uint8Array): string {
  return createHash('sha1').update(bytes).digest('hex');
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
        return bytes ? {
          bytes: Buffer.from(bytes), version: `cos-${sha256(bytes)}`,
          sha256: sha256(bytes), contentType: 'application/json',
        } : null;
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

async function epochTwoFixture() {
  const { privateKey, publicKey } = generateKeyPairSync('ec', { namedCurve: 'P-256' });
  const spki = publicKey.export({ format: 'der', type: 'spki' });
  const previous = makeSignedPolicy(privateKey, 1, '2028-01-01T00:00:00Z');
  const candidate = makeSignedPolicy(privateKey, 2, '2030-01-01T00:00:00Z');
  const envelope = await verifiedEnvelope({ policy: candidate, spki, expectedPreviousEpoch: 1 });
  const state = await fakeAdapters(envelope);
  state.adapters.files.readFile = async () => spki;
  const options = await testOptions({
    policyPath: 'epoch-two/policy.json',
    auditPath: 'epoch-two/audit.json',
    reviewedSPKIPath: 'epoch-two/root.der',
    expectedSPKISHA256: sha256(spki),
    expectedPreviousEpoch: 1,
    now: new Date('2029-01-02T03:04:05Z'),
  });
  return { ...state, candidate, envelope, options, previous, privateKey, spki };
}

const remoteEnvironment = {
  COS_BUCKET: 'publisher-policy-1250000000',
  COS_REGION: 'ap-shanghai',
  TENCENTCLOUD_SECRET_ID: 'temporary-id',
  TENCENTCLOUD_SECRET_KEY: 'temporary-key',
  TENCENTCLOUD_SESSION_TOKEN: 'temporary-token',
  GITHUB_REPOSITORY: 'owner/repository',
  GITHUB_SHA: 'a'.repeat(40),
  GH_TOKEN: 'github-token-must-not-forward',
};

function releaseWithAsset(tag: string, name: string, bytes: Buffer, id = 1) {
  return {
    id: 99,
    tag_name: tag,
    draft: false,
    prerelease: false,
    upload_url: 'https://uploads.github.com/repos/owner/repository/releases/99/assets{?name,label}',
    assets: [{
      id,
      name,
      url: `https://api.github.com/repos/owner/repository/releases/assets/${id}`,
      size: bytes.length,
      content_type: 'application/json',
      state: 'uploaded',
      digest: `sha256:${sha256(bytes)}`,
    }],
  };
}

function streamingResponse(
  chunks: Uint8Array[],
  options: { status?: number; headers?: Record<string, string>; onPull?: () => void } = {},
) {
  let index = 0;
  return new Response(new ReadableStream<Uint8Array>({
    pull(controller) {
      options.onPull?.();
      if (index >= chunks.length) {
        controller.close();
        return;
      }
      controller.enqueue(chunks[index++]);
    },
  }), { status: options.status ?? 200, headers: options.headers });
}

describe('real publisher remote adapters', () => {
  it.each([
    { name: 'direct 200', redirect: false },
    { name: 'one reviewed 302', redirect: true },
  ])('downloads a bounded GitHub policy asset through $name', async ({ redirect }) => {
    const tag = 'publisher-policy-epoch-00000001';
    const bytes = Buffer.from('{"policy":"public"}');
    const release = releaseWithAsset(tag, 'gift-panel-publisher-policy.json', bytes);
    const requests: Array<{ url: string; headers: Headers; redirect?: RequestRedirect; credentials?: RequestCredentials }> = [];
    const fetchFake = async (input: string | URL | Request, init?: RequestInit) => {
      const url = String(input);
      requests.push({ url, headers: new Headers(init?.headers), redirect: init?.redirect, credentials: init?.credentials });
      if (url.includes('/releases/tags/')) return new Response(JSON.stringify(release), { status: 200 });
      if (url.includes('/releases/assets/')) {
        if (!redirect) return streamingResponse([bytes], { headers: { 'content-length': String(bytes.length), 'content-type': 'application/json' } });
        return new Response(null, { status: 302, headers: { location: 'https://release-assets.githubusercontent.com/github-production-release-asset/1/policy.json?sig=signed-query' } });
      }
      if (url.startsWith('https://release-assets.githubusercontent.com/')) {
        return streamingResponse([bytes.subarray(0, 3), bytes.subarray(3)], { headers: { 'content-length': String(bytes.length), 'content-type': 'application/json' } });
      }
      throw new Error('unexpected fake URL');
    };
    const adapter = createGitHubPublisherAdapter(remoteEnvironment, fetchFake);

    await expect(adapter.downloadReleaseAsset(tag, 'gift-panel-publisher-policy.json')).resolves.toEqual(bytes);
    expect(requests[1]?.headers.get('authorization')).toBe(`Bearer ${remoteEnvironment.GH_TOKEN}`);
    expect(requests[1]?.redirect).toBe('manual');
    if (redirect) {
      expect(requests[2]?.headers.has('authorization')).toBe(false);
      expect(requests[2]?.headers.has('cookie')).toBe(false);
      expect(requests[2]?.credentials).toBe('omit');
      expect(requests[2]?.redirect).toBe('manual');
    }
  });

  it.each([
    'http://release-assets.githubusercontent.com/file?secret=query',
    'https://evil.example.test/file?secret=query',
    'https://release-assets.githubusercontent.com:444/file?secret=query',
  ])('rejects an unreviewed GitHub asset redirect without echoing it: %s', async (location) => {
    const tag = 'publisher-policy-epoch-00000001';
    const bytes = Buffer.from('{}');
    const release = releaseWithAsset(tag, 'gift-panel-publisher-policy.json', bytes);
    const adapter = createGitHubPublisherAdapter(remoteEnvironment, async (input) => {
      if (String(input).includes('/releases/tags/')) return new Response(JSON.stringify(release), { status: 200 });
      return new Response(null, { status: 302, headers: { location } });
    });

    let message = '';
    try {
      await adapter.downloadReleaseAsset(tag, 'gift-panel-publisher-policy.json');
    } catch (error) {
      message = error instanceof Error ? error.message : String(error);
    }
    expect(message).toBe('publisher policy publication failed');
    expect(message).not.toContain('secret=query');
    expect(message).not.toContain(location);
  });

  it('rejects a second GitHub asset redirect and strips authorization from the only allowed hop', async () => {
    const tag = 'publisher-policy-epoch-00000001';
    const bytes = Buffer.from('{}');
    const release = releaseWithAsset(tag, 'gift-panel-publisher-policy.json', bytes);
    const headers: Headers[] = [];
    const adapter = createGitHubPublisherAdapter(remoteEnvironment, async (input, init) => {
      headers.push(new Headers(init?.headers));
      if (String(input).includes('/releases/tags/')) return new Response(JSON.stringify(release), { status: 200 });
      return new Response(null, { status: 302, headers: { location: 'https://release-assets.githubusercontent.com/another-hop' } });
    });

    await expect(adapter.downloadReleaseAsset(tag, 'gift-panel-publisher-policy.json'))
      .rejects.toThrow('publisher policy publication failed');
    expect(headers).toHaveLength(3);
    expect(headers[2]?.has('authorization')).toBe(false);
  });

  it('collapses a GitHub transport exception without leaking its signed query or content', async () => {
    const secret = 'recognizable-signed-query-and-policy-content';
    const adapter = createGitHubPublisherAdapter(remoteEnvironment, async () => { throw new Error(secret); });
    let message = '';
    try {
      await adapter.downloadReleaseAsset('publisher-policy-epoch-00000001', 'gift-panel-publisher-policy.json');
    } catch (error) {
      message = error instanceof Error ? error.message : String(error);
    }
    expect(message).toBe('publisher policy publication failed');
    expect(message).not.toContain(secret);
  });

  it('rejects conflicting GitHub asset metadata with a nonnumeric asset ID', async () => {
    const tag = 'publisher-policy-epoch-00000001';
    const bytes = Buffer.from('{}');
    const release = releaseWithAsset(tag, 'gift-panel-publisher-policy.json', bytes);
    release.assets[0]!.id = '1' as unknown as number;
    const adapter = createGitHubPublisherAdapter(remoteEnvironment, async (input) => {
      if (String(input).includes('/releases/tags/')) return new Response(JSON.stringify(release), { status: 200 });
      return streamingResponse([bytes], { headers: { 'content-length': String(bytes.length) } });
    });

    await expect(adapter.downloadReleaseAsset(tag, 'gift-panel-publisher-policy.json'))
      .rejects.toThrow('publisher policy publication failed');
  });

  it('rejects an unreviewed GitHub upload URL before forwarding authorization', async () => {
    const tag = 'publisher-policy-epoch-00000001';
    const policy = Buffer.from('{}');
    const audit = Buffer.from('{"audit":true}');
    const release = {
      id: 99, tag_name: tag, draft: true, prerelease: false,
      upload_url: 'https://evil.example.test/steal{?name,label}', assets: [],
    };
    let evilRequests = 0;
    const adapter = createGitHubPublisherAdapter(remoteEnvironment, async (input, init) => {
      const url = String(input);
      if (url.startsWith('https://evil.example.test/')) { evilRequests += 1; return new Response('{}', { status: 201 }); }
      if (url.includes('/git/ref/tags/')) return new Response(JSON.stringify({ ref: `refs/tags/${tag}`, object: { type: 'commit', sha: remoteEnvironment.GITHUB_SHA } }), { status: 200 });
      if (url.includes('/releases/tags/')) return new Response(JSON.stringify(release), { status: 200 });
      if (url.endsWith('/releases/99') && init?.method === 'PATCH') return new Response('{}', { status: 200 });
      return new Response('{}', { status: 200 });
    });

    await expect(adapter.publishImmutableRelease({
      tag, title: tag, assets: [
        { name: 'gift-panel-publisher-policy.json', bytes: policy, sha256: sha256(policy) },
        { name: 'gift-panel-publisher-policy.audit.json', bytes: audit, sha256: sha256(audit) },
      ],
    })).rejects.toThrow('publisher policy publication failed');
    expect(evilRequests).toBe(0);
  });

  it.each([409, 412])('accepts COS create-only HTTP %d only after exact metadata and byte readback', async (status) => {
    const key = 'trust/publisher/epochs/00000001.json';
    const bytes = Buffer.from('{"policy":"public"}');
    const digest = sha256(bytes);
    const requests: string[] = [];
    const adapter = createCOSPublisherAdapter(remoteEnvironment, async (input, init) => {
      requests.push(`${init?.method}:${String(input)}`);
      if (init?.method === 'PUT') return new Response(null, { status });
      return streamingResponse([bytes], { headers: {
        'content-length': String(bytes.length),
        'content-type': 'application/json',
        'etag': '"cos-version"',
        'x-cos-meta-sha256': digest,
      } });
    }, () => new Date('2029-01-02T03:04:05Z'));

    await expect(adapter.putImmutable(key, bytes, digest)).resolves.toBeUndefined();
    expect(requests.map((request) => request.split(':', 1)[0])).toEqual(['PUT', 'GET']);
  });

  it('rejects a COS immutable conflict whose existing bytes or metadata differ', async () => {
    const key = 'trust/publisher/epochs/00000001.json';
    const bytes = Buffer.from('{"policy":"public"}');
    const digest = sha256(bytes);
    const adapter = createCOSPublisherAdapter(remoteEnvironment, async (_input, init) => {
      if (init?.method === 'PUT') return new Response(null, { status: 409 });
      return streamingResponse([Buffer.from('{"policy":"other"}')], { headers: {
        'content-type': 'application/json',
        'etag': '"cos-version"',
        'x-cos-meta-sha256': digest,
      } });
    }, () => new Date('2029-01-02T03:04:05Z'));

    await expect(adapter.putImmutable(key, bytes, digest)).rejects.toThrow('publisher policy publication failed');
  });

  it.each([
    { name: 'declared oversize', length: String((256 << 10) + 1), chunks: [Buffer.from('{}')], pulls: 0 },
    { name: 'missing length oversize', length: undefined, chunks: [Buffer.alloc(256 << 10), Buffer.from('x')], pulls: 2 },
    { name: 'lying short length', length: '1', chunks: [Buffer.alloc(256 << 10), Buffer.from('x')], pulls: 2 },
  ])('bounds a hostile GitHub policy stream with $name', async ({ length, chunks }) => {
    const tag = 'publisher-policy-epoch-00000001';
    const release = releaseWithAsset(tag, 'gift-panel-publisher-policy.json', Buffer.from('{}'));
    let pulls = 0;
    let bodyAccesses = 0;
    const adapter = createGitHubPublisherAdapter(remoteEnvironment, async (input) => {
      if (String(input).includes('/releases/tags/')) return new Response(JSON.stringify(release), { status: 200 });
      if (length && Number(length) > (256 << 10)) {
        return {
          status: 200,
          headers: new Headers({ 'content-length': length }),
          get body() { bodyAccesses += 1; throw new Error('body must not be opened'); },
        } as unknown as Response;
      }
      return streamingResponse(chunks, { headers: length === undefined ? {} : { 'content-length': length }, onPull: () => { pulls += 1; } });
    });

    await expect(adapter.downloadReleaseAsset(tag, 'gift-panel-publisher-policy.json'))
      .rejects.toThrow('publisher policy publication failed');
    if (length && Number(length) > (256 << 10)) expect(bodyAccesses).toBe(0);
    else expect(pulls).toBeGreaterThan(0);
  });

  it.each([
    { name: 'existing published Release', initialDraft: false, initialPrerelease: false },
    { name: 'existing draft/prerelease Release', initialDraft: true, initialPrerelease: true },
  ])('revalidates, patches, and proves latest isolation for an $name', async ({ initialDraft, initialPrerelease }) => {
    const tag = 'publisher-policy-epoch-00000001';
    const policy = Buffer.from('{"policy":"public"}');
    const audit = Buffer.from('{"audit":"public"}');
    const assets = [
      { name: 'gift-panel-publisher-policy.json', bytes: policy, sha256: sha256(policy) },
      { name: 'gift-panel-publisher-policy.audit.json', bytes: audit, sha256: sha256(audit) },
    ];
    const release = {
      id: 99, tag_name: tag, draft: initialDraft, prerelease: initialPrerelease,
      upload_url: 'https://uploads.github.com/repos/owner/repository/releases/99/assets{?name,label}',
      assets: assets.map((asset, index) => ({
        id: index + 1, name: asset.name,
        url: `https://api.github.com/repos/owner/repository/releases/assets/${index + 1}`,
        size: asset.bytes.length, content_type: 'application/json', state: 'uploaded',
        digest: `sha256:${asset.sha256}`,
      })),
    };
    const patchBodies: Array<Record<string, unknown>> = [];
    let latestReads = 0;
    const adapter = createGitHubPublisherAdapter(remoteEnvironment, async (input, init) => {
      const url = String(input);
      if (url.includes('/git/ref/tags/')) return new Response(JSON.stringify({ ref: `refs/tags/${tag}`, object: { type: 'commit', sha: remoteEnvironment.GITHUB_SHA } }), { status: 200 });
      if (url.endsWith('/releases/latest')) {
        latestReads += 1;
        return new Response(JSON.stringify({ tag_name: 'v0.4.12', id: 7 }), { status: 200 });
      }
      if (url.includes('/releases/assets/')) {
        const index = url.endsWith('/1') ? 0 : 1;
        const bytes = assets[index]!.bytes;
        return streamingResponse([bytes], { headers: { 'content-length': String(bytes.length) } });
      }
      if (url.endsWith('/releases/99') && init?.method === 'PATCH') {
        patchBodies.push(JSON.parse(String(init.body)) as Record<string, unknown>);
        release.draft = false;
        release.prerelease = false;
        return new Response(JSON.stringify(release), { status: 200 });
      }
      if (url.includes('/releases/tags/')) return new Response(JSON.stringify(release), { status: 200 });
      throw new Error('unexpected GitHub fake request');
    });

    await expect(adapter.publishImmutableRelease({ tag, title: tag, assets })).resolves.toBeUndefined();
    expect(patchBodies).toEqual([{ draft: false, prerelease: false, make_latest: 'false' }]);
    expect(latestReads).toBe(1);
  });

  it('creates a draft with string make_latest false, uploads closure, patches final state, and verifies latest', async () => {
    const tag = 'publisher-policy-epoch-00000001';
    const policy = Buffer.from('{"policy":"public"}');
    const audit = Buffer.from('{"audit":"public"}');
    const assets = [
      { name: 'gift-panel-publisher-policy.json', bytes: policy, sha256: sha256(policy) },
      { name: 'gift-panel-publisher-policy.audit.json', bytes: audit, sha256: sha256(audit) },
    ];
    const releaseAssets: Record<string, unknown>[] = [];
    const createBodies: Array<Record<string, unknown>> = [];
    const patchBodies: Array<Record<string, unknown>> = [];
    let tagExists = false;
    let releaseExists = false;
    let releaseDraft = true;
    const release = () => ({
      id: 99, tag_name: tag, draft: releaseDraft, prerelease: false,
      upload_url: 'https://uploads.github.com/repos/owner/repository/releases/99/assets{?name,label}',
      assets: releaseAssets,
    });
    const adapter = createGitHubPublisherAdapter(remoteEnvironment, async (input, init) => {
      const url = String(input);
      if (url.includes('/git/ref/tags/')) return tagExists
        ? new Response(JSON.stringify({ ref: `refs/tags/${tag}`, object: { type: 'commit', sha: remoteEnvironment.GITHUB_SHA } }), { status: 200 })
        : new Response(null, { status: 404 });
      if (url.endsWith('/git/refs') && init?.method === 'POST') { tagExists = true; return new Response('{}', { status: 201 }); }
      if (url.includes('/releases/tags/')) return releaseExists
        ? new Response(JSON.stringify(release()), { status: 200 })
        : new Response(null, { status: 404 });
      if (url.endsWith('/releases') && init?.method === 'POST') {
        createBodies.push(JSON.parse(String(init.body)) as Record<string, unknown>);
        releaseExists = true;
        return new Response(JSON.stringify(release()), { status: 201 });
      }
      if (url.startsWith('https://uploads.github.com/')) {
        const name = new URL(url).searchParams.get('name')!;
        const asset = assets.find((candidate) => candidate.name === name)!;
        releaseAssets.push({
          id: releaseAssets.length + 1, name, size: asset.bytes.length,
          content_type: 'application/json', state: 'uploaded', digest: `sha256:${asset.sha256}`,
          url: `https://api.github.com/repos/owner/repository/releases/assets/${releaseAssets.length + 1}`,
        });
        return new Response('{}', { status: 201 });
      }
      if (url.includes('/releases/assets/')) {
        const index = url.endsWith('/1') ? 0 : 1;
        const bytes = assets[index]!.bytes;
        return streamingResponse([bytes], { headers: { 'content-length': String(bytes.length) } });
      }
      if (url.endsWith('/releases/99') && init?.method === 'PATCH') {
        patchBodies.push(JSON.parse(String(init.body)) as Record<string, unknown>);
        releaseDraft = false;
        return new Response('{}', { status: 200 });
      }
      if (url.endsWith('/releases/latest')) return new Response(JSON.stringify({ tag_name: 'v0.4.12', id: 7 }), { status: 200 });
      throw new Error(`unexpected GitHub fake request ${url}`);
    });

    await expect(adapter.publishImmutableRelease({ tag, title: tag, assets })).resolves.toBeUndefined();
    expect(createBodies).toEqual([expect.objectContaining({ draft: true, prerelease: false, make_latest: 'false' })]);
    expect(patchBodies).toEqual([{ draft: false, prerelease: false, make_latest: 'false' }]);
  });

  it('rejects an otherwise matching policy Release when GitHub reports it as repository latest', async () => {
    const tag = 'publisher-policy-epoch-00000001';
    const policy = Buffer.from('{"policy":"public"}');
    const audit = Buffer.from('{"audit":"public"}');
    const assets = [
      { name: 'gift-panel-publisher-policy.json', bytes: policy, sha256: sha256(policy) },
      { name: 'gift-panel-publisher-policy.audit.json', bytes: audit, sha256: sha256(audit) },
    ];
    const release = {
      id: 99, tag_name: tag, draft: false, prerelease: false,
      upload_url: 'https://uploads.github.com/repos/owner/repository/releases/99/assets{?name,label}',
      assets: assets.map((asset, index) => ({
        id: index + 1, name: asset.name, size: asset.bytes.length,
        content_type: 'application/json', state: 'uploaded', digest: `sha256:${asset.sha256}`,
        url: `https://api.github.com/repos/owner/repository/releases/assets/${index + 1}`,
      })),
    };
    const adapter = createGitHubPublisherAdapter(remoteEnvironment, async (input, init) => {
      const url = String(input);
      if (url.includes('/git/ref/tags/')) return new Response(JSON.stringify({ ref: `refs/tags/${tag}`, object: { type: 'commit', sha: remoteEnvironment.GITHUB_SHA } }), { status: 200 });
      if (url.endsWith('/releases/latest')) return new Response(JSON.stringify({ tag_name: tag, id: 99 }), { status: 200 });
      if (url.includes('/releases/assets/')) {
        const bytes = url.endsWith('/1') ? policy : audit;
        return streamingResponse([bytes], { headers: { 'content-length': String(bytes.length) } });
      }
      if (url.endsWith('/releases/99') && init?.method === 'PATCH') return new Response('{}', { status: 200 });
      if (url.includes('/releases/tags/')) return new Response(JSON.stringify(release), { status: 200 });
      throw new Error('unexpected GitHub fake request');
    });

    await expect(adapter.publishImmutableRelease({ tag, title: tag, assets }))
      .rejects.toThrow('publisher policy publication failed');
  });

  it.each([409, 412])('marks COS pointer HTTP %d as a compare-and-swap conflict', async (status) => {
    const adapter = createCOSPublisherAdapter(remoteEnvironment, async () => new Response(null, { status }), () => new Date('2029-01-02T03:04:05Z'));
    let code = '';
    try {
      await adapter.compareAndSwapPointer('trust/publisher/latest.json', Buffer.from('{}'), '"old-etag"', sha256(Buffer.from('{}')));
    } catch (error) {
      code = String((error as { code?: string }).code ?? '');
    }
    expect(code).toBe('publisher-pointer-cas-conflict');
  });

  it.each([409, 422])('sends the exact GitHub blob SHA and marks HTTP %d as a CAS conflict', async (status) => {
    const requests: Array<Record<string, unknown>> = [];
    const adapter = createGitHubPublisherAdapter(remoteEnvironment, async (_input, init) => {
      requests.push(JSON.parse(String(init?.body)) as Record<string, unknown>);
      return new Response('{}', { status });
    });
    let code = '';
    try {
      await adapter.compareAndSwapPointer({
        ref: 'refs/heads/publisher-trust',
        path: 'gift-panel-publisher-policy.json',
        bytes: Buffer.from('{}'),
        expectedVersion: 'b'.repeat(40),
      });
    } catch (error) {
      code = String((error as { code?: string }).code ?? '');
    }
    expect(requests).toEqual([expect.objectContaining({ sha: 'b'.repeat(40), branch: 'publisher-trust' })]);
    expect(code).toBe('publisher-pointer-cas-conflict');
  });

  it('rejects a malformed GitHub blob SHA before a pointer request', async () => {
    let requests = 0;
    const adapter = createGitHubPublisherAdapter(remoteEnvironment, async () => {
      requests += 1;
      return new Response('{}', { status: 200 });
    });

    await expect(adapter.compareAndSwapPointer({
      ref: 'refs/heads/publisher-trust',
      path: 'gift-panel-publisher-policy.json',
      bytes: Buffer.from('{}'),
      expectedVersion: 'not-a-blob-sha',
    })).rejects.toThrow('publisher policy publication failed');
    expect(requests).toBe(0);
  });

  it('rejects a GitHub pointer whose decoded policy exceeds 256 KiB', async () => {
    const oversized = Buffer.alloc((256 << 10) + 1);
    const document = JSON.stringify({ encoding: 'base64', content: oversized.toString('base64'), sha: 'd'.repeat(40) });
    const adapter = createGitHubPublisherAdapter(remoteEnvironment, async () => streamingResponse([Buffer.from(document)]));

    await expect(adapter.readPointer('refs/heads/publisher-trust', 'gift-panel-publisher-policy.json'))
      .rejects.toThrow('publisher policy publication failed');
  });

  it('enforces the 64 KiB audit cap even when GitHub omits Content-Length', async () => {
    const tag = 'publisher-policy-epoch-00000001';
    const declared = Buffer.from('{}');
    const release = releaseWithAsset(tag, 'gift-panel-publisher-policy.audit.json', declared);
    const adapter = createGitHubPublisherAdapter(remoteEnvironment, async (input) => {
      if (String(input).includes('/releases/tags/')) return new Response(JSON.stringify(release), { status: 200 });
      return streamingResponse([Buffer.alloc(64 << 10), Buffer.from('x')]);
    });

    await expect(adapter.downloadReleaseAsset(tag, 'gift-panel-publisher-policy.audit.json'))
      .rejects.toThrow('publisher policy publication failed');
  });

  it('enforces the COS policy cap with a lying short Content-Length', async () => {
    const adapter = createCOSPublisherAdapter(remoteEnvironment, async () => streamingResponse(
      [Buffer.alloc(256 << 10), Buffer.from('x')],
      { headers: { 'content-length': '1' } },
    ), () => new Date('2029-01-02T03:04:05Z'));

    await expect(adapter.read('trust/publisher/latest.json')).rejects.toThrow('publisher policy publication failed');
  });

  it('resumes GitHub publication after a prior COS success only when COS 409 readback is exact', async () => {
    const envelope = await verifiedEnvelope();
    const policy = Buffer.from(envelope.policy.bytesBase64, 'base64');
    const audit = Buffer.from(envelope.audit.bytesBase64, 'base64');
    const tag = 'publisher-policy-epoch-00000001';
    const assets = [
      { name: 'gift-panel-publisher-policy.json', bytes: policy, sha256: sha256(policy) },
      { name: 'gift-panel-publisher-policy.audit.json', bytes: audit, sha256: sha256(audit) },
    ];
    const release = {
      id: 99, tag_name: tag, draft: false, prerelease: false,
      upload_url: 'https://uploads.github.com/repos/owner/repository/releases/99/assets{?name,label}',
      assets: assets.map((asset, index) => ({
        id: index + 1, name: asset.name, size: asset.bytes.length,
        content_type: 'application/json', state: 'uploaded', digest: `sha256:${asset.sha256}`,
        url: `https://api.github.com/repos/owner/repository/releases/assets/${index + 1}`,
      })),
    };
    const calls: string[] = [];
    const fetchFake = async (input: string | URL | Request, init?: RequestInit) => {
      const url = String(input);
      calls.push(`${init?.method ?? 'GET'}:${url}`);
      if (url.includes('.cos.')) {
        if (init?.method === 'PUT') return new Response(null, { status: 409 });
        return streamingResponse([policy], { headers: {
          'content-length': String(policy.length), 'content-type': 'application/json',
          'etag': '"cos-version"', 'x-cos-meta-sha256': sha256(policy),
        } });
      }
      if (url.includes('/git/ref/tags/')) return new Response(JSON.stringify({ ref: `refs/tags/${tag}`, object: { type: 'commit', sha: remoteEnvironment.GITHUB_SHA } }), { status: 200 });
      if (url.endsWith('/releases/latest')) return new Response(JSON.stringify({ tag_name: 'v0.4.12', id: 7 }), { status: 200 });
      if (url.includes('/releases/assets/')) {
        const bytes = url.endsWith('/1') ? policy : audit;
        return streamingResponse([bytes], { headers: { 'content-length': String(bytes.length) } });
      }
      if (url.endsWith('/releases/99') && init?.method === 'PATCH') return new Response('{}', { status: 200 });
      if (url.includes('/releases/tags/')) return new Response(JSON.stringify(release), { status: 200 });
      throw new Error('unexpected combined fake request');
    };
    const state = await fakeAdapters(envelope);
    state.adapters.cos = createCOSPublisherAdapter(remoteEnvironment, fetchFake, () => new Date('2029-01-02T03:04:05Z'));
    state.adapters.github = createGitHubPublisherAdapter(remoteEnvironment, fetchFake);

    await expect(publishTrustPolicy(await testOptions({ mode: 'publish-immutable' }), state.adapters))
      .resolves.toMatchObject({ epoch: 1 });
    expect(calls.some((call) => call.startsWith('PUT:https://publisher-policy-1250000000.cos.'))).toBe(true);
    expect(calls.some((call) => call.endsWith('/releases/99'))).toBe(true);
  });

  it.each([
    { name: 'COS-first partial', cosCandidate: true },
    { name: 'GitHub-first partial', cosCandidate: false },
  ])('completes only the stale pointer through real adapters after a $name', async ({ cosCandidate }) => {
    const fixture = await epochTwoFixture();
    const policy = fixture.candidate;
    const audit = Buffer.from(fixture.envelope.audit.bytesBase64, 'base64');
    const targets = publisherTargets(2);
    let cosPointer = cosCandidate ? fixture.candidate : fixture.previous;
    let githubPointer = cosCandidate ? fixture.previous : fixture.candidate;
    const puts: string[] = [];
    const release = {
      id: 99, tag_name: targets.githubReleaseTag, draft: false, prerelease: false,
      upload_url: 'https://uploads.github.com/repos/owner/repository/releases/99/assets{?name,label}',
      assets: [
        { id: 1, name: targets.githubPolicyAsset, size: policy.length, content_type: 'application/json', state: 'uploaded', digest: `sha256:${sha256(policy)}`, url: 'https://api.github.com/repos/owner/repository/releases/assets/1' },
        { id: 2, name: targets.githubAuditAsset, size: audit.length, content_type: 'application/json', state: 'uploaded', digest: `sha256:${sha256(audit)}`, url: 'https://api.github.com/repos/owner/repository/releases/assets/2' },
      ],
    };
    const fetchFake = async (input: string | URL | Request, init?: RequestInit) => {
      const url = new URL(String(input));
      if (url.hostname.includes('.cos.')) {
        const isPointer = url.pathname.endsWith('/trust/publisher/latest.json');
        if (init?.method === 'PUT') {
          puts.push('cos');
          cosPointer = policy;
          return new Response(null, { status: 200 });
        }
        const bytes = isPointer ? cosPointer : policy;
        return streamingResponse([bytes], { headers: {
          'content-length': String(bytes.length), 'content-type': 'application/json',
          'etag': `"${sha256(bytes)}"`, 'x-cos-meta-sha256': sha256(bytes),
        } });
      }
      if (url.pathname.includes('/releases/tags/')) return new Response(JSON.stringify(release), { status: 200 });
      if (url.pathname.includes('/releases/assets/')) {
        const bytes = url.pathname.endsWith('/1') ? policy : audit;
        return streamingResponse([bytes], { headers: { 'content-length': String(bytes.length) } });
      }
      if (url.pathname.endsWith('/contents/gift-panel-publisher-policy.json')) {
        if (init?.method === 'PUT') {
          const body = JSON.parse(String(init.body)) as { sha?: string };
          expect(body.sha).toBe(gitVersion(githubPointer));
          puts.push('github');
          githubPointer = policy;
          return new Response('{}', { status: 200 });
        }
        return new Response(JSON.stringify({ encoding: 'base64', content: githubPointer.toString('base64'), sha: gitVersion(githubPointer) }), { status: 200 });
      }
      throw new Error(`unexpected partial fake request ${url.pathname}`);
    };
    fixture.adapters.cos = createCOSPublisherAdapter(remoteEnvironment, fetchFake, () => new Date('2029-01-02T03:04:05Z'));
    fixture.adapters.github = createGitHubPublisherAdapter(remoteEnvironment, fetchFake);

    await expect(publishTrustPolicy({ ...fixture.options, mode: 'advance-discovery', advanceDiscovery: true }, fixture.adapters))
      .resolves.toMatchObject({ epoch: 2 });
    expect(puts).toEqual(cosCandidate ? ['github'] : ['cos']);
    expect(cosPointer).toEqual(policy);
    expect(githubPointer).toEqual(policy);
  });
});

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

  it('rejects epoch-one pointer advancement when a source is neither absent nor the exact candidate', async () => {
    const { adapters, pointerWrites } = await fakeAdapters();
    await publishTrustPolicy(await testOptions({ mode: 'publish-immutable' }), adapters);
    const readCOS = adapters.cos.read;
    adapters.cos.read = async (key) => key === publisherTargets(1).cosPointerKey
      ? { bytes: Buffer.from('{}'), version: 'unexpected-existing-pointer' }
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

describe('publisher discovery partial completion', () => {
  it.each([
    { name: 'COS already candidate', candidateSide: 'cos' as const },
    { name: 'GitHub already candidate', candidateSide: 'github' as const },
  ])('updates only the stale authenticated previous side when $name', async ({ candidateSide }) => {
    const fixture = await epochTwoFixture();
    await publishTrustPolicy({ ...fixture.options, mode: 'publish-immutable' }, fixture.adapters);
    const targets = publisherTargets(2);
    fixture.cosObjects.set(targets.cosPointerKey, candidateSide === 'cos' ? fixture.candidate : fixture.previous);
    fixture.githubAssets.set(`${targets.githubPointerRef}/${targets.githubPointerPath}`, candidateSide === 'github' ? fixture.candidate : fixture.previous);

    await expect(publishTrustPolicy({ ...fixture.options, mode: 'advance-discovery', advanceDiscovery: true }, fixture.adapters))
      .resolves.toMatchObject({ epoch: 2, advanceDiscovery: true });
    expect(fixture.pointerWrites).toEqual(candidateSide === 'cos'
      ? [`github:${targets.githubPointerRef}:${targets.githubPointerPath}`]
      : [`cos:${targets.cosPointerKey}`]);
    expect(fixture.cosObjects.get(targets.cosPointerKey)).toEqual(fixture.candidate);
    expect(fixture.githubAssets.get(`${targets.githubPointerRef}/${targets.githubPointerPath}`)).toEqual(fixture.candidate);
  });

  it('treats two exact candidate pointers as an idempotent completed advancement', async () => {
    const fixture = await epochTwoFixture();
    await publishTrustPolicy({ ...fixture.options, mode: 'publish-immutable' }, fixture.adapters);
    const targets = publisherTargets(2);
    fixture.cosObjects.set(targets.cosPointerKey, fixture.candidate);
    fixture.githubAssets.set(`${targets.githubPointerRef}/${targets.githubPointerPath}`, fixture.candidate);

    await expect(publishTrustPolicy({ ...fixture.options, mode: 'advance-discovery', advanceDiscovery: true }, fixture.adapters))
      .resolves.toMatchObject({ epoch: 2 });
    expect(fixture.pointerWrites).toEqual([]);
  });

  it('reconfirms an already-candidate pointer and rejects a concurrent regression before success', async () => {
    const fixture = await epochTwoFixture();
    await publishTrustPolicy({ ...fixture.options, mode: 'publish-immutable' }, fixture.adapters);
    const targets = publisherTargets(2);
    fixture.cosObjects.set(targets.cosPointerKey, fixture.candidate);
    fixture.githubAssets.set(`${targets.githubPointerRef}/${targets.githubPointerPath}`, fixture.candidate);
    const readCOS = fixture.adapters.cos.read;
    let pointerReads = 0;
    fixture.adapters.cos.read = async (key) => {
      const value = await readCOS(key);
      if (key === targets.cosPointerKey) {
        pointerReads += 1;
        if (pointerReads > 1) return {
          bytes: fixture.previous, version: gitVersion(fixture.previous),
          sha256: sha256(fixture.previous), contentType: 'application/json',
        };
      }
      return value;
    };

    await expect(publishTrustPolicy({ ...fixture.options, mode: 'advance-discovery', advanceDiscovery: true }, fixture.adapters))
      .rejects.toThrow('publisher policy publication failed');
    expect(fixture.pointerWrites).toEqual([]);
  });

  it('rereads and reclassifies a COS CAS conflict that another writer completed, then advances GitHub once', async () => {
    const fixture = await epochTwoFixture();
    await publishTrustPolicy({ ...fixture.options, mode: 'publish-immutable' }, fixture.adapters);
    const targets = publisherTargets(2);
    fixture.cosObjects.set(targets.cosPointerKey, fixture.previous);
    fixture.githubAssets.set(`${targets.githubPointerRef}/${targets.githubPointerPath}`, fixture.previous);
    let cosCASCalls = 0;
    fixture.adapters.cos.compareAndSwapPointer = async () => {
      cosCASCalls += 1;
      fixture.cosObjects.set(targets.cosPointerKey, fixture.candidate);
      throw Object.assign(new Error('publisher policy publication failed'), { code: 'publisher-pointer-cas-conflict' });
    };

    await expect(publishTrustPolicy({ ...fixture.options, mode: 'advance-discovery', advanceDiscovery: true }, fixture.adapters))
      .resolves.toMatchObject({ epoch: 2 });
    expect(cosCASCalls).toBe(1);
    expect(fixture.pointerWrites).toEqual([`github:${targets.githubPointerRef}:${targets.githubPointerPath}`]);
  });

  it('fails after one CAS conflict reread when the pointer is still previous and never blind-retries', async () => {
    const fixture = await epochTwoFixture();
    await publishTrustPolicy({ ...fixture.options, mode: 'publish-immutable' }, fixture.adapters);
    const targets = publisherTargets(2);
    fixture.cosObjects.set(targets.cosPointerKey, fixture.previous);
    fixture.githubAssets.set(`${targets.githubPointerRef}/${targets.githubPointerPath}`, fixture.previous);
    let cosCASCalls = 0;
    fixture.adapters.cos.compareAndSwapPointer = async () => {
      cosCASCalls += 1;
      throw Object.assign(new Error('publisher policy publication failed'), { code: 'publisher-pointer-cas-conflict' });
    };

    await expect(publishTrustPolicy({ ...fixture.options, mode: 'advance-discovery', advanceDiscovery: true }, fixture.adapters))
      .rejects.toThrow('publisher policy publication failed');
    expect(cosCASCalls).toBe(1);
    expect(fixture.pointerWrites).toEqual([]);
  });

  it.each([
    { name: 'wrong epoch', value: async (fixture: Awaited<ReturnType<typeof epochTwoFixture>>) => makeSignedPolicy(fixture.privateKey, 3, '2030-01-01T00:00:00Z') },
    { name: 'wrong root', value: async (_fixture: Awaited<ReturnType<typeof epochTwoFixture>>) => {
      const other = generateKeyPairSync('ec', { namedCurve: 'P-256' });
      return makeSignedPolicy(other.privateKey, 1, '2030-01-01T00:00:00Z');
    } },
    { name: 'malformed content', value: async () => Buffer.from('{"signed":') },
  ])('rejects an invalid pointer with $name before mutating either source', async ({ value }) => {
    const fixture = await epochTwoFixture();
    await publishTrustPolicy({ ...fixture.options, mode: 'publish-immutable' }, fixture.adapters);
    const targets = publisherTargets(2);
    fixture.cosObjects.set(targets.cosPointerKey, await value(fixture));
    fixture.githubAssets.set(`${targets.githubPointerRef}/${targets.githubPointerPath}`, fixture.previous);

    await expect(publishTrustPolicy({ ...fixture.options, mode: 'advance-discovery', advanceDiscovery: true }, fixture.adapters))
      .rejects.toThrow('publisher policy publication failed');
    expect(fixture.pointerWrites).toEqual([]);
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

  it.each(['OIDC', 'STS'])('bounds an oversized %s JSON response before accepting credentials', async (oversizedStage) => {
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
    let calls = 0;
    const oversized = Buffer.from(JSON.stringify({
      value: 'github-oidc-token',
      padding: 'x'.repeat(64 << 10),
      Response: {
        Credentials: { TmpSecretId: 'temporary-id', TmpSecretKey: 'temporary-key', Token: 'temporary-token' },
      },
    }));
    await expect(exchangeTencentSession(environment, {
      fetch: async () => {
        calls += 1;
        if ((oversizedStage === 'OIDC' && calls === 1) || (oversizedStage === 'STS' && calls === 2)) {
          return streamingResponse([oversized]);
        }
        if (calls === 1) return new Response(JSON.stringify({ value: 'github-oidc-token' }), { status: 200 });
        return new Response(JSON.stringify({ Response: { Credentials: {
          TmpSecretId: 'temporary-id', TmpSecretKey: 'temporary-key', Token: 'temporary-token',
        } } }), { status: 200 });
      },
      appendFile: async () => undefined,
      mask: () => undefined,
    })).rejects.toThrow('publisher session exchange failed');
  });
});
