import { useState, type ReactNode } from "react";
import type { ActivityDownload } from "../../lib/api";
import { queueApi, MAX_BULK_REMOVE, type BulkRemoveMode, type BulkRemoveResult } from "../../lib/queueApi";
import { Button, ConfirmDialog, type ConfirmChoice } from "../../ui";

// The Queue's select mode: tick several downloads (or every one the current view and
// filters show) and remove them together, the way the series page picks episodes.

/** The download's title for a checkbox label: what it's for, else its release name. */
const nameOf = (it: ActivityDownload) => it.item?.title ?? it.name;

/**
 * Selectable lays a checkbox over a transfer card while select mode is on: the whole card
 * toggles it (a big target on a phone), and the card's own buttons are covered so a tap
 * can't pause or delete one by mistake. A picked card is ringed in the accent.
 */
export function Selectable({ it, selecting, selected, onToggle, children }: { it: ActivityDownload; selecting: boolean; selected: boolean; onToggle: () => void; children: ReactNode }) {
  if (!selecting) return <>{children}</>;
  return (
    <div className="relative" data-testid="selectable-transfer">
      {children}
      <label className="absolute inset-0 z-[1] cursor-pointer rounded-xl" style={{ boxShadow: selected ? "inset 0 0 0 2px var(--accent)" : undefined }}>
        <input type="checkbox" checked={selected} onChange={onToggle} className="absolute left-2 top-2 h-5 w-5 accent-[var(--accent)]" aria-label={`Select ${nameOf(it)}`} />
      </label>
    </div>
  );
}

const plural = (n: number) => `${n} download${n === 1 ? "" : "s"}`;

/** The toast after a bulk removal: how many went and what happened to their files. */
export function bulkRemovedMessage(r: BulkRemoveResult): string {
  const files = r.mode === "delete_files" ? "their files deleted" : "the files are kept";
  return `Removed ${plural(r.removed)} — ${files}.`;
}

/**
 * QueueBulkBar is select mode's action bar, held at the bottom of the screen while the list
 * scrolls: how many are picked, All (every download the view and filters show), Remove, and
 * Clear. Remove asks through RemoveManyDialog.
 */
export function QueueBulkBar({ shown, picked, setPicked, onRemoved, onFailed }: {
  shown: ActivityDownload[];
  picked: ActivityDownload[];
  setPicked: (hashes: Set<string>) => void;
  onRemoved: (r: BulkRemoveResult) => void;
  onFailed: (m: string) => void;
}) {
  const [asking, setAsking] = useState(false);
  const n = picked.length;
  const tooMany = n > MAX_BULK_REMOVE;
  return (
    <>
      <div
        role="region"
        aria-label="Selected downloads"
        className="sticky z-20 mt-3 flex flex-wrap items-center gap-2 rounded-xl px-3 py-2.5"
        style={{ bottom: "calc(var(--bottom-chrome) + 12px)", background: "var(--panel)", border: "1px solid var(--accent-line)", boxShadow: "var(--shadow)" }}
      >
        <span className="mr-auto text-base font-semibold" aria-live="polite">{n} selected</span>
        {n < shown.length && <Button size="sm" variant="ghost" onClick={() => setPicked(new Set(shown.map((d) => d.hash)))} title="Every download this view and its filters show">All</Button>}
        <Button
          size="sm"
          variant="danger"
          onClick={() => setAsking(true)}
          disabled={n === 0 || tooMany}
          title={tooMany ? `Pick at most ${MAX_BULK_REMOVE} downloads to remove at once` : "Remove these from the download client — asks what to do with the files"}
        >
          Remove
        </Button>
        <Button size="sm" variant="ghost" onClick={() => setPicked(new Set())} disabled={n === 0}>Clear</Button>
      </div>
      {asking && (
        <RemoveManyDialog
          items={picked}
          onClose={() => setAsking(false)}
          onDone={(r) => {
            setAsking(false);
            // What the client refused stays picked, so it can be tried again or looked at.
            setPicked(new Set(r.failed.map((f) => f.hash)));
            onRemoved(r);
            if (r.failed.length > 0) onFailed(`Couldn't remove ${plural(r.failed.length)}: ${r.failed[0].error}`);
          }}
        />
      )}
    </>
  );
}

const CONFIRM_LABEL: Record<BulkRemoveMode, (n: number) => string> = {
  keep_files: (n) => `Remove ${n}, keep files`,
  delete_files: (n) => `Remove ${n} and delete files`,
};

/**
 * RemoveManyDialog asks what removing several downloads does with their files, as the single
 * Delete's dialog does, with the same default: keep them. Block and "stop wanting" are left
 * to the single Delete: one starts a search per download, the other is worded per download.
 */
export function RemoveManyDialog({ items, onClose, onDone }: { items: ActivityDownload[]; onClose: () => void; onDone: (r: BulkRemoveResult) => void }) {
  const [mode, setMode] = useState<BulkRemoveMode>("keep_files");
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  const n = items.length;
  const neverImported = items.filter((it) => it.progress >= 1 && !it.imported).length;
  const choices: ConfirmChoice<BulkRemoveMode>[] = [
    { value: "keep_files", label: "Remove from the client, keep the files", hint: "They stop; everything they downloaded stays where it is." },
    { value: "delete_files", label: `Remove and delete the files of all ${n}`, hint: "Deletes everything these downloads fetched. Files already imported into the library are separate and stay.", danger: true },
  ];
  const confirm = async () => {
    setBusy(true); setErr(null);
    try {
      const r = await queueApi.removeDownloads(items.map((it) => ({ hash: it.hash, name: it.name })), mode);
      // Nothing went: say why here, with the dialog still open, rather than in a toast.
      if (r.removed === 0 && r.failed.length > 0) { setErr(r.failed[0].error); setBusy(false); return; }
      onDone(r);
    } catch (e) { setErr((e as Error).message); setBusy(false); }
  };
  const named = items.slice(0, 3);
  return (
    <ConfirmDialog
      title={`Remove ${plural(n)}?`}
      body={
        <>
          {named.map((it) => <span key={it.hash} className="block truncate font-mono text-xs" title={it.name}>{it.name}</span>)}
          {n > named.length && <span className="block text-xs">and {n - named.length} more</span>}
        </>
      }
      choices={choices}
      choice={mode}
      onChoice={setMode}
      extra={
        <>
          {mode === "delete_files" && (
            <div className="rounded-lg p-2.5 text-sm" style={{ color: "var(--reject-text)", background: "var(--reject-soft)", fontWeight: 600 }} data-testid="bulk-delete-warning">
              The data of {plural(n)} will be deleted. This can't be undone.
            </div>
          )}
          {neverImported > 0 && (
            <div className="rounded-lg p-2.5 text-sm" style={mode === "keep_files" ? { color: "var(--ink-dim)", background: "var(--panel-2)" } : { color: "var(--reject-text)", background: "var(--reject-soft)", fontWeight: 600 }}>
              {neverImported === 1 ? "One of these was" : `${neverImported} of these were`} never imported — deleting the files destroys the only copy.
            </div>
          )}
        </>
      }
      confirmLabel={CONFIRM_LABEL[mode](n)}
      tone={mode === "keep_files" ? "accent" : "danger"}
      busyLabel="Removing…"
      busy={busy}
      error={err}
      onConfirm={confirm}
      onCancel={onClose}
    />
  );
}
