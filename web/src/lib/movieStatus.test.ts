import { describe, expect, it } from "vitest";
import { movieStatus, trackStatus } from "./movieStatus";
import type { MovieFile } from "./api";

const file = (missing: boolean): MovieFile => ({ path: "/m/a.mkv", filename: "a.mkv", size_bytes: 1, missing });

describe("movieStatus", () => {
  it("never calls a missing file Downloaded", () => {
    const s = movieStatus({ has_file: true, monitored: true, file: file(true) });
    expect(s.key).toBe("missing");
    expect(s.label).toBe("File missing");
    expect(s.tone).toBe("avoid");
  });

  it("orders downloading, downloaded, wanted, unmonitored", () => {
    expect(movieStatus({ has_file: false, monitored: true, download: { state: "downloading", progress: 0.5 } }).key).toBe("downloading");
    expect(movieStatus({ has_file: true, monitored: false, file: file(false) }).key).toBe("downloaded");
    expect(movieStatus({ has_file: true, monitored: true }).key).toBe("downloaded"); // list rows carry no file yet
    expect(movieStatus({ has_file: false, monitored: true }).key).toBe("wanted");
    expect(movieStatus({ has_file: false, monitored: false }).key).toBe("unmonitored");
  });

  it("reads a track on its own", () => {
    expect(trackStatus({ has_file: true, monitored: true, file: file(true) }).label).toBe("File missing");
    expect(trackStatus({ has_file: false, monitored: false }).label).toBe("Unmonitored");
  });
});
