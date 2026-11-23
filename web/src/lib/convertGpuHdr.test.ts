import { describe, expect, it } from "vitest";
import { gpuHdrSentence } from "./convertGpuHdr";

// The "Use my GPU" note says which HDR the GPU takes, from its startup test.
describe("gpuHdrSentence", () => {
  it("says HDR stays on the CPU when the GPU kept none", () => {
    expect(gpuHdrSentence([], [], true)).toMatch(/^HDR files encode on the CPU/);
    expect(gpuHdrSentence(undefined, undefined, false)).toMatch(/^HDR files encode on the CPU/);
  });
  it("names what the GPU takes and what stays on the CPU", () => {
    expect(gpuHdrSentence(["HDR10", "HLG"], ["HDR10", "HLG"], true)).toBe(
      "HDR10 and HLG files encode on the GPU too: it kept their HDR information when it was tested as Arrmada started. HDR10+ and Dolby Vision files encode on the CPU.",
    );
    expect(gpuHdrSentence(["HDR10"], [], false)).toBe(
      "HDR10 files encode on the GPU too: it kept their HDR information when it was tested as Arrmada started. HLG, HDR10+ and Dolby Vision files encode on the CPU.",
    );
  });
  it("splits HEVC from AV1 when they differ", () => {
    expect(gpuHdrSentence(["HDR10", "HLG"], [], true)).toContain("(in HEVC: HDR10 and HLG; in AV1: none)");
    // No AV1 encoder on the GPU: AV1's empty list is no news.
    expect(gpuHdrSentence(["HDR10", "HLG"], [], false)).not.toContain("in AV1");
  });
});
