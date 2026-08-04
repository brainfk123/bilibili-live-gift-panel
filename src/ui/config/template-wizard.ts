import { DISPLAY_THEMES, getDisplayTheme } from '../../display-themes';
import { formatValue } from '../../format';
import {
  buildGameplayTemplate,
  createDefaultTemplateInput,
  GAMEPLAY_TEMPLATES,
  validateGameplayTemplateInput,
  type GameplayTemplateBuildResult,
  type GameplayTemplateDefinition,
  type GameplayTemplateInput,
  type TemplateParameterDefinition,
} from '../../gameplay-templates';
import { matchesGiftSearch } from '../../gifts/catalog';
import type { GiftInfo } from '../../types';
import { el } from '../common';

const GIFT_PAGE_SIZE = 40;

export interface GameplayTemplateWizardOptions {
  gifts: GiftInfo[];
  existingAttributeNames: string[];
  onCreate: (result: GameplayTemplateBuildResult) => Promise<void>;
  onBlank: () => void;
  onClose?: () => void;
}

export interface GameplayTemplateWizard {
  element: HTMLElement;
  close: () => void;
}

export function createGameplayTemplateWizard(options: GameplayTemplateWizardOptions): GameplayTemplateWizard {
  const overlay = el('div', { class: 'overlay template-wizard-overlay' });
  const dialog = el('section', {
    class: 'card template-wizard',
    role: 'dialog',
    ariaLabel: '从玩法模板创建属性',
  } as any);
  const header = el('header', { class: 'template-wizard-header' });
  const progress = el('ol', { class: 'template-wizard-progress' });
  const body = el('div', { class: 'template-wizard-body' });
  const message = el('p', { class: 'template-wizard-message', role: 'status' } as any);
  const backButton = el('button', { class: 'btn ghost', type: 'button', text: '上一步' }) as HTMLButtonElement;
  const nextButton = el('button', { class: 'btn', type: 'button', text: '下一步' }) as HTMLButtonElement;
  const footer = el('footer', { class: 'template-wizard-footer' }, [message, el('div', { class: 'template-wizard-actions' }, [backButton, nextButton])]);
  const closeButton = el('button', { class: 'modal-close', type: 'button', text: '×', ariaLabel: '关闭模板向导' } as any) as HTMLButtonElement;
  let step = 1;
  let selectedTemplate: GameplayTemplateDefinition | undefined;
  let input: GameplayTemplateInput | undefined;
  let activeGiftSlotId = '';
  let giftSearchQuery = '';
  let visibleGiftCount = GIFT_PAGE_SIZE;
  let saving = false;

  const close = (): void => {
    overlay.remove();
    options.onClose?.();
  };
  closeButton.onclick = close;
  overlay.onpointerdown = (event) => {
    overlay.dataset.pointerOutside = String(event.target === overlay);
  };
  overlay.onclick = (event) => {
    const shouldClose = overlay.dataset.pointerOutside === 'true' && event.target === overlay;
    overlay.dataset.pointerOutside = 'false';
    if (shouldClose) close();
  };

  function setMessage(text = '', tone: 'error' | 'normal' = 'normal'): void {
    message.textContent = text;
    message.classList.toggle('is-error', tone === 'error');
  }

  function selectTemplate(template: GameplayTemplateDefinition): void {
    selectedTemplate = template;
    input = createDefaultTemplateInput(template);
    activeGiftSlotId = template.giftSlots[0]?.id ?? '';
    giftSearchQuery = '';
    visibleGiftCount = GIFT_PAGE_SIZE;
    setMessage();
    render();
  }

  function goToStep(nextStep: number): void {
    step = Math.min(4, Math.max(1, nextStep));
    setMessage();
    render();
  }

  function validateParameters(): string | null {
    if (!selectedTemplate || !input) return '请先选择玩法';
    for (const parameter of selectedTemplate.parameters) {
      const value = input.parameters[parameter.id];
      if (parameter.kind === 'text' && String(value ?? '').trim() === '') return `请填写${parameter.label}`;
      if (parameter.kind === 'number' || parameter.kind === 'duration') {
        const numeric = Number(value);
        if (!Number.isFinite(numeric)) return `${parameter.label}必须是数字`;
        if (parameter.min !== undefined && numeric < parameter.min) return `${parameter.label}不能小于${displayParameterNumber(parameter, parameter.min)}`;
        if (parameter.max !== undefined && numeric > parameter.max) return `${parameter.label}不能大于${displayParameterNumber(parameter, parameter.max)}`;
      }
    }
    return null;
  }

  function buildResult(): GameplayTemplateBuildResult | null {
    if (!selectedTemplate || !input) return null;
    const errors = validateGameplayTemplateInput(selectedTemplate, input);
    if (errors.length > 0) {
      setMessage(errors[0], 'error');
      return null;
    }
    try {
      return buildGameplayTemplate(selectedTemplate, input);
    } catch (error) {
      setMessage(error instanceof Error ? error.message : '模板配置有误', 'error');
      return null;
    }
  }

  async function confirmCreate(): Promise<void> {
    if (saving) return;
    const result = buildResult();
    if (!result) return;
    const duplicated = result.attributes.find((attribute) => options.existingAttributeNames.includes(attribute.name));
    if (duplicated) {
      setMessage(`已经存在名为“${duplicated.name}”的属性，请返回修改名称`, 'error');
      return;
    }
    saving = true;
    nextButton.disabled = true;
    nextButton.textContent = '后台校验中…';
    try {
      await options.onCreate(result);
      close();
    } catch (error) {
      setMessage(error instanceof Error ? error.message : '创建失败，请检查配置后重试', 'error');
      saving = false;
      nextButton.disabled = false;
      nextButton.textContent = '确认创建';
    }
  }

  backButton.onclick = () => goToStep(step - 1);
  nextButton.onclick = () => {
    if (step === 1) {
      if (!selectedTemplate) {
        setMessage('请先选择一个玩法模板', 'error');
        return;
      }
      goToStep(2);
      return;
    }
    if (step === 2) {
      const error = validateParameters();
      if (error) {
        setMessage(error, 'error');
        return;
      }
      goToStep(3);
      return;
    }
    if (step === 3) {
      if (!selectedTemplate || !input) return;
      const errors = validateGameplayTemplateInput(selectedTemplate, input);
      if (errors.length > 0) {
        setMessage(errors[0], 'error');
        return;
      }
      goToStep(4);
      return;
    }
    void confirmCreate();
  };

  function renderHeader(): void {
    const copy = [
      ['选择玩法', '先决定观众怎么玩'],
      ['核心参数', '只填写决定玩法的内容'],
      ['分配礼物', '为每个角色选择礼物'],
      ['确认创建', '检查结果和 OBS 外观'],
    ][step - 1];
    header.replaceChildren(
      el('div', {}, [
        el('span', { class: 'section-kicker', text: '玩法模板' }),
        el('h2', { text: copy[0] }),
        el('p', { text: copy[1] }),
      ]),
      closeButton,
    );
    progress.replaceChildren();
    ['选择玩法', '核心参数', '分配礼物', '确认创建'].forEach((label, index) => {
      const number = index + 1;
      progress.append(el('li', { class: `${number === step ? 'is-active' : ''}${number < step ? ' is-done' : ''}` }, [
        el('span', { text: number < step ? '✓' : String(number) }),
        el('strong', { text: label }),
      ]));
    });
  }

  function renderTemplateCards(): void {
    const intro = el('div', { class: 'template-library-intro' }, [
      el('div', {}, [
        el('h3', { text: '选择一种直播玩法' }),
        el('p', { text: '模板会一次生成属性、礼物规则、定时器和推荐的 OBS 外观，创建后仍可逐项修改。' }),
      ]),
      el('button', { class: 'btn ghost template-blank-button', type: 'button', text: '创建空白属性（高级）', onclick: () => { close(); options.onBlank(); } }),
    ]);
    const grid = el('div', { class: 'gameplay-template-grid' });
    for (const template of GAMEPLAY_TEMPLATES) {
      const selected = selectedTemplate?.id === template.id;
      const card = el('button', {
        class: `gameplay-template-card is-${template.preview}${selected ? ' is-selected' : ''}${template.id === 'overtime' ? ' guide-overtime-template' : ''}`,
        type: 'button',
        ariaPressed: String(selected),
      } as any) as HTMLButtonElement;
      card.onclick = () => selectTemplate(template);
      card.append(
        createMiniPreview(template.preview, template.recommendedThemeId),
        el('span', { class: 'gameplay-template-copy' }, [
          el('span', { class: 'gameplay-template-title-row' }, [
            el('strong', { text: template.title }),
            el('small', { text: template.difficulty }),
          ]),
          el('span', { class: 'gameplay-template-summary', text: template.summary }),
          el('span', { class: 'gameplay-template-facts' }, [
            el('small', { text: `${template.giftSlots.reduce((total, slot) => total + slot.minimum, 0)} 个起步礼物` }),
            el('small', { text: template.category === 'timer' || template.id === 'resource' ? '会自动变化' : '只在收礼时变化' }),
          ]),
        ]),
        el('span', { class: 'gameplay-template-check', text: selected ? '✓' : '选择' }),
      );
      grid.append(card);
    }
    body.append(intro, grid);
  }

  function renderParameterFields(): void {
    if (!selectedTemplate || !input) return;
    const heading = el('div', { class: 'template-step-heading' }, [
      el('div', {}, [el('h3', { text: selectedTemplate.title }), el('p', { text: selectedTemplate.audiencePlay })]),
      createMiniPreview(selectedTemplate.preview, input.displayThemeId ?? selectedTemplate.recommendedThemeId),
    ]);
    const fields = el('div', { class: 'template-parameter-grid' });
    for (const parameter of selectedTemplate.parameters) fields.append(renderParameter(parameter, input));
    body.append(heading, fields);
  }

  function renderGiftAssignment(): void {
    if (!selectedTemplate || !input) return;
    const slots = el('div', { class: 'template-gift-slots' });
    for (const slot of selectedTemplate.giftSlots) {
      const assigned = input.gifts[slot.id] ?? [];
      const button = el('button', {
        class: `template-gift-slot${activeGiftSlotId === slot.id ? ' is-active' : ''}`,
        type: 'button',
      }) as HTMLButtonElement;
      button.onclick = () => {
        activeGiftSlotId = slot.id;
        giftSearchQuery = '';
        visibleGiftCount = GIFT_PAGE_SIZE;
        render();
      };
      button.append(
        el('span', { class: 'template-gift-slot-head' }, [
          el('strong', { text: slot.label }),
          el('small', { text: slot.minimum > 0 ? `至少 ${slot.minimum} 个` : '可选' }),
        ]),
        el('span', { class: 'template-gift-slot-description', text: slot.description }),
        assigned.length > 0
          ? el('span', { class: 'template-assigned-gifts' }, assigned.slice(0, 4).map((gift) => giftChip(gift)).concat(
            assigned.length > 4 ? [el('small', { text: `+${assigned.length - 4}` })] : [],
          ))
          : el('span', { class: 'template-gift-slot-empty', text: '点击后在下方选择礼物' }),
      );
      slots.append(button);
    }
    const slot = selectedTemplate.giftSlots.find((candidate) => candidate.id === activeGiftSlotId) ?? selectedTemplate.giftSlots[0];
    if (!slot) {
      body.append(slots);
      return;
    }
    const search = el('input', {
      class: 'field-input template-gift-search',
      value: giftSearchQuery,
      placeholder: `搜索要分配给“${slot.label}”的礼物名称或 ID…`,
      ariaLabel: '搜索模板礼物',
    } as any) as HTMLInputElement;
    const grid = el('div', { class: 'template-gift-grid' });
    const status = el('small', { class: 'template-gift-grid-status' });
    const renderGrid = (): void => {
      const matches = options.gifts.filter((gift) => matchesGiftSearch(gift, giftSearchQuery));
      const visible = matches.slice(0, visibleGiftCount);
      grid.replaceChildren();
      for (const gift of visible) grid.append(renderGiftChoice(gift, slot.id, slot.multiple));
      status.textContent = matches.length === 0
        ? '没有匹配的礼物'
        : visible.length < matches.length ? `已显示 ${visible.length} / ${matches.length}，向下滚动加载更多` : `已显示全部 ${matches.length} 个礼物`;
    };
    search.oninput = () => {
      giftSearchQuery = search.value;
      visibleGiftCount = GIFT_PAGE_SIZE;
      renderGrid();
    };
    grid.onscroll = () => {
      if (grid.scrollTop + grid.clientHeight < grid.scrollHeight - 48) return;
      visibleGiftCount += GIFT_PAGE_SIZE;
      renderGrid();
    };
    renderGrid();
    body.append(
      el('div', { class: 'template-step-heading template-gift-heading' }, [
        el('div', {}, [el('h3', { text: '把礼物分配给玩法角色' }), el('p', { text: '同一个礼物只承担一个角色，选择到新角色时会自动移动。' })]),
      ]),
      slots,
      el('section', { class: 'template-gift-picker' }, [
        el('div', { class: 'template-gift-picker-title' }, [el('strong', { text: slot.label }), el('small', { text: `${(input.gifts[slot.id] ?? []).length} 个已选择` })]),
        search,
        grid,
        status,
      ]),
    );
  }

  function renderGiftChoice(gift: GiftInfo, slotId: string, multiple: boolean): HTMLElement {
    if (!input) return el('span');
    const selected = (input.gifts[slotId] ?? []).some((candidate) => candidate.id === gift.id);
    const usedBy = Object.entries(input.gifts).find(([candidateSlotId, gifts]) => candidateSlotId !== slotId && gifts.some((candidate) => candidate.id === gift.id))?.[0];
    const image = el('img', { class: 'template-gift-image', alt: '' }) as HTMLImageElement;
    image.src = gift.imgBasic || transparentPixel();
    const button = el('button', {
      class: `template-gift-choice${selected ? ' is-selected' : ''}`,
      type: 'button',
      ariaPressed: String(selected),
      title: usedBy ? '已用于其他角色，点击后移动到当前角色' : gift.name,
    } as any) as HTMLButtonElement;
    button.onclick = () => {
      if (!input) return;
      for (const [candidateSlotId, gifts] of Object.entries(input.gifts)) {
        if (candidateSlotId === slotId) continue;
        input.gifts[candidateSlotId] = gifts.filter((candidate) => candidate.id !== gift.id);
      }
      const current = input.gifts[slotId] ?? [];
      input.gifts[slotId] = selected
        ? current.filter((candidate) => candidate.id !== gift.id)
        : multiple ? [...current, gift] : [gift];
      setMessage();
      render();
    };
    button.append(
      image,
      el('span', { class: 'template-gift-choice-copy' }, [
        el('strong', { text: gift.name, title: gift.name }),
        el('small', { text: `${gift.price} ${gift.coinType === 'gold' ? '金瓜子' : '银瓜子'}` }),
      ]),
      el('span', { class: 'template-gift-choice-action', text: selected ? '✓' : usedBy ? '移动' : '+' }),
    );
    return button;
  }

  function renderConfirmation(): void {
    if (!selectedTemplate || !input) return;
    const result = buildResult();
    if (!result) return;
    const attribute = result.attributes[0];
    const themeGrid = el('div', { class: 'display-theme-grid template-theme-grid' });
    for (const theme of DISPLAY_THEMES) {
      const selected = input.displayThemeId === theme.id;
      const button = el('button', {
        class: `display-theme-option ${theme.previewClass}${selected ? ' is-selected' : ''}`,
        type: 'button',
        ariaPressed: String(selected),
        title: theme.description,
      } as any) as HTMLButtonElement;
      button.onclick = () => {
        if (!input) return;
        input.displayThemeId = theme.id;
        render();
      };
      button.append(
        el('span', { class: 'display-theme-swatch' }, [el('span'), el('span')]),
        el('span', {}, [el('strong', { text: theme.name }), el('small', { text: theme.recommendedFor })]),
        el('span', { class: 'display-theme-check', text: selected ? '✓' : '' }),
      );
      themeGrid.append(button);
    }
    const summary = el('ul', { class: 'template-result-summary' }, result.summary.map((line) => el('li', { text: line })));
    const rules = el('div', { class: 'template-result-rules' });
    for (const rule of result.rules) {
      const gift = result.usedGifts.find((candidate) => candidate.id === rule.giftId);
      rules.append(el('div', { class: 'template-result-rule' }, [
        gift ? giftChip(gift) : el('span', { text: `礼物 ${rule.giftId}` }),
        el('span', {}, [el('strong', { text: rule.formulaName ?? '礼物规则' }), el('small', { text: '收到 1 个执行一次' })]),
      ]));
    }
    body.append(
      el('div', { class: 'template-confirm-grid' }, [
        el('section', { class: 'template-confirm-copy' }, [
          el('span', { class: 'section-kicker', text: selectedTemplate.title }),
          el('h3', { text: `将创建“${attribute.name}”` }),
          summary,
          rules,
          result.timerRules.length > 0
            ? el('div', { class: 'template-result-timers' }, result.timerRules.map((timerRule) => el('span', { text: `⏱ ${timerRule.formulaName} · 每 ${timerRule.intervalSeconds} 秒` })))
            : el('p', { class: 'template-result-no-timer', text: '这个玩法不需要定时器。' }),
        ]),
        el('section', { class: 'template-confirm-preview' }, [
          el('h3', { text: 'OBS 真实结构预览' }),
          createResultPreview(result),
          el('p', { text: '切换外观不会改变数值、规则或 OBS 链接。' }),
        ]),
      ]),
      el('section', { class: 'template-theme-section' }, [
        el('div', {}, [el('h3', { text: '选择初始外观' }), el('p', { text: `推荐：${getDisplayTheme(selectedTemplate.recommendedThemeId).name}，创建后可随时更换。` })]),
        themeGrid,
      ]),
    );
  }

  function render(): void {
    renderHeader();
    body.replaceChildren();
    if (step === 1) renderTemplateCards();
    else if (step === 2) renderParameterFields();
    else if (step === 3) renderGiftAssignment();
    else renderConfirmation();
    backButton.hidden = step === 1;
    nextButton.textContent = step === 4 ? '确认创建' : '下一步';
    nextButton.disabled = saving;
    footer.classList.toggle('is-first-step', step === 1);
  }

  dialog.append(header, progress, body, footer);
  overlay.append(dialog);
  render();
  return { element: overlay, close };
}

function renderParameter(parameter: TemplateParameterDefinition, input: GameplayTemplateInput): HTMLElement {
  const description = parameter.description ? el('small', { text: parameter.description }) : null;
  if (parameter.kind === 'toggle') {
    const checkbox = el('input', { type: 'checkbox' }) as HTMLInputElement;
    checkbox.checked = input.parameters[parameter.id] === true;
    checkbox.onchange = () => { input.parameters[parameter.id] = checkbox.checked; };
    return el('label', { class: 'template-parameter template-toggle-parameter' }, [
      checkbox,
      el('span', { class: 'setting-switch-track', ariaHidden: 'true' }),
      el('span', {}, [el('strong', { text: parameter.label }), ...(description ? [description] : [])]),
    ]);
  }
  const label = el('span', { class: 'field-label', text: parameter.label });
  let control: HTMLElement;
  if (parameter.kind === 'select') {
    const select = el('select', { class: 'field-input' }) as HTMLSelectElement;
    for (const option of parameter.options ?? []) select.append(el('option', { value: option.value, text: option.label }));
    select.value = String(input.parameters[parameter.id] ?? '');
    select.onchange = () => { input.parameters[parameter.id] = select.value; };
    control = select;
  } else {
    const inputElement = el('input', {
      class: 'field-input',
      type: parameter.kind === 'text' ? 'text' : 'number',
      value: parameter.kind === 'duration'
        ? String(durationDisplayValue(parameter, Number(input.parameters[parameter.id])))
        : String(input.parameters[parameter.id] ?? ''),
    }) as HTMLInputElement;
    inputElement.dataset.fieldLabel = parameter.label;
    if (parameter.kind !== 'text') {
      if (parameter.min !== undefined) inputElement.min = String(displayParameterNumber(parameter, parameter.min));
      if (parameter.max !== undefined) inputElement.max = String(displayParameterNumber(parameter, parameter.max));
      inputElement.step = String(parameter.step ?? (parameter.kind === 'duration' ? 1 : 1));
      inputElement.inputMode = 'decimal';
    }
    inputElement.oninput = () => {
      input.parameters[parameter.id] = parameter.kind === 'text'
        ? inputElement.value
        : parameter.kind === 'duration'
          ? Number(inputElement.value) * (parameter.durationUnit === 'minutes' ? 60 : 1)
          : Number(inputElement.value);
    };
    control = parameter.unit || parameter.kind === 'duration'
      ? el('div', { class: 'template-input-with-unit' }, [
        inputElement,
        el('span', { text: parameter.unit ?? (parameter.durationUnit === 'minutes' ? '分钟' : '秒') }),
      ])
      : inputElement;
  }
  return el('label', { class: 'field template-parameter' }, [label, control, ...(description ? [description] : [])]);
}

function durationDisplayValue(parameter: TemplateParameterDefinition, value: number): number {
  return parameter.durationUnit === 'minutes' ? value / 60 : value;
}

function displayParameterNumber(parameter: TemplateParameterDefinition, value: number): number {
  return parameter.kind === 'duration' ? durationDisplayValue(parameter, value) : value;
}

function giftChip(gift: GiftInfo): HTMLElement {
  const image = el('img', { alt: '' }) as HTMLImageElement;
  image.src = gift.imgBasic || transparentPixel();
  return el('span', { class: 'template-gift-chip', title: gift.name }, [image, el('span', { text: gift.name })]);
}

function createMiniPreview(
  variant: GameplayTemplateDefinition['preview'],
  themeId: string,
): HTMLElement {
  return el('span', { class: `template-mini-preview is-${variant} theme-${themeId}` }, [
    el('span', { class: 'template-mini-value', text: variant === 'timer' ? '01:30:00' : variant === 'health' ? '720 / 1000' : '68' }),
    el('span', { class: 'template-mini-track' }, [el('span')]),
  ]);
}

function createResultPreview(result: GameplayTemplateBuildResult): HTMLElement {
  const attribute = result.attributes[0];
  const display = attribute.display;
  const maximum = display?.max ?? Math.max(100, attribute.value);
  const minimum = display?.min ?? 0;
  const progress = Math.max(0, Math.min(100, ((attribute.value - minimum) / Math.max(1, maximum - minimum)) * 100));
  const rules = result.rules.slice(0, 4).map((rule) => {
    const gift = result.usedGifts.find((candidate) => candidate.id === rule.giftId);
    return el('span', { class: 'template-preview-rule' }, [
      gift ? giftChip(gift) : el('span', { text: '礼物' }),
      el('strong', { text: rule.formulaName ?? '规则' }),
    ]);
  });
  return el('div', {
    class: `template-result-preview theme-${display?.themeId ?? 'glass'} is-${display?.variant ?? 'number'}`,
    style: `--preview-progress:${progress}%`,
  }, [
    el('div', { class: 'template-preview-summary' }, [
      el('span', { text: display?.title || attribute.name }),
      el('strong', { text: formatValue(attribute.value, attribute) }),
      ...(['progress', 'health', 'resource', 'tug'].includes(display?.variant ?? '')
        ? [el('span', { class: 'template-preview-meter' }, [el('span')])]
        : []),
    ]),
    el('div', { class: 'template-preview-rules' }, rules),
    el('div', { class: 'template-preview-broadcast', text: attribute.broadcastMessage || '感谢大家的支持' }),
  ]);
}

function transparentPixel(): string {
  return 'data:image/gif;base64,R0lGODlhAQABAIAAAAAAAP///yH5BAEAAAAALAAAAAABAAEAAAIBRAA7';
}

