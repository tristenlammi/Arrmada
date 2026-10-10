// Every deep link that copy points at lives here, and so does every page name a sentence
// uses. When a page or a setting moves, change it here and every banner, toast and hint
// follows — rather than hunting the old address down string by string.
export const LINKS = {
  apiKeys: "/settings/system#api-keys",
  diskGuard: "/settings/downloads#disk-guard",
  recycleBin: "/settings/downloads#recycle-bin",
  libraryFolders: "/settings/library#media-folders",
  users: "/settings/users",
  plexConnection: "/insights?tab=settings",
  downloadsSearching: "/downloads?tab=searching",
  downloadProblems: "/downloads?show=problems",
  downloadClients: "/downloadclients",
  indexers: "/indexers",
  subtitlesSettings: "/subtitles?tab=settings",
  convertSettings: "/convert?tab=settings",
  requests: "/requests",
  requestsWaiting: "/requests?tab=needs",
  backups: "/settings/system#backups",
  audiobookServer: "/audiobooks",
  status: "/settings/status",
  tasks: "/settings/status#tasks",
  review: "/review",
} as const;

// fixLink is where a health warning sends you: the LINKS entry the server named, else the
// server's own path. Only in-app paths are followed; anything else gets no link.
export function fixLink(w: { link?: string; link_key?: string }): string | undefined {
  const known = w.link_key && Object.prototype.hasOwnProperty.call(LINKS, w.link_key)
    ? LINKS[w.link_key as keyof typeof LINKS]
    : undefined;
  const to = known ?? w.link;
  return to && to.startsWith("/") && !to.startsWith("//") ? to : undefined;
}

// Page names as they appear in the sidebar, for sentences like "follow it in Downloads".
export const PAGE = {
  downloads: "Downloads",
  settings: "Settings",
  insights: "Insights",
} as const;
