import { useCallback, useEffect, useState } from "react";
import { api, type PendingRestart } from "../lib/api";
import { isAdmin, useMe } from "../lib/me";
import { busyLines, FOLDERS_SAVED_EVENT, LIBRARY_LABEL, restartAppAndWait } from "../lib/restart";
import { ConfirmDialog } from "./ConfirmDialog";

// RestartBanner sits above every staff page while a saved setting is waiting on a restart.
// Library folders no longer do — imports, qBittorrent's save path and the disk guard read
// them live — so today it stays hidden; it's kept for the next setting that only applies
// at startup. It stays until the restart happens; there's nothing to dismiss.
export function RestartBanner() {
  const { user } = useMe();
  const [st, setSt] = useState<PendingRestart | null>(null);
  const [confirming, setConfirming] = useState(false);
  const [restarting, setRestarting] = useState(false);
  const [err, setErr] = useState<string | null>(null);

  const load = useCallback(() => { api.pendingRestart().then(setSt).catch(() => { /* advisory */ }); }, []);
  useEffect(() => {
    load();
    window.addEventListener(FOLDERS_SAVED_EVENT, load);
    return () => window.removeEventListener(FOLDERS_SAVED_EVENT, load);
  }, [load]);

  if (!st?.restart_needed) return null;
  const admin = isAdmin(user);
  const changes = st.changed.map((c) => (
    <span key={c.library}>
      {LIBRARY_LABEL[c.library] ?? c.library} <span className="font-mono">{c.running}</span> → <span className="font-mono">{c.saved}</span>
    </span>
  ));

  // Fresh numbers for the confirm: the banner may have been sitting there for hours.
  const openConfirm = () => { setErr(null); load(); setConfirming(true); };
  const restart = async () => {
    setRestarting(true); setErr(null);
    try { await restartAppAndWait(); } catch (e) { setErr((e as Error).message); setRestarting(false); }
  };
  const lines = busyLines(st.busy);

  return (
    <div className="flex flex-wrap items-center gap-x-4 gap-y-2 px-4 py-2.5 text-[12px]" style={{ background: "var(--panel-2)", borderBottom: "1px solid var(--avoid)", color: "var(--ink)" }} role="status">
      <div className="min-w-0 flex-1">
        <span className="font-semibold" style={{ color: "var(--avoid)" }}>New folders are saved but not in use yet: </span>
        {changes.reduce<React.ReactNode[]>((acc, c, i) => (i === 0 ? [c] : [...acc, "; ", c]), [])}.
        <span className="text-ink-dim"> Downloads and imports keep using the old folders until Arrmada restarts. </span>
        {!(admin && st.can_restart) && (
          <span className="text-ink-dim">{admin ? "Restart the Arrmada container the way you started it." : "Ask an admin to restart Arrmada."}</span>
        )}
      </div>
      {admin && st.can_restart && (
        <button onClick={openConfirm} className="flex-none rounded-lg px-3 py-1.5 text-[11.5px] font-semibold" style={{ border: "1px solid var(--avoid)", color: "var(--avoid)" }}>Restart now</button>
      )}
      {confirming && (
        <ConfirmDialog
          title="Restart Arrmada now?"
          tone="accent"
          body={
            <div className="flex flex-col gap-1.5">
              <span>Arrmada restarts and starts using the new folders. Files already imported stay where they are. This page reloads once it's back, usually in a few seconds.</span>
              {lines.length > 0
                ? lines.map((l) => <span key={l} style={{ color: "var(--avoid)" }}>{l}</span>)
                : <span>Nothing is converting or making subtitles right now.</span>}
            </div>
          }
          confirmLabel="Restart now"
          busyLabel="Restarting…"
          busy={restarting}
          error={err}
          onConfirm={restart}
          onCancel={() => setConfirming(false)}
        />
      )}
    </div>
  );
}
