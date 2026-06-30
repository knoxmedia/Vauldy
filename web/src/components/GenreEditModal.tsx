import { Input, Modal, Spin, Typography, message } from "antd";
import { useEffect, useState } from "react";
import { GenreSummary, updateLibraryGenre } from "../api/client";
import { useT } from "../i18n";

const { Text } = Typography;

export interface GenreEditModalProps {
  genre: GenreSummary | null;
  libraryId: number;
  open: boolean;
  onClose: () => void;
  onSaved?: (update: { old_name: string; genre: string }) => void;
}

export default function GenreEditModal({ genre, libraryId, open, onClose, onSaved }: GenreEditModalProps) {
  const t = useT();
  const [loading, setLoading] = useState(false);
  const [saving, setSaving] = useState(false);
  const [name, setName] = useState("");

  useEffect(() => {
    if (!open || !genre) return;
    setLoading(true);
    setName(genre.genre || "");
    setLoading(false);
  }, [open, genre]);

  async function handleSave() {
    if (!genre) return;
    const oldName = genre.genre.trim();
    const newName = name.trim();
    if (!newName) {
      message.warning(t("components.genre_edit_modal.name_required"));
      return;
    }
    setSaving(true);
    try {
      const data = await updateLibraryGenre(libraryId, { old_name: oldName, new_name: newName });
      message.success(t("components.genre_edit_modal.saved"));
      onSaved?.({ old_name: oldName, genre: data.genre || newName });
      onClose();
    } catch (e: unknown) {
      message.error((e as Error).message || t("components.genre_edit_modal.save_failed"));
    } finally {
      setSaving(false);
    }
  }

  return (
    <Modal
      title={t("components.genre_edit_modal.title")}
      open={open}
      onCancel={onClose}
      onOk={() => void handleSave()}
      okText={t("components.genre_edit_modal.ok")}
      cancelText={t("components.genre_edit_modal.cancel")}
      confirmLoading={saving}
      destroyOnClose
      centered
      width={480}
    >
      {loading ? (
        <div style={{ display: "flex", justifyContent: "center", padding: 32 }}>
          <Spin />
        </div>
      ) : (
        <div>
          <Text type="secondary">{t("components.genre_edit_modal.label_name")}</Text>
          <Input
            value={name}
            onChange={(e) => setName(e.target.value)}
            placeholder={t("components.genre_edit_modal.name_placeholder")}
          />
        </div>
      )}
    </Modal>
  );
}
