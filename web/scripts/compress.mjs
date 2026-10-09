// compress: post-build step for the embedded UI.
//
// 1. Stamps the service worker: dist/sw.js carries a __BUILD__ placeholder that
//    becomes a short hash of index.html (which names every hashed bundle), so each
//    deploy ships a byte-different sw.js. The browser then installs it, and its
//    activate step drops the previous build's cache.
// 2. Writes .br (brotli q11) and .gz (gzip -9) next to every text asset over 1 KB.
//    internal/webui serves those precompressed copies to browsers that accept them,
//    so a phone downloads ~240 KB instead of ~950 KB without the server compressing
//    on every request.
//
// Plain node (node:zlib), no dependencies. Usage: node scripts/compress.mjs [distDir]
import { createHash } from "node:crypto";
import { readFileSync, readdirSync, statSync, writeFileSync } from "node:fs";
import { extname, join, relative } from "node:path";
import { fileURLToPath } from "node:url";
import { brotliCompressSync, constants, gzipSync } from "node:zlib";

const dist = process.argv[2] ?? fileURLToPath(new URL("../../internal/webui/dist/", import.meta.url));

const COMPRESSIBLE = new Set([".js", ".css", ".svg", ".webmanifest"]);
const MIN_BYTES = 1024;
// The worker script must stay identity-encoded and uncached (the browser's update
// check compares it byte for byte); index.html is tiny and always revalidated.
const SKIP = new Set(["sw.js", "index.html"]);

const indexHtml = readFileSync(join(dist, "index.html"));
const build = createHash("sha256").update(indexHtml).digest("hex").slice(0, 10);

const swPath = join(dist, "sw.js");
const sw = readFileSync(swPath, "utf8");
if (!sw.includes("__BUILD__")) {
  console.error("compress: dist/sw.js has no __BUILD__ placeholder to stamp");
  process.exit(1);
}
writeFileSync(swPath, sw.replaceAll("__BUILD__", build));

function walk(dir, out = []) {
  for (const name of readdirSync(dir)) {
    const p = join(dir, name);
    if (statSync(p).isDirectory()) walk(p, out);
    else out.push(p);
  }
  return out;
}

let raw = 0;
let br = 0;
let count = 0;
for (const file of walk(dist)) {
  const rel = relative(dist, file).replace(/\\/g, "/");
  if (SKIP.has(rel) || !COMPRESSIBLE.has(extname(file))) continue;
  const data = readFileSync(file);
  if (data.length <= MIN_BYTES) continue;
  const b = brotliCompressSync(data, {
    params: {
      [constants.BROTLI_PARAM_QUALITY]: 11,
      [constants.BROTLI_PARAM_SIZE_HINT]: data.length,
    },
  });
  writeFileSync(file + ".br", b);
  writeFileSync(file + ".gz", gzipSync(data, { level: 9 }));
  raw += data.length;
  br += b.length;
  count++;
}

const kb = (n) => `${(n / 1024).toFixed(1)} KB`;
console.log(`compress: build ${build}; ${count} files, ${kb(raw)} -> ${kb(br)} brotli`);
