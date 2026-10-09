import { useEffect, useState } from "react";
import { Link } from "react-router-dom";
import { ConfirmDialog } from "../../components/ConfirmDialog";
import { Field, Note, Section, Toggle, input, inputStyle } from "../../components/settings/ui";
import { api, type AppSettings, type DiskGuardStatus, type RecycleBinStats, type RecycleItem, type RecycleStats } from "../../lib/api";
import { LINKS } from "../../lib/links";
import { SaveBar, useLoadedSettings } from "../../lib/useSettings";

// Settings → Downloads (admin only): the download clients and indexers live on their own
// pages; here are the guard rails around them — the disk guard, the stall timeout and the
// recycle bin that deletes and replacements go through.
export function DownloadsSettings() {
  const { s, patch } = useLoadedSettings();
  return (
    <div className="flex flex-col gap-6">
      <Section id="download-links" title="Download clients and indexers" subtitle="Each has its own page: where downloads are sent, and where releases are searched for.">
        <div className="flex flex-wrap gap-x-5 gap-y-2 text-[12px] font-semibold">
          <Link to={LINKS.downloadClients} style={{ color: "var(--accent)" }}>Download clients →</Link>
          <Link to={LINKS.indexers} style={{ color: "var(--accent)" }}>Indexers →</Link>
        </div>
      </Section>
      <DiskGuardSection s={s} patch={patch} />
      <StallSection s={s} patch={patch} />
      <RecycleBin s={s} patch={patch} />
      <SaveBar />
    </div>
  );
}

function fmtBytes(b: number): string {
  if (!b || b <= 0) return "0 MB";
  const tb = b / 1024 ** 4;
  if (tb >= 1) return `${tb.toFixed(2)} TB`;
  const gb = b / 1024 ** 3;
  if (gb >= 1) return `${gb.toFixed(1)} GB`;
  return `${(b / 1024 ** 2).toFixed(0)} MB`;
}
function fmtDay(unix: number): string {
  return new Date(unix * 1000).toLocaleDateString(undefined, { day: "numeric", month: "short", year: "numeric" });
}
function ageOf(unix: number): string {
  const days = Math.floor((Date.now() / 1000 - unix) / 86400);
  return days <= 0 ? "today" : `${days} day${days === 1 ? "" : "s"} ago`;
}

// RecycleBin shows what the bin is holding and lets an admin set the guard rails (max size /
// retention) — saved with the page's Save button — and empty it on demand. Deleted & replaced
// files (movie/episode deletes, Convert originals) land here instead of being erased.
function RecycleBin({ s, patch }: { s: AppSettings; patch: (p: Partial<AppSettings>) => void }) {
  const [stats, setStats] = useState<RecycleStats | null>(null);
  const [items, setItems] = useState<RecycleItem[] | null>(null);
  const [showItems, setShowItems] = useState(false);
  const [busy, setBusy] = useState(false);
  const [rowBusy, setRowBusy] = useState<string | null>(null);
  const [msg, setMsg] = useState<string | null>(null);

  const load = () => api.recycleStats().then(setStats).catch(() => setStats(null));
  const loadItems = () => api.recycleItems().then(setItems).catch(() => setItems([]));
  useEffect(() => { load(); }, []);
  useEffect(() => { if (showItems) loadItems(); }, [showItems]);

  // Both "for good" actions ask through the shared dialog; a refusal shows inside it.
  // An empty with a bin empties just that one.
  const [confirm, setConfirm] = useState<{ kind: "empty"; bin?: RecycleBinStats } | { kind: "item"; it: RecycleItem } | null>(null);
  const [confirmErr, setConfirmErr] = useState<string | null>(null);
  const closeConfirm = () => { setConfirm(null); setConfirmErr(null); };

  const empty = async (bin?: RecycleBinStats) => {
    setBusy(true); setMsg(null); setConfirmErr(null);
    try {
      const r = await api.emptyRecycle(bin?.key);
      setMsg(`Freed ${fmtBytes(r.freed_bytes)}.`);
      closeConfirm();
      load(); if (showItems) loadItems();
    } catch (e) { setConfirmErr((e as Error).message); }
    finally { setBusy(false); }
  };

  const restore = async (it: RecycleItem) => {
    setRowBusy(it.id); setMsg(null);
    try { await api.restoreRecycle(it.id); setMsg(`Restored ${it.name}.`); load(); loadItems(); }
    catch (e) { setMsg((e as Error).message); }
    finally { setRowBusy(null); }
  };
  const deleteItem = async (it: RecycleItem) => {
    setRowBusy(it.id); setMsg(null); setConfirmErr(null);
    try { await api.deleteRecycleItem(it.id); closeConfirm(); load(); loadItems(); }
    catch (e) { setConfirmErr((e as Error).message); }
    finally { setRowBusy(null); }
  };

  const digits = (v: string) => v.replace(/[^0-9]/g, "");
  const bins = stats?.bins ?? [];
  const emptying = confirm?.kind === "empty" ? confirm : null;

  return (
    <Section id="recycle-bin" title="Recycle bin" subtitle="Deleted & replaced files (movie/episode deletes and Convert originals) are moved into a hidden .arrmada-recycle folder inside the library they came from — a quick move on the same drive, hidden from Plex — instead of being erased, so a mistake can be undone until the guard rails below purge it. The size cap counts every bin together, and the oldest files go first once they're over it. Convert only starts a file whose original fits under the cap. To restore a converted film, delete the converted file first: the bin won't restore over it.">
      {stats && !stats.enabled ? (
        <p className="text-[12px] text-ink-dim">Recycling is turned off (<code>ARRMADA_RECYCLE_DIR=off</code>) — deleted files are erased immediately, and Convert deletes each original once its conversion is verified, with no undo.</p>
      ) : (
        <>
          <div className="flex flex-wrap items-baseline gap-x-5 gap-y-1 text-[12.5px]">
            <span>Holding <b>{stats ? fmtBytes(stats.bytes) : "…"}</b>{stats ? ` · ${stats.files} file${stats.files === 1 ? "" : "s"}` : ""}</span>
            {stats?.oldest_unix ? <span className="text-ink-faint">oldest {ageOf(stats.oldest_unix)}</span> : null}
          </div>
          {bins.length > 0 && (
            <div className="rounded-lg" style={{ border: "1px solid var(--line)" }}>
              {bins.map((b, i) => (
                <div key={b.key} className="flex flex-wrap items-center gap-x-3 gap-y-1 px-3 py-2" style={i > 0 ? { borderTop: "1px solid var(--line-soft)" } : undefined}>
                  <div className="min-w-0 flex-1">
                    <div className="flex flex-wrap items-center gap-2 text-[12.5px] font-medium">
                      {b.label}
                      {b.legacy && <span className="rounded px-1.5 py-0.5 text-[10px] font-semibold" style={{ border: "1px solid var(--avoid)", color: "var(--avoid)" }}>Legacy bin</span>}
                    </div>
                    <div className="truncate font-mono text-[10.5px] text-ink-faint" title={b.dir}>{b.dir}</div>
                    {b.legacy && <div className="text-[10.5px] text-ink-faint">Where deletes used to go. Only files outside every library folder land here now — empty it once you've checked it.</div>}
                    {b.other_drive && <div className="text-[10.5px]" style={{ color: "var(--avoid)" }}>On a different drive from your library — every delete into it is a full copy.</div>}
                  </div>
                  <span className="flex-none font-mono text-[10.5px] text-ink-faint">{fmtBytes(b.bytes)} · {b.files} file{b.files === 1 ? "" : "s"}{b.free_known ? ` · ${fmtBytes(b.free_bytes)} free` : ""}</span>
                  {bins.length > 1 && (
                    <button onClick={() => setConfirm({ kind: "empty", bin: b })} disabled={busy || b.files === 0} className="flex-none rounded-md px-2.5 py-1 text-[11px] font-semibold disabled:opacity-40" style={{ border: "1px solid var(--line)", color: "var(--reject)" }}>Empty this bin</button>
                  )}
                </div>
              ))}
            </div>
          )}
          {stats && stats.over_cap_bytes > 0 && (
            <p className="m-0 text-[11.5px]" style={{ color: "var(--avoid)" }}>
              Over the cap by {fmtBytes(stats.over_cap_bytes)} — {stats.protected_bytes > 0 && stats.over_cap_bytes <= stats.protected_bytes
                ? "items from the last 3 days are kept until they're older."
                : "the next hourly clean-up will trim it."}
            </p>
          )}
          {stats && stats.max_gb > 0 && stats.largest_item_bytes > stats.max_gb * 1024 ** 3 && (
            <p className="m-0 text-[11.5px]" style={{ color: "var(--avoid)" }}>Your cap ({stats.max_gb} GB) is smaller than the largest file here ({fmtBytes(stats.largest_item_bytes)}).</p>
          )}
          <div className="grid gap-4 sm:grid-cols-2">
            <Field label="Max size (GB)">
              <input inputMode="numeric" value={s.recycle_max_gb} onChange={(e) => patch({ recycle_max_gb: digits(e.target.value) })} placeholder="0" className={input} style={inputStyle} />
              <span className="text-[10.5px] text-ink-faint">0 = unlimited. Over this, the oldest files are purged first — never anything deleted in the last 3 days, or the newest file.</span>
            </Field>
            <Field label="Keep for (days)">
              <input inputMode="numeric" value={s.recycle_retention_days} onChange={(e) => patch({ recycle_retention_days: digits(e.target.value) })} placeholder="0" className={input} style={inputStyle} />
              <span className="text-[10.5px] text-ink-faint">0 = keep forever. Older files are auto-deleted.</span>
            </Field>
          </div>
          <div className="flex flex-wrap items-center gap-3">
            <button onClick={() => setShowItems((v) => !v)} disabled={(stats?.files ?? 0) === 0} className="rounded-lg px-4 py-2 text-[12.5px] font-semibold disabled:opacity-50" style={{ border: "1px solid var(--line)", color: "var(--ink)" }}>{showItems ? "Hide contents" : `Manage contents${stats ? ` (${stats.files})` : ""}`}</button>
            <button onClick={() => setConfirm({ kind: "empty" })} disabled={busy || (stats?.files ?? 0) === 0} className="rounded-lg px-4 py-2 text-[12.5px] font-semibold disabled:opacity-50" style={{ border: "1px solid var(--reject)", color: "var(--reject)" }}>{busy ? "Emptying…" : "Empty now"}</button>
            {msg && <span className="text-[11.5px] text-ink-dim">{msg}</span>}
          </div>

          {showItems && (
            <div className="rounded-lg" style={{ border: "1px solid var(--line)" }}>
              {items === null ? (
                <div className="p-4 text-center text-[12px] text-ink-dim">Loading…</div>
              ) : items.length === 0 ? (
                <div className="p-4 text-center text-[12px] text-ink-dim">The recycle bin is empty.</div>
              ) : (
                <div className="thin-scroll max-h-[340px] overflow-y-auto">
                  {items.map((it) => (
                    <div key={it.id} className="flex items-center gap-3 px-3 py-2" style={{ borderTop: "1px solid var(--line-soft)" }}>
                      <div className="min-w-0 flex-1">
                        <div className="truncate text-[12.5px] font-medium" title={it.name}>{it.name}</div>
                        <div className="truncate font-mono text-[10px] text-ink-faint" title={it.orig_path || "origin not recorded"}>{it.orig_path || "origin not recorded"}</div>
                        {bins.length > 1 && <div className="text-[10px] text-ink-faint">In: {it.bin_label}{it.legacy ? " (legacy bin)" : ""}</div>}
                        {it.expires_at > 0 && <div className="text-[10px] text-ink-faint">Deleted for good on {fmtDay(it.expires_at)}</div>}
                      </div>
                      <span className="flex-none font-mono text-[10.5px] text-ink-faint">{fmtBytes(it.size_bytes)}</span>
                      <span className="hidden flex-none font-mono text-[10.5px] text-ink-faint sm:block">{ageOf(it.deleted_unix)}</span>
                      <button onClick={() => restore(it)} disabled={!it.restorable || rowBusy !== null} title={it.restorable ? "Move back to its original location" : "Original location wasn't recorded for this item"} className="flex-none rounded-md px-2.5 py-1 text-[11px] font-semibold disabled:opacity-40" style={{ border: "1px solid var(--accent-line)", color: "var(--accent)" }}>{rowBusy === it.id ? "…" : "Restore"}</button>
                      <button onClick={() => setConfirm({ kind: "item", it })} disabled={rowBusy !== null} title="Delete permanently" className="flex-none rounded-md px-2.5 py-1 text-[11px] font-semibold disabled:opacity-40" style={{ border: "1px solid var(--line)", color: "var(--reject)" }}>Delete</button>
                    </div>
                  ))}
                </div>
              )}
            </div>
          )}

          <p className="text-[10.5px] text-ink-faint">Guard rails run automatically about once an hour. The size/retention values save with the button below. Restore moves a file back to where it was deleted from (when that location is free).</p>
        </>
      )}
      {emptying && (
        <ConfirmDialog
          title={emptying.bin ? <>Empty the “{emptying.bin.label}” bin?</> : bins.length > 1 ? "Empty every recycle bin?" : "Empty the recycle bin?"}
          body={emptying.bin
            ? <>Permanently deletes <b>{fmtBytes(emptying.bin.bytes)}</b> ({emptying.bin.files} file{emptying.bin.files === 1 ? "" : "s"}) from {emptying.bin.dir}. The other bins are left alone. This can't be undone.</>
            : <>Permanently deletes {stats ? <b>{fmtBytes(stats.bytes)}</b> : "everything"}{stats ? ` (${stats.files} file${stats.files === 1 ? "" : "s"})` : ""}. This can't be undone.</>}
          confirmLabel={emptying.bin ? "Delete this bin's files for good" : "Delete everything for good"}
          busyLabel="Emptying…"
          busy={busy}
          error={confirmErr}
          onConfirm={() => empty(emptying.bin)}
          onCancel={closeConfirm}
        />
      )}
      {confirm?.kind === "item" && (
        <ConfirmDialog
          title={<>Delete “{confirm.it.name}” for good?</>}
          body={<>Permanently deletes {fmtBytes(confirm.it.size_bytes)}. It can't be restored afterwards.</>}
          confirmLabel="Delete for good"
          busyLabel="Deleting…"
          busy={rowBusy !== null}
          error={confirmErr}
          onConfirm={() => deleteItem(confirm.it)}
          onCancel={closeConfirm}
        />
      )}
    </Section>
  );
}

// A full downloads volume errors every torrent at once and, on a shared cache pool,
// takes everything else on that pool down with it. This is on by default for that
// reason — it isn't a preference anyone opts into deliberately after the fact.
function DiskGuardSection({ s, patch }: { s: AppSettings; patch: (p: Partial<AppSettings>) => void }) {
  const digits = (v: string) => v.replace(/[^0-9]/g, "").slice(0, 3);
  const pause = Number(s.downloads_disk_guard_pause_pct);
  const resume = Number(s.downloads_disk_guard_resume_pct);
  // Mirrors the server's rule. Equal or inverted thresholds would pause and resume on
  // alternate passes forever, so the server rejects them — say so before saving.
  const bad = !Number.isNaN(pause) && !Number.isNaN(resume) && resume >= pause;

  // The guard measures the downloads folder (lib_downloads_dir, picked in Settings → Library;
  // ARRMADA_DOWNLOADS_DIR is only the fallback) and nothing else. Whether that path is
  // the torrent drive is not knowable from in here, so show the resolved path and the
  // reading taken from it and let the user confirm it against their own setup.
  const [status, setStatus] = useState<DiskGuardStatus | null>(null);
  useEffect(() => {
    api.diskGuard().then(setStatus).catch(() => setStatus(null));
  }, []);

  return (
    <Section
      id="disk-guard"
      title="Download disk guard"
      subtitle="Pause downloads before the downloads volume fills up. A full disk errors every torrent at once, and on a shared cache pool it takes everything else on that pool with it. Seeding torrents are never paused — they aren't writing anything, and pausing them would put your seed goals at risk."
    >
      <Note tone="warn">
        <b>This only works if your torrents live on their own drive.</b> The guard measures
        one folder: your Downloads folder, set in{" "}
        <Link to={LINKS.libraryFolders} style={{ color: "var(--accent)" }}>Settings → Library</Link>
        {status?.path ? <> (currently <code>{status.path}</code>)</> : null}. If that folder is on
        your main array rather than the torrent or cache drive, the percentage measures the
        array, and it will either never trigger or pause your queue for a reason that has
        nothing to do with downloads — choose the right folder there. A changed folder is
        watched from the next check, a minute at most.
      </Note>

      {status && (
        <div className="rounded-lg p-3" style={{ background: "var(--panel-2)", border: "1px solid var(--line)" }}>
          <div className="text-[10px] uppercase tracking-[0.1em] text-ink-faint">Currently watching</div>
          <div className="mt-0.5 break-all font-mono text-[11.5px]">
            {status.path || "(not set)"}
          </div>
          {status.measurable ? (
            <div className="mt-1 text-[11.5px] text-ink-dim">
              {status.used_pct.toFixed(1)}% full
              {status.holding > 0 && (
                <span style={{ color: "var(--avoid)" }}>
                  {" "}· holding {status.holding} torrent{status.holding === 1 ? "" : "s"} paused
                </span>
              )}
            </div>
          ) : (
            <div className="mt-1 text-[11.5px]" style={{ color: "var(--reject)" }}>
              Arrmada can't read this folder's disk usage, so the guard will do nothing. Check the
              path exists and is mounted into the container.
            </div>
          )}
          {(status.shared_with?.length ?? 0) > 0 && (
            <div className="mt-1.5 text-[11.5px]" style={{ color: "var(--reject)" }}>
              Shares a drive with: {status.shared_with.map((f) => f.label).join(", ")}.
              The guard will be measuring your whole array, not a torrent drive — a threshold like
              85% almost certainly isn't what you want here.
            </div>
          )}
        </div>
      )}

      <Toggle
        label="Pause downloads when the disk gets full"
        hint="Checked every minute. Only torrents Arrmada paused are resumed — anything you paused by hand stays paused."
        checked={s.downloads_disk_guard}
        onChange={(v) => patch({ downloads_disk_guard: v })}
      />
      {s.downloads_disk_guard && (
        <>
          <div className="grid gap-4 sm:grid-cols-2">
            <Field label="Pause at (% full)">
              <input
                inputMode="numeric"
                value={s.downloads_disk_guard_pause_pct}
                onChange={(e) => patch({ downloads_disk_guard_pause_pct: digits(e.target.value) })}
                placeholder="85"
                className={input}
                style={inputStyle}
              />
              <span className="text-[10.5px] text-ink-faint">Active downloads are paused once the volume is this full.</span>
            </Field>
            <Field label="Resume at (% full)">
              <input
                inputMode="numeric"
                value={s.downloads_disk_guard_resume_pct}
                onChange={(e) => patch({ downloads_disk_guard_resume_pct: digits(e.target.value) })}
                placeholder="80"
                className={input}
                style={inputStyle}
              />
              <span className="text-[10.5px] text-ink-faint">They restart once it drops back to this. Must be below the pause point.</span>
            </Field>
          </div>
          {bad && (
            <p className="m-0 text-[11.5px]" style={{ color: "var(--reject)" }}>
              Resume ({resume}%) must be below pause ({pause}%) — otherwise downloads would pause and resume on alternate checks.
            </p>
          )}
        </>
      )}
    </Section>
  );
}

// The global stall timeout, shown in hours and stored in minutes. Profiles left on "Use
// default" follow it. Safe to have on: a stalled download is only removed once another
// release has been grabbed in its place.
function StallSection({ s, patch }: { s: AppSettings; patch: (p: Partial<AppSettings>) => void }) {
  const minutes = s.downloads_stall_minutes ?? 360;
  const hours = Math.round((minutes / 60) * 10) / 10;
  return (
    <Section
      id="stalled-downloads"
      title="Stalled downloads"
      subtitle="When a download makes no progress for this long, Arrmada looks for another release. The stalled one is only removed once a replacement has been grabbed — with nothing else available it's left to keep trying. Time spent paused, queued by the client, rechecking or held by the disk guard doesn't count."
    >
      <Field label="Give up on a download with no progress after (hours)">
        <input
          type="number"
          min={0}
          max={168}
          step={0.5}
          value={hours}
          onChange={(e) => {
            const h = Math.min(168, Math.max(0, Number(e.target.value) || 0));
            patch({ downloads_stall_minutes: Math.round(h * 60) });
          }}
          className={input}
          style={inputStyle}
        />
        <span className="text-[10.5px] text-ink-faint">0 = never. Quality profiles can set their own time, or turn it off.</span>
      </Field>
    </Section>
  );
}
