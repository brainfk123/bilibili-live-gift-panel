import { describe, expect, it } from 'vitest';
import { calculateFittedFontSize } from '../src/ui/display/display';

describe('OBS attribute value fitting', () => {
  it('keeps the configured font size when the value fits', () => {
    expect(calculateFittedFontSize(48, 430, 210)).toBe(48);
  });

  it('scales a long value to the available column width', () => {
    expect(calculateFittedFontSize(48, 430, 1313)).toBe(15);
  });

  it('uses a readable lower bound for extreme values', () => {
    expect(calculateFittedFontSize(48, 200, 4000)).toBe(14);
  });
});
