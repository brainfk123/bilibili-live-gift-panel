export interface AdminActionLabels { idle: string; busy: string; }

export async function runAdminAction(
  button: HTMLButtonElement,
  labels: AdminActionLabels,
  operation: () => Promise<void>,
): Promise<'success' | 'failure'> {
  button.disabled = true;
  button.setAttribute('aria-busy', 'true');
  button.textContent = labels.busy;
  const spinner = button.ownerDocument.createElement('span');
  spinner.className = 'hosted-admin-action-spinner';
  spinner.setAttribute('aria-hidden', 'true');
  button.append(spinner);
  try {
    await operation();
    return 'success';
  } catch {
    return 'failure';
  } finally {
    button.disabled = false;
    button.removeAttribute('aria-busy');
    button.textContent = labels.idle;
  }
}
