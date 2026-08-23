import { HostedAPIError, type EmailLoginChallenge } from '../api';
import { adminEmailFailure } from './request-feedback';

export type AdminLoginState =
  | { kind: 'checking-session' }
  | { kind: 'restore-timeout' }
  | { kind: 'ready' }
  | { kind: 'requesting-email' }
  | { kind: 'awaiting-email-code' }
  | { kind: 'verifying-email' }
  | { kind: 'network-error' }
  | { kind: 'rate-limited' }
  | { kind: 'email-error'; reason: 'email-unavailable' | 'invalid-or-expired-code' }
  | { kind: 'signed-in' }
  | { kind: 'disposed' };

export interface AdminLoginAPI {
  adminSession(): Promise<void>;
  beginAdminEmailLogin(): Promise<EmailLoginChallenge>;
  adminEmailLogin(challengeId: string, emailCode: string): Promise<void>;
  adminLogout(): Promise<void>;
}

interface AdminLoginTimerPort {
  setTimeout(callback: () => void, milliseconds: number): unknown;
  clearTimeout(timer: unknown): void;
}

const rateLimitCooldownMilliseconds = 60_000;
const restoreTimeoutMilliseconds = 3_000;
const defaultTimers: AdminLoginTimerPort = {
  setTimeout: (callback, milliseconds) => globalThis.setTimeout(callback, milliseconds),
  clearTimeout: (timer) => globalThis.clearTimeout(timer as ReturnType<typeof globalThis.setTimeout>),
};

export function createAdminLoginController(api: AdminLoginAPI, render: (state: AdminLoginState) => void, timers: AdminLoginTimerPort = defaultTimers) {
  let emailChallenge: EmailLoginChallenge | undefined;
  let state: AdminLoginState = { kind: 'checking-session' };
  let disposed = false;
  let generation = 0;
  let startOperation: Promise<void> | undefined;
  let emailOperation: Promise<void> | undefined;
  const submitOperations = new Map<string, Promise<void>>();
  let cooldownTimer: unknown;
  let restoreTimer: unknown;
  const publish = (next: AdminLoginState): void => { state = next; if (!disposed) render(next); };
  const unavailable = (): HostedAPIError => new HostedAPIError('operation_failed', 0);
  const eraseChallenge = (): void => { emailChallenge = undefined; };
  const clearCooldown = (): void => { if (cooldownTimer !== undefined) timers.clearTimeout(cooldownTimer); cooldownTimer = undefined; };
  const clearRestoreTimer = (): void => { if (restoreTimer !== undefined) timers.clearTimeout(restoreTimer); restoreTimer = undefined; };
  const publishError = (error: unknown, current: number): void => {
    const next = adminEmailFailure(error); publish(next);
    if (next.kind !== 'rate-limited') return;
    clearCooldown();
    cooldownTimer = timers.setTimeout(() => { cooldownTimer = undefined; if (!disposed && current === generation && state.kind === 'rate-limited') publish({ kind: 'ready' }); }, rateLimitCooldownMilliseconds);
  };
  const start = (): Promise<void> => {
    if (disposed) return Promise.reject(unavailable());
    if (startOperation) return startOperation;
    if (state.kind === 'rate-limited') return Promise.reject(new HostedAPIError('invalid_request', 400));
    const current = ++generation; clearCooldown(); eraseChallenge(); publish({ kind: 'checking-session' }); clearRestoreTimer();
    restoreTimer = timers.setTimeout(() => { restoreTimer = undefined; if (disposed || current !== generation || state.kind !== 'checking-session') return; generation += 1; startOperation = undefined; publish({ kind: 'restore-timeout' }); }, restoreTimeoutMilliseconds);
    const operation = (async () => {
      try { await api.adminSession(); if (!disposed && current === generation) { clearRestoreTimer(); publish({ kind: 'signed-in' }); } }
      catch (error) { if (!disposed && current === generation) { clearRestoreTimer(); publish(error instanceof HostedAPIError && error.status === 401 ? { kind: 'ready' } : { kind: 'email-error', reason: 'email-unavailable' }); } }
    })().finally(() => { if (startOperation === operation) startOperation = undefined; });
    startOperation = operation; return operation;
  };
  const startEmail = (): Promise<void> => {
    if (emailOperation) return emailOperation;
    if (disposed || !['ready', 'restore-timeout', 'awaiting-email-code', 'verifying-email', 'email-error'].includes(state.kind)) return Promise.reject(new HostedAPIError('invalid_request', 400));
    const current = ++generation; eraseChallenge(); publish({ kind: 'requesting-email' });
    const operation = (async () => {
      try { const created = await api.beginAdminEmailLogin(); if (disposed || current !== generation) return; emailChallenge = created; publish({ kind: 'awaiting-email-code' }); }
      catch (error) { if (!disposed && current === generation) publishError(error, current); }
    })().finally(() => { if (emailOperation === operation) emailOperation = undefined; });
    emailOperation = operation; return operation;
  };
  const submitEmailCode = (code: string): Promise<void> => {
    if (disposed || !emailChallenge || !/^[0-9]{6}$/.test(code)) return Promise.reject(new HostedAPIError('invalid_request', 400));
    const id = emailChallenge.challengeId; const current = generation; const owner = `${current}\u0000${id}`;
    const existing = submitOperations.get(owner); if (existing) return existing;
    if (submitOperations.size > 0) return Promise.reject(new HostedAPIError('operation_conflict', 409));
    if (state.kind !== 'awaiting-email-code' && state.kind !== 'network-error') return Promise.reject(new HostedAPIError('invalid_request', 400));
    const ownsChallenge = (): boolean => current === generation && emailChallenge?.challengeId === id;
    publish({ kind: 'verifying-email' });
    const operation = (async () => {
      try { await api.adminEmailLogin(id, code); }
      catch (error) { if (!disposed && ownsChallenge()) { if (!(error instanceof HostedAPIError) || error.status === 0) publish({ kind: 'network-error' }); else { eraseChallenge(); publishError(error, current); } } throw error; }
      if (disposed || !ownsChallenge()) { await api.adminLogout(); return; }
      eraseChallenge(); publish({ kind: 'signed-in' });
    })().finally(() => { if (submitOperations.get(owner) === operation) submitOperations.delete(owner); });
    submitOperations.set(owner, operation); return operation;
  };
  return Object.freeze({ start, startEmail, submitEmailCode, state: (): AdminLoginState => ({ ...state }), async dispose(): Promise<void> { if (disposed) return; disposed = true; generation += 1; clearCooldown(); clearRestoreTimer(); eraseChallenge(); state = { kind: 'disposed' }; await Promise.allSettled([...[emailOperation].filter((value): value is Promise<void> => Boolean(value)), ...submitOperations.values()]); } });
}

export const createAdminLoginFlow = createAdminLoginController;
