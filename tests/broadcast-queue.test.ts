import { afterEach, describe, expect, it, vi } from 'vitest';
import { SequentialBroadcastQueue } from '../src/ui/display/broadcast-queue';

afterEach(() => {
  vi.useRealTimers();
});

describe('OBS gift broadcast queue', () => {
  it('shows burst events one by one instead of replacing the active message', () => {
    vi.useFakeTimers();
    const shown: string[] = [];
    let idleCount = 0;
    const queue = new SequentialBroadcastQueue<string>({
      durationMs: () => 1000,
      keyOf: (item) => item,
      maxPending: 200,
      onShow: (item) => shown.push(item),
      onIdle: () => { idleCount += 1; },
    });

    queue.enqueue('第一条');
    queue.enqueue('第二条');
    queue.enqueue('第三条');

    expect(shown).toEqual(['第一条']);
    expect(queue.size).toBe(3);
    vi.advanceTimersByTime(1000);
    expect(shown).toEqual(['第一条', '第二条']);
    vi.advanceTimersByTime(1000);
    expect(shown).toEqual(['第一条', '第二条', '第三条']);
    vi.advanceTimersByTime(1000);
    expect(idleCount).toBe(1);
    expect(queue.size).toBe(0);
  });

  it('deduplicates the same backend log entry across refreshes', () => {
    vi.useFakeTimers();
    const shown: string[] = [];
    const queue = new SequentialBroadcastQueue<string>({
      durationMs: () => 1000,
      keyOf: (item) => item,
      maxPending: 200,
      onShow: (item) => shown.push(item),
      onIdle: () => undefined,
    });

    expect(queue.enqueue('event-1')).toBe(true);
    expect(queue.enqueue('event-1')).toBe(false);
    vi.advanceTimersByTime(1000);
    expect(shown).toEqual(['event-1']);
  });

  it('replays the active announcement after a display structure rerender', () => {
    vi.useFakeTimers();
    const shown: string[] = [];
    const queue = new SequentialBroadcastQueue<string>({
      durationMs: () => 1000,
      keyOf: (item) => item,
      maxPending: 200,
      onShow: (item) => shown.push(item),
      onIdle: () => undefined,
    });

    queue.enqueue('event-1');
    queue.enqueue('event-2');
    queue.pause();
    queue.resume();

    expect(shown).toEqual(['event-1', 'event-1']);
    vi.advanceTimersByTime(1000);
    expect(shown).toEqual(['event-1', 'event-1', 'event-2']);
  });
});
