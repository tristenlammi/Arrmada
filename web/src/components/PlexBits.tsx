import { useEffect, useState } from "react";
import { plexApi, type WatchStats } from "../lib/plexApi";
import { ago } from "../lib/taskTime";

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

// WatchedBy is the "Watched by 3 · last played 2 days ago" line under a title's header,
// from the Plex play history; tap it for the names. Staff pages only. Nothing shows when
// Plex isn't connected or nobody has played it.
export function WatchedBy({ kind, id }: { kind: "movies" | "series"; id: number }) {
  const [ws, setWs] = useState<WatchStats | null>(null);
  useEffect(() => {
    let live = true;
    setWs(null);
    plexApi.watchStats(kind, id).then((s) => { if (live) setWs(s); }).catch(() => {});
    return () => { live = false; };
  }, [kind, id]);
  if (!ws || !ws.available || ws.plays === 0 || ws.users.length === 0) return null;
  const names = ws.users.map((u) => u.name || "Someone");
  return (
    <details className="mt-2 text-[12px] text-ink-dim">
      <summary className="cursor-pointer select-none" title={names.join(", ")}>
        Watched by {ws.users.length === 1 ? names[0] : ws.users.length} · last played {ago(ws.last_played)}
      </summary>
      <ul className="m-0 mt-1.5 flex list-none flex-col gap-1 p-0 pl-3">
        {ws.users.map((u, i) => (
          <li key={u.id}>
            <span className="text-ink">{names[i]}</span>
            <span className="text-ink-faint"> · {u.plays} play{u.plays === 1 ? "" : "s"} · {ago(u.last_played)}</span>
          </li>
        ))}
      </ul>
    </details>
  );
}
