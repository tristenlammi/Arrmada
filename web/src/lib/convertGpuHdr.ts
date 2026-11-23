// The HDR sentence under Settings → Convert → "Use my GPU": which kinds of HDR the GPU takes,
// from what its HEVC and AV1 encoders kept when they were tested as Arrmada started
// (gpu_hdr / gpu_hdr_av1). HDR10+ and Dolby Vision always go to the CPU.

const KINDS = ["HDR10", "HLG"];

function words(items: string[]): string {
  if (items.length <= 1) return items.join("");
  return `${items.slice(0, -1).join(", ")} and ${items[items.length - 1]}`;
}

export function gpuHdrSentence(hevc: string[] = [], av1: string[] = [], hasAV1 = false): string {
  const taken = KINDS.filter((k) => hevc.includes(k) || (hasAV1 && av1.includes(k)));
  if (taken.length === 0) {
    return "HDR files encode on the CPU: your GPU didn't keep their HDR information when it was tested as Arrmada started.";
  }
  let s = `${words(taken)} files encode on the GPU too: it kept their HDR information when it was tested as Arrmada started`;
  const inHEVC = KINDS.filter((k) => hevc.includes(k));
  const inAV1 = KINDS.filter((k) => av1.includes(k));
  if (hasAV1 && words(inHEVC) !== words(inAV1)) {
    s += ` (in HEVC: ${words(inHEVC) || "none"}; in AV1: ${words(inAV1) || "none"})`;
  }
  const cpu = [...KINDS.filter((k) => !taken.includes(k)), "HDR10+", "Dolby Vision"];
  return `${s}. ${words(cpu)} files encode on the CPU.`;
}
