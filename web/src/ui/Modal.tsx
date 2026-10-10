import { useId, useRef, type CSSProperties, type ReactNode, type RefObject } from "react";
import { createPortal } from "react-dom";
import { openDialogCount, useDialogA11y } from "../lib/useDialogA11y";

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
  const downOnBackdrop = useRef(true);
  // Focus in and trapped, Escape, focus back to the opener, page scroll locked.
  useDialogA11y(ref, { open, onClose, dismissible, initialFocus });

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
        // React bubbles events out of a portal to the component that rendered it, so
        // a click on Confirm would otherwise also reach (and close) a hand-built
        // overlay this modal was opened from.
        e.stopPropagation();
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
export const openModalCount = openDialogCount;
