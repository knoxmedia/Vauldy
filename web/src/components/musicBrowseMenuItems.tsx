import type { MenuProps } from "antd";
import { Modal, message } from "antd";
import {
  addFavorite,
  deleteMedia,
} from "../api/client";
import type { RecentPlaylistEntry } from "../lib/recentPlaylists";
import { tGlobal as t } from "../i18n";

export type MusicBrowseMenuHandlers = {
  onPlayAll: () => void | Promise<void>;
  onPlayNext: () => void | Promise<void>;
  onAddToPlaylist?: (mediaId: number) => void;
  recentPlaylists?: RecentPlaylistEntry[];
  onQuickAddToPlaylist?: (mediaId: number, playlistId: number) => void | Promise<void>;
  onAddToFavoriteFolder?: (mediaId: number) => void;
  onRefreshMetadata: (mediaIds: number[]) => void | Promise<void>;
  onAnalyze: (mediaIds: number[]) => void | Promise<void>;
  onMatch?: (mediaId: number) => void;
  onViewHistory: () => void;
  onDelete: () => void;
  /** Track media ids for batch actions (metadata, analyze, delete). */
  mediaIds?: number[];
  /** First track media id for add-to / match shortcuts. */
  primaryMediaId?: number;
};

export function buildMusicBrowseMenuItems(
  handlers: MusicBrowseMenuHandlers,
): MenuProps {
  const {
    onPlayAll,
    onPlayNext,
    onAddToPlaylist,
    recentPlaylists = [],
    onQuickAddToPlaylist,
    onAddToFavoriteFolder,
    onRefreshMetadata,
    onAnalyze,
    onMatch,
    onViewHistory,
    onDelete,
    mediaIds = [],
    primaryMediaId,
  } = handlers;

  const addToChildren: MenuProps["items"] = [
    {
      key: "addToCollection",
      label: t("components.media_menu.add_to_collection"),
      disabled: !primaryMediaId,
    },
    { type: "divider" as const },
    {
      key: "openAddToFavoriteFolder",
      label: t("components.media_menu.add_to_favorite_folder"),
      disabled: !onAddToFavoriteFolder || !primaryMediaId,
    },
    { type: "divider" as const },
    {
      key: "openAddToPlaylist",
      label: t("components.media_menu.add_to_playlist"),
      disabled: !onAddToPlaylist || !primaryMediaId,
    },
  ];
  if (recentPlaylists.length > 0 && onQuickAddToPlaylist && primaryMediaId) {
    addToChildren.push({
      type: "group",
      label: t("components.media_menu.recent"),
      children: recentPlaylists.slice(0, 3).map((pl) => ({
        key: `recentPlaylist:${pl.id}`,
        label: pl.name,
      })),
    });
  }

  return {
    items: [
      { key: "playAll", label: t("pages.music_browse.menu_play_all") },
      { key: "playNext", label: t("pages.playlists.menu_play_next") },
      { type: "divider" as const },
      { key: "addTo", label: t("components.media_menu.add_to"), children: addToChildren },
      { type: "divider" as const },
      { key: "refreshMetadata", label: t("components.media_menu.refresh_metadata") },
      { key: "analyze", label: t("components.media_menu.analyze") },
      { key: "match", label: t("components.media_menu.match"), disabled: !onMatch || !primaryMediaId },
      { type: "divider" as const },
      { key: "viewHistory", label: t("components.media_menu.view_history") },
      { key: "delete", label: t("components.media_menu.delete"), danger: true },
    ],
    onClick: ({ key, domEvent }) => {
      domEvent.stopPropagation();
      switch (key) {
        case "playAll":
          void Promise.resolve(onPlayAll());
          break;
        case "playNext":
          void Promise.resolve(onPlayNext());
          break;
        case "addToCollection":
          if (!primaryMediaId) return;
          addFavorite(primaryMediaId)
            .then(() => message.success(t("components.media_menu.added_to_favorites")))
            .catch(() => message.error(t("components.media_menu.operation_failed")));
          break;
        case "openAddToFavoriteFolder":
          if (primaryMediaId) onAddToFavoriteFolder?.(primaryMediaId);
          break;
        case "openAddToPlaylist":
          if (primaryMediaId) onAddToPlaylist?.(primaryMediaId);
          break;
        case "refreshMetadata":
          if (mediaIds.length === 0) return;
          void Promise.resolve(onRefreshMetadata(mediaIds));
          break;
        case "analyze":
          if (mediaIds.length === 0) return;
          void Promise.resolve(onAnalyze(mediaIds));
          break;
        case "match":
          if (primaryMediaId) onMatch?.(primaryMediaId);
          break;
        case "viewHistory":
          onViewHistory();
          break;
        case "delete":
          onDelete();
          break;
        default: {
          const sk = String(key);
          if (sk.startsWith("recentPlaylist:") && onQuickAddToPlaylist && primaryMediaId) {
            const pid = Number(sk.slice("recentPlaylist:".length));
            if (!Number.isNaN(pid)) void Promise.resolve(onQuickAddToPlaylist(primaryMediaId, pid));
          }
          break;
        }
      }
    },
  };
}

export function confirmDeleteMusicBrowseEntity(
  title: string,
  mediaIds: number[],
  afterDelete?: () => void | Promise<void>,
): void {
  if (mediaIds.length === 0) {
    message.warning(t("pages.music_browse.delete_no_tracks"));
    return;
  }
  Modal.confirm({
    title: t("pages.music_browse.delete_title", { title }),
    centered: true,
    okText: t("components.media_menu.ok"),
    cancelText: t("components.media_menu.cancel"),
    okButtonProps: { danger: true },
    content: t("pages.music_browse.delete_confirm", { count: mediaIds.length }),
    onOk: async () => {
      let ok = 0;
      for (const id of mediaIds) {
        try {
          await deleteMedia(id);
          ok++;
        } catch {
          /* skip */
        }
      }
      if (ok > 0) {
        message.success(t("pages.music_browse.deleted_tracks", { ok }));
        await afterDelete?.();
      } else {
        message.error(t("components.media_menu.delete_failed"));
      }
    },
  });
}
