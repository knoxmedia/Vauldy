export type LrcLine = {
  timeSec: number;
  text: string;
};

const TIME_RE = /\[(\d{1,2}):(\d{2})(?:\.(\d{1,3}))?\]/g;

/** Parse LRC text into timed lines (sorted by time). */
export function parseLrc(raw: string): LrcLine[] {
  if (!raw.trim()) return [];
  const lines: LrcLine[] = [];
  for (const line of raw.split(/\r?\n/)) {
    const trimmed = line.trim();
    if (!trimmed) continue;
    let match: RegExpExecArray | null;
    TIME_RE.lastIndex = 0;
    const times: number[] = [];
    while ((match = TIME_RE.exec(trimmed)) !== null) {
      times.push(lrcTimestampToSec(match[1]!, match[2]!, match[3]));
    }
    if (times.length === 0) continue;
    const text = trimmed.replace(/\[\d{1,2}:\d{2}(?:\.\d{1,3})?\]/g, "").trim();
    if (!text) continue;
    for (const timeSec of times) {
      lines.push({ timeSec, text });
    }
  }
  lines.sort((a, b) => a.timeSec - b.timeSec || a.text.localeCompare(b.text));
  return lines;
}

function lrcTimestampToSec(min: string, sec: string, frac?: string): number {
  const m = Number(min);
  const s = Number(sec);
  let ms = 0;
  if (frac) {
    const padded = frac.padEnd(3, "0").slice(0, 3);
    ms = Number(padded) / 1000;
  }
  return m * 60 + s + ms;
}

/** Index of the active lyric line for the current playback position. */
export function activeLrcIndex(lines: LrcLine[], positionSec: number): number {
  if (lines.length === 0) return -1;
  let active = -1;
  for (let i = 0; i < lines.length; i++) {
    if (lines[i]!.timeSec <= positionSec + 0.05) active = i;
    else break;
  }
  return active;
}
