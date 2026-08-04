import { fetchMedia, type Library, type MediaItem } from "../api/client";
import type { TranslateFn } from "../i18n";

export const HOME_RECENT_LIMIT = 24;

/** 首页「继续观看」仅展示影片、电视剧、其他视频库中的资源。 */
export const CONTINUE_WATCHING_LIBRARY_TYPES = ["movie", "tv", "video"] as const;

export type HomeRecentSection = {
  key: string;
  title: string;
  libTypes: string[];
  landscape: boolean;
};

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
  if (sectionKey === "photo") {
    return (m.photo_taken_at || m.created_at || "").trim();
  }
  return (m.created_at || "").trim();
}

/** Load up to HOME_RECENT_LIMIT items per section from matching libraries (fair per category). */
export async function loadHomeRecentBySection(
  libs: Library[],
  sections: HomeRecentSection[],
): Promise<Map<string, MediaItem[]>> {
  const out = new Map<string, MediaItem[]>();
  await Promise.all(
    sections.map(async (sec) => {
      const libIds = libs.filter((l) => sec.libTypes.includes((l.type || "").trim())).map((l) => l.id);
      if (libIds.length === 0) {
        out.set(sec.key, []);
        return;
      }
      const sort = sec.key === "photo" ? ("taken_desc" as const) : ("created_desc" as const);
      const batches = await Promise.all(
        libIds.map((lid) =>
          fetchMedia(lid, { sort, limit: HOME_RECENT_LIMIT }).catch(() => [] as MediaItem[]),
        ),
      );
      const merged = batches.flat();
      merged.sort((a, b) => recentSortKey(b, sec.key).localeCompare(recentSortKey(a, sec.key)));
      const seen = new Set<number>();
      const deduped: MediaItem[] = [];
      for (const m of merged) {
        if (seen.has(m.id)) continue;
        seen.add(m.id);
        deduped.push(m);
        if (deduped.length >= HOME_RECENT_LIMIT) break;
      }
      out.set(sec.key, deduped);
    }),
  );
  return out;
}

export function flattenHomeRecent(map: Map<string, MediaItem[]>): MediaItem[] {
  const seen = new Set<number>();
  const items: MediaItem[] = [];
  for (const arr of map.values()) {
    for (const m of arr) {
      if (seen.has(m.id)) continue;
      seen.add(m.id);
      items.push(m);
    }
  }
  return items;
}
