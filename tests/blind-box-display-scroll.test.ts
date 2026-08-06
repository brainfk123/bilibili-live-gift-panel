import { describe, expect, it } from 'vitest';
import { advanceBlindBoxScroll } from '../src/ui/display/blind-box-display';

describe('blind-box leaderboard ping-pong scrolling', () => {
  it('walks to the bottom and reverses back to the top', () => {
    let position = { index: 0, direction: 1 as 1 | -1 };
    const visited = [position.index];
    for (let step = 0; step < 6; step += 1) {
      position = advanceBlindBoxScroll(position.index, position.direction, 3);
      visited.push(position.index);
    }
    expect(visited).toEqual([0, 1, 2, 3, 2, 1, 0]);
    expect(position.direction).toBe(1);
  });

  it('stays still when every viewer already fits in the viewport', () => {
    expect(advanceBlindBoxScroll(0, -1, 0)).toEqual({ index: 0, direction: 1 });
  });
});
