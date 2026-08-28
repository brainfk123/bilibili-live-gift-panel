import { getDisplayTheme } from '../../display-themes';
import { formatNumber } from '../../format';
import type { OBSOutputSelector } from './selector';

export interface OBSViewerRow {
  uid: number;
  name: string;
  avatar: string;
  gifts: number;
  giftCoin: number;
}

export interface OBSDisplaySnapshot {
  accountId: number;
  liveSessionId: number;
  revision: number;
  runtime: {
    attributeValues: Record<string, number> | null;
    giftTargetReceived: Array<{ panelId: string; giftId: number; received: number }>;
    activities: unknown[];
    ruleLimits: { localDate: string; appliedCounts: Record<string, number> };
  };
  effects?: unknown[];
  viewers?: OBSViewerRow[];
}

export interface OBSRenderOptions {
  theme?: unknown;
  viewportWidth?: number;
  viewportHeight?: number;
  output?: OBSOutputSelector;
}

export interface OBSLayout {
  columns: number;
  cardWidth: number;
  gap: number;
  gutter: number;
  contentWidth: number;
}

export function computeOBSLayout(viewportWidth: number, _viewportHeight: number, itemCount: number): OBSLayout {
  const width = Math.max(1, Math.floor(Number.isFinite(viewportWidth) ? viewportWidth : 1));
  const gutter = width >= 1600 ? 32 : 20;
  const gap = width >= 1600 ? 20 : 14;
  const available = Math.max(1, width - gutter * 2);
  const maximumColumns = Math.max(1, Math.floor((available + gap) / (220 + gap)));
  const columns = Math.max(1, Math.min(Math.max(1, itemCount), maximumColumns));
  const cardWidth = Math.max(1, Math.floor((available - (columns - 1) * gap) / columns));
  const contentWidth = columns * cardWidth + (columns - 1) * gap + gutter * 2;
  return { columns, cardWidth, gap, gutter, contentWidth };
}

export function renderOBSSnapshot(root: HTMLElement, snapshot: OBSDisplaySnapshot, options: OBSRenderOptions = {}): void {
  const document = root.ownerDocument;
  if (!document) throw new Error('OBS view requires a document.');
  const theme = getDisplayTheme(options.theme);
  const allowedAttributes = options.output?.kind === 'attribute' || options.output?.kind === 'scene' ? new Set(options.output.attributeIds) : undefined;
  const attributes = Object.entries(snapshot.runtime.attributeValues ?? {}).filter(([id]) => !allowedAttributes || allowedAttributes.has(id));
  const targets = options.output?.kind === 'gift-target' ? snapshot.runtime.giftTargetReceived.filter((item) => item.panelId === options.output?.id) : [];
  const itemCount = options.output?.kind === 'gift-target' ? targets.length : attributes.length;
  const layout = computeOBSLayout(options.viewportWidth ?? globalThis.innerWidth, options.viewportHeight ?? globalThis.innerHeight, itemCount);
  root.setAttribute('data-theme', theme.id);
  root.style.setProperty('--obs-theme-accent', theme.accent);
  root.style.setProperty('--obs-theme-surface', theme.surface);
  root.style.setProperty('--obs-columns', String(layout.columns));
  root.style.setProperty('--obs-gap', `${layout.gap}px`);
  root.style.setProperty('--obs-gutter', `${layout.gutter}px`);

  const stage = document.createElement('main');
  stage.className = 'hosted-obs-stage';
  if (attributes.length === 0) {
    const empty = document.createElement('p');
    empty.className = 'hosted-obs-empty';
    empty.textContent = '等待互动数据';
    stage.append(empty);
  }
  for (const [name, value] of attributes) {
    const card = document.createElement('section');
    card.className = 'hosted-obs-card';
    const label = document.createElement('span');
    label.className = 'hosted-obs-label';
    label.textContent = name;
    const number = document.createElement('strong');
    number.className = 'hosted-obs-value';
    number.textContent = formatNumber(value, Number.isInteger(value) ? 0 : 1);
    card.append(label, number);
    stage.append(card);
  }
  for (const target of targets) {
    const card = document.createElement('section'); card.className = 'hosted-obs-card';
    const label = document.createElement('span'); label.className = 'hosted-obs-label'; label.textContent = `礼物 ${target.giftId}`;
    const number = document.createElement('strong'); number.className = 'hosted-obs-value'; number.textContent = formatNumber(target.received, 0);
    card.append(label, number); stage.append(card);
  }
  if (!options.output && (snapshot.viewers?.length ?? 0) > 0) {
    const viewers = document.createElement('aside');
    viewers.className = 'hosted-obs-viewers';
    for (const viewer of snapshot.viewers ?? []) {
      const row = document.createElement('span');
      row.className = 'hosted-obs-viewer';
      row.textContent = `${viewer.name || '匿名观众'} · ${formatNumber(viewer.gifts, 0)}`;
      viewers.append(row);
    }
    stage.append(viewers);
  }
  root.replaceChildren(stage);
}

export function parseOBSDisplaySnapshot(value: unknown): OBSDisplaySnapshot | undefined {
  if (!isRecord(value) || !exactKeys(value, ['accountId', 'liveSessionId', 'revision', 'runtime'], ['effects', 'viewers'])) return undefined;
  if (!positiveInteger(value.accountId) || !nonnegativeInteger(value.liveSessionId) || !nonnegativeInteger(value.revision) || !runtimeState(value.runtime)) return undefined;
  if (value.effects !== undefined && (!Array.isArray(value.effects) || !value.effects.every(effectRow))) return undefined;
  if (value.viewers !== undefined && (!Array.isArray(value.viewers) || !value.viewers.every(viewerRow))) return undefined;
  return value as unknown as OBSDisplaySnapshot;
}

function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value);
}

function exactKeys(value: Record<string, unknown>, required: string[], optional: string[] = []): boolean {
  const keys = Object.keys(value);
  return required.every((key) => Object.hasOwn(value, key)) && keys.every((key) => required.includes(key) || optional.includes(key));
}

function positiveInteger(value: unknown): value is number { return Number.isSafeInteger(value) && Number(value) > 0; }
function nonnegativeInteger(value: unknown): value is number { return Number.isSafeInteger(value) && Number(value) >= 0; }

function numberRecord(value: unknown): value is Record<string, number> {
  return isRecord(value) && Object.values(value).every((entry) => typeof entry === 'number' && Number.isFinite(entry));
}

function runtimeState(value: unknown): boolean {
  if (!isRecord(value) || !exactKeys(value, ['attributeValues', 'giftTargetReceived', 'activities', 'ruleLimits'])) return false;
  if (value.attributeValues !== null && !numberRecord(value.attributeValues)) return false;
  if (!Array.isArray(value.giftTargetReceived) || !value.giftTargetReceived.every(giftTargetRow)) return false;
  if (!Array.isArray(value.activities) || !value.activities.every(activityRow)) return false;
  return ruleLimitState(value.ruleLimits);
}

function giftTargetRow(value: unknown): boolean {
  return isRecord(value) && exactKeys(value, ['panelId', 'giftId', 'received'])
    && typeof value.panelId === 'string' && nonnegativeInteger(value.giftId) && nonnegativeInteger(value.received);
}

function activityRow(value: unknown): boolean {
  if (!isRecord(value) || !exactKeys(value, ['id', 'status', 'milestones'], ['startedAtMillis', 'lockedAtMillis', 'settledAtMillis', 'result', 'giftTimeout'])) return false;
  if (typeof value.id !== 'string' || typeof value.status !== 'string' || !Array.isArray(value.milestones) || !value.milestones.every(milestoneRow)) return false;
  for (const key of ['startedAtMillis', 'lockedAtMillis', 'settledAtMillis'] as const) {
    if (value[key] !== undefined && !Number.isSafeInteger(value[key])) return false;
  }
  if (value.result !== undefined && !activityResult(value.result)) return false;
  return value.giftTimeout === undefined || giftTimeout(value.giftTimeout);
}

function milestoneRow(value: unknown): boolean {
  return isRecord(value) && exactKeys(value, ['id'], ['triggeredAtMillis', 'triggerValue'])
    && typeof value.id === 'string'
    && (value.triggeredAtMillis === undefined || Number.isSafeInteger(value.triggeredAtMillis))
    && (value.triggerValue === undefined || finiteNumber(value.triggerValue));
}

function activityResult(value: unknown): boolean {
  return isRecord(value) && exactKeys(value, ['values'], ['winnerAttributeId']) && numberRecord(value.values)
    && (value.winnerAttributeId === undefined || typeof value.winnerAttributeId === 'string');
}

function giftTimeout(value: unknown): boolean {
  return isRecord(value) && (exactKeys(value, []) || (exactKeys(value, ['lastGiftAtMillis', 'deadlineAtMillis'])
    && Number.isSafeInteger(value.lastGiftAtMillis) && Number.isSafeInteger(value.deadlineAtMillis)));
}

function ruleLimitState(value: unknown): boolean {
  return isRecord(value) && exactKeys(value, ['localDate', 'appliedCounts']) && typeof value.localDate === 'string'
    && isRecord(value.appliedCounts) && Object.values(value.appliedCounts).every(nonnegativeInteger);
}

function effectRow(value: unknown): boolean {
  if (!isRecord(value) || !exactKeys(value, ['delta', 'valueAfter'], ['ruleId', 'attributeName', 'triggerName', 'target', 'activity'])) return false;
  if (!finiteNumber(value.delta) || !finiteNumber(value.valueAfter)) return false;
  for (const key of ['ruleId', 'attributeName', 'triggerName'] as const) {
    if (value[key] !== undefined && typeof value[key] !== 'string') return false;
  }
  return (value.target === undefined || targetNotice(value.target)) && (value.activity === undefined || activityNotice(value.activity));
}

function targetNotice(value: unknown): boolean {
  return isRecord(value) && exactKeys(value, ['panelId', 'giftId', 'received', 'target']) && typeof value.panelId === 'string'
    && nonnegativeInteger(value.giftId) && nonnegativeInteger(value.received) && nonnegativeInteger(value.target);
}

function activityNotice(value: unknown): boolean {
  return isRecord(value) && exactKeys(value, ['activityId', 'action', 'status'], ['milestoneId'])
    && typeof value.activityId === 'string' && typeof value.action === 'string' && typeof value.status === 'string'
    && (value.milestoneId === undefined || typeof value.milestoneId === 'string');
}

function finiteNumber(value: unknown): value is number { return typeof value === 'number' && Number.isFinite(value); }

function viewerRow(value: unknown): value is OBSViewerRow {
  return isRecord(value) && exactKeys(value, ['uid', 'name', 'avatar', 'gifts', 'giftCoin']) && positiveInteger(value.uid) && typeof value.name === 'string' && typeof value.avatar === 'string' && nonnegativeInteger(value.gifts) && typeof value.giftCoin === 'number' && Number.isFinite(value.giftCoin) && value.giftCoin >= 0;
}
