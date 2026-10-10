import type { ShellIconName } from "../components/ShellIcons";

// The requester shell's one list of places. It draws the top bar's links (sm and up), the
// phone's bottom tabs and the section title a phone shows in its header, and it is what
// Settings → Users tells admins a requester gets, so none of them can drift apart. Rename
// a tab here and nowhere else.

export interface RequesterNavCtx {
  /** The session is from outside the LAN: no Calendar (the server doesn't allow it there). */
  external: boolean;
  /** The Books module is on: the shelf exists. */
  booksEnabled: boolean;
}

export interface RequesterNavItem {
  key: "discover" | "requests" | "calendar" | "shelf" | "listen" | "me";
  to: string;
  /** In the top bar and as the section title. */
  label: string;
  /** Under the icon in the bottom tab bar, where room is short. */
  tabLabel: string;
  icon: ShellIconName;
  /** A link in the top bar (sm and up). */
  header: boolean;
  /** A tab in the phone's bottom bar. */
  tab: boolean;
}

// REQUESTS_ROUTE: requesters have their own routed /requests page (REQ). While it exists
// Requests takes a phone tab and Calendar moves under Me, keeping the bar at five.
export const REQUESTS_ROUTE = true;

export function requesterNav({ external, booksEnabled }: RequesterNavCtx): RequesterNavItem[] {
  const items: RequesterNavItem[] = [
    { key: "discover", to: "/discover", label: "Discover", tabLabel: "Discover", icon: "discover", header: true, tab: true },
  ];
  if (REQUESTS_ROUTE) {
    items.push({ key: "requests", to: "/requests", label: "Requests", tabLabel: "Requests", icon: "requests", header: true, tab: true });
  }
  if (!external) {
    items.push({ key: "calendar", to: "/calendar", label: "Calendar", tabLabel: "Calendar", icon: "calendar", header: true, tab: !REQUESTS_ROUTE });
  }
  if (booksEnabled) {
    items.push({ key: "shelf", to: "/shelf", label: "My shelf", tabLabel: "Shelf", icon: "shelf", header: true, tab: true });
  }
  items.push(
    { key: "listen", to: "/audiobooks", label: "Audiobooks", tabLabel: "Listen", icon: "listen", header: true, tab: true },
    // Me lives in the avatar menu on wider screens; on a phone it is the last tab.
    { key: "me", to: "/me", label: "Me", tabLabel: "Me", icon: "me", header: false, tab: true },
  );
  return items;
}

// sectionFor is the place an address belongs to ("/discover/movie/12" is Discover), for
// the phone header's title and the active tab.
export function sectionFor(items: RequesterNavItem[], pathname: string): RequesterNavItem | undefined {
  return items.find((i) => pathname === i.to || pathname.startsWith(i.to + "/"));
}
