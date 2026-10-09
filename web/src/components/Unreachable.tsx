import { useCallback, useEffect, useRef, useState } from "react";
import { FleetMark } from "./FleetMark";
import { useMe } from "../lib/me";

// Back off quickly at first (a container restart takes seconds), then settle on
// a gentle 30s so a phone left on this screen isn't hammering a dead server.
const DELAYS = [5_000, 10_000, 20_000];
export function retryDelay(attempt: number): number {
  return DELAYS[attempt] ?? 30_000;
}

// Unreachable is the boot screen when Arrmada doesn't answer at all. It keeps
// retrying in the background (and straight away when the device comes back
// online); the first success boots the app normally.
export function Unreachable() {
  const { retry } = useMe();
  const [attempt, setAttempt] = useState(0);
  const [busy, setBusy] = useState(false);
  const inFlight = useRef(false);

  const run = useCallback(async () => {
    if (inFlight.current) return;
    inFlight.current = true;
    setBusy(true);
    const ok = await retry();
    inFlight.current = false;
    // On success MeProvider flips unreachable off and this screen unmounts.
    if (!ok) {
      setBusy(false);
      setAttempt((a) => a + 1);
    }
  }, [retry]);

  useEffect(() => {
    const t = window.setTimeout(run, retryDelay(attempt));
    return () => window.clearTimeout(t);
  }, [attempt, run]);

  useEffect(() => {
    window.addEventListener("online", run);
    return () => window.removeEventListener("online", run);
  }, [run]);

  return (
    <div className="grid h-full place-items-center bg-bg px-5">
      <div className="w-full max-w-[380px] text-center">
        <span className="mx-auto mb-4 grid h-12 w-12 place-items-center rounded-2xl bg-accent-grad text-accent-ink">
          <FleetMark className="h-6 w-6" />
        </span>
        <div className="rounded-2xl border border-line bg-panel p-5 shadow-panel">
          <h1 className="m-0 text-[17px] font-bold">Can't reach Arrmada</h1>
          <p className="m-0 mt-1.5 text-[12.5px] text-ink-dim">
            The server isn't answering. It may be restarting, or this device may be offline.
          </p>
          <button
            type="button"
            onClick={run}
            disabled={busy}
            className="mt-4 w-full rounded-lg bg-accent-grad px-4 py-2.5 text-[13px] font-semibold text-accent-ink"
            aria-live="polite"
          >
            {busy ? "Retrying…" : "Retry"}
          </button>
          <p className="m-0 mt-2.5 text-[11px] text-ink-faint">Arrmada keeps trying by itself and opens as soon as it answers.</p>
        </div>
      </div>
    </div>
  );
}
