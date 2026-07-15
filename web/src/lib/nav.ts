export interface NavItem {
  to: string;
  label: string;
  end?: boolean;
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
    ],
  },
  {
    group: "Library",
    items: [
      { to: "/movies", label: "Movies" },
      { to: "/series", label: "Series" },
      { to: "/books", label: "Books" },
      { to: "/music", label: "Music" },
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
    ],
  },
  {
    group: "System",
    items: [
      { to: "/library", label: "Library" },
      { to: "/indexers", label: "Indexers" },
      { to: "/downloadclients", label: "Download clients" },
      { to: "/quality", label: "Quality profiles" },
      { to: "/settings", label: "Settings" },
    ],
  },
];
