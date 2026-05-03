import { Progress, Spin, Tag, Typography } from "antd";
import { FileImageOutlined, LeftOutlined, PlayCircleOutlined, RightOutlined } from "@ant-design/icons";
import { useEffect, useMemo, useRef, useState } from "react";
import { Link, useNavigate } from "react-router-dom";
import {
  Library,
  MediaItem,
  HistoryItem,
  fetchLibraries,
  fetchMedia,
  fetchUserHistory,
  mediaPosterSrc,
} from "../api/client";
import styles from "./Home.module.css";

const { Title } = Typography;

function libGradient(id: number, type: string) {
  const base: Record<string, [string, string]> = {
    movie: ["#1a2a4a", "#0d1528"],
    tv: ["#2a1a4a", "#150d28"],
    anime: ["#4a1a3a", "#280d20"],
    music: ["#1a3a2a", "#0d2818"],
    photo: ["#1a3a4a", "#0d2028"],
    document: ["#3a3a2a", "#202018"],
  };
  const [a, b] = base[type] || ["#252535", "#12121a"];
  const tint = id % 40;
  return `linear-gradient(135deg, ${a} 0%, ${b} 50%, hsl(${220 + tint}, 28%, ${14 + (id % 8)}%) 100%)`;
}

function formatYear(path: string) {
  const m = path?.match(/(19|20)\d{2}/);
  return m ? m[0] : "";
}

export default function HomePage() {
  const nav = useNavigate();
  const [loading, setLoading] = useState(true);
  const [libs, setLibs] = useState<Library[]>([]);
  const [history, setHistory] = useState<HistoryItem[]>([]);
  const [recent, setRecent] = useState<MediaItem[]>([]);
  const historyScrollRef = useRef<HTMLDivElement | null>(null);
  const [showHistoryLeft, setShowHistoryLeft] = useState(false);
  const [showHistoryRight, setShowHistoryRight] = useState(false);
  const recentScrollRef = useRef<HTMLDivElement | null>(null);
  const [showRecentLeft, setShowRecentLeft] = useState(false);
  const [showRecentRight, setShowRecentRight] = useState(false);

  const updateHistoryArrows = () => {
    const el = historyScrollRef.current;
    if (!el) {
      setShowHistoryLeft(false);
      setShowHistoryRight(false);
      return;
    }
    const maxLeft = el.scrollWidth - el.clientWidth;
    setShowHistoryLeft(el.scrollLeft > 4);
    setShowHistoryRight(maxLeft > 4 && el.scrollLeft < maxLeft - 4);
  };

  const scrollHistoryBy = (delta: number) => {
    const el = historyScrollRef.current;
    if (!el) return;
    el.scrollBy({ left: delta, behavior: "smooth" });
  };

  const updateRecentArrows = () => {
    const el = recentScrollRef.current;
    if (!el) {
      setShowRecentLeft(false);
      setShowRecentRight(false);
      return;
    }
    const maxLeft = el.scrollWidth - el.clientWidth;
    setShowRecentLeft(el.scrollLeft > 4);
    setShowRecentRight(maxLeft > 4 && el.scrollLeft < maxLeft - 4);
  };

  const scrollRecentBy = (delta: number) => {
    const el = recentScrollRef.current;
    if (!el) return;
    el.scrollBy({ left: delta, behavior: "smooth" });
  };

  useEffect(() => {
    let cancelled = false;
    setLoading(true);
    // 分项处理：任一接口失败不应清空其它接口的成功数据（Promise.all 会全失败）
    void (async () => {
      const [libR, histR, mediaR] = await Promise.allSettled([
        fetchLibraries(),
        fetchUserHistory(24),
        fetchMedia(undefined, { sort: "created_desc", limit: 24 }),
      ]);
      if (cancelled) return;
      if (libR.status === "fulfilled") {
        setLibs(Array.isArray(libR.value) ? libR.value : []);
      } else {
        setLibs([]);
      }
      if (histR.status === "fulfilled") {
        setHistory(histR.value.filter((h) => h.media_id > 0));
      } else {
        setHistory([]);
      }
      if (mediaR.status === "fulfilled") {
        setRecent(Array.isArray(mediaR.value) ? mediaR.value : []);
      } else {
        setRecent([]);
      }
      setLoading(false);
    })();
    return () => {
      cancelled = true;
    };
  }, []);

  useEffect(() => {
    let cancelled = false;
    const timer = window.setInterval(() => {
      void fetchLibraries()
        .then((items) => {
          if (!cancelled) {
            setLibs(Array.isArray(items) ? items : []);
          }
        })
        .catch(() => {
          // keep existing list on transient polling failures
        });
    }, 3000);
    return () => {
      cancelled = true;
      window.clearInterval(timer);
    };
  }, []);

  /** Prefer poster_url from recent list when the same media appears in「继续观看」. */
  const recentPosterById = useMemo(() => {
    const m = new Map<number, string>();
    for (const r of recent) {
      const u = (r.poster_url || "").trim();
      if (u) m.set(r.id, u);
    }
    return m;
  }, [recent]);

  useEffect(() => {
    updateHistoryArrows();
    updateRecentArrows();
    const onResize = () => {
      updateHistoryArrows();
      updateRecentArrows();
    };
    window.addEventListener("resize", onResize);
    return () => window.removeEventListener("resize", onResize);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [history.length, recent.length]);

  if (loading) {
    return (
      <div className={styles.page} style={{ display: "flex", justifyContent: "center", paddingTop: 80 }}>
        <Spin size="large" />
      </div>
    );
  }

  return (
    <div className={styles.page}>
      <section className={styles.section}>
        <div className={styles.sectionHead}>
          <Title level={3} className={styles.title}>
            媒体库
          </Title>
        </div>
        {libs.length === 0 ? (
          <div className={styles.emptyHint}>暂无媒体库，请由管理员在「媒体库」中添加。</div>
        ) : (
          <div className={styles.rowScroll}>
            {libs.map((lib) => {
              const processed = lib.scan_processed_count ?? 0;
              const total = lib.scan_total_count ?? 0;
              const percent = total > 0 ? Math.max(0, Math.min(100, Math.round((processed / total) * 100))) : 0;
              const progressColor = percent < 50 ? "#13b6ff" : percent < 90 ? "#faad14" : "#52c41a";
              return (
                <Link
                  key={lib.id}
                  to={`/browse?library_id=${lib.id}`}
                  className={styles.libCard}
                  style={{ background: libGradient(lib.id, lib.type) }}
                >
                  {lib.scan_status === "running" ? (
                    <div className={styles.libScanOverlay}>
                      <Progress
                        type="circle"
                        size={48}
                        percent={percent}
                        strokeColor={progressColor}
                        railColor="rgba(255,255,255,0.2)"
                        className={styles.libScanCircle}
                      />
                      <div className={styles.libScanInfo}>
                        <div className={styles.libScanTitle}>扫描中</div>
                        <div className={styles.libScanMeta}>
                          {total > 0 ? `${processed}/${total}` : `已处理 ${processed}`} · 新增 {lib.scan_added_count ?? 0}
                        </div>
                      </div>
                    </div>
                  ) : null}
                  <div className={styles.libCardInner}>
                    <div className={styles.libName}>{lib.name}</div>
                    <div className={styles.libMeta}>
                      {lib.type === "movie"
                        ? "电影"
                        : lib.type === "tv"
                          ? "剧集"
                          : lib.type === "anime"
                            ? "动漫"
                            : lib.type}{" "}
                      · {lib.media_count ?? 0} 项
                    </div>
                  </div>
                </Link>
              );
            })}
          </div>
        )}
      </section>

      <section className={styles.section}>
        <div className={styles.sectionHead}>
          <Title level={3} className={styles.title}>
            继续观看
          </Title>
        </div>
        {history.length === 0 ? (
          <div className={styles.emptyHint}>暂无播放记录，去「浏览媒体」中选一部开始观看吧。</div>
        ) : (
          <div className={styles.carouselWrap}>
            {showHistoryLeft ? (
              <button
                type="button"
                className={`${styles.carouselArrow} ${styles.carouselArrowLeft}`}
                onClick={() => scrollHistoryBy(-340)}
                aria-label="向左滚动"
              >
                <LeftOutlined />
              </button>
            ) : null}
            {showHistoryRight ? (
              <button
                type="button"
                className={`${styles.carouselArrow} ${styles.carouselArrowRight}`}
                onClick={() => scrollHistoryBy(340)}
                aria-label="向右滚动"
              >
                <RightOutlined />
              </button>
            ) : null}
            <div
              ref={historyScrollRef}
              className={`${styles.rowScroll} ${styles.rowScrollNoBar}`}
              onScroll={updateHistoryArrows}
            >
              {history.map((h) => {
                const dur = h.duration > 0 ? h.duration : 1;
                const pct = Math.min(100, Math.round((h.position / dur) * 100));
                const thumbSrc = mediaPosterSrc({
                  id: h.media_id,
                  poster_url: recentPosterById.get(h.media_id) || "",
                });
                return (
                  <div
                    key={`${h.file_id}-${h.update_at}`}
                    className={styles.thumb169}
                    role="button"
                    tabIndex={0}
                    onClick={() => nav(`/detail/${h.media_id}`)}
                    onKeyDown={(e) => {
                      if (e.key === "Enter" || e.key === " ") {
                        e.preventDefault();
                        nav(`/detail/${h.media_id}`);
                      }
                    }}
                  >
                    <div className={styles.thumb169Box}>
                      <img
                        className={styles.thumb169Cover}
                        src={thumbSrc}
                        alt=""
                        loading="lazy"
                        decoding="async"
                        onError={(e) => {
                          e.currentTarget.style.display = "none";
                        }}
                      />
                      <div className={styles.thumb169IconBg} aria-hidden>
                        <PlayCircleOutlined />
                      </div>
                      <button
                        type="button"
                        className={styles.thumb169PlayBtn}
                        onClick={(e) => {
                          e.stopPropagation();
                          nav(`/player/${h.media_id}?t=${h.position}`);
                        }}
                        aria-label={`播放 ${h.title || "影片"}`}
                      >
                        <PlayCircleOutlined />
                      </button>
                    </div>
                    <div className={styles.progressBar}>
                      <div className={styles.progressFill} style={{ width: `${pct}%` }} />
                    </div>
                    <div className={styles.thumb169Cap}>
                      <div className={styles.thumb169Title}>{h.title || "未命名"}</div>
                      <div className={styles.thumb169Sub}>
                        {pct}% · {formatYear(h.file_path)}
                      </div>
                      <div className={styles.thumb169Sub} style={{ display: "flex", gap: 6, marginTop: 4, alignItems: "center", flexWrap: "wrap" }}>
                        {h.completed === 1 ? <Tag color="green" style={{ marginInlineEnd: 0 }}>已看完</Tag> : null}
                        <Tag color="blue" style={{ marginInlineEnd: 0 }}>播放 {h.play_count ?? 0} 次</Tag>
                      </div>
                    </div>
                  </div>
                );
              })}
            </div>
          </div>
        )}
      </section>

      <section className={styles.section}>
        <div className={styles.sectionHead}>
          <Title level={3} className={styles.title}>
            最近添加
          </Title>
          <Link to="/browse?sort=recent" className={styles.more}>
            更多 &gt;
          </Link>
        </div>
        {recent.length === 0 ? (
          <div className={styles.emptyHint}>暂无媒体条目。</div>
        ) : (
          <div className={styles.carouselWrap}>
            {showRecentLeft ? (
              <button
                type="button"
                className={`${styles.carouselArrow} ${styles.carouselArrowLeft}`}
                onClick={() => scrollRecentBy(-340)}
                aria-label="向左滚动"
              >
                <LeftOutlined />
              </button>
            ) : null}
            {showRecentRight ? (
              <button
                type="button"
                className={`${styles.carouselArrow} ${styles.carouselArrowRight}`}
                onClick={() => scrollRecentBy(340)}
                aria-label="向右滚动"
              >
                <RightOutlined />
              </button>
            ) : null}
            <div
              ref={recentScrollRef}
              className={`${styles.rowScroll} ${styles.rowScrollNoBar}`}
              onScroll={updateRecentArrows}
            >
              {recent.map((m) => (
                <div
                  key={m.id}
                  className={styles.thumbPoster}
                  role="button"
                  tabIndex={0}
                  onClick={() => nav(`/detail/${m.id}`)}
                  onKeyDown={(e) => {
                    if (e.key === "Enter" || e.key === " ") {
                      e.preventDefault();
                      nav(`/detail/${m.id}`);
                    }
                  }}
                >
                  <div className={styles.posterBox}>
                    <img
                      className={styles.posterFillImg}
                      src={mediaPosterSrc(m)}
                      alt=""
                      loading="lazy"
                      decoding="async"
                      onError={(e) => {
                        e.currentTarget.style.display = "none";
                      }}
                    />
                    <div className={styles.posterEmpty}>
                      <FileImageOutlined />
                      <span>暂无海报</span>
                    </div>
                  </div>
                  <button
                    type="button"
                    className={styles.posterPlayBtn}
                    onClick={(e) => {
                      e.stopPropagation();
                      nav(`/player/${m.id}`);
                    }}
                    aria-label={`播放 ${m.title || "影片"}`}
                  >
                    <PlayCircleOutlined />
                  </button>
                  <div className={styles.posterCap}>
                    <div className={styles.posterTitle}>{m.title || "未命名"}</div>
                  </div>
                </div>
              ))}
            </div>
          </div>
        )}
      </section>
    </div>
  );
}
