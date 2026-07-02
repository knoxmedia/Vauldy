#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""
Bitmap subtitle → WebVTT via Tesseract (PGS: pgsrip; VobSub: ffmpeg + tesseract / mkvextract).

Dependencies:
  - ffmpeg, ffprobe
  - tesseract + tessdata (TESSDATA_PREFIX optional)
  - pip install pgsrip  (PGS / Blu-ray; uses Tesseract internally)
  - MKVToolNix (mkvmerge, mkvextract) — optional, for embedded dvd_subtitle in MKV

Knox subtitle.graphical_ocr.shell is not used; the Go service invokes this script directly.
"""
from __future__ import annotations

import argparse
import json
import os
import re
import shutil
import subprocess
import sys
import tempfile
from pathlib import Path


def run(cmd: list[str], env: dict | None = None, cwd: str | None = None) -> None:
    run_with_stdin(cmd, None, env, cwd)


def run_with_stdin(cmd: list[str], stdin_src, env: dict | None = None, cwd: str | None = None) -> None:
    p = subprocess.run(cmd, capture_output=True, stdin=stdin_src, env=env, cwd=cwd)
    if p.returncode != 0:
        err = (p.stderr or p.stdout or b"")
        if isinstance(err, bytes):
            err = err.decode("utf-8", errors="replace")
        raise RuntimeError(str(err).strip() or f"command failed: {cmd}")


def ffprobe_json(ffprobe: str, path: str) -> dict:
    cmd = [ffprobe, "-v", "quiet", "-print_format", "json", "-show_streams", path]
    stdin_src = None
    if path == "pipe:0":
        import sys
        stdin_src = sys.stdin.buffer
    p = subprocess.run(cmd, capture_output=True, stdin=stdin_src)
    if p.returncode != 0:
        raise RuntimeError((p.stderr or b"").decode("utf-8", errors="replace") or "ffprobe failed")
    return json.loads(p.stdout.decode("utf-8", errors="replace"))


def subtitle_ordinal(streams: list[dict], global_index: int) -> int:
    n = 0
    for st in streams:
        if st.get("codec_type") != "subtitle":
            continue
        if int(st.get("index", -1)) == global_index:
            return n
        n += 1
    return -1


def mkv_subtitle_track_id(mkvmerge: str, mkv_path: str, subtitle_index: int) -> int:
    p = subprocess.run([mkvmerge, "-J", mkv_path], capture_output=True, text=True)
    if p.returncode != 0:
        raise RuntimeError(p.stderr or "mkvmerge -J failed")
    data = json.loads(p.stdout)
    tracks = [t for t in data.get("tracks", []) if t.get("type") == "subtitles"]
    if subtitle_index < 0 or subtitle_index >= len(tracks):
        raise RuntimeError("subtitle track index out of range for mkvmerge")
    tid = tracks[subtitle_index].get("id")
    if tid is None:
        tid = tracks[subtitle_index].get("properties", {}).get("number")
    return int(tid)


def srt_block_to_seconds(start: str) -> float:
    # 00:00:01,234 or 00:00:01.234
    m = re.match(r"(\d+):(\d+):(\d+)[,.](\d+)", start.strip())
    if not m:
        return 0.0
    h, mi, s, ms = map(int, m.groups())
    return h * 3600 + mi * 60 + s + ms / 1000.0


def srt_to_vtt(srt_text: str) -> str:
    lines_out = ["WEBVTT", ""]
    blocks = re.split(r"\n\s*\n", srt_text.strip())
    for block in blocks:
        ls = [l for l in block.splitlines() if l.strip()]
        if len(ls) < 2:
            continue
        # skip index line if numeric
        i = 0
        if re.match(r"^\d+$", ls[0].strip()):
            i = 1
        if i >= len(ls):
            continue
        times = ls[i]
        m = re.match(
            r"(\d+:\d+:\d+[,.]\d+)\s*-->\s*(\d+:\d+:\d+[,.]\d+)",
            times,
        )
        if not m:
            continue
        t0 = srt_block_to_seconds(m.group(1).replace(".", ","))
        t1 = srt_block_to_seconds(m.group(2).replace(".", ","))
        text = "\n".join(ls[i + 1 :]).strip()
        if not text:
            continue
        def fmt(t: float) -> str:
            ms = int(round(t * 1000))
            s, ms = divmod(ms, 1000)
            m, s = divmod(s, 60)
            h, m = divmod(m, 60)
            return f"{h:02d}:{m:02d}:{s:02d}.{ms:03d}"

        lines_out.append(f"{fmt(t0)} --> {fmt(t1)}")
        lines_out.append(text)
        lines_out.append("")
    return "\n".join(lines_out) + "\n"


def run_pgsrip_on_sup(
    pgsrip_bin: str | None,
    sup_path: Path,
    workdir: Path,
    extra_env: dict,
) -> Path:
    cmd: list[str]
    if pgsrip_bin:
        cmd = [pgsrip_bin, str(sup_path)]
    else:
        exe = shutil.which("pgsrip")
        if exe:
            cmd = [exe, str(sup_path)]
        else:
            cmd = [sys.executable, "-m", "pgsrip", str(sup_path)]
    env = {**os.environ, **extra_env}
    run(cmd, env=env, cwd=str(workdir))
    srts = list(workdir.glob("*.srt"))
    if not srts:
        srts = list(workdir.glob(f"{sup_path.stem}*.srt"))
    if not srts:
        raise RuntimeError("pgsrip produced no .srt; install pgsrip and tesseract tessdata")
    return srts[0]


def extract_sup(ffmpeg: str, src: str, stream_index: int, out_sup: Path) -> None:
    cmd = [ffmpeg, "-y", "-i", src, "-map", f"0:{stream_index}", "-c", "copy", str(out_sup)]
    stdin_src = None
    if src == "pipe:0":
        import sys
        stdin_src = sys.stdin.buffer
    run_with_stdin(cmd, stdin_src)


def extract_dvd_vobsub(
    mkvextract: str,
    mkv_path: str,
    mkv_track_id: int,
    out_prefix: Path,
) -> tuple[Path, Path]:
    out_dir = out_prefix.parent
    base = out_prefix.name
    idx = out_dir / f"{base}.idx"
    sub = out_dir / f"{base}.sub"
    run([mkvextract, "tracks", mkv_path, f"{mkv_track_id}:{str(out_prefix)}"])
    if not idx.is_file() or not sub.is_file():
        raise RuntimeError("mkvextract did not produce .idx/.sub")
    return idx, sub


def ocr_pngs_with_tesseract(tesseract: str, lang: str, pngs: list[Path], times_sec: list[float]) -> str:
    if len(pngs) != len(times_sec) and times_sec:
        m = min(len(pngs), len(times_sec))
        pngs, times_sec = pngs[:m], times_sec[:m]
    lines = ["WEBVTT", ""]
    dur = 2.0
    for i, png in enumerate(pngs):
        t0 = times_sec[i] if i < len(times_sec) else i * dur
        t1 = times_sec[i + 1] if i + 1 < len(times_sec) else t0 + dur
        cmd = [tesseract, str(png), "stdout", "-l", lang] if lang else [tesseract, str(png), "stdout"]
        p = subprocess.run(cmd, capture_output=True, text=True)
        if p.returncode != 0:
            continue
        txt = " ".join((p.stdout or "").split()).strip()
        if not txt:
            continue

        def fmt(t: float) -> str:
            ms = int(round(t * 1000))
            s, ms = divmod(ms, 1000)
            m, s = divmod(s, 60)
            h, m = divmod(m, 60)
            return f"{h:02d}:{m:02d}:{s:02d}.{ms:03d}"

        lines.append(f"{fmt(t0)} --> {fmt(t1)}")
        lines.append(txt)
        lines.append("")
    return "\n".join(lines) + "\n"


def parse_idx_times(idx_path: Path) -> list[float]:
    """Best-effort VobSub .idx timestamp lines → seconds."""
    times: list[float] = []
    raw = idx_path.read_text(encoding="utf-8", errors="replace")
    for line in raw.splitlines():
        line = line.strip()
        if not line.lower().startswith("timestamp:"):
            continue
        rest = line.split(":", 1)[-1]
        m = re.search(r"(\d+):(\d+):(\d+):(\d+)", rest)
        if not m:
            continue
        h, mi, s, fr = map(int, m.groups())
        # assume 25 fps for last field if it looks like frame count
        sec = h * 3600 + mi * 60 + s + fr / 25.0
        times.append(sec)
    return times


def vobsub_ffmpeg_pngs(ffmpeg: str, idx_path: Path, out_dir: Path) -> list[Path]:
    pattern = str(out_dir / "f%05d.png")
    try:
        run([ffmpeg, "-y", "-i", str(idx_path), "-vsync", "0", pattern])
    except Exception:
        return []
    return sorted(out_dir.glob("f*.png"))


def process_embedded(
    ffmpeg: str,
    ffprobe: str,
    src: str,
    stream_index: int,
    out_vtt: Path,
    tesseract: str,
    lang: str,
    tess_prefix: str | None,
    pgsrip_bin: str | None,
    mkvextract: str | None,
    mkvmerge: str | None,
) -> None:
    data = ffprobe_json(ffprobe, src)
    streams = data.get("streams") or []
    st = next((x for x in streams if int(x.get("index", -2)) == stream_index), None)
    if not st:
        raise RuntimeError("stream index not found")
    codec = (st.get("codec_name") or "").lower()
    env = os.environ.copy()
    if tess_prefix:
        env["TESSDATA_PREFIX"] = tess_prefix

    ord_sub = subtitle_ordinal(streams, stream_index)

    with tempfile.TemporaryDirectory(prefix="knox_ocr_") as td:
        td_path = Path(td)
        sup_path = td_path / "sub.sup"

        if codec in ("hdmv_pgs_subtitle", "pgssub", "pgs"):
            extract_sup(ffmpeg, src, stream_index, sup_path)
            srt_path = run_pgsrip_on_sup(pgsrip_bin, sup_path, td_path, env)
            vtt = srt_to_vtt(srt_path.read_text(encoding="utf-8", errors="replace"))
            out_vtt.parent.mkdir(parents=True, exist_ok=True)
            out_vtt.write_text(vtt, encoding="utf-8")
            return

        if codec in ("dvd_subtitle", "dvdsub", "vobsub") and src.lower().endswith(".mkv"):
            mx = (mkvextract or "").strip() or shutil.which("mkvextract") or ""
            mm = (mkvmerge or "").strip() or shutil.which("mkvmerge") or ""
            if not mx or not mm:
                raise RuntimeError("dvd_subtitle in MKV requires mkvextract and mkvmerge (MKVToolNix) on PATH")
            tid = mkv_subtitle_track_id(mm, src, ord_sub)
            prefix = td_path / "vob"
            idx_p, _ = extract_dvd_vobsub(mx, src, tid, prefix)
            times = parse_idx_times(idx_p)
            pngs = vobsub_ffmpeg_pngs(ffmpeg, idx_p, td_path)
            if not pngs:
                raise RuntimeError("could not decode VobSub to PNGs; check ffmpeg VobSub support")
            if not times:
                times = [float(i) * 2.0 for i in range(len(pngs))]
            vtt = ocr_pngs_with_tesseract(tesseract, lang or "eng", pngs, times)
            out_vtt.parent.mkdir(parents=True, exist_ok=True)
            out_vtt.write_text(vtt, encoding="utf-8")
            return

        raise RuntimeError(f"unsupported codec for OCR: {codec}")


def process_vobsub_sidecar(
    ffmpeg: str,
    idx_path: Path,
    out_vtt: Path,
    tesseract: str,
    lang: str,
    tess_prefix: str | None,
) -> None:
    env = os.environ.copy()
    if tess_prefix:
        env["TESSDATA_PREFIX"] = tess_prefix
    sub_path = idx_path.with_suffix(".sub")
    if not sub_path.is_file():
        raise RuntimeError(f"missing companion file: {sub_path}")
    with tempfile.TemporaryDirectory(prefix="knox_vob_") as td:
        td_path = Path(td)
        times = parse_idx_times(idx_path)
        pngs = vobsub_ffmpeg_pngs(ffmpeg, idx_path, td_path)
        if not pngs:
            raise RuntimeError("ffmpeg could not decode VobSub .idx to images")
        if not times:
            times = [float(i) * 2.0 for i in range(len(pngs))]
        vtt = ocr_pngs_with_tesseract(tesseract, lang or "eng", pngs, times)
        out_vtt.parent.mkdir(parents=True, exist_ok=True)
        out_vtt.write_text(vtt, encoding="utf-8")


def main() -> int:
    ap = argparse.ArgumentParser(description="Bitmap subtitles → WebVTT (Tesseract / pgsrip)")
    ap.add_argument("--mode", choices=("embedded", "vobsub"), default="embedded")
    ap.add_argument("--input", default="", help="Video file or pipe:0 (stdin = decrypted stream)")
    ap.add_argument("--stream-index", type=int, default=-1)
    ap.add_argument("--vobsub-idx", default="", help="VobSub .idx path (vobsub mode)")
    ap.add_argument("--output-vtt", required=True)
    ap.add_argument("--tesseract", default="tesseract")
    ap.add_argument("--lang", default="", help="Tesseract -l (e.g. chi_sim+eng)")
    ap.add_argument("--tessdata-prefix", default="")
    ap.add_argument("--ffmpeg", default="ffmpeg")
    ap.add_argument("--ffprobe", default="ffprobe")
    ap.add_argument("--pgsrip", default="")
    ap.add_argument("--mkvextract", default="")
    ap.add_argument("--mkvmerge", default="")
    args = ap.parse_args()

    tess_prefix = args.tessdata_prefix.strip() or None
    out_vtt = Path(args.output_vtt)

    try:
        if args.mode == "vobsub":
            if not args.vobsub_idx:
                print("error: --vobsub-idx required", file=sys.stderr)
                return 2
            process_vobsub_sidecar(
                args.ffmpeg,
                Path(args.vobsub_idx),
                out_vtt,
                args.tesseract,
                args.lang,
                tess_prefix,
            )
        else:
            if not args.input or args.stream_index < 0:
                print("error: --input and --stream-index required", file=sys.stderr)
                return 2
            process_embedded(
                args.ffmpeg,
                args.ffprobe,
                args.input,
                args.stream_index,
                out_vtt,
                args.tesseract,
                args.lang,
                tess_prefix,
                args.pgsrip.strip() or None,
                args.mkvextract.strip() or None,
                args.mkvmerge.strip() or None,
            )
    except Exception as e:
        print(f"error: {e}", file=sys.stderr)
        return 1
    print(f"ok: {out_vtt}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
