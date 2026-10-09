import { useCallback, useEffect, useState } from "react";
import { api, type LibraryPaths, type BrowseResult, type FolderCheck, type UnmatchedFolder, type MatchCandidate } from "../lib/api";
import { useMe, isAdmin } from "../lib/me";
import { fmtBytes } from "../lib/disposal";
import { announceFoldersSaved } from "../lib/restart";

// LibraryFolders — points each library at a folder (with an in-app picker) and scans it.
// Lives inside Settings → Library. Mount your media into the container (see the
// installer), then browse + select here.
type PathKey = keyof LibraryPaths;
const ROWS: { key: PathKey; label: string; hint: string; scan?: () => Promise<unknown> }[] = [
  { key: "movies", label: "Movies", hint: "folder of movie subfolders", scan: () => api.scanLibrary() },
  { key: "tv", label: "TV Shows", hint: "folder of show subfolders", scan: () => api.scanSeries() },
  { key: "ebooks", label: "Ebooks", hint: "folder of book subfolders", scan: () => api.scanBooks() },
  { key: "audiobooks", label: "Audiobooks", hint: "may share the ebooks folder", scan: () => api.scanBooks() },
  { key: "music", label: "Music", hint: "folder of artist subfolders", scan: () => api.scanMusic() },
  { key: "downloads", label: "Downloads", hint: "where the download client saves files — the disk guard watches this folder" },
];

export function LibraryFolders() {
  const [paths, setPaths] = useState<LibraryPaths | null>(null);
  const [draft, setDraft] = useState<LibraryPaths | null>(null);
  const [picking, setPicking] = useState<PathKey | null>(null);
  const [busy, setBusy] = useState(false);
  const [reviewKey, setReviewKey] = useState(0); // bump to reload the unmatched lists
  const [toast, setToast] = useState<string | null>(null);
  const [blocking, setBlocking] = useState<Partial<Record<PathKey, boolean>>>({});
  const flash = (m: string) => { setToast(m); window.setTimeout(() => setToast(null), 3500); };
  const { user, musicEnabled } = useMe();
  // Moving a library or browsing the host's folders is the admin's call; a manager sees
  // where each library lives and can still scan it.
  const admin = isAdmin(user);
  const onBlocking = useCallback((k: PathKey, b: boolean) => setBlocking((cur) => (cur[k] === b ? cur : { ...cur, [k]: b })), []);

  useEffect(() => { api.libraryPaths().then((p) => { setPaths(p); setDraft(p); }).catch(() => flash("Could not load library paths")); }, []);
  if (!draft) return <div className="text-[12.5px] text-ink-dim">Loading…</div>;

  const changedKeys = paths ? (Object.keys(draft) as PathKey[]).filter((k) => draft[k] !== paths[k]) : [];
  const dirty = changedKeys.length > 0;
  const blocked = changedKeys.some((k) => blocking[k]);
  const save = async () => {
    setBusy(true);
    // Only what changed is sent, so an untouched folder that's odd right now (a share
    // that isn't mounted) can never block saving the others.
    const changes = Object.fromEntries(changedKeys.map((k) => [k, draft[k]])) as Partial<LibraryPaths>;
    try {
      const p = await api.setLibraryPaths(changes);
      setPaths(p); setDraft(p);
      announceFoldersSaved();
      // Folders apply live: say what that means for what's already there.
      flash("Saved. Changing a folder applies to new imports right away. What's already imported stays where it is.");
    }
    catch (e) { flash((e as Error).message); } finally { setBusy(false); }
  };
  // "Create it" saved that one folder; keep any other edits in progress.
  const created = (k: PathKey) => (saved: LibraryPaths) => {
    setPaths(saved);
    setDraft((d) => (d ? { ...d, [k]: saved[k] } : saved));
    announceFoldersSaved();
  };
  const scan = async (row: typeof ROWS[number]) => {
    if (!row.scan) return;
    if (dirty) { flash("Save your folders first, then scan."); return; }
    try {
      await row.scan();
      flash(`Scanning ${row.label}… matches appear in the library; anything unsure lands in "needs review" below.`);
      window.setTimeout(() => setReviewKey((k) => k + 1), 5000); // reload review once the scan has had a moment
    } catch (e) { flash((e as Error).message); }
  };

  return (
    <div>
      <p className="mb-4 max-w-[64ch] text-[12.5px] text-ink-dim">Point each library at a folder inside your mounted media. Use <b>Browse</b> to pick from the folders Arrmada can see, then <b>Scan</b> to catalog what's there. Ebooks and audiobooks can share one folder.</p>

      <div className="flex flex-col gap-2.5">
        {ROWS.map((row) => {
          // The folder can be set ahead of switching Music on, but a scan while it's off
          // only hits the gated endpoint and flashes "module is turned off".
          const canScan = row.scan && (row.key !== "music" || musicEnabled);
          return (
          <div key={row.key} className="rounded-xl p-3.5" style={{ border: "1px solid var(--line)", background: "var(--panel-2)" }}>
            <div className="flex items-center justify-between gap-3">
              <div>
                <div className="text-[13px] font-semibold">{row.label}</div>
                <div className="text-[10.5px] text-ink-faint">{row.hint}</div>
              </div>
              {canScan && <button onClick={() => scan(row)} className="rounded-lg px-3 py-1.5 text-[11.5px] font-semibold" style={{ border: "1px solid var(--accent-line)", color: "var(--accent)" }}>Scan</button>}
            </div>
            <div className="mt-2 flex gap-2">
              <input
                value={draft[row.key]}
                onChange={(e) => setDraft({ ...draft, [row.key]: e.target.value })}
                readOnly={!admin}
                placeholder={admin ? "/storage/media/…" : "Not set"}
                className="flex-1 rounded-lg px-2.5 py-1.5 font-mono text-[11.5px]"
                style={{ background: "var(--panel)", border: "1px solid var(--line)", color: admin ? "var(--ink)" : "var(--ink-dim)" }}
              />
              {admin && <button onClick={() => setPicking(row.key)} className="rounded-lg px-3 py-1.5 text-[11.5px] font-semibold" style={{ border: "1px solid var(--line)", background: "var(--panel)", color: "var(--ink)" }}>Browse…</button>}
            </div>
            <FolderChips kind={row.key} path={draft[row.key]} current={paths?.[row.key] ?? ""} downloads={draft.downloads} onBlocking={onBlocking} onCreated={created(row.key)} />
          </div>
          );
        })}
      </div>

      {admin ? (
        <div className="mt-4 flex items-center justify-end gap-3">
          {dirty && <span className="text-[11.5px] text-ink-faint">{blocked ? "Fix the folders marked in red to save" : "Unsaved changes"}</span>}
          <button onClick={save} disabled={!dirty || busy || blocked} className="rounded-lg px-4 py-2 text-[13px] font-semibold disabled:opacity-50" style={{ background: "linear-gradient(150deg, var(--accent), var(--accent-deep))", color: "var(--accent-ink)" }}>{busy ? "Saving…" : "Save folders"}</button>
        </div>
      ) : (
        <p className="mt-3 text-[11.5px] text-ink-faint">Only an admin can change these folders.</p>
      )}

      <UnmatchedReview media="movie" reloadKey={reviewKey} flash={flash} />
      <UnmatchedReview media="series" reloadKey={reviewKey} flash={flash} />

      {picking && (
        <FolderPicker
          initial={draft[picking] || undefined}
          onClose={() => setPicking(null)}
          onSelect={(p) => { setDraft({ ...draft, [picking]: p }); setPicking(null); }}
        />
      )}
      {toast && <div className="fixed bottom-5 left-1/2 -translate-x-1/2 rounded-lg px-4 py-2.5 text-[12.5px] font-medium" style={{ background: "var(--panel-2)", border: "1px solid var(--line)", boxShadow: "var(--shadow)", color: "var(--ink)" }}>{toast}</div>}
    </div>
  );
}

// UnmatchedReview lists folders the last scan couldn't confidently identify and
// lets the admin pick the right title (from candidates or a manual search), so a
// mis-titled folder never gets silently mis-filed.
function UnmatchedReview({ media, reloadKey, flash }: { media: "movie" | "series"; reloadKey: number; flash: (m: string) => void }) {
  const label = media === "movie" ? "Movies" : "TV Shows";
  const [items, setItems] = useState<UnmatchedFolder[] | null>(null);
  const [busy, setBusy] = useState(false);

  const load = () =>
    (media === "movie" ? api.moviesUnmatched() : api.seriesUnmatched()).then(setItems).catch(() => setItems([]));
  useEffect(() => { load(); }, [reloadKey]); // eslint-disable-line react-hooks/exhaustive-deps

  const pick = async (folder: string, tmdb_id: number) => {
    setBusy(true);
    try {
      await (media === "movie" ? api.importMovieFolder(folder, tmdb_id) : api.importSeriesFolder(folder, tmdb_id));
      setItems((xs) => (xs ?? []).filter((u) => u.folder !== folder));
      flash(`Imported “${folder}”.`);
    } catch (e) { flash((e as Error).message); } finally { setBusy(false); }
  };

  if (items === null || items.length === 0) return null;
  return (
    <div className="mt-4 rounded-xl p-3.5" style={{ border: "1px solid var(--line)", background: "var(--panel)" }}>
      <div className="flex items-center justify-between gap-3">
        <div>
          <div className="text-[13px] font-semibold">
            {label} — needs review
            <span className="ml-1.5 rounded-full px-1.5 py-0.5 text-[10px] font-bold" style={{ background: "var(--reject-soft)", color: "var(--reject)" }}>{items.length}</span>
          </div>
          <div className="text-[10.5px] text-ink-faint">Arrmada couldn't confidently identify these — pick the right title so nothing is mis-filed.</div>
        </div>
        <button onClick={load} className="rounded-lg px-3 py-1.5 text-[11.5px] font-semibold" style={{ border: "1px solid var(--line)", background: "var(--panel-2)", color: "var(--ink)" }}>Refresh</button>
      </div>
      <div className="mt-3 flex flex-col gap-2.5">
        {items.map((u) => <UnmatchedRow key={u.folder} media={media} item={u} busy={busy} onPick={pick} />)}
      </div>
    </div>
  );
}

function UnmatchedRow({ media, item, busy, onPick }: { media: "movie" | "series"; item: UnmatchedFolder; busy: boolean; onPick: (folder: string, tmdb: number) => void }) {
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
  const shown = results ?? item.candidates;

  return (
    <div className="rounded-lg p-2.5" style={{ border: "1px solid var(--line)", background: "var(--panel-2)" }}>
      <div className="flex items-center gap-2 text-[12px]">
        <span style={{ color: "var(--accent)" }}>📁</span>
        <span className="font-mono font-semibold">{item.folder}</span>
        <span className="text-ink-faint">— looked for “{item.title}”{item.year ? ` (${item.year})` : ""}</span>
      </div>
      <div className="mt-2 flex flex-wrap gap-1.5">
        {shown.length === 0 && <span className="text-[11px] text-ink-faint">No candidates — search a different title below.</span>}
        {shown.map((c) => (
          <button
            key={c.tmdb_id}
            disabled={busy}
            onClick={() => onPick(item.folder, c.tmdb_id)}
            title={c.overview}
            className="flex items-center gap-2 rounded-lg px-2 py-1 text-left text-[11.5px] disabled:opacity-50"
            style={{ border: "1px solid var(--line)", background: "var(--panel)" }}
          >
            {c.poster_url
              ? <img src={c.poster_url} alt="" className="h-9 w-6 flex-none rounded object-cover" />
              : <span className="grid h-9 w-6 flex-none place-items-center rounded text-[9px] text-ink-faint" style={{ background: "var(--panel-2)" }}>?</span>}
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
          className="flex-1 rounded-lg px-2 py-1 text-[11.5px]"
          style={{ background: "var(--panel)", border: "1px solid var(--line)", color: "var(--ink)" }}
        />
        <button onClick={search} disabled={searching} className="rounded-lg px-2.5 py-1 text-[11px] font-semibold" style={{ border: "1px solid var(--line)", background: "var(--panel)", color: "var(--ink)" }}>{searching ? "…" : "Search"}</button>
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
  const [nonce, setNonce] = useState(0); // bump to re-check after creating
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
  }, [p, kind, linkFrom, nonce]);

  // A result for an older value is stale; show nothing until the new one lands.
  const c = check && check.path === p ? check : null;
  const changed = p !== current.trim();
  const blocking = !!c?.error && (c.under_data_dir || changed);
  useEffect(() => { onBlocking?.(kind, p !== "" && blocking); }, [kind, p, blocking, onBlocking]);

  const create = async () => {
    setCreating(true); setCreateErr(null);
    try {
      const saved = await api.setLibraryPaths({ [kind]: p, create: true });
      onCreated?.(saved);
      setNonce((n) => n + 1);
    } catch (e) { setCreateErr((e as Error).message); } finally { setCreating(false); }
  };

  if (!p) return null;
  if (!c) return failed === p ? null : <div className="mt-1.5 text-[10.5px] text-ink-faint">Checking…</div>;
  if (c.error) {
    return (
      <div className="mt-1.5 flex flex-wrap items-center gap-2 text-[11px]" style={{ color: blocking ? "var(--reject)" : "var(--avoid)" }}>
        <span>{c.error}</span>
        {!c.exists && !c.under_data_dir && (
          <button onClick={create} disabled={creating} className="rounded-lg px-2.5 py-1 text-[11px] font-semibold disabled:opacity-50" style={{ border: "1px solid var(--accent-line)", color: "var(--accent)" }}>{creating ? "Creating…" : "Create it"}</button>
        )}
        {createErr && <span style={{ color: "var(--reject)" }}>{createErr}</span>}
      </div>
    );
  }
  const good = "var(--good)", warn = "var(--avoid)";
  return (
    <div className="mt-1.5 flex flex-wrap gap-1.5 text-[10.5px]">
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
    <div className="fixed inset-0 z-50 grid place-items-start justify-center overflow-y-auto p-6" style={{ background: "rgba(0,0,0,.55)" }} onClick={onClose}>
      <div className="mt-12 w-full max-w-[560px] rounded-2xl p-5" style={{ background: "var(--panel)", border: "1px solid var(--line)", boxShadow: "var(--shadow)" }} onClick={(e) => e.stopPropagation()}>
        <div className="mb-3 flex items-center justify-between gap-3">
          <h2 className="m-0 text-[15px] font-bold">Select a folder</h2>
          <button onClick={onClose} className="text-ink-faint hover:text-[var(--ink)]">✕</button>
        </div>

        <div className="mb-2 flex items-center gap-2">
          <button onClick={() => data && go(data.parent)} disabled={!data || data.path === "/"} className="rounded-lg px-2.5 py-1.5 text-[12px] font-semibold disabled:opacity-40" style={{ border: "1px solid var(--line)", background: "var(--panel-2)", color: "var(--ink)" }} title="Up one level">↑</button>
          <div className="min-w-0 flex-1 truncate rounded-lg px-2.5 py-1.5 font-mono text-[11.5px]" style={{ background: "var(--panel-2)", border: "1px solid var(--line)", color: "var(--ink-dim)" }}>{data?.path ?? "…"}</div>
        </div>

        {err && <div className="mb-2 rounded-lg p-2.5 text-[11.5px]" style={{ border: "1px solid var(--reject)", color: "var(--reject)" }}>{err}</div>}

        <div className="thin-scroll max-h-[46vh] overflow-y-auto rounded-lg" style={{ border: "1px solid var(--line)" }}>
          {!data ? (
            <div className="p-6 text-center text-[12px] text-ink-faint">Loading…</div>
          ) : data.dirs.length === 0 ? (
            <div className="p-6 text-center text-[12px] text-ink-faint">No sub-folders here.</div>
          ) : data.dirs.map((d) => (
            <button key={d.path} onClick={() => go(d.path)} className="flex w-full items-center gap-2 px-3 py-2 text-left text-[12.5px] hover:bg-[var(--panel-2)]" style={{ borderTop: "1px solid var(--line-soft)" }}>
              <span style={{ color: "var(--accent)" }}>📁</span>
              <span className="truncate">{d.name}</span>
            </button>
          ))}
        </div>

        <div className="mt-3 flex items-center justify-between gap-2">
          <span className="text-[10.5px]" style={{ color: data?.path_disabled ? "var(--avoid)" : "var(--ink-faint)" }}>
            {data?.path_disabled ? "This folder holds Arrmada's own data — go into your media folder and pick one there." : "Navigate into the folder you want, then select it."}
          </span>
          <button onClick={() => data && !data.path_disabled && onSelect(data.path)} disabled={!data || data.path_disabled}className="rounded-lg px-4 py-2 text-[12.5px] font-semibold disabled:opacity-50" style={{ background: "linear-gradient(150deg, var(--accent), var(--accent-deep))", color: "var(--accent-ink)" }}>Select this folder</button>
        </div>
      </div>
    </div>
  );
}
