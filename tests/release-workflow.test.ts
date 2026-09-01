import { spawnSync } from 'node:child_process';
import { createHash } from 'node:crypto';
import { existsSync, mkdtempSync, mkdirSync, readFileSync, rmSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { describe, expect, it } from 'vitest';
import { isScalar, parseDocument } from 'yaml';

interface ReleaseStep {
  env?: Record<string, string>;
  id?: string;
  if?: string;
  name?: string;
  run?: string;
  uses?: string;
  with?: Record<string, unknown>;
  'working-directory'?: string;
}

interface WorkflowJob {
  environment?: string;
  env?: Record<string, string>;
  if?: string;
  needs?: string | string[];
  permissions?: Record<string, string>;
  steps?: ReleaseStep[];
}

interface PublisherRotationWorkflow {
  on?: Record<string, { inputs?: Record<string, Record<string, unknown>> }>;
  permissions?: Record<string, string>;
  jobs?: Record<string, WorkflowJob>;
}

interface BridgeReleaseWorkflow {
  on?: Record<string, { inputs?: Record<string, Record<string, unknown>> }>;
  permissions?: Record<string, string>;
  concurrency?: { group?: string; 'cancel-in-progress'?: boolean };
  jobs?: Record<string, WorkflowJob>;
}

const auditedSetupMSYS2Commit = '66cd2cce69caa17b53920067426061ca1de3a884';

function releaseWorkflow() {
  const source = readFileSync(new URL('../.github/workflows/release.yml', import.meta.url), 'utf8');
  const document = parseDocument(
    source,
  );
  expect(document.errors).toEqual([]);

  const workflow = document.toJS() as {
    concurrency?: { group?: string; 'cancel-in-progress'?: boolean };
    jobs?: {
      release?: {
        env?: Record<string, string>;
        environment?: string;
        steps?: ReleaseStep[];
      };
    };
  };
  const release = workflow.jobs?.release;
  const steps = release?.steps;
  expect(Array.isArray(steps)).toBe(true);
  return { concurrency: workflow.concurrency, document, release, source, steps: steps ?? [], workflow };
}

function publisherRotationWorkflow(): PublisherRotationWorkflow {
  const source = readFileSync(new URL('../.github/workflows/publisher-rotation.yml', import.meta.url), 'utf8');
  const document = parseDocument(source);
  expect(document.errors).toEqual([]);
  return document.toJS() as PublisherRotationWorkflow;
}

function bridgeReleaseWorkflow(): BridgeReleaseWorkflow & { source: string } {
  const source = readFileSync(new URL('../.github/workflows/bridge-release.yml', import.meta.url), 'utf8');
  const document = parseDocument(source);
  expect(document.errors).toEqual([]);
  return { ...(document.toJS() as BridgeReleaseWorkflow), source };
}

function jobSteps(job: WorkflowJob | undefined): ReleaseStep[] {
  expect(Array.isArray(job?.steps)).toBe(true);
  return job?.steps ?? [];
}

function semanticCommands(job: WorkflowJob | undefined): string[] {
  return jobSteps(job).flatMap((step) => {
    const commands: string[] = [];
    if (step.uses) commands.push(`uses:${step.uses.split('@', 1)[0]}`);
    for (const line of step.run?.split(/\r?\n/) ?? []) {
      const trimmed = line.trim();
      if (trimmed && !trimmed.startsWith('#')) commands.push(`run:${trimmed}`);
    }
    return commands;
  });
}

function stepIndex(steps: ReleaseStep[], name: string): number {
  const index = steps.findIndex((step) => step.name === name);
  expect(index, `missing workflow step ${name}`).toBeGreaterThanOrEqual(0);
  return index;
}

function publishedManifestFixture(): Record<string, unknown> {
  const asset = Buffer.from('signed-executable-fixture');
  return {
    tag_name: 'v1.2.3',
    draft: false,
    prerelease: false,
    assets: [{
      name: 'gift-panel-windows-x64.exe',
      browser_download_url: 'https://github.com/example/repository/releases/download/v1.2.3/gift-panel-windows-x64.exe',
      size: asset.length,
      digest: `sha256:${createHash('sha256').update(asset).digest('hex')}`,
    }],
  };
}

function runPublishedReleaseValidation(
  manifest: unknown,
  manifestJSON = JSON.stringify(manifest),
  standalone?: { checksum?: string; componentHash?: string },
) {
  const temporaryRoot = mkdtempSync(join(tmpdir(), 'gift-panel-release-validation-'));
  try {
    const dist = join(temporaryRoot, 'dist');
    mkdirSync(dist);
    const asset = Buffer.from('signed-executable-fixture');
    const digest = createHash('sha256').update(asset).digest('hex');
    writeFileSync(join(dist, 'gift-panel-windows-x64.exe'), asset);
    writeFileSync(join(dist, 'gift-panel-windows-x64.exe.sha256'), `${digest}  gift-panel-windows-x64.exe`);
    writeFileSync(join(dist, 'gift-panel-update.json'), manifestJSON);
    writeFileSync(join(dist, 'gift-panel-changelog.json'), '{"schemaVersion":1,"releases":[{}]}');
    if (standalone) {
      const ffmpeg = Buffer.from('signed-ffmpeg-fixture');
      const ffmpegDigest = createHash('sha256').update(ffmpeg).digest('hex');
      writeFileSync(join(dist, 'ffmpeg-windows-x64.exe'), ffmpeg);
      writeFileSync(
        join(dist, 'ffmpeg-windows-x64.exe.sha256'),
        standalone.checksum ?? `${ffmpegDigest}  ffmpeg-windows-x64.exe`,
      );
      writeFileSync(join(dist, 'standalone-component-manifest.json'), JSON.stringify({
        version: '9.0', authenticode: true, size: ffmpeg.length,
        sha256: standalone.componentHash ?? ffmpegDigest,
      }));
    }

    const { steps } = releaseWorkflow();
    const validation = steps[stepIndex(steps, 'Validate published release assets')]?.run;
    expect(validation).toBeTypeOf('string');
    const script = String.raw`
$ErrorActionPreference = 'Stop'
function Get-AuthenticodeSignature {
  param([string]$LiteralPath)
  [pscustomobject]@{
    Status = [System.Management.Automation.SignatureStatus]::Valid
    SignerCertificate = [pscustomobject]@{ Subject = 'CN=Release Test' }
  }
}
function go { $global:LASTEXITCODE = 0 }
function Mock-Inspector { $global:LASTEXITCODE = 0 }

${validation}
`;
    return spawnSync('pwsh', ['-NoLogo', '-NoProfile', '-NonInteractive', '-Command', '-'], {
      cwd: temporaryRoot,
      encoding: 'utf8',
      env: {
        ...process.env,
        EVSIGN_EXPECTED_SUBJECT: 'CN=Release Test',
        AUTHENTICODE_INSPECTOR_PATH: 'Mock-Inspector',
        GITHUB_REPOSITORY: 'example/repository',
        RELEASE_TAG: 'v1.2.3',
        RELEASE_EXISTS: 'true',
        RELEASE_STANDALONE_FFMPEG: standalone ? 'true' : 'false',
      },
      input: script,
      timeout: 30_000,
    });
  } finally {
    rmSync(temporaryRoot, { recursive: true, force: true });
  }
}

function runBridgeEvidencePreparation(expectedFFmpegHash?: string) {
  const temporaryRoot = mkdtempSync(join(tmpdir(), 'gift-panel-bridge-evidence-'));
  try {
    const dist = join(temporaryRoot, 'dist');
    mkdirSync(dist);
    const readinessRoot = join(temporaryRoot, 'bridge-readiness');
    mkdirSync(readinessRoot);
    const executable = Buffer.from('rushrush-signed-executable');
    const ffmpeg = Buffer.from('naisnet-signed-ffmpeg');
    const componentManifest = Buffer.from('{"version":"9.0"}');
    const ffmpegHash = createHash('sha256').update(ffmpeg).digest('hex');
    const manifestHash = createHash('sha256').update(componentManifest).digest('hex');
    writeFileSync(join(dist, 'gift-panel-windows-x64.exe'), executable);
    writeFileSync(join(dist, 'ffmpeg-windows-x64.exe'), ffmpeg);
    writeFileSync(join(dist, 'ffmpeg-component-manifest.json'), componentManifest);
    writeFileSync(join(dist, 'bridge-artifact-inspection.json'), JSON.stringify({
      version: '0.4.11', tag: 'v0.4.11', commit: 'a'.repeat(40), peContentSha256: 'd'.repeat(64),
      rootSpkiSha256: 'b'.repeat(64), policySha256: 'c'.repeat(64), policyEpoch: 1,
      outerIdentity: { country: 'CN', organization: 'RushRush Network Technology Ltd', organizationId: '91450900MADM3GLG5P' },
      ffmpegVersion: '9.0', ffmpegSha256: ffmpegHash, ffmpegSize: ffmpeg.length,
      ffmpegIdentity: { country: 'CN', organization: 'NaisNet Technology Co., Ltd.', organizationId: '91210103MA7CJ3C094' },
    }));
    writeFileSync(join(readinessRoot, 'readiness.json'), JSON.stringify({
      schemaVersion: 1, stableReleaseId: 412, stablePublishedAt: '2026-08-01T00:00:00Z', stableArtifactSha256:'1'.repeat(64), observationEndedAt: '2026-08-08T00:00:00Z', observationEvidenceSha256: 'e'.repeat(64),
      policyReleaseId: 501, policyEpoch: 1, policySha256: 'c'.repeat(64), rootSpkiSha256: 'b'.repeat(64), kmsKeyId: 'kms-production-key', kmsRequestId: 'kms-request-1', trustAttestationSha256: 'f'.repeat(64),
    }));
    writeFileSync(join(temporaryRoot, 'gift-panel-changelog.json'), '{"schemaVersion":1,"releases":[{"version":"0.4.11"}]}');
    const steps = jobSteps(bridgeReleaseWorkflow().jobs?.['bridge-release']);
    const evidence = steps[stepIndex(steps, 'Prepare public bridge evidence')]?.run;
    expect(evidence).toBeTypeOf('string');
    const result = spawnSync('pwsh', ['-NoLogo', '-NoProfile', '-NonInteractive', '-File', '-'], {
      cwd: temporaryRoot,
      encoding: 'utf8',
      env: {
        ...process.env,
        BRIDGE_TAG: 'v0.4.11',
        BRIDGE_COMMIT: 'a'.repeat(40),
        GITHUB_REPOSITORY: 'brainfk123/bilibili-live-gift-panel',
        BRIDGE_TRUST_ROOT_SPKI_SHA256: 'b'.repeat(64),
        BRIDGE_BOOTSTRAP_POLICY_SHA256: 'c'.repeat(64),
        BRIDGE_BOOTSTRAP_POLICY_EPOCH: '1',
        BRIDGE_FFMPEG_COMPONENT_MANIFEST_SHA256: manifestHash,
        BRIDGE_FFMPEG_SHA256: expectedFFmpegHash ?? ffmpegHash,
        BRIDGE_FFMPEG_SIZE: String(ffmpeg.length),
        BRIDGE_READINESS_ROOT: readinessRoot,
      },
      input: `$ErrorActionPreference = 'Stop'\n${evidence}\n`,
      timeout: 30_000,
    });
    return {
      result,
      evidence: existsSync(join(dist, 'bridge-release-evidence.json'))
        ? JSON.parse(readFileSync(join(dist, 'bridge-release-evidence.json'), 'utf8'))
        : undefined,
      checksums: existsSync(join(dist, 'SHA256SUMS.txt'))
        ? readFileSync(join(dist, 'SHA256SUMS.txt'), 'utf8')
        : '',
    };
  } finally {
    rmSync(temporaryRoot, { recursive: true, force: true });
  }
}

describe('release workflow supply-chain contract', () => {
  it('rejects the bridge tag in a no-environment job before stable protected work for push and dispatch', () => {
    const { workflow } = releaseWorkflow();
    const jobs = workflow.jobs as Record<string, WorkflowJob & { outputs?:Record<string,string> }>;
    expect(Object.keys(jobs)).toEqual(['validate-tag','release']);
    const gate=jobs['validate-tag'];const stable=jobs.release;
    expect(gate.environment).toBeUndefined();expect(stable.environment).toBe('release');expect(stable.needs).toBe('validate-tag');
    const steps=jobSteps(gate);expect(steps).toHaveLength(1);expect(steps[0]?.env?.RELEASE_TAG).toBe('${{ inputs.tag || github.ref_name }}');expect(steps[0]?.run).toContain("\"$RELEASE_TAG\" == 'v0.4.11'");expect(steps[0]?.run).toContain('bridge workflow alone owns v0.4.11');
    expect(stable.env?.RELEASE_TAG).toBe('${{ needs.validate-tag.outputs.tag }}');
    expect(semanticCommands(gate).some((command)=>/checkout|build|sign|release create|--latest/.test(command))).toBe(false);
  });
  it('builds security tooling from a protected reviewed commit before any historical target checkout', () => {
    const { steps } = releaseWorkflow();
    const toolingCheckout=stepIndex(steps,'Check out reviewed release tooling');const buildTools=stepIndex(steps,'Build reviewed release security tools');const target=stepIndex(steps,'Check out release tag');const profile=stepIndex(steps,'Resolve EVSign signer profile');const validate=stepIndex(steps,'Validate published release assets');
    expect(steps[toolingCheckout]?.with).toMatchObject({ref:'${{ vars.RELEASE_TOOLING_COMMIT_SHA }}',path:'release-tools','persist-credentials':false});expect(toolingCheckout).toBeLessThan(buildTools);expect(buildTools).toBeLessThan(target);
    expect(steps[buildTools]?.run).toContain('artifact-inspector.exe');expect(steps[buildTools]?.run).toContain('sign-evsign.mjs');expect(steps[profile]?.run).toContain('$env:EVSIGN_SCRIPT_PATH');expect(steps[validate]?.run).toContain('$env:AUTHENTICODE_INSPECTOR_PATH');
  });
  it('pins every external Action to an immutable commit SHA', () => {
    const { steps } = releaseWorkflow();
    const externalActions = steps
      .map((step) => step.uses)
      .filter((uses): uses is string => typeof uses === 'string');

    expect(externalActions.length).toBeGreaterThan(0);
    for (const uses of externalActions) {
      expect(uses).toMatch(/^[^@\s]+@[0-9a-f]{40}$/);
    }
  });

  it('serializes all production releases in one non-canceling global group', () => {
    const { concurrency } = releaseWorkflow();

    expect(concurrency).toEqual({
      group: 'gift-panel-production-release',
      'cancel-in-progress': false,
    });
  });

  it('validates a canonical tag before an exact non-credentialed checkout', () => {
    const { release, source, steps } = releaseWorkflow();
    const validate = stepIndex(steps, 'Validate release tag');
    const checkout = stepIndex(steps, 'Check out release tag');
    const resolveCommit = stepIndex(steps, 'Resolve checked-out release commit');

    expect(release?.environment).toBe('release');
    expect(validate).toBe(0);
    expect(validate).toBeLessThan(checkout);
    expect(steps[validate]?.run).toContain(
      "^v(?:0|[1-9][0-9]*)\\.(?:0|[1-9][0-9]*)\\.(?:0|[1-9][0-9]*)$",
    );
    expect(steps[checkout]?.with).toMatchObject({
      ref: 'refs/tags/${{ env.RELEASE_TAG }}',
      'persist-credentials': false,
    });
    expect(checkout).toBeLessThan(resolveCommit);
    expect(steps[resolveCommit]?.run).toContain('$releaseCommit = git rev-parse HEAD');
    expect(steps[resolveCommit]?.run).toContain('RELEASE_COMMIT=$releaseCommit');
    expect(source).not.toContain('github.sha');
  });

  it('keeps GitHub Release independent from COS publishers and remote triggers', () => {
    const { release, source, steps } = releaseWorkflow();
    const checkoutSteps = steps.filter((step) => step.uses?.startsWith('actions/checkout@'));

    expect(release?.environment).toBe('release');
    expect(checkoutSteps).toHaveLength(2);
    expect(checkoutSteps.map((step) => step.name)).toEqual(['Check out reviewed release tooling','Check out release tag']);
    for (const [name, forbidden] of [
      ['COS release secret ID', /COS_RELEASE_SECRET_ID/i],
      ['COS release secret key', /COS_RELEASE_SECRET_KEY/i],
      ['publisher tool pin', /UPDATE_PUBLISHER_TOOL_SHA/i],
      ['publisher tool checkout', /_update-publisher-tool/i],
      ['Tencent COS', /Tencent\s+COS/i],
      ['TAT', /\bTAT\b/i],
      ['webhook', /\bwebhooks?\b/i],
      ['connectivity script', /test-cos-connectivity/i],
    ]) {
      expect(source, `release workflow must not reference ${name}`).not.toMatch(forbidden);
    }
    expect(source).not.toMatch(/\bgo(?:\.exe)?(?:\s+-C\s+\S+)?\s+run\s+\.\/cmd\/publish\b/);
  });

  it('keeps obsolete direct COS connectivity entry points deleted', () => {
    expect(existsSync(new URL('../.github/workflows/cos-connectivity-test.yml', import.meta.url)))
      .toBe(false);
    expect(existsSync(new URL('../scripts/test-cos-connectivity.mjs', import.meta.url)))
      .toBe(false);
  });

  it('validates release publication timestamps without producing unused publisher metadata', () => {
    const { steps } = releaseWorkflow();

    for (const name of ['Inspect existing GitHub release', 'Create GitHub release']) {
      const run = steps[stepIndex(steps, name)]?.run ?? '';
      expect(run, name).toContain('$publishedAt = [DateTimeOffset]$release.published_at');
      expect(run, name).not.toContain('$publishedAtRFC3339');
      expect(run, name).toContain('publication timestamp is invalid');
    }
  });

  it('race-tests the update module from the release tag checkout itself', () => {
    const { steps } = releaseWorkflow();
    const checkoutRelease = stepIndex(steps, 'Check out release tag');
    const setupGo = stepIndex(steps, 'Set up Go');
    const testUpdateApi = stepIndex(steps, 'Test domestic update tooling');

    expect(checkoutRelease).toBeLessThan(setupGo);
    expect(steps[setupGo]?.with).toMatchObject({
      'go-version-file': 'updateapi/go.mod',
      'cache-dependency-path': 'updateapi/go.sum',
    });
    expect(setupGo).toBeLessThan(testUpdateApi);
    expect(steps[testUpdateApi]?.env?.GOWORK).toBe('off');
    expect(steps[testUpdateApi]?.run)
      .toBe('go -C updateapi test ./... -race -count=1');
  });

  it('uses the audited setup-msys2 v2 commit and rejects mutable refs', () => {
    const { document, steps } = releaseWorkflow();
    const setupSteps = steps.filter((step) => step.uses?.startsWith('msys2/setup-msys2@'));
    expect(setupSteps).toHaveLength(1);
    expect(setupSteps[0]?.uses).toBe(`msys2/setup-msys2@${auditedSetupMSYS2Commit}`);
    expect(setupSteps[0]?.uses).toMatch(/^msys2\/setup-msys2@[0-9a-f]{40}$/);

    const setupIndex = steps.indexOf(setupSteps[0]!);
    const usesNode = document.getIn(['jobs', 'release', 'steps', setupIndex, 'uses'], true);
    expect(isScalar(usesNode)).toBe(true);
    expect(isScalar(usesNode) ? usesNode.comment?.trim() : undefined).toBe('v2');
  });

  it('keeps the exact pinned toolchain and signed-package verification order', () => {
    const { steps } = releaseWorkflow();
    const lock = JSON.parse(readFileSync(
      new URL('../third_party/ffmpeg/toolchain-lock.json', import.meta.url),
      'utf8',
    )) as { packages?: unknown[] };
    expect(lock.packages).toHaveLength(35);

    const buildFrontend = stepIndex(steps, 'Build frontend');
    const setup = stepIndex(steps, 'Set up MSYS2 host environment');
    const prepareAssets = stepIndex(steps, 'Prepare backend UI assets');
    const build = stepIndex(steps, 'Build and verify pinned FFmpeg');
    const signInner = stepIndex(steps, 'Sign and verify inner FFmpeg');
    const packageInner = stepIndex(steps, 'Package and verify signed FFmpeg payload');
    const buildOuter = stepIndex(steps, 'Build release executable');
    const signOuter = stepIndex(steps, 'Prepare and sign release executable');
    const e2e = stepIndex(steps, 'Verify deterministic gift clip exports from signed package chain');

    expect(buildFrontend).toBeLessThan(prepareAssets);
    expect(prepareAssets).toBeLessThan(build);
    expect(steps[prepareAssets]?.run).toBe('npm run prepare:go-assets');
    expect([setup, build, signInner, packageInner, buildOuter, signOuter, e2e])
      .toEqual([...new Set([setup, build, signInner, packageInner, buildOuter, signOuter, e2e])].sort((a, b) => a - b));
    expect(steps[setup]?.id).toBe('msys2');
    expect(steps[build]?.run).toContain('${{ steps.msys2.outputs.msys2-location }}');
    expect(steps[build]?.run).toContain('npm run build:ffmpeg -- -Msys2Root $msys2Root -InstallPinnedToolchain');
    expect(steps[build]?.run).toContain('RELEASE_TOOL_ROOT/scripts/verify-ffmpeg.mjs');
    expect(steps[e2e]?.run).toContain('scripts/gift-clip-test-tools.mjs');
    expect(steps[e2e]?.run).toContain('npm run verify:gift-clip-export');
  });

  it('reuses an immutable signed FFmpeg component before entering the build path', () => {
    const { steps } = releaseWorkflow();
    const identity = stepIndex(steps, 'Resolve FFmpeg component identity');
    const inspect = stepIndex(steps, 'Inspect signed FFmpeg component');
    const downloadHit = stepIndex(steps, 'Download signed FFmpeg component');
    const setup = stepIndex(steps, 'Set up MSYS2 host environment');
    const build = stepIndex(steps, 'Build and verify pinned FFmpeg');
    const sign = stepIndex(steps, 'Sign and verify inner FFmpeg');
    const packageComponent = stepIndex(steps, 'Package signed FFmpeg component');
    const attestComponent = stepIndex(steps, 'Attest signed FFmpeg component');
    const publish = stepIndex(steps, 'Publish signed FFmpeg component');
    const downloadPublished = stepIndex(steps, 'Download published FFmpeg component');
    const install = stepIndex(steps, 'Verify and install signed FFmpeg component');
    const buildOuter = stepIndex(steps, 'Build release executable');

    expect([identity, inspect, downloadHit, setup, build, sign, packageComponent, attestComponent, publish, downloadPublished, install, buildOuter])
      .toEqual([...new Set([identity, inspect, downloadHit, setup, build, sign, packageComponent, attestComponent, publish, downloadPublished, install, buildOuter])].sort((a, b) => a - b));
    for (const index of [setup, build, sign, packageComponent, publish, downloadPublished]) {
      expect(steps[index]?.if).toContain("env.FFMPEG_COMPONENT_EXISTS != 'true'");
    }
    expect(steps[downloadHit]?.if).toContain("env.FFMPEG_COMPONENT_EXISTS == 'true'");
    expect(steps[identity]?.env).toBeUndefined();
    expect(steps[identity]?.run).toContain('$identity.schema -ne 2');
    expect(steps[identity]?.run).toContain('ffmpeg-component-v2-$($identity.fingerprint)');
    expect(steps[packageComponent]?.env).toBeUndefined();
    expect(steps[install]?.run).toContain('RELEASE_TOOL_ROOT/scripts/ffmpeg-component-assets.mjs');
    expect(steps[install]?.run).toContain('install --tool-root');
    expect(steps[install]?.run).toContain('--input $componentDirectory');
    expect(steps[install]?.run).toContain('--tool-root $env:RELEASE_TOOL_ROOT');
    expect(steps[install]?.run).toContain('verify-metadata');
    expect(steps[downloadPublished]?.run).toContain('Invoke-RestMethod');
    expect(steps[downloadPublished]?.run).toContain('ffmpeg-component-release.json');
    expect(steps[attestComponent]?.uses).toBe('actions/attest@1e69f48acb82d1966a394da916b4c1698aa569d6');
    expect(steps[install]?.run).toContain('gh attestation verify');
    expect(steps[install]?.run).toContain('ffmpeg-build-config.txt');
    expect(steps[publish]?.run).not.toContain('--clobber');
    expect(steps[publish]?.run).toContain('--latest=false');
    expect(steps[publish]?.run).toContain('Another publisher created the FFmpeg component');
    expect(steps[publish]?.run).toContain('Invoke-RestMethod');

    for (const step of steps.filter((candidate) => candidate.run?.includes('ffmpeg-component-assets.mjs'))) {
      expect(step.run).toContain('--tool-root $env:RELEASE_TOOL_ROOT');
    }
  });

  it('publishes a separately downloadable signed FFmpeg from v0.4.10 without changing the updater manifest', () => {
    const { steps } = releaseWorkflow();
    const validateTag = stepIndex(steps, 'Validate release tag');
    const installComponent = stepIndex(steps, 'Verify and install signed FFmpeg component');
    const prepareStandalone = stepIndex(steps, 'Prepare standalone FFmpeg release asset');
    const prepareRelease = stepIndex(steps, 'Prepare release assets');
    const createRelease = stepIndex(steps, 'Create GitHub release');
    const redownloadStandalone = stepIndex(steps, 'Redownload published standalone FFmpeg');
    const verifyRepairComponent = stepIndex(steps, 'Verify standalone FFmpeg repair component');
    const validatePublished = stepIndex(steps, 'Validate published release assets');
    const downloadExisting = stepIndex(steps, 'Download existing release assets');

    expect(steps[validateTag]?.run).toContain("[Version]'0.4.10'");
    expect(steps[validateTag]?.run).toContain('RELEASE_STANDALONE_FFMPEG');
    expect(installComponent).toBeLessThan(prepareStandalone);
    expect(prepareStandalone).toBeLessThan(createRelease);
    expect(createRelease).toBeLessThan(redownloadStandalone);
    expect(redownloadStandalone).toBeLessThan(validatePublished);
    expect(steps[prepareStandalone]?.if).toBe("env.RELEASE_EXISTS != 'true' && env.RELEASE_STANDALONE_FFMPEG == 'true'");
    expect(steps[prepareStandalone]?.run).toContain('$componentDirectory/ffmpeg.zip');
    expect(steps[prepareStandalone]?.run).toContain('dist/ffmpeg-windows-x64.exe');
    expect(steps[prepareStandalone]?.run).toContain('dist/ffmpeg-windows-x64.exe.sha256');
    expect(steps[prepareStandalone]?.run).toContain('$env:AUTHENTICODE_INSPECTOR_PATH authenticode');
    expect(steps[prepareStandalone]?.run).toContain('$componentManifest.sha256');
    expect(steps[prepareStandalone]?.run).not.toContain('SignerCertificate.Subject');
    expect(steps[createRelease]?.run).toContain('dist/ffmpeg-windows-x64.exe');
    expect(steps[createRelease]?.run).toContain('dist/ffmpeg-windows-x64.exe.sha256');
    expect(steps[redownloadStandalone]?.if).toBe("env.RELEASE_EXISTS != 'true' && env.RELEASE_STANDALONE_FFMPEG == 'true'");
    expect(steps[redownloadStandalone]?.run).toContain('--pattern ffmpeg-windows-x64.exe');
    expect(steps[downloadExisting]?.run).toContain("$env:RELEASE_STANDALONE_FFMPEG -eq 'true'");
    expect(steps[downloadExisting]?.run).toContain('--pattern ffmpeg-windows-x64.exe');
    expect(steps[verifyRepairComponent]?.if).toBe("env.RELEASE_EXISTS == 'true' && env.RELEASE_STANDALONE_FFMPEG == 'true'");
    expect(steps[verifyRepairComponent]?.run).toContain('verify-metadata');
    expect(steps[verifyRepairComponent]?.run).toContain('gh attestation verify');
    expect(steps[verifyRepairComponent]?.run).toContain('standalone-component-manifest.json');
    expect(steps[validatePublished]?.run).toContain('$env:AUTHENTICODE_INSPECTOR_PATH authenticode --file dist/ffmpeg-windows-x64.exe');
    expect(steps[validatePublished]?.run).toContain('ffmpeg-windows-x64.exe.sha256');
    expect(steps[validatePublished]?.run).toContain('$componentManifest.sha256');
    expect(steps[validatePublished]?.run).toContain("'dist/standalone-component-manifest.json'");
    expect(steps[prepareRelease]?.run).not.toContain('name = "ffmpeg-windows-x64.exe"');
    expect(steps[prepareRelease]?.run).toContain('assets = @(');
    expect(steps[prepareRelease]?.run).toContain('name = "gift-panel-windows-x64.exe"');
  });

  it('resolves the closed stable profile before component identity and keeps bridge variables out', () => {
    const { steps } = releaseWorkflow();
    const resolveSigner = stepIndex(steps, 'Resolve EVSign signer profile');
    const identity = stepIndex(steps, 'Resolve FFmpeg component identity');
    const signInner = stepIndex(steps, 'Sign and verify inner FFmpeg');
    const buildOuter = stepIndex(steps, 'Build release executable');
    const signOuter = stepIndex(steps, 'Prepare and sign release executable');
    const validate = stepIndex(steps, 'Validate published release assets');

    expect(resolveSigner).toBeLessThan(identity);
    expect(steps[resolveSigner]?.env).toEqual({
      EVSIGN_CERTIFICATE: '${{ vars.EVSIGN_CERTIFICATE }}',
      EVSIGN_PUBLISHER_IDENTITY: '${{ vars.EVSIGN_PUBLISHER_IDENTITY }}',
    });
    expect(steps[resolveSigner]?.run).toContain('node $env:EVSIGN_SCRIPT_PATH --resolve-profile stable');
    expect(steps[resolveSigner]?.run).toContain('$profile.schema -ne 2');
    expect(steps[resolveSigner]?.run).toContain("APP_UPDATE_PUBLISHER=NaisNet Technology Co., Ltd.");
    expect(steps[identity]?.run).not.toContain('EVSIGN_EXPECTED_SUBJECT');
    for (const [index, step] of steps.entries()) {
      if ([resolveSigner, signInner, signOuter].includes(index)) continue;
      expect(Object.values(step.env ?? {})).not.toContain('${{ vars.EVSIGN_CERTIFICATE }}');
      expect(Object.values(step.env ?? {})).not.toContain('${{ vars.EVSIGN_PUBLISHER_IDENTITY }}');
    }
    for (const index of [identity, signInner, buildOuter, signOuter, validate]) {
      expect(steps[index]?.env ?? {}).not.toHaveProperty('EVSIGN_CERT');
      expect(steps[index]?.env ?? {}).not.toHaveProperty('EVSIGN_EXPECTED_SUBJECT');
    }
    expect(steps[buildOuter]?.env).not.toHaveProperty('APP_UPDATE_PUBLISHER');
    expect(steps[signInner]?.env).toMatchObject({
      EVSIGN_CERTIFICATE: '${{ vars.EVSIGN_CERTIFICATE }}',
      EVSIGN_PUBLISHER_IDENTITY: '${{ vars.EVSIGN_PUBLISHER_IDENTITY }}',
    });
    expect(steps[signInner]?.run).toContain('$env:EVSIGN_SCRIPT_PATH --profile stable');
    expect(steps[signOuter]?.env).toMatchObject({
      EVSIGN_CERTIFICATE: '${{ vars.EVSIGN_CERTIFICATE }}',
      EVSIGN_PUBLISHER_IDENTITY: '${{ vars.EVSIGN_PUBLISHER_IDENTITY }}',
    });
    expect(steps[signOuter]?.run).toContain('$env:EVSIGN_SCRIPT_PATH --profile stable');
    expect(steps[signOuter]?.run).not.toContain('sign-evsign-cli.mjs');
    for (const step of steps) {
      expect(Object.keys(step.env ?? {}).some((key) => key.startsWith('EVSIGN_BRIDGE_'))).toBe(false);
    }
  });

  it('gates stable publication on update tooling tests and structured NaisNet inspection', () => {
    const { steps } = releaseWorkflow();
    const testUpdateApi = stepIndex(steps, 'Test domestic update tooling');
    const signOuter = stepIndex(steps, 'Prepare and sign release executable');
    const githubRelease = stepIndex(steps, 'Create GitHub release');

    expect(steps[testUpdateApi]?.run)
      .toBe('go -C updateapi test ./... -race -count=1');
    expect(testUpdateApi).toBeLessThan(githubRelease);
    expect(signOuter).toBeLessThan(githubRelease);
    expect(steps[signOuter]?.env).not.toHaveProperty('EVSIGN_EXPECTED_SUBJECT');
    expect(steps[signOuter]?.run).toContain('$env:AUTHENTICODE_INSPECTOR_PATH verify-static');
    expect(steps[signOuter]?.run).toContain('--organization-id 91210103MA7CJ3C094');
    expect(steps[signOuter]?.run).not.toContain('SignerCertificate.Subject');
  });

  it('builds domestic update identity into the signed executable and ends at a validated GitHub Release', () => {
    const { steps } = releaseWorkflow();
    const build = stepIndex(steps, 'Build release executable');
    const sign = stepIndex(steps, 'Prepare and sign release executable');
    const githubRelease = stepIndex(steps, 'Create GitHub release');
    const validate = stepIndex(steps, 'Validate published release assets');

    expect(steps[build]?.env).toMatchObject({
      APP_COMMIT: '${{ env.RELEASE_COMMIT }}',
      APP_UPDATE_API_URL: '${{ vars.UPDATE_API_BASE_URL }}',
    });
    expect(steps[build]?.env).not.toHaveProperty('APP_UPDATE_PUBLISHER');
    expect(steps[build]?.env).not.toHaveProperty('EVSIGN_EXPECTED_SUBJECT');
    expect(build).toBeLessThan(sign);
    expect(steps.some((step) => step.name === 'Prepare pinned EVSign CLI for outer executable')).toBe(false);
    expect(steps[sign]?.run).toContain('node $env:EVSIGN_SCRIPT_PATH --profile stable');
    expect(githubRelease).toBeLessThan(validate);
    expect(validate).toBe(steps.length - 1);
  });

  it('prepares the release test workspace and pinned browser before running tests', () => {
    const { steps } = releaseWorkflow();
    const installDependencies = stepIndex(steps, 'Install dependencies');
    const prepareBrowser = stepIndex(steps, 'Install pinned Playwright Chromium for release E2E');
    const runTests = stepIndex(steps, 'Run tests');

    expect(installDependencies).toBeLessThan(prepareBrowser);
    expect(prepareBrowser).toBeLessThan(runTests);
    expect(steps[prepareBrowser]?.if).toBe("env.RELEASE_EXISTS != 'true'");
    expect(steps[prepareBrowser]?.run).toContain(
      'New-Item -ItemType Directory -Force -Path .cache',
    );
    expect(steps[prepareBrowser]?.run).toContain('npm exec -- playwright install chromium');
    expect(steps[runTests]?.run).toBe(
      'npm test -- --reporter=dot --minWorkers=2 --maxWorkers=2',
    );
  });

  it('reuses complete existing GitHub assets without rebuilding, resigning, or clobbering', () => {
    const { steps } = releaseWorkflow();
    const inspect = stepIndex(steps, 'Inspect existing GitHub release');
    const download = stepIndex(steps, 'Download existing release assets');
    const build = stepIndex(steps, 'Build release executable');
    const sign = stepIndex(steps, 'Prepare and sign release executable');
    const prepare = stepIndex(steps, 'Prepare release assets');
    const create = stepIndex(steps, 'Create GitHub release');
    const validate = stepIndex(steps, 'Validate published release assets');

    expect(inspect).toBeLessThan(download);
    expect(steps[inspect]?.run).toContain('published_at');
    expect(steps[download]?.if).toBe("env.RELEASE_EXISTS == 'true'");
    expect(steps[download]?.run).toContain('--pattern gift-panel-windows-x64.exe');
    expect(steps[download]?.run).toContain('--pattern gift-panel-windows-x64.exe.sha256');
    expect(steps[download]?.run).toContain('--pattern gift-panel-changelog.json');
    expect(steps[download]?.run).toContain('--pattern gift-panel-update.json');
    expect(steps[download]?.run).toContain('Manual recovery required');
    for (const name of [
      'Install dependencies',
      'Run tests',
      'Type check',
      'Build frontend',
      'Prepare backend UI assets',
      'Set up MSYS2 host environment',
      'Build and verify pinned FFmpeg',
      'Sign and verify inner FFmpeg',
      'Package and verify signed FFmpeg payload',
      'Build release executable',
      'Run backend tests',
      'Prepare and sign release executable',
      'Install pinned Playwright Chromium for release E2E',
      'Verify deterministic gift clip exports from signed package chain',
      'Prepare release assets',
      'Attest executable provenance',
    ]) {
      const ffmpegMissOnly = new Set([
        'Set up MSYS2 host environment',
        'Build and verify pinned FFmpeg',
        'Sign and verify inner FFmpeg',
        'Package and verify signed FFmpeg payload',
      ]);
      expect(steps[stepIndex(steps, name)]?.if, `${name} must be skipped for repair`)
        .toBe(ffmpegMissOnly.has(name)
          ? "env.RELEASE_EXISTS != 'true' && env.FFMPEG_COMPONENT_EXISTS != 'true'"
          : "env.RELEASE_EXISTS != 'true'");
    }
    expect(steps[build]?.if).toBe("env.RELEASE_EXISTS != 'true'");
    expect(steps[sign]?.if).toBe("env.RELEASE_EXISTS != 'true'");
    expect(steps[create]?.if).toBe("env.RELEASE_EXISTS != 'true'");
    expect(steps[create]?.run).not.toContain('--clobber');
    expect(steps[create]?.run).toContain(
      'gh release upload $env:RELEASE_TAG dist/gift-panel-windows-x64.exe dist/gift-panel-windows-x64.exe.sha256',
    );
    expect(steps[create]?.run).toContain('dist/gift-panel-update.json');
    expect(steps[create]?.run).toContain('dist/gift-panel-changelog.json');
    expect(steps[prepare]?.run).toContain(
      'https://github.com/$env:GITHUB_REPOSITORY/releases/download/$env:RELEASE_TAG/gift-panel-windows-x64.exe',
    );
    expect(create).toBeLessThan(validate);
    expect(steps[validate]?.run).toContain(
      '$env:AUTHENTICODE_INSPECTOR_PATH authenticode --file dist/gift-panel-windows-x64.exe',
    );
    expect(steps[validate]?.run).not.toContain('SignerCertificate.Subject');
    expect(steps[validate]?.run).toContain('Get-FileHash -Algorithm SHA256 -LiteralPath dist/gift-panel-windows-x64.exe');
    expect(steps[validate]?.run).toContain('gift-panel-windows-x64.exe.sha256');
    expect(steps[validate]?.run).toContain('dist/gift-panel-update.json');
    expect(validate).toBe(steps.length - 1);
  });

  it('accepts the exact typed fallback update manifest contract', () => {
    const result = runPublishedReleaseValidation(publishedManifestFixture());

    expect(result.status, result.stderr).toBe(0);
  });

  it('validates a separately downloadable signed FFmpeg during release repair', () => {
    const result = runPublishedReleaseValidation(publishedManifestFixture(), undefined, {});

    expect(result.status, result.stderr).toBe(0);
  });

  it('rejects a standalone FFmpeg whose checksum does not match', () => {
    const result = runPublishedReleaseValidation(publishedManifestFixture(), undefined, {
      checksum: `${'0'.repeat(64)}  ffmpeg-windows-x64.exe`,
    });

    expect(result.status).not.toBe(0);
    expect(result.stderr).toContain('Published standalone FFmpeg does not match its checksum');
  });

  it('rejects a self-consistent standalone FFmpeg that differs from the fixed component manifest', () => {
    const result = runPublishedReleaseValidation(publishedManifestFixture(), undefined, {
      componentHash: '0'.repeat(64),
    });

    expect(result.status).not.toBe(0);
    expect(result.stderr).toContain('does not match the verified signed component manifest');
  });

  const malformedManifestCases: Array<{
    name: string;
    mutate: (manifest: Record<string, unknown>) => unknown;
    serialize?: (manifest: unknown) => string;
  }> = [
    {
      name: 'an array tag_name',
      mutate: (manifest) => { manifest.tag_name = ['v1.2.3']; },
    },
    {
      name: 'numeric draft false',
      mutate: (manifest) => { manifest.draft = 0; },
    },
    {
      name: 'numeric prerelease false',
      mutate: (manifest) => { manifest.prerelease = 0; },
    },
    {
      name: 'a non-array assets value',
      mutate: (manifest) => { manifest.assets = (manifest.assets as unknown[])[0]; },
    },
    {
      name: 'an array asset name',
      mutate: (manifest) => { (manifest.assets as Record<string, unknown>[])[0]!.name = ['gift-panel-windows-x64.exe']; },
    },
    {
      name: 'an array asset download URL',
      mutate: (manifest) => {
        (manifest.assets as Record<string, unknown>[])[0]!.browser_download_url =
          ['https://github.com/example/repository/releases/download/v1.2.3/gift-panel-windows-x64.exe'];
      },
    },
    {
      name: 'an array asset digest',
      mutate: (manifest) => {
        const asset = (manifest.assets as Record<string, unknown>[])[0]!;
        asset.digest = [asset.digest];
      },
    },
    {
      name: 'a string asset size',
      mutate: (manifest) => {
        const asset = (manifest.assets as Record<string, unknown>[])[0]!;
        asset.size = String(asset.size);
      },
    },
    {
      name: 'a decimal-form asset size',
      mutate: (manifest) => manifest,
      serialize: (manifest) => {
        const serialized = JSON.stringify(manifest);
        const size = ((manifest as Record<string, unknown>).assets as Record<string, unknown>[])[0]!.size;
        return serialized.replace(`"size":${size}`, `"size":${size}.0`);
      },
    },
    {
      name: 'an array root',
      mutate: (manifest) => [manifest],
    },
    {
      name: 'an unknown root property',
      mutate: (manifest) => { manifest.extra = true; },
    },
  ];

  it.each(malformedManifestCases)(
    'rejects fallback manifests with $name during GitHub Release repair',
    ({ mutate, serialize }) => {
      const manifest = publishedManifestFixture();
      const replacement = mutate(manifest);
      const candidate = replacement ?? manifest;
      const result = runPublishedReleaseValidation(candidate, serialize?.(candidate));

      expect(result.status).not.toBe(0);
      expect(result.stderr).toContain('Published fallback update manifest');
    },
  );

  it('checks every gh command immediately so publication failures cannot be masked', () => {
    const { steps } = releaseWorkflow();
    const ghRuns = steps
      .map((step) => step.run)
      .filter((run): run is string => typeof run === 'string' && /\bgh (?:api|release|attestation)\b/.test(run));
    expect(ghRuns.length).toBeGreaterThan(0);

    for (const run of ghRuns) {
      const lines = run.split(/\r?\n/);
      for (let index = 0; index < lines.length; index += 1) {
        if (!/\bgh (?:api|release|attestation)\b/.test(lines[index] ?? '')) continue;
        const guard = lines[index + 1]?.trim() ?? '';
        if (lines[index]?.includes('/git/refs')) {
          expect(guard, `unchecked gh command: ${lines[index]?.trim()}`).toBe('if ($LASTEXITCODE -ne 0) {');
          expect(run).toContain('Another publisher created the FFmpeg component');
          expect(run).toContain('Invoke-RestMethod');
        } else {
          expect(guard, `unchecked gh command: ${lines[index]?.trim()}`)
            .toMatch(/^if \(\$LASTEXITCODE -ne 0\) \{ throw /);
        }
      }
    }
  });
});

describe('publisher rotation workflow contract', () => {
  it('pins every external Action to one immutable commit', () => {
    const workflow = publisherRotationWorkflow();
    const actions = Object.values(workflow.jobs ?? {}).flatMap(jobSteps)
      .map((step) => step.uses)
      .filter((uses): uses is string => typeof uses === 'string');
    expect(actions.length).toBeGreaterThan(0);
    for (const uses of actions) expect(uses).toMatch(/^[^@\s]+@[0-9a-f]{40}$/);
  });

  it('has one manual trigger with explicit epoch transition and pointer choice', () => {
    const workflow = publisherRotationWorkflow();
    expect(Object.keys(workflow.on ?? {})).toEqual(['workflow_dispatch']);
    const inputs = workflow.on?.workflow_dispatch?.inputs;
    expect(inputs).toEqual({
      candidate_epoch: expect.objectContaining({ required: true, type: 'number' }),
      expected_previous_epoch: expect.objectContaining({ required: true, type: 'number' }),
      advance_discovery: expect.objectContaining({ required: true, type: 'boolean', default: false }),
    });
  });

  it('separates validation, KMS signing, immutable publication, and optional discovery advancement', () => {
    const workflow = publisherRotationWorkflow();
    const jobs = workflow.jobs ?? {};
    expect(Object.keys(jobs)).toEqual([
      'validate-candidate',
      'sign-policy',
      'publish-immutable',
      'advance-discovery',
    ]);
    expect(jobs['validate-candidate']?.environment).toBeUndefined();
    for (const name of ['sign-policy', 'publish-immutable', 'advance-discovery']) {
      expect(jobs[name]?.environment, name).toBe('publisher-rotation');
    }
    expect(jobs['sign-policy']?.needs).toBe('validate-candidate');
    expect(jobs['publish-immutable']?.needs).toBe('sign-policy');
    expect(jobs['advance-discovery']?.needs).toBe('publish-immutable');
    expect(jobs['advance-discovery']?.if).toBe('${{ inputs.advance_discovery == true }}');
  });

  it('grants OIDC only to exchange jobs and GitHub write only to dedicated trust publication jobs', () => {
    const workflow = publisherRotationWorkflow();
    const jobs = workflow.jobs ?? {};
    expect(workflow.permissions).toEqual({ contents: 'read' });
    expect(jobs['validate-candidate']?.permissions).toEqual({ contents: 'read' });
    expect(jobs['sign-policy']?.permissions).toEqual({ contents: 'read', 'id-token': 'write' });
    expect(jobs['publish-immutable']?.permissions).toEqual({ contents: 'write', 'id-token': 'write' });
    expect(jobs['advance-discovery']?.permissions).toEqual({ contents: 'write', 'id-token': 'write' });

    const signingSteps = jobSteps(jobs['sign-policy']);
    const sign = signingSteps.find((step) => step.name === 'Sign publisher policy');
    expect(sign?.env).toMatchObject({
      GIFT_PANEL_KMS_PROVIDER_MODE: 'environment-session',
      TENCENTCLOUD_SECRET_ID: '${{ steps.kms-session.outputs.secret-id }}',
      TENCENTCLOUD_SECRET_KEY: '${{ steps.kms-session.outputs.secret-key }}',
      TENCENTCLOUD_SESSION_TOKEN: '${{ steps.kms-session.outputs.session-token }}',
    });
    expect(sign?.env).not.toHaveProperty('GH_TOKEN');
    expect(Object.keys(sign?.env ?? {}).some((name) => name.startsWith('COS_'))).toBe(false);

    for (const jobName of ['publish-immutable', 'advance-discovery']) {
      const publish = jobSteps(jobs[jobName]).find((step) => step.env?.COS_BUCKET !== undefined);
      expect(publish?.env).toMatchObject({
        TENCENTCLOUD_SECRET_ID: '${{ steps.cos-session.outputs.secret-id }}',
        TENCENTCLOUD_SECRET_KEY: '${{ steps.cos-session.outputs.secret-key }}',
        TENCENTCLOUD_SESSION_TOKEN: '${{ steps.cos-session.outputs.session-token }}',
      });
      expect(Object.values(publish?.env ?? {}).some((value) => value.includes('secrets.'))).toBe(false);
    }
  });

  it('runs only the policy CLI and publisher and cannot build or sign an executable', () => {
    const workflow = publisherRotationWorkflow();
    const commands = Object.values(workflow.jobs ?? {}).flatMap(semanticCommands);
    expect(commands.some((command) => command.includes('./cmd/trustpolicy'))).toBe(true);
    expect(commands.some((command) => command.includes('scripts/publish-trust-policy.mjs'))).toBe(true);
    for (const command of commands) {
      expect(command).not.toMatch(/(?:sign-evsign|build-go|gift-panel\.exe|release-stable|legacy-rushrush)/i);
    }
  });

  it('keeps the ordinary release workflow unable to call KMS or the legacy pointer', () => {
    const { release } = releaseWorkflow();
    const commands = semanticCommands(release as WorkflowJob);
    expect(commands.some((command) => /trustpolicy.*\bsign\b/i.test(command))).toBe(false);
    expect(commands.some((command) => /kms|legacy-rushrush/i.test(command))).toBe(false);
    expect(release?.env ?? {}).not.toHaveProperty('GIFT_PANEL_KMS_PROVIDER_MODE');
  });

  it('requires reviewed public-root configuration instead of embedding a production digest', () => {
    const workflow = publisherRotationWorkflow();
    const allSteps = Object.values(workflow.jobs ?? {}).flatMap(jobSteps);
    const verificationSteps = allSteps.filter((step) => step.env?.PUBLISHER_ROTATION_SPKI_SHA256 !== undefined);
    expect(verificationSteps.length).toBeGreaterThan(0);
    for (const step of verificationSteps) {
      expect(step.env?.PUBLISHER_ROTATION_SPKI_SHA256).toBe('${{ vars.PUBLISHER_ROTATION_SPKI_SHA256 }}');
      expect(step.env?.PUBLISHER_ROTATION_SPKI_PATH).toBe('${{ vars.PUBLISHER_ROTATION_SPKI_PATH }}');
    }
  });
});

describe('exact RushRush bridge release workflow contract', () => {
  it('uses reviewed prebuilt security tools and bounded policy downloads',()=>{const steps=jobSteps(bridgeReleaseWorkflow().jobs?.['bridge-release']);const tools=stepIndex(steps,'Build reviewed bridge security tools');const target=stepIndex(steps,'Check out exact bridge tag');const trust=stepIndex(steps,'Fetch immutable production trust binding');expect(tools).toBeLessThan(target);expect(steps[tools]?.run).toContain('bounded-github-asset.mjs');expect(steps[trust]?.run).toContain('BOUNDED_GITHUB_ASSET_SCRIPT_PATH');expect(steps[trust]?.run).not.toContain('gh release download');expect(steps[trust]?.run).toContain('verify-bundle');});

  it('imports the exact immutable Task9 bundle before verification without pre-creating or weakening its DACL',()=>{const steps=jobSteps(bridgeReleaseWorkflow().jobs?.['bridge-release']);const trust=steps[stepIndex(steps,'Fetch immutable production trust binding')]?.run??'';const imported=trust.indexOf(' import-bundle ');const verified=trust.indexOf(' verify-bundle ');expect(imported).toBeGreaterThanOrEqual(0);expect(verified).toBeGreaterThan(imported);expect(trust).toContain('$bundleParent = "$env:BRIDGE_READINESS_ROOT/private-bundle"');expect(trust).toContain('--commit-source "$downloadDirectory/commit.json"');expect(trust).toContain('--policy "$bundleDirectory/policy.json"');expect(trust).toContain('--audit "$bundleDirectory/audit.json"');expect(trust).not.toMatch(/New-Item[^\n]+private-bundle|icacls/);});
  it('is manual, fixed to v0.4.11, isolated, and minimally permissioned', () => {
    const workflow = bridgeReleaseWorkflow();
    expect(Object.keys(workflow.on ?? {})).toEqual(['workflow_dispatch']);
    expect(workflow.on?.workflow_dispatch?.inputs ?? {}).toEqual({});
    expect(workflow.permissions).toEqual({ contents: 'write', 'id-token': 'write', attestations: 'write' });
    expect(workflow.concurrency).toEqual({ group: 'gift-panel-bridge-v0.4.11', 'cancel-in-progress': false });
    expect(Object.keys(workflow.jobs ?? {})).toEqual(['bridge-release']);
    const job = workflow.jobs?.['bridge-release'];
    expect(job?.environment).toBe('bridge-release');
    expect(job?.env?.BRIDGE_TAG).toBe('v0.4.11');
    expect(job?.permissions).toEqual({ contents: 'write', 'id-token': 'write', attestations: 'write' });
  });

  it('checks out only the fixed tag and rejects ref-derived or conflicting publication state', () => {
    const steps = jobSteps(bridgeReleaseWorkflow().jobs?.['bridge-release']);
    const validate = stepIndex(steps, 'Validate fixed bridge release request');
    const checkout = stepIndex(steps, 'Check out exact bridge tag');
    const conflict = stepIndex(steps, 'Reject conflicting GitHub release');
    expect(validate).toBe(0);
    expect(steps[validate]?.run).toContain("$env:BRIDGE_TAG -cne 'v0.4.11'");
    expect(steps[checkout]?.with).toMatchObject({
      ref: 'refs/tags/v0.4.11',
      'persist-credentials': false,
      'fetch-depth': 0,
    });
    expect(steps[checkout]?.with?.ref).not.toContain('github.ref');
    expect(checkout).toBeLessThan(conflict);
    expect(steps[conflict]?.run).toContain('Existing v0.4.11 GitHub Release conflicts with this create-only workflow');
    expect(steps[conflict]?.run).not.toContain('RELEASE_EXISTS=true');
  });

  it('fails closed on reviewed enrollment inputs before build and keeps app trust NaisNet', () => {
    const steps = jobSteps(bridgeReleaseWorkflow().jobs?.['bridge-release']);
    const inputs = steps[stepIndex(steps, 'Validate reviewed bridge inputs')];
    const build = steps[stepIndex(steps, 'Build bridge executable')];
    expect(inputs?.env).toEqual({
      BRIDGE_REVIEWED_COMMIT_SHA: '${{ vars.BRIDGE_REVIEWED_COMMIT_SHA }}',
      BRIDGE_TRUST_ROOT_SPKI_B64: '${{ vars.BRIDGE_TRUST_ROOT_SPKI_B64 }}',
      BRIDGE_TRUST_ROOT_SPKI_SHA256: '${{ vars.BRIDGE_TRUST_ROOT_SPKI_SHA256 }}',
      BRIDGE_BOOTSTRAP_POLICY_B64: '${{ vars.BRIDGE_BOOTSTRAP_POLICY_B64 }}',
      BRIDGE_BOOTSTRAP_POLICY_SHA256: '${{ vars.BRIDGE_BOOTSTRAP_POLICY_SHA256 }}',
      BRIDGE_BOOTSTRAP_POLICY_EPOCH: '${{ vars.BRIDGE_BOOTSTRAP_POLICY_EPOCH }}',
      BRIDGE_FFMPEG_COMPONENT_MANIFEST_SHA256: '${{ vars.BRIDGE_FFMPEG_COMPONENT_MANIFEST_SHA256 }}',
      BRIDGE_UPDATE_API_BASE_URL: '${{ vars.UPDATE_API_BASE_URL }}',
      BRIDGE_OBSERVATION_EVIDENCE_B64: '${{ vars.BRIDGE_OBSERVATION_EVIDENCE_B64 }}',
      BRIDGE_OBSERVATION_EVIDENCE_SHA256: '${{ vars.BRIDGE_OBSERVATION_EVIDENCE_SHA256 }}',
      BRIDGE_PRODUCTION_TRUST_ATTESTATION_B64: '${{ vars.BRIDGE_PRODUCTION_TRUST_ATTESTATION_B64 }}',
      BRIDGE_PRODUCTION_TRUST_ATTESTATION_SHA256: '${{ vars.BRIDGE_PRODUCTION_TRUST_ATTESTATION_SHA256 }}',
    });
    expect(inputs?.run).toContain('reviewed bridge input is missing');
    expect(inputs?.run).not.toContain('AddDays(7)');
    expect(build?.env).toMatchObject({
      APP_VERSION: 'v0.4.11',
      APP_UPDATE_PUBLISHER: 'NaisNet Technology Co., Ltd.',
      APP_UPDATE_TRUST_REQUIRED: '1',
      APP_UPDATE_TRUST_ROOT_SPKI_B64: '${{ vars.BRIDGE_TRUST_ROOT_SPKI_B64 }}',
      APP_UPDATE_TRUST_BOOTSTRAP_POLICY_B64: '${{ vars.BRIDGE_BOOTSTRAP_POLICY_B64 }}',
    });
    expect(build?.env?.APP_UPDATE_PUBLISHER).not.toContain('RushRush');
    expect(steps.some((step) => step.name === 'Client-verify embedded enrollment contract')).toBe(false);
  });

  it('binds the local and remote peeled tag commit to one protected reviewed SHA three times', () => {
    const steps = jobSteps(bridgeReleaseWorkflow().jobs?.['bridge-release']);
    const afterCheckout = stepIndex(steps, 'Bind exact reviewed bridge tag after checkout');
    const beforeDraft = stepIndex(steps, 'Recheck reviewed bridge tag before draft');
    const beforePublish = stepIndex(steps, 'Recheck reviewed bridge tag before publication');
    const create = stepIndex(steps, 'Create immutable-shaped bridge draft');
    const publish = stepIndex(steps, 'Publish bridge as non-latest');
    expect(afterCheckout).toBeLessThan(beforeDraft);
    expect(beforeDraft).toBeLessThan(create);
    expect(create).toBeLessThan(beforePublish);
    expect(beforePublish).toBeLessThan(publish);
    for (const index of [afterCheckout, beforeDraft, beforePublish]) {
      expect(steps[index]?.env?.BRIDGE_REVIEWED_COMMIT_SHA).toBe('${{ vars.BRIDGE_REVIEWED_COMMIT_SHA }}');
      expect(steps[index]?.env?.BRIDGE_REVIEWED_TAG_OBJECT_SHA).toBe('${{ vars.BRIDGE_REVIEWED_TAG_OBJECT_SHA }}');
      expect(steps[index]?.run).toContain('git rev-parse refs/tags/v0.4.11^{commit}');
      expect(steps[index]?.run).toContain('git rev-parse refs/tags/v0.4.11');
      expect(steps[index]?.run).toContain('git ls-remote origin refs/tags/v0.4.11 refs/tags/v0.4.11^{}');
      expect(steps[index]?.run).toContain('$env:BRIDGE_REVIEWED_COMMIT_SHA -cnotmatch');
      expect(steps[index]?.run).toContain('$env:BRIDGE_REVIEWED_TAG_OBJECT_SHA -cnotmatch');
      expect(steps[index]?.run).toContain("$remote['refs/tags/v0.4.11'] -cne $env:BRIDGE_REVIEWED_TAG_OBJECT_SHA");
      expect(steps[index]?.run).toContain('reviewed bridge tag binding failed');
    }
  });

  it('uses actual stable and policy Releases plus reviewed artifacts for readiness before build', () => {
    const steps = jobSteps(bridgeReleaseWorkflow().jobs?.['bridge-release']);
    const stable = stepIndex(steps, 'Fetch immutable v0.4.12 observation binding');
    const trust = stepIndex(steps, 'Fetch immutable production trust binding');
    const readiness = stepIndex(steps, 'Verify reviewed bridge readiness');
    const build = stepIndex(steps, 'Build bridge executable');
    expect(stable).toBeLessThan(readiness);
    expect(trust).toBeLessThan(readiness);
    expect(readiness).toBeLessThan(build);
    expect(steps[stable]?.run).toContain('/releases/tags/v0.4.12');
    expect(steps[stable]?.run).toContain('stable-release.json');
    expect(steps[stable]?.run).toContain('--max-bytes 134217728');
    expect(steps[stable]?.run).toContain('--output "$env:BRIDGE_READINESS_ROOT/stable-artifact.exe"');
    expect(steps[trust]?.run).toContain('publisher-policy-epoch-00000001');
    expect(steps[trust]?.run).toContain('BOUNDED_GITHUB_ASSET_SCRIPT_PATH');
    expect(steps[trust]?.run).toContain('verify-bundle');
    expect(steps[readiness]?.run).toContain('node $env:BRIDGE_READINESS_SCRIPT_PATH verify');
    expect(steps[readiness]?.run).toContain('--observation-evidence');
    expect(steps[readiness]?.run).toContain('--trust-attestation');
    expect(steps[readiness]?.run).toContain('--stable-artifact "$env:BRIDGE_READINESS_ROOT/stable-artifact.exe"');
    expect(steps[readiness]?.run).toContain('--verified-bundle "$env:BRIDGE_READINESS_ROOT/verified-bundle.json"');
    expect(steps[readiness]?.run).toContain('stableIdentity.organization');
    expect(steps[readiness]?.run).toContain('$env:BRIDGE_READINESS_ROOT/readiness.json');
    expect(bridgeReleaseWorkflow().source).not.toContain('BRIDGE_STABLE_PUBLISHED_AT');
    expect(bridgeReleaseWorkflow().source).not.toContain('BRIDGE_STABLE_OBSERVATION_APPROVED_AT');
  });

  it('keeps the verified private readiness closure outside Vite-cleared dist through final inspection', () => {
    const workflow = bridgeReleaseWorkflow();
    const steps = jobSteps(workflow.jobs?.['bridge-release']);
    const tools = steps[stepIndex(steps, 'Build reviewed bridge security tools')];
    const readiness = stepIndex(steps, 'Verify reviewed bridge readiness');
    const buildFrontend = stepIndex(steps, 'Build frontend');
    const finalInspection = stepIndex(steps, 'Inspect final bound bridge artifact');
    expect(readiness).toBeLessThan(buildFrontend);
    expect(buildFrontend).toBeLessThan(finalInspection);
    expect(tools?.run).toContain('BRIDGE_READINESS_ROOT=$env:RUNNER_TEMP/bridge-readiness');
    expect(workflow.source).not.toContain('dist/bridge-readiness');
    for (const name of ['Fetch immutable v0.4.12 observation binding', 'Fetch immutable production trust binding', 'Verify reviewed bridge readiness', 'Inspect final bound bridge artifact']) {
      expect(steps[stepIndex(steps, name)]?.run).toContain('$env:BRIDGE_READINESS_ROOT');
    }
  });

  it('uses the closed bridge signer and structured RushRush outer identity only', () => {
    const steps = jobSteps(bridgeReleaseWorkflow().jobs?.['bridge-release']);
    const resolveProfile = steps[stepIndex(steps, 'Validate bridge signer profile')];
    const sign = steps[stepIndex(steps, 'Sign RushRush outer executable')];
    expect(resolveProfile?.env).toEqual({
      EVSIGN_BRIDGE_CERTIFICATE: '${{ vars.EVSIGN_BRIDGE_CERTIFICATE }}',
      EVSIGN_BRIDGE_PUBLISHER_IDENTITY: '${{ vars.EVSIGN_BRIDGE_PUBLISHER_IDENTITY }}',
    });
    expect(resolveProfile?.run).toContain('$env:EVSIGN_SCRIPT_PATH --resolve-profile bridge');
    expect(sign?.env).toMatchObject({
      EVSIGN_BRIDGE_CERTIFICATE: '${{ vars.EVSIGN_BRIDGE_CERTIFICATE }}',
      EVSIGN_BRIDGE_PUBLISHER_IDENTITY: '${{ vars.EVSIGN_BRIDGE_PUBLISHER_IDENTITY }}',
      EVSIGN_KEY: '${{ secrets.EVSIGN_BRIDGE_KEY }}',
      EVSIGN_PASSWORD: '${{ secrets.EVSIGN_BRIDGE_PASSWORD }}',
    });
    expect(sign?.run).toContain('$env:EVSIGN_SCRIPT_PATH --profile bridge');
    expect(sign?.run).toContain('dist/gift-panel-windows-x64.unsigned.exe dist/gift-panel-windows-x64.exe');
    expect(sign?.run).not.toContain('Get-AuthenticodeSignature');
    for (const step of steps) {
      expect(Object.values(step.env ?? {})).not.toContain('${{ secrets.EVSIGN_KEY }}');
      expect(Object.values(step.env ?? {})).not.toContain('${{ vars.EVSIGN_CERTIFICATE }}');
    }
  });

  it('reuses and independently verifies the fixed NaisNet FFmpeg before and after packaging', () => {
    const steps = jobSteps(bridgeReleaseWorkflow().jobs?.['bridge-release']);
    const inspect = steps[stepIndex(steps, 'Resolve fixed signed FFmpeg component')];
    const verifyBefore = steps[stepIndex(steps, 'Verify NaisNet FFmpeg component before packaging')];
    const verifyAfter = steps[stepIndex(steps, 'Verify NaisNet FFmpeg after packaging')];
    expect(inspect?.run).toContain('RELEASE_TOOL_ROOT/scripts/ffmpeg-component-assets.mjs');
    expect(inspect?.run).toContain('identity --tool-root $env:RELEASE_TOOL_ROOT | ConvertFrom-Json');
    expect(inspect?.run).toContain('--tool-root $env:RELEASE_TOOL_ROOT');
    expect(inspect?.run).toContain('FFMPEG_COMPONENT_EXISTS');
    expect(verifyBefore?.run).toContain('RELEASE_TOOL_ROOT/scripts/ffmpeg-component-assets.mjs');
    expect(verifyBefore?.run).toContain('verify-metadata --tool-root');
    expect(verifyBefore?.run).toContain('--metadata dist/bridge-ffmpeg-component-release.json');
    expect(verifyBefore?.run).toContain('verify --tool-root');
    expect(verifyBefore?.run).toContain('--tool-root $env:RELEASE_TOOL_ROOT');
    expect(verifyBefore?.run).toContain('$env:AUTHENTICODE_INSPECTOR_PATH authenticode');
    expect(verifyBefore?.run).toContain('--organization-id 91210103MA7CJ3C094');
    expect(verifyAfter?.run).toContain('$componentManifest.sha256');
    expect(verifyAfter?.run).toContain('$componentManifest.size');
    expect(verifyAfter?.run).toContain('$env:AUTHENTICODE_INSPECTOR_PATH authenticode');
    expect(verifyAfter?.run).not.toMatch(/sign-evsign|EVSIGN_BRIDGE_CERTIFICATE/);
  });

  it('binds EVSign output to the unsigned PE and final-inspects every embedded security input', () => {
    const steps = jobSteps(bridgeReleaseWorkflow().jobs?.['bridge-release']);
    expect(steps.some((step) => step.name === 'Write structured Authenticode verifier')).toBe(false);
    const sign = stepIndex(steps, 'Sign RushRush outer executable');
    const inspect = stepIndex(steps, 'Inspect final bound bridge artifact');
    const evidence = stepIndex(steps, 'Prepare public bridge evidence');
    expect(sign).toBeLessThan(inspect);
    expect(inspect).toBeLessThan(evidence);
    expect(steps[sign]?.run).toContain('gift-panel-windows-x64.unsigned.exe');
    expect(steps[sign]?.run).not.toContain('Get-AuthenticodeSignature');
    expect(steps[inspect]?.run).toContain('$env:AUTHENTICODE_INSPECTOR_PATH verify-artifact');
    for (const binding of ['--unsigned', '--signed', '--version', '--tag', '--commit', '--root-spki', '--root-sha256', '--policy', '--policy-sha256', '--policy-epoch', '--stable-artifact', '--stable-tag', '--stable-channel', '--ffmpeg-archive', '--ffmpeg-manifest']) {
      expect(steps[inspect]?.run).toContain(binding);
    }
    expect(steps[inspect]?.run).toContain('--policy "$env:BRIDGE_READINESS_ROOT/private-bundle/bundle/policy.json"');
    expect(steps[inspect]?.run).toContain('--stable-artifact "$env:BRIDGE_READINESS_ROOT/stable-artifact.exe"');
    expect(steps[inspect]?.run).not.toContain('dist/bridge-bootstrap-policy.json');
    expect(steps[inspect]?.run).toContain('bridge-artifact-inspection.json');
  });

  it('creates complete evidence, reads back exact draft bytes, then publishes non-latest', () => {
    const workflow = bridgeReleaseWorkflow();
    const steps = jobSteps(workflow.jobs?.['bridge-release']);
    const evidence = steps[stepIndex(steps, 'Prepare public bridge evidence')];
    const create = steps[stepIndex(steps, 'Create immutable-shaped bridge draft')];
    const verifyDraft = stepIndex(steps, 'Read back and verify bridge draft');
    const publish = stepIndex(steps, 'Publish bridge as non-latest');
    const verifyPublished = stepIndex(steps, 'Verify published bridge remains non-latest');
    expect(evidence?.run).toContain('bridge-release-evidence.json');
    expect(evidence?.run).toContain('rootSpkiSha256');
    expect(evidence?.run).toContain('bootstrapPolicyEpoch');
    expect(evidence?.run).toContain('ffmpegIdentity');
    expect(create?.run).toContain('gh release create $env:BRIDGE_TAG --draft --verify-tag --title $env:BRIDGE_TAG --latest=false');
    expect(create?.run).toContain('gift-panel-windows-x64.exe');
    expect(create?.run).toContain('gift-panel-windows-x64.exe.sha256');
    expect(create?.run).toContain('gift-panel-update.json');
    expect(create?.run).toContain('gift-panel-changelog.json');
    expect(create?.run).toContain('ffmpeg-windows-x64.exe');
    expect(create?.run).toContain('ffmpeg-component-manifest.json');
    expect(create?.run).toContain('bridge-release-evidence.json');
    expect(verifyDraft).toBeLessThan(publish);
    expect(steps[publish]?.run).toContain('gh release edit $env:BRIDGE_TAG --draft=false --latest=false');
    expect(publish).toBeLessThan(verifyPublished);
    expect(steps[verifyPublished]?.run).toContain('/releases/latest');
    expect(steps[verifyPublished]?.run).toContain('$latest.id -eq $release.id');
  });

  it('validates the exact Task 7 ByTag mirror closure locally before draft creation', () => {
    const steps = jobSteps(bridgeReleaseWorkflow().jobs?.['bridge-release']);
    const evidence = stepIndex(steps, 'Prepare public bridge evidence');
    const mirrorClosure = stepIndex(steps, 'Validate local bridge mirror closure');
    const create = stepIndex(steps, 'Create immutable-shaped bridge draft');
    expect(evidence).toBeLessThan(mirrorClosure);
    expect(mirrorClosure).toBeLessThan(create);
    expect(steps[evidence]?.run).toContain('gift-panel-windows-x64.exe.sha256');
    expect(steps[evidence]?.run).toContain('gift-panel-update.json');
    expect(steps[evidence]?.run).toContain('local-bridge-release.json');
    expect(steps[mirrorClosure]?.run).toContain('go -C updateapi run ./cmd/release-closure');
    expect(steps[mirrorClosure]?.run).toContain('--tag v0.4.11');
  });

  it('executes public evidence generation with exact identities, hashes, sizes, and policy metadata', () => {
    const prepared = runBridgeEvidencePreparation();
    expect(prepared.result.status, prepared.result.stderr).toBe(0);
    expect(prepared.evidence, `${prepared.result.stdout}\n${prepared.result.stderr}`).toBeDefined();
    expect(prepared.evidence).toMatchObject({
      schemaVersion: 1,
      version: '0.4.11',
      tag: 'v0.4.11',
      commit: 'a'.repeat(40),
      latest: false,
      outerIdentity: {
        country: 'CN', organization: 'RushRush Network Technology Ltd', organizationId: '91450900MADM3GLG5P', authenticode: 'Valid',
      },
      ffmpegIdentity: {
        country: 'CN', organization: 'NaisNet Technology Co., Ltd.', organizationId: '91210103MA7CJ3C094', authenticode: 'Valid', version: '9.0',
      },
      rootSpkiSha256: 'b'.repeat(64),
      bootstrapPolicyEpoch: 1,
      bootstrapPolicySha256: 'c'.repeat(64),
      peContentSha256: 'd'.repeat(64),
      stableReleaseId: 412,
      stableArtifactSha256:'1'.repeat(64),
      observationEvidenceSha256: 'e'.repeat(64),
      policyReleaseId: 501,
      kmsKeyId: 'kms-production-key',
      trustAttestationSha256: 'f'.repeat(64),
    });
    expect(prepared.evidence.assets).toHaveLength(6);
    expect(prepared.checksums.split(/\r?\n/)).toHaveLength(7);
  });

  it('rejects evidence if packaged FFmpeg differs from the pre-packaging verified component', () => {
    const prepared = runBridgeEvidencePreparation('0'.repeat(64));
    expect(prepared.result.status).not.toBe(0);
    expect(prepared.result.stderr).toContain('Packaged FFmpeg differs from the pre-packaging verified component');
  });

  it('cannot mutate update pointers, COS, KMS, or ordinary latest state', () => {
    const workflow = bridgeReleaseWorkflow();
    const commands = semanticCommands(workflow.jobs?.['bridge-release']);
    for (const command of commands) {
      expect(command).not.toMatch(/channels\/(?:stable|legacy-rushrush)\/latest\.json/i);
      expect(command).not.toMatch(/SignByAsymmetricKey|trustpolicy.*\bsign\b|\bCOS_/i);
      expect(command).not.toMatch(/--latest(?:\s|$)(?!false)/);
    }
    expect(workflow.source).not.toMatch(/TENCENTCLOUD_|GIFT_PANEL_KMS_|PUBLISHER_ROTATION_/);
  });
});
