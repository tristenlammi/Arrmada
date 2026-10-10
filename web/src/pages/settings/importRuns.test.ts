import { describe, expect, it } from "vitest";
import type { ImportRun } from "../../lib/api";
import { canRetry, runProgress, runSummary } from "./importRuns";

const run = (over: Partial<ImportRun> = {}): ImportRun => ({
  id: 1, job_id: 1, source: "tautulli", started_at: 0, finished_at: 0, status: "done",
  total: 13533, processed: 13533, imported: 12340, duplicates: 210, overlaps: 980, invalid: 3,
  after_cutoff: 0, failed: 0, error: "", cutoff_at: 0, rows: 12340, removed_at: 0, removed_rows: 0,
  ...over,
});

describe("runSummary", () => {
  it("names every bucket", () => {
    expect(runSummary(run())).toBe("Imported 12,340 · 210 already there · 980 recorded live · 3 invalid · 0 failed");
  });
  it("mentions the cutoff only when it skipped something", () => {
    expect(runSummary(run({ after_cutoff: 40 }))).toContain("40 after the cutoff");
  });
});

describe("runProgress", () => {
  it("is processed over total, capped at 1", () => {
    expect(runProgress(run({ status: "running", total: 1200, processed: 600 }))).toBe(0.5);
    expect(runProgress(run({ total: 10, processed: 12 }))).toBe(1);
  });
  it("is 0 before Tautulli reports a total, unless the run is done", () => {
    expect(runProgress(run({ status: "running", total: 0, processed: 0 }))).toBe(0);
    expect(runProgress(run({ status: "done", total: 0, processed: 0 }))).toBe(1);
  });
});

describe("canRetry", () => {
  it("offers Retry only for a run that stopped early", () => {
    expect(canRetry(run({ status: "failed" }))).toBe(true);
    expect(canRetry(run({ status: "timeout" }))).toBe(true);
    expect(canRetry(run({ status: "interrupted" }))).toBe(true);
    expect(canRetry(run({ status: "done" }))).toBe(false);
    expect(canRetry(run({ status: "running" }))).toBe(false);
  });
});
