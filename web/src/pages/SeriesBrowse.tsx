import { CaretRightOutlined, EditOutlined } from "@ant-design/icons";
import { Empty, Input, Select, Space, Spin, message } from "antd";
import { useEffect, useMemo, useState } from "react";
import { useNavigate } from "react-router-dom";
import {
  SeriesSummary,
  fetchLibrarySeries,
  fetchSeries,
  fetchSeriesPlayTarget,
  seriesPosterSrc,
} from "../api/client";
import SeriesEditModal from "../components/SeriesEditModal";
import { fetchSeriesEpisodeMediaOrder } from "../lib/seriesEpisodeOrder";
import { storeSeriesPlaySession } from "../lib/seriesPlayback";
import { useT, type TranslateFn } from "../i18n";
import styles from "./Browse.module.css";

type Props = {
  libraryId: number;
  libraryName?: string;
  onEmpty?: () => void;
};

function fmtEpisodeCount(n: number | undefined, t: TranslateFn): string {
  if (n == null || n <= 0) return "";
  return t("pages.series_browse.episode_count", { count: n });
}

export default function SeriesBrowse({ libraryId, libraryName, onEmpty }: Props) {
  const nav = useNavigate();
  const t = useT();
  const [rows, setRows] = useState<SeriesSummary[]>([]);
  const [loading, setLoading] = useState(false);
  const [q, setQ] = useState("");
  const [sortField, setSortField] = useState<"title" | "year" | "episodes">("title");
  const [editSeries, setEditSeries] = useState<SeriesSummary | null>(null);
  const [playingId, setPlayingId] = useState<number | null>(null);

  useEffect(() => {
    let cancelled = false;
    (async () => {
      setLoading(true);
      try {
        const items = await fetchLibrarySeries(libraryId);
        if (cancelled) return;
        if (items.length === 0) {
          onEmpty?.();
          return;
        }
        setRows(items);
      } catch (e: unknown) {
        if (!cancelled) message.error((e as Error).message || t("pages.series_browse.load_failed"));
      } finally {
        if (!cancelled) setLoading(false);
      }
    })();
    return () => {
      cancelled = true;
    };
  }, [libraryId, onEmpty]);

  const filtered = useMemo(() => {
    const needle = q.trim().toLowerCase();
    let list = rows;
    if (needle) {
      list = list.filter(
        (s) =>
          (s.title || "").toLowerCase().includes(needle) ||
          (s.title_norm || "").toLowerCase().includes(needle),
      );
    }
    return [...list].sort((a, b) => {
      if (sortField === "year") {
        return (b.year ?? 0) - (a.year ?? 0);
      }
      if (sortField === "episodes") {
        return (b.episode_count ?? 0) - (a.episode_count ?? 0);
      }
      return (a.title || "").localeCompare(b.title || "", "zh");
    });
  }, [rows, q, sortField]);

  async function handlePlay(seriesId: number) {
    if (playingId != null) return;
    setPlayingId(seriesId);
    try {
      const [target, series] = await Promise.all([
        fetchSeriesPlayTarget(seriesId),
        fetchSeries(seriesId),
      ]);
      const order = await fetchSeriesEpisodeMediaOrder(series.seasons ?? []);
      storeSeriesPlaySession(seriesId, order);
      const index = order.indexOf(target.media_id);
      const idx = index >= 0 ? index : 0;
      const pos = target.position > 0 ? `&t=${target.position}` : "";
      nav(`/player/${target.media_id}?series_id=${seriesId}&index=${idx}${pos}`);
    } catch (e: unknown) {
      message.error((e as Error).message || t("pages.series_browse.cannot_play"));
    } finally {
      setPlayingId(null);
    }
  }

  function applySeriesUpdate(update: Partial<SeriesSummary> & { id: number }) {
    setRows((prev) =>
      prev.map((s) => (s.id === update.id ? { ...s, ...update } : s)),
    );
  }

  return (
    <div style={{ padding: "16px 0 32px" }}>
      <div className={styles.topBar}>
        <Space wrap className={styles.topLeftTools}>
          <Input.Search
            allowClear
            placeholder={t("pages.series_browse.search_placeholder")}
            value={q}
            onChange={(e) => setQ(e.target.value)}
            style={{ width: 220 }}
          />
          <Select
            size="small"
            value={sortField}
            onChange={setSortField}
            options={[
              { value: "title", label: t("pages.series_browse.sort_title") },
              { value: "year", label: t("pages.series_browse.sort_year") },
              { value: "episodes", label: t("pages.series_browse.sort_episodes") },
            ]}
            style={{ width: 120 }}
          />
        </Space>
        <div style={{ color: "rgba(255,255,255,0.55)", fontSize: 13 }}>
          {libraryName ? `${libraryName} · ` : ""}
          {t("pages.series_browse.library_count", { count: filtered.length })}
        </div>
      </div>

      {loading ? (
        <div className={styles.loadingWrap}>
          <Spin />
        </div>
      ) : filtered.length === 0 ? (
        <Empty description={t("pages.series_browse.no_series")} />
      ) : (
        <div className={styles.posterGrid}>
          {filtered.map((s) => {
            const poster = seriesPosterSrc(s);
            const playBusy = playingId === s.id;
            return (
              <div key={s.id} className={styles.posterCard}>
                <div
                  className={styles.posterImage}
                  role="button"
                  tabIndex={0}
                  aria-label={t("pages.series_browse.card_view_label", { title: s.title || t("pages.series_browse.unnamed") })}
                  onClick={(e) => {
                    if ((e.target as HTMLElement).closest("[data-series-card-action]")) return;
                    nav(`/series/${s.id}`);
                  }}
                  onKeyDown={(e) => {
                    if (e.key === "Enter" || e.key === " ") {
                      e.preventDefault();
                      if ((e.target as HTMLElement).closest("[data-series-card-action]")) return;
                      nav(`/series/${s.id}`);
                    }
                  }}
                >
                  {poster ? (
                    <img className={styles.gridCoverImg} src={poster} alt="" loading="lazy" />
                  ) : null}
                  <div className={styles.gridHoverShade}>
                    <button
                      type="button"
                      data-series-card-action
                      className={`${styles.gridCornerBtn} ${styles.gridEditBtn}`}
                      aria-label={t("pages.series_browse.card_edit")}
                      onClick={(e) => {
                        e.stopPropagation();
                        setEditSeries(s);
                      }}
                    >
                      <EditOutlined />
                    </button>
                    <button
                      type="button"
                      data-series-card-action
                      className={styles.gridPlayBtn}
                      aria-label={t("pages.series_browse.card_play")}
                      disabled={playBusy}
                      onClick={(e) => {
                        e.stopPropagation();
                        void handlePlay(s.id);
                      }}
                    >
                      <CaretRightOutlined />
                    </button>
                  </div>
                </div>
                <div className={styles.cardBody}>
                  <div className={styles.cardTitle}>{s.title}</div>
                  <div style={{ fontSize: 12, color: "rgba(255,255,255,0.45)", marginTop: 4 }}>
                    {[s.year && s.year > 0 ? String(s.year) : null, fmtEpisodeCount(s.episode_count, t)]
                      .filter(Boolean)
                      .join(" · ")}
                  </div>
                </div>
              </div>
            );
          })}
        </div>
      )}

      <SeriesEditModal
        series={editSeries}
        open={editSeries != null}
        onClose={() => setEditSeries(null)}
        onSaved={applySeriesUpdate}
      />
    </div>
  );
}
