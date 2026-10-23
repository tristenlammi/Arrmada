import { Suspense, lazy, useCallback, useEffect, useId, useRef, useState, type ReactNode } from "react";
import { Link, useNavigate, useParams } from "react-router-dom";
import { PageHeader } from "../components/PageHeader";
import { CastRow } from "../components/CastRow";
import { shownOverview, shownTitle } from "../lib/displayTitle";
import { AudioLangChips } from "../components/AudioLangChips";
import { ReleaseSearchModal } from "../components/ReleaseSearchModal";
import { UploadTorrentModal } from "../components/UploadTorrentModal";
import { FileDetailsModal } from "../components/FileDetailsModal";
import { ConfirmDialog } from "../components/ConfirmDialog";
import { DeleteMovieDialog } from "../components/DeleteMovieDialog";
import { MovieRenameModal } from "../components/MovieRenameModal";
import { MoveFolderDialog } from "../components/MoveFolderDialog";
import { useLibraryFolders } from "../components/FolderSelect";
import { folderLabel } from "../lib/foldersApi";
import { LastSearchLine } from "../components/LastSearch";
import { TitleWatchPills, WatchedBy } from "../components/PlexBits";
import { disposalLine, useRecycleMode } from "../lib/disposal";
import { PAGE } from "../lib/links";
import { usePoll } from "../lib/usePoll";
import { useUnsavedGuard } from "../lib/useUnsavedGuard";
import { invalidate } from "../lib/query";
import {
  api,
  importListNotice,
  type AttemptSummary,
  type BlockEntry,
  type CollectionMember,
  type ImportCandidate,
  type Movie,
  type MovieEvent,
  type MovieDownload,
  type MovieFile,
  type MovieVersion,
} from "../lib/api";
import { qualityApi } from "../lib/qualityApi";
import { convertApi } from "../lib/convertApi";
import { useLiveEvent } from "../lib/live";
import { QUEUE_CHANGED, QUEUE_PROGRESS, progressByHash, queueChangedFor } from "../lib/queueLive";
import { jobFailed, useJob } from "../lib/useJob";
import { attemptLine, searchJobLine } from "../lib/searchOutcome";
import { queuedNote } from "../lib/movieQueue";
import { formatPercent } from "../lib/format";
import { Button, Menu, Modal, StatusChip, useToast } from "../ui";
import { Switch } from "../ui/Switch";
import { orphanApi } from "./subtitles/orphanApi";
import { MovieSubtitles } from "../components/subtitles/MovieSubtitles";
import { TitleProfilePicker } from "./subtitles/ProfileSelect";
import { movieStatus, trackStatus } from "../lib/movieStatus";
import { Page } from "../ui/Page";

// The "Display title…" picker: loaded when it opens.
const DisplayTitleDialog = lazy(() => import("../components/DisplayTitleDialog"));

const AVAILABILITY_LABELS: Record<string, string> = {
  announced: "Announced",
  inCinemas: "In cinemas",
  released: "Released",
};

// Events that can change what this page shows. movie.* and search.finished are narrowed to
// this movie where the event names one.
const MOVIE_TOPICS = ["download.imported", "release.grabbed", "file.removed", "movie.refreshed", "movie.renamed", "search.finished"];
// The download bar's backstop when queue.progress and queue.changed move it.
const DOWNLOAD_FALLBACK_MS = 30_000;

export function MovieDetail() {
  const { id } = useParams<{ id: string }>();
  const movieId = Number(id);
  const [movie, setMovie] = useState<Movie | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [notFound, setNotFound] = useState(false);
  // Bumped by anything that changes this movie's History or Blocklist (a grab, an import,
  // a block, a search), so those panels refresh without leaving the page.
  const [activity, setActivity] = useState(0);

  // err shows the toast in the error colour: a search that failed says so plainly.
  const toast = useToast();
  const say = (msg: string, err = false) => toast(msg, { tone: err ? "error" : "info", ms: 3500 });

  const load = useCallback(() => {
    return api
      .movie(movieId)
      .then((m) => {
        setMovie(m);
        setError(null);
      })
      .catch((e: Error) => {
        if (e.message.toLowerCase().includes("not found")) setNotFound(true);
        else setError(e.message);
      });
  }, [movieId]);

  useEffect(() => {
    load();
  }, [load]);

  // While a download is in progress the bar moves from queue.progress (matched by the
  // torrent's hash), and a queue.changed naming it — finished, removed, errored — reads the
  // movie again: the file may have landed. A slow check of only its progress (not the
  // whole movie) backs that up; when it ends the movie is read again too.
  const downloading = !!movie?.download;
  const dlHash = movie?.download?.hash?.toLowerCase() ?? "";
  const pollDownload = async () => {
    try {
      const r = await api.movieDownloads(movieId);
      const d = r.downloads.find((x) => x.movie_id === movieId);
      if (!d) {
        void load();
        return;
      }
      setMovie((m) => (m ? { ...m, download: { state: d.state, progress: d.progress, hash: d.hash, kind: d.kind, version_id: d.version_id, version_label: d.version_label } } : m));
    } catch {
      /* the next tick tries again */
    }
  };
  // Without a hash (an old grab) only the poll can move the bar, so it stays quick.
  usePoll(pollDownload, downloading ? (dlHash ? DOWNLOAD_FALLBACK_MS : 3000) : null, { immediate: false });
  useLiveEvent([QUEUE_PROGRESS, QUEUE_CHANGED], (ev) => {
    if (!dlHash) return;
    if (queueChangedFor(ev, [dlHash])) { void load(); return; }
    const p = progressByHash(ev)?.get(dlHash);
    if (p) setMovie((m) => (m?.download ? { ...m, download: { ...m.download, progress: p.progress } } : m));
  }, downloading);

  // A grab, an import, a removed file or this movie's own refresh, rename or search reads
  // it again, and refreshes its History and Blocklist panels.
  useLiveEvent(MOVIE_TOPICS, (ev) => {
    const d = ev.data as { media_type?: string; media_id?: number; id?: number } | null;
    if (ev.topic === "search.finished" && !(d?.media_type === "movie" && d.media_id === movieId)) return;
    if (ev.topic.startsWith("movie.") && typeof d?.id === "number" && d.id !== movieId) return;
    load();
    setActivity((n) => n + 1);
  });

  if (notFound) {
    return (
      <>
        <PageHeader title="Movie" />
        <Page className="py-10 text-center text-md text-ink-dim">
          That movie isn't in your library.{" "}
          <Link to="/movies" className="underline" style={{ color: "var(--accent-text)" }}>Back to Movies</Link>
        </Page>
      </>
    );
  }

  if (!movie) {
    return (
      <>
        <PageHeader title="Movie" />
        <Page className="py-10 text-md text-ink-dim">
          {error ? <span style={{ color: "var(--reject-text)" }}>{error}</span> : "Loading…"}
        </Page>
      </>
    );
  }

  const st = movieStatus(movie);
  const ex = movie.extra;

  return (
    <>
      <PageHeader title={shownTitle(movie)} />

      {/* Hero band with backdrop */}
      <div className="relative">
        {ex?.backdrop_url && (
          <>
            <div
              className="pointer-events-none absolute inset-0 bg-cover bg-center opacity-[0.18]"
              style={{ backgroundImage: `url(${ex.backdrop_url})` }}
            />
            <div className="pointer-events-none absolute inset-0" style={{ background: "linear-gradient(180deg, transparent, var(--bg))" }} />
          </>
        )}
        <Page className="relative py-6">
          <Link to="/movies" className="mb-4 inline-flex items-center gap-1 text-sm text-ink-dim hover:text-[var(--ink)]">
            <svg width="14" height="14" viewBox="0 0 24 24" fill="none"><path d="M15 19l-7-7 7-7" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" /></svg>
            All movies
          </Link>

          <div className="flex flex-col gap-6 sm:flex-row">
            <div className="w-[180px] flex-none overflow-hidden rounded-xl" style={{ border: "1px solid var(--line)", aspectRatio: "2/3" }}>
              {movie.poster_url ? (
                <img src={movie.poster_url} alt={shownTitle(movie)} className="h-full w-full object-cover" />
              ) : (
                <div className="flex h-full w-full items-end p-3" style={{ background: "linear-gradient(150deg, hsl(24 40% 30%), hsl(20 35% 16%))" }}>
                  <span className="text-lg font-bold text-white">{shownTitle(movie)}</span>
                </div>
              )}
            </div>

            <div className="min-w-0 flex-1">
              <div className="flex flex-wrap items-center gap-2.5">
                <span className="rounded-full px-2.5 py-1 font-mono text-2xs font-semibold uppercase" style={{ background: st.soft, color: st.color }}>{st.label}</span>
                {ex?.certification && (
                  <span className="rounded px-1.5 py-0.5 font-mono text-xs font-bold" style={{ border: "1px solid var(--line)", color: "var(--ink-dim)" }}>{ex.certification}</span>
                )}
                <span className="font-mono text-xs text-ink-faint">{movie.year || "—"}</span>
                {shownTitle(movie) !== movie.title && (
                  <span className="text-xs text-ink-faint" title="TMDB's English title: matching and file names use it">{movie.title}</span>
                )}
                {movie.runtime ? <span className="font-mono text-xs text-ink-faint">{fmtRuntime(movie.runtime)}</span> : null}
                {typeof ex?.vote_average === "number" && ex.vote_average > 0 && (
                  <span className="font-mono text-xs" style={{ color: "var(--accent-text)" }}>★ {ex.vote_average.toFixed(1)}</span>
                )}
                <ExternalLinks movie={movie} />
              </div>
              <WatchedBy kind="movies" id={movie.id} />

              <p className="mt-3 text-md leading-relaxed text-ink-dim">{shownOverview(movie) || "No overview available."}</p>

              <FactRow movie={movie} />

              <div className="mt-4 flex flex-wrap items-center gap-4">
                <ProfileSelector movie={movie} onChange={load} />
                <AvailabilitySelector movie={movie} onChange={load} />
                <TitleProfilePicker kind="movie" id={movie.id} title={movie.title} />
              </div>

              <AcquisitionStatus movie={movie} onChange={load} flash={say} searchInfo={{ summary: movie.last_search, nextAt: movie.next_search_at }} />
              <UpgradeHoldChip movie={movie} onChange={load} flash={say} />
              <Toolbar movie={movie} onChange={load} flash={say} />
            </div>
          </div>
        </Page>
      </div>

      <Page className="pb-10">
        {movie.download && <DownloadBar dl={movie.download} />}
        <VersionsArea movie={movie} onChange={load} flash={say} />
        <CastRow cast={movie.extra?.cast} />
        <BlocklistPanel movieId={movie.id} refreshKey={`${movie.has_file}:${activity}`} />
        <HistoryPanel movieId={movie.id} refreshKey={`${movie.has_file}:${activity}`} />
      </Page>
    </>
  );
}

function fmtRuntime(min: number): string {
  const h = Math.floor(min / 60);
  const m = min % 60;
  return h > 0 ? `${h}h ${m}m` : `${m}m`;
}

function fmtSize(bytes: number): string {
  if (bytes <= 0) return "—";
  const gb = bytes / 1024 ** 3;
  if (gb >= 1) return `${gb.toFixed(2)} GB`;
  return `${(bytes / 1024 ** 2).toFixed(0)} MB`;
}

function DownloadBar({ dl }: { dl: MovieDownload }) {
  const pctNum = Math.min(100, Math.max(0, dl.progress * 100));
  const pctText = formatPercent(dl.progress);
  // An upgrade or an extra version says what it is; the film's own file just downloads.
  const what = dl.kind === "upgrade" ? "Downloading an upgrade" : dl.kind === "version" ? `Downloading ${dl.version_label || "an extra version"}` : "Downloading";
  return (
    <div className="mt-6 rounded-xl p-3.5" style={{ border: "1px solid var(--accent)", background: "var(--accent-soft)" }}>
      <div className="mb-2 flex items-center justify-between text-sm">
        <span className="font-semibold" style={{ color: "var(--accent-text)" }}>{what} — {dl.state}</span>
        <span className="font-mono text-ink-dim">{pctText}</span>
      </div>
      <div className="h-2 overflow-hidden rounded-full" style={{ background: "var(--line)" }}>
        <div className="h-full rounded-full transition-all" style={{ width: `${Math.round(pctNum)}%`, background: "var(--accent)" }} />
      </div>
    </div>
  );
}

// VersionsArea keeps single-file movies looking exactly as before (one file
// panel), and switches to a multi-track view once an extra version is added.
function VersionsArea({ movie, onChange, flash }: { movie: Movie; onChange: () => void; flash: (m: string) => void }) {
  const [adding, setAdding] = useState(false);
  const [profileNames, setProfileNames] = useState<Record<string, string>>({});
  const versions = movie.versions ?? [];
  const extras = versions.filter((v) => !v.is_default);

  useEffect(() => {
    qualityApi
      .qualityProfiles("movie")
      .then((r) => setProfileNames(Object.fromEntries(r.profiles.map((p) => [p.key, p.name]))))
      .catch(() => {});
  }, []);
  const profileName = (ref: string) => (ref === "n/a" ? "Not set" : profileNames[ref] ?? ref);
  // After a missing file's record is cleared, a monitored movie gets a one-click search.
  const [cleared, setCleared] = useState(false);
  const onCleared = () => { setCleared(true); onChange(); };
  const clearedNotice = cleared && (
    <ClearedNotice movie={movie} flash={flash} onDismiss={() => setCleared(false)} />
  );

  if (extras.length === 0) {
    return (
      <>
        {clearedNotice}
        {movie.file && <FilePanel file={movie.file} movieId={movie.id} audio={versions[0]} onChange={onChange} onCleared={onCleared} />}
        <button onClick={() => setAdding(true)} className="mt-4 inline-flex items-center gap-1.5 rounded-lg px-3 py-1.5 text-sm font-semibold" style={{ border: "1px dashed var(--line)", color: "var(--ink-dim)" }}>
          ＋ Keep another version <span className="text-ink-faint">(e.g. 1080p + 4K, or a Director's Cut)</span>
        </button>
        {adding && <VersionFormModal movieId={movie.id} onClose={() => setAdding(false)} onSaved={() => { setAdding(false); onChange(); flash("Version added — searching for it."); }} />}
      </>
    );
  }

  return (
    <div className="mt-6">
      {clearedNotice}
      <div className="mb-3 flex items-center justify-between">
        <h2 className="m-0 text-md font-bold">Versions <span className="font-normal text-ink-faint">· {versions.length} tracks</span></h2>
        <button onClick={() => setAdding(true)} className="rounded-lg px-3 py-1.5 text-sm font-semibold" style={{ border: "1px solid var(--line)", background: "var(--panel-2)", color: "var(--ink)" }}>＋ Add version</button>
      </div>
      <div className="flex flex-col gap-2.5">
        {versions.map((v) => (
          <VersionCard key={`${v.is_default ? "d" : v.id}`} movie={movie} version={v} onChange={onChange} onCleared={onCleared} flash={flash} profileName={profileName} />
        ))}
      </div>
      {adding && <VersionFormModal movieId={movie.id} onClose={() => setAdding(false)} onSaved={() => { setAdding(false); onChange(); flash("Version added — searching for it."); }} />}
    </div>
  );
}

// ClearedNotice follows a cleared missing-file record: nothing on disk was touched, and a
// monitored movie can be searched for right away instead of waiting for the next sweep.
function ClearedNotice({ movie, flash, onDismiss }: { movie: Movie; flash: (m: string, err?: boolean) => void; onDismiss: () => void }) {
  const [busy, setBusy] = useState(false);
  const search = async () => {
    setBusy(true);
    try {
      const r = await api.searchMovie(movie.id);
      const note = queuedNote(r);
      flash(note ? `${note} — it searches when its turn comes.` : `Searching — follow it in ${PAGE.activity} → Wanted.`);
      onDismiss();
    } catch (e) {
      flash((e as Error).message, true);
    } finally {
      setBusy(false);
    }
  };
  return (
    <div className="mt-6 flex flex-wrap items-center gap-3 rounded-xl p-3.5 text-sm text-ink-dim" style={{ border: "1px solid var(--line)", background: "var(--panel)" }}>
      <span className="min-w-0 flex-1">
        Record cleared — nothing on disk was touched.
        {movie.monitored ? " Arrmada will look for it on its next sweep, or search now." : " Turn on Monitor for Arrmada to look for it."}
      </span>
      {movie.monitored && <Button size="sm" variant="primary" onClick={search} busy={busy} busyLabel="Searching…">Search now</Button>}
      <Button size="sm" variant="ghost" onClick={onDismiss}>Dismiss</Button>
    </div>
  );
}

// trackLine is what a track is doing, in words: its own download, or why it's still
// wanted (the movie's last search), or that it's left alone.
function trackLine(movie: Movie, version: MovieVersion): string {
  const dl = movie.download;
  if (dl && (dl.version_id ?? 0) === version.id) return `Downloading — ${formatPercent(dl.progress)}`;
  if (version.has_file) return "";
  if (!version.monitored) return "Not monitored — Arrmada won't look for it.";
  // The movie's searches cover every track at once, so its last one speaks for this too.
  const last = movie.last_search?.latest;
  const why = last ? `${attemptLine(last)}.` : "Arrmada looks for it on its sweeps.";
  const tail = version.edition ? ` Only releases named as the ${version.edition} count.` : "";
  return `Wanted. ${why}${tail}`;
}

// VersionCard is one track: the same file panel the movie's only file gets (details,
// subtitles, the missing state), its own status, and its own Search, Import, Edit and
// confirmed deletes — searching or importing from a track lands the file in that track.
function VersionCard({ movie, version, onChange, onCleared, flash, profileName }: { movie: Movie; version: MovieVersion; onChange: () => void; onCleared: () => void; flash: (m: string, err?: boolean) => void; profileName: (ref: string) => string }) {
  const movieId = movie.id;
  const [busy, setBusy] = useState(false);
  const f = version.file;

  const toggleMonitor = async () => {
    setBusy(true);
    try {
      if (version.is_default) await api.setMonitored(movieId, !version.monitored);
      else await api.updateVersion(movieId, version.id, { label: version.label, quality_profile: version.quality_profile, edition: version.edition, monitored: !version.monitored });
      onChange();
    } catch (e) {
      flash((e as Error).message, true);
    } finally {
      setBusy(false);
    }
  };

  // Both destructive buttons ask first, naming the file, its size and where it goes.
  const [confirm, setConfirm] = useState<"file" | "version" | null>(null);
  const [panel, setPanel] = useState<"search" | "import" | "edit" | null>(null);

  const status = trackStatus(version);
  const missing = status.key === "missing";
  const line = trackLine(movie, version);
  const small = "rounded-lg px-2.5 py-1 text-xs font-semibold disabled:opacity-50";
  const ghost = { border: "1px solid var(--line)", background: "var(--panel-2)", color: "var(--ink)" } as const;

  return (
    <div data-testid="version-card" className="rounded-xl p-3.5" style={{ border: `1px solid ${missing ? "var(--avoid)" : "var(--line)"}`, background: "var(--panel)" }}>
      <div className="flex flex-wrap items-center gap-2">
        <span className="text-md font-semibold">{version.label}</span>
        {version.is_default && <span className="rounded px-1.5 py-0.5 font-mono text-2xs font-bold uppercase" style={{ background: "var(--panel-2)", color: "var(--ink-faint)" }}>Default</span>}
        <span className="rounded px-1.5 py-0.5 font-mono text-2xs uppercase" style={{ background: "var(--accent-soft)", color: "var(--accent-text)" }}>{profileName(version.quality_profile)}</span>
        {version.edition && <span className="rounded px-1.5 py-0.5 text-xs" style={{ background: "var(--panel-2)", color: "var(--ink-dim)" }}>{version.edition}</span>}
        <StatusChip tone={status.tone}>{status.label}</StatusChip>
      </div>
      {line && <div className="mt-1.5 text-sm text-ink-dim" role="status">{line}</div>}
      {f && <FileBody movieId={movieId} versionId={version.id} file={f} audio={version} onAdopted={version.is_default ? onChange : undefined} />}
      <div className="mt-2.5 flex flex-wrap items-center gap-1.5">
        <button onClick={toggleMonitor} disabled={busy} className={small} style={ghost} aria-pressed={version.monitored}>{version.monitored ? "Monitored" : "Monitor"}</button>
        <button onClick={() => setPanel("search")} disabled={busy} className={small} style={ghost}>Search</button>
        <button onClick={() => setPanel("import")} disabled={busy} className={small} style={ghost}>Import</button>
        {!version.is_default && <button onClick={() => setPanel("edit")} disabled={busy} className={small} style={ghost}>Edit</button>}
        {f && <button onClick={() => setConfirm("file")} disabled={busy} className={small} style={missing ? ghost : { border: "1px solid var(--reject)", color: "var(--reject-text)" }}>{missing ? "Clear record" : "Delete file"}</button>}
        {!version.is_default && <button onClick={() => setConfirm("version")} disabled={busy} className="rounded-lg px-2.5 py-1 text-xs" style={{ color: "var(--ink-faint)" }}>Remove version</button>}
      </div>
      {panel === "search" && (
        <ReleaseSearchModal
          title={`Search indexers — ${movie.title} · ${version.label}`}
          subtitle={`Ranked for this version's profile${version.edition ? ` and the ${version.edition} edition` : ""}. A grab lands in this version.`}
          fetchReleases={() => api.movieReleases(movieId, version.id)}
          onGrab={async (rel) => { await api.grab({ token: rel.token ?? "", movie_id: movieId, version_id: version.id }); onChange(); }}
          onBlock={async (rel) => { await api.blockRelease(movieId, { token: rel.token ?? "", search_again: true }); flash(`Blocklisted "${rel.summary}" — searching for an alternate.`); onChange(); }}
          onClose={() => setPanel(null)}
        />
      )}
      {panel === "import" && (
        <ManualImportModal movie={movie} versionId={version.id} onClose={() => setPanel(null)} onImported={() => { setPanel(null); onChange(); flash(`Imported into ${version.label}.`); }} />
      )}
      {panel === "edit" && (
        <VersionFormModal movieId={movieId} version={version} onClose={() => setPanel(null)} onSaved={(queued) => { setPanel(null); onChange(); flash(queued ? `Saved "${version.label}" — searching under the new settings.` : "Version saved."); }} />
      )}
      {confirm === "file" && f && missing && (
        <ClearRecordDialog
          movieId={movieId}
          versionId={version.id}
          file={f}
          onDone={() => { setConfirm(null); onCleared(); }}
          onCancel={() => setConfirm(null)}
        />
      )}
      {confirm === "file" && f && !missing && (
        <FileDeleteDialog
          title={<>Delete the “{version.label}” file?</>}
          file={f}
          note="The version stays, so Arrmada looks for it again while it's monitored."
          confirmLabel="Delete file"
          run={() => api.deleteVersionFile(movieId, version.id)}
          onDone={() => { setConfirm(null); onChange(); }}
          onCancel={() => setConfirm(null)}
        />
      )}
      {confirm === "version" && (
        <FileDeleteDialog
          title={<>Remove the “{version.label}” version?</>}
          file={f}
          note="Arrmada stops looking for this version."
          confirmLabel="Remove version"
          run={() => api.deleteVersion(movieId, version.id)}
          onDone={() => { setConfirm(null); onChange(); flash(`Removed "${version.label}" version.`); }}
          onCancel={() => setConfirm(null)}
        />
      )}
    </div>
  );
}

// FileDeleteDialog asks before one of a movie's files goes. It names the file and its size,
// says honestly where it goes (the recycle bin, or gone for good when the bin is off), and
// keeps the server's refusal on screen instead of closing as if it had worked.
function FileDeleteDialog({ title, file, note, confirmLabel, run, onDone, onCancel }: {
  title: ReactNode;
  file?: MovieFile | null;
  note: string;
  confirmLabel: string;
  run: () => Promise<unknown>;
  onDone: () => void;
  onCancel: () => void;
}) {
  const mode = useRecycleMode();
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  const confirm = async () => {
    setBusy(true); setErr(null);
    try {
      await run();
      onDone();
    } catch (e) {
      setErr((e as Error).message);
      setBusy(false);
    }
  };
  let where = "";
  if (file) where = file.missing ? "The file is already gone from disk — this only clears the record." : `${disposalLine(file.size_bytes, mode)}. Its subtitles go with it.`;
  return (
    <ConfirmDialog
      title={title}
      body={
        <>
          {file && <div className="mt-1 break-all font-mono text-sm" style={{ color: "var(--ink)" }}>{file.filename || file.path}</div>}
          <p className="mb-0 mt-1.5">{where} {note}</p>
        </>
      }
      confirmLabel={confirmLabel}
      busyLabel="Deleting…"
      busy={busy}
      error={err}
      onConfirm={confirm}
      onCancel={onCancel}
    />
  );
}

// ClearRecordDialog forgets a track whose file is gone from disk. Unlike Delete file it
// never touches the disk: the server checks the file is really gone and refuses (409) when
// it's back, and that refusal stays on screen.
function ClearRecordDialog({ movieId, versionId, file, onDone, onCancel }: {
  movieId: number;
  versionId: number;
  file: MovieFile;
  onDone: () => void;
  onCancel: () => void;
}) {
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  const confirm = async () => {
    setBusy(true); setErr(null);
    try {
      await api.forgetMissingFile(movieId, versionId);
      onDone();
    } catch (e) {
      setErr((e as Error).message);
      setBusy(false);
    }
  };
  return (
    <ConfirmDialog
      title="Clear the missing file's record?"
      body={
        <>
          <div className="mt-1 break-all font-mono text-sm" style={{ color: "var(--ink)" }}>{file.filename || file.path}</div>
          <p className="mb-0 mt-1.5">Nothing on disk is touched — the file is already gone. The movie stays, so Arrmada searches for it again while it's monitored.</p>
        </>
      }
      confirmLabel="Clear record"
      busyLabel="Clearing…"
      busy={busy}
      error={err}
      onConfirm={confirm}
      onCancel={onCancel}
    />
  );
}

// EditionField picks a version's edition from the ones release names are recognised by,
// or "Other…" for free text. An edition is a requirement: the version only grabs releases
// that carry it, so the hint says so.
function EditionField({ value, onChange }: { value: string; onChange: (v: string) => void }) {
  const [known, setKnown] = useState<string[]>([]);
  const [other, setOther] = useState(false);
  useEffect(() => {
    api.movieEditions().then(setKnown).catch(() => {});
  }, []);
  const isKnown = value === "" || known.includes(value);
  const showText = other || (!isKnown && known.length > 0);
  const fieldStyle = { background: "var(--panel-2)", border: "1px solid var(--line)", color: "var(--ink)" };
  return (
    <>
      <label htmlFor="version-edition" className="mb-1 block font-mono text-2xs font-bold uppercase text-accent-text">Edition <span className="text-ink-faint">(optional)</span></label>
      <select
        id="version-edition"
        value={showText ? "__other" : value}
        onChange={(e) => {
          if (e.target.value === "__other") { setOther(true); onChange(""); }
          else { setOther(false); onChange(e.target.value); }
        }}
        className="mb-2 w-full rounded-lg px-3 py-2 text-base"
        style={fieldStyle}
      >
        <option value="">Any — no particular edition</option>
        {known.map((e) => <option key={e} value={e}>{e}</option>)}
        <option value="__other">Other…</option>
      </select>
      {showText && (
        <input aria-label="Edition name" value={value} onChange={(e) => onChange(e.target.value)} placeholder="e.g. Ultimate Cut" className="mb-2 w-full rounded-lg px-3 py-2 text-md" style={fieldStyle} />
      )}
      <p className="mb-4 mt-0 text-xs text-ink-faint">
        {value.trim() ? `Only releases whose names say “${value.trim()}” are grabbed for this version, and its file is named as a Plex edition.` : "Any release that fits the profile can fill this version."}
      </p>
    </>
  );
}

// VersionFormModal adds a version, or edits one (version given): its label, quality profile
// and edition. Saving a new profile or edition searches under it; onSaved says whether a
// search was queued.
function VersionFormModal({ movieId, version, onClose, onSaved }: { movieId: number; version?: MovieVersion; onClose: () => void; onSaved: (queued: boolean) => void }) {
  const editing = !!version;
  const labelRef = useRef<HTMLInputElement>(null);
  const [label, setLabel] = useState(version?.label ?? "");
  const [profile, setProfile] = useState(version?.quality_profile ?? "");
  const [edition, setEdition] = useState(version?.edition ?? "");
  const [profiles, setProfiles] = useState<{ key: string; name: string }[]>([]);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState<string | null>(null);
  // A new version's profile fills in from the default by itself, so only an edit's counts.
  useUnsavedGuard(label !== (version?.label ?? "") || edition !== (version?.edition ?? "") || (editing && profile !== (version?.quality_profile ?? "")));

  useEffect(() => {
    qualityApi.qualityProfiles("movie").then((r) => {
      setProfiles(r.profiles.map((p) => ({ key: p.key, name: p.name })));
      if (editing) return; // keep the version's own profile
      const def = r.profiles.find((p) => p.is_default) ?? r.profiles[0];
      if (def) setProfile(def.key);
    }).catch(() => {});
  }, [editing]);

  const save = async () => {
    if (!label.trim()) {
      setError("Give the version a label (e.g. 4K, Director's Cut).");
      return;
    }
    setSaving(true);
    setError(null);
    try {
      if (version) {
        const r = await api.updateVersion(movieId, version.id, { label: label.trim(), quality_profile: profile, edition: edition.trim(), monitored: version.monitored });
        onSaved(!!r.queued);
      } else {
        await api.addVersion(movieId, { label: label.trim(), quality_profile: profile, edition: edition.trim(), monitored: true });
        onSaved(true);
      }
    } catch (e) {
      setError((e as Error).message);
      setSaving(false);
    }
  };

  const fieldStyle = { background: "var(--panel-2)", border: "1px solid var(--line)", color: "var(--ink)" };
  // A saved profile that's no longer in the list still shows, named by its key.
  const listed = profile === "" || profiles.some((p) => p.key === profile);
  return (
    <Modal
      onClose={onClose}
      title={editing ? `Edit “${version?.label}”` : "Add a version"}
      size="sm"
      dismissible={!saving}
      initialFocus={labelRef}
      footer={
        <>
          <Button variant="ghost" onClick={onClose} disabled={saving}>Cancel</Button>
          <Button variant="primary" onClick={save} busy={saving} busyLabel={editing ? "Saving…" : "Adding…"}>{editing ? "Save" : "Add version"}</Button>
        </>
      }
    >
      <p className="mb-4 mt-0 text-sm text-ink-dim">
        {editing ? "Changes apply to this version's next search; its file stays where it is." : "A separate track with its own quality target — kept alongside your existing file, not replacing it."}
      </p>
      <label htmlFor="version-label" className="mb-1 block font-mono text-2xs font-bold uppercase text-accent-text">Label</label>
      <input ref={labelRef} id="version-label" value={label} onChange={(e) => setLabel(e.target.value)} placeholder="4K · Director's Cut · Remux" className="mb-3 w-full rounded-lg px-3 py-2 text-md" style={fieldStyle} />

      <label htmlFor="version-profile" className="mb-1 block font-mono text-2xs font-bold uppercase text-accent-text">Quality profile</label>
      <select id="version-profile" value={profile} onChange={(e) => setProfile(e.target.value)} className="mb-3 w-full rounded-lg px-3 py-2 text-base" style={fieldStyle}>
        {!listed && <option value={profile}>{profile}</option>}
        {profiles.map((p) => <option key={p.key} value={p.key}>{p.name}</option>)}
      </select>

      <EditionField value={edition} onChange={setEdition} />
      {error && <div className="mb-1 text-sm" style={{ color: "var(--reject-text)" }}>{error}</div>}
    </Modal>
  );
}

function ExternalLinks({ movie }: { movie: Movie }) {
  return (
    <span className="flex items-center gap-2">
      {movie.imdb_id && (
        <a href={`https://www.imdb.com/title/${movie.imdb_id}`} target="_blank" rel="noreferrer" className="rounded px-1.5 py-0.5 font-mono text-xs font-bold" style={{ background: "#f5c518", color: "#000" }}>IMDb</a>
      )}
      <a href={`https://www.themoviedb.org/movie/${movie.tmdb_id}`} target="_blank" rel="noreferrer" className="rounded px-1.5 py-0.5 font-mono text-xs font-bold" style={{ background: "#01b4e4", color: "#0d253f" }}>TMDB</a>
      <TitleWatchPills media="movie" tmdbId={movie.tmdb_id} />
    </span>
  );
}

function FactRow({ movie }: { movie: Movie }) {
  const ex = movie.extra;
  const [showCollection, setShowCollection] = useState(false);
  if (!ex) return null;
  return (
    <div className="mt-3 flex flex-col gap-2 text-sm">
      {ex.genres && ex.genres.length > 0 && (
        <div className="flex flex-wrap items-center gap-1.5">
          {ex.genres.map((g) => (
            <span key={g} className="rounded-full px-2 py-0.5 text-xs" style={{ background: "var(--panel-2)", color: "var(--ink-dim)" }}>{g}</span>
          ))}
        </div>
      )}
      <div className="flex flex-wrap items-center gap-x-5 gap-y-1 text-ink-faint">
        {ex.studios && ex.studios.length > 0 && <span><span className="text-ink-faint">Studio</span> <span className="text-ink-dim">{ex.studios.slice(0, 2).join(", ")}</span></span>}
        {ex.original_language && <span><span className="text-ink-faint">Language</span> <span className="text-ink-dim uppercase">{ex.original_language}</span></span>}
        {ex.collection_name && (
          <button onClick={() => setShowCollection(true)} className="inline-flex items-center gap-1 rounded-md px-2 py-0.5 text-sm font-medium" style={{ background: "var(--accent-soft)", color: "var(--accent-text)", border: "1px solid var(--accent-line)" }}>
            {ex.collection_name} <span className="text-xs">· view collection</span>
          </button>
        )}
      </div>
      {showCollection && <CollectionModal movieId={movie.id} onClose={() => setShowCollection(false)} />}
    </div>
  );
}

function CollectionModal({ movieId, onClose }: { movieId: number; onClose: () => void }) {
  const [name, setName] = useState("");
  const titleId = useId();
  const [members, setMembers] = useState<CollectionMember[]>([]);
  const [loading, setLoading] = useState(true);
  const [profile, setProfile] = useState("");
  const [adding, setAdding] = useState<Set<number>>(new Set());
  const [error, setError] = useState<string | null>(null);

  const load = useCallback(() => {
    api.movieCollection(movieId)
      .then((r) => { setName(r.name); setMembers(r.members); })
      .catch((e: Error) => setError(e.message))
      .finally(() => setLoading(false));
  }, [movieId]);

  useEffect(() => {
    load();
    qualityApi.qualityProfiles("movie").then((r) => {
      const def = r.profiles.find((p) => p.is_default) ?? r.profiles[0];
      if (def) setProfile(def.key);
    }).catch(() => {});
  }, [load]);

  const addOne = async (tmdbId: number) => {
    setAdding((s) => new Set(s).add(tmdbId));
    setError(null);
    try {
      await api.addMovie({ tmdb_id: tmdbId, quality_profile: profile, monitored: true });
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setAdding((s) => { const n = new Set(s); n.delete(tmdbId); return n; });
      load();
    }
  };

  const missing = members.filter((m) => !m.in_library);
  // One call for the lot (MOV-23): the server adds them in turn, skips any already there
  // and queues their searches; the collection reloads once at the end.
  const addAll = async () => {
    const ids = missing.map((m) => m.tmdb_id);
    setAdding(new Set(ids));
    setError(null);
    try {
      const r = await api.bulkAddMovies({ tmdb_ids: ids, quality_profile: profile, monitored: true });
      const failed = r.results.filter((x) => x.status === "failed");
      if (failed.length) setError(`${failed.length} couldn't be added: ${failed[0].error ?? "unknown error"}`);
    } catch (e) {
      setError((e as Error).message);
    } finally {
      setAdding(new Set());
      load();
    }
  };

  return (
    <Modal onClose={onClose} labelledBy={titleId} size="md">
      <div className="mb-3 flex items-center justify-between gap-3">
        <div>
          <h2 id={titleId} className="m-0 text-lg font-bold">{name || "Collection"}</h2>
          <p className="m-0 mt-0.5 text-sm text-ink-dim">{members.length} films · {missing.length} not in your library</p>
        </div>
        {missing.length > 0 && (
          <button onClick={addAll} disabled={!profile || adding.size > 0} className="flex-none rounded-lg px-3.5 py-2 text-base font-semibold" style={{ background: "linear-gradient(150deg, var(--accent), var(--accent-deep))", color: "var(--accent-ink)", opacity: !profile || adding.size > 0 ? 0.6 : 1 }}>
            {adding.size > 0 ? "Adding…" : `Add all ${missing.length}`}
          </button>
        )}
      </div>
      {error && <div className="mb-3 rounded-lg p-2.5 text-sm" style={{ border: "1px solid var(--reject)", color: "var(--reject-text)" }}>{error}</div>}
      {loading ? (
        <p className="py-6 text-center text-base text-ink-dim">Loading collection…</p>
      ) : (
        <div className="flex max-h-[60vh] flex-col gap-1.5 overflow-y-auto">
          {members.map((m) => (
            <div key={m.tmdb_id} className="flex items-center gap-3 rounded-lg p-2" style={{ background: "var(--panel-2)" }}>
              {m.poster_url
                ? <img src={m.poster_url} alt="" className="h-14 w-9 flex-none rounded object-cover" />
                : <div className="h-14 w-9 flex-none rounded" style={{ background: "var(--line)" }} />}
              <div className="min-w-0 flex-1">
                <div className="truncate text-md font-semibold">{m.title}</div>
                <div className="text-xs text-ink-faint">{m.year || "—"}{m.vote_average ? ` · ★ ${m.vote_average.toFixed(1)}` : ""}</div>
              </div>
              {m.in_library ? (
                <span className="flex-none rounded px-2 py-1 font-mono text-2xs uppercase" style={{ background: "var(--good-soft)", color: "var(--good-text)" }}>In library</span>
              ) : (
                <button onClick={() => addOne(m.tmdb_id)} disabled={!profile || adding.has(m.tmdb_id)} className="flex-none rounded-lg px-3 py-1.5 text-sm font-semibold" style={{ border: "1px solid var(--accent-line)", color: "var(--accent-text)" }}>
                  {adding.has(m.tmdb_id) ? "Adding…" : "Add"}
                </button>
              )}
            </div>
          ))}
        </div>
      )}
    </Modal>
  );
}

function ProfileSelector({ movie, onChange }: { movie: Movie; onChange: () => void }) {
  const [saving, setSaving] = useState(false);
  const [saved, setSaved] = useState(false);
  const [profiles, setProfiles] = useState<{ key: string; name: string }[]>([]);

  useEffect(() => {
    qualityApi
      .qualityProfiles("movie")
      .then((r) => setProfiles(r.profiles.map((p) => ({ key: p.key, name: p.name }))))
      .catch(() => {});
  }, []);

  // Set when the file on disk doesn't fit the newly chosen profile (see DowngradePrompt).
  const [downgrade, setDowngrade] = useState<Downgrade | null>(null);
  const profileId = useId();

  const change = async (profile: string) => {
    if (profile === movie.quality_profile) return;
    setSaving(true);
    setSaved(false);
    setDowngrade(null);
    try {
      const res = await api.setQualityProfile(movie.id, profile);
      const d = downgradeOf(res);
      if (d) {
        setDowngrade(d);
      } else {
        setSaved(true);
        window.setTimeout(() => setSaved(false), 2000);
      }
      onChange();
    } finally {
      setSaving(false);
    }
  };

  return (
    <div className="flex flex-col gap-2">
      <div className="flex flex-wrap items-center gap-2">
        <label htmlFor={profileId} className="font-mono text-2xs uppercase text-ink-faint">Quality</label>
        <select id={profileId} value={movie.quality_profile} onChange={(e) => change(e.target.value)} disabled={saving} className="rounded-lg px-2.5 py-1.5 text-sm font-medium" style={{ background: "var(--accent-soft)", border: "1px solid var(--line)", color: "var(--accent-text)" }}>
          {(() => {
            const opts = [...profiles];
            // Surface an unset ("n/a") profile as a real option so the select shows it.
            const cur = movie.quality_profile;
            if (cur && !opts.some((o) => o.key === cur)) opts.unshift({ key: cur, name: cur === "n/a" ? "Not set (n/a)" : cur });
            return opts.map((p) => (
              <option key={p.key} value={p.key} style={{ background: "var(--panel)", color: "var(--ink)" }}>{p.name}</option>
            ));
          })()}
        </select>
        {saved && <span className="text-xs" style={{ color: "var(--good-text)" }}>Saved ✓</span>}
      </div>
      {downgrade && <DowngradePrompt movieId={movie.id} downgrade={downgrade} onClose={() => setDowngrade(null)} />}
    </div>
  );
}

/** Why the file on disk doesn't fit a newly chosen profile: whether a smaller release fixes
 * it (above the profile's ceiling) or it needs a different one altogether. */
interface Downgrade {
  kind: "smaller" | "different";
  reason: string;
  ceiling: string;
}

// downgradeOf reads a profile change's answer; null when the file still fits.
function downgradeOf(res: { downgrade: boolean; downgrade_kind?: "smaller" | "different"; downgrade_reason?: string; downgrade_ceiling?: string }): Downgrade | null {
  if (!res.downgrade) return null;
  return { kind: res.downgrade_kind ?? "different", reason: res.downgrade_reason ?? "", ceiling: res.downgrade_ceiling ?? "" };
}

// DowngradePrompt asks what to do when a new profile doesn't fit the file on disk: download
// a release that does (a smaller one, when the file is only over the ceiling), or keep the
// file. Nothing happens on its own. ProfileSelector and AcquisitionStatus both show it.
function DowngradePrompt({ movieId, downgrade, onClose }: { movieId: number; downgrade: Downgrade; onClose: () => void }) {
  const [regrabbing, setRegrabbing] = useState(false);
  const doRegrab = async () => {
    setRegrabbing(true);
    try {
      await api.regrabMovie(movieId);
      onClose();
    } finally {
      setRegrabbing(false);
    }
  };
  return (
    <div className="rounded-lg p-3 text-sm" style={{ background: "var(--avoid-soft)", border: "1px solid var(--avoid)" }}>
      <div className="mb-2 text-ink-dim">
        {downgrade.kind === "smaller"
          ? `Your file is above this profile's ${downgrade.ceiling || "size"} ceiling. Download a smaller release, or keep it?`
          : `Your file doesn't meet this profile${downgrade.reason ? ` (${downgrade.reason})` : ""}. Find a release that does, or keep it?`}
      </div>
      <div className="flex gap-2">
        <button onClick={doRegrab} disabled={regrabbing} className="rounded-lg px-3 py-1.5 text-sm font-semibold" style={{ background: "var(--accent)", color: "var(--accent-ink)" }}>
          {regrabbing ? "Searching…" : downgrade.kind === "smaller" ? "Download smaller version" : "Find a matching release"}
        </button>
        <button onClick={onClose} className="rounded-lg px-3 py-1.5 text-sm font-semibold" style={{ border: "1px solid var(--line)", color: "var(--ink-dim)" }}>Keep current file</button>
      </div>
    </div>
  );
}

function AvailabilitySelector({ movie, onChange }: { movie: Movie; onChange: () => void }) {
  const whenId = useId();
  const [saving, setSaving] = useState(false);
  const change = async (v: string) => {
    if (v === movie.min_availability) return;
    setSaving(true);
    try {
      await api.setAvailability(movie.id, v);
      onChange();
    } finally {
      setSaving(false);
    }
  };
  return (
    <div className="flex flex-wrap items-center gap-2">
      <label htmlFor={whenId} className="font-mono text-2xs uppercase text-ink-faint">Search when</label>
      <select id={whenId} value={movie.min_availability} onChange={(e) => change(e.target.value)} disabled={saving} className="rounded-lg px-2.5 py-1.5 text-sm font-medium" style={{ background: "var(--panel-2)", border: "1px solid var(--line)", color: "var(--ink)" }}>
        {Object.entries(AVAILABILITY_LABELS).map(([key, label]) => (
          <option key={key} value={key}>{label}</option>
        ))}
      </select>
    </div>
  );
}

/** The last search and the next automatic try (MOV-04, from search_attempts: the movie's
 * last_search and next_search_at). */
interface AcquisitionSearchInfo {
  /** How the stored searches went: the latest, the empty tries behind it, the main reason. */
  summary?: AttemptSummary;
  /** When the missing-sweep searches next; absent when no automatic search is coming. */
  nextAt?: string;
}

// AcquisitionStatus says what Arrmada will do about this movie, built only from the facts
// the sweeps act on (movie.acquisition, from the server): a scanned-in or unmonitored film
// is never upgraded, a profile with upgrades off keeps its file, a missing film is searched
// for once it's available, and a recorded file that's gone is never "you have this movie".
function AcquisitionStatus({ movie, onChange, flash, searchInfo }: {
  movie: Movie;
  onChange: () => void;
  flash: (m: string, err?: boolean) => void;
  searchInfo?: AcquisitionSearchInfo;
}) {
  const acq = movie.acquisition;
  const [downgrade, setDowngrade] = useState<Downgrade | null>(null);
  const [busy, setBusy] = useState<string | null>(null);
  const [clearing, setClearing] = useState(false);
  if (!acq) return null;

  const run = async (key: string, fn: () => Promise<void>) => {
    setBusy(key);
    try {
      await fn();
    } catch (e) {
      flash((e as Error).message, true);
    } finally {
      setBusy(null);
    }
  };
  const noProfile = acq.profile_known ? "" : " It has no quality profile of its own, so your default profile applies.";
  const avail = AVAILABILITY_LABELS[movie.min_availability] ?? movie.min_availability;

  let tone = "var(--ink-dim)";
  let msg: ReactNode;
  let actions: ReactNode = null;

  if (acq.file_missing) {
    tone = "var(--reject-text)";
    msg = "Arrmada has a record of this file but it isn't on disk. Refresh & rescan to look again; if it's really gone, clear the record and Arrmada will search for it (when monitored).";
    actions = (
      <>
        <Button size="sm" onClick={() => run("rescan", async () => { await api.refreshMovie(movie.id); onChange(); flash("Refreshed metadata and rescanned disk."); })} busy={busy === "rescan"} busyLabel="Rescanning…" disabled={busy !== null}>Refresh & rescan</Button>
        {movie.file && <Button size="sm" onClick={() => setClearing(true)} disabled={busy !== null}>Clear record</Button>}
        {acq.monitored && (
          <Button size="sm" variant="primary" disabled={busy !== null} busy={busy === "search"} busyLabel="Searching…"
            onClick={() => run("search", async () => {
              await api.forgetMissingFile(movie.id, 0);
              const note = queuedNote(await api.searchMovie(movie.id));
              flash(note ? `Cleared the missing file's record. ${note} — it searches when its turn comes.` : `Cleared the missing file's record — searching. Follow it in ${PAGE.activity} → Wanted.`);
              onChange();
            })}>Search</Button>
        )}
      </>
    );
  } else if (acq.downloading) {
    tone = "var(--accent-text)";
    const pctText = formatPercent(acq.download_progress ?? 0);
    const what = acq.download_title ? <span className="break-all font-mono text-sm">{acq.download_title}</span> : "a release";
    msg = <>{movie.has_file ? "Downloading an upgrade: " : "Downloading "}{what} — {pctText}.</>;
  } else if (movie.has_file && !acq.monitored) {
    tone = "var(--good-text)";
    msg = acq.scanned_in
      ? "Found in your library and not monitored, so it won't be upgraded. Monitor it with a profile to let Arrmada look for better releases."
      : "You have this movie. It isn't monitored, so it won't be upgraded.";
    actions = <MonitorWithProfile movie={movie} onDone={onChange} onDowngrade={setDowngrade} flash={flash} />;
  } else if (movie.has_file && !acq.upgrades_allowed) {
    tone = "var(--good-text)";
    msg = `You have this movie. Your profile doesn't allow upgrades, so this file stays as it is.${noProfile}`;
  } else if (movie.has_file && movie.upgrade_hold) {
    tone = "var(--good-text)";
    msg = "You have this movie. Its file was kept as it is when its profile changed, so upgrades won't replace it until you resume them.";
  } else if (movie.has_file) {
    tone = "var(--good-text)";
    msg = `You have this movie. Arrmada looks for a clearly better release every 6 hours and grabs it automatically.${noProfile}`;
  } else if (!acq.monitored) {
    tone = "var(--avoid-text)";
    msg = "Not monitored, so Arrmada won't search for it.";
    actions = (
      <Button size="sm" variant="primary" disabled={busy !== null} busy={busy === "monitor"} busyLabel="Monitoring…"
        onClick={() => run("monitor", async () => { await api.setMonitored(movie.id, true); onChange(); })}>Monitor</Button>
    );
  } else if (!acq.available) {
    tone = "var(--accent-text)";
    const from = acq.available_from ? ` (expected ${fmtDate(acq.available_from)})` : "";
    msg = `Monitored and missing. Arrmada starts searching once it's "${avail}"${from}, and grabs the best release for your quality profile.${noProfile}`;
  } else {
    tone = "var(--accent-text)";
    msg = `Monitored and missing — Arrmada searches automatically and grabs the best release for your quality profile.${noProfile}`;
  }

  return (
    <div className="mt-4 rounded-lg p-3 text-sm leading-relaxed" style={{ border: "1px solid var(--line)" }}>
      <div style={{ color: tone }}>{msg}</div>
      {/* The searches only answer "why isn't it downloading?": the summary leaves upgrade
          searches out, so a film with its file on disk doesn't show them. */}
      {(!movie.has_file || acq.file_missing) && <LastSearchLine summary={searchInfo?.summary} nextSearchAt={searchInfo?.nextAt} className="mt-1.5" />}
      {actions && <div className="mt-2.5 flex flex-wrap items-center gap-2">{actions}</div>}
      {downgrade && <div className="mt-2.5"><DowngradePrompt movieId={movie.id} downgrade={downgrade} onClose={() => setDowngrade(null)} /></div>}
      {clearing && movie.file && (
        <ClearRecordDialog
          movieId={movie.id}
          versionId={0}
          file={movie.file}
          onDone={() => { setClearing(false); onChange(); flash("Record cleared — nothing on disk was touched."); }}
          onCancel={() => setClearing(false)}
        />
      )}
    </div>
  );
}

// MonitorWithProfile monitors a film that has a file and puts it on a chosen profile in one
// step. Monitoring comes first, so the profile change is judged like any other: a profile
// that targets less than the file asks before downloading anything (onDowngrade).
function MonitorWithProfile({ movie, onDone, onDowngrade, flash }: {
  movie: Movie;
  onDone: () => void;
  onDowngrade: (d: Downgrade) => void;
  flash: (m: string, err?: boolean) => void;
}) {
  const [profiles, setProfiles] = useState<{ key: string; name: string }[]>([]);
  const [pick, setPick] = useState("");
  const [busy, setBusy] = useState(false);
  useEffect(() => {
    qualityApi.qualityProfiles("movie").then((r) => {
      setProfiles(r.profiles.map((p) => ({ key: p.key, name: p.name })));
      const own = r.profiles.find((p) => p.key === movie.quality_profile);
      const def = own ?? r.profiles.find((p) => p.is_default) ?? r.profiles[0];
      if (def) setPick(def.key);
    }).catch(() => {});
  }, [movie.quality_profile]);

  const monitor = async () => {
    setBusy(true);
    try {
      await api.setMonitored(movie.id, true);
      if (pick && pick !== movie.quality_profile) {
        const res = await api.setQualityProfile(movie.id, pick);
        const d = downgradeOf(res);
        if (d) onDowngrade(d);
      }
      flash("Monitored — Arrmada now looks for better releases under this profile.");
      onDone();
    } catch (e) {
      flash((e as Error).message, true);
    } finally {
      setBusy(false);
    }
  };

  return (
    <>
      <select aria-label="Quality profile" value={pick} onChange={(e) => setPick(e.target.value)} disabled={busy || profiles.length === 0} className="rounded-lg px-2.5 py-1.5 text-sm font-medium" style={{ background: "var(--panel-2)", border: "1px solid var(--line)", color: "var(--ink)" }}>
        {profiles.map((p) => <option key={p.key} value={p.key}>{p.name}</option>)}
      </select>
      <Button size="sm" variant="primary" onClick={monitor} busy={busy} busyLabel="Monitoring…" disabled={!pick}>Monitor</Button>
    </>
  );
}

function fmtDate(s: string): string {
  const d = new Date(s + "T00:00:00");
  if (isNaN(d.getTime())) return s;
  return d.toLocaleDateString(undefined, { day: "numeric", month: "short", year: "numeric" });
}

// UpgradeHoldChip shows when a file of this movie was kept as it is when its profile
// changed ("keep existing files"), with Resume to let upgrades replace it again. A hold
// nobody can see is a file that silently never upgrades, so it's always on the page.
function UpgradeHoldChip({ movie, onChange, flash }: { movie: Movie; onChange: () => void; flash: (m: string, err?: boolean) => void }) {
  const [busy, setBusy] = useState(false);
  const held = (movie.versions ?? []).filter((v) => v.has_file && v.upgrade_hold);
  if (held.length === 0 && !(movie.has_file && movie.upgrade_hold)) return null;
  // Name the tracks when it's not simply the movie's one file.
  const which = movie.versions && movie.versions.length > 1 && held.length > 0 ? ` (${held.map((v) => v.label).join(", ")})` : "";
  const resume = async () => {
    setBusy(true);
    try {
      await qualityApi.resumeMovieUpgrades(movie.id);
      flash("Upgrades resumed — the next sweep can replace this file again.");
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
      <span>Kept when the profile changed{which}</span>
      <Button size="sm" variant="secondary" onClick={resume} busy={busy} busyLabel="Resuming…">Resume</Button>
    </div>
  );
}

function Toolbar({ movie, onChange, flash }: { movie: Movie; onChange: () => void; flash: (m: string, err?: boolean) => void }) {
  const [busy, setBusy] = useState<string | null>(null);
  // The search runs as a job; when it ends the page says what it actually found — in a
  // toast, and on a line under the buttons that stays until the next search.
  const [searchJob, setSearchJob] = useState<number | null>(null);
  const [searchResult, setSearchResult] = useState<{ text: string; failed: boolean } | null>(null);
  const search = useJob(searchJob, {
    onDone: (j) => {
      setSearchJob(null);
      const text = searchJobLine(j);
      setSearchResult({ text, failed: jobFailed(j) });
      flash(text, jobFailed(j));
      onChange();
    },
  });
  const [showImport, setShowImport] = useState(false);
  const [showSearch, setShowSearch] = useState(false);
  const [showPaste, setShowPaste] = useState(false);
  const [showDelete, setShowDelete] = useState(false);
  const navigate = useNavigate();

  const run = async (key: string, fn: () => Promise<void>) => {
    setBusy(key);
    try {
      await fn();
    } catch (e) {
      flash((e as Error).message);
    } finally {
      setBusy(null);
    }
  };

  const btn = "rounded-lg px-3 py-2 text-base font-semibold disabled:opacity-50";
  const ghost = { border: "1px solid var(--line)", background: "var(--panel-2)", color: "var(--ink)" } as const;

  // A recorded file that's gone from disk blocks the search (the track reads as having a
  // file), so Auto-grab clears that record first. The server refuses if the file is back.
  const missing = movie.has_file && !!movie.file?.missing;
  const autoGrab = async () => {
    setSearchResult(null);
    if (missing) await api.forgetMissingFile(movie.id, 0);
    const r = await api.searchMovie(movie.id);
    if (r.job_id) setSearchJob(r.job_id);
    const note = queuedNote(r);
    const what = note ? `${note} — it searches when its turn comes.` : `Searching — follow it in ${PAGE.activity} → Wanted.`;
    flash(missing ? `Cleared the missing file's record. ${what}` : what);
    onChange();
  };

  // Rename previews every track (old → new) in a modal before anything moves.
  const [showRename, setShowRename] = useState(false);
  const hasAnyFile = movie.has_file || !!movie.versions?.some((v) => v.has_file);
  // More than one Movies folder: the film's folder shows, and it can move to another one.
  // With one folder neither appears.
  const folders = useLibraryFolders().data?.movie;
  const multiFolder = !!folders?.multi;
  const [showMove, setShowMove] = useState(false);
  const [showTitle, setShowTitle] = useState(false);
  const name = shownTitle(movie);

  // At most one primary button, and it matches the movie's state: Search when it's
  // monitored and has no file (or its file is gone), Search for upgrade when it has a
  // file the upgrade sweep would still replace. Nothing while a download is running.
  const acq = movie.acquisition;
  const primary: "search" | "upgrade" | null = acq?.downloading
    ? null
    : movie.monitored && (!movie.has_file || missing)
      ? "search"
      : movie.has_file && !missing && (acq?.upgrades_allowed ?? movie.upgrades_allowed) && !movie.upgrade_hold
        ? "upgrade"
        : null;
  const accentBtn = { background: "linear-gradient(150deg, var(--accent), var(--accent-deep))", color: "var(--accent-ink)" } as const;
  const searchUpgrade = async () => {
    const r = await api.bulkMovieSearch([movie.id], "upgrade");
    flash(r.duplicates > 0 ? "An upgrade search for this movie is already queued." : "Looking for an upgrade — it searches when its turn in the queue comes.");
  };

  return (
    <>
      <div className="mt-4 flex flex-wrap items-center gap-3">
        <Switch size="sm" checked={movie.monitored} disabled={busy !== null}
          title={movie.monitored ? "Monitored — click to stop" : "Not monitored — click to monitor"}
          onChange={() => run("monitor", async () => { await api.setMonitored(movie.id, !movie.monitored); onChange(); })}>
          {movie.monitored ? "Monitored" : "Monitor"}
        </Switch>
        {primary === "search" && (
          <button
            className={btn}
            style={accentBtn}
            disabled={busy !== null || search.running}
            title={missing ? "Clears the missing file's record (nothing on disk is touched), then searches" : "Searches your indexers and grabs the best release for the profile"}
            onClick={() => run("search", autoGrab)}
          >
            {busy === "search" || search.running ? "Searching…" : "Search"}
          </button>
        )}
        {primary === "upgrade" && (
          <button
            className={btn}
            style={accentBtn}
            disabled={busy !== null}
            title="Looks for a release clearly better than the file you have, under this movie's profile"
            onClick={() => run("upgrade", searchUpgrade)}
          >
            {busy === "upgrade" ? "Queuing…" : "Search for upgrade"}
          </button>
        )}
        {/* Everything else lives in one menu, reachable by keyboard and on touch. */}
        <Menu
          portal
          label="More actions"
          trigger={<span aria-hidden className="text-lg leading-none">⋯</span>}
          triggerClassName={`${btn} min-w-[40px]`}
          triggerStyle={ghost}
          itemClassName="py-2"
          items={[
            { label: "Interactive search", onSelect: () => setShowSearch(true) },
            { label: "Manual import", onSelect: () => setShowImport(true) },
            { label: "Upload .torrent or .nzb", onSelect: () => setShowPaste(true) },
            ...(hasAnyFile ? [{ label: "Rename…", onSelect: () => setShowRename(true) }] : []),
            ...(multiFolder ? [{ label: "Move to folder…", onSelect: () => setShowMove(true) }] : []),
            { label: "Display title…", onSelect: () => setShowTitle(true) },
            { label: "Refresh & rescan", busyLabel: "Refreshing…", onSelect: () => run("refresh", async () => { await api.refreshMovie(movie.id); onChange(); flash("Refreshed metadata and rescanned disk."); }) },
            { label: "Delete movie…", tone: "danger" as const, onSelect: () => setShowDelete(true) },
          ]}
        />
        {multiFolder && (
          <span className="text-sm text-ink-faint" title={folders?.folders.find((f) => f.id === (movie.library_folder_id ?? 0))?.path}>
            Folder: <b className="font-semibold text-ink-dim">{folderLabel(folders, movie.library_folder_id)}</b>
          </span>
        )}
      </div>
      {showMove && folders && (
        <MoveFolderDialog
          media="movie" id={movie.id} title={name} current={movie.library_folder_id ?? 0} folders={folders}
          onClose={() => setShowMove(false)}
          onMoved={() => { onChange(); flash(`Moved “${name}”.`); }}
        />
      )}
      {showTitle && (
        <Suspense fallback={null}>
          <DisplayTitleDialog
            media="movies" id={movie.id} title={movie.title} override={movie.title_override ?? ""}
            onClose={() => setShowTitle(false)}
            onSaved={() => { invalidate("movies"); onChange(); }}
          />
        </Suspense>
      )}
      {(search.running || searchResult) && (
        <div className="mt-2 text-sm" role="status" style={{ color: search.running ? "var(--ink-dim)" : searchResult?.failed ? "var(--reject-text)" : "var(--ink)" }}>
          {search.running ? "Searching…" : searchResult?.text}
        </div>
      )}
      {showDelete && <DeleteMovieDialog movie={movie} onClose={() => setShowDelete(false)} onDeleted={() => { invalidate("movies"); navigate("/movies"); }} />}
      {showRename && <MovieRenameModal movieId={movie.id} title={name} onClose={() => setShowRename(false)} onRenamed={onChange} />}
      {showPaste && (
        <UploadTorrentModal
          what={name}
          onPreview={(torrent) => api.previewTorrent(torrent)}
          onGrab={async (torrent, filename, title) => { await api.grabMovieTorrent(movie.id, torrent, filename, title); onChange(); }}
          onClose={() => setShowPaste(false)}
        />
      )}
      {showImport && <ManualImportModal movie={movie} onClose={() => setShowImport(false)} onImported={() => { setShowImport(false); onChange(); flash("Imported."); }} />}
      {showSearch && (
        <ReleaseSearchModal
          title={`Search indexers — ${name}`}
          subtitle="Pick a release to grab, or blocklist one to search for an alternate."
          fetchReleases={() => api.movieReleases(movie.id)}
          onGrab={async (rel) => { await api.grab({ token: rel.token ?? "", movie_id: movie.id }); onChange(); }}
          onBlock={async (rel) => { await api.blockRelease(movie.id, { token: rel.token ?? "", search_again: true }); flash(`Blocklisted "${rel.summary}" — searching for an alternate.`); onChange(); }}
          onClose={() => setShowSearch(false)}
        />
      )}
    </>
  );
}

function FilePanel({ file, movieId, audio, onChange, onCleared }: { file: MovieFile; movieId: number; audio?: TrackAudio; onChange: () => void; onCleared: () => void }) {
  const [confirming, setConfirming] = useState(false);
  const tone = file.missing ? "var(--avoid)" : "var(--good)";

  return (
    <div className="mt-6 rounded-xl p-4" style={{ border: `1px solid ${tone}`, background: "var(--panel)" }}>
      <div className="flex items-start justify-between gap-4">
        <div className="min-w-0">
          <div className="font-mono text-2xs uppercase" style={{ color: tone }}>{file.missing ? "File missing from disk" : "On disk"}</div>
          <FileBody movieId={movieId} versionId={0} file={file} audio={audio} onAdopted={onChange} />
        </div>
        <div className="flex-none">
          <button onClick={() => setConfirming(true)} className="rounded-lg px-3 py-1.5 text-sm font-semibold" style={file.missing ? { border: "1px solid var(--line)", color: "var(--ink)" } : { border: "1px solid var(--reject)", color: "var(--reject-text)" }}>{file.missing ? "Clear record" : "Delete file"}</button>
        </div>
      </div>
      {confirming && file.missing && (
        <ClearRecordDialog
          movieId={movieId}
          versionId={0}
          file={file}
          onDone={() => { setConfirming(false); onCleared(); }}
          onCancel={() => setConfirming(false)}
        />
      )}
      {confirming && !file.missing && (
        <FileDeleteDialog
          title="Delete this file?"
          file={file}
          note="The movie stays, so Arrmada looks for it again while it's monitored."
          confirmLabel="Delete file"
          run={() => api.deleteMovieFile(movieId)}
          onDone={() => { setConfirming(false); onChange(); }}
          onCancel={() => setConfirming(false)}
        />
      )}
    </div>
  );
}

// FileBody is what every track's file shows, whichever track it is: quality and media
// chips, size and runtime, the path (opening the file's details), its subtitles, the ones
// that pair with no video (with Adopt, on the file Subtitles works on), and the
// missing-from-disk note.
// TrackAudio is the part of a track the language badges read.
type TrackAudio = Pick<MovieVersion, "audio_langs" | "audio_lang_fit">;

function FileBody({ movieId, versionId, file, audio, onAdopted }: { movieId: number; versionId: number; file: MovieFile; audio?: TrackAudio; onAdopted?: () => void }) {
  const [showFile, setShowFile] = useState(false);
  const [adopting, setAdopting] = useState<string | null>(null);
  const toast = useToast();
  // Adopt renames the orphan to pair with this file, so Plex shows it.
  const adopt = async (name: string) => {
    setAdopting(name);
    try {
      const r = await orphanApi.adopt(movieId, name);
      toast(`Adopted — now ${r.name}`);
      onAdopted?.();
    } catch (e) { toast((e as Error).message, { tone: "error" }); } finally { setAdopting(null); }
  };
  const subNames = file.subtitles && file.subtitles.length > 0 && (
    <div className="mt-1.5 flex flex-wrap items-center gap-1.5 text-xs text-ink-dim">
      <span className="font-mono uppercase text-ink-faint">Subs</span>
      {file.subtitles.map((s) => (
        <span key={s} className="rounded px-1.5 py-0.5" style={{ background: "var(--panel-2)" }}>{s}</span>
      ))}
    </div>
  );
  const chips: string[] = [];
  if (file.codec) chips.push(file.codec);
  if (file.audio) chips.push(...file.audio);
  if (file.hdr) chips.push(...file.hdr);

  return (
    <>
      <div className="mt-1.5 flex flex-wrap items-center gap-2 text-base">
        {file.quality && <span className="rounded px-2 py-0.5 text-xs font-semibold" style={{ background: "var(--accent-soft)", color: "var(--accent-text)" }}>{file.quality}</span>}
        {chips.map((c) => (
          <span key={c} className="rounded px-2 py-0.5 text-xs" style={{ background: "var(--panel-2)", color: "var(--ink-dim)" }}>{c}</span>
        ))}
        {!file.missing && <AudioLangChips langs={audio?.audio_langs} fit={audio?.audio_lang_fit} />}
        <span className="font-mono text-ink-dim">{fmtSize(file.size_bytes)}</span>
        {file.duration_min ? <span className="font-mono text-xs text-ink-faint">{file.duration_min}m</span> : null}
        {file.group && <span className="font-mono text-xs text-ink-faint">{file.group}</span>}
        {file.probed && <span title="Read from the actual file (ffprobe)" className="font-mono text-xs" style={{ color: "var(--good-text)" }}>✓ probed</span>}
      </div>
      <button
        onClick={() => setShowFile(true)}
        title="Show this file's details — media info and the release it came from"
        className="mt-1.5 block break-all text-left font-mono text-sm text-ink-faint hover:underline"
      >
        {file.path}
      </button>
      {/* The file Subtitles works on shows each kept language's state and Find subtitles;
          another version's file lists its sidecars by name. */}
      {onAdopted && !file.missing ? <MovieSubtitles movieId={movieId} fallback={subNames} onChanged={onAdopted} /> : subNames}
      {/* Subtitles named for no video in the folder: Plex won't show them with this file. */}
      {file.orphan_subtitles && file.orphan_subtitles.length > 0 && (
        <div className="mt-1 flex flex-wrap items-center gap-1.5 text-xs text-ink-faint" title="Named for no video in this folder, so Plex won't show them with this file">
          <span className="font-mono uppercase">Not linked to this file</span>
          {file.orphan_subtitles.map((s) => (
            <span key={s} className="inline-flex items-center gap-1">
              <span className="rounded px-1.5 py-0.5 opacity-60" style={{ border: "1px dashed var(--line)" }}>{s}</span>
              {onAdopted && (
                <button type="button" onClick={() => adopt(s)} disabled={adopting !== null}
                  title="Rename it to pair with this file, so Plex shows it"
                  className="rounded px-1.5 py-0.5 text-xs font-semibold disabled:opacity-50" style={{ border: "1px solid var(--accent-line)", color: "var(--accent-text)" }}>
                  {adopting === s ? "…" : "Adopt"}
                </button>
              )}
            </span>
          ))}
        </div>
      )}
      {file.missing && <div className="mt-1.5 text-sm" style={{ color: "var(--reject-text)" }}>Tracked but not on disk. Refresh & rescan to look again, or clear the record (nothing on disk is touched) to search again.</div>}
      {showFile && (
        <FileDetailsModal
          path={file.path}
          title={file.filename || "File details"}
          subtitle={file.quality || undefined}
          // Convert works on every track: the default file is 'movie:ID', an extra
          // version 'movie:ID:v:VID'.
          onCleanTracks={async () => { await convertApi.convertRequest(versionId === 0 ? `movie:${movieId}` : `movie:${movieId}:v:${versionId}`); }}
          onClose={() => setShowFile(false)}
        />
      )}
    </>
  );
}

// ManualImportModal imports a file on disk into the movie. With extra versions it asks
// which one ("Import into"): opened from a version card it starts on that version; from
// the toolbar it starts on "whichever fits", which routes by the file name's resolution
// and edition the way an automatic import does.
function ManualImportModal({ movie, versionId, onClose, onImported }: { movie: Movie; versionId?: number; onClose: () => void; onImported: () => void }) {
  const [cands, setCands] = useState<ImportCandidate[] | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [importing, setImporting] = useState<string | null>(null);
  const [notice, setNotice] = useState<string | null>(null);
  const tracks = movie.versions ?? [];
  const [into, setInto] = useState<string>(versionId === undefined ? "auto" : String(versionId));
  const target = into === "auto" ? undefined : Number(into);

  useEffect(() => {
    api
      .manualImportList(movie.id)
      .then((r) => { setCands(r.candidates); setNotice(importListNotice(r)); })
      .catch((e: Error) => setError(e.message));
  }, [movie.id]);

  const doImport = async (path: string) => {
    setImporting(path);
    setError(null);
    try {
      await api.manualImport(movie.id, path, target);
      onImported();
    } catch (e) {
      setError((e as Error).message);
      setImporting(null);
    }
  };

  return (
    <Modal onClose={onClose} title="Manual import" size="lg" dismissible={importing === null}>
      <p className="mb-3 mt-0 text-sm text-ink-dim">Pick a file already on disk to import as <b>{movie.title}</b>. It'll be renamed to the library scheme.</p>
      {tracks.length > 1 && (
        <div className="mb-3 flex flex-wrap items-center gap-2 text-sm">
          <label htmlFor="import-into" className="font-mono text-2xs font-bold uppercase text-accent-text">Import into</label>
          <select id="import-into" value={into} onChange={(e) => setInto(e.target.value)} className="rounded-lg px-2.5 py-1.5 text-base" style={{ background: "var(--panel-2)", border: "1px solid var(--line)", color: "var(--ink)" }}>
            <option value="auto">Whichever version fits its name</option>
            {tracks.map((v) => <option key={v.is_default ? "d" : v.id} value={String(v.id)}>{v.label}{v.has_file ? " — replaces its file" : ""}</option>)}
          </select>
        </div>
      )}
      {error && <div className="mb-2 text-sm" style={{ color: "var(--reject-text)" }}>{error}</div>}
      {notice && <div className="mb-2 text-sm text-ink-faint">{notice}</div>}
      <div className="thin-scroll max-h-[52vh] overflow-y-auto">
        {cands === null ? (
          <div className="p-6 text-center text-base text-ink-dim">Scanning…</div>
        ) : cands.length === 0 ? (
          <div className="p-6 text-center text-base text-ink-dim">No importable video files found in the downloads folder.</div>
        ) : (
          cands.map((c) => (
            <button key={c.path} onClick={() => doImport(c.path)} disabled={importing !== null} className="flex w-full items-center gap-3 rounded-lg p-2.5 text-left transition-colors hover:bg-[var(--panel-2)]">
              <div className="min-w-0 flex-1">
                <div className="truncate text-base font-medium" title={c.filename}>{c.filename}</div>
                <div className="mt-0.5 flex items-center gap-3 font-mono text-xs text-ink-faint">
                  {c.quality && <span>{c.quality}</span>}
                  <span>{fmtSize(c.size_bytes)}</span>
                </div>
              </div>
              <span className="flex-none rounded-lg px-3 py-1.5 text-sm font-semibold" style={{ background: "var(--accent-soft)", color: "var(--accent-text)" }}>{importing === c.path ? "Importing…" : "Import"}</span>
            </button>
          ))
        )}
      </div>
    </Modal>
  );
}


function BlocklistPanel({ movieId, refreshKey }: { movieId: number; refreshKey: unknown }) {
  const [entries, setEntries] = useState<BlockEntry[] | null>(null);

  const load = () => api.blocklist(movieId).then(setEntries).catch(() => setEntries([]));
  useEffect(() => {
    load();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [movieId, refreshKey]);

  const unblock = async (bid: number) => {
    await api.unblock(movieId, bid);
    load();
  };

  if (!entries || entries.length === 0) return null;
  return (
    <div className="mt-8">
      <h2 className="m-0 mb-3 text-md font-bold">Blocklist <span className="font-normal text-ink-faint">· {entries.length}</span></h2>
      <div className="flex flex-col gap-2">
        {entries.map((e) => (
          <div key={e.id} className="flex items-center gap-3 rounded-lg p-2.5 text-sm" style={{ border: "1px solid var(--line)", background: "var(--panel)" }}>
            <div className="min-w-0 flex-1">
              <div className="truncate font-mono text-sm" title={e.title}>{e.title}</div>
              <div className="mt-0.5 text-xs text-ink-faint">{e.reason}{e.indexer ? ` · ${e.indexer}` : ""}</div>
            </div>
            <button onClick={() => unblock(e.id)} className="flex-none rounded-lg px-2.5 py-1 text-xs font-semibold" style={{ border: "1px solid var(--line)", color: "var(--ink-dim)" }}>Remove</button>
          </div>
        ))}
      </div>
    </div>
  );
}

const EVENT_TONES: Record<string, string> = {
  added: "var(--ink-faint)",
  grabbed: "var(--accent-text)",
  imported: "var(--good-text)",
  upgraded: "var(--good-text)",
  detected: "var(--good-text)",
  deleted: "var(--reject-text)",
  missing: "var(--avoid-text)",
  missing_cleared: "var(--ink-dim)",
  renamed: "var(--ink-dim)",
  refreshed: "var(--ink-faint)",
  searched: "var(--ink-dim)",
};

function HistoryPanel({ movieId, refreshKey }: { movieId: number; refreshKey: unknown }) {
  const [events, setEvents] = useState<MovieEvent[] | null>(null);

  useEffect(() => {
    api.movieHistory(movieId).then(setEvents).catch(() => setEvents([]));
  }, [movieId, refreshKey]);

  return (
    <div className="mt-8">
      <h2 className="m-0 mb-3 text-md font-bold">History</h2>
      {events === null ? (
        <div className="text-base text-ink-dim">Loading…</div>
      ) : events.length === 0 ? (
        <div className="rounded-xl p-6 text-center text-base text-ink-dim" style={{ border: "1px solid var(--line)" }}>No activity yet.</div>
      ) : (
        <div className="flex flex-col">
          {events.map((e, i) => (
            <div key={i} className="flex items-center gap-3 border-b py-2 text-sm" style={{ borderColor: "var(--line)" }}>
              <span className="w-[74px] flex-none font-mono text-2xs font-bold uppercase" style={{ color: EVENT_TONES[e.event] ?? "var(--ink-dim)" }}>{e.event}</span>
              <span className="min-w-0 flex-1 truncate text-ink-dim" title={e.detail}>{e.detail || "—"}</span>
              <span className="flex-none font-mono text-xs text-ink-faint">{fmtTime(e.created_at)}</span>
            </div>
          ))}
        </div>
      )}
    </div>
  );
}

function fmtTime(s: string): string {
  const d = new Date(s.includes("T") ? s : s.replace(" ", "T") + "Z");
  if (isNaN(d.getTime())) return s;
  return d.toLocaleString(undefined, { month: "short", day: "numeric", hour: "2-digit", minute: "2-digit" });
}
