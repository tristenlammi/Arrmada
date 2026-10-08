import { useEffect, useId, useRef, useState, type ReactNode } from "react";

// One choice in a ConfirmDialog radio list (e.g. "keep the files" / "delete the files").
export interface ConfirmChoice<V extends string = string> {
  value: V;
  label: ReactNode;
  hint?: ReactNode;
  danger?: boolean;
  disabled?: boolean;
}

interface Props<V extends string> {
  title: ReactNode;
  body?: ReactNode;
  confirmLabel: string;
  busyLabel?: string;
  tone?: "danger" | "accent";
  // Optional radio list. The caller owns the selection so the confirm label and body can
  // follow it ("Remove and delete files").
  choices?: ConfirmChoice<V>[];
  choice?: V;
  onChoice?: (v: V) => void;
  // Slot under the choices for checkboxes ("Also stop wanting this").
  extra?: ReactNode;
  // When set, Confirm stays disabled until this exact text is typed. Kept for things that
  // are irreversible or very large, so it doesn't become a reflex.
  typedPhrase?: string;
  busy?: boolean;
  // A server refusal (e.g. the recycle bin couldn't take a file) shows here and the dialog
  // stays open, so nobody has to guess whether anything happened.
  error?: string | null;
  // Disables Confirm without an error, e.g. while a preview is still loading.
  confirmDisabled?: boolean;
  onConfirm: () => void;
  onCancel: () => void;
}

// ConfirmDialog is the one shape every destructive action asks through: what will happen,
// an optional choice, an optional typed phrase, and the server's answer if it says no.
// Escape and a backdrop click cancel; focus starts on Cancel so Enter never destroys
// anything by accident. The look is the existing modal (panel, line, shadow, reject).
export function ConfirmDialog<V extends string = string>({
  title, body, confirmLabel, busyLabel, tone = "danger", choices, choice, onChoice, extra,
  typedPhrase, busy = false, error, confirmDisabled = false, onConfirm, onCancel,
}: Props<V>) {
  const titleId = useId();
  const cancelRef = useRef<HTMLButtonElement>(null);
  const panelRef = useRef<HTMLDivElement>(null);
  const [typed, setTyped] = useState("");

  useEffect(() => { cancelRef.current?.focus(); }, []);

  // Escape cancels and Tab stays inside the dialog, so keyboard users can't wander into
  // the page behind it while it's open.
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key === "Escape") {
        e.preventDefault();
        if (!busy) onCancel();
        return;
      }
      if (e.key !== "Tab" || !panelRef.current) return;
      const focusable = panelRef.current.querySelectorAll<HTMLElement>("button:not([disabled]), input:not([disabled]), select:not([disabled]), textarea:not([disabled]), a[href]");
      if (focusable.length === 0) return;
      const first = focusable[0], last = focusable[focusable.length - 1];
      if (e.shiftKey && document.activeElement === first) { e.preventDefault(); last.focus(); }
      else if (!e.shiftKey && document.activeElement === last) { e.preventDefault(); first.focus(); }
    };
    document.addEventListener("keydown", onKey);
    return () => document.removeEventListener("keydown", onKey);
  }, [busy, onCancel]);

  const phraseOK = !typedPhrase || typed === typedPhrase;
  const canConfirm = phraseOK && !busy && !confirmDisabled;
  const solid = tone === "danger" ? "var(--reject)" : "linear-gradient(150deg, var(--accent), var(--accent-deep))";
  const solidInk = tone === "danger" ? "#fff" : "var(--accent-ink)";

  return (
    <div className="fixed inset-0 z-50 grid place-items-center p-4" style={{ background: "rgba(0,0,0,.6)" }} onClick={() => { if (!busy) onCancel(); }}>
      <div
        ref={panelRef}
        role="dialog"
        aria-modal="true"
        aria-labelledby={titleId}
        className="thin-scroll max-h-[calc(100vh-32px)] w-full max-w-[440px] overflow-y-auto rounded-2xl p-5"
        style={{ background: "var(--panel)", border: "1px solid var(--line)", boxShadow: "var(--shadow)" }}
        onClick={(e) => e.stopPropagation()}
      >
        <h2 id={titleId} className="m-0 text-[15px] font-bold">{title}</h2>
        {body && <div className="mt-1 text-[12px] text-ink-dim">{body}</div>}

        {choices && choices.length > 0 && (
          <div className="mt-4 flex flex-col gap-2" role="radiogroup">
            {choices.map((c) => {
              const on = c.value === choice;
              const tint = c.danger ? "var(--reject)" : "var(--accent)";
              return (
                <label key={c.value} className="flex items-start gap-2.5 rounded-lg p-3 text-[12.5px]" style={{ border: `1px solid ${on ? tint : "var(--line)"}`, background: on ? (c.danger ? "var(--reject-soft)" : "var(--accent-soft)") : "var(--panel-2)", cursor: c.disabled ? "default" : "pointer", opacity: c.disabled ? 0.6 : 1 }}>
                  <input type="radio" name={titleId} checked={on} disabled={c.disabled || busy} onChange={() => onChoice?.(c.value)} className="mt-0.5" />
                  <span>
                    <span className="font-semibold" style={{ color: on ? tint : "var(--ink)" }}>{c.label}</span>
                    {c.hint && <span className="mt-0.5 block text-[11px] text-ink-faint">{c.hint}</span>}
                  </span>
                </label>
              );
            })}
          </div>
        )}

        {extra && <div className="mt-3 flex flex-col gap-2">{extra}</div>}

        {typedPhrase && (
          <label className="mt-4 flex flex-col gap-1.5">
            <span className="text-[11.5px] text-ink-dim">Type <b className="font-mono" style={{ color: "var(--ink)" }}>{typedPhrase}</b> to confirm</span>
            <input
              value={typed}
              onChange={(e) => setTyped(e.target.value)}
              onKeyDown={(e) => { if (e.key === "Enter" && canConfirm) onConfirm(); }}
              disabled={busy}
              autoComplete="off"
              spellCheck={false}
              className="rounded-lg px-3 py-2 font-mono text-[12.5px]"
              style={{ background: "var(--panel-2)", border: `1px solid ${phraseOK ? "var(--line)" : "var(--reject)"}`, color: "var(--ink)" }}
            />
          </label>
        )}

        {error && <div role="alert" className="mt-3 whitespace-pre-line rounded-lg p-2.5 text-[12px]" style={{ color: "var(--reject)", background: "var(--reject-soft)" }}>{error}</div>}

        <div className="mt-4 flex justify-end gap-2.5">
          <button ref={cancelRef} onClick={onCancel} disabled={busy} className="rounded-lg px-4 py-2 text-[12.5px] font-semibold" style={{ border: "1px solid var(--line)", color: "var(--ink-dim)" }}>Cancel</button>
          <button onClick={onConfirm} disabled={!canConfirm} className="rounded-lg px-4 py-2 text-[12.5px] font-semibold disabled:opacity-50" style={{ background: solid, color: solidInk }}>{busy ? (busyLabel ?? "Working…") : confirmLabel}</button>
        </div>
      </div>
    </div>
  );
}
