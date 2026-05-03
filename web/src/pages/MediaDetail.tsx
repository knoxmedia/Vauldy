import { Button, Progress, Spin, Statistic, Tag, Typography, message } from "antd";
import {
  ArrowLeftOutlined,
  CalendarOutlined,
  ClockCircleOutlined,
  FileImageOutlined,
  PlayCircleOutlined,
  SoundOutlined,
  TeamOutlined,
  VideoCameraOutlined,
} from "@ant-design/icons";
import { useEffect, useMemo, useState } from "react";
import { Link, useNavigate, useParams } from "react-router-dom";
import {
  MediaDetail,
  HistoryItem,
  MediaItem,
  MediaStats,
  fetchMedia,
  fetchMediaDetail,
  fetchMediaStats,
  fetchUserHistory,
  mediaPosterSrc,
} from "../api/client";
import styles from "./MediaDetail.module.css";

type ParsedMeta = {
  container?: string;
  videoCodec?: string;
  audioCodec?: string;
  videoProfile?: string;
  audioChannels?: string;
  audioLanguage?: string;
  bitrate?: number;
  fps?: string;
  overview?: string;
  releaseDate?: string;
  rating?: number;
  director?: string[];
  poster?: string;
  backdrop?: string;
  logo?: string;
  cast?: Array<{ name: string; role?: string; avatar?: string }>;
  subtitleCodecs: string[];
};

function parseMeta(metaJson?: string): ParsedMeta {
  if (!metaJson) return { subtitleCodecs: [] };
  try {
    const raw = JSON.parse(metaJson) as {
      format?: { format_name?: string };
      streams?: Array<{
        codec_type?: string;
        codec_name?: string;
        profile?: string;
        channels?: number | string;
        avg_frame_rate?: string;
        bit_rate?: string | number;
        tags?: { language?: string };
      }>;
      scrape?: {
        overview?: string;
        release_date?: string;
        rating?: number;
        poster?: string;
        backdrop?: string;
        logo?: string;
        extra?: Record<string, unknown>;
      };
    };
    const out: ParsedMeta = {
      container: raw.format?.format_name || "",
      subtitleCodecs: [],
      director: [],
      cast: [],
    };
    for (const st of raw.streams ?? []) {
      const type = (st.codec_type || "").toLowerCase();
      const codec = st.codec_name || "";
      if (type === "video" && !out.videoCodec) {
        out.videoCodec = codec;
        out.videoProfile = st.profile || "";
        out.fps = st.avg_frame_rate || "";
      }
      if (type === "audio" && !out.audioCodec) {
        out.audioCodec = codec;
        out.audioChannels = st.channels ? String(st.channels) : "";
        out.audioLanguage = st.tags?.language || "";
      }
      if (type === "subtitle" && codec) out.subtitleCodecs.push(codec);
      const bitRate = typeof st.bit_rate === "string" ? Number(st.bit_rate) : st.bit_rate;
      if (!Number.isNaN(Number(bitRate)) && Number(bitRate) > 0 && !out.bitrate) {
        out.bitrate = Number(bitRate);
      }
    }

    const scrape = raw.scrape;
    if (scrape) {
      out.overview = scrape.overview || "";
      out.releaseDate = scrape.release_date || "";
      out.rating = scrape.rating || 0;
      const extra = scrape.extra || {};
      const director =
        (extra.director as string[]) ||
        (extra.directors as string[]) ||
        (extra.crew as string[]) ||
        [];
      if (Array.isArray(director)) {
        out.director = director.filter((x): x is string => typeof x === "string" && x.trim().length > 0);
      }
      const actors =
        (extra.cast as Array<Record<string, unknown>>) ||
        (extra.actors as Array<Record<string, unknown>>) ||
        [];
      if (Array.isArray(actors)) {
        out.cast = actors
          .map((x) => ({
            name: String(x.name || x.actor || ""),
            role: x.role ? String(x.role) : x.character ? String(x.character) : "",
            avatar: x.profile_path
              ? String(x.profile_path)
              : x.avatar
                ? String(x.avatar)
                : x.image
                  ? String(x.image)
                  : "",
          }))
          .filter((x) => x.name.trim().length > 0);
      }
      const pick = (a: string, b: string) => {
        const x = (a || "").trim();
        if (x) return x;
        return (b || "").trim();
      };
      out.poster = pick(
        typeof extra.poster === "string" ? extra.poster : "",
        typeof scrape.poster === "string" ? scrape.poster : ""
      );
      out.backdrop = pick(
        typeof extra.backdrop === "string" ? extra.backdrop : "",
        typeof scrape.backdrop === "string" ? scrape.backdrop : ""
      );
      out.logo = pick(
        typeof extra.logo === "string" ? extra.logo : "",
        typeof scrape.logo === "string" ? scrape.logo : ""
      );
    }
    return out;
  } catch {
    return { subtitleCodecs: [] };
  }
}

function fmtSeconds(sec?: number) {
  if (!sec || sec <= 0) return "—";
  const h = Math.floor(sec / 3600);
  const m = Math.floor((sec % 3600) / 60);
  const s = sec % 60;
  if (h > 0) return `${h}h ${m}m ${s}s`;
  return `${m}m ${s}s`;
}

function fmtDate(v?: string) {
  if (!v) return "—";
  return v.length > 10 ? v.slice(0, 10) : v;
}

function fmtBitrate(v?: number) {
  if (!v || v <= 0) return "—";
  const kb = Math.round(v / 1000);
  return `${kb} kbps`;
}

export default function MediaDetailPage() {
  const { id } = useParams();
  const nav = useNavigate();
  const mediaId = Number(id || "");
  const [loading, setLoading] = useState(true);
  const [detail, setDetail] = useState<MediaDetail | null>(null);
  const [stats, setStats] = useState<MediaStats | null>(null);
  const [related, setRelated] = useState<MediaItem[]>([]);
  const [historyItem, setHistoryItem] = useState<HistoryItem | null>(null);
  const [brokenImages, setBrokenImages] = useState<Record<string, true>>({});

  useEffect(() => {
    if (!mediaId || Number.isNaN(mediaId)) {
      message.error("无效的媒体 ID");
      nav("/browse", { replace: true });
      return;
    }
    setLoading(true);
    void Promise.allSettled([fetchMediaDetail(mediaId), fetchMediaStats(mediaId), fetchUserHistory(300)]).then(async ([d, s, h]) => {
      if (d.status === "fulfilled") {
        setDetail(d.value);
        try {
          const libItems = await fetchMedia(d.value.library_id, { sort: "created_desc", limit: 40 });
          setRelated(libItems.filter((x) => x.id !== d.value.id).slice(0, 8));
        } catch {
          setRelated([]);
        }
      } else {
        message.error("加载影片信息失败");
      }
      if (s.status === "fulfilled") {
        setStats(s.value);
      } else {
        setStats(null);
      }
      if (h.status === "fulfilled") {
        const found = h.value.find((x) => x.media_id === mediaId) || null;
        setHistoryItem(found);
      } else {
        setHistoryItem(null);
      }
      setLoading(false);
    });
  }, [mediaId, nav]);

  const meta = useMemo(() => parseMeta(detail?.meta_json), [detail?.meta_json]);
  const posterLetter = (detail?.title || "?").slice(0, 1).toUpperCase();
  const avgPct = Math.round(stats?.avg_progress_percent ?? 0);
  const runtime = fmtSeconds(detail?.duration);
  const overview = meta.overview || "暂无简介";
  const castList = meta.cast?.slice(0, 18) ?? [];
  const resumeSeconds = historyItem?.position ?? 0;
  const playableDuration = detail?.duration ?? 0;
  const canResume = resumeSeconds > 0 && playableDuration > 0 && resumeSeconds < playableDuration - 8;
  const isCompleted = historyItem?.completed === 1;
  const showResumeActions = canResume && !isCompleted;
  const resumeTarget = `/player/${detail?.id}?t=${resumeSeconds}`;
  const playFromStartTarget = `/player/${detail?.id}`;
  const posterCandidate =
    (meta.poster || "").trim() ||
    (detail?.id ? mediaPosterSrc({ id: detail.id, poster_url: "" }) : "");
  const posterUrl = posterCandidate && !brokenImages.poster ? posterCandidate : "";
  const bannerUrl = meta.backdrop && !brokenImages.backdrop ? meta.backdrop : "";
  const logoUrl = meta.logo && !brokenImages.logo ? meta.logo : "";

  if (loading) {
    return (
      <div className={styles.loadingWrap}>
        <Spin size="large" />
      </div>
    );
  }
  if (!detail) return null;

  return (
    <div className={styles.page}>
      <section className={styles.hero}>
        {bannerUrl ? (
          <img
            src={bannerUrl}
            alt=""
            className={styles.heroBanner}
            onError={() => setBrokenImages((prev) => ({ ...prev, backdrop: true }))}
          />
        ) : null}
        <div className={styles.heroBackdrop} />
        <div className={styles.head}>
          <Button type="text" icon={<ArrowLeftOutlined />} onClick={() => nav(-1)} className={styles.backBtn}>
            返回
          </Button>
        </div>

        <div className={styles.heroBody}>
          <div className={styles.poster}>
            {posterUrl ? (
              <img
                src={posterUrl}
                alt={`${detail.title || "影片"} 海报`}
                className={styles.posterImg}
                onError={() => setBrokenImages((prev) => ({ ...prev, poster: true }))}
              />
            ) : (
              <div className={styles.posterFallback}>
                <FileImageOutlined />
                <span>暂无海报</span>
              </div>
            )}
          </div>
          <div className={styles.heroInfo}>
            <div className={styles.badges}>
              <Tag color="blue">{detail.file_type || "video"}</Tag>
              <Tag>{detail.width && detail.height ? `${detail.width}x${detail.height}` : "Unknown Res"}</Tag>
              <Tag>{meta.videoCodec?.toUpperCase() || "Video"}</Tag>
              <Tag>{meta.audioCodec?.toUpperCase() || "Audio"}</Tag>
              {historyItem?.completed === 1 ? <Tag color="green">已看完</Tag> : null}
              {historyItem ? <Tag color="cyan">播放 {historyItem.play_count ?? 0} 次</Tag> : null}
            </div>
            <Typography.Title level={2} className={styles.title}>
              {detail.title || "未命名影片"}
            </Typography.Title>
            {logoUrl ? (
              <img
                src={logoUrl}
                alt={`${detail.title || "影片"} logo`}
                className={styles.logoLayer}
                onError={() => setBrokenImages((prev) => ({ ...prev, logo: true }))}
              />
            ) : (
              <div className={styles.logoText}>{posterLetter}</div>
            )}
            <Typography.Text className={styles.subtitle}>
              {detail.original_title || detail.file_path}
            </Typography.Text>
            <div className={styles.infoChips}>
              <span><CalendarOutlined /> {fmtDate(meta.releaseDate || detail.created_at)}</span>
              <span><ClockCircleOutlined /> {runtime}</span>
              <span><VideoCameraOutlined /> {meta.container || detail.format || "container"}</span>
            </div>
            <div className={styles.actions}>
              {showResumeActions ? (
                <>
                  <Button type="primary" size="large" icon={<PlayCircleOutlined />} onClick={() => nav(resumeTarget)}>
                    继续播放
                  </Button>
                  <Button size="large" type="default" style={{ opacity: 0.82 }} onClick={() => nav(playFromStartTarget)}>
                    从头播放
                  </Button>
                </>
              ) : (
                <Button type="primary" size="large" icon={<PlayCircleOutlined />} onClick={() => nav(playFromStartTarget)}>
                  播放
                </Button>
              )}
              <Button size="large" onClick={() => nav(`/browse?library_id=${detail.library_id}`)}>
                返回媒体库
              </Button>
            </div>
          </div>
        </div>
      </section>

      <section className={styles.overviewPanel}>
        <Typography.Title level={4}>简介</Typography.Title>
        <Typography.Paragraph className={styles.overviewText}>{overview}</Typography.Paragraph>
      </section>

      <section className={styles.grid}>
        <div className={styles.panel}>
          <Typography.Title level={4}>文件信息与技术规格</Typography.Title>
          <div className={styles.specGrid}>
            <div className={styles.specItem}>
              <div className={styles.specLabel}>容器</div>
              <div className={styles.specValue}>{meta.container || detail.format || "—"}</div>
            </div>
            <div className={styles.specItem}>
              <div className={styles.specLabel}>视频</div>
              <div className={styles.specValue}>
                {meta.videoCodec || "—"}
                {meta.videoProfile ? ` / ${meta.videoProfile}` : ""}
              </div>
            </div>
            <div className={styles.specItem}>
              <div className={styles.specLabel}>音频</div>
              <div className={styles.specValue}>
                {meta.audioCodec || "—"}
                {meta.audioChannels ? ` / ${meta.audioChannels}ch` : ""}
              </div>
            </div>
            <div className={styles.specItem}>
              <div className={styles.specLabel}>字幕</div>
              <div className={styles.specValue}>
                {meta.subtitleCodecs.length ? meta.subtitleCodecs.join(", ") : "无内嵌字幕"}
              </div>
            </div>
            <div className={styles.specItem}>
              <div className={styles.specLabel}>比特率</div>
              <div className={styles.specValue}>{fmtBitrate(meta.bitrate || detail.bitrate)}</div>
            </div>
            <div className={styles.specItem}>
              <div className={styles.specLabel}>帧率</div>
              <div className={styles.specValue}>{meta.fps || "—"}</div>
            </div>
            <div className={styles.specItem}>
              <div className={styles.specLabel}>配音/语言</div>
              <div className={styles.specValue}>
                <SoundOutlined /> {meta.audioLanguage || "默认音轨"}
              </div>
            </div>
          </div>
        </div>

        <div className={styles.panel}>
          <Typography.Title level={4}>播放统计</Typography.Title>
          <div className={styles.stats}>
            <Statistic title="观看人数" value={stats?.watch_users ?? 0} />
            <Statistic title="平均观看时长" value={fmtSeconds(Math.round(stats?.avg_position_seconds ?? 0))} />
            <Statistic title="最近播放" value={stats?.latest_watch_at || "—"} />
          </div>
          <div className={styles.progressBox}>
            <div className={styles.specLabel}>平均观看进度</div>
            <Progress percent={avgPct} strokeColor="#00b3ff" trailColor="rgba(255,255,255,0.18)" />
          </div>
          <div className={styles.progressBox}>
            <div className={styles.specLabel}>我的观看记录</div>
            <div style={{ display: "flex", gap: 10, flexWrap: "wrap", alignItems: "center" }}>
              <Tag color={historyItem?.completed === 1 ? "green" : "default"}>
                {historyItem?.completed === 1 ? "已看完" : "未看完"}
              </Tag>
              <Tag color="blue">最后位置：{fmtSeconds(historyItem?.position ?? 0)}</Tag>
              <Tag color="cyan">播放次数：{historyItem?.play_count ?? 0}</Tag>
              <Tag>开始：{historyItem?.play_start_at || "—"}</Tag>
              <Tag>结束：{historyItem?.play_end_at || "—"}</Tag>
            </div>
          </div>
          <div className={styles.directorLine}>
            <TeamOutlined /> 导演：{meta.director?.length ? meta.director.join(" / ") : "暂无"}
          </div>
        </div>
      </section>

      <section className={styles.panel}>
        <Typography.Title level={4}>演职人员</Typography.Title>
        {castList.length === 0 ? (
          <div className={styles.empty}>暂无演员信息</div>
        ) : (
          <div className={styles.castRow}>
            {castList.map((member, idx) => (
              <div key={`${member.name}-${idx}`} className={styles.castCard}>
                {member.avatar && !brokenImages[`actor-${idx}`] ? (
                  <img
                    src={member.avatar}
                    alt={member.name}
                    className={styles.castAvatarImage}
                    onError={() => setBrokenImages((prev) => ({ ...prev, [`actor-${idx}`]: true }))}
                  />
                ) : (
                  <div className={styles.castAvatarEmpty} />
                )}
                <div className={styles.castName}>{member.name}</div>
                <div className={styles.castRole}>{member.role || "演员"}</div>
              </div>
            ))}
          </div>
        )}
      </section>

      <section className={styles.panel}>
        <Typography.Title level={4}>相关推荐影片</Typography.Title>
        {related.length === 0 ? (
          <div className={styles.empty}>暂无相关推荐</div>
        ) : (
          <div className={styles.relatedRow}>
            {related.map((m) => (
              <Link key={m.id} to={`/detail/${m.id}`} className={styles.relatedCard}>
                <div className={styles.relatedPoster}>{(m.title || "?").slice(0, 1)}</div>
                <div className={styles.relatedTitle}>{m.title || "未命名"}</div>
              </Link>
            ))}
          </div>
        )}
      </section>
    </div>
  );
}
