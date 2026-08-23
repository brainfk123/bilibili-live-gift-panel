import { HostedAPIError } from '../api';

export type AdminEmailFailureState =
  | { kind: 'rate-limited' }
  | { kind: 'email-error'; reason: 'email-unavailable' | 'invalid-or-expired-code' };

export function adminEmailFailure(error: unknown): AdminEmailFailureState {
  if (error instanceof HostedAPIError && error.status === 429) return { kind: 'rate-limited' };
  if (error instanceof HostedAPIError && error.status === 401) return { kind: 'email-error', reason: 'invalid-or-expired-code' };
  return { kind: 'email-error', reason: 'email-unavailable' };
}

