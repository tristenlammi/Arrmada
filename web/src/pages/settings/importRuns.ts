import type { ImportRun, ImportRunStatus } from "../../lib/api";
import type { Tone } from "../../ui";

// Plain words for a Tautulli import run, shared by the Import settings card and its tests.

export const RUN_STATUS: Record<ImportRunStatus, { label: string; tone: Tone }> = {
  running: { label: "Importing", tone: "accent" },
  done: { label: "Finished", tone: "good" },
  failed: { label: "Failed", tone: "reject" },
  timeout: { label: "Timed out", tone: "avoid" },
  interrupted: { label: "Interrupted", tone: "avoid" },
};

const n = (v: number) => v.toLocaleString("en-US");

// runSummary is the one-line count of what a run did with every row it read:
// "Imported 12,340 · 210 already there · 980 recorded live · 3 invalid · 0 failed".
export function runSummary(r: ImportRun): string {
  const parts = [
    `Imported ${n(r.imported)}`,
    `${n(r.duplicates)} already there`,
    `${n(r.overlaps)} recorded live`,
    `${n(r.invalid)} invalid`,
  ];
  if (r.after_cutoff > 0) parts.push(`${n(r.after_cutoff)} after the cutoff`);
  parts.push(`${n(r.failed)} failed`);
  return parts.join(" · ");
}

// runProgress is how far a run has read, 0..1 (0 until Tautulli has said how many there are).
export function runProgress(r: ImportRun): number {
  if (r.total <= 0) return r.status === "done" ? 1 : 0;
  return Math.min(1, r.processed / r.total);
}

// canRetry: a run that stopped before reading everything can be run again; plays it already
// brought in are skipped.
export function canRetry(r: ImportRun): boolean {
  return r.status === "failed" || r.status === "timeout" || r.status === "interrupted";
}
