// Arrmada service worker — enables PWA install + a resilient app shell.
// Strategy: network-first for navigations (so the SPA always gets fresh HTML when
// online, and the cached shell when offline); cache-first for hashed build assets.
const CACHE = "arrmada-v1";
const SHELL = ["/", "/index.html", "/icon.svg", "/manifest.webmanifest"];

self.addEventListener("install", (e) => {
  e.waitUntil(caches.open(CACHE).then((c) => c.addAll(SHELL)).then(() => self.skipWaiting()));
});

self.addEventListener("activate", (e) => {
  e.waitUntil(
    caches.keys().then((keys) => Promise.all(keys.filter((k) => k !== CACHE).map((k) => caches.delete(k)))).then(() => self.clients.claim())
  );
});

self.addEventListener("fetch", (e) => {
  const req = e.request;
  if (req.method !== "GET") return;
  const url = new URL(req.url);
  // Never cache the API — always hit the network for live data.
  if (url.pathname.startsWith("/api/")) return;

  // SPA navigations: network-first, fall back to the cached shell when offline.
  if (req.mode === "navigate") {
    e.respondWith(
      fetch(req).catch(() => caches.match("/index.html").then((r) => r || caches.match("/")))
    );
    return;
  }

  // Static assets: cache-first, then network (and cache the result).
  e.respondWith(
    caches.match(req).then((hit) =>
      hit ||
      fetch(req).then((res) => {
        if (res.ok && (url.origin === self.location.origin)) {
          const copy = res.clone();
          caches.open(CACHE).then((c) => c.put(req, copy));
        }
        return res;
      }).catch(() => hit)
    )
  );
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
