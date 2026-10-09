import { beforeEach, describe, expect, it, vi } from "vitest";

const status = vi.fn();
const restartApp = vi.fn();
vi.mock("./api", () => ({ api: { status: () => status(), restartApp: () => restartApp() } }));

import { busyLines, fmtAge, restartAndWait, RESTART_TIMEOUT_MESSAGE } from "./restart";

const idle = { convert_running: 0, convert_longest_sec: 0, convert_progress: 0, subtitles_running: 0, subtitles_queued: 0 };

describe("restartAndWait", () => {
  beforeEach(() => { status.mockReset(); restartApp.mockReset(); restartApp.mockResolvedValue({ status: "restarting" }); });

  it("waits past the old process (same started_at) and a down moment for the new one", async () => {
    status
      .mockResolvedValueOnce({ started_at: "old" }) // before the restart
      .mockResolvedValueOnce({ started_at: "old" }) // old process, still answering
      .mockRejectedValueOnce(new Error("down")) // between the two
      .mockResolvedValueOnce({ started_at: "new" });
    const then = vi.fn();
    await restartAndWait({ then, intervalMs: 1, timeoutMs: 5000 });
    expect(restartApp).toHaveBeenCalledOnce();
    expect(then).toHaveBeenCalledOnce();
    expect(status).toHaveBeenCalledTimes(4);
  });

  it("gives up with a pointer to the logs when the app never comes back", async () => {
    status.mockResolvedValue({ started_at: "old" });
    const then = vi.fn();
    await expect(restartAndWait({ then, intervalMs: 1, timeoutMs: 20 })).rejects.toThrow(RESTART_TIMEOUT_MESSAGE);
    expect(then).not.toHaveBeenCalled();
  });
});

describe("busyLines", () => {
  it("says nothing when nothing is running", () => {
    expect(busyLines(idle)).toEqual([]);
  });

  it("names a running conversion's age and progress", () => {
    expect(busyLines({ ...idle, convert_running: 1, convert_longest_sec: 3 * 3600 + 12 * 60, convert_progress: 0.41 }))
      .toEqual(["A conversion has been running 3h 12m (41%) and will start over."]);
  });

  it("counts subtitle jobs, running and queued", () => {
    expect(busyLines({ ...idle, subtitles_running: 1, subtitles_queued: 2 })[0]).toMatch(/^3 subtitle jobs will stop/);
  });
});

describe("fmtAge", () => {
  it("reads like a person would say it", () => {
    expect(fmtAge(40)).toBe("40s");
    expect(fmtAge(12 * 60 + 5)).toBe("12m");
    expect(fmtAge(2 * 3600)).toBe("2h 0m");
  });
});
