import { createHash } from 'node:crypto';
import { mkdir, mkdtemp, rm, writeFile } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { afterEach, describe, expect, it } from 'vitest';
import { planStableDraft, runStableReleaseTransaction, type StableReleaseAsset, type StableReleaseGitHub } from '../scripts/stable-release-transaction.mjs';

const roots: string[] = [];
afterEach(async () => Promise.all(roots.splice(0).map((root) => rm(root, { recursive: true, force: true }))));
const digest = (bytes: Uint8Array) => createHash('sha256').update(bytes).digest('hex');
const bytesByName = new Map([
  ['gift-panel-windows-x64.exe', Buffer.from('signed executable')],
  ['gift-panel-update.json', Buffer.from('{"schemaVersion":1}')],
]);
const requiredAssets: StableReleaseAsset[] = [...bytesByName].map(([name, bytes]) => ({ name, size: bytes.length, sha256: digest(bytes) }));

function asset(name: string, id = 100) {
  const expected = requiredAssets.find((candidate) => candidate.name === name)!;
  return { id, name, size: expected.size, digest: `sha256:${expected.sha256}` };
}

function draft(assets: ReturnType<typeof asset>[] = []) {
  return {
    id: 77, tag_name: 'v0.4.13', target_commitish: 'a'.repeat(40), name: 'v0.4.13',
    draft: true, prerelease: false, published_at: null as string | null, assets,
  };
}

const plannerInput = (releases: unknown[]) => ({ releases, tag: 'v0.4.13', targetCommit: 'a'.repeat(40), title: 'v0.4.13', requiredAssets });

describe('stable release draft planner', () => {
  it('creates when no exact tag exists', () => {
    expect(planStableDraft(plannerInput([]))).toEqual({ action: 'create', missingAssets: requiredAssets.map((item) => item.name) });
  });

  it.each([
    ['empty', draft(), requiredAssets.map((item) => item.name)],
    ['partial', draft([asset(requiredAssets[0]!.name)]), [requiredAssets[1]!.name]],
    ['complete', draft(requiredAssets.map((item, index) => asset(item.name, 100 + index))), []],
  ])('resumes one %s exact draft and returns only missing assets', (_name, release, missingAssets) => {
    expect(planStableDraft(plannerInput([release]))).toEqual({ action: 'resume', releaseId: 77, missingAssets });
  });

  it.each([
    ['conflicting title', { ...draft(), name: 'wrong' }],
    ['conflicting target', { ...draft(), target_commitish: 'b'.repeat(40) }],
    ['prerelease', { ...draft(), prerelease: true }],
    ['published state', { ...draft(), draft: false, published_at: '2026-09-03T00:00:00Z' }],
    ['extra asset', draft([{ id: 900, name: 'extra.txt', size: 1, digest: `sha256:${'0'.repeat(64)}` }])],
    ['wrong asset size', draft([{ ...asset(requiredAssets[0]!.name), size: 999 }])],
    ['wrong asset digest', draft([{ ...asset(requiredAssets[0]!.name), digest: `sha256:${'0'.repeat(64)}` }])],
  ])('rejects %s', (_name, release) => {
    expect(() => planStableDraft(plannerInput([release]))).toThrow('stable release transaction is invalid');
  });

  it('rejects duplicate exact tag matches', () => {
    expect(() => planStableDraft(plannerInput([draft(), { ...draft(), id: 78 }]))).toThrow('stable release transaction is invalid');
  });
});

describe('stable release transaction', () => {
  it('resumes by numeric ID, uploads only missing assets, publishes Latest, and rehashes downloads', async () => {
    const root = await mkdtemp(join(tmpdir(), 'stable-release-transaction-'));
    roots.push(root);
    const assetDirectory = join(root, 'assets');
    await mkdir(assetDirectory);
    for (const [name, bytes] of bytesByName) await writeFile(join(assetDirectory, name), bytes);

    let release = draft([asset(requiredAssets[0]!.name)]);
    const stored = new Map<string, Buffer>([[requiredAssets[0]!.name, bytesByName.get(requiredAssets[0]!.name)!]]);
    const operations: string[] = [];
    const github: StableReleaseGitHub = {
      listReleases: async () => { operations.push('list'); return [structuredClone(release)]; },
      createDraft: async () => { throw new Error('unexpected create'); },
      getReleaseById: async (id) => { if (id !== 77) throw new Error('wrong id'); operations.push(`get-id:${id}`); return structuredClone(release); },
      uploadAssetById: async (id, name, bytes) => {
        if (id !== 77 || name !== requiredAssets[1]!.name) throw new Error('unexpected upload');
        operations.push(`upload:${id}:${name}`);
        stored.set(name, Buffer.from(bytes));
        release.assets.push(asset(name, 101));
      },
      publishById: async (id) => { if (id !== 77) throw new Error('wrong id'); operations.push(`publish:${id}`); release = { ...release, draft: false, published_at: '2026-09-03T00:00:00Z' }; return structuredClone(release); },
      getReleaseByTag: async (tag) => { if (release.draft || tag !== 'v0.4.13') throw new Error('tag lookup before publish'); operations.push(`get-tag:${tag}`); return structuredClone(release); },
      getLatest: async () => { operations.push('latest'); return structuredClone(release); },
      downloadAsset: async (id, name, maximumBytes) => {
        if (id !== 77 || maximumBytes !== requiredAssets.find((item) => item.name === name)?.size) throw new Error('unexpected download');
        operations.push(`download:${id}:${name}`);
        return Buffer.from(stored.get(name)!);
      },
    };

    const result = await runStableReleaseTransaction({ github, repository: 'brainfk123/bilibili-live-gift-panel', tag: 'v0.4.13', targetCommit: 'a'.repeat(40), title: 'v0.4.13', assetDirectory, requiredAssets });

    expect(result).toEqual({ schemaVersion: 1, releaseId: 77, tag: 'v0.4.13', uploadedAssets: [requiredAssets[1]!.name], verifiedAssets: requiredAssets.map((item) => item.name) });
    expect(operations).toEqual(['list', 'get-id:77', `upload:77:${requiredAssets[1]!.name}`, 'get-id:77', 'publish:77', 'get-id:77', 'get-tag:v0.4.13', 'latest', ...requiredAssets.map((item) => `download:77:${item.name}`)]);
  });
});
