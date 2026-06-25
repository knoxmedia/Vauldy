import { Button, Input, List, message, Modal, Spin } from "antd";
import { useEffect, useState } from "react";
import {
  FavoriteFolder,
  addFavoriteFolderItem,
  createFavoriteFolder,
  fetchFavoriteFolders,
  mediaPosterSrc,
} from "../api/client";
import { favoriteFolderCoverSrc } from "../lib/favoriteCategories";
import { useT } from "../i18n";

export interface FavoriteFolderAddedInfo {
  id: number;
  name: string;
}

interface AddToFavoriteFolderPickerModalProps {
  mediaId: number;
  open: boolean;
  onClose: () => void;
  onAdded?: (folder: FavoriteFolderAddedInfo) => void;
}

export default function AddToFavoriteFolderPickerModal({
  mediaId,
  open,
  onClose,
  onAdded,
}: AddToFavoriteFolderPickerModalProps) {
  const t = useT();
  const [folders, setFolders] = useState<FavoriteFolder[]>([]);
  const [loading, setLoading] = useState(false);
  const [creating, setCreating] = useState(false);
  const [newName, setNewName] = useState("");

  useEffect(() => {
    if (!open || mediaId <= 0) return;
    setLoading(true);
    fetchFavoriteFolders()
      .then(setFolders)
      .catch(() => message.error(t("components.add_to_favorite_folder_picker_modal.load_failed")))
      .finally(() => setLoading(false));
    setNewName("");
  }, [open, mediaId, t]);

  async function addToFolder(folder: Pick<FavoriteFolder, "id" | "name">): Promise<boolean> {
    try {
      await addFavoriteFolderItem(folder.id, mediaId);
      message.success(t("components.add_to_favorite_folder_picker_modal.added_single", { name: folder.name }));
      onAdded?.({ id: folder.id, name: folder.name });
      return true;
    } catch {
      message.error(t("components.add_to_favorite_folder_picker_modal.add_failed_dup"));
      return false;
    }
  }

  async function handleAddTo(folder: FavoriteFolder) {
    if (mediaId <= 0) return;
    const done = await addToFolder(folder);
    if (done) onClose();
  }

  async function handleCreate() {
    const name = newName.trim();
    if (!name) {
      message.error(t("components.add_to_favorite_folder_picker_modal.name_required"));
      return;
    }
    if (mediaId <= 0) return;
    setCreating(true);
    try {
      const id = await createFavoriteFolder(name);
      const ok = await addToFolder({ id, name });
      if (ok) {
        setNewName("");
        onClose();
      }
    } catch (e: unknown) {
      message.error((e as Error).message || t("components.add_to_favorite_folder_picker_modal.create_failed"));
    } finally {
      setCreating(false);
    }
  }

  function folderCover(folder: FavoriteFolder): string {
    const cover = favoriteFolderCoverSrc(folder);
    if (cover) return cover;
    if (folder.first_media_id) return mediaPosterSrc({ id: folder.first_media_id, poster_url: "" });
    return "";
  }

  return (
    <Modal
      open={open && mediaId > 0}
      title={t("components.add_to_favorite_folder_picker_modal.title_single")}
      onCancel={onClose}
      footer={null}
      destroyOnClose
      width={420}
    >
      {loading ? (
        <div style={{ textAlign: "center", padding: "24px 0" }}>
          <Spin />
        </div>
      ) : folders.length === 0 ? (
        <div style={{ textAlign: "center", color: "#888", padding: "16px 0 8px" }}>
          {t("components.add_to_favorite_folder_picker_modal.empty_create_below")}
        </div>
      ) : (
        <List
          size="small"
          dataSource={folders}
          style={{ maxHeight: 320, overflowY: "auto", marginBottom: 16 }}
          renderItem={(folder) => {
            const cover = folderCover(folder);
            return (
              <List.Item
                key={folder.id}
                style={{ cursor: "pointer", padding: "8px 0" }}
                onClick={() => void handleAddTo(folder)}
                onKeyDown={(e) => {
                  if (e.key === "Enter" || e.key === " ") void handleAddTo(folder);
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
                    {cover ? (
                      <img
                        src={cover}
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
                    {folder.name}
                  </div>
                </div>
              </List.Item>
            );
          }}
        />
      )}

      <div style={{ display: "flex", gap: 8, borderTop: "1px solid rgba(255,255,255,0.08)", paddingTop: 12 }}>
        <Input
          placeholder={t("components.add_to_favorite_folder_picker_modal.create_placeholder")}
          value={newName}
          onChange={(e) => setNewName(e.target.value)}
          onPressEnter={handleCreate}
          maxLength={15}
          style={{ flex: 1 }}
        />
        <Button type="primary" loading={creating} onClick={handleCreate} disabled={!newName.trim()}>
          {t("components.add_to_favorite_folder_picker_modal.create_btn")}
        </Button>
      </div>
    </Modal>
  );
}
