/*
 * 年度回顾 - 月度观看目标控制器。
 * 目标以整数分钟为单位存 localStorage（键 vw:review-goal-min），纯前端、零后端依赖，
 * 不跨设备同步；隐私模式等 localStorage 抛错时降级为内存态（仅当前页面生效）。
 * 达成率取参照月份的观看秒数：当前年取本月，历史年取全年观看最多的月份。
 */
(function () {
  'use strict';

  var STORAGE_KEY = 'vw:review-goal-min';
  // 与模板 input 的 max 对齐，防止手输超大值撑爆进度条
  var MAX_GOAL_MIN = 100000;

  // 内存态兜底：localStorage 不可用时仅当前页面记住目标
  var memoryGoal = null;

  function getGoal() {
    try {
      var raw = window.localStorage.getItem(STORAGE_KEY);
      var n = parseInt(raw, 10);
      if (Number.isFinite(n) && n > 0) return n;
    } catch (err) {
      if (memoryGoal !== null) return memoryGoal;
    }
    return null;
  }

  function setGoal(minutes) {
    try {
      window.localStorage.setItem(STORAGE_KEY, String(minutes));
    } catch (err) {
      memoryGoal = minutes;
    }
  }

  function clearGoal() {
    try {
      window.localStorage.removeItem(STORAGE_KEY);
    } catch (err) {
      memoryGoal = null;
    }
  }

  // 读取参照月份的观看秒数：当前年取本月，历史年取 12 行中秒数最大的月份
  function referenceSeconds() {
    var rows = document.querySelectorAll('.dist-row[data-seconds]');
    if (!rows.length) return 0;
    var now = new Date();
    var currentMonth = now.getMonth() + 1;
    var best = 0;
    for (var i = 0; i < rows.length; i++) {
      var secs = parseFloat(rows[i].getAttribute('data-seconds')) || 0;
      var month = parseInt(rows[i].getAttribute('data-month'), 10);
      if (month === currentMonth) return secs; // 当前年页面命中本月直接返回
      if (secs > best) best = secs;
    }
    return best;
  }

  // 依据已保存目标刷新进度条；无目标时整行隐藏
  function render() {
    var row = document.getElementById('goal-row');
    var fill = document.getElementById('goal-fill');
    var text = document.getElementById('goal-text');
    if (!row || !fill || !text) return;

    var goal = getGoal();
    if (goal === null) {
      row.hidden = true;
      return;
    }

    var watchedMin = Math.round(referenceSeconds() / 60);
    var pct = Math.min(100, Math.round(watchedMin * 100 / goal));
    row.hidden = false;
    fill.style.width = pct + '%';
    text.textContent = '已看 ' + watchedMin + ' 分钟 / 目标 ' + goal + ' 分钟（' + pct + '%）';
  }

  // 校验并保存输入：单位换算为分钟，钳制到 [1, MAX_GOAL_MIN]
  function saveFromForm() {
    var input = document.getElementById('goal-input');
    var unit = document.getElementById('goal-unit');
    if (!input) return;

    var value = parseFloat(input.value);
    if (!Number.isFinite(value) || value <= 0) {
      input.focus();
      return;
    }
    var minutes = unit && unit.value === 'hour' ? Math.round(value * 60) : Math.round(value);
    if (minutes > MAX_GOAL_MIN) minutes = MAX_GOAL_MIN;
    if (minutes < 1) minutes = 1;

    setGoal(minutes);
    input.value = '';
    render();
  }

  function bind() {
    var saveBtn = document.getElementById('goal-save');
    var input = document.getElementById('goal-input');
    if (saveBtn) saveBtn.addEventListener('click', saveFromForm);
    if (input) {
      input.addEventListener('keydown', function (e) {
        if (e.key === 'Enter') {
          e.preventDefault();
          saveFromForm();
        }
      });
    }
    render();
  }

  // 暴露给控制台/测试使用的最小控制器
  window.ReviewGoal = {
    get: getGoal,
    set: function (minutes) {
      var n = parseInt(minutes, 10);
      if (!Number.isFinite(n) || n <= 0) {
        clearGoal();
      } else {
        setGoal(Math.min(MAX_GOAL_MIN, n));
      }
      render();
    },
    clear: function () {
      clearGoal();
      render();
    }
  };

  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', bind);
  } else {
    bind();
  }
})();
