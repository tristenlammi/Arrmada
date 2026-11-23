import { useCallback, useEffect, useState } from "react";
import { Link, useLocation } from "react-router-dom";
import { LINKS } from "../lib/links";
import { api, type LibraryPaths, type BrowseResult, type FolderCheck, type UnmatchedFolder, type MatchCandidate } from "../lib/api";
import { useMe, isAdmin } from "../lib/me";
import { foldersApi } from "../lib/foldersApi";
import { fmtBytes } from "../lib/disposal";
import { announceFoldersSaved } from "../lib/restart";
import { useAutoSave } from "../lib/useAutoSave";
import { SavedTick } from "../components/settings/auto";
import { Modal, useToast } from "../ui";
import { ErrorBoundary } from "../components/ErrorBoundary";
import { useLiveEvent, useOnReconnect } from "../lib/live";
import { musicReviewsOf, scanLine, unmatchedOf, useLibraryScan } from "../lib/useLibraryScan";

// LibraryFolders — points each library at a folder (with an in-app picker) and scans it.
// Lives inside Settings → Library. Mount your media into the container (see the
// installer), then browse + select here.
type PathKey = keyof LibraryPaths;
// job: the kind of job a row's scan runs as, so a scan started anywhere shows on its row.
// Ebooks and Audiobooks share the one book scan. review: its unmatched folders are listed
// under the rows ("needs review"). target: Movies and TV follow whole-library scans only;
// a scan of one extra folder shows on that folder's row (More Movies and TV folders).
const ROWS: { key: PathKey; label: string; hint: string; scan?: () => Promise<{ job_id?: number }>; job?: string; target?: string; review?: boolean }[] = [
  { key: "movies", label: "Movies", hint: "folder of movie subfolders", scan: () => api.scanLibrary(), job: "movie.scan", target: "all", review: true },
  { key: "tv", label: "TV Shows", hint: "folder of show subfolders", scan: () => api.scanSeries(), job: "series.scan", target: "all", review: true },
  { key: "ebooks", label: "Ebooks", hint: "folder of book subfolders", scan: () => api.scanBooks(), job: "books.scan" },
  { key: "audiobooks", label: "Audiobooks", hint: "may share the ebooks folder", scan: () => api.scanBooks(), job: "books.scan" },
  { key: "music", label: "Music", hint: "folder of artist subfolders", scan: () => api.scanMusic(), job: "music.scan" },
  { key: "downloads", label: "Downloads", hint: "where the download client saves files — the disk guard watches this folder" },
];
const noScan = () => Promise.resolve({});

const NEEDS_REVIEW_CHANGED = "arrmada:needs-review-changed";
/** A scan elsewhere on the page finished: the needs-review lists reload. */
export function needsReviewChanged() {
  window.dispatchEvent(new Event(NEEDS_REVIEW_CHANGED));
}

export function LibraryFolders() {
  const [paths, setPaths] = useState<LibraryPaths | null>(null);
  const [reviewKey, setReviewKey] = useState(0); // bump to reload the unmatched lists
  const toast = useToast();
  const { user, musicEnabled } = useMe();
  // Moving a library or browsing the host's folders is the admin's call; a manager sees
  // where each library lives and can still scan it.
  const admin = isAdmin(user);
  // A finished scan (from here, the Movies page or another tab) reloads the review lists:
  // its folders land there. A reconnect reloads too, in case the event was missed.
  const reloadReview = () => setReviewKey((k) => k + 1);
  useLiveEvent("library.scanned", reloadReview);
  useOnReconnect(reloadReview);
  // A scan of one extra folder (More Movies and TV folders, below) says so directly, so the
  // list follows it even without a live socket.
  useEffect(() => {
    const reload = () => setReviewKey((k) => k + 1);
    window.addEventListener(NEEDS_REVIEW_CHANGED, reload);
    return () => window.removeEventListener(NEEDS_REVIEW_CHANGED, reload);
  }, []);

  useEffect(() => { api.libraryPaths().then(setPaths).catch(() => toast("Could not load library paths", { tone: "error" })); }, [toast]);
  // A scan summary's "need review →" lands here; the list renders only once the folders
  // have loaded, after Settings' own scroll-to-anchor has run.
  const { hash } = useLocation();
  useEffect(() => {
    if (paths && hash === "#needs-review") document.getElementById("needs-review")?.scrollIntoView({ block: "start" });
  }, [paths, hash]);
  if (!paths) return <div className="text-base text-ink-dim">Loading…</div>;

  // A row's save answers with every folder; take them all so each row compares against
  // what's really stored.
  const saved = (p: LibraryPaths) => { setPaths(p); announceFoldersSaved(); };

  return (
    <div>
      <p className="mb-4 max-w-[64ch] text-base text-ink-dim">Point each library at a folder inside your mounted media. Use <b>Browse</b> to pick from the folders Arrmada can see — a picked folder is saved straight away — then <b>Scan</b> to catalog what's there. Ebooks and audiobooks can share one folder.</p>

      <div className="flex flex-col gap-2.5">
        {ROWS.map((row) => (
          <FolderRow
            key={row.key}
            row={row}
            paths={paths}
            admin={admin}
            // The folder can be set ahead of switching Music on, but a scan while it's off
            // only hits the gated endpoint and flashes "module is turned off".
            canScan={!!row.scan && (row.key !== "music" || musicEnabled)}
            onScanned={reloadReview}
            onSaved={saved}
          />
        ))}
      </div>

      {!admin && <p className="mt-3 text-sm text-ink-faint">Only an admin can change these folders.</p>}

      {/* A needs-review list that can't be shown never takes the folders above down with it. */}
      <div id="needs-review">
        <ErrorBoundary resetKey={reviewKey} fallback={<ReviewBroken label="Movies" />}>
          <UnmatchedReview media="movie" reloadKey={reviewKey} />
        </ErrorBoundary>
        <ErrorBoundary resetKey={reviewKey} fallback={<ReviewBroken label="TV Shows" />}>
          <UnmatchedReview media="series" reloadKey={reviewKey} />
        </ErrorBoundary>
      </div>

    </div>
  );
}

// FolderRow is one library's folder. It saves on its own, and only on purpose: a folder
// picked in Browse, or a typed path confirmed with "Use this folder" or Enter — never on
// every keystroke or on blur, because a saved folder re-points imports at once. The
// server's checks (exists, writable, never under /data) answer inline on this row.
function FolderRow({ row, paths, admin, canScan, onScanned, onSaved }: {
  row: typeof ROWS[number];
  paths: LibraryPaths;
  admin: boolean;
  canScan: boolean;
  /** A scan of this library finished (the review lists may have changed). */
  onScanned: () => void;
  onSaved: (p: LibraryPaths) => void;
}) {
  const scan = useLibraryScan(row.job ?? "", row.scan ?? noScan, onScanned, row.target);
  const line = scanLine(row.label, scan);
  const unmatched = row.review ? unmatchedOf(scan.job) : 0;
  // Music's unplaced folders wait in Reviews (MUS-37), not in the list below.
  const toMatch = row.key === "music" ? musicReviewsOf(scan.job) : 0;
  const [picking, setPicking] = useState(false);
  const [blocking, setBlocking] = useState(false);
  const onBlocking = useCallback((_: PathKey, b: boolean) => setBlocking(b), []);
  const folder = useAutoSave<string>(paths[row.key], async (v) => {
    // Only this row is sent, so another folder that's odd right now (a share that isn't
    // mounted) can never block saving this one.
    onSaved(await api.setLibraryPaths({ [row.key]: v.trim() }));
  }, { equal: (a, b) => a.trim() === b.trim() });
  const use = () => void folder.commit(folder.value.trim());

  return (
    <div className="rounded-xl p-3.5" style={{ border: "1px solid var(--line)", background: "var(--panel-2)" }}>
      <div className="flex items-center justify-between gap-3">
        <div>
          <div className="text-md font-semibold">{row.label}</div>
          <div className="text-xs text-ink-faint">{row.hint}</div>
        </div>
        <div className="flex items-center gap-2">
          {canScan && scan.running && scan.job && <button onClick={() => void scan.stop()} aria-label={`Stop the ${row.label} scan`} className="rounded-lg px-3 py-1.5 text-sm font-semibold" style={{ border: "1px solid var(--line)", background: "var(--panel)", color: "var(--ink)" }}>Stop</button>}
          {canScan && <button onClick={() => void scan.start()} disabled={scan.running} className="rounded-lg px-3 py-1.5 text-sm font-semibold disabled:opacity-60" style={{ border: "1px solid var(--accent-line)", color: "var(--accent-text)" }}>{scan.running ? "Scanning…" : "Scan"}</button>}
        </div>
      </div>
      {(line || scan.note) && (
        <div role="status" className="mt-1.5 flex flex-wrap items-center gap-x-2 gap-y-0.5 text-sm">
          {line && <span style={{ color: line.tone === "good" ? "var(--good-text)" : line.tone === "error" ? "var(--reject-text)" : "var(--ink-dim)" }}>{line.text}</span>}
          {unmatched > 0 && <a href="#needs-review" className="font-semibold" style={{ color: "var(--accent-text)" }}>{unmatched} need review ↓</a>}
          {toMatch > 0 && <Link to={LINKS.review} className="font-semibold" style={{ color: "var(--accent-text)" }}>{toMatch.toLocaleString()} to match in Reviews →</Link>}
          {scan.note && !scan.note.error && <span className="text-ink-faint">{scan.note.text}</span>}
        </div>
      )}
      <div className="mt-2 flex flex-wrap gap-2">
        <input
          aria-label={`${row.label} folder`}
          value={folder.value}
          onChange={(e) => folder.set(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === "Enter" && folder.dirty && !blocking) { e.preventDefault(); use(); }
            if (e.key === "Escape" && folder.dirty) { e.preventDefault(); folder.revert(); }
          }}
          readOnly={!admin}
          placeholder={admin ? "/storage/media/…" : "Not set"}
          className="min-w-0 flex-1 rounded-lg px-2.5 py-1.5 font-mono text-sm"
          style={{ background: "var(--panel)", border: `1px solid ${folder.status === "error" ? "var(--reject)" : "var(--line)"}`, color: admin ? "var(--ink)" : "var(--ink-dim)" }}
        />
        {admin && folder.dirty && folder.status !== "saving" && (
          <button onClick={use} disabled={blocking} title={blocking ? "Fix the folder marked in red first" : undefined} className="rounded-lg px-3 py-1.5 text-sm font-semibold disabled:opacity-50" style={{ background: "linear-gradient(150deg, var(--accent), var(--accent-deep))", color: "var(--accent-ink)" }}>Use this folder</button>
        )}
        {admin && <button onClick={() => setPicking(true)} className="rounded-lg px-3 py-1.5 text-sm font-semibold" style={{ border: "1px solid var(--line)", background: "var(--panel)", color: "var(--ink)" }}>Browse…</button>}
      </div>
      <div className="mt-1 flex flex-wrap items-center gap-2">
        <SavedTick save={folder} />
        {folder.status === "saved" && <span className="text-xs text-ink-faint">New imports go here from now on; what's already imported stays where it is.</span>}
      </div>
      <FolderChips kind={row.key} path={folder.value} current={paths[row.key]} downloads={row.key === "downloads" ? undefined : paths.downloads} onBlocking={onBlocking} onCreated={(p) => { onSaved(p); folder.revert(); }} />
      {picking && (
        <FolderPicker
          initial={folder.value || undefined}
          onClose={() => setPicking(false)}
          onSelect={(p) => { setPicking(false); void folder.commit(p); }}
        />
      )}
    </div>
  );
}

// ReviewBroken stands in for a needs-review list that failed to show.
function ReviewBroken({ label }: { label: string }) {
  return (
    <div role="alert" className="mt-4 rounded-xl p-3.5 text-sm text-ink-dim" style={{ border: "1px solid var(--line)", background: "var(--panel)" }}>
      {label} — needs review couldn't be shown. Scan again or reload the page; the folders above still work.
    </div>
  );
}

// UnmatchedReview lists folders the last scan couldn't confidently identify and
// lets the admin pick the right title (from candidates or a manual search), so a
// mis-titled folder never gets silently mis-filed.
export function UnmatchedReview({ media, reloadKey }: { media: "movie" | "series"; reloadKey: number }) {
  const toast = useToast();
  const label = media === "movie" ? "Movies" : "TV Shows";
  const [items, setItems] = useState<UnmatchedFolder[] | null>(null);
  const [busy, setBusy] = useState(false);

  const load = () =>
    (media === "movie" ? api.moviesUnmatched() : api.seriesUnmatched()).then(setItems).catch(() => setItems([]));
  useEffect(() => { load(); }, [reloadKey]); // eslint-disable-line react-hooks/exhaustive-deps

  const pick = async (folder: string, tmdb_id: number, root?: string) => {
    setBusy(true);
    try {
      await foldersApi.importFolder(media, folder, tmdb_id, root);
      setItems((xs) => (xs ?? []).filter((u) => u.folder !== folder || u.root !== root));
      toast(`Imported “${folder}”.`, { tone: "good" });
    } catch (e) { toast((e as Error).message, { tone: "error", ms: 6000 }); } finally { setBusy(false); }
  };

  if (items === null || items.length === 0) return null;
  return (
    <div className="mt-4 rounded-xl p-3.5" style={{ border: "1px solid var(--line)", background: "var(--panel)" }}>
      <div className="flex items-center justify-between gap-3">
        <div>
          <div className="text-md font-semibold">
            {label} — needs review
            <span className="ml-1.5 rounded-full px-1.5 py-0.5 text-xs font-bold" style={{ background: "var(--reject-soft)", color: "var(--reject-text)" }}>{items.length}</span>
          </div>
          <div className="text-xs text-ink-faint">Arrmada couldn't confidently identify these — pick the right title so nothing is mis-filed.</div>
        </div>
        <button onClick={load} className="rounded-lg px-3 py-1.5 text-sm font-semibold" style={{ border: "1px solid var(--line)", background: "var(--panel-2)", color: "var(--ink)" }}>Refresh</button>
      </div>
      <div className="mt-3 flex flex-col gap-2.5">
        {items.map((u) => <UnmatchedRow key={`${u.root ?? ""}/${u.folder}`} media={media} item={u} busy={busy} onPick={pick} />)}
      </div>
    </div>
  );
}

function UnmatchedRow({ media, item, busy, onPick }: { media: "movie" | "series"; item: UnmatchedFolder; busy: boolean; onPick: (folder: string, tmdb: number, root?: string) => void }) {
  const [q, setQ] = useState(item.title);
  const [results, setResults] = useState<MatchCandidate[] | null>(null);
  const [searching, setSearching] = useState(false);

  const search = async () => {
    if (!q.trim()) return;
    setSearching(true);
    try {
      const r = media === "movie" ? await api.lookupMovies(q) : await api.lookupSeries(q);
      setResults(r.map((x) => ({ tmdb_id: x.tmdb_id, title: x.title, year: x.year, poster_url: x.poster_url })));
    } catch { setResults([]); } finally { setSearching(false); }
  };
  // An older server sent null for a folder with no candidates (#136): treat it as none.
  const shown = results ?? item.candidates ?? [];

  return (
    <div className="rounded-lg p-2.5" style={{ border: "1px solid var(--line)", background: "var(--panel-2)" }}>
      <div className="flex items-center gap-2 text-sm">
        <span style={{ color: "var(--accent-text)" }}>📁</span>
        <span className="font-mono font-semibold">{item.folder}</span>
        <span className="text-ink-faint">— looked for “{item.title}”{item.year ? ` (${item.year})` : ""}</span>
      </div>
      <div className="mt-2 flex flex-wrap gap-1.5">
        {shown.length === 0 && <span className="text-xs text-ink-faint">No candidates — search a different title below.</span>}
        {shown.map((c) => (
          <button
            key={c.tmdb_id}
            disabled={busy}
            onClick={() => onPick(item.folder, c.tmdb_id, item.root)}
            title={c.overview}
            className="flex items-center gap-2 rounded-lg px-2 py-1 text-left text-sm disabled:opacity-50"
            style={{ border: "1px solid var(--line)", background: "var(--panel)" }}
          >
            {c.poster_url
              ? <img src={c.poster_url} alt="" className="h-9 w-6 flex-none rounded object-cover" />
              : <span className="grid h-9 w-6 flex-none place-items-center rounded text-xs text-ink-faint" style={{ background: "var(--panel-2)" }}>?</span>}
            <span><span className="font-semibold">{c.title}</span>{c.year ? <span className="text-ink-faint"> ({c.year})</span> : ""}</span>
          </button>
        ))}
      </div>
      <div className="mt-2 flex gap-1.5">
        <input
          value={q}
          onChange={(e) => setQ(e.target.value)}
          onKeyDown={(e) => e.key === "Enter" && search()}
          placeholder="Search a different title…"
          aria-label="Search a different title"
          className="flex-1 rounded-lg px-2 py-1 text-sm"
          style={{ background: "var(--panel)", border: "1px solid var(--line)", color: "var(--ink)" }}
        />
        <button onClick={search} disabled={searching} className="rounded-lg px-2.5 py-1 text-xs font-semibold" style={{ border: "1px solid var(--line)", background: "var(--panel)", color: "var(--ink)" }}>{searching ? "…" : "Search"}</button>
      </div>
    </div>
  );
}

// FolderChips checks a folder shortly after it's typed or picked and shows what Arrmada
// sees there: is it there, can the app write to it, will finished downloads hardlink in
// (or be copied), how much room, how many folders. A problem that would make the save
// refuse it is shown in red and reported through onBlocking. The data-folder rule applies
// to every folder; "missing" only blocks a folder that's being changed, the same as the
// server. A missing folder can be created on the spot, which saves that one folder.
export function FolderChips({ kind, path, current, downloads, onBlocking, onCreated }: {
  kind: PathKey;
  path: string;
  current: string;
  downloads?: string;
  onBlocking?: (kind: PathKey, blocking: boolean) => void;
  onCreated?: (saved: LibraryPaths) => void;
}) {
  const [check, setCheck] = useState<FolderCheck | null>(null);
  const [creating, setCreating] = useState(false);
  const [createErr, setCreateErr] = useState<string | null>(null);
  const [failed, setFailed] = useState<string | null>(null); // the path whose check errored
  const p = path.trim();
  const linkFrom = kind === "downloads" ? undefined : downloads?.trim() || undefined;

  useEffect(() => {
    if (!p) return;
    let live = true;
    const t = window.setTimeout(() => {
      api.checkLibraryFolder(p, kind, linkFrom)
        .then((c) => { if (live) setCheck(c); })
        .catch(() => { if (live) setFailed(p); }); // the chips are advice; the save still checks
    }, 500);
    return () => { live = false; window.clearTimeout(t); };
  }, [p, kind, linkFrom]);

  // A result for an older value is stale; show nothing until the new one lands.
  const c = check && check.path === p ? check : null;
  const changed = p !== current.trim();
  const blocking = !!c?.error && (c.under_data_dir || changed);
  useEffect(() => { onBlocking?.(kind, p !== "" && blocking); }, [kind, p, blocking, onBlocking]);

  // After the save, look again straight away instead of trusting it: a press that left
  // the folder missing must say so here (it once answered OK and made nothing).
  const create = async () => {
    setCreating(true); setCreateErr(null);
    try {
      const saved = await api.setLibraryPaths({ [kind]: p, create: true });
      onCreated?.(saved);
      const after = await api.checkLibraryFolder(p, kind, linkFrom);
      setCheck(after);
      if (!after.exists) setCreateErr(`${p} still isn't there. Check the container's volume mapping and Arrmada's log.`);
    } catch (e) { setCreateErr((e as Error).message); } finally { setCreating(false); }
  };

  if (!p) return null;
  if (!c) return failed === p ? null : <div className="mt-1.5 text-xs text-ink-faint">Checking…</div>;
  if (c.error) {
    return (
      <div className="mt-1.5 flex flex-wrap items-center gap-2 text-xs" style={{ color: blocking ? "var(--reject-text)" : "var(--avoid-text)" }}>
        <span>{c.error}</span>
        {!c.exists && !c.under_data_dir && (
          <button onClick={create} disabled={creating} className="rounded-lg px-2.5 py-1 text-xs font-semibold disabled:opacity-50" style={{ border: "1px solid var(--accent-line)", color: "var(--accent-text)" }}>{creating ? "Creating…" : "Create it"}</button>
        )}
        {createErr && <span style={{ color: "var(--reject-text)" }}>{createErr}</span>}
      </div>
    );
  }
  const good = "var(--good-text)", warn = "var(--avoid-text)";
  return (
    <div className="mt-1.5 flex flex-wrap gap-1.5 text-xs">
      <FolderChip color={good}>✓ Exists</FolderChip>
      {c.writable
        ? <FolderChip color={good}>✓ Writable</FolderChip>
        : <FolderChip color={warn} title="Arrmada can read this folder but not write to it: imports can't land here. Check PUID/PGID and the share's permissions.">⚠ Read-only</FolderChip>}
      {c.hardlink_with_downloads === true && <FolderChip color={good}>✓ Hardlinks with Downloads</FolderChip>}
      {c.hardlink_with_downloads === false && (
        <FolderChip color={warn} title="Downloads and this folder are on different drives or shares, so each finished download is copied (using the space twice) instead of hardlinked.">⚠ Will copy, not hardlink</FolderChip>
      )}
      {c.total_bytes > 0 && <FolderChip>Free {fmtBytes(c.free_bytes)}</FolderChip>}
      <FolderChip>{c.entries.toLocaleString()}{c.entries_capped ? "+" : ""} folder{c.entries === 1 ? "" : "s"}</FolderChip>
    </div>
  );
}

function FolderChip({ children, color, title }: { children: React.ReactNode; color?: string; title?: string }) {
  return <span title={title} className="rounded px-1.5 py-0.5" style={{ background: "var(--panel)", border: "1px solid var(--line-soft)", color: color ?? "var(--ink-faint)" }}>{children}</span>;
}

export function FolderPicker({ initial, onClose, onSelect }: { initial?: string; onClose: () => void; onSelect: (path: string) => void }) {
  const [data, setData] = useState<BrowseResult | null>(null);
  const [err, setErr] = useState<string | null>(null);
  const go = (path?: string) => { setErr(null); api.browseFolders(path).then(setData).catch((e) => setErr((e as Error).message)); };
  // A saved folder that no longer exists can't be listed; open on the media mount instead
  // of an error with nowhere to go.
  useEffect(() => {
    api.browseFolders(initial).then(setData).catch(() => go());
  }, []); // eslint-disable-line react-hooks/exhaustive-deps

  return (
    <Modal onClose={onClose} labelledBy="folder-picker-title" size="md">
      <div className="mb-3 flex items-center justify-between gap-3">
        <h2 id="folder-picker-title" className="m-0 text-lg font-bold">Select a folder</h2>
        <button onClick={onClose} aria-label="Close" className="text-ink-faint hover:text-[var(--ink)]">✕</button>
      </div>

      <div className="mb-2 flex items-center gap-2">
        <button onClick={() => data && go(data.parent)} disabled={!data || data.path === "/"} className="rounded-lg px-2.5 py-1.5 text-sm font-semibold disabled:opacity-40" style={{ border: "1px solid var(--line)", background: "var(--panel-2)", color: "var(--ink)" }} title="Up one level">↑</button>
        <div className="min-w-0 flex-1 truncate rounded-lg px-2.5 py-1.5 font-mono text-sm" style={{ background: "var(--panel-2)", border: "1px solid var(--line)", color: "var(--ink-dim)" }}>{data?.path ?? "…"}</div>
      </div>

      {err && <div className="mb-2 rounded-lg p-2.5 text-sm" style={{ border: "1px solid var(--reject)", color: "var(--reject-text)" }}>{err}</div>}

      <div className="thin-scroll max-h-[46vh] overflow-y-auto rounded-lg" style={{ border: "1px solid var(--line)" }}>
        {!data ? (
          <div className="p-6 text-center text-sm text-ink-faint">Loading…</div>
        ) : data.dirs.length === 0 ? (
          <div className="p-6 text-center text-sm text-ink-faint">No sub-folders here.</div>
        ) : data.dirs.map((d) => (
          <button key={d.path} onClick={() => go(d.path)} className="flex w-full items-center gap-2 px-3 py-2 text-left text-base hover:bg-[var(--panel-2)]" style={{ borderTop: "1px solid var(--line-soft)" }}>
            <span style={{ color: "var(--accent-text)" }}>📁</span>
            <span className="truncate">{d.name}</span>
          </button>
        ))}
      </div>

      <div className="mt-3 flex items-center justify-between gap-2">
        <span className="text-xs" style={{ color: data?.path_disabled ? "var(--avoid-text)" : "var(--ink-faint)" }}>
          {data?.path_disabled ? "This folder holds Arrmada's own data — go into your media folder and pick one there." : "Navigate into the folder you want, then select it."}
        </span>
        <button onClick={() => data && !data.path_disabled && onSelect(data.path)} disabled={!data || data.path_disabled}className="rounded-lg px-4 py-2 text-base font-semibold disabled:opacity-50" style={{ background: "linear-gradient(150deg, var(--accent), var(--accent-deep))", color: "var(--accent-ink)" }}>Select this folder</button>
      </div>
    </Modal>
  );
}
