import { HostedAPIError, type Challenge, type MigrationJob, type MigrationPreview } from './api';

interface MigrationAPI {
  previewMigration?(rawJSON: string): Promise<MigrationPreview>;
  getMigration?(id: number): Promise<MigrationJob>;
  applyMigration?(id: number, challengeID: string, keepRoomSuggestion: boolean): Promise<MigrationJob>;
  cancelMigration?(id: number): Promise<MigrationJob>;
  rollbackMigration?(id: number, challengeID: string): Promise<MigrationJob>;
  beginLogin?(): Promise<Challenge>;
  pollLogin?(id: string): Promise<{ status: 'pending' | 'verified' | 'registration_required' | 'expired'; expiresAt?: string }>;
  cancelLogin?(id: string): Promise<void>;
}

export const migrationFileLimit = 2 * 1024 * 1024;
export interface MigrationFile { name: string; size: number; text(): Promise<string>; }
export interface MigrationViewState { preview?: MigrationPreview; job?: MigrationJob; proof?: Pick<Challenge, 'qrImage' | 'expiresAt'>; replacementConfirmed: boolean; keepRoomSuggestion: boolean; rawFileActive: boolean; rollbackDaysRemaining?: number; error?: string; }

export function createMigrationFlow(api: MigrationAPI, render: (state: MigrationViewState) => void, clock: { now(): Date } = { now: () => new Date() }) {
  let preview: MigrationPreview | undefined; let job: MigrationJob | undefined; let proof: Challenge | undefined; let replacementConfirmed = false; let keepRoomSuggestion = false; let rawFileActive = false; let disposed = false; let error: string | undefined;
  const days = (): number | undefined => { const expires = job?.rollbackExpiresAt; if (!expires) return undefined; return Math.max(0, Math.ceil((Date.parse(expires) - clock.now().getTime()) / 86_400_000)); };
  const publish = (): void => { if (!disposed) render({ ...(preview ? { preview: structuredClone(preview) } : {}), ...(job ? { job: structuredClone(job) } : {}), ...(proof ? { proof: { qrImage: proof.qrImage, expiresAt: proof.expiresAt } } : {}), replacementConfirmed, keepRoomSuggestion, rawFileActive, ...(days() === undefined ? {} : { rollbackDaysRemaining: days() }), ...(error ? { error } : {}) }); };
  const fail = (): void => { error = '操作失败，请重试'; publish(); };
  const discardProof = async (): Promise<void> => { const active = proof; proof = undefined; if (active) await api.cancelLogin?.(active.challengeId); };
  const nextProof = async (): Promise<Challenge> => { if (proof) return proof; if (!api.beginLogin) throw new HostedAPIError('invalid_request', 400); const created = await api.beginLogin(); if (disposed) { await api.cancelLogin?.(created.challengeId); throw new HostedAPIError('operation_failed', 0); } proof = created; publish(); return created; };
  const checkedProof = async (): Promise<string> => { const active = await nextProof(); if (!api.pollLogin) throw new HostedAPIError('invalid_request', 400); const result = await api.pollLogin(active.challengeId); if (result.status === 'pending') { error = '等待 B 站扫码确认，请稍后重试'; publish(); throw new HostedAPIError('verification_pending', 202); } if (result.status !== 'verified') { await discardProof(); throw new HostedAPIError('proof_rejected', 401); } return active.challengeId; };
  return Object.freeze({
    async preview(file: MigrationFile): Promise<void> { if (!api.previewMigration || !file.name.toLowerCase().endsWith('.json') || !Number.isSafeInteger(file.size) || file.size < 0 || file.size > migrationFileLimit) throw new HostedAPIError('invalid_request', 400); rawFileActive = true; publish(); let raw = ''; try { raw = await file.text(); if (disposed) return; const result = await api.previewMigration(raw); if (!disposed) { preview = result; replacementConfirmed = false; keepRoomSuggestion = false; error = undefined; } } catch (cause) { if (!disposed) fail(); throw cause; } finally { raw = ''; rawFileActive = false; publish(); } },
    acceptPreview(value: MigrationPreview): void { preview = structuredClone(value); replacementConfirmed = false; keepRoomSuggestion = false; error = undefined; publish(); },
    confirmReplacement(value: boolean): void { replacementConfirmed = value; publish(); },
    setKeepRoomSuggestion(value: boolean): void { keepRoomSuggestion = value; publish(); },
    async apply(): Promise<void> { if (!preview || !replacementConfirmed || !api.applyMigration) throw new HostedAPIError('invalid_request', 400); let id: string; try { id = await checkedProof(); job = await api.applyMigration(preview.id, id, keepRoomSuggestion); proof = undefined; error = undefined; publish(); } catch (cause) { if (!(cause instanceof HostedAPIError && cause.code === 'verification_pending')) await discardProof(); if (!disposed && !(cause instanceof HostedAPIError && cause.code === 'verification_pending')) fail(); throw cause; } },
    async rollback(): Promise<void> { if (!job || !api.rollbackMigration) throw new HostedAPIError('invalid_request', 400); let id: string; try { id = await checkedProof(); job = await api.rollbackMigration(job.id, id); proof = undefined; error = undefined; publish(); } catch (cause) { if (!(cause instanceof HostedAPIError && cause.code === 'verification_pending')) await discardProof(); if (!disposed && !(cause instanceof HostedAPIError && cause.code === 'verification_pending')) fail(); throw cause; } },
    async refresh(id: number): Promise<void> { if (!api.getMigration) throw new HostedAPIError('invalid_request', 400); try { job = await api.getMigration(id); error = undefined; publish(); } catch (cause) { fail(); throw cause; } },
    async cancel(): Promise<void> { if (!job || !api.cancelMigration) throw new HostedAPIError('invalid_request', 400); try { job = await api.cancelMigration(job.id); error = undefined; publish(); } catch (cause) { fail(); throw cause; } },
    reportFailure(_cause: unknown): void { fail(); },
    async dispose(): Promise<void> { disposed = true; const active = proof; proof = undefined; preview = undefined; job = undefined; error = undefined; if (active) await api.cancelLogin?.(active.challengeId); },
  });
}

export function mountMigrationView(root: HTMLElement, api: MigrationAPI, callbacks: { onConfiguration(): void }) {
  const document = root.ownerDocument; let disposed = false; let selected: File | undefined;
  const render = (state: MigrationViewState): void => {
    if (disposed) return;
    const panel = document.createElement('main'); panel.className = 'hosted-shell hosted-panel'; const title = document.createElement('h1'); title.textContent = '迁移本地配置';
    const status = document.createElement('p'); status.setAttribute('role', 'alert'); status.setAttribute('aria-live', 'assertive'); status.textContent = state.error ?? (state.job?.status === 'pending' ? '迁移将在当前直播会话自然结束后生效。' : '文件只在预览请求期间读取，不会保存在浏览器。');
    const file = document.createElement('input'); file.type = 'file'; file.accept = '.json,application/json'; file.setAttribute('aria-label', '选择迁移 JSON 文件'); file.addEventListener('change', () => { selected = file.files?.item(0) ?? undefined; });
     const previewButton = document.createElement('button'); previewButton.type = 'button'; previewButton.textContent = state.rawFileActive ? '正在预览…' : '生成服务器预览'; previewButton.disabled = state.rawFileActive; previewButton.addEventListener('click', () => { if (!selected) { status.textContent = '请选择 .json 文件'; return; } void flow.preview(selected).catch(() => undefined).finally(() => { selected = undefined; file.value = ''; }); });
    panel.append(title, status, file, previewButton);
    if (state.preview) {
      const groups = document.createElement('ul'); groups.className = 'hosted-list'; const c = state.preview.counts;
      for (const text of [`属性：${c.attributes}`, `规则：${c.rules}`, `活动：${c.activities}`, `礼物目标面板：${c.giftTargetPanels}`, `礼物目标项：${c.giftTargetItems}`]) { const row = document.createElement('li'); row.textContent = text; groups.append(row); }
      for (const warning of state.preview.warnings ?? []) { const row = document.createElement('li'); row.textContent = `提示：${warning}`; groups.append(row); }
      for (const ignored of state.preview.ignored ?? []) { const row = document.createElement('li'); row.textContent = `已忽略：${ignored}`; groups.append(row); }
      const replace = document.createElement('label'); const replaceBox = document.createElement('input'); replaceBox.type = 'checkbox'; replaceBox.checked = state.replacementConfirmed; replace.append(replaceBox, document.createTextNode('我确认用这份迁移整体替换当前在线配置和状态')); replaceBox.addEventListener('change', () => flow.confirmReplacement(replaceBox.checked));
      panel.append(groups, replace);
      if (state.preview.roomSuggestion) { const keep = document.createElement('label'); const keepBox = document.createElement('input'); keepBox.type = 'checkbox'; keepBox.checked = state.keepRoomSuggestion; keep.append(keepBox, document.createTextNode(`保留建议直播间 ${state.preview.roomSuggestion}（不会自动运行）`)); keepBox.addEventListener('change', () => flow.setKeepRoomSuggestion(keepBox.checked)); panel.append(keep); }
       const apply = document.createElement('button'); apply.type = 'button'; apply.textContent = state.proof ? '检查 B 站扫码并应用' : '创建 B 站验证后应用'; apply.disabled = !state.replacementConfirmed; apply.addEventListener('click', () => { void flow.apply().catch(() => undefined); }); panel.append(apply);
      if (state.proof) { const qr = document.createElement('img'); qr.className = 'hosted-qr'; qr.alt = '迁移确认 B 站二维码'; qr.src = state.proof.qrImage; panel.append(qr); }
    }
    if (state.job) { const job = document.createElement('p'); job.textContent = `迁移状态：${state.job.status}`; panel.append(job); const refresh = document.createElement('button'); refresh.type = 'button'; refresh.textContent = '刷新迁移状态'; refresh.addEventListener('click', () => { void flow.refresh(state.job!.id).catch(() => undefined); }); panel.append(refresh); if (state.job.status === 'previewed' || state.job.status === 'pending') { const cancel = document.createElement('button'); cancel.type = 'button'; cancel.textContent = '取消迁移'; cancel.addEventListener('click', () => { void flow.cancel().catch(() => undefined); }); panel.append(cancel); } if (state.rollbackDaysRemaining !== undefined) { const rollback = document.createElement('button'); rollback.type = 'button'; rollback.textContent = `在 ${state.rollbackDaysRemaining} 天内回滚`; rollback.addEventListener('click', () => { void flow.rollback().catch(() => undefined); }); panel.append(rollback); } }
    const back = document.createElement('button'); back.type = 'button'; back.textContent = '返回在线配置'; back.addEventListener('click', callbacks.onConfiguration); panel.append(back); root.replaceChildren(panel);
  };
  const flow = createMigrationFlow(api, render); render({ replacementConfirmed: false, keepRoomSuggestion: false, rawFileActive: false });
  return Object.freeze({ dispose: async (): Promise<void> => { disposed = true; selected = undefined; await flow.dispose(); root.replaceChildren(); } });
}
