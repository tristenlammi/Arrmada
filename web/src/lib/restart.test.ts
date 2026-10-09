import { describe, expect, it } from "vitest";
import { restartAndWait, waitForRestart } from "./restart";

// A fake clock: sleep moves time forward instead of waiting.
function clock() {
  let t = 0;
  return { now: () => t, sleep: async (ms: number) => { t += ms; } };
}

// status answers from a script: a start time, or an Error for "server down".
function scripted(answers: (string | Error)[]) {
  let i = 0;
  return async () => {
    const a = answers[Math.min(i++, answers.length - 1)];
    if (a instanceof Error) throw a;
    return { started_at: a };
  };
}

describe("waitForRestart", () => {
  it("waits through the old process and the downtime until started_at changes", async () => {
    const c = clock();
    const status = scripted(["old", new Error("down"), new Error("down"), "new"]);
    expect(await waitForRestart("old", { ...c, status })).toBe(true);
  });

  it("gives up after the timeout when the server never comes back", async () => {
    const c = clock();
    const status = scripted([new Error("down")]);
    expect(await waitForRestart("old", { ...c, status, timeoutMs: 10_000 })).toBe(false);
    expect(c.now()).toBeGreaterThanOrEqual(10_000);
  });

  it("doesn't count the same process answering again as a restart", async () => {
    const c = clock();
    expect(await waitForRestart("old", { ...c, status: scripted(["old"]), timeoutMs: 5_000 })).toBe(false);
  });
});

describe("restartAndWait", () => {
  it("records the start time before triggering the restart", async () => {
    const c = clock();
    let triggered = false;
    const status = async () => ({ started_at: triggered ? "new" : "old" });
    const ok = await restartAndWait(async () => { triggered = true; }, { ...c, status });
    expect(ok).toBe(true);
  });
});
