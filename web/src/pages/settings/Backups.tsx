import { useEffect, useRef, useState } from "react";
import { ConfirmDialog } from "../../components/ConfirmDialog";
import { api, backupDownloadURL, uploadBackup, type BackupFile, type BackupKind, type BackupSchedule, type BackupsState } from "../../lib/api";
import { restartAndWait, startedAt, waitForRestart } from "../../lib/restart";

// The Backups card (Settings → System, admin only): every database copy with why it was
// taken, Back up now, Download, Restore (from the list or an uploaded file), Delete and the
// nightly schedule. It shows file names, sizes and schema versions only — never anything
// from inside a backup.

const KIND: Record<BackupKind, { label: string; tone: string }> = {
  "pre-migrate": { label: "Before update", tone: "var(--accent)" },
  nightly: { label: "Nightly", tone: "var(--good)" },
  manual: { label: "Manual", tone: "var(--ink-dim)" },
  "pre-restore": { label: "Before restore", tone: "var(--avoid)" },
  "pre-delete-user": { label: "Before user delete", tone: "var(--avoid)" },
  "pre-delete-empty-user": { label: "Before user delete", tone: "var(--avoid)" },
  "pre-merge-user": { label: "Before account merge", tone: "var(--avoid)" },
  uploaded: { label: "Uploaded", tone: "var(--accent)" },
};

const btn = "rounded-lg px-3.5 py-2 text-[12.5px] font-semibold disabled:opacity-50";
const btnStyle = { border: "1px solid var(--line)", background: "var(--panel-2)", color: "var(--ink)" } as const;
const rowBtn = "flex-none rounded-md px-2.5 py-1 text-[11px] font-semibold disabled:opacity-40";
const inputStyle = { background: "var(--panel-2)", border: "1px solid var(--line)", color: "var(--ink)" } as const;

function fmtBytes(b: number): string {
  if (!b || b <= 0) return "0 MB";
  const gb = b / 1024 ** 3;
  if (gb >= 1) return `${gb.toFixed(1)} GB`;
  const mb = b / 1024 ** 2;
  return mb >= 10 ? `${mb.toFixed(0)} MB` : `${mb.toFixed(1)} MB`;
}

function ago(iso: string): string {
  const s = Math.max(0, (Date.now() - new Date(iso).getTime()) / 1000);
  if (s < 90) return "just now";
  if (s < 90 * 60) return `${Math.round(s / 60)} min ago`;
  if (s < 36 * 3600) return `${Math.round(s / 3600)} h ago`;
  const d = Math.round(s / 86400);
  return `${d} day${d === 1 ? "" : "s"} ago`;
}

const when = (iso: string) => new Date(iso).toLocaleString(undefined, { day: "numeric", month: "short", year: "numeric", hour: "2-digit", minute: "2-digit" });
const hourLabel = (h: number) => `${String(h).padStart(2, "0")}:00`;
const kindLabel = (k: BackupKind) => (KIND[k]?.label ?? k).toLowerCase();

// The server's upload limit (backup.MaxUploadBytes), checked here to fail fast.
const MAX_UPLOAD = 4 * 1024 ** 3;

// The last restore result this browser has dismissed (per-viewer convenience only).
const SEEN_RESTORE_KEY = "arrmada.backups.seenRestore";

export function Backups() {
  const [state, setState] = useState<BackupsState | null>(null);
  const [loadErr, setLoadErr] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [msg, setMsg] = useState<{ ok: boolean; text: string } | null>(null);
  const [schedule, setSchedule] = useState<BackupSchedule | null>(null);
  const [savingSchedule, setSavingSchedule] = useState(false);
  const [toDelete, setToDelete] = useState<BackupFile | null>(null);
  const [toRestore, setToRestore] = useState<BackupFile | null>(null);
  const [restarting, setRestarting] = useState<"waiting" | "timeout" | null>(null);
  const fileRef = useRef<HTMLInputElement>(null);
  const [progress, setProgress] = useState<number | null>(null);
  const [uploaded, setUploaded] = useState<BackupFile | null>(null);
  const [confirmBusy, setConfirmBusy] = useState(false);
  const [confirmErr, setConfirmErr] = useState<string | null>(null);

  const load = () => api.backups()
    .then((s) => { setState(s); setLoadErr(null); setSchedule((cur) => cur ?? s.settings); })
    .catch((e: Error) => setLoadErr(e.message));
  useEffect(() => { load(); }, []);

  // Restore from file: upload a .db or .db.gz, which the server checks before it joins the
  // list as Uploaded; restoring it is then the normal Restore.
  const upload = async (f: File) => {
    if (f.size > MAX_UPLOAD) { setMsg({ ok: false, text: `${f.name} is ${fmtBytes(f.size)}; the limit is 4 GB.` }); return; }
    setMsg(null); setUploaded(null); setProgress(0);
    try {
      const b = await uploadBackup(f, setProgress);
      setUploaded(b);
      load();
    } catch (e) { setMsg({ ok: false, text: (e as Error).message }); }
    finally { setProgress(null); }
  };

  const backUpNow = async () => {
    setBusy(true); setMsg(null);
    try {
      const b = await api.backupNow();
      setMsg({ ok: true, text: `Saved ${b.name} (${fmtBytes(b.size_bytes)}).` });
      load();
    } catch (e) { setMsg({ ok: false, text: (e as Error).message }); }
    finally { setBusy(false); }
  };

  const saveSchedule = async () => {
    if (!schedule) return;
    setSavingSchedule(true); setMsg(null);
    try {
      const s = await api.saveBackupSchedule(schedule);
      setSchedule(s);
      setMsg({ ok: true, text: "Schedule saved — it applies from the next hourly check." });
      load();
    } catch (e) { setMsg({ ok: false, text: (e as Error).message }); }
    finally { setSavingSchedule(false); }
  };

  const closeConfirm = () => { setToDelete(null); setToRestore(null); setConfirmErr(null); };
  const remove = async (b: BackupFile) => {
    setConfirmBusy(true); setConfirmErr(null);
    try { await api.deleteBackup(b.name); closeConfirm(); load(); }
    catch (e) { setConfirmErr((e as Error).message); }
    finally { setConfirmBusy(false); }
  };

  // Wait for the restarted app, then reload onto it (the restore ran during its start).
  const awaitRestart = async (wait: () => Promise<boolean>) => {
    setRestarting("waiting");
    if (await wait()) window.location.reload();
    else setRestarting("timeout");
  };

  // Restore stages the backup; the server swaps it in at its next start and, inside
  // Docker, restarts itself straight away. Otherwise the card shows how to restart.
  const restore = async (b: BackupFile) => {
    setConfirmBusy(true); setConfirmErr(null);
    try {
      const before = await startedAt();
      const r = await api.restoreBackup(b.name);
      closeConfirm(); setUploaded(null);
      if (r.restarting) await awaitRestart(() => waitForRestart(before));
      else load();
    } catch (e) { setConfirmErr((e as Error).message); }
    finally { setConfirmBusy(false); }
  };

  const restartNow = () => awaitRestart(() => restartAndWait(() => api.restartApp())).catch((e: Error) => { setRestarting(null); setMsg({ ok: false, text: e.message }); });

  const cancelRestore = async () => {
    setBusy(true); setMsg(null);
    try { await api.cancelRestore(); setMsg({ ok: true, text: "Restore cancelled. Nothing was changed." }); load(); }
    catch (e) { setMsg({ ok: false, text: (e as Error).message }); }
    finally { setBusy(false); }
  };

  const [seenRestore, setSeenRestore] = useState<string>(() => { try { return localStorage.getItem(SEEN_RESTORE_KEY) ?? ""; } catch { return ""; } });
  const dismissRestore = (at: string) => {
    setSeenRestore(at);
    try { localStorage.setItem(SEEN_RESTORE_KEY, at); } catch { /* storage blocked */ }
  };
  const last = state?.last_restore && state.last_restore.at !== seenRestore ? state.last_restore : null;
  const pending = state?.pending_restore ?? null;
  const pendingFile = pending ? state?.backups.find((b) => b.name === pending.name) : undefined;

  const dirty = !!schedule && !!state && (schedule.enabled !== state.settings.enabled || schedule.hour !== state.settings.hour || schedule.keep_nightly !== state.settings.keep_nightly);

  return (
    <div id="backups" className="scroll-mt-20 rounded-xl p-5" style={{ background: "var(--panel)", border: "1px solid var(--line)" }}>
      <h2 className="m-0 text-[14px] font-bold">Backups</h2>
      <p className="mb-4 mt-0.5 text-[11.5px] text-ink-faint">
        Copies of Arrmada's database: one every night, one before every update, and any you take yourself. Backups contain your API keys and password hashes. They're stored next to the database (on Unraid: /mnt/user/appdata/arrmada/backups when the data dir is in appdata), so download one now and then to keep a copy off this server.
      </p>

      <div className="flex flex-col gap-4">
        {loadErr && <p className="m-0 text-[12px]" style={{ color: "var(--reject)" }}>{loadErr}</p>}

        {last && (
          <div role="status" className="flex items-start gap-3 rounded-lg p-3 text-[12px]" style={{ color: last.ok ? "var(--good)" : "var(--reject)", background: last.ok ? "var(--good-soft)" : "var(--reject-soft)" }}>
            <div className="min-w-0 flex-1 break-words">
              {last.ok
                ? <>Restored {last.from} on {when(last.at)}. The database it replaced is kept as a “Before restore” backup.</>
                : <>The restore on {when(last.at)} didn't happen: {last.error}. Arrmada started on the database as it was.</>}
            </div>
            <button onClick={() => dismissRestore(last.at)} className="flex-none text-[11px] font-semibold text-ink-dim">Dismiss</button>
          </div>
        )}

        {pending && (
          <div className="flex flex-col gap-2 rounded-lg p-3 text-[12px]" style={{ border: "1px solid var(--avoid)", background: "var(--panel-2)" }}>
            <div>
              <b>Restore staged.</b> {pendingFile ? <>The {kindLabel(pendingFile.kind)} backup from {when(pendingFile.created_at)}</> : <>{pending.name}</>} replaces the database the next time Arrmada starts{pending.requested_by ? ` (asked for by ${pending.requested_by})` : ""}. Anything changed until then will be lost.
            </div>
            {!state?.can_restart && (
              <div className="text-ink-dim">
                Restart the Arrmada container to run it, for example <code className="select-all rounded px-1.5 py-0.5 font-mono text-[11px]" style={{ background: "var(--panel)" }}>docker restart Arrmada-app</code>.
              </div>
            )}
            <div className="flex flex-wrap gap-2">
              {state?.can_restart && <button onClick={restartNow} disabled={busy} className={btn} style={{ background: "var(--reject)", color: "#fff" }}>Restart and restore now</button>}
              <button onClick={cancelRestore} disabled={busy} className={btn} style={btnStyle}>Cancel restore</button>
            </div>
          </div>
        )}

        <div className="flex flex-wrap items-center gap-3">
          <button onClick={backUpNow} disabled={busy || progress !== null} className={btn} style={btnStyle}>{busy ? "Backing up…" : "Back up now"}</button>
          <button onClick={() => fileRef.current?.click()} disabled={busy || progress !== null} title="A .db, or a .db.gz from Download — up to 4 GB" className={btn} style={btnStyle}>Restore from file…</button>
          <input ref={fileRef} type="file" accept=".db,.gz,.sqlite,application/gzip" className="hidden" onChange={(e) => { const f = e.target.files?.[0]; e.target.value = ""; if (f) upload(f); }} />
          {state && (
            <span className="text-[12px] text-ink-dim">
              {state.backups.length} backup{state.backups.length === 1 ? "" : "s"} · <b>{fmtBytes(state.total_bytes)}</b>
              {state.free_bytes !== null && <> · {fmtBytes(state.free_bytes)} free on this disk</>}
            </span>
          )}
        </div>
        {progress !== null && (
          <div className="flex items-center gap-3 text-[11.5px] text-ink-dim">
            <div className="h-1.5 flex-1 overflow-hidden rounded-full" style={{ background: "var(--panel-2)" }} role="progressbar" aria-label="Upload progress" aria-valuemin={0} aria-valuemax={100} aria-valuenow={Math.round(progress * 100)}>
              <div className="h-full rounded-full" style={{ width: `${Math.round(progress * 100)}%`, background: "var(--accent)" }} />
            </div>
            <span className="flex-none font-mono">{progress < 1 ? `${Math.round(progress * 100)}%` : "Checking…"}</span>
          </div>
        )}
        {msg && <p className="m-0 break-all text-[12px]" style={{ color: msg.ok ? "var(--good)" : "var(--reject)" }}>{msg.text}</p>}
        {uploaded && (
          <div className="flex flex-wrap items-center gap-3 rounded-lg p-3 text-[12px]" style={{ border: "1px solid var(--accent-line)", background: "var(--accent-soft)" }}>
            <span className="min-w-0 flex-1">Uploaded and checked: {uploaded.name} ({fmtBytes(uploaded.size_bytes)}). It's in the list as Uploaded.</span>
            <button onClick={() => setToRestore(uploaded)} className={btn} style={btnStyle}>Restore it…</button>
          </div>
        )}

        <div className="rounded-lg" style={{ border: "1px solid var(--line)" }}>
          {state === null ? (
            <div className="p-4 text-center text-[12px] text-ink-dim">{loadErr ? "Couldn't load the backups." : "Loading…"}</div>
          ) : state.backups.length === 0 ? (
            <div className="p-4 text-center text-[12px] text-ink-dim">No backups yet. The first nightly one is taken within the hour.</div>
          ) : (
            <div className="thin-scroll max-h-[380px] overflow-y-auto">
              {state.backups.map((b, i) => {
                const k = KIND[b.kind] ?? { label: b.kind, tone: "var(--ink-dim)" };
                return (
                  <div key={b.name} className="flex flex-wrap items-center gap-x-3 gap-y-1.5 px-3 py-2" style={{ borderTop: i === 0 ? undefined : "1px solid var(--line-soft)" }}>
                    <div className="min-w-0 flex-1">
                      <div className="flex items-center gap-2">
                        <span className="flex-none rounded px-1.5 py-0.5 font-mono text-[9.5px] font-bold uppercase tracking-[0.06em]" style={{ color: k.tone, border: `1px solid ${k.tone}` }}>{k.label}</span>
                        <span className="truncate text-[12.5px] font-medium" title={when(b.created_at)}>{ago(b.created_at)}</span>
                      </div>
                      <div className="truncate font-mono text-[10px] text-ink-faint" title={b.name}>{b.name}{b.schema_version ? ` · schema ${b.schema_version.split("_")[0]}` : ""}</div>
                    </div>
                    <span className="flex-none font-mono text-[10.5px] text-ink-faint">{fmtBytes(b.size_bytes)}</span>
                    <a href={backupDownloadURL(b.name)} download className={rowBtn} style={{ border: "1px solid var(--accent-line)", color: "var(--accent)" }}>Download</a>
                    <button onClick={() => setToRestore(b)} className={rowBtn} style={{ border: "1px solid var(--line)", color: "var(--ink)" }}>Restore</button>
                    <button onClick={() => setToDelete(b)} disabled={pending?.name === b.name} title={pending?.name === b.name ? "Staged to be restored — cancel the restore first" : undefined} className={rowBtn} style={{ border: "1px solid var(--line)", color: "var(--reject)" }}>Delete</button>
                  </div>
                );
              })}
            </div>
          )}
        </div>

        {schedule && (
          <div className="flex flex-col gap-3 rounded-lg p-3" style={{ background: "var(--panel-2)" }}>
            <div className="flex items-center justify-between gap-3">
              <div className="min-w-0">
                <div className="text-[12.5px] font-semibold">Nightly backup</div>
                <div className="text-[10.5px] text-ink-faint">
                  {state?.last_nightly_at ? `Last one ${ago(state.last_nightly_at)}.` : "None taken yet."} Taken once a day after the hour below (server time), or at once after a long downtime.
                </div>
              </div>
              <button
                role="switch"
                aria-checked={schedule.enabled}
                aria-label="Nightly backup"
                onClick={() => setSchedule({ ...schedule, enabled: !schedule.enabled })}
                className="relative inline-flex h-6 w-11 flex-none items-center rounded-full transition-colors"
                style={{ background: schedule.enabled ? "var(--accent)" : "var(--panel)", border: "1px solid var(--line)" }}
              >
                <span className="inline-block h-4 w-4 rounded-full bg-white transition-transform" style={{ transform: schedule.enabled ? "translateX(22px)" : "translateX(3px)" }} />
              </button>
            </div>
            <div className="flex flex-wrap items-end gap-4">
              <label className="flex flex-col gap-1.5">
                <span className="font-mono text-[9.5px] font-bold uppercase tracking-[0.1em] text-ink-faint">From</span>
                <select value={schedule.hour} disabled={!schedule.enabled} onChange={(e) => setSchedule({ ...schedule, hour: Number(e.target.value) })} className="rounded-lg px-3 py-2 font-mono text-[12.5px] disabled:opacity-50" style={inputStyle}>
                  {Array.from({ length: 24 }, (_, h) => <option key={h} value={h}>{hourLabel(h)}</option>)}
                </select>
              </label>
              <label className="flex flex-col gap-1.5">
                <span className="font-mono text-[9.5px] font-bold uppercase tracking-[0.1em] text-ink-faint">Keep</span>
                <input
                  inputMode="numeric"
                  value={schedule.keep_nightly || ""}
                  onChange={(e) => setSchedule({ ...schedule, keep_nightly: Math.min(365, Number(e.target.value.replace(/[^0-9]/g, "")) || 0) })}
                  className="w-24 rounded-lg px-3 py-2 font-mono text-[12.5px]"
                  style={inputStyle}
                />
              </label>
              <button onClick={saveSchedule} disabled={!dirty || savingSchedule || schedule.keep_nightly < 1} className={btn} style={btnStyle}>{savingSchedule ? "Saving…" : "Save schedule"}</button>
            </div>
            <p className="m-0 text-[10.5px] text-ink-faint">Keeps the newest {schedule.keep_nightly || "…"} nightly backups; older ones are removed after the next nightly. Copies taken before updates (newest 5) and manual ones (newest 10) are kept separately.</p>
          </div>
        )}

        {state && <p className="m-0 truncate font-mono text-[10.5px] text-ink-faint" title={state.dir}>{state.dir}</p>}
        <p className="m-0 text-[10.5px] text-ink-faint">Backups sit on the same disk as the database, so they cover a bad update or a mistake — not a failed disk. That's what Download is for. Restore from file takes a .db or a downloaded .db.gz up to 4 GB; behind Cloudflare anything over 100 MB fails, so upload big ones from your home network.</p>
      </div>

      {toDelete && (
        <ConfirmDialog
          title="Delete this backup?"
          body={<>Permanently deletes the {(KIND[toDelete.kind]?.label ?? toDelete.kind).toLowerCase()} backup from {when(toDelete.created_at)} ({fmtBytes(toDelete.size_bytes)}). This can't be undone.</>}
          confirmLabel="Delete backup"
          busyLabel="Deleting…"
          busy={confirmBusy}
          error={confirmErr}
          onConfirm={() => remove(toDelete)}
          onCancel={closeConfirm}
        />
      )}
      {toRestore && (
        <ConfirmDialog
          title="Restore this backup?"
          body={
            <div className="flex flex-col gap-2">
              <span>Puts back the {kindLabel(toRestore.kind)} backup from <b>{when(toRestore.created_at)}</b>.</span>
              <span>Everything since {when(toRestore.created_at)} will be lost: requests, watch history, listening places, users and settings. Downloads grabbed since then keep going in qBittorrent but Arrmada won't know about them. You may need to sign in again. The current database is kept as Before restore.</span>
              <span>{state?.can_restart ? "Arrmada restarts to do this; it takes a few seconds." : "It runs the next time Arrmada starts; you'll restart the container yourself."}</span>
            </div>
          }
          confirmLabel="Restore"
          busyLabel="Checking the backup…"
          typedPhrase="RESTORE"
          busy={confirmBusy}
          error={confirmErr}
          onConfirm={() => restore(toRestore)}
          onCancel={closeConfirm}
        />
      )}
      {restarting && (
        <div role="alertdialog" aria-modal="true" aria-label="Restarting" className="fixed inset-0 z-50 grid place-items-center p-4" style={{ background: "var(--bg)" }}>
          <div className="max-w-[420px] rounded-2xl p-6 text-center" style={{ background: "var(--panel)", border: "1px solid var(--line)", boxShadow: "var(--shadow)" }}>
            {restarting === "waiting" ? (
              <>
                <h2 className="m-0 text-[15px] font-bold">Restarting…</h2>
                <p className="mb-0 mt-2 text-[12px] text-ink-dim">Arrmada is restoring the backup and starting again. This page reloads when it's back.</p>
              </>
            ) : (
              <>
                <h2 className="m-0 text-[15px] font-bold">Still not back</h2>
                <p className="mt-2 text-[12px] text-ink-dim">Arrmada hasn't answered for two minutes. Check the container is running and look at its log; the card shows how the restore went once it's up.</p>
                <button onClick={() => window.location.reload()} className={btn} style={btnStyle}>Reload</button>
              </>
            )}
          </div>
        </div>
      )}
    </div>
  );
}
