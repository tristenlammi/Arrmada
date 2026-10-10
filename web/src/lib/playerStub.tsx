import { Suspense, useEffect, useSyncExternalStore } from "react";
import { useMe } from "./me";
import { lazyPage } from "./lazyPage";
import type { OpenOptions } from "./player";

// The always-loaded sliver of the audiobook player. The player itself (lib/player.ts) and
// the mini-player are lazy chunks, so nobody downloads them until they press play — or
// come back with a book to resume. What has to be here from the start:
//
//  - the one <audio> element, so the sound outlives every page and route change;
//  - playAudiobook(), which every Play button calls. iPhones only let a page start audio
//    from inside a tap, and loading the player chunk takes an await, so the tap first
//    plays a moment of silence on the element ("unlocking" it); the real book follows
//    once the chunk is in;
//  - PlayerHost, which the layouts render: nothing until the player is loaded, then the
//    mini-player.

type PlayerModule = typeof import("./player");

let el: HTMLAudioElement | undefined;
let silentUrl: string | undefined;
let mod: PlayerModule | undefined;
let loading: Promise<PlayerModule> | undefined;
let uid = 0;
const subs = new Set<() => void>();

/** The page's one audio element. Detached from the DOM: it plays anyway, on every page. */
export function audioEl(): HTMLAudioElement {
  if (!el) {
    el = new Audio();
    el.preload = "metadata";
    el.setAttribute("playsinline", "");
  }
  return el;
}

/** The signed-in person's id (0 signed out), for tagging what the player remembers. */
export const playerUser = () => uid;

// silence is a tenth of a second of 8 kHz silence as a WAV, built rather than shipped.
function silence(): string {
  if (!silentUrl) {
    const n = 800;
    const b = new Uint8Array(44 + n);
    const v = new DataView(b.buffer);
    const tag = (o: number, s: string) => { for (let i = 0; i < 4; i++) b[o + i] = s.charCodeAt(i); };
    tag(0, "RIFF"); v.setUint32(4, 36 + n, true); tag(8, "WAVE"); tag(12, "fmt ");
    v.setUint32(16, 16, true); v.setUint16(20, 1, true); v.setUint16(22, 1, true);
    v.setUint32(24, 8000, true); v.setUint32(28, 8000, true); v.setUint16(32, 1, true); v.setUint16(34, 8, true);
    tag(36, "data"); v.setUint32(40, n, true); b.fill(128, 44);
    silentUrl = URL.createObjectURL(new Blob([b], { type: "audio/wav" }));
  }
  return silentUrl;
}

/** unlockAudio plays silence on the element. Call it synchronously inside a tap. */
export function unlockAudio(): void {
  const a = audioEl();
  try {
    a.src = silence();
    void a.play()?.catch(() => { /* the real source replaces it anyway */ });
  } catch { /* nothing to unlock in this browser */ }
}

/** loadPlayer fetches the player chunk (once) and shows the mini-player. */
export function loadPlayer(): Promise<PlayerModule> {
  loading ??= import("./player").then((m) => {
    mod = m;
    subs.forEach((f) => f());
    return m;
  }).catch((e) => {
    loading = undefined; // a later tap tries again
    throw e;
  });
  return loading;
}

/**
 * playAudiobook starts (or resumes) a book from a tap: at its saved place, or at `at`
 * seconds. Always call it straight from the click handler, before any await.
 */
export function playAudiobook(key: string, opts: OpenOptions = {}): void {
  if (mod) {
    mod.open(key, opts);
    return;
  }
  unlockAudio();
  void loadPlayer().then((m) => m.open(key, opts)).catch(() => { /* a chunk that won't load: the button just does nothing */ });
}

const LAST = "arrmada.player.last";
// hasLast: this person left a book part-way in this browser, so the mini-player offers it.
function hasLast(id: number): boolean {
  try { return (JSON.parse(localStorage.getItem(LAST) ?? "null") as { u?: number } | null)?.u === id; } catch { return false; }
}

const MiniPlayer = lazyPage(() => import("../components/audiobooks/MiniPlayer"), "MiniPlayer");
const subscribe = (f: () => void) => { subs.add(f); return () => { subs.delete(f); }; };

/** PlayerHost renders the mini-player once the player is loaded. Both layouts mount it. */
export function PlayerHost({ sidebar = false }: { sidebar?: boolean }) {
  const { user } = useMe();
  const ready = useSyncExternalStore(subscribe, () => !!mod);
  const id = user?.id ?? 0;
  useEffect(() => {
    uid = id;
    if (mod) mod.setUser(id);
    else if (id && hasLast(id)) void loadPlayer().catch(() => {});
  }, [id, ready]);
  // Unmounted: signed out (App swaps the layout for the sign-in screen).
  useEffect(() => () => { uid = 0; mod?.setUser(0); }, []);
  if (!ready || !id) return null;
  return <Suspense fallback={null}><MiniPlayer sidebar={sidebar} /></Suspense>;
}
