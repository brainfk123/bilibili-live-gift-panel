import { describe, expect, it } from 'vitest';
import { readFileSync } from 'node:fs';

describe('gift receipt presentation', () => {
  const source = readFileSync(new URL('../src/ui/config/config.ts', import.meta.url), 'utf8');
  const css = readFileSync(new URL('../src/ui/config/config.css', import.meta.url), 'utf8');

  it('loads Bilibili gift icons without sending the localhost referrer', () => {
    expect(source).toMatch(/class: 'gift-history-gift-image',[^}]*referrerPolicy: 'no-referrer'/);
  });

  it('uses blue, purple, and red membership badges for captain, admiral, and governor', () => {
    expect(css).toMatch(/\.gift-history-membership\.is-captain[^}]*#4f8cff/i);
    expect(css).toMatch(/\.gift-history-membership\.is-admiral[^}]*#9f6cff/i);
    expect(css).toMatch(/\.gift-history-membership\.is-governor[^}]*#ff5d67/i);
  });
});
