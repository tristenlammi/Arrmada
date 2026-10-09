import type { Series, SeriesEpisodeDownload } from "../../lib/api";

// downloadingCount is how many episodes of the show have a download in flight.
export function downloadingCount(s: Series): number {
  let n = 0;
  for (const sn of s.seasons ?? []) for (const e of sn.episodes ?? []) if (e.download) n++;
  return n;
}

// mergeDownloads lays the light poll's answer over the loaded show: every episode takes its
// download from the poll (or loses it when the poll no longer lists it), and nothing else
// changes. The show object is returned untouched when no episode's download moved, so a
// quiet tick doesn't re-render the page.
export function mergeDownloads(s: Series, dls: SeriesEpisodeDownload[]): Series {
  const byEp = new Map(dls.map((d) => [`${d.season}:${d.episode}`, d]));
  let changed = false;
  const seasons = (s.seasons ?? []).map((sn) => {
    let seasonChanged = false;
    const episodes = (sn.episodes ?? []).map((e) => {
      const d = byEp.get(`${e.season_number}:${e.episode_number}`);
      const next = d ? { state: d.state, progress: d.progress } : undefined;
      const cur = e.download;
      if (cur?.state === next?.state && cur?.progress === next?.progress) return e;
      seasonChanged = true;
      return { ...e, download: next };
    });
    if (!seasonChanged) return sn;
    changed = true;
    return { ...sn, episodes };
  });
  return changed ? { ...s, seasons } : s;
}
