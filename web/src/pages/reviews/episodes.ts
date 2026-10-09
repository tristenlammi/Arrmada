import type { ReviewFile } from "../../lib/api";

const pad = (n: number) => String(n).padStart(2, "0");

// guessLabel renders what a file's own name says about its episode: "S01E02",
// "S01E03-E04", "#137", "no number" — or nothing for a file that isn't a video.
export function guessLabel(f: ReviewFile): string {
  const g = f.guess;
  if (!f.video) return "";
  if (g.season != null && g.season >= 0 && g.episodes && g.episodes.length > 0) {
    const first = g.episodes[0], last = g.episodes[g.episodes.length - 1];
    return `S${pad(g.season)}E${pad(first)}${g.episodes.length > 1 ? `-E${pad(last)}` : ""}`;
  }
  if (g.absolute && g.absolute.length > 0) return `#${g.absolute.join(", #")}`;
  return "no number";
}

// parseEpisodes reads an Episode(s) box: "3", "3,4", "3 4" or "3-4". Blank is null (the
// row is skipped); anything else unreadable is undefined (the row is invalid).
export function parseEpisodes(raw: string): number[] | null | undefined {
  const s = raw.trim();
  if (s === "") return null;
  const out: number[] = [];
  for (const part of s.split(/[\s,]+/)) {
    const range = /^(\d+)\s*-\s*(\d+)$/.exec(part);
    if (range) {
      const a = Number(range[1]), b = Number(range[2]);
      if (a < 1 || b < a || b - a > 50) return undefined;
      for (let n = a; n <= b; n++) out.push(n);
      continue;
    }
    if (!/^\d+$/.test(part) || Number(part) < 1) return undefined;
    out.push(Number(part));
  }
  return [...new Set(out)];
}

// prefill is the Season and Episode(s) a file's row starts with, from its own name.
export function prefill(f: ReviewFile): { season: string; episodes: string } {
  const g = f.guess;
  if (g.season != null && g.season >= 0 && g.episodes && g.episodes.length > 0) {
    return { season: String(g.season), episodes: g.episodes.join(",") };
  }
  return { season: g.season ? String(g.season) : "", episodes: "" };
}
