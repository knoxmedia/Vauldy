#!/usr/bin/env bash
# Usage: ./whisper_subtitle.sh /path/to/video.mp4 /path/to/out.vtt
set -euo pipefail
DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
exec python "$DIR/asr_to_vtt.py" --engine whisper --input "$1" --output-vtt "$2" "${@:3}"
