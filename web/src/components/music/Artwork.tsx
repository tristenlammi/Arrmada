import { useState } from "react";

// Artwork is an artist's photo or an album's cover, filling its box (the caller sizes and
// rounds it). With no picture, or one that won't load (the Cover Art Archive answers 404
// for a release group nobody has scanned), it shows a placeholder instead of a broken
// image: an artist's initials, or a record for an album. Decorative: the name is always
// printed beside it.

// caaSized asks the Cover Art Archive for the thumbnail size the box needs: the stored
// URL is the 500px front, and a 40px album row needs only the 250px one. Anything else
// is returned unchanged.
export function caaSized(url: string, size: 250 | 500): string {
  return url.replace(/\/front-(?:250|500|1200)$/, `/front-${size}`);
}

// initials are an artist's monogram: the first letters of its first two words, skipping
// a leading "The" ("The Quiet Harbours" → "QH"), or the first two letters of a one-word
// name ("Björk" → "BJ"). Letters and digits only; "♫" when there are none.
export function initials(name: string): string {
  let words = name.split(/\s+/).map((w) => w.replace(/[^\p{L}\p{N}]/gu, "")).filter(Boolean);
  if (words.length > 1 && /^the$/i.test(words[0])) words = words.slice(1);
  if (words.length === 0) return "♫";
  const out = words.length === 1 ? Array.from(words[0]).slice(0, 2).join("") : Array.from(words[0])[0] + Array.from(words[1])[0];
  return out.toUpperCase();
}

// hue is a stable colour for a name, so a placeholder keeps its colour between visits.
function hue(name: string): number {
  let h = 0;
  for (const ch of name) h = (h * 31 + (ch.codePointAt(0) ?? 0)) % 360;
  return h;
}

export function Artwork({
  src,
  name,
  kind,
  size = 250,
  className = "",
}: {
  src?: string;
  name: string;
  kind: "artist" | "album";
  /** The Cover Art Archive thumbnail to ask for. */
  size?: 250 | 500;
  /** Box size and rounding. */
  className?: string;
}) {
  // The URL that failed, so a new one (after a refresh) is tried again.
  const [failed, setFailed] = useState<string | null>(null);
  const show = !!src && failed !== src;
  if (show) {
    return (
      <img
        src={caaSized(src, size)}
        alt=""
        loading="lazy"
        decoding="async"
        onError={() => setFailed(src)}
        className={`block object-cover ${className}`}
        style={{ background: "var(--panel-2)" }}
      />
    );
  }
  if (kind === "artist") {
    const h = hue(name);
    return (
      <div
        aria-hidden
        data-placeholder="artist"
        className={`flex select-none items-center justify-center font-bold text-white ${className}`}
        style={{ background: `linear-gradient(150deg, hsl(${h} 28% 30%), hsl(${h} 26% 17%))`, containerType: "size" }}
      >
        <span style={{ fontSize: "clamp(0.75rem, 34cqmin, 4rem)", lineHeight: 1 }}>{initials(name)}</span>
      </div>
    );
  }
  return (
    <div
      aria-hidden
      data-placeholder="album"
      className={`flex items-center justify-center ${className}`}
      style={{ background: "var(--panel-2)", color: "var(--ink-faint)", containerType: "size" }}
    >
      {/* A record: what an album is, cover or not. */}
      <svg viewBox="0 0 24 24" fill="none" style={{ width: "46%", height: "46%" }}>
        <circle cx="12" cy="12" r="9" stroke="currentColor" strokeWidth="1.6" />
        <circle cx="12" cy="12" r="2.6" stroke="currentColor" strokeWidth="1.6" />
      </svg>
    </div>
  );
}
