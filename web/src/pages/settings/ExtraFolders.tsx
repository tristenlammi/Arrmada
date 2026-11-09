import { useState } from "react";
import { FOLDERS_KEY, foldersChanged } from "../../components/FolderSelect";
import { FOLDER_RULES, foldersApi, type FolderMedia, type FolderRule, type LibraryFolder, type LibraryFolderSet, type LibraryFolders } from "../../lib/foldersApi";
import { isAdmin, useMe } from "../../lib/me";
import { useQuery } from "../../lib/query";
import { jobDone, jobFailed } from "../../lib/useJob";
import { foundLine, scanLine, unmatchedOf, useLibraryScan } from "../../lib/useLibraryScan";
import { Button, IconButton, useConfirm, useToast } from "../../ui";
import { FolderPicker, needsReviewChanged } from "../Library";

const field = { background: "var(--panel)", border: "1px solid var(--line)", color: "var(--ink)" } as const;

// ExtraFolders is Settings → Library's "More folders" card: extra Movies and TV folders
// next to the main one (a Kids, Animation or Anime folder, often its own Plex or Jellyfin
// library). Each can carry a rule; a new title goes to the first folder, top to bottom,
// whose rule matches it, otherwise to the main folder. Admins change them; managers see
// them. Removing one that still holds titles is refused and says what to do. Adding a
// folder doesn't import what's already in it, so each extra folder says what it holds on
// disk and can be scanned on its own (GitHub #3).
export function ExtraFolders() {
  const { user } = useMe();
  const admin = isAdmin(user);
  // The folders with what each extra one holds on disk: a key of its own (the add dialogs'
  // list never touches the disk), reloaded with the rest by foldersChanged.
  const { data, error } = useQuery<LibraryFolders>(`${FOLDERS_KEY}:disk`, foldersApi.listWithDisk, { staleMs: 60_000 });
  // A folder's own downloads folder is offered on every row once a library has several
  // folders or one is set; a one-folder install sees just the line below until it asks.
  const [opened, setOpened] = useState(false);
  if (error && !data) return <div className="text-sm" style={{ color: "var(--reject-text)" }}>Couldn't load the folders: {error.message}</div>;
  if (!data) return <div className="text-base text-ink-dim">Loading…</div>;
  const movie: LibraryFolderSet = data.movie ?? { multi: false, folders: [] };
  const series: LibraryFolderSet = data.series ?? { multi: false, folders: [] };
  const movieFolders = movie.folders ?? [];
  const seriesFolders = series.folders ?? [];
  const anySet = [...movieFolders, ...seriesFolders].some((f) => !!f.downloads);
  const offer = admin && (opened || anySet || !!movie.multi || !!series.multi);
  return (
    <div className="flex flex-col gap-5">
      <FolderList media="movie" title="Movies" set={movie} admin={admin} offer={offer} />
      <FolderList media="series" title="TV Shows" set={series} admin={admin} offer={offer} />
      <p className="m-0 text-sm text-ink-faint">
        A new folder has to be inside a folder mounted into the container. Under the same mount as your other libraries (say <span className="font-mono">/storage</span>), just pick it; anywhere else, add its mount to <span className="font-mono">docker-compose.override.yml</span> first (see <span className="font-mono">docker-compose.override.example.yml</span>) and recreate the container, then pick it here. Moving a title between folders moves its files: use <b>Move to folder</b> on the title's page.
      </p>
      {offer ? (
        <p className="m-0 text-sm text-ink-faint">
          A folder on another disk can download onto that disk, so its imports are hardlinks rather than copies (<b>Download to a folder on this disk…</b>). That folder must be mounted into qBittorrent's container too, at the same path. It applies to torrents; usenet downloads keep using SABnzbd's own folders. A seeding download stays where it is when its title moves to another folder; later downloads follow the title.
        </p>
      ) : admin && (
        <button type="button" className="self-start text-sm font-semibold underline-offset-2 hover:underline" style={{ color: "var(--accent-text)" }} onClick={() => setOpened(true)}>
          Libraries on different disks? Give a folder its own downloads folder…
        </button>
      )}
      {!admin && <p className="m-0 text-sm text-ink-faint">Only an admin can change these folders.</p>}
    </div>
  );
}

function FolderList({ media, title, set, admin, offer }: { media: FolderMedia; title: string; set: LibraryFolderSet; admin: boolean; offer: boolean }) {
  const toast = useToast();
  const [picking, setPicking] = useState(false);
  const [busy, setBusy] = useState(false);
  // The folder just added: its row offers to scan what's already in it.
  const [fresh, setFresh] = useState<number | null>(null);
  const folders = set.folders ?? [];
  const extras = folders.filter((f) => !f.default);
  const main = folders.find((f) => f.default);

  const add = async (path: string) => {
    setPicking(false);
    setBusy(true);
    try {
      const f = await foldersApi.add({ media, path });
      setFresh(f.id);
      foldersChanged();
      toast(`Added ${f.label} to ${title}.`, { tone: "good" });
    } catch (e) {
      toast((e as Error).message, { tone: "error", ms: 8000 });
    } finally {
      setBusy(false);
    }
  };
  // Move one folder up or down; the order is the order rules are tried in.
  const move = async (id: number, dir: -1 | 1) => {
    const ids = extras.map((f) => f.id);
    const i = ids.indexOf(id), j = i + dir;
    if (i < 0 || j < 0 || j >= ids.length) return;
    [ids[i], ids[j]] = [ids[j], ids[i]];
    setBusy(true);
    try {
      await foldersApi.order(media, ids);
      foldersChanged();
    } catch (e) {
      toast((e as Error).message, { tone: "error" });
    } finally {
      setBusy(false);
    }
  };

  return (
    <div>
      <div className="mb-2 flex flex-wrap items-center justify-between gap-2">
        <span className="text-md font-semibold">{title}</span>
        {admin && <Button size="sm" variant="secondary" onClick={() => setPicking(true)} busy={busy} busyLabel="Saving…">Add a folder…</Button>}
      </div>
      <div className="flex flex-col gap-2">
        {main && (
          <div className="rounded-xl p-3" style={{ border: "1px solid var(--line)", background: "var(--panel-2)" }}>
            <div className="flex flex-wrap items-center justify-between gap-2">
              <span className="text-base font-semibold">{main.label} <span className="font-normal text-ink-faint">— the main folder</span></span>
              <span className="text-xs text-ink-faint">{titles(main.titles)}</span>
            </div>
            <div className="mt-0.5 font-mono text-xs text-ink-faint">{main.path || "Not set — choose it in Media folders above"}</div>
            {extras.length > 0 && <div className="mt-1 text-xs text-ink-faint">Anything no rule below matches goes here.</div>}
            {main.path && <DownloadsTo f={main} admin={admin} offer={offer} />}
          </div>
        )}
        {extras.map((f, i) => (
          <ExtraRow key={f.id} f={f} admin={admin} first={i === 0} last={i === extras.length - 1} busy={busy} onMove={move} offer={offer} fresh={f.id === fresh} />
        ))}
        {extras.length === 0 && <div className="text-sm text-ink-faint">No other {title} folders. Everything goes to the main one.</div>}
      </div>
      {picking && <FolderPicker onClose={() => setPicking(false)} onSelect={(p) => void add(p)} />}
    </div>
  );
}

function titles(n: number) {
  return `${n.toLocaleString()} title${n === 1 ? "" : "s"}`;
}

// DownloadsTo is a folder's own downloads folder (GitHub #11): torrents for its titles
// save there, on the library's disk, so imports hardlink instead of copying. Most installs
// keep one Downloads folder, so until one is set the row shows at most a small link: for
// admins, once the card offers it (offer); for managers, nothing.
function DownloadsTo({ f, admin, offer }: { f: LibraryFolder; admin: boolean; offer: boolean }) {
  const toast = useToast();
  const [picking, setPicking] = useState(false);
  const [saving, setSaving] = useState(false);
  const dl = f.downloads ?? "";

  const save = async (path: string) => {
    setPicking(false);
    setSaving(true);
    try {
      const res = await foldersApi.setDownloads(f.media, f.id, path);
      foldersChanged();
      if (res.warning) toast(`Saved, but ${res.warning}.`, { ms: 10000 });
      else toast(path ? `${f.label} now downloads to ${path}.` : `${f.label} downloads to the Downloads folder again.`, { tone: "good" });
    } catch (e) {
      toast((e as Error).message, { tone: "error", ms: 8000 });
    } finally {
      setSaving(false);
    }
  };

  if (!dl && !offer) return null;
  return (
    <div className="mt-2 text-xs">
      {dl ? (
        <div className="flex flex-wrap items-center gap-2">
          <span className="font-mono text-2xs uppercase text-ink-faint">Downloads to</span>
          <span className="min-w-0 break-all font-mono text-ink-dim">{dl}</span>
          {admin && (
            <>
              <Button size="sm" variant="ghost" onClick={() => setPicking(true)} disabled={saving}>Change…</Button>
              <Button size="sm" variant="ghost" onClick={() => void save("")} disabled={saving}>Use the Downloads folder</Button>
            </>
          )}
        </div>
      ) : (
        <button type="button" className="text-xs font-semibold underline-offset-2 hover:underline disabled:opacity-50" style={{ color: "var(--accent-text)" }} onClick={() => setPicking(true)} disabled={saving}>
          Download to a folder on this disk…
        </button>
      )}
      {f.downloads_warning && <div className="mt-1" style={{ color: "var(--avoid-text)" }}>{f.downloads_warning}.</div>}
      {picking && <FolderPicker initial={dl || undefined} onClose={() => setPicking(false)} onSelect={(p) => void save(p)} />}
    </div>
  );
}

function ExtraRow({ f, admin, first, last, busy, onMove, offer, fresh }: {
  f: LibraryFolder;
  admin: boolean;
  first: boolean;
  last: boolean;
  busy: boolean;
  onMove: (id: number, dir: -1 | 1) => void;
  offer: boolean;
  /** Just added: offer to scan what's already in it. */
  fresh: boolean;
}) {
  const toast = useToast();
  const confirm = useConfirm();
  const [label, setLabel] = useState(f.label);
  const [saving, setSaving] = useState(false);

  const save = async (body: { label?: string; rule?: FolderRule }) => {
    setSaving(true);
    try {
      await foldersApi.update(f.id, body);
      foldersChanged();
    } catch (e) {
      toast((e as Error).message, { tone: "error" });
      setLabel(f.label);
    } finally {
      setSaving(false);
    }
  };
  const commitLabel = () => {
    const v = label.trim();
    if (v && v !== f.label) void save({ label: v });
    else setLabel(f.label);
  };
  const remove = async () => {
    const yes = await confirm({
      title: `Remove the “${f.label}” folder from Arrmada?`,
      body: f.titles > 0
        ? `${titles(f.titles)} still belong to it. Move them to another folder first (Move to folder on each title's page) — Arrmada won't leave them pointing at a folder it no longer knows.`
        : "Only Arrmada forgets it: the folder and anything in it stay on disk.",
      confirmLabel: "Remove",
      tone: "danger",
    });
    if (!yes) return;
    setSaving(true);
    try {
      await foldersApi.remove(f.id);
      foldersChanged();
      toast(`Removed ${f.label}.`);
    } catch (e) {
      toast((e as Error).message, { tone: "error", ms: 8000 });
    } finally {
      setSaving(false);
    }
  };
  const rule = FOLDER_RULES.find((r) => r.value === f.rule) ?? FOLDER_RULES[0];

  return (
    <div className="rounded-xl p-3" style={{ border: "1px solid var(--line)", background: "var(--panel-2)" }}>
      <div className="flex flex-wrap items-center gap-2">
        {admin ? (
          <input
            aria-label={`Name of the ${f.label} folder`}
            value={label}
            onChange={(e) => setLabel(e.target.value)}
            onBlur={commitLabel}
            onKeyDown={(e) => { if (e.key === "Enter") { e.preventDefault(); commitLabel(); } if (e.key === "Escape") setLabel(f.label); }}
            disabled={saving}
            className="min-w-0 flex-1 rounded-lg px-2.5 py-1.5 text-base font-semibold"
            style={field}
          />
        ) : (
          <span className="min-w-0 flex-1 text-base font-semibold">{f.label}</span>
        )}
        <span className="text-xs text-ink-faint">{titles(f.titles)}</span>
        {admin && (
          <>
            <IconButton label="Try this folder's rule earlier" onClick={() => onMove(f.id, -1)} disabled={first || busy || saving} className="h-[30px] w-[30px] rounded-lg disabled:opacity-40" style={field}>↑</IconButton>
            <IconButton label="Try this folder's rule later" onClick={() => onMove(f.id, 1)} disabled={last || busy || saving} className="h-[30px] w-[30px] rounded-lg disabled:opacity-40" style={field}>↓</IconButton>
            <Button size="sm" variant="ghost" onClick={remove} disabled={saving}>Remove</Button>
          </>
        )}
      </div>
      <div className="mt-1 font-mono text-xs text-ink-faint">{f.path}</div>
      <FolderScan f={f} fresh={fresh} />
      <div className="mt-2 flex flex-wrap items-center gap-2">
        {admin ? (
          <label className="flex items-center gap-2 text-sm">
            <span className="font-mono text-2xs uppercase text-ink-faint">Rule</span>
            <select value={f.rule} onChange={(e) => void save({ rule: e.target.value as FolderRule })} disabled={saving} className="rounded-lg px-2 py-1 text-sm" style={field}>
              {FOLDER_RULES.map((r) => <option key={r.value} value={r.value}>{r.label}</option>)}
            </select>
          </label>
        ) : (
          <span className="text-sm text-ink-dim">Rule: {rule.label}</span>
        )}
        <span className="text-xs text-ink-faint">{rule.help}</span>
      </div>
      <DownloadsTo f={f} admin={admin} offer={offer} />
    </div>
  );
}

// FolderScan is an extra folder's scan. Adding a folder doesn't import what's already in
// it, so the row says what's there — the folder just added offers a scan straight away,
// one with no titles yet says how much isn't in the library — and scans just that folder,
// following the job and saying what it found. A folder that's missing, or empty while
// titles belong to it (a share that isn't mounted), says so and is never scanned.
function FolderScan({ f, fresh }: { f: LibraryFolder; fresh: boolean }) {
  const scan = useLibraryScan(
    f.media === "movie" ? "movie.scan" : "series.scan",
    () => foldersApi.scan(f.media, f.path),
    () => {
      foldersChanged(); // the title counts and what's on disk changed
      needsReviewChanged(); // and so may the needs-review list in Media folders
    },
    `folder:${f.id}`,
  );
  if (!f.disk) return null; // not looked at (yet)
  if (f.disk === "missing") {
    return <div className="mt-1.5 text-sm" style={{ color: "var(--reject-text)" }}>Can't find this folder — is its drive or share mounted?</div>;
  }
  if (f.disk === "unknown") {
    return <div className="mt-1.5 text-sm" style={{ color: "var(--reject-text)" }}>This folder is not responding — check its drive or share.</div>;
  }
  if (f.disk === "empty") {
    return f.titles > 0
      ? <div className="mt-1.5 text-sm" style={{ color: "var(--avoid-text)" }}>This folder looks empty, but titles belong to it — is its drive or share mounted?</div>
      : null;
  }

  const n = f.on_disk ?? 0;
  // The scan that ended, while it's still the one shown.
  const ended = !scan.running && scan.job && jobDone(scan.job.status) ? scan.job : null;
  const review = unmatchedOf(ended);
  let line: { text: string; color: string } | null = null;
  if (scan.running || scan.note?.error || (ended && (jobFailed(ended) || ended.status === "cancelled"))) {
    const s = scanLine(f.label, scan);
    if (s) line = { text: s.text, color: s.tone === "error" ? "var(--reject-text)" : "var(--ink-dim)" };
  } else if (ended) {
    line = { text: foundLine(ended), color: "var(--good-text)" };
  }
  // Nothing asked yet: the folder just added offers the scan; one with no titles says
  // what isn't in the library.
  const prompt = !line && n > 0
    ? fresh
      ? `This folder has ${plural(n, "item", "items")}. Scan it now to add what's already there?`
      : f.titles === 0 ? `${plural(n, "folder here isn't", "folders here aren't")} in the library yet.` : ""
    : "";

  return (
    <div className="mt-1.5 flex flex-wrap items-center gap-x-2 gap-y-1 text-sm">
      {line && <span role="status" style={{ color: line.color }}>{line.text}</span>}
      {review > 0 && <a href="#needs-review" className="font-semibold" style={{ color: "var(--accent-text)" }}>Review them ↑</a>}
      {prompt && <span className="text-ink-dim">{prompt}</span>}
      <Button
        size="sm"
        variant={prompt ? "primary" : "ghost"}
        onClick={() => void scan.start()}
        busy={scan.running}
        busyLabel="Scanning…"
        aria-label={prompt && fresh ? undefined : `Scan the ${f.label} folder`}
      >
        {prompt && fresh ? "Scan now" : "Scan"}
      </Button>
    </div>
  );
}
