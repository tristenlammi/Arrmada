import { useState } from "react";
import { api, ApiError, type DeletePreview } from "../lib/api";
import { disposalLine, useRecycleMode } from "../lib/disposal";
import { ConfirmDialog } from "./ConfirmDialog";

const plural = (n: number, one: string, many = one + "s") => `${n} ${n === 1 ? one : many}`;

// DeleteSeriesDialog is the one way to remove a show, from the grid or its page. Removing
// it from the library is the default; deleting its files is an explicit tick that first
// shows how many files, how big, and whether they go to the recycle bin or are gone for
// good. A very large show needs its title typed.
export function DeleteSeriesDialog({ series, onClose, onDeleted }: {
  series: { id: number; title: string; poster_url?: string };
  onClose: () => void;
  onDeleted: () => void;
}) {
  const mode = useRecycleMode();
  const [deleteFiles, setDeleteFiles] = useState(false);
  const [preview, setPreview] = useState<DeletePreview | null>(null);
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);

  const toggleFiles = (on: boolean) => {
    setDeleteFiles(on);
    setErr(null);
    if (on && !preview) {
      api.seriesDeletePreview(series.id).then(setPreview).catch((e: Error) => setErr(`Couldn't list the show's files: ${e.message}`));
    }
  };

  const big = deleteFiles && !!preview && preview.bytes > preview.confirm_over_bytes;
  const binMode = preview?.recycle ?? mode;

  const confirm = async () => {
    setBusy(true); setErr(null);
    try {
      await api.deleteSeries(series.id, deleteFiles, big ? series.title : undefined);
      onDeleted();
    } catch (e) {
      let msg = (e as Error).message;
      if (e instanceof ApiError && e.status === 409) {
        const moved = (e.body?.moved as string[] | undefined) ?? [];
        const failed = (e.body?.failed as string[] | undefined) ?? [];
        msg += `\nMoved before it stopped: ${moved.length ? plural(moved.length, "file") : "nothing"}.`;
        if (failed.length) msg += `\nCouldn't move: ${failed.join(", ")}.`;
        msg += "\nThe show is still in your library; episodes already moved now show as missing.";
      }
      setErr(msg);
      setBusy(false);
    }
  };

  const filesHint = !deleteFiles
    ? (binMode && !binMode.enabled ? "Permanently deletes every episode file and its subtitles — the recycle bin is switched off." : "Moves every episode file and its subtitles to the recycle bin.")
    : !preview
      ? "Counting the show's files…"
      : preview.files === 0
        ? "This series has no files on disk."
        : `${plural(preview.files, "episode file")} and ${plural(preview.sidecars, "subtitle")}. ${disposalLine(preview.bytes, binMode)}.`;

  return (
    <ConfirmDialog
      title={<>Remove “{series.title}”?</>}
      body={
        <div className="flex items-start gap-3">
          {series.poster_url && (
            <div className="h-[84px] w-[56px] flex-none overflow-hidden rounded-lg" style={{ background: "var(--panel-2)" }}>
              <img src={series.poster_url} alt="" className="h-full w-full object-cover" />
            </div>
          )}
          <p className="m-0">It'll be removed from your library and Arrmada will stop monitoring it. Its files stay on disk unless you tick the box below.</p>
        </div>
      }
      extra={
        <label className="flex items-start gap-2.5 rounded-lg p-3 text-[12.5px]" style={{ border: `1px solid ${deleteFiles ? "var(--reject)" : "var(--line)"}`, background: deleteFiles ? "var(--reject-soft)" : "var(--panel-2)", cursor: "pointer" }}>
          <input type="checkbox" checked={deleteFiles} disabled={busy} onChange={(e) => toggleFiles(e.target.checked)} className="mt-0.5" />
          <span>
            <span className="font-semibold" style={{ color: deleteFiles ? "var(--reject)" : "var(--ink)" }}>Also delete files from disk</span>
            <span className="mt-0.5 block text-[11px] text-ink-faint">{filesHint}</span>
          </span>
        </label>
      }
      typedPhrase={big ? series.title : undefined}
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
