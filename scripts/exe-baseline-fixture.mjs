import assert from 'node:assert/strict';

export function assertEmptyBaseline(config) {
  assert.equal(config.roomId, '', 'Refusing to capture a configured live room');
  for (const key of ['attributes','displayScenes','activities','giftKpiPanels','rules','timerRules','formulaPresets','giftCatalog','recentGifts','giftReceipts','log']) {
    assert.deepEqual(config[key], [], `Refusing to replace nonempty ${key}`);
  }
  assert.deepEqual(config.contributions, {viewers:[]}, 'Refusing to replace contribution data');
  assert.deepEqual(config.stats, {}, 'Refusing to replace statistics');
  assert.deepEqual(config.settings?.giftClipCrops, {}, 'Refusing to replace crop presets');
  if (config.giftTargetProgress !== undefined) assert.deepEqual(config.giftTargetProgress, {}, 'Refusing to replace target progress');
  assert.ok(!config.simplePlay, 'Refusing to replace simple gameplay');
}
