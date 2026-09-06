import {readFileSync} from 'node:fs';
import {describe,it,expect} from 'vitest';
import {buildPopulatedShards,verifyLoadedShards} from '../scripts/exe-populated-fixture.mjs';
const load=(name:string)=>JSON.parse(readFileSync(new URL(`../acceptance/exe-hosted-ui/fixtures/${name}-0.4.10.json`,import.meta.url),'utf8'));
describe('offline EXE populated fixture',()=>{
  it('separates backend-owned receipts and progress from editable configuration',()=>{
    const shards=buildPopulatedShards(load('empty'),load('populated'));
    expect(shards['config.json']).not.toHaveProperty('giftReceipts');
    expect(shards['config.json'].giftKpiPanels[0].items[0]).not.toHaveProperty('received');
    expect(shards['history.json'].giftTargetProgress).toEqual({'baseline-target':{'900000001':45}});
    expect(shards['history.json'].giftReceipts).toHaveLength(45);
    expect(shards['history.json'].contributions.viewers.map((v:any)=>v.giftCount)).toEqual([23,22]);
  });
  it('has no room, active timers, real account IDs, or external media URLs',()=>{
    const shards=buildPopulatedShards(load('empty'),load('populated'));
    expect(shards['config.json'].roomId).toBe('');
    expect(shards['config.json'].timerRules).toEqual([]);
    expect(shards['config.json'].settings.autoUpdate).toBe(false);
    expect(shards['history.json'].giftReceipts.every((r:any)=>!r.senderUid&&!r.avatar&&!r.animation)).toBe(true);
    expect(JSON.stringify(shards)).not.toMatch(/https?:|cookie|token|password/i);
  });
  it('uses fixed timestamps and validates the pagination boundary',()=>{
    const spec=load('populated');
    const rows=buildPopulatedShards(load('empty'),spec)['history.json'].giftReceipts;
    expect(rows[0].time).toBe(1788249644000);
    expect(rows[44].time).toBe(1788249600000);
    spec.receiptCount=40;
    expect(()=>buildPopulatedShards(load('empty'),spec)).toThrow();
  });
  it.each(['progress','scene','values','viewers','receipts'])('rejects a loaded fixture with corrupted %s',kind=>{
    const shards=buildPopulatedShards(load('empty'),load('populated'));
    const state=structuredClone({...shards['config.json'],...shards['cache.json'],...shards['history.json']});
    state.giftKpiPanels[0].items[0].received=45;
    expect(()=>verifyLoadedShards(shards,state)).not.toThrow();
    if(kind==='progress') state.giftKpiPanels[0].items[0].received=0;
    if(kind==='scene') state.displayScenes[0].layout='stack';
    if(kind==='values') state.attributes[0].value=0;
    if(kind==='viewers') state.contributions.viewers[0].giftCount=0;
    if(kind==='receipts') state.giftReceipts[0].uname='unexpected';
    expect(()=>verifyLoadedShards(shards,state)).toThrow();
  });
});
