import { NAV } from "../../lib/nav";
import { requesterNav } from "../../lib/requesterNav";
import { SECTIONS } from "./sections";

// What each role gets, worded for Settings → Users, built from the same lists the app is
// drawn from so the description moves when the pages do.

// "a, b and c"
export function andList(xs: string[]): string {
  return xs.length <= 1 ? xs.join("") : `${xs.slice(0, -1).join(", ")} and ${xs[xs.length - 1]}`;
}

// requesterPages is a requester's top bar at home, with any page they lose from outside
// the network marked as such: "Discover, Calendar (at home only), My shelf, Audiobooks".
export function requesterPages(booksEnabled: boolean): string {
  const outside = new Set(requesterNav({ external: true, booksEnabled }).map((n) => n.to));
  return requesterNav({ external: false, booksEnabled })
    .filter((n) => n.header)
    .map((n) => (outside.has(n.to) ? n.label : `${n.label} (at home only)`))
    .join(", ");
}

// managerExceptions is what a manager can't open: the admin-only sidebar pages, the
// audiobook server settings (an admin-only card on Audiobooks) and the admin-only Settings
// sections. They can see the library folders but only an admin can move them.
export function managerExceptions(): string {
  const pages = NAV.flatMap((g) => g.items).filter((i) => i.adminOnly).map((i) => i.label);
  const sections = SECTIONS.filter((s) => s.adminOnly).map((s) => s.label);
  return `the whole console except ${andList([...pages, "the audiobook server settings", `Settings → ${sections.join(", ")}`])}. They can see the library folders but not move them.`;
}
