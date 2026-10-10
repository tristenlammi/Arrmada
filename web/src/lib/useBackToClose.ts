import { useCallback, useEffect, useId, useRef } from "react";
import { useLocation, useNavigate } from "react-router-dom";

// useBackToClose makes Back close an open sheet instead of leaving the page: Android's
// Back button, the iOS swipe and the browser's Back all do it, which is what a phone
// app's sheet does and what people try first.
//
// While the sheet is open it owns one history entry: the same address with the sheet's
// id added to the entry's router state (sheets: [...]). Back pops that entry, the id is
// gone, and the sheet closes. Closing it any other way (Esc, the backdrop, a Close button,
// the parent unmounting it) steps back over its entry, so Back afterwards leaves the page
// as normal and no dead entries pile up. It goes through the router, not raw
// history.pushState, so the router's own idea of the history stays right.
//
// A sheet opened over another one adds its id to the list: Back closes only the top one.
//
// It returns the close to hand to the sheet's Esc and backdrop: it steps back, and onClose
// runs when the entry is gone.

interface SheetState { sheets?: string[] }

const sheetsIn = (state: unknown): string[] => {
  const s = (state as SheetState | null)?.sheets;
  return Array.isArray(s) ? s : [];
};
// The entry the browser is on right now (react-router keeps its state under usr). Read at
// unmount, when the hook's last render may already be out of date.
const currentEntrySheets = (): string[] => {
  try { return sheetsIn((window.history.state as { usr?: unknown } | null)?.usr); } catch { return []; }
};

export function useBackToClose(open: boolean, onClose: () => void, enabled = true): () => void {
  const id = useId();
  const location = useLocation();
  const navigate = useNavigate();
  // none: no entry; pending: pushed, the router hasn't moved yet; on: our entry is current
  // (or below a stacked sheet's).
  const phase = useRef<"none" | "pending" | "on">("none");
  const closeRef = useRef(onClose);
  const openRef = useRef(open);
  useEffect(() => { closeRef.current = onClose; openRef.current = open; });
  const mine = sheetsIn(location.state).includes(id);

  // Opened: push our entry. Closed by the parent while the entry is still there: step
  // back over it.
  useEffect(() => {
    if (!enabled) return;
    if (open && phase.current === "none") {
      phase.current = "pending";
      const { pathname, search, hash, state } = location;
      navigate({ pathname, search, hash }, { state: { ...(state as object | null), sheets: [...sheetsIn(state), id] } });
    } else if (!open && phase.current !== "none") {
      const was = phase.current;
      phase.current = "none";
      if (was === "on" && currentEntrySheets().includes(id)) navigate(-1);
    }
    // Only open/close drive this; location is read at that moment on purpose.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [open, enabled]);

  // The address moved: our entry arrived, or Back took it away.
  useEffect(() => {
    if (phase.current === "pending" && mine) phase.current = "on";
    else if (phase.current === "on" && !mine) {
      phase.current = "none";
      if (openRef.current) closeRef.current();
    }
  }, [location.key, mine]);

  // Unmounted while open (the parent dropped the sheet): don't leave our entry behind.
  // Unless the address already moved on, e.g. a link inside the sheet went elsewhere.
  useEffect(() => () => {
    if (phase.current === "on" && currentEntrySheets().includes(id)) {
      phase.current = "none";
      navigate(-1);
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  return useCallback(() => {
    if (enabled && phase.current === "on") {
      navigate(-1); // the location effect calls onClose once the entry is gone
      return;
    }
    phase.current = "none";
    closeRef.current();
  }, [enabled, navigate]);
}
