// Generates realistic synthetic release titles (deterministic) that exercise
// TRaSH custom formats, with emphasis on German releases. Group names are
// taken from TRaSH ReleaseGroupSpecification patterns of the form ^(A|B)$.
const fs = require("fs");
const path = require("path");
const [, , jsonRoot, outDir] = process.argv;

let seed = 1337;
function rnd() { seed = (Math.imul(seed, 1103515245) + 12345) >>> 0; return (seed >>> 1) / 0x80000000; }
function pick(a) { return a[Math.floor(rnd() * a.length)]; }
function maybe(p, v) { return rnd() < p ? v : ""; }

function groupsFrom(app) {
  const dir = path.join(jsonRoot, app, "cf");
  const out = new Map();
  for (const f of fs.readdirSync(dir)) {
    const cf = JSON.parse(fs.readFileSync(path.join(dir, f), "utf8"));
    for (const s of cf.specifications || []) {
      if (s.implementation !== "ReleaseGroupSpecification" || s.negate) continue;
      const v = String(s.fields?.value ?? "");
      const m = v.match(/^\^\(?([A-Za-z0-9_|.\\-]+)\)?\$$/);
      if (!m) continue;
      for (const g of m[1].split("|")) {
        const clean = g.replace(/\\/g, "");
        if (/^[A-Za-z0-9_.-]{2,}$/.test(clean)) out.set(clean, cf.name);
      }
    }
  }
  return [...out.keys()];
}

const series = ["Dark", "Babylon.Berlin", "Der.Tatortreiniger", "The.Office", "Breaking.Bad", "Stranger.Things", "Haus.des.Geldes", "How.to.Sell.Drugs.Online.Fast", "4.Blocks", "Das.Boot", "The.Last.of.Us", "Shogun.2024", "Severance", "Andor", "Attack.on.Titan", "One.Piece", "Frieren"];
const movies = ["Der.Pate", "Oppenheimer", "Das.Leben.der.Anderen", "Lola.rennt", "Im.Westen.nichts.Neues", "Dune.Part.Two", "Inception", "Parasite", "Der.Untergang", "Spirited.Away", "Mad.Max.Fury.Road", "The.Matrix"];
const years = ["1972", "1999", "2004", "2010", "2015", "2022", "2023", "2024"];
const langs = ["German", "GERMAN", "German.DL", "GERMAN.DL", "German.DUBBED.DL", "German.DL.AC3", "German.AC3D.DL", "German.ML", "German.Subbed", "German.MD", "MULTi", "MULTi.GERMAN", "English", "ENGLiSH", "FRENCH", "TRUEFRENCH", "Japanese", "ITALiAN", "SPANiSH", "", "", ""];
const res = ["2160p", "1080p", "720p", "576p", "480p", ""];
const sources = ["WEB", "WEB-DL", "WEBRip", "AMZN.WEB-DL", "NF.WEB-DL", "DSNP.WEB-DL", "ATVP.WEB-DL", "WEB.H264", "BluRay", "BDRip", "UHD.BluRay", "BluRay.REMUX", "UHD.BluRay.REMUX", "HDTV", "DVDRip", "DVD", "COMPLETE.BLURAY", "HDRip", "REMUX"];
const codecs = ["x264", "x265", "h264", "h265", "HEVC", "AVC", "AV1", "XviD", ""];
const audio = ["DTS-HD.MA.5.1", "DD+5.1", "DDP5.1", "EAC3", "AAC2.0", "TrueHD.Atmos.7.1", "AC3", "DTS", "FLAC", "Opus", ""];
const hdr = ["DV", "HDR", "HDR10", "HDR10Plus", "DV.HDR", "HLG", "SDR", "", "", ""];
const extras = ["PROPER", "REPACK", "REPACK2", "iNTERNAL", "REAL.PROPER", "READ.NFO", "UNCUT", "EXTENDED", "Directors.Cut", "IMAX", "Remastered", "Hybrid", ""];

function build(app, groups, n) {
  const out = new Set();
  while (out.size < n) {
    const parts = [];
    if (app === "sonarr") {
      parts.push(pick(series));
      const s = String(1 + Math.floor(rnd() * 5)).padStart(2, "0");
      const e = String(1 + Math.floor(rnd() * 12)).padStart(2, "0");
      const kind = rnd();
      if (kind < 0.7) parts.push(`S${s}E${e}`);
      else if (kind < 0.8) parts.push(`S${s}E${e}E${String(+e + 1).padStart(2, "0")}`);
      else parts.push(`S${s}`);
    } else {
      parts.push(pick(movies), pick(years));
    }
    for (const p of [maybe(0.25, pick(extras)), pick(langs), pick(res), pick(hdr), pick(sources), pick(audio), pick(codecs)]) if (p) parts.push(p);
    let t = parts.join(".");
    if (rnd() < 0.92) t += "-" + pick(groups);
    out.add(t);
  }
  return [...out];
}

for (const app of ["sonarr", "radarr"]) {
  const groups = groupsFrom(app);
  const titles = build(app, groups, 1500);
  fs.writeFileSync(path.join(outDir, `${app}-synthetic.txt`), titles.join("\n") + "\n");
  console.log(app, groups.length, "groups,", titles.length, "titles");
}
