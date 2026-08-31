import { existsSync, readFileSync } from 'node:fs';
import { expect, it } from 'vitest';

const checklist = readFileSync(new URL('../docs/operations/exe-retirement-checklist.md', import.meta.url), 'utf8');
const readme = readFileSync(new URL('../README.md', import.meta.url), 'utf8');
const stages = ['A: migration-development', 'B: closed-pilot-and-voluntary-migration', 'C: exe-feature-freeze', 'D: maintenance-ended'];

function stageBlock(stage: string): string {
  const start = checklist.indexOf(`## Stage ${stage}`);
  const next = checklist.indexOf('\n## Stage ', start + 1);
  expect(start).toBeGreaterThanOrEqual(0);
  return checklist.slice(start, next < 0 ? undefined : next);
}

it('keeps every EXE retirement stage structurally evidence-gated and initially unchecked', () => {
  for (const stage of stages) {
    const block = stageBlock(stage);
    for (const heading of ['### Entry evidence', '### Windows policy after entry', '### Return to previous stage when']) expect(block).toContain(heading);
    expect(block).not.toMatch(/\[[xX]\]/);
  }
  expect(checklist).toContain('CI success is not production acceptance');
});

it('keeps Stage B gated on migration, parity, pilot, and rollback evidence', () => {
  const block = stageBlock('B: closed-pilot-and-voluntary-migration');
  for (const gate of ['TypeScript exporter v2', 'Hosted decoder compatibility', 'media parity', 'EXE-versus-Hosted screenshots', 'migration export', 'preview', 'apply', 'seven-day rollback']) expect(block).toContain(gate);
});

it('keeps Stage C and D support and provenance gates explicit', () => {
  const stageC = stageBlock('C: exe-feature-freeze');
  for (const gate of ['user notification', 'support policy']) expect(stageC).toContain(gate);
  const stageD = stageBlock('D: maintenance-ended');
  for (const gate of ['signed EXE', 'SHA-256', 'signer subject', 'source commit', 'build instructions', 'migration instructions', 'old migration-package policy']) expect(stageD).toContain(gate);
});

it('links only existing tracked authority documents', () => {
  const links = [...checklist.matchAll(/\[[^\]]+\]\(([^)]+)\)/g)].map((match) => match[1]);
  for (const target of links) expect(existsSync(new URL(target, new URL('../docs/operations/exe-retirement-checklist.md', import.meta.url)))).toBe(true);
  expect(checklist).not.toMatch(/2026-08-25-[^)\s]+\.md/);
});

it('keeps README compatibility links and Stage B prohibition', () => {
  expect(readme).toContain('[Mac 与 Windows 兼容性工作流](docs/development/mac-hosted-workflow.md)');
  expect(readme).toContain('[EXE 退役证据清单](docs/operations/exe-retirement-checklist.md)');
  expect(readme).toContain('不能仅凭 CI 进入 Stage B');
});
