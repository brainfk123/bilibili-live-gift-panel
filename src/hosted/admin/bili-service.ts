import type { HostedAPI } from '../api';
import type { HostedView } from '../shell';
import { mountVerificationCode, type VerificationCodeControl } from '../verification-code';
import { createBiliServiceController, type BiliServiceSnapshot } from './bili-service-controller';
import { runAdminAction } from './ui/async-action';
import { mountAdminNotice } from './ui/notice';

type BiliServiceAPI = Pick<HostedAPI, 'biliServiceStatus' | 'checkBiliService' | 'beginBiliServiceChallenge' | 'replaceBiliServiceCredential' | 'authorizeAdminOperation'>;

const statusText = (status: BiliServiceSnapshot['status']): string => {
  if (!status) return '正在加载服务账号状态…';
  if (status.health === 'healthy') return `运行正常 · UID ${status.maskedUid ?? '已隐藏'} · 最近验证 ${new Date(status.lastVerifiedAt).toLocaleString()}`;
  return status.health === 'missing' ? '尚未配置 B站服务账号' : '服务账号暂不可用';
};

export function mountBiliServiceView(host: HTMLElement, api: BiliServiceAPI): HostedView {
  const document = host.ownerDocument;
  let disposed = false;
  let code: VerificationCodeControl | undefined;
  const card = document.createElement('section'); card.className = 'hosted-admin-card hosted-admin-bili-service';
  const title = document.createElement('h3'); title.textContent = 'B站服务账号';
  const status = document.createElement('p'); status.className = 'hosted-admin-bili-status';
  const actionRow = document.createElement('div'); actionRow.className = 'hosted-admin-bili-actions';
  const check = document.createElement('button'); check.type = 'button'; check.dataset.variant = 'secondary'; check.textContent = '立即检查';
  const begin = document.createElement('button'); begin.type = 'button'; begin.dataset.variant = 'primary'; begin.textContent = '更换服务账号';
  actionRow.append(check, begin);
  const noticeHost = document.createElement('div'); const notice = mountAdminNotice(noticeHost);
  const flowHost = document.createElement('div');
  card.append(title, status, actionRow, noticeHost, flowHost); host.append(card);
  let controller!: ReturnType<typeof createBiliServiceController>;

  const renderFlow = (snapshot: BiliServiceSnapshot): void => {
    code?.dispose(); code = undefined; flowHost.replaceChildren();
    if (!snapshot.challenge) return;
    const flow = document.createElement('section'); flow.className = 'hosted-admin-bili-flow';
    const intro = document.createElement('div'); intro.className = 'hosted-admin-bili-step';
    const heading = document.createElement('h4'); heading.textContent = snapshot.phase === 'qr' ? '1. 扫描二维码登录服务账号' : '2. 输入授权码以确认替换';
    intro.append(heading);
    const figure = document.createElement('figure'); figure.className = 'hosted-admin-bili-qr';
    const image = document.createElement('img'); image.src = snapshot.challenge.qrImage; image.alt = 'B站服务账号登录二维码'; image.setAttribute('width', '448'); image.setAttribute('height', '448');
    const caption = document.createElement('figcaption'); caption.textContent = `二维码有效期至 ${new Date(snapshot.challenge.expiresAt).toLocaleString()}`;
    figure.append(image, caption);
    const actions = document.createElement('div'); actions.className = 'hosted-admin-bili-flow-actions';
    const continueButton = document.createElement('button'); continueButton.type = 'button'; continueButton.dataset.variant = 'primary'; continueButton.textContent = '二维码确认后继续';
    continueButton.disabled = snapshot.phase !== 'qr';
    continueButton.addEventListener('click', () => controller.enterAuthorization());
    const regenerate = document.createElement('button'); regenerate.type = 'button'; regenerate.dataset.variant = 'secondary'; regenerate.textContent = '重新生成'; regenerate.disabled = snapshot.phase !== 'qr';
    regenerate.addEventListener('click', () => { void runAdminAction(regenerate, { idle: '重新生成', busy: '生成中…' }, () => controller.beginReplacement()); });
    const cancel = document.createElement('button'); cancel.type = 'button'; cancel.dataset.variant = 'quiet'; cancel.textContent = '取消'; cancel.disabled = snapshot.phase !== 'qr'; cancel.addEventListener('click', () => controller.cancelReplacement());
    actions.append(continueButton, regenerate, cancel);
    flow.append(intro, figure, actions);
    if (snapshot.phase === 'authorizing' || snapshot.phase === 'replacing') {
      const totpStep = document.createElement('div'); totpStep.className = 'hosted-admin-bili-step hosted-admin-bili-totp';
      const totpTitle = document.createElement('h4'); totpTitle.textContent = snapshot.phase === 'replacing' ? '正在替换服务账号…' : '输入 6 位 TOTP 以授权替换'; totpStep.append(totpTitle);
      if (snapshot.phase === 'authorizing') { code = mountVerificationCode(totpStep, { label: '输入 6 位 TOTP 以授权替换', onComplete: (totp) => { void controller.authorizeAndReplace(totp); } }); code.focus(); }
      flow.append(totpStep);
    }
    flowHost.append(flow);
  };

  const render = (snapshot: BiliServiceSnapshot): void => {
    if (disposed) return;
    status.textContent = statusText(snapshot.status);
    const checking = snapshot.phase === 'checking';
    check.disabled = checking;
    if (checking) {
      check.setAttribute('aria-busy', 'true');
      check.textContent = '检查中…';
      const spinner = document.createElement('span'); spinner.className = 'hosted-admin-action-spinner'; spinner.setAttribute('aria-hidden', 'true'); check.append(spinner);
    } else {
      check.removeAttribute('aria-busy');
      check.textContent = '立即检查';
    }
    begin.disabled = snapshot.phase !== 'idle';
    if (snapshot.notice) notice.show(snapshot.notice.kind, snapshot.notice.message, snapshot.notice.kind === 'error' ? () => { void controller.load(); } : undefined); else notice.clear();
    renderFlow(snapshot);
  };

  controller = createBiliServiceController(api, render);
  check.addEventListener('click', () => { void controller.check(); });
  begin.addEventListener('click', () => { void runAdminAction(begin, { idle: '更换服务账号', busy: '创建中…' }, () => controller.beginReplacement()); });
  void controller.load();
  return { async dispose() { disposed = true; controller.dispose(); code?.dispose(); notice.dispose(); flowHost.replaceChildren(); } };
}
