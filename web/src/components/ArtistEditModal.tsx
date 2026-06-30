import { Button, Input, Modal, Spin, Typography, message } from "antd";
import { EditOutlined } from "@ant-design/icons";
import { useEffect, useMemo, useState } from "react";
import {
  ArtistSummary,
  artistArtworkSrc,
  fetchArtist,
  updateArtist,
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

export interface ArtistEditModalProps {
  artist: ArtistSummary | null;
  open: boolean;
  onClose: () => void;
  onSaved?: (update: Partial<ArtistSummary> & { id: number }) => void;
}

export default function ArtistEditModal({ artist, open, onClose, onSaved }: ArtistEditModalProps) {
  const t = useT();
  const [loading, setLoading] = useState(false);
  const [saving, setSaving] = useState(false);
  const [artworkPickerOpen, setArtworkPickerOpen] = useState(false);
  const [name, setName] = useState("");
  const [artwork, setArtwork] = useState("");

  useEffect(() => {
    if (!open || !artist) return;
    let cancelled = false;
    setLoading(true);
    void (async () => {
      try {
        const detail = await fetchArtist(artist.id);
        if (cancelled) return;
        setName(detail.name || artist.name || "");
        setArtwork((detail.artwork_path || artist.artwork_path || "").trim());
      } catch (e: unknown) {
        if (!cancelled) message.error((e as Error).message || t("components.artist_edit_modal.load_failed"));
      } finally {
        if (!cancelled) setLoading(false);
      }
    })();
    return () => {
      cancelled = true;
    };
  }, [open, artist, t]);

  const artworkPickerCurrent = useMemo(() => {
    const raw = artwork.trim();
    if (!raw) return "";
    if (isBrowsableArtworkPath(raw)) return proxyImageSrc(raw);
    if (artist?.id) return artistArtworkSrc(artist.id);
    return raw;
  }, [artwork, artist?.id]);

  async function handleSave() {
    if (!artist) return;
    const trimmedName = name.trim();
    if (!trimmedName) {
      message.warning(t("components.artist_edit_modal.name_required"));
      return;
    }
    setSaving(true);
    try {
      const data = await updateArtist(artist.id, {
        name: trimmedName,
        artwork: artwork.trim() || undefined,
      });
      message.success(t("components.artist_edit_modal.saved"));
      onSaved?.({
        id: artist.id,
        name: data.name || trimmedName,
        artwork_path: data.artwork_path || artwork.trim() || undefined,
      });
      onClose();
    } catch (e: unknown) {
      message.error((e as Error).message || t("components.artist_edit_modal.save_failed"));
    } finally {
      setSaving(false);
    }
  }

  return (
    <>
      <Modal
        title={t("components.artist_edit_modal.title")}
        open={open}
        onCancel={onClose}
        onOk={() => void handleSave()}
        okText={t("components.artist_edit_modal.ok")}
        cancelText={t("components.artist_edit_modal.cancel")}
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
              <Text type="secondary">{t("components.artist_edit_modal.label_name")}</Text>
              <Input
                value={name}
                onChange={(e) => setName(e.target.value)}
                placeholder={t("components.artist_edit_modal.name_placeholder")}
              />
            </div>
            <div>
              <span style={{ display: "inline-flex", alignItems: "center", gap: 6, marginBottom: 4 }}>
                <Text type="secondary">{t("components.artist_edit_modal.label_artwork")}</Text>
                <Button
                  type="text"
                  size="small"
                  icon={<EditOutlined />}
                  aria-label={t("pages.media_manager.poster_picker_edit_aria")}
                  disabled={!artist}
                  onClick={() => setArtworkPickerOpen(true)}
                />
              </span>
              <Input
                value={artwork}
                onChange={(e) => setArtwork(e.target.value)}
                placeholder={t("components.artist_edit_modal.artwork_placeholder")}
              />
            </div>
          </div>
        )}
      </Modal>
      <MediaImagePickerDialog
        open={artworkPickerOpen}
        onClose={() => setArtworkPickerOpen(false)}
        artistId={artist?.id}
        kind="poster"
        currentUrl={artworkPickerCurrent}
        onConfirm={setArtwork}
      />
    </>
  );
}
