import assert from 'node:assert/strict';

export function buildPopulatedShards(empty, spec) {
  assert.equal(spec.schema,1);
  assert.equal(empty.roomId,'');
  assert.equal(spec.viewers.length,2);
  assert.equal(spec.gifts.length,1);
  assert.ok(Number.isInteger(spec.receiptCount) && spec.receiptCount > 40 && spec.receiptCount <= 200);
  const timestamp=Date.parse(spec.timestamp);
  assert.ok(Number.isFinite(timestamp));
  const gift=spec.gifts[0];
  assert.equal(gift.imgBasic,'');
  const state=structuredClone(empty);
  state.attributes=structuredClone(spec.attributes);
  state.displayScenes=structuredClone(spec.displayScenes);
  state.activities=structuredClone(spec.activities);
  const {viewerSlots,...appearance}=state.blindBoxDisplay;
  state.giftKpiPanels=spec.giftTargets.map(panel=>({...structuredClone(panel),appearance:{...appearance}}));
  state.rules=spec.attributes.map((attribute,index)=>({id:`baseline-rule-${index}`,giftId:gift.id,attributeName:attribute.name,formula:`${attribute.name}+1`,enabled:false}));
  state.giftCatalog=structuredClone(spec.gifts);
  state.giftReceipts=Array.from({length:spec.receiptCount},(_,index)=>({
    id:`baseline-receipt-${String(index).padStart(3,'0')}`, time:timestamp+index*1000,
    giftId:gift.id,giftName:gift.name,num:1,price:gift.price,totalCoin:gift.price,coinType:gift.coinType,
    uname:spec.viewers[index%2].uname,
    effects:[{attributeName:spec.attributes[index%2].name,delta:1,valueAfter:Math.floor(index/2)+1,ruleId:`baseline-rule-${index%2}`}],
  })).sort((a,b)=>b.time-a.time);
  state.contributions={viewers:spec.viewers.map((viewer,index)=>{
    const count=state.giftReceipts.filter(row=>row.uname===viewer.uname).length;
    return {...viewer,giftCount:count,goldValue:count*gift.price,silverValue:0,ruleTriggers:count,attributeDeltas:{[spec.attributes[index].name]:count},blindBoxCount:0,blindBoxCost:0,blindBoxValue:0,blindBoxProfit:0,lastGiftAt:timestamp+(spec.receiptCount-1)*1000};
  }),updatedAt:timestamp+spec.receiptCount*1000};
  const giftTargetProgress=Object.fromEntries(state.giftKpiPanels.map(panel=>[panel.id,Object.fromEntries(panel.items.map(item=>[String(item.giftId),item.received]))]));
  const {giftCatalog,recentGifts,stats,log,giftReceipts,contributions,...configuration}=state;
  configuration.giftKpiPanels=configuration.giftKpiPanels.map(panel=>({...panel,items:panel.items.map(({received,...item})=>item)}));
  return {
    'config.json':{schemaVersion:12,...configuration},
    'cache.json':{schemaVersion:12,giftCatalog,recentGifts},
    'history.json':{schemaVersion:12,stats,contributions,giftTargetProgress,giftReceipts},
    'events.log':'',
  };
}

export function verifyLoadedShards(shards,actual) {
  const subset=(expected,value,path)=>{
    if(Array.isArray(expected)) {
      assert.ok(Array.isArray(value),`${path} must be an array`);
      assert.equal(value.length,expected.length,`${path} length mismatch`);
      expected.forEach((item,index)=>subset(item,value[index],`${path}[${index}]`));
    } else if(expected && typeof expected==='object') {
      for(const [key,item] of Object.entries(expected)) subset(item,value?.[key],`${path}.${key}`);
    } else assert.deepEqual(value,expected,`${path} mismatch`);
  };
  for(const name of ['config.json','cache.json','history.json']) {
    for(const [key,value] of Object.entries(shards[name])) {
      if(key==='schemaVersion'||key==='giftTargetProgress') continue;
      subset(value,actual[key],key);
    }
  }
  for(const panel of actual.giftKpiPanels) for(const item of panel.items) {
    assert.equal(item.received,shards['history.json'].giftTargetProgress[panel.id][String(item.giftId)],'Backend target progress mismatch');
  }
}
