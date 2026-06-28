import { Button, Input, InputNumber, Modal, Spin, Typography, message } from "antd";
import { EditOutlined } from "@ant-design/icons";
import { useEffect, useState } from "react";
import {
  SeriesSummary,
  fetchSeries,
  normalizeListPosterUrl,
  updateSeries,
} from "../api/client";
import MediaImagePickerDialog from "./MediaImagePickerDialog";
import { useT } from "../i18n";

const { Text } = Typography;

function parseSeriesOverview(metaJSON?: string): string {
  if (!metaJSON) return "";
  try {
    const root = JSON.parse(metaJSON) as { scrape?: { overview?: string } };
    return typeof root.scrape?.overview === "string" ? root.scrape.overview : "";
  } catch {
    return "";
  }
}

export interface SeriesEditModalProps {
  series: SeriesSummary | null;
  open: boolean;
  onClose: () => void;
  onSaved?: (update: Partial<SeriesSummary> & { id: number; overview?: string }) => void;
}

export default function SeriesEditModal({ series, open, onClose, onSaved }: SeriesEditModalProps) {
  const t = useT();
  const [loading, setLoading] = useState(false);
  const [saving, setSaving] = useState(false);
  const [posterPickerOpen, setPosterPickerOpen] = useState(false);
  const [title, setTitle] = useState("");
  const [year, setYear] = useState<number | null>(null);
  const [poster, setPoster] = useState("");
  const [overview, setOverview] = useState("");

  useEffect(() => {
    if (!open || !series) return;
    let cancelled = false;
    setLoading(true);
    void (async () => {
      try {
        const detail = await fetchSeries(series.id);
        if (cancelled) return;
        setTitle(detail.title || series.title || "");
        setYear((detail.year ?? series.year ?? 0) > 0 ? (detail.year ?? series.year)! : null);
        const posterVal =
          normalizeListPosterUrl(detail.poster || series.poster_url || series.poster || "") || "";
        setPoster(posterVal);
        setOverview(parseSeriesOverview(detail.meta_json));
      } catch (e: unknown) {
        if (!cancelled) message.error((e as Error).message || t("components.series_edit_modal.load_failed"));
      } finally {
        if (!cancelled) setLoading(false);
      }
    })();
    return () => {
      cancelled = true;
    };
  }, [open, series, t]);

  async function handleSave() {
    if (!series) return;
    const trimmedTitle = title.trim();
    if (!trimmedTitle) {
      message.warning(t("components.series_edit_modal.title_required"));
      return;
    }
    setSaving(true);
    try {
      const data = await updateSeries(series.id, {
        title: trimmedTitle,
        year: year ?? undefined,
        poster: poster.trim() || undefined,
        overview: overview.trim() || undefined,
      });
      message.success(t("components.series_edit_modal.saved"));
      onSaved?.({
        id: series.id,
        title: data.title || trimmedTitle,
        year: data.year ?? year ?? undefined,
        poster: data.poster || poster.trim() || undefined,
        poster_url: data.poster || poster.trim() || undefined,
        overview: data.overview ?? overview.trim(),
      });
      onClose();
    } catch (e: unknown) {
      message.error((e as Error).message || t("components.series_edit_modal.save_failed"));
    } finally {
      setSaving(false);
    }
  }

  return (
    <>
      <Modal
        title={t("components.series_edit_modal.title")}
        open={open}
        onCancel={onClose}
        onOk={() => void handleSave()}
        okText={t("components.series_edit_modal.ok")}
        cancelText={t("components.series_edit_modal.cancel")}
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
              <Text type="secondary">{t("components.series_edit_modal.label_title")}</Text>
              <Input value={title} onChange={(e) => setTitle(e.target.value)} placeholder={t("components.series_edit_modal.title_placeholder")} />
            </div>
            <div>
              <Text type="secondary">{t("components.series_edit_modal.label_year")}</Text>
              <InputNumber
                value={year}
                onChange={(v) => setYear(typeof v === "number" ? v : null)}
                min={1800}
                max={2100}
                placeholder={t("components.series_edit_modal.year_placeholder")}
                style={{ width: "100%" }}
              />
            </div>
            <div>
              <span style={{ display: "inline-flex", alignItems: "center", gap: 6, marginBottom: 4 }}>
                <Text type="secondary">{t("components.series_edit_modal.label_poster")}</Text>
                <Button
                  type="text"
                  size="small"
                  icon={<EditOutlined />}
                  aria-label={t("pages.media_manager.poster_picker_edit_aria")}
                  disabled={!series}
                  onClick={() => setPosterPickerOpen(true)}
                />
              </span>
              <Input
                value={poster}
                onChange={(e) => setPoster(e.target.value)}
                placeholder={t("components.series_edit_modal.poster_placeholder")}
              />
            </div>
            <div>
              <Text type="secondary">{t("components.series_edit_modal.label_overview")}</Text>
              <Input.TextArea
                value={overview}
                onChange={(e) => setOverview(e.target.value)}
                rows={5}
                placeholder={t("components.series_edit_modal.overview_placeholder")}
              />
            </div>
          </div>
        )}
      </Modal>
      <MediaImagePickerDialog
        open={posterPickerOpen}
        onClose={() => setPosterPickerOpen(false)}
        seriesId={series?.id}
        kind="poster"
        currentUrl={poster}
        onConfirm={setPoster}
      />
    </>
  );
}
