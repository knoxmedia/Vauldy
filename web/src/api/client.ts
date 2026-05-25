import axios from "axios";
import { message } from "antd";
import type { PlayerPrefs } from "../lib/playerPrefs";
import { useAuthStore, type UserRole } from "../store/auth";

export const api = axios.create({
  baseURL: "",
  timeout: 120000,
});

api.interceptors.request.use((config) => {
  const t = useAuthStore.getState().token;
  if (t) {
    config.headers.Authorization = `Bearer ${t}`;
  }
  return config;
});

api.interceptors.response.use(
  (res) => res,
  (err: unknown) => {
    const ax = err as { response?: { status?: number }; config?: { url?: string } };
    const status = ax.response?.status;
    const isLoginCall = ax.config?.url?.includes("/user/login");
    if (status === 401 && !isLoginCall) {
      useAuthStore.getState().clearSession();
      const path = window.location.pathname;
      if (!path.startsWith("/login")) {
        window.location.assign(`/login?redirect=${encodeURIComponent(path + window.location.search)}`);
      }
    } else if (status === 403) {
      const data = (ax as { response?: { data?: { error?: string } } }).response?.data;
      const errMsg = (data && typeof data.error === "string" && data.error.trim()) || "";
      if (/library access denied|folder access denied/i.test(errMsg)) {
        message.error("无权限访问该媒体库或目录");
      } else if (/playback denied/i.test(errMsg)) {
        message.error("无播放权限");
      } else if (/download denied/i.test(errMsg)) {
        message.error("无下载权限");
      } else if (/parental/i.test(errMsg)) {
        message.error("家长控制限制：" + errMsg);
      } else {
        message.error("权限不足（需要管理员或更高权限）");
      }
    }
    return Promise.reject(err);
  }
);

export type Library = {
  id: number;
  name: string;
  type: string;
  path: string;
  folders?: string[];
  auto_scan: number;
  enabled?: number;
  realtime_monitor?: number;
  preview_extract?: number;
  drm_enabled?: number;
  encryption_mode?: "standard" | "powerdrm" | "drm";
  cleanup_local_source_after_package?: number;
  metadata_providers?: string[];
  image_providers?: string[];
  metadata_refresh_policy?: string;
  scraper: string;
  created_at: string;
  media_count?: number;
  scan_task_id?: number;
  scan_status?: string;
  scan_processed_count?: number;
  scan_total_count?: number;
  scan_added_count?: number;
  scan_started_at?: string;
  /** Composite preview from latest 4 video posters (/uploads/library_previews/{id}.jpg). */
  preview_url?: string;
};

export type DRMCapabilities = {
  widevine_enabled: boolean;
  powerdrm_enabled: boolean;
};

export type MediaItem = {
  id: number;
  library_id: number;
  file_id: string;
  title: string;
  original_title?: string;
  file_path: string;
  file_type: string;
  duration: number;
  width: number;
  height: number;
  bitrate?: number;
  format: string;
  status: string;
  created_at?: string;
  last_play_at?: string;
  release_date?: string;
  year?: number;
  /** From scrape or empty; UI may fall back to `/uploads/posters/{id}.jpg`. */
  poster_url?: string;
  /** True when meaningful scrape metadata exists. */
  scraped?: boolean;
};

/** Normalize poster string from DB (some SQLite/json paths may retain JSON quotes). */
export function normalizeListPosterUrl(raw: string): string {
  let s = (raw || "").trim();
  if (s.length >= 2 && s.startsWith('"') && s.endsWith('"')) {
    try {
      const parsed = JSON.parse(s) as unknown;
      if (typeof parsed === "string") s = parsed;
      else s = s.slice(1, -1);
    } catch {
      s = s.slice(1, -1);
    }
  }
  return s.trim();
}

/** Server-generated frame capture when scrape poster is missing or failed to load. */
export function localPosterSrc(id: number): string {
  return `/uploads/posters/${id}.jpg`;
}

/** True when meta_json has a scraped poster URL (may still 404 at runtime). */
export function hasScrapedPosterUrl(r: Pick<MediaItem, "poster_url">): boolean {
  return Boolean(normalizeListPosterUrl(r.poster_url || ""));
}

/** Poster/thumbnail URL for grids: scraped poster or server-generated frame capture. */
export function mediaPosterSrc(r: Pick<MediaItem, "id" | "poster_url">): string {
  const u = normalizeListPosterUrl(r.poster_url || "");
  if (u) return u;
  return localPosterSrc(r.id);
}

export type ManualMatchResponse = {
  ok?: boolean;
  scrape?: {
    title?: string;
    overview?: string;
    poster?: string;
    release_date?: string;
    source?: string;
    extra?: Record<string, unknown>;
  };
};

/** Fields to patch a browse/list row after manual match without reloading the page. */
export type MediaMatchListUpdate = {
  id: number;
  title: string;
  poster_url?: string;
  year?: number;
  release_date?: string;
  scraped: boolean;
};

function yearFromReleaseDate(releaseDate: string): number | undefined {
  const y = Number(releaseDate.trim().slice(0, 4));
  return y >= 1800 && y <= 2100 ? y : undefined;
}

export function mediaMatchListUpdate(
  mediaId: number,
  response: ManualMatchResponse,
  fallback?: Pick<ScrapeMatchCandidate, "title" | "poster" | "year" | "release_date">,
): MediaMatchListUpdate {
  const scrape = response.scrape ?? {};
  const extra = scrape.extra ?? {};
  const poster = normalizeListPosterUrl(
    String(scrape.poster ?? extra.poster ?? fallback?.poster ?? ""),
  );
  const title = (scrape.title || fallback?.title || "").trim();
  const releaseDate = String(
    scrape.release_date ?? extra.release_date ?? fallback?.release_date ?? "",
  ).trim();
  let year = fallback?.year;
  if (year == null || year <= 0) {
    year = yearFromReleaseDate(releaseDate);
  }
  return {
    id: mediaId,
    title,
    poster_url: poster || undefined,
    year: year && year > 0 ? year : undefined,
    release_date: releaseDate || undefined,
    scraped: true,
  };
}

export type HistoryItem = {
  file_id: string;
  position: number;
  update_at: string;
  media_id: number;
  title: string;
  file_path: string;
  duration: number;
  play_start_at?: string;
  play_end_at?: string;
  completed?: number;
  play_count?: number;
};

export async function fetchLibraries() {
  const { data } = await api.get<{ items?: Library[] }>("/api/v1/library");
  return data?.items ?? [];
}

export async function fetchLibrariesWithCapabilities() {
  const { data } = await api.get<{ items?: Library[]; drm_capabilities?: DRMCapabilities }>("/api/v1/library");
  return {
    items: data?.items ?? [],
    drmCapabilities: data?.drm_capabilities ?? { widevine_enabled: true, powerdrm_enabled: true },
  };
}

export async function createLibrary(payload: {
  name: string;
  type: string;
  path?: string;
  folders?: string[];
  auto_scan?: number;
  enabled?: number;
  realtime_monitor?: number;
  preview_extract?: number;
  drm_enabled?: number;
  encryption_mode?: "standard" | "powerdrm" | "drm";
  cleanup_local_source_after_package?: number;
  metadata_providers?: string[];
  image_providers?: string[];
  metadata_refresh_policy?: string;
  scraper?: string;
}) {
  const { data } = await api.post<{ id: number }>("/api/v1/library", payload);
  return data;
}

export async function updateLibrary(
  id: number,
  payload: {
    name: string;
    type: string;
    path?: string;
    folders?: string[];
    auto_scan?: number;
    enabled?: number;
    realtime_monitor?: number;
    preview_extract?: number;
    drm_enabled?: number;
    encryption_mode?: "standard" | "powerdrm" | "drm";
    cleanup_local_source_after_package?: number;
    metadata_providers?: string[];
    image_providers?: string[];
    metadata_refresh_policy?: string;
    scraper?: string;
  }
) {
  await api.put(`/api/v1/library/${id}`, payload);
}

export async function deleteLibrary(id: number) {
  await api.delete(`/api/v1/library/${id}`);
}

export async function scanLibrary(id: number) {
  const { data } = await api.post<{ task_id: number; status: string; running?: boolean }>(`/api/v1/library/${id}/scan`);
  return data;
}

export type ScanTask = {
  id: number;
  library_id: number;
  library_name: string;
  status: string;
  source: string;
  processed_count: number;
  total_count: number;
  added_count: number;
  error_message?: string;
  cancelled: number;
  started_at: string;
  finished_at?: string;
  created_at: string;
  updated_at: string;
};

export async function fetchScanTasks(limit = 100) {
  const { data } = await api.get<{ items: ScanTask[] }>("/api/v1/scan/task", { params: { limit } });
  return data.items ?? [];
}

export async function cancelScanTask(id: number) {
  await api.post(`/api/v1/scan/task/${id}/cancel`);
}

export async function fetchMedia(
  libraryId?: number,
  opts?: { sort?: "id_desc" | "created_desc"; limit?: number }
) {
  const params: Record<string, string | number> = {};
  if (libraryId !== undefined) params.library_id = libraryId;
  if (opts?.sort) params.sort = opts.sort;
  if (opts?.limit !== undefined) params.limit = opts.limit;
  const { data } = await api.get<{ items?: MediaItem[] }>("/api/v1/media", { params });
  return data?.items ?? [];
}

export type SeriesSummary = {
  id: number;
  library_id: number;
  title: string;
  title_norm?: string;
  year?: number;
  tmdb_id?: string;
  tvdb_id?: string;
  poster?: string;
  poster_url?: string;
  folder_paths?: string[];
  season_count?: number;
  episode_count?: number;
  created_at?: string;
  updated_at?: string;
};

export type SeasonSummary = {
  id: number;
  season_num: number;
  name: string;
  poster?: string;
  episode_count?: number;
};

export type EpisodeMediaVersion = {
  media_id: number;
  file_id?: string;
  title?: string;
  file_path?: string;
  duration?: number;
  width?: number;
  height?: number;
  bitrate?: number;
  format?: string;
  sort_order?: number;
  poster_url?: string;
};

export type EpisodeRow = {
  id: number;
  episode_num: number;
  title?: string;
  duration?: number;
  versions?: EpisodeMediaVersion[];
};

export type SeriesDetail = {
  id: number;
  library_id: number;
  title: string;
  title_norm?: string;
  year?: number;
  tmdb_id?: string;
  tvdb_id?: string;
  poster?: string;
  poster_url?: string;
  folder_paths?: string[];
  meta_json?: string;
  seasons?: SeasonSummary[];
  created_at?: string;
  updated_at?: string;
};

export function isTVLibraryType(type?: string): boolean {
  const t = (type || "").trim().toLowerCase();
  return t === "tv" || t === "anime" || t === "television" || t === "series";
}

export function seriesPosterSrc(s: Pick<SeriesSummary, "id" | "poster_url" | "poster">): string {
  const u = normalizeListPosterUrl(s.poster_url || s.poster || "");
  if (u) return u;
  return "";
}

export async function fetchLibrarySeries(libraryId: number) {
  const { data } = await api.get<{ items?: SeriesSummary[] }>(`/api/v1/library/${libraryId}/series`);
  return data?.items ?? [];
}

export async function fetchSeries(seriesId: number) {
  const { data } = await api.get<SeriesDetail>(`/api/v1/series/${seriesId}`);
  return data;
}

export type SeriesPlayTarget = {
  media_id: number;
  position: number;
};

export async function fetchSeriesPlayTarget(seriesId: number) {
  const { data } = await api.get<SeriesPlayTarget>(`/api/v1/series/${seriesId}/play-target`);
  return data;
}

export async function updateSeries(
  seriesId: number,
  payload: { title?: string; year?: number; poster?: string; overview?: string },
) {
  const { data } = await api.patch<{
    ok: boolean;
    id: number;
    title: string;
    year?: number;
    poster?: string;
    overview?: string;
  }>(`/api/v1/series/${seriesId}`, payload);
  return data;
}

export async function fetchSeasonEpisodes(seasonId: number) {
  const { data } = await api.get<{ items?: EpisodeRow[] }>(`/api/v1/season/${seasonId}/episodes`);
  return data?.items ?? [];
}

export type MediaDetail = MediaItem & {
  md5?: string;
  meta_json?: string;
};

export async function fetchMediaDetail(mediaId: number) {
  const { data } = await api.get<MediaDetail>(`/api/v1/media/${mediaId}`);
  return data;
}

export type MediaSubtitleRow = {
  id: number;
  source_kind: string;
  stream_index?: number;
  codec_name?: string;
  lang: string;
  lang_source?: string;
  label?: string;
  source_path?: string;
  vtt_path?: string;
  status: string;
  error_message?: string;
  updated_at?: string;
};

export async function fetchMediaSubtitles(mediaId: number) {
  const { data } = await api.get<{ items: MediaSubtitleRow[] }>(`/api/v1/media/${mediaId}/subtitles`);
  return data?.items ?? [];
}

export async function updateMediaAdmin(
  mediaId: number,
  payload: {
    title?: string;
    original_title?: string;
    status?: string;
    duration?: number;
    width?: number;
    height?: number;
    bitrate?: number;
    format?: string;
    meta_json?: string;
  }
) {
  await api.put(`/api/v1/media/${mediaId}`, payload);
}

export type MediaStats = {
  watch_users: number;
  avg_position_seconds: number;
  avg_progress_percent: number;
  latest_watch_at: string;
  media_duration_seconds: number;
};

export async function fetchMediaStats(mediaId: number) {
  const { data } = await api.get<MediaStats>(`/api/v1/media/${mediaId}/stats`);
  return data;
}

/** 继续观看：同一 media 只保留 update_at 最新的一条（与 API 去重一致，前端兜底）。 */
export function dedupeUserHistory(items: HistoryItem[]): HistoryItem[] {
  const out: HistoryItem[] = [];
  const seenMedia = new Set<number>();
  const seenFile = new Set<string>();
  for (const h of items) {
    if (h.media_id > 0) {
      if (seenMedia.has(h.media_id)) continue;
      seenMedia.add(h.media_id);
    } else if (h.file_id) {
      if (seenFile.has(h.file_id)) continue;
      seenFile.add(h.file_id);
    }
    out.push(h);
  }
  return out;
}

export async function fetchUserHistory(limit = 24) {
  const { data } = await api.get<{ items?: HistoryItem[] }>("/api/v1/user/history", {
    params: { limit },
  });
  return dedupeUserHistory(data?.items ?? []);
}

export async function fetchFavorites() {
  const { data } = await api.get<{ items?: MediaItem[] }>("/api/v1/favorites");
  return data?.items ?? [];
}

export async function fetchFavoriteStatus(mediaId: number) {
  const { data } = await api.get<{ favorited: boolean }>(`/api/v1/media/${mediaId}/favorite`);
  return data.favorited;
}

export async function addFavorite(mediaId: number) {
  await api.post(`/api/v1/media/${mediaId}/favorite`);
}

export async function removeFavorite(mediaId: number) {
  await api.delete(`/api/v1/media/${mediaId}/favorite`);
}

export async function markWatched(mediaId: number) {
  await api.put(`/api/v1/media/${mediaId}/watched`);
}

export async function markUnwatched(mediaId: number) {
  await api.delete(`/api/v1/media/${mediaId}/watched`);
}

export async function fetchMediaDeletionPlan(mediaId: number) {
  const { data } = await api.get<{ files: string[] }>(`/api/v1/media/${mediaId}/deletion-plan`);
  return data?.files ?? [];
}

export async function deleteMedia(mediaId: number) {
  await api.delete(`/api/v1/media/${mediaId}`);
}

export interface PlaylistItem {
  id: number;
  media_id: number;
  sort_order: number;
  title: string;
  file_type: string;
  duration: number;
  width: number;
  height: number;
  poster_url: string;
  added_at: string;
}

/** Set by Playlists when starting playback; Player reads on PowerPlayer `onComplete` / xgplayer `ended`. */
export const PLAYLIST_PLAY_SESSION_KEY = "knox_playlist_session";

/** Set by SeriesDetail when starting episode playback; Player auto-advances on episode end. */
export const SERIES_PLAY_SESSION_KEY = "knox_series_session";

export interface Playlist {
  id: number;
  name: string;
  description: string;
  poster_url: string;
  background_url: string;
  logo_url: string;
  square_art_url: string;
  item_count: number;
  first_media_id: number;
  created_at: string;
  updated_at: string;
  items?: PlaylistItem[];
}

export async function fetchPlaylists() {
  const { data } = await api.get<{ items: Playlist[] }>("/api/v1/playlists");
  return data?.items ?? [];
}

export async function fetchPlaylist(id: number) {
  const { data } = await api.get<Playlist>(`/api/v1/playlists/${id}`);
  return data;
}

export async function createPlaylist(
  name: string,
  description = "",
  posterUrl = "",
  backgroundUrl = "",
  logoUrl = "",
  squareArtUrl = ""
) {
  const { data } = await api.post<{ id: number }>("/api/v1/playlists", {
    name,
    description,
    poster_url: posterUrl,
    background_url: backgroundUrl,
    logo_url: logoUrl,
    square_art_url: squareArtUrl,
  });
  return data.id;
}

export async function updatePlaylist(
  id: number,
  name: string,
  description = "",
  posterUrl = "",
  backgroundUrl = "",
  logoUrl = "",
  squareArtUrl = ""
) {
  await api.put(`/api/v1/playlists/${id}`, {
    name,
    description,
    poster_url: posterUrl,
    background_url: backgroundUrl,
    logo_url: logoUrl,
    square_art_url: squareArtUrl,
  });
}

export async function deletePlaylist(id: number) {
  await api.delete(`/api/v1/playlists/${id}`);
}

export async function addPlaylistItem(playlistId: number, mediaId: number) {
  await api.post(`/api/v1/playlists/${playlistId}/items`, { media_id: mediaId });
}

export async function removePlaylistItem(playlistId: number, itemId: number) {
  await api.delete(`/api/v1/playlists/${playlistId}/items/${itemId}`);
}

export async function reorderPlaylistItems(playlistId: number, items: { id: number; sort_order: number }[]) {
  await api.put(`/api/v1/playlists/${playlistId}/reorder`, { items });
}

export async function uploadPlaylistImage(
  playlistId: number,
  field: "poster" | "background" | "logo" | "square_art",
  file: File
) {
  const formData = new FormData();
  formData.append("file", file);
  const { data } = await api.post<{ ok: boolean; url: string }>(
    `/api/v1/playlists/${playlistId}/images/${field}`,
    formData,
    { headers: { "Content-Type": "multipart/form-data" } }
  );
  return data.url;
}

export async function transcodeAsync(mediaId: number, mode = "auto") {
  await api.post("/api/v1/transcode/async", { media_id: mediaId, mode });
}

export async function login(username: string, password: string) {
  const { data } = await api.post<{ token: string }>("/api/v1/user/login", {
    username,
    password,
  });
  return data.token;
}

export type SessionUserInfo = {
  id: number;
  username: string;
  role: string;
  /** When omitted (legacy server), treated as allowed. */
  can_play?: boolean;
  can_download?: boolean;
  avatar_url?: string;
  ui_locale?: string;
  player_prefs?: Partial<PlayerPrefs>;
};

export async function fetchUserInfo() {
  const { data } = await api.get<SessionUserInfo>("/api/v1/user/info");
  return { ...data, role: data.role as UserRole };
}

export async function updateUserProfile(payload: { ui_locale?: string; player_prefs?: PlayerPrefs }) {
  const { data } = await api.put<{ ok: boolean; ui_locale: string; player_prefs: PlayerPrefs }>(
    "/api/v1/user/profile",
    payload
  );
  return data;
}

export async function changeUserPassword(newPassword: string, confirmPassword: string) {
  await api.put("/api/v1/user/password", {
    new_password: newPassword,
    confirm_password: confirmPassword,
  });
}

export async function uploadUserAvatar(file: Blob) {
  const formData = new FormData();
  formData.append("file", file, "avatar.png");
  const { data } = await api.post<{ ok: boolean; url: string }>("/api/v1/user/avatar", formData, {
    headers: { "Content-Type": "multipart/form-data" },
  });
  return data.url;
}

export async function deleteUserAvatar() {
  await api.delete("/api/v1/user/avatar");
}

export type AdminUser = {
  id: number;
  username: string;
  role: "admin" | "user";
  can_manage: number;
  can_play: number;
  can_download: number;
  can_access_features: number;
  library_scope: "all" | "selected";
  library_ids: number[];
  library_folders?: Record<string, string[]>;
  parental_enabled: number;
  parental_max_rating: string;
  allowed_time_start: string;
  allowed_time_end: string;
  parental_plans?: Array<{
    weekday: number;
    start_time: string;
    end_time: string;
  }>;
};

export async function fetchAdminUsers() {
  const { data } = await api.get<{ items: AdminUser[] }>("/api/v1/admin/users");
  return data?.items ?? [];
}

export async function createAdminUser(payload: {
  username: string;
  password: string;
  role: "admin" | "user";
  can_manage: number;
  can_play: number;
  can_download: number;
  can_access_features: number;
  library_scope: "all" | "selected";
  library_ids: number[];
  library_folders?: Record<string, string[]>;
  parental_enabled: number;
  parental_max_rating?: string;
  parental_pin?: string;
  allowed_time_start?: string;
  allowed_time_end?: string;
  parental_plans?: Array<{
    weekday: number;
    start_time: string;
    end_time: string;
  }>;
}) {
  const { data } = await api.post<{ id: number }>("/api/v1/admin/users", payload);
  return data.id;
}

export async function updateAdminUser(id: number, payload: {
  username: string;
  role: "admin" | "user";
  can_manage: number;
  can_play: number;
  can_download: number;
  can_access_features: number;
  library_scope: "all" | "selected";
  library_ids: number[];
  library_folders?: Record<string, string[]>;
  parental_enabled: number;
  parental_max_rating?: string;
  parental_pin?: string;
  allowed_time_start?: string;
  allowed_time_end?: string;
  parental_plans?: Array<{
    weekday: number;
    start_time: string;
    end_time: string;
  }>;
}) {
  await api.put(`/api/v1/admin/users/${id}`, payload);
}

export async function deleteAdminUser(id: number) {
  await api.delete(`/api/v1/admin/users/${id}`);
}

export async function resetAdminUserPassword(id: number, password: string) {
  await api.post(`/api/v1/admin/users/${id}/reset-password`, { password });
}

export type APIClientRow = {
  app_id: number;
  name: string;
  description: string;
  client_id: string;
  revoked: boolean;
  created_at: string;
};

export type CreateApiClientResult = {
  app_id: number;
  client_id: string;
  client_secret: string;
  name: string;
  description: string;
  hint?: string;
};

export async function listApiClients() {
  const { data } = await api.get<{ items: APIClientRow[] }>("/api/v1/admin/api-clients");
  return data?.items ?? [];
}

export async function createApiClient(payload: { name: string; description?: string }) {
  const { data } = await api.post<CreateApiClientResult>("/api/v1/admin/api-clients", payload);
  return data;
}

export async function revokeApiClient(appId: number) {
  await api.delete(`/api/v1/admin/api-clients/${appId}`);
}

export async function logout() {
  await api.post("/api/v1/user/logout");
}

/** Optional `session_id` is the JIT HLS session (e.g. `jit-…`) for correlating access logs after idle recovery. */
export type PlaybackLogPayload = {
  position?: number;
  completed?: number;
  session_id?: string;
};

export async function reportPlaybackStart(mediaId: number, payload?: PlaybackLogPayload) {
  await api.post(`/api/v1/media/${mediaId}/playback/start`, payload ?? {});
}

export async function reportPlaybackEnd(mediaId: number, payload?: PlaybackLogPayload) {
  await api.post(`/api/v1/media/${mediaId}/playback/end`, payload ?? {});
}

export async function savePlaybackProgress(
  mediaId: number,
  payload: { position: number; completed?: number; session_id?: string }
) {
  await api.post(`/api/v1/media/${mediaId}/progress`, payload);
}

export async function removePlayProgress(mediaId: number) {
  await api.delete(`/api/v1/media/${mediaId}/progress`);
}

export type PlaybackHistoryItem = {
  id: number;
  user_id: number;
  username: string;
  media_id: number;
  title: string;
  file_type: string;
  library_id: number;
  library_type: string;
  player: string;
  platform: string;
  played_at: string;
};

export type PlaybackHistoryRange = "7d" | "30d" | "90d" | "1y" | "all";

export async function fetchPlaybackHistory(params?: {
  limit?: number;
  media_id?: number;
  library_id?: number;
  user_id?: number;
  range?: PlaybackHistoryRange;
}) {
  const { data } = await api.get<{ items: PlaybackHistoryItem[]; total: number }>(
    "/api/v1/playback-history",
    {
      params: {
        limit: params?.limit ?? 200,
        media_id: params?.media_id,
        library_id: params?.library_id,
        user_id: params?.user_id,
        range: params?.range ?? "all",
      },
    },
  );
  return data.items ?? [];
}

export type AccessLogItem = {
  id: number;
  username: string;
  action: "login" | "logout" | "playback_start" | "playback_end" | string;
  media_id: number;
  message: string;
  created_at: string;
};

export type DRMLicenseAuditItem = {
  id: number;
  media_id: number;
  drm_type: string;
  result: string;
  reason: string;
  client_ip: string;
  created_at: string;
};

export async function fetchAccessLogs(params?: {
  limit?: number;
  action?: string;
  range?: "today" | "7d" | "30d" | "custom";
  from?: string;
  to?: string;
}) {
  const limit = params?.limit ?? 200;
  const action = params?.action ?? "all";
  const range = params?.range ?? "7d";
  const { data } = await api.get<{ items: AccessLogItem[] }>("/api/v1/admin/access-log", {
    params: { limit, action, range, from: params?.from, to: params?.to },
  });
  return data.items ?? [];
}

export async function fetchDRMLicenseAudits(params?: {
  limit?: number;
  media_id?: number;
  drm_type?: string;
  result?: string;
  reason?: string;
  range?: "all" | "today" | "7d" | "30d" | "custom";
  from?: string;
  to?: string;
}) {
  const { data } = await api.get<{ items: DRMLicenseAuditItem[] }>("/api/v1/admin/drm-license-audit", {
    params: {
      limit: params?.limit ?? 100,
      media_id: params?.media_id,
      drm_type: params?.drm_type ?? "all",
      result: params?.result ?? "all",
      reason: params?.reason,
      range: params?.range ?? "all",
      from: params?.from,
      to: params?.to,
    },
  });
  return data.items ?? [];
}

export type VerifyDRMLicenseResponse = {
  valid: boolean;
  canonical?: string;
  code?: string;
  claims?: {
    drm_type: string;
    media_id: number;
    kid: string;
    kid_version: string;
    key_ref: string;
    nonce: string;
    iat: number;
    exp: number;
    sig_version: string;
  };
  error?: string;
};

export async function verifyDRMLicense(payload: { license: string; sig: string }) {
  const { data } = await api.post<VerifyDRMLicenseResponse>("/api/v1/admin/drm/license/verify", payload);
  return data;
}

export type TranscodeTask = {
  id: number;
  file_id: string;
  quality: string;
  status: string;
  progress: number;
  error_message?: string;
  output_path: string;
  created_at: string;
  pipeline_type?: string;
  drm_status?: string;
  source_cleanup_status?: string;
};

export async function fetchTranscodeTasks(limit = 50) {
  const { data } = await api.get<{ items: TranscodeTask[] }>("/api/v1/transcode/task", {
    params: { limit },
  });
  return data.items;
}

export async function cancelTranscodeTask(id: number) {
  const { data } = await api.post<{ ok: boolean; cancelled: boolean }>(`/api/v1/transcode/task/${id}/cancel`);
  return data;
}

export async function cleanupFailedTranscodeTasks(limit?: number) {
  const payload = typeof limit === "number" && limit > 0 ? { limit } : {};
  const { data } = await api.post<{ deleted: number }>("/api/v1/transcode/task/cleanup-failed", payload);
  return data.deleted ?? 0;
}

export async function cleanupFailedTranscodeTasksBefore(days = 7) {
  const { data } = await api.post<{ deleted: number }>("/api/v1/transcode/task/cleanup-failed-before", { days });
  return data.deleted ?? 0;
}

export async function retryTranscodeTask(id: number) {
  const { data } = await api.post<{ ok: boolean; status: string; task_id: number }>(`/api/v1/transcode/task/${id}/retry`);
  return data;
}

export type AdminOverview = {
  monitor: {
    cpu_percent: number;
    memory_percent: number;
    disk_percent: number;
    transcode_task_count: number;
    media_total: number;
  };
  system: {
    cpu_count: number;
    memory_total: number;
    os: string;
    database: string;
    software_version: string;
  };
  activities: Array<{
    id: number;
    username: string;
    action: string;
    media_id: number;
    message: string;
    created_at: string;
  }>;
};

export async function fetchAdminOverview() {
  const { data } = await api.get<AdminOverview>("/api/v1/admin/overview");
  return data;
}

export type PreviewTask = {
  media_id: number;
  title: string;
  status: string;
  interval_sec: number;
  thumb_count: number;
  thumb_width: number;
  thumb_height: number;
  error_message?: string;
  updated_at: string;
};

export async function fetchPreviewTasks(limit = 100) {
  const { data } = await api.get<{ items: PreviewTask[] }>("/api/v1/preview/task", { params: { limit } });
  return data.items ?? [];
}

export async function retryPreviewTask(mediaId: number) {
  const { data } = await api.post<{ ok: boolean; status: string }>(`/api/v1/preview/task/${mediaId}/retry`);
  return data;
}

export type SubtitleTask = {
  id: number;
  media_id: number;
  title: string;
  status: string;
  message?: string;
  created_at: string;
  started_at?: string;
  finished_at?: string;
  updated_at: string;
};

export async function fetchSubtitleTasks(limit = 200) {
  const { data } = await api.get<{ items: SubtitleTask[] }>("/api/v1/subtitle/task", { params: { limit } });
  return data.items ?? [];
}

export async function resetSubtitleTask(mediaId: number) {
  await api.post(`/api/v1/subtitle/task/${mediaId}/reset`);
}

export async function retrySubtitleTask(mediaId: number) {
  await api.post(`/api/v1/subtitle/task/${mediaId}/retry`);
}

/** Reset subtitle output and re-run sidecar / embedded / ASR / OCR processing. */
export async function recognizeMediaSubtitles(mediaId: number) {
  await api.post(`/api/v1/media/${mediaId}/subtitle`);
}

export async function cleanupFailedSubtitleTasks() {
  const { data } = await api.post<{ deleted: number }>("/api/v1/subtitle/task/cleanup-failed");
  return data.deleted;
}

export async function cleanupSubtitleTasksBefore(days: number) {
  const { data } = await api.post<{ deleted: number; days: number }>("/api/v1/subtitle/task/cleanup-before", { days });
  return data.deleted;
}

export type ScheduledTask = {
  id: number;
  name: string;
  category: string;
  task_type: string;
  interval_min: number;
  enabled: number;
  payload?: Record<string, unknown>;
  last_run_at?: string;
  last_status?: string;
  last_message?: string;
  created_at: string;
  updated_at: string;
};

export async function fetchScheduledTasks() {
  const { data } = await api.get<{ items: ScheduledTask[] }>("/api/v1/schedule/task");
  return data.items ?? [];
}

export async function createScheduledTask(payload: {
  name: string;
  category?: string;
  task_type: string;
  interval_min: number;
  enabled?: number;
  payload?: Record<string, unknown>;
}) {
  const { data } = await api.post<{ id: number }>("/api/v1/schedule/task", payload);
  return data.id;
}

export async function updateScheduledTask(
  id: number,
  payload: {
    name: string;
    category?: string;
    task_type: string;
    interval_min: number;
    enabled?: number;
    payload?: Record<string, unknown>;
  }
) {
  await api.put(`/api/v1/schedule/task/${id}`, payload);
}

export async function deleteScheduledTask(id: number) {
  await api.delete(`/api/v1/schedule/task/${id}`);
}

export async function runScheduledTask(id: number) {
  const { data } = await api.post<{ ok: boolean; message?: string }>(`/api/v1/schedule/task/${id}/run`);
  return data;
}

export type AIProvider = {
  id: string;
  name: string;
  api_url: string;
  api_key: string;
  model: string;
  enabled: number;
  request_count: number;
  token_count: number;
  last_used_at?: string;
  updated_at?: string;
};

export async function fetchAIProviders() {
  const { data } = await api.get<{ items: AIProvider[] }>("/api/v1/ai-provider");
  return data.items ?? [];
}

export async function saveAIProvider(
  id: string,
  payload: { api_url?: string; api_key?: string; model?: string; enabled?: number },
) {
  await api.put(`/api/v1/ai-provider/${id}`, payload);
}

export async function testAIProvider(id: string) {
  const { data } = await api.post<ScrapeProviderTestResult>(`/api/v1/ai-provider/${id}/test`);
  return data;
}

export type ScrapeConfig = {
  enabled: number;
  providers: string[];
  image_sources: string[];
  api_keys: Record<string, string>;
};

export type ScrapeTask = {
  id: number;
  media_id: number;
  title: string;
  task_type: string;
  source: string;
  query: string;
  year: number;
  status: string;
  progress: number;
  fail_count?: number;
  message: string;
  created_at: string;
  started_at?: string;
  finished_at?: string;
};

export type ScrapeHistory = {
  id: number;
  task_id: number;
  media_id: number;
  source: string;
  query: string;
  status: string;
  message: string;
  created_at: string;
};

export async function fetchScrapeConfig() {
  const { data } = await api.get<ScrapeConfig>("/api/v1/scrape/config");
  return data;
}

export async function saveScrapeConfig(payload: {
  enabled: number;
  providers: string[];
  image_sources: string[];
  api_keys: Record<string, string>;
}) {
  await api.put("/api/v1/scrape/config", payload);
}

export type ScrapeProviderTestResult = {
  ok: boolean;
  message: string;
};

export async function testScrapeProvider(provider: string) {
  const { data } = await api.post<ScrapeProviderTestResult>("/api/v1/scrape/config/test", {
    provider,
  });
  return data;
}

export async function fetchScrapeTasks(limit = 100) {
  const { data } = await api.get<{ items: ScrapeTask[] }>("/api/v1/scrape/task", { params: { limit } });
  return data.items ?? [];
}

export async function createScrapeTasks(mediaIds: number[], source = "manual") {
  const { data } = await api.post<{ created: number }>("/api/v1/scrape/task", { media_ids: mediaIds, source });
  return data.created ?? 0;
}

export async function runScrapeTasks(ids?: number[], limit = 20) {
  const { data } = await api.post<{ done: number; failed: number }>("/api/v1/scrape/task/run", { ids, limit });
  return data;
}

export async function fetchScrapeHistory(limit = 100) {
  const { data } = await api.get<{ items: ScrapeHistory[] }>("/api/v1/scrape/history", { params: { limit } });
  return data.items ?? [];
}

export async function manualMatchMedia(
  mediaId: number,
  payload: {
    query?: string;
    year?: number;
    source?: string;
    external_id?: string;
    media_type?: string;
    language?: string;
    poster?: string;
    overview?: string;
  },
) {
  const { data } = await api.post<ManualMatchResponse>(`/api/v1/media/${mediaId}/manual-match`, payload);
  return data;
}

export type ScrapeMatchCandidate = {
  source: string;
  external_id: string;
  media_type?: string;
  title: string;
  overview?: string;
  poster?: string;
  year?: number;
  release_date?: string;
};

export async function parseScrapeTitle(raw: string) {
  const { data } = await api.get<{ title?: string; title_alt?: string; year?: number }>(
    "/api/v1/scrape/parse-title",
    { params: { raw } },
  );
  return {
    title: (data.title ?? "").trim(),
    titleAlt: (data.title_alt ?? "").trim(),
    year: typeof data.year === "number" && data.year > 0 ? data.year : undefined,
  };
}

export async function searchScrapeMatches(params: {
  query: string;
  year?: number;
  source?: string;
  language?: string;
  limit?: number;
}) {
  const { data } = await api.get<{ items?: ScrapeMatchCandidate[]; message?: string }>(
    "/api/v1/scrape/search",
    { params },
  );
  return { items: data?.items ?? [], message: data?.message };
}

export async function unmatchMedia(mediaId: number) {
  await api.delete(`/api/v1/media/${mediaId}/match`);
}

export async function updateMediaMetadata(
  mediaId: number,
  payload: { title?: string; overview?: string; rating?: number; genres?: string[] }
) {
  await api.patch(`/api/v1/media/${mediaId}/meta`, payload);
}

export async function updateMediaImages(
  mediaId: number,
  payload: { poster?: string; backdrop?: string; logo?: string }
) {
  await api.patch(`/api/v1/media/${mediaId}/images`, payload);
}

export async function searchTmdbImages(query: string, year?: number) {
  const { data } = await api.get<{
    tmdb_id: number;
    posters: string[];
    backdrops: string[];
    logos: string[];
  }>("/api/v1/scrape/tmdb/images", { params: { query, year } });
  return data;
}

export async function uploadImageFile(file: File) {
  const fd = new FormData();
  fd.append("file", file);
  const { data } = await api.post<{ ok: boolean; url: string; path: string }>("/api/v1/upload/image", fd, {
    headers: { "Content-Type": "multipart/form-data" },
  });
  return data;
}

export async function createUploadDirectory(payload: {
  library_id?: number;
  target_dir?: string;
  name: string;
}) {
  const { data } = await api.post<{ ok: boolean; path: string }>("/api/v1/upload/mkdir", payload);
  return data;
}

// --- Audio Track Extraction (atrack) ---

export type AtrackTask = {
  id: number;
  media_id: number;
  title: string;
  file_path: string;
  status: string;
  output_dir: string;
  error_message?: string;
  created_at: string;
  updated_at: string;
};

export async function extractAudioTrack(mediaId: number) {
  await api.post(`/api/v1/media/${mediaId}/atrack`);
}

export async function retryAudioTrackExtraction(mediaId: number) {
  await api.post(`/api/v1/atrack/task/${mediaId}/retry`);
}

export async function fetchAtrackTasks(limit = 100) {
  const { data } = await api.get<{ items: AtrackTask[] }>("/api/v1/atrack/task", { params: { limit } });
  return data.items ?? [];
}

// --- Keyframe Extraction ---

export type KeyframeTask = {
  id: number;
  media_id: number;
  title: string;
  file_path: string;
  status: string;
  output_dir: string;
  keyframe_count: number;
  error_message?: string;
  created_at: string;
  updated_at: string;
};

export async function extractKeyframes(mediaId: number) {
  await api.post(`/api/v1/media/${mediaId}/keyframe`);
}

export async function retryKeyframeExtraction(mediaId: number) {
  await api.post(`/api/v1/keyframe/task/${mediaId}/retry`);
}

export async function fetchKeyframeTasks(limit = 100) {
  const { data } = await api.get<{ items: KeyframeTask[] }>("/api/v1/keyframe/task", { params: { limit } });
  return data.items ?? [];
}

// --- System options (admin) ---

export type SystemOptionsGeneral = {
  display_language: string;
  start_on_boot: boolean;
  open_browser_on_first_start: boolean;
  maintenance_mode: boolean;
  cache_path: string;
  auto_update_enabled: boolean;
};

export type SystemOptionsPlayback = {
  home_stream_quality: string;
  screen_orientation: string;
};

export type SystemOptionsTranscoder = {
  quality: string;
  temp_dir: string;
  download_temp_dir: string;
  throttle_buffer_seconds: number;
  background_x264_preset: string;
  disable_video_stream_transcoding: boolean;
  max_cpu_concurrent: string;
  max_background_concurrent: string;
};

export type SystemOptionsASR = {
  provider: string;
  whisper_path: string;
  extra_args: string[];
  shell: string;
};

export type SystemOptionsOCR = {
  enabled: boolean;
  tesseract_path: string;
  tessdata_prefix: string;
  languages: string;
  python_path: string;
  script_path: string;
  pgsrip_path: string;
  mkvextract_path: string;
  mkvmerge_path: string;
};

export type SystemOptionsRecognition = {
  asr: SystemOptionsASR;
  ocr: SystemOptionsOCR;
};

export type SystemOptions = {
  general: SystemOptionsGeneral;
  playback: SystemOptionsPlayback;
  transcoder: SystemOptionsTranscoder;
  recognition: SystemOptionsRecognition;
};

export async function fetchSystemOptions() {
  const { data } = await api.get<SystemOptions>("/api/v1/admin/system-options");
  return data;
}

export async function saveSystemOptions(payload: SystemOptions) {
  const { data } = await api.put<{ ok: boolean; options?: SystemOptions }>("/api/v1/admin/system-options", payload);
  if (!data?.options) {
    throw new Error("保存响应无效");
  }
  return data.options;
}

export type RecognitionTestResult = {
  ok: boolean;
  message: string;
};

export async function testSystemOptionsASR(asr?: SystemOptionsASR) {
  const { data } = await api.post<RecognitionTestResult>("/api/v1/admin/system-options/test/asr", asr ? { asr } : {});
  return data;
}

export async function testSystemOptionsOCR(ocr?: SystemOptionsOCR) {
  const { data } = await api.post<RecognitionTestResult>("/api/v1/admin/system-options/test/ocr", ocr ? { ocr } : {});
  return data;
}

export type RecognitionInstallResult = {
  ok: boolean;
  message: string;
  recognition?: SystemOptionsRecognition;
};

export async function installSystemOptionsASR() {
  const { data } = await api.post<RecognitionInstallResult>(
    "/api/v1/admin/system-options/install/asr",
    {},
    { timeout: 45 * 60 * 1000 },
  );
  return data;
}

export async function installSystemOptionsOCR() {
  const { data } = await api.post<RecognitionInstallResult>(
    "/api/v1/admin/system-options/install/ocr",
    {},
    { timeout: 45 * 60 * 1000 },
  );
  return data;
}
