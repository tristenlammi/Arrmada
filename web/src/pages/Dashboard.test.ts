import { describe, expect, it } from "vitest";
import { downloadingSub } from "./Dashboard";
import type { QueueSummary } from "../lib/api";

const q = (over: Partial<QueueSummary> = {}): QueueSummary => ({ downloading: 0, seeding: 0, paused: 0, errored: 0, down_speed: 0, up_speed: 0, ...over });

describe("downloadingSub", () => {
  it("says the client is unreachable instead of '0 seeding'", () => {
    expect(downloadingSub({ queue: q(), queue_note: "all download clients failed to list" })).toBe("Client unreachable");
  });

  it("counts errored torrents", () => {
    expect(downloadingSub({ queue: q({ errored: 2, seeding: 3 }) })).toBe("2 errored · 3 seeding");
  });

  it("mentions torrents waiting for peers only when there are some", () => {
    expect(downloadingSub({ queue: q({ seeding: 1 }) })).toBe("1 seeding");
    expect(downloadingSub({ queue: q({ seeding: 1, stalled: 4 }) })).toBe("1 seeding · 4 waiting for peers");
  });
});
