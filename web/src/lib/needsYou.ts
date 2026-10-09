import type { Attention, AttentionKind } from "./api";
import { fixLink } from "./links";

// The Dashboard's "Needs you" card, as rows: one per kind of problem ("3 requests are
// waiting for approval — Review them →"), except where each item is its own fix. A health
// problem links to its own setting, and a failing import or a misfiled download names
// the one torrent, so those are listed one per row.

export interface NeedsYouRow {
  key: string;
  level: "warning" | "error";
  text: string;
  detail?: string;
  /** Where the action goes (an in-app path), and what it says. */
  to?: string;
  action?: string;
  /** Names behind a grouped row, for its tooltip. */
  sample?: string[];
}

// Kinds listed item by item rather than as one counted line.
const ITEMIZED = new Set<AttentionKind>(["health", "import", "wrongcat"]);

const ACTION: Record<AttentionKind, [one: string, many: string]> = {
  request: ["Review it", "Review them"],
  review: ["Open Review", "Open Review"],
  download: ["See it", "See them"],
  stalled: ["See it", "See them"],
  import: ["See downloads", "See downloads"],
  wrongcat: ["See downloads", "See downloads"],
  search: ["See what's searching", "See what's searching"],
  health: ["Fix", "Fix"],
};

export function needsYouRows(a: Attention | undefined): NeedsYouRow[] {
  if (!a) return [];
  const rows: NeedsYouRow[] = [];
  for (const g of a.groups) {
    const [one, many] = ACTION[g.kind] ?? ["Open", "Open"];
    if (ITEMIZED.has(g.kind)) {
      const items = a.items.filter((it) => it.kind === g.kind);
      for (const it of items) {
        rows.push({ key: it.key, level: it.level, text: it.title, detail: it.detail, to: fixLink(it), action: one });
      }
      // The answer carries the worst 50 items; the rest are counted, not dropped.
      const rest = g.count - items.length;
      if (rest > 0) {
        rows.push({ key: `${g.kind}:more`, level: g.level, text: `…and ${rest} more`, to: fixLink(g), action: many });
      }
      continue;
    }
    rows.push({
      key: `group:${g.kind}`,
      level: g.level,
      text: g.title,
      to: fixLink(g),
      action: g.count === 1 ? one : many,
      sample: g.sample,
    });
  }
  return rows;
}
