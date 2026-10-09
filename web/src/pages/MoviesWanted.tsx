import { useEffect, useMemo, useRef, useState, type ReactNode } from "react";
import { Link } from "react-router-dom";
import { PageHeader } from "../components/PageHeader";
import { MoviesSwitch } from "../components/MoviesSwitch";
import { api, type MoviesMissing, type MovieWantedRow, type WantedRow } from "../lib/api";
import { useQuery, type QueryResult } from "../lib/query";
import { useTabParam } from "../lib/useTabParam";
import { useLive } from "../lib/useLive";
import { posterThumb } from "../lib/img";
import { wantedChip, wantedLine } from "../lib/wanted";
import { attemptLine, nextTryText } from "../lib/searchOutcome";
import { queueLine, queuedNote, useMovieSearchQueue } from "../lib/movieQueue";
import { Button, EmptyState, ErrorState, Skeleton, StaleBanner, StatusChip, useConfirm, useToast } from "../ui";
import { TabPanel, Tabs } from "../ui/Tabs";

// Movies → Wanted: what Arrmada is still looking for among the films, why it hasn't found
// it, and when it tries next. The Wanted tab lists the films badged Wanted (the library's
// Wanted filter, same count); Cutoff unmet lists files that don't meet their profile's target and
// says whether the upgrade sweep will do anything about them. Every search here goes
// through the movie search queue, two at a time.

const TABS = ["missing", "cutoff"] as const;

// The events after which the lists may have changed: a search finished or moved in the
// queue, a grab, an import, a scan.
const REFRESH_TOPICS = ["search.finished", "movie.search.started", "movie.search.done", "release.grabbed", "movie.downloaded", "movie.file_deleted", "library.scanned", "movie.deleted"];

export function MoviesWanted() {
  const [tab, setTab] = useTabParam(TABS, "missing");
  const missing = useQuery("movies:wanted:missing", () => api.moviesMissing(), { staleMs: 0 });
  const cutoff = useQuery("movies:wanted:cutoff", () => api.moviesCutoff(), { staleMs: 0 });
  const { last } = useLive();
  const queue = useMovieSearchQueue(last);
  const queueText = queueLine(queue.running, queue.queued);

  // Refetch on anything that changes the lists, at most once a second: a Search all moves
  // dozens of rows through the queue.
  const timer = useRef<number | undefined>(undefined);
  const { refetch: refetchMissing } = missing;
  const { refetch: refetchCutoff } = cutoff;
  useEffect(() => {
    if (!last || !REFRESH_TOPICS.includes(last.topic)) return;
    if (timer.current !== undefined) return;
    timer.current = window.setTimeout(() => {
      timer.current = undefined;
      void refetchMissing();
      void refetchCutoff();
    }, 1000);
  }, [last, refetchMissing, refetchCutoff]);
  useEffect(() => () => window.clearTimeout(timer.current), []);

  const missingCount = (missing.data?.searching.length ?? 0) + (missing.data?.upcoming.length ?? 0);
  const cutoffCount = cutoff.data?.rows.length ?? 0;

  return (
    <>
      <PageHeader title="Wanted movies" />
      <div className="mx-auto w-full max-w-[1200px] px-4 py-6 sm:px-6">
        <div className="mb-4 flex flex-wrap items-center justify-between gap-3">
          <MoviesSwitch active="wanted" />
          {queueText && <StatusChip tone="accent" title="Movie searches run two at a time; the rest wait their turn.">{queueText}</StatusChip>}
        </div>
        <Tabs
          idPrefix="movies-wanted"
          label="Wanted movies"
          value={tab}
          onChange={setTab}
          tabs={[
            { key: "missing", label: "Wanted", count: missing.data ? missingCount : undefined },
            { key: "cutoff", label: "Cutoff unmet", count: cutoff.data ? cutoffCount : undefined },
          ]}
        />
        {tab === "missing" ? (
          <TabPanel idPrefix="movies-wanted" value="missing">
            <MissingTab q={missing} />
          </TabPanel>
        ) : (
          <TabPanel idPrefix="movies-wanted" value="cutoff">
            <CutoffTab q={cutoff} />
          </TabPanel>
        )}
      </div>
    </>
  );
}

type MissingQuery = QueryResult<MoviesMissing>;
type CutoffQuery = QueryResult<{ rows: MovieWantedRow[]; queue_known: boolean }>;

// searchable: rows a Search all should queue — not already queued, not waiting on a
// download or a review, and not while the client can't be read (it may be downloading).
function searchable(r: MovieWantedRow): boolean {
  return !r.queued && (r.state === "searching" || r.state === "slowed" || r.state === "indexers_failed");
}

function MissingTab({ q }: { q: MissingQuery }) {
  const confirm = useConfirm();
  const toast = useToast();
  const [busy, setBusy] = useState(false);
  const data = q.data;
  const toSearch = useMemo(() => [...(data?.searching ?? []), ...(data?.versions ?? [])].filter(searchable), [data]);

  if (!data) {
    return q.error ? <ErrorState what="the wanted movies" message={q.error.message} onRetry={q.refetch} busy={q.loading} /> : <Skeleton variant="table" />;
  }
  const searchAll = async () => {
    const n = toSearch.length;
    const ok = await confirm({
      title: `Search for ${n} ${n === 1 ? "movie" : "movies"}?`,
      body: "This queues the searches at the throttled rate — two run at a time, the rest wait their turn. Nothing is grabbed that your profiles wouldn't take.",
      confirmLabel: `Queue ${n} ${n === 1 ? "search" : "searches"}`,
    });
    if (!ok) return;
    setBusy(true);
    try {
      const r = await api.bulkMovieSearch(toSearch.map((row) => row.id), "missing");
      toast(`Queued ${r.queued} ${r.queued === 1 ? "search" : "searches"}${r.duplicates ? ` (${r.duplicates} already queued)` : ""} — they run two at a time.`, { tone: "good" });
      void q.refetch();
    } catch (e) {
      toast((e as Error).message, { tone: "error" });
    } finally {
      setBusy(false);
    }
  };
  const empty = data.searching.length === 0 && data.upcoming.length === 0 && data.versions.length === 0;
  return (
    <>
      {q.error && <StaleBanner message={q.error.message} onRetry={q.refetch} />}
      {!data.queue_known && (
        <div className="mb-3 rounded-lg px-3 py-2 text-[12px]" style={{ border: "1px solid var(--line)", background: "var(--panel)", color: "var(--ink-dim)" }}>
          The download client isn't answering, so some of these may already be downloading — automatic searches are paused until it's back.
        </div>
      )}
      {empty ? (
        <EmptyState title="Nothing missing" body="Every monitored movie has its file. New ones you add show here until Arrmada finds them." />
      ) : (
        <>
          <div className="mb-3 flex flex-wrap items-center justify-between gap-2">
            <span className="text-[12px] text-ink-dim">Monitored films without a file — the ones the library badges Wanted.</span>
            <Button size="sm" variant="primary" disabled={toSearch.length === 0} busy={busy} busyLabel="Queuing…" onClick={() => void searchAll()}>
              Search all ({toSearch.length})
            </Button>
          </div>
          <Rows rows={data.searching} onChange={q.refetch} />
          {data.upcoming.length > 0 && (
            <Section title={`Not released yet (${data.upcoming.length})`} note="Searched once they reach their minimum availability.">
              <Rows rows={data.upcoming} onChange={q.refetch} upcoming />
            </Section>
          )}
          {data.versions.length > 0 && (
            <Section title={`Extra versions still missing (${data.versions.length})`} note="The film has its file; a monitored extra version (a 4K copy, an edition) doesn't yet.">
              <Rows rows={data.versions} onChange={q.refetch} />
            </Section>
          )}
        </>
      )}
    </>
  );
}

function CutoffTab({ q }: { q: CutoffQuery }) {
  const confirm = useConfirm();
  const toast = useToast();
  const [busy, setBusy] = useState(false);
  const [showAll, setShowAll] = useState(false);
  const rows = q.data?.rows;
  const willUpgrade = useMemo(() => (rows ?? []).filter((r) => r.will_upgrade), [rows]);
  const wont = (rows?.length ?? 0) - willUpgrade.length;
  // One search per film, even when several of its tracks miss their target.
  const toSearch = useMemo(() => [...new Set(willUpgrade.filter((r) => !r.queued && r.state === "upgrading").map((r) => r.id))], [willUpgrade]);

  if (!rows) {
    return q.error ? <ErrorState what="the library's files" message={q.error.message} onRetry={q.refetch} busy={q.loading} /> : <Skeleton variant="table" />;
  }
  const searchAll = async () => {
    const n = toSearch.length;
    const ok = await confirm({
      title: `Look for upgrades for ${n} ${n === 1 ? "movie" : "movies"}?`,
      body: "This queues the searches at the throttled rate — two run at a time. Like the upgrade sweep, it grabs no more upgrades than your per-sweep limit (Settings → Downloads); the rest wait for the next sweep.",
      confirmLabel: `Queue ${n} ${n === 1 ? "search" : "searches"}`,
    });
    if (!ok) return;
    setBusy(true);
    try {
      const r = await api.bulkMovieSearch(toSearch, "upgrade");
      toast(`Queued ${r.queued} upgrade ${r.queued === 1 ? "search" : "searches"}${r.duplicates ? ` (${r.duplicates} already queued)` : ""}.`, { tone: "good" });
      void q.refetch();
    } catch (e) {
      toast((e as Error).message, { tone: "error" });
    } finally {
      setBusy(false);
    }
  };
  const shown = showAll ? rows : willUpgrade;
  return (
    <>
      {q.error && <StaleBanner message={q.error.message} onRetry={q.refetch} />}
      {rows.length === 0 ? (
        <EmptyState title="Every file meets its target" body="Files whose profile has a target file set up are judged here. Nothing on disk falls short of it." />
      ) : (
        <>
          <div className="mb-3 flex flex-wrap items-center justify-between gap-2">
            <label className="inline-flex cursor-pointer items-center gap-1.5 text-[12px] text-ink-dim">
              <input type="checkbox" checked={showAll} onChange={(e) => setShowAll(e.target.checked)} />
              Also show files Arrmada won't upgrade ({wont})
            </label>
            <Button size="sm" variant="primary" disabled={toSearch.length === 0} busy={busy} busyLabel="Queuing…" onClick={() => void searchAll()}>
              Search all ({toSearch.length})
            </Button>
          </div>
          {shown.length === 0 ? (
            <EmptyState title="Nothing for the upgrade sweep" body={`${wont} ${wont === 1 ? "file misses" : "files miss"} the target, but Arrmada won't upgrade ${wont === 1 ? "it" : "them"} — tick the box above to see why.`} />
          ) : (
            <Rows rows={shown} onChange={q.refetch} cutoff />
          )}
        </>
      )}
    </>
  );
}

function Section({ title, note, children }: { title: string; note: string; children: ReactNode }) {
  return (
    <section className="mt-6">
      <h2 className="m-0 text-[13px] font-semibold">{title}</h2>
      <p className="mb-2 mt-0.5 text-[11.5px] text-ink-faint">{note}</p>
      {children}
    </section>
  );
}

function Rows({ rows, onChange, upcoming, cutoff }: { rows: MovieWantedRow[]; onChange: () => void; upcoming?: boolean; cutoff?: boolean }) {
  if (rows.length === 0) return null;
  return (
    <div className="overflow-hidden rounded-xl" style={{ border: "1px solid var(--line)" }}>
      {rows.map((r) => (
        <WantedMovieRow key={`${r.id}:${r.version_id ?? 0}`} row={r} onChange={onChange} upcoming={upcoming} cutoff={cutoff} />
      ))}
    </div>
  );
}

// The right-hand state word and the line under the title: the shared Wanted words for a
// missing film, the target and the upgrade sweep's intent for a cutoff row.
function rowWords(r: MovieWantedRow, upcoming?: boolean, cutoff?: boolean): { chip: string; tone: string; line: string } {
  if (upcoming) {
    return { chip: r.available_at ? `Out ${r.available_at}` : "Not released", tone: "var(--ink-faint)", line: "Searched once it's released" };
  }
  if (!cutoff) {
    const w = r as unknown as WantedRow; // a Missing row's state is always a shared Wanted state
    const c = wantedChip(w);
    return { chip: c.text, tone: c.tone, line: wantedLine(w) };
  }
  const parts = [r.track ? `${r.track}: ${r.detail}` : r.detail ?? ""];
  if (r.state === "waiting_download") {
    return { chip: "Upgrade downloading", tone: "var(--accent)", line: `${parts[0]} · downloading ${r.waiting_on ?? "an upgrade"}` };
  }
  if (!r.will_upgrade) {
    return { chip: "Won't upgrade", tone: "var(--ink-faint)", line: `${parts[0]} · ${r.why_not ?? "Arrmada won't upgrade it"}` };
  }
  if (r.last_search) parts.push(`last looked: ${attemptLine(r.last_search.latest)}`);
  const next = nextTryText(r.next_search_at);
  if (next) parts.push(next);
  return { chip: "Will upgrade", tone: "var(--avoid)", line: parts.filter(Boolean).join(" · ") };
}

function WantedMovieRow({ row, onChange, upcoming, cutoff }: { row: MovieWantedRow; onChange: () => void; upcoming?: boolean; cutoff?: boolean }) {
  const toast = useToast();
  const [busy, setBusy] = useState(false);
  const words = rowWords(row, upcoming, cutoff);
  // A search can start for a film that is looked for and not already in the queue; one
  // waiting on a download or a review is settled there, not here.
  const canSearch = !upcoming && !row.queued && (cutoff ? !!row.will_upgrade && row.state === "upgrading" : searchable(row));
  const searchNow = async () => {
    setBusy(true);
    try {
      if (cutoff) {
        const r = await api.bulkMovieSearch([row.id], "upgrade");
        toast(r.queued ? `Looking for an upgrade for “${row.title}”.` : `“${row.title}” is already queued.`);
      } else {
        // The Wanted view's Search now: clears the backoff, then the movie page's own
        // search job, through the same queue.
        const r = await api.wantedSearch("movie", row.id);
        const note = queuedNote(r);
        toast(note ? `“${row.title}”: ${note}.` : `Searching for “${row.title}”.`);
      }
      onChange();
    } catch (e) {
      toast((e as Error).message, { tone: "error" });
    } finally {
      setBusy(false);
    }
  };
  return (
    <div className="flex items-center gap-3 px-3 py-2.5" style={{ background: "var(--panel)", borderBottom: "1px solid var(--line-soft)" }} data-testid="movie-wanted-row">
      <Link to={`/movies/${row.id}`} className="flex min-w-0 flex-1 items-center gap-3">
        <div className="h-[54px] w-[36px] flex-none overflow-hidden rounded" style={{ background: "var(--panel-2)" }}>
          {row.poster_url && <img src={posterThumb(row.poster_url)} alt="" className="h-full w-full object-cover" loading="lazy" decoding="async" />}
        </div>
        <div className="min-w-0 flex-1">
          <div className="truncate text-[12.5px] font-medium">
            {row.title} <span className="font-mono text-[10.5px] text-ink-faint">{row.year || ""}</span>
            {row.tracks?.map((t) => (
              <span key={t} className="ml-1.5 rounded px-1.5 py-0.5 font-mono text-[9px] uppercase" style={{ background: "var(--panel-2)", color: "var(--ink-faint)" }}>{t}</span>
            ))}
          </div>
          {words.line && <div className="mt-0.5 line-clamp-2 text-[11px] leading-snug text-ink-faint" data-testid="movie-wanted-line">{words.line}</div>}
        </div>
      </Link>
      <span className="hidden flex-none flex-col items-end gap-1 sm:flex">
        {row.queued && <StatusChip tone="accent">Queued</StatusChip>}
        {row.quality_profile && <span className="font-mono text-[10px] text-ink-faint">{row.quality_profile}</span>}
      </span>
      <span className="w-[110px] flex-none text-right font-mono text-[10px] uppercase sm:w-[140px]" style={{ color: words.tone }}>{words.chip}</span>
      <span className="flex w-[90px] flex-none justify-end">
        {canSearch && (
          <Button size="sm" onClick={() => void searchNow()} busy={busy} busyLabel="Queuing…" title={cutoff ? "Look for a better release now" : "Search your indexers for it now"}>
            Search
          </Button>
        )}
      </span>
    </div>
  );
}
