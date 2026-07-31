import { GiftEvent, ScEvent } from '../bilibili/messages';
import { DanmakuClient, ConnState } from '../bilibili/client';
import { AppState } from '../types';
import { upsertRecentGift } from '../gifts/catalog';
import { applyGiftToState, recordGiftTotals, TriggerResult } from './rules';

export type { TriggerResult };

const DEDUP_WINDOW_MS = 60000;

export class Engine {
  private client: DanmakuClient;
  private seen = new Map<string, number>();
  private onState?: (s: ConnState) => void;

  constructor(
    private readonly state: AppState,
    private readonly onTrigger?: (r: TriggerResult) => void,
    wsFactory?: (url: string) => any,
  ) {
    this.client = new DanmakuClient({
      roomId: Number(state.roomId),
      wsFactory,
      onGift: (ev) => this.handleGift(ev),
      onSc: (ev) => this.handleSc(ev),
      onState: (s) => this.onState?.(s),
    });
  }

  setStateListener(fn: (s: ConnState) => void): void {
    this.onState = fn;
  }

  start(): void {
    if (!this.state.roomId) return;
    this.client.start();
  }

  stop(): void {
    this.client.stop();
  }

  handleGift(ev: GiftEvent): void {
    if (this.isDuplicate(ev.rnd)) return;
    upsertRecentGift(this.state, ev);
    recordGiftTotals(this.state, ev);
    const results = applyGiftToState(this.state, ev);
    for (const r of results) this.onTrigger?.(r);
  }

  handleSc(ev: ScEvent): void {
    recordGiftTotals(this.state, {
      giftId: ev.giftId,
      giftName: ev.giftName,
      num: 1,
      price: ev.price,
      coinType: 'gold',
      totalCoin: ev.price,
      uname: ev.uname,
      uid: ev.uid,
      timestamp: Math.floor(Date.now() / 1000),
      imgBasic: '',
      rnd: `sc-${ev.id}`,
    });
  }

  private isDuplicate(key: string): boolean {
    const now = Date.now();
    if (!key) return false;
    const last = this.seen.get(key);
    this.seen.set(key, now);
    if (this.seen.size > 500) {
      for (const [k, t] of this.seen) {
        if (now - t > DEDUP_WINDOW_MS) this.seen.delete(k);
      }
    }
    return last !== undefined && now - last < DEDUP_WINDOW_MS;
  }
}
