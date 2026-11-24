import { admin } from '../api.js';
import { h, pill, switchEl, openSheet, sheetButtons, confirmDialog, toast, errMsg, parseJSONField, field } from '../ui.js';

const KINDS = { image: '图片', video: '视频', speech: '语音' };

// 插入模板 presets keyed by `${type}:${kind}`.
const TEMPLATES = {
  'fal:video': ['fal 视频（图生+参考端点）', {
    upstream_image: 'bytedance/seedance-2.5/image-to-video',
    upstream_reference: 'bytedance/seedance-2.5/references',
    defaults: {},
    fields: { duration_format: '{n}s' },
    drop: [],
  }],
  'fal:image': ['fal 图片（含编辑端点）', {
    upstream_edit: 'fal-ai/flux-pro/v1.1/edit',
    fields: { size_mode: 'image_size' },
  }],
  'fal:speech': ['fal 语音', {
    fields: {},
    defaults: {},
  }],
  'elevenlabs:speech': ['ElevenLabs 语音别名', {
    voices: { narrator: '21m00Tcm4TlvDq8ikWAM' },
    default_voice: 'narrator',
  }],
  'fishaudio:speech': ['Fish Audio 语音别名', {
    voices: { narrator: 'reference-id' },
    default_voice: 'narrator',
  }],
  '*:*': ['账号白名单', { accounts: ['acc_xxxxxxxxxxxx'] }],
};

export function mountModels(root) {
  const listEl = h('div');
  root.append(listEl);

  async function load() {
    try {
      const [models, providers] = await Promise.all([
        admin('/admin/models'), admin('/admin/providers'),
      ]);
      render(listEl, models.models || [], providers.providers || [], load);
    } catch (e) {
      listEl.textContent = '';
      listEl.append(h('div', { class: 'err-text', text: `加载失败: ${errMsg(e)}` }));
    }
  }
  load();
  return null;
}

function render(listEl, models, providers, reload) {
  listEl.textContent = '';
  const byKind = {};
  for (const m of models) (byKind[m.kind] = byKind[m.kind] || []).push(m);
  const table = h('table', null,
    h('thead', null, h('tr', null, ['ID', '供应商', '上游端点', '启用', '操作'].map(t => h('th', { text: t })))),
    h('tbody'));
  let any = false;
  for (const kind of ['image', 'video', 'speech']) {
    const rows = byKind[kind];
    if (!rows || !rows.length) continue;
    any = true;
    table.lastChild.append(h('tr', { class: 'kind-head' }, h('td', { colspan: '5', text: KINDS[kind] })));
    for (const m of rows) {
      table.lastChild.append(h('tr', null,
        h('td', { class: 'mono', text: m.id }),
        h('td', { class: 'mono muted', text: m.provider_id }),
        h('td', { class: 'mono muted', text: m.upstream || m.id }),
        h('td', null, switchEl(m.enabled, async v => {
          try { await admin(`/admin/models/${m.id}`, { method: 'PATCH', json: { enabled: v } }); }
          catch (e) { toast(errMsg(e), 'bad'); }
          reload();
        })),
        h('td', null,
          h('button', { class: 'btn sm', text: '编辑', onclick: () => modelSheet(m, providers, reload) }),
          ' ',
          h('button', {
            class: 'btn sm danger', text: '删除', onclick: async () => {
              if (!await confirmDialog(`删除模型 "${m.id}"？`)) return;
              try { await admin(`/admin/models/${m.id}`, { method: 'DELETE' }); toast('已删除', 'ok'); reload(); }
              catch (e) { toast(errMsg(e), 'bad'); }
            },
          }))));
    }
  }
  if (!any) table.lastChild.append(h('tr', null, h('td', { colspan: '5' }, h('div', { class: 'empty', text: '还没有模型' }))));
  listEl.append(
    h('div', { class: 'table-wrap' }, table),
    h('div', { class: 'muted fs12 mt10' },
      '提示：未注册的模型可按 fal/<上游端点> 直通调用（供应商 options.passthrough 默认为 true）。'),
    h('button', {
      class: 'btn text add-btn', text: '＋ 添加模型',
      onclick: () => modelSheet(null, providers, reload),
    }));
}

function modelSheet(existing, providers, reload) {
  const idIn = h('input', { type: 'text', value: existing ? existing.id : '', disabled: !!existing, placeholder: '对外的模型名，如 seedance-2.5' });
  const provSel = h('select', {}, providers.map(p => h('option', { value: p.id, text: `${p.id} (${p.type})` })));
  if (existing) provSel.value = existing.provider_id;
  const kindSel = h('select', {},
    h('option', { value: 'image', text: '图片' }),
    h('option', { value: 'video', text: '视频' }),
    h('option', { value: 'speech', text: '语音' }));
  if (existing) kindSel.value = existing.kind;
  const upIn = h('input', { type: 'text', value: existing ? existing.upstream : '', placeholder: '上游端点，如 bytedance/seedance-2.5/text-to-video' });
  const optsIn = h('textarea', { rows: 9, value: existing ? JSON.stringify(existing.options || {}, null, 2) : '', placeholder: '{}' });
  const enabledIn = h('input', { type: 'checkbox', checked: existing ? existing.enabled : true });
  const err = h('div', { class: 'err-text' });

  const tplRow = h('div', { class: 'toolbar m4y' });
  const syncTemplates = () => {
    tplRow.textContent = '';
    const ptype = (providers.find(p => p.id === provSel.value) || {}).type || '';
    const specific = `${ptype}:${kindSel.value}`;
    const sel = h('select', { class: 'w-auto' }, h('option', { value: '', text: '插入模板…' }));
    for (const key of [specific, `${ptype}:*`, '*:*']) {
      if (TEMPLATES[key]) sel.append(h('option', { value: key, text: TEMPLATES[key][0] }));
    }
    sel.onchange = () => {
      if (sel.value) {
        optsIn.value = JSON.stringify(mergeOpts(optsIn.value.trim(), TEMPLATES[sel.value][1]), null, 2);
        sel.value = '';
      }
    };
    tplRow.append(sel, h('span', { class: 'muted fs12', text: '模板会合并进当前 Options' }));
  };
  provSel.onchange = syncTemplates;
  kindSel.onchange = syncTemplates;
  syncTemplates();

  const close = openSheet(existing ? `编辑模型 ${existing.id}` : '添加模型',
    h('div', null,
      field('ID', idIn),
      field('供应商', provSel),
      field('类型', kindSel),
      field('上游端点', upIn, '留空时默认使用模型 ID'),
      h('div', null, h('label', { class: 'lbl', text: 'Options（JSON 对象）' }), optsIn, tplRow),
      h('label', { class: 'check' }, enabledIn, ' 启用'),
      err,
      sheetButtons('保存', async () => {
        err.textContent = '';
        const o = parseJSONField(optsIn.value, 'Options ');
        if (!o.ok) { err.textContent = o.error; return; }
        const body = {
          provider_id: provSel.value, kind: kindSel.value,
          upstream: upIn.value.trim(), enabled: enabledIn.checked,
        };
        if (o.value !== undefined) body.options = o.value;
        try {
          if (existing) {
            await admin(`/admin/models/${existing.id}`, { method: 'PATCH', json: body });
          } else {
            const id = idIn.value.trim();
            if (!id) { err.textContent = 'ID 不能为空'; return; }
            await admin('/admin/models', { method: 'POST', json: Object.assign({ id }, body) });
          }
          close(); toast('已保存', 'ok'); reload();
        } catch (e) { err.textContent = errMsg(e); }
      }, close)));
}

function mergeOpts(curText, tpl) {
  try { return Object.assign({}, JSON.parse(curText), tpl); } catch { return tpl; }
}
