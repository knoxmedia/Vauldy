import type { MenuProps } from "antd";
import { Modal, message } from "antd";
import type { NavigateFunction } from "react-router-dom";
import {
  addFavorite,
  createScrapeTasks,
  deleteMedia,
  extractAudioTrack,
  extractKeyframes,
  fetchMediaDeletionPlan,
  markUnwatched,
  markWatched,
  transcodeAsync,
  unmatchMedia,
} from "../api/client";
import type { RecentPlaylistEntry } from "../lib/recentPlaylists";

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
      title: "删除媒体",
      centered: true,
      okText: "确定",
      cancelText: "取消",
      okButtonProps: { danger: true },
      content: (
        <div>
          <p style={{ marginBottom: 8 }}>
            删除此项目将从文件系统和媒体库同时删除，将删除以下文件：
          </p>
          {files.length > 0 ? (
            <ul style={{ margin: "0 0 12px", paddingLeft: 20, wordBreak: "break-all" }}>
              {files.map((f) => (
                <li key={f}>{f}</li>
              ))}
            </ul>
          ) : (
            <p style={{ margin: "0 0 12px", color: "#8c8c8c" }}>（无可用文件路径）</p>
          )}
          <p style={{ marginBottom: 0 }}>您确定要继续吗？</p>
        </div>
      ),
      onOk: async () => {
        try {
          await deleteMedia(target.id);
          message.success("已删除");
          await afterDelete?.();
        } catch (err: unknown) {
          message.error((err as Error).message || "删除失败");
          throw err;
        }
      },
    });
  })();
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
  const afterDelete = extra?.afterDelete;
  const hideDelete = extra?.hideDelete ?? false;
  const onRemoveFromContinueWatching = extra?.onRemoveFromContinueWatching;
  const scraped = extra?.scraped ?? false;
  const onOpenMatch = extra?.onOpenMatch;
  const afterUnmatch = extra?.afterUnmatch;

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
      ...(onRemoveFromContinueWatching
        ? [{ key: "removeFromContinueWatching", label: "从继续观看移除" }]
        : []),
      ...(onOpenMatch && !scraped ? [{ key: "match", label: "匹配" }] : []),
      ...(onOpenMatch && scraped
        ? [
            { key: "fixMatch", label: "修改匹配" },
            { key: "unmatch", label: "取消匹配" },
          ]
        : []),
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
      ...(!hideDelete
        ? [
            { type: "divider" as const },
            { key: "delete", label: "删除", danger: true },
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
        case "removeFromContinueWatching":
          void Promise.resolve(onRemoveFromContinueWatching?.(r.id)).catch(() =>
            message.error("操作失败"),
          );
          break;
        case "match":
        case "fixMatch":
          onOpenMatch?.(r.id);
          break;
        case "unmatch":
          Modal.confirm({
            title: "取消匹配",
            centered: true,
            okText: "确定",
            cancelText: "取消",
            content: "将清除该媒体的刮削元数据，标题将恢复为原始文件名。",
            onOk: async () => {
              try {
                await unmatchMedia(r.id);
                message.success("已取消匹配");
                await afterUnmatch?.();
              } catch (err: unknown) {
                message.error((err as Error).message || "操作失败");
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
