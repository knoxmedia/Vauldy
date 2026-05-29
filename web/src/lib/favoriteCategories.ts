import type { FavoriteFolder, FavoriteFolderItem, FavoriteFolderPreview, MediaItem } from "../api/client";
import type { TranslateFn } from "../i18n";

export const FAVORITE_MEDIA_CATEGORIES = ["movie", "tv", "video", "anime", "music", "photo", "document"] as const;
export type FavoriteMediaCategory = (typeof FAVORITE_MEDIA_CATEGORIES)[number];
export type FavoriteCategoryKey = "folders" | FavoriteMediaCategory;

export const FAVORITE_CATEGORY_ORDER: FavoriteCategoryKey[] = [
  "folders",
  "movie",
  "tv",
  "video",
  "anime",
  "music",
  "photo",
  "document",
];

export function isFavoriteCategoryKey(v: string): v is FavoriteCategoryKey {
  return (FAVORITE_CATEGORY_ORDER as string[]).includes(v);
}

export function buildFavoriteCategoryLabels(t: TranslateFn): Record<FavoriteCategoryKey, string> {
  return {
    folders: t("pages.favorites.type_folders"),
    movie: t("pages.favorites.type_movie"),
    tv: t("pages.favorites.type_tv"),
    video: t("pages.favorites.type_video"),
    anime: t("pages.favorites.type_anime"),
    music: t("pages.favorites.type_music"),
    photo: t("pages.favorites.type_photo"),
    document: t("pages.favorites.type_document"),
  };
}

export function resolveLibraryType(item: MediaItem, libTypeById: Map<number, string>): string {
  const fromItem = (item.library_type || "").trim().toLowerCase();
  if (fromItem) return fromItem;
  return (libTypeById.get(item.library_id) || "").trim().toLowerCase();
}

export function getFavoriteMediaCategory(
  item: MediaItem,
  libTypeById: Map<number, string>,
): FavoriteMediaCategory | null {
  const libType = resolveLibraryType(item, libTypeById);
  if (FAVORITE_MEDIA_CATEGORIES.includes(libType as FavoriteMediaCategory)) {
    return libType as FavoriteMediaCategory;
  }
  const ft = (item.file_type || "").toLowerCase();
  if (ft.includes("audio")) return "music";
  if (ft.includes("image")) return "photo";
  if (ft === "document") return "document";
  if (ft.includes("video")) return "video";
  return null;
}

export function countFavoritesByCategory(
  rows: MediaItem[],
  libTypeById: Map<number, string>,
  folderCount: number,
): Record<FavoriteCategoryKey, number> {
  const counts: Record<FavoriteCategoryKey, number> = {
    folders: folderCount,
    movie: 0,
    tv: 0,
    video: 0,
    anime: 0,
    music: 0,
    photo: 0,
    document: 0,
  };
  for (const row of rows) {
    const cat = getFavoriteMediaCategory(row, libTypeById);
    if (cat) counts[cat] += 1;
  }
  return counts;
}

export function pickDefaultFavoriteCategory(counts: Record<FavoriteCategoryKey, number>): FavoriteCategoryKey {
  let best: FavoriteMediaCategory = "movie";
  let max = 0;
  for (const key of FAVORITE_MEDIA_CATEGORIES) {
    if (counts[key] > max) {
      max = counts[key];
      best = key;
    }
  }
  if (max > 0) return best;
  if (counts.folders > 0) return "folders";
  return "movie";
}

export function filterFavoritesByCategory(
  rows: MediaItem[],
  category: FavoriteMediaCategory,
  libTypeById: Map<number, string>,
): MediaItem[] {
  return rows.filter((row) => getFavoriteMediaCategory(row, libTypeById) === category);
}

export function favoriteFolderItemToMediaItem(item: FavoriteFolderItem): MediaItem {
  return {
    id: item.media_id,
    library_id: 0,
    file_id: "",
    title: item.title,
    file_path: "",
    file_type: item.file_type,
    duration: item.duration,
    width: item.width,
    height: item.height,
    format: "",
    status: "active",
    poster_url: item.poster_url,
    created_at: item.added_at,
  };
}

export function favoriteFolderCoverSrc(folder: FavoriteFolder): string {
  if (folder.cover_url) return folder.cover_url;
  const firstPreview = folder.preview_items?.[0];
  if (firstPreview?.poster_url) return firstPreview.poster_url;
  return "";
}

export const FOLDER_PREVIEW_SLOT_COUNT = 6;

export function buildFolderPreviewSlots(folder: FavoriteFolder): (FavoriteFolderPreview | null)[] {
  const previews = folder.preview_items ?? [];
  return Array.from({ length: FOLDER_PREVIEW_SLOT_COUNT }, (_, i) => previews[i] ?? null);
}

export const FAVORITE_VIDEO_CATEGORIES: FavoriteMediaCategory[] = ["movie", "tv", "video", "anime"];

export function isFavoriteVideoItem(item: MediaItem, libTypeById: Map<number, string>): boolean {
  const cat = getFavoriteMediaCategory(item, libTypeById);
  return cat != null && FAVORITE_VIDEO_CATEGORIES.includes(cat);
}
