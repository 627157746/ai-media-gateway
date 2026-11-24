import { v1, getToken } from '../api.js';
import { h, errMsg, field } from '../ui.js';

const KIND_LABEL = { image: '图片', video: '视频', speech: '语音' };

export function mountPlayground(root) {
  let stopPoll = () => {};
  let models = [];

  const bodyEl = h('div');
  root.append(
    h('div', { class: 'banner', text: '试用会真实调用平台并产生费用' }),
    bodyEl);

  async function loadModels() {
    try {
      const data = await v1('/v1/models');
      models = data.data || [];
      renderTabs();
    } catch (e) {
      bodyEl.textContent = '';
      bodyEl.append(h('div', { class: 'err-text', text: `加载模型失败: ${errMsg(e)}` }));
    }
  }

  let curTab = 'image';
  function renderTabs() {
    stopPoll();
    bodyEl.textContent = '';
    const tabs = h('div', { class: 'tabs' },
      Object.entries(KIND_LABEL).map(([k, label]) =>
        h('button', { class: `tab${k === curTab ? ' active' : ''}`, text: label, onclick: () => { curTab = k; renderTabs(); } })));
    bodyEl.append(tabs);
    const panel = h('div');
    bodyEl.append(panel);
    if (curTab === 'image') imagePanel(panel);
    else if (curTab === 'video') videoPanel(panel);
    else speechPanel(panel);
  }

  function modelSelect(kind) {
    const sel = h('select', {},
      models.filter(m => m.kind === kind).map(m => h('option', { value: m.id, text: `${m.id} (${m.owned_by})` })));
    if (!sel.children.length) sel.append(h('option', { value: '', text: `没有${KIND_LABEL[kind]}模型` }));
    return sel;
  }

  function resultBox() { return h('div', { class: 'result-media mt14' }); }

// Vendors name their parameters in English, which the admin UI has no reason to
// expose raw. These maps turn the vendor's enum values into Chinese labels while
// the request keeps sending the original value.
const VALUE_LABELS = {
  // fal's image_size presets describe a SHAPE, so the label is the aspect ratio
  // itself. The two square presets both mean 1:1 and differ only in resolution,
  // so they get a 大/小 hint; no pixel count is shown because the vendor
  // documents the ratio but not the size per preset.
  size: {
    auto: '自动',
    square_hd: '1:1（大）',
    square: '1:1（小）',
    portrait_4_3: '3:4',
    portrait_16_9: '9:16',
    landscape_4_3: '4:3',
    landscape_16_9: '16:9',
    // Some vendors encode the resolution TIER inside image_size instead of
    // exposing a separate resolution field, so 1K/1.5K/2K appear here.
    auto_1K: '自动 · 1K',
    'auto_1.5K': '自动 · 1.5K',
    auto_2K: '自动 · 2K',
    auto_4K: '自动 · 4K',
    '1024x1024': '1:1（1024×1024）',
    '1024x1536': '2:3（1024×1536）',
    '1536x1024': '3:2（1536×1024）',
  },
  quality: {
    auto: '自动', low: '低', medium: '中', high: '高',
    xhigh: '超高', max: '最高',
  },
  background: { auto: '自动', transparent: '透明', opaque: '不透明' },
  format: {
    png: 'PNG（无损）', jpg: 'JPG（有损）', jpeg: 'JPEG（有损）',
    webp: 'WebP（体积小）', gif: 'GIF（动图）', svg: 'SVG（矢量）',
  },
  resolution: {
    '1K': '1K', '1.5K': '1.5K', '2K': '2K', '4K': '4K',
    '480P': '480P 流畅', '720P': '720P 高清', '768P': '768P 高清',
    '1080P': '1080P 全高清',
  },
  ratio: {
    auto: '自动', adaptive: '自适应（跟随输入）',
    '16:9': '16:9', '9:16': '9:16', '4:3': '4:3',
    '3:4': '3:4', '1:1': '1:1', '21:9': '21:9', '9:21': '9:21',
  },
  duration: {},
};

// valueLabel decides how an option is displayed; the underlying value stays as
// the vendor expects, so nothing English leaks into the request.
function valueLabel(kind, v) {
  const m = VALUE_LABELS[kind] || {};
  return m[v] || v;
}

  // ---- image ----
  function imagePanel(panel) {
    const model = modelSelect('image');
    const prompt = h('textarea', { rows: 3, placeholder: '提示词…' });
    // Every control below is rendered from the model's capability declaration.
    const sizeSel = h('select');
    const nIn = h('input', { type: 'number', value: 1, min: 1, max: 10 });
    const qualitySel = h('select');
    const resolutionSel = h('select');
    const bgSel = h('select');
    const fmtSel = h('select');
    const imgs = h('input', { type: 'file', multiple: '', accept: 'image/*' });
    const capHint = h('div', { class: 'muted fs12' });
    const sizeRow = h('div', { class: 'flex-1' }, field('尺寸', sizeSel));
    const nRow = h('div', { class: 'flex-1' }, field('数量', nIn));
    const qualityRow = h('div', { class: 'flex-1' }, field('质量', qualitySel));
    const resolutionRow = h('div', { class: 'flex-1' }, field('分辨率', resolutionSel));
    const bgRow = h('div', { class: 'flex-1' }, field('背景', bgSel));
    const fmtRow = h('div', { class: 'flex-1' }, field('格式', fmtSel));
    const imgField = field('参考图', imgs, '');
    const imgRow = h('div', null);
    const out = resultBox();
    const err = h('div', { class: 'err-text' });
    const btn = h('button', { class: 'btn primary', text: '生成图片' });

    let cap = {};
    // fill populates a select from a capability list and marks the vendor's
    // real default, so "默认" is not a blind choice.
    const fill = (sel, values, kind, def) => {
      sel.textContent = '';
      // The first entry means "whatever the model defaults to". It used to be
      // labelled "默认：横版 4:3" while that same value was ALSO listed below,
      // so the dropdown showed the identical option twice and the user could not
      // tell which one was the real default.
      if (def) {
        sel.append(h('option', {
          value: '',
          text: `跟随模型默认（${valueLabel(kind, def)}）`,
        }));
      } else {
        sel.append(h('option', { value: '', text: '跟随模型默认' }));
      }
      for (const v of values) {
        const isDef = def && v === def;
        sel.append(h('option', {
          value: v,
          text: valueLabel(kind, v) + (isDef ? '（模型默认）' : ''),
        }));
      }
      sel.disabled = values.length === 0;
    };

    function refresh() {
      const m = models.find((x) => x.id === model.value);
      cap = (m && m.capabilities) || {};
      const defs = cap.defaults || {};
      fill(sizeSel, cap.sizes || [], 'size', defs.size);
      fill(qualitySel, cap.qualities || [], 'quality', defs.quality);
      fill(resolutionSel, cap.resolutions || [], 'resolution', defs.resolution);
      fill(bgSel, cap.backgrounds || [], 'background', defs.background);
      fill(fmtSel, cap.formats || [], 'format', defs.format);

      const maxN = cap.max_n || 0;
      nIn.disabled = maxN <= 0;
      nIn.max = maxN || 1;
      if (maxN <= 0) nIn.value = '';
      // A count left over from a model with a higher cap would be rejected.
      else if (Number(nIn.value) > maxN) nIn.value = String(maxN);
      nRow.style.display = maxN > 0 ? '' : 'none';
      sizeRow.style.display = (cap.sizes || []).length ? '' : 'none';
      qualityRow.style.display = (cap.qualities || []).length ? '' : 'none';
      resolutionRow.style.display = (cap.resolutions || []).length ? '' : 'none';
      bgRow.style.display = (cap.backgrounds || []).length ? '' : 'none';
      fmtRow.style.display = (cap.formats || []).length ? '' : 'none';
      imgRow.style.display = cap.supports_edit ? '' : 'none';
      // There is no "optional vs required" choice here: either the model can
      // only edit, in which case images are required, or it can also generate
      // from text, in which case the field is a mode switch. Labelling either
      // case 选填 contradicts the send path.
      const imgLabel = imgField.querySelector('label') || imgField.appendChild(h('label'));
      // field() only creates the hint element when given a non-empty string, so
      // it may not exist yet; create it on demand instead of assuming.
      let hintEl = imgField.querySelector('.hint');
      if (!hintEl) {
        hintEl = h('div', { class: 'hint' });
        imgField.append(hintEl);
      }
      imgLabel.textContent = '参考图';
      // first_image_is_source models (Muse) split the uploads: image 1 is what
      // gets edited, the rest are references. Other models treat every image as
      // a reference. The hint has to say which, or the same files produce
      // different results than the user expects.
      if (cap.requires_images) {
        hintEl.textContent = cap.first_image_is_source
          ? `该模型只能改图：第 1 张是待编辑的源图，第 2 张起最多 ${cap.max_edit_images} 张参考图`
          : `该模型只能改图：必须选${cap.max_edit_images ? ' 1–' + cap.max_edit_images : ''} 张参考图`;
      } else if (cap.first_image_is_source) {
        hintEl.textContent = cap.max_edit_images > 0
          ? `不选 = 文生图；选了 = 改图：第 1 张是源图，第 2 张起最多 ${cap.max_edit_images} 张参考图`
          : '不选 = 文生图；选了 = 改图：第 1 张是源图，其余是参考图';
      } else {
        hintEl.textContent = cap.max_edit_images > 0
          ? `不选 = 文生图；选了 = 改图（最多 ${cap.max_edit_images} 张参考图）`
          : '不选 = 文生图；选了 = 改图';
      }
      btn.textContent = cap.requires_images ? '编辑图片' : '生成图片';
      capHint.textContent = cap.size_mode === 'aspect_ratio'
        ? '该模型按比例输出，尺寸会被折算成最接近的比例'
        : '';
    }
    model.onchange = refresh;

    btn.onclick = async () => {
      err.textContent = ''; out.textContent = '';
      if (!prompt.value.trim()) { err.textContent = '请输入提示词'; return; }
      btn.disabled = true;
      try {
        const files = imgs.files ? [...imgs.files] : [];
        if (cap.requires_images && !files.length) {
          throw new Error('该模型只能改图，请先选择参考图');
        }
        const useEdit = cap.supports_edit && files.length > 0;
        if (useEdit && cap.max_edit_images > 0 && files.length > cap.max_edit_images) {
          throw new Error(`参考图最多 ${cap.max_edit_images} 张`);
        }
        if (useEdit) {
          const fd = new FormData();
          fd.set('model', model.value);
          fd.set('prompt', prompt.value);
          if (sizeSel.value) fd.set('size', sizeSel.value);
          if (nIn.value) fd.set('n', nIn.value);
          if (qualitySel.value) fd.set('quality', qualitySel.value);
          if (resolutionSel.value) fd.set('resolution', resolutionSel.value);
          if (bgSel.value) fd.set('background', bgSel.value);
          if (fmtSel.value) fd.set('response_format', fmtSel.value);
          for (const f of files) fd.append('image[]', f);
          const r = await fetch('/v1/images/edits', {
            method: 'POST',
            headers: { Authorization: `Bearer ${getToken()}` },
            body: fd,
          });
          if (!r.ok) {
            const j = await r.json().catch(() => null);
            throw Object.assign(new Error(j && j.error ? j.error.message : `HTTP ${r.status}`), { status: r.status });
          }
          renderImages(await r.json(), out);
          return;
        }
        const body = { model: model.value, prompt: prompt.value };
        if (sizeSel.value) body.size = sizeSel.value;
        if (nIn.value) body.n = parseInt(nIn.value, 10) || 1;
        if (qualitySel.value) body.quality = qualitySel.value;
        if (resolutionSel.value) body.resolution = resolutionSel.value;
        if (bgSel.value) body.background = bgSel.value;
        if (fmtSel.value) body.response_format = fmtSel.value;
        const r = await v1('/v1/images/generations', { method: 'POST', json: body });
        renderImages(r, out);
      } catch (e) { err.textContent = errMsg(e); }
      finally { btn.disabled = false; }
    };

    // Initial text only; refresh() rewrites both label and hint per model.
    imgRow.append(imgField);
    panel.append(
      field('模型', model),
      field('提示词', prompt),
      capHint,
      h('div', { class: 'flex-row' }, sizeRow, nRow),
      h('div', { class: 'flex-row' }, qualityRow, resolutionRow),
      h('div', { class: 'flex-row' }, bgRow, fmtRow),
      imgRow,
      btn, err, out);
    refresh();
  }

  function renderImages(data, out) {
    for (const item of data.data || []) {
      const src = item.b64_json
        ? `data:image/png;base64,${item.b64_json}`
        : item.url;
      if (src) out.append(h('img', { src, style: 'max-width:100%;border-radius:8px;margin-top:8px' }));
    }
  }

  // ---- video ----
  function videoPanel(panel) {
    const model = modelSelect('video');
    const prompt = h('textarea', { rows: 3, placeholder: '提示词：描述画面与运动…' });
    // Everything below is rendered from the model's declared capabilities.
    const modeSel = h('select');
    const durSel = h('select');
    const ratioSel = h('select');
    const resSel = h('select');
    const ff = h('input', { type: 'file', accept: 'image/*' });
    const lf = h('input', { type: 'file', accept: 'image/*' });
    const refs = h('input', { type: 'file', accept: 'image/*', multiple: '' });
    const refVids = h('input', { type: 'file', accept: 'video/*', multiple: '' });
    const refAuds = h('input', { type: 'file', accept: 'audio/*', multiple: '' });
    const modeRow = h('div', { class: 'flex-1' }, field('模式', modeSel));
    const durRow = h('div', { class: 'flex-1' }, field('时长(秒)', durSel));
    const ratioRow = h('div', { class: 'flex-1' }, field('比例', ratioSel));
    const resRow = h('div', { class: 'flex-1' }, field('分辨率', resSel));
    const ffRow = h('div', { class: 'flex-1' }, field('首帧图', ff));
    const lfRow = h('div', { class: 'flex-1' }, field('尾帧图（选填）', lf));
    const refsRow = h('div', { class: 'flex-1' }, field('参考图', refs));
    const refVidsRow = h('div', { class: 'flex-1' }, field('参考视频', refVids));
    const refAudsRow = h('div', { class: 'flex-1' }, field('参考音频', refAuds));
    const capHint = h('div', { class: 'muted fs12' });
    const prog = h('div', { class: 'progress-line' });
    const out = resultBox();
    const err = h('div', { class: 'err-text' });
    const btn = h('button', { class: 'btn primary', text: '提交视频任务' });
    let pollTimer = null;
    let cap = {};
    const stop = () => { if (pollTimer) { clearInterval(pollTimer); pollTimer = null; } };
    const prev = stopPoll;
    stopPoll = () => { prev(); stop(); };

    // Display names follow fal's own endpoint suffixes. "frames" is only an
    // internal id; the vendor calls the endpoint image-to-video, and the UI has
    // no reason to invent a different term.
    const MODE_LABEL = { frames: '图生视频（image-to-video）', reference: '参考生视频（reference-to-video）' };
    const fmtOpt = (sel, values, kind, def) => {
      sel.textContent = '';
      sel.append(h('option', { value: '', text: def ? `默认：${valueLabel(kind, def)}` : '默认' }));
      for (const v of values) sel.append(h('option', { value: v, text: valueLabel(kind, v) }));
      sel.disabled = values.length === 0;
    };

    // The selected mode lives in its own variable. refresh() rebuilds the
    // <select>, which would reset its value and make switching modes impossible.
    let currentMode = '';
    const mode = () => currentMode || (cap.modes || [])[0] || '';

    function refresh() {
      const m = models.find((x) => x.id === model.value);
      cap = (m && m.capabilities) || {};
      const modes = cap.modes || [];
      // Keep the user's choice when it is still valid for this model.
      if (!modes.includes(currentMode)) {
        currentMode = modes[0] || '';
      }
      modeSel.textContent = '';
      for (const v of modes) modeSel.append(h('option', { value: v, text: MODE_LABEL[v] || v }));
      if (!modes.length) modeSel.append(h('option', { value: '', text: '该模型无可声明模式' }));
      modeSel.value = currentMode;
      modeSel.disabled = modes.length <= 1;
      modeRow.style.display = modes.length > 1 ? '' : 'none';

      fmtOpt(durSel, cap.durations || [], 'duration', cap.duration_default);
      if (!(cap.durations || []).length) {
        // The vendor either published a numeric range (Alibaba: 2-30s) or
        // nothing at all. Only offer values inside the documented range; the
        // old hardcoded 5/10 would have sent 10 to a model that caps at 6.
        durSel.textContent = '';
        durSel.append(h('option', {
          value: '',
          text: cap.duration_default ? `默认：${cap.duration_default} 秒` : '默认（由模型决定）',
        }));
        const lo = cap.duration_min || 0;
        const hi = cap.duration_max || 0;
        if (hi > 0) {
          for (let v = Math.max(lo, 1); v <= hi; v++) {
            durSel.append(h('option', { value: String(v), text: `${v} 秒` }));
          }
        } else if (lo > 0) {
          for (const v of [lo, lo + 1, lo + 2, 10, 15]) {
            if (v >= lo) durSel.append(h('option', { value: String(v), text: `${v} 秒` }));
          }
        }
        durSel.disabled = false;
      }
      fmtOpt(ratioSel, cap.ratios || [], 'ratio', cap.ratio_default);
      fmtOpt(resSel, cap.resolutions || [], 'resolution', cap.resolution_default);

      // A published range (duration_min/max) with no default still has options.
      durRow.style.display = (cap.durations || []).length || cap.duration_default
        || cap.duration_min || cap.duration_max ? '' : 'none';
      ratioRow.style.display = (cap.ratios || []).length ? '' : 'none';
      resRow.style.display = (cap.resolutions || []).length ? '' : 'none';

      // Visibility is per MODE, not per capability: a model can support both
      // modes, but a given request uses one of them. Showing the reference
      // uploads while image-to-video is selected invites the user to attach
      // files that the request would never send.
      // Visibility follows two independent rules:
      //   - The MODEL's capability decides whether an input exists at all
      //     (cap.supports_*, cap.max_*).
      //   - Only when the model has BOTH modes does the selected mode further
      //     narrow which inputs apply, because one request uses one mode.
      // A single-mode model (Muse) has no reference MODE but still accepts
      // reference IMAGES, so its reference row must stay visible — the earlier
      // condition hid it, which made a supported input unreachable.
      const allModes = cap.modes || [];
      const hasFramesMode = allModes.includes('frames');
      const hasRefMode = allModes.includes('reference');
      const isRef = mode() === 'reference';
      // A model with NO declared mode (unrecognised path suffix) still shows
      // the inputs its capability declares; otherwise a requires_images model
      // without a mode could never be submitted.
      const frameOk = (hasFramesMode || !allModes.length) && !(hasRefMode && isRef);
      const refMediaOk = hasRefMode ? isRef : !allModes.length;
      const refOk = cap.max_references
        ? (hasRefMode ? isRef : true)
        : false;

      ffRow.style.display = frameOk && cap.supports_first_frame ? '' : 'none';
      lfRow.style.display = frameOk && cap.supports_last_frame ? '' : 'none';
      refsRow.style.display = refOk ? '' : 'none';
      refVidsRow.style.display = refMediaOk && cap.max_reference_videos ? '' : 'none';
      refAudsRow.style.display = refMediaOk && cap.max_reference_audios ? '' : 'none';
      // The hint must be REWRITTEN on every refresh, not only created once:
      // switching models changes the cap (h3-max allows 3 reference videos, Muse
      // allows 6 reference images), and a create-once hint kept showing the
      // previous model's number.
      const capText = (v) => (v > 0 ? `最多 ${v} 个` : v < 0 ? '数量不限' : '');
      const setHint = (row, value) => {
        if (!value) return;
        let h0 = row.querySelector('.hint');
        if (!h0) {
          h0 = h('div', { class: 'hint' });
          row.append(h0);
        }
        h0.textContent = capText(value);
      };
      setHint(refsRow, cap.max_references);
      setHint(refVidsRow, cap.max_reference_videos);
      setHint(refAudsRow, cap.max_reference_audios);

      const durHint = durRow.querySelector('.hint');
      if (!durHint && (cap.duration_min || cap.duration_max)) {
        durRow.append(h('div', { class: 'hint' }));
      }
      const dh = durRow.querySelector('.hint');
      if (dh) {
        // Vendors often publish only one bound (MiniMax caps at 15s with no
        // minimum), so each case gets its own wording instead of "?–15".
        const lo = cap.duration_min || 0;
        const hi = cap.duration_max || 0;
        if (lo && hi) dh.textContent = `可选 ${lo}–${hi} 秒`;
        else if (hi) dh.textContent = `最长 ${hi} 秒`;
        else if (lo) dh.textContent = `至少 ${lo} 秒`;
        else dh.textContent = '';
      }
      capHint.textContent = modes.length === 2
        ? (isRef
          ? '参考生视频：画面由提示词生成，参考素材只约束人物/风格/场景'
          : '图生视频：以首帧图为起点生成，可选尾帧图指定结束画面')
        : '';
    }
    model.onchange = () => { currentMode = ''; refresh(); };
    // Switching mode changes which inputs apply, so the form has to re-render.
    modeSel.onchange = () => { currentMode = modeSel.value; refresh(); };

    btn.onclick = async () => {
      err.textContent = ''; out.textContent = ''; prog.textContent = '';
      if (!prompt.value.trim()) { err.textContent = '请输入提示词'; return; }
      btn.disabled = true; stop();
      try {
        const md = mode();
        // A first frame is optional on vendors that also do text-to-video (Muse
        // renders from the prompt alone), so only a model that CANNOT work
        // without one is guarded here. Anything else is left to the vendor, whose
        // message is authoritative.
        // Only inputs whose row is visible belong to this request. A file picked
        // before switching mode or model stays in its hidden <input>, and sending
        // it would silently change the request (the server folds a first frame
        // into the references in reference mode).
        const shown = (row) => row.style.display !== 'none';
        const pick = (row, input) => (shown(row) ? [...(input.files || [])] : []);
        const ffFiles = pick(ffRow, ff);
        const lfFiles = pick(lfRow, lf);
        const refFiles = pick(refsRow, refs);
        const vidFiles = pick(refVidsRow, refVids);
        const audFiles = pick(refAudsRow, refAuds);
        const hasAnyInput = ffFiles.length || lfFiles.length || refFiles.length;
        if (cap.requires_images && !hasAnyInput) {
          throw new Error('该模型需要至少一张图片');
        }
        const fd = new FormData();
        fd.set('model', model.value);
        fd.set('prompt', prompt.value);
        if (md) fd.set('mode', md);
        if (durSel.value) fd.set('seconds', durSel.value);
        if (ratioSel.value) fd.set('ratio', ratioSel.value);
        if (resSel.value) fd.set('resolution', resSel.value);
        // Send whatever the visible inputs contain. A model may combine a first
        // frame WITH reference images (Muse allows both), so the two are not
        // mutually exclusive.
        if (ffFiles[0]) fd.set('first_frame', ffFiles[0]);
        if (lfFiles[0]) fd.set('last_frame', lfFiles[0]);
        for (const f of refFiles) fd.append('image[]', f);
        for (const f of vidFiles) fd.append('video[]', f);
        for (const f of audFiles) fd.append('audio[]', f);
        const task = await v1('/v1/videos', { method: 'POST', body: fd });
        prog.textContent = `任务 ${task.id}: ${task.status} ${task.progress ?? 0}%`;
        pollTimer = setInterval(async () => {
          try {
            const t = await v1(`/v1/videos/${task.id}`);
            prog.textContent = `任务 ${t.id}: ${t.status} ${t.progress ?? 0}%`;
            if (t.status === 'completed' && t.video_url) {
              stop();
              // same-origin /content proxy: never send the gateway key to the vendor CDN
              const r = await fetch(`/v1/videos/${encodeURIComponent(t.id)}/content`, { headers: { Authorization: `Bearer ${getToken()}` } });
              if (!r.ok) throw new Error(`下载视频失败 HTTP ${r.status}`);
              const url = URL.createObjectURL(await r.blob());
              out.append(h('video', { controls: '', src: url }),
                h('div', null, h('a', { href: url, target: '_blank', rel: 'noopener', text: '打开视频 ↗' })));
            } else if (t.status === 'failed' || t.status === 'cancelled') {
              stop();
              err.textContent = t.error ? t.error.message || '任务失败' : '任务失败';
            }
          } catch (e) { stop(); err.textContent = errMsg(e); }
        }, 3000);
      } catch (e) { err.textContent = errMsg(e); }
      finally { btn.disabled = false; }
    };
    panel.append(
      field('模型', model),
      field('提示词', prompt),
      capHint,
      modeRow,
      h('div', { class: 'flex-row' }, durRow, ratioRow, resRow),
      h('div', { class: 'flex-row' }, ffRow, lfRow),
      h('div', { class: 'flex-row' }, refsRow),
      h('div', { class: 'flex-row' }, refVidsRow, refAudsRow),
      btn, prog, err, out);
    refresh();
  }

  // ---- speech ----
  function speechPanel(panel) {
    const model = modelSelect('speech');
    const input = h('textarea', { rows: 3, placeholder: '要合成的文本…' });
    // Prefer the 音色库 dropdown; the free-text box stays for raw vendor ids.
    const voiceSel = h('select', {}, h('option', { value: '', text: '（默认音色）' }));
    const voiceManual = h('input', { type: 'text', placeholder: '或直接填上游音色 ID / 别名' });
    const voiceHint = h('div', { class: 'muted fs12' });
    const fmt = h('select', {},
      ['mp3', 'opus', 'wav', 'pcm'].map(f => h('option', { value: f, text: f })));
    const speed = h('input', { type: 'number', value: '', step: '0.05', placeholder: '1.0' });
    const lang = h('select', {}, h('option', { value: '', text: '自动判断' }));
    const langHint = h('div', { class: 'muted fs12', text: '' });
    const out = resultBox();
    const err = h('div', { class: 'err-text' });
    const btn = h('button', { class: 'btn primary', text: '合成语音' });

    const currentVoice = () => (voiceManual.value.trim() || voiceSel.value);

    // 音色库整栏与手动输入栏按 voice_mode 显示：预设名/描述模式下，音色库
    // 下拉没有任何合法选项，留着只会误导，所以整栏隐藏。
    const libraryCol = h('div', { class: 'flex-2' }, field('音色库', voiceSel), voiceHint);
    const manualRow = h('div', null, field('手动指定（优先）', voiceManual, '留空则用上面的音色库选项'));

    // Each model declares what it accepts in `voice`; render the matching
    // control instead of always offering the library.
    const PRESET_VOICES = {
      mimo: ['mimo_default', '冰糖', '茉莉', '苏打', '白桦', 'Mia', 'Chloe', 'Milo', 'Dean'],
    };
    let voiceMode = 'library';

    // Render every control from the server's capability declaration, so adding
    // a provider never requires touching this file.
    async function loadVoices() {
      voiceSel.textContent = '';
      voiceSel.append(h('option', { value: '', text: '（默认音色）' }));
      voiceManual.value = '';
      lang.textContent = '';
      lang.append(h('option', { value: '', text: '自动判断' }));
      const mid = model.value;
      if (!mid) { voiceHint.textContent = ''; return; }
      try {
        const r = await v1(`/v1/audio/voices?model=${encodeURIComponent(mid)}`);
        const cap = r.capabilities || {};
        const mode = cap.voice_mode || r.voice_mode || 'library';
        const list = r.data || [];

        // ---- voice ----
        const presets = cap.voice_presets || [];
        const defPreset = cap.default_preset || '';
        if (mode === 'preset' && presets.length) {
          voiceSel.append(h('option', { value: '', text: defPreset ? `（默认 ${defPreset}）` : '（默认音色）' }));
          for (const name of presets) voiceSel.append(h('option', { value: name, text: name }));
          libraryCol.style.display = '';
          manualRow.style.display = '';
          voiceHint.textContent = `该模型只接受上游预置音色，共 ${presets.length} 个`;
        } else if (mode === 'preset') {
          // No preset list: the vendor requires a raw voice id.
          libraryCol.style.display = 'none';
          manualRow.style.display = '';
          voiceManual.placeholder = cap.voice_required ? '必填：上游 voice_id' : '上游 voice_id（可留空）';
          voiceHint.textContent = cap.voice_required
            ? '该模型必须提供上游 voice_id（音色库不适用）'
            : '该模型接受上游 voice_id';
        } else if (mode === 'sample') {
          libraryCol.style.display = '';
          manualRow.style.display = 'none';
          for (const v of list) voiceSel.append(h('option', { value: v.id, text: `${v.name}（${v.provider_id}）` }));
          voiceManual.placeholder = '（零样本模型请从音色库选择参考样本）';
          voiceHint.textContent = list.length
            ? `零样本克隆：从音色库选 ${list.length} 个参考样本之一`
            : '零样本克隆需要参考样本，请先到「音色」页克隆一个';
        } else if (mode === 'prompt') {
          libraryCol.style.display = 'none';
          manualRow.style.display = '';
          voiceManual.placeholder = '音色描述，如 甜美可爱的二次元少女';
          voiceHint.textContent = '音色设计模型：填的是描述（提示词），不是 ID';
        } else if (mode === 'none') {
          libraryCol.style.display = 'none';
          manualRow.style.display = 'none';
          voiceManual.placeholder = '（该模型不使用 voice）';
          voiceHint.textContent = '该模型忽略 voice 参数';
        } else {
          libraryCol.style.display = '';
          manualRow.style.display = '';
          for (const v of list) voiceSel.append(h('option', { value: v.id, text: `${v.name}（${v.provider_id}）` }));
          voiceHint.textContent = list.length
            ? `音色库中该平台有 ${list.length} 个可用音色`
            : '音色库中该平台还没有音色，可在「音色」页导入或克隆';
        }

        // ---- formats ----
        const formats = cap.formats || [];
        if (formats.length) {
          const keep = fmt.value;
          fmt.textContent = '';
          for (const f of formats) fmt.append(h('option', { value: f, text: f }));
          if (formats.includes(keep)) fmt.value = keep;
        }

        // ---- speed ----
        const sp = cap.speed || {};
        const speedOff = sp.mode === 'none';
        speed.disabled = speedOff;
        speed.placeholder = speedOff ? '不支持' : (sp.min ? `${sp.min}–${sp.max}` : '1.0');
        if (speedOff) speed.value = '';

        // ---- language ----
        const lg = cap.language || {};
        const codes = r.languages || lg.inline || [];
        if (lg.supported && codes.length) {
          for (const c of codes) lang.append(h('option', { value: c, text: c }));
          lang.disabled = false;
        } else if (lg.supported) {
          // Free-form: the server accepts tags it cannot enumerate.
          lang.disabled = true;
          if (lg.note) langHint.textContent = lg.note;
        } else {
          lang.disabled = true;
          lang.textContent = '';
          lang.append(h('option', { value: '', text: '该模型不支持' }));
        }
        langHint.textContent = lg.supported
          ? (lg.rejects_regional ? '只接受语言代码，不要带地区（如用 en 而非 en-US）' : (lg.note || '留空由上游自动判断'))
          : '该模型没有 language 参数';
      } catch (e) {
        voiceHint.textContent = `能力加载失败：${errMsg(e)}`;
      }
    }

    model.onchange = loadVoices;
    loadVoices();

    btn.onclick = async () => {
      err.textContent = ''; out.textContent = '';
      if (!input.value.trim()) { err.textContent = '请输入文本'; return; }
      btn.disabled = true;
      try {
        const body = { model: model.value, input: input.value, response_format: fmt.value };
        const v = currentVoice();
        if (v) body.voice = v;
        const sp = parseFloat(speed.value);
        if (sp > 0) body.speed = sp;
        if (lang.value) body.language = lang.value;
        const r = await fetch('/v1/audio/speech', {
          method: 'POST',
          headers: { Authorization: `Bearer ${getToken()}`, 'Content-Type': 'application/json' },
          body: JSON.stringify(body),
        });
        if (!r.ok) {
          const j = await r.json().catch(() => null);
          throw Object.assign(new Error(j && j.error ? j.error.message : `HTTP ${r.status}`), { status: r.status });
        }
        const blob = await r.blob();
        const url = URL.createObjectURL(blob);
        out.append(
          h('audio', { controls: '', src: url }),
          h('div', null, h('a', { href: url, download: `speech.${fmt.value}`, text: '下载音频 ↓' })));
      } catch (e) { err.textContent = errMsg(e); }
      finally { btn.disabled = false; }
    };
    panel.append(
      field('模型', model),
      field('文本', input),
      h('div', { class: 'flex-row' },
        libraryCol,
        h('div', { class: 'flex-1' }, field('格式', fmt)),
        h('div', { class: 'flex-1' }, field('语速', speed))),
      h('div', { class: 'flex-row' },
        h('div', { class: 'flex-1' }, field('语言', lang), langHint)),
      manualRow,
      btn, err, out);
  }

  loadModels();
  return () => stopPoll();
}
