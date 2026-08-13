import { describe, expect, it, vi } from 'vitest';
import { createBlindBoxLeaderboardResource } from '../src/blind-box-leaderboard-resource';
import type { BlindBoxLeaderboardSnapshot, getBlindBoxLeaderboard } from '../src/backend';

function deferred<T>(): { promise: Promise<T>; resolve(value: T): void; reject(reason: unknown): void } {
  let resolve!: (value: T) => void;
  let reject!: (reason: unknown) => void;
  const promise = new Promise<T>((resolvePromise, rejectPromise) => {
    resolve = resolvePromise;
    reject = rejectPromise;
  });
  return { promise, resolve, reject };
}

function snapshot(updatedAt: number): BlindBoxLeaderboardSnapshot {
  return {
    updatedAt,
    summary: { viewerCount: 0, blindBoxCount: 0, cost: 0, value: 0, profit: 0, unpricedCount: 0 },
    viewers: [],
    scopes: [],
  };
}

describe('blind box leaderboard request resource', () => {
  it('applies only the newest refresh when requests resolve out of order', async () => {
    const firstLoad = deferred<BlindBoxLeaderboardSnapshot>();
    const secondLoad = deferred<BlindBoxLeaderboardSnapshot>();
    const loads = [firstLoad, secondLoad];
    const load = vi.fn(() => loads.shift()!.promise) as typeof getBlindBoxLeaderboard;
    const resource = createBlindBoxLeaderboardResource(load);
    const first = resource.refresh({ giftId: 1 });
    const second = resource.refresh({ giftId: 2 });
    const newer = snapshot(20);
    const older = snapshot(10);

    secondLoad.resolve(newer);
    await expect(second).resolves.toEqual({ status: 'applied', snapshot: newer });
    firstLoad.resolve(older);
    await expect(first).resolves.toEqual({ status: 'stale' });
    expect(resource.current()).toBe(newer);
  });

  it('aborts the replaced request and classifies its abort as stale', async () => {
    const firstLoad = deferred<BlindBoxLeaderboardSnapshot>();
    const secondLoad = deferred<BlindBoxLeaderboardSnapshot>();
    const signals: AbortSignal[] = [];
    const load = vi.fn(({ signal }) => {
      signals.push(signal!);
      return signals.length === 1 ? firstLoad.promise : secondLoad.promise;
    }) as typeof getBlindBoxLeaderboard;
    const resource = createBlindBoxLeaderboardResource(load);
    const first = resource.refresh();
    const second = resource.refresh();

    expect(signals[0].aborted).toBe(true);
    firstLoad.reject(new DOMException('replaced', 'AbortError'));
    await expect(first).resolves.toEqual({ status: 'stale' });
    secondLoad.resolve(snapshot(2));
    await expect(second).resolves.toEqual({ status: 'applied', snapshot: snapshot(2) });
  });

  it('keeps the last successful snapshot when a later request fails', async () => {
    const successful = snapshot(1);
    const error = new Error('network down');
    const load = vi.fn()
      .mockResolvedValueOnce(successful)
      .mockRejectedValueOnce(error) as typeof getBlindBoxLeaderboard;
    const resource = createBlindBoxLeaderboardResource(load);

    await expect(resource.refresh()).resolves.toEqual({ status: 'applied', snapshot: successful });
    await expect(resource.refresh()).resolves.toEqual({ status: 'failed', error, snapshot: successful });
    expect(resource.current()).toBe(successful);
  });

  it('rejects a lower revision without replacing the current snapshot', async () => {
    const newer = snapshot(20);
    const older = snapshot(10);
    const load = vi.fn()
      .mockResolvedValueOnce(newer)
      .mockResolvedValueOnce(older) as typeof getBlindBoxLeaderboard;
    const resource = createBlindBoxLeaderboardResource(load);

    await resource.refresh();
    await expect(resource.refresh()).resolves.toEqual({ status: 'stale' });
    expect(resource.current()).toBe(newer);
  });

  it('cancels an active request without discarding the last snapshot', async () => {
    const pending = deferred<BlindBoxLeaderboardSnapshot>();
    const signals: AbortSignal[] = [];
    const load = vi.fn(({ signal }) => {
      signals.push(signal!);
      return signals.length === 1 ? Promise.resolve(snapshot(1)) : pending.promise;
    }) as typeof getBlindBoxLeaderboard;
    const resource = createBlindBoxLeaderboardResource(load);
    const successful = await resource.refresh();
    const active = resource.refresh();

    resource.cancel();
    expect(signals[1].aborted).toBe(true);
    pending.resolve(snapshot(2));
    await expect(active).resolves.toEqual({ status: 'stale' });
    expect(resource.current()).toEqual(successful.status === 'applied' ? successful.snapshot : undefined);
  });

  it('forgets the last snapshot only when clear is called', async () => {
    const pending = deferred<BlindBoxLeaderboardSnapshot>();
    const load = vi.fn()
      .mockResolvedValueOnce(snapshot(1))
      .mockReturnValueOnce(pending.promise) as typeof getBlindBoxLeaderboard;
    const resource = createBlindBoxLeaderboardResource(load);

    await resource.refresh();
    const active = resource.refresh();
    resource.clear();
    pending.resolve(snapshot(2));
    await expect(active).resolves.toEqual({ status: 'stale' });
    expect(resource.current()).toBeUndefined();
  });
});
