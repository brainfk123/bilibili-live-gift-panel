import type { HostedRuntimePresence, HostedRuntimeViewState } from './runtime';
import { validHostedRoomID } from './room-id';

export interface RuntimeRoomAPI {
  setRuntimeRoom(roomID: string): Promise<void>;
}

export type RoomRuntimePresence = Pick<HostedRuntimePresence, 'state' | 'subscribe' | 'refresh'>;

export interface RoomControlOptions {
  confirm?(message: string): boolean;
}

export function mountRoomControls(root: HTMLElement, api: RuntimeRoomAPI, presence: RoomRuntimePresence, options: RoomControlOptions = {}): { dispose(): void } {
  const document = root.ownerDocument;
  if (!document) throw new Error('Room controls require a document.');
  let disposed = false;
  let busy = false;
  let authoritative = false;
  const panel = document.createElement('section');
  panel.className = 'hosted-room-controls';
  const title = document.createElement('h2'); title.textContent = '直播间运行';
  const status = document.createElement('p'); status.setAttribute('role', 'status'); status.setAttribute('aria-live', 'polite');
  const label = document.createElement('label'); label.textContent = '直播间号';
  const input = document.createElement('input'); input.type = 'text'; input.inputMode = 'numeric'; input.setAttribute('autocomplete', 'off'); label.append(input);
  const apply = document.createElement('button'); apply.type = 'button'; apply.textContent = '确认直播间';
  const explanation = document.createElement('p'); explanation.textContent = '在线配置页和 OBS 展示页都离线 10 分钟后，直播间会自动结束。';

  const render = (state: HostedRuntimeViewState): void => {
    const room = state.roomID ? `（直播间 ${state.roomID}）` : '';
    if (state.connection === 'pending') status.textContent = `运行状态：正在连接${room}`;
    else if (state.connection === 'reconnecting') status.textContent = `运行状态：正在重连${room}`;
    else if (state.runtimeState === 'disabled') status.textContent = '运行状态：账号已停用';
    else if (state.runtimeState === 'shutting_down') status.textContent = '运行状态：服务正在关闭';
    else if (state.runtimeState === 'idle') status.textContent = '运行状态：等待选择直播间';
    else if (state.connection === 'degraded' || state.runtimeState === 'degraded') status.textContent = `运行状态：服务降级${room}`;
    else status.textContent = `运行状态：已连接${room}`;
    authoritative = (state.connection === 'connected' || state.connection === 'degraded') &&
      state.runtimeState !== undefined && state.runtimeState !== 'disabled' && state.runtimeState !== 'shutting_down';
    apply.disabled = busy || !authoritative;
    if (!input.value && state.roomID) input.value = state.roomID;
  };
  const unsubscribe = presence.subscribe(render);
  const confirmSwitch = options.confirm ?? ((message: string) => document.defaultView?.confirm(message) === true);
  apply.addEventListener('click', () => {
    if (disposed || busy || !authoritative) return;
    const roomID = input.value.trim();
    if (!validRuntimeRoomID(roomID)) {
      status.textContent = '直播间号无效';
      return;
    }
    const currentRoom = presence.state().roomID;
    if (currentRoom && currentRoom !== roomID && !confirmSwitch(`确认从直播间 ${currentRoom} 切换到 ${roomID}？`)) return;
    busy = true;
    apply.disabled = true;
    status.textContent = '正在切换直播间';
    void api.setRuntimeRoom(roomID).then(() => {
      if (!disposed) presence.refresh();
    }).catch(() => {
      if (!disposed) status.textContent = '直播间切换失败，请稍后重试';
    }).finally(() => {
      if (!disposed) {
        busy = false;
        apply.disabled = !authoritative;
      }
    });
  });

  panel.append(title, status, label, apply, explanation);
  root.replaceChildren(panel);
  return Object.freeze({
    dispose(): void {
      if (disposed) return;
      disposed = true;
      unsubscribe();
      input.value = '';
      root.replaceChildren();
    },
  });
}

export const validRuntimeRoomID = validHostedRoomID;
