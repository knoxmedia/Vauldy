import { Button, Empty, Input, List, Modal, Spin, message } from "antd";
import { useEffect, useMemo, useState } from "react";
import {
  MediaItem,
  addFavoriteFolderItem,
  fetchFavoriteFolder,
  mediaPosterSrc,
} from "../api/client";
import { useT } from "../i18n";

interface AddToFavoriteFolderModalProps {
  open: boolean;
  folderId: number | null;
  candidates: MediaItem[];
  onClose: () => void;
  onAdded: () => void;
}

export default function AddToFavoriteFolderModal({
  open,
  folderId,
  candidates,
  onClose,
  onAdded,
}: AddToFavoriteFolderModalProps) {
  const t = useT();
  const [loading, setLoading] = useState(false);
  const [submitting, setSubmitting] = useState(false);
  const [query, setQuery] = useState("");
  const [existingIds, setExistingIds] = useState<Set<number>>(() => new Set());
  const [selectedIds, setSelectedIds] = useState<Set<number>>(() => new Set());

  useEffect(() => {
    if (!open || folderId == null) return;
    setQuery("");
    setSelectedIds(new Set());
    setLoading(true);
    void fetchFavoriteFolder(folderId)
      .then((folder) => {
        setExistingIds(new Set((folder.items ?? []).map((item) => item.media_id)));
      })
      .catch(() => {
        setExistingIds(new Set());
        message.error(t("components.add_to_favorite_folder_modal.load_failed"));
      })
      .finally(() => setLoading(false));
  }, [open, folderId, t]);

  const available = useMemo(() => {
    const q = query.trim().toLowerCase();
    return candidates.filter((item) => {
      if (existingIds.has(item.id)) return false;
      if (!q) return true;
      return (item.title || "").toLowerCase().includes(q);
    });
  }, [candidates, existingIds, query]);

  function toggle(id: number) {
    setSelectedIds((prev) => {
      const next = new Set(prev);
      if (next.has(id)) next.delete(id);
      else next.add(id);
      return next;
    });
  }

  async function handleConfirm() {
    if (folderId == null || selectedIds.size === 0) return;
    setSubmitting(true);
    let ok = 0;
    let fail = 0;
    for (const mediaId of selectedIds) {
      try {
        await addFavoriteFolderItem(folderId, mediaId);
        ok++;
      } catch {
        fail++;
      }
    }
    setSubmitting(false);
    if (ok === 0) {
      message.error(t("components.add_to_favorite_folder_modal.add_failed"));
      return;
    }
    message.success(
      fail > 0
        ? t("components.add_to_favorite_folder_modal.added_with_skip", { ok, fail })
        : t("components.add_to_favorite_folder_modal.added", { count: ok }),
    );
    onAdded();
    onClose();
  }

  return (
    <Modal
      open={open}
      title={t("components.add_to_favorite_folder_modal.title")}
      onCancel={onClose}
      destroyOnClose
      width={520}
      footer={[
        <Button key="cancel" onClick={onClose}>
          {t("components.add_to_favorite_folder_modal.cancel")}
        </Button>,
        <Button
          key="confirm"
          type="primary"
          disabled={selectedIds.size === 0}
          loading={submitting}
          onClick={() => void handleConfirm()}
        >
          {t("components.add_to_favorite_folder_modal.confirm")}
        </Button>,
      ]}
    >
      <Input
        allowClear
        value={query}
        placeholder={t("components.add_to_favorite_folder_modal.search_placeholder")}
        onChange={(e) => setQuery(e.target.value)}
        style={{ marginBottom: 12 }}
      />
      {loading ? (
        <div style={{ display: "flex", justifyContent: "center", padding: 24 }}>
          <Spin />
        </div>
      ) : available.length === 0 ? (
        <Empty description={t("components.add_to_favorite_folder_modal.empty")} />
      ) : (
        <List
          dataSource={available}
          style={{ maxHeight: 360, overflow: "auto" }}
          renderItem={(item) => {
            const selected = selectedIds.has(item.id);
            const cover = mediaPosterSrc(item) || item.poster_url;
            return (
              <List.Item
                style={{ cursor: "pointer", background: selected ? "rgba(0,164,220,0.12)" : undefined }}
                onClick={() => toggle(item.id)}
              >
                <List.Item.Meta
                  avatar={
                    <div
                      style={{
                        width: 48,
                        height: 72,
                        borderRadius: 4,
                        overflow: "hidden",
                        background: "#1a2229",
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
                  }
                  title={item.title || t("pages.favorites.untitled")}
                />
                {selected ? <span>{t("components.add_to_favorite_folder_modal.selected")}</span> : null}
              </List.Item>
            );
          }}
        />
      )}
    </Modal>
  );
}
