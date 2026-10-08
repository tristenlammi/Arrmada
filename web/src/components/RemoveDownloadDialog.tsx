import { useState } from "react";
import { api, type ActivityDownload, type RemoveDownloadMode, type RemoveDownloadResult } from "../lib/api";
import { ConfirmDialog, type ConfirmChoice } from "./ConfirmDialog";

// seriesScope reads what a TV release covers from its name, so "stop wanting" can say
// exactly what it will switch off. null when it can't tell (the server then unmonitors
// nothing rather than the whole show).
function seriesScope(name: string): string | null {
  const ep = name.match(/S(\d{1,2})E(\d{1,3})/i);
  if (ep) return `S${ep[1].padStart(2, "0")}E${ep[2].padStart(2, "0")}`;
  const season = name.match(/\bS(\d{1,2})(?![0-9E])/i) ?? name.match(/\bSeason[ ._-]?(\d{1,2})\b/i);
  if (season) return `season ${Number(season[1])}`;
  return null;
}

function stopWantingLabel(it: ActivityDownload): { label: string; disabled: boolean } {
  switch (it.media_type) {
    case "series": {
      const scope = seriesScope(it.name);
      return scope
        ? { label: `Also stop wanting ${scope}`, disabled: false }
        : { label: "Also stop wanting these episodes — can't tell which ones this covers", disabled: true };
    }
    case "book": return { label: "Also stop wanting this book", disabled: false };
    case "music": return { label: "Also stop wanting this album", disabled: false };
    default: return { label: "Also stop wanting this movie", disabled: false };
  }
}

const CONFIRM_LABEL: Record<RemoveDownloadMode, string> = {
  keep_files: "Remove, keep files",
  delete_files: "Remove and delete files",
  block: "Remove, block and find another",
};

// RemoveDownloadDialog asks what removing a torrent should do with its files. Keeping them
// is the default — an un-imported download may be the only copy — and blocklisting is only
// offered for downloads Arrmada grabbed for something in the library.
export function RemoveDownloadDialog({ it, onClose, onDone }: { it: ActivityDownload; onClose: () => void; onDone: (r: RemoveDownloadResult) => void }) {
  const [mode, setMode] = useState<RemoveDownloadMode>("keep_files");
  const [unmonitor, setUnmonitor] = useState(false);
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  const linked = !!it.seed_known;
  const neverImported = it.progress >= 1 && !it.imported;
  const stop = stopWantingLabel(it);

  const choices: ConfirmChoice<RemoveDownloadMode>[] = [
    { value: "keep_files", label: "Remove from the client, keep the files", hint: "The torrent stops; everything it downloaded stays where it is." },
    { value: "delete_files", label: "Remove and delete the files", hint: "Deletes what this torrent downloaded. Files already imported into the library are separate and stay.", danger: true },
    ...(linked ? [{ value: "block" as const, label: "Remove, delete, blocklist this release and find another", hint: "For a bad or fake release: it won't be grabbed again and a different one is searched for.", danger: true }] : []),
  ];

  const confirm = async () => {
    setBusy(true); setErr(null);
    try {
      const r = await api.deleteDownload(it.hash, { mode, unmonitor: unmonitor && mode !== "block", name: it.name });
      onDone(r);
    } catch (e) { setErr((e as Error).message); setBusy(false); }
  };

  return (
    <ConfirmDialog
      title="Remove this download?"
      body={<span className="block truncate font-mono text-[11px]" title={it.name}>{it.name}</span>}
      choices={choices}
      choice={mode}
      onChoice={setMode}
      extra={
        <>
          {linked && mode !== "block" && (
            <label className="flex items-start gap-2 text-[12px] text-ink-dim" style={{ opacity: stop.disabled ? 0.6 : 1 }}>
              <input type="checkbox" checked={unmonitor} disabled={stop.disabled || busy} onChange={(e) => setUnmonitor(e.target.checked)} className="mt-0.5" />
              <span>{stop.label}</span>
            </label>
          )}
          {neverImported && (
            <div className="rounded-lg p-2.5 text-[12px]" style={mode === "keep_files" ? { color: "var(--ink-dim)", background: "var(--panel-2)" } : { color: "var(--reject)", background: "var(--reject-soft)", fontWeight: 600 }}>
              This download was never imported — deleting its files destroys the only copy.
            </div>
          )}
        </>
      }
      confirmLabel={CONFIRM_LABEL[mode]}
      tone={mode === "keep_files" ? "accent" : "danger"}
      busyLabel="Removing…"
      busy={busy}
      error={err}
      onConfirm={confirm}
      onCancel={onClose}
    />
  );
}
