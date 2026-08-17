import { validHostedRoomID } from './room-id';

export type HostedRuntimeConnection = 'pending' | 'connected' | 'degraded' | 'reconnecting';
export type HostedRuntimeState = 'idle' | 'active' | 'degraded' | 'disabled' | 'shutting_down';

export interface HostedRuntimeViewState {
  connection: HostedRuntimeConnection;
  roomID?: string;
  runtimeState?: HostedRuntimeState;
}

export interface RuntimeEventSourceLike {
  close(): void;
  addEventListener(type: string, listener: (event: MessageEvent<string>) => void): void;
  onerror: ((event: Event) => void) | null;
}

export interface HostedRuntimePresence {
  state(): HostedRuntimeViewState;
  subscribe(listener: (state: HostedRuntimeViewState) => void): () => void;
  refresh(): void;
  dispose(): void;
}

export interface HostedApplicationLifecycle {
  active(): boolean;
  run(action: () => void): void;
  dispose(): void;
}

export interface HostedRuntimePresenceOptions {
  createEventSource(path: string): RuntimeEventSourceLike;
  setTimer(callback: () => void, delay: number): unknown;
  clearTimer(timer: unknown): void;
  random(): number;
}

interface RuntimeStatusDTO {
  state: HostedRuntimeState;
  roomId?: string;
  degraded: boolean;
}

export function createHostedApplicationLifecycle(): HostedApplicationLifecycle {
  let active = true;
  return Object.freeze({
    active: (): boolean => active,
    run(action: () => void): void { if (active) action(); },
    dispose(): void { active = false; },
  });
}

export function createHostedRuntimePresence(options: HostedRuntimePresenceOptions): HostedRuntimePresence {
  let current: HostedRuntimeViewState = { connection: 'pending' };
  let source: RuntimeEventSourceLike | undefined;
  let reconnectTimer: unknown;
  let reconnectAttempts = 0;
  let disposed = false;
  const listeners = new Set<(state: HostedRuntimeViewState) => void>();

  const publish = (next: HostedRuntimeViewState): void => {
    current = Object.freeze({ ...next });
    for (const listener of listeners) listener(current);
  };
  const clearReconnect = (): void => {
    if (reconnectTimer !== undefined) options.clearTimer(reconnectTimer);
    reconnectTimer = undefined;
  };
  const connect = (): void => {
    if (disposed) return;
    const opened = options.createEventSource('/api/runtime/events');
    source = opened;
    opened.addEventListener('status', (event) => {
      if (disposed || source !== opened) return;
      const status = parseRuntimeStatus(event.data);
      if (!status) {
        opened.onerror?.(new Event('error'));
        return;
      }
      reconnectAttempts = 0;
      publish({
        connection: status.degraded || status.state === 'degraded' ? 'degraded' : 'connected',
        ...(status.roomId === undefined ? {} : { roomID: status.roomId }),
        runtimeState: status.state,
      });
    });
    opened.addEventListener('degraded', (event) => {
      if (disposed || source !== opened) return;
      const degraded = parseDegraded(event.data);
      if (degraded === undefined) {
        opened.onerror?.(new Event('error'));
        return;
      }
      if (current.runtimeState !== undefined) publish({ ...current, connection: degraded ? 'degraded' : 'connected' });
    });
    opened.onerror = () => {
      if (disposed || source !== opened) return;
      opened.close();
      source = undefined;
      publish({ ...current, connection: 'reconnecting' });
      clearReconnect();
      const base = Math.min(30_000, 1_000 * (2 ** Math.min(reconnectAttempts, 5)));
      reconnectAttempts += 1;
      const random = Math.min(1, Math.max(0, options.random()));
      const delay = Math.round(base * (0.8 + random * 0.4));
      reconnectTimer = options.setTimer(() => {
        reconnectTimer = undefined;
        connect();
      }, delay);
    };
  };

  connect();
  return Object.freeze({
    state: () => ({ ...current }),
    subscribe(listener: (state: HostedRuntimeViewState) => void): () => void {
      listeners.add(listener);
      listener(current);
      return () => { listeners.delete(listener); };
    },
    refresh(): void {
      if (disposed) return;
      clearReconnect();
      source?.close();
      source = undefined;
      reconnectAttempts = 0;
      publish({ connection: 'pending', ...(current.roomID === undefined ? {} : { roomID: current.roomID }) });
      connect();
    },
    dispose(): void {
      if (disposed) return;
      disposed = true;
      clearReconnect();
      source?.close();
      source = undefined;
      listeners.clear();
    },
  });
}

function parseRuntimeStatus(raw: string): RuntimeStatusDTO | undefined {
  let value: unknown;
  try { value = JSON.parse(raw); } catch { return undefined; }
  if (!record(value) || !exactKeys(value, ['state', 'leases', 'configLease', 'obsLease', 'degraded', 'connectionHealthy'], ['roomId', 'sessionId', 'persistenceBuffered'])) return undefined;
  if (typeof value.state !== 'string' || !['idle', 'active', 'degraded', 'disabled', 'shutting_down'].includes(value.state)) return undefined;
  if (!nonnegativeInteger(value.leases) || typeof value.configLease !== 'boolean' || typeof value.obsLease !== 'boolean' || typeof value.degraded !== 'boolean' || typeof value.connectionHealthy !== 'boolean') return undefined;
  if (value.roomId !== undefined && (typeof value.roomId !== 'string' || !validHostedRoomID(value.roomId))) return undefined;
  if (value.sessionId !== undefined && !nonnegativeInteger(value.sessionId)) return undefined;
  if (value.persistenceBuffered !== undefined && !nonnegativeInteger(value.persistenceBuffered)) return undefined;
  return { state: value.state as HostedRuntimeState, degraded: value.degraded, ...(value.roomId === undefined ? {} : { roomId: value.roomId }) };
}

function parseDegraded(raw: string): boolean | undefined {
  let value: unknown;
  try { value = JSON.parse(raw); } catch { return undefined; }
  return record(value) && exactKeys(value, ['degraded']) && typeof value.degraded === 'boolean' ? value.degraded : undefined;
}

function record(value: unknown): value is Record<string, unknown> { return typeof value === 'object' && value !== null && !Array.isArray(value); }
function exactKeys(value: Record<string, unknown>, required: string[], optional: string[] = []): boolean {
  const allowed = new Set([...required, ...optional]);
  return required.every((key) => Object.hasOwn(value, key)) && Object.keys(value).every((key) => allowed.has(key));
}
function nonnegativeInteger(value: unknown): value is number { return Number.isSafeInteger(value) && Number(value) >= 0; }
