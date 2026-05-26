import type { MusicTrackRow } from "../api/client";

export type TrackColumnId =
  | "title"
  | "album_artist"
  | "artist"
  | "album"
  | "rating"
  | "duration"
  | "plays"
  | "added_at"
  | "played_at"
  | "rated_at"
  | "popularity"
  | "bitrate";

export type TrackSortOrder = "asc" | "desc";

export type TrackColumnDef = {
  id: TrackColumnId;
  label: string;
  defaultVisible: boolean;
  sortable: boolean;
  /** Hidden from picker when album column is not applicable */
  requiresAlbum?: boolean;
};

export const TRACK_COLUMNS: TrackColumnDef[] = [
  { id: "title", label: "标题", defaultVisible: true, sortable: true },
  { id: "album_artist", label: "专辑艺人", defaultVisible: true, sortable: true },
  { id: "artist", label: "艺术家", defaultVisible: true, sortable: true },
  { id: "album", label: "专辑", defaultVisible: true, sortable: true, requiresAlbum: true },
  { id: "rating", label: "评分", defaultVisible: false, sortable: true },
  { id: "duration", label: "时长", defaultVisible: true, sortable: true },
  { id: "plays", label: "播放", defaultVisible: false, sortable: true },
  { id: "added_at", label: "日期已添加", defaultVisible: false, sortable: true },
  { id: "played_at", label: "播放日期", defaultVisible: false, sortable: true },
  { id: "rated_at", label: "评分日期", defaultVisible: false, sortable: true },
  { id: "popularity", label: "人气", defaultVisible: false, sortable: true },
  { id: "bitrate", label: "比特率", defaultVisible: false, sortable: true },
];

export const TRACK_COLUMN_STORAGE_KEY = "knox.music.trackColumns.v1";

export function availableColumns(showAlbumColumn: boolean): TrackColumnDef[] {
  return TRACK_COLUMNS.filter((c) => !c.requiresAlbum || showAlbumColumn);
}

export function readVisibleColumns(showAlbumColumn: boolean): Set<TrackColumnId> {
  const available = availableColumns(showAlbumColumn);
  const availableIds = new Set(available.map((c) => c.id));
  try {
    const raw = localStorage.getItem(TRACK_COLUMN_STORAGE_KEY);
    if (raw) {
      const parsed = JSON.parse(raw) as unknown;
      if (Array.isArray(parsed)) {
        const ids = parsed.filter((id): id is TrackColumnId => typeof id === "string" && availableIds.has(id as TrackColumnId));
        if (ids.length > 0) return new Set(ids);
      }
    }
  } catch {
    /* ignore */
  }
  return new Set(available.filter((c) => c.defaultVisible).map((c) => c.id));
}

export function storeVisibleColumns(ids: Iterable<TrackColumnId>): void {
  try {
    localStorage.setItem(TRACK_COLUMN_STORAGE_KEY, JSON.stringify([...ids]));
  } catch {
    /* ignore */
  }
}

function str(v?: string): string {
  return (v ?? "").trim();
}

function num(v?: number): number {
  return Number.isFinite(v) ? (v as number) : 0;
}

export function trackColumnValue(track: MusicTrackRow, id: TrackColumnId): string | number {
  switch (id) {
    case "title":
      return str(track.title);
    case "album_artist":
      return str(track.album_artist);
    case "artist":
      return str(track.artist);
    case "album":
      return str(track.album_title);
    case "rating":
      return 0;
    case "duration":
      return num(track.duration);
    case "plays":
      return 0;
    case "added_at":
      return str(track.created_at);
    case "played_at":
      return "";
    case "rated_at":
      return "";
    case "popularity":
      return 0;
    case "bitrate":
      return num(track.bitrate);
    default:
      return "";
  }
}

export function compareTracks(
  a: MusicTrackRow,
  b: MusicTrackRow,
  field: TrackColumnId,
  order: TrackSortOrder,
): number {
  const av = trackColumnValue(a, field);
  const bv = trackColumnValue(b, field);
  let cmp = 0;
  if (typeof av === "number" && typeof bv === "number") {
    cmp = av - bv;
  } else {
    cmp = String(av).localeCompare(String(bv), "zh");
  }
  if (cmp === 0) cmp = a.id - b.id;
  return order === "asc" ? cmp : -cmp;
}

export function fmtDuration(sec?: number): string {
  if (!sec || sec <= 0) return "—";
  const m = Math.floor(sec / 60);
  const s = sec % 60;
  return `${m}:${String(s).padStart(2, "0")}`;
}

export function fmtDate(raw?: string): string {
  const s = str(raw);
  if (!s) return "—";
  const d = new Date(s);
  if (Number.isNaN(d.getTime())) return s.slice(0, 10) || "—";
  return d.toLocaleDateString("zh-CN");
}

export function fmtBitrate(kbps?: number): string {
  if (!kbps || kbps <= 0) return "—";
  return `${Math.round(kbps / 1000)} kbps`;
}
