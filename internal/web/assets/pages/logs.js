import { admin } from '../api.js';
import { h, switchEl, toast, errMsg, fmtTime } from '../ui.js';

const RANGES = [['3600000', '近 1 小时'], ['86400000', '近 24 小时'], ['604800000', '近 7 天'], ['', '全部']];

function statusClass(code) {
  if (code >= 500) return 'bad';
  if (code >= 400) return 'warn';
  return 'ok';
}

export function mountLogs(root) {
  const accountSel = h('select', { class: 'w220' }, h('option', { value: '', text: '全部账号' }));
  const rangeSel = h('select', {}, RANGES.map(([v, t]) => h('option', { value: v, text: t })));
  const limitSel = h('select', {},
    [50, 100, 500].map(n => h('option', { value: String(n), text: `${n} 条` })));
  rangeSel.value = '86400000';
  const autoRef = { on: false };
  const autoSw = switchEl(false, v => { autoRef.on = v; });
  const listEl = h('div');
  root.append(
    h('div', { class: 'toolbar' },
      h('span', { class: 'muted', text: '账号' }), accountSel,
      rangeSel, limitSel,
      h('label', { class: 'check' }, autoSw, ' 自动刷新'),
      h('button', { class: 'btn sm', text: '刷新', onclick: () => load() })),
    listEl);

  async function load() {
    try {
      const q = new URLSearchParams();
      if (accountSel.value) q.set('account', accountSel.value);
      if (rangeSel.value) q.set('since', String(Date.now() - parseInt(rangeSel.value, 10)));
      q.set('limit', limitSel.value);
      const data = await admin(`/admin/logs?${q}`);
      render(listEl, data.logs || []);
    } catch (e) {
      listEl.textContent = '';
      listEl.append(h('div', { class: 'err-text', text: `加载失败: ${errMsg(e)}` }));
    }
  }

  async function loadAccounts() {
    try {
      const data = await admin('/admin/status');
      const seen = new Set();
      for (const p of data.providers || []) {
        for (const a of p.accounts || []) {
          if (seen.has(a.id)) continue;
          seen.add(a.id);
          accountSel.append(h('option', { value: a.id, text: `${p.id} / ${a.name || a.id}` }));
        }
      }
    } catch (e) { toast(errMsg(e), 'bad'); }
  }

  accountSel.onchange = load;
  rangeSel.onchange = load;
  limitSel.onchange = load;
  loadAccounts();
  load();
  const timer = setInterval(() => { if (autoRef.on) load(); }, 5000);
  return () => clearInterval(timer);
}

function render(listEl, logs) {
  listEl.textContent = '';
  const table = h('table', null,
    h('thead', null, h('tr', null,
      ['时间', '接口', '模型', '供应商', '账号', '尝试', '状态', '耗时', '错误'].map(t => h('th', { text: t })))),
    h('tbody'));
  for (const l of logs) {
    table.lastChild.append(h('tr', null,
      h('td', { class: 'muted mono', text: fmtTime(l.at) }),
      h('td', { class: 'mono', text: l.endpoint }),
      h('td', { class: 'mono', text: l.model_id || '—' }),
      h('td', { text: l.provider_id || '—' }),
      h('td', { class: 'mono muted', text: l.account_id || '—' }),
      h('td', null, l.attempts > 1
        ? h('span', { class: 'pill warn', text: `${l.attempts} 次` , title: '发生故障切换' })
        : String(l.attempts)),
      h('td', null, h('span', { class: `status-num ${statusClass(l.status)}`, text: String(l.status) })),
      h('td', { class: 'muted', text: `${l.latency_ms} ms` }),
      h('td', { class: 'err-text cell-err', title: l.error || '', text: l.error || '' })));
  }
  if (!logs.length) table.lastChild.append(h('tr', null, h('td', { colspan: '9' }, h('div', { class: 'empty', text: '暂无日志' }))));
  listEl.append(h('div', { class: 'table-wrap' }, table));
}
