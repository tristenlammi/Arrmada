import { useEffect, useState } from "react";
import { ApiError, type DeletePreview, type PendingSeriesDownload } from "../lib/api";
import { downloadPercent } from "../lib/percent";
import { seriesApi } from "../lib/seriesApi";
import { disposalLine, useRecycleMode } from "../lib/disposal";
import { ConfirmDialog } from "./ConfirmDialog";
import { shownTitle } from "../lib/displayTitle";

const plural = (n: number, one: string, many = one + "s") => `${n} ${n === 1 ? one : many}`;

// DeleteSeriesDialog is the one way to remove a show, from the grid or its page. Removing
// it from the library is the default; deleting its files is an explicit tick that first
// shows how many files, how big, and whether they go to the recycle bin or are gone for
// good. A very large show needs its title typed. Its downloads still in progress are
// cancelled by default (#111); left running, they wait in Needs you when they finish.
export function DeleteSeriesDialog({ series, onClose, onDeleted }: {
  series: { id: number; title: string; display_title?: string; poster_url?: string };
  onClose: () => void;
  onDeleted: () => void;
}) {
  const mode = useRecycleMode();
  const [deleteFiles, setDeleteFiles] = useState(false);
  const [cancelDownloads, setCancelDownloads] = useState(true);
  const [preview, setPreview] = useState<DeletePreview | null>(null);
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);

  // Read up front: the downloads in progress matter even when the files are kept.
  useEffect(() => {
    seriesApi.seriesDeletePreview(series.id).then(setPreview).catch((e: Error) => setErr(`Couldn't list the show's files: ${e.message}`));
  }, [series.id]);
  const pending = preview?.pending_downloads ?? [];

  const toggleFiles = (on: boolean) => {
    setDeleteFiles(on);
    setErr(null);
  };

  const big = deleteFiles && !!preview && preview.bytes > preview.confirm_over_bytes;
  const binMode = preview?.recycle ?? mode;

  const confirm = async () => {
    setBusy(true); setErr(null);
    try {
      await seriesApi.deleteSeries(series.id, deleteFiles, big ? series.title : undefined, cancelDownloads && pending.length > 0);
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
      title={<>Remove “{shownTitle(series)}”?</>}
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
        <div className="flex flex-col gap-2">
        <label className="flex items-start gap-2.5 rounded-lg p-3 text-base" style={{ border: `1px solid ${deleteFiles ? "var(--reject)" : "var(--line)"}`, background: deleteFiles ? "var(--reject-soft)" : "var(--panel-2)", cursor: "pointer" }}>
          <input type="checkbox" checked={deleteFiles} disabled={busy} onChange={(e) => toggleFiles(e.target.checked)} className="mt-0.5" />
          <span>
            <span className="font-semibold" style={{ color: deleteFiles ? "var(--reject-text)" : "var(--ink)" }}>Also delete files from disk</span>
            <span className="mt-0.5 block text-xs text-ink-faint">{filesHint}</span>
          </span>
        </label>
        {pending.length > 0 && <CancelDownloadsBox pending={pending} checked={cancelDownloads} disabled={busy} onChange={setCancelDownloads} />}
        </div>
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

// CancelDownloadsBox offers to cancel a show's (or a season's) downloads still in progress,
// naming each one.
export function CancelDownloadsBox({ pending, checked, disabled, onChange, leftRunning }: {
  pending: PendingSeriesDownload[];
  checked: boolean;
  disabled: boolean;
  onChange: (on: boolean) => void;
  /** What happens to a download left running; a removed show's wait in Needs you. */
  leftRunning?: string;
}) {
  return (
    <label className="flex items-start gap-2.5 rounded-lg p-3 text-base" style={{ border: `1px solid ${checked ? "var(--reject)" : "var(--line)"}`, background: checked ? "var(--reject-soft)" : "var(--panel-2)", cursor: "pointer" }}>
      <input type="checkbox" checked={checked} disabled={disabled} onChange={(e) => onChange(e.target.checked)} className="mt-0.5" />
      <span className="min-w-0">
        <span className="font-semibold" style={{ color: checked ? "var(--reject-text)" : "var(--ink)" }}>
          Cancel {pending.length === 1 ? "the download" : `${pending.length} downloads`} in progress and delete the partial data
        </span>
        {pending.map((d) => (
          <span key={d.hash || d.title} className="mt-0.5 block break-all font-mono text-xs text-ink-faint">{d.title}{d.progress > 0 ? ` · ${downloadPercent(d.progress)}` : ""}</span>
        ))}
        {!checked && <span className="mt-0.5 block text-xs text-ink-faint">{leftRunning ?? `Left running, ${pending.length === 1 ? "it waits" : "each waits"} in Activity → Needs you when it finishes instead of coming back into the library.`}</span>}
      </span>
    </label>
  );
}
