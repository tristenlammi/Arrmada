// check-tokens: fail the build on a design token that doesn't exist.
//
// A var(--x) whose --x is never declared doesn't error anywhere: the property
// silently computes to nothing, so a chip loses its fill or a modal its shadow
// and nobody notices. This script runs as `prebuild` and exits 1 when:
//   (a) any file uses var(--x) and no token block in src/index.css declares --x;
//   (b) one of the four theme blocks is missing a name another block declares
//       (so a theme toggle never falls back to the other theme's value);
//   (c) a colour/shadow token has no alias in tailwind.config.js.
//
// Plain node, no dependencies. Run it by hand with `node scripts/check-tokens.mjs`.
import { readFileSync, readdirSync, statSync } from "node:fs";
import { join, relative, extname } from "node:path";
import { fileURLToPath } from "node:url";

const root = fileURLToPath(new URL("..", import.meta.url));
const cssPath = join(root, "src", "index.css");

// The four theme blocks, identified by their selector (and enclosing @media).
const BLOCKS = [
  { label: ":root (dark default)", media: null, selector: ":root" },
  { label: "@media light :root", media: "prefers-color-scheme: light", selector: ":root" },
  { label: ':root[data-theme="dark"]', media: null, selector: ':root[data-theme="dark"]' },
  { label: ':root[data-theme="light"]', media: null, selector: ':root[data-theme="light"]' },
];

// Tailwind's own runtime variables are defined by its generated CSS.
const IGNORED = (name) => name.startsWith("--tw-");

const stripBlockComments = (s) => s.replace(/\/\*[\s\S]*?\*\//g, "");

// parseRules walks the CSS one brace level at a time and returns every rule as
// { selector, media, body }, where media is the enclosing @media prelude (if any).
function parseRules(css) {
  const rules = [];
  const walk = (text, media) => {
    let i = 0;
    while (i < text.length) {
      const open = text.indexOf("{", i);
      if (open < 0) break;
      const prelude = text.slice(i, open).trim();
      let depth = 1;
      let j = open + 1;
      for (; j < text.length && depth > 0; j++) {
        if (text[j] === "{") depth++;
        else if (text[j] === "}") depth--;
      }
      const body = text.slice(open + 1, j - 1);
      // Text before the prelude may hold `@tailwind x;` statements; keep the last statement.
      const sel = prelude.split(";").pop().trim();
      if (sel.startsWith("@media")) walk(body, sel.replace(/^@media\s*/, "").replace(/^\(|\)$/g, "").trim());
      else rules.push({ selector: sel, media, body });
      i = j;
    }
  };
  walk(stripBlockComments(css), null);
  return rules;
}

const declared = (body) => new Set([...body.matchAll(/(--[A-Za-z0-9_-]+)\s*:/g)].map((m) => m[1]));

function walkFiles(dir, exts, out = []) {
  for (const name of readdirSync(dir)) {
    const p = join(dir, name);
    if (statSync(p).isDirectory()) walkFiles(p, exts, out);
    else if (exts.includes(extname(name))) out.push(p);
  }
  return out;
}

const errors = [];
const rules = parseRules(readFileSync(cssPath, "utf8"));

const blockSets = BLOCKS.map((b) => {
  const hits = rules.filter((r) => r.selector === b.selector && r.media === b.media);
  if (hits.length !== 1) {
    errors.push(`src/index.css: expected exactly one ${b.label} block, found ${hits.length}`);
    return { ...b, names: new Set() };
  }
  return { ...b, names: declared(hits[0].body) };
});
const defined = new Set(blockSets.flatMap((b) => [...b.names]));
// Anything else index.css declares (none today) counts as defined too, so a
// component-scoped custom property doesn't trip (a).
for (const r of rules) for (const n of declared(r.body)) defined.add(n);

// (b) every theme block declares the full set.
for (const b of blockSets) {
  const missing = [...new Set(blockSets.flatMap((x) => [...x.names]))].filter((n) => !b.names.has(n));
  if (missing.length) errors.push(`src/index.css ${b.label} is missing: ${missing.sort().join(", ")}`);
}

// (a) every var(--x) in the app resolves.
const files = [...walkFiles(join(root, "src"), [".ts", ".tsx", ".css"]), join(root, "index.html"), join(root, "tailwind.config.js")];
for (const f of files) {
  let text = readFileSync(f, "utf8");
  if (f.endsWith(".css")) text = stripBlockComments(text);
  const lines = text.split("\n");
  lines.forEach((line, n) => {
    for (const m of line.matchAll(/var\(\s*(--[A-Za-z0-9_-]+)/g)) {
      if (!defined.has(m[1]) && !IGNORED(m[1])) {
        errors.push(`${relative(root, f).replace(/\\/g, "/")}:${n + 1}: ${m[1]} is not defined in src/index.css`);
      }
    }
  });
}

// (c) Tailwind can reach every token. --shadow is aliased as boxShadow.panel.
const tw = readFileSync(join(root, "tailwind.config.js"), "utf8");
const aliased = new Set([...tw.matchAll(/var\((--[A-Za-z0-9_-]+)\)/g)].map((m) => m[1]));
const unaliased = [...(blockSets[0]?.names ?? [])].filter((n) => !aliased.has(n));
if (unaliased.length) errors.push(`tailwind.config.js has no alias for: ${unaliased.sort().join(", ")}`);

if (errors.length) {
  console.error("check-tokens: design token problems found:\n  " + errors.join("\n  "));
  process.exit(1);
}
console.log(`check-tokens: ${defined.size} tokens, ${files.length} files OK`);
