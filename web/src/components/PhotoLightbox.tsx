import {
  CloseOutlined,
  DownloadOutlined,
  LeftOutlined,
  RightOutlined,
  RotateLeftOutlined,
  RotateRightOutlined,
  StarFilled,
  StarOutlined,
  ZoomInOutlined,
  ZoomOutOutlined,
} from "@ant-design/icons";
import { Button, Space, Spin, Tag, message } from "antd";
import { useCallback, useEffect, useRef, useState } from "react";
import { createPortal } from "react-dom";
import {
  MediaItem,
  addFavorite,
  fetchFavoriteStatus,
  photoMediumSrc,
  photoOriginalSrc,
  removeFavorite,
  updatePhotoTags,
} from "../api/client";
import { formatServerDateTime } from "../lib/datetime";
import { useT } from "../i18n";
import styles from "../pages/PhotoBrowse.module.css";

type Props = {
  items: MediaItem[];
  index: number;
  onClose: () => void;
  onChangeIndex: (index: number) => void;
  onTagsUpdated?: (mediaId: number, tags: string[]) => void;
};

function fmtTakenAt(v?: string): string {
  return formatServerDateTime(v, { empty: "" });
}

function computeFitScale(imgW: number, imgH: number, stageW: number, stageH: number, rotation: number): number {
  if (imgW <= 0 || imgH <= 0 || stageW <= 0 || stageH <= 0) return 1;
  const rot = ((rotation % 360) + 360) % 360;
  const boundsW = rot === 90 || rot === 270 ? imgH : imgW;
  const boundsH = rot === 90 || rot === 270 ? imgW : imgH;
  return Math.min(stageW / boundsW, stageH / boundsH);
}

export default function PhotoLightbox({ items, index, onClose, onChangeIndex, onTagsUpdated }: Props) {
  const t = useT();
  const item = items[index];
  const stageRef = useRef<HTMLDivElement>(null);
  const [userZoom, setUserZoom] = useState(1);
  const [fitScale, setFitScale] = useState(1);
  const [rotation, setRotation] = useState(0);
  const [offset, setOffset] = useState({ x: 0, y: 0 });
  const [imageSize, setImageSize] = useState({ w: 0, h: 0 });
  const [stageSize, setStageSize] = useState({ w: 0, h: 0 });
  const [dragging, setDragging] = useState(false);
  const dragStart = useRef({ x: 0, y: 0, ox: 0, oy: 0 });
  const [loading, setLoading] = useState(true);
  const [useOriginal, setUseOriginal] = useState(false);
  const [editingTags, setEditingTags] = useState(false);
  const [tagDraft, setTagDraft] = useState("");
  const [localTags, setLocalTags] = useState<string[]>([]);
  const [favorited, setFavorited] = useState(false);
  const [favoriteBusy, setFavoriteBusy] = useState(false);
  const tagEditRef = useRef<HTMLDivElement>(null);

  const hasPrev = index > 0;
  const hasNext = index < items.length - 1;
  const displayScale = fitScale * userZoom;

  const resetView = useCallback(() => {
    setUserZoom(1);
    setRotation(0);
    setOffset({ x: 0, y: 0 });
    setUseOriginal(false);
    setLoading(true);
    setImageSize({ w: 0, h: 0 });
  }, []);

  useEffect(() => {
    setFitScale(computeFitScale(imageSize.w, imageSize.h, stageSize.w, stageSize.h, rotation));
  }, [imageSize, stageSize, rotation]);

  useEffect(() => {
    const el = stageRef.current;
    if (!el) return;
    const update = () => {
      const rect = el.getBoundingClientRect();
      setStageSize({ w: rect.width, h: rect.height });
    };
    update();
    const ro = new ResizeObserver(update);
    ro.observe(el);
    return () => ro.disconnect();
  }, []);

  function rotateBy(delta: number) {
    setRotation((r) => (r + delta + 360) % 360);
    setOffset({ x: 0, y: 0 });
    setUserZoom(1);
  }

  useEffect(() => {
    resetView();
    setLocalTags(item?.photo_tags ?? []);
    setEditingTags(false);
    setTagDraft("");
  }, [index, resetView, item?.photo_tags]);

  useEffect(() => {
    if (!item?.id) {
      setFavorited(false);
      return;
    }
    let cancelled = false;
    void fetchFavoriteStatus(item.id)
      .then((v) => {
        if (!cancelled) setFavorited(v);
      })
      .catch(() => {
        if (!cancelled) setFavorited(false);
      });
    return () => {
      cancelled = true;
    };
  }, [item?.id]);

  useEffect(() => {
    setLoading(true);
    setImageSize({ w: 0, h: 0 });
  }, [item?.id, useOriginal]);

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
    document.body.classList.add("photo-lightbox-open");
    return () => {
      document.body.classList.remove("photo-lightbox-open");
    };
  }, []);

  useEffect(() => {
    function onKey(e: KeyboardEvent) {
      if (e.key === "Escape") onClose();
      if (e.key === "ArrowLeft" && hasPrev) onChangeIndex(index - 1);
      if (e.key === "ArrowRight" && hasNext) onChangeIndex(index + 1);
      if (e.key === "+" || e.key === "=") setUserZoom((s) => Math.min(4, s + 0.25));
      if (e.key === "-") setUserZoom((s) => Math.max(0.25, s - 0.25));
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
    if (userZoom <= 1) return;
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
    setUserZoom((s) => Math.min(4, Math.max(0.25, s + delta)));
  }

  async function onToggleFavorite() {
    if (!item?.id || favoriteBusy) return;
    setFavoriteBusy(true);
    try {
      if (favorited) {
        await removeFavorite(item.id);
        setFavorited(false);
        message.success(t("components.photo_lightbox.unfavorited"));
      } else {
        await addFavorite(item.id);
        setFavorited(true);
        message.success(t("components.photo_lightbox.favorited"));
      }
    } catch {
      message.error(t("components.photo_lightbox.favorite_failed"));
    } finally {
      setFavoriteBusy(false);
    }
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
      message.success(t("components.photo_lightbox.tags_updated"));
      setEditingTags(false);
    } catch (e: unknown) {
      message.error((e as Error).message || t("components.photo_lightbox.save_failed"));
    }
  }

  return createPortal(
    <div className={styles.photoLightbox} role="dialog" aria-modal="true" aria-label={t("components.photo_lightbox.aria_dialog")}>
      <div className={styles.toolbar}>
        <span className={styles.title}>{item.title || t("components.photo_lightbox.untitled")}</span>
        <Space>
          <Button
            type="text"
            icon={<ZoomOutOutlined />}
            aria-label={t("components.photo_lightbox.aria_zoom_out")}
            onClick={() => setUserZoom((s) => Math.max(0.25, s - 0.25))}
            style={{ color: "#fff" }}
          />
          <Button
            type="text"
            icon={<ZoomInOutlined />}
            aria-label={t("components.photo_lightbox.aria_zoom_in")}
            onClick={() => setUserZoom((s) => Math.min(4, s + 0.25))}
            style={{ color: "#fff" }}
          />
          <Button
            type="text"
            icon={<RotateLeftOutlined />}
            aria-label={t("components.photo_lightbox.aria_rotate_left")}
            onClick={() => rotateBy(-90)}
            style={{ color: "#fff" }}
          />
          <Button
            type="text"
            icon={<RotateRightOutlined />}
            aria-label={t("components.photo_lightbox.aria_rotate_right")}
            onClick={() => rotateBy(90)}
            style={{ color: "#fff" }}
          />
          <Button
            type="text"
            icon={favorited ? <StarFilled /> : <StarOutlined />}
            aria-label={
              favorited
                ? t("components.photo_lightbox.aria_unfavorite")
                : t("components.photo_lightbox.aria_favorite")
            }
            loading={favoriteBusy}
            onClick={() => void onToggleFavorite()}
            style={{ color: favorited ? "#ed6d00" : "#fff" }}
          />
          <Button
            type="text"
            icon={<DownloadOutlined />}
            aria-label={t("components.photo_lightbox.aria_download")}
            style={{ color: "#fff" }}
            href={downloadUrl}
            target="_blank"
            rel="noreferrer"
          />
          <Button type="text" icon={<CloseOutlined />} aria-label={t("components.photo_lightbox.aria_close")} onClick={onClose} style={{ color: "#fff" }} />
        </Space>
      </div>

      {hasPrev ? (
        <button type="button" className={`${styles.navBtn} ${styles.navPrev}`} aria-label={t("components.photo_lightbox.aria_prev")} onClick={() => onChangeIndex(index - 1)}>
          <LeftOutlined />
        </button>
      ) : null}
      {hasNext ? (
        <button type="button" className={`${styles.navBtn} ${styles.navNext}`} aria-label={t("components.photo_lightbox.aria_next")} onClick={() => onChangeIndex(index + 1)}>
          <RightOutlined />
        </button>
      ) : null}

      <div ref={stageRef} className={styles.stage} onWheel={onWheel}>
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
            width={imageSize.w || undefined}
            height={imageSize.h || undefined}
            style={{
              transform: `translate(${offset.x}px, ${offset.y}px) rotate(${rotation}deg) scale(${displayScale})`,
              opacity: loading ? 0 : 1,
            }}
            onLoad={(e) => {
              const img = e.currentTarget;
              setImageSize({ w: img.naturalWidth, h: img.naturalHeight });
              setLoading(false);
            }}
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
            {localTags.map((tg) => (
              <Tag key={tg} color="blue">
                {tg}
              </Tag>
            ))}
            {!editingTags ? (
              <Button type="link" size="small" onClick={() => { setEditingTags(true); setTagDraft(localTags.join("、")); }} style={{ padding: 0, height: "auto" }}>
                {t("components.photo_lightbox.edit_tags")}
              </Button>
            ) : (
              <div ref={tagEditRef}>
                <Space.Compact style={{ marginTop: 4 }}>
                  <input
                    className={styles.tagInput}
                    value={tagDraft}
                    onChange={(e) => setTagDraft(e.target.value)}
                    placeholder={t("components.photo_lightbox.tags_placeholder")}
                  />
                  <Button size="small" type="primary" onClick={() => void saveTags()}>
                    {t("components.photo_lightbox.save")}
                  </Button>
                </Space.Compact>
              </div>
            )}
          </div>
          <span className={styles.counter}>
            {index + 1} / {items.length}
            {useOriginal ? t("components.photo_lightbox.original_label") : t("components.photo_lightbox.load_original_hint")}
          </span>
        </div>
      </div>
    </div>,
    document.body,
  );
}
