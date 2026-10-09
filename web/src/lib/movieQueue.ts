import { useEffect, useState } from "react";
import { api, type MovieQueuedRef, type MovieSearchQueueEvent } from "./api";
import type { LiveEvent } from "./useLive";

// Every movie search runs through one throttled queue (two at a time). These helpers word
// it: the toast after a click, and the "Searching 2 · 40 queued" line in the header.

const QUEUE_TOPICS = ["movie.search.queued", "movie.search.started", "movie.search.done"];

/** "Queued (position 4)" for a search that has others ahead of it; "" when it is next. */
export function queuedNote(r: MovieQueuedRef | undefined): string {
  const n = r?.position ?? 0;
  return r?.queued && n > 1 ? `Queued (position ${n})` : "";
}

/** The header line: "Searching 2 · 298 queued", "Searching 1", or "" when the queue is idle. */
export function queueLine(running: number, queued: number): string {
  if (running <= 0 && queued <= 0) return "";
  const parts: string[] = [];
  if (running > 0) parts.push(`Searching ${running}`);
  if (queued > 0) parts.push(`${queued} queued`);
  return parts.join(" · ");
}

/** The counts a movie.search.* event carries, or null for any other event. */
export function queueCounts(ev: LiveEvent | null): { running: number; queued: number } | null {
  if (!ev || !QUEUE_TOPICS.includes(ev.topic)) return null;
  const d = ev.data as Partial<MovieSearchQueueEvent> | null;
  if (!d || typeof d.running !== "number" || typeof d.depth !== "number") return null;
  return { running: d.running, queued: d.depth };
}

/**
 * useMovieSearchQueue is the queue's running and waiting counts: read once on arrival,
 * then kept live by the movie.search.* events (each carries the counts after it).
 */
export function useMovieSearchQueue(last: LiveEvent | null): { running: number; queued: number } {
  const [counts, setCounts] = useState({ running: 0, queued: 0 });
  useEffect(() => {
    let cancelled = false;
    api
      .movieSearchQueue()
      .then((q) => {
        if (!cancelled) setCounts({ running: q.running.length, queued: q.queued.length });
      })
      .catch(() => {});
    return () => {
      cancelled = true;
    };
  }, []);
  useEffect(() => {
    const c = queueCounts(last);
    if (c) setCounts(c);
  }, [last]);
  return counts;
}
