import { Suspense, lazy, useState } from "react";
import { useNavigate } from "react-router-dom";
import { DeleteSeriesDialog } from "../../components/DeleteSeriesDialog";
import { MoveFolderDialog } from "../../components/MoveFolderDialog";
import { useLibraryFolders } from "../../components/FolderSelect";
import { folderLabel } from "../../lib/foldersApi";
import { ReleaseSearchModal } from "../../components/ReleaseSearchModal";
import { UploadTorrentModal } from "../../components/UploadTorrentModal";
import { LastSearches, refreshSearches } from "../../components/LastSearch";
import { api, type Series as SeriesT } from "../../lib/api";
import { seriesApi, type MonitorReply } from "../../lib/seriesApi";
import { jobFailed, jobToast, useJob } from "../../lib/useJob";
import { searchJobLine } from "../../lib/searchOutcome";
import { Button, ConfirmDialog, Menu, type MenuItem } from "../../ui";
import { RenameModal } from "./RenameModal";
import { MONITOR_PRESETS } from "./presets";
import { waitingOnSearch } from "./util";
import { ManualImportModal } from "./ManualImportModal";
import { invalidate } from "../../lib/query";
import { ProfileSelector } from "./ProfileSelector";
import { SameNameHint } from "./SameNameHint";
import { TitleProfilePicker } from "../subtitles/ProfileSelect";
import { shownTitle } from "../../lib/displayTitle";

// The "Display title…" picker: loaded when it opens.
const DisplayTitleDialog = lazy(() => import("../../components/DisplayTitleDialog"));

export type Flash = (msg: string, err?: boolean) => void;

// Phone-sized tap targets in the page's menus; a desktop keeps the compact rows.
export const MENU_ITEM = "py-2.5 sm:py-1.5";

// switchNumbering flips a show between standard and anime (absolute) numbering. The hero's
// overflow menu and the Numbering tab both offer it.
export async function switchNumbering(series: SeriesT, onChange: () => void, flash: Flash) {
  const next = series.series_type === "anime" ? "standard" : "anime";
  try {
    await seriesApi.setSeriesType(series.id, next);
    onChange();
    flash(next === "anime" ? "Anime numbering on — rescan to match absolute-numbered files." : "Standard numbering.");
  } catch (e) {
    flash((e as Error).message, true);
  }
}

export const numberingLabel = (series: SeriesT) => (series.series_type === "anime" ? "Switch to standard numbering" : "Switch to anime numbering");

// HeroActions is the show's action row: the Monitor menu and the quality profile, then
// three primary actions — Search missing (the one accent button), Choose release… and the
// overflow menu holding everything else. extraItems lets another feature add to the
// overflow (above Delete) without touching this file's layout.
export function HeroActions({ series, onChange, flash, extraItems = [] }: { series: SeriesT; onChange: () => void; flash: Flash; extraItems?: MenuItem[] }) {
  const navigate = useNavigate();
  // The search and a whole-folder import run as jobs; when one ends the toast says what
  // actually happened.
  const [searchJob, setSearchJob] = useState<number | null>(null);
  const [importJob, setImportJob] = useState<number | null>(null);
  const [starting, setStarting] = useState(false);
  const [searchResult, setSearchResult] = useState<{ text: string; failed: boolean } | null>(null);
  const search = useJob(searchJob, {
    onDone: (j) => {
      setSearchJob(null);
      const text = searchJobLine(j);
      setSearchResult({ text, failed: jobFailed(j) });
      flash(text, jobFailed(j));
      refreshSearches("series", series.id);
      onChange();
    },
  });
  useJob(importJob, {
    onDone: (j) => { setImportJob(null); flash(jobToast(j, "Import finished."), jobFailed(j)); onChange(); },
  });
  const [open, setOpen] = useState<null | "import" | "choose" | "upload" | "rename" | "delete" | "move" | "title" | "forget">(null);
  const [forgetting, setForgetting] = useState(false);
  const [forgetError, setForgetError] = useState<string | null>(null);
  const name = shownTitle(series);
  const close = () => setOpen(null);
  // More than one TV folder: the show's folder shows, and it can move to another one.
  // With one folder neither appears.
  const folders = useLibraryFolders().data?.series;
  const multiFolder = !!folders?.multi;

  const searchMissing = async () => {
    setStarting(true);
    setSearchResult(null);
    try {
      const r = await api.searchSeries(series.id);
      if (r.job_id) setSearchJob(r.job_id);
      flash("Searching — follow packs and episodes in Activity → Queue once grabbed.");
    } catch (e) {
      flash((e as Error).message, true);
    } finally {
      setStarting(false);
    }
  };
  const searching = starting || search.running;

  const overflow: MenuItem[] = [
    {
      label: "Refresh & rescan",
      busyLabel: "Refreshing…",
      onSelect: async () => {
        try { await api.refreshSeries(series.id); onChange(); flash("Refreshed metadata and rescanned disk."); }
        catch (e) { flash((e as Error).message, true); }
      },
    },
    { label: "Forget missing files…", onSelect: () => { setForgetError(null); setOpen("forget"); } },
    { label: "Rename…", onSelect: () => setOpen("rename") },
    { label: "Manual import…", onSelect: () => setOpen("import") },
    { label: "Upload .torrent or .nzb…", onSelect: () => setOpen("upload") },
    { label: numberingLabel(series), busyLabel: "Saving…", onSelect: () => switchNumbering(series, onChange, flash) },
    ...(multiFolder ? [{ label: "Move to folder…", onSelect: () => setOpen("move") }] : []),
    { label: "Display title…", onSelect: () => setOpen("title") },
    ...extraItems,
    { label: "Delete…", tone: "danger", onSelect: () => setOpen("delete") },
  ];

  return (
    <>
      <div className="mt-4 flex flex-wrap items-center gap-2 sm:gap-3">
        <MonitorMenu series={series} onChange={onChange} flash={flash} />
        <ProfileSelector series={series} onChange={onChange} />
        {/* The episodes' subtitle chips follow the show's profile. */}
        <TitleProfilePicker kind="series" id={series.id} title={series.title} onChanged={() => invalidate(`subs-episodes:${series.id}`)} />
      </div>
      <div role="group" aria-label="Show actions" className="mt-2.5 flex flex-nowrap items-center gap-2">
        <Button variant="primary" onClick={searchMissing} busy={searching} busyLabel="Searching…" title="Search every monitored season for what's missing and grab the best fit">
          Search missing
        </Button>
        <Button onClick={() => setOpen("choose")} title="Search indexers for the whole show and pick a pack or episode yourself">Choose release…</Button>
        <Menu
          label="More actions"
          portal
          trigger={<span aria-hidden className="text-lg leading-none">⋯</span>}
          triggerClassName="grid h-[34px] w-[38px] place-items-center rounded-lg"
          triggerStyle={{ background: "var(--panel-2)", border: "1px solid var(--line)", color: "var(--ink)" }}
          itemClassName={MENU_ITEM}
          items={overflow}
        />
        {multiFolder && (
          <span className="min-w-0 truncate text-sm text-ink-faint" title={folders?.folders.find((f) => f.id === (series.library_folder_id ?? 0))?.path}>
            Folder: <b className="font-semibold text-ink-dim">{folderLabel(folders, series.library_folder_id)}</b>
          </span>
        )}
      </div>
      {(search.running || searchResult) && (
        <div className="mt-2 text-sm" role="status" style={{ color: search.running ? "var(--ink-dim)" : searchResult?.failed ? "var(--reject-text)" : "var(--ink)" }}>
          {search.running ? "Searching…" : searchResult?.text}
        </div>
      )}
      <LastSearches kind="series" id={series.id} />
      <SameNameHint seriesId={series.id} />

      {open === "upload" && (
        <UploadTorrentModal
          what={name}
          onPreview={(torrent) => api.previewTorrent(torrent)}
          onGrab={async (torrent, filename, title) => { await api.grabSeriesTorrent(series.id, torrent, filename, title); onChange(); }}
          onClose={close}
        />
      )}
      {open === "rename" && <RenameModal seriesId={series.id} title={name} onClose={close} onRenamed={onChange} />}
      {open === "import" && (
        <ManualImportModal
          series={series}
          onClose={close}
          onImported={() => { onChange(); flash("Imported."); }}
          onBackground={(jobId) => { onChange(); if (jobId) setImportJob(jobId); }}
        />
      )}
      {open === "choose" && (
        <ReleaseSearchModal
          title={`Choose a release — ${name}`}
          subtitle="Whole-series search. Pick any pack or episode to grab."
          fetchReleases={() => api.seriesReleases(series.id)}
          onGrab={async (rel) => { await api.grabSeries(series.id, { token: rel.token ?? "" }); onChange(); }}
          onClose={close}
        />
      )}
      {open === "forget" && (
        <ConfirmDialog
          title={<>Forget missing files for “{name}”?</>}
          body={
            <p className="m-0">
              For files you deleted on purpose, outside Arrmada. Episodes whose files are gone go back to missing, even if the show's whole folder is gone. Nothing on disk is touched.
              {series.monitored ? " Searching is on for this show, so Arrmada will look for those episodes again." : ""}
            </p>
          }
          confirmLabel="Forget missing files"
          busyLabel="Checking…"
          busy={forgetting}
          error={forgetError}
          onConfirm={async () => {
            setForgetting(true); setForgetError(null);
            try {
              const { cleared } = await seriesApi.forgetMissingEpisodes(series.id);
              close();
              onChange();
              flash(cleared === 0 ? "No missing files: every recorded file is still there." : `Forgot ${cleared} missing ${cleared === 1 ? "file" : "files"}.`);
            } catch (e) {
              setForgetError((e as Error).message);
            } finally {
              setForgetting(false);
            }
          }}
          onCancel={close}
        />
      )}
      {open === "delete" && <DeleteSeriesDialog series={series} onClose={close} onDeleted={() => navigate("/series")} />}
      {open === "move" && folders && (
        <MoveFolderDialog
          media="series" id={series.id} title={name} current={series.library_folder_id ?? 0} folders={folders}
          onClose={close}
          onMoved={() => { onChange(); flash(`Moved “${name}”.`); }}
        />
      )}
      {open === "title" && (
        <Suspense fallback={null}>
          <DisplayTitleDialog
            media="series" id={series.id} title={series.title} override={series.title_override ?? ""}
            onClose={close}
            onSaved={() => { invalidate("series"); onChange(); }}
          />
        </Suspense>
      )}
    </>
  );
}

// SearchingOffBanner sits under the show's actions while its searching is off (GH-52): the
// season and episode choices below still read Monitored, so the page says plainly that
// nothing is searched, how much is waiting on it, and turns it on in one click.
export function SearchingOffBanner({ series, onChange, flash }: { series: SeriesT; onChange: () => void; flash: Flash }) {
  const [busy, setBusy] = useState(false);
  if (series.monitored) return null;
  const { wanted, anyMonitored } = waitingOnSearch(series);
  const turnOn = async () => {
    setBusy(true);
    try { await seriesApi.setSeriesMonitored(series.id, true); onChange(); flash("Searching on."); }
    catch (e) { flash((e as Error).message, true); }
    finally { setBusy(false); }
  };
  const detail = !anyMonitored
    ? "No episode is monitored yet: turning it on monitors every episode, or pick what to monitor from Searching."
    : wanted > 0
      ? `${wanted} wanted episode${wanted === 1 ? " waits" : "s wait"} for it.`
      : "Season and episode choices below are kept for when it's on.";
  return (
    <div role="status" data-testid="searching-off-banner" className="mt-3 flex flex-wrap items-center gap-x-3 gap-y-2 rounded-lg px-3 py-2 text-sm" style={{ border: "1px solid var(--line)", background: "var(--panel-2)" }}>
      <span className="min-w-0 flex-1">
        <b className="font-semibold">Searching is off for this show</b> — nothing is searched. <span className="text-ink-dim">{detail}</span>
      </span>
      <Button size="sm" variant="primary" onClick={turnOn} busy={busy} busyLabel="Turning on…">Turn searching on</Button>
    </div>
  );
}

// presetToast is what applying a preset says: that searching came on with it when it did
// (GH-53: a preset on a show with searching off used to change nothing anyone could see).
export function presetToast(label: string, resumed: boolean | undefined): string {
  return resumed ? `Monitoring ${label.toLowerCase()} — searching turned on.` : `Monitoring: ${label}.`;
}

// MonitorMenu holds the show's choices in one place: its searching switch ("Searching: On /
// Off" — season and episode choices are kept while it's off), the presets that pick which
// episodes are wanted (picking one turns searching on, and the menu ticks the one last
// applied, or Custom once episodes were changed by hand), and whether a new season found on
// refresh is monitored.
function MonitorMenu({ series, onChange, flash }: { series: SeriesT; onChange: () => void; flash: Flash }) {
  const act = (fn: () => Promise<unknown>, done: string | ((r: unknown) => string)) => async () => {
    try { const r = await fn(); onChange(); flash(typeof done === "string" ? done : done(r)); }
    catch (e) { flash((e as Error).message, true); }
  };
  const row = (title: string, help?: string, on?: boolean) => (
    <span className="flex w-[240px] max-w-[calc(100vw-72px)] items-start gap-2">
      <span className="min-w-0 flex-1">
        <span className="block">{title}</span>
        {help && <span className="block text-xs font-normal leading-snug text-ink-faint">{help}</span>}
      </span>
      {on !== undefined && <span aria-hidden className="flex-none font-mono text-sm" style={{ color: on ? "var(--accent-text)" : "transparent" }}>✓</span>}
      {on && <span className="sr-only"> (on)</span>}
    </span>
  );
  const on = series.monitored;
  const preset = series.monitor_preset ?? "";
  const items: MenuItem[] = [
    on
      ? { label: row("Turn searching off", "Search nothing for this show. Season and episode choices are kept."), busyLabel: "Turning off…", onSelect: act(() => seriesApi.setSeriesMonitored(series.id, false), "Searching off — nothing is searched until you turn it back on.") }
      : { label: row("Turn searching on", "Search again for the episodes chosen below."), busyLabel: "Turning on…", onSelect: act(() => seriesApi.setSeriesMonitored(series.id, true), "Searching on.") },
    ...MONITOR_PRESETS.map((p): MenuItem => ({
      label: row(`Monitor: ${p.label}`, on ? p.help : `${p.help} Turns searching on.`, preset === p.value),
      busyLabel: "Applying…",
      onSelect: act(() => seriesApi.applySeriesMonitorPreset(series.id, p.value), (r) => presetToast(p.label, (r as MonitorReply | undefined)?.resumed)),
    })),
    // Custom: seasons or episodes were changed by hand since the last preset. Shown ticked
    // so the menu always says what's in force; picking a preset above starts over.
    ...(preset === "custom" ? [{
      label: row("Monitor: Custom (changed by hand)", "Seasons or episodes were picked one by one. Pick a preset above to start over.", true),
      disabled: true,
      onSelect: () => {},
    }] : []),
    {
      label: row("Monitor new seasons", "When a refresh finds a season the show didn't have, monitor it.", !!series.monitor_new_seasons),
      busyLabel: "Saving…",
      onSelect: act(() => seriesApi.setSeriesMonitorNewSeasons(series.id, !series.monitor_new_seasons), series.monitor_new_seasons ? "New seasons won't be monitored." : "New seasons will be monitored."),
    },
  ];
  return (
    <Menu
      label={`Searching: ${on ? "On" : "Off"}`}
      align="left"
      header={on ? "Searching on — wanted episodes are searched" : "Searching off — nothing is searched"}
      trigger={
        <span className="inline-flex items-center gap-2">
          <span aria-hidden className="inline-block h-2.5 w-2.5 rounded-full" style={{ background: on ? "var(--accent)" : "var(--line)" }} />
          Searching: {on ? "On" : "Off"}
          <span aria-hidden className="text-xs text-ink-faint">▾</span>
        </span>
      }
      triggerClassName="rounded-lg px-3 py-2 text-base font-semibold"
      triggerStyle={{ background: "var(--panel-2)", border: `1px solid ${on ? "var(--accent-line)" : "var(--line)"}`, color: on ? "var(--ink)" : "var(--ink-dim)" }}
      itemClassName={MENU_ITEM}
      items={items}
    />
  );
}
