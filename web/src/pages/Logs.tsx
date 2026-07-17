import { useCallback, useEffect, useRef, useState } from "react";
import { PageHeader } from "../components/PageHeader";
import { api, type LogEntry } from "../lib/api";

const LEVELS = ["debug", "info", "warn", "error"] as const;
const LEVEL_STYLE: Record<string, { color: string; label: string }> = {
  DEBUG: { color: "var(--ink-faint)", label: "DBG" },
  INFO: { color: "var(--accent)", label: "INF" },
  WARN: { color: "var(--avoid)", label: "WRN" },
  ERROR: { color: "var(--reject)", label: "ERR" },
};

function fmtTime(ms: number): string {
  const d = new Date(ms);
  const p = (n: number) => String(n).padStart(2, "0");
  return `${p(d.getHours())}:${p(d.getMinutes())}:${p(d.getSeconds())}`;
}

// Logs — a live view of Arrmada's application log (also on stdout/docker logs). Filter by
// level and search text; auto-refreshes so you can watch things happen.
export function Logs() {
  const [entries, setEntries] = useState<LogEntry[] | null>(null);
  const [level, setLevel] = useState("info");
  const [q, setQ] = useState("");
  const [auto, setAuto] = useState(true);
  const [err, setErr] = useState<string | null>(null);
  const scroller = useRef<HTMLDivElement>(null);
  const pinned = useRef(true);

  const load = useCallback(() => {
    api.logs({ limit: 2000, level, q })
      .then((e) => { setEntries(e); setErr(null); })
      .catch((e: Error) => setErr(e.message));
  }, [level, q]);

  useEffect(() => { load(); }, [load]);
  useEffect(() => {
    if (!auto) return;
    const id = window.setInterval(load, 3000);
    return () => window.clearInterval(id);
  }, [auto, load]);

  // Keep the view pinned to the bottom (newest) unless the user scrolls up.
  useEffect(() => {
    const el = scroller.current;
    if (el && pinned.current) el.scrollTop = el.scrollHeight;
  }, [entries]);
  const onScroll = () => {
    const el = scroller.current;
    if (el) pinned.current = el.scrollHeight - el.scrollTop - el.clientHeight < 40;
  };

  const copyAll = () => {
    const text = (entries ?? []).map((e) => `${fmtTime(e.time_ms)} ${e.level} ${e.msg}${e.attrs ? " " + e.attrs : ""}`).join("\n");
    navigator.clipboard?.writeText(text);
  };

  return (
    <>
      <PageHeader title="Logs" crumb="System / Logs" />
      <div className="mx-auto flex max-w-[1100px] flex-col gap-3 px-4 py-5">
        <div className="flex flex-wrap items-center gap-2">
          <div className="flex overflow-hidden rounded-lg" style={{ border: "1px solid var(--line)" }}>
            {LEVELS.map((l) => (
              <button key={l} onClick={() => setLevel(l)} className="px-3 py-1.5 text-[11.5px] font-semibold capitalize" style={{ background: level === l ? "var(--accent-soft)" : "transparent", color: level === l ? "var(--accent)" : "var(--ink-dim)" }}>{l}</button>
            ))}
          </div>
          <input value={q} onChange={(e) => setQ(e.target.value)} placeholder="Filter text (message or fields)…" className="min-w-[220px] flex-1 rounded-lg px-3 py-1.5 text-[12.5px]" style={{ background: "var(--panel-2)", border: "1px solid var(--line)", color: "var(--ink)" }} />
          <button onClick={() => setAuto((v) => !v)} className="rounded-lg px-3 py-1.5 text-[11.5px] font-semibold" style={{ border: `1px solid ${auto ? "var(--accent-line)" : "var(--line)"}`, color: auto ? "var(--accent)" : "var(--ink-dim)" }}>{auto ? "Live ●" : "Paused"}</button>
          <button onClick={load} className="rounded-lg px-3 py-1.5 text-[11.5px] font-semibold" style={{ border: "1px solid var(--line)", color: "var(--ink)" }}>Refresh</button>
          <button onClick={copyAll} className="rounded-lg px-3 py-1.5 text-[11.5px] font-semibold" style={{ border: "1px solid var(--line)", color: "var(--ink)" }}>Copy</button>
        </div>

        {err && <div className="text-[12px]" style={{ color: "var(--reject)" }}>{err}</div>}

        <div ref={scroller} onScroll={onScroll} className="thin-scroll overflow-auto rounded-xl p-2 font-mono text-[11.5px] leading-[1.5]" style={{ background: "var(--panel)", border: "1px solid var(--line)", height: "calc(100vh - 220px)" }}>
          {entries === null ? (
            <div className="p-6 text-center text-ink-dim">Loading…</div>
          ) : entries.length === 0 ? (
            <div className="p-6 text-center text-ink-dim">No log lines match.</div>
          ) : (
            entries.map((e, i) => {
              const ls = LEVEL_STYLE[e.level] ?? LEVEL_STYLE.INFO;
              return (
                <div key={i} className="flex gap-2 whitespace-pre-wrap px-1 py-0.5" style={{ borderTop: i === 0 ? "none" : "1px solid var(--line-soft)" }}>
                  <span className="flex-none text-ink-faint">{fmtTime(e.time_ms)}</span>
                  <span className="w-8 flex-none font-bold" style={{ color: ls.color }}>{ls.label}</span>
                  <span className="min-w-0 flex-1 break-words">
                    <span style={{ color: "var(--ink)" }}>{e.msg}</span>
                    {e.attrs && <span className="text-ink-faint">  {e.attrs}</span>}
                  </span>
                </div>
              );
            })
          )}
        </div>
        <p className="text-[10.5px] text-ink-faint">Showing the most recent {entries?.length ?? 0} lines (kept in memory, up to 5000). Same stream as the container logs.</p>
      </div>
    </>
  );
}
