import { readFileSync } from 'node:fs';
import { expect, it } from 'vitest';
import { validateUIParityRequirements } from '../scripts/validate-ui-parity-requirements.mjs';

const requirementsPath = new URL('../acceptance/exe-hosted-ui/requirements.json', import.meta.url);
const expectedCompare = ['structure', 'hierarchy', 'spacing', 'controls', 'states', 'responsive', 'interactions'];

function readContract(): unknown {
  return JSON.parse(readFileSync(requirementsPath, 'utf8'));
}

function mutate(change: (value: any) => void): unknown {
  const value = structuredClone(readContract()) as any;
  change(value);
  return value;
}

it('requires all workspaces, states, comparisons, and viewports', () => {
  const contract = validateUIParityRequirements(readContract());
  expect(contract.viewports.map((item) => item.id)).toEqual(['desktop-1440x900', 'narrow-1024x768', 'mobile-390x844']);
  expect(contract.features.map((item) => item.id)).toEqual(['overview', 'attributes', 'activities', 'gift-targets', 'obs', 'analytics']);
  for (const feature of contract.features) {
    expect(feature.states).toEqual(expect.arrayContaining(['empty', 'populated', 'loading', 'error']));
    expect(feature.compare).toEqual(expectedCompare);
  }
});

it('reconstructs an allowlisted result rather than retaining caller properties', () => {
  const source = readContract() as any;
  const contract = validateUIParityRequirements(source);
  source.features[0].states.length = 0;

  expect(contract.features[0]).toEqual({
    id: 'overview',
    states: ['empty', 'populated', 'loading', 'error'],
    interactions: ['navigate', 'refresh', 'open-settings'],
    compare: expectedCompare,
  });
});

it.each([
  ['unknown keys', (value: any) => { value.extra = true; }],
  ['duplicate IDs', (value: any) => { value.features[1].id = 'overview'; }],
  ['missing viewports', (value: any) => { value.viewports.pop(); }],
  ['absolute paths', (value: any) => { value.capture = 'C:\\captures\\image.png'; }],
  ['localhost URLs', (value: any) => { value.capture = 'http://localhost:5173/image.png'; }],
  ['tokenized URLs', (value: any) => { value.capture = 'acceptance/exe-hosted-ui/captures/0.4.10/image.png?token=secret'; }],
  ['empty states', (value: any) => { value.features[0].states = []; }],
  ['shell-only comparisons', (value: any) => { value.features[0].compare = ['shell', 'header']; }],
])('rejects %s', (_name, change) => {
  expect(() => validateUIParityRequirements(mutate(change))).toThrow();
});
