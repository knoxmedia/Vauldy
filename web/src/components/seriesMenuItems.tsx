import type { MenuProps } from "antd";
import { Modal, message } from "antd";
import { deleteMedia, unmatchMedia } from "../api/client";
import type { RecentPlaylistEntry } from "../lib/recentPlaylists";
import { tGlobal as t } from "../i18n";

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
      label: t("components.series_menu.add_to_playlist"),
      disabled: !onAddToPlaylist || allMediaIds.length === 0,
    },
  ];
  if (recentPlaylists.length > 0 && onQuickAddToPlaylist) {
    addToChildren.push({
      type: "group",
      label: t("components.series_menu.recent"),
      children: recentPlaylists.slice(0, 3).map((pl) => ({
        key: `recentPlaylist:${pl.id}`,
        label: pl.name,
      })),
    });
  }

  const items: MenuProps["items"] = [
    {
      key: "addTo",
      label: t("components.series_menu.add_to"),
      children: addToChildren,
    },
    { type: "divider" as const },
    ...(onOpenMatch && !scraped ? [{ key: "match", label: t("components.series_menu.match") }] : []),
    ...(onOpenMatch && scraped
      ? [
          { key: "fixMatch", label: t("components.series_menu.fix_match") },
          { key: "unmatch", label: t("components.series_menu.unmatch") },
        ]
      : []),
    { type: "divider" as const },
    { key: "deleteSeries", label: t("components.series_menu.delete_series"), danger: true },
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
            message.success(t("components.series_menu.unmatched"));
            await afterUnmatch?.();
          } catch (e: unknown) {
            message.error((e as Error).message || t("components.series_menu.unmatch_failed"));
          }
        })();
        return;
      }
      if (key === "deleteSeries") {
        if (allMediaIds.length === 0) {
          message.warning(t("components.series_menu.no_files_to_delete"));
          return;
        }
        Modal.confirm({
          title: t("components.series_menu.delete_modal_title"),
          centered: true,
          okText: t("components.series_menu.delete_modal_ok"),
          cancelText: t("components.series_menu.delete_modal_cancel"),
          okButtonProps: { danger: true },
          content: (
            <div>
              <p style={{ marginBottom: 8 }}>
                {t("components.series_menu.delete_warning", { count: allMediaIds.length })}
              </p>
            </div>
          ),
          onOk: async () => {
            try {
              for (const id of allMediaIds) {
                await deleteMedia(id);
              }
              message.success(t("components.series_menu.deleted"));
              await afterDelete?.();
            } catch (e: unknown) {
              message.error((e as Error).message || t("components.series_menu.delete_failed"));
              throw e;
            }
          },
        });
      }
    },
  };
}
