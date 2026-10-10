// gen-icons: renders the PNG app icons from the SVGs in web/public.
//
// iOS ignores SVG touch icons (it falls back to a screenshot of the page), Android
// wants PNG manifest icons for a proper adaptive icon, and a notification badge has to
// be a monochrome PNG. The PNGs are committed; run this only after changing the art:
//
//   npm run icons
//
// It draws each SVG in the Chromium that Playwright already installs for the e2e
// tests (npx playwright install chromium), so the build gains no new dependency.
import { readFileSync, writeFileSync } from "node:fs";
import { fileURLToPath } from "node:url";
import { chromium } from "@playwright/test";

const pub = (name) => fileURLToPath(new URL(`../public/${name}`, import.meta.url));

// icon.svg is full-bleed with the mark well inside the middle 80%, so the same art
// serves as the maskable icon: Android's mask only ever trims the plain background.
const OUTPUTS = [
  { src: "icon.svg", out: "apple-touch-icon.png", size: 180 },
  { src: "icon.svg", out: "icon-192.png", size: 192 },
  { src: "icon.svg", out: "icon-512.png", size: 512 },
  { src: "icon.svg", out: "icon-maskable-512.png", size: 512 },
  { src: "icon.svg", out: "favicon-32.png", size: 32 },
  { src: "badge.svg", out: "badge-96.png", size: 96 },
];

const browser = await chromium.launch();
try {
  const page = await browser.newPage({ deviceScaleFactor: 1 });
  for (const { src, out, size } of OUTPUTS) {
    const svg = readFileSync(pub(src), "utf8");
    const uri = `data:image/svg+xml;base64,${Buffer.from(svg).toString("base64")}`;
    await page.setViewportSize({ width: size, height: size });
    await page.setContent(
      `<html><body style="margin:0;background:transparent"><img src="${uri}" width="${size}" height="${size}" style="display:block"></body></html>`,
    );
    await page.waitForFunction(() => document.images[0]?.complete);
    // omitBackground keeps the badge's transparency; the icon art is opaque anyway.
    const png = await page.screenshot({ omitBackground: true, clip: { x: 0, y: 0, width: size, height: size } });
    writeFileSync(pub(out), png);
    console.log(`gen-icons: ${out} (${size}x${size}, ${png.length} bytes)`);
  }
} finally {
  await browser.close();
}
