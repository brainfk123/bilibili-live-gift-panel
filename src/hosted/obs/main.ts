import './obs.css';
import { parseOBSDisplaySnapshot, renderOBSSnapshot } from './view';

const publicIDPattern = /^[A-Za-z0-9_-]{43}$/;
type OBSFetcher = (input: RequestInfo | URL, init?: RequestInit) => Promise<Response>;

/** Minimal OBS-only exchange boundary; the complete hosted API is not loaded. */
export async function postOBSToken(publicID: string, token: string, fetcher: OBSFetcher = globalThis.fetch.bind(globalThis)): Promise<void> {
  if (!publicIDPattern.test(publicID) || token.length === 0 || token.length > 512) throw new Error('invalid_obs_credential');
  const response = await fetcher(`/obs/${publicID}/exchange`, {
    method: 'POST', credentials: 'same-origin', headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ token }),
  });
  if (response.status !== 204) throw new Error('obs_exchange_failed');
}

export interface OBSLocation {
  pathname: string;
  search: string;
  hash: string;
}

export interface ParsedOBSLocation {
  publicID: string;
  token?: string;
  cleanURL: string;
}

export interface OBSEventSourceLike {
  close(): void;
  addEventListener?(type: string, listener: (event: MessageEvent<string>) => void): void;
  onerror?: ((event: Event) => void) | null;
}

export interface OBSPageDependencies {
  location: OBSLocation;
  replaceState(state: unknown, title: string, url: string): void;
  exchange(publicID: string, token: string): Promise<void>;
  createEventSource(path: string): OBSEventSourceLike;
  onSnapshot(snapshot: ReturnType<typeof parseOBSDisplaySnapshot>): void;
  onError(): void;
}

export function parseOBSLocation(location: OBSLocation): ParsedOBSLocation | undefined {
  const match = /^\/obs\/([A-Za-z0-9_-]{43})\/?$/.exec(location.pathname);
  if (!match || !publicIDPattern.test(match[1] ?? '')) return undefined;
  if (location.hash === '') return { publicID: match[1]!, cleanURL: `${location.pathname}${location.search}` };
  const parameters = new URLSearchParams(location.hash.slice(1));
  if ([...parameters.keys()].length !== 1 || !parameters.has('token')) return undefined;
  const token = parameters.get('token');
  if (!token || token.length > 512) return undefined;
  return { publicID: match[1]!, token, cleanURL: `${location.pathname}${location.search}` };
}

export async function startOBSPage(dependencies: OBSPageDependencies): Promise<OBSEventSourceLike> {
	const capturedLocation: OBSLocation = {
	  pathname: dependencies.location.pathname,
	  search: dependencies.location.search,
	  hash: dependencies.location.hash,
	};
	const cleanURL = `${capturedLocation.pathname}${capturedLocation.search}`;
	if (capturedLocation.hash !== '') dependencies.replaceState(null, '', cleanURL);
  const parsed = parseOBSLocation(capturedLocation);
	capturedLocation.hash = '';
  if (!parsed) {
    dependencies.onError();
    throw new Error('invalid_obs_location');
  }
  let token = parsed.token ?? '';
	parsed.token = undefined;
  if (token) {
    const exchange = dependencies.exchange(parsed.publicID, token);
    try {
      await exchange;
    } catch (error) {
      dependencies.onError();
      throw error;
    } finally {
      token = '';
    }
  }
  const source = dependencies.createEventSource(`/obs/${parsed.publicID}/events`);
  source.addEventListener?.('display', (event) => {
    try {
      const snapshot = parseOBSDisplaySnapshot(JSON.parse(event.data));
      if (snapshot) dependencies.onSnapshot(snapshot);
      else dependencies.onError();
    } catch {
      dependencies.onError();
    }
  });
  source.onerror = () => dependencies.onError();
  return source;
}

function mountBrowserOBS(): void {
  const root = document.getElementById('hosted-obs-app');
  if (!(root instanceof HTMLElement)) throw new Error('Hosted OBS root is missing.');
  const showError = (): void => {
    const message = root.ownerDocument.createElement('p');
    message.className = 'hosted-obs-error';
    message.textContent = 'OBS 展示连接失败，请重新复制展示地址。';
    root.replaceChildren(message);
  };
  void startOBSPage({
    location: window.location,
    replaceState: history.replaceState.bind(history),
    exchange: postOBSToken,
    createEventSource: (path) => new EventSource(path),
    onSnapshot: (snapshot) => {
      if (snapshot) renderOBSSnapshot(root, snapshot, { theme: new URLSearchParams(window.location.search).get('theme') });
    },
    onError: showError,
  }).then((source) => {
    window.addEventListener('pagehide', () => source.close(), { once: true });
  }).catch(() => undefined);
}

if (typeof document !== 'undefined' && typeof window !== 'undefined') mountBrowserOBS();
