import { spawnSync } from 'node:child_process';
import { createHash } from 'node:crypto';
import { mkdtempSync, mkdirSync, readFileSync, rmSync, writeFileSync } from 'node:fs';
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
  return { concurrency: workflow.concurrency, document, release, source, steps: steps ?? [] };
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
${validation}
`;
    return spawnSync('pwsh', ['-NoLogo', '-NoProfile', '-NonInteractive', '-Command', '-'], {
      cwd: temporaryRoot,
      encoding: 'utf8',
      env: {
        ...process.env,
        EVSIGN_EXPECTED_SUBJECT: 'CN=Release Test',
        GITHUB_REPOSITORY: 'example/repository',
        RELEASE_TAG: 'v1.2.3',
      },
      input: script,
      timeout: 30_000,
    });
  } finally {
    rmSync(temporaryRoot, { recursive: true, force: true });
  }
}

describe('release workflow supply-chain contract', () => {
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
    expect(checkoutSteps).toHaveLength(1);
    expect(checkoutSteps[0]?.name).toBe('Check out release tag');
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
    expect(steps[build]?.run).toContain('npm run verify:ffmpeg');
    expect(steps[e2e]?.run).toContain('scripts/gift-clip-test-tools.mjs');
    expect(steps[e2e]?.run).toContain('npm run verify:gift-clip-export');
  });

  it('gates release publication on update tooling tests and the expected signer subject', () => {
    const { steps } = releaseWorkflow();
    const testUpdateApi = stepIndex(steps, 'Test domestic update tooling');
    const signOuter = stepIndex(steps, 'Prepare and sign release executable');
    const githubRelease = stepIndex(steps, 'Create GitHub release');

    expect(steps[testUpdateApi]?.run)
      .toBe('go -C updateapi test ./... -race -count=1');
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

  it('builds domestic update identity into the signed executable and ends at a validated GitHub Release', () => {
    const { steps } = releaseWorkflow();
    const build = stepIndex(steps, 'Build release executable');
    const sign = stepIndex(steps, 'Prepare and sign release executable');
    const githubRelease = stepIndex(steps, 'Create GitHub release');
    const validate = stepIndex(steps, 'Validate published release assets');

    expect(steps[build]?.env).toMatchObject({
      APP_COMMIT: '${{ env.RELEASE_COMMIT }}',
      APP_UPDATE_API_URL: '${{ vars.UPDATE_API_BASE_URL }}',
      APP_UPDATE_PUBLISHER: '${{ vars.EVSIGN_EXPECTED_SUBJECT }}',
    });
    expect(steps[sign]?.run).toContain(
      'node scripts/sign-evsign.mjs dist/gift-panel-windows-x64.exe',
    );
    expect(githubRelease).toBeLessThan(validate);
    expect(validate).toBe(steps.length - 1);
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
      expect(steps[stepIndex(steps, name)]?.if, `${name} must be skipped for repair`)
        .toBe("env.RELEASE_EXISTS != 'true'");
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
      'Get-AuthenticodeSignature -LiteralPath dist/gift-panel-windows-x64.exe',
    );
    expect(steps[validate]?.run).toContain('$signature.SignerCertificate.Subject -cne $env:EVSIGN_EXPECTED_SUBJECT');
    expect(steps[validate]?.run).toContain('Get-FileHash -Algorithm SHA256 -LiteralPath dist/gift-panel-windows-x64.exe');
    expect(steps[validate]?.run).toContain('gift-panel-windows-x64.exe.sha256');
    expect(steps[validate]?.run).toContain('dist/gift-panel-update.json');
    expect(validate).toBe(steps.length - 1);
  });

  it('accepts the exact typed fallback update manifest contract', () => {
    const result = runPublishedReleaseValidation(publishedManifestFixture());

    expect(result.status, result.stderr).toBe(0);
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
      .filter((run): run is string => typeof run === 'string' && /\bgh (?:api|release)\b/.test(run));
    expect(ghRuns.length).toBeGreaterThan(0);

    for (const run of ghRuns) {
      const lines = run.split(/\r?\n/);
      for (let index = 0; index < lines.length; index += 1) {
        if (!/\bgh (?:api|release)\b/.test(lines[index] ?? '')) continue;
        expect(lines[index + 1]?.trim(), `unchecked gh command: ${lines[index]?.trim()}`)
          .toMatch(/^if \(\$LASTEXITCODE -ne 0\) \{ throw /);
      }
    }
  });
});
