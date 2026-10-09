// Words and colours for an indexer's health on the Indexers page (see IndexerStatus).
import type { IndexerStatus, ProwlarrSyncResult } from "./api";
import { ago, toMs } from "./taskTime";

// The row's dot: green working, amber failing (sweeps still ask it), red paused after
// repeated failures, grey unused or disabled. label is the dot's tooltip.
export const INDEXER_DOT: Record<IndexerStatus["state"], { color: string; label: string }> = {
  ok: { color: "var(--good)", label: "Working" },
  failing: { color: "var(--avoid)", label: "Failing — still searched" },
  backing_off: { color: "var(--reject)", label: "Paused after repeated failures — automatic searches skip it" },
  unknown: { color: "var(--ink-faint)", label: "Not used yet" },
  disabled: { color: "var(--ink-faint)", label: "Disabled" },
};

// hhmm is a time as the server's messages word it: "14:02" (local, 24-hour).
export function hhmm(v?: string | null): string {
  const ms = toMs(v);
  if (ms === null) return "—";
  const d = new Date(ms);
  return `${String(d.getHours()).padStart(2, "0")}:${String(d.getMinutes()).padStart(2, "0")}`;
}

export interface StatusLine {
  text: string;
  color: string;
}

// indexerStatusLine is the one line under a row: when it last worked and how much it was
// used ("Last OK 3 min ago · 214 searches, 3 failed (24h)"), or since when it has been
// failing, why, and when it is next tried ("Failing since 14:02: login failed · next try
// 15:00"). A disabled indexer has none — its "disabled" tag says it.
export function indexerStatusLine(s: IndexerStatus, now = Date.now()): StatusLine | null {
  switch (s.state) {
    case "disabled":
      return null;
    case "unknown":
      return { text: "Not used yet", color: "var(--ink-dim)" };
    case "ok": {
      let usage = "";
      if (s.queries_24h > 0) {
        usage = ` · ${s.queries_24h} search${s.queries_24h === 1 ? "" : "es"}`;
        if (s.failures_24h > 0) usage += `, ${s.failures_24h} failed`;
        usage += " (24h)";
      }
      return { text: `Last OK ${ago(s.last_ok_at, now)}${usage}`, color: "var(--ink-dim)" };
    }
  }
  let text = `Failing since ${hhmm(s.failing_since ?? s.last_error_at)}`;
  if (s.last_error) text += `: ${s.last_error}`;
  if (s.state === "backing_off" && s.backoff_until) text += ` · next try ${hhmm(s.backoff_until)}`;
  return { text, color: s.state === "backing_off" ? "var(--reject-text)" : "var(--avoid-text)" };
}

const plural = (n: number, one: string, many = `${one}s`) => `${n} ${n === 1 ? one : many}`;

// prowlarrSyncMessage says what a Prowlarr sync changed: "Added 2, updated 1, turned off
// 1 (disabled or removed in Prowlarr). Skipped 1 usenet indexer: Arrmada has no usenet
// download client." It claims FlareSolverr only when Prowlarr itself lists the proxy.
export function prowlarrSyncMessage(r: ProwlarrSyncResult): string {
  const parts: string[] = [];
  if (r.added) parts.push(`added ${r.added}`);
  if (r.updated) parts.push(`updated ${r.updated}`);
  if (r.reenabled) parts.push(`turned ${r.reenabled} back on`);
  if (r.disabled) parts.push(`turned off ${r.disabled} (disabled or removed in Prowlarr)`);
  let msg = parts.length > 0 ? `${parts.join(", ")}.` : "Nothing changed.";
  msg = msg.charAt(0).toUpperCase() + msg.slice(1);
  if (r.unchanged && parts.length > 0) msg += ` ${plural(r.unchanged, "indexer")} unchanged.`;
  if (r.skipped_usenet) msg += ` Skipped ${plural(r.skipped_usenet, "usenet indexer")}: Arrmada has no usenet download client.`;
  if (r.flaresolverr_ready) msg += " Prowlarr has a FlareSolverr proxy for Cloudflare trackers.";
  for (const n of r.notes ?? []) msg += ` ${n}`;
  return msg;
}
