import { useEffect, useRef, useState } from "react";
import { api, SIGNED_OUT_EVENT, type Job, type JobStatus } from "./api";
import type { LiveEvent } from "./useLive";

// Following the background job a button started (a search, a scan, an import), so the
// button can say what actually happened instead of "searching" and silence.
//
// This keeps its own wait loop rather than usePoll: a job.updated event has to cut the
// current wait short (usePoll has no "run now"), and following ends by itself — on a
// finished job, or after five minutes — with one onDone. It follows usePoll's rules
// otherwise: one request at a time, none while the tab is hidden, a check on return, and
// it stops on sign-out.

export const JOB_POLL_MS = 1500; // between checks when no live socket is connected
export const JOB_POLL_LIVE_MS = 5000; // a backstop when job.updated events arrive live
export const JOB_GIVE_UP_MS = 5 * 60 * 1000; // a button stops watching after five minutes

export function jobDone(status: JobStatus | undefined | null): boolean {
  return !!status && status !== "queued" && status !== "running";
}

// A job that ended badly, for choosing a toast's error look.
export function jobFailed(job: Job): boolean {
  return job.status === "failed" || job.status === "panicked" || job.status === "interrupted";
}

// The sentence a page shows when its job finishes: the job's own message, or the error.
export function jobToast(job: Job, fallback = "Done."): string {
  if (jobFailed(job)) return job.error || "That didn't work.";
  if (job.status === "cancelled") return "Stopped.";
  return job.message || fallback;
}

export interface FollowDeps {
  get: (id: number) => Promise<Job>;
  sleep: (ms: number) => Promise<void>;
  now: () => number;
  hidden: () => boolean; // a hidden tab doesn't poll
  pollMs: () => number;
  giveUpMs?: number;
  stopped: () => boolean; // the page went away or started following another job
  onUpdate?: (job: Job) => void;
}

// followJob checks a job until it ends, the caller stops, or giveUpMs passes. It returns
// the finished job, or null when it stopped first. A failed read is retried, not fatal:
// the job carries on server-side whatever the page sees.
export async function followJob(id: number, d: FollowDeps): Promise<Job | null> {
  const start = d.now();
  const limit = d.giveUpMs ?? JOB_GIVE_UP_MS;
  for (;;) {
    if (d.stopped()) return null;
    if (!d.hidden()) {
      try {
        const job = await d.get(id);
        if (d.stopped()) return null;
        d.onUpdate?.(job);
        if (jobDone(job.status)) return job;
      } catch {
        /* the next check tries again */
      }
    }
    if (d.now() - start >= limit) return null;
    await d.sleep(d.pollMs());
  }
}

export interface JobView {
  job: Job | null;
  status: JobStatus | null;
  message: string;
  result: unknown;
  error: string;
  running: boolean; // being followed and not finished yet
}

/**
 * useJob follows job jobId until it finishes, then calls onDone once with it. It checks
 * every 1.5 s, or — when the page's live socket is connected — wakes on job.updated and
 * keeps a slow backstop check. Hidden tabs don't poll; it gives up after five minutes.
 * Leaving the page stops watching, never the job.
 */
export function useJob(
  jobId?: number | null,
  opts: { live?: { connected: boolean; last: LiveEvent | null }; onDone?: (job: Job) => void } = {},
): JobView {
  const [job, setJob] = useState<Job | null>(null);
  const [following, setFollowing] = useState(false);
  const onDone = useRef(opts.onDone);
  const connected = useRef(!!opts.live?.connected);
  const wake = useRef<(() => void) | null>(null);
  useEffect(() => {
    onDone.current = opts.onDone;
    connected.current = !!opts.live?.connected;
  });

  useEffect(() => {
    setJob(null);
    if (!jobId) {
      setFollowing(false);
      return;
    }
    let stopped = false;
    setFollowing(true);
    const sleep = (ms: number) =>
      new Promise<void>((resolve) => {
        const t = setTimeout(() => { wake.current = null; resolve(); }, ms);
        wake.current = () => { clearTimeout(t); wake.current = null; resolve(); };
      });
    followJob(jobId, {
      get: api.job,
      sleep,
      now: Date.now,
      hidden: () => document.hidden,
      pollMs: () => (connected.current ? JOB_POLL_LIVE_MS : JOB_POLL_MS),
      stopped: () => stopped,
      onUpdate: (j) => setJob(j),
    }).then((done) => {
      if (stopped) return;
      setFollowing(false);
      if (done) onDone.current?.(done);
    });
    // A tab coming back into view checks at once rather than at the next tick.
    const onVis = () => { if (!document.hidden) wake.current?.(); };
    const onSignedOut = () => { stopped = true; wake.current?.(); };
    document.addEventListener("visibilitychange", onVis);
    window.addEventListener(SIGNED_OUT_EVENT, onSignedOut);
    return () => {
      stopped = true;
      document.removeEventListener("visibilitychange", onVis);
      window.removeEventListener(SIGNED_OUT_EVENT, onSignedOut);
      wake.current?.();
    };
  }, [jobId]);

  // A job.updated for this job (staff sockets get them) wakes the follower, which then
  // reads the finished job — the event itself carries no result.
  const last = opts.live?.last;
  useEffect(() => {
    if (!jobId || !last || last.topic !== "job.updated") return;
    const d = last.data as { id?: number; status?: JobStatus } | null;
    if (d?.id === jobId && jobDone(d.status)) wake.current?.();
  }, [last, jobId]);

  return {
    job,
    status: job?.status ?? null,
    message: job?.message ?? "",
    result: job?.result,
    error: job?.error ?? "",
    running: following,
  };
}
