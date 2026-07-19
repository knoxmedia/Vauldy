import type { MediaDetail, SeriesDetail } from "../api/client";

export type SeriesMatchMedia = Pick<MediaDetail, "id" | "title" | "year" | "file_path">;

export function buildSeriesMatchMedia(
  detail: SeriesDetail,
  representative?: MediaDetail | null,
  fallbackMediaID?: number,
): SeriesMatchMedia {
  return {
    id: representative?.id ?? fallbackMediaID ?? 0,
    title: detail.title,
    year: detail.year,
    file_path: "",
  };
}

export type SeriesOwnedMedia = { seriesId: number; media: MediaDetail | null };
export type SeriesOwnedMediaID = { seriesId: number; mediaId?: number };

export function resolveSeriesMatchMedia(
  currentSeriesId: number,
  detail: SeriesDetail | null,
  representative?: SeriesOwnedMedia | null,
  fallback?: SeriesOwnedMediaID | null,
): SeriesMatchMedia | null {
  if (!detail || detail.id !== currentSeriesId) return null;
  const currentRepresentative = representative?.seriesId === currentSeriesId ? representative.media : null;
  const currentFallbackID = fallback?.seriesId === currentSeriesId ? fallback.mediaId : undefined;
  if (!currentRepresentative && !currentFallbackID) return null;
  return buildSeriesMatchMedia(detail, currentRepresentative, currentFallbackID);
}
