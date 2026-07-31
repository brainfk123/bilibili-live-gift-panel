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

export interface RoomInfo {
  roomId: number;
  buvid: string;
  token: string;
  hostList: { host: string; wss_port: number }[];
}

export interface DanmakuClientOptions {
  roomId: number;
  roomInfoFetcher?: (roomId: number) => Promise<RoomInfo>;
  wsFactory?: (url: string) => WsLike;
  onMessage?: (cmd: string, data: any) => void;
  onGift?: (ev: GiftEvent) => void;
  onSc?: (ev: ScEvent) => void;
  onState?: (s: ConnState) => void;
}

const HEARTBEAT_MS = 30000;
const REINIT_PERIOD = 6;

async function defaultRoomInfoFetcher(roomId: number): Promise<RoomInfo> {
  const res = await fetch(`/api/room_info?roomId=${roomId}`);
  const json = await res.json();
  if (json.code !== 0) throw new Error(json.message || '获取房间信息失败');
  return json;
}

export class DanmakuClient {
  private ws: WsLike | null = null;
  private heartbeatTimer: ReturnType<typeof setInterval> | null = null;
  private reconnectTimer: ReturnType<typeof setTimeout> | null = null;
  private attempt = 0;
  private stopped = false;
  private info: RoomInfo | null = null;

  constructor(private readonly opts: DanmakuClientOptions) {}

  async start(): Promise<void> {
    this.stopped = false;
    try {
      await this.initRoomInfo();
    } catch {
      this.opts.onState?.('error');
      this.scheduleRetry();
      return;
    }
    this.connect();
  }

  stop(): void {
    this.stopped = true;
    if (this.heartbeatTimer) clearInterval(this.heartbeatTimer);
    if (this.reconnectTimer) clearTimeout(this.reconnectTimer);
    this.ws?.close();
    this.ws = null;
  }

  private async initRoomInfo(): Promise<void> {
    const fetcher = this.opts.roomInfoFetcher ?? defaultRoomInfoFetcher;
    this.info = await fetcher(this.opts.roomId);
  }

  private scheduleRetry(): void {
    if (this.stopped) return;
    this.attempt++;
    const delay = Math.min(30000, Math.pow(2, this.attempt) * 1000);
    this.opts.onState?.('reconnecting');
    this.reconnectTimer = setTimeout(() => void this.start(), delay);
  }

  private getWsUrl(): string {
    const list = this.info?.hostList ?? [{ host: 'broadcastlv.chat.bilibili.com', wss_port: 443 }];
    const server = list[this.attempt % list.length];
    return `wss://${server.host}:${server.wss_port}/sub`;
  }

  private connect(): void {
    const ws = this.opts.wsFactory ? this.opts.wsFactory(this.getWsUrl()) : this.createWs(this.getWsUrl());
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
    const info = this.info;
    if (!info) return;
    this.ws?.send(encodeJson(OP_AUTH, {
      uid: 0,
      roomid: info.roomId,
      protover: 2,
      platform: 'web',
      buvid: info.buvid,
      type: 2,
      key: info.token,
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
          // 认证失败（token 失效等），重新获取房间信息后重连
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
      try {
        const inflated = await inflate(p.body);
        bodies = decodePackets(inflated).map((x) => x.body);
      } catch {
        return;
      }
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
    this.reconnectTimer = setTimeout(() => {
      if (this.stopped) return;
      if (this.attempt % REINIT_PERIOD === 0) {
        // 定期重新获取 token，避免过期
        void this.start();
      } else {
        this.connect();
      }
    }, delay);
  }
}
