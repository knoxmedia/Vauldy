#!/usr/bin/env bash
# Usage: ./paraformer_subtitle.sh /path/to/video.mp4 /path/to/out.vtt
# Add --paraformer-lite for smaller zh-only model.
set -euo pipefail
DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
exec python "$DIR/asr_to_vtt.py" --engine paraformer --input "$1" --output-vtt "$2" "${@:3}"
