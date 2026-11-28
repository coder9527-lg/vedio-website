/*
 * 首页卡片密度控制器：紧凑 / 标准 / 宽松 三档，偏好存 localStorage（vw:cardDensity）。
 * 生效方式是在 <html> 上写 data-density 属性，CSS 据此调整卡片网格列宽与间距，
 * 纯前端实现，不新增后端接口。首帧防闪烁由 index.html head 内的同步内联脚本负责，
 * 本脚本（defer）只处理分段按钮交互。localStorage 不可用时降级为内存态。
 */
(function () {
  'use strict';

  var STORAGE_KEY = 'vw:cardDensity';
  var DENSITIES = ['compact', 'normal', 'comfortable'];
  var DEFAULT = 'normal';

  var memoryDensity = null;

  function get() {
    try {
      var raw = window.localStorage.getItem(STORAGE_KEY);
      if (DENSITIES.indexOf(raw) !== -1) return raw;
    } catch (err) {
      if (memoryDensity && DENSITIES.indexOf(memoryDensity) !== -1) return memoryDensity;
    }
    return DEFAULT;
  }

  function set(density) {
    if (DENSITIES.indexOf(density) === -1) density = DEFAULT;
    try {
      window.localStorage.setItem(STORAGE_KEY, density);
    } catch (err) {
      memoryDensity = density;
    }
    apply(density);
  }

  // 写 <html data-density> 并同步分段控件的选中态
  function apply(density) {
    document.documentElement.setAttribute('data-density', density);
    var buttons = document.querySelectorAll('[data-density-option]');
    for (var i = 0; i < buttons.length; i++) {
      var active = buttons[i].getAttribute('data-density-option') === density;
      buttons[i].classList.toggle('active', active);
      buttons[i].setAttribute('aria-pressed', active ? 'true' : 'false');
    }
  }

  function bind() {
    var group = document.querySelector('[data-density-group]');
    if (!group) return;
    group.addEventListener('click', function (e) {
      var btn = e.target.closest('[data-density-option]');
      if (btn) set(btn.getAttribute('data-density-option'));
    });
    apply(get());
  }

  window.CardDensity = { get: get, set: set };

  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', bind);
  } else {
    bind();
  }
})();
