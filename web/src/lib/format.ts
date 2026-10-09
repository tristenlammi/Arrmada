// Shared formatters. Pages each grew their own copy (fmtSize, gb, bytes, ago, eta…)
// with slightly different rules; these are the one version new code should use.
// Before switching an old call site over, check what it printed: a few want "0 B" or
// an empty string for zero rather than the dash used here.

const UNITS = ["B", "KB", "MB", "GB", "TB", "PB"];

// formatBytes is 1024-based: "512 B", "1.0 KB", "1.5 GB". Nothing to show (zero,
// negative, NaN) prints a dash, as History does, so an unknown size never reads "0 B".
export function formatBytes(n: number | null | undefined, digits = 1): string {
  if (n == null || !Number.isFinite(n) || n <= 0) return "—";
  const i = Math.min(Math.floor(Math.log(n) / Math.log(1024)), UNITS.length - 1);
  return `${(n / 1024 ** i).toFixed(i === 0 ? 0 : digits)} ${UNITS[i]}`;
}

// formatDuration turns seconds into the largest two units: "45s", "12m", "3h 40m",
// "2d 4h". Zero or nothing prints a dash.
export function formatDuration(sec: number | null | undefined): string {
  if (sec == null || !Number.isFinite(sec) || sec <= 0) return "—";
  const s = Math.floor(sec);
  if (s < 60) return `${s}s`;
  const m = Math.floor(s / 60);
  if (m < 60) return `${m}m`;
  const h = Math.floor(m / 60);
  if (h < 24) return m % 60 ? `${h}h ${m % 60}m` : `${h}h`;
  const d = Math.floor(h / 24);
  return h % 24 ? `${d}d ${h % 24}h` : `${d}d`;
}

// formatAgo says how long ago a millisecond timestamp was: "just now", "5m ago",
// "3h ago", "2d ago". A time in the future (clock skew) is "just now"; no time at
// all is "never".
export function formatAgo(ms: number | null | undefined, now = Date.now()): string {
  if (!ms || !Number.isFinite(ms)) return "never";
  const s = Math.max(0, Math.floor((now - ms) / 1000));
  if (s < 60) return "just now";
  if (s < 3600) return `${Math.floor(s / 60)}m ago`;
  if (s < 86400) return `${Math.floor(s / 3600)}h ago`;
  return `${Math.floor(s / 86400)}d ago`;
}

// formatEta is how long a download has left, for a sentence like "12m left":
// "under a minute", "12m", "3h 5m", "2d 4h".
export function formatEta(sec: number): string {
  if (sec < 60) return "under a minute";
  const m = Math.round(sec / 60);
  if (m < 60) return `${m}m`;
  const h = Math.floor(m / 60);
  return h < 24 ? `${h}h ${m % 60}m` : `${Math.floor(h / 24)}d ${h % 24}h`;
}

// formatCheckDay is the day a book's next search falls on, in the viewer's own locale
// ("Tue 14 Oct" in en-GB). The server only ever sends the time; the words are ours.
// A time already passed — the book is due and waiting for the sweep — or no time at
// all reads "soon".
export function formatCheckDay(iso: string | null | undefined, now = Date.now(), locale?: string): string {
  const ms = iso ? Date.parse(iso) : NaN;
  if (!Number.isFinite(ms) || ms <= now) return "soon";
  return new Date(ms).toLocaleDateString(locale, { weekday: "short", day: "numeric", month: "short" }).replace(/,/g, "");
}

// notFoundYet is the line for a wanted book the searches keep missing: "Not found yet ·
// next check Tue 14 Oct", or "Not found yet — last checked 3d ago, next check Tue 14 Oct"
// when the last check is passed in.
export function notFoundYet(next: string | null | undefined, last?: string | null, now = Date.now()): string {
  const lastMs = last ? Date.parse(last) : NaN;
  const lead = Number.isFinite(lastMs) ? ` — last checked ${formatAgo(lastMs, now)}, ` : " · ";
  return `Not found yet${lead}next check ${formatCheckDay(next, now)}`;
}
