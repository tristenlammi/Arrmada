import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { Link, useParams, useSearchParams } from "react-router-dom";
import { PageHeader } from "../components/PageHeader";
import { CastRow } from "../components/CastRow";
import { shownTitle } from "../lib/displayTitle";
import { NumberingBanner } from "./series/NumberingReviewModal";
import { MisplacedBanner } from "./series/MisplacedFiles";
import { usePoll } from "../lib/usePoll";
import { useLiveConnected, useLiveEvent } from "../lib/live";
import { QUEUE_CHANGED, QUEUE_PROGRESS, progressByHash, queueChangedFor } from "../lib/queueLive";
import { downloadHashes, downloadingCount, mergeDownloads, patchSeriesProgress } from "./series/downloads";
import { searchFinishedFor } from "../lib/searchOutcome";
import { refreshSearches } from "../components/LastSearch";
import { Button, StatusChip, useToast } from "../ui";
import { TabPanel, Tabs } from "../ui/Tabs";
import { api, type FitItem, type Series as SeriesT, type Episode } from "../lib/api";
import { seriesApi } from "../lib/seriesApi";
import { qualityApi } from "../lib/qualityApi";
import { Hero } from "./series/Hero";
import { HeroActions, SearchingOffBanner } from "./series/HeroActions";
import { AliasPanel } from "./series/panels";
import { FilesTab } from "./series/FilesTab";
import { ActivityTab } from "./series/ActivityTab";
import { NumberingTab, showNumberingTab } from "./series/NumberingTab";
import { invalidate, useQuery } from "../lib/query";
import { useTabParam } from "../lib/useTabParam";
import { defaultSeason, sortSeasons } from "./series/util";
import { SeasonPanel, SeasonRail } from "./series/SeasonRail";
import { DeleteSeasonsDialog } from "./series/DeleteSeasonsDialog";
import { Page } from "../ui/Page";

// What the page listens for besides the queue (see SeriesDetail).
const SERIES_TOPICS = ["series.imported", "series.searched", "search.finished", "release.grabbed"];
// The download bars' backstop when queue.progress and queue.changed move them.
const DOWNLOAD_FALLBACK_MS = 30_000;
// The old marks are dead weight in this browser now; drop them once.
try { localStorage.removeItem("arrmada.grabRequested"); } catch { /* storage unavailable */ }

export function SeriesDetail() {
  const { id } = useParams();
  const sid = Number(id);
  const [params, setParams] = useSearchParams();
  // The season lives in the address (?season=), replaced rather than pushed: Back leaves
  // the show instead of stepping through every season looked at.
  const pickSeason = useCallback((n: number) => {
    setParams((p) => { const q = new URLSearchParams(p); q.set("season", String(n)); return q; }, { replace: true });
  }, [setParams]);
  const [s, setS] = useState<SeriesT | null>(null);
  const [notFound, setNotFound] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const toast = useToast();

  // err shows the toast in the error colour: a search that failed says so plainly.
  const say = useCallback((msg: string, err = false) => toast(msg, { tone: err ? "error" : "info", ms: 3500 }), [toast]);

  const load = useCallback(() => {
    return api.seriesDetail(sid).then(setS).catch((e: Error) => {
      if (e.message.toLowerCase().includes("not found")) setNotFound(true);
      else setError(e.message);
    });
  }, [sid]);

  useEffect(() => { load(); }, [load]);

  // How each episode file fits the show's profile's ideal file — report only.
  const [fits, setFits] = useState<Map<string, FitItem>>(new Map());
  useEffect(() => {
    qualityApi.libraryFitEpisodes(sid).then((r) => setFits(new Map((r.items ?? []).map((it) => [`${it.season}:${it.episode}`, it])))).catch(() => {});
  }, [sid]);

  // The Numbering tab shows for anime, or while a standard show has something to decide:
  // a pending renumber or downloads held because their files couldn't be placed. The
  // numbering query is the banner's own (same key), re-asked whenever the show was
  // refreshed; the held downloads are re-asked when its history moves.
  const numbering = useQuery(`series-numbering:${sid}`, () => seriesApi.seriesNumbering(sid), { staleMs: 0 });
  const reviews = useQuery(`series-reviews:${sid}`, () => api.seriesReviews(sid), { staleMs: 30_000 });
  const unplaced = reviews.data ?? [];
  const numberingKey = s ? `${s.last_refreshed_at ?? ""}|${s.numbering_source ?? ""}` : "";
  const refetchReviews = reviews.refetch;
  const lastEvent = s?.last_event_id;
  const seenEvent = useRef<number | undefined>(undefined);
  useEffect(() => {
    if (lastEvent === undefined) return;
    if (seenEvent.current !== undefined && seenEvent.current !== lastEvent) void refetchReviews();
    seenEvent.current = lastEvent;
  }, [lastEvent, refetchReviews]);
  const seenNumbering = useRef("");
  useEffect(() => {
    if (!numberingKey) return;
    if (seenNumbering.current && seenNumbering.current !== numberingKey) invalidate(`series-numbering:${sid}`);
    seenNumbering.current = numberingKey;
  }, [numberingKey, sid]);
  const settleNumbering = useCallback(() => { invalidate(`series-numbering:${sid}`); load(); }, [sid, load]);
  const numberingTab = !!s && showNumberingTab(s, numbering.data?.pending, unplaced.length);
  const tabs = useMemo(() => [
    { key: "episodes" as const, label: "Episodes" },
    { key: "files" as const, label: "Files" },
    { key: "activity" as const, label: "Activity" },
    ...(numberingTab ? [{ key: "numbering" as const, label: "Numbering", count: (numbering.data?.pending ? 1 : 0) + unplaced.length || undefined }] : []),
  ], [numberingTab, numbering.data?.pending, unplaced.length]);
  const [tab, setTab] = useTabParam(tabs.map((t) => t.key), "episodes");

  // Season mass edit (GH-80): the picked seasons live here rather than in the rail,
  // because the bulk bar and its delete dialog act on them.
  const [seasonSelecting, setSeasonSelecting] = useState(false);
  const [pickedSeasons, setPickedSeasons] = useState<Set<number>>(() => new Set());
  const [seasonsToDelete, setSeasonsToDelete] = useState<number[] | null>(null);

  // While any episode is downloading its bar moves from queue.progress (by the torrent's
  // hash), and a queue.changed naming one of its torrents reads the light downloads
  // endpoint — not the whole show. That endpoint is also the slow backstop (quick only
  // when an old grab has no hash to follow). The full detail reloads when the server
  // announces an import or a search for this show.
  const connected = useLiveConnected();
  const downloading = s ? downloadingCount(s) : 0;
  const { hashes: dlHashes, complete: allHashed } = useMemo(() => (s ? downloadHashes(s) : { hashes: [], complete: true }), [s]);
  const lastCount = useRef(0);
  const grabLook = useRef<number | undefined>(undefined);
  const pollDownloads = useCallback(() => {
    return api.seriesDownloads(sid).then((dls) => {
      // Without the websocket nothing announces the import, so a download leaving the
      // list (finished, failed or removed) is the cue to reload instead.
      if (!connected && dls.length < lastCount.current) load();
      lastCount.current = dls.length;
      setS((cur) => (cur ? mergeDownloads(cur, dls) : cur));
    }).catch(() => { /* the next tick tries again */ });
  }, [sid, load, connected]);
  useEffect(() => { lastCount.current = downloading; }, [downloading]);
  usePoll(pollDownloads, downloading > 0 ? (connected && allHashed ? DOWNLOAD_FALLBACK_MS : 3000) : null, { immediate: false });
  useLiveEvent([QUEUE_PROGRESS, QUEUE_CHANGED], (ev) => {
    if (queueChangedFor(ev, dlHashes)) { void pollDownloads(); return; }
    const p = progressByHash(ev);
    if (p) setS((cur) => (cur ? patchSeriesProgress(cur, p) : cur));
  }, downloading > 0);
  useLiveEvent(SERIES_TOPICS, (ev) => {
    const evID = (ev.data as { id?: number } | null)?.id;
    if ((ev.topic === "series.imported" || ev.topic === "series.searched") && evID === sid) {
      load();
      if (ev.topic === "series.searched") refreshSearches("series", sid); // the per-scope last-search lines
    } else if (searchFinishedFor(ev, "series", sid)) {
      // Any stored search of this show — the sweep's, RSS's, or a Grab pressed on another
      // device — updates the season and episode lines here, without a reload.
      refreshSearches("series", sid);
    } else if (ev.topic === "release.grabbed") {
      // The grab names no show, and the client takes a moment to list the torrent: look
      // twice, cheaply, rather than reload the whole page for every grab anywhere.
      void pollDownloads();
      window.clearTimeout(grabLook.current);
      grabLook.current = window.setTimeout(() => void pollDownloads(), 5000);
    }
  });
  useEffect(() => () => window.clearTimeout(grabLook.current), []);

  const seasons = useMemo(() => (s ? sortSeasons(s.seasons) : []), [s]);
  // The season shown: the one in the address (so a reload stays put), else where there's
  // something to do (see defaultSeason).
  const asked = Number(params.get("season"));
  const current = seasons.find((sn) => params.has("season") && sn.season_number === asked) ?? seasons.find((sn) => sn.season_number === defaultSeason(seasons));

  const toggleSeasonPicked = useCallback((n: number) => {
    setPickedSeasons((cur) => {
      const next = new Set(cur);
      if (next.has(n)) next.delete(n);
      else next.add(n);
      return next;
    });
  }, []);

  // Leaving select mode drops the pick, so re-entering never starts from a stale choice.
  const stopSelectingSeasons = useCallback(() => {
    setSeasonSelecting(false);
    setPickedSeasons(new Set());
  }, []);

  const toggleSeasonSelecting = useCallback(() => {
    if (seasonSelecting) stopSelectingSeasons();
    else setSeasonSelecting(true);
  }, [seasonSelecting, stopSelectingSeasons]);

  const seasonsDeleted = useCallback((deleted: number[], files: number) => {
    setSeasonsToDelete(null);
    stopSelectingSeasons();
    // The season on screen may be one that just went: the address must not point at it.
    if (current && deleted.includes(current.season_number)) {
      const left = seasons.filter((sn) => !deleted.includes(sn.season_number));
      setParams((p) => {
        const q = new URLSearchParams(p);
        if (left.length) q.set("season", String(left[0].season_number));
        else q.delete("season");
        return q;
      }, { replace: true });
    }
    void load();
    const gone = deleted.length === 1 ? "1 season" : `${deleted.length} seasons`;
    toast(files > 0 ? `${gone} deleted with ${files} file${files === 1 ? "" : "s"}.` : `${gone} deleted.`, { tone: "info", ms: 4000 });
  }, [current, seasons, load, setParams, stopSelectingSeasons, toast]);

  if (notFound) return <Shell><div className="py-10 text-center text-md text-ink-dim">That series isn't in your library. <Link to="/series" className="underline" style={{ color: "var(--accent-text)" }}>Back to Series</Link></div></Shell>;
  if (!s) return <Shell><p className="text-base text-ink-dim">{error ?? "Loading…"}</p></Shell>;

  const ex = s.extra;
  // Every regular episode, for the show-wide upgrade-hold chip.
  const allEps = seasons.filter((sn) => sn.season_number > 0).flatMap((sn) => sn.episodes ?? []);
  return (
    <>
      <PageHeader title={shownTitle(s)} />

      <Hero series={s}>
        <HeroActions series={s} onChange={load} flash={say} />
        <SearchingOffBanner series={s} onChange={load} flash={say} />
        <UpgradeHoldChip series={s} episodes={allEps} onChange={load} flash={say} />
      </Hero>

      <Page className="pb-10">
        <MisplacedBanner seriesId={s.id} title={s.title} refreshKey={s.last_event_id} onChange={load} />
        <Tabs tabs={tabs} value={tab} onChange={setTab} idPrefix="series" label="Series sections" className="mb-4 mt-1 border-b" />

        {tab === "episodes" && (
          <TabPanel idPrefix="series" value="episodes">
            <NumberingBanner seriesId={s.id} refreshKey={numberingKey} onApplied={settleNumbering} />
            <div className="mt-4">
              <SeasonRail
                seasons={seasons}
                selected={current?.season_number}
                onSelect={pickSeason}
                selecting={seasonSelecting}
                picked={pickedSeasons}
                onToggleSelecting={toggleSeasonSelecting}
                onTogglePick={toggleSeasonPicked}
              />
              {seasonSelecting && (
                <div
                  role="region"
                  aria-label="Selected seasons"
                  className="sticky z-20 mt-3 flex flex-wrap items-center gap-2 rounded-xl px-3 py-2.5"
                  style={{ bottom: "calc(var(--bottom-chrome) + 12px)", background: "var(--panel)", border: "1px solid var(--accent-line)", boxShadow: "var(--shadow)" }}
                >
                  <span className="mr-auto text-base font-semibold" aria-live="polite">
                    {pickedSeasons.size} season{pickedSeasons.size === 1 ? "" : "s"} selected
                  </span>
                  {pickedSeasons.size < seasons.length && (
                    <Button size="sm" variant="ghost" onClick={() => setPickedSeasons(new Set(seasons.map((sn) => sn.season_number)))}>All</Button>
                  )}
                  <Button size="sm" variant="danger" onClick={() => setSeasonsToDelete([...pickedSeasons].sort((a, b) => a - b))} disabled={pickedSeasons.size === 0}>
                    Delete seasons…
                  </Button>
                  <Button size="sm" variant="ghost" onClick={stopSelectingSeasons}>Cancel</Button>
                </div>
              )}
              {seasonsToDelete && (
                <DeleteSeasonsDialog
                  series={s}
                  seasons={seasonsToDelete}
                  onClose={() => setSeasonsToDelete(null)}
                  onDeleted={seasonsDeleted}
                />
              )}
              {current && <SeasonPanel key={current.season_number} series={s} season={current} fits={fits} onChange={load} flash={say} />}
            </div>
            {/* Without a Numbering tab (a standard show with nothing to decide) the
                alternate titles stay reachable here, collapsed. */}
            {!numberingTab && <AliasPanel series={s} />}
            <CastRow cast={ex?.cast} />
          </TabPanel>
        )}
        {tab === "files" && (
          <TabPanel idPrefix="series" value="files">
            <FilesTab series={s} fits={fits} refreshKey={s.last_event_id} downloading={downloading} onChange={load} flash={say} />
          </TabPanel>
        )}
        {tab === "activity" && (
          <TabPanel idPrefix="series" value="activity">
            <ActivityTab seriesId={s.id} refreshKey={s.last_event_id} />
          </TabPanel>
        )}
        {tab === "numbering" && (
          <TabPanel idPrefix="series" value="numbering">
            <NumberingTab series={s} pending={numbering.data?.pending} unplaced={unplaced} onChange={load} onSettled={settleNumbering} flash={say} />
          </TabPanel>
        )}
      </Page>
    </>
  );
}

// UpgradeHoldChip shows when episodes of this show were kept as they are when its profile
// changed ("keep existing files"), with Resume for all of them; each season has its own
// Resume too. A hold nobody can see is a file that silently never upgrades.
function UpgradeHoldChip({ series, episodes, onChange, flash }: { series: SeriesT; episodes: Episode[]; onChange: () => void; flash: (m: string, err?: boolean) => void }) {
  const [busy, setBusy] = useState(false);
  const held = episodes.filter((e) => e.has_file && e.upgrade_hold).length;
  if (held === 0) return null;
  const resume = async () => {
    setBusy(true);
    try {
      await qualityApi.resumeSeriesUpgrades(series.id);
      flash("Upgrades resumed — the next sweep can replace these episodes again.");
      onChange();
    } catch (e) {
      flash((e as Error).message, true);
    } finally {
      setBusy(false);
    }
  };
  return (
    <div className="mt-3 flex flex-wrap items-center gap-2 text-sm text-ink-dim">
      <StatusChip tone="accent">Upgrades paused</StatusChip>
      <span>{held} episode{held === 1 ? "" : "s"} kept when the profile changed</span>
      <Button size="sm" variant="secondary" onClick={resume} busy={busy} busyLabel="Resuming…">Resume</Button>
    </div>
  );
}


function Shell({ children }: { children: React.ReactNode }) {
  return <><PageHeader title="Series" /><Page>{children}</Page></>;
}

