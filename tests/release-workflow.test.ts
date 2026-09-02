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
  concurrency?: { group?: string; 'cancel-in-progress'?: boolean };
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

function publisherDiscoveryWorkflow(): PublisherRotationWorkflow {
  const source = readFileSync(new URL('../.github/workflows/publisher-discovery.yml', import.meta.url), 'utf8');
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
  omitDist?: boolean;
}) {
  const jobs = releaseWorkflow().workflow.jobs as unknown as Record<string, WorkflowJob>;
  const steps = jobSteps(jobs['prepare-candidate']);
  const step = steps[stepIndex(steps, 'Build canonical candidate changelog')];
  const root = mkdtempSync(join(tmpdir(), 'canonical-changelog-'));
  try {
    const tooling = join(root, 'tooling');
    mkdirSync(join(root, '.github'), { recursive: true });
    if (!options.omitDist) mkdirSync(join(root, 'dist'));
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

describe('release workflow supply-chain contract', () => {
	it('builds the reviewed inspector before sealing the downloaded FFmpeg closure', () => {
	  const jobs=releaseWorkflow().workflow.jobs as unknown as Record<string,WorkflowJob>;
	  const steps=jobSteps(jobs['prepare-candidate']);
	  const step=steps[stepIndex(steps,'Download reviewed signed FFmpeg closure')];
	  const root=mkdtempSync(join(tmpdir(),'stable-ffmpeg-closure-'));
	  try{
		const manifest=Buffer.from('reviewed-manifest-fixture');
		const names=['ffmpeg.zip','manifest.json','gift-clip-test-tools.zip','ffmpeg-9.0.tar.xz','ffmpeg-9.0.tar.xz.asc','ffmpeg-build-config.txt','ffmpeg-component-gate.txt','toolchain-lock.json','NOTICE.md','COPYING.LGPLv2.1','SHA256SUMS.txt'];
		const release={draft:false,prerelease:false,tag_name:'ffmpeg-component-v2-'+ 'a'.repeat(64),assets:names.map((name)=>({name}))};
		const script=`$ErrorActionPreference='Stop'\nfunction Invoke-RestMethod { $env:RELEASE_JSON|ConvertFrom-Json }\nfunction gh { if($args[0] -eq 'release'){ $index=[Array]::IndexOf($args,'--pattern');$name=$args[$index+1];$path=Join-Path 'dist/ffmpeg-component' $name;if($name -eq 'manifest.json'){[IO.File]::WriteAllBytes($path,[Convert]::FromBase64String($env:MANIFEST_B64))}else{Set-Content -NoNewline -Encoding ascii $path 'fixture'} };$global:LASTEXITCODE=0 }\nfunction go { $index=[Array]::IndexOf($args,'-o');$path=$args[$index+1];Set-Content -NoNewline -Encoding ascii $path 'inspector';$global:LASTEXITCODE=0 }\nfunction node { if($args -contains 'install'){if(-not $env:AUTHENTICODE_INSPECTOR_PATH){throw 'missing reviewed inspector'};$index=[Array]::IndexOf($args,'--sealed-output');$out=$args[$index+1];New-Item -ItemType Directory -Path $out -ErrorAction Stop|Out-Null;Copy-Item dist/ffmpeg-component/manifest.json (Join-Path $out 'manifest.json');Set-Content -NoNewline -Encoding ascii (Join-Path $out 'ffmpeg.zip') 'fixture'};$global:LASTEXITCODE=0 }\n${step?.run??''}`;
		const execution=spawnSync('pwsh',['-NoLogo','-NoProfile','-NonInteractive','-File','-'],{cwd:root,encoding:'utf8',env:{...process.env,GITHUB_REPOSITORY:'example/repository',GH_TOKEN:'test',RELEASE_JSON:JSON.stringify(release),MANIFEST_B64:manifest.toString('base64'),RUNNER_TEMP:root,RELEASE_TOOL_ROOT:root,STABLE_FFMPEG_COMPONENT_TAG:release.tag_name,STABLE_FFMPEG_COMPONENT_MANIFEST_SHA256:createHash('sha256').update(manifest).digest('hex')},input:script});
		expect(execution.status,execution.stderr).toBe(0);
		expect(existsSync(join(root,'dist','release-ffmpeg-sealed','manifest.json'))).toBe(true);
	  }finally{rmSync(root,{recursive:true,force:true});}
	});

	it('hands an unsigned closed candidate to a fresh protected stable signing runner', () => {
	  const jobs = releaseWorkflow().workflow.jobs as unknown as Record<string, WorkflowJob>;
	  const build = jobs['prepare-candidate'];
	  const sign = jobs['sign-candidate'];
	  expect(build?.environment).toBeUndefined();
	  expect(build?.permissions).toEqual({ contents: 'read' });
	  expect(semanticCommands(build).join('\n')).not.toMatch(/EVSIGN_|sign-evsign|signed-candidate/i);
	  expect(sign?.environment).toBe('stable-sign');
	  expect(sign?.permissions).toEqual({ contents: 'read' });
	  expect(sign?.needs).toBe('prepare-candidate');
	  const steps = jobSteps(sign);
	  expect(stepIndex(steps, 'Download exact unsigned stable handoff')).toBeLessThan(stepIndex(steps, 'Check out reviewed stable signing tools'));
	  expect(stepIndex(steps, 'Validate unsigned stable handoff')).toBeLessThan(stepIndex(steps, 'Check out reviewed stable signing tools'));
	  const commands = semanticCommands(sign).join('\n');
	  expect(commands).toContain('sign-evsign');
	  expect(commands).not.toMatch(/working-directory: source|npm (?:ci|test|run)|build:exe|refs\/tags\/\$\{\{/i);
	});

	it('splits stable classification, historical verification, candidate preparation, and publication into isolated capabilities', () => {
	  const { workflow } = releaseWorkflow();
	  const jobs = workflow.jobs as unknown as Record<string, WorkflowJob>;
	  expect(workflow.permissions).toEqual({ contents: 'read' });
	  expect(releaseWorkflow().source).not.toContain('STABLE_TRUST_ROOT_KEY_ID');
	  expect(Object.keys(jobs)).toEqual(['classify', 'historical-verify', 'prepare-candidate', 'sign-candidate', 'publish-candidate']);
	  expect(jobs.classify?.permissions).toEqual({ contents: 'read' });
	  expect(jobs['historical-verify']?.permissions).toEqual({ contents: 'read' });
	  expect(jobs['historical-verify']?.environment).toBeUndefined();
	  expect(jobs['prepare-candidate']?.permissions).toEqual({ contents: 'read' });
	  expect(jobs['prepare-candidate']?.environment).toBeUndefined();
	  expect(jobs['sign-candidate']?.environment).toBe('stable-sign');
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
	  const signSteps=jobSteps(jobs['sign-candidate']);
	  const sign=signSteps[stepIndex(signSteps,'Sign stable executable on protected runner')];
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
	  const signSteps=jobSteps(jobs['sign-candidate']);
	  const prepare=signSteps[stepIndex(signSteps,'Seal and close signed stable candidate')];
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

	it('creates the canonical changelog output directory on a clean candidate checkout', () => {
	  const execution=runCandidateChangelogStep({target:[changelogRelease('0.4.12')],omitDist:true});
	  expect(execution.result.status,execution.result.stderr).toBe(0);
	  expect(execution.output).toBeDefined();
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

	it('executes the stable unsigned handoff gate and rejects PATH or tool poisoning before checkout', () => {
	  const jobs=releaseWorkflow().workflow.jobs as unknown as Record<string,WorkflowJob>;
	  const steps=jobSteps(jobs['sign-candidate']);
	  const validation=steps[stepIndex(steps,'Validate unsigned stable handoff')];
	  expect(stepIndex(steps,'Validate unsigned stable handoff')).toBeLessThan(stepIndex(steps,'Check out reviewed stable signing tools'));
	  const root=mkdtempSync(join(tmpdir(),'stable-unsigned-poison-'));
	  try{
		const handoff=join(root,'handoff');mkdirSync(handoff);
		const unsigned=Buffer.from('unsigned-stable-fixture');const digest=createHash('sha256').update(unsigned).digest('hex');
		writeFileSync(join(handoff,digest+'.exe'),unsigned);
		for(const name of ['root-spki.der','bootstrap-policy.json','gift-panel-changelog.json','ffmpeg.zip','manifest.json','ffmpeg-windows-x64.exe','ffmpeg-9.0.tar.xz','ffmpeg-9.0.tar.xz.asc','ffmpeg-build-config.txt','ffmpeg-component-gate.txt','toolchain-lock.json','NOTICE.md','COPYING.LGPLv2.1'])writeFileSync(join(handoff,name),name);
		writeFileSync(join(handoff,'handoff.json'),JSON.stringify({schemaVersion:1,tag:'v0.4.12',version:'0.4.12',commit:'a'.repeat(40),unsignedSha256:digest,unsignedSize:unsigned.length,changelogSha256:'b'.repeat(64),changelogHistorySha256:'c'.repeat(64),toolingCommit:'d'.repeat(40)}));
		const githubEnv=join(root,'fresh-github-env');const poison=join(root,'poison');mkdirSync(poison);
		const execute=()=>spawnSync('pwsh',['-NoLogo','-NoProfile','-NonInteractive','-File','-'],{cwd:root,encoding:'utf8',env:{...process.env,PATH:poison+';'+(process.env.PATH??''),GITHUB_ENV:githubEnv,EXPECTED_UNSIGNED_SHA256:digest,EXPECTED_UNSIGNED_SIZE:String(unsigned.length),STABLE_REVIEWED_COMMIT_SHA:'a'.repeat(40)},input:"$ErrorActionPreference='Stop'\n"+(validation?.run??'')});
		writeFileSync(join(handoff,'PATH.cmd'),'target controlled');const poisoned=execute();expect(poisoned.status!==0||poisoned.stderr.includes('handoff closure'),poisoned.stderr).toBe(true);
		rmSync(join(handoff,'PATH.cmd'));const valid=execute();expect(valid.status,valid.stderr).toBe(0);expect(readFileSync(githubEnv,'utf8')).not.toContain(poison);
	  }finally{rmSync(root,{recursive:true,force:true});}
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
	  const prepare = semanticCommands(jobs['sign-candidate']).join('\n');
	  const publishSteps = jobSteps(jobs['publish-candidate']);
	  const revalidate = publishSteps[stepIndex(publishSteps, 'Revalidate reviewed stable candidate')];
	  const beforeDraft = publishSteps[stepIndex(publishSteps, 'Recheck reviewed tag before stable draft')];
	  const beforePublish = publishSteps[stepIndex(publishSteps, 'Recheck reviewed tag before stable publication')];
	  expect(prepare).toContain('Signed stable candidate closure');
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

	  const bridgeSign = jobSteps(bridgeReleaseWorkflow().jobs?.['bridge-sign']);
	  const bridgeLink = bridgeSign[stepIndex(bridgeSign, 'Seal and close signed bridge candidate')];
	  const bridgePublish = jobSteps(bridgeReleaseWorkflow().jobs?.['bridge-publish']);
	  const bridgeDraft = bridgePublish[stepIndex(bridgePublish, 'Create immutable-shaped bridge draft')];
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

  it('separates validation, protected private-key signing, immutable publication, and optional discovery advancement', () => {
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

  it('keeps the signing root isolated from COS publication and grants no OIDC capability', () => {
    const workflow = publisherRotationWorkflow();
    const jobs = workflow.jobs ?? {};
    expect(workflow.permissions).toEqual({ contents: 'read' });
    expect(jobs['validate-candidate']?.permissions).toEqual({ contents: 'read' });
    expect(jobs['sign-policy']?.permissions).toEqual({ contents: 'read' });
    expect(jobs['publish-immutable']?.permissions).toEqual({ contents: 'write' });
    expect(jobs['advance-discovery']?.permissions).toEqual({ contents: 'write' });

    const signingSteps = jobSteps(jobs['sign-policy']);
    const sign = signingSteps.find((step) => step.name === 'Sign publisher policy');
    expect(sign?.env).toMatchObject({
      PUBLISHER_ROTATION_PRIVATE_KEY_PEM: '${{ secrets.PUBLISHER_ROTATION_PRIVATE_KEY_PEM }}',
      PUBLISHER_ROTATION_KEY_ID: '${{ vars.PUBLISHER_ROTATION_KEY_ID }}',
      PUBLISHER_ROTATION_REQUEST_ID: 'github-run:${{ github.run_id }}:attempt:${{ github.run_attempt }}',
    });
    expect(sign?.env).not.toHaveProperty('GH_TOKEN');
    expect(sign?.env).not.toHaveProperty('TENCENTCLOUD_SECRET_ID');
    expect(sign?.env).not.toHaveProperty('TENCENTCLOUD_SECRET_KEY');
    expect(sign?.env).not.toHaveProperty('TENCENTCLOUD_SESSION_TOKEN');
    expect(Object.keys(sign?.env ?? {}).some((name) => name.startsWith('COS_'))).toBe(false);
    expect(semanticCommands(jobs['sign-policy']).join('\n')).not.toContain('exchange-session');

    for (const jobName of ['publish-immutable', 'advance-discovery']) {
      const publish = jobSteps(jobs[jobName]).find((step) => step.env?.COS_BUCKET !== undefined);
      expect(publish?.env).toMatchObject({
        TENCENTCLOUD_SECRET_ID: '${{ secrets.TENCENT_CLOUD_SECRET_ID }}',
        TENCENTCLOUD_SECRET_KEY: '${{ secrets.TENCENT_CLOUD_SECRET_KEY }}',
      });
      expect(publish?.env).not.toHaveProperty('PUBLISHER_ROTATION_PRIVATE_KEY_PEM');
      expect(publish?.env).not.toHaveProperty('TENCENTCLOUD_SESSION_TOKEN');
      expect(semanticCommands(jobs[jobName]).join('\n')).not.toContain('exchange-session');
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

  it('keeps the ordinary release workflow unable to sign publisher policy or modify the legacy pointer', () => {
    const { release } = releaseWorkflow();
    const commands = semanticCommands(release as WorkflowJob);
    expect(commands.some((command) => /trustpolicy.*\bsign\b/i.test(command))).toBe(false);
    expect(commands.some((command) => /trustpolicy.*\bsign\b|legacy-rushrush/i.test(command))).toBe(false);
    expect(release?.env ?? {}).not.toHaveProperty('PUBLISHER_ROTATION_PRIVATE_KEY_PEM');
  });

  it('requires reviewed public-root configuration instead of embedding a production digest', () => {
    const workflow = publisherRotationWorkflow();
    const allSteps = Object.values(workflow.jobs ?? {}).flatMap(jobSteps);
    const signingStep = jobSteps(workflow.jobs?.['sign-policy']).find((step) => step.name === 'Sign publisher policy');
    expect(signingStep?.env?.PUBLISHER_ROTATION_SPKI_SHA256).toBe('${{ vars.PUBLISHER_ROTATION_SPKI_SHA256 }}');
    const verificationSteps = allSteps.filter((step) => step.env?.PUBLISHER_ROTATION_SPKI_PATH !== undefined);
    expect(verificationSteps.length).toBeGreaterThan(0);
    for (const step of verificationSteps) {
      expect(step.env?.PUBLISHER_ROTATION_SPKI_SHA256).toBe('${{ vars.PUBLISHER_ROTATION_SPKI_SHA256 }}');
      expect(step.env?.PUBLISHER_ROTATION_SPKI_PATH).toBe('${{ vars.PUBLISHER_ROTATION_SPKI_PATH }}');
    }
  });
});

describe('publisher discovery recovery workflow contract', () => {
  it('resumes from one exact immutable epoch without signing a new policy', () => {
    const workflow = publisherDiscoveryWorkflow();
    expect(Object.keys(workflow.on ?? {})).toEqual(['workflow_dispatch']);
    expect(workflow.on?.workflow_dispatch?.inputs).toEqual({
      candidate_epoch: expect.objectContaining({ required: true, type: 'number' }),
      expected_previous_epoch: expect.objectContaining({ required: true, type: 'number' }),
    });
    expect(workflow.permissions).toEqual({ contents: 'read' });
    expect(workflow.concurrency).toEqual({ group: 'publisher-policy-rotation', 'cancel-in-progress': false });
    expect(Object.keys(workflow.jobs ?? {})).toEqual(['advance-existing']);

    const job = workflow.jobs?.['advance-existing'];
    expect(job?.environment).toBe('publisher-rotation');
    expect(job?.permissions).toEqual({ contents: 'write' });
    const commands = semanticCommands(job).join('\n');
    expect(commands).not.toMatch(/trustpolicy.*\bsign\b|PUBLISHER_ROTATION_PRIVATE_KEY_PEM/);
  });

  it('downloads the exact three-asset release into a verified local bundle before advancing both pointers', () => {
    const steps = jobSteps(publisherDiscoveryWorkflow().jobs?.['advance-existing']);
    const fetch = steps[stepIndex(steps, 'Fetch immutable publisher bundle')];
    for (const name of [
      'gift-panel-publisher-policy.json', 'gift-panel-publisher-policy.audit.json', 'gift-panel-publisher-policy.commit.json',
      'policy.json', 'audit.json', 'commit.json',
    ]) expect(fetch?.run).toContain(name);
    expect(fetch?.run).toContain('scripts/bounded-github-asset.mjs');
    expect(fetch?.run).toContain('--content-type application/octet-stream');
    expect(fetch?.run).not.toContain('--content-type application/json');
    expect(fetch?.run).toContain('import-bundle');
    expect(fetch?.env).toEqual({
      GH_TOKEN: '${{ github.token }}',
      PUBLISHER_CANDIDATE_EPOCH: '${{ inputs.candidate_epoch }}',
    });

    const advance = steps[stepIndex(steps, 'Advance discovery from immutable bundle')];
    expect(advance?.env).toMatchObject({
      GH_TOKEN: '${{ github.token }}',
      TENCENTCLOUD_SECRET_ID: '${{ secrets.TENCENT_CLOUD_SECRET_ID }}',
      TENCENTCLOUD_SECRET_KEY: '${{ secrets.TENCENT_CLOUD_SECRET_KEY }}',
      PUBLISHER_MODE: 'advance-discovery',
      PUBLISHER_EXPECTED_PREVIOUS_EPOCH: '${{ inputs.expected_previous_epoch }}',
      PUBLISHER_ADVANCE_DISCOVERY: 'true',
    });
    expect(advance?.env).not.toHaveProperty('PUBLISHER_ROTATION_PRIVATE_KEY_PEM');
    expect(advance?.run).toBe('node scripts/publish-trust-policy.mjs run');
  });
});

describe('exact RushRush bridge release workflow contract', () => {
  it('uses fresh build, protected signing, and signer-free publish runners', () => {
    const jobs=bridgeReleaseWorkflow().jobs as Record<string,WorkflowJob>;
    expect(Object.keys(jobs)).toEqual(['bridge-build','bridge-sign','bridge-publish']);
    expect(jobs['bridge-build']?.environment).toBeUndefined();
    expect(jobs['bridge-build']?.permissions).toEqual({contents:'read'});
    expect(semanticCommands(jobs['bridge-build']).join('\n')).not.toMatch(/EVSIGN_|sign-evsign|gh release (?:create|upload|edit)/i);
    const sign=jobSteps(jobs['bridge-sign']);
    expect(jobs['bridge-sign']?.environment).toBe('bridge-sign');
    expect(jobs['bridge-sign']?.permissions).toEqual({contents:'read'});
    expect(stepIndex(sign,'Download exact unsigned bridge handoff')).toBeLessThan(stepIndex(sign,'Check out reviewed bridge signing tools'));
    expect(stepIndex(sign,'Validate unsigned bridge handoff')).toBeLessThan(stepIndex(sign,'Check out reviewed bridge signing tools'));
    expect(semanticCommands(jobs['bridge-sign']).join('\n')).not.toMatch(/npm (?:ci|test|run)|build:exe|gh release (?:create|upload|edit)/i);
    expect(jobs['bridge-publish']?.environment).toBe('bridge-publish');
    expect(jobs['bridge-publish']?.permissions).toEqual({contents:'write','id-token':'write',attestations:'write'});
    expect(semanticCommands(jobs['bridge-publish']).join('\n')).not.toMatch(/EVSIGN_|sign-evsign|build:exe|npm (?:ci|test|run)/i);
  });

  it('is manual, exact-tagged, read-only until its dedicated publisher', () => {
    const workflow=bridgeReleaseWorkflow();const jobs=workflow.jobs as Record<string,WorkflowJob>;
    expect(Object.keys(workflow.on??{})).toEqual(['workflow_dispatch']);
    expect(workflow.permissions).toEqual({contents:'read'});
    expect(workflow.concurrency).toEqual({group:'gift-panel-bridge-v0.4.11','cancel-in-progress':false});
    expect(jobs['bridge-build']?.env).toEqual({BRIDGE_TAG:'v0.4.11'});
    const build=jobSteps(jobs['bridge-build']);
    expect(build[stepIndex(build,'Check out exact bridge tag')]?.with).toMatchObject({ref:'refs/tags/v0.4.11','persist-credentials':false});
    expect(stepIndex(build,'Build reviewed bridge security tools')).toBeLessThan(stepIndex(build,'Check out exact bridge tag'));
  });

  it('maps the Task9 three-asset Release into a higher exact-hash authorization bundle', () => {
    const jobs=bridgeReleaseWorkflow().jobs as Record<string,WorkflowJob>;const build=jobSteps(jobs['bridge-build']);
    const fetch=build[stepIndex(build,'Fetch immutable production trust binding')]?.run??'';
    for(const name of ['gift-panel-publisher-policy.json','gift-panel-publisher-policy.audit.json','gift-panel-publisher-policy.commit.json','policy.json','audit.json','commit.json'])expect(fetch).toContain(name);
    expect(fetch).toContain('--content-type application/octet-stream');expect(fetch).not.toContain('--content-type application/json');
    expect(fetch).toContain('import-bundle');expect(fetch).toContain('verify-bundle');
    expect(fetch).toContain('BRIDGE_AUTHORIZATION_POLICY_EPOCH');expect(fetch).toContain('BRIDGE_BOOTSTRAP_POLICY_EPOCH');
    expect(fetch).toContain('authorization-evidence.json');expect(fetch).not.toContain('Bootstrap policy bytes do not match');
    const readiness=build[stepIndex(build,'Verify reviewed bridge readiness')]?.run??'';
    for(const flag of ['--bootstrap-policy','--authorization-policy','--authorization-evidence'])expect(readiness).toContain(flag);
  });

  it('executes target code only in the unprivileged build job before unsigned handoff', () => {
    const jobs=bridgeReleaseWorkflow().jobs as Record<string,WorkflowJob>;const build=jobSteps(jobs['bridge-build']);
    expect(stepIndex(build,'Run repository tests')).toBeLessThan(stepIndex(build,'Build bridge executable'));
    expect(stepIndex(build,'Build bridge executable')).toBeLessThan(stepIndex(build,'Prepare closed unsigned bridge handoff'));
    expect(stepIndex(build,'Prepare closed unsigned bridge handoff')).toBeLessThan(stepIndex(build,'Upload exact unsigned bridge handoff'));
    const commands=semanticCommands(jobs['bridge-build']).join('\n');
    expect(commands).toContain('npm test');expect(commands).toContain('go test');expect(commands).toContain('npm run build:exe');
    expect(commands).not.toMatch(/secrets\.|EVSIGN_/i);
  });

  it('final-inspects embedded bootstrap and external authorization on the protected runner', () => {
    const jobs=bridgeReleaseWorkflow().jobs as Record<string,WorkflowJob>;const sign=jobSteps(jobs['bridge-sign']);
    const final=sign[stepIndex(sign,'Seal and close signed bridge candidate')]?.run??'';
    for(const value of ['verify-artifact','--bootstrap-policy','--authorization-policy','--authorization-policy-epoch','--stable-artifact','authorizationPolicyEpoch -le','link-sealed-executable'])expect(final).toContain(value);
    expect(final).not.toContain('npm');
    const secretStep=sign[stepIndex(sign,'Sign RushRush bridge executable on protected runner')];
    expect(Object.keys(secretStep?.env??{}).sort()).toEqual(['EVSIGN_BRIDGE_CERTIFICATE','EVSIGN_BRIDGE_PUBLISHER_IDENTITY','EVSIGN_KEY','EVSIGN_PASSWORD']);
    expect(JSON.stringify(jobs['bridge-build'])).not.toMatch(/EVSIGN_|secrets\./);
    expect(JSON.stringify(jobs['bridge-publish'])).not.toMatch(/EVSIGN_|secrets\./);
  });

  it('executes the handoff gate and rejects PATH, GITHUB_ENV, or tool poisoning', () => {
    const jobs=bridgeReleaseWorkflow().jobs as Record<string,WorkflowJob>;const steps=jobSteps(jobs['bridge-sign']);
    const gate=steps[stepIndex(steps,'Validate unsigned bridge handoff')];const root=mkdtempSync(join(tmpdir(),'bridge-poison-'));
    try{
      const handoff=join(root,'handoff');const bundle=join(handoff,'readiness','private-bundle','bundle');mkdirSync(bundle,{recursive:true});
      const unsigned=Buffer.from('unsigned-bridge');const digest=createHash('sha256').update(unsigned).digest('hex');writeFileSync(join(handoff,digest+'.exe'),unsigned);
      for(const name of ['ffmpeg.zip','manifest.json','ffmpeg-windows-x64.exe','ffmpeg-component-manifest.json','gift-panel-changelog.json'])writeFileSync(join(handoff,name),name);
      for(const name of ['root-spki.der','bootstrap-policy.json','stable-artifact.exe','readiness.json','authorization-evidence.json','policy-release.json','stable-release.json','observation-evidence.json','trust-attestation.json','verified-bundle.json'])writeFileSync(join(handoff,'readiness',name),name);
      for(const name of ['policy.json','audit.json','commit.json'])writeFileSync(join(bundle,name),name);
      writeFileSync(join(handoff,'handoff.json'),JSON.stringify({schemaVersion:1,tag:'v0.4.11',version:'0.4.11',commit:'a'.repeat(40),unsignedSha256:digest,unsignedSize:unsigned.length,rootSpkiSha256:'b'.repeat(64),bootstrapPolicySha256:'c'.repeat(64),bootstrapPolicyEpoch:1,authorizationPolicySha256:'d'.repeat(64),authorizationPolicyEpoch:2}));
      const githubEnv=join(root,'fresh-env');const poison=join(root,'poison');mkdirSync(poison);
      const execute=()=>spawnSync('pwsh',['-NoLogo','-NoProfile','-NonInteractive','-File','-'],{cwd:root,encoding:'utf8',env:{...process.env,PATH:poison+';'+(process.env.PATH??''),GITHUB_ENV:githubEnv,EXPECTED_UNSIGNED_SHA256:digest,EXPECTED_UNSIGNED_SIZE:String(unsigned.length),BRIDGE_REVIEWED_COMMIT_SHA:'a'.repeat(40)},input:"$ErrorActionPreference='Stop'\n"+(gate?.run??'')});
      writeFileSync(join(handoff,'GITHUB_ENV.cmd'),'target controlled');const poisoned=execute();expect(poisoned.status!==0||poisoned.stderr.includes('handoff closure'),poisoned.stderr).toBe(true);
      rmSync(join(handoff,'GITHUB_ENV.cmd'));const valid=execute();expect(valid.status,valid.stderr).toBe(0);expect(readFileSync(githubEnv,'utf8')).not.toContain(poison);
    }finally{rmSync(root,{recursive:true,force:true});}
  });

  it('publishes one exact eight-asset non-latest closure without signer capability', () => {
    const jobs=bridgeReleaseWorkflow().jobs as Record<string,WorkflowJob>;const publish=jobSteps(jobs['bridge-publish']);
    const validate=publish[stepIndex(publish,'Validate signed bridge publication handoff')]?.run??'';
    for(const name of ['gift-panel-windows-x64.exe','gift-panel-windows-x64.exe.sha256','gift-panel-update.json','ffmpeg-windows-x64.exe','gift-panel-changelog.json','ffmpeg-component-manifest.json','bridge-release-evidence.json','SHA256SUMS.txt'])expect(validate).toContain(name);
    expect(publish[stepIndex(publish,'Create immutable-shaped bridge draft')]?.run).toContain('--latest=false');
    expect(publish[stepIndex(publish,'Publish bridge as non-latest')]?.run).toContain('--latest=false');
    expect(semanticCommands(jobs['bridge-publish']).join('\n')).not.toMatch(/EVSIGN_|SignByAsymmetricKey|channels\/|COS_|--latest(?:\s|$)(?!false)/i);
  });

  it('rechecks the raw and peeled reviewed bridge tag in the signer-free publisher', () => {
    const jobs=bridgeReleaseWorkflow().jobs as Record<string,WorkflowJob>;const publish=jobSteps(jobs['bridge-publish']);
    const recheck=publish[stepIndex(publish,'Recheck reviewed bridge tag through GitHub API')];
    expect(recheck?.env).toMatchObject({BRIDGE_REVIEWED_COMMIT_SHA:'${{ vars.BRIDGE_REVIEWED_COMMIT_SHA }}',BRIDGE_REVIEWED_TAG_OBJECT_SHA:'${{ vars.BRIDGE_REVIEWED_TAG_OBJECT_SHA }}'});
    expect(recheck?.run).toContain('/git/tags/');
    expect(recheck?.run).toContain('BRIDGE_REVIEWED_COMMIT_SHA');
    expect(recheck?.run).toContain("object.type -ceq 'tag'");
  });

  it('pins every external Action and cannot mutate stable, legacy, COS, or KMS state', () => {
    const jobs=bridgeReleaseWorkflow().jobs as Record<string,WorkflowJob>;
    for(const job of Object.values(jobs))for(const step of jobSteps(job))if(step.uses)expect(step.uses).toMatch(/^[^@]+@[0-9a-f]{40}$/);
    expect(Object.values(jobs).flatMap(semanticCommands).join('\n')).not.toMatch(/channels\/stable|channels\/legacy-rushrush|SignByAsymmetricKey|TENCENTCLOUD_|COS_/i);
  });
});
