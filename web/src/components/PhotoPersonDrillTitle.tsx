import { Input, message } from "antd";
import type { InputRef } from "antd";
import { useEffect, useRef, useState } from "react";
import { updatePhotoPersonName } from "../api/client";
import styles from "../pages/PhotoBrowse.module.css";

type Props = {
  libraryId: number;
  personId: string;
  name: string;
  onRenamed: (name: string) => void;
};

export default function PhotoPersonDrillTitle({ libraryId, personId, name, onRenamed }: Props) {
  const [editing, setEditing] = useState(false);
  const [draft, setDraft] = useState(name);
  const [saving, setSaving] = useState(false);
  const inputRef = useRef<InputRef>(null);

  useEffect(() => {
    setDraft(name);
    setEditing(false);
  }, [name, personId]);

  useEffect(() => {
    if (editing) inputRef.current?.focus({ cursor: "all" });
  }, [editing]);

  async function save() {
    if (saving) return;
    const next = draft.trim() || "未命名人物";
    setEditing(false);
    if (next === name) {
      setDraft(name);
      return;
    }
    setSaving(true);
    try {
      const res = await updatePhotoPersonName(libraryId, Number(personId), next);
      onRenamed(res.name || next);
      message.success("人物名称已更新");
    } catch (e: unknown) {
      setDraft(name);
      message.error((e as Error).message || "保存失败");
    } finally {
      setSaving(false);
    }
  }

  function cancel() {
    setDraft(name);
    setEditing(false);
  }

  if (editing) {
    return (
      <Input
        ref={inputRef}
        size="small"
        className={styles.drillTitleInput}
        value={draft}
        disabled={saving}
        maxLength={64}
        onChange={(e) => setDraft(e.target.value)}
        onBlur={() => void save()}
        onPressEnter={() => void save()}
        onKeyDown={(e) => {
          if (e.key === "Escape") {
            e.preventDefault();
            cancel();
          }
        }}
      />
    );
  }

  return (
    <button
      type="button"
      className={`${styles.drillTitle} ${styles.drillTitleEditable}`}
      onClick={() => setEditing(true)}
      title="点击编辑人物名称"
    >
      {name}
    </button>
  );
}
