// The Settings hub's map: which sections exist, who sees them, where every control lives
// (for the search box) and where an old link should land. Kept free of components so it
// can be tested on its own; pages/Settings.tsx pairs each id with the component it renders.

export type SectionId = "library" | "media" | "downloads" | "users" | "import" | "system" | "status";

export interface SettingsSection {
  id: SectionId;
  label: string;
  /** Hidden from managers (and from their search results); the server refuses them anyway. */
  adminOnly: boolean;
}

// Rail order. The names match the paths copy already uses ("Settings → Library",
// "Settings → Downloads", "Settings → System → API keys", "Settings → Users").
export const SECTIONS: readonly SettingsSection[] = [
  { id: "library", label: "Library", adminOnly: false },
  { id: "media", label: "Naming & metadata", adminOnly: false },
  { id: "downloads", label: "Downloads", adminOnly: true },
  { id: "users", label: "Users", adminOnly: true },
  { id: "import", label: "Import", adminOnly: true },
  { id: "system", label: "System", adminOnly: true },
  // Slot for System → Status (pages/settings/Status.tsx, built by the health work).
  { id: "status", label: "Status", adminOnly: true },
];

export const DEFAULT_SECTION: SectionId = "library";

export function visibleSections(admin: boolean): SettingsSection[] {
  return SECTIONS.filter((s) => admin || !s.adminOnly);
}

export interface SearchEntry {
  label: string;
  keywords: string;
  section: SectionId;
  /** The card's element id, so a result scrolls straight to it. */
  anchor: string;
}

// Every card in every section, with the words someone might type to find it.
export const SEARCH_INDEX: readonly SearchEntry[] = [
  { label: "Media folders", keywords: "movies tv shows ebooks audiobooks music downloads folder path browse scan mount directory unmatched needs review", section: "library", anchor: "media-folders" },
  { label: "Adding titles", keywords: "search on add automatic default", section: "library", anchor: "adding-titles" },
  { label: "Movie naming", keywords: "folder file name format template tokens rename", section: "media", anchor: "movie-naming" },
  { label: "Series naming", keywords: "tv show season episode folder file name format template tokens specials rename", section: "media", anchor: "series-naming" },
  { label: "Metadata", keywords: "nfo movie.nfo artwork poster fanart kodi plex jellyfin emby sidecar", section: "media", anchor: "metadata" },
  { label: "Download clients and indexers", keywords: "qbittorrent prowlarr torrent client indexer", section: "downloads", anchor: "download-links" },
  { label: "Download disk guard", keywords: "disk full space free pause resume threshold percent drive cache", section: "downloads", anchor: "disk-guard" },
  { label: "Stalled downloads", keywords: "stall stuck timeout no progress replacement fail over hours", section: "downloads", anchor: "stalled-downloads" },
  { label: "Upgrades per sweep", keywords: "upgrade limit budget sweep quality profile replace better release grabs at once", section: "downloads", anchor: "upgrade-limit" },
  { label: "Recycle bin", keywords: "trash deleted restore empty retention size cap purge undo", section: "downloads", anchor: "recycle-bin" },
  { label: "Users", keywords: "add user people accounts roles admin manager requester read-only password auto-approve block delete disable sign in", section: "users", anchor: "users" },
  { label: "Plex sign-in", keywords: "plex login sign in with plex auto-approve home shared users", section: "users", anchor: "plex-sign-in" },
  { label: "Import from Overseerr / Jellyseerr", keywords: "overseerr jellyseerr requests migrate import", section: "import", anchor: "overseerr-import" },
  { label: "Import from Tautulli", keywords: "tautulli watch history plex insights migrate import", section: "import", anchor: "tautulli-import" },
  { label: "Modules", keywords: "books music enable disable turn on off preview module", section: "system", anchor: "modules" },
  { label: "API keys", keywords: "tmdb tvdb omdb hardcover opensubtitles key token metadata credentials", section: "system", anchor: "api-keys" },
  { label: "Discovery region", keywords: "country region tmdb discover localize popular upcoming", section: "system", anchor: "discovery-region" },
  { label: "Restart", keywords: "restart reboot logs", section: "system", anchor: "restart" },
  { label: "Backups", keywords: "backup restore database copy download upload nightly schedule", section: "system", anchor: "backups" },
  { label: "Status", keywords: "status health version uptime", section: "status", anchor: "status" },
];

// searchSettings finds the cards whose name or keywords contain every word typed, among
// the sections this viewer can open.
export function searchSettings(query: string, visible: readonly string[]): SearchEntry[] {
  const words = query.toLowerCase().split(/\s+/).filter(Boolean);
  if (words.length === 0) return [];
  const sectionLabel = new Map(SECTIONS.map((s) => [s.id, s.label]));
  return SEARCH_INDEX.filter((e) => {
    if (!visible.includes(e.section)) return false;
    const hay = `${e.label} ${e.keywords} ${sectionLabel.get(e.section) ?? ""}`.toLowerCase();
    return words.every((w) => hay.includes(w));
  });
}

// Settings used to be one page of tabs (?tab=system with #api-keys and friends). Those links live on
// in bookmarks and old copy, so each tab and each card anchor has a new home.
const TAB_SECTION: Record<string, SectionId> = { media: "media", library: "library", system: "system", users: "users" };
const ANCHOR_SECTION: Record<string, SectionId> = Object.fromEntries(SEARCH_INDEX.map((e) => [e.anchor, e.section]));

// sectionFromPath is the section named in /settings/<section>[/…], or "" for bare /settings.
export function sectionFromPath(pathname: string): string {
  const m = /^\/settings\/([^/]+)/.exec(pathname);
  if (!m) return "";
  try { return decodeURIComponent(m[1]); } catch { return m[1]; }
}

// settingsRedirect says where a /settings URL should go instead, or null when it already
// names a section this viewer can open. A card anchor wins over ?tab= (it's the more
// specific of the two); a section this viewer can't open falls back to the default, like
// an unknown ?tab= used to. ?tab= is dropped; other query params and the #anchor are kept.
export function settingsRedirect(pathname: string, search: string, hash: string, visible: readonly string[]): string | null {
  const current = sectionFromPath(pathname);
  if (current && visible.includes(current)) return null;
  const params = new URLSearchParams(search);
  const fromAnchor = ANCHOR_SECTION[hash.replace(/^#/, "")];
  const fromTab = TAB_SECTION[params.get("tab") ?? ""];
  const target = [fromAnchor, fromTab].find((s) => s && visible.includes(s)) ?? DEFAULT_SECTION;
  params.delete("tab");
  const qs = params.toString();
  return `/settings/${target}${qs ? `?${qs}` : ""}${hash}`;
}
