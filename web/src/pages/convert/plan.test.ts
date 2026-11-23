import { describe, expect, it } from "vitest";
import type { ConvertPlan } from "../../lib/api";
import { planChanges, previewLines, requestedToast, trackLabel, videoLine } from "./plan";

const GB = 1024 ** 3;

const dvRemuxPlan: ConvertPlan = {
  key: "movie:7", kind: "movie", title: "Remux", size_bytes: 70 * GB,
  needs: { video: true, subs: true, audio: true, save: 30 * GB, worth: true },
  video: { from_codec: "HEVC", from_res: "2160p", from_hdr: "Dolby Vision", from_bits: 10, to_codec: "HEVC", to_bits: 10, to_hdr: "HDR10", crf: 18, est_bytes: 40 * GB },
  audio: [
    { type: "audio", index: 0, codec: "truehd", lang: "eng", channels: 8, keep: true },
    { type: "audio", index: 1, codec: "ac3", lang: "eng", channels: 2, title: "Commentary", keep: false, reason: "commentary" },
  ],
  subs: [
    { type: "subtitle", index: 0, codec: "hdmv_pgs_subtitle", lang: "eng", image: true, keep: false, reason: "image subtitle covered by text" },
    { type: "subtitle", index: 1, codec: "hdmv_pgs_subtitle", lang: "eng", image: true, forced: true, keep: true },
  ],
  container: { from: "MP4", to: "MKV" },
  warnings: ["Dolby Vision layer dropped — kept as HDR10"],
  eta_sec: null,
};

describe("conversion plan words", () => {
  it("names the picture before and after", () => {
    expect(videoLine(dvRemuxPlan)).toBe("HEVC 2160p Dolby Vision 10-bit → HEVC HDR10 10-bit · CRF 18");
    expect(videoLine({ ...dvRemuxPlan, video: { ...dvRemuxPlan.video, to_codec: undefined } })).toBe("HEVC 2160p Dolby Vision 10-bit — copied untouched");
  });

  it("names tracks the way a player menu would", () => {
    expect(trackLabel(dvRemuxPlan.audio[1])).toBe("ENG · AC-3 stereo · Commentary");
    expect(trackLabel(dvRemuxPlan.subs[1])).toBe("ENG · PGS · forced · image");
    expect(trackLabel({ type: "audio", index: 0, codec: "aac", keep: true })).toBe("untagged · AAC");
  });

  it("lists one line per change, removed tracks with their reasons", () => {
    const lines = planChanges(dvRemuxPlan);
    expect(lines[0]).toMatch(/^Picture: HEVC 2160p Dolby Vision/);
    expect(lines[1]).toBe("Size: 70.0 GB → ~40.0 GB (estimated)");
    expect(lines).toContain("Audio: 1 of 2 tracks removed — ENG · AC-3 stereo · Commentary (commentary)");
    expect(lines).toContain("Subtitles: 1 of 2 tracks removed — ENG · PGS · image (image subtitle covered by text)");
    expect(lines).toContain("Container: MP4 → MKV");
  });

  it("sums a library in counts, leaving out what nothing loses", () => {
    const lines = previewLines({
      files: 120, reencode: 100, bytes: 20 * 1024 * GB, est_bytes: 11 * 1024 * GB, dv_files: 3, cc_files: 1, image_sub_tracks: 14,
      filtered_audio_tracks: 2, container_changes: 0, forced_tracks: 0, as_of: 0,
    });
    expect(lines).toEqual([
      "120 files (100 re-encoded, 20 only rewritten to tidy tracks): about 20.00 TB → ~11.00 TB",
      "3 files lose Dolby Vision (kept as HDR10 or HLG)",
      "1 file loses embedded closed captions",
      "14 image-subtitle tracks removed",
      "2 audio tracks filtered out",
    ]);
    expect(previewLines({ files: 0, reencode: 0, bytes: 0, est_bytes: 0, dv_files: 0, cc_files: 0, image_sub_tracks: 0, filtered_audio_tracks: 0, container_changes: 0, forced_tracks: 0, as_of: 0 })).toEqual([]);
  });

  it("says how many Dolby Vision files the Dolby Vision choice leaves alone", () => {
    const lines = previewLines({
      files: 4, reencode: 4, bytes: 40 * GB, est_bytes: 22 * GB, dv_files: 0, dv_left_alone: 3, cc_files: 0, image_sub_tracks: 0,
      filtered_audio_tracks: 0, container_changes: 0, forced_tracks: 0, as_of: 0,
    });
    expect(lines).toContain("3 Dolby Vision files left as they are — your Dolby Vision choice in Convert's settings");
    expect(previewLines({ files: 1, reencode: 1, bytes: GB, est_bytes: GB / 2, dv_files: 0, dv_left_alone: 1, cc_files: 0, image_sub_tracks: 0, filtered_audio_tracks: 0, container_changes: 0, forced_tracks: 0, as_of: 0 }))
      .toContain("1 Dolby Vision file left as it is — your Dolby Vision choice in Convert's settings");
  });

  it("says where a request stands, as the server placed it", () => {
    expect(requestedToast("Ep 2", { requested: "k", starting: true })).toBe("“Ep 2” is starting now");
    expect(requestedToast("Ep 2", { requested: "k", position: 1, ahead: "Film" })).toBe("“Ep 2” is next in line — after “Film”");
    expect(requestedToast("Ep 3", { requested: "k", position: 2, ahead: "Ep 2" })).toBe("“Ep 3” is #2 in line — after “Ep 2”");
    expect(requestedToast("Ep 2", { requested: "k", position: 1, reason: "someone is watching — it starts when they stop" }))
      .toBe("“Ep 2” waits — someone is watching — it starts when they stop");
  });

  it("says a tracks-only rewrite leaves the picture alone", () => {
    const tidy: ConvertPlan = { ...dvRemuxPlan, video: { ...dvRemuxPlan.video, to_codec: undefined }, needs: { ...dvRemuxPlan.needs, video: false, save: 2 * GB } };
    expect(planChanges(tidy)[0]).toMatch(/^Size:/);
  });
});
