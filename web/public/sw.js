// Arrmada service worker — enables PWA install + a resilient app shell.
// Strategy: network-first for navigations (so the SPA always gets fresh HTML when
// online, and the cached shell when offline); cache-first only for the hashed build
// assets under /assets/; network-first for everything else.
//
// The build stamps __BUILD__ (web/scripts/compress.mjs), so every deploy ships a
// byte-different sw.js: the browser installs it, and activate drops the previous
// build's cache instead of old bundles piling up forever.
const CACHE = "arrmada-__BUILD__";
// The PNGs are what a notification shows (icon and Android's monochrome badge), so
// they're kept for when a push arrives while the server is out of reach.
const SHELL = ["/", "/index.html", "/icon.svg", "/manifest.webmanifest", "/icon-192.png", "/badge-96.png", "/apple-touch-icon.png"];

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
      // Android won't draw an SVG here, and the badge must be a single-colour PNG
      // (only its alpha is used).
      icon: "/icon-192.png",
      badge: "/badge-96.png",
      data: { url: data.url },
      tag: data.body || data.title, // collapse duplicate pings for the same item
    })
  );
});

// Tapping the notification opens what it's about (the server sends the exact title's
// address): it focuses an open Arrmada tab and takes it there, or opens one. Only a page
// this worker controls can be navigated; for any other (one opened before the worker
// took over) navigate() rejects, and a fresh window opens instead. Only same-origin
// paths are followed.
self.addEventListener("notificationclick", (e) => {
  e.notification.close();
  let url = (e.notification.data && e.notification.data.url) || "/discover";
  if (typeof url !== "string" || !url.startsWith("/") || url.startsWith("//")) url = "/discover";
  e.waitUntil(
    clients.matchAll({ type: "window", includeUncontrolled: true }).then((tabs) => {
      const tab = tabs.find((t) => "focus" in t && "navigate" in t);
      if (!tab) return clients.openWindow(url);
      return tab.navigate(url)
        .then((t) => (t || tab).focus())
        .catch(() => clients.openWindow(url));
    })
  );
});
