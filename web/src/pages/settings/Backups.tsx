import { useEffect, useRef, useState } from "react";
import { ConfirmDialog } from "../../components/ConfirmDialog";
import { api, backupDownloadURL, uploadBackup, type BackupFile, type BackupKind, type BackupSchedule, type BackupsState } from "../../lib/api";
import { systemApi } from "../../lib/systemApi";
import { restartAndWait, startedAt, waitForRestart } from "../../lib/restart";
import { useAutoSave } from "../../lib/useAutoSave";
import { AutoNumber, AutoSelect, AutoToggle, wholeNumber } from "../../components/settings/auto";
import { Modal } from "../../ui";

const noop = () => {};

// The Backups card (Settings → System, admin only): every database copy with why it was
// taken, Back up now, Download, Restore (from the list or an uploaded file), Delete and the
// nightly schedule. It shows file names, sizes and schema versions only — never anything
// from inside a backup.

const KIND: Record<BackupKind, { label: string; tone: string }> = {
  "pre-migrate": { label: "Before update", tone: "var(--accent-text)" },
  nightly: { label: "Nightly", tone: "var(--good-text)" },
  manual: { label: "Manual", tone: "var(--ink-dim)" },
  "pre-restore": { label: "Before restore", tone: "var(--avoid-text)" },
  "pre-delete-user": { label: "Before user delete", tone: "var(--avoid-text)" },
  "pre-delete-empty-user": { label: "Before user delete", tone: "var(--avoid-text)" },
  "pre-merge-user": { label: "Before account merge", tone: "var(--avoid-text)" },
  uploaded: { label: "Uploaded", tone: "var(--accent-text)" },
  "pre-insights-repair": { label: "Before Insights cleanup", tone: "var(--avoid-text)" },
};

const btn = "rounded-lg px-3.5 py-2 text-base font-semibold disabled:opacity-50";
const btnStyle = { border: "1px solid var(--line)", background: "var(--panel-2)", color: "var(--ink)" } as const;
const rowBtn = "flex-none rounded-md px-2.5 py-1 text-xs font-semibold disabled:opacity-40";

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
  const [toDelete, setToDelete] = useState<BackupFile | null>(null);
  const [toRestore, setToRestore] = useState<BackupFile | null>(null);
  const [restarting, setRestarting] = useState<"waiting" | "timeout" | null>(null);
  const fileRef = useRef<HTMLInputElement>(null);
  const [progress, setProgress] = useState<number | null>(null);
  const [uploaded, setUploaded] = useState<BackupFile | null>(null);
  const [confirmBusy, setConfirmBusy] = useState(false);
  const [confirmErr, setConfirmErr] = useState<string | null>(null);
  const scheduleChain = useRef<Promise<unknown>>(Promise.resolve());
  const storedSchedule = useRef<BackupSchedule | null>(null);

  const load = () => systemApi.backups()
    .then((s) => { storedSchedule.current = s.settings; setState(s); setLoadErr(null); })
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
      const b = await systemApi.backupNow();
      setMsg({ ok: true, text: `Saved ${b.name} (${fmtBytes(b.size_bytes)}).` });
      load();
    } catch (e) { setMsg({ ok: false, text: (e as Error).message }); }
    finally { setBusy(false); }
  };

  const closeConfirm = () => { setToDelete(null); setToRestore(null); setConfirmErr(null); };
  const remove = async (b: BackupFile) => {
    setConfirmBusy(true); setConfirmErr(null);
    try { await systemApi.deleteBackup(b.name); closeConfirm(); load(); }
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
      const r = await systemApi.restoreBackup(b.name);
      closeConfirm(); setUploaded(null);
      if (r.restarting) await awaitRestart(() => waitForRestart(before));
      else load();
    } catch (e) { setConfirmErr((e as Error).message); }
    finally { setConfirmBusy(false); }
  };

  const restartNow = () => awaitRestart(() => restartAndWait(() => api.restartApp())).catch((e: Error) => { setRestarting(null); setMsg({ ok: false, text: e.message }); });

  const cancelRestore = async () => {
    setBusy(true); setMsg(null);
    try { await systemApi.cancelRestore(); setMsg({ ok: true, text: "Restore cancelled. Nothing was changed." }); load(); }
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

  // Each part of the schedule saves itself; the endpoint takes the whole schedule, so the
  // other two parts go along as they're stored. Saves queue up and each starts from the
  // last stored schedule: leaving Keep by clicking the switch sends two saves at once, and
  // the second mustn't undo the first.
  const saveSchedule = (p: Partial<BackupSchedule>) => {
    const run = scheduleChain.current.then(async () => {
      const base = storedSchedule.current ?? state?.settings;
      if (!base) return;
      const next = await systemApi.saveBackupSchedule({ ...base, ...p });
      storedSchedule.current = next;
      setState((cur) => (cur ? { ...cur, settings: next } : cur));
    });
    scheduleChain.current = run.catch(() => {});
    return run;
  };

  return (
    <div id="backups" className="scroll-mt-20 rounded-xl p-5" style={{ background: "var(--panel)", border: "1px solid var(--line)" }}>
      <h2 className="m-0 text-md font-bold">Backups</h2>
      <p className="mb-4 mt-0.5 text-sm text-ink-faint">
        Copies of Arrmada's database: one every night, one before every update, and any you take yourself. Backups contain your API keys and password hashes. They're stored in Arrmada's data directory (on Unraid: /mnt/user/appdata/arrmada/backups when the data dir is in appdata), so download one now and then to keep a copy off this server.
      </p>

      <div className="flex flex-col gap-4">
        {loadErr && <p className="m-0 text-sm" style={{ color: "var(--reject-text)" }}>{loadErr}</p>}

        {last && (
          <div role="status" className="flex items-start gap-3 rounded-lg p-3 text-sm" style={{ color: last.ok ? "var(--good-text)" : "var(--reject-text)", background: last.ok ? "var(--good-soft)" : "var(--reject-soft)" }}>
            <div className="min-w-0 flex-1 break-words">
              {last.ok
                ? <>Restored {last.from} on {when(last.at)}. The database it replaced is kept as a “Before restore” backup.</>
                : <>The restore on {when(last.at)} didn't happen: {last.error}. Arrmada started on the database as it was.</>}
            </div>
            <button onClick={() => dismissRestore(last.at)} className="flex-none text-xs font-semibold text-ink-dim">Dismiss</button>
          </div>
        )}

        {pending && (
          <div className="flex flex-col gap-2 rounded-lg p-3 text-sm" style={{ border: "1px solid var(--avoid)", background: "var(--panel-2)" }}>
            <div>
              <b>Restore staged.</b> {pendingFile ? <>The {kindLabel(pendingFile.kind)} backup from {when(pendingFile.created_at)}</> : <>{pending.name}</>} replaces the database the next time Arrmada starts{pending.requested_by ? ` (asked for by ${pending.requested_by})` : ""}. Anything changed until then will be lost.
            </div>
            {!state?.can_restart && (
              <div className="text-ink-dim">
                Restart the Arrmada container to run it, for example <code className="select-all rounded px-1.5 py-0.5 font-mono text-xs" style={{ background: "var(--panel)" }}>docker restart Arrmada-app</code>.
              </div>
            )}
            <div className="flex flex-wrap gap-2">
              {state?.can_restart && <button onClick={restartNow} disabled={busy} className={btn} style={{ background: "var(--reject)", color: "var(--accent-ink)" }}>Restart and restore now</button>}
              <button onClick={cancelRestore} disabled={busy} className={btn} style={btnStyle}>Cancel restore</button>
            </div>
          </div>
        )}

        <div className="flex flex-wrap items-center gap-3">
          <button onClick={backUpNow} disabled={busy || progress !== null} className={btn} style={btnStyle}>{busy ? "Backing up…" : "Back up now"}</button>
          {state && state.can_upload !== false && <button onClick={() => fileRef.current?.click()} disabled={busy || progress !== null} title="A .db, or a .db.gz from Download — up to 4 GB" className={btn} style={btnStyle}>Restore from file…</button>}
          <input ref={fileRef} type="file" accept=".db,.gz,.sqlite,application/gzip" className="hidden" onChange={(e) => { const f = e.target.files?.[0]; e.target.value = ""; if (f) upload(f); }} />
          {state && (
            <span className="text-sm text-ink-dim">
              {state.backups.length} backup{state.backups.length === 1 ? "" : "s"} · <b>{fmtBytes(state.total_bytes)}</b>
              {state.free_bytes !== null && <> · {fmtBytes(state.free_bytes)} free on this disk</>}
            </span>
          )}
        </div>
        {progress !== null && (
          <div className="flex items-center gap-3 text-sm text-ink-dim">
            <div className="h-1.5 flex-1 overflow-hidden rounded-full" style={{ background: "var(--panel-2)" }} role="progressbar" aria-label="Upload progress" aria-valuemin={0} aria-valuemax={100} aria-valuenow={Math.round(progress * 100)}>
              <div className="h-full rounded-full" style={{ width: `${Math.round(progress * 100)}%`, background: "var(--accent)" }} />
            </div>
            <span className="flex-none font-mono">{progress < 1 ? `${Math.round(progress * 100)}%` : "Checking…"}</span>
          </div>
        )}
        {msg && <p className="m-0 break-all text-sm" style={{ color: msg.ok ? "var(--good-text)" : "var(--reject-text)" }}>{msg.text}</p>}
        {uploaded && (
          <div className="flex flex-wrap items-center gap-3 rounded-lg p-3 text-sm" style={{ border: "1px solid var(--accent-line)", background: "var(--accent-soft)" }}>
            <span className="min-w-0 flex-1">Uploaded and checked: {uploaded.name} ({fmtBytes(uploaded.size_bytes)}). It's in the list as Uploaded.</span>
            <button onClick={() => setToRestore(uploaded)} className={btn} style={btnStyle}>Restore it…</button>
          </div>
        )}

        <div className="rounded-lg" style={{ border: "1px solid var(--line)" }}>
          {state === null ? (
            <div className="p-4 text-center text-sm text-ink-dim">{loadErr ? "Couldn't load the backups." : "Loading…"}</div>
          ) : state.backups.length === 0 ? (
            <div className="p-4 text-center text-sm text-ink-dim">No backups yet. The first nightly one is taken within the hour.</div>
          ) : (
            <div className="thin-scroll max-h-[380px] overflow-y-auto">
              {state.backups.map((b, i) => {
                const k = KIND[b.kind] ?? { label: b.kind, tone: "var(--ink-dim)" };
                return (
                  <div key={b.name} className="flex flex-wrap items-center gap-x-3 gap-y-1.5 px-3 py-2" style={{ borderTop: i === 0 ? undefined : "1px solid var(--line-soft)" }}>
                    <div className="min-w-0 flex-1">
                      <div className="flex items-center gap-2">
                        <span className="flex-none rounded px-1.5 py-0.5 font-mono text-2xs font-bold uppercase tracking-[0.06em]" style={{ color: k.tone, border: `1px solid ${k.tone}` }}>{k.label}</span>
                        <span className="truncate text-base font-medium" title={when(b.created_at)}>{ago(b.created_at)}</span>
                      </div>
                      <div className="truncate font-mono text-xs text-ink-faint" title={b.name}>{b.name}{b.schema_version ? ` · schema ${b.schema_version.split("_")[0]}` : ""}</div>
                    </div>
                    <span className="flex-none font-mono text-xs text-ink-faint">{fmtBytes(b.size_bytes)}</span>
                    <a href={backupDownloadURL(b.name)} download className={rowBtn} style={{ border: "1px solid var(--accent-line)", color: "var(--accent-text)" }}>Download</a>
                    {state.can_restore !== false && b.backend !== "postgres" && <button onClick={() => setToRestore(b)} className={rowBtn} style={{ border: "1px solid var(--line)", color: "var(--ink)" }}>Restore</button>}
                    <button onClick={() => setToDelete(b)} disabled={pending?.name === b.name} title={pending?.name === b.name ? "Staged to be restored — cancel the restore first" : undefined} className={rowBtn} style={{ border: "1px solid var(--line)", color: "var(--reject-text)" }}>Delete</button>
                  </div>
                );
              })}
            </div>
          )}
        </div>

        {state && <NightlySchedule schedule={state.settings} lastNightly={state.last_nightly_at} save={saveSchedule} />}

        {state && <p className="m-0 truncate font-mono text-xs text-ink-faint" title={state.dir}>{state.dir}</p>}
        <p className="m-0 text-xs text-ink-faint">{state?.backend === "postgres"
          ? <>PostgreSQL native archives live on this Arrmada server. Download a copy off-server. Recovery is offline only: stop Arrmada, provision an empty schema and use <code>arrmada restore-postgres --schema arrmada_recovered backup.pgdump</code> with the connection URL supplied securely through the environment. Media and other filesystem data need separate backups.</>
          : <>Backups sit on the same disk as the database, so they cover a bad update or a mistake — not a failed disk. That's what Download is for. Restore from file takes a .db or a downloaded .db.gz up to 4 GB; behind Cloudflare anything over 100 MB fails, so upload big ones from your home network.</>}</p>
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
        // Covers the page while the app is away: nothing behind it works until it's back.
        <Modal onClose={noop} dismissible={false} role="alertdialog" ariaLabel="Restarting" backdrop="var(--bg)"
          panelClassName="max-w-[420px] rounded-2xl p-6 text-center" panelStyle={{ boxShadow: "var(--shadow)" }}>
          {restarting === "waiting" ? (
            <>
              <h2 className="m-0 text-lg font-bold">Restarting…</h2>
              <p className="mb-0 mt-2 text-sm text-ink-dim">Arrmada is restoring the backup and starting again. This page reloads when it's back.</p>
            </>
          ) : (
            <>
              <h2 className="m-0 text-lg font-bold">Still not back</h2>
              <p className="mt-2 text-sm text-ink-dim">Arrmada hasn't answered for two minutes. Check the container is running and look at its log; the card shows how the restore went once it's up.</p>
              <button onClick={() => window.location.reload()} className={btn} style={btnStyle}>Reload</button>
            </>
          )}
        </Modal>
      )}
    </div>
  );
}

// NightlySchedule is the nightly backup's switch, hour and how many to keep. The switch and
// hour save when changed, the count when its field is left; it applies from the next
// hourly check.
function NightlySchedule({ schedule, lastNightly, save }: { schedule: BackupSchedule; lastNightly?: string | null; save: (p: Partial<BackupSchedule>) => Promise<void> }) {
  const enabled = useAutoSave(schedule.enabled, (v) => save({ enabled: v }));
  const hour = useAutoSave(String(schedule.hour), (v) => save({ hour: Number(v) }), { keepOnError: false });
  const keep = useAutoSave(String(schedule.keep_nightly), (v) => save({ keep_nightly: Number(v) }), { validate: wholeNumber(1, 365, "Keep") });
  return (
    <div className="flex flex-col gap-3 rounded-lg p-3" style={{ background: "var(--panel-2)" }}>
      <AutoToggle
        label="Nightly backup"
        hint={`${lastNightly ? `Last one ${ago(lastNightly)}.` : "None taken yet."} Taken once a day after the hour below (server time), or at once after a long downtime.`}
        save={enabled}
      />
      <div className="flex flex-wrap items-start gap-4">
        <AutoSelect label="From" save={hour} options={HOURS} className="font-mono disabled:opacity-50" />
        <AutoNumber label="Keep" save={keep} min={1} max={365} className="w-24 rounded-lg px-3 py-2 font-mono text-base" />
      </div>
      <p className="m-0 text-xs text-ink-faint">Keeps the newest {keep.value || "…"} nightly backups; older ones are removed after the next nightly. Copies taken before updates (newest 5) and manual ones (newest 10) are kept separately.</p>
    </div>
  );
}

const HOURS = Array.from({ length: 24 }, (_, h) => ({ value: String(h), label: hourLabel(h) }));
