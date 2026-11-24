// DOM helpers shared by all pages. All user/server data goes through
// textContent — never innerHTML (CSP + XSS).

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

export function switchEl(checked, onChange) {
  return h('label', { class: 'switch' },
    h('input', { type: 'checkbox', checked, onchange: e => onChange(e.target.checked) }),
    h('i'));
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
