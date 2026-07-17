import { useCallback, useEffect, useState } from "react";
import { Link, useParams } from "react-router-dom";
import { PageHeader } from "../components/PageHeader";
import { ReleaseSearchModal } from "../components/ReleaseSearchModal";
import { PasteLinkModal } from "../components/PasteLinkModal";
import { api, type Series as SeriesT, type Season, type Episode, type SeriesImportCandidate, type MovieEvent, type BlockEntry } from "../lib/api";

const today = new Date().toISOString().slice(0, 10);
const aired = (e: Episode) => !!e.air_date && e.air_date <= today;
const sxe = (e: Episode) => `S${String(e.season_number).padStart(2, "0")}E${String(e.episode_number).padStart(2, "0")}`;

function fmtSize(bytes?: number): string {
  if (!bytes || bytes <= 0) return "—";
  const gb = bytes / 1024 ** 3;
  return gb >= 1 ? `${gb.toFixed(2)} GB` : `${(bytes / 1024 ** 2).toFixed(0)} MB`;
}

export function SeriesDetail() {
  const { id } = useParams();
  const sid = Number(id);
  const [s, setS] = useState<SeriesT | null>(null);
  const [notFound, setNotFound] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [toast, setToast] = useState<string | null>(null);

  const flash = (msg: string) => { setToast(msg); window.setTimeout(() => setToast(null), 3500); };

  const load = useCallback(() => {
    api.seriesDetail(sid).then(setS).catch((e: Error) => {
      if (e.message.toLowerCase().includes("not found")) setNotFound(true);
      else setError(e.message);
    });
  }, [sid]);

  useEffect(() => { load(); }, [load]);

  // While any episode is downloading, refresh so the progress ticks up.
  const anyDownloading = !!s?.seasons?.some((sn) => sn.episodes?.some((e) => e.download));
  useEffect(() => {
    if (!anyDownloading) return;
    const t = setInterval(load, 3000);
    return () => clearInterval(t);
  }, [anyDownloading, load]);

  if (notFound) return <Shell><div className="py-10 text-center text-[13px] text-ink-dim">That series isn't in your library. <Link to="/series" className="underline" style={{ color: "var(--accent)" }}>Back to Series</Link></div></Shell>;
  if (!s) return <Shell><p className="text-[12.5px] text-ink-dim">{error ?? "Loading…"}</p></Shell>;

  const ex = s.extra;
  // Ascending (Season 1 at the top), with Specials (season 0) pushed to the bottom.
  const seasons = [...(s.seasons ?? [])].sort((a, b) => {
    if (a.season_number === 0) return 1;
    if (b.season_number === 0) return -1;
    return a.season_number - b.season_number;
  });
  const continuing = /return|continu/i.test(s.status ?? "");
  // Overall progress from the loaded episodes (aired-aware, specials excluded) — the detail
  // endpoint doesn't carry the roll-up stats the way the list does.
  const allEps = seasons.filter((sn) => sn.season_number > 0).flatMap((sn) => sn.episodes ?? []);
  const haveAll = allEps.filter((e) => e.has_file).length;
  const countedAll = allEps.filter((e) => e.has_file || aired(e)).length;
  const st = statusOf(haveAll, countedAll, s.monitored);

  return (
    <>
      <PageHeader title={s.title} crumb="Library / Series" />

      {/* Hero band with backdrop — mirrors the Movie detail layout */}
      <div className="relative">
        {ex?.backdrop_url && (
          <>
            <div className="pointer-events-none absolute inset-0 bg-cover bg-center opacity-[0.18]" style={{ backgroundImage: `url(${ex.backdrop_url})` }} />
            <div className="pointer-events-none absolute inset-0" style={{ background: "linear-gradient(180deg, transparent, var(--bg))" }} />
          </>
        )}
        <div className="relative mx-auto w-full max-w-[1200px] px-4 py-6 sm:px-6">
          <Link to="/series" className="mb-4 inline-flex items-center gap-1 text-[12px] text-ink-dim hover:text-[var(--ink)]">
            <svg width="14" height="14" viewBox="0 0 24 24" fill="none"><path d="M15 19l-7-7 7-7" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" /></svg>
            All series
          </Link>

          <div className="flex flex-col gap-6 sm:flex-row">
            <div className="w-[180px] flex-none overflow-hidden rounded-xl" style={{ border: "1px solid var(--line)", aspectRatio: "2/3" }}>
              {s.poster_url ? (
                <img src={s.poster_url} alt={s.title} className="h-full w-full object-cover" />
              ) : (
                <div className="flex h-full w-full items-end p-3" style={{ background: "linear-gradient(150deg, hsl(24 40% 30%), hsl(20 35% 16%))" }}>
                  <span className="text-[15px] font-bold text-white">{s.title}</span>
                </div>
              )}
            </div>

            <div className="min-w-0 flex-1">
              <div className="flex flex-wrap items-center gap-2.5">
                <span className="rounded-full px-2.5 py-1 font-mono text-[10.5px] font-semibold uppercase" style={{ background: st.soft, color: st.tone }}>{st.label}</span>
                <span className="rounded px-1.5 py-0.5 font-mono text-[9.5px] uppercase" style={{ background: continuing ? "var(--good-soft)" : "var(--panel-2)", color: continuing ? "var(--good)" : "var(--ink-faint)" }}>{continuing ? "Continuing" : "Ended"}</span>
                {s.year > 0 && <span className="font-mono text-[11px] text-ink-faint">{s.year}</span>}
                {s.network && <span className="font-mono text-[11px] text-ink-faint">{s.network}</span>}
                <span className="flex items-center gap-2">
                  {s.imdb_id && <a href={`https://www.imdb.com/title/${s.imdb_id}`} target="_blank" rel="noreferrer" className="rounded px-1.5 py-0.5 font-mono text-[10px] font-bold" style={{ background: "#f5c518", color: "#000" }}>IMDb</a>}
                  <a href={`https://www.themoviedb.org/tv/${s.tmdb_id}`} target="_blank" rel="noreferrer" className="rounded px-1.5 py-0.5 font-mono text-[10px] font-bold" style={{ background: "#01b4e4", color: "#fff" }}>TMDB</a>
                </span>
              </div>

              <p className="mt-3 text-[13px] leading-relaxed text-ink-dim">{s.overview || "No overview available."}</p>

              {ex?.genres && ex.genres.length > 0 && (
                <div className="mt-3 flex flex-wrap gap-1.5">{ex.genres.map((g) => <span key={g} className="rounded-full px-2 py-0.5 text-[11px]" style={{ background: "var(--panel-2)", color: "var(--ink-dim)" }}>{g}</span>)}</div>
              )}

              <div className="mt-4 flex flex-wrap items-center gap-4">
                <ProfileSelector series={s} onChange={load} />
              </div>

              <Toolbar series={s} onChange={load} flash={flash} />
            </div>
          </div>
        </div>
      </div>

      <div className="mx-auto w-full max-w-[1200px] px-4 pb-10 sm:px-6">
        <div className="mt-2 flex flex-col gap-3">
          {seasons.map((sn) => <SeasonBlock key={sn.id} series={s} season={sn} onChange={load} flash={flash} defaultOpen={false} />)}
        </div>

        <SeriesBlocklistPanel seriesId={s.id} refreshKey={s.seasons} />

        {ex?.cast && ex.cast.length > 0 && (
          <div className="mt-8">
            <h2 className="m-0 mb-3 text-[14px] font-bold">Cast</h2>
            <div className="thin-scroll flex gap-3 overflow-x-auto pb-2">
              {ex.cast.map((c, i) => (
                <div key={i} className="w-[92px] flex-none text-center">
                  <div className="mb-1.5 overflow-hidden rounded-lg" style={{ aspectRatio: "2/3", background: "var(--panel-2)" }}>
                    {c.profile_url ? <img src={c.profile_url} alt={c.name} className="h-full w-full object-cover" loading="lazy" /> : null}
                  </div>
                  <div className="truncate text-[11px] font-semibold" title={c.name}>{c.name}</div>
                  {c.character && <div className="truncate text-[10px] text-ink-faint" title={c.character}>{c.character}</div>}
                </div>
              ))}
            </div>
          </div>
        )}

        <HistoryPanel seriesId={s.id} refreshKey={s.stats?.have_files} />
      </div>

      {toast && (
        <div className="fixed bottom-5 left-1/2 -translate-x-1/2 rounded-lg px-4 py-2.5 text-[12.5px] font-medium" style={{ background: "var(--panel-2)", border: "1px solid var(--line)", boxShadow: "var(--shadow)", color: "var(--ink)" }}>{toast}</div>
      )}
    </>
  );
}

function statusOf(have: number, total: number, monitored: boolean): { label: string; tone: string; soft: string } {
  if (total > 0 && have >= total) return { label: "Complete", tone: "var(--good)", soft: "var(--good-soft, rgba(90,140,90,.12))" };
  if (monitored) return { label: have > 0 ? "In progress" : "Wanted", tone: "var(--avoid)", soft: "var(--avoid-soft)" };
  return { label: "Unmonitored", tone: "var(--ink-faint)", soft: "var(--panel-2)" };
}

function Toolbar({ series, onChange, flash }: { series: SeriesT; onChange: () => void; flash: (m: string) => void }) {
  const [busy, setBusy] = useState<string | null>(null);
  const [showImport, setShowImport] = useState(false);
  const [showSearch, setShowSearch] = useState(false);
  const [showPaste, setShowPaste] = useState(false);

  const run = async (key: string, fn: () => Promise<void>) => {
    setBusy(key);
    try { await fn(); } catch (e) { flash((e as Error).message); } finally { setBusy(null); }
  };

  const btn = "rounded-lg px-3 py-2 text-[12.5px] font-semibold disabled:opacity-50";
  const ghost = { border: "1px solid var(--line)", background: "var(--panel-2)", color: "var(--ink)" } as const;

  const rename = async () => {
    const p = await api.seriesRenamePreview(series.id);
    if (p.matches) { flash("Episode files are already named correctly."); return; }
    const res = await api.renameSeries(series.id);
    flash(`Renamed ${res.renamed} file${res.renamed === 1 ? "" : "s"}.`);
    onChange();
  };

  return (
    <>
      <div className="mt-4 flex flex-wrap items-center gap-3">
        <button
          role="switch"
          aria-checked={series.monitored}
          disabled={busy !== null}
          onClick={() => run("monitor", async () => { await api.setSeriesMonitored(series.id, !series.monitored); onChange(); })}
          className="inline-flex items-center gap-2 text-[12.5px] font-semibold disabled:opacity-50"
          title={series.monitored ? "Monitored — click to stop" : "Not monitored — click to monitor"}
        >
          <span className="relative inline-block h-[22px] w-[38px] rounded-full transition-colors" style={{ background: series.monitored ? "var(--accent)" : "var(--line)" }}>
            <span className="absolute top-[3px] h-[16px] w-[16px] rounded-full bg-white transition-all" style={{ left: series.monitored ? "19px" : "3px" }} />
          </span>
          <span style={{ color: series.monitored ? "var(--ink)" : "var(--ink-dim)" }}>{series.monitored ? "Monitored" : "Monitor"}</span>
        </button>
        <button className={btn} style={ghost} disabled={busy !== null} onClick={() => run("refresh", async () => { await api.refreshSeries(series.id); onChange(); flash("Refreshed metadata and rescanned disk."); })}>
          {busy === "refresh" ? "Refreshing…" : "Refresh & rescan"}
        </button>
        <button
          className={btn}
          style={series.series_type === "anime" ? { background: "var(--accent-soft)", color: "var(--accent)", border: "1px solid var(--accent-line)" } : ghost}
          disabled={busy !== null}
          title="Anime numbers episodes 1..N across the whole run. Turn on to match releases by absolute episode number (and per-cour files). Auto-detected for Japanese animation."
          onClick={() => run("type", async () => {
            const next = series.series_type === "anime" ? "standard" : "anime";
            await api.setSeriesType(series.id, next);
            onChange();
            flash(next === "anime" ? "Anime numbering on — rescan to match absolute-numbered files." : "Standard numbering.");
          })}
        >
          {busy === "type" ? "Saving…" : series.series_type === "anime" ? "Anime ✓" : "Anime"}
        </button>
        <button className={btn} style={{ background: "linear-gradient(150deg, var(--accent), var(--accent-deep))", color: "var(--accent-ink)" }} disabled={busy !== null} onClick={() => run("search", async () => { await api.searchSeries(series.id); flash("Searching — packs and episodes will show in Downloads once grabbed."); })}>
          {busy === "search" ? "Searching…" : "Auto-grab missing"}
        </button>
        <button className={btn} style={ghost} disabled={busy !== null} onClick={() => setShowSearch(true)}>Search indexers</button>
        <button className={btn} style={ghost} disabled={busy !== null} onClick={() => setShowPaste(true)}>Paste link</button>
        <button className={btn} style={ghost} disabled={busy !== null} onClick={() => setShowImport(true)}>Manual import</button>
        <button className={btn} style={ghost} disabled={busy !== null} onClick={() => run("rename", rename)}>{busy === "rename" ? "Renaming…" : "Rename"}</button>
        <DeleteButton onDelete={async (df) => { await api.deleteSeries(series.id, df); window.location.href = "/series"; }} />
      </div>
      {showPaste && (
        <PasteLinkModal
          what={series.title}
          onPreview={(link) => api.previewLink(link)}
          onGrab={async (link, title) => { await api.grabSeriesLink(series.id, link, title); onChange(); }}
          onClose={() => setShowPaste(false)}
        />
      )}
      {showImport && <ManualImportModal series={series} onClose={() => setShowImport(false)} onImported={() => { onChange(); flash("Imported."); }} />}
      {showSearch && (
        <ReleaseSearchModal
          title={`Search indexers — ${series.title}`}
          subtitle="Whole-series search. Pick any pack or episode to grab."
          fetchReleases={() => api.seriesReleases(series.id)}
          onGrab={async (rel) => { await api.grabSeries(series.id, { indexer: rel.indexer, download_url: rel.download_url, title: rel.title }); onChange(); }}
          onClose={() => setShowSearch(false)}
        />
      )}
    </>
  );
}

function SeasonBlock({ series, season, onChange, flash, defaultOpen }: { series: SeriesT; season: Season; onChange: () => void; flash: (m: string) => void; defaultOpen: boolean }) {
  const [open, setOpen] = useState(defaultOpen);
  const [searching, setSearching] = useState(false);
  const [busy, setBusy] = useState(false);
  const eps = season.episodes ?? [];
  const have = eps.filter((e) => e.has_file).length;
  const total = eps.length;
  const airedCount = eps.filter(aired).length;
  // The progress denominator only counts episodes that have aired (or that we already have),
  // so an in-progress season isn't shown as behind on episodes that haven't come out yet.
  const counted = eps.filter((e) => e.has_file || aired(e)).length;
  const nextAir = eps.map((e) => e.air_date).filter(Boolean).sort()[0];
  // The on-disk folder for this season = the directory of any episode file we have.
  const seasonDir = (eps.find((e) => e.file_path)?.file_path ?? "").replace(/[\\/][^\\/]*$/, "");
  const state: "unreleased" | "upcoming" | "normal" = total === 0 ? "unreleased" : airedCount === 0 ? "upcoming" : "normal";
  const name = season.season_number === 0 ? "Specials" : `Season ${season.season_number}`;
  const pct = counted ? Math.round((have / counted) * 100) : 0;

  const grabSeason = async () => {
    setBusy(true);
    try { await api.autoGrabSeries(series.id, season.season_number, 0); flash(`Searching for a ${name} pack…`); }
    catch (e) { flash((e as Error).message); }
    finally { setBusy(false); }
  };

  return (
    <div className="overflow-hidden rounded-xl" style={{ background: "var(--panel)", border: "1px solid var(--line)", opacity: state === "unreleased" ? 0.7 : 1 }}>
      <div className="flex items-center gap-3 px-4 py-3">
        <button onClick={() => state !== "unreleased" && setOpen((o) => !o)} className="flex min-w-0 flex-1 items-center gap-2 text-left" style={{ cursor: state === "unreleased" ? "default" : "pointer" }}>
          <span className="font-mono text-[13px] text-ink-faint">{state === "unreleased" ? " " : open ? "▾" : "▸"}</span>
          <span className="text-[13.5px] font-semibold">{name}</span>
          {state === "unreleased" ? (
            <span className="rounded px-1.5 py-0.5 font-mono text-[9.5px] uppercase" style={{ background: "var(--panel-2)", color: "var(--ink-faint)" }}>Not yet released</span>
          ) : state === "upcoming" ? (
            <span className="rounded px-1.5 py-0.5 font-mono text-[9.5px] uppercase" style={{ background: "var(--panel-2)", color: "var(--ink-faint)" }} title={nextAir ? `First airs ${nextAir}` : undefined}>{nextAir ? `Airs ${nextAir}` : "Upcoming"}</span>
          ) : (
            <span className="flex-none font-mono text-[10.5px] text-ink-faint">{have}/{counted}</span>
          )}
          {seasonDir && <span className="min-w-0 flex-1 truncate font-mono text-[10px] text-ink-faint" title={seasonDir}>{seasonDir}</span>}
        </button>
        {state !== "unreleased" && (
          <div className="h-1.5 w-24 overflow-hidden rounded-full" style={{ background: "var(--line)" }}>
            <div className="h-full rounded-full" style={{ width: `${pct}%`, background: have >= counted && counted > 0 ? "var(--good)" : "var(--accent)" }} />
          </div>
        )}
        {state !== "unreleased" && (
          <>
            <button onClick={grabSeason} disabled={busy} title={`Auto-grab the best ${name} pack`} className="rounded-lg px-2.5 py-1 text-[11px] font-semibold" style={{ border: "1px solid var(--accent-line)", color: "var(--accent)" }}>{busy ? "…" : "Grab"}</button>
            <button onClick={() => setSearching(true)} title={`Search indexers for ${name}`} className="rounded-lg px-2.5 py-1 text-[11px] font-semibold" style={{ border: "1px solid var(--line)", color: "var(--ink-dim)" }}>Search</button>
          </>
        )}
        <button onClick={async () => { await api.setSeasonMonitored(series.id, season.season_number, !season.monitored); onChange(); }} title="Monitor this whole season" className="rounded-lg px-2.5 py-1 text-[11px] font-semibold" style={{ border: `1px solid ${season.monitored ? "var(--accent-line)" : "var(--line)"}`, color: season.monitored ? "var(--accent)" : "var(--ink-faint)" }}>
          {season.monitored ? "Monitored" : "Unmonitored"}
        </button>
      </div>
      {open && total > 0 && (
        <div style={{ borderTop: "1px solid var(--line)" }}>
          {eps.map((e) => <EpisodeRow key={e.id} series={series} ep={e} onChange={onChange} flash={flash} />)}
        </div>
      )}
      {searching && (
        <ReleaseSearchModal
          title={`${series.title} — ${name}`}
          subtitle="Season packs and episodes for this season."
          fetchReleases={() => api.seriesReleases(series.id, season.season_number)}
          onGrab={async (rel) => { await api.grabSeries(series.id, { indexer: rel.indexer, download_url: rel.download_url, title: rel.title }); onChange(); }}
          onClose={() => setSearching(false)}
        />
      )}
    </div>
  );
}

function EpisodeRow({ series, ep, onChange, flash }: { series: SeriesT; ep: Episode; onChange: () => void; flash: (m: string) => void }) {
  const [searching, setSearching] = useState(false);
  const [busy, setBusy] = useState(false);
  const dl = !ep.has_file && ep.download ? ep.download : null;
  const dlPct = dl ? Math.round(dl.progress * 100) : 0;
  const status = ep.has_file
    ? { label: "Downloaded", tone: "var(--good)" }
    : dl
      ? { label: `↓ ${dlPct}%`, tone: "var(--accent)" }
      : aired(ep) ? (ep.monitored ? { label: "Missing", tone: "var(--avoid)" } : { label: "Not monitored", tone: "var(--ink-faint)" }) : { label: "Unaired", tone: "var(--ink-faint)" };

  const grabEp = async () => {
    setBusy(true);
    try { await api.autoGrabSeries(series.id, ep.season_number, ep.episode_number); flash(`Searching for ${sxe(ep)}…`); }
    catch (e) { flash((e as Error).message); }
    finally { setBusy(false); }
  };
  const replaceEp = async () => {
    setBusy(true);
    try { await api.regrabEpisode(series.id, ep.season_number, ep.episode_number); flash(`Replacing ${sxe(ep)} — blocklisted the current release, searching…`); }
    catch (e) { flash((e as Error).message); }
    finally { setBusy(false); }
  };
  const deleteEpFile = async () => {
    if (!window.confirm(`Delete the file for ${sxe(ep)}? It goes to the recycle bin and the episode becomes wanted again.`)) return;
    setBusy(true);
    try { await api.deleteEpisodeFile(series.id, ep.season_number, ep.episode_number); flash(`Deleted ${sxe(ep)} file`); onChange(); }
    catch (e) { flash((e as Error).message); }
    finally { setBusy(false); }
  };

  return (
    <div className="relative flex items-center gap-3 px-4 py-2.5" style={{ borderBottom: "1px solid var(--line-soft)", opacity: ep.monitored ? 1 : 0.55 }}>
      <button onClick={async () => { await api.setEpisodeMonitored(ep.id, !ep.monitored); onChange(); }} title={ep.monitored ? "Monitored — click to stop" : "Not monitored — click to monitor"} className="flex-none text-[13px]" style={{ color: ep.monitored ? "var(--accent)" : "var(--ink-faint)" }}>
        {ep.monitored ? "◉" : "○"}
      </button>
      <span className="w-[64px] flex-none font-mono text-[11px] text-ink-faint">{sxe(ep)}</span>
      {series.series_type === "anime" && ep.absolute_number ? (
        <span className="flex-none rounded px-1 font-mono text-[10px]" style={{ background: "var(--accent-soft)", color: "var(--accent)" }} title="Absolute episode number">#{ep.absolute_number}</span>
      ) : null}
      <span className="min-w-0 flex-1 truncate text-[12.5px]">{ep.title || "TBA"}</span>
      <span className="hidden w-[92px] flex-none font-mono text-[10.5px] text-ink-faint sm:block">{ep.air_date || "—"}</span>
      <span className="w-[80px] flex-none text-right font-mono text-[10px] uppercase" style={{ color: status.tone }}>{status.label}</span>
      <div className="flex flex-none items-center gap-1">
        {ep.has_file ? (<>
          <button onClick={replaceEp} disabled={busy} title="Blocklist this release and grab a different one" className="rounded-md px-2 py-1 text-[10.5px] font-semibold" style={{ border: "1px solid var(--accent-line)", color: "var(--accent)" }}>{busy ? "…" : "Replace"}</button>
          <button onClick={deleteEpFile} disabled={busy} title="Delete this episode's file (to recycle bin)" className="rounded-md px-2 py-1 text-[10.5px] font-semibold" style={{ border: "1px solid var(--line)", color: "var(--reject)" }}>Delete</button>
        </>) : !dl && (
          <button onClick={grabEp} disabled={busy} title="Auto-grab the best release for this episode" className="rounded-md px-2 py-1 text-[10.5px] font-semibold" style={{ border: "1px solid var(--accent-line)", color: "var(--accent)" }}>{busy ? "…" : "Grab"}</button>
        )}
        <button onClick={() => setSearching(true)} title="Search indexers for this episode" className="rounded-md px-2 py-1 text-[10.5px] font-semibold" style={{ border: "1px solid var(--line)", color: "var(--ink-dim)" }}>Search</button>
      </div>
      {dl && (
        <div className="absolute inset-x-0 bottom-0 h-[2px]" style={{ background: "var(--panel-2)" }}>
          <div className="h-full" style={{ width: `${dlPct}%`, background: "var(--accent)", transition: "width 1s linear" }} />
        </div>
      )}
      {searching && (
        <ReleaseSearchModal
          title={`${series.title} — ${sxe(ep)}`}
          subtitle={ep.title || undefined}
          fetchReleases={() => api.seriesReleases(series.id, ep.season_number, ep.episode_number)}
          onGrab={async (rel) => { await api.grabSeries(series.id, { indexer: rel.indexer, download_url: rel.download_url, title: rel.title }); onChange(); }}
          onClose={() => setSearching(false)}
        />
      )}
    </div>
  );
}

function ManualImportModal({ series, onClose, onImported }: { series: SeriesT; onClose: () => void; onImported: () => void }) {
  const [cands, setCands] = useState<SeriesImportCandidate[] | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [importing, setImporting] = useState<string | null>(null);

  useEffect(() => {
    api.seriesManualImportList(series.id).then((r) => setCands(r.candidates)).catch((e: Error) => setError(e.message));
  }, [series.id]);

  const doImport = async (path: string) => {
    setImporting(path);
    setError(null);
    try {
      await api.seriesManualImport(series.id, path);
      onImported();
      // refresh the list so the imported file drops off
      const r = await api.seriesManualImportList(series.id);
      setCands(r.candidates);
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setImporting(null);
    }
  };

  return (
    <div className="fixed inset-0 z-50 grid place-items-start justify-center overflow-y-auto p-6" style={{ background: "rgba(0,0,0,.55)" }} onClick={onClose}>
      <div className="mt-12 w-full max-w-[680px] rounded-2xl p-5" style={{ background: "var(--panel)", border: "1px solid var(--line)", boxShadow: "var(--shadow)" }} onClick={(e) => e.stopPropagation()}>
        <div className="mb-1 flex items-center justify-between">
          <h2 className="m-0 text-[15px] font-bold">Manual import</h2>
          <button onClick={onClose} className="text-ink-faint hover:text-[var(--ink)]">✕</button>
        </div>
        <p className="mb-3 text-[12px] text-ink-dim">Pick episode files already on disk to import as <b>{series.title}</b>. Season/episode is detected from each filename; files are renamed to the library scheme.</p>
        {error && <div className="mb-2 text-[12px]" style={{ color: "var(--reject)" }}>{error}</div>}
        <div className="thin-scroll max-h-[52vh] overflow-y-auto">
          {cands === null ? (
            <div className="p-6 text-center text-[12.5px] text-ink-dim">Scanning…</div>
          ) : cands.length === 0 ? (
            <div className="p-6 text-center text-[12.5px] text-ink-dim">No importable video files found in the downloads folder.</div>
          ) : (
            cands.map((c) => {
              const detected = c.season > 0 && c.episode > 0;
              return (
                <button key={c.path} onClick={() => detected && doImport(c.path)} disabled={importing !== null || !detected} className="flex w-full items-center gap-3 rounded-lg p-2.5 text-left transition-colors hover:bg-[var(--panel-2)] disabled:opacity-50">
                  <div className="min-w-0 flex-1">
                    <div className="truncate text-[12.5px] font-medium" title={c.filename}>{c.filename}</div>
                    <div className="mt-0.5 flex items-center gap-3 font-mono text-[10.5px] text-ink-faint">
                      {detected ? <span style={{ color: "var(--accent)" }}>S{String(c.season).padStart(2, "0")}E{String(c.episode).padStart(2, "0")}</span> : <span style={{ color: "var(--avoid)" }}>No S/E detected</span>}
                      {c.quality && <span>{c.quality}</span>}
                      <span>{fmtSize(c.size_bytes)}</span>
                    </div>
                  </div>
                  <span className="flex-none rounded-lg px-3 py-1.5 text-[11.5px] font-semibold" style={{ background: "var(--accent-soft)", color: "var(--accent)" }}>{importing === c.path ? "Importing…" : "Import"}</span>
                </button>
              );
            })
          )}
        </div>
      </div>
    </div>
  );
}

function ProfileSelector({ series, onChange }: { series: SeriesT; onChange: () => void }) {
  const [profiles, setProfiles] = useState<{ key: string; name: string }[]>([]);
  const [fallbackName, setFallbackName] = useState<string | null>(null);
  const cur = series.quality_profile;
  useEffect(() => { api.qualityProfiles("series").then((r) => setProfiles(r.profiles.map((p) => ({ key: p.key, name: p.name })))).catch(() => {}); }, []);
  useEffect(() => {
    if (!cur || cur === "n/a" || profiles.some((p) => p.key === cur)) { setFallbackName(null); return; }
    api.qualityProfile(cur).then((sp) => setFallbackName(sp.name || cur)).catch(() => setFallbackName(cur));
  }, [cur, profiles]);
  const change = async (p: string) => { if (p === cur) return; await api.setSeriesProfile(series.id, p); onChange(); };
  const opts = [...profiles];
  if (cur && !opts.some((o) => o.key === cur)) opts.unshift({ key: cur, name: cur === "n/a" ? "Not set" : (fallbackName ?? "…") });
  return (
    <div className="flex items-center gap-2">
      <span className="font-mono text-[10.5px] uppercase text-ink-faint">Quality</span>
      <select value={series.quality_profile} onChange={(e) => change(e.target.value)} className="rounded-lg px-2.5 py-1.5 text-[12px] font-medium" style={{ background: "var(--accent-soft)", border: "1px solid var(--line)", color: "var(--accent)" }}>
        {opts.map((p) => <option key={p.key} value={p.key} style={{ background: "var(--panel)", color: "var(--ink)" }}>{p.name}</option>)}
      </select>
    </div>
  );
}

function DeleteButton({ onDelete }: { onDelete: (deleteFiles: boolean) => void }) {
  const [confirm, setConfirm] = useState(false);
  const [deleteFiles, setDeleteFiles] = useState(true);
  if (!confirm) return <button onClick={() => setConfirm(true)} className="rounded-lg px-3 py-2 text-[12.5px] font-semibold" style={{ border: "1px solid var(--reject)", color: "var(--reject)" }}>Delete</button>;
  return (
    <div className="flex items-center gap-2">
      <label className="flex items-center gap-1.5 text-[11.5px] text-ink-dim" title="Also delete every episode file from disk">
        <input type="checkbox" checked={deleteFiles} onChange={(e) => setDeleteFiles(e.target.checked)} /> delete files
      </label>
      <button onClick={() => onDelete(deleteFiles)} className="rounded-lg px-3 py-2 text-[12.5px] font-semibold" style={{ background: "var(--reject)", color: "#fff" }}>{deleteFiles ? "Remove + files" : "Remove series"}</button>
      <button onClick={() => setConfirm(false)} className="rounded-lg px-2 py-2 text-[12.5px]" style={{ border: "1px solid var(--line)", color: "var(--ink-dim)" }}>✕</button>
    </div>
  );
}

const EVENT_TONES: Record<string, string> = {
  added: "var(--ink-faint)",
  grabbed: "var(--accent)",
  imported: "var(--good)",
  upgraded: "var(--good)",
  renamed: "var(--ink-dim)",
  failed: "var(--avoid)",
  refreshed: "var(--ink-faint)",
};

function fmtTime(s: string): string {
  const d = new Date(s.includes("T") ? s : s.replace(" ", "T") + "Z");
  if (isNaN(d.getTime())) return s;
  return d.toLocaleString(undefined, { month: "short", day: "numeric", hour: "2-digit", minute: "2-digit" });
}

function HistoryPanel({ seriesId, refreshKey }: { seriesId: number; refreshKey: unknown }) {
  const [events, setEvents] = useState<MovieEvent[] | null>(null);
  useEffect(() => {
    api.seriesHistory(seriesId).then(setEvents).catch(() => setEvents([]));
  }, [seriesId, refreshKey]);
  return (
    <div className="mt-8">
      <h2 className="m-0 mb-3 text-[14px] font-bold">History</h2>
      {events === null ? (
        <div className="text-[12.5px] text-ink-dim">Loading…</div>
      ) : events.length === 0 ? (
        <div className="rounded-xl p-6 text-center text-[12.5px] text-ink-dim" style={{ border: "1px solid var(--line)" }}>No activity yet.</div>
      ) : (
        <div className="flex flex-col">
          {events.map((e, i) => (
            <div key={i} className="flex items-center gap-3 border-b py-2 text-[12px]" style={{ borderColor: "var(--line)" }}>
              <span className="w-[74px] flex-none font-mono text-[10px] font-bold uppercase" style={{ color: EVENT_TONES[e.event] ?? "var(--ink-dim)" }}>{e.event}</span>
              <span className="min-w-0 flex-1 truncate text-ink-dim" title={e.detail}>{e.detail || "—"}</span>
              <span className="flex-none font-mono text-[10.5px] text-ink-faint">{fmtTime(e.created_at)}</span>
            </div>
          ))}
        </div>
      )}
    </div>
  );
}

function Shell({ children }: { children: React.ReactNode }) {
  return <><PageHeader title="Series" crumb="Library / Series" /><div className="mx-auto w-full max-w-[1200px] px-4 py-6 sm:px-6">{children}</div></>;
}

// SeriesBlocklistPanel mirrors the movie blocklist: releases rejected via Replace/stall-failover,
// with a Remove action. Hidden when empty.
function SeriesBlocklistPanel({ seriesId, refreshKey }: { seriesId: number; refreshKey: unknown }) {
  const [entries, setEntries] = useState<BlockEntry[] | null>(null);
  const load = () => api.seriesBlocklist(seriesId).then(setEntries).catch(() => setEntries([]));
  useEffect(() => {
    load();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [seriesId, refreshKey]);
  const unblock = async (bid: number) => { await api.unblockSeries(seriesId, bid); load(); };
  if (!entries || entries.length === 0) return null;
  return (
    <div className="mt-8">
      <h2 className="m-0 mb-3 text-[14px] font-bold">Blocklist <span className="font-normal text-ink-faint">· {entries.length}</span></h2>
      <div className="flex flex-col gap-2">
        {entries.map((e) => (
          <div key={e.id} className="flex items-center gap-3 rounded-lg p-2.5 text-[12px]" style={{ border: "1px solid var(--line)", background: "var(--panel)" }}>
            <div className="min-w-0 flex-1">
              <div className="truncate font-mono text-[11.5px]" title={e.title}>{e.title}</div>
              <div className="mt-0.5 text-[10.5px] text-ink-faint">{e.reason}{e.indexer ? ` · ${e.indexer}` : ""}</div>
            </div>
            <button onClick={() => unblock(e.id)} className="flex-none rounded-lg px-2.5 py-1 text-[11px] font-semibold" style={{ border: "1px solid var(--line)", color: "var(--ink-dim)" }}>Remove</button>
          </div>
        ))}
      </div>
    </div>
  );
}
