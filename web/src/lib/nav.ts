import type { IconName } from "../components/icons";

export interface NavItem {
  to: string;
  label: string;
  icon: IconName;
  end?: boolean;
  /** A short tag shown beside the label, e.g. "Preview" for a module still being hardened. */
  tag?: string;
  /** Shown to admins only (the server refuses everyone else anyway). */
  adminOnly?: boolean;
  /** Belongs to a module an admin can switch off; hidden while it's off. */
  module?: "books" | "music";
  /**
   * A live count beside the label, from the Needs-you feed (lib/useAttention.ts badgeFor):
   * requests waiting, downloads that need a look, held imports, health problems.
   */
  badge?: "requests" | "activity" | "review" | "issues";
  /** A status dot beside the label: the audiobook server's running state. */
  status?: "audiobook-server";
}

export interface NavGroup {
  /** The heading shown in the sidebar; the first group has none. */
  group: string;
  /** What the page breadcrumb calls the group, when that differs from the heading. */
  crumb?: string;
  items: NavItem[];
}

// requesterNav is the requester shell's top bar (UserLayout), and also what Settings →
// Users tells admins a requester gets, so the two can't drift apart. From outside the
// network there's no Calendar (the server doesn't allow it there); Books only while the
// module is on.
export function requesterNav({ external, booksEnabled }: { external: boolean; booksEnabled: boolean }): Pick<NavItem, "to" | "label">[] {
  return [
    { to: "/discover", label: "Discover" },
    { to: "/requests", label: "Requests" },
    ...(external ? [] : [{ to: "/calendar", label: "Calendar" }]),
    ...(booksEnabled ? [{ to: "/books", label: "Books" }] : []),
    { to: "/audiobooks", label: "Audiobooks" },
  ];
}

// The sidebar, grouped by how the app works: what's happening now (Activity), what you
// have (Library), things that work on files (Tools), Plex monitoring, and setup (System).
// Routes don't follow this grouping; only the sidebar and the page breadcrumbs do.
export const NAV: NavGroup[] = [
  {
    group: "",
    crumb: "Home",
    items: [
      { to: "/", label: "Dashboard", icon: "dashboard", end: true },
      { to: "/discover", label: "Discover", icon: "discover" },
      // Pending requests are decided on the Requests page; its pill counts them.
      { to: "/requests", label: "Requests", icon: "requests", badge: "requests" },
    ],
  },
  {
    group: "Activity",
    items: [
      { to: "/downloads", label: "Downloads", icon: "downloads", badge: "activity" },
      { to: "/history", label: "History", icon: "history" },
      { to: "/review", label: "Review", icon: "review", badge: "review" },
      { to: "/blocklist", label: "Blocklist", icon: "blocklist" },
    ],
  },
  {
    group: "Library",
    items: [
      { to: "/movies", label: "Movies", icon: "movies" },
      { to: "/series", label: "Series", icon: "series" },
      { to: "/books", label: "Books", icon: "books", module: "books" },
      { to: "/audiobooks", label: "Audiobooks", icon: "audiobooks", status: "audiobook-server" },
      { to: "/music", label: "Music", icon: "music", module: "music", tag: "Preview" },
      { to: "/calendar", label: "Calendar", icon: "calendar" },
    ],
  },
  {
    group: "Tools",
    items: [
      { to: "/subtitles", label: "Subtitles", icon: "subtitles" },
      { to: "/convert", label: "Convert", icon: "convert" },
    ],
  },
  {
    group: "Plex",
    items: [{ to: "/insights", label: "Insights", icon: "insights" }],
  },
  {
    group: "System",
    items: [
      { to: "/indexers", label: "Indexers", icon: "indexers" },
      { to: "/downloadclients", label: "Download clients", icon: "downloadClients" },
      { to: "/quality", label: "Quality profiles", icon: "quality" },
      { to: "/logs", label: "Logs", icon: "logs", adminOnly: true },
      // Health problems are listed on Settings → Status.
      { to: "/settings", label: "Settings", icon: "settings", badge: "issues" },
    ],
  },
];

export interface NavViewer {
  admin: boolean;
  booksEnabled: boolean;
  musicEnabled: boolean;
}

// visibleNav drops what this viewer shouldn't see: pages of modules an admin switched
// off, and admin-only pages (Logs) for managers. Groups left empty disappear.
export function visibleNav(viewer: NavViewer, nav: NavGroup[] = NAV): NavGroup[] {
  const on = (m: NavItem["module"]) => !m || (m === "books" ? viewer.booksEnabled : viewer.musicEnabled);
  return nav
    .map((g) => ({ ...g, items: g.items.filter((i) => on(i.module) && (viewer.admin || !i.adminOnly)) }))
    .filter((g) => g.items.length > 0);
}

// crumbFor is the page breadcrumb for an address: "<group> / <page>" from the sidebar
// entry the address belongs to, so a page's crumb always matches where it sits in the
// sidebar. The longest matching entry wins ("/movies/12" belongs to Movies); "/" only
// matches the Dashboard itself. Unknown addresses get no crumb.
export function crumbFor(pathname: string): string | undefined {
  if (pathname === "/") return "Home";
  let best: { group: NavGroup; item: NavItem } | undefined;
  for (const group of NAV) {
    for (const item of group.items) {
      if (item.to === "/") continue;
      const hit = pathname === item.to || pathname.startsWith(item.to + "/");
      if (hit && (!best || item.to.length > best.item.to.length)) best = { group, item };
    }
  }
  if (!best) return undefined;
  return `${best.group.crumb ?? best.group.group} / ${best.item.label}`;
}
