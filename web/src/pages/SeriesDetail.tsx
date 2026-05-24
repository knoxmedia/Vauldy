import { ArrowLeftOutlined } from "@ant-design/icons";
import { Button, Empty, Spin, Tabs, Tag, message } from "antd";
import { useEffect, useMemo, useState } from "react";
import { useNavigate, useParams } from "react-router-dom";
import {
  EpisodeRow,
  SeasonSummary,
  SeriesDetail,
  fetchSeasonEpisodes,
  fetchSeries,
  normalizeListPosterUrl,
  seriesPosterSrc,
} from "../api/client";
import ToolbarPlayIcon from "../components/ToolbarPlayIcon";
import styles from "./SeriesDetail.module.css";

function fmtDuration(sec?: number): string {
  if (!sec || sec <= 0) return "—";
  const h = Math.floor(sec / 3600);
  const m = Math.floor((sec % 3600) / 60);
  if (h > 0) return `${h} 小时 ${m} 分钟`;
  return `${m} 分钟`;
}

function parseSeriesMeta(metaJSON?: string): {
  overview?: string;
  poster?: string;
  backdrop?: string;
  rating?: number;
} {
  if (!metaJSON) return {};
  try {
    const root = JSON.parse(metaJSON) as { scrape?: Record<string, unknown> };
    const scrape = root.scrape;
    if (!scrape || typeof scrape !== "object") return {};
    const extra = (scrape.extra as Record<string, unknown>) || {};
    return {
      overview: typeof scrape.overview === "string" ? scrape.overview : undefined,
      poster:
        normalizeListPosterUrl(
          String(scrape.poster || extra.poster || extra.series_poster || ""),
        ) || undefined,
      backdrop:
        normalizeListPosterUrl(
          String(scrape.backdrop || extra.backdrop || extra.series_backdrop || ""),
        ) || undefined,
      rating: typeof scrape.rating === "number" ? scrape.rating : undefined,
    };
  } catch {
    return {};
  }
}

function pickPrimaryMediaId(ep: EpisodeRow): number | null {
  const versions = ep.versions ?? [];
  if (versions.length === 0) return null;
  const sorted = [...versions].sort((a, b) => (a.sort_order ?? 0) - (b.sort_order ?? 0));
  return sorted[0]?.media_id ?? null;
}

function pickBestVersion(ep: EpisodeRow) {
  const versions = ep.versions ?? [];
  if (versions.length === 0) return null;
  return [...versions].sort((a, b) => {
    const score = (v: typeof versions[0]) => (v.height ?? 0) * 1000 + (v.bitrate ?? 0);
    return score(b) - score(a);
  })[0];
}

export default function SeriesDetailPage() {
  const { id } = useParams();
  const seriesId = Number(id);
  const nav = useNavigate();
  const [detail, setDetail] = useState<SeriesDetail | null>(null);
  const [loading, setLoading] = useState(true);
  const [activeSeasonId, setActiveSeasonId] = useState<number | null>(null);
  const [episodes, setEpisodes] = useState<EpisodeRow[]>([]);
  const [epLoading, setEpLoading] = useState(false);

  useEffect(() => {
    if (!seriesId || Number.isNaN(seriesId)) return;
    let cancelled = false;
    (async () => {
      setLoading(true);
      try {
        const data = await fetchSeries(seriesId);
        if (cancelled) return;
        setDetail(data);
        const seasons = data.seasons ?? [];
        if (seasons.length > 0) {
          setActiveSeasonId(seasons[0].id);
        }
      } catch (e: unknown) {
        if (!cancelled) message.error((e as Error).message || "加载失败");
      } finally {
        if (!cancelled) setLoading(false);
      }
    })();
    return () => {
      cancelled = true;
    };
  }, [seriesId]);

  useEffect(() => {
    if (!activeSeasonId) {
      setEpisodes([]);
      return;
    }
    let cancelled = false;
    (async () => {
      setEpLoading(true);
      try {
        const items = await fetchSeasonEpisodes(activeSeasonId);
        if (!cancelled) setEpisodes(items);
      } catch (e: unknown) {
        if (!cancelled) message.error((e as Error).message || "加载集数失败");
      } finally {
        if (!cancelled) setEpLoading(false);
      }
    })();
    return () => {
      cancelled = true;
    };
  }, [activeSeasonId]);

  const meta = useMemo(() => parseSeriesMeta(detail?.meta_json), [detail?.meta_json]);
  const heroPoster = useMemo(() => {
    if (!detail) return "";
    return (
      seriesPosterSrc({
        id: detail.id,
        poster_url: detail.poster_url ?? detail.poster,
        poster: meta.poster,
      }) ||
      meta.poster ||
      ""
    );
  }, [detail, meta.poster]);

  const seasons = detail?.seasons ?? [];

  if (loading) {
    return (
      <div className={styles.loadingWrap}>
        <Spin size="large" />
      </div>
    );
  }

  if (!detail) {
    return <Empty description="剧集不存在" />;
  }

  return (
    <div className={styles.page}>
      <div className={styles.topBar}>
        <Button type="text" icon={<ArrowLeftOutlined />} onClick={() => nav(-1)} className={styles.backBtn}>
          返回
        </Button>
      </div>
      <div
        className={styles.hero}
        style={
          meta.backdrop
            ? { backgroundImage: `linear-gradient(to bottom, rgba(0,0,0,0.35), rgba(0,0,0,0.92)), url(${meta.backdrop})` }
            : undefined
        }
      >
        <div className={styles.heroBody}>
          <div className={styles.heroPoster}>
            {heroPoster ? (
              <img src={heroPoster} alt="" />
            ) : (
              <div className={styles.heroPosterFallback} />
            )}
          </div>
          <div className={styles.heroInfo}>
            <h1 className={styles.heroTitle}>{detail.title}</h1>
            <div className={styles.heroTags}>
              {detail.year ? <Tag>{detail.year}</Tag> : null}
              <Tag color="blue">{seasons.length} 季</Tag>
              {meta.rating ? <Tag color="gold">评分 {meta.rating.toFixed(1)}</Tag> : null}
            </div>
            {meta.overview ? <p className={styles.heroOverview}>{meta.overview}</p> : null}
            {(detail.folder_paths?.length ?? 0) > 0 ? (
              <div className={styles.folderHint}>
                来源目录：{detail.folder_paths!.length} 个路径已合并
              </div>
            ) : null}
          </div>
        </div>
      </div>

      <div className={styles.content}>
        {seasons.length === 0 ? (
          <Empty description="暂无季/集数据" />
        ) : (
          <Tabs
            activeKey={String(activeSeasonId ?? seasons[0]?.id)}
            onChange={(key) => setActiveSeasonId(Number(key))}
            items={seasons.map((s: SeasonSummary) => ({
              key: String(s.id),
              label: s.name || `第 ${s.season_num} 季`,
            }))}
          />
        )}

        {epLoading ? (
          <div className={styles.epLoading}>
            <Spin />
          </div>
        ) : episodes.length === 0 ? (
          <Empty description="该季暂无剧集" />
        ) : (
          <div className={styles.episodeList}>
            {episodes.map((ep) => {
              const best = pickBestVersion(ep);
              const mediaId = pickPrimaryMediaId(ep);
              const epLabel = `E${String(ep.episode_num).padStart(2, "0")}`;
              const versionCount = ep.versions?.length ?? 0;
              return (
                <div key={ep.id} className={styles.episodeRow}>
                  <div className={styles.episodeNum}>{epLabel}</div>
                  <div className={styles.episodeMain}>
                    {mediaId ? (
                      <button
                        type="button"
                        className={styles.episodeTitle}
                        onClick={() => nav(`/detail/${mediaId}`)}
                      >
                        {ep.title || `第 ${ep.episode_num} 集`}
                      </button>
                    ) : (
                      <div className={styles.episodeTitle}>
                        {ep.title || `第 ${ep.episode_num} 集`}
                      </div>
                    )}
                    <div className={styles.episodeMeta}>
                      {fmtDuration(best?.duration ?? ep.duration)}
                      {best?.width && best?.height ? ` · ${best.width}×${best.height}` : ""}
                      {versionCount > 1 ? ` · ${versionCount} 个版本` : ""}
                    </div>
                    {versionCount > 1 ? (
                      <div className={styles.versionList}>
                        {ep.versions!.map((v) => (
                          <Button
                            key={v.media_id}
                            size="small"
                            type="link"
                            onClick={() => nav(`/player/${v.media_id}`)}
                          >
                            {v.height ? `${v.height}p` : v.format || "播放"}
                            {v.bitrate ? ` · ${Math.round(v.bitrate / 1000)}k` : ""}
                          </Button>
                        ))}
                      </div>
                    ) : null}
                  </div>
                  <div className={styles.episodeActions}>
                    {mediaId ? (
                      <Button
                        type="primary"
                        size="large"
                        icon={<ToolbarPlayIcon className={styles.episodePlaySvg} />}
                        className={styles.playBtn}
                        onClick={() => nav(`/player/${mediaId}`)}
                      >
                        播放
                      </Button>
                    ) : (
                      <span className={styles.noMedia}>无关联文件</span>
                    )}
                  </div>
                </div>
              );
            })}
          </div>
        )}
      </div>
    </div>
  );
}
