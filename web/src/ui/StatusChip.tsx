import type { ReactNode } from "react";

export type Tone = "accent" | "good" | "avoid" | "reject" | "faint";

// The plain hue for each tone: fills, borders, bars, and words over artwork.
export const TONE_HUE: Record<Tone, string> = {
  accent: "var(--accent)",
  good: "var(--good)",
  avoid: "var(--avoid)",
  reject: "var(--reject)",
  faint: "var(--ink-faint)",
};
// The text-tuned hue and the soft fill, for chips sitting on a panel.
const TONE_TEXT: Record<Tone, string> = {
  accent: "var(--accent-text)",
  good: "var(--good-text)",
  avoid: "var(--avoid-text)",
  reject: "var(--reject-text)",
  faint: "var(--ink-faint)",
};
const TONE_SOFT: Record<Tone, string> = {
  accent: "var(--accent-soft)",
  good: "var(--good-soft)",
  avoid: "var(--avoid-soft)",
  reject: "var(--reject-soft)",
  faint: "var(--panel-2)",
};

// A near-opaque dark chip so a status stays legible over any poster, in either theme.
export const POSTER_CHIP_BG = "rgba(14,10,7,.92)";

const SIZE = {
  xs: "px-1.5 py-0.5 text-[8.5px]",
  sm: "px-2 py-0.5 text-[9px] tracking-wide",
  md: "px-2 py-0.5 text-[9.5px] tracking-wide",
} as const;

export interface StatusChipProps {
  tone: Tone;
  children: ReactNode;
  /** panel: soft fill on a card or row. poster: the dark chip over artwork. */
  surface?: "panel" | "poster";
  size?: keyof typeof SIZE;
  /** Positioning only (absolute, margins, flex-none); the look is the chip's own. */
  className?: string;
  title?: string;
}

// StatusChip is the small uppercase status pill: a tone-coloured border and label on
// either a soft fill (panel) or the dark poster chip.
export function StatusChip({ tone, children, surface = "panel", size = "sm", className, title }: StatusChipProps) {
  const poster = surface === "poster";
  const border = tone === "faint" && !poster ? "var(--line)" : TONE_HUE[tone];
  return (
    <span
      title={title}
      className={`rounded-full font-bold uppercase ${SIZE[size]} ${className ?? ""}`}
      style={{
        background: poster ? POSTER_CHIP_BG : TONE_SOFT[tone],
        color: poster ? TONE_HUE[tone] : TONE_TEXT[tone],
        border: `1px solid ${border}`,
      }}
    >
      {children}
    </span>
  );
}
