import { api } from "./api";

// Web Push on this browser: the Me page's "Get notified" switch, the prompt after a first
// request and the Alerts page's "This device" connection all go through here, against the
// server's VAPID key. Only lazy chunks import it, so it stays out of the first load.

export function pushSupported(): boolean {
  return typeof navigator !== "undefined" && "serviceWorker" in navigator && typeof window !== "undefined" && "PushManager" in window && "Notification" in window;
}

// PushEnvironment is what this browser can do about push, before asking the server.
export interface PushEnvironment {
  /** https (or localhost). A plain-http LAN address gets no service worker and no push. */
  secure: boolean;
  supported: boolean;
  /** An iPhone or iPad, where push only works from the Home Screen app. */
  ios: boolean;
  /** Running as the installed app rather than a browser tab. */
  standalone: boolean;
}

export function pushEnvironment(): PushEnvironment {
  const nav = typeof navigator !== "undefined" ? navigator : undefined;
  const ua = nav?.userAgent ?? "";
  // iPadOS reports itself as a Mac; a touch screen gives it away.
  const ios = /iPad|iPhone|iPod/.test(ua) || (/Macintosh/.test(ua) && (nav?.maxTouchPoints ?? 0) > 1);
  const standalone = typeof window !== "undefined" && (
    (typeof window.matchMedia === "function" && window.matchMedia("(display-mode: standalone)").matches) ||
    (nav as Navigator & { standalone?: boolean } | undefined)?.standalone === true
  );
  return { secure: typeof window !== "undefined" && window.isSecureContext, supported: pushSupported(), ios, standalone };
}

// PushStatus is one plain answer about push on this device:
//   unavailable   — the server has no push key, so nobody can turn it on;
//   insecure      — opened over plain http, where browsers allow no push at all;
//   needs-install — an iPhone in Safari: only the Home Screen app can get pushes;
//   unsupported   — an old browser without push;
//   blocked       — the person (or the browser) said no to notifications for this site;
//   off / on      — whether this device is subscribed.
export type PushStatus = "unavailable" | "insecure" | "needs-install" | "unsupported" | "blocked" | "off" | "on";

// pushStatus works out this device's PushStatus. It never waits on the service worker
// (it may not be running yet, or ever, on an http address), only on the server's key.
export async function pushStatus(): Promise<PushStatus> {
  const key = await api.pushKey().catch(() => "");
  if (!key) return "unavailable";
  const env = pushEnvironment();
  if (!env.secure) return "insecure";
  if (env.ios && !env.standalone) return "needs-install";
  if (!env.supported) return "unsupported";
  if (Notification.permission === "denied") return "blocked";
  return (await thisDeviceEndpoint()) ? "on" : "off";
}

// urlBase64ToUint8Array converts the VAPID public key into the form
// PushManager.subscribe expects.
export function urlBase64ToUint8Array(base64: string): Uint8Array {
  const padding = "=".repeat((4 - (base64.length % 4)) % 4);
  const b64 = (base64 + padding).replace(/-/g, "+").replace(/_/g, "/");
  const raw = window.atob(b64);
  const out = new Uint8Array(raw.length);
  for (let i = 0; i < raw.length; i++) out[i] = raw.charCodeAt(i);
  return out;
}

// registration is the app's service worker registration if there is one, without waiting
// for one to appear (navigator.serviceWorker.ready never settles when none registers).
async function registration(): Promise<ServiceWorkerRegistration | undefined> {
  if (!pushSupported()) return undefined;
  return navigator.serviceWorker.getRegistration().catch(() => undefined);
}

// thisDeviceEndpoint is this browser's current push subscription endpoint, if it has one
// and notifications are allowed.
export async function thisDeviceEndpoint(): Promise<string | null> {
  const reg = await registration();
  const sub = await reg?.pushManager.getSubscription();
  return sub && Notification.permission === "granted" ? sub.endpoint : null;
}

// readyWorker waits for the service worker, but not forever: on a page where it never
// registered, a person would otherwise watch a spinner that never stops.
async function readyWorker(): Promise<ServiceWorkerRegistration> {
  let timer = 0;
  const timeout = new Promise<never>((_, reject) => {
    timer = window.setTimeout(() => reject(new Error("The app's background worker isn't running — reload the page and try again.")), 10_000);
  });
  try {
    return await Promise.race([navigator.serviceWorker.ready, timeout]);
  } finally {
    window.clearTimeout(timer);
  }
}

// subscribeThisDevice (enablePush) asks for permission FIRST, before any other await —
// iOS only shows the prompt from inside the tap — then subscribes this browser and
// registers it with the server for the signed-in user. It throws a sentence a person can
// act on.
export async function subscribeThisDevice(): Promise<void> {
  if (!pushSupported()) throw new Error("This browser can't do push notifications.");
  const perm = await Notification.requestPermission();
  if (perm !== "granted") {
    throw new Error(perm === "denied" ? "Notifications are blocked for this site in your browser settings." : "Permission not granted.");
  }
  const key = await api.pushKey();
  if (!key) throw new Error("Push isn't available on this server.");
  const reg = await readyWorker();
  const sub = (await reg.pushManager.getSubscription()) ?? (await reg.pushManager.subscribe({
    userVisibleOnly: true,
    applicationServerKey: urlBase64ToUint8Array(key) as BufferSource,
  }));
  const json = sub.toJSON();
  await api.pushSubscribe({ endpoint: sub.endpoint, keys: { p256dh: json.keys?.p256dh ?? "", auth: json.keys?.auth ?? "" } });
}
export const enablePush = subscribeThisDevice;

// unsubscribeThisDevice (disablePush) drops this browser's subscription, here and on the
// server.
export async function unsubscribeThisDevice(): Promise<void> {
  const reg = await registration();
  const sub = await reg?.pushManager.getSubscription();
  if (sub) {
    await api.pushUnsubscribe(sub.endpoint).catch(() => {});
    await sub.unsubscribe();
  }
}
export const disablePush = unsubscribeThisDevice;

// deviceName is what the status line calls this device: "On for this iPhone".
export function deviceName(env: PushEnvironment = pushEnvironment()): string {
  const ua = typeof navigator !== "undefined" ? navigator.userAgent : "";
  if (env.ios) return /iPad|Macintosh/.test(ua) ? "this iPad" : "this iPhone";
  if (/Android/.test(ua)) return /Mobile/.test(ua) ? "this phone" : "this tablet";
  return "this browser";
}
