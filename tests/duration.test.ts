import { describe, expect, it } from 'vitest';
import { formatDurationZh } from '../src/duration';

describe('formatDurationZh', () => {
  it.each([
    [0, '0秒'],
    [300, '5分'],
    [3601, '1时1秒'],
    [90000, '25时'],
  ])('formats %i seconds as %s', (seconds, formatted) => {
    expect(formatDurationZh(seconds)).toBe(formatted);
  });
});
