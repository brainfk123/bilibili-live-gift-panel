import {
  checkAssistantModelUpdate,
  getAssistantStatus,
  installAssistantModel,
  streamAssistantChat,
  type AssistantChatEvent,
  type AssistantHistoryMessage,
  type AssistantSafeAction,
  type AssistantSource,
  type AssistantStatus,
} from '../../backend';
import type { TrainingTopicId } from '../../types';
import { el } from '../common';
import type { ConfigPageId } from './config-route';
import { normalizeAssistantMarkdown, renderAssistantMarkdown } from './assistant-markdown';

interface AssistantDrawerOptions {
  getSuggestions?: () => string[];
  onClose: () => void;
  onNavigatePage: (page: ConfigPageId) => void;
  onOpenTrainingTopic: (topic: TrainingTopicId) => void;
  onNotify: (message: string) => void;
}

interface AssistantReplyMetadata {
  appVersion?: string;
  modelVersion?: string;
  stateSummary?: Record<string, unknown>;
}

interface ChatMessage {
  role: 'user' | 'assistant';
  content: string;
  sources?: AssistantSource[];
  metadata?: AssistantReplyMetadata;
  question?: string;
  pending?: boolean;
  error?: boolean;
}

export interface AssistantFeedbackInput {
  question: string;
  answer: string;
  sources?: AssistantSource[];
  appVersion?: string | null;
  modelVersion?: string | null;
  stateSummary?: Record<string, unknown> | null;
}

export interface AssistantDrawer {
  element: HTMLElement;
  open: () => void;
  close: () => void;
  dispose: () => void;
  refresh: () => Promise<void>;
}

const CONFIG_PAGES = new Set<ConfigPageId>(['overview', 'attributes', 'activities', 'kpi', 'obs', 'data']);
const TRAINING_TOPICS = new Set<TrainingTopicId>([
  'multi-gift', 'blind-box', 'manual-gift', 'advanced-rule', 'cross-attribute', 'display-format',
  'broadcast-output', 'combined-scenes', 'activity-session', 'contribution-ranking', 'rule-no-effect',
  'timer-skipped', 'obs-no-change',
]);
const DEFAULT_STATUS: AssistantStatus = { state: 'missing', message: '正在读取模型状态…' };
const DOWNLOAD_SIZE = 639 * 1024 * 1024;
const UPDATE_CHECK_STORAGE_KEY = 'bilibili-gift-panel.assistant-model-last-check-at';
const UPDATE_CHECK_INTERVAL_MS = 24 * 60 * 60 * 1000;
const SAFE_FEEDBACK_STATE_FIELDS = [
  'appVersion', 'connection', 'login', 'roomConfigured', 'attributeCount', 'ruleCount', 'timerCount', 'currentError',
] as const;
const REDACTED_FEEDBACK_FIELDS = ['roomId', 'username', 'audienceData', 'cookies', 'fullConfig', 'logs'] as const;

export function buildAssistantFeedbackJSON(input: AssistantFeedbackInput): string {
  const stateSummary = input.stateSummary
    ? Object.fromEntries(SAFE_FEEDBACK_STATE_FIELDS
      .filter((field) => Object.hasOwn(input.stateSummary as Record<string, unknown>, field))
      .map((field) => [field, input.stateSummary?.[field]]))
    : null;
  return JSON.stringify({
    schemaVersion: 1,
    redaction: {
      status: 'redacted',
      excludedFields: REDACTED_FEEDBACK_FIELDS,
    },
    question: input.question,
    answer: input.answer,
    sources: input.sources?.map(({ id, title, sourceLabel }) => ({ id, title, sourceLabel })) ?? [],
    appVersion: input.appVersion ?? null,
    modelVersion: input.modelVersion ?? null,
    stateSummary,
  }, null, 2);
}

export function createAssistantDrawer(options: AssistantDrawerOptions): AssistantDrawer {
  let status = DEFAULT_STATUS;
  let visible = false;
  let messages: ChatMessage[] = [];
  let generation: AbortController | undefined;
  let pollTimer: ReturnType<typeof globalThis.setTimeout> | undefined;
  let checkingStatus = false;
  let hasCheckedUpdate = false;

  const drawer = el('aside', {
    class: 'assistant-drawer',
    ariaLabel: '答疑助手 Beta',
  } as any);
  drawer.hidden = true;
  const closeButton = el('button', { class: 'assistant-icon-button', type: 'button', text: '×' }) as HTMLButtonElement;
  closeButton.setAttribute('aria-label', '关闭答疑助手并清空对话');
  closeButton.title = '关闭并清空本次对话';
  const header = el('header', { class: 'assistant-drawer-header' }, [
    el('div', {}, [
      el('div', { class: 'assistant-title-line' }, [
        el('h2', { text: '答疑助手' }),
        el('span', { class: 'assistant-beta-badge', text: 'Beta' }),
      ]),
      el('p', { text: '只回答本项目的安装、配置和故障排查问题' }),
    ]),
    closeButton,
  ]);
  const body = el('div', { class: 'assistant-drawer-body', ariaLive: 'polite' } as any);
  const footer = el('footer', { class: 'assistant-composer' });
  drawer.append(header, body, footer);

  const clearPoll = (): void => {
    if (pollTimer !== undefined) globalThis.clearTimeout(pollTimer);
    pollTimer = undefined;
  };

  const scheduleStatusPoll = (): void => {
    clearPoll();
    if (!visible || !['downloading', 'loading'].includes(status.state)) return;
    pollTimer = globalThis.setTimeout(() => void refreshStatus(), 700);
  };

  async function refreshStatus(): Promise<void> {
    if (checkingStatus) return;
    checkingStatus = true;
    try {
      status = await getAssistantStatus();
    } catch (error) {
      status = {
        state: 'error',
        message: error instanceof Error ? error.message : '无法读取答疑模型状态',
      };
    } finally {
      checkingStatus = false;
      if (visible) render();
      scheduleStatusPoll();
    }
  }

  async function runStatusAction(action: () => Promise<AssistantStatus>): Promise<void> {
    clearPoll();
    try {
      status = await action();
    } catch (error) {
      status = { state: 'error', message: error instanceof Error ? error.message : '答疑模型操作失败' };
    }
    render();
    scheduleStatusPoll();
  }

  function render(): void {
    body.replaceChildren();
    footer.replaceChildren();
    if (status.state === 'missing' || status.state === 'error') {
      renderInstallState();
      return;
    }
    if (status.state === 'downloading' || status.state === 'loading') {
      renderProgressState();
      return;
    }
    renderChatState();
  }

  function renderInstallState(): void {
    const install = el('button', { class: 'btn assistant-primary-action', type: 'button', text: status.state === 'error' ? '重试安装或校验' : '下载并安装模型' }) as HTMLButtonElement;
    install.onclick = () => void runStatusAction(installAssistantModel);
    body.append(el('section', { class: 'assistant-install-card' }, [
      el('div', { class: 'assistant-install-mark', text: 'AI' }),
      el('div', {}, [
        el('span', { class: 'section-kicker', text: status.state === 'error' ? '暂时不可用' : '首次使用' }),
        el('h3', { text: status.state === 'error' ? '答疑模型未能就绪' : '需要安装本地答疑模型' }),
        el('p', { text: status.message || '下载完成后，问题和答案都只在这台电脑上处理。' }),
      ]),
      el('ul', { class: 'assistant-consent-list' }, [
        el('li', { text: '下载约 639MB，来源为 ModelScope 上的 Qwen 官方模型' }),
        el('li', { text: '问答仅在本机运行，不上传配置、日志或聊天记录' }),
        el('li', { text: '模型使用 Apache 2.0 许可证，可随时在“程序与数据”中删除' }),
      ]),
      install,
    ]));
  }

  function renderProgressState(): void {
    const fraction = normalizedProgress(status);
    const loaded = status.installedBytes ?? Math.round((status.sizeBytes ?? DOWNLOAD_SIZE) * fraction);
    const total = status.sizeBytes ?? DOWNLOAD_SIZE;
    body.append(el('section', { class: 'assistant-progress-card' }, [
      el('div', { class: 'assistant-progress-heading' }, [
        el('strong', { text: status.state === 'loading' ? '正在加载模型' : '正在下载答疑模型' }),
        el('span', { text: status.state === 'loading' ? '请稍候' : `${Math.round(fraction * 100)}%` }),
      ]),
      el('div', { class: 'assistant-progress-track', role: 'progressbar' } as any, [
        el('span', { style: `width:${Math.round(fraction * 100)}%` }),
      ]),
      el('p', { text: status.state === 'loading' ? status.message : `${formatBytes(loaded)} / ${formatBytes(total)} · 关闭侧栏不会中断下载` }),
    ]));
  }

  function renderChatState(): void {
    if (status.updateAvailable) renderUpdateNotice();
    const suggestions = currentSuggestions();
    const messageList = el('div', { class: 'assistant-message-list' });
    if (messages.length === 0) {
      messageList.append(el('section', { class: 'assistant-welcome' }, [
        el('strong', { text: '可以这样问我' }),
        el('p', { text: '我会优先引用项目帮助，并根据当前连接和配置数量给出步骤。' }),
        renderSuggestions(suggestions),
      ]));
    } else {
      messages.forEach((message, index) => messageList.append(renderMessage(message, index)));
    }
    body.append(messageList);

    const input = el('textarea', {
      class: 'assistant-question-input',
      placeholder: suggestions[0] ?? '为什么收到礼物但数值没变化？',
      rows: 2,
      maxLength: 1000,
      disabled: Boolean(generation),
    }) as HTMLTextAreaElement;
    const counter = el('span', { text: '0/1000' });
    input.oninput = () => { counter.textContent = `${input.value.length}/1000`; };
    input.onkeydown = (event) => {
      if (event.key === 'Enter' && !event.shiftKey && !event.isComposing) {
        event.preventDefault();
        void submitQuestion(input.value);
      }
    };
    const submit = el('button', { class: 'btn', type: 'button', text: '发送', disabled: Boolean(generation) }) as HTMLButtonElement;
    submit.onclick = () => void submitQuestion(input.value);
    const stop = el('button', { class: 'btn ghost', type: 'button', text: '停止回答' }) as HTMLButtonElement;
    stop.onclick = () => generation?.abort();
    footer.append(input, el('div', { class: 'assistant-composer-actions' }, [counter, generation ? stop : submit]));
    if (!generation) input.focus();
  }

  function renderUpdateNotice(): HTMLElement {
    const notice = el('section', { class: 'assistant-update-notice' }, [
      el('div', {}, [
        el('strong', { text: '有新的答疑模型' }),
        el('p', { text: `当前 ${status.modelVersion ?? '已安装'}，新版本 ${status.latestVersion ?? '可用'}。请在“程序与数据”中管理。` }),
      ]),
    ]);
    body.append(notice);
    return notice;
  }

  function renderSuggestions(suggestions: string[]): HTMLElement {
    const group = el('div', { class: 'assistant-suggestions' });
    for (const suggestion of suggestions) {
      const button = el('button', { type: 'button', text: suggestion }) as HTMLButtonElement;
      button.onclick = () => void submitQuestion(suggestion);
      group.append(button);
    }
    return group;
  }

  function renderMessage(message: ChatMessage, index: number): HTMLElement {
    const article = el('article', { class: `assistant-message is-${message.role}${message.error ? ' is-error' : ''}` });
    article.append(el('span', { class: 'assistant-message-role', text: message.role === 'user' ? '你' : '答疑助手' }));
    const content = el('div', { class: 'assistant-message-content' });
    const rawDisplayText = message.content || (message.pending ? '正在查找项目帮助…' : '');
    const displayText = message.role === 'assistant' && !message.error
      ? normalizeAssistantMarkdown(rawDisplayText)
      : rawDisplayText;
    if (message.pending && !message.content) content.textContent = displayText;
    else content.append(renderAssistantMarkdown(displayText));
    if (message.pending) content.classList.add('is-pending');
    article.append(content);
    if (message.sources?.length) article.append(renderSources(message.sources));
    if (message.role === 'assistant' && !message.pending && !message.error && message.content) {
      const unresolved = el('button', { class: 'assistant-feedback-toggle', type: 'button', text: '没解决' }) as HTMLButtonElement;
      const host = el('div', { class: 'assistant-feedback-host' });
      unresolved.onclick = () => renderFeedbackPreview(message, index, host);
      article.append(unresolved, host);
    }
    return article;
  }

  function renderSources(sources: AssistantSource[]): HTMLElement {
    const list = el('div', { class: 'assistant-sources' }, [el('strong', { text: '参考帮助' })]);
    for (const source of sources) {
      const row = el('div', { class: 'assistant-source' }, [
        el('div', {}, [el('span', { text: source.sourceLabel }), el('strong', { text: source.title })]),
      ]);
      if (source.action && isSafeAction(source.action)) {
        const action = el('button', { type: 'button', text: source.action.label || '带我去' }) as HTMLButtonElement;
        action.onclick = () => activateSafeAction(source.action as AssistantSafeAction);
        row.append(action);
      }
      list.append(row);
    }
    return list;
  }

  function renderFeedbackPreview(message: ChatMessage, index: number, host: HTMLElement): void {
    if (host.childElementCount > 0) {
      host.replaceChildren();
      return;
    }
    const payload = buildAssistantFeedbackJSON({
      question: message.question ?? messages[index - 1]?.content ?? '',
      answer: message.content,
      sources: message.sources,
      appVersion: message.metadata?.appVersion ?? null,
      modelVersion: message.metadata?.modelVersion ?? status.modelVersion ?? null,
      stateSummary: message.metadata?.stateSummary ?? null,
    });
    const preview = el('pre', { class: 'assistant-feedback-preview', text: payload });
    const copy = el('button', { class: 'btn ghost small', type: 'button', text: '复制 JSON' }) as HTMLButtonElement;
    copy.onclick = async () => {
      try {
        await globalThis.navigator?.clipboard?.writeText(payload);
        options.onNotify('已复制脱敏反馈 JSON，可手动粘贴到 Issue');
      } catch {
        options.onNotify('无法自动复制，请从预览中手动选择文本');
      }
    };
    host.append(el('p', { text: '内容不会自动上传。确认不含隐私后，可手动提交到 Issue。' }), preview, copy);
  }

  async function submitQuestion(rawQuestion: string): Promise<void> {
    const question = rawQuestion.trim();
    if (!question || generation) return;
    const history = messages
      .filter((message) => !message.pending && !message.error && message.content)
      .slice(-8)
      .map<AssistantHistoryMessage>((message) => ({ role: message.role, content: message.content }));
    messages.push({ role: 'user', content: question });
    const reply: ChatMessage = { role: 'assistant', content: '', question, pending: true };
    messages.push(reply);
    const controller = new AbortController();
    generation = controller;
    render();
    try {
      await streamAssistantChat(question, history, (event) => applyChatEvent(reply, event), controller.signal);
      reply.pending = false;
      if (!reply.content) reply.content = '没有生成有效回答，请换一种问法重试。';
    } catch (error) {
      reply.pending = false;
      if (error instanceof DOMException && error.name === 'AbortError') {
        reply.content = reply.content || '已停止回答。';
      } else {
        reply.error = true;
        reply.content = error instanceof Error ? error.message : '答疑失败，请稍后重试。';
      }
    } finally {
      if (generation === controller) generation = undefined;
      if (visible) {
        render();
        body.scrollTop = body.scrollHeight;
      }
    }
  }

  function applyChatEvent(reply: ChatMessage, event: AssistantChatEvent): void {
    if (event.type === 'sources') reply.sources = event.sources;
    if (event.type === 'delta') {
      reply.pending = false;
      reply.content += event.text;
    }
    if (event.type === 'done') {
      reply.pending = false;
      reply.content = normalizeAssistantMarkdown(reply.content);
      reply.metadata = event;
    }
    if (event.type === 'error') {
      reply.pending = false;
      reply.error = true;
      reply.content = event.message;
    }
    if (visible) {
      render();
      body.scrollTop = body.scrollHeight;
    }
  }

  function activateSafeAction(action: AssistantSafeAction): void {
    if (action.kind === 'config-page' && CONFIG_PAGES.has(action.target as ConfigPageId)) {
      options.onNavigatePage(action.target as ConfigPageId);
      return;
    }
    if (action.kind === 'training-topic' && TRAINING_TOPICS.has(action.target as TrainingTopicId)) {
      options.onOpenTrainingTopic(action.target as TrainingTopicId);
    }
  }

  function currentSuggestions(): string[] {
    const configured = options.getSuggestions?.().map((suggestion) => suggestion.trim()).filter(Boolean) ?? [];
    const unique = [...new Set(configured)].slice(0, 3);
    return unique.length > 0 ? unique : ['怎么连接直播间？', '收到礼物但数值没变化怎么办？', '怎么把面板添加到 OBS？'];
  }

  function onKeyDown(event: KeyboardEvent): void {
    if (visible && event.key === 'Escape') close();
  }

  function open(): void {
    visible = true;
    drawer.hidden = false;
    drawer.classList.add('is-open');
    render();
    void refreshStatus().then(async () => {
      if (!visible || hasCheckedUpdate || !['installed', 'ready'].includes(status.state) || !shouldCheckUpdateNow()) return;
      hasCheckedUpdate = true;
      recordUpdateCheckAttempt();
      try {
        status = await checkAssistantModelUpdate();
        if (visible) render();
      } catch {
        // Update checks are advisory and must not block local questions.
      }
    });
  }

  function close(): void {
    generation?.abort();
    generation = undefined;
    clearPoll();
    messages = [];
    visible = false;
    drawer.classList.remove('is-open');
    drawer.hidden = true;
    options.onClose();
  }

  function dispose(): void {
    close();
    globalThis.removeEventListener?.('keydown', onKeyDown);
    drawer.remove();
  }

  closeButton.onclick = close;
  globalThis.addEventListener?.('keydown', onKeyDown);
  return { element: drawer, open, close, dispose, refresh: refreshStatus };
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

function isSafeAction(action: AssistantSafeAction): boolean {
  if (action.kind === 'config-page') return CONFIG_PAGES.has(action.target as ConfigPageId);
  if (action.kind === 'training-topic') return TRAINING_TOPICS.has(action.target as TrainingTopicId);
  return false;
}

function shouldCheckUpdateNow(now = Date.now()): boolean {
  try {
    const previous = Number(globalThis.localStorage?.getItem(UPDATE_CHECK_STORAGE_KEY));
    return !Number.isFinite(previous) || previous <= 0 || now - previous >= UPDATE_CHECK_INTERVAL_MS;
  } catch {
    return true;
  }
}

function recordUpdateCheckAttempt(now = Date.now()): void {
  try {
    globalThis.localStorage?.setItem(UPDATE_CHECK_STORAGE_KEY, String(now));
  } catch {
    // Update checks still work when storage is unavailable.
  }
}
