import { useEffect, useRef, useState } from "react";
import { ApiError, api, type SystemTask } from "../../lib/api";
import { ago, every, took, until } from "../../lib/taskTime";
import { useVisiblePoll } from "../../lib/useVisiblePoll";

// TasksTable: every scheduled background job — how often it runs, when it last ran and
// how that went, when it runs next — with Run now. It re-reads every 10 s while the tab is
// visible, so a run started here flips to Running and then OK or Failed on its own.
// Self-contained so the Settings hub can mount it as is. On a server without the tasks
// API it says so instead of failing.

const rowBtn = "flex-none rounded-md px-2.5 py-1 text-[11px] font-semibold disabled:opacity-40";
// Columns on a wide screen; below 640px each task is a card.
const grid = "sm:grid sm:grid-cols-[minmax(0,1fr)_76px_minmax(0,128px)_84px_72px_auto] sm:items-center sm:gap-x-3";

type Result = { label: string; color: string; border: string };
const RESULT: Record<"running" | "failed" | "ok" | "never", Result> = {
  running: { label: "Running", color: "var(--accent)", border: "var(--accent-line)" },
  failed: { label: "Failed", color: "var(--reject)", border: "var(--reject)" },
  ok: { label: "OK", color: "var(--good)", border: "var(--good)" },
  never: { label: "Not run yet", color: "var(--ink-faint)", border: "var(--line)" },
};

function resultOf(t: SystemTask, starting: boolean): keyof typeof RESULT {
  if (t.running || starting) return "running";
  if (t.last_error) return "failed";
  return t.runs > 0 ? "ok" : "never";
}

const nameOf = (t: SystemTask) => t.label || t.name;

export function TasksTable() {
  const [tasks, setTasks] = useState<SystemTask[] | null>(null);
  const [unavailable, setUnavailable] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  const [msg, setMsg] = useState<{ ok: boolean; text: string } | null>(null);
  const [starting, setStarting] = useState<Set<string>>(new Set());
  const [openErr, setOpenErr] = useState<string | null>(null);
  const [, setTick] = useState(0); // keeps "20s ago" moving between loads
  const alive = useRef(true);
  const timers = useRef<number[]>([]);

  useEffect(() => {
    alive.current = true;
    const t = timers.current;
    return () => { alive.current = false; t.forEach((id) => window.clearTimeout(id)); };
  }, []);

  const load = () =>
    api.systemTasks()
      .then((ts) => { if (alive.current) { setTasks(ts); setUnavailable(false); setErr(null); } })
      .catch((e: Error) => {
        if (!alive.current) return;
        // This build's server may not have the tasks API yet.
        if (e instanceof ApiError && (e.status === 404 || e.status === 405)) setUnavailable(true);
        else setErr(e.message);
      });

  useVisiblePoll(() => { setTick((n) => n + 1); load(); }, 10_000);

  const runNow = async (t: SystemTask) => {
    setMsg(null);
    setStarting((s) => new Set(s).add(t.name));
    try {
      await api.runSystemTask(t.name);
      setMsg({ ok: true, text: `Started “${nameOf(t)}”.` });
    } catch (e) {
      if (e instanceof ApiError && e.status === 409) setMsg({ ok: false, text: `“${nameOf(t)}” is already running.` });
      else setMsg({ ok: false, text: (e as Error).message });
    } finally {
      await load();
      setStarting((s) => { const n = new Set(s); n.delete(t.name); return n; });
      // A short task is done before the next 10 s poll; look again soon so its result shows.
      timers.current.push(window.setTimeout(load, 2000));
    }
  };

  if (unavailable) {
    return <p className="m-0 text-[12px] text-ink-dim">Task history isn't available yet.</p>;
  }

  return (
    <div className="flex flex-col gap-3">
      {err && <p className="m-0 text-[12px]" style={{ color: "var(--reject)" }}>Couldn't load the tasks: {err}</p>}
      {msg && <p role="status" className="m-0 text-[12px]" style={{ color: msg.ok ? "var(--good)" : "var(--reject)" }}>{msg.text}</p>}
      <div className="rounded-lg" style={{ border: "1px solid var(--line)" }}>
        {tasks === null ? (
          <div className="p-4 text-center text-[12px] text-ink-dim">{err ? "Couldn't load the tasks." : "Loading…"}</div>
        ) : tasks.length === 0 ? (
          <div className="p-4 text-center text-[12px] text-ink-dim">No scheduled tasks.</div>
        ) : (
          <>
            <div className={`hidden px-3 py-2 font-mono text-[9.5px] font-bold uppercase tracking-[0.1em] text-ink-faint ${grid}`} style={{ borderBottom: "1px solid var(--line-soft)" }}>
              <span>Task</span><span>Every</span><span>Last run</span><span>Result</span><span>Next</span><span />
            </div>
            {tasks.map((t, i) => {
              const r = resultOf(t, starting.has(t.name));
              const res = RESULT[r];
              const running = r === "running";
              const showErr = openErr === t.name && !!t.last_error;
              return (
                <div key={t.name} className="px-3 py-2.5" style={{ borderTop: i === 0 ? undefined : "1px solid var(--line-soft)" }}>
                  <div className={`flex flex-col gap-1.5 ${grid}`}>
                    <div className="min-w-0">
                      <div className="truncate text-[12.5px] font-semibold" title={t.description || t.name}>{nameOf(t)}</div>
                      {t.label && t.label !== t.name && <div className="truncate font-mono text-[10px] text-ink-faint">{t.name}</div>}
                    </div>
                    <Cell label="Every">{every(t.interval_seconds)}</Cell>
                    <Cell label="Last run">
                      {ago(t.last_start)}
                      {t.last_duration_ms > 0 && <span className="text-ink-faint"> · {took(t.last_duration_ms)}</span>}
                    </Cell>
                    <Cell label="Result">
                      {r === "failed" ? (
                        <button
                          onClick={() => setOpenErr(showErr ? null : t.name)}
                          title={t.last_error}
                          aria-expanded={showErr}
                          className="rounded px-1.5 py-0.5 font-mono text-[9.5px] font-bold uppercase tracking-[0.06em]"
                          style={{ color: res.color, border: `1px solid ${res.border}` }}
                        >
                          {res.label}{t.consecutive_failures && t.consecutive_failures > 1 ? ` ×${t.consecutive_failures}` : ""}
                        </button>
                      ) : (
                        <span className="rounded px-1.5 py-0.5 font-mono text-[9.5px] font-bold uppercase tracking-[0.06em]" style={{ color: res.color, border: `1px solid ${res.border}` }}>{res.label}</span>
                      )}
                    </Cell>
                    <Cell label="Next">{running ? "—" : until(t.next_run)}</Cell>
                    <div className="sm:text-right">
                      <button
                        onClick={() => runNow(t)}
                        disabled={running}
                        className={rowBtn}
                        style={{ border: "1px solid var(--accent-line)", color: "var(--accent)" }}
                      >
                        Run now
                      </button>
                    </div>
                  </div>
                  {showErr && (
                    <div className="mt-2 break-words rounded-md p-2 font-mono text-[11px]" style={{ background: "var(--reject-soft)", color: "var(--reject)" }}>{t.last_error}</div>
                  )}
                </div>
              );
            })}
          </>
        )}
      </div>
    </div>
  );
}

// Cell is one value; on a phone (no column headers) it carries its own label.
function Cell({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <div className="flex min-w-0 items-baseline gap-2 text-[12px] text-ink-dim">
      <span className="w-16 flex-none font-mono text-[9.5px] font-bold uppercase tracking-[0.1em] text-ink-faint sm:hidden">{label}</span>
      <span className="min-w-0 truncate">{children}</span>
    </div>
  );
}
