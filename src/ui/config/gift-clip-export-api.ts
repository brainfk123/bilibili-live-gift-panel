import type { GiftClipCrop } from '../../types';

export type GiftClipJobState = 'queued' | 'encoding' | 'retrying' | 'ready' | 'failed' | 'cancelled';

export interface GiftClipJobSnapshot {
  id: string;
  state: GiftClipJobState;
  progress: number;
  message?: string;
  output: { width: number; height: number; fps: 30 };
}

export interface GiftClipCreateInput {
  receiptId: string;
  crop: GiftClipCrop;
  background: Blob;
  overlay: Blob;
}

export interface GiftClipJobWaitOptions {
  signal?: AbortSignal;
  intervalMs?: number;
  sleep?: (milliseconds: number, signal?: AbortSignal) => Promise<void>;
  onSnapshot?: (snapshot: GiftClipJobSnapshot) => void;
  maxAttempts?: number;
}

const jobStates = new Set<GiftClipJobState>(['queued', 'encoding', 'retrying', 'ready', 'failed', 'cancelled']);
const jobIDPattern = /^[A-Za-z0-9_-]{24}$/;
const compatibilityMessage = '已切换兼容编码模式。';
const invalidResponseMessage = 'Invalid gift clip job response.';

export async function createGiftClipJob(input: GiftClipCreateInput, signal?: AbortSignal): Promise<GiftClipJobSnapshot> {
  const form = new FormData();
  form.set('metadata', JSON.stringify({ receiptId: input.receiptId, crop: input.crop, version: 1 }));
  form.set('background', input.background, 'background.png');
  form.set('overlay', input.overlay, 'overlay.png');
  return requestGiftClipJob('/api/gift-clips', { method: 'POST', body: form, signal }, 202, signal);
}

export function getGiftClipJob(id: string, signal?: AbortSignal): Promise<GiftClipJobSnapshot> {
  assertGiftClipJobID(id);
  return requestGiftClipJob(`/api/gift-clips/${encodeURIComponent(id)}`, { cache: 'no-store', signal }, 200, signal);
}

export async function cancelGiftClipJob(id: string): Promise<void> {
  assertGiftClipJobID(id);
  let response: Response;
  try {
    response = await fetch(`/api/gift-clips/${encodeURIComponent(id)}`, { method: 'DELETE' });
  } catch {
    throw new Error('Unable to cancel gift clip job.');
  }
  if (!response.ok && response.status !== 404 && response.status !== 410) {
    throw new Error('Unable to cancel gift clip job.');
  }
}

export function giftClipJobVideoURL(id: string): string {
  assertGiftClipJobID(id);
  return `/api/gift-clips/${encodeURIComponent(id)}/video`;
}

export async function waitForGiftClipJob(id: string, options: GiftClipJobWaitOptions = {}): Promise<GiftClipJobSnapshot> {
  assertGiftClipJobID(id);
  const intervalMs = options.intervalMs ?? 250;
  const sleep = options.sleep ?? defaultSleep;
  const maxAttempts = options.maxAttempts ?? 120;
  if (!Number.isSafeInteger(intervalMs) || intervalMs < 0 || !Number.isSafeInteger(maxAttempts) || maxAttempts < 1) {
    throw new Error('Invalid gift clip polling options.');
  }

  let attemptProgress = 0;
  for (let attempt = 0; attempt < maxAttempts; attempt += 1) {
    throwIfAborted(options.signal);
    const received = await getGiftClipJob(id, options.signal);
    throwIfAborted(options.signal);
    const snapshot = received.state === 'retrying'
      ? { ...received, progress: 0, message: compatibilityMessage }
      : received;
    if (snapshot.state === 'retrying') {
      attemptProgress = 0;
    } else if (snapshot.state === 'failed' || snapshot.state === 'cancelled') {
      options.onSnapshot?.(snapshot);
      throw new Error(snapshot.message ?? 'Gift clip export did not complete.');
    } else if (snapshot.progress < attemptProgress) {
      throw new Error('Gift clip job progress regressed.');
    } else {
      attemptProgress = snapshot.progress;
    }
    options.onSnapshot?.(snapshot);

    if (snapshot.state === 'ready') return snapshot;
    if (attempt + 1 === maxAttempts) break;
    throwIfAborted(options.signal);
    await sleep(intervalMs, options.signal);
    throwIfAborted(options.signal);
  }
  throw new Error('Gift clip job polling timed out.');
}

async function requestGiftClipJob(url: string, init: RequestInit, expectedStatus: number, signal?: AbortSignal): Promise<GiftClipJobSnapshot> {
  let response: Response;
  try {
    throwIfAborted(signal);
    response = await fetch(url, init);
  } catch (error) {
    if (signal?.aborted || isAbortError(error)) throw error;
    throw new Error(invalidResponseMessage);
  }
  throwIfAborted(signal);
  if (response.status !== expectedStatus || !isJSONContentType(response.headers.get('content-type'))) {
    throw new Error(invalidResponseMessage);
  }
  let payload: unknown;
  try {
    payload = await response.json();
  } catch {
    throw new Error(invalidResponseMessage);
  }
  return parseGiftClipJobSnapshot(payload);
}

function parseGiftClipJobSnapshot(value: unknown): GiftClipJobSnapshot {
  if (!value || typeof value !== 'object' || Object.getPrototypeOf(value) !== Object.prototype) {
    throw new Error(invalidResponseMessage);
  }
  const nestedOutput = Object.prototype.hasOwnProperty.call(value, 'output');
  const fields = nestedOutput
    ? ['id', 'state', 'progress', 'output', 'message']
    : ['id', 'state', 'progress', 'width', 'height', 'fps', 'message'];
  if (!isExactPlainObject(value, fields)) throw new Error(invalidResponseMessage);
  const { id, state, progress, message } = value;
  if (!isGiftClipJobID(id) || typeof state !== 'string' || !jobStates.has(state as GiftClipJobState)
    || typeof progress !== 'number' || !Number.isFinite(progress) || progress < 0 || progress > 1
    || (message !== undefined && typeof message !== 'string')) {
    throw new Error(invalidResponseMessage);
  }
  const output = nestedOutput ? value.output : value;
  if (!isExactPlainObject(output, nestedOutput ? ['width', 'height', 'fps'] : fields)) throw new Error(invalidResponseMessage);
  const { width, height, fps } = output;
  if (!validOutputDimension(width) || !validOutputDimension(height) || fps !== 30) throw new Error(invalidResponseMessage);
  return message === undefined
    ? { id, state: state as GiftClipJobState, progress, output: { width, height, fps: 30 } }
    : { id, state: state as GiftClipJobState, progress, message, output: { width, height, fps: 30 } };
}

function isExactPlainObject(value: unknown, allowedKeys: readonly string[]): value is Record<string, unknown> {
  if (!value || typeof value !== 'object' || Object.getPrototypeOf(value) !== Object.prototype) return false;
  const keys = Object.keys(value);
  return keys.every((key) => allowedKeys.includes(key)) && keys.every((key) => Object.prototype.propertyIsEnumerable.call(value, key));
}

function validOutputDimension(value: unknown): value is number {
  return typeof value === 'number' && Number.isSafeInteger(value) && value >= 64 && value <= 4096 && value % 2 === 0;
}

function isGiftClipJobID(value: unknown): value is string {
  return typeof value === 'string' && jobIDPattern.test(value);
}

function assertGiftClipJobID(id: string): void {
  if (!isGiftClipJobID(id)) throw new Error('Invalid gift clip job ID.');
}

function isJSONContentType(value: string | null): boolean {
  return value !== null && /^application\/json(?:\s*;|\s*$)/i.test(value);
}

function isAbortError(error: unknown): boolean {
  return error instanceof DOMException && error.name === 'AbortError';
}

function throwIfAborted(signal?: AbortSignal): void {
  if (signal?.aborted) throw signal.reason ?? new DOMException('The operation was aborted.', 'AbortError');
}

function defaultSleep(milliseconds: number, signal?: AbortSignal): Promise<void> {
  return new Promise((resolve, reject) => {
    if (signal?.aborted) {
      reject(signal.reason ?? new DOMException('The operation was aborted.', 'AbortError'));
      return;
    }
    const timer = setTimeout(resolve, milliseconds);
    signal?.addEventListener('abort', () => {
      clearTimeout(timer);
      reject(signal.reason ?? new DOMException('The operation was aborted.', 'AbortError'));
    }, { once: true });
  });
}
