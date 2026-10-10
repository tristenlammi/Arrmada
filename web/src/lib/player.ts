import { useSyncExternalStore } from "react";
import { api, ApiError, SIGNED_OUT_EVENT, type AudioChapter, type AudioSyncReport, type AudioSyncResult, type AudioTrack } from "./api";
import { audioEl, playerUser, unlockAudio } from "./playerStub";
import {
  bookLength, chapterAt, chapterEndAt, clampRate, deviceName, holdFrom, locate, nextSectionStart, prevSectionStart, sections,
  type HoldKind,
} from "./playerMath";

// The built-in audiobook player (a lazy chunk: lib/playerStub.tsx loads it on the first
// Play). One per browser tab, living outside React so the sound carries on across pages:
// pages read it with usePlayer() and drive it with the exported functions.
//
// It plays through Arrmada's own listening API under exactly the place guards the apps
// get (internal/httpapi/audioplayer.go has the contract): open a session with play, sync
// about every 15 s while playing, close on pause, on page hide and at the end. A sync
// says where the place is; a big jump the guards hold comes back as held_position, and
// the player says so instead of pretending it was saved.
//
// Privacy: the book's title only ever goes to this person's own screen, lock screen and
// this browser's storage. What reaches the server is the item key in the person's own
// /me/audio URLs, positions and seconds listened; the device is named by kind only
// ("iPhone"), and the server logs route patterns, never the key.

export interface PlayerMeta { title: string; author?: string; series?: string; cover?: string }
export interface OpenOptions {
  /** Start here (seconds into the book) instead of at the saved place. */
  at?: number;
  /** Start playing (the default). false loads the book paused. */
  autoplay?: boolean;
  /** What's already on screen about the book, so the mini-player has a title at once. */
  meta?: PlayerMeta;
}
export type Sleep =
  | { kind: "time"; minutes: number; endsAt: number } // endsAt: performance.now() ms
  | { kind: "chapter"; endsAt: number }; // endsAt: book seconds

export interface PlayerState {
  key: string | null;
  meta: PlayerMeta | null;
  tracks: AudioTrack[];
  chapters: AudioChapter[];
  duration: number;
  /** Where the player is, in seconds into the whole book. */
  position: number;
  /** The person wants it playing. */
  playing: boolean;
  /** Waiting for the server or for audio to arrive. */
  loading: boolean;
  rate: number;
  /** The spot the player is at is held by the place guards, not saved yet. */
  hold: { position: number; kind: HoldKind } | null;
  sleep: Sleep | null;
  /** A passing note ("Skipped part 3 — it couldn't be played"). */
  note: string | null;
  /** Playback stopped and why ("Signed out", "Audiobooks are switched off"). */
  error: string | null;
  /** After a reload: the book left part-way, offered paused ("Resume … at 1:23:45"). */
  resume: { key: string; meta: PlayerMeta; position: number } | null;
}

const RATE_KEY = "arrmada.player.rate";
const DEVICE_KEY = "arrmada.player.device";
const LAST_KEY = "arrmada.player.last";
const SYNC_MS = 15_000;
// Paused this long, a book picks up a place another device saved meanwhile.
const STALE_MS = 10 * 60_000;
const FADE_S = 10;

const store = {
  get: (k: string): string | null => { try { return localStorage.getItem(k); } catch { return null; } },
  set: (k: string, v: string) => { try { localStorage.setItem(k, v); } catch { /* storage blocked: nothing remembered */ } },
  del: (k: string) => { try { localStorage.removeItem(k); } catch { /* ignore */ } },
};

const initial: PlayerState = {
  key: null, meta: null, tracks: [], chapters: [], duration: 0, position: 0, playing: false, loading: false,
  rate: clampRate(Number(store.get(RATE_KEY) ?? 1)), hold: null, sleep: null, note: null, error: null, resume: null,
};
let state: PlayerState = initial;
const listeners = new Set<() => void>();
function set(patch: Partial<PlayerState>) {
  state = { ...state, ...patch };
  listeners.forEach((f) => f());
}
export function getPlayer(): PlayerState { return state; }
export function subscribePlayer(f: () => void): () => void { listeners.add(f); return () => { listeners.delete(f); }; }
/** usePlayer re-renders on every player change (about four times a second while playing). */
export function usePlayer(): PlayerState { return useSyncExternalStore(subscribePlayer, getPlayer); }

// --- session bookkeeping ---------------------------------------------------------

const a = audioEl();
let sid: string | null = null;
let openSeq = 0;
// The person wants sound. Kept apart from a.paused, which also flips while a file loads.
let intent = false;
// The file the element is meant to be playing (absolute URL); anything else on it (the
// unlocking silence) is ignored by every handler.
let loadedUrl = "";
let trackIdx = 0;
// A seek waiting for the file's metadata: where to go within the file once it can.
let pendingOffset: number | null = null;
// Seconds actually played since the last report the server took; kept across failures.
let listened = 0;
let playingSince: number | null = null;
let pausedAt = 0;
let savedPosition: number | null = null;
let syncTimer: ReturnType<typeof setInterval> | undefined;
let seekTimer: ReturnType<typeof setTimeout> | undefined;
let sleepTimer: ReturnType<typeof setInterval> | undefined;
let lastPositionState = 0;
let retried = -1; // the file index a failed load was already re-fetched for
let chain: Promise<unknown> = Promise.resolve();

function deviceId(): string {
  let id = store.get(DEVICE_KEY);
  if (!id) {
    // randomUUID needs a secure context; a LAN http address isn't one.
    const b = new Uint8Array(16);
    crypto.getRandomValues(b);
    id = "web-" + Array.from(b, (x) => x.toString(16).padStart(2, "0")).join("");
    store.set(DEVICE_KEY, id);
  }
  return id;
}

function deviceLabel(): string {
  return deviceName(navigator.userAgent, navigator.maxTouchPoints > 1);
}

function abs(u: string): string {
  try { return new URL(u, window.location.href).href; } catch { return u; }
}

/** Where the player is now, in the whole book. A seek still waiting on a file counts as done. */
function here(): number {
  const tr = state.tracks[trackIdx];
  if (!tr) return state.position;
  const off = pendingOffset ?? (a.src === loadedUrl ? a.currentTime : 0);
  return Math.min(tr.start_offset + (Number.isFinite(off) ? off : 0), state.duration || Infinity);
}

function account() {
  if (playingSince !== null) {
    const now = performance.now();
    listened += (now - playingSince) / 1000;
    playingSince = now;
  }
}

function report(): AudioSyncReport {
  account();
  const r: AudioSyncReport = { time_listened: Math.round(listened * 10) / 10 };
  if (state.tracks.length > 0) r.current_time = Math.round(here() * 10) / 10;
  if (state.duration > 0) r.duration = state.duration;
  return r;
}

function rememberLast() {
  if (!state.key || !state.meta || !playerUser()) return;
  store.set(LAST_KEY, JSON.stringify({ u: playerUser(), key: state.key, meta: state.meta, position: here() }));
}

// sync reports to the server: the session, position and listening as they are right
// now, sent one at a time and in order. The listening is taken off the count when it's
// sent and put back if the report fails, so the next report carries it and nothing is
// counted twice.
function sync(close = false): Promise<unknown> {
  const id = sid;
  if (!id || !state.key) return chain;
  const r = report();
  listened = Math.max(0, listened - r.time_listened);
  rememberLast();
  chain = chain.then(async () => {
    try {
      const res = close ? await api.audioClose(id, r) : await api.audioSync(id, r);
      if (id === sid) applyReply(res);
    } catch (e) {
      if (id === sid) listened += r.time_listened;
      failed(e);
    }
  });
  return chain;
}

function applyReply(res: AudioSyncResult) {
  savedPosition = res.position;
  const patch: Partial<PlayerState> = { hold: holdFrom(res) };
  if (!state.duration && res.duration > 0) patch.duration = res.duration;
  set(patch);
}

function failed(e: unknown) {
  if (!(e instanceof ApiError)) return; // offline: the next tick or 'online' retries
  if (e.status === 401) stop("Signed out — sign in again to keep listening.");
  else if (e.status === 403) stop(e.message || "Audiobooks aren't available to your account.");
  else if (e.status === 404) sid = null; // the session's gone: the next play opens a new one
}

function startTicking() {
  if (syncTimer === undefined) syncTimer = setInterval(() => { if (intent) void sync(); }, SYNC_MS);
}
function stopTicking() {
  if (syncTimer !== undefined) clearInterval(syncTimer);
  syncTimer = undefined;
}

// --- loading files ---------------------------------------------------------------

function applyRate() {
  a.defaultPlaybackRate = state.rate;
  a.playbackRate = state.rate;
  (a as HTMLAudioElement & { preservesPitch?: boolean; webkitPreservesPitch?: boolean }).preservesPitch = true;
  (a as HTMLAudioElement & { webkitPreservesPitch?: boolean }).webkitPreservesPitch = true;
}

// loadTrack puts file idx on the element and goes to offset in it once it can.
function loadTrack(idx: number, offset: number, play: boolean) {
  const tr = state.tracks[idx];
  if (!tr) return;
  trackIdx = idx;
  const url = abs(tr.url);
  if (a.src === url && loadedUrl === url) {
    if (a.readyState >= 1) {
      pendingOffset = null;
      a.currentTime = offset;
    } else {
      pendingOffset = offset; // still loading: go there when it can
    }
    if (play) playElement();
    return;
  }
  loadedUrl = url;
  pendingOffset = offset;
  a.src = url;
  applyRate();
  // iPhones fetch nothing until play() is called, so a playing book starts loading with
  // play() (nothing is heard before the metadata arrives, and the seek happens then). A
  // paused one just loads.
  if (play) playElement();
  else a.load();
}

// mine: the element is on the file the player put there (not the unlocking silence, not
// emptied by close).
const mine = () => loadedUrl !== "" && a.src === loadedUrl;

function playElement() {
  const p = a.play();
  p?.catch((e: DOMException) => {
    if (e?.name === "NotAllowedError") {
      // The browser wants a tap first (a reload, a resume from the lock screen on an
      // older iPhone): show it paused with the play button ready.
      intent = false;
      set({ playing: false, loading: false });
    }
    // AbortError: a newer load replaced this one.
  });
}

a.addEventListener("loadedmetadata", () => {
  if (!mine()) return;
  applyRate();
  if (pendingOffset !== null) {
    const off = Math.min(pendingOffset, Number.isFinite(a.duration) ? a.duration : pendingOffset);
    pendingOffset = null;
    if (off > 0) a.currentTime = off;
  }
});

a.addEventListener("playing", () => {
  if (!mine()) return;
  playingSince = performance.now();
  retried = -1;
  set({ loading: false, playing: true, note: state.note, error: null });
  try { channel?.postMessage({ type: "playing", tab: TAB }); } catch { /* closed channel */ }
  updateSession(true);
});
const stopAccounting = () => { account(); playingSince = null; };
a.addEventListener("waiting", () => { stopAccounting(); if (intent) set({ loading: true }); });
a.addEventListener("pause", () => {
  stopAccounting();
  // The end of a file pauses before 'ended', and loading a new file can pause too:
  // only a pause from outside (a call coming in, headphones out) is the person's.
  if (!mine() || a.ended || pendingOffset !== null || !intent) return;
  userPause();
});
a.addEventListener("timeupdate", () => {
  if (!mine() || pendingOffset !== null) return;
  const pos = here();
  set({ position: pos });
  const now = performance.now();
  if (now - lastPositionState > 1000) {
    lastPositionState = now;
    updatePositionState();
  }
  checkSleep();
});
a.addEventListener("ended", () => {
  if (!mine()) return;
  stopAccounting();
  if (trackIdx + 1 < state.tracks.length) {
    loadTrack(trackIdx + 1, 0, intent);
    void sync();
    return;
  }
  // The end of the book: close the session there, and nothing left to resume.
  intent = false;
  set({ playing: false, position: state.duration, sleep: null });
  clearSleep();
  stopTicking();
  void sync(true);
  store.del(LAST_KEY);
  updateSession(false);
});
a.addEventListener("error", () => {
  if (!mine() || !state.key) return;
  const idx = trackIdx;
  const key = state.key;
  if (retried !== idx) {
    // Maybe the files changed (a re-import merged them): read the book again and carry
    // on at the same moment of it.
    retried = idx;
    const pos = here();
    api.audioItem(key).then((d) => {
      if (state.key !== key) return;
      const same = d.tracks.length === state.tracks.length && d.tracks.every((t, i) => t.url === state.tracks[i].url);
      if (same) { skipBroken(idx); return; }
      set({ tracks: d.tracks, chapters: d.chapters, duration: d.duration || bookLength(d.tracks) });
      const { idx: i, offset } = locate(d.tracks, pos);
      loadTrack(i, offset, intent);
    }).catch(failed);
    return;
  }
  skipBroken(idx);
});

function skipBroken(idx: number) {
  if (idx + 1 < state.tracks.length) {
    set({ note: `Skipped part ${idx + 1} — it couldn't be played.` });
    loadTrack(idx + 1, 0, intent);
  } else {
    intent = false;
    set({ playing: false, loading: false, note: null, error: "This audiobook couldn't be played." });
  }
}

// --- public controls ---------------------------------------------------------------

/**
 * open starts a book: the saved place (or opts.at), playing unless autoplay is false.
 * Called from a tap (through playAudiobook), so the element is unlocked before any await.
 */
export function open(key: string, opts: OpenOptions = {}): void {
  const autoplay = opts.autoplay ?? true;
  if (state.key === key && sid) {
    if (opts.at !== undefined) seek(opts.at);
    if (autoplay && !intent) resume();
    return;
  }
  // Another book was playing: close its session where it got to.
  if (state.key && sid) {
    a.pause();
    void sync(true);
  }
  const seq = ++openSeq;
  sid = null;
  savedPosition = null;
  listened = 0;
  playingSince = null;
  loadedUrl = "";
  pendingOffset = null;
  retried = -1;
  intent = autoplay;
  stopTicking();
  if (autoplay) unlockAudio();
  const known = opts.meta ?? (state.resume?.key === key ? state.resume.meta : null);
  set({
    key, meta: known, tracks: [], chapters: [], duration: 0, position: opts.at ?? 0, playing: autoplay, loading: true,
    hold: null, note: null, error: null, resume: null, sleep: null,
  });
  clearSleep();
  void (async () => {
    try {
      const [start, detail] = await Promise.all([
        api.audioPlay(key, { device_id: deviceId(), device_name: deviceLabel() }),
        api.audioItem(key),
      ]);
      if (seq !== openSeq) {
        // Superseded while loading: close the session this opened, unused.
        void api.audioClose(start.session_id, { time_listened: 0 }).catch(() => {});
        return;
      }
      sid = start.session_id;
      savedPosition = start.start_time;
      const tracks = start.tracks.length ? start.tracks : detail.tracks;
      if (tracks.length === 0) {
        intent = false;
        set({ loading: false, playing: false, error: "This audiobook has no playable files yet." });
        return;
      }
      const duration = start.duration || detail.duration || bookLength(tracks);
      const meta: PlayerMeta = { title: detail.title, author: detail.author, series: detail.series ? `${detail.series}${detail.series_seq ? ` #${detail.series_seq}` : ""}` : undefined, cover: detail.cover };
      const at = Math.min(Math.max(0, opts.at ?? start.start_time), duration);
      set({
        meta, tracks, chapters: start.chapters.length ? start.chapters : detail.chapters, duration, position: at,
        hold: start.restart ? { position: 0, kind: "again" } : null,
      });
      setMediaMetadata();
      const { idx, offset } = locate(tracks, at);
      loadTrack(idx, offset, intent);
      if (intent) startTicking();
      else set({ loading: false });
      rememberLast();
      // Starting somewhere other than the saved place (a chapter, a bookmark): report it
      // now, so a jump the guards hold says so straight away.
      if (opts.at !== undefined && Math.abs(at - start.start_time) > 1) void sync(!intent);
    } catch (e) {
      if (seq !== openSeq) return;
      intent = false;
      const msg = e instanceof ApiError && e.status === 401 ? "Signed out — sign in again to keep listening." : (e as Error).message || "Couldn't start the audiobook.";
      set({ loading: false, playing: false, error: msg });
    }
  })();
}

/** resume plays from where the player is. Called from a tap or the lock screen. */
export function resume(): void {
  if (!state.key && state.resume) {
    open(state.resume.key, { meta: state.resume.meta });
    return;
  }
  if (!state.key) return;
  if (!sid) {
    // The session was lost (stopped by an error, forgotten by the server): start a new
    // one at the saved place.
    open(state.key, { meta: state.meta ?? undefined });
    return;
  }
  if (state.tracks.length === 0) return;
  intent = true;
  set({ playing: true, error: null });
  const stale = pausedAt > 0 && performance.now() - pausedAt > STALE_MS;
  if (mine()) {
    playElement();
  } else {
    const { idx, offset } = locate(state.tracks, state.position);
    loadTrack(idx, offset, true);
  }
  startTicking();
  if (stale) void pickUpNewerPlace();
}

// pickUpNewerPlace: after a long pause, another device may have moved the place on. If
// the saved place is no longer the one this player last saw, go there.
async function pickUpNewerPlace() {
  const key = state.key;
  if (!key) return;
  try {
    const d = await api.audioItem(key);
    const p = d.progress;
    if (state.key !== key || !p || p.finished || p.pending_position != null || savedPosition === null) return;
    if (Math.abs(p.position - savedPosition) > 5 && Math.abs(p.position - here()) > 5) {
      savedPosition = p.position;
      seek(p.position, false);
      set({ note: p.device ? `Picked up where you left off on ${p.device}.` : "Picked up where you left off." });
    }
  } catch (e) { failed(e); }
}

function userPause() {
  intent = false;
  pausedAt = performance.now();
  a.pause();
  stopAccounting();
  stopTicking();
  set({ playing: false, loading: false });
  updateSession(false);
  void sync(true);
}

/** pause stops playing and saves the place (closing the session). */
export function pause(): void {
  if (!intent) return;
  userPause();
}

export function toggle(): void {
  if (intent) pause();
  else resume();
}

/**
 * seek goes to a moment of the book. Reported once the person stops scrubbing, so a jump
 * the guards hold is noticed straight away.
 */
export function seek(t: number, report = true): void {
  if (state.tracks.length === 0) return;
  const to = Math.min(Math.max(0, t), state.duration || bookLength(state.tracks));
  account(); // what played before the jump counts before it
  const { idx, offset } = locate(state.tracks, to);
  loadTrack(idx, offset, intent);
  set({ position: to });
  updatePositionState();
  if (report) {
    clearTimeout(seekTimer);
    seekTimer = setTimeout(() => { void sync(!intent); }, 1500);
  }
  // A chapter timer follows the chapter you're now in.
  if (state.sleep?.kind === "chapter") set({ sleep: { kind: "chapter", endsAt: chapterEndAt(state.chapters, to, state.duration) } });
}

export function skip(seconds: number): void { seek(here() + seconds); }
export function prevChapter(): void { seek(prevSectionStart(sections(state.chapters, state.tracks), here())); }
export function nextChapter(): void {
  const to = nextSectionStart(sections(state.chapters, state.tracks), here());
  if (to !== null) seek(to);
}

export function setRate(r: number): void {
  const rate = clampRate(r);
  store.set(RATE_KEY, String(rate));
  set({ rate });
  applyRate();
  updatePositionState();
}

/** keepHeld confirms a held jump now ("Keep it now") instead of after listening on. */
export async function keepHeld(): Promise<void> {
  if (!state.key || !state.hold) return;
  const r = await api.acceptAudioJump(state.key);
  savedPosition = r.position;
  set({ hold: null });
}

/** close stops and puts the player away (the mini-player's ×). */
export function close(): void {
  if (intent) userPause();
  else if (sid && state.key) void sync(true);
  openSeq++;
  sid = null;
  loadedUrl = "";
  a.removeAttribute("src");
  a.load();
  clearSleep();
  store.del(LAST_KEY);
  set({ ...initial, rate: state.rate });
  clearMediaSession();
}

// stop is close without forgetting the book: signed out or switched off.
function stop(error: string | null) {
  intent = false;
  a.pause();
  stopAccounting();
  stopTicking();
  clearSleep();
  sid = null;
  set({ playing: false, loading: false, error });
  updateSession(false);
}

export function dismissNote(): void { set({ note: null }); }

/** setUser is the signed-in person (0 signed out); it stops the player when they change. */
let currentUser = 0;
export function setUser(id: number): void {
  if (id === currentUser) return;
  const was = currentUser;
  currentUser = id;
  if (was && (state.key || state.resume)) {
    if (sid && state.key) sendClose();
    stop(null);
    openSeq++;
    set({ ...initial, rate: state.rate });
    clearMediaSession();
  }
  if (id && !state.key) {
    // Back with a book left part-way in this browser: offer it, paused.
    try {
      const last = JSON.parse(store.get(LAST_KEY) ?? "null") as { u?: number; key?: string; meta?: PlayerMeta; position?: number } | null;
      if (last && last.u === id && last.key && last.meta?.title) {
        set({ resume: { key: last.key, meta: last.meta, position: Number(last.position) || 0 } });
      }
    } catch { /* unreadable: nothing to offer */ }
  }
}

// --- sleep timer -----------------------------------------------------------------

/** sleepIn pauses after `minutes`, or at the end of this chapter (null cancels). */
export function setSleep(choice: number | "chapter" | null): void {
  clearSleep();
  if (choice === null) { set({ sleep: null }); return; }
  if (choice === "chapter") set({ sleep: { kind: "chapter", endsAt: chapterEndAt(state.chapters, here(), state.duration) } });
  else set({ sleep: { kind: "time", minutes: choice, endsAt: performance.now() + choice * 60_000 } });
  // Timeupdates stop when the element does, and a hidden tab throttles timers; checking
  // from both keeps the fade and the deadline close enough.
  sleepTimer = setInterval(checkSleep, 1000);
}

function clearSleep() {
  if (sleepTimer !== undefined) clearInterval(sleepTimer);
  sleepTimer = undefined;
  a.volume = 1;
}

/** sleepLeft is the seconds left on the sleep timer, in wall time. */
export function sleepLeft(s: PlayerState = state): number | null {
  if (!s.sleep) return null;
  if (s.sleep.kind === "time") return Math.max(0, (s.sleep.endsAt - performance.now()) / 1000);
  return Math.max(0, (s.sleep.endsAt - s.position) / (s.rate || 1));
}

function checkSleep() {
  if (!state.sleep || !intent) return;
  const left = sleepLeft({ ...state, position: here() });
  if (left === null) return;
  if (left <= 0) {
    clearSleep();
    set({ sleep: null });
    userPause();
    return;
  }
  // Fade out over the last few seconds. Some iPhones ignore volume; they just pause.
  try { a.volume = Math.max(0, Math.min(1, left / FADE_S)); } catch { /* read-only volume */ }
}

// --- lock screen (Media Session) ------------------------------------------------------

function setMediaMetadata() {
  const ms = typeof navigator !== "undefined" ? navigator.mediaSession : undefined;
  if (!ms || typeof MediaMetadata === "undefined" || !state.meta) return;
  const art = state.meta.cover ? ["96x96", "256x256", "512x512"].map((sizes) => ({ src: abs(state.meta!.cover!), sizes })) : [];
  try {
    ms.metadata = new MediaMetadata({ title: state.meta.title, artist: state.meta.author ?? "", album: state.meta.series ?? "", artwork: art });
  } catch { /* an old browser's MediaMetadata */ }
}

function updateSession(playing: boolean) {
  const ms = navigator.mediaSession;
  if (!ms) return;
  try { ms.playbackState = playing ? "playing" : "paused"; } catch { /* ignore */ }
  updatePositionState();
}

function updatePositionState() {
  const ms = navigator.mediaSession;
  if (!ms?.setPositionState || !(state.duration > 0)) return;
  try {
    ms.setPositionState({ duration: state.duration, playbackRate: state.rate, position: Math.min(here(), state.duration) });
  } catch { /* a position the browser won't take */ }
}

function clearMediaSession() {
  const ms = navigator.mediaSession;
  if (!ms) return;
  try { ms.metadata = null; ms.playbackState = "none"; } catch { /* ignore */ }
}

if (typeof navigator !== "undefined" && navigator.mediaSession) {
  const handle = (action: MediaSessionAction, fn: MediaSessionActionHandler) => {
    try { navigator.mediaSession.setActionHandler(action, fn); } catch { /* not offered by this browser */ }
  };
  handle("play", () => resume());
  handle("pause", () => pause());
  handle("stop", () => pause());
  handle("seekbackward", (d) => skip(-(d.seekOffset || 30)));
  handle("seekforward", (d) => skip(d.seekOffset || 30));
  handle("previoustrack", () => prevChapter());
  handle("nexttrack", () => nextChapter());
  handle("seekto", (d) => { if (typeof d.seekTime === "number") seek(d.seekTime); });
}

// --- one player per browser, and leaving the page ------------------------------------

const TAB = Math.random().toString(36).slice(2);
let channel: BroadcastChannel | null = null;
try {
  channel = new BroadcastChannel("arrmada-player");
  // Another tab started playing: this one steps aside. The message carries no book.
  channel.onmessage = (e: MessageEvent<{ type?: string; tab?: string }>) => {
    if (e.data?.type === "playing" && e.data.tab !== TAB && intent) pause();
  };
} catch { /* no BroadcastChannel (old Safari): two tabs can both play */ }

// sendClose closes the session as the page goes away: a beacon survives the unload where
// a fetch might not; keepalive fetch where beacons aren't available.
function sendClose() {
  const id = sid;
  if (!id) return;
  const r = report();
  let sent = false;
  try { sent = api.audioCloseBeacon(id, r); } catch { /* no beacons */ }
  if (!sent) {
    try {
      void fetch(`/api/v1/me/audio/sessions/${encodeURIComponent(id)}/close`, {
        method: "POST", keepalive: true, headers: { "Content-Type": "application/json" }, body: JSON.stringify(r),
      }).catch(() => {});
    } catch { /* nothing more to try */ }
  }
  listened = 0;
  rememberLast();
}

window.addEventListener("pagehide", sendClose);
// Hidden while playing (the screen locked, another app on top): the sound carries on,
// so it's a sync, not a close. A hidden page may not get another chance for a while.
document.addEventListener("visibilitychange", () => {
  if (document.visibilityState === "hidden" && intent && sid) void sync();
});
window.addEventListener("online", () => { if (sid && intent) void sync(); });
window.addEventListener(SIGNED_OUT_EVENT, () => { if (state.key) stop("Signed out — sign in again to keep listening."); });

/** Keyboard shortcuts for a desktop: Space, ←/→ 30 s, Shift+←/→ chapters. */
export function playerKeys(e: KeyboardEvent): void {
  if (!state.key || e.defaultPrevented || e.metaKey || e.ctrlKey || e.altKey) return;
  const t = e.target as HTMLElement | null;
  if (t && (t.isContentEditable || /^(INPUT|TEXTAREA|SELECT|BUTTON|A)$/.test(t.tagName) || t.closest("[role=slider]"))) return;
  if (e.key === " ") { e.preventDefault(); toggle(); }
  else if (e.key === "ArrowLeft") { e.preventDefault(); if (e.shiftKey) prevChapter(); else skip(-30); }
  else if (e.key === "ArrowRight") { e.preventDefault(); if (e.shiftKey) nextChapter(); else skip(30); }
}

export { chapterAt };
