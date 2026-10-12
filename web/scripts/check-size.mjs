// Fails the build when gzipped JS+CSS in dist exceeds the budget (plan: 60 KB).
import { readdirSync, readFileSync } from "node:fs";
import { gzipSync } from "node:zlib";
import { join } from "node:path";

const BUDGET = 60 * 1024;
const dir = new URL("../dist/assets/", import.meta.url).pathname;
let total = 0;
for (const f of readdirSync(dir)) {
  if (!/\.(js|css)$/.test(f)) continue;
  const n = gzipSync(readFileSync(join(dir, f))).length;
  total += n;
  console.log(`${f}  ${(n / 1024).toFixed(1)} KB gz`);
}
console.log(`total ${(total / 1024).toFixed(1)} KB gz (budget ${BUDGET / 1024} KB)`);
if (total > BUDGET) { console.error("bundle budget exceeded"); process.exit(1); }
