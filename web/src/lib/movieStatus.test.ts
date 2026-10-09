import { describe, expect, it } from "vitest";
import { MOVIE_STATUS_LABELS, isMovieDownloaded, isMovieWanted, movieStatus, trackStatus } from "./movieStatus";
import { libraryStatus } from "./status";
import type { MovieFile } from "./api";

const file = (missing: boolean): MovieFile => ({ path: "/m/a.mkv", filename: "a.mkv", size_bytes: 1, missing });

describe("movieStatus", () => {
  it("never calls a missing file Downloaded", () => {
    const s = movieStatus({ has_file: true, monitored: true, file: file(true) });
    expect(s.key).toBe("missing");
    expect(s.label).toBe("File missing");
    expect(s.tone).toBe("reject"); // the shared vocabulary's tone for File missing
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

  it("takes its words and tones from the shared vocabulary", () => {
    const shared = libraryStatus({ hasFile: false, monitored: true });
    const s = movieStatus({ has_file: false, monitored: true });
    expect(s.label).toBe(shared.label);
    expect(s.tone).toBe(shared.tone);
    expect(MOVIE_STATUS_LABELS.missing).toBe(libraryStatus({ hasFile: true, monitored: true, fileMissing: true }).label);
  });

  it("filters to exactly the badge", () => {
    const items = [
      { has_file: false, monitored: true },
      { has_file: false, monitored: false },
      { has_file: true, monitored: true },
      { has_file: true, monitored: true, file: file(true) },
      { has_file: false, monitored: true, download: { state: "downloading", progress: 0.2 } },
      { has_file: true, monitored: true, download: { state: "downloading", progress: 0.2 } },
    ];
    for (const m of items) {
      const l = movieStatus(m).label;
      expect(isMovieWanted(m)).toBe(l === "Wanted");
      expect(isMovieDownloaded(m)).toBe(l === "Downloaded");
    }
  });

  it("says Status unknown, not Wanted, while the download client can't be read", () => {
    const wanted = { has_file: false, monitored: true };
    expect(movieStatus(wanted, undefined, false).label).toBe("Status unknown");
    expect(isMovieWanted(wanted, false)).toBe(false);
    // Only a would-be Wanted film: what's on disk, or unmonitored, is still known.
    expect(movieStatus({ has_file: true, monitored: true }, undefined, false).label).toBe("Downloaded");
    expect(movieStatus({ has_file: false, monitored: false }, undefined, false).label).toBe("Unmonitored");
    expect(isMovieDownloaded({ has_file: true, monitored: true }, false)).toBe(true);
  });
});
