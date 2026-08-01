#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""
ASR → WebVTT for Knox media (subtitle.asr.provider: shell).

Knox Go side extracts 16 kHz mono MP3 once for video inputs and passes that path as
--input. This script must not re-demux the full movie when --input is already audio.

Long audio (> chunk seconds) is split and transcribed in segments, then merged with
time offsets. CUDA defaults to 1 worker (safe VRAM); raise with --asr-workers.
"""
from __future__ import annotations

import argparse
import concurrent.futures
import json
import os
import subprocess
import sys
import tempfile
from pathlib import Path

AUDIO_SUFFIXES = {".wav", ".mp3", ".flac", ".m4a", ".aac", ".ogg", ".opus", ".wma"}
DEFAULT_CHUNK_SEC = 12 * 60  # 12 minutes


def _which_or_env(name: str, env_key: str) -> str:
    v = os.environ.get(env_key, "").strip()
    if v:
        return v
    return name


def _run_cmd(cmd: list[str]) -> subprocess.CompletedProcess[str]:
    return subprocess.run(
        cmd,
        capture_output=True,
        text=True,
        encoding="utf-8",
        errors="replace",
    )


def extract_audio_16k_mono(src: Path, dst: Path) -> Path:
    """Extract first audio stream only; prefer MP3 then FLAC then WAV."""
    ffmpeg = _which_or_env("ffmpeg", "FFMPEG_PATH")
    base = dst.with_suffix("")
    attempts = [
        (base.with_suffix(".mp3"), ["-c:a", "libmp3lame", "-q:a", "4"]),
        (base.with_suffix(".flac"), ["-c:a", "flac"]),
        (base.with_suffix(".wav"), ["-c:a", "pcm_s16le"]),
    ]
    last_err = ""
    for out, codec in attempts:
        cmd = [
            ffmpeg, "-y", "-i", str(src),
            "-map", "0:a:0", "-vn", "-sn",
            "-ac", "1", "-ar", "16000",
            *codec, str(out),
        ]
        p = _run_cmd(cmd)
        if p.returncode == 0 and out.is_file() and out.stat().st_size > 0:
            if out != dst:
                dst.write_bytes(out.read_bytes())
                try:
                    out.unlink()
                except OSError:
                    pass
            return dst
        last_err = p.stderr or p.stdout or f"exit {p.returncode}"
        if out.exists():
            try:
                out.unlink()
            except OSError:
                pass
    raise RuntimeError(f"ffmpeg audio extract failed: {last_err}")


def probe_duration_sec(path: Path) -> float:
    ffprobe = _which_or_env("ffprobe", "FFPROBE_PATH")
    cmd = [
        ffprobe, "-v", "quiet",
        "-show_entries", "format=duration",
        "-of", "default=noprint_wrappers=1:nokey=1",
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


def prepare_asr_audio(src: Path, td: Path) -> Path:
    """Return audio path usable by Whisper. Skip re-extract when input is already audio."""
    if src.suffix.lower() in AUDIO_SUFFIXES:
        return src
    out = td / "audio.mp3"
    return extract_audio_16k_mono(src, out)


def split_audio_chunks(audio: Path, td: Path, chunk_sec: int) -> list[tuple[Path, float]]:
    """Split audio into ~chunk_sec segments; returns (path, start_offset_sec)."""
    dur = probe_duration_sec(audio)
    if dur <= 0 or dur <= chunk_sec * 1.25:
        return [(audio, 0.0)]
    ffmpeg = _which_or_env("ffmpeg", "FFMPEG_PATH")
    chunks: list[tuple[Path, float]] = []
    start = 0.0
    idx = 0
    while start < dur - 0.5:
        out = td / f"chunk_{idx:04d}{audio.suffix or '.mp3'}"
        cmd = [
            ffmpeg, "-y", "-ss", f"{start:.3f}", "-t", str(chunk_sec),
            "-i", str(audio), "-ac", "1", "-ar", "16000", "-c", "copy", str(out),
        ]
        # copy may fail across codecs with -ss after -i issues; re-encode fallback
        p = _run_cmd(cmd)
        if p.returncode != 0 or not out.is_file() or out.stat().st_size == 0:
            if out.exists():
                try:
                    out.unlink()
                except OSError:
                    pass
            cmd = [
                ffmpeg, "-y", "-ss", f"{start:.3f}", "-t", str(chunk_sec),
                "-i", str(audio),
                "-ac", "1", "-ar", "16000", "-c:a", "libmp3lame", "-q:a", "4", str(out.with_suffix(".mp3")),
            ]
            out = out.with_suffix(".mp3")
            p = _run_cmd(cmd)
            if p.returncode != 0 or not out.is_file() or out.stat().st_size == 0:
                raise RuntimeError(f"ffmpeg chunk split failed at {start:.1f}s: {p.stderr or p.stdout}")
        chunks.append((out, start))
        start += chunk_sec
        idx += 1
        if idx > 500:
            break
    return chunks or [(audio, 0.0)]


def run_whisper(
    wav_path: Path,
    model_name: str,
    language: str | None,
    device: str | None,
) -> list[tuple[float, float, str]]:
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
    return cues


def run_faster_whisper(
    wav_path: Path,
    model_name: str,
    language: str | None,
    device: str | None,
) -> list[tuple[float, float, str]]:
    if os.name == "nt":
        os.environ.setdefault("PYTHONUTF8", "1")
        os.environ.setdefault("PYTHONIOENCODING", "utf-8")
    from faster_whisper import WhisperModel

    dev = (device or "cpu").strip() or "cpu"
    compute_type = "float16" if dev.startswith("cuda") else "int8"
    model = WhisperModel(model_name, device=dev, compute_type=compute_type)
    segments, _info = model.transcribe(
        str(wav_path),
        language=language,
        vad_filter=True,
    )
    cues: list[tuple[float, float, str]] = []
    for seg in segments:
        cues.append((float(seg.start), float(seg.end), seg.text or ""))
    if not cues:
        raise RuntimeError("faster-whisper returned no segments")
    return cues


def _maybe_seconds(v: float) -> float:
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
    model_id: str,
    vad_model: str | None,
    punc_model: str | None,
    lite: bool,
) -> list[tuple[float, float, str]]:
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
    return cues


def offset_cues(cues: list[tuple[float, float, str]], offset: float) -> list[tuple[float, float, str]]:
    if offset <= 0:
        return cues
    return [(s + offset, e + offset, t) for s, e, t in cues]


def transcribe_chunks(
    engine: str,
    chunks: list[tuple[Path, float]],
    workers: int,
    **kwargs,
) -> list[tuple[float, float, str]]:
    def one(item: tuple[Path, float]) -> list[tuple[float, float, str]]:
        path, offset = item
        if engine == "whisper":
            cues = run_whisper(path, kwargs["model_name"], kwargs["language"], kwargs["device"])
        elif engine == "faster-whisper":
            cues = run_faster_whisper(path, kwargs["model_name"], kwargs["language"], kwargs["device"])
        else:
            cues = run_paraformer(
                path,
                kwargs["paraformer_model"],
                kwargs["vad"],
                kwargs["punc"],
                kwargs["lite"],
            )
        return offset_cues(cues, offset)

    if len(chunks) == 1 or workers <= 1:
        merged: list[tuple[float, float, str]] = []
        for ch in chunks:
            merged.extend(one(ch))
        return merged

    merged = []
    with concurrent.futures.ThreadPoolExecutor(max_workers=workers) as pool:
        futs = [pool.submit(one, ch) for ch in chunks]
        for fut in futs:
            merged.extend(fut.result())
    merged.sort(key=lambda c: c[0])
    return merged


def main() -> int:
    if os.name == "nt":
        os.environ.setdefault("PYTHONUTF8", "1")
        os.environ.setdefault("PYTHONIOENCODING", "utf-8")
    ap = argparse.ArgumentParser(description="ASR (Whisper / Faster-Whisper / Paraformer) → WebVTT")
    ap.add_argument("--engine", choices=("whisper", "faster-whisper", "paraformer"), required=True)
    ap.add_argument("--input", required=True, help="Video or audio file (prefer Go-extracted 16k mono MP3)")
    ap.add_argument("--output-vtt", required=True, help="Output .vtt path")
    ap.add_argument("--whisper-model", default="small", help="Whisper model name (tiny/base/small/medium/large)")
    ap.add_argument("--whisper-language", default="", help="Force language code, e.g. zh, en")
    ap.add_argument("--whisper-device", default="", help="cuda / cpu")
    ap.add_argument("--chunk-seconds", type=int, default=DEFAULT_CHUNK_SEC, help="Segment length for long media (default 720)")
    ap.add_argument("--asr-workers", type=int, default=1, help="Parallel chunk workers (CUDA: keep 1–2)")
    ap.add_argument(
        "--paraformer-model",
        default="iic/speech_paraformer-large-vad-punc_asr_nat-zh-cn-16k-common-vocab8404-pytorch",
        help="FunASR model id (ModelScope)",
    )
    ap.add_argument("--paraformer-vad", default="iic/speech_fsmn_vad_zh-cn-16k-common-pytorch", help="VAD model id")
    ap.add_argument(
        "--paraformer-punc",
        default="iic/punc_ct-transformer_zh-cn-common-vocab272727-pytorch",
        help="Punctuation model id",
    )
    ap.add_argument("--paraformer-lite", action="store_true", help="Use small zh model only")
    args = ap.parse_args()

    src = Path(args.input).resolve()
    out = Path(args.output_vtt).resolve()
    if not src.is_file():
        print(f"error: input not found: {src}", file=sys.stderr)
        return 2

    chunk_sec = max(60, int(args.chunk_seconds or DEFAULT_CHUNK_SEC))
    workers = max(1, min(4, int(args.asr_workers or 1)))
    lang = (args.whisper_language or "").strip() or None
    dev = (args.whisper_device or "").strip() or None
    # CUDA: allow 2 chunk workers by default when user left --asr-workers at 1
    if workers == 1 and (dev or "").startswith("cuda") and args.asr_workers == 1:
        workers = 2

    try:
        with tempfile.TemporaryDirectory(prefix="knox_asr_") as td:
            td_path = Path(td)
            audio = prepare_asr_audio(src, td_path)
            chunks_dir = td_path / "chunks"
            chunks_dir.mkdir(parents=True, exist_ok=True)
            chunks = split_audio_chunks(audio, chunks_dir, chunk_sec)

            cues = transcribe_chunks(
                args.engine,
                chunks,
                workers,
                model_name=args.whisper_model,
                language=lang,
                device=dev,
                paraformer_model=args.paraformer_model,
                vad=(args.paraformer_vad or "").strip() or None,
                punc=(args.paraformer_punc or "").strip() or None,
                lite=bool(args.paraformer_lite),
            )
            if not cues:
                raise RuntimeError("ASR returned no cues")
            write_webvtt(out, cues)
    except Exception as e:
        print(f"error: {e}", file=sys.stderr)
        return 1

    print(f"ok: wrote {out}")
    return 0


if __name__ == "__main__":
    sys.exit(main())
