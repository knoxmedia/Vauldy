/** Ignore spurious player complete/ended events fired before meaningful playback. */
export function isLikelyNaturalPlaybackEnd(
  started: boolean,
  positionSec: number,
  durationSec: number,
  mediaEnded = false
): boolean {
  if (!started) return false;
  if (!Number.isFinite(positionSec) || positionSec < 1) return false;
  if (Number.isFinite(durationSec) && durationSec > 0) {
    if (durationSec <= 3) return positionSec >= durationSec * 0.85;
    return positionSec >= durationSec - 3 || positionSec / durationSec >= 0.85;
  }
  // Duration not available (common for HLS): require the media element to report ended.
  return mediaEnded;
}
