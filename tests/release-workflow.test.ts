import { readFileSync } from 'node:fs';
import { describe, expect, it } from 'vitest';
import { isScalar, parseDocument } from 'yaml';

interface ReleaseStep {
  env?: Record<string, string>;
  id?: string;
  name?: string;
  run?: string;
  uses?: string;
  'working-directory'?: string;
}

const auditedSetupMSYS2Commit = '66cd2cce69caa17b53920067426061ca1de3a884';

function releaseWorkflow() {
  const document = parseDocument(
    readFileSync(new URL('../.github/workflows/release.yml', import.meta.url), 'utf8'),
  );
  expect(document.errors).toEqual([]);

  const workflow = document.toJS() as {
    jobs?: { release?: { steps?: ReleaseStep[] } };
  };
  const steps = workflow.jobs?.release?.steps;
  expect(Array.isArray(steps)).toBe(true);
  return { document, steps: steps ?? [] };
}

function stepIndex(steps: ReleaseStep[], name: string): number {
  const index = steps.findIndex((step) => step.name === name);
  expect(index, `missing workflow step ${name}`).toBeGreaterThanOrEqual(0);
  return index;
}

describe('release workflow supply-chain contract', () => {
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
    expect(steps[build]?.run).toContain('npm run verify:ffmpeg');
    expect(steps[e2e]?.run).toContain('scripts/gift-clip-test-tools.mjs');
    expect(steps[e2e]?.run).toContain('npm run verify:gift-clip-export');
  });

  it('gates release publication on update tooling tests and the expected signer subject', () => {
    const { steps } = releaseWorkflow();
    const testUpdateApi = stepIndex(steps, 'Test domestic update tooling');
    const signOuter = stepIndex(steps, 'Prepare and sign release executable');
    const githubRelease = stepIndex(steps, 'Create or update GitHub release');

    expect(steps[testUpdateApi]?.run).toBe('go -C updateapi test ./... -race -count=1');
    expect(testUpdateApi).toBeLessThan(githubRelease);
    expect(signOuter).toBeLessThan(githubRelease);
    expect(steps[signOuter]?.env?.EVSIGN_EXPECTED_SUBJECT)
      .toBe('${{ vars.EVSIGN_EXPECTED_SUBJECT }}');
    expect(steps[signOuter]?.run).toContain(
      '$signature.Status -ne [System.Management.Automation.SignatureStatus]::Valid',
    );
    expect(steps[signOuter]?.run).toContain('$null -eq $signature.SignerCertificate');
    expect(steps[signOuter]?.run).toContain(
      '$signature.SignerCertificate.Subject -cne $env:EVSIGN_EXPECTED_SUBJECT',
    );
  });

  it('builds domestic update identity into the signed executable before mirroring it last', () => {
    const { steps } = releaseWorkflow();
    const build = stepIndex(steps, 'Build release executable');
    const sign = stepIndex(steps, 'Prepare and sign release executable');
    const githubRelease = stepIndex(steps, 'Create or update GitHub release');
    const mirror = stepIndex(steps, 'Mirror release to Tencent COS');
    const mirrorStep = steps[mirror];

    expect(steps[build]?.env).toMatchObject({
      APP_UPDATE_API_URL: '${{ vars.UPDATE_API_BASE_URL }}',
      APP_UPDATE_PUBLISHER: '${{ vars.EVSIGN_EXPECTED_SUBJECT }}',
    });
    expect(steps[sign]?.run).toContain(
      'node scripts/sign-evsign.mjs dist/gift-panel-windows-x64.exe',
    );
    expect(githubRelease).toBeLessThan(mirror);
    expect(mirror).toBe(steps.length - 1);
    expect(mirrorStep?.['working-directory']).toBe('updateapi');
    expect(mirrorStep?.run).toContain(
      'go run ./cmd/publish --tag $env:RELEASE_TAG --asset ../dist/gift-panel-windows-x64.exe --checksum ../dist/gift-panel-windows-x64.exe.sha256 --changelog ../dist/gift-panel-changelog.json',
    );
    expect(mirrorStep?.run).toContain('throw "Tencent COS release mirror failed"');
    expect(mirrorStep?.env).toEqual({
      COS_BUCKET: '${{ vars.COS_BUCKET }}',
      COS_REGION: '${{ vars.COS_REGION }}',
      COS_SECRET_ID: '${{ secrets.COS_RELEASE_SECRET_ID }}',
      COS_SECRET_KEY: '${{ secrets.COS_RELEASE_SECRET_KEY }}',
    });
    expect(mirrorStep?.run).not.toMatch(/COS_(?:SECRET_ID|SECRET_KEY)/);
  });
});
