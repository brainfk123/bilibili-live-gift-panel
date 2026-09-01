import { readFileSync } from 'node:fs';
import { expect, it } from 'vitest';

const text = readFileSync(new URL('../docs/development/mac-hosted-workflow.md', import.meta.url), 'utf8');

function section(heading: string): string {
  const start = text.indexOf(heading);
  expect(start, `missing section ${heading}`).toBeGreaterThanOrEqual(0);
  const next = text.indexOf('\n## ', start + heading.length);
  return text.slice(start, next < 0 ? undefined : next);
}

it('documents Hosted-first Mac development without claiming ARM is x64 acceptance', () => {
  for (const command of ['npm ci', 'npm test', 'npm run typecheck', 'npm run build:hosted']) {
    expect(text).toContain(command);
  }
  expect(text).toContain('交叉编译不等于 Windows 验收');
  expect(text).toContain('Windows 11 ARM');
  expect(text).toContain('不作为 x64 驱动、显卡或硬件编码证据');
  expect(text).toContain('5a0bbfb');
  expect(text).toContain('../../deploy/hosted/README.md');
  expect(text).toContain('更新 API 部署说明');
  expect(text).toContain('仅当 artifact 成功上传时才下载');
  expect(text).toContain('无 artifact 时');
  expect(text).toContain('../../deploy/update-api/README.md');
});

it('keeps the Mac daily loop on the final platform boundary in dependency order', () => {
  const daily = section('## Daily Hosted loop');
  const commands = [
    'npm run build:ui',
    'npm run prepare:go-assets',
    'npm run verify:go-linux-compile',
    'npm run build:hosted',
    'go -C goserver test -race -count=1 ./cmd/hosted ./internal/...',
    'go -C goserver vet ./cmd/hosted ./internal/...',
    'npm run test:update-api',
  ];
  for (const command of commands) expect(daily).toContain(command);
  for (let index = 1; index < commands.length; index += 1) {
    expect(daily.indexOf(commands[index - 1]!), `${commands[index - 1]} must precede ${commands[index]}`)
      .toBeLessThan(daily.indexOf(commands[index]!));
  }
  expect(daily).not.toMatch(/^go -C goserver test \.\/\.\.\.$/m);
});

it('requires Homebrew Bash before tests and a real Apple Silicon fresh-checkout acceptance', () => {
  const prerequisites = section('## Mac prerequisites');
  expect(prerequisites).toContain('brew install bash');
  expect(prerequisites).toContain('export BASH_BIN="$(brew --prefix)/bin/bash"');
  expect(text.indexOf('export BASH_BIN="$(brew --prefix)/bin/bash"')).toBeLessThan(text.indexOf('npm test'));

  const acceptance = section('## Apple Silicon fresh-checkout acceptance');
  expect(acceptance).toContain('arm64');
  expect(acceptance.match(/^- \[ \]/gm)?.length ?? 0).toBeGreaterThanOrEqual(6);
  expect(acceptance).toContain('external evidence');
  expect(acceptance).toContain('用户的 Apple Silicon Mac');
  expect(acceptance).toContain('不新增永久 macOS CI job');
});

it('requires complete manual evidence before protected release approval for desktop-high-risk changes', () => {
  const evidence = section('## desktop-high-risk release approval evidence');
  for (const field of [
    'PR/run URL',
    'Windows x64 artifact/evidence SHA-256',
    'smoke routes',
    'real GPU/OBS/driver evidence required',
    'evidence pointer',
    'approver and approval date',
    'required Environment reviewers remain enabled',
  ]) expect(evidence).toContain(field);
  expect(evidence).toContain('protected `release` Environment');
  expect(evidence).toContain('no release approval occurs without the evidence pointer');
});
