import {
  getBlindBoxLeaderboard,
  type BlindBoxLeaderboardSnapshot,
} from './backend';

export type BlindBoxLeaderboardLoadResult =
  | { status: 'applied'; snapshot: BlindBoxLeaderboardSnapshot }
  | { status: 'stale' }
  | { status: 'failed'; error: unknown; snapshot?: BlindBoxLeaderboardSnapshot };

export interface BlindBoxLeaderboardResource {
  refresh(options?: { giftId?: number; limit?: number }): Promise<BlindBoxLeaderboardLoadResult>;
  current(): BlindBoxLeaderboardSnapshot | undefined;
  clear(): void;
  cancel(): void;
}

export function createBlindBoxLeaderboardResource(
  load: typeof getBlindBoxLeaderboard = getBlindBoxLeaderboard,
): BlindBoxLeaderboardResource {
  let snapshot: BlindBoxLeaderboardSnapshot | undefined;
  let generation = 0;
  let activeController: AbortController | undefined;

  const cancel = (): void => {
    generation += 1;
    activeController?.abort();
    activeController = undefined;
  };

  return {
    async refresh(options = {}): Promise<BlindBoxLeaderboardLoadResult> {
      cancel();
      const requestGeneration = generation;
      const controller = new AbortController();
      activeController = controller;
      try {
        const next = await load({ ...options, signal: controller.signal });
        if (requestGeneration !== generation || controller.signal.aborted) return { status: 'stale' };
        if (snapshot && next.updatedAt < snapshot.updatedAt) return { status: 'stale' };
        snapshot = next;
        return { status: 'applied', snapshot: next };
      } catch (error) {
        if (requestGeneration !== generation || controller.signal.aborted) return { status: 'stale' };
        return snapshot ? { status: 'failed', error, snapshot } : { status: 'failed', error };
      } finally {
        if (activeController === controller) activeController = undefined;
      }
    },
    current: () => snapshot,
    clear: () => {
      cancel();
      snapshot = undefined;
    },
    cancel,
  };
}
