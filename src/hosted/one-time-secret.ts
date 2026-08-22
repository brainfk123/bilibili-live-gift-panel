export function createOneTimeSecretDialog(
  document: Document,
  options: {
    titleID: string;
    title: string;
    value: string;
    copyLabel: string;
    onCopy(): void;
    onClose(): void;
  },
): HTMLDialogElement {
  const dialog = document.createElement('dialog');
  dialog.className = 'hosted-secret-dialog';
  dialog.setAttribute('aria-modal', 'true');
  dialog.setAttribute('aria-labelledby', options.titleID);

  const header = document.createElement('header');
  header.className = 'hosted-secret-dialog-header';
  const title = document.createElement('h2');
  title.id = options.titleID;
  title.textContent = options.title;
  const warning = document.createElement('p');
  warning.textContent = '只显示一次，请先复制并妥善保存。';
  header.append(title, warning);

  const secret = document.createElement('code');
  secret.className = 'hosted-secret-dialog-value';
  secret.textContent = options.value;

  const actions = document.createElement('div');
  actions.className = 'hosted-secret-dialog-actions';
  const copy = document.createElement('button');
  copy.type = 'button';
  copy.className = 'hosted-secret-dialog-primary';
  copy.textContent = options.copyLabel;
  copy.addEventListener('click', options.onCopy);
  const close = document.createElement('button');
  close.type = 'button';
  close.className = 'hosted-secret-dialog-secondary';
  close.textContent = '已保存并关闭';
  close.addEventListener('click', options.onClose);
  actions.append(copy, close);

  dialog.addEventListener('cancel', (event) => { event.preventDefault(); options.onClose(); });
  dialog.append(header, secret, actions);
  return dialog;
}
