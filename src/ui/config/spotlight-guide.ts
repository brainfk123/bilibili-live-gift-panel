import { el } from '../common';
import type { TutorialLesson } from '../../types';
import { TUTORIAL_LESSONS } from './wizard';

interface SpotlightGuideContext {
  host: HTMLElement;
  lesson: TutorialLesson;
  editorOpen: boolean;
  onDismiss: () => void;
  onSkipLesson: () => void;
}

export interface SpotlightGuideElement extends HTMLElement {
  dispose: () => void;
}

interface GuideCopy {
  targets: string[];
  panelTargets: string[];
  title: string;
  body: string;
  observe: string;
  task: string;
}

const GUIDE_COPY: Record<TutorialLesson, GuideCopy> = {
  room: {
    targets: ['.guide-room-input'],
    panelTargets: ['.room-card'],
    title: '填写你的直播间房间号',
    body: '房间号决定后台监听哪一个直播间。连接成功后，托盘后台才会开始接收礼物。',
    observe: '找到房间号输入框和旁边的“连接”按钮。',
    task: '亲手填写房间号，再点击页面里的“连接”。',
  },
  attribute: {
    targets: ['.guide-attribute-add'],
    panelTargets: ['.attributes-section'],
    title: '打开属性创建中心',
    body: '属性是后台持续保存和计算的数值，礼物规则与定时器都会改变它。',
    observe: '看看“属性与礼物规则”工作区里已有的属性卡片。',
    task: '亲手点击高亮的“+ 添加属性”。',
  },
  template: {
    targets: ['.guide-blank-template'],
    panelTargets: ['.template-wizard'],
    title: '从空白创建，完整练习一次',
    body: '玩法模板适合快速开始；空白创建会让你逐项认识完整配置。',
    observe: '比较玩法模板和“从空白创建”卡片的区别。',
    task: '亲手选择高亮的“从空白创建”。',
  },
  basics: {
    targets: ['.guide-overtime-template'],
    panelTargets: ['.attribute-overview-panel'],
    title: '套用加班机模板',
    body: '属性名称和值参与后台计算，显示格式只改变 OBS 中的呈现。',
    observe: '先看属性名称、当前值和显示格式三个区域。',
    task: '亲手点击“使用加班机模板”，观察这些字段如何变化。',
  },
  gift: {
    targets: ['.guide-gift-selection-ready', '.guide-gift-search', '.guide-add-gift'],
    panelTargets: ['.gift-picker-drawer', '.attribute-rules-panel'],
    title: '选择一种观众礼物',
    body: '礼物是触发入口；一个属性可以绑定多个礼物，每个礼物都能使用不同规则。',
    observe: '留意礼物图片、名称、价格和上架状态。',
    task: '亲手打开礼物列表，选择一个礼物并确认。',
  },
  rule: {
    targets: ['.guide-rule-preview-confirm', '.guide-rule-simulator', '.guide-add-gift'],
    panelTargets: ['.selected-gift-rule', '.attribute-rules-panel'],
    title: '决定礼物如何改变时间',
    body: '规则名称方便主播辨认；等号右侧的计算结果会成为属性的新值。',
    observe: '先看规则名称、规则方式、计算表达式和下方预览。',
    task: '亲手选择一种规则方式，再点击页面里的“模拟收到 1 个”。',
  },
  preset: {
    targets: ['.guide-preset-confirm', '.guide-save-preset'],
    panelTargets: ['.formula-preset-name-dialog', '.rule-advanced-settings', '.selected-gift-rule'],
    title: '把这条规则保存为预设',
    body: '预设只保存计算方法，不会保存属性当前值，之后可快速套用到其他礼物。',
    observe: '看看高级规则里的表达式和已有预设胶囊。',
    task: '亲手点击“保存预设”并给它命名。',
  },
  timer: {
    targets: ['.guide-timer-preview-confirm', '.guide-timer-simulator', '.guide-add-timer'],
    panelTargets: ['.timer-rule-editor', '.timer-binding-panel'],
    title: '让时间自动减少',
    body: '定时器由托盘后台独立运行，不依赖配置页或 OBS 页面保持打开。',
    observe: '先看触发间隔、运行条件和触发后的属性值。',
    task: '亲手添加每秒 -1 的定时器，并模拟执行一次。',
  },
  appearance: {
    targets: ['.guide-output-confirm'],
    panelTargets: ['.attribute-output-panel'],
    title: '检查 OBS 中会显示什么',
    body: '这里设置默认播报、数值状态和当前属性的皮肤，只改变画面，不改变后台数值。',
    observe: '观察预览里的属性名、数值、规则卡片和播报区域。',
    task: '确认画面结构后，亲手点击“确认输出预览”。',
  },
  save: {
    targets: ['.guide-attribute-save'],
    panelTargets: ['.attribute-workbench'],
    title: '保存并交给后台校验',
    body: '保存时后台会校验礼物规则和定时器；只有全部有效才会写入本机配置。',
    observe: '回顾工作区标签，确认礼物规则、定时器和输出都已配置。',
    task: '亲手点击页面底部的“创建属性”。',
  },
  enable: {
    targets: ['.guide-rule-toggle'],
    panelTargets: ['.guide-attribute-detail', '.guide-attribute-card'],
    title: '在悬浮详情中启用规则',
    body: '属性卡片平时只显示关键信息；悬停或键盘聚焦后会展开详情。打开开关，后台才会执行这条礼物规则。',
    observe: '看看展开卡片里的礼物规则和启用开关。',
    task: '亲手打开高亮规则的开关。',
  },
  output: {
    targets: ['.guide-obs-copy'],
    panelTargets: ['.guide-attribute-detail', '.guide-attribute-card'],
    title: '把面板放进 OBS',
    body: '从展开的属性卡片复制专属链接，添加为 OBS“浏览器”来源。之后可以关闭配置页，托盘后台会继续收礼、计算和更新 OBS。',
    observe: '每个属性都有独立链接，一个链接只显示一个属性面板。',
    task: '亲手点击页面里的“复制 OBS 链接”。',
  },
};

function clamp(value: number, min: number, max: number): number {
  return Math.min(Math.max(value, min), max);
}

function isGuideTargetAvailable(candidate: HTMLElement | null): candidate is HTMLElement {
  if (!candidate || candidate.hidden) return false;
  let ancestor = ((candidate as any).parentElement ?? (candidate as any).parent) as HTMLElement | null;
  while (ancestor) {
    if (ancestor.hidden) return false;
    ancestor = ((ancestor as any).parentElement ?? (ancestor as any).parent) as HTMLElement | null;
  }
  return true;
}

function positionGuide(
  target: HTMLElement | null,
  panel: HTMLElement | null,
  focus: HTMLElement,
  targetOutline: HTMLElement,
  bubble: HTMLElement,
): void {
  if (!target || typeof target.getBoundingClientRect !== 'function') {
    focus.hidden = true;
    targetOutline.hidden = true;
    bubble.style.left = '50%';
    bubble.style.top = '50%';
    bubble.style.transform = 'translate(-50%, -50%)';
    return;
  }

  const rect = target.getBoundingClientRect();
  const panelRect = panel && typeof panel.getBoundingClientRect === 'function'
    ? panel.getBoundingClientRect()
    : rect;
  const targetVisible = rect.bottom > 0
    && rect.top < window.innerHeight
    && rect.right > 0
    && rect.left < window.innerWidth;
  const panelVisible = panelRect.bottom > 0
    && panelRect.top < window.innerHeight
    && panelRect.right > 0
    && panelRect.left < window.innerWidth;
  focus.hidden = !panelVisible;
  targetOutline.hidden = !targetVisible;
  bubble.hidden = !targetVisible;
  if (!targetVisible) return;

  if (panelVisible) {
    const panelPad = 6;
    const panelLeft = Math.max(8, panelRect.left - panelPad);
    const panelTop = Math.max(8, panelRect.top - panelPad);
    const panelRight = Math.min(window.innerWidth - 8, panelRect.right + panelPad);
    const panelBottom = Math.min(window.innerHeight - 8, panelRect.bottom + panelPad);
    focus.style.left = `${panelLeft}px`;
    focus.style.top = `${panelTop}px`;
    focus.style.width = `${Math.max(24, panelRight - panelLeft)}px`;
    focus.style.height = `${Math.max(24, panelBottom - panelTop)}px`;
  }

  const targetPad = 4;
  targetOutline.style.left = `${Math.max(8, rect.left - targetPad)}px`;
  targetOutline.style.top = `${Math.max(8, rect.top - targetPad)}px`;
  targetOutline.style.width = `${Math.max(36, rect.width + targetPad * 2)}px`;
  targetOutline.style.height = `${Math.max(36, rect.height + targetPad * 2)}px`;

  const width = Math.min(380, window.innerWidth - 32);
  bubble.style.width = `${width}px`;
  const height = bubble.offsetHeight;
  const gap = 18;
  const above = panelRect.top - height - gap;
  const below = panelRect.bottom + gap;
  const right = panelRect.right + gap;
  const left = panelRect.left - width - gap;
  const canPlaceAbove = above >= 16;
  const canPlaceBelow = below + height <= window.innerHeight - 16;
  const canPlaceRight = right + width <= window.innerWidth - 16;
  const canPlaceLeft = left >= 16;
  bubble.classList.remove('is-below', 'is-right', 'is-left');
  if (canPlaceAbove || canPlaceBelow) {
    const bubbleLeft = clamp(rect.left + rect.width / 2 - width / 2, 16, window.innerWidth - width - 16);
    if (!canPlaceAbove) bubble.classList.add('is-below');
    bubble.style.left = `${bubbleLeft}px`;
    bubble.style.top = `${canPlaceAbove ? above : below}px`;
  } else if (canPlaceRight || canPlaceLeft) {
    const placeRight = canPlaceRight;
    bubble.classList.add(placeRight ? 'is-right' : 'is-left');
    bubble.style.left = `${placeRight ? right : left}px`;
    bubble.style.top = `${clamp(
      panelRect.top + panelRect.height / 2 - height / 2,
      16,
      window.innerHeight - height - 16,
    )}px`;
  } else {
    const bubbleLeft = clamp(rect.left + rect.width / 2 - width / 2, 16, window.innerWidth - width - 16);
    const targetAbove = rect.top - height - gap;
    const placeBelow = targetAbove < 16;
    if (placeBelow) bubble.classList.add('is-below');
    bubble.style.left = `${bubbleLeft}px`;
    bubble.style.top = `${Math.max(16, placeBelow ? Math.min(window.innerHeight - height - 16, rect.bottom + gap) : targetAbove)}px`;
  }
  bubble.style.transform = 'none';
}

export function renderSpotlightGuide(context: SpotlightGuideContext): SpotlightGuideElement {
  const copy = GUIDE_COPY[context.lesson];
  const lessonIndex = TUTORIAL_LESSONS.findIndex((lesson) => lesson.id === context.lesson);
  const frame = el('div', { class: 'tour-prototype tour-variant-spotlight' }) as unknown as SpotlightGuideElement;
  frame.classList.toggle('is-modal-step', context.editorOpen);
  frame.classList.toggle(
    'is-card-detail-step',
    context.lesson === 'enable' || context.lesson === 'output',
  );
  const target = copy.targets
    .map((selector) => context.host.querySelector(selector) as HTMLElement | null)
    .find(isGuideTargetAvailable) ?? null;
  const panel = copy.panelTargets
    .map((selector) => context.host.querySelector(selector) as HTMLElement | null)
    .find(isGuideTargetAvailable) ?? target;
  const focus = el('div', { class: 'tour-focus', ariaHidden: 'true' } as any);
  const targetOutline = el('div', { class: 'tour-target-outline', ariaHidden: 'true' } as any);
  const bubble = el('section', { class: 'tour-bubble', role: 'dialog', ariaLabel: '训练提示' } as any);
  const targetClasses = String(target?.getAttribute?.('class') ?? (target as any)?.className ?? '')
    .split(/\s+/);
  const task = targetClasses.includes('guide-rule-preview-confirm')
    ? '核对预览里的原值和新值，再点击“确认这次变化”。'
    : targetClasses.includes('guide-timer-preview-confirm')
      ? '核对本次是否执行以及原值和新值，再点击“确认这次变化”。'
      : targetClasses.includes('guide-preset-confirm')
        ? '输入预设名称，再点击弹窗里的“保存”。'
      : copy.task;
  bubble.append(
    el('div', { class: 'tour-bubble-eyebrow', text: `加班机训练 · ${lessonIndex + 1}/${TUTORIAL_LESSONS.length}` }),
    el('h2', { class: 'tour-bubble-title', text: copy.title }),
    el('p', { class: 'tour-bubble-body', text: copy.body }),
    el('div', { class: 'tour-bubble-observe' }, [
      el('strong', { text: '先观察' }),
      el('span', { text: copy.observe }),
    ]),
    el('div', { class: 'tour-bubble-task', role: 'status', ariaLive: 'polite' } as any, [
      el('span', { class: 'tour-bubble-task-dot', ariaHidden: 'true' } as any),
      el('span', { text: task }),
    ]),
  );

  const footer = el('div', { class: 'tour-bubble-footer' });
  const exit = el('button', { class: 'tour-bubble-skip', type: 'button', text: '退出训练' }) as HTMLButtonElement;
  const skip = el('button', { class: 'tour-bubble-skip', type: 'button', text: '跳过本关' }) as HTMLButtonElement;
  let positionQueued = false;
  const position = (): void => {
    positionQueued = false;
    positionGuide(target, panel, focus, targetOutline, bubble);
  };
  const schedulePosition = (): void => {
    if (positionQueued) return;
    positionQueued = true;
    const raf = globalThis.requestAnimationFrame;
    if (typeof raf === 'function') raf(position);
    else position();
  };
  const canListen = typeof globalThis.addEventListener === 'function';
  if (canListen) {
    globalThis.addEventListener('scroll', schedulePosition, true);
    globalThis.addEventListener('resize', schedulePosition);
  }
  frame.dispose = () => {
    if (canListen) {
      globalThis.removeEventListener('scroll', schedulePosition, true);
      globalThis.removeEventListener('resize', schedulePosition);
    }
    frame.remove();
  };
  exit.onclick = () => {
    frame.dispose();
    context.onDismiss();
  };
  skip.onclick = () => {
    frame.dispose();
    context.onSkipLesson();
  };
  footer.append(exit, skip);
  bubble.append(footer);
  frame.append(focus, targetOutline, bubble);
  context.host.append(frame);
  schedulePosition();
  return frame;
}
