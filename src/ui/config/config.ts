import { AppState, GiftRule, STORAGE_KEY } from '../../types';
import { loadState, saveState } from '../../storage';
import { el, inputField, toast } from '../common';
import { builtinCatalog, findGift, upsertRecentGift } from '../../gifts/catalog';
import { evalFormula, collectVars, FormulaError } from '../../formula';
import { formatValue, todayStr } from '../../format';
import { parseGift } from '../../bilibili/messages';
import { DanmakuClient, ConnState } from '../../bilibili/client';

export function mountConfig(root: HTMLElement): void {
  let state = loadState();
  let current = 'room';
  const content = el('div', { class: 'content' });
  const sidebar = el('div', { class: 'sidebar' });
  sidebar.append(el('div', { class: 'sidebar-title', text: '直播礼物面板' }));
  const nav = [
    ['room', '房间设置'],
    ['attributes', '属性管理'],
    ['rules', '礼物规则'],
    ['stats', '统计'],
    ['settings', '设置'],
  ] as const;
  const navItems: Record<string, HTMLButtonElement> = {};
  for (const [key, label] of nav) {
    const item = el('button', { class: 'nav-item', text: label }) as HTMLButtonElement;
    item.onclick = () => switchTo(key);
    navItems[key] = item;
    sidebar.append(item);
  }
  root.append(sidebar, content);

  function switchTo(key: string): void {
    current = key;
    for (const [k, item] of Object.entries(navItems)) item.classList.toggle('active', k === key);
    render();
  }

  function render(): void {
    content.replaceChildren();
    if (current === 'room') renderRoom();
    else if (current === 'attributes') renderAttributes();
    else if (current === 'rules') renderRules();
    else if (current === 'stats') renderStats();
    else renderSettings();
  }

  function save(): void {
    saveState(state);
  }

  function renderRoom(): void {
    content.append(el('div', { class: 'section-title', text: '房间设置' }));
    const card = el('div', { class: 'card' });
    const roomInput = inputField('直播间房间号（live.bilibili.com/xxxx 中的数字）', state.roomId);
    roomInput.placeholder = '例如 2145';
    const row = el('div', { class: 'row gap' });
    const statusText = el('span', { text: '未连接' });
    const connectBtn = el('button', { class: 'btn', text: '测试连接' }) as HTMLButtonElement;
    let client: DanmakuClient | null = null;
    connectBtn.onclick = () => {
      const roomId = roomInput.value.trim();
      if (!roomId) { toast('请输入房间号', root); return; }
      state.roomId = roomId;
      save();
      client?.stop();
      client = new DanmakuClient({
        roomId: Number(roomId),
        onState: (s: ConnState) => {
          statusText.textContent = s === 'connected' ? '已连接' : s === 'connecting' ? '连接中…' : s === 'reconnecting' ? '重连中…' : '连接失败';
        },
        onGift: (ev) => {
          upsertRecentGift(state, ev);
          save();
        },
      });
      client.start();
    };
    row.append(connectBtn, statusText);
    card.append(roomInput, row);
    content.append(card);
  }

  function renderAttributes(): void {
    content.append(el('div', { class: 'section-title', text: '属性管理' }));
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
        nameInput,
        el('div', { class: 'field', children: [el('span', { class: 'field-label', text: '单位' }), unitSelect] }),
        el('div', { class: 'field', children: [el('span', { class: 'field-label', text: '显示格式' }), fmtSelect] }),
        valueInput, preview,
        el('div', { class: 'row' }, [resetBtn, delBtn]),
      );
      content.append(card);
    }
  }

  function renderRules(): void {
    content.append(el('div', { class: 'section-title', text: '礼物规则' }));
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
      content.append(el('div', { class: 'empty', text: '还没有规则。在下方搜索礼物并创建规则，或等待观众送出礼物后自动捕获。' }));
    }

    const addCard = el('div', { class: 'card' });
    addCard.append(el('h3', { text: '新增规则' }));
    const search = el('input', { class: 'field-input' }) as HTMLInputElement;
    search.placeholder = '搜索礼物名称…';
    const giftList = el('div', {});
    const allGifts = [...state.recentGifts, ...builtinCatalog];
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
    manualCard.append(gidInput, gnameInput, giconInput, addBtn);
    content.append(manualCard);
  }

  function openRuleEditor(giftId: number, giftName: string, _giftImg: string): void {
    const overlay = el('div', { class: 'overlay' });
    const card = el('div', { class: 'card' });
    card.append(el('h3', { text: `配置规则：${giftName}` }));
    const attrSelect = el('select', { class: 'field-input' }) as HTMLSelectElement;
    attrSelect.innerHTML = state.attributes.map((a) => `<option>${a.name}</option>`).join('');
    const formulaInput = inputField('公式（price=单价  count=数量）', 'price/1000*60');
    formulaInput.classList.add('formula');
    formulaInput.placeholder = '例如 price/1000*60 或 RANDBETWEEN(10,60)';
    const preview = el('div', { class: 'preview' });
    const minInput = inputField('最低门槛 price≥（可留空）', '');
    const capInput = inputField('上限封顶（可留空）', '');
    const limitInput = inputField('当日限次（可留空）', '');
    function updatePreview(): void {
      const formula = formulaInput.value.trim();
      preview.replaceChildren();
      try {
        const env: Record<string, number> = { price: 1000, count: 1 };
        for (const a of state.attributes) env[a.name] = a.value;
        const vars = collectVars(formula);
        const missing = vars.filter((v) => v !== 'price' && v !== 'count' && !state.attributes.some((a) => a.name === v));
        const result = evalFormula(formula, env);
        const target = state.attributes[attrSelect.selectedIndex];
        preview.append(el('div', { text: `示例：单价1000、数量1 时，结果为：` }),
          el('span', { class: 'result', text: target ? formatValue(result, target) : String(result) }));
        if (missing.length > 0) preview.append(el('div', { class: 'hint error', text: `未定义变量：${missing.join('、')}` }));
        if (vars.length === 0) preview.append(el('div', { class: 'hint', text: '提示：可使用变量 price（礼物单价）、count（数量），以及属性名' }));
      } catch (e) {
        const msg = e instanceof FormulaError ? e.message : String(e);
        preview.append(el('div', { class: 'error', text: msg }));
      }
    }
    formulaInput.oninput = updatePreview;
    attrSelect.onchange = updatePreview;
    updatePreview();
    const saveBtn = el('button', { class: 'btn', text: '保存规则' });
    saveBtn.onclick = () => {
      const formula = formulaInput.value.trim();
      if (!formula) { toast('请填写公式', root); return; }
      try {
        evalFormula(formula, { price: 1000, count: 1 });
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
        minPrice: minInput.value ? Number(minInput.value) : undefined,
        cap: capInput.value ? Number(capInput.value) : undefined,
        dailyLimit: limitInput.value ? Number(limitInput.value) : undefined,
      };
      state.rules.push(rule);
      save();
      overlay.remove();
      render();
      toast('规则已保存', root);
    };
    const cancelBtn = el('button', { class: 'btn ghost', text: '取消' });
    cancelBtn.onclick = () => overlay.remove();
    card.append(attrSelect, formulaInput, preview, minInput, capInput, limitInput, el('div', { class: 'row gap' }, [saveBtn, cancelBtn]));
    overlay.append(card);
    overlay.onclick = (e) => { if (e.target === overlay) overlay.remove(); };
    root.append(overlay);
  }

  function renderStats(): void {
    content.append(el('div', { class: 'section-title', text: '今日统计' }));
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
      fontSize,
      accent,
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
        try {
          const parsed = JSON.parse(text) as AppState;
          state = { ...state, ...parsed };
          save();
          render();
          toast('配置已导入', root);
        } catch {
          toast('文件解析失败', root);
        }
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
