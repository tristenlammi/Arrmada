import { useEffect, useLayoutEffect, useMemo, useRef, useState, type CSSProperties, type ReactNode } from "react";
import { Link, useSearchParams } from "react-router-dom";
import { PageHeader } from "../components/PageHeader";
import { api, type CalendarItem } from "../lib/api";
import { AGENDA_WEEKS, agendaRange, dayLabel, groupByDate, itemHref, itemLine, itemStatus, monthCells, shortDay, ymd } from "../lib/calendar";
import { posterThumb } from "../lib/img";
import { isStaff, useMe } from "../lib/me";
import { usePersisted } from "../lib/persist";
import { pickTab } from "../lib/useTabParam";
import { Button, ErrorState, Sheet, StaleBanner, StatusChip, type Tone } from "../ui";

// Calendar — upcoming episodes and movie releases. Visible to everyone (staff and
// requesters). Two views: a month grid (the desktop default) and an agenda list (the
// default on a phone, where a 7-column month leaves each day ~45px wide). Items open their
// title: staff go to the library page, requesters to the title's Discover page. 'My
// requests' narrows it to what the viewer asked for or follows (the requester's default).
const MONTHS = ["January", "February", "March", "April", "May", "June", "July", "August", "September", "October", "November", "December"];
const DOW = ["Sun", "Mon", "Tue", "Wed", "Thu", "Fri", "Sat"];

const VIEWS = ["month", "agenda"] as const;
type View = (typeof VIEWS)[number];
const VIEW_KEY = "calendar.view";
const PHONE = "(max-width: 639px)";

// The view a visit opens on when the address doesn't say: the last one picked on this
// device, else Agenda on a phone-width screen and Month on anything wider.
function initialView(): View {
  try {
    const v = localStorage.getItem(VIEW_KEY);
    if (v === "month" || v === "agenda") return v;
  } catch { /* storage blocked */ }
  try { return window.matchMedia(PHONE).matches ? "agenda" : "month"; } catch { return "month"; }
}

const SCOPES = ["mine", "all"] as const;
type Scope = (typeof SCOPES)[number];

const STATUS_TONE: Record<ReturnType<typeof itemStatus>, Tone> = { Downloaded: "good", Upcoming: "accent", Wanted: "avoid", Unmonitored: "faint" };

interface Loaded { key: string; start: string; end: string; items: CalendarItem[] }

export function Calendar({ chrome = true }: { chrome?: boolean }) {
  const { user } = useMe();
  const staff = isStaff(user);
  const today = useMemo(() => new Date(), []);
  const todayStr = ymd(today);

  const [params, setParams] = useSearchParams();
  const [fallbackView] = useState(initialView);
  const view = pickTab(params.get("view"), VIEWS, fallbackView);
  const setView = (v: View) => {
    try { localStorage.setItem(VIEW_KEY, v); } catch { /* ignore */ }
    // Replace, not push: the view is a display choice, not a place Back should step through.
    setParams((p) => { const n = new URLSearchParams(p); n.set("view", v); return n; }, { replace: true });
  };

  // 'My requests' or 'Everything': requesters start on their own, staff on everything.
  // Remembered per person, so a shared tablet doesn't hand the kids the owner's choice.
  const [scope, setScope] = usePersisted<Scope>(`calendar.scope.${user?.id ?? 0}`, staff ? "all" : "mine", SCOPES);
  const mine = scope === "mine";

  const [cursor, setCursor] = useState(() => new Date(today.getFullYear(), today.getMonth(), 1));
  const cells = useMemo(() => monthCells(cursor), [cursor]);
  const [more, setMore] = useState(0); // agenda 'Show more' presses
  const range = view === "month" ? { start: ymd(cells[0]), end: ymd(cells[cells.length - 1]) } : agendaRange(today, more);
  const rangeKey = `${range.start}|${range.end}`;

  // Fetch race: paging months quickly sends several requests, and an older, slower one
  // must never land after a newer one. Each request is aborted when the range moves on,
  // and an answer only counts if it belongs to the newest request (seq).
  const [loaded, setLoaded] = useState<Loaded | null>(null);
  const [failed, setFailed] = useState<{ key: string; message: string } | null>(null);
  const [attempt, setAttempt] = useState(0);
  const seq = useRef(0);
  useEffect(() => {
    const my = ++seq.current;
    const ctl = new AbortController();
    const { start, end } = range;
    api.calendar(start, end, ctl.signal).then(
      (r) => { if (seq.current === my) setLoaded({ key: rangeKey, start, end, items: r.items ?? [] }); },
      (e: unknown) => { if (seq.current === my && !ctl.signal.aborted) setFailed({ key: rangeKey, message: (e as Error).message }); },
    );
    return () => ctl.abort();
    // range is derived from rangeKey.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [rangeKey, attempt]);

  // Only items for what's on screen. The month grid shows a month's answer and nothing
  // else (never the previous month's while the next loads). The agenda keeps showing what
  // it has while 'Show more' loads the longer window: the same start, so still correct.
  const items = useMemo(() => {
    if (!loaded) return null;
    if (loaded.key === rangeKey) return loaded.items;
    if (view === "agenda" && loaded.start === range.start) return loaded.items.filter((it) => it.date <= range.end);
    return null;
  }, [loaded, rangeKey, view, range.start, range.end]);
  // The filter is applied here rather than by the server (?mine=1), so switching is instant.
  const shown = useMemo(() => (items && mine ? items.filter((it) => it.requested_by_me) : items), [items, mine]);
  const loading = loaded?.key !== rangeKey && failed?.key !== rangeKey;
  const error = failed?.key === rangeKey && loaded?.key !== rangeKey ? failed.message : null;
  const retry = () => { setFailed(null); setAttempt((n) => n + 1); };

  const [daySheet, setDaySheet] = useState<string | null>(null);

  // The staff console's page header sticks to the top; the agenda's date headers stick
  // just below it, so measure it.
  const head = useRef<HTMLDivElement>(null);
  const [headH, setHeadH] = useState(0);
  useLayoutEffect(() => {
    const el = head.current;
    if (!el) return;
    const measure = () => setHeadH(el.offsetHeight);
    measure();
    if (typeof ResizeObserver === "undefined") return;
    const ro = new ResizeObserver(measure);
    ro.observe(el);
    return () => ro.disconnect();
  }, [chrome]);

  const monthIdx = cursor.getMonth();

  return (
    <>
      {chrome && <div ref={head} className="sticky top-0 z-30"><PageHeader title="Calendar" /></div>}
      <div className="mx-auto w-full max-w-[1200px] px-4 py-6 sm:px-6">
        <div className="mb-4 flex flex-wrap items-center justify-between gap-3">
          <h2 className="m-0 text-[18px] font-bold">{view === "month" ? `${MONTHS[monthIdx]} ${cursor.getFullYear()}` : "Coming up"}</h2>
          {/* Wraps on a narrow phone rather than pushing the page sideways. */}
          <div className="flex flex-wrap items-center gap-2">
            <Segmented label="Show" value={scope} onChange={setScope} options={[{ v: "mine", l: "My requests" }, { v: "all", l: "Everything" }]} />
            <Segmented label="View" value={view} onChange={setView} options={[{ v: "month", l: "Month" }, { v: "agenda", l: "Agenda" }]} />
            {view === "month" && (
              <>
                <div className="flex items-center gap-3 text-[11px]">
                  <Legend color="var(--accent)" label="Episodes" />
                  <Legend color="var(--good)" label="Movies" />
                </div>
                <span className="mx-1 h-5 w-px" style={{ background: "var(--line)" }} />
                <button onClick={() => setCursor(new Date(today.getFullYear(), today.getMonth(), 1))} className="rounded-lg px-3 py-1.5 text-[12px] font-semibold" style={NAV_BTN}>Today</button>
                <button onClick={() => setCursor((c) => new Date(c.getFullYear(), c.getMonth() - 1, 1))} className="grid h-8 w-8 place-items-center rounded-lg" style={NAV_BTN} aria-label="Previous month">‹</button>
                <button onClick={() => setCursor((c) => new Date(c.getFullYear(), c.getMonth() + 1, 1))} className="grid h-8 w-8 place-items-center rounded-lg" style={NAV_BTN} aria-label="Next month">›</button>
              </>
            )}
          </div>
        </div>

        {error && !items ? (
          <ErrorState what="the calendar" message={error} onRetry={retry} />
        ) : view === "month" ? (
          <>
            <MonthGrid cells={cells} monthIdx={monthIdx} todayStr={todayStr} items={shown ?? []} staff={staff} onMore={setDaySheet} />
            {loading && <div className="mt-3 text-center text-[11.5px] text-ink-faint">Loading…</div>}
            {!loading && shown?.length === 0 && (mine
              ? <MineEmpty onShowAll={() => setScope("all")} />
              : <div className="mt-4 rounded-xl p-8 text-center text-[12px] text-ink-faint" style={{ border: "1px dashed var(--line)" }}>Nothing scheduled this month. Upcoming episodes and movie releases from your library appear here.</div>)}
          </>
        ) : (
          <AgendaList
            items={shown} today={today} end={range.end} staff={staff} stickyTop={chrome ? headH : 0}
            loadingMore={loading && !!items} onMore={() => (error ? retry() : setMore((n) => n + 1))}
            error={error} markMine={!mine} empty={mine ? <MineEmpty onShowAll={() => setScope("all")} /> : null}
          />
        )}
      </div>

      {daySheet && (
        <Sheet onClose={() => setDaySheet(null)} title={dayLabel(daySheet, today)} size="md">
          <div className="px-3 pb-4 sm:px-0 sm:pb-0">
            {groupByDate(shown ?? []).find((g) => g.date === daySheet)?.items.map((it, i) => (
              <AgendaRow key={i} it={it} staff={staff} todayStr={todayStr} markMine={!mine} replace />
            ))}
          </div>
        </Sheet>
      )}
    </>
  );
}

// MineEmpty is 'My requests' with nothing in the window: say so plainly, and offer the
// whole schedule rather than a blank page that looks broken.
function MineEmpty({ onShowAll }: { onShowAll: () => void }) {
  return (
    <div className="mt-4 rounded-xl p-6 text-center text-[12.5px] text-ink-dim" style={{ border: "1px dashed var(--line)" }}>
      <p className="m-0">Nothing you asked for is airing in this window.</p>
      <Button className="mt-3" onClick={onShowAll}>Show everything</Button>
    </div>
  );
}

const NAV_BTN: CSSProperties = { border: "1px solid var(--line)", background: "var(--panel-2)", color: "var(--ink)" };

function Segmented<T extends string>({ label, value, options, onChange }: { label: string; value: T; options: { v: T; l: string }[]; onChange: (v: T) => void }) {
  return (
    <div role="group" aria-label={label} className="inline-flex overflow-hidden rounded-lg" style={{ border: "1px solid var(--line)", background: "var(--panel-2)" }}>
      {options.map((o) => {
        const on = o.v === value;
        return (
          <button
            key={o.v} type="button" aria-pressed={on} onClick={() => onChange(o.v)}
            className="min-h-8 px-3 py-1.5 text-[12px] font-semibold"
            style={{ background: on ? "var(--accent-soft)" : "transparent", color: on ? "var(--accent-text)" : "var(--ink-dim)" }}
          >
            {o.l}
          </button>
        );
      })}
    </div>
  );
}

function Legend({ color, label }: { color: string; label: string }) {
  return <span className="inline-flex items-center gap-1 text-ink-dim"><span className="inline-block h-2 w-2 rounded-full" style={{ background: color }} />{label}</span>;
}

function MonthGrid({ cells, monthIdx, todayStr, items, staff, onMore }: {
  cells: Date[]; monthIdx: number; todayStr: string; items: CalendarItem[]; staff: boolean; onMore: (date: string) => void;
}) {
  const byDate = useMemo(() => {
    const m: Record<string, CalendarItem[]> = {};
    for (const it of items) (m[it.date] ??= []).push(it);
    return m;
  }, [items]);
  return (
    <>
      {/* Weekday header */}
      <div className="grid grid-cols-7 gap-1.5">
        {DOW.map((d) => <div key={d} className="pb-1 text-center font-mono text-[11px] font-bold uppercase tracking-wide text-ink-faint">{d}</div>)}
      </div>

      {/* Day grid */}
      <div className="grid grid-cols-7 gap-1.5">
        {cells.map((d) => {
          const key = ymd(d);
          const inMonth = d.getMonth() === monthIdx;
          const isToday = key === todayStr;
          const dayItems = byDate[key] ?? [];
          return (
            <div key={key} className="flex min-h-[92px] min-w-0 flex-col gap-1 rounded-lg p-1.5" style={{ border: `1px solid ${isToday ? "var(--accent)" : "var(--line)"}`, background: inMonth ? "var(--panel)" : "transparent", opacity: inMonth ? 1 : 0.45 }}>
              <div className="flex items-center justify-between px-0.5">
                <span className="text-[11px] font-semibold" style={{ color: isToday ? "var(--accent)" : "var(--ink-dim)" }}>{d.getDate()}</span>
              </div>
              <div className="flex flex-col gap-1">
                {dayItems.slice(0, 3).map((it, i) => <DayItem key={i} it={it} staff={staff} />)}
                {dayItems.length > 3 && (
                  <button type="button" onClick={() => onMore(key)} className="self-start rounded px-1 text-left text-[10px] font-semibold text-ink-dim" aria-label={`+${dayItems.length - 3} more on ${shortDay(key)}`}>
                    +{dayItems.length - 3} more
                  </button>
                )}
              </div>
            </div>
          );
        })}
      </div>
    </>
  );
}

function DayItem({ it, staff }: { it: CalendarItem; staff: boolean }) {
  const color = it.type === "movie" ? "var(--good)" : "var(--accent)";
  const label = `${it.title} — ${itemLine(it)}`;
  const body = (
    <div className="flex items-center gap-1 overflow-hidden rounded px-1 py-0.5 text-[10px]" style={{ background: it.has_file ? "var(--panel-2)" : "color-mix(in srgb, " + color + " 14%, transparent)", opacity: it.monitored || it.has_file ? 1 : 0.65 }} title={label}>
      <span className="h-2.5 w-[3px] flex-none rounded-full" style={{ background: color }} />
      <span className="truncate" style={{ color: "var(--ink)" }}>{it.title}</span>
      {it.has_file && <span className="flex-none" style={{ color: "var(--good)" }}>✓</span>}
    </div>
  );
  const href = itemHref(it, staff);
  if (!href) return body;
  return <Link to={href} className="block" aria-label={label}>{body}</Link>;
}

// AgendaList is the phone's calendar: one section per day that has something (and always
// Today, so there's somewhere to land), sticky date headers, opening scrolled to Today.
function AgendaList({ items, today, end, staff, stickyTop, loadingMore, onMore, error, markMine, empty }: {
  items: CalendarItem[] | null; today: Date; end: string; staff: boolean; stickyTop: number; loadingMore: boolean; onMore: () => void;
  error: string | null; markMine: boolean; empty: ReactNode;
}) {
  const todayStr = ymd(today);
  const days = useMemo(() => {
    const groups = groupByDate(items ?? []);
    if (!groups.some((g) => g.date === todayStr)) groups.push({ date: todayStr, items: [] });
    return groups.sort((a, b) => (a.date < b.date ? -1 : a.date > b.date ? 1 : 0));
  }, [items, todayStr]);

  // Land on Today once, when the first answer arrives; not again after 'Show more'.
  const todayRef = useRef<HTMLElement>(null);
  const landed = useRef(false);
  const ready = items !== null;
  useEffect(() => {
    if (!ready || landed.current) return;
    landed.current = true;
    todayRef.current?.scrollIntoView({ block: "start" });
  }, [ready]);

  if (!items) return <div className="mt-3 text-center text-[11.5px] text-ink-faint">Loading…</div>;
  const untilLabel = dayLabel(end, today);
  return (
    <div>
      {days.map((g) => {
        const label = dayLabel(g.date, today);
        return (
        <section key={g.date} ref={g.date === todayStr ? todayRef : undefined} aria-label={label} style={{ scrollMarginTop: stickyTop }}>
          <h3 className="sticky z-10 m-0 py-2 text-[12px] font-bold uppercase tracking-wide" style={{ top: stickyTop, background: "var(--bg)", color: g.date === todayStr ? "var(--accent-text)" : "var(--ink-dim)" }}>
            {label}
            {/* Yesterday/Today/Tomorrow still say which day they are. */}
            {label !== shortDay(g.date) && <span className="ml-2 font-normal normal-case text-ink-faint">{shortDay(g.date)}</span>}
          </h3>
          {g.items.length === 0 ? (
            <div className="px-2 pb-3 text-[12px] text-ink-faint">Nothing today.</div>
          ) : (
            <div className="mb-2 flex flex-col">
              {g.items.map((it, i) => <AgendaRow key={i} it={it} staff={staff} todayStr={todayStr} markMine={markMine} />)}
            </div>
          )}
        </section>
        );
      })}
      {items.length === 0 && (empty ?? (
        <div className="mt-2 rounded-xl p-6 text-center text-[12px] text-ink-faint" style={{ border: "1px dashed var(--line)" }}>
          Nothing scheduled through {untilLabel}. Upcoming episodes and movie releases from the library appear here.
        </div>
      ))}
      {error && <div className="mt-3"><StaleBanner message={error} onRetry={onMore} /></div>}
      <div className="mt-4 flex justify-center">
        <Button onClick={onMore} busy={loadingMore} busyLabel="Loading…">Show {AGENDA_WEEKS} more weeks</Button>
      </div>
    </div>
  );
}

// AgendaRow is one item as a 44px+ tappable row: a small poster, the title, 'S02E05 ·
// Episode name' or 'Movie · 2026', and where it stands. Shared by the agenda and the day sheet.
// markMine adds 'You asked for this' under the title (in 'Everything', where it tells
// the viewer's titles apart; in 'My requests' every row would say it).
function AgendaRow({ it, staff, todayStr, markMine, replace }: { it: CalendarItem; staff: boolean; todayStr: string; markMine?: boolean; replace?: boolean }) {
  const status = itemStatus(it, todayStr);
  const href = itemHref(it, staff);
  const body = (
    <>
      <div className="h-10 w-[27px] flex-none overflow-hidden rounded" style={{ background: "var(--panel-2)" }}>
        {it.poster_url && <img src={posterThumb(it.poster_url)} alt="" loading="lazy" className="h-full w-full object-cover" />}
      </div>
      <div className="min-w-0 flex-1">
        <div className="truncate text-[13px] font-semibold" style={{ color: "var(--ink)" }}>{it.title}</div>
        <div className="truncate text-[11.5px] text-ink-dim">{itemLine(it)}</div>
        {markMine && it.requested_by_me && <StatusChip tone="accent" size="xs" className="mt-0.5 inline-block">You asked for this</StatusChip>}
      </div>
      <StatusChip tone={STATUS_TONE[status]} className="flex-none">{status === "Downloaded" ? "✓ Downloaded" : status}</StatusChip>
    </>
  );
  const className = "flex min-h-[44px] items-center gap-3 rounded-lg px-2 py-1.5";
  const style: CSSProperties = { opacity: status === "Unmonitored" ? 0.6 : 1 };
  if (!href) return <div className={className} style={style}>{body}</div>;
  // replace: from the day sheet, the title takes the sheet's place in history, so Back
  // from the title returns to the calendar rather than to a sheet that's gone.
  return <Link to={href} replace={replace} className={className} style={style}>{body}</Link>;
}
