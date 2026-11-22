/*
 * 主题控制器：浅色 / 深色 / 跟随系统 三档，全站偏好存 localStorage。
 * 首帧防闪烁由各模板 head 内的同步内联脚本负责，本脚本（defer）只处理交互：
 * 读取偏好、循环切换、响应系统外观变化、同步按钮图标与 theme-color。
 * 不依赖任何后端接口，localStorage 不可用时降级为内存态（仅当前页面生效）。
 */
(function () {
  'use strict';

  var STORAGE_KEY = 'vw:theme';
  var MODES = ['system', 'light', 'dark'];
  // 各档按钮图标与无障碍标签
  var ICONS = { system: '🌓', light: '☀️', dark: '🌙' };
  var LABELS = {
    system: '主题：跟随系统（点击切换为浅色）',
    light: '主题：浅色（点击切换为深色）',
    dark: '主题：深色（点击切换为跟随系统）'
  };
  // 与 CSS 变量档对齐的浏览器地址栏配色
  var THEME_COLORS = { light: '#f1f5f9', dark: '#0f172a' };

  // 内存态兜底：隐私模式等场景 localStorage 抛错时仅当前页面记住选择
  var memoryMode = null;
  var media = window.matchMedia('(prefers-color-scheme: light)');

  // 读取原始偏好：缺省或非法值一律回退跟随系统
  function get() {
    try {
      var raw = window.localStorage.getItem(STORAGE_KEY);
      if (MODES.indexOf(raw) !== -1) return raw;
    } catch (err) {
      if (memoryMode && MODES.indexOf(memoryMode) !== -1) return memoryMode;
    }
    return 'system';
  }

  // 解析当前实际生效的外观，返回 light 或 dark
  function effective() {
    var mode = get();
    if (mode === 'system') return media.matches ? 'light' : 'dark';
    return mode;
  }

  // 同步 <meta name="theme-color">，与实际外观保持一致
  function syncMetaColor() {
    var meta = document.querySelector('meta[name="theme-color"]');
    if (meta) meta.setAttribute('content', THEME_COLORS[effective()]);
  }

  // 刷新所有切换按钮的图标与提示
  function renderButtons() {
    var mode = get();
    var buttons = document.querySelectorAll('[data-theme-toggle]');
    for (var i = 0; i < buttons.length; i++) {
      var btn = buttons[i];
      btn.textContent = ICONS[mode];
      btn.setAttribute('aria-label', LABELS[mode]);
      btn.setAttribute('title', LABELS[mode]);
    }
  }

  // 把实际外观写入 <html data-theme>，CSS 变量据此切换
  function apply() {
    document.documentElement.setAttribute('data-theme', effective());
    syncMetaColor();
    renderButtons();
  }

  // 校验后保存偏好并立即生效
  function set(mode) {
    if (MODES.indexOf(mode) === -1) mode = 'system';
    try {
      window.localStorage.setItem(STORAGE_KEY, mode);
    } catch (err) {
      memoryMode = mode;
    }
    apply();
  }

  // 点击按 system → light → dark → system 循环
  function cycle() {
    var current = get();
    var next = MODES[(MODES.indexOf(current) + 1) % MODES.length];
    set(next);
  }

  // 系统外观变化：仅跟随系统档需要实时响应
  function onSystemChange() {
    if (get() === 'system') apply();
  }

  function bind() {
    var buttons = document.querySelectorAll('[data-theme-toggle]');
    for (var i = 0; i < buttons.length; i++) {
      buttons[i].addEventListener('click', cycle);
    }
    // Safari < 14 只有 addListener，做能力守卫
    if (typeof media.addEventListener === 'function') {
      media.addEventListener('change', onSystemChange);
    } else if (typeof media.addListener === 'function') {
      media.addListener(onSystemChange);
    }
    apply();
  }

  window.ThemeController = { get: get, set: set, effective: effective };

  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', bind);
  } else {
    bind();
  }
})();
