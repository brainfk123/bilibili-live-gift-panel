import { spawnSync } from 'node:child_process';
import { createHash } from 'node:crypto';
import { existsSync, mkdtempSync, mkdirSync, readFileSync, rmSync, symlinkSync, writeFileSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';
import { describe, expect, it } from 'vitest';
import { parseDocument } from 'yaml';

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

function releaseWorkflow() {
  const source = readFileSync(new URL('../.github/workflows/release.yml', import.meta.url), 'utf8');
  const document = parseDocument(
    source,
  );
  expect(document.errors).toEqual([]);

  const workflow = document.toJS() as {
    concurrency?: { group?: string; 'cancel-in-progress'?: boolean };
	permissions?: Record<string, string>;
	jobs?: Record<string, WorkflowJob>;
  };
	const release = workflow.jobs?.['prepare-candidate'];
	const steps = ['classify', 'historical-verify', 'prepare-candidate', 'publish-candidate']
	  .flatMap((name) => workflow.jobs?.[name]?.steps ?? []);
	expect(steps.length).toBeGreaterThan(0);
	return { concurrency: workflow.concurrency, document, release, source, steps, workflow };
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

interface ChangelogReleaseFixture {
  version: string;
  date: string;
  title: string;
  summary: string;
  highlights: Array<{ label: string; title: string; description: string }>;
  visuals: string[];
}

interface ChangelogHistoryFixture {
  schemaVersion: number;
  releases: ChangelogReleaseFixture[];
}

const reviewedChangelogHistoryURL = new URL('../.github/changelog-history.json', import.meta.url);

function reviewedChangelogHistory() {
  const bytes = readFileSync(reviewedChangelogHistoryURL);
  return {
    bytes,
    digest: createHash('sha256').update(bytes).digest('hex'),
    document: JSON.parse(bytes.toString('utf8')) as ChangelogHistoryFixture,
  };
}

function mutateReviewedChangelogHistory(
  mutate: (releases: ChangelogReleaseFixture[]) => void,
): Buffer {
  const document = structuredClone(reviewedChangelogHistory().document);
  mutate(document.releases);
  return Buffer.from(JSON.stringify(document));
}

function changelogRelease(version: string): ChangelogReleaseFixture {
  return {
    version,
    date: '2026-09-01',
    title: `Release ${version}`,
    summary: `Summary ${version}`,
    highlights: [],
    visuals: [],
  };
}

function runCandidateChangelogStep(options: {
  target: readonly ChangelogReleaseFixture[];
  history?: Buffer;
  omitHistory?: boolean;
  sourceHistory?: readonly ChangelogReleaseFixture[];
  expectedHistorySHA256?: string;
  releaseTag?: string;
  releaseVersion?: string;
}) {
  const jobs = releaseWorkflow().workflow.jobs as unknown as Record<string, WorkflowJob>;
  const steps = jobSteps(jobs['prepare-candidate']);
  const step = steps[stepIndex(steps, 'Build canonical candidate changelog')];
  const root = mkdtempSync(join(tmpdir(), 'canonical-changelog-'));
  try {
    const tooling = join(root, 'tooling');
    mkdirSync(join(root, '.github'), { recursive: true });
    mkdirSync(join(root, 'dist'));
    mkdirSync(join(tooling, '.github'), { recursive: true });
    writeFileSync(join(root, 'gift-panel-changelog.json'), JSON.stringify({ schemaVersion: 1, releases: options.target }));
    const historyBytes = options.history ?? reviewedChangelogHistory().bytes;
    if (!options.omitHistory) writeFileSync(join(tooling, '.github', 'changelog-history.json'), historyBytes);
    writeFileSync(join(root, '.github', 'changelog-history.json'), JSON.stringify({
      schemaVersion: 1,
      releases: options.sourceHistory ?? [changelogRelease('0.3.0')],
    }));
    const outputPath = join(root, 'dist', 'canonical-gift-panel-changelog.json');
    const githubOutput = join(root, 'github-output.txt');
    const expectedHistorySHA256 = options.expectedHistorySHA256
      ?? createHash('sha256').update(historyBytes).digest('hex');
    const result = spawnSync('pwsh', ['-NoLogo', '-NoProfile', '-NonInteractive', '-File', '-'], {
      cwd: root,
      encoding: 'utf8',
      env: {
        ...process.env,
        GITHUB_OUTPUT: githubOutput,
        RELEASE_TAG: options.releaseTag ?? 'v0.4.12',
        RELEASE_VERSION: options.releaseVersion ?? '0.4.12',
        RELEASE_TOOL_ROOT: tooling,
        RELEASE_TOOLING_COMMIT_SHA: 'a'.repeat(40),
        STABLE_CHANGELOG_HISTORY_SHA256: expectedHistorySHA256,
      },
      input: `$ErrorActionPreference='Stop'\ntrap { Write-Error $_; exit 1 }\nfunction git { if($args.Count -eq 4 -and $args[0] -eq '-C' -and $args[2] -eq 'rev-parse' -and $args[3] -eq 'HEAD'){$env:RELEASE_TOOLING_COMMIT_SHA;$global:LASTEXITCODE=0;return};throw 'dynamic git changelog selection is forbidden' }\nfunction gh { throw 'dynamic GitHub changelog access is forbidden' }\nfunction Invoke-RestMethod { throw 'dynamic GitHub changelog access is forbidden' }\n${step?.run ?? ''}\n`,
      timeout: 30_000,
    });
    return {
      result,
      output: existsSync(outputPath) ? readFileSync(outputPath) : undefined,
      githubOutput: existsSync(githubOutput) ? readFileSync(githubOutput, 'utf8') : undefined,
    };
  } finally {
    rmSync(root, { recursive: true, force: true });
  }
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
    const dist = join(temporaryRoot, 'dist', 'historical');
    mkdirSync(dist, { recursive: true });
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
      writeFileSync(join(temporaryRoot, 'dist', 'historical-component-manifest.json'), JSON.stringify({
        version: '9.0', authenticode: true, size: ffmpeg.length,
        sha256: standalone.componentHash ?? ffmpegDigest,
      }));
    }

    const { steps } = releaseWorkflow();
    const validation = steps[stepIndex(steps, 'Verify historical Release without mutation')]?.run;
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
        RELEASE_VERSION: standalone ? '0.4.10' : '0.4.9',
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
	const sealedExecutableDirectory = join(dist, 'bridge-sealed-executable');
	mkdirSync(sealedExecutableDirectory);
	const executableHash = createHash('sha256').update(executable).digest('hex');
	const sealedExecutablePath = join(sealedExecutableDirectory, `${executableHash}.exe`);
    const ffmpegHash = createHash('sha256').update(ffmpeg).digest('hex');
    const manifestHash = createHash('sha256').update(componentManifest).digest('hex');
	writeFileSync(sealedExecutablePath, executable);
	writeFileSync(join(dist, 'gift-panel-windows-x64.exe'), executable);
    writeFileSync(join(dist, 'ffmpeg-windows-x64.exe'), ffmpeg);
    writeFileSync(join(dist, 'ffmpeg-component-manifest.json'), componentManifest);
    writeFileSync(join(dist, 'bridge-artifact-inspection.json'), JSON.stringify({
	  version: '0.4.11', tag: 'v0.4.11', commit: 'a'.repeat(40), peContentSha256: 'd'.repeat(64), signedFileSha256: executableHash, signedFileSize: executable.length,
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
		BRIDGE_SEALED_EXE_PATH: sealedExecutablePath,
		BRIDGE_EXPECTED_EXE_PATH: join(dist, 'gift-panel-windows-x64.exe'),
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
	it('splits stable classification, historical verification, candidate preparation, and publication into isolated capabilities', () => {
	  const { workflow } = releaseWorkflow();
	  const jobs = workflow.jobs as unknown as Record<string, WorkflowJob>;
	  expect(workflow.permissions).toEqual({ contents: 'read' });
	  expect(releaseWorkflow().source).not.toContain('STABLE_TRUST_ROOT_KEY_ID');
	  expect(Object.keys(jobs)).toEqual(['classify', 'historical-verify', 'prepare-candidate', 'publish-candidate']);
	  expect(jobs.classify?.permissions).toEqual({ contents: 'read' });
	  expect(jobs['historical-verify']?.permissions).toEqual({ contents: 'read' });
	  expect(jobs['historical-verify']?.environment).toBeUndefined();
	  expect(jobs['prepare-candidate']?.permissions).toEqual({ contents: 'read' });
	  expect(jobs['prepare-candidate']?.environment).toBe('stable-prepare');
	  expect(jobs['publish-candidate']?.permissions).toEqual({ actions: 'read', contents: 'write', 'id-token': 'write', attestations: 'write' });
	  expect(jobs['publish-candidate']?.environment).toBe('stable-publish');
	});

	it('binds candidate retrieval to the successful reviewed push run and exact artifact lifetime', () => {
	  const jobs=releaseWorkflow().workflow.jobs as unknown as Record<string,WorkflowJob>;
	  const steps=jobSteps(jobs['publish-candidate']);
	  const validate=steps[stepIndex(steps,'Validate reviewed publish inputs')];
	  const metadata=steps[stepIndex(steps,'Verify exact candidate run and artifact provenance')];
	  const download=steps[stepIndex(steps,'Download exact reviewed stable candidate')];
	  expect(validate?.env).toMatchObject({
		STABLE_CANDIDATE_WORKFLOW_ID:'${{ vars.STABLE_CANDIDATE_WORKFLOW_ID }}',
		STABLE_CANDIDATE_RUN_ATTEMPT:'${{ vars.STABLE_CANDIDATE_RUN_ATTEMPT }}',
		STABLE_CANDIDATE_ARTIFACT_CREATED_AT:'${{ vars.STABLE_CANDIDATE_ARTIFACT_CREATED_AT }}',
		STABLE_CANDIDATE_ARTIFACT_EXPIRES_AT:'${{ vars.STABLE_CANDIDATE_ARTIFACT_EXPIRES_AT }}',
	  });
	  const run=metadata?.run??'';
	  for(const binding of ['/actions/runs/','repository.full_name','head_repository.full_name','.github/workflows/release.yml','workflow_id','event','push','status','completed','conclusion','success','head_sha','run_attempt','pull_requests','created_at','expires_at']) expect(run).toContain(binding);
	  expect(download?.with).toMatchObject({'artifact-ids':'${{ vars.STABLE_CANDIDATE_ARTIFACT_ID }}','run-id':'${{ vars.STABLE_CANDIDATE_RUN_ID }}','github-token':'${{ github.token }}'});
	});

	it('executes candidate provenance validation and rejects 403, wrong run state, reruns, forks, or moved artifacts', () => {
	  const jobs=releaseWorkflow().workflow.jobs as unknown as Record<string,WorkflowJob>;
	  const step=jobSteps(jobs['publish-candidate'])[stepIndex(jobSteps(jobs['publish-candidate']),'Verify exact candidate run and artifact provenance')];
	  const baseRun={id:123,repository:{full_name:'example/repository'},head_repository:{full_name:'example/repository'},path:'.github/workflows/release.yml',workflow_id:55,event:'push',status:'completed',conclusion:'success',head_sha:'a'.repeat(40),head_branch:'v0.4.12',run_attempt:1,pull_requests:[]};
	  const baseArtifact={id:456,workflow_run:{id:123},name:`stable-candidate-v0.4.12-${'b'.repeat(64)}`,expired:false,digest:`sha256:${'c'.repeat(64)}`,created_at:'2026-09-01T00:00:00Z',expires_at:'2030-09-01T00:00:00Z'};
	  const execute=(run:unknown,artifact:unknown,forbidden=false)=>spawnSync('pwsh',['-NoLogo','-NoProfile','-NonInteractive','-File','-'],{encoding:'utf8',env:{...process.env,RUN_JSON:JSON.stringify(run),ARTIFACT_JSON:JSON.stringify(artifact),GITHUB_REPOSITORY:'example/repository',GH_TOKEN:'test',STABLE_CANDIDATE_RUN_ID:'123',STABLE_CANDIDATE_WORKFLOW_ID:'55',STABLE_CANDIDATE_RUN_ATTEMPT:'1',STABLE_CANDIDATE_ARTIFACT_ID:'456',STABLE_CANDIDATE_ARTIFACT_DIGEST:'c'.repeat(64),STABLE_CANDIDATE_ARTIFACT_CREATED_AT:'2026-09-01T00:00:00Z',STABLE_CANDIDATE_ARTIFACT_EXPIRES_AT:'2030-09-01T00:00:00Z',STABLE_CANDIDATE_SHA256:'b'.repeat(64),STABLE_CANDIDATE_COMMIT_SHA:'a'.repeat(40),RELEASE_TAG:'v0.4.12'},input:`$ErrorActionPreference='Stop'\nfunction Invoke-RestMethod { param([Parameter(Position=0)][string]$Uri,[hashtable]$Headers) if(${forbidden?'$true':'$false'}){throw '403 Forbidden'}; if($Uri -match '/actions/runs/'){return $env:RUN_JSON|ConvertFrom-Json};return $env:ARTIFACT_JSON|ConvertFrom-Json }\n${step?.run??''}`});
	  const valid=execute(baseRun,baseArtifact);expect(valid.status,valid.stderr).toBe(0);
	  const rejected=(result:ReturnType<typeof execute>)=>result.status!==0||/403 Forbidden|provenance mismatch|artifact metadata mismatch|artifact is expired/.test(result.stderr);
	  const forbidden=execute(baseRun,baseArtifact,true);expect(rejected(forbidden),forbidden.stderr).toBe(true);
	  for(const [name,mutate] of [
		['in progress',(run:any)=>{run.status='in_progress';}],['failed',(run:any)=>{run.conclusion='failure';}],['rerun',(run:any)=>{run.run_attempt=2;}],['workflow',(run:any)=>{run.workflow_id=99;}],['fork',(run:any)=>{run.head_repository.full_name='fork/repository';}],['head',(run:any)=>{run.head_sha='0'.repeat(40);}],
	  ] as const){const run=structuredClone(baseRun);mutate(run);const result=execute(run,baseArtifact);expect(rejected(result),`${name}: ${result.stderr}`).toBe(true);}
	  for(const mutate of [(artifact:any)=>{artifact.workflow_run.id=999;},(artifact:any)=>{artifact.digest=`sha256:${'0'.repeat(64)}`;},(artifact:any)=>{artifact.expires_at='2029-09-01T00:00:00Z';}]){const artifact=structuredClone(baseArtifact);mutate(artifact);const result=execute(baseRun,artifact);expect(rejected(result),result.stderr).toBe(true);}
	});

	it('uses separate persistent tooling and target checkout roots that survive source cleanup', () => {
	  const jobs=releaseWorkflow().workflow.jobs as unknown as Record<string,WorkflowJob>;
	  for(const name of ['historical-verify','prepare-candidate']){
		const steps=jobSteps(jobs[name]);
		const checkouts=steps.filter((step)=>step.uses?.startsWith('actions/checkout'));
		expect(checkouts.map((step)=>step.with?.path)).toEqual(['tooling','source']);
		expect(steps[0]?.name).toContain('Validate reviewed tooling SHA');
		expect(semanticCommands(jobs[name]).join('\n')).toContain('RELEASE_TOOL_ROOT=$env:GITHUB_WORKSPACE/tooling');
	  }
	  const root=mkdtempSync(join(tmpdir(),'checkout-root-isolation-'));
	  try{mkdirSync(join(root,'tooling'),{recursive:true});mkdirSync(join(root,'source'),{recursive:true});writeFileSync(join(root,'tooling','marker'),'reviewed');rmSync(join(root,'source'),{recursive:true,force:true});mkdirSync(join(root,'source'));expect(readFileSync(join(root,'tooling','marker'),'utf8')).toBe('reviewed');}finally{rmSync(root,{recursive:true,force:true});}
	});

	it('keeps target build and test environments free of every EVSign secret or selector', () => {
	  const jobs=releaseWorkflow().workflow.jobs as unknown as Record<string,WorkflowJob>;
	  const steps=jobSteps(jobs['prepare-candidate']);
	  const build=steps[stepIndex(steps,'Build stable candidate executable')];
	  const sign=steps[stepIndex(steps,'Sign stable candidate executable')];
	  for(const key of Object.keys(build?.env??{})) expect(key).not.toMatch(/^EVSIGN_/);
	  expect(build?.run).toContain('npm run build:exe');
	  expect(build?.run).not.toContain('sign-evsign');
	  expect(sign?.env).toMatchObject({EVSIGN_CERTIFICATE:'${{ vars.EVSIGN_CERTIFICATE }}',EVSIGN_PUBLISHER_IDENTITY:'${{ vars.EVSIGN_PUBLISHER_IDENTITY }}',EVSIGN_KEY:'${{ secrets.EVSIGN_KEY }}',EVSIGN_PASSWORD:'${{ secrets.EVSIGN_PASSWORD }}'});
	  expect(sign?.run).toContain('$env:EVSIGN_SCRIPT_PATH --profile stable');
	  expect(sign?.run).not.toMatch(/npm|go\s+-C/);
	  const root=mkdtempSync(join(tmpdir(),'candidate-env-isolation-'));try{
		const dumper=join(root,'dump-env.mjs');writeFileSync(dumper,"process.stdout.write(JSON.stringify(Object.keys(process.env).filter((name)=>name.startsWith('EVSIGN_')).sort()))");
		const run=(environment:Record<string,string>)=>spawnSync(process.execPath,[dumper],{encoding:'utf8',env:environment});
		const buildDump=run(Object.fromEntries(Object.keys(build?.env??{}).map((name)=>[name,'fixture'])));expect(JSON.parse(buildDump.stdout)).toEqual([]);
		const signDump=run(Object.fromEntries(Object.keys(sign?.env??{}).map((name)=>[name,'fixture'])));expect(JSON.parse(signDump.stdout)).toEqual(['EVSIGN_CERTIFICATE','EVSIGN_KEY','EVSIGN_PASSWORD','EVSIGN_PUBLISHER_IDENTITY']);
	  }finally{rmSync(root,{recursive:true,force:true});}
	});

	it('re-verifies every published asset after latest mutation and detects concurrent closure changes', () => {
	  const jobs=releaseWorkflow().workflow.jobs as unknown as Record<string,WorkflowJob>;
	  const steps=jobSteps(jobs['publish-candidate']);
	  const final=steps[stepIndex(steps,'Verify final published stable closure')];
	  const run=final?.run??'';
	  expect(run).toContain('published-readback');
	  expect(run).toContain('gh release download');
	  expect(run).toContain('[string]$asset.digest');
	  expect(run).toContain('[int64]$asset.size');
	  expect(run).toContain('/releases/latest');
	  expect(run).toContain('exact published asset closure changed');
	  expect(run.indexOf('/releases/latest')).toBeGreaterThan(run.indexOf('foreach($name in $required)'));
	  expect(run).toContain('latest asset closure');
	});

	it('executes final published closure verification and rejects post-draft removal or replacement', () => {
	  const jobs=releaseWorkflow().workflow.jobs as unknown as Record<string,WorkflowJob>;const steps=jobSteps(jobs['publish-candidate']);const step=steps[stepIndex(steps,'Verify final published stable closure')];
	  const root=mkdtempSync(join(tmpdir(),'published-stable-closure-'));try{
		const names=['gift-panel-windows-x64.exe','gift-panel-windows-x64.exe.sha256','gift-panel-update.json','gift-panel-changelog.json','stable-release-evidence.json','ffmpeg-9.0.tar.xz','ffmpeg-9.0.tar.xz.asc','ffmpeg-build-config.txt','ffmpeg-component-gate.txt','toolchain-lock.json','NOTICE.md','COPYING.LGPLv2.1','ffmpeg-windows-x64.exe','ffmpeg-windows-x64.exe.sha256'];mkdirSync(join(root,'candidate','sealed'),{recursive:true});
		for(const name of names){const path=name==='gift-panel-windows-x64.exe'?join(root,'candidate','sealed',name):join(root,'candidate',name);writeFileSync(path,Buffer.from(`asset:${name}`));}
		const assets=names.map((name)=>{const path=name==='gift-panel-windows-x64.exe'?join(root,'candidate','sealed',name):join(root,'candidate',name);const bytes=readFileSync(path);return{name,size:bytes.length,digest:`sha256:${createHash('sha256').update(bytes).digest('hex')}`};});
		const release={id:77,tag_name:'v0.4.12',draft:false,prerelease:false,published_at:'2026-09-01T00:00:00Z',assets};const latest=structuredClone(release);
		const execute=(candidateRelease:unknown,candidateLatest:unknown=latest)=>{rmSync(join(root,'published-readback'),{recursive:true,force:true});return spawnSync('pwsh',['-NoLogo','-NoProfile','-NonInteractive','-File','-'],{cwd:root,encoding:'utf8',env:{...process.env,RELEASE_JSON:JSON.stringify(candidateRelease),LATEST_JSON:JSON.stringify(candidateLatest),RELEASE_TAG:'v0.4.12',GITHUB_REPOSITORY:'example/repository',GH_TOKEN:'test'},input:`$ErrorActionPreference='Stop'\nfunction Invoke-RestMethod { param([Parameter(Position=0)][string]$Uri,[hashtable]$Headers) if($Uri -like '*/releases/latest'){return $env:LATEST_JSON|ConvertFrom-Json};return $env:RELEASE_JSON|ConvertFrom-Json }\nfunction gh { $name=$args[$args.Count-1];$source=if($name -ceq 'gift-panel-windows-x64.exe'){'candidate/sealed/gift-panel-windows-x64.exe'}else{"candidate/$name"};Copy-Item -LiteralPath $source -Destination "published-readback/$name";$global:LASTEXITCODE=0 }\n${step?.run??''}`});};
		const valid=execute(release);expect(valid.status,`${valid.stdout}${valid.stderr}`).toBe(0);
		const removed=structuredClone(release);removed.assets.pop();const removedResult=execute(removed);expect(removedResult.status!==0||`${removedResult.stdout}${removedResult.stderr}`.includes('exact published asset closure changed')).toBe(true);
		const replaced=structuredClone(release);replaced.assets[0]!.digest=`sha256:${'0'.repeat(64)}`;const replacedResult=execute(replaced);expect(replacedResult.status!==0||`${replacedResult.stdout}${replacedResult.stderr}`.includes('exact published asset closure changed')).toBe(true);
		const latestRemoved=structuredClone(latest);latestRemoved.assets.pop();const latestResult=execute(release,latestRemoved);expect(latestResult.status!==0||`${latestResult.stdout}${latestResult.stderr}`.includes('latest asset closure'),`${latestResult.stdout}${latestResult.stderr}`).toBe(true);
	  }finally{rmSync(root,{recursive:true,force:true});}
	});

	it('uses a closed supported historical tag to structured signer identity map', () => {
	  const jobs=releaseWorkflow().workflow.jobs as unknown as Record<string,WorkflowJob>;
	  const classify=semanticCommands(jobs.classify).join('\n');
	  const historical=jobSteps(jobs['historical-verify']);
	  const identity=historical[stepIndex(historical,'Resolve exact historical signer identity')];
	  expect(classify).toContain("v0.4.7|v0.4.9|v0.4.10");
	  expect(classify).toContain('unsupported historical stable tag');
	  expect(identity?.run).toContain("'v0.4.7'");
	  expect(identity?.run).toContain('RushRush Network Technology Ltd');
	  expect(identity?.run).toContain("'v0.4.9'");
	  expect(identity?.run).toContain("'v0.4.10'");
	  expect(identity?.run).toContain('NaisNet Technology Co., Ltd.');
	  expect(semanticCommands(jobs['historical-verify']).join('\n')).toContain('--organization $env:HISTORICAL_SIGNER_ORGANIZATION');
	});

	it('uses only digest-bound reviewed-tooling changelog history before target build', () => {
	  const jobs=releaseWorkflow().workflow.jobs as unknown as Record<string,WorkflowJob>;
	  const steps=jobSteps(jobs['prepare-candidate']);
	  const merge=steps[stepIndex(steps,'Build canonical candidate changelog')];
	  const run=merge?.run??'';
	  expect(stepIndex(steps,'Build canonical candidate changelog')).toBeLessThan(stepIndex(steps,'Install and test candidate source'));
	  expect(merge?.env).toMatchObject({
		RELEASE_TOOLING_COMMIT_SHA:'${{ vars.RELEASE_TOOLING_COMMIT_SHA }}',
		STABLE_CHANGELOG_HISTORY_SHA256:'${{ vars.STABLE_CHANGELOG_HISTORY_SHA256 }}',
	  });
	  expect(run).toContain('gift-panel-changelog.json');
	  expect(run).toContain('.github/changelog-history.json');
	  expect(run).toContain('$env:RELEASE_TOOL_ROOT');
	  expect(run).toContain('$env:STABLE_CHANGELOG_HISTORY_SHA256');
	  expect(run).toContain('262144');
	  expect(run).toContain('duplicate changelog version');
	  expect(run).toContain('changelog version order');
	  expect(run).not.toMatch(/\bgit\s+(?:tag|show)\b|\bgh\s+release\s+download\b|Invoke-RestMethod/);
	  const prepare=steps[stepIndex(steps,'Prepare signed stable candidate')];
	  expect(prepare?.run).toContain('historySha256');
	  expect(prepare?.run).toContain('toolingCommit');
	  const publishSteps=jobSteps(jobs['publish-candidate']);
	  expect(publishSteps[stepIndex(publishSteps,'Validate reviewed publish inputs')]?.env).toMatchObject({
		RELEASE_TOOLING_COMMIT_SHA:'${{ vars.RELEASE_TOOLING_COMMIT_SHA }}',
		STABLE_CHANGELOG_HISTORY_SHA256:'${{ vars.STABLE_CHANGELOG_HISTORY_SHA256 }}',
	  });
	  expect(publishSteps[stepIndex(publishSteps,'Revalidate reviewed stable candidate')]?.run).toContain('historySha256');
	});

	it('deterministically prepends v0.4.12 to the exact checked-in reviewed history', () => {
	  const reviewed=reviewedChangelogHistory();
	  const options={target:[changelogRelease('0.4.12')],history:reviewed.bytes,sourceHistory:[changelogRelease('0.3.0')]};
	  const first=runCandidateChangelogStep(options);expect(first.result.status,`${first.result.stdout}${first.result.stderr}`).toBe(0);
	  const second=runCandidateChangelogStep(options);expect(second.result.status,`${second.result.stdout}${second.result.stderr}`).toBe(0);
	  expect(first.output).toEqual(second.output);
	  expect(first.githubOutput).toContain(`history_sha256=${reviewed.digest}`);
	  const merged=JSON.parse(first.output!.toString('utf8')) as ChangelogHistoryFixture;
	  const versions=merged.releases.map((release)=>release.version);
	  expect(versions.slice(0,4)).toEqual(['0.4.12','0.4.10','0.4.9','0.4.7']);
	  expect(versions.slice(1)).toEqual(reviewed.document.releases.map((release)=>release.version));
	  expect(versions).not.toContain('0.4.8');
	});

	it('keeps later stable history digest-bound without reusing the v0.4.12 sequence invariant', () => {
	  const history=mutateReviewedChangelogHistory((releases)=>releases.unshift(changelogRelease('0.4.11')));
	  const execution=runCandidateChangelogStep({
		target:[changelogRelease('0.4.13')],history,releaseTag:'v0.4.13',releaseVersion:'0.4.13',
	  });
	  expect(execution.result.status,`${execution.result.stdout}${execution.result.stderr}`).toBe(0);
	  const merged=JSON.parse(execution.output!.toString('utf8')) as ChangelogHistoryFixture;
	  expect(merged.releases.slice(0,5).map((release)=>release.version)).toEqual(['0.4.13','0.4.11','0.4.10','0.4.9','0.4.7']);
	});

	it.each(['0.4.10','0.4.9','0.4.7'])('rejects checked-in reviewed history missing %s for first enrollment', (version) => {
	  const history=mutateReviewedChangelogHistory((releases)=>{
		const index=releases.findIndex((release)=>release.version===version);
		expect(index,`checked-in reviewed history must contain ${version}`).toBeGreaterThanOrEqual(0);
		releases.splice(index,1);
	  });
	  const execution=runCandidateChangelogStep({target:[changelogRelease('0.4.12')],history});
	  expect(execution.result.status,`${execution.result.stdout}${execution.result.stderr}`).not.toBe(0);
	});

	it.each([
	  ['an inserted v0.4.8',()=>mutateReviewedChangelogHistory((releases)=>releases.splice(2,0,changelogRelease('0.4.8'))),undefined],
	  ['reordered required recent versions',()=>mutateReviewedChangelogHistory((releases)=>{[releases[0],releases[1]]=[releases[1],releases[0]];}),undefined],
	  ['changed reviewed history content',()=>mutateReviewedChangelogHistory((releases)=>{releases[0].summary=`${releases[0].summary} changed`;}),reviewedChangelogHistory().digest],
	  ['a reviewed history digest mismatch',()=>reviewedChangelogHistory().bytes,'0'.repeat(64)],
	  ['reviewed history from a higher moved tag',()=>mutateReviewedChangelogHistory((releases)=>releases.unshift(changelogRelease('0.4.13'))),undefined],
	  ['duplicate reviewed history versions',()=>mutateReviewedChangelogHistory((releases)=>releases.splice(1,0,structuredClone(releases[0]))),undefined],
	  ['a current-version history collision',()=>mutateReviewedChangelogHistory((releases)=>releases.unshift(changelogRelease('0.4.12'))),undefined],
	] as const)('rejects %s',(_name,historyFactory,expectedHistorySHA256)=>{
	  const execution=runCandidateChangelogStep({target:[changelogRelease('0.4.12')],history:historyFactory(),expectedHistorySHA256});
	  expect(execution.result.status,`${execution.result.stdout}${execution.result.stderr}`).not.toBe(0);
	});

	it.each([
	  ['an extra target entry',{target:[changelogRelease('0.4.12'),changelogRelease('0.4.11')]}],
	  ['missing reviewed history',{target:[changelogRelease('0.4.12')],omitHistory:true}],
	  ['malformed reviewed history',{target:[changelogRelease('0.4.12')],history:Buffer.from('{"schemaVersion":1,"releases":')}],
	] as const)('rejects %s',(_name,options)=>{
	  const execution=runCandidateChangelogStep(options);
	  expect(execution.result.status,`${execution.result.stdout}${execution.result.stderr}`).not.toBe(0);
	});

	it('validates extracted candidate directories before executing downloaded tools', () => {
	  const jobs=releaseWorkflow().workflow.jobs as unknown as Record<string,WorkflowJob>;
	  const steps=jobSteps(jobs['publish-candidate']);
	  const extraction=steps[stepIndex(steps,'Validate extracted candidate tree')];
	  expect(stepIndex(steps,'Validate extracted candidate tree')).toBeLessThan(stepIndex(steps,'Revalidate reviewed stable candidate'));
	  const run=extraction?.run??'';
	  expect(run).toContain('ReparsePoint');
	  expect(run).toContain('unexpected candidate directory');
	  expect(run).toContain('empty candidate directory');
	  expect(run).toContain("@('sealed','tools')");
	  const root=mkdtempSync(join(tmpdir(),'candidate-extraction-'));try{
		const candidate=join(root,'candidate');mkdirSync(join(candidate,'sealed'),{recursive:true});mkdirSync(join(candidate,'tools'));
		const hash='b'.repeat(64);const files=[`sealed/${hash}.exe`,'stable-artifact-inspection.json','candidate-evidence.json','gift-panel-windows-x64.exe.sha256','gift-panel-update.json','gift-panel-changelog.json','ffmpeg.zip','manifest.json','ffmpeg-windows-x64.exe','ffmpeg-windows-x64.exe.sha256','ffmpeg-9.0.tar.xz','ffmpeg-9.0.tar.xz.asc','ffmpeg-build-config.txt','ffmpeg-component-gate.txt','toolchain-lock.json','NOTICE.md','COPYING.LGPLv2.1','tools/artifact-inspector.exe','tools/artifact-inspector.exe.sha256','tools/verify-enrollment-build.mjs','tools/verify-enrollment-build.mjs.sha256'];
		for(const relative of files)writeFileSync(join(candidate,relative),Buffer.from(relative));
		const execute=()=>spawnSync('pwsh',['-NoLogo','-NoProfile','-NonInteractive','-File','-'],{cwd:root,encoding:'utf8',env:{...process.env,STABLE_CANDIDATE_SHA256:hash},input:`$ErrorActionPreference='Stop'\n${run}`});
		const valid=execute();expect(valid.status,valid.stderr).toBe(0);
		for(const file of ['artifact-inspector.exe','artifact-inspector.exe.sha256','verify-enrollment-build.mjs','verify-enrollment-build.mjs.sha256'])rmSync(join(candidate,'tools',file));
		const empty=execute();expect(empty.status!==0||`${empty.stdout}${empty.stderr}`.includes('empty candidate directory'),`${empty.stdout}${empty.stderr}`).toBe(true);
		for(const file of ['artifact-inspector.exe','artifact-inspector.exe.sha256','verify-enrollment-build.mjs','verify-enrollment-build.mjs.sha256'])writeFileSync(join(candidate,'tools',file),file);
		const outside=join(root,'outside');mkdirSync(outside);let linked=false;try{symlinkSync(outside,join(candidate,'junction'),process.platform==='win32'?'junction':'dir');linked=true;}catch{/* junction creation is host-policy dependent */}if(linked){const reparse=execute();expect(reparse.status!==0||`${reparse.stdout}${reparse.stderr}`.includes('ReparsePoint'),`${reparse.stdout}${reparse.stderr}`).toBe(true);}
	  }finally{rmSync(root,{recursive:true,force:true});}
	});

	it('prepares and reads back a content-addressed candidate without Release, KMS, or publication authority', () => {
	  const jobs = releaseWorkflow().workflow.jobs as unknown as Record<string, WorkflowJob>;
	  const prepare = jobs['prepare-candidate'];
	  const steps = jobSteps(prepare);
	  const upload = steps[stepIndex(steps, 'Upload content-addressed stable candidate')];
	  const readback = steps[stepIndex(steps, 'Read back uploaded stable candidate')];
	  expect(upload?.uses).toBe('actions/upload-artifact@ea165f8d65b6e75b540449e92b4886f43607fa02');
	  expect(upload?.with).toMatchObject({ 'compression-level': 0, 'if-no-files-found': 'error' });
	  expect(String(upload?.with?.name)).toContain('steps.candidate.outputs.artifact-name');
	  expect(readback?.uses).toBe('actions/download-artifact@d3f86a106a0bac45b974a628896c90dbdf5c8093');
	  const commands = semanticCommands(prepare).join('\n');
	  expect(commands).toContain('sign-evsign');
	  expect(commands).toContain('signedFileSha256');
	  expect(commands).toContain('artifact-digest');
	  expect(commands).toContain('candidate-readback');
	  expect(commands).not.toMatch(/gh release (?:create|upload|edit)|SignByAsymmetricKey|TENCENTCLOUD_|\bCOS_/i);
	});

	it('executes candidate upload readback equality and rejects substituted bytes', () => {
	  const jobs = releaseWorkflow().workflow.jobs as unknown as Record<string, WorkflowJob>;
	  const step = jobSteps(jobs['prepare-candidate'])[stepIndex(jobSteps(jobs['prepare-candidate']), 'Verify uploaded candidate is bit-identical')];
	  const root = mkdtempSync(join(tmpdir(), 'stable-candidate-readback-'));
	  try {
		for (const directory of ['dist/stable-candidate/sealed', 'dist/candidate-readback/sealed']) mkdirSync(join(root, directory), { recursive: true });
		for (const relative of ['evidence.json', 'sealed/candidate.exe']) {
		  const contents = Buffer.from(`exact:${relative}`);
		  writeFileSync(join(root, 'dist/stable-candidate', relative), contents);
		  writeFileSync(join(root, 'dist/candidate-readback', relative), contents);
		}
		const script=(step?.run ?? '')
		  .replace("'${{ steps.upload.outputs.artifact-id }}'", "'123'")
		  .replace("'${{ steps.upload.outputs.artifact-digest }}'", `'${'a'.repeat(64)}'`);
		const run=()=>spawnSync('pwsh',['-NoLogo','-NoProfile','-NonInteractive','-Command','-'],{cwd:root,input:`$ErrorActionPreference='Stop'\n${script}`,encoding:'utf8'});
		expect(run().status).toBe(0);
		writeFileSync(join(root,'dist/candidate-readback/sealed/candidate.exe'),Buffer.from('substituted'));
		const rejected=run();
		expect(rejected.status).not.toBe(0);
		expect(rejected.stderr).toContain('Candidate bytes changed during Actions artifact upload/readback');
	  } finally { rmSync(root,{recursive:true,force:true}); }
	});

	it('publishes only an exact protected candidate and has no signer, build, target-code, bridge, KMS, or COS capability', () => {
	  const jobs = releaseWorkflow().workflow.jobs as unknown as Record<string, WorkflowJob>;
	  const publish = jobs['publish-candidate'];
	  const steps = jobSteps(publish);
	  const validate = steps[stepIndex(steps, 'Validate reviewed publish inputs')];
	  expect(validate?.env).toMatchObject({
		STABLE_CANDIDATE_RUN_ID: '${{ vars.STABLE_CANDIDATE_RUN_ID }}',
		STABLE_CANDIDATE_ARTIFACT_ID: '${{ vars.STABLE_CANDIDATE_ARTIFACT_ID }}',
		STABLE_CANDIDATE_ARTIFACT_DIGEST: '${{ vars.STABLE_CANDIDATE_ARTIFACT_DIGEST }}',
		STABLE_CANDIDATE_SHA256: '${{ vars.STABLE_CANDIDATE_SHA256 }}',
		STABLE_CANDIDATE_SIZE: '${{ vars.STABLE_CANDIDATE_SIZE }}',
		STABLE_CANDIDATE_TAG: '${{ vars.STABLE_CANDIDATE_TAG }}',
		STABLE_CANDIDATE_COMMIT_SHA: '${{ vars.STABLE_CANDIDATE_COMMIT_SHA }}',
	  });
	  expect(steps[stepIndex(steps, 'Download exact reviewed stable candidate')]?.uses)
		.toBe('actions/download-artifact@d3f86a106a0bac45b974a628896c90dbdf5c8093');
	  const commands = semanticCommands(publish).join('\n');
	  expect(commands).toContain('STABLE_CANDIDATE_ARTIFACT_DIGEST');
	  expect(commands).toContain('STABLE_CANDIDATE_SHA256');
	  expect(commands).toContain('verify-enrollment');
	  expect(commands).toContain('gh release create');
	  expect(commands).not.toMatch(/sign-evsign|EVSIGN_|npm (?:run|ci)|build:exe|go\s+.*\bbuild\b|SignByAsymmetricKey|TENCENTCLOUD_|BRIDGE_|\bCOS_/i);
	});

	it('keeps historical verification read-only and selected only from verified existing Release state', () => {
	  const jobs = releaseWorkflow().workflow.jobs as unknown as Record<string, WorkflowJob>;
	  const classify = semanticCommands(jobs.classify).join('\n');
	  const historical = semanticCommands(jobs['historical-verify']).join('\n');
	  expect(classify).toContain('/releases/tags/');
	  expect(classify).toContain('historical-verify');
	  expect(classify).toContain('Existing enrollment stable Releases are immutable');
	  expect(historical).toContain('gh release download');
	  expect(historical).toContain('verify --kind fixed');
	  expect(historical).toContain('--manifest-output dist/historical-component-manifest.json');
	  expect(historical).not.toMatch(/gh release (?:create|upload|edit)|actions\/attest|upload-artifact|sign-evsign|EVSIGN_|SignByAsymmetricKey/i);
	});

	it('executes strict historical fallback and fixed FFmpeg verification', () => {
	  expect(runPublishedReleaseValidation(publishedManifestFixture()).status).toBe(0);
	  expect(runPublishedReleaseValidation(publishedManifestFixture(), undefined, {}).status).toBe(0);
	  const badChecksum=runPublishedReleaseValidation(publishedManifestFixture(),undefined,{checksum:`${'0'.repeat(64)}  ffmpeg-windows-x64.exe`});
	  expect(badChecksum.status).not.toBe(0);
	  expect(badChecksum.stderr).toContain('Historical standalone FFmpeg does not match its checksum');
	  const badComponent=runPublishedReleaseValidation(publishedManifestFixture(),undefined,{componentHash:'0'.repeat(64)});
	  expect(badComponent.status).not.toBe(0);
	  expect(badComponent.stderr).toContain('Historical standalone FFmpeg differs from fixed sealed component');
	});

	const malformedHistoricalManifestCases: Array<{name:string;mutate:(manifest:Record<string,unknown>)=>unknown;serialize?:(manifest:unknown)=>string}>=[
	  {name:'an array tag_name',mutate:(manifest)=>{manifest.tag_name=['v1.2.3'];}},
	  {name:'numeric draft false',mutate:(manifest)=>{manifest.draft=0;}},
	  {name:'numeric prerelease false',mutate:(manifest)=>{manifest.prerelease=0;}},
	  {name:'a non-array assets value',mutate:(manifest)=>{manifest.assets=(manifest.assets as unknown[])[0];}},
	  {name:'an array asset name',mutate:(manifest)=>{(manifest.assets as Record<string,unknown>[])[0]!.name=['gift-panel-windows-x64.exe'];}},
	  {name:'an array asset download URL',mutate:(manifest)=>{(manifest.assets as Record<string,unknown>[])[0]!.browser_download_url=['https://github.com/example/repository/releases/download/v1.2.3/gift-panel-windows-x64.exe'];}},
	  {name:'an array asset digest',mutate:(manifest)=>{const asset=(manifest.assets as Record<string,unknown>[])[0]!;asset.digest=[asset.digest];}},
	  {name:'a string asset size',mutate:(manifest)=>{const asset=(manifest.assets as Record<string,unknown>[])[0]!;asset.size=String(asset.size);}},
	  {name:'a decimal-form asset size',mutate:(manifest)=>manifest,serialize:(manifest)=>{const serialized=JSON.stringify(manifest);const size=((manifest as Record<string,unknown>).assets as Record<string,unknown>[])[0]!.size;return serialized.replace(`"size":${size}`,`"size":${size}.0`);}},
	  {name:'an array root',mutate:(manifest)=>[manifest]},
	  {name:'an unknown root property',mutate:(manifest)=>{manifest.extra=true;}},
	];
	it.each(malformedHistoricalManifestCases)('rejects $name in historical fallback metadata',({mutate,serialize})=>{
	  const manifest=publishedManifestFixture();const replacement=mutate(manifest);const candidate=replacement??manifest;
	  const result=runPublishedReleaseValidation(candidate,serialize?.(candidate));
	  expect(result.status).not.toBe(0);
	  expect(result.stderr).toContain('Historical fallback update manifest');
	});

	it('rejects unexpected candidate closure entries and rechecks the exact reviewed tag before both mutations', () => {
	  const jobs = releaseWorkflow().workflow.jobs as unknown as Record<string, WorkflowJob>;
	  const prepare = semanticCommands(jobs['prepare-candidate']).join('\n');
	  const publishSteps = jobSteps(jobs['publish-candidate']);
	  const revalidate = publishSteps[stepIndex(publishSteps, 'Revalidate reviewed stable candidate')];
	  const beforeDraft = publishSteps[stepIndex(publishSteps, 'Recheck reviewed tag before stable draft')];
	  const beforePublish = publishSteps[stepIndex(publishSteps, 'Recheck reviewed tag before stable publication')];
	  expect(prepare).toContain('Candidate closure');
	  expect(revalidate?.run).toContain('unexpected candidate file');
	  for (const step of [beforeDraft, beforePublish]) {
		expect(step?.run).toContain('/git/ref/tags/');
		expect(step?.run).toContain('/git/tags/');
		expect(step?.run).toContain('STABLE_REVIEWED_TAG_OBJECT_SHA');
		expect(step?.run).toContain('STABLE_CANDIDATE_COMMIT_SHA');
	  }
	});

	it('publishes expected-name hard links for stable and bridge without gh label rewriting', () => {
	  const jobs = releaseWorkflow().workflow.jobs as unknown as Record<string, WorkflowJob>;
	  const stable = jobSteps(jobs['publish-candidate']);
	  const stableLink = stable[stepIndex(stable, 'Create expected-name stable asset')];
	  const stableDraft = stable[stepIndex(stable, 'Create immutable-shaped stable draft')];
	  expect(stableLink?.run).toContain('link-sealed-executable');
	  expect(stableLink?.run).toContain('gift-panel-windows-x64.exe');
	  expect(stableDraft?.run).toContain('gift-panel-windows-x64.exe');
	  expect(stableDraft?.run).not.toContain('#gift-panel-windows-x64.exe');

	  const bridge = jobSteps(bridgeReleaseWorkflow().jobs?.['bridge-release']);
	  const bridgeLink = bridge[stepIndex(bridge, 'Create expected-name bridge asset')];
	  const bridgeDraft = bridge[stepIndex(bridge, 'Create immutable-shaped bridge draft')];
	  expect(bridgeLink?.run).toContain('link-sealed-executable');
	  expect(bridgeDraft?.run).not.toContain('#gift-panel-windows-x64.exe');
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
	expect(sign?.run).toContain('dist/gift-panel-windows-x64.unsigned.exe dist/gift-panel-windows-x64.signed-candidate.exe');
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
    expect(inspect?.run).toContain('identity --kind fixed --tool-root $env:RELEASE_TOOL_ROOT | ConvertFrom-Json');
    expect(inspect?.run).toContain('--tool-root $env:RELEASE_TOOL_ROOT');
    expect(inspect?.run).toContain('FFMPEG_COMPONENT_EXISTS');
    expect(verifyBefore?.run).toContain('RELEASE_TOOL_ROOT/scripts/ffmpeg-component-assets.mjs');
    expect(verifyBefore?.run).toContain('verify-metadata --tool-root');
    expect(verifyBefore?.run).toContain('--metadata dist/bridge-ffmpeg-component-release.json');
	expect(verifyBefore?.run).toContain('install --kind fixed --tool-root');
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
	expect(steps[mirrorClosure]?.run).toContain('$env:RELEASE_CLOSURE_PATH');
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
	  signedFileSha256: createHash('sha256').update(Buffer.from('rushrush-signed-executable')).digest('hex'),
	  signedFileSize: Buffer.from('rushrush-signed-executable').length,
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
