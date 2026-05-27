import {
  CloseOutlined,
  DownloadOutlined,
  LeftOutlined,
  RightOutlined,
  ZoomInOutlined,
  ZoomOutOutlined,
} from "@ant-design/icons";
import { Button, Space, Spin, Tag, message } from "antd";
import { useCallback, useEffect, useRef, useState } from "react";
import { MediaItem, photoMediumSrc, photoOriginalSrc, updatePhotoTags } from "../api/client";
import styles from "../pages/PhotoBrowse.module.css";

type Props = {
  items: MediaItem[];
  index: number;
  onClose: () => void;
  onChangeIndex: (index: number) => void;
  onTagsUpdated?: (mediaId: number, tags: string[]) => void;
};

function fmtTakenAt(v?: string): string {
  if (!v) return "";
  return v.replace("T", " ").slice(0, 19);
}

export default function PhotoLightbox({ items, index, onClose, onChangeIndex, onTagsUpdated }: Props) {
  const item = items[index];
  const [scale, setScale] = useState(1);
  const [offset, setOffset] = useState({ x: 0, y: 0 });
  const [dragging, setDragging] = useState(false);
  const dragStart = useRef({ x: 0, y: 0, ox: 0, oy: 0 });
  const [loading, setLoading] = useState(true);
  const [useOriginal, setUseOriginal] = useState(false);
  const [editingTags, setEditingTags] = useState(false);
  const [tagDraft, setTagDraft] = useState("");
  const [localTags, setLocalTags] = useState<string[]>([]);
  const tagEditRef = useRef<HTMLDivElement>(null);

  const hasPrev = index > 0;
  const hasNext = index < items.length - 1;

  const resetView = useCallback(() => {
    setScale(1);
    setOffset({ x: 0, y: 0 });
    setUseOriginal(false);
    setLoading(true);
  }, []);

  useEffect(() => {
    resetView();
    setLocalTags(item?.photo_tags ?? []);
    setEditingTags(false);
    setTagDraft("");
  }, [index, resetView, item?.photo_tags]);

  const cancelTagEdit = useCallback(() => {
    setEditingTags(false);
    setTagDraft("");
  }, []);

  useEffect(() => {
    if (!editingTags) return;
    function onPointerDown(e: PointerEvent) {
      const el = tagEditRef.current;
      if (el && !el.contains(e.target as Node)) {
        cancelTagEdit();
      }
    }
    document.addEventListener("pointerdown", onPointerDown);
    return () => document.removeEventListener("pointerdown", onPointerDown);
  }, [editingTags, cancelTagEdit]);

  useEffect(() => {
    function onKey(e: KeyboardEvent) {
      if (e.key === "Escape") onClose();
      if (e.key === "ArrowLeft" && hasPrev) onChangeIndex(index - 1);
      if (e.key === "ArrowRight" && hasNext) onChangeIndex(index + 1);
      if (e.key === "+" || e.key === "=") setScale((s) => Math.min(4, s + 0.25));
      if (e.key === "-") setScale((s) => Math.max(0.25, s - 0.25));
      if (e.key === "0" && e.ctrlKey) resetView();
      if (e.key === "i" || e.key === "I") setEditingTags((v) => !v);
    }
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [hasPrev, hasNext, index, onChangeIndex, onClose, resetView]);

  useEffect(() => {
    const prev = index > 0 ? photoMediumSrc(items[index - 1].id) : null;
    const next = index < items.length - 1 ? photoMediumSrc(items[index + 1].id) : null;
    if (prev) {
      const img = new Image();
      img.src = prev;
    }
    if (next) {
      const img = new Image();
      img.src = next;
    }
  }, [index, items]);

  if (!item) return null;

  const downloadUrl = `${photoOriginalSrc(item.id)}${photoOriginalSrc(item.id).includes("?") ? "&" : "?"}download=1`;

  const src = useOriginal ? photoOriginalSrc(item.id) : photoMediumSrc(item.id);

  function onPointerDown(e: React.PointerEvent) {
    if (scale <= 1) return;
    setDragging(true);
    dragStart.current = { x: e.clientX, y: e.clientY, ox: offset.x, oy: offset.y };
    (e.currentTarget as HTMLElement).setPointerCapture(e.pointerId);
  }

  function onPointerMove(e: React.PointerEvent) {
    if (!dragging) return;
    setOffset({
      x: dragStart.current.ox + (e.clientX - dragStart.current.x),
      y: dragStart.current.oy + (e.clientY - dragStart.current.y),
    });
  }

  function onPointerUp() {
    setDragging(false);
  }

  function onWheel(e: React.WheelEvent) {
    e.preventDefault();
    const delta = e.deltaY > 0 ? -0.1 : 0.1;
    setScale((s) => Math.min(4, Math.max(0.25, s + delta)));
  }

  async function saveTags() {
    const tags = tagDraft
      .split(/[,，、\s]+/)
      .map((t) => t.trim())
      .filter(Boolean);
    try {
      await updatePhotoTags(item.id, tags);
      setLocalTags(tags);
      onTagsUpdated?.(item.id, tags);
      message.success("分类已更新");
      setEditingTags(false);
    } catch (e: unknown) {
      message.error((e as Error).message || "保存失败");
    }
  }

  return (
    <div className={styles.photoLightbox} role="dialog" aria-modal="true" aria-label="照片预览">
      <div className={styles.toolbar}>
        <span className={styles.title}>{item.title || "未命名"}</span>
        <Space>
          <Button
            type="text"
            icon={<ZoomOutOutlined />}
            aria-label="缩小"
            onClick={() => setScale((s) => Math.max(0.25, s - 0.25))}
            style={{ color: "#fff" }}
          />
          <Button
            type="text"
            icon={<ZoomInOutlined />}
            aria-label="放大"
            onClick={() => setScale((s) => Math.min(4, s + 0.25))}
            style={{ color: "#fff" }}
          />
          <Button
            type="text"
            icon={<DownloadOutlined />}
            aria-label="下载"
            style={{ color: "#fff" }}
            href={downloadUrl}
            target="_blank"
            rel="noreferrer"
          />
          <Button type="text" icon={<CloseOutlined />} aria-label="关闭" onClick={onClose} style={{ color: "#fff" }} />
        </Space>
      </div>

      {hasPrev ? (
        <button type="button" className={`${styles.navBtn} ${styles.navPrev}`} aria-label="上一张" onClick={() => onChangeIndex(index - 1)}>
          <LeftOutlined />
        </button>
      ) : null}
      {hasNext ? (
        <button type="button" className={`${styles.navBtn} ${styles.navNext}`} aria-label="下一张" onClick={() => onChangeIndex(index + 1)}>
          <RightOutlined />
        </button>
      ) : null}

      <div className={styles.stage} onWheel={onWheel}>
        <div
          className={`${styles.imageWrap} ${dragging ? styles.imageWrapDragging : ""}`}
          onPointerDown={onPointerDown}
          onPointerMove={onPointerMove}
          onPointerUp={onPointerUp}
          onPointerCancel={onPointerUp}
        >
          {loading ? (
            <Spin size="large" />
          ) : null}
          <img
            className={styles.image}
            src={src}
            alt={item.title || ""}
            style={{
              transform: `translate(${offset.x}px, ${offset.y}px) scale(${scale})`,
              opacity: loading ? 0 : 1,
            }}
            onLoad={() => setLoading(false)}
            onDoubleClick={() => {
              if (useOriginal) resetView();
              else setUseOriginal(true);
            }}
            draggable={false}
          />
        </div>
      </div>

      <div className={styles.footer}>
        <div className={styles.meta}>
          <span>
            {item.width && item.height ? `${item.width} × ${item.height}` : ""}
            {item.photo_taken_at ? ` · ${fmtTakenAt(item.photo_taken_at)}` : ""}
          </span>
          <div className={styles.footerTagRow}>
            {localTags.map((t) => (
              <Tag key={t} color="blue">
                {t}
              </Tag>
            ))}
            {!editingTags ? (
              <Button type="link" size="small" onClick={() => { setEditingTags(true); setTagDraft(localTags.join("、")); }} style={{ padding: 0, height: "auto" }}>
                编辑分类 (I)
              </Button>
            ) : (
              <div ref={tagEditRef}>
                <Space.Compact style={{ marginTop: 4 }}>
                  <input
                    className={styles.tagInput}
                    value={tagDraft}
                    onChange={(e) => setTagDraft(e.target.value)}
                    placeholder="人物、风景（逗号分隔）"
                  />
                  <Button size="small" type="primary" onClick={() => void saveTags()}>
                    保存
                  </Button>
                </Space.Compact>
              </div>
            )}
          </div>
          <span className={styles.counter}>
            {index + 1} / {items.length}
            {useOriginal ? " · 原图" : " · 双击加载原图"}
          </span>
        </div>
      </div>
    </div>
  );
}
