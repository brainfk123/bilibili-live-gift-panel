import { mkdtemp, readFile, rm, writeFile } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { afterEach, describe, expect, it } from 'vitest';
import { buildPublisherChangeRequest, writePublisherChangeRequest, type PublisherChangeRequestInput } from '../scripts/publisher-change-request.mjs';

const roots: string[] = [];
afterEach(async () => {
  await Promise.all(roots.splice(0).map((root) => rm(root, { recursive: true, force: true })));
});

function validInput(): PublisherChangeRequestInput {
  return {
    tag: 'v0.4.13',
    artifactSha256: 'a'.repeat(64),
    certificateDerSha256: 'b'.repeat(64),
    identity: { country: 'CN', organization: 'FutureCo Technology Co., Ltd.', organizationId: '91110000EXAMPLE01' },
    currentPolicyEpoch: 3,
    runId: '12345678901234567890',
    runAttempt: 1,
  };
}

describe('publisher change request', () => {
  it('builds deterministic bounded bytes in the exact reviewed key order', () => {
    const input = validInput();
    const first = buildPublisherChangeRequest(input);
    const second = buildPublisherChangeRequest({ ...input, identity: { ...input.identity } });
    const expected = `{"schemaVersion":1,"tag":"v0.4.13","artifactSha256":"${'a'.repeat(64)}","certificateDerSha256":"${'b'.repeat(64)}","identity":{"country":"CN","organization":"FutureCo Technology Co., Ltd.","organizationId":"91110000EXAMPLE01"},"currentPolicyEpoch":3,"runId":"12345678901234567890","runAttempt":1}\n`;
    expect(first).toEqual(Buffer.from(expected));
    expect(second).toEqual(first);
    expect(first.length).toBeLessThan(4096);
  });

  it.each([
    ['unknown property', { token: 'EVSIGN_SECRET' }],
    ['noncanonical tag', { tag: 'v0.04.13' }],
    ['path-like tag', { tag: 'v0.4.13/../../token' }],
    ['uppercase artifact hash', { artifactSha256: 'A'.repeat(64) }],
    ['short certificate hash', { certificateDerSha256: 'b'.repeat(63) }],
    ['lowercase country', { identity: { ...validInput().identity, country: 'cn' } }],
    ['newline organization', { identity: { ...validInput().identity, organization: 'FutureCo\nTOKEN' } }],
    ['untrimmed organization', { identity: { ...validInput().identity, organization: ' FutureCo' } }],
    ['lowercase organization ID', { identity: { ...validInput().identity, organizationId: '91110000example01' } }],
    ['zero policy epoch', { currentPolicyEpoch: 0 }],
    ['path-like run ID', { runId: 'C:\\secret\\token' }],
    ['numeric run ID', { runId: 123 }],
    ['zero run attempt', { runAttempt: 0 }],
  ])('rejects %s without serializing attacker-controlled content', (_name, override) => {
    const input = { ...validInput(), ...override } as PublisherChangeRequestInput;
    expect(() => buildPublisherChangeRequest(input)).toThrow('publisher change request is invalid');
  });

  it('creates the output once and never overwrites an existing request', async () => {
    const root = await mkdtemp(join(tmpdir(), 'publisher-change-request-'));
    roots.push(root);
    const path = join(root, 'request.json');
    const expected = buildPublisherChangeRequest(validInput());

    await writePublisherChangeRequest(path, validInput());
    expect(await readFile(path)).toEqual(expected);
    await writeFile(path, 'reviewed-existing-request');
    await expect(writePublisherChangeRequest(path, validInput())).rejects.toThrow('publisher change request could not be created');
    expect(await readFile(path, 'utf8')).toBe('reviewed-existing-request');
  });
});
