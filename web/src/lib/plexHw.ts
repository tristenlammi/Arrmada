import type { InsightsStream } from "./api";

export interface HwBadge {
  /** "HW dec+enc", "HW enc", "HW dec" or "CPU". */
  label: string;
  /** Hardware transcoding was asked for but Plex is encoding on the CPU. */
  fellBack: boolean;
}

type HwFields = Pick<InsightsStream, "decision" | "hw_decode" | "hw_encode" | "hw_requested">;

// hwBadge says where Plex is really doing a transcode, from what it reports using
// (hwDecoding/hwEncoding), not from "hardware requested" — with hardware transcoding
// switched on, Plex still falls back to the CPU when the GPU can't take a stream. Direct
// play and direct stream (a remux: video and audio copied) don't transcode, so no badge.
export function hwBadge(s: HwFields): HwBadge | null {
  if (s.decision !== "transcode") return null;
  const label = s.hw_decode && s.hw_encode ? "HW dec+enc" : s.hw_encode ? "HW enc" : s.hw_decode ? "HW dec" : "CPU";
  return { label, fellBack: !!s.hw_requested && !s.hw_encode };
}
