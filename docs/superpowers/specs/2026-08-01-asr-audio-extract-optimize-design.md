# ASR audio extraction + segmented recognition

Date: 2026-08-01  
Status: approved (user directive)

## Goals

1. Extract ASR audio with `-map 0:a:0 -vn -sn` only (no subtitle/video demux).
2. Prefer compact **16 kHz mono MP3** (libmp3lame) instead of PCM WAV.
3. Go always prepares ASR audio for **video** inputs; shell/Python must not re-extract from video.
4. Encrypted video: pass extracted audio to ASR; never `MaterializePlaintextTemp` the full movie for shell ASR.
5. Long media: segment into ~12 minute chunks; transcribe with limited parallelism on CUDA; merge cues with time offsets.
6. Keep skip-ASR-when-ready-subtitles behavior unchanged.
7. Keep default engine/model/device (faster-whisper / base / cuda) unchanged.

## Design

### Go (`video_io.go`, `service.go`, `asr_transcribe.go`)

- `extractASRAudio` → write `.asr-input.mp3` via:
  `-map 0:a:0 -vn -sn -ac 1 -ar 16000 -c:a libmp3lame -q:a 4`
  Fallback if lame missing: `-c:a flac` then `-c:a pcm_s16le` wav.
- `asrInputPath`: for video (or encrypted pipe), always extract compact audio; for existing audio sidecars (lyric `.mp3`/`.flac`/…), return path as-is.
- Shell ASR paths: use extracted audio path; remove `MaterializePlaintextTemp` for ASR.

### Python (`asr_to_vtt.py`)

- If input suffix is audio (mp3/opus/flac/wav/m4a/aac/ogg): use directly (optional light normalize only if not 16k mono and needed).
- If input is video: extract once with same map flags to mp3 (compat for CLI use outside Knox).
- Duration > 12 min: split with ffmpeg into 12-min chunks; run faster-whisper/whisper per chunk; offset timestamps; merge VTT.
- CUDA workers default 1 (safe); allow `--asr-workers` up to 2 for parallel chunk jobs.

## Non-goals

- Changing when ASR is scheduled (still only if no ready subtitle).
- Changing default model size.
