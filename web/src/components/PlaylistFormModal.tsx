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
import styles from "./PlaylistFormModal.module.css";

type TabKey = "general" | "poster" | "background" | "logo" | "square_art";
type ImageField = "poster" | "background" | "logo" | "square_art";

const TABS: { key: TabKey; label: string; icon: ReactNode }[] = [
  { key: "general", label: "常规", icon: <UnorderedListOutlined /> },
  { key: "poster", label: "海报", icon: <PictureOutlined /> },
  { key: "background", label: "背景", icon: <LayoutOutlined /> },
  { key: "logo", label: "Logo", icon: <TrademarkCircleOutlined /> },
  { key: "square_art", label: "Square Art", icon: <AppstoreOutlined /> },
];

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
            <span style={{ fontSize: 12 }}>上传</span>
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
          plId = await createPlaylist(name.trim() || "未命名播放列表", description, "", "", "", "");
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
      message.error((e as Error).message || "上传失败");
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
      message.error("请输入标题");
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
        message.success("已更新");
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
        message.success("已创建");
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
        message.success("已创建");
      }
    } catch (e: unknown) {
      message.error((e as Error).message || "操作失败");
    } finally {
      setSaving(false);
    }
  }

  const headerText = playlist?.id
    ? `编辑 ${name.trim() || playlist.name || "播放列表"}`
    : "新建播放列表";

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
            {playlist?.id ? <EditOutlined style={{ color: "#faad14" }} /> : <EditOutlined style={{ opacity: 0.65 }} />}
            <span>{headerText}</span>
          </div>
          <Button type="text" icon={<CloseOutlined />} onClick={onClose} className={styles.closeBtn} aria-label="关闭" />
        </header>

        <div className={styles.body}>
          <nav className={styles.sidebar} aria-label="播放列表表单分区">
            {TABS.map((t) => (
              <button
                key={t.key}
                type="button"
                className={`${styles.tab} ${activeTab === t.key ? styles.tabActive : ""}`}
                onClick={() => setActiveTab(t.key)}
              >
                {t.icon}
                {t.label}
              </button>
            ))}
          </nav>

          <div className={styles.main}>
            {activeTab === "general" && (
              <div className={styles.panel}>
                <div className={styles.fieldGap}>
                  <label className={styles.fieldLabel} htmlFor="pl-title">
                    标题
                  </label>
                  <Input
                    id="pl-title"
                    value={name}
                    onChange={(e) => setName(e.target.value)}
                    placeholder="播放列表标题"
                    maxLength={100}
                    className={styles.darkInput}
                  />
                </div>
                <div className={styles.fieldGap}>
                  <label className={styles.fieldLabel} htmlFor="pl-overview">
                    总览
                  </label>
                  <Input.TextArea
                    id="pl-overview"
                    value={description}
                    onChange={(e) => setDescription(e.target.value)}
                    placeholder="简介（可选）"
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
                  label="海报"
                  ratio="2/3"
                  maxWidth={200}
                />
              </div>
            )}

            {activeTab === "background" && (
              <div className={styles.panel}>
                <div className={styles.fieldGap}>
                  <div className={styles.uploadLabel}>背景图片</div>
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
                        alt="背景"
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
                        <span style={{ fontSize: 13 }}>点击或拖拽上传背景图</span>
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
            取消
          </Button>
          <Button className={styles.btnPrimary} loading={saving} onClick={handleSave}>
            {playlist?.id ? "保存修改" : "创建"}
          </Button>
        </footer>
      </div>
    </Modal>
  );
}
