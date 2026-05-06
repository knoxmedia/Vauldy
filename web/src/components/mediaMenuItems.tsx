import type { MenuProps } from "antd";
import { message } from "antd";
import type { NavigateFunction } from "react-router-dom";
import {
  addFavorite,
  createScrapeTasks,
  markUnwatched,
  markWatched,
  transcodeAsync,
} from "../api/client";

export interface MediaMenuTarget {
  id: number;
}

export function buildMediaMenuItems(
  r: MediaMenuTarget,
  nav: NavigateFunction,
  extra?: { isWatched?: boolean },
): MenuProps {
  const isWatched = extra?.isWatched ?? false;
  const watchedLabel = isWatched ? "标记为未观看" : "标记为已观看";

  return {
    items: [
      { key: "play", label: "播放" },
      { key: "detail", label: "详情" },
      { type: "divider" as const },
      {
        key: "addTo",
        label: "添加到",
        children: [
          { key: "addToCollection", label: "收藏集" },
          { key: "addToPlaylist", label: "播放列表" },
        ],
      },
      { key: "toggleWatched", label: watchedLabel },
      { type: "divider" as const },
      { key: "refreshMetadata", label: "刷新元数据" },
      { key: "analyze", label: "分析" },
      { key: "optimize", label: "优化" },
      { type: "divider" as const },
      { key: "viewHistory", label: "查看播放历史" },
      { key: "getInfo", label: "获取信息" },
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
        case "addToPlaylist":
          message.info("播放列表功能开发中");
          break;
        case "toggleWatched":
          if (isWatched) {
            markUnwatched(r.id)
              .then(() => message.success("已标记为未观看"))
              .catch(() => message.error("操作失败"));
          } else {
            markWatched(r.id)
              .then(() => message.success("已标记为已观看"))
              .catch(() => message.error("操作失败"));
          }
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
        case "viewHistory":
          nav(`/detail/${r.id}`);
          break;
        case "getInfo":
          nav(`/detail/${r.id}`);
          break;
      }
    },
  };
}
