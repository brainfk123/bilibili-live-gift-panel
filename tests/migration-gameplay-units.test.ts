import { describe, expect, it } from 'vitest';
import { createOnlineMigration } from '../src/migration';
import { deriveGameplayUnits } from '../src/migration-gameplay-units';
import { defaultState } from '../src/storage';

describe('migration gameplay-unit derivation', () => {
  it('links an attribute gameplay to every attribute referenced by its formula', () => {
    const state = defaultState();
    state.attributes = [
      { id: 'score', name: '积分', value: 1, unit: 'none', format: 'number', decimals: 0, suffix: '分' },
      { id: 'bonus', name: '加成', value: 2, unit: 'none', format: 'number', decimals: 0, suffix: '倍' },
    ];
    state.rules = [{ id: 'combined', giftId: 1, attributeName: '积分', formula: '积分+加成' }];
    state.giftCatalog = [{ id: 1, name: '计分礼物', price: 100, coinType: 'gold', imgBasic: '' }];
    const migration = createOnlineMigration(state, '0.4.7', new Date('2026-08-25T00:00:00.000Z'));

    const declaration = deriveGameplayUnits({
      definition: migration.payload.definition,
      runtime: migration.payload.runtime,
      cropPresets: migration.payload.cropPresets,
    });

    expect(declaration.units).toMatchObject([
      { id: 'attribute:bonus', attributeIds: ['bonus'] },
      { id: 'attribute:score', attributeIds: ['bonus', 'score'], ruleIds: ['combined'], giftIds: [1] },
    ]);
    expect(declaration.groups).toMatchObject([{
      unitIds: ['attribute:bonus', 'attribute:score'],
      reasons: [{ kind: 'shared-attribute', referenceId: 'bonus' }],
    }]);
  });

  it('links otherwise independent attribute gameplay through a shared display scene', () => {
    const state = defaultState();
    state.attributes = [
      { id: 'left', name: '左队', value: 1, unit: 'none', format: 'number', decimals: 0, suffix: '分' },
      { id: 'right', name: '右队', value: 2, unit: 'none', format: 'number', decimals: 0, suffix: '分' },
    ];
    state.displayScenes = [{ id: 'versus', name: '对战面板', attributeNames: ['左队', '右队'], layout: 'versus', themeId: 'neon' }];
    const declaration = createOnlineMigration(state, '0.4.7', new Date('2026-08-25T00:00:00.000Z'))
      .payload.dependencyDeclaration;

    expect(declaration.groups).toMatchObject([{
      unitIds: ['attribute:left', 'attribute:right'],
      reasons: [{ kind: 'shared-scene', referenceId: 'versus' }],
    }]);
  });

  it('keeps gift targets independent and derives stable output from reordered definitions', () => {
    const state = defaultState();
    state.attributes = [
      { id: 'second', name: '第二项', value: 2, unit: 'none', format: 'number', decimals: 0, suffix: '' },
      { id: 'first', name: '第一项', value: 1, unit: 'none', format: 'number', decimals: 0, suffix: '' },
    ];
    state.giftKpiPanels = [{
      id: 'goal', name: '礼物目标', layout: 'stack', items: [],
      appearance: { themeId: 'minimal', fontSize: 30, accentColor: '#123456', showConnection: false, align: 'left', panelOpacity: 80 },
    }];
    const migration = createOnlineMigration(state, '0.4.7', new Date('2026-08-25T00:00:00.000Z'));
    const reordered = {
      ...migration.payload.definition,
      attributes: [...migration.payload.definition.attributes].reverse(),
      giftTargetPanels: [...migration.payload.definition.giftTargetPanels].reverse(),
    };

    expect(deriveGameplayUnits({ definition: reordered, runtime: migration.payload.runtime }))
      .toEqual(migration.payload.dependencyDeclaration);
    expect(migration.payload.dependencyDeclaration.units.map((unit) => unit.id)).toEqual([
      'attribute:first', 'attribute:second', 'gift-target:goal',
    ]);
    expect(migration.payload.dependencyDeclaration.groups).toEqual([]);
  });

  it('uses the same first-name mapping as the exporter when legacy attributes have duplicate names', () => {
    const state = defaultState();
    state.attributes = [
      { id: 'first', name: '重名', value: 1, unit: 'none', format: 'number', decimals: 0, suffix: '' },
      { id: 'second', name: '重名', value: 2, unit: 'none', format: 'number', decimals: 0, suffix: '' },
    ];
    state.rules = [{ id: 'legacy-rule', giftId: 1, attributeName: '重名', formula: '重名+1' }];
    state.giftCatalog = [{ id: 1, name: '礼物', price: 100, coinType: 'gold', imgBasic: '' }];

    const declaration = createOnlineMigration(state, '0.4.7', new Date('2026-08-25T00:00:00.000Z'))
      .payload.dependencyDeclaration;

    expect(declaration.units).toMatchObject([
      { id: 'attribute:first', attributeIds: ['first'], ruleIds: ['legacy-rule'] },
      { id: 'attribute:second', attributeIds: ['second'], ruleIds: [] },
    ]);
    expect(declaration.groups).toEqual([]);
  });

  it('links independent gameplay that share one stable crop preset', () => {
    const state = defaultState();
    state.attributes = [
      { id: 'left', name: '左队', value: 1, unit: 'none', format: 'number', decimals: 0, suffix: '' },
      { id: 'right', name: '右队', value: 2, unit: 'none', format: 'number', decimals: 0, suffix: '' },
    ];
    state.rules = [
      { id: 'left-rule', giftId: 1, attributeName: '左队', formula: '左队+1' },
      { id: 'right-rule', giftId: 1, attributeName: '右队', formula: '右队+1' },
    ];
    state.giftCatalog = [{ id: 1, name: '共享礼物', price: 100, coinType: 'gold', imgBasic: '' }];
    state.settings.giftClipCrops = { 'gift:1': { x: 0, y: 0, width: 1, height: 1 } };

    const declaration = createOnlineMigration(state, '0.4.7', new Date('2026-08-25T00:00:00.000Z'))
      .payload.dependencyDeclaration;

    expect(declaration.groups).toMatchObject([{
      unitIds: ['attribute:left', 'attribute:right'],
      reasons: [{ kind: 'shared-crop-preset', referenceId: 'gift:1' }],
    }]);
  });

  it('sorts arbitrary stable IDs by code units instead of the host locale', () => {
    const state = defaultState();
    state.attributes = [
      { id: 'ä', name: '变量 A', value: 1, unit: 'none', format: 'number', decimals: 0, suffix: '' },
      { id: 'z', name: '变量 Z', value: 2, unit: 'none', format: 'number', decimals: 0, suffix: '' },
    ];

    const declaration = createOnlineMigration(state, '0.4.7', new Date('2026-08-25T00:00:00.000Z'))
      .payload.dependencyDeclaration;

    expect(declaration.units.map((unit) => unit.id)).toEqual(['attribute:z', 'attribute:ä']);
  });
});
