import { OP_AUTH, OP_AUTH_REPLY, OP_MESSAGE, OP_HEARTBEAT, Packet, decodePackets, decodeText, encodeJson, encodePacket, inflate } from './protocol';
import { GiftEvent, ScEvent, parseGift, parseSc } from './messages';

export type ConnState = 'idle' | 'connecting' | 'connected' | 'reconnecting' | 'error';

export interface WsLike {
  binaryType: string;
  onopen: (() => void) | null;
  onmessage: ((ev: { data: ArrayBuffer }) => void) | null;
  onclose: ((ev: { code: number }) => void) | null;
  onerror: ((ev: unknown) => void) | null;
  send: (data: ArrayBuffer) => void;
  close: () => void;
}

export interface DanmakuClientOptions {
  roomId: number;
  wsFactory?: (url: string) => WsLike;
  onMessage?: (cmd: string, data: any) => void;
  onGift?: (ev: GiftEvent) => void;
  onSc?: (ev: ScEvent) => void;
  onState?: (s: ConnState) => void;
}

const WS_URL = 'wss://broadcastlv.chat.bilibili.com/sub';
const HEARTBEAT_MS = 30000;

function randomBuvid(): string {
  let s = 'XY';
  for (let i = 0; i < 32; i++) s += Math.floor(Math.random() * 16).toString(16);
  return s;
}

export class DanmakuClient {
  private ws: WsLike | null = null;
  private heartbeatTimer: ReturnType<typeof setInterval> | null = null;
  private reconnectTimer: ReturnType<typeof setTimeout> | null = null;
  private attempt = 0;
  private stopped = false;

  constructor(private readonly opts: DanmakuClientOptions) {}

  start(): void {
    this.stopped = false;
    this.connect();
  }

  stop(): void {
    this.stopped = true;
    if (this.heartbeatTimer) clearInterval(this.heartbeatTimer);
    if (this.reconnectTimer) clearTimeout(this.reconnectTimer);
    this.ws?.close();
    this.ws = null;
  }

  private connect(): void {
    const ws = this.opts.wsFactory ? this.opts.wsFactory(WS_URL) : this.createWs(WS_URL);
    this.ws = ws;
    ws.binaryType = 'arraybuffer';
    ws.onopen = () => this.onOpen();
    ws.onmessage = (ev) => void this.onMessage(ev);
    ws.onclose = () => this.onClose();
    ws.onerror = () => { /* close 事件会触发重连 */ };
    this.opts.onState?.('connecting');
  }

  private createWs(url: string): WsLike {
    return new WebSocket(url) as unknown as WsLike;
  }

  private onOpen(): void {
    this.attempt = 0;
    this.opts.onState?.('connected');
    this.ws?.send(encodeJson(OP_AUTH, {
      uid: 0,
      roomid: this.opts.roomId,
      protover: 2,
      platform: 'web',
      buvid: randomBuvid(),
      type: 2,
    }).buffer as ArrayBuffer);
    this.startHeartbeat();
  }

  private startHeartbeat(): void {
    if (this.heartbeatTimer) clearInterval(this.heartbeatTimer);
    this.heartbeatTimer = setInterval(() => {
      this.ws?.send(encodePacket(OP_HEARTBEAT, new Uint8Array(0)).buffer as ArrayBuffer);
    }, HEARTBEAT_MS);
  }

  private async onMessage(ev: { data: ArrayBuffer }): Promise<void> {
    const packets = decodePackets(new Uint8Array(ev.data));
    for (const p of packets) {
      await this.handlePacket(p);
    }
  }

  private async handlePacket(p: Packet): Promise<void> {
    if (p.op === OP_AUTH_REPLY) {
      try {
        const reply = JSON.parse(decodeText(p.body));
        if (reply.code !== 0) {
          // 认证失败（如房间号错误），关闭连接触发重连兜底
          this.ws?.close();
        }
      } catch {
        /* ignore */
      }
      return;
    }
    if (p.op !== OP_MESSAGE) return;
    let bodies: Uint8Array[];
    if (p.protover === 2) {
      const inflated = await inflate(p.body);
      bodies = decodePackets(inflated).map((x) => x.body);
    } else {
      bodies = [p.body];
    }
    for (const body of bodies) {
      this.dispatchJson(body);
    }
  }

  private dispatchJson(body: Uint8Array): void {
    let parsed: any;
    try {
      parsed = JSON.parse(decodeText(body));
    } catch {
      return;
    }
    const cmd = parsed?.cmd;
    const data = parsed?.data;
    if (typeof cmd === 'string') this.opts.onMessage?.(cmd, data);
    if (cmd === 'SEND_GIFT') this.opts.onGift?.(parseGift(data ?? {}));
    if (cmd === 'SUPER_CHAT_MESSAGE') this.opts.onSc?.(parseSc(data ?? {}));
  }

  private onClose(): void {
    if (this.heartbeatTimer) clearInterval(this.heartbeatTimer);
    if (this.stopped) return;
    this.attempt++;
    const delay = Math.min(30000, Math.pow(2, this.attempt) * 1000);
    this.opts.onState?.('reconnecting');
    this.reconnectTimer = setTimeout(() => this.connect(), delay);
  }
}
