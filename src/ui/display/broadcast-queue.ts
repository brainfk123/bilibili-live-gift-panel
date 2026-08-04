export interface SequentialBroadcastQueueOptions<T> {
  durationMs: (pendingCount: number) => number;
  keyOf: (item: T) => string;
  maxPending: number;
  onShow: (item: T) => void;
  onIdle: () => void;
}

export class SequentialBroadcastQueue<T> {
  private readonly pending: T[] = [];
  private readonly seenKeys = new Set<string>();
  private readonly seenKeyOrder: string[] = [];
  private active: T | undefined;
  private timer: ReturnType<typeof globalThis.setTimeout> | undefined;

  constructor(private readonly options: SequentialBroadcastQueueOptions<T>) {}

  enqueue(item: T): boolean {
    const key = this.options.keyOf(item);
    if (this.seenKeys.has(key)) return false;
    this.remember(key);
    if (this.pending.length >= this.options.maxPending) this.pending.shift();
    this.pending.push(item);
    this.startNext();
    return true;
  }

  pause(requeueActive = true): void {
    if (this.timer !== undefined) globalThis.clearTimeout(this.timer);
    this.timer = undefined;
    if (this.active !== undefined && requeueActive) this.pending.unshift(this.active);
    this.active = undefined;
  }

  resume(): void {
    this.startNext();
  }

  dispose(): void {
    this.pause(false);
    this.pending.splice(0);
    this.seenKeys.clear();
    this.seenKeyOrder.splice(0);
  }

  get size(): number {
    return this.pending.length + (this.active === undefined ? 0 : 1);
  }

  private startNext(): void {
    if (this.active !== undefined || this.timer !== undefined) return;
    const next = this.pending.shift();
    if (next === undefined) return;
    this.active = next;
    this.options.onShow(next);
    const duration = Math.max(0, this.options.durationMs(this.pending.length));
    this.timer = globalThis.setTimeout(() => {
      this.timer = undefined;
      this.active = undefined;
      if (this.pending.length > 0) this.startNext();
      else this.options.onIdle();
    }, duration);
  }

  private remember(key: string): void {
    this.seenKeys.add(key);
    this.seenKeyOrder.push(key);
    const maximumRemembered = Math.max(this.options.maxPending * 2, 20);
    while (this.seenKeyOrder.length > maximumRemembered) {
      const expired = this.seenKeyOrder.shift();
      if (expired !== undefined) this.seenKeys.delete(expired);
    }
  }
}
