import './shell.css';
import { HostedAPI, type HostedConfiguration, type HostedConfigurationDefinition } from './api';
import { mountAdminView } from './admin';
import { mountAuthView } from './auth';
import { mountConfigurationView } from './configuration';
import { mountInvitationView } from './invitations';
import { mountRoomControls } from './room';
import { createHostedApplicationLifecycle, createHostedRuntimePresence, type HostedRuntimePresence } from './runtime';
import { createHostedViewHost, isAdminEntryHash, renderHostedShell, type HostedSession, type HostedView } from './shell';
import { hostedUserPageSearch, parseHostedUserPage, type HostedUserPage } from './user/routes';
import { createHostedUserShell, renderHostedUserPageState, type HostedUserExperience } from './user/shell';
import { mountMigrationPrompt, mountMigrationSettingsView } from './user/settings/migration-center';

const root = document.getElementById('hosted-app');

if (!(root instanceof HTMLElement)) {
  throw new Error('Hosted application root is missing.');
}

const viewHost = createHostedViewHost();
const applicationLifecycle = createHostedApplicationLifecycle();
const observeViewOperation = (operation: Promise<void>): void => { void operation.catch(() => undefined); };
let runtimePresence: HostedRuntimePresence | undefined;
let activeAccountShell: ReturnType<typeof createHostedUserShell> | undefined;
let authenticatedAccountScope: string | undefined;
let sessionRequestGeneration = 0;
const ensureRuntimePresence = (): HostedRuntimePresence => {
  runtimePresence ??= createHostedRuntimePresence({
    createEventSource: (path) => new EventSource(path),
    setTimer: (callback, delay) => window.setTimeout(callback, delay),
    clearTimer: (timer) => window.clearTimeout(timer as number),
    random: Math.random,
  });
  return runtimePresence;
};
const disposeRuntimePresence = (): void => { runtimePresence?.dispose(); runtimePresence = undefined; };
const configurationExperience = (definition: HostedConfigurationDefinition): HostedUserExperience => {
  const settings = definition.generalSettings;
  return settings && typeof settings === 'object' && !Array.isArray(settings)
    && (settings as { configurationMode?: unknown }).configurationMode === 'advanced' ? 'advanced' : 'simple';
};
const definitionWithExperience = (definition: HostedConfigurationDefinition, experience: HostedUserExperience): HostedConfigurationDefinition => ({
  ...structuredClone(definition),
  generalSettings: { configurationMode: experience },
});
const workspaceCard = (host: HTMLElement, titleText: string, descriptionText: string, actionText: string, action: () => void): HostedView => {
  const document = host.ownerDocument;
  const card = document.createElement('section'); card.className = 'hosted-user-card';
  const title = document.createElement('h2'); title.textContent = titleText;
  const description = document.createElement('p'); description.textContent = descriptionText;
  const button = document.createElement('button'); button.type = 'button'; button.textContent = actionText; button.addEventListener('click', action);
  card.append(title, description, button); host.replaceChildren(card);
  return { dispose: () => { host.replaceChildren(); } };
};
const mountAccountWorkspace = (page: HostedUserPage, host: HTMLElement, api: HostedAPI, accountScope: string): HostedView => {
  if (page === 'overview') {
    const document = host.ownerDocument;
    const prompt = document.createElement('div');
    let storage: Storage | undefined;
    try { storage = document.defaultView?.localStorage; } catch { storage = undefined; }
    const promptView = mountMigrationPrompt(prompt, storage, accountScope, { onOpen: () => showSettings(api, accountScope) });
    const room = document.createElement('section'); room.className = 'hosted-user-card hosted-panel';
    const roomView = mountRoomControls(room, api, ensureRuntimePresence());
    host.append(prompt, room);
    return { dispose: () => { promptView.dispose(); roomView.dispose(); host.replaceChildren(); } };
  }
  if (page === 'attributes') {
    return workspaceCard(host, '属性玩法', '当前可进入在线配置；与 EXE 一致的属性编辑将在后续阶段接入。', '打开在线配置', () => showConfiguration(api, accountScope));
  }
  const copy: Record<Exclude<HostedUserPage, 'overview' | 'attributes'>, readonly [string, string]> = {
    activities: ['暂无活动会话', '后续阶段会在这里接入活动运行状态与阶段结果。'],
    targets: ['暂无礼物目标', '后续阶段会在这里接入目标面板与完成进度。'],
    obs: ['OBS 面板尚未接入', '后续阶段会在这里管理在线输出定义与浏览器源。'],
    data: ['数据中心尚未接入', '后续阶段会在这里展示场次、趋势和观众贡献。'],
  };
  const [title, description] = copy[page];
  renderHostedUserPageState(host, { kind: 'empty', title, description });
  return { dispose: () => { host.replaceChildren(); } };
};
const mountAccountView = (api: HostedAPI, accountScope: string, initialPage: HostedUserPage): HostedView => {
  let shell!: ReturnType<typeof createHostedUserShell>;
  let disposed = false;
  let authoritative: HostedConfiguration | undefined;
  let desiredExperience: HostedUserExperience = 'simple';
  const persistExperience = (experience: HostedUserExperience): void => {
    desiredExperience = experience;
    shell.setExperiencePending(true);
    const operation = (async () => {
      try {
        const current = authoritative ?? await api.loadConfiguration();
        const definition = definitionWithExperience(current.definition, experience);
        const saved = await api.saveConfigurationDefinition(current.version, definition);
        authoritative = { ...current, definition, version: saved.version, revision: saved.revision };
      } catch {
        try {
          const current = await api.loadConfiguration();
          authoritative = current;
          if (!disposed && desiredExperience === experience) shell.syncExperience(configurationExperience(current.definition));
        } catch { /* Keep the explicit failure copy without disclosing a raw response. */ }
        if (!disposed && desiredExperience === experience) shell.announce('配置模式保存失败，请重试');
      } finally {
        if (!disposed && desiredExperience === experience) shell.setExperiencePending(false);
      }
    })();
    observeViewOperation(operation);
  };
  shell = createHostedUserShell(root, {
    initialPage,
    experience: 'simple',
    experiencePending: true,
    configurationId: accountScope,
    onPageChange: (page) => {
      const search = hostedUserPageSearch(window.location.search, page);
      window.history.pushState(null, '', `${window.location.pathname}${search}${window.location.hash}`);
    },
    onExperienceChange: persistExperience,
    onSettings: () => showSettings(api, accountScope),
    onInvitations: () => { observeViewOperation(viewHost.replace(() => mountInvitationView(root, api, undefined, () => returnToAccount(api), () => returnToSignedOut(api)))); },
    onLogout: () => { void api.logout().then(() => returnToSignedOut(api)).catch(() => applicationLifecycle.run(() => { shell.announce('退出失败，请稍后重试'); })); },
    mount: (page, host) => mountAccountWorkspace(page, host, api, accountScope),
  });
  activeAccountShell = shell;
  const loadExperience = api.loadConfiguration().then((current) => {
    authoritative = current;
    desiredExperience = configurationExperience(current.definition);
    if (!disposed) shell.syncExperience(desiredExperience);
  }).catch(() => {
    if (!disposed) shell.announce('配置模式读取失败，暂以简单模式显示');
  }).finally(() => {
    if (!disposed) shell.setExperiencePending(false);
  });
  observeViewOperation(loadExperience);
  return { async dispose(): Promise<void> {
    disposed = true;
    if (activeAccountShell === shell) activeAccountShell = undefined;
    await shell.dispose();
  } };
};
const showAccount = (api: HostedAPI, accountScope: string): void => {
  authenticatedAccountScope = accountScope;
  observeViewOperation(viewHost.replace(() => mountAccountView(api, accountScope, parseHostedUserPage(window.location.search))));
};
const showConfiguration = (api: HostedAPI, accountScope: string): void => { observeViewOperation(viewHost.replace(() => mountConfigurationView(root, api, { onMigration: () => showSettings(api, accountScope), onExit: () => showAccount(api, accountScope) }))); };
const showSettings = (api: HostedAPI, accountScope: string): void => { observeViewOperation(viewHost.replace(() => mountMigrationSettingsView(root, api, { onExit: () => showAccount(api, accountScope) }))); };
const mountShell = (api: HostedAPI, serviceStatus: HostedSession['serviceStatus']): HostedView => {
  renderHostedShell(root, {
    serviceStatus,
    onLogin: () => { observeViewOperation(viewHost.replace(() => mountAuthView(root, api, {
        onSignedIn: () => returnToAccount(api),
        onRegistrationRequired: (intent) => { applicationLifecycle.run(() => { observeViewOperation(viewHost.replace(() => mountInvitationView(root, api, intent, () => returnToAccount(api)))); }); },
        onExit: () => returnToSignedOut(api),
      }))); },
  });
  return { dispose: () => { root.replaceChildren(); } };
};
const showAdmin = (api: HostedAPI): void => { disposeRuntimePresence(); observeViewOperation(viewHost.replace(() => mountAdminView(root, api))); };
const showSignedOut = (api: HostedAPI, serviceStatus: HostedSession['serviceStatus']): void => {
  if (isAdminEntryHash(window.location.hash)) showAdmin(api);
  else showShell(api, serviceStatus);
};
const showShell = (api: HostedAPI, serviceStatus: HostedSession['serviceStatus']): void => { disposeRuntimePresence(); observeViewOperation(viewHost.replace(() => mountShell(api, serviceStatus))); };
const returnToAccount = (api: HostedAPI): void => {
  const requested = ++sessionRequestGeneration;
  void api.session().then(({ accountScope }) => {
    if (requested === sessionRequestGeneration) applicationLifecycle.run(() => showAccount(api, accountScope));
  }).catch(() => {
    if (requested === sessionRequestGeneration) returnToSignedOut(api);
  });
};
const returnToSignedOut = (api: HostedAPI): void => {
  sessionRequestGeneration += 1;
  authenticatedAccountScope = undefined;
  applicationLifecycle.run(() => showSignedOut(api, 'ready'));
};

renderHostedShell(root, { serviceStatus: 'checking', onLogin: () => undefined });
void HostedAPI.connect().then(async (api) => {
  if (!applicationLifecycle.active()) return;
  window.addEventListener('hashchange', () => {
    if (!applicationLifecycle.active()) return;
    if (isAdminEntryHash(window.location.hash)) applicationLifecycle.run(() => showAdmin(api));
    else if (authenticatedAccountScope) applicationLifecycle.run(() => showAccount(api, authenticatedAccountScope!));
    else applicationLifecycle.run(() => showShell(api, 'ready'));
  });
  window.addEventListener('popstate', () => {
    if (!applicationLifecycle.active()) return;
    if (isAdminEntryHash(window.location.hash)) applicationLifecycle.run(() => showAdmin(api));
    else if (activeAccountShell) observeViewOperation(activeAccountShell.syncPage(parseHostedUserPage(window.location.search)));
    else if (authenticatedAccountScope) applicationLifecycle.run(() => showAccount(api, authenticatedAccountScope!));
    else returnToAccount(api);
  });
  const requested = ++sessionRequestGeneration;
  try {
    const { accountScope } = await api.session();
    if (requested === sessionRequestGeneration) applicationLifecycle.run(() => showAccount(api, accountScope));
  } catch {
    if (requested === sessionRequestGeneration) applicationLifecycle.run(() => showSignedOut(api, 'ready'));
  }
}).catch(() => {
  applicationLifecycle.run(() => renderHostedShell(root, { serviceStatus: 'unavailable', onLogin: () => undefined }));
});

window.addEventListener('pagehide', (event) => {
  if (event.persisted) return;
  applicationLifecycle.dispose();
  disposeRuntimePresence();
  observeViewOperation(viewHost.dispose());
});
