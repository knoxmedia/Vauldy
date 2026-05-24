import { Input, InputNumber, Modal, Spin, Typography, message } from "antd";
import { useEffect, useState } from "react";
import {
  SeriesSummary,
  fetchSeries,
  normalizeListPosterUrl,
  updateSeries,
} from "../api/client";

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
  const [loading, setLoading] = useState(false);
  const [saving, setSaving] = useState(false);
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
        if (!cancelled) message.error((e as Error).message || "加载剧集信息失败");
      } finally {
        if (!cancelled) setLoading(false);
      }
    })();
    return () => {
      cancelled = true;
    };
  }, [open, series]);

  async function handleSave() {
    if (!series) return;
    const trimmedTitle = title.trim();
    if (!trimmedTitle) {
      message.warning("请填写标题");
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
      message.success("已保存");
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
      message.error((e as Error).message || "保存失败");
    } finally {
      setSaving(false);
    }
  }

  return (
    <Modal
      title="编辑剧集"
      open={open}
      onCancel={onClose}
      onOk={() => void handleSave()}
      okText="保存"
      cancelText="取消"
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
            <Text type="secondary">标题</Text>
            <Input value={title} onChange={(e) => setTitle(e.target.value)} placeholder="剧集标题" />
          </div>
          <div>
            <Text type="secondary">年份</Text>
            <InputNumber
              value={year}
              onChange={(v) => setYear(typeof v === "number" ? v : null)}
              min={1800}
              max={2100}
              placeholder="可选"
              style={{ width: "100%" }}
            />
          </div>
          <div>
            <Text type="secondary">海报 URL</Text>
            <Input
              value={poster}
              onChange={(e) => setPoster(e.target.value)}
              placeholder="/metadata/library/… 或完整 URL"
            />
          </div>
          <div>
            <Text type="secondary">简介</Text>
            <Input.TextArea
              value={overview}
              onChange={(e) => setOverview(e.target.value)}
              rows={5}
              placeholder="剧集简介"
            />
          </div>
        </div>
      )}
    </Modal>
  );
}
