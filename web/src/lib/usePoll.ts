import { useCallback, useEffect, useRef, useState } from "react";
import { SIGNED_OUT_EVENT } from "./api";

export interface PollOptions {
  /** Run fn as soon as the poll arms (the default), or only after the first wait. */
  immediate?: boolean;
  /** Stop while the tab is hidden and catch up the moment it's visible again (the default). */
  pauseHidden?: boolean;
}

// usePoll runs fn every `ms` milliseconds, and is the only timer loop pages should use.
//
//  - It's a setTimeout chain, not setInterval: the next wait starts after fn's promise
//    settles, so a slow server gets one request at a time instead of a pile-up.
//  - A hidden tab makes no requests. When the tab comes back, fn runs straight away
//    and the chain resumes, so the page is fresh within one request.
//  - ms = null switches it off; a new ms (fast while something is running, slow when
//    idle) re-arms it.
//  - It stops for good on sign-out, so a dead session doesn't keep knocking.
//  - fn is read from a ref: passing a fresh closure every render doesn't re-arm it.
//
// The first run happens even in a hidden tab (a page opened in the background still
// needs its data once); only the repeats wait for visibility.
export function usePoll(fn: () => Promise<unknown> | void, ms: number | null, { immediate = true, pauseHidden = true }: PollOptions = {}) {
  const ref = useRef(fn);
  useEffect(() => { ref.current = fn; });

  useEffect(() => {
    if (ms == null) return;
    let stopped = false;
    let running = false;
    let timer: ReturnType<typeof setTimeout> | undefined;
    const hidden = () => pauseHidden && document.visibilityState === "hidden";

    const schedule = () => {
      clearTimeout(timer);
      timer = undefined;
      if (stopped || hidden()) return;
      timer = setTimeout(run, ms);
    };
    const run = async () => {
      timer = undefined;
      if (stopped || running) return;
      running = true;
      try { await ref.current(); } catch { /* the page shows its own errors; keep polling */ }
      running = false;
      schedule();
    };

    const onVisible = () => {
      if (stopped) return;
      if (hidden()) { clearTimeout(timer); timer = undefined; return; }
      // Back in view: refresh now, unless a request is already out (its settle
      // re-arms the chain).
      if (!running) { clearTimeout(timer); run(); }
    };
    const stop = () => { stopped = true; clearTimeout(timer); };

    if (pauseHidden) document.addEventListener("visibilitychange", onVisible);
    window.addEventListener(SIGNED_OUT_EVENT, stop);
    if (immediate) run(); else schedule();

    return () => {
      stop();
      document.removeEventListener("visibilitychange", onVisible);
      window.removeEventListener(SIGNED_OUT_EVENT, stop);
    };
  }, [ms, immediate, pauseHidden]);
}

// usePollBurst is for "the server is working on it in the background": after start(),
// fn runs every ms for `times` runs, then onDone fires (e.g. to re-enable a Scan
// button). It is usePoll underneath, so the runs pause in a hidden tab and never overlap.
export function usePollBurst(fn: () => Promise<unknown> | void, ms: number, times: number, onDone?: () => void): () => void {
  const [on, setOn] = useState(false);
  const left = useRef(0);
  const done = useRef(onDone);
  useEffect(() => { done.current = onDone; });
  usePoll(() => {
    if (--left.current <= 0) { setOn(false); done.current?.(); }
    return fn();
  }, on ? ms : null, { immediate: false });
  return useCallback(() => { left.current = times; setOn(true); }, [times]);
}
