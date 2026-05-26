import {
  AppstoreOutlined,
  ArrowDownOutlined,
  ArrowUpOutlined,
  CaretRightOutlined,
  CustomerServiceOutlined,
  TableOutlined,
  UnorderedListOutlined,
} from "@ant-design/icons";
import { Button, Empty, Input, Select, Space, Spin, Tabs, message } from "antd";
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { useNavigate } from "react-router-dom";
import {
  AlbumSummary,
  ArtistSummary,
  GenreSummary,
  MusicTrackRow,
  addPlaylistItem,
  albumArtworkSrc,
  fetchAlbum,
  fetchLibraryAlbums,
  fetchLibraryArtists,
  fetchLibraryGenres,
  fetchLibraryTracks,
} from "../api/client";
import AddToPlaylistModal from "../components/AddToPlaylistModal";
import MusicTrackList from "../components/MusicTrackList";
import { buildMusicTrackMenuItems } from "../components/musicTrackMenuItems";
import { albumTracksToQueue, libraryTracksToQueue } from "../lib/albumPlayback";
import { readRecentPlaylists, rememberPlaylistAdded, type RecentPlaylistEntry } from "../lib/recentPlaylists";
import { useMusicPlayerStore } from "../store/musicPlayer";
import styles from "./Browse.module.css";
import musicStyles from "./MusicBrowse.module.css";

type ViewMode = "grid" | "table";
type MusicTab = "albums" | "artists" | "genres" | "tracks";
type SortField = "title" | "year" | "artist";
type SortOrder = "asc" | "desc";

const VIEW_MODE_KEY = "knox.music.viewMode.v1";

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
  const [recentPlaylistMenu, setRecentPlaylistMenu] = useState<RecentPlaylistEntry[]>(() => readRecentPlaylists());
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
      message.error((e as Error).message || "加载音乐库失败");
    } finally {
      setLoading(false);
    }
  }, [libraryId]);

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
        if (!cancelled) message.error((e as Error).message || "加载音乐库失败");
      } finally {
        if (!cancelled) setLoading(false);
      }
    })();
    return () => {
      cancelled = true;
    };
  }, [libraryId]);

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

  async function playAlbum(albumId: number, e?: React.MouseEvent) {
    e?.stopPropagation();
    e?.preventDefault();
    if (playingId != null) return;
    setPlayingId(albumId);
    try {
      const album = await fetchAlbum(albumId);
      const queue = albumTracksToQueue(album);
      if (queue.length === 0) {
        message.warning("专辑暂无音轨，请重新扫描音乐库");
        return;
      }
      useMusicPlayerStore.getState().loadAlbum(albumId, queue, 0, { sequential: true });
    } catch (err: unknown) {
      message.error((err as Error).message || "无法播放专辑");
    } finally {
      setPlayingId(null);
    }
  }

  function playTrackFromList(mediaId: number, orderedTracks?: MusicTrackRow[]) {
    const source = orderedTracks ?? filteredTracks;
    const queue = libraryTracksToQueue(source);
    const idx = queue.findIndex((q) => q.mediaId === mediaId);
    if (idx < 0 || queue.length === 0) {
      message.warning("无法播放该曲目");
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
                "播放列表";
              message.success(`已添加到「${name}」`);
              rememberPlaylistAdded({ id: playlistId, name });
              setRecentPlaylistMenu(readRecentPlaylists());
            } catch {
              message.error("添加失败，可能已在列表中");
            }
          },
          afterDelete: reloadLibrary,
        },
      ),
    [nav, recentPlaylistMenu, reloadLibrary],
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
        return `${filteredArtists.length} 位艺人`;
      case "genres":
        return `${filteredGenres.length} 个流派`;
      case "tracks":
        return `${filteredTracks.length} 首曲目`;
      default:
        return `${filteredAlbums.length} 张专辑`;
    }
  })();

  return (
    <div className={musicStyles.wrap}>
      <div className={musicStyles.header}>
        <div>
          <div className={musicStyles.libraryTitle}>{libraryName || "音乐库"}</div>
          <Tabs
            activeKey={tab}
            onChange={(k) => setTab(k as MusicTab)}
            className={musicStyles.tabs}
            items={[
              { key: "albums", label: "专辑" },
              { key: "artists", label: "艺人" },
              { key: "genres", label: "流派" },
              { key: "tracks", label: "曲目" },
            ]}
          />
        </div>
        <Space wrap className={musicStyles.toolbar}>
          <Input.Search
            allowClear
            placeholder="搜索…"
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
                  { value: "title", label: "按标题" },
                  { value: "artist", label: "按专辑艺人" },
                  { value: "year", label: "按年份" },
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
                  aria-label="网格视图"
                />
                <Button
                  type={viewMode === "table" ? "primary" : "text"}
                  size="small"
                  icon={<TableOutlined />}
                  onClick={() => setViewMode("table")}
                  aria-label="列表视图"
                />
              </div>
            </>
          )}
          <span className={musicStyles.count}>{countLabel}</span>
        </Space>
      </div>

      {loading && albums.length === 0 && artists.length === 0 && tracks.length === 0 ? (
        <div className={styles.loadingWrap}>
          <Spin />
        </div>
      ) : tab === "albums" ? (
        filteredAlbums.length === 0 ? (
          <Empty description="暂无专辑，请先扫描音乐库" />
        ) : viewMode === "grid" ? (
          <div className={musicStyles.albumGrid}>
            {filteredAlbums.map((a) => (
              <div
                key={a.id}
                className={`${musicStyles.albumCard} ${a.is_unknown ? musicStyles.unknownAlbum : ""}`}
              >
                <div
                  className={musicStyles.albumCover}
                  role="link"
                  tabIndex={0}
                  aria-label={`查看专辑 ${a.title}`}
                  onClick={() => nav(`/album/${a.id}`)}
                  onKeyDown={(e) => e.key === "Enter" && nav(`/album/${a.id}`)}
                >
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
                    <CustomerServiceOutlined />
                  </div>
                  <div className={musicStyles.playOverlay} aria-hidden>
                    <button
                      type="button"
                      className={musicStyles.playOverlayBtn}
                      aria-label="播放专辑"
                      disabled={playingId === a.id}
                      onClick={(e) => {
                        e.stopPropagation();
                        e.preventDefault();
                        void playAlbum(a.id, e);
                      }}
                    >
                      <CaretRightOutlined />
                    </button>
                  </div>
                </div>
                <div
                  className={musicStyles.albumMeta}
                  role="link"
                  tabIndex={0}
                  onClick={() => nav(`/album/${a.id}`)}
                  onKeyDown={(e) => e.key === "Enter" && nav(`/album/${a.id}`)}
                >
                  <div className={musicStyles.albumTitle} title={a.title}>
                    {a.title}
                  </div>
                  <div className={musicStyles.albumArtist} title={a.album_artist || "Various Artists"}>
                    {a.album_artist || "Various Artists"}
                  </div>
                </div>
              </div>
            ))}
          </div>
        ) : (
          <div className={musicStyles.tableWrap}>
            <table className={musicStyles.table}>
              <thead>
                <tr>
                  <th>标题</th>
                  <th>专辑艺人</th>
                  <th>年份</th>
                  <th style={{ width: 72 }}>音轨</th>
                </tr>
              </thead>
              <tbody>
                {filteredAlbums.map((a) => (
                  <tr key={a.id} onClick={() => nav(`/album/${a.id}`)}>
                    <td>
                      <span className={musicStyles.tableTitle}>{a.title}</span>
                    </td>
                    <td>{a.album_artist || "—"}</td>
                    <td>{a.year || "—"}</td>
                    <td>{a.track_count ?? "—"}</td>
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )
      ) : tab === "artists" ? (
        filteredArtists.length === 0 ? (
          <Empty description="暂无艺人" />
        ) : (
          <div className={musicStyles.listGrid}>
            {filteredArtists.map((a) => (
              <div
                key={a.id}
                className={musicStyles.listRow}
                role="button"
                tabIndex={0}
                onClick={() => nav(`/artist/${a.id}`)}
                onKeyDown={(e) => e.key === "Enter" && nav(`/artist/${a.id}`)}
              >
                <div className={musicStyles.listIcon}>
                  <CustomerServiceOutlined />
                </div>
                <div className={musicStyles.listMain}>
                  <div className={musicStyles.listTitle}>{a.name}</div>
                  <div className={musicStyles.listSub}>
                    {a.album_count ?? 0} 张专辑 · {a.track_count ?? 0} 首曲目
                  </div>
                </div>
              </div>
            ))}
          </div>
        )
      ) : tab === "genres" ? (
        filteredGenres.length === 0 ? (
          <Empty description="暂无流派" />
        ) : (
          <div className={musicStyles.listGrid}>
            {filteredGenres.map((g) => (
              <div
                key={g.genre}
                className={musicStyles.listRow}
                role="button"
                tabIndex={0}
                onClick={() => nav(`/genre?library=${libraryId}&name=${encodeURIComponent(g.genre)}`)}
                onKeyDown={(e) =>
                  e.key === "Enter" && nav(`/genre?library=${libraryId}&name=${encodeURIComponent(g.genre)}`)
                }
              >
                <div className={musicStyles.listIcon}>
                  <UnorderedListOutlined />
                </div>
                <div className={musicStyles.listMain}>
                  <div className={musicStyles.listTitle}>{g.genre}</div>
                  <div className={musicStyles.listSub}>
                    {g.album_count ?? 0} 张专辑 · {g.track_count ?? 0} 首曲目
                  </div>
                </div>
              </div>
            ))}
          </div>
        )
      ) : filteredTracks.length === 0 ? (
        <Empty description="暂无曲目" />
      ) : (
        <MusicTrackList
          tracks={filteredTracks}
          onPlayTrack={playTrackFromList}
          resolveArtistId={resolveTrackArtistId}
          buildTrackMenu={buildTrackMenu}
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
    </div>
  );
}
