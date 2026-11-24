import { admin } from '../api.js';
import { h, switchEl, toast, errMsg, fmtTime, openSheet, isSheetOpen } from '../ui.js';

const RANGES = [['3600000', '近 1 小时'], ['86400000', '近 24 小时'], ['604800000', '近 7 天'], ['', '全部']];

function statusClass(code) {
  if (code >= 500) return 'bad';
  if (code >= 400) return 'warn';
  return 'ok';
}

// Param labels + explanations shown in the detail sheet, in display order.
const PARAM_INFO = [
  ['prompt', ['提示词', '描述要生成内容的文字']],
  ['n', ['数量', '一次生成几张图']],
  ['size', ['尺寸', '输出的宽x高（如 1024x1024）或比例']],
  ['quality', ['质量', '画质档位，如 low / medium / high / auto']],
  ['background', ['背景', 'transparent 透明 / opaque 不透明 / auto']],
  ['resolution', ['分辨率', '输出清晰度，如 720p、1080p、2K']],
  ['response_format', ['返回格式', 'url 返回链接，b64_json 返回内嵌图片数据']],
  ['edit', ['编辑模式', '带参考图改图（/v1/images/edits）']],
  ['images', ['参考图', '改图时上传的原图']],
  ['mask', ['蒙版', '标出允许修改的区域']],
  ['mode', ['视频模式', 'frames 首尾帧生成，reference 全能参考']],
  ['seconds', ['时长', '视频秒数']],
  ['ratio', ['画面比例', '如 16:9、9:16、1:1']],
  ['generate_audio', ['生成音频', '是否同时生成配音和音效']],
  ['watermark', ['水印', '是否带平台水印']],
  ['first_frame', ['首帧图', '视频第一帧']],
  ['last_frame', ['尾帧图', '视频最后一帧']],
  ['references', ['参考素材图', '全能参考模式的参考图']],
  ['videos', ['参考视频', '']],
  ['audios', ['参考音频', '']],
  ['input', ['朗读文本', '要合成语音的文字']],
  ['voice', ['音色', '使用的声音（别名或上游音色 ID）']],
  ['format', ['音频格式', 'mp3 / wav / opus / pcm']],
  ['instructions', ['语气指令', '对说话风格的描述']],
  ['speed', ['语速', '1 为正常速度']],
  ['extra', ['其他参数', '客户端额外传入、原样透传给上游的字段']],
];
const PARAM_ORDER = new Map(PARAM_INFO.map(([k], i) => [k, i]));
const PARAM_LABEL = new Map(PARAM_INFO);

function fmtBytes(n) {
  if (n >= 1 << 20) return (n / (1 << 20)).toFixed(1) + ' MB';
  if (n >= 1024) return (n / 1024).toFixed(1) + ' KB';
  return n + ' B';
}

// One summarized media input: {name,mime,bytes} or {url}.
function inputText(o) {
  if (o.url) return `链接：${o.url}`;
  const bits = [];
  if (o.mime) bits.push(o.mime);
  if (o.bytes) bits.push(fmtBytes(o.bytes));
  const meta = bits.length ? `（${bits.join('，')}）` : '';
  return (o.name || '文件') + meta;
}

function paramValueEl(v) {
  if (v === true || v === false) return document.createTextNode(v ? '是' : '否');
  if (Array.isArray(v)) {
    return h('span', null,
      h('span', { text: `${v.length} 个：` }),
      v.map((x, i) => [
        i ? h('br') : null,
        x && typeof x === 'object' ? inputText(x) : String(x),
      ]));
  }
  if (v && typeof v === 'object') return document.createTextNode(inputText(v));
  return document.createTextNode(String(v));
}

function logDetail(l) {
  const infoRows = [
    ['时间', fmtTime(l.at)],
    ['接口', l.endpoint],
    ['模型', l.model_id || '—'],
    ['平台', l.provider_id || '—'],
    ['账号', l.account_id || '—'],
    ['尝试次数', String(l.attempts)],
    ['状态', String(l.status)],
    ['耗时', `${l.latency_ms} ms`],
    ['错误', l.error || '—'],
  ];
  const info = h('div', { class: 'kv-blocks' },
    infoRows.map(([k, v]) => h('div', { class: 'kv-block' },
      h('div', { class: 'kv-line' }, h('b', { text: k }), '：', v))));

  const paramsEl = h('div', { class: 'kv-blocks' });
  const params = l.params && typeof l.params === 'object' ? l.params : null;
  if (!params || !Object.keys(params).length) {
    paramsEl.append(h('div', { class: 'muted fs12', text: '该请求没有记录参数（旧日志、非生成类请求或请求解析失败）' }));
  } else {
    const keys = Object.keys(params).sort((a, b) =>
      (PARAM_ORDER.get(a) ?? 999) - (PARAM_ORDER.get(b) ?? 999));
    for (const k of keys) {
      const [label, desc] = PARAM_LABEL.get(k) || [k, ''];
      const v = params[k];
      const block = h('div', { class: 'kv-block' },
        h('div', { class: 'kv-line' }, h('b', { text: label }), '：', paramValueEl(v)),
        desc ? h('div', { class: 'muted fs12', text: desc }) : null);
      if (k === 'extra' && v && typeof v === 'object') {
        block.append(h('div', { class: 'mono pre-wrap kv-extra', text: JSON.stringify(v, null, 2) }));
      }
      paramsEl.append(block);
    }
  }

  openSheet(`请求详情 #${l.id}`, h('div', null,
    h('div', { class: 'lbl', text: '基本信息' }), info,
    h('div', { class: 'lbl', text: '请求参数' }), paramsEl));
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
  const timer = setInterval(() => { if (autoRef.on && !isSheetOpen()) load(); }, 5000);
  return () => clearInterval(timer);
}

function render(listEl, logs) {
  listEl.textContent = '';
  const table = h('table', null,
    h('thead', null, h('tr', null,
      ['', '时间', '接口', '模型', '平台', '账号', '尝试', '状态', '耗时', '错误'].map(t => h('th', { text: t })))),
    h('tbody'));
  for (const l of logs) {
    // Whole row opens the detail sheet; the 详情 button stays in the first
    // column so it is visible without scrolling the wide table.
    table.lastChild.append(h('tr', { class: 'row-click', onclick: () => logDetail(l) },
      h('td', null, h('button', { class: 'btn sm', text: '详情' })),
      h('td', { class: 'muted mono', text: fmtTime(l.at) }),
      h('td', { class: 'mono', text: l.endpoint }),
      h('td', { class: 'mono', text: l.model_id || '—' }),
      h('td', { text: l.provider_id || '—' }),
      h('td', { class: 'mono muted', text: l.account_id || '—' }),
      h('td', null, l.attempts > 1
        ? h('span', { class: 'pill warn', text: `${l.attempts} 次`, title: '发生故障切换' })
        : String(l.attempts)),
      h('td', null, h('span', { class: `status-num ${statusClass(l.status)}`, text: String(l.status) })),
      h('td', { class: 'muted', text: `${l.latency_ms} ms` }),
      h('td', { class: 'err-text cell-err', title: l.error || '', text: l.error || '' })));
  }
  if (!logs.length) table.lastChild.append(h('tr', null, h('td', { colspan: '10' }, h('div', { class: 'empty', text: '暂无日志' }))));
  listEl.append(h('div', { class: 'table-wrap' }, table));
}
