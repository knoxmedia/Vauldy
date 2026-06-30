import { useCallback, useEffect, useRef, useState } from "react";
import {
  Button,
  Image as AntImage,
  Modal,
  Spin,
  Typography,
  message,
} from "antd";
import { PlusOutlined, CheckCircleFilled } from "@ant-design/icons";
import {
  derivedVideoPosterSrc,
  fetchAlbumImageCandidates,
  fetchArtistImageCandidates,
  fetchMediaImageCandidates,
  fetchSeriesImageCandidates,
  type ImageCandidatesResponse,
  uploadImageFile,
  withAccessToken,
} from "../api/client";
import { proxyImageSrc } from "../lib/imageUrl";
import { useT } from "../i18n";

const { Text } = Typography;

export type ImagePickerKind = "poster" | "backdrop" | "logo";

export interface MediaImagePickerDialogProps {
  open: boolean;
  onClose: () => void;
  /** Media id used to query the backend for library-configured image candidates. */
  mediaId?: number;
  /** Series id — alternative to mediaId for TV series poster picking. */
  seriesId?: number;
  /** Album id — alternative to mediaId for music album artwork picking. */
  albumId?: number;
  /** Artist id — for music artist portrait picking. */
  artistId?: number;
  /** Title fallback used only when mediaId is unavailable. */
  mediaTitle?: string;
  /** Release year hint (unused when mediaId is present; backend reads it from the media row). */
  mediaYear?: number;
  /** Which image kind to pick; selects the matching image array. */
  kind: ImagePickerKind;
  /** Currently selected URL (from the form field). */
  currentUrl?: string;
  /** Optional auto-captured frame URL (e.g. server-generated poster.jpg). */
  autoFrameUrl?: string;
  /** Called with the user-confirmed URL. */
  onConfirm: (url: string) => void;
}

type Candidate = {
  url: string;
  /** Display source label (当前 / 自动截取 / TMDb / 豆瓣 / ... / 已上传). */
  source: string;
};

/** Map a backend provider id to a localized source label. */
function sourceLabel(id: string, t: (k: string) => string): string {
  switch (id) {
    case "tmdb":
      return t("pages.media_manager.poster_picker_source_tmdb");
    case "douban":
      return t("pages.media_manager.poster_picker_source_douban");
    case "bangumi":
      return t("pages.media_manager.poster_picker_source_bangumi");
    case "tvdb":
      return t("pages.media_manager.poster_picker_source_tvdb");
    case "omdb":
      return t("pages.media_manager.poster_picker_source_omdb");
    case "fanart":
      return t("pages.media_manager.poster_picker_source_fanart");
    default:
      return id;
  }
}

/** Resolve a candidate URL for <img src>: proxy hotlink-protected hosts, auth Knox API URLs. */
function displaySrc(url: string): string {
  return withAccessToken(proxyImageSrc(url));
}

/** Normalize a URL for dedupe comparison (trim + strip access_token query). */
function dedupeKey(url: string): string {
  const u = (url || "").trim();
  if (!u) return "";
  try {
    const parsed = new URL(u, window.location.origin);
    parsed.searchParams.delete("access_token");
    return parsed.toString();
  } catch {
    return u;
  }
}

/** Show scrape-failure hint only when scraping actually ran and every provider failed. */
function scrapeProvidersFailed(res: ImageCandidatesResponse, scrapedCandidateCount: number): boolean {
  if (!res.scraped) return false;
  if (scrapedCandidateCount > 0) return false;
  const errs = res.errors;
  return errs != null && Object.keys(errs).length > 0;
}

export default function MediaImagePickerDialog({
  open,
  onClose,
  mediaId,
  seriesId,
  albumId,
  artistId,
  kind,
  currentUrl,
  autoFrameUrl,
  onConfirm,
}: MediaImagePickerDialogProps) {
  const t = useT();
  const [candidates, setCandidates] = useState<Candidate[]>([]);
  const [selectedUrl, setSelectedUrl] = useState<string>("");
  const [loading, setLoading] = useState(false);
  const [uploading, setUploading] = useState(false);
  const [scrapeFailed, setScrapeFailed] = useState(false);
  const [dragActive, setDragActive] = useState(false);
  const fileInputRef = useRef<HTMLInputElement | null>(null);

  const buildCandidates = useCallback(async () => {
    setLoading(true);
    setScrapeFailed(false);
    const out: Candidate[] = [];
    const seen = new Set<string>();
    const push = (url: string, source: string) => {
      const trimmed = (url || "").trim();
      if (!trimmed) return;
      const key = dedupeKey(trimmed);
      if (seen.has(key)) return;
      seen.add(key);
      out.push({ url: trimmed, source });
    };

    const trimmedCurrent = (currentUrl || "").trim();
    if (trimmedCurrent) {
      push(trimmedCurrent, t("pages.media_manager.poster_picker_source_current"));
    }
    if (autoFrameUrl) push(autoFrameUrl, t("pages.media_manager.poster_picker_source_auto"));

    let scrapeProvidersErrored = false;

    const applyScrapeResponse = (res: ImageCandidatesResponse) => {
      let scrapedAdded = 0;
      for (const c of res.candidates || []) {
        push(c.url, sourceLabel(c.source, t));
        scrapedAdded++;
      }
      if (scrapeProvidersFailed(res, scrapedAdded)) {
        scrapeProvidersErrored = true;
      }
    };

    // Query the media-centric endpoint, which contacts ONLY the image sources
    // configured on the media's owning library — unconfigured providers are never
    // reached, so unreachable sources can be omitted from the library config to
    // avoid long network timeouts. Without a scrape target id there's nothing to query.
    if (mediaId) {
      try {
        applyScrapeResponse(await fetchMediaImageCandidates(mediaId, kind));
      } catch {
        // Request failed before scrape (e.g. 403/400) — not a scrape-source failure.
      }
    } else if (seriesId) {
      try {
        applyScrapeResponse(await fetchSeriesImageCandidates(seriesId, kind));
      } catch {
        // Request failed before scrape — not a scrape-source failure.
      }
    } else if (albumId) {
      try {
        applyScrapeResponse(await fetchAlbumImageCandidates(albumId, kind));
      } catch {
        // Request failed before scrape — not a scrape-source failure.
      }
    } else if (artistId) {
      try {
        applyScrapeResponse(await fetchArtistImageCandidates(artistId, kind));
      } catch {
        // Request failed before scrape — not a scrape-source failure.
      }
    }
    setScrapeFailed(scrapeProvidersErrored);
    setCandidates(out);
    const initialSel =
      trimmedCurrent && out.some((c) => dedupeKey(c.url) === dedupeKey(trimmedCurrent))
        ? trimmedCurrent
        : out[0]?.url || "";
    setSelectedUrl(initialSel);
    setLoading(false);
  }, [autoFrameUrl, currentUrl, kind, mediaId, seriesId, albumId, artistId, t]);

  useEffect(() => {
    if (open) void buildCandidates();
  }, [open, buildCandidates]);

  const uploadOne = useCallback(
    async (file: File): Promise<string | null> => {
      try {
        const res = await uploadImageFile(file);
        const url = res.url;
        setCandidates((prev) => {
          const key = dedupeKey(url);
          if (prev.some((c) => dedupeKey(c.url) === key)) return prev;
          return [...prev, { url, source: t("pages.media_manager.poster_picker_source_uploaded") }];
        });
        return url;
      } catch (e: unknown) {
        message.error((e as Error).message || t("pages.media_manager.poster_picker_upload_failed"));
        return null;
      }
    },
    [t],
  );

  const handleFiles = useCallback(
    (files: FileList | File[]) => {
      const arr = Array.from(files).filter(
        (f): f is File => f instanceof File && (f.type || "").startsWith("image/"),
      );
      if (arr.length === 0) return;
      setUploading(true);
      void (async () => {
        let lastUrl: string | null = null;
        for (const file of arr) {
          const url = await uploadOne(file);
          if (url) lastUrl = url;
        }
        if (lastUrl) {
          setSelectedUrl(lastUrl);
          message.success(t("pages.media_manager.poster_picker_upload_ok"));
        }
        setUploading(false);
      })();
    },
    [t, uploadOne],
  );

  const handleDragOver = useCallback((e: React.DragEvent<HTMLDivElement>) => {
    e.preventDefault();
    e.stopPropagation();
    if (e.dataTransfer?.types?.includes("Files")) setDragActive(true);
  }, []);

  const handleDragLeave = useCallback((e: React.DragEvent<HTMLDivElement>) => {
    e.preventDefault();
    e.stopPropagation();
    // Only deactivate when the pointer actually leaves the list container,
    // not when it moves between child cells (dragenter/leave bubble up).
    if (e.relatedTarget && e.currentTarget.contains(e.relatedTarget as Node)) return;
    setDragActive(false);
  }, []);

  const handleDrop = useCallback(
    (e: React.DragEvent<HTMLDivElement>) => {
      e.preventDefault();
      e.stopPropagation();
      setDragActive(false);
      if (e.dataTransfer?.files && e.dataTransfer.files.length > 0) {
        handleFiles(e.dataTransfer.files);
      }
    },
    [handleFiles],
  );

  const handleFileInputChange = useCallback(
    (e: React.ChangeEvent<HTMLInputElement>) => {
      if (e.target.files) handleFiles(e.target.files);
      e.target.value = "";
    },
    [handleFiles],
  );

  const handleConfirm = () => {
    if (!selectedUrl) {
      message.warning(t("pages.media_manager.poster_picker_none_selected"));
      return;
    }
    onConfirm(selectedUrl);
    onClose();
  };

  const titleText = t(`pages.media_manager.poster_picker_title_${kind}`, undefined, kind);
  const cellAspect = kind === "poster" ? "2 / 3" : "16 / 9";

  return (
    <Modal
      open={open}
      onCancel={onClose}
      width={760}
      title={titleText}
      footer={[
        <Button key="cancel" onClick={onClose}>
          {t("pages.media_manager.poster_picker_cancel")}
        </Button>,
        <Button key="ok" type="primary" disabled={!selectedUrl} onClick={handleConfirm}>
          {t("pages.media_manager.poster_picker_confirm")}
        </Button>,
      ]}
      destroyOnClose
    >
      <Spin spinning={loading}>
        {scrapeFailed && (
          <Text type="secondary" style={{ display: "block", marginBottom: 8 }}>
            {t("pages.media_manager.poster_picker_scrape_failed")}
          </Text>
        )}
        <Spin spinning={uploading}>
          <div
            onDragOver={handleDragOver}
            onDragLeave={handleDragLeave}
            onDrop={handleDrop}
            style={{
              display: "grid",
              gridTemplateColumns: "repeat(auto-fill, minmax(140px, 1fr))",
              gap: 12,
              maxHeight: 480,
              overflowY: "auto",
              padding: 4,
              border: dragActive ? "2px dashed #1677ff" : "2px dashed transparent",
              borderRadius: 8,
              transition: "border-color 0.15s",
            }}
          >
            {candidates.map((c) => {
              const active = dedupeKey(c.url) === dedupeKey(selectedUrl);
              return (
                <button
                  type="button"
                  key={c.url}
                  onClick={() => setSelectedUrl(c.url)}
                  style={{
                    position: "relative",
                    border: active ? "2px solid #1677ff" : "1px solid #303030",
                    borderRadius: 8,
                    padding: 0,
                    background: "#1f1f1f",
                    cursor: "pointer",
                    overflow: "hidden",
                    aspectRatio: cellAspect,
                  }}
                  aria-pressed={active}
                  aria-label={c.source}
                >
                  <AntImage
                    src={displaySrc(c.url)}
                    alt={c.source}
                    preview={false}
                    style={{ width: "100%", height: "100%", objectFit: "cover", display: "block" }}
                  />
                  {active && (
                    <CheckCircleFilled
                      style={{
                        position: "absolute",
                        top: 6,
                        right: 6,
                        color: "#1677ff",
                        fontSize: 18,
                        filter: "drop-shadow(0 1px 2px rgba(0,0,0,0.6))",
                      }}
                    />
                  )}
                  <span
                    style={{
                      position: "absolute",
                      bottom: 0,
                      left: 0,
                      right: 0,
                      padding: "2px 6px",
                      fontSize: 11,
                      color: "#fff",
                      background: "rgba(0,0,0,0.55)",
                      textAlign: "left",
                      overflow: "hidden",
                      textOverflow: "ellipsis",
                      whiteSpace: "nowrap",
                    }}
                  >
                    {c.source}
                  </span>
                </button>
              );
            })}
            {/* Upload affordance as the last grid cell — same shape as image cells.
                Click to pick a file; dragging a file anywhere over the list also uploads. */}
            <button
              type="button"
              onClick={() => fileInputRef.current?.click()}
              aria-label={t("pages.media_manager.poster_picker_upload_cell")}
              style={{
                border: "1px dashed #1677ff",
                borderRadius: 8,
                background: "#1f1f1f",
                cursor: "pointer",
                aspectRatio: cellAspect,
                display: "flex",
                flexDirection: "column",
                alignItems: "center",
                justifyContent: "center",
                gap: 6,
                color: "rgba(255,255,255,0.65)",
                padding: 0,
              }}
            >
              <PlusOutlined style={{ fontSize: 26, color: "#1677ff" }} />
              <span style={{ fontSize: 12 }}>
                {t("pages.media_manager.poster_picker_upload_cell")}
              </span>
            </button>
          </div>
        </Spin>
        <Text type="secondary" style={{ display: "block", marginTop: 8, fontSize: 12 }}>
          {t("pages.media_manager.poster_picker_drop_hint")}
        </Text>
        <input
          ref={fileInputRef}
          type="file"
          accept="image/*"
          multiple
          onChange={handleFileInputChange}
          style={{ display: "none" }}
        />
      </Spin>
    </Modal>
  );
}

/** Convenience helper: the auto-captured poster frame for a video media id. */
export function autoFrameForMedia(mediaId: number, kind: ImagePickerKind): string | undefined {
  if (kind === "poster") return derivedVideoPosterSrc(mediaId);
  return undefined;
}
