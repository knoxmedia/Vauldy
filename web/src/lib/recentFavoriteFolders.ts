const STORAGE_KEY = "knox.favorite-folder.recent.v1";
export const MAX_RECENT_FAVORITE_FOLDERS = 5;

export type RecentFavoriteFolderEntry = { id: number; name: string };

import { serverDateTimeToMillis } from "./datetime";

type FolderLike = { id: number; name: string; updated_at?: string; created_at?: string };

export function readRecentFavoriteFolders(): RecentFavoriteFolderEntry[] {
  if (typeof window === "undefined") return [];
  try {
    const raw = window.localStorage.getItem(STORAGE_KEY);
    if (!raw) return [];
    const parsed = JSON.parse(raw) as unknown;
    if (!Array.isArray(parsed)) return [];
    return parsed
      .filter((x): x is RecentFavoriteFolderEntry => {
        if (x == null || typeof x !== "object") return false;
        const o = x as Record<string, unknown>;
        return typeof o.id === "number" && typeof o.name === "string";
      })
      .slice(0, MAX_RECENT_FAVORITE_FOLDERS);
  } catch {
    return [];
  }
}

function folderSortTime(folder: FolderLike): number {
  const t = folder.updated_at || folder.created_at;
  return t ? serverDateTimeToMillis(t) : 0;
}

/** Merge local recents with server folders (by updated_at) for menu shortcuts. */
export function mergeRecentFavoriteFolders(folders: FolderLike[]): RecentFavoriteFolderEntry[] {
  const byId = new Map(folders.map((f) => [f.id, f]));
  const merged: RecentFavoriteFolderEntry[] = [];

  for (const entry of readRecentFavoriteFolders()) {
    const live = byId.get(entry.id);
    if (live) merged.push({ id: live.id, name: live.name });
  }

  const usedIds = new Set(merged.map((f) => f.id));
  for (const folder of [...folders].sort((a, b) => folderSortTime(b) - folderSortTime(a))) {
    if (merged.length >= MAX_RECENT_FAVORITE_FOLDERS) break;
    if (!usedIds.has(folder.id)) merged.push({ id: folder.id, name: folder.name });
  }

  return merged.slice(0, MAX_RECENT_FAVORITE_FOLDERS);
}

/** Move folder to front of recents (max five), for submenu shortcuts. */
export function rememberFavoriteFolderAdded(entry: RecentFavoriteFolderEntry): void {
  if (typeof window === "undefined") return;
  const without = readRecentFavoriteFolders().filter((f) => f.id !== entry.id);
  const name = entry.name.trim() || "收藏夹";
  const next = [{ id: entry.id, name }, ...without].slice(0, MAX_RECENT_FAVORITE_FOLDERS);
  window.localStorage.setItem(STORAGE_KEY, JSON.stringify(next));
}
