// Arrmada service worker — enables PWA install + a resilient app shell.
// Strategy: network-first for navigations (so the SPA always gets fresh HTML when
// online, and the cached shell when offline); cache-first only for the hashed build
// assets under /assets/; network-first for everything else.
//
// The build stamps __BUILD__ (web/scripts/compress.mjs), so every deploy ships a
// byte-different sw.js: the browser installs it, and activate drops the previous
// build's cache instead of old bundles piling up forever.
const CACHE = "arrmada-__BUILD__";
const SHELL = ["/", "/index.html", "/icon.svg", "/manifest.webmanifest"];

self.addEventListener("install", (e) => {
  e.waitUntil(caches.open(CACHE).then((c) => c.addAll(SHELL)).then(() => self.skipWaiting()));
});

self.addEventListener("activate", (e) => {
  e.waitUntil(
    caches.keys()
      .then((keys) => Promise.all(keys.filter((k) => k.startsWith("arrmada-") && k !== CACHE).map((k) => caches.delete(k))))
      .then(() => self.clients.claim())
  );
});

// Only real build output is worth keeping: a missing chunk must never get an HTML
// page (or an error) cached under a .js URL.
const CACHEABLE = /javascript|css|^image\/|font/;
function cacheableAsset(res) {
  return res.ok && CACHEABLE.test(res.headers.get("content-type") || "");
}

self.addEventListener("fetch", (e) => {
  const req = e.request;
  if (req.method !== "GET") return;
  const url = new URL(req.url);
  if (url.origin !== self.location.origin) return;
  // Never cache the API — always hit the network for live data.
  if (url.pathname.startsWith("/api/")) return;

  // SPA navigations: network-first, fall back to the cached shell when offline.
  if (req.mode === "navigate") {
    e.respondWith(
      fetch(req).catch(() => caches.match("/index.html").then((r) => r || caches.match("/")))
    );
    return;
  }

  // Hashed build assets never change under a name: cache-first, then network
  // (keeping the result only if it really is JS, CSS, an image or a font).
  if (url.pathname.startsWith("/assets/")) {
    e.respondWith(
      caches.match(req).then((hit) =>
        hit ||
        fetch(req).then((res) => {
          if (cacheableAsset(res)) {
            const copy = res.clone();
            caches.open(CACHE).then((c) => c.put(req, copy));
          }
          return res;
        })
      )
    );
    return;
  }

  // Everything else (icon, manifest, …): network-first, the shell copy offline.
  e.respondWith(fetch(req).catch(() => caches.match(req).then((r) => r || Response.error())));
});

// --- Web Push -------------------------------------------------------------
// The server sends an encrypted JSON payload: { title, body, url }.
self.addEventListener("push", (e) => {
  let data = { title: "Arrmada", body: "", url: "/discover" };
  try { data = { ...data, ...e.data.json() }; } catch { /* body may be empty */ }
  e.waitUntil(
    self.registration.showNotification(data.title, {
      body: data.body,
      icon: "/icon.svg",
      badge: "/icon.svg",
      data: { url: data.url },
      tag: data.body || data.title, // collapse duplicate pings for the same item
    })
  );
});

// Tapping the notification focuses an open Arrmada tab (navigating it), or opens one.
self.addEventListener("notificationclick", (e) => {
  e.notification.close();
  const url = (e.notification.data && e.notification.data.url) || "/discover";
  e.waitUntil(
    clients.matchAll({ type: "window", includeUncontrolled: true }).then((tabs) => {
      for (const tab of tabs) {
        if ("focus" in tab) { tab.navigate(url); return tab.focus(); }
      }
      return clients.openWindow(url);
    })
  );
});
