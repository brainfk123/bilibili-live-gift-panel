import { HostedAPIError, type Challenge, type MigrationJob, type MigrationPreview } from './api';

interface MigrationAPI {
  previewMigration?(rawJSON: string): Promise<MigrationPreview>;
  getMigration?(id: number): Promise<MigrationJob>;
  applyMigration?(id: number, challengeID: string, keepRoomSuggestion: boolean): Promise<MigrationJob>;
  cancelMigration?(id: number): Promise<MigrationJob>;
  rollbackMigration?(id: number, challengeID: string): Promise<MigrationJob>;
  beginLogin?(): Promise<Challenge>;
  pollLogin?(id: string): Promise<{ status: 'pending' | 'scanned' | 'verified' | 'registration_required' | 'expired'; expiresAt?: string }>;
  cancelLogin?(id: string): Promise<void>;
}

export const migrationFileLimit = 2 * 1024 * 1024;
export interface MigrationFile { name: string; size: number; text(): Promise<string>; }
export interface MigrationViewState { preview?: MigrationPreview; job?: MigrationJob; proof?: Pick<Challenge, 'qrImage' | 'expiresAt'>; replacementConfirmed: boolean; keepRoomSuggestion: boolean; rawFileActive: boolean; operationInFlight: boolean; canApply: boolean; canCancel: boolean; canRefresh: boolean; canRollback: boolean; rollbackDaysRemaining?: number; error?: string; }

export function createMigrationFlow(api: MigrationAPI, render: (state: MigrationViewState) => void, clock: { now(): Date } = { now: () => new Date() }) {
  let preview: MigrationPreview | undefined; let job: MigrationJob | undefined; let proof: Challenge | undefined; let replacementConfirmed = false; let keepRoomSuggestion = false; let rawFileActive = false; let operationInFlight = false; let disposed = false; let error: string | undefined; let generation = 0; let proofOperation: Promise<void> | undefined;
  const days = (): number | undefined => { const expires = job?.rollbackExpiresAt; if (!expires) return undefined; return Math.max(0, Math.ceil((Date.parse(expires) - clock.now().getTime()) / 86_400_000)); };
  const canApply = (): boolean => job?.status === 'previewed' && replacementConfirmed;
  const canCancel = (): boolean => job?.status === 'previewed' || job?.status === 'pending';
  const canRefresh = (): boolean => job?.status === 'pending';
  const canRollback = (): boolean => job?.status === 'applied' && (days() ?? 0) > 0;
  const publish = (): void => { if (!disposed) render({ ...(preview ? { preview: structuredClone(preview) } : {}), ...(job ? { job: structuredClone(job) } : {}), ...(proof ? { proof: { qrImage: proof.qrImage, expiresAt: proof.expiresAt } } : {}), replacementConfirmed, keepRoomSuggestion, rawFileActive, operationInFlight, canApply: canApply(), canCancel: canCancel(), canRefresh: canRefresh(), canRollback: canRollback(), ...(days() === undefined ? {} : { rollbackDaysRemaining: days() }), ...(error ? { error } : {}) }); };
  const fail = (): void => { error = '操作失败，请重试'; publish(); };
  const bestEffortCancel = (challenge?: Challenge): void => { if (challenge) void api.cancelLogin?.(challenge.challengeId).catch(() => undefined); };
  const bestEffortJobCancel = (id: number): void => { void api.cancelMigration?.(id).catch(() => undefined); };
  const operationConflict = (): HostedAPIError => new HostedAPIError('operation_conflict', 409);
  const discardProof = (): void => { const active = proof; proof = undefined; bestEffortCancel(active); };
  const ensureLive = (started: number): void => { if (disposed || started !== generation) throw new HostedAPIError('operation_failed', 0); };
  const nextProof = async (started: number): Promise<Challenge> => { if (proof) return proof; if (!api.beginLogin) throw new HostedAPIError('invalid_request', 400); const created = await api.beginLogin(); if (disposed || started !== generation) { bestEffortCancel(created); throw new HostedAPIError('operation_failed', 0); } proof = created; publish(); return created; };
  const checkedProof = async (started: number): Promise<string> => { const active = await nextProof(started); if (!api.pollLogin) throw new HostedAPIError('invalid_request', 400); const result = await api.pollLogin(active.challengeId); ensureLive(started); if (result.status === 'pending') { error = '等待 B 站扫码确认，请稍后重试'; publish(); throw new HostedAPIError('verification_pending', 202); } if (result.status !== 'verified') { discardProof(); throw new HostedAPIError('proof_rejected', 401); } return active.challengeId; };
  const runProofOperation = (action: (challengeID: string, started: number) => Promise<void>): Promise<void> => {
    if (proofOperation || operationInFlight) return Promise.reject(operationConflict());
    const started = ++generation;
    operationInFlight = true; publish();
    const running = (async () => { try { const challengeID = await checkedProof(started); await action(challengeID, started); } catch (cause) { if (!(cause instanceof HostedAPIError && (cause.code === 'verification_pending' || cause.code === 'temporarily_unavailable'))) discardProof(); if (!disposed && started === generation && !(cause instanceof HostedAPIError && cause.code === 'verification_pending')) fail(); throw cause; } })();
    proofOperation = running;
    void running.finally(() => { if (proofOperation === running) proofOperation = undefined; if (!disposed && started === generation) { operationInFlight = false; publish(); } }).catch(() => undefined);
    return running;
  };
  return Object.freeze({
    async preview(file: MigrationFile): Promise<void> { if (proofOperation || operationInFlight) throw operationConflict(); if (disposed || !api.previewMigration || !file.name.toLowerCase().endsWith('.json') || !Number.isSafeInteger(file.size) || file.size < 0 || file.size > migrationFileLimit) throw new HostedAPIError('invalid_request', 400); const started = ++generation; rawFileActive = true; operationInFlight = true; publish(); let raw = ''; try { raw = await file.text(); ensureLive(started); const result = await api.previewMigration(raw); if (disposed || started !== generation) { if (!result.reused) bestEffortJobCancel(result.id); throw new HostedAPIError('operation_failed', 0); } const authoritativeJob = result.reused ? await api.getMigration?.(result.id) : undefined; ensureLive(started); if (result.reused && !authoritativeJob) throw new HostedAPIError('invalid_request', 400); preview = result; job = authoritativeJob ?? { id: result.id, status: 'previewed', expiresAt: result.expiresAt }; replacementConfirmed = false; keepRoomSuggestion = false; error = undefined; } catch (cause) { if (!disposed && started === generation) fail(); throw cause; } finally { raw = ''; if (!disposed && started === generation) { rawFileActive = false; operationInFlight = false; publish(); } } },
    acceptPreview(value: MigrationPreview): void { if (disposed || proofOperation || operationInFlight) return; generation += 1; preview = structuredClone(value); job = { id: value.id, status: 'previewed', expiresAt: value.expiresAt }; replacementConfirmed = false; keepRoomSuggestion = false; error = undefined; publish(); },
    confirmReplacement(value: boolean): void { if (disposed || proofOperation || operationInFlight) return; replacementConfirmed = value; publish(); },
    setKeepRoomSuggestion(value: boolean): void { if (disposed || proofOperation || operationInFlight) return; keepRoomSuggestion = value; publish(); },
    apply(): Promise<void> { if (proofOperation || operationInFlight) return Promise.reject(operationConflict()); if (!preview || !canApply() || !api.applyMigration) return Promise.reject(new HostedAPIError('invalid_request', 400)); const id = preview.id; const keep = keepRoomSuggestion; return runProofOperation(async (challengeID, started) => { const result = await api.applyMigration!(id, challengeID, keep); ensureLive(started); job = result; discardProof(); error = undefined; publish(); }); },
    rollback(): Promise<void> { if (proofOperation || operationInFlight) return Promise.reject(operationConflict()); if (!job || !canRollback() || !api.rollbackMigration) return Promise.reject(new HostedAPIError('invalid_request', 400)); const id = job.id; return runProofOperation(async (challengeID, started) => { const result = await api.rollbackMigration!(id, challengeID); ensureLive(started); job = result; discardProof(); error = undefined; publish(); }); },
    async refresh(id: number): Promise<void> { if (proofOperation || operationInFlight) throw operationConflict(); if (disposed || !canRefresh() || !job || id !== job.id || !api.getMigration) throw new HostedAPIError('invalid_request', 400); const started = ++generation; operationInFlight = true; publish(); try { const result = await api.getMigration(id); ensureLive(started); job = result; error = undefined; publish(); } catch (cause) { if (!disposed && started === generation) fail(); throw cause; } finally { if (!disposed && started === generation) { operationInFlight = false; publish(); } } },
    async cancel(): Promise<void> { if (proofOperation || operationInFlight) throw operationConflict(); if (disposed || !canCancel() || !job || !api.cancelMigration) throw new HostedAPIError('invalid_request', 400); const started = ++generation; const id = job.id; operationInFlight = true; publish(); try { const result = await api.cancelMigration(id); ensureLive(started); job = result; error = undefined; publish(); } catch (cause) { if (!disposed && started === generation) fail(); throw cause; } finally { if (!disposed && started === generation) { operationInFlight = false; publish(); } } },
    reportFailure(_cause: unknown): void { fail(); },
    async dispose(): Promise<void> { generation += 1; disposed = true; const active = proof; proof = undefined; preview = undefined; job = undefined; error = undefined; rawFileActive = false; operationInFlight = false; bestEffortCancel(active); },
  });
}

export function mountMigrationView(root: HTMLElement, api: MigrationAPI, callbacks: { onConfiguration(): void }) {
  const document = root.ownerDocument; let disposed = false; let selected: File | undefined; let state: MigrationViewState = { replacementConfirmed: false, keepRoomSuggestion: false, rawFileActive: false, operationInFlight: false, canApply: false, canCancel: false, canRefresh: false, canRollback: false };
  const panel = document.createElement('main'); panel.className = 'hosted-shell hosted-panel'; const title = document.createElement('h1'); title.textContent = '迁移本地配置';
  const status = document.createElement('p'); status.setAttribute('role', 'alert'); status.setAttribute('aria-live', 'assertive');
  const file = document.createElement('input'); file.type = 'file'; file.accept = '.json,application/json'; file.setAttribute('aria-label', '选择迁移 JSON 文件'); file.addEventListener('change', () => { selected = file.files?.item(0) ?? undefined; });
  const previewButton = document.createElement('button'); previewButton.type = 'button'; previewButton.addEventListener('click', () => { if (!selected) { status.textContent = '请选择 .json 文件'; return; } void flow.preview(selected).catch(() => undefined).finally(() => { selected = undefined; file.value = ''; }); });
  const groups = document.createElement('ul'); groups.className = 'hosted-list';
  const replace = document.createElement('label'); const replaceBox = document.createElement('input'); replaceBox.type = 'checkbox'; replace.append(replaceBox, document.createTextNode('我确认用这份迁移整体替换当前在线配置和状态')); replaceBox.addEventListener('change', () => flow.confirmReplacement(replaceBox.checked));
  const keep = document.createElement('label'); const keepBox = document.createElement('input'); const keepText = document.createTextNode(''); keepBox.type = 'checkbox'; keep.append(keepBox, keepText); keepBox.addEventListener('change', () => flow.setKeepRoomSuggestion(keepBox.checked));
  const apply = document.createElement('button'); apply.type = 'button'; apply.addEventListener('click', () => { void flow.apply().catch(() => undefined); });
  const qr = document.createElement('img'); qr.className = 'hosted-qr'; qr.alt = '迁移确认 B 站二维码';
  const job = document.createElement('p'); const refresh = document.createElement('button'); refresh.type = 'button'; refresh.textContent = '刷新迁移状态'; refresh.addEventListener('click', () => { if (state.job) void flow.refresh(state.job.id).catch(() => undefined); });
  const cancel = document.createElement('button'); cancel.type = 'button'; cancel.textContent = '取消迁移'; cancel.addEventListener('click', () => { void flow.cancel().catch(() => undefined); });
  const rollback = document.createElement('button'); rollback.type = 'button'; rollback.addEventListener('click', () => { void flow.rollback().catch(() => undefined); });
  const back = document.createElement('button'); back.type = 'button'; back.textContent = '返回在线配置'; back.addEventListener('click', () => { if (!back.disabled) callbacks.onConfiguration(); });
  panel.append(title, status, file, previewButton, groups, replace, keep, apply, qr, job, refresh, cancel, rollback, back); root.replaceChildren(panel);
  const visible = (element: HTMLElement, value: boolean): void => { element.hidden = !value; };
  const render = (next: MigrationViewState): void => {
    if (disposed) return; state = next; const busy = next.operationInFlight;
    status.textContent = next.error ?? (next.job?.status === 'pending' ? '迁移将在当前直播会话自然结束后生效。' : '文件只在预览请求期间读取，不会保存在浏览器。');
    previewButton.textContent = next.rawFileActive ? '正在预览…' : '生成服务器预览'; previewButton.disabled = busy; file.disabled = busy;
    visible(groups, Boolean(next.preview)); visible(replace, Boolean(next.preview)); replaceBox.checked = next.replacementConfirmed; replaceBox.disabled = busy;
    while (groups.firstChild) groups.removeChild(groups.firstChild); if (next.preview) { const c = next.preview.counts; for (const text of [`属性：${c.attributes}`, `规则：${c.rules}`, `活动：${c.activities}`, `礼物目标面板：${c.giftTargetPanels}`, `礼物目标项：${c.giftTargetItems}`, ...(next.preview.warnings ?? []).map((warning) => `提示：${warning}`), ...(next.preview.ignored ?? []).map((ignored) => `已忽略：${ignored}`)]) { const row = document.createElement('li'); row.textContent = text; groups.append(row); } }
    visible(keep, Boolean(next.preview?.roomSuggestion)); keepText.textContent = next.preview?.roomSuggestion ? `保留建议直播间 ${next.preview.roomSuggestion}（不会自动运行）` : ''; keepBox.checked = next.keepRoomSuggestion; keepBox.disabled = busy;
    visible(apply, next.canApply); apply.textContent = next.proof ? '再次检查 B 站扫码并应用' : '创建 B 站验证后应用'; apply.disabled = busy;
    visible(qr, Boolean(next.proof)); if (next.proof) qr.src = next.proof.qrImage; else qr.removeAttribute('src');
    visible(job, Boolean(next.job)); job.textContent = next.job ? `迁移状态：${next.job.status}` : ''; visible(refresh, next.canRefresh); refresh.disabled = busy; visible(cancel, next.canCancel); cancel.disabled = busy; visible(rollback, next.canRollback && next.rollbackDaysRemaining !== undefined); rollback.textContent = `在 ${next.rollbackDaysRemaining ?? 0} 天内回滚`; rollback.disabled = busy; back.disabled = busy;
  };
  const flow = createMigrationFlow(api, render); render(state);
  const pagehide = (): void => { void dispose(); }; document.defaultView?.addEventListener('pagehide', pagehide, { once: true });
  const dispose = async (): Promise<void> => { if (disposed) return; disposed = true; selected = undefined; file.value = ''; qr.removeAttribute('src'); root.replaceChildren(); document.defaultView?.removeEventListener('pagehide', pagehide); void flow.dispose(); };
  return Object.freeze({ dispose });
}
