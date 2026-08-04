import { getRuntimeStatus, RuntimeConnectionState } from '../../backend';
import { formatValue } from '../../format';
import { findGift } from '../../gifts/catalog';
import { loadState, refreshStateFromServer } from '../../storage';
import { AppState, Attribute, LogEntry, MAX_LOG } from '../../types';
import { getDisplayTheme, resolveAttributeDisplayTheme, resolveAttributeDisplayVariant } from '../../display-themes';
import { createBrandIcon } from '../brand';
import { el } from '../common';
import { SequentialBroadcastQueue } from './broadcast-queue';
import { resolveDisplayTarget, type DisplayTarget } from '../../display-scenes';
import { activityForScene, activityStatusLabel } from '../../activities';

const DEFAULT_BROADCAST_MESSAGE = '感谢大家的支持，欢迎投喂礼物';
const GIFT_BROADCAST_DURATION = 5500;
const BROADCAST_TRANSITION_DURATION = 450;

export function formatDelta(delta: number, attr?: Attribute): string {
  const sign = delta > 0 ? '+' : delta < 0 ? '-' : '';
  const value = attr ? formatValue(Math.abs(delta), attr) : String(Math.abs(delta));
  return `${sign}${value}`;
}

export function calculateFittedFontSize(
  preferredFontSize: number,
  availableWidth: number,
  contentWidth: number,
  minimumFontSize = 14,
): number {
  if (availableWidth <= 0 || contentWidth <= availableWidth) return preferredFontSize;
  return Math.max(minimumFontSize, Math.floor(preferredFontSize * availableWidth / contentWidth));
}

export function resolveAttributeValuePresentation(attribute: Attribute): { text: string; color?: string; imageUrl?: string } {
  const mapping = attribute.display?.variant === 'enum'
    ? attribute.display.valueMappings?.find((candidate) => candidate.value === attribute.value)
    : undefined;
  if (!mapping) return { text: formatValue(attribute.value, attribute) };
  return {
    text: mapping.label,
    ...(mapping.color ? { color: mapping.color } : {}),
    ...(mapping.imageUrl ? { imageUrl: mapping.imageUrl } : {}),
  };
}

export function mountDisplay(root: HTMLElement, target: DisplayTarget = {}): void {
  let state = loadState();
  let connectionState: RuntimeConnectionState = 'idle';
  let lastLogKey = state.log[0] ? logKey(state.log[0]) : '';
  let stateRefreshActive = false;
  const stack = el('div', { class: 'display-stack' });
  const panel = el('div', { class: 'panel' });
  const attrEls = new Map<string, HTMLElement>();
  let broadcastStage: HTMLElement | null = null;
  stack.append(panel);
  root.replaceChildren(stack);

  const currentTarget = () => resolveDisplayTarget(state, target);

  function giftBroadcastDuration(pendingCount: number): number {
    if (pendingCount >= 12) return 2200;
    if (pendingCount >= 4) return 3500;
    return GIFT_BROADCAST_DURATION;
  }

  const broadcastQueue = new SequentialBroadcastQueue<LogEntry>({
    durationMs: giftBroadcastDuration,
    keyOf: logKey,
    maxPending: MAX_LOG,
    onShow: (entry) => renderGiftBroadcast(entry),
    onIdle: () => {
      const latestAttribute = defaultBroadcastAttribute();
      if (latestAttribute) swapBroadcastContent(createDefaultBroadcast(latestAttribute));
    },
  });

  function defaultBroadcastAttribute(): Attribute | undefined {
    const attributes = currentTarget().attributes;
    return attributes.find((attribute) => attribute.broadcastMessage?.trim()) ?? attributes[0];
  }

  function renderAttrs(): void {
    broadcastQueue.pause();
    panel.replaceChildren();
    attrEls.clear();
    broadcastStage = null;
    const resolved = currentTarget();
    const attributes = resolved.attributes;
    const scene = resolved.scene;
    const activity = activityForScene(state, scene?.id);
    stack.classList.toggle('is-scene', Boolean(scene));
    stack.classList.toggle('is-scene-grid', scene?.layout === 'grid');
    panel.classList.toggle('is-scene', Boolean(scene));
    panel.classList.toggle('scene-layout-grid', scene?.layout === 'grid');
    panel.classList.toggle('scene-layout-stack', scene?.layout === 'stack');
    const panelThemeId = scene?.themeId ?? (attributes.length === 1
      ? resolveAttributeDisplayTheme(attributes[0], state.settings)
      : state.settings.defaultDisplayThemeId);
    panel.dataset.theme = panelThemeId;
    panel.classList.toggle('has-mixed-themes', attributes.length > 1 && !scene);
    if (scene) {
      panel.append(el('header', { class: 'display-scene-header' }, [
        el('div', {}, [
          el('span', { text: '组合面板' }),
          el('h1', { text: scene.name, title: scene.name }),
        ]),
        el('div', { class: 'display-scene-meta' }, [
          el('strong', { text: `${attributes.length} 个属性` }),
          ...(activity ? [el('span', { class: `display-activity-status is-${activity.status}`, text: activityStatusLabel(activity.status) })] : []),
        ]),
      ]));
      if (activity?.giftTimeout) {
        panel.append(el('div', { class: 'display-activity-timeout' }, [
          el('span', { text: '送礼后倒计时' }),
          el('strong', { text: '等待活动开始' }),
        ]));
      }
      const latestMilestone = activity?.milestones
        .filter((milestone) => milestone.triggeredAt)
        .sort((left, right) => Number(right.triggeredAt) - Number(left.triggeredAt))[0];
      if (latestMilestone) {
        panel.append(el('div', { class: 'display-activity-milestone' }, [
          el('span', { text: '◆ 里程碑达成' }),
          el('strong', { text: latestMilestone.message || latestMilestone.name, title: latestMilestone.message || latestMilestone.name }),
        ]));
      }
      if (activity?.status === 'settled' && activity.result) {
        const winner = activity.result.winnerAttributeName;
        panel.append(el('div', { class: 'display-activity-result' }, [
          el('span', { text: activity.resultMode === 'none' ? '结算完成' : winner ? '本局胜出' : '本局平局' }),
          el('strong', { text: winner ?? activity.name }),
        ]));
      }
    }
    if (attributes.length === 0) {
      const empty = el('div', { class: 'display-empty' });
      empty.append(
        createBrandIcon(64, 'display-empty-brand-icon'),
        el('div', { text: resolved.missingLabel ?? '还没有可显示的属性' }),
      );
      panel.append(empty);
    }
    for (const attr of attributes) {
      const variant = resolveAttributeDisplayVariant(attr);
      const themeId = scene?.themeId ?? resolveAttributeDisplayTheme(attr, state.settings);
      const summary = el('div', { class: 'attr-summary' }, [
        el('div', { class: 'attr-name', text: attr.display?.title?.trim() || attr.name, title: attr.display?.title?.trim() || attr.name }),
        el('div', { class: 'attr-value' }),
      ]);
      if (['progress', 'health', 'resource', 'tug'].includes(variant)) summary.append(createAttributeMeter(attr, variant));
      const giftRules = el('div', { class: 'display-gift-rules' });
      const rules = state.rules.filter((rule) => rule.attributeName === attr.name && rule.enabled !== false);
      if (rules.length === 0) {
        giftRules.append(el('div', { class: 'display-rule-empty', text: '还没有配置礼物规则' }));
      } else {
        for (const rule of rules) {
          const gift = findGift(state, rule.giftId);
          const formulaName = rule.formulaName?.trim() || '未命名规则';
          const formulaNameClass = Array.from(formulaName).length > 7
            ? 'display-formula-name is-long'
            : 'display-formula-name';
          const image = el('img', { class: 'display-gift-image', alt: gift?.name ?? '' }) as HTMLImageElement;
          image.src = gift?.imgBasic || transparentPixel();
          giftRules.append(el('div', { class: 'display-gift-rule' }, [
            image,
            el('div', { class: 'display-gift-copy' }, [
              el('span', { class: 'display-gift-name', text: gift?.name ?? `礼物 ${rule.giftId}` }),
              el('strong', { class: formulaNameClass, text: formulaName, title: formulaName }),
            ]),
          ]));
        }
      }
      const block = el('section', { class: `attr is-${variant}` }, [summary, giftRules]);
      block.dataset.theme = themeId;
      block.dataset.variant = variant;
      attrEls.set(attr.name, block);
      panel.append(block);
    }
    const defaultAttribute = defaultBroadcastAttribute();
    if (defaultAttribute) {
      broadcastStage = el('div', { class: 'broadcast-stage' }, [createDefaultBroadcast(defaultAttribute)]);
      const ticker = el('div', {
        class: `broadcast-ticker${scene ? ' display-scene-broadcast' : ''}`,
      }, [broadcastStage]);
      if (scene) panel.append(ticker);
      else attrEls.get(defaultAttribute.name)?.append(ticker);
    }
    panel.append(el('div', { class: 'conn' }));
    updateAll();
    if (broadcastStage) broadcastQueue.resume();
  }

  function updateAll(): void {
    const opacity = Math.min(100, Math.max(10, Number(state.settings.panelOpacity) || 55)) / 100;
    root.style.setProperty('--accent', state.settings.accentColor);
    root.style.setProperty('--accent2', state.settings.accentColor);
    root.style.setProperty('--panel-opacity', String(opacity));
    root.style.setProperty('--panel-surface-opacity', String(opacity * 0.62));
    const resolved = currentTarget();
    const attributes = resolved.attributes;
    const scene = resolved.scene;
    const panelTheme = getDisplayTheme(scene?.themeId ?? (attributes.length === 1
      ? resolveAttributeDisplayTheme(attributes[0], state.settings)
      : state.settings.defaultDisplayThemeId));
    panel.dataset.theme = panelTheme.id;
    panel.style.setProperty('--theme-accent', panelTheme.id === 'glass' ? state.settings.accentColor : panelTheme.accent);
    panel.style.setProperty('--theme-surface', panelTheme.surface);
    stack.classList.toggle('center', state.settings.align === 'center');
    stack.classList.toggle('right', state.settings.align === 'right');
    for (const attr of attributes) {
      const block = attrEls.get(attr.name);
      if (!block) continue;
      const theme = getDisplayTheme(scene?.themeId ?? resolveAttributeDisplayTheme(attr, state.settings));
      const variant = resolveAttributeDisplayVariant(attr);
      block.dataset.theme = theme.id;
      block.dataset.variant = variant;
      block.style.setProperty('--theme-accent', theme.id === 'glass' ? state.settings.accentColor : theme.accent);
      block.style.setProperty('--theme-surface', theme.surface);
      const meter = block.querySelector<HTMLElement>('.attr-meter');
      if (meter) {
        const minimum = Number.isFinite(attr.display?.min) ? Number(attr.display?.min) : 0;
        const defaultMaximum = Math.max(100, Math.abs(attr.value), minimum + 1);
        const maximum = Number.isFinite(attr.display?.max) && Number(attr.display?.max) > minimum
          ? Number(attr.display?.max)
          : defaultMaximum;
        const progress = Math.max(0, Math.min(100, ((attr.value - minimum) / (maximum - minimum)) * 100));
        meter.style.setProperty('--meter-progress', `${progress}%`);
        meter.classList.toggle('is-low', variant === 'health' && progress <= (attr.display?.lowThreshold ?? 20));
        const minimumLabel = meter.querySelector<HTMLElement>('.attr-meter-min');
        const maximumLabel = meter.querySelector<HTMLElement>('.attr-meter-max');
        if (minimumLabel) minimumLabel.textContent = attr.display?.leftLabel?.trim() || formatMeterBoundary(minimum, attr);
        if (maximumLabel) maximumLabel.textContent = attr.display?.rightLabel?.trim() || formatMeterBoundary(maximum, attr);
      }
      const valueEl = block.querySelector('.attr-value') as HTMLElement | null;
      if (valueEl) {
        const presentation = resolveAttributeValuePresentation(attr);
        valueEl.style.fontSize = `${state.settings.fontSize}px`;
        const formattedValue = formatValue(attr.value, attr);
        valueEl.classList.toggle('is-enum-mapped', Boolean(presentation.color || presentation.imageUrl || variant === 'enum' && presentation.text !== formattedValue));
        valueEl.style.setProperty('--enum-value-color', presentation.color ?? 'var(--theme-accent, var(--accent))');
        if (presentation.imageUrl) {
          const image = el('img', { class: 'attr-enum-image', alt: '', referrerPolicy: 'no-referrer' }) as HTMLImageElement;
          image.src = presentation.imageUrl;
          image.onerror = () => image.remove();
          valueEl.replaceChildren(image, el('span', { text: presentation.text }));
        } else {
          valueEl.textContent = presentation.text;
        }
        valueEl.title = presentation.text;
        const fittedFontSize = calculateFittedFontSize(
          state.settings.fontSize,
          valueEl.clientWidth,
          valueEl.scrollWidth,
        );
        valueEl.style.fontSize = `${fittedFontSize}px`;
        valueEl.dataset.fittedFontSize = String(fittedFontSize);
      }
    }
    updateActivityTimeout();
    updateConnection();
  }

  function updateActivityTimeout(): void {
    const timeout = panel.querySelector<HTMLElement>('.display-activity-timeout');
    if (!timeout) return;
    const resolved = currentTarget();
    const activity = activityForScene(state, resolved.scene?.id);
    const value = timeout.querySelector<HTMLElement>('strong');
    if (!value || !activity?.giftTimeout) return;
    if (activity.status !== 'active') {
      value.textContent = activity.status === 'not_started' ? '活动开始后等待礼物' : '已停止';
      timeout.classList.remove('is-running');
      return;
    }
    if (!activity.giftTimeout.deadlineAt) {
      value.textContent = '等待第一份生效礼物';
      timeout.classList.remove('is-running');
      return;
    }
    const remaining = Math.max(0, Math.ceil((activity.giftTimeout.deadlineAt - Date.now()) / 1000));
    value.textContent = `${formatCountdown(remaining)} 后${timeoutActionText(activity.giftTimeout.action)}`;
    timeout.classList.add('is-running');
  }

  function updateConnection(): void {
    const conn = panel.querySelector('.conn') as HTMLElement | null;
    if (!conn) return;
    conn.className = 'conn';
    if (connectionState === 'connected') conn.classList.add('connected');
    else if (connectionState === 'connecting' || connectionState === 'reconnecting') conn.classList.add('reconnecting');
    else conn.classList.add('error');
    conn.style.display = state.settings.showConnection ? '' : 'none';
  }

  function swapBroadcastContent(next: HTMLElement): void {
    const stage = broadcastStage;
    if (!stage) return;
    const previous = stage.children[stage.children.length - 1] as HTMLElement | undefined;
    next.classList.add('is-entering');
    stage.append(next);
    previous?.classList.add('is-leaving');
    globalThis.setTimeout(() => {
      previous?.remove();
      next.classList.remove('is-entering');
    }, BROADCAST_TRANSITION_DURATION);
  }

  function renderGiftBroadcast(entry: LogEntry): void {
    const block = attrEls.get(entry.attributeName);
    const attribute = state.attributes.find((item) => item.name === entry.attributeName);
    if (!block || !attribute) return;
    block.classList.remove('flash');
    void block.offsetWidth;
    block.classList.add('flash');
    globalThis.setTimeout(() => block.classList.remove('flash'), 700);
    swapBroadcastContent(createGiftBroadcast(entry, attribute));
  }

  function enqueueGiftBroadcast(entry: LogEntry): void {
    if (!attrEls.has(entry.attributeName)) return;
    broadcastQueue.enqueue(entry);
  }

  async function refreshState(): Promise<void> {
    if (stateRefreshActive) return;
    stateRefreshActive = true;
    try {
      const structure = displayStructureSignature(state);
      state = await refreshStateFromServer();
      if (displayStructureSignature(state) !== structure) renderAttrs();
      else updateAll();
      const newEntries = entriesSince(state.log, lastLogKey);
      lastLogKey = state.log[0] ? logKey(state.log[0]) : lastLogKey;
      for (const entry of newEntries.reverse()) {
        if (entry.source !== 'timer') enqueueGiftBroadcast(entry);
      }
    } finally {
      stateRefreshActive = false;
    }
  }

  async function refreshRuntime(): Promise<void> {
    try {
      connectionState = (await getRuntimeStatus()).state;
    } catch {
      connectionState = 'error';
    }
    updateConnection();
  }

  renderAttrs();
  void refreshRuntime();
  const pollTimer = globalThis.setInterval(() => {
    void refreshState();
    void refreshRuntime();
  }, 750);
  if (typeof globalThis.addEventListener === 'function') {
    globalThis.addEventListener('beforeunload', () => {
      globalThis.clearInterval(pollTimer);
      broadcastQueue.dispose();
    }, { once: true });
  }
}

function createAttributeMeter(attribute: Attribute, variant: ReturnType<typeof resolveAttributeDisplayVariant>): HTMLElement {
  const meter = el('div', { class: `attr-meter attr-meter-${variant}` });
  const track = el('div', { class: 'attr-meter-track' }, [
    el('span', { class: 'attr-meter-fill' }),
    ...(variant === 'tug' ? [el('span', { class: 'attr-meter-center' }), el('span', { class: 'attr-meter-marker' })] : []),
  ]);
  meter.append(
    track,
    el('div', { class: 'attr-meter-labels' }, [
      el('span', { class: 'attr-meter-min', text: attribute.display?.leftLabel ?? '' }),
      el('span', { class: 'attr-meter-max', text: attribute.display?.rightLabel ?? '' }),
    ]),
  );
  return meter;
}

function formatMeterBoundary(value: number, attribute: Attribute): string {
  if (attribute.format === 'suffix' && attribute.suffix) return `${value.toLocaleString('zh-CN')} ${attribute.suffix}`;
  return value.toLocaleString('zh-CN');
}

function formatCountdown(totalSeconds: number): string {
  const minutes = Math.floor(totalSeconds / 60);
  const seconds = totalSeconds % 60;
  return `${String(minutes).padStart(2, '0')}:${String(seconds).padStart(2, '0')}`;
}

function timeoutActionText(action: 'lock' | 'settle' | 'reset'): string {
  if (action === 'settle') return '自动结算';
  if (action === 'reset') return '自动重置';
  return '自动锁定';
}

function createDefaultBroadcast(attribute: Attribute): HTMLElement {
  const message = attribute.broadcastMessage?.trim() || DEFAULT_BROADCAST_MESSAGE;
  return el('div', { class: 'broadcast-content broadcast-default' }, [
    el('div', { class: 'broadcast-marquee' }, [
      el('span', { class: 'broadcast-marquee-text', text: message, title: message }),
    ]),
  ]);
}

function createGiftBroadcast(entry: LogEntry, attribute: Attribute): HTMLElement {
  const avatar = el('img', {
    class: 'broadcast-avatar',
    alt: entry.uname ? `${entry.uname}的头像` : '用户头像',
    referrerPolicy: 'no-referrer',
  }) as HTMLImageElement;
  avatar.src = entry.avatar || transparentPixel();
  const nickname = entry.uname?.trim() || '匿名观众';
  const delta = formatDelta(entry.delta, attribute);
  return el('div', { class: 'broadcast-content broadcast-gift' }, [
    avatar,
    el('strong', { class: 'broadcast-user-name', text: nickname, title: nickname }),
    el('span', { class: 'broadcast-action', text: '投喂了' }),
    el('span', { class: 'broadcast-gift-name', text: `${entry.giftName || '礼物'}x${Math.max(1, entry.num || 1)}` }),
    el('span', { class: 'broadcast-attribute-name', text: entry.attributeName, title: entry.attributeName }),
    el('strong', { class: 'broadcast-delta', text: delta, title: delta }),
  ]);
}

function entriesSince(log: LogEntry[], previousKey: string): LogEntry[] {
  const entries: LogEntry[] = [];
  for (const entry of log) {
    if (previousKey && logKey(entry) === previousKey) break;
    entries.push(entry);
  }
  return entries;
}

function displayStructureSignature(state: AppState): string {
  return JSON.stringify({
    attributes: state.attributes.map(({ value: _value, ...attribute }) => attribute),
    displayScenes: state.displayScenes,
    activities: state.activities,
    rules: state.rules,
    giftCatalog: state.giftCatalog,
    settings: state.settings,
  });
}

function logKey(entry: LogEntry): string {
  return entry.eventId || `${entry.time}:${entry.ruleId}:${entry.attributeName}:${entry.valueAfter}`;
}

function transparentPixel(): string {
  return 'data:image/gif;base64,R0lGODlhAQABAIAAAAAAAP///yH5BAEAAAAALAAAAAABAAEAAAIBRAA7';
}
