import {
  AppstoreOutlined,
  BarsOutlined,
  CaretRightOutlined,
  CheckOutlined,
  DownOutlined,
  EditOutlined,
  EllipsisOutlined,
  FolderAddOutlined,
  PictureOutlined,
  PlusOutlined,
  RollbackOutlined,
  TableOutlined,
  UpOutlined,
} from "@ant-design/icons";
import type { MenuProps } from "antd";
import { Button, Dropdown, Empty, Modal, Pagination, Spin, message } from "antd";
import type { ComponentType } from "react";
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { useNavigate } from "react-router-dom";
import {
  FavoriteFolder,
  FavoriteFolderItem,
  MediaItem,
  addFavoriteFolderItem,
  addPlaylistItem,
  createFavoriteFolder,
  deleteFavoriteFolder,
  fetchFavoriteFolder,
  fetchFavoriteFolders,
  fetchFavorites,
  fetchLibraries,
  mediaPosterSrc,
  musicMediaPosterSrc,
  photoThumbSrc,
  removeFavorite,
  updateFavoriteFolder,
} from "../api/client";
import AddToFavoriteFolderPickerModal from "../components/AddToFavoriteFolderPickerModal";
import AddToFavoriteFolderModal from "../components/AddToFavoriteFolderModal";
import AddToPlaylistModal from "../components/AddToPlaylistModal";
import FavoriteFolderFormModal from "../components/FavoriteFolderFormModal";
import MediaPosterImg from "../components/MediaPosterImg";
import MusicPosterPlaceholderIcon from "../components/MusicPosterPlaceholderIcon";
import PhotoLightbox from "../components/PhotoLightbox";
import { buildMediaMenuItems } from "../components/mediaMenuItems";
import { mediaItemsToMusicQueue } from "../lib/albumPlayback";
import { useMusicPlayerStore } from "../store/musicPlayer";
import {
  FAVORITE_CATEGORY_ORDER,
  FavoriteCategoryKey,
  FavoriteMediaCategory,
  buildFavoriteCategoryLabels,
  buildFolderPreviewSlots,
  countFavoritesByCategory,
  favoriteFolderCoverSrc,
  favoriteFolderItemToMediaItem,
  filterFavoritesByCategory,
  getFavoriteMediaCategory,
  isFavoriteCategoryKey,
  isFavoriteVideoItem,
  pickDefaultFavoriteCategory,
} from "../lib/favoriteCategories";
import { useFavoriteFolderMenuRecents } from "../lib/useFavoriteFolderMenuRecents";
import { formatServerDateTime, serverDateTimeToMillis } from "../lib/datetime";
import { readRecentPlaylists, rememberPlaylistAdded } from "../lib/recentPlaylists";
import { useT, type TranslateFn } from "../i18n";
import browseStyles from "./Browse.module.css";
import styles from "./Favorites.module.css";
import musicStyles from "./MusicBrowse.module.css";

type ViewMode = "poster" | "thumb" | "list" | "table";
type SortField = "title" | "added";
type SortOrder = "asc" | "desc";

const FAVORITES_PREFS_KEY = "knox.favorites.prefs.v1";
const TABLE_PAGE_SIZE = 20;

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

function readFavoritesPrefs(): {
  viewMode: ViewMode;
  sortField: SortField;
  sortOrder: SortOrder;
  category?: FavoriteCategoryKey;
} | null {
  if (typeof window === "undefined") return null;
  try {
    const raw = window.localStorage.getItem(FAVORITES_PREFS_KEY);
    if (!raw) return null;
    const parsed = JSON.parse(raw) as {
      viewMode?: ViewMode;
      sortField?: SortField;
      sortOrder?: SortOrder;
      category?: FavoriteCategoryKey;
    };
    const viewMode: ViewMode = ["poster", "thumb", "list", "table"].includes(String(parsed.viewMode))
      ? (parsed.viewMode as ViewMode)
      : "table";
    const sortField: SortField = ["title", "added"].includes(String(parsed.sortField))
      ? (parsed.sortField as SortField)
      : "added";
    const sortOrder: SortOrder = parsed.sortOrder === "asc" || parsed.sortOrder === "desc" ? parsed.sortOrder : "desc";
    const category =
      parsed.category && isFavoriteCategoryKey(String(parsed.category))
        ? (parsed.category as FavoriteCategoryKey)
        : undefined;
    return { viewMode, sortField, sortOrder, category };
  } catch {
    return null;
  }
}

type TableColSpec = { key: string; label: string; sortField: SortField; widthPx: number };

function buildTableColSpecs(t: TranslateFn): TableColSpec[] {
  return [
    { key: "title", label: t("pages.browse.col_title"), sortField: "title", widthPx: 0 },
    { key: "duration", label: t("pages.browse.col_duration"), sortField: "added", widthPx: 112 },
    { key: "quality", label: t("pages.browse.col_quality"), sortField: "added", widthPx: 104 },
    { key: "added", label: t("pages.browse.col_added"), sortField: "added", widthPx: 168 },
  ];
}

export default function FavoritesPage() {
  const nav = useNavigate();
  const t = useT();
  const VIEW_MODES = useMemo(() => buildViewModes(t), [t]);
  const TABLE_COL_SPECS = useMemo(() => buildTableColSpecs(t), [t]);
  const CATEGORY_LABELS = useMemo(() => buildFavoriteCategoryLabels(t), [t]);
  const [rows, setRows] = useState<MediaItem[]>([]);
  const [folders, setFolders] = useState<FavoriteFolder[]>([]);
  const [libTypeById, setLibTypeById] = useState<Map<number, string>>(() => new Map());
  const [activeCategory, setActiveCategory] = useState<FavoriteCategoryKey>(
    () => readFavoritesPrefs()?.category ?? "movie",
  );
  const [selectedFolderId, setSelectedFolderId] = useState<number | null>(null);
  const [folderItems, setFolderItems] = useState<FavoriteFolderItem[]>([]);
  const [folderLoading, setFolderLoading] = useState(false);
  const categoryInitialized = useRef(false);
  const [loading, setLoading] = useState(false);
  const [viewMode, setViewMode] = useState<ViewMode>(() => readFavoritesPrefs()?.viewMode ?? "table");
  const [sortField, setSortField] = useState<SortField>(() => readFavoritesPrefs()?.sortField ?? "added");
  const [sortOrder, setSortOrder] = useState<SortOrder>(() => readFavoritesPrefs()?.sortOrder ?? "desc");
  const [viewModeMenuOpen, setViewModeMenuOpen] = useState(false);
  const [selectedIds, setSelectedIds] = useState<Set<number>>(() => new Set());
  const [tablePage, setTablePage] = useState(1);
  const [addToPlaylistMediaId, setAddToPlaylistMediaId] = useState<number | null>(null);
  const [addToFavoriteFolderMediaId, setAddToFavoriteFolderMediaId] = useState<number | null>(null);
  const [recentPlaylistMenu, setRecentPlaylistMenu] = useState(readRecentPlaylists);
  const { recentFavoriteFolders, rememberFolderMenuAdded, reloadRecentFavoriteFolders } =
    useFavoriteFolderMenuRecents();
  const [folderForm, setFolderForm] = useState<null | { mode: "create" } | { mode: "edit"; folder: FavoriteFolder }>(
    null,
  );
  const [folderFormSubmitting, setFolderFormSubmitting] = useState(false);
  const [addVideoFolderId, setAddVideoFolderId] = useState<number | null>(null);
  const [photoLightboxIndex, setPhotoLightboxIndex] = useState<number | null>(null);

  const reloadFolders = useCallback(async () => {
    const list = await fetchFavoriteFolders();
    setFolders(list);
    return list;
  }, []);

  const load = useCallback(async () => {
    setLoading(true);
    try {
      const [favR, folderR, libR] = await Promise.allSettled([
        fetchFavorites(),
        fetchFavoriteFolders(),
        fetchLibraries(),
      ]);
      const favItems = favR.status === "fulfilled" ? favR.value : [];
      const folderItemsList = folderR.status === "fulfilled" ? folderR.value : [];
      const libs = libR.status === "fulfilled" ? libR.value : [];
      setRows(favItems);
      setFolders(folderItemsList);
      const typeMap = new Map<number, string>();
      for (const lib of libs) {
        typeMap.set(lib.id, (lib.type || "").trim().toLowerCase());
      }
      setLibTypeById(typeMap);
      if (!categoryInitialized.current) {
        categoryInitialized.current = true;
        const saved = readFavoritesPrefs()?.category;
        const counts = countFavoritesByCategory(favItems, typeMap, folderItemsList.length);
        if (saved && isFavoriteCategoryKey(saved)) {
          setActiveCategory(saved);
        } else {
          setActiveCategory(pickDefaultFavoriteCategory(counts));
        }
      }
    } catch (e: unknown) {
      message.error((e as Error).message || t("pages.favorites.load_failed"));
      setRows([]);
      setFolders([]);
    } finally {
      setLoading(false);
    }
  }, [t]);

  useEffect(() => {
    void load();
  }, [load]);

  useEffect(() => {
    if (typeof window === "undefined") return;
    window.localStorage.setItem(
      FAVORITES_PREFS_KEY,
      JSON.stringify({ viewMode, sortField, sortOrder, category: activeCategory })
    );
  }, [viewMode, sortField, sortOrder, activeCategory]);

  useEffect(() => {
    if (activeCategory !== "folders" || selectedFolderId == null) {
      setFolderItems([]);
      setFolderLoading(false);
      return;
    }
    let cancelled = false;
    setFolderLoading(true);
    void fetchFavoriteFolder(selectedFolderId)
      .then((folder) => {
        if (!cancelled) setFolderItems(folder.items ?? []);
      })
      .catch(() => {
        if (!cancelled) setFolderItems([]);
      })
      .finally(() => {
        if (!cancelled) setFolderLoading(false);
      });
    return () => {
      cancelled = true;
    };
  }, [activeCategory, selectedFolderId]);

  const categoryCounts = useMemo(
    () => countFavoritesByCategory(rows, libTypeById, folders.length),
    [rows, libTypeById, folders.length],
  );

  const filteredRows = useMemo(() => {
    if (activeCategory === "folders") {
      return selectedFolderId != null ? folderItems.map(favoriteFolderItemToMediaItem) : [];
    }
    return filterFavoritesByCategory(rows, activeCategory as FavoriteMediaCategory, libTypeById);
  }, [activeCategory, selectedFolderId, folderItems, rows, libTypeById]);

  const sortedRows = [...filteredRows].sort((a, b) => {
    const factor = sortOrder === "asc" ? 1 : -1;
    if (sortField === "title") {
      return (a.title ?? "").localeCompare(b.title ?? "", "zh-CN") * factor;
    }
    const timeA = serverDateTimeToMillis(a.created_at);
    const timeB = serverDateTimeToMillis(b.created_at);
    return (timeA - timeB) * factor;
  });

  const photoFavorites = useMemo(
    () => sortedRows.filter((row) => getFavoriteMediaCategory(row, libTypeById) === "photo"),
    [sortedRows, libTypeById],
  );

  const musicFavorites = useMemo(
    () => sortedRows.filter((row) => getFavoriteMediaCategory(row, libTypeById) === "music"),
    [sortedRows, libTypeById],
  );

  const tableGridTemplate = (() => {
    const parts: string[] = ["40px"];
    for (const spec of TABLE_COL_SPECS) {
      parts.push(spec.widthPx ? `${spec.widthPx}px` : "minmax(160px, 1fr)");
    }
    parts.push("40px");
    return parts.join(" ");
  })();

  const pagedTableRows = sortedRows.slice((tablePage - 1) * TABLE_PAGE_SIZE, tablePage * TABLE_PAGE_SIZE);

  useEffect(() => {
    setTablePage(1);
    setSelectedIds(new Set());
    setPhotoLightboxIndex(null);
  }, [sortedRows.length, viewMode, activeCategory, selectedFolderId]);

  const openFavoritePreview = useCallback(
    (item: MediaItem) => {
      const cat = getFavoriteMediaCategory(item, libTypeById);
      if (cat === "photo") {
        const idx = photoFavorites.findIndex((r) => r.id === item.id);
        if (idx >= 0) setPhotoLightboxIndex(idx);
        return;
      }
      if (cat === "document") {
        nav(`/reader/${item.id}`);
        return;
      }
      if (cat === "music") {
        const queue = mediaItemsToMusicQueue(
          musicFavorites.map((m) => ({ ...m, file_type: m.file_type || "audio" })),
        );
        if (queue.length === 0) return;
        const idx = queue.findIndex((q) => q.mediaId === item.id);
        const st = useMusicPlayerStore.getState();
        st.playQueue(queue, idx >= 0 ? idx : 0);
        st.openFullscreen();
        return;
      }
      nav(`/player/${item.id}`);
    },
    [libTypeById, musicFavorites, nav, photoFavorites],
  );

  function selectCategory(key: FavoriteCategoryKey) {
    setActiveCategory(key);
    if (key !== "folders") setSelectedFolderId(null);
  }

  function openFolder(id: number) {
    setActiveCategory("folders");
    setSelectedFolderId(id);
    setViewMode("thumb");
  }

  function closeFolderDetail() {
    setSelectedFolderId(null);
  }

  const videoFavorites = useMemo(
    () => rows.filter((row) => isFavoriteVideoItem(row, libTypeById)),
    [rows, libTypeById],
  );

  const openCreateFolderForm = useCallback(() => {
    setFolderForm({ mode: "create" });
  }, []);

  const openEditFolderForm = useCallback((folder: FavoriteFolder) => {
    setFolderForm({ mode: "edit", folder });
  }, []);

  const openAddVideoModal = useCallback((folderId: number) => {
    setAddVideoFolderId(folderId);
  }, []);

  const handleFolderFormSubmit = useCallback(
    async (name: string) => {
      if (!folderForm) return;
      setFolderFormSubmitting(true);
      try {
        if (folderForm.mode === "create") {
          await createFavoriteFolder(name);
          message.success(t("pages.favorites.folder_created"));
        } else {
          await updateFavoriteFolder(folderForm.folder.id, name);
          message.success(t("pages.favorites.folder_updated"));
        }
        await reloadFolders();
        void reloadRecentFavoriteFolders();
        setFolderForm(null);
      } catch (e: unknown) {
        message.error((e as Error).message || t("pages.favorites.operation_failed"));
      } finally {
        setFolderFormSubmitting(false);
      }
    },
    [folderForm, reloadFolders, t],
  );

  const handleDeleteFolder = useCallback(
    (folder: FavoriteFolder) => {
      Modal.confirm({
        title: t("pages.favorites.folder_delete_title"),
        content: t("pages.favorites.folder_delete_confirm", { name: folder.name }),
        okText: t("pages.favorites.folder_delete_ok"),
        cancelText: t("pages.favorites.folder_delete_cancel"),
        okButtonProps: { danger: true },
        onOk: async () => {
          try {
            await deleteFavoriteFolder(folder.id);
            if (selectedFolderId === folder.id) setSelectedFolderId(null);
            await reloadFolders();
            void reloadRecentFavoriteFolders();
            message.success(t("pages.favorites.folder_deleted"));
          } catch (e: unknown) {
            message.error((e as Error).message || t("pages.favorites.operation_failed"));
            throw e;
          }
        },
      });
    },
    [reloadFolders, selectedFolderId, t],
  );

  const buildFolderMenu = useCallback(
    (folder: FavoriteFolder): MenuProps => ({
      items: [
        {
          key: "add",
          label: t("pages.favorites.menu_add_video"),
          onClick: () => openAddVideoModal(folder.id),
        },
        {
          key: "edit",
          label: t("pages.favorites.menu_edit_folder"),
          onClick: () => openEditFolderForm(folder),
        },
        {
          key: "delete",
          label: t("pages.favorites.menu_delete_folder"),
          danger: true,
          onClick: () => handleDeleteFolder(folder),
        },
      ],
    }),
    [handleDeleteFolder, openAddVideoModal, openEditFolderForm, t],
  );

  const refreshFolderItems = useCallback(async () => {
    if (selectedFolderId == null) return;
    setFolderLoading(true);
    try {
      const folder = await fetchFavoriteFolder(selectedFolderId);
      setFolderItems(folder.items ?? []);
      await reloadFolders();
    } catch {
      setFolderItems([]);
    } finally {
      setFolderLoading(false);
    }
  }, [reloadFolders, selectedFolderId]);

  const showFolderGrid = activeCategory === "folders" && selectedFolderId == null;
  const showFolderSplit = activeCategory === "folders" && selectedFolderId != null;
  const contentLoading = loading || (showFolderSplit && folderLoading);
  const isMusicGrid = activeCategory === "music" && viewMode !== "table" && viewMode !== "list";
  const isPhotoGrid = activeCategory === "photo" && viewMode !== "table" && viewMode !== "list";
  const isSquareListPoster = activeCategory === "music" || activeCategory === "photo";

  function toggleSelect(id: number) {
    setSelectedIds((prev) => {
      const next = new Set(prev);
      if (next.has(id)) next.delete(id);
      else next.add(id);
      return next;
    });
  }

  const selectionCount = selectedIds.size;
  const bulkPick = selectionCount > 0;

  async function onUnfavorite(id: number) {
    try {
      await removeFavorite(id);
      message.success(t("pages.favorites.unfavorited"));
      void load();
    } catch (e: unknown) {
      message.error((e as Error).message || t("pages.favorites.operation_failed"));
    }
  }

  const isWatched = (r: MediaItem) => Boolean(r.last_play_at);

  const makeMenu = useCallback(
    (r: MediaItem): MenuProps =>
      buildMediaMenuItems(r, nav, {
        isWatched: isWatched(r),
        onAddToPlaylist: (mid) => setAddToPlaylistMediaId(mid),
        recentPlaylists: recentPlaylistMenu,
        onQuickAddToPlaylist: async (mid, pid) => {
          try {
            await addPlaylistItem(pid, mid);
            const name =
              recentPlaylistMenu.find((p) => p.id === pid)?.name ??
              readRecentPlaylists().find((p) => p.id === pid)?.name ??
              t("pages.favorites.playlist_fallback");
            message.success(t("pages.favorites.added_to_playlist", { name }));
            rememberPlaylistAdded({ id: pid, name });
            setRecentPlaylistMenu(readRecentPlaylists());
          } catch {
            message.error(t("pages.favorites.add_failed"));
          }
        },
        onAddToFavoriteFolder: (mid) => setAddToFavoriteFolderMediaId(mid),
        recentFavoriteFolders,
        onQuickAddToFavoriteFolder: async (mid, folderId) => {
          try {
            await addFavoriteFolderItem(folderId, mid);
            const name =
              recentFavoriteFolders.find((f) => f.id === folderId)?.name ??
              t("components.media_menu.favorite_folder_fallback");
            message.success(t("components.add_to_favorite_folder_picker_modal.added_single", { name }));
            rememberFolderMenuAdded({ id: folderId, name });
            if (selectedFolderId === folderId) void refreshFolderItems();
          } catch {
            message.error(t("components.add_to_favorite_folder_picker_modal.add_failed_dup"));
          }
        },
        onUnfavorite: (id) => void onUnfavorite(id),
        afterToggleWatched: () => void load(),
      }),
    [nav, recentPlaylistMenu, recentFavoriteFolders, rememberFolderMenuAdded, selectedFolderId, refreshFolderItems, load, t],
  );

  const addToPlaylistTarget = useMemo(
    () => (addToPlaylistMediaId != null ? rows.find((x) => x.id === addToPlaylistMediaId) : undefined),
    [addToPlaylistMediaId, rows],
  );

  const viewModeMenuItems: MenuProps["items"] = VIEW_MODES.map(({ value, label, Icon }) => ({
    key: value,
    icon: <Icon />,
    label,
  }));

  const CurrentViewIcon = VIEW_MODES.find((m) => m.value === viewMode)?.Icon ?? TableOutlined;
  const currentViewLabel = VIEW_MODES.find((m) => m.value === viewMode)?.label ?? t("pages.browse.table_fallback");

  const viewModePicker = (
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
  );

  return (
    <div style={{ padding: "16px 0 32px" }}>
      <div className={styles.topBar}>
        <div className={styles.topLeftTools}>
          <div className={styles.categoryTabs} role="tablist" aria-label={t("pages.favorites.category_tabs_aria")}>
            {FAVORITE_CATEGORY_ORDER.map((key) => (
              <button
                key={key}
                type="button"
                role="tab"
                aria-selected={activeCategory === key}
                className={`${styles.categoryTab} ${activeCategory === key ? styles.categoryTabActive : ""}`}
                onClick={() => selectCategory(key)}
              >
                <span>{CATEGORY_LABELS[key]}</span>
                {categoryCounts[key] > 0 ? (
                  <span className={styles.categoryCount}>{categoryCounts[key]}</span>
                ) : null}
              </button>
            ))}
          </div>
        </div>
        <div className={styles.topRightTools}>
          {showFolderGrid ? (
            <button type="button" className={styles.folderActionBtn} onClick={openCreateFolderForm}>
              <FolderAddOutlined />
              <span>{t("pages.favorites.new_folder")}</span>
            </button>
          ) : showFolderSplit ? (
            <>
              <button
                type="button"
                className={`${styles.folderActionBtn} ${styles.folderActionBtnIconOnly}`}
                aria-label={t("pages.favorites.add_video")}
                onClick={() => selectedFolderId != null && openAddVideoModal(selectedFolderId)}
              >
                <FolderAddOutlined />
              </button>
              <button
                type="button"
                className={`${styles.folderActionBtn} ${styles.folderActionBtnIconOnly}`}
                aria-label={t("pages.favorites.back")}
                onClick={closeFolderDetail}
              >
                <RollbackOutlined />
              </button>
              {viewModePicker}
            </>
          ) : (
            viewModePicker
          )}
        </div>
      </div>

      {showFolderGrid ? (
        <div className={styles.folderListPage}>
          {loading ? (
            <div className={styles.loadingWrap}>
              <Spin />
            </div>
          ) : folders.length === 0 ? (
            <div className={styles.folderListEmpty}>
              <Empty description={t("pages.favorites.folders_empty")} />
            </div>
          ) : (
            <>
              <div className={styles.folderListGrid}>
                {folders.map((folder) => {
                  const previewSlots = buildFolderPreviewSlots(folder);
                  return (
                    <div
                      key={folder.id}
                      className={styles.folderListCard}
                      role="button"
                      tabIndex={0}
                      onClick={() => openFolder(folder.id)}
                      onKeyDown={(e) => {
                        if (e.key === "Enter" || e.key === " ") {
                          e.preventDefault();
                          openFolder(folder.id);
                        }
                      }}
                    >
                      <div className={styles.folderListCardHead}>
                        <div>
                          <div className={styles.folderListCardTitle}>{folder.name}</div>
                          <div className={styles.folderListCardMeta}>
                            {t("pages.favorites.folder_work_count", { count: folder.item_count ?? 0 })}
                          </div>
                        </div>
                        <Dropdown menu={buildFolderMenu(folder)} trigger={["click"]} placement="bottomRight">
                          <button
                            type="button"
                            className={styles.folderListCardMenuBtn}
                            aria-label={t("pages.favorites.folder_menu_aria")}
                            onClick={(e) => e.stopPropagation()}
                          >
                            <EllipsisOutlined />
                          </button>
                        </Dropdown>
                      </div>
                      <div className={styles.folderListCardPreviews}>
                        {previewSlots.map((slot, idx) => {
                          const poster = slot?.poster_url || (slot?.media_id ? mediaPosterSrc({ id: slot.media_id, poster_url: "" }) : "");
                          return (
                            <div key={`${folder.id}-${idx}`} className={styles.folderPreviewSlot}>
                              {poster ? (
                                <img className={styles.folderPreviewImg} src={poster} alt="" loading="lazy" />
                              ) : (
                                <div className={styles.folderPreviewEmpty} aria-hidden>
                                  <MusicPosterPlaceholderIcon className={styles.folderPreviewEmptyIcon} />
                                </div>
                              )}
                            </div>
                          );
                        })}
                      </div>
                    </div>
                  );
                })}
              </div>
              <div className={styles.folderListEnd}>{t("pages.favorites.no_more")}</div>
            </>
          )}
        </div>
      ) : (
        <div className={showFolderSplit ? styles.folderLayout : undefined}>
          {showFolderSplit ? (
            <aside className={styles.folderSidebar}>
              <button type="button" className={styles.folderSidebarNew} onClick={openCreateFolderForm}>
                <span className={styles.folderSidebarNewIcon}>
                  <PlusOutlined />
                </span>
                <span>{t("pages.favorites.new_folder")}</span>
              </button>
              <div className={styles.folderSidebarList}>
                {folders.map((folder) => {
                  const cover =
                    favoriteFolderCoverSrc(folder) ||
                    (folder.first_media_id ? mediaPosterSrc({ id: folder.first_media_id, poster_url: "" }) : "");
                  return (
                    <div
                      key={folder.id}
                      className={`${styles.folderSidebarCard} ${selectedFolderId === folder.id ? styles.folderSidebarCardActive : ""}`}
                    >
                      <button
                        type="button"
                        className={styles.folderSidebarCardMain}
                        onClick={() => setSelectedFolderId(folder.id)}
                      >
                        <div className={styles.folderSidebarThumb}>
                          {cover ? (
                            <img className={styles.folderSidebarThumbImg} src={cover} alt="" loading="lazy" />
                          ) : (
                            <div className={styles.folderSidebarThumbFallback} aria-hidden />
                          )}
                        </div>
                        <div className={styles.folderSidebarInfo}>
                          <div className={styles.folderSidebarCardTitle}>{folder.name}</div>
                          <div className={styles.folderSidebarCardCount}>{folder.item_count ?? 0}</div>
                        </div>
                      </button>
                      <Dropdown menu={buildFolderMenu(folder)} trigger={["click"]} placement="bottomRight">
                        <button
                          type="button"
                          className={styles.folderSidebarCardMenuBtn}
                          aria-label={t("pages.favorites.folder_menu_aria")}
                          onClick={(e) => e.stopPropagation()}
                        >
                          <EllipsisOutlined />
                        </button>
                      </Dropdown>
                    </div>
                  );
                })}
              </div>
            </aside>
          ) : null}
          <div className={showFolderSplit ? styles.folderMain : undefined}>
            {contentLoading ? (
              <div className={styles.loadingWrap}>
                <Spin />
              </div>
            ) : sortedRows.length === 0 ? (
              <Empty
                description={
                  showFolderSplit ? t("pages.favorites.folder_items_empty") : t("pages.favorites.empty")
                }
              />
            ) : viewMode === "table" ? (
        <div className={styles.browseTableWrap}>
          <div className={styles.browseTableHead}>
            <div className={styles.browseTableHeadRow} style={{ gridTemplateColumns: tableGridTemplate }}>
              <div className={styles.browseThGutter} />
              {TABLE_COL_SPECS.map((spec) => (
                <div
                  key={spec.key}
                  role="button"
                  tabIndex={0}
                  className={styles.browseTh}
                  onClick={() => {
                    if (sortField === spec.sortField) {
                      setSortOrder((o) => (o === "asc" ? "desc" : "asc"));
                    } else {
                      setSortField(spec.sortField);
                      setSortOrder("desc");
                    }
                  }}
                  onKeyDown={(e) => {
                    if (e.key === "Enter" || e.key === " ") {
                      e.preventDefault();
                      if (sortField === spec.sortField) {
                        setSortOrder((o) => (o === "asc" ? "desc" : "asc"));
                      } else {
                        setSortField(spec.sortField);
                        setSortOrder("desc");
                      }
                    }
                  }}
                >
                  <span>{spec.label}</span>
                </div>
              ))}
              <div className={styles.browseThActions} aria-hidden />
            </div>
          </div>
          <div className={styles.browseTableBody}>
            {pagedTableRows.map((r, idx) => {
              const globalIdx = (tablePage - 1) * TABLE_PAGE_SIZE + idx;
              const isSel = selectedIds.has(r.id);
              return (
                <div
                  key={r.id}
                  className={styles.browseTr}
                  style={{ gridTemplateColumns: tableGridTemplate }}
                  data-selected={isSel ? "" : undefined}
                  data-stripe={globalIdx % 2 === 1 ? "" : undefined}
                  data-bulk-pick={bulkPick ? "" : undefined}
                  onClick={() => {
                    if (!bulkPick) nav(`/detail/${r.id}`);
                  }}
                >
                  <div className={styles.browseTdGutter}>
                    <button
                      type="button"
                      className={styles.browseGutterSelect}
                      aria-label={isSel ? t("pages.browse.aria_deselect") : t("pages.browse.aria_select")}
                      data-selected={isSel ? "" : undefined}
                      onClick={(e) => {
                        e.stopPropagation();
                        toggleSelect(r.id);
                      }}
                    >
                      {isSel ? <CheckOutlined /> : null}
                    </button>
                  </div>
                  <div className={styles.browseTdTitle}>
                    {!bulkPick ? (
                      <button
                        type="button"
                        className={styles.browseRowPlay}
                        aria-label={t("pages.browse.aria_play")}
                        onClick={(e) => {
                          e.stopPropagation();
                          openFavoritePreview(r);
                        }}
                      >
                        <CaretRightOutlined />
                      </button>
                    ) : null}
                    <span className={styles.browseTitleText}>{r.title || t("pages.favorites.untitled")}</span>
                  </div>
                  <div className={styles.browseTd}>{fmtDurationLocalized(r.duration, t)}</div>
                  <div className={styles.browseTd}>{r.width && r.height ? `${r.width}x${r.height}` : "—"}</div>
                  <div className={styles.browseTd}>{formatServerDateTime(r.created_at)}</div>
                  <div className={styles.browseTdActions}>
                    {!bulkPick ? (
                      <Dropdown
                        menu={makeMenu(r)}
                        trigger={["click"]}
                        placement="bottomRight"
                      >
                        <Button
                          type="text"
                          size="small"
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
            const isListSelected = selectedIds.has(r.id);
            const musicCoverSrc =
              activeCategory === "music" ? musicMediaPosterSrc({ ...r, file_type: "audio" }) : null;
            return (
              <div
                key={r.id}
                className={styles.listRow}
                data-selected={isListSelected ? "" : undefined}
                data-bulk-pick={bulkPick ? "" : undefined}
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
                      toggleSelect(r.id);
                    }}
                  >
                    {isListSelected ? <CheckOutlined /> : null}
                  </button>
                </div>
                <div
                  className={styles.listRowMain}
                  tabIndex={0}
                  onClick={() => {
                    if (!bulkPick) nav(`/detail/${r.id}`);
                  }}
                  onKeyDown={(e) => {
                    if (e.key === "Enter" || e.key === " ") {
                      e.preventDefault();
                      if (bulkPick) toggleSelect(r.id);
                      else nav(`/detail/${r.id}`);
                    }
                  }}
                >
                  <div
                    className={styles.listPosterBlock}
                    onClick={
                      bulkPick
                        ? (e) => {
                            e.stopPropagation();
                            toggleSelect(r.id);
                          }
                        : undefined
                    }
                  >
                    <div
                      className={`${styles.listPosterInner} ${isSquareListPoster ? styles.listPosterInnerSquare : ""}`}
                      data-selected={isListSelected ? "" : undefined}
                    >
                      {activeCategory === "music" ? (
                        musicCoverSrc ? (
                          <img
                            src={musicCoverSrc}
                            alt=""
                            className={styles.listPosterImg}
                            loading="lazy"
                            decoding="async"
                          />
                        ) : (
                          <div className={styles.listPosterMusicPlaceholder}>
                            <MusicPosterPlaceholderIcon />
                          </div>
                        )
                      ) : activeCategory === "photo" ? (
                        <img
                          src={photoThumbSrc(r.id)}
                          alt=""
                          className={styles.listPosterImg}
                          loading="lazy"
                          decoding="async"
                        />
                      ) : (
                        <MediaPosterImg item={r} className={styles.listPosterImg} />
                      )}
                      {!bulkPick ? (
                        <button
                          type="button"
                          className={styles.listPlayOverlay}
                          aria-label={t("pages.browse.aria_play")}
                          onClick={(e) => {
                            e.stopPropagation();
                            openFavoritePreview(r);
                          }}
                        >
                          <span className={styles.listPlayCircle}>
                            <CaretRightOutlined />
                          </span>
                        </button>
                      ) : null}
                    </div>
                  </div>
                  <div className={styles.listInfo}>
                    <div className={styles.listTitle}>{r.title || t("pages.favorites.untitled")}</div>
                    <div className={styles.listMeta}>
                      {r.width && r.height ? `${r.width}x${r.height}` : "—"} · {fmtDurationLocalized(r.duration, t)}
                    </div>
                  </div>
                </div>
                {!bulkPick ? (
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
      ) : isMusicGrid ? (
        <div className={musicStyles.albumGrid}>
          {sortedRows.map((r) => {
            const isCardSelected = selectedIds.has(r.id);
            const subtitle = (r.music_artist || r.music_album_title || "").trim() || "—";
            const coverSrc = musicMediaPosterSrc({ ...r, file_type: "audio" });
            return (
              <div key={r.id} className={musicStyles.albumCard}>
                <div
                  className={`${musicStyles.albumCover} ${browseStyles.musicBrowseCover} ${!coverSrc ? musicStyles.noCover : ""}`}
                  data-selected={isCardSelected ? "" : undefined}
                  data-bulk-pick={bulkPick ? "" : undefined}
                  role="link"
                  tabIndex={0}
                  aria-label={r.title || t("pages.favorites.untitled")}
                  onClick={(e) => {
                    if ((e.target as HTMLElement).closest("[data-browse-card-action]")) return;
                    if (bulkPick) {
                      toggleSelect(r.id);
                      return;
                    }
                    nav(`/detail/${r.id}`);
                  }}
                  onKeyDown={(e) => {
                    if (e.key === "Enter" || e.key === " ") {
                      e.preventDefault();
                      if (bulkPick) toggleSelect(r.id);
                      else nav(`/detail/${r.id}`);
                    }
                  }}
                >
                  {coverSrc ? (
                    <img
                      className={musicStyles.albumCoverImg}
                      src={coverSrc}
                      alt=""
                      loading="lazy"
                      decoding="async"
                      onLoad={(e) => {
                        e.currentTarget.parentElement?.classList.remove(musicStyles.noCover);
                      }}
                      onError={(e) => {
                        e.currentTarget.style.display = "none";
                        e.currentTarget.parentElement?.classList.add(musicStyles.noCover);
                      }}
                    />
                  ) : null}
                  <div className={musicStyles.noCoverIcon}>
                    <MusicPosterPlaceholderIcon />
                  </div>
                  <div className={browseStyles.gridHoverShade} aria-hidden={bulkPick ? true : undefined}>
                    {!bulkPick ? (
                      <>
                        <button
                          type="button"
                          data-browse-card-action
                          className={`${browseStyles.gridCornerBtn} ${browseStyles.gridEditBtn}`}
                          aria-label={t("pages.browse.aria_edit")}
                          onClick={(e) => {
                            e.stopPropagation();
                            nav(`/detail/${r.id}`);
                          }}
                        >
                          <EditOutlined />
                        </button>
                        <div className={browseStyles.gridMoreCorner} data-browse-card-action>
                          <Dropdown menu={makeMenu(r)} trigger={["click"]} placement="bottomRight">
                            <Button
                              type="text"
                              size="small"
                              className={browseStyles.gridMoreIconBtn}
                              icon={<EllipsisOutlined rotate={90} />}
                              aria-label={t("pages.browse.aria_more")}
                              onClick={(e) => e.stopPropagation()}
                            />
                          </Dropdown>
                        </div>
                        <button
                          type="button"
                          data-browse-card-action
                          className={browseStyles.gridPlayBtn}
                          aria-label={t("pages.browse.aria_play")}
                          onClick={(e) => {
                            e.stopPropagation();
                            openFavoritePreview(r);
                          }}
                        >
                          <CaretRightOutlined />
                        </button>
                      </>
                    ) : null}
                  </div>
                  <button
                    type="button"
                    data-browse-card-action
                    className={browseStyles.gridSelectBtn}
                    data-selected={isCardSelected ? "" : undefined}
                    aria-label={isCardSelected ? t("pages.browse.aria_deselect") : t("pages.browse.aria_select")}
                    aria-pressed={isCardSelected}
                    onClick={(e) => {
                      e.stopPropagation();
                      toggleSelect(r.id);
                    }}
                  >
                    {isCardSelected ? <CheckOutlined /> : null}
                  </button>
                </div>
                <div
                  className={musicStyles.albumMeta}
                  role="link"
                  tabIndex={0}
                  onClick={() => nav(`/detail/${r.id}`)}
                  onKeyDown={(e) => e.key === "Enter" && nav(`/detail/${r.id}`)}
                >
                  <div className={musicStyles.albumTitle} title={r.title || t("pages.favorites.untitled")}>
                    {r.title || t("pages.favorites.untitled")}
                  </div>
                  <div className={musicStyles.albumArtist} title={subtitle}>
                    {subtitle}
                  </div>
                </div>
              </div>
            );
          })}
        </div>
      ) : (
        <div
          className={
            isPhotoGrid ? styles.photoGrid : viewMode === "poster" ? styles.posterGrid : styles.thumbGrid
          }
        >
          {sortedRows.map((r) => {
            const isCardSelected = selectedIds.has(r.id);
            const coverClass = isPhotoGrid
              ? styles.photoImage
              : viewMode === "poster"
                ? styles.posterImage
                : styles.thumbImage;
            const cardClass = isPhotoGrid
              ? styles.photoCard
              : viewMode === "poster"
                ? styles.posterCard
                : styles.thumbCard;
            return (
              <div key={r.id} className={cardClass}>
                <div
                  className={coverClass}
                  data-selected={isCardSelected ? "" : undefined}
                  data-bulk-pick={bulkPick ? "" : undefined}
                  tabIndex={0}
                  onClick={(e) => {
                    if ((e.target as HTMLElement).closest("[data-browse-card-action]")) return;
                    if (bulkPick) {
                      toggleSelect(r.id);
                      return;
                    }
                    if (isPhotoGrid) {
                      openFavoritePreview(r);
                      return;
                    }
                    nav(`/detail/${r.id}`);
                  }}
                  onKeyDown={(e) => {
                    if (e.key === "Enter" || e.key === " ") {
                      e.preventDefault();
                      if (bulkPick) toggleSelect(r.id);
                      else if (isPhotoGrid) openFavoritePreview(r);
                      else nav(`/detail/${r.id}`);
                    }
                  }}
                >
                  {isPhotoGrid ? (
                    <img
                      src={photoThumbSrc(r.id)}
                      alt=""
                      className={styles.photoCoverImg}
                      loading="lazy"
                      decoding="async"
                      onLoadStart={(e) => {
                        e.currentTarget.parentElement?.removeAttribute("data-cover-loaded");
                      }}
                      onLoad={(e) => {
                        e.currentTarget.parentElement?.setAttribute("data-cover-loaded", "");
                      }}
                      onError={(e) => {
                        e.currentTarget.style.display = "none";
                        e.currentTarget.parentElement?.removeAttribute("data-cover-loaded");
                      }}
                    />
                  ) : (
                    <MediaPosterImg
                      item={r}
                      className={styles.gridCoverImg}
                      onLoadStart={(e) => {
                        e.currentTarget.parentElement?.removeAttribute("data-cover-loaded");
                      }}
                      onLoad={(e) => {
                        e.currentTarget.parentElement?.setAttribute("data-cover-loaded", "");
                      }}
                    />
                  )}
                  <div className={styles.gridHoverShade} aria-hidden={bulkPick ? true : undefined}>
                    {!bulkPick ? (
                      <>
                        <button
                          type="button"
                          data-browse-card-action
                          className={`${styles.gridCornerBtn} ${styles.gridEditBtn}`}
                          aria-label={t("pages.browse.aria_edit")}
                          onClick={(e) => {
                            e.stopPropagation();
                            nav(`/detail/${r.id}`);
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
                            openFavoritePreview(r);
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
                      toggleSelect(r.id);
                    }}
                  >
                    {isCardSelected ? <CheckOutlined /> : null}
                  </button>
                </div>
                <div className={styles.cardBody}>
                  <div className={styles.cardTitle}>{r.title || t("pages.favorites.untitled")}</div>
                </div>
              </div>
            );
          })}
        </div>
      )}
          </div>
        </div>
      )}
      {addToPlaylistMediaId != null && (
        <AddToPlaylistModal
          mediaIds={[addToPlaylistMediaId]}
          open
          defaultNewPlaylistName={addToPlaylistTarget?.title ?? ""}
          onClose={() => setAddToPlaylistMediaId(null)}
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
          onAdded={(folder) => {
            rememberFolderMenuAdded(folder);
            if (selectedFolderId === folder.id) void refreshFolderItems();
            void reloadFolders();
          }}
        />
      )}
      {folderForm ? (
        <FavoriteFolderFormModal
          open
          mode={folderForm.mode}
          initialName={folderForm.mode === "edit" ? folderForm.folder.name : ""}
          submitting={folderFormSubmitting}
          onClose={() => setFolderForm(null)}
          onSubmit={handleFolderFormSubmit}
        />
      ) : null}
      <AddToFavoriteFolderModal
        open={addVideoFolderId != null}
        folderId={addVideoFolderId}
        candidates={videoFavorites}
        onClose={() => setAddVideoFolderId(null)}
        onAdded={() => void refreshFolderItems()}
      />
      {photoLightboxIndex != null && photoFavorites.length > 0 ? (
        <PhotoLightbox
          items={photoFavorites}
          index={photoLightboxIndex}
          onClose={() => setPhotoLightboxIndex(null)}
          onChangeIndex={setPhotoLightboxIndex}
        />
      ) : null}
    </div>
  );
}
