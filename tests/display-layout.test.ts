import { describe, expect, it } from 'vitest';
import { readFileSync } from 'node:fs';
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

describe('OBS broadcast panel layout', () => {
  const css = readFileSync(new URL('../src/ui/display/display.css', import.meta.url), 'utf8');

  it('uses a one-third narrower panel without shrinking the primary value typography', () => {
    expect(css).toContain('width: min(480px, calc(100% - 40px));');
    expect(css).toMatch(/\.attr-name[\s\S]*?font-size: 32px/);
    expect(css).toMatch(/\.attr-value[\s\S]*?font-size: 56px/);
  });

  it('constrains long broadcast fields and animates message changes', () => {
    expect(css).toMatch(/\.broadcast-user-name[\s\S]*?max-width:/);
    expect(css).toMatch(/\.broadcast-delta[\s\S]*?max-width:/);
    expect(css).toContain('text-overflow: ellipsis;');
    expect(css).toContain('@keyframes broadcastScroll');
    expect(css).toContain('@keyframes broadcastIn');
    expect(css).toContain('@keyframes broadcastOut');
  });

  it('emphasizes the default message and keeps gift announcements compact', () => {
    expect(css).toMatch(/\.broadcast-default[\s\S]*?font-size: 26px/);
    expect(css).toMatch(/\.broadcast-gift[\s\S]*?font-size: 16px/);
    expect(css).toMatch(/\.broadcast-delta[\s\S]*?font-size: 17px/);
  });

  it('applies configurable opacity to the panel background only', () => {
    expect(css).toContain('background: rgba(14, 15, 20, var(--panel-opacity, 0.55));');
  });
});
