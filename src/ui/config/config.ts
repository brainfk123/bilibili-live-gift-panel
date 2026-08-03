import { AppState, Attribute, FormulaPresetContext, GiftInfo, GiftRule, LogEntry, MAX_LOG, TimerRule } from '../../types';
import { consumeConfigMigrationRequired, loadState, refreshStateFromServer, resetState, saveState } from '../../storage';
import { applyFormulaPreset, replaceFormulaVariable, saveFormulaPreset } from '../../formula-presets';
import { el, fieldControl, inputField, toast } from '../common';
import { builtinCatalog, findGift, giftDisplayKey, matchesGiftSearch, sortGiftsByUsage } from '../../gifts/catalog';
import { formatValue } from '../../format';
import {
  BiliAuthStatus,
  checkForUpdates,
  getBlindBoxInfo,
  getBiliAuthStatus,
  getRoomGiftCatalog,
  getRuntimeStatus,
  getUpdateStatus,
  logoutBiliAuth,
  pollBiliQRCodeLogin,
  previewFormula,
  RuntimeConnectionState,
  startBiliQRCodeLogin,
  UpdateStatus,
} from '../../backend';
import { createBrandIcon } from '../brand';
import { getTutorialStep } from './wizard';
import { renderSpotlightGuide, type SpotlightGuideElement } from './spotlight-guide';

interface SelectedGiftRule {
  gift: GiftInfo;
  formulaName: string;
  formula: string;
  enabled: boolean;
  previous?: GiftRule;
  matchGiftIds?: number[];
  blindBoxName?: string;
  blindBoxStatus?: 'matched' | 'login-required' | 'not-blind-box' | 'error';
}

export function mountConfig(root: HTMLElement): void {
  let state = loadState();
  root.classList.add('config-root');
  state.settings.theme = normalizeConfigTheme(state.settings.theme);
  state.settings.giftView = state.settings.giftView === 'grid' ? 'grid' : 'list';
  const metadataChanged = ensureRuleGiftCatalog(state);
  if (metadataChanged || consumeConfigMigrationRequired()) void saveState(state);

  let connectionState: RuntimeConnectionState = 'idle';
  let biliAuth: BiliAuthStatus = { state: 'anonymous' };
  let guideDismissed = !state.settings.showTutorial;
  let activeGuide: SpotlightGuideElement | null = null;
  let editorOpen = false;
  let editorGuideEnabled = false;
  let runtimeRefreshPromise: Promise<void> | null = null;
  let stateRefreshActive = false;
  let authRefreshActive = false;
  let roomGiftCatalogRefreshActive = false;
  let roomGiftCatalogRoomId = '';
  let roomGiftCatalog: GiftInfo[] = [];
  let refreshOpenGiftCatalog: (() => void) | null = null;
  let currentUpdateStatus: UpdateStatus = {
    state: 'idle', currentVersion: '', message: '正在读取版本信息…', autoUpdate: state.settings.autoUpdate, restartRequired: false,
  };
  let updateRefreshActive = false;
  let refreshUpdateCard: (() => void) | null = null;
  let loginModalOpen = false;
  let loginPollTimer: ReturnType<typeof globalThis.setInterval> | undefined;
  let localStateVersion = 0;

  const shell = el('div', { class: 'wizard-shell config-shell' });
  const header = el('header', { class: 'app-header' });
  const brand = el('div', { class: 'app-brand' });
  brand.append(createBrandIcon(40), el('div', { class: 'app-brand-copy' }, [
    el('strong', { text: '直播礼物面板' }),
  ]));
  const themeToggle = el('button', { class: 'theme-toggle', type: 'button' }) as HTMLButtonElement;
  const guideToggle = el('button', { class: 'theme-toggle', type: 'button', text: '显示教程' }) as HTMLButtonElement;
  const status = el('div', { class: 'app-status' });
  const headerActions = el('div', { class: 'app-header-actions' });
  headerActions.append(guideToggle, themeToggle, status);
  header.append(brand, headerActions);

  const content = el('main', { class: 'wizard-content config-page' });
  shell.append(header, content);
  root.replaceChildren(shell);

  function applyConfigTheme(theme: 'dark' | 'light'): void {
    root.dataset.theme = theme;
    const label = theme === 'dark' ? '切换至亮色主题' : '切换至深色主题';
    themeToggle.textContent = label;
    themeToggle.setAttribute('aria-label', label);
  }

  function connectionLabel(value: RuntimeConnectionState): string {
    if (value === 'connected') return '已连接';
    if (value === 'connecting') return '连接中…';
    if (value === 'reconnecting') return '重连中…';
    if (value === 'error') return '连接失败';
    return '未连接';
  }

  function renderHeaderStatus(): void {
    status.className = `app-status is-${connectionState}`;
    status.replaceChildren(
      el('span', { class: 'app-status-dot' }),
      el('span', { text: connectionLabel(connectionState) }),
    );
  }

  async function refreshRuntime(forceAfterCurrent = false): Promise<void> {
    if (runtimeRefreshPromise) {
      await runtimeRefreshPromise;
      if (forceAfterCurrent) await refreshRuntime();
      return;
    }
    const refresh = (async () => {
      try {
        const runtime = await getRuntimeStatus();
        const previous = connectionState;
        connectionState = runtime.state;
        renderHeaderStatus();
        const inlineStatus = root.querySelector('.connection-inline-status');
        if (inlineStatus) inlineStatus.textContent = connectionLabel(connectionState);
        if (previous !== connectionState && connectionState === 'connected') {
          void refreshRoomGiftCatalog(true);
          if (!editorOpen) render();
        }
      } catch {
        connectionState = 'error';
        renderHeaderStatus();
        const inlineStatus = root.querySelector('.connection-inline-status');
        if (inlineStatus) inlineStatus.textContent = connectionLabel(connectionState);
      }
    })();
    runtimeRefreshPromise = refresh;
    try {
      await refresh;
    } finally {
      if (runtimeRefreshPromise === refresh) runtimeRefreshPromise = null;
    }
  }

  async function refreshBiliAuth(): Promise<void> {
    if (authRefreshActive || loginModalOpen) return;
    authRefreshActive = true;
    try {
      const next = await getBiliAuthStatus(state.roomId);
      const changed = JSON.stringify(next) !== JSON.stringify(biliAuth);
      biliAuth = next;
      if (changed) void refreshRoomGiftCatalog(true);
      if (changed && !editorOpen) render();
    } catch (error) {
      const next: BiliAuthStatus = {
        state: 'error',
        message: error instanceof Error ? error.message : '登录状态读取失败',
      };
      const changed = JSON.stringify(next) !== JSON.stringify(biliAuth);
      biliAuth = next;
      if (changed && !editorOpen) render();
    } finally {
      authRefreshActive = false;
    }
  }

  async function refreshUpdateStatus(): Promise<UpdateStatus> {
    if (updateRefreshActive) return currentUpdateStatus;
    updateRefreshActive = true;
    try {
      currentUpdateStatus = await getUpdateStatus();
    } catch (error) {
      currentUpdateStatus = {
        ...currentUpdateStatus,
        state: 'error',
        message: error instanceof Error ? error.message : '更新状态读取失败',
        autoUpdate: state.settings.autoUpdate,
      };
    } finally {
      updateRefreshActive = false;
      refreshUpdateCard?.();
    }
    return currentUpdateStatus;
  }

  async function runManualUpdateCheck(): Promise<void> {
    try {
      currentUpdateStatus = await checkForUpdates();
      refreshUpdateCard?.();
      for (let attempt = 0; attempt < 120 && ['checking', 'downloading'].includes(currentUpdateStatus.state); attempt++) {
        await new Promise((resolve) => globalThis.setTimeout(resolve, 500));
        await refreshUpdateStatus();
      }
    } catch (error) {
      currentUpdateStatus = {
        ...currentUpdateStatus,
        state: 'error',
        message: error instanceof Error ? error.message : '手动检查更新失败',
      };
      refreshUpdateCard?.();
    }
  }

  async function refreshRoomGiftCatalog(force = false): Promise<void> {
    const requestedRoomId = state.roomId.trim();
    if (!requestedRoomId) {
      roomGiftCatalogRoomId = '';
      roomGiftCatalog = [];
      return;
    }
    if (roomGiftCatalogRefreshActive || (!force && roomGiftCatalogRoomId === requestedRoomId)) return;
    roomGiftCatalogRefreshActive = true;
    try {
      const nextCatalog = await getRoomGiftCatalog(requestedRoomId);
      if (state.roomId.trim() !== requestedRoomId) return;
      const changed = roomGiftCatalogRoomId !== requestedRoomId
        || JSON.stringify(roomGiftCatalog) !== JSON.stringify(nextCatalog);
      roomGiftCatalogRoomId = requestedRoomId;
      roomGiftCatalog = nextCatalog;
      if (!changed) return;
      if (refreshOpenGiftCatalog) refreshOpenGiftCatalog();
      else if (!editorOpen) render();
    } catch {
      // Keep the local catalog as an offline fallback when Bilibili is unavailable.
    } finally {
      roomGiftCatalogRefreshActive = false;
    }
  }

  async function refreshBackendState(): Promise<void> {
    if (stateRefreshActive || editorOpen) return;
    stateRefreshActive = true;
    try {
      const previousStructure = configStructureSignature(state);
      const requestedVersion = localStateVersion;
      const nextState = await refreshStateFromServer(() => requestedVersion === localStateVersion);
      if (requestedVersion !== localStateVersion) return;
      state = nextState;
      void refreshRoomGiftCatalog();
      if (ensureRuleGiftCatalog(state)) await saveState(state);
      if (configStructureSignature(state) !== previousStructure) {
        render();
        return;
      }
      for (const valueElement of Array.from(root.querySelectorAll('.attribute-current-value'))) {
        const attribute = state.attributes.find((item) => item.name === (valueElement as HTMLElement).dataset.attributeName);
        if (attribute) valueElement.textContent = formatValue(attribute.value, attribute);
      }
    } finally {
      stateRefreshActive = false;
    }
  }

  function save(): void {
    void saveAndWait().catch(() => undefined);
  }

  async function saveAndWait(): Promise<void> {
    localStateVersion += 1;
    try {
      await saveState(state);
    } catch (error) {
      toast(error instanceof Error ? error.message : '配置保存失败', root);
      throw error;
    }
  }

  themeToggle.onclick = () => {
    state.settings.theme = state.settings.theme === 'dark' ? 'light' : 'dark';
    applyConfigTheme(state.settings.theme);
    save();
  };
  guideToggle.onclick = () => {
    guideDismissed = false;
    state.settings.showTutorial = true;
    save();
    renderGuide();
  };

  function renderGuide(): void {
    activeGuide?.dispose();
    activeGuide = null;
    if (guideDismissed) return;
    const step = getTutorialStep(state, connectionState === 'connected', editorOpen);
    if (editorOpen && !editorGuideEnabled) return;
    if (editorOpen && step === 'obs') return;
    activeGuide = renderSpotlightGuide({
      host: root,
      step,
      editorOpen,
      onDismiss: () => {
        guideDismissed = true;
        state.settings.showTutorial = false;
        save();
        activeGuide = null;
      },
    });
  }

  function render(): void {
    activeGuide?.dispose();
    activeGuide = null;
    content.replaceChildren();
    renderHeaderStatus();
    renderConnectionWorkspace();
    renderAttributesWorkspace();
    renderGiftHistory();
    renderAdvancedSettings();
    renderGuide();
  }

  function sectionHeading(kicker: string, title: string, description: string): HTMLElement {
    return el('div', { class: 'section-heading' }, [
      el('span', { class: 'section-kicker', text: kicker }),
      el('h2', { text: title }),
      el('p', { text: description }),
    ]);
  }

  function renderConnectionWorkspace(): void {
    const grid = el('section', { class: 'connection-grid' });
    const roomCard = el('article', { class: 'workspace-card room-card' });
    roomCard.append(sectionHeading('直播来源', '连接直播间', '输入房间号并测试连接，礼物目录会随着直播事件自动补充。'));

    const roomInput = inputField('房间号', state.roomId);
    roomInput.classList.add('guide-room-input');
    roomInput.placeholder = '例如 88888888';
    roomInput.inputMode = 'numeric';
    roomInput.oninput = () => undefined;
    const connectionText = el('span', { class: 'connection-inline-status', text: connectionLabel(connectionState) });
    const connectButton = el('button', { class: 'btn', type: 'button', text: '测试连接' }) as HTMLButtonElement;
    connectButton.onclick = async () => {
      const roomId = roomInput.value.trim();
      if (!roomId) {
        toast('请输入房间号', root);
        roomInput.focus();
        return;
      }
      state.roomId = roomId;
      connectionState = 'connecting';
      connectionText.textContent = connectionLabel(connectionState);
      renderHeaderStatus();
      void refreshBiliAuth();
      try {
        await saveAndWait();
        await refreshRuntime(true);
        void refreshRoomGiftCatalog(true);
      } catch {
        // saveAndWait already reports the persistence error in the page.
      }
    };
    roomCard.append(
      fieldControl(roomInput),
      el('div', { class: 'row connection-actions' }, [connectButton, connectionText]),
      el('details', { class: 'inline-help' }, [
        el('summary', { text: '房间号在哪里？' }),
        el('p', { text: '直播地址 live.bilibili.com/88888888 中的 88888888 就是房间号，不要复制问号后的参数。' }),
      ]),
    );

    grid.append(roomCard, renderLoginCard());
    content.append(grid);
  }

  function renderLoginCard(): HTMLElement {
    const card = el('article', { class: 'workspace-card login-card' });
    card.append(sectionHeading(
      '可选登录',
      '主播账号',
      '登录是可选的，用普通 B 站账号扫码即可；登录信息只加密保存在本机。',
    ));
    const identity = el('div', { class: `login-identity is-${biliAuth.state}` });
    if (biliAuth.state === 'logged_in') {
      const avatar = el('img', {
        class: 'login-avatar',
        alt: biliAuth.uname ? `${biliAuth.uname}的头像` : '主播头像',
        referrerPolicy: 'no-referrer',
      }) as HTMLImageElement;
      avatar.src = biliAuth.avatar || transparentPixel();
      identity.append(
        avatar,
        el('div', { class: 'login-identity-copy' }, [
          el('strong', { text: biliAuth.uname || `用户 ${biliAuth.uid ?? ''}` }),
          el('span', { text: `已登录 · UID ${biliAuth.uid ?? ''}${biliAuth.isRoomOwner === true ? ' · 主播身份已验证' : biliAuth.isRoomOwner === false ? ' · 普通登录身份' : ' · 填写房间号后识别身份'}` }),
        ]),
      );
    } else {
      identity.append(
        el('span', { class: 'login-mode-icon', text: '◌' }),
        el('div', { class: 'login-identity-copy' }, [
          el('strong', { text: biliAuth.state === 'expired' ? '登录已失效' : '匿名模式' }),
          el('span', { text: biliAuth.message || '礼物计算正常，但 B 站可能隐藏观众昵称和 UID。' }),
        ]),
      );
    }
    const actions = el('div', { class: 'row login-actions' });
    if (biliAuth.state === 'logged_in') {
      const logoutButton = el('button', { class: 'btn ghost', type: 'button', text: '退出登录' }) as HTMLButtonElement;
      logoutButton.onclick = () => {
        logoutButton.disabled = true;
        void logoutBiliAuth().then((next) => {
          biliAuth = next;
          render();
          toast('已切换为匿名模式', root);
        }).catch((error) => {
          logoutButton.disabled = false;
          toast(error instanceof Error ? error.message : '退出登录失败', root);
        });
      };
      actions.append(logoutButton);
    } else {
      const loginButton = el('button', { class: 'btn', type: 'button', text: '扫码登录' }) as HTMLButtonElement;
      loginButton.onclick = () => openLoginModal();
      actions.append(loginButton);
    }
    const capabilities = el('div', { class: 'login-capabilities' }, [
      el('strong', {
        class: 'login-capabilities-title',
        text: biliAuth.state === 'logged_in' ? '登录能力已开启' : '登录后可以',
      }),
      el('div', { class: 'login-capability-list' }, [
        loginCapability('自动识别盲盒会开出哪些礼物'),
        loginCapability('尽量补全送礼人的昵称和头像'),
        loginCapability('普通 B 站账号也能登录，不一定要主播本人'),
      ]),
    ]);
    card.append(
      identity,
      capabilities,
      el('p', {
        class: `login-fallback-note${biliAuth.isRoomOwner === false ? ' is-info' : ''}`,
        text: biliAuth.state === 'logged_in'
          ? 'B 站仍然隐藏的信息无法补全时，会继续显示脱敏昵称。'
          : '不登录也能连接直播间和执行礼物规则。',
      }),
      actions,
    );
    return card;
  }

  function loginCapability(text: string): HTMLElement {
    return el('div', { class: 'login-capability' }, [
      el('span', { class: 'login-capability-icon', text: '✓' }),
      el('span', { text }),
    ]);
  }

  function openLoginModal(): void {
    if (loginModalOpen) return;
    loginModalOpen = true;
    activeGuide?.dispose();
    activeGuide = null;
    const overlay = el('div', { class: 'overlay login-modal-overlay' });
    const dialog = el('section', { class: 'login-modal', role: 'dialog', ariaModal: 'true' });
    const closeButton = el('button', { class: 'modal-close', type: 'button', text: '×', ariaLabel: '关闭登录窗口' }) as HTMLButtonElement;
    const body = el('div', { class: 'login-modal-body' });
    const close = (): void => {
      if (loginPollTimer !== undefined) globalThis.clearInterval(loginPollTimer);
      loginPollTimer = undefined;
      loginModalOpen = false;
      overlay.remove();
      renderGuide();
    };
    closeButton.onclick = close;
    overlay.onclick = (event) => {
      if (event.target === overlay) close();
    };
    dialog.append(
      el('header', { class: 'modal-header' }, [
        el('div', {}, [el('span', { class: 'section-kicker', text: 'B 站账号' }), el('h2', { text: '使用哔哩哔哩 App 扫码' })]),
        closeButton,
      ]),
      body,
    );
    overlay.append(dialog);
    root.append(overlay);

    const begin = async (): Promise<void> => {
      if (loginPollTimer !== undefined) globalThis.clearInterval(loginPollTimer);
      loginPollTimer = undefined;
      body.replaceChildren(el('div', { class: 'login-loading', text: '正在生成登录二维码…' }));
      try {
        const started = await startBiliQRCodeLogin();
        const image = el('img', { class: 'login-qr-image', alt: '哔哩哔哩登录二维码' }) as HTMLImageElement;
        image.src = started.qrImage || transparentPixel();
        const statusText = el('p', { class: 'login-qr-status', text: '请使用哔哩哔哩 App 扫码，并在手机上确认登录。' });
        const refreshButton = el('button', { class: 'btn ghost', type: 'button', text: '重新生成' }) as HTMLButtonElement;
        refreshButton.onclick = () => void begin();
        body.replaceChildren(
          image,
          statusText,
          el('p', { class: 'login-security-note', text: 'Cookie 不会发送给配置页面，将使用 Windows DPAPI 加密后保存在本机。' }),
          refreshButton,
        );
        const poll = async (): Promise<void> => {
          try {
            const next = await pollBiliQRCodeLogin(state.roomId);
            if (next.state === 'logged_in') {
              biliAuth = next;
              close();
              render();
              toast(`已登录为 ${next.uname || `UID ${next.uid}`}`, root);
              return;
            }
            statusText.textContent = next.message || (next.state === 'scanned' ? '已扫码，请在手机上确认。' : '等待扫码…');
            if (next.state === 'expired' || next.state === 'error') {
              if (loginPollTimer !== undefined) globalThis.clearInterval(loginPollTimer);
              loginPollTimer = undefined;
            }
          } catch (error) {
            statusText.textContent = error instanceof Error ? error.message : '登录状态读取失败';
            if (loginPollTimer !== undefined) globalThis.clearInterval(loginPollTimer);
            loginPollTimer = undefined;
          }
        };
        loginPollTimer = globalThis.setInterval(() => void poll(), 1500);
      } catch (error) {
        body.replaceChildren(
          el('p', { class: 'login-qr-status is-error', text: error instanceof Error ? error.message : '二维码生成失败' }),
          el('button', { class: 'btn', type: 'button', text: '重试', onclick: () => void begin() }),
        );
      }
    };
    void begin();
  }

  function renderAttributesWorkspace(): void {
    const section = el('section', { class: 'attributes-section' });
    const headingRow = el('div', { class: 'attributes-heading-row' });
    headingRow.append(
      sectionHeading('互动逻辑', '属性与礼物规则', '一个属性可以被多个礼物影响；连送 N 个会按单个礼物连续执行 N 次规则。'),
    );
    const addButton = el('button', { class: 'btn guide-attribute-add', type: 'button', text: '+ 添加属性' }) as HTMLButtonElement;
    addButton.onclick = () => openAttributeEditor();
    headingRow.append(addButton);
    section.append(headingRow);

    if (state.attributes.length === 0) {
      const empty = emptyState('还没有属性。添加属性后，可以在同一个窗口里选择任意数量的礼物。');
      empty.classList.add('attribute-empty');
      section.append(empty);
    } else {
      const list = el('div', { class: 'attribute-list' });
      state.attributes.forEach((attribute, index) => list.append(renderAttributeCard(attribute, index)));
      section.append(list);
    }
    content.append(section);
  }

  function renderAttributeCard(attribute: Attribute, index: number): HTMLElement {
    const rules = state.rules.filter((rule) => rule.attributeName === attribute.name);
    const timerRules = state.timerRules.filter((rule) => rule.attributeName === attribute.name);
    const card = el('article', { class: 'attribute-card' });
    const editButton = el('button', { class: `btn ghost attribute-action-button${index === 0 ? ' guide-attribute-edit' : ''}`, type: 'button', text: '编辑' }) as HTMLButtonElement;
    editButton.onclick = () => openAttributeEditor(index);
    const deleteButton = el('button', { class: 'btn text-danger attribute-action-button', type: 'button', text: '删除' }) as HTMLButtonElement;
    deleteButton.onclick = () => {
      if (!confirm(`删除属性“${attribute.name}”及其全部触发规则？`)) return;
      state.attributes.splice(index, 1);
      state.rules = state.rules.filter((rule) => rule.attributeName !== attribute.name);
      state.timerRules = state.timerRules.filter((rule) => rule.attributeName !== attribute.name);
      save();
      render();
      toast('属性已删除', root);
    };
    const title = el('div', { class: 'attribute-card-title' });
    title.append(
      el('div', { class: 'attribute-title-copy' }, [
        el('div', { class: 'attribute-name-row' }, [
          el('h3', { text: attribute.name }),
          attributeValueElement(attribute),
        ]),
        el('span', { class: 'attribute-meta', text: `${displayFormatLabel(attribute)} · ${rules.length} 条礼物规则 · ${timerRules.length} 个定时器` }),
      ]),
      el('div', { class: 'attribute-actions' }, [editButton, deleteButton]),
    );

    const formulas = el('div', { class: 'attribute-formulas' });
    const createEnabledButton = (
      label: string,
      className: string,
      initialEnabled: boolean,
      onToggle: (enabled: boolean) => void,
    ): HTMLButtonElement => {
      let enabled = initialEnabled;
      const button = el('button', {
        class: `attribute-rule-enabled attribute-rule-enabled-button ${className}`,
        type: 'button',
        ariaLabel: label,
        title: label,
      } as any) as HTMLButtonElement;
      button.setAttribute('role', 'switch');
      button.append(el('span', { class: 'attribute-rule-enabled-track' }));
      const renderEnabled = (): void => {
        button.classList.toggle('is-active', enabled);
        button.setAttribute('aria-checked', String(enabled));
      };
      button.onclick = () => {
        enabled = !enabled;
        renderEnabled();
        onToggle(enabled);
      };
      renderEnabled();
      return button;
    };
    if (rules.length === 0 && timerRules.length === 0) {
      formulas.append(el('div', { class: 'formula-empty', text: '尚未配置触发规则，点击“编辑”即可添加。' }));
    } else {
      for (const rule of rules) {
        const gift = findGift(state, rule.giftId);
        const toggleLabel = `启用礼物规则 ${rule.formulaName?.trim() || gift?.name || rule.giftId}`;
        const giftImage = el('img', { class: 'attribute-gift-image', alt: '' }) as HTMLImageElement;
        giftImage.src = gift?.imgBasic || transparentPixel();
        const ruleCard = el('div', { class: 'attribute-gift-rule' });
        const updateEnabledAppearance = (enabled: boolean): void => {
          ruleCard.classList.toggle('is-disabled', !enabled);
        };
        const enabledButton = createEnabledButton(toggleLabel, 'gift-rule-enabled-button', rule.enabled !== false, (enabled) => {
          const currentRule = state.rules.find((candidate) => candidate.id === rule.id);
          if (!currentRule) {
            render();
            return;
          }
          currentRule.enabled = enabled;
          updateEnabledAppearance(enabled);
          save();
        });
        ruleCard.append(
          giftImage,
          el('div', { class: 'attribute-gift-copy' }, [
            el('strong', { text: gift?.name ?? `礼物 ${rule.giftId}` }),
            el('span', { text: rule.formulaName?.trim() || '未命名规则' }),
          ]),
          enabledButton,
        );
        updateEnabledAppearance(rule.enabled !== false);
        formulas.append(ruleCard);
      }
      for (const rule of timerRules) {
        const toggleLabel = `启用定时器 ${rule.formulaName || rule.id}`;
        const ruleCard = el('div', { class: 'attribute-gift-rule attribute-timer-rule' });
        const status = el('span');
        const updateEnabledAppearance = (enabled: boolean): void => {
          ruleCard.classList.toggle('is-disabled', !enabled);
          status.textContent = `每 ${formatInterval(rule.intervalSeconds)}${enabled ? '' : ' · 已停用'}`;
        };
        const enabledButton = createEnabledButton(toggleLabel, 'timer-rule-enabled-button', rule.enabled, (enabled) => {
          const currentRule = state.timerRules.find((candidate) => candidate.id === rule.id);
          if (!currentRule) {
            render();
            return;
          }
          currentRule.enabled = enabled;
          updateEnabledAppearance(enabled);
          save();
        });
        ruleCard.append(
          el('span', { class: 'attribute-timer-icon', text: '⏱' }),
          el('div', { class: 'attribute-gift-copy' }, [
            el('strong', { text: rule.formulaName || '未命名定时器' }),
            status,
          ]),
          enabledButton,
        );
        updateEnabledAppearance(rule.enabled);
        formulas.append(ruleCard);
      }
    }

    const obsUrl = `${location.origin}/?mode=display&attribute=${encodeURIComponent(attribute.name)}`;
    const obsInput = el('input', {
      class: 'field-input attribute-obs-input',
      value: obsUrl,
      readOnly: true,
      ariaLabel: `${attribute.name} 的 OBS 专属链接`,
    } as any) as HTMLInputElement;
    const copyObsButton = el('button', {
      class: `btn attribute-obs-copy${index === 0 ? ' guide-obs-copy' : ''}`,
      type: 'button',
      text: '复制 OBS 链接',
    }) as HTMLButtonElement;
    copyObsButton.onclick = async () => {
      try {
        await navigator.clipboard.writeText(obsUrl);
        toast(`“${attribute.name}”的 OBS 链接已复制`, root);
      } catch {
        obsInput.select();
        toast('请按 Ctrl+C 复制地址', root);
      }
    };
    const obsRow = el('div', { class: 'attribute-obs-row' }, [
      el('span', { class: 'attribute-obs-label', text: 'OBS 专属链接' }),
      obsInput,
      copyObsButton,
    ]);

    card.append(title, formulas, obsRow);
    return card;
  }

  function renderGiftHistory(): void {
    const entries = state.log.filter((entry) => entry.source !== 'timer');
    const section = el('section', { class: 'gift-history-section' });
    const clearButton = el('button', {
      class: 'btn ghost gift-history-clear',
      type: 'button',
      text: '清空记录',
    }) as HTMLButtonElement;
    clearButton.disabled = entries.length === 0;
    clearButton.onclick = () => {
      if (!confirm('清空全部送礼生效记录？属性值、礼物规则和定时器日志不会受影响。')) return;
      const previousLog = state.log;
      state.log = state.log.filter((entry) => entry.source === 'timer');
      clearButton.disabled = true;
      void saveAndWait().then(() => {
        render();
        toast('送礼生效记录已清空', root);
      }).catch(() => {
        state.log = previousLog;
        clearButton.disabled = false;
      });
    };
    const heading = el('div', { class: 'gift-history-heading' }, [
      sectionHeading(
        '运行核对',
        '送礼生效记录',
        `只显示真正执行过礼物规则的事件；未命中规则的礼物不会出现。最多保留最近 ${MAX_LOG} 条计算日志。`,
      ),
      el('div', { class: 'gift-history-actions' }, [
        el('div', { class: 'gift-history-count', text: `${entries.length} 条生效记录` }),
        clearButton,
      ]),
    ]);
    section.append(heading);

    if (entries.length === 0) {
      section.append(el('div', {
        class: 'gift-history-empty',
        text: '还没有送礼规则生效记录。收到命中规则的礼物后，会在这里显示完整的数值变化。',
      }));
      content.append(section);
      return;
    }

    const distinctGifts = new Set(entries.map((entry) => entry.giftId));
    const attributeTotals = new Map<string, { count: number; delta: number }>();
    for (const entry of entries) {
      const total = attributeTotals.get(entry.attributeName) ?? { count: 0, delta: 0 };
      total.count += 1;
      total.delta += entry.delta;
      attributeTotals.set(entry.attributeName, total);
    }
    const summary = el('div', { class: 'gift-history-summary' }, [
      el('span', { text: `${distinctGifts.size} 种礼物` }),
    ]);
    for (const [attributeName, total] of attributeTotals) {
      const attribute = state.attributes.find((item) => item.name === attributeName);
      const fullSummary = `${attributeName}：${total.count} 次 · 净变化 ${formatHistoryDelta(total.delta, attribute)}`;
      summary.append(el('span', {
        text: `${attributeName}：${total.count} 次 · 净变化 ${formatHistorySummaryDelta(total.delta, attribute)}`,
        title: fullSummary,
      }));
    }
    section.append(summary);

    const list = el('div', { class: 'gift-history-list' });
    const batchSize = 40;
    let visibleCount = 0;
    const loader = el('button', { class: 'gift-history-loader', type: 'button' }) as HTMLButtonElement;
    const appendBatch = (): void => {
      const nextVisibleCount = Math.min(entries.length, visibleCount + batchSize);
      loader.remove();
      for (const entry of entries.slice(visibleCount, nextVisibleCount)) {
        list.append(renderGiftHistoryRow(entry));
      }
      visibleCount = nextVisibleCount;
      loader.textContent = visibleCount < entries.length
        ? `继续下滑加载 · ${visibleCount} / ${entries.length}`
        : `已显示全部 ${entries.length} 条记录`;
      loader.disabled = visibleCount >= entries.length;
      loader.classList.toggle('is-complete', loader.disabled);
      list.append(loader);
    };
    loader.onclick = appendBatch;
    list.onscroll = () => {
      if (list.scrollTop + list.clientHeight >= list.scrollHeight - 80) appendBatch();
    };
    appendBatch();
    section.append(list);
    content.append(section);
  }

  function renderGiftHistoryRow(entry: LogEntry): HTMLElement {
    const attribute = state.attributes.find((item) => item.name === entry.attributeName);
    const rule = state.rules.find((item) => item.id === entry.ruleId);
    const gift = findGift(state, entry.giftId);
    const triggerName = entry.triggerName?.trim() || rule?.formulaName?.trim() || '历史规则';
    const before = entry.valueAfter - entry.delta;
    const avatar = el('img', {
      class: 'gift-history-avatar',
      alt: entry.uname ? `${entry.uname}的头像` : '用户头像',
      referrerPolicy: 'no-referrer',
    }) as HTMLImageElement;
    avatar.src = entry.avatar || transparentPixel();
    const giftImage = el('img', { class: 'gift-history-gift-image', alt: gift?.name || entry.giftName }) as HTMLImageElement;
    giftImage.src = gift?.imgBasic || transparentPixel();
    const time = new Date(entry.time < 1_000_000_000_000 ? entry.time * 1000 : entry.time);
    const timeText = time.toLocaleString('zh-CN', {
      month: '2-digit',
      day: '2-digit',
      hour: '2-digit',
      minute: '2-digit',
      second: '2-digit',
      hour12: false,
    });
    const beforeText = formatHistoryValue(before, attribute);
    const afterText = formatHistoryValue(entry.valueAfter, attribute);
    const deltaText = formatHistoryDelta(entry.delta, attribute);
    const transitionText = `${beforeText} → ${afterText}`;
    return el('article', { class: 'gift-history-row' }, [
      el('time', { class: 'gift-history-time', dateTime: time.toISOString(), text: timeText, title: time.toLocaleString('zh-CN') }),
      el('div', { class: 'gift-history-person' }, [
        avatar,
        el('div', { class: 'gift-history-copy' }, [
          el('strong', { text: entry.uname?.trim() || '匿名观众', title: entry.uname?.trim() || '匿名观众' }),
          el('span', { text: entry.senderUid ? `UID ${entry.senderUid}` : 'UID 未提供' }),
        ]),
      ]),
      el('div', { class: 'gift-history-gift' }, [
        giftImage,
        el('div', { class: 'gift-history-copy' }, [
          el('strong', { text: `${entry.giftName || gift?.name || '礼物'} ×${Math.max(1, entry.num || 1)}` }),
          el('span', { text: `礼物 ID ${entry.giftId}` }),
        ]),
      ]),
      el('div', { class: 'gift-history-effect' }, [
        el('strong', { text: entry.attributeName }),
        el('span', { text: triggerName, title: triggerName }),
      ]),
      el('div', { class: 'gift-history-change' }, [
        el('span', { text: transitionText, title: transitionText }),
        el('strong', { class: entry.delta < 0 ? 'is-negative' : entry.delta > 0 ? 'is-positive' : 'is-zero', text: deltaText, title: deltaText }),
      ]),
    ]);
  }

  function openAttributeEditor(index?: number): void {
    activeGuide?.dispose();
    activeGuide = null;
    root.querySelector('.attribute-overlay')?.remove();
    const stepBeforeOpen = getTutorialStep(state, connectionState === 'connected', false);
    editorOpen = true;
    editorGuideEnabled = index === undefined
      && !guideDismissed
      && (stepBeforeOpen === 'attributes' || stepBeforeOpen === 'rules');

    const original = index === undefined ? undefined : state.attributes[index];
    const originalName = original?.name ?? '';
    const timerRules = original
      ? state.timerRules.filter((rule) => rule.attributeName === original.name).map((rule) => ({ ...rule }))
      : [];
    let allGifts = giftPickerGifts(state, roomGiftCatalog);
    const selected = new Map<number, SelectedGiftRule>();
    const blindBoxLookups: SelectedGiftRule[] = [];
    if (original) {
      for (const rule of state.rules.filter((item) => item.attributeName === original.name)) {
        const gift = findGift(state, rule.giftId);
        if (gift && !selected.has(gift.id)) {
          const item: SelectedGiftRule = {
            gift,
            formulaName: rule.formulaName?.trim() || `${gift.name}规则`,
            formula: rule.formula,
            enabled: rule.enabled !== false,
            previous: rule,
            ...(rule.matchGiftIds ? { matchGiftIds: [...rule.matchGiftIds] } : {}),
            ...(rule.matchGiftIds && rule.matchGiftIds.length > 1 ? { blindBoxStatus: 'matched' as const } : {}),
          };
          selected.set(gift.id, item);
          if (!item.matchGiftIds || item.matchGiftIds.length <= 1) blindBoxLookups.push(item);
        }
      }
    }

    const overlay = el('div', { class: 'overlay attribute-overlay' });
    const modal = el('section', { class: 'card attribute-modal', role: 'dialog', ariaLabel: original ? `编辑属性 ${original.name}` : '添加属性' } as any);
    const closeButton = el('button', { class: 'modal-close', type: 'button', text: '×', ariaLabel: '关闭' } as any) as HTMLButtonElement;
    const close = (): void => {
      overlay.remove();
      editorOpen = false;
      editorGuideEnabled = false;
      refreshOpenGiftCatalog = null;
      renderGuide();
    };
    closeButton.onclick = close;
    modal.append(el('header', { class: 'modal-header' }, [
      el('div', {}, [
        el('span', { class: 'section-kicker', text: original ? '编辑互动属性' : '新建互动属性' }),
        el('h2', { text: original ? `配置“${original.name}”` : '添加属性并绑定礼物' }),
        el('p', { text: '属性基础信息、礼物选择和每个礼物的规则都在这里完成。' }),
      ]),
      closeButton,
    ]));

    const nameInput = inputField('属性名称', original?.name ?? `属性${state.attributes.length + 1}`);
    nameInput.placeholder = '例如 加班时间';
    nameInput.oninput = () => {
      const targetName = nameInput.value.trim() || '属性';
      for (const label of Array.from(modal.querySelectorAll('.formula-target-name'))) {
        label.textContent = `${targetName} =`;
      }
    };
    const valueInput = inputField('当前值', String(original?.value ?? 0));
    valueInput.inputMode = 'decimal';
    const formatSelect = el('select', { class: 'field-input' }) as HTMLSelectElement;
    formatSelect.innerHTML = '<option value="hhmmss">HH:MM:SS 计时器</option><option value="number">纯数字</option><option value="suffix">数字 + 后缀</option>';
    formatSelect.value = original?.format ?? 'hhmmss';
    const suffixInput = inputField('数值后缀', original?.suffix ?? '');
    suffixInput.placeholder = '例如 次、分、点';
    const suffixControl = fieldControl(suffixInput);
    const broadcastMessageInput = inputField('默认播报消息', original?.broadcastMessage ?? '');
    broadcastMessageInput.placeholder = '例如 感谢大家的支持，欢迎投喂礼物';
    broadcastMessageInput.maxLength = 200;
    const broadcastMessageControl = fieldControl(broadcastMessageInput);
    broadcastMessageControl.classList.add('attribute-broadcast-message');
    const updateSuffixVisibility = (): void => {
      suffixControl.hidden = formatSelect.value !== 'suffix';
    };
    formatSelect.onchange = updateSuffixVisibility;
    updateSuffixVisibility();
    const basics = el('div', { class: 'attribute-basics' }, [
      fieldControl(nameInput),
      fieldControl(valueInput),
      el('label', { class: 'field' }, [el('span', { class: 'field-label', text: '显示格式' }), formatSelect]),
      suffixControl,
      broadcastMessageControl,
    ]);

    const currentAttributeName = (): string => nameInput.value.trim() || originalName || '属性';
    const formulaForCurrentAttribute = (formula: string): string => (
      originalName && originalName !== currentAttributeName()
        ? replaceFormulaVariable(formula.trim(), originalName, currentAttributeName())
        : formula.trim()
    );

    type RefreshablePresetList = HTMLElement & { refreshPresets?: () => void };

    function refreshFormulaPresetLists(context: FormulaPresetContext): void {
      const lists = Array.from(modal.querySelectorAll<RefreshablePresetList>('.formula-preset-list'))
        .filter((list) => list.dataset.presetContext === context);
      for (const list of lists) list.refreshPresets?.();
    }

    function openFormulaPresetNameDialog(
      context: FormulaPresetContext,
      formulaInput: HTMLInputElement,
      formulaNameInput: HTMLInputElement,
    ): void {
      root.querySelector('.formula-preset-name-overlay')?.remove();
      const nameOverlay = el('div', { class: 'overlay formula-preset-name-overlay' });
      const nameDialog = el('section', {
        class: 'card formula-preset-name-dialog',
        role: 'dialog',
        ariaLabel: '命名规则预设',
      } as any);
      const presetNameInput = inputField('预设名称', formulaNameInput.value.trim());
      presetNameInput.placeholder = '例如 按价格增加时间';
      const cancelButton = el('button', { class: 'btn ghost', type: 'button', text: '取消' }) as HTMLButtonElement;
      const confirmButton = el('button', { class: 'btn', type: 'button', text: '保存' }) as HTMLButtonElement;
      const close = (): void => nameOverlay.remove();
      cancelButton.onclick = close;
      const commit = (): void => {
        if (confirmButton.disabled) return;
        void (async () => {
          const attributeName = currentAttributeName();
          const formula = formulaForCurrentAttribute(formulaInput.value);
          confirmButton.disabled = true;
          confirmButton.textContent = '校验中…';
          try {
            const value = Number(valueInput.value);
            await previewFormula(formula, attributeName, Number.isFinite(value) ? value : 0, context === 'timer' ? 'timer' : undefined);
            const result = saveFormulaPreset(state.formulaPresets, {
              name: presetNameInput.value,
              context,
              formula,
              sourceAttributeName: attributeName,
            });
            state.formulaPresets = result.presets;
            await saveAndWait();
            refreshFormulaPresetLists(context);
            close();
            toast(result.created ? '规则预设已保存' : '同名规则预设已更新', root);
          } catch (error) {
            toast(error instanceof Error ? error.message : '规则预设保存失败', root);
          } finally {
            confirmButton.disabled = false;
            confirmButton.textContent = '保存';
          }
        })();
      };
      confirmButton.onclick = commit;
      presetNameInput.onkeydown = (event) => {
        if (event.key === 'Enter') commit();
        if (event.key === 'Escape') close();
      };
      nameDialog.append(
        el('div', { class: 'formula-preset-name-header' }, [
          el('h3', { text: '保存规则预设' }),
          el('p', { text: '给当前规则起个容易识别的名字。' }),
        ]),
        fieldControl(presetNameInput),
        el('div', { class: 'formula-preset-name-actions' }, [cancelButton, confirmButton]),
      );
      nameOverlay.append(nameDialog);
      root.append(nameOverlay);
      presetNameInput.focus();
    }

    function renderFormulaPresetControls(
      context: FormulaPresetContext,
      formulaInput: HTMLInputElement,
      formulaNameInput: HTMLInputElement,
      updatePreview: () => void,
    ): { saveButton: HTMLButtonElement; presetList: HTMLElement } {
      const saveButton = el('button', {
        class: 'formula-save-preset',
        type: 'button',
        text: '保存预设',
      }) as HTMLButtonElement;
      saveButton.onclick = () => openFormulaPresetNameDialog(context, formulaInput, formulaNameInput);

      const presetList = el('div', { class: 'formula-preset-list' }) as RefreshablePresetList;
      presetList.dataset.presetContext = context;
      presetList.refreshPresets = () => {
        presetList.replaceChildren();
        for (const preset of state.formulaPresets.filter((item) => item.context === context)) {
          const applyButton = el('button', {
            class: 'formula-preset-apply',
            type: 'button',
            text: preset.name,
            title: `应用预设“${preset.name}”`,
          }) as HTMLButtonElement;
          applyButton.onclick = () => {
            formulaInput.value = applyFormulaPreset(preset, currentAttributeName());
            updatePreview();
            toast(`已应用预设“${preset.name}”`, root);
          };
          const deleteButton = el('button', {
            class: 'formula-preset-delete',
            type: 'button',
            text: '×',
            ariaLabel: `删除预设 ${preset.name}`,
          } as any) as HTMLButtonElement;
          deleteButton.onclick = () => {
            state.formulaPresets = state.formulaPresets.filter((item) => item.id !== preset.id);
            void saveAndWait().then(() => {
              refreshFormulaPresetLists(context);
              toast('规则预设已删除', root);
            });
          };
          presetList.append(el('span', { class: 'formula-preset-chip' }, [applyButton, deleteButton]));
        }
      };
      presetList.refreshPresets();
      return { saveButton, presetList };
    }

    const timerList = el('div', { class: 'timer-rule-list' });
    const timerCount = el('span', { class: 'selection-count' });

    function renderTimerRules(): void {
      timerCount.textContent = timerRules.length === 0 ? '未启用' : `${timerRules.length} 个定时器`;
      timerList.replaceChildren();
      if (timerRules.length === 0) {
        timerList.append(el('div', {
          class: 'timer-rule-empty',
          text: '没有定时器。添加后，即使配置页和 OBS 都关闭，托盘后台仍会按间隔执行规则。',
        }));
        return;
      }
      timerRules.forEach((rule) => timerList.append(renderTimerRuleEditor(rule)));
    }

    function renderTimerRuleEditor(rule: TimerRule): HTMLElement {
      const editor = el('article', { class: 'timer-rule-editor' });
      const removeButton = el('button', { class: 'rule-remove', type: 'button', text: '移除' }) as HTMLButtonElement;
      removeButton.onclick = () => {
        const timerIndex = timerRules.findIndex((candidate) => candidate.id === rule.id);
        if (timerIndex >= 0) timerRules.splice(timerIndex, 1);
        renderTimerRules();
      };
      editor.append(el('div', { class: 'timer-rule-header' }, [
        el('div', { class: 'timer-rule-title' }, [
          el('span', { class: 'timer-rule-clock', text: '⏱' }),
          el('div', {}, [
            el('strong', { text: '后台定时触发' }),
            el('small', { text: '从保存或服务启动后开始计算第一个完整间隔' }),
          ]),
        ]),
        el('div', { class: 'timer-rule-actions' }, [
          removeButton,
        ]),
      ]));

      const formulaNameInput = inputField('触发器名称', rule.formulaName);
      formulaNameInput.placeholder = '例如 每分钟自动减少';
      formulaNameInput.oninput = () => {
        rule.formulaName = formulaNameInput.value;
      };

      const intervalParts = splitInterval(rule.intervalSeconds);
      const intervalInput = inputField('触发间隔', String(intervalParts.value));
      intervalInput.type = 'number';
      intervalInput.min = '1';
      intervalInput.step = '1';
      const intervalUnit = el('select', { class: 'field-input timer-interval-unit' }) as HTMLSelectElement;
      intervalUnit.innerHTML = '<option value="1">秒</option><option value="60">分钟</option><option value="3600">小时</option>';
      intervalUnit.value = String(intervalParts.multiplier);
      const syncInterval = (): void => {
        const amount = Math.max(1, Math.floor(Number(intervalInput.value) || 1));
        rule.intervalSeconds = amount * Number(intervalUnit.value);
      };
      intervalInput.oninput = syncInterval;
      intervalUnit.onchange = syncInterval;
      const intervalControl = el('label', { class: 'field timer-interval-field' }, [
        el('span', { class: 'field-label', text: '触发间隔' }),
        el('div', { class: 'timer-interval-row' }, [intervalInput, intervalUnit]),
      ]);

      const conditionInput = inputField('运行条件（可留空）', rule.condition ?? '');
      conditionInput.classList.add('formula');
      conditionInput.placeholder = `${nameInput.value.trim() || '属性'}>0`;
      conditionInput.oninput = () => {
        rule.condition = conditionInput.value;
        updatePreview();
      };

      const formulaInput = inputField('定时触发后属性值', rule.formula);
      formulaInput.classList.add('formula');
      formulaInput.placeholder = `MAX(${nameInput.value.trim() || '属性'}-60,0)`;
      formulaInput.setAttribute('aria-label', '定时触发后属性值');
      const formulaControl = el('div', { class: 'field formula-assignment-field' });
      const formulaHeading = el('div', { class: 'formula-field-heading' }, [
        el('span', { class: 'field-label', text: '触发后属性值' }),
      ]);
      const assignmentRow = el('div', { class: 'formula-assignment-row' });
      assignmentRow.append(
        el('code', { class: 'formula-target-name', text: `${nameInput.value.trim() || '属性'} =` }),
        formulaInput,
      );
      formulaControl.append(
        formulaHeading,
        assignmentRow,
      );

      const preview = el('div', { class: 'formula-preview' });
      let previewVersion = 0;
      const updatePreview = (): void => {
        rule.formula = formulaInput.value;
        const name = nameInput.value.trim() || originalName || '属性';
        const value = Number(valueInput.value);
        const currentValue = Number.isFinite(value) ? value : 0;
        const condition = originalName && originalName !== name
          ? replaceFormulaVariable((rule.condition ?? '').trim(), originalName, name)
          : (rule.condition ?? '').trim();
        const formula = originalName && originalName !== name
          ? replaceFormulaVariable(rule.formula.trim(), originalName, name)
          : rule.formula.trim();
        const requestVersion = ++previewVersion;
        preview.replaceChildren(el('span', { text: '由后台计算预览…' }));
        void (async () => {
          if (condition) {
            const conditionResult = await previewFormula(condition, name, currentValue, 'timer');
            if (conditionResult === 0) return { skipped: true as const, result: currentValue };
          }
          return { skipped: false as const, result: await previewFormula(formula, name, currentValue, 'timer') };
        })().then(({ skipped, result }) => {
          if (requestVersion !== previewVersion) return;
          if (skipped) {
            preview.replaceChildren(el('span', { text: '当前条件不满足，本次会跳过' }));
            return;
          }
          preview.replaceChildren(
            el('span', { text: `预览：${currentValue} → ` }),
            el('strong', { text: String(result) }),
          );
        }).catch((error) => {
          if (requestVersion !== previewVersion) return;
          preview.replaceChildren(el('span', {
            class: 'error',
            text: error instanceof Error ? error.message : String(error),
          }));
        });
      };
      formulaInput.oninput = updatePreview;

      const examples = el('div', { class: 'formula-examples' });
      const presetControls = renderFormulaPresetControls('timer', formulaInput, formulaNameInput, updatePreview);
      formulaHeading.append(presetControls.saveButton);
      const timerExamples: Array<[string, () => string]> = [
        ['每次 -1', () => `MAX(${nameInput.value.trim() || '属性'}-1,0)`],
        ['每次 -60', () => `MAX(${nameInput.value.trim() || '属性'}-60,0)`],
        ['直接归零', () => '0'],
      ];
      for (const [label, makeFormula] of timerExamples) {
        const example = el('button', { class: 'formula-example', type: 'button', text: label }) as HTMLButtonElement;
        example.onclick = () => {
          formulaInput.value = makeFormula();
          updatePreview();
        };
        examples.append(example);
      }
      examples.append(presetControls.presetList);

      editor.append(
        el('div', { class: 'timer-rule-fields' }, [
          fieldControl(formulaNameInput),
          intervalControl,
          fieldControl(conditionInput),
          formulaControl,
        ]),
        el('div', { class: 'formula-editor-meta' }, [examples, preview]),
      );
      updatePreview();
      return editor;
    }

    const addTimerButton = el('button', { class: 'btn ghost add-timer-button', type: 'button', text: '+ 添加定时器' }) as HTMLButtonElement;
    addTimerButton.onclick = () => {
      const attributeName = nameInput.value.trim() || '属性';
      timerRules.push({
        id: createTimerRuleId(),
        attributeName,
        formulaName: '每分钟自动减少',
        intervalSeconds: 60,
        condition: `${attributeName}>0`,
        formula: `MAX(${attributeName}-60,0)`,
        enabled: true,
      });
      renderTimerRules();
      timerList.querySelector('.timer-rule-editor:last-child')?.scrollIntoView({ block: 'nearest' });
    };
    const timerPanel = el('section', { class: 'timer-binding-panel' }, [
      el('div', { class: 'modal-section-heading' }, [
        el('div', {}, [
          el('h3', { text: '定时触发器' }),
          el('p', { text: '按固定间隔独立执行规则，不依赖直播连接、配置页或 OBS 页面。' }),
        ]),
        el('div', { class: 'timer-heading-actions' }, [timerCount, addTimerButton]),
      ]),
      el('p', {
        class: 'timer-condition-help',
        text: '运行条件留空时每次都执行；也可使用属性名和 >、>=、<、<=、=，例如“加班时间 > 0”。定时器只修改属性值，不会显示在 OBS 面板中。',
      }),
      timerList,
    ]);

    const giftSearch = el('input', { class: 'field-input gift-search guide-gift-search', placeholder: '搜索礼物名称或 ID…' }) as HTMLInputElement;
    const giftPicker = el('div', { class: 'gift-picker-grid' });
    const selectedRules = el('div', { class: 'selected-rules' });
    const selectionCount = el('span', { class: 'selection-count' });
    const giftChoiceButtons = new Map<number, HTMLButtonElement>();
    const giftPickerBatchSize = 40;
    let filteredGifts: GiftInfo[] = [];
    let visibleGiftCount = 0;
    let giftPickerLoader: HTMLButtonElement | null = null;

    const defaultFormula = (): string => `${nameInput.value.trim() || '属性'}+price/1000*60`;

    function updateGiftChoice(giftId: number): void {
      const button = giftChoiceButtons.get(giftId);
      if (!button) return;
      const selectedNow = selected.has(giftId);
      button.classList.toggle('is-selected', selectedNow);
      const check = button.querySelector('.gift-choice-check');
      if (check) check.textContent = selectedNow ? '✓' : '+';
      button.setAttribute('aria-pressed', String(selectedNow));
    }

    function createGiftChoice(gift: GiftInfo, showListingStatus: boolean): HTMLButtonElement {
      const selectedNow = selected.has(gift.id);
      const button = el('button', {
        class: `gift-choice${selectedNow ? ' is-selected' : ''}`,
        type: 'button',
        ariaPressed: String(selectedNow),
      }) as HTMLButtonElement;
      button.dataset.giftId = String(gift.id);
      const image = el('img', { class: 'gift-choice-image', alt: '' }) as HTMLImageElement;
      image.src = gift.imgBasic || transparentPixel();
      const listingStatus = gift.listed === true ? '已上架' : '未上架';
      button.append(
        image,
        el('span', { class: 'gift-choice-copy' }, [
          el('strong', { text: gift.name }),
          el('span', { class: 'gift-choice-meta' }, [
            el('small', { text: giftPriceLabel(gift) }),
            ...(showListingStatus ? [el('span', {
              class: `gift-listing-status ${gift.listed === true ? 'is-listed' : 'is-unlisted'}`,
              text: listingStatus,
            })] : []),
          ]),
        ]),
        el('span', { class: 'gift-choice-check', text: selectedNow ? '✓' : '+' }),
      );
      button.onclick = () => {
        if (selected.has(gift.id)) selected.delete(gift.id);
        else {
          const item: SelectedGiftRule = { gift, formulaName: `${gift.name}规则`, formula: defaultFormula(), enabled: true };
          selected.set(gift.id, item);
          void hydrateBlindBoxRule(item);
        }
        updateGiftChoice(gift.id);
        renderSelectedRules();
      };
      giftChoiceButtons.set(gift.id, button);
      return button;
    }

    function updateGiftPickerLoader(): void {
      giftPickerLoader?.remove();
      const complete = visibleGiftCount >= filteredGifts.length;
      giftPickerLoader = el('button', {
        class: `gift-picker-loader${complete ? ' is-complete' : ''}`,
        type: 'button',
        disabled: complete,
        text: complete
          ? `已显示全部 ${filteredGifts.length} 个礼物`
          : `继续下滑加载 · ${visibleGiftCount} / ${filteredGifts.length}`,
      }) as HTMLButtonElement;
      if (!complete) giftPickerLoader.onclick = appendGiftPickerBatch;
      giftPicker.append(giftPickerLoader);
    }

    function appendGiftPickerBatch(): void {
      if (visibleGiftCount >= filteredGifts.length) return;
      giftPickerLoader?.remove();
      const nextCount = Math.min(filteredGifts.length, visibleGiftCount + giftPickerBatchSize);
      const showListingStatus = giftSearch.value.trim().length > 0;
      for (const gift of filteredGifts.slice(visibleGiftCount, nextCount)) {
        giftPicker.append(createGiftChoice(gift, showListingStatus));
      }
      visibleGiftCount = nextCount;
      updateGiftPickerLoader();
    }

    function renderGiftPicker(): void {
      const query = giftSearch.value.trim();
      filteredGifts = allGifts.filter((gift) => (query.length > 0 || gift.listed !== false)
        && matchesGiftSearch(gift, query));
      visibleGiftCount = 0;
      giftPickerLoader = null;
      giftChoiceButtons.clear();
      giftPicker.replaceChildren();
      giftPicker.scrollTop = 0;
      if (filteredGifts.length === 0) {
        giftPicker.append(el('div', { class: 'picker-empty', text: '没有匹配的礼物，可在下方手动添加。' }));
        return;
      }
      appendGiftPickerBatch();
    }

    giftPicker.onscroll = () => {
      const distanceToBottom = giftPicker.scrollHeight - giftPicker.scrollTop - giftPicker.clientHeight;
      if (distanceToBottom <= 80) appendGiftPickerBatch();
    };

    function renderSelectedRules(): void {
      selectionCount.textContent = `已选择 ${selected.size} 个礼物`;
      selectedRules.replaceChildren();
      if (selected.size === 0) {
        selectedRules.append(el('div', { class: 'selected-rules-empty', text: '还没有选择礼物。属性可以先单独保存，之后再回来绑定。' }));
        return;
      }
      for (const item of selected.values()) {
        selectedRules.append(renderFormulaEditor(item));
        updateBlindBoxStatus(item);
      }
    }

    async function hydrateBlindBoxRule(item: SelectedGiftRule): Promise<void> {
      if (selected.get(item.gift.id) !== item || (item.matchGiftIds?.length ?? 0) > 1) return;
      try {
        const lookup = await getBlindBoxInfo(item.gift.id);
        if (selected.get(item.gift.id) !== item) return;
        if (lookup.info && lookup.info.gifts.length > 0) {
          item.matchGiftIds = Array.from(new Set([
            item.gift.id,
            ...lookup.info.gifts.map((gift) => gift.id),
          ])).filter((id) => id > 0);
          item.blindBoxName = lookup.info.name;
          item.blindBoxStatus = 'matched';
        } else if (lookup.requiresLogin) {
          item.blindBoxStatus = 'login-required';
        } else {
          item.blindBoxStatus = 'not-blind-box';
        }
        updateBlindBoxStatus(item);
      } catch {
        if (selected.get(item.gift.id) !== item) return;
        item.blindBoxStatus = 'error';
        updateBlindBoxStatus(item);
      }
    }

    function updateBlindBoxStatus(item: SelectedGiftRule): void {
      const row = selectedRules.querySelector<HTMLElement>(`.selected-gift-rule[data-gift-id="${item.gift.id}"]`);
      const status = row?.querySelector<HTMLElement>('.selected-rule-blind-box');
      if (!status) return;
      if (item.blindBoxStatus === 'matched' && item.matchGiftIds && item.matchGiftIds.length > 1) {
        status.textContent = `${item.blindBoxName || '盲盒'}：自动匹配 ${item.matchGiftIds.length - 1} 种爆出礼物`;
        status.hidden = false;
      } else if (item.blindBoxStatus === 'login-required') {
        status.textContent = '登录 B 站后可自动加载盲盒爆出礼物';
        status.hidden = false;
      } else if (item.blindBoxStatus === 'error') {
        status.textContent = '盲盒信息暂时不可用，仍会匹配已配置礼物';
        status.hidden = false;
      } else {
        status.textContent = '';
        status.hidden = true;
      }
    }

    function renderFormulaEditor(item: SelectedGiftRule): HTMLElement {
      const row = el('article', { class: 'selected-gift-rule' });
      row.dataset.giftId = String(item.gift.id);
      const removeButton = el('button', { class: 'rule-remove', type: 'button', text: '移除' }) as HTMLButtonElement;
      removeButton.onclick = () => {
        selected.delete(item.gift.id);
        updateGiftChoice(item.gift.id);
        renderSelectedRules();
      };
      const giftImage = el('img', { class: 'selected-rule-gift-image', alt: '' }) as HTMLImageElement;
      giftImage.src = item.gift.imgBasic || transparentPixel();
      const blindBoxStatus = el('small', { class: 'selected-rule-blind-box' });
      blindBoxStatus.hidden = true;
      row.append(el('div', { class: 'selected-rule-header' }, [
        el('div', { class: 'selected-rule-gift' }, [
          giftImage,
          el('div', {}, [
            el('strong', { text: item.gift.name }),
            el('small', { text: `每收到 1 个执行一次 · ${giftPriceLabel(item.gift)}` }),
            blindBoxStatus,
          ]),
        ]),
        removeButton,
      ]));
      const formulaNameInput = inputField('规则名称', item.formulaName);
      formulaNameInput.placeholder = `例如 ${item.gift.name}加时`;
      formulaNameInput.oninput = () => {
        item.formulaName = formulaNameInput.value;
      };
      const formulaInput = inputField('触发后属性值', item.formula);
      formulaInput.classList.add('formula');
      formulaInput.placeholder = `${nameInput.value.trim() || '属性'}+60`;
      formulaInput.setAttribute('aria-label', '触发后属性值');
      const formulaControl = el('div', { class: 'field formula-assignment-field' });
      const formulaHeading = el('div', { class: 'formula-field-heading' }, [
        el('span', { class: 'field-label', text: '触发后属性值' }),
      ]);
      const assignmentRow = el('div', { class: 'formula-assignment-row' });
      assignmentRow.append(
        el('code', { class: 'formula-target-name', text: `${nameInput.value.trim() || '属性'} =` }),
        formulaInput,
      );
      formulaControl.append(
        formulaHeading,
        assignmentRow,
      );
      const preview = el('div', { class: 'formula-preview' });
      let previewVersion = 0;
      const updatePreview = (): void => {
        item.formula = formulaInput.value;
        preview.replaceChildren();
        const name = nameInput.value.trim() || originalName || '属性';
        const value = Number(valueInput.value);
        const formula = originalName && originalName !== name
          ? replaceFormulaVariable(item.formula.trim(), originalName, name)
          : item.formula.trim();
        const currentValue = Number.isFinite(value) ? value : 0;
        const requestVersion = ++previewVersion;
        preview.append(el('span', { text: '由后台计算预览…' }));
        void previewFormula(formula, name, currentValue).then((result) => {
          if (requestVersion !== previewVersion) return;
          preview.replaceChildren(
            el('span', { text: `预览：${currentValue} → ` }),
            el('strong', { text: String(result) }),
          );
        }).catch((error) => {
          if (requestVersion !== previewVersion) return;
          preview.replaceChildren(
            el('span', { class: 'error', text: error instanceof Error ? error.message : String(error) }),
          );
        });
      };
      formulaInput.oninput = updatePreview;
      const examples = el('div', { class: 'formula-examples' });
      const presetControls = renderFormulaPresetControls('gift', formulaInput, formulaNameInput, updatePreview);
      formulaHeading.append(presetControls.saveButton);
      const exampleFactories: Array<[string, () => string]> = [
        ['每个 +1', () => `${nameInput.value.trim() || '属性'}+1`],
        ['按价格增加时间', () => `${nameInput.value.trim() || '属性'}+price/1000*60`],
        ['随机 +10~60', () => `${nameInput.value.trim() || '属性'}+RANDBETWEEN(10,60)`],
      ];
      for (const [label, makeFormula] of exampleFactories) {
        const example = el('button', { class: 'formula-example', type: 'button', text: label }) as HTMLButtonElement;
        example.onclick = () => {
          formulaInput.value = makeFormula();
          updatePreview();
        };
        examples.append(example);
      }
      examples.append(presetControls.presetList);
      const editorFields = el('div', { class: 'rule-editor-fields' }, [
        fieldControl(formulaNameInput),
        formulaControl,
      ]);
      const editorMeta = el('div', { class: 'formula-editor-meta' }, [examples, preview]);
      row.append(editorFields, editorMeta);
      updatePreview();
      return row;
    }

    giftSearch.oninput = renderGiftPicker;
    const manualGift = renderManualGiftAdder(() => {
      allGifts = giftPickerGifts(state, roomGiftCatalog);
      renderGiftPicker();
      renderSelectedRules();
    }, selected, defaultFormula, (item) => { void hydrateBlindBoxRule(item); });

    const giftsPanel = el('section', { class: 'gift-binding-panel' });
    giftsPanel.append(
      el('div', { class: 'modal-section-heading' }, [
        el('div', {}, [
          el('h3', { text: '选择会影响这个属性的礼物' }),
          el('p', { text: '默认只显示当前已上架礼物；搜索时会同时显示未上架礼物并标注状态。向下滚动会自动加载更多，数字 ID 需要完整匹配。' }),
        ]),
        selectionCount,
      ]),
      giftSearch,
      giftPicker,
      manualGift,
    );

    const formulasPanel = el('section', { class: 'formula-binding-panel' });
    const formulaHelp = renderFormulaHelp(nameInput.value.trim() || '属性');
    formulasPanel.append(
      el('div', { class: 'modal-section-heading' }, [
        el('div', {}, [
          el('h3', { text: '为每个礼物配置规则' }),
          el('p', {}, [
            '可用变量：',
            el('code', { text: 'price' }),
            ' 和任意属性名。连送会按单个礼物逐次执行；盲盒会自动匹配爆出的子礼物。',
          ]),
        ]),
      ]),
      formulaHelp,
      selectedRules,
    );

    const cancelButton = el('button', { class: 'btn ghost', type: 'button', text: '取消' }) as HTMLButtonElement;
    cancelButton.onclick = close;
    const saveButton = el('button', { class: 'btn', type: 'button', text: original ? '保存修改' : '创建属性' }) as HTMLButtonElement;
    saveButton.onclick = () => {
      void saveAttributeEditor(index, original, nameInput, valueInput, formatSelect, suffixInput, broadcastMessageInput, selected, timerRules, overlay, saveButton);
    };

    modal.append(
      basics,
      el('div', { class: 'modal-tip', text: '礼物规则和定时规则都由托盘后台执行；关闭配置页或 OBS 不会停止计算。' }),
      timerPanel,
      giftsPanel,
      formulasPanel,
      el('footer', { class: 'modal-actions' }, [cancelButton, saveButton]),
    );
    overlay.append(modal);
    let overlayPointerStartedOutside = false;
    overlay.onpointerdown = (event) => {
      overlayPointerStartedOutside = event.target === overlay;
    };
    overlay.onclick = (event) => {
      const shouldClose = overlayPointerStartedOutside && event.target === overlay;
      overlayPointerStartedOutside = false;
      if (shouldClose) close();
    };
    root.append(overlay);
    refreshOpenGiftCatalog = () => {
      allGifts = giftPickerGifts(state, roomGiftCatalog);
      renderGiftPicker();
      renderSelectedRules();
    };
    renderGiftPicker();
    renderSelectedRules();
    for (const item of blindBoxLookups) void hydrateBlindBoxRule(item);
    renderTimerRules();
    renderGuide();
    nameInput.focus();
  }

  function renderManualGiftAdder(
    onAdded: () => void,
    selected: Map<number, SelectedGiftRule>,
    defaultFormula: () => string,
    hydrateBlindBoxRule?: (item: SelectedGiftRule) => void,
  ): HTMLElement {
    const details = el('details', { class: 'manual-gift-adder' });
    details.append(el('summary', { text: '找不到礼物？按 ID 手动添加' }));
    const idInput = inputField('礼物 ID', '');
    const nameInput = inputField('礼物名称', '');
    const priceInput = inputField('单价 price（可填 0）', '0');
    const addButton = el('button', { class: 'btn ghost', type: 'button', text: '添加并选中' }) as HTMLButtonElement;
    addButton.onclick = () => {
      const id = Number(idInput.value.trim());
      const price = Number(priceInput.value.trim());
      if (!Number.isInteger(id) || id <= 0) {
        toast('请输入有效的礼物 ID', root);
        return;
      }
      const gift: GiftInfo = {
        id,
        name: nameInput.value.trim() || `礼物 ${id}`,
        price: Number.isFinite(price) ? price : 0,
        coinType: 'gold',
        imgBasic: '',
      };
      const recentIndex = state.recentGifts.findIndex((item) => item.id === id);
      const recent = { ...gift, lastReceived: 0, count: recentIndex >= 0 ? state.recentGifts[recentIndex].count : 0 };
      if (recentIndex >= 0) state.recentGifts[recentIndex] = recent;
      else state.recentGifts.unshift(recent);
      const item: SelectedGiftRule = { gift, formulaName: `${gift.name}规则`, formula: defaultFormula(), enabled: true };
      selected.set(id, item);
      hydrateBlindBoxRule?.(item);
      save();
      onAdded();
      details.open = false;
    };
    details.append(el('div', { class: 'manual-gift-fields' }, [
      fieldControl(idInput),
      fieldControl(nameInput),
      fieldControl(priceInput),
      addButton,
    ]));
    return details;
  }

  function saveAttributeEditor(
    index: number | undefined,
    original: Attribute | undefined,
    nameInput: HTMLInputElement,
    valueInput: HTMLInputElement,
    formatSelect: HTMLSelectElement,
    suffixInput: HTMLInputElement,
    broadcastMessageInput: HTMLInputElement,
    selected: Map<number, SelectedGiftRule>,
    timerRules: TimerRule[],
    overlay: HTMLElement,
    saveButton: HTMLButtonElement,
  ): Promise<void> {
    return saveAttributeEditorAsync(index, original, nameInput, valueInput, formatSelect, suffixInput, broadcastMessageInput, selected, timerRules, overlay, saveButton);
  }

  async function saveAttributeEditorAsync(
    index: number | undefined,
    original: Attribute | undefined,
    nameInput: HTMLInputElement,
    valueInput: HTMLInputElement,
    formatSelect: HTMLSelectElement,
    suffixInput: HTMLInputElement,
    broadcastMessageInput: HTMLInputElement,
    selected: Map<number, SelectedGiftRule>,
    timerRules: TimerRule[],
    overlay: HTMLElement,
    saveButton: HTMLButtonElement,
  ): Promise<void> {
    const name = nameInput.value.trim();
    const value = Number(valueInput.value);
    if (!name) {
      toast('请填写属性名称', root);
      nameInput.focus();
      return;
    }
    if (state.attributes.some((attribute, attributeIndex) => attribute.name === name && attributeIndex !== index)) {
      toast('属性名称不能重复', root);
      nameInput.focus();
      return;
    }
    if (!Number.isFinite(value)) {
      toast('当前值必须是数字', root);
      valueInput.focus();
      return;
    }

    const originalName = original?.name ?? '';
    const normalizedRules: SelectedGiftRule[] = [];
    const normalizedTimers: TimerRule[] = [];
    for (const item of selected.values()) {
      const formulaName = item.formulaName.trim();
      if (!formulaName) {
        toast(`请填写“${item.gift.name}”的规则名称`, root);
        return;
      }
      const formula = originalName && originalName !== name
        ? replaceFormulaVariable(item.formula.trim(), originalName, name)
        : item.formula.trim();
      if (!formula) {
        toast(`请填写“${item.gift.name}”的规则`, root);
        return;
      }
      normalizedRules.push({ ...item, formulaName, formula });
    }

    for (const timer of timerRules) {
      const formulaName = timer.formulaName.trim();
      if (!formulaName) {
        toast('请填写定时器名称', root);
        return;
      }
      if (!Number.isInteger(timer.intervalSeconds) || timer.intervalSeconds < 1) {
        toast(`“${formulaName}”的触发间隔必须至少为 1 秒`, root);
        return;
      }
      const condition = originalName && originalName !== name
        ? replaceFormulaVariable((timer.condition ?? '').trim(), originalName, name)
        : (timer.condition ?? '').trim();
      const formula = originalName && originalName !== name
        ? replaceFormulaVariable(timer.formula.trim(), originalName, name)
        : timer.formula.trim();
      if (!formula) {
        toast(`请填写“${formulaName}”的规则`, root);
        return;
      }
      normalizedTimers.push({ ...timer, attributeName: name, formulaName, condition, formula });
    }

    saveButton.disabled = true;
    saveButton.textContent = '后台校验中…';
    try {
      for (const item of normalizedRules) {
        await previewFormula(item.formula, name, value);
      }
      for (const timer of normalizedTimers) {
        if (timer.condition) await previewFormula(timer.condition, name, value, 'timer');
        await previewFormula(timer.formula, name, value, 'timer');
      }
    } catch (error) {
      toast(error instanceof Error ? `规则有误：${error.message}` : '规则有误', root);
      saveButton.disabled = false;
      saveButton.textContent = original ? '保存修改' : '创建属性';
      return;
    }

    const format = formatSelect.value as Attribute['format'];
    const nextAttribute: Attribute = {
      name,
      value,
      unit: format === 'hhmmss' ? 'seconds' : 'none',
      format,
      decimals: original?.decimals ?? 0,
      suffix: format === 'suffix' ? suffixInput.value : '',
      broadcastMessage: broadcastMessageInput.value.trim(),
      ...(original?.color ? { color: original.color } : {}),
    };
    if (index === undefined) state.attributes.push(nextAttribute);
    else state.attributes[index] = nextAttribute;

    const renamedRules = state.rules.map((rule) => ({
      ...rule,
      attributeName: originalName && rule.attributeName === originalName ? name : rule.attributeName,
      formula: originalName && originalName !== name ? replaceFormulaVariable(rule.formula, originalName, name) : rule.formula,
    }));
    const unrelatedRules = renamedRules.filter((rule) => rule.attributeName !== name);
    const replacementRules: GiftRule[] = normalizedRules.map((item) => ({
      id: item.previous?.id ?? createRuleId(),
      giftId: item.gift.id,
      attributeName: name,
      formulaName: item.formulaName,
      formula: item.formula,
      enabled: item.enabled,
      ...(item.matchGiftIds && item.matchGiftIds.length > 1
        ? { matchGiftIds: Array.from(new Set(item.matchGiftIds.filter((giftId) => giftId > 0))) }
        : {}),
      ...(item.previous?.minPrice !== undefined ? { minPrice: item.previous.minPrice } : {}),
      ...(item.previous?.cap !== undefined ? { cap: item.previous.cap } : {}),
      ...(item.previous?.dailyLimit !== undefined ? { dailyLimit: item.previous.dailyLimit } : {}),
    }));
    state.rules = [...unrelatedRules, ...replacementRules];
    const renamedTimers = state.timerRules.map((rule) => ({
      ...rule,
      attributeName: originalName && rule.attributeName === originalName ? name : rule.attributeName,
      condition: originalName && originalName !== name
        ? replaceFormulaVariable(rule.condition ?? '', originalName, name)
        : rule.condition,
      formula: originalName && originalName !== name
        ? replaceFormulaVariable(rule.formula, originalName, name)
        : rule.formula,
    }));
    const unrelatedTimers = renamedTimers.filter((rule) => rule.attributeName !== name);
    state.timerRules = [...unrelatedTimers, ...normalizedTimers];
    for (const item of normalizedRules) upsertGiftCatalog(state, item.gift);
    try {
      await saveAndWait();
    } catch {
      saveButton.disabled = false;
      saveButton.textContent = original ? '保存修改' : '创建属性';
      return;
    }
    overlay.remove();
    editorOpen = false;
    editorGuideEnabled = false;
    refreshOpenGiftCatalog = null;
    render();
    toast(index === undefined ? '属性已创建' : '属性配置已保存', root);
  }

  function renderAdvancedSettings(): void {
    const details = el('details', { class: 'advanced-settings' });
    details.append(el('summary', {}, [
      el('span', { text: '外观与数据' }),
      el('small', { text: '面板外观、自动更新和配置备份' }),
    ]));
    const settingsGrid = el('div', { class: 'advanced-settings-grid' });

    const appearance = el('section', { class: 'workspace-card advanced-card' });
    appearance.append(el('h3', { text: 'OBS 面板外观' }));

    const rangeSetting = (
      label: string,
      value: number,
      min: number,
      max: number,
      unit: string,
      commit: (next: number) => void,
    ): HTMLElement => {
      const output = el('output', { class: 'setting-value', text: `${value}${unit}` });
      const range = el('input', {
        class: 'setting-range',
        type: 'range',
        value: String(value),
      }) as HTMLInputElement;
      range.dataset.fieldLabel = label;
      range.setAttribute('min', String(min));
      range.setAttribute('max', String(max));
      range.setAttribute('step', '1');
      const updateVisual = (): number => {
        const next = Math.min(max, Math.max(min, Number(range.value) || value));
        output.textContent = `${next}${unit}`;
        range.style.setProperty('--range-progress', `${((next - min) / (max - min)) * 100}%`);
        return next;
      };
      range.oninput = () => { updateVisual(); };
      range.onchange = () => {
        const next = updateVisual();
        range.value = String(next);
        commit(next);
        save();
      };
      updateVisual();
      return el('label', { class: 'field setting-control range-setting' }, [
        el('span', { class: 'setting-control-head' }, [el('span', { class: 'field-label', text: label }), output]),
        range,
      ]);
    };

    const fontSize = rangeSetting('字体大小（px）', state.settings.fontSize, 24, 96, ' px', (next) => {
      state.settings.fontSize = next;
    });

    const accentValue = el('output', { class: 'setting-value color-value', text: state.settings.accentColor.toUpperCase() });
    const accent = el('input', { class: 'setting-color-input', type: 'color', value: state.settings.accentColor }) as HTMLInputElement;
    accent.dataset.fieldLabel = '强调色';
    accent.oninput = () => { accentValue.textContent = accent.value.toUpperCase(); };
    accent.onchange = () => {
      state.settings.accentColor = accent.value;
      accentValue.textContent = accent.value.toUpperCase();
      save();
    };
    const accentControl = el('label', { class: 'field setting-control color-setting' }, [
      el('span', { class: 'setting-control-head' }, [el('span', { class: 'field-label', text: '强调色' }), accentValue]),
      el('span', { class: 'color-picker-row' }, [accent, el('span', { class: 'color-picker-copy', text: '点击色块选择颜色' })]),
    ]);

    const alignControl = el('fieldset', { class: 'field setting-control alignment-setting' });
    alignControl.append(el('legend', { class: 'field-label', text: '对齐' }));
    const alignOptions = el('div', { class: 'alignment-control', role: 'group', ariaLabel: 'OBS 面板对齐方式' });
    const alignments: Array<{ value: AppState['settings']['align']; label: string }> = [
      { value: 'left', label: '左对齐' },
      { value: 'center', label: '居中' },
      { value: 'right', label: '右对齐' },
    ];
    for (const option of alignments) {
      const button = el('button', {
        class: `alignment-option${state.settings.align === option.value ? ' is-active' : ''}`,
        type: 'button',
        text: option.label,
        ariaPressed: String(state.settings.align === option.value),
      }) as HTMLButtonElement;
      button.onclick = () => {
        state.settings.align = option.value;
        for (const candidate of Array.from(alignOptions.querySelectorAll('.alignment-option'))) {
          const active = candidate === button;
          candidate.classList.toggle('is-active', active);
          candidate.setAttribute('aria-pressed', String(active));
        }
        save();
      };
      alignOptions.append(button);
    }
    alignControl.append(alignOptions);

    const panelOpacity = rangeSetting('面板透明度（%）', state.settings.panelOpacity, 10, 100, '%', (next) => {
      state.settings.panelOpacity = next;
    });

    const showConnectionInput = el('input', { class: 'setting-switch-input', type: 'checkbox' }) as HTMLInputElement;
    showConnectionInput.checked = state.settings.showConnection;
    showConnectionInput.onchange = () => {
      state.settings.showConnection = showConnectionInput.checked;
      save();
    };
    const showConnection = el('label', { class: 'setting-switch' }, [
      showConnectionInput,
      el('span', { class: 'setting-switch-track', ariaHidden: 'true' }),
      el('span', { class: 'setting-switch-copy' }, [
        el('strong', { text: '显示连接状态' }),
        el('small', { text: '在 OBS 属性面板中显示当前连接状态' }),
      ]),
    ]);
    appearance.append(
      fontSize,
      accentControl,
      alignControl,
      panelOpacity,
      showConnection,
    );

    const dataCard = el('section', { class: 'workspace-card advanced-card data-settings-card' });
    dataCard.append(
      el('h3', { text: '配置与数据' }),
      el('p', { class: 'advanced-copy', text: `当前有 ${state.attributes.length} 个属性、${state.rules.length} 条礼物规则、${state.timerRules.length} 个定时器和 ${state.log.length} 条变动记录。` }),
    );
    const exportButton = el('button', { class: 'btn', type: 'button', text: '导出配置' }) as HTMLButtonElement;
    exportButton.onclick = () => {
      const blob = new Blob([JSON.stringify(state, null, 2)], { type: 'application/json' });
      const url = URL.createObjectURL(blob);
      const link = el('a', { href: url, download: `gift-panel-config-${new Date().toISOString().slice(0, 10)}.json` }) as HTMLAnchorElement;
      link.click();
      URL.revokeObjectURL(url);
    };
    const importInput = el('input', { type: 'file', accept: '.json' }) as HTMLInputElement;
    importInput.hidden = true;
    importInput.onchange = () => {
      const file = importInput.files?.[0];
      if (!file) return;
      void file.text().then((text) => {
        let parsed: Partial<AppState>;
        try {
          parsed = JSON.parse(text) as Partial<AppState>;
        } catch {
          toast('文件解析失败', root);
          return;
        }
        if (!validImportedState(parsed)) {
          toast('配置文件格式不正确', root);
          return;
        }
        state = {
          ...state,
          ...parsed,
          settings: { ...state.settings, ...(parsed.settings ?? {}) },
          attributes: parsed.attributes ?? state.attributes,
          rules: parsed.rules ?? state.rules,
          timerRules: parsed.timerRules ?? state.timerRules,
          formulaPresets: parsed.formulaPresets ?? state.formulaPresets,
        };
        state.settings.theme = normalizeConfigTheme(state.settings.theme);
        applyConfigTheme(state.settings.theme);
        save();
        render();
        toast('配置已导入', root);
      });
    };
    const importButton = el('button', { class: 'btn ghost', type: 'button', text: '导入配置' }) as HTMLButtonElement;
    importButton.onclick = () => importInput.click();
    const resetButton = el('button', { class: 'btn text-danger', type: 'button', text: '恢复默认' }) as HTMLButtonElement;
    resetButton.onclick = () => {
      if (!confirm('确定恢复默认设置？当前配置将被清除。')) return;
      resetState();
      location.reload();
    };
    dataCard.append(el('div', { class: 'data-actions' }, [exportButton, importButton, importInput, resetButton]));

    const updateCard = el('section', { class: 'data-update-section update-settings-card' });
    const versionText = el('strong', { class: 'update-current-version', text: '读取中…' });
    const stateBadge = el('span', { class: 'update-state-badge', text: '读取中' });
    const statusMessage = el('p', { class: 'advanced-copy update-status-message', text: currentUpdateStatus.message });
    const progressTrack = el('div', { class: 'update-progress-track', ariaLabel: '更新下载进度' } as any);
    const progressBar = el('span', { class: 'update-progress-bar' });
    progressTrack.append(progressBar);
    const autoUpdateInput = el('input', { class: 'setting-switch-input', type: 'checkbox' }) as HTMLInputElement;
    autoUpdateInput.checked = state.settings.autoUpdate;
    autoUpdateInput.onchange = () => {
      state.settings.autoUpdate = autoUpdateInput.checked;
      currentUpdateStatus = { ...currentUpdateStatus, autoUpdate: autoUpdateInput.checked };
      refreshUpdateCard?.();
      void saveAndWait().then(refreshUpdateStatus).catch(() => undefined);
    };
    const autoUpdateControl = el('label', { class: 'setting-switch update-auto-switch' }, [
      autoUpdateInput,
      el('span', { class: 'setting-switch-track', ariaHidden: 'true' }),
      el('span', { class: 'setting-switch-copy' }, [
        el('strong', { text: '自动更新' }),
        el('small', { text: '启动时和每 6 小时检查；下载完成后，在退出后台程序时安装。' }),
      ]),
    ]);
    const checkUpdateButton = el('button', { class: 'btn update-check-button', type: 'button', text: '检查更新' }) as HTMLButtonElement;
    checkUpdateButton.onclick = () => { void runManualUpdateCheck(); };
    const lastChecked = el('small', { class: 'update-last-checked', text: '尚未检查' });
    const updateActions = el('div', { class: 'update-actions' }, [checkUpdateButton, lastChecked]);
    updateCard.append(
      el('div', { class: 'update-heading' }, [
        el('div', {}, [el('h3', { text: '程序更新' }), el('span', { class: 'update-version-label', text: '当前版本' }), versionText]),
        stateBadge,
      ]),
      statusMessage,
      progressTrack,
      autoUpdateControl,
      updateActions,
    );

    const updateStateLabels: Record<UpdateStatus['state'], string> = {
      idle: '未检查',
      disabled: '自动更新已关闭',
      development: '开发版本',
      unsupported: '不支持',
      checking: '检查中',
      downloading: '下载中',
      ready: '等待安装',
      'up-to-date': '已是最新',
      error: '检查失败',
    };
    const syncUpdateCard = (): void => {
      const updateState = currentUpdateStatus.state;
      updateCard.dataset.updateState = updateState;
      versionText.textContent = currentUpdateStatus.currentVersion
        ? currentUpdateStatus.currentVersion === 'dev' ? 'dev' : `v${currentUpdateStatus.currentVersion}`
        : '读取中…';
      stateBadge.className = `update-state-badge is-${updateState}`;
      stateBadge.textContent = updateStateLabels[updateState];
      statusMessage.textContent = currentUpdateStatus.message;
      const progress = Math.min(100, Math.max(0, currentUpdateStatus.progress ?? 0));
      progressTrack.hidden = updateState !== 'downloading';
      progressBar.style.width = `${progress}%`;
      autoUpdateInput.checked = state.settings.autoUpdate;
      checkUpdateButton.disabled = updateState === 'checking' || updateState === 'downloading' || updateState === 'ready';
      checkUpdateButton.textContent = updateState === 'checking'
        ? '正在检查…'
        : updateState === 'downloading'
          ? '正在下载…'
          : updateState === 'ready'
            ? '更新已下载'
            : '检查更新';
      lastChecked.textContent = currentUpdateStatus.lastCheckedAt
        ? `上次检查：${new Date(currentUpdateStatus.lastCheckedAt * 1000).toLocaleString('zh-CN')}`
        : '尚未检查';
    };
    refreshUpdateCard = syncUpdateCard;
    syncUpdateCard();

    dataCard.append(updateCard);
    settingsGrid.append(appearance, dataCard);
    details.append(settingsGrid);
    content.append(details);
  }

  function renderFormulaHelp(attributeName: string): HTMLElement {
    const details = el('details', { class: 'formula-help' }) as HTMLDetailsElement;
    const current = attributeName || '属性';
    details.append(
      el('summary', { text: '规则怎么用？查看完整说明' }),
      el('div', { class: 'formula-help-content' }, [
        el('p', { text: '等号右侧的计算结果会成为属性的新值。要在原值上增加或减少，规则中必须写上当前属性名；只写数字会直接把属性设成该数字。' }),
        el('div', { class: 'formula-help-grid' }, [
          formulaHelpBlock('变量', [
            ['price', '当前单个礼物的价格'],
            [current, '触发前的当前属性值'],
            ['其他属性名', '可读取其他属性当前值'],
          ]),
          formulaHelpBlock('运算与函数', [
            ['+  -  *  /  ( )', '基础四则运算与括号'],
            ['IF(条件,A,B)', '按条件选择结果'],
            ['MIN / MAX', '限制最小值或最大值'],
            ['ROUND / ABS', '四舍五入或取绝对值'],
            ['RAND()', '生成 0 到 1 之间的随机数'],
            ['RANDBETWEEN(A,B)', '生成 A 到 B 的随机整数'],
          ]),
        ]),
        el('div', { class: 'formula-help-examples' }, [
          el('code', { text: `${current}+60` }), el('span', { text: '在当前值上增加 60' }),
          el('code', { text: `${current}+price/1000*60` }), el('span', { text: '每 1000 金瓜子增加 60' }),
          el('code', { text: `MIN(${current}+60,3600)` }), el('span', { text: '增加 60，但最大不超过 3600' }),
          el('code', { text: `IF(price>=1000,${current}+60,${current}+10)` }), el('span', { text: '按礼物价格选择增加量' }),
        ]),
        el('p', { class: 'formula-help-note', text: '连送会拆成单个礼物逐次执行：例如一次连送 3 个，就依次计算 3 次。' }),
      ]),
    );
    return details;
  }

  function formulaHelpBlock(title: string, rows: Array<[string, string]>): HTMLElement {
    const block = el('section', { class: 'formula-help-block' }, [el('strong', { text: title })]);
    for (const [syntax, description] of rows) {
      block.append(el('div', { class: 'formula-help-row' }, [
        el('code', { text: syntax }),
        el('span', { text: description }),
      ]));
    }
    return block;
  }

  applyConfigTheme(state.settings.theme);
  render();
  void refreshRuntime();
  void refreshBiliAuth();
  void refreshRoomGiftCatalog();
  void refreshUpdateStatus();
  const pollTimer = globalThis.setInterval(() => {
    void refreshRuntime();
    void refreshBackendState();
  }, 1000);
  const authPollTimer = globalThis.setInterval(() => void refreshBiliAuth(), 10000);
  const updatePollTimer = globalThis.setInterval(() => void refreshUpdateStatus(), 5000);
  const disposePolling = (): void => {
    globalThis.clearInterval(pollTimer);
    globalThis.clearInterval(authPollTimer);
    globalThis.clearInterval(updatePollTimer);
    if (loginPollTimer !== undefined) globalThis.clearInterval(loginPollTimer);
  };
  if (typeof globalThis.addEventListener === 'function') globalThis.addEventListener('beforeunload', disposePolling, { once: true });
}

function giftPickerGifts(state: AppState, roomGiftCatalog: GiftInfo[]): GiftInfo[] {
  const configuredGifts = state.rules
    .map((rule) => findGift(state, rule.giftId))
    .filter((gift): gift is GiftInfo => gift !== undefined);
  if (roomGiftCatalog.length > 0) {
    const seen = new Set<string>();
    const hasListingStatus = roomGiftCatalog.some((gift) => typeof gift.listed === 'boolean');
    const knownGifts = [...configuredGifts, ...state.recentGifts, ...builtinCatalog]
      .map((gift) => hasListingStatus ? { ...gift, listed: false } : gift);
    const currentGifts = [...roomGiftCatalog, ...knownGifts].filter((gift) => {
      const key = giftDisplayKey(gift);
      if (seen.has(key)) return false;
      seen.add(key);
      return true;
    });
    return sortGiftsByUsage(currentGifts, configuredGifts, state.recentGifts);
  }
  const seen = new Set<string>();
  const fallbackGifts = [...configuredGifts, ...state.recentGifts, ...builtinCatalog].filter((gift) => {
    const key = giftDisplayKey(gift);
    if (seen.has(key)) return false;
    seen.add(key);
    return true;
  });
  return sortGiftsByUsage(fallbackGifts, configuredGifts, state.recentGifts);
}

function configStructureSignature(state: AppState): string {
  return JSON.stringify({
    roomId: state.roomId,
    attributes: state.attributes.map(({ value: _value, ...attribute }) => attribute),
    rules: state.rules,
    timerRules: state.timerRules,
    formulaPresets: state.formulaPresets,
    settings: state.settings,
    giftCatalog: state.giftCatalog,
    log: state.log,
  });
}

function formatHistoryValue(value: number, attribute?: Attribute): string {
  if (attribute) return formatValue(value, attribute);
  return value.toLocaleString('zh-CN', { maximumFractionDigits: 4 });
}

function formatHistoryDelta(delta: number, attribute?: Attribute): string {
  const sign = delta > 0 ? '+' : delta < 0 ? '-' : '';
  return `${sign}${formatHistoryValue(Math.abs(delta), attribute)}`;
}

function formatHistorySummaryDelta(delta: number, attribute?: Attribute): string {
  if (Math.abs(delta) < 1_000_000) return formatHistoryDelta(delta, attribute);
  const sign = delta > 0 ? '+' : delta < 0 ? '-' : '';
  const unit = attribute?.format === 'suffix' && attribute.suffix
    ? ` ${attribute.suffix}`
    : attribute?.unit === 'seconds'
      ? ' 秒'
      : '';
  return `${sign}${Math.abs(delta).toExponential(2)}${unit}`;
}

function attributeValueElement(attribute: Attribute): HTMLElement {
  const value = el('strong', { class: 'attribute-current-value', text: formatValue(attribute.value, attribute) });
  value.dataset.attributeName = attribute.name;
  return value;
}

function upsertGiftCatalog(state: AppState, gift: GiftInfo): void {
  const index = state.giftCatalog.findIndex((item) => item.id === gift.id);
  if (index >= 0) state.giftCatalog[index] = { ...gift };
  else state.giftCatalog.push({ ...gift });
}

function ensureRuleGiftCatalog(state: AppState): boolean {
  let changed = false;
  for (const rule of state.rules) {
    if (state.giftCatalog.some((gift) => gift.id === rule.giftId)) continue;
    const gift = findGift(state, rule.giftId);
    if (!gift) continue;
    state.giftCatalog.push({ ...gift });
    changed = true;
  }
  return changed;
}

function displayFormatLabel(attribute: Attribute): string {
  if (attribute.format === 'hhmmss') return '计时器';
  if (attribute.format === 'suffix') return `数字${attribute.suffix ? ` · ${attribute.suffix}` : ' + 后缀'}`;
  return '纯数字';
}

function giftPriceLabel(gift: GiftInfo): string {
  return `${gift.price} ${gift.coinType === 'gold' ? '金瓜子' : '银瓜子'}`;
}

function emptyState(text: string): HTMLElement {
  const empty = el('div', { class: 'empty' });
  empty.append(createBrandIcon(44, 'empty-brand-icon'), el('span', { text }));
  return empty;
}

function transparentPixel(): string {
  return 'data:image/gif;base64,R0lGODlhAQABAIAAAAAAAP///yH5BAEAAAAALAAAAAABAAEAAAIBRAA7';
}

function createRuleId(): string {
  return `r-${Date.now()}-${Math.random().toString(36).slice(2, 7)}`;
}

function createTimerRuleId(): string {
  return `t-${Date.now()}-${Math.random().toString(36).slice(2, 7)}`;
}

function splitInterval(intervalSeconds: number): { value: number; multiplier: 1 | 60 | 3600 } {
  const seconds = Math.max(1, Math.floor(intervalSeconds || 1));
  if (seconds % 3600 === 0) return { value: seconds / 3600, multiplier: 3600 };
  if (seconds % 60 === 0) return { value: seconds / 60, multiplier: 60 };
  return { value: seconds, multiplier: 1 };
}

function formatInterval(intervalSeconds: number): string {
  const interval = splitInterval(intervalSeconds);
  if (interval.multiplier === 3600) return `${interval.value} 小时`;
  if (interval.multiplier === 60) return `${interval.value} 分钟`;
  return `${interval.value} 秒`;
}

function validImportedState(parsed: Partial<AppState> | null): parsed is Partial<AppState> {
  return parsed !== null
    && typeof parsed === 'object'
    && !Array.isArray(parsed)
    && (parsed.attributes === undefined || Array.isArray(parsed.attributes))
    && (parsed.rules === undefined || Array.isArray(parsed.rules))
    && (parsed.timerRules === undefined || Array.isArray(parsed.timerRules))
    && (parsed.formulaPresets === undefined || Array.isArray(parsed.formulaPresets))
    && (parsed.settings === undefined || (typeof parsed.settings === 'object' && parsed.settings !== null));
}

function normalizeConfigTheme(theme: unknown): 'dark' | 'light' {
  return theme === 'light' ? 'light' : 'dark';
}
