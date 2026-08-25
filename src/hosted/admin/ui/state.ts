export type AdminStateKind = 'loading' | 'empty' | 'error';

export function createAdminState(
  document: Document,
  kind: AdminStateKind,
  message: string,
  retry?: () => void,
): HTMLElement {
  const state = document.createElement('section');
  state.className = `hosted-admin-state is-${kind}`;
  state.setAttribute('role', kind === 'error' ? 'alert' : 'status');
  if (kind === 'loading') {
    const spinner = document.createElement('span');
    spinner.className = 'hosted-admin-action-spinner';
    spinner.setAttribute('aria-hidden', 'true');
    state.append(spinner);
  }
  const text = document.createElement('p');
  text.textContent = message;
  state.append(text);
  if (retry) {
    const button = document.createElement('button');
    button.type = 'button';
    button.dataset.variant = 'secondary';
    button.textContent = '重试';
    button.addEventListener('click', retry);
    state.append(button);
  }
  return state;
}
