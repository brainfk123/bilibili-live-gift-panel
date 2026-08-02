import { AppState, Attribute, GiftInfo, GiftRule, TimerRule } from '../../types';
import { consumeConfigMigrationRequired, loadState, refreshStateFromServer, resetState, saveState } from '../../storage';
import { el, fieldControl, inputField, toast } from '../common';
import { builtinCatalog, findGift, giftDisplayKey } from '../../gifts/catalog';
import { formatValue } from '../../format';
import { getRuntimeStatus, previewFormula, RuntimeConnectionState } from '../../backend';
import { createBrandIcon } from '../brand';
import { getTutorialStep } from './wizard';
import { renderSpotlightGuide, type SpotlightGuideElement } from './spotlight-guide';

interface SelectedGiftRule {
  gift: GiftInfo;
  formulaName: string;
  formula: string;
  previous?: GiftRule;
}

export function mountConfig(root: HTMLElement): void {
  let state = loadState();
  root.classList.add('config-root');
  state.settings.theme = normalizeConfigTheme(state.settings.theme);
  state.settings.giftView = state.settings.giftView === 'grid' ? 'grid' : 'list';
  const metadataChanged = ensureRuleGiftCatalog(state);
  if (metadataChanged || consumeConfigMigrationRequired()) void saveState(state);

  let connectionState: RuntimeConnectionState = 'idle';
  let guideDismissed = !state.settings.showTutorial;
  let activeGuide: SpotlightGuideElement | null = null;
  let editorOpen = false;
  let editorGuideEnabled = false;
  let runtimeRefreshActive = false;
  let stateRefreshActive = false;

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

  async function refreshRuntime(): Promise<void> {
    if (runtimeRefreshActive) return;
    runtimeRefreshActive = true;
    try {
      const runtime = await getRuntimeStatus();
      const previous = connectionState;
      connectionState = runtime.state;
      renderHeaderStatus();
      const inlineStatus = root.querySelector('.connection-inline-status');
      if (inlineStatus) inlineStatus.textContent = connectionLabel(connectionState);
      if (!editorOpen && previous !== connectionState && connectionState === 'connected') render();
    } catch {
      connectionState = 'error';
      renderHeaderStatus();
      const inlineStatus = root.querySelector('.connection-inline-status');
      if (inlineStatus) inlineStatus.textContent = connectionLabel(connectionState);
    } finally {
      runtimeRefreshActive = false;
    }
  }

  async function refreshBackendState(): Promise<void> {
    if (stateRefreshActive || editorOpen) return;
    stateRefreshActive = true;
    try {
      const previousStructure = configStructureSignature(state);
      state = await refreshStateFromServer();
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
    connectButton.onclick = () => {
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
      void saveAndWait().then(refreshRuntime).catch(() => undefined);
    };
    roomCard.append(
      fieldControl(roomInput),
      el('div', { class: 'row connection-actions' }, [connectButton, connectionText]),
      el('details', { class: 'inline-help' }, [
        el('summary', { text: '房间号在哪里？' }),
        el('p', { text: '直播地址 live.bilibili.com/88888888 中的 88888888 就是房间号，不要复制问号后的参数。' }),
      ]),
    );

    grid.append(roomCard);
    content.append(grid);
  }

  function renderAttributesWorkspace(): void {
    const section = el('section', { class: 'attributes-section' });
    const headingRow = el('div', { class: 'attributes-heading-row' });
    headingRow.append(
      sectionHeading('互动逻辑', '属性与礼物公式', '一个属性可以被多个礼物影响；连送 N 个会按单个礼物连续执行 N 次公式。'),
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
        el('span', { class: 'attribute-meta', text: `${displayFormatLabel(attribute)} · ${rules.length} 条礼物公式 · ${timerRules.length} 个定时器` }),
      ]),
      el('div', { class: 'attribute-actions' }, [editButton, deleteButton]),
    );

    const formulas = el('div', { class: 'attribute-formulas' });
    if (rules.length === 0 && timerRules.length === 0) {
      formulas.append(el('div', { class: 'formula-empty', text: '尚未配置触发规则，点击“编辑”即可添加。' }));
    } else {
      for (const rule of rules) {
        const gift = findGift(state, rule.giftId);
        const giftImage = el('img', { class: 'attribute-gift-image', alt: '' }) as HTMLImageElement;
        giftImage.src = gift?.imgBasic || transparentPixel();
        formulas.append(el('div', { class: 'attribute-gift-rule' }, [
          giftImage,
          el('div', { class: 'attribute-gift-copy' }, [
            el('strong', { text: gift?.name ?? `礼物 ${rule.giftId}` }),
            el('span', { text: rule.formulaName?.trim() || '未命名公式' }),
          ]),
        ]));
      }
      for (const rule of timerRules) {
        formulas.append(el('div', { class: 'attribute-gift-rule attribute-timer-rule' }, [
          el('span', { class: 'attribute-timer-icon', text: '⏱' }),
          el('div', { class: 'attribute-gift-copy' }, [
            el('strong', { text: rule.formulaName || '未命名定时器' }),
            el('span', { text: `每 ${formatInterval(rule.intervalSeconds)}${rule.enabled ? '' : ' · 已停用'}` }),
          ]),
        ]));
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
    let allGifts = availableGifts(state);
    const selected = new Map<number, SelectedGiftRule>();
    if (original) {
      for (const rule of state.rules.filter((item) => item.attributeName === original.name)) {
        const gift = findGift(state, rule.giftId);
        if (gift && !selected.has(gift.id)) {
          selected.set(gift.id, {
            gift,
            formulaName: rule.formulaName?.trim() || `${gift.name}规则`,
            formula: rule.formula,
            previous: rule,
          });
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
      renderGuide();
    };
    closeButton.onclick = close;
    modal.append(el('header', { class: 'modal-header' }, [
      el('div', {}, [
        el('span', { class: 'section-kicker', text: original ? '编辑互动属性' : '新建互动属性' }),
        el('h2', { text: original ? `配置“${original.name}”` : '添加属性并绑定礼物' }),
        el('p', { text: '属性基础信息、礼物选择和每个礼物的公式都在这里完成。' }),
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
    ]);

    const timerList = el('div', { class: 'timer-rule-list' });
    const timerCount = el('span', { class: 'selection-count' });

    function renderTimerRules(): void {
      timerCount.textContent = timerRules.length === 0 ? '未启用' : `${timerRules.length} 个定时器`;
      timerList.replaceChildren();
      if (timerRules.length === 0) {
        timerList.append(el('div', {
          class: 'timer-rule-empty',
          text: '没有定时器。添加后，即使配置页和 OBS 都关闭，托盘后台仍会按间隔执行公式。',
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
      const enabledInput = el('input', { type: 'checkbox' }) as HTMLInputElement;
      enabledInput.checked = rule.enabled;
      enabledInput.onchange = () => {
        rule.enabled = enabledInput.checked;
        editor.classList.toggle('is-disabled', !rule.enabled);
      };
      editor.classList.toggle('is-disabled', !rule.enabled);
      editor.append(el('div', { class: 'timer-rule-header' }, [
        el('div', { class: 'timer-rule-title' }, [
          el('span', { class: 'timer-rule-clock', text: '⏱' }),
          el('div', {}, [
            el('strong', { text: '后台定时触发' }),
            el('small', { text: '从保存或服务启动后开始计算第一个完整间隔' }),
          ]),
        ]),
        el('div', { class: 'timer-rule-actions' }, [
          el('label', { class: 'timer-enabled-toggle' }, [enabledInput, el('span', { text: '启用' })]),
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
      const formulaControl = el('label', { class: 'field formula-assignment-field' });
      const assignmentRow = el('div', { class: 'formula-assignment-row' });
      assignmentRow.append(
        el('code', { class: 'formula-target-name', text: `${nameInput.value.trim() || '属性'} =` }),
        formulaInput,
      );
      formulaControl.append(
        el('span', { class: 'field-label', text: '触发后属性值' }),
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
          el('p', { text: '按固定间隔独立执行公式，不依赖直播连接、配置页或 OBS 页面。' }),
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

    const defaultFormula = (): string => `${nameInput.value.trim() || '属性'}+price/1000*60`;

    function renderGiftPicker(): void {
      const query = giftSearch.value.trim().toLowerCase();
      const matches = allGifts
        .filter((gift) => gift.name.toLowerCase().includes(query) || String(gift.id).includes(query))
        .slice(0, 80);
      giftPicker.replaceChildren();
      if (matches.length === 0) giftPicker.append(el('div', { class: 'picker-empty', text: '没有匹配的礼物，可在下方手动添加。' }));
      for (const gift of matches) {
        const selectedNow = selected.has(gift.id);
        const button = el('button', { class: `gift-choice${selectedNow ? ' is-selected' : ''}`, type: 'button' }) as HTMLButtonElement;
        const image = el('img', { class: 'gift-choice-image', alt: '' }) as HTMLImageElement;
        image.src = gift.imgBasic || transparentPixel();
        button.append(
          image,
          el('span', { class: 'gift-choice-copy' }, [
            el('strong', { text: gift.name }),
            el('small', { text: giftPriceLabel(gift) }),
          ]),
          el('span', { class: 'gift-choice-check', text: selectedNow ? '✓' : '+' }),
        );
        button.onclick = () => {
          if (selected.has(gift.id)) selected.delete(gift.id);
          else selected.set(gift.id, { gift, formulaName: `${gift.name}规则`, formula: defaultFormula() });
          renderGiftPicker();
          renderSelectedRules();
        };
        giftPicker.append(button);
      }
    }

    function renderSelectedRules(): void {
      selectionCount.textContent = `已选择 ${selected.size} 个礼物`;
      selectedRules.replaceChildren();
      if (selected.size === 0) {
        selectedRules.append(el('div', { class: 'selected-rules-empty', text: '还没有选择礼物。属性可以先单独保存，之后再回来绑定。' }));
        return;
      }
      for (const item of selected.values()) selectedRules.append(renderFormulaEditor(item));
    }

    function renderFormulaEditor(item: SelectedGiftRule): HTMLElement {
      const row = el('article', { class: 'selected-gift-rule' });
      const removeButton = el('button', { class: 'rule-remove', type: 'button', text: '移除' }) as HTMLButtonElement;
      removeButton.onclick = () => {
        selected.delete(item.gift.id);
        renderGiftPicker();
        renderSelectedRules();
      };
      const giftImage = el('img', { class: 'selected-rule-gift-image', alt: '' }) as HTMLImageElement;
      giftImage.src = item.gift.imgBasic || transparentPixel();
      row.append(el('div', { class: 'selected-rule-header' }, [
        el('div', { class: 'selected-rule-gift' }, [
          giftImage,
          el('div', {}, [
            el('strong', { text: item.gift.name }),
            el('small', { text: `每收到 1 个执行一次 · ${giftPriceLabel(item.gift)}` }),
          ]),
        ]),
        removeButton,
      ]));
      const formulaNameInput = inputField('公式名称', item.formulaName);
      formulaNameInput.placeholder = `例如 ${item.gift.name}加时`;
      formulaNameInput.oninput = () => {
        item.formulaName = formulaNameInput.value;
      };
      const formulaInput = inputField('触发后属性值', item.formula);
      formulaInput.classList.add('formula');
      formulaInput.placeholder = `${nameInput.value.trim() || '属性'}+60`;
      const formulaControl = el('label', { class: 'field formula-assignment-field' });
      const assignmentRow = el('div', { class: 'formula-assignment-row' });
      assignmentRow.append(
        el('code', { class: 'formula-target-name', text: `${nameInput.value.trim() || '属性'} =` }),
        formulaInput,
      );
      formulaControl.append(
        el('span', { class: 'field-label', text: '触发后属性值' }),
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
      allGifts = availableGifts(state);
      renderGiftPicker();
      renderSelectedRules();
    }, selected, defaultFormula);

    const giftsPanel = el('section', { class: 'gift-binding-panel' });
    giftsPanel.append(
      el('div', { class: 'modal-section-heading' }, [
        el('div', {}, [
          el('h3', { text: '选择会影响这个属性的礼物' }),
          el('p', { text: '可选择任意数量；同一个礼物也可以出现在其他属性中。' }),
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
          el('h3', { text: '为每个礼物配置公式' }),
          el('p', {}, [
            '可用变量：',
            el('code', { text: 'price' }),
            ' 和任意属性名。连送会按单个礼物逐次执行。',
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
      void saveAttributeEditor(index, original, nameInput, valueInput, formatSelect, suffixInput, selected, timerRules, overlay, saveButton);
    };

    modal.append(
      basics,
      el('div', { class: 'modal-tip', text: '礼物公式和定时公式都由托盘后台执行；关闭配置页或 OBS 不会停止计算。' }),
      timerPanel,
      giftsPanel,
      formulasPanel,
      el('footer', { class: 'modal-actions' }, [cancelButton, saveButton]),
    );
    overlay.append(modal);
    overlay.onclick = (event) => {
      if (event.target === overlay) close();
    };
    root.append(overlay);
    renderGiftPicker();
    renderSelectedRules();
    renderTimerRules();
    renderGuide();
    nameInput.focus();
  }

  function renderManualGiftAdder(
    onAdded: () => void,
    selected: Map<number, SelectedGiftRule>,
    defaultFormula: () => string,
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
      selected.set(id, { gift, formulaName: `${gift.name}规则`, formula: defaultFormula() });
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
    selected: Map<number, SelectedGiftRule>,
    timerRules: TimerRule[],
    overlay: HTMLElement,
    saveButton: HTMLButtonElement,
  ): Promise<void> {
    return saveAttributeEditorAsync(index, original, nameInput, valueInput, formatSelect, suffixInput, selected, timerRules, overlay, saveButton);
  }

  async function saveAttributeEditorAsync(
    index: number | undefined,
    original: Attribute | undefined,
    nameInput: HTMLInputElement,
    valueInput: HTMLInputElement,
    formatSelect: HTMLSelectElement,
    suffixInput: HTMLInputElement,
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
        toast(`请填写“${item.gift.name}”的公式名称`, root);
        return;
      }
      const formula = originalName && originalName !== name
        ? replaceFormulaVariable(item.formula.trim(), originalName, name)
        : item.formula.trim();
      if (!formula) {
        toast(`请填写“${item.gift.name}”的公式`, root);
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
        toast(`请填写“${formulaName}”的公式`, root);
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
      toast(error instanceof Error ? `公式有误：${error.message}` : '公式有误', root);
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
    render();
    toast(index === undefined ? '属性已创建' : '属性配置已保存', root);
  }

  function renderAdvancedSettings(): void {
    const details = el('details', { class: 'advanced-settings' });
    details.append(el('summary', {}, [
      el('span', { text: '外观与数据' }),
      el('small', { text: '面板字号、颜色、对齐和配置备份' }),
    ]));
    const settingsGrid = el('div', { class: 'advanced-settings-grid' });

    const appearance = el('section', { class: 'workspace-card advanced-card' });
    appearance.append(el('h3', { text: 'OBS 面板外观' }));
    const fontSize = inputField('字体大小（px）', String(state.settings.fontSize));
    fontSize.onchange = () => {
      state.settings.fontSize = Number(fontSize.value) || 48;
      save();
    };
    const accent = inputField('强调色', state.settings.accentColor);
    accent.onchange = () => {
      state.settings.accentColor = accent.value;
      save();
    };
    const align = el('select', { class: 'field-input' }) as HTMLSelectElement;
    align.innerHTML = '<option value="left">左对齐</option><option value="center">居中</option><option value="right">右对齐</option>';
    align.value = state.settings.align;
    align.onchange = () => {
      state.settings.align = align.value as AppState['settings']['align'];
      save();
    };
    const showStats = checkboxField('显示今日统计', state.settings.showStats, (checked) => {
      state.settings.showStats = checked;
      save();
    });
    const showConnection = checkboxField('显示连接状态', state.settings.showConnection, (checked) => {
      state.settings.showConnection = checked;
      save();
    });
    appearance.append(
      fieldControl(fontSize),
      fieldControl(accent),
      el('label', { class: 'field' }, [el('span', { class: 'field-label', text: '对齐' }), align]),
      showStats,
      showConnection,
    );

    const dataCard = el('section', { class: 'workspace-card advanced-card' });
    dataCard.append(
      el('h3', { text: '配置与数据' }),
      el('p', { class: 'advanced-copy', text: `当前有 ${state.attributes.length} 个属性、${state.rules.length} 条礼物公式、${state.timerRules.length} 个定时器和 ${state.log.length} 条变动记录。` }),
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
    settingsGrid.append(appearance, dataCard);
    details.append(settingsGrid);
    content.append(details);
  }

  function checkboxField(label: string, checked: boolean, onChange: (checked: boolean) => void): HTMLElement {
    const input = el('input', { type: 'checkbox' }) as HTMLInputElement;
    input.checked = checked;
    input.onchange = () => onChange(input.checked);
    return el('label', { class: 'checkbox-field' }, [input, el('span', { text: label })]);
  }

  function renderFormulaHelp(attributeName: string): HTMLElement {
    const details = el('details', { class: 'formula-help' }) as HTMLDetailsElement;
    const current = attributeName || '属性';
    details.append(
      el('summary', { text: '公式怎么用？查看完整说明' }),
      el('div', { class: 'formula-help-content' }, [
        el('p', { text: '等号右侧的计算结果会成为属性的新值。要在原值上增加或减少，公式中必须写上当前属性名；只写数字会直接把属性设成该数字。' }),
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
  const pollTimer = globalThis.setInterval(() => {
    void refreshRuntime();
    void refreshBackendState();
  }, 1000);
  const disposePolling = (): void => globalThis.clearInterval(pollTimer);
  if (typeof globalThis.addEventListener === 'function') globalThis.addEventListener('beforeunload', disposePolling, { once: true });
}

function availableGifts(state: AppState): GiftInfo[] {
  const configuredGifts = state.rules
    .map((rule) => findGift(state, rule.giftId))
    .filter((gift): gift is GiftInfo => gift !== undefined);
  const seen = new Set<string>();
  return [...configuredGifts, ...state.recentGifts, ...builtinCatalog].filter((gift) => {
    const key = giftDisplayKey(gift);
    if (seen.has(key)) return false;
    seen.add(key);
    return true;
  });
}

function configStructureSignature(state: AppState): string {
  return JSON.stringify({
    roomId: state.roomId,
    attributes: state.attributes.map(({ value: _value, ...attribute }) => attribute),
    rules: state.rules,
    timerRules: state.timerRules,
    settings: state.settings,
    giftCatalog: state.giftCatalog,
  });
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

function replaceFormulaVariable(formula: string, from: string, to: string): string {
  if (!from || from === to) return formula;
  const escaped = from.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
  return formula.replace(new RegExp(`(?<![\\p{L}\\p{N}_])${escaped}(?![\\p{L}\\p{N}_])`, 'gu'), to);
}

function validImportedState(parsed: Partial<AppState> | null): parsed is Partial<AppState> {
  return parsed !== null
    && typeof parsed === 'object'
    && !Array.isArray(parsed)
    && (parsed.attributes === undefined || Array.isArray(parsed.attributes))
    && (parsed.rules === undefined || Array.isArray(parsed.rules))
    && (parsed.timerRules === undefined || Array.isArray(parsed.timerRules))
    && (parsed.settings === undefined || (typeof parsed.settings === 'object' && parsed.settings !== null));
}

function normalizeConfigTheme(theme: unknown): 'dark' | 'light' {
  return theme === 'light' ? 'light' : 'dark';
}
