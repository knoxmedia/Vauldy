import type { NavigateFunction } from "react-router-dom";
import { SERIES_PLAY_SESSION_KEY } from "../api/client";

export type SeriesPlaySession = {
  seriesId: number;
  order: number[];
};

export function readSeriesPlaySession(): SeriesPlaySession | null {
  try {
    const raw = sessionStorage.getItem(SERIES_PLAY_SESSION_KEY);
    if (!raw) return null;
    const sess = JSON.parse(raw) as SeriesPlaySession;
    if (!sess.seriesId || !Array.isArray(sess.order) || sess.order.length === 0) return null;
    return sess;
  } catch {
    return null;
  }
}

export function storeSeriesPlaySession(seriesId: number, order: number[]): void {
  sessionStorage.setItem(
    SERIES_PLAY_SESSION_KEY,
    JSON.stringify({ seriesId, order: order.filter((id) => id > 0) })
  );
}

export function resolveNextSeriesMedia(
  session: SeriesPlaySession,
  currentMediaId: number,
  currentIndex: number | null
): { mediaId: number; index: number } | null {
  const { order } = session;
  let pos = currentIndex;
  if (pos == null || pos < 0 || pos >= order.length || order[pos] !== currentMediaId) {
    pos = order.indexOf(currentMediaId);
  }
  if (pos < 0 || pos + 1 >= order.length) return null;
  const nextIndex = pos + 1;
  return { mediaId: order[nextIndex]!, index: nextIndex };
}

export function isSeriesPlaybackFinished(
  searchParams: URLSearchParams,
  currentMediaId: number
): boolean {
  const session = readSeriesPlaySession();
  if (!session) return false;
  const sidParam = searchParams.get("series_id");
  if (!sidParam || Number(sidParam) !== Number(session.seriesId)) return false;
  const idxParam = searchParams.get("index");
  const parsedIndex = idxParam != null && idxParam !== "" ? Number(idxParam) : NaN;
  const currentIndex = Number.isFinite(parsedIndex) ? parsedIndex : null;
  if (resolveNextSeriesMedia(session, currentMediaId, currentIndex) !== null) return false;
  const pos =
    currentIndex != null &&
    currentIndex >= 0 &&
    currentIndex < session.order.length &&
    session.order[currentIndex] === currentMediaId
      ? currentIndex
      : session.order.indexOf(currentMediaId);
  return pos >= 0 && pos === session.order.length - 1;
}

export function clearSeriesPlaySession(): void {
  sessionStorage.removeItem(SERIES_PLAY_SESSION_KEY);
}

/** SPA navigation to the next series episode (no full page reload). */
export function navigateSeriesNext(
  nav: NavigateFunction,
  searchParams: URLSearchParams,
  currentMediaId: number
): boolean {
  const session = readSeriesPlaySession();
  if (!session) return false;
  const sidParam = searchParams.get("series_id");
  if (!sidParam || Number(sidParam) !== Number(session.seriesId)) return false;
  const idxParam = searchParams.get("index");
  const parsedIndex = idxParam != null && idxParam !== "" ? Number(idxParam) : NaN;
  const currentIndex = Number.isFinite(parsedIndex) ? parsedIndex : null;
  const next = resolveNextSeriesMedia(session, currentMediaId, currentIndex);
  if (!next) return false;
  nav(`/player/${next.mediaId}?series_id=${session.seriesId}&index=${next.index}`, { replace: true });
  return true;
}

export function buildSeriesPageUrl(seriesId: number, currentMediaId?: number): string {
  if (currentMediaId == null || currentMediaId <= 0) {
    return `/series/${seriesId}`;
  }
  const params = new URLSearchParams();
  params.set("current_media_id", String(currentMediaId));
  return `/series/${seriesId}?${params.toString()}`;
}
