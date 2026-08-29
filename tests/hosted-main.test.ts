import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

const harness = vi.hoisted(() => ({
  connect: vi.fn(),
  replace: vi.fn(),
  dispose: vi.fn(),
  renderShell: vi.fn(),
  createUserShell: vi.fn(),
  parseUserPage: vi.fn(() => 'overview'),
  pageSearch: vi.fn((_search: string, page: string) => `?workspace=${page}`),
  lifecycleDispose: vi.fn(),
  listeners: new Map<string, (event?: { persisted?: boolean }) => void>(),
  listenerOptions: new Map<string, { once?: boolean } | undefined>(),
}));

const registerListener = (
  type: string,
  listener: (event?: { persisted?: boolean }) => void,
  options?: { once?: boolean },
): void => {
  harness.listeners.set(type, listener);
  harness.listenerOptions.set(type, options);
};

const dispatch = (type: string, event?: { persisted?: boolean }): void => {
  const listener = harness.listeners.get(type);
  if (harness.listenerOptions.get(type)?.once) {
    harness.listeners.delete(type);
    harness.listenerOptions.delete(type);
  }
  listener?.(event);
};

vi.mock('../src/hosted/api', () => ({
  HostedAPI: { connect: harness.connect },
}));
vi.mock('../src/hosted/shell', () => ({
  createHostedViewHost: () => ({ replace: harness.replace, dispose: harness.dispose }),
  isAdminEntryHash: () => false,
  renderHostedShell: harness.renderShell,
}));
vi.mock('../src/hosted/runtime', () => ({
  createHostedApplicationLifecycle: () => ({
    active: () => true,
    run: (action: () => void) => action(),
    dispose: harness.lifecycleDispose,
  }),
  createHostedRuntimePresence: vi.fn(),
}));
vi.mock('../src/hosted/admin', () => ({ mountAdminView: vi.fn() }));
vi.mock('../src/hosted/auth', () => ({ mountAuthView: vi.fn() }));
vi.mock('../src/hosted/configuration', () => ({ mountConfigurationView: vi.fn() }));
vi.mock('../src/hosted/invitations', () => ({ mountInvitationView: vi.fn() }));
vi.mock('../src/hosted/migration', () => ({ mountMigrationView: vi.fn() }));
vi.mock('../src/hosted/room', () => ({ mountRoomControls: vi.fn() }));
vi.mock('../src/hosted/user/shell', () => ({
  createHostedUserShell: harness.createUserShell,
  renderHostedUserPageState: vi.fn(),
}));
vi.mock('../src/hosted/user/routes', () => ({
  parseHostedUserPage: harness.parseUserPage,
  hostedUserPageSearch: harness.pageSearch,
}));
vi.mock('../src/hosted/user/settings/migration-center', () => ({
  mountMigrationPrompt: vi.fn(() => ({ dispose() {} })),
  mountMigrationSettingsView: vi.fn(),
}));

class MainRoot {
  textContent = '';
  readonly ownerDocument = { createElement: vi.fn() };
  replaceChildren(): void { this.textContent = ''; }
}

const flushUnhandled = async (): Promise<void> => {
  await Promise.resolve();
  await Promise.resolve();
  await new Promise<void>((resolve) => setTimeout(resolve, 0));
};

function trackedOperation(): {
  promise: Promise<void>;
  reject(reason: Error): void;
  catchCalls(): number;
} {
  let reject!: (reason: Error) => void;
  const promise = new Promise<void>((_resolve, rejectPromise) => { reject = rejectPromise; });
  const originalCatch = promise.catch.bind(promise);
  let calls = 0;
  promise.catch = ((onRejected) => {
    calls += 1;
    return originalCatch(onRejected);
  }) as typeof promise.catch;
  return { promise, reject, catchCalls: () => calls };
}

describe('Hosted production root operations', () => {
  beforeEach(() => {
    vi.resetModules();
    harness.connect.mockReset();
    harness.replace.mockReset();
    harness.dispose.mockReset();
    harness.renderShell.mockReset();
    harness.createUserShell.mockReset();
    harness.parseUserPage.mockReset().mockReturnValue('overview');
    harness.pageSearch.mockReset().mockImplementation((_search: string, page: string) => `?workspace=${page}`);
    harness.lifecycleDispose.mockReset();
    harness.listeners.clear();
    harness.listenerOptions.clear();
  });

  it('mounts the six-workspace account shell from URL state and publishes page navigation', async () => {
    const root = new MainRoot();
    const api = { session: vi.fn(async () => ({ accountScope: 'account-scope-7' })), logout: vi.fn() };
    const history = { pushState: vi.fn() };
    harness.connect.mockResolvedValue(api);
    harness.replace.mockImplementation(async (mount: () => unknown) => { mount(); });
    harness.dispose.mockResolvedValue(undefined);
    harness.createUserShell.mockReturnValue({ dispose: vi.fn(), announce: vi.fn() });
    harness.parseUserPage.mockReturnValue('obs');
    vi.stubGlobal('HTMLElement', MainRoot);
    vi.stubGlobal('document', { getElementById: () => root });
    vi.stubGlobal('window', {
      location: { hash: '', search: '?workspace=obs', pathname: '/hosted' },
      history,
      addEventListener: registerListener,
      setTimeout,
      clearTimeout,
    });

    await import('../src/hosted/main');
    await vi.waitFor(() => expect(harness.createUserShell).toHaveBeenCalledTimes(1));
    const options = harness.createUserShell.mock.calls[0]![1] as {
      initialPage: string;
      experience: string;
      configurationId: string;
      onPageChange(page: string): void;
    };
    expect(options).toMatchObject({ initialPage: 'obs', experience: 'simple', configurationId: 'account-scope-7' });
    options.onPageChange('data');
    expect(harness.pageSearch).toHaveBeenCalledWith('?workspace=obs', 'data');
    expect(history.pushState).toHaveBeenCalledWith(null, '', '/hosted?workspace=data');

    dispatch('popstate');
    await vi.waitFor(() => expect(harness.createUserShell).toHaveBeenCalledTimes(2));
    expect(api.session).toHaveBeenCalledTimes(2);
  });

  afterEach(() => {
    vi.unstubAllGlobals();
    vi.restoreAllMocks();
  });

  it('handles rejected navigation and pagehide cleanup without unhandled rejection or disclosure', async () => {
    const navigationFailure = new Error('RAW MAIN NAVIGATION private-challenge');
    const pagehideFailure = new Error('RAW MAIN PAGEHIDE private-challenge');
    const navigation = trackedOperation();
    const pagehide = trackedOperation();
    const root = new MainRoot();
    const api = { session: vi.fn(async () => undefined) };
    harness.connect.mockResolvedValue(api);
    harness.replace.mockReturnValue(navigation.promise);
    harness.dispose.mockReturnValue(pagehide.promise);
    const consoleError = vi.spyOn(console, 'error').mockImplementation(() => undefined);
    const consoleWarn = vi.spyOn(console, 'warn').mockImplementation(() => undefined);
    vi.stubGlobal('HTMLElement', MainRoot);
    vi.stubGlobal('document', { getElementById: () => root });
    vi.stubGlobal('window', {
      location: { hash: '' },
      addEventListener: registerListener,
      setTimeout,
      clearTimeout,
    });

    await import('../src/hosted/main');
    await vi.waitFor(() => expect(harness.replace).toHaveBeenCalledTimes(1));
    dispatch('pagehide', { persisted: false });
    await vi.waitFor(() => expect(harness.dispose).toHaveBeenCalledTimes(1));

    expect([navigation.catchCalls(), pagehide.catchCalls()]).toEqual([1, 1]);
    navigation.reject(navigationFailure);
    pagehide.reject(pagehideFailure);
    await flushUnhandled();

    expect(harness.replace).toHaveBeenCalledTimes(1);
    expect(harness.dispose).toHaveBeenCalledTimes(1);
    expect(harness.lifecycleDispose).toHaveBeenCalledTimes(1);
    expect(consoleError).not.toHaveBeenCalled();
    expect(consoleWarn).not.toHaveBeenCalled();
    expect(JSON.stringify(root)).not.toContain(navigationFailure.message);
    expect(JSON.stringify(root)).not.toContain(pagehideFailure.message);
  });

  it('keeps the active login view through cached navigation and cleans up on permanent departure', async () => {
    const root = new MainRoot();
    const api = { session: vi.fn(async () => undefined) };
    harness.connect.mockResolvedValue(api);
    harness.replace.mockResolvedValue(undefined);
    harness.dispose.mockResolvedValue(undefined);
    vi.stubGlobal('HTMLElement', MainRoot);
    vi.stubGlobal('document', { getElementById: () => root });
    vi.stubGlobal('window', {
      location: { hash: '' },
      addEventListener: registerListener,
      setTimeout,
      clearTimeout,
    });

    await import('../src/hosted/main');
    await vi.waitFor(() => expect(harness.replace).toHaveBeenCalledTimes(1));
    dispatch('pagehide', { persisted: true });

    expect(harness.dispose).not.toHaveBeenCalled();
    expect(harness.lifecycleDispose).not.toHaveBeenCalled();

    dispatch('pagehide', { persisted: false });

    expect(harness.dispose).toHaveBeenCalledTimes(1);
    expect(harness.lifecycleDispose).toHaveBeenCalledTimes(1);
  });
});
