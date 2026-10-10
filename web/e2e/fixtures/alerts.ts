import type { AlertCatalog, AlertDelivery, NotificationConn } from "../../src/lib/api";
import { NOW } from "./clock";

const sec = (ms: number) => Math.floor(ms / 1000);

// Settings → Alerts: one Discord channel whose last alert failed, and the owner's push
// connection. The server never sends a URL, only a hint of it.
export const connections: { notifications: NotificationConn[] } = {
  notifications: [
    {
      id: 1, name: "Family Discord", kind: "", enabled: true, events: ["movie.imported", "release.grabbed"],
      url_hint: "discord://••••OKEN", url_set: true,
      last_status: "failed", last_error: "apprise: exit status 1 (404 Page not found)", last_sent_at: sec(NOW) - 3600,
    },
    {
      id: 2, name: "This device", kind: "webpush", enabled: true, events: ["movie.imported"], config: { user_id: 1 },
      url_hint: "", url_set: false, last_status: "sent", last_sent_at: sec(NOW) - 120,
    },
  ],
};

export const catalog: AlertCatalog = {
  groups: [
    { key: "needs_you", label: "Needs you" },
    { key: "requests", label: "Requests" },
    { key: "downloads", label: "Downloads" },
    { key: "library", label: "Library" },
    { key: "plex", label: "Plex" },
    { key: "convert", label: "Convert" },
  ],
  events: [
    { key: "request.auto_approved", group: "requests", label: "Auto-approved request", hint: "Someone allowed to auto-approve asked for something.", default_on: false },
    { key: "release.grabbed", group: "downloads", label: "Grabbed", hint: "A release was sent to the download client.", default_on: false },
    { key: "movie.imported", group: "library", label: "Movie imported", hint: "A movie file landed in the library.", default_on: true },
    { key: "episodes.imported", group: "library", label: "Episodes imported", hint: "New episodes landed.", default_on: true },
    { key: "book.imported", group: "library", label: "Book imported", hint: "An ebook or audiobook landed.", default_on: true, module: "books" },
    { key: "music.imported", group: "library", label: "Album imported", hint: "Tracks of an album landed.", default_on: false, module: "music" },
    { key: "plex.stream.started", group: "plex", label: "Stream started", hint: "Someone started playing.", default_on: false },
    { key: "plex.buffering", group: "plex", label: "Buffering", hint: "A stream started buffering.", default_on: false },
  ],
};

export const deliveries: { deliveries: AlertDelivery[] } = {
  deliveries: [
    { id: 9, event_key: "movie.imported", title: "Imported", body: "📥 The Cartographer", status: "failed", attempts: 4, last_error: "apprise: exit status 1 (404 Page not found)", created_at: sec(NOW) - 7200, next_attempt_at: 0, sent_at: 0 },
    { id: 8, event_key: "test", title: "Arrmada", body: "✅ Test notification — this connection works.", status: "sent", attempts: 1, last_error: "", created_at: sec(NOW) - 86400, next_attempt_at: 0, sent_at: sec(NOW) - 86400 },
  ],
};
