import type { RuntimeConnectionState } from '../../backend';
import { formatDurationZh } from '../../duration';
import type { SimplePlayDraft, SimplePlayTransitionImpact } from '../../simple-play';
import type { GiftInfo } from '../../types';
import { el } from '../common';
import { createGiftPicker, createGiftLoginBadge, type GiftPickerCatalog } from './gift-picker';

type SimpleTemplateId = SimplePlayDraft['templateId'];
type OvertimeGiftAction = NonNullable<SimplePlayDraft['overtimeGiftActions']>[number];
type OvertimeOperation = OvertimeGiftAction['operation'];

export interface SimplePlayView {
  draft: SimplePlayDraft;
  attributeName: string;
  currentValue: number;
  enabled: boolean;
  fingerprintChanged?: boolean;
}

export interface SimpleModeCounts {
  attributes: number;
  rules: number;
  timers: number;
  activities: number;
  scenes: number;
}

export interface SimpleModeOptions {
  roomId: string;
  connectionState: RuntimeConnectionState;
  loggedIn: boolean;
  gifts: GiftInfo[];
  play?: SimplePlayView;
  session: SimpleModeSession;
  extra: SimpleModeCounts;
  onConnect: (roomId: string) => Promise<void>;
  onLogin: () => void;
  onSave: (draft: SimplePlayDraft) => Promise<void>;
  onToggleEnabled: (enabled: boolean) => Promise<void>;
  onReset: () => Promise<void>;
  onCopyObs: () => Promise<void>;
  getObsUrl: () => string | undefined;
  previewTransition: (draft: SimplePlayDraft) => SimplePlayTransitionImpact;
  onRefresh: () => void;
  onSwitchAdvanced: () => void;
  onDone: () => void;
}

export interface SimpleModeSession {
  step: number;
  draft: SimplePlayDraft;
  requestedRoom: string;
  saving: boolean;
  saved: boolean;
  copied: boolean;
  message: string;
  messageTone: 'normal' | 'error';
  confirmAction: 'reset' | 'replace' | 'adjust' | null;
}

export interface SimpleModeUi {
  element: HTMLElement;
}

const OPERATION_COPY: Record<OvertimeOperation, { label: string; verb: string; amount: boolean }> = {
  add: { label: '增加', verb: '增加', amount: true },
  subtract: { label: '减少', verb: '减少', amount: true },
  double: { label: '翻倍', verb: '翻倍', amount: false },
  halve: { label: '减半', verb: '减半', amount: false },
  reset: { label: '归零', verb: '归零', amount: false },
};

const TEMPLATE_COPY: Record<SimpleTemplateId, {
  title: string;
  summary: string;
  icon: string;
  giftSlot: string;
  giftLabel: string;
}> = {
  overtime: { title: '加班机', summary: '礼物改变剩余直播时间，并自动倒计时。', icon: '⏱', giftSlot: 'overtime', giftLabel: '加班礼物' },
  counter: { title: '礼物计数器', summary: '收到一个礼物，就增加固定数量。', icon: '＋', giftSlot: 'count', giftLabel: '计数礼物' },
  goal: { title: '目标进度', summary: '礼物按价格推进一个共同目标。', icon: '◎', giftSlot: 'progress', giftLabel: '推进礼物' },
};

export function parseBilibiliRoomId(value: string): string | null {
  const trimmed = value.trim();
  if (/^\d+$/.test(trimmed)) return trimmed;
  try {
    const url = new URL(trimmed);
    if (!/(^|\.)live\.bilibili\.com$/i.test(url.hostname)) return null;
    const roomId = url.pathname.split('/').filter(Boolean)[0] ?? '';
    return /^\d+$/.test(roomId) ? roomId : null;
  } catch {
    return null;
  }
}

export function formatSimpleCurrentValue(templateId: SimpleTemplateId, value: number): string {
  if (templateId !== 'overtime') return Number.isFinite(value) ? value.toLocaleString('zh-CN', { maximumFractionDigits: 4 }) : '0';
  const seconds = Math.max(0, Math.round(Number(value) || 0));
  const hours = Math.floor(seconds / 3600);
  const minutes = Math.floor((seconds % 3600) / 60);
  const remainder = seconds % 60;
  return [hours, minutes, remainder].map((part) => String(part).padStart(2, '0')).join(':');
}

export function simpleDraftSummary(draft: SimplePlayDraft, gifts: GiftInfo[]): string[] {
  const slot = TEMPLATE_COPY[draft.templateId].giftSlot;
  const giftIds = draft.gifts[slot] ?? [];
  if (draft.templateId === 'overtime') {
    const actions = draft.overtimeGiftActions ?? [];
    return giftIds.map((giftId) => {
      const gift = gifts.find((candidate) => candidate.id === giftId);
      const action = actions.find((candidate) => candidate.giftId === giftId) ?? { giftId, operation: 'add' as const, seconds: 60 };
      const copy = OPERATION_COPY[action.operation];
      return `${gift?.name ?? `礼物 ${giftId}`}：${copy.verb}${copy.amount ? ` ${formatDurationZh(action.seconds ?? 60)}` : ''}`;
    });
  }
  const amount = Number(draft.parameters[draft.templateId === 'counter' ? 'amount' : 'perYuan']) || 1;
  return giftIds.map((giftId) => {
    const gift = gifts.find((candidate) => candidate.id === giftId);
    return draft.templateId === 'counter'
      ? `${gift?.name ?? `礼物 ${giftId}`}：每个增加 ${amount}`
      : `${gift?.name ?? `礼物 ${giftId}`}：每 1 元推进 ${amount}`;
  });
}

export function createDefaultSimpleDraft(templateId: SimpleTemplateId): SimplePlayDraft {
  if (templateId === 'overtime') {
    return {
      templateId,
      parameters: { name: '加班时间', maxSeconds: 0 },
      gifts: { overtime: [] },
      overtimeGiftActions: [],
      displayThemeId: 'glass',
    };
  }
  if (templateId === 'counter') {
    return {
      templateId,
      parameters: { name: '挑战次数', amount: 1 },
      gifts: { count: [] },
      displayThemeId: 'pixel',
    };
  }
  return {
    templateId,
    parameters: { name: '目标进度', target: 100, perYuan: 1 },
    gifts: { progress: [] },
    displayThemeId: 'kawaii',
  };
}

export function createSimpleModeSession(play: SimplePlayView | undefined, roomId: string): SimpleModeSession {
  return {
    step: play ? 0 : 1,
    draft: cloneDraft(play?.draft ?? createDefaultSimpleDraft('overtime')),
    requestedRoom: roomId,
    saving: false,
    saved: false,
    copied: false,
    message: '',
    messageTone: 'normal',
    confirmAction: null,
  };
}

export function createSimpleMode(options: SimpleModeOptions): SimpleModeUi {
  const element = el('section', { class: 'simple-mode' });
  const session = options.session;
  let step = session.step;
  let draft = cloneDraft(session.draft);
  let requestedRoom = session.requestedRoom;
  let saving = session.saving;
  let saved = session.saved;
  let copied = session.copied;
  let message = session.message;
  let messageTone: 'normal' | 'error' = session.messageTone;
  let confirmAction: 'reset' | 'replace' | 'adjust' | null = session.confirmAction;
  let focusAfterRender: string | undefined;

  const syncSession = (): void => {
    session.step = step;
    session.draft = cloneDraft(draft);
    session.requestedRoom = requestedRoom;
    session.saving = saving;
    session.saved = saved;
    session.copied = copied;
    session.message = message;
    session.messageTone = messageTone;
    session.confirmAction = confirmAction;
  };

  const setMessage = (text = '', tone: 'normal' | 'error' = 'normal'): void => {
    message = text;
    messageTone = tone;
    syncSession();
  };

  const showWizard = (nextStep: number, nextDraft?: SimplePlayDraft): void => {
    if (nextDraft) draft = cloneDraft(nextDraft);
    step = nextStep;
    saved = false;
    copied = false;
    confirmAction = null;
    setMessage();
    render();
  };

  function render(): void {
    syncSession();
    element.replaceChildren();
    if (step === 0 && options.play) renderHome();
    else renderWizard();
    const selector = focusAfterRender;
    focusAfterRender = undefined;
    if (selector) globalThis.queueMicrotask(() => element.querySelector<HTMLElement>(selector)?.focus());
  }

  function renderHome(): void {
    const play = options.play as SimplePlayView;
    const template = TEMPLATE_COPY[play.draft.templateId];
    const header = el('div', { class: 'simple-home-header' }, [
      el('div', {}, [
        el('span', { class: 'section-kicker', text: '简单模式' }),
        el('h1', { text: '你的直播玩法' }),
        el('p', { text: '这里只管理这一台玩法；其他高级配置会继续在后台运行。' }),
      ]),
    ]);
    const value = el('strong', {
      class: 'simple-current-value',
      text: formatSimpleCurrentValue(play.draft.templateId, play.currentValue),
      ariaLabel: `${play.attributeName}当前值`,
    } as any);
    value.dataset.attributeName = play.attributeName;
    value.dataset.simpleTemplateId = play.draft.templateId;
    const toggle = el('button', {
      class: `btn simple-enabled-toggle${play.enabled ? ' is-enabled' : ''}`,
      type: 'button',
      text: play.enabled ? '暂停玩法' : '启用玩法',
      ariaPressed: String(play.enabled),
    } as any) as HTMLButtonElement;
    toggle.onclick = () => runAction(toggle, async () => {
      await options.onToggleEnabled(!play.enabled);
      play.enabled = !play.enabled;
      render();
    });
    const reset = el('button', {
      class: `btn ghost simple-reset${confirmAction === 'reset' ? ' is-confirming' : ''}`,
      type: 'button',
      text: confirmAction === 'reset' ? '再次点击确认归零' : '归零',
    }) as HTMLButtonElement;
    reset.onclick = () => {
      if (confirmAction !== 'reset') {
        confirmAction = 'reset';
        render();
        return;
      }
      void runAction(reset, async () => {
        await options.onReset();
        play.currentValue = 0;
        confirmAction = null;
        render();
      });
    };
    const adjust = el('button', {
      class: `btn ghost simple-adjust${confirmAction === 'adjust' ? ' is-confirming' : ''}`,
      type: 'button',
      text: confirmAction === 'adjust' ? '确认覆盖高级改动' : '调整玩法',
    }) as HTMLButtonElement;
    adjust.onclick = () => {
      if (play.fingerprintChanged && confirmAction !== 'adjust') {
        confirmAction = 'adjust';
        setMessage('继续后会重建简单模式管理的礼物规则和定时器；其他高级引用会保留。');
        render();
        return;
      }
      showWizard(3, play.draft);
    };
    const replace = el('button', {
      class: `btn ghost simple-replace${confirmAction === 'replace' ? ' is-confirming' : ''}`,
      type: 'button',
      text: confirmAction === 'replace' ? '确认换玩法' : '换玩法',
    }) as HTMLButtonElement;
    replace.onclick = () => {
      if (confirmAction !== 'replace') {
        confirmAction = 'replace';
        render();
        return;
      }
      showWizard(2, createDefaultSimpleDraft(play.draft.templateId));
    };
    const copy = el('button', { class: 'btn simple-copy-obs', type: 'button', text: '复制 OBS 链接' }) as HTMLButtonElement;
    copy.onclick = () => runAction(copy, options.onCopyObs, '已复制');
    const card = el('article', { class: `simple-live-card is-${play.draft.templateId}` }, [
      el('div', { class: 'simple-live-heading' }, [
        el('span', { class: 'simple-template-icon', text: template.icon }),
        el('div', {}, [el('span', { text: template.title }), el('h2', { text: play.attributeName })]),
        el('span', { class: `simple-state-badge${play.enabled ? ' is-running' : ''}`, text: play.enabled ? '运行中' : '已暂停' }),
      ]),
      el('div', { class: 'simple-value-panel' }, [el('small', { text: '当前值' }), value]),
      el('div', { class: 'simple-primary-actions' }, [toggle, reset, copy]),
      el('div', { class: 'simple-secondary-actions' }, [adjust, replace]),
    ]);
    const rules = simpleDraftSummary(play.draft, options.gifts);
    const detail = el('article', { class: 'simple-detail-card' }, [
      el('h3', { text: '礼物规则' }),
      rules.length > 0
        ? el('ul', { class: 'simple-rule-summary' }, rules.map((line) => el('li', { text: line })))
        : el('p', { text: '还没有选择礼物。点击“调整玩法”即可添加。' }),
    ]);
    const aside = el('aside', { class: 'simple-home-aside' });
    if (!options.loggedIn) {
      const login = el('button', { class: 'btn simple-login-cta', type: 'button', text: '扫码登录' }) as HTMLButtonElement;
      login.onclick = options.onLogin;
      aside.append(el('article', { class: 'simple-notice-card is-login' }, [
        el('strong', { text: '登录后盲盒识别更完整' }),
        el('p', { text: '不登录不影响普通礼物规则；匿名观众信息可能不完整。' }),
        login,
      ]));
    }
    if (play.fingerprintChanged) {
      aside.append(el('article', { class: 'simple-notice-card is-warning', role: 'status' } as any, [
        el('strong', { text: '这台玩法已在完整配置中修改' }),
        el('p', { text: '继续调整会以当前简单玩法设置重新整理关联规则，请先确认高级改动是否需要保留。' }),
      ]));
    }
    const extra = totalExtra(options.extra);
    if (extra > 0) {
      aside.append(el('article', { class: 'simple-notice-card is-extra' }, [
        el('strong', { text: `另有 ${extra} 项高级配置继续运行` }),
        el('p', { text: extraDescription(options.extra) }),
        el('button', { class: 'btn ghost', type: 'button', text: '前往完整配置', onclick: options.onSwitchAdvanced }),
      ]));
    }
    element.append(header, el('div', { class: 'simple-home-grid' }, [el('div', { class: 'simple-home-main' }, [card, detail]), aside]));
  }

  function renderWizard(): void {
    const labels = ['连接直播间', '选择玩法', '设置玩法', '确认使用'];
    const progress = el('ol', { class: 'simple-wizard-progress', ariaLabel: '设置进度' } as any);
    labels.forEach((label, index) => {
      const number = index + 1;
      progress.append(el('li', { class: `${number === step ? 'is-active' : ''}${number < step ? ' is-done' : ''}` }, [
        el('span', { text: number < step ? '✓' : String(number) }),
        el('strong', { text: label }),
      ]));
    });
    const body = el('div', { class: 'simple-wizard-body' });
    if (step === 1) renderRoomStep(body);
    else if (step === 2) renderTemplateStep(body);
    else if (step === 3) renderConfigurationStep(body);
    else renderConfirmationStep(body);
    const status = el('p', { class: `simple-wizard-message${messageTone === 'error' ? ' is-error' : ''}`, text: message, role: 'status' } as any);
    const extraCount = totalExtra(options.extra);
    const extraNotice = extraCount > 0
      ? el('p', {
        class: 'simple-wizard-extra',
        text: `另有 ${extraCount} 项高级配置会继续运行：${extraDescription(options.extra)}。`,
        role: 'status',
      } as any)
      : null;
    element.append(
      el('header', { class: 'simple-wizard-header' }, [
        el('div', {}, [
          el('span', { class: 'section-kicker', text: options.play ? '调整简单玩法' : '第一次设置' }),
          el('h1', { text: labels[step - 1] }),
          el('p', { text: wizardLead(step) }),
          el('div', { class: 'room-anchor-host simple-wizard-anchor', ariaLive: 'polite' } as any),
        ]),
      ]),
      progress,
      ...(extraNotice ? [extraNotice] : []),
      body,
      status,
    );
  }

  function renderRoomStep(body: HTMLElement): void {
    const input = el('input', {
      class: 'field-input simple-room-input',
      value: requestedRoom,
      placeholder: '房间号，或 https://live.bilibili.com/…',
      ariaLabel: 'B 站直播间房间号或完整地址',
    } as any) as HTMLInputElement;
    input.oninput = () => {
      requestedRoom = input.value;
      syncSession();
    };
    const next = el('button', { class: 'btn simple-room-next', type: 'button', text: options.loggedIn ? '连接并继续' : '连接并继续（暂不登录）' }) as HTMLButtonElement;
    next.onclick = () => {
      const roomId = parseBilibiliRoomId(requestedRoom);
      if (!roomId) {
        setMessage('请输入数字房间号，或完整的 live.bilibili.com 直播地址。', 'error');
        render();
        return;
      }
      requestedRoom = roomId;
      void runAction(next, async () => {
        step = 2;
        syncSession();
        try {
          await options.onConnect(roomId);
        } catch (error) {
          step = 1;
          syncSession();
          throw error;
        }
        setMessage();
        render();
      });
    };
    const login = el('button', { class: 'btn simple-room-login', type: 'button', text: options.loggedIn ? '已登录' : '扫码登录（强烈建议）' }) as HTMLButtonElement;
    login.disabled = options.loggedIn;
    login.onclick = options.onLogin;
    body.append(
      el('div', { class: 'simple-room-grid' }, [
        el('article', { class: 'simple-step-card' }, [
          el('span', { class: 'simple-step-number', text: '1' }),
          el('h2', { text: '粘贴直播间地址即可' }),
          el('p', { text: '我们会自动取出房间号并连接。以后打开程序会继续使用这个房间。' }),
          input,
          el('small', { text: connectionCopy(options.connectionState) }),
          el('div', { class: 'room-anchor-host', ariaLive: 'polite' } as any),
        ]),
        el('article', { class: 'simple-login-recommendation' }, [
          el('span', { class: 'simple-login-mark', text: '推荐' }),
          el('h2', { text: '现在扫码登录' }),
          el('p', { text: '登录可识别盲盒实际开出的礼物，并尽量补全观众昵称。普通 B 站账号即可。' }),
          login,
          el('small', { text: '可以跳过；普通礼物和 OBS 面板仍可正常使用。登录信息只加密保存在本机。' }),
        ]),
      ]),
      el('div', { class: 'simple-step-actions' }, [next]),
    );
  }

  function renderTemplateStep(body: HTMLElement): void {
    const grid = el('div', { class: 'simple-template-grid' });
    (Object.keys(TEMPLATE_COPY) as SimpleTemplateId[]).forEach((templateId) => {
      const copy = TEMPLATE_COPY[templateId];
      const selected = draft.templateId === templateId;
      const button = el('button', {
        class: `simple-template-card is-${templateId}${selected ? ' is-selected' : ''}`,
        type: 'button',
        ariaPressed: String(selected),
      } as any) as HTMLButtonElement;
      button.onclick = () => {
        draft = createDefaultSimpleDraft(templateId);
        focusAfterRender = `.simple-template-card.is-${templateId}`;
        render();
      };
      button.append(el('span', { class: 'simple-template-icon', text: copy.icon }), el('strong', { text: copy.title }), el('p', { text: copy.summary }), el('span', { class: 'simple-template-select', text: selected ? '✓ 已选择' : '选择' }));
      grid.append(button);
    });
    const back = el('button', { class: 'btn ghost', type: 'button', text: '上一步' }) as HTMLButtonElement;
    back.onclick = () => showWizard(options.play ? 0 : 1);
    const next = el('button', { class: 'btn', type: 'button', text: '下一步' }) as HTMLButtonElement;
    next.onclick = () => showWizard(3);
    body.append(grid, el('div', { class: 'simple-step-actions' }, [back, next]));
  }

  function renderConfigurationStep(body: HTMLElement): void {
    const fields = el('div', { class: 'simple-parameter-grid' });
    const nameLabel = draft.templateId === 'overtime' ? '显示名称' : draft.templateId === 'counter' ? '计数名称' : '目标名称';
    const preservesObsAddress = options.play?.draft.templateId === draft.templateId;
    fields.append(simpleField(
      nameLabel,
      String(draft.parameters.name ?? ''),
      (value) => {
        draft.parameters.name = value;
        syncSession();
      },
      preservesObsAddress,
      preservesObsAddress ? '调整同一玩法时保持名称不变，现有 OBS 链接会继续有效。' : undefined,
    ));
    if (draft.templateId === 'overtime') {
      fields.append(simpleNumberField('最多累计', Number(draft.parameters.maxSeconds ?? 0), '秒（0 为不限）', 0, (value) => {
        draft.parameters.maxSeconds = value;
        syncSession();
      }, true));
    } else if (draft.templateId === 'counter') {
      fields.append(simpleNumberField('每个礼物增加', Number(draft.parameters.amount ?? 1), '', 0.01, (value) => {
        draft.parameters.amount = value;
        syncSession();
      }));
    } else {
      fields.append(simpleNumberField('目标值', Number(draft.parameters.target ?? 100), '', 1, (value) => {
        draft.parameters.target = value;
        syncSession();
      }));
    }
    const pickerHost = el('section', { class: 'simple-gift-section' }, [
      el('div', {}, [el('h2', { text: TEMPLATE_COPY[draft.templateId].giftLabel }), el('p', { text: '可选择多个礼物。带“建议登录”的盲盒在匿名模式下可能无法自动识别。' })]),
    ]);
    const pickerCatalog: GiftPickerCatalog = {
      gifts: options.gifts,
      availabilityById: new Map(options.gifts.map((gift) => [gift.id, 'historical' as const])),
      hasLiveListingStatus: false,
    };
    const slot = TEMPLATE_COPY[draft.templateId].giftSlot;
    const picker = createGiftPicker({
      catalog: pickerCatalog,
      gridClassName: 'simple-gift-picker-grid',
      searchClassName: 'simple-gift-search',
      isSelected: (gift) => (draft.gifts[slot] ?? []).includes(gift.id),
      onToggle: (gift, selected) => {
        focusAfterRender = `[data-gift-id="${gift.id}"]`;
        const current = draft.gifts[slot] ?? [];
        draft.gifts[slot] = selected ? [...current, gift.id] : current.filter((giftId) => giftId !== gift.id);
        if (draft.templateId === 'overtime') {
          const actions = draft.overtimeGiftActions ?? [];
          draft.overtimeGiftActions = selected
            ? [...actions.filter((action) => action.giftId !== gift.id), { giftId: gift.id, operation: 'add', seconds: 60 }]
            : actions.filter((action) => action.giftId !== gift.id);
        }
      },
      onSelectionChange: render,
    });
    pickerHost.append(picker.search, picker.grid);
    if (draft.templateId === 'overtime' && (draft.gifts[slot] ?? []).length > 0) pickerHost.append(renderOvertimeActions());
    if (!options.loggedIn && (draft.gifts[slot] ?? []).some((giftId) => options.gifts.find((gift) => gift.id === giftId)?.requiresLogin)) {
      pickerHost.append(el('p', { class: 'simple-anonymous-warning', role: 'status' } as any, [
        '已选择建议登录识别的盲盒。匿名模式下无法保证触发规则。',
        el('button', { class: 'btn ghost', type: 'button', text: '现在登录', onclick: options.onLogin }),
      ] as any));
    }
    const back = el('button', { class: 'btn ghost', type: 'button', text: '上一步' }) as HTMLButtonElement;
    back.onclick = () => showWizard(2);
    const next = el('button', { class: 'btn', type: 'button', text: '检查设置' }) as HTMLButtonElement;
    next.onclick = () => {
      const error = validateDraft(draft);
      if (error) {
        setMessage(error, 'error');
        render();
        return;
      }
      showWizard(4);
    };
    body.append(fields, pickerHost, el('div', { class: 'simple-step-actions' }, [back, next]));
  }

  function renderOvertimeActions(): HTMLElement {
    const list = el('div', { class: 'simple-overtime-actions' }, [el('h3', { text: '每个礼物收到后做什么' })]);
    for (const giftId of draft.gifts.overtime ?? []) {
      const gift = options.gifts.find((candidate) => candidate.id === giftId);
      const action = draft.overtimeGiftActions?.find((candidate) => candidate.giftId === giftId)
        ?? { giftId, operation: 'add' as const, seconds: 60 };
      const select = el('select', { class: 'field-input simple-operation-select', ariaLabel: `${gift?.name ?? giftId}的动作` } as any) as HTMLSelectElement;
      select.dataset.giftId = String(giftId);
      (Object.keys(OPERATION_COPY) as OvertimeOperation[]).forEach((operation) => select.append(el('option', { value: operation, text: OPERATION_COPY[operation].label })));
      select.value = action.operation;
      select.onchange = () => {
        focusAfterRender = `.simple-operation-select[data-gift-id="${giftId}"]`;
        updateOvertimeAction(giftId, select.value as OvertimeOperation, action.seconds);
      };
      const row = el('div', { class: 'simple-overtime-action-row' }, [
        el('span', { class: 'simple-action-gift' }, [
          gift ? giftChip(gift) : el('strong', { text: `礼物 ${giftId}` }),
          ...(gift?.requiresLogin ? [createGiftLoginBadge(gift) as HTMLElement] : []),
        ]),
        select,
      ]);
      if (OPERATION_COPY[action.operation].amount) {
        const amount = el('input', { class: 'field-input simple-action-seconds', type: 'number', min: '1', step: '1', value: String(action.seconds ?? 60), ariaLabel: `${gift?.name ?? giftId}时长（秒）` } as any) as HTMLInputElement;
        const readable = el('small', { class: 'simple-duration-readable', text: durationInputPreview(action.seconds ?? 60, false) });
        amount.oninput = () => {
          const seconds = Number(amount.value);
          updateOvertimeAction(giftId, action.operation, seconds, false);
          readable.textContent = durationInputPreview(seconds, false);
        };
        row.append(el('div', { class: 'simple-action-amount' }, [amount, el('span', { text: '秒' }), readable]));
      }
      list.append(row);
    }
    return list;
  }

  function updateOvertimeAction(giftId: number, operation: OvertimeOperation, seconds = 60, rerender = true): void {
    const actions = draft.overtimeGiftActions ?? [];
    draft.overtimeGiftActions = [
      ...actions.filter((candidate) => candidate.giftId !== giftId),
      { giftId, operation, ...(OPERATION_COPY[operation].amount ? { seconds: Number(seconds) } : {}) },
    ];
    syncSession();
    if (rerender) render();
  }

  function renderConfirmationStep(body: HTMLElement): void {
    const summary = simpleDraftSummary(draft, options.gifts);
    const impact = options.previewTransition(cloneDraft(draft));
    const obsUrl = options.getObsUrl();
    const selectedSlot = TEMPLATE_COPY[draft.templateId].giftSlot;
    const selectedLoginGift = (draft.gifts[selectedSlot] ?? [])
      .some((giftId) => options.gifts.find((gift) => gift.id === giftId)?.requiresLogin);
    const previewValue = draft.templateId === 'overtime' ? '01:30:00' : draft.templateId === 'counter' ? '12' : `68 / ${Number(draft.parameters.target) || 100}`;
    const preview = obsUrl && (saved || options.play)
      ? el('div', { class: 'simple-real-preview-frame-shell' }, [
        el('iframe', {
          class: 'simple-real-preview-frame',
          src: obsUrl,
          title: `${TEMPLATE_COPY[draft.templateId].title} OBS 实际预览`,
        }),
      ])
      : el('div', { class: `simple-real-preview is-${draft.templateId}` }, [
        el('span', { text: String(draft.parameters.name ?? '') }),
        el('strong', { text: previewValue }),
        draft.templateId === 'goal' ? el('span', { class: 'simple-preview-meter' }, [el('span')]) : el('span'),
      ]);
    body.append(el('div', { class: 'simple-confirm-grid' }, [
      el('article', { class: 'simple-confirm-card' }, [
        el('span', { class: 'section-kicker', text: TEMPLATE_COPY[draft.templateId].title }),
        el('h2', { text: `将使用“${String(draft.parameters.name ?? '')}”` }),
        el('ul', { class: 'simple-rule-summary' }, summary.map((line) => el('li', { text: line }))),
        draft.templateId === 'overtime' && Number(draft.parameters.maxSeconds) > 0
          ? el('p', { text: `最多累计 ${formatDurationZh(Number(draft.parameters.maxSeconds))}` })
          : el('p', { text: draft.templateId === 'overtime' ? '累计时间不封顶' : `共 ${summary.length} 个礼物规则` }),
      ]),
      el('article', { class: 'simple-obs-preview' }, [
        preview,
        el('h2', { text: obsUrl && (saved || options.play) ? 'OBS 实际预览' : 'OBS 画面预览' }),
        el('p', { text: '调整同一玩法会保留显示名称，因此现有 OBS 链接不会改变。' }),
      ]),
    ]));
    if (impact.kind === 'replace') {
      body.append(el('article', { class: 'simple-notice-card is-warning simple-replace-impact', role: 'alert' } as any, [
        el('strong', { text: '更换玩法会替换当前简单玩法' }),
        el('p', { text: replacementImpactCopy(impact) }),
        el('p', { text: '其他不相关的完整配置会继续运行。' }),
      ]));
    }
    if (!options.loggedIn && selectedLoginGift) {
      body.append(el('article', { class: 'simple-notice-card is-login simple-confirm-login', role: 'status' } as any, [
        el('strong', { text: '这些盲盒礼物建议登录后使用' }),
        el('p', { text: '匿名模式下无法保证识别实际开出的礼物；可以继续保存，也可以现在扫码登录。' }),
        el('button', { class: 'btn ghost', type: 'button', text: '现在登录', onclick: options.onLogin }),
      ]));
    }
    const back = el('button', { class: 'btn ghost', type: 'button', text: '返回修改' }) as HTMLButtonElement;
    back.onclick = () => showWizard(3);
    if (!saved) {
      const save = el('button', {
        class: 'btn simple-save-play',
        type: 'button',
        text: impact.kind === 'replace' ? '确认替换并保存' : options.play ? '保存调整' : '保存玩法',
      }) as HTMLButtonElement;
      save.disabled = saving;
      save.onclick = () => {
        void runAction(save, async () => {
          saving = true;
          try {
            await options.onSave(cloneDraft(draft));
            saved = true;
            copied = false;
            setMessage('玩法已保存。复制 OBS 链接后即可开播。');
          } finally {
            saving = false;
          }
          syncSession();
          options.onRefresh();
        });
      };
      body.append(el('div', { class: 'simple-step-actions' }, [back, save]));
      return;
    }
    const savedObsUrl = options.getObsUrl() ?? '';
    const obsInput = el('input', { class: 'field-input simple-obs-url', value: savedObsUrl, ariaLabel: 'OBS 浏览器来源链接' } as any) as HTMLInputElement;
    obsInput.readOnly = true;
    obsInput.onclick = () => obsInput.select();
    body.append(el('label', { class: 'field simple-obs-url-field' }, [
      el('span', { class: 'field-label', text: 'OBS 浏览器来源链接' }),
      obsInput,
      el('small', { text: '复制后，在 OBS 中添加“浏览器”来源并粘贴此链接。' }),
    ]));
    const copy = el('button', { class: 'btn simple-confirm-copy', type: 'button', text: '复制 OBS 链接' }) as HTMLButtonElement;
    copy.onclick = () => void runAction(copy, async () => {
      await options.onCopyObs();
      copied = true;
      setMessage('链接已复制。请在 OBS 中添加“浏览器”来源并粘贴。');
      options.onRefresh();
    });
    const manualCopy = el('button', { class: 'btn ghost simple-confirm-manual-copy', type: 'button', text: '我已手动复制' }) as HTMLButtonElement;
    manualCopy.onclick = () => {
      copied = true;
      setMessage('已确认复制。请在 OBS 中添加“浏览器”来源并粘贴。');
      options.onRefresh();
    };
    const done = el('button', {
      class: 'btn ghost simple-confirm-done',
      type: 'button',
      text: copied ? '完成，开始使用' : '请先复制链接',
    }) as HTMLButtonElement;
    done.disabled = !copied;
    done.onclick = options.onDone;
    body.append(el('div', { class: 'simple-step-actions simple-saved-actions' }, [copy, manualCopy, done]));
  }

  async function runAction(button: HTMLButtonElement, action: () => Promise<void>, successText?: string): Promise<void> {
    if (button.disabled) return;
    button.disabled = true;
    const original = button.textContent;
    try {
      await action();
      if (successText) button.textContent = successText;
    } catch (error) {
      setMessage(error instanceof Error ? error.message : '操作失败，请重试。', 'error');
      render();
    } finally {
      button.disabled = false;
      if (!successText) button.textContent = original;
    }
  }

  render();
  return { element };
}

function cloneDraft(draft: SimplePlayDraft): SimplePlayDraft {
  return {
    ...draft,
    parameters: { ...draft.parameters },
    gifts: Object.fromEntries(Object.entries(draft.gifts).map(([slot, giftIds]) => [slot, [...giftIds]])),
    ...(draft.overtimeGiftActions ? { overtimeGiftActions: draft.overtimeGiftActions.map((action) => ({ ...action })) } : {}),
  };
}

function validateDraft(draft: SimplePlayDraft): string | null {
  if (!String(draft.parameters.name ?? '').trim()) return '请填写显示名称。';
  const slot = TEMPLATE_COPY[draft.templateId].giftSlot;
  if ((draft.gifts[slot] ?? []).length === 0) return '请至少选择一个礼物。';
  if (draft.templateId === 'overtime') {
    const maximum = Number(draft.parameters.maxSeconds);
    if (!Number.isInteger(maximum) || maximum < 0) return '最多累计必须是大于等于 0 的整数秒数。';
    const invalidAction = (draft.overtimeGiftActions ?? []).find((action) => (
      (action.operation === 'add' || action.operation === 'subtract')
      && (!Number.isInteger(Number(action.seconds)) || Number(action.seconds) <= 0)
    ));
    if (invalidAction) return '增加或减少的时长必须是正整数秒数。';
  }
  if (draft.templateId === 'counter' && (!(Number(draft.parameters.amount) > 0))) return '每个礼物增加量必须大于 0。';
  if (draft.templateId === 'goal' && (!(Number(draft.parameters.target) > 0))) return '目标值必须大于 0。';
  return null;
}

function simpleField(
  label: string,
  value: string,
  onInput: (value: string) => void,
  disabled = false,
  help?: string,
): HTMLElement {
  const input = el('input', { class: 'field-input', value }) as HTMLInputElement;
  input.dataset.fieldLabel = label;
  input.disabled = disabled;
  input.oninput = () => onInput(input.value);
  return el('label', { class: 'field simple-parameter' }, [
    el('span', { class: 'field-label', text: label }),
    input,
    ...(help ? [el('small', { text: help })] : []),
  ]);
}

function simpleNumberField(label: string, value: number, unit: string, min: number, onInput: (value: number) => void, showDuration = false): HTMLElement {
  const input = el('input', { class: 'field-input', type: 'number', min: String(min), step: '1', value: String(value) }) as HTMLInputElement;
  input.dataset.fieldLabel = label;
  const readable = el('small', {
    class: 'simple-duration-readable',
    text: showDuration ? durationInputPreview(value, true) : '',
  });
  input.oninput = () => {
    const next = input.value.trim() === '' ? Number.NaN : Number(input.value);
    onInput(next);
    if (showDuration) readable.textContent = durationInputPreview(next, true);
  };
  return el('label', { class: 'field simple-parameter' }, [
    el('span', { class: 'field-label', text: label }),
    unit ? el('div', { class: 'simple-input-with-unit' }, [input, el('span', { text: unit })]) : input,
    readable,
  ]);
}

function durationInputPreview(value: number, allowZero: boolean): string {
  if (!Number.isInteger(value) || value < 0 || (!allowZero && value === 0)) {
    return allowZero ? '请输入非负整数秒' : '请输入正整数秒';
  }
  if (allowZero && value === 0) return '不限制';
  return formatDurationZh(value);
}

function giftChip(gift: GiftInfo): HTMLElement {
  return el('span', { class: 'simple-gift-chip' }, [
    el('img', { src: gift.imgBasic || transparentPixel(), alt: '' }),
    el('strong', { text: gift.name }),
  ]);
}

function totalExtra(extra: SimpleModeCounts): number {
  return extra.attributes + extra.rules + extra.timers + extra.activities + extra.scenes;
}

function extraDescription(extra: SimpleModeCounts): string {
  return [
    extra.attributes ? `${extra.attributes} 个属性` : '',
    extra.rules ? `${extra.rules} 条规则` : '',
    extra.timers ? `${extra.timers} 个定时器` : '',
    extra.activities ? `${extra.activities} 个活动` : '',
    extra.scenes ? `${extra.scenes} 个组合面板` : '',
  ].filter(Boolean).join('、');
}

function replacementImpactCopy(impact: SimplePlayTransitionImpact): string {
  const details = [
    impact.attributesRemoved ? `${impact.attributesRemoved} 个旧属性` : '',
    impact.rulesRemoved ? `${impact.rulesRemoved} 条关联规则` : '',
    impact.timerRulesRemoved ? `${impact.timerRulesRemoved} 个关联定时器` : '',
    impact.displayScenesUpdated + impact.displayScenesRemoved > 0
      ? `${impact.displayScenesUpdated + impact.displayScenesRemoved} 个组合面板`
      : '',
    impact.activitiesUpdated + impact.activitiesRemoved > 0
      ? `${impact.activitiesUpdated + impact.activitiesRemoved} 个活动`
      : '',
    impact.formulaPresetsRemoved ? `${impact.formulaPresetsRemoved} 个公式预设` : '',
  ].filter(Boolean);
  return details.length > 0
    ? `将清理或更新：${details.join('、')}。`
    : '将替换当前简单玩法的属性和规则。';
}

function connectionCopy(state: RuntimeConnectionState): string {
  if (state === 'connected') return '当前已连接';
  if (state === 'connecting' || state === 'reconnecting') return '正在连接…';
  if (state === 'error') return '连接失败，请检查房间号或网络后重试';
  return '填写后会连接直播间';
}

function wizardLead(step: number): string {
  if (step === 1) return '先连接直播间；登录很有帮助，但不是必需。';
  if (step === 2) return '只保留最常用的三种玩法，选一个即可。';
  if (step === 3) return '填写核心参数，然后选择会触发玩法的礼物。';
  return '检查礼物规则和 OBS 画面，保存后复制链接。';
}

function transparentPixel(): string {
  return 'data:image/gif;base64,R0lGODlhAQABAIAAAAAAAP///yH5BAEAAAAALAAAAAABAAEAAAIBRAA7';
}
