import type { FileFit, FitItem, FitStatus } from "../lib/api";

// How a library file fits its quality profile's ideal file (report only). Red: over the
// bitrate ceiling. Orange: under the floor. Yellow: the bitrate's fine, something else
// (codec, HDR, audio) isn't. Green: fits.

export const FIT_COLOR: Record<FitStatus, string> = {
  over: "var(--reject)",
  under: "var(--under)",
  mismatch: "var(--mismatch)",
  fits: "var(--good)",
};

const FIT_LABEL: Record<FitStatus, string> = { over: "Over", under: "Under", mismatch: "Doesn't fit", fits: "Fits" };

// fitRank sorts the worst first: over, under, mismatch, fits; unjudged last.
export function fitRank(it?: FitItem): number | undefined {
  if (!it?.fit) return undefined;
  return { over: 0, under: 1, mismatch: 2, fits: 3 }[it.fit.status];
}

// fitTitle is the tooltip: every reason, plus the window that applied.
export function fitTitle(it?: FitItem): string {
  if (!it) return "Not analysed yet — files are analysed in the background after they're imported";
  if (!it.fit) return "This title's quality profile has no ideal file set up (Settings → Quality)";
  const lines = (it.fit.issues ?? []).map((i) => "• " + i.msg);
  if (it.fit.window) lines.push(`Bitrate window: ${win(it.fit.window)}`);
  if (it.profile) lines.unshift(`Profile: ${it.profile}`);
  return lines.join("\n") || "Fits its profile's ideal file";
}

function win(w: { min: number; max: number }): string {
  if (w.min && w.max) return `${w.min}–${w.max} Mb/s`;
  return w.max ? `up to ${w.max} Mb/s` : `at least ${w.min} Mb/s`;
}

// hasIssue reports whether the fit names a problem of this kind (codec, hdr, atmos…).
export function hasIssue(fit: FileFit | undefined, kind: string): boolean {
  return !!fit?.issues?.some((i) => i.kind === kind);
}

// bitrateColor colours a bitrate against its window: red over, orange under, green inside.
// undefined = no window applies, leave it plain.
export function bitrateColor(fit?: FileFit): string | undefined {
  if (!fit?.window) return undefined;
  if (fit.status === "over") return FIT_COLOR.over;
  if (fit.status === "under") return FIT_COLOR.under;
  return FIT_COLOR.fits;
}

export function FitBadge({ item }: { item?: FitItem }) {
  if (!item?.fit) return <span className="text-[11px] text-ink-faint" title={fitTitle(item)}>—</span>;
  const c = FIT_COLOR[item.fit.status];
  return (
    <span title={fitTitle(item)} className="inline-block cursor-help whitespace-nowrap rounded px-1.5 py-0.5 text-[10px] font-bold"
      style={{ color: c, border: `1px solid ${c}`, background: "transparent" }}>
      {FIT_LABEL[item.fit.status]}
    </span>
  );
}
