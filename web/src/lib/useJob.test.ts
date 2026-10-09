import { describe, expect, it } from "vitest";
import type { Job, JobStatus } from "./api";
import { followJob, jobDone, jobToast, JOB_GIVE_UP_MS, type FollowDeps } from "./useJob";

function job(status: JobStatus, extra: Partial<Job> = {}): Job {
  return {
    id: 7, kind: "movie.search", target: "movie:1", trigger: "user:1", status, progress: 0,
    message: "", error: "", created_at: null, started_at: null, finished_at: null, ...extra,
  };
}

// A fake clock and a scripted server: sleep moves time on instead of waiting.
function deps(answers: (JobStatus | Error)[], over: Partial<FollowDeps> = {}) {
  let t = 0;
  let i = 0;
  const calls: number[] = [];
  const d: FollowDeps = {
    get: async () => {
      calls.push(t);
      const a = answers[Math.min(i++, answers.length - 1)];
      if (a instanceof Error) throw a;
      return job(a, a === "succeeded" ? { message: "Grabbed Arrival.2016.1080p" } : {});
    },
    sleep: async (ms) => { t += ms; },
    now: () => t,
    hidden: () => false,
    pollMs: () => 1500,
    stopped: () => false,
    ...over,
  };
  return { d, calls, now: () => t };
}

describe("followJob", () => {
  it("checks until the job ends and returns it", async () => {
    const { d, calls } = deps(["queued", "running", new Error("blip"), "succeeded"]);
    const done = await followJob(7, d);
    expect(done?.status).toBe("succeeded");
    expect(done?.message).toBe("Grabbed Arrival.2016.1080p");
    expect(calls).toEqual([0, 1500, 3000, 4500]);
  });

  it("gives up after five minutes of a job that never ends", async () => {
    const { d, now } = deps(["running"]);
    expect(await followJob(7, d)).toBeNull();
    expect(now()).toBeGreaterThanOrEqual(JOB_GIVE_UP_MS);
  });

  it("makes no requests while the tab is hidden", async () => {
    let hidden = true;
    const r = deps(["succeeded"], { hidden: () => hidden, giveUpMs: 60_000 });
    const sleep = r.d.sleep;
    r.d.sleep = async (ms) => { await sleep(ms); if (r.now() >= 6000) hidden = false; };
    const done = await followJob(7, r.d);
    expect(done?.status).toBe("succeeded");
    expect(r.calls).toEqual([6000]);
  });

  it("stops when the page stops following", async () => {
    let stopped = false;
    const r = deps(["running"], { stopped: () => stopped });
    r.d.sleep = async () => { stopped = true; };
    expect(await followJob(7, r.d)).toBeNull();
    expect(r.calls.length).toBe(1);
  });
});

describe("job toasts", () => {
  it("says what happened", () => {
    expect(jobDone("running")).toBe(false);
    expect(jobDone("queued")).toBe(false);
    expect(jobDone("failed")).toBe(true);
    expect(jobToast(job("succeeded", { message: "No releases found" }))).toBe("No releases found");
    expect(jobToast(job("failed", { error: "every indexer failed" }))).toBe("every indexer failed");
    expect(jobToast(job("succeeded"), "Scan finished.")).toBe("Scan finished.");
  });
});
