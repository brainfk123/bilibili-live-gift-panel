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
    body: '填写直播间网址末尾的数字，再点击“测试连接”。连接成功后，托盘后台才会接收礼物。',
    action: '填写房间号',
  },
  attribute: {
    targets: ['.guide-attribute-add'],
    title: '添加第一个属性',
    body: '属性是一份会被礼物规则和定时器修改的数据。先打开属性工作台。',
    action: '添加属性',
  },
  basics: {
    targets: ['.guide-overtime-template'],
    title: '套用加班机模板',
    body: '模板会填入“加班时间”、初始值 0 和计时器格式。名称和值参与计算，显示格式只影响 OBS。',
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
  timer: {
    targets: ['.guide-timer-simulator', '.guide-add-timer'],
    title: '让时间自动减少',
    body: '定时器由托盘后台独立运行；条件可限制“只有加班时间大于 0 时”才执行。先添加，再模拟一次。',
    action: '配置定时器',
  },
  preset: {
    targets: ['.guide-save-preset'],
    title: '保存可复用的规则',
    body: '预设保存计算方法。以后配置其他礼物时，可以一键套用，不必重新输入。',
    action: '保存预设',
  },
  save: {
    targets: ['.guide-attribute-save'],
    title: '保存并交给后台校验',
    body: '保存时后台会校验礼物规则和定时器；只有全部有效才会写入本机配置。',
    action: '创建属性',
  },
  enable: {
    targets: ['.guide-rule-toggle'],
    title: '启用真正的礼物响应',
    body: '卡片开关控制后台是否执行这条规则。关闭的规则仍会保留，但不会改变属性，也不会出现在 OBS 中。',
    action: '启用规则',
  },
  output: {
    targets: ['.guide-obs-copy'],
    title: '把面板放进 OBS',
    body: '复制链接并添加为 OBS“浏览器”来源。之后可关闭配置页；托盘后台会继续收礼、计算和更新 OBS。',
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
