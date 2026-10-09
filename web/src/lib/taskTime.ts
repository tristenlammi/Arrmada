// Time helpers for the Status page: the tasks API and the health checks send times as RFC
// 3339 strings, unix seconds or milliseconds, and "never" as null, "" or Go's zero time
// (0001-01-01…). Everything here reads all of those, and words the result the way the
// rest of the app does ("20s ago", "in 4 min", "every 15 min").

// toMs is a time as epoch milliseconds, or null when it's unset.
export function toMs(v: string | number | null | undefined): number | null {
  if (v === null || v === undefined || v === "") return null;
  if (typeof v === "number") {
    if (!Number.isFinite(v) || v <= 0) return null;
    return v < 1e12 ? v * 1000 : v; // seconds or milliseconds
  }
  const ms = Date.parse(v);
  // Go's zero time parses to year 1 — before 1970, so negative.
  return Number.isNaN(ms) || ms <= 0 ? null : ms;
}

function span(sec: number): string {
  if (sec < 60) return `${Math.max(1, Math.round(sec))}s`;
  if (sec < 90 * 60) return `${Math.round(sec / 60)} min`;
  if (sec < 36 * 3600) return `${Math.round(sec / 3600)} h`;
  const d = Math.round(sec / 86400);
  return `${d} day${d === 1 ? "" : "s"}`;
}

// ago is "just now", "20s ago", "4 min ago"…; "never" when unset.
export function ago(v: string | number | null | undefined, now = Date.now()): string {
  const ms = toMs(v);
  if (ms === null) return "never";
  const sec = (now - ms) / 1000;
  if (sec < 5) return "just now";
  return `${span(sec)} ago`;
}

// until is "in 4 min", or "now" when it's due or overdue; "—" when unset.
export function until(v: string | number | null | undefined, now = Date.now()): string {
  const ms = toMs(v);
  if (ms === null) return "—";
  const sec = (ms - now) / 1000;
  if (sec < 5) return "now";
  return `in ${span(sec)}`;
}

// every is a task's interval as people say it: "10 s", "15 min", "6 h", "1 day".
export function every(seconds: number): string {
  if (!seconds || seconds <= 0) return "—";
  if (seconds < 60) return `${Math.round(seconds)} s`;
  if (seconds < 3600) return `${+(seconds / 60).toFixed(1)} min`;
  if (seconds < 86400) return `${+(seconds / 3600).toFixed(1)} h`;
  const d = +(seconds / 86400).toFixed(1);
  return `${d} day${d === 1 ? "" : "s"}`;
}

// took is a run's duration: "120 ms", "4.2 s", "3 min".
export function took(ms: number): string {
  if (!ms || ms < 0) return "";
  if (ms < 1000) return `${Math.round(ms)} ms`;
  if (ms < 60_000) return `${(ms / 1000).toFixed(1)} s`;
  return `${Math.round(ms / 60_000)} min`;
}
