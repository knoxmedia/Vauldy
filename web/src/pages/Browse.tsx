import {
  AppstoreOutlined,
  ArrowDownOutlined,
  ArrowUpOutlined,
  BarsOutlined,
  CaretDownOutlined,
  CaretRightOutlined,
  CaretUpOutlined,
  CheckCircleOutlined,
  CheckOutlined,
  CloseOutlined,
  DownOutlined,
  EditOutlined,
  EllipsisOutlined,
  PictureOutlined,
  PlayCircleOutlined,
  SlidersOutlined,
  TableOutlined,
  UnorderedListOutlined,
  UpOutlined,
} from "@ant-design/icons";
import type { MenuProps } from "antd";
import { Button, Checkbox, Dropdown, Empty, Modal, Popover, Select, Space, Spin, Pagination, message } from "antd";
import type { ComponentType } from "react";
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { useNavigate, useSearchParams } from "react-router-dom";
import { formatServerDateTime, serverDateTimeToMillis } from "../lib/datetime";
import { buildMediaMenuItems } from "../components/mediaMenuItems";
import AddToFavoriteFolderPickerModal from "../components/AddToFavoriteFolderPickerModal";
import AddToPlaylistModal from "../components/AddToPlaylistModal";
import MediaMatchModal from "../components/MediaMatchModal";
import VideoOptimizationModal from "../components/VideoOptimizationModal";
import { useLicenseStatus } from "../enterprise";
import {
  MediaItem,
  PLAYLIST_PLAY_SESSION_KEY,
  addFavorite,
  addFavoriteFolderItem,
  addPlaylistItem,
  createScrapeTasks,
  deleteMedia,
  fetchLibraries,
  fetchMedia,
  isMusicLibraryType,
  isPhotoLibraryType,
  isDocumentLibraryType,
  mediaPosterSrc,
  normalizeListPosterUrl,
  removePlayProgress,
  transcodeAsync,
  unmatchMedia,
  type MediaMatchListUpdate,
  optimizationAssetRecorded,
} from "../api/client";
import SeriesBrowse from "./SeriesBrowse";
import MusicBrowse from "./MusicBrowse";
import PhotoBrowse from "./PhotoBrowse";
import DocumentBrowse from "./DocumentBrowse";
import { useFavoriteFolderMenuRecents } from "../lib/useFavoriteFolderMenuRecents";
import { MAX_RECENT_FAVORITE_FOLDERS } from "../lib/recentFavoriteFolders";
import { readRecentPlaylists, rememberPlaylistAdded } from "../lib/recentPlaylists";
import { useT, type TranslateFn } from "../i18n";
import styles from "./Browse.module.css";
import { useLibraryRequestScope } from "../lib/libraryRequestScope";

type ViewMode = "poster" | "thumb" | "list" | "table";
type SortField = "title" | "added" | "played" | "release_date" | "year" | "type" | "quality" | "bitrate" | "duration";
type SortOrder = "asc" | "desc";
type TableColKey = "title" | "year" | "release_date" | "duration" | "last_play" | "quality" | "bitrate" | "added" | "type";
type LibraryResolution = {
  status: "idle" | "loading" | "ready" | "error";
  type: string;
  name: string;
  libraryId?: number;
  generation: number;
  error?: string;
  signal?: AbortSignal;
};

const BROWSE_PREFS_KEY = "knox.browse.prefs.v1";
/** Per-library view mode (poster / thumb / list / table). */
const BROWSE_VIEW_MODE_KEY = "knox.browse.viewModeByLibrary.v1";
const TABLE_PAGE_SIZE = 20;
/** Session playlist id for multi-select play / shuffle from Browse. */
const BROWSE_BULK_PLAYLIST_ID = -1;

function shuffleMediaIds(ids: number[]): number[] {
  const arr = [...ids];
  for (let i = arr.length - 1; i > 0; i--) {
    const j = Math.floor(Math.random() * (i + 1));
    [arr[i], arr[j]] = [arr[j]!, arr[i]!];
  }
  return arr;
}

function browseLibraryKey(libraryId: number | undefined): string {
  return libraryId != null ? String(libraryId) : "_all";
}

function isViewMode(v: unknown): v is ViewMode {
  return v === "poster" || v === "thumb" || v === "list" || v === "table";
}

function readViewModeStore(): Record<string, ViewMode> {
  if (typeof window === "undefined") return {};
  try {
    const raw = window.localStorage.getItem(BROWSE_VIEW_MODE_KEY);
    if (!raw) return {};
    const parsed = JSON.parse(raw) as Record<string, unknown>;
    const out: Record<string, ViewMode> = {};
    for (const [k, v] of Object.entries(parsed)) {
      if (isViewMode(v)) out[k] = v;
    }
    return out;
  } catch {
    return {};
  }
}

function readBrowseViewMode(libraryId: number | undefined): ViewMode {
  const key = browseLibraryKey(libraryId);
  const stored = readViewModeStore()[key];
  if (stored) return stored;
  return readBrowsePrefs()?.viewMode ?? "table";
}

function writeBrowseViewMode(libraryId: number | undefined, mode: ViewMode): void {
  if (typeof window === "undefined") return;
  const store = readViewModeStore();
  store[browseLibraryKey(libraryId)] = mode;
  window.localStorage.setItem(BROWSE_VIEW_MODE_KEY, JSON.stringify(store));
}

type TableColSpec = { key: TableColKey; label: string; sortField: SortField; widthPx: number };

function buildTableColSpecs(t: TranslateFn): TableColSpec[] {
  return [
    { key: "title", label: t("pages.browse.col_title"), sortField: "title", widthPx: 0 },
    { key: "year", label: t("pages.browse.col_year"), sortField: "year", widthPx: 72 },
    { key: "release_date", label: t("pages.browse.col_release_date"), sortField: "release_date", widthPx: 118 },
    { key: "duration", label: t("pages.browse.col_duration"), sortField: "duration", widthPx: 112 },
    { key: "last_play", label: t("pages.browse.col_last_play"), sortField: "played", widthPx: 168 },
    { key: "quality", label: t("pages.browse.col_quality"), sortField: "quality", widthPx: 104 },
    { key: "bitrate", label: t("pages.browse.col_bitrate"), sortField: "bitrate", widthPx: 96 },
    { key: "added", label: t("pages.browse.col_added"), sortField: "added", widthPx: 168 },
    { key: "type", label: t("pages.browse.col_type"), sortField: "type", widthPx: 80 },
  ];
}

const TABLE_COL_KEYS: TableColKey[] = [
  "title", "year", "release_date", "duration", "last_play", "quality", "bitrate", "added", "type",
];

const DEFAULT_TABLE_VISIBLE: TableColKey[] = ["title", "year", "release_date", "duration", "last_play", "quality", "bitrate"];

function normalizeTableVisibleCols(raw: unknown): TableColKey[] {
  const valid = new Set(TABLE_COL_KEYS);
  if (!Array.isArray(raw)) return [...DEFAULT_TABLE_VISIBLE];
  const xs = raw.filter((k): k is TableColKey => typeof k === "string" && valid.has(k as TableColKey));
  const withTitle: TableColKey[] = xs.includes("title") ? xs : (["title", ...xs] as TableColKey[]);
  return withTitle.length ? withTitle : [...DEFAULT_TABLE_VISIBLE];
}

type ViewModeEntry = { value: ViewMode; label: string; Icon: ComponentType };

function buildViewModes(t: TranslateFn): ViewModeEntry[] {
  return [
    { value: "poster", label: t("pages.browse.view_poster"), Icon: PictureOutlined },
    { value: "thumb", label: t("pages.browse.view_thumb"), Icon: AppstoreOutlined },
    { value: "list", label: t("pages.browse.view_list"), Icon: BarsOutlined },
    { value: "table", label: t("pages.browse.view_table"), Icon: TableOutlined },
  ];
}

function fmtDurationLocalized(sec: number, t: TranslateFn): string {
  if (sec == null || Number.isNaN(sec) || sec <= 0) return "—";
  const total = Math.floor(sec);
  const h = Math.floor(total / 3600);
  const m = Math.floor((total % 3600) / 60);
  if (h > 0) return t("pages.browse.fmt_h_m", { h, m });
  if (m > 0) return t("pages.browse.fmt_m", { m });
  return t("pages.browse.fmt_s", { s: total % 60 });
}

function displayYear(r: MediaItem): string | number {
  if (r.year != null && r.year > 0) return r.year;
  const m = (r.title ?? "").match(/(19|20)\d{2}/) || (r.file_path ?? "").match(/(19|20)\d{2}/);
  return m ? Number(m[0]) : "—";
}

function readBrowsePrefs(): {
  viewMode?: ViewMode;
  sortField: SortField;
  sortOrder: SortOrder;
  tableVisibleCols?: TableColKey[];
} | null {
  if (typeof window === "undefined") return null;
  try {
    const raw = window.localStorage.getItem(BROWSE_PREFS_KEY);
    if (!raw) return null;
    const parsed = JSON.parse(raw) as {
      viewMode?: ViewMode;
      sortField?: SortField;
      sortOrder?: SortOrder;
      tableVisibleCols?: TableColKey[];
    };
    const viewMode: ViewMode | undefined = isViewMode(parsed.viewMode) ? parsed.viewMode : undefined;
    const sortField: SortField = [
      "title",
      "added",
      "played",
      "release_date",
      "year",
      "type",
      "quality",
      "bitrate",
      "duration",
    ].includes(String(parsed.sortField))
      ? (parsed.sortField as SortField)
      : "added";
    const sortOrder: SortOrder = parsed.sortOrder === "asc" || parsed.sortOrder === "desc" ? parsed.sortOrder : "desc";
    const tableVisibleCols = normalizeTableVisibleCols(parsed.tableVisibleCols);
    return { viewMode, sortField, sortOrder, tableVisibleCols: tableVisibleCols };
  } catch {
    return null;
  }
}

function isBrowseTVLibraryType(type?: string): boolean {
  const normalized = (type || "").trim().toLowerCase();
  return normalized === "tv" || normalized === "television" || normalized === "series";
}

function isGenericBrowseLibraryType(type?: string): boolean {
  const normalized = (type || "").trim().toLowerCase();
  return normalized === "movie" || normalized === "video" || normalized === "anime";
}

function isSupportedBrowseLibraryType(type?: string): boolean {
  return isBrowseTVLibraryType(type) || isMusicLibraryType(type) || isPhotoLibraryType(type) ||
    isDocumentLibraryType(type) || isGenericBrowseLibraryType(type);
}

export default function BrowsePage() {
  const t = useT();
  const VIEW_MODES = useMemo(() => buildViewModes(t), [t]);
  const TABLE_COL_SPECS = useMemo(() => buildTableColSpecs(t), [t]);
  const nav = useNavigate();
  const libraryRequests = useLibraryRequestScope();
  const [searchParams] = useSearchParams();
  const libraryIdParam = searchParams.get("library_id") ?? searchParams.get("library");
  const sortParam = searchParams.get("sort");
  const qParam = searchParams.get("q")?.trim() ?? "";
  const parsedLibraryId = libraryIdParam == null ? undefined : Number(libraryIdParam);
  const libraryParamInvalid =
    libraryIdParam != null &&
    (!Number.isInteger(parsedLibraryId) || (parsedLibraryId ?? 0) <= 0);
  const libFromUrl = libraryParamInvalid ? undefined : parsedLibraryId;

  const [rows, setRows] = useState<MediaItem[]>([]);
  const [loading, setLoading] = useState(false);
  const [encryptAssetsEnabled, setEncryptAssetsEnabled] = useState(true);
  const [viewMode, setViewMode] = useState<ViewMode>(() => readBrowseViewMode(libFromUrl));
  const [sortField, setSortField] = useState<SortField>(() => readBrowsePrefs()?.sortField ?? "added");
  const [sortOrder, setSortOrder] = useState<SortOrder>(() => readBrowsePrefs()?.sortOrder ?? "desc");
  const [viewModeMenuOpen, setViewModeMenuOpen] = useState(false);
  const [browseSelectedIds, setBrowseSelectedIds] = useState<Set<number>>(() => new Set());
  const [tableVisibleCols, setTableVisibleCols] = useState<TableColKey[]>(
    () => readBrowsePrefs()?.tableVisibleCols ?? [...DEFAULT_TABLE_VISIBLE]
  );
  const [tablePage, setTablePage] = useState(1);
  const [colPickerOpen, setColPickerOpen] = useState(false);
  const [playlistModalMediaIds, setPlaylistModalMediaIds] = useState<number[] | null>(null);
  const [addToFavoriteFolderMediaId, setAddToFavoriteFolderMediaId] = useState<number | null>(null);
  const [matchMedia, setMatchMedia] = useState<MediaItem | null>(null);
  const [optimizationModalMediaId, setOptimizationModalMediaId] = useState<number | null>(null);
  const [optimizationModalTitle, setOptimizationModalTitle] = useState<string | undefined>();
  const { status: licenseStatus } = useLicenseStatus();
  const [recentPlaylistMenu, setRecentPlaylistMenu] = useState(readRecentPlaylists);
  const { recentFavoriteFolders, rememberFolderMenuAdded } = useFavoriteFolderMenuRecents();
  const [resolution, setResolution] = useState<LibraryResolution>(() => ({
    status: libraryParamInvalid ? "error" : libFromUrl == null ? "idle" : "loading",
    type: "",
    name: "",
    libraryId: libFromUrl,
    generation: 0,
    error: libraryParamInvalid ? t("common.loading_failed") : undefined,
  }));
  const [resolutionRetry, setResolutionRetry] = useState(0);
  const [tvUseFlatFiles, setTvUseFlatFiles] = useState(false);
  const [photoUseFlatFiles, setPhotoUseFlatFiles] = useState(false);
  const resolutionGenerationRef = useRef(0);
  const currentLibraryRef = useRef(libFromUrl);
  const mediaControllerRef = useRef<AbortController | null>(null);
  const specializedControllerRef = useRef<AbortController | null>(null);
  const mediaGenerationRef = useRef(0);
  const currentRouteKeyRef = useRef("");
  currentLibraryRef.current = libFromUrl;
  const routeKey = `${libraryIdParam ?? "_all"}|${sortParam ?? ""}`;
  const activeRouteKey = `${routeKey}|${resolution.generation}`;
  currentRouteKeyRef.current = activeRouteKey;

  useEffect(() => {
    const generation = ++resolutionGenerationRef.current;
    specializedControllerRef.current?.abort();
    specializedControllerRef.current = null;
    mediaControllerRef.current?.abort();
    mediaGenerationRef.current++;
    setRows([]);
    setLoading(false);
    setBrowseSelectedIds(new Set());
    setTvUseFlatFiles(false);
    setPhotoUseFlatFiles(false);

    if (libraryParamInvalid) {
      setResolution({ status: "error", type: "", name: "", generation, error: t("common.loading_failed") });
      return;
    }
    if (libFromUrl == null) {
      setResolution({ status: "idle", type: "", name: "", generation });
      return;
    }

    const controller = new AbortController();
    setResolution({ status: "loading", type: "", name: "", libraryId: libFromUrl, generation });
    const request = libraryRequests
      ? libraryRequests.load(controller.signal).then((result) => {
          setEncryptAssetsEnabled(result.encryptedAssetsConfig?.enabled !== false);
          return result.items;
        })
      : fetchLibraries(controller.signal).then((items) => {
          setEncryptAssetsEnabled(true);
          return items;
        });
    void request
      .then((libs) => {
        if (controller.signal.aborted || resolutionGenerationRef.current !== generation) return;
        const lib = libs.find((item) => item.id === libFromUrl);
        if (!lib || !isSupportedBrowseLibraryType(lib.type)) throw new Error(t("common.loading_failed"));
        const specializedController = new AbortController();
        specializedControllerRef.current?.abort();
        specializedControllerRef.current = specializedController;
        setResolution({ status: "ready", type: lib.type.trim().toLowerCase(), name: lib.name || "", libraryId: libFromUrl, generation, signal: specializedController.signal });
      })
      .catch((error: unknown) => {
        if (controller.signal.aborted || resolutionGenerationRef.current !== generation) return;
        setResolution({
          status: "error", type: "", name: "", libraryId: libFromUrl, generation,
          error: (error as Error).message || t("common.loading_failed"),
        });
      });
    return () => {
      controller.abort();
      specializedControllerRef.current?.abort();
      specializedControllerRef.current = null;
    };
  }, [libFromUrl, libraryParamInvalid, resolutionRetry, t, libraryRequests]);

  const acceptsEmpty = useCallback((kind: "tv" | "photo") => {
    if (resolution.status !== "ready" || resolution.libraryId !== currentLibraryRef.current || resolution.generation !== resolutionGenerationRef.current) return false;
    if (kind === "tv") return isBrowseTVLibraryType(resolution.type);
    return isPhotoLibraryType(resolution.type);
  }, [resolution]);

  const handleTvBrowseEmpty = useCallback(() => { if (acceptsEmpty("tv")) setTvUseFlatFiles(true); }, [acceptsEmpty]);
  const handlePhotoBrowseEmpty = useCallback(() => { if (acceptsEmpty("photo")) setPhotoUseFlatFiles(true); }, [acceptsEmpty]);

  const genericRouteReady =
    (!libraryParamInvalid && libFromUrl == null) ||
    (resolution.status === "ready" && resolution.libraryId === libFromUrl &&
      (isGenericBrowseLibraryType(resolution.type) ||
        (isBrowseTVLibraryType(resolution.type) && tvUseFlatFiles) ||
        (isPhotoLibraryType(resolution.type) && photoUseFlatFiles)));

  const load = useCallback(async (expectedRouteKey = activeRouteKey) => {
    if (!genericRouteReady || currentRouteKeyRef.current !== expectedRouteKey) return;
    mediaControllerRef.current?.abort();
    const controller = new AbortController();
    mediaControllerRef.current = controller;
    const generation = ++mediaGenerationRef.current;
    setRows([]);
    setLoading(true);
    try {
      const opts = sortParam === "recent" ? ({ sort: "created_desc" as const, limit: 200 }) : undefined;
      const items = await fetchMedia(libFromUrl, opts, controller.signal);
      if (controller.signal.aborted || mediaGenerationRef.current !== generation || currentRouteKeyRef.current !== expectedRouteKey) return;
      setRows(items);
    } catch (error: unknown) {
      if (controller.signal.aborted || mediaGenerationRef.current !== generation || currentRouteKeyRef.current !== expectedRouteKey) return;
      message.error((error as Error).message || t("pages.browse.load_failed"));
    } finally {
      if (!controller.signal.aborted && mediaGenerationRef.current === generation && currentRouteKeyRef.current === expectedRouteKey) setLoading(false);
    }
  }, [activeRouteKey, genericRouteReady, libFromUrl, sortParam, t]);

  useEffect(() => {
    if (!genericRouteReady) return;
    const expectedRouteKey = activeRouteKey;
    const timer = window.setTimeout(() => { void load(expectedRouteKey); }, 0);
    return () => {
      window.clearTimeout(timer);
      mediaControllerRef.current?.abort();
      mediaGenerationRef.current++;
    };
  }, [activeRouteKey, genericRouteReady, load]);

  function applyMediaMatchUpdate(update: MediaMatchListUpdate, expectedRouteKey = activeRouteKey) {
    if (currentRouteKeyRef.current !== expectedRouteKey) return;
    setRows((prev) => prev.map((row) => row.id === update.id ? {
      ...row,
      title: update.title || row.title,
      poster_url: update.poster_url ?? row.poster_url,
      year: update.year ?? row.year,
      release_date: update.release_date ?? row.release_date,
      scraped: update.scraped,
    } : row));
  }

  function posterImgKey(row: MediaItem): string {
    return `${row.id}:${normalizeListPosterUrl(row.poster_url || "")}`;
  }

  useEffect(() => {
    if (sortParam === "recent") {
      setSortField("added");
      setSortOrder("desc");
    }
  }, [sortParam]);

  useEffect(() => {
    setViewMode(readBrowseViewMode(libFromUrl));
  }, [libFromUrl]);

  useEffect(() => {
    writeBrowseViewMode(libFromUrl, viewMode);
    // Persist only when viewMode changes; library switch restores via the effect above.
    // eslint-disable-next-line react-hooks/exhaustive-deps -- libFromUrl is read from the render that changed viewMode
  }, [viewMode]);

  useEffect(() => {
    if (typeof window === "undefined") return;
    window.localStorage.setItem(
      BROWSE_PREFS_KEY,
      JSON.stringify({
        sortField,
        sortOrder,
        tableVisibleCols,
      })
    );
  }, [sortField, sortOrder, tableVisibleCols]);

  const displayRows = useMemo<MediaItem[]>(() => {
    if (!qParam) return rows;
    const q = qParam.toLowerCase();
    return rows.filter((r) => (r.title ?? "").toLowerCase().includes(q));
  }, [rows, qParam]);

  const sortedRows = useMemo<MediaItem[]>(() => {
    const list = [...displayRows];
    const factor = sortOrder === "asc" ? 1 : -1;

    const timeVal = (v?: string) => serverDateTimeToMillis(v);
    const yearVal = (r: MediaItem) => {
      if ((r.year ?? 0) > 0) return r.year ?? 0;
      const m = (r.title ?? "").match(/(19|20)\d{2}/) || (r.file_path ?? "").match(/(19|20)\d{2}/);
      return m ? Number(m[0]) : 0;
    };
    const qualityVal = (r: MediaItem) => Math.max(r.width ?? 0, r.height ?? 0);

    list.sort((a, b) => {
      switch (sortField) {
        case "title":
          return (a.title ?? "").localeCompare(b.title ?? "", "zh-CN") * factor;
        case "added":
          return (timeVal(a.created_at) - timeVal(b.created_at)) * factor;
        case "played":
          return (timeVal(a.last_play_at) - timeVal(b.last_play_at)) * factor;
        case "release_date":
          return (timeVal(a.release_date) - timeVal(b.release_date)) * factor;
        case "year":
          return (yearVal(a) - yearVal(b)) * factor;
        case "type":
          return (a.file_type ?? "").localeCompare(b.file_type ?? "", "zh-CN") * factor;
        case "quality":
          return (qualityVal(a) - qualityVal(b)) * factor;
        case "bitrate":
          return ((a.bitrate ?? 0) - (b.bitrate ?? 0)) * factor;
        case "duration":
          return ((a.duration ?? 0) - (b.duration ?? 0)) * factor;
        default:
          return 0;
      }
    });
    return list;
  }, [displayRows, sortField, sortOrder]);

  const fmtDate = (v?: string) => formatServerDateTime(v);
  const fmtReleaseDate = (v?: string) => {
    if (!v) return "—";
    const d = v.slice(0, 10);
    return d || "—";
  };
  const fmtResolution = (r: MediaItem) => (r.width && r.height ? `${r.width}x${r.height}` : "—");
  const fmtBitrateMbps = (v?: number) => {
    if (v == null || v <= 0) return "—";
    const mbps = v / 1_000_000;
    return mbps >= 0.1 ? `${mbps.toFixed(1)} Mbps` : `${Math.round(v / 1000)} kbps`;
  };

  const tableOrderedSpecs = useMemo(() => {
    const vis = new Set(tableVisibleCols);
    const picked = TABLE_COL_SPECS.filter((s) => vis.has(s.key));
    const title = picked.find((s) => s.key === "title");
    const rest = picked.filter((s) => s.key !== "title");
    return title ? [title, ...rest] : picked;
  }, [tableVisibleCols]);

  /** 表头与数据行共用同一列宽，保证对齐 */
  const tableGridTemplate = useMemo(() => {
    const parts: string[] = ["40px"];
    for (const spec of tableOrderedSpecs) {
      parts.push(spec.widthPx ? `${spec.widthPx}px` : "minmax(160px, 1fr)");
    }
    parts.push("40px");
    return parts.join(" ");
  }, [tableOrderedSpecs]);

  const pagedTableRows = useMemo(() => {
    const start = (tablePage - 1) * TABLE_PAGE_SIZE;
    return sortedRows.slice(start, start + TABLE_PAGE_SIZE);
  }, [sortedRows, tablePage]);

  useEffect(() => {
    setTablePage(1);
  }, [sortedRows.length, viewMode, qParam, libFromUrl]);

  function toggleTableCol(key: TableColKey) {
    if (key === "title") return;
    setTableVisibleCols((prev) => {
      const has = prev.includes(key);
      if (has) {
        const next = prev.filter((k) => k !== key);
        return next.includes("title") ? next : ["title", ...next];
      }
      return [...prev, key];
    });
  }

  function onTableHeaderSort(field: SortField) {
    if (sortField === field) setSortOrder((o) => (o === "asc" ? "desc" : "asc"));
    else {
      setSortField(field);
      setSortOrder("desc");
    }
  }

  function renderTableCell(r: MediaItem, key: TableColKey): string {
    switch (key) {
      case "title":
        return r.title || t("pages.browse.untitled");
      case "year":
        return String(displayYear(r));
      case "release_date":
        return fmtReleaseDate(r.release_date);
      case "duration":
        return fmtDurationLocalized(r.duration, t);
      case "last_play":
        return fmtDate(r.last_play_at);
      case "quality":
        return fmtResolution(r);
      case "bitrate":
        return fmtBitrateMbps(r.bitrate);
      case "added":
        return fmtDate(r.created_at);
      case "type":
        return r.file_type || "—";
      default:
        return "—";
    }
  }

  const viewModeMenuItems: MenuProps["items"] = VIEW_MODES.map(({ value, label, Icon }) => ({
    key: value,
    icon: <Icon />,
    label,
  }));

  const CurrentViewIcon = VIEW_MODES.find((m) => m.value === viewMode)?.Icon ?? TableOutlined;
  const currentViewLabel = VIEW_MODES.find((m) => m.value === viewMode)?.label ?? t("pages.browse.table_fallback");

  function toggleBrowseSelect(id: number) {
    setBrowseSelectedIds((prev) => {
      const next = new Set(prev);
      if (next.has(id)) next.delete(id);
      else next.add(id);
      return next;
    });
  }

  function clearBrowseSelection() {
    setBrowseSelectedIds(new Set());
  }

  const firstSelectedId = useMemo(() => {
    for (const r of sortedRows) {
      if (browseSelectedIds.has(r.id)) return r.id;
    }
    const [x] = browseSelectedIds;
    return x;
  }, [sortedRows, browseSelectedIds]);

  const browseSelectedIdList = useMemo(
    () => sortedRows.filter((r) => browseSelectedIds.has(r.id)).map((r) => r.id),
    [sortedRows, browseSelectedIds],
  );

  const browseSelectionCount = browseSelectedIds.size;
  /** 任意项已选中时：隐藏播放/编辑/更多，海报区点击切换选中 */
  const browseBulkPick = browseSelectionCount > 0;

  const playlistModalDefaultTitle = useMemo(() => {
    if (playlistModalMediaIds == null || playlistModalMediaIds.length !== 1) return "";
    const id = playlistModalMediaIds[0];
    return rows.find((x) => x.id === id)?.title ?? "";
  }, [playlistModalMediaIds, rows]);

  async function bulkAddSelectedToCollection(ids: number[]) {
    if (ids.length === 0) return;
    let ok = 0;
    let fail = 0;
    for (const id of ids) {
      try {
        await addFavorite(id);
        ok++;
      } catch {
        fail++;
      }
    }
    if (ok > 0) {
      message.success(
        fail > 0
          ? t("pages.browse.added_to_favorites_with_skip", { ok, fail })
          : t("pages.browse.added_to_favorites", { ok })
      );
    } else {
      message.warning(t("pages.browse.favorite_failed"));
    }
  }

  async function bulkAddSelectedToPlaylist(ids: number[], playlistId: number) {
    if (ids.length === 0) return;
    let ok = 0;
    let fail = 0;
    for (const mid of ids) {
      try {
        await addPlaylistItem(playlistId, mid);
        ok++;
      } catch {
        fail++;
      }
    }
    const name =
      recentPlaylistMenu.find((p) => p.id === playlistId)?.name ??
      readRecentPlaylists().find((p) => p.id === playlistId)?.name ??
      t("pages.browse.playlist_fallback");
    if (ok > 0) {
      rememberPlaylistAdded({ id: playlistId, name });
      setRecentPlaylistMenu(readRecentPlaylists());
      message.success(
        fail > 0
          ? t("pages.browse.added_to_playlist_with_skip", { ok, name, fail })
          : t("pages.browse.added_to_playlist", { ok, name })
      );
    } else {
      message.warning(t("pages.browse.playlist_add_failed"));
    }
  }

  async function bulkAddSelectedToFavoriteFolder(ids: number[], folderId: number) {
    if (ids.length === 0) return;
    let ok = 0;
    let fail = 0;
    for (const mid of ids) {
      try {
        await addFavoriteFolderItem(folderId, mid);
        ok++;
      } catch {
        fail++;
      }
    }
    const name =
      recentFavoriteFolders.find((f) => f.id === folderId)?.name ??
      t("components.media_menu.favorite_folder_fallback");
    if (ok > 0) {
      rememberFolderMenuAdded({ id: folderId, name });
      message.success(
        fail > 0
          ? t("pages.browse.added_to_favorite_folder_with_skip", { ok, name, fail })
          : t("pages.browse.added_to_favorite_folder", { ok, name })
      );
    } else {
      message.warning(t("pages.browse.favorite_folder_add_failed"));
    }
  }

  const browseBulkAddMenuItems = useMemo((): MenuProps["items"] => {
    const items: MenuProps["items"] = [
      { key: "bulkAddCollection", label: t("pages.browse.bulk_add_collection") },
      { type: "divider" },
    ];
    if (recentFavoriteFolders.length > 0) {
      items.push({
        type: "group",
        label: t("components.media_menu.recent_favorite_folders"),
        children: recentFavoriteFolders.slice(0, MAX_RECENT_FAVORITE_FOLDERS).map((folder) => ({
          key: `recentFavoriteFolder:${folder.id}`,
          label: folder.name,
        })),
      });
      items.push({ type: "divider" });
    }
    items.push({ key: "bulkOpenPlaylist", label: t("pages.browse.bulk_open_playlist") });
    if (recentPlaylistMenu.length > 0) {
      items.push({
        type: "group",
        label: t("pages.browse.bulk_recent"),
        children: recentPlaylistMenu.slice(0, 3).map((pl) => ({
          key: `recentPlaylist:${pl.id}`,
          label: pl.name,
        })),
      });
    }
    return items;
  }, [recentPlaylistMenu, recentFavoriteFolders, t]);

  function onBrowseBulkAddMenuClick(key: string) {
    const ids = [...browseSelectedIds];
    if (ids.length === 0) return;
    const sk = String(key);
    if (sk === "bulkAddCollection") {
      void bulkAddSelectedToCollection(ids);
      return;
    }
    if (sk.startsWith("recentFavoriteFolder:")) {
      const fid = Number(sk.slice("recentFavoriteFolder:".length));
      if (!Number.isNaN(fid)) void bulkAddSelectedToFavoriteFolder(ids, fid);
      return;
    }
    if (sk === "bulkOpenPlaylist") {
      setPlaylistModalMediaIds(ids);
      return;
    }
    if (sk.startsWith("recentPlaylist:")) {
      const pid = Number(sk.slice("recentPlaylist:".length));
      if (!Number.isNaN(pid)) void bulkAddSelectedToPlaylist(ids, pid);
    }
  }

  function startBrowseBulkPlayback(ids: number[], mode: "ordered" | "shuffle") {
    if (ids.length === 0) return;
    const order = mode === "shuffle" ? shuffleMediaIds(ids) : [...ids];
    sessionStorage.setItem(
      PLAYLIST_PLAY_SESSION_KEY,
      JSON.stringify({ playlistId: BROWSE_BULK_PLAYLIST_ID, order, mode }),
    );
    nav(`/player/${order[0]}?playlist_id=${BROWSE_BULK_PLAYLIST_ID}&index=0`);
  }

  async function bulkRemoveSelectedFromContinue(ids: number[]) {
    if (ids.length === 0) return;
    let ok = 0;
    for (const id of ids) {
      try {
        await removePlayProgress(id);
        ok++;
      } catch {
        /* skip items without progress */
      }
    }
    if (ok > 0) message.success(t("pages.home.removed_from_continue"));
    else message.warning(t("pages.browse.remove_continue_none"));
  }

  async function bulkRefreshSelectedMetadata(ids: number[]) {
    if (ids.length === 0) return;
    try {
      await createScrapeTasks(ids);
      message.success(t("components.media_menu.scrape_task_created"));
    } catch {
      message.error(t("components.media_menu.operation_failed"));
    }
  }

  async function bulkAnalyzeSelected(ids: number[]) {
    if (ids.length === 0) return;
    let ok = 0;
    let fail = 0;
    for (const id of ids) {
      try {
        await transcodeAsync(id, "analyze");
        ok++;
      } catch {
        fail++;
      }
    }
    if (ok > 0) {
      message.success(
        fail > 0
          ? t("pages.browse.analyze_with_skip", { ok, fail })
          : t("components.media_menu.analyze_task_created"),
      );
    } else {
      message.error(t("components.media_menu.operation_failed"));
    }
  }

  function bulkDeleteSelected(ids: number[]) {
    if (ids.length === 0) return;
    const mutationRouteKey = activeRouteKey;
    Modal.confirm({
      title: t("pages.browse.bulk_delete_title", { count: ids.length }),
      centered: true,
      okText: t("components.media_menu.ok"),
      cancelText: t("components.media_menu.cancel"),
      okButtonProps: { danger: true },
      content: t("pages.browse.bulk_delete_confirm"),
      onOk: async () => {
        let ok = 0;
        let fail = 0;
        for (const id of ids) {
          try {
            await deleteMedia(id);
            ok++;
          } catch {
            fail++;
          }
        }
        if (currentRouteKeyRef.current !== mutationRouteKey) return;
        setBrowseSelectedIds(new Set());
        await load(mutationRouteKey);
        if (currentRouteKeyRef.current !== mutationRouteKey) return;
        if (ok > 0) {
          message.success(
            fail > 0
              ? t("pages.browse.bulk_deleted_with_skip", { ok, fail })
              : t("pages.browse.bulk_deleted", { ok }),
          );
        } else {
          message.error(t("components.media_menu.delete_failed"));
        }
      },
    });
  }

  function bulkUnmatchSelected(ids: number[]) {
    const mutationRouteKey = activeRouteKey;
    const scrapedIds = ids.filter((id) => rows.some((r) => r.id === id && r.scraped));
    if (scrapedIds.length === 0) return;
    Modal.confirm({
      title: t("components.media_menu.unmatch_modal_title"),
      centered: true,
      okText: t("components.media_menu.ok"),
      cancelText: t("components.media_menu.cancel"),
      content: t("components.media_menu.unmatch_modal_content"),
      onOk: async () => {
        let ok = 0;
        let fail = 0;
        for (const id of scrapedIds) {
          try {
            await unmatchMedia(id);
            ok++;
          } catch {
            fail++;
          }
        }
        if (currentRouteKeyRef.current !== mutationRouteKey) return;
        await load(mutationRouteKey);
        if (currentRouteKeyRef.current !== mutationRouteKey) return;
        if (ok > 0) {
          message.success(
            fail > 0
              ? t("pages.browse.analyze_with_skip", { ok, fail })
              : t("components.media_menu.unmatched"),
          );
        } else {
          message.error(t("components.series_menu.unmatch_failed"));
        }
      },
    });
  }

  const browseBulkMoreMenuItems = useMemo((): MenuProps["items"] => {
    const hasScrapedSelected = browseSelectedIdList.some((id) =>
      rows.some((r) => r.id === id && r.scraped),
    );
    return [
      { key: "play", label: t("pages.playlists.menu_play_next") },
      { key: "shuffle", label: t("pages.playlists.btn_shuffle") },
      { key: "removeFromContinue", label: t("components.media_menu.remove_from_continue") },
      { type: "divider" },
      { key: "unmatch", label: t("components.media_menu.unmatch"), disabled: !hasScrapedSelected },
      { key: "refreshMetadata", label: t("components.media_menu.refresh_metadata") },
      { key: "analyze", label: t("components.media_menu.analyze") },
      { type: "divider" },
      { key: "merge", label: t("components.media_menu.merge") },
      { key: "delete", label: t("components.media_menu.delete"), danger: true },
    ];
  }, [t, browseSelectedIdList, rows]);

  function onBrowseBulkMoreMenuClick(key: string) {
    const ids = browseSelectedIdList;
    if (ids.length === 0) return;
    switch (key) {
      case "play":
        startBrowseBulkPlayback(ids, "ordered");
        break;
      case "shuffle":
        startBrowseBulkPlayback(ids, "shuffle");
        break;
      case "removeFromContinue":
        void bulkRemoveSelectedFromContinue(ids);
        break;
      case "unmatch":
        bulkUnmatchSelected(ids);
        break;
      case "refreshMetadata":
        void bulkRefreshSelectedMetadata(ids);
        break;
      case "analyze":
        void bulkAnalyzeSelected(ids);
        break;
      case "merge":
        message.info(t("pages.browse.merge_wip"));
        break;
      case "delete":
        bulkDeleteSelected(ids);
        break;
      default:
        break;
    }
  }

  function makeMenu(r: MediaItem, extra?: { isWatched?: boolean }): MenuProps {
    const menuRouteKey = activeRouteKey;
    const isWatched = extra?.isWatched ?? r.completed === 1;
    return buildMediaMenuItems(r, nav, {
      ...extra,
      isWatched,
      showEncryptAsset: encryptAssetsEnabled && !r.encrypted_asset,
      encryptedAsset: !!r.encrypted_asset,
      afterEncryptAsset: () => load(menuRouteKey),
      afterToggleWatched: () => load(menuRouteKey),
      scraped: r.scraped,
      onOpenMatch: (mediaId) => {
        const item = rows.find((x) => x.id === mediaId) ?? r;
        setMatchMedia(item);
      },
      afterUnmatch: () => load(menuRouteKey),
      afterDelete: () => {
        if (currentRouteKeyRef.current !== menuRouteKey) return Promise.resolve();
        setBrowseSelectedIds((prev) => {
          if (!prev.has(r.id)) return prev;
          const next = new Set(prev);
          next.delete(r.id);
          return next;
        });
        return load(menuRouteKey);
      },
      onAddToPlaylist: (mediaId: number) => setPlaylistModalMediaIds([mediaId]),
      recentPlaylists: recentPlaylistMenu,
      onQuickAddToPlaylist: async (mediaId: number, playlistId: number) => {
        try {
          await addPlaylistItem(playlistId, mediaId);
          const name =
            recentPlaylistMenu.find((p) => p.id === playlistId)?.name ??
            readRecentPlaylists().find((p) => p.id === playlistId)?.name ??
            t("pages.browse.playlist_fallback");
          message.success(t("pages.browse.single_added_to_playlist", { name }));
          rememberPlaylistAdded({ id: playlistId, name });
          setRecentPlaylistMenu(readRecentPlaylists());
        } catch {
          message.error(t("pages.browse.single_add_failed"));
        }
      },
      onAddToFavoriteFolder: (mediaId: number) => setAddToFavoriteFolderMediaId(mediaId),
      onOpenOptimization: licenseStatus?.pretranscode
        ? (mediaId: number) => {
            setOptimizationModalMediaId(mediaId);
            setOptimizationModalTitle(undefined);
          }
        : undefined,
      optimizationAvailable: r.file_type === "video" && optimizationAssetRecorded(r),
      recentFavoriteFolders,
      onQuickAddToFavoriteFolder: async (mediaId: number, folderId: number) => {
        try {
          await addFavoriteFolderItem(folderId, mediaId);
          const name =
            recentFavoriteFolders.find((f) => f.id === folderId)?.name ??
            t("components.media_menu.favorite_folder_fallback");
          message.success(t("components.add_to_favorite_folder_picker_modal.added_single", { name }));
          rememberFolderMenuAdded({ id: folderId, name });
        } catch {
          message.error(t("components.add_to_favorite_folder_picker_modal.add_failed_dup"));
        }
      },
    });
  }

  const currentResolution =
    resolution.libraryId === libFromUrl &&
    (libraryParamInvalid || resolution.generation === resolutionGenerationRef.current);
  const currentResolutionReady = libFromUrl != null && currentResolution && resolution.status === "ready";
  if (!libraryParamInvalid && libFromUrl != null && !currentResolutionReady && !(currentResolution && resolution.status === "error")) {
    return (
      <div className={styles.loadingWrap}>
        <Spin />
      </div>
    );
  }
  if ((libraryParamInvalid || libFromUrl != null) && currentResolution && resolution.status === "error") {
    return (
      <div className={styles.loadingWrap} role="alert">
        <Space direction="vertical">
          <span>{resolution.error || t("common.loading_failed")}</span>
          <Button onClick={() => setResolutionRetry((value) => value + 1)}>{t("common.retry")}</Button>
        </Space>
      </div>
    );
  }
  const libraryType = resolution.type;
  const libraryName = resolution.name;
  if (currentResolutionReady && isBrowseTVLibraryType(libraryType) && !tvUseFlatFiles) {
    return (
      <SeriesBrowse
        libraryId={libFromUrl}
        libraryName={libraryName}
        onEmpty={handleTvBrowseEmpty}
        signal={resolution.signal}
      />
    );
  }
  if (currentResolutionReady && isMusicLibraryType(libraryType)) {
    return (
      <MusicBrowse
        libraryId={libFromUrl}
        libraryName={libraryName}
        signal={resolution.signal}
      />
    );
  }
  if (currentResolutionReady && isPhotoLibraryType(libraryType) && !photoUseFlatFiles) {
    return (
      <PhotoBrowse
        libraryId={libFromUrl}
        libraryName={libraryName}
        onEmpty={handlePhotoBrowseEmpty}
        signal={resolution.signal}
      />
    );
  }
  if (currentResolutionReady && isDocumentLibraryType(libraryType)) {
    return (
      <DocumentBrowse
        libraryId={libFromUrl}
        libraryName={libraryName}
        signal={resolution.signal}
      />
    );
  }

  return (
    <div style={{ padding: "16px 0 32px", ["--browse-sticky-pad-top" as string]: "16px" }}>
      <div className={styles.browsePageStickyHead}>
        <div className={styles.topBar}>
        <Space wrap className={styles.topLeftTools}>
          {viewMode !== "table" && (
            <>
              <Select<SortField>
                size="small"
                value={sortField}
                onChange={setSortField}
                options={[
                  { value: "title", label: t("pages.browse.sort_title") },
                  { value: "added", label: t("pages.browse.sort_added") },
                  { value: "played", label: t("pages.browse.sort_played") },
                  { value: "release_date", label: t("pages.browse.sort_release_date") },
                  { value: "year", label: t("pages.browse.sort_year") },
                  { value: "duration", label: t("pages.browse.sort_duration") },
                  { value: "type", label: t("pages.browse.sort_type") },
                  { value: "quality", label: t("pages.browse.sort_quality") },
                  { value: "bitrate", label: t("pages.browse.sort_bitrate") },
                ]}
                style={{ width: 150 }}
              />
              <Button size="small" onClick={() => setSortOrder((s) => (s === "asc" ? "desc" : "asc"))}>
                {sortOrder === "asc" ? <ArrowUpOutlined /> : <ArrowDownOutlined />}
              </Button>
            </>
          )}
        </Space>
        <Space wrap className={styles.topRightTools}>
          <div className={styles.viewModePicker}>
            <span className={styles.viewModeCurrentIcon} title={currentViewLabel} aria-label={currentViewLabel}>
              <CurrentViewIcon />
            </span>
            <Dropdown
              open={viewModeMenuOpen}
              onOpenChange={setViewModeMenuOpen}
              menu={{
                items: viewModeMenuItems,
                selectedKeys: [viewMode],
                onClick: ({ key }) => {
                  setViewMode(key as ViewMode);
                  setViewModeMenuOpen(false);
                },
              }}
              trigger={["click"]}
              placement="bottomRight"
            >
              <Button
                type="text"
                size="small"
                icon={viewModeMenuOpen ? <UpOutlined /> : <DownOutlined />}
                aria-label={t("pages.browse.view_mode_aria")}
                aria-expanded={viewModeMenuOpen}
              />
            </Dropdown>
          </div>
        </Space>
      </div>

      {browseSelectionCount > 0 && (
        <div className={styles.browseSelectionBar}>
          <div className={styles.browseSelectionBarLeft}>
            <CheckOutlined className={styles.browseSelectionCheckIcon} aria-hidden />
            <span>{t("pages.browse.selection_count", { count: browseSelectionCount })}</span>
          </div>
          <div className={styles.browseSelectionBarCenter}>
            <Space size="middle">
              <Button
                type="text"
                className={styles.browseSelectionActionBtn}
                icon={<PlayCircleOutlined />}
                aria-label={t("pages.browse.selection_play_aria")}
                disabled={firstSelectedId == null}
                onClick={() => {
                  if (firstSelectedId != null) nav(`/player/${firstSelectedId}`);
                }}
              />
              <Button
                type="text"
                className={styles.browseSelectionActionBtn}
                icon={<CheckCircleOutlined />}
                aria-label={t("pages.browse.selection_mark_aria")}
                onClick={() => message.info(t("pages.browse.mark_wip"))}
              />
              <Dropdown
                menu={{
                  items: browseBulkAddMenuItems,
                  onClick: ({ key, domEvent }) => {
                    domEvent.stopPropagation();
                    onBrowseBulkAddMenuClick(String(key));
                  },
                }}
                trigger={["click"]}
                placement="bottom"
              >
                <Button
                  type="text"
                  className={styles.browseSelectionActionBtn}
                  icon={<UnorderedListOutlined />}
                  aria-label={t("pages.browse.selection_add_aria")}
                  onClick={(e) => e.stopPropagation()}
                />
              </Dropdown>
              <Dropdown
                menu={{
                  items: browseBulkMoreMenuItems,
                  onClick: ({ key, domEvent }) => {
                    domEvent.stopPropagation();
                    onBrowseBulkMoreMenuClick(String(key));
                  },
                }}
                trigger={["click"]}
                placement="bottom"
              >
                <Button type="text" className={styles.browseSelectionActionBtn} icon={<EllipsisOutlined />} aria-label={t("pages.browse.selection_more_aria")} />
              </Dropdown>
            </Space>
          </div>
          <div className={styles.browseSelectionBarRight}>
            <Button
              type="text"
              className={styles.browseSelectionClearBtn}
              icon={<CloseOutlined />}
              onClick={clearBrowseSelection}
            >
              {t("pages.browse.deselect_all")}
            </Button>
          </div>
        </div>
      )}

      </div>

      {loading ? (
        <div className={styles.loadingWrap}>
          <Spin />
        </div>
      ) : sortedRows.length === 0 ? (
        <Empty description={t("pages.browse.empty_media")} />
      ) : viewMode === "table" ? (
        <div className={styles.browseTableWrap}>
          <div className={styles.browseTableHead}>
            <div className={styles.browseTableHeadRow} style={{ gridTemplateColumns: tableGridTemplate }}>
              <div className={styles.browseThGutter}>
                <Popover
                  open={colPickerOpen}
                  onOpenChange={setColPickerOpen}
                  trigger="click"
                  placement="bottomLeft"
                  classNames={{ root: styles.browseColPickerOverlay }}
                  content={
                    <div className={styles.browseColPickerList}>
                      {TABLE_COL_SPECS.map((s) => {
                        const checked = tableVisibleCols.includes(s.key);
                        return (
                          <label key={s.key} className={styles.browseColPickerRow}>
                            <Checkbox
                              checked={checked}
                              disabled={s.key === "title"}
                              onChange={() => toggleTableCol(s.key)}
                            />
                            <span className={checked ? styles.browseColPickerActive : styles.browseColPickerMuted}>{s.label}</span>
                          </label>
                        );
                      })}
                    </div>
                  }
                >
                  <Button type="text" size="small" icon={<SlidersOutlined />} aria-label={t("pages.browse.col_picker_aria")} className={styles.browseColPickerTrigger} />
                </Popover>
              </div>
              {tableOrderedSpecs.map((spec) => (
                <div
                  key={spec.key}
                  role="button"
                  tabIndex={0}
                  className={styles.browseTh}
                  onClick={() => onTableHeaderSort(spec.sortField)}
                  onKeyDown={(e) => {
                    if (e.key === "Enter" || e.key === " ") {
                      e.preventDefault();
                      onTableHeaderSort(spec.sortField);
                    }
                  }}
                >
                  <span>{spec.label}</span>
                  {sortField === spec.sortField && (
                    <span className={styles.browseSortIcon}>
                      {sortOrder === "asc" ? <CaretUpOutlined /> : <CaretDownOutlined />}
                    </span>
                  )}
                </div>
              ))}
              <div className={styles.browseThActions} aria-hidden />
            </div>
          </div>
          <div className={styles.browseTableBody}>
            {pagedTableRows.map((r, idx) => {
              const globalIdx = (tablePage - 1) * TABLE_PAGE_SIZE + idx;
              const isSel = browseSelectedIds.has(r.id);
              return (
                <div
                  key={r.id}
                  className={styles.browseTr}
                  style={{ gridTemplateColumns: tableGridTemplate }}
                  data-selected={isSel ? "" : undefined}
                  data-stripe={globalIdx % 2 === 1 ? "" : undefined}
                  data-bulk-pick={browseBulkPick ? "" : undefined}
                  onClick={(e) => {
                    if ((e.target as HTMLElement).closest("[data-browse-table-action]")) return;
                    if ((e.target as HTMLElement).closest("[data-browse-table-detail]")) return;
                    if (browseBulkPick) {
                      toggleBrowseSelect(r.id);
                      return;
                    }
                    nav(`/detail/${r.id}`);
                  }}
                >
                  <div className={styles.browseTdGutter}>
                    <button
                      type="button"
                      className={styles.browseGutterSelect}
                      data-browse-table-action
                      aria-label={isSel ? t("pages.browse.aria_deselect") : t("pages.browse.aria_select")}
                      data-selected={isSel ? "" : undefined}
                      onClick={(e) => {
                        e.stopPropagation();
                        toggleBrowseSelect(r.id);
                      }}
                    >
                      {isSel ? <CheckOutlined /> : null}
                    </button>
                  </div>
                  {tableOrderedSpecs.map((spec) =>
                    spec.key === "title" ? (
                      <div key={spec.key} className={styles.browseTdTitle}>
                        {!browseBulkPick ? (
                          <button
                            type="button"
                            className={styles.browseRowPlay}
                            data-browse-table-action
                            aria-label={t("pages.browse.aria_play")}
                            onClick={(e) => {
                              e.stopPropagation();
                              nav(`/player/${r.id}`);
                            }}
                          >
                            <CaretRightOutlined />
                          </button>
                        ) : null}
                        <span
                          className={styles.browseTitleText}
                          data-browse-table-detail={browseBulkPick ? "" : undefined}
                          role={browseBulkPick ? "link" : undefined}
                          tabIndex={browseBulkPick ? 0 : undefined}
                          onClick={
                            browseBulkPick
                              ? (e) => {
                                  e.stopPropagation();
                                  nav(`/detail/${r.id}`);
                                }
                              : undefined
                          }
                          onKeyDown={
                            browseBulkPick
                              ? (e) => {
                                  if (e.key === "Enter" || e.key === " ") {
                                    e.preventDefault();
                                    e.stopPropagation();
                                    nav(`/detail/${r.id}`);
                                  }
                                }
                              : undefined
                          }
                        >
                          {renderTableCell(r, spec.key)}
                        </span>
                      </div>
                    ) : (
                      <div key={spec.key} className={styles.browseTd}>
                        {renderTableCell(r, spec.key)}
                      </div>
                    )
                  )}
                  <div className={styles.browseTdActions}>
                    {!browseBulkPick ? (
                      <Dropdown
                        menu={makeMenu(r)}
                        trigger={["click"]}
                        placement="bottomRight"
                      >
                        <Button
                          type="text"
                          size="small"
                          data-browse-table-action
                          className={styles.browseRowMoreBtn}
                          icon={<EllipsisOutlined rotate={90} />}
                          aria-label={t("pages.browse.aria_more")}
                          onClick={(e) => e.stopPropagation()}
                        />
                      </Dropdown>
                    ) : null}
                  </div>
                </div>
              );
            })}
          </div>
          {sortedRows.length > TABLE_PAGE_SIZE ? (
            <div className={styles.browseTablePagination}>
              <Pagination
                current={tablePage}
                pageSize={TABLE_PAGE_SIZE}
                total={sortedRows.length}
                onChange={(p) => setTablePage(p)}
                showSizeChanger={false}
                size="small"
              />
            </div>
          ) : null}
        </div>
      ) : viewMode === "list" ? (
        <div className={styles.listWrap}>
          {sortedRows.map((r) => {
            const isListSelected = browseSelectedIds.has(r.id);
            return (
              <div
                key={r.id}
                className={styles.listRow}
                data-selected={isListSelected ? "" : undefined}
                data-bulk-pick={browseBulkPick ? "" : undefined}
              >
                <div className={styles.listSelectSlot}>
                  <button
                    type="button"
                    className={styles.listSelectBtn}
                    aria-label={isListSelected ? t("pages.browse.aria_deselect") : t("pages.browse.aria_select")}
                    aria-pressed={isListSelected}
                    data-selected={isListSelected ? "" : undefined}
                    onClick={(e) => {
                      e.stopPropagation();
                      toggleBrowseSelect(r.id);
                    }}
                  >
                    {isListSelected ? <CheckOutlined /> : null}
                  </button>
                </div>
                <div
                  className={styles.listRowMain}
                  tabIndex={0}
                  aria-label={
                    browseBulkPick
                      ? t("pages.browse.list_view_label", { title: r.title || t("pages.browse.untitled") })
                      : t("pages.browse.list_view_label_detail", { title: r.title || t("pages.browse.untitled") })
                  }
                  onClick={() => {
                    if (!browseBulkPick) nav(`/detail/${r.id}`);
                  }}
                  onKeyDown={(e) => {
                    if (e.key === "Enter" || e.key === " ") {
                      e.preventDefault();
                      if (browseBulkPick) toggleBrowseSelect(r.id);
                      else nav(`/detail/${r.id}`);
                    }
                  }}
                >
                  <div
                    className={styles.listPosterBlock}
                    onClick={
                      browseBulkPick
                        ? (e) => {
                            e.stopPropagation();
                            toggleBrowseSelect(r.id);
                          }
                        : undefined
                    }
                  >
                    <div
                      className={styles.listPosterInner}
                      data-selected={isListSelected ? "" : undefined}
                    >
                      <img
                        key={posterImgKey(r)}
                        className={styles.listPosterImg}
                        src={mediaPosterSrc(r)}
                        alt=""
                        loading="lazy"
                        decoding="async"
                        onError={(e) => {
                          e.currentTarget.style.display = "none";
                        }}
                      />
                      {!browseBulkPick ? (
                        <button
                          type="button"
                          className={styles.listPlayOverlay}
                          aria-label={t("pages.browse.aria_play")}
                          onClick={(e) => {
                            e.stopPropagation();
                            nav(`/player/${r.id}`);
                          }}
                        >
                          <span className={styles.listPlayCircle}>
                            <CaretRightOutlined />
                          </span>
                        </button>
                      ) : null}
                    </div>
                  </div>
                  <div
                    className={styles.listInfo}
                    onClick={browseBulkPick ? () => nav(`/detail/${r.id}`) : undefined}
                    style={browseBulkPick ? { cursor: "pointer" } : undefined}
                  >
                    <div className={styles.listTitle}>{r.title || t("pages.browse.untitled")}</div>
                    <div className={styles.listMeta}>
                      {displayYear(r)} · {fmtDurationLocalized(r.duration, t)}
                    </div>
                  </div>
                </div>
                {!browseBulkPick ? (
                  <div className={styles.listMoreSlot}>
                    <Dropdown
                      menu={makeMenu(r)}
                      trigger={["click"]}
                      placement="bottomRight"
                    >
                      <Button
                        type="text"
                        size="small"
                        className={styles.listMoreBtn}
                        icon={<EllipsisOutlined rotate={90} />}
                        aria-label={t("pages.browse.aria_more")}
                        onClick={(e) => e.stopPropagation()}
                      />
                    </Dropdown>
                  </div>
                ) : null}
              </div>
            );
          })}
        </div>
      ) : (
        <div className={viewMode === "poster" ? styles.posterGrid : styles.thumbGrid}>
          {sortedRows.map((r) => {
            const isCardSelected = browseSelectedIds.has(r.id);
            const coverClass = viewMode === "poster" ? styles.posterImage : styles.thumbImage;
            return (
              <div key={r.id} className={viewMode === "poster" ? styles.posterCard : styles.thumbCard}>
                <div
                  className={coverClass}
                  data-selected={isCardSelected ? "" : undefined}
                  data-bulk-pick={browseBulkPick ? "" : undefined}
                  tabIndex={0}
                  aria-label={
                    browseBulkPick
                      ? t("pages.browse.card_view_label_select", { title: r.title || t("pages.browse.untitled") })
                      : t("pages.browse.list_view_label_detail", { title: r.title || t("pages.browse.untitled") })
                  }
                  onClick={(e) => {
                    if ((e.target as HTMLElement).closest("[data-browse-card-action]")) return;
                    if (browseBulkPick) {
                      toggleBrowseSelect(r.id);
                      return;
                    }
                    nav(`/detail/${r.id}`);
                  }}
                  onKeyDown={(e) => {
                    if (e.key === "Enter" || e.key === " ") {
                      e.preventDefault();
                      if (browseBulkPick) toggleBrowseSelect(r.id);
                      else nav(`/detail/${r.id}`);
                    }
                  }}
                >
                  <img
                    key={posterImgKey(r)}
                    className={styles.gridCoverImg}
                    src={mediaPosterSrc(r)}
                    alt=""
                    loading="lazy"
                    decoding="async"
                    onLoadStart={(e) => {
                      e.currentTarget.parentElement?.removeAttribute("data-cover-loaded");
                    }}
                    onLoad={(e) => {
                      e.currentTarget.parentElement?.setAttribute("data-cover-loaded", "");
                    }}
                    onError={(ev) => {
                      ev.currentTarget.style.display = "none";
                      ev.currentTarget.parentElement?.removeAttribute("data-cover-loaded");
                    }}
                  />
                  {r.completed === 1 ? (
                    <div className={styles.gridWatchedBadge} role="status" aria-label={t("pages.media_detail.aria_watched")}>
                      <CheckOutlined />
                    </div>
                  ) : null}
                  <div className={styles.gridHoverShade} aria-hidden={browseBulkPick ? true : undefined}>
                    {!browseBulkPick ? (
                      <>
                        <button
                          type="button"
                          data-browse-card-action
                          className={`${styles.gridCornerBtn} ${styles.gridEditBtn}`}
                          aria-label={t("pages.browse.aria_edit")}
                          onClick={(e) => {
                            e.stopPropagation();
                            nav(`/media-manager?media_id=${r.id}`);
                          }}
                        >
                          <EditOutlined />
                        </button>
                        <button
                          type="button"
                          data-browse-card-action
                          className={styles.gridPlayBtn}
                          aria-label={t("pages.browse.aria_play")}
                          onClick={(e) => {
                            e.stopPropagation();
                            nav(`/player/${r.id}`);
                          }}
                        >
                          <CaretRightOutlined />
                        </button>
                        <div className={styles.gridMoreCorner} data-browse-card-action>
                          <Dropdown
                            menu={makeMenu(r)}
                            trigger={["click"]}
                            placement="bottomRight"
                          >
                            <Button
                              type="text"
                              size="small"
                              className={styles.gridMoreIconBtn}
                              icon={<EllipsisOutlined rotate={90} />}
                              aria-label={t("pages.browse.aria_more")}
                              onClick={(e) => e.stopPropagation()}
                            />
                          </Dropdown>
                        </div>
                      </>
                    ) : null}
                  </div>
                  <button
                    type="button"
                    data-browse-card-action
                    className={styles.gridSelectBtn}
                    data-selected={isCardSelected ? "" : undefined}
                    aria-label={isCardSelected ? t("pages.browse.aria_deselect") : t("pages.browse.aria_select")}
                    aria-pressed={isCardSelected}
                    onClick={(e) => {
                      e.stopPropagation();
                      toggleBrowseSelect(r.id);
                    }}
                  >
                    {isCardSelected ? <CheckOutlined /> : null}
                  </button>
                </div>
                <div
                  className={styles.cardBody}
                  onClick={browseBulkPick ? () => nav(`/detail/${r.id}`) : undefined}
                  style={browseBulkPick ? { cursor: "pointer" } : undefined}
                >
                  <div className={styles.cardTitle}>{r.title || t("pages.browse.untitled")}</div>
                </div>
              </div>
            );
          })}
        </div>
      )}
      {playlistModalMediaIds != null && playlistModalMediaIds.length > 0 && (
        <AddToPlaylistModal
          mediaIds={playlistModalMediaIds}
          open
          defaultNewPlaylistName={playlistModalDefaultTitle}
          onClose={() => setPlaylistModalMediaIds(null)}
          onAdded={(pl) => {
            rememberPlaylistAdded(pl);
            setRecentPlaylistMenu(readRecentPlaylists());
          }}
        />
      )}
      {addToFavoriteFolderMediaId != null && (
        <AddToFavoriteFolderPickerModal
          mediaId={addToFavoriteFolderMediaId}
          open
          onClose={() => setAddToFavoriteFolderMediaId(null)}
          onAdded={(folder) => rememberFolderMenuAdded(folder)}
        />
      )}
      <MediaMatchModal
        media={matchMedia}
        fixMatch={Boolean(matchMedia?.scraped)}
        open={matchMedia != null}
        onClose={() => setMatchMedia(null)}
        onMatched={(update) => applyMediaMatchUpdate(update, activeRouteKey)}
      />
      {optimizationModalMediaId !== null && (
        <VideoOptimizationModal
          mediaId={optimizationModalMediaId}
          mediaTitle={optimizationModalTitle}
          open={optimizationModalMediaId !== null}
          onClose={() => {
            setOptimizationModalMediaId(null);
            setOptimizationModalTitle(undefined);
          }}
          onOptimized={() => {
            load();
          }}
        />
      )}
    </div>
  );
}
