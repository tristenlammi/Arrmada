import { createContext, useCallback, useContext, useEffect, useRef, useState, type ReactNode } from "react";
import { createPortal } from "react-dom";

export type ToastTone = "info" | "good" | "error";
export interface ToastOptions {
  tone?: ToastTone;
  /** How long it stays up, in ms. */
  ms?: number;
}
export type ToastFn = (msg: string, opts?: ToastOptions) => void;

interface Item { id: number; msg: string; tone: ToastTone }

// At most this many on screen; a burst of failures shouldn't wallpaper the page.
const MAX = 3;

const ToastCtx = createContext<ToastFn | null>(null);

// ToastProvider owns the one live region every page's short confirmations and
// failures go through. It sits above the requester bottom bar (--bottom-chrome),
// stacks up to three, and each message clears on its own timer — a second toast
// no longer cuts the first one short.
export function ToastProvider({ children }: { children: ReactNode }) {
  const [items, setItems] = useState<Item[]>([]);
  const next = useRef(0);
  const timers = useRef(new Map<number, ReturnType<typeof setTimeout>>());

  const dismiss = useCallback((id: number) => {
    const t = timers.current.get(id);
    if (t) clearTimeout(t);
    timers.current.delete(id);
    setItems((xs) => xs.filter((x) => x.id !== id));
  }, []);

  const toast = useCallback<ToastFn>((msg, opts) => {
    const id = ++next.current;
    const tone = opts?.tone ?? "info";
    setItems((xs) => {
      const keep = [...xs, { id, msg, tone }];
      // Drop the oldest beyond the cap, and its timer with it.
      for (const old of keep.slice(0, Math.max(0, keep.length - MAX))) {
        const t = timers.current.get(old.id);
        if (t) clearTimeout(t);
        timers.current.delete(old.id);
      }
      return keep.slice(-MAX);
    });
    timers.current.set(id, setTimeout(() => dismiss(id), opts?.ms ?? 3000));
  }, [dismiss]);

  useEffect(() => {
    const map = timers.current;
    return () => { map.forEach(clearTimeout); map.clear(); };
  }, []);

  return (
    <ToastCtx.Provider value={toast}>
      {children}
      {createPortal(
        <div
          aria-live="polite"
          className="pointer-events-none fixed left-1/2 z-toast flex w-max max-w-[calc(100vw-2rem)] -translate-x-1/2 flex-col items-center gap-2 font-sans"
          style={{ bottom: "calc(var(--bottom-chrome) + 20px)" }}
        >
          {items.map((t) => (
            <div
              key={t.id}
              role={t.tone === "error" ? "alert" : "status"}
              className="pointer-events-auto rounded-lg px-4 py-2.5 text-[12.5px] font-medium"
              style={{
                background: "var(--panel-2)",
                border: `1px solid ${t.tone === "error" ? "var(--reject)" : t.tone === "good" ? "var(--good)" : "var(--line)"}`,
                boxShadow: "var(--shadow)",
                color: t.tone === "error" ? "var(--reject-text)" : "var(--ink)",
              }}
            >
              {t.msg}
            </div>
          ))}
        </div>,
        document.body,
      )}
    </ToastCtx.Provider>
  );
}

// useToast returns toast(msg, {tone, ms}). Outside a provider (an isolated test)
// it's a no-op rather than a crash.
export function useToast(): ToastFn {
  return useContext(ToastCtx) ?? noop;
}
const noop: ToastFn = () => {};
