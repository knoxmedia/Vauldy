import {
  AppstoreOutlined,
  ArrowDownOutlined,
  ArrowLeftOutlined,
  ArrowUpOutlined,
  CaretRightOutlined,
  CustomerServiceOutlined,
  MoreOutlined,
  StarOutlined,
  TableOutlined,
} from "@ant-design/icons";
import { Button, Empty, Select, Space, Spin, Typography, message } from "antd";
import { useEffect, useMemo, useState } from "react";
import { useNavigate, useParams } from "react-router-dom";
import { AlbumSummary, albumArtworkSrc, fetchAlbum, fetchArtistAlbums } from "../api/client";
import ToolbarPlayIcon from "../components/ToolbarPlayIcon";
import { albumTracksToQueue } from "../lib/albumPlayback";
import { useMusicPlayerStore } from "../store/musicPlayer";
import artistStyles from "./ArtistDetail.module.css";
import styles from "./Browse.module.css";
import musicStyles from "./MusicBrowse.module.css";
import md from "./MediaDetail.module.css";

type ViewMode = "grid" | "table";
type SortField = "title" | "year";
type SortOrder = "asc" | "desc";

const VIEW_MODE_KEY = "knox.music.artist.viewMode.v1";

function readViewMode(): ViewMode {
  try {
    const v = localStorage.getItem(VIEW_MODE_KEY);
    if (v === "grid" || v === "table") return v;
  } catch {
    /* ignore */
  }
  return "grid";
}

function artistInitials(name: string): string {
  const t = name.trim();
  if (!t) return "?";
  const parts = t.split(/[\s/]+/).filter(Boolean);
  if (parts.length >= 2) {
    return (parts[0]!.charAt(0) + parts[1]!.charAt(0)).toUpperCase();
  }
  if (t.length >= 2) return t.slice(0, 2).toUpperCase();
  return t.charAt(0).toUpperCase();
}

function fmtDuration(sec?: number): string {
  if (!sec || sec <= 0) return "—";
  const h = Math.floor(sec / 3600);
  const m = Math.floor((sec % 3600) / 60);
  if (h > 0) return `${h}:${String(m).padStart(2, "0")}:${String(sec % 60).padStart(2, "0")}`;
  return `${m}:${String(sec % 60).padStart(2, "0")}`;
}

export default function ArtistDetailPage() {
  const { id } = useParams();
  const nav = useNavigate();
  const artistId = Number(id);
  const [albums, setAlbums] = useState<AlbumSummary[]>([]);
  const [artistName, setArtistName] = useState("");
  const [libraryId, setLibraryId] = useState<number | null>(null);
  const [loading, setLoading] = useState(true);
  const [playing, setPlaying] = useState(false);
  const [playingAlbumId, setPlayingAlbumId] = useState<number | null>(null);
  const [viewMode, setViewMode] = useState<ViewMode>(() => readViewMode());
  const [sortField, setSortField] = useState<SortField>("title");
  const [sortOrder, setSortOrder] = useState<SortOrder>("asc");

  useEffect(() => {
    if (!Number.isFinite(artistId) || artistId <= 0) return;
    let cancelled = false;
    (async () => {
      setLoading(true);
      try {
        const data = await fetchArtistAlbums(artistId);
        if (cancelled) return;
        const items = data.items ?? [];
        setAlbums(items);
        setArtistName(data.artist_name ?? "");
        setLibraryId(items[0]?.library_id ?? null);
      } finally {
        if (!cancelled) setLoading(false);
      }
    })();
    return () => {
      cancelled = true;
    };
  }, [artistId]);

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
        message.warning("专辑暂无音轨，请重新扫描音乐库");
        return;
      }
      useMusicPlayerStore.getState().loadAlbum(albumId, queue, 0, { sequential: true });
    } catch (err: unknown) {
      message.error((err as Error).message || "无法播放专辑");
    } finally {
      setPlayingAlbumId(null);
    }
  }

  async function playArtist() {
    if (playing || sortedAlbums.length === 0) return;
    setPlaying(true);
    try {
      const queue = [];
      for (const a of sortedAlbums) {
        const album = await fetchAlbum(a.id);
        queue.push(...albumTracksToQueue(album));
      }
      if (queue.length === 0) {
        message.warning("暂无可用音轨，请重新扫描音乐库");
        return;
      }
      const firstAlbumId = queue[0]?.albumId ?? sortedAlbums[0]!.id;
      useMusicPlayerStore.getState().loadAlbum(firstAlbumId, queue, 0, { sequential: true });
    } catch (err: unknown) {
      message.error((err as Error).message || "无法播放");
    } finally {
      setPlaying(false);
    }
  }

  if (loading) {
    return (
      <div className={musicStyles.wrap}>
        <Spin />
      </div>
    );
  }

  if (!artistName && albums.length === 0) {
    return (
      <div className={musicStyles.wrap}>
        <Empty description="艺人不存在" />
      </div>
    );
  }

  const displayName = artistName || "Unknown Artist";

  return (
    <div className={musicStyles.wrap}>
      <Button
        type="text"
        icon={<ArrowLeftOutlined />}
        onClick={() => (libraryId ? nav(`/browse?library_id=${libraryId}`) : nav(-1))}
        style={{ color: "rgba(255,255,255,0.65)", marginBottom: 16 }}
      >
        返回资料库
      </Button>

      <div className={md.hero}>
        <div className={md.heroBody}>
          <div className={artistStyles.artistAvatar} aria-hidden>
            {artistInitials(displayName)}
          </div>
          <div className={md.heroInfo}>
            <Typography.Text type="secondary">艺人</Typography.Text>
            <Typography.Title level={2} style={{ color: "#fff", margin: 0 }}>
              {displayName}
            </Typography.Title>
            <div className={musicStyles.albumMetaRow}>
              <span>{albums.length} 张专辑</span>
              {trackTotal > 0 ? <span>{trackTotal} 首曲目</span> : null}
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
                onClick={() => void playArtist()}
              >
                播放
              </Button>
              <Button type="text" icon={<MoreOutlined />} style={{ color: "rgba(255,255,255,0.65)" }} aria-label="更多" />
            </div>
          </div>
        </div>
      </div>

      <div className={artistStyles.sectionHeader}>
        <Typography.Title level={5} className={artistStyles.sectionTitle}>
          <CustomerServiceOutlined className={artistStyles.sectionIcon} />
          {albums.length} 专辑
        </Typography.Title>
        <Space wrap className={artistStyles.sectionToolbar}>
          <Select
            size="small"
            value={sortField}
            onChange={setSortField}
            options={[
              { value: "title", label: "按标题" },
              { value: "year", label: "按年份" },
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
              aria-label="网格视图"
            />
            <Button
              type={viewMode === "table" ? "primary" : "text"}
              size="small"
              icon={<TableOutlined />}
              onClick={() => setViewMode("table")}
              aria-label="详细视图"
            />
          </div>
        </Space>
      </div>

      {sortedAlbums.length === 0 ? (
        <Empty description="暂无专辑" />
      ) : viewMode === "grid" ? (
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
                <div className={musicStyles.albumArtist}>
                  {a.year ? `${a.year}` : "—"}
                  {a.track_count != null ? ` · ${a.track_count} 首` : ""}
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
                <th style={{ width: 88 }}>年份</th>
                <th style={{ width: 72 }}>音轨</th>
                <th style={{ width: 96 }}>时长</th>
                <th style={{ width: 40 }} />
              </tr>
            </thead>
            <tbody>
              {sortedAlbums.map((a) => (
                <tr key={a.id} onClick={() => nav(`/album/${a.id}`)}>
                  <td>
                    <span className={musicStyles.tableTitle}>{a.title}</span>
                  </td>
                  <td>{a.year || "—"}</td>
                  <td>{a.track_count ?? "—"}</td>
                  <td>{fmtDuration(a.total_duration)}</td>
                  <td>
                    <Button
                      type="text"
                      size="small"
                      icon={<CaretRightOutlined />}
                      aria-label="播放专辑"
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
