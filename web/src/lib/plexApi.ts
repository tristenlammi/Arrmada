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

export const plexApi = {
  scanView: () => req<PlexScanView>("/api/v1/insights/plex/scan"),
  saveScan: (body: { enabled: boolean; path_map: PlexPathMap[] }) =>
    req<PlexScanView>("/api/v1/insights/plex/scan", { method: "PUT", body: JSON.stringify(body) }),
  // run=false only resolves the folder (Test); run=true also asks Plex to scan it.
  testScan: (kind: "movie" | "show", run: boolean) =>
    req<{ root: PlexScanRoot; last_scan: PlexScanStatus }>("/api/v1/insights/plex/scan/test", { method: "POST", body: JSON.stringify({ kind, run }) }),
};
