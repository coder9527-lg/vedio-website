/*
 * 播放队列：纯前端功能，队列存 localStorage（vw:queue），不新增后端接口与数据表。
 * 职责：
 *  1. 相关视频卡片「＋ 队列」入队、队列面板逐条移除与清空；
 *  2. 进入播放页即把当前视频从队列中消费掉（队列随播放自然前进）；
 *  3. 播放结束时优先连播队首（队列为空则回退服务端给出的下一集/播放列表下一项），5 秒倒计时可取消。
 */
(function () {
  'use strict';

  var QUEUE_KEY = 'vw:queue';
  var MAX_ITEMS = 50;
  var COUNTDOWN = 5;

  var main = document.querySelector('[data-watch-id]');
  if (!main) return;

  var currentId = main.dataset.watchId || '';
  var currentName = main.dataset.watchName || '';
  var serverNextUrl = main.dataset.nextUrl || '';
  var serverNextName = main.dataset.nextName || '';

  var panel = document.querySelector('[data-queue-panel]');
  var listEl = document.querySelector('[data-queue-list]');
  var countEl = document.querySelector('[data-queue-count]');
  var toastEl = document.getElementById('queue-toast');

  // ---- 队列读写：localStorage 不可用（隐私模式）时退化为内存数组 ----
  var memory = [];
  function readQueue() {
    try {
      var raw = localStorage.getItem(QUEUE_KEY);
      if (!raw) return [];
      var arr = JSON.parse(raw);
      if (!Array.isArray(arr)) return [];
      return arr.filter(function (it) {
        return it && typeof it.id === 'string' && it.id;
      }).map(function (it) {
        return { id: it.id, name: typeof it.name === 'string' ? it.name : it.id };
      });
    } catch (err) {
      return memory.slice();
    }
  }
  function writeQueue(arr) {
    memory = arr.slice();
    try {
      localStorage.setItem(QUEUE_KEY, JSON.stringify(arr));
    } catch (err) {
      /* 写入失败时保留内存副本，本页内仍可正常使用 */
    }
  }

  function showToast(msg) {
    if (!toastEl) return;
    toastEl.textContent = msg;
    toastEl.hidden = false;
    clearTimeout(showToast._t);
    showToast._t = setTimeout(function () { toastEl.hidden = true; }, 2400);
  }

  // ---- 队列面板渲染 ----
  function renderPanel() {
    if (!panel || !listEl) return;
    var queue = readQueue();
    if (countEl) countEl.textContent = String(queue.length);
    panel.hidden = queue.length === 0;
    listEl.innerHTML = queue.map(function (it, idx) {
      return '<li class="queue-item" data-queue-item data-id="' + escapeAttr(it.id) + '">' +
        '<span class="queue-index">' + (idx + 1) + '</span>' +
        '<a class="queue-name" href="/watch/' + encodeURIComponent(it.id) + '" title="' + escapeAttr(it.name) + '">' +
        escapeHTML(it.name) + '</a>' +
        '<button type="button" class="queue-remove" data-queue-remove data-id="' + escapeAttr(it.id) +
        '" aria-label="从队列移除" title="从队列移除">✕</button></li>';
    }).join('');
  }

  function escapeHTML(s) {
    return String(s == null ? '' : s)
      .replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;')
      .replace(/"/g, '&quot;').replace(/'/g, '&#39;');
  }
  function escapeAttr(s) { return escapeHTML(s); }

  // ---- 入队：禁止重复与当前正在播放的视频 ----
  function enqueue(id, name) {
    if (!id) return;
    if (id === currentId) { showToast('当前视频正在播放，无需入队'); return; }
    var queue = readQueue();
    if (queue.some(function (it) { return it.id === id; })) {
      showToast('「' + (name || id) + '」已在队列中');
      return;
    }
    if (queue.length >= MAX_ITEMS) { showToast('队列已满（最多 ' + MAX_ITEMS + ' 个）'); return; }
    queue.push({ id: id, name: name || id });
    writeQueue(queue);
    renderPanel();
    showToast('已加入队列：' + (name || id));
  }

  function dequeue(id) {
    var queue = readQueue().filter(function (it) { return it.id !== id; });
    writeQueue(queue);
    renderPanel();
  }

  // 进入播放页即消费当前视频：从队列移除，避免重复排队
  if (currentId) {
    var cleaned = readQueue().filter(function (it) { return it.id !== currentId; });
    writeQueue(cleaned);
  }
  renderPanel();

  // ---- 面板交互（事件委托，重绘后依然有效）----
  if (panel) {
    panel.addEventListener('click', function (e) {
      var removeBtn = e.target.closest('[data-queue-remove]');
      if (removeBtn) {
        e.preventDefault();
        dequeue(removeBtn.dataset.id);
        showToast('已从队列移除');
        return;
      }
      if (e.target.closest('[data-queue-clear]')) {
        e.preventDefault();
        if (!readQueue().length) return;
        if (!confirm('确定清空播放队列？')) return;
        writeQueue([]);
        renderPanel();
        showToast('播放队列已清空');
      }
    });
  }

  // 相关视频卡片入队按钮
  document.addEventListener('click', function (e) {
    var addBtn = e.target.closest('[data-queue-add]');
    if (!addBtn) return;
    e.preventDefault();
    e.stopPropagation();
    enqueue(addBtn.dataset.id, addBtn.dataset.name);
  });

  // ---- 播完连播：队列优先，否则服务端下一集 ----
  var player = document.getElementById('player');
  if (!player) return;

  function ensureBox() {
    var box = document.getElementById('auto-next');
    if (box) return box;
    box = document.createElement('div');
    box.id = 'auto-next';
    box.className = 'auto-next';
    box.hidden = true;
    box.setAttribute('data-auto-next', '');
    box.innerHTML =
      '<div class="auto-next-card">' +
        '<p class="auto-next-title">本集播放完毕</p>' +
        '<p class="auto-next-desc"><span id="auto-next-count">' + COUNTDOWN +
        '</span> 秒后自动播放下一个：<span id="auto-next-name"></span></p>' +
        '<div class="auto-next-actions">' +
          '<button type="button" id="auto-next-go" class="btn-primary">立即播放</button>' +
          '<button type="button" id="auto-next-cancel" class="btn-secondary">取消连播</button>' +
        '</div>' +
      '</div>';
    document.body.appendChild(box);
    return box;
  }

  var box = null;
  var countEl = null;
  var nameEl = null;
  var timer = null;
  var remain = COUNTDOWN;
  var target = null; // { url, name, fromQueue }

  function setBox(b) {
    box = b;
    countEl = b.querySelector('#auto-next-count');
    nameEl = b.querySelector('#auto-next-name');
  }

  function clearTimer() {
    if (timer) { clearInterval(timer); timer = null; }
  }

  function go() {
    if (!target) return;
    clearTimer();
    if (target.fromQueue) {
      // 消费队首：先出队再跳转，刷新后队列即前进一项
      var queue = readQueue();
      if (queue.length && queue[0].id === target.id) {
        queue.shift();
        writeQueue(queue);
      }
    }
    location.href = target.url;
  }

  function cancel() {
    clearTimer();
    if (box) box.hidden = true;
    target = null;
  }

  function startCountdown() {
    var queue = readQueue();
    if (queue.length) {
      target = {
        url: '/watch/' + encodeURIComponent(queue[0].id),
        name: queue[0].name,
        id: queue[0].id,
        fromQueue: true
      };
    } else if (serverNextUrl) {
      target = { url: serverNextUrl, name: serverNextName, fromQueue: false };
    } else {
      return; // 既无队列也无下一集：保持原行为，不弹卡片
    }

    if (!box) setBox(ensureBox());
    if (nameEl) nameEl.textContent = target.name || '';
    if (countEl) countEl.textContent = String(COUNTDOWN);
    box.hidden = false;

    remain = COUNTDOWN;
    clearTimer();
    timer = setInterval(function () {
      remain -= 1;
      if (remain <= 0) { go(); return; }
      if (countEl) countEl.textContent = String(remain);
    }, 1000);
  }

  // 延迟绑定：服务端卡片可能晚于本脚本存在，故每次结束时重取一次
  player.addEventListener('ended', function () {
    if (!box) {
      var existing = document.getElementById('auto-next');
      if (existing) setBox(existing);
    }
    startCountdown();
  });

  // 用户手动重播本集时撤销连播
  player.addEventListener('play', function () {
    if (box && !box.hidden) cancel();
  });

  // 卡片按钮（服务端渲染与脚本动态创建共用）
  document.addEventListener('click', function (e) {
    if (e.target.id === 'auto-next-go') { e.preventDefault(); go(); return; }
    if (e.target.id === 'auto-next-cancel') { e.preventDefault(); cancel(); }
  });
})();
