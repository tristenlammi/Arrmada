import { api } from "./api";

// Web Push on this browser: the bell's "Push notifications" switch and the Alerts page's
// "This device" connection both subscribe through here, against the server's VAPID key.

export function pushSupported(): boolean {
  return typeof navigator !== "undefined" && "serviceWorker" in navigator && typeof window !== "undefined" && "PushManager" in window && "Notification" in window;
}

// urlBase64ToUint8Array converts the VAPID public key into the form
// PushManager.subscribe expects.
function urlBase64ToUint8Array(base64: string): Uint8Array {
  const padding = "=".repeat((4 - (base64.length % 4)) % 4);
  const b64 = (base64 + padding).replace(/-/g, "+").replace(/_/g, "/");
  const raw = window.atob(b64);
  const out = new Uint8Array(raw.length);
  for (let i = 0; i < raw.length; i++) out[i] = raw.charCodeAt(i);
  return out;
}

// thisDeviceEndpoint is this browser's current push subscription endpoint, if it has one
// and notifications are allowed.
export async function thisDeviceEndpoint(): Promise<string | null> {
  if (!pushSupported()) return null;
  const reg = await navigator.serviceWorker.ready;
  const sub = await reg.pushManager.getSubscription();
  return sub && Notification.permission === "granted" ? sub.endpoint : null;
}

// subscribeThisDevice asks for permission (it must run from a tap — iOS insists),
// subscribes this browser and registers it with the server for the signed-in user.
// It throws a sentence a person can act on.
export async function subscribeThisDevice(): Promise<void> {
  if (!pushSupported()) throw new Error("This browser can't do push notifications.");
  const perm = await Notification.requestPermission();
  if (perm !== "granted") {
    throw new Error(perm === "denied" ? "Notifications are blocked for this site in your browser settings." : "Permission not granted.");
  }
  const key = await api.pushKey();
  if (!key) throw new Error("Push isn't available on this server.");
  const reg = await navigator.serviceWorker.ready;
  const sub = (await reg.pushManager.getSubscription()) ?? (await reg.pushManager.subscribe({
    userVisibleOnly: true,
    applicationServerKey: urlBase64ToUint8Array(key) as BufferSource,
  }));
  const json = sub.toJSON();
  await api.pushSubscribe({ endpoint: sub.endpoint, keys: { p256dh: json.keys?.p256dh ?? "", auth: json.keys?.auth ?? "" } });
}

// unsubscribeThisDevice drops this browser's subscription, here and on the server.
export async function unsubscribeThisDevice(): Promise<void> {
  if (!pushSupported()) return;
  const reg = await navigator.serviceWorker.ready;
  const sub = await reg.pushManager.getSubscription();
  if (sub) {
    await api.pushUnsubscribe(sub.endpoint).catch(() => {});
    await sub.unsubscribe();
  }
}
