#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""
ASR → WebVTT for Knox media (subtitle.asr.provider: shell).

Dependencies:
  - FFmpeg/ffprobe on PATH (or FFMPEG_PATH / FFPROBE_PATH).
  - Whisper: pip install openai-whisper
  - Paraformer: pip install funasr modelscope (+ PyTorch)

Examples:
  python asr_to_vtt.py --engine whisper --input video.mp4 --output-vtt out.vtt
  python asr_to_vtt.py --engine whisper --input video.mp4 --output-vtt out.vtt --whisper-model medium --whisper-language zh
  python asr_to_vtt.py --engine paraformer --input video.mp4 --output-vtt out.vtt --paraformer-lite

Knox config.yml (adjust script path):
  subtitle:
    asr:
      provider: shell
      shell: >-
        python "E:/Projects/Knox/media/tools/asr/asr_to_vtt.py"
        --engine whisper --input "{input}" --output-vtt "{output_vtt}"
"""
from __future__ import annotations

import argparse
import json
import os
import subprocess
import sys
import tempfile
from pathlib import Path


def _which_or_env(name: str, env_key: str) -> str:
    v = os.environ.get(env_key, "").strip()
    if v:
        return v
    return name


def _run_cmd(cmd: list[str]) -> subprocess.CompletedProcess[str]:
    """Run a subprocess; decode stdout/stderr as UTF-8 (Windows GBK-safe)."""
    return subprocess.run(
        cmd,
        capture_output=True,
        text=True,
        encoding="utf-8",
        errors="replace",
    )


def extract_wav_16k_mono(src: Path, dst_wav: Path) -> None:
    ffmpeg = _which_or_env("ffmpeg", "FFMPEG_PATH")
    cmd = [
        ffmpeg,
        "-y",
        "-i",
        str(src),
        "-vn",
        "-ac",
        "1",
        "-ar",
        "16000",
        "-f",
        "wav",
        str(dst_wav),
    ]
    p = _run_cmd(cmd)
    if p.returncode != 0:
        raise RuntimeError(f"ffmpeg failed: {p.stderr or p.stdout}")


def probe_duration_sec(path: Path) -> float:
    ffprobe = _which_or_env("ffprobe", "FFPROBE_PATH")
    cmd = [
        ffprobe,
        "-v",
        "quiet",
        "-show_entries",
        "format=duration",
        "-of",
        "default=noprint_wrappers=1:nokey=1",
        str(path),
    ]
    p = _run_cmd(cmd)
    if p.returncode != 0:
        return 0.0
    try:
        return float((p.stdout or "").strip())
    except ValueError:
        return 0.0


def format_vtt_time(sec: float) -> str:
    if sec < 0:
        sec = 0.0
    ms_total = int(round(sec * 1000))
    ms = ms_total % 1000
    total_sec = ms_total // 1000
    s = total_sec % 60
    m = (total_sec // 60) % 60
    h = total_sec // 3600
    return f"{h:02d}:{m:02d}:{s:02d}.{ms:03d}"


def write_webvtt(path: Path, cues: list[tuple[float, float, str]]) -> None:
    lines = ["WEBVTT", ""]
    for start, end, text in cues:
        text = " ".join((text or "").strip().split())
        if not text:
            continue
        if end <= start:
            end = start + 0.5
        lines.append(f"{format_vtt_time(start)} --> {format_vtt_time(end)}")
        lines.append(text)
        lines.append("")
    path.parent.mkdir(parents=True, exist_ok=True)
    path.write_text("\n".join(lines), encoding="utf-8")


def run_whisper(
    wav_path: Path,
    output_vtt: Path,
    model_name: str,
    language: str | None,
    device: str | None,
) -> None:
    if os.name == "nt":
        os.environ.setdefault("PYTHONUTF8", "1")
        os.environ.setdefault("PYTHONIOENCODING", "utf-8")
    import whisper

    model = whisper.load_model(model_name, device=device or None)
    kwargs: dict = {"verbose": False}
    if language:
        kwargs["language"] = language
    result = model.transcribe(str(wav_path), **kwargs)
    cues: list[tuple[float, float, str]] = []
    for seg in result.get("segments") or []:
        cues.append((float(seg["start"]), float(seg["end"]), seg.get("text") or ""))
    if not cues and (result.get("text") or "").strip():
        dur = float(result.get("duration") or probe_duration_sec(wav_path) or 60.0)
        cues.append((0.0, dur, result["text"].strip()))
    write_webvtt(output_vtt, cues)


def _maybe_seconds(v: float) -> float:
    """FunASR may return ms for long values."""
    if v > 1e6:
        return v / 1000.0
    if v > 10000:
        return v / 1000.0
    return float(v)


def funasr_result_to_cues(res: object, fallback_duration: float) -> list[tuple[float, float, str]]:
    cues: list[tuple[float, float, str]] = []
    item: dict | None = None
    if isinstance(res, (list, tuple)) and len(res) > 0:
        item = res[0] if isinstance(res[0], dict) else None
    elif isinstance(res, dict):
        item = res
    if not item:
        return cues

    # sentence-level (common when sentence_timestamp=True)
    sinfo = item.get("sentence_info")
    if isinstance(sinfo, list) and sinfo:
        for s in sinfo:
            if not isinstance(s, dict):
                continue
            st = s.get("start", 0)
            ed = s.get("end", 0)
            txt = s.get("text") or s.get("sentence") or ""
            if st is None or ed is None:
                continue
            stf = _maybe_seconds(float(st))
            edf = _maybe_seconds(float(ed))
            if edf < stf:
                edf = stf + 0.5
            cues.append((stf, edf, str(txt)))
        if cues:
            return cues

    text = (item.get("text") or "").strip()

    if text:
        cues.append((0.0, max(fallback_duration, 1.0), text))
    return cues


def run_paraformer(
    wav_path: Path,
    output_vtt: Path,
    model_id: str,
    vad_model: str | None,
    punc_model: str | None,
    lite: bool,
) -> None:
    from funasr import AutoModel

    if lite:
        model = AutoModel(model="paraformer-zh")
    else:
        kwargs: dict = {"model": model_id}
        if vad_model:
            kwargs["vad_model"] = vad_model
            kwargs["vad_kwargs"] = {"max_single_segment_time": 60000}
        if punc_model:
            kwargs["punc_model"] = punc_model
        model = AutoModel(**kwargs)
    gen_kw: dict = {
        "input": str(wav_path),
        "cache": {},
        "pred_timestamp": True,
        "sentence_timestamp": True,
    }
    try:
        res = model.generate(**gen_kw)
    except TypeError:
        gen_kw.pop("pred_timestamp", None)
        gen_kw.pop("sentence_timestamp", None)
        res = model.generate(**gen_kw)

    dur = probe_duration_sec(wav_path)
    cues = funasr_result_to_cues(res, dur)
    if not cues:
        raise RuntimeError(f"Paraformer returned no cues: {json.dumps(res, ensure_ascii=False, default=str)[:800]}")
    write_webvtt(output_vtt, cues)


def main() -> int:
    if os.name == "nt":
        os.environ.setdefault("PYTHONUTF8", "1")
        os.environ.setdefault("PYTHONIOENCODING", "utf-8")
    ap = argparse.ArgumentParser(description="ASR (Whisper / Paraformer) → WebVTT")
    ap.add_argument("--engine", choices=("whisper", "paraformer"), required=True)
    ap.add_argument("--input", required=True, help="Video or audio file")
    ap.add_argument("--output-vtt", required=True, help="Output .vtt path")
    ap.add_argument("--whisper-model", default="small", help="Whisper model name (tiny/base/small/medium/large)")
    ap.add_argument("--whisper-language", default="", help="Force language code, e.g. zh, en (Whisper only)")
    ap.add_argument("--whisper-device", default="", help="cuda / cpu (Whisper only)")
    ap.add_argument(
        "--paraformer-model",
        default="iic/speech_paraformer-large-vad-punc_asr_nat-zh-cn-16k-common-vocab8404-pytorch",
        help="FunASR model id (ModelScope)",
    )
    ap.add_argument(
        "--paraformer-vad",
        default="iic/speech_fsmn_vad_zh-cn-16k-common-pytorch",
        help="VAD model id; use empty to disable",
    )
    ap.add_argument(
        "--paraformer-punc",
        default="iic/punc_ct-transformer_zh-cn-common-vocab272727-pytorch",
        help="Punctuation model id; use empty to disable",
    )
    ap.add_argument(
        "--paraformer-lite",
        action="store_true",
        help="Use small zh model paraformer-zh only (no VAD/punc; faster setup)",
    )
    args = ap.parse_args()

    src = Path(args.input).resolve()
    out = Path(args.output_vtt).resolve()
    if not src.is_file():
        print(f"error: input not found: {src}", file=sys.stderr)
        return 2

    suffix = src.suffix.lower()
    audio_only = suffix in (".wav", ".mp3", ".flac", ".m4a", ".aac", ".ogg", ".opus")

    try:
        with tempfile.TemporaryDirectory(prefix="knox_asr_") as td:
            td_path = Path(td)
            if audio_only:
                wav_path = td_path / "audio.wav"
                if suffix == ".wav":
                    # still normalize to 16k mono for models
                    extract_wav_16k_mono(src, wav_path)
                else:
                    extract_wav_16k_mono(src, wav_path)
            else:
                wav_path = td_path / "audio.wav"
                extract_wav_16k_mono(src, wav_path)

            lang = (args.whisper_language or "").strip() or None
            dev = (args.whisper_device or "").strip() or None

            if args.engine == "whisper":
                run_whisper(wav_path, out, args.whisper_model, lang, dev)
            else:
                vad = (args.paraformer_vad or "").strip() or None
                punc = (args.paraformer_punc or "").strip() or None
                run_paraformer(wav_path, out, args.paraformer_model, vad, punc, args.paraformer_lite)
    except Exception as e:
        print(f"error: {e}", file=sys.stderr)
        return 1

    print(f"ok: wrote {out}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
