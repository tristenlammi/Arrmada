export interface NavItem {
  to: string;
  label: string;
  end?: boolean;
  /** A short tag shown beside the label, e.g. "Preview" for a module still being hardened. */
  badge?: string;
}

export interface NavGroup {
  group: string;
  items: NavItem[];
}

export const NAV: NavGroup[] = [
  {
    group: "",
    items: [
      { to: "/", label: "Dashboard", end: true },
      { to: "/downloads", label: "Downloads" },
      { to: "/history", label: "History" },
      { to: "/review", label: "Review" },
    ],
  },
  {
    group: "Library",
    items: [
      { to: "/movies", label: "Movies" },
      { to: "/series", label: "Series" },
      { to: "/books", label: "Books" },
      { to: "/music", label: "Music", badge: "Preview" },
    ],
  },
  {
    group: "Services",
    items: [
      { to: "/discover", label: "Discover" },
      { to: "/calendar", label: "Calendar" },
      { to: "/subtitles", label: "Subtitles" },
      { to: "/convert", label: "Convert" },
      { to: "/insights", label: "Insights" },
      { to: "/audiobooks", label: "Audiobooks" },
    ],
  },
  {
    group: "System",
    items: [
      { to: "/indexers", label: "Indexers" },
      { to: "/downloadclients", label: "Download clients" },
      { to: "/quality", label: "Quality profiles" },
      { to: "/logs", label: "Logs" },
      { to: "/settings", label: "Settings" },
    ],
  },
];
