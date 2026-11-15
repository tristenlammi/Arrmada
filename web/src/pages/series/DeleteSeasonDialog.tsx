import { useEffect, useState } from "react";
import { ApiError, type SeasonDeletePreview } from "../../lib/api";
import { seriesApi } from "../../lib/seriesApi";
import { disposalLine, useRecycleMode } from "../../lib/disposal";
import { ConfirmDialog } from "../../ui";
import { CancelDownloadsBox } from "../../components/DeleteSeriesDialog";

const plural = (n: number, one: string, many = one + "s") => `${n} ${n === 1 ? one : many}`;

// DeleteSeasonDialog deletes one season's files (#111): the count, the size and where they
// go come first. The season stops being monitored by default, or the next search would
// download it straight back; its downloads still in progress are cancelled by default. A
// file another season shares stays, and the dialog says so.
export function DeleteSeasonDialog({ seriesId, season, name, monitored, onClose, onDone }: {
  seriesId: number;
  season: number;
  name: string;
  monitored: boolean;
  onClose: () => void;
  onDone: (msg: string) => void;
}) {
  const mode = useRecycleMode();
  const [preview, setPreview] = useState<SeasonDeletePreview | null>(null);
  const [unmonitor, setUnmonitor] = useState(true);
  const [cancelDownloads, setCancelDownloads] = useState(true);
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);

  useEffect(() => {
    seriesApi.seasonDeletePreview(seriesId, season).then(setPreview).catch((e: Error) => setErr(`Couldn't list ${name}'s files: ${e.message}`));
  }, [seriesId, season, name]);

  const pending = preview?.pending_downloads ?? [];
  const binMode = preview?.recycle ?? mode;

  const confirm = async () => {
    setBusy(true); setErr(null);
    try {
      await seriesApi.deleteSeasonFiles(seriesId, season, { unmonitor: monitored && unmonitor, cancelDownloads: cancelDownloads && pending.length > 0 });
      const videos = preview?.files ?? 0;
      onDone(`Deleted ${name}'s ${plural(videos, "file")}${monitored && unmonitor ? ` · ${name} is no longer monitored` : ""}.`);
    } catch (e) {
      let msg = (e as Error).message;
      if (e instanceof ApiError && e.status === 409) {
        const moved = (e.body?.moved as string[] | undefined) ?? [];
        const failed = (e.body?.failed as string[] | undefined) ?? [];
        msg += `\nMoved before it stopped: ${moved.length ? plural(moved.length, "file") : "nothing"}.`;
        if (failed.length) msg += `\nCouldn't move: ${failed.join(", ")}.`;
        msg += "\nEpisodes already moved now show as missing.";
      }
      setErr(msg);
      setBusy(false);
    }
  };

  const filesLine = !preview
    ? "Counting the season's files…"
    : preview.files === 0
      ? `${name} has no files on disk.`
      : `${plural(preview.files, "episode file")}, with their subtitles. ${disposalLine(preview.bytes, binMode)}.`;

  return (
    <ConfirmDialog
      title={<>Delete {name}'s files?</>}
      body={
        <div className="flex flex-col gap-1.5">
          <p className="m-0">{filesLine}</p>
          {preview && preview.shared.length > 0 && (
            <p className="m-0 text-sm text-ink-dim">
              Kept, because another season's episodes are in {preview.shared.length === 1 ? "it" : "them"} too: <span className="break-all font-mono text-xs">{preview.shared.join(", ")}</span>
            </p>
          )}
          <p className="m-0 text-sm text-ink-dim">The show and its other seasons stay as they are.</p>
        </div>
      }
      extra={
        <div className="flex flex-col gap-2">
          {monitored && (
            <label className="flex items-start gap-2.5 rounded-lg p-3 text-base" style={{ border: "1px solid var(--line)", background: "var(--panel-2)", cursor: "pointer" }}>
              <input type="checkbox" checked={unmonitor} disabled={busy} onChange={(e) => setUnmonitor(e.target.checked)} className="mt-0.5" />
              <span>
                <span className="font-semibold">Stop monitoring {name}</span>
                <span className="mt-0.5 block text-xs text-ink-faint">
                  {unmonitor ? "It won't be searched for again." : `Left monitored, its episodes are missing again and the next search downloads ${name} again.`}
                </span>
              </span>
            </label>
          )}
          {pending.length > 0 && <CancelDownloadsBox pending={pending} checked={cancelDownloads} disabled={busy} onChange={setCancelDownloads} />}
        </div>
      }
      confirmLabel="Delete files"
      busyLabel="Deleting…"
      busy={busy}
      confirmDisabled={!preview || preview.files === 0}
      error={err}
      onConfirm={confirm}
      onCancel={onClose}
    />
  );
}
