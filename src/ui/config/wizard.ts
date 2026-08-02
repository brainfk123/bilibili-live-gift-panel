import type { AppState } from '../../types';

export type WizardStep = 'room' | 'attributes' | 'rules' | 'obs';

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
