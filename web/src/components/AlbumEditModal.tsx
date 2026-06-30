import { Button, Input, InputNumber, Modal, Spin, Typography, message } from "antd";
import { EditOutlined } from "@ant-design/icons";
import { useEffect, useMemo, useState } from "react";
import {
  AlbumSummary,
  albumArtworkSrc,
  fetchAlbum,
  updateAlbum,
} from "../api/client";
import { proxyImageSrc } from "../lib/imageUrl";
import MediaImagePickerDialog from "./MediaImagePickerDialog";
import { useT } from "../i18n";

const { Text } = Typography;

function isBrowsableArtworkPath(path: string): boolean {
  const p = path.trim();
  return (
    p.startsWith("http://") ||
    p.startsWith("https://") ||
    p.startsWith("/uploads/") ||
    p.startsWith("/metadata/")
  );
}

export interface AlbumEditModalProps {
  album: AlbumSummary | null;
  open: boolean;
  onClose: () => void;
  onSaved?: (update: Partial<AlbumSummary> & { id: number }) => void;
}

export default function AlbumEditModal({ album, open, onClose, onSaved }: AlbumEditModalProps) {
  const t = useT();
  const [loading, setLoading] = useState(false);
  const [saving, setSaving] = useState(false);
  const [artworkPickerOpen, setArtworkPickerOpen] = useState(false);
  const [title, setTitle] = useState("");
  const [year, setYear] = useState<number | null>(null);
  const [genre, setGenre] = useState("");
  const [artwork, setArtwork] = useState("");

  useEffect(() => {
    if (!open || !album) return;
    let cancelled = false;
    setLoading(true);
    void (async () => {
      try {
        const detail = await fetchAlbum(album.id);
        if (cancelled) return;
        setTitle(detail.title || album.title || "");
        setYear((detail.year ?? album.year ?? 0) > 0 ? (detail.year ?? album.year)! : null);
        setGenre(detail.genre || album.genre || "");
        setArtwork((detail.artwork_path || album.artwork_path || "").trim());
      } catch (e: unknown) {
        if (!cancelled) message.error((e as Error).message || t("components.album_edit_modal.load_failed"));
      } finally {
        if (!cancelled) setLoading(false);
      }
    })();
    return () => {
      cancelled = true;
    };
  }, [open, album, t]);

  const artworkPickerCurrent = useMemo(() => {
    const raw = artwork.trim();
    if (!raw) return "";
    if (isBrowsableArtworkPath(raw)) return proxyImageSrc(raw);
    if (album?.id) return albumArtworkSrc(album.id);
    return raw;
  }, [artwork, album?.id]);

  async function handleSave() {
    if (!album) return;
    const trimmedTitle = title.trim();
    if (!trimmedTitle) {
      message.warning(t("components.album_edit_modal.title_required"));
      return;
    }
    setSaving(true);
    try {
      const data = await updateAlbum(album.id, {
        title: trimmedTitle,
        year: year ?? undefined,
        genre: genre.trim() || undefined,
        artwork: artwork.trim() || undefined,
      });
      message.success(t("components.album_edit_modal.saved"));
      onSaved?.({
        id: album.id,
        title: data.title || trimmedTitle,
        year: data.year ?? year ?? undefined,
        genre: data.genre ?? genre.trim(),
        artwork_path: data.artwork_path || artwork.trim() || undefined,
      });
      onClose();
    } catch (e: unknown) {
      message.error((e as Error).message || t("components.album_edit_modal.save_failed"));
    } finally {
      setSaving(false);
    }
  }

  return (
    <>
      <Modal
        title={t("components.album_edit_modal.title")}
        open={open}
        onCancel={onClose}
        onOk={() => void handleSave()}
        okText={t("components.album_edit_modal.ok")}
        cancelText={t("components.album_edit_modal.cancel")}
        confirmLoading={saving}
        destroyOnClose
        centered
        width={560}
      >
        {loading ? (
          <div style={{ display: "flex", justifyContent: "center", padding: 32 }}>
            <Spin />
          </div>
        ) : (
          <div style={{ display: "flex", flexDirection: "column", gap: 14 }}>
            <div>
              <Text type="secondary">{t("components.album_edit_modal.label_title")}</Text>
              <Input
                value={title}
                onChange={(e) => setTitle(e.target.value)}
                placeholder={t("components.album_edit_modal.title_placeholder")}
              />
            </div>
            <div>
              <Text type="secondary">{t("components.album_edit_modal.label_year")}</Text>
              <InputNumber
                value={year}
                onChange={(v) => setYear(typeof v === "number" ? v : null)}
                min={1800}
                max={2100}
                placeholder={t("components.album_edit_modal.year_placeholder")}
                style={{ width: "100%" }}
              />
            </div>
            <div>
              <Text type="secondary">{t("components.album_edit_modal.label_genre")}</Text>
              <Input
                value={genre}
                onChange={(e) => setGenre(e.target.value)}
                placeholder={t("components.album_edit_modal.genre_placeholder")}
              />
            </div>
            <div>
              <span style={{ display: "inline-flex", alignItems: "center", gap: 6, marginBottom: 4 }}>
                <Text type="secondary">{t("components.album_edit_modal.label_artwork")}</Text>
                <Button
                  type="text"
                  size="small"
                  icon={<EditOutlined />}
                  aria-label={t("pages.media_manager.poster_picker_edit_aria")}
                  disabled={!album}
                  onClick={() => setArtworkPickerOpen(true)}
                />
              </span>
              <Input
                value={artwork}
                onChange={(e) => setArtwork(e.target.value)}
                placeholder={t("components.album_edit_modal.artwork_placeholder")}
              />
            </div>
          </div>
        )}
      </Modal>
      <MediaImagePickerDialog
        open={artworkPickerOpen}
        onClose={() => setArtworkPickerOpen(false)}
        albumId={album?.id}
        kind="poster"
        currentUrl={artworkPickerCurrent}
        onConfirm={setArtwork}
      />
    </>
  );
}
