import type { MenuProps } from "antd";
import { message } from "antd";
import type { NavigateFunction } from "react-router-dom";
import {
  addFavorite,
  createScrapeTasks,
  extractAudioTrack,
  extractKeyframes,
  markUnwatched,
  markWatched,
  transcodeAsync,
} from "../api/client";
import type { RecentPlaylistEntry } from "../lib/recentPlaylists";

export interface MediaMenuTarget {
  id: number;
}

export function buildMediaMenuItems(
  r: MediaMenuTarget,
  nav: NavigateFunction,
  extra?: {
    isWatched?: boolean;
    atrackDone?: boolean;
    keyframeDone?: boolean;
    onAddToPlaylist?: (mediaId: number) => void;
    recentPlaylists?: RecentPlaylistEntry[];
    onQuickAddToPlaylist?: (mediaId: number, playlistId: number) => void | Promise<void>;
    /** 收藏页：菜单底部「取消收藏」 */
    onUnfavorite?: (mediaId: number) => void;
    /** 标记观看状态成功后刷新列表（如收藏页） */
    afterToggleWatched?: () => void;
  },
): MenuProps {
  const isWatched = extra?.isWatched ?? false;
  const watchedLabel = isWatched ? "标记为未观看" : "标记为已观看";
  const atrackDone = extra?.atrackDone ?? false;
  const keyframeDone = extra?.keyframeDone ?? false;
  const onAddToPlaylist = extra?.onAddToPlaylist;
  const recentPlaylists = extra?.recentPlaylists ?? [];
  const onQuickAddToPlaylist = extra?.onQuickAddToPlaylist;
  const onUnfavorite = extra?.onUnfavorite;
  const afterToggleWatched = extra?.afterToggleWatched;

  const addToChildren: MenuProps["items"] = [
    {
      key: "addToCollection",
      label: "添加到收藏集...",
    },
    { type: "divider" as const },
    {
      key: "openAddToPlaylist",
      label: "添加到播放列表...",
      disabled: !onAddToPlaylist,
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
      { key: "detail", label: "详情" },
      { type: "divider" as const },
      {
        key: "addTo",
        label: "添加到",
        children: addToChildren,
      },
      { key: "toggleWatched", label: watchedLabel },
      { type: "divider" as const },
      { key: "refreshMetadata", label: "刷新元数据" },
      { key: "analyze", label: "分析" },
      { key: "optimize", label: "优化" },
      { type: "divider" as const },
      { key: "extractAudio", label: atrackDone ? "重新分离音轨" : "分离音轨" },
      { key: "extractKeyframes", label: keyframeDone ? "重新提取关键帧" : "提取关键帧" },
      { type: "divider" as const },
      { key: "viewHistory", label: "查看播放历史" },
      { key: "getInfo", label: "获取信息" },
      ...(onUnfavorite
        ? [
            { type: "divider" as const },
            { key: "unfavorite", label: "取消收藏", danger: true },
          ]
        : []),
    ],
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
            .then(() => message.success("已加入收藏集"))
            .catch(() => message.error("操作失败"));
          break;
        case "openAddToPlaylist":
          if (onAddToPlaylist) {
            onAddToPlaylist(r.id);
          } else {
            message.info("播放列表功能开发中");
          }
          break;
        case "toggleWatched":
          if (isWatched) {
            markUnwatched(r.id)
              .then(() => {
                message.success("已标记为未观看");
                afterToggleWatched?.();
              })
              .catch(() => message.error("操作失败"));
          } else {
            markWatched(r.id)
              .then(() => {
                message.success("已标记为已观看");
                afterToggleWatched?.();
              })
              .catch(() => message.error("操作失败"));
          }
          break;
        case "unfavorite":
          onUnfavorite?.(r.id);
          break;
        case "refreshMetadata":
          createScrapeTasks([r.id])
            .then(() => message.success("已创建刮削任务"))
            .catch(() => message.error("操作失败"));
          break;
        case "analyze":
          transcodeAsync(r.id, "analyze")
            .then(() => message.success("已创建分析任务"))
            .catch(() => message.error("操作失败"));
          break;
        case "optimize":
          transcodeAsync(r.id, "optimize")
            .then(() => message.success("已创建优化任务"))
            .catch(() => message.error("操作失败"));
          break;
        case "extractAudio":
          extractAudioTrack(r.id)
            .then(() => message.success("已创建音轨提取任务"))
            .catch(() => message.error("操作失败"));
          break;
        case "extractKeyframes":
          extractKeyframes(r.id)
            .then(() => message.success("已创建关键帧提取任务"))
            .catch(() => message.error("操作失败"));
          break;
        case "viewHistory":
          nav(`/detail/${r.id}`);
          break;
        case "getInfo":
          nav(`/detail/${r.id}`);
          break;
        default: {
          const sk = String(key);
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
