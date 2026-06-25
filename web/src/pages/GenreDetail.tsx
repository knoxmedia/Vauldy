import {
  AppstoreOutlined,
  ArrowDownOutlined,
  ArrowLeftOutlined,
  ArrowUpOutlined,
  CaretRightOutlined,
  MoreOutlined,
  StarOutlined,
  TableOutlined,
  UnorderedListOutlined,
} from "@ant-design/icons";
import { Button, Empty, Select, Space, Spin, Typography, message } from "antd";
import { useEffect, useMemo, useState } from "react";
import { useNavigate, useSearchParams } from "react-router-dom";
import { AlbumSummary, albumArtworkSrc, fetchAlbum, fetchGenreAlbums } from "../api/client";
import MusicPosterPlaceholderIcon from "../components/MusicPosterPlaceholderIcon";
import ToolbarPlayIcon from "../components/ToolbarPlayIcon";
import { albumTracksToQueue } from "../lib/albumPlayback";
import { useMusicPlayerStore } from "../store/musicPlayer";
import { useT } from "../i18n";
import artistStyles from "./ArtistDetail.module.css";
import styles from "./Browse.module.css";
import musicStyles from "./MusicBrowse.module.css";
import md from "./MediaDetail.module.css";

type ViewMode = "grid" | "table";
type SortField = "title" | "year";
type SortOrder = "asc" | "desc";

const VIEW_MODE_KEY = "knox.music.genre.viewMode.v1";

function readViewMode(): ViewMode {
  try {
    const v = localStorage.getItem(VIEW_MODE_KEY);
    if (v === "grid" || v === "table") return v;
  } catch {
    /* ignore */
  }
  return "grid";
}

function genreInitials(name: string): string {
  const n = name.trim();
  if (!n) return "?";
  if (n.length >= 2) return n.slice(0, 2).toUpperCase();
  return n.charAt(0).toUpperCase();
}

function fmtDuration(sec?: number): string {
  if (!sec || sec <= 0) return "—";
  const h = Math.floor(sec / 3600);
  const m = Math.floor((sec % 3600) / 60);
  if (h > 0) return `${h}:${String(m).padStart(2, "0")}:${String(sec % 60).padStart(2, "0")}`;
  return `${m}:${String(sec % 60).padStart(2, "0")}`;
}

export default function GenreDetailPage() {
  const nav = useNavigate();
  const t = useT();
  const [searchParams] = useSearchParams();
  const libraryId = Number(searchParams.get("library"));
  const genreName = searchParams.get("name") ?? "";
  const [albums, setAlbums] = useState<AlbumSummary[]>([]);
  const [loading, setLoading] = useState(true);
  const [playing, setPlaying] = useState(false);
  const [playingAlbumId, setPlayingAlbumId] = useState<number | null>(null);
  const [viewMode, setViewMode] = useState<ViewMode>(() => readViewMode());
  const [sortField, setSortField] = useState<SortField>("title");
  const [sortOrder, setSortOrder] = useState<SortOrder>("asc");

  useEffect(() => {
    if (!Number.isFinite(libraryId) || libraryId <= 0 || !genreName.trim()) return;
    let cancelled = false;
    (async () => {
      setLoading(true);
      try {
        const data = await fetchGenreAlbums(libraryId, genreName);
        if (!cancelled) setAlbums(data.items ?? []);
      } finally {
        if (!cancelled) setLoading(false);
      }
    })();
    return () => {
      cancelled = true;
    };
  }, [libraryId, genreName]);

  useEffect(() => {
    localStorage.setItem(VIEW_MODE_KEY, viewMode);
  }, [viewMode]);

  const sortedAlbums = useMemo(() => {
    return [...albums].sort((a, b) => {
      const factor = sortOrder === "asc" ? 1 : -1;
      if (sortField === "year") return ((a.year ?? 0) - (b.year ?? 0)) * factor;
      return (a.title || "").localeCompare(b.title || "", "zh") * factor;
    });
  }, [albums, sortField, sortOrder]);

  const trackTotal = albums.reduce((sum, a) => sum + (a.track_count ?? 0), 0);

  async function playAlbum(albumId: number, e?: React.MouseEvent) {
    e?.stopPropagation();
    e?.preventDefault();
    if (playingAlbumId != null) return;
    setPlayingAlbumId(albumId);
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
      setPlayingAlbumId(null);
    }
  }

  async function playGenre() {
    if (playing || sortedAlbums.length === 0) return;
    setPlaying(true);
    try {
      const queue = [];
      for (const a of sortedAlbums) {
        const album = await fetchAlbum(a.id);
        queue.push(...albumTracksToQueue(album));
      }
      if (queue.length === 0) {
        message.warning(t("pages.artist_detail.no_tracks_rescan"));
        return;
      }
      const firstAlbumId = queue[0]?.albumId ?? sortedAlbums[0]!.id;
      useMusicPlayerStore.getState().loadAlbum(firstAlbumId, queue, 0, { sequential: true });
    } catch (err: unknown) {
      message.error((err as Error).message || t("pages.artist_detail.cannot_play"));
    } finally {
      setPlaying(false);
    }
  }

  if (!Number.isFinite(libraryId) || libraryId <= 0 || !genreName.trim()) {
    return (
      <div className={musicStyles.wrap}>
        <Empty description={t("pages.genre_detail.invalid_link")} />
      </div>
    );
  }

  if (loading) {
    return (
      <div className={musicStyles.wrap}>
        <Spin />
      </div>
    );
  }

  if (albums.length === 0) {
    return (
      <div className={musicStyles.wrap}>
        <Button
          type="text"
          icon={<ArrowLeftOutlined />}
          onClick={() => nav(`/browse?library_id=${libraryId}`)}
          style={{ color: "rgba(255,255,255,0.65)", marginBottom: 16 }}
        >
          {t("pages.genre_detail.back_to_library")}
        </Button>
        <Empty description={t("pages.genre_detail.no_albums_in_genre")} />
      </div>
    );
  }

  return (
    <div className={musicStyles.wrap}>
      <Button
        type="text"
        icon={<ArrowLeftOutlined />}
        onClick={() => nav(`/browse?library_id=${libraryId}`)}
        style={{ color: "rgba(255,255,255,0.65)", marginBottom: 16 }}
      >
        {t("pages.genre_detail.back_to_library")}
      </Button>

      <div className={md.hero}>
        <div className={md.heroBody}>
          <div className={artistStyles.artistAvatar} aria-hidden>
            {genreInitials(genreName)}
          </div>
          <div className={md.heroInfo}>
            <Typography.Text type="secondary">{t("pages.genre_detail.kind_genre")}</Typography.Text>
            <Typography.Title level={2} style={{ color: "#fff", margin: 0 }}>
              {genreName}
            </Typography.Title>
            <div className={musicStyles.albumMetaRow}>
              <span>{t("pages.genre_detail.albums_count", { count: albums.length })}</span>
              {trackTotal > 0 ? <span>{t("pages.genre_detail.tracks_count", { count: trackTotal })}</span> : null}
            </div>
            <div style={{ marginTop: 4 }}>
              {[1, 2, 3, 4, 5].map((n) => (
                <StarOutlined key={n} style={{ color: "rgba(255,255,255,0.25)", marginRight: 4 }} />
              ))}
            </div>
            <div style={{ display: "flex", gap: 8, marginTop: 12, flexWrap: "wrap" }}>
              <Button
                type="primary"
                size="large"
                icon={<ToolbarPlayIcon className={md.mediaDetailPlaySvg} />}
                className={md.playBtn}
                loading={playing}
                disabled={albums.length === 0}
                onClick={() => void playGenre()}
              >
                {t("pages.genre_detail.play")}
              </Button>
              <Button type="text" icon={<MoreOutlined />} style={{ color: "rgba(255,255,255,0.65)" }} aria-label={t("pages.genre_detail.more_aria")} />
            </div>
          </div>
        </div>
      </div>

      <div className={artistStyles.sectionHeader}>
        <Typography.Title level={5} className={artistStyles.sectionTitle}>
          <UnorderedListOutlined className={artistStyles.sectionIcon} />
          {t("pages.genre_detail.albums_section", { count: albums.length })}
        </Typography.Title>
        <Space wrap className={artistStyles.sectionToolbar}>
          <Select
            size="small"
            value={sortField}
            onChange={setSortField}
            options={[
              { value: "title", label: t("pages.artist_detail.sort_title") },
              { value: "year", label: t("pages.artist_detail.sort_year") },
            ]}
            style={{ width: 120 }}
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
              aria-label={t("pages.artist_detail.view_grid_aria")}
            />
            <Button
              type={viewMode === "table" ? "primary" : "text"}
              size="small"
              icon={<TableOutlined />}
              onClick={() => setViewMode("table")}
              aria-label={t("pages.artist_detail.view_table_aria")}
            />
          </div>
        </Space>
      </div>

      {viewMode === "grid" ? (
        <div className={musicStyles.albumGrid}>
          {sortedAlbums.map((a) => (
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
                  <MusicPosterPlaceholderIcon />
                </div>
                <div className={musicStyles.playOverlay} aria-hidden>
                  <button
                    type="button"
                    className={musicStyles.playOverlayBtn}
                    aria-label={t("pages.artist_detail.play_album")}
                    disabled={playingAlbumId === a.id}
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
                <div className={musicStyles.albumArtist} title={a.album_artist || t("pages.genre_detail.various_artists")}>
                  {a.album_artist || t("pages.genre_detail.various_artists")}
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
                <th>{t("pages.artist_detail.col_title")}</th>
                <th>{t("pages.genre_detail.col_album_artist")}</th>
                <th style={{ width: 88 }}>{t("pages.artist_detail.col_year")}</th>
                <th style={{ width: 72 }}>{t("pages.artist_detail.col_track")}</th>
                <th style={{ width: 96 }}>{t("pages.artist_detail.col_duration")}</th>
                <th style={{ width: 40 }} />
              </tr>
            </thead>
            <tbody>
              {sortedAlbums.map((a) => (
                <tr key={a.id} onClick={() => nav(`/album/${a.id}`)}>
                  <td>
                    <span className={musicStyles.tableTitle}>{a.title}</span>
                  </td>
                  <td>{a.album_artist || "—"}</td>
                  <td>{a.year || "—"}</td>
                  <td>{a.track_count ?? "—"}</td>
                  <td>{fmtDuration(a.total_duration)}</td>
                  <td>
                    <Button
                      type="text"
                      size="small"
                      icon={<CaretRightOutlined />}
                      aria-label={t("pages.artist_detail.play_album")}
                      onClick={(e) => {
                        e.stopPropagation();
                        void playAlbum(a.id, e);
                      }}
                    />
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </div>
  );
}
