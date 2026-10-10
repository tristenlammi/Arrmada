import { Modal, type ModalProps } from "./Modal";
import { useBackToClose } from "../lib/useBackToClose";

export interface SheetProps extends Omit<ModalProps, "variant"> {
  /**
   * Back (Android's button, the iOS swipe, the browser's) closes the sheet instead of
   * leaving the page. Turn it off when the sheet's open state already lives in the
   * address (?id=, a title route): Back works through that instead.
   */
  closeOnBack?: boolean;
  /** The grab handle a phone shows on a bottom sheet; off for a full-screen panel. */
  handle?: boolean;
}

// Sheet is the kit's bottom sheet: docked to the bottom of a phone (above the tab bar
// and the home indicator), a centred card from 640px up. It is the Modal's sheet variant
// plus Back-to-close, so it keeps all of Modal's focus, Escape and scroll handling.
export function Sheet({ closeOnBack = true, handle = true, open = true, onClose, children, ...rest }: SheetProps) {
  const close = useBackToClose(open, onClose, closeOnBack);
  return (
    <Modal {...rest} open={open} variant="sheet" onClose={close}>
      {handle && <div aria-hidden className="mx-auto mt-2 h-1 w-10 rounded-full sm:hidden" style={{ background: "var(--line)" }} />}
      {children}
    </Modal>
  );
}
