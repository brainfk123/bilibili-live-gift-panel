import {
  HostedAPIError,
  type Challenge,
  type MigrationConflictChoice,
  type MigrationJob,
  type MigrationPreview,
  type MigrationSelection,
  type MigrationUnit,
} from './api';

export interface MigrationAPI {
  previewMigration?(rawJSON: string): Promise<MigrationPreview>;
  selectMigration?(id: number, selection: MigrationSelection): Promise<MigrationPreview>;
  getMigration?(id: number): Promise<MigrationJob>;
  applyMigration?(id: number, challengeID: string, selection: MigrationSelection): Promise<MigrationJob>;
  cancelMigration?(id: number): Promise<MigrationJob>;
  rollbackMigration?(id: number, challengeID: string): Promise<MigrationJob>;
  beginLogin?(): Promise<Challenge>;
  pollLogin?(id: string): Promise<{ status: 'pending' | 'scanned' | 'verified' | 'registration_required' | 'expired'; expiresAt?: string }>;
  cancelLogin?(id: string): Promise<void>;
}

export const migrationFileLimit = 2 * 1024 * 1024;
export interface MigrationFile { name: string; size: number; text(): Promise<string> }
export interface OBSReplacementItem { outputId: string; name: string; url: string; replaced: boolean }
export type MigrationApplyProgress = 'idle' | 'preview_ready' | 'waiting_for_live_boundary' | 'applied' | 'cancelled' | 'rolled_back' | 'expired';
export interface MigrationViewState {
  preview?: MigrationPreview;
  job?: MigrationJob;
  proof?: Pick<Challenge, 'qrImage' | 'expiresAt'>;
  proofStatus?: 'pending' | 'scanned';
  replacementConfirmed: boolean;
  rawFileActive: boolean;
  operationInFlight: boolean;
  canApply: boolean;
  canCancel: boolean;
  canRefresh: boolean;
  canRollback: boolean;
  duplicatePackage: boolean;
  applyProgress: MigrationApplyProgress;
  obsChecklist?: OBSReplacementItem[];
  previewHoursRemaining?: number;
  rollbackDaysRemaining?: number;
  error?: string;
}

const clone = <T>(value: T): T => structuredClone(value);
const selectionCopy = (selection: MigrationSelection): MigrationSelection => ({
  unitIds: [...selection.unitIds],
  conflictChoices: { ...selection.conflictChoices },
  includeGeneralSettings: selection.includeGeneralSettings,
  includeRoomSuggestion: selection.includeRoomSuggestion,
});

export function createMigrationFlow(api: MigrationAPI, render: (state: MigrationViewState) => void, clock: { now(): Date } = { now: () => new Date() }) {
  let preview: MigrationPreview | undefined;
  let job: MigrationJob | undefined;
  let proof: Challenge | undefined;
  let proofStatus: 'pending' | 'scanned' | undefined;
  let replacementConfirmed = false;
  let rawFileActive = false;
  let operationInFlight = false;
  let duplicatePackage = false;
  let disposed = false;
  let error: string | undefined;
  let generation = 0;
  let proofOperation: Promise<void> | undefined;
  let obsChecklist: OBSReplacementItem[] = [];

  const previewHours = (): number | undefined => preview ? Math.max(0, Math.ceil((Date.parse(preview.expiresAt) - clock.now().getTime()) / 3_600_000)) : undefined;
  const rollbackDays = (): number | undefined => job?.rollbackExpiresAt ? Math.max(0, Math.ceil((Date.parse(job.rollbackExpiresAt) - clock.now().getTime()) / 86_400_000)) : undefined;
  const previewAlive = (): boolean => (previewHours() ?? 0) > 0;
  const canApply = (): boolean => Boolean(preview && job?.status === 'previewed' && preview.canConfirm && replacementConfirmed && previewAlive());
  const canCancel = (): boolean => Boolean(job && (job.status === 'previewed' || job.status === 'pending') && previewAlive());
  const canRefresh = (): boolean => Boolean(job?.status === 'pending' && previewAlive());
  const canRollback = (): boolean => job?.status === 'applied' && (rollbackDays() ?? 0) > 0;
  const progress = (): MigrationApplyProgress => {
    if (job?.status === 'pending') return 'waiting_for_live_boundary';
    if (job?.status === 'applied') return 'applied';
    if (job?.status === 'cancelled') return 'cancelled';
    if (job?.status === 'rolled_back') return 'rolled_back';
    if (job?.status === 'expired' || preview && !previewAlive()) return 'expired';
    if (preview) return 'preview_ready';
    return 'idle';
  };
  const publish = (): void => {
    if (disposed) return;
    render({
      ...(preview ? { preview: clone(preview) } : {}),
      ...(job ? { job: clone(job) } : {}),
      ...(proof ? { proof: { qrImage: proof.qrImage, expiresAt: proof.expiresAt } } : {}),
      ...(proofStatus ? { proofStatus } : {}),
      replacementConfirmed, rawFileActive, operationInFlight,
      canApply: canApply(), canCancel: canCancel(), canRefresh: canRefresh(), canRollback: canRollback(),
      duplicatePackage, applyProgress: progress(),
      ...(obsChecklist.length ? { obsChecklist: clone(obsChecklist) } : {}),
      ...(previewHours() === undefined ? {} : { previewHoursRemaining: previewHours() }),
      ...(rollbackDays() === undefined ? {} : { rollbackDaysRemaining: rollbackDays() }),
      ...(error ? { error } : {}),
    });
  };
  const fail = (cause: unknown): void => {
    error = cause instanceof TypeError ? '网络连接失败，请检查网络后重试' : '操作失败，请重试';
    publish();
  };
  const bestEffortCancel = (challenge?: Challenge): void => { if (challenge) void api.cancelLogin?.(challenge.challengeId).catch(() => undefined); };
  const bestEffortJobCancel = (id: number): void => { void api.cancelMigration?.(id).catch(() => undefined); };
  const operationConflict = (): HostedAPIError => new HostedAPIError('operation_conflict', 409);
  const discardProof = (): void => { const active = proof; proof = undefined; proofStatus = undefined; bestEffortCancel(active); };
  const ensureLive = (started: number): void => { if (disposed || started !== generation) throw new HostedAPIError('operation_failed', 0); };
  const applyJob = (value: MigrationJob): void => {
    job = clone(value);
    if (value.obsLinks) obsChecklist = value.obsLinks.map((link) => ({ ...link, replaced: obsChecklist.find((item) => item.outputId === link.outputId)?.replaced ?? false }));
  };
  const nextProof = async (started: number): Promise<Challenge> => {
    if (proof) return proof;
    if (!api.beginLogin) throw new HostedAPIError('invalid_request', 400);
    const created = await api.beginLogin();
    if (disposed || started !== generation) { bestEffortCancel(created); throw new HostedAPIError('operation_failed', 0); }
    proof = created; publish(); return created;
  };
  const checkedProof = async (started: number): Promise<string> => {
    const active = await nextProof(started);
    if (!api.pollLogin) throw new HostedAPIError('invalid_request', 400);
    const result = await api.pollLogin(active.challengeId); ensureLive(started);
    if (result.status === 'pending' || result.status === 'scanned') {
      proofStatus = result.status;
      error = result.status === 'pending' ? '等待 B 站扫码确认，请稍后重试' : '已扫码，请在手机确认';
      publish(); throw new HostedAPIError('verification_pending', 202);
    }
    if (result.status !== 'verified') { discardProof(); throw new HostedAPIError('proof_rejected', 401); }
    proofStatus = undefined; return active.challengeId;
  };
  const runProofOperation = (action: (challengeID: string, started: number) => Promise<void>): Promise<void> => {
    if (proofOperation || operationInFlight) return Promise.reject(operationConflict());
    const started = ++generation; operationInFlight = true; error = undefined; publish();
    const running = (async () => {
      try { const challengeID = await checkedProof(started); await action(challengeID, started); }
      catch (cause) {
        if (!(cause instanceof HostedAPIError && (cause.code === 'verification_pending' || cause.code === 'temporarily_unavailable'))) discardProof();
        if (!disposed && started === generation && !(cause instanceof HostedAPIError && cause.code === 'verification_pending')) fail(cause);
        throw cause;
      }
    })();
    proofOperation = running;
    void running.finally(() => { if (proofOperation === running) proofOperation = undefined; if (!disposed && started === generation) { operationInFlight = false; publish(); } }).catch(() => undefined);
    return running;
  };
  const submitSelection = async (next: MigrationSelection): Promise<void> => {
    if (disposed || operationInFlight || proofOperation) throw operationConflict();
    if (!preview || !job || job.status !== 'previewed' || !api.selectMigration || !previewAlive()) throw new HostedAPIError('invalid_request', 400);
    const started = ++generation; operationInFlight = true; error = undefined; publish();
    try { const result = await api.selectMigration(preview.id, selectionCopy(next)); ensureLive(started); preview = clone(result); replacementConfirmed = false; }
    catch (cause) { if (!disposed && started === generation) fail(cause); throw cause; }
    finally { if (!disposed && started === generation) { operationInFlight = false; publish(); } }
  };

  return Object.freeze({
    async preview(file: MigrationFile): Promise<void> {
      if (proofOperation || operationInFlight) throw operationConflict();
      if (disposed || !api.previewMigration || !file.name.toLowerCase().endsWith('.json') || !Number.isSafeInteger(file.size) || file.size < 0 || file.size > migrationFileLimit) throw new HostedAPIError('invalid_request', 400);
      const started = ++generation; rawFileActive = true; operationInFlight = true; error = undefined; publish();
      let raw = '';
      try {
        raw = await file.text(); ensureLive(started);
        const result = await api.previewMigration(raw);
        if (disposed || started !== generation) { if (!result.reused) bestEffortJobCancel(result.id); throw new HostedAPIError('operation_failed', 0); }
        const authoritativeJob = result.reused ? await api.getMigration?.(result.id) : undefined; ensureLive(started);
        if (result.reused && !authoritativeJob) throw new HostedAPIError('invalid_request', 400);
        preview = clone(result); applyJob(authoritativeJob ?? { id: result.id, status: 'previewed', expiresAt: result.expiresAt });
        duplicatePackage = result.reused; replacementConfirmed = false; obsChecklist = []; error = undefined;
      } catch (cause) { if (!disposed && started === generation) fail(cause); throw cause; }
      finally { raw = ''; if (!disposed && started === generation) { rawFileActive = false; operationInFlight = false; publish(); } }
    },
    acceptPreview(value: MigrationPreview): void {
      if (disposed || proofOperation || operationInFlight) return;
      generation += 1; preview = clone(value); applyJob({ id: value.id, status: 'previewed', expiresAt: value.expiresAt }); duplicatePackage = value.reused; replacementConfirmed = false; obsChecklist = []; error = undefined; publish();
    },
    setUnitSelected(unitID: string, selected: boolean): Promise<void> {
      const unit = preview?.units.find((candidate) => candidate.id === unitID);
      if (!preview || !unit || unit.compatibility.status !== 'complete') return Promise.reject(new HostedAPIError('invalid_request', 400));
      const group = preview.groups.find((candidate) => candidate.unitIds.includes(unitID));
      if (group) return this.setGroupSelected(group.id, selected);
      const unitIDs = new Set(preview.selection.unitIds); if (selected) unitIDs.add(unitID); else unitIDs.delete(unitID);
      return submitSelection({ ...selectionCopy(preview.selection), unitIds: preview.units.map((candidate) => candidate.id).filter((id) => unitIDs.has(id)), conflictChoices: {} });
    },
    setGroupSelected(groupID: string, selected: boolean): Promise<void> {
      const group = preview?.groups.find((candidate) => candidate.id === groupID);
      if (!preview || !group || group.unitIds.some((id) => preview?.units.find((unit) => unit.id === id)?.compatibility.status !== 'complete')) return Promise.reject(new HostedAPIError('invalid_request', 400));
      const unitIDs = new Set(preview.selection.unitIds); for (const id of group.unitIds) { if (selected) unitIDs.add(id); else unitIDs.delete(id); }
      return submitSelection({ ...selectionCopy(preview.selection), unitIds: preview.units.map((unit) => unit.id).filter((id) => unitIDs.has(id)), conflictChoices: {} });
    },
    setGeneralSettingsIncluded(value: boolean): Promise<void> { if (!preview) return Promise.reject(new HostedAPIError('invalid_request', 400)); return submitSelection({ ...selectionCopy(preview.selection), includeGeneralSettings: value }); },
    setRoomSuggestionIncluded(value: boolean): Promise<void> { if (!preview) return Promise.reject(new HostedAPIError('invalid_request', 400)); return submitSelection({ ...selectionCopy(preview.selection), includeRoomSuggestion: value }); },
    setConflictChoice(conflictID: string, choice: MigrationConflictChoice): Promise<void> {
      if (!preview?.conflicts.some((conflict) => conflict.id === conflictID)) return Promise.reject(new HostedAPIError('invalid_request', 400));
      return submitSelection({ ...selectionCopy(preview.selection), conflictChoices: { ...preview.selection.conflictChoices, [conflictID]: choice } });
    },
    confirmReplacement(value: boolean): void { if (!disposed && !proofOperation && !operationInFlight) { replacementConfirmed = value; publish(); } },
    apply(): Promise<void> {
      if (proofOperation || operationInFlight) return Promise.reject(operationConflict());
      if (!preview || !canApply() || !api.applyMigration) return Promise.reject(new HostedAPIError('invalid_request', 400));
      const id = preview.id; const confirmedSelection = selectionCopy(preview.selection);
      return runProofOperation(async (challengeID, started) => { const result = await api.applyMigration!(id, challengeID, confirmedSelection); ensureLive(started); applyJob(result); discardProof(); error = undefined; publish(); });
    },
    rollback(): Promise<void> {
      if (proofOperation || operationInFlight) return Promise.reject(operationConflict());
      if (!job || !canRollback() || !api.rollbackMigration) return Promise.reject(new HostedAPIError('invalid_request', 400));
      const id = job.id;
      return runProofOperation(async (challengeID, started) => { const result = await api.rollbackMigration!(id, challengeID); ensureLive(started); applyJob(result); discardProof(); error = undefined; publish(); });
    },
    async refresh(id: number): Promise<void> {
      if (proofOperation || operationInFlight) throw operationConflict();
      if (disposed || !canRefresh() || !job || id !== job.id || !api.getMigration) throw new HostedAPIError('invalid_request', 400);
      const started = ++generation; operationInFlight = true; error = undefined; publish();
      try { const result = await api.getMigration(id); ensureLive(started); applyJob(result); error = undefined; publish(); }
      catch (cause) { if (!disposed && started === generation) fail(cause); throw cause; }
      finally { if (!disposed && started === generation) { operationInFlight = false; publish(); } }
    },
    async cancel(): Promise<void> {
      if (proofOperation || operationInFlight) throw operationConflict();
      if (disposed || !canCancel() || !job || !api.cancelMigration) throw new HostedAPIError('invalid_request', 400);
      const started = ++generation; const id = job.id; operationInFlight = true; error = undefined; publish();
      try { const result = await api.cancelMigration(id); ensureLive(started); applyJob(result); error = undefined; publish(); }
      catch (cause) { if (!disposed && started === generation) fail(cause); throw cause; }
      finally { if (!disposed && started === generation) { operationInFlight = false; publish(); } }
    },
    confirmOBSReplacement(outputID: string, value: boolean): void { const item = obsChecklist.find((candidate) => candidate.outputId === outputID); if (item) { item.replaced = value; publish(); } },
    refreshTime(): void { publish(); },
    reportFailure(cause: unknown): void { fail(cause); },
    async dispose(): Promise<void> {
      generation += 1; disposed = true; const active = proof; proof = undefined; proofStatus = undefined; preview = undefined; job = undefined; error = undefined; rawFileActive = false; operationInFlight = false; obsChecklist = []; bestEffortCancel(active);
    },
  });
}

const compatibilityLabels: Record<string, string> = {
  crop_presets_unsupported: '剪裁预设不受支持',
  display_scenes_unsupported: '部分展示场景不受支持',
  formula_presets_unsupported: '公式预设不受支持',
  rules_unsupported: '礼物规则不受支持',
  simple_play_template_unsupported: '简单玩法版本不受支持',
  timer_rules_unsupported: '定时规则不受支持',
  unit_kind_unsupported: '玩法类型不受支持',
};
const reasonText = (code: string): string => `${compatibilityLabels[code] ?? '此内容不受支持'}（${code}）`;

export function mountMigrationView(root: HTMLElement, api: MigrationAPI, callbacks: { onConfiguration(): void }) {
  const document = root.ownerDocument; let disposed = false; let selected: File | undefined;
  let state: MigrationViewState = { replacementConfirmed: false, rawFileActive: false, operationInFlight: false, canApply: false, canCancel: false, canRefresh: false, canRollback: false, duplicatePackage: false, applyProgress: 'idle' };
  const panel = document.createElement('main'); panel.className = 'hosted-migration-center';
  const header = document.createElement('header'); header.className = 'hosted-migration-header';
  const heading = document.createElement('div'); const eyebrow = document.createElement('span'); eyebrow.className = 'hosted-migration-eyebrow'; eyebrow.textContent = '设置 · 数据迁移'; const title = document.createElement('h1'); title.textContent = '从本地 EXE 迁移'; heading.append(eyebrow, title);
  const back = document.createElement('button'); back.type = 'button'; back.textContent = '返回设置'; back.addEventListener('click', () => { if (!back.disabled) callbacks.onConfiguration(); }); header.append(heading, back);
  const status = document.createElement('div'); status.className = 'hosted-migration-feedback'; status.setAttribute('role', 'alert'); status.setAttribute('aria-live', 'assertive');
  const upload = document.createElement('section'); upload.className = 'hosted-migration-card hosted-migration-upload';
  const uploadCopy = document.createElement('div'); const uploadTitle = document.createElement('h2'); uploadTitle.textContent = '1. 上传迁移包'; const uploadHint = document.createElement('p'); uploadHint.textContent = '仅接受 EXE 导出的 JSON，最大 2 MiB。原始文件只在本次请求中读取。'; uploadCopy.append(uploadTitle, uploadHint);
  const file = document.createElement('input'); file.type = 'file'; file.accept = '.json,application/json'; file.setAttribute('aria-label', '选择迁移 JSON 文件'); file.addEventListener('change', () => { selected = file.files?.item(0) ?? undefined; });
  const previewButton = document.createElement('button'); previewButton.type = 'button'; previewButton.addEventListener('click', () => { if (!selected) { status.dataset.kind = 'error'; status.textContent = '请选择 EXE 导出的 .json 文件'; return; } void flow.preview(selected).catch(() => undefined).finally(() => { selected = undefined; file.value = ''; }); });
  const uploadActions = document.createElement('div'); uploadActions.className = 'hosted-migration-upload-actions'; uploadActions.append(file, previewButton); upload.append(uploadCopy, uploadActions);
  const previewHost = document.createElement('div'); previewHost.className = 'hosted-migration-preview';
  panel.append(header, status, upload, previewHost); root.replaceChildren(panel);

  const appendText = (parent: HTMLElement, tag: 'p' | 'span' | 'strong' | 'h2' | 'h3', text: string, className?: string): HTMLElement => { const element = document.createElement(tag); element.textContent = text; if (className) element.className = className; parent.append(element); return element; };
  const button = (text: string, action: () => void, disabled = false): HTMLButtonElement => { const element = document.createElement('button'); element.type = 'button'; element.textContent = text; element.disabled = disabled; element.addEventListener('click', action); return element; };
  const unitRow = (unit: MigrationUnit): HTMLElement => {
    const row = document.createElement('label'); row.className = 'hosted-migration-unit'; row.dataset.compatibility = unit.compatibility.status;
    const box = document.createElement('input'); box.type = 'checkbox'; box.checked = unit.selected; box.disabled = state.operationInFlight || unit.compatibility.status !== 'complete'; box.addEventListener('change', () => { void flow.setUnitSelected(unit.id, box.checked).catch(() => undefined); });
    const copy = document.createElement('span'); copy.className = 'hosted-migration-unit-copy'; appendText(copy, 'strong', unit.name || unit.id); appendText(copy, 'span', unit.compatibility.status === 'complete' ? '完全兼容' : unit.compatibility.status === 'partial' ? '部分兼容，默认不导入' : '不兼容，无法导入', 'hosted-migration-badge');
    for (const reason of unit.compatibility.reasonCodes) appendText(copy, 'span', reasonText(reason), 'hosted-migration-reason');
    row.append(box, copy); return row;
  };
  const render = (next: MigrationViewState): void => {
    if (disposed) return; state = next; const busy = next.operationInFlight;
    status.dataset.kind = next.error ? 'error' : busy ? 'loading' : next.duplicatePackage ? 'info' : 'neutral';
    status.replaceChildren();
    if (busy) { const spinner = document.createElement('span'); spinner.className = 'hosted-migration-spinner'; spinner.setAttribute('aria-hidden', 'true'); status.append(spinner); }
    appendText(status, 'span', next.error ?? (busy ? '正在与服务器同步迁移预览…' : next.duplicatePackage ? '已识别重复迁移包，继续显示服务器上的上一次预览。' : '迁移预览不会改动当前在线配置。'));
    previewButton.replaceChildren(); if (next.rawFileActive) { const spinner = document.createElement('span'); spinner.className = 'hosted-migration-spinner'; spinner.setAttribute('aria-hidden', 'true'); previewButton.append(spinner, document.createTextNode('正在预览')); } else previewButton.textContent = next.preview ? '重新上传并预览' : '生成服务器预览';
    previewButton.disabled = busy; previewButton.setAttribute('aria-busy', String(next.rawFileActive)); file.disabled = busy; back.disabled = busy;
    previewHost.replaceChildren(); if (!next.preview) return;
    const current = next.preview;
    const summary = document.createElement('section'); summary.className = 'hosted-migration-card hosted-migration-summary'; appendText(summary, 'h2', '2. 检查服务器预览');
    const metrics = document.createElement('div'); metrics.className = 'hosted-migration-metrics';
    for (const [label, value] of [['玩法属性', current.counts.attributes], ['礼物规则', current.counts.rules], ['活动', current.counts.activities], ['目标项', current.counts.giftTargetItems]] as const) { const metric = document.createElement('div'); appendText(metric, 'strong', String(value)); appendText(metric, 'span', label); metrics.append(metric); }
    summary.append(metrics); appendText(summary, 'p', `预览剩余约 ${next.previewHoursRemaining ?? 0} 小时；到期后需重新上传。`, 'hosted-migration-expiry');
    if (current.warnings?.length || current.ignored?.length) { const notices = document.createElement('ul'); notices.className = 'hosted-migration-notices'; for (const warning of current.warnings ?? []) { const item = document.createElement('li'); item.textContent = `提示：${warning}`; notices.append(item); } for (const ignored of current.ignored ?? []) { const item = document.createElement('li'); item.textContent = `已忽略：${ignored}`; notices.append(item); } summary.append(notices); }
    previewHost.append(summary);

    const units = document.createElement('section'); units.className = 'hosted-migration-card'; appendText(units, 'h2', '3. 选择玩法单元'); appendText(units, 'p', '关联组会整体选择；通用设置和房间建议保持独立。');
    const grouped = new Set<string>();
    for (const group of current.groups) {
      const card = document.createElement('fieldset'); card.className = 'hosted-migration-group'; const legend = document.createElement('legend'); const groupBox = document.createElement('input'); groupBox.type = 'checkbox'; const members = current.units.filter((unit) => group.unitIds.includes(unit.id)); groupBox.checked = members.length > 0 && members.every((unit) => unit.selected); groupBox.disabled = busy || members.some((unit) => unit.compatibility.status !== 'complete'); groupBox.addEventListener('change', () => { void flow.setGroupSelected(group.id, groupBox.checked).catch(() => undefined); }); legend.append(groupBox, document.createTextNode(' 关联玩法组')); card.append(legend);
      for (const reason of group.reasons) appendText(card, 'p', `关联原因：${reason.kind} · ${reason.referenceId}`, 'hosted-migration-group-reason');
      for (const member of members) { grouped.add(member.id); card.append(unitRow(member)); }
      units.append(card);
    }
    for (const unit of current.units) if (!grouped.has(unit.id)) units.append(unitRow(unit));
    const independent = document.createElement('div'); independent.className = 'hosted-migration-independent';
    const settings = document.createElement('label'); const settingsBox = document.createElement('input'); settingsBox.type = 'checkbox'; settingsBox.checked = current.selection.includeGeneralSettings; settingsBox.disabled = busy; settingsBox.addEventListener('change', () => { void flow.setGeneralSettingsIncluded(settingsBox.checked).catch(() => undefined); }); settings.append(settingsBox, document.createTextNode(` 导入通用设置（配置模式：${current.generalSettings.configurationMode || '未指定'}）`)); independent.append(settings);
    if (current.roomSuggestion) { const room = document.createElement('label'); const roomBox = document.createElement('input'); roomBox.type = 'checkbox'; roomBox.checked = current.selection.includeRoomSuggestion; roomBox.disabled = busy; roomBox.addEventListener('change', () => { void flow.setRoomSuggestionIncluded(roomBox.checked).catch(() => undefined); }); room.append(roomBox, document.createTextNode(` 采用房间建议 ${current.roomSuggestion}（不会自动运行）`)); independent.append(room); }
    units.append(independent); previewHost.append(units);

    const confirm = document.createElement('section'); confirm.className = 'hosted-migration-card'; appendText(confirm, 'h2', '4. 解决冲突并确认');
    if (!current.conflicts.length) appendText(confirm, 'p', '相同稳定 ID 的玩法会作为同一玩法的新版本替换；未选择的 Hosted 玩法保持不变。');
    for (const conflict of current.conflicts) { const label = document.createElement('label'); label.className = 'hosted-migration-conflict'; appendText(label, 'span', `同名冲突：${conflict.importedUnitIds.join('、')}`); const select = document.createElement('select'); select.disabled = busy; select.setAttribute('aria-label', `解决冲突 ${conflict.id}`); for (const [value, text] of [['', '请选择处理方式'], ['replace', '替换 Hosted 同名玩法'], ['keep_both', `保留两份${Object.values(conflict.suggestedNames)[0] ? `：${Object.values(conflict.suggestedNames)[0]}` : ''}`], ['skip', '跳过 EXE 玩法']] as const) { const option = document.createElement('option'); option.value = value; option.textContent = text; select.append(option); } select.value = current.selection.conflictChoices[conflict.id] ?? ''; select.addEventListener('change', () => { if (select.value) void flow.setConflictChoice(conflict.id, select.value as MigrationConflictChoice).catch(() => undefined); }); label.append(select); confirm.append(label); }
    const replacement = document.createElement('label'); replacement.className = 'hosted-migration-confirmation'; const replacementBox = document.createElement('input'); replacementBox.type = 'checkbox'; replacementBox.checked = next.replacementConfirmed; replacementBox.disabled = busy || !current.canConfirm; replacementBox.addEventListener('change', () => flow.confirmReplacement(replacementBox.checked)); replacement.append(replacementBox, document.createTextNode(' 我已核对选择和冲突处理，确认应用服务器重新组装的完整配置')); confirm.append(replacement);
    const actions = document.createElement('div'); actions.className = 'hosted-migration-actions';
    const apply = button(next.proof ? '再次检查扫码并应用' : '扫码确认并应用', () => { void flow.apply().catch(() => undefined); }, busy || !next.canApply); apply.dataset.variant = 'primary';
    if (next.canCancel) actions.append(button('取消此迁移', () => { void flow.cancel().catch(() => undefined); }, busy)); actions.append(apply); confirm.append(actions);
    if (next.proof) { const proof = document.createElement('div'); proof.className = 'hosted-migration-proof'; const image = document.createElement('img'); image.className = 'hosted-qr'; image.src = next.proof.qrImage; image.alt = '迁移确认 B 站二维码'; proof.append(image); appendText(proof, 'p', next.proofStatus === 'scanned' ? '已扫码，请在手机确认。' : '请使用 B 站客户端扫码确认。'); confirm.append(proof); }
    previewHost.append(confirm);

    if (next.job) { const progress = document.createElement('section'); progress.className = 'hosted-migration-card hosted-migration-progress'; appendText(progress, 'h2', '迁移进度'); const labels: Record<MigrationApplyProgress, string> = { idle: '尚未开始', preview_ready: '预览已就绪', waiting_for_live_boundary: '等待直播事件边界，B站连接不会断开', applied: '迁移已应用', cancelled: '迁移已取消', rolled_back: '已创建回滚版本', expired: '迁移预览已过期' }; appendText(progress, 'p', labels[next.applyProgress]); const progressActions = document.createElement('div'); progressActions.className = 'hosted-migration-actions'; if (next.canRefresh) progressActions.append(button('刷新应用进度', () => { void flow.refresh(next.job!.id).catch(() => undefined); }, busy)); if (next.canRollback) progressActions.append(button(`回滚（剩余 ${next.rollbackDaysRemaining ?? 0} 天）`, () => { void flow.rollback().catch(() => undefined); }, busy)); progress.append(progressActions); previewHost.append(progress); }
    if (next.obsChecklist?.length) { const obs = document.createElement('section'); obs.className = 'hosted-migration-card hosted-migration-obs'; appendText(obs, 'h2', '逐项替换 OBS 链接'); appendText(obs, 'p', 'EXE 的 localhost 链接仍可独立使用；请在 OBS 中逐项替换为新的 HTTPS 链接。'); for (const item of next.obsChecklist) { const row = document.createElement('label'); const box = document.createElement('input'); box.type = 'checkbox'; box.checked = item.replaced; box.addEventListener('change', () => flow.confirmOBSReplacement(item.outputId, box.checked)); const copy = document.createElement('span'); appendText(copy, 'strong', item.name || item.outputId); const link = document.createElement('a'); link.href = item.url; link.textContent = item.url; link.target = '_blank'; link.rel = 'noopener noreferrer'; copy.append(link); row.append(box, copy); obs.append(row); } previewHost.append(obs); }
  };
  const flow = createMigrationFlow(api, render); render(state);
  const beforeUnload = (event: BeforeUnloadEvent): void => { if (state.rawFileActive || state.operationInFlight) { event.preventDefault(); event.returnValue = ''; } }; document.defaultView?.addEventListener('beforeunload', beforeUnload);
  return Object.freeze({ flow, async dispose(): Promise<void> { disposed = true; document.defaultView?.removeEventListener('beforeunload', beforeUnload); selected = undefined; file.value = ''; root.replaceChildren(); await flow.dispose(); } });
}
