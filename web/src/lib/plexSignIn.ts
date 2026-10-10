import { useCallback, useEffect, useRef, useState } from "react";

// Sign in with Plex, shared by the login page, Settings → Plex (connecting the server) and
// linking a Plex account. Plex's sign-in is a PIN: Arrmada starts one, the person approves
// it on plex.tv, and Arrmada polls the PIN until plex.tv says yes.
//
// Getting the person to plex.tv is the hard part on phones. Safari only allows a popup
// opened directly inside the tap, before any await, and the installed app (a home-screen
// PWA) can't open one at all. So the window is opened first, empty, and pointed at plex.tv
// once the PIN is known. When there is no window (blocked, or the installed app) the whole
// page goes to plex.tv instead, and plex.tv sends it back with ?plexpin=<id>; the page that
// started it picks the PIN up again from there.

export type PlexFlowKind = "login" | "connect" | "link";
export type PlexMode = "popup" | "redirect";
export interface PlexPin { id: number; auth_url: string }

export interface PlexFlow<T> {
  kind: PlexFlowKind;
  start: (mode: PlexMode) => Promise<PlexPin>;
  /** The flow's answer once Plex approved, or null while it's still waiting. */
  poll: (id: number) => Promise<T | null>;
  /** Runs just before the page leaves for plex.tv (remember where to come back to). */
  beforeRedirect?: () => void;
}

const POLL_MS = 2000;
const POPUP_TRIES = 90; // ~3 minutes with the Plex window open
const RESUME_TRIES = 15; // ~30 seconds after coming back from plex.tv
const SLOW_MS = 5000; // then offer "Continue in this tab"

const storeKey = (kind: PlexFlowKind) => `arrmada.plexpin.${kind}`;
const sleep = (ms: number) => new Promise((r) => setTimeout(r, ms));

// isStandalone: running as the installed app, where a popup can't open.
export function isStandalone(): boolean {
  try {
    return window.matchMedia?.("(display-mode: standalone)").matches || (navigator as { standalone?: boolean }).standalone === true;
  } catch {
    return false;
  }
}

// openPlexWindow opens the (still empty) Plex window. It must run synchronously inside the
// click handler, before any await, or Safari blocks it. null means no window: go by redirect.
export function openPlexWindow(): Window | null {
  if (isStandalone()) return null;
  let w: Window | null = null;
  try {
    w = window.open("", "plex-auth", "width=620,height=720");
  } catch {
    return null;
  }
  try {
    if (w) {
      w.document.title = "Plex";
      w.document.body.textContent = "Opening Plex…";
    }
  } catch {
    /* already somewhere else: fine */
  }
  return w;
}

// plexNav leaves the page for plex.tv (a seam, so tests can watch it).
export const plexNav = { assign: (url: string) => window.location.assign(url) };

export type PlexOutcome<T> = { done: T } | "cancelled" | "redirected";

export interface PlexRun<T> {
  outcome: Promise<PlexOutcome<T>>;
  cancel: () => void;
}

// runPlexFlow starts a PIN and sees it through: in the popup when there is one, else by
// sending this page to plex.tv. A closed popup ends it within one poll (about 2 s) — after
// one last look, in case they approved and then closed it.
//
// A window that reads as closed at the very first look wasn't closed by a person: the
// browser cut this page's handle to it (a Cross-Origin-Opener-Policy somewhere on the way,
// say a proxy's). Then closing can't be seen, so it just keeps waiting, with Cancel and
// "Continue in this tab" on offer. A network blip while polling is waited out too.
export function runPlexFlow<T>(flow: PlexFlow<T>, popup: Window | null, onSlow?: () => void): PlexRun<T> {
  let cancelled = false;
  let slow: ReturnType<typeof setTimeout> | undefined;
  const close = () => {
    try { popup?.close(); } catch { /* gone */ }
  };
  const outcome = (async (): Promise<PlexOutcome<T>> => {
    try {
      const pin = await flow.start(popup ? "popup" : "redirect");
      if (cancelled) return "cancelled";
      if (!popup) {
        flow.beforeRedirect?.();
        try { sessionStorage.setItem(storeKey(flow.kind), String(pin.id)); } catch { /* private mode */ }
        plexNav.assign(pin.auth_url);
        return "redirected";
      }
      popup.location.href = pin.auth_url;
      slow = setTimeout(() => { if (!cancelled) onSlow?.(); }, SLOW_MS);
      let watchClosed = true;
      for (let i = 0; i < POPUP_TRIES; i++) {
        await sleep(POLL_MS);
        if (cancelled) return "cancelled";
        let closed = false;
        try { closed = popup.closed; } catch { watchClosed = false; }
        if (closed && i === 0) watchClosed = false; // severed, not closed (see above)
        let r: T | null = null;
        try {
          r = await flow.poll(pin.id);
        } catch (e) {
          if (e instanceof TypeError) continue; // fetch failed: the network, not an answer
          throw e;
        }
        if (cancelled) return "cancelled";
        if (r !== null) return { done: r };
        if (closed && watchClosed) throw new Error("The Plex window closed before you finished — try again.");
      }
      throw new Error("Plex sign-in timed out — try again.");
    } finally {
      clearTimeout(slow);
      close();
    }
  })();
  return { outcome, cancel: () => { cancelled = true; close(); } };
}

// takePendingPlexPin is the PIN this page sent to plex.tv, if plex.tv just sent it back:
// ?<param>=<id> must equal the one this tab stored before leaving. A link carrying someone
// else's PIN is ignored, so it can't sign anyone into another account. The param is
// removed from the address either way (strip, else history.replaceState).
export function takePendingPlexPin(kind: PlexFlowKind, param: string, strip?: () => void): number | null {
  let fromURL: string | null = null;
  try {
    fromURL = new URLSearchParams(window.location.search).get(param);
  } catch {
    return null;
  }
  if (!fromURL) return null;
  if (strip) strip();
  else {
    try {
      const u = new URL(window.location.href);
      u.searchParams.delete(param);
      window.history.replaceState(window.history.state, "", u.pathname + u.search + u.hash);
    } catch { /* leave it */ }
  }
  let stored: string | null = null;
  try {
    stored = sessionStorage.getItem(storeKey(kind));
    sessionStorage.removeItem(storeKey(kind));
  } catch { /* storage blocked: nothing to match */ }
  const id = Number(fromURL);
  return stored !== null && stored === fromURL && Number.isInteger(id) && id > 0 ? id : null;
}

// resumePlexFlow finishes a PIN after the redirect back from plex.tv.
export async function resumePlexFlow<T>(flow: PlexFlow<T>, id: number): Promise<T> {
  for (let i = 0; i < RESUME_TRIES; i++) {
    const r = await flow.poll(id);
    if (r !== null) return r;
    await sleep(POLL_MS);
  }
  throw new Error("Plex didn't confirm in time — try again.");
}

export type PlexPhase = "idle" | "waiting" | "slow" | "finishing";

interface Options<T> {
  flow: PlexFlow<T>;
  onDone: (r: T) => void;
  onError: (msg: string) => void;
  /** The query param plex.tv comes back with (redirect mode); picked up on mount. */
  resumeParam?: string;
  /** Removes resumeParam from the URL (a router-aware setSearchParams), if not the default. */
  strip?: () => void;
}

// usePlexPinSignIn drives one Plex PIN flow for a page. begin() must be called straight
// from the click handler (it opens the window). While waiting the page shows Cancel, and
// after a few seconds "Continue in this tab" (continueHere), for a popup that never showed.
export function usePlexPinSignIn<T>(opts: Options<T>) {
  const [phase, setPhase] = useState<PlexPhase>("idle");
  const ref = useRef(opts);
  ref.current = opts;
  const run = useRef<PlexRun<T> | null>(null);

  const go = useCallback((popup: Window | null) => {
    run.current?.cancel();
    setPhase("waiting");
    const r = runPlexFlow(ref.current.flow, popup, () => setPhase((p) => (p === "waiting" ? "slow" : p)));
    run.current = r;
    r.outcome.then(
      (o) => {
        if (run.current !== r || o === "redirected") return; // superseded, or the page is leaving
        run.current = null;
        setPhase("idle");
        if (o !== "cancelled") ref.current.onDone(o.done);
      },
      (e: unknown) => {
        if (run.current !== r) return;
        run.current = null;
        setPhase("idle");
        ref.current.onError((e as Error).message);
      },
    );
  }, []);

  const begin = useCallback(() => go(openPlexWindow()), [go]);
  const continueHere = useCallback(() => go(null), [go]);
  const cancel = useCallback(() => {
    run.current?.cancel();
    run.current = null;
    setPhase("idle");
  }, []);

  // Back from plex.tv with ?<resumeParam>=<id>: finish that PIN.
  useEffect(() => {
    const o = ref.current;
    if (!o.resumeParam) return;
    const id = takePendingPlexPin(o.flow.kind, o.resumeParam, o.strip);
    if (id === null) return;
    setPhase("finishing");
    resumePlexFlow(o.flow, id).then(
      (r) => { setPhase("idle"); ref.current.onDone(r); },
      (e: unknown) => { setPhase("idle"); ref.current.onError((e as Error).message); },
    );
  }, []);

  return { phase, begin, cancel, continueHere };
}
