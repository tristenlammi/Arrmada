import { req } from "./api";
import type { PlexPathMap, PlexScanRoot, PlexScanStatus, PlexScanView } from "./plexLibraryApi";

// The Jellyfin connection's calls (its card on Settings → Connections, its group on
// Settings → Media servers). They live here rather than in api.ts so they only download
// with those pages, not with every requester's first load. The API key goes up, never
// comes back: the server answers key_set only.
//
// The same connection can be to an Emby server (server_kind "emby"): Emby, which Jellyfin
// grew out of, speaks nearly the same API, so it uses this slot.

/** Which server the Jellyfin slot is connected to. */
export type JellyfinServerKind = "jellyfin" | "emby";

/** GET /api/v1/jellyfin: the saved connection and what the server said about itself. */
export interface JellyfinConfig {
  /** Missing from a server older than Emby support: Jellyfin. */
  server_kind?: JellyfinServerKind;
  url: string;
  /** The address Watch links use; blank means the connection URL. */
  public_url: string;
  key_set: boolean;
  server_name: string;
  server_id: string;
  version: string;
  monitoring_enabled: boolean;
  poll_seconds: number;
}

/** PUT /api/v1/jellyfin. A missing api_key keeps the saved one; a missing server_kind the
 *  saved kind. */
export interface JellyfinSave {
  server_kind?: JellyfinServerKind;
  url: string;
  public_url: string;
  api_key?: string;
}

/** POST /api/v1/jellyfin/test: blank fields test the saved ones. */
export interface JellyfinTestResult {
  ok: boolean;
  detail: string;
  server_name?: string;
  version?: string;
}

export const jellyfinApi = {
  get: () => req<JellyfinConfig>("/api/v1/jellyfin"),
  save: (body: JellyfinSave) => req<JellyfinConfig>("/api/v1/jellyfin", { method: "PUT", body: JSON.stringify(body) }),
  test: (body: { server_kind?: JellyfinServerKind; url?: string; api_key?: string }) =>
    req<JellyfinTestResult>("/api/v1/jellyfin/test", { method: "POST", body: JSON.stringify(body) }),
  // Forgets the address, the key and the identity (admin). How Jellyfin was used stays.
  disconnect: () => req<JellyfinConfig>("/api/v1/jellyfin", { method: "DELETE" }),
  // Library updates: the same shapes as Plex's (plex_path is the folder on Jellyfin's side).
  scanView: () => req<PlexScanView>("/api/v1/jellyfin/scan"),
  saveScan: (body: { enabled: boolean; path_map: PlexPathMap[] }) =>
    req<PlexScanView>("/api/v1/jellyfin/scan", { method: "PUT", body: JSON.stringify(body) }),
  testScan: (kind: "movie" | "show", run: boolean, root = "") =>
    req<{ root: PlexScanRoot; last_scan: PlexScanStatus }>("/api/v1/jellyfin/scan/test", { method: "POST", body: JSON.stringify({ kind, run, root }) }),
};
