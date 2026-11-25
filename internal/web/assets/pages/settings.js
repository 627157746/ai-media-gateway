import { admin } from '../api.js';
import { h, field, proxyField, toast, errMsg, openSheet, sheetButtons, confirmDialog, invalidateProxies, switchEl } from '../ui.js';

export function mountSettings(root) {
  const err = h('div', { class: 'err-text' });
  const poolCard = h('div', { class: 'card' });
  const proxy = proxyField('', '跟随系统环境变量（默认）');
  const dryErr = h('div', { class: 'err-text' });
  const dryBox = h('span');
  function renderDry(on) {
    dryBox.textContent = '';
    dryBox.append(switchEl(on, async v => {
      dryErr.textContent = '';
      try {
        const s = await admin('/admin/settings', { method: 'PATCH', json: { dry_run: v } });
        renderDry(!!s.dry_run);
        toast(s.dry_run ? '试运行已开启：生成请求不会发往上游' : '试运行已关闭', 'ok');
      } catch (e) { dryErr.textContent = errMsg(e); renderDry(!v); }
    }, '开启试运行'));
  }
  renderDry(false);
  root.append(h('div', { class: 'card' },
    h('div', { class: 'card-head' }, h('span', { class: 'title', text: '试运行' })),
    dryBox,
    h('div', { class: 'muted fs12 mt16', text: '开启后，生图/视频/语音请求只做解析、校验并写入请求日志，不发往上游、不消耗额度，直接返回 400（code: dry_run）。参数可在 请求日志 → 详情 查看。测完记得关闭。' }),
    dryErr));
  root.append(poolCard, h('div', { class: 'card' },
    h('div', { class: 'card-head' }, h('span', { class: 'title', text: '全局代理' })),
    field('代理', proxy.el, '所有上游请求默认走这个代理'),
    h('div', { class: 'muted fs12', text: '优先级：账号代理 > 平台代理 > 全局代理；留空表示继承上一级' }),
    err,
    h('div', { class: 'mt16' },
      h('button', {
        class: 'btn primary', text: '保存', onclick: async () => {
          err.textContent = '';
          try {
            await admin('/admin/settings', { method: 'PATCH', json: { proxy_url: proxy.value() } });
            toast('已保存', 'ok');
          } catch (e) { err.textContent = errMsg(e); }
        },
      }))));

  async function load() {
    try {
      const [s, ps] = await Promise.all([admin('/admin/settings'), admin('/admin/proxies')]);
      proxy.set(s.proxy_url || '');
      renderDry(!!s.dry_run);
      renderPool(ps.proxies || []);
    } catch (e) { err.textContent = errMsg(e); }
  }
  load();

  function reload() { invalidateProxies(); load(); }

  function renderPool(list) {
    poolCard.textContent = '';
    poolCard.append(
      h('div', { class: 'card-head' },
        h('span', { class: 'title', text: '代理池' }),
        h('span', { class: 'muted fs12', text: '常用的代理地址，全局/平台/账号设置里可直接选用' }),
        h('span', { class: 'spacer' }),
        h('button', { class: 'btn sm primary', text: '＋ 添加代理', onclick: () => addProxySheet() })),
      list.length
        ? h('div', { class: 'table-wrap' },
          h('table', null,
            h('thead', null, h('tr', null,
              ['名称', '地址', '使用情况', '操作'].map(t => h('th', { text: t })))),
            h('tbody', null, list.map(row))))
        : h('div', { class: 'empty', text: '还没有代理' }));
  }

  function row(p) {
    const testOut = h('span', { class: 'hint' });
    return h('tr', null,
      h('td', { text: p.name }),
      h('td', null, h('span', { class: 'mono fs12', text: p.url_masked })),
      h('td', { class: 'fs12', text: p.used_by && p.used_by.length ? p.used_by.join('、') : '未使用' }),
      h('td', null,
        h('button', {
          class: 'btn sm', text: '测试', onclick: async () => {
            testOut.textContent = '测试中…';
            testOut.className = 'hint';
            try {
              const r = await admin('/admin/proxy/test', { method: 'POST', json: { proxy_url: `proxy:${p.id}` } });
              if (r.ok) testOut.textContent = `可连通（HTTP ${r.status}，${r.latency_ms} ms）`;
              else { testOut.textContent = `不可达：${r.error}`; testOut.className = 'hint warn-text'; }
            } catch (e) { testOut.textContent = errMsg(e); testOut.className = 'hint warn-text'; }
          },
        }),
        h('button', { class: 'btn sm', text: '编辑', onclick: () => editProxySheet(p) }),
        h('button', {
          class: 'btn sm danger', text: '删除', onclick: async () => {
            if (!await confirmDialog(`删除代理 "${p.name}"？`)) return;
            try {
              await admin(`/admin/proxies/${p.id}`, { method: 'DELETE' });
              toast('已删除', 'ok'); reload();
            } catch (e) { toast(errMsg(e), 'bad'); }
          },
        }),
        ' ', testOut));
  }

  // parse "URL" or "名称 URL" lines; name defaults to host:port
  function parseProxies(text) {
    const out = [];
    for (const line of (text || '').split('\n')) {
      const t = line.trim();
      if (!t) continue;
      const i = t.search(/\s/);
      if (i > 0) out.push({ name: t.slice(0, i), url: t.slice(i).trim() });
      else {
        let name = t;
        try { const u = new URL(t); name = u.host; } catch { /* keep raw as name */ }
        out.push({ name, url: t });
      }
    }
    return out;
  }

  function addProxySheet() {
    const listIn = h('textarea', { rows: 5, placeholder: '名称 http://127.0.0.1:7890\nsocks5://user:pass@host:1080' });
    const err2 = h('div', { class: 'err-text' });
    const close = openSheet('添加代理',
      h('div', null,
        field('代理（每行一个，可带名称）', listIn, '格式：名称 URL，或直接 URL（名称自动取主机:端口）'),
        err2,
        sheetButtons('保存', async () => {
          err2.textContent = '';
          const items = parseProxies(listIn.value);
          if (!items.length) { err2.textContent = '请输入至少一个代理'; return; }
          const failed = [];
          for (const it of items) {
            try {
              await admin('/admin/proxies', { method: 'POST', json: it });
            } catch (e) { failed.push({ it, err: e }); }
          }
          reload();
          if (failed.length) {
            listIn.value = failed.map(f => `${f.it.name} ${f.it.url}`).join('\n');
            err2.textContent = `${failed.length} 个失败：${errMsg(failed[0].err)}`;
            return;
          }
          close(); toast('已保存', 'ok');
        }, () => close())));
  }

  function editProxySheet(p) {
    const nameIn = h('input', { type: 'text', value: p.name });
    const urlIn = h('input', { type: 'text', value: p.url, placeholder: 'http://127.0.0.1:7890 或 socks5://host:1080' });
    const err2 = h('div', { class: 'err-text' });
    const close = openSheet(`编辑代理 ${p.name}`,
      h('div', null,
        field('名称', nameIn),
        field('地址', urlIn, '支持 http / https / socks5 / socks5h'),
        err2,
        sheetButtons('保存', async () => {
          err2.textContent = '';
          try {
            await admin(`/admin/proxies/${p.id}`, {
              method: 'PATCH',
              json: { name: nameIn.value.trim(), url: urlIn.value.trim() },
            });
            close(); toast('已保存', 'ok'); reload();
          } catch (e) { err2.textContent = errMsg(e); }
        }, () => close())));
  }
}
