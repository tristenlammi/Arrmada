export interface NavItem {
  to: string;
  label: string;
  end?: boolean;
  /** A short tag shown beside the label, e.g. "Preview" for a module still being hardened. */
  badge?: string;
  /** Shown to admins only (the server refuses everyone else anyway). */
  adminOnly?: boolean;
}

export interface NavGroup {
  group: string;
  items: NavItem[];
}

// requesterNav is the requester shell's top bar (UserLayout), and also what Settings →
// Users tells admins a requester gets, so the two can't drift apart. From outside the
// network there's no Calendar (the server doesn't allow it there); Books only while the
// module is on.
export function requesterNav({ external, booksEnabled }: { external: boolean; booksEnabled: boolean }): NavItem[] {
  return [
    { to: "/discover", label: "Discover" },
    ...(external ? [] : [{ to: "/calendar", label: "Calendar" }]),
    ...(booksEnabled ? [{ to: "/books", label: "Books" }] : []),
    { to: "/audiobooks", label: "Audiobooks" },
  ];
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
      { to: "/logs", label: "Logs", adminOnly: true },
      { to: "/settings", label: "Settings" },
    ],
  },
];
