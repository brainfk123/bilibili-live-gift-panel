import type { AppState, Settings, TutorialLesson } from '../../types';

export type WizardStep = 'room' | 'attributes' | 'rules' | 'obs';
export type AttributeWorkspaceSection = 'overview' | 'rules' | 'timers' | 'output';

export interface WizardProgress {
  room: boolean;
  attributes: boolean;
  rules: boolean;
  obs: boolean;
}

export interface WizardChecklistStep {
  label: string;
  target: WizardStep;
  done: boolean;
}

export interface TutorialEditorProgress {
  open: boolean;
  templateOpen?: boolean;
  isNew?: boolean;
  basicsConfigured?: boolean;
  giftCount?: number;
  giftPreviewed?: boolean;
  timerCount?: number;
  timerPreviewed?: boolean;
  outputPreviewed?: boolean;
}

export interface TutorialLessonDefinition {
  id: TutorialLesson;
  label: string;
  summary: string;
  section?: AttributeWorkspaceSection;
}

export interface TutorialLessonState extends TutorialLessonDefinition {
  done: boolean;
  active: boolean;
}

export const TUTORIAL_VERSION = 3;

export const TUTORIAL_LESSONS: TutorialLessonDefinition[] = [
  { id: 'room', label: '连接直播间', summary: '填写房间号并确认托盘后台已经连接。' },
  { id: 'attribute', label: '打开创建中心', summary: '从配置页进入模板与空白创建入口。' },
  { id: 'template', label: '从空白开始练习', summary: '模板适合快速开播；空白创建可以逐项学会完整功能。' },
  { id: 'basics', label: '认识属性与显示', summary: '名称和值参与计算，显示格式只改变 OBS 中的样子。', section: 'overview' },
  { id: 'gift', label: '选择礼物', summary: '一个属性可以绑定任意数量的礼物。', section: 'rules' },
  { id: 'rule', label: '配置并模拟规则', summary: '收到单个礼物时，后台把规则结果保存为属性新值。', section: 'rules' },
  { id: 'preset', label: '保存规则预设', summary: '预设保存计算方法，之后可复用到其他礼物。', section: 'rules' },
  { id: 'timer', label: '定时减少与条件', summary: '定时器不依赖礼物，并可在条件满足时执行。', section: 'timers' },
  { id: 'appearance', label: '确认 OBS 外观', summary: '预览皮肤、默认播报和专属链接，不会改变后台计算。', section: 'output' },
  { id: 'save', label: '保存可用配置', summary: '后台校验全部规则后再写入本机配置。', section: 'output' },
  { id: 'enable', label: '展开卡片并启用', summary: '悬停属性卡片查看详情，再决定哪些规则和定时器生效。' },
  { id: 'output', label: '复制链接并后台运行', summary: 'OBS 只负责显示；关闭配置页后托盘后台继续计算。' },
];

export function getWizardProgress(state: Pick<AppState, 'roomId' | 'attributes' | 'rules'>): WizardProgress {
  const room = state.roomId.trim().length > 0;
  const attributes = state.attributes.length > 0;
  const rules = state.rules.length > 0;
  return { room, attributes, rules, obs: room && attributes && rules };
}

export function getWizardChecklist(progress: WizardProgress): WizardChecklistStep[] {
  return [
    { label: '填写房间号', target: 'room', done: progress.room },
    { label: '创建属性（如加班时间）', target: 'attributes', done: progress.attributes },
    { label: '配置礼物规则', target: 'rules', done: progress.rules },
    { label: '在 OBS 中显示', target: 'obs', done: progress.obs },
  ];
}

export function getNextWizardStep(progress: WizardProgress): WizardStep | null {
  if (!progress.room) return 'room';
  if (!progress.attributes) return 'attributes';
  if (!progress.rules) return 'rules';
  return null;
}

function recordedCompletion(state: Pick<AppState, 'settings'>, lesson: TutorialLesson): boolean {
  return state.settings.tutorialCompletedLessons?.includes(lesson) ?? false;
}

export function isTutorialLessonComplete(
  state: Pick<AppState, 'attributes' | 'rules' | 'timerRules' | 'formulaPresets' | 'settings'>,
  connected: boolean,
  editor: TutorialEditorProgress,
  lesson: TutorialLesson,
): boolean {
  if (recordedCompletion(state, lesson)) return true;
  if (lesson === 'room') return connected;
  if (lesson === 'attribute') return state.attributes.length > 0 || editor.templateOpen === true || editor.open;
  if (lesson === 'template') return state.attributes.length > 0 || editor.open;
  if (lesson === 'basics') {
    if (editor.open && editor.isNew) return editor.basicsConfigured === true;
    return state.attributes.length > 0;
  }
  if (lesson === 'gift') return state.rules.length > 0 || (editor.giftCount ?? 0) > 0;
  if (lesson === 'rule') return state.rules.length > 0 || editor.giftPreviewed === true;
  if (lesson === 'timer') {
    if (editor.open && editor.isNew) return editor.timerPreviewed === true;
    return state.timerRules.length > 0 || editor.timerPreviewed === true;
  }
  if (lesson === 'preset') return state.formulaPresets.length > 0;
  if (lesson === 'appearance') return state.attributes.length > 0 || editor.outputPreviewed === true;
  if (lesson === 'save') return state.attributes.length > 0 && state.rules.length > 0;
  if (lesson === 'enable') return state.rules.some((rule) => rule.enabled !== false);
  return false;
}

export function getTutorialLesson(
  state: Pick<AppState, 'attributes' | 'rules' | 'timerRules' | 'formulaPresets' | 'settings'>,
  connected: boolean,
  editor: TutorialEditorProgress = { open: false },
): TutorialLesson | null {
  for (const lesson of TUTORIAL_LESSONS) {
    if (!isTutorialLessonComplete(state, connected, editor, lesson.id)) return lesson.id;
  }
  return null;
}

export function getTutorialLessonStates(
  state: Pick<AppState, 'attributes' | 'rules' | 'timerRules' | 'formulaPresets' | 'settings'>,
  connected: boolean,
  editor: TutorialEditorProgress = { open: false },
  forcedLesson?: TutorialLesson | null,
): TutorialLessonState[] {
  const active = forcedLesson ?? getTutorialLesson(state, connected, editor);
  return TUTORIAL_LESSONS.map((lesson) => ({
    ...lesson,
    done: isTutorialLessonComplete(state, connected, editor, lesson.id),
    active: lesson.id === active,
  }));
}

export function sectionForTutorialLesson(lesson: TutorialLesson | null): AttributeWorkspaceSection {
  return TUTORIAL_LESSONS.find((item) => item.id === lesson)?.section ?? 'overview';
}

export function markTutorialLessonComplete(settings: Settings, lesson: TutorialLesson): void {
  settings.tutorialVersion = TUTORIAL_VERSION;
  settings.tutorialCompletedLessons = Array.from(new Set([
    ...(settings.tutorialCompletedLessons ?? []),
    lesson,
  ]));
}

export function resetTutorialProgress(settings: Settings): void {
  settings.tutorialVersion = TUTORIAL_VERSION;
  settings.tutorialCompletedLessons = [];
  settings.showTutorial = true;
}

export function getTutorialStep(
  state: Pick<AppState, 'attributes' | 'rules'>,
  connected: boolean,
  editorOpen: boolean,
): WizardStep {
  if (!connected) return 'room';
  if (state.attributes.length === 0) return editorOpen ? 'rules' : 'attributes';
  if (state.rules.length === 0) return 'rules';
  return 'obs';
}

export function getRoomNumberHint(rawUrl: string): { path: string; query: string } | null {
  try {
    const url = new URL(rawUrl);
    const match = url.pathname.match(/\/([^/]+)\/?$/);
    if (!match || !/^\d+$/.test(match[1])) return null;
    return { path: match[1], query: url.search };
  } catch {
    return null;
  }
}
