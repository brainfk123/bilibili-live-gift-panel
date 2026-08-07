import { describe, expect, it } from 'vitest';
import {
  configPageForSelector,
  configPageForTutorialLesson,
  configPageSearch,
  parseConfigPage,
} from '../src/ui/config/config-route';

describe('config page routing', () => {
  it('opens the overview for missing and unknown pages', () => {
    expect(parseConfigPage('?mode=config')).toBe('overview');
    expect(parseConfigPage('?mode=config&page=unknown')).toBe('overview');
  });

  it('restores each supported page from the address', () => {
    expect(parseConfigPage('?mode=config&page=attributes')).toBe('attributes');
    expect(parseConfigPage('?mode=config&page=activities')).toBe('activities');
    expect(parseConfigPage('?mode=config&page=kpi')).toBe('kpi');
    expect(parseConfigPage('?mode=config&page=obs')).toBe('obs');
    expect(parseConfigPage('?mode=config&page=data')).toBe('data');
  });

  it('preserves unrelated parameters while changing the page', () => {
    expect(configPageSearch('?mode=config&source=tray&page=overview', 'kpi'))
      .toBe('?mode=config&source=tray&page=kpi');
  });

  it('routes training lessons and page targets to their owning workspace', () => {
    expect(configPageForTutorialLesson('room')).toBe('overview');
    expect(configPageForTutorialLesson('gift')).toBe('attributes');
    expect(configPageForSelector('.display-scenes-section')).toBe('obs');
    expect(configPageForSelector('.activity-workspace-section')).toBe('activities');
    expect(configPageForSelector('.contribution-section')).toBe('data');
  });
});
