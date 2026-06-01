import {
  AppstoreOutlined,
  CloseOutlined,
  EditOutlined,
  InboxOutlined,
  LayoutOutlined,
  PictureOutlined,
  PlusOutlined,
  TrademarkCircleOutlined,
  UnorderedListOutlined,
} from "@ant-design/icons";
import { Button, Input, message, Modal, Spin, Upload } from "antd";
import type { UploadProps } from "antd";
import type { ReactNode } from "react";
import { useEffect, useState } from "react";
import {
  createPlaylist,
  Playlist,
  updatePlaylist,
  uploadPlaylistImage,
} from "../api/client";
import { useT, tGlobal, type TranslateFn } from "../i18n";
import styles from "./PlaylistFormModal.module.css";

type TabKey = "general" | "poster" | "background" | "logo" | "square_art";
type ImageField = "poster" | "background" | "logo" | "square_art";

function buildTabs(t: TranslateFn): { key: TabKey; label: string; icon: ReactNode }[] {
  return [
    { key: "general", label: t("components.playlist_form_modal.tab_general"), icon: <UnorderedListOutlined /> },
    { key: "poster", label: t("components.playlist_form_modal.tab_poster"), icon: <PictureOutlined /> },
    { key: "background", label: t("components.playlist_form_modal.tab_background"), icon: <LayoutOutlined /> },
    { key: "logo", label: "Logo", icon: <TrademarkCircleOutlined /> },
    { key: "square_art", label: "Square Art", icon: <AppstoreOutlined /> },
  ];
}

interface ImagePreviewProps {
  url: string;
  onUpload: (file: File) => Promise<void>;
  label: string;
  ratio: "2/3" | "16/9" | "1/1";
  maxWidth?: number;
}

function ImagePreview({ url, onUpload, label, ratio, maxWidth }: ImagePreviewProps) {
  const [uploading, setUploading] = useState(false);

  const uploadProps: UploadProps = {
    accept: "image/*",
    showUploadList: false,
    disabled: uploading,
    beforeUpload: async (file) => {
      setUploading(true);
      try {
        await onUpload(file);
      } finally {
        setUploading(false);
      }
      return false;
    },
  };

  const aspectStyle =
    ratio === "2/3"
      ? { aspectRatio: "2/3" }
      : ratio === "16/9"
        ? { aspectRatio: "16/9" }
        : { aspectRatio: "1/1" };

  const mw = maxWidth ?? (ratio === "2/3" ? 160 : ratio === "16/9" ? 360 : 120);

  return (
    <div className={styles.fieldGap}>
      <div className={styles.uploadLabel}>{label}</div>
      <Upload.Dragger
        {...uploadProps}
        className={styles.draggerDark}
        style={{ ...aspectStyle, maxWidth: mw }}
      >
        {uploading ? (
          <div style={{ display: "flex", alignItems: "center", justifyContent: "center", height: "100%" }}>
            <Spin size="small" />
          </div>
        ) : url ? (
          <img
            src={url}
            alt={label}
            style={{ width: "100%", height: "100%", objectFit: "cover", display: "block" }}
          />
        ) : (
          <div
            style={{
              display: "flex",
              flexDirection: "column",
              alignItems: "center",
              justifyContent: "center",
              height: "100%",
              color: "rgba(255,255,255,0.45)",
              padding: 12,
            }}
          >
            <PlusOutlined style={{ fontSize: 22, marginBottom: 6 }} />
            <span style={{ fontSize: 12 }}>{tGlobal("components.playlist_form_modal.upload")}</span>
          </div>
        )}
      </Upload.Dragger>
    </div>
  );
}

interface PlaylistFormModalProps {
  open: boolean;
  playlist?: Playlist | null;
  onClose: () => void;
  onSaved: (playlist: Playlist) => void;
}

export default function PlaylistFormModal({ open, playlist, onClose, onSaved }: PlaylistFormModalProps) {
  const t = useT();
  const TABS = buildTabs(t);
  const [activeTab, setActiveTab] = useState<TabKey>("general");
  const [name, setName] = useState("");
  const [description, setDescription] = useState("");
  const [posterUrl, setPosterUrl] = useState("");
  const [backgroundUrl, setBackgroundUrl] = useState("");
  const [logoUrl, setLogoUrl] = useState("");
  const [squareArtUrl, setSquareArtUrl] = useState("");
  const [saving, setSaving] = useState(false);
  const [uploading, setUploading] = useState<Set<string>>(new Set());
  /** When user uploads images before clicking save, API creates a row first; reuse that id. */
  const [draftPlaylistId, setDraftPlaylistId] = useState<number | null>(null);

  useEffect(() => {
    if (open) {
      setActiveTab("general");
      setDraftPlaylistId(null);
      setName(playlist?.name ?? "");
      setDescription(playlist?.description ?? "");
      setPosterUrl(playlist?.poster_url ?? "");
      setBackgroundUrl(playlist?.background_url ?? "");
      setLogoUrl(playlist?.logo_url ?? "");
      setSquareArtUrl(playlist?.square_art_url ?? "");
    }
  }, [open, playlist]);

  async function handleImageUpload(field: ImageField, file: File) {
    setUploading((prev) => new Set(prev).add(field));
    try {
      let plId = playlist?.id ?? draftPlaylistId ?? undefined;
      if (!plId) {
        setSaving(true);
        try {
          plId = await createPlaylist(name.trim() || t("components.playlist_form_modal.default_playlist_name"), description, "", "", "", "");
          setDraftPlaylistId(plId);
        } finally {
          setSaving(false);
        }
      }
      const url = await uploadPlaylistImage(plId, field, file);
      if (field === "poster") setPosterUrl(url);
      else if (field === "background") setBackgroundUrl(url);
      else if (field === "logo") setLogoUrl(url);
      else setSquareArtUrl(url);
    } catch (e: unknown) {
      message.error((e as Error).message || t("components.playlist_form_modal.upload_failed"));
    } finally {
      setUploading((prev) => {
        const next = new Set(prev);
        next.delete(field);
        return next;
      });
    }
  }

  async function handleSave() {
    if (!name.trim()) {
      message.error(t("components.playlist_form_modal.title_required"));
      return;
    }
    setSaving(true);
    try {
      if (playlist?.id) {
        await updatePlaylist(
          playlist.id,
          name.trim(),
          description,
          posterUrl,
          backgroundUrl,
          logoUrl,
          squareArtUrl
        );
        const updated: Playlist = {
          ...playlist,
          name: name.trim(),
          description,
          poster_url: posterUrl,
          background_url: backgroundUrl,
          logo_url: logoUrl,
          square_art_url: squareArtUrl,
        };
        onSaved(updated);
        onClose();
        message.success(t("components.playlist_form_modal.updated"));
      } else if (draftPlaylistId) {
        await updatePlaylist(
          draftPlaylistId,
          name.trim(),
          description,
          posterUrl,
          backgroundUrl,
          logoUrl,
          squareArtUrl
        );
        const newPlaylist: Playlist = {
          id: draftPlaylistId,
          name: name.trim(),
          description,
          poster_url: posterUrl,
          background_url: backgroundUrl,
          logo_url: logoUrl,
          square_art_url: squareArtUrl,
          item_count: 0,
          first_media_id: 0,
          created_at: new Date().toISOString(),
          updated_at: new Date().toISOString(),
        };
        onSaved(newPlaylist);
        onClose();
        message.success(t("components.playlist_form_modal.created"));
      } else {
        const id = await createPlaylist(
          name.trim(),
          description,
          posterUrl,
          backgroundUrl,
          logoUrl,
          squareArtUrl
        );
        const newPlaylist: Playlist = {
          id,
          name: name.trim(),
          description,
          poster_url: posterUrl,
          background_url: backgroundUrl,
          logo_url: logoUrl,
          square_art_url: squareArtUrl,
          item_count: 0,
          first_media_id: 0,
          created_at: new Date().toISOString(),
          updated_at: new Date().toISOString(),
        };
        onSaved(newPlaylist);
        onClose();
        message.success(t("components.playlist_form_modal.created"));
      }
    } catch (e: unknown) {
      message.error((e as Error).message || t("components.playlist_form_modal.operation_failed"));
    } finally {
      setSaving(false);
    }
  }

  const headerText = playlist?.id
    ? t("components.playlist_form_modal.edit_title_prefix", { name: name.trim() || playlist.name || t("components.playlist_form_modal.playlist_fallback") })
    : t("components.playlist_form_modal.new_title");

  return (
    <Modal
      open={open}
      title={null}
      onCancel={onClose}
      footer={null}
      destroyOnClose
      width={640}
      className={styles.modal}
      closable={false}
    >
      <div className={styles.shell}>
        <header className={styles.header}>
          <div className={styles.headerTitle}>
            {playlist?.id ? <EditOutlined style={{ color: "#ed6d00" }} /> : <EditOutlined style={{ opacity: 0.65 }} />}
            <span>{headerText}</span>
          </div>
          <Button type="text" icon={<CloseOutlined />} onClick={onClose} className={styles.closeBtn} aria-label={t("components.playlist_form_modal.close")} />
        </header>

        <div className={styles.body}>
          <nav className={styles.sidebar} aria-label={t("components.playlist_form_modal.aria_sections")}>
            {TABS.map((tab) => (
              <button
                key={tab.key}
                type="button"
                className={`${styles.tab} ${activeTab === tab.key ? styles.tabActive : ""}`}
                onClick={() => setActiveTab(tab.key)}
              >
                {tab.icon}
                {tab.label}
              </button>
            ))}
          </nav>

          <div className={styles.main}>
            {activeTab === "general" && (
              <div className={styles.panel}>
                <div className={styles.fieldGap}>
                  <label className={styles.fieldLabel} htmlFor="pl-title">
                    {t("components.playlist_form_modal.field_title")}
                  </label>
                  <Input
                    id="pl-title"
                    value={name}
                    onChange={(e) => setName(e.target.value)}
                    placeholder={t("components.playlist_form_modal.title_placeholder")}
                    maxLength={100}
                    className={styles.darkInput}
                  />
                </div>
                <div className={styles.fieldGap}>
                  <label className={styles.fieldLabel} htmlFor="pl-overview">
                    {t("components.playlist_form_modal.field_overview")}
                  </label>
                  <Input.TextArea
                    id="pl-overview"
                    value={description}
                    onChange={(e) => setDescription(e.target.value)}
                    placeholder={t("components.playlist_form_modal.overview_placeholder")}
                    rows={6}
                    maxLength={2000}
                    className={styles.darkInput}
                  />
                </div>
              </div>
            )}

            {activeTab === "poster" && (
              <div className={styles.panel}>
                <ImagePreview
                  url={posterUrl}
                  onUpload={(file) => handleImageUpload("poster", file)}
                  label={t("components.playlist_form_modal.field_poster")}
                  ratio="2/3"
                  maxWidth={200}
                />
              </div>
            )}

            {activeTab === "background" && (
              <div className={styles.panel}>
                <div className={styles.fieldGap}>
                  <div className={styles.uploadLabel}>{t("components.playlist_form_modal.field_background")}</div>
                  <Upload.Dragger
                    accept="image/*"
                    showUploadList={false}
                    disabled={saving || uploading.has("background")}
                    className={styles.draggerDark}
                    beforeUpload={async (file) => {
                      await handleImageUpload("background", file);
                      return false;
                    }}
                    style={{ aspectRatio: "16/9", maxWidth: "100%" }}
                  >
                    {uploading.has("background") ? (
                      <div style={{ display: "flex", alignItems: "center", justifyContent: "center", minHeight: 120 }}>
                        <Spin size="small" />
                      </div>
                    ) : backgroundUrl ? (
                      <img
                        src={backgroundUrl}
                        alt={t("components.playlist_form_modal.background_alt")}
                        style={{ width: "100%", height: "100%", objectFit: "cover", display: "block", minHeight: 140 }}
                      />
                    ) : (
                      <div
                        style={{
                          display: "flex",
                          flexDirection: "column",
                          alignItems: "center",
                          justifyContent: "center",
                          padding: "28px 16px",
                          color: "rgba(255,255,255,0.45)",
                        }}
                      >
                        <InboxOutlined style={{ fontSize: 32, marginBottom: 10 }} />
                        <span style={{ fontSize: 13 }}>{t("components.playlist_form_modal.drag_background_hint")}</span>
                      </div>
                    )}
                  </Upload.Dragger>
                </div>
              </div>
            )}

            {activeTab === "logo" && (
              <div className={styles.panel}>
                <ImagePreview
                  url={logoUrl}
                  onUpload={(file) => handleImageUpload("logo", file)}
                  label="Logo"
                  ratio="1/1"
                  maxWidth={140}
                />
              </div>
            )}

            {activeTab === "square_art" && (
              <div className={styles.panel}>
                <ImagePreview
                  url={squareArtUrl}
                  onUpload={(file) => handleImageUpload("square_art", file)}
                  label="Square Art"
                  ratio="1/1"
                  maxWidth={200}
                />
              </div>
            )}
          </div>
        </div>

        <footer className={styles.footer}>
          <Button className={styles.btnCancel} onClick={onClose} disabled={saving}>
            {t("components.playlist_form_modal.cancel")}
          </Button>
          <Button className={styles.btnPrimary} loading={saving} onClick={handleSave}>
            {playlist?.id ? t("components.playlist_form_modal.btn_save") : t("components.playlist_form_modal.btn_create")}
          </Button>
        </footer>
      </div>
    </Modal>
  );
}
