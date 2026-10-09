import { useState } from "react";
import { api, ApiError, type MovieDeletePreview } from "../lib/api";
import { disposalLine, useRecycleMode } from "../lib/disposal";
import { ConfirmDialog } from "./ConfirmDialog";

const plural = (n: number, one: string, many = one + "s") => `${n} ${n === 1 ? one : many}`;

// DeleteMovieDialog is the one way to remove a movie, from the grid or its page. Removing
// it from the library is the default; deleting its files is an explicit tick that first
// shows which files, how big, and whether they go to the recycle bin or are gone for good.
export function DeleteMovieDialog({ movie, onClose, onDeleted }: {
  movie: { id: number; title: string; poster_url?: string };
  onClose: () => void;
  onDeleted: () => void;
}) {
  const mode = useRecycleMode();
  const [deleteFiles, setDeleteFiles] = useState(false);
  const [preview, setPreview] = useState<MovieDeletePreview | null>(null);
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);

  const toggleFiles = (on: boolean) => {
    setDeleteFiles(on);
    setErr(null);
    if (on && !preview) {
      api.movieDeletePreview(movie.id).then(setPreview).catch((e: Error) => setErr(`Couldn't list the movie's files: ${e.message}`));
    }
  };

  const binMode = preview?.recycle ?? mode;

  const confirm = async () => {
    setBusy(true); setErr(null);
    try {
      await api.deleteMovie(movie.id, deleteFiles);
      onDeleted();
    } catch (e) {
      let msg = (e as Error).message;
      if (e instanceof ApiError && e.status === 409) {
        const moved = (e.body?.moved as string[] | undefined) ?? [];
        if (moved.length) msg += `\nAlready in the recycle bin: ${moved.join(", ")} — those now show as missing.`;
        msg += "\nThe movie is still in your library.";
      }
      setErr(msg);
      setBusy(false);
    }
  };

  const filesHint = !deleteFiles
    ? (binMode && !binMode.enabled ? "Permanently deletes the movie's files and subtitles — the recycle bin is switched off." : "Moves the movie's files and subtitles to the recycle bin.")
    : !preview
      ? "Listing the movie's files…"
      : preview.versions.length === 0
        ? "This movie has no files on disk."
        : `${preview.versions.map((v) => v.file_name).join(", ")}${preview.sidecars ? ` and ${plural(preview.sidecars, "subtitle")}` : ""}. ${disposalLine(preview.bytes, binMode)}.`;

  return (
    <ConfirmDialog
      title={<>Remove “{movie.title}”?</>}
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
        <label className="flex items-start gap-2.5 rounded-lg p-3 text-[12.5px]" style={{ border: `1px solid ${deleteFiles ? "var(--reject)" : "var(--line)"}`, background: deleteFiles ? "var(--reject-soft)" : "var(--panel-2)", cursor: "pointer" }}>
          <input type="checkbox" checked={deleteFiles} disabled={busy} onChange={(e) => toggleFiles(e.target.checked)} className="mt-0.5" />
          <span className="min-w-0">
            <span className="font-semibold" style={{ color: deleteFiles ? "var(--reject)" : "var(--ink)" }}>Also delete files from disk</span>
            <span className="mt-0.5 block break-words text-[11px] text-ink-faint">{filesHint}</span>
          </span>
        </label>
      }
      confirmLabel={deleteFiles ? "Remove + delete files" : "Remove"}
      busyLabel="Removing…"
      busy={busy}
      confirmDisabled={deleteFiles && !preview}
      error={err}
      onConfirm={confirm}
      onCancel={onClose}
    />
  );
}
