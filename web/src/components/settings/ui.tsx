// The building blocks every Settings section is made of, shared so the sections (one file
// each under pages/settings/) look the same.

export const input = "w-full rounded-lg px-3 py-2 font-mono text-[12.5px]";
export const inputStyle = { background: "var(--panel-2)", border: "1px solid var(--line)", color: "var(--ink)" } as const;

// id makes a section a deep-link target (LINKS.apiKeys and friends); scroll-mt keeps the
// sticky page header from covering its title when a link scrolls to it.
export function Section({ id, title, subtitle, children }: { id?: string; title: string; subtitle: React.ReactNode; children: React.ReactNode }) {
  return (
    <div id={id} className="scroll-mt-20 rounded-xl p-5" style={{ background: "var(--panel)", border: "1px solid var(--line)" }}>
      <h2 className="m-0 text-[14px] font-bold">{title}</h2>
      <p className="mb-4 mt-0.5 text-[11.5px] text-ink-faint">{subtitle}</p>
      <div className="flex flex-col gap-4">{children}</div>
    </div>
  );
}

export function Field({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <label className="flex flex-col gap-1.5">
      <span className="font-mono text-[9.5px] font-bold uppercase tracking-[0.1em] text-ink-faint">{label}</span>
      {children}
    </label>
  );
}

export function Preview({ children }: { children: React.ReactNode }) {
  return <span className="text-[11px] text-ink-dim">→ <span className="font-mono" style={{ color: "var(--accent)" }}>{children}</span></span>;
}

export function TokenList({ tokens }: { tokens: string[] }) {
  return (
    <div className="flex flex-wrap gap-1.5">
      {tokens.map((t) => (
        <code key={t} className="rounded px-1.5 py-0.5 font-mono text-[10.5px]" style={{ background: "var(--panel-2)", color: "var(--ink-dim)" }}>{`{${t}}`}</code>
      ))}
    </div>
  );
}

export function Note({ tone, children }: { tone: "warn" | "info"; children: React.ReactNode }) {
  const color = tone === "warn" ? "var(--avoid)" : "var(--line)";
  return (
    <div
      className="rounded-lg p-3 text-[11.5px] leading-relaxed"
      style={{ background: "var(--panel-2)", border: `1px solid ${color}`, color: "var(--ink-dim)" }}
    >
      {children}
    </div>
  );
}

export function Toggle({ label, hint, checked, onChange }: { label: string; hint: string; checked: boolean; onChange: (v: boolean) => void }) {
  return (
    <div className="flex items-center justify-between gap-3">
      <div className="min-w-0">
        <div className="text-[12.5px] font-semibold">{label}</div>
        <div className="text-[10.5px] text-ink-faint">{hint}</div>
      </div>
      <button
        role="switch"
        aria-checked={checked}
        onClick={() => onChange(!checked)}
        className="relative inline-flex h-6 w-11 flex-none items-center rounded-full transition-colors"
        style={{ background: checked ? "var(--accent)" : "var(--panel-2)", border: "1px solid var(--line)" }}
      >
        <span className="inline-block h-4 w-4 rounded-full bg-white transition-transform" style={{ transform: checked ? "translateX(22px)" : "translateX(3px)" }} />
      </button>
    </div>
  );
}
