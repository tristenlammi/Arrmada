import { useEffect, useState } from "react";
import { api, ApiError, type MovieBulkDeleteReply, type MovieDeletePreview } from "../lib/api";
import { disposalLine, useRecycleMode } from "../lib/disposal";
import { ConfirmDialog } from "./ConfirmDialog";
import { shownTitle } from "../lib/displayTitle";
import { formatPercent } from "../lib/format";

const plural = (n: number, one: string, many = one + "s") => `${n} ${n === 1 ? one : many}`;

// extrasLine names every leftover file the delete moves from the movie's own folder.
function extrasLine(p: MovieDeletePreview): string {
  const extras = p.extras ?? [];
  // Versions in separate folders: name each file with its folder, so two posters read apart.
  const several = new Set(extras.map((x) => x.folder)).size > 1;
  return extras.length ? ` Also its artwork and metadata: ${extras.map((x) => several ? `${x.folder}/${x.name}` : x.name).join(", ")}.` : "";
}

// foldersLine says which folders go, and why one whose other files stay is kept.
function foldersLine(p: MovieDeletePreview): string {
  return (p.folders ?? []).map((f) => f.removed
    ? ` The folder “${f.name}” is removed.`
    : f.kept ? ` The folder “${f.name}” stays: ${f.kept}.` : "").join("");
}

// DeleteMovieDialog is the one way to remove a movie, from the grid or its page. Removing
// it from the library is the default; deleting its files is an explicit tick that first
// shows which files, how big, and whether they go to the recycle bin or are gone for good.
// A download still in flight for it is cancelled by default; left running, it's held in
// Review when it finishes instead of putting the movie back.
export function DeleteMovieDialog({ movie, onClose, onDeleted }: {
  movie: { id: number; title: string; display_title?: string; poster_url?: string };
  onClose: () => void;
  onDeleted: () => void;
}) {
  const mode = useRecycleMode();
  const [deleteFiles, setDeleteFiles] = useState(false);
  const [cancelDownloads, setCancelDownloads] = useState(true);
  const [preview, setPreview] = useState<MovieDeletePreview | null>(null);
  const [previewErr, setPreviewErr] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);

  // Read up front: the in-flight downloads matter even when the files are kept.
  useEffect(() => {
    api.movieDeletePreview(movie.id).then(setPreview).catch((e: Error) => setPreviewErr(`Couldn't list the movie's files: ${e.message}`));
  }, [movie.id]);

  const binMode = preview?.recycle ?? mode;
  const pending = preview?.pending_downloads ?? [];

  const confirm = async () => {
    setBusy(true); setErr(null);
    try {
      // The leftovers this dialog listed go back with the delete: only those can go.
      await api.deleteMovie(movie.id, deleteFiles, cancelDownloads && pending.length > 0, (preview?.extras ?? []).map((x) => x.path));
      onDeleted();
    } catch (e) {
      let msg = (e as Error).message;
      if (e instanceof ApiError && e.status === 409) {
        const moved = (e.body?.moved as string[] | undefined) ?? [];
        if (moved.length) msg += `\nAlready in the recycle bin: ${moved.join(", ")} — those now show as missing.`;
        msg += "\nThe movie is still in your library, and its downloads are untouched.";
      }
      setErr(msg);
      setBusy(false);
    }
  };

  const filesHint = !deleteFiles
    ? (binMode && !binMode.enabled
      ? "Permanently deletes the movie's files and subtitles — the recycle bin is switched off. Artwork and metadata stay."
      : "Moves the movie's files and subtitles to the recycle bin, with the artwork and metadata in its own folder.")
    : !preview
      ? "Listing the movie's files…"
      : preview.versions.length === 0
        ? "This movie has no files on disk."
        : `${preview.versions.map((v) => v.file_name).join(", ")}${preview.sidecars ? ` and ${plural(preview.sidecars, "subtitle")}` : ""}.${extrasLine(preview)}${foldersLine(preview)} ${disposalLine(preview.bytes, binMode)}.`;

  const box = (on: boolean) => ({ border: `1px solid ${on ? "var(--reject)" : "var(--line)"}`, background: on ? "var(--reject-soft)" : "var(--panel-2)", cursor: "pointer" });

  return (
    <ConfirmDialog
      title={<>Remove “{shownTitle(movie)}”?</>}
      body={
        <div className="flex items-start gap-3">
          {movie.poster_url && (
            <div className="h-[84px] w-[56px] flex-none overflow-hidden rounded-lg" style={{ background: "var(--panel-2)" }}>
              <img src={movie.poster_url} alt="" className="h-full w-full object-cover" />
            </div>
          )}
          <p className="m-0">It'll be removed from your library and Arrmada will stop monitoring it. Its files stay on disk unless you tick the box below.</p>
        </div>
      }
      extra={
        <>
          <label className="flex items-start gap-2.5 rounded-lg p-3 text-base" style={box(deleteFiles)}>
            <input type="checkbox" checked={deleteFiles} disabled={busy} onChange={(e) => { setDeleteFiles(e.target.checked); setErr(null); }} className="mt-0.5" />
            <span className="min-w-0">
              <span className="font-semibold" style={{ color: deleteFiles ? "var(--reject-text)" : "var(--ink)" }}>Also delete files from disk</span>
              <span className="mt-0.5 block break-words text-xs text-ink-faint">{filesHint}</span>
            </span>
          </label>
          {pending.map((d) => (
            <label key={d.hash || d.title} className="flex items-start gap-2.5 rounded-lg p-3 text-base" style={box(cancelDownloads)}>
              <input type="checkbox" checked={cancelDownloads} disabled={busy} onChange={(e) => setCancelDownloads(e.target.checked)} className="mt-0.5" />
              <span className="min-w-0">
                <span className="font-semibold" style={{ color: cancelDownloads ? "var(--reject-text)" : "var(--ink)" }}>Cancel the download in progress and delete its partial data</span>
                <span className="mt-0.5 block break-all text-xs text-ink-faint">{d.title} · {formatPercent(d.progress)}</span>
                {!cancelDownloads && <span className="mt-0.5 block text-xs text-ink-faint">Left running, it waits in Activity → Needs you when it finishes instead of coming back into the library.</span>}
              </span>
            </label>
          ))}
        </>
      }
      confirmLabel={deleteFiles ? "Remove + delete files" : "Remove"}
      busyLabel="Removing…"
      busy={busy}
      confirmDisabled={deleteFiles && !preview}
      error={err ?? (deleteFiles ? previewErr : null)}
      onConfirm={confirm}
      onCancel={onClose}
    />
  );
}

// DeleteMoviesDialog is DeleteMovieDialog's bulk mode, for the mass editor: the same
// choices for a whole selection. Each film is removed exactly as its own Delete would; if
// the recycle bin refuses a file, that film and the ones after it are kept and listed, and
// onDone hears what happened so the page can keep the failures selected.
export function DeleteMoviesDialog({ movies, onClose, onDone }: {
  movies: { id: number; title: string; display_title?: string }[];
  onClose: () => void;
  onDone: (reply: MovieBulkDeleteReply) => void;
}) {
  const mode = useRecycleMode();
  const [deleteFiles, setDeleteFiles] = useState(false);
  const [cancelDownloads, setCancelDownloads] = useState(true);
  const [busy, setBusy] = useState(false);
  const [done, setDone] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  const titleOf = new Map(movies.map((m) => [m.id, shownTitle(m)]));

  const confirm = async () => {
    if (done) { onClose(); return; }
    setBusy(true); setErr(null);
    try {
      const r = await api.bulkDeleteMovies(movies.map((m) => m.id), deleteFiles, cancelDownloads);
      onDone(r);
      if (r.failed > 0) {
        const lines = r.results.filter((x) => !x.deleted).map((x) => `${titleOf.get(x.id) ?? `#${x.id}`}: ${x.error ?? "not removed"}`);
        setErr(`Removed ${plural(r.deleted, "movie")}; ${r.failed} ${r.failed === 1 ? "wasn't" : "weren't"} and stay in your library:\n${lines.join("\n")}`);
        setDone(true);
        setBusy(false);
        return;
      }
      onClose();
    } catch (e) {
      setErr((e as Error).message);
      setBusy(false);
    }
  };

  const filesHint = !deleteFiles
    ? "Their files stay on disk."
    : mode && !mode.enabled
      ? "Permanently deletes their files and subtitles — the recycle bin is switched off. Artwork and metadata stay."
      : "Moves their files and subtitles to the recycle bin. Artwork and metadata stay: delete a film on its own to see and clear its folder.";
  const box = (on: boolean) => ({ border: `1px solid ${on ? "var(--reject)" : "var(--line)"}`, background: on ? "var(--reject-soft)" : "var(--panel-2)", cursor: "pointer" });

  return (
    <ConfirmDialog
      title={<>Remove {plural(movies.length, "movie")}?</>}
      body={<p className="m-0">They'll be removed from your library and Arrmada will stop monitoring them. Files stay on disk unless you tick the box below.</p>}
      extra={
        <>
          <label className="flex items-start gap-2.5 rounded-lg p-3 text-base" style={box(deleteFiles)}>
            <input type="checkbox" checked={deleteFiles} disabled={busy || done} onChange={(e) => { setDeleteFiles(e.target.checked); setErr(null); }} className="mt-0.5" />
            <span className="min-w-0">
              <span className="font-semibold" style={{ color: deleteFiles ? "var(--reject-text)" : "var(--ink)" }}>Also delete their files from disk</span>
              <span className="mt-0.5 block break-words text-xs text-ink-faint">{filesHint}</span>
            </span>
          </label>
          <label className="flex items-start gap-2.5 rounded-lg p-3 text-base" style={box(cancelDownloads)}>
            <input type="checkbox" checked={cancelDownloads} disabled={busy || done} onChange={(e) => setCancelDownloads(e.target.checked)} className="mt-0.5" />
            <span className="min-w-0">
              <span className="font-semibold" style={{ color: cancelDownloads ? "var(--reject-text)" : "var(--ink)" }}>Cancel their downloads in progress and delete the partial data</span>
              {!cancelDownloads && <span className="mt-0.5 block text-xs text-ink-faint">Left running, each waits in Activity → Needs you when it finishes instead of coming back into the library.</span>}
            </span>
          </label>
        </>
      }
      confirmLabel={done ? "Close" : deleteFiles ? "Remove + delete files" : "Remove"}
      busyLabel="Removing…"
      tone={done ? "accent" : "danger"}
      busy={busy}
      error={err}
      onConfirm={confirm}
      onCancel={onClose}
    />
  );
}
