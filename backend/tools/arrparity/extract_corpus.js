// Extracts release-title inputs from Sonarr/Radarr parser test sources and
// ptt-go tests into plain text files (one title per line). Only the input
// strings are taken; expected outputs come from querying the running oracle.
const fs = require("fs");
const path = require("path");

const [, , arrRoot, pttRoot, outDir] = process.argv;

function walk(dir) {
  return fs.readdirSync(dir, { withFileTypes: true }).flatMap((e) =>
    e.isDirectory() ? walk(path.join(dir, e.name)) : [path.join(dir, e.name)]
  );
}

function looksLikeRelease(t) {
  if (t.length < 10 || t.length > 300) return false;
  if (/[\r\n]/.test(t)) return false;
  // Needs at least one separator typical for release names.
  return /[._ \-\[\]]/.test(t);
}

const testCase = /\[TestCase\(@?"((?:[^"\\]|\\.|"")*)"/g;
for (const app of ["Sonarr", "Radarr"]) {
  const set = new Set();
  const dir = path.join(arrRoot, `${app}-test`, "src", "NzbDrone.Core.Test", "ParserTests");
  for (const f of walk(dir)) {
    if (!f.endsWith(".cs")) continue;
    const src = fs.readFileSync(f, "utf8");
    for (const m of src.matchAll(testCase)) {
      let t = m[1].replace(/""/g, '"').replace(/\\\\/g, "\\").replace(/\\"/g, '"');
      if (looksLikeRelease(t)) set.add(t);
    }
  }
  fs.writeFileSync(path.join(outDir, `${app.toLowerCase()}-tests.txt`), [...set].join("\n") + "\n");
  console.log(app, set.size);
}

const ptt = new Set();
const pttCall = /\.?Parse\("((?:[^"\\]|\\.)*)"/g;
for (const f of fs.readdirSync(pttRoot)) {
  if (!f.endsWith("_test.go")) continue;
  const src = fs.readFileSync(path.join(pttRoot, f), "utf8");
  for (const m of src.matchAll(pttCall)) {
    const t = m[1].replace(/\\"/g, '"').replace(/\\\\/g, "\\");
    if (looksLikeRelease(t)) ptt.add(t);
  }
}
fs.writeFileSync(path.join(outDir, "ptt.txt"), [...ptt].join("\n") + "\n");
console.log("ptt", ptt.size);
