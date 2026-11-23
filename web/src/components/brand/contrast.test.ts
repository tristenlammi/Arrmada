/// <reference types="node" />
import { readFileSync } from "node:fs";
import { describe, expect, it } from "vitest";

// The media server brand tokens in index.css: every ink must read on its fill at WCAG AA
// (4.5:1) in all four theme blocks. Jellyfin's fill is a gradient, so its ink is checked
// against both ends and the colours in between.

const css = readFileSync(new URL("../../index.css", import.meta.url), "utf8").replace(/\/\*[\s\S]*?\*\//g, "");

// Each theme block's body: the dark default, the light media block, and the two toggles.
function blocks(): Record<string, string> {
  const at = (re: RegExp) => {
    const m = css.match(re);
    if (!m) throw new Error(`no block for ${re}`);
    return m[1];
  };
  return {
    dark: at(/^:root\s*\{([^}]*)\}/m),
    "light (system)": at(/@media \(prefers-color-scheme: light\)\s*\{\s*:root\s*\{([^}]*)\}/),
    "dark (toggle)": at(/:root\[data-theme="dark"\]\s*\{([^}]*)\}/),
    "light (toggle)": at(/:root\[data-theme="light"\]\s*\{([^}]*)\}/),
  };
}

const token = (body: string, name: string) => {
  const m = body.match(new RegExp(`${name}\\s*:\\s*([^;]+);`));
  if (!m) throw new Error(`${name} missing`);
  return m[1].trim();
};
const hexes = (v: string) => [...v.matchAll(/#([0-9a-f]{6})\b/gi)].map((m) => m[1]);
const rgb = (hex: string) => [0, 2, 4].map((i) => parseInt(hex.slice(i, i + 2), 16));
const lum = ([r, g, b]: number[]) => {
  const ch = (v: number) => ((v /= 255) <= 0.03928 ? v / 12.92 : ((v + 0.055) / 1.055) ** 2.4);
  return 0.2126 * ch(r) + 0.7152 * ch(g) + 0.0722 * ch(b);
};
const ratio = (a: number[], b: number[]) => {
  const [hi, lo] = [lum(a), lum(b)].sort((x, y) => y - x);
  return (hi + 0.05) / (lo + 0.05);
};

// Every colour a fill can show under its words: a solid fill is one, a gradient is its
// stops and nine steps between each pair (sRGB interpolation, as the browser draws it).
function fillColours(v: string): number[][] {
  const stops = hexes(v).map(rgb);
  if (stops.length < 2) return stops;
  const out: number[][] = [];
  for (let i = 0; i < stops.length - 1; i++) {
    for (let t = 0; t <= 10; t++) out.push(stops[i].map((c, k) => Math.round(c + ((stops[i + 1][k] - c) * t) / 10)));
  }
  return out;
}

const PAIRS = [
  { fill: "--plex", ink: "--plex-ink" },
  { fill: "--jellyfin", ink: "--jellyfin-ink" },
  { fill: "--emby", ink: "--emby-ink" },
];

describe("brand token contrast", () => {
  for (const [theme, body] of Object.entries(blocks())) {
    for (const { fill, ink } of PAIRS) {
      it(`${ink} on ${fill} clears 4.5:1 (${theme})`, () => {
        const inkHex = hexes(token(body, ink));
        expect(inkHex).toHaveLength(1);
        const colours = fillColours(token(body, fill));
        expect(colours.length).toBeGreaterThan(0);
        const worst = Math.min(...colours.map((c) => ratio(rgb(inkHex[0]), c)));
        expect(worst).toBeGreaterThanOrEqual(4.5);
      });
    }
  }

  it("white would fail on the Jellyfin gradient (why its ink is dark)", () => {
    const worst = Math.min(...fillColours(token(blocks().dark, "--jellyfin")).map((c) => ratio([255, 255, 255], c)));
    expect(worst).toBeLessThan(4.5);
  });
});
