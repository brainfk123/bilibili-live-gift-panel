import type { AdminInvitationRecord, AdminInvitationStatus, HostedAPI } from '../../api';
import type { HostedView } from '../../shell';
import { mountAdminNotice } from '../ui/notice';
import { createInvitationInventoryController, type InvitationInventorySnapshot } from './controller';
import { mountInvitationCreatePanel } from './create-panel';
import { createInvitationQueryState } from './model';
import { createAdminState } from '../ui/state';

const statusLabel = (status: AdminInvitationRecord['status']): string => ({ active: '可用', used: '已兑换', revoked: '已作废', expired: '已过期' })[status];

export function mountAdminInvitationView(host: HTMLElement, api: Pick<HostedAPI, 'adminInvitations' | 'createAdminInvitations' | 'revokeAdminInvitation'>): HostedView {
  const document = host.ownerDocument;
  const query = createInvitationQueryState();
  let disposed = false; let createView: HostedView | undefined; let timer: ReturnType<typeof setTimeout> | undefined;
  const actions = document.createElement('div'); actions.className = 'hosted-admin-invitation-actions';
  const search = document.createElement('input'); search.placeholder = '搜索邀请码后四位或兑换账号'; search.setAttribute('aria-label', '搜索邀请码');
  const filterLabel = document.createElement('label'); filterLabel.className = 'hosted-admin-field'; filterLabel.textContent = '状态';
  const filter = document.createElement('select'); filter.setAttribute('aria-label', '邀请码状态');
  for (const [label, value] of [['全部状态', ''], ['可用', 'active'], ['已兑换', 'used'], ['已作废', 'revoked'], ['已过期', 'expired']] as const) { const option = document.createElement('option'); option.value = value; option.textContent = label; filter.append(option); }
  filterLabel.append(filter);
  const open = document.createElement('button'); open.type = 'button'; open.dataset.variant = 'primary'; open.textContent = '创建邀请码'; actions.append(search, filterLabel, open);
  const noticeHost = document.createElement('div'); const notice = mountAdminNotice(noticeHost); const createHost = document.createElement('div'); const empty = document.createElement('section'); empty.className = 'hosted-admin-empty'; const table = document.createElement('div'); table.className = 'hosted-admin-invitation-table'; host.append(actions, noticeHost, createHost, empty, table);
  const clipboard = globalThis.navigator?.clipboard ? { writeText: (value: string) => globalThis.navigator.clipboard.writeText(value) } : undefined;
  const share = typeof globalThis.navigator?.share === 'function' ? (value: string) => globalThis.navigator.share({ text: value }) : undefined;
  let controller!: ReturnType<typeof createInvitationInventoryController>;
  const closeCreate = (): void => { void createView?.dispose(); createView = undefined; createHost.replaceChildren(); open.setAttribute('aria-expanded', 'false'); };
  const renderTable = (snapshot: InvitationInventorySnapshot): void => {
    table.replaceChildren(); empty.replaceChildren();
    if (!snapshot.rows.length && snapshot.loading) { table.append(createAdminState(document,'loading','正在加载邀请码…')); return; }
    if (!snapshot.rows.length && snapshot.error) return;
    if (!snapshot.rows.length) { const title = document.createElement('h3'); title.textContent = '还没有邀请码'; const description = document.createElement('p'); description.textContent = '创建邀请码后可分享给新的主播账号。'; const createFirst = document.createElement('button'); createFirst.type = 'button'; createFirst.dataset.variant = 'secondary'; createFirst.textContent = '创建第一个邀请码'; createFirst.addEventListener('click', () => open.click()); empty.append(title, description, createFirst); return; }
    const header = document.createElement('div'); header.className = 'hosted-admin-invitation-row hosted-admin-invitation-head'; const codeLabel = document.createElement('span'); codeLabel.textContent = '邀请码'; header.append(codeLabel);
    for (const [label, sort] of [['状态', 'status'], ['创建时间', 'created_at']] as const) { const button = document.createElement('button'); button.type = 'button'; button.dataset.variant = 'quiet'; button.textContent = label; button.setAttribute('aria-sort', query.get().sort === sort ? (query.get().direction === 'asc' ? 'ascending' : 'descending') : 'none'); button.addEventListener('click', () => { query.toggleSort(sort); void controller.reload(query.get()); }); header.append(button); }
    for (const label of ['兑换账号', '操作']) { const cell = document.createElement('span'); cell.textContent = label; header.append(cell); } table.append(header);
    for (const item of snapshot.rows) { const row = document.createElement('div'); row.className = 'hosted-admin-invitation-row'; const code = document.createElement('strong'); code.textContent = item.code ?? item.codeHint; const status = document.createElement('span'); status.textContent = statusLabel(item.status); const created = document.createElement('time'); created.textContent = new Date(item.createdAt).toLocaleString(); const used = document.createElement('span'); used.textContent = item.usedByAccountId ? `主播账号 #${item.usedByAccountId}` : '—'; const operation = document.createElement('span'); if (item.code) { const copy = document.createElement('button'); copy.type = 'button'; copy.dataset.variant = 'secondary'; copy.textContent = '复制'; copy.addEventListener('click', () => { void controller.copy(item.id); }); const shareButton = document.createElement('button'); shareButton.type = 'button'; shareButton.dataset.variant = 'secondary'; shareButton.textContent = '分享'; shareButton.addEventListener('click', () => { void controller.share(item.id); }); const revoke = document.createElement('button'); revoke.type = 'button'; revoke.dataset.variant = 'danger'; revoke.textContent = '作废'; revoke.addEventListener('click', () => { if (globalThis.confirm('确认作废该邀请码？')) void controller.revoke(item.id); }); operation.append(copy, shareButton, revoke); } row.append(code, status, created, used, operation); table.append(row); }
    if (snapshot.notice?.code) { const manual = document.createElement('p'); manual.className = 'hosted-admin-manual-copy'; manual.textContent = '长按或拖选后复制'; const code = document.createElement('code'); code.textContent = snapshot.notice.code; table.append(manual, code); }
  };
  const render = (snapshot: InvitationInventorySnapshot): void => { if (disposed) return; if (snapshot.error) notice.show('error', snapshot.error, () => { void controller.reload(query.get()); }); else if (snapshot.notice) notice.show(snapshot.notice.kind, snapshot.notice.message); else notice.clear(); renderTable(snapshot); };
  controller = createInvitationInventoryController(api, render, clipboard, share);
  open.addEventListener('click', () => { if (createView) { closeCreate(); return; } open.setAttribute('aria-expanded', 'true'); createView = mountInvitationCreatePanel(createHost, async (count, validity) => controller.create(count, validity), closeCreate); });
  search.addEventListener('input', () => { if (timer) clearTimeout(timer); timer = setTimeout(() => { query.update({ query: search.value }); void controller.reload(query.get()); }, 300); });
  filter.addEventListener('change', () => { query.update({ status: (filter.value || undefined) as AdminInvitationStatus | undefined }); void controller.reload(query.get()); });
  void controller.reload(query.get());
  return { async dispose() { disposed = true; controller.dispose(); if (timer) clearTimeout(timer); await createView?.dispose(); notice.dispose(); } };
}
