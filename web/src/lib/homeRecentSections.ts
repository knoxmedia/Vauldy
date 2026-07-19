import { fetchMedia, type Library, type MediaItem } from "../api/client";
import type { TranslateFn } from "../i18n";

export const HOME_RECENT_LIMIT = 24;
export const CONTINUE_WATCHING_LIBRARY_TYPES = ["movie", "tv", "video"] as const;

export type HomeRecentSection = { key: string; title: string; libTypes: string[]; landscape: boolean };
export type HomeMediaFetcher = typeof fetchMedia;

export function buildHomeRecentSections(t: TranslateFn): HomeRecentSection[] {
  return [
    { key: "movie", title: t("pages.home.section_movie"), libTypes: ["movie"], landscape: false },
    { key: "tv", title: t("pages.home.section_tv"), libTypes: ["tv"], landscape: true },
    { key: "anime", title: t("pages.home.section_anime"), libTypes: ["anime"], landscape: true },
    { key: "music", title: t("pages.home.section_music"), libTypes: ["music"], landscape: false },
    { key: "photo", title: t("pages.home.section_photo"), libTypes: ["photo"], landscape: false },
    { key: "document", title: t("pages.home.section_document"), libTypes: ["document"], landscape: false },
    { key: "other_video", title: t("pages.home.section_other_video"), libTypes: ["video"], landscape: true },
  ];
}

function recentSortKey(m: MediaItem, sectionKey: string): string {
  return sectionKey === "photo" ? (m.photo_taken_at || m.created_at || "").trim() : (m.created_at || "").trim();
}

export async function loadHomeRecentBySection(
  libs: Library[],
  sections: HomeRecentSection[],
  signal: AbortSignal,
  onSection: (key: string, items: MediaItem[]) => void,
  fetcher: HomeMediaFetcher = fetchMedia,
): Promise<void> {
  await Promise.all(
    sections.map(async (sec) => {
      try {
        const libIds = libs.filter((l) => sec.libTypes.includes((l.type || "").trim())).map((l) => l.id);
        const sort = sec.key === "photo" ? ("taken_desc" as const) : ("created_desc" as const);
        const batches = await Promise.all(
          libIds.map((id) => fetcher(id, { sort, limit: HOME_RECENT_LIMIT }, signal)),
        );
        if (signal.aborted) return;
        const merged = batches.flat();
        merged.sort((a, b) => recentSortKey(b, sec.key).localeCompare(recentSortKey(a, sec.key)) || b.id - a.id);
        const seen = new Set<number>();
        const items: MediaItem[] = [];
        for (const item of merged) {
          if (seen.has(item.id)) continue;
          seen.add(item.id);
          items.push(item);
          if (items.length === HOME_RECENT_LIMIT) break;
        }
        if (!signal.aborted) onSection(sec.key, items);
      } catch {
        // A failed section deliberately leaves its previous shelf untouched.
      }
    }),
  );
}

type HomeRecentComparableField = keyof MediaItem;

const HOME_RECENT_PROJECTION_FIELDS: readonly HomeRecentComparableField[] = [
  "id", "library_id", "library_type", "file_id", "title", "original_title", "file_path", "file_type",
  "duration", "width", "height", "bitrate", "format", "status", "created_at", "last_play_at", "completed",
  "release_date", "year", "poster_url", "backdrop_url", "photo_taken_at", "photo_tags", "photo_tag_ids", "scraped",
  "encrypted_asset", "optimization_asset_recorded", "optimization_available", "optimization_source_available",
  "music_album_id", "music_album_title", "music_artist",
];

function canonicalValue(value: unknown): unknown {
  if (Array.isArray(value)) return value.map(canonicalValue);
  if (value && typeof value === "object") {
    return Object.fromEntries(
      Object.entries(value as Record<string, unknown>)
        .sort(([a], [b]) => a.localeCompare(b))
        .map(([key, nested]) => [key, canonicalValue(nested)]),
    );
  }
  return value === undefined ? null : value;
}

export function homeRecentProjectionSignature(item: MediaItem): string {
  return JSON.stringify(HOME_RECENT_PROJECTION_FIELDS.map((field) => canonicalValue(item[field])));
}
function sameDisplayedItems(a: MediaItem[] | undefined, b: MediaItem[]): boolean {
  if (!a || a.length !== b.length) return false;
  return a.every((oldItem, index) => homeRecentProjectionSignature(oldItem) === homeRecentProjectionSignature(b[index]!));
}
export function stableHomeRecentMap(
  previous: Map<string, MediaItem[]>,
  key: string,
  items: MediaItem[],
): Map<string, MediaItem[]> {
  if (sameDisplayedItems(previous.get(key), items)) return previous;
  const next = new Map(previous);
  next.set(key, items);
  return next;
}

export function flattenHomeRecent(map: Map<string, MediaItem[]>): MediaItem[] {
  const seen = new Set<number>();
  const items: MediaItem[] = [];
  for (const arr of map.values()) for (const item of arr) if (!seen.has(item.id)) { seen.add(item.id); items.push(item); }
  return items;
}
