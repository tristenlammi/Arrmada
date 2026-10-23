import { useState } from "react";
import { ApiError, type DeletePreview } from "../../lib/api";
import { seriesApi } from "../../lib/seriesApi";
import { disposalLine, useRecycleMode } from "../../lib/disposal";
import { ConfirmDialog } from "../../components/ConfirmDialog";
import { shownTitle } from "../../lib/displayTitle";
import { seasonName } from "./util";

const plural = (n: number, one: string, many = one + "s") => `${n} ${n === 1 ? one : many}`;

// DeleteSeasonsDialog is the one way to remove several seasons at once from the show's
// mass edit (GH-80). Their rows always go; deleting the episode files too is an explicit
// tick that first counts what would move and where — the recycle bin or, with it off,
// for good. One dialog covers the whole selection, and a very large one needs the show's
// title typed, exactly like removing the show itself.
export function DeleteSeasonsDialog({ series, seasons, onClose, onDeleted }: {
  series: { id: number; title: string; display_title?: string };
  seasons: number[];
  onClose: () => void;
  onDeleted: (deletedSeasons: number[], deletedFiles: number) => void;
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
      seriesApi.seriesDeletePreview(series.id, seasons)
        .then(setPreview)
        .catch((e: Error) => setErr(`Couldn't list the selected seasons' files: ${e.message}`));
    }
  };

  const big = deleteFiles && !!preview && preview.bytes > preview.confirm_over_bytes;
  const binMode = preview?.recycle ?? mode;
  const names = seasons.map(seasonName);

  const confirm = async () => {
    setBusy(true);
    setErr(null);
    try {
      const r = await seriesApi.bulkDeleteSeasons(series.id, seasons, deleteFiles, big ? series.title : undefined);
      onDeleted(r.deleted_seasons, r.deleted_files);
    } catch (e) {
      let msg = (e as Error).message;
      if (e instanceof ApiError && e.status === 409) {
        const moved = Number(e.body?.deleted_files ?? 0);
        const failed = (e.body?.errors as string[] | undefined) ?? [];
        msg += `\nFiles moved before it stopped: ${moved ? plural(moved, "file") : "nothing"}.`;
        if (failed.length) msg += `\nCouldn't move: ${failed.join(", ")}.`;
        msg += "\nThe seasons are still in your library; episodes already moved now show as missing.";
      }
      setErr(msg);
      setBusy(false);
    }
  };

  const filesHint = !deleteFiles
    ? "The seasons and their episodes are removed from the library; any files stay on disk."
    : !preview
      ? "Counting the selected seasons' files…"
      : preview.files === 0
        ? "These seasons have no files on disk."
        : `${plural(preview.files, "episode file")} and ${plural(preview.sidecars, "subtitle")}. ${disposalLine(preview.bytes, binMode)}.`;

  return (
    <ConfirmDialog
      title={<>Delete {plural(seasons.length, "season")}?</>}
      body={
        <p className="m-0">
          {names.join(", ")} {seasons.length === 1 ? "is" : "are"} removed from {shownTitle(series)}.
          Episodes and their rows go with them; files stay on disk unless you tick the box below.
        </p>
      }
      extra={
        <label className="flex items-start gap-2.5 rounded-lg p-3 text-base" style={{ border: `1px solid ${deleteFiles ? "var(--reject)" : "var(--line)"}`, background: deleteFiles ? "var(--reject-soft)" : "var(--panel-2)", cursor: "pointer" }}>
          <input type="checkbox" checked={deleteFiles} disabled={busy} onChange={(e) => toggleFiles(e.target.checked)} className="mt-0.5" />
          <span className="min-w-0">
            <span className="font-semibold" style={{ color: deleteFiles ? "var(--reject-text)" : "var(--ink)" }}>Also delete their files from disk</span>
            <span className="mt-0.5 block break-words text-xs text-ink-faint">{filesHint}</span>
          </span>
        </label>
      }
      typedPhrase={big ? series.title : undefined}
      confirmLabel={deleteFiles ? "Delete seasons + files" : "Delete seasons"}
      busyLabel="Deleting…"
      busy={busy}
      confirmDisabled={deleteFiles && !preview}
      error={err}
      onConfirm={confirm}
      onCancel={onClose}
    />
  );
}
