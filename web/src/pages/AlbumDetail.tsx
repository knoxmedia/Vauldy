import {
  ArrowLeftOutlined,
  CustomerServiceOutlined,
  MoreOutlined,
  StarOutlined,
} from "@ant-design/icons";
import { Button, Empty, Spin, Typography, message } from "antd";
import { useCallback, useEffect, useMemo, useState } from "react";
import { useNavigate, useParams } from "react-router-dom";
import {
  AlbumDetail as AlbumDetailType,
  addPlaylistItem,
  albumArtworkSrc,
  fetchAlbum,
  type MusicTrackRow,
} from "../api/client";
import AddToPlaylistModal from "../components/AddToPlaylistModal";
import MusicTrackList from "../components/MusicTrackList";
import ToolbarPlayIcon from "../components/ToolbarPlayIcon";
import { buildMusicTrackMenuItems } from "../components/musicTrackMenuItems";
import { albumTracksToQueue } from "../lib/albumPlayback";
import { readRecentPlaylists, rememberPlaylistAdded, type RecentPlaylistEntry } from "../lib/recentPlaylists";
import { useMusicPlayerStore } from "../store/musicPlayer";
import musicStyles from "./MusicBrowse.module.css";
import md from "./MediaDetail.module.css";

function fmtTotalDuration(sec?: number): string {
  if (!sec || sec <= 0) return "";
  const h = Math.floor(sec / 3600);
  const m = Math.floor((sec % 3600) / 60);
  if (h > 0) return `${h} 小时 ${m} 分钟`;
  return `${m} 分钟`;
}

export default function AlbumDetailPage() {
  const { id } = useParams();
  const nav = useNavigate();
  const albumId = Number(id);
  const [album, setAlbum] = useState<AlbumDetailType | null>(null);
  const [loading, setLoading] = useState(true);
  const [playing, setPlaying] = useState(false);
  const [coverFailed, setCoverFailed] = useState(false);
  const [addToPlaylistMediaId, setAddToPlaylistMediaId] = useState<number | null>(null);
  const [recentPlaylistMenu, setRecentPlaylistMenu] = useState<RecentPlaylistEntry[]>(() => readRecentPlaylists());

  const reloadAlbum = useCallback(async () => {
    if (!Number.isFinite(albumId) || albumId <= 0) return;
    try {
      const data = await fetchAlbum(albumId);
      setAlbum(data);
      setCoverFailed(false);
    } catch (e: unknown) {
      message.error((e as Error).message || "加载专辑失败");
    }
  }, [albumId]);

  useEffect(() => {
    if (!Number.isFinite(albumId) || albumId <= 0) return;
    let cancelled = false;
    (async () => {
      setLoading(true);
      try {
        const data = await fetchAlbum(albumId);
        if (!cancelled) {
          setAlbum(data);
          setCoverFailed(false);
        }
      } catch (e: unknown) {
        if (!cancelled) message.error((e as Error).message || "加载专辑失败");
      } finally {
        if (!cancelled) setLoading(false);
      }
    })();
    return () => {
      cancelled = true;
    };
  }, [albumId]);

  const tracks = album?.tracks ?? [];
  const totalDuration = useMemo(
    () => tracks.reduce((sum, t) => sum + (t.duration ?? 0), 0),
    [tracks],
  );

  function playTrackFromList(mediaId: number, orderedTracks?: MusicTrackRow[]) {
    if (!album) return;
    const source = orderedTracks ?? tracks;
    const queue = albumTracksToQueue({ ...album, tracks: source });
    const idx = queue.findIndex((q) => q.mediaId === mediaId);
    if (idx < 0 || queue.length === 0) {
      message.warning("无法播放该曲目");
      return;
    }
    useMusicPlayerStore.getState().playTrack(queue[idx]!, queue, idx);
  }

  const resolveTrackArtistId = useCallback(
    (track: { artist_id?: number }) => {
      if (track.artist_id && track.artist_id > 0) return track.artist_id;
      if (album?.album_artist_id && album.album_artist_id > 0) return album.album_artist_id;
      return null;
    },
    [album?.album_artist_id],
  );

  const buildTrackMenu = useCallback(
    (track: { media_id: number; title: string; file_path?: string }) =>
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
          afterDelete: reloadAlbum,
        },
      ),
    [nav, recentPlaylistMenu, reloadAlbum],
  );

  async function startPlayback(startMediaId?: number) {
    if (!album || playing) return;
    setPlaying(true);
    try {
      let detail = album;
      let queue = albumTracksToQueue(detail);
      if (queue.length === 0) {
        detail = await fetchAlbum(albumId);
        setAlbum(detail);
        queue = albumTracksToQueue(detail);
      }
      if (queue.length === 0) {
        message.warning("专辑暂无可用音轨，请在管理控制台重新扫描该音乐库");
        return;
      }
      if (startMediaId) {
        playTrackFromList(startMediaId);
      } else {
        const ok = useMusicPlayerStore.getState().loadAlbum(albumId, queue, 0, { sequential: true });
        if (!ok) message.warning("无法开始播放，请重新扫描音乐库");
      }
    } catch (e: unknown) {
      message.error((e as Error).message || "无法播放");
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

  if (!album) {
    return (
      <div className={musicStyles.wrap}>
        <Empty description="专辑不存在" />
      </div>
    );
  }

  return (
    <div className={musicStyles.wrap}>
      <Button
        type="text"
        icon={<ArrowLeftOutlined />}
        onClick={() => nav(`/browse?library_id=${album.library_id}`)}
        style={{ color: "rgba(255,255,255,0.65)", marginBottom: 16 }}
      >
        返回资料库
      </Button>

      <div className={md.hero}>
        <div className={`${md.heroBody} ${musicStyles.albumHeroBody}`}>
          <div className={musicStyles.albumCoverWrap}>
            {!coverFailed ? (
              <img
                src={albumArtworkSrc(album.id)}
                alt=""
                className={musicStyles.albumCoverImg}
                onError={() => setCoverFailed(true)}
              />
            ) : (
              <div className={musicStyles.albumCoverPlaceholder}>
                <CustomerServiceOutlined style={{ fontSize: 64, color: "rgba(255,255,255,0.15)" }} />
              </div>
            )}
          </div>
          <Typography.Text type="secondary" className={musicStyles.albumHeroKind}>
            专辑
          </Typography.Text>
          <div className={musicStyles.albumHeroMain}>
            <Typography.Title level={2} style={{ color: "#fff", margin: 0 }}>
              {album.title}
            </Typography.Title>
            <Typography.Title level={4} className={md.subtitle} style={{ marginTop: 0 }}>
              {album.album_artist_id ? (
                <button
                  type="button"
                  className={musicStyles.trackLink}
                  style={{ font: "inherit", fontSize: "inherit", fontWeight: "inherit" }}
                  onClick={() => nav(`/artist/${album.album_artist_id}`)}
                >
                  {album.album_artist || "Various Artists"}
                </button>
              ) : (
                album.album_artist || "Various Artists"
              )}
            </Typography.Title>
            <div className={musicStyles.albumMetaRow}>
              {album.year ? <span>{album.year}</span> : null}
              {tracks.length > 0 ? <span>{tracks.length} 音轨</span> : null}
              {totalDuration > 0 ? <span>{fmtTotalDuration(totalDuration)}</span> : null}
              {album.genre ? <span>{album.genre}</span> : null}
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
                onClick={() => startPlayback()}
              >
                播放
              </Button>
              <Button type="text" icon={<MoreOutlined />} style={{ color: "rgba(255,255,255,0.65)" }} aria-label="更多" />
            </div>
          </div>
        </div>
      </div>

      <Typography.Title level={5} style={{ color: "#fff", margin: "24px 0 12px" }}>
        {tracks.length} 音轨
      </Typography.Title>

      <MusicTrackList
        tracks={tracks}
        onPlayTrack={playTrackFromList}
        resolveArtistId={resolveTrackArtistId}
        buildTrackMenu={buildTrackMenu}
        showAlbumColumn={false}
      />

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
