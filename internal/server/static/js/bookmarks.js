/*
 * 全局书签页控制器：页内管理（编辑备注/删除/批量删除）、备注与视频名搜索、
 * 分组/平铺视图切换、排序、JSON/Markdown 导出、复制跳转链接、分组折叠，
 * 以及视图/排序/折叠偏好的本地持久化。
 * 数据唯一来源是模板注入的 data-bookmarks-json 数据岛；
 * 写操作复用播放页同一套书签 API，本文件不引入任何后端新接口。
 */
(function () {
  'use strict';

  var PREF_VIEW = 'vw:bm:view';
  var PREF_SORT = 'vw:bm:sort';
  var PREF_COLLAPSED = 'vw:bm:collapsed';

  var jsonEl = document.querySelector('[data-bookmarks-json]');
  var groupView = document.querySelector('[data-bm-group-view]');
  var flatView = document.querySelector('[data-bm-flat-view]');
  if (!jsonEl || !groupView || !flatView) return;

  var records = [];
  try {
    var parsed = JSON.parse(jsonEl.textContent || '[]');
    if (Array.isArray(parsed)) records = parsed;
  } catch (err) {
    records = [];
  }

  // ---- 本地偏好读取：localStorage 不可用（隐私模式等）时静默降级默认值 ----
  function prefGet(key) {
    try { return localStorage.getItem(key); } catch (err) { return null; }
  }
  function prefSet(key, val) {
    try { localStorage.setItem(key, val); } catch (err) { /* 忽略写入失败 */ }
  }
  function prefGetJSON(key) {
    var raw = prefGet(key);
    if (!raw) return [];
    try {
      var v = JSON.parse(raw);
      return Array.isArray(v) ? v : [];
    } catch (err) { return []; }
  }
  function prefSetJSON(key, val) {
    try { prefSet(key, JSON.stringify(val)); } catch (err) { /* 忽略 */ }
  }

  var initView = prefGet(PREF_VIEW) === 'flat' ? 'flat' : 'group';
  var initSort = prefGet(PREF_SORT) === 'position' ? 'position' : 'recent';
  var state = { view: initView, sort: initSort, query: '' };
  var selected = new Set();               // 勾选的书签 id（跨搜索保留，删除后清理）
  var collapsed = new Set(prefGetJSON(PREF_COLLAPSED)); // 折叠的视频 ID

  var searchInput = document.querySelector('[data-bm-search]');
  var sortSelect = document.querySelector('[data-bm-sort]');
  var noResultEl = document.querySelector('[data-bm-no-result]');
  var toastEl = document.querySelector('[data-bm-toast]');
  var batchBar = document.querySelector('[data-bm-batch-bar]');
  var batchCount = document.querySelector('[data-bm-batch-count]');
  var batchDeleteBtn = document.querySelector('[data-bm-batch-delete]');
  var selectAllCb = document.querySelector('[data-bm-select-all]');
  var toastTimer = null;

  function showToast(msg) {
    if (!toastEl) return;
    toastEl.textContent = msg;
    toastEl.hidden = false;
    requestAnimationFrame(function () { toastEl.classList.add('show'); });
    clearTimeout(toastTimer);
    toastTimer = setTimeout(function () {
      toastEl.classList.remove('show');
      setTimeout(function () { toastEl.hidden = true; }, 250);
    }, 2500);
  }

  function escapeHTML(s) {
    return String(s == null ? '' : s)
      .replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;')
      .replace(/"/g, '&quot;').replace(/'/g, '&#39;');
  }

  function matchesQuery(rec) {
    if (!state.query) return true;
    var q = state.query.toLowerCase();
    return (rec.note || '').toLowerCase().indexOf(q) >= 0 ||
           (rec.videoName || '').toLowerCase().indexOf(q) >= 0;
  }

  function filteredRecords() {
    return records.filter(matchesQuery);
  }

  // 平铺排序：recent 按创建时间倒序（同时间 id 倒序）；position 按时间点升序（同位置 id 升序）
  function sortFlat(list) {
    var copy = list.slice();
    copy.sort(function (a, b) {
      if (state.sort === 'position') {
        if (a.position !== b.position) return a.position - b.position;
        return a.id - b.id;
      }
      if (a.createdAt !== b.createdAt) return b.createdAt - a.createdAt;
      return b.id - a.id;
    });
    return copy;
  }

  // 按视频分组，返回 [{ videoId, videoName, items }]；组内始终按位置升序
  function groupRecords(list) {
    var order = [];
    var map = {};
    list.forEach(function (rec) {
      if (!map[rec.videoId]) {
        map[rec.videoId] = { videoId: rec.videoId, videoName: rec.videoName, items: [] };
        order.push(rec.videoId);
      }
      map[rec.videoId].items.push(rec);
    });
    var groups = order.map(function (id) { return map[id]; });
    groups.forEach(function (g) {
      g.items.sort(function (a, b) {
        if (a.position !== b.position) return a.position - b.position;
        return a.id - b.id;
      });
    });
    if (state.sort === 'position') {
      // 时间点模式：组按组内最早书签的位置升序
      groups.sort(function (a, b) { return a.items[0].position - b.items[0].position; });
    } else {
      // 最近打点：组按组内最新创建时间倒序
      groups.sort(function (a, b) {
        var ma = Math.max.apply(null, a.items.map(function (r) { return r.createdAt; }));
        var mb = Math.max.apply(null, b.items.map(function (r) { return r.createdAt; }));
        return mb - ma;
      });
    }
    return groups;
  }

  function itemButtonsHTML(rec) {
    return '<button type="button" class="bm-action" data-bm-copy data-id="' + rec.id +
      '" title="复制跳转链接">复制</button>' +
      '<button type="button" class="bm-action" data-bm-edit data-id="' + rec.id +
      '" data-video="' + escapeHTML(rec.videoId) + '" title="编辑备注">编辑</button>' +
      '<button type="button" class="bm-action bm-action-danger" data-bm-delete data-id="' +
      rec.id + '" data-video="' + escapeHTML(rec.videoId) + '" title="删除书签">删除</button>';
  }

  function checkHTML(rec) {
    return '<input type="checkbox" class="bm-row-check" data-bm-check data-id="' + rec.id +
      '"' + (selected.has(rec.id) ? ' checked' : '') + ' aria-label="选择该书签">';
  }

  function noteHTML(rec, flat) {
    var cls = flat ? 'bm-note' : 'bm-note';
    return rec.note
      ? '<span class="' + cls + '" data-note-text title="' + escapeHTML(rec.note) + '">' + escapeHTML(rec.note) + '</span>'
      : '<span class="' + cls + ' bm-note-empty" data-note-text>无备注</span>';
  }

  function itemHTML(rec) {
    return '<li class="bm-item" data-bookmark-item data-bookmark-id="' + rec.id +
      '" data-video-id="' + escapeHTML(rec.videoId) + '">' +
      checkHTML(rec) +
      '<a class="bm-time" href="' + escapeHTML(rec.seekUrl) +
      '" data-bookmark-seek title="跳转到 ' + rec.timeText + '">' + rec.timeText + '</a>' +
      noteHTML(rec, false) + itemButtonsHTML(rec) + '</li>';
  }

  function renderGroupView(list) {
    var groups = groupRecords(list);
    var html = groups.map(function (g) {
      var isCollapsed = collapsed.has(g.videoId);
      var items = g.items.map(itemHTML).join('');
      return '<section class="bm-group shelf' + (isCollapsed ? ' is-collapsed' : '') +
        '" data-bookmark-group data-video-id="' + escapeHTML(g.videoId) + '">' +
        '<h2 class="shelf-title bm-group-title">' +
        '<button type="button" class="bm-collapse-btn" data-bm-collapse data-video="' +
        escapeHTML(g.videoId) + '" aria-expanded="' + (!isCollapsed) +
        '" title="折叠/展开该视频">▾</button>' +
        '<a href="/watch/' + encodeURIComponent(g.videoId) +
        '" class="bm-group-link">' + escapeHTML(g.videoName) + '</a>' +
        '<span class="bm-group-count">' + g.items.length + ' 条</span></h2>' +
        '<ul class="bm-list bm-global-list" data-bm-group-list' +
        (isCollapsed ? ' hidden' : '') + '>' + items + '</ul></section>';
    }).join('');
    groupView.innerHTML = html;
  }

  function renderFlatView(list) {
    var sorted = sortFlat(list);
    var html = '<ul class="bm-list bm-global-list bm-flat-list">' + sorted.map(function (rec) {
      return '<li class="bm-item bm-flat-item" data-bookmark-item data-bookmark-id="' + rec.id + '">' +
        checkHTML(rec) +
        '<a class="bm-time" href="' + escapeHTML(rec.seekUrl) +
        '" data-bookmark-seek title="跳转到 ' + rec.timeText + '">' + rec.timeText + '</a>' +
        '<span class="bm-flat-video" title="' + escapeHTML(rec.videoName) + '">' +
        escapeHTML(rec.videoName) + '</span>' + noteHTML(rec, true) + itemButtonsHTML(rec) + '</li>';
    }).join('') + '</ul>';
    flatView.innerHTML = html;
  }

  // 同步批量管理条：显隐、已选计数、全选框状态、删除按钮可用性
  function syncBatchBar(visibleList) {
    var visibleIds = new Set(visibleList.map(function (r) { return r.id; }));
    var visibleSelected = visibleList.filter(function (r) { return selected.has(r.id); });
    var totalSelected = records.filter(function (r) { return selected.has(r.id); }).length;
    batchBar.hidden = totalSelected === 0;
    batchCount.textContent = '已选 ' + totalSelected + ' 条';
    batchDeleteBtn.disabled = totalSelected === 0;
    selectAllCb.checked = visibleIds.size > 0 && visibleSelected.length === visibleIds.size;
    selectAllCb.indeterminate = visibleSelected.length > 0 && visibleSelected.length < visibleIds.size;
  }

  function render() {
    var list = filteredRecords();
    noResultEl.hidden = list.length !== 0;
    if (state.view === 'flat') {
      groupView.hidden = true;
      flatView.hidden = false;
      renderFlatView(list);
    } else {
      flatView.hidden = true;
      groupView.hidden = false;
      renderGroupView(list);
    }
    syncBatchBar(list);
  }

  // ---- 写操作：与播放页 bmFetch 同口径处理 401/503 ----
  function bmFetch(url, options) {
    return fetch(url, options).then(function (res) {
      if (res.status === 401) { location.href = '/login'; throw new Error('未登录'); }
      if (res.status === 503) { alert('未启用持久化（DB_PATH 未配置）'); throw new Error('503'); }
      if (!res.ok) {
        var msg = 'HTTP ' + res.status;
        return res.json().catch(function () { return {}; }).then(function (d) {
          if (d.error) msg = d.error;
          throw new Error(msg);
        });
      }
      return res.json();
    });
  }

  function bookmarkURL(rec) {
    return '/api/v1/videos/' + encodeURIComponent(rec.videoId) + '/bookmarks/' + rec.id;
  }

  function editNote(id) {
    var rec = records.find(function (r) { return r.id === id; });
    if (!rec) return;
    var note = prompt('编辑书签备注（可留空）', rec.note || '');
    if (note === null) return;
    bmFetch(bookmarkURL(rec), {
      method: 'PATCH',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ note: note })
    }).then(function (data) {
      var idx = records.findIndex(function (r) { return r.id === id; });
      if (idx >= 0 && data.bookmark) records[idx].note = data.bookmark.note || '';
      render();
      showToast('书签备注已更新');
    }).catch(function (err) {
      if (err.message !== '未登录' && err.message !== '503') alert('更新失败：' + err.message);
    });
  }

  function removeBookmarks(ids) {
    var targets = records.filter(function (r) { return ids.indexOf(r.id) >= 0; });
    if (!targets.length) return;
    if (!confirm('确定删除选中的 ' + targets.length + ' 条书签？')) return;
    Promise.all(targets.map(function (rec) {
      return bmFetch(bookmarkURL(rec), { method: 'DELETE' })
        .then(function () { return { id: rec.id, ok: true }; })
        .catch(function () { return { id: rec.id, ok: false }; });
    })).then(function (results) {
      var okIds = results.filter(function (r) { return r.ok; }).map(function (r) { return r.id; });
      var failCount = results.length - okIds.length;
      var okSet = new Set(okIds);
      records = records.filter(function (r) { return !okSet.has(r.id); });
      okIds.forEach(function (id) { selected.delete(id); });
      if (records.length === 0) { location.reload(); return; }
      render();
      showToast(failCount ? '已删除 ' + okIds.length + ' 条，' + failCount + ' 条失败' : '书签已删除');
    });
  }

  // ---- 复制跳转链接：优先 Clipboard API，降级 textarea + execCommand ----
  function copyLink(id) {
    var rec = records.find(function (r) { return r.id === id; });
    if (!rec) return;
    var url = location.origin + rec.seekUrl;
    var done = function () { showToast('链接已复制：' + rec.timeText); };
    if (navigator.clipboard && navigator.clipboard.writeText) {
      navigator.clipboard.writeText(url).then(done).catch(function () { fallbackCopy(url, done); });
    } else {
      fallbackCopy(url, done);
    }
  }
  function fallbackCopy(text, done) {
    var ta = document.createElement('textarea');
    ta.value = text;
    ta.style.position = 'fixed';
    ta.style.opacity = '0';
    document.body.appendChild(ta);
    ta.select();
    try { document.execCommand('copy'); done(); }
    catch (err) { alert('复制失败，请手动复制：' + text); }
    ta.remove();
  }

  // 事件委托：分组与平铺两套 DOM 共用一套管理逻辑
  document.querySelector('[data-bookmarks-page]').addEventListener('click', function (e) {
    var copyBtn = e.target.closest('[data-bm-copy]');
    var editBtn = e.target.closest('[data-bm-edit]');
    var delBtn = e.target.closest('[data-bm-delete]');
    var collapseBtn = e.target.closest('[data-bm-collapse]');
    if (copyBtn) {
      e.preventDefault();
      copyLink(Number(copyBtn.dataset.id));
      return;
    }
    if (editBtn) {
      e.preventDefault();
      editNote(Number(editBtn.dataset.id));
      return;
    }
    if (delBtn) {
      e.preventDefault();
      var rec = records.find(function (r) { return r.id === Number(delBtn.dataset.id); });
      removeBookmarks([rec ? rec.id : Number(delBtn.dataset.id)]);
      return;
    }
    if (collapseBtn) {
      e.preventDefault();
      var videoId = collapseBtn.dataset.video;
      var section = collapseBtn.closest('[data-bookmark-group]');
      var listEl = section.querySelector('[data-bm-group-list]');
      if (collapsed.has(videoId)) {
        collapsed.delete(videoId);
        section.classList.remove('is-collapsed');
        collapseBtn.setAttribute('aria-expanded', 'true');
        if (listEl) listEl.hidden = false;
      } else {
        collapsed.add(videoId);
        section.classList.add('is-collapsed');
        collapseBtn.setAttribute('aria-expanded', 'false');
        if (listEl) listEl.hidden = true;
      }
      prefSetJSON(PREF_COLLAPSED, Array.from(collapsed));
    }
  });

  // 勾选变化（change 事件，兼容键盘空格勾选）
  document.querySelector('[data-bookmarks-page]').addEventListener('change', function (e) {
    var check = e.target.closest('[data-bm-check]');
    if (check) {
      var id = Number(check.dataset.id);
      if (check.checked) selected.add(id); else selected.delete(id);
      syncBatchBar(filteredRecords());
      return;
    }
    if (e.target === selectAllCb) {
      filteredRecords().forEach(function (r) {
        if (selectAllCb.checked) selected.add(r.id); else selected.delete(r.id);
      });
      render();
    }
  });

  batchDeleteBtn.addEventListener('click', function () {
    var ids = records.filter(function (r) { return selected.has(r.id); }).map(function (r) { return r.id; });
    removeBookmarks(ids);
  });

  // ---- 搜索 / 视图 / 排序（偏好同步写入 localStorage）----
  if (searchInput) {
    searchInput.addEventListener('input', function () {
      state.query = searchInput.value.trim();
      render();
    });
  }
  if (sortSelect) {
    sortSelect.value = state.sort; // 恢复排序偏好
    sortSelect.addEventListener('change', function () {
      state.sort = sortSelect.value === 'position' ? 'position' : 'recent';
      prefSet(PREF_SORT, state.sort);
      render();
    });
  }
  function applyViewButtons() {
    document.querySelectorAll('[data-bm-view]').forEach(function (b) {
      var active = b.dataset.bmView === state.view;
      b.classList.toggle('is-active', active);
      b.setAttribute('aria-pressed', String(active));
    });
  }
  document.querySelectorAll('[data-bm-view]').forEach(function (btn) {
    btn.addEventListener('click', function () {
      state.view = btn.dataset.bmView;
      prefSet(PREF_VIEW, state.view);
      applyViewButtons();
      render();
    });
  });

  // ---- 导出：纯前端生成文件，导出范围与当前搜索结果一致 ----
  function download(filename, mime, content) {
    var blob = new Blob([content], { type: mime + ';charset=utf-8' });
    var url = URL.createObjectURL(blob);
    var a = document.createElement('a');
    a.href = url;
    a.download = filename;
    document.body.appendChild(a);
    a.click();
    a.remove();
    setTimeout(function () { URL.revokeObjectURL(url); }, 1000);
  }

  function stamp() {
    var d = new Date();
    var p = function (n) { return String(n).padStart(2, '0'); };
    return d.getFullYear() + p(d.getMonth() + 1) + p(d.getDate()) + '-' + p(d.getHours()) + p(d.getMinutes());
  }

  function exportJSON(list) {
    var payload = list.map(function (r) {
      return {
        id: r.id, videoId: r.videoId, videoName: r.videoName,
        position: r.position, timeText: r.timeText, note: r.note,
        seekUrl: r.seekUrl, createdAt: r.createdAt
      };
    });
    download('bookmarks-' + stamp() + '.json', 'application/json', JSON.stringify(payload, null, 2));
  }

  function mdEscape(s) {
    return String(s == null ? '' : s).replace(/\|/g, '\\|').replace(/\n/g, ' ');
  }

  function exportMarkdown(list) {
    var groups = groupRecords(list);
    var lines = ['# 全部书签（' + list.length + ' 条）', ''];
    groups.forEach(function (g) {
      lines.push('## ' + mdEscape(g.videoName));
      lines.push('');
      g.items.forEach(function (r) {
        var line = '- [' + r.timeText + '](' + r.seekUrl + ')';
        if (r.note) line += ' — ' + mdEscape(r.note);
        lines.push(line);
      });
      lines.push('');
    });
    download('bookmarks-' + stamp() + '.md', 'text/markdown', lines.join('\n'));
  }

  document.querySelectorAll('[data-bm-export]').forEach(function (btn) {
    btn.addEventListener('click', function () {
      var list = filteredRecords();
      if (!list.length) { alert('当前没有可导出的书签'); return; }
      if (btn.dataset.bmExport === 'markdown') exportMarkdown(list);
      else exportJSON(list);
      showToast('已导出 ' + list.length + ' 条书签');
    });
  });

  applyViewButtons();
  render();
})();
