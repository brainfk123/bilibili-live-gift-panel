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
  const source = readFileSync(new URL('../src/ui/display/display.ts', import.meta.url), 'utf8');

  it('uses a one-third narrower panel without shrinking the primary value typography', () => {
    expect(css).toContain('width: min(480px, calc(100% - 40px));');
    expect(css).toMatch(/\.attr-name[\s\S]*?font-size: 32px/);
    expect(css).toMatch(/\.attr-value[\s\S]*?font-size: 56px/);
  });

  it('adds a visible accent outline around the complete OBS panel', () => {
    expect(css).toMatch(/\.panel::after\s*\{[\s\S]*?border: 1px solid color-mix/);
    expect(css).toMatch(/\.panel::after\s*\{[\s\S]*?box-shadow: 0 0 18px color-mix/);
    expect(css).toContain('pointer-events: none;');
  });

  it('keeps ordinary attribute values visible without gradient text clipping', () => {
    const ordinaryValueRules = Array.from(
      css.matchAll(/^[ \t]*\.attr-value[ \t]*\{([^}]*)\}/gm),
      (match) => match[1],
    );

    expect(ordinaryValueRules.length).toBeGreaterThan(0);
    expect(ordinaryValueRules[0]).toContain('color: var(--theme-accent, var(--accent));');
    for (const rule of ordinaryValueRules) {
      expect(rule).not.toContain('color: transparent;');
      expect(rule).not.toContain('background-clip: text;');
      expect(rule).not.toContain('-webkit-background-clip: text;');
    }
  });

  it('highlights gift names and separates them from formula names', () => {
    expect(css).toMatch(/\.display-gift-name\s*\{[\s\S]*?color: color-mix/);
    expect(css).toMatch(/\.display-formula-name\s*\{[\s\S]*?border-top: 1px solid color-mix/);
  });

  it('does not render disabled gift rules in the OBS panel', () => {
    expect(source).toContain("state.rules.filter((rule) => rule.attributeName === attr.name && rule.enabled !== false)");
  });

  it('provides distinct gameplay scene structures', () => {
    expect(source).toContain("['grid', 'versus', 'dashboard'].includes(scene.layout)");
    for (const layout of ['focus', 'versus', 'dashboard']) {
      expect(css).toContain(`.panel.scene-layout-${layout}`);
    }
    expect(css).toContain('content: "VS";');
  });

  it('wraps formula names before truncating them', () => {
    expect(css).toMatch(/\.display-formula-name\s*\{[^}]*white-space: normal;/);
    expect(css).toMatch(/\.display-formula-name\s*\{[^}]*-webkit-line-clamp: 2;/);
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

  it('loads Bilibili avatars without sending the localhost referrer', () => {
    expect(source).toContain("referrerPolicy: 'no-referrer'");
  });

  it('keeps the blind-box ranking layout compact', () => {
    expect(source).toContain('mountSpecializedDisplay(root, target)');
    expect(css).toMatch(/\.blind-box-ranking\s*\{[\s\S]*?border-radius: 16px/);
    expect(css).toMatch(/\.blind-box-ranking-track\s*\{[\s\S]*?transition-property: transform/);
    expect(css).toMatch(/\.blind-box-row\s*\{[\s\S]*?grid-template-columns:/);
  });
});
