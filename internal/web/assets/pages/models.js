import { admin } from '../api.js';
import { h, pill, switchEl, openSheet, sheetButtons, confirmDialog, toast, errMsg, field, mergeOpts, more, voiceEditor, kvRows } from '../ui.js';

const KINDS = { image: '图片', video: '视频', speech: '语音' };

// Logical parameter keys the fal adapter maps through options.fields.
const FAL_FIELD_KEYS = {
  image: ['prompt', 'count', 'ratio', 'size', 'resolution', 'quality', 'background', 'images', 'mask'],
  video: ['prompt', 'duration', 'ratio', 'resolution', 'generate_audio', 'watermark', 'first_frame', 'last_frame', 'references', 'videos', 'audios'],
  speech: ['text', 'voice', 'speed'],
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
    h('thead', null, h('tr', null, ['ID', '平台', '上游端点', '启用', '操作'].map(t => h('th', { text: t })))),
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
        h('td', { class: 'mid' }, switchEl(m.enabled, async v => {
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
      '提示：未注册的模型可按 fal/<上游端点> 直通调用（平台默认开启「允许直通调用」，可在平台编辑中关闭）。'),
    h('button', {
      class: 'btn text add-btn', text: '＋ 添加模型',
      onclick: () => modelSheet(null, providers, reload),
    }),
    h('button', {
      class: 'btn text add-btn', text: '从平台获取',
      onclick: () => fetchSheet(providers, models, reload),
    }));
}

function modelSheet(existing, providers, reload) {
  const idIn = h('input', { type: 'text', value: existing ? existing.id : '', placeholder: '对外的模型名，如 seedance-2.5' });
  const provSel = h('select', {}, providers.map(p => h('option', { value: p.id, text: `${p.id} (${p.type})` })));
  if (existing) provSel.value = existing.provider_id;
  const kindSel = h('select', {},
    h('option', { value: 'image', text: '图片' }),
    h('option', { value: 'video', text: '视频' }),
    h('option', { value: 'speech', text: '语音' }));
  if (existing) kindSel.value = existing.kind;
  const upIn = h('input', { type: 'text', value: existing ? existing.upstream : '', placeholder: '上游端点，如 bytedance/seedance-2.5/text-to-video' });
  const enabledIn = h('input', { type: 'checkbox', checked: existing ? existing.enabled : true });
  const err = h('div', { class: 'err-text' });
  const secBox = h('div');
  // Warn when the chosen ID won't be detected by Infinite Canvas as the
  // selected kind (it guesses from the name, not the kind field).
  const idHint = h('div', { class: 'warn-text' });
  const syncIdHint = () => { idHint.textContent = canvasHint(idIn.value.trim(), kindSel.value); };
  idIn.addEventListener('input', syncIdHint);

  // Working copy of options: pre-filled from the existing model, then
  // updated by collect() so typed values survive section rebuilds and
  // unknown keys are preserved on save.
  let workOpts = Object.assign({}, (existing && existing.options) || {});
  let widgets = {};

  // Pull the currently rendered widget values into workOpts.
  function collect() {
    const w = widgets;
    const patch = {};
    if (w.editIn) patch.upstream_edit = w.editIn.value.trim();
    if (w.imgIn) patch.upstream_image = w.imgIn.value.trim();
    if (w.refIn) patch.upstream_reference = w.refIn.value.trim();
    if (w.fieldsKV || w.sizeSel || w.durSel) {
      const f = w.fieldsKV ? w.fieldsKV.value() : {};
      if (w.sizeSel && w.sizeSel.value !== 'image_size') f.size_mode = w.sizeSel.value;
      if (w.durSel && w.durSel.value !== 'string') f.duration_format = w.durSel.value;
      patch.fields = f;
    }
    if (w.defaultsKV) {
      const d = {};
      for (const [k, raw] of Object.entries(w.defaultsKV.value())) {
        try { d[k] = JSON.parse(raw); } catch { d[k] = raw; }
      }
      patch.defaults = d;
    }
    if (w.dropIn) patch.drop = w.dropIn.value.split(/[\s,]+/).map(s => s.trim()).filter(Boolean);
    if (w.voice) { const v = w.voice.value(); patch.default_voice = v.default_voice; patch.voices = v.voices; }
    if (w.accts) patch.accounts = w.accts.filter(cb => cb.checked).map(cb => cb.value);
    workOpts = mergeOpts(workOpts, patch);
  }

  // Rebuild the collapsed sections for the current 平台/类型.
  async function rebuild() {
    collect();
    const w = {};
    widgets = w;
    secBox.textContent = '';
    const p = providers.find(x => x.id === provSel.value) || {};
    const ptype = p.type || '';
    const kind = kindSel.value;
    const o = workOpts;
    const fieldsOpts = o.fields || {};

    if (ptype === 'fal' && kind === 'image') {
      w.editIn = h('input', { type: 'text', value: o.upstream_edit || '', placeholder: '如 fal-ai/flux-pro/v1.1/edit' });
      w.sizeSel = h('select', {},
        h('option', { value: 'image_size', text: 'image_size（宽x高，默认）' }),
        h('option', { value: 'aspect_ratio', text: 'aspect_ratio（宽高比）' }),
        h('option', { value: 'none', text: 'none（不传尺寸）' }));
      w.sizeSel.value = fieldsOpts.size_mode || 'image_size';
      secBox.append(more('编辑与尺寸',
        field('编辑端点', w.editIn, '带参考图请求时改用的上游端点'),
        field('尺寸参数方式', w.sizeSel, '上游接收尺寸参数的形式')));
    }

    if (ptype === 'fal' && kind === 'video') {
      w.imgIn = h('input', { type: 'text', value: o.upstream_image || '', placeholder: '如 bytedance/seedance-2.5/image-to-video' });
      w.refIn = h('input', { type: 'text', value: o.upstream_reference || '', placeholder: '如 bytedance/seedance-2.5/references' });
      w.durSel = h('select', {},
        h('option', { value: 'string', text: 'string（原样，默认）' }),
        h('option', { value: 'int', text: 'int（秒数）' }),
        h('option', { value: '{n}s', text: '{n}s（如 10s）' }));
      w.durSel.value = fieldsOpts.duration_format || 'string';
      secBox.append(more('视频端点与时长',
        field('图生视频端点', w.imgIn, '图生视频请求时改用的上游端点'),
        field('参考生视频端点', w.refIn, '参考素材请求时改用的上游端点'),
        field('时长格式', w.durSel, '发送给上游的时长值格式')));
    }

    if (ptype === 'fal') {
      const mapping = {};
      for (const [k, v] of Object.entries(fieldsOpts)) {
        if (k !== 'size_mode' && k !== 'duration_format') mapping[k] = v;
      }
      w.fieldsKV = kvRows({
        rows: mapping, keyOptions: FAL_FIELD_KEYS[kind] || [],
        keyPlaceholder: '选择参数', valPlaceholder: '上游字段名（留空 = 不发送该参数）',
      });
      w.defaultsKV = kvRows({
        rows: o.defaults || {},
        keyPlaceholder: '上游字段名', valPlaceholder: '值（5、true、[..] 等按 JSON 解析）',
      });
      w.dropIn = h('input', { type: 'text', value: (o.drop || []).join(', '), placeholder: '如 prompt, seed' });
      secBox.append(more('上游参数',
        h('div', null, h('label', { class: 'lbl', text: '参数映射' }), w.fieldsKV.el),
        h('div', { class: 'mt10' }, h('label', { class: 'lbl', text: '固定参数' }), w.defaultsKV.el),
        field('移除参数', w.dropIn, '多个用逗号或空格分隔')));
    }

    if (kind === 'speech') {
      w.voice = voiceEditor(o);
      secBox.append(more('音色设置', w.voice.el));
    }

    // 限定账号: checkboxes for the selected platform's accounts.
    const acctBox = h('div', null, h('div', { class: 'muted fs12', text: '账号加载中…' }));
    secBox.append(more('限定账号', acctBox,
      h('div', { class: 'muted fs12', text: '都不勾选 = 该平台全部账号可用' })));
    const pid = provSel.value;
    const want = new Set(o.accounts || []);
    try {
      const res = await admin(`/admin/providers/${pid}/accounts`);
      if (widgets !== w || provSel.value !== pid) return;
      acctBox.textContent = '';
      const accs = res.accounts || [];
      if (!accs.length) {
        w.accts = [];
        acctBox.append(h('div', { class: 'muted fs12', text: '该平台还没有账号' }));
        return;
      }
      w.accts = accs.map(a => {
        const cb = h('input', { type: 'checkbox', value: a.id, checked: want.has(a.id) });
        acctBox.append(h('label', { class: 'check' }, cb, ` ${a.name || a.id}`));
        return cb;
      });
    } catch (e) {
      if (widgets !== w) return;
      acctBox.textContent = '';
      acctBox.append(h('div', { class: 'err-text', text: `账号加载失败：${errMsg(e)}` }));
    }
  }

  provSel.onchange = rebuild;
  kindSel.onchange = () => { syncIdHint(); rebuild(); };

  const idField = field('ID', idIn);
  if (existing) {
    idField.append(h('div', { class: 'hint', text: '改名后，客户端（如 Infinite Canvas）需要改用新名字并重新拉取模型列表' }));
  }
  idField.append(idHint);
  syncIdHint();

  const close = openSheet(existing ? `编辑模型 ${existing.id}` : '添加模型',
    h('div', null,
      idField,
      field('平台', provSel),
      field('类型', kindSel),
      field('上游端点', upIn, '留空时默认使用模型 ID'),
      secBox,
      h('label', { class: 'check' }, enabledIn, ' 启用'),
      err,
      sheetButtons('保存', async () => {
        err.textContent = '';
        collect();
        const body = {
          provider_id: provSel.value, kind: kindSel.value,
          upstream: upIn.value.trim(), enabled: enabledIn.checked,
          options: workOpts,
        };
        try {
          if (existing) {
            const nid = idIn.value.trim();
            if (!nid) { err.textContent = 'ID 不能为空'; return; }
            if (nid !== existing.id) {
              if (!/^[a-z0-9][a-z0-9.-]*$/.test(nid)) {
                err.textContent = '模型 ID 只能包含小写字母、数字、点和连字符，且不能以点或连字符开头';
                return;
              }
              body.id = nid;
            }
            await admin(`/admin/models/${existing.id}`, { method: 'PATCH', json: body });
          } else {
            const id = idIn.value.trim();
            if (!id) { err.textContent = 'ID 不能为空'; return; }
            await admin('/admin/models', { method: 'POST', json: Object.assign({ id }, body) });
          }
          close(); toast('已保存', 'ok'); reload();
        } catch (e) { err.textContent = errMsg(e); }
      }, () => close())));
  rebuild();
}

// ---- 从平台获取 (R9) ----

const FAL_CATEGORIES = [
  ['text-to-image', '文生图'],
  ['image-to-image', '图生图/编辑'],
  ['text-to-video', '文生视频'],
  ['image-to-video', '图生视频'],
  ['text-to-speech', '文生语音'],
];

const TASK_SUFFIXES = [
  'text-to-image', 'image-to-image', 'text-to-video', 'image-to-video',
  'reference-to-video', 'text-to-speech', 'edit',
];

// Infinite Canvas guesses model capability from the NAME only and ignores
// the /v1/models `kind` field — ported verbatim from
// basketikun/infinite-canvas web/src/stores/use-config-store.ts
// (guessCapability). Keep the keyword lists and their order in sync.
const CANVAS_VIDEO_KEYWORDS = ['video', 'sora', 'veo', 'kling', 'wan', 'hailuo'];
const CANVAS_AUDIO_KEYWORDS = ['audio', 'tts', 'speech', 'voice', 'music', 'sound'];
const CANVAS_IMAGE_KEYWORDS = ['seedream', 'gpt-image', 'image', 'dall-e', 'dalle', 'imagen', 'flux', 'sdxl', 'stable-diffusion', 'midjourney'];
function canvasGuess(name) {
  const v = String(name || '').toLowerCase();
  if (CANVAS_VIDEO_KEYWORDS.some(k => v.includes(k))) return 'video';
  if (CANVAS_AUDIO_KEYWORDS.some(k => v.includes(k))) return 'audio';
  if (CANVAS_IMAGE_KEYWORDS.some(k => v.includes(k))) return 'image';
  return 'text';
}
const KIND_TO_CANVAS = { image: 'image', video: 'video', speech: 'audio' };
const CANVAS_SUFFIX = { image: '-image', video: '-video', speech: '-tts' };
const CANVAS_LABEL = { image: '生图', video: '视频', text: '文本', audio: '音频' };

// canvasHint warns when a model id won't be detected by Canvas as its kind
// (e.g. a video name like `wan-…` on an image model can't be fixed by a
// suffix). '' = Canvas will detect it correctly.
function canvasHint(id, kind) {
  const want = KIND_TO_CANVAS[kind];
  if (!want) return '';
  const g = canvasGuess(id);
  if (g === want) return '';
  return `Infinite Canvas 会按名称识别为「${CANVAS_LABEL[g]}」，需在 Canvas 渠道里手动改成「${CANVAS_LABEL[want]}」`;
}

// autoModelID derives a model id from an upstream endpoint id:
// lowercase, drop the owner segment (or the fal-ai/ prefix), drop a
// trailing task segment, join with '-', keep [a-z0-9.-], collapse/trim
// dashes; fish ids get a fish- prefix. When Canvas wouldn't guess `kind`
// from the result, append a capability suffix (-image/-video/-tts).
// Dedupes with -2, -3… against `taken` (existing model ids + other
// selections).
function autoModelID(raw, providerType, kind, taken) {
  const parts = String(raw || '').toLowerCase().split('/');
  if (parts[0] === 'fal-ai') parts.shift();
  else if (parts.length > 1) parts.shift();
  if (parts.length > 1 && TASK_SUFFIXES.includes(parts[parts.length - 1])) parts.pop();
  let id = parts.join('-')
    .replace(/[^a-z0-9.-]+/g, '-')
    .replace(/-{2,}/g, '-')
    .replace(/^-+|-+$/g, '');
  if (providerType === 'fishaudio' && !id.startsWith('fish-')) id = 'fish-' + id;
  if (CANVAS_SUFFIX[kind] && canvasGuess(id) !== KIND_TO_CANVAS[kind]) id += CANVAS_SUFFIX[kind];
  if (!/^[a-z0-9]/.test(id)) id = `m-${id}`;
  let cand = id;
  for (let i = 2; taken.has(cand); i++) cand = `${id}-${i}`;
  return cand;
}

// fetchSheet lists the platform's upstream models via
// GET /admin/providers/{id}/upstream-models and imports the checked ones.
function fetchSheet(providers, models, reload) {
  const provSel = h('select', {}, providers.map(p => h('option', { value: p.id, text: `${p.id} (${p.type})` })));
  const catSel = h('select', {}, FAL_CATEGORIES.map(([v, l]) => h('option', { value: v, text: l })));
  const catField = field('分类', catSel);
  const qIn = h('input', { type: 'text', placeholder: '搜索模型名，如 seedance' });
  const listEl = h('div');
  const moreBox = h('div', { class: 'mt10' });
  const err = h('div', { class: 'err-text' });
  const countBtn = h('button', { class: 'btn primary', text: '添加选中的 0 个', disabled: true });

  const sel = new Map();   // upstream id -> {item, idIn, fieldEl}
  const imported = new Set();
  let items = [];
  let nextCursor = '';
  let loading = false;

  const ptype = () => (providers.find(p => p.id === provSel.value) || {}).type || '';
  const isAdded = id => imported.has(id) ||
    models.some(m => m.provider_id === provSel.value && m.upstream === id);
  const updateCount = () => {
    countBtn.textContent = `添加选中的 ${sel.size} 个`;
    countBtn.disabled = !sel.size;
  };

  function toggle(it, on) {
    if (on) {
      const taken = new Set(models.map(m => m.id));
      for (const s of sel.values()) taken.add(s.idIn.value.trim());
      const idIn = h('input', { type: 'text', class: 'mono', value: autoModelID(it.id, ptype(), it.kind, taken) });
      const hintEl = h('div', { class: 'warn-text' });
      const syncHint = () => { hintEl.textContent = canvasHint(idIn.value.trim(), it.kind); };
      idIn.addEventListener('input', syncHint);
      syncHint();
      const fieldEl = h('div', { class: 'mt4' },
        h('label', { class: 'lbl', text: '模型 ID（可修改）' }), idIn, hintEl);
      sel.set(it.id, { item: it, idIn, fieldEl });
    } else {
      sel.delete(it.id);
    }
    renderRows();
  }

  function renderRows() {
    listEl.textContent = '';
    for (const it of items) {
      const added = isAdded(it.id);
      const cb = h('input', {
        type: 'checkbox', disabled: added, checked: sel.has(it.id),
        onchange: () => toggle(it, cb.checked),
      });
      const idBox = h('div');
      const s = sel.get(it.id);
      if (s) idBox.append(s.fieldEl);
      listEl.append(h('div', { class: 'acct' }, cb,
        h('span', { class: 'grow' },
          h('div', null,
            h('span', { text: it.name || it.id }),
            ' ',
            it.deprecated ? pill('已弃用', 'warn') : null),
          h('div', { class: 'muted mono fs12', text: it.id }),
          idBox),
        it.kind ? pill(KINDS[it.kind] || it.kind, 'acc') : null,
        added ? h('span', { class: 'muted fs12', text: '已添加' }) : null));
    }
    if (!items.length) listEl.append(h('div', { class: 'empty', text: '没有匹配的模型' }));
    moreBox.textContent = '';
    if (nextCursor) {
      moreBox.append(h('button', { class: 'btn', text: '加载更多', onclick: () => search(false) }));
    }
    updateCount();
  }

  async function search(reset) {
    if (loading) return;
    loading = true;
    err.textContent = '';
    if (reset) { items = []; nextCursor = ''; }
    try {
      const params = new URLSearchParams();
      if (qIn.value.trim()) params.set('q', qIn.value.trim());
      if (ptype() === 'fal') params.set('category', catSel.value);
      if (!reset && nextCursor) params.set('cursor', nextCursor);
      const res = await admin(`/admin/providers/${provSel.value}/upstream-models?${params}`);
      const seen = new Set(items.map(i => i.id));
      for (const it of res.data || []) {
        if (!seen.has(it.id)) { items.push(it); seen.add(it.id); }
      }
      nextCursor = res.next_cursor || '';
      renderRows();
    } catch (e) {
      err.textContent = errMsg(e);
    }
    loading = false;
  }

  // fal sibling endpoints: collect candidates, check existence in one
  // find-mode call per ≤50 ids, then map to upstream_* options.
  async function falLinks(pid) {
    const cand = new Map(); // endpoint id -> [{up, role}]
    const add = (ep, up, role) => {
      if (!cand.has(ep)) cand.set(ep, []);
      cand.get(ep).push({ up, role });
    };
    for (const s of sel.values()) {
      const id = s.item.id;
      if (s.item.kind === 'video' && id.endsWith('/text-to-video')) {
        const P = id.slice(0, -'/text-to-video'.length);
        add(`${P}/image-to-video`, id, 'upstream_image');
        add(`${P}/reference-to-video`, id, 'upstream_reference');
      }
      if (s.item.kind === 'image') {
        add(`${id}/edit`, id, 'upstream_edit');
        if (id.endsWith('/text-to-image')) {
          const P = id.slice(0, -'/text-to-image'.length);
          add(`${P}/edit`, id, 'upstream_edit');
          add(`${P}/image-to-image`, id, 'upstream_edit');
        }
      }
    }
    const links = {};
    if (!cand.size) return links;
    const ids = [...cand.keys()];
    const found = new Set();
    for (let i = 0; i < ids.length; i += 50) {
      const params = new URLSearchParams();
      for (const ep of ids.slice(i, i + 50)) params.append('endpoint_id', ep);
      const res = await admin(`/admin/providers/${pid}/upstream-models?${params}`);
      for (const m of res.data || []) found.add(m.id);
    }
    for (const [ep, roles] of cand) {
      if (!found.has(ep)) continue;
      for (const { up, role } of roles) {
        const o = links[up] = links[up] || {};
        if (!o[role]) o[role] = ep;
      }
    }
    return links;
  }

  async function importSelected() {
    err.textContent = '';
    const pid = provSel.value;
    let links = {};
    let linksFailed = false;
    if (ptype() === 'fal') {
      try {
        links = await falLinks(pid);
      } catch {
        linksFailed = true; // 继续按单个端点导入
      }
    }
    const failed = [];
    for (const s of [...sel.values()]) {
      const body = {
        id: s.idIn.value.trim(), provider_id: pid,
        kind: s.item.kind, upstream: s.item.id, enabled: true,
      };
      const opts = links[s.item.id];
      if (opts && Object.keys(opts).length) body.options = opts;
      try {
        await admin('/admin/models', { method: 'POST', json: body });
        imported.add(s.item.id);
        sel.delete(s.item.id);
      } catch (e) {
        failed.push({ s, err: e });
      }
    }
    reload();
    renderRows();
    if (linksFailed) toast('未能检测同系列端点，已按单个端点导入', 'bad');
    if (failed.length) {
      err.textContent = `${failed.length} 个失败：${errMsg(failed[0].err)}`;
      return;
    }
    close();
    toast('已添加', 'ok');
  }

  const close = openSheet('从平台获取',
    h('div', null,
      field('平台', provSel),
      catField,
      h('div', { class: 'flex-row' },
        h('div', { class: 'flex-1' }, qIn),
        h('button', { class: 'btn', text: '搜索', onclick: () => search(true) })),
      listEl,
      moreBox,
      err,
      h('div', { class: 'row' },
        h('button', { class: 'btn', text: '关闭', onclick: () => close() }),
        countBtn)));

  // hidden attr keeps the category field collapsible without inline styles.
  const syncCat = () => { catField.hidden = ptype() !== 'fal'; };
  provSel.onchange = () => { sel.clear(); syncCat(); search(true); };
  catSel.onchange = () => search(true);
  qIn.onkeydown = e => { if (e.key === 'Enter') search(true); };
  countBtn.onclick = importSelected;
  syncCat();
  // Default the platform to the first provider with an enabled account,
  // else the first fal one, else the first — via a single /admin/status call.
  (async () => {
    try {
      const res = await admin('/admin/status');
      const list = res.providers || [];
      const pref = list.find(p => (p.accounts || []).some(a => a.enabled))
        || list.find(p => p.type === 'fal') || list[0];
      if (pref && [...provSel.options].some(o => o.value === pref.id)) provSel.value = pref.id;
    } catch { /* keep the first option */ }
    syncCat();
    search(true);
  })();
}
