// Every deep link that copy points at lives here, and so does every page name a sentence
// uses. When a page or a setting moves, change it here and every banner, toast and hint
// follows — rather than hunting the old address down string by string.
export const LINKS = {
  apiKeys: "/settings?tab=system#api-keys",
  diskGuard: "/settings?tab=system#disk-guard",
  recycleBin: "/settings?tab=system#recycle-bin",
  libraryFolders: "/settings?tab=library#media-folders",
  users: "/settings?tab=users",
  plexConnection: "/insights?tab=settings",
  downloadsSearching: "/downloads?tab=searching",
  downloadClients: "/downloadclients",
  indexers: "/indexers",
  subtitlesSettings: "/subtitles?tab=settings",
  convertSettings: "/convert?tab=settings",
  requests: "/discover",
} as const;

// Page names as they appear in the sidebar, for sentences like "follow it in Downloads".
export const PAGE = {
  downloads: "Downloads",
  settings: "Settings",
  insights: "Insights",
} as const;
