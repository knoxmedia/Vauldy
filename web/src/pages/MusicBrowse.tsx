import {
  AppstoreOutlined,
  ArrowDownOutlined,
  ArrowUpOutlined,
  CheckOutlined,
  CloseOutlined,
  EditOutlined,
  EllipsisOutlined,
  PlayCircleOutlined,
  TableOutlined,
  UnorderedListOutlined,
} from "@ant-design/icons";
import type { MenuProps } from "antd";
import { Button, Dropdown, Empty, Input, Select, Space, Spin, Tabs, message } from "antd";
import { useCallback, useEffect, useMemo, useRef, useState, type ReactNode } from "react";
import { useNavigate } from "react-router-dom";
import {
  AlbumSummary,
  ArtistSummary,
  GenreSummary,
  MediaItem,
  MusicTrackRow,
  addFavorite,
  addPlaylistItem,
  albumArtworkSrc,
  createScrapeTasks,
  fetchAlbum,
  fetchGenreAlbums,
  fetchLibraryAlbums,
  fetchLibraryArtists,
  fetchLibraryGenres,
  fetchLibraryTracks,
  transcodeAsync,
} from "../api/client";
import AddToFavoriteFolderPickerModal from "../components/AddToFavoriteFolderPickerModal";
import AddToPlaylistModal from "../components/AddToPlaylistModal";
import MediaMatchModal from "../components/MediaMatchModal";
import MusicArtistIcon from "../components/MusicArtistIcon";
import MusicPosterPlaceholderIcon from "../components/MusicPosterPlaceholderIcon";
import MusicTrackList from "../components/MusicTrackList";
import {
  buildMusicBrowseMenuItems,
  confirmDeleteMusicBrowseEntity,
} from "../components/musicBrowseMenuItems";
import { buildMusicTrackMenuItems } from "../components/musicTrackMenuItems";
import {
  libraryTracksToQueue,
  storeQueueSession,
  type MusicQueueItem,
} from "../lib/albumPlayback";
import { useFavoriteFolderMenuRecents } from "../lib/useFavoriteFolderMenuRecents";
import { readRecentPlaylists, rememberPlaylistAdded, type RecentPlaylistEntry } from "../lib/recentPlaylists";
import { useMusicPlayerStore } from "../store/musicPlayer";
import { useT } from "../i18n";
import styles from "./Browse.module.css";
import musicStyles from "./MusicBrowse.module.css";

type ViewMode = "grid" | "table";
type MusicTab = "albums" | "artists" | "genres" | "tracks";
type SortField = "title" | "year" | "artist";
type SortOrder = "asc" | "desc";
type MusicCardKind = "album" | "artist" | "genre";

const VIEW_MODE_KEY = "knox.music.viewMode.v1";

function musicSelectionKey(kind: MusicCardKind, id: string | number): string {
  return `${kind}:${id}`;
}

function parseSelectionKey(key: string): { kind: MusicCardKind; id: string } {
  const idx = key.indexOf(":");
  return { kind: key.slice(0, idx) as MusicCardKind, id: key.slice(idx + 1) };
}

function insertQueueNext(items: MusicQueueItem[]): boolean {
  const filtered = items.filter((q) => q.mediaId > 0);
  if (filtered.length === 0) return false;
  const st = useMusicPlayerStore.getState();
  if (!st.active || st.queue.length === 0) {
    st.playQueue(filtered, 0);
    return true;
  }
  const head = st.queue.slice(0, st.queueIndex + 1);
  const tail = st.queue.slice(st.queueIndex + 1);
  const newQueue = [...head, ...filtered, ...tail];
  storeQueueSession(newQueue);
  useMusicPlayerStore.setState({ queue: newQueue, active: true, albumId: filtered[0]?.albumId ?? st.albumId });
  return true;
}

type Props = {
  libraryId: number;
  libraryName?: string;
  onEmpty?: () => void;
};

function readViewMode(): ViewMode {
  try {
    const v = localStorage.getItem(VIEW_MODE_KEY);
    if (v === "grid" || v === "table") return v;
  } catch {
    /* ignore */
  }
  return "grid";
}

export default function MusicBrowse({ libraryId, libraryName, onEmpty }: Props) {
  const nav = useNavigate();
  const t = useT();
  const [tab, setTab] = useState<MusicTab>("albums");
  const [viewMode, setViewMode] = useState<ViewMode>(() => readViewMode());
  const [loading, setLoading] = useState(false);
  const [q, setQ] = useState("");
  const [sortField, setSortField] = useState<SortField>("title");
  const [sortOrder, setSortOrder] = useState<SortOrder>("asc");
  const [albums, setAlbums] = useState<AlbumSummary[]>([]);
  const [artists, setArtists] = useState<ArtistSummary[]>([]);
  const [genres, setGenres] = useState<GenreSummary[]>([]);
  const [tracks, setTracks] = useState<MusicTrackRow[]>([]);
  const [playingId, setPlayingId] = useState<number | null>(null);
  const [addToPlaylistMediaId, setAddToPlaylistMediaId] = useState<number | null>(null);
  const [bulkPlaylistMediaIds, setBulkPlaylistMediaIds] = useState<number[] | null>(null);
  const [addToFavoriteFolderMediaId, setAddToFavoriteFolderMediaId] = useState<number | null>(null);
  const [matchMedia, setMatchMedia] = useState<MediaItem | null>(null);
  const [selectedKeys, setSelectedKeys] = useState<Set<string>>(() => new Set());
  const [recentPlaylistMenu, setRecentPlaylistMenu] = useState<RecentPlaylistEntry[]>(() => readRecentPlaylists());
  const { rememberFolderMenuAdded } = useFavoriteFolderMenuRecents();
  const onEmptyRef = useRef(onEmpty);
  const onEmptyCalledRef = useRef(false);

  onEmptyRef.current = onEmpty;

  useEffect(() => {
    onEmptyCalledRef.current = false;
  }, [libraryId]);

  useEffect(() => {
    localStorage.setItem(VIEW_MODE_KEY, viewMode);
  }, [viewMode]);

  const reloadLibrary = useCallback(async () => {
    setLoading(true);
    try {
      const [albumRows, artistRows, genreRows, trackRows] = await Promise.all([
        fetchLibraryAlbums(libraryId),
        fetchLibraryArtists(libraryId),
        fetchLibraryGenres(libraryId),
        fetchLibraryTracks(libraryId),
      ]);
      setAlbums(albumRows);
      setArtists(artistRows);
      setGenres(genreRows);
      setTracks(trackRows);
    } catch (e: unknown) {
      message.error((e as Error).message || t("pages.music_browse.load_failed"));
    } finally {
      setLoading(false);
    }
  }, [libraryId, t]);

  useEffect(() => {
    let cancelled = false;
    (async () => {
      setLoading(true);
      try {
        const [albumRows, artistRows, genreRows, trackRows] = await Promise.all([
          fetchLibraryAlbums(libraryId),
          fetchLibraryArtists(libraryId),
          fetchLibraryGenres(libraryId),
          fetchLibraryTracks(libraryId),
        ]);
        if (cancelled) return;
        if (albumRows.length === 0 && trackRows.length === 0 && !onEmptyCalledRef.current) {
          onEmptyCalledRef.current = true;
          onEmptyRef.current?.();
        }
        setAlbums(albumRows);
        setArtists(artistRows);
        setGenres(genreRows);
        setTracks(trackRows);
      } catch (e: unknown) {
        if (!cancelled) message.error((e as Error).message || t("pages.music_browse.load_failed"));
      } finally {
        if (!cancelled) setLoading(false);
      }
    })();
    return () => {
      cancelled = true;
    };
  }, [libraryId, t]);

  const needle = q.trim().toLowerCase();

  const filteredAlbums = useMemo(() => {
    let list = albums;
    if (needle) {
      list = list.filter(
        (a) =>
          (a.title || "").toLowerCase().includes(needle) ||
          (a.album_artist || "").toLowerCase().includes(needle),
      );
    }
    return [...list].sort((a, b) => {
      const factor = sortOrder === "asc" ? 1 : -1;
      if (sortField === "year") return ((a.year ?? 0) - (b.year ?? 0)) * factor;
      if (sortField === "artist") {
        return (a.album_artist || "").localeCompare(b.album_artist || "", "zh") * factor;
      }
      return (a.title || "").localeCompare(b.title || "", "zh") * factor;
    });
  }, [albums, needle, sortField, sortOrder]);

  const filteredArtists = useMemo(() => {
    if (!needle) return artists;
    return artists.filter((a) => (a.name || "").toLowerCase().includes(needle));
  }, [artists, needle]);

  const filteredGenres = useMemo(() => {
    if (!needle) return genres;
    return genres.filter((g) => (g.genre || "").toLowerCase().includes(needle));
  }, [genres, needle]);

  const filteredTracks = useMemo(() => {
    if (!needle) return tracks;
    return tracks.filter(
      (t) =>
        (t.title || "").toLowerCase().includes(needle) ||
        (t.artist || "").toLowerCase().includes(needle) ||
        (t.album_title || "").toLowerCase().includes(needle),
    );
  }, [tracks, needle]);

  useEffect(() => {
    setSelectedKeys(new Set());
  }, [tab]);

  function toggleSelect(key: string) {
    setSelectedKeys((prev) => {
      const next = new Set(prev);
      if (next.has(key)) next.delete(key);
      else next.add(key);
      return next;
    });
  }

  const tracksForArtist = useCallback(
    (artistId: number): MusicTrackRow[] => {
      const artist = artists.find((a) => a.id === artistId);
      const name = (artist?.name || "").trim().toLowerCase();
      return tracks.filter(
        (t) =>
          t.artist_id === artistId ||
          (name && (t.artist || t.album_artist || "").trim().toLowerCase() === name),
      );
    },
    [artists, tracks],
  );

  const tracksForGenre = useCallback(
    async (genre: string): Promise<MusicTrackRow[]> => {
      const resp = await fetchGenreAlbums(libraryId, genre);
      const albumIds = new Set((resp.items ?? []).map((a) => a.id));
      return tracks.filter((t) => t.album_id && albumIds.has(t.album_id));
    },
    [libraryId, tracks],
  );

  const [genreMediaIds, setGenreMediaIds] = useState<Record<string, number[]>>({});

  useEffect(() => {
    if (genres.length === 0) {
      setGenreMediaIds({});
      return;
    }
    let cancelled = false;
    void (async () => {
      const map: Record<string, number[]> = {};
      for (const g of genres) {
        const rows = await tracksForGenre(g.genre);
        map[g.genre] = rows.map((t) => t.media_id).filter((id) => id > 0);
      }
      if (!cancelled) setGenreMediaIds(map);
    })();
    return () => {
      cancelled = true;
    };
  }, [genres, tracksForGenre]);

  async function refreshMetadataForIds(ids: number[]) {
    if (ids.length === 0) return;
    try {
      await createScrapeTasks(ids);
      message.success(t("components.media_menu.scrape_task_created"));
    } catch {
      message.error(t("components.media_menu.operation_failed"));
    }
  }

  async function analyzeMediaIds(ids: number[]) {
    if (ids.length === 0) return;
    let ok = 0;
    for (const id of ids) {
      try {
        await transcodeAsync(id, "analyze");
        ok++;
      } catch {
        /* skip */
      }
    }
    if (ok > 0) message.success(t("components.media_menu.analyze_task_created"));
    else message.error(t("components.media_menu.operation_failed"));
  }

  const bulkPick = selectedKeys.size > 0;
  const selectionCount = selectedKeys.size;

  function clearSelection() {
    setSelectedKeys(new Set());
  }

  const resolveAllSelectedMediaIds = useCallback(async (): Promise<number[]> => {
    const out = new Set<number>();
    for (const key of selectedKeys) {
      const { kind, id } = parseSelectionKey(key);
      if (kind === "album") {
        tracks.filter((t) => t.album_id === Number(id)).forEach((t) => out.add(t.media_id));
      } else if (kind === "artist") {
        tracksForArtist(Number(id)).forEach((t) => out.add(t.media_id));
      } else if (kind === "genre") {
        const rows = await tracksForGenre(id);
        rows.forEach((t) => out.add(t.media_id));
      }
    }
    return [...out].filter((mid) => mid > 0);
  }, [selectedKeys, tracks, tracksForArtist, tracksForGenre]);

  async function playAllSelected() {
    const mediaIds = await resolveAllSelectedMediaIds();
    const queue = libraryTracksToQueue(tracks.filter((t) => mediaIds.includes(t.media_id)));
    if (queue.length === 0) {
      message.warning(t("pages.artist_detail.album_no_tracks_rescan"));
      return;
    }
    useMusicPlayerStore.getState().playQueue(queue, 0);
  }

  async function bulkAddSelectedToCollection() {
    const mediaIds = await resolveAllSelectedMediaIds();
    if (mediaIds.length === 0) return;
    let ok = 0;
    let fail = 0;
    for (const id of mediaIds) {
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

  async function bulkDeleteSelected() {
    const mediaIds = await resolveAllSelectedMediaIds();
    confirmDeleteMusicBrowseEntity(
      t("pages.music_browse.bulk_delete_label", { count: selectionCount }),
      mediaIds,
      () => {
        clearSelection();
        void reloadLibrary();
      },
    );
  }

  const musicBulkAddMenuItems = useMemo((): MenuProps["items"] => {
    return [
      { key: "bulkAddCollection", label: t("pages.browse.bulk_add_collection") },
      { type: "divider" },
      { key: "bulkOpenPlaylist", label: t("pages.browse.bulk_open_playlist") },
    ];
  }, [t]);

  const musicBulkMoreMenuItems = useMemo((): MenuProps["items"] => {
    return [
      { key: "playAll", label: t("pages.music_browse.menu_play_all") },
      { key: "refreshMetadata", label: t("components.media_menu.refresh_metadata") },
      { key: "delete", label: t("components.media_menu.delete"), danger: true },
    ];
  }, [t]);

  async function onMusicBulkAddMenuClick(key: string) {
    if (key === "bulkAddCollection") {
      void bulkAddSelectedToCollection();
      return;
    }
    if (key === "bulkOpenPlaylist") {
      const mediaIds = await resolveAllSelectedMediaIds();
      if (mediaIds.length > 0) setBulkPlaylistMediaIds(mediaIds);
    }
  }

  async function onMusicBulkMoreMenuClick(key: string) {
    const mediaIds = await resolveAllSelectedMediaIds();
    switch (key) {
      case "playAll":
        void playAllSelected();
        break;
      case "refreshMetadata":
        if (mediaIds.length > 0) void refreshMetadataForIds(mediaIds);
        break;
      case "delete":
        void bulkDeleteSelected();
        break;
      default:
        break;
    }
  }

  async function queueFromTracks(rows: MusicTrackRow[], mode: "all" | "next", albumId = 0) {
    const queue = libraryTracksToQueue(rows);
    if (queue.length === 0) {
      message.warning(t("pages.artist_detail.album_no_tracks_rescan"));
      return;
    }
    if (mode === "all") {
      if (albumId > 0) useMusicPlayerStore.getState().loadAlbum(albumId, queue, 0, { sequential: true });
      else useMusicPlayerStore.getState().playQueue(queue, 0);
      return;
    }
    if (insertQueueNext(queue)) {
      message.success(t("pages.music_browse.queued_next", { count: queue.length }));
    }
  }

  async function playAlbumAll(albumId: number) {
    if (playingId != null) return;
    setPlayingId(albumId);
    try {
      const album = await fetchAlbum(albumId);
      await queueFromTracks(album.tracks ?? [], "all", albumId);
    } catch (err: unknown) {
      message.error((err as Error).message || t("pages.artist_detail.cannot_play_album"));
    } finally {
      setPlayingId(null);
    }
  }

  async function playAlbumNext(albumId: number) {
    try {
      const album = await fetchAlbum(albumId);
      await queueFromTracks(album.tracks ?? [], "next", albumId);
    } catch (err: unknown) {
      message.error((err as Error).message || t("pages.artist_detail.cannot_play_album"));
    }
  }

  const quickAddToPlaylist = useCallback(
    async (mediaId: number, playlistId: number) => {
      try {
        await addPlaylistItem(playlistId, mediaId);
        const name =
          recentPlaylistMenu.find((p) => p.id === playlistId)?.name ??
          readRecentPlaylists().find((p) => p.id === playlistId)?.name ??
          t("pages.album_detail.playlist_fallback");
        message.success(t("pages.album_detail.added_to_playlist", { name }));
        rememberPlaylistAdded({ id: playlistId, name });
        setRecentPlaylistMenu(readRecentPlaylists());
      } catch {
        message.error(t("pages.album_detail.add_failed_duplicate"));
      }
    },
    [recentPlaylistMenu, t],
  );

  const buildCardMenu = useCallback(
    (
      title: string,
      mediaIds: number[],
      handlers: {
        onPlayAll: () => void | Promise<void>;
        onPlayNext: () => void | Promise<void>;
        onViewHistory: () => void;
        onDelete: () => void;
      },
    ): MenuProps =>
      buildMusicBrowseMenuItems({
        ...handlers,
        mediaIds,
        primaryMediaId: mediaIds[0],
        onAddToPlaylist: setAddToPlaylistMediaId,
        recentPlaylists: recentPlaylistMenu,
        onQuickAddToPlaylist: quickAddToPlaylist,
        onAddToFavoriteFolder: setAddToFavoriteFolderMediaId,
        onRefreshMetadata: refreshMetadataForIds,
        onAnalyze: analyzeMediaIds,
        onMatch: (mediaId) => {
          const tr = tracks.find((x) => x.media_id === mediaId);
          setMatchMedia({
            id: mediaId,
            library_id: libraryId,
            file_id: "",
            title: tr?.title || title,
            file_path: tr?.file_path || "",
            file_type: "audio",
            duration: tr?.duration ?? 0,
            width: 0,
            height: 0,
            format: tr?.format || "",
            status: "active",
          });
        },
      }),
    [libraryId, nav, quickAddToPlaylist, recentPlaylistMenu, tracks, t],
  );

  const renderMusicGridCard = useCallback(
    (opts: {
      kind: MusicCardKind;
      id: string | number;
      title: string;
      subtitle: string;
      coverClassName?: string;
      href: string;
      onEdit: () => void;
      menu: MenuProps;
      cover?: ReactNode;
    }) => {
      const selKey = musicSelectionKey(opts.kind, opts.id);
      const isSelected = selectedKeys.has(selKey);
      const isArtist = opts.kind === "artist";

      if (isArtist) {
        return (
          <div key={selKey} className={`${musicStyles.albumCard} ${musicStyles.artistCard}`}>
            <div
              className={`${musicStyles.albumCover} ${styles.musicBrowseCover} ${opts.coverClassName ?? ""}`}
              role="link"
              tabIndex={0}
              aria-label={opts.title}
              onClick={(e) => {
                if ((e.target as HTMLElement).closest("[data-music-card-action]")) return;
                nav(opts.href);
              }}
              onKeyDown={(e) => {
                if (e.key === "Enter" || e.key === " ") {
                  e.preventDefault();
                  nav(opts.href);
                }
              }}
            >
              {opts.cover}
              <div className={musicStyles.artistEditOnCover}>
                <button
                  type="button"
                  data-music-card-action
                  className={musicStyles.artistEditBtn}
                  aria-label={t("pages.music_browse.aria_edit")}
                  onClick={(e) => {
                    e.stopPropagation();
                    opts.onEdit();
                  }}
                >
                  <EditOutlined />
                </button>
              </div>
            </div>
            <div
              className={musicStyles.albumMeta}
              role="link"
              tabIndex={0}
              onClick={() => nav(opts.href)}
              onKeyDown={(e) => e.key === "Enter" && nav(opts.href)}
            >
              <div className={musicStyles.albumTitle} title={opts.title}>
                {opts.title}
              </div>
              <div className={musicStyles.albumArtist} title={opts.subtitle}>
                {opts.subtitle}
              </div>
            </div>
          </div>
        );
      }

      return (
        <div key={selKey} className={musicStyles.albumCard}>
          <div
            className={`${musicStyles.albumCover} ${styles.musicBrowseCover} ${opts.coverClassName ?? ""}`}
            data-selected={isSelected ? "" : undefined}
            data-bulk-pick={bulkPick ? "" : undefined}
            role="link"
            tabIndex={0}
            aria-label={opts.title}
            onClick={(e) => {
              if ((e.target as HTMLElement).closest("[data-music-card-action]")) return;
              if (bulkPick) {
                toggleSelect(selKey);
                return;
              }
              nav(opts.href);
            }}
            onKeyDown={(e) => {
              if (e.key === "Enter" || e.key === " ") {
                e.preventDefault();
                if (bulkPick) toggleSelect(selKey);
                else nav(opts.href);
              }
            }}
          >
            {opts.cover}
            <div className={styles.gridHoverShade} aria-hidden={bulkPick ? true : undefined}>
              {!bulkPick ? (
                <>
                  <button
                    type="button"
                    data-music-card-action
                    className={`${styles.gridCornerBtn} ${styles.gridEditBtn}`}
                    aria-label={t("pages.music_browse.aria_edit")}
                    onClick={(e) => {
                      e.stopPropagation();
                      opts.onEdit();
                    }}
                  >
                    <EditOutlined />
                  </button>
                  <div className={styles.gridMoreCorner} data-music-card-action>
                    <Dropdown menu={opts.menu} trigger={["click"]} placement="bottomRight">
                      <Button
                        type="text"
                        size="small"
                        className={styles.gridMoreIconBtn}
                        icon={<EllipsisOutlined rotate={90} />}
                        aria-label={t("pages.music_browse.aria_more")}
                        onClick={(e) => e.stopPropagation()}
                      />
                    </Dropdown>
                  </div>
                </>
              ) : null}
            </div>
            <button
              type="button"
              data-music-card-action
              className={styles.gridSelectBtn}
              data-selected={isSelected ? "" : undefined}
              aria-label={isSelected ? t("pages.music_browse.aria_deselect") : t("pages.music_browse.aria_select")}
              aria-pressed={isSelected}
              onClick={(e) => {
                e.stopPropagation();
                toggleSelect(selKey);
              }}
            >
              {isSelected ? <CheckOutlined /> : null}
            </button>
          </div>
          <div
            className={musicStyles.albumMeta}
            role="link"
            tabIndex={0}
            onClick={() => nav(opts.href)}
            onKeyDown={(e) => e.key === "Enter" && nav(opts.href)}
          >
            <div className={musicStyles.albumTitle} title={opts.title}>
              {opts.title}
            </div>
            <div className={musicStyles.albumArtist} title={opts.subtitle}>
              {opts.subtitle}
            </div>
          </div>
        </div>
      );
    },
    [bulkPick, nav, selectedKeys, t],
  );

  function playTrackFromList(mediaId: number, orderedTracks?: MusicTrackRow[]) {
    const source = orderedTracks ?? filteredTracks;
    const queue = libraryTracksToQueue(source);
    const idx = queue.findIndex((q) => q.mediaId === mediaId);
    if (idx < 0 || queue.length === 0) {
      message.warning(t("pages.music_browse.cannot_play_track"));
      return;
    }
    useMusicPlayerStore.getState().playQueue(queue, idx);
  }

  const buildTrackMenu = useCallback(
    (track: MusicTrackRow) =>
      buildMusicTrackMenuItems(
        { media_id: track.media_id, title: track.title, file_path: track.file_path },
        nav,
        {
          onPlay: playTrackFromList,
          onAddToPlaylist: setAddToPlaylistMediaId,
          recentPlaylists: recentPlaylistMenu,
          onQuickAddToPlaylist: async (mediaId, playlistId) => {
            try {
              await addPlaylistItem(playlistId, mediaId);
              const name =
                recentPlaylistMenu.find((p) => p.id === playlistId)?.name ??
                readRecentPlaylists().find((p) => p.id === playlistId)?.name ??
                t("pages.album_detail.playlist_fallback");
              message.success(t("pages.album_detail.added_to_playlist", { name }));
              rememberPlaylistAdded({ id: playlistId, name });
              setRecentPlaylistMenu(readRecentPlaylists());
            } catch {
              message.error(t("pages.album_detail.add_failed_duplicate"));
            }
          },
          afterDelete: reloadLibrary,
        },
      ),
    [nav, recentPlaylistMenu, reloadLibrary, t],
  );

  const resolveTrackArtistId = useCallback(
    (track: MusicTrackRow): number | null => {
      if (track.artist_id && track.artist_id > 0) return track.artist_id;
      const name = (track.artist || track.album_artist || "").trim();
      if (!name) return null;
      const found = artists.find((a) => a.name.trim().toLowerCase() === name.toLowerCase());
      return found?.id ?? null;
    },
    [artists],
  );

  const countLabel = (() => {
    switch (tab) {
      case "artists":
        return t("pages.music_browse.count_artists", { count: filteredArtists.length });
      case "genres":
        return t("pages.music_browse.count_genres", { count: filteredGenres.length });
      case "tracks":
        return t("pages.music_browse.count_tracks", { count: filteredTracks.length });
      default:
        return t("pages.music_browse.count_albums", { count: filteredAlbums.length });
    }
  })();

  return (
    <div className={musicStyles.wrap}>
      <div className={styles.browsePageStickyHead}>
      <div className={musicStyles.header}>
        <div>
          <div className={musicStyles.libraryTitle}>{libraryName || t("pages.music_browse.library_fallback")}</div>
          <Tabs
            activeKey={tab}
            onChange={(k) => setTab(k as MusicTab)}
            className={musicStyles.tabs}
            items={[
              { key: "albums", label: t("pages.music_browse.tab_albums") },
              { key: "artists", label: t("pages.music_browse.tab_artists") },
              { key: "genres", label: t("pages.music_browse.tab_genres") },
              { key: "tracks", label: t("pages.music_browse.tab_tracks") },
            ]}
          />
        </div>
        <Space wrap className={musicStyles.toolbar}>
          <Input.Search
            allowClear
            placeholder={t("pages.music_browse.search_placeholder")}
            value={q}
            onChange={(e) => setQ(e.target.value)}
            style={{ width: 220 }}
          />
          {tab === "albums" && (
            <>
              <Select
                size="small"
                value={sortField}
                onChange={setSortField}
                options={[
                  { value: "title", label: t("pages.music_browse.sort_title") },
                  { value: "artist", label: t("pages.music_browse.sort_artist") },
                  { value: "year", label: t("pages.music_browse.sort_year") },
                ]}
                style={{ width: 130 }}
              />
              <Button size="small" onClick={() => setSortOrder((s) => (s === "asc" ? "desc" : "asc"))}>
                {sortOrder === "asc" ? <ArrowUpOutlined /> : <ArrowDownOutlined />}
              </Button>
              <div className={styles.viewModePicker}>
                <Button
                  type={viewMode === "grid" ? "primary" : "text"}
                  size="small"
                  icon={<AppstoreOutlined />}
                  onClick={() => setViewMode("grid")}
                  aria-label={t("pages.music_browse.view_grid_aria")}
                />
                <Button
                  type={viewMode === "table" ? "primary" : "text"}
                  size="small"
                  icon={<TableOutlined />}
                  onClick={() => setViewMode("table")}
                  aria-label={t("pages.music_browse.view_table_aria")}
                />
              </div>
            </>
          )}
          <span className={musicStyles.count}>{countLabel}</span>
        </Space>
      </div>

      {selectionCount > 0 && tab !== "tracks" && tab !== "artists" ? (
        <div className={styles.browseSelectionBar}>
          <div className={styles.browseSelectionBarLeft}>
            <CheckOutlined className={styles.browseSelectionCheckIcon} aria-hidden />
            <span>{t("pages.browse.selection_count", { count: selectionCount })}</span>
          </div>
          <div className={styles.browseSelectionBarCenter}>
            <Space size="middle">
              <Button
                type="text"
                className={styles.browseSelectionActionBtn}
                icon={<PlayCircleOutlined />}
                aria-label={t("pages.browse.selection_play_aria")}
                onClick={() => void playAllSelected()}
              />
              <Dropdown
                menu={{
                  items: musicBulkAddMenuItems,
                  onClick: ({ key, domEvent }) => {
                    domEvent.stopPropagation();
                    void onMusicBulkAddMenuClick(String(key));
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
                  items: musicBulkMoreMenuItems,
                  onClick: ({ key, domEvent }) => {
                    domEvent.stopPropagation();
                    void onMusicBulkMoreMenuClick(String(key));
                  },
                }}
                trigger={["click"]}
                placement="bottom"
              >
                <Button
                  type="text"
                  className={styles.browseSelectionActionBtn}
                  icon={<EllipsisOutlined />}
                  aria-label={t("pages.browse.selection_more_aria")}
                />
              </Dropdown>
            </Space>
          </div>
          <div className={styles.browseSelectionBarRight}>
            <Button
              type="text"
              className={styles.browseSelectionClearBtn}
              icon={<CloseOutlined />}
              onClick={clearSelection}
            >
              {t("pages.browse.deselect_all")}
            </Button>
          </div>
        </div>
      ) : null}

      </div>

      {loading && albums.length === 0 && artists.length === 0 && tracks.length === 0 ? (
        <div className={styles.loadingWrap}>
          <Spin />
        </div>
      ) : tab === "albums" ? (
        filteredAlbums.length === 0 ? (
          <Empty description={t("pages.music_browse.no_albums")} />
        ) : viewMode === "grid" ? (
          <div className={musicStyles.albumGrid}>
            {filteredAlbums.map((a) => {
              const albumTracks = tracks.filter((t) => t.album_id === a.id);
              const mediaIds = albumTracks.map((t) => t.media_id).filter((id) => id > 0);
              return renderMusicGridCard({
                kind: "album",
                id: a.id,
                title: a.title,
                subtitle: a.album_artist || t("pages.music_browse.various_artists"),
                href: `/album/${a.id}`,
                onEdit: () => nav(`/album/${a.id}`),
                menu: buildCardMenu(a.title, mediaIds, {
                  onPlayAll: () => playAlbumAll(a.id),
                  onPlayNext: () => playAlbumNext(a.id),
                  onViewHistory: () => nav(mediaIds[0] ? `/playback-history?media_id=${mediaIds[0]}` : "/playback-history"),
                  onDelete: () =>
                    confirmDeleteMusicBrowseEntity(a.title, mediaIds, () => reloadLibrary()),
                }),
                cover: (
                  <>
                    <img
                      className={musicStyles.albumCoverImg}
                      src={albumArtworkSrc(a.id)}
                      alt=""
                      loading="lazy"
                      onError={(e) => {
                        e.currentTarget.style.display = "none";
                        e.currentTarget.parentElement?.classList.add(musicStyles.noCover);
                      }}
                    />
                    <div className={musicStyles.noCoverIcon}>
                      <MusicPosterPlaceholderIcon />
                    </div>
                  </>
                ),
              });
            })}
          </div>
        ) : (
          <div className={musicStyles.tableWrap}>
            <table className={musicStyles.table}>
              <thead>
                <tr>
                  <th className={musicStyles.columnPickerCell} aria-hidden />
                  <th>{t("pages.music_browse.col_title")}</th>
                  <th>{t("pages.music_browse.col_album_artist")}</th>
                  <th>{t("pages.music_browse.col_year")}</th>
                  <th style={{ width: 72 }}>{t("pages.music_browse.col_track")}</th>
                  <th className={musicStyles.rowActionsCell} aria-label={t("pages.music_browse.aria_more")} />
                </tr>
              </thead>
              <tbody>
                {filteredAlbums.map((a) => {
                  const selKey = musicSelectionKey("album", a.id);
                  const isSelected = selectedKeys.has(selKey);
                  const albumTracks = tracks.filter((tr) => tr.album_id === a.id);
                  const mediaIds = albumTracks.map((tr) => tr.media_id).filter((id) => id > 0);
                  const menu = buildCardMenu(a.title, mediaIds, {
                    onPlayAll: () => playAlbumAll(a.id),
                    onPlayNext: () => playAlbumNext(a.id),
                    onViewHistory: () =>
                      nav(mediaIds[0] ? `/playback-history?media_id=${mediaIds[0]}` : "/playback-history"),
                    onDelete: () => confirmDeleteMusicBrowseEntity(a.title, mediaIds, () => reloadLibrary()),
                  });
                  return (
                    <tr
                      key={a.id}
                      className={musicStyles.trackRow}
                      data-selected={isSelected ? "" : undefined}
                      data-bulk-pick={bulkPick ? "" : undefined}
                      onClick={(e) => {
                        if ((e.target as HTMLElement).closest("[data-music-table-action]")) return;
                        if (bulkPick) {
                          toggleSelect(selKey);
                          return;
                        }
                        nav(`/album/${a.id}`);
                      }}
                    >
                      <td className={musicStyles.columnPickerCell}>
                        <button
                          type="button"
                          className={musicStyles.trackGutterSelect}
                          data-music-table-action
                          aria-label={
                            isSelected ? t("pages.music_browse.aria_deselect") : t("pages.music_browse.aria_select")
                          }
                          aria-pressed={isSelected}
                          data-selected={isSelected ? "" : undefined}
                          onClick={(e) => {
                            e.stopPropagation();
                            toggleSelect(selKey);
                          }}
                        >
                          {isSelected ? <CheckOutlined /> : null}
                        </button>
                      </td>
                      <td>
                        <span className={musicStyles.tableTitle}>{a.title}</span>
                      </td>
                      <td>{a.album_artist || "—"}</td>
                      <td>{a.year || "—"}</td>
                      <td>{a.track_count ?? "—"}</td>
                      <td className={musicStyles.rowActionsCell}>
                        {!bulkPick ? (
                          <div className={musicStyles.rowActions}>
                            <Dropdown menu={menu} trigger={["click"]} placement="bottomRight">
                              <Button
                                type="text"
                                size="small"
                                data-music-table-action
                                className={musicStyles.rowActionBtn}
                                icon={<EllipsisOutlined rotate={90} />}
                                aria-label={t("pages.music_browse.aria_more")}
                                onClick={(e) => e.stopPropagation()}
                              />
                            </Dropdown>
                          </div>
                        ) : null}
                      </td>
                    </tr>
                  );
                })}
              </tbody>
            </table>
          </div>
        )
      ) : tab === "artists" ? (
        filteredArtists.length === 0 ? (
          <Empty description={t("pages.music_browse.no_artists")} />
        ) : (
          <div className={musicStyles.albumGrid}>
            {filteredArtists.map((a) => {
              const artistTracks = tracksForArtist(a.id);
              const mediaIds = artistTracks.map((t) => t.media_id).filter((id) => id > 0);
              return renderMusicGridCard({
                kind: "artist",
                id: a.id,
                title: a.name,
                subtitle: t("pages.music_browse.list_albums_tracks", {
                  albums: a.album_count ?? 0,
                  tracks: a.track_count ?? 0,
                }),
                coverClassName: musicStyles.artistCover,
                href: `/artist/${a.id}`,
                onEdit: () => nav(`/artist/${a.id}`),
                menu: buildCardMenu(a.name, mediaIds, {
                  onPlayAll: () => queueFromTracks(artistTracks, "all"),
                  onPlayNext: () => queueFromTracks(artistTracks, "next"),
                  onViewHistory: () => nav(mediaIds[0] ? `/playback-history?media_id=${mediaIds[0]}` : "/playback-history"),
                  onDelete: () =>
                    confirmDeleteMusicBrowseEntity(a.name, mediaIds, () => reloadLibrary()),
                }),
                cover: (
                  <div className={musicStyles.musicCardIcon}>
                    <MusicArtistIcon />
                  </div>
                ),
              });
            })}
          </div>
        )
      ) : tab === "genres" ? (
        filteredGenres.length === 0 ? (
          <Empty description={t("pages.music_browse.no_genres")} />
        ) : (
          <div className={musicStyles.albumGrid}>
            {filteredGenres.map((g) => {
              const genreKey = encodeURIComponent(g.genre);
              const mediaIds = genreMediaIds[g.genre] ?? [];
              return renderMusicGridCard({
                kind: "genre",
                id: g.genre,
                title: g.genre,
                subtitle: t("pages.music_browse.list_albums_tracks", {
                  albums: g.album_count ?? 0,
                  tracks: g.track_count ?? 0,
                }),
                href: `/genre?library=${libraryId}&name=${genreKey}`,
                onEdit: () => nav(`/genre?library=${libraryId}&name=${genreKey}`),
                menu: buildCardMenu(g.genre, mediaIds, {
                  onPlayAll: async () => {
                    const rows = await tracksForGenre(g.genre);
                    await queueFromTracks(rows, "all");
                  },
                  onPlayNext: async () => {
                    const rows = await tracksForGenre(g.genre);
                    await queueFromTracks(rows, "next");
                  },
                  onViewHistory: () =>
                    nav(mediaIds[0] ? `/playback-history?media_id=${mediaIds[0]}` : "/playback-history"),
                  onDelete: () =>
                    confirmDeleteMusicBrowseEntity(g.genre, mediaIds, () => reloadLibrary()),
                }),
                cover: (
                  <div className={musicStyles.musicCardIcon}>
                    <UnorderedListOutlined />
                  </div>
                ),
              });
            })}
          </div>
        )
      ) : filteredTracks.length === 0 ? (
        <Empty description={t("pages.music_browse.no_tracks")} />
      ) : (
        <MusicTrackList
          tracks={filteredTracks}
          onPlayTrack={playTrackFromList}
          resolveArtistId={resolveTrackArtistId}
          buildTrackMenu={buildTrackMenu}
        />
      )}
      {bulkPlaylistMediaIds != null && bulkPlaylistMediaIds.length > 0 && (
        <AddToPlaylistModal
          mediaIds={bulkPlaylistMediaIds}
          open
          onClose={() => setBulkPlaylistMediaIds(null)}
          onAdded={(pl) => {
            rememberPlaylistAdded(pl);
            setRecentPlaylistMenu(readRecentPlaylists());
          }}
        />
      )}
      {addToPlaylistMediaId != null ? (
        <AddToPlaylistModal
          mediaIds={[addToPlaylistMediaId]}
          open
          defaultNewPlaylistName={tracks.find((t) => t.media_id === addToPlaylistMediaId)?.title ?? ""}
          onClose={() => setAddToPlaylistMediaId(null)}
          onAdded={(pl) => {
            rememberPlaylistAdded(pl);
            setRecentPlaylistMenu(readRecentPlaylists());
          }}
        />
      ) : null}
      {addToFavoriteFolderMediaId != null && (
        <AddToFavoriteFolderPickerModal
          mediaId={addToFavoriteFolderMediaId}
          open
          onClose={() => setAddToFavoriteFolderMediaId(null)}
          onAdded={(folder) => rememberFolderMenuAdded(folder)}
        />
      )}
      {matchMedia != null && (
        <MediaMatchModal
          media={matchMedia}
          open
          onClose={() => setMatchMedia(null)}
          onMatched={() => {
            setMatchMedia(null);
            void reloadLibrary();
          }}
        />
      )}
    </div>
  );
}
