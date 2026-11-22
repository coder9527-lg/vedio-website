/*
 * PWA 注册入口：能力守卫后在根作用域注册 Service Worker。
 * 不支持的浏览器静默跳过，不影响任何现有功能。
 */
(function () {
  if (!('serviceWorker' in navigator)) return;
  window.addEventListener('load', () => {
    navigator.serviceWorker.register('/sw.js').catch(() => {
      // 注册失败（如隐私模式/协议限制）静默忽略
    });
  });
})();
