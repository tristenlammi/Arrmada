import { createContext, useCallback, useContext, useRef, useState, type ReactNode } from "react";
import { ConfirmDialog } from "./ConfirmDialog";

export interface ConfirmOptions {
  title: ReactNode;
  body?: ReactNode;
  confirmLabel?: string;
  /** danger: the red button, for anything destructive or that tells someone "no". */
  tone?: "danger" | "default";
}
export type ConfirmFn = (opts: ConfirmOptions) => Promise<boolean>;

const ConfirmCtx = createContext<ConfirmFn | null>(null);

interface Pending { id: number; opts: ConfirmOptions; resolve: (ok: boolean) => void }

// ConfirmProvider answers useConfirm: one ConfirmDialog at a time, resolved true on
// Confirm and false on Cancel, Escape or the backdrop. It replaces window.confirm,
// which can't be styled, blocks the whole tab, and some browsers suppress.
export function ConfirmProvider({ children }: { children: ReactNode }) {
  const [pending, setPending] = useState<Pending | null>(null);
  const current = useRef<Pending | null>(null);
  const seq = useRef(0);

  const settle = useCallback((ok: boolean) => {
    const p = current.current;
    current.current = null;
    setPending(null);
    p?.resolve(ok);
  }, []);

  const confirm = useCallback<ConfirmFn>((opts) => new Promise<boolean>((resolve) => {
    // A second ask while one is open cancels the first rather than queueing behind it.
    current.current?.resolve(false);
    const p = { id: ++seq.current, opts, resolve };
    current.current = p;
    setPending(p);
  }), []);

  return (
    <ConfirmCtx.Provider value={confirm}>
      {children}
      {pending && (
        <ConfirmDialog
          key={pending.id}
          title={pending.opts.title}
          body={pending.opts.body}
          confirmLabel={pending.opts.confirmLabel ?? "Confirm"}
          tone={pending.opts.tone === "danger" ? "danger" : "accent"}
          onConfirm={() => settle(true)}
          onCancel={() => settle(false)}
        />
      )}
    </ConfirmCtx.Provider>
  );
}

// useConfirm returns confirm({title, body, confirmLabel, tone}) => Promise<boolean>.
export function useConfirm(): ConfirmFn {
  const fn = useContext(ConfirmCtx);
  if (!fn) throw new Error("useConfirm needs a <ConfirmProvider> above it (main.tsx mounts one)");
  return fn;
}
