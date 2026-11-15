(() => {
  'use strict';

  const DEFAULT_TEMPLATE = '{title}\n{author}\n{content}\n{url}';
  const SECRET_SENTINEL = '********';

  const STYLED_VARS = [
    { token: '{title}', label: '【标题】', desc: '带书名/方括号标题块' },
    { token: '{author}', label: '作者：昵称', desc: '带前缀作者行' },
    { token: '{content}', label: '正文摘要', desc: '优先 Markdown，与标题相同时自动去重' },
    { token: '{url}', label: '链接：URL', desc: '按全局去追踪策略格式化链接' },
    { token: '{clean_url}', label: '净化链接', desc: '始终剥离分享追踪参数' },
    { token: '{platform}', label: '[平台名]', desc: '例如 [哔哩哔哩]' },
    { token: '{type}', label: '[类型]', desc: '例如 [视频] / [图集]' },
    { token: '{media_stats}', label: '媒体统计', desc: '例如 共 1 个视频' },
  ];

  const RAW_VARS = [
    { token: '{raw_title}', label: '纯标题' },
    { token: '{raw_author}', label: '纯作者' },
    { token: '{raw_content}', label: '纯正文' },
    { token: '{raw_url}', label: '纯链接' },
    { token: '{raw_clean_url}', label: '纯净化链接' },
    { token: '{raw_platform}', label: '纯平台名' },
    { token: '{raw_type}', label: '纯类型名' },
  ];

  const ALL_TOKENS = [
    '{raw_clean_url}', '{raw_platform}', '{raw_content}', '{raw_author}',
    '{raw_title}', '{raw_type}', '{raw_url}', '{media_stats}',
    '{clean_url}', '{platform}', '{content}', '{author}',
    '{title}', '{type}', '{url}',
  ];

  const TEMPLATE_PRESETS = [
    { name: '标准完整', value: '{title}\n{author}\n{content}\n{url}' },
    { name: '仅标题', value: '{title}' },
    { name: '标题 + 链接', value: '{title}\n{url}' },
    { name: '精简卡片', value: '{platform} {type}\n{title}\n{author}\n{media_stats}\n{clean_url}' },
  ];

  const ROUTE_META = {
    overview: {
      title: '运行概览与沙盒',
      subtitle: '实时掌握解析引擎运行指标，在线测试链接解析与消息块排版效果',
    },
    tasks: {
      title: '任务与缓存中心',
      subtitle: '追踪历史解析投递队列状态，精细化管理本地媒体缓存文件',
    },
    platforms: {
      title: '平台与消息模板',
      subtitle: '自由组合每个平台的消息区块变量、自定义文案、封面开关与平台专属凭据',
    },
    onebot: {
      title: '机器人与群策略',
      subtitle: '管理 OneBot v11 反向 WebSocket 接入、引用回复、会话冷却及黑白名单',
    },
    media: {
      title: '媒体与网络代理',
      subtitle: '配置视频画质、体积配额、FFmpeg 并发转码与分流代理连通性测试',
    },
    system: {
      title: '系统与安全设置',
      subtitle: '配置监听端口与 TLS 证书、防暴力破解封禁策略、管理员凭据与配置导入导出',
    },
  };

  const state = {
    theme: localStorage.getItem('multiparse_theme') || (window.matchMedia('(prefers-color-scheme: dark)').matches ? 'dark' : 'light'),
    me: null,
    csrf: '',
    route: 'overview',
    tasksSubTab: 'tasks',
    tasksFilter: 'all',
    tasksQuery: '',
    systemSubTab: 'server',
    platformFilter: 'all',
    platformQuery: '',
    drawerPlatformId: '',
    proxyTestPlatform: 'youtube',
    rotatedToken: '',
    sandboxUrl: '',
    sandboxTemplate: '',
    sandboxLoading: false,
    sandboxResult: null,
    status: null,
    platforms: [],
    config: null,
    draft: null,
    tasks: [],
    cache: [],
    bans: [],
    pollTimer: null,
    reauthCallback: null,
  };

  const $ = (id) => document.getElementById(id);

  function esc(val) {
    return String(val ?? '')
      .replace(/&/g, '&amp;')
      .replace(/</g, '&lt;')
      .replace(/>/g, '&gt;')
      .replace(/"/g, '&quot;')
      .replace(/'/g, '&#39;');
  }

  function deepClone(obj) {
    return obj == null ? obj : JSON.parse(JSON.stringify(obj));
  }

  function deepEqual(a, b) {
    return JSON.stringify(a) === JSON.stringify(b);
  }

  function formatBytes(bytes) {
    const n = Number(bytes || 0);
    if (n <= 0) return '0 B';
    const units = ['B', 'KiB', 'MiB', 'GiB', 'TiB'];
    const i = Math.min(Math.floor(Math.log(n) / Math.log(1024)), units.length - 1);
    return `${(n / (1024 ** i)).toFixed(i === 0 ? 0 : 2)} ${units[i]}`;
  }

  function formatDuration(sec) {
    const s = Math.max(0, Math.floor(Number(sec || 0)));
    const d = Math.floor(s / 86400);
    const h = Math.floor((s % 86400) / 3600);
    const m = Math.floor((s % 3600) / 60);
    if (d > 0) return `${d}天 ${h}小时 ${m}分`;
    if (h > 0) return `${h}小时 ${m}分`;
    if (m > 0) return `${m}分 ${s % 60}秒`;
    return `${s}秒`;
  }

  function formatTime(iso) {
    if (!iso) return '-';
    const d = new Date(iso);
    if (Number.isNaN(d.getTime())) return String(iso);
    return d.toLocaleString('zh-CN', { hour12: false });
  }

  function platformName(id) {
    const p = state.platforms.find((item) => item.id === id);
    return p ? p.name : id;
  }

  function typeName(t) {
    const map = { video: '视频', image: '图集', article: '文章', audio: '音频', livephoto: '实况图' };
    return map[t] || t || '内容';
  }

  function toast(msg, kind = 'info') {
    const root = $('toast-root');
    if (!root) return;
    const el = document.createElement('div');
    el.className = `toast ${kind}`;
    el.textContent = msg;
    root.appendChild(el);
    setTimeout(() => {
      el.remove();
    }, 3600);
  }

  function applyTheme(theme) {
    state.theme = theme === 'dark' ? 'dark' : 'light';
    document.documentElement.setAttribute('data-theme', state.theme);
    localStorage.setItem('multiparse_theme', state.theme);
    const icon = state.theme === 'dark'
      ? '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8"><circle cx="12" cy="12" r="4"/><path d="M12 2v2m0 16v2M4.93 4.93l1.41 1.41m11.32 11.32 1.41 1.41M2 12h2m16 0h2M4.93 19.07l1.41-1.41m11.32-11.32 1.41-1.41" stroke-linecap="round"/></svg>'
      : '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8"><path d="M21 12.79A9 9 0 1 1 11.21 3 7 7 0 0 0 21 12.79Z" stroke-linejoin="round"/></svg>';
    for (const id of ['theme-toggle-btn', 'auth-theme-btn']) {
      const btn = $(id);
      if (btn) btn.innerHTML = icon;
    }
  }

  async function api(path, options = {}) {
    const method = options.method || 'GET';
    const headers = Object.assign({}, options.headers || {});
    if (options.body !== undefined) {
      headers['Content-Type'] = 'application/json';
    }
    if (method !== 'GET' && state.csrf) {
      headers['X-CSRF-Token'] = state.csrf;
    }
    const res = await fetch(path, {
      method,
      headers,
      credentials: 'same-origin',
      body: options.body !== undefined ? JSON.stringify(options.body) : undefined,
    });
    let data = null;
    const ct = res.headers.get('content-type') || '';
    if (ct.includes('application/json')) {
      data = await res.json().catch(() => null);
    }
    if (!res.ok) {
      if (res.status === 401 && path !== '/api/login' && path !== '/api/me') {
        showAuthView();
      }
      const err = new Error((data && data.error) || `请求失败 (HTTP ${res.status})`);
      err.status = res.status;
      throw err;
    }
    return data;
  }

  // 与后端 sensitive(old, c) 完全对齐的敏感变更判定
  function isSensitiveDraft(oldCfg, draftCfg) {
    if (!oldCfg || !draftCfg) return false;
    if (!deepEqual(oldCfg.server, draftCfg.server)) return true;
    if (!deepEqual(oldCfg.security, draftCfg.security)) return true;
    if (!deepEqual(oldCfg.onebot, draftCfg.onebot)) return true;
    if (!deepEqual(oldCfg.proxy, draftCfg.proxy)) return true;
    const ids = new Set([...Object.keys(oldCfg.platforms || {}), ...Object.keys(draftCfg.platforms || {})]);
    for (const id of ids) {
      const a = (oldCfg.platforms && oldCfg.platforms[id] && oldCfg.platforms[id].cookie) || '';
      const b = (draftCfg.platforms && draftCfg.platforms[id] && draftCfg.platforms[id].cookie) || '';
      if (a !== b) return true;
    }
    return false;
  }

  function isDirty() {
    if (!state.config || !state.draft) return false;
    return !deepEqual(state.config, state.draft);
  }

  function updateDirtyBar() {
    const bar = $('dirty-bar');
    if (!bar) return;
    if (!isDirty()) {
      bar.classList.add('hidden');
      return;
    }
    bar.classList.remove('hidden');
    const badge = $('dirty-sensitivity-badge');
    if (isSensitiveDraft(state.config, state.draft)) {
      badge.className = 'badge badge-warn';
      badge.textContent = '含敏感配置 · 保存时需验证管理员密码';
    } else {
      badge.className = 'badge badge-info';
      badge.textContent = '常规变更 · 免密一键保存';
    }
  }

  // 前端实时消息块渲染器（与 internal/policy/template.go 保持一致）
  function renderLocalPreview(tmpl, sample, cleanUrlEnabled = true) {
    if (!tmpl || !tmpl.trim()) return '';
    const normalized = tmpl.replace(/\r\n/g, '\n');
    const rawTitle = (sample.title || '').trim();
    const rawAuthor = (sample.author || '').trim();
    let rawContent = (sample.content || '').trim();

    const hasTitle = normalized.includes('{title}') || normalized.includes('{raw_title}');
    const hasContent = normalized.includes('{content}') || normalized.includes('{raw_content}');
    if (hasTitle && hasContent && rawTitle && rawContent === rawTitle) {
      rawContent = '';
    }

    const cleanUrl = sample.clean_url || sample.url || '';
    const displayUrl = cleanUrlEnabled ? cleanUrl : (sample.url || cleanUrl);
    const rawPlatform = sample.platform_name || '哔哩哔哩';
    const rawType = sample.type_label || '视频';
    const mediaStats = sample.media_stats || '';

    const values = {
      '{raw_title}': rawTitle,
      '{raw_author}': rawAuthor,
      '{raw_content}': rawContent,
      '{raw_url}': displayUrl,
      '{raw_clean_url}': cleanUrl,
      '{raw_platform}': rawPlatform,
      '{raw_type}': rawType,
      '{media_stats}': mediaStats,
      '{title}': rawTitle ? `【${rawTitle}】` : '',
      '{author}': rawAuthor ? `作者：${rawAuthor}` : '',
      '{content}': rawContent,
      '{url}': displayUrl ? `链接：${displayUrl}` : '',
      '{clean_url}': cleanUrl ? `链接：${cleanUrl}` : '',
      '{platform}': rawPlatform ? `[${rawPlatform}]` : '',
      '{type}': rawType ? `[${rawType}]` : '',
    };

    const out = [];
    for (const line of normalized.split('\n')) {
      let referenced = 0;
      let nonEmpty = 0;
      for (const token of ALL_TOKENS) {
        if (line.includes(token)) {
          referenced++;
          if ((values[token] || '').trim() !== '') nonEmpty++;
        }
      }
      if (referenced > 0) {
        if (nonEmpty === 0) continue;
        let rendered = line;
        for (const token of ALL_TOKENS) {
          rendered = rendered.split(token).join(values[token] || '');
        }
        if (rendered.trim() === '') continue;
        out.push(rendered);
      } else {
        out.push(line);
      }
    }
    return out.join('\n').replace(/^\n+|\n+$/g, '');
  }

  function showAuthView() {
    stopPolling();
    state.me = null;
    state.csrf = '';
    $('app-shell').classList.add('hidden');
    $('force-pwd-view').classList.add('hidden');
    $('auth-view').classList.remove('hidden');
    closeDrawer();
  }

  function showForcePasswordView(username) {
    stopPolling();
    $('auth-view').classList.add('hidden');
    $('app-shell').classList.add('hidden');
    $('force-pwd-view').classList.remove('hidden');
    $('force-username').value = username || 'admin';
    $('force-current-pwd').value = '';
    $('force-new-pwd').value = '';
  }

  async function enterConsole(me) {
    state.me = me;
    state.csrf = me.csrf_token || '';
    if (me.must_change_password) {
      showForcePasswordView(me.username);
      return;
    }
    $('auth-view').classList.add('hidden');
    $('force-pwd-view').classList.add('hidden');
    $('app-shell').classList.remove('hidden');
    $('current-admin-name').textContent = me.username || 'admin';
    await loadAllData(true);
    renderRoute();
    startPolling();
  }

  async function loadAllData(resetDraft = false) {
    const [platforms, cfg, status, tasks, cache, bans] = await Promise.all([
      api('/api/platforms'),
      api('/api/config'),
      api('/api/status').catch(() => null),
      api('/api/tasks?limit=200').catch(() => []),
      api('/api/cache').catch(() => []),
      api('/api/bans').catch(() => []),
    ]);
    state.platforms = platforms || [];
    state.config = cfg;
    if (resetDraft || !state.draft || !isDirty()) {
      state.draft = deepClone(cfg);
    }
    state.status = status;
    state.tasks = tasks || [];
    state.cache = cache || [];
    state.bans = bans || [];
    syncNavBadges();
    updateDirtyBar();
  }

  async function refreshRuntimeOnly() {
    try {
      const [status, tasks, cache, bans] = await Promise.all([
        api('/api/status'),
        api('/api/tasks?limit=200'),
        api('/api/cache'),
        api('/api/bans'),
      ]);
      state.status = status;
      state.tasks = tasks || [];
      state.cache = cache || [];
      state.bans = bans || [];
      syncNavBadges();
      if (state.route === 'overview') {
        updateOverviewKPIsOnly();
      } else if (state.route === 'tasks') {
        renderRoute();
      }
    } catch {
      // ignore transient background poll errors
    }
  }

  function syncNavBadges() {
    if (state.status && state.status.version) {
      $('sidebar-version').textContent = `v${state.status.version}`;
    }
    const botsCount = (state.status && Array.isArray(state.status.bots)) ? state.status.bots.length : 0;
    const botsBadge = $('nav-badge-bots');
    botsBadge.textContent = String(botsCount);

    const activeCount = state.status ? Number(state.status.active_tasks || 0) + Number(state.status.queue_size || 0) : 0;
    const tasksBadge = $('nav-badge-tasks');
    tasksBadge.textContent = String(activeCount);
    tasksBadge.classList.toggle('hidden', activeCount === 0);

    const bansBadge = $('nav-badge-bans');
    bansBadge.textContent = String(state.bans.length);
    bansBadge.classList.toggle('hidden', state.bans.length === 0);
  }

  function startPolling() {
    stopPolling();
    state.pollTimer = setInterval(refreshRuntimeOnly, 8000);
  }

  function stopPolling() {
    if (state.pollTimer) {
      clearInterval(state.pollTimer);
      state.pollTimer = null;
    }
  }

  function openReauthModal(title, desc, onConfirm) {
    $('reauth-title').textContent = title || '安全验证';
    $('reauth-desc').textContent = desc || '请输入当前管理员密码确认此敏感操作。';
    $('reauth-password').value = '';
    state.reauthCallback = onConfirm;
    $('reauth-modal').classList.remove('hidden');
    setTimeout(() => $('reauth-password').focus(), 30);
  }

  function closeReauthModal() {
    $('reauth-modal').classList.add('hidden');
    $('reauth-password').value = '';
    state.reauthCallback = null;
  }

  async function saveDraftConfig() {
    if (!state.draft) return;
    if (!isDirty()) {
      toast('当前没有待保存的更改');
      return;
    }
    const execSave = async (currentPassword = '') => {
      const headers = {};
      if (currentPassword) {
        headers['X-Current-Password'] = currentPassword;
      }
      await api('/api/config', { method: 'PUT', headers, body: state.draft });
      const fresh = await api('/api/config');
      state.config = fresh;
      state.draft = deepClone(fresh);
      updateDirtyBar();
      renderRoute();
      if (state.drawerPlatformId) {
        renderDrawerBody(state.drawerPlatformId);
      }
      toast('配置已保存并立即生效', 'success');
    };

    if (isSensitiveDraft(state.config, state.draft)) {
      openReauthModal(
        '确认保存敏感配置',
        '本次更改包含监听地址、登录安全、OneBot 鉴权、代理或平台 Cookie，请输入当前管理员密码确认。',
        execSave
      );
      return;
    }
    try {
      await execSave('');
    } catch (err) {
      toast(err.message, 'error');
    }
  }

  function setRoute(route) {
    if (!ROUTE_META[route]) route = 'overview';
    state.route = route;
    for (const btn of document.querySelectorAll('#sidebar-nav .nav-item')) {
      btn.classList.toggle('active', btn.dataset.route === route);
    }
    $('page-title').textContent = ROUTE_META[route].title;
    $('page-subtitle').textContent = ROUTE_META[route].subtitle;
    $('sidebar').classList.remove('open');
    $('sidebar-backdrop').classList.add('hidden');
    renderRoute();
  }

  function renderVarToolbar(targetInputId) {
    const styledBtns = STYLED_VARS.map((v) =>
      `<button type="button" class="var-pill" data-action="insert-var" data-target="${esc(targetInputId)}" data-token="${esc(v.token)}" title="${esc(v.desc || '')}"><span>${esc(v.label)}</span><code>${esc(v.token)}</code></button>`
    ).join('');
    const rawBtns = RAW_VARS.map((v) =>
      `<button type="button" class="var-pill" data-action="insert-var" data-target="${esc(targetInputId)}" data-token="${esc(v.token)}"><span>${esc(v.label)}</span><code>${esc(v.token)}</code></button>`
    ).join('');
    return `
      <div class="var-toolbar">
        <div class="var-group-label">点击插入消息块变量（空值行会自动智能折叠，不留空行）：</div>
        ${styledBtns}
        <div class="var-group-label" style="margin-top:4px;">纯文本原子变量（适合搭配自定义前缀与 Emoji）：</div>
        ${rawBtns}
      </div>
    `;
  }

  function renderRoute() {
    const container = $('page-content');
    if (!container || !state.draft) return;
    switch (state.route) {
      case 'overview':
        container.innerHTML = buildOverviewHTML();
        break;
      case 'tasks':
        container.innerHTML = buildTasksAndCacheHTML();
        break;
      case 'platforms':
        container.innerHTML = buildPlatformsHTML();
        break;
      case 'onebot':
        container.innerHTML = buildOneBotHTML();
        break;
      case 'media':
        container.innerHTML = buildMediaHTML();
        break;
      case 'system':
        container.innerHTML = buildSystemHTML();
        break;
    }
  }

  function updateOverviewKPIsOnly() {
    const s = state.status || {};
    const elUptime = document.getElementById('kpi-uptime');
    if (!elUptime) return;
    elUptime.textContent = formatDuration(s.uptime_seconds || 0);
    const bots = Array.isArray(s.bots) ? s.bots : [];
    const elBots = document.getElementById('kpi-bots');
    if (elBots) elBots.textContent = `${bots.length} 在线`;
    const elTasks = document.getElementById('kpi-tasks');
    if (elTasks) elTasks.textContent = `${s.active_tasks || 0} / ${s.queue_size || 0}`;
    const elCache = document.getElementById('kpi-cache');
    if (elCache) elCache.textContent = formatBytes(s.cache_bytes || 0);
  }

  function buildOverviewHTML() {
    const s = state.status || {};
    const bots = Array.isArray(s.bots) ? s.bots : [];
    const maxDisk = Math.max(1, Number(state.draft.media.max_disk_bytes || 1));
    const usedPct = Math.min(100, Math.round((Number(s.cache_bytes || 0) / maxDisk) * 100));
    const enabledPlatforms = Object.values(state.draft.platforms || {}).filter((p) => p.enabled).length;

    let sandboxOut = '';
    if (state.sandboxResult) {
      const r = state.sandboxResult.result || {};
      const rendered = state.sandboxResult.rendered || '';
      sandboxOut = `
        <div class="grid-2" style="margin-top:14px;">
          <div class="card" style="background:var(--surface-alt);padding:14px;">
            <div class="row-between" style="margin-bottom:8px;">
              <strong>结构化解析结果</strong>
              <div class="row-gap-sm">
                <span class="badge badge-info">${esc(platformName(r.platform))}</span>
                <span class="badge badge-neutral">${esc(typeName(r.type))} · ${Number(state.sandboxResult.media_count || 0)} 项媒体</span>
              </div>
            </div>
            <div class="stack-sm text-sm">
              <div><span class="muted">标题：</span>${esc(r.title || '(无)')}</div>
              <div><span class="muted">作者：</span>${esc(r.author || '(无)')}</div>
              <div><span class="muted">净化链接：</span><span class="mono text-xs">${esc(state.sandboxResult.clean_url || r.source_url || '')}</span></div>
              <div><span class="muted">生效模板：</span><code class="mono text-xs">${esc(state.sandboxResult.effective_template || '(不输出文本)')}</code></div>
            </div>
          </div>
          <div class="chat-preview-box">
            <div class="chat-preview-header">
              <span>OneBot 投递消息仿真预览</span>
              <span>${state.sandboxResult.show_cover ? '附带封面图' : '无封面图'}</span>
            </div>
            <div class="chat-bubble">${state.draft.policy.group_reply ? '<div class="chat-reply-quote">回复 群成员的消息</div>' : ''}${state.sandboxResult.show_cover && r.type === 'video' ? '<div class="chat-cover-mock">🖼️ [视频封面图]</div>\n' : ''}${rendered ? esc(rendered) : '<span class="muted">（当前模板模式下仅发送媒体，不输出文本块）</span>'}</div>
          </div>
        </div>
      `;
    }

    const recentTasks = state.tasks.slice(0, 6);
    const recentRows = recentTasks.length === 0
      ? '<tr><td colspan="5" class="muted" style="text-align:center;padding:20px;">暂无近期解析任务</td></tr>'
      : recentTasks.map((t) => `
          <tr>
            <td><span class="badge badge-neutral">${esc(platformName(t.platform))}</span></td>
            <td class="cell-url mono text-xs" title="${esc(t.url)}">${esc(t.url)}</td>
            <td>${renderTaskStatusBadge(t.status)}</td>
            <td class="text-xs muted">${esc(formatTime(t.updated_at || t.created_at))}</td>
            <td class="text-xs muted">${esc(t.message || '-')}</td>
          </tr>
        `).join('');

    return `
      ${s.cleanup_error ? `<div class="alert alert-warn">缓存清理提示：${esc(s.cleanup_error)}</div>` : ''}
      <section class="kpi-grid">
        <div class="kpi-card">
          <span class="kpi-label">引擎运行时长</span>
          <div id="kpi-uptime" class="kpi-value">${esc(formatDuration(s.uptime_seconds || 0))}</div>
          <div class="kpi-sub mono">${esc(s.listen || '-')} · 已启用 ${enabledPlatforms}/${state.platforms.length} 平台</div>
        </div>
        <div class="kpi-card">
          <span class="kpi-label">OneBot 机器人连接</span>
          <div id="kpi-bots" class="kpi-value">${bots.length} 在线</div>
          <div class="kpi-sub">${bots.length > 0 ? '账号：' + esc(bots.map((b) => b.self_id).join(', ')) : '等待 NapCat / OneBot 客户端接入'}</div>
        </div>
        <div class="kpi-card">
          <span class="kpi-label">活跃执行 / 队列等待</span>
          <div id="kpi-tasks" class="kpi-value">${Number(s.active_tasks || 0)} / ${Number(s.queue_size || 0)}</div>
          <div class="kpi-sub">并发槽位上限：${Number(state.draft.media.concurrency || 2)} · 转码并发：${Number(state.draft.media.transcode_concurrency || 1)}</div>
        </div>
        <div class="kpi-card">
          <span class="kpi-label">本地媒体缓存占用</span>
          <div id="kpi-cache" class="kpi-value">${esc(formatBytes(s.cache_bytes || 0))}</div>
          <div class="progress-track"><div class="progress-fill" style="width:${usedPct}%"></div></div>
          <div class="kpi-sub">${Number(s.cache_entries || 0)} 条缓存 · 配额 ${esc(formatBytes(maxDisk))} (${usedPct}%)</div>
        </div>
      </section>

      <section class="card">
        <div class="card-header">
          <div>
            <h3>在线解析与消息模板预览沙盒</h3>
            <p>支持直接粘贴带文案的分享口令（如抖音/B站/小红书分享文本）或纯链接，免下载快速验证解析结果与消息块排版</p>
          </div>
        </div>
        <div class="stack-md">
          <div class="input-group">
            <input id="sandbox-url-input" type="text" value="${esc(state.sandboxUrl)}" placeholder="粘贴任意支持平台的链接或分享口令，例如 https://www.bilibili.com/video/BV1... 或抖音分享文案">
            <button type="button" class="btn btn-primary" data-action="sandbox-preview" ${state.sandboxLoading ? 'disabled' : ''}>${state.sandboxLoading ? '解析中…' : '预览消息与元数据'}</button>
            <button type="button" class="btn btn-secondary" data-action="sandbox-enqueue" ${state.sandboxLoading ? 'disabled' : ''} title="提交至后台完整解析并缓存媒体文件">入队缓存测试</button>
          </div>
          <details ${state.sandboxTemplate ? 'open' : ''}>
            <summary class="text-xs muted" style="cursor:pointer;user-select:none;">可选：输入临时模板覆盖测试（留空则自动使用该平台当前已保存的生效模板）</summary>
            <div class="stack-sm" style="margin-top:10px;">
              ${renderVarToolbar('sandbox-template-input')}
              <textarea id="sandbox-template-input" rows="3" placeholder="留空默认按平台生效模板渲染，或在此临时输入如 {title}">${esc(state.sandboxTemplate)}</textarea>
            </div>
          </details>
        </div>
        ${sandboxOut}
      </section>

      <div class="grid-2">
        <section class="card">
          <div class="card-header">
            <div>
              <h3>OneBot v11 反向 WebSocket 接入信息</h3>
              <p>在 NapCat / LLOneBot / Lagrange 中配置反向 WebSocket 地址与 Bearer Token</p>
            </div>
            <button type="button" class="btn btn-ghost btn-sm" data-action="goto-route" data-route="onebot">前往配置 →</button>
          </div>
          <div class="stack-sm">
            <div class="input-group">
              <input type="text" class="mono" readonly value="${esc(s.onebot_ws_url || '')}">
              <button type="button" class="btn btn-secondary btn-sm" data-action="copy-text" data-copy="${esc(s.onebot_ws_url || '')}">复制 WS 地址</button>
            </div>
            <p class="field-hint">数据目录：<code class="mono">${esc(s.data_dir || './data')}</code> · 缓存目录：<code class="mono">${esc(s.cache_dir || './media')}</code></p>
          </div>
        </section>

        <section class="card">
          <div class="card-header">
            <div>
              <h3>最近解析任务动态</h3>
              <p>展示最近触发的 6 条任务状态</p>
            </div>
            <button type="button" class="btn btn-ghost btn-sm" data-action="goto-route" data-route="tasks">查看全部 →</button>
          </div>
          <div class="table-wrap">
            <table>
              <thead><tr><th>平台</th><th>链接</th><th>状态</th><th>更新时间</th><th>备注</th></tr></thead>
              <tbody>${recentRows}</tbody>
            </table>
          </div>
        </section>
      </div>
    `;
  }

  function renderTaskStatusBadge(status) {
    const map = {
      completed: ['已完成', 'badge-success'],
      running: ['处理中', 'badge-info'],
      queued: ['排队中', 'badge-info'],
      failed: ['失败', 'badge-danger'],
      rejected: ['策略拦截', 'badge-warn'],
      cancelled: ['已取消', 'badge-neutral'],
    };
    const [label, cls] = map[status] || [status || '未知', 'badge-neutral'];
    return `<span class="badge ${cls}">${esc(label)}</span>`;
  }

  function buildTasksAndCacheHTML() {
    const isTasks = state.tasksSubTab === 'tasks';
    const q = state.tasksQuery.trim().toLowerCase();

    let bodyHTML = '';
    if (isTasks) {
      const filtered = state.tasks.filter((t) => {
        if (state.tasksFilter !== 'all' && t.status !== state.tasksFilter) return false;
        if (!q) return true;
        return (t.url || '').toLowerCase().includes(q)
          || (t.platform || '').toLowerCase().includes(q)
          || platformName(t.platform).toLowerCase().includes(q)
          || String(t.group_id || '').includes(q)
          || (t.message || '').toLowerCase().includes(q);
      });
      const rows = filtered.length === 0
        ? '<tr><td colspan="6" class="muted" style="text-align:center;padding:28px;">没有匹配的任务记录</td></tr>'
        : filtered.map((t) => `
            <tr>
              <td><span class="badge badge-neutral">${esc(platformName(t.platform))}</span></td>
              <td class="cell-url mono text-xs" title="${esc(t.url)}">${esc(t.url)}</td>
              <td class="text-xs">${t.group_id ? `群 ${esc(t.group_id)}` : (t.user_id ? `私聊 ${esc(t.user_id)}` : '管理台测试')}</td>
              <td>${renderTaskStatusBadge(t.status)}</td>
              <td class="text-xs muted">${esc(formatTime(t.updated_at || t.created_at))}</td>
              <td class="text-xs ${t.status === 'failed' ? 'muted' : 'muted'}">${esc(t.message || '-')}</td>
            </tr>
          `).join('');

      bodyHTML = `
        <div class="row-between" style="margin-bottom:14px;">
          <div class="segmented">
            ${[
              ['all', '全部'],
              ['completed', '已完成'],
              ['running', '处理中'],
              ['failed', '失败'],
              ['rejected', '已拦截'],
            ].map(([val, label]) => `<button type="button" class="seg-btn ${state.tasksFilter === val ? 'active' : ''}" data-action="task-filter" data-filter="${val}">${label}</button>`).join('')}
          </div>
          <div style="width:280px;max-width:100%;">
            <input id="tasks-search-input" type="search" value="${esc(state.tasksQuery)}" placeholder="搜索链接 / 平台 / 群号 / 错误原因…">
          </div>
        </div>
        <div class="table-wrap">
          <table>
            <thead><tr><th>平台</th><th>源链接</th><th>来源会话</th><th>状态</th><th>更新时间</th><th>详情信息</th></tr></thead>
            <tbody>${rows}</tbody>
          </table>
        </div>
      `;
    } else {
      const filteredCache = state.cache.filter((c) => {
        if (!q) return true;
        return (c.url || '').toLowerCase().includes(q)
          || (c.title || '').toLowerCase().includes(q)
          || (c.platform || '').toLowerCase().includes(q)
          || platformName(c.platform).toLowerCase().includes(q);
      });
      const rows = filteredCache.length === 0
        ? '<tr><td colspan="7" class="muted" style="text-align:center;padding:28px;">当前暂无本地缓存记录</td></tr>'
        : filteredCache.map((c) => `
            <tr>
              <td><span class="badge badge-neutral">${esc(platformName(c.platform))}</span></td>
              <td style="max-width:260px;">
                <div class="truncate" title="${esc(c.title || c.url)}"><strong>${esc(c.title || '(无标题)')}</strong></div>
                <div class="cell-url mono text-xs muted" title="${esc(c.url)}">${esc(c.url)}</div>
              </td>
              <td><span class="badge badge-info">${esc(typeName(c.type))}</span></td>
              <td class="mono text-xs">${Number(c.file_count || 0)} 个文件</td>
              <td class="mono text-xs">${esc(formatBytes(c.size || c.bytes || 0))}</td>
              <td class="text-xs muted">
                <div>创建：${esc(formatTime(c.created_at))}</div>
                <div>过期：${esc(formatTime(c.expires_at))}</div>
              </td>
              <td>
                <button type="button" class="btn btn-danger btn-xs" data-action="delete-cache-item" data-key="${esc(c.key)}">删除</button>
              </td>
            </tr>
          `).join('');

      bodyHTML = `
        <div class="row-between" style="margin-bottom:14px;">
          <div style="width:280px;max-width:100%;">
            <input id="tasks-search-input" type="search" value="${esc(state.tasksQuery)}" placeholder="搜索缓存标题 / 链接 / 平台…">
          </div>
          <div class="row-gap-sm">
            <button type="button" class="btn btn-secondary btn-sm" data-action="clear-cache" data-all="false">清理已过期缓存</button>
            <button type="button" class="btn btn-danger btn-sm" data-action="clear-cache" data-all="true">清空全部缓存</button>
          </div>
        </div>
        <div class="table-wrap">
          <table>
            <thead><tr><th>平台</th><th>内容标题与链接</th><th>类型</th><th>文件数</th><th>占用体积</th><th>有效期</th><th>操作</th></tr></thead>
            <tbody>${rows}</tbody>
          </table>
        </div>
      `;
    }

    return `
      <section class="card">
        <div class="card-header">
          <div class="segmented">
            <button type="button" class="seg-btn ${isTasks ? 'active' : ''}" data-action="tasks-subtab" data-tab="tasks">解析任务记录 (${state.tasks.length})</button>
            <button type="button" class="seg-btn ${!isTasks ? 'active' : ''}" data-action="tasks-subtab" data-tab="cache">媒体缓存管理 (${state.cache.length})</button>
          </div>
        </div>
        ${bodyHTML}
      </section>
    `;
  }

  function buildPlatformsHTML() {
    const media = state.draft.media;
    const globalSample = {
      platform_name: '哔哩哔哩',
      type_label: '视频',
      title: '【4K】2026 年度技术架构演进与工程实践总结',
      author: '技术极客UP',
      content: '本期视频深入讲解多平台消息块自定义排版、Alpine 镜像瘦身与高可靠媒体流水线。',
      url: 'https://www.bilibili.com/video/BV1GJ411x7h7?p=1&vd_source=track123',
      clean_url: 'https://www.bilibili.com/video/BV1GJ411x7h7?p=1',
      media_stats: '共 1 个视频',
    };
    const globalPreview = media.show_info
      ? renderLocalPreview(media.message_template || DEFAULT_TEMPLATE, globalSample, media.clean_url)
      : '';

    const q = state.platformQuery.trim().toLowerCase();
    const filtered = state.platforms.filter((p) => {
      const pol = state.draft.platforms[p.id] || {};
      if (state.platformFilter === 'domestic' && pol.overseas) return false;
      if (state.platformFilter === 'overseas' && !pol.overseas) return false;
      if (state.platformFilter === 'enabled' && !pol.enabled) return false;
      if (state.platformFilter === 'custom_tpl' && pol.template_mode !== 'custom' && pol.template_mode !== 'none') return false;
      if (state.platformFilter === 'has_cookie' && !pol.cookie) return false;
      if (!q) return true;
      return p.name.toLowerCase().includes(q)
        || p.id.toLowerCase().includes(q)
        || (p.domains || []).some((d) => d.toLowerCase().includes(q));
    });

    const cards = filtered.map((p) => {
      const pol = state.draft.platforms[p.id] || { enabled: true, template_mode: 'inherit', cover_mode: 'inherit' };
      const tplBadge = pol.template_mode === 'custom'
        ? '<span class="badge badge-info">专属消息模板</span>'
        : (pol.template_mode === 'none' ? '<span class="badge badge-warn">仅发送媒体</span>' : '<span class="badge badge-neutral">跟随全局模板</span>');
      const cookieBadge = pol.cookie ? '<span class="badge badge-success">已配 Cookie</span>' : '';
      const overseasBadge = pol.overseas ? '<span class="badge badge-info">海外分流</span>' : '';
      const nsfwBadge = pol.nsfw_blocked ? '<span class="badge badge-danger">NSFW 拦截</span>' : '';
      const coverBadge = pol.cover_mode === 'off' ? '<span class="badge badge-neutral">无封面</span>' : (pol.cover_mode === 'on' ? '<span class="badge badge-info">强制封面</span>' : '');

      return `
        <div class="platform-card ${pol.enabled ? '' : 'disabled-card'}">
          <div class="platform-card-top">
            <div>
              <div class="platform-name">${esc(p.name)}</div>
              <div class="platform-id mono">${esc(p.id)} · ${(p.types || []).map(typeName).join(' / ')}</div>
            </div>
            <label class="switch" title="启用或停用 ${esc(p.name)}">
              <input type="checkbox" data-action="toggle-platform-enabled" data-platform="${esc(p.id)}" ${pol.enabled ? 'checked' : ''}>
              <span class="switch-slider"></span>
            </label>
          </div>
          <div class="platform-tags">
            ${tplBadge}
            ${overseasBadge}
            ${cookieBadge}
            ${coverBadge}
            ${nsfwBadge}
          </div>
          <div class="platform-card-footer">
            <span class="text-xs muted truncate mono">${esc((p.domains || []).slice(0, 2).join(', '))}</span>
            <button type="button" class="btn btn-secondary btn-xs" data-action="open-platform-drawer" data-platform="${esc(p.id)}">配置模板与策略</button>
          </div>
        </div>
      `;
    }).join('');

    const presetBtns = TEMPLATE_PRESETS.map((pr) =>
      `<button type="button" class="btn btn-secondary btn-xs" data-action="apply-global-preset" data-preset="${esc(pr.value)}">${esc(pr.name)}</button>`
    ).join('');

    return `
      <section class="card">
        <div class="card-header">
          <div>
            <h3>全局默认消息块模板与排版策略</h3>
            <p>所有未单独设置“专属模板”的平台默认继承以下消息块排版；您也可以点击下方任意平台卡片单独定制其输出变量</p>
          </div>
          <div class="row-gap-sm">${presetBtns}</div>
        </div>

        <div class="grid-3" style="margin-bottom:16px;">
          <div class="switch-row">
            <div class="switch-row-text">
              <strong>输出文本信息块</strong>
              <span>全局默认附带标题/作者/链接文本</span>
            </div>
            <label class="switch">
              <input type="checkbox" data-bind="media.show_info" ${media.show_info ? 'checked' : ''}>
              <span class="switch-slider"></span>
            </label>
          </div>
          <div class="switch-row">
            <div class="switch-row-text">
              <strong>附带视频封面图</strong>
              <span>图文首条消息展示视频高清封面</span>
            </div>
            <label class="switch">
              <input type="checkbox" data-bind="media.show_cover" ${media.show_cover ? 'checked' : ''}>
              <span class="switch-slider"></span>
            </label>
          </div>
          <div class="switch-row">
            <div class="switch-row-text">
              <strong>自动净化分享追踪参数</strong>
              <span>剥离 vd_source / spm_id_from / utm_* 等尾巴</span>
            </div>
            <label class="switch">
              <input type="checkbox" data-bind="media.clean_url" ${media.clean_url ? 'checked' : ''}>
              <span class="switch-slider"></span>
            </label>
          </div>
        </div>

        <div class="grid-2">
          <div class="stack-sm">
            ${renderVarToolbar('global-template-textarea')}
            <textarea id="global-template-textarea" rows="5" data-bind="media.message_template" placeholder="输入全局默认消息模板，每行一个消息块变量或自定义文本">${esc(media.message_template || DEFAULT_TEMPLATE)}</textarea>
          </div>
          <div class="chat-preview-box">
            <div class="chat-preview-header">
              <span>全局默认消息实时预览</span>
              <span>标题与正文重复时自动去重</span>
            </div>
            <div id="global-template-preview" class="chat-bubble">${state.draft.policy.group_reply ? '<div class="chat-reply-quote">回复 群成员的消息</div>' : ''}${media.show_cover ? '<div class="chat-cover-mock">🖼️ [视频封面图]</div>\n' : ''}${globalPreview ? esc(globalPreview) : '<span class="muted">（已关闭全局文本信息输出）</span>'}</div>
          </div>
        </div>
      </section>

      <section class="card">
        <div class="card-header">
          <div>
            <h3>各平台独立解析策略与自定义输出 (${filtered.length}/${state.platforms.length})</h3>
            <p>点击任一平台的「配置模板与策略」即可为该平台单独挑选输出区块（例如仅输出 <code>{title}</code>）、设置专属提示词或填入登录 Cookie</p>
          </div>
          <div class="row-gap-sm">
            <button type="button" class="btn btn-secondary btn-xs" data-action="bulk-platforms" data-op="enable-all">全部启用</button>
            <button type="button" class="btn btn-secondary btn-xs" data-action="bulk-platforms" data-op="reset-templates">全部恢复全局模板</button>
          </div>
        </div>

        <div class="row-between" style="margin-bottom:16px;">
          <div class="segmented">
            ${[
              ['all', `全部 (${state.platforms.length})`],
              ['domestic', '国内平台'],
              ['overseas', '海外平台'],
              ['enabled', '已启用'],
              ['custom_tpl', '已定制模板'],
              ['has_cookie', '已配 Cookie'],
            ].map(([val, label]) => `<button type="button" class="seg-btn ${state.platformFilter === val ? 'active' : ''}" data-action="platform-filter" data-filter="${val}">${label}</button>`).join('')}
          </div>
          <div style="width:260px;max-width:100%;">
            <input id="platform-search-input" type="search" value="${esc(state.platformQuery)}" placeholder="搜索平台名称 / ID / 域名…">
          </div>
        </div>

        <div class="platform-grid">${cards || '<p class="muted">没有符合筛选条件的平台</p>'}</div>
      </section>
    `;
  }

  function openDrawer(platformId) {
    const meta = state.platforms.find((p) => p.id === platformId);
    if (!meta || !state.draft) return;
    state.drawerPlatformId = platformId;
    $('drawer-platform-title').textContent = `${meta.name} (${meta.id})`;
    $('drawer-platform-domains').textContent = (meta.domains || []).join(' · ');
    renderDrawerBody(platformId);
    $('platform-drawer-backdrop').classList.remove('hidden');
    $('platform-drawer').classList.remove('hidden');
  }

  function closeDrawer() {
    state.drawerPlatformId = '';
    $('platform-drawer-backdrop').classList.add('hidden');
    $('platform-drawer').classList.add('hidden');
  }

  function renderDrawerBody(platformId) {
    const meta = state.platforms.find((p) => p.id === platformId);
    if (!meta || !state.draft) return;
    const pol = state.draft.platforms[platformId];
    if (!pol) return;

    const mode = pol.template_mode || 'inherit';
    const coverMode = pol.cover_mode || 'inherit';
    const effectiveShowCover = coverMode === 'on' ? true : (coverMode === 'off' ? false : state.draft.media.show_cover);
    const effectiveTpl = mode === 'none'
      ? ''
      : (mode === 'custom' ? (pol.message_template || '') : (state.draft.media.show_info ? (state.draft.media.message_template || DEFAULT_TEMPLATE) : ''));

    const sample = {
      platform_name: meta.name,
      type_label: typeName((meta.types && meta.types[0]) || 'video'),
      title: `${meta.name}示例作品标题`,
      author: '示例创作者',
      content: `${meta.name}正文内容摘要，支持任意组合标题、作者、统计与自定义文字。`,
      url: `https://${(meta.domains && meta.domains[0]) || 'example.com'}/post/10086?utm_source=share`,
      clean_url: `https://${(meta.domains && meta.domains[0]) || 'example.com'}/post/10086`,
      media_stats: '共 1 个视频',
    };
    const previewText = renderLocalPreview(effectiveTpl, sample, state.draft.media.clean_url);

    const presetBtns = TEMPLATE_PRESETS.map((pr) =>
      `<button type="button" class="btn btn-secondary btn-xs" data-action="apply-drawer-preset" data-preset="${esc(pr.value)}">${esc(pr.name)}</button>`
    ).join('');

    const hasSavedCookie = pol.cookie === SECRET_SENTINEL;

    $('drawer-body').innerHTML = `
      <div class="grid-2">
        <div class="switch-row">
          <div class="switch-row-text">
            <strong>启用该平台解析</strong>
            <span>关闭后不再自动解析此平台链接</span>
          </div>
          <label class="switch">
            <input type="checkbox" data-drawer-field="enabled" ${pol.enabled ? 'checked' : ''}>
            <span class="switch-slider"></span>
          </label>
        </div>
        <div class="switch-row">
          <div class="switch-row-text">
            <strong>归属海外分流</strong>
            <span>代理模式为「仅海外平台」时走代理</span>
          </div>
          <label class="switch">
            <input type="checkbox" data-drawer-field="overseas" ${pol.overseas ? 'checked' : ''}>
            <span class="switch-slider"></span>
          </label>
        </div>
        <div class="switch-row">
          <div class="switch-row-text">
            <strong>NSFW 风险拦截</strong>
            <span>开启后拒绝解析此平台内容</span>
          </div>
          <label class="switch">
            <input type="checkbox" data-drawer-field="nsfw_blocked" ${pol.nsfw_blocked ? 'checked' : ''}>
            <span class="switch-slider"></span>
          </label>
        </div>
        <div class="switch-row">
          <div class="switch-row-text">
            <strong>拒绝时保持静默</strong>
            <span>平台停用或拦截时不向群内发送提示</span>
          </div>
          <label class="switch">
            <input type="checkbox" data-drawer-field="silent" ${pol.silent ? 'checked' : ''}>
            <span class="switch-slider"></span>
          </label>
        </div>
      </div>

      <div class="field">
        <span class="field-label">视频封面图策略</span>
        <div class="segmented">
          <button type="button" class="seg-btn ${coverMode === 'inherit' ? 'active' : ''}" data-action="drawer-cover-mode" data-mode="inherit">跟随全局 (${state.draft.media.show_cover ? '开启' : '关闭'})</button>
          <button type="button" class="seg-btn ${coverMode === 'on' ? 'active' : ''}" data-action="drawer-cover-mode" data-mode="on">始终发送封面</button>
          <button type="button" class="seg-btn ${coverMode === 'off' ? 'active' : ''}" data-action="drawer-cover-mode" data-mode="off">不发封面图</button>
        </div>
      </div>

      <div class="field">
        <div class="row-between">
          <span class="field-label">消息块输出模式</span>
          ${mode === 'custom' ? `<div class="row-gap-sm">${presetBtns}</div>` : ''}
        </div>
        <div class="segmented">
          <button type="button" class="seg-btn ${mode === 'inherit' ? 'active' : ''}" data-action="drawer-tpl-mode" data-mode="inherit">跟随全局模板</button>
          <button type="button" class="seg-btn ${mode === 'custom' ? 'active' : ''}" data-action="drawer-tpl-mode" data-mode="custom">自定义专属模板</button>
          <button type="button" class="seg-btn ${mode === 'none' ? 'active' : ''}" data-action="drawer-tpl-mode" data-mode="none">仅发媒体（无文本）</button>
        </div>
      </div>

      ${mode === 'custom' ? `
        <div class="stack-sm">
          ${renderVarToolbar('drawer-template-textarea')}
          <textarea id="drawer-template-textarea" rows="5" data-drawer-field="message_template" placeholder="例如只填 {title} 即可仅输出标题；支持任意穿插自定义文字与换行">${esc(pol.message_template || '')}</textarea>
          ${!(pol.message_template || '').trim() ? '<span class="text-xs" style="color:var(--danger);">提示：自定义模式下模板内容不能为空，点击上方任意变量胶囊即可快速插入。</span>' : ''}
        </div>
      ` : ''}

      <div class="chat-preview-box">
        <div class="chat-preview-header">
          <span>${esc(meta.name)} 投递效果实时预览</span>
          <span>${mode === 'inherit' ? '继承全局' : (mode === 'custom' ? '专属定制' : '无文本模式')}</span>
        </div>
        <div id="drawer-live-preview" class="chat-bubble">${state.draft.policy.group_reply ? '<div class="chat-reply-quote">回复 群成员的消息</div>' : ''}${effectiveShowCover ? '<div class="chat-cover-mock">🖼️ [封面预览]</div>\n' : ''}${previewText ? esc(previewText) : '<span class="muted">（仅发送图片/视频媒体，不附带文本消息块）</span>'}</div>
      </div>

      <label class="field">
        <span class="field-label">自定义拒绝提示词（可选）</span>
        <input type="text" data-drawer-field="reject_text" value="${esc(pol.reject_text || '')}" placeholder="留空使用全局拒绝文案，支持 {platform} 与 {reason}">
      </label>

      <div class="field">
        <div class="row-between">
          <span class="field-label">平台登录 Cookie（可选，AES-GCM 加密存储）</span>
          ${hasSavedCookie ? '<span class="badge badge-success">已加密保存 Cookie</span>' : '<span class="badge badge-neutral">未配置</span>'}
        </div>
        <textarea rows="3" class="mono text-xs" data-drawer-field="cookie" placeholder="如需高画质或登录态解析，请粘贴 Netscape 或 Header 格式 Cookie；留空则使用匿名解析">${esc(pol.cookie || '')}</textarea>
        <div class="row-between">
          <span class="field-hint">保持 <code>********</code> 表示沿用已存 Cookie；清空后保存即移除 Cookie（修改 Cookie 属于敏感操作需验证密码）。</span>
          ${pol.cookie ? '<button type="button" class="btn btn-ghost btn-xs" data-action="clear-drawer-cookie">清空 Cookie</button>' : ''}
        </div>
      </div>
    `;
  }

  function buildOneBotHTML() {
    const s = state.status || {};
    const bots = Array.isArray(s.bots) ? s.bots : [];
    const pol = state.draft.policy;
    const ob = state.draft.onebot;

    const botRows = bots.length === 0
      ? '<tr><td colspan="3" class="muted" style="text-align:center;padding:18px;">暂无已连接的 OneBot 客户端</td></tr>'
      : bots.map((b) => `
          <tr>
            <td class="mono"><strong>${esc(b.self_id)}</strong></td>
            <td class="text-xs muted">${esc(formatTime(b.connected_at))}</td>
            <td class="text-xs muted">${esc(formatTime(b.last_seen))}</td>
          </tr>
        `).join('');

    return `
      <div class="grid-2">
        <section class="card stack-md">
          <div class="card-header" style="margin-bottom:0;">
            <div>
              <h3>OneBot v11 反向 WebSocket 鉴权</h3>
              <p>仅接受携带有效 Bearer Token 与 <code>X-Client-Role: Universal</code> 的机器连接</p>
            </div>
          </div>

          <label class="field">
            <span class="field-label">反向 WebSocket 连接地址</span>
            <div class="input-group">
              <input type="text" class="mono" readonly value="${esc(s.onebot_ws_url || '')}">
              <button type="button" class="btn btn-secondary btn-sm" data-action="copy-text" data-copy="${esc(s.onebot_ws_url || '')}">复制</button>
            </div>
          </label>

          <label class="field">
            <span class="field-label">Bearer Access Token（至少 24 字符）</span>
            <div class="input-group">
              <input type="password" class="mono" data-bind="onebot.token" value="${esc(ob.token || '')}" placeholder="至少 24 位高强度密钥">
              <button type="button" class="btn btn-secondary btn-sm" data-action="rotate-onebot-token">随机轮换密钥</button>
            </div>
            <span class="field-hint">轮换后新 Token 立即生效并断开旧连接，只展示一次明文以便复制到 NapCat。</span>
          </label>

          ${state.rotatedToken ? `
            <div class="alert alert-info stack-sm">
              <strong>新生成的 OneBot Token（请立即复制保存）：</strong>
              <div class="input-group">
                <input type="text" class="mono" readonly value="${esc(state.rotatedToken)}">
                <button type="button" class="btn btn-primary btn-sm" data-action="copy-text" data-copy="${esc(state.rotatedToken)}">复制明文</button>
              </div>
            </div>
          ` : ''}

          <label class="field">
            <span class="field-label">允许接入的机器人 QQ 号白名单（每行或逗号分隔，留空允许全部）</span>
            <textarea rows="3" class="mono" data-bind-list="onebot.self_ids" placeholder="例如：\n10001\n10002">${esc((ob.self_ids || []).join('\n'))}</textarea>
          </label>

          <div>
            <h4 class="text-sm" style="margin-bottom:8px;">当前在线机器人 (${bots.length})</h4>
            <div class="table-wrap">
              <table>
                <thead><tr><th>机器人账号 (Self ID)</th><th>接入时间</th><th>最后心跳</th></tr></thead>
                <tbody>${botRows}</tbody>
              </table>
            </div>
          </div>
        </section>

        <section class="card stack-md">
          <div class="card-header" style="margin-bottom:0;">
            <div>
              <h3>自动解析与群聊投递策略</h3>
              <p>控制群聊引用回复、私聊开关、防刷屏冷却以及群黑白名单规则</p>
            </div>
          </div>

          <div class="switch-row">
            <div class="switch-row-text">
              <strong>启用消息自动解析总开关</strong>
              <span>关闭后所有群聊与私聊消息均不再触发自动解析</span>
            </div>
            <label class="switch">
              <input type="checkbox" data-bind="policy.enabled" ${pol.enabled ? 'checked' : ''}>
              <span class="switch-slider"></span>
            </label>
          </div>

          <div class="switch-row">
            <div class="switch-row-text">
              <strong>允许私聊消息解析</strong>
              <span>开启后机器人好友或私聊发送链接也可触发解析</span>
            </div>
            <label class="switch">
              <input type="checkbox" data-bind="policy.private_enabled" ${pol.private_enabled ? 'checked' : ''}>
              <span class="switch-slider"></span>
            </label>
          </div>

          <div class="switch-row">
            <div class="switch-row-text">
              <strong>群聊消息引用回复原发送者</strong>
              <span>发送首条图文结果时自动挂载 OneBot <code>reply</code> 段引用原链接消息</span>
            </div>
            <label class="switch">
              <input type="checkbox" data-bind="policy.group_reply" ${pol.group_reply ? 'checked' : ''}>
              <span class="switch-slider"></span>
            </label>
          </div>

          <div class="grid-2">
            <label class="field">
              <span class="field-label">同一链接防刷屏冷却（秒）</span>
              <input type="number" min="0" max="86400" data-bind-num="policy.cooldown_seconds" value="${Number(pol.cooldown_seconds || 0)}">
            </label>
            <label class="field">
              <span class="field-label">默认策略拒绝文案</span>
              <input type="text" data-bind="policy.reject_text" value="${esc(pol.reject_text || '')}">
            </label>
          </div>

          <label class="field">
            <span class="field-label">群聊白名单（填写后<strong>仅白名单内群号</strong>生效，每行一个群号）</span>
            <textarea rows="3" class="mono" data-bind-intlist="policy.group_whitelist" placeholder="留空表示不启用白名单限制">${esc((pol.group_whitelist || []).join('\n'))}</textarea>
          </label>

          <label class="field">
            <span class="field-label">群聊黑名单（白名单为空时生效，忽略这些群号，每行一个群号）</span>
            <textarea rows="3" class="mono" data-bind-intlist="policy.group_blacklist" placeholder="例如：\n123456789">${esc((pol.group_blacklist || []).join('\n'))}</textarea>
          </label>
        </section>
      </div>
    `;
  }

  function buildMediaHTML() {
    const m = state.draft.media;
    const p = state.draft.proxy;
    const maxMiB = Math.max(1, Math.round((Number(m.max_bytes || 0) / (1024 * 1024)) * 100) / 100);
    const diskGiB = Math.max(0.1, Math.round((Number(m.max_disk_bytes || 0) / (1024 * 1024 * 1024)) * 100) / 100);

    const platformOptions = state.platforms.map((item) =>
      `<option value="${esc(item.id)}" ${state.proxyTestPlatform === item.id ? 'selected' : ''}>${esc(item.name)} (${esc(item.id)})</option>`
    ).join('');

    return `
      <div class="grid-2">
        <section class="card stack-md">
          <div class="card-header" style="margin-bottom:0;">
            <div>
              <h3>媒体下载、画质与转码配额</h3>
              <p>使用直观的 MiB / GiB 单位管理下载大小与缓存容量，自动执行 H.264 兼容性重编码</p>
            </div>
          </div>

          <div class="switch-row">
            <div class="switch-row-text">
              <strong>自动下载与处理媒体文件</strong>
              <span>关闭后仅解析元数据与链接，不下载图片或视频到本地</span>
            </div>
            <label class="switch">
              <input type="checkbox" data-bind="media.auto_download" ${m.auto_download ? 'checked' : ''}>
              <span class="switch-slider"></span>
            </label>
          </div>

          <div class="grid-2">
            <label class="field">
              <span class="field-label">单文件最大体积 (MiB)</span>
              <input type="number" min="1" max="4096" step="1" data-bind-mib="media.max_bytes" value="${maxMiB}">
              <span class="field-hint">当前精确值：${esc(formatBytes(m.max_bytes))}</span>
            </label>
            <label class="field">
              <span class="field-label">本地缓存磁盘上限 (GiB)</span>
              <input type="number" min="0.1" max="1024" step="0.5" data-bind-gib="media.max_disk_bytes" value="${diskGiB}">
              <span class="field-hint">当前精确值：${esc(formatBytes(m.max_disk_bytes))}</span>
            </label>
            <label class="field">
              <span class="field-label">目标视频短边画质</span>
              <select data-bind-num="media.quality">
                ${[360, 480, 720, 1080, 1440, 2160].map((q) => `<option value="${q}" ${Number(m.quality) === q ? 'selected' : ''}>${q}p</option>`).join('')}
              </select>
            </label>
            <label class="field">
              <span class="field-label">允许最大视频时长（秒）</span>
              <input type="number" min="1" max="86400" data-bind-num="media.max_duration_seconds" value="${Number(m.max_duration_seconds || 1500)}">
            </label>
            <label class="field">
              <span class="field-label">单帖最多图片张数</span>
              <input type="number" min="1" max="200" data-bind-num="media.max_images" value="${Number(m.max_images || 30)}">
            </label>
            <label class="field">
              <span class="field-label">缓存保留时长（小时）</span>
              <input type="number" min="1" max="8760" data-bind-num="cache.ttl_hours" value="${Number(state.draft.cache.ttl_hours || 24)}">
            </label>
            <label class="field">
              <span class="field-label">解析并发任务数 (1–16)</span>
              <input type="number" min="1" max="16" data-bind-num="media.concurrency" value="${Number(m.concurrency || 2)}">
            </label>
            <label class="field">
              <span class="field-label">FFmpeg 并发转码数 (1–4)</span>
              <input type="number" min="1" max="4" data-bind-num="media.transcode_concurrency" value="${Number(m.transcode_concurrency || 1)}">
            </label>
          </div>

          <div class="grid-2">
            <label class="field">
              <span class="field-label">超大媒体处理方式</span>
              <select data-bind="media.oversize_mode">
                <option value="link" ${m.oversize_mode === 'link' ? 'selected' : ''}>仅降级发送原链接 (link)</option>
                <option value="file" ${m.oversize_mode === 'file' ? 'selected' : ''}>转为群文件/私聊文件上传 (file)</option>
              </select>
            </label>
            <label class="field">
              <span class="field-label">OneBot 媒体传输方式</span>
              <select data-bind="media.transport">
                <option value="file" ${m.transport === 'file' ? 'selected' : ''}>本地共享路径 file:// (需共享挂载目录)</option>
                <option value="http" ${m.transport === 'http' ? 'selected' : ''}>HMAC 签名 HTTP 地址 (适合跨机隔离部署)</option>
              </select>
            </label>
          </div>

          ${m.transport === 'http' ? `
            <label class="field">
              <span class="field-label">对外访问根地址 External URL（HTTP 传输必填，供 NapCat 拉取签名媒体）</span>
              <input type="url" data-bind="server.external_url" value="${esc(state.draft.server.external_url || '')}" placeholder="例如 http://multiparse:10492 或 https://parse.example.com">
            </label>
          ` : ''}
        </section>

        <section class="card stack-md">
          <div class="card-header" style="margin-bottom:0;">
            <div>
              <h3>出站网络代理与平台连通性测试</h3>
              <p>支持按平台「海外分流」标记智能路由代理，内置 SSRF 私网地址拦截保护</p>
            </div>
          </div>

          <label class="field">
            <span class="field-label">代理分流模式</span>
            <select data-bind="proxy.mode">
              <option value="direct" ${p.mode === 'direct' ? 'selected' : ''}>全部直连 (direct)</option>
              <option value="overseas" ${p.mode === 'overseas' ? 'selected' : ''}>仅标记为「海外」的平台走代理 (overseas)</option>
              <option value="all" ${p.mode === 'all' ? 'selected' : ''}>所有平台统一走代理 (all)</option>
            </select>
          </label>

          <label class="field">
            <span class="field-label">代理服务器地址（支持 HTTP / HTTPS / SOCKS5 / SOCKS5H）</span>
            <input type="text" class="mono" data-bind="proxy.url" value="${esc(p.url || '')}" placeholder="例如 http://127.0.0.1:7890 或 socks5h://127.0.0.1:1080">
            <span class="field-hint">含账号密码的代理地址会自动脱敏为 <code>********</code>；如修改代理配置，请先保存后再执行下方连通性测试。</span>
          </label>

          <div class="card" style="background:var(--surface-alt);padding:15px;">
            <strong class="text-sm">在线连通性诊断（使用当前已保存生效的配置发起探测）</strong>
            <div class="input-group" style="margin-top:10px;">
              <select id="proxy-test-platform">${platformOptions}</select>
              <button type="button" class="btn btn-secondary" data-action="test-proxy">发起连通性测试</button>
            </div>
          </div>
        </section>
      </div>
    `;
  }

  function buildSystemHTML() {
    const tab = state.systemSubTab;
    const srv = state.draft.server;
    const sec = state.draft.security;

    let subHTML = '';
    if (tab === 'server') {
      subHTML = `
        <div class="stack-md">
          <div class="grid-3">
            <label class="field">
              <span class="field-label">监听地址 (IP 或 localhost)</span>
              <input type="text" class="mono" data-bind="server.listen" value="${esc(srv.listen || '::')}">
            </label>
            <label class="field">
              <span class="field-label">监听端口 (1–65535)</span>
              <input type="number" min="1" max="65535" data-bind-num="server.port" value="${Number(srv.port || 10492)}">
            </label>
            <label class="field">
              <span class="field-label">对外访问地址 External URL（可选）</span>
              <input type="text" class="mono" data-bind="server.external_url" value="${esc(srv.external_url || '')}" placeholder="https://parse.example.com">
            </label>
          </div>

          <div class="switch-row">
            <div class="switch-row-text">
              <strong>启用原生 HTTPS / WSS (TLS)</strong>
              <span>开启后需成对提供容器内可读的证书与私钥路径；若前置 Nginx 反代请保持关闭并配置受信反代 IP</span>
            </div>
            <label class="switch">
              <input type="checkbox" data-bind="server.tls" ${srv.tls ? 'checked' : ''}>
              <span class="switch-slider"></span>
            </label>
          </div>

          <div class="grid-2">
            <label class="field">
              <span class="field-label">TLS 证书文件路径 (Cert File)</span>
              <input type="text" class="mono" data-bind="server.cert_file" value="${esc(srv.cert_file || '')}" placeholder="/certs/fullchain.pem">
            </label>
            <label class="field">
              <span class="field-label">TLS 私钥文件路径 (Key File)</span>
              <input type="text" class="mono" data-bind="server.key_file" value="${esc(srv.key_file || '')}" placeholder="/certs/privkey.pem">
            </label>
          </div>

          <div class="grid-2">
            <label class="field">
              <span class="field-label">受信任反向代理 IP / CIDR（每行一个，仅受信代理才解析 X-Forwarded-For）</span>
              <textarea rows="4" class="mono" data-bind-list="server.trusted_proxies" placeholder="例如：\n127.0.0.1\n172.16.0.0/12">${esc((srv.trusted_proxies || []).join('\n'))}</textarea>
            </label>
            <label class="field">
              <span class="field-label">允许访问管理后台的客户端网段（留空不限制，建议配置内网或管理 IP）</span>
              <textarea rows="4" class="mono" data-bind-list="server.admin_networks" placeholder="留空允许任意来源访问登录页">${esc((srv.admin_networks || []).join('\n'))}</textarea>
            </label>
          </div>
        </div>
      `;
    } else if (tab === 'security') {
      const banRows = state.bans.length === 0
        ? '<tr><td colspan="4" class="muted" style="text-align:center;padding:20px;">当前没有处于封禁状态的客户端 IP</td></tr>'
        : state.bans.map((b) => `
            <tr>
              <td class="mono"><strong>${esc(b.ip)}</strong></td>
              <td>${Number(b.failures || 0)} 次</td>
              <td class="text-xs muted">${esc(formatTime(b.banned_until))}</td>
              <td><button type="button" class="btn btn-secondary btn-xs" data-action="unban-ip" data-ip="${esc(b.ip)}">解除封禁</button></td>
            </tr>
          `).join('');

      subHTML = `
        <div class="stack-md">
          <div class="grid-2">
            <label class="field">
              <span class="field-label">允许连续密码错误次数 (1–100)</span>
              <input type="number" min="1" max="100" data-bind-num="security.failures" value="${Number(sec.failures || 5)}">
            </label>
            <label class="field">
              <span class="field-label">失败计数统计窗口（分钟，1–1440）</span>
              <input type="number" min="1" max="1440" data-bind-num="security.window_minutes" value="${Number(sec.window_minutes || 15)}">
            </label>
            <label class="field">
              <span class="field-label">触发阈值后自动封禁时长（分钟，1–43200）</span>
              <input type="number" min="1" max="43200" data-bind-num="security.ban_minutes" value="${Number(sec.ban_minutes || 60)}">
            </label>
            <label class="field">
              <span class="field-label">登录会话有效时长（小时，1–168）</span>
              <input type="number" min="1" max="168" data-bind-num="security.session_hours" value="${Number(sec.session_hours || 12)}">
            </label>
          </div>

          <div>
            <h4 class="text-sm" style="margin-bottom:8px;">当前封禁 IP 列表 (${state.bans.length})</h4>
            <div class="table-wrap">
              <table>
                <thead><tr><th>被封禁 IP</th><th>累计失败</th><th>解封时间</th><th>操作</th></tr></thead>
                <tbody>${banRows}</tbody>
              </table>
            </div>
          </div>
        </div>
      `;
    } else {
      subHTML = `
        <div class="grid-2">
          <div class="card" style="background:var(--surface-alt);">
            <h4 style="margin-bottom:12px;">修改管理员账号与密码</h4>
            <form id="change-account-form" class="stack-md">
              <label class="field">
                <span class="field-label">新管理员用户名</span>
                <input id="acc-username" type="text" required maxlength="64" value="${esc((state.me && state.me.username) || 'admin')}">
              </label>
              <label class="field">
                <span class="field-label">当前管理员密码</span>
                <input id="acc-current-pwd" type="password" required maxlength="1024" autocomplete="current-password">
              </label>
              <label class="field">
                <span class="field-label">新管理员密码（至少 12 位）</span>
                <input id="acc-new-pwd" type="password" required minlength="12" maxlength="1024" autocomplete="new-password">
              </label>
              <button type="submit" class="btn btn-primary">保存新凭据并重新登录</button>
            </form>
          </div>

          <div class="card stack-md" style="background:var(--surface-alt);">
            <div>
              <h4 style="margin-bottom:6px;">配置备份导出与导入恢复</h4>
              <p class="text-xs muted">支持一键导出当前脱敏 JSON 配置备份，或从 v1.x / v2.x 备份 JSON 文件导入恢复（自动过滤旧版废弃字段并保留未改动的加密密钥与 Cookie）</p>
            </div>
            <div class="row-gap-sm">
              <button type="button" class="btn btn-secondary" data-action="export-config">导出配置 JSON</button>
              <button type="button" class="btn btn-primary" data-action="trigger-import-config">导入配置 JSON…</button>
            </div>
            <div class="alert alert-info text-xs">
              导入配置后将载入到当前待保存草稿，您可以先检查各项参数与平台消息模板，确认无误后点击底部「保存配置」正式生效。
            </div>
          </div>
        </div>
      `;
    }

    return `
      <section class="card">
        <div class="card-header">
          <div class="segmented">
            <button type="button" class="seg-btn ${tab === 'server' ? 'active' : ''}" data-action="system-subtab" data-tab="server">网络监听与 TLS</button>
            <button type="button" class="seg-btn ${tab === 'security' ? 'active' : ''}" data-action="system-subtab" data-tab="security">登录安全与封禁 (${state.bans.length})</button>
            <button type="button" class="seg-btn ${tab === 'account' ? 'active' : ''}" data-action="system-subtab" data-tab="account">账户与配置备份</button>
          </div>
        </div>
        ${subHTML}
      </section>
    `;
  }

  function setNestedValue(root, path, value) {
    const keys = path.split('.');
    let cur = root;
    for (let i = 0; i < keys.length - 1; i++) {
      cur = cur[keys[i]];
    }
    cur[keys[keys.length - 1]] = value;
  }

  function parseStringList(raw) {
    return String(raw || '')
      .split(/[\n,]+/)
      .map((s) => s.trim())
      .filter(Boolean);
  }

  function parseIntList(raw) {
    const out = [];
    for (const part of parseStringList(raw)) {
      if (/^[1-9]\d{0,18}$/.test(part)) {
        out.push(Number(part));
      }
    }
    return out;
  }

  function insertAtCursor(el, token) {
    if (!el) return;
    const start = el.selectionStart ?? el.value.length;
    const end = el.selectionEnd ?? el.value.length;
    const before = el.value.slice(0, start);
    const after = el.value.slice(end);
    el.value = before + token + after;
    const nextPos = start + token.length;
    el.focus();
    el.setSelectionRange(nextPos, nextPos);
    el.dispatchEvent(new Event('input', { bubbles: true }));
  }

  // 安全合并导入的 v1.x / v2.x 配置 JSON 到待保存草稿
  function mergeImportedConfig(rawObj) {
    if (!rawObj || typeof rawObj !== 'object' || !state.config) {
      throw new Error('配置文件格式无效');
    }
    const next = deepClone(state.config);

    if (rawObj.server && typeof rawObj.server === 'object') {
      for (const k of ['listen', 'external_url', 'cert_file', 'key_file']) {
        if (typeof rawObj.server[k] === 'string') next.server[k] = rawObj.server[k];
      }
      if (typeof rawObj.server.port === 'number') next.server.port = rawObj.server.port;
      if (typeof rawObj.server.tls === 'boolean') next.server.tls = rawObj.server.tls;
      if (Array.isArray(rawObj.server.trusted_proxies)) next.server.trusted_proxies = rawObj.server.trusted_proxies.map(String);
      if (Array.isArray(rawObj.server.admin_networks)) next.server.admin_networks = rawObj.server.admin_networks.map(String);
    }

    if (rawObj.onebot && typeof rawObj.onebot === 'object') {
      if (typeof rawObj.onebot.token === 'string' && rawObj.onebot.token.trim() !== '') {
        next.onebot.token = rawObj.onebot.token;
      }
      if (Array.isArray(rawObj.onebot.self_ids)) {
        next.onebot.self_ids = rawObj.onebot.self_ids.map(String);
      }
    }

    if (rawObj.security && typeof rawObj.security === 'object') {
      for (const k of ['failures', 'window_minutes', 'ban_minutes', 'session_hours']) {
        if (typeof rawObj.security[k] === 'number') next.security[k] = rawObj.security[k];
      }
    }

    if (rawObj.proxy && typeof rawObj.proxy === 'object') {
      if (typeof rawObj.proxy.mode === 'string') next.proxy.mode = rawObj.proxy.mode;
      if (typeof rawObj.proxy.url === 'string') next.proxy.url = rawObj.proxy.url;
    }

    if (rawObj.media && typeof rawObj.media === 'object') {
      for (const k of ['max_bytes', 'max_duration_seconds', 'max_images', 'quality', 'concurrency', 'transcode_concurrency', 'max_disk_bytes']) {
        if (typeof rawObj.media[k] === 'number') next.media[k] = rawObj.media[k];
      }
      for (const k of ['auto_download', 'show_cover', 'show_info', 'clean_url']) {
        if (typeof rawObj.media[k] === 'boolean') next.media[k] = rawObj.media[k];
      }
      for (const k of ['message_template', 'oversize_mode', 'transport']) {
        if (typeof rawObj.media[k] === 'string') next.media[k] = rawObj.media[k];
      }
    }

    if (rawObj.cache && typeof rawObj.cache === 'object' && typeof rawObj.cache.ttl_hours === 'number') {
      next.cache.ttl_hours = rawObj.cache.ttl_hours;
    }

    if (rawObj.policy && typeof rawObj.policy === 'object') {
      for (const k of ['enabled', 'private_enabled', 'group_reply']) {
        if (typeof rawObj.policy[k] === 'boolean') next.policy[k] = rawObj.policy[k];
      }
      if (typeof rawObj.policy.cooldown_seconds === 'number') next.policy.cooldown_seconds = rawObj.policy.cooldown_seconds;
      if (typeof rawObj.policy.reject_text === 'string') next.policy.reject_text = rawObj.policy.reject_text;
      if (Array.isArray(rawObj.policy.group_whitelist)) next.policy.group_whitelist = rawObj.policy.group_whitelist.map(Number).filter((n) => n > 0);
      if (Array.isArray(rawObj.policy.group_blacklist)) next.policy.group_blacklist = rawObj.policy.group_blacklist.map(Number).filter((n) => n > 0);
    }

    if (rawObj.platforms && typeof rawObj.platforms === 'object') {
      for (const [id, src] of Object.entries(rawObj.platforms)) {
        if (!next.platforms[id] || !src || typeof src !== 'object') continue;
        const dst = next.platforms[id];
        for (const k of ['enabled', 'nsfw_blocked', 'overseas', 'silent']) {
          if (typeof src[k] === 'boolean') dst[k] = src[k];
        }
        if (typeof src.reject_text === 'string') dst.reject_text = src.reject_text;
        if (typeof src.template_mode === 'string' && ['inherit', 'custom', 'none'].includes(src.template_mode)) {
          dst.template_mode = src.template_mode;
        }
        if (typeof src.message_template === 'string') dst.message_template = src.message_template;
        if (typeof src.cover_mode === 'string' && ['inherit', 'on', 'off'].includes(src.cover_mode)) {
          dst.cover_mode = src.cover_mode;
        }
        if (typeof src.cookie === 'string' && src.cookie !== SECRET_SENTINEL) {
          dst.cookie = src.cookie;
        }
      }
    }

    state.draft = next;
    updateDirtyBar();
    renderRoute();
  }

  function bindGlobalEvents() {
    // 主题切换
    for (const id of ['theme-toggle-btn', 'auth-theme-btn']) {
      $(id).addEventListener('click', () => {
        applyTheme(state.theme === 'dark' ? 'light' : 'dark');
      });
    }

    // 登录密码显隐
    $('toggle-login-pwd').addEventListener('click', () => {
      const input = $('login-password');
      const showing = input.type === 'text';
      input.type = showing ? 'password' : 'text';
      $('toggle-login-pwd').textContent = showing ? '显示' : '隐藏';
    });

    // 登录提交
    $('login-form').addEventListener('submit', async (e) => {
      e.preventDefault();
      const errBox = $('login-error');
      errBox.classList.add('hidden');
      const btn = $('login-submit');
      btn.disabled = true;
      try {
        const me = await api('/api/login', {
          method: 'POST',
          body: {
            username: $('login-username').value.trim(),
            password: $('login-password').value,
          },
        });
        $('login-password').value = '';
        await enterConsole(me);
      } catch (err) {
        errBox.textContent = err.message;
        errBox.classList.remove('hidden');
      } finally {
        btn.disabled = false;
      }
    });

    // 首次登录强制改密
    $('force-pwd-form').addEventListener('submit', async (e) => {
      e.preventDefault();
      const errBox = $('force-pwd-error');
      errBox.classList.add('hidden');
      const btn = $('force-pwd-submit');
      btn.disabled = true;
      try {
        await api('/api/account', {
          method: 'POST',
          body: {
            username: $('force-username').value.trim(),
            current_password: $('force-current-pwd').value,
            new_password: $('force-new-pwd').value,
          },
        });
        toast('管理员凭据已更新，请使用新密码登录', 'success');
        showAuthView();
      } catch (err) {
        errBox.textContent = err.message;
        errBox.classList.remove('hidden');
      } finally {
        btn.disabled = false;
      }
    });

    // 侧边栏导航
    $('sidebar-nav').addEventListener('click', (e) => {
      const btn = e.target.closest('.nav-item[data-route]');
      if (btn) setRoute(btn.dataset.route);
    });

    // 移动端抽屉菜单
    $('mobile-menu-btn').addEventListener('click', () => {
      $('sidebar').classList.toggle('open');
      $('sidebar-backdrop').classList.toggle('hidden', !$('sidebar').classList.contains('open'));
    });
    $('sidebar-backdrop').addEventListener('click', () => {
      $('sidebar').classList.remove('open');
      $('sidebar-backdrop').classList.add('hidden');
    });

    // 顶部刷新与登出
    $('refresh-btn').addEventListener('click', async () => {
      try {
        await loadAllData(false);
        renderRoute();
        toast('数据已刷新');
      } catch (err) {
        toast(err.message, 'error');
      }
    });

    $('logout-btn').addEventListener('click', async () => {
      try {
        await api('/api/logout', { method: 'POST' });
      } catch {
        // ignore
      }
      showAuthView();
    });

    // 底部未保存浮动栏
    $('discard-config-btn').addEventListener('click', () => {
      if (!state.config) return;
      state.draft = deepClone(state.config);
      updateDirtyBar();
      renderRoute();
      if (state.drawerPlatformId) renderDrawerBody(state.drawerPlatformId);
      toast('已放弃未保存更改');
    });

    $('save-config-btn').addEventListener('click', saveDraftConfig);

    // 平台抽屉按钮
    $('close-drawer-btn').addEventListener('click', () => {
      closeDrawer();
      if (state.route === 'platforms') renderRoute();
    });
    $('drawer-done-btn').addEventListener('click', () => {
      closeDrawer();
      if (state.route === 'platforms') renderRoute();
    });
    $('platform-drawer-backdrop').addEventListener('click', () => {
      closeDrawer();
      if (state.route === 'platforms') renderRoute();
    });
    $('drawer-save-now-btn').addEventListener('click', saveDraftConfig);

    // 密码重验弹窗
    $('reauth-cancel').addEventListener('click', closeReauthModal);
    $('reauth-form').addEventListener('submit', async (e) => {
      e.preventDefault();
      const pwd = $('reauth-password').value;
      const fn = state.reauthCallback;
      closeReauthModal();
      if (fn) {
        try {
          await fn(pwd);
        } catch (err) {
          toast(err.message, 'error');
        }
      }
    });

    // 快捷键 Ctrl/Cmd + S 保存
    document.addEventListener('keydown', (e) => {
      if ((e.ctrlKey || e.metaKey) && e.key.toLowerCase() === 's') {
        if (!$('app-shell').classList.contains('hidden') && isDirty()) {
          e.preventDefault();
          saveDraftConfig();
        }
      } else if (e.key === 'Escape') {
        if (!$('reauth-modal').classList.contains('hidden')) {
          closeReauthModal();
        } else if (state.drawerPlatformId) {
          closeDrawer();
          if (state.route === 'platforms') renderRoute();
        }
      }
    });

    // 配置导入文件读取
    $('import-config-input').addEventListener('change', async (e) => {
      const file = e.target.files && e.target.files[0];
      e.target.value = '';
      if (!file) return;
      try {
        const text = await file.text();
        const parsed = JSON.parse(text);
        mergeImportedConfig(parsed);
        toast('配置文件已导入草稿，请检查后点击「保存配置」生效', 'success');
      } catch (err) {
        toast(`导入失败：${err.message}`, 'error');
      }
    });

    // 全局点击事件委托（主工作区 + 平台抽屉）
    document.addEventListener('click', async (e) => {
      const actionEl = e.target.closest('[data-action]');
      if (!actionEl) return;
      const action = actionEl.dataset.action;

      if (action === 'goto-route') {
        setRoute(actionEl.dataset.route);
        return;
      }

      if (action === 'copy-text') {
        const text = actionEl.dataset.copy || '';
        if (!text) return;
        try {
          await navigator.clipboard.writeText(text);
          toast('已复制到剪贴板', 'success');
        } catch {
          toast('复制失败，请手动选中复制', 'error');
        }
        return;
      }

      if (action === 'insert-var') {
        const target = $(actionEl.dataset.target);
        insertAtCursor(target, actionEl.dataset.token || '');
        return;
      }

      if (action === 'apply-global-preset') {
        state.draft.media.message_template = actionEl.dataset.preset || DEFAULT_TEMPLATE;
        state.draft.media.show_info = true;
        updateDirtyBar();
        renderRoute();
        return;
      }

      if (action === 'apply-drawer-preset') {
        if (!state.drawerPlatformId) return;
        const pol = state.draft.platforms[state.drawerPlatformId];
        pol.template_mode = 'custom';
        pol.message_template = actionEl.dataset.preset || DEFAULT_TEMPLATE;
        updateDirtyBar();
        renderDrawerBody(state.drawerPlatformId);
        return;
      }

      if (action === 'open-platform-drawer') {
        openDrawer(actionEl.dataset.platform);
        return;
      }

      if (action === 'drawer-tpl-mode') {
        if (!state.drawerPlatformId) return;
        const pol = state.draft.platforms[state.drawerPlatformId];
        const nextMode = actionEl.dataset.mode;
        pol.template_mode = nextMode;
        if (nextMode === 'custom' && !(pol.message_template || '').trim()) {
          pol.message_template = state.draft.media.message_template || DEFAULT_TEMPLATE;
        }
        updateDirtyBar();
        renderDrawerBody(state.drawerPlatformId);
        return;
      }

      if (action === 'drawer-cover-mode') {
        if (!state.drawerPlatformId) return;
        state.draft.platforms[state.drawerPlatformId].cover_mode = actionEl.dataset.mode;
        updateDirtyBar();
        renderDrawerBody(state.drawerPlatformId);
        return;
      }

      if (action === 'clear-drawer-cookie') {
        if (!state.drawerPlatformId) return;
        state.draft.platforms[state.drawerPlatformId].cookie = '';
        updateDirtyBar();
        renderDrawerBody(state.drawerPlatformId);
        return;
      }

      if (action === 'platform-filter') {
        state.platformFilter = actionEl.dataset.filter;
        renderRoute();
        return;
      }

      if (action === 'bulk-platforms') {
        const op = actionEl.dataset.op;
        for (const p of state.platforms) {
          const pol = state.draft.platforms[p.id];
          if (!pol) continue;
          if (op === 'enable-all') pol.enabled = true;
          if (op === 'reset-templates') pol.template_mode = 'inherit';
        }
        updateDirtyBar();
        renderRoute();
        toast(op === 'enable-all' ? '已将全部平台设为启用（保存后生效）' : '已将全部平台恢复为继承全局模板（保存后生效）');
        return;
      }

      if (action === 'tasks-subtab') {
        state.tasksSubTab = actionEl.dataset.tab;
        renderRoute();
        return;
      }

      if (action === 'task-filter') {
        state.tasksFilter = actionEl.dataset.filter;
        renderRoute();
        return;
      }

      if (action === 'system-subtab') {
        state.systemSubTab = actionEl.dataset.tab;
        renderRoute();
        return;
      }

      if (action === 'delete-cache-item') {
        const key = actionEl.dataset.key;
        if (!key) return;
        try {
          await api(`/api/cache?key=${encodeURIComponent(key)}`, { method: 'DELETE' });
          await refreshRuntimeOnly();
          renderRoute();
          toast('该条缓存记录已删除', 'success');
        } catch (err) {
          toast(err.message, 'error');
        }
        return;
      }

      if (action === 'clear-cache') {
        const clearAll = actionEl.dataset.all === 'true';
        if (clearAll && !window.confirm('确定要清空所有本地媒体缓存与关联任务记录吗？')) return;
        try {
          await api('/api/cache/clear', { method: 'POST', body: { all: clearAll } });
          await loadAllData(false);
          renderRoute();
          toast(clearAll ? '已清空全部缓存' : '已清理过期缓存', 'success');
        } catch (err) {
          toast(err.message, 'error');
        }
        return;
      }

      if (action === 'unban-ip') {
        const ip = actionEl.dataset.ip;
        if (!ip) return;
        try {
          await api(`/api/bans?ip=${encodeURIComponent(ip)}`, { method: 'DELETE' });
          state.bans = await api('/api/bans');
          syncNavBadges();
          renderRoute();
          toast(`已解除对 ${ip} 的封禁`, 'success');
        } catch (err) {
          toast(err.message, 'error');
        }
        return;
      }

      if (action === 'rotate-onebot-token') {
        openReauthModal(
          '轮换 OneBot Access Token',
          '轮换密钥会立即断开当前旧 Token 连接，请输入当前管理员密码确认。',
          async (pwd) => {
            const res = await api('/api/onebot/token', {
              method: 'POST',
              body: { current_password: pwd },
            });
            state.rotatedToken = res.token || '';
            const fresh = await api('/api/config');
            state.config = fresh;
            state.draft = deepClone(fresh);
            updateDirtyBar();
            renderRoute();
            toast('OneBot Token 已轮换，请复制新密钥', 'success');
          }
        );
        return;
      }

      if (action === 'test-proxy') {
        const sel = $('proxy-test-platform');
        const platform = sel ? sel.value : state.proxyTestPlatform;
        state.proxyTestPlatform = platform;
        actionEl.disabled = true;
        const prevText = actionEl.textContent;
        actionEl.textContent = '测试中…';
        try {
          await api('/api/proxy/test', { method: 'POST', body: { platform } });
          toast(`${platformName(platform)} 连通性测试通过`, 'success');
        } catch (err) {
          toast(`连通性测试失败：${err.message}`, 'error');
        } finally {
          actionEl.disabled = false;
          actionEl.textContent = prevText;
        }
        return;
      }

      if (action === 'sandbox-preview' || action === 'sandbox-enqueue') {
        const urlInput = $('sandbox-url-input');
        const tplInput = $('sandbox-template-input');
        state.sandboxUrl = urlInput ? urlInput.value.trim() : state.sandboxUrl;
        state.sandboxTemplate = tplInput ? tplInput.value : state.sandboxTemplate;
        if (!state.sandboxUrl) {
          toast('请先输入要测试的链接或分享口令', 'error');
          return;
        }
        state.sandboxLoading = true;
        renderRoute();
        try {
          if (action === 'sandbox-preview') {
            state.sandboxResult = await api('/api/parse/preview', {
              method: 'POST',
              body: { url: state.sandboxUrl, template: state.sandboxTemplate },
            });
            toast('解析与消息排版预览完成', 'success');
          } else {
            const res = await api('/api/parse', {
              method: 'POST',
              body: { url: state.sandboxUrl },
            });
            toast(`任务已入队执行 (ID: ${res.id.slice(0, 8)}…)`, 'success');
            await refreshRuntimeOnly();
          }
        } catch (err) {
          toast(err.message, 'error');
        } finally {
          state.sandboxLoading = false;
          renderRoute();
        }
        return;
      }

      if (action === 'export-config') {
        const payload = JSON.stringify(state.config || state.draft, null, 2);
        const blob = new Blob([payload], { type: 'application/json;charset=utf-8' });
        const url = URL.createObjectURL(blob);
        const a = document.createElement('a');
        const date = new Date().toISOString().slice(0, 10);
        a.href = url;
        a.download = `multiparse-config-${date}.json`;
        document.body.appendChild(a);
        a.click();
        a.remove();
        URL.revokeObjectURL(url);
        toast('已导出脱敏配置 JSON 文件', 'success');
        return;
      }

      if (action === 'trigger-import-config') {
        $('import-config-input').click();
      }
    });

    // 表单输入实时双向绑定（草稿状态与实时预览）
    document.addEventListener('input', (e) => {
      const el = e.target;
      if (!el) return;

      if (el.id === 'sandbox-url-input') {
        state.sandboxUrl = el.value;
        return;
      }
      if (el.id === 'sandbox-template-input') {
        state.sandboxTemplate = el.value;
        return;
      }
      if (el.id === 'tasks-search-input') {
        state.tasksQuery = el.value;
        const pos = el.selectionStart;
        renderRoute();
        const next = $('tasks-search-input');
        if (next) {
          next.focus();
          next.setSelectionRange(pos, pos);
        }
        return;
      }
      if (el.id === 'platform-search-input') {
        state.platformQuery = el.value;
        const pos = el.selectionStart;
        renderRoute();
        const next = $('platform-search-input');
        if (next) {
          next.focus();
          next.setSelectionRange(pos, pos);
        }
        return;
      }
      if (el.id === 'proxy-test-platform') {
        state.proxyTestPlatform = el.value;
        return;
      }

      if (!state.draft) return;

      // 平台卡片快捷启用开关
      if (el.dataset.action === 'toggle-platform-enabled') {
        const pid = el.dataset.platform;
        if (state.draft.platforms[pid]) {
          state.draft.platforms[pid].enabled = el.checked;
          updateDirtyBar();
          renderRoute();
        }
        return;
      }

      // 侧滑抽屉字段绑定
      if (el.dataset.drawerField && state.drawerPlatformId) {
        const pol = state.draft.platforms[state.drawerPlatformId];
        const field = el.dataset.drawerField;
        pol[field] = el.type === 'checkbox' ? el.checked : el.value;
        updateDirtyBar();
        if (field === 'message_template') {
          const meta = state.platforms.find((p) => p.id === state.drawerPlatformId);
          const sample = {
            platform_name: meta ? meta.name : state.drawerPlatformId,
            type_label: typeName((meta && meta.types && meta.types[0]) || 'video'),
            title: `${meta ? meta.name : ''}示例作品标题`,
            author: '示例创作者',
            content: `${meta ? meta.name : ''}正文内容摘要，支持任意组合标题、作者、统计与自定义文字。`,
            url: `https://${(meta && meta.domains && meta.domains[0]) || 'example.com'}/post/10086?utm_source=share`,
            clean_url: `https://${(meta && meta.domains && meta.domains[0]) || 'example.com'}/post/10086`,
            media_stats: '共 1 个视频',
          };
          const previewEl = $('drawer-live-preview');
          if (previewEl) {
            const coverMode = pol.cover_mode || 'inherit';
            const showCover = coverMode === 'on' ? true : (coverMode === 'off' ? false : state.draft.media.show_cover);
            const txt = renderLocalPreview(pol.message_template || '', sample, state.draft.media.clean_url);
            previewEl.innerHTML = `${state.draft.policy.group_reply ? '<div class="chat-reply-quote">回复 群成员的消息</div>' : ''}${showCover ? '<div class="chat-cover-mock">🖼️ [封面预览]</div>\n' : ''}${txt ? esc(txt) : '<span class="muted">（模板为空，保存前请填写内容或切换为仅发媒体）</span>'}`;
          }
        }
        return;
      }

      if (el.dataset.bind) {
        const path = el.dataset.bind;
        const val = el.type === 'checkbox' ? el.checked : el.value;
        setNestedValue(state.draft, path, val);
        updateDirtyBar();
        if (path === 'media.transport') {
          renderRoute();
        } else if (path === 'media.message_template' || path === 'media.show_info' || path === 'media.show_cover' || path === 'media.clean_url') {
          if (path !== 'media.message_template') {
            renderRoute();
          } else {
            const previewEl = $('global-template-preview');
            if (previewEl) {
              const sample = {
                platform_name: '哔哩哔哩',
                type_label: '视频',
                title: '【4K】2026 年度技术架构演进与工程实践总结',
                author: '技术极客UP',
                content: '本期视频深入讲解多平台消息块自定义排版、Alpine 镜像瘦身与高可靠媒体流水线。',
                url: 'https://www.bilibili.com/video/BV1GJ411x7h7?p=1&vd_source=track123',
                clean_url: 'https://www.bilibili.com/video/BV1GJ411x7h7?p=1',
                media_stats: '共 1 个视频',
              };
              const txt = state.draft.media.show_info
                ? renderLocalPreview(state.draft.media.message_template || DEFAULT_TEMPLATE, sample, state.draft.media.clean_url)
                : '';
              previewEl.innerHTML = `${state.draft.policy.group_reply ? '<div class="chat-reply-quote">回复 群成员的消息</div>' : ''}${state.draft.media.show_cover ? '<div class="chat-cover-mock">🖼️ [视频封面图]</div>\n' : ''}${txt ? esc(txt) : '<span class="muted">（已关闭全局文本信息输出）</span>'}`;
            }
          }
        }
        return;
      }

      if (el.dataset.bindNum) {
        const n = Number(el.value);
        if (!Number.isNaN(n)) {
          setNestedValue(state.draft, el.dataset.bindNum, Math.trunc(n));
          updateDirtyBar();
        }
        return;
      }

      if (el.dataset.bindMib) {
        const n = Number(el.value);
        if (!Number.isNaN(n) && n > 0) {
          setNestedValue(state.draft, el.dataset.bindMib, Math.round(n * 1024 * 1024));
          updateDirtyBar();
        }
        return;
      }

      if (el.dataset.bindGib) {
        const n = Number(el.value);
        if (!Number.isNaN(n) && n > 0) {
          setNestedValue(state.draft, el.dataset.bindGib, Math.round(n * 1024 * 1024 * 1024));
          updateDirtyBar();
        }
        return;
      }

      if (el.dataset.bindList) {
        setNestedValue(state.draft, el.dataset.bindList, parseStringList(el.value));
        updateDirtyBar();
        return;
      }

      if (el.dataset.bindIntlist) {
        setNestedValue(state.draft, el.dataset.bindIntlist, parseIntList(el.value));
        updateDirtyBar();
      }
    });

    // 管理员账户修改表单（委托）
    document.addEventListener('submit', async (e) => {
      if (e.target && e.target.id === 'change-account-form') {
        e.preventDefault();
        try {
          await api('/api/account', {
            method: 'POST',
            body: {
              username: $('acc-username').value.trim(),
              current_password: $('acc-current-pwd').value,
              new_password: $('acc-new-pwd').value,
            },
          });
          toast('管理员凭据已修改，请重新登录', 'success');
          showAuthView();
        } catch (err) {
          toast(err.message, 'error');
        }
      }
    });
  }

  async function bootstrap() {
    applyTheme(state.theme);
    bindGlobalEvents();
    try {
      const me = await api('/api/me');
      await enterConsole(me);
    } catch {
      showAuthView();
    }
  }

  bootstrap();
})();
