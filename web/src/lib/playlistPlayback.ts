import type { NavigateFunction } from "react-router-dom";
import { PLAYLIST_PLAY_SESSION_KEY } from "../api/client";

export type PlaylistPlayMode = "ordered" | "shuffle";

/** Written by Playlists when starting playback; order is API order or shuffled display order. */
export type PlaylistPlaySession = {
  playlistId: number;
  order: number[];
  mode?: PlaylistPlayMode;
};

export function readPlaylistPlaySession(): PlaylistPlaySession | null {
  try {
    const raw = sessionStorage.getItem(PLAYLIST_PLAY_SESSION_KEY);
    if (!raw) return null;
    const sess = JSON.parse(raw) as PlaylistPlaySession;
    if (!sess.playlistId || !Array.isArray(sess.order) || sess.order.length === 0) return null;
    return sess;
  } catch {
    return null;
  }
}

export function resolveNextPlaylistMedia(
  session: PlaylistPlaySession,
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

/** True when the current item is the last in an active playlist play session. */
export function isPlaylistPlaybackFinished(
  searchParams: URLSearchParams,
  currentMediaId: number
): boolean {
  const session = readPlaylistPlaySession();
  if (!session) return false;
  const pidParam = searchParams.get("playlist_id");
  if (!pidParam || Number(pidParam) !== Number(session.playlistId)) return false;
  const idxParam = searchParams.get("index");
  const parsedIndex = idxParam != null && idxParam !== "" ? Number(idxParam) : NaN;
  const currentIndex = Number.isFinite(parsedIndex) ? parsedIndex : null;
  if (resolveNextPlaylistMedia(session, currentMediaId, currentIndex) !== null) return false;
  const pos =
    currentIndex != null &&
    currentIndex >= 0 &&
    currentIndex < session.order.length &&
    session.order[currentIndex] === currentMediaId
      ? currentIndex
      : session.order.indexOf(currentMediaId);
  return pos >= 0 && pos === session.order.length - 1;
}

export function clearPlaylistPlaySession(): void {
  sessionStorage.removeItem(PLAYLIST_PLAY_SESSION_KEY);
}

/** SPA navigation to the next playlist item (no full page reload). */
export function navigatePlaylistNext(
  nav: NavigateFunction,
  searchParams: URLSearchParams,
  currentMediaId: number
): boolean {
  const session = readPlaylistPlaySession();
  if (!session) return false;
  const pidParam = searchParams.get("playlist_id");
  if (!pidParam || Number(pidParam) !== Number(session.playlistId)) return false;
  const idxParam = searchParams.get("index");
  const parsedIndex = idxParam != null && idxParam !== "" ? Number(idxParam) : NaN;
  const currentIndex = Number.isFinite(parsedIndex) ? parsedIndex : null;
  const next = resolveNextPlaylistMedia(session, currentMediaId, currentIndex);
  if (!next) return false;
  nav(`/player/${next.mediaId}?playlist_id=${session.playlistId}&index=${next.index}`, { replace: true });
  return true;
}

export function buildPlaylistPageUrl(playlistId: number, currentMediaId?: number): string {
  const params = new URLSearchParams();
  params.set("playlist_id", String(playlistId));
  if (currentMediaId != null && currentMediaId > 0) {
    params.set("current_media_id", String(currentMediaId));
  }
  return `/playlists?${params.toString()}`;
}
