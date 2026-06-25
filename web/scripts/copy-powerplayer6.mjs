import { cpSync, existsSync, mkdirSync } from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

const here = path.dirname(fileURLToPath(import.meta.url));
const webRoot = path.resolve(here, "..");
const mediaRoot = path.resolve(webRoot, "..");
const dest = path.join(webRoot, "dist", "static", "powerplayer6");

const sources = [
  path.join(mediaRoot, "static", "powerplayer6"),
  path.join(mediaRoot, "data", "static", "powerplayer6"),
];

const src = sources.find((p) => existsSync(p));
if (!src) {
  console.warn(
    "[knox] skip copy-powerplayer6: no source at static/powerplayer6 or data/static/powerplayer6",
  );
  process.exit(0);
}

mkdirSync(path.dirname(dest), { recursive: true });
cpSync(src, dest, { recursive: true, force: true });
console.log(`[knox] copied ${path.relative(mediaRoot, src)} -> web/dist/static/powerplayer6`);
