export interface ServerContinuityActions {
  show: (message: string) => void;
  hide: () => void;
  reload: () => void;
}

export interface ServerContinuityController {
  unavailable: (plannedUpdate?: boolean) => void;
  ready: (version: string) => void;
}

let plannedUpdateRestart = false;

export function setPlannedUpdateRestart(planned: boolean): void {
  plannedUpdateRestart = planned;
}

export function plannedUpdateRestartExpected(): boolean {
  return plannedUpdateRestart;
}

export function createServerContinuityController(actions: ServerContinuityActions): ServerContinuityController {
  let connectedVersion = '';
  let unavailable = false;
  return {
    unavailable: (plannedUpdate = false) => {
      unavailable = true;
      actions.show(plannedUpdate
        ? '正在替换程序并重新连接…'
        : '本地服务暂时不可用，可能正在安装更新。页面会自动重连。');
    },
    ready: (version) => {
      const normalized = version.trim();
      const recoveredWithoutBaseline = unavailable && connectedVersion === '';
      const versionChanged = unavailable && connectedVersion !== '' && normalized !== '' && normalized !== connectedVersion;
      if (normalized !== '') connectedVersion = normalized;
      unavailable = false;
      if (recoveredWithoutBaseline || versionChanged) {
        actions.show('更新完成，正在刷新页面…');
        actions.reload();
        return;
      }
      actions.hide();
    },
  };
}
