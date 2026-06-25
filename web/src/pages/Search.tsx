import {
  CheckCircleOutlined,
  CheckOutlined,
  CloseOutlined,
  PlayCircleOutlined,
  UnorderedListOutlined,
} from "@ant-design/icons";
import { Button, Dropdown, Empty, Space, Spin, message } from "antd";
import type { MenuProps } from "antd";
import { useCallback, useEffect, useMemo, useState } from "react";
import { useNavigate, useSearchParams } from "react-router-dom";
import {
  MediaItem,
  addFavorite,
  addFavoriteFolderItem,
  addPlaylistItem,
  fetchLibraries,
  fetchMedia,
  mediaPosterSrc,
  normalizeListPosterUrl,
  type Library,
  type MediaMatchListUpdate,
} from "../api/client";
import AddToFavoriteFolderPickerModal from "../components/AddToFavoriteFolderPickerModal";
import AddToPlaylistModal from "../components/AddToPlaylistModal";
import CirclePlayIcon from "../components/CirclePlayIcon";
import MediaMatchModal from "../components/MediaMatchModal";
import VerticalMoreIcon from "../components/VerticalMoreIcon";
import { buildMediaMenuItems } from "../components/mediaMenuItems";
import { useT, type TranslateFn } from "../i18n";
import { useFavoriteFolderMenuRecents } from "../lib/useFavoriteFolderMenuRecents";
import { MAX_RECENT_FAVORITE_FOLDERS } from "../lib/recentFavoriteFolders";
import { readRecentPlaylists, rememberPlaylistAdded } from "../lib/recentPlaylists";
import browseStyles from "./Browse.module.css";
import styles from "./Search.module.css";

type SearchFilter = "all" | "movie" | "tv" | "music" | "image" | "document";

const FILTER_SPECS: { key: SearchFilter; i18nKey: string }[] = [
  { key: "all", i18nKey: "pages.search.filter_all" },
  { key: "movie", i18nKey: "pages.search.filter_movie" },
  { key: "tv", i18nKey: "pages.search.filter_tv" },
  { key: "music", i18nKey: "pages.search.filter_music" },
  { key: "image", i18nKey: "pages.search.filter_photo" },
  { key: "document", i18nKey: "pages.search.filter_document" },
];

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

function matchesFilter(
  r: MediaItem,
  filter: SearchFilter,
  libraryTypeById: Map<number, string>,
): boolean {
  if (filter === "all") return true;
  const libType = libraryTypeById.get(r.library_id) || "";
  switch (filter) {
    case "movie":
      return r.file_type === "video" && libType === "movie";
    case "tv":
      return r.file_type === "video" && (libType === "tv" || libType === "anime");
    case "music":
      return r.file_type === "audio";
    case "image":
      return r.file_type === "image";
    case "document":
      return r.file_type === "document";
    default:
      return true;
  }
}

function mediaTypeLabel(r: MediaItem, libraryTypeById: Map<number, string>, t: TranslateFn): string {
  const libType = libraryTypeById.get(r.library_id) || "";
  if (libType === "movie") return t("pages.search.type_movie");
  if (libType === "tv") return t("pages.search.type_tv");
  if (libType === "anime") return t("pages.search.type_anime");
  if (libType === "music" || r.file_type === "audio") return t("pages.search.type_music");
  if (r.file_type === "image") return t("pages.search.type_photo");
  if (r.file_type === "document") return t("pages.search.type_document");
  if (r.file_type === "video") return t("pages.search.type_video");
  return r.file_type || "—";
}

function openMediaItem(r: MediaItem, nav: ReturnType<typeof useNavigate>) {
  if (r.file_type === "document") {
    nav(`/reader/${r.id}`);
    return;
  }
  if (r.file_type === "image") {
    nav(`/detail/${r.id}`);
    return;
  }
  nav(`/player/${r.id}`);
}

export default function SearchPage() {
  const nav = useNavigate();
  const t = useT();
  const [searchParams] = useSearchParams();
  const qParam = searchParams.get("q")?.trim() ?? "";
  const [rows, setRows] = useState<MediaItem[]>([]);
  const [libraries, setLibraries] = useState<Library[]>([]);
  const [loading, setLoading] = useState(false);
  const [typeFilter, setTypeFilter] = useState<SearchFilter>("all");
  const [selectedIds, setSelectedIds] = useState<Set<number>>(() => new Set());
  const [playlistModalMediaIds, setPlaylistModalMediaIds] = useState<number[] | null>(null);
  const [addToFavoriteFolderMediaId, setAddToFavoriteFolderMediaId] = useState<number | null>(null);
  const [matchMedia, setMatchMedia] = useState<MediaItem | null>(null);
  const [recentPlaylistMenu, setRecentPlaylistMenu] = useState(readRecentPlaylists);
  const { recentFavoriteFolders, rememberFolderMenuAdded } = useFavoriteFolderMenuRecents();

  const libraryTypeById = useMemo(() => {
    const map = new Map<number, string>();
    for (const lib of libraries) map.set(lib.id, lib.type);
    return map;
  }, [libraries]);

  useEffect(() => {
    setTypeFilter("all");
    setSelectedIds(new Set());
  }, [qParam]);

  useEffect(() => {
    setSelectedIds(new Set());
  }, [typeFilter]);

  async function load() {
    if (!qParam) {
      setRows([]);
      setLoading(false);
      return;
    }
    setLoading(true);
    try {
      const [items, libs] = await Promise.all([
        fetchMedia(undefined, { q: qParam, limit: 500 }),
        fetchLibraries(),
      ]);
      setRows(items);
      setLibraries(libs);
    } catch (e: unknown) {
      message.error((e as Error).message || t("pages.search.load_failed"));
      setRows([]);
    } finally {
      setLoading(false);
    }
  }

  useEffect(() => {
    void load();
  }, [qParam, t]);

  const filteredRows = useMemo(
    () => rows.filter((r) => matchesFilter(r, typeFilter, libraryTypeById)),
    [rows, typeFilter, libraryTypeById],
  );

  const visibleFilters = useMemo(() => {
    return FILTER_SPECS.filter((spec) => {
      if (spec.key === "all") return rows.length > 0;
      return rows.some((r) => matchesFilter(r, spec.key, libraryTypeById));
    });
  }, [rows, libraryTypeById]);

  const selectionCount = selectedIds.size;
  const bulkPick = selectionCount > 0;

  const firstSelectedId = useMemo(() => {
    for (const r of filteredRows) {
      if (selectedIds.has(r.id)) return r.id;
    }
    return undefined;
  }, [filteredRows, selectedIds]);

  const toggleSelect = useCallback((id: number) => {
    setSelectedIds((prev) => {
      const next = new Set(prev);
      if (next.has(id)) next.delete(id);
      else next.add(id);
      return next;
    });
  }, []);

  const clearSelection = useCallback(() => {
    setSelectedIds(new Set());
  }, []);

  function applyMediaMatchUpdate(update: MediaMatchListUpdate) {
    setRows((prev) =>
      prev.map((r) =>
        r.id === update.id
          ? {
              ...r,
              title: update.title || r.title,
              poster_url: update.poster_url ?? r.poster_url,
              year: update.year ?? r.year,
              release_date: update.release_date ?? r.release_date,
              scraped: update.scraped,
            }
          : r,
      ),
    );
  }

  function posterImgKey(r: MediaItem): string {
    return `${r.id}:${normalizeListPosterUrl(r.poster_url || "")}`;
  }

  function makeMenu(r: MediaItem): MenuProps {
    return buildMediaMenuItems(r, nav, {
      isWatched: r.completed === 1,
      showEncryptAsset: r.file_type === "video",
      encryptedAsset: !!r.encrypted_asset,
      afterEncryptAsset: () => load(),
      afterToggleWatched: () => load(),
      scraped: r.scraped,
      onOpenMatch: (mediaId) => {
        const item = rows.find((x) => x.id === mediaId) ?? r;
        setMatchMedia(item);
      },
      afterUnmatch: () => load(),
      afterDelete: () => {
        setSelectedIds((prev) => {
          if (!prev.has(r.id)) return prev;
          const next = new Set(prev);
          next.delete(r.id);
          return next;
        });
        return load();
      },
      onAddToPlaylist: (mediaId) => setPlaylistModalMediaIds([mediaId]),
      recentPlaylists: recentPlaylistMenu,
      onQuickAddToPlaylist: async (mediaId, playlistId) => {
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
      onAddToFavoriteFolder: (mediaId) => setAddToFavoriteFolderMediaId(mediaId),
      recentFavoriteFolders,
      onQuickAddToFavoriteFolder: async (mediaId, folderId) => {
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
          : t("pages.browse.added_to_favorites", { ok }),
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
          : t("pages.browse.added_to_playlist", { ok, name }),
      );
    } else {
      message.warning(t("pages.browse.playlist_add_failed"));
    }
  }

  const bulkAddMenuItems = useMemo((): MenuProps["items"] => {
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
  }, [recentFavoriteFolders, recentPlaylistMenu, t]);

  function onBulkAddMenuClick(key: string) {
    const ids = [...selectedIds];
    if (ids.length === 0) return;
    if (key === "bulkAddCollection") {
      void bulkAddSelectedToCollection(ids);
      return;
    }
    if (key.startsWith("recentFavoriteFolder:")) {
      const fid = Number(key.slice("recentFavoriteFolder:".length));
      if (!Number.isNaN(fid)) void bulkAddSelectedToFavoriteFolder(ids, fid);
      return;
    }
    if (key === "bulkOpenPlaylist") {
      setPlaylistModalMediaIds(ids);
      return;
    }
    if (key.startsWith("recentPlaylist:")) {
      const pid = Number(key.slice("recentPlaylist:".length));
      if (!Number.isNaN(pid)) void bulkAddSelectedToPlaylist(ids, pid);
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
          : t("pages.browse.added_to_favorite_folder", { ok, name }),
      );
    } else {
      message.warning(t("pages.browse.favorite_folder_add_failed"));
    }
  }

  const playlistModalDefaultTitle = useMemo(() => {
    if (playlistModalMediaIds == null || playlistModalMediaIds.length !== 1) return "";
    const id = playlistModalMediaIds[0];
    return rows.find((x) => x.id === id)?.title ?? "";
  }, [playlistModalMediaIds, rows]);

  return (
    <div className={styles.page}>
      {qParam ? (
        <h1 className={styles.resultsTitle}>{t("pages.search.top_results", { q: qParam })}</h1>
      ) : (
        <p style={{ color: "rgba(255,255,255,0.45)", marginTop: 0 }}>{t("pages.search.empty_hint")}</p>
      )}

      {qParam && visibleFilters.length > 0 ? (
        <div className={styles.filterBar}>
          <div className={styles.filterChips}>
            {visibleFilters.map((spec) => (
              <button
                key={spec.key}
                type="button"
                className={`${styles.filterChip}${typeFilter === spec.key ? ` ${styles.filterChipActive}` : ""}`}
                onClick={() => setTypeFilter(spec.key)}
              >
                {t(spec.i18nKey)}
              </button>
            ))}
          </div>
        </div>
      ) : null}

      {selectionCount > 0 ? (
        <div className={browseStyles.browseSelectionBar}>
          <div className={browseStyles.browseSelectionBarLeft}>
            <CheckOutlined className={browseStyles.browseSelectionCheckIcon} aria-hidden />
            <span>{t("pages.browse.selection_count", { count: selectionCount })}</span>
          </div>
          <div className={browseStyles.browseSelectionBarCenter}>
            <Space size="middle">
              <Button
                type="text"
                className={browseStyles.browseSelectionActionBtn}
                icon={<PlayCircleOutlined />}
                aria-label={t("pages.browse.selection_play_aria")}
                disabled={firstSelectedId == null}
                onClick={() => {
                  if (firstSelectedId != null) openMediaItem(rows.find((r) => r.id === firstSelectedId)!, nav);
                }}
              />
              <Button
                type="text"
                className={browseStyles.browseSelectionActionBtn}
                icon={<CheckCircleOutlined />}
                aria-label={t("pages.browse.selection_mark_aria")}
                onClick={() => message.info(t("pages.browse.mark_wip"))}
              />
              <Dropdown
                menu={{
                  items: bulkAddMenuItems,
                  onClick: ({ key, domEvent }) => {
                    domEvent.stopPropagation();
                    onBulkAddMenuClick(String(key));
                  },
                }}
                trigger={["click"]}
                placement="bottom"
              >
                <Button
                  type="text"
                  className={browseStyles.browseSelectionActionBtn}
                  icon={<UnorderedListOutlined />}
                  aria-label={t("pages.browse.selection_add_aria")}
                  onClick={(e) => e.stopPropagation()}
                />
              </Dropdown>
            </Space>
          </div>
          <div className={browseStyles.browseSelectionBarRight}>
            <Button
              type="text"
              className={browseStyles.browseSelectionClearBtn}
              icon={<CloseOutlined />}
              onClick={clearSelection}
            >
              {t("pages.browse.deselect_all")}
            </Button>
          </div>
        </div>
      ) : null}

      {loading ? (
        <div className={styles.loadingWrap}>
          <Spin />
        </div>
      ) : !qParam ? (
        <Empty description={t("pages.search.start_hint")} />
      ) : filteredRows.length === 0 ? (
        <Empty description={t("pages.search.no_match")} />
      ) : (
        <div className={`${browseStyles.listWrap} ${styles.searchListWrap}`}>
          {filteredRows.map((r) => {
            const isSelected = selectedIds.has(r.id);
            const typeLabel = mediaTypeLabel(r, libraryTypeById, t);
            const year = displayYear(r);
            return (
              <div
                key={r.id}
                className={`${browseStyles.listRow} ${styles.searchListRow}`}
                data-selected={isSelected ? "" : undefined}
                data-bulk-pick={bulkPick ? "" : undefined}
              >
                <div className={browseStyles.listSelectSlot}>
                  <button
                    type="button"
                    className={browseStyles.listSelectBtn}
                    aria-label={isSelected ? t("pages.browse.aria_deselect") : t("pages.browse.aria_select")}
                    aria-pressed={isSelected}
                    data-selected={isSelected ? "" : undefined}
                    onClick={(e) => {
                      e.stopPropagation();
                      toggleSelect(r.id);
                    }}
                  >
                    {isSelected ? <CheckOutlined /> : null}
                  </button>
                </div>
                <div
                  className={browseStyles.listRowMain}
                  tabIndex={0}
                  aria-label={
                    bulkPick
                      ? t("pages.browse.list_view_label", { title: r.title || t("pages.browse.untitled") })
                      : t("pages.browse.list_view_label_detail", { title: r.title || t("pages.browse.untitled") })
                  }
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
                    className={browseStyles.listPosterBlock}
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
                      className={browseStyles.listPosterInner}
                      data-selected={isSelected ? "" : undefined}
                    >
                      <img
                        key={posterImgKey(r)}
                        className={browseStyles.listPosterImg}
                        src={mediaPosterSrc(r)}
                        alt=""
                        loading="lazy"
                        decoding="async"
                        onError={(e) => {
                          e.currentTarget.style.display = "none";
                        }}
                      />
                    </div>
                  </div>
                  <div
                    className={browseStyles.listInfo}
                    onClick={bulkPick ? () => nav(`/detail/${r.id}`) : undefined}
                    style={bulkPick ? { cursor: "pointer" } : undefined}
                  >
                    <div className={browseStyles.listTitle}>{r.title || t("pages.browse.untitled")}</div>
                    <div className={browseStyles.listMeta}>
                      {year} · {typeLabel}
                      {r.file_type === "video" || r.file_type === "audio"
                        ? ` · ${fmtDurationLocalized(r.duration, t)}`
                        : null}
                    </div>
                  </div>
                </div>
                {!bulkPick ? (
                  <div className={styles.listActionsSlot}>
                    <button
                      type="button"
                      className={styles.listPlayBtn}
                      aria-label={t("pages.browse.aria_play")}
                      onClick={(e) => {
                        e.stopPropagation();
                        openMediaItem(r, nav);
                      }}
                    >
                      <CirclePlayIcon className={styles.listPlayIcon} size={24} />
                    </button>
                    <Dropdown menu={makeMenu(r)} trigger={["click"]} placement="bottomRight">
                      <button
                        type="button"
                        className={styles.listMoreBtn}
                        aria-label={t("pages.browse.aria_more")}
                        onClick={(e) => e.stopPropagation()}
                      >
                        <VerticalMoreIcon className={styles.listMoreIcon} size={24} />
                      </button>
                    </Dropdown>
                  </div>
                ) : null}
              </div>
            );
          })}
        </div>
      )}

      <AddToPlaylistModal
        open={playlistModalMediaIds != null}
        mediaIds={playlistModalMediaIds ?? []}
        defaultNewPlaylistName={playlistModalDefaultTitle}
        onClose={() => setPlaylistModalMediaIds(null)}
      />
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
        onMatched={applyMediaMatchUpdate}
      />
    </div>
  );
}
