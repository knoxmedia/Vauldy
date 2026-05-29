import { Button, Input, Modal } from "antd";
import { useEffect, useState } from "react";
import { useT } from "../i18n";
import styles from "./FavoriteFolderFormModal.module.css";

export const FAVORITE_FOLDER_NAME_MAX = 15;

interface FavoriteFolderFormModalProps {
  open: boolean;
  mode: "create" | "edit";
  initialName?: string;
  submitting?: boolean;
  onClose: () => void;
  onSubmit: (name: string) => void | Promise<void>;
}

export default function FavoriteFolderFormModal({
  open,
  mode,
  initialName = "",
  submitting = false,
  onClose,
  onSubmit,
}: FavoriteFolderFormModalProps) {
  const t = useT();
  const [name, setName] = useState("");

  useEffect(() => {
    if (open) setName(initialName);
  }, [open, initialName]);

  const trimmed = name.trim();
  const canSubmit = trimmed.length > 0 && trimmed.length <= FAVORITE_FOLDER_NAME_MAX;

  return (
    <Modal
      open={open}
      title={
        mode === "create"
          ? t("components.favorite_folder_form_modal.title_create")
          : t("components.favorite_folder_form_modal.title_edit")
      }
      footer={null}
      onCancel={onClose}
      destroyOnClose
      className={styles.modal}
      width={420}
    >
      <Input
        className={styles.input}
        value={name}
        maxLength={FAVORITE_FOLDER_NAME_MAX}
        placeholder={t("components.favorite_folder_form_modal.name_placeholder")}
        onChange={(e) => setName(e.target.value)}
        onPressEnter={() => {
          if (canSubmit && !submitting) void onSubmit(trimmed);
        }}
        autoFocus
      />
      <Button
        type="primary"
        className={styles.confirmBtn}
        disabled={!canSubmit || submitting}
        loading={submitting}
        onClick={() => void onSubmit(trimmed)}
      >
        {t("components.favorite_folder_form_modal.confirm")}
      </Button>
    </Modal>
  );
}
