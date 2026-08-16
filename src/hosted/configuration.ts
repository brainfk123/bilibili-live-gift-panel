import { HostedAPIError, isHostedConfigurationDefinition, isHostedConfigurationRuntime, type HostedConfiguration, type HostedConfigurationDefinition, type HostedConfigurationRuntime } from './api';

interface ConfigurationAPI {
  loadConfiguration(): Promise<HostedConfiguration>;
  saveConfigurationDefinition(expectedVersion: number, definition: HostedConfigurationDefinition): Promise<{ version: number; revision: number }>;
  saveConfigurationRuntime(expectedRevision: number, runtime: HostedConfigurationRuntime): Promise<{ revision: number }>;
  suggestRoom(roomID: string): Promise<void>;
}

export interface ConfigurationViewState {
  authoritative?: HostedConfiguration;
  draftDefinition?: HostedConfigurationDefinition;
  draftRuntime?: HostedConfigurationRuntime;
  conflict: boolean;
  busy: boolean;
  error?: string;
}

const copy = <T>(value: T): T => structuredClone(value);

export function createConfigurationFlow(api: ConfigurationAPI, render: (state: ConfigurationViewState) => void) {
  let authoritative: HostedConfiguration | undefined;
  let draftDefinition: HostedConfigurationDefinition | undefined;
  let draftRuntime: HostedConfigurationRuntime | undefined;
  let conflict = false; let busy = false; let disposed = false; let error: string | undefined;
  const publish = (): void => { if (!disposed) render({ ...(authoritative ? { authoritative: copy(authoritative) } : {}), ...(draftDefinition ? { draftDefinition: copy(draftDefinition) } : {}), ...(draftRuntime ? { draftRuntime: copy(draftRuntime) } : {}), conflict, busy, ...(error ? { error } : {}) }); };
  const genericFailure = (): void => { error = '保存失败，请重试'; publish(); };
  return Object.freeze({
    async load(): Promise<void> { if (disposed) throw new HostedAPIError('invalid_request', 400); busy = true; error = undefined; publish(); try { const loaded = await api.loadConfiguration(); if (disposed) return; authoritative = loaded; draftDefinition ??= copy(loaded.definition); draftRuntime ??= copy(loaded.runtime); } catch { if (!disposed) genericFailure(); throw new HostedAPIError('operation_failed', 0); } finally { if (!disposed) { busy = false; publish(); } } },
    setDefinition(definition: HostedConfigurationDefinition): void { if (!isHostedConfigurationDefinition(definition)) throw new HostedAPIError('invalid_request', 400); draftDefinition = copy(definition); conflict = false; error = undefined; publish(); },
    setRuntime(runtime: HostedConfigurationRuntime): void { if (!isHostedConfigurationRuntime(runtime)) throw new HostedAPIError('invalid_request', 400); draftRuntime = copy(runtime); conflict = false; error = undefined; publish(); },
    async saveDefinition(): Promise<void> {
      if (disposed || !authoritative || !draftDefinition || busy) throw new HostedAPIError('invalid_request', 400);
      busy = true; error = undefined; publish();
      try { const saved = await api.saveConfigurationDefinition(authoritative.version, copy(draftDefinition)); authoritative = { ...authoritative, version: saved.version, revision: saved.revision, definition: copy(draftDefinition) }; draftRuntime = copy(authoritative.runtime); conflict = false; }
      catch (cause) { if (cause instanceof HostedAPIError && cause.code === 'revision_conflict') { conflict = true; try { const loaded = await api.loadConfiguration(); if (!disposed) authoritative = loaded; } catch { genericFailure(); } } else genericFailure(); throw cause; }
      finally { busy = false; publish(); }
    },
    async saveRuntime(): Promise<void> {
      if (disposed || !authoritative || !draftRuntime || busy) throw new HostedAPIError('invalid_request', 400);
      busy = true; error = undefined; publish();
      try { const saved = await api.saveConfigurationRuntime(authoritative.revision, copy(draftRuntime)); authoritative = { ...authoritative, revision: saved.revision, runtime: copy(draftRuntime) }; conflict = false; }
      catch (cause) { if (cause instanceof HostedAPIError && cause.code === 'revision_conflict') { conflict = true; try { const loaded = await api.loadConfiguration(); if (!disposed) authoritative = loaded; } catch { genericFailure(); } } else genericFailure(); throw cause; }
      finally { busy = false; publish(); }
    },
    async suggestRoom(roomID: string): Promise<void> { if (disposed || !roomID.trim()) throw new HostedAPIError('invalid_request', 400); await api.suggestRoom(roomID.trim()); },
    dispose(): void { authoritative = undefined; draftDefinition = undefined; draftRuntime = undefined; error = undefined; conflict = false; busy = false; render({ conflict: false, busy: false }); disposed = true; },
  });
}

export function mountConfigurationView(root: HTMLElement, api: ConfigurationAPI, callbacks: { onMigration(): void; onExit(): void }) {
  const document = root.ownerDocument; let disposed = false; let loaded = false; let definitionDirty = false; let runtimeDirty = false; let definitionEditVersion = 0; let runtimeEditVersion = 0;
  const panel = document.createElement('main'); panel.className = 'hosted-shell hosted-panel'; const title = document.createElement('h1'); title.textContent = '在线配置';
  const status = document.createElement('p'); status.setAttribute('role', 'alert'); status.setAttribute('aria-live', 'assertive');
  const definitionLabel = document.createElement('label'); definitionLabel.textContent = '配置定义 JSON（仅保存在本页内存）'; const definitionInput = document.createElement('textarea'); definitionLabel.append(definitionInput);
  const runtimeLabel = document.createElement('label'); runtimeLabel.textContent = '当前玩法状态 JSON（仅保存在本页内存）'; const runtimeInput = document.createElement('textarea'); runtimeLabel.append(runtimeInput);
  const compare = document.createElement('pre'); compare.setAttribute('aria-label', '服务器权威配置对照');
  const parse = (input: HTMLTextAreaElement): HostedConfigurationDefinition => {
    const value: unknown = JSON.parse(input.value); if (!value || typeof value !== 'object' || Array.isArray(value)) throw new HostedAPIError('invalid_request', 400); return value as HostedConfigurationDefinition;
  };
  definitionInput.addEventListener('input', () => { definitionDirty = true; definitionEditVersion += 1; }); runtimeInput.addEventListener('input', () => { runtimeDirty = true; runtimeEditVersion += 1; });
  const saveDefinition = document.createElement('button'); saveDefinition.type = 'button'; saveDefinition.addEventListener('click', () => { try { flow.setDefinition(parse(definitionInput)); const savedEditVersion = definitionEditVersion; void flow.saveDefinition().then(() => { if (definitionEditVersion === savedEditVersion) definitionDirty = false; }).catch(() => undefined); } catch { status.textContent = 'JSON 格式无效，未发送到服务器'; } });
  const saveRuntime = document.createElement('button'); saveRuntime.type = 'button'; saveRuntime.addEventListener('click', () => { try { flow.setRuntime(parse(runtimeInput) as HostedConfigurationRuntime); const savedEditVersion = runtimeEditVersion; void flow.saveRuntime().then(() => { if (runtimeEditVersion === savedEditVersion) runtimeDirty = false; }).catch(() => undefined); } catch { status.textContent = 'JSON 格式无效，未发送到服务器'; } });
  const roomLabel = document.createElement('label'); roomLabel.textContent = '建议直播间号（不会自动运行）'; const room = document.createElement('input'); room.inputMode = 'numeric'; roomLabel.append(room);
  const roomButton = document.createElement('button'); roomButton.type = 'button'; roomButton.textContent = '保存房间建议'; roomButton.addEventListener('click', () => { void flow.suggestRoom(room.value).then(() => { room.value = ''; status.textContent = '已保存建议，尚未启动直播间。'; }).catch(() => { status.textContent = '房间建议保存失败'; }); });
  const confirmDiscard = (): boolean => !definitionDirty && !runtimeDirty || document.defaultView?.confirm('存在未保存的配置草稿，确定离开吗？') !== false;
  const migration = document.createElement('button'); migration.type = 'button'; migration.textContent = '迁移本地配置'; migration.addEventListener('click', () => { if (confirmDiscard()) callbacks.onMigration(); });
  const exit = document.createElement('button'); exit.type = 'button'; exit.textContent = '返回账号'; exit.addEventListener('click', () => { if (confirmDiscard()) callbacks.onExit(); });
  panel.append(title, status, definitionLabel, saveDefinition, runtimeLabel, saveRuntime, compare, roomLabel, roomButton, migration, exit); root.replaceChildren(panel);
  const render = (state: ConfigurationViewState): void => {
    if (disposed) return;
    status.textContent = state.conflict ? '服务器配置已更新；已保留本页未保存草稿，请对照后重试。' : state.error ?? '';
    if (!loaded && state.draftDefinition && state.draftRuntime) { if (definitionEditVersion === 0) definitionInput.value = JSON.stringify(state.draftDefinition, null, 2); if (runtimeEditVersion === 0) runtimeInput.value = JSON.stringify(state.draftRuntime, null, 2); loaded = true; if (definitionEditVersion === 0 && runtimeEditVersion === 0) definitionInput.focus(); }
    compare.textContent = state.authoritative ? `服务器权威版本 ${state.authoritative.version} / 状态修订 ${state.authoritative.revision}\n${JSON.stringify(state.authoritative, null, 2)}` : '';
    saveDefinition.textContent = state.busy ? '正在保存…' : '保存配置定义'; saveDefinition.disabled = state.busy;
    saveRuntime.textContent = state.busy ? '正在保存…' : '保存当前状态'; saveRuntime.disabled = state.busy;
  };
  const flow = createConfigurationFlow(api, render); render({ conflict: false, busy: false }); const ready = flow.load().catch(() => undefined);
  const beforeUnload = (event: BeforeUnloadEvent): void => { if (definitionDirty || runtimeDirty) { event.preventDefault(); event.returnValue = ''; } }; document.defaultView?.addEventListener('beforeunload', beforeUnload);
  return Object.freeze({ ready, dispose(): void { disposed = true; document.defaultView?.removeEventListener('beforeunload', beforeUnload); definitionDirty = false; runtimeDirty = false; definitionInput.value = ''; runtimeInput.value = ''; compare.textContent = ''; root.replaceChildren(); flow.dispose(); } });
}
