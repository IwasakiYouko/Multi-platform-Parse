'use strict';
(() => {
  const $ = id => document.getElementById(id);
  const MASK = '********';
  const routes = [
    ['overview', '运行概览'], ['onebot', 'OneBot 连接'], ['platforms', '平台与内容策略'],
    ['policy', '解析与群聊策略'], ['proxy', '全局代理'], ['media', '媒体与下载'],
    ['cache', '缓存管理'], ['tasks', '解析任务'], ['security', '登录安全'],
    ['bans', '封禁管理'], ['server', '监听与 TLS'], ['account', '管理员账户']
  ];
  const state = {me: null, config: null, draft: null, platforms: [], csrf: '', dirty: false, page: '', capToken: '', capReady: false, capEnabled: true, bootstrapReady: false, busy: false};
  let noticeTimer, capScriptPromise, routeGeneration = 0;
  function node(tag, text, className) {
    const el = document.createElement(tag);
    if (text !== undefined && text !== null) el.textContent = String(text);
    if (className) el.className = className;
    return el;
  }
  function button(text, action, className = 'secondary') {
    const el = node('button', text, className);
    el.type = 'button';
    el.addEventListener('click', () => Promise.resolve(action()).catch(showError));
    return el;
  }
  function append(parent, ...children) { for (const child of children) if (child) parent.append(child); return parent; }
  function callout(text) { return node('p', text, 'callout'); }
  function notify(text, error = false) {
    clearTimeout(noticeTimer);
    $('notice').textContent = text;
    $('notice').dataset.error = String(error);
    $('notice').hidden = false;
    noticeTimer = setTimeout(() => { $('notice').hidden = true; }, error ? 12000 : 6500);
  }
  function showError(error) { notify(error.message || String(error), true); }
  async function api(path, options = {}) {
    const headers = {'Accept': 'application/json', ...options.headers};
    if (options.body !== undefined) headers['Content-Type'] = 'application/json';
    if (state.csrf && options.method && options.method !== 'GET') headers['X-CSRF-Token'] = state.csrf;
    const controller = new AbortController();
    const timer = setTimeout(() => controller.abort(), 60000);
    let response;
    try {
      response = await fetch(path, {...options, headers, credentials: 'same-origin', cache: 'no-store', signal: controller.signal, body: options.body === undefined ? undefined : JSON.stringify(options.body)});
    } catch (error) {
      throw new Error(error.name === 'AbortError' ? '请求超时，请检查服务连接。' : '无法连接服务，请检查网络和监听地址。');
    } finally { clearTimeout(timer); }
    const text = await response.text();
    let data = null;
    if (text) { try { data = JSON.parse(text); } catch { if (response.ok) throw new Error('服务返回了无法识别的数据。'); } }
    if (!response.ok) {
      if (response.status === 401 && state.me) showLogin('会话已失效，请重新登录。');
      throw new Error(data && data.error ? data.error : `请求失败（HTTP ${response.status}）`);
    }
    return data;
  }
  function confirmAction(title, message, password = false, confirmText = '确认') {
    return new Promise(resolve => {
      const dialog = $('confirm-dialog');
      $('dialog-title').textContent = title;
      $('dialog-message').textContent = message;
      $('dialog-confirm').textContent = confirmText;
      $('dialog-password-label').hidden = !password;
      $('dialog-password').hidden = !password;
      $('dialog-password').required = password;
      $('dialog-password').value = '';
      dialog.returnValue = '';
      dialog.addEventListener('close', () => {
        const result = dialog.returnValue === 'confirm' ? (password ? $('dialog-password').value : true) : null;
        $('dialog-password').value = '';
        resolve(result);
      }, {once: true});
      dialog.showModal();
      if (password) $('dialog-password').focus(); else $('dialog-confirm').focus();
    });
  }
  function dirty() {
    state.dirty = true;
    $('dirty-state').textContent = '有未保存的更改';
  }
  function get(path) { return path.split('.').reduce((value, key) => value[key], state.draft); }
  function set(path, value) {
    const keys = path.split('.');
    const key = keys.pop();
    const target = keys.reduce((value, part) => value[part], state.draft);
    if (JSON.stringify(target[key]) !== JSON.stringify(value)) { target[key] = value; dirty(); }
  }
  function panel(title, description) {
    const el = node('section', null, 'panel');
    if (title) el.append(node('h2', title));
    if (description) el.append(node('p', description, 'muted'));
    return el;
  }
  let fieldNumber = 0;
  function field(parent, path, label, options = {}) {
    const wrap = node('div', null, 'field');
    const id = `field-${++fieldNumber}`;
    const caption = node('label', label);
    caption.htmlFor = id;
    const type = options.type || 'text';
    let input;
    if (type === 'select') {
      input = node('select');
      for (const [value, text] of options.choices) { const option = node('option', text); option.value = value; input.append(option); }
    } else if (type === 'textarea' || type === 'lines' || type === 'ids') input = node('textarea');
    else { input = node('input'); input.type = type === 'secret' ? 'password' : type; }
    input.id = id;
    input.name = path;
    if (options.min !== undefined) input.min = options.min;
    if (options.max !== undefined) input.max = options.max;
    if (options.step !== undefined) input.step = options.step;
    if (type === 'url') input.pattern = 'https?://.*';
    if (options.required) input.required = true;
    if (options.placeholder) input.placeholder = options.placeholder;
    if (options.autocomplete) input.autocomplete = options.autocomplete;
    if (type === 'secret') {
      input.autocomplete = 'new-password';
      input.value = '';
      input.placeholder = get(path) ? '已保存；留空保持原值' : '未配置';
    } else if (type === 'lines' || type === 'ids') input.value = (get(path) || []).join('\n');
    else if (type === 'checkbox') {
      input.checked = !!get(path);
      caption.className = 'switch';
      caption.prepend(input);
    } else input.value = get(path) ?? '';
    input.addEventListener(type === 'checkbox' || type === 'select' ? 'change' : 'input', () => {
      input.setCustomValidity('');
      let value = input.value;
      if (type === 'checkbox') value = input.checked;
      if (type === 'number') {
        if (value === '' || !Number.isFinite(Number(value))) return;
        value = Number(value);
      }
      if (type === 'lines' || type === 'ids') {
        const values = value.split(/[\n,，\s]+/).map(v => v.trim()).filter(Boolean);
        if (type === 'ids') {
          if (values.some(v => !/^\d+$/.test(v) || !Number.isSafeInteger(Number(v)) || Number(v) <= 0)) {
            input.setCustomValidity('请填写正整数群号，每行一个。'); return;
          }
          value = [...new Set(values.map(Number))];
        } else value = [...new Set(values)];
      }
      set(path, value);
    });
    if (type === 'checkbox') wrap.append(caption);
    else {
      wrap.append(caption);
      if (type === 'secret') {
        const row = node('div', null, 'inline-secret');
        row.append(input, button('清除', async () => {
          if (await confirmAction('清除已保存的密钥', `确认清除「${label}」？保存设置后生效。`)) {
            input.value = ''; input.placeholder = '保存后清除'; set(path, '');
          }
        }));
        wrap.append(row);
      } else wrap.append(input);
    }
    if (options.help) {
      const hint = node('p', options.help, 'hint');
      hint.id = `${id}-hint`; input.setAttribute('aria-describedby', hint.id); wrap.append(hint);
    }
    parent.append(wrap);
    return input;
  }
  function settingsForm(title, description) {
    const form = node('form');
    form.dataset.settings = 'true';
    const box = panel(title, description);
    form.append(box);
    form.addEventListener('submit', event => { event.preventDefault(); saveConfig(form).catch(showError); });
    const bar = node('div', null, 'save-bar');
    bar.append(node('span', '更改仅在保存后生效。', 'muted'));
    const actions = node('div', null, 'actions');
    const save = node('button', '保存设置'); save.type = 'submit';
    actions.append(button('放弃更改', discard), save);
    bar.append(actions);
    return {form, box, bar};
  }
  async function saveConfig(form) {
    if (state.busy || !form.reportValidity()) return;
    const currentPassword = await confirmAction('保存设置', '请输入当前管理员密码。服务器将验证敏感设置，并原子保存完整配置。', true, '验证并保存');
    if (currentPassword === null) return;
    state.busy = true;
    const buttons = [...form.querySelectorAll('button')];
    buttons.forEach(el => { el.disabled = true; });
    const oldServer = JSON.stringify(state.config.server);
    try {
      await api('/api/config', {method: 'PUT', headers: {'X-Current-Password': currentPassword}, body: state.draft});
      const serverChanged = oldServer !== JSON.stringify(state.draft.server);
      const saved = structuredClone(state.draft);
      saved.onebot.token = MASK;
      if (saved.security.cap_secret) saved.security.cap_secret = MASK;
      if (saved.proxy.url && saved.proxy.url !== MASK) {
        const proxyAddress = new URL(saved.proxy.url);
        if (proxyAddress.username || proxyAddress.password) saved.proxy.url = MASK;
      }
      for (const platform of Object.values(saved.platforms)) if (platform.cookie) platform.cookie = MASK;
      state.config = saved;
      state.draft = structuredClone(saved);
      state.dirty = false;
      $('dirty-state').textContent = '';
      if (serverChanged) {
        await renderPage('server');
        const connection = panel('监听设置已应用', '服务已确认保存并应用新设置。若访问来源发生变化，需要在新地址重新登录；本页不会自动跳转。');
        const url = new URL(location.href);
        url.protocol = saved.server.tls ? 'https:' : 'http:';
        url.port = String(saved.server.port);
        if (saved.server.listen && !['0.0.0.0', '::', '[::]'].includes(saved.server.listen)) url.hostname = saved.server.listen.includes(':') && !saved.server.listen.startsWith('[') ? `[${saved.server.listen}]` : saved.server.listen;
        const address = saved.server.external_url || url.origin;
        connection.append(safeLink(address, '打开管理地址：' + address));
        $('page-content').prepend(connection);
      } else await renderPage(state.page);
      notify(serverChanged ? '监听 / TLS 设置已应用，请使用管理地址确认连接。' : '设置已安全保存。');
    } finally {
      state.busy = false;
      buttons.forEach(el => { el.disabled = false; });
    }
  }
  async function discard() {
    if (state.dirty && !await confirmAction('放弃未保存更改', '本次所有页面中尚未保存的更改都会丢弃。')) return;
    state.draft = structuredClone(state.config); state.dirty = false; $('dirty-state').textContent = '';
    await renderPage(state.page);
  }
  function formatBytes(value) {
    if (!Number.isFinite(Number(value))) return '—';
    let number = Number(value), unit = 0;
    const units = ['B', 'KiB', 'MiB', 'GiB', 'TiB'];
    while (number >= 1024 && unit < units.length - 1) { number /= 1024; unit++; }
    return `${number.toLocaleString('zh-CN', {maximumFractionDigits: unit ? 2 : 0})} ${units[unit]}`;
  }
  function date(value) {
    if (!value) return '—';
    const parsed = new Date(value);
    return Number.isNaN(parsed.getTime()) ? String(value) : parsed.toLocaleString('zh-CN');
  }
  function table(parent, headers, rows, emptyMessage) {
    if (!rows.length) { parent.append(node('p', emptyMessage || '暂无记录', 'empty')); return; }
    const wrap = node('div', null, 'table-wrap'); wrap.tabIndex = 0;
    wrap.setAttribute('aria-label', '可横向滚动的数据表');
    const el = node('table'), head = node('thead'), tr = node('tr');
    for (const title of headers) { const th = node('th', title); th.scope = 'col'; tr.append(th); }
    head.append(tr); el.append(head);
    const body = node('tbody');
    for (const values of rows) {
      const row = node('tr');
      for (const value of values) { const td = node('td'); if (value instanceof Node) td.append(value); else td.textContent = value ?? '—'; row.append(td); }
      body.append(row);
    }
    el.append(body); wrap.append(el); parent.append(wrap);
  }
  function refreshAction(action) { return button('刷新记录', action); }
  function safeLink(value, label) {
    try {
      const url = new URL(value);
      if (url.protocol === 'http:' || url.protocol === 'https:') {
        const link = node('a', label || value); link.href = url.href; link.target = '_blank'; link.rel = 'noopener noreferrer'; return link;
      }
    } catch {}
    return node('span', label || value || '—');
  }
  async function copy(text, label = '内容') {
    if (!text) throw new Error('没有可复制的内容。');
    try { await navigator.clipboard.writeText(text); notify(`${label}已复制。`); }
    catch {
      const area = node('textarea'); area.value = text; area.setAttribute('aria-label', '复制内容');
      $('page-content').append(area); area.select();
      if (document.execCommand('copy')) { area.remove(); notify(`${label}已复制。`); }
      else { area.focus(); throw new Error('浏览器不允许自动复制，已选中文本，请按 Ctrl+C / Command+C。'); }
    }
  }
  function onebotURL(status) {
    if (status?.onebot_ws_url) return status.onebot_ws_url;
    const base = state.config.server.external_url || location.origin;
    try { const url = new URL('/onebot/v11/ws', base); url.protocol = url.protocol === 'https:' ? 'wss:' : 'ws:'; return url.href; }
    catch { return ''; }
  }
  async function overview(root) {
    const status = await api('/api/status');
    const grid = node('div', null, 'grid');
    const uptime = Number(status.uptime_seconds);
    const metrics = [
      ['服务版本', status.version || '未报告'],
      ['运行时长', Number.isFinite(uptime) ? `${Math.floor(uptime / 3600)} 小时 ${Math.floor(uptime % 3600 / 60)} 分钟` : '未报告'],
      ['在线 OneBot', Array.isArray(status.bots) ? status.bots.length : '未报告'],
      ['缓存条目', status.cache_entries ?? '未报告'],
      ['缓存占用', status.cache_bytes === undefined ? '未报告' : formatBytes(status.cache_bytes)],
      ['活跃任务', status.active_tasks ?? '未报告']
    ];
    for (const [label, value] of metrics) { const item = panel(); item.classList.add('metric'); append(item, node('p', label, 'metric-label'), node('div', value, 'metric-value')); grid.append(item); }
    root.append(grid);
    const info = panel('当前服务', '以下信息来自实际运行状态。设置中的监听地址可能正在应用。');
    info.append(node('p', `当前监听：${status.listen || '服务未报告'}`, 'mono'));
    info.append(node('p', `OneBot WebSocket：${onebotURL(status)}`, 'mono'));
    if (status.queue_size !== undefined) info.append(node('p', `等待队列：${status.queue_size}`));
    if (status.currenturl) info.append(append(node('p'), node('span', '当前管理地址：'), safeLink(status.currenturl)));
    if (status.pending_cleanup !== undefined) info.append(node('p', `待清理缓存：${status.pending_cleanup}`));
    if (status.cleanup_error) info.append(node('p', `缓存清理错误：${status.cleanup_error}`, 'error'));
    info.append(refreshAction(() => renderPage('overview')));
    root.append(info);
    const manual = panel('手动解析', '提交真实平台链接进入解析队列；任务页面查看处理结果。此操作不向聊天对象主动推送。');
    const form = node('form');
    const label = node('label', '分享链接'); label.htmlFor = 'parse-url';
    const input = node('input'); input.type = 'url'; input.id = 'parse-url'; input.required = true; input.placeholder = 'https://…';
    const submit = node('button', '提交解析'); submit.type = 'submit';
    append(form, label, input, append(node('div', null, 'actions'), submit));
    form.addEventListener('submit', async event => {
      event.preventDefault(); if (!form.reportValidity()) return;
      submit.disabled = true;
      try { const result = await api('/api/parse', {method: 'POST', body: {url: input.value.trim()}}); notify(`任务已提交：${result.id}`); location.hash = '#tasks'; }
      catch (error) { showError(error); } finally { submit.disabled = false; }
    });
    manual.append(form); root.append(manual);
  }
  async function onebot(root) {
    const {form, box, bar} = settingsForm('OneBot v11 反向 WebSocket', '在 NapCat 或其他 OneBot v11 实现中配置反向 WebSocket，连接角色请选择 Universal。');
    const status = await api('/api/status');
    const address = onebotURL(status);
    append(box, node('p', '连接地址', 'muted'), node('div', address || '请先配置正确的服务对外地址。', 'code-box'));
    const actions = node('div', null, 'actions');
    actions.append(button('复制连接地址', () => copy(address, '连接地址')));
    let tokenBox;
    actions.append(button('轮换访问 Token', async () => {
      if (state.dirty) throw new Error('请先保存或放弃未保存配置，再轮换 Token。');
      const password = await confirmAction('轮换 OneBot Token', '旧 Token 将立即失效，所有 OneBot 客户端需要更新。新 Token 只显示一次。请输入当前密码。', true, '立即轮换');
      if (password === null) return;
      const result = await api('/api/onebot/token', {method: 'POST', headers: {'X-Current-Password': password}, body: {current_password: password}});
      state.config.onebot.token = MASK; state.draft.onebot.token = MASK;
      if (tokenBox) tokenBox.remove();
      tokenBox = panel('新的访问 Token（仅显示一次）', '请立即复制至 OneBot 客户端。离开此页面后不再显示。切勿将 Token 拼入公开链接。');
      const token = node('input'); token.type = 'password'; token.readOnly = true; token.value = result.token; token.autocomplete = 'off'; token.setAttribute('aria-label', '新 OneBot Token');
      const tokenActions = node('div', null, 'actions');
      tokenActions.append(button('复制 Token', () => copy(token.value, 'Token')), button('显示 / 隐藏', () => { token.type = token.type === 'password' ? 'text' : 'password'; }), button('隐藏并清除', () => { token.value = ''; tokenBox.remove(); tokenBox = null; }));
      append(tokenBox, token, tokenActions); box.append(tokenBox);
      notify('Token 已轮换，请立即更新 OneBot 客户端。');
    }, 'danger'));
    box.append(actions);
    box.append(callout('访问 Token 不会从服务器回显。请求头使用 Authorization: Bearer <Token>。若机器人运行在容器中，请确保它能访问该地址，并共享实际媒体目录。'));
    field(box, 'onebot.self_ids', '允许连接的机器人 QQ 号', {type: 'lines', help: '每行一个；留空允许持有效 Token 的机器人连接。'});
    const connected = panel('已连接机器人');
    table(connected, ['机器人账号', '连接时间', '最近心跳'], (status.bots || []).map(bot => [bot.self_id, date(bot.connected_at), date(bot.last_seen)]), '暂无机器人连接。请检查地址、Token、防火墙及连接角色。');
    connected.append(refreshAction(() => renderPage('onebot')));
    append(form, connected, bar); root.append(form);
  }
  function platforms(root) {
    const {form, box, bar} = settingsForm('24 平台策略', '平台启用与 NSFW 屏蔽彼此独立。关闭平台不会自动修改其 NSFW、代理分流或 Cookie 设置。');
    const searchLabel = node('label', '搜索平台'); searchLabel.htmlFor = 'platform-search';
    const search = node('input'); search.id = 'platform-search'; search.type = 'search'; search.placeholder = '名称、域名或平台标识'; search.className = 'search';
    append(box, searchLabel, search);
    const actions = node('div', null, 'actions');
    function bulk(key, value) { for (const p of state.platforms) set(`platforms.${p.id}.${key}`, value); renderPage('platforms').catch(showError); }
    actions.append(button('全部启用', () => bulk('enabled', true)), button('全部停用', () => bulk('enabled', false)), button('全部屏蔽 NSFW', () => bulk('nsfw_blocked', true)), button('全部允许 NSFW', async () => {
      if (await confirmAction('允许 NSFW 内容', '此操作将关闭全部平台的 NSFW 屏蔽。仍需遵守平台规则及所在地区法律。保存后生效。')) bulk('nsfw_blocked', false);
    }));
    box.append(actions);
    const grid = node('div', null, 'grid'), cards = [];
    for (const platform of state.platforms) {
      const card = panel(); card.classList.add('platform-card');
      const title = node('div', null, 'platform-title');
      append(title, node('h2', platform.name), node('span', platform.id, 'pill')); card.append(title);
      card.append(node('p', (platform.domains || []).join(' · '), 'domains muted'));
      const typeLabels = {video:'视频',image:'图片/图文',article:'文章',audio:'音频',multimedia:'混合媒体'};
      card.append(node('p', '支持：' + (platform.types || []).map(type => typeLabels[type] || type).join('、'), 'hint'));
      const controls = node('div', null, 'switches');
      const prefix = `platforms.${platform.id}.`;
      field(controls, prefix + 'enabled', '启用解析', {type: 'checkbox'});
      field(controls, prefix + 'nsfw_blocked', '屏蔽 NSFW', {type: 'checkbox'});
      field(controls, prefix + 'overseas', '海外代理分流', {type: 'checkbox'});
      field(controls, prefix + 'silent', '拒绝时静默', {type: 'checkbox'});
      card.append(controls);
      const details = node('details'); details.append(node('summary', '拒绝提示与登录 Cookie'));
      field(details, prefix + 'reject_text', '本平台拒绝提示', {type: 'textarea', help: '留空使用全局提示。可使用 {platform}、{reason}。静默开启时不发送提示。'});
      field(details, prefix + 'cookie', '平台 Cookie', {type: 'secret', help: '仅替换需要登录的平台凭据；留空保持原值。Cookie 可能拥有账户权限，请使用专用账户。'});
      card.append(details); grid.append(card); cards.push({card, terms: `${platform.id} ${platform.name} ${(platform.domains || []).join(' ')}`.toLowerCase()});
    }
    const empty = node('p', '没有匹配的平台。', 'empty'); empty.hidden = true;
    search.addEventListener('input', () => {
      const query = search.value.trim().toLowerCase(); let visible = 0;
      for (const item of cards) { item.card.hidden = !item.terms.includes(query); if (!item.card.hidden) visible++; }
      empty.hidden = visible !== 0;
    });
    append(form, grid, empty, bar); root.append(form);
  }
  function policy(root) {
    const {form, box, bar} = settingsForm('解析与群聊策略', '策略在任务处理和结果发送前检查，包括缓存命中。白名单非空时仅允许白名单，否则应用黑名单。');
    field(box, 'policy.enabled', '启用全部解析', {type: 'checkbox'});
    field(box, 'policy.private_enabled', '允许私聊解析', {type: 'checkbox'});
    field(box, 'policy.cooldown_seconds', '同会话重复内容冷却（秒）', {type: 'number', min: 0, max: 86400, help: '默认 30 秒；任务执行中也阻止重复入队。成功发送后起算，失败允许重试；0 关闭，不影响其他群。'});
    const grid = node('div', null, 'field-grid');
    field(grid, 'policy.group_whitelist', '群聊白名单', {type: 'ids', help: '每行一个群号。留空不限制群聊范围。'});
    field(grid, 'policy.group_blacklist', '群聊黑名单', {type: 'ids', help: '每行一个群号。仅在白名单为空时应用。'});
    box.append(grid);
    field(box, 'policy.reject_text', '全局拒绝提示', {type: 'textarea', help: '支持 {platform} 和 {reason}；平台可单独覆盖，也可设置静默。'});
    append(form, bar); root.append(form);
  }
  function proxy(root) {
    const {form, box, bar} = settingsForm('全局网络代理', '所有平台解析及媒体下载使用统一网络策略。海外分流由各平台独立的「海外代理分流」开关决定。');
    field(box, 'proxy.mode', '代理模式', {type: 'select', choices: [['direct', '全部直连'], ['all', '全部走代理'], ['overseas', '仅海外平台走代理']]});
    const original = get('proxy.url');
    let parsed = null;
    if (original && original !== MASK) { try { parsed = new URL(original); } catch {} }
    const secretSaved = original === MASK;
    if (secretSaved) box.append(callout('已有代理凭据已隐藏。不修改下方地址字段时保持原配置；若需要更换，请完整填写协议、地址和端口。'));
    const grid = node('div', null, 'field-grid');
    const controls = {};
    function proxyInput(key, caption, type, value, choices) {
      const wrap = node('div', null, 'field'), label = node('label', caption); label.htmlFor = `proxy-${key}`;
      const input = node(type === 'select' ? 'select' : 'input'); input.id = `proxy-${key}`;
      if (choices) for (const [val, text] of choices) { const option = node('option', text); option.value = val; input.append(option); }
      else input.type = type;
      input.value = value || '';
      if (type === 'password') input.autocomplete = 'new-password';
      append(wrap, label, input); grid.append(wrap); controls[key] = input;
      input.addEventListener(type === 'select' ? 'change' : 'input', compose);
      return input;
    }
    function decode(value) { try { return decodeURIComponent(value || ''); } catch { return value || ''; } }
    proxyInput('scheme', '协议', 'select', parsed?.protocol.replace(':', '') || 'http', [['http', 'HTTP'], ['https', 'HTTPS'], ['socks5', 'SOCKS5'], ['socks5h', 'SOCKS5（代理解析 DNS）']]);
    proxyInput('host', '代理主机', 'text', parsed?.hostname || '');
    proxyInput('port', '代理端口', 'number', parsed?.port || ''); controls.port.min = '1'; controls.port.max = '65535';
    proxyInput('username', '用户名（可选）', 'text', decode(parsed?.username));
    proxyInput('password', '密码（可选）', 'password', decode(parsed?.password));
    const status = node('p', secretSaved ? '已保留保存的代理地址。' : '填写后保存生效。', 'hint');
    function compose() {
      controls.host.setCustomValidity('');
      const host = controls.host.value.trim();
      if (!host) { controls.host.setCustomValidity('更换代理时必须填写主机；若需移除，请使用「清除代理地址」。'); dirty(); return; }
      try {
        if (/[\s/@?#]/.test(host)) throw new Error();
        const url = new URL(`${controls.scheme.value}://${host.includes(':') && !host.startsWith('[') ? `[${host}]` : host}${controls.port.value ? ':' + controls.port.value : ''}`);
        if (!url.hostname || url.pathname !== '' && url.pathname !== '/') throw new Error();
        url.username = controls.username.value; url.password = controls.password.value;
        set('proxy.url', url.href.replace(/\/$/, ''));
        status.textContent = '代理地址已修改，保存后生效。';
      } catch { controls.host.setCustomValidity('请输入有效的代理主机和端口，不要填写协议或路径。'); }
    }
    box.append(grid, status);
    box.append(button('清除代理地址', async () => {
      if (!await confirmAction('清除代理地址', '将清除代理及凭据，并切换为全部直连。保存后生效。')) return;
      set('proxy.url', ''); set('proxy.mode', 'direct'); await renderPage('proxy');
    }, 'danger'));
    const test = panel('测试已保存的代理策略', '测试使用服务端已保存配置。修改代理后请先保存；测试会按所选平台的分流策略发起真实连接，不会自动回退直连。');
    const label = node('label', '测试平台'); label.htmlFor = 'proxy-test-platform';
    const select = node('select'); select.id = 'proxy-test-platform';
    for (const platform of state.platforms) { const option = node('option', platform.name); option.value = platform.id; select.append(option); }
    select.value = 'youtube';
    const result = node('p', '', 'hint'); result.setAttribute('role', 'status');
    const testButton = button('测试连接', async () => {
      if (state.dirty) throw new Error('请先保存或放弃未保存更改，再测试实际生效的代理配置。');
      testButton.disabled = true; result.textContent = '正在测试…';
      try { await api('/api/proxy/test', {method: 'POST', body: {platform: select.value}}); result.textContent = '连接测试成功。'; }
      catch (error) { result.textContent = `测试失败：${error.message}`; throw error; }
      finally { testButton.disabled = false; }
    });
    append(test, label, select, append(node('div', null, 'actions'), testButton), result);
    append(form, test, bar); root.append(form);
  }
  function media(root) {
    const {form, box, bar} = settingsForm('媒体与下载', '大小单位均为字节；1 MiB = 1048576 字节，1 GiB = 1073741824 字节。调整限制不会绕过平台策略。');
    const grid = node('div', null, 'field-grid');
    const transport = field(grid, 'media.transport', '媒体传输方式', {type: 'select', choices: [['file', '同机共享路径（默认）'], ['http', '跨机器短期签名 URL']], help: '共享路径模式要求解析服务与 OneBot 客户端可读取相同媒体路径；HTTP 模式仅提供短期签名链接，不公开媒体目录。'});
    const externalURL = field(grid, 'server.external_url', '媒体对外访问地址', {type: 'url', placeholder: 'https://parse.example.com', required: get('media.transport') === 'http', help: 'HTTP 传输必须填写 OneBot 客户端可访问的服务 HTTP(S) 地址。此字段与「监听与 TLS」中的对外访问地址共用配置。'});
    transport.addEventListener('change', () => { externalURL.required = transport.value === 'http'; });
    field(grid, 'media.max_bytes', '单次媒体大小上限（字节）', {type: 'number', min: 1024, max: 4294967296, required: true, help: `当前：${formatBytes(get('media.max_bytes'))}`});
    field(grid, 'media.max_duration_seconds', '视频时长上限（秒）', {type: 'number', min: 1, max: 86400, required: true, help: '最多 86400 秒（24 小时）。'});
    field(grid, 'media.max_images', '单次图片数量上限', {type: 'number', min: 1, max: 200, required: true});
    field(grid, 'media.quality', '视频清晰度上限（高度像素）', {type: 'number', min: 144, max: 4320, required: true, help: '例如 720、1080；以平台可用资源为准。'});
    field(grid, 'media.concurrency', '同时解析 / 下载任务数', {type: 'number', min: 1, max: 16, required: true});
    field(grid, 'media.transcode_concurrency', '同时转码任务数', {type: 'number', min: 1, max: 4, required: true});
    field(grid, 'media.max_disk_bytes', '媒体磁盘容量上限（字节）', {type: 'number', min: 1024, required: true, help: `当前：${formatBytes(get('media.max_disk_bytes'))}，不得小于单次媒体大小上限。`});
    field(grid, 'media.oversize_mode', '超限处理', {type: 'select', choices: [['link', '返回原始链接'], ['file', '尝试作为文件发送']]});
    box.append(grid);
    field(box, 'media.auto_download', '自动下载并发送媒体', {type: 'checkbox'});
    field(box, 'media.show_cover', '显示视频封面', {type: 'checkbox'});
    field(box, 'media.show_info', '显示标题与作者信息', {type: 'checkbox'});
    form.append(bar); root.append(form);
  }
  async function cache(root) {
    const {form, box, bar} = settingsForm('缓存期限', '默认保留 24 小时，自写入时间起计算，不因命中而续期。缩短期限也会更新已有记录的有效期。');
    field(box, 'cache.ttl_hours', '缓存保留时长（小时）', {type: 'number', min: 1, max: 8760, required: true});
    form.append(bar); root.append(form);
    const entries = await api('/api/cache');
    const list = panel('缓存记录', '清理会同时处理对应媒体和关联任务记录；正在使用的文件由服务端租约保护。');
    const actions = node('div', null, 'actions');
    actions.append(refreshAction(() => renderPage('cache')));
    for (const [all, label] of [[false, '清理过期缓存'], [true, '清空全部缓存']]) actions.append(button(label, async () => {
      if (!await confirmAction(label, all ? '此操作会删除全部缓存媒体及关联任务记录，无法恢复；后续请求需要重新解析和下载。确认继续？' : '确认删除已过期的缓存媒体及关联任务记录？此操作无法撤销。', false, '确认清理')) return;
      await api('/api/cache/clear', {method: 'POST', body: {all}}); await renderPage('cache'); notify('缓存清理请求已完成。');
    }, all ? 'danger' : 'secondary'));
    list.append(actions);
    table(list, ['平台', '链接', '写入时间', '到期时间', '缓存标识'], (entries || []).map(entry => [platformName(entry.platform), safeLink(entry.url), date(entry.created_at), date(entry.expires_at), entry.key]), '暂无缓存记录。');
    root.append(list);
  }
  function platformName(id) { return state.platforms.find(platform => platform.id === id)?.name || id || '—'; }
  async function tasks(root) {
    const entries = await api('/api/tasks');
    const box = panel('解析任务', '任务状态和错误消息由服务器记录。不会把平台登录受限或不可用伪装成解析成功。');
    box.append(refreshAction(() => renderPage('tasks')));
    table(box, ['任务', '平台', '状态', '链接', '处理结果 / 错误', '提交时间', '更新时间', '到期时间'], (entries || []).map(task => [task.id, platformName(task.platform), task.status, safeLink(task.url), task.message || '—', date(task.created_at), date(task.updated_at), date(task.expires_at)]), '暂无任务。可在概览中手动提交链接，或通过 OneBot 触发解析。');
    root.append(box);
  }
  function security(root) {
    const {form, box, bar} = settingsForm('登录防护', '默认启用 Cap 验证（强度 4）。关闭后仍需管理员密码，且保留登录限速与失败封禁。');
    const disabled = field(box, 'security.cap_disabled', '关闭 Cap CAPTCHA（仅使用密码登录）', {type: 'checkbox', help: '警告：关闭后不再提供 CAPTCHA 防护。公网暴露的管理面板不建议关闭；请使用可信网络或访问控制保护。保存时必须验证当前管理员密码。'});
    const capGrid = node('div', null, 'field-grid');
    field(capGrid, 'security.cap_url', '自托管 Cap 服务地址', {type: 'url', required: true, help: '由本服务代理验证资源，浏览器不直接访问第三方 CDN。'});
    field(capGrid, 'security.cap_site_key', 'Cap 站点密钥（Site Key）', {type: 'text', help: '首次启用可留空，由服务端使用本机管理密钥创建并验证站点。'});
    field(capGrid, 'security.cap_secret', 'Cap 服务端密钥（Secret）', {type: 'secret'});
    field(capGrid, 'security.cap_level', 'Cap 验证强度', {type: 'number', min: 1, max: 10, required: true, help: '默认 4；对应本地 Cap 服务配置，不是可绕过的前端参数。'});
    const updateCapFields = () => {
      capGrid.hidden = disabled.checked;
      capGrid.querySelectorAll('input, button').forEach(input => { input.disabled = disabled.checked; });
    };
    disabled.addEventListener('change', updateCapFields);
    updateCapFields();
    box.append(capGrid);
    const grid = node('div', null, 'field-grid');
    field(grid, 'security.failures', '触发封禁的失败次数', {type: 'number', min: 1, max: 100, required: true});
    field(grid, 'security.window_minutes', '失败计数窗口（分钟）', {type: 'number', min: 1, max: 1440, required: true});
    field(grid, 'security.ban_minutes', '自动封禁时长（分钟）', {type: 'number', min: 1, max: 43200, required: true});
    field(grid, 'security.session_hours', '登录会话有效期（小时）', {type: 'number', min: 1, max: 168, required: true});
    box.append(grid);
    box.append(callout('重新启用前，请先启动 Cap 及其 Redis / Valkey，并确认部署主机上的管理密钥文件可用。服务端验证成功后才会保存；失败时保留原配置。修改 Cap 或可信代理前，请确认部署主机仍可本地管理。'));
    form.append(bar); root.append(form);
  }
  async function bans(root) {
    const entries = await api('/api/bans');
    const box = panel('登录封禁管理', '仅展示实际失败和封禁记录。解封立即生效；也可在部署主机使用管理员 CLI 恢复访问。');
    box.append(refreshAction(() => renderPage('bans')));
    table(box, ['来源 IP', '失败次数', '计数窗口起点', '封禁到期', '操作'], (entries || []).map(ban => [ban.ip, ban.failures, date(ban.window_start), date(ban.banned_until), button('解除封禁', async () => {
      if (!await confirmAction('解除封禁', `确认解除 ${ban.ip} 的封禁并清除其失败计数？`)) return;
      await api('/api/bans?ip=' + encodeURIComponent(ban.ip), {method: 'DELETE'}); await renderPage('bans'); notify('已解除封禁。');
    })]), '暂无封禁或失败记录。');
    root.append(box);
  }
  function server(root) {
    const {form, box, bar} = settingsForm('监听与 TLS', 'HTTP 面板与 OneBot WebSocket 共用一个监听器。修改地址或 TLS 后，请使用新地址确认连接。');
    box.append(callout('公开部署建议启用 HTTPS，或通过可信反向代理终止 TLS。配置可信代理时仅填写实际代理地址，切勿无条件信任全部来源。'));
    const grid = node('div', null, 'field-grid');
    field(grid, 'server.listen', '监听 IP / 主机', {required: true, help: '默认 ::，同时监听全部 IPv4 / IPv6 网络接口（需系统支持 IPv6）；127.0.0.1 仅允许本机 IPv4 访问。'});
    field(grid, 'server.port', '监听端口', {type: 'number', min: 1, max: 65535, required: true, help: '默认 10492；仅端口已占用时尝试下一个端口。实际端口见概览。'});
    field(grid, 'server.external_url', '对外访问地址', {type: 'url', placeholder: 'https://parse.example.com', required: get('media.transport') === 'http', help: '用于生成 OneBot 地址和签名媒体链接。HTTP 媒体传输时必填；共享路径模式留空时使用当前浏览器访问地址。'});
    field(grid, 'server.cert_file', 'TLS 证书文件路径', {help: '服务进程可读取的 PEM 证书链文件。'});
    field(grid, 'server.key_file', 'TLS 私钥文件路径', {help: '服务进程可读取的 PEM 私钥文件；不是私钥内容。'});
    box.append(grid);
    field(box, 'server.tls', '由本服务启用 TLS', {type: 'checkbox', help: '启用前必须配置可读取且匹配的证书和私钥。'});
    field(box, 'server.trusted_proxies', '可信反向代理 IP / CIDR', {type: 'lines', help: '每行一个。只有这些来源转发的客户端 IP 才会被信任。留空不信任转发头。'});
    field(box, 'server.admin_networks', '允许访问管理接口的 IP / CIDR', {type: 'lines', help: '每行一个；留空不额外限制。误填可能导致远程管理失联，请保留本地主机恢复通道。'});
    form.append(bar); root.append(form);
  }
  function account(root) {
    if (state.me.must_change_password) root.append(callout('首次登录必须更换密码，也可以修改用户名。完成之前，其他管理功能不可访问。'));
    const box = panel('管理员账户', '修改成功后会重新确认会话。请使用唯一且足够长的密码，不要复用 OneBot Token 或 Cap 密钥。');
    const form = node('form'); form.autocomplete = 'on';
    function accountInput(id, caption, type, value, autocomplete) {
      const wrap = node('div', null, 'field'), label = node('label', caption); label.htmlFor = id;
      const input = node('input'); input.id = id; input.type = type; input.value = value; input.required = true; input.autocomplete = autocomplete;
      if (id === 'account-new-password') input.minLength = 12;
      append(wrap, label, input); form.append(wrap); return input;
    }
    const username = accountInput('account-username', '用户名', 'text', state.me.username, 'username'); username.maxLength = 64;
    const current = accountInput('account-current-password', '当前密码', 'password', '', 'current-password');
    const password = accountInput('account-new-password', state.me.must_change_password ? '新密码（至少 12 个字符）' : '新密码（至少 12 个字符；留空保留原密码）', 'password', '', 'new-password');
    const repeat = accountInput('account-repeat-password', '再次输入新密码', 'password', '', 'new-password');
    password.required = state.me.must_change_password;
    repeat.required = state.me.must_change_password;
    password.addEventListener('input', () => { repeat.required = state.me.must_change_password || password.value.length > 0; repeat.setCustomValidity(''); });
    repeat.addEventListener('input', () => repeat.setCustomValidity(''));
    const submit = node('button', '更新管理员账户'); submit.type = 'submit'; form.append(submit);
    form.addEventListener('submit', async event => {
      event.preventDefault(); repeat.setCustomValidity(password.value === repeat.value ? '' : '两次输入的密码不一致。');
      if (!form.reportValidity()) return;
      submit.disabled = true;
      try {
        await api('/api/account', {method: 'POST', body: {username: username.value.trim(), current_password: current.value, new_password: password.value}});
        current.value = ''; password.value = ''; repeat.value = '';
        showLogin('账户已更新，全部旧会话已失效。请使用新凭据登录。');
      } catch (error) { showError(error); } finally { submit.disabled = false; }
    });
    username.addEventListener('input', () => username.setCustomValidity(''));
    box.append(form); root.append(box);
    if (!state.me.must_change_password) {
      const backup = panel('配置导出与恢复', '导出仅包含已保存的脱敏配置，不包含可用的 Token、Cookie 或代理密码。完整恢复还需备份数据库、加密密钥及 Cap 数据卷。');
      backup.append(button('导出脱敏配置', async () => {
        const saved = await api('/api/config');
        const address = URL.createObjectURL(new Blob([JSON.stringify(saved, null, 2)], {type: 'application/json'}));
        const link = node('a'); link.href = address; link.download = 'multiparse-config-redacted.json';
        document.body.append(link); link.click(); link.remove();
        setTimeout(() => URL.revokeObjectURL(address), 1000);
      }));
      root.append(backup);
    }
  }
  const renderers = {overview, onebot, platforms, policy, proxy, media, cache, tasks, security, bans, server, account};
  async function renderPage(route) {
    if (!state.me) return;
    if (state.me.must_change_password) route = 'account';
    if (!renderers[route]) route = 'overview';
    const generation = ++routeGeneration;
    state.page = route;
    $('page-title').textContent = routes.find(item => item[0] === route)[1];
    document.title = `${$('page-title').textContent} · 多平台解析`;
    for (const link of $('navigation').querySelectorAll('a')) {
      if (link.hash === '#' + route) link.setAttribute('aria-current', 'page'); else link.removeAttribute('aria-current');
    }
    const root = node('div');
    $('page-content').replaceChildren(node('p', '正在读取…', 'muted'));
    try {
      await renderers[route](root);
      if (generation === routeGeneration && state.me) $('page-content').replaceChildren(root);
    } catch (error) {
      if (generation !== routeGeneration || !state.me) return;
      const failed = panel('无法加载此页面'); failed.append(node('p', error.message, 'error'), button('重新加载', () => renderPage(route)));
      $('page-content').replaceChildren(failed);
    }
  }
  async function navigate() {
    if (!state.me) return;
    let target = location.hash.slice(1) || 'overview';
    if (!renderers[target]) target = 'overview';
    const currentForm = $('page-content').querySelector('form[data-settings]');
    if (state.dirty && currentForm && !currentForm.reportValidity()) {
      history.replaceState(null, '', '#' + state.page);
      notify('请先修正当前页面中的无效字段，再切换页面。', true); return;
    }
    await renderPage(target);
  }
  async function enterApp() {
    state.me = await api('/api/me'); state.csrf = state.me.csrf_token;
    if (!state.csrf) throw new Error('服务未返回 CSRF 令牌，无法安全操作。');
    if (!state.me.must_change_password) {
      const [config, platformList] = await Promise.all([api('/api/config'), api('/api/platforms')]);
      state.config = config; state.draft = structuredClone(config); state.platforms = platformList;
    }
    state.dirty = false; $('dirty-state').textContent = '';
    $('login-password').value = ''; state.capToken = '';
    $('cap-container').replaceChildren();
    $('login-view').hidden = true; $('app-view').hidden = false;
    $('current-user').textContent = state.me.username;
    const nav = $('navigation'); nav.replaceChildren();
    for (const [id, label] of routes) {
      if (state.me.must_change_password && id !== 'account') continue;
      const link = node('a', label); link.href = '#' + id; nav.append(link);
    }
    await renderPage(state.me.must_change_password ? 'account' : location.hash.slice(1) || 'overview');
  }
  function showLogin(message = '') {
    routeGeneration++;
    state.me = null; state.csrf = ''; state.config = null; state.draft = null; state.platforms = []; state.dirty = false;
    $('page-content').replaceChildren(); $('navigation').replaceChildren();
    $('app-view').hidden = true; $('login-view').hidden = false;
    $('login-password').value = ''; $('login-error').textContent = message;
    document.title = '安全登录 · 多平台解析';
    bootstrap().catch(error => { $('cap-status').textContent = error.message; });
  }
  function loginReady() {
    return state.bootstrapReady && (!state.capEnabled || (state.capReady && !!state.capToken));
  }
  function updateLoginReady() {
    $('login-submit').disabled = !loginReady();
  }
  async function loadWidget(url) {
    const resolved = new URL(url, location.origin);
    if (resolved.origin !== location.origin || !resolved.pathname.startsWith('/captcha/')) throw new Error('拒绝加载非本地 Cap 脚本。请检查自托管配置。');
    if (customElements.get('cap-widget')) return;
    if (!capScriptPromise) {
      capScriptPromise = new Promise((resolve, reject) => {
        window.CAP_CUSTOM_WASM_URL = '/captcha/assets/cap_wasm_bg.wasm';
        window.CAP_CUSTOM_HASHWX_URL = '/captcha/assets/hashwx.wasm';
        window.CAP_PAKO_URL = '/captcha/assets/pako_inflate.min.js';
        const script = node('script'); script.src = resolved.href;
        script.addEventListener('load', resolve, {once: true});
        script.addEventListener('error', () => { script.remove(); capScriptPromise = null; reject(new Error('无法加载本地 Cap 组件。请检查 /captcha/ 代理和 Cap 服务。')); }, {once: true});
        document.head.append(script);
      });
    }
    await capScriptPromise;
  }
  let bootstrapGeneration = 0;
  async function bootstrap() {
    const generation = ++bootstrapGeneration;
    state.bootstrapReady = false; state.capEnabled = true; state.capReady = false; state.capToken = ''; updateLoginReady();
    $('cap-container').replaceChildren(); $('cap-container').hidden = true;
    $('cap-status').className = 'hint'; $('cap-status').textContent = '正在读取登录安全配置…';
    $('bootstrap-message').textContent = ''; $('cap-help').hidden = false;
    $('cap-retry').disabled = true;
    try {
      const setup = await api('/api/bootstrap');
      if (generation !== bootstrapGeneration || state.me) return;
      $('bootstrap-message').textContent = setup.setup_message || '';
      state.capEnabled = setup.cap_enabled !== false;
      $('cap-help').hidden = !state.capEnabled;
      if (!state.capEnabled) {
        state.bootstrapReady = true;
        $('cap-status').className = 'callout';
        $('cap-status').textContent = '警告：Cap CAPTCHA 已关闭。请输入管理员用户名和密码登录；公网暴露时建议启用 CAPTCHA 或限制访问来源。';
        updateLoginReady(); return;
      }
      if (!setup.cap_ready || !setup.cap_site_key || !setup.cap_endpoint || !setup.cap_widget_url) {
        $('cap-status').textContent = 'Cap 尚未就绪，登录已锁定。请在部署主机完成本地初始化后重新连接。';
        $('bootstrap-help').open = true; return;
      }
      const endpoint = new URL(setup.cap_endpoint, location.origin);
      if (endpoint.origin !== location.origin || !endpoint.pathname.startsWith('/captcha/')) throw new Error('Cap 验证端点必须由本地 /captcha/ 提供。');
      await loadWidget(setup.cap_widget_url);
      if (generation !== bootstrapGeneration || state.me) return;
      const widget = node('cap-widget'); widget.setAttribute('data-cap-api-endpoint', endpoint.href);
      const labels = {
        'initial-state': '点击进行安全验证',
        'verifying-label': '正在验证…',
        'solved-label': '验证通过',
        'error-label': '验证失败，请重试',
        'verify-aria-label': '开始安全验证',
        'verifying-aria-label': '安全验证进行中',
        'verified-aria-label': '安全验证已通过',
        'error-aria-label': '安全验证失败',
        'wasm-disabled': '请启用浏览器 WebAssembly 以完成安全验证',
        'required-label': '请先完成安全验证'
      };
      for (const [key, label] of Object.entries(labels)) widget.setAttribute('data-cap-i18n-' + key, label);
      const currentWidget = () => generation === bootstrapGeneration && !state.me && state.capEnabled && widget.isConnected;
      widget.addEventListener('solve', event => {
        if (!currentWidget()) return;
        state.capToken = typeof event.detail?.token === 'string' ? event.detail.token : '';
        $('cap-status').textContent = state.capToken ? '验证通过，请登录。' : '验证结果无效，请重新验证。'; updateLoginReady();
      });
      for (const eventName of ['error', 'reset', 'expire', 'expired']) widget.addEventListener(eventName, () => {
        if (!currentWidget()) return;
        state.capToken = ''; $('cap-status').textContent = '验证已失效或失败，请重新完成验证；如无法恢复，请重新连接验证服务。'; updateLoginReady();
      });
      $('cap-container').hidden = false; $('cap-container').append(widget);
      state.bootstrapReady = true; state.capReady = true;
      $('cap-status').textContent = `请完成本地 Cap 验证（强度 ${setup.cap_level}）。`; updateLoginReady();
    } catch (error) {
      if (generation === bootstrapGeneration && !state.me) {
        state.bootstrapReady = false; state.capReady = false; state.capToken = ''; updateLoginReady();
        $('cap-container').replaceChildren(); $('cap-container').hidden = true;
        $('cap-status').textContent = error.message; $('bootstrap-help').open = true;
      }
    } finally { if (generation === bootstrapGeneration) $('cap-retry').disabled = false; }
  }
  $('login-form').addEventListener('submit', async event => {
    event.preventDefault();
    if (!loginReady() || !$('login-form').reportValidity()) return;
    $('login-submit').disabled = true; $('login-error').textContent = '';
    try {
      await api('/api/login', {method: 'POST', body: {username: $('login-username').value.trim(), password: $('login-password').value, cap_token: state.capToken}});
      $('login-password').value = ''; await enterApp();
    } catch (error) {
      $('login-password').value = ''; $('login-error').textContent = error.message;
      await bootstrap();
    }
  });
  $('cap-retry').addEventListener('click', () => bootstrap().catch(showError));
  $('logout').addEventListener('click', async () => {
    try {
      if (state.dirty && !await confirmAction('退出登录', '未保存的配置更改将丢失。确认退出？')) return;
      await api('/api/logout', {method: 'POST', body: {}}); showLogin();
    } catch (error) { showError(error); }
  });
  $('theme-toggle').addEventListener('click', () => {
    const dark = document.body.dataset.theme ? document.body.dataset.theme === 'dark' : matchMedia('(prefers-color-scheme: dark)').matches;
    document.body.dataset.theme = dark ? 'light' : 'dark';
  });
  window.addEventListener('hashchange', () => navigate().catch(showError));
  window.addEventListener('beforeunload', event => { if (state.dirty) { event.preventDefault(); event.returnValue = ''; } });
  enterApp().catch(() => showLogin());
})();
