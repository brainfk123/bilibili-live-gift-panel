import { describe, expect, it, vi } from 'vitest';

import { createHostedViewHost } from '../src/hosted/shell';

describe('Hosted root view ownership', () => {
  it('retries the retained current view after replacement cleanup fails and mounts only the newest request', async () => {
    const events: string[] = [];
    const cleanupFailure = new Error('RAW HOST CLEANUP private-challenge');
    let rejectFirstCleanup!: (reason: Error) => void;
    const firstCleanup = new Promise<void>((_resolve, reject) => { rejectFirstCleanup = reject; });
    let cleanupAttempts = 0;
    const host = createHostedViewHost();
    await host.replace(() => {
      events.push('mount:current');
      return {
        async dispose(): Promise<void> {
          cleanupAttempts += 1;
          events.push(`dispose:current:${cleanupAttempts}`);
          if (cleanupAttempts === 1) await firstCleanup;
        },
      };
    });
    const staleMount = vi.fn(() => {
      events.push('mount:stale');
      return { dispose() {} };
    });
    const newestMount = vi.fn(() => {
      events.push('mount:newest');
      return { dispose: () => { events.push('dispose:newest'); } };
    });

    const stale = host.replace(staleMount);
    await vi.waitFor(() => expect(events).toContain('dispose:current:1'));
    const newest = host.replace(newestMount);
    rejectFirstCleanup(cleanupFailure);

    await expect(stale).rejects.toBe(cleanupFailure);
    await newest;

    expect(cleanupAttempts).toBe(2);
    expect(staleMount).not.toHaveBeenCalled();
    expect(newestMount).toHaveBeenCalledTimes(1);
    expect(events).toEqual([
      'mount:current',
      'dispose:current:1',
      'dispose:current:2',
      'mount:newest',
    ]);
    await host.dispose();
  });

  it('rejects root disposal without losing the view and retries that exact owner later', async () => {
    const cleanupFailure = new Error('RAW ROOT DISPOSE private-challenge');
    let attempts = 0;
    const host = createHostedViewHost();
    await host.replace(() => ({
      async dispose(): Promise<void> {
        attempts += 1;
        if (attempts === 1) throw cleanupFailure;
      },
    }));

    await expect(host.dispose()).rejects.toBe(cleanupFailure);

    expect(attempts).toBe(1);
    await host.dispose();
    expect(attempts).toBe(2);
  });

  it('serializes pending cleanup once and mounts only the latest requested replacement', async () => {
    const events: string[] = [];
    let releaseCleanup!: () => void;
    const pendingCleanup = new Promise<void>((resolve) => { releaseCleanup = resolve; });
    let cleanupCalls = 0;
    const host = createHostedViewHost();
    await host.replace(() => ({
      async dispose(): Promise<void> {
        cleanupCalls += 1;
        events.push('dispose:current');
        await pendingCleanup;
      },
    }));
    const staleMount = vi.fn(() => ({ dispose() {} }));
    const newestMount = vi.fn(() => {
      events.push('mount:newest');
      return { dispose() {} };
    });

    const stale = host.replace(staleMount);
    await vi.waitFor(() => expect(cleanupCalls).toBe(1));
    const newest = host.replace(newestMount);
    await Promise.resolve();

    expect(cleanupCalls).toBe(1);
    expect(staleMount).not.toHaveBeenCalled();
    expect(newestMount).not.toHaveBeenCalled();
    releaseCleanup();
    await Promise.all([stale, newest]);

    expect(cleanupCalls).toBe(1);
    expect(staleMount).not.toHaveBeenCalled();
    expect(newestMount).toHaveBeenCalledTimes(1);
    expect(events).toEqual(['dispose:current', 'mount:newest']);
    await host.dispose();
  });
});
