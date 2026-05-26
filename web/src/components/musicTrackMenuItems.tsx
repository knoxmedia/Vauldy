import type { MenuProps } from "antd";
import { message } from "antd";
import type { NavigateFunction } from "react-router-dom";
import { addFavorite, enqueueLyricRecognition } from "../api/client";
import type { RecentPlaylistEntry } from "../lib/recentPlaylists";
import { confirmDeleteMedia } from "./mediaMenuItems";

export type MusicTrackMenuTarget = {
  media_id: number;
  title?: string;
  file_path?: string;
};

export function buildMusicTrackMenuItems(
  track: MusicTrackMenuTarget,
  nav: NavigateFunction,
  extra?: {
    onPlay?: (mediaId: number) => void;
    onAddToPlaylist?: (mediaId: number) => void;
    recentPlaylists?: RecentPlaylistEntry[];
    onQuickAddToPlaylist?: (mediaId: number, playlistId: number) => void | Promise<void>;
    afterDelete?: () => void | Promise<void>;
  },
): MenuProps {
  const mediaId = track.media_id;
  const onPlay = extra?.onPlay;
  const onAddToPlaylist = extra?.onAddToPlaylist;
  const recentPlaylists = extra?.recentPlaylists ?? [];
  const onQuickAddToPlaylist = extra?.onQuickAddToPlaylist;
  const afterDelete = extra?.afterDelete;

  const addToChildren: MenuProps["items"] = [
    {
      key: "openAddToPlaylist",
      label: "添加到播放列表…",
      disabled: !onAddToPlaylist,
    },
    {
      key: "addFavorite",
      label: "收藏",
    },
  ];
  if (recentPlaylists.length > 0 && onQuickAddToPlaylist) {
    addToChildren.push({
      type: "group",
      label: "最近",
      children: recentPlaylists.slice(0, 3).map((pl) => ({
        key: `recentPlaylist:${pl.id}`,
        label: pl.name,
      })),
    });
  }

  return {
    items: [
      { key: "play", label: "播放" },
      { type: "divider" },
      { key: "addTo", label: "添加到", children: addToChildren },
      { key: "edit", label: "编辑" },
      { key: "identifyLyrics", label: "识别歌词" },
      { type: "divider" },
      { key: "viewHistory", label: "查看播放历史" },
      { key: "getInfo", label: "查看信息" },
      { type: "divider" },
      { key: "delete", label: "删除", danger: true },
    ],
    onClick: ({ key, domEvent }) => {
      domEvent.stopPropagation();
      switch (key) {
        case "play":
          if (onPlay) onPlay(mediaId);
          else message.info("无法播放该曲目");
          break;
        case "openAddToPlaylist":
          onAddToPlaylist?.(mediaId);
          break;
        case "addFavorite":
          addFavorite(mediaId)
            .then(() => message.success("已加入收藏"))
            .catch(() => message.error("操作失败"));
          break;
        case "edit":
          nav(`/detail/${mediaId}`);
          break;
        case "identifyLyrics":
          void enqueueLyricRecognition(mediaId)
            .then(() => message.success("已加入歌词识别任务"))
            .catch(() => message.error("加入歌词识别任务失败"));
          break;
        case "viewHistory":
          nav(`/playback-history?media_id=${mediaId}`);
          break;
        case "getInfo":
          nav(`/detail/${mediaId}`);
          break;
        case "delete":
          confirmDeleteMedia(
            { id: mediaId, title: track.title, file_path: track.file_path },
            afterDelete,
          );
          break;
        default: {
          const sk = String(key);
          if (sk.startsWith("recentPlaylist:") && onQuickAddToPlaylist) {
            const pid = Number(sk.slice("recentPlaylist:".length));
            if (!Number.isNaN(pid)) {
              void Promise.resolve(onQuickAddToPlaylist(mediaId, pid));
            }
          }
          break;
        }
      }
    },
  };
}
