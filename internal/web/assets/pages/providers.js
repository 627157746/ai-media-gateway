import { admin } from '../api.js';
import { h, pill, switchEl, openSheet, sheetButtons, confirmDialog, toast, errMsg, relTime, fmtTime, isSheetOpen, field, mergeOpts, more, voiceEditor, proxyField, maskProxy, proxies, proxyLabel } from '../ui.js';

const TYPE_DEFAULTS = {
  fal: 'https://queue.fal.run',
  elevenlabs: 'https://api.elevenlabs.io',
  fishaudio: 'https://api.fish.audio',
};

const TYPE_LABELS = { fal: 'fal', elevenlabs: 'ElevenLabs', fishaudio: 'Fish Audio' };
const typeLabel = t => TYPE_LABELS[t] || t;

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
      const [data, pool] = await Promise.all([admin('/admin/status'), proxies().catch(() => [])]);
      proxyPool = pool;
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
  listEl.append(h('div', {
    class: 'muted fs12 m4y',
    text: '一个平台可以添加多个 API Key，按优先级和权重自动轮流使用，出错的 key 会自动冷却并切换。',
  }));
  for (const p of providers) listEl.append(providerCard(p, reload));
  listEl.append(
    h('button', {
      class: 'btn text add-btn', text: '＋ 添加平台',
      onclick: () => addPlatformSheet(providers, reload),
    }));
  if (!providers.length) {
    listEl.append(h('div', { class: 'empty', text: '还没有平台' }));
  }
}

// proxyPool caches the 代理池 list (filled by mountProviders.load) for
// display labels — proxy:<id> refs show their name.
let proxyPool = [];

function providerCard(p, reload) {
  const accounts = p.accounts || [];
  const enabledSwitch = switchEl(p.enabled, async v => {
    try {
      await admin(`/admin/providers/${p.id}`, { method: 'PATCH', json: { enabled: v } });
      reload();
    } catch (e) { toast(errMsg(e), 'bad'); reload(); }
  }, p.enabled ? '已启用' : '已停用');
  return h('div', { class: `card${p.enabled ? '' : ' off'}` },
    h('div', { class: 'card-head' },
      h('span', { class: 'title mono', text: p.id }),
      pill(typeLabel(p.type), 'acc'),
      p.base_url ? h('span', { class: 'muted mono', text: p.base_url }) : null,
      p.proxy_url ? h('span', { class: 'muted fs12', text: `代理：${proxyLabel(p.proxy_url, proxyPool)}` }) : null,
      pill(accounts.length ? `${accounts.length} 个 key 轮流使用` : '还没有 key', accounts.length ? 'dim' : 'warn'),
      h('span', { class: 'spacer' }),
      enabledSwitch,
      h('button', { class: 'btn sm primary', text: '＋ 添加 key', onclick: () => accountSheet(p, null, reload) }),
      h('button', { class: 'btn sm', text: '编辑', onclick: () => providerEditSheet(p, reload) }),
      h('button', {
        class: 'btn sm danger', text: '删除', onclick: async () => {
          if (!await confirmDialog(`删除平台 "${p.id}"？其下所有 key 和模型的关联需要先清理。`)) return;
          try { await admin(`/admin/providers/${p.id}`, { method: 'DELETE' }); toast('已删除', 'ok'); reload(); }
          catch (e) { toast(errMsg(e), 'bad'); }
        },
      })),
    accounts.map(a => accountRow(p, a, reload)));
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
        (a.last_used_at ? ` · 最近使用 ${relTime(a.last_used_at)}` : '') +
        (a.proxy_url ? ` · 代理：${proxyLabel(a.proxy_url, proxyPool)}` : ''))),
    a.state === 'cooling' || a.last_error ? h('button', {
      class: 'btn sm', text: '重置', onclick: async () => {
        try { await admin(`/admin/accounts/${a.id}/reset`, { method: 'POST' }); toast('已重置', 'ok'); reload(); }
        catch (e) { toast(errMsg(e), 'bad'); }
      },
    }) : null,
    switchEl(a.enabled, async v => {
      try { await admin(`/admin/accounts/${a.id}`, { method: 'PATCH', json: { enabled: v } }); reload(); }
      catch (e) { toast(errMsg(e), 'bad'); }
    }, a.enabled ? '启用' : '停用'),
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

// trim lines, drop blanks, dedupe, keep order
function parseKeys(text) {
  const seen = new Set(), out = [];
  for (const line of (text || '').split('\n')) {
    const k = line.trim();
    if (k && !seen.has(k)) { seen.add(k); out.push(k); }
  }
  return out;
}

// type, else type-2, type-3 … not already in providers ids
function autoID(type, providers) {
  const ids = new Set(providers.map(p => p.id));
  if (!ids.has(type)) return type;
  for (let i = 2; ; i++) if (!ids.has(`${type}-${i}`)) return `${type}-${i}`;
}

// Sequential account creates; common fields apply to all. When several
// keys share a non-empty name they become <name>-1, <name>-2, …
// Returns [{key, err}] for the failures.
async function createAccounts(providerID, keys, common) {
  const failed = [];
  for (let i = 0; i < keys.length; i++) {
    const body = Object.assign({}, common, { api_key: keys[i] });
    if (keys.length > 1 && body.name) body.name = `${body.name}-${i + 1}`;
    try {
      await admin(`/admin/providers/${providerID}/accounts`, { method: 'POST', json: body });
    } catch (e) {
      failed.push({ key: keys[i], err: e });
    }
  }
  return failed;
}

// One-step add: pick platform, paste keys, done. 更多设置 holds the
// rarely touched fields. After the provider exists (create succeeded),
// retries only add the remaining keys — the provider is never recreated.
async function addPlatformSheet(providers, reload) {
  let types = [];
  try { types = (await admin('/admin/provider-types')).data || []; } catch (e) { toast(errMsg(e), 'bad'); return; }
  const typeSel = h('select', {}, types.map(t => h('option', { value: t, text: typeLabel(t) })));
  const keysIn = h('textarea', { rows: 5, placeholder: 'sk-...\nsk-...' });
  const dupHint = h('div', { class: 'hint' });
  const idIn = h('input', { type: 'text', placeholder: 'a-z 0-9 -' });
  let idDirty = false;
  idIn.oninput = () => { idDirty = true; };
  const baseIn = h('input', { type: 'url' });
  const passIn = h('input', { type: 'checkbox', checked: true });
  const enabledIn = h('input', { type: 'checkbox', checked: true });
  const proxy = proxyField('', '继承全局代理');
  const syncType = () => {
    baseIn.placeholder = TYPE_DEFAULTS[typeSel.value] || '';
    if (!idDirty) idIn.value = autoID(typeSel.value, providers);
    dupHint.textContent = providers.some(p => p.type === typeSel.value)
      ? '已有同类型的平台，也可以直接在它的卡片上「＋ 添加 key」（需要的话仍可再建一个）'
      : '';
  };
  typeSel.onchange = syncType;
  syncType();
  const err = h('div', { class: 'err-text' });
  let providerID = null;
  const lockProviderFields = () => {
    for (const el of [typeSel, idIn, baseIn, passIn, enabledIn]) el.disabled = true;
  };
  const close = openSheet('添加平台',
    h('div', null,
      h('div', { class: 'field' }, h('label', { text: '平台' }), typeSel, dupHint),
      field('API Key（每行一个，可留空稍后添加）', keysIn),
      more('更多设置',
        field('ID', idIn, '小写字母、数字、连字符；自动按平台类型填充，可修改'),
        field('自定义 Base URL', baseIn, '留空使用该平台的默认上游地址'),
        field('代理', proxy.el),
        h('label', { class: 'check' }, passIn, ' 允许直通调用'),
        h('label', { class: 'check mt4' }, enabledIn, ' 启用')),
      err,
      sheetButtons('保存', async () => {
        err.textContent = '';
        try {
          if (!providerID) {
            const id = idIn.value.trim();
            if (!/^[a-z0-9-]+$/.test(id)) { err.textContent = 'ID 只能包含小写字母、数字、连字符'; return; }
            await admin('/admin/providers', {
              method: 'POST', json: {
                id, type: typeSel.value, base_url: baseIn.value.trim(), proxy_url: proxy.value(),
                enabled: enabledIn.checked, options: { passthrough: passIn.checked },
              },
            });
            providerID = id;
            lockProviderFields();
          }
          const keys = parseKeys(keysIn.value);
          if (keys.length) {
            const failed = await createAccounts(providerID, keys, { enabled: true });
            reload();
            if (failed.length) {
              keysIn.value = failed.map(f => f.key).join('\n');
              err.textContent = `${failed.length} 个失败：${errMsg(failed[0].err)}`;
              return;
            }
          }
          close(); toast('已保存', 'ok'); reload();
        } catch (e) { err.textContent = errMsg(e); }
      }, () => close())));
}

function providerEditSheet(p, reload) {
  const opts = p.options || {};
  const baseIn = h('input', { type: 'url', value: p.base_url || '', placeholder: TYPE_DEFAULTS[p.type] || '' });
  const passIn = h('input', { type: 'checkbox', checked: opts.passthrough !== false });
  const enabledIn = h('input', { type: 'checkbox', checked: p.enabled });
  const proxy = proxyField(p.proxy_url || '', '继承全局代理');
  const voice = voiceEditor(opts);
  const err = h('div', { class: 'err-text' });
  const close = openSheet(`编辑平台 ${p.id}`,
    h('div', null,
      field('平台类型', h('input', { type: 'text', value: typeLabel(p.type), disabled: true })),
      field('Base URL', baseIn, '留空使用该平台的默认上游地址'),
      field('代理', proxy.el),
      h('label', { class: 'check' }, passIn, ' 允许直通调用'),
      h('label', { class: 'check mt4' }, enabledIn, ' 启用'),
      more('音色设置', voice.el),
      err,
      sheetButtons('保存', async () => {
        err.textContent = '';
        const v = voice.value();
        try {
          await admin(`/admin/providers/${p.id}`, {
            method: 'PATCH', json: {
              base_url: baseIn.value.trim(), proxy_url: proxy.value(), enabled: enabledIn.checked,
              options: mergeOpts(opts, { passthrough: passIn.checked, default_voice: v.default_voice, voices: v.voices }),
            },
          });
          close(); toast('已保存', 'ok'); reload();
        } catch (e) { err.textContent = errMsg(e); }
      }, () => close())));
}

// Batch create (textarea of keys) or single edit (password input).
// 更多设置 holds priority/weight/base_url/enabled/voices; no JSON.
function accountSheet(p, existing, reload) {
  const nameIn = h('input', { type: 'text', value: existing ? existing.name : '', placeholder: '显示名（可选）' });
  const keysIn = existing ? null : h('textarea', { rows: 5, placeholder: 'sk-...\nsk-...' });
  const keyIn = existing ? h('input', { type: 'password', value: '', placeholder: '留空保持不变' }) : null;
  const baseIn = h('input', { type: 'url', value: existing ? existing.base_url : '', placeholder: '留空继承平台' });
  const prioIn = h('input', { type: 'number', value: existing ? existing.priority : 0 });
  const weightIn = h('input', { type: 'number', value: existing ? existing.weight : 1, min: '1' });
  const enabledIn = h('input', { type: 'checkbox', checked: existing ? existing.enabled : true });
  const proxy = proxyField(existing ? existing.proxy_url : '', '继承平台代理');
  const voice = voiceEditor(existing ? existing.options : {});
  const err = h('div', { class: 'err-text' });
  const close = openSheet(existing ? `编辑账号 ${existing.name || existing.id}` : `添加 key · ${p.id}`,
    h('div', null,
      existing
        ? field('API Key', keyIn, '密钥加密存储，只显示掩码')
        : field('API Key（每行一个）', keysIn),
      field('备注名', nameIn),
      more('更多设置',
        h('div', { class: 'flex-row' },
          h('div', { class: 'flex-1' }, field('优先级', prioIn, '大者先用')),
          h('div', { class: 'flex-1' }, field('权重', weightIn, '同优先级内按比例轮流'))),
        field('自定义 Base URL', baseIn, '留空继承平台'),
        field('代理', proxy.el),
        h('label', { class: 'check' }, enabledIn, ' 启用'),
        more('音色设置', voice.el)),
      err,
      sheetButtons('保存', async () => {
        err.textContent = '';
        const w = parseInt(weightIn.value, 10) || 0;
        if (w < 1) { err.textContent = '权重必须 ≥ 1'; return; }
        const common = {
          name: nameIn.value.trim(), base_url: baseIn.value.trim(), proxy_url: proxy.value(),
          priority: parseInt(prioIn.value, 10) || 0, weight: w,
          enabled: enabledIn.checked,
          options: mergeOpts(existing ? existing.options : {}, voice.value()),
        };
        try {
          if (existing) {
            const key = keyIn.value.trim();
            if (key) common.api_key = key;
            await admin(`/admin/accounts/${existing.id}`, { method: 'PATCH', json: common });
            close(); toast('已保存', 'ok'); reload();
            return;
          }
          const keys = parseKeys(keysIn.value);
          if (!keys.length) { err.textContent = 'API Key 不能为空'; return; }
          const failed = await createAccounts(p.id, keys, common);
          reload();
          if (failed.length) {
            keysIn.value = failed.map(f => f.key).join('\n');
            err.textContent = `${failed.length} 个失败：${errMsg(failed[0].err)}`;
            return;
          }
          close(); toast('已保存', 'ok'); reload();
        } catch (e) { err.textContent = errMsg(e); }
      }, () => close())));
}
