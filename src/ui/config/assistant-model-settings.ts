import {
  checkAssistantModelUpdate,
  deleteAssistantModel,
  getAssistantStatus,
  installAssistantModel,
  updateAssistantModel,
  type AssistantStatus,
} from '../../backend';
import { el } from '../common';

interface AssistantModelSettingsOptions {
  onChanged?: () => void;
  onNotify: (message: string) => void;
}

export interface AssistantModelSettings {
  element: HTMLElement;
  dispose: () => void;
  refresh: () => Promise<void>;
}

const DOWNLOAD_SIZE = 639 * 1024 * 1024;
const INITIAL_STATUS: AssistantStatus = { state: 'missing', message: '正在读取本地模型状态…' };

export function createAssistantModelSettings(options: AssistantModelSettingsOptions): AssistantModelSettings {
  let status = INITIAL_STATUS;
  let busy = false;
  let disposed = false;
  let pollTimer: ReturnType<typeof globalThis.setTimeout> | undefined;
  const section = el('section', { class: 'data-update-section assistant-model-settings-card' });

  const clearPoll = (): void => {
    if (pollTimer !== undefined) globalThis.clearTimeout(pollTimer);
    pollTimer = undefined;
  };

  const schedulePoll = (): void => {
    clearPoll();
    if (disposed || !['downloading', 'loading'].includes(status.state)) return;
    pollTimer = globalThis.setTimeout(() => void refresh(), 700);
  };

  async function refresh(): Promise<void> {
    if (disposed) return;
    try {
      status = await getAssistantStatus();
    } catch (error) {
      status = { state: 'error', message: error instanceof Error ? error.message : '无法读取本地模型状态' };
    }
    if (!disposed) {
      render();
      schedulePoll();
    }
  }

  async function runAction(action: () => Promise<AssistantStatus>, successMessage?: string): Promise<void> {
    if (busy || disposed) return;
    busy = true;
    render();
    try {
      status = await action();
      if (successMessage) options.onNotify(successMessage);
      options.onChanged?.();
    } catch (error) {
      status = { state: 'error', message: error instanceof Error ? error.message : '本地模型操作失败' };
      options.onNotify(status.message);
    } finally {
      busy = false;
      if (!disposed) {
        render();
        schedulePoll();
      }
    }
  }

  function render(): void {
    section.dataset.modelState = status.state;
    const stateBadge = el('span', {
      class: `update-state-badge assistant-model-state is-${status.state}`,
      text: stateLabel(status),
    });
    const version = status.modelVersion
      ? status.modelVersion
      : status.state === 'missing' ? '未安装' : '读取中…';
    const heading = el('div', { class: 'update-heading' }, [
      el('div', {}, [
        el('h3', { text: '本地答疑模型' }),
        el('span', { class: 'update-version-label', text: '当前版本' }),
        el('strong', { class: 'update-current-version assistant-model-version', text: version }),
      ]),
      stateBadge,
    ]);
    const description = el('p', {
      class: 'advanced-copy assistant-model-description',
      text: statusMessage(status),
    });
    const progressTrack = renderProgress(status);
    const actions = el('div', { class: 'assistant-model-settings-actions' });

    if (status.state === 'missing' || status.state === 'error') {
      const install = el('button', {
        class: 'btn', type: 'button', disabled: busy,
        text: status.state === 'error' ? '重试安装或校验' : '安装本地模型',
      }) as HTMLButtonElement;
      install.onclick = () => void runAction(installAssistantModel);
      actions.append(install);
    } else {
      const check = el('button', {
        class: 'btn ghost', type: 'button', disabled: busy || ['downloading', 'loading', 'answering'].includes(status.state),
        text: busy ? '处理中…' : '检查模型更新',
      }) as HTMLButtonElement;
      check.onclick = () => void runAction(checkAssistantModelUpdate, '模型更新检查完成');
      actions.append(check);
      if (status.updateAvailable) {
        const update = el('button', { class: 'btn', type: 'button', disabled: busy, text: '更新模型' }) as HTMLButtonElement;
        update.onclick = () => void runAction(updateAssistantModel);
        actions.append(update);
      }
      const remove = el('button', {
        class: 'btn danger', type: 'button', disabled: busy || ['downloading', 'loading', 'answering'].includes(status.state),
        text: '删除本地模型',
      }) as HTMLButtonElement;
      remove.onclick = () => {
        if (globalThis.confirm?.('删除本地答疑模型？之后需要重新下载约 639MB 才能使用答疑助手。')) {
          void runAction(deleteAssistantModel, '本地答疑模型已删除');
        }
      };
      actions.append(remove);
    }

    section.replaceChildren(heading, description, progressTrack, actions);
  }

  function dispose(): void {
    disposed = true;
    clearPoll();
  }

  render();
  void refresh();
  return { element: section, dispose, refresh };
}

function stateLabel(status: AssistantStatus): string {
  if (status.updateAvailable) return '有更新';
  return {
    missing: '未安装', downloading: '下载中', installed: '已安装', loading: '加载中',
    ready: '可用', answering: '回答中', error: '异常',
  }[status.state];
}

function statusMessage(status: AssistantStatus): string {
  if (status.message) return status.message;
  if (status.state === 'missing') return '尚未安装。首次使用会下载约 639MB，模型仅在本机运行。';
  if (status.state === 'downloading') return '正在下载 Qwen3-0.6B Q8_0；关闭设置页不会中断下载。';
  if (status.state === 'loading') return '正在加载并校验模型，请稍候。';
  if (status.updateAvailable) return `Qwen3-0.6B Q8_0 · 新版本 ${status.latestVersion ?? '可用'} · 本机处理 · 对话不保存`;
  return 'Qwen3-0.6B Q8_0 · 本机处理 · 对话不保存';
}

function renderProgress(status: AssistantStatus): HTMLElement {
  const progress = normalizedProgress(status);
  const total = status.sizeBytes ?? DOWNLOAD_SIZE;
  const loaded = status.installedBytes ?? Math.round(total * progress);
  const track = el('div', {
    class: 'assistant-model-settings-progress',
    ariaLabel: '答疑模型下载进度',
  } as any, [el('span', { style: `width:${Math.round(progress * 100)}%` })]);
  track.hidden = !['downloading', 'loading'].includes(status.state);
  if (!track.hidden) {
    track.append(el('small', {
      text: status.state === 'loading' ? '正在加载模型' : `${formatBytes(loaded)} / ${formatBytes(total)}`,
    }));
  }
  return track;
}

function normalizedProgress(status: AssistantStatus): number {
  if (typeof status.progress === 'number') {
    const value = status.progress > 1 ? status.progress / 100 : status.progress;
    return Math.min(1, Math.max(0, value));
  }
  if (status.installedBytes && status.sizeBytes) return Math.min(1, status.installedBytes / status.sizeBytes);
  return 0;
}

function formatBytes(bytes: number): string {
  if (!Number.isFinite(bytes) || bytes <= 0) return '0 MB';
  return `${(bytes / 1024 / 1024).toFixed(bytes >= 100 * 1024 * 1024 ? 0 : 1)} MB`;
}
