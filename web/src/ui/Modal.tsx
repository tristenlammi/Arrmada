import { useEffect, useId, useRef, type CSSProperties, type ReactNode, type RefObject } from "react";
import { createPortal } from "react-dom";

// Every open Modal, oldest first. Only the last one answers Escape and keeps Tab
// inside itself, so a confirm opened over a sheet closes on Esc without taking the
// sheet with it.
const stack: symbol[] = [];
const isTop = (id: symbol) => stack[stack.length - 1] === id;

// Body scroll lock is a counter, not a saved value per modal: with two stacked
// modals the second would otherwise "restore" the first one's hidden overflow and
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

export type ModalSize = "sm" | "md" | "lg" | "xl";
const WIDTH: Record<ModalSize, string> = {
  sm: "max-w-[440px]",
  md: "max-w-[560px]",
  lg: "max-w-[720px]",
  xl: "max-w-[820px]",
};

export interface ModalProps {
  /** Defaults to true, so a caller that mounts the modal conditionally needn't pass it. */
  open?: boolean;
  onClose: () => void;
  /** A heading rendered at the top of the panel and used as the dialog's name. */
  title?: ReactNode;
  /** The id of a heading the caller renders itself (instead of title). */
  labelledBy?: string;
  /** A plain-text name when there is no visible heading. */
  ariaLabel?: string;
  size?: ModalSize;
  /** dialog: centred card. sheet: docks to the bottom on a phone, centred above sm. */
  variant?: "dialog" | "sheet";
  /** false: Escape and the backdrop do nothing (e.g. while a request is in flight). */
  dismissible?: boolean;
  /** What takes focus on open; the panel itself when unset. */
  initialFocus?: RefObject<HTMLElement>;
  /** Buttons along the bottom, right-aligned. */
  footer?: ReactNode;
  /** Replaces the variant's panel sizing and shape, for a sheet with its own layout. */
  panelClassName?: string;
  panelStyle?: CSSProperties;
  panelRef?: RefObject<HTMLDivElement>;
  /** Backdrop darkness, 0..1. */
  scrim?: number;
  children?: ReactNode;
}

// Modal is the one overlay every dialog and sheet renders through: portalled to
// <body> so no page's stacking context can clip it or sink it under the header,
// role=dialog with a name, focus moved in and trapped, focus restored to the opener,
// Escape and backdrop to close, and the page behind it locked from scrolling.
export function Modal({
  open = true, onClose, title, labelledBy, ariaLabel, size = "md", variant = "dialog",
  dismissible = true, initialFocus, footer, panelClassName, panelStyle, panelRef, scrim = 0.6, children,
}: ModalProps) {
  const titleId = useId();
  const ownRef = useRef<HTMLDivElement>(null);
  const ref = panelRef ?? ownRef;
  // Latest values without re-running the open/close effect on every parent render
  // (onClose is usually a fresh arrow each time).
  const downOnBackdrop = useRef(true);
  const closeRef = useRef(onClose);
  const dismissRef = useRef(dismissible);
  useEffect(() => { closeRef.current = onClose; dismissRef.current = dismissible; });

  useEffect(() => {
    if (!open) return;
    const id = Symbol("modal");
    stack.push(id);
    lockScroll();
    const opener = document.activeElement as HTMLElement | null;
    (initialFocus?.current ?? ref.current)?.focus();

    const onKey = (e: KeyboardEvent) => {
      if (!isTop(id)) return;
      if (e.key === "Escape") {
        // Captured and stopped here so the page under the modal (a search box, a
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

  if (!open) return null;

  const sheet = variant === "sheet";
  const overlay = sheet
    ? "fixed inset-0 z-modal flex items-end justify-center overflow-hidden font-sans sm:items-center sm:p-6"
    : "fixed inset-0 z-modal grid place-items-center p-4 font-sans";
  const shape = panelClassName ?? (sheet
    ? `thin-scroll w-full overflow-y-auto rounded-t-2xl pb-[env(safe-area-inset-bottom)] max-h-[calc(100dvh-2rem)] sm:rounded-2xl sm:pb-0 ${WIDTH[size]}`
    : `thin-scroll w-full overflow-y-auto rounded-2xl p-5 max-h-[calc(100dvh-2rem)] ${WIDTH[size]}`);
  const named = labelledBy ?? (title ? titleId : undefined);

  return createPortal(
    <div
      role="presentation"
      className={overlay}
      style={{ background: `rgba(0,0,0,${scrim})` }}
      // Only a click that starts and ends on the backdrop itself closes: a drag that
      // began inside the panel (selecting text) and ended outside must not.
      onMouseDown={(e) => { downOnBackdrop.current = e.target === e.currentTarget; }}
      onClick={(e) => {
        if (e.target === e.currentTarget && downOnBackdrop.current && dismissible) onClose();
        downOnBackdrop.current = true;
      }}
    >
      <div
        ref={ref}
        role="dialog"
        aria-modal="true"
        aria-labelledby={named}
        aria-label={named ? undefined : ariaLabel}
        tabIndex={-1}
        className={`relative outline-none ${shape}`}
        style={{ background: "var(--panel)", border: "1px solid var(--line)", boxShadow: panelClassName ? undefined : "var(--shadow)", ...panelStyle }}
      >
        {title && <h2 id={titleId} className="m-0 text-[15px] font-bold">{title}</h2>}
        {children}
        {footer && <div className="mt-4 flex justify-end gap-2.5">{footer}</div>}
      </div>
    </div>,
    document.body,
  );
}

// For tests: how many modals are open right now.
export const openModalCount = () => stack.length;
