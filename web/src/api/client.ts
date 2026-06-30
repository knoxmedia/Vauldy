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
  encrypted_assets_enabled?: number;
  encrypted_assets_cleanup_plaintext?: number;
  encrypted_assets_dir_mode?: "library" | "data" | "custom";
  encrypted_assets_custom_dir?: string;
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
  /** 1 when the current user has marked or finished watching this item. */
  completed?: number;
  release_date?: string;
  year?: number;
  /** From scrape or empty; UI may fall back to authenticated `/api/v1/media/{id}/poster.jpg`. */
  poster_url?: string;
  /** Landscape backdrop from scrape (TV / anime shelves). */
  backdrop_url?: string;
  /** EXIF or file mtime for photo libraries. */
  photo_taken_at?: string;
  /** AI / manual classification tags. */
  photo_tags?: string[];
  /** Stable catalog ids for photo_tags (builtin categories). */
  photo_tag_ids?: string[];
  /** True when meaningful scrape metadata exists. */
  scraped?: boolean;
  /** Knox 9527 envelope encryption at rest. */
  encrypted_asset?: boolean;
  /** Populated for audio tracks linked in music_track. */
  music_album_id?: number;
  music_album_title?: string;
  music_artist?: string;
  /** Joined from library.type when listing favorites. */
  library_type?: string;
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

/** Append JWT for authenticated media asset URLs used in <img src>. */
export function withAccessToken(url: string): string {
  const u = (url || "").trim();
  if (!u.startsWith("/api/v1/")) return u;
  const token = useAuthStore.getState().token;
  if (!token) return u;
  const sep = u.includes("?") ? "&" : "?";
  return `${u}${sep}access_token=${encodeURIComponent(token)}`;
}

/** Scrape/list poster or backdrop URL with JWT when pointing at Knox API assets. */
export function authListPosterUrl(raw: string): string {
  const u = normalizeListPosterUrl(raw);
  return u ? withAccessToken(u) : "";
}

/** Server-generated frame capture for encrypted libraries (auth + decrypt). */
export function derivedVideoPosterSrc(id: number): string {
  return withAccessToken(`/api/v1/media/${id}/poster.jpg`);
}

/** Server-generated frame capture when scrape poster is missing or failed to load. */
export function localPosterSrc(id: number, encryptedAsset?: boolean | number): string {
  if (encryptedAsset) return derivedVideoPosterSrc(id);
  return `/uploads/posters/${id}.jpg`;
}

/** True when meta_json has a scraped poster URL (may still 404 at runtime). */
export function hasScrapedPosterUrl(r: Pick<MediaItem, "poster_url">): boolean {
  return Boolean(normalizeListPosterUrl(r.poster_url || ""));
}

/** Album artwork for music tracks; null when none (UI should leave cover blank). */
export function musicMediaPosterSrc(
  r: Pick<MediaItem, "id" | "poster_url" | "music_album_id" | "file_type">,
): string | null {
  if (r.file_type !== "audio") {
    return mediaPosterSrc(r);
  }
  if (r.music_album_id && r.music_album_id > 0) {
    return albumArtworkSrc(r.music_album_id);
  }
  if (hasScrapedPosterUrl(r)) {
    return normalizeListPosterUrl(r.poster_url || "");
  }
  return null;
}

/** 16:9 shelf / thumb: prefer scraped backdrop, then poster / frame capture. */
export function mediaLandscapeThumbSrc(
  r: Pick<MediaItem, "id" | "poster_url" | "backdrop_url" | "music_album_id"> & { file_type?: string },
): string {
  if (r.file_type === "image" || r.file_type === "document" || r.file_type === "audio") {
    return mediaPosterSrc(r);
  }
  const backdrop = normalizeListPosterUrl(r.backdrop_url || "");
  if (backdrop) return withAccessToken(backdrop);
  return mediaPosterSrc(r);
}

/** Poster/thumbnail URL for grids: scraped poster or server-generated frame capture. */
export function mediaPosterSrc(
  r: Pick<MediaItem, "id" | "poster_url" | "music_album_id" | "encrypted_asset"> & { file_type?: string },
): string {
  if (r.file_type === "audio") {
    return musicMediaPosterSrc({ ...r, file_type: "audio" }) ?? "";
  }
  if (r.file_type === "image") {
    return photoThumbSrc(r.id);
  }
  if (r.file_type === "document") {
    return documentCoverSrc(r.id);
  }
  const u = normalizeListPosterUrl(r.poster_url || "");
  if (u) return withAccessToken(u);
  return localPosterSrc(r.id, r.encrypted_asset);
}

/** Detail page poster: meta scrape path or derived frame capture for encrypted video. */
export function mediaDetailPosterSrc(
  detail: Pick<MediaItem, "id" | "file_path" | "poster_url" | "encrypted_asset">,
  metaPoster?: string,
): string {
  const fromMeta = authListPosterUrl(metaPoster || "");
  if (fromMeta) return fromMeta;
  const encrypted =
    Boolean(detail.encrypted_asset) || (detail.file_path || "").toLowerCase().endsWith(".enc");
  return mediaPosterSrc({
    id: detail.id,
    poster_url: detail.poster_url,
    encrypted_asset: encrypted,
  });
}

/** Cached photo thumbnail (480px max edge). */
export function photoThumbSrc(id: number): string {
  const token = useAuthStore.getState().token;
  const q = token ? `?access_token=${encodeURIComponent(token)}` : "";
  return `/api/v1/media/${id}/photo/thumb.jpg${q}`;
}

/** Cached photo medium preview (1920px max edge). */
export function photoMediumSrc(id: number): string {
  const token = useAuthStore.getState().token;
  const q = token ? `?access_token=${encodeURIComponent(token)}` : "";
  return `/api/v1/media/${id}/photo/medium.jpg${q}`;
}

/** Original image file for download / full-screen view. */
export function photoOriginalSrc(id: number): string {
  const token = useAuthStore.getState().token;
  const q = token ? `?access_token=${encodeURIComponent(token)}` : "";
  return `/api/v1/media/${id}/play${q}`;
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
  library_type?: string;
};

export async function fetchLibraries() {
  const { data } = await api.get<{ items?: Library[] }>("/api/v1/library");
  return data?.items ?? [];
}

export type EncryptedAssetsConfig = {
  data_dot_encrypted_dir?: string;
};

export async function fetchLibrariesWithCapabilities() {
  const { data } = await api.get<{
    items?: Library[];
    drm_capabilities?: DRMCapabilities;
    encrypted_assets_config?: EncryptedAssetsConfig;
  }>("/api/v1/library");
  return {
    items: data?.items ?? [],
    drmCapabilities: data?.drm_capabilities ?? { widevine_enabled: true, powerdrm_enabled: true },
    encryptedAssetsConfig: data?.encrypted_assets_config ?? {},
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
  encrypted_assets_enabled?: number;
  encrypted_assets_cleanup_plaintext?: number;
  encrypted_assets_dir_mode?: "library" | "data" | "custom";
  encrypted_assets_custom_dir?: string;
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
    encrypted_assets_enabled?: number;
    encrypted_assets_cleanup_plaintext?: number;
    encrypted_assets_dir_mode?: "library" | "data" | "custom";
    encrypted_assets_custom_dir?: string;
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
  opts?: {
    sort?: "id_desc" | "created_desc" | "taken_desc";
    limit?: number;
    file_type?: string;
    photo_tag?: string;
    photo_place?: string;
    photo_person?: string;
    /** Full-text fuzzy search across title, overview, genres, tags, etc. */
    q?: string;
  },
) {
  const params: Record<string, string | number> = {};
  if (libraryId !== undefined) params.library_id = libraryId;
  if (opts?.sort) params.sort = opts.sort;
  if (opts?.limit !== undefined) params.limit = opts.limit;
  if (opts?.file_type) params.file_type = opts.file_type;
  if (opts?.photo_tag) params.photo_tag = opts.photo_tag;
  if (opts?.photo_place) params.photo_place = opts.photo_place;
  if (opts?.photo_person) params.photo_person = opts.photo_person;
  if (opts?.q) params.q = opts.q;
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
  completed?: number;
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

export function isMusicLibraryType(type?: string): boolean {
  return (type || "").trim().toLowerCase() === "music";
}

export function isPhotoLibraryType(type?: string): boolean {
  return (type || "").trim().toLowerCase() === "photo";
}

export function isDocumentLibraryType(type?: string): boolean {
  return (type || "").trim().toLowerCase() === "document";
}

export type DocumentItem = {
  id: number;
  file_id?: string;
  title: string;
  file_path?: string;
  format: string;
  author?: string;
  publisher?: string;
  year?: number;
  file_size?: number;
  modified_at?: string;
  description?: string;
  page_count?: number;
  created_at?: string;
  last_read_at?: string;
  tags?: string[];
  cover_url?: string;
};

export type DocumentFacet = {
  name: string;
  count: number;
};

export type DocumentNode = {
  path: string;
  name: string;
  node_type: "dir" | "file";
  media_id?: number;
};

export async function fetchDocuments(
  libraryId: number,
  params?: Record<string, string | number | boolean | undefined>,
): Promise<DocumentItem[]> {
  const { data } = await api.get<{ items?: DocumentItem[] }>(`/api/v1/library/${libraryId}/documents`, { params });
  return data?.items ?? [];
}

export async function fetchDocumentNodes(libraryId: number, parent = ""): Promise<DocumentNode[]> {
  const { data } = await api.get<{ items?: DocumentNode[] }>(`/api/v1/library/${libraryId}/document/nodes`, {
    params: { parent },
  });
  return data?.items ?? [];
}

export async function fetchDocumentFacets(libraryId: number, kind: string): Promise<DocumentFacet[]> {
  const { data } = await api.get<{ items?: DocumentFacet[] }>(`/api/v1/library/${libraryId}/document/facets`, {
    params: { kind },
  });
  return data?.items ?? [];
}

export async function fetchRecentDocuments(libraryId?: number): Promise<DocumentItem[]> {
  const { data } = await api.get<{ items?: DocumentItem[] }>(`/api/v1/library/${libraryId ?? 0}/documents/recent`, {
    params: libraryId ? { library_id: libraryId } : undefined,
  });
  return data?.items ?? [];
}

export async function fetchDocumentDetail(id: number) {
  const { data } = await api.get(`/api/v1/media/${id}/document`);
  return data;
}

export async function saveReadProgress(id: number, position: string, percent?: number) {
  const { data } = await api.post(`/api/v1/media/${id}/read-progress`, { position, percent });
  return data;
}

export async function fetchReadProgress(id: number): Promise<{ position: string; percent: number }> {
  const { data } = await api.get<{ position?: string; percent?: number }>(`/api/v1/media/${id}/read-progress`);
  return { position: data?.position ?? "", percent: data?.percent ?? 0 };
}

export async function updateDocumentMeta(
  id: number,
  patch: { title?: string; author?: string; publisher?: string; year?: number; description?: string; tags?: string[] },
) {
  const { data } = await api.patch(`/api/v1/media/${id}/document`, patch);
  return data;
}

export function documentCoverSrc(id: number, token?: string | null): string {
  const base = `/api/v1/media/${id}/document/cover.jpg`;
  const t = token ?? useAuthStore.getState().token;
  if (t) return `${base}?access_token=${encodeURIComponent(t)}`;
  return base;
}

export function documentStreamSrc(id: number, token?: string | null): string {
  const base = `/api/v1/media/${id}/play`;
  const t = token ?? useAuthStore.getState().token;
  if (t) return `${base}?access_token=${encodeURIComponent(t)}`;
  return base;
}

export function documentDownloadSrc(id: number, token?: string | null): string {
  const base = documentStreamSrc(id, token);
  return `${base}${base.includes("?") ? "&" : "?"}download=1`;
}

export function documentPreviewSrc(id: number, token?: string | null): string {
  const base = `/api/v1/media/${id}/document/preview.pdf`;
  const t = token ?? useAuthStore.getState().token;
  if (t) return `${base}?access_token=${encodeURIComponent(t)}`;
  return base;
}

export const OFFICE_DOCUMENT_FORMATS = new Set(["doc", "docx", "xls", "xlsx", "ppt", "pptx"]);

export function isOfficeDocumentFormat(format?: string): boolean {
  return OFFICE_DOCUMENT_FORMATS.has((format || "").trim().toLowerCase());
}

export async function batchDownloadDocuments(mediaIds: number[]): Promise<Blob> {
  const { data } = await api.post("/api/v1/documents/download", { media_ids: mediaIds }, { responseType: "blob" });
  return data as Blob;
}

export type PhotoCategory = {
  id: string;
  name: string;
  type: string;
  count: number;
};

export type PhotoPlace = {
  id: string;
  name: string;
  type: string;
  count: number;
  cover_id?: number;
};

export type PhotoPerson = {
  id: number;
  name: string;
  count: number;
  cover_face_id?: number;
};

export async function fetchPhotoCategories(libraryId: number): Promise<PhotoCategory[]> {
  const { data } = await api.get<{ items?: PhotoCategory[] }>(`/api/v1/library/${libraryId}/photo/categories`);
  return data?.items ?? [];
}

export async function fetchPhotoPlaces(libraryId: number): Promise<PhotoPlace[]> {
  const { data } = await api.get<{ items?: PhotoPlace[] }>(`/api/v1/library/${libraryId}/photo/places`);
  return data?.items ?? [];
}

export async function fetchPhotoPersons(libraryId: number): Promise<PhotoPerson[]> {
  const { data } = await api.get<{ items?: PhotoPerson[] }>(`/api/v1/library/${libraryId}/photo/persons`);
  return data?.items ?? [];
}

export async function updatePhotoPersonName(
  libraryId: number,
  personId: number,
  name: string,
): Promise<{ ok: boolean; name: string }> {
  const { data } = await api.patch<{ ok: boolean; name: string }>(
    `/api/v1/library/${libraryId}/photo/persons/${personId}`,
    { name },
  );
  return data ?? { ok: false, name };
}

export function photoFaceThumbSrc(faceId: number): string {
  const token = useAuthStore.getState().token;
  const params = new URLSearchParams({ v: "2" });
  if (token) params.set("access_token", token);
  return `/api/v1/photo/face/${faceId}/thumb.jpg?${params.toString()}`;
}

export async function backfillPhotoFaces(libraryId: number): Promise<{ ok: boolean; queued: number }> {
  const { data } = await api.post<{ ok: boolean; queued: number }>(
    `/api/v1/library/${libraryId}/photo/faces/backfill`,
  );
  return data ?? { ok: false, queued: 0 };
}

export async function fetchPhotoFaceProgress(libraryId: number): Promise<{
  total: number;
  processed: number;
  detected: number;
  pending: number;
  failed: number;
  percent: number;
}> {
  const { data } = await api.get<{
    total: number;
    processed: number;
    detected: number;
    pending: number;
    failed?: number;
    percent: number;
  }>(`/api/v1/library/${libraryId}/photo/faces/progress`);
  return {
    total: data?.total ?? 0,
    processed: data?.processed ?? 0,
    detected: data?.detected ?? 0,
    pending: data?.pending ?? 0,
    failed: data?.failed ?? 0,
    percent: data?.percent ?? 0,
  };
}

export async function backfillPhotoLocations(libraryId: number): Promise<{ ok: boolean; queued: number }> {
  const { data } = await api.post<{ ok: boolean; queued: number }>(
    `/api/v1/library/${libraryId}/photo/locations/backfill`,
  );
  return data ?? { ok: false, queued: 0 };
}

export async function fetchPhotoLocationProgress(libraryId: number): Promise<{
  total: number;
  located: number;
  pending: number;
  percent: number;
}> {
  const { data } = await api.get<{ total: number; located: number; pending: number; percent: number }>(
    `/api/v1/library/${libraryId}/photo/locations/progress`,
  );
  return data ?? { total: 0, located: 0, pending: 0, percent: 0 };
}

export async function fetchPhotoClassifyProgress(libraryId: number): Promise<{
  total: number;
  classified: number;
  pending: number;
  percent: number;
}> {
  const { data } = await api.get<{ total: number; classified: number; pending: number; percent: number }>(
    `/api/v1/library/${libraryId}/photo/classify/progress`,
  );
  return data ?? { total: 0, classified: 0, pending: 0, percent: 0 };
}

export async function enqueuePhotoLibraryClassify(
  libraryId: number,
  force = false,
): Promise<{ ok: boolean; queued: number }> {
  const { data } = await api.post<{ ok: boolean; queued: number }>(
    `/api/v1/library/${libraryId}/photo/classify`,
    {},
    { params: force ? { force: "1" } : undefined },
  );
  return data ?? { ok: false, queued: 0 };
}

export async function updatePhotoTags(mediaId: number, tags: string[]): Promise<void> {
  await api.patch(`/api/v1/media/${mediaId}/photo/tags`, { tags });
}

export type AlbumSummary = {
  id: number;
  library_id: number;
  title: string;
  title_norm?: string;
  year?: number;
  genre?: string;
  artwork_path?: string;
  album_artist?: string;
  album_artist_id?: number;
  track_count?: number;
  total_duration?: number;
  is_unknown?: boolean;
  rating?: number;
  created_at?: string;
  updated_at?: string;
};

export type MusicTrackRow = {
  id: number;
  media_id: number;
  track_number?: number;
  title: string;
  artist?: string;
  duration?: number;
  bitrate?: number;
  format?: string;
  album_id?: number;
  album_title?: string;
  album_artist?: string;
  artist_id?: number;
  year?: number;
  artwork_path?: string;
  file_path?: string;
  created_at?: string;
};

export type AlbumDetail = AlbumSummary & {
  tracks?: MusicTrackRow[];
  meta_json?: string;
};

export type ArtistSummary = {
  id: number;
  library_id: number;
  name: string;
  name_norm?: string;
  artwork_path?: string;
  album_count?: number;
  track_count?: number;
};

export type GenreSummary = {
  genre: string;
  album_count?: number;
  track_count?: number;
};

export function albumArtworkSrc(albumId: number): string {
  const token = useAuthStore.getState().token;
  const q = token ? `?access_token=${encodeURIComponent(token)}` : "";
  return `/api/v1/album/${albumId}/artwork${q}`;
}

export function artistArtworkSrc(artistId: number): string {
  const token = useAuthStore.getState().token;
  const q = token ? `?access_token=${encodeURIComponent(token)}` : "";
  return `/api/v1/artist/${artistId}/artwork${q}`;
}

export async function fetchLibraryAlbums(libraryId: number) {
  const { data } = await api.get<{ items?: AlbumSummary[] }>(`/api/v1/library/${libraryId}/albums`);
  return data?.items ?? [];
}

export async function fetchLibraryArtists(libraryId: number) {
  const { data } = await api.get<{ items?: ArtistSummary[] }>(`/api/v1/library/${libraryId}/artists`);
  return data?.items ?? [];
}

export async function fetchLibraryGenres(libraryId: number) {
  const { data } = await api.get<{ items?: GenreSummary[] }>(`/api/v1/library/${libraryId}/genres`);
  return data?.items ?? [];
}

export async function fetchLibraryTracks(libraryId: number) {
  const { data } = await api.get<{ items?: MusicTrackRow[] }>(`/api/v1/library/${libraryId}/tracks`);
  return data?.items ?? [];
}

export async function fetchAlbum(albumId: number) {
  const { data } = await api.get<AlbumDetail>(`/api/v1/album/${albumId}`);
  return data;
}

export async function updateAlbum(
  albumId: number,
  payload: { title?: string; year?: number; genre?: string; artwork?: string },
) {
  const { data } = await api.patch<{
    ok: boolean;
    id: number;
    title: string;
    year?: number;
    genre?: string;
    artwork_path?: string;
  }>(`/api/v1/album/${albumId}`, payload);
  return data;
}

/** Fetch poster candidates for an album from the library's configured image scrape sources. */
export async function fetchAlbumImageCandidates(
  albumId: number,
  kind: "poster" | "backdrop" | "logo" = "poster",
): Promise<ImageCandidatesResponse> {
  const { data } = await api.get<ImageCandidatesResponse>(
    `/api/v1/album/${albumId}/image-candidates`,
    { params: { kind } },
  );
  return data;
}

export type AlbumPlayTarget = {
  media_id: number;
  position: number;
};

export async function fetchAlbumPlayTarget(albumId: number) {
  const { data } = await api.get<AlbumPlayTarget>(`/api/v1/album/${albumId}/play-target`);
  return data;
}

export type ArtistAlbumsResponse = {
  items?: AlbumSummary[];
  artist_id?: number;
  artist_name?: string;
};

export async function fetchGenreAlbums(libraryId: number, genre: string) {
  const { data } = await api.get<{ items?: AlbumSummary[]; library_id?: number; genre?: string }>(
    `/api/v1/library/${libraryId}/genre/albums`,
    { params: { genre } },
  );
  return data;
}

export async function fetchArtistAlbums(artistId: number) {
  const { data } = await api.get<ArtistAlbumsResponse>(`/api/v1/artist/${artistId}/albums`);
  return data;
}

export async function fetchArtist(artistId: number) {
  const { data } = await api.get<ArtistSummary>(`/api/v1/artist/${artistId}`);
  return data;
}

export async function updateArtist(
  artistId: number,
  payload: { name?: string; artwork?: string },
) {
  const { data } = await api.patch<{
    ok: boolean;
    id: number;
    name: string;
    artwork_path?: string;
  }>(`/api/v1/artist/${artistId}`, payload);
  return data;
}

export async function fetchArtistImageCandidates(
  artistId: number,
  kind: "poster" | "backdrop" | "logo" = "poster",
): Promise<ImageCandidatesResponse> {
  const { data } = await api.get<ImageCandidatesResponse>(
    `/api/v1/artist/${artistId}/image-candidates`,
    { params: { kind } },
  );
  return data;
}

export async function updateLibraryGenre(
  libraryId: number,
  payload: { old_name: string; new_name: string },
) {
  const { data } = await api.patch<{ ok: boolean; genre: string; updated_albums?: number }>(
    `/api/v1/library/${libraryId}/genre`,
    payload,
  );
  return data;
}

export function seriesPosterSrc(s: Pick<SeriesSummary, "id" | "poster_url" | "poster">): string {
  const u = normalizeListPosterUrl(s.poster_url || s.poster || "");
  return u ? withAccessToken(u) : "";
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

export async function fetchUserHistory(limit = 24, opts?: { libraryTypes?: readonly string[] }) {
  const params: Record<string, string | number> = { limit };
  const types = opts?.libraryTypes?.map((t) => t.trim()).filter(Boolean);
  if (types?.length) {
    params.library_types = types.join(",");
  }
  const { data } = await api.get<{ items?: HistoryItem[] }>("/api/v1/user/history", {
    params,
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

export interface FavoriteFolderItem {
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

export interface FavoriteFolderPreview {
  media_id: number;
  poster_url: string;
}

export interface FavoriteFolder {
  id: number;
  name: string;
  description: string;
  item_count: number;
  first_media_id: number;
  cover_url: string;
  created_at: string;
  updated_at: string;
  preview_items?: FavoriteFolderPreview[];
  items?: FavoriteFolderItem[];
}

export async function fetchFavoriteFolders() {
  const { data } = await api.get<{ items?: FavoriteFolder[] }>("/api/v1/favorite-folders");
  return data?.items ?? [];
}

export async function fetchFavoriteFolder(id: number) {
  const { data } = await api.get<FavoriteFolder>(`/api/v1/favorite-folders/${id}`);
  return data;
}

export async function createFavoriteFolder(name: string, description = "") {
  const { data } = await api.post<{ id: number }>("/api/v1/favorite-folders", { name, description });
  return data.id;
}

export async function updateFavoriteFolder(id: number, name: string, description = "") {
  await api.put(`/api/v1/favorite-folders/${id}`, { name, description });
}

export async function deleteFavoriteFolder(id: number) {
  await api.delete(`/api/v1/favorite-folders/${id}`);
}

export async function addFavoriteFolderItem(folderId: number, mediaId: number) {
  await api.post(`/api/v1/favorite-folders/${folderId}/items`, { media_id: mediaId });
}

export async function removeFavoriteFolderItem(folderId: number, itemId: number) {
  await api.delete(`/api/v1/favorite-folders/${folderId}/items/${itemId}`);
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

/** Set when playing an album; MusicPlayerBar / Player auto-advances on track end. */
export const ALBUM_PLAY_SESSION_KEY = "knox_album_session";

/** Direct file stream URL for audio/video native playback. */
export function mediaPlaySrc(mediaId: number): string {
  const token = useAuthStore.getState().token;
  const q = token ? `?access_token=${encodeURIComponent(token)}` : "";
  return `/api/v1/media/${mediaId}/play${q}`;
}

export interface MediaLyricsResponse {
  lrc: string;
  source: string;
}

export async function fetchMediaLyrics(mediaId: number): Promise<MediaLyricsResponse> {
  const { data } = await api.get<MediaLyricsResponse>(`/api/v1/media/${mediaId}/lyrics`);
  return data ?? { lrc: "", source: "" };
}

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

export async function deleteSubtitleTask(mediaId: number) {
  await api.delete(`/api/v1/subtitle/task/${mediaId}`);
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

export type LyricTask = {
  id: number;
  media_id: number;
  title: string;
  status: string;
  message?: string;
  vtt_path?: string;
  lrc_path?: string;
  created_at: string;
  started_at?: string;
  finished_at?: string;
  updated_at: string;
};

export async function fetchLyricTasks(limit = 200) {
  const { data } = await api.get<{ items: LyricTask[] }>("/api/v1/lyric/task", { params: { limit } });
  return data.items ?? [];
}

/** Enqueue ASR lyric recognition for an audio track (VTT → sidecar LRC). */
export async function enqueueLyricRecognition(mediaId: number) {
  await api.post(`/api/v1/media/${mediaId}/lyrics/recognize`);
}

export type SubtitleCue = {
  start: string;
  end: string;
  text: string;
};

export type SubtitleCuesResponse = {
  format: string;
  cues: SubtitleCue[];
};

export async function fetchSubtitleCues(mediaId: number, subtitleId: number): Promise<SubtitleCuesResponse> {
  const { data } = await api.get<SubtitleCuesResponse>(`/api/v1/media/${mediaId}/subtitles/${subtitleId}/cues`);
  return data ?? { format: "vtt", cues: [] };
}

export async function saveSubtitleCues(mediaId: number, subtitleId: number, cues: SubtitleCue[]): Promise<void> {
  await api.put(`/api/v1/media/${mediaId}/subtitles/${subtitleId}/cues`, { cues });
}

export async function importSubtitle(mediaId: number, file: File): Promise<MediaSubtitleRow> {
  const form = new FormData();
  form.append("file", file);
  const { data } = await api.post<{ ok: boolean; subtitle: MediaSubtitleRow }>(`/api/v1/media/${mediaId}/subtitles/import`, form, {
    headers: { "Content-Type": "multipart/form-data" },
  });
  return data.subtitle;
}

export async function saveMediaLyrics(mediaId: number, lrc: string): Promise<void> {
  await api.put(`/api/v1/media/${mediaId}/lyrics`, { lrc });
}

export async function importMediaLyrics(mediaId: number, file: File): Promise<void> {
  const form = new FormData();
  form.append("file", file);
  await api.post(`/api/v1/media/${mediaId}/lyrics/import`, form, {
    headers: { "Content-Type": "multipart/form-data" },
  });
}

export async function retryLyricTask(mediaId: number) {
  await api.post(`/api/v1/lyric/task/${mediaId}/retry`);
}

export async function cleanupFailedLyricTasks() {
  const { data } = await api.post<{ deleted: number }>("/api/v1/lyric/task/cleanup-failed");
  return data.deleted;
}

export async function cleanupLyricTasksBefore(days: number) {
  const { data } = await api.post<{ deleted: number; days: number }>("/api/v1/lyric/task/cleanup-before", { days });
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

export interface MediaImageCandidate {
  url: string;
  /** Backend provider id, e.g. "tmdb" | "douban" | "bangumi" | "tvdb" | "omdb" | "fanart". */
  source: string;
}

export type ImageCandidatesResponse = {
  candidates: MediaImageCandidate[];
  errors?: Record<string, string>;
  /** True when at least one online image scrape source was contacted. */
  scraped?: boolean;
};

/**
 * Fetch poster/backdrop/logo candidates for a media item from the image sources
 * configured on the media's owning library. The backend only contacts sources
 * selected for that library, so unreachable providers can be omitted from the
 * library config to avoid long connection delays.
 */
export async function fetchMediaImageCandidates(
  mediaId: number,
  kind: "poster" | "backdrop" | "logo",
): Promise<ImageCandidatesResponse> {
  const { data } = await api.get<ImageCandidatesResponse>(
    `/api/v1/media/${mediaId}/image-candidates`,
    { params: { kind } },
  );
  return data;
}

/** Same as fetchMediaImageCandidates but for a TV series row (uses library image sources). */
export async function fetchSeriesImageCandidates(
  seriesId: number,
  kind: "poster" | "backdrop" | "logo",
): Promise<ImageCandidatesResponse> {
  const { data } = await api.get<ImageCandidatesResponse>(
    `/api/v1/series/${seriesId}/image-candidates`,
    { params: { kind } },
  );
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

/** Queue Knox 9527 envelope encryption for a single media item. */
export async function encryptMediaAssets(mediaId: number) {
  await api.post(`/api/v1/media/${mediaId}/encrypt-assets`);
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
  hardware_acceleration: string;
  enable_hardware_encoding: boolean;
  disable_video_stream_transcoding: boolean;
  max_cpu_concurrent: string;
  max_background_concurrent: string;
};

export type SystemOptionsASR = {
  auto_on_scan: boolean;
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
  ai_proofread: boolean;
};

export type SystemOptionsPhotoClassify = {
  auto_on_scan: boolean;
  engine: string;
  python_path: string;
  script_path: string;
  model_path: string;
  labels_path: string;
};

export type SystemOptionsPhotoFace = {
  auto_on_scan: boolean;
  python_path: string;
  script_path: string;
  similarity_threshold: number;
};

export type SystemOptionsDocTrans = {
  enabled: boolean;
  engine_order: string[];
  libreoffice_path: string;
  soffice_path: string;
  office_path: string;
  wps_path: string;
  cache_dir: string;
  cache_ttl_days: number;
  timeout_seconds: number;
};

export type DocTransEngineStatus = {
  kind: string;
  label: string;
  available: boolean;
  path?: string;
  version?: string;
  message?: string;
};

export type DocTransTestResult = {
  ok: boolean;
  message: string;
  active_engine?: string;
  engines?: DocTransEngineStatus[];
  soffice_path?: string;
  version?: string;
};

export type SystemOptions = {
  general: SystemOptionsGeneral;
  playback: SystemOptionsPlayback;
  transcoder: SystemOptionsTranscoder;
  recognition: SystemOptionsRecognition;
  photo_classify: SystemOptionsPhotoClassify;
  photo_face: SystemOptionsPhotoFace;
  doc_trans: SystemOptionsDocTrans;
};

export type SystemOptionsResponse = SystemOptions & {
  available_hardware_acceleration?: string[];
};

export async function fetchSystemOptions() {
  const { data } = await api.get<SystemOptionsResponse>("/api/v1/admin/system-options");
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

export type PhotoClassifyInstallResult = {
  ok: boolean;
  message: string;
  photo_classify?: SystemOptionsPhotoClassify;
};

export async function testSystemOptionsPhotoClassify(photoClassify?: SystemOptionsPhotoClassify) {
  const { data } = await api.post<RecognitionTestResult>(
    "/api/v1/admin/system-options/test/photo-classify",
    photoClassify ? { photo_classify: photoClassify } : {},
  );
  return data;
}

export async function installSystemOptionsPhotoClassify() {
  const { data } = await api.post<PhotoClassifyInstallResult>(
    "/api/v1/admin/system-options/install/photo-classify",
    {},
    { timeout: 45 * 60 * 1000 },
  );
  return data;
}

export type PhotoFaceInstallResult = {
  ok: boolean;
  message: string;
  photo_face?: SystemOptionsPhotoFace;
};

export async function testSystemOptionsPhotoFace(photoFace?: SystemOptionsPhotoFace) {
  const { data } = await api.post<RecognitionTestResult>(
    "/api/v1/admin/system-options/test/photo-face",
    photoFace ? { photo_face: photoFace } : {},
    { timeout: 10 * 60 * 1000 },
  );
  return data;
}

export async function installSystemOptionsPhotoFace() {
  const { data } = await api.post<PhotoFaceInstallResult>(
    "/api/v1/admin/system-options/install/photo-face",
    {},
    { timeout: 45 * 60 * 1000 },
  );
  return data;
}

export type DocTransInstallResult = {
  ok: boolean;
  message: string;
  doc_trans?: SystemOptionsDocTrans;
  engines?: DocTransEngineStatus[];
};

export async function testSystemOptionsDocTrans(docTrans?: SystemOptionsDocTrans) {
  const { data } = await api.post<DocTransTestResult>(
    "/api/v1/admin/system-options/test/doc-trans",
    docTrans ? { doc_trans: docTrans } : {},
    { timeout: 5 * 60 * 1000 },
  );
  return data;
}

export async function installSystemOptionsDocTrans() {
  const { data } = await api.post<DocTransInstallResult>(
    "/api/v1/admin/system-options/install/doc-trans",
    {},
    { timeout: 5 * 60 * 1000 },
  );
  return data;
}

export async function installLibreOfficeDocTrans() {
  const { data } = await api.post<DocTransInstallResult>(
    "/api/v1/admin/system-options/install/libreoffice",
    {},
    { timeout: 30 * 60 * 1000 },
  );
  return data;
}
