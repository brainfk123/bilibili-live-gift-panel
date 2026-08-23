import type { HostedView } from '../../shell';
import { runAdminAction } from '../ui/async-action';

export function mountInvitationCreatePanel(host: HTMLElement, createInvitations: (count: number, validity: '7d' | '30d' | 'permanent') => Promise<void>, onClose: () => void): HostedView {
  const document = host.ownerDocument; const panel = document.createElement('section'); panel.className = 'hosted-admin-invitation-create';
  const countLabel = document.createElement('label'); countLabel.className = 'hosted-admin-field'; countLabel.textContent = '数量'; const count = document.createElement('select'); count.setAttribute('aria-label', '邀请码数量');
  for (const value of [1, 5, 10, 20, 50]) { const option = document.createElement('option'); option.value = String(value); option.textContent = `${value} 个`; count.append(option); } countLabel.append(count);
  const validityLabel = document.createElement('label'); validityLabel.className = 'hosted-admin-field'; validityLabel.textContent = '有效期'; const validity = document.createElement('select'); validity.setAttribute('aria-label', '邀请码有效期');
  for (const [label, value] of [['7 天', '7d'], ['30 天', '30d'], ['永久', 'permanent']] as const) { const option = document.createElement('option'); option.value = value; option.textContent = label; validity.append(option); } validityLabel.append(validity);
  const create = document.createElement('button'); create.type = 'button'; create.dataset.variant = 'primary'; create.textContent = '创建'; create.addEventListener('click', () => { void runAdminAction(create, { idle: '创建', busy: '创建中…' }, () => createInvitations(Number(count.value), validity.value as '7d' | '30d' | 'permanent')); });
  const cancel = document.createElement('button'); cancel.type = 'button'; cancel.dataset.variant = 'quiet'; cancel.textContent = '取消'; cancel.addEventListener('click', onClose);
  panel.append(countLabel, validityLabel, create, cancel); host.append(panel);
  const onKey = (event: KeyboardEvent): void => { if (event.key === 'Escape') onClose(); }; panel.addEventListener('keydown', onKey);
  return { dispose() { panel.removeEventListener('keydown', onKey); panel.remove(); } };
}
