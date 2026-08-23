export type AdminNoticeKind = 'info' | 'success' | 'warning' | 'error';

export interface AdminNoticeControl {
  readonly element: HTMLElement;
  show(kind: AdminNoticeKind, message: string, retry?: () => void): void;
  clear(): void;
  dispose(): void;
}

export function mountAdminNotice(host: HTMLElement): AdminNoticeControl {
  const document = host.ownerDocument;
  const element = document.createElement('section');
  element.className = 'hosted-admin-notice';
  element.setAttribute('role', 'status');
  element.setAttribute('aria-live', 'polite');
  element.hidden = true;
  host.append(element);

  const clear = (): void => {
    element.hidden = true;
    element.replaceChildren();
  };

  return {
    element,
    show(kind, message, retry) {
      element.dataset.kind = kind;
      element.replaceChildren();
      const text = document.createElement('span');
      text.textContent = message;
      element.append(text);
      if (retry) {
        const retryButton = document.createElement('button');
        retryButton.type = 'button';
        retryButton.dataset.variant = 'secondary';
        retryButton.textContent = '重试';
        retryButton.addEventListener('click', retry);
        element.append(retryButton);
      }
      const closeButton = document.createElement('button');
      closeButton.type = 'button';
      closeButton.dataset.variant = 'quiet';
      closeButton.setAttribute('aria-label', '关闭提示');
      closeButton.textContent = '关闭';
      closeButton.addEventListener('click', clear);
      element.append(closeButton);
      element.hidden = false;
    },
    clear,
    dispose() { element.remove(); },
  };
}
