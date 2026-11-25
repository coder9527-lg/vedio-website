/*
 * 全局书签页控制器：页内管理（编辑备注/删除）、备注与视频名搜索、
 * 分组/平铺视图切换、排序与 JSON/Markdown 导出。
 * 数据唯一来源是模板注入的 data-bookmarks-json 数据岛；
 * 写操作复用播放页同一套书签 API，本文件不引入任何后端新接口。
 */
(function () {
  'use strict';

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

  var state = { view: 'group', sort: 'recent', query: '' };

  var searchInput = document.querySelector('[data-bm-search]');
  var sortSelect = document.querySelector('[data-bm-sort]');
  var noResultEl = document.querySelector('[data-bm-no-result]');
  var toastEl = document.querySelector('[data-bm-toast]');
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

  // 秒 → mm:ss（超过 1 小时进位 h:mm:ss），与服务端 formatBookmarkClock 同口径
  function fmtTime(sec) {
    sec = Math.max(0, Math.floor(sec || 0));
    var h = Math.floor(sec / 3600);
    var m = Math.floor((sec % 3600) / 60);
    var s = sec % 60;
    var pad = function (n) { return String(n).padStart(2, '0'); };
    return h > 0 ? h + ':' + pad(m) + ':' + pad(s) : pad(m) + ':' + pad(s);
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
    return '<button type="button" class="bm-action" data-bm-edit data-id="' + rec.id +
      '" data-video="' + escapeHTML(rec.videoId) + '" title="编辑备注">编辑</button>' +
      '<button type="button" class="bm-action bm-action-danger" data-bm-delete data-id="' +
      rec.id + '" data-video="' + escapeHTML(rec.videoId) + '" title="删除书签">删除</button>';
  }

  function itemHTML(rec) {
    var note = rec.note
      ? '<span class="bm-note" data-note-text title="' + escapeHTML(rec.note) + '">' + escapeHTML(rec.note) + '</span>'
      : '<span class="bm-note bm-note-empty" data-note-text>无备注</span>';
    return '<li class="bm-item" data-bookmark-item data-bookmark-id="' + rec.id +
      '" data-video-id="' + escapeHTML(rec.videoId) + '">' +
      '<a class="bm-time" href="' + escapeHTML(rec.seekUrl) +
      '" data-bookmark-seek title="跳转到 ' + rec.timeText + '">' + rec.timeText + '</a>' +
      note + itemButtonsHTML(rec) + '</li>';
  }

  function renderGroupView(list) {
    var groups = groupRecords(list);
    var html = groups.map(function (g) {
      var items = g.items.map(itemHTML).join('');
      return '<section class="bm-group shelf" data-bookmark-group data-video-id="' +
        escapeHTML(g.videoId) + '">' +
        '<h2 class="shelf-title bm-group-title">' +
        '<a href="/watch/' + encodeURIComponent(g.videoId) +
        '" class="bm-group-link">' + escapeHTML(g.videoName) + '</a>' +
        '<span class="bm-group-count">' + g.items.length + ' 条</span></h2>' +
        '<ul class="bm-list bm-global-list">' + items + '</ul></section>';
    }).join('');
    groupView.innerHTML = html;
  }

  function renderFlatView(list) {
    var sorted = sortFlat(list);
    var html = '<ul class="bm-list bm-global-list bm-flat-list">' + sorted.map(function (rec) {
      var note = rec.note
        ? '<span class="bm-note" title="' + escapeHTML(rec.note) + '">' + escapeHTML(rec.note) + '</span>'
        : '<span class="bm-note bm-note-empty">无备注</span>';
      return '<li class="bm-item bm-flat-item" data-bookmark-item data-bookmark-id="' + rec.id + '">' +
        '<a class="bm-time" href="' + escapeHTML(rec.seekUrl) +
        '" data-bookmark-seek title="跳转到 ' + rec.timeText + '">' + rec.timeText + '</a>' +
        '<span class="bm-flat-video" title="' + escapeHTML(rec.videoName) + '">' +
        escapeHTML(rec.videoName) + '</span>' + note + itemButtonsHTML(rec) + '</li>';
    }).join('') + '</ul>';
    flatView.innerHTML = html;
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

  function editNote(id, videoId) {
    var rec = records.find(function (r) { return r.id === id; });
    if (!rec) return;
    var note = prompt('编辑书签备注（可留空）', rec.note || '');
    if (note === null) return;
    bmFetch('/api/v1/videos/' + encodeURIComponent(videoId) + '/bookmarks/' + id, {
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

  function removeBookmark(id, videoId, timeText) {
    if (!confirm('删除 ' + timeText + ' 的书签？')) return;
    bmFetch('/api/v1/videos/' + encodeURIComponent(videoId) + '/bookmarks/' + id, { method: 'DELETE' })
      .then(function () {
        records = records.filter(function (r) { return r.id !== id; });
        if (records.length === 0) { location.reload(); return; }
        render();
        showToast('书签已删除');
      })
      .catch(function (err) {
        if (err.message !== '未登录' && err.message !== '503') alert('删除失败：' + err.message);
      });
  }

  // 事件委托：分组与平铺两套 DOM 共用一套管理逻辑
  document.querySelector('[data-bookmarks-page]').addEventListener('click', function (e) {
    var editBtn = e.target.closest('[data-bm-edit]');
    var delBtn = e.target.closest('[data-bm-delete]');
    if (editBtn) {
      e.preventDefault();
      editNote(Number(editBtn.dataset.id), editBtn.dataset.video);
      return;
    }
    if (delBtn) {
      e.preventDefault();
      var rec = records.find(function (r) { return r.id === Number(delBtn.dataset.id); });
      removeBookmark(Number(delBtn.dataset.id), delBtn.dataset.video, rec ? rec.timeText : '');
    }
  });

  // ---- 搜索 / 视图 / 排序 ----
  if (searchInput) {
    searchInput.addEventListener('input', function () {
      state.query = searchInput.value.trim();
      render();
    });
  }
  if (sortSelect) {
    sortSelect.addEventListener('change', function () {
      state.sort = sortSelect.value === 'position' ? 'position' : 'recent';
      render();
    });
  }
  document.querySelectorAll('[data-bm-view]').forEach(function (btn) {
    btn.addEventListener('click', function () {
      state.view = btn.dataset.bmView;
      document.querySelectorAll('[data-bm-view]').forEach(function (b) {
        var active = b === btn;
        b.classList.toggle('is-active', active);
        b.setAttribute('aria-pressed', String(active));
      });
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

  render();
})();
