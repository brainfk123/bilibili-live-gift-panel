import { createHash } from 'node:crypto';
import { describe, expect, it } from 'vitest';
import { buildPackageManifest } from '../scripts/package-ffmpeg.mjs';

const descriptor = Buffer.from('schema=1\nfixture=true\n');
const descriptorHash = createHash('sha256').update(descriptor).digest('hex');
const binary = Buffer.from('signed-binary');
const archive = Buffer.from('archive');
const gate = Buffer.from('gate\n');

describe('FFmpeg package manifest', () => {
  it('emits the exact signed schema-1 component contract', () => {
    const manifest = buildPackageManifest({
      identity: { descriptor, descriptorSha256: descriptorHash, fingerprint: descriptorHash },
      binary,
      archive,
      componentGate: gate,
      authenticode: true,
      signerSubject: 'C=CN;O=NaisNet Technology Co., Ltd.;SERIALNUMBER=91210103MA7CJ3C094',
      sourceReleaseCommit: '1'.repeat(40),
    });

    expect(Object.keys(manifest)).toEqual([
      'schema', 'component_fingerprint', 'descriptor', 'descriptor_sha256',
      'version', 'sha256', 'archive_sha256', 'component_gate',
      'component_gate_sha256', 'size', 'authenticode', 'signer_subject',
      'source_release_commit',
    ]);
    expect(manifest).toMatchObject({
      schema: 1,
      component_fingerprint: descriptorHash,
      descriptor: descriptor.toString('utf8'),
      descriptor_sha256: descriptorHash,
      version: '9.0',
      sha256: createHash('sha256').update(binary).digest('hex'),
      archive_sha256: createHash('sha256').update(archive).digest('hex'),
      component_gate_sha256: createHash('sha256').update(gate).digest('hex'),
      authenticode: true,
      signer_subject: 'C=CN;O=NaisNet Technology Co., Ltd.;SERIALNUMBER=91210103MA7CJ3C094',
      source_release_commit: '1'.repeat(40),
    });
  });

  it('uses one explicit state for unsigned development payloads', () => {
    const manifest = buildPackageManifest({
      identity: { descriptor, descriptorSha256: descriptorHash, fingerprint: descriptorHash },
      binary,
      archive,
      componentGate: gate,
      authenticode: false,
      signerSubject: '',
      sourceReleaseCommit: '0'.repeat(40),
    });
    expect(manifest).toMatchObject({
      authenticode: false,
      signer_subject: '',
      source_release_commit: '0'.repeat(40),
    });
  });

  it('rejects a formatted display Subject in signed component metadata', () => {
    expect(() => buildPackageManifest({ identity:{descriptor,descriptorSha256:descriptorHash,fingerprint:descriptorHash},binary,archive,componentGate:gate,authenticode:true,signerSubject:'CN=Display Subject',sourceReleaseCommit:'1'.repeat(40) })).toThrow(/structured signer identity/);
  });

  it('rejects inconsistent signed and development metadata', () => {
    const base = {
      identity: { descriptor, descriptorSha256: descriptorHash, fingerprint: descriptorHash },
      binary,
      archive,
      componentGate: gate,
    };
    expect(() => buildPackageManifest({ ...base, authenticode: true, signerSubject: '', sourceReleaseCommit: '1'.repeat(40) })).toThrow(/signer/);
    expect(() => buildPackageManifest({ ...base, authenticode: false, signerSubject: 'CN=Release Test', sourceReleaseCommit: '0'.repeat(40) })).toThrow(/development/);
    expect(() => buildPackageManifest({ ...base, authenticode: true, signerSubject: 'CN=Release Test', sourceReleaseCommit: 'bad' })).toThrow(/commit/);
  });
});
