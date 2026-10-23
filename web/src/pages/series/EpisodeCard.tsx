import { useState } from "react";
import { AudioLangChips } from "../../components/AudioLangChips";
import { FitBadge } from "../../components/FitBadge";
import { refreshSearches } from "../../components/LastSearch";
import { api, type Episode, type FitItem, type SearchAttempt, type Series as SeriesT, type SubFileEntry } from "../../lib/api";
import { seriesApi } from "../../lib/seriesApi";
import { stillThumb } from "../../lib/img";
import { attemptLine, outcomeTone } from "../../lib/searchOutcome";
import { SEARCHING_OFF_HINT, STATUS_LABEL, libraryStatus } from "../../lib/status";
import { jobFailed, jobToast, useJob } from "../../lib/useJob";
import { Menu, StatusChip, useConfirm, type MenuItem, type Tone } from "../../ui";
import { MENU_ITEM, type Flash } from "./HeroActions";
import { aired, fmtAirDay, sxe } from "./util";
import { EpisodeSubChip } from "./EpisodeSubs";
import { formatPercent } from "../../lib/format";

// What a card asks its season to open. The dialogs live outside the cards: a card is
// paint-contained (content-visibility) while off screen, which would clip a fixed overlay.
export type EpisodeDialog = { kind: "choose" | "file" | "delete"; ep: Episode };

// episodeStatus is the card's one status word: Downloaded, the live download, or for an
// aired episode without a file Wanted, Unmonitored (the episode, or its season, isn't
// wanted) or Searching off (it is wanted, but the show's searching is off — GH-52: that
// used to read Unmonitored, blaming an episode its menu showed as monitored), or Unaired.
// title explains a word that needs it.
export function episodeStatus(series: SeriesT, ep: Episode, seasonMonitored = true): { label: string; tone: Tone; title?: string } {
  const dl = !ep.has_file && ep.download ? ep.download : null;
  if (ep.has_file || (!dl && aired(ep))) {
    const st = libraryStatus({ hasFile: ep.has_file, monitored: ep.monitored && seasonMonitored, searchingOff: !series.monitored });
    if (st.label === STATUS_LABEL.searchingOff) return { label: st.label, tone: st.tone, title: SEARCHING_OFF_HINT };
    return { label: st.label, tone: st.tone };
  }
  if (dl) return { label: `↓ ${formatPercent(dl.progress)}`, tone: "accent" };
  return { label: "Unaired", tone: "faint" };
}

export interface EpisodeCardProps {
  series: SeriesT;
  ep: Episode;
  /** The episode's season is monitored (the sweep needs both flags). */
  seasonMonitored?: boolean;
  fit?: FitItem;
  last?: SearchAttempt;
  onChange: () => void;
  flash: Flash;
  open: (d: EpisodeDialog) => void;
  /** Select mode (SER-16): the card is a checkbox instead of a set of actions. */
  selecting?: boolean;
  selected?: boolean;
  onToggleSelect?: () => void;
  /** The episode's subtitle state (SUB-29), once the season's cards have been seen. */
  subs?: SubFileEntry;
  /** Opens the episode's subtitle drawer. */
  onSubs?: () => void;
}

// EpisodeCard is one episode: its still, number and title, air date and runtime, a
// two-line overview that opens on a tap, the status and fit chips, the live download, and
// one menu holding every action. The monitored dot is the monitor toggle.
export function EpisodeCard({ series, ep, seasonMonitored = true, fit, last, onChange, flash, open, selecting = false, selected = false, onToggleSelect, subs, onSubs }: EpisodeCardProps) {
  const confirm = useConfirm();
  const [expanded, setExpanded] = useState(false);
  const [grabJob, setGrabJob] = useState<number | null>(null);
  const grabbing = useJob(grabJob, {
    onDone: (j) => { setGrabJob(null); if (jobFailed(j)) flash(jobToast(j), true); refreshSearches("series", series.id); onChange(); },
  });
  const dl = !ep.has_file && ep.download ? ep.download : null;
  const dlPct = dl ? Math.round(dl.progress * 100) : 0;
  const status = episodeStatus(series, ep, seasonMonitored);
  const label = sxe(ep);
  const canGrab = !ep.has_file && !dl && aired(ep);

  const toggleMonitored = async () => {
    try { await seriesApi.setEpisodeMonitored(ep.id, !ep.monitored); onChange(); }
    catch (e) { flash((e as Error).message, true); }
  };
  const grab = async () => {
    try {
      const r = await api.autoGrabSeries(series.id, ep.season_number, ep.episode_number);
      if (r.job_id) setGrabJob(r.job_id);
      flash(`Searching for ${label}…`);
    } catch (e) {
      flash((e as Error).message, true);
    }
  };
  const replace = async () => {
    // Replace blocklists a release and downloads another one in the background, so it asks
    // first — and says how far it reaches: a pack it lands only overrides this episode.
    const ok = await confirm({
      title: `Replace ${label}?`,
      body: "The current release is blocklisted and a different one is downloaded. Only this episode's file is replaced.",
      confirmLabel: "Replace",
    });
    if (!ok) return;
    try { await seriesApi.regrabEpisode(series.id, ep.season_number, ep.episode_number); flash(`Replacing ${label} — blocklisted the current release, searching…`); }
    catch (e) { flash((e as Error).message, true); }
  };

  const items: MenuItem[] = [
    ...(canGrab ? [{ label: last ? "Grab again" : "Search automatically", onSelect: grab, disabled: grabbing.running }] : []),
    { label: "Choose release…", onSelect: () => open({ kind: "choose", ep }) },
    ...(ep.has_file ? [
      { label: "Replace…", onSelect: replace },
      { label: "Delete file…", tone: "danger" as const, onSelect: () => open({ kind: "delete", ep }) },
    ] : []),
    ...(ep.has_file && ep.file_path ? [{ label: "File details", onSelect: () => open({ kind: "file", ep }) }] : []),
    { label: ep.monitored ? "Stop monitoring" : "Monitor", busyLabel: "Saving…", onSelect: toggleMonitored },
  ];

  const meta = [ep.air_date ? fmtAirDay(ep.air_date) : "No air date", ep.runtime ? `${ep.runtime} min` : ""].filter(Boolean).join(" · ");
  // The dot says the episode's own state; with the show's searching off it also says when
  // that state counts, rather than one "searching off" line on every episode.
  const dotHint = series.monitored
    ? (ep.monitored ? "Monitored — click to stop" : "Not monitored — click to monitor")
    : (ep.monitored ? "Wanted — searched for once you turn the show's searching on. Click to stop monitoring." : "Not wanted — click to monitor. Nothing is searched while the show's searching is off.");

  return (
    <div
      data-testid="episode-card"
      data-episode={label}
      className="relative flex gap-3 rounded-xl p-2.5 [contain-intrinsic-size:auto_112px] [content-visibility:auto] focus-within:[content-visibility:visible]"
      style={{ background: "var(--panel)", border: `1px solid ${selected ? "var(--accent)" : "var(--line)"}` }}
    >
      {selecting && (
        <label className="absolute inset-0 z-[1] cursor-pointer rounded-xl">
          <input type="checkbox" checked={selected} onChange={onToggleSelect} className="absolute left-4 top-4 h-5 w-5 accent-[var(--accent)]" aria-label={`Select ${label}${ep.title ? ` — ${ep.title}` : ""}`} />
        </label>
      )}
      <div className="relative aspect-video w-[120px] flex-none self-start overflow-hidden rounded-lg sm:w-[176px]" style={{ background: "linear-gradient(150deg, hsl(24 38% 28%), hsl(20 32% 14%))", opacity: ep.monitored || ep.has_file ? 1 : 0.6 }}>
        {ep.still_url ? (
          <img src={stillThumb(ep.still_url)} alt="" loading="lazy" decoding="async" className="h-full w-full object-cover" />
        ) : (
          <span aria-hidden className="absolute bottom-1.5 left-2 font-mono text-sm font-bold text-white/80">E{String(ep.episode_number).padStart(2, "0")}</span>
        )}
        {dl && (
          <div className="absolute inset-x-0 bottom-0 h-[3px]" style={{ background: "rgba(0,0,0,.45)" }}>
            <div className="h-full" style={{ width: `${dlPct}%`, background: "var(--accent)", transition: "width 1s linear" }} />
          </div>
        )}
      </div>

      <div className="min-w-0 flex-1">
        <div className="flex items-start gap-1.5">
          <button
            type="button"
            onClick={toggleMonitored}
            disabled={selecting}
            aria-pressed={ep.monitored}
            aria-label={ep.monitored ? `${label} is monitored — stop monitoring` : `Monitor ${label}`}
            title={dotHint}
            className="-ml-1 grid h-6 w-6 flex-none place-items-center text-md"
            style={{ color: ep.monitored ? "var(--accent-text)" : "var(--ink-faint)" }}
          >
            {ep.monitored ? "◉" : "○"}
          </button>
          <div className="min-w-0 flex-1 pt-[3px] text-md font-semibold leading-snug">
            <span className="font-mono text-sm text-ink-faint">E{String(ep.episode_number).padStart(2, "0")}</span>
            <span className="text-ink-faint"> · </span>
            {ep.has_file && ep.file_path && !selecting ? (
              <button type="button" onClick={() => open({ kind: "file", ep })} title="Show this file's details — media info and the release it came from" className="text-left hover:underline">{ep.title || "TBA"}</button>
            ) : (
              <span>{ep.title || "TBA"}</span>
            )}
          </div>
          {!selecting && (
            <Menu
              label={`Actions for ${label}`}
              trigger={<span aria-hidden className="text-lg leading-none">⋯</span>}
              triggerClassName="-mr-1 -mt-0.5 grid h-8 w-8 flex-none place-items-center rounded-lg text-ink-dim hover:bg-[var(--panel-2)]"
              itemClassName={MENU_ITEM}
              items={items}
            />
          )}
        </div>

        <div className="mt-1 flex flex-wrap items-center gap-x-2 gap-y-1 text-xs text-ink-faint">
          <span className="font-mono">{meta}</span>
          {series.series_type === "anime" && ep.absolute_number ? <span className="rounded px-1 font-mono text-xs" style={{ background: "var(--accent-soft)", color: "var(--accent-text)" }} title="Absolute episode number">#{ep.absolute_number}</span> : null}
          <StatusChip tone={status.tone} size="xs" title={status.title}>{grabbing.running ? "Searching…" : status.label}</StatusChip>
          {ep.has_file && fit?.fit && (
            <span className="inline-flex items-center gap-1">
              <FitBadge item={fit} />
              {fit.facts.bitrate_mbps ? <span className="font-mono text-xs">{fit.facts.bitrate_mbps.toFixed(1)}</span> : null}
            </span>
          )}
          {ep.has_file && <AudioLangChips langs={ep.audio_langs} fit={ep.audio_lang_fit} />}
          {ep.has_file && subs && onSubs && !selecting && <EpisodeSubChip entry={subs} onOpen={onSubs} />}
        </div>

        {ep.overview && (
          <button
            type="button"
            onClick={() => setExpanded((v) => !v)}
            aria-expanded={expanded}
            disabled={selecting}
            className={`mt-1.5 w-full text-left text-sm leading-relaxed text-ink-dim ${expanded ? "block" : "line-clamp-2"}`}
          >
            {ep.overview}
          </button>
        )}
        {canGrab && last && !grabbing.running && (
          <div className="mt-1 text-xs" style={{ color: outcomeTone(last.outcome) }} title="This episode's last search">{attemptLine(last)}</div>
        )}
      </div>
    </div>
  );
}
