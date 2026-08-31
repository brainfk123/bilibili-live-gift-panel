import { readFileSync } from 'node:fs';
import { expect, it } from 'vitest';

it('keeps EXE retirement evidence-gated and initially unchecked', () => {
  const text = readFileSync(new URL('../docs/operations/exe-retirement-checklist.md', import.meta.url), 'utf8');
  for (const heading of ['Stage A: migration-development', 'Stage B: closed-pilot-and-voluntary-migration', 'Stage C: exe-feature-freeze', 'Stage D: maintenance-ended']) expect(text).toContain(heading);
  for (const gate of ['media parity', 'EXE-versus-Hosted screenshots', 'migration export', 'seven-day rollback', 'backup restore', 'real Bilibili connections', 'user notification']) expect(text).toContain(gate);
  expect(text).not.toMatch(/^- \[x\]/m);
  expect(text).toContain('CI success is not production acceptance');
});

it('links the retirement checklist to tracked authority and keeps migration compatibility explicit', () => {
  const text = readFileSync(new URL('../docs/operations/exe-retirement-checklist.md', import.meta.url), 'utf8');
  for (const link of [
    'hosted-pilot-checklist.md',
    '2026-08-31-mac-hosted-windows-compatibility-design.md',
    '2026-08-16-hosted-online-service-design.md',
    '2026-08-16-hosted-configuration-migration.md',
    '2026-08-16-hosted-operations-pilot.md',
  ]) expect(text).toContain(link);
  expect(text).toContain('TypeScript exporter v2');
  expect(text).toContain('Hosted decoder compatibility');
});
