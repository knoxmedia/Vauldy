import type { NavigateFunction } from "react-router-dom";
import type { AlbumDetail, MediaItem, MusicTrackRow } from "../api/client";
import { ALBUM_PLAY_SESSION_KEY } from "../api/client";

export type AlbumPlaySession = {
  albumId: number;
  order: number[];
};

export type MusicQueueItem = {
  mediaId: number;
  title: string;
  artist: string;
  albumTitle: string;
  albumId: number;
  duration?: number;
};

export function readAlbumPlaySession(): AlbumPlaySession | null {
  try {
    const raw = sessionStorage.getItem(ALBUM_PLAY_SESSION_KEY);
    if (!raw) return null;
    const sess = JSON.parse(raw) as AlbumPlaySession;
    if (!sess.albumId || !Array.isArray(sess.order) || sess.order.length === 0) return null;
    return sess;
  } catch {
    return null;
  }
}

export function storeAlbumPlaySession(albumId: number, order: number[]): void {
  sessionStorage.setItem(
    ALBUM_PLAY_SESSION_KEY,
    JSON.stringify({ albumId, order: order.filter((id) => id > 0) }),
  );
}

export function clearAlbumPlaySession(): void {
  sessionStorage.removeItem(ALBUM_PLAY_SESSION_KEY);
}

export function albumTracksToQueue(album: AlbumDetail): MusicQueueItem[] {
  const albumArtist = album.album_artist || "Various Artists";
  return (album.tracks ?? [])
    .filter((t: MusicTrackRow) => Number(t.media_id) > 0)
    .map((t: MusicTrackRow) => ({
      mediaId: Number(t.media_id),
      title: t.title,
      artist: t.artist || albumArtist,
      albumTitle: album.title,
      albumId: album.id,
      duration: t.duration,
    }));
}

/** Build a playback queue from library track list rows (曲目 tab). */
export function libraryTracksToQueue(tracks: MusicTrackRow[]): MusicQueueItem[] {
  return tracks
    .filter((t) => Number(t.media_id) > 0)
    .map((t) => ({
      mediaId: Number(t.media_id),
      title: t.title,
      artist: t.artist || t.album_artist || "Various Artists",
      albumTitle: t.album_title || "",
      albumId: Number(t.album_id) || 0,
      duration: t.duration,
    }));
}

/** Build a playback queue from home / media list rows (audio). */
export function mediaItemsToMusicQueue(items: MediaItem[]): MusicQueueItem[] {
  return items
    .filter((m) => m.file_type === "audio" && m.id > 0)
    .map((m) => ({
      mediaId: m.id,
      title: m.title,
      artist: (m.music_artist || "").trim() || "Various Artists",
      albumTitle: (m.music_album_title || "").trim(),
      albumId: Number(m.music_album_id) || 0,
      duration: m.duration,
    }));
}

export function storeQueueSession(queue: MusicQueueItem[]): void {
  if (queue.length === 0) return;
  storeAlbumPlaySession(queue[0]!.albumId, queue.map((q) => q.mediaId));
}

export function resolveNextAlbumMedia(
  session: AlbumPlaySession,
  currentMediaId: number,
  currentIndex: number | null,
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

export function resolvePrevAlbumMedia(
  session: AlbumPlaySession,
  currentMediaId: number,
  currentIndex: number | null,
): { mediaId: number; index: number } | null {
  const { order } = session;
  let pos = currentIndex;
  if (pos == null || pos < 0 || pos >= order.length || order[pos] !== currentMediaId) {
    pos = order.indexOf(currentMediaId);
  }
  if (pos <= 0) return null;
  const prevIndex = pos - 1;
  return { mediaId: order[prevIndex]!, index: prevIndex };
}

export function isAlbumPlaybackFinished(
  searchParams: URLSearchParams,
  currentMediaId: number,
): boolean {
  const session = readAlbumPlaySession();
  if (!session) return false;
  const aidParam = searchParams.get("album_id");
  if (!aidParam || Number(aidParam) !== Number(session.albumId)) return false;
  const idxParam = searchParams.get("index");
  const parsedIndex = idxParam != null && idxParam !== "" ? Number(idxParam) : NaN;
  const currentIndex = Number.isFinite(parsedIndex) ? parsedIndex : null;
  if (resolveNextAlbumMedia(session, currentMediaId, currentIndex) !== null) return false;
  const pos =
    currentIndex != null &&
    currentIndex >= 0 &&
    currentIndex < session.order.length &&
    session.order[currentIndex] === currentMediaId
      ? currentIndex
      : session.order.indexOf(currentMediaId);
  return pos >= 0 && pos === session.order.length - 1;
}

export function navigateAlbumNext(
  nav: NavigateFunction,
  searchParams: URLSearchParams,
  currentMediaId: number,
): boolean {
  const session = readAlbumPlaySession();
  if (!session) return false;
  const aidParam = searchParams.get("album_id");
  if (!aidParam || Number(aidParam) !== Number(session.albumId)) return false;
  const idxParam = searchParams.get("index");
  const parsedIndex = idxParam != null && idxParam !== "" ? Number(idxParam) : NaN;
  const currentIndex = Number.isFinite(parsedIndex) ? parsedIndex : null;
  const next = resolveNextAlbumMedia(session, currentMediaId, currentIndex);
  if (!next) return false;
  nav(`/player/${next.mediaId}?album_id=${session.albumId}&index=${next.index}`, { replace: true });
  return true;
}

export function buildAlbumPageUrl(albumId: number): string {
  return `/album/${albumId}`;
}
