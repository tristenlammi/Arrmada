import { req } from "./api";

// The Plex calls staff pages make (library updates, Watch on Plex links, Watched by).
// They live apart from lib/api.ts because only lazily loaded pages import them: the
// requester's first load is right at its budget.

export type PlexScanHow = "direct" | "mapped" | "guessed" | "section" | "none";

export interface PlexPathMap { from: string; to: string }

export interface PlexScanRoot {
  kind: "movie" | "show";
  arrmada_root: string;
  sections: string[];
  plex_path: string;
  how?: PlexScanHow;
  note?: string;
}

export interface PlexScanStatus {
  at: string;
  kind?: string;
  path?: string;
  section?: string;
  how?: PlexScanHow;
  error?: string;
}

export interface PlexScanView {
  enabled: boolean;
  configured: boolean;
  path_map: PlexPathMap[];
  roots: PlexScanRoot[];
  last_scan: PlexScanStatus | null;
  pending: number;
  error?: string;
}

/** Who has played a title on Plex (staff only). available is false without Plex. */
export interface WatchStats {
  available: boolean;
  plays: number;
  last_played: number; // unix seconds
  users: { id: string; name: string; plays: number; last_played: number }[];
}

export const plexApi = {
  watchStats: (kind: "movies" | "series", id: number) => req<WatchStats>(`/api/v1/${kind}/${id}/watch-stats`),
  // The title's app.plex.tv page, or null when Plex doesn't have it (the server answers 204).
  link: (media: "movie" | "series", tmdbId: number) =>
    req<{ url: string } | undefined>(`/api/v1/plex/link?media_type=${media}&tmdb_id=${tmdbId}`).then((r) => r?.url ?? null),
  scanView: () => req<PlexScanView>("/api/v1/insights/plex/scan"),
  saveScan: (body: { enabled: boolean; path_map: PlexPathMap[] }) =>
    req<PlexScanView>("/api/v1/insights/plex/scan", { method: "PUT", body: JSON.stringify(body) }),
  // run=false only resolves the folder (Test); run=true also asks Plex to scan it.
  testScan: (kind: "movie" | "show", run: boolean) =>
    req<{ root: PlexScanRoot; last_scan: PlexScanStatus }>("/api/v1/insights/plex/scan/test", { method: "POST", body: JSON.stringify({ kind, run }) }),
};
