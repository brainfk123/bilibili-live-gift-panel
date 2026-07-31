import { AppState, GiftRule, STORAGE_KEY } from '../../types';
import { loadState, saveState } from '../../storage';
import { el, fieldControl, inputField, toast } from '../common';
import { builtinCatalog, findGift, upsertRecentGift } from '../../gifts/catalog';
import { evalFormula, collectVars, FormulaError } from '../../formula';
import { formatValue, todayStr } from '../../format';
import { parseGift } from '../../bilibili/messages';
import { DanmakuClient, ConnState } from '../../bilibili/client';
import { createBrandIcon } from '../brand';
import { getNextWizardStep, getWizardProgress, type WizardStep } from './wizard';

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
    el('span', { text: '简单三步，开始互动' }),
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
        el('span', { class: 'wizard-progress-number', text: String(index + 1) }),
        el('span', { class: 'wizard-progress-label', text: step.label }),
      );
      item.onclick = () => switchTo(step.key);
      progressNav.append(item);
    }
    content.append(progressNav);
  }

  function switchTo(key: string): void {
    client?.stop();
    client = null;
    connectionState = 'idle';
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
  }

  function save(): void {
    saveState(state);
  }

  function guideCard(text: string, bold?: string): void {
    const card = el('div', { class: 'guide-card' });
    if (bold) card.append(el('b', { text: bold }), el('span', { text: ' ' + text }));
    else card.append(el('span', { text }));
    content.append(card);
  }

  function hintToggle(label: string, panelText: string): void {
    const panel = el('div', { class: 'hint-panel', text: panelText });
    panel.style.display = 'none';
    const btn = el('button', { class: 'hint-toggle', text: label }) as HTMLButtonElement;
    btn.onclick = () => {
      const showing = panel.style.display !== 'none';
      panel.style.display = showing ? 'none' : 'block';
      btn.textContent = showing ? label : '收起 ▲';
    };
    content.append(btn, panel);
  }

  function renderOnboarding(): void {
    const roomDone = state.roomId.trim() !== '';
    const attrDone = state.attributes.length > 0;
    const ruleDone = state.rules.length > 0;
    const ready = roomDone && attrDone && ruleDone;
    const nextTarget = !roomDone ? 'room' : !attrDone ? 'attributes' : !ruleDone ? 'rules' : '';
    const steps: [string, string, boolean][] = [
      ['填写房间号', 'room', roomDone],
      ['创建属性（如加班时间）', 'attributes', attrDone],
      ['配置礼物规则', 'rules', ruleDone],
      ['在 OBS 中显示', 'room', ready],
    ];
    const box = el('div', { class: 'onboard' });
    box.append(
      el('div', { class: 'onboard-title', text: ready ? '🎉 配置完成，接下来放进 OBS' : '🎉 第一次使用？跟着做就好' }),
      el('div', { class: 'onboard-desc', text: ready ? '浏览器里的设置已经完成。把下面的地址添加到 OBS 浏览器源，直播时保持本程序窗口不要关闭。' : '不用懂技术，按顺序完成下面几步。每一步都有说明，点按钮可以直接跳到对应位置。' }),
    );
    const stepsRow = el('div', { class: 'steps' });
    for (const [label, target, done] of steps) {
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
    } else {
      const urlRow = el('div', { class: 'ready-url' });
      const urlInput = el('input', { class: 'field-input', value: `${location.origin}/?mode=display`, readOnly: true }) as HTMLInputElement;
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
      urlRow.append(urlInput, copyButton);
      box.append(
        el('div', { class: 'onboard-note', text: 'OBS 操作：来源 → 浏览器 → 粘贴上面的地址 → 勾选“关闭来源时刷新浏览器”不要勾选 → 设置宽度和高度 → 确定。' }),
        urlRow,
      );
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
    content.append(el('div', { class: 'section-title', text: '房间设置' }));
    guideCard('这是第一步：告诉插件去你的哪个直播间听礼物。填写后点「测试连接」，看到「已连接」就成功了。', '为什么需要这一步？');
    const card = el('div', { class: 'card' });
    const roomInput = inputField('直播间房间号', state.roomId);
    roomInput.placeholder = '例如 2145';
    hintToggle('怎么找到我的房间号？', '打开你的直播间网页（live.bilibili.com/后面那串数字），地址栏里的数字就是房间号。例如网址是 live.bilibili.com/2145，就填 2145。');
    const row = el('div', { class: 'row gap' });
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
    content.append(card);
    if (state.roomId && state.rules.length === 0) {
      content.append(el('div', { class: 'tip-banner' }, [
        el('b', { text: '下一步：' }),
        el('span', { text: '去「礼物规则」里配置，观众送礼物才能累加加班时间。点击左侧「礼物规则」。' }),
      ]));
    }
  }

  function renderAttributes(): void {
    content.append(el('div', { class: 'section-title', text: '属性管理' }));
    guideCard('「属性」就是你想让观众用礼物来累加的东西，最常用的是「加班时间」。比如观众送礼物就能给你的直播增加时长。', '属性是什么？');
    if (state.attributes.length === 0) {
      content.append(el('div', { class: 'empty', text: '还没有属性，点击下方「+ 新增属性」创建一个吧（推荐：加班时间）。' }));
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
      const preview = el('div', { class: 'preview' });
      const updatePreview = () => {
        preview.replaceChildren(el('span', { text: `当前值：` }), el('span', { class: 'result', text: formatValue(a.value, a) }));
      };
      updatePreview();
      const valueInput = inputField('手动调整当前值', String(a.value));
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
        el('div', { class: 'field', children: [el('span', { class: 'field-label', text: '单位' }), unitSelect] }),
        el('div', { class: 'field', children: [el('span', { class: 'field-label', text: '显示格式' }), fmtSelect] }),
        fieldControl(valueInput), preview,
        el('div', { class: 'row' }, [resetBtn, delBtn]),
      );
      content.append(card);
    }
  }

  function renderRules(): void {
    content.append(el('div', { class: 'section-title', text: '礼物规则' }));
    guideCard('规则就是「观众送哪个礼物 → 你的什么属性加多少」。例如：观众送「辣条」→ 加班时间 +1 分钟。', '规则是什么？');
    const ruleTutorial = el('details', { class: 'tutorial', open: state.rules.length === 0 });
    ruleTutorial.append(
      el('summary', { text: '📖 第一次配规则？照这 3 步做' }),
      el('div', { class: 'hint-panel', style: 'display:block;' }, [
        el('div', { text: '1. 在下面搜索礼物名称，例如“辣条”，点击礼物。' }),
        el('div', { text: '2. 选择要增加的属性，默认推荐“加班时间”。' }),
        el('div', { text: '3. 公式直接点示例即可，不确定时先用“每 1 元礼物加 60 秒”。' }),
        el('div', { class: 'hint', style: 'margin-top:6px;', text: '封顶、每日限次、最低门槛都是可选项，第一次使用可以先留空。' }),
      ]),
    );
    content.append(ruleTutorial);
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
      content.append(el('div', { class: 'empty', text: '还没有规则。' }));
      content.append(el('div', { class: 'tip-banner' }, [
        el('b', { text: '怎么开始？' }),
        el('span', { text: ' 在下方「搜索礼物名称」里找到你想用的礼物（比如辣条），点它 → 选择加哪个属性 → 填公式 → 保存。也可以等观众送出第一个礼物后，它会自动出现在列表里。' }),
      ]));
    }

    const addCard = el('div', { class: 'card' });
    addCard.append(el('h3', { text: '新增规则' }));
    const search = el('input', { class: 'field-input' }) as HTMLInputElement;
    search.placeholder = '搜索礼物名称…';
    const giftList = el('div', {});
    const seen = new Set<number>();
    const allGifts = [...state.recentGifts, ...builtinCatalog].filter((g) => !seen.has(g.id) && (seen.add(g.id), true));
    function renderGiftList(filter: string): void {
      giftList.replaceChildren();
      const list = allGifts.filter((g) => g.name.includes(filter) || String(g.id).includes(filter)).slice(0, 50);
      if (list.length === 0) giftList.append(el('div', { class: 'empty', text: '没有匹配的礼物' }));
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

    const manualCard = el('div', { class: 'card' });
    manualCard.append(el('h3', { text: '手动添加礼物' }));
    manualCard.append(el('div', { class: 'sub', style: 'margin-bottom:12px;color:var(--text-dim);font-size:13px;', text: '一般用不到这里——观众送过的礼物会自动出现在上方列表。只有当你已经知道礼物 ID、想在别人送之前就配好规则时才用。' }));
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

  function openRuleEditor(giftId: number, giftName: string, _giftImg: string): void {
    const overlay = el('div', { class: 'overlay' });
    const card = el('div', { class: 'card' });
    card.append(el('h3', { text: `配置规则：${giftName}` }));
    const attrSelect = el('select', { class: 'field-input' }) as HTMLSelectElement;
    attrSelect.innerHTML = state.attributes.map((a) => `<option>${a.name}</option>`).join('');
    const formulaInput = inputField('计算公式（加减的数值）', 'price/1000*60');
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
        if (vars.length === 0) preview.append(el('div', { class: 'hint', text: '提示：可用变量 price（礼物单价）、count（数量），以及属性名。点上方「公式怎么写」查看示例' }));
      } catch (e) {
        const msg = e instanceof FormulaError ? e.message : String(e);
        preview.append(el('div', { class: 'error', text: msg }));
      }
    }
    formulaInput.oninput = updatePreview;
    attrSelect.onchange = updatePreview;
    updatePreview();

    const tut = el('details', { class: 'tutorial' });
    tut.append(el('summary', { text: '📖 公式怎么写？（新手必看）' }));
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
    card.append(tut);

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
      render();
      toast('规则已保存', root);
    };
    const cancelBtn = el('button', { class: 'btn ghost', text: '取消' });
    cancelBtn.onclick = () => overlay.remove();
    card.append(
      el('label', { class: 'field' }, [el('span', { class: 'field-label', text: '增加哪个属性' }), attrSelect]),
      fieldControl(formulaInput),
      preview,
      fieldControl(minInput),
      fieldControl(capInput),
      fieldControl(limitInput),
      el('div', { class: 'row gap' }, [saveBtn, cancelBtn]),
    );
    overlay.append(card);
    overlay.onclick = (e) => { if (e.target === overlay) overlay.remove(); };
    root.append(overlay);
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
      card.append(el('div', { class: 'empty', text: '今天还没有礼物' }));
    }
    content.append(card);
    const logCard = el('div', { class: 'card' });
    logCard.append(el('h3', { text: '属性变动日志' }));
    if (state.log.length === 0) logCard.append(el('div', { class: 'empty', text: '暂无变动记录' }));
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
