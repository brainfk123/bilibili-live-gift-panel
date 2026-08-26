import { describe, expect, it, vi } from 'vitest';
import {
  createBiliChallengePoller,
  type BiliChallengePoller,
  type BiliChallengePollSnapshot,
  type BiliChallengeTimerPort,
} from '../src/hosted/bili-challenge-poller';
import { HostedAPIError } from '../src/hosted/api';

class ControlledTimers implements BiliChallengeTimerPort {
  private clock = 0;
  private nextID = 1;
  private readonly scheduled = new Map<number, { callback: () => void; dueAt: number }>();

  setTimeout(callback: () => void, milliseconds: number): number {
    const id = this.nextID++;
    this.scheduled.set(id, { callback, dueAt: this.clock + milliseconds });
    return id;
  }

  clearTimeout(id: number): void {
    this.scheduled.delete(id);
  }

  now(): number {
    return this.clock;
  }

  nextDelay(): number | undefined {
    const next = [...this.scheduled.values()].sort((left, right) => left.dueAt - right.dueAt)[0];
    return next ? next.dueAt - this.clock : undefined;
  }

  count(): number {
    return this.scheduled.size;
  }

  elapse(milliseconds: number): void {
    this.clock += milliseconds;
  }

  async fireNext(): Promise<void> {
    const next = [...this.scheduled.entries()].sort((left, right) => left[1].dueAt - right[1].dueAt)[0];
    if (!next) throw new Error('No controlled timer is scheduled.');
    const [id, task] = next;
    this.scheduled.delete(id);
    this.clock = task.dueAt;
    task.callback();
    for (let turn = 0; turn < 5; turn++) await Promise.resolve();
  }
}

describe('Bilibili challenge poller', () => {
  it('waits six seconds between healthy pending polls', async () => {
    const timers = new ControlledTimers();
    const poll = vi.fn(async () => 'pending' as const);
    const poller = createBiliChallengePoller({ poll }, timers, vi.fn());

    poller.start();
    expect(timers.nextDelay()).toBe(6_000);
    await timers.fireNext();

    expect(poll).toHaveBeenCalledTimes(1);
    expect(timers.nextDelay()).toBe(6_000);
  });

  it('backs browser network failures off at two, four, eight, then fifteen seconds', async () => {
    const timers = new ControlledTimers();
    const poll = vi.fn(async () => { throw new TypeError('Failed to fetch'); });
    const poller = createBiliChallengePoller({ poll }, timers, vi.fn());
    const networkDelays: number[] = [];

    poller.start();
    for (let attempt = 0; attempt < 5; attempt++) {
      await timers.fireNext();
      networkDelays.push(timers.nextDelay() ?? -1);
    }

    expect(networkDelays).toEqual([2_000, 4_000, 8_000, 15_000, 15_000]);
  });

  it.each(['pending', 'scanned'] as const)(
    'resets network backoff after a healthy %s response',
    async (healthyOutcome) => {
      const timers = new ControlledTimers();
      const outcomes: Array<Error | 'pending' | 'scanned'> = [
        new TypeError('offline'),
        healthyOutcome,
        new TypeError('offline again'),
      ];
      const poll = vi.fn(async () => {
        const next = outcomes.shift();
        if (next instanceof Error) throw next;
        return next ?? 'pending';
      });
      const poller = createBiliChallengePoller({ poll }, timers, vi.fn());

      poller.start();
      await timers.fireNext();
      expect(timers.nextDelay()).toBe(2_000);
      await timers.fireNext();
      expect(timers.nextDelay()).toBe(6_000);
      await timers.fireNext();

      expect(timers.nextDelay()).toBe(2_000);
    },
  );

  it('distinguishes rate limiting and waits at least fifteen seconds', async () => {
    const timers = new ControlledTimers();
    const snapshots: BiliChallengePollSnapshot[] = [];
    const poller = createBiliChallengePoller({
      poll: vi.fn(async () => { throw new HostedAPIError('rate_limited', 429); }),
    }, timers, (snapshot) => snapshots.push(snapshot));

    poller.start();
    await timers.fireNext();

    expect(snapshots.at(-1)).toMatchObject({ failureKind: 'rate_limited', canRetryNow: false });
    expect(timers.nextDelay()).toBeGreaterThanOrEqual(15_000);
  });

  it('distinguishes temporary unavailability from browser network failures', async () => {
    const timers = new ControlledTimers();
    const snapshots: BiliChallengePollSnapshot[] = [];
    const poller = createBiliChallengePoller({
      poll: vi.fn(async () => { throw new HostedAPIError('temporarily_unavailable', 503); }),
    }, timers, (snapshot) => snapshots.push(snapshot));

    poller.start();
    await timers.fireNext();

    expect(snapshots.at(-1)).toMatchObject({ failureKind: 'temporarily_unavailable' });
    expect(timers.nextDelay()).toBe(2_000);
  });

  it('allows an immediate retry when a failed request already exceeded the cooldown', async () => {
    const timers = new ControlledTimers();
    const snapshots: BiliChallengePollSnapshot[] = [];
    const poller = createBiliChallengePoller({
      poll: vi.fn(async () => {
        timers.elapse(2_000);
        throw new TypeError('slow network failure');
      }),
    }, timers, (snapshot) => snapshots.push(snapshot));

    poller.start();
    await timers.fireNext();

    expect(snapshots.at(-1)).toMatchObject({ failureKind: 'network', canRetryNow: true });
  });

  it('stops on an invalid response instead of retrying a fatal contract failure', async () => {
    const timers = new ControlledTimers();
    const snapshots: BiliChallengePollSnapshot[] = [];
    const poller = createBiliChallengePoller({
      poll: vi.fn(async () => { throw new HostedAPIError('invalid_response', 200); }),
    }, timers, (snapshot) => snapshots.push(snapshot));

    poller.start();
    await timers.fireNext();

    expect(snapshots.at(-1)).toMatchObject({
      busy: false,
      failureKind: 'fatal',
      canRetryNow: false,
    });
    expect(timers.count()).toBe(0);
  });

  it('publishes an idle snapshot when polling reaches a terminal outcome', async () => {
    const timers = new ControlledTimers();
    const snapshots: BiliChallengePollSnapshot[] = [];
    const poller = createBiliChallengePoller({
      poll: vi.fn(async () => 'terminal' as const),
    }, timers, (snapshot) => snapshots.push(snapshot));

    poller.start();
    await timers.fireNext();

    expect(snapshots.at(-1)).toEqual({ busy: false, canRetryNow: false });
    expect(timers.count()).toBe(0);
  });

  it('ignores repeated manual retries while one request is in flight', async () => {
    const timers = new ControlledTimers();
    let release!: (outcome: 'pending') => void;
    const pending = new Promise<'pending'>((resolve) => { release = resolve; });
    const poll = vi.fn(() => pending);
    const poller = createBiliChallengePoller({ poll }, timers, vi.fn());

    poller.start();
    await timers.fireNext();
    timers.elapse(2_000);
    poller.retryNow();
    poller.retryNow();
    poller.retryNow();
    for (let turn = 0; turn < 5; turn++) await Promise.resolve();

    expect(poll).toHaveBeenCalledTimes(1);
    release('pending');
    for (let turn = 0; turn < 5; turn++) await Promise.resolve();
    expect(timers.count()).toBe(1);
  });

  it('does not install a timer after render synchronously stops the poller', async () => {
    const timers = new ControlledTimers();
    let poller!: BiliChallengePoller;
    let stopping: Promise<void> | undefined;
    poller = createBiliChallengePoller(
      { poll: vi.fn(async () => 'pending' as const) },
      timers,
      () => { stopping = poller.stop(); },
    );

    poller.start();
    await stopping;

    expect(timers.count()).toBe(0);
  });

  it('enforces a two-second manual retry cooldown and replaces the automatic timer', async () => {
    const timers = new ControlledTimers();
    const poll = vi.fn()
      .mockRejectedValueOnce(new TypeError('offline'))
      .mockResolvedValue('pending');
    const poller = createBiliChallengePoller({ poll }, timers, vi.fn());

    poller.start();
    await timers.fireNext();
    poller.retryNow();
    timers.elapse(1_999);
    poller.retryNow();
    expect(poll).toHaveBeenCalledTimes(1);
    expect(timers.count()).toBe(1);

    timers.elapse(1);
    poller.retryNow();
    for (let turn = 0; turn < 5; turn++) await Promise.resolve();

    expect(poll).toHaveBeenCalledTimes(2);
    expect(timers.count()).toBe(1);
    expect(timers.nextDelay()).toBe(6_000);
  });

  it('clears its timer when stopped before an attempt', async () => {
    const timers = new ControlledTimers();
    const poller = createBiliChallengePoller({ poll: vi.fn(async () => 'pending' as const) }, timers, vi.fn());

    poller.start();
    await poller.stop();

    expect(timers.count()).toBe(0);
  });

  it('suppresses rendering and rescheduling after an in-flight request finishes late', async () => {
    const timers = new ControlledTimers();
    const snapshots: BiliChallengePollSnapshot[] = [];
    let release!: (outcome: 'pending') => void;
    const pending = new Promise<'pending'>((resolve) => { release = resolve; });
    const poller = createBiliChallengePoller({ poll: () => pending }, timers, (snapshot) => snapshots.push(snapshot));

    poller.start();
    await timers.fireNext();
    const rendersBeforeStop = snapshots.length;
    const stopping = poller.stop();
    release('pending');
    await stopping;

    expect(timers.count()).toBe(0);
    expect(snapshots).toHaveLength(rendersBeforeStop);
  });
});
