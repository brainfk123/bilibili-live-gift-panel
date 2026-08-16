import { describe, expect, it, vi } from 'vitest';
import { defaultState } from '../src/storage';
import { createOnlineMigration, downloadOnlineMigration, onlineMigrationFilename } from '../src/migration';

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

  it('exports only validated template parameters from a simple play', () => {
    const state = defaultState();
    state.simplePlay = {
      version: 1, templateId: 'counter', templateVersion: 1, attributeId: 'score', gifts: { count: [1] }, managedFingerprint: 'safe',
      parameters: {
        name: '计数器', suffix: '次', amount: 2, cap: 9, broadcastMessage: '继续加油',
        maxSeconds: 60, cookie: 'synthetic-value', token: 'synthetic-value', imageUrl: 'https://assets.invalid/image.png', custom: true,
        nested: { value: 'synthetic-value' } as unknown as string,
      } as NonNullable<typeof state.simplePlay>['parameters'],
    };

    const migration = createOnlineMigration(state, '0.4.4', new Date(2026, 7, 16, 12));

    expect(migration.payload.definition.simplePlay?.parameters).toEqual({
      name: '计数器', suffix: '次', amount: 2, cap: 9, broadcastMessage: '继续加油',
    });
    expect(JSON.stringify(migration)).not.toContain('synthetic-value');
  });

  it.each([
    'https://assets.invalid/image.png',
    'data:text/plain,blocked',
    'file:///blocked',
    '\\\\server\\share',
    '//assets.invalid/image.png',
    'javascript:blocked()',
    'blob:blocked',
  ])('fails closed for unsafe simple-play text parameters: %s', (unsafeValue) => {
    const state = defaultState();
    state.simplePlay = {
      version: 1, templateId: 'counter', templateVersion: 1, attributeId: 'score', gifts: {}, managedFingerprint: 'safe',
      parameters: { name: unsafeValue, amount: 1 },
    };

    const migration = createOnlineMigration(state, '0.4.4', new Date(2026, 7, 16, 12));

    expect(migration.payload.definition.simplePlay).toBeUndefined();
    expect(JSON.stringify(migration)).not.toContain(unsafeValue);
  });

  it('exports only current-day non-negative counters for configured rules', () => {
    const state = defaultState();
    state.rules = [
      { id: 'rule-limited', giftId: 1, attributeName: '积分', formula: '积分+1', dailyLimit: 10 },
      { id: 'rule-zero', giftId: 2, attributeName: '积分', formula: '积分+1' },
    ];
    state.stats = {
      '2026-08-16': {
        date: '2026-08-16', giftTotals: { 1: 7 },
        ruleTriggers: { 'rule-limited': 9, 'rule-zero': 0, removed: 99, negative: -1, fractional: 1.5 },
      },
      '2026-08-15': {
        date: '2026-08-15', giftTotals: { 2: 4 }, ruleTriggers: { 'rule-limited': 8 },
      },
    };

    const migration = createOnlineMigration(state, '0.4.4', new Date(2026, 7, 16, 12));

    expect(migration.payload.runtime.ruleLimits).toEqual({
      localDate: '2026-08-16', appliedCounts: { 'rule-limited': 9, 'rule-zero': 0 },
    });
    expect(JSON.stringify(migration)).not.toContain('giftTotals');
    expect(JSON.stringify(migration)).not.toContain('removed');
  });

  it('revokes a local migration URL when the download click throws', () => {
    const revokeObjectURL = vi.fn();
    const click = vi.fn(() => { throw new Error('synthetic download failure'); });
    const adapter = {
      createBlob: vi.fn(() => ({} as Blob)),
      createObjectURL: vi.fn(() => 'blob:synthetic-migration'),
      click,
      revokeObjectURL,
    };

    expect(() => downloadOnlineMigration(defaultState(), '0.4.4', new Date(2026, 7, 16, 12), adapter)).toThrow('synthetic download failure');
    expect(revokeObjectURL).toHaveBeenCalledTimes(1);
    expect(revokeObjectURL).toHaveBeenLastCalledWith('blob:synthetic-migration');
  });

  it('keeps ordinary punctuation in validated text parameters', () => {
    const state = defaultState();
    state.simplePlay = {
      version: 1, templateId: 'counter', templateVersion: 1, attributeId: 'score', gifts: { count: [1] }, managedFingerprint: 'safe',
      parameters: { name: 'PK: 红队', suffix: '次', amount: 1, cap: 0, broadcastMessage: 'HP:剩余' },
    };

    const migration = createOnlineMigration(state, '0.4.4', new Date(2026, 7, 16, 12));

    expect(migration.payload.definition.simplePlay?.parameters).toMatchObject({ name: 'PK: 红队', broadcastMessage: 'HP:剩余' });
  });

  it('fails closed when a current template text parameter contains a resource reference', () => {
    const state = defaultState();
    state.simplePlay = {
      version: 1, templateId: 'counter', templateVersion: 1, attributeId: 'score', gifts: { count: [1] }, managedFingerprint: 'safe',
      parameters: { name: '计数器', suffix: '次', amount: 1, cap: 0, broadcastMessage: '正文 https://assets.invalid/image.png' },
    };

    const migration = createOnlineMigration(state, '0.4.4', new Date(2026, 7, 16, 12));

    expect(migration.payload.definition.simplePlay).toBeUndefined();
    expect(JSON.stringify(migration)).not.toContain('https://assets.invalid');
  });

  it('preserves the explicit overtime v1 legacy shape without v2 actions', () => {
    const state = defaultState();
    state.simplePlay = {
      version: 1, templateId: 'overtime', templateVersion: 1, attributeId: 'time', gifts: { overtime: [1] }, managedFingerprint: 'safe',
      parameters: { name: '加班时间', minutesPerYuan: 60, maxHours: 24, broadcastMessage: '继续加油' },
      overtimeGiftActions: [{ giftId: 1, operation: 'add', seconds: 60 }],
    };

    const migration = createOnlineMigration(state, '0.4.4', new Date(2026, 7, 16, 12));

    expect(migration.payload.definition.simplePlay).toMatchObject({
      templateId: 'overtime', templateVersion: 1,
      parameters: { name: '加班时间', minutesPerYuan: 60, maxHours: 24, broadcastMessage: '继续加油' },
      gifts: { overtime: [1] },
    });
    expect(migration.payload.definition.simplePlay).not.toHaveProperty('overtimeGiftActions');
  });

  it('omits unknown template versions rather than exporting a partial simple play', () => {
    const state = defaultState();
    state.simplePlay = {
      version: 1, templateId: 'counter', templateVersion: 2, attributeId: 'score', gifts: { count: [1] }, managedFingerprint: 'safe',
      parameters: { name: '计数器', suffix: '次', amount: 1, cap: 0, broadcastMessage: '继续加油' },
    };

    expect(createOnlineMigration(state, '0.4.4', new Date(2026, 7, 16, 12)).payload.definition.simplePlay).toBeUndefined();
  });

  it('exports only current template gift slots and removes non-overtime actions from references', () => {
    const state = defaultState();
    state.simplePlay = {
      version: 1, templateId: 'counter', templateVersion: 1, attributeId: 'score', managedFingerprint: 'safe',
      parameters: { name: '计数器', suffix: '次', amount: 1, cap: 0, broadcastMessage: '继续加油' },
      gifts: { count: [1, 1, 0, -2, 2.5], unknown: [9] },
      overtimeGiftActions: [{ giftId: 9, operation: 'add', seconds: 60 }],
    };
    state.giftCatalog = [
      { id: 1, name: '有效礼物', price: 1, coinType: 'gold', imgBasic: '' },
      { id: 9, name: '未知槽位礼物', price: 1, coinType: 'gold', imgBasic: '' },
    ];

    const migration = createOnlineMigration(state, '0.4.4', new Date(2026, 7, 16, 12));

    expect(migration.payload.definition.simplePlay).toMatchObject({ gifts: { count: [1] } });
    expect(migration.payload.definition.simplePlay).not.toHaveProperty('overtimeGiftActions');
    expect(migration.payload.definition.gifts.map((gift) => gift.id)).toEqual([1]);
  });

  it('limits overtime v2 actions to unique configured gifts with valid operations', () => {
    const state = defaultState();
    state.simplePlay = {
      version: 1, templateId: 'overtime', templateVersion: 2, attributeId: 'time', managedFingerprint: 'safe',
      parameters: { name: '加班时间', maxSeconds: 3600, broadcastMessage: '继续加油' },
      gifts: { overtime: [1, 1, 2, 0], unknown: [9] },
      overtimeGiftActions: [
        { giftId: 1, operation: 'add', seconds: 60 },
        { giftId: 1, operation: 'double' },
        { giftId: 2, operation: 'invalid' as 'add' },
        { giftId: 9, operation: 'reset' },
      ],
    };
    state.giftCatalog = [
      { id: 1, name: '有效礼物 1', price: 1, coinType: 'gold', imgBasic: '' },
      { id: 2, name: '有效礼物 2', price: 1, coinType: 'gold', imgBasic: '' },
      { id: 9, name: '未知槽位礼物', price: 1, coinType: 'gold', imgBasic: '' },
    ];

    const migration = createOnlineMigration(state, '0.4.4', new Date(2026, 7, 16, 12));

    expect(migration.payload.definition.simplePlay).toMatchObject({
      gifts: { overtime: [1, 2] },
      overtimeGiftActions: [{ giftId: 1, operation: 'add', seconds: 60 }],
    });
    expect(migration.payload.definition.gifts.map((gift) => gift.id)).toEqual([1, 2]);
  });

  it('completes omitted current template parameters from their live defaults', () => {
    const state = defaultState();
    state.simplePlay = {
      version: 1, templateId: 'counter', templateVersion: 1, attributeId: 'score', gifts: { count: [1] }, managedFingerprint: 'safe',
      parameters: { name: '计数器', suffix: '次', amount: 2, cap: 9 },
    };

    const migration = createOnlineMigration(state, '0.4.4', new Date(2026, 7, 16, 12));

    expect(migration.payload.definition.simplePlay?.parameters).toEqual({
      name: '计数器', suffix: '次', amount: 2, cap: 9, broadcastMessage: '感谢大家的支持，欢迎投喂礼物',
    });
  });

  it('does not replace explicit invalid current values with defaults', () => {
    const state = defaultState();
    state.simplePlay = {
      version: 1, templateId: 'counter', templateVersion: 1, attributeId: 'score', gifts: { count: [1] }, managedFingerprint: 'safe',
      parameters: { name: '', suffix: '次', amount: -1, cap: 0 },
    };

    expect(createOnlineMigration(state, '0.4.4', new Date(2026, 7, 16, 12)).payload.definition.simplePlay).toBeUndefined();
  });

  it('completes omitted legacy overtime v1 parameters from historical defaults', () => {
    const state = defaultState();
    state.simplePlay = {
      version: 1, templateId: 'overtime', templateVersion: 1, attributeId: 'time', gifts: { overtime: [1] }, managedFingerprint: 'safe',
      parameters: {},
    };

    const migration = createOnlineMigration(state, '0.4.4', new Date(2026, 7, 16, 12));

    expect(migration.payload.definition.simplePlay?.parameters).toEqual({
      name: '加班时间', minutesPerYuan: 60, maxHours: 0, broadcastMessage: '感谢大家的支持，欢迎投喂礼物',
    });
  });

  it('does not replace an explicit invalid legacy overtime v1 value with its default', () => {
    const state = defaultState();
    state.simplePlay = {
      version: 1, templateId: 'overtime', templateVersion: 1, attributeId: 'time', gifts: { overtime: [1] }, managedFingerprint: 'safe',
      parameters: { minutesPerYuan: 0 },
    };

    expect(createOnlineMigration(state, '0.4.4', new Date(2026, 7, 16, 12)).payload.definition.simplePlay).toBeUndefined();
  });

  it.each([
    '正文 ipfs://cid/image.png',
    '正文 s3://bucket/image.png',
    '正文 ipfs:CID',
    '正文 s3:bucket',
    '正文 pk:regular',
    '正文 content:asset',
    '正文 custom+scheme.foo:asset',
    '正文 C:\\assets\\image.png',
    '正文 C:\\private\\secret',
    '正文 /private/secret',
    '正文 \\private\\secret',
    '正文 .\\assets\\image.png',
    '正文 .\\private\\secret',
    'HP:https://assets.invalid/image.png',
  ])('fails closed for a resource reference with an arbitrary scheme or path: %s', (unsafeValue) => {
    const state = defaultState();
    state.simplePlay = {
      version: 1, templateId: 'counter', templateVersion: 1, attributeId: 'score', gifts: { count: [1] }, managedFingerprint: 'safe',
      parameters: { name: 'PK: 红队', suffix: '次', amount: 1, cap: 0, broadcastMessage: unsafeValue },
    };

    expect(createOnlineMigration(state, '0.4.4', new Date(2026, 7, 16, 12)).payload.definition.simplePlay).toBeUndefined();
  });

  it.each(['Score: 10', 'Boss: 红队'])('preserves an ordinary ASCII label followed by whitespace: %s', (label) => {
    const state = defaultState();
    state.simplePlay = {
      version: 1, templateId: 'counter', templateVersion: 1, attributeId: 'score', gifts: { count: [1] }, managedFingerprint: 'safe',
      parameters: { name: label, suffix: '次', amount: 1, cap: 0, broadcastMessage: '继续加油' },
    };

    expect(createOnlineMigration(state, '0.4.4', new Date(2026, 7, 16, 12)).payload.definition.simplePlay?.parameters.name).toBe(label);
  });

  it.each([
    'PK:/private/secret',
    'HP:../secret',
    'PK:data:text/plain,blocked',
    'HP:https://assets.invalid/image.png',
    '说明=[/private/secret]',
  ])('still rejects a resource reference following an allowed label or punctuation boundary: %s', (unsafeValue) => {
    const state = defaultState();
    state.simplePlay = {
      version: 1, templateId: 'counter', templateVersion: 1, attributeId: 'score', gifts: { count: [1] }, managedFingerprint: 'safe',
      parameters: { name: 'PK: 红队', suffix: '次', amount: 1, cap: 0, broadcastMessage: unsafeValue },
    };

    expect(createOnlineMigration(state, '0.4.4', new Date(2026, 7, 16, 12)).payload.definition.simplePlay).toBeUndefined();
  });

  it.each([
    '正文 http: example.invalid',
    '正文 https: example.invalid',
    '正文 data: text/plain,blocked',
    '正文 file: local-path',
    '正文 blob: payload',
    '正文 javascript: alert(1)',
    '正文 vbscript: msgbox',
  ])('rejects a known dangerous scheme even when its colon is followed by whitespace: %s', (unsafeValue) => {
    const state = defaultState();
    state.simplePlay = {
      version: 1, templateId: 'counter', templateVersion: 1, attributeId: 'score', gifts: { count: [1] }, managedFingerprint: 'safe',
      parameters: { name: '计数器', suffix: '次', amount: 1, cap: 0, broadcastMessage: unsafeValue },
    };

    expect(createOnlineMigration(state, '0.4.4', new Date(2026, 7, 16, 12)).payload.definition.simplePlay).toBeUndefined();
  });

  it.each([
    '说明={/private}',
    '提示，../private',
    '值{\\private}',
  ])('rejects path syntax regardless of its preceding character: %s', (unsafeValue) => {
    const state = defaultState();
    state.simplePlay = {
      version: 1, templateId: 'counter', templateVersion: 1, attributeId: 'score', gifts: { count: [1] }, managedFingerprint: 'safe',
      parameters: { name: 'PK: 红队', suffix: '次', amount: 1, cap: 0, broadcastMessage: unsafeValue },
    };

    expect(createOnlineMigration(state, '0.4.4', new Date(2026, 7, 16, 12)).payload.definition.simplePlay).toBeUndefined();
  });

  it.each([
    { parameters: { name: '', maxSeconds: 3600, broadcastMessage: '继续加油' }, label: 'an empty required text parameter' },
    { parameters: { name: '加班时间', maxSeconds: 1.5, broadcastMessage: '继续加油' }, label: 'a non-integer v2 maxSeconds value' },
  ])('fails closed for $label', ({ parameters }) => {
    const state = defaultState();
    state.simplePlay = {
      version: 1, templateId: 'overtime', templateVersion: 2, attributeId: 'time', gifts: { overtime: [1] }, managedFingerprint: 'safe',
      parameters,
    };

    expect(createOnlineMigration(state, '0.4.4', new Date(2026, 7, 16, 12)).payload.definition.simplePlay).toBeUndefined();
  });
});
