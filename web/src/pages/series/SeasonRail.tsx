import { useEffect, useRef, useState } from "react";
import { SeasonAudioSummary } from "../../components/AudioLangChips";
import { FileDetailsModal } from "../../components/FileDetailsModal";
import { refreshSearches, useSearchAttempts } from "../../components/LastSearch";
import { ReleaseSearchModal } from "../../components/ReleaseSearchModal";
import { api, type Episode, type FitItem, type Season, type Series as SeriesT } from "../../lib/api";
import { seriesApi } from "../../lib/seriesApi";
import { qualityApi } from "../../lib/qualityApi";
import { convertApi } from "../../lib/convertApi";
import { disposalLine, useRecycleMode } from "../../lib/disposal";
import { posterThumb } from "../../lib/img";
import { attemptLine, outcomeTone } from "../../lib/searchOutcome";
import { SEARCHING_OFF_HINT, STATUS_LABEL } from "../../lib/status";
import { jobFailed, jobToast, useJob } from "../../lib/useJob";
import { Button, ConfirmDialog, StatusChip } from "../../ui";
import { scrollDelta } from "../../ui/Tabs";
import { EpisodeCard, type EpisodeDialog } from "./EpisodeCard";
import { useEpisodeSubs, useSeenOnce } from "./EpisodeSubs";
import { LazySubtitleDrawer } from "../../components/subtitles/LazySubtitleDrawer";
import type { Flash } from "./HeroActions";
import { aired, latestFor, seasonCounts, seasonName, seasonScope, sxe } from "./util";

// Seasons this long render in pages, so a 1000-episode anime opens without lag.
export const LONG_SEASON = 100;
export const PAGE = 50;
// The server's cap on one picked-episodes search.
const MAX_BULK_SEARCH = 200;

// SeasonRail is the row of season chips under the hero: poster, name and progress. It
// scrolls sideways on a phone and keeps the chosen season in view. In select mode a chip
// picks its season instead of opening it, so the page can act on several at once (GH-80);
// the pick itself lives in the page, because the bulk bar and its dialog do.
export function SeasonRail({ seasons, selected, onSelect, selecting = false, picked, onToggleSelecting, onTogglePick }: {
  seasons: Season[];
  selected?: number;
  onSelect: (n: number) => void;
  selecting?: boolean;
  picked?: Set<number>;
  onToggleSelecting?: () => void;
  onTogglePick?: (n: number) => void;
}) {
  const row = useRef<HTMLDivElement>(null);
  const chips = useRef(new Map<number, HTMLButtonElement>());
  useEffect(() => {
    const el = selected !== undefined ? chips.current.get(selected) : undefined;
    if (!el || !row.current) return;
    // Scroll the rail itself, never the page, so a deep link doesn't jump.
    const d = scrollDelta(row.current.getBoundingClientRect(), el.getBoundingClientRect());
    if (d !== 0) row.current.scrollLeft += d > 0 ? Math.ceil(d) : Math.floor(d);
  }, [selected]);

  return (
    <div>
      {onToggleSelecting && (
        <div className="mb-2 flex items-center justify-end">
          <Button size="sm" variant={selecting ? "primary" : "ghost"} aria-pressed={selecting} onClick={onToggleSelecting}>
            {selecting ? "Done" : "Select seasons"}
          </Button>
        </div>
      )}
      <div ref={row} role="group" aria-label={selecting ? "Seasons to delete" : "Seasons"} className="thin-scroll -mx-4 flex gap-2 overflow-x-auto px-4 pb-2 sm:mx-0 sm:px-0">
        {seasons.map((sn) => {
          const c = seasonCounts(sn);
          const active = !selecting && sn.season_number === selected;
          const isPicked = selecting && !!picked?.has(sn.season_number);
          const done = c.counted > 0 && c.have >= c.counted;
          const pct = c.counted ? Math.round((c.have / c.counted) * 100) : 0;
          const thumb = posterThumb(sn.poster_url);
          const lit = active || isPicked;
          return (
            <button
              key={sn.id}
              ref={(el) => { if (el) chips.current.set(sn.season_number, el); else chips.current.delete(sn.season_number); }}
              type="button"
              aria-pressed={selecting ? isPicked : active}
              onClick={() => (selecting ? onTogglePick?.(sn.season_number) : onSelect(sn.season_number))}
              className="flex w-[156px] flex-none items-center gap-2.5 rounded-xl p-1.5 pr-3 text-left"
              style={{ background: lit ? "var(--accent-soft)" : "var(--panel)", border: `1px solid ${lit ? "var(--accent)" : "var(--line)"}` }}
            >
              {/* An unreleased season fades its artwork only: fading the card took its words
                  below a readable contrast. In select mode the artwork carries the tick. */}
              <span className="relative h-[54px] w-[36px] flex-none overflow-hidden rounded-md" style={{ background: "linear-gradient(150deg, hsl(24 40% 30%), hsl(20 35% 16%))", opacity: c.total === 0 ? 0.65 : 1 }}>
                {thumb && <img src={thumb} alt="" loading="lazy" className="h-full w-full object-cover" />}
                {selecting && (
                  <span className="absolute inset-0 flex items-center justify-center" style={{ background: isPicked ? "rgba(0,0,0,0.25)" : "rgba(0,0,0,0.45)" }}>
                    <span aria-hidden className="flex h-[18px] w-[18px] items-center justify-center rounded-full text-xs font-bold" style={{ border: "1px solid #fff", color: "#fff", background: isPicked ? "var(--accent)" : "transparent" }}>
                      {isPicked ? "✓" : ""}
                    </span>
                  </span>
                )}
              </span>
              <span className="min-w-0 flex-1">
                <span className="block truncate text-base font-semibold" style={{ color: lit ? "var(--accent-text)" : "var(--ink)" }}>{seasonName(sn.season_number)}</span>
                <span className={`block font-mono text-xs ${lit ? "text-ink-dim" : "text-ink-faint"}`}>
                  {c.total === 0 ? "Not yet released" : c.airedCount === 0 ? "Upcoming" : `${c.have}/${c.counted}`}
                </span>
                {c.total > 0 && c.airedCount > 0 && (
                  <span className="mt-1 block h-1 overflow-hidden rounded-full" style={{ background: "var(--line)" }}>
                    <span className="block h-full rounded-full" style={{ width: `${pct}%`, background: done ? "var(--good)" : "var(--accent)" }} />
                  </span>
                )}
              </span>
            </button>
          );
        })}
      </div>
    </div>
  );
}

// SeasonPanel is the chosen season: its header (progress, folder, upgrade hold, Grab
// missing, Choose release…, Monitor) and its episode cards. Only this season's cards are
// mounted, a long season pages in 50 at a time, and the episode dialogs live here rather
// than in each card.
export function SeasonPanel({ series, season, fits, onChange, flash }: { series: SeriesT; season: Season; fits: Map<string, FitItem>; onChange: () => void; flash: Flash }) {
  const attempts = useSearchAttempts("series", series.id);
  const last = latestFor(attempts, seasonScope(season.season_number));
  const eps = season.episodes ?? [];
  const c = seasonCounts(season);
  const name = seasonName(season.season_number);
  const [busy, setBusy] = useState<null | "grab" | "resume" | "monitor">(null);
  const [choose, setChoose] = useState(false);
  const [dialog, setDialog] = useState<EpisodeDialog | null>(null);
  const [shown, setShown] = useState(eps.length > LONG_SEASON ? PAGE : Infinity);
  // Select mode (SER-16): cards become checkboxes and a bar acts on the picked ones. The
  // pick is per season — the panel is keyed by season, so switching season clears it.
  const [selecting, setSelecting] = useState(false);
  const [picked, setPicked] = useState<Set<number>>(() => new Set());
  const [grabJob, setGrabJob] = useState<number | null>(null);
  // Subtitle chips: read (which probes the show's files) only once the cards are seen.
  const grid = useRef<HTMLDivElement>(null);
  const subs = useEpisodeSubs(series.id, useSeenOnce(grid));
  const [subsFor, setSubsFor] = useState<Episode | null>(null);
  const grabbing = useJob(grabJob, {
    onDone: (j) => { setGrabJob(null); if (jobFailed(j)) flash(jobToast(j), true); refreshSearches("series", series.id); onChange(); },
  });

  // Grab missing only fills gaps, so it's offered only while there is one: an aired,
  // monitored episode with no file. On a full season it could only fetch a pack that the
  // import gate then throws away. Never on Specials: they have no packs, so they're grabbed
  // one special at a time from their own cards.
  const anyMissing = season.season_number > 0 && c.missing > 0;
  const held = eps.filter((e) => e.has_file && e.upgrade_hold).length;
  // The on-disk folder for this season = the directory of any episode file we have.
  const seasonDir = (eps.find((e) => e.file_path)?.file_path ?? "").replace(/[\\/][^\\/]*$/, "");

  const run = async (key: NonNullable<typeof busy>, fn: () => Promise<void>) => {
    setBusy(key);
    try { await fn(); } catch (e) { flash((e as Error).message, true); } finally { setBusy(null); }
  };
  const grabSeason = () => run("grab", async () => {
    const r = await api.autoGrabSeries(series.id, season.season_number, 0);
    if (r.job_id) setGrabJob(r.job_id);
    flash(`Searching for ${name}'s missing episodes…`);
  });
  const resume = () => run("resume", async () => {
    await qualityApi.resumeSeriesUpgrades(series.id, season.season_number);
    flash(`Upgrades resumed for ${name}.`);
    onChange();
  });
  const monitor = () => run("monitor", async () => {
    await seriesApi.setSeasonMonitored(series.id, season.season_number, !season.monitored);
    onChange();
  });
  const searching = busy === "grab" || grabbing.running;
  const toggle = (id: number) => setPicked((cur) => {
    const next = new Set(cur);
    if (next.has(id)) next.delete(id); else next.add(id);
    return next;
  });
  const stopSelecting = () => { setSelecting(false); setPicked(new Set()); };

  return (
    <section aria-label={name} className="mt-3">
      <div className="flex flex-wrap items-center gap-x-3 gap-y-2">
        <h2 className="m-0 text-lg font-bold">{name}</h2>
        <span className="font-mono text-xs text-ink-faint">
          {c.total === 0 ? "Not yet released" : c.airedCount === 0 ? "Upcoming" : `${c.have}/${c.counted}`}
          {c.notMonitored > 0 && <span title="Aired episodes without a file that aren't monitored — not counted as missing"> · {c.notMonitored} not monitored</span>}
        </span>
        {/* The season is wanted but the show's searching is off: said here too, beside
            the season's own Monitored, so the two don't seem to disagree. */}
        {!series.monitored && season.monitored && <StatusChip tone="faint" title={SEARCHING_OFF_HINT}>{STATUS_LABEL.searchingOff}</StatusChip>}
        <SeasonAudioSummary episodes={eps} />
        {held > 0 && (
          <span className="flex items-center gap-2">
            <StatusChip tone="accent" title={`${held} episode${held === 1 ? "" : "s"} kept as they were when the profile changed`}>Upgrades paused</StatusChip>
            <Button size="sm" onClick={resume} busy={busy === "resume"} busyLabel="Resuming…" title={`Let upgrades replace ${name}'s kept episodes again`}>Resume</Button>
          </span>
        )}
        <div className="ml-auto flex flex-wrap items-center gap-2">
          {anyMissing && (
            <Button
              size="sm"
              onClick={grabSeason}
              busy={searching}
              busyLabel="Searching…"
              title={(last ? `${attemptLine(last)}. Click to search again.` : `Grabs ${name}'s missing episodes — a season pack when most of the season is missing or no single episodes exist`) + (series.monitored ? "" : ". A one-off search: the show's searching stays off.")}
              style={{ border: "1px solid var(--accent-line)", color: "var(--accent-text)", background: "transparent" }}
            >
              {last ? "Grab again" : "Grab missing"}
            </Button>
          )}
          {c.total > 0 && <Button size="sm" onClick={() => setChoose(true)} title={`Search indexers for ${name}`}>Choose release…</Button>}
          {c.total > 0 && (
            <Button size="sm" onClick={() => (selecting ? stopSelecting() : setSelecting(true))} aria-pressed={selecting} title="Pick episodes to monitor, unmonitor or search together">
              {selecting ? "Done" : "Select"}
            </Button>
          )}
          <Button
            size="sm"
            onClick={monitor}
            disabled={busy === "monitor"}
            aria-pressed={season.monitored}
            title={series.monitored
              ? (season.monitored ? `${name} is monitored — click to stop` : `Monitor all of ${name}`)
              : (season.monitored ? `${name} is monitored — searched for once you turn the show's searching on. Click to stop.` : `Monitor all of ${name} — searched for once you turn the show's searching on`)}
            style={{ border: `1px solid ${season.monitored ? "var(--accent-line)" : "var(--line)"}`, color: season.monitored ? "var(--accent-text)" : "var(--ink-faint)", background: "transparent" }}
          >
            {season.monitored ? "Monitored" : "Monitor"}
          </Button>
        </div>
      </div>
      {seasonDir && <div className="mt-1 truncate font-mono text-xs text-ink-faint" title={seasonDir}>{seasonDir}</div>}
      {anyMissing && last && !searching && <div className="mt-1 text-sm" style={{ color: outcomeTone(last.outcome) }}>{attemptLine(last)}</div>}

      {c.total === 0 ? (
        <div className="mt-3 rounded-xl p-6 text-center text-base text-ink-dim" style={{ border: "1px solid var(--line)" }}>No episodes are listed for {name} yet.</div>
      ) : (
        <div ref={grid} className="mt-3 grid gap-2.5 lg:grid-cols-2">
          {eps.slice(0, shown).map((e) => (
            <EpisodeCard
              key={e.id}
              series={series}
              ep={e}
              seasonMonitored={season.monitored}
              fit={fits.get(`${e.season_number}:${e.episode_number}`)}
              last={latestFor(attempts, sxe(e))}
              onChange={onChange}
              flash={flash}
              open={setDialog}
              selecting={selecting}
              selected={picked.has(e.id)}
              onToggleSelect={() => toggle(e.id)}
              subs={subs.bySxE?.get(`${e.season_number}:${e.episode_number}`)}
              onSubs={() => setSubsFor(e)}
            />
          ))}
        </div>
      )}
      {selecting && (
        <BulkBar
          series={series}
          season={season}
          picked={picked}
          setPicked={setPicked}
          onDone={stopSelecting}
          onSearch={(id) => { if (id) setGrabJob(id); }}
          onChange={onChange}
          flash={flash}
        />
      )}
      {eps.length > shown && (
        <div className="mt-3 flex items-center justify-center gap-3">
          <span className="font-mono text-xs text-ink-faint">{shown} of {eps.length}</span>
          <Button size="sm" onClick={() => setShown((n) => n + PAGE)}>Show more</Button>
        </div>
      )}

      {choose && (
        <ReleaseSearchModal
          title={`${series.title} — ${name}`}
          subtitle={season.season_number === 0 ? "Releases tagged S00, or named by a special's title, for this show." : "Season packs and episodes for this season."}
          fetchReleases={() => api.seriesReleases(series.id, season.season_number)}
          onGrab={async (rel) => { await api.grabSeries(series.id, { token: rel.token ?? "" }); onChange(); }}
          onClose={() => setChoose(false)}
        />
      )}
      <EpisodeDialogs series={series} dialog={dialog} onClose={() => setDialog(null)} onChange={onChange} flash={flash} />
      {subsFor && (
        <LazySubtitleDrawer
          media={{ kind: "episode", series_id: series.id, season: subsFor.season_number, episode: subsFor.episode_number }}
          title={`${series.title} - ${sxe(subsFor)}`}
          onClose={() => setSubsFor(null)}
          onChanged={subs.refetch}
        />
      )}
    </section>
  );
}

// BulkBar is select mode's action bar, held at the bottom of the screen while the season is
// in view: how many are picked, Monitor, Unmonitor, Search (one scoped search of exactly
// those episodes), Delete files (their videos and subtitles, through the one confirm
// dialog) and Clear.
function BulkBar({ series, season, picked, setPicked, onDone, onSearch, onChange, flash }: {
  series: SeriesT;
  season: Season;
  picked: Set<number>;
  setPicked: (s: Set<number>) => void;
  onDone: () => void;
  onSearch: (jobId?: number) => void;
  onChange: () => void;
  flash: Flash;
}) {
  const [busy, setBusy] = useState<null | "on" | "off" | "search" | "delete">(null);
  const eps = (season.episodes ?? []).filter((e) => picked.has(e.id));
  const n = eps.length;
  const all = (season.episodes ?? []).length;
  // Only aired episodes without a file can be searched for; the rest of the pick is left out.
  const searchable = eps.filter((e) => !e.has_file && aired(e));
  const tooMany = searchable.length > MAX_BULK_SEARCH;
  const words = `${n} episode${n === 1 ? "" : "s"}`;
  const binMode = useRecycleMode();
  const deletable = eps.filter((e) => e.has_file);
  const deletableCount = deletable.length;
  const totalBytes = deletable.reduce((a, e) => a + (e.size_bytes || 0), 0);
  const [confirmOpen, setConfirmOpen] = useState(false);
  const act = async (key: NonNullable<typeof busy>, fn: () => Promise<void>) => {
    setBusy(key);
    try { await fn(); } catch (e) { flash((e as Error).message, true); } finally { setBusy(null); }
  };
  const setMonitored = (on: boolean) => act(on ? "on" : "off", async () => {
    await api.setEpisodesMonitored(series.id, eps.map((e) => e.id), on);
    flash(`${words} ${on ? "monitored" : "no longer monitored"}.`);
    onChange();
    onDone();
  });
  const searchPicked = () => act("search", async () => {
    const r = await api.searchSeriesEpisodes(series.id, searchable.map((e) => ({ season: e.season_number, episode: e.episode_number })));
    onSearch(r.job_id);
    flash(`Searching for ${searchable.length} episode${searchable.length === 1 ? "" : "s"}…`);
    onDone();
  });
  // One request per episode (the endpoint deletes one file), and every refusal is reported
  // rather than swallowed: a bin that took four of five files must not read as a success.
  const deletePicked = async () => {
    setBusy("delete");
    let deleted = 0;
    const failed: string[] = [];
    for (const e of deletable) {
      try {
        await seriesApi.deleteEpisodeFile(series.id, e.season_number, e.episode_number);
        deleted++;
      } catch (err) {
        failed.push(`${sxe(e)}: ${(err as Error).message}`);
      }
    }
    setBusy(null);
    setConfirmOpen(false);
    onChange();
    onDone();
    if (failed.length) {
      flash(`Deleted ${deleted} of ${deletableCount} files. Still there: ${failed.join("; ")}`, true);
      return;
    }
    flash(`Deleted the files for ${words}.`);
  };
  return (
    <>
      <div
        role="region"
        aria-label="Selected episodes"
        className="sticky z-20 mt-3 flex flex-wrap items-center gap-2 rounded-xl px-3 py-2.5"
        style={{ bottom: "calc(var(--bottom-chrome) + 12px)", background: "var(--panel)", border: "1px solid var(--accent-line)", boxShadow: "var(--shadow)" }}
      >
        <span className="mr-auto text-base font-semibold" aria-live="polite">{n} selected</span>
        {n < all && <Button size="sm" variant="ghost" onClick={() => setPicked(new Set((season.episodes ?? []).map((e) => e.id)))}>All</Button>}
        <Button size="sm" onClick={() => setMonitored(true)} disabled={n === 0 || busy !== null} busy={busy === "on"} busyLabel="Saving…">Monitor</Button>
        <Button size="sm" onClick={() => setMonitored(false)} disabled={n === 0 || busy !== null} busy={busy === "off"} busyLabel="Saving…">Unmonitor</Button>
        <Button
          size="sm"
          variant="primary"
          onClick={searchPicked}
          disabled={searchable.length === 0 || tooMany || busy !== null}
          busy={busy === "search"}
          busyLabel="Starting…"
          title={tooMany ? `Pick at most ${MAX_BULK_SEARCH} episodes to search at once` : searchable.length === 0 ? "None of these is missing — there is nothing to search for" : "One search for exactly these episodes; ones already here are left out"}
        >
          Search
        </Button>
        <Button size="sm" variant="danger" onClick={() => setConfirmOpen(true)} disabled={deletableCount === 0 || busy !== null} busy={busy === "delete"} busyLabel="Deleting…">Delete files…</Button>
        <Button size="sm" variant="ghost" onClick={() => setPicked(new Set())} disabled={n === 0}>Clear</Button>
      </div>
      {confirmOpen && (
        <ConfirmDialog
          title={`Delete files for ${deletableCount} episode${deletableCount === 1 ? "" : "s"}?`}
          body={<>{disposalLine(totalBytes, binMode)}, with their subtitles. The episodes become wanted again.</>}
          confirmLabel="Delete files"
          busyLabel="Deleting…"
          busy={busy === "delete"}
          onConfirm={deletePicked}
          onCancel={() => setConfirmOpen(false)}
        />
      )}
    </>
  );
}

// EpisodeDialogs opens what an episode card asked for: the release picker, the file's
// details, or the delete confirm (which says truthfully where the file goes; a bin refusal
// shows in the dialog).
function EpisodeDialogs({ series, dialog, onClose, onChange, flash }: { series: SeriesT; dialog: EpisodeDialog | null; onClose: () => void; onChange: () => void; flash: Flash }) {
  const binMode = useRecycleMode();
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  if (!dialog) return null;
  const { ep } = dialog;
  const label = sxe(ep);
  const done = () => { setErr(null); onClose(); };

  if (dialog.kind === "choose") {
    return (
      <ReleaseSearchModal
        title={`${series.title} — ${label}`}
        subtitle={ep.title || undefined}
        fetchReleases={() => api.seriesReleases(series.id, ep.season_number, ep.episode_number)}
        onGrab={async (rel) => { await api.grabSeries(series.id, { token: rel.token ?? "" }); onChange(); }}
        onClose={done}
      />
    );
  }
  if (dialog.kind === "file" && ep.file_path) {
    return (
      <FileDetailsModal
        path={ep.file_path}
        title={`${series.title} — ${label}`}
        subtitle={ep.title || undefined}
        onCleanTracks={async () => { await convertApi.convertRequest(`episode:${series.id}:${ep.season_number}:${ep.episode_number}`); }}
        onClose={done}
      />
    );
  }
  if (dialog.kind === "delete") {
    const del = async () => {
      setBusy(true);
      setErr(null);
      try { await seriesApi.deleteEpisodeFile(series.id, ep.season_number, ep.episode_number); flash(`Deleted ${label} file`); onChange(); done(); }
      catch (e) { setErr((e as Error).message); }
      finally { setBusy(false); }
    };
    return (
      <ConfirmDialog
        title={<>Delete the file for {label}?</>}
        body={<>{disposalLine(ep.size_bytes ?? 0, binMode)}, with its subtitles. The episode becomes wanted again.</>}
        confirmLabel="Delete file"
        busyLabel="Deleting…"
        busy={busy}
        error={err}
        onConfirm={del}
        onCancel={done}
      />
    );
  }
  return null;
}
