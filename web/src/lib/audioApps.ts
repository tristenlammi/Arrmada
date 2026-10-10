// The ways to listen to Arrmada's audiobooks, for the Apps & devices setup. Honest by
// design: an app is "verified" only once someone has actually listened with it against
// Arrmada on a real device. The others speak the same Audiobookshelf protocol and have
// conversation tests built from their source code (internal/audioserver, AUD-12), but
// until they've been tried on a phone they're shown as not tested yet — never as working.

export type AppPlatform = "android" | "ios" | "any";
export type AppStatus = "verified" | "untested";

export interface AudioApp {
  id: "web" | "lissen" | "abs" | "shelfplayer" | "plappa";
  name: string;
  platform: AppPlatform;
  status: AppStatus;
  /** Where to get it (none for the web player). */
  url?: string;
  /** One line under the name. */
  blurb: string;
}

export const AUDIO_APPS: AudioApp[] = [
  { id: "web", name: "Listen here", platform: "any", status: "verified", blurb: "Arrmada's own player, in this browser or the home-screen app. Nothing to install or sign in to." },
  { id: "lissen", name: "Lissen", platform: "android", status: "verified", url: "https://github.com/GrakovNe/lissen-android", blurb: "An Android app for Audiobookshelf servers. Downloads books for offline listening." },
  { id: "abs", name: "Audiobookshelf app", platform: "ios", status: "untested", url: "https://www.audiobookshelf.org/", blurb: "The official Audiobookshelf app." },
  { id: "shelfplayer", name: "ShelfPlayer", platform: "ios", status: "untested", url: "https://github.com/rasmuslos/ShelfPlayer", blurb: "An iPhone app for Audiobookshelf servers." },
  { id: "plappa", name: "Plappa", platform: "ios", status: "untested", blurb: "An iPhone app for Audiobookshelf servers." },
];

export type DevicePlatform = "ios" | "android" | "other";

/** devicePlatform is what kind of device this browser is on, to show its apps first. */
export function devicePlatform(ua: string, touchMac = false): DevicePlatform {
  if (/iPhone|iPad|iPod/.test(ua) || (touchMac && /Macintosh/.test(ua))) return "ios";
  if (/Android/.test(ua)) return "android";
  return "other";
}

/**
 * appsFor lists the apps for a platform, the recommended one first: on an iPhone that's
 * listening right here (no iPhone app is verified yet); on Android it's Lissen; on a
 * computer, here. Verified apps come before ones not tested yet.
 */
export function appsFor(p: DevicePlatform): AudioApp[] {
  const fits = AUDIO_APPS.filter((a) => a.platform === "any" || p === "other" || a.platform === p);
  const rank = (a: AudioApp) => (a.status === "verified" ? 0 : 1) * 10 + (p === "android" && a.id === "lissen" ? 0 : a.id === "web" ? 1 : 2);
  return [...fits].sort((x, y) => rank(x) - rank(y));
}

/**
 * newDevice finds a device that signed in after waiting began — one that wasn't in the
 * list then (by id, so a phone's clock being off can't matter) — the newest, so the step
 * names what just connected.
 */
export function newDevice<T extends { id: string; created_at: number }>(devices: T[], known: ReadonlySet<string>): T | null {
  const fresh = devices.filter((d) => !known.has(d.id)).sort((a, b) => b.created_at - a.created_at);
  return fresh[0] ?? null;
}
