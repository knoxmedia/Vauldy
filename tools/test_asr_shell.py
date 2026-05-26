import sqlite3
import subprocess
from pathlib import Path

media_root = Path(__file__).resolve().parents[1]
fp = sqlite3.connect(media_root / "data/knox-media.db").execute(
    "SELECT file_path FROM media WHERE id=396"
).fetchone()[0]
out_dir = media_root / "data/subtitles/396"
out_vtt = out_dir / "asr-test3.vtt"
out_dir.mkdir(parents=True, exist_ok=True)
py = str((media_root / "tools/recognition/.venv/Scripts/python.exe").resolve())
script = str((media_root / "tools/asr/asr_to_vtt.py").resolve())

def run_shell(label: str, sh: str, cwd: Path) -> None:
    env = __import__("os").environ.copy()
    env["FFMPEG_PATH"] = str((media_root / "tools/ffmpeg/bin/ffmpeg.exe").resolve())
    env["FFPROBE_PATH"] = str((media_root / "tools/ffmpeg/bin/ffprobe.exe").resolve())
    p = subprocess.run(["cmd", "/C", sh], cwd=str(cwd), env=env, capture_output=True)
    err = (p.stderr or b"").decode("gbk", errors="replace")
    out = (p.stdout or b"").decode("gbk", errors="replace")
    print(f"[{label}] rc={p.returncode}")
    if out.strip():
        print("stdout:", out.strip()[:500])
    if err.strip():
        print("stderr:", err.strip()[:500])

body = (
    f'"{py}" "{script}" --engine whisper --input "{fp}" '
    f'--output-vtt "{out_vtt}" --whisper-model tiny --whisper-language zh'
)

run_shell("no cd, media cwd", body, media_root)
run_shell("cd /d out_dir", f'cd /d "{out_dir}" && {body}', media_root)
run_shell("cd /d out_dir backslash", f'cd /d "{str(out_dir)}" && {body}', media_root)
run_shell("only body relative tools", (
    f'"tools/recognition/.venv/Scripts/python.exe" "tools/asr/asr_to_vtt.py" '
    f'--engine whisper --input "{fp}" --output-vtt "{out_vtt}" --whisper-model tiny --whisper-language zh'
), media_root)
