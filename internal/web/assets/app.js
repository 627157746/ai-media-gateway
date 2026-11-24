import { getToken, saveToken, clearToken, validateToken } from './api.js';
import { h, toast, errMsg } from './ui.js';
import { mountProviders } from './pages/providers.js';
import { mountModels } from './pages/models.js';
import { mountLogs } from './pages/logs.js';
import { mountTasks } from './pages/tasks.js';
import { mountKeys } from './pages/keys.js';
import { mountPlayground } from './pages/playground.js';
import { mountSettings } from './pages/settings.js';
import { mountVoices } from './pages/voices.js';

const NAV = [
  ['模型', [['#/providers', '平台'], ['#/models', '模型'], ['#/voices', '音色']]],
  ['网关', [['#/keys', 'API Key'], ['#/playground', '试用'], ['#/settings', '设置']]],
  ['洞察', [['#/logs', '请求日志'], ['#/tasks', '视频任务']]],
];

const ROUTES = {
  '#/providers': { title: '平台', desc: '上游平台及其 API Key 池：优先级、权重、冷却状态与故障转移。', mount: mountProviders },
  '#/models': { title: '模型', desc: '对外暴露的模型别名，绑定平台与上游端点。', mount: mountModels },
  '#/voices': { title: '音色', desc: '语音音色库：显示名 → 上游音色 ID，可从上游直接导入。客户端通过 /v1/audio/voices 读取。', mount: mountVoices },
  '#/keys': { title: 'API Key', desc: '客户端访问 /v1 接口所需的网关密钥（sk-mg-*）。', mount: mountKeys },
  '#/playground': { title: '试用', desc: '直接调用 /v1 接口验证模型配置。', mount: mountPlayground },
  '#/settings': { title: '设置', desc: '全局网关设置：代理等。', mount: mountSettings },
  '#/logs': { title: '请求日志', desc: '每一次 /v1 请求的模型、账号、重试与结果。', mount: mountLogs },
  '#/tasks': { title: '视频任务', desc: '异步视频生成任务及其进度。', mount: mountTasks },
};

let cleanup = null;

// ---- theme ----
const THEME_KEY = 'mgw_theme';
function applyTheme(mode) {
  if (mode === 'auto') document.documentElement.removeAttribute('data-theme');
  else document.documentElement.setAttribute('data-theme', mode);
}
applyTheme(localStorage.getItem(THEME_KEY) || 'auto');

function themeSelect() {
  const sel = h('select', {
    onchange: e => { localStorage.setItem(THEME_KEY, e.target.value); applyTheme(e.target.value); },
    class: 'w-auto',
  },
    h('option', { value: 'auto', text: '自动' }),
    h('option', { value: 'light', text: '浅色' }),
    h('option', { value: 'dark', text: '深色' }));
  sel.value = localStorage.getItem(THEME_KEY) || 'auto';
  return sel;
}

// ---- login ----

function renderLogin() {
  const tokenIn = h('input', { type: 'password', placeholder: 'ADMIN_TOKEN', autofocus: '' });
  const remember = h('input', { type: 'checkbox', checked: true });
  const err = h('div', { class: 'err-text' });
  const btn = h('button', { class: 'btn primary', text: '登 录' });
  const submit = async () => {
    err.textContent = '';
    const t = tokenIn.value.trim();
    if (!t) { err.textContent = '请输入管理令牌'; return; }
    btn.disabled = true;
    try {
      await validateToken(t);
      saveToken(t, remember.checked);
      renderApp();
    } catch (e) {
      err.textContent = e.status === 401 ? '令牌无效' : `无法连接: ${errMsg(e)}`;
    } finally { btn.disabled = false; }
  };
  btn.onclick = submit;
  tokenIn.addEventListener('keydown', e => { if (e.key === 'Enter') submit(); });
  app.append(h('div', { class: 'login-wrap' },
    h('div', { class: 'login-card' },
      h('h1', { text: 'AI Media Gateway' }),
      h('div', { class: 'sub', text: '管理台登录 — 请输入管理令牌 (ADMIN_TOKEN)' }),
      h('div', { class: 'field' }, tokenIn),
      h('label', { class: 'check' }, remember, ' 记住登录'),
      h('div', { class: 'row mt16' }, btn),
      err)));
}

// ---- app shell ----

const app = document.getElementById('app');

function route() {
  const hash = location.hash || '#/providers';
  return ROUTES[hash] ? hash : '#/providers';
}

function renderApp() {
  if (cleanup) { cleanup(); cleanup = null; }
  app.textContent = '';
  const cur = route();
  const nav = h('div', { class: 'side' },
    h('div', { class: 'brand' },
      h('img', { src: '/assets/favicon.svg', alt: '' }),
      'Gateway'),
    NAV.map(([group, items]) =>
      h('div', { class: 'nav-group' },
        h('div', { class: 'nav-title', text: group }),
        items.map(([hash, label]) =>
          h('a', { class: `nav-item${hash === cur ? ' active' : ''}`, href: hash, text: label })))));
  const content = h('div', { class: 'content' },
    h('h1', { text: ROUTES[cur].title }),
    h('p', { class: 'page-desc', text: ROUTES[cur].desc }));
  const topbar = h('div', { class: 'topbar' },
    themeSelect(),
    h('button', { class: 'btn sm', text: '退出登录', onclick: () => { clearToken(); location.hash = ''; boot(); } }));
  app.append(
    h('div', { class: 'layout' },
      nav,
      h('div', { class: 'main' }, topbar, content)));
  cleanup = ROUTES[cur].mount(content) || null;
}

function boot() {
  app.textContent = '';
  if (!getToken()) { renderLogin(); return; }
  renderApp();
}

window.addEventListener('hashchange', () => {
  if (!getToken()) { renderLogin(); return; }
  renderApp();
});

boot();
