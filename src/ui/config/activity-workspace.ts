import { activityStatusLabel, createActivityId, createActivityMilestoneId, MAX_ACTIVITY_ATTRIBUTES, type ActivityTransitionAction } from '../../activities';
import { transitionActivity } from '../../backend';
import { formatValue } from '../../format';
import type { ActivityGiftTimeoutAction, ActivityMilestone, ActivityResultMode, ActivitySession, AppState } from '../../types';
import { bindFloatingDetailCard, el, fieldControl, inputField, toast } from '../common';

interface ActivityWorkspaceOptions {
  state: AppState;
  root: HTMLElement;
  onPersist: () => Promise<void>;
  onRender: () => void;
  onEditorOpenChange: (open: boolean) => void;
}

export function createActivityWorkspace(options: ActivityWorkspaceOptions): HTMLElement {
  const { state, root } = options;
  const section = el('section', { class: 'activity-workspace-section' });
  const addButton = el('button', { class: 'btn', type: 'button', text: '+ 新建活动' }) as HTMLButtonElement;
  addButton.disabled = state.attributes.length === 0;
  addButton.title = addButton.disabled ? '请先创建至少 1 个属性' : '';
  addButton.onclick = () => openEditor();
  section.append(el('div', { class: 'activity-workspace-heading' }, [
    el('div', { class: 'section-heading' }, [
      el('span', { class: 'section-kicker', text: '场景控制' }),
      el('h2', { text: '活动会话' }),
      el('p', { text: '管理一局互动的开始、锁定和结算；需要时可让关联属性只在活动进行中响应。' }),
    ]),
    addButton,
  ]));

  if (state.attributes.length === 0) {
    section.append(el('div', { class: 'activity-empty', text: '创建属性后，才能建立活动会话。' }));
    return section;
  }
  if (state.activities.length === 0) {
    section.append(el('div', { class: 'activity-empty', text: '还没有活动。两队对战、礼物票选和限时挑战都会使用这里的会话状态。' }));
    return section;
  }

  const list = el('div', { class: 'activity-card-list' });
  state.activities.forEach((activity, index) => list.append(renderCard(activity, index)));
  section.append(list);
  return section;

  function renderCard(activity: ActivitySession, index: number): HTMLElement {
    const detailPersisted = root.dataset.expandedActivityId === activity.id;
    const detailAbove = detailPersisted && root.dataset.expandedActivitySide === 'above';
    const card = el('article', {
      class: `activity-card hover-detail-card is-${activity.status}${detailPersisted ? ' is-detail-persisted' : ''}${detailAbove ? ' is-detail-above' : ''}`,
      tabIndex: 0,
      ariaLabel: `活动“${activity.name}”，${activityStatusLabel(activity.status)}。悬停或聚焦查看详细设置。`,
    } as any);
    const status = el('span', { class: `activity-status is-${activity.status}`, text: activityStatusLabel(activity.status) });
    const scene = state.displayScenes.find((candidate) => candidate.id === activity.sceneId);
    const actions = el('div', { class: 'activity-card-actions' });
    const transitionButton = (label: string, action: ActivityTransitionAction, className = 'btn'): HTMLButtonElement => {
      const button = el('button', { class: className, type: 'button', text: label }) as HTMLButtonElement;
      button.onclick = () => void runTransition(activity, action, actions, card);
      return button;
    };
    const remove = el('button', { class: 'btn text-danger', type: 'button', text: '删除' }) as HTMLButtonElement;
    remove.onclick = () => {
      const activeWarning = activity.status === 'active' || activity.status === 'locked'
        ? '活动仍在进行，删除后会立即停止本局控制。'
        : '';
      if (!confirm(`${activeWarning}确定删除活动“${activity.name}”？属性、规则和组合面板不会被删除。`)) return;
      if (root.dataset.expandedActivityId === activity.id) {
        delete root.dataset.expandedActivityId;
        delete root.dataset.expandedActivitySide;
      }
      state.activities.splice(index, 1);
      void options.onPersist().then(options.onRender).catch(() => undefined);
    };
    if (activity.status === 'not_started') {
      actions.append(transitionButton('开始活动', 'start'));
      const edit = el('button', { class: 'btn ghost', type: 'button', text: '编辑' }) as HTMLButtonElement;
      edit.onclick = () => openEditor(index);
      actions.append(edit);
    } else if (activity.status === 'active') {
      actions.append(transitionButton('锁定结果', 'lock', 'btn ghost'), transitionButton('直接结算', 'settle'));
    } else if (activity.status === 'locked') {
      actions.append(transitionButton('确认结算', 'settle'));
    } else {
      actions.append(transitionButton('重新准备', 'reset', 'btn ghost'));
    }
    actions.append(remove);

    const result = activity.status === 'settled' && activity.result
      ? renderResult(activity)
      : null;
    const latestMilestone = activity.milestones
      .filter((milestone) => milestone.triggeredAt)
      .sort((left, right) => Number(right.triggeredAt) - Number(left.triggeredAt))[0];
    const statusIcon = activity.status === 'active' ? '▶'
      : activity.status === 'locked' ? '◆'
        : activity.status === 'settled' ? '✓' : '◇';
    const cover = el('div', { class: 'activity-card-cover hover-detail-cover', title: '悬停查看活动详情' }, [
      el('div', { class: `activity-card-visual is-${activity.status}`, text: statusIcon, ariaHidden: 'true' } as any),
      el('div', { class: 'activity-card-cover-copy' }, [
        el('h3', { text: activity.name }),
        el('div', { class: 'activity-card-compact-row' }, [
          el('span', { class: 'activity-card-compact-meta', text: `${activity.attributeNames.length} 个属性` }),
          status,
        ]),
      ]),
    ]);
    const detailsContent = el('div', { class: 'activity-card-details-content hover-detail-panel-content' }, [
      el('div', { class: 'activity-card-head activity-card-detail-head' }, [
        el('p', { text: activity.gateRules ? '关联属性只在活动进行中响应规则与定时器' : '仅记录活动状态，不限制规则执行' }),
        actions,
      ]),
      el('div', { class: 'activity-attribute-chips' }, activity.attributeNames.map((attributeName) => {
        const attribute = state.attributes.find((candidate) => candidate.name === attributeName);
        return el('span', {}, [
          el('strong', { text: attributeName }),
          el('small', { text: attribute ? formatValue(attribute.value, attribute) : '—' }),
        ]);
      })),
      el('div', { class: 'activity-card-meta' }, [
        el('span', { text: scene ? `OBS：${scene.name}` : '未关联组合面板' }),
        el('span', { text: resultModeLabel(activity.resultMode) }),
        ...(activity.milestones.length > 0
          ? [el('span', { text: `里程碑 ${activity.milestones.filter((milestone) => milestone.triggeredAt).length}/${activity.milestones.length}` })]
          : []),
        ...(activity.giftTimeout
          ? [el('span', { text: `送礼后 ${activity.giftTimeout.seconds} 秒${timeoutActionLabel(activity.giftTimeout.action)}` })]
          : []),
        ...(activity.status === 'not_started' ? [el('span', { text: '开始时恢复初始值' })] : []),
      ]),
      ...(latestMilestone ? [el('div', { class: 'activity-milestone-event' }, [
        el('span', { text: '◆' }),
        el('div', {}, [
          el('strong', { text: latestMilestone.message || latestMilestone.name }),
          el('small', { text: `${latestMilestone.attributeName} ${latestMilestone.comparison === 'gte' ? '≥' : '≤'} ${latestMilestone.threshold}` }),
        ]),
      ])] : []),
      ...(result ? [result] : []),
    ]);
    const details = el('div', { class: 'activity-card-details hover-detail-panel' }, [
      el('div', { class: 'activity-card-details-inner hover-detail-panel-inner' }, [detailsContent]),
    ]);
    card.append(cover, details);
    bindFloatingDetailCard(card, cover, {
      panelWidth: 560,
      estimatedPanelHeight: 440,
      onPointerLeave: () => {
        if (root.dataset.expandedActivityId === activity.id) {
          delete root.dataset.expandedActivityId;
          delete root.dataset.expandedActivitySide;
        }
        card.classList.remove('is-detail-persisted');
      },
    });
    return card;
  }

  function renderResult(activity: ActivitySession): HTMLElement {
    const winner = activity.result?.winnerAttributeName;
    const heading = activity.resultMode === 'none'
      ? '结算快照'
      : winner
        ? `胜出：${winner}`
        : '结果：平局';
    return el('div', { class: 'activity-result' }, [
      el('strong', { text: heading }),
      el('div', {}, activity.attributeNames.flatMap((attributeName) => {
        const value = activity.result?.values?.[attributeName];
        if (!Number.isFinite(value)) return [];
        const attribute = state.attributes.find((candidate) => candidate.name === attributeName);
        return [el('span', {}, [
          el('small', { text: attributeName }),
          el('b', { text: attribute ? formatValue(Number(value), attribute) : String(value) }),
        ])];
      })),
    ]);
  }

  async function runTransition(
    activity: ActivitySession,
    action: ActivityTransitionAction,
    actionRoot: HTMLElement,
    card: HTMLElement,
  ): Promise<void> {
    if (action === 'reset' && !confirm('重新准备会把关联属性恢复为初始值，继续吗？')) return;
    const buttons = Array.from(actionRoot.querySelectorAll('button')) as HTMLButtonElement[];
    buttons.forEach((button) => { button.disabled = true; });
    if (card.classList.contains('is-pointer-focus')) {
      root.dataset.expandedActivityId = activity.id;
      root.dataset.expandedActivitySide = card.classList.contains('is-detail-above') ? 'above' : 'below';
    }
    try {
      const result = await transitionActivity(activity.id, action);
      const index = state.activities.findIndex((candidate) => candidate.id === activity.id);
      if (index >= 0) state.activities[index] = result.activity;
      for (const [attributeName, value] of Object.entries(result.attributeValues)) {
        const attribute = state.attributes.find((candidate) => candidate.name === attributeName);
        if (attribute) attribute.value = value;
      }
      options.onRender();
      toast(transitionSuccessMessage(action), root);
    } catch (error) {
      if (root.dataset.expandedActivityId === activity.id) {
        delete root.dataset.expandedActivityId;
        delete root.dataset.expandedActivitySide;
      }
      buttons.forEach((button) => { button.disabled = false; });
      toast(error instanceof Error ? error.message : '活动操作失败', root);
    }
  }

  function openEditor(index?: number): void {
    root.querySelector('.activity-editor-overlay')?.remove();
    options.onEditorOpenChange(true);
    const original = index === undefined ? undefined : state.activities[index];
    let selectedNames = original ? [...original.attributeNames] : state.attributes.slice(0, Math.min(2, state.attributes.length)).map((attribute) => attribute.name);
    let milestones: ActivityMilestone[] = original?.milestones.map((milestone) => ({ ...milestone })) ?? [];
    const initialValues = new Map<string, number>(selectedNames.map((attributeName) => [
      attributeName,
      original?.initialValues[attributeName] ?? state.attributes.find((attribute) => attribute.name === attributeName)?.value ?? 0,
    ]));
    const overlay = el('div', { class: 'overlay activity-editor-overlay' });
    const dialog = el('section', { class: 'card activity-editor-dialog', role: 'dialog', ariaLabel: original ? `编辑活动 ${original.name}` : '新建活动' } as any);
    const close = (): void => {
      overlay.remove();
      options.onEditorOpenChange(false);
    };
    const closeButton = el('button', { class: 'modal-close', type: 'button', text: '×', ariaLabel: '关闭活动编辑器' } as any) as HTMLButtonElement;
    closeButton.onclick = close;
    overlay.onpointerdown = (event) => { overlay.dataset.pointerOutside = String(event.target === overlay); };
    overlay.onclick = (event) => {
      const shouldClose = overlay.dataset.pointerOutside === 'true' && event.target === overlay;
      overlay.dataset.pointerOutside = 'false';
      if (shouldClose) close();
    };

    const nameInput = inputField('活动名称', original?.name ?? `活动 ${state.activities.length + 1}`);
    nameInput.maxLength = 40;
    const resultSelect = el('select', { class: 'field-input' }) as HTMLSelectElement;
    resultSelect.dataset.fieldLabel = '结算方式';
    ([
      ['none', '只保存结算快照，不判定胜者'],
      ['highest', '数值最高的属性胜出'],
      ['lowest', '数值最低的属性胜出'],
    ] as const).forEach(([value, label]) => resultSelect.append(el('option', { value, text: label })));
    resultSelect.value = original?.resultMode ?? (selectedNames.length > 1 ? 'highest' : 'none');
    const sceneSelect = el('select', { class: 'field-input' }) as HTMLSelectElement;
    sceneSelect.dataset.fieldLabel = '关联 OBS 组合面板（可选）';
    sceneSelect.append(el('option', { value: '', text: '不关联组合面板' }));
    const usedSceneIds = new Set(state.activities.filter((_, activityIndex) => activityIndex !== index).map((activity) => activity.sceneId).filter(Boolean));
    state.displayScenes.forEach((scene) => {
      const option = el('option', { value: scene.id, text: scene.name }) as HTMLOptionElement;
      option.disabled = usedSceneIds.has(scene.id);
      sceneSelect.append(option);
    });
    sceneSelect.value = original?.sceneId ?? '';

    const gateInput = el('input', { class: 'setting-switch-input', type: 'checkbox' }) as HTMLInputElement;
    gateInput.checked = original?.gateRules ?? true;
    const gateControl = el('label', { class: 'setting-switch activity-gate-switch' }, [
      gateInput,
      el('span', { class: 'setting-switch-track', ariaHidden: 'true' } as any),
      el('span', { class: 'setting-switch-copy' }, [
        el('strong', { text: '只在活动进行中响应' }),
        el('small', { text: '未开始、锁定或结算后，关联属性的礼物规则和定时器暂停执行。' }),
      ]),
    ]);

    const timeoutEnabled = el('input', { class: 'setting-switch-input', type: 'checkbox' }) as HTMLInputElement;
    timeoutEnabled.checked = Boolean(original?.giftTimeout);
    const timeoutSeconds = inputField('无新礼物等待时间（秒）', String(original?.giftTimeout?.seconds ?? 30));
    timeoutSeconds.type = 'number';
    timeoutSeconds.min = '1';
    timeoutSeconds.max = '86400';
    timeoutSeconds.step = '1';
    const timeoutAction = el('select', { class: 'field-input' }) as HTMLSelectElement;
    timeoutAction.dataset.fieldLabel = '倒计时结束后';
    timeoutAction.append(
      el('option', { value: 'lock', text: '锁定活动，等待手动结算' }),
      el('option', { value: 'settle', text: '自动结算' }),
      el('option', { value: 'reset', text: '恢复初始值并结束活动' }),
    );
    timeoutAction.value = original?.giftTimeout?.action ?? 'lock';
    const timeoutFields = el('div', { class: 'activity-timeout-fields' }, [fieldControl(timeoutSeconds), fieldControl(timeoutAction)]);
    const refreshTimeout = (): void => {
      timeoutFields.classList.toggle('is-disabled', !timeoutEnabled.checked);
      timeoutSeconds.disabled = !timeoutEnabled.checked;
      timeoutAction.disabled = !timeoutEnabled.checked;
    };
    timeoutEnabled.onchange = refreshTimeout;
    const timeoutSection = el('section', { class: 'activity-editor-section activity-timeout-section' }, [
      el('label', { class: 'setting-switch' }, [
        timeoutEnabled,
        el('span', { class: 'setting-switch-track', ariaHidden: 'true' } as any),
        el('span', { class: 'setting-switch-copy' }, [
          el('strong', { text: '送礼后倒计时' }),
          el('small', { text: '第一次生效礼物后开始计时；后续每次生效礼物都会重新计时。' }),
        ]),
      ]),
      timeoutFields,
    ]);
    refreshTimeout();

    const selectionCount = el('strong', { class: 'activity-selection-count' });
    const picker = el('div', { class: 'activity-attribute-picker' });
    const buttons = new Map<string, HTMLButtonElement>();
    const valueFields = el('div', { class: 'activity-initial-values' });
    let renderMilestones = (): void => undefined;
    const refresh = (): void => {
      selectionCount.textContent = `已选择 ${selectedNames.length} / ${MAX_ACTIVITY_ATTRIBUTES}`;
      for (const [attributeName, button] of buttons) {
        const selected = selectedNames.includes(attributeName);
        button.classList.toggle('is-selected', selected);
        button.setAttribute('aria-pressed', String(selected));
      }
      valueFields.replaceChildren();
      selectedNames.forEach((attributeName) => {
        const input = inputField(`${attributeName} 的初始值`, String(initialValues.get(attributeName) ?? 0));
        input.type = 'number';
        input.step = 'any';
        input.oninput = () => initialValues.set(attributeName, Number(input.value));
        valueFields.append(fieldControl(input));
      });
    };
    state.attributes.forEach((attribute) => {
      const button = el('button', { class: 'activity-attribute-option', type: 'button', ariaPressed: 'false' } as any) as HTMLButtonElement;
      button.append(el('strong', { text: attribute.name }), el('small', { text: formatValue(attribute.value, attribute) }));
      button.onclick = () => {
        if (selectedNames.includes(attribute.name)) {
          selectedNames = selectedNames.filter((name) => name !== attribute.name);
          const previousLength = milestones.length;
          milestones = milestones.filter((milestone) => milestone.attributeName !== attribute.name);
          if (milestones.length !== previousLength) toast(`已移除“${attribute.name}”关联的里程碑`, root);
        }
        else if (selectedNames.length < MAX_ACTIVITY_ATTRIBUTES) {
          selectedNames.push(attribute.name);
          initialValues.set(attribute.name, attribute.value);
        } else toast(`一个活动最多关联 ${MAX_ACTIVITY_ATTRIBUTES} 个属性`, root);
        refresh();
        renderMilestones();
      };
      buttons.set(attribute.name, button);
      picker.append(button);
    });
    refresh();

    const milestoneList = el('div', { class: 'activity-milestone-list' });
    const addMilestoneButton = el('button', { class: 'btn ghost', type: 'button', text: '+ 添加里程碑' }) as HTMLButtonElement;
    renderMilestones = (): void => {
      milestoneList.replaceChildren();
      if (milestones.length === 0) {
        milestoneList.append(el('div', { class: 'activity-milestone-empty', text: '没有里程碑。活动仍可手动锁定和结算。' }));
        return;
      }
      milestones.forEach((milestone, milestoneIndex) => {
        const name = inputField('里程碑名称', milestone.name);
        name.maxLength = 40;
        name.oninput = () => { milestone.name = name.value; };
        const attribute = el('select', { class: 'field-input' }) as HTMLSelectElement;
        attribute.dataset.fieldLabel = '监控属性';
        selectedNames.forEach((attributeName) => attribute.append(el('option', { value: attributeName, text: attributeName })));
        attribute.value = milestone.attributeName;
        attribute.onchange = () => { milestone.attributeName = attribute.value; };
        const comparison = el('select', { class: 'field-input' }) as HTMLSelectElement;
        comparison.dataset.fieldLabel = '触发条件';
        comparison.append(
          el('option', { value: 'gte', text: '达到或超过（≥）' }),
          el('option', { value: 'lte', text: '降到或低于（≤）' }),
        );
        comparison.value = milestone.comparison;
        comparison.onchange = () => { milestone.comparison = comparison.value as ActivityMilestone['comparison']; };
        const threshold = inputField('目标值', String(milestone.threshold));
        threshold.type = 'number';
        threshold.step = 'any';
        threshold.oninput = () => { milestone.threshold = Number(threshold.value); };
        const action = el('select', { class: 'field-input' }) as HTMLSelectElement;
        action.dataset.fieldLabel = '达到后';
        action.append(
          el('option', { value: 'announce', text: '只显示达成提示' }),
          el('option', { value: 'lock', text: '提示并锁定活动' }),
          el('option', { value: 'settle', text: '提示并自动结算' }),
        );
        action.value = milestone.action;
        action.onchange = () => { milestone.action = action.value as ActivityMilestone['action']; };
        const message = inputField('达成提示（可选）', milestone.message);
        message.maxLength = 120;
        message.placeholder = '例如：目标达成！';
        message.oninput = () => { milestone.message = message.value; };
        const remove = el('button', { class: 'btn text-danger', type: 'button', text: '删除' }) as HTMLButtonElement;
        remove.onclick = () => {
          milestones.splice(milestoneIndex, 1);
          renderMilestones();
        };
        milestoneList.append(el('article', { class: 'activity-milestone-editor' }, [
          el('header', {}, [
            el('div', {}, [el('strong', { text: `里程碑 ${milestoneIndex + 1}` }), el('small', { text: '每次活动只触发一次，重置后可再次触发' })]),
            remove,
          ]),
          el('div', { class: 'activity-milestone-fields' }, [
            fieldControl(name), fieldControl(attribute), fieldControl(comparison), fieldControl(threshold), fieldControl(action), fieldControl(message),
          ]),
        ]));
      });
    };
    addMilestoneButton.onclick = () => {
      const attributeName = selectedNames[0];
      if (!attributeName) {
        toast('请先选择至少 1 个关联属性', root);
        return;
      }
      const base = Number(initialValues.get(attributeName) ?? 0);
      milestones.push({
        id: createActivityMilestoneId(),
        name: `${attributeName}达到目标`,
        attributeName,
        comparison: 'gte',
        threshold: base + 10,
        action: 'announce',
        message: '目标达成！',
      });
      renderMilestones();
      milestoneList.lastElementChild?.scrollIntoView({ block: 'nearest' });
    };
    const milestoneSection = el('section', { class: 'activity-editor-section' }, [
      el('div', { class: 'activity-editor-section-head' }, [
        el('div', {}, [
          el('h3', { text: '一次性里程碑' }),
          el('p', { text: '监控属性达到目标后显示提示，也可以自动锁定或结算。' }),
        ]),
        addMilestoneButton,
      ]),
      milestoneList,
    ]);
    renderMilestones();

    const cancelButton = el('button', { class: 'btn ghost', type: 'button', text: '取消' }) as HTMLButtonElement;
    cancelButton.onclick = close;
    const saveButton = el('button', { class: 'btn', type: 'button', text: original ? '保存修改' : '创建活动' }) as HTMLButtonElement;
    saveButton.onclick = async () => {
      const name = nameInput.value.trim();
      if (!name) return focusWithToast(nameInput, '请填写活动名称');
      if (state.activities.some((activity, activityIndex) => activityIndex !== index && activity.name.toLowerCase() === name.toLowerCase())) {
        return focusWithToast(nameInput, '活动名称不能重复');
      }
      if (selectedNames.length === 0) {
        toast('请至少选择 1 个属性', root);
        return;
      }
      const invalidInitialValue = selectedNames.find((attributeName) => !Number.isFinite(initialValues.get(attributeName)));
      if (invalidInitialValue) {
        toast(`“${invalidInitialValue}”的初始值无效`, root);
        return;
      }
      const invalidMilestone = milestones.find((milestone) => !milestone.name.trim()
        || !selectedNames.includes(milestone.attributeName)
        || !Number.isFinite(milestone.threshold));
      if (invalidMilestone) {
        toast('请补全里程碑名称、属性和有效目标值', root);
        return;
      }
      const parsedTimeoutSeconds = Math.floor(Number(timeoutSeconds.value));
      if (timeoutEnabled.checked && (!Number.isFinite(parsedTimeoutSeconds) || parsedTimeoutSeconds < 1 || parsedTimeoutSeconds > 86_400)) {
        return focusWithToast(timeoutSeconds, '送礼后倒计时必须在 1 秒到 24 小时之间');
      }
      if (gateInput.checked) {
        const conflict = state.activities.find((activity, activityIndex) => activityIndex !== index
          && activity.gateRules
          && activity.attributeNames.some((attributeName) => selectedNames.includes(attributeName)));
        if (conflict) {
          toast(`这些属性已由活动“${conflict.name}”控制`, root);
          return;
        }
      }
      const next: ActivitySession = {
        id: original?.id ?? createActivityId(),
        name,
        attributeNames: [...selectedNames],
        ...(sceneSelect.value ? { sceneId: sceneSelect.value } : {}),
        status: 'not_started',
        resultMode: resultSelect.value as ActivityResultMode,
        gateRules: gateInput.checked,
        initialValues: Object.fromEntries(selectedNames.map((attributeName) => [attributeName, Number(initialValues.get(attributeName))])),
        milestones: milestones.map((milestone) => ({
          id: milestone.id,
          name: milestone.name.trim(),
          attributeName: milestone.attributeName,
          comparison: milestone.comparison,
          threshold: milestone.threshold,
          action: milestone.action,
          message: milestone.message.trim(),
        })),
        ...(timeoutEnabled.checked ? {
          giftTimeout: {
            seconds: parsedTimeoutSeconds,
            action: timeoutAction.value as ActivityGiftTimeoutAction,
          },
        } : {}),
      };
      const previous = state.activities;
      state.activities = [...state.activities];
      if (index === undefined) state.activities.push(next);
      else state.activities[index] = next;
      saveButton.disabled = true;
      saveButton.textContent = '保存中…';
      try {
        await options.onPersist();
      } catch {
        state.activities = previous;
        saveButton.disabled = false;
        saveButton.textContent = original ? '保存修改' : '创建活动';
        return;
      }
      close();
      options.onRender();
      toast(index === undefined ? '活动已创建' : '活动已保存', root);
    };

    dialog.append(
      el('header', { class: 'activity-editor-header' }, [
        el('div', {}, [
          el('span', { class: 'section-kicker', text: '活动会话' }),
          el('h2', { text: original ? `编辑“${original.name}”` : '创建一局互动' }),
          el('p', { text: '活动状态和结算都由后台保存，关闭配置页面也不会中断。' }),
        ]),
        closeButton,
      ]),
      el('div', { class: 'activity-editor-body' }, [
        el('div', { class: 'activity-editor-fields' }, [fieldControl(nameInput), fieldControl(resultSelect), fieldControl(sceneSelect)]),
        el('section', { class: 'activity-editor-section' }, [
          el('div', { class: 'activity-editor-section-head' }, [
            el('div', {}, [el('h3', { text: '关联属性' }), el('p', { text: '选择参与本局活动的属性，并设置每次开始时恢复到的数值。' })]),
            selectionCount,
          ]),
          picker,
          valueFields,
        ]),
        gateControl,
        timeoutSection,
        milestoneSection,
      ]),
      el('footer', { class: 'modal-actions activity-editor-actions' }, [cancelButton, saveButton]),
    );
    overlay.append(dialog);
    root.append(overlay);
  }

  function focusWithToast(input: HTMLInputElement, message: string): void {
    toast(message, root);
    input.focus();
  }
}

function resultModeLabel(mode: ActivityResultMode): string {
  if (mode === 'highest') return '结算：最高值胜出';
  if (mode === 'lowest') return '结算：最低值胜出';
  return '结算：只保存快照';
}

function transitionSuccessMessage(action: ActivityTransitionAction): string {
  if (action === 'start') return '活动已开始';
  if (action === 'lock') return '结果已锁定，规则已暂停';
  if (action === 'settle') return '活动已结算';
  return '活动已恢复为未开始';
}

function timeoutActionLabel(action: ActivityGiftTimeoutAction): string {
  if (action === 'settle') return '自动结算';
  if (action === 'reset') return '自动重置';
  return '自动锁定';
}
