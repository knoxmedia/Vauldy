import type { MediaItem, PhotoCategory } from "../api/client";
import { localDayKey, parseServerDateTime } from "./datetime";

export type SortMode = "taken_desc" | "created_desc";
export type MainTab = "timeline" | "smart";
export type LayoutMode = "grid" | "masonry";

export type DrillDown = {
  section: "people" | "place" | "thing" | "person";
  categoryId: string;
  title: string;
};

/** Drill-down id for the full person shelf (all clustered faces). */
export const PERSON_ALL_ID = "__all__";

/** Drill-down id for the full place shelf (all GPS locations). */
export const PLACE_ALL_ID = "__all__";

export function isPersonAllDrill(drill: DrillDown | null | undefined): boolean {
  return drill?.section === "person" && drill.categoryId === PERSON_ALL_ID;
}

export function isPlaceAllDrill(drill: DrillDown | null | undefined): boolean {
  return drill?.section === "place" && drill.categoryId === PLACE_ALL_ID;
}

export function isShelfAllDrill(drill: DrillDown | null | undefined): boolean {
  return isPersonAllDrill(drill) || isPlaceAllDrill(drill);
}

export type MonthBucket = {
  key: string;
  label: string;
  year: number;
  month: number;
  days: { day: string; label: string; items: MediaItem[] }[];
};

export type TimelineMark = {
  key: string;
  label: string;
  kind: "year" | "month";
  year: number;
  month?: number;
};

const PEOPLE_IDS = new Set(["people", "selfie"]);
const PLACE_IDS = new Set<string>(); // reserved for GPS locations

const THING_IDS = new Set([
  "document",
  "screenshot",
  "architecture",
  "landscape",
  "night",
  "food",
  "animal",
  "warm",
  "cool",
  "mono",
  "saturated",
  "camera",
  "download",
]);

export function dayKey(v?: string): string {
  return localDayKey(v);
}

export function fmtDayLabel(key: string): string {
  if (key === "unknown") return "未知日期";
  const d = parseServerDateTime(`${key}T12:00:00Z`);
  if (!d || Number.isNaN(d.getTime())) return key;
  return d.toLocaleDateString("zh-CN", { year: "numeric", month: "long", day: "numeric" });
}

export function fmtMonthLabel(key: string): string {
  const [y, m] = key.split("-");
  if (!y || !m) return key;
  return `${y}年${m}月`;
}

export function photoSortTime(item: MediaItem, sort: SortMode): string {
  if (sort === "created_desc") return item.created_at || "";
  return item.photo_taken_at || item.created_at || "";
}

export function filterPhotos(items: MediaItem[], q: string): MediaItem[] {
  const needle = q.trim().toLowerCase();
  if (!needle) return items;
  return items.filter(
    (r) =>
      (r.title ?? "").toLowerCase().includes(needle) ||
      (r.photo_tags ?? []).some((t) => t.toLowerCase().includes(needle)),
  );
}

export function groupByMonth(items: MediaItem[], sort: SortMode): MonthBucket[] {
  const monthMap = new Map<string, Map<string, MediaItem[]>>();
  for (const item of items) {
    const dk = dayKey(photoSortTime(item, sort));
    const monthKey = dk === "unknown" ? "unknown" : dk.slice(0, 7);
    const days = monthMap.get(monthKey) ?? new Map<string, MediaItem[]>();
    const list = days.get(dk) ?? [];
    list.push(item);
    days.set(dk, list);
    monthMap.set(monthKey, days);
  }

  return [...monthMap.entries()]
    .sort((a, b) => b[0].localeCompare(a[0]))
    .map(([monthKey, daysMap]) => {
      const year = monthKey === "unknown" ? 0 : Number(monthKey.slice(0, 4));
      const month = monthKey === "unknown" ? 0 : Number(monthKey.slice(5, 7));
      const days = [...daysMap.entries()]
        .sort((a, b) => b[0].localeCompare(a[0]))
        .map(([day, dayItems]) => ({ day, label: fmtDayLabel(day), items: dayItems }));
      return {
        key: monthKey,
        label: monthKey === "unknown" ? "未知日期" : fmtMonthLabel(monthKey),
        year,
        month,
        days,
      };
    });
}

export function buildTimelineMarks(months: MonthBucket[]): TimelineMark[] {
  const marks: TimelineMark[] = [];
  let lastYear = -1;
  for (const m of months) {
    if (m.key === "unknown") continue;
    if (m.year !== lastYear) {
      marks.push({ key: `y-${m.year}`, label: String(m.year), kind: "year", year: m.year });
      lastYear = m.year;
    }
    marks.push({
      key: `m-${m.key}`,
      label: fmtMonthLabel(m.key),
      kind: "month",
      year: m.year,
      month: m.month,
    });
  }
  return marks;
}

export function smartSectionOf(cat: PhotoCategory): "people" | "place" | "thing" | null {
  if (cat.id === "all") return null;
  if (PLACE_IDS.has(cat.id) || cat.type === "place") return "place";
  if (PEOPLE_IDS.has(cat.id)) return "people";
  if (THING_IDS.has(cat.id) || cat.type === "scene" || cat.type === "color" || cat.type === "source") return "thing";
  if (cat.type === "custom") return "thing";
  return "thing";
}

export function categoriesForSection(categories: PhotoCategory[], section: DrillDown["section"]): PhotoCategory[] {
  return categories.filter((c) => c.id !== "all" && smartSectionOf(c) === section);
}

export function sampleCover(items: MediaItem[], categoryId: string): number | null {
  for (const item of items) {
    const ids = item.photo_tag_ids ?? [];
    if (categoryId.startsWith("custom:")) {
      const name = categoryId.slice("custom:".length);
      if ((item.photo_tags ?? []).includes(name)) return item.id;
      continue;
    }
    if (ids.includes(categoryId)) return item.id;
  }
  return items[0]?.id ?? null;
}
