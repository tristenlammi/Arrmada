import type { Page, Route } from "@playwright/test";
import type { MockedApi } from "../mockApi";
import type { AudioCard, AudioItemDetail, AudioPlayStart, AudioShelf, AudioSyncResult } from "../../src/lib/api";
import { NOW } from "./clock";

// The listening API for the player specs: a two-file book whose "files" are silent WAVs
// built here (Chromium plays WAV; no codec, no binary fixture in the repo), served with
// Range like the real server, plus shelves, the catalogue, play/sync/close and bookmarks.

/** wav is `seconds` of 8 kHz 8-bit mono silence. */
export function wav(seconds: number): Buffer {
  const n = Math.round(8000 * seconds);
  const b = Buffer.alloc(44 + n, 128);
  b.write("RIFF", 0, "ascii"); b.writeUInt32LE(36 + n, 4); b.write("WAVE", 8, "ascii"); b.write("fmt ", 12, "ascii");
  b.writeUInt32LE(16, 16); b.writeUInt16LE(1, 20); b.writeUInt16LE(1, 22); b.writeUInt32LE(8000, 24); b.writeUInt32LE(8000, 28);
  b.writeUInt16LE(1, 32); b.writeUInt16LE(8, 34); b.write("data", 36, "ascii"); b.writeUInt32LE(n, 40);
  return b;
}

// File 1 is short so a spec can watch playback cross into file 2 in real time.
export const FILE_SECONDS = [3, 600];
const files = FILE_SECONDS.map((s) => wav(s));
const DURATION = FILE_SECONDS[0] + FILE_SECONDS[1];

const card = (key: string, title: string, extra: Partial<AudioCard> = {}): AudioCard => ({
  key, book_id: parseInt(key.slice(1), 10) || 12, title, author: "Matt Dinniman", duration: DURATION, added_at: NOW - 86_400_000,
  cover: `/api/v1/me/audio/items/${key}/cover?v=1`, progress: null, ...extra,
});

export const carl = card("b12", "Dungeon Crawler Carl", {
  series: "Dungeon Crawler Carl", series_seq: "1",
  progress: { item_key: "b12", position: 1, duration: DURATION, finished: false, updated_at: NOW - 3_600_000, device: "Pixel 8" },
});
export const doomsday = card("b13", "Carl's Doomsday Scenario", { series: "Dungeon Crawler Carl", series_seq: "2" });
export const finished = card("b14", "The Gate of the Feral Gods", {
  progress: { item_key: "b14", position: DURATION, duration: DURATION, finished: true, updated_at: NOW - 86_400_000 },
});

export const shelves: AudioShelf[] = [
  { id: "continue-listening", label: "Continue Listening", items: [carl] },
  { id: "continue-series", label: "Continue Series", items: [doomsday] },
  { id: "recently-added", label: "Recently Added", items: [doomsday, carl, finished] },
  { id: "listen-again", label: "Listen Again", items: [finished] },
];

export const tracks = [
  { ino: "1", index: 1, start_offset: 0, duration: FILE_SECONDS[0], mime: "audio/wav", url: "/api/v1/me/audio/items/b12/file/1" },
  { ino: "2", index: 2, start_offset: FILE_SECONDS[0], duration: FILE_SECONDS[1], mime: "audio/wav", url: "/api/v1/me/audio/items/b12/file/2" },
];
export const chapters = [
  { id: 0, start: 0, end: 100, title: "Chapter 1: The Stairwell" },
  { id: 1, start: 100, end: 300, title: "Chapter 2: Mordecai" },
  { id: 2, start: 300, end: DURATION, title: "Chapter 3: The Safe Room" },
];

export function detail(c: AudioCard = carl): AudioItemDetail {
  return {
    ...c, description: "A man, his ex-girlfriend's cat and the end of the world.", year: 2020, genres: ["LitRPG"],
    chapters, tracks: tracks.map((t) => ({ ...t, url: t.url.replace("b12", c.key) })),
    versions: c.key === "b12" ? [{ key: "b12v3", title: "Dungeon Crawler Carl (Full cast)" }] : [],
    bookmarks: [{ item_key: c.key, time: 200, title: "The good bit", created_at: NOW - 60_000 }],
  };
}

export interface AudioMock {
  /** What the next sync or close answers (position defaults to the reported spot). */
  reply: Partial<AudioSyncResult>;
  /** Where play starts. */
  startAt: number;
  restart: boolean;
  /** The status the listening API answers (403: switched off). */
  status: number;
}

/**
 * mockAudio answers /api/v1/me/audio/* (except /api/v1/me/audio itself and its
 * listening page, which the default table has) and records every call in api.calls.
 */
export async function mockAudio(page: Page, api: MockedApi, opts: Partial<AudioMock> = {}): Promise<AudioMock> {
  const m: AudioMock = { reply: {}, startAt: 1, restart: false, status: 200, ...opts };
  await page.route((u) => u.pathname.startsWith("/api/v1/me/audio/"), async (route: Route) => {
    const req = route.request();
    const url = new URL(req.url());
    const path = url.pathname;
    const method = req.method();
    let body: unknown;
    try { body = req.postDataJSON(); } catch { body = req.postData(); }
    const json = (status: number, out: unknown) => route.fulfill({ status, contentType: "application/json", body: JSON.stringify(out) });

    const file = path.match(/\/items\/([^/]+)\/file\/(\d+)$/);
    if (file && method === "GET") {
      api.calls.push({ method, path, body: undefined });
      const data = files[Number(file[2]) - 1];
      if (!data) return json(404, { message: "no such file" });
      const range = req.headers()["range"];
      const r = range?.match(/bytes=(\d+)-(\d*)/);
      if (r) {
        const start = Number(r[1]);
        const end = r[2] ? Math.min(Number(r[2]), data.length - 1) : data.length - 1;
        return route.fulfill({ status: 206, body: data.subarray(start, end + 1), headers: {
          "Content-Type": "audio/wav", "Accept-Ranges": "bytes", "Content-Range": `bytes ${start}-${end}/${data.length}`,
        } });
      }
      return route.fulfill({ status: 200, body: data, headers: { "Content-Type": "audio/wav", "Accept-Ranges": "bytes" } });
    }
    if (path.endsWith("/cover")) {
      return route.fulfill({ status: 200, contentType: "image/png", body: Buffer.from("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mNkYAAAAAYAAjCB0C8AAAAASUVORK5CYII=", "base64") });
    }
    // Anything else under /me/audio/ that this mock doesn't know goes to the default table.
    const known = /\/(shelves|library|accept|items\/[^/]+(\/play|\/bookmarks(\/[\d.]+)?)?|sessions\/[^/]+\/(sync|close))$/.test(path);
    if (!known) return route.fallback();
    api.calls.push({ method, path: path + url.search, body });
    if (m.status !== 200) return json(m.status, { message: m.status === 403 ? "Audiobooks are switched off" : "nope" });

    if (path.endsWith("/shelves")) return json(200, { shelves });
    if (path.endsWith("/accept")) return json(200, { position: m.reply.held_position ?? m.startAt });
    if (path.endsWith("/library")) {
      const all = [carl, doomsday, finished];
      const q = (url.searchParams.get("q") ?? "").toLowerCase();
      const items = all.filter((c) => !q || c.title.toLowerCase().includes(q));
      return json(200, { items, total: items.length, page: 0, limit: 60 });
    }
    const play = path.match(/\/items\/([^/]+)\/play$/);
    if (play) {
      const start: AudioPlayStart = { session_id: `s-${play[1]}`, start_time: m.restart ? 0 : m.startAt, duration: DURATION, tracks: detail().tracks.map((t) => ({ ...t, url: t.url.replace("b12", play[1]) })), chapters, restart: m.restart };
      return json(200, start);
    }
    const bm = path.match(/\/items\/([^/]+)\/bookmarks(?:\/([\d.]+))?$/);
    if (bm) {
      if (method === "DELETE") return route.fulfill({ status: 204 });
      if (method === "POST") {
        const b = body as { time: number; title: string };
        return json(200, { item_key: bm[1], time: b.time, title: b.title, created_at: NOW });
      }
      return json(200, { bookmarks: detail().bookmarks });
    }
    const item = path.match(/\/items\/([^/]+)$/);
    if (item) {
      const c = [carl, doomsday, finished].find((x) => x.key === item[1]) ?? { ...carl, key: item[1] };
      return json(200, detail(c));
    }
    if (/\/sessions\/[^/]+\/(sync|close)$/.test(path)) {
      const b = (body ?? {}) as { current_time?: number };
      const out: AudioSyncResult = { position: b.current_time ?? m.startAt, held_position: null, finished: false, duration: DURATION, ...m.reply };
      return json(200, out);
    }
    return route.fallback();
  });
  return m;
}
