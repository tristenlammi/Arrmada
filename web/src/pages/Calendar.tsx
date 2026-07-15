import { useEffect, useMemo, useState } from "react";
import { Link } from "react-router-dom";
import { PageHeader } from "../components/PageHeader";
import { api, type CalendarItem } from "../lib/api";

// Calendar — upcoming episodes and movie releases as a month grid. Visible to everyone
// (staff and requesters), under Services. Items link to their detail page only when the
// viewer has access (chrome = full app); requesters see the schedule without links.
const MONTHS = ["January", "February", "March", "April", "May", "June", "July", "August", "September", "October", "November", "December"];
const DOW = ["Sun", "Mon", "Tue", "Wed", "Thu", "Fri", "Sat"];

function ymd(d: Date): string {
  return `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, "0")}-${String(d.getDate()).padStart(2, "0")}`;
}

export function Calendar({ chrome = true }: { chrome?: boolean }) {
  const today = useMemo(() => new Date(), []);
  const [cursor, setCursor] = useState(() => new Date(today.getFullYear(), today.getMonth(), 1));
  const [items, setItems] = useState<CalendarItem[]>([]);
  const [loading, setLoading] = useState(true);

  // 6-week grid covering the visible month (leading/trailing days from adjacent months).
  const cells = useMemo(() => {
    const first = new Date(cursor.getFullYear(), cursor.getMonth(), 1);
    const gridStart = new Date(first);
    gridStart.setDate(1 - first.getDay());
    return Array.from({ length: 42 }, (_, i) => { const d = new Date(gridStart); d.setDate(gridStart.getDate() + i); return d; });
  }, [cursor]);

  useEffect(() => {
    setLoading(true);
    const start = ymd(cells[0]);
    const end = ymd(cells[cells.length - 1]);
    api.calendar(start, end).then((r) => setItems(r.items ?? [])).catch(() => setItems([])).finally(() => setLoading(false));
  }, [cells]);

  const byDate = useMemo(() => {
    const m: Record<string, CalendarItem[]> = {};
    for (const it of items) (m[it.date] ??= []).push(it);
    return m;
  }, [items]);

  const todayStr = ymd(today);
  const monthIdx = cursor.getMonth();

  return (
    <>
      {chrome && <PageHeader title="Calendar" crumb="Services / Calendar" />}
      <div className="mx-auto w-full max-w-[1200px] px-4 py-6 sm:px-6">
        <div className="mb-4 flex flex-wrap items-center justify-between gap-3">
          <div className="flex items-center gap-2">
            <h2 className="m-0 text-[18px] font-bold">{MONTHS[monthIdx]} {cursor.getFullYear()}</h2>
          </div>
          <div className="flex items-center gap-2">
            <div className="flex items-center gap-3 text-[11px]">
              <Legend color="var(--accent)" label="Episodes" />
              <Legend color="var(--good)" label="Movies" />
            </div>
            <span className="mx-1 h-5 w-px" style={{ background: "var(--line)" }} />
            <button onClick={() => setCursor(new Date(today.getFullYear(), today.getMonth(), 1))} className="rounded-lg px-3 py-1.5 text-[12px] font-semibold" style={{ border: "1px solid var(--line)", background: "var(--panel-2)", color: "var(--ink)" }}>Today</button>
            <button onClick={() => setCursor((c) => new Date(c.getFullYear(), c.getMonth() - 1, 1))} className="grid h-8 w-8 place-items-center rounded-lg" style={{ border: "1px solid var(--line)", background: "var(--panel-2)", color: "var(--ink)" }} aria-label="Previous month">‹</button>
            <button onClick={() => setCursor((c) => new Date(c.getFullYear(), c.getMonth() + 1, 1))} className="grid h-8 w-8 place-items-center rounded-lg" style={{ border: "1px solid var(--line)", background: "var(--panel-2)", color: "var(--ink)" }} aria-label="Next month">›</button>
          </div>
        </div>

        {/* Weekday header */}
        <div className="grid grid-cols-7 gap-1.5">
          {DOW.map((d) => <div key={d} className="pb-1 text-center font-mono text-[9.5px] font-bold uppercase tracking-wide text-ink-faint">{d}</div>)}
        </div>

        {/* Day grid */}
        <div className="grid grid-cols-7 gap-1.5">
          {cells.map((d) => {
            const key = ymd(d);
            const inMonth = d.getMonth() === monthIdx;
            const isToday = key === todayStr;
            const dayItems = byDate[key] ?? [];
            return (
              <div key={key} className="flex min-h-[92px] flex-col gap-1 rounded-lg p-1.5" style={{ border: `1px solid ${isToday ? "var(--accent)" : "var(--line)"}`, background: inMonth ? "var(--panel)" : "transparent", opacity: inMonth ? 1 : 0.45 }}>
                <div className="flex items-center justify-between px-0.5">
                  <span className="text-[11px] font-semibold" style={{ color: isToday ? "var(--accent)" : "var(--ink-dim)" }}>{d.getDate()}</span>
                </div>
                <div className="flex flex-col gap-1">
                  {dayItems.slice(0, 3).map((it, i) => <DayItem key={i} it={it} chrome={chrome} />)}
                  {dayItems.length > 3 && <span className="px-1 text-[9.5px] text-ink-faint">+{dayItems.length - 3} more</span>}
                </div>
              </div>
            );
          })}
        </div>

        {loading && <div className="mt-3 text-center text-[11.5px] text-ink-faint">Loading…</div>}
        {!loading && items.length === 0 && <div className="mt-4 rounded-xl p-8 text-center text-[12px] text-ink-faint" style={{ border: "1px dashed var(--line)" }}>Nothing scheduled this month. Upcoming episodes and movie releases from your library appear here.</div>}
      </div>
    </>
  );
}

function Legend({ color, label }: { color: string; label: string }) {
  return <span className="inline-flex items-center gap-1 text-ink-dim"><span className="inline-block h-2 w-2 rounded-full" style={{ background: color }} />{label}</span>;
}

function DayItem({ it, chrome }: { it: CalendarItem; chrome: boolean }) {
  const color = it.type === "movie" ? "var(--good)" : "var(--accent)";
  const label = `${it.title}${it.subtitle ? ` — ${it.subtitle}` : ""}`;
  const body = (
    <div className="flex items-center gap-1 overflow-hidden rounded px-1 py-0.5 text-[10px]" style={{ background: it.has_file ? "var(--panel-2)" : "color-mix(in srgb, " + color + " 14%, transparent)", opacity: it.monitored || it.has_file ? 1 : 0.65 }} title={label}>
      <span className="h-2.5 w-[3px] flex-none rounded-full" style={{ background: color }} />
      <span className="truncate" style={{ color: "var(--ink)" }}>{it.title}</span>
      {it.has_file && <span className="flex-none" style={{ color: "var(--good)" }}>✓</span>}
    </div>
  );
  // Detail pages are staff-only; requesters (chrome=false) get a non-linked schedule.
  if (!chrome) return body;
  return <Link to={it.type === "movie" ? `/movies/${it.ref_id}` : `/series/${it.ref_id}`} className="block">{body}</Link>;
}
