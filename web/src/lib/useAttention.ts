import { useEffect, useSyncExternalStore } from "react";
import { api, SIGNED_OUT_EVENT, type Attention, type AttentionCounts } from "./api";
import type { NavItem } from "./nav";
import { usePoll } from "./usePoll";

// The Needs-you feed, shared by every part of the staff shell that shows it: the sidebar
// badges, the mobile menu dot and the Dashboard card all read one answer, and one poller
// (mounted once, in the staff layout) keeps it fresh. The server keeps the snapshot in
// memory, so the poll is cheap; a hidden tab doesn't poll at all.
//
// A small store of its own rather than lib/query: the shell is part of every first load,
// requesters' included, and this keeps the query cache out of it.

export const ATTENTION_POLL_MS = 30_000;

let current: Attention | undefined;
let inflight: Promise<void> | null = null;
const listeners = new Set<() => void>();

function publish(next: Attention | undefined) {
  current = next;
  listeners.forEach((fn) => fn());
}

/** Fetches the snapshot (joining one already on its way). Never rejects. */
export function loadAttention(): Promise<void> {
  if (!inflight) {
    inflight = api.attention().then(publish, () => {}).finally(() => { inflight = null; });
  }
  return inflight;
}

if (typeof window !== "undefined") window.addEventListener(SIGNED_OUT_EVENT, () => publish(undefined));

function subscribe(fn: () => void) {
  listeners.add(fn);
  return () => { listeners.delete(fn); };
}

/** The poller. Mount exactly once, in the staff layout. */
export function useAttentionPoll() {
  usePoll(loadAttention, ATTENTION_POLL_MS);
  // And the live line that asks again the moment the counts move (lib/attentionLive).
  useEffect(() => { void import("./attentionLive").then((m) => m.startAttentionLive(), () => {}); }, []);
}

/** The latest Needs-you snapshot; undefined until the first answer. Never fetches itself. */
export function useAttention(): Attention | undefined {
  return useSyncExternalStore(subscribe, () => current);
}

// After something the feed reports on changes (a request approved, a review resolved),
// the server refreshes its snapshot about a second later; asking again then makes the
// badge follow at once rather than at the next 30-second poll. Several calls in a row
// make one request, and nothing is asked where nobody shows the answer (a requester).
let refreshTimer: ReturnType<typeof setTimeout> | undefined;
export function refreshAttention(delayMs = 1500) {
  clearTimeout(refreshTimer);
  refreshTimer = setTimeout(() => {
    refreshTimer = undefined;
    if (listeners.size > 0) void loadAttention();
  }, delayMs);
}

export interface NavBadge {
  count: number;
  /** accent: something to do; reject: something is broken; avoid: something is degraded. */
  tone: "accent" | "reject" | "avoid";
  label: string; // for screen readers and the tooltip
}

const plural = (n: number, one: string, many: string) => `${n} ${n === 1 ? one : many}`;

// badgeFor is the count pill beside a sidebar entry, or null when there's nothing (pills
// are hidden at zero).
export function badgeFor(badge: NonNullable<NavItem["badge"]>, c: AttentionCounts | undefined): NavBadge | null {
  if (!c) return null;
  switch (badge) {
    case "requests":
      return c.requests > 0 ? { count: c.requests, tone: "accent", label: plural(c.requests, "request waiting for approval", "requests waiting for approval") } : null;
    case "review":
      return c.reviews > 0 ? { count: c.reviews, tone: "accent", label: plural(c.reviews, "import needs review", "imports need review") } : null;
    case "activity": {
      const n = c.downloads + c.imports;
      return n > 0 ? { count: n, tone: "accent", label: plural(n, "download needs a look", "downloads need a look") } : null;
    }
    case "issues":
      if (c.health_errors > 0) return { count: c.health_errors, tone: "reject", label: plural(c.health_errors, "health error", "health errors") };
      return c.health > 0 ? { count: c.health, tone: "avoid", label: plural(c.health, "health warning", "health warnings") } : null;
  }
  return null;
}
