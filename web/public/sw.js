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
// The server sends an encrypted JSON payload: { title, body, url, plex_url?, watch_url?,
// watch_label?, image? }. On a 'ready' notice for a title a media server has, watch_url is its page
// on the reader's own server (Plex's app.plex.tv page, or Jellyfin's at the owner's public
// address) and watch_label that server's name; plex_url is Plex's page alone, which a
// server from before Jellyfin sends instead.
self.addEventListener("push", (e) => {
  let data = { title: "Arrmada", body: "", url: "/discover" };
  try { data = { ...data, ...e.data.json() }; } catch { /* body may be empty */ }
  const watch = watchLink(data);
  e.waitUntil(
    self.registration.showNotification(data.title, {
      body: data.body,
      // Android won't draw an SVG here, and the badge must be a single-colour PNG
      // (only its alpha is used).
      icon: "/icon-192.png",
      badge: "/badge-96.png",
      data: { url: data.url, watch_url: watch ? watch.url : "" },
      // A ready movie or show's poster, as the large image where the platform draws one
      // (Android, desktop Chrome; iOS ignores it). Only TMDB's public image host.
      ...(posterURL(data.image) ? { image: posterURL(data.image) } : {}),
      // Where the platform shows actions (Android, desktop Chrome): Watch on <server>, or
      // the request in Arrmada. Elsewhere a plain tap is all there is, and opens the server.
      ...(watch ? { actions: [{ action: "watch", title: "Watch on " + watch.label }, { action: "open", title: "Details" }] } : {}),
      tag: data.body || data.title, // collapse duplicate pings for the same item
    })
  );
});

// plexURL keeps a Watch on Plex link only when it really is an app.plex.tv page.
function plexURL(u) {
  return typeof u === "string" && u.startsWith("https://app.plex.tv/") ? u : "";
}

// posterURL keeps a push's poster only when it is an https image from TMDB's image host.
function posterURL(u) {
  if (typeof u !== "string") return "";
  try {
    const parsed = new URL(u);
    return parsed.protocol === "https:" && parsed.hostname === "image.tmdb.org" ? parsed.href : "";
  } catch {
    return "";
  }
}

// watchURL keeps a Watch link only when it is a plain http(s) address: a Jellyfin server
// lives wherever its owner put it, but nothing else (javascript:, data:, a relative path)
// is ever opened.
function watchURL(u) {
  if (typeof u !== "string") return "";
  try {
    const parsed = new URL(u);
    return parsed.protocol === "https:" || parsed.protocol === "http:" ? parsed.href : "";
  } catch {
    return "";
  }
}

// watchLink is a push's Watch link and the server's name for its action: watch_url when
// it's a usable address, else (an older server) plex_url as Plex's. null when neither.
function watchLink(data) {
  const url = watchURL(data.watch_url);
  if (url) {
    const label = typeof data.watch_label === "string" && data.watch_label.trim() ? data.watch_label.trim().slice(0, 40) : "your server";
    return { url, label };
  }
  const plex = plexURL(data.plex_url);
  return plex ? { url: plex, label: "Plex" } : null;
}

// Tapping the notification opens what it's about (the server sends the exact title's
// address): it focuses an open Arrmada tab and takes it there, or opens one. Only a page
// this worker controls can be navigated; for any other (one opened before the worker
// took over) navigate() rejects, and a fresh window opens instead. Only same-origin
// paths are followed.
//
// A 'ready' notice whose title is on a media server opens it there — from its Watch
// action, or a plain tap (iOS and others show no actions) — and its Details action opens
// it here. (A notice shown by the worker from before Jellyfin carries plex_url.)
self.addEventListener("notificationclick", (e) => {
  e.notification.close();
  const d = e.notification.data || {};
  const watch = watchURL(d.watch_url) || plexURL(d.plex_url);
  if (watch && e.action !== "open") {
    e.waitUntil(clients.openWindow(watch));
    return;
  }
  let url = d.url || "/discover";
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
