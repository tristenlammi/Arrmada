import { Suspense, lazy, useCallback, useEffect, useId, useState } from "react";
import { Link } from "react-router-dom";
import { ConfirmDialog } from "../../components/ConfirmDialog";
import { AutoNumber, AutoSelect, AutoText, AutoToggle, SavedTick, wholeNumber } from "../../components/settings/auto";
import { Note, Section, input, inputStyle } from "../../components/settings/ui";
import { Field } from "../../ui/Field";
import { Button } from "../../ui/Button";
import { measurePathProblem, pauseFreeProblem, sizeAndFree } from "../../lib/diskGuard";
import { api, type DiskGuardStatus, type NetworkPreference, type RecycleBinStats, type RecycleItem, type RecycleStats } from "../../lib/api";
import { downloadClientApi } from "../../lib/downloadClientApi";
import { hasUsenetClient, NETWORK_PREFERENCES } from "../../lib/network";
import { useQuery } from "../../lib/query";
import { systemApi } from "../../lib/systemApi";
import { LINKS } from "../../lib/links";
import { useAutoSave } from "../../lib/useAutoSave";
import { useLoadedSettings, useSetting, useSettingGroup } from "../../lib/useSettings";

// Settings → Downloads (admin only): the download clients and indexers live on Settings →
// Connections; here are the guard rails around them — the disk guard, the stall timeout and the
// recycle bin that deletes and replacements go through. Each field saves itself.
export function DownloadsSettings() {
  return (
    <div className="flex flex-col gap-6">
      <Section id="download-links" title="Download clients and indexers" subtitle="Both live on Settings → Connections, with their live status: where downloads are sent, and where releases are searched for.">
        <div className="flex flex-wrap gap-x-5 gap-y-2 text-sm font-semibold">
          <Link to={LINKS.downloadClients} style={{ color: "var(--accent-text)" }}>Download clients →</Link>
          <Link to={LINKS.indexers} style={{ color: "var(--accent-text)" }}>Indexers →</Link>
        </div>
      </Section>
      <Section id="search-categories" title="Search categories" subtitle="Which categories an indexer is searched in when it names none of its own (Prowlarr's synced indexers name none).">
        <DefaultCategories />
      </Section>
      <NetworkSection />
      <DiskGuardSection />
      <StallSection />
      <UpgradeBudgetSection />
      <GlobalSeedingSection />
      <RecycleBin />
    </div>
  );
}

// DefaultCategories switches the usual search categories for indexers that name none of
// their own (Prowlarr's synced indexers), so a book search stops coming back with films.
// Off asks every category, as before.
function DefaultCategories() {
  const save = useSetting("indexer_default_categories");
  return (
    <AutoToggle
      label="Search the usual categories"
      hint="Indexers without categories of their own are asked only for the kind of release being searched for — movies, TV, books, audiobooks or music — when they list that category. Turn off if an indexer files releases in odd categories."
      save={save}
    />
  );
}

// NetworkSection is which network wins when the same release is on Usenet and torrents.
// It only means something with a usenet download client, so it only shows with one; the
// client list is the Connections page's, shared through the query cache.
function NetworkSection() {
  const dl = useQuery("download-clients", () => downloadClientApi.downloadClientList(), { staleMs: 30_000 });
  const { s, commit } = useLoadedSettings();
  const pref = useAutoSave<NetworkPreference>(s.download_network_preference ?? "best", (v) => commit({ download_network_preference: v }));
  if (!hasUsenetClient(dl.data?.clients)) return null;
  return (
    <Section
      id="network"
      title="Usenet and torrents"
      subtitle="By default the better release wins, wherever it is. A preference only decides between releases that are otherwise as good; it never skips the better one."
    >
      <AutoSelect label="When a release is on both Usenet and torrents" save={pref} options={NETWORK_PREFERENCES} />
    </Section>
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
// retention, each saved when its field is left) and empty it on demand. Deleted & replaced
// files (movie/episode deletes, Convert originals) land here instead of being erased.
function RecycleBin() {
  // Blank isn't "unlimited" here (the server would read it as 0): 0 has to be typed.
  const maxGB = useSetting("recycle_max_gb", { validate: wholeNumber(0, 100000, "The size cap") });
  const keepDays = useSetting("recycle_retention_days", { validate: wholeNumber(0, 36500, "Keep for") });
  const [stats, setStats] = useState<RecycleStats | null>(null);
  const [items, setItems] = useState<RecycleItem[] | null>(null);
  const [showItems, setShowItems] = useState(false);
  const [busy, setBusy] = useState(false);
  const [rowBusy, setRowBusy] = useState<string | null>(null);
  const [msg, setMsg] = useState<string | null>(null);

  const load = () => api.recycleStats().then(setStats).catch(() => setStats(null));
  const loadItems = () => systemApi.recycleItems().then(setItems).catch(() => setItems([]));
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
      const r = await systemApi.emptyRecycle(bin?.key);
      setMsg(`Freed ${fmtBytes(r.freed_bytes)}.`);
      closeConfirm();
      load(); if (showItems) loadItems();
    } catch (e) { setConfirmErr((e as Error).message); }
    finally { setBusy(false); }
  };

  const restore = async (it: RecycleItem) => {
    setRowBusy(it.id); setMsg(null);
    try { await systemApi.restoreRecycle(it.id); setMsg(`Restored ${it.name}.`); load(); loadItems(); }
    catch (e) { setMsg((e as Error).message); }
    finally { setRowBusy(null); }
  };
  const deleteItem = async (it: RecycleItem) => {
    setRowBusy(it.id); setMsg(null); setConfirmErr(null);
    try { await systemApi.deleteRecycleItem(it.id); closeConfirm(); load(); loadItems(); }
    catch (e) { setConfirmErr((e as Error).message); }
    finally { setRowBusy(null); }
  };

  const bins = stats?.bins ?? [];
  const emptying = confirm?.kind === "empty" ? confirm : null;

  return (
    <Section id="recycle-bin" title="Recycle bin" subtitle="Deleted & replaced files (movie/episode deletes and Convert originals) are moved into a hidden .arrmada-recycle folder inside the library they came from — a quick move on the same drive, hidden from Plex — instead of being erased, so a mistake can be undone until the guard rails below purge it. The size cap counts every bin together, and the oldest files go first once they're over it. Convert only starts a file whose original fits under the cap. To restore a converted film, delete the converted file first: the bin won't restore over it.">
      {stats && !stats.enabled ? (
        <p className="text-sm text-ink-dim">Recycling is turned off (<code>ARRMADA_RECYCLE_DIR=off</code>) — deleted files are erased immediately, and Convert deletes each original once its conversion is verified, with no undo.</p>
      ) : (
        <>
          <div className="flex flex-wrap items-baseline gap-x-5 gap-y-1 text-base">
            <span>Holding <b>{stats ? fmtBytes(stats.bytes) : "…"}</b>{stats ? ` · ${stats.files} file${stats.files === 1 ? "" : "s"}` : ""}</span>
            {stats?.oldest_unix ? <span className="text-ink-faint">oldest {ageOf(stats.oldest_unix)}</span> : null}
          </div>
          {bins.length > 0 && (
            <div className="rounded-lg" style={{ border: "1px solid var(--line)" }}>
              {bins.map((b, i) => (
                <div key={b.key} className="flex flex-wrap items-center gap-x-3 gap-y-1 px-3 py-2" style={i > 0 ? { borderTop: "1px solid var(--line-soft)" } : undefined}>
                  <div className="min-w-0 flex-1">
                    <div className="flex flex-wrap items-center gap-2 text-base font-medium">
                      {b.label}
                      {b.legacy && <span className="rounded px-1.5 py-0.5 text-xs font-semibold" style={{ border: "1px solid var(--avoid)", color: "var(--avoid-text)" }}>Legacy bin</span>}
                    </div>
                    <div className="truncate font-mono text-xs text-ink-faint" title={b.dir}>{b.dir}</div>
                    {b.legacy && <div className="text-xs text-ink-faint">Where deletes used to go. Only files outside every library folder land here now — empty it once you've checked it.</div>}
                    {b.other_drive && <div className="text-xs" style={{ color: "var(--avoid-text)" }}>On a different drive from your library — every delete into it is a full copy.</div>}
                  </div>
                  <span className="flex-none font-mono text-xs text-ink-faint">{fmtBytes(b.bytes)} · {b.files} file{b.files === 1 ? "" : "s"}{b.free_known ? ` · ${fmtBytes(b.free_bytes)} free` : ""}</span>
                  {bins.length > 1 && (
                    <button onClick={() => setConfirm({ kind: "empty", bin: b })} disabled={busy || b.files === 0} className="flex-none rounded-md px-2.5 py-1 text-xs font-semibold disabled:opacity-40" style={{ border: "1px solid var(--line)", color: "var(--reject-text)" }}>Empty this bin</button>
                  )}
                </div>
              ))}
            </div>
          )}
          {stats && stats.over_cap_bytes > 0 && (
            <p className="m-0 text-sm" style={{ color: "var(--avoid-text)" }}>
              Over the cap by {fmtBytes(stats.over_cap_bytes)} — {stats.protected_bytes > 0 && stats.over_cap_bytes <= stats.protected_bytes
                ? "items from the last 3 days are kept until they're older."
                : "the next hourly clean-up will trim it."}
            </p>
          )}
          {stats && stats.max_gb > 0 && stats.largest_item_bytes > stats.max_gb * 1024 ** 3 && (
            <p className="m-0 text-sm" style={{ color: "var(--avoid-text)" }}>Your cap ({stats.max_gb} GB) is smaller than the largest file here ({fmtBytes(stats.largest_item_bytes)}).</p>
          )}
          <div className="grid gap-4 sm:grid-cols-2">
            <AutoNumber label="Max size (GB)" save={maxGB} min={0} max={100000} placeholder="0">
              <span className="text-xs text-ink-faint">0 = unlimited. Over this, the oldest files are purged first — never anything deleted in the last 3 days, or the newest file.</span>
            </AutoNumber>
            <AutoNumber label="Keep for (days)" save={keepDays} min={0} max={36500} placeholder="0">
              <span className="text-xs text-ink-faint">0 = keep forever. Older files are auto-deleted.</span>
            </AutoNumber>
          </div>
          <div className="flex flex-wrap items-center gap-3">
            <button onClick={() => setShowItems((v) => !v)} disabled={(stats?.files ?? 0) === 0} className="rounded-lg px-4 py-2 text-base font-semibold disabled:opacity-50" style={{ border: "1px solid var(--line)", color: "var(--ink)" }}>{showItems ? "Hide contents" : `Manage contents${stats ? ` (${stats.files})` : ""}`}</button>
            <button onClick={() => setConfirm({ kind: "empty" })} disabled={busy || (stats?.files ?? 0) === 0} className="rounded-lg px-4 py-2 text-base font-semibold disabled:opacity-50" style={{ border: "1px solid var(--reject)", color: "var(--reject-text)" }}>{busy ? "Emptying…" : "Empty now"}</button>
            {msg && <span className="text-sm text-ink-dim">{msg}</span>}
          </div>

          {showItems && (
            <div className="rounded-lg" style={{ border: "1px solid var(--line)" }}>
              {items === null ? (
                <div className="p-4 text-center text-sm text-ink-dim">Loading…</div>
              ) : items.length === 0 ? (
                <div className="p-4 text-center text-sm text-ink-dim">The recycle bin is empty.</div>
              ) : (
                <div className="thin-scroll max-h-[340px] overflow-y-auto">
                  {items.map((it) => (
                    <div key={it.id} className="flex items-center gap-3 px-3 py-2" style={{ borderTop: "1px solid var(--line-soft)" }}>
                      <div className="min-w-0 flex-1">
                        <div className="truncate text-base font-medium" title={it.name}>{it.name}</div>
                        <div className="truncate font-mono text-xs text-ink-faint" title={it.orig_path || "origin not recorded"}>{it.orig_path || "origin not recorded"}</div>
                        {bins.length > 1 && <div className="text-xs text-ink-faint">In: {it.bin_label}{it.legacy ? " (legacy bin)" : ""}</div>}
                        {it.expires_at > 0 && <div className="text-xs text-ink-faint">Deleted for good on {fmtDay(it.expires_at)}</div>}
                      </div>
                      <span className="flex-none font-mono text-xs text-ink-faint">{fmtBytes(it.size_bytes)}</span>
                      <span className="hidden flex-none font-mono text-xs text-ink-faint sm:block">{ageOf(it.deleted_unix)}</span>
                      <button onClick={() => restore(it)} disabled={!it.restorable || rowBusy !== null} title={it.restorable ? "Move back to its original location" : "Original location wasn't recorded for this item"} className="flex-none rounded-md px-2.5 py-1 text-xs font-semibold disabled:opacity-40" style={{ border: "1px solid var(--accent-line)", color: "var(--accent-text)" }}>{rowBusy === it.id ? "…" : "Restore"}</button>
                      <button onClick={() => setConfirm({ kind: "item", it })} disabled={rowBusy !== null} title="Delete permanently" className="flex-none rounded-md px-2.5 py-1 text-xs font-semibold disabled:opacity-40" style={{ border: "1px solid var(--line)", color: "var(--reject-text)" }}>Delete</button>
                    </div>
                  ))}
                </div>
              )}
            </div>
          )}

          <p className="text-xs text-ink-faint">Guard rails run automatically about once an hour. Restore moves a file back to where it was deleted from (when that location is free).</p>
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
// The pause and resume points save together, whichever field is left: the server checks
// them as a pair, and checking one against a half-edited other would refuse a good change.
type GuardPair = { downloads_disk_guard_pause_pct: string; downloads_disk_guard_resume_pct: string };
const GUARD_KEYS = ["downloads_disk_guard_pause_pct", "downloads_disk_guard_resume_pct"] as const;
export function guardProblem(v: GuardPair): string | null {
  const p = v.downloads_disk_guard_pause_pct.trim(), r = v.downloads_disk_guard_resume_pct.trim();
  if (!/^\d+$/.test(p) || Number(p) < 1 || Number(p) > 99) return "Pause needs a percentage from 1 to 99.";
  if (!/^\d+$/.test(r)) return "Resume needs a percentage below the pause point.";
  // Mirrors the server's rule. Equal or inverted thresholds would pause and resume on
  // alternate passes forever, so the server rejects them — say so before sending.
  if (Number(r) >= Number(p)) return `Resume (${r}%) must be below pause (${p}%) — otherwise downloads would pause and resume on alternate checks.`;
  return null;
}

function DiskGuardSection() {
  const on = useSetting("downloads_disk_guard");
  const pair = useSettingGroup(GUARD_KEYS, { validate: guardProblem });
  const digits = (v: string) => v.replace(/[^0-9]/g, "").slice(0, 3);
  const edit = (p: Partial<GuardPair>) => pair.set({ ...pair.value, ...p });
  const keys = (e: React.KeyboardEvent<HTMLInputElement>) => {
    if (e.key === "Enter") { e.preventDefault(); void pair.commit(); }
    if (e.key === "Escape" && pair.dirty) { e.preventDefault(); pair.revert(); }
  };
  const fieldStyle = pair.status === "error" ? { ...inputStyle, border: "1px solid var(--reject)" } : inputStyle;

  // The guard measures the downloads folder (lib_downloads_dir, picked in Settings → Library;
  // ARRMADA_DOWNLOADS_DIR is only the fallback), or the drive set under "Measure a different
  // drive". Whether that is the drive torrents fill is not knowable from in here, so show
  // the path it reads and the reading taken from it, and let the user confirm it against
  // their own setup.
  const [status, setStatus] = useState<DiskGuardStatus | null>(null);
  const loadStatus = useCallback(() => {
    api.diskGuard().then(setStatus).catch(() => setStatus(null));
  }, []);
  useEffect(loadStatus, [loadStatus]);
  const { s } = useLoadedSettings();
  const overrideSaved = (s.downloads_disk_guard_measure_path ?? "").trim() !== "";
  const size = status ? sizeAndFree(status.total_bytes, status.free_bytes) : null;
  const floor = useSetting("downloads_disk_guard_pause_free_gb", { validate: pauseFreeProblem });

  return (
    <Section
      id="disk-guard"
      title="Download disk guard"
      subtitle="Pause downloads before the downloads volume fills up. A full disk errors every torrent at once, and on a shared cache pool it takes everything else on that pool with it. Seeding torrents are never paused — they aren't writing anything, and pausing them would put your seed goals at risk."
    >
      {!overrideSaved && (
        <Note tone="warn">
          <b>The guard has to measure the drive your torrents fill.</b> By default it measures
          your Downloads folder, set in{" "}
          <Link to={LINKS.libraryFolders} style={{ color: "var(--accent-text)" }}>Settings → Library</Link>
          {status?.path ? <> (currently <code>{status.path}</code>)</> : null}. If that folder is on
          your main array or in an Unraid user share rather than on the torrent or cache drive,
          the percentage measures the array, and it will either never trigger or pause your queue
          for a reason that has nothing to do with downloads. Point the guard at the right drive
          under <b>Measure a different drive</b> below, or choose a downloads folder on that
          drive. A change is watched from the next check, a minute at most.
        </Note>
      )}

      {status && (
        <div className="rounded-lg p-3" style={{ background: "var(--panel-2)", border: "1px solid var(--line)" }}>
          <div className="flex flex-wrap items-center gap-2">
            <span className="text-xs uppercase tracking-[0.1em] text-ink-faint">Currently watching</span>
            <span className="rounded px-1.5 py-0.5 font-mono text-2xs uppercase" style={{ background: "var(--panel)", border: "1px solid var(--line-soft)", color: "var(--ink-dim)" }}>
              {status.override ? "Different drive" : "Downloads folder"}
            </span>
          </div>
          <div className="mt-0.5 break-all font-mono text-sm">
            {status.measured_path || status.path || "(not set)"}
          </div>
          {status.override && status.path && (
            <div className="mt-0.5 break-all text-xs text-ink-faint">
              Measured instead of the downloads folder, <span className="font-mono">{status.path}</span>. Downloads still go there.
            </div>
          )}
          {status.measurable && (
            <div className="mt-1 text-sm text-ink-dim">
              {status.used_pct.toFixed(1)}% full
              {size && <> · {size}</>}
              {status.holding > 0 && (
                <span style={{ color: "var(--avoid-text)" }}>
                  {" "}· holding {status.holding} torrent{status.holding === 1 ? "" : "s"} paused
                </span>
              )}
            </div>
          )}
          {status.measurable && status.zfs && (
            <div className="mt-1 text-sm" style={{ color: "var(--avoid-text)" }}>
              This is a ZFS pool: the percentage only counts part of it — use the free-space limit below.
            </div>
          )}
          {status.measure_unreadable ? (
            <div role="alert" className="mt-1 text-sm" style={{ color: "var(--reject-text)" }}>
              {status.measure_not_disk ? (
                <>
                  <span className="font-mono">{status.measure_path}</span> reads as a RAM or container
                  filesystem ({status.measure_not_disk}), not a disk: the mapping is gone, or its Host Path
                  doesn't match your pool's name.
                </>
              ) : (
                <>Arrmada can't read the drive at <span className="font-mono">{status.measure_path}</span> any more.</>
              )}{" "}
              {status.measurable
                ? "Downloads measured there aren't paused or resumed until it can be read; downloads folders on other drives are still guarded."
                : "The guard isn't pausing or resuming downloads until it can be read."}{" "}
              It doesn't fall back to the downloads folder, which would measure the wrong drive.
              Anything it already paused stays paused for now: resume it by hand, or turn the guard
              off to release it (clearing the setting releases it too, unless the downloads folder's
              own drive is over the line). Check the path is still mapped into the container, or
              clear it below.
            </div>
          ) : !status.measurable && (
            <div className="mt-1 text-sm" style={{ color: "var(--reject-text)" }}>
              Arrmada can't read this folder's disk usage, so the guard will do nothing. Check the
              path exists and is mounted into the container.
            </div>
          )}
          {(status.volumes?.length ?? 0) > 1 && (
            <ul className="mb-0 mt-2 flex list-none flex-col gap-0.5 p-0 text-xs text-ink-dim">
              {status.volumes!.map((v) => (
                <li key={v.path} className="break-all">
                  <span className="font-mono">{v.path}</span>
                  {" — "}
                  {v.measurable ? `${v.used_pct.toFixed(1)}% full` : "can't be read"}
                  {v.override && <> (measured at <span className="font-mono">{v.measured_path}</span>)</>}
                </li>
              ))}
            </ul>
          )}
          {(status.shared_with?.length ?? 0) > 0 && (
            <div className="mt-1.5 text-sm" style={{ color: "var(--reject-text)" }}>
              Shares a drive with: {status.shared_with.map((f) => f.label).join(", ")}.
              The guard will be measuring your whole array, not a torrent drive — a threshold like
              85% almost certainly isn't what you want here.
              {!status.override && <> Set the drive torrents fill under Measure a different drive below.</>}
            </div>
          )}
        </div>
      )}

      <AutoToggle
        label="Pause downloads when the disk gets full"
        hint="Checked every minute. Only torrents Arrmada paused are resumed — anything you paused by hand stays paused."
        save={on}
      />
      {on.value && (
        <>
          <div className="grid gap-4 sm:grid-cols-2">
            <Field label="Pause at (% full)" hint="Active downloads are paused once the volume is this full.">
              <input
                inputMode="numeric"
                value={pair.value.downloads_disk_guard_pause_pct}
                onChange={(e) => edit({ downloads_disk_guard_pause_pct: digits(e.target.value) })}
                onBlur={() => void pair.commit()}
                onKeyDown={keys}
                aria-invalid={pair.status === "error" || undefined}
                placeholder="85"
                className={input}
                style={fieldStyle}
              />
            </Field>
            <Field label="Resume at (% full)" hint="They restart once it drops back to this. Must be below the pause point.">
              <input
                inputMode="numeric"
                value={pair.value.downloads_disk_guard_resume_pct}
                onChange={(e) => edit({ downloads_disk_guard_resume_pct: digits(e.target.value) })}
                onBlur={() => void pair.commit()}
                onKeyDown={keys}
                aria-invalid={pair.status === "error" || undefined}
                placeholder="80"
                className={input}
                style={fieldStyle}
              />
            </Field>
          </div>
          <SavedTick save={pair} />
          <AutoText
            label="Also pause when free space drops below (GB)"
            save={floor}
            mono={false}
            inputMode="numeric"
            placeholder="0"
            filter={(v) => v.replace(/[^0-9]/g, "").slice(0, 7)}
          >
            <span className="text-xs text-ink-faint">
              0 = off. Applies to whichever drive is measured, alongside the percentage. Held downloads
              resume once free space is back above this plus 10% (at least 1 GB more) and the
              percentage is under its resume point. Set this on a ZFS pool, where the percentage only
              counts part of the pool.
            </span>
          </AutoText>
        </>
      )}
      <MeasureDriveField onSaved={loadStatus} />
    </Section>
  );
}

// FolderPicker is exported from the Library page's module, which this section otherwise
// doesn't need: it's fetched when Browse is pressed rather than with the section.
const FolderPicker = lazy(() => import("../Library").then((m) => ({ default: m.FolderPicker })));

// MeasureDriveField is "Measure a different drive". On Unraid the downloads folder is in a
// user share, which reads as the whole array, while torrents land on the cache pool first:
// the pool mapped into the container read-only, and measured here, is what fills up. A
// folder path, so it saves on an explicit action (Enter, Save, or picking a folder), never
// on blur; the server checks it exists, can be measured and isn't the downloads folder's
// own drive.
function MeasureDriveField({ onSaved }: { onSaved: () => void }) {
  const save = useSetting("downloads_disk_guard_measure_path", { validate: measurePathProblem });
  const { s } = useLoadedSettings();
  const saved = s.downloads_disk_guard_measure_path;
  const [picking, setPicking] = useState(false);
  const id = useId();
  const commit = (v?: string) => {
    void save.commit(v).then((ok) => { if (ok) onSaved(); });
  };
  return (
    <Field label="Measure a different drive (optional)" controlId={id}>
      <div className="flex flex-wrap items-center gap-2">
        <input
          id={id}
          aria-describedby={`${id}-help`}
          value={save.value ?? ""}
          onChange={(e) => save.set(e.target.value)}
          onKeyDown={(e) => {
            if (e.key === "Enter") { e.preventDefault(); commit(); }
            if (e.key === "Escape" && save.dirty) { e.preventDefault(); save.revert(); }
          }}
          placeholder="Empty: measure the downloads folder"
          aria-invalid={save.status === "error" || undefined}
          className={`${input} min-w-[220px] flex-1`}
          style={save.status === "error" ? { ...inputStyle, border: "1px solid var(--reject)" } : inputStyle}
        />
        <Button onClick={() => setPicking(true)}>Browse…</Button>
        {save.dirty && <Button variant="primary" onClick={() => commit()}>Save</Button>}
        {saved && !save.dirty && <Button variant="ghost" onClick={() => commit("")}>Clear</Button>}
      </div>
      <SavedTick save={save} />
      <div id={`${id}-help`} className="flex flex-col gap-1.5 text-xs leading-relaxed text-ink-faint">
        <span>
          Leave this empty to measure the downloads folder. Set it when the downloads folder's drive
          isn't the one that fills up — on Unraid, a downloads folder in a user share (/mnt/user)
          measures the whole array, while new torrents land on the cache pool. Downloads still go to
          the downloads folder; only the measurement moves. Library folders with their own downloads
          folder on the same drive as the Downloads folder are measured here too; ones on another
          drive keep their own measurement.
        </span>
        <span>
          <b className="text-ink-dim">Unraid:</b> edit Arrmada in the Docker tab, choose <b>Add another
          Path, Port, Variable, Label or Device</b>, and add a Path with Container Path{" "}
          <code>/cache</code>, Host Path <code>/mnt/cache</code> (or your pool's name), Access
          Mode <b>Read Only</b>. Apply, then pick <code>/cache</code> here.
        </span>
        <span>
          <b className="text-ink-dim">Docker Compose:</b> add <code>- /mnt/cache:/cache:ro</code> to
          the <code>arrmada-app</code> service's <code>volumes</code>, run <code>docker compose up -d</code>,
          then pick <code>/cache</code> here.
        </span>
        <span>
          <b className="text-ink-dim">ZFS pool:</b> the percentage only counts part of a ZFS pool, so
          also set the free-space limit above.
        </span>
      </div>
      {picking && (
        <Suspense fallback={null}>
          <FolderPicker
            initial={(save.value ?? "").trim() || "/"}
            onClose={() => setPicking(false)}
            onSelect={(p) => { setPicking(false); commit(p); }}
          />
        </Suspense>
      )}
    </Field>
  );
}

// The global stall timeout, shown in hours and stored in minutes. Profiles left on "Use
// default" follow it. Safe to have on: a stalled download is only removed once another
// release has been grabbed in its place.
const hoursText = (minutes: number) => String(Math.round((minutes / 60) * 10) / 10);
export const stallHoursProblem = (v: string) => (/^\d+(\.\d*)?$/.test(v.trim()) && Number(v) <= 168 ? null : "Hours needs a number from 0 to 168.");
function StallSection() {
  const { s, commit } = useLoadedSettings();
  const hours = useAutoSave(hoursText(s.downloads_stall_minutes ?? 360), (v) => commit({ downloads_stall_minutes: Math.round(Number(v) * 60) }), {
    validate: stallHoursProblem,
    // 6 and 6.0 are the same timeout.
    equal: (a, b) => a.trim() !== "" && b.trim() !== "" && Number(a) === Number(b),
  });
  return (
    <Section
      id="stalled-downloads"
      title="Stalled downloads"
      subtitle="When a download makes no progress for this long, Arrmada looks for another release. The stalled one is only removed once a replacement has been grabbed — with nothing else available it's left to keep trying. Time spent paused, queued by the client, rechecking or held by the disk guard doesn't count."
    >
      <AutoText label="Give up on a download with no progress after (hours)" save={hours} mono={false} inputMode="decimal" filter={(v) => v.replace(/[^0-9.]/g, "").slice(0, 5)}>
        <span className="text-xs text-ink-faint">0 = never. Quality profiles can set their own time, or turn it off.</span>
      </AutoText>
    </Section>
  );
}

// How many upgrades one upgrade sweep (movies and TV each, every six hours) may grab. A
// profile edit can make hundreds of files eligible at once; this spreads them out.
function UpgradeBudgetSection() {
  const { s, commit } = useLoadedSettings();
  const budget = useAutoSave(String(s.upgrade_max_grabs_per_sweep ?? 10), (v) => commit({ upgrade_max_grabs_per_sweep: Number(v) }), {
    validate: wholeNumber(0, 1000, "Upgrades per sweep"),
  });
  return (
    <Section
      id="upgrade-limit"
      title="Upgrades per sweep"
      subtitle="Each upgrade sweep — movies and TV separately — grabs at most this many better releases. The rest wait for the next sweep, so changing a quality profile can't start hundreds of downloads at once. Upgrading a single title by hand is never held back."
    >
      <AutoNumber label="Upgrades per sweep" save={budget} min={0} max={1000}>
        <span className="text-xs text-ink-faint">0 = no limit. Each episode counts as one.</span>
      </AutoNumber>
    </Section>
  );
}

// Global seeding limits: defaults for all downloads, with per-indexer overrides.
// When enabled, completed downloads exceeding these limits are paused/stopped/removed.
function GlobalSeedingSection() {
  const enabled = useSetting("seeding_global_enabled");
  const ratioLimit = useSetting("seeding_global_ratio_limit", { validate: nonNegativeNumber("Seed ratio limit") });
  const maxTimeHours = useSetting("seeding_global_max_time_hours", { validate: nonNegativeNumber("Max seeding time (hours)") });
  const action = useSetting("seeding_global_limit_action", { validate: seedingActionValidator });
  const applyToExisting = useSetting("seeding_global_apply_to_existing");

  return (
    <Section
      id="global-seeding-limits"
      title="Global seeding limits"
      subtitle="Default seeding limits for all downloads. Per-indexer settings on Settings → Connections override these. When enabled, Arrmada checks completed downloads against these limits and can automatically pause, stop, or remove torrents that exceed them."
    >
      <AutoToggle
        label="Enable global seeding limits"
        hint="When on, the limits below apply to all downloads unless an indexer has its own explicit seed ratio or seed time set."
        save={enabled}
      />
      {enabled.value && (
        <>
          <div className="grid gap-4 sm:grid-cols-2">
            <AutoNumber
              label="Default seed ratio limit"
              save={ratioLimit}
              min={0}
              max={9999}
              placeholder="2.0"
            >
              <span className="text-xs text-ink-faint">0 = no ratio limit (seed forever by ratio). Indexers with their own setting override this.</span>
            </AutoNumber>
            <AutoNumber
              label="Default max seeding time (hours)"
              save={maxTimeHours}
              min={0}
              max={87600}
              placeholder="168"
            >
              <span className="text-xs text-ink-faint">0 = no time limit (seed forever by time). Indexers with their own setting override this.</span>
            </AutoNumber>
          </div>
          <AutoSelect
            label="When a download exceeds either limit"
            save={action}
            options={[
              { value: "pause", label: "Pause" },
              { value: "stop", label: "Stop (pause, don't auto-resume)" },
              { value: "remove", label: "Remove (delete torrent + data)" },
            ]}
          >
            <span className="text-xs text-ink-faint">Pause = client pauses it. Stop = pause and mark not to auto-resume. Remove = delete from client and disk.</span>
          </AutoSelect>
          <AutoToggle
            label="Apply to existing downloads with per-indexer overrides"
            hint="When on, downloads that already have indexer-specific seed settings are also checked against global limits. When off, only downloads using the global defaults are affected."
            save={applyToExisting}
          />
          <GlobalSeedingActions />
        </>
      )}
    </Section>
  );
}

const nonNegativeNumber = (label: string) => (v: string): string | null => {
  const n = Number(v);
  if (isNaN(n) || n < 0) return `${label} must be a number >= 0`;
  return null;
};

const seedingActionValidator = (v: string): string | null => {
  const valid = ["pause", "stop", "remove"];
  if (!valid.includes(v)) return "Action must be pause, stop, or remove";
  return null;
};

// Actions to verify and apply global seeding limits
function GlobalSeedingActions() {
  const [verifyBusy, setVerifyBusy] = useState(false);
  const [applyBusy, setApplyBusy] = useState(false);
  const [verifyData, setVerifyData] = useState<{ verifications: any[]; count: number } | null>(null);
  const [msg, setMsg] = useState<string | null>(null);

  const verify = async () => {
    setVerifyBusy(true);
    setMsg(null);
    try {
      const res = await fetch("/api/v1/downloads/seeding/verify", { method: "GET" });
      if (!res.ok) throw new Error(await res.text());
      const data = await res.json();
      setVerifyData(data);
      setMsg(`Found ${data.count} download${data.count === 1 ? "" : "s"} exceeding limits.`);
    } catch (e) {
      setMsg((e as Error).message);
    } finally {
      setVerifyBusy(false);
    }
  };

  const apply = async () => {
    setApplyBusy(true);
    setMsg(null);
    try {
      const res = await fetch("/api/v1/downloads/seeding/apply", { method: "POST" });
      if (!res.ok) throw new Error(await res.text());
      const data = await res.json();
      setMsg(`Applied limits to ${data.affected} download${data.affected === 1 ? "" : "s"}.`);
    } catch (e) {
      setMsg((e as Error).message);
    } finally {
      setApplyBusy(false);
    }
  };

  return (
    <div style={{ marginTop: "0.5rem" }}>
      <div className="flex flex-wrap gap-2">
        <button onClick={verify} disabled={verifyBusy} className="rounded-lg px-4 py-2 text-base font-semibold disabled:opacity-50" style={{ border: "1px solid var(--line)", color: "var(--ink)" }}>
          {verifyBusy ? "Verifying…" : "Verify limits"}
        </button>
        <button onClick={apply} disabled={applyBusy || !verifyData?.count} className="rounded-lg px-4 py-2 text-base font-semibold disabled:opacity-50" style={{ border: "1px solid var(--accent-line)", color: "var(--accent-text)" }}>
          {applyBusy ? "Applying…" : "Apply limits"}
        </button>
        {msg && <span className="self-center text-sm text-ink-dim">{msg}</span>}
      </div>
      {verifyData && verifyData.count > 0 && (
        <details className="mt-2" style={{ border: "1px solid var(--line)", borderRadius: "0.5rem" }}>
          <summary className="p-3 cursor-pointer text-sm font-medium">Exceeding downloads ({verifyData.count})</summary>
          <div className="p-3 max-h-64 overflow-auto">
            <table className="w-full text-sm">
              <thead>
                <tr style={{ borderBottom: "1px solid var(--line)" }}>
                  <th className="text-left p-2">Title</th>
                  <th className="text-left p-2">Ratio</th>
                  <th className="text-left p-2">Limit</th>
                  <th className="text-left p-2">Time (h)</th>
                  <th className="text-left p-2">Limit</th>
                  <th className="text-left p-2">Action</th>
                  <th className="text-left p-2">Override</th>
                </tr>
              </thead>
              <tbody>
                {verifyData.verifications.map((v, i) => (
                  <tr key={i} style={{ borderBottom: "1px solid var(--line-soft)" }}>
                    <td className="p-2 truncate max-w-xs" title={v.Title}>{v.Title}</td>
                    <td className="p-2 font-mono">{v.SeedRatio?.toFixed(2) ?? "—"}</td>
                    <td className="p-2 font-mono">{v.SeedRatioLimit > 0 ? v.SeedRatioLimit.toFixed(1) : "—"}</td>
                    <td className="p-2 font-mono">{v.SeedingTimeHours}</td>
                    <td className="p-2 font-mono">{v.MaxSeedingTime > 0 ? v.MaxSeedingTime : "—"}</td>
                    <td className="p-2 capitalize">{v.Action}</td>
                    <td className="p-2">{v.HasOverride ? "Yes" : "No"}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        </details>
      )}
    </div>
  );
}
