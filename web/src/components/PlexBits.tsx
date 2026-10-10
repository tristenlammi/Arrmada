import { useEffect, useState } from "react";
import { plexApi } from "../lib/plexApi";

// PlexPill is the "Plex" link beside IMDb and TMDB on a title's page: it opens the title
// in app.plex.tv on the owner's server. Asked for after the page has loaded, and not shown
// at all when Plex doesn't have the title (or isn't connected).
export function PlexPill({ media, tmdbId }: { media: "movie" | "series"; tmdbId: number }) {
  const [url, setUrl] = useState<string | null>(null);
  useEffect(() => {
    let live = true;
    setUrl(null);
    if (tmdbId > 0) plexApi.link(media, tmdbId).then((u) => { if (live) setUrl(u); }).catch(() => {});
    return () => { live = false; };
  }, [media, tmdbId]);
  if (!url) return null;
  return (
    <a href={url} target="_blank" rel="noopener noreferrer" title="Watch on Plex" className="rounded px-1.5 py-0.5 font-mono text-[10px] font-bold" style={{ background: "#e5a00d", color: "#1f1f1f" }}>
      ▶ Plex
    </a>
  );
}
