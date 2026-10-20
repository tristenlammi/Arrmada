/// <reference types="node" />
import { readFileSync } from "node:fs";
import { runInNewContext } from "node:vm";
import { describe, expect, it } from "vitest";

// The service worker (public/sw.js) runs in a worker, not the app, so it's loaded here
// into a sandbox with a stand-in `self`: its push and click handlers and the link checks
// are exercised as the browser would call them.

const source = readFileSync(new URL("../public/sw.js", import.meta.url), "utf8");

type Handler = (e: unknown) => void;
interface Shown { title: string; opts: { image?: string; data: { url: string; watch_url: string }; actions?: { action: string; title: string }[] } }

function loadWorker() {
  const handlers: Record<string, Handler> = {};
  const shown: Shown[] = [];
  const opened: string[] = [];
  const sandbox: Record<string, unknown> = {
    URL,
    self: {
      addEventListener: (type: string, fn: Handler) => { handlers[type] = fn; },
      registration: { showNotification: (title: string, opts: Shown["opts"]) => { shown.push({ title, opts }); return Promise.resolve(); } },
      location: { origin: "https://arrmada.example.com" },
    },
    clients: {
      openWindow: (u: string) => { opened.push(u); return Promise.resolve(null); },
      matchAll: () => Promise.resolve([]),
    },
    caches: {},
  };
  runInNewContext(source, sandbox);
  const fns = sandbox as unknown as { watchURL: (u: unknown) => string; plexURL: (u: unknown) => string };
  const push = (payload: unknown) => handlers.push({ data: { json: () => payload }, waitUntil: () => {} });
  const click = (data: unknown, action = "") => handlers.notificationclick({ action, notification: { data, close: () => {} }, waitUntil: () => {} });
  return { ...fns, push, click, shown, opened };
}

describe("service worker Watch links", () => {
  it("accepts any http(s) watch address and nothing else", () => {
    const sw = loadWorker();
    expect(sw.watchURL("https://watch.example.com/web/#/details?id=abc&serverId=def")).toBe("https://watch.example.com/web/#/details?id=abc&serverId=def");
    expect(sw.watchURL("http://192.168.1.5:8096/web/#/details?id=abc")).toBe("http://192.168.1.5:8096/web/#/details?id=abc");
    for (const bad of ["javascript:alert(1)", "data:text/html,hi", "/discover", "//evil.example.com/x", "", 42, null, undefined]) {
      expect(sw.watchURL(bad)).toBe("");
    }
    // plex_url keeps its stricter check: only app.plex.tv pages.
    expect(sw.plexURL("https://app.plex.tv/desktop/#!/server/m/details")).toBe("https://app.plex.tv/desktop/#!/server/m/details");
    expect(sw.plexURL("https://watch.example.com/web/")).toBe("");
  });

  it("offers Watch on the reader's server and opens it on a tap", () => {
    const sw = loadWorker();
    const jf = "https://watch.example.com/web/#/details?id=abc&serverId=def";
    sw.push({ title: "Your request is ready", body: "“Dune” is ready to watch on Jellyfin.", url: "/discover/movie/438631", plex_url: "https://app.plex.tv/desktop/#!/x", watch_url: jf, watch_label: "Jellyfin" });
    const n = sw.shown[0];
    expect(n.opts.actions?.map((a) => a.title)).toEqual(["Watch on Jellyfin", "Details"]);
    expect(n.opts.data.watch_url).toBe(jf);
    sw.click(n.opts.data);
    expect(sw.opened).toEqual([jf]);
  });

  it("shows a ready title's poster as the image, only from TMDB, and keeps the tap target", () => {
    const sw = loadWorker();
    const poster = "https://image.tmdb.org/t/p/w500/abc.jpg";
    sw.push({ title: "Your request is ready", body: "b", url: "/discover/movie/7", image: poster });
    expect(sw.shown[0].opts.image).toBe(poster);
    expect(sw.shown[0].opts.data.url).toBe("/discover/movie/7");
    for (const bad of ["http://image.tmdb.org/x.jpg", "https://evil.example.com/x.jpg", "https://192.168.1.5/x.jpg", "javascript:alert(1)", 42]) {
      sw.push({ title: "t", body: String(bad), url: "/discover", image: bad });
    }
    expect(sw.shown.slice(1).every((n) => n.opts.image === undefined)).toBe(true);
    // An older server sends no image at all.
    sw.push({ title: "Old", body: "old", url: "/discover" });
    expect(sw.shown[sw.shown.length - 1].opts.image).toBeUndefined();
  });

  it("falls back to plex_url from an older server, and never opens an unsafe link", () => {
    const sw = loadWorker();
    sw.push({ title: "Ready", body: "b", url: "/discover/movie/1", plex_url: "https://app.plex.tv/desktop/#!/p1" });
    expect(sw.shown[0].opts.actions?.[0].title).toBe("Watch on Plex");
    sw.push({ title: "Ready", body: "c", url: "/discover/movie/2", watch_url: "javascript:alert(1)", watch_label: "Jellyfin", plex_url: "https://evil.example.com/" });
    expect(sw.shown[1].opts.actions).toBeUndefined();
    expect(sw.shown[1].opts.data.watch_url).toBe("");
    // A notice the old worker showed carries plex_url in its data.
    sw.click({ url: "/discover/movie/1", plex_url: "https://app.plex.tv/desktop/#!/p1" });
    expect(sw.opened).toEqual(["https://app.plex.tv/desktop/#!/p1"]);
  });
});
