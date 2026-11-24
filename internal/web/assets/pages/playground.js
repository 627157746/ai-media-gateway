import { v1, getGwKey, saveGwKey } from '../api.js';
import { h, toast, errMsg, field } from '../ui.js';

const KIND_LABEL = { image: '图片', video: '视频', speech: '语音' };

export function mountPlayground(root) {
  let stopPoll = () => {};
  let models = [];

  // ---- gateway key bar ----
  const keyIn = h('input', { type: 'password', value: getGwKey(), placeholder: 'sk-mg-…' });
  const keyBar = h('div', { class: 'toolbar' },
    h('span', { class: 'muted', text: '网关密钥' }),
    h('div', { class: 'w320' }, keyIn),
    h('button', { class: 'btn sm primary', text: '保存', onclick: () => { saveGwKey(keyIn.value.trim()); toast('已保存试用密钥', 'ok'); loadModels(); } }),
    h('a', { href: '#/keys', text: '去 API Key 页创建 ↗' }));

  const bodyEl = h('div');
  root.append(
    h('div', { class: 'banner', text: '试用会真实调用供应商并产生费用' }),
    keyBar,
    bodyEl);

  async function loadModels() {
    if (!getGwKey()) { bodyEl.textContent = ''; bodyEl.append(h('div', { class: 'empty', text: '请先填写网关密钥（sk-mg-*）' })); return; }
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

  // ---- image ----
  function imagePanel(panel) {
    const model = modelSelect('image');
    const prompt = h('textarea', { rows: 3, placeholder: '提示词…' });
    const size = h('input', { type: 'text', placeholder: '1024x1024 或 auto' });
    const n = h('input', { type: 'number', value: 1, min: 1, max: 10 });
    const imgs = h('input', { type: 'file', multiple: '', accept: 'image/*' });
    const out = resultBox();
    const err = h('div', { class: 'err-text' });
    const btn = h('button', { class: 'btn primary', text: '生成图片' });
    btn.onclick = async () => {
      err.textContent = ''; out.textContent = '';
      if (!prompt.value.trim()) { err.textContent = '请输入提示词'; return; }
      btn.disabled = true;
      try {
        let data;
        if (imgs.files.length) {
          const fd = new FormData();
          fd.set('model', model.value); fd.set('prompt', prompt.value);
          if (size.value.trim()) fd.set('size', size.value.trim());
          fd.set('n', n.value || '1');
          for (const f of imgs.files) fd.append('image[]', f);
          data = await v1('/v1/images/edits', { method: 'POST', body: fd });
        } else {
          const body = { model: model.value, prompt: prompt.value, n: parseInt(n.value, 10) || 1 };
          if (size.value.trim()) body.size = size.value.trim();
          data = await v1('/v1/images/generations', { method: 'POST', json: body });
        }
        for (const item of data.data || []) {
          if (item.url) {
            out.append(h('div', null,
              h('img', { src: item.url, alt: '' }),
              h('div', null, h('a', { href: item.url, target: '_blank', rel: 'noopener', text: '打开原图 ↗' }))));
          } else if (item.b64_json) {
            out.append(h('img', { src: `data:image/png;base64,${item.b64_json}`, alt: '' }));
          }
        }
        if (!(data.data || []).length) out.append(h('div', { class: 'muted', text: '上游没有返回图片' }));
      } catch (e) { err.textContent = errMsg(e); }
      finally { btn.disabled = false; }
    };
    panel.append(
      field('模型', model),
      field('提示词', prompt),
      h('div', { class: 'flex-row' },
        h('div', { class: 'flex-2' }, field('尺寸', size)),
        h('div', { class: 'flex-1' }, field('数量', n))),
      field('参考图（选填，选择后走图片编辑接口）', imgs),
      btn, err, out);
  }

  // ---- video ----
  function videoPanel(panel) {
    const model = modelSelect('video');
    const prompt = h('textarea', { rows: 3, placeholder: '提示词…' });
    const seconds = h('input', { type: 'text', placeholder: '5' });
    const size = h('input', { type: 'text', placeholder: '1280x720' });
    const ratio = h('input', { type: 'text', placeholder: '16:9' });
    const res = h('input', { type: 'text', placeholder: '720p' });
    const ff = h('input', { type: 'file', accept: 'image/*' });
    const lf = h('input', { type: 'file', accept: 'image/*' });
    const prog = h('div', { class: 'progress-line' });
    const out = resultBox();
    const err = h('div', { class: 'err-text' });
    const btn = h('button', { class: 'btn primary', text: '提交视频任务' });
    let pollTimer = null;
    const stop = () => { if (pollTimer) { clearInterval(pollTimer); pollTimer = null; } };
    const prev = stopPoll;
    stopPoll = () => { prev(); stop(); };

    btn.onclick = async () => {
      err.textContent = ''; out.textContent = ''; prog.textContent = '';
      if (!prompt.value.trim()) { err.textContent = '请输入提示词'; return; }
      btn.disabled = true; stop();
      try {
        const fd = new FormData();
        fd.set('model', model.value); fd.set('prompt', prompt.value);
        if (seconds.value.trim()) fd.set('seconds', seconds.value.trim());
        if (size.value.trim()) fd.set('size', size.value.trim());
        if (ratio.value.trim()) fd.set('ratio', ratio.value.trim());
        if (res.value.trim()) fd.set('resolution_name', res.value.trim());
        if (ff.files[0]) fd.set('first_frame', ff.files[0]);
        if (lf.files[0]) fd.set('last_frame', lf.files[0]);
        const task = await v1('/v1/videos', { method: 'POST', body: fd });
        prog.textContent = `任务 ${task.id}: ${task.status} ${task.progress ?? 0}%`;
        pollTimer = setInterval(async () => {
          try {
            const t = await v1(`/v1/videos/${task.id}`);
            prog.textContent = `任务 ${t.id}: ${t.status} ${t.progress ?? 0}%`;
            if (t.status === 'completed' && t.video_url) {
              stop();
              // same-origin /content proxy: never send the gateway key to the vendor CDN
              const r = await fetch(`/v1/videos/${encodeURIComponent(t.id)}/content`, { headers: { Authorization: `Bearer ${getGwKey()}` } });
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
      h('div', { class: 'flex-row' },
        h('div', { class: 'flex-1' }, field('时长(秒)', seconds)),
        h('div', { class: 'flex-1' }, field('尺寸', size)),
        h('div', { class: 'flex-1' }, field('比例', ratio)),
        h('div', { class: 'flex-1' }, field('分辨率名', res))),
      h('div', { class: 'flex-row' },
        h('div', { class: 'flex-1' }, field('首帧（选填）', ff)),
        h('div', { class: 'flex-1' }, field('尾帧（选填）', lf))),
      btn, prog, err, out);
  }

  // ---- speech ----
  function speechPanel(panel) {
    const model = modelSelect('speech');
    const input = h('textarea', { rows: 3, placeholder: '要合成的文本…' });
    const voice = h('input', { type: 'text', placeholder: 'voice（可选，模型可配别名/默认）' });
    const fmt = h('select', {},
      ['mp3', 'opus', 'wav', 'pcm'].map(f => h('option', { value: f, text: f })));
    const speed = h('input', { type: 'number', value: '', step: '0.05', placeholder: '1.0' });
    const out = resultBox();
    const err = h('div', { class: 'err-text' });
    const btn = h('button', { class: 'btn primary', text: '合成语音' });
    btn.onclick = async () => {
      err.textContent = ''; out.textContent = '';
      if (!input.value.trim()) { err.textContent = '请输入文本'; return; }
      btn.disabled = true;
      try {
        const body = { model: model.value, input: input.value, response_format: fmt.value };
        if (voice.value.trim()) body.voice = voice.value.trim();
        const sp = parseFloat(speed.value);
        if (sp > 0) body.speed = sp;
        const r = await fetch('/v1/audio/speech', {
          method: 'POST',
          headers: { Authorization: `Bearer ${getGwKey()}`, 'Content-Type': 'application/json' },
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
        h('div', { class: 'flex-2' }, field('Voice', voice)),
        h('div', { class: 'flex-1' }, field('格式', fmt)),
        h('div', { class: 'flex-1' }, field('语速', speed))),
      btn, err, out);
  }

  loadModels();
  return () => stopPoll();
}
