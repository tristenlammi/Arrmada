import { useMemo, useState } from "react";
import type { AudioListening } from "../../lib/api";

// Formatting and building blocks shared by the Audiobooks page and its admin tabs.

// --- formatting ---------------------------------------------------------------

export function fmtClock(sec: number): string {
  const s = Math.max(0, Math.floor(sec));
  const h = Math.floor(s / 3600);
  const m = Math.floor((s % 3600) / 60);
  const ss = s % 60;
  return h > 0 ? `${h}:${String(m).padStart(2, "0")}:${String(ss).padStart(2, "0")}` : `${m}:${String(ss).padStart(2, "0")}`;
}

export function fmtHours(sec: number): string {
  if (sec < 60) return sec > 0 ? "<1m" : "0m";
  const h = Math.floor(sec / 3600);
  const m = Math.round((sec % 3600) / 60);
  return h > 0 ? `${h}h ${m}m` : `${m}m`;
}

export function fmtAgo(ms: number): string {
  if (!ms) return "never";
  const d = (Date.now() - ms) / 1000;
  if (d < 90) return "just now";
  if (d < 3600) return `${Math.round(d / 60)} min ago`;
  if (d < 86400) return `${Math.round(d / 3600)} h ago`;
  if (d < 86400 * 14) return `${Math.round(d / 86400)} d ago`;
  return new Date(ms).toLocaleDateString();
}

export function fmtWhen(ms: number): string {
  return new Date(ms).toLocaleString([], { weekday: "short", day: "numeric", month: "short", hour: "2-digit", minute: "2-digit" });
}

// --- shared bits ----------------------------------------------------------------

export const card = { background: "var(--panel)", border: "1px solid var(--line)" } as const;
export const ghost = { border: "1px solid var(--line)", background: "var(--panel-2)", color: "var(--ink)" } as const;
export const inputStyle = { background: "var(--panel-2)", border: "1px solid var(--line)", color: "var(--ink)" } as const;
export const danger = { border: "1px solid var(--reject)", color: "var(--reject)" } as const;
export const primary = { background: "var(--accent)", color: "var(--accent-ink)" } as const;

export function Card({ title, note, children, right }: { title: string; note?: string; children: React.ReactNode; right?: React.ReactNode }) {
  return (
    <section className="rounded-xl p-4" style={card}>
      <div className="mb-2 flex flex-wrap items-start justify-between gap-2">
        <div className="min-w-0">
          <h2 className="m-0 text-[14px] font-bold">{title}</h2>
          {note && <p className="m-0 mt-0.5 text-[11.5px] text-ink-faint">{note}</p>}
        </div>
        {right}
      </div>
      {children}
    </section>
  );
}

export function Loading({ error }: { error?: string | null }) {
  if (error) return <div className="text-[12.5px]" style={{ color: "var(--reject)" }}>{error}</div>;
  return <div className="text-[12.5px] text-ink-dim">Loading…</div>;
}

export function Field({ label, value, note }: { label: string; value: string; note?: string }) {
  const [copied, setCopied] = useState(false);
  const copy = () => { navigator.clipboard?.writeText(value).then(() => { setCopied(true); setTimeout(() => setCopied(false), 1500); }).catch(() => {}); };
  return (
    <div className="mb-2">
      <div className="text-[11px] font-semibold text-ink-faint">{label}{note ? ` · ${note}` : ""}</div>
      <div className="flex items-center gap-2">
        {/* Wraps rather than truncating: on a phone you need the whole address to type it in. */}
        <code className="min-w-0 flex-1 break-all rounded-lg px-2.5 py-1.5 text-[12.5px]" style={{ background: "var(--panel-2)", border: "1px solid var(--line)" }}>{value}</code>
        <button onClick={copy} className="flex-none rounded-lg px-2.5 py-1.5 text-[11.5px] font-semibold" style={ghost}>{copied ? "Copied" : "Copy"}</button>
      </div>
    </div>
  );
}

// The addresses to type into an app. The home one only means something on the home
// network, so from outside only the public address is shown.
export function addresses(a: { public_url: string; host_port: string }, external: boolean): { label: string; value: string; note?: string }[] {
  const out: { label: string; value: string; note?: string }[] = [];
  if (!external) out.push({ label: "Server address", value: `http://${window.location.hostname}:${a.host_port}`, note: a.public_url ? "At home" : undefined });
  if (a.public_url) out.push({ label: "Server address", value: a.public_url, note: external ? undefined : "Away from home" });
  return out;
}

// DailyBars draws one bar per day from `since` for `days` days.
export function DailyBars({ since, days, perDay }: { since: string; days: number; perDay: Map<string, number> }) {
  const list = useMemo(() => {
    const [y, m, d] = since.split("-").map(Number);
    return Array.from({ length: days }, (_, i) => {
      const dt = new Date(y, m - 1, d + i);
      const key = `${dt.getFullYear()}-${String(dt.getMonth() + 1).padStart(2, "0")}-${String(dt.getDate()).padStart(2, "0")}`;
      return { key, dt, secs: perDay.get(key) ?? 0 };
    });
  }, [since, days, perDay]);
  const max = Math.max(...list.map((x) => x.secs), 0);
  if (max === 0) return <div className="py-6 text-center text-[12px] text-ink-faint">No listening in this period.</div>;
  const label = (dt: Date) => dt.toLocaleDateString([], { day: "numeric", month: "short" });
  return (
    <div>
      <div className="mb-1 text-right text-[10.5px] text-ink-faint">top {fmtHours(max)}</div>
      {/* 1px gaps on a phone: 90 days of 3px gaps alone outgrow a 320px screen's card. */}
      <div className="flex h-[110px] items-end gap-px sm:gap-[3px]">
        {list.map((x) => (
          <div key={x.key} className="group relative flex h-full flex-1 items-end" title={`${x.dt.toLocaleDateString([], { weekday: "short", day: "numeric", month: "short" })} · ${fmtHours(x.secs)}`}>
            <div className="w-full rounded-t-[3px]" style={{ height: x.secs > 0 ? `${Math.max(4, (x.secs / max) * 100)}%` : "2px", background: x.secs > 0 ? "var(--accent)" : "var(--line)", opacity: x.secs > 0 ? 0.9 : 1 }} />
          </div>
        ))}
      </div>
      <div className="mt-1 flex justify-between text-[10.5px] text-ink-faint">
        <span>{label(list[0].dt)}</span>
        <span>Today</span>
      </div>
    </div>
  );
}

export function Totals({ t }: { t?: { today: number; week: number; month: number; all_time: number } }) {
  const cells = [
    ["Today", t?.today ?? 0],
    ["7 days", t?.week ?? 0],
    ["30 days", t?.month ?? 0],
    ["All time", t?.all_time ?? 0],
  ] as const;
  return (
    <div className="mb-3 grid grid-cols-2 gap-2 sm:grid-cols-4">
      {cells.map(([k, v]) => (
        <div key={k} className="rounded-lg px-3 py-2" style={{ background: "var(--panel-2)", border: "1px solid var(--line-soft)" }}>
          <div className="text-[10.5px] font-semibold uppercase tracking-wide text-ink-faint">{k}</div>
          <div className="text-[16px] font-bold">{fmtHours(v)}</div>
        </div>
      ))}
    </div>
  );
}

export function Sessions({ list, showUser }: { list: AudioListening["sessions"]; showUser?: boolean }) {
  if (list.length === 0) return <div className="text-[12px] text-ink-faint">No listening sessions in this period.</div>;
  return (
    <div className="thin-scroll max-h-[320px] overflow-y-auto">
      {list.map((s, i) => (
        <div key={i} className="flex items-center justify-between gap-3 py-1.5 text-[12.5px]" style={{ borderTop: i ? "1px solid var(--line-soft)" : undefined }}>
          <span className="min-w-0 truncate">
            {showUser && <b>{s.username} · </b>}
            <span className={showUser ? "text-ink-faint" : ""}>{s.device || s.client || "App"}</span>
          </span>
          <span className="flex-none text-ink-dim">{fmtWhen(s.started_at)} · {fmtHours(s.seconds)}</span>
        </div>
      ))}
    </div>
  );
}

export function DaysPicker({ days, setDays }: { days: number; setDays: (d: number) => void }) {
  return (
    <select value={days} onChange={(e) => setDays(Number(e.target.value))} className="rounded-lg px-2 py-1 text-[12px]" style={inputStyle}>
      <option value={14}>14 days</option><option value={30}>30 days</option><option value={90}>90 days</option>
    </select>
  );
}
