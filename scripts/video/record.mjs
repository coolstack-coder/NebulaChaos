// Records the NebulaChaos demo video from a manifest of segments.
//
//   NODE_PATH=<dir with playwright> node scripts/video/record.mjs scripts/video/manifest.json
//
// Segment types:
//   stage   a title / explainer card            {scene, data, dur}
//   term    real CLI output replayed            {cast, speed, hold}
//   replay  animated replay of a real run       {data, from, to, speed, freeze, label}
//   page    an HTML report, scrolled            {file, steps:[{y, wait}]}
// Every segment carries subtitles: [[start_s, end_s, text], ...] relative
// to the segment. They become an .srt and are burned into the video.
import { chromium } from "playwright";
import { execFileSync } from "node:child_process";
import fs from "node:fs";
import path from "node:path";
import { pathToFileURL } from "node:url";

const manifestPath = process.argv[2];
const M = JSON.parse(fs.readFileSync(manifestPath, "utf8"));
const base = path.dirname(path.resolve(manifestPath));
const here = path.dirname(new URL(import.meta.url).pathname);
const FF = process.env.FFMPEG || "ffmpeg";
const OUT = path.resolve(base, M.out || "../../docs/media");
const TMP = path.resolve(base, M.tmp || "../../.cache/video");
fs.rmSync(TMP, { recursive: true, force: true });
fs.mkdirSync(TMP, { recursive: true });
fs.mkdirSync(OUT, { recursive: true });
const W = 1920, H = 1080;
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
const rel = (p) => path.resolve(base, p);

const browser = await chromium.launch();
const parts = [];
const subs = [];
let clock = 0;

for (const [i, seg] of M.segments.entries()) {
  const dir = path.join(TMP, `seg${i}`);
  const ctx = await browser.newContext({ viewport: { width: W, height: H }, recordVideo: { dir, size: { width: W, height: H } } });
  const page = await ctx.newPage();
  const t0 = Date.now();
  let ready;
  if (seg.type === "stage") {
    await page.goto(pathToFileURL(path.join(here, "stage.html")).href);
    await page.waitForLoadState("networkidle");
    ready = Date.now();
    await page.evaluate(([s, d]) => window.show(s, d), [seg.scene, seg.data || {}]);
    await sleep(seg.dur * 1000);
  } else if (seg.type === "term") {
    await page.goto(pathToFileURL(path.join(here, "term.html")).href);
    await page.waitForLoadState("networkidle");
    ready = Date.now();
    const cast = JSON.parse(fs.readFileSync(rel(seg.cast), "utf8"));
    if (seg.drop) cast.lines = cast.lines.filter((l) => !new RegExp(seg.drop).test(l.s));
    await page.evaluate(([c, s, g]) => window.play(c, s, g), [cast, seg.speed || 1, seg.maxGap || 1.2]);
    await sleep((seg.hold || 3) * 1000);
  } else if (seg.type === "replay") {
    const q = new URLSearchParams({ from: seg.from ?? 0, speed: seg.speed ?? 1, freeze: seg.freeze ?? 9, label: seg.label || "" });
    if (seg.to != null) q.set("to", seg.to);
    await page.goto(pathToFileURL(path.join(here, "replay.html")).href + "?" + q);
    await page.waitForLoadState("networkidle");
    const data = JSON.parse(fs.readFileSync(rel(seg.data), "utf8"));
    ready = Date.now();
    await page.evaluate((d) => window.start(d), data);
    if (seg.still) await page.screenshot({ path: path.join(OUT, seg.still) });
  } else if (seg.type === "page") {
    await page.goto(pathToFileURL(rel(seg.file)).href);
    await page.waitForLoadState("load");
    ready = Date.now();
    for (const st of seg.steps) {
      await page.evaluate((y) => window.scrollTo({ top: y, behavior: "smooth" }), st.y || 0);
      await sleep((st.wait || 2) * 1000);
      if (st.still) await page.screenshot({ path: path.join(OUT, st.still) });
    }
  }
  const total = (Date.now() - t0) / 1000;
  const lead = (ready - t0) / 1000;
  await ctx.close();
  const webm = fs.readdirSync(dir).find((f) => f.endsWith(".webm"));
  const mp4 = path.join(TMP, `seg${i}.mp4`);
  const len = total - lead;
  execFileSync(FF, ["-y", "-loglevel", "error", "-ss", lead.toFixed(3), "-i", path.join(dir, webm), "-t", len.toFixed(3),
    "-vf", `scale=${W}:${H},fps=30,format=yuv420p`, "-c:v", "libx264", "-preset", "medium", "-crf", "20", mp4]);
  for (const [a, b, text] of seg.subs || []) {
    subs.push([clock + a, clock + Math.min(b, len - 0.05), text]);
  }
  parts.push(mp4);
  console.log(`segment ${i} (${seg.type}${seg.scene ? " " + seg.scene : ""}): ${len.toFixed(1)}s`);
  clock += len;
}
await browser.close();

const ts = (s) => {
  const ms = Math.round(s * 1000);
  const h = Math.floor(ms / 3600000), m = Math.floor(ms / 60000) % 60, sec = Math.floor(ms / 1000) % 60;
  return `${String(h).padStart(2, "0")}:${String(m).padStart(2, "0")}:${String(sec).padStart(2, "0")},${String(ms % 1000).padStart(3, "0")}`;
};
const srt = subs.map(([a, b, t], i) => `${i + 1}\n${ts(a)} --> ${ts(b)}\n${t}\n`).join("\n");
const srtPath = path.join(OUT, `${M.name}.srt`);
fs.writeFileSync(srtPath, srt);

const list = path.join(TMP, "list.txt");
fs.writeFileSync(list, parts.map((p) => `file '${p}'`).join("\n"));
const joined = path.join(TMP, "joined.mp4");
execFileSync(FF, ["-y", "-loglevel", "error", "-f", "concat", "-safe", "0", "-i", list, "-c", "copy", joined]);
fs.copyFileSync(srtPath, path.join(TMP, "subs.srt"));
const style = "FontName=Helvetica Neue,FontSize=13,PrimaryColour=&H00FFFFFF,OutlineColour=&H90120A0C,BorderStyle=3,Outline=6,Shadow=0,MarginV=18,Bold=1";
const final = path.join(OUT, `${M.name}.mp4`);
execFileSync(FF, ["-y", "-loglevel", "error", "-i", joined, "-vf", `subtitles=subs.srt:force_style='${style}'`,
  "-c:v", "libx264", "-preset", "slow", "-crf", String(M.crf || 26), "-pix_fmt", "yuv420p", "-movflags", "+faststart", final], { cwd: TMP });
console.log(`video: ${final} (${clock.toFixed(1)}s)`);

if (M.gif) {
  const g = M.gif;
  execFileSync(FF, ["-y", "-loglevel", "error", "-ss", String(g.from), "-t", String(g.dur), "-i", final, "-vf",
    `fps=${g.fps || 10},scale=${g.width || 960}:-1:flags=lanczos,split[a][b];[a]palettegen=max_colors=128:stats_mode=diff[p];[b][p]paletteuse=dither=bayer:bayer_scale=5:diff_mode=rectangle`,
    "-loop", "0", path.join(OUT, g.name)]);
  console.log(`gif: ${path.join(OUT, g.name)}`);
}
