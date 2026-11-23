// The media servers Arrmada hands the library to, and how each is drawn: its name, its
// brand fill and the ink that reads on that fill (index.css --plex / --jellyfin tokens,
// contrast-checked in contrast.test.ts).

export type MediaServerKind = "plex" | "jellyfin";

export const MEDIA_SERVERS: readonly MediaServerKind[] = ["plex", "jellyfin"];

export const SERVER_LABEL: Record<MediaServerKind, string> = { plex: "Plex", jellyfin: "Jellyfin" };

/** The brand fill: Plex's amber, Jellyfin's purple-to-blue. Use with `background`. */
export const SERVER_FILL: Record<MediaServerKind, string> = { plex: "var(--plex)", jellyfin: "var(--jellyfin)" };

/** The words and mark drawn on SERVER_FILL. */
export const SERVER_INK: Record<MediaServerKind, string> = { plex: "var(--plex-ink)", jellyfin: "var(--jellyfin-ink)" };

// Emby, the server Jellyfin grew out of, connects through the Jellyfin slot (same settings,
// recorded plays and linked accounts). When it does, the slot is named and drawn as Emby
// everywhere these tables are read. The app sets it from /status (and the connection card
// after a save); nothing else changes.
let embyInSlot = false;

/** Says which server the Jellyfin slot is connected to ("emby", else Jellyfin). */
export function setJellyfinSlotKind(kind: string | undefined | null): void {
  embyInSlot = kind === "emby";
  SERVER_LABEL.jellyfin = embyInSlot ? "Emby" : "Jellyfin";
  SERVER_FILL.jellyfin = embyInSlot ? "var(--emby)" : "var(--jellyfin)";
  SERVER_INK.jellyfin = embyInSlot ? "var(--emby-ink)" : "var(--jellyfin-ink)";
}

/** Whether the Jellyfin slot is connected to an Emby server. */
export function jellyfinSlotIsEmby(): boolean {
  return embyInSlot;
}

export function isMediaServer(s: string | undefined | null): s is MediaServerKind {
  return s === "plex" || s === "jellyfin";
}

/** One server's Watch link for a title (the API's watch_links rows). label names the
 *  server when it isn't one this build knows. */
export interface WatchLink {
  server: string;
  label?: string;
  url: string;
}

/** A record's Watch links: its watch_links, or — from a server older than Jellyfin
 *  support — its plex_url as the one Plex link. */
export function watchLinksOf(x: { watch_links?: readonly WatchLink[]; plex_url?: string } | null | undefined): readonly WatchLink[] {
  if (!x) return [];
  if (x.watch_links?.length) return x.watch_links;
  return x.plex_url ? [{ server: "plex", label: "Plex", url: x.plex_url }] : [];
}

/** The first usable Watch link (the viewer's own server, as the API orders them) as an
 *  href and the server's name, for a row with room for one button. */
export function firstWatch(x: Parameters<typeof watchLinksOf>[0]): { href: string; label: string } | null {
  for (const l of watchLinksOf(x)) {
    const href = safeWatchURL(l.url);
    if (href) return { href, label: watchLabel(l) };
  }
  return null;
}

/** The name a Watch link says ("Plex"), from the server when it's known. */
export function watchLabel(l: WatchLink): string {
  return isMediaServer(l.server) ? SERVER_LABEL[l.server] : l.label || l.server;
}

/** The link only when it's a plain http(s) address: anything else (javascript:, data:)
 *  never becomes an href. */
export function safeWatchURL(url: string): string | null {
  try {
    const u = new URL(url);
    return u.protocol === "http:" || u.protocol === "https:" ? u.href : null;
  } catch {
    return null;
  }
}
