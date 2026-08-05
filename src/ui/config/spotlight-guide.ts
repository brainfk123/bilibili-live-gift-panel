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
  title: string;
  body: string;
  action: string;
}

const GUIDE_COPY: Record<TutorialLesson, GuideCopy> = {
  room: {
    targets: ['.guide-room-input'],
    title: '填写你的直播间房间号',
    body: '填写直播间网址末尾的数字，再点击“连接”。连接成功后，托盘后台才会接收礼物。',
    action: '填写房间号',
  },
  attribute: {
    targets: ['.guide-attribute-add'],
    title: '打开属性创建中心',
    body: '这里既有可以直接开播的玩法模板，也能从空白逐项配置。先打开创建中心。',
    action: '打开创建中心',
  },
  template: {
    targets: ['.guide-blank-template'],
    title: '从空白创建，完整练习一次',
    body: '玩法模板适合快速开始；这次选择“从空白创建”，进入属性工作台学习每一项功能。',
    action: '从空白创建',
  },
  basics: {
    targets: ['.guide-overtime-template'],
    title: '套用加班机模板',
    body: '工作台里的训练模板只填写“加班时间”、起始值和计时器格式，后面的规则仍由你亲手配置。',
    action: '使用模板',
  },
  gift: {
    targets: ['.guide-gift-selection-ready', '.guide-gift-search', '.guide-add-gift'],
    title: '选择一种观众礼物',
    body: '一个属性可以绑定任意数量的礼物。选好后点击“确认选择”，再配置它的规则。',
    action: '添加礼物',
  },
  rule: {
    targets: ['.guide-rule-simulator', '.guide-add-gift'],
    title: '决定礼物如何改变时间',
    body: '选择增加、按价格增加、设为固定值或随机增加，再模拟收到 1 个礼物。模拟只预览，不改真实数值。',
    action: '模拟一次',
  },
  preset: {
    targets: ['.guide-save-preset'],
    title: '把这条规则保存为预设',
    body: '“保存预设”位于高级规则中，只保存计算方法。以后配置其他礼物时可以直接套用。',
    action: '保存预设',
  },
  timer: {
    targets: ['.guide-timer-simulator', '.guide-add-timer'],
    title: '让时间自动减少',
    body: '添加每秒 -1 的定时器，并设置“加班时间大于 0”时才运行。它由托盘后台独立执行。',
    action: '配置定时器',
  },
  appearance: {
    targets: ['.guide-output-confirm'],
    title: '检查 OBS 中会显示什么',
    body: '这里设置默认播报、数值状态和当前属性的皮肤。它们只改变画面，不会改变后台数值。',
    action: '确认输出预览',
  },
  save: {
    targets: ['.guide-attribute-save'],
    title: '保存并交给后台校验',
    body: '保存时后台会校验礼物规则和定时器；只有全部有效才会写入本机配置。',
    action: '创建属性',
  },
  enable: {
    targets: ['.guide-rule-toggle'],
    title: '在悬浮详情中启用规则',
    body: '属性卡片平时只显示关键信息；悬停或键盘聚焦后会展开详情。打开开关，后台才会执行这条礼物规则。',
    action: '启用规则',
  },
  output: {
    targets: ['.guide-obs-copy'],
    title: '把面板放进 OBS',
    body: '从展开的属性卡片复制专属链接，添加为 OBS“浏览器”来源。之后可以关闭配置页，托盘后台会继续收礼、计算和更新 OBS。',
    action: '复制 OBS 链接',
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

function positionGuide(target: HTMLElement | null, focus: HTMLElement, bubble: HTMLElement): void {
  if (!target || typeof target.getBoundingClientRect !== 'function') {
    focus.hidden = true;
    bubble.style.left = '50%';
    bubble.style.top = '50%';
    bubble.style.transform = 'translate(-50%, -50%)';
    return;
  }

  const rect = target.getBoundingClientRect();
  const targetVisible = rect.bottom > 0
    && rect.top < window.innerHeight
    && rect.right > 0
    && rect.left < window.innerWidth;
  focus.hidden = !targetVisible;
  bubble.hidden = !targetVisible;
  if (!targetVisible) return;

  const pad = 8;
  focus.style.left = `${Math.max(8, rect.left - pad)}px`;
  focus.style.top = `${Math.max(8, rect.top - pad)}px`;
  focus.style.width = `${Math.max(44, rect.width + pad * 2)}px`;
  focus.style.height = `${Math.max(44, rect.height + pad * 2)}px`;

  const width = Math.min(380, window.innerWidth - 32);
  bubble.style.width = `${width}px`;
  const left = clamp(rect.left + rect.width / 2 - width / 2, 16, window.innerWidth - width - 16);
  const above = rect.top - bubble.offsetHeight - 18;
  const placeBelow = above < 16;
  const below = Math.min(window.innerHeight - bubble.offsetHeight - 16, rect.bottom + 18);
  bubble.classList.toggle('is-below', placeBelow);
  bubble.style.left = `${left}px`;
  bubble.style.top = `${Math.max(16, placeBelow ? below : above)}px`;
  bubble.style.transform = 'none';
}

export function renderSpotlightGuide(context: SpotlightGuideContext): SpotlightGuideElement {
  const copy = GUIDE_COPY[context.lesson];
  const lessonIndex = TUTORIAL_LESSONS.findIndex((lesson) => lesson.id === context.lesson);
  const frame = el('div', { class: 'tour-prototype tour-variant-spotlight' }) as unknown as SpotlightGuideElement;
  frame.classList.toggle('is-modal-step', context.editorOpen);
  const target = copy.targets
    .map((selector) => context.host.querySelector(selector) as HTMLElement | null)
    .find(isGuideTargetAvailable) ?? null;
  const focus = el('div', { class: 'tour-focus', ariaHidden: 'true' } as any);
  const bubble = el('section', { class: 'tour-bubble', role: 'dialog', ariaLabel: '训练提示' } as any);
  bubble.append(
    el('div', { class: 'tour-bubble-eyebrow', text: `加班机训练 · ${lessonIndex + 1}/${TUTORIAL_LESSONS.length}` }),
    el('h2', { class: 'tour-bubble-title', text: copy.title }),
    el('p', { class: 'tour-bubble-body', text: copy.body }),
  );

  const footer = el('div', { class: 'tour-bubble-footer' });
  const exit = el('button', { class: 'tour-bubble-skip', type: 'button', text: '退出训练' }) as HTMLButtonElement;
  const skip = el('button', { class: 'tour-bubble-skip', type: 'button', text: '跳过本关' }) as HTMLButtonElement;
  const actionLabel = target?.className.split(/\s+/).includes('guide-gift-selection-ready') ? '确认选择' : copy.action;
  const action = el('button', { class: 'btn tour-bubble-action', type: 'button', text: actionLabel }) as HTMLButtonElement;
  let positionQueued = false;
  const position = (): void => {
    positionQueued = false;
    positionGuide(target, focus, bubble);
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
  action.onclick = () => {
    frame.dispose();
    if (target?.tagName === 'INPUT') (target as HTMLInputElement).focus();
    else if (typeof target?.click === 'function') target.click();
    else (target as any)?.onclick?.();
    if (context.lesson === 'output') context.onDismiss();
  };
  footer.append(exit, skip, action);
  bubble.append(footer);
  frame.append(focus, bubble);
  context.host.append(frame);
  schedulePosition();
  return frame;
}
