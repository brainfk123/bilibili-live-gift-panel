import { mountMigrationView, type MigrationAPI } from '../../migration';
import type { MigrationHistoryJob } from '../../api';

export const HOSTED_USER_SETTINGS_SECTIONS = Object.freeze([
  { id: 'account', label: '账号', description: 'B站身份与 Hosted 账号资料管理将在后续功能中接入。' },
  { id: 'devices', label: '已登录设备', description: '设备列表与会话撤销将在后续功能中接入。' },
  { id: 'retention', label: '数据保留', description: '默认保留 180 天；30、90、180、365 天选项将在数据中心接入时开放。' },
  { id: 'migration', label: '迁移中心', description: '上传迁移包、选择玩法、解决冲突，并查看应用历史与 7 天回滚入口。' },
] as const);

export const migrationPromptStorageKey = (accountScope: string): string => `hosted.migration.prompt.dismissed.v1.${accountScope}`;

interface PromptStorage { getItem(key: string): string | null; setItem(key: string, value: string): void }

export function shouldShowMigrationPrompt(storage: PromptStorage | undefined, accountScope: string): boolean {
  if (!storage) return true;
  try { return storage.getItem(migrationPromptStorageKey(accountScope)) !== 'true'; }
  catch { return true; }
}

export function dismissMigrationPrompt(storage: PromptStorage | undefined, accountScope: string): void {
  if (!storage) return;
  try { storage.setItem(migrationPromptStorageKey(accountScope), 'true'); }
  catch { /* A blocked storage policy must not block the account page. */ }
}

export function mountMigrationPrompt(container: HTMLElement, storage: PromptStorage | undefined, accountScope: string, callbacks: { onOpen(): void }) {
  const document = container.ownerDocument;
  if (!shouldShowMigrationPrompt(storage, accountScope)) return Object.freeze({ dispose(): void { container.replaceChildren(); } });
  const prompt = document.createElement('aside'); prompt.className = 'hosted-migration-prompt'; prompt.setAttribute('aria-label', '本地配置迁移提示');
  const copy = document.createElement('div'); const title = document.createElement('strong'); title.textContent = '已有本地 EXE 配置？'; const text = document.createElement('p'); text.textContent = '可上传一次迁移包，在服务器预览后选择要导入的玩法。也可以稍后从设置进入。'; copy.append(title, text);
  const actions = document.createElement('div'); actions.className = 'hosted-migration-prompt-actions';
  const skip = document.createElement('button'); skip.type = 'button'; skip.textContent = '暂时跳过';
  const open = document.createElement('button'); open.type = 'button'; open.textContent = '前往迁移'; open.dataset.variant = 'primary';
  const dismiss = (): void => { dismissMigrationPrompt(storage, accountScope); prompt.remove(); };
  skip.addEventListener('click', dismiss);
  open.addEventListener('click', () => { dismiss(); callbacks.onOpen(); });
  actions.append(skip, open); prompt.append(copy, actions); container.replaceChildren(prompt);
  return Object.freeze({ dispose(): void { prompt.remove(); } });
}

export function mountMigrationSettingsView(root: HTMLElement, api: MigrationAPI, callbacks: { onExit(): void }) {
  const document = root.ownerDocument;
  let nested: ReturnType<typeof mountMigrationView> | undefined;
  let disposed = false;
  let generation = 0;
  let history: MigrationHistoryJob[] = [];
  let historyController: AbortController | undefined;
  const renderSettings = (): void => {
    if (disposed || nested) return;
    const page = document.createElement('main'); page.className = 'hosted-migration-settings';
    const header = document.createElement('header'); header.className = 'hosted-migration-header';
    const title = document.createElement('h1'); title.textContent = '设置'; const back = document.createElement('button'); back.type = 'button'; back.textContent = '返回账号'; back.addEventListener('click', callbacks.onExit); header.append(title, back);
    for (const section of HOSTED_USER_SETTINGS_SECTIONS.slice(0, 3)) {
      const card = document.createElement('section'); card.className = 'hosted-migration-settings-card'; card.dataset.section = section.id;
      const copy = document.createElement('div'); const heading = document.createElement('h2'); heading.textContent = section.label; const description = document.createElement('p'); description.textContent = section.description; copy.append(heading, description);
      card.append(copy); page.append(card);
    }
    const migrationSection = HOSTED_USER_SETTINGS_SECTIONS[3];
    const card = document.createElement('section'); card.className = 'hosted-migration-settings-card'; card.dataset.section = migrationSection.id;
    const copy = document.createElement('div'); const heading = document.createElement('h2'); heading.textContent = migrationSection.label; const description = document.createElement('p'); description.textContent = migrationSection.description; copy.append(heading, description);
    const open = document.createElement('button'); open.type = 'button'; open.textContent = '打开迁移中心'; open.addEventListener('click', () => { void openJob(); });
    card.append(copy, open); page.append(card); page.prepend(header); root.replaceChildren(page);
    const recoverable=history.filter((item)=>item.status==='pending'&&Boolean(item.expiresAt)&&Date.parse(item.expiresAt!)>Date.now()||item.status==='applied'&&Boolean(item.rollbackExpiresAt)&&Date.parse(item.rollbackExpiresAt!)>Date.now());
    if(recoverable.length){const list=document.createElement('section');list.className='hosted-migration-settings-history';const heading=document.createElement('h2');heading.textContent='可继续的迁移';list.append(heading);for(const item of recoverable){const row=document.createElement('article');const text=document.createElement('div');const title=document.createElement('strong');title.textContent=item.status==='pending'?'等待应用':'可回滚的迁移';const detail=document.createElement('p');detail.textContent=`任务 ${item.id} · ${item.createdAt}`;text.append(title,detail);const action=document.createElement('button');action.type='button';action.textContent=item.status==='pending'?'继续查看':'查看并回滚';action.addEventListener('click',()=>{void openJob(item);});row.append(text,action);list.append(row);}page.append(list);}
  };
  const loadHistory = (): Promise<void> => {
    const current = ++generation;
    historyController?.abort();
    const controller = new AbortController();
    historyController = controller;
    return (api.migrationHistory?.(controller.signal) ?? Promise.resolve([])).then((items) => {
      if (!disposed && !nested && current === generation) { history = items; renderSettings(); }
    }).catch(() => undefined).finally(() => { if (historyController === controller) historyController = undefined; });
  };
  const returnToSettings = async (child: ReturnType<typeof mountMigrationView>): Promise<void> => {
    const current = ++generation;
    historyController?.abort(); historyController = undefined;
    if (nested === child) nested = undefined;
    await child.dispose();
    if (disposed || current !== generation) return;
    history = []; renderSettings(); await loadHistory();
  };
  const openJob = async (item?: MigrationHistoryJob): Promise<void> => {
    if (disposed) return;
    const current = ++generation;
    historyController?.abort(); historyController = undefined;
    const previous = nested; nested = undefined;
    await previous?.dispose();
    if (disposed || current !== generation) return;
    let child: ReturnType<typeof mountMigrationView>;
    child = mountMigrationView(root, api, { onConfiguration: () => { void returnToSettings(child); } });
    nested = child;
    if(item)child.flow.resumeJob({id:item.id,status:item.status,...(item.expiresAt?{expiresAt:item.expiresAt}:{}),...(item.rollbackExpiresAt?{rollbackExpiresAt:item.rollbackExpiresAt}:{}),...(item.obsReissueAvailable?{obsReissueAvailable:true,obsReissueRequired:true}:{})});
  };
  renderSettings();
  const ready = loadHistory();
  return Object.freeze({ ready, async dispose(): Promise<void> { disposed = true; generation += 1; historyController?.abort(); historyController = undefined; const child = nested; nested = undefined; await child?.dispose(); root.replaceChildren(); } });
}
