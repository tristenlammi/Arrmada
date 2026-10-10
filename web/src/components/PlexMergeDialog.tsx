import { useEffect, useState } from "react";
import { plexApi, type PlexMergePreview } from "../lib/plexApi";
import { ConfirmDialog } from "../ui";

const plural = (n: number, one: string, many = one + "s") => `${n} ${n === 1 ? one : many}`;

// mergeLines says exactly what a merge moves, in the words the dialog lists. Audiobook
// data is "yes": never which books.
export function mergeLines(p: PlexMergePreview): string[] {
  const out: string[] = [];
  if (p.requests) out.push(plural(p.requests, "request"));
  if (p.following) out.push(`${plural(p.following, "followed request")}`);
  if (p.notifications) out.push(plural(p.notifications, "inbox message"));
  if (p.push_devices) out.push(plural(p.push_devices, "phone or browser", "phones and browsers") + " getting alerts");
  if (p.quota_usage) out.push(plural(p.quota_usage, "request-limit entry", "request-limit entries"));
  if (p.audiobook_progress) out.push("audiobook progress");
  if (p.audiobook_password) out.push("their audiobook password");
  out.push(`the Plex link${p.plex_username ? ` (${p.plex_username})` : ""}`);
  return out;
}

// PlexMergeDialog (admin) merges a duplicate Plex requester into the account it belongs
// with: a preview of exactly what moves, then one all-or-nothing merge after the server
// copies the database.
export function PlexMergeDialog({ targetId, fromId, onClose, onMerged }: {
  targetId: number;
  fromId: number;
  onClose: () => void;
  onMerged: () => void;
}) {
  const [preview, setPreview] = useState<PlexMergePreview | null>(null);
  const [busy, setBusy] = useState(false);
  const [err, setErr] = useState<string | null>(null);
  useEffect(() => {
    plexApi.mergePreview(targetId, fromId).then(setPreview).catch((e: Error) => setErr(e.message));
  }, [targetId, fromId]);

  const merge = async () => {
    setBusy(true); setErr(null);
    try { await plexApi.merge(targetId, fromId); onMerged(); }
    catch (e) { setErr((e as Error).message); setBusy(false); }
  };

  return (
    <ConfirmDialog
      title={preview ? <>Merge {preview.from} into {preview.to}?</> : "Merge accounts?"}
      body={preview ? (
        <>
          <p className="m-0">Moves to {preview.to}: {mergeLines(preview).join(", ")}. Then {preview.from} is deleted.</p>
          {(preview.signed_in_devices > 0 || preview.listening_apps > 0) && (
            <p className="m-0 mt-2">Devices signed in as {preview.from} are signed out ({plural(preview.signed_in_devices + preview.listening_apps, "device")}); they sign in again as {preview.to}.</p>
          )}
          <p className="m-0 mt-2">Where both accounts have the same thing (an inbox message, a followed request, an audiobook place), {preview.to}'s is kept. A copy of the database is taken first, under Backups.</p>
        </>
      ) : !err ? "Checking what would move…" : null}
      tone="danger"
      confirmLabel="Back up and merge"
      busyLabel="Merging…"
      busy={busy}
      error={err}
      confirmDisabled={!preview}
      onConfirm={merge}
      onCancel={onClose}
    />
  );
}

export default PlexMergeDialog;
