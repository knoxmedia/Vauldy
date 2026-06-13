import type { MenuProps } from "antd";
import { Modal, message } from "antd";
import type { NavigateFunction } from "react-router-dom";
import {
  addFavorite,
  createScrapeTasks,
  deleteMedia,
  encryptMediaAssets,
  extractAudioTrack,
  extractKeyframes,
  fetchMediaDeletionPlan,
  markUnwatched,
  markWatched,
  recognizeMediaSubtitles,
  transcodeAsync,
  unmatchMedia,
} from "../api/client";
import type { RecentFavoriteFolderEntry } from "../lib/recentFavoriteFolders";
import { MAX_RECENT_FAVORITE_FOLDERS } from "../lib/recentFavoriteFolders";
import type { RecentPlaylistEntry } from "../lib/recentPlaylists";
import { tGlobal as t } from "../i18n";

export interface MediaMenuTarget {
  id: number;
  file_path?: string;
  title?: string;
}

export function confirmDeleteMedia(
  target: MediaMenuTarget,
  afterDelete?: () => void | Promise<void>,
): void {
  void (async () => {
    let files: string[] = [];
    try {
      files = await fetchMediaDeletionPlan(target.id);
    } catch {
      files = target.file_path ? [target.file_path] : [];
    }
    if (files.length === 0 && target.file_path) {
      files = [target.file_path];
    }

    Modal.confirm({
      title: t("components.media_menu.delete_title"),
      centered: true,
      okText: t("components.media_menu.ok"),
      cancelText: t("components.media_menu.cancel"),
      okButtonProps: { danger: true },
      content: (
        <div>
          <p style={{ marginBottom: 8 }}>
            {t("components.media_menu.delete_warning")}
          </p>
          {files.length > 0 ? (
            <ul style={{ margin: "0 0 12px", paddingLeft: 20, wordBreak: "break-all" }}>
              {files.map((f) => (
                <li key={f}>{f}</li>
              ))}
            </ul>
          ) : (
            <p style={{ margin: "0 0 12px", color: "#8c8c8c" }}>{t("components.media_menu.no_paths")}</p>
          )}
          <p style={{ marginBottom: 0 }}>{t("components.media_menu.confirm_continue")}</p>
        </div>
      ),
      onOk: async () => {
        try {
          await deleteMedia(target.id);
          message.success(t("components.media_menu.deleted"));
          await afterDelete?.();
        } catch (err: unknown) {
          message.error((err as Error).message || t("components.media_menu.delete_failed"));
          throw err;
        }
      },
    });
  })();
}

export type MediaMenuPreset = "full" | "detailMore";

export function buildMediaMenuItems(
  r: MediaMenuTarget,
  nav: NavigateFunction,
  extra?: {
    /** 详情页「更多」菜单：仅显示管理类子项 */
    preset?: MediaMenuPreset;
    isWatched?: boolean;
    atrackDone?: boolean;
    keyframeDone?: boolean;
    onAddToPlaylist?: (mediaId: number) => void;
    recentPlaylists?: RecentPlaylistEntry[];
    onQuickAddToPlaylist?: (mediaId: number, playlistId: number) => void | Promise<void>;
    onAddToFavoriteFolder?: (mediaId: number) => void;
    recentFavoriteFolders?: RecentFavoriteFolderEntry[];
    onQuickAddToFavoriteFolder?: (mediaId: number, folderId: number) => void | Promise<void>;
    /** 收藏页：菜单底部「取消收藏」 */
    onUnfavorite?: (mediaId: number) => void;
    /** 标记观看状态成功后刷新列表（如收藏页） */
    afterToggleWatched?: () => void;
    /** 删除成功后刷新列表 */
    afterDelete?: () => void | Promise<void>;
    /** 隐藏删除项（默认显示） */
    hideDelete?: boolean;
    /** 继续观看：从列表移除（不删除媒体） */
    onRemoveFromContinueWatching?: (mediaId: number) => void | Promise<void>;
    /** 媒体库：是否已成功刮削 */
    scraped?: boolean;
    /** 媒体库：打开匹配对话框 */
    onOpenMatch?: (mediaId: number) => void;
    /** 媒体库：取消匹配后刷新 */
    afterUnmatch?: () => void | Promise<void>;
    /** 详情页「查看信息」：滚动到信息区而非跳转 */
    onGetInfo?: () => void;
    /** 媒体库视频：显示「加密资源」菜单项 */
    showEncryptAsset?: boolean;
    /** 已加密则禁用「加密资源」 */
    encryptedAsset?: boolean;
    /** 加密完成后刷新列表 */
    afterEncryptAsset?: () => void | Promise<void>;
  },
): MenuProps {
  const preset = extra?.preset ?? "full";
  const isWatched = extra?.isWatched ?? false;
  const watchedLabel = isWatched ? t("components.media_menu.watched_mark_as_unwatched") : t("components.media_menu.watched_mark_as_watched");
  const atrackDone = extra?.atrackDone ?? false;
  const keyframeDone = extra?.keyframeDone ?? false;
  const onAddToPlaylist = extra?.onAddToPlaylist;
  const recentPlaylists = extra?.recentPlaylists ?? [];
  const onQuickAddToPlaylist = extra?.onQuickAddToPlaylist;
  const onAddToFavoriteFolder = extra?.onAddToFavoriteFolder;
  const recentFavoriteFolders = extra?.recentFavoriteFolders ?? [];
  const onQuickAddToFavoriteFolder = extra?.onQuickAddToFavoriteFolder;
  const onUnfavorite = extra?.onUnfavorite;
  const afterToggleWatched = extra?.afterToggleWatched;
  const afterDelete = extra?.afterDelete;
  const hideDelete = extra?.hideDelete ?? false;
  const onRemoveFromContinueWatching = extra?.onRemoveFromContinueWatching;
  const scraped = extra?.scraped ?? false;
  const onOpenMatch = extra?.onOpenMatch;
  const afterUnmatch = extra?.afterUnmatch;
  const onGetInfo = extra?.onGetInfo;
  const showEncryptAsset = extra?.showEncryptAsset ?? false;
  const encryptedAsset = extra?.encryptedAsset ?? false;
  const afterEncryptAsset = extra?.afterEncryptAsset;

  const addToChildren: MenuProps["items"] = [
    {
      key: "addToCollection",
      label: t("components.media_menu.add_to_collection"),
    },
    { type: "divider" as const },
    {
      key: "openAddToFavoriteFolder",
      label: t("components.media_menu.add_to_favorite_folder"),
      disabled: !onAddToFavoriteFolder,
    },
  ];
  if (recentFavoriteFolders.length > 0 && onQuickAddToFavoriteFolder) {
    addToChildren.push({
      type: "group",
      label: t("components.media_menu.recent_favorite_folders"),
      children: recentFavoriteFolders.slice(0, MAX_RECENT_FAVORITE_FOLDERS).map((folder) => ({
        key: `recentFavoriteFolder:${folder.id}`,
        label: folder.name,
      })),
    });
  }
  addToChildren.push({ type: "divider" as const });
  addToChildren.push({
    key: "openAddToPlaylist",
    label: t("components.media_menu.add_to_playlist"),
    disabled: !onAddToPlaylist,
  });
  if (recentPlaylists.length > 0 && onQuickAddToPlaylist) {
    addToChildren.push({
      type: "group",
      label: t("components.media_menu.recent"),
      children: recentPlaylists.slice(0, 3).map((pl) => ({
        key: `recentPlaylist:${pl.id}`,
        label: pl.name,
      })),
    });
  }

  const fullItems: MenuProps["items"] = [
      { key: "play", label: t("components.media_menu.play") },
      { key: "detail", label: t("components.media_menu.detail") },
      { type: "divider" as const },
      {
        key: "addTo",
        label: t("components.media_menu.add_to"),
        children: addToChildren,
      },
      { key: "toggleWatched", label: watchedLabel },
      ...(onRemoveFromContinueWatching
        ? [{ key: "removeFromContinueWatching", label: t("components.media_menu.remove_from_continue") }]
        : []),
      ...(onOpenMatch && !scraped ? [{ key: "match", label: t("components.media_menu.match") }] : []),
      ...(onOpenMatch && scraped
        ? [
            { key: "fixMatch", label: t("components.media_menu.fix_match") },
            { key: "unmatch", label: t("components.media_menu.unmatch") },
          ]
        : []),
      { type: "divider" as const },
      { key: "refreshMetadata", label: t("components.media_menu.refresh_metadata") },
      { key: "analyze", label: t("components.media_menu.analyze") },
      { key: "optimize", label: t("components.media_menu.optimize") },
      { type: "divider" as const },
      { key: "recognizeSubtitles", label: t("components.media_menu.recognize_subtitles") },
      { key: "extractAudio", label: atrackDone ? t("components.media_menu.reextract_audio") : t("components.media_menu.extract_audio") },
      { key: "extractKeyframes", label: keyframeDone ? t("components.media_menu.reextract_keyframes") : t("components.media_menu.extract_keyframes") },
      ...(showEncryptAsset
        ? [
            {
              key: "encryptAsset",
              label: t("components.media_menu.encrypt_asset"),
              disabled: encryptedAsset,
            },
          ]
        : []),
      { type: "divider" as const },
      { key: "viewHistory", label: t("components.media_menu.view_history") },
      { key: "getInfo", label: preset === "detailMore" ? t("components.media_menu.get_info_view") : t("components.media_menu.get_info_get") },
      ...(onUnfavorite
        ? [
            { type: "divider" as const },
            { key: "unfavorite", label: t("components.media_menu.unfavorite"), danger: true },
          ]
        : []),
      ...(!hideDelete
        ? [
            { type: "divider" as const },
            { key: "delete", label: t("components.media_menu.delete"), danger: true },
          ]
        : []),
    ];

  const detailMoreItems: MenuProps["items"] = [
    {
      key: "addTo",
      label: t("components.media_menu.add_to"),
      children: addToChildren,
    },
    { type: "divider" as const },
    { key: "refreshMetadata", label: t("components.media_menu.refresh_metadata") },
    { key: "analyze", label: t("components.media_menu.analyze") },
    ...(onOpenMatch && !scraped ? [{ key: "match", label: t("components.media_menu.match") }] : []),
    ...(onOpenMatch && scraped
      ? [
          { key: "fixMatch", label: t("components.media_menu.fix_match") },
          { key: "unmatch", label: t("components.media_menu.unmatch") },
        ]
      : []),
    { key: "optimize", label: t("components.media_menu.optimize") },
    { type: "divider" as const },
    { key: "viewHistory", label: t("components.media_menu.view_history") },
    { key: "getInfo", label: t("components.media_menu.get_info_view") },
  ];

  return {
    items: preset === "detailMore" ? detailMoreItems : fullItems,
    onClick: ({ key, domEvent }) => {
      domEvent.stopPropagation();
      switch (key) {
        case "play":
          nav(`/player/${r.id}`);
          break;
        case "detail":
          nav(`/detail/${r.id}`);
          break;
        case "addToCollection":
          addFavorite(r.id)
            .then(() => message.success(t("components.media_menu.added_to_favorites")))
            .catch(() => message.error(t("components.media_menu.operation_failed")));
          break;
        case "openAddToFavoriteFolder":
          if (onAddToFavoriteFolder) {
            onAddToFavoriteFolder(r.id);
          } else {
            message.info(t("components.media_menu.favorite_folder_wip"));
          }
          break;
        case "openAddToPlaylist":
          if (onAddToPlaylist) {
            onAddToPlaylist(r.id);
          } else {
            message.info(t("components.media_menu.playlist_wip"));
          }
          break;
        case "toggleWatched":
          if (isWatched) {
            markUnwatched(r.id)
              .then(() => {
                message.success(t("components.media_menu.marked_unwatched"));
                afterToggleWatched?.();
              })
              .catch(() => message.error(t("components.media_menu.operation_failed")));
          } else {
            markWatched(r.id)
              .then(() => {
                message.success(t("components.media_menu.marked_watched"));
                afterToggleWatched?.();
              })
              .catch(() => message.error(t("components.media_menu.operation_failed")));
          }
          break;
        case "removeFromContinueWatching":
          void Promise.resolve(onRemoveFromContinueWatching?.(r.id)).catch(() =>
            message.error(t("components.media_menu.operation_failed")),
          );
          break;
        case "match":
        case "fixMatch":
          onOpenMatch?.(r.id);
          break;
        case "unmatch":
          Modal.confirm({
            title: t("components.media_menu.unmatch_modal_title"),
            centered: true,
            okText: t("components.media_menu.ok"),
            cancelText: t("components.media_menu.cancel"),
            content: t("components.media_menu.unmatch_modal_content"),
            onOk: async () => {
              try {
                await unmatchMedia(r.id);
                message.success(t("components.media_menu.unmatched"));
                await afterUnmatch?.();
              } catch (err: unknown) {
                message.error((err as Error).message || t("components.media_menu.operation_failed"));
                throw err;
              }
            },
          });
          break;
        case "unfavorite":
          onUnfavorite?.(r.id);
          break;
        case "delete":
          confirmDeleteMedia(r, afterDelete);
          break;
        case "refreshMetadata":
          createScrapeTasks([r.id])
            .then(() => message.success(t("components.media_menu.scrape_task_created")))
            .catch(() => message.error(t("components.media_menu.operation_failed")));
          break;
        case "analyze":
          transcodeAsync(r.id, "analyze")
            .then(() => message.success(t("components.media_menu.analyze_task_created")))
            .catch(() => message.error(t("components.media_menu.operation_failed")));
          break;
        case "optimize":
          transcodeAsync(r.id, "optimize")
            .then(() => message.success(t("components.media_menu.optimize_task_created")))
            .catch(() => message.error(t("components.media_menu.operation_failed")));
          break;
        case "recognizeSubtitles":
          recognizeMediaSubtitles(r.id)
            .then(() => message.success(t("components.media_menu.subtitle_task_created")))
            .catch((err: unknown) => {
              const msg = (err as { response?: { data?: { error?: string } } })?.response?.data?.error;
              message.error(msg || t("components.media_menu.operation_failed"));
            });
          break;
        case "extractAudio":
          extractAudioTrack(r.id)
            .then(() => message.success(t("components.media_menu.atrack_task_created")))
            .catch(() => message.error(t("components.media_menu.operation_failed")));
          break;
        case "extractKeyframes":
          extractKeyframes(r.id)
            .then(() => message.success(t("components.media_menu.keyframe_task_created")))
            .catch(() => message.error(t("components.media_menu.operation_failed")));
          break;
        case "encryptAsset":
          if (encryptedAsset) {
            break;
          }
          encryptMediaAssets(r.id)
            .then(() => {
              message.success(t("components.media_menu.encrypt_asset_queued"));
              void afterEncryptAsset?.();
              window.setTimeout(() => void afterEncryptAsset?.(), 8000);
              window.setTimeout(() => void afterEncryptAsset?.(), 25000);
            })
            .catch((err: unknown) => {
              const msg = (err as { response?: { data?: { error?: string } } })?.response?.data?.error;
              if (msg === "already encrypted") {
                message.info(t("components.media_menu.encrypt_asset_already"));
                void afterEncryptAsset?.();
                return;
              }
              message.error(msg || t("components.media_menu.operation_failed"));
            });
          break;
        case "viewHistory":
          nav(`/playback-history?media_id=${r.id}`);
          break;
        case "getInfo":
          if (onGetInfo) {
            onGetInfo();
          } else {
            nav(`/detail/${r.id}`);
          }
          break;
        default: {
          const sk = String(key);
          if (sk.startsWith("recentFavoriteFolder:") && onQuickAddToFavoriteFolder) {
            const fid = Number(sk.slice("recentFavoriteFolder:".length));
            if (!Number.isNaN(fid)) {
              void Promise.resolve(onQuickAddToFavoriteFolder(r.id, fid));
            }
            break;
          }
          if (sk.startsWith("recentPlaylist:") && onQuickAddToPlaylist) {
            const pid = Number(sk.slice("recentPlaylist:".length));
            if (!Number.isNaN(pid)) {
              void Promise.resolve(onQuickAddToPlaylist(r.id, pid));
            }
          }
          break;
        }
      }
    },
  };
}
