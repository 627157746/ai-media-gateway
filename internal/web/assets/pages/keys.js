import { admin, saveGwKey } from '../api.js';
import { h, openSheet, sheetButtons, confirmDialog, modalDialog, toast, errMsg, relTime, fmtTime, copyText, field } from '../ui.js';

export function mountKeys(root) {
  const listEl = h('div');
  root.append(listEl);

  async function load() {
    try {
      const data = await admin('/admin/keys');
      render(listEl, data.keys || [], load);
    } catch (e) {
      listEl.textContent = '';
      listEl.append(h('div', { class: 'err-text', text: `加载失败: ${errMsg(e)}` }));
    }
  }
  load();
  return null;
}

function render(listEl, keys, reload) {
  listEl.textContent = '';
  const table = h('table', null,
    h('thead', null, h('tr', null, ['名称', 'ID', '创建时间', '最近使用', '操作'].map(t => h('th', { text: t })))),
    h('tbody'));
  for (const k of keys) {
    table.lastChild.append(h('tr', null,
      h('td', { text: k.name || '—' }),
      h('td', { class: 'mono muted', text: k.id }),
      h('td', { class: 'muted mono', text: fmtTime(k.created_at) }),
      h('td', { class: 'muted', text: k.last_used_at ? relTime(k.last_used_at) : '从未使用' }),
      h('td', null,
        h('button', {
          class: 'btn sm danger', text: '删除', onclick: async () => {
            if (!await confirmDialog(`删除密钥 "${k.name || k.id}"？使用该密钥的客户端将立即失效。`)) return;
            try { await admin(`/admin/keys/${k.id}`, { method: 'DELETE' }); toast('已删除', 'ok'); reload(); }
            catch (e) { toast(errMsg(e), 'bad'); }
          },
        }))));
  }
  if (!keys.length) table.lastChild.append(h('tr', null, h('td', { colspan: '5' }, h('div', { class: 'empty', text: '还没有密钥' }))));
  listEl.append(
    h('div', { class: 'table-wrap' }, table),
    h('button', { class: 'btn text add-btn', text: '＋ 创建密钥', onclick: () => createSheet(reload) }));
}

function createSheet(reload) {
  const nameIn = h('input', { type: 'text', placeholder: '例如 canvas' });
  const err = h('div', { class: 'err-text' });
  const close = openSheet('创建 API Key',
    h('div', null,
      field('名称', nameIn),
      err,
      sheetButtons('创建', async () => {
        err.textContent = '';
        try {
          const k = await admin('/admin/keys', { method: 'POST', json: { name: nameIn.value.trim() } });
          close();
          showKeyOnce(k, reload);
        } catch (e) { err.textContent = errMsg(e); }
      }, close)));
}

function showKeyOnce(k, reload) {
  const useBtn = h('button', {
    class: 'btn', text: '用于试用',
    onclick: () => { saveGwKey(k.key); toast('已保存为试用密钥', 'ok'); },
  });
  modalDialog('密钥已创建 — 仅显示这一次，请立即保存',
    h('div', null,
      h('div', { class: 'muted', text: `${k.name || k.id}:` }),
      h('div', { class: 'key-show', text: k.key }),
      h('div', { class: 'row start' },
        h('button', { class: 'btn primary', text: '复制', onclick: () => copyText(k.key) }),
        useBtn,
        h('span', { class: 'muted fs12 self-c', text: '「用于试用」会把此密钥填入试用页面' }))));
  reload();
}
