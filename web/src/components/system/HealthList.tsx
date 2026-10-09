import { useEffect, useRef, useState } from "react";
import { Link } from "react-router-dom";
import { api, type HealthCheck, type SystemHealth } from "../../lib/api";
import { fixLink } from "../../lib/links";
import { ago } from "../../lib/taskTime";
import { useVisiblePoll } from "../../lib/useVisiblePoll";

// HealthList: every background health check, grouped by category, with its level, what's
// wrong and where to fix it. It reads the server's cached results (cheap), so it re-reads
// every 10 s while the tab is visible; "Check now" asks the server to run them all first.
// Self-contained so the Settings hub can mount it as is.

const CATEGORIES = ["Storage", "Downloads", "Indexers", "Integrations", "Tasks"];

const LEVEL: Record<HealthCheck["level"], { color: string; label: string }> = {
  ok: { color: "var(--good)", label: "OK" },
  warning: { color: "var(--avoid)", label: "Warning" },
  error: { color: "var(--reject)", label: "Error" },
  pending: { color: "var(--ink-faint)", label: "Not checked yet" },
};

const btn = "rounded-lg px-3.5 py-2 text-[12.5px] font-semibold disabled:opacity-50";
const btnStyle = { border: "1px solid var(--line)", background: "var(--panel-2)", color: "var(--ink)" } as const;

export function HealthList() {
  const [data, setData] = useState<SystemHealth | null>(null);
  const [err, setErr] = useState<string | null>(null);
  const [checking, setChecking] = useState(false);
  const [, setTick] = useState(0); // re-render so "checked 20s ago" keeps moving
  const alive = useRef(true);

  const load = (refresh = false) =>
    api.systemHealth(refresh)
      .then((d) => { if (alive.current) { setData(d); setErr(null); } })
      .catch((e: Error) => { if (alive.current) setErr(e.message); });

  useEffect(() => { alive.current = true; return () => { alive.current = false; }; }, []);
  useVisiblePoll(() => { setTick((t) => t + 1); load(); }, 10_000);

  const checkNow = async () => {
    setChecking(true);
    try { await load(true); } finally { setChecking(false); }
  };

  const checks = data?.checks ?? [];
  const groups = [...CATEGORIES, ...new Set(checks.map((c) => c.category).filter((c) => !CATEGORIES.includes(c)))]
    .map((cat) => ({ cat, items: checks.filter((c) => c.category === cat) }))
    .filter((g) => g.items.length > 0);
  const errors = checks.filter((c) => c.level === "error").length;
  const warnings = checks.filter((c) => c.level === "warning").length;

  return (
    <div className="flex flex-col gap-4">
      <div className="flex flex-wrap items-center gap-3">
        <button onClick={checkNow} disabled={checking} className={btn} style={btnStyle}>{checking ? "Checking…" : "Check now"}</button>
        {data && (
          <span className="text-[12px] text-ink-dim">
            {errors === 0 && warnings === 0
              ? "Everything checked is working."
              : [errors > 0 && `${errors} error${errors === 1 ? "" : "s"}`, warnings > 0 && `${warnings} warning${warnings === 1 ? "" : "s"}`].filter(Boolean).join(" · ")}
          </span>
        )}
      </div>
      {err && <p className="m-0 text-[12px]" style={{ color: "var(--reject)" }}>Couldn't load the health checks: {err}</p>}
      {data === null && !err && <p className="m-0 text-[12px] text-ink-dim">Loading…</p>}
      {data && checks.length === 0 && <p className="m-0 text-[12px] text-ink-dim">No checks have run yet. They start within half a minute of Arrmada starting.</p>}

      {groups.map((g) => (
        <div key={g.cat} className="flex flex-col gap-1.5">
          <div className="font-mono text-[9.5px] font-bold uppercase tracking-[0.1em] text-ink-faint">{g.cat}</div>
          <div className="rounded-lg" style={{ border: "1px solid var(--line)" }}>
            {g.items.map((c, i) => <CheckRow key={c.key} c={c} first={i === 0} />)}
          </div>
        </div>
      ))}
    </div>
  );
}

function CheckRow({ c, first }: { c: HealthCheck; first: boolean }) {
  const lv = LEVEL[c.level] ?? LEVEL.pending;
  const checked = c.level === "pending" ? "not checked yet" : `checked ${ago(c.checked_at)}${c.stale ? " · timed out" : ""}`;
  return (
    <div className="flex flex-col gap-1.5 px-3 py-2.5" style={{ borderTop: first ? undefined : "1px solid var(--line-soft)" }}>
      <div className="flex flex-wrap items-center gap-x-2.5 gap-y-0.5">
        <span className="h-2 w-2 flex-none rounded-full" style={{ background: lv.color }} role="img" aria-label={lv.label} title={lv.label} />
        <span className="min-w-0 flex-1 text-[12.5px] font-semibold">{c.name}</span>
        <span className="flex-none font-mono text-[10.5px] text-ink-faint">{checked}</span>
      </div>
      {c.findings.length === 0 ? (
        c.level === "ok" && <div className="pl-[18px] text-[11.5px] text-ink-faint">Working.</div>
      ) : (
        c.findings.map((f, i) => {
          const to = fixLink(f);
          return (
            <div key={f.key ?? i} className="flex flex-wrap items-baseline gap-x-3 gap-y-1 pl-[18px] text-[12px]">
              <span className="min-w-0 flex-1 break-words" style={{ color: f.level === "error" ? "var(--reject)" : "var(--ink-dim)" }}>{f.message}</span>
              {to && (
                <Link to={to} title={f.link_label} className="flex-none whitespace-nowrap font-semibold no-underline" style={{ color: "var(--accent)" }}>Fix →</Link>
              )}
            </div>
          );
        })
      )}
    </div>
  );
}
