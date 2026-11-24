import { admin } from '../api.js';
import { h, field, toast, errMsg, openSheet, sheetButtons, confirmDialog } from '../ui.js';

// mountVoices renders the 音色库 page: a per-provider list of (friendly name →
// vendor voice id), plus a picker that imports voices straight from the
// vendor's own library where the adapter supports it (fal does not).
export function mountVoices(root) {
  const err = h('div', { class: 'err-text' });
  const card = h('div', { class: 'card' });
  root.append(card, err);

  let providers = [];
  let voices = [];

  async function load() {
    err.textContent = '';
    try {
      const [ps, vs] = await Promise.all([admin('/admin/providers'), admin('/admin/voices')]);
      providers = ps.providers || [];
      voices = vs.voices || [];
      render();
    } catch (e) {
      err.textContent = errMsg(e);
    }
  }
  load();

  const provLabel = (p) => `${p.id}（${p.type}）`;
  const provName = (id) => {
    const p = providers.find((x) => x.id === id);
    return p ? provLabel(p) : id;
  };

  function render() {
    card.textContent = '';
    card.append(
      h('div', { class: 'card-head' },
        h('span', { class: 'title', text: '音色库' }),
        h('span', { class: 'muted fs12', text: '客户端通过 /v1/audio/voices 读取启用项' }),
        h('span', { class: 'spacer' }),
        h('button', { class: 'btn sm', text: '从上游导入', onclick: () => importSheet() }),
        h('button', { class: 'btn sm', text: '克隆音色', onclick: () => cloneSheet() }),
        h('button', { class: 'btn sm primary', text: '＋ 新建音色', onclick: () => editSheet(null) })),
      voices.length
        ? h('div', { class: 'table-wrap' },
          h('table', null,
            h('thead', null, h('tr', null,
              ['显示名', '平台', '上游音色 ID', '状态', '操作'].map((t) => h('th', { text: t })))),
            h('tbody', null, voices.map(row))))
        : h('div', { class: 'empty', text: '还没有音色，可以点「从上游导入」直接拉取' }));
  }

  function row(v) {
    return h('tr', null,
      h('td', { text: v.name }),
      h('td', { class: 'fs12', text: provName(v.provider_id) }),
      h('td', null, h('span', { class: 'mono fs12', text: v.upstream_id })),
      h('td', null, h('span', { class: `fs12 ${v.enabled ? '' : 'muted'}`, text: v.enabled ? '启用' : '停用' })),
      h('td', null,
        h('button', { class: 'btn sm', text: '编辑', onclick: () => editSheet(v) }),
        h('button', {
          class: 'btn sm', text: v.enabled ? '停用' : '启用', onclick: async () => {
            try {
              await admin(`/admin/voices/${v.id}`, { method: 'PATCH', json: { enabled: !v.enabled } });
              toast('已更新', 'ok');
              load();
            } catch (e) { toast(errMsg(e), 'bad'); }
          },
        }),
        h('button', {
          class: 'btn sm danger', text: '删除', onclick: async () => {
            if (!await confirmDialog(`删除音色 "${v.name}"？`)) return;
            try {
              await admin(`/admin/voices/${v.id}`, { method: 'DELETE' });
              toast('已删除', 'ok');
              load();
            } catch (e) { toast(errMsg(e), 'bad'); }
          },
        })));
  }

  function provSelect(value) {
    const sel = h('select', null, providers.map((p) => h('option', { value: p.id, text: provLabel(p) })));
    if (value) sel.value = value;
    return sel;
  }

  function editSheet(v) {
    const isNew = !v;
    const nameIn = h('input', { type: 'text', value: isNew ? '' : v.name, placeholder: '显示名，如 中文旁白' });
    const provSel = provSelect(isNew ? (providers[0] || {}).id : v.provider_id);
    provSel.disabled = !isNew; // 平台不可改：唯一约束与解析都按平台划分
    const upIn = h('input', { type: 'text', value: isNew ? '' : v.upstream_id, placeholder: '上游音色 ID（voice_id / reference_id）' });
    const err2 = h('div', { class: 'err-text' });
    const close = openSheet(isNew ? '新建音色' : `编辑音色 ${v.name}`,
      h('div', null,
        field('显示名', nameIn, '画布上显示的名字，可随时改'),
        field('平台', provSel, isNew ? '音色归属的平台，创建后不可改' : '平台不可修改'),
        field('上游音色 ID', upIn, 'ElevenLabs 的 voice_id 或 Fish Audio 的 reference_id'),
        err2,
        sheetButtons('保存', async () => {
          err2.textContent = '';
          const body = { name: nameIn.value.trim(), upstream_id: upIn.value.trim() };
          try {
            if (isNew) {
              await admin('/admin/voices', { method: 'POST', json: { ...body, provider_id: provSel.value } });
            } else {
              await admin(`/admin/voices/${v.id}`, { method: 'PATCH', json: body });
            }
            close();
            toast('已保存', 'ok');
            load();
          } catch (e) { err2.textContent = errMsg(e); }
        }, () => close())));
  }

  function importSheet() {
    if (!providers.length) { toast('请先在「平台」页添加平台', 'bad'); return; }
    const provSel = provSelect((providers[0] || {}).id);
    const listBox = h('div', null, h('div', { class: 'muted fs12', text: '拉取中…' }));
    const err2 = h('div', { class: 'err-text' });
    // 勾选状态与名字后缀在每次重新拉取时重置。
    let picks = [];
    let suffixIn = h('input', { type: 'text' });

    const applyBtn = h('button', {
      class: 'btn primary', text: '导入选中', style: 'display:none', onclick: async () => {
        err2.textContent = '';
        const chosen = picks.filter((p) => p.cb.checked);
        if (!chosen.length) { err2.textContent = '请至少勾选一个音色'; return; }
        const suffix = suffixIn.value.trim();
        let ok = 0;
        const failed = [];
        for (const p of chosen) {
          try {
            await admin('/admin/voices', {
              method: 'POST',
              json: { name: (p.item.name || p.item.id) + suffix, provider_id: provSel.value, upstream_id: p.item.id },
            });
            ok++;
          } catch (e) { failed.push({ item: p.item, err: e }); }
        }
        await load();
        if (failed.length) {
          err2.textContent = `成功 ${ok} 个，失败 ${failed.length} 个：${errMsg(failed[0].err)}`;
          loadList();
          return;
        }
        close();
        toast(`已导入 ${ok} 个音色`, 'ok');
      },
    });

    const close = openSheet('从上游导入音色',
      h('div', null,
        field('平台', provSel, '选定平台后会自动拉取该平台账号下的音色'),
        listBox,
        err2,
        sheetButtons('关闭', () => close())));

    async function loadList() {
      picks = [];
      applyBtn.style.display = 'none';
      listBox.textContent = '';
      listBox.append(h('div', { class: 'muted fs12', text: '拉取中…' }));
      err2.textContent = '';
      const pid = provSel.value;
      let list;
      try {
        const r = await admin(`/admin/providers/${pid}/upstream-voices`);
        list = r.data || [];
      } catch (e) {
        // fal 等无音色库的平台由后端明确报错，这里直接展示并引导手动填写。
        listBox.textContent = '';
        err2.textContent = errMsg(e);
        return;
      }
      listBox.textContent = '';
      if (!list.length) {
        listBox.append(h('div', { class: 'muted fs12', text: '该平台账号下没有可用音色，可手动新建' }));
        return;
      }
      const taken = new Set(voices.filter((v) => v.provider_id === pid).map((v) => v.upstream_id));
      const rows = list.map((item) => {
        const exists = taken.has(item.id);
        const cb = h('input', { type: 'checkbox' });
        if (exists) cb.disabled = true;
        else picks.push({ cb, item });
        return h('label', { class: 'check', style: 'display:flex;gap:8px;align-items:flex-start;margin:4px 0' },
          cb,
          h('span', null,
            h('span', { class: exists ? 'muted' : '', text: item.name || item.id }),
            item.detail ? h('span', { class: 'muted fs12', text: ` · ${item.detail}` }) : null,
            h('br'),
            h('span', { class: 'mono fs12 muted', text: item.id }),
            exists ? h('span', { class: 'hint', text: ' · 已导入' }) : null));
      });
      suffixIn = h('input', { type: 'text', value: '', placeholder: '留空使用上游名称' });
      listBox.append(
        h('div', { class: 'muted fs12', text: `共 ${list.length} 个，已导入 ${taken.size} 个` }),
        h('div', { style: 'max-height:260px;overflow:auto;margin:8px 0' }, rows),
        field('统一后缀（可选）', suffixIn, '导入后加在名字后面，方便区分'),
        h('div', { class: 'mt10' }, applyBtn));
      applyBtn.style.display = '';
    }

    provSel.onchange = loadList;
    loadList();
  }

  // cloneSheet uploads a recording and creates a 音色库 entry. The two vendors
  // behave differently and the copy says so: ElevenLabs builds a durable voice
  // immediately, Fish Audio replays the sample on every synthesis request.
  function cloneSheet() {
    if (!providers.length) { toast('请先在「平台」页添加平台', 'bad'); return; }
    const nameIn = h('input', { type: 'text', placeholder: '音色显示名，如 我的声音' });
    const provSel = provSelect((providers[0] || {}).id);
    const fileIn = h('input', { type: 'file', accept: 'audio/*,.mp3,.wav,.m4a,.ogg,.flac' });
    const hint = h('div', { class: 'muted fs12' });
    const err2 = h('div', { class: 'err-text' });

    const updateHint = () => {
      const t = providers.find((p) => p.id === provSel.value);
      const type = t ? t.type : '';
      hint.textContent = type === 'fishaudio'
        ? 'Fish Audio：零样本克隆，样本只存在本地，每次合成都会上传这段音频，无需等待训练'
        : type === 'mimo'
          ? 'MiMo：零样本克隆，样本存本地，合成时以 data URL 上传。需配合 voiceclone 模型使用'
          : type === 'elevenlabs'
            ? 'ElevenLabs：创建持久音色，克隆后可长期复用；若上游要求验证会自动存为停用'
            : `平台 ${type} 暂不支持克隆，请用「从上游导入」或手动新建`;
    };
    updateHint();

    const close = openSheet('克隆音色',
      h('div', null,
        field('显示名', nameIn, '克隆完成后在画布上显示的名字'),
        field('平台', provSel),
        field('音频样本', fileIn, '建议 10 秒到 3 分钟的清晰单人人声；25MB 以内'),
        hint,
        err2,
        sheetButtons('开始克隆', async () => {
          err2.textContent = '';
          const file = fileIn.files && fileIn.files[0];
          if (!nameIn.value.trim()) { err2.textContent = '请填写显示名'; return; }
          if (!file) { err2.textContent = '请选择音频文件'; return; }
          const fd = new FormData();
          fd.set('provider_id', provSel.value);
          fd.set('name', nameIn.value.trim());
          fd.set('files', file, file.name);
          try {
            const r = await admin('/admin/voices/clone', { method: 'POST', body: fd });
            close();
            toast(r.notice || '克隆完成', 'ok');
            load();
          } catch (e) { err2.textContent = errMsg(e); }
        }, () => close())));
    provSel.onchange = updateHint;
  }
}
