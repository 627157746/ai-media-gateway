// DOM helpers shared by all pages. All user/server data goes through
// textContent — never HTML injection (CSP + XSS).

export function h(tag, attrs, ...children) {
  const el = document.createElement(tag);
  if (attrs) {
    for (const [k, v] of Object.entries(attrs)) {
      if (v === null || v === undefined || v === false) continue;
      if (k === 'class') el.className = v;
      else if (k === 'text') el.textContent = v;
      else if (k.startsWith('on') && typeof v === 'function') el.addEventListener(k.slice(2).toLowerCase(), v);
      else if (k === 'value') el.value = v;
      else if (k === 'checked') el.checked = !!v;
      else if (k === 'disabled') el.disabled = !!v;
      else el.setAttribute(k, v === true ? '' : String(v));
    }
  }
  for (const c of children.flat(Infinity)) {
    if (c === null || c === undefined || c === false) continue;
    el.append(c.nodeType ? c : document.createTextNode(String(c)));
  }
  return el;
}

export function field(labelText, input, hint) {
  const f = h('div', { class: 'field' }, h('label', { text: labelText }), input);
  if (hint) f.append(h('div', { class: 'hint', text: hint }));
  return f;
}

export function pill(text, kind) { return h('span', { class: `pill ${kind || 'dim'}`, text }); }

export function switchEl(checked, onChange, text) {
  const hasText = text !== undefined && text !== null;
  // With text, the outer label owns the click; nested labels would double-toggle.
  const sw = h(hasText ? 'span' : 'label', { class: 'switch' },
    h('input', { type: 'checkbox', checked, onchange: e => onChange(e.target.checked) }),
    h('i'));
  return hasText ? h('label', { class: 'switch-wrap' }, sw, h('span', { text })) : sw;
}

// ---- sheet (right side panel) ----

let sheetCount = 0;
export function isSheetOpen() { return sheetCount > 0; }

export function openSheet(title, content) {
  sheetCount++;
  const close = () => { sheetCount--; wrap.remove(); };
  const wrap = h('div', { class: 'overlay', onclick: e => { if (e.target === wrap) close(); } },
    h('div', { class: 'sheet' }, h('h2', { text: title }), content));
  wrap.addEventListener('keydown', e => { if (e.key === 'Escape') close(); });
  wrap.tabIndex = -1;
  document.body.append(wrap);
  const first = wrap.querySelector('input,select,textarea,button');
  if (first) first.focus();
  return close;
}

export function sheetButtons(submitText, onSubmit, close, submitClass = 'primary') {
  return h('div', { class: 'row' },
    h('button', { class: 'btn', text: '取消', onclick: close }),
    h('button', { class: `btn ${submitClass}`, text: submitText, onclick: onSubmit }));
}

// ---- confirm dialog ----

export function confirmDialog(message, okText = '删除') {
  return new Promise(resolve => {
    const done = v => { wrap.remove(); resolve(v); };
    const wrap = h('div', { class: 'dialog-wrap', onclick: e => { if (e.target === wrap) done(false); } },
      h('div', { class: 'dialog' },
        h('h3', { text: '确认操作' }),
        h('div', { text: message }),
        h('div', { class: 'row' },
          h('button', { class: 'btn', text: '取消', onclick: () => done(false) }),
          h('button', { class: 'btn danger', text: okText, onclick: () => done(true) }))));
    document.body.append(wrap);
    wrap.querySelector('button').focus();
  });
}

export function modalDialog(title, content) {
  const close = () => wrap.remove();
  const wrap = h('div', { class: 'dialog-wrap' },
    h('div', { class: 'dialog' }, h('h3', { text: title }), content));
  document.body.append(wrap);
  return close;
}

// ---- toast ----

let toastBox;
export function toast(msg, kind = '') {
  if (!toastBox) { toastBox = h('div', { class: 'toasts' }); document.body.append(toastBox); }
  const t = h('div', { class: `toast ${kind}`, text: msg });
  toastBox.append(t);
  setTimeout(() => t.remove(), kind === 'bad' ? 6000 : 3000);
}

// ---- misc ----

export function relTime(iso) {
  if (!iso) return '—';
  const d = new Date(iso);
  if (isNaN(d)) return iso;
  const s = Math.round((Date.now() - d.getTime()) / 1000);
  if (s < 0) return fmtTime(iso);
  if (s < 60) return `${s} 秒前`;
  if (s < 3600) return `${Math.floor(s / 60)} 分钟前`;
  if (s < 86400) return `${Math.floor(s / 3600)} 小时前`;
  return `${Math.floor(s / 86400)} 天前`;
}

export function fmtTime(iso) {
  if (!iso) return '—';
  const d = new Date(iso);
  if (isNaN(d)) return iso;
  const p = n => String(n).padStart(2, '0');
  return `${d.getFullYear()}-${p(d.getMonth() + 1)}-${p(d.getDate())} ${p(d.getHours())}:${p(d.getMinutes())}:${p(d.getSeconds())}`;
}

export async function copyText(text, btn) {
  try {
    await navigator.clipboard.writeText(text);
    toast('已复制到剪贴板', 'ok');
  } catch {
    const ta = h('textarea', { value: text });
    document.body.append(ta); ta.select();
    document.execCommand('copy'); ta.remove();
    toast('已复制', 'ok');
  }
}

export function errMsg(e) { return e && e.message ? e.message : String(e); }

export function parseJSONField(text, label) {
  const t = (text || '').trim();
  if (!t) return { ok: true, value: undefined };
  try {
    const v = JSON.parse(t);
    if (typeof v !== 'object' || v === null || Array.isArray(v)) {
      return { ok: false, error: `${label}必须是 JSON 对象` };
    }
    return { ok: true, value: v };
  } catch (e) {
    return { ok: false, error: `${label}JSON 解析失败: ${e.message}` };
  }
}

// ---- structured options editors ----

// mergeOpts returns a new object: base plus patch. A patch value of
// undefined / '' / empty object / empty array deletes the key, so
// clearing a control removes the stored option; keys the UI does not
// know about survive the merge.
export function mergeOpts(base, patch) {
  const out = Object.assign({}, base || {});
  for (const [k, v] of Object.entries(patch || {})) {
    if (v === undefined || v === '' || (v !== null && typeof v === 'object' && !Object.keys(v).length)) {
      delete out[k];
    } else {
      out[k] = v;
    }
  }
  return out;
}

// Collapsible section built on native <details> (CSP-safe).
export function more(title, ...children) {
  return h('details', { class: 'more' }, h('summary', { text: title }), children);
}

// kvRows edits a string map. With keyOptions the key column is a
// <select>, otherwise a text input. Rows with an empty key are skipped
// in value(); values are kept as strings (a blank value is preserved —
// for fal field mapping it means "omit that field").
export function kvRows({ rows, keyOptions, keyPlaceholder, valPlaceholder } = {}) {
  const box = h('div');
  const addRow = (k, v) => {
    let keyEl;
    if (keyOptions) {
      const opts = keyOptions.includes(k) || !k ? keyOptions : [...keyOptions, k];
      keyEl = h('select', { class: 'kv-key' },
        h('option', { value: '', text: keyPlaceholder || '选择…' }),
        opts.map(o => h('option', { value: o, text: o })));
      if (k) keyEl.value = k;
    } else {
      keyEl = h('input', { type: 'text', class: 'kv-key', value: k || '', placeholder: keyPlaceholder || '' });
    }
    const valEl = h('input', {
      type: 'text', class: 'kv-val',
      value: v == null ? '' : (typeof v === 'object' ? JSON.stringify(v) : String(v)),
      placeholder: valPlaceholder || '',
    });
    const row = h('div', { class: 'kv-row' }, keyEl, valEl,
      h('button', { class: 'btn sm', text: '删除', onclick: () => row.remove() }));
    box.append(row);
  };
  for (const [k, v] of Array.isArray(rows) ? rows : Object.entries(rows || {})) addRow(k, v);
  const el = h('div', null, box,
    h('button', { class: 'btn sm mt4', text: '＋ 添加一行', onclick: () => addRow('', '') }));
  const value = () => {
    const out = {};
    for (const row of box.querySelectorAll('.kv-row')) {
      const k = row.querySelector('.kv-key').value.trim();
      if (k) out[k] = row.querySelector('.kv-val').value;
    }
    return out;
  };
  return { el, value };
}

// voiceEditor edits default_voice + voices ({alias: vendor voice id}).
// value() returns { default_voice, voices } — feed it to mergeOpts so
// empty values delete their keys.
export function voiceEditor(opts) {
  const o = opts || {};
  const defIn = h('input', { type: 'text', value: o.default_voice || '', placeholder: '别名或上游音色 ID' });
  const rows = kvRows({ rows: o.voices || {}, keyPlaceholder: '别名', valPlaceholder: '上游音色 ID' });
  const el = h('div', null,
    field('默认音色', defIn),
    h('div', null, h('label', { class: 'lbl', text: '音色别名' }), rows.el),
    h('div', { class: 'muted fs12', text: '查找顺序：账号 → 模型 → 平台' }));
  const voices = () => Object.fromEntries(Object.entries(rows.value()).map(([k, v]) => [k, v.trim()]).filter(([, v]) => v));
  return { el, value: () => ({ default_voice: defIn.value.trim(), voices: voices() }) };
}

// ---- proxy pool (shared cache) ----

let proxiesPromise = null;
export function proxies() {
  if (!proxiesPromise) {
    proxiesPromise = import('./api.js').then(({ admin }) =>
      admin('/admin/proxies').then(r => r.proxies || []));
  }
  return proxiesPromise;
}
export function invalidateProxies() { proxiesPromise = null; }
const proxyByID = (list, id) => list.find(p => p.id === id);

// proxyLabel renders a proxy_url value for display: pool name for
// references, 直连, or the masked URL.
export function proxyLabel(v, list) {
  if (!v) return '';
  if (v === 'direct') return '直连';
  if (v.startsWith('proxy:')) {
    const p = proxyByID(list || [], v.slice(6));
    return p ? p.name : `已删除的代理（${v.slice(6)}）`;
  }
  return maskProxy(v);
}

// proxyField is a 3-level proxy picker: inherit / direct / a proxy-pool
// entry / custom URL. Returns { el, value(), set() } — value() is "",
// "direct", "proxy:<id>" or the trimmed URL.
export function proxyField(value, inheritLabel) {
  const sel = h('select', {},
    h('option', { value: '', text: inheritLabel }),
    h('option', { value: 'direct', text: '直连（不走代理）' }));
  const urlIn = h('input', { type: 'text', placeholder: 'http://127.0.0.1:7890 或 socks5://user:pass@host:1080' });
  const urlRow = h('div', { class: 'mt4' }, urlIn);
  const result = h('div', { class: 'hint' });
  const testBtn = h('button', { class: 'btn sm', text: '测试', type: 'button' });
  const el = h('div', null,
    sel, urlRow,
    h('div', { class: 'mt4' },
      testBtn, ' ',
      h('a', { href: '#/settings', class: 'muted fs12', text: '管理代理池' }),
      ' ', h('span', { class: 'muted fs12', text: '支持 http / https / socks5 / socks5h' })),
    result);

  // pool entries are appended once loaded; a dangling stored reference gets
  // a placeholder option so the value stays visible.
  const addDanglingOption = v => {
    if (v.startsWith('proxy:') && !sel.querySelector(`option[value="${v}"]`)) {
      sel.insertBefore(
        h('option', { value: v, text: `已删除的代理（${v.slice(6)}）` }),
        sel.querySelector('option[value="custom"]'));
    }
  };
  let pending = value || '';
  proxies().then(list => {
    for (const p of list) {
      sel.insertBefore(
        h('option', { value: `proxy:${p.id}`, text: `${p.name}（${p.url_masked}）` }),
        sel.querySelector('option[value="custom"]'));
    }
    addDanglingOption(pending);
    sel.value = pending === '' || pending === 'direct'
      ? pending
      : (pending.startsWith('proxy:') ? pending : 'custom');
  }).catch(() => {});
  sel.append(h('option', { value: 'custom', text: '自定义代理' }));

  const setValue = v => {
    pending = v;
    if (v === '' || v === 'direct') { sel.value = v; urlIn.value = ''; }
    else if (v.startsWith('proxy:')) { addDanglingOption(v); sel.value = v; urlIn.value = ''; }
    else { sel.value = 'custom'; urlIn.value = v; }
    urlRow.hidden = sel.value !== 'custom';
  };
  const value_ = () => sel.value === 'custom' ? urlIn.value.trim() : sel.value;
  sel.onchange = () => { urlRow.hidden = sel.value !== 'custom'; };
  testBtn.onclick = async () => {
    result.textContent = '测试中…';
    result.className = 'hint';
    try {
      const { admin } = await import('./api.js');
      const r = await admin('/admin/proxy/test', { method: 'POST', json: { proxy_url: value_() } });
      if (r.ok) {
        result.textContent = `可连通（HTTP ${r.status}，${r.latency_ms} ms）`;
      } else {
        result.textContent = `不可达：${r.error}`;
        result.className = 'hint warn-text';
      }
    } catch (e) {
      result.textContent = errMsg(e);
      result.className = 'hint warn-text';
    }
  };
  setValue(value || '');
  return { el, value: value_, set: setValue };
}

// maskProxy hides the userinfo password for display (same rule as
// netproxy.Mask on the backend).
export function maskProxy(u) {
  if (!u || u === 'direct') return u;
  try {
    const p = new URL(u);
    if (p.password) p.password = '***';
    return p.href;
  } catch {
    return u;
  }
}
