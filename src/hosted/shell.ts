export type HostedServiceStatus = 'checking' | 'ready' | 'unavailable';

export interface HostedSession {
  serviceStatus: HostedServiceStatus;
  onLogin: () => void;
}

const statusCopy: Record<HostedServiceStatus, string> = {
  checking: '服务状态：正在确认服务可用性',
  ready: '服务状态：可用',
  unavailable: '服务状态：暂不可用',
};

/** Renders the signed-out hosted entry without accessing browser persistence or network APIs. */
export function renderHostedShell(root: HTMLElement, session: HostedSession): void {
  const document = root.ownerDocument;
  if (!document) throw new Error('Hosted shell requires a document.');

  const shell = document.createElement('main');
  shell.className = 'hosted-shell';
  shell.setAttribute('aria-labelledby', 'hosted-title');

  const title = document.createElement('h1');
  title.id = 'hosted-title';
  title.textContent = '礼物互动工坊';

  const status = document.createElement('p');
  status.className = 'hosted-status';
  status.setAttribute('role', 'status');
  status.setAttribute('aria-live', 'polite');
  status.textContent = statusCopy[session.serviceStatus];

  const login = document.createElement('button');
  login.type = 'button';
  login.className = 'hosted-login';
  login.setAttribute('aria-label', '使用 B 站账号登录');
  login.textContent = '使用 B 站账号登录';
  login.addEventListener('click', session.onLogin);

  const privacy = document.createElement('section');
  privacy.className = 'hosted-privacy';
  privacy.setAttribute('aria-labelledby', 'hosted-privacy-title');

  const privacyTitle = document.createElement('h2');
  privacyTitle.id = 'hosted-privacy-title';
  privacyTitle.textContent = '隐私说明';

  const privacySummary = document.createElement('p');
  privacySummary.textContent = '仅在登录授权后处理账号信息；不会在此页面读取或保存浏览器 Cookie。';

  privacy.append(privacyTitle, privacySummary);
  shell.append(title, status, login, privacy);
  root.replaceChildren(shell);
}
