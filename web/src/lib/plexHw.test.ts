import { describe, expect, it } from "vitest";
import { hwBadge } from "./plexHw";

const t = (over: Partial<Parameters<typeof hwBadge>[0]>) => ({ decision: "transcode", hw_decode: false, hw_encode: false, hw_requested: false, ...over });

describe("hwBadge", () => {
  it("names what the GPU is doing", () => {
    expect(hwBadge(t({ hw_decode: true, hw_encode: true, hw_requested: true }))).toEqual({ label: "HW dec+enc", fellBack: false });
    expect(hwBadge(t({ hw_encode: true, hw_requested: true }))).toEqual({ label: "HW enc", fellBack: false });
  });
  it("flags a CPU fallback when hardware was requested", () => {
    expect(hwBadge(t({ hw_requested: true }))).toEqual({ label: "CPU", fellBack: true });
    // A GPU decode with a CPU encode is still a fallback: the encoder sets the pace.
    expect(hwBadge(t({ hw_decode: true, hw_requested: true }))).toEqual({ label: "HW dec", fellBack: true });
  });
  it("plain CPU when hardware is off, and nothing without a transcode", () => {
    expect(hwBadge(t({}))).toEqual({ label: "CPU", fellBack: false });
    expect(hwBadge(t({ decision: "direct_play", hw_requested: true }))).toBeNull();
    expect(hwBadge(t({ decision: "direct_stream", hw_requested: true }))).toBeNull();
  });
});
