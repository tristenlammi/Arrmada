import { useEffect, useState } from "react";
import { ConfirmDialog } from "../../components/ConfirmDialog";
import { api, backupDownloadURL, type BackupFile, type BackupKind, type BackupSchedule, type BackupsState } from "../../lib/api";

// The Backups card (Settings → System, admin only): every database copy with why it was
// taken, Back up now, Download, Delete and the nightly schedule. It shows file names,
// sizes and schema versions only — never anything from inside a backup.

const KIND: Record<BackupKind, { label: string; tone: string }> = {
  "pre-migrate": { label: "Before update", tone: "var(--accent)" },
  nightly: { label: "Nightly", tone: "var(--good)" },
  manual: { label: "Manual", tone: "var(--ink-dim)" },
  "pre-restore": { label: "Before restore", tone: "var(--avoid)" },
  "pre-delete-user": { label: "Before user delete", tone: "var(--avoid)" },
  "pre-delete-empty-user": { label: "Before user delete", tone: "var(--avoid)" },
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

export function Backups() {
  const [state, setState] = useState<BackupsState | null>(null);
  const [loadErr, setLoadErr] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [msg, setMsg] = useState<{ ok: boolean; text: string } | null>(null);
  const [schedule, setSchedule] = useState<BackupSchedule | null>(null);
  const [savingSchedule, setSavingSchedule] = useState(false);
  const [toDelete, setToDelete] = useState<BackupFile | null>(null);
  const [confirmBusy, setConfirmBusy] = useState(false);
  const [confirmErr, setConfirmErr] = useState<string | null>(null);

  const load = () => api.backups()
    .then((s) => { setState(s); setLoadErr(null); setSchedule((cur) => cur ?? s.settings); })
    .catch((e: Error) => setLoadErr(e.message));
  useEffect(() => { load(); }, []);

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

  const closeConfirm = () => { setToDelete(null); setConfirmErr(null); };
  const remove = async (b: BackupFile) => {
    setConfirmBusy(true); setConfirmErr(null);
    try { await api.deleteBackup(b.name); closeConfirm(); load(); }
    catch (e) { setConfirmErr((e as Error).message); }
    finally { setConfirmBusy(false); }
  };

  const dirty = !!schedule && !!state && (schedule.enabled !== state.settings.enabled || schedule.hour !== state.settings.hour || schedule.keep_nightly !== state.settings.keep_nightly);

  return (
    <div className="rounded-xl p-5" style={{ background: "var(--panel)", border: "1px solid var(--line)" }}>
      <h2 className="m-0 text-[14px] font-bold">Backups</h2>
      <p className="mb-4 mt-0.5 text-[11.5px] text-ink-faint">
        Copies of Arrmada's database: one every night, one before every update, and any you take yourself. Backups contain your API keys and password hashes. They're stored next to the database (on Unraid: /mnt/user/appdata/arrmada/backups when the data dir is in appdata), so download one now and then to keep a copy off this server.
      </p>

      <div className="flex flex-col gap-4">
        {loadErr && <p className="m-0 text-[12px]" style={{ color: "var(--reject)" }}>{loadErr}</p>}

        <div className="flex flex-wrap items-center gap-3">
          <button onClick={backUpNow} disabled={busy} className={btn} style={btnStyle}>{busy ? "Backing up…" : "Back up now"}</button>
          {state && (
            <span className="text-[12px] text-ink-dim">
              {state.backups.length} backup{state.backups.length === 1 ? "" : "s"} · <b>{fmtBytes(state.total_bytes)}</b>
              {state.free_bytes !== null && <> · {fmtBytes(state.free_bytes)} free on this disk</>}
            </span>
          )}
        </div>
        {msg && <p className="m-0 break-all text-[12px]" style={{ color: msg.ok ? "var(--good)" : "var(--reject)" }}>{msg.text}</p>}

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
                    <button onClick={() => setToDelete(b)} className={rowBtn} style={{ border: "1px solid var(--line)", color: "var(--reject)" }}>Delete</button>
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
        <p className="m-0 text-[10.5px] text-ink-faint">Backups sit on the same disk as the database, so they cover a bad update or a mistake — not a failed disk. That's what Download is for.</p>
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
    </div>
  );
}
