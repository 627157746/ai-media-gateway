import { admin } from '../api.js';
import { h, pill, switchEl, openSheet, sheetButtons, confirmDialog, toast, errMsg, relTime, fmtTime, parseJSONField, isSheetOpen, field } from '../ui.js';

const TYPE_DEFAULTS = {
  fal: 'https://queue.fal.run',
  elevenlabs: 'https://api.elevenlabs.io',
  fishaudio: 'https://api.fish.audio',
};

function statePill(a) {
  if (a.state === 'cooling' && a.cooldown_until) {
    return pill(`冷却至 ${fmtTime(a.cooldown_until)}`, 'warn');
  }
  if (a.state === 'disabled') return pill('已停用', 'dim');
  return pill('正常', 'ok');
}

export function mountProviders(root) {
  const listEl = h('div');
  root.append(listEl);

  async function load() {
    try {
      const data = await admin('/admin/status');
      render(listEl, data.providers || [], reload);
    } catch (e) {
      listEl.textContent = '';
      listEl.append(h('div', { class: 'err-text', text: `加载失败: ${errMsg(e)}` }));
    }
  }
  load();

  const timer = setInterval(() => { if (!isSheetOpen()) load(); }, 10000);
  return () => clearInterval(timer);

  function reload() { load(); }
}

function render(listEl, providers, reload) {
  listEl.textContent = '';
  for (const p of providers) listEl.append(providerCard(p, reload));
  listEl.append(
    h('button', {
      class: 'btn text add-btn', text: '＋ 添加供应商',
      onclick: () => providerSheet(null, reload),
    }));
  if (!providers.length) {
    listEl.append(h('div', { class: 'empty', text: '还没有供应商' }));
  }
}

function providerCard(p, reload) {
  const accounts = p.accounts || [];
  const enabledSwitch = switchEl(p.enabled, async v => {
    try {
      await admin(`/admin/providers/${p.id}`, { method: 'PATCH', json: { enabled: v } });
      reload();
    } catch (e) { toast(errMsg(e), 'bad'); reload(); }
  });
  return h('div', { class: 'card' },
    h('div', { class: 'card-head' },
      h('span', { class: 'title mono', text: p.id }),
      pill(p.type, 'acc'),
      p.base_url ? h('span', { class: 'muted mono', text: p.base_url }) : null,
      h('span', { class: 'spacer' }),
      enabledSwitch,
      h('button', { class: 'btn sm', text: '编辑', onclick: () => providerSheet(p, reload) }),
      h('button', {
        class: 'btn sm danger', text: '删除', onclick: async () => {
          if (!await confirmDialog(`删除供应商 "${p.id}"？其下所有账号和模型的关联需要先清理。`)) return;
          try { await admin(`/admin/providers/${p.id}`, { method: 'DELETE' }); toast('已删除', 'ok'); reload(); }
          catch (e) { toast(errMsg(e), 'bad'); }
        },
      })),
    accounts.map(a => accountRow(p, a, reload)),
    h('button', {
      class: 'btn text add-btn', text: '＋ 添加账号',
      onclick: () => accountSheet(p, null, reload),
    }));
}

function accountRow(p, a, reload) {
  const parts = [];
  if (a.last_error) {
    parts.push(h('div', { class: 'acct-err', title: a.last_error, text: `最后错误: ${a.last_error}` }));
  }
  return h('div', { class: 'acct' },
    statePill(a),
    h('span', { class: 'grow' },
      h('div', null,
        h('span', { text: a.name || a.id }),
        ' ',
        h('span', { class: 'muted mono', text: a.api_key_masked })),
      h('div', { class: 'muted fs12' },
        `优先级 ${a.priority} · 权重 ${a.weight} · 成功 ${a.success_count} / 失败 ${a.failure_count}` +
        (a.last_used_at ? ` · 最近使用 ${relTime(a.last_used_at)}` : ''))),
    a.state === 'cooling' || a.last_error ? h('button', {
      class: 'btn sm', text: '重置', onclick: async () => {
        try { await admin(`/admin/accounts/${a.id}/reset`, { method: 'POST' }); toast('已重置', 'ok'); reload(); }
        catch (e) { toast(errMsg(e), 'bad'); }
      },
    }) : null,
    h('button', {
      class: 'btn sm', text: a.enabled ? '停用' : '启用', onclick: async () => {
        try { await admin(`/admin/accounts/${a.id}`, { method: 'PATCH', json: { enabled: !a.enabled } }); reload(); }
        catch (e) { toast(errMsg(e), 'bad'); }
      },
    }),
    h('button', { class: 'btn sm', text: '编辑', onclick: () => accountSheet(p, a, reload) }),
    h('button', {
      class: 'btn sm danger', text: '删除', onclick: async () => {
        if (!await confirmDialog(`删除账号 "${a.name || a.id}"？`)) return;
        try { await admin(`/admin/accounts/${a.id}`, { method: 'DELETE' }); toast('已删除', 'ok'); reload(); }
        catch (e) { toast(errMsg(e), 'bad'); }
      },
    }),
    parts);
}

async function providerSheet(existing, reload) {
  let types = [];
  try { types = (await admin('/admin/provider-types')).data || []; } catch (e) { toast(errMsg(e), 'bad'); return; }
  const idIn = h('input', { type: 'text', value: existing ? existing.id : '', placeholder: 'a-z 0-9 -', disabled: !!existing });
  const typeSel = h('select', {}, types.map(t => h('option', { value: t, text: t })));
  if (existing) typeSel.value = existing.type;
  const baseIn = h('input', { type: 'url', value: existing ? existing.base_url : '', placeholder: TYPE_DEFAULTS[typeSel.value] || '' });
  typeSel.onchange = () => { baseIn.placeholder = TYPE_DEFAULTS[typeSel.value] || ''; };
  const optsIn = h('textarea', { rows: 4, value: existing ? JSON.stringify(existing.options || {}) : '', placeholder: '{"passthrough":true}' });
  const enabledIn = h('input', { type: 'checkbox', checked: existing ? existing.enabled : true });
  const err = h('div', { class: 'err-text' });
  const close = openSheet(existing ? `编辑供应商 ${existing.id}` : '添加供应商',
    h('div', null,
      field('ID（仅创建时可设置）', idIn, '小写字母、数字、连字符，例如 fal'),
      field('类型', typeSel),
      field('Base URL', baseIn, '留空使用该类型的默认上游地址'),
      field('Options（JSON 对象）', optsIn),
      h('label', { class: 'check' }, enabledIn, ' 启用'),
      err,
      sheetButtons('保存', async () => {
        err.textContent = '';
        const o = parseJSONField(optsIn.value, 'Options ');
        if (!o.ok) { err.textContent = o.error; return; }
        const body = { type: typeSel.value, base_url: baseIn.value.trim(), enabled: enabledIn.checked };
        if (o.value !== undefined) body.options = o.value;
        try {
          if (existing) {
            await admin(`/admin/providers/${existing.id}`, { method: 'PATCH', json: body });
          } else {
            const id = idIn.value.trim();
            if (!/^[a-z0-9-]+$/.test(id)) { err.textContent = 'ID 只能包含小写字母、数字、连字符'; return; }
            await admin('/admin/providers', { method: 'POST', json: Object.assign({ id }, body) });
          }
          close(); toast('已保存', 'ok'); reload();
        } catch (e) { err.textContent = errMsg(e); }
      }, () => close())));
}

function accountSheet(p, existing, reload) {
  const nameIn = h('input', { type: 'text', value: existing ? existing.name : '', placeholder: '显示名（可选）' });
  const keyIn = h('input', { type: 'password', value: '', placeholder: existing ? '留空保持不变' : '供应商 API Key' });
  const baseIn = h('input', { type: 'url', value: existing ? existing.base_url : '', placeholder: '留空继承供应商' });
  const prioIn = h('input', { type: 'number', value: existing ? existing.priority : 0 });
  const weightIn = h('input', { type: 'number', value: existing ? existing.weight : 1, min: '1' });
  const enabledIn = h('input', { type: 'checkbox', checked: existing ? existing.enabled : true });
  const optsIn = h('textarea', { rows: 4, value: existing ? JSON.stringify(existing.options || {}) : '', placeholder: '{"default_voice":"…","voices":{}}' });
  const err = h('div', { class: 'err-text' });
  const close = openSheet(existing ? `编辑账号 ${existing.name || existing.id}` : `添加账号 · ${p.id}`,
    h('div', null,
      field('名称', nameIn),
      field('API Key', keyIn, '密钥加密存储，只显示掩码'),
      field('Base URL（覆盖）', baseIn),
      h('div', { class: 'flex-row' },
        h('div', { class: 'flex-1' }, field('优先级（大者先用）', prioIn)),
        h('div', { class: 'flex-1' }, field('权重（组内轮询）', weightIn))),
      field('Options（JSON 对象）', optsIn),
      h('label', { class: 'check' }, enabledIn, ' 启用'),
      err,
      sheetButtons('保存', async () => {
        err.textContent = '';
        const o = parseJSONField(optsIn.value, 'Options ');
        if (!o.ok) { err.textContent = o.error; return; }
        const w = parseInt(weightIn.value, 10) || 0;
        if (w < 1) { err.textContent = '权重必须 ≥ 1'; return; }
        const body = {
          name: nameIn.value.trim(), base_url: baseIn.value.trim(),
          priority: parseInt(prioIn.value, 10) || 0, weight: w,
          enabled: enabledIn.checked,
        };
        if (o.value !== undefined) body.options = o.value;
        const key = keyIn.value.trim();
        try {
          if (existing) {
            if (key) body.api_key = key;
            await admin(`/admin/accounts/${existing.id}`, { method: 'PATCH', json: body });
          } else {
            if (!key) { err.textContent = 'API Key 不能为空'; return; }
            body.api_key = key;
            await admin(`/admin/providers/${p.id}/accounts`, { method: 'POST', json: body });
          }
          close(); toast('已保存', 'ok'); reload();
        } catch (e) { err.textContent = errMsg(e); }
      }, () => close())));
}
