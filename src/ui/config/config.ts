import { AppState, Attribute, AttributeDisplay, AttributeValueMapping, DisplayScene, DisplaySceneLayout, DisplayThemeId, FormulaPresetContext, GiftInfo, GiftRule, LogEntry, MAX_LOG, TimerRule, TutorialLesson, ViewerContribution } from '../../types';
import { clearRoomScopedRecords, consumeConfigMigrationRequired, createConfigBackup, loadState, mergeConfigBackup, refreshStateFromServer, resetState, saveState } from '../../storage';
import { applyFormulaPreset, replaceFormulaVariable, saveFormulaPreset } from '../../formula-presets';
import { bindFloatingDetailCard, el, fieldControl, inputField, setFloatingDetailGuideExpanded, toast } from '../common';
import { builtinCatalog, findGift, giftDisplayKey, matchesGiftSearch, sortGiftsByUsage } from '../../gifts/catalog';
import { giftPriceDescription, isSpecialEventGift } from '../../gifts/special-events';
import { formatValue } from '../../format';
import {
  BiliAuthStatus,
  checkForUpdates,
  clearContributionLedger,
  getBlindBoxInfo,
  getBiliAuthStatus,
  getHostedChangelog,
  getRoomAnchorInfo,
  getRoomGiftCatalog,
  getRuntimeStatus,
  getUpdateStatus,
  logoutBiliAuth,
  pollBiliQRCodeLogin,
  previewFormula,
  RuntimeConnectionState,
  RoomAnchorInfo,
  startBiliQRCodeLogin,
  UpdateStatus,
} from '../../backend';
import { createBrandIcon } from '../brand';
import {
  getTutorialLesson,
  getTutorialLessonStates,
  markTutorialLessonComplete,
  resetTutorialProgress,
  sectionForTutorialLesson,
  TUTORIAL_LESSONS,
  type TutorialEditorProgress,
} from './wizard';
import { renderSpotlightGuide, type SpotlightGuideElement } from './spotlight-guide';
import { createAttributeWorkspace, type AttributeWorkspace } from './attribute-workspace';
import {
  buildQuickGiftFormula,
  detectQuickGiftRule,
  QUICK_GIFT_OPERATION_GROUPS,
  quickGiftOperationLabel,
  quickGiftOperationSupportsMaximum,
  quickGiftOperationUnit,
  quickGiftOperationUsesAmount,
  type QuickGiftOperation,
} from './quick-gift-rules';
import { createGameplayTemplateWizard } from './template-wizard';
import type { GameplayTemplateBuildResult } from '../../gameplay-templates';
import { DISPLAY_THEMES, getDisplayTheme } from '../../display-themes';
import { blindBoxDisplayUrl, createDisplaySceneId, displaySceneUrl, MAX_DISPLAY_SCENE_ATTRIBUTES } from '../../display-scenes';
import { buildBlindBoxLeaderboard } from '../../blind-box-leaderboard';
import { createActivityWorkspace } from './activity-workspace';
import { createTrainingCenter } from './training-center';
import type { TrainingTopicDefinition } from '../../training';
import {
  CHANGELOG_RELEASES,
  changelogReleaseForVersion,
  latestChangelogRelease,
  mergeChangelogReleases,
  shouldShowChangelog,
  type ChangelogRelease,
} from '../../changelog';
import { createChangelogDialog } from './changelog-dialog';

interface SelectedGiftRule {
  gift: GiftInfo;
  formulaName: string;
  formula: string;
  enabled: boolean;
  quickOperation?: QuickGiftOperation;
  quickAmount?: number;
  quickMaximum?: number;
  quickMaximumEnabled?: boolean;
  previous?: GiftRule;
  matchGiftIds?: number[];
  blindBoxName?: string;
  blindBoxStatus?: 'matched' | 'login-required' | 'not-blind-box' | 'error';
  simulationPreview?: { currentValue: number; result: number };
}

type GiftAvailability = 'special' | 'listed' | 'observed' | 'historical';
type LeaderboardMode = 'contribution' | 'rules' | 'blind-box';

interface GiftPickerCatalog {
  gifts: GiftInfo[];
  availabilityById: Map<number, GiftAvailability>;
  hasLiveListingStatus: boolean;
}

type HeaderActionIcon = 'training' | 'changelog' | 'sun' | 'moon';

function createHeaderActionIcon(kind: HeaderActionIcon): HTMLElement {
  const namespace = 'http://www.w3.org/2000/svg';
  const createSvgElement = (tag: 'svg' | 'path'): SVGElement => (
    typeof document.createElementNS === 'function'
      ? document.createElementNS(namespace, tag)
      : document.createElement(tag) as unknown as SVGElement
  );
  const paths: Record<HeaderActionIcon, string[]> = {
    training: [
      'M8 3h8v3H8z',
      'M6 4H5a2 2 0 0 0-2 2v14a2 2 0 0 0 2 2h14a2 2 0 0 0 2-2V6a2 2 0 0 0-2-2h-1',
      'm7 13 2 2 4-4',
    ],
    changelog: [
      'M3 12a9 9 0 1 0 3-6.7L3 8',
      'M3 3v5h5',
      'M12 7v5l3 2',
    ],
    sun: [
      'M12 17a5 5 0 1 0 0-10 5 5 0 0 0 0 10',
      'M12 2v2M12 20v2M4.93 4.93l1.42 1.42M17.65 17.65l1.42 1.42M2 12h2M20 12h2M4.93 19.07l1.42-1.42M17.65 6.35l1.42-1.42',
    ],
    moon: [
      'M20.5 14.2A8.5 8.5 0 0 1 9.8 3.5 8.7 8.7 0 1 0 20.5 14.2Z',
    ],
  };
  const svg = createSvgElement('svg') as SVGSVGElement;
  svg.setAttribute('class', 'header-action-icon');
  svg.setAttribute('viewBox', '0 0 24 24');
  svg.setAttribute('aria-hidden', 'true');
  svg.setAttribute('focusable', 'false');
  for (const data of paths[kind]) {
    const path = createSvgElement('path');
    path.setAttribute('d', data);
    path.setAttribute('fill', 'none');
    path.setAttribute('stroke', 'currentColor');
    path.setAttribute('stroke-width', '1.8');
    path.setAttribute('stroke-linecap', 'round');
    path.setAttribute('stroke-linejoin', 'round');
    svg.append(path);
  }
  return svg as unknown as HTMLElement;
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
  let forcedTutorialLesson: TutorialLesson | null = null;
  let editorTutorialProgress: TutorialEditorProgress = { open: false };
  let activeEditorWorkspace: AttributeWorkspace | null = null;
  let runtimeRefreshPromise: Promise<void> | null = null;
  let stateRefreshActive = false;
  let authRefreshActive = false;
  let roomGiftCatalogRefreshActive = false;
  let roomGiftCatalogRoomId = '';
  let roomGiftCatalog: GiftInfo[] = [];
  let roomAnchorInfo: RoomAnchorInfo | null = null;
  let roomAnchorInfoRoomId = '';
  let roomAnchorRequestVersion = 0;
  let refreshOpenGiftCatalog: (() => void) | null = null;
  let currentUpdateStatus: UpdateStatus = {
    state: 'idle', currentVersion: '', message: '正在读取版本信息…', autoUpdate: state.settings.autoUpdate, restartRequired: false,
  };
  let updateRefreshActive = false;
  let refreshUpdateCard: (() => void) | null = null;
  let changelogAutoEvaluated = false;
  let changelogOpen = false;
  let changelogReleases: ChangelogRelease[] = CHANGELOG_RELEASES;
  let advancedSettingsOpen = false;
  let loginModalOpen = false;
  let loginPollTimer: ReturnType<typeof globalThis.setInterval> | undefined;
  let localStateVersion = 0;
  let leaderboardMode: LeaderboardMode = 'contribution';

  const shell = el('div', { class: 'wizard-shell config-shell' });
  const header = el('header', { class: 'app-header' });
  const brand = el('div', { class: 'app-brand' });
  brand.append(createBrandIcon(40), el('div', { class: 'app-brand-copy' }, [
    el('strong', { text: '直播礼物面板' }),
  ]));
  const themeToggle = el('button', { class: 'theme-toggle config-theme-toggle', type: 'button' }) as HTMLButtonElement;
  const guideToggle = el('button', { class: 'theme-toggle training-toggle', type: 'button' }, [createHeaderActionIcon('training')]) as HTMLButtonElement;
  const changelogToggle = el('button', { class: 'theme-toggle changelog-toggle', type: 'button' }, [createHeaderActionIcon('changelog')]) as HTMLButtonElement;
  changelogToggle.setAttribute('aria-label', '更新日志');
  changelogToggle.setAttribute('title', '更新日志');
  const status = el('div', { class: 'app-status' });
  const headerActions = el('div', { class: 'app-header-actions' });
  headerActions.append(guideToggle, changelogToggle, themeToggle, status);
  header.append(brand, headerActions);

  const content = el('main', { class: 'wizard-content config-page' });
  shell.append(header, content);
  root.replaceChildren(shell);

  function applyConfigTheme(theme: 'dark' | 'light'): void {
    root.dataset.theme = theme;
    const label = theme === 'dark' ? '切换至亮色主题' : '切换至深色主题';
    themeToggle.replaceChildren(createHeaderActionIcon(theme === 'dark' ? 'sun' : 'moon'));
    themeToggle.setAttribute('aria-label', label);
    themeToggle.setAttribute('title', label);
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
    maybeOpenChangelog();
    return currentUpdateStatus;
  }

  async function refreshHostedChangelog(): Promise<void> {
    try {
      changelogReleases = mergeChangelogReleases(await getHostedChangelog());
    } catch {
      // The bundled current-version note remains available when GitHub is slow or unavailable.
    }
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

  function renderRoomAnchorHosts(): void {
    const currentRoomId = state.roomId.trim();
    for (const host of Array.from(root.querySelectorAll<HTMLElement>('.room-anchor-host'))) {
      host.replaceChildren();
      if (!roomAnchorInfo || roomAnchorInfoRoomId !== currentRoomId) continue;
      const avatar = roomAnchorInfo.avatar
        ? el('img', {
          class: 'room-anchor-avatar',
          src: roomAnchorInfo.avatar,
          alt: roomAnchorInfo.uname ? `${roomAnchorInfo.uname}的头像` : '主播头像',
          referrerPolicy: 'no-referrer',
        })
        : el('span', { class: 'room-anchor-avatar is-fallback', text: '主', ariaHidden: 'true' });
      host.append(
        avatar,
        el('div', { class: 'room-anchor-copy' }, [
          el('strong', { text: roomAnchorInfo.uname || '直播间主播' }),
          el('span', { text: `主播 UID ${roomAnchorInfo.uid}` }),
        ]),
      );
    }
  }

  async function refreshRoomAnchorInfo(force = false): Promise<void> {
    const requestedRoomId = state.roomId.trim();
    if (!requestedRoomId) {
      roomAnchorRequestVersion += 1;
      roomAnchorInfo = null;
      roomAnchorInfoRoomId = '';
      renderRoomAnchorHosts();
      return;
    }
    if (!force && roomAnchorInfoRoomId === requestedRoomId) return;
    const requestVersion = ++roomAnchorRequestVersion;
    try {
      const next = await getRoomAnchorInfo(requestedRoomId);
      if (requestVersion !== roomAnchorRequestVersion || state.roomId.trim() !== requestedRoomId) return;
      roomAnchorInfo = next;
      roomAnchorInfoRoomId = requestedRoomId;
    } catch {
      if (requestVersion !== roomAnchorRequestVersion || state.roomId.trim() !== requestedRoomId) return;
      roomAnchorInfo = null;
      roomAnchorInfoRoomId = requestedRoomId;
    }
    renderRoomAnchorHosts();
  }

  async function refreshBackendState(): Promise<void> {
    if (stateRefreshActive || editorOpen) return;
    stateRefreshActive = true;
    try {
      const previousStructure = configStructureSignature(state);
      const previousActivities = activityStateSignature(state);
      const previousContributions = contributionStateSignature(state);
      const previousGiftHistory = giftHistoryStateSignature(state);
      const requestedVersion = localStateVersion;
      const nextState = await refreshStateFromServer(() => requestedVersion === localStateVersion);
      if (requestedVersion !== localStateVersion) return;
      const previousRoomId = state.roomId.trim();
      state = nextState;
      if (state.roomId.trim() !== previousRoomId) {
        roomAnchorInfo = null;
        roomAnchorInfoRoomId = '';
        roomGiftCatalogRoomId = '';
        roomGiftCatalog = [];
        void refreshRoomAnchorInfo(true);
      }
      void refreshRoomGiftCatalog();
      if (ensureRuleGiftCatalog(state)) await saveState(state);
      if (configStructureSignature(state) !== previousStructure) {
        render();
        return;
      }
      if (activityStateSignature(state) !== previousActivities) renderActivities(true);
      syncLiveAttributeValues();
      if (contributionStateSignature(state) !== previousContributions) renderContributionLeaderboard(true);
      if (giftHistoryStateSignature(state) !== previousGiftHistory) renderGiftHistory(true);
    } finally {
      stateRefreshActive = false;
    }
  }

  function syncLiveAttributeValues(): void {
    const attributesByName = new Map(state.attributes.map((attribute) => [attribute.name, attribute]));
    for (const valueElement of Array.from(root.querySelectorAll<HTMLElement>('.attribute-live-value'))) {
      const attribute = attributesByName.get(valueElement.dataset.attributeName ?? '');
      if (attribute) valueElement.textContent = formatValue(attribute.value, attribute);
    }
    for (const card of Array.from(root.querySelectorAll<HTMLElement>('.attribute-card'))) {
      const attribute = attributesByName.get(card.dataset.attributeName ?? '');
      if (attribute) {
        card.setAttribute('aria-label', `属性“${attribute.name}”，当前值 ${formatValue(attribute.value, attribute)}。悬停或聚焦查看详细设置。`);
      }
    }
  }

  function appendOrReplaceSection(section: HTMLElement, selector: string, replaceExisting: boolean): void {
    const existing = replaceExisting ? content.querySelector<HTMLElement>(selector) : null;
    if (existing) existing.replaceWith(section);
    else content.append(section);
  }

  function save(): void {
    void saveAndWait().catch(() => undefined);
  }

  function isRoomSwitch(nextRoomId: string): boolean {
    const currentRoomId = state.roomId.trim();
    nextRoomId = nextRoomId.trim();
    return currentRoomId !== '' && nextRoomId !== '' && currentRoomId !== nextRoomId;
  }

  function confirmRoomSwitch(nextRoomId: string): boolean {
    if (!isRoomSwitch(nextRoomId)) return true;
    return confirm(
      `从房间 ${state.roomId.trim()} 切换到 ${nextRoomId.trim()}？\n\n`
      + '切换后会清空最近礼物、礼物统计、生效记录和观众排行榜；属性、规则和 OBS 链接会保留。',
    );
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
    openTrainingCenter();
  };
  changelogToggle.onclick = () => {
    openChangelog(currentUpdateStatus.currentVersion);
  };

  function activeTutorialLesson(): TutorialLesson | null {
    return forcedTutorialLesson ?? getTutorialLesson(
      state,
      connectionState === 'connected',
      editorTutorialProgress,
    );
  }

  function completeTutorialLesson(lesson: TutorialLesson): void {
    markTutorialLessonComplete(state.settings, lesson);
    if (forcedTutorialLesson === lesson) forcedTutorialLesson = null;
  }

  function updateTrainingToggle(): void {
    const lessons = getTutorialLessonStates(
      state,
      connectionState === 'connected',
      editorTutorialProgress,
      forcedTutorialLesson,
    );
    const done = lessons.filter((lesson) => lesson.done).length;
    const label = `训练任务：${done}/${lessons.length}`;
    guideToggle.setAttribute('aria-label', label);
    guideToggle.setAttribute('title', label);
    guideToggle.classList.toggle('is-complete', done === lessons.length);
  }

  function refreshEditorTutorial(navigate = true): void {
    const lesson = activeTutorialLesson();
    const trainingVisible = !guideDismissed && (editorGuideEnabled || forcedTutorialLesson !== null);
    const lessons = getTutorialLessonStates(
      state,
      connectionState === 'connected',
      editorTutorialProgress,
      forcedTutorialLesson,
    ).filter((item) => item.section);
    activeEditorWorkspace?.updateLessons(lessons);
    activeEditorWorkspace?.refreshBadges();
    activeEditorWorkspace?.setTrainingVisible(trainingVisible);
    root.querySelectorAll<HTMLElement>('.workbench-lesson-card').forEach((card) => {
      if (!card.dataset.tutorialLesson) return;
      card.hidden = !trainingVisible || card.dataset.tutorialLesson !== lesson;
    });
    if (trainingVisible && lesson === 'preset') {
      const advancedRule = root.querySelector<HTMLDetailsElement>('.rule-advanced-settings');
      if (advancedRule) advancedRule.open = true;
    }
    if (navigate && lesson && activeEditorWorkspace && (editorGuideEnabled || forcedTutorialLesson !== null)) {
      activeEditorWorkspace.setSection(sectionForTutorialLesson(lesson));
    }
    updateTrainingToggle();
    renderGuide(navigate);
  }

  function openTrainingCenter(): void {
    root.querySelector('.training-center-overlay')?.remove();
    activeGuide?.dispose();
    activeGuide = null;
    const lessons = getTutorialLessonStates(
      state,
      connectionState === 'connected',
      editorTutorialProgress,
      forcedTutorialLesson,
    );
    let overlay: HTMLElement;
    const close = (): void => {
      overlay.remove();
      renderGuide();
    };
    const beginLesson = (lesson: TutorialLesson): void => {
      const tutorialAttributeIndex = tutorialLessonRequiresAttribute(lesson)
        ? ensureTutorialAttributeTarget(state)
        : -1;
      if (tutorialLessonRequiresAttribute(lesson) && tutorialAttributeIndex < 0) {
        resetTutorialProgress(state.settings);
        forcedTutorialLesson = null;
        guideDismissed = false;
        save();
        overlay.remove();
        render();
        toast('还没有“加班时间”，已从创建属性步骤重新开始训练', root);
        return;
      }
      forcedTutorialLesson = lesson;
      guideDismissed = false;
      state.settings.showTutorial = true;
      state.settings.tutorialReplayMode = true;
      save();
      overlay.remove();
      if (lesson === 'template') {
        openGameplayTemplateWizard();
        return;
      }
      if (sectionForTutorialLesson(lesson) !== 'overview' || ['basics'].includes(lesson)) {
        if (!editorOpen) openAttributeEditor(tutorialAttributeIndex >= 0 ? tutorialAttributeIndex : undefined);
        else refreshEditorTutorial();
        return;
      }
      renderGuide();
    };
    const openTopic = (topic: TrainingTopicDefinition): void => {
      overlay.remove();
      if (topic.destination.kind === 'editor') {
        if (!editorOpen) openAttributeEditor(0);
        activeEditorWorkspace?.setSection(topic.destination.section);
        if (['multi-gift', 'blind-box', 'manual-gift'].includes(topic.id)) {
          const addGift = root.querySelector<HTMLButtonElement>('.guide-add-gift');
          if (typeof addGift?.click === 'function') addGift.click();
          else (addGift as any)?.onclick?.();
          if (topic.id === 'manual-gift') {
            const manualAdder = root.querySelector<HTMLDetailsElement>('.manual-gift-adder');
            if (manualAdder) manualAdder.open = true;
          }
        }
        if (['advanced-rule', 'cross-attribute'].includes(topic.id)) {
          const advanced = root.querySelector<HTMLDetailsElement>('.rule-advanced-settings');
          if (advanced) advanced.open = true;
        }
        return;
      }
      const target = root.querySelector<HTMLElement>(topic.destination.selector);
      if (!target) {
        toast('请先完成这项功能需要的基础配置', root);
        return;
      }
      target.classList.add('training-jump-target');
      if (typeof target.scrollIntoView === 'function') target.scrollIntoView({ behavior: 'smooth', block: 'start' });
      globalThis.setTimeout(() => target.classList.remove('training-jump-target'), 1800);
    };
    const resumeLesson = activeTutorialLesson() ?? 'room';
    overlay = createTrainingCenter({
      lessons,
      completedTopics: state.settings.trainingCompletedTopics,
      resumeLesson,
      hasAttribute: state.attributes.length > 0,
      onClose: close,
      onBeginLesson: beginLesson,
      onOpenTopic: openTopic,
      onTopicCompletionChange: (topic, complete) => {
        state.settings.trainingCompletedTopics = complete
          ? Array.from(new Set([...state.settings.trainingCompletedTopics, topic]))
          : state.settings.trainingCompletedTopics.filter((candidate) => candidate !== topic);
        save();
      },
      onReset: () => {
        resetTutorialProgress(state.settings);
        forcedTutorialLesson = null;
        guideDismissed = false;
        save();
        overlay.remove();
        render();
      },
    });
    root.append(overlay);
  }

  function openChangelog(version?: string): void {
    if (changelogOpen) return;
    root.querySelector('.changelog-overlay')?.remove();
    activeGuide?.dispose();
    activeGuide = null;
    changelogOpen = true;
    const release = changelogReleaseForVersion(version, changelogReleases) ?? latestChangelogRelease(changelogReleases);
    const overlay = createChangelogDialog({
      currentVersion: release.version,
      releases: changelogReleases,
      onClose: (seenVersion) => {
        changelogOpen = false;
        if (state.settings.lastSeenChangelogVersion !== seenVersion) {
          state.settings.lastSeenChangelogVersion = seenVersion;
          save();
        }
        renderGuide();
      },
    });
    root.append(overlay);
  }

  function maybeOpenChangelog(): void {
    if (changelogAutoEvaluated) return;
    const version = currentUpdateStatus.currentVersion;
    if (!version) return;
    if (version === 'dev' || !changelogReleaseForVersion(version, changelogReleases)) {
      changelogAutoEvaluated = true;
      return;
    }
    if (!shouldShowChangelog(version, state.settings.lastSeenChangelogVersion, changelogReleases)) {
      changelogAutoEvaluated = true;
      return;
    }
    if (editorOpen || loginModalOpen || changelogOpen || root.querySelector('.overlay')) return;
    changelogAutoEvaluated = true;
    openChangelog(version);
  }

  function renderGuide(navigate = true): void {
    activeGuide?.dispose();
    activeGuide = null;
    updateTrainingToggle();
    const lesson = guideDismissed ? null : activeTutorialLesson();
    root.querySelectorAll<HTMLElement>('.attribute-card').forEach((card) => {
      setFloatingDetailGuideExpanded(card, false);
    });
    if (!editorOpen && (lesson === 'enable' || lesson === 'output')) {
      const guideCard = root.querySelector<HTMLElement>('.guide-attribute-card')
        ?? root.querySelector<HTMLElement>('.attribute-card');
      if (guideCard) setFloatingDetailGuideExpanded(guideCard, true);
    }
    if (guideDismissed) return;
    if (!lesson) return;
    if (editorOpen && !editorGuideEnabled && forcedTutorialLesson === null) return;
    if (navigate && editorOpen && activeEditorWorkspace) {
      activeEditorWorkspace.setSection(sectionForTutorialLesson(lesson));
    }
    activeGuide = renderSpotlightGuide({
      host: root,
      lesson,
      editorOpen,
      onDismiss: () => {
        guideDismissed = true;
        editorGuideEnabled = false;
        forcedTutorialLesson = null;
        const guideCard = root.querySelector<HTMLElement>('.attribute-card.is-guide-expanded');
        if (guideCard) setFloatingDetailGuideExpanded(guideCard, false);
        state.settings.showTutorial = false;
        save();
        activeEditorWorkspace?.setTrainingVisible(false);
        const editorKicker = root.querySelector<HTMLElement>('.attribute-workbench-header .section-kicker');
        const editorTitle = root.querySelector<HTMLElement>('.attribute-workbench-header h2');
        if (editorKicker) editorKicker.textContent = '属性工作台';
        if (editorTitle?.textContent === '制作第一台加班机') editorTitle.textContent = '创建互动属性';
        activeGuide = null;
      },
      onSkipLesson: () => {
        markTutorialLessonComplete(state.settings, lesson);
        forcedTutorialLesson = null;
        save();
        refreshEditorTutorial();
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
    renderActivities();
    renderDisplayScenes();
    renderContributionLeaderboard();
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
    roomCard.append(sectionHeading('直播来源', '连接直播间', '输入房间号并连接，礼物目录会随着直播事件自动补充。'));

    const roomInput = inputField('房间号', state.roomId);
    roomInput.classList.add('guide-room-input');
    roomInput.placeholder = '例如 88888888';
    roomInput.inputMode = 'numeric';
    roomInput.oninput = () => undefined;
    const connectionText = el('span', { class: 'connection-inline-status', text: connectionLabel(connectionState) });
    const connectButton = el('button', { class: 'btn', type: 'button', text: '连接' }) as HTMLButtonElement;
    connectButton.onclick = async () => {
      const roomId = roomInput.value.trim();
      if (!roomId) {
        toast('请输入房间号', root);
        roomInput.focus();
        return;
      }
      if (!confirmRoomSwitch(roomId)) {
        roomInput.value = state.roomId;
        return;
      }
      const previousState = state;
      const connectionBeforeClick = connectionState;
      const switchingRooms = isRoomSwitch(roomId);
      state = switchingRooms ? clearRoomScopedRecords({ ...state, roomId }) : { ...state, roomId };
      if (switchingRooms) {
        roomGiftCatalogRoomId = '';
        roomGiftCatalog = [];
        roomAnchorInfo = null;
        roomAnchorInfoRoomId = '';
      }
      connectionState = 'connecting';
      connectionText.textContent = connectionLabel(connectionState);
      renderHeaderStatus();
      try {
        await saveAndWait();
        render();
        void refreshBiliAuth();
        void refreshRoomAnchorInfo(true);
        await refreshRuntime(true);
        void refreshRoomGiftCatalog(true);
      } catch {
        state = previousState;
        roomInput.value = previousState.roomId;
        connectionState = connectionBeforeClick;
        renderHeaderStatus();
        void refreshBackendState();
        void refreshRoomAnchorInfo(true);
        void refreshRoomGiftCatalog(true);
        // saveAndWait already reports the persistence error in the page.
      }
    };
    const roomAnchorHost = el('div', { class: 'room-anchor-host', ariaLive: 'polite' });
    roomCard.append(
      fieldControl(roomInput),
      el('div', { class: 'row connection-actions' }, [connectButton, connectionText]),
      roomAnchorHost,
      el('details', { class: 'inline-help' }, [
        el('summary', { text: '房间号在哪里？' }),
        el('p', { text: '直播地址 live.bilibili.com/88888888 中的 88888888 就是房间号，不要复制问号后的参数。' }),
      ]),
    );
    grid.append(roomCard, renderLoginCard());
    content.append(grid);
    renderRoomAnchorHosts();
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
    addButton.onclick = openGameplayTemplateWizard;
    headingRow.append(addButton);
    section.append(headingRow);

    if (state.attributes.length === 0) {
      const empty = emptyState('还没有属性。添加属性后，可以在同一个窗口里选择任意数量的礼物。');
      empty.classList.add('attribute-empty');
      section.append(empty);
    } else {
      const list = el('div', { class: 'attribute-list' });
      const tutorialAttributeIndex = findTutorialAttributeIndex(state);
      state.attributes.forEach((attribute, index) => list.append(renderAttributeCard(
        attribute,
        index,
        index === tutorialAttributeIndex,
      )));
      section.append(list);
    }
    content.append(section);
  }

  function openGameplayTemplateWizard(): void {
    activeGuide?.dispose();
    activeGuide = null;
    root.querySelector('.template-wizard-overlay')?.remove();
    const lessonBeforeOpen = activeTutorialLesson();
    const catalog = buildGiftPickerCatalog(state, roomGiftCatalog);
    editorOpen = true;
    editorGuideEnabled = !guideDismissed && (lessonBeforeOpen === 'attribute' || forcedTutorialLesson !== null);
    if (editorGuideEnabled) {
      editorTutorialProgress = { open: false, templateOpen: true, isNew: true };
    }
    const wizard = createGameplayTemplateWizard({
      gifts: catalog.gifts,
      existingAttributeNames: state.attributes.map((attribute) => attribute.name),
      onBlank: () => {
        if (forcedTutorialLesson === 'template') forcedTutorialLesson = null;
        openAttributeEditor();
      },
      onClose: (reason) => {
        if (reason === 'blank') return;
        editorOpen = false;
        editorGuideEnabled = false;
        editorTutorialProgress = { open: false };
        renderGuide();
      },
      onCreate: async (result) => {
        await createFromGameplayTemplate(result);
      },
    });
    root.append(wizard.element);
    renderGuide();
  }

  async function createFromGameplayTemplate(result: GameplayTemplateBuildResult): Promise<void> {
    for (const attribute of result.attributes) {
      if (state.attributes.some((candidate) => candidate.name === attribute.name)) {
        throw new Error(`已经存在名为“${attribute.name}”的属性`);
      }
    }
    for (const scene of result.displayScenes) {
      if (state.displayScenes.some((candidate) => candidate.name.toLowerCase() === scene.name.toLowerCase())) {
        throw new Error(`已经存在名为“${scene.name}”的组合面板`);
      }
    }
    for (const activity of result.activities) {
      if (state.activities.some((candidate) => candidate.name.toLowerCase() === activity.name.toLowerCase())) {
        throw new Error(`已经存在名为“${activity.name}”的活动会话`);
      }
    }
    const createdAttributes = result.attributes.map((attribute) => ({
      ...attribute,
      id: attribute.id ?? createAttributeId(),
    }));
    const attributesByName = new Map(createdAttributes.map((attribute) => [attribute.name, attribute]));
    const giftsById = new Map(result.usedGifts.map((gift) => [gift.id, gift]));
    try {
      for (const rule of result.rules) {
        const attribute = attributesByName.get(rule.attributeName);
        if (!attribute) throw new Error(`规则引用了不存在的属性“${rule.attributeName}”`);
        await previewFormula(rule.formula, attribute.name, attribute.value, 'gift', giftsById.get(rule.giftId)?.price);
      }
      for (const timer of result.timerRules) {
        const attribute = attributesByName.get(timer.attributeName);
        if (!attribute) throw new Error(`定时器引用了不存在的属性“${timer.attributeName}”`);
        if (timer.condition) await previewFormula(timer.condition, attribute.name, attribute.value, 'timer');
        await previewFormula(timer.formula, attribute.name, attribute.value, 'timer');
      }
    } catch (error) {
      const reason = error instanceof Error ? error.message : '未知错误';
      throw new Error(`模板规则校验失败：${reason}`);
    }

    const previous = {
      attributes: state.attributes,
      rules: state.rules,
      timerRules: state.timerRules,
      displayScenes: state.displayScenes,
      activities: state.activities,
      giftCatalog: state.giftCatalog,
      tutorialTargetAttributeId: state.settings.tutorialTargetAttributeId,
    };
    state.attributes = [...state.attributes, ...createdAttributes];
    state.rules = [...state.rules, ...result.rules];
    state.timerRules = [...state.timerRules, ...result.timerRules];
    state.displayScenes = [...state.displayScenes, ...result.displayScenes];
    state.activities = [...state.activities, ...result.activities];
    state.giftCatalog = [...state.giftCatalog];
    for (const gift of result.usedGifts) upsertGiftCatalog(state, gift);
    if (editorGuideEnabled || state.settings.tutorialReplayMode) {
      const overtimeAttribute = createdAttributes.find((attribute) => (
        attribute.createdFromTemplateId === 'overtime' || attribute.name === '加班时间'
      ));
      if (overtimeAttribute?.id) state.settings.tutorialTargetAttributeId = overtimeAttribute.id;
    }
    try {
      await saveAndWait();
    } catch (error) {
      state.attributes = previous.attributes;
      state.rules = previous.rules;
      state.timerRules = previous.timerRules;
      state.displayScenes = previous.displayScenes;
      state.activities = previous.activities;
      state.giftCatalog = previous.giftCatalog;
      state.settings.tutorialTargetAttributeId = previous.tutorialTargetAttributeId;
      throw error;
    }
    editorOpen = false;
    render();
    toast(`已创建“${result.attributes.map((attribute) => attribute.name).join('、')}”玩法`, root);
  }

  function renderAttributeCard(attribute: Attribute, index: number, isTutorialTarget: boolean): HTMLElement {
    const rules = state.rules.filter((rule) => rule.attributeName === attribute.name);
    const timerRules = state.timerRules.filter((rule) => rule.attributeName === attribute.name);
    const card = el('article', {
      class: `attribute-card hover-detail-card${isTutorialTarget ? ' guide-attribute-card' : ''}`,
      tabIndex: 0,
      ariaLabel: `属性“${attribute.name}”，当前值 ${formatValue(attribute.value, attribute)}。悬停或聚焦查看详细设置。`,
    } as any);
    card.dataset.attributeName = attribute.name;
    const editButton = el('button', { class: `btn ghost attribute-action-button${index === 0 ? ' guide-attribute-edit' : ''}`, type: 'button', text: '编辑' }) as HTMLButtonElement;
    editButton.onclick = () => openAttributeEditor(index);
    const deleteButton = el('button', { class: 'btn text-danger attribute-action-button', type: 'button', text: '删除' }) as HTMLButtonElement;
    deleteButton.onclick = () => {
      if (!confirm(`删除属性“${attribute.name}”及其全部触发规则？`)) return;
      if (attribute.id && state.settings.tutorialTargetAttributeId === attribute.id) {
        delete state.settings.tutorialTargetAttributeId;
      }
      state.attributes.splice(index, 1);
      state.rules = state.rules.filter((rule) => rule.attributeName !== attribute.name);
      state.timerRules = state.timerRules.filter((rule) => rule.attributeName !== attribute.name);
      state.activities = state.activities.flatMap((activity) => {
        const attributeNames = activity.attributeNames.filter((name) => name !== attribute.name);
        if (attributeNames.length === 0) return [];
        const initialValues = { ...activity.initialValues };
        delete initialValues[attribute.name];
        const resultValues = { ...(activity.result?.values ?? {}) };
        delete resultValues[attribute.name];
        const result = activity.result
          ? {
            values: resultValues,
            ...(activity.result.winnerAttributeName && activity.result.winnerAttributeName !== attribute.name
              ? { winnerAttributeName: activity.result.winnerAttributeName }
              : {}),
          }
          : undefined;
        return [{
          ...activity,
          attributeNames,
          initialValues,
          milestones: activity.milestones.filter((milestone) => milestone.attributeName !== attribute.name),
          ...(result ? { result } : {}),
        }];
      });
      state.displayScenes = state.displayScenes.flatMap((scene) => {
        const attributeNames = scene.attributeNames.filter((name) => name !== attribute.name);
        return attributeNames.length > 0 ? [{ ...scene, attributeNames }] : [];
      });
      save();
      render();
      toast('属性已删除', root);
    };
    const mappedImage = attribute.display?.valueMappings
      ?.find((mapping) => mapping.value === attribute.value)?.imageUrl?.trim();
    const coverImageSources = [
      mappedImage,
      ...rules.map((rule) => findGift(state, rule.giftId)?.imgBasic?.trim()),
    ].filter((source, sourceIndex, sources): source is string => (
      Boolean(source) && sources.indexOf(source) === sourceIndex
    )).slice(0, 3);
    const visual = el('div', {
      class: `attribute-card-visual${coverImageSources.length > 1 ? ' has-multiple' : ''}`,
      ariaHidden: 'true',
    } as any);
    if (coverImageSources.length > 0) {
      coverImageSources.forEach((source) => {
        const image = el('img', { class: 'attribute-cover-image', alt: '', loading: 'lazy' }) as HTMLImageElement;
        image.src = source;
        visual.append(image);
      });
    } else {
      visual.append(el('span', {
        class: 'attribute-cover-placeholder',
        text: timerRules.length > 0 ? '⏱' : (Array.from(attribute.name.trim())[0] || '值'),
      }));
    }

    const attributeMeta = el('span', {
      class: 'attribute-meta',
      text: `${displayFormatLabel(attribute)} · ${rules.length} 条礼物规则 · ${timerRules.length} 个定时器`,
    });
    const title = el('div', { class: 'attribute-card-title hover-detail-cover', title: '悬停查看规则与 OBS 输出' });
    title.append(
      visual,
      el('div', { class: 'attribute-title-copy' }, [
        el('div', { class: 'attribute-name-row' }, [
          el('h3', { text: attribute.name }),
          attributeValueElement(attribute),
        ]),
      ]),
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
        const enabledButton = createEnabledButton(toggleLabel, `gift-rule-enabled-button${isTutorialTarget && rule === rules[0] ? ' guide-rule-toggle' : ''}`, rule.enabled !== false, (enabled) => {
          const currentRule = state.rules.find((candidate) => candidate.id === rule.id);
          if (!currentRule) {
            render();
            return;
          }
          currentRule.enabled = enabled;
          if (enabled && !guideDismissed && activeTutorialLesson() === 'enable') {
            completeTutorialLesson('enable');
          }
          updateEnabledAppearance(enabled);
          save();
          renderGuide();
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
      class: `btn attribute-obs-copy${isTutorialTarget ? ' guide-obs-copy' : ''}`,
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
      markTutorialLessonComplete(state.settings, 'output');
      state.settings.showTutorial = false;
      guideDismissed = true;
      forcedTutorialLesson = null;
      setFloatingDetailGuideExpanded(card, false);
      activeGuide?.dispose();
      activeGuide = null;
      save();
    };
    const obsRow = el('div', { class: 'attribute-obs-row' }, [
      el('span', { class: 'attribute-obs-label', text: 'OBS 专属链接' }),
      obsInput,
      copyObsButton,
    ]);

    const detailsContent = el('div', { class: 'attribute-card-details-content hover-detail-panel-content' }, [
      el('div', { class: 'attribute-detail-toolbar' }, [
        el('div', { class: 'attribute-detail-copy' }, [
          el('span', { class: 'attribute-detail-label', text: '规则与输出' }),
          attributeMeta,
        ]),
        el('div', { class: 'attribute-actions' }, [editButton, deleteButton]),
      ]),
      formulas,
      obsRow,
    ]);
    const details = el('div', {
      class: `attribute-card-details hover-detail-panel${isTutorialTarget ? ' guide-attribute-detail' : ''}`,
    }, [
      el('div', { class: 'attribute-card-details-inner hover-detail-panel-inner' }, [detailsContent]),
    ]);

    card.append(title, details);
    bindFloatingDetailCard(card, title, { panelWidth: 560, estimatedPanelHeight: 420 });
    return card;
  }

  function renderActivities(replaceExisting = false): void {
    const section = createActivityWorkspace({
      state,
      root,
      onPersist: saveAndWait,
      onRender: () => {
        renderActivities(true);
        syncLiveAttributeValues();
      },
      onEditorOpenChange: (open) => {
        editorOpen = open;
        if (open) {
          activeGuide?.dispose();
          activeGuide = null;
        } else {
          renderGuide();
        }
      },
    });
    appendOrReplaceSection(section, '.activity-workspace-section', replaceExisting);
  }

  function renderDisplayScenes(): void {
    const section = el('section', { class: 'display-scenes-section' });
    const headingRow = el('div', { class: 'display-scenes-heading' });
    const addButton = el('button', { class: 'btn', type: 'button', text: '+ 新建组合面板' }) as HTMLButtonElement;
    addButton.disabled = state.attributes.length < 2;
    addButton.title = addButton.disabled ? '至少创建 2 个属性后才能组合' : '';
    addButton.onclick = () => openDisplaySceneEditor();
    headingRow.append(
      sectionHeading('直播输出', 'OBS 组合面板', '把多个现有属性放进同一个直播画面；不会复制数值或规则，单属性链接仍可继续使用。'),
      addButton,
    );
    section.append(headingRow);

    if (state.attributes.length < 2) {
      section.append(el('div', { class: 'display-scenes-prerequisite' }, [
        el('strong', { text: '还需要至少 2 个属性' }),
        el('span', { text: '创建第二个属性后，就能把它们组合为纵向或双列面板。' }),
      ]));
    } else if (state.displayScenes.length === 0) {
      section.append(emptyState('还没有组合面板。新建后会得到一个独立 OBS 链接。'));
    } else {
      const grid = el('div', { class: 'display-scene-list' });
      state.displayScenes.forEach((scene, index) => grid.append(renderDisplaySceneCard(scene, index)));
      section.append(grid);
    }
    content.append(section);
  }

  function renderDisplaySceneCard(scene: DisplayScene, index: number): HTMLElement {
    const theme = getDisplayTheme(scene.themeId);
    const obsUrl = displaySceneUrl(location.origin, scene.id);
    const card = el('article', {
      class: 'display-scene-card hover-detail-card',
      tabIndex: 0,
      ariaLabel: `OBS 组合面板“${scene.name}”，${scene.attributeNames.length} 个属性。悬停或聚焦查看详细设置。`,
    } as any);
    const preview = el('div', { class: `display-scene-preview ${theme.previewClass} is-${scene.layout}` });
    preview.style.setProperty('--scene-preview-accent', theme.accent);
    preview.style.setProperty('--scene-preview-surface', theme.surface);
    for (const attributeName of scene.attributeNames.slice(0, 4)) {
      const attribute = state.attributes.find((candidate) => candidate.name === attributeName);
      preview.append(el('span', {}, [
        el('small', { text: attributeName }),
        attribute ? attributeLiveValueElement('strong', attribute) : el('strong', { text: '—' }),
      ]));
    }
    if (scene.attributeNames.length > 4) preview.append(el('em', { text: `+${scene.attributeNames.length - 4}` }));

    const copyButton = el('button', { class: 'btn display-scene-copy', type: 'button', text: '复制 OBS 链接' }) as HTMLButtonElement;
    const obsInput = el('input', { class: 'field-input display-scene-url', value: obsUrl, readOnly: true, ariaLabel: `${scene.name} 的 OBS 链接` } as any) as HTMLInputElement;
    copyButton.onclick = async () => {
      try {
        await navigator.clipboard.writeText(obsUrl);
        toast(`“${scene.name}”的组合链接已复制`, root);
      } catch {
        obsInput.select();
        toast('请按 Ctrl+C 复制地址', root);
      }
    };
    const editButton = el('button', { class: 'btn ghost', type: 'button', text: '编辑' }) as HTMLButtonElement;
    editButton.onclick = () => openDisplaySceneEditor(index);
    const deleteButton = el('button', { class: 'btn text-danger', type: 'button', text: '删除' }) as HTMLButtonElement;
    deleteButton.onclick = () => {
      if (!confirm(`删除组合面板“${scene.name}”？属性、规则和单属性 OBS 链接不会受影响。`)) return;
      state.displayScenes.splice(index, 1);
      state.activities = state.activities.map((activity) => activity.sceneId === scene.id
        ? { ...activity, sceneId: undefined }
        : activity);
      save();
      render();
      toast('组合面板已删除', root);
    };

    const sceneMeta = `${scene.layout === 'grid' ? '双列布局' : '纵向布局'} · ${theme.name} · ${scene.attributeNames.length} 个属性`;
    const cover = el('div', { class: 'display-scene-card-cover hover-detail-cover', title: '悬停查看组合面板详情' }, [
      preview,
      el('div', { class: 'display-scene-card-cover-copy' }, [
        el('h3', { text: scene.name }),
        el('span', { text: sceneMeta }),
      ]),
    ]);
    const detailsContent = el('div', { class: 'display-scene-card-body hover-detail-panel-content' }, [
        el('div', { class: 'display-scene-card-head' }, [
          el('span', { class: 'display-scene-detail-label', text: '面板内容与输出' }),
          el('div', { class: 'display-scene-card-actions' }, [editButton, deleteButton]),
        ]),
        el('div', { class: 'display-scene-attributes' }, scene.attributeNames.map((name) => el('span', { text: name }))),
        el('div', { class: 'display-scene-link-row' }, [obsInput, copyButton]),
    ]);
    const details = el('div', { class: 'display-scene-card-details hover-detail-panel' }, [
      el('div', { class: 'display-scene-card-details-inner hover-detail-panel-inner' }, [detailsContent]),
    ]);
    card.append(cover, details);
    bindFloatingDetailCard(card, cover, { panelWidth: 540, estimatedPanelHeight: 300 });
    return card;
  }

  function openDisplaySceneEditor(index?: number): void {
    activeGuide?.dispose();
    activeGuide = null;
    root.querySelector('.display-scene-overlay')?.remove();
    editorOpen = true;
    const original = index === undefined ? undefined : state.displayScenes[index];
    let selectedNames = original
      ? [...original.attributeNames]
      : state.attributes.slice(0, 2).map((attribute) => attribute.name);
    let layout: DisplaySceneLayout = original?.layout ?? 'grid';
    let themeId: DisplayThemeId = original?.themeId ?? state.settings.defaultDisplayThemeId;
    const overlay = el('div', { class: 'overlay display-scene-overlay' });
    const dialog = el('section', { class: 'card display-scene-dialog', role: 'dialog', ariaLabel: original ? `编辑组合面板 ${original.name}` : '新建组合面板' } as any);
    const closeButton = el('button', { class: 'modal-close', type: 'button', text: '×', ariaLabel: '关闭组合面板编辑器' } as any) as HTMLButtonElement;
    const close = (): void => {
      overlay.remove();
      editorOpen = false;
      renderGuide();
    };
    closeButton.onclick = close;
    overlay.onpointerdown = (event) => { overlay.dataset.pointerOutside = String(event.target === overlay); };
    overlay.onclick = (event) => {
      const shouldClose = overlay.dataset.pointerOutside === 'true' && event.target === overlay;
      overlay.dataset.pointerOutside = 'false';
      if (shouldClose) close();
    };

    const nameInput = inputField('组合面板名称', original?.name ?? `组合面板 ${state.displayScenes.length + 1}`);
    nameInput.maxLength = 40;
    const layoutButtons = new Map<DisplaySceneLayout, HTMLButtonElement>();
    const layoutControl = el('div', { class: 'display-scene-layout-options' });
    const refreshLayout = (): void => {
      for (const [candidate, button] of layoutButtons) {
        const active = candidate === layout;
        button.classList.toggle('is-selected', active);
        button.setAttribute('aria-pressed', String(active));
      }
    };
    ([
      ['stack', '纵向布局', '适合窄画布，属性从上到下排列'],
      ['grid', '双列布局', '适合横向画布，同时查看更多属性'],
    ] as const).forEach(([value, label, description]) => {
      const button = el('button', { class: 'display-scene-layout-option', type: 'button', ariaPressed: 'false' } as any) as HTMLButtonElement;
      button.append(el('span', { class: `display-scene-layout-icon is-${value}` }, [el('i'), el('i')]), el('strong', { text: label }), el('small', { text: description }));
      button.onclick = () => { layout = value; refreshLayout(); };
      layoutButtons.set(value, button);
      layoutControl.append(button);
    });
    refreshLayout();

    const selectionStatus = el('strong', { class: 'display-scene-selection-count' });
    const attributeGrid = el('div', { class: 'display-scene-attribute-picker' });
    const attributeButtons = new Map<string, HTMLButtonElement>();
    const refreshAttributes = (): void => {
      selectionStatus.textContent = `已选择 ${selectedNames.length} / ${MAX_DISPLAY_SCENE_ATTRIBUTES}`;
      for (const [name, button] of attributeButtons) {
        const order = selectedNames.indexOf(name);
        button.classList.toggle('is-selected', order >= 0);
        button.setAttribute('aria-pressed', String(order >= 0));
        const marker = button.querySelector('.display-scene-attribute-order');
        if (marker) marker.textContent = order >= 0 ? String(order + 1) : '+';
      }
    };
    for (const attribute of state.attributes) {
      const button = el('button', { class: 'display-scene-attribute-option', type: 'button', ariaPressed: 'false' } as any) as HTMLButtonElement;
      button.append(
        el('span', { class: 'display-scene-attribute-order' }),
        el('span', {}, [el('strong', { text: attribute.name }), el('small', { text: formatValue(attribute.value, attribute) })]),
      );
      button.onclick = () => {
        const position = selectedNames.indexOf(attribute.name);
        if (position >= 0) selectedNames.splice(position, 1);
        else if (selectedNames.length < MAX_DISPLAY_SCENE_ATTRIBUTES) selectedNames.push(attribute.name);
        else toast(`一个组合面板最多显示 ${MAX_DISPLAY_SCENE_ATTRIBUTES} 个属性`, root);
        refreshAttributes();
      };
      attributeButtons.set(attribute.name, button);
      attributeGrid.append(button);
    }
    refreshAttributes();

    const themeControl = createDisplayThemeControl(
      themeId,
      (nextThemeId) => { themeId = nextThemeId; },
      '组合面板皮肤',
      '只覆盖这个组合链接中的外观，不改变各属性自己的专属链接。',
    );
    themeControl.classList.add('display-scene-theme-control');

    const cancelButton = el('button', { class: 'btn ghost', type: 'button', text: '取消' }) as HTMLButtonElement;
    cancelButton.onclick = close;
    const saveButton = el('button', { class: 'btn', type: 'button', text: original ? '保存修改' : '创建组合面板' }) as HTMLButtonElement;
    saveButton.onclick = async () => {
      const name = nameInput.value.trim();
      if (!name) {
        toast('请填写组合面板名称', root);
        nameInput.focus();
        return;
      }
      if (state.displayScenes.some((scene, sceneIndex) => sceneIndex !== index && scene.name.toLowerCase() === name.toLowerCase())) {
        toast('组合面板名称不能重复', root);
        nameInput.focus();
        return;
      }
      if (selectedNames.length < 2) {
        toast('请至少选择 2 个属性', root);
        return;
      }
      const nextScene: DisplayScene = {
        id: original?.id ?? createDisplaySceneId(),
        name,
        attributeNames: [...selectedNames],
        layout,
        themeId,
      };
      const previousScenes = state.displayScenes;
      state.displayScenes = [...state.displayScenes];
      if (index === undefined) state.displayScenes.push(nextScene);
      else state.displayScenes[index] = nextScene;
      saveButton.disabled = true;
      saveButton.textContent = '保存中…';
      try {
        await saveAndWait();
      } catch {
        state.displayScenes = previousScenes;
        saveButton.disabled = false;
        saveButton.textContent = original ? '保存修改' : '创建组合面板';
        return;
      }
      overlay.remove();
      editorOpen = false;
      render();
      toast(index === undefined ? '组合面板已创建' : '组合面板已保存', root);
    };

    dialog.append(
      el('header', { class: 'display-scene-dialog-header' }, [
        el('div', {}, [
          el('span', { class: 'section-kicker', text: 'OBS 组合面板' }),
          el('h2', { text: original ? `编辑“${original.name}”` : '组合多个属性' }),
          el('p', { text: '只组合展示，不复制属性数据。编号表示在面板中的显示顺序。' }),
        ]),
        closeButton,
      ]),
      el('div', { class: 'display-scene-dialog-body' }, [
        fieldControl(nameInput),
        el('section', { class: 'display-scene-editor-section' }, [
          el('div', { class: 'display-scene-editor-heading' }, [el('div', {}, [el('h3', { text: '选择布局' }), el('p', { text: 'OBS 中可随时使用同一个链接切换布局。' })])]),
          layoutControl,
        ]),
        el('section', { class: 'display-scene-editor-section' }, [
          el('div', { class: 'display-scene-editor-heading' }, [el('div', {}, [el('h3', { text: '选择并排序属性' }), el('p', { text: '至少选择 2 个；取消后重新选择可以调整顺序。' })]), selectionStatus]),
          attributeGrid,
        ]),
        themeControl,
      ]),
      el('footer', { class: 'modal-actions display-scene-dialog-actions' }, [cancelButton, saveButton]),
    );
    overlay.append(dialog);
    root.append(overlay);
  }

  function renderContributionLeaderboard(replaceExisting = false): void {
    const viewers = state.contributions.viewers;
    const blindBoxLeaderboard = buildBlindBoxLeaderboard(state.contributions);
    const section = el('section', { class: 'contribution-section' });
    const copyObsButton = el('button', {
      class: 'btn ghost contribution-obs-copy',
      type: 'button',
      text: '复制盈亏榜 OBS 链接',
    }) as HTMLButtonElement;
    copyObsButton.onclick = async () => {
      try {
        await navigator.clipboard.writeText(blindBoxDisplayUrl(location.origin));
        toast('盲盒盈亏榜 OBS 链接已复制', root);
      } catch {
        toast('复制失败，请检查浏览器剪贴板权限', root);
      }
    };
    const clearButton = el('button', {
      class: 'btn ghost contribution-clear',
      type: 'button',
      text: '清空排行榜',
    }) as HTMLButtonElement;
    clearButton.disabled = viewers.length === 0;
    clearButton.onclick = () => {
      if (!confirm('清空全部观众贡献和盲盒盈亏统计？送礼生效记录与属性值不会受影响。')) return;
      clearButton.disabled = true;
      void clearContributionLedger().then((contributions) => {
        state.contributions = contributions;
        render();
        toast('观众排行榜已清空', root);
      }).catch((error) => {
        clearButton.disabled = false;
        toast(error instanceof Error ? error.message : '排行榜清空失败', root);
      });
    };
    const heading = el('div', { class: 'contribution-heading' }, [
      sectionHeading(
        '观众数据',
        '贡献与盲盒排行榜',
        '后台统计收到的全部礼物；规则命中只计算真正生效的规则。数据从上次清空开始累计，关闭页面不会中断。',
      ),
      el('div', { class: 'contribution-heading-actions' }, [
        el('span', { class: 'contribution-viewer-count', text: `${viewers.length} 位观众` }),
        copyObsButton,
        clearButton,
      ]),
    ]);
    section.append(heading);

    const totals = viewers.reduce((summary, viewer) => ({
      giftCount: summary.giftCount + viewer.giftCount,
      goldValue: summary.goldValue + viewer.goldValue,
      ruleTriggers: summary.ruleTriggers + viewer.ruleTriggers,
    }), { giftCount: 0, goldValue: 0, ruleTriggers: 0 });
    section.append(el('div', { class: 'contribution-summary' }, [
      contributionSummaryItem('收到礼物', `${formatLedgerNumber(totals.giftCount)} 个`),
      contributionSummaryItem('金瓜子贡献', formatLedgerNumber(totals.goldValue)),
      contributionSummaryItem('规则命中', `${formatLedgerNumber(totals.ruleTriggers)} 次`),
      contributionSummaryItem('盲盒净盈亏', formatSignedLedgerNumber(blindBoxLeaderboard.summary.profit), contributionTone(blindBoxLeaderboard.summary.profit)),
    ]));

    const modeTabs = el('div', { class: 'contribution-tabs', role: 'tablist', ariaLabel: '排行榜类型' } as any);
    const listHost = el('div', { class: 'contribution-list-host' });
    const modes: Array<{ id: LeaderboardMode; label: string }> = [
      { id: 'contribution', label: '礼物贡献' },
      { id: 'rules', label: '规则命中' },
      { id: 'blind-box', label: '盲盒盈亏' },
    ];
    const renderRows = (): void => {
      for (const button of Array.from(modeTabs.querySelectorAll<HTMLButtonElement>('.contribution-tab'))) {
        const active = button.dataset.mode === leaderboardMode;
        button.classList.toggle('is-active', active);
        button.setAttribute('aria-selected', String(active));
      }
      const ranked = rankContributors(viewers, leaderboardMode);
      if (ranked.length === 0) {
        listHost.replaceChildren(el('div', {
          class: 'contribution-empty',
          text: leaderboardMode === 'blind-box'
            ? '还没有收到盲盒礼物。收到后会按实际开出价值计算净盈亏。'
            : leaderboardMode === 'rules'
              ? '还没有观众命中过已启用的礼物规则。'
              : '还没有收到礼物，排行榜会在后台自动累计。',
        }));
        return;
      }
      const list = el('div', { class: 'contribution-list' });
      ranked.slice(0, 100).forEach((viewer, index) => list.append(renderContributionRow(viewer, index + 1, leaderboardMode)));
      listHost.replaceChildren(list);
    };
    for (const mode of modes) {
      const button = el('button', {
        class: 'contribution-tab',
        type: 'button',
        text: mode.label,
        role: 'tab',
      } as any) as HTMLButtonElement;
      button.dataset.mode = mode.id;
      button.onclick = () => {
        leaderboardMode = mode.id;
        renderRows();
      };
      modeTabs.append(button);
    }
    section.append(modeTabs, listHost);
    renderRows();
    appendOrReplaceSection(section, '.contribution-section', replaceExisting);
  }

  function renderContributionRow(viewer: ViewerContribution, rank: number, mode: LeaderboardMode): HTMLElement {
    const avatar = el('img', {
      class: 'contribution-avatar',
      alt: viewer.uname ? `${viewer.uname}的头像` : '用户头像',
      referrerPolicy: 'no-referrer',
    }) as HTMLImageElement;
    avatar.src = viewer.avatar || transparentPixel();
    const identity = viewer.uid ? `UID ${viewer.uid}` : '昵称由 B 站脱敏或未提供 UID';
    const metric = contributionMetric(viewer, mode);
    const details = el('div', { class: 'contribution-details' });
    if (mode === 'contribution') {
      details.append(
        el('span', { text: `${formatLedgerNumber(viewer.giftCount)} 个礼物` }),
        el('span', { text: `${formatLedgerNumber(viewer.goldValue)} 金瓜子` }),
        ...(viewer.silverValue > 0 ? [el('span', { text: `${formatLedgerNumber(viewer.silverValue)} 银瓜子` })] : []),
      );
    } else if (mode === 'rules') {
      const deltas = Object.entries(viewer.attributeDeltas)
        .filter(([, delta]) => Math.abs(delta) > Number.EPSILON)
        .slice(0, 4);
      details.append(el('span', { text: `${formatLedgerNumber(viewer.ruleTriggers)} 次规则命中` }));
      for (const [attributeName, delta] of deltas) {
        const attribute = state.attributes.find((item) => item.name === attributeName);
        details.append(el('span', {
          class: `is-${contributionTone(delta)}`,
          text: `${attributeName} ${formatHistoryDelta(delta, attribute)}`,
          title: `${attributeName}净变化 ${formatHistoryDelta(delta, attribute)}`,
        }));
      }
    } else {
      details.append(
        el('span', { text: `${formatLedgerNumber(viewer.blindBoxCount)} 个盲盒` }),
        el('span', { text: `投入 ${formatLedgerNumber(viewer.blindBoxCost)}` }),
        el('span', { text: `开出 ${formatLedgerNumber(viewer.blindBoxValue)}` }),
        ...(viewer.unpricedBlindBoxCount
          ? [el('span', { class: 'is-warning', text: `${viewer.unpricedBlindBoxCount} 个缺少成本价` })]
          : []),
      );
    }
    const lastGift = new Date(viewer.lastGiftAt < 1_000_000_000_000 ? viewer.lastGiftAt * 1000 : viewer.lastGiftAt);
    return el('article', { class: 'contribution-row' }, [
      el('strong', { class: `contribution-rank is-${Math.min(rank, 4)}`, text: String(rank) }),
      avatar,
      el('div', { class: 'contribution-person' }, [
        el('strong', { text: viewer.uname || '匿名观众', title: viewer.uname || '匿名观众' }),
        el('span', { text: identity, title: identity }),
      ]),
      details,
      el('div', { class: `contribution-metric is-${metric.tone}` }, [
        el('strong', { text: metric.value, title: metric.title }),
        el('span', { text: metric.label }),
      ]),
      el('time', {
        class: 'contribution-time',
        dateTime: Number.isNaN(lastGift.getTime()) ? '' : lastGift.toISOString(),
        text: Number.isNaN(lastGift.getTime()) ? '' : lastGift.toLocaleString('zh-CN', { month: '2-digit', day: '2-digit', hour: '2-digit', minute: '2-digit', hour12: false }),
      }),
    ]);
  }

  function contributionSummaryItem(label: string, value: string, tone = 'neutral'): HTMLElement {
    return el('div', { class: `contribution-summary-item is-${tone}` }, [
      el('span', { text: label }),
      el('strong', { text: value, title: value }),
    ]);
  }

  function rankContributors(viewers: ViewerContribution[], mode: LeaderboardMode): ViewerContribution[] {
    if (mode === 'blind-box') return buildBlindBoxLeaderboard({ viewers }).viewers;
    const ranked = viewers.filter((viewer) => (
      mode === 'rules' ? viewer.ruleTriggers > 0 : viewer.giftCount > 0
    ));
    return ranked.sort((left, right) => {
      const metric = mode === 'rules'
        ? right.ruleTriggers - left.ruleTriggers
        : right.goldValue - left.goldValue;
      return metric || right.giftCount - left.giftCount || right.lastGiftAt - left.lastGiftAt;
    });
  }

  function contributionMetric(viewer: ViewerContribution, mode: LeaderboardMode): { value: string; label: string; title: string; tone: string } {
    if (mode === 'rules') {
      const value = formatLedgerNumber(viewer.ruleTriggers);
      return { value, label: '次命中', title: `${value} 次规则命中`, tone: 'positive' };
    }
    if (mode === 'blind-box') {
      const value = formatSignedLedgerNumber(viewer.blindBoxProfit);
      return { value, label: '净盈亏', title: `盲盒净盈亏 ${value} 金瓜子`, tone: contributionTone(viewer.blindBoxProfit) };
    }
    const value = formatLedgerNumber(viewer.goldValue);
    return { value, label: '金瓜子', title: `${value} 金瓜子贡献`, tone: 'positive' };
  }

  function formatLedgerNumber(value: number): string {
    return Number(value || 0).toLocaleString('zh-CN', { maximumFractionDigits: 2 });
  }

  function formatSignedLedgerNumber(value: number): string {
    const normalized = Number(value || 0);
    return `${normalized > 0 ? '+' : normalized < 0 ? '-' : ''}${formatLedgerNumber(Math.abs(normalized))}`;
  }

  function contributionTone(value: number): 'positive' | 'negative' | 'neutral' {
    return value > 0 ? 'positive' : value < 0 ? 'negative' : 'neutral';
  }

  function renderGiftHistory(replaceExisting = false): void {
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
      appendOrReplaceSection(section, '.gift-history-section', replaceExisting);
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
    section.append(el('div', { class: 'gift-history-list-frame' }, [list]));
    appendOrReplaceSection(section, '.gift-history-section', replaceExisting);
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
    const lessonBeforeOpen = activeTutorialLesson();
    editorOpen = true;
    editorGuideEnabled = !guideDismissed && (
      (index === undefined && (lessonBeforeOpen === 'attribute' || lessonBeforeOpen === 'template'))
      || (forcedTutorialLesson !== null && TUTORIAL_LESSONS.some((lesson) => (
        lesson.id === forcedTutorialLesson && lesson.section
      )))
    );

    const original = index === undefined ? undefined : state.attributes[index];
    const originalName = original?.name ?? '';
    const timerRules = original
      ? state.timerRules.filter((rule) => rule.attributeName === original.name).map((rule) => ({ ...rule }))
      : [];
    let pickerCatalog = buildGiftPickerCatalog(state, roomGiftCatalog);
    let allGifts = pickerCatalog.gifts;
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

    editorTutorialProgress = {
      open: true,
      isNew: index === undefined,
      giftCount: selected.size,
      timerCount: timerRules.length,
    };

    const overlay = el('div', { class: 'overlay attribute-overlay' });
    let modal: HTMLElement;
    const closeButton = el('button', { class: 'modal-close', type: 'button', text: '×', ariaLabel: '关闭' } as any) as HTMLButtonElement;
    const close = (): void => {
      overlay.remove();
      editorOpen = false;
      editorGuideEnabled = false;
      editorTutorialProgress = { open: false };
      activeEditorWorkspace = null;
      forcedTutorialLesson = null;
      refreshOpenGiftCatalog = null;
      renderGuide();
    };
    closeButton.onclick = close;
    const modalHeader = el('header', { class: 'modal-header attribute-workbench-header' }, [
      el('div', {}, [
        el('span', {
          class: 'section-kicker',
          text: original ? '属性工作台' : editorGuideEnabled ? '新手实战 · 加班机' : '新建互动属性',
        }),
        el('h2', { text: original ? `配置“${original.name}”` : editorGuideEnabled ? '制作第一台加班机' : '创建互动属性' }),
        el('p', { text: '按工作区逐项配置；左侧训练任务会解释每项功能为什么存在。' }),
      ]),
      closeButton,
    ]);

    const nameInput = inputField('属性名称', original?.name ?? `属性${state.attributes.length + 1}`);
    nameInput.placeholder = '例如 加班时间';
    nameInput.oninput = () => {
      const targetName = nameInput.value.trim() || '属性';
      for (const label of Array.from(modal.querySelectorAll('.formula-target-name'))) {
        label.textContent = `${targetName} =`;
      }
      updateOverviewPreview();
    };
    const valueInput = inputField('当前值', String(original?.value ?? 0));
    valueInput.inputMode = 'decimal';
    const formatSelect = el('select', { class: 'field-input' }) as HTMLSelectElement;
    formatSelect.innerHTML = '<option value="hhmmss">HH:MM:SS 计时器</option><option value="number">纯数字</option><option value="suffix">数字 + 后缀</option>';
    formatSelect.value = original?.format ?? 'hhmmss';
    const displayConfig: AttributeDisplay = original?.display
      ? { ...original.display, valueMappings: original.display.valueMappings?.map((mapping) => ({ ...mapping })) }
      : {
        variant: formatSelect.value === 'hhmmss' ? 'timer' : 'number',
        themeId: state.settings.defaultDisplayThemeId,
        title: original?.name ?? '',
      };
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
    formatSelect.onchange = () => {
      if (displayConfig.variant === 'number' || displayConfig.variant === 'timer') {
        displayConfig.variant = formatSelect.value === 'hhmmss' ? 'timer' : 'number';
      }
      updateSuffixVisibility();
      updateOverviewPreview();
    };
    updateSuffixVisibility();
    const basics = el('div', { class: 'attribute-basics' }, [
      fieldControl(nameInput),
      fieldControl(valueInput),
      el('label', { class: 'field' }, [el('span', { class: 'field-label', text: '显示格式' }), formatSelect]),
      suffixControl,
    ]);
    const overviewPreviewName = el('span', { class: 'attribute-overview-preview-name' });
    const overviewPreviewValue = el('strong', { class: 'attribute-overview-preview-value' });
    const updateOverviewPreview = (): void => {
      const value = Number(valueInput.value);
      const format = formatSelect.value as Attribute['format'];
      const previewAttribute: Attribute = {
        name: nameInput.value.trim() || '属性名称',
        value: Number.isFinite(value) ? value : 0,
        unit: format === 'hhmmss' ? 'seconds' : 'none',
        format,
        decimals: original?.decimals ?? 0,
        suffix: format === 'suffix' ? suffixInput.value : '',
        broadcastMessage: broadcastMessageInput.value.trim(),
      };
      overviewPreviewName.textContent = previewAttribute.name;
      overviewPreviewValue.textContent = formatValue(previewAttribute.value, previewAttribute);
    };
    valueInput.oninput = updateOverviewPreview;
    suffixInput.oninput = updateOverviewPreview;
    const templateButton = el('button', {
      class: 'btn guide-overtime-template',
      type: 'button',
      text: '使用加班机模板',
    }) as HTMLButtonElement;
    templateButton.onclick = () => {
      nameInput.value = '加班时间';
      valueInput.value = '0';
      formatSelect.value = 'hhmmss';
      suffixInput.value = '';
      if (!broadcastMessageInput.value.trim()) broadcastMessageInput.value = '感谢大家的支持，欢迎投喂礼物';
      updateSuffixVisibility();
      for (const label of Array.from(modal.querySelectorAll('.formula-target-name'))) {
        label.textContent = '加班时间 =';
      }
      editorTutorialProgress.basicsConfigured = true;
      updateOverviewPreview();
      refreshEditorTutorial();
      toast('已套用加班机模板', root);
    };
    const overviewLessonCard = el('div', { class: 'workbench-lesson-card' }, [
        el('span', { class: 'workbench-lesson-icon', text: '01' }),
        el('div', {}, [
          el('strong', { text: '先定义后台要保存的数据' }),
          el('p', { text: '属性名称会出现在规则中；当前值是计算起点；显示格式只改变 OBS 中的呈现。' }),
        ]),
        templateButton,
    ]);
    overviewLessonCard.dataset.tutorialLesson = 'basics';
    const overviewPanel = el('section', { class: 'attribute-overview-panel' }, [
      overviewLessonCard,
      basics,
      el('div', { class: 'attribute-overview-preview' }, [
        el('span', { text: 'OBS 数值预览' }),
        el('div', {}, [overviewPreviewName, overviewPreviewValue]),
      ]),
    ]);
    updateOverviewPreview();

    const currentAttributeName = (): string => nameInput.value.trim() || originalName || '属性';
    const formulaForCurrentAttribute = (formula: string): string => (
      originalName && originalName !== currentAttributeName()
        ? replaceFormulaVariable(formula.trim(), originalName, currentAttributeName())
        : formula.trim()
    );

    const isActiveEditorTutorialLesson = (lesson: TutorialLesson): boolean => (
      !guideDismissed
      && (editorGuideEnabled || forcedTutorialLesson === lesson)
      && activeTutorialLesson() === lesson
    );
    const shouldConfirmTutorialPreview = (lesson: 'rule' | 'timer'): boolean => (
      isActiveEditorTutorialLesson(lesson)
    );
    const appendTutorialPreviewConfirmation = (
      preview: HTMLElement,
      lesson: 'rule' | 'timer',
      onConfirm: () => void,
    ): boolean => {
      if (!shouldConfirmTutorialPreview(lesson)) return false;
      preview.classList.add('has-tutorial-confirmation');
      const confirmButton = el('button', {
        class: `formula-preview-confirm guide-${lesson}-preview-confirm`,
        type: 'button',
        text: '确认这次变化',
      }) as HTMLButtonElement;
      confirmButton.onclick = () => {
        confirmButton.disabled = true;
        onConfirm();
      };
      preview.append(confirmButton);
      return true;
    };

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
      const confirmButton = el('button', {
        class: `btn${context === 'gift' ? ' guide-preset-confirm' : ''}`,
        type: 'button',
        text: '保存',
      }) as HTMLButtonElement;
      const close = (refreshGuide = true): void => {
        nameOverlay.remove();
        if (refreshGuide && isActiveEditorTutorialLesson('preset')) refreshEditorTutorial(false);
      };
      cancelButton.onclick = () => close();
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
            if (context === 'gift') editorTutorialProgress.presetSaved = true;
            refreshFormulaPresetLists(context);
            close(false);
            refreshEditorTutorial();
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
      if (isActiveEditorTutorialLesson('preset')) refreshEditorTutorial(false);
      presetNameInput.focus();
    }

    function renderFormulaPresetControls(
      context: FormulaPresetContext,
      formulaInput: HTMLInputElement,
      formulaNameInput: HTMLInputElement,
      updatePreview: () => void,
    ): { saveButton: HTMLButtonElement; presetList: HTMLElement } {
      const saveButton = el('button', {
        class: `formula-save-preset${context === 'gift' ? ' guide-save-preset' : ''}`,
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
        editorTutorialProgress.timerCount = timerRules.length;
        editorTutorialProgress.timerPreviewed = false;
        renderTimerRules();
        refreshEditorTutorial(false);
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
      const updatePreview = (completeLesson = false): void => {
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
        preview.classList.remove('has-tutorial-confirmation');
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
            preview.replaceChildren(el('span', {
              text: completeLesson ? '当前条件不满足，本次未执行' : '当前条件不满足，本次会跳过',
            }));
            if (completeLesson) {
              const awaitingConfirmation = appendTutorialPreviewConfirmation(preview, 'timer', () => {
                editorTutorialProgress.timerPreviewed = true;
                refreshEditorTutorial();
              });
              if (!awaitingConfirmation) editorTutorialProgress.timerPreviewed = true;
              refreshEditorTutorial(!awaitingConfirmation);
            }
            return;
          }
          if (completeLesson) {
            valueInput.value = String(result);
            updateOverviewPreview();
          }
          preview.replaceChildren(
            el('span', { text: `${completeLesson ? '已模拟执行' : '预览'}：${currentValue} → ` }),
            el('strong', { text: String(result) }),
          );
          if (completeLesson) {
            const awaitingConfirmation = appendTutorialPreviewConfirmation(preview, 'timer', () => {
              editorTutorialProgress.timerPreviewed = true;
              refreshEditorTutorial();
            });
            if (!awaitingConfirmation) editorTutorialProgress.timerPreviewed = true;
            refreshEditorTutorial(!awaitingConfirmation);
          }
        }).catch((error) => {
          if (requestVersion !== previewVersion) return;
          preview.replaceChildren(el('span', {
            class: 'error',
            text: error instanceof Error ? error.message : String(error),
          }));
        });
      };
      formulaInput.oninput = () => updatePreview();

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
      const simulateButton = el('button', {
        class: 'btn ghost formula-simulate guide-timer-simulator',
        type: 'button',
        text: '模拟执行一次',
      }) as HTMLButtonElement;
      simulateButton.onclick = () => updatePreview(true);

      editor.append(
        el('div', { class: 'timer-rule-fields' }, [
          fieldControl(formulaNameInput),
          intervalControl,
          fieldControl(conditionInput),
          formulaControl,
        ]),
        el('div', { class: 'formula-editor-meta' }, [examples, el('div', { class: 'formula-preview-row' }, [preview, simulateButton])]),
      );
      updatePreview();
      return editor;
    }

    const addTimerButton = el('button', { class: 'btn ghost add-timer-button guide-add-timer', type: 'button', text: '+ 添加定时器' }) as HTMLButtonElement;
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
      editorTutorialProgress.timerCount = timerRules.length;
      editorTutorialProgress.timerPreviewed = false;
      renderTimerRules();
      timerList.querySelector('.timer-rule-editor:last-child')?.scrollIntoView({ block: 'nearest' });
      refreshEditorTutorial(false);
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
    const giftDrawerSelectionCount = el('span', { class: 'gift-picker-drawer-selection' });
    const giftDrawer = el('aside', { class: 'gift-picker-drawer', ariaLabel: '添加礼物' } as any);
    giftDrawer.hidden = true;
    let giftSelectionSnapshot: Map<number, SelectedGiftRule> | null = null;
    let giftPreviewSnapshot = false;
    let modalFooter: HTMLElement | null = null;
    let confirmGiftSelectionButton: HTMLButtonElement | null = null;
    const giftChoiceButtons = new Map<number, HTMLButtonElement>();
    const giftPickerBatchSize = 40;
    let filteredGifts: GiftInfo[] = [];
    let visibleGiftCount = 0;
    let giftPickerLoader: HTMLButtonElement | null = null;

    const openGiftDrawer = (): void => {
      if (!giftDrawer.hidden) return;
      giftSelectionSnapshot = new Map(selected);
      giftPreviewSnapshot = editorTutorialProgress.giftPreviewed === true;
      giftDrawer.hidden = false;
      giftDrawer.classList.add('is-open');
      if (modalFooter) modalFooter.hidden = true;
      giftSearch.focus();
      renderGuide();
    };

    const closeGiftDrawer = (commit: boolean): void => {
      if (giftDrawer.hidden) return;
      const selectionChanged = giftSelectionSnapshot === null
        || giftSelectionSnapshot.size !== selected.size
        || Array.from(selected).some(([giftId, item]) => giftSelectionSnapshot?.get(giftId) !== item);
      if (!commit && giftSelectionSnapshot) {
        selected.clear();
        for (const [giftId, item] of giftSelectionSnapshot) selected.set(giftId, item);
        renderGiftPicker();
        renderSelectedRules();
      }
      giftSelectionSnapshot = null;
      giftDrawer.hidden = true;
      giftDrawer.classList.remove('is-open');
      if (modalFooter) modalFooter.hidden = false;
      editorTutorialProgress.giftCount = selected.size;
      editorTutorialProgress.giftPreviewed = commit && selectionChanged ? false : giftPreviewSnapshot;
      refreshEditorTutorial();
    };

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

    function createGiftChoice(gift: GiftInfo, showAllStatuses: boolean): HTMLButtonElement {
      const selectedNow = selected.has(gift.id);
      const button = el('button', {
        class: `gift-choice${selectedNow ? ' is-selected' : ''}`,
        type: 'button',
        ariaPressed: String(selectedNow),
      }) as HTMLButtonElement;
      button.dataset.giftId = String(gift.id);
      const image = el('img', { class: 'gift-choice-image', alt: '' }) as HTMLImageElement;
      image.src = gift.imgBasic || transparentPixel();
      const availability = pickerCatalog.availabilityById.get(gift.id) ?? 'historical';
      const statusLabel: Record<GiftAvailability, string> = {
        special: '直播事件',
        listed: '已上架',
        observed: '直播中收到过',
        historical: '历史礼物',
      };
      const showStatus = showAllStatuses || availability === 'observed' || availability === 'special';
      button.append(
        image,
        el('span', { class: 'gift-choice-copy' }, [
          el('strong', { text: gift.name }),
          el('span', { class: 'gift-choice-meta' }, [
            el('small', { text: giftPriceLabel(gift) }),
            ...(showStatus ? [el('span', {
              class: `gift-listing-status is-${availability}`,
              text: statusLabel[availability],
            })] : []),
          ]),
        ]),
        el('span', { class: 'gift-choice-check', text: selectedNow ? '✓' : '+' }),
      );
      button.onclick = () => {
        if (selected.has(gift.id)) selected.delete(gift.id);
        else {
          const item: SelectedGiftRule = {
            gift,
            formulaName: `${gift.name}规则`,
            formula: defaultFormula(),
            enabled: !editorGuideEnabled,
            quickOperation: 'price',
            quickAmount: 60,
          };
          selected.set(gift.id, item);
          void hydrateBlindBoxRule(item);
        }
        updateGiftChoice(gift.id);
        renderSelectedRules();
        renderGuide();
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
      const showAllStatuses = giftSearch.value.trim().length > 0;
      for (const gift of filteredGifts.slice(visibleGiftCount, nextCount)) {
        giftPicker.append(createGiftChoice(gift, showAllStatuses));
      }
      visibleGiftCount = nextCount;
      updateGiftPickerLoader();
    }

    function renderGiftPicker(): void {
      const query = giftSearch.value.trim();
      filteredGifts = allGifts.filter((gift) => (query.length > 0
        || !pickerCatalog.hasLiveListingStatus
        || pickerCatalog.availabilityById.get(gift.id) !== 'historical')
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
      giftDrawerSelectionCount.textContent = `本次已选择 ${selected.size} 个礼物`;
      if (confirmGiftSelectionButton) {
        confirmGiftSelectionButton.textContent = `确认选择（${selected.size}）`;
        confirmGiftSelectionButton.classList.toggle('guide-gift-selection-ready', selected.size > 0);
      }
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
      if (isSpecialEventGift(item.gift) || selected.get(item.gift.id) !== item || (item.matchGiftIds?.length ?? 0) > 1) return;
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
        editorTutorialProgress.giftCount = selected.size;
        editorTutorialProgress.giftPreviewed = false;
        updateGiftChoice(item.gift.id);
        renderSelectedRules();
        refreshEditorTutorial(false);
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
            el('small', {
              text: isSpecialEventGift(item.gift)
                ? '每次发生时执行一次 · 实际支付金额会传入 price'
                : `每收到 1 个执行一次 · ${giftPriceLabel(item.gift)}`,
            }),
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
      const renderSimulationPreview = (): boolean => {
        if (!item.simulationPreview) return false;
        preview.classList.remove('has-tutorial-confirmation');
        preview.replaceChildren(
          el('span', { text: `已模拟 1 个 ${item.gift.name}：${item.simulationPreview.currentValue} → ` }),
          el('strong', { text: String(item.simulationPreview.result) }),
        );
        return appendTutorialPreviewConfirmation(preview, 'rule', () => {
          editorTutorialProgress.giftPreviewed = true;
          refreshEditorTutorial();
        });
      };
      const updatePreview = (completeLesson = false): void => {
        item.formula = formulaInput.value;
        if (!completeLesson) item.simulationPreview = undefined;
        preview.classList.remove('has-tutorial-confirmation');
        preview.replaceChildren();
        const name = nameInput.value.trim() || originalName || '属性';
        const value = Number(valueInput.value);
        const formula = originalName && originalName !== name
          ? replaceFormulaVariable(item.formula.trim(), originalName, name)
          : item.formula.trim();
        const currentValue = Number.isFinite(value) ? value : 0;
        const requestVersion = ++previewVersion;
        preview.append(el('span', { text: '由后台计算预览…' }));
        void previewFormula(formula, name, currentValue, 'gift', item.gift.price).then((result) => {
          if (requestVersion !== previewVersion) return;
          if (completeLesson) {
            valueInput.value = String(result);
            item.simulationPreview = { currentValue, result };
            updateOverviewPreview();
          }
          let awaitingConfirmation = false;
          if (completeLesson) awaitingConfirmation = renderSimulationPreview();
          else preview.replaceChildren(
              el('span', { text: `预览收到 1 个 ${item.gift.name}：${currentValue} → ` }),
              el('strong', { text: String(result) }),
            );
          if (completeLesson) {
            if (!awaitingConfirmation) editorTutorialProgress.giftPreviewed = true;
            refreshEditorTutorial(!awaitingConfirmation);
          }
        }).catch((error) => {
          if (requestVersion !== previewVersion) return;
          preview.replaceChildren(
            el('span', { class: 'error', text: error instanceof Error ? error.message : String(error) }),
          );
        });
      };
      const attributeName = nameInput.value.trim() || originalName || '属性';
      if (!item.quickOperation) {
        const detected = detectQuickGiftRule(item.formula, attributeName);
        item.quickOperation = detected.operation;
        item.quickAmount = detected.amount;
        item.quickMaximum = detected.maximum;
        item.quickMaximumEnabled = detected.maximum !== undefined;
      }
      const operationSelect = el('select', { class: 'field-input quick-rule-operation' }) as HTMLSelectElement;
      for (const group of QUICK_GIFT_OPERATION_GROUPS) {
        const optionGroup = el('optgroup') as HTMLOptGroupElement;
        optionGroup.label = group.label;
        for (const operation of group.operations) {
          const option = el('option', {
            value: operation,
            text: quickGiftOperationLabel(operation, attributeName),
          }) as HTMLOptionElement;
          optionGroup.append(option);
        }
        operationSelect.append(optionGroup);
      }
      operationSelect.value = item.quickOperation ?? 'advanced';
      const amountInput = inputField('变化数值', String(item.quickAmount ?? 60));
      amountInput.type = 'number';
      amountInput.step = 'any';
      const quickUnit = el('span', { class: 'quick-rule-unit' });
      const maximumToggle = el('input', { class: 'setting-switch-input', type: 'checkbox' }) as HTMLInputElement;
      maximumToggle.checked = item.quickMaximumEnabled === true;
      const maximumInput = inputField(
        '最高不超过',
        String(item.quickMaximum ?? (formatSelect.value === 'hhmmss' ? 3600 : 100)),
      );
      maximumInput.type = 'number';
      maximumInput.step = 'any';
      const maximumUnit = el('span', { class: 'quick-rule-unit' });
      const maximumLimit = el('div', { class: 'quick-rule-limit' }, [
        el('label', { class: 'quick-rule-limit-toggle' }, [
          maximumToggle,
          el('span', { class: 'setting-switch-track', ariaHidden: 'true' } as any),
          el('span', { text: '最高不超过' }),
        ]),
        maximumInput,
        maximumUnit,
      ]);
      const syncQuickCopy = (): void => {
        const operation = operationSelect.value as QuickGiftOperation;
        const targetName = nameInput.value.trim() || originalName || '属性';
        for (const option of Array.from(operationSelect.querySelectorAll('option'))) {
          option.textContent = quickGiftOperationLabel(option.value as QuickGiftOperation, targetName);
        }
        quickUnit.textContent = quickGiftOperationUnit(operation, formatSelect.value === 'hhmmss');
        amountInput.hidden = !quickGiftOperationUsesAmount(operation);
        amountInput.min = operation === 'set' ? '' : operation.startsWith('random') ? '1' : '0';
        maximumLimit.hidden = !quickGiftOperationSupportsMaximum(operation);
        maximumInput.disabled = !maximumToggle.checked;
        maximumUnit.textContent = formatSelect.value === 'hhmmss' ? '秒' : '单位';
      };
      const syncQuickRule = (): void => {
        const operation = operationSelect.value as QuickGiftOperation;
        const amount = Number(amountInput.value);
        item.quickOperation = operation;
        item.quickAmount = Number.isFinite(amount) ? amount : 0;
        const maximum = Number(maximumInput.value);
        item.quickMaximumEnabled = maximumToggle.checked && quickGiftOperationSupportsMaximum(operation);
        item.quickMaximum = Number.isFinite(maximum) ? maximum : 0;
        const targetName = nameInput.value.trim() || originalName || '属性';
        const formula = buildQuickGiftFormula(
          operation,
          targetName,
          item.quickAmount,
          item.quickMaximumEnabled ? item.quickMaximum : undefined,
        );
        if (formula !== null) formulaInput.value = formula;
        syncQuickCopy();
        updatePreview();
      };
      operationSelect.onchange = syncQuickRule;
      amountInput.oninput = syncQuickRule;
      maximumToggle.onchange = syncQuickRule;
      maximumInput.oninput = syncQuickRule;
      formulaInput.oninput = () => {
        item.quickOperation = 'advanced';
        operationSelect.value = 'advanced';
        syncQuickCopy();
        updatePreview();
      };
      const examples = el('div', { class: 'formula-examples' });
      const presetControls = renderFormulaPresetControls('gift', formulaInput, formulaNameInput, () => updatePreview());
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
          item.quickOperation = 'advanced';
          operationSelect.value = 'advanced';
          syncQuickCopy();
          updatePreview();
        };
        examples.append(example);
      }
      const simulateButton = el('button', {
        class: 'btn formula-simulate guide-rule-simulator',
        type: 'button',
        text: '模拟收到 1 个',
      }) as HTMLButtonElement;
      simulateButton.onclick = () => updatePreview(true);
      const quickBuilder = el('section', { class: 'quick-rule-builder' }, [
        el('div', { class: 'quick-rule-heading' }, [
          el('div', {}, [
            el('strong', { text: '小白模式：用一句话配置' }),
            el('small', { text: '后台仍会保存为同一套规则，可随时切换到高级输入。' }),
          ]),
        ]),
        fieldControl(formulaNameInput),
        el('div', { class: 'quick-rule-sentence' }, [
          el('span', { text: `每收到 1 个“${item.gift.name}”后` }),
          operationSelect,
          amountInput,
          quickUnit,
        ]),
        maximumLimit,
        el('div', { class: 'quick-rule-actions' }, [presetControls.presetList, simulateButton]),
      ]);
      const advanced = el('details', { class: 'rule-advanced-settings' });
      advanced.append(
        el('summary', { text: '高级规则：直接编辑计算表达式' }),
        formulaControl,
        examples,
      );
      row.append(quickBuilder, advanced, preview);
      syncQuickCopy();
      if (item.simulationPreview) renderSimulationPreview();
      else updatePreview();
      return row;
    }

    giftSearch.oninput = renderGiftPicker;
    const manualGift = renderManualGiftAdder(() => {
      pickerCatalog = buildGiftPickerCatalog(state, roomGiftCatalog);
      allGifts = pickerCatalog.gifts;
      renderGiftPicker();
      renderSelectedRules();
      renderGuide();
    }, selected, defaultFormula, (item) => { void hydrateBlindBoxRule(item); });

    const cancelGiftSelectionButton = el('button', {
      class: 'btn ghost',
      type: 'button',
      text: '取消',
    }) as HTMLButtonElement;
    cancelGiftSelectionButton.onclick = () => closeGiftDrawer(false);
    confirmGiftSelectionButton = el('button', {
      class: 'btn guide-confirm-gifts',
      type: 'button',
      text: `确认选择（${selected.size}）`,
    }) as HTMLButtonElement;
    confirmGiftSelectionButton.classList.toggle('guide-gift-selection-ready', selected.size > 0);
    confirmGiftSelectionButton.onclick = () => closeGiftDrawer(true);
    const giftDrawerActions = el('footer', { class: 'gift-picker-drawer-actions' }, [
      giftDrawerSelectionCount,
      el('div', { class: 'gift-picker-drawer-action-buttons' }, [
        cancelGiftSelectionButton,
        confirmGiftSelectionButton,
      ]),
    ]);

    const giftsPanel = el('section', { class: 'gift-binding-panel gift-picker-drawer-body' });
    giftsPanel.append(
      el('div', { class: 'modal-section-heading' }, [
        el('div', {}, [
          el('h3', { text: '选择会影响这个属性的礼物' }),
          el('p', { text: '默认显示已上架和直播中实际收到过的礼物；搜索时会同时显示历史礼物并标注状态。向下滚动会自动加载更多，数字 ID 需要完整匹配。' }),
        ]),
        el('button', { class: 'modal-close gift-picker-drawer-close', type: 'button', text: '×', ariaLabel: '取消礼物选择', onclick: () => closeGiftDrawer(false) } as any),
      ]),
      giftSearch,
      giftPicker,
      manualGift,
      giftDrawerActions,
    );
    giftDrawer.append(giftsPanel);

    const formulasPanel = el('section', { class: 'formula-binding-panel attribute-rules-panel' });
    const formulaHelp = renderFormulaHelp(nameInput.value.trim() || '属性');
    const addGiftButton = el('button', {
      class: 'btn guide-add-gift',
      type: 'button',
      text: '+ 添加礼物',
    }) as HTMLButtonElement;
    addGiftButton.onclick = openGiftDrawer;
    formulasPanel.append(
      el('div', { class: 'modal-section-heading' }, [
        el('div', {}, [
          el('h3', { text: '礼物规则' }),
          el('p', { text: '每个礼物独立配置；连送 N 个会由后台按单个礼物连续执行 N 次。' }),
        ]),
        el('div', { class: 'rules-heading-actions' }, [selectionCount, addGiftButton]),
      ]),
      selectedRules,
      formulaHelp,
      giftDrawer,
    );

    const confirmOutputPreviewButton = el('button', {
      class: 'btn guide-output-confirm',
      type: 'button',
      text: '确认输出预览',
    }) as HTMLButtonElement;
    confirmOutputPreviewButton.onclick = () => {
      editorTutorialProgress.outputPreviewed = true;
      refreshEditorTutorial();
      toast('输出外观已确认', root);
    };
    const outputLessonCard = el('div', { class: 'workbench-lesson-card' }, [
        el('span', { class: 'workbench-lesson-icon', text: '04' }),
        el('div', {}, [
          el('strong', { text: '先确认 OBS 中会显示什么' }),
          el('p', { text: '默认播报、数值状态和皮肤只改变画面；礼物规则与定时计算仍留在托盘后台。' }),
        ]),
        confirmOutputPreviewButton,
    ]);
    outputLessonCard.dataset.tutorialLesson = 'appearance';
    const attributeThemeControl = createDisplayThemeControl(
      displayConfig.themeId ?? state.settings.defaultDisplayThemeId,
      (themeId) => { displayConfig.themeId = themeId; },
      '这个属性的 OBS 皮肤',
      '只影响当前属性；模板已经为玩法选择了推荐皮肤。',
    );
    let valueMappings: AttributeValueMapping[] = displayConfig.valueMappings?.map((mapping) => ({ ...mapping })) ?? [];
    const enumEnabled = el('input', { class: 'setting-switch-input', type: 'checkbox' }) as HTMLInputElement;
    enumEnabled.checked = displayConfig.variant === 'enum';
    const mappingList = el('div', { class: 'enum-mapping-list' });
    const addMappingButton = el('button', { class: 'btn ghost', type: 'button', text: '+ 添加状态' }) as HTMLButtonElement;
    const renderMappings = (): void => {
      mappingList.replaceChildren();
      if (valueMappings.length === 0) {
        mappingList.append(el('div', { class: 'enum-mapping-empty', text: '还没有状态映射；未匹配到的数值仍会按原数字显示。' }));
      }
      valueMappings.forEach((mapping, mappingIndex) => {
        const value = inputField('数值', String(mapping.value));
        value.type = 'number';
        value.step = 'any';
        value.oninput = () => { mapping.value = Number(value.value); };
        const label = inputField('显示文字', mapping.label);
        label.maxLength = 80;
        label.oninput = () => { mapping.label = label.value; };
        const color = el('input', {
          class: 'enum-color-input', type: 'color', value: /^#[0-9a-f]{6}$/i.test(mapping.color ?? '') ? mapping.color : '#fb7299',
          ariaLabel: '状态颜色',
        } as any) as HTMLInputElement;
        const colorValue = el('span', { text: (mapping.color ?? '#FB7299').toUpperCase() });
        color.oninput = () => {
          mapping.color = color.value;
          colorValue.textContent = color.value.toUpperCase();
        };
        const imageUrl = inputField('图片地址（可选）', mapping.imageUrl ?? '');
        imageUrl.type = 'url';
        imageUrl.maxLength = 2048;
        imageUrl.placeholder = 'https://…';
        imageUrl.oninput = () => { mapping.imageUrl = imageUrl.value; };
        const remove = el('button', { class: 'btn text-danger', type: 'button', text: '删除' }) as HTMLButtonElement;
        remove.onclick = () => {
          valueMappings.splice(mappingIndex, 1);
          displayConfig.valueMappings = valueMappings;
          renderMappings();
        };
        mappingList.append(el('article', { class: 'enum-mapping-row' }, [
          fieldControl(value),
          fieldControl(label),
          el('label', { class: 'field enum-color-field' }, [
            el('span', { class: 'field-label', text: '颜色' }),
            el('span', { class: 'enum-color-control' }, [color, colorValue]),
          ]),
          fieldControl(imageUrl),
          remove,
        ]));
      });
      addMappingButton.disabled = !enumEnabled.checked || valueMappings.length >= 50;
    };
    const syncEnumMode = (): void => {
      if (enumEnabled.checked) {
        displayConfig.variant = 'enum';
        if (valueMappings.length === 0) {
          valueMappings.push({ value: Number(valueInput.value) || 0, label: '当前状态', color: '#fb7299' });
        }
      } else if (displayConfig.variant === 'enum') {
        displayConfig.variant = formatSelect.value === 'hhmmss' ? 'timer' : 'number';
      }
      displayConfig.valueMappings = valueMappings;
      mappingList.classList.toggle('is-disabled', !enumEnabled.checked);
      renderMappings();
    };
    enumEnabled.onchange = syncEnumMode;
    addMappingButton.onclick = () => {
      if (valueMappings.length >= 50) return;
      const usedValues = new Set(valueMappings.map((mapping) => mapping.value));
      let value = 0;
      while (usedValues.has(value)) value++;
      valueMappings.push({ value, label: `状态 ${value}`, color: '#fb7299' });
      displayConfig.valueMappings = valueMappings;
      renderMappings();
      mappingList.lastElementChild?.scrollIntoView({ block: 'nearest' });
    };
    const enumMappingSection = el('section', { class: 'enum-mapping-section' }, [
      el('div', { class: 'enum-mapping-heading' }, [
        el('label', { class: 'setting-switch' }, [
          enumEnabled,
          el('span', { class: 'setting-switch-track', ariaHidden: 'true' } as any),
          el('span', { class: 'setting-switch-copy' }, [
            el('strong', { text: '把数值显示成状态' }),
            el('small', { text: '后台仍保存数字；OBS 命中指定数值时改为显示文字、颜色和图片。' }),
          ]),
        ]),
        addMappingButton,
      ]),
      mappingList,
    ]);
    syncEnumMode();
    const outputPanel = el('section', { class: 'attribute-output-panel' }, [
      outputLessonCard,
      el('div', { class: 'runtime-role-grid' }, [
        el('article', {}, [el('strong', { text: '配置页' }), el('span', { text: '修改属性、规则和外观' })]),
        el('article', {}, [el('strong', { text: '托盘后台' }), el('span', { text: '收礼、计算、定时与保存' })]),
        el('article', {}, [el('strong', { text: 'OBS 面板' }), el('span', { text: '只显示一个属性的结果' })]),
      ]),
      el('div', { class: 'attribute-output-fields' }, [
        broadcastMessageControl,
        el('div', { class: 'output-link-preview' }, [
          el('span', { class: 'field-label', text: 'OBS 专属链接' }),
          el('code', {
            text: original
              ? `${location.origin}/?mode=display&attribute=${encodeURIComponent(original.name)}`
              : '创建属性后，在属性卡片中复制专属链接',
          }),
        ]),
      ]),
      enumMappingSection,
      attributeThemeControl,
      el('p', { class: 'modal-tip', text: '默认播报会在没有礼物消息时滚动显示；收到礼物后会临时切换为本次送礼信息。' }),
    ]);

    const cancelButton = el('button', { class: 'btn ghost', type: 'button', text: '取消' }) as HTMLButtonElement;
    cancelButton.onclick = close;
    const saveButton = el('button', { class: 'btn guide-attribute-save', type: 'button', text: original ? '保存修改' : '创建属性' }) as HTMLButtonElement;
    saveButton.onclick = () => {
      void saveAttributeEditor(index, original, nameInput, valueInput, formatSelect, suffixInput, broadcastMessageInput, displayConfig, selected, timerRules, overlay, saveButton);
    };
    modalFooter = el('footer', { class: 'modal-actions attribute-workbench-actions' }, [
      el('span', { class: 'attribute-save-note', text: '保存前会由后台统一校验规则' }),
      cancelButton,
      saveButton,
    ]);
    const lessonStates = getTutorialLessonStates(
      state,
      connectionState === 'connected',
      editorTutorialProgress,
      forcedTutorialLesson,
    ).filter((lesson) => lesson.section);
    const workspace = createAttributeWorkspace({
      ariaLabel: original ? `编辑属性 ${original.name}` : '添加属性',
      header: modalHeader,
      footer: modalFooter,
      sections: [
        { id: 'overview', label: '概览', content: overviewPanel },
        { id: 'rules', label: '礼物规则', badge: () => String(selected.size), content: formulasPanel },
        { id: 'timers', label: '定时器', badge: () => String(timerRules.length), content: timerPanel },
        { id: 'output', label: '输出与预览', content: outputPanel },
      ],
      lessons: lessonStates,
      trainingVisible: !guideDismissed && (editorGuideEnabled || forcedTutorialLesson !== null),
      initialSection: editorGuideEnabled || forcedTutorialLesson !== null
        ? sectionForTutorialLesson(activeTutorialLesson())
        : 'overview',
      onLessonClick: (lesson) => {
        forcedTutorialLesson = lesson;
        guideDismissed = false;
        state.settings.showTutorial = true;
        save();
        refreshEditorTutorial();
      },
    });
    activeEditorWorkspace = workspace;
    modal = workspace.element;
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
      pickerCatalog = buildGiftPickerCatalog(state, roomGiftCatalog);
      allGifts = pickerCatalog.gifts;
      renderGiftPicker();
      renderSelectedRules();
    };
    renderGiftPicker();
    renderSelectedRules();
    for (const item of blindBoxLookups) void hydrateBlindBoxRule(item);
    renderTimerRules();
    refreshEditorTutorial();
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
      const item: SelectedGiftRule = {
        gift,
        formulaName: `${gift.name}规则`,
        formula: defaultFormula(),
        enabled: !editorGuideEnabled,
        quickOperation: 'price',
        quickAmount: 60,
      };
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
    displayConfig: AttributeDisplay,
    selected: Map<number, SelectedGiftRule>,
    timerRules: TimerRule[],
    overlay: HTMLElement,
    saveButton: HTMLButtonElement,
  ): Promise<void> {
    return saveAttributeEditorAsync(index, original, nameInput, valueInput, formatSelect, suffixInput, broadcastMessageInput, displayConfig, selected, timerRules, overlay, saveButton);
  }

  async function saveAttributeEditorAsync(
    index: number | undefined,
    original: Attribute | undefined,
    nameInput: HTMLInputElement,
    valueInput: HTMLInputElement,
    formatSelect: HTMLSelectElement,
    suffixInput: HTMLInputElement,
    broadcastMessageInput: HTMLInputElement,
    displayConfig: AttributeDisplay,
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

    const mappingValues = new Set<number>();
    const normalizedMappings: AttributeValueMapping[] = [];
    for (const mapping of displayConfig.valueMappings ?? []) {
      const mappingValue = Number(mapping.value);
      const label = mapping.label.trim();
      const imageUrl = mapping.imageUrl?.trim() ?? '';
      if (!Number.isFinite(mappingValue) || !label) {
        toast('状态映射需要填写有效数值和显示文字', root);
        return;
      }
      if (mappingValues.has(mappingValue)) {
        toast(`状态映射的数值不能重复：${mappingValue}`, root);
        return;
      }
      if (imageUrl && !/^(https?:\/\/|data:image\/)/i.test(imageUrl)) {
        toast(`“${label}”的图片需要使用 http、https 或 data:image 地址`, root);
        return;
      }
      mappingValues.add(mappingValue);
      normalizedMappings.push({
        value: mappingValue,
        label,
        ...(/^#[0-9a-f]{6}$/i.test(mapping.color ?? '') ? { color: mapping.color } : {}),
        ...(imageUrl ? { imageUrl } : {}),
      });
    }
    displayConfig.valueMappings = normalizedMappings;

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
      id: original?.id ?? createAttributeId(),
      name,
      value,
      unit: format === 'hhmmss' ? 'seconds' : 'none',
      format,
      decimals: original?.decimals ?? 0,
      suffix: format === 'suffix' ? suffixInput.value : '',
      broadcastMessage: broadcastMessageInput.value.trim(),
      display: {
        ...displayConfig,
        themeId: displayConfig.themeId ?? state.settings.defaultDisplayThemeId,
        title: !displayConfig.title || displayConfig.title === originalName ? name : displayConfig.title,
      },
      ...(original?.color ? { color: original.color } : {}),
      ...(original?.createdFromTemplateId ? { createdFromTemplateId: original.createdFromTemplateId } : {}),
      ...(original?.createdFromTemplateVersion !== undefined ? { createdFromTemplateVersion: original.createdFromTemplateVersion } : {}),
    };
    if (index === undefined) state.attributes.push(nextAttribute);
    else state.attributes[index] = nextAttribute;
    if (editorGuideEnabled && state.settings.tutorialReplayMode && nextAttribute.id) {
      state.settings.tutorialTargetAttributeId = nextAttribute.id;
    }
    if (originalName && originalName !== name) {
      state.displayScenes = state.displayScenes.map((scene) => ({
        ...scene,
        attributeNames: scene.attributeNames.map((attributeName) => attributeName === originalName ? name : attributeName),
      }));
      state.activities = state.activities.map((activity) => {
        if (!activity.attributeNames.includes(originalName)) return activity;
        const initialValues = { ...activity.initialValues, [name]: activity.initialValues[originalName] ?? nextAttribute.value };
        delete initialValues[originalName];
        const resultValues = { ...(activity.result?.values ?? {}) };
        if (Object.prototype.hasOwnProperty.call(resultValues, originalName)) {
          resultValues[name] = resultValues[originalName];
          delete resultValues[originalName];
        }
        return {
          ...activity,
          attributeNames: activity.attributeNames.map((attributeName) => attributeName === originalName ? name : attributeName),
          initialValues,
          milestones: activity.milestones.map((milestone) => ({
            ...milestone,
            attributeName: milestone.attributeName === originalName ? name : milestone.attributeName,
          })),
          ...(activity.result ? {
            result: {
              values: resultValues,
              ...(activity.result.winnerAttributeName
                ? { winnerAttributeName: activity.result.winnerAttributeName === originalName ? name : activity.result.winnerAttributeName }
                : {}),
            },
          } : {}),
        };
      });
    }

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
    if (state.settings.tutorialReplayMode) {
      markTutorialLessonComplete(state.settings, 'attribute');
      markTutorialLessonComplete(state.settings, 'template');
      if (editorTutorialProgress.basicsConfigured) markTutorialLessonComplete(state.settings, 'basics');
      if ((editorTutorialProgress.giftCount ?? 0) > 0) markTutorialLessonComplete(state.settings, 'gift');
      if (editorTutorialProgress.giftPreviewed) markTutorialLessonComplete(state.settings, 'rule');
      if (editorTutorialProgress.presetSaved) markTutorialLessonComplete(state.settings, 'preset');
      if (editorTutorialProgress.timerPreviewed) markTutorialLessonComplete(state.settings, 'timer');
      if (editorTutorialProgress.outputPreviewed) markTutorialLessonComplete(state.settings, 'appearance');
      markTutorialLessonComplete(state.settings, 'save');
    }
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
    editorTutorialProgress = { open: false };
    activeEditorWorkspace = null;
    forcedTutorialLesson = null;
    refreshOpenGiftCatalog = null;
    render();
    toast(index === undefined ? '属性已创建' : '属性配置已保存', root);
  }

  function createDisplayThemeControl(
    initialThemeId: DisplayThemeId,
    onSelect: (themeId: DisplayThemeId) => void,
    label: string,
    description: string,
  ): HTMLElement {
    let selectedThemeId = initialThemeId;
    const selectedName = el('span', { class: 'display-theme-current', text: getDisplayTheme(selectedThemeId).name });
    const grid = el('div', { class: 'display-theme-grid' });
    const buttons = new Map<DisplayThemeId, HTMLButtonElement>();
    const refresh = (): void => {
      selectedName.textContent = getDisplayTheme(selectedThemeId).name;
      for (const [themeId, button] of buttons) {
        const active = themeId === selectedThemeId;
        button.classList.toggle('is-selected', active);
        button.setAttribute('aria-pressed', String(active));
        const check = button.querySelector('.display-theme-check');
        if (check) check.textContent = active ? '✓' : '';
      }
    };
    for (const theme of DISPLAY_THEMES) {
      const button = el('button', {
        class: `display-theme-option ${theme.previewClass}`,
        type: 'button',
        ariaPressed: String(theme.id === selectedThemeId),
      } as any) as HTMLButtonElement;
      const swatch = el('span', { class: 'display-theme-swatch' }, [el('span'), el('span')]);
      swatch.style.setProperty('--swatch-accent', theme.accent);
      swatch.style.setProperty('--swatch-bg', theme.surface);
      button.append(
        swatch,
        el('span', {}, [el('strong', { text: theme.name }), el('small', { text: theme.recommendedFor })]),
        el('span', { class: 'display-theme-check' }),
      );
      button.onclick = () => {
        selectedThemeId = theme.id;
        onSelect(theme.id);
        refresh();
      };
      buttons.set(theme.id, button);
      grid.append(button);
    }
    refresh();
    return el('div', { class: 'field setting-control display-theme-setting', role: 'group', ariaLabel: label }, [
      el('div', { class: 'field-label' }, [el('span', { text: label }), selectedName]),
      el('p', { class: 'display-theme-description', text: description }),
      grid,
    ]);
  }

  function renderAdvancedSettings(): void {
    const details = el('details', { class: 'advanced-settings' });
    details.open = advancedSettingsOpen;
    details.ontoggle = () => { advancedSettingsOpen = details.open; };
    details.append(el('summary', {}, [
      el('span', { text: '外观与数据' }),
      el('small', { text: '面板外观、自动更新和配置备份' }),
    ]));
    const settingsGrid = el('div', { class: 'advanced-settings-grid' });

    const appearance = el('section', { class: 'workspace-card advanced-card' });
    appearance.append(el('h3', { text: 'OBS 面板外观' }));

    const defaultThemeControl = createDisplayThemeControl(
      state.settings.defaultDisplayThemeId,
      (themeId) => {
        state.settings.defaultDisplayThemeId = themeId;
        save();
      },
      '新属性默认皮肤',
      '未单独指定皮肤的属性会使用这里的选择。',
    );

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
      defaultThemeControl,
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
      const blob = new Blob([JSON.stringify(createConfigBackup(state), null, 2)], { type: 'application/json' });
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
        let parsed: unknown;
        try {
          parsed = JSON.parse(text) as unknown;
          const importedState = mergeConfigBackup(state, parsed);
          if (!confirmRoomSwitch(importedState.roomId)) {
            importInput.value = '';
            return;
          }
          state = isRoomSwitch(importedState.roomId) ? clearRoomScopedRecords(importedState) : importedState;
        } catch (error) {
          toast(error instanceof Error ? error.message : '文件解析失败', root);
          return;
        }
        state.settings.theme = normalizeConfigTheme(state.settings.theme);
        roomGiftCatalogRoomId = '';
        roomGiftCatalog = [];
        roomAnchorInfo = null;
        roomAnchorInfoRoomId = '';
        applyConfigTheme(state.settings.theme);
        save();
        render();
        void refreshBiliAuth();
        void refreshRoomAnchorInfo(true);
        void refreshRoomGiftCatalog(true);
        toast('配置已导入', root);
      });
    };
    const importButton = el('button', { class: 'btn ghost', type: 'button', text: '导入配置' }) as HTMLButtonElement;
    importButton.onclick = () => importInput.click();
    const diagnosticLogLink = el('a', {
      class: 'btn ghost diagnostic-log-export',
      href: '/api/diagnostics/log',
      download: `gift-panel-runtime-${new Date().toISOString().slice(0, 10)}.log`,
      text: '导出运行日志',
      title: '导出连接、礼物解析和盲盒识别日志',
    }) as HTMLAnchorElement;
    const resetButton = el('button', { class: 'btn text-danger', type: 'button', text: '恢复默认' }) as HTMLButtonElement;
    resetButton.onclick = () => {
      if (!confirm('确定恢复默认设置？当前配置将被清除。')) return;
      resetState();
      location.reload();
    };
    dataCard.append(
      el('div', { class: 'data-actions' }, [exportButton, importButton, diagnosticLogLink, importInput, resetButton]),
      el('small', { class: 'advanced-copy diagnostic-log-note', text: '运行日志包含连接状态、礼物 ID、发送者 UID 和盲盒识别结果，不包含 Cookie 或登录凭据。' }),
    );

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
  void refreshRoomAnchorInfo();
  void refreshRoomGiftCatalog();
  void refreshHostedChangelog();
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

function buildGiftPickerCatalog(state: AppState, roomGiftCatalog: GiftInfo[]): GiftPickerCatalog {
  const configuredGifts = state.rules
    .map((rule) => findGift(state, rule.giftId))
    .filter((gift): gift is GiftInfo => gift !== undefined);
  const hasLiveListingStatus = roomGiftCatalog.some((gift) => typeof gift.listed === 'boolean');
  const candidates: Array<{ gift: GiftInfo; availability: GiftAvailability }> = [
    ...roomGiftCatalog.map((gift) => ({
      gift,
      availability: (hasLiveListingStatus && gift.listed === false ? 'historical' : 'listed') as GiftAvailability,
    })),
    ...state.recentGifts.map((gift) => ({
      gift,
      availability: (gift.count > 0 || gift.lastReceived > 0 ? 'observed' : 'historical') as GiftAvailability,
    })),
    ...configuredGifts.map((gift) => ({ gift, availability: 'historical' as GiftAvailability })),
    ...builtinCatalog.map((gift) => ({
      gift,
      availability: (isSpecialEventGift(gift) ? 'special' : 'historical') as GiftAvailability,
    })),
  ];
  const priority: Record<GiftAvailability, number> = { special: 4, listed: 3, observed: 2, historical: 1 };
  const byDisplayKey = new Map<string, { gift: GiftInfo; availability: GiftAvailability }>();
  for (const candidate of candidates) {
    const key = giftDisplayKey(candidate.gift);
    const existing = byDisplayKey.get(key);
    if (!existing || priority[candidate.availability] > priority[existing.availability]) {
      byDisplayKey.set(key, candidate);
    }
  }
  const merged = Array.from(byDisplayKey.values());
  const availabilityById = new Map(merged.map(({ gift, availability }) => [gift.id, availability]));
  const gifts = sortGiftsByUsage(merged.map(({ gift }) => gift), configuredGifts, state.recentGifts)
    .sort((left, right) => Number(isSpecialEventGift(right)) - Number(isSpecialEventGift(left)));
  return { gifts, availabilityById, hasLiveListingStatus };
}

function configStructureSignature(state: AppState): string {
  return JSON.stringify({
    roomId: state.roomId,
    attributes: state.attributes.map(({ value: _value, ...attribute }) => attribute),
    displayScenes: state.displayScenes,
    rules: state.rules,
    timerRules: state.timerRules,
    formulaPresets: state.formulaPresets,
    settings: state.settings,
    giftCatalog: state.giftCatalog,
  });
}

function activityStateSignature(state: AppState): string {
  return JSON.stringify(state.activities);
}

function contributionStateSignature(state: AppState): string {
  return JSON.stringify(state.contributions);
}

function giftHistoryStateSignature(state: AppState): string {
  return JSON.stringify(state.log.filter((entry) => entry.source !== 'timer'));
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

function attributeLiveValueElement(tag: 'small' | 'strong', attribute: Attribute): HTMLElement {
  const value = el(tag, { class: 'attribute-live-value', text: formatValue(attribute.value, attribute) });
  value.dataset.attributeName = attribute.name;
  return value;
}

function attributeValueElement(attribute: Attribute): HTMLElement {
  const value = attributeLiveValueElement('strong', attribute);
  value.classList.add('attribute-current-value');
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
  return giftPriceDescription(gift);
}

function emptyState(text: string): HTMLElement {
  const empty = el('div', { class: 'empty' });
  empty.append(createBrandIcon(44, 'empty-brand-icon'), el('span', { text }));
  return empty;
}

function transparentPixel(): string {
  return 'data:image/gif;base64,R0lGODlhAQABAIAAAAAAAP///yH5BAEAAAAALAAAAAABAAEAAAIBRAA7';
}

function tutorialLessonRequiresAttribute(lesson: TutorialLesson): boolean {
  return !['room', 'attribute', 'template'].includes(lesson);
}

function findTutorialAttributeIndex(state: Pick<AppState, 'attributes' | 'settings'>): number {
  const targetId = state.settings.tutorialTargetAttributeId?.trim();
  if (targetId) {
    const storedIndex = state.attributes.findIndex((attribute) => attribute.id === targetId);
    if (storedIndex >= 0) return storedIndex;
  }
  const namedIndex = state.attributes.findIndex((attribute) => attribute.name === '加班时间');
  if (namedIndex >= 0) return namedIndex;
  return state.attributes.findIndex((attribute) => attribute.createdFromTemplateId === 'overtime');
}

function ensureTutorialAttributeTarget(state: Pick<AppState, 'attributes' | 'settings'>): number {
  const index = findTutorialAttributeIndex(state);
  if (index < 0) {
    delete state.settings.tutorialTargetAttributeId;
    return -1;
  }
  const attribute = state.attributes[index];
  attribute.id ??= createAttributeId();
  state.settings.tutorialTargetAttributeId = attribute.id;
  return index;
}

function createAttributeId(): string {
  const uuid = globalThis.crypto?.randomUUID?.();
  return uuid ? `attribute-${uuid}` : `attribute-${Date.now()}-${Math.random().toString(36).slice(2, 9)}`;
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

function normalizeConfigTheme(theme: unknown): 'dark' | 'light' {
  return theme === 'light' ? 'light' : 'dark';
}
