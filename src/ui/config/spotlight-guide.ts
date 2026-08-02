import { el } from '../common';
import type { WizardStep } from './wizard';

interface SpotlightGuideContext {
  host: HTMLElement;
  step: WizardStep;
  editorOpen: boolean;
  onDismiss: () => void;
}

export interface SpotlightGuideElement extends HTMLElement {
  dispose: () => void;
}

interface GuideCopy {
  target: string;
  eyebrow: string;
  title: string;
  body: string;
  action: string;
}

function guideCopy(step: WizardStep, editorOpen: boolean): GuideCopy {
  if (step === 'room') {
    return {
      target: '.guide-room-input',
      eyebrow: '先完成直播连接',
      title: '填写你的直播间房间号',
      body: '复制 live.bilibili.com/ 后面的数字，填好后点击“测试连接”。',
      action: '开始填写',
    };
  }
  if (step === 'attributes') {
    return {
      target: '.guide-attribute-add',
      eyebrow: '第 2 步',
      title: '添加第一个属性',
      body: '点击“添加属性”打开配置面板，例如创建“加班时间”或“挑战次数”。',
      action: '添加属性',
    };
  }
  if (step === 'rules' && editorOpen) {
    return {
      target: '.guide-gift-search',
      eyebrow: '第 3 步',
      title: '添加礼物并配置公式',
      body: '选择一个或多个礼物，填写各自的公式名称和公式，然后点击“创建属性”保存。',
      action: '开始配置',
    };
  }
  if (step === 'rules') {
    return {
      target: '.guide-attribute-edit',
      eyebrow: '第 3 步',
      title: '补充礼物和公式',
      body: '打开属性，至少选择一个礼物并配置公式；保存后教程会进入 OBS 步骤。',
      action: '继续配置',
    };
  }
  return {
    target: '.guide-obs-copy',
    eyebrow: '第 4 步',
    title: '复制 OBS 链接',
    body: '把专属链接添加到 OBS“浏览器”来源后就可以关闭这个网页。托盘后台运行时会继续接收礼物并计算属性；OBS 链接只负责显示，临时关闭也不会中断计算。需要修改时单击托盘图标。',
    action: '复制地址',
  };
}

function clamp(value: number, min: number, max: number): number {
  return Math.min(Math.max(value, min), max);
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

  const width = Math.min(360, window.innerWidth - 32);
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
  const copy = guideCopy(context.step, context.editorOpen);
  const frame = el('div', { class: 'tour-prototype tour-variant-spotlight' }) as unknown as SpotlightGuideElement;
  frame.classList.toggle('is-modal-step', context.editorOpen);
  const target = context.host.querySelector(copy.target) as HTMLElement | null;
  const focus = el('div', { class: 'tour-focus', ariaHidden: 'true' } as any);
  const bubble = el('section', { class: 'tour-bubble', role: 'dialog', ariaLabel: '配置提示' } as any);
  bubble.append(
    el('div', { class: 'tour-bubble-eyebrow', text: copy.eyebrow }),
    el('h2', { class: 'tour-bubble-title', text: copy.title }),
    el('p', { class: 'tour-bubble-body', text: copy.body }),
  );

  const footer = el('div', { class: 'tour-bubble-footer' });
  const skip = el('button', { class: 'tour-bubble-skip', type: 'button', text: '暂时跳过' }) as HTMLButtonElement;
  const action = el('button', { class: 'btn tour-bubble-action', type: 'button', text: copy.action }) as HTMLButtonElement;
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
  const skipTutorial = (): void => {
    frame.dispose();
    context.onDismiss();
  };
  skip.onclick = skipTutorial;
  action.onclick = () => {
    frame.dispose();
    if (target?.tagName === 'INPUT') (target as HTMLInputElement).focus();
    else if (typeof target?.click === 'function') target.click();
    else (target as any)?.onclick?.();
    if (context.step === 'obs') context.onDismiss();
  };
  footer.append(skip, action);
  bubble.append(footer);
  frame.append(focus, bubble);
  context.host.append(frame);
  schedulePosition();
  return frame;
}
