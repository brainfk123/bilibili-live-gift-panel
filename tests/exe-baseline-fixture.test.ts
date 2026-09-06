import { readFileSync } from 'node:fs';
import { describe, expect, it } from 'vitest';
import { assertEmptyBaseline } from '../scripts/exe-baseline-fixture.mjs';

const fixture = () => JSON.parse(readFileSync(new URL('../acceptance/exe-hosted-ui/fixtures/empty-0.4.10.json', import.meta.url), 'utf8'));
describe('EXE capture fixture replacement guard', () => {
  it('accepts the empty baseline', () => expect(() => assertEmptyBaseline(fixture())).not.toThrow());
  it.each(['attributes','displayScenes','activities','giftKpiPanels','rules','timerRules','formulaPresets','giftCatalog','recentGifts','giftReceipts','log'])('refuses existing %s before seeding', key => {
    const state=fixture(); state[key]=[{id:'existing'}];
    expect(() => assertEmptyBaseline(state)).toThrow();
  });
  it.each(['roomId','stats','contributions','giftTargetProgress','simplePlay'])('refuses existing %s state', key => {
    const state=fixture(); state[key]=key==='roomId'?'123':{existing:1};
    expect(() => assertEmptyBaseline(state)).toThrow();
  });
  it('refuses existing crop presets', () => {
    const state=fixture(); state.settings.giftClipCrops={existing:{x:0}};
    expect(() => assertEmptyBaseline(state)).toThrow();
  });
});
