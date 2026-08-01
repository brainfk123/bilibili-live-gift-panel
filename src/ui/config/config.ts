import { AppState, GiftRule, STORAGE_KEY } from '../../types';
import { loadState, saveState } from '../../storage';
import { el, fieldControl, inputField, toast } from '../common';
import { builtinCatalog, findGift, upsertRecentGift } from '../../gifts/catalog';
import { evalFormula, collectVars, FormulaError } from '../../formula';
import { formatValue, todayStr } from '../../format';
import { parseGift } from '../../bilibili/messages';
import { DanmakuClient, ConnState } from '../../bilibili/client';
import { createBrandIcon } from '../brand';
import { getNextWizardStep, getWizardChecklist, getWizardProgress, type WizardStep } from './wizard';

export function mountConfig(root: HTMLElement): void {
  let state = loadState();
  const initialProgress = getWizardProgress(state);
  let current: string = getNextWizardStep(initialProgress) ?? 'obs';
  let client: DanmakuClient | null = null;
  let connectionState: ConnState = 'idle';
  const shell = el('div', { class: 'wizard-shell' });
  const header = el('header', { class: 'app-header' });
  const brand = el('div', { class: 'app-brand' });
  brand.append(createBrandIcon(40), el('div', { class: 'app-brand-copy' }, [
    el('strong', { text: '直播礼物面板' }),
    el('span', { text: '简单四步，开始互动' }),
  ]));
  const status = el('div', { class: 'app-status' });
  header.append(brand, status);

  const content = el('main', { class: 'wizard-content' });
  shell.append(header, content);
  root.replaceChildren(shell);

  const wizardSteps: { key: WizardStep; label: string }[] = [
    { key: 'room', label: '连接房间' },
    { key: 'attributes', label: '设置属性' },
    { key: 'rules', label: '绑定礼物' },
    { key: 'obs', label: '放进 OBS' },
  ];

  function connectionLabel(stateValue: ConnState): string {
    if (stateValue === 'connected') return '已连接';
    if (stateValue === 'connecting') return '连接中…';
    if (stateValue === 'reconnecting') return '重连中…';
    if (stateValue === 'error') return '连接失败';
    return '未连接';
  }

  function renderWizardHeader(): void {
    status.className = `app-status is-${connectionState}`;
    status.replaceChildren(
      el('span', { class: 'app-status-dot' }),
      el('span', { text: connectionLabel(connectionState) }),
    );
  }

  function renderProgress(): void {
    const progress = getWizardProgress(state);
    const progressNav = el('nav', { class: 'wizard-progress' });
    progressNav.setAttribute('aria-label', '配置进度');
    for (const [index, step] of wizardSteps.entries()) {
      const done = progress[step.key];
      const item = el('button', {
        class: `wizard-progress-item${current === step.key ? ' is-active' : ''}${done ? ' is-done' : ''}`,
        type: 'button',
      }) as HTMLButtonElement;
      item.dataset.step = step.key;
      item.append(
        el('span', { class: 'wizard-progress-number', text: done ? '✓' : String(index + 1) }),
        el('span', { class: 'wizard-progress-label', text: step.label }),
      );
      item.onclick = () => switchTo(step.key);
      progressNav.append(item);
    }
    const currentProgress = content.querySelector('.wizard-progress');
    if (currentProgress) currentProgress.replaceWith(progressNav);
    else content.append(progressNav);
  }

  function switchTo(key: string, stopClient = true): void {
    if (stopClient) {
      client?.stop();
      client = null;
      connectionState = 'idle';
    }
    current = key;
    renderWizardHeader();
    render();
  }

  function render(): void {
    content.replaceChildren();
    renderWizardHeader();
    renderProgress();
    if (current === 'room') renderRoom();
    else if (current === 'attributes') renderAttributes();
    else if (current === 'rules') renderRules();
    else if (current === 'obs') renderOnboarding();
    else if (current === 'stats') renderStats();
    else if (current === 'settings') renderSettings();
    else if (current === 'manual') renderManualAdd();
    renderMoreSettings();
  }

  function save(): void {
    saveState(state);
    renderProgress();
  }

  function guideCard(text: string, bold?: string): void {
    const card = el('div', { class: 'guide-card' });
    if (bold) card.append(el('b', { text: bold }), el('span', { text: ' ' + text }));
    else card.append(el('span', { text }));
    content.append(card);
  }

  function emptyState(text: string): HTMLElement {
    const empty = el('div', { class: 'empty' });
    empty.append(createBrandIcon(44, 'empty-brand-icon'), el('span', { text }));
    return empty;
  }

  function renderOnboarding(): void {
    const progress = getWizardProgress(state);
    const ready = progress.obs;

    if (ready) {
      const home = el('div', { class: 'completion-home' });
      const statusCard = el('div', { class: 'completion-status' });
      statusCard.append(
        el('span', { class: 'completion-status-label', text: '当前连接状态' }),
        el('strong', { text: connectionLabel(connectionState) }),
      );

      const attributeCard = el('div', { class: 'completion-summary-card' });
      attributeCard.append(el('h2', { class: 'completion-section-title', text: '属性预览' }));
      for (const attribute of state.attributes) {
        attributeCard.append(el('div', { class: 'completion-attribute' }, [
          el('span', { class: 'completion-attribute-name', text: attribute.name }),
          el('strong', { class: 'completion-attribute-value', text: formatValue(attribute.value, attribute) }),
        ]));
      }

      const recentRule = state.rules[state.rules.length - 1];
      const recentGift = recentRule ? findGift(state, recentRule.giftId) : undefined;
      const ruleCard = el('div', { class: 'completion-summary-card' });
      ruleCard.append(el('h2', { class: 'completion-section-title', text: '最近规则' }));
      if (recentRule) {
        ruleCard.append(
          el('div', { class: 'completion-rule-name', text: `${recentGift?.name ?? `礼物${recentRule.giftId}`} → ${recentRule.attributeName}` }),
          el('code', { class: 'completion-rule-formula', text: recentRule.formula }),
        );
      }

      const obsUrl = `${location.origin}/?mode=display`;
      const card = el('div', { class: 'completion-card' });
      const urlInput = el('input', { class: 'field-input', value: obsUrl, readOnly: true }) as HTMLInputElement;
      const copyButton = el('button', { class: 'btn', text: '复制地址' }) as HTMLButtonElement;
      copyButton.onclick = async () => {
        try {
          await navigator.clipboard.writeText(urlInput.value);
          toast('OBS 地址已复制', root);
        } catch {
          urlInput.select();
          toast('请按 Ctrl+C 复制地址', root);
        }
      };
      const urlRow = el('div', { class: 'ready-url' });
      urlRow.append(urlInput, copyButton);
      const instructions = el('ol', { class: 'obs-steps' });
      for (const text of ['OBS 添加“浏览器”来源。', '粘贴地址并设置宽高。', '保持 gift-panel.exe 运行。']) {
        instructions.append(el('li', { class: 'obs-step', text }));
      }
      card.append(
        createBrandIcon(56, 'completion-brand-icon'),
        el('div', { class: 'completion-title', text: '配置完成' }),
        el('p', { class: 'completion-subtitle', text: '把下面的地址添加到 OBS 浏览器源。' }),
        urlRow,
        instructions,
      );
      const restartButton = el('button', { class: 'secondary-toggle completion-restart', text: '重新查看向导' }) as HTMLButtonElement;
      restartButton.onclick = () => switchTo('room');
      home.append(statusCard, attributeCard, ruleCard, card, restartButton);
      content.append(home);
      return;
    }

    const nextTarget = !progress.room ? 'room' : !progress.attributes ? 'attributes' : !progress.rules ? 'rules' : '';
    const steps = getWizardChecklist(progress);
    const box = el('div', { class: 'onboard' });
    box.append(
      el('div', { class: 'onboard-title', text: '第一次使用？跟着做就好' }),
      el('div', { class: 'onboard-desc', text: '按顺序完成下面几步。每一步都有说明，点按钮可以直接跳到对应位置。' }),
    );
    const stepsRow = el('div', { class: 'steps' });
    for (const { label, target, done } of steps) {
      const active = !done && target === nextTarget;
      const s = el('div', { class: done ? 'step done' : active ? 'step active-step' : 'step' }, [
        el('span', { class: 'step-num', text: done ? '✓' : '○' }),
        el('span', { text: label }),
      ]);
      s.onclick = () => {
        if (!done || target !== 'room') switchTo(target);
      };
      stepsRow.append(s);
    }
    box.append(stepsRow);

    if (!ready) {
      const next = el('div', { class: 'onboard-next' });
      if (nextTarget === 'room') {
        next.append(
          el('span', { text: '现在先填你的直播间房间号。' }),
          el('button', { class: 'btn', text: '去填写房间号' }),
        );
      } else if (nextTarget === 'rules') {
        next.append(
          el('span', { text: '房间已设置。下一步给一个礼物绑定“加多少时间”。' }),
          el('button', { class: 'btn', text: '去配置第一个礼物' }),
        );
      } else {
        next.append(
          el('span', { text: '属性已经准备好了，可以继续下一步。' }),
          el('button', { class: 'btn', text: '去看属性设置' }),
        );
      }
      const nextButton = next.querySelector('button') as HTMLButtonElement;
      nextButton.onclick = () => switchTo(nextTarget || 'room');
      box.append(next);
    }
    content.append(box);
  }

  function buildPreviewEnv(s: AppState): Record<string, number> {
    const env: Record<string, number> = { price: 1000, count: 1 };
    for (const a of s.attributes) env[a.name] = a.value;
    return env;
  }

  const numOrUndef = (v: string): number | undefined => {
    const n = Number(v);
    return v.trim() === '' || !Number.isFinite(n) ? undefined : n;
  };

  function renderRoom(): void {
    content.append(
      el('h1', { class: 'wizard-main-title', text: '输入你的直播间房间号' }),
      el('p', { class: 'wizard-subtitle', text: '填好后点击测试连接。' }),
    );
    const roomHelp = el('details', { class: 'details-card' });
    roomHelp.append(
      el('summary', { text: '房间号在哪里？' }),
      el('p', { text: '看地址中 live.bilibili.com/ 后面的数字，不要复制问号后的访问参数。' }),
      el('code', { text: 'https://live.bilibili.com/88888888?live_from=1111&visit_id=abc123' }),
      el('p', { text: '要填写：88888888' }),
    );
    const card = el('div', { class: 'card' });
    const roomInput = inputField('房间号', state.roomId);
    roomInput.placeholder = '例如 88888888';
    const row = el('div', { class: 'row input-row gap' });
    const statusText = el('span', { text: connectionLabel(connectionState) });
    const connectBtn = el('button', { class: 'btn', text: '测试连接' }) as HTMLButtonElement;
    connectBtn.onclick = () => {
      const roomId = roomInput.value.trim();
      if (!roomId) { toast('请输入房间号', root); return; }
      state.roomId = roomId;
      save();
      client?.stop();
      client = new DanmakuClient({
        roomId: Number(roomId),
        onState: (s: ConnState) => {
          connectionState = s;
          if (s === 'connected' && current === 'room') {
            switchTo('attributes', false);
            return;
          }
          statusText.textContent = connectionLabel(s);
          renderWizardHeader();
        },
        onGift: (ev) => {
          upsertRecentGift(state, ev);
          save();
        },
      });
      void client.start();
    };
    row.append(connectBtn, statusText);
    card.append(fieldControl(roomInput), row);
    content.append(roomHelp, card);
  }

  function renderAttributes(): void {
    content.append(
      el('h1', { class: 'wizard-main-title', text: '设置属性' }),
      el('p', { class: 'wizard-subtitle', text: '属性就是礼物触发后会变化的数字，例如加班时间。' }),
    );
    if (state.attributes.length === 0) {
      content.append(emptyState('还没有属性，点击下方「+ 新增属性」创建一个吧（推荐：加班时间）。'));
    }
    const addBtn = el('button', { class: 'btn', text: '+ 新增属性' });
    addBtn.onclick = () => {
      state.attributes.push({ name: `属性${state.attributes.length + 1}`, value: 0, unit: 'seconds', format: 'hhmmss', decimals: 0, suffix: '' });
      save();
      render();
    };
    content.append(addBtn, el('div', { class: 'gap' }));
    for (let i = 0; i < state.attributes.length; i++) {
      const a = state.attributes[i];
      const card = el('div', { class: 'card' });
      const nameInput = inputField('名称', a.name);
      nameInput.oninput = () => { a.name = nameInput.value; save(); };
      const unitSelect = el('select', { class: 'field-input' }) as HTMLSelectElement;
      unitSelect.innerHTML = `<option value="seconds">秒（时间类）</option><option value="none">无单位（数值类）</option>`;
      unitSelect.value = a.unit;
      unitSelect.onchange = () => { a.unit = unitSelect.value as any; save(); render(); };
      const fmtSelect = el('select', { class: 'field-input' }) as HTMLSelectElement;
      fmtSelect.innerHTML = `<option value="hhmmss">HH:MM:SS 计时器</option><option value="number">纯数字</option><option value="suffix">数字+后缀</option>`;
      fmtSelect.value = a.format;
      fmtSelect.onchange = () => { a.format = fmtSelect.value as any; save(); render(); };
      const advanced = el('details', { class: 'details-card' });
      advanced.append(
        el('summary', { text: '更多属性设置' }),
        el('div', { class: 'field', children: [el('span', { class: 'field-label', text: '单位' }), unitSelect] }),
      );
      const preview = el('div', { class: 'preview' });
      const updatePreview = () => {
        preview.replaceChildren(el('span', { text: `当前值：` }), el('span', { class: 'result', text: formatValue(a.value, a) }));
      };
      updatePreview();
      const valueInput = inputField('初始值', String(a.value));
      valueInput.oninput = () => {
        const v = Number(valueInput.value);
        if (Number.isFinite(v)) { a.value = v; save(); updatePreview(); }
      };
      const resetBtn = el('button', { class: 'btn ghost', text: '清零' });
      resetBtn.onclick = () => { a.value = 0; valueInput.value = '0'; save(); updatePreview(); };
      const delBtn = el('button', { class: 'btn danger', text: '删除' });
      delBtn.onclick = () => {
        state.attributes.splice(i, 1);
        state.rules = state.rules.filter((r) => r.attributeName !== a.name);
        save();
        render();
      };
      card.append(
        fieldControl(nameInput),
        fieldControl(valueInput),
        el('div', { class: 'field', children: [el('span', { class: 'field-label', text: '显示格式' }), fmtSelect] }),
        advanced, preview,
        el('div', { class: 'row' }, [resetBtn, delBtn]),
      );
      content.append(card);
    }
  }

  function renderRules(): void {
    content.append(
      el('h1', { class: 'wizard-main-title', text: '绑定礼物规则' }),
      el('p', { class: 'wizard-subtitle', text: '把观众送的礼物连接到一个属性。' }),
    );
    const actions = ['搜索礼物', '选择属性', '选择公式示例', '保存规则'];
    const actionList = el('ol', { class: 'rule-actions' });
    for (const action of actions) actionList.append(el('li', { class: 'rule-action', text: action }));
    content.append(actionList);

    const addCard = el('div', { class: 'card' });
    addCard.append(el('h3', { text: '搜索礼物' }));
    const search = el('input', { class: 'field-input' }) as HTMLInputElement;
    search.placeholder = '搜索礼物名称…';
    const giftList = el('div', {});
    const seen = new Set<number>();
    const allGifts = [...state.recentGifts, ...builtinCatalog].filter((g) => !seen.has(g.id) && (seen.add(g.id), true));
    function renderGiftList(filter: string): void {
      giftList.replaceChildren();
      const list = allGifts.filter((g) => g.name.includes(filter) || String(g.id).includes(filter)).slice(0, 50);
      if (list.length === 0) giftList.append(emptyState('没有匹配的礼物'));
      for (const g of list) {
        const row = el('div', { class: 'list-item' });
        const img = el('img', { class: 'gift-img' }) as HTMLImageElement;
        img.src = g.imgBasic || 'data:image/gif;base64,R0lGODlhAQABAIAAAAAAAP///yH5BAEAAAAALAAAAAABAAEAAAIBRAA7';
        const configured = state.rules.some((r) => r.giftId === g.id);
        row.append(img,
          el('div', { class: 'grow' }, [
            el('div', { class: 'name', text: g.name }),
            el('div', { class: 'sub', text: `ID ${g.id}` }),
          ]),
          configured ? el('span', { class: 'badge', text: '已设置' }) : el('span', { class: 'badge unset', text: '未配置' }));
        row.onclick = () => openRuleEditor(g.id, g.name, g.imgBasic);
        giftList.append(row);
      }
    }
    renderGiftList('');
    search.oninput = () => renderGiftList(search.value.trim());
    addCard.append(search, giftList);
    content.append(addCard);

    if (state.rules.length > 0) {
      for (const rule of state.rules) {
        const gi = findGift(state, rule.giftId);
        const item = el('div', { class: 'list-item' });
        const img = el('img', { class: 'gift-img' }) as HTMLImageElement;
        img.src = gi?.imgBasic || 'data:image/gif;base64,R0lGODlhAQABAIAAAAAAAP///yH5BAEAAAAALAAAAAABAAEAAAIBRAA7';
        const del = el('button', { class: 'btn danger', text: '删除' });
        del.onclick = () => {
          state.rules = state.rules.filter((r) => r.id !== rule.id);
          save();
          render();
        };
        item.append(img,
          el('div', { class: 'grow' }, [
            el('div', { class: 'name', text: `${gi?.name ?? rule.giftId} → ${rule.attributeName}` }),
            el('div', { class: 'sub', text: `${rule.formula}` }),
          ]),
          del);
        content.append(item);
      }
    } else {
      content.append(emptyState('先搜索一个观众会送的礼物。'));
    }
  }

  function openRuleEditor(giftId: number, giftName: string, _giftImg: string): void {
    const overlay = el('div', { class: 'overlay' });
    const card = el('div', { class: 'card' });
    card.append(el('h3', { text: `配置规则：${giftName}` }));
    const attrSelect = el('select', { class: 'field-input' }) as HTMLSelectElement;
    attrSelect.innerHTML = state.attributes.map((a) => `<option>${a.name}</option>`).join('');
    const formulaInput = inputField('公式', 'price/1000*60');
    formulaInput.classList.add('formula');
    formulaInput.placeholder = '例如 price/1000*60（点下方示例可自动填入）';
    const preview = el('div', { class: 'preview' });
    const minInput = inputField('最低门槛 price≥（可留空）', '');
    const capInput = inputField('上限封顶（可留空）', '');
    const limitInput = inputField('当日限次（可留空）', '');
    function updatePreview(): void {
      const formula = formulaInput.value.trim();
      preview.replaceChildren();
      try {
        const env = buildPreviewEnv(state);
        const vars = collectVars(formula);
        const missing = vars.filter((v) => v !== 'price' && v !== 'count' && !state.attributes.some((a) => a.name === v));
        const result = evalFormula(formula, env);
        const target = state.attributes[attrSelect.selectedIndex];
        preview.append(el('div', { text: `示例：单价1000、数量1 时，结果为：` }),
          el('span', { class: 'result', text: target ? formatValue(result, target) : String(result) }));
        if (missing.length > 0) preview.append(el('div', { class: 'hint error', text: `未定义变量：${missing.join('、')}` }));
        if (vars.length === 0) preview.append(el('div', { class: 'hint', text: '提示：可用变量 price（礼物单价）、count（数量），以及属性名。点上方示例查看写法' }));
      } catch (e) {
        const msg = e instanceof FormulaError ? e.message : String(e);
        preview.append(el('div', { class: 'error', text: msg }));
      }
    }
    formulaInput.oninput = updatePreview;
    attrSelect.onchange = updatePreview;
    updatePreview();

    const tut = el('details', { class: 'tutorial' });
    tut.append(el('summary', { text: '不会写公式？看示例' }));
    const varsTable = el('table', {}, [
      el('tr', {}, [el('th', { text: '变量' }), el('th', { text: '代表什么' })]),
      el('tr', {}, [el('td', {}, [el('code', { text: 'price' })]), el('td', { text: '这个礼物的单价（数值越大礼物越贵）' })]),
      el('tr', {}, [el('td', {}, [el('code', { text: 'count' })]), el('td', { text: '观众一次送的数量' })]),
      ...state.attributes.map((a) => el('tr', {}, [el('td', {}, [el('code', { text: a.name })]), el('td', { text: `${a.name} 当前的值` })])),
    ]);
    const exTable = el('table', {}, [
      el('tr', {}, [el('th', { text: '函数' }), el('th', { text: '作用' })]),
      el('tr', {}, [el('td', {}, [el('code', { text: 'IF(条件, 是, 否)' })]), el('td', { text: '如果条件成立返回"是"，否则返回"否"' })]),
      el('tr', {}, [el('td', {}, [el('code', { text: 'RAND()' })]), el('td', { text: '0~1 之间的随机数' })]),
      el('tr', {}, [el('td', {}, [el('code', { text: 'RANDBETWEEN(最小, 最大)' })]), el('td', { text: '范围内的随机整数（如抽奖）' })]),
      el('tr', {}, [el('td', {}, [el('code', { text: 'MAX(1,2) / MIN(1,2)' })]), el('td', { text: '取最大 / 最小' })]),
    ]);
    const examples: [string, string][] = [
      ['每 1 元礼物加 60 秒', 'price/1000*60'],
      ['随机加 10~60 秒', 'RANDBETWEEN(10,60)'],
      ['满 100 元额外加 5 分钟', 'IF(price>=100000, 300, 0)'],
      ['按礼物数量加，每个 30 秒', 'count*30'],
      ['1 元 = 1 积分（整数）', 'ROUND(price/1000)'],
    ];
    const exRow = el('div', { class: 'examples' });
    for (const [label, formula] of examples) {
      const chip = el('button', { class: 'example-chip', text: `${label} → ${formula}` }) as HTMLButtonElement;
      chip.title = '点击填入公式框';
      chip.onclick = () => {
        formulaInput.value = formula;
        updatePreview();
      };
      exRow.append(chip);
    }
    tut.append(
      el('div', { style: 'margin-top:8px;font-weight:600;', text: '可用的变量（直接在公式里用）：' }),
      varsTable,
      el('div', { style: 'margin-top:8px;font-weight:600;', text: '常用函数：' }),
      exTable,
      el('div', { style: 'margin-top:8px;font-weight:600;', text: '点一下就能填入的示例：' }),
      exRow,
      el('div', { class: 'hint', style: 'margin-top:8px;', text: '提示：加班时间按秒计算，60 秒 = 1 分钟。填完上面会自动显示计算结果，不用自己算。' }),
    );
    const limits = el('details', { class: 'details-card' });
    limits.append(
      el('summary', { text: '可选限制' }),
      fieldControl(minInput),
      fieldControl(capInput),
      fieldControl(limitInput),
    );

    const saveBtn = el('button', { class: 'btn', text: '保存规则' });
    saveBtn.onclick = () => {
      const formula = formulaInput.value.trim();
      if (!formula) { toast('请填写公式', root); return; }
      try {
        evalFormula(formula, buildPreviewEnv(state));
      } catch {
        toast('公式有误，无法保存', root);
        return;
      }
      const attrName = state.attributes[attrSelect.selectedIndex]?.name;
      if (!attrName) { toast('请先创建属性', root); return; }
      const rule: GiftRule = {
        id: `r-${Date.now()}-${Math.random().toString(36).slice(2, 6)}`,
        giftId,
        attributeName: attrName,
        formula,
        minPrice: numOrUndef(minInput.value),
        cap: numOrUndef(capInput.value),
        dailyLimit: numOrUndef(limitInput.value),
      };
      state.rules.push(rule);
      save();
      overlay.remove();
      switchTo('obs');
      toast('规则已保存', root);
    };
    const cancelBtn = el('button', { class: 'btn ghost', text: '取消' });
    cancelBtn.onclick = () => overlay.remove();
    card.append(
      el('label', { class: 'field' }, [el('span', { class: 'field-label', text: '选择属性' }), attrSelect]),
      fieldControl(formulaInput),
      tut,
      preview,
      limits,
      el('div', { class: 'row gap' }, [saveBtn, cancelBtn]),
    );
    overlay.append(card);
    overlay.onclick = (e) => { if (e.target === overlay) overlay.remove(); };
    root.append(overlay);
  }

  function renderManualAdd(): void {
    content.append(
      el('h1', { class: 'wizard-main-title', text: '手动添加礼物' }),
      el('p', { class: 'wizard-subtitle', text: '知道礼物 ID 时，可以在观众送出前创建规则。' }),
    );
    const manualCard = el('div', { class: 'card manual-add-card' });
    manualCard.append(el('div', { class: 'sub', style: 'margin-bottom:12px;color:var(--text-dim);font-size:13px;', text: '一般用不到这里——观众送过的礼物会自动出现在礼物列表。' }));
    const gidInput = inputField('礼物 ID', '');
    const gnameInput = inputField('礼物名称（用于显示）', '');
    const giconInput = inputField('图标 URL（可选）', '');
    const addBtn = el('button', { class: 'btn', text: '添加到目录并建规则' });
    addBtn.onclick = () => {
      const gid = Number(gidInput.value.trim());
      if (!gid) { toast('请输入礼物 ID', root); return; }
      const name = gnameInput.value.trim() || `礼物${gid}`;
      upsertRecentGift(state, parseGift({ giftId: gid, giftName: name, gift_info: { img_basic: giconInput.value.trim() } }));
      save();
      openRuleEditor(gid, name, giconInput.value.trim());
    };
    manualCard.append(fieldControl(gidInput), fieldControl(gnameInput), fieldControl(giconInput), addBtn);
    content.append(manualCard);
  }

  function renderMoreSettings(): void {
    const panel = el('div', { class: 'secondary-panel' });
    panel.style.display = 'none';
    const toggle = el('button', { class: 'secondary-toggle', text: '更多设置' }) as HTMLButtonElement;
    toggle.onclick = () => {
      const showing = panel.style.display !== 'none';
      panel.style.display = showing ? 'none' : 'flex';
    };
    const statsButton = el('button', { class: 'btn ghost', text: '查看统计' }) as HTMLButtonElement;
    statsButton.onclick = () => switchTo('stats');
    const settingsButton = el('button', { class: 'btn ghost', text: '面板设置' }) as HTMLButtonElement;
    settingsButton.onclick = () => switchTo('settings');
    const manualButton = el('button', { class: 'btn ghost', text: '手动添加礼物' }) as HTMLButtonElement;
    manualButton.onclick = () => switchTo('manual');
    panel.append(statsButton, settingsButton, manualButton);
    content.append(toggle, panel);
  }

  function renderStats(): void {
    content.append(el('div', { class: 'section-title', text: '统计' }));
    guideCard('这里是今天收到礼物的汇总，以及每次礼物触发规则后属性的变动记录，方便你核对。', '这里能看到什么？');
    const day = state.stats[todayStr()];
    const card = el('div', { class: 'card' });
    if (day && Object.keys(day.giftTotals).length > 0) {
      for (const [gid, cnt] of Object.entries(day.giftTotals)) {
        const g = findGift(state, Number(gid));
        card.append(el('div', { class: 'list-item' }, [
          el('span', { text: `${g?.name ?? gid} x${cnt}` }),
        ]));
      }
    } else {
      card.append(emptyState('今天还没有礼物'));
    }
    content.append(card);
    const logCard = el('div', { class: 'card' });
    logCard.append(el('h3', { text: '属性变动日志' }));
    if (state.log.length === 0) logCard.append(emptyState('暂无变动记录'));
    for (const e of state.log) {
      logCard.append(el('div', { class: 'log-item' }, [
        el('span', { text: `${new Date(e.time * 1000).toLocaleString('zh-CN')} ` }),
        el('b', { text: `${e.uname} 送出 ${e.giftName} ` }),
        el('span', { text: `${e.attributeName} ${e.delta > 0 ? '+' : ''}${e.delta} → ${e.valueAfter}` }),
      ]));
    }
    content.append(logCard);
  }

  function renderSettings(): void {
    content.append(el('div', { class: 'section-title', text: '设置' }));
    guideCard('调整面板在直播画面里的样子，以及备份/迁移你的配置。', '设置里能做什么？');
    const card = el('div', { class: 'card' });
    const fontSize = inputField('字体大小（px）', String(state.settings.fontSize));
    fontSize.oninput = () => { state.settings.fontSize = Number(fontSize.value) || 48; save(); };
    const accent = inputField('强调色（十六进制）', state.settings.accentColor);
    accent.oninput = () => { state.settings.accentColor = accent.value; save(); };
    const align = el('select', { class: 'field-input' }) as HTMLSelectElement;
    align.innerHTML = `<option value="left">左对齐</option><option value="center">居中</option><option value="right">右对齐</option>`;
    align.value = state.settings.align;
    align.onchange = () => { state.settings.align = align.value as any; save(); };
    const showStats = el('input', { type: 'checkbox' }) as HTMLInputElement;
    showStats.checked = state.settings.showStats;
    showStats.onchange = () => { state.settings.showStats = showStats.checked; save(); };
    const showConn = el('input', { type: 'checkbox' }) as HTMLInputElement;
    showConn.checked = state.settings.showConnection;
    showConn.onchange = () => { state.settings.showConnection = showConn.checked; save(); };
    card.append(
      fieldControl(fontSize),
      fieldControl(accent),
      el('div', { class: 'field', children: [el('span', { class: 'field-label', text: '对齐' }), align] }),
      el('div', { class: 'field', children: [el('label', { text: '显示今日统计' }), showStats] }),
      el('div', { class: 'field', children: [el('label', { text: '显示连接状态' }), showConn] }),
    );
    content.append(card);

    const dataCard = el('div', { class: 'card' });
    dataCard.append(el('h3', { text: '数据管理' }));
    const exportBtn = el('button', { class: 'btn', text: '导出配置' });
    exportBtn.onclick = () => {
      const blob = new Blob([JSON.stringify(state, null, 2)], { type: 'application/json' });
      const url = URL.createObjectURL(blob);
      const a = el('a', { href: url, download: `gift-panel-config-${new Date().toISOString().slice(0, 10)}.json` }) as HTMLAnchorElement;
      a.click();
      URL.revokeObjectURL(url);
    };
    const importInput = el('input', { type: 'file', accept: '.json' }) as HTMLInputElement;
    importInput.style.display = 'none';
    importInput.onchange = () => {
      const file = importInput.files?.[0];
      if (!file) return;
      file.text().then((text) => {
        let parsed: Partial<AppState>;
        try {
          parsed = JSON.parse(text) as Partial<AppState>;
        } catch {
          toast('文件解析失败', root);
          return;
        }
        if (parsed === null || typeof parsed !== 'object' || Array.isArray(parsed)
          || (parsed.attributes !== undefined && !Array.isArray(parsed.attributes))
          || (parsed.rules !== undefined && !Array.isArray(parsed.rules))
          || (parsed.settings !== undefined && (typeof parsed.settings !== 'object' || parsed.settings === null))) {
          toast('配置文件格式不正确', root);
          return;
        }
        state = {
          ...state,
          ...parsed,
          settings: { ...state.settings, ...(parsed.settings ?? {}) },
          attributes: Array.isArray(parsed.attributes) ? parsed.attributes : state.attributes,
          rules: Array.isArray(parsed.rules) ? parsed.rules : state.rules,
        };
        save();
        render();
        toast('配置已导入', root);
      });
    };
    const importBtn = el('button', { class: 'btn ghost', text: '导入配置' });
    importBtn.onclick = () => importInput.click();
    const resetBtn = el('button', { class: 'btn danger', text: '恢复默认' });
    resetBtn.onclick = () => {
      if (confirm('确定恢复默认设置？当前配置将被清除。')) {
        localStorage.removeItem(STORAGE_KEY);
        location.reload();
      }
    };
    dataCard.append(exportBtn, importBtn, importInput, resetBtn);
    content.append(dataCard);
  }

  render();
}
