import { admin } from '../api.js';
import { h, pill, errMsg, fmtTime } from '../ui.js';

const STATUSES = [['', '全部'], ['queued', '排队中'], ['in_progress', '生成中'], ['completed', '已完成'], ['failed', '失败'], ['cancelled', '已取消']];
const KIND = { queued: 'dim', in_progress: 'acc', completed: 'ok', failed: 'bad', cancelled: 'warn' };

export function mountTasks(root) {
  const statusSel = h('select', {}, STATUSES.map(([v, t]) => h('option', { value: v, text: t })));
  const listEl = h('div');
  root.append(
    h('div', { class: 'toolbar' },
      statusSel,
      h('button', { class: 'btn sm', text: '刷新', onclick: () => load() })),
    listEl);

  async function load() {
    try {
      const q = new URLSearchParams({ limit: '100' });
      if (statusSel.value) q.set('status', statusSel.value);
      const data = await admin(`/admin/tasks?${q}`);
      render(listEl, data.tasks || []);
    } catch (e) {
      listEl.textContent = '';
      listEl.append(h('div', { class: 'err-text', text: `加载失败: ${errMsg(e)}` }));
    }
  }
  statusSel.onchange = load;
  load();
  return null;
}

function render(listEl, tasks) {
  listEl.textContent = '';
  const table = h('table', null,
    h('thead', null, h('tr', null,
      ['任务 ID', '模型', '平台', '账号', '状态', '进度', '创建时间', '完成时间', '结果 / 错误'].map(t => h('th', { text: t })))),
    h('tbody'));
  for (const t of tasks) {
    table.lastChild.append(h('tr', null,
      h('td', { class: 'mono muted', text: t.id }),
      h('td', { class: 'mono', text: t.model_id }),
      h('td', { text: t.provider_id }),
      h('td', { class: 'mono muted', text: t.account_id }),
      h('td', null, pill(t.status, KIND[t.status] || 'dim')),
      h('td', { text: `${t.progress}%` }),
      h('td', { class: 'muted mono', text: fmtTime(t.created_at) }),
      h('td', { class: 'muted mono', text: t.completed_at ? fmtTime(t.completed_at) : '—' }),
      h('td', null,
        t.status === 'completed' && t.result_url
          ? h('a', { href: t.result_url, target: '_blank', rel: 'noopener', text: '打开视频 ↗' })
          : h('span', { class: 'err-text cell-err', title: t.error || '', text: t.error || '—' }))));
  }
  if (!tasks.length) table.lastChild.append(h('tr', null, h('td', { colspan: '9' }, h('div', { class: 'empty', text: '暂无任务' }))));
  listEl.append(h('div', { class: 'table-wrap' }, table));
}
