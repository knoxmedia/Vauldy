import { Button, Input, List, message, Modal, Spin } from "antd";
import { useEffect, useMemo, useState } from "react";
import { addPlaylistItem, createPlaylist, derivedVideoPosterSrc, fetchPlaylists, Playlist } from "../api/client";
import { useT } from "../i18n";

export interface PlaylistAddedInfo {
  id: number;
  name: string;
}

interface AddToPlaylistModalProps {
  /** 要加入列表的媒体 ID（单个或多个） */
  mediaIds: number[];
  open: boolean;
  onClose: () => void;
  /** 仅单项时预填新建列表名称 */
  defaultNewPlaylistName?: string;
  onAdded?: (playlist: PlaylistAddedInfo) => void;
}

export default function AddToPlaylistModal({
  mediaIds,
  open,
  onClose,
  defaultNewPlaylistName = "",
  onAdded,
}: AddToPlaylistModalProps) {
  const t = useT();
  const uniqueIds = useMemo(() => [...new Set(mediaIds)].filter((id) => id > 0), [mediaIds]);
  const count = uniqueIds.length;

  const [playlists, setPlaylists] = useState<Playlist[]>([]);
  const [loading, setLoading] = useState(false);
  const [creating, setCreating] = useState(false);
  const [newName, setNewName] = useState("");

  useEffect(() => {
    if (open) {
      setLoading(true);
      fetchPlaylists()
        .then(setPlaylists)
        .catch(() => message.error(t("components.add_to_playlist_modal.load_failed")))
        .finally(() => setLoading(false));
      setNewName(count === 1 ? defaultNewPlaylistName.trim() : "");
    }
  }, [open, defaultNewPlaylistName, count, t]);

  async function addAllToPlaylist(playlist: Pick<Playlist, "id" | "name">): Promise<boolean> {
    let ok = 0;
    let fail = 0;
    for (const mid of uniqueIds) {
      try {
        await addPlaylistItem(playlist.id, mid);
        ok++;
      } catch {
        fail++;
      }
    }
    if (ok === 0) {
      message.error(t("components.add_to_playlist_modal.add_failed_dup"));
      return false;
    }
    if (count > 1) {
      message.success(
        fail > 0
          ? t("components.add_to_playlist_modal.added_multi_with_skip", { ok, name: playlist.name, fail })
          : t("components.add_to_playlist_modal.added_multi", { ok, name: playlist.name })
      );
    } else {
      message.success(t("components.add_to_playlist_modal.added_single", { name: playlist.name }));
    }
    onAdded?.({ id: playlist.id, name: playlist.name });
    return true;
  }

  async function handleAddTo(playlist: Playlist) {
    if (uniqueIds.length === 0) return;
    const done = await addAllToPlaylist(playlist);
    if (done) onClose();
  }

  async function handleCreate() {
    const name = newName.trim();
    if (!name) {
      message.error(t("components.add_to_playlist_modal.name_required"));
      return;
    }
    if (uniqueIds.length === 0) return;
    setCreating(true);
    try {
      const id = await createPlaylist(name);
      const ok = await addAllToPlaylist({ id, name });
      if (ok) {
        setNewName("");
        onClose();
      }
    } catch (e: unknown) {
      message.error((e as Error).message || t("components.add_to_playlist_modal.create_failed"));
    } finally {
      setCreating(false);
    }
  }

  const modalTitle = count > 1
    ? t("components.add_to_playlist_modal.title_multi", { count })
    : t("components.add_to_playlist_modal.title_single");

  return (
    <Modal open={open && count > 0} title={modalTitle} onCancel={onClose} footer={null} destroyOnClose width={420}>
      {loading ? (
        <div style={{ textAlign: "center", padding: "24px 0" }}>
          <Spin />
        </div>
      ) : playlists.length === 0 ? (
        <div style={{ textAlign: "center", color: "#888", padding: "16px 0 8px" }}>
          {t("components.add_to_playlist_modal.empty_create_below")}
        </div>
      ) : (
        <List
          size="small"
          dataSource={playlists}
          style={{ maxHeight: 320, overflowY: "auto", marginBottom: 16 }}
          renderItem={(pl) => (
            <List.Item
              key={pl.id}
              style={{ cursor: "pointer", padding: "8px 0" }}
              onClick={() => void handleAddTo(pl)}
              onKeyDown={(e) => {
                if (e.key === "Enter" || e.key === " ") void handleAddTo(pl);
              }}
              tabIndex={0}
            >
              <div style={{ display: "flex", alignItems: "center", gap: 10, width: "100%" }}>
                <div
                  style={{
                    width: 44,
                    height: 44,
                    background: "linear-gradient(135deg, #2a2a3a 0%, #1a1a24 100%)",
                    borderRadius: 6,
                    overflow: "hidden",
                    flexShrink: 0,
                  }}
                >
                  {pl.first_media_id ? (
                    <img
                      src={derivedVideoPosterSrc(pl.first_media_id)}
                      alt=""
                      style={{ width: "100%", height: "100%", objectFit: "cover" }}
                    />
                  ) : null}
                </div>
                <div
                  style={{
                    flex: 1,
                    minWidth: 0,
                    color: "#e6edf3",
                    fontSize: 14,
                    fontWeight: 500,
                    whiteSpace: "nowrap",
                    overflow: "hidden",
                    textOverflow: "ellipsis",
                  }}
                >
                  {pl.name}
                </div>
              </div>
            </List.Item>
          )}
        />
      )}

      <div style={{ display: "flex", gap: 8, borderTop: "1px solid rgba(255,255,255,0.08)", paddingTop: 12 }}>
        <Input
          placeholder={t("components.add_to_playlist_modal.create_placeholder")}
          value={newName}
          onChange={(e) => setNewName(e.target.value)}
          onPressEnter={handleCreate}
          maxLength={100}
          style={{ flex: 1 }}
        />
        <Button type="primary" loading={creating} onClick={handleCreate} disabled={!newName.trim()}>
          {t("components.add_to_playlist_modal.create_btn")}
        </Button>
      </div>
    </Modal>
  );
}
