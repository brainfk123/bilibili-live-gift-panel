import { describe, expect, it } from 'vitest';
import { defaultState } from '../src/storage';
import { createOnlineMigration, onlineMigrationFilename } from '../src/migration';

describe('online migration exporter', () => {
  it('uses a deterministic versioned local migration filename', () => {
    expect(onlineMigrationFilename(new Date('2026-08-16T23:59:59.999Z'))).toBe('gift-panel-migration-v1-2026-08-16.json');
  });

  it('exports a detached allowlisted package without local, viewer, or remote-resource data', () => {
    const state = defaultState();
    state.roomId = '31567150';
    state.attributes = [{
      id: 'score', name: '积分', value: 42, unit: 'none', format: 'number', decimals: 0, suffix: '分',
      display: {
        variant: 'enum', title: '积分榜', valueMappings: [{ value: 42, label: '满分', color: '#ffffff', imageUrl: 'https://assets.example/mapping.png' }],
      },
    }];
    state.displayScenes = [{
      id: 'main', name: '主面板', attributeNames: ['积分'], layout: 'focus', themeId: 'neon',
      appearance: { themeId: 'neon', fontSize: 36, accentColor: '#abcdef', showConnection: false, align: 'left', panelOpacity: 71 },
    }];
    state.blindBoxDisplay = { themeId: 'pixel', fontSize: 28, accentColor: '#123456', showConnection: true, align: 'right', panelOpacity: 63, viewerSlots: 8 };
    state.giftKpiPanels = [{
      id: 'target-1', name: '目标', layout: 'grid',
      items: [{ giftId: 2, giftName: '目标礼物', imageUrl: 'https://assets.example/target.png', target: 10, received: 7, barStyle: 'progress' }],
      appearance: { themeId: 'glass', fontSize: 33, accentColor: '#654321', showConnection: false, align: 'center', panelOpacity: 44 },
    }];
    state.activities = [{
      id: 'activity-1', name: '活动', attributeNames: ['积分'], sceneId: 'main', status: 'active', resultMode: 'highest', gateRules: true,
      initialValues: { 积分: 1 }, milestones: [{ id: 'milestone-1', name: '达成', attributeName: '积分', comparison: 'gte', threshold: 40, action: 'settle', message: '完成', triggeredAt: 123, triggerValue: 42 }],
      giftTimeout: { seconds: 60, action: 'settle', lastGiftAt: 100, deadlineAt: 160 }, startedAt: 99, lockedAt: 101, settledAt: 102,
      result: { winnerAttributeName: '积分', values: { 积分: 42 } },
    }];
    state.rules = [{ id: 'rule-1', giftId: 1, attributeName: '积分', formulaName: '加分', condition: '积分>=0', formula: '积分+1', enabled: true, matchGiftIds: [2], minPrice: 1, cap: 100, dailyLimit: 10 }];
    state.timerRules = [{ id: 'timer-1', attributeName: '积分', formulaName: '每分钟', intervalSeconds: 60, condition: '积分>0', formula: '积分-1', enabled: true }];
    state.formulaPresets = [{ id: 'preset-1', name: '加分', context: 'gift', formula: '积分+1', sourceAttributeName: '积分' }];
    state.simplePlay = { version: 1, templateId: 'counter', templateVersion: 1, attributeId: 'score', parameters: { amount: 1 }, gifts: { add: [1, 2] }, overtimeGiftActions: [{ giftId: 2, operation: 'add', seconds: 10 }], managedFingerprint: 'safe-fingerprint' };
    state.giftCatalog = [
      { id: 1, name: '规则礼物', price: 100, coinType: 'gold', imgBasic: 'https://assets.example/rule.png', gif: 'https://assets.example/rule.gif', webp: 'https://assets.example/rule.webp', effectMp4: 'https://assets.example/rule.mp4', effectMp4Json: 'https://assets.example/rule.json', blindBoxParentId: 9, blindBoxParentName: '盲盒', blindBoxParentPrice: 200 },
      { id: 2, name: '目标礼物', price: 20, coinType: 'silver', imgBasic: 'https://assets.example/target.png' },
      { id: 3, name: '无关礼物', price: 30, coinType: 'gold', imgBasic: 'https://assets.example/unused.png' },
    ];
    state.recentGifts = [{ id: 4, name: '观众历史礼物', price: 40, coinType: 'gold', imgBasic: 'https://assets.example/history.png', lastReceived: 1, count: 1 }];
    state.stats = { today: { date: '2026-08-16', giftTotals: { 1: 9 }, ruleTriggers: { 'rule-1': 9 } } };
    state.log = [{ time: 1, giftId: 1, giftName: '规则礼物', num: 1, uname: 'viewer-name', senderUid: 123, attributeName: '积分', delta: 1, valueAfter: 42, ruleId: 'rule-1' }];
    state.giftReceipts = [{ id: 'receipt', time: 1, giftId: 1, giftName: '规则礼物', num: 1, price: 1, totalCoin: 1, coinType: 'gold', uname: 'viewer-name', senderUid: 123, effects: [] }];
    state.contributions = { viewers: [{ key: 'uid:123', uid: 123, uname: 'viewer-name', giftCount: 1, goldValue: 1, silverValue: 0, ruleTriggers: 1, attributeDeltas: { 积分: 1 }, blindBoxCount: 0, blindBoxCost: 0, blindBoxValue: 0, blindBoxProfit: 0, lastGiftAt: 1 }] };
    state.settings = { ...state.settings, theme: 'light', fontSize: 31, accentColor: '#ff00ff', align: 'right', panelOpacity: 62, showConnection: false, autoUpdate: false, tutorialCompletedLessons: ['room'], giftClipCrops: { clip: { x: 0, y: 0, width: 1, height: 1 } } };
    (state as typeof state & { cookie: string }).cookie = 'secret-cookie';

    const migration = createOnlineMigration(state, '0.4.4', new Date('2026-08-16T00:00:00.000Z'));
    const text = JSON.stringify(migration);

    expect(migration).toMatchObject({
      kind: 'gift-panel-online-migration', migrationVersion: 1,
      source: { appVersion: '0.4.4', configSchemaVersion: 5 }, exportedAt: '2026-08-16T00:00:00.000Z',
      payload: {
        roomSuggestion: '31567150',
        definition: {
          appearance: { theme: 'light', fontSize: 31, accentColor: '#ff00ff', align: 'right', panelOpacity: 62, showConnection: false },
          attributes: [{ id: 'score', name: '积分', unit: 'none', display: { valueMappings: [{ value: 42, label: '满分', color: '#ffffff' }] } }],
          giftTargetPanels: [{ items: [{ giftId: 2, name: '目标礼物', target: 10, barStyle: 'progress' }] }],
          gifts: [{ id: 1, name: '规则礼物', price: 100, coinType: 'gold' }, { id: 2, name: '目标礼物', price: 20, coinType: 'silver' }],
        },
        runtime: {
          attributeValues: { score: 42 },
          giftTargetReceived: [{ panelId: 'target-1', giftId: 2, received: 7 }],
          activities: [{ id: 'activity-1', startedAtMillis: 99, lockedAtMillis: 101, settledAtMillis: 102, milestones: [{ id: 'milestone-1', triggeredAtMillis: 123, triggerValue: 42 }] }],
        },
      },
    });
    for (const forbiddenField of ['recentGifts', 'stats', 'log', 'giftReceipts', 'contributions', 'giftClipCrops', 'autoUpdate', 'tutorialCompletedLessons', 'cookie', 'senderUid', 'uname', 'imageUrl', 'imgBasic', 'gif', 'webp', 'effectMp4', 'effectMp4Json']) {
      expect(text).not.toContain(`\"${forbiddenField}\"`);
    }
    expect(text).not.toContain('https://');
    expect(migration.payload.definition.gifts.map((gift) => gift.id)).toEqual([1, 2]);

    migration.payload.definition.attributes[0]!.name = '已修改';
    migration.payload.runtime.attributeValues.score = 999;
    expect(state.attributes[0]!.name).toBe('积分');
    expect(state.attributes[0]!.value).toBe(42);
  });

  it('uses a null room suggestion when the desktop has no room', () => {
    const migration = createOnlineMigration(defaultState(), '0.4.4', new Date('2026-08-16T00:00:00.000Z'));

    expect(migration.payload.roomSuggestion).toBeNull();
  });
});
