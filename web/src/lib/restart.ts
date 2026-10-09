import { api } from "./api";

// Waiting out a restart. Docker's restart policy brings Arrmada back on its own; the page
// knows it's back when /api/v1/status reports a different started_at. (Polling for any
// answer isn't enough: the old process keeps answering for a moment while it shuts down.)

export interface WaitOptions {
  timeoutMs?: number; // give up after this long (default 2 minutes)
  intervalMs?: number; // between polls
  initialDelayMs?: number; // before the first poll, while the old process shuts down
  status?: () => Promise<{ started_at: string }>; // swappable for tests
  sleep?: (ms: number) => Promise<void>;
  now?: () => number;
}

const defaultSleep = (ms: number) => new Promise<void>((r) => setTimeout(r, ms));

// startedAt is the running process's start time, or "" when the server can't be reached.
export async function startedAt(status: () => Promise<{ started_at: string }> = api.status): Promise<string> {
  try { return (await status()).started_at; } catch { return ""; }
}

// waitForRestart resolves true once the server reports a start time other than before,
// or false when it hasn't come back within the timeout.
export async function waitForRestart(before: string, opts: WaitOptions = {}): Promise<boolean> {
  const { timeoutMs = 120_000, intervalMs = 1500, initialDelayMs = 2000, status = api.status, sleep = defaultSleep, now = Date.now } = opts;
  const start = now();
  await sleep(initialDelayMs);
  while (now() - start < timeoutMs) {
    const at = await startedAt(status);
    if (at && at !== before) return true;
    await sleep(intervalMs);
  }
  return false;
}

// restartAndWait notes when the server started, runs trigger (the call that makes it
// restart), then waits for the new process. The caller reloads the page afterwards.
export async function restartAndWait(trigger: () => Promise<unknown>, opts: WaitOptions = {}): Promise<boolean> {
  const before = await startedAt(opts.status);
  await trigger();
  return waitForRestart(before, opts);
}
