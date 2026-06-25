import type { MenuProps } from "antd";
import { message } from "antd";
import type { NavigateFunction } from "react-router-dom";
import { addFavorite, enqueueLyricRecognition } from "../api/client";
import type { RecentPlaylistEntry } from "../lib/recentPlaylists";
import { tGlobal as t } from "../i18n";
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
      label: t("components.music_track_menu.add_to_playlist"),
      disabled: !onAddToPlaylist,
    },
    {
      key: "addFavorite",
      label: t("components.music_track_menu.favorite"),
    },
  ];
  if (recentPlaylists.length > 0 && onQuickAddToPlaylist) {
    addToChildren.push({
      type: "group",
      label: t("components.music_track_menu.recent"),
      children: recentPlaylists.slice(0, 3).map((pl) => ({
        key: `recentPlaylist:${pl.id}`,
        label: pl.name,
      })),
    });
  }

  return {
    items: [
      { key: "play", label: t("components.music_track_menu.play") },
      { type: "divider" },
      { key: "addTo", label: t("components.music_track_menu.add_to"), children: addToChildren },
      { key: "edit", label: t("components.music_track_menu.edit") },
      { key: "identifyLyrics", label: t("components.music_track_menu.identify_lyrics") },
      { type: "divider" },
      { key: "viewHistory", label: t("components.music_track_menu.view_history") },
      { key: "getInfo", label: t("components.music_track_menu.get_info") },
      { type: "divider" },
      { key: "delete", label: t("components.music_track_menu.delete"), danger: true },
    ],
    onClick: ({ key, domEvent }) => {
      domEvent.stopPropagation();
      switch (key) {
        case "play":
          if (onPlay) onPlay(mediaId);
          else message.info(t("components.music_track_menu.cannot_play_track"));
          break;
        case "openAddToPlaylist":
          onAddToPlaylist?.(mediaId);
          break;
        case "addFavorite":
          addFavorite(mediaId)
            .then(() => message.success(t("components.music_track_menu.added_to_favorites")))
            .catch(() => message.error(t("components.music_track_menu.operation_failed")));
          break;
        case "edit":
          nav(`/detail/${mediaId}`);
          break;
        case "identifyLyrics":
          void enqueueLyricRecognition(mediaId)
            .then(() => message.success(t("components.music_track_menu.lyric_task_created")))
            .catch(() => message.error(t("components.music_track_menu.lyric_task_failed")));
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
