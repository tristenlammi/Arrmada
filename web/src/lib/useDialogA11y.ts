import { useEffect, useRef, useState, type RefObject } from "react";

// useDialogA11y is the plumbing every dialog needs, whatever it looks like: focus moved
// in on open and trapped inside, Escape to close, focus handed back to whatever opened
// it, and the page behind locked from scrolling. The kit's Modal (and so every Sheet)
// is built on it; a hand-built overlay can use it directly.

// Every open dialog, oldest first. Only the last one answers Escape and keeps Tab
// inside itself, so a confirm opened over a sheet closes on Esc without taking the
// sheet with it.
const stack: symbol[] = [];
const isTop = (id: symbol) => stack[stack.length - 1] === id;

// For tests: how many dialogs are open right now.
export const openDialogCount = () => stack.length;

// Body scroll lock is a counter, not a saved value per dialog: with two stacked
// dialogs the second would otherwise "restore" the first one's hidden overflow and
// leave the page frozen after both close.
let locks = 0;
let savedOverflow = "";
function lockScroll() {
  if (locks++ === 0) {
    savedOverflow = document.body.style.overflow;
    document.body.style.overflow = "hidden";
  }
}
function unlockScroll() {
  if (locks > 0 && --locks === 0) document.body.style.overflow = savedOverflow;
}

const FOCUSABLE = 'a[href],button:not([disabled]),input:not([disabled]),select:not([disabled]),textarea:not([disabled]),[tabindex]:not([tabindex="-1"])';

export interface DialogA11yOptions {
  open: boolean;
  onClose: () => void;
  /** false: Escape does nothing (e.g. while a request is in flight). */
  dismissible?: boolean;
  /** What takes focus on open; the dialog element itself when unset. */
  initialFocus?: RefObject<HTMLElement>;
}

export function useDialogA11y(ref: RefObject<HTMLElement>, { open, onClose, dismissible = true, initialFocus }: DialogA11yOptions): void {
  // What had focus at first render, before any autoFocus child inside could take it.
  const [firstFocus] = useState(() => (typeof document === "undefined" ? null : document.activeElement));
  // Latest values without re-running the open/close effect on every parent render
  // (onClose is usually a fresh arrow each time).
  const closeRef = useRef(onClose);
  const dismissRef = useRef(dismissible);
  useEffect(() => { closeRef.current = onClose; dismissRef.current = dismissible; });

  useEffect(() => {
    if (!open) return;
    const id = Symbol("dialog");
    stack.push(id);
    lockScroll();
    // A child with autoFocus has already taken focus by now: leave it there, and
    // restore to whatever had focus when the dialog first rendered instead.
    const active = document.activeElement as HTMLElement | null;
    const focusedInside = !!active && !!ref.current?.contains(active);
    const opener = focusedInside ? (firstFocus as HTMLElement | null) : active;
    if (!focusedInside) (initialFocus?.current ?? ref.current)?.focus();

    const onKey = (e: KeyboardEvent) => {
      if (!isTop(id)) return;
      if (e.key === "Escape") {
        // Captured and stopped here so the page under the dialog (a search box, a
        // menu) never sees the same Escape.
        e.stopPropagation();
        e.preventDefault();
        if (dismissRef.current) closeRef.current();
        return;
      }
      if (e.key !== "Tab" || !ref.current) return;
      const nodes = ref.current.querySelectorAll<HTMLElement>(FOCUSABLE);
      if (nodes.length === 0) { e.preventDefault(); return; }
      const first = nodes[0], last = nodes[nodes.length - 1];
      const at = document.activeElement;
      const inside = !!at && ref.current.contains(at);
      if (e.shiftKey && (at === first || at === ref.current || !inside)) { e.preventDefault(); last.focus(); }
      else if (!e.shiftKey && (at === last || !inside)) { e.preventDefault(); first.focus(); }
    };
    document.addEventListener("keydown", onKey, true);
    return () => {
      document.removeEventListener("keydown", onKey, true);
      const i = stack.indexOf(id);
      if (i >= 0) stack.splice(i, 1);
      unlockScroll();
      opener?.focus?.();
    };
    // initialFocus and ref are read once at open time on purpose.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [open]);
}
