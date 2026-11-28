/*
 * Service Worker：个人视频库 PWA 离线基座
 * 策略：导航 network-first（失败回退缓存/离线页）；静态资源 stale-while-revalidate；
 * 媒体流与鉴权内容一律不缓存。缓存名版本化，activate 时清理旧版本。
 */
const CACHE_NAME = 'vw-cache-v7';

// 安装期预缓存：应用骨架资源（不含任何视频/图片等大文件）
const PRECACHE_URLS = [
  '/static/css/style.css',
  '/static/js/pwa.js',
  '/static/manifest.webmanifest',
  '/static/icon.svg',
  '/static/offline.html'
];

// 禁止缓存的路径前缀：大文件、Range 请求、鉴权与动态内容
const NEVER_CACHE_PREFIXES = [
  '/stream/',
  '/hls/',
  '/artwork/',
  '/thumb/',
  '/subtitles/',
  '/api/',
  '/login',
  '/logout',
  '/share/'
];

self.addEventListener('install', (event) => {
  event.waitUntil(
    caches.open(CACHE_NAME)
      .then((cache) => cache.addAll(PRECACHE_URLS))
      .then(() => self.skipWaiting())
  );
});

self.addEventListener('activate', (event) => {
  event.waitUntil(
    caches.keys()
      .then((keys) => Promise.all(
        keys.filter((key) => key !== CACHE_NAME).map((key) => caches.delete(key))
      ))
      .then(() => self.clients.claim())
  );
});

function isNeverCached(url) {
  const path = new URL(url).pathname;
  return NEVER_CACHE_PREFIXES.some((prefix) => path.startsWith(prefix));
}

self.addEventListener('fetch', (event) => {
  const request = event.request;
  // 只处理同源 GET，其余直接交给浏览器默认行为
  if (request.method !== 'GET' || new URL(request.url).origin !== self.location.origin) {
    return;
  }
  if (isNeverCached(request.url)) {
    return; // 媒体流/鉴权内容不拦截
  }

  if (request.mode === 'navigate') {
    // 页面导航：network-first，离线时回退缓存，再不行给离线页
    event.respondWith(
      fetch(request)
        .then((response) => {
          const copy = response.clone();
          caches.open(CACHE_NAME).then((cache) => cache.put(request, copy));
          return response;
        })
        .catch(() => caches.match(request).then((cached) =>
          cached || caches.match('/static/offline.html')
        ))
    );
    return;
  }

  // 静态资源：stale-while-revalidate，优先缓存秒开并后台更新
  event.respondWith(
    caches.match(request).then((cached) => {
      const networkFetch = fetch(request)
        .then((response) => {
          if (response && response.status === 200) {
            const copy = response.clone();
            caches.open(CACHE_NAME).then((cache) => cache.put(request, copy));
          }
          return response;
        })
        .catch(() => cached);
      return cached || networkFetch;
    })
  );
});
