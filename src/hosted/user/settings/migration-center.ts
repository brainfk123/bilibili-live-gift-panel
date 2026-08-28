import { mountMigrationView, type MigrationAPI } from '../../migration';

export const migrationPromptStorageKey = 'hosted.migration.prompt.dismissed.v1';

interface PromptStorage { getItem(key: string): string | null; setItem(key: string, value: string): void }

export function shouldShowMigrationPrompt(storage: PromptStorage | undefined): boolean {
  if (!storage) return true;
  try { return storage.getItem(migrationPromptStorageKey) !== 'true'; }
  catch { return true; }
}

export function dismissMigrationPrompt(storage: PromptStorage | undefined): void {
  if (!storage) return;
  try { storage.setItem(migrationPromptStorageKey, 'true'); }
  catch { /* A blocked storage policy must not block the account page. */ }
}

export function mountMigrationPrompt(container: HTMLElement, storage: PromptStorage | undefined, callbacks: { onOpen(): void }) {
  const document = container.ownerDocument;
  if (!shouldShowMigrationPrompt(storage)) return Object.freeze({ dispose(): void { container.replaceChildren(); } });
  const prompt = document.createElement('aside'); prompt.className = 'hosted-migration-prompt'; prompt.setAttribute('aria-label', '本地配置迁移提示');
  const copy = document.createElement('div'); const title = document.createElement('strong'); title.textContent = '已有本地 EXE 配置？'; const text = document.createElement('p'); text.textContent = '可上传一次迁移包，在服务器预览后选择要导入的玩法。也可以稍后从设置进入。'; copy.append(title, text);
  const actions = document.createElement('div'); actions.className = 'hosted-migration-prompt-actions';
  const skip = document.createElement('button'); skip.type = 'button'; skip.textContent = '暂时跳过';
  const open = document.createElement('button'); open.type = 'button'; open.textContent = '前往迁移'; open.dataset.variant = 'primary';
  const dismiss = (): void => { dismissMigrationPrompt(storage); prompt.remove(); };
  skip.addEventListener('click', dismiss);
  open.addEventListener('click', () => { dismiss(); callbacks.onOpen(); });
  actions.append(skip, open); prompt.append(copy, actions); container.replaceChildren(prompt);
  return Object.freeze({ dispose(): void { prompt.remove(); } });
}

export function mountMigrationSettingsView(root: HTMLElement, api: MigrationAPI, callbacks: { onExit(): void }) {
  const document = root.ownerDocument; let nested: { dispose(): Promise<void> } | undefined; let disposed = false;
  const renderSettings = (): void => {
    if (disposed) return;
    const page = document.createElement('main'); page.className = 'hosted-migration-settings';
    const header = document.createElement('header'); header.className = 'hosted-migration-header';
    const title = document.createElement('h1'); title.textContent = '设置'; const back = document.createElement('button'); back.type = 'button'; back.textContent = '返回账号'; back.addEventListener('click', callbacks.onExit); header.append(title, back);
    const card = document.createElement('section'); card.className = 'hosted-migration-settings-card';
    const copy = document.createElement('div'); const heading = document.createElement('h2'); heading.textContent = '从本地 EXE 迁移'; const description = document.createElement('p'); description.textContent = '上传迁移包、选择玩法、解决冲突，并查看应用历史与 7 天回滚入口。'; copy.append(heading, description);
    const open = document.createElement('button'); open.type = 'button'; open.textContent = '打开迁移中心'; open.addEventListener('click', () => { if (!disposed) nested = mountMigrationView(root, api, { onConfiguration: () => { void nested?.dispose().then(renderSettings); } }); });
    card.append(copy, open); page.append(header, card); root.replaceChildren(page);
  };
  renderSettings();
  return Object.freeze({ async dispose(): Promise<void> { disposed = true; await nested?.dispose(); nested = undefined; root.replaceChildren(); } });
}
