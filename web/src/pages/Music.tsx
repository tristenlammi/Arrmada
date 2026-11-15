import { useEffect, useMemo, useRef, useState } from "react";
import { Link } from "react-router-dom";
import { PageHeader } from "../components/PageHeader";
import { api, type Artist, type ArtistLookup } from "../lib/api";
import { qualityApi } from "../lib/qualityApi";
import { jobFailed, jobToast, useJob } from "../lib/useJob";
import { useSearchParamState } from "../lib/useSearchState";
import { isWanted, libraryStatus } from "../lib/status";
import { Button, ErrorState, FilterBar, IconButton, LibraryToolbar, Modal, Skeleton, StatusChip, TONE_HUE, useConfirm, useToast } from "../ui";
import { LibraryEmpty } from "../features/library/LibraryEmpty";
import { NewReleasesModal } from "../components/music/NewReleases";
import { ScanResultsModal } from "../components/music/ScanResults";
import { MISSING_MOVED_HINT } from "../components/music/fileMarks";
import { Artwork } from "../components/music/Artwork";
import { musicApi } from "../lib/musicApi";
import { Page } from "../ui/Page";

const FILTERS = [
  { key: "all", label: "All" },
  { key: "monitored", label: "Monitored" },
  { key: "wanted", label: "Wanted" },
  { key: "complete", label: "Complete" },
] as const;
type FilterKey = (typeof FILTERS)[number]["key"];
const FILTER_KEYS = FILTERS.map((f) => f.key);

function have(a: Artist): number {
  return a.stats?.have_tracks ?? 0;
}
function total(a: Artist): number {
  return a.stats?.tracks ?? 0;
}
// An artist is complete by its albums, not its tracks: every monitored album has all its
// tracks, and an album with no listing yet is unknown, never complete. Counting tracks
// made a scanned artist with 1 of 20 albums read 12/12, complete.
function isComplete(a: Artist): boolean {
  const monitoredAlbums = a.stats?.albums_monitored ?? 0;
  return monitoredAlbums > 0 && (a.stats?.albums_wanted ?? 0) === 0;
}
function statusInput(a: Artist) {
  const monitoredAlbums = a.stats?.albums_monitored ?? 0;
  const held = (a.stats?.albums_with_files ?? 0) > 0;
  let done = monitoredAlbums - (a.stats?.albums_wanted ?? 0);
  // Partial as soon as anything is on disk, not only once a whole album is.
  if (done === 0 && held && monitoredAlbums > 0) done = 0.5;
  return { multi: true, hasFile: held, monitored: a.monitored, have: done, total: monitoredAlbums };
}

function matches(a: Artist, f: FilterKey): boolean {
  switch (f) {
    case "monitored":
      return a.monitored;
    case "wanted":
      return isWanted(statusInput(a));
    case "complete":
      return isComplete(a);
    default:
      return true;
  }
}

export function Music() {
  const [artists, setArtists] = useState<Artist[] | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [filter, setFilter] = useSearchParamState<FilterKey>("filter", "all", { allowed: FILTER_KEYS });
  const [query, setQuery] = useSearchParamState<string>("q", "", { debounceMs: 300 });
  const [adding, setAdding] = useState(false);
  const [showNew, setShowNew] = useState(false);
  const [multiSelect, setMultiSelect] = useState(false);
  const [selected, setSelected] = useState<Set<number>>(new Set());
  const [bulkBusy, setBulkBusy] = useState(false);
  const [profiles, setProfiles] = useState<{ key: string; name: string; isDefault?: boolean }[]>([]);

  const toast = useToast();
  const confirm = useConfirm();
  // err shows the toast in the error colour: a scan that failed says so plainly.
  const say = (m: string, err = false) => toast(m, { tone: err ? "error" : "info", ms: 3500 });
  // The scan runs as a job; when it ends the toast says what it found, and the results
  // drawer opens with the details (MUS-37).
  const [scanJob, setScanJob] = useState<number | null>(null);
  const [showScan, setShowScan] = useState(false);
  const scan = useJob(scanJob, {
    giveUpMs: 12 * 60 * 60 * 1000, // a first scan of a big collection runs for a long time
    onDone: (j) => {
      setScanJob(null);
      load();
      say(jobToast(j, "Scan finished."), jobFailed(j));
      if (j.status === "succeeded") setShowScan(true);
    },
  });
  // A failed first load is an error with Retry, never "No artists yet"; a failed refresh
  // keeps the last good list under the error banner.
  const load = () =>
    api
      .artists()
      .then((a) => { setArtists(a); setError(null); })
      .catch((e: Error) => setError(e.message));
  const scanLibrary = async (full = false) => {
    setShowScan(false);
    try {
      const r = full ? await musicApi.scanLibrary(true) : await api.scanMusic();
      if (r.job_id) setScanJob(r.job_id);
      // The scan runs in the background — one MusicBrainz lookup per unknown
      // artist at a request per second — so say so rather than implying it's done.
      say("Scanning your music folder… this runs in the background.");
    } catch (e) {
      say((e as Error).message, true); // a toast: `error` is the list's own load state
    }
  };
  // Albums with track files gone from disk (MUS-15): never searched by themselves, so the
  // tools menu offers the search, after asking.
  const [missingAlbums, setMissingAlbums] = useState(0);
  const loadMissing = () => musicApi.missingAlbums().then((r) => setMissingAlbums(r.albums)).catch(() => {});
  const [missingJob, setMissingJob] = useState<number | null>(null);
  const missingSearch = useJob(missingJob, {
    giveUpMs: 6 * 60 * 60 * 1000,
    onDone: (j) => { setMissingJob(null); load(); void loadMissing(); say(jobToast(j, "Search finished."), jobFailed(j)); },
  });
  const searchAllMissing = async () => {
    const ok = await confirm({
      title: `Search for the missing tracks of ${missingAlbums} album${missingAlbums === 1 ? "" : "s"}?`,
      body: (
        <>
          <p className="m-0">
            Their track files are gone from disk. Arrmada doesn't download them again by itself, because they may only be renamed or on a disk
            that's away. Search only if they're really gone. One run searches up to 25 albums; run it again for the rest.
          </p>
          <p className="mb-0 mt-2">{MISSING_MOVED_HINT}</p>
        </>
      ),
      confirmLabel: "Search",
    });
    if (!ok) return;
    try {
      const r = await musicApi.searchAllMissing();
      if (r.job_id) setMissingJob(r.job_id);
    } catch (e) {
      say((e as Error).message, true);
    }
  };
  useEffect(() => {
    load();
    void loadMissing();
    qualityApi.qualityProfiles("music")
      .then((r) => setProfiles(r.profiles.map((p) => ({ key: p.key, name: p.name, isDefault: p.is_default }))))
      .catch(() => {});
  }, []);

  const toggleSelect = (id: number) =>
    setSelected((s) => {
      const n = new Set(s);
      if (n.has(id)) n.delete(id);
      else n.add(id);
      return n;
    });
  const clearSelect = () => setSelected(new Set());
  const exitSelect = () => {
    setMultiSelect(false);
    clearSelect();
  };

  // Partial failures are reported rather than swallowed. Promise.all rejects on the first
  // error, which skips the refresh and leaves the UI showing stale state for the ones that
  // DID change — allSettled lets us apply what worked and say what didn't.
  const runBulk = async (label: string, fn: (id: number) => Promise<unknown>) => {
    const ids = [...selected];
    setBulkBusy(true);
    try {
      const results = await Promise.allSettled(ids.map(fn));
      const failed = results.filter((r) => r.status === "rejected").length;
      if (failed === 0) say(`${label} ${ids.length} artist${ids.length === 1 ? "" : "s"}.`);
      else say(`${label} ${ids.length - failed} of ${ids.length}; ${failed} failed.`, true);
      clearSelect();
      load();
    } finally {
      setBulkBusy(false);
    }
  };

  const shown = useMemo(() => {
    const q = query.trim().toLowerCase();
    return (artists ?? [])
      .filter((a) => matches(a, filter))
      .filter((a) => !q || a.name.toLowerCase().includes(q));
  }, [artists, filter, query]);

  return (
    <>
      <PageHeader title="Music" />
      <Page width="wide">
        <LibraryToolbar
          count={artists === null ? null : artists.length}
          select={{ active: multiSelect, onToggle: () => (multiSelect ? exitSelect() : setMultiSelect(true)) }}
          tools={[{
            label: "Scan library",
            busyLabel: "Scanning…",
            busy: scan.running,
            progress: scan.running && scan.message ? scan.message : undefined,
            title: "Find music already in your music folder and catalog it where it is",
            onClick: () => void scanLibrary(),
          }, {
            label: "Last scan results",
            title: "What the last library scan found, and the folders waiting to be matched",
            onClick: () => setShowScan(true),
          }, {
            label: `Search for missing tracks (${missingAlbums})`,
            busyLabel: "Searching for missing tracks…",
            busy: missingSearch.running,
            progress: missingSearch.running && missingSearch.message ? missingSearch.message : undefined,
            title: "Search for the albums whose track files are gone from disk",
            hidden: missingAlbums === 0,
            onClick: () => void searchAllMissing(),
          }]}
          actions={[{
            label: "New releases",
            title: "What the daily refresh found on MusicBrainz lately, wanted or only listed",
            onClick: () => setShowNew(true),
          }]}
          primary={{ label: "+ Add artist", onClick: () => setAdding(true) }}
        />
        <FilterBar
          filters={FILTERS.map((f) => ({ key: f.key, label: f.label, count: (artists ?? []).filter((a) => matches(a, f.key)).length }))}
          active={filter}
          onFilter={(k) => setFilter(k as FilterKey)}
          query={query}
          onQuery={setQuery}
          noun="artists"
        />

        {error && artists !== null && (
          <div className="mb-4 rounded-lg p-3 text-sm" style={{ border: "1px solid var(--reject)", color: "var(--reject-text)" }}>
            {error}
          </div>
        )}

        {multiSelect && (
          <div
            className="mb-3 flex flex-wrap items-center gap-2 rounded-xl px-3.5 py-2.5"
            style={{ background: "var(--panel)", border: "1px solid var(--accent-line, var(--line))" }}
          >
            <span className="text-base font-semibold">
              {selected.size} selected
            </span>
            <button
              onClick={() => setSelected(new Set(shown.map((a) => a.id)))}
              className="rounded-lg px-2.5 py-1 text-sm font-semibold"
              style={{ border: "1px solid var(--line)", color: "var(--ink-dim)" }}
            >
              Select all ({shown.length})
            </button>
            <button
              onClick={clearSelect}
              disabled={selected.size === 0}
              className="rounded-lg px-2.5 py-1 text-sm font-semibold disabled:opacity-40"
              style={{ border: "1px solid var(--line)", color: "var(--ink-dim)" }}
            >
              Clear
            </button>
            <div className="ml-auto flex flex-wrap items-center gap-2">
              <button
                disabled={selected.size === 0 || bulkBusy}
                onClick={() => runBulk("Monitored", (id) => api.setArtistMonitored(id, true))}
                className="rounded-lg px-2.5 py-1 text-sm font-semibold disabled:opacity-40"
                style={{ border: "1px solid var(--accent)", color: "var(--accent-text)" }}
              >
                Monitor
              </button>
              <button
                disabled={selected.size === 0 || bulkBusy}
                onClick={() => runBulk("Unmonitored", (id) => api.setArtistMonitored(id, false))}
                className="rounded-lg px-2.5 py-1 text-sm font-semibold disabled:opacity-40"
                style={{ border: "1px solid var(--line)", color: "var(--ink-dim)" }}
              >
                Unmonitor
              </button>
              <select
                aria-label="Set the quality profile of the selected artists"
                disabled={selected.size === 0 || bulkBusy || profiles.length === 0}
                value=""
                onChange={(e) => {
                  const v = e.target.value;
                  if (v) void runBulk("Set the profile on", (id) => api.setArtistProfile(id, v));
                  e.target.value = "";
                }}
                className="rounded-lg px-2 py-1 text-sm disabled:opacity-40"
                style={{ background: "var(--panel-2)", border: "1px solid var(--line)", color: "var(--ink)" }}
              >
                <option value="">Quality profile…</option>
                {profiles.map((p) => (
                  <option key={p.key} value={p.key}>
                    {p.name}
                  </option>
                ))}
              </select>
              <button
                disabled={selected.size === 0 || bulkBusy}
                onClick={async () => {
                  const ok = await confirm({
                    title: `Remove ${selected.size} artist${selected.size === 1 ? "" : "s"} from Arrmada?`,
                    body: "Files on disk are kept.",
                    confirmLabel: "Remove",
                    tone: "danger",
                  });
                  if (ok) void runBulk("Removed", (id) => api.deleteArtist(id));
                }}
                className="rounded-lg px-2.5 py-1 text-sm font-semibold disabled:opacity-40"
                style={{ border: "1px solid var(--reject)", color: "var(--reject-text)" }}
              >
                Remove
              </button>
            </div>
          </div>
        )}

        {artists === null ? (
          error ? <ErrorState what="your artists" message={error} onRetry={load} /> : <Skeleton variant="list" />
        ) : artists.length === 0 ? (
          <LibraryEmpty noun="artists" folder="music folder" addLabel="Add artist" onScan={() => void scanLibrary()} scanning={scan.running} />
        ) : shown.length === 0 ? (
          <div className="rounded-xl p-10 text-center text-base text-ink-dim" style={{ border: "1px solid var(--line)" }}>
            No artists match this filter.
          </div>
        ) : (
          <div className="grid gap-3" style={{ gridTemplateColumns: "repeat(auto-fill, minmax(150px, 1fr))" }}>
            {shown.map((a) => (
              <ArtistCard
                key={a.id}
                a={a}
                selectable={multiSelect}
                selected={selected.has(a.id)}
                onToggleSelect={() => toggleSelect(a.id)}
              />
            ))}
          </div>
        )}
      </Page>

      {adding && (
        <AddArtistModal
          profiles={profiles}
          onClose={() => setAdding(false)}
          onAdded={(name) => {
            load();
            say(`Added ${name}.`);
          }}
        />
      )}
      {showNew && <NewReleasesModal onClose={() => { setShowNew(false); load(); }} />}
      {showScan && <ScanResultsModal onClose={() => setShowScan(false)} onRescan={(full) => void scanLibrary(full)} />}
    </>
  );
}

function ArtistCard({
  a,
  selectable,
  selected,
  onToggleSelect,
}: {
  a: Artist;
  selectable: boolean;
  selected: boolean;
  onToggleSelect: () => void;
}) {
  const done = have(a);
  const all = total(a);
  // Albums first (complete of all), tracks second.
  const albums = a.stats?.albums ?? 0;
  const albumsDone = a.stats?.albums_complete ?? 0;
  const pct = albums > 0 ? Math.round((albumsDone / albums) * 100) : 0;
  const st = libraryStatus(statusInput(a));
  const tone = TONE_HUE[st.tone];

  // The artist's photo when a source has one; otherwise one of its album covers, then its
  // initials (Artwork falls back when a cover won't load).
  const body = (
    <>
      <div className="relative">
        <Artwork src={a.image_url || a.cover_url} name={a.name} kind="artist" size={500} className="aspect-square w-full" />
        {selectable && (
          <span
            className="absolute left-2 top-2 grid h-5 w-5 place-items-center rounded text-xs font-bold"
            style={{
              border: `1px solid ${selected ? "var(--accent)" : "rgba(255,255,255,.8)"}`,
              background: selected ? "var(--accent)" : "rgba(14,10,7,.6)",
              color: "var(--accent-ink)",
            }}
          >
            {selected ? "✓" : ""}
          </span>
        )}
      </div>
      <div className="flex min-w-0 flex-col gap-1.5 p-3">
        <div className="min-w-0">
          <div className="truncate text-md font-semibold" title={a.name}>{a.name}</div>
          <div className="mt-0.5 truncate text-xs text-ink-faint">
            {albumsDone}/{albums} album{albums === 1 ? "" : "s"}
            {a.genres && a.genres.length > 0 && <> · {a.genres[0]}</>}
          </div>
        </div>
        <div className="flex flex-wrap items-center justify-between gap-x-2 gap-y-1">
          <StatusChip tone={st.tone}>{st.label}</StatusChip>
          <span className="font-mono text-xs text-ink-faint" title="Tracks on disk, of the tracks wanted">
            {all > 0 ? `${done}/${all}` : "—"}
          </span>
        </div>
        <div className="h-1.5 overflow-hidden rounded-full" style={{ background: "var(--line)" }}>
          <div className="h-full rounded-full" style={{ width: `${pct}%`, background: tone }} />
        </div>
      </div>
    </>
  );

  const cls = "flex min-w-0 flex-col overflow-hidden rounded-xl text-left transition-colors hover:bg-[var(--panel-2)]";
  const style = {
    background: selected ? "var(--accent-soft)" : "var(--panel)",
    border: `1px solid ${selected ? "var(--accent)" : "var(--line)"}`,
  };

  // In select mode the card stops being a link entirely — a card that both navigates and
  // selects depending on where you click is how you lose a selection by mis-clicking.
  if (selectable) {
    return (
      <button type="button" onClick={onToggleSelect} className={cls} style={style}>
        {body}
      </button>
    );
  }
  return (
    <Link to={`/music/${a.id}`} className={cls} style={style}>
      {body}
    </Link>
  );
}

// AddArtistModal searches MusicBrainz. The disambiguation is shown prominently because it's
// very often the only way to tell two same-named artists apart.
function AddArtistModal({ profiles, onClose, onAdded }: { profiles: { key: string; name: string; isDefault?: boolean }[]; onClose: () => void; onAdded: (name: string) => void }) {
  const [query, setQuery] = useState("");
  const [results, setResults] = useState<ArtistLookup[] | null>(null);
  const [searching, setSearching] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [saving, setSaving] = useState<string | null>(null);
  const [monitor, setMonitor] = useState<"all" | "future" | "latest" | "none">("all");
  // "" adds under the music default profile.
  const [profile, setProfile] = useState(() => profiles.find((p) => p.isDefault)?.key ?? "");
  const [searchNow, setSearchNow] = useState(true);
  const inputRef = useRef<HTMLInputElement>(null);

  const search = async (e?: React.FormEvent) => {
    e?.preventDefault();
    if (!query.trim()) return;
    setSearching(true);
    setError(null);
    try {
      setResults(await api.lookupArtists(query.trim()));
    } catch (err) {
      setError((err as Error).message);
      setResults([]);
    } finally {
      setSearching(false);
    }
  };

  const add = async (r: ArtistLookup) => {
    setSaving(r.mbid);
    setError(null);
    try {
      await api.addArtist({ mbid: r.mbid, monitored: true, monitor, quality_profile: profile || undefined, search_now: searchNow });
      onAdded(r.name);
      onClose();
    } catch (err) {
      setError((err as Error).message);
      setSaving(null);
    }
  };

  return (
    <Modal onClose={onClose} labelledBy="add-artist-title" size="lg" initialFocus={inputRef} dismissible={saving === null}>
        <div className="mb-1 flex items-center justify-between">
          <h2 id="add-artist-title" className="m-0 text-lg font-bold">Add an artist</h2>
          <IconButton label="Close" onClick={onClose} className="h-7 w-7 rounded-md text-ink-faint hover:text-[var(--ink)]">
            ✕
          </IconButton>
        </div>
        <p className="mb-3 text-sm text-ink-dim">
          Searches MusicBrainz. Adding an artist brings in their albums; track listings fill in when you open an album.
        </p>

        <form className="mb-3 flex gap-2" onSubmit={search}>
          <input
            ref={inputRef}
            aria-label="Artist name"
            value={query}
            onChange={(e) => setQuery(e.target.value)}
            placeholder="Artist name…"
            className="min-w-0 flex-1 rounded-lg px-3 py-2 text-md"
            style={{ background: "var(--panel-2)", border: "1px solid var(--line)", color: "var(--ink)" }}
          />
          <Button type="submit" variant="primary" busy={searching} busyLabel="Searching…" className="disabled:opacity-50">
            Search
          </Button>
        </form>

        <label className="mb-3 flex flex-wrap items-center gap-2 text-sm text-ink-dim">
          Want
          <select
            value={monitor}
            onChange={(e) => setMonitor(e.target.value as typeof monitor)}
            className="rounded-lg px-2 py-1 text-sm"
            style={{ background: "var(--panel-2)", border: "1px solid var(--line)", color: "var(--ink)" }}
          >
            <option value="all">every album listed</option>
            <option value="latest">the latest album out, and new releases</option>
            <option value="future">new releases only</option>
            <option value="none">nothing for now</option>
          </select>
        </label>
        <div className="mb-3 flex flex-wrap items-center gap-x-4 gap-y-2 text-sm text-ink-dim">
          {profiles.length > 0 && (
            <label className="flex items-center gap-2">
              Quality
              <select
                value={profile}
                onChange={(e) => setProfile(e.target.value)}
                className="rounded-lg px-2 py-1 text-sm"
                style={{ background: "var(--panel-2)", border: "1px solid var(--line)", color: "var(--ink)" }}
              >
                {!profiles.some((p) => p.isDefault) && <option value="">the music default</option>}
                {profiles.map((p) => (
                  <option key={p.key} value={p.key}>{p.name}</option>
                ))}
              </select>
            </label>
          )}
          <label className="flex items-center gap-2">
            <input type="checkbox" checked={searchNow} onChange={(e) => setSearchNow(e.target.checked)} />
            Start searching now
          </label>
        </div>

        {error && (
          <div className="mb-3 rounded-lg p-2.5 text-sm" style={{ border: "1px solid var(--reject)", color: "var(--reject-text)" }}>
            {error}
          </div>
        )}

        {results !== null && (
          results.length === 0 ? (
            <div className="rounded-xl p-6 text-center text-base text-ink-dim" style={{ border: "1px solid var(--line)" }}>
              No artists found.
            </div>
          ) : (
            <div className="thin-scroll flex max-h-[52vh] flex-col overflow-y-auto">
              {results.map((r) => (
                <div key={r.mbid} className="flex items-center gap-3 border-b py-2.5" style={{ borderColor: "var(--line)" }}>
                  <div className="min-w-0 flex-1">
                    <div className="truncate text-md font-medium">
                      {r.name}
                      {r.disambiguation && <span className="ml-2 text-sm text-ink-faint">({r.disambiguation})</span>}
                    </div>
                    {r.aka && <div className="truncate text-sm text-ink-dim">also known as {r.aka}</div>}
                    <div className="truncate font-mono text-xs text-ink-faint">
                      {[r.type, r.country].filter(Boolean).join(" · ") || r.mbid}
                    </div>
                  </div>
                  <button
                    disabled={saving !== null}
                    onClick={() => add(r)}
                    className="flex-none rounded-lg px-3 py-1.5 text-sm font-semibold disabled:opacity-50"
                    style={{ border: "1px solid var(--accent)", color: "var(--accent-text)" }}
                  >
                    {saving === r.mbid ? "Adding…" : "Add"}
                  </button>
                </div>
              ))}
            </div>
          )
        )}
    </Modal>
  );
}
