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
import { useT } from "../i18n";
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

  async function playAlbum(albumId: number, e?: React.MouseEvent) {
    e?.stopPropagation();
    e?.preventDefault();
    if (playingId != null) return;
    setPlayingId(albumId);
    try {
      const album = await fetchAlbum(albumId);
      const queue = albumTracksToQueue(album);
      if (queue.length === 0) {
        message.warning(t("pages.artist_detail.album_no_tracks_rescan"));
        return;
      }
      useMusicPlayerStore.getState().loadAlbum(albumId, queue, 0, { sequential: true });
    } catch (err: unknown) {
      message.error((err as Error).message || t("pages.artist_detail.cannot_play_album"));
    } finally {
      setPlayingId(null);
    }
  }

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

      {loading && albums.length === 0 && artists.length === 0 && tracks.length === 0 ? (
        <div className={styles.loadingWrap}>
          <Spin />
        </div>
      ) : tab === "albums" ? (
        filteredAlbums.length === 0 ? (
          <Empty description={t("pages.music_browse.no_albums")} />
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
                  aria-label={t("pages.artist_detail.view_album_label", { title: a.title })}
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
                      aria-label={t("pages.artist_detail.play_album")}
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
                  <div className={musicStyles.albumArtist} title={a.album_artist || t("pages.music_browse.various_artists")}>
                    {a.album_artist || t("pages.music_browse.various_artists")}
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
                  <th>{t("pages.music_browse.col_title")}</th>
                  <th>{t("pages.music_browse.col_album_artist")}</th>
                  <th>{t("pages.music_browse.col_year")}</th>
                  <th style={{ width: 72 }}>{t("pages.music_browse.col_track")}</th>
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
          <Empty description={t("pages.music_browse.no_artists")} />
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
                    {t("pages.music_browse.list_albums_tracks", { albums: a.album_count ?? 0, tracks: a.track_count ?? 0 })}
                  </div>
                </div>
              </div>
            ))}
          </div>
        )
      ) : tab === "genres" ? (
        filteredGenres.length === 0 ? (
          <Empty description={t("pages.music_browse.no_genres")} />
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
                    {t("pages.music_browse.list_albums_tracks", { albums: g.album_count ?? 0, tracks: g.track_count ?? 0 })}
                  </div>
                </div>
              </div>
            ))}
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
