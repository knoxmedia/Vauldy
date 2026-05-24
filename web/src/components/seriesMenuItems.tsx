import type { MenuProps } from "antd";
import { Modal, message } from "antd";
import { deleteMedia, unmatchMedia } from "../api/client";
import type { RecentPlaylistEntry } from "../lib/recentPlaylists";

export function buildSeriesMenuItems(
  extra: {
    scraped?: boolean;
    allMediaIds: number[];
    onAddToPlaylist?: () => void;
    onOpenMatch?: () => void;
    afterUnmatch?: () => void | Promise<void>;
    afterDelete?: () => void | Promise<void>;
    recentPlaylists?: RecentPlaylistEntry[];
    onQuickAddToPlaylist?: (mediaIds: number[], playlistId: number) => void | Promise<void>;
  },
): MenuProps {
  const {
    scraped = false,
    allMediaIds,
    onAddToPlaylist,
    onOpenMatch,
    afterUnmatch,
    afterDelete,
    recentPlaylists = [],
    onQuickAddToPlaylist,
  } = extra;

  const addToChildren: MenuProps["items"] = [
    {
      key: "openAddToPlaylist",
      label: "添加到播放列表...",
      disabled: !onAddToPlaylist || allMediaIds.length === 0,
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

  const items: MenuProps["items"] = [
    {
      key: "addTo",
      label: "添加到",
      children: addToChildren,
    },
    { type: "divider" as const },
    ...(onOpenMatch && !scraped ? [{ key: "match", label: "匹配" }] : []),
    ...(onOpenMatch && scraped
      ? [
          { key: "fixMatch", label: "修正匹配" },
          { key: "unmatch", label: "取消匹配" },
        ]
      : []),
    { type: "divider" as const },
    { key: "deleteSeries", label: "删除剧集", danger: true },
  ];

  return {
    items,
    onClick: ({ key, domEvent }) => {
      domEvent.stopPropagation();
      if (key === "openAddToPlaylist") {
        onAddToPlaylist?.();
        return;
      }
      if (key.startsWith("recentPlaylist:")) {
        const plId = Number(key.slice("recentPlaylist:".length));
        if (plId > 0) void onQuickAddToPlaylist?.(allMediaIds, plId);
        return;
      }
      if (key === "match" || key === "fixMatch") {
        onOpenMatch?.();
        return;
      }
      if (key === "unmatch") {
        void (async () => {
          try {
            for (const id of allMediaIds) {
              await unmatchMedia(id);
            }
            message.success("已取消匹配");
            await afterUnmatch?.();
          } catch (e: unknown) {
            message.error((e as Error).message || "取消匹配失败");
          }
        })();
        return;
      }
      if (key === "deleteSeries") {
        if (allMediaIds.length === 0) {
          message.warning("暂无可删除的媒体文件");
          return;
        }
        Modal.confirm({
          title: "删除剧集",
          centered: true,
          okText: "确定删除",
          cancelText: "取消",
          okButtonProps: { danger: true },
          content: (
            <div>
              <p style={{ marginBottom: 8 }}>
                将从文件系统和媒体库删除此剧集的全部 {allMediaIds.length} 个视频文件，确定继续吗？
              </p>
            </div>
          ),
          onOk: async () => {
            try {
              for (const id of allMediaIds) {
                await deleteMedia(id);
              }
              message.success("剧集已删除");
              await afterDelete?.();
            } catch (e: unknown) {
              message.error((e as Error).message || "删除失败");
              throw e;
            }
          },
        });
      }
    },
  };
}
