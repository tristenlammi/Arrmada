import type { ReactNode } from "react";
import type { FitItem, MovieDownload, MovieSummary, SummaryMedia } from "../../lib/api";
import { FitBadge, FIT_COLOR, bitrateColor, fitRank, hasIssue } from "../../components/FitBadge";
import { shownTitle } from "../../lib/displayTitle";
import { isMovieWanted, movieStatus } from "../../lib/movieStatus";
import { STATUS_LABEL } from "../../lib/status";
import type { LibraryConfig } from "../../features/library/types";
import { shortDate } from "../../features/library/format";
import { POSTER_CHIP_BG, type MenuItem } from "../../ui";
import { formatPercent } from "../../lib/format";

// The Movies library as LibraryGrid config: its filters, card slots and table columns.

// downloadLabel names a download that isn't the film's own missing file: "Upgrade", or the
// extra version's label.
export function downloadLabel(d?: MovieDownload): string {
  if (d?.kind === "upgrade") return "Upgrade";
  if (d?.kind === "version") return d.version_label || "Version";
  return "";
}

function gb(bytes?: number): string {
  return bytes ? `${(bytes / 1024 ** 3).toFixed(1)} GB` : "—";
}
function resValue(f?: SummaryMedia): number | undefined {
  const r = f?.resolution || (f?.quality ? f.quality.split(" ")[0] : "");
  if (!r) return undefined;
  const s = r.toLowerCase();
  if (s.includes("4k") || s.includes("2160")) return 2160;
  if (s.includes("1440")) return 1440;
  if (s.includes("1080")) return 1080;
  if (s.includes("720")) return 720;
  if (s.includes("480") || s === "sd") return 480;
  const n = parseInt(s.replace(/\D/g, ""), 10);
  return isNaN(n) ? undefined : n;
}
export function hasAtmos(f?: SummaryMedia): boolean {
  return !!f?.atmos || !!f?.audio?.some((a) => /atmos/i.test(a));
}

// YesNo renders a clear yes/no so gaps (missing HDR/Atmos) are easy to scan for.
function YesNo({ on, label }: { on: boolean; label?: string }) {
  return on
    ? <span className="rounded px-1.5 py-0.5 text-xs font-bold" style={{ background: "var(--good-soft, rgba(90,140,90,.14))", color: "var(--good-text)" }}>{label ?? "Yes"}</span>
    : <span className="text-xs text-ink-faint">—</span>;
}

// DownloadBar is a download's progress along the poster's foot, labelled when it's an
// upgrade or an extra version rather than the film's own missing file.
function DownloadBar({ d }: { d: MovieDownload }) {
  const label = downloadLabel(d);
  const pctNum = Math.min(100, Math.max(0, d.progress * 100));
  const pctText = formatPercent(d.progress);
  return (
    <div className="absolute inset-x-0 bottom-0 p-1.5">
      <div className="flex items-center gap-1.5 rounded-md px-1.5 py-1" style={{ background: "rgba(20,12,7,.82)" }} title={label ? `Downloading: ${label}` : "Downloading"}>
        {label && <span className="max-w-[60px] truncate font-mono text-2xs font-bold uppercase" style={{ color: "var(--accent-text)" }}>{label}</span>}
        <div className="h-1 flex-1 overflow-hidden rounded-full" style={{ background: "rgba(255,255,255,.25)" }}>
          <div className="h-full rounded-full" style={{ width: `${Math.round(pctNum)}%`, background: "var(--accent)" }} />
        </div>
        <span className="font-mono text-xs font-bold" style={{ color: "#fff" }}>{pctText}</span>
      </div>
    </div>
  );
}

// isMisfit: the file was judged against its profile's ideal file and doesn't fit.
export function isMisfit(it?: FitItem): boolean {
  const s = it?.fit?.status;
  return !!s && s !== "fits";
}

// The poster's quality badges, from the file's media facts: "4K", "1080p"…; "DV" or "HDR".
export function resBadge(f?: SummaryMedia): string {
  const v = resValue(f);
  if (!v) return "";
  if (v >= 2160) return "4K";
  if (v >= 720) return `${v}p`;
  return "SD";
}
export function hdrBadge(f?: SummaryMedia): string {
  const h = f?.hdr ?? [];
  if (h.some((x) => /^dv$|dolby/i.test(x))) return "DV";
  return h.length ? "HDR" : "";
}

function Badge({ children }: { children: ReactNode }) {
  return <span className="rounded px-1 py-px font-mono text-xs font-bold leading-[1.4]" style={{ background: POSTER_CHIP_BG, color: "#fff" }}>{children}</span>;
}

// MovieBadges sit over a poster's top-left corner: the monitored dot (filled when
// monitored, hollow when not), then the file's resolution, DV/HDR and Atmos.
function MovieBadges({ m }: { m: MovieSummary }) {
  const f = m.has_file ? m.media : undefined;
  const res = resBadge(f), hdr = hdrBadge(f);
  const mon = m.monitored ? "Monitored" : STATUS_LABEL.unmonitored;
  return (
    <>
      <span role="img" aria-label={mon} title={mon} className="mr-0.5 mt-[3px] h-2.5 w-2.5 flex-none rounded-full" style={{ background: m.monitored ? "var(--accent)" : "rgba(14,10,7,.6)", border: `1.5px solid ${m.monitored ? "var(--accent)" : "rgba(255,255,255,.8)"}`, boxShadow: "0 0 0 1px rgba(14,10,7,.55)" }} />
      {res && <Badge>{res}</Badge>}
      {hdr && <Badge>{hdr}</Badge>}
      {f && hasAtmos(f) && <Badge>Atmos</Badge>}
    </>
  );
}

export interface MovieConfigDeps {
  fits: Map<number, FitItem>;
  /** false while the download client can't be read: a wanted film may already be downloading. */
  queueKnown: boolean;
  actions: (m: MovieSummary) => MenuItem[];
  rowAction: (m: MovieSummary) => ReactNode;
}

export function movieLibraryConfig({ fits, queueKnown, actions, rowAction }: MovieConfigDeps): LibraryConfig<MovieSummary> {
  const bad = (m: MovieSummary, kind: string) => (hasIssue(fits.get(m.id)?.fit, kind) ? { color: FIT_COLOR.over, fontWeight: 600 } : undefined);
  return {
    noun: { one: "movie", many: "movies" },
    storageKey: "movies",
    detailPath: (m) => `/movies/${m.id}`,
    // Wanted is exactly what's badged Wanted (monitored, no file, nothing downloading) — the
    // same films as Wanted → Missing, so the counts agree. A film with no file that isn't
    // monitored is never searched for; it has its own filter rather than hiding inside
    // Wanted. Downloading is any download in flight (its own file, an upgrade, a version).
    filters: [
      { key: "all", label: "All" },
      { key: "wanted", label: STATUS_LABEL.wanted, match: (m) => isMovieWanted(m, queueKnown), title: "Monitored, no file yet: Arrmada is searching for these" },
      { key: "downloading", label: "Downloading", match: (m) => !!m.download },
      { key: "nofile", label: "No file (unmonitored)", match: (m) => !m.monitored && !m.has_file, title: "No file and not monitored: never searched for" },
      { key: "filemissing", label: STATUS_LABEL.fileMissing, match: (m) => movieStatus(m, undefined, queueKnown).key === "missing", title: "A file was recorded but is gone from disk" },
      { key: "misfit", label: "Doesn't fit", match: (m) => isMisfit(fits.get(m.id)), title: "The file doesn't fit its profile's ideal file (bitrate or spec) — the table says why" },
      { key: "unmonitored", label: STATUS_LABEL.unmonitored, match: (m) => !m.monitored },
    ],
    // "dune 2021" finds Dune (2021); the sort title lets "matrix" find The Matrix. The
    // shown title and the English one both find a film shown under another name.
    // Original and alternative titles join this when the summary carries them (MOV-24).
    searchText: (m) => `${m.display_title ?? ""} ${m.title} ${m.sort_title ?? ""} ${m.year || ""}`,
    sorts: [
      { key: "title", label: "Title" },
      { key: "added", label: "Added", dir: "desc", value: (m) => m.added_at || undefined },
      { key: "year", label: "Year", dir: "desc", value: (m) => m.year || undefined },
      { key: "rating", label: "Rating", dir: "desc", value: (m) => m.vote_average || undefined },
      { key: "size", label: "Size", dir: "desc", value: (m) => m.size_bytes || undefined },
    ],
    // Alphabetical by the shown title (the server's sort title follows it), "The" ignored.
    compare: (a, b) => (a.sort_title || shownTitle(a)).localeCompare(b.sort_title || shownTitle(b)),
    tableMinWidth: 1120,
    // Year, Size, Rating and Added are the Sort menu's orders too: a header click and the
    // menu pick the same sort (newest, biggest and best first).
    columns: [
      { key: "year", label: "Year", align: "right", className: "font-mono text-xs text-ink-dim", sort: (m) => m.year || undefined, cell: (m) => m.year || "—" },
      { key: "status", label: "Status", sort: (m) => (m.has_file ? 0 : m.monitored ? 1 : 2), cell: (m) => { const st = movieStatus(m, undefined, queueKnown); return <span className="font-mono text-2xs uppercase" style={{ color: st.color }}>{st.label}</span>; } },
      { key: "resolution", label: "Resolution", sort: (m) => resValue(m.media), cell: (m) => m.media?.resolution || (m.media?.quality ? m.media.quality.split(" ")[0] : "—") },
      { key: "codec", label: "Codec", sort: (m) => m.media?.codec || undefined, cell: (m) => <span style={bad(m, "codec")}>{m.media?.codec || "—"}</span> },
      { key: "audio", label: "Audio", sort: (m) => (m.media?.audio?.length ? m.media.audio.join(", ").toLowerCase() : undefined), cell: (m) => <span style={bad(m, "lossless")}>{m.media?.audio?.length ? m.media.audio.join(", ") : "—"}</span> },
      { key: "atmos", label: "Atmos", sort: (m) => (hasAtmos(m.media) ? 1 : 0), cell: (m) => (hasIssue(fits.get(m.id)?.fit, "atmos") ? <span className="text-xs font-semibold" style={{ color: FIT_COLOR.over }}>No</span> : <YesNo on={hasAtmos(m.media)} />) },
      { key: "hdr", label: "HDR", sort: (m) => (m.media?.hdr?.length ? 1 : 0), cell: (m) => {
        const hdr = m.media?.hdr;
        if (hasIssue(fits.get(m.id)?.fit, "hdr")) return <span className="text-xs font-semibold" style={{ color: FIT_COLOR.over }}>{hdr?.length ? hdr.join("/") : "SDR"}</span>;
        return hdr?.length ? <YesNo on label={hdr.join("/")} /> : <YesNo on={false} />;
      } },
      { key: "type", label: "Type", sort: (m) => m.media?.container || undefined, cell: (m) => <span className="font-mono text-xs text-ink-dim">{m.media?.container || "—"}</span> },
      { key: "size", label: "Size", align: "right", className: "font-mono text-xs text-ink-dim", sort: (m) => m.size_bytes || undefined, cell: (m) => gb(m.size_bytes) },
      { key: "bitrate", label: "Bitrate", align: "right", className: "font-mono text-xs text-ink-dim", sort: (m) => m.media?.bitrate_mbps || undefined, cell: (m) => {
        const fi = fits.get(m.id);
        const c = bitrateColor(fi?.fit);
        const n = m.media?.bitrate_mbps;
        return <span style={c ? { color: c, fontWeight: 600 } : undefined} title={fi?.fit?.window ? `Window ${fi.fit.window.min || 0}–${fi.fit.window.max || "∞"} Mb/s` : undefined}>{n ? `${n.toFixed(1)} Mbps` : "—"}</span>;
      } },
      { key: "fit", label: "Fit", sort: (m) => fitRank(fits.get(m.id)), cell: (m) => <FitBadge item={fits.get(m.id)} /> },
      { key: "rating", label: "Rating", align: "right", title: "TMDB's rating", className: "font-mono text-xs text-ink-dim", sort: (m) => m.vote_average || undefined, cell: (m) => (m.vote_average ? m.vote_average.toFixed(1) : "—") },
      { key: "added", label: "Added", align: "right", className: "whitespace-nowrap font-mono text-xs text-ink-dim", sort: (m) => m.added_at || undefined, cell: (m) => shortDate(m.added_at) },
    ],
    card: {
      meta: (m) => m.year || "—",
      status: (m) => movieStatus(m, undefined, queueKnown),
      overlay: (m) => m.download && <DownloadBar d={m.download} />,
      badges: (m) => <MovieBadges m={m} />,
    },
    actions,
    rowAction,
  };
}

// FitLegend sits above the table: how many judged files don't fit their profile (the
// "Doesn't fit" filter lists them) and what the fit colours mean.
export function FitLegend({ judged, misfits }: { judged: number; misfits: number }) {
  if (judged === 0) return null;
  return (
    <div className="mb-2 flex flex-wrap items-center gap-3 text-sm text-ink-dim">
      <span>{misfits} of {judged} judged files don't fit their profile</span>
      <span className="text-xs text-ink-faint">
        <span style={{ color: FIT_COLOR.over }}>■</span> over the bitrate ceiling ·{" "}
        <span style={{ color: FIT_COLOR.under }}>■</span> under the floor ·{" "}
        <span style={{ color: FIT_COLOR.mismatch }}>■</span> bitrate fine, spec isn't ·{" "}
        <span style={{ color: FIT_COLOR.fits }}>■</span> fits — hover for why
      </span>
    </div>
  );
}
