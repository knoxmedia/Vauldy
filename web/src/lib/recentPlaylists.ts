const STORAGE_KEY = "knox.playlist.recent.v1";
const MAX_RECENT = 3;

export type RecentPlaylistEntry = { id: number; name: string };

export function readRecentPlaylists(): RecentPlaylistEntry[] {
  if (typeof window === "undefined") return [];
  try {
    const raw = window.localStorage.getItem(STORAGE_KEY);
    if (!raw) return [];
    const parsed = JSON.parse(raw) as unknown;
    if (!Array.isArray(parsed)) return [];
    return parsed
      .filter((x): x is RecentPlaylistEntry => {
        if (x == null || typeof x !== "object") return false;
        const o = x as Record<string, unknown>;
        return typeof o.id === "number" && typeof o.name === "string";
      })
      .slice(0, MAX_RECENT);
  } catch {
    return [];
  }
}

/** Move playlist to front of recents (max three), for submenu shortcuts. */
export function rememberPlaylistAdded(entry: RecentPlaylistEntry): void {
  if (typeof window === "undefined") return;
  const without = readRecentPlaylists().filter((p) => p.id !== entry.id);
  const name = entry.name.trim() || "播放列表";
  const next = [{ id: entry.id, name }, ...without].slice(0, MAX_RECENT);
  window.localStorage.setItem(STORAGE_KEY, JSON.stringify(next));
}
