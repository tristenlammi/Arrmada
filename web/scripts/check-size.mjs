// check-size: post-build bundle budget for the embedded UI (runs after compress.mjs).
//
// Pages are split into their own chunks so a requester's phone downloads Discover and
// the app shell, not the whole admin console. This script keeps it that way:
//
// 1. Prints every JS/CSS chunk with its raw and brotli size.
// 2. Works out a requester's first load from dist/.vite/manifest.json: the JavaScript of
//    the entry chunk, the Discover page and everything they import statically (vendor
//    included). That is what a phone must download and parse before Discover appears.
// 3. Fails the build if that first load grows past the budget, or if it pulls in an
//    admin-only page (Quality, Convert, Insights, Settings, the audiobook admin tabs).
// 4. Deletes dist/.vite: the manifest is a build input only, not something to embed.
//
// Plain node, no dependencies. Usage: node scripts/check-size.mjs [distDir]
import { existsSync, readFileSync, rmSync } from "node:fs";
import { join } from "node:path";
import { fileURLToPath } from "node:url";
import { brotliCompressSync, constants } from "node:zlib";

const dist = process.argv[2] ?? fileURLToPath(new URL("../../internal/webui/dist/", import.meta.url));

// The first split build measured 327 KB raw / 88 KB brotli. The raw budget is the
// roadmap's 350 KB cap; the brotli one is that measurement plus 10%, tighter than the
// roadmap's 110 KB. Raise them on purpose, in review, never just to make a build pass.
const BUDGET_RAW = 350 * 1024;
const BUDGET_BR = 97 * 1024;

const REQUESTER_PAGE = "src/pages/Discover.tsx";
const ADMIN_ONLY = ["Quality", "Convert", "Insights", "Settings", "AudiobooksAdmin"].map((p) => `src/pages/${p}.tsx`);

const manifestPath = join(dist, ".vite", "manifest.json");
if (!existsSync(manifestPath)) {
  console.error("check-size: no dist/.vite/manifest.json; is build.manifest on in vite.config.ts?");
  process.exit(1);
}
const manifest = JSON.parse(readFileSync(manifestPath, "utf8"));

// staticClosure collects the files a manifest entry loads up front: its own file, its CSS,
// and the same for everything it imports statically. Dynamic imports load later, on demand.
function staticClosure(key, out = new Set(), seen = new Set()) {
  if (seen.has(key)) return out;
  seen.add(key);
  const e = manifest[key];
  if (!e) throw new Error(`check-size: ${key} is not in the manifest`);
  out.add(e.file);
  for (const css of e.css ?? []) out.add(css);
  for (const imp of e.imports ?? []) staticClosure(imp, out, seen);
  return out;
}

const entryKey = Object.keys(manifest).find((k) => manifest[k].isEntry);
if (!entryKey) {
  console.error("check-size: the manifest has no entry chunk");
  process.exit(1);
}
const firstLoad = staticClosure(entryKey);
// A page's chunk is keyed by its source path, unless other chunks import it statically
// (Discover's lazy pages import its shared cards): Vite then keys it by chunk name.
const requesterKey = manifest[REQUESTER_PAGE]
  ? REQUESTER_PAGE
  : Object.keys(manifest).find((k) => manifest[k].isDynamicEntry && manifest[k].name === "Discover" && k.startsWith("_"));
if (!requesterKey) {
  console.error(`check-size: ${REQUESTER_PAGE} is not in the manifest`);
  process.exit(1);
}
staticClosure(requesterKey, firstLoad);

function sizes(file) {
  const data = readFileSync(join(dist, file));
  const brPath = join(dist, file + ".br");
  // compress.mjs skips files under 1 KB; compress those here so the totals are honest.
  const br = existsSync(brPath)
    ? readFileSync(brPath).length
    : brotliCompressSync(data, { params: { [constants.BROTLI_PARAM_QUALITY]: 11 } }).length;
  return { raw: data.length, br };
}

const kb = (n) => `${(n / 1024).toFixed(1)} KB`;
const all = new Set();
for (const e of Object.values(manifest)) {
  all.add(e.file);
  for (const css of e.css ?? []) all.add(css);
}
const rows = [...all]
  .filter((f) => /\.(js|css)$/.test(f))
  .map((f) => ({ file: f, ...sizes(f), first: firstLoad.has(f) }))
  .sort((a, b) => b.raw - a.raw);

console.log("check-size: chunks (* = in a requester's first load)");
for (const r of rows) {
  console.log(`  ${r.first ? "*" : " "} ${r.file.padEnd(44)} ${kb(r.raw).padStart(10)} ${kb(r.br).padStart(10)} br`);
}

let raw = 0;
let br = 0;
for (const r of rows) {
  if (r.first && r.file.endsWith(".js")) {
    raw += r.raw;
    br += r.br;
  }
}
console.log(`check-size: requester first load (JS) ${kb(raw)} raw, ${kb(br)} brotli (budget ${kb(BUDGET_RAW)} / ${kb(BUDGET_BR)})`);

let failed = false;
if (raw > BUDGET_RAW || br > BUDGET_BR) {
  console.error("check-size: the requester first load is over budget. Lazy-load what Discover doesn't need up front.");
  failed = true;
}
for (const key of ADMIN_ONLY) {
  const file = manifest[key]?.file;
  if (file && firstLoad.has(file)) {
    console.error(`check-size: ${key} is in the requester first load; it must stay a lazy chunk.`);
    failed = true;
  }
}

rmSync(join(dist, ".vite"), { recursive: true, force: true });
if (failed) process.exit(1);
