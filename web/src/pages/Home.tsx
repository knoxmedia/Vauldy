import { Dropdown, Progress, Spin, Tag, Typography } from "antd";
import type { MenuProps } from "antd";
import {
  CaretRightOutlined,
  CheckOutlined,
  EditOutlined,
  EllipsisOutlined,
  FileImageOutlined,
  LeftOutlined,
  PlayCircleOutlined,
  RightOutlined,
} from "@ant-design/icons";
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
    video: ["#2a2a3a", "#14141c"],
  };
  const [a, b] = base[type] || ["#252535", "#12121a"];
  const tint = id % 40;
  return `linear-gradient(135deg, ${a} 0%, ${b} 50%, hsl(${220 + tint}, 28%, ${14 + (id % 8)}%) 100%)`;
}

function formatYear(path: string) {
  const m = path?.match(/(19|20)\d{2}/);
  return m ? m[0] : "";
}

function mediaReleaseYear(m: MediaItem): string {
  if (typeof m.year === "number" && m.year > 0) return String(m.year);
  const rd = (m.release_date || "").trim();
  if (rd.length >= 4) {
    const y = rd.slice(0, 4);
    if (/^\d{4}$/.test(y)) return y;
  }
  return formatYear(m.file_path);
}

/** 继续观看：悬停遮罩与角标逻辑对齐「最近添加的电影」 */
function HistoryContinueCard({
  h,
  nav,
  thumbSrc,
  pct,
}: {
  h: HistoryItem;
  nav: ReturnType<typeof useNavigate>;
  thumbSrc: string;
  pct: number;
}) {
  const [posterFailed, setPosterFailed] = useState(false);
  const [selected, setSelected] = useState(false);
  const moreItems: MenuProps["items"] = useMemo(
    () => [
      { key: "detail", label: "查看详情", onClick: () => nav(`/detail/${h.media_id}`) },
      { key: "play", label: "继续播放", onClick: () => nav(`/player/${h.media_id}?t=${h.position}`) },
    ],
    [h.media_id, h.position, nav]
  );

  return (
    <div
      className={`${styles.thumb169} ${selected ? styles.thumb169Selected : ""}`}
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
        {posterFailed ? (
          <div className={styles.posterEmptyMovieSolid} aria-hidden />
        ) : (
          <img
            className={styles.thumb169Cover}
            src={thumbSrc}
            alt=""
            loading="lazy"
            decoding="async"
            onError={() => setPosterFailed(true)}
          />
        )}
        <div
          className={styles.thumb169Overlay}
          onClick={(e) => {
            e.stopPropagation();
            nav(`/detail/${h.media_id}`);
          }}
          role="presentation"
        >
          <button
            type="button"
            className={`${styles.posterOverlayIconBtn} ${styles.posterOverlaySelect}`}
            aria-label={selected ? "取消选中" : "选中"}
            onClick={(e) => {
              e.stopPropagation();
              setSelected((s) => !s);
            }}
          >
            {selected ? <CheckOutlined /> : null}
          </button>
          <button
            type="button"
            className={styles.posterPlayFab}
            aria-label={`播放 ${h.title || "影片"}`}
            onClick={(e) => {
              e.stopPropagation();
              nav(`/player/${h.media_id}?t=${h.position}`);
            }}
          >
            <CaretRightOutlined />
          </button>
          <button
            type="button"
            className={`${styles.posterOverlayIconBtn} ${styles.posterOverlayEdit}`}
            aria-label="编辑"
            onClick={(e) => {
              e.stopPropagation();
              nav(`/detail/${h.media_id}`);
            }}
          >
            <EditOutlined />
          </button>
          <Dropdown menu={{ items: moreItems }} trigger={["click"]} placement="bottomRight">
            <button
              type="button"
              className={`${styles.posterOverlayIconBtn} ${styles.posterOverlayMore}`}
              aria-label="更多"
              onClick={(e) => e.stopPropagation()}
            >
              <EllipsisOutlined style={{ transform: "rotate(90deg)" }} />
            </button>
          </Dropdown>
        </div>
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
}

/** 首页「最近添加」分区：与后台媒体库 type 对应；动漫归入「其他影片」横版预览。 */
const RECENT_SECTIONS: { key: string; title: string; libTypes: string[]; landscape: boolean }[] = [
  { key: "movie", title: "电影", libTypes: ["movie"], landscape: false },
  { key: "tv", title: "电视节目", libTypes: ["tv"], landscape: false },
  { key: "music", title: "音乐", libTypes: ["music"], landscape: false },
  { key: "photo", title: "图片", libTypes: ["photo"], landscape: false },
  { key: "document", title: "文档", libTypes: ["document"], landscape: false },
  { key: "other_video", title: "其他影片", libTypes: ["video", "anime"], landscape: true },
];

function RecentMovieShelfCard({
  m,
  nav,
  selected,
  onToggleSelect,
}: {
  m: MediaItem;
  nav: ReturnType<typeof useNavigate>;
  selected: boolean;
  onToggleSelect: () => void;
}) {
  const [posterFailed, setPosterFailed] = useState(false);
  const year = mediaReleaseYear(m);
  const moreItems: MenuProps["items"] = [
    { key: "detail", label: "查看详情", onClick: () => nav(`/detail/${m.id}`) },
    { key: "play", label: "立即播放", onClick: () => nav(`/player/${m.id}`) },
  ];

  return (
    <div
      className={`${styles.thumbPoster} ${styles.thumbPosterMovie} ${selected ? styles.thumbPosterMovieSelected : ""}`}
      tabIndex={0}
      onKeyDown={(e) => {
        if (e.key === "Enter" || e.key === " ") {
          e.preventDefault();
          nav(`/detail/${m.id}`);
        }
      }}
    >
      <div
        className={`${styles.posterBox} ${styles.posterBoxMovie}`}
        role="presentation"
        onClick={() => nav(`/detail/${m.id}`)}
      >
        <>
          {posterFailed ? (
            <div className={styles.posterEmptyMovieSolid} aria-hidden />
          ) : (
            <img
              className={styles.posterImgMovie}
              src={mediaPosterSrc(m)}
              alt=""
              loading="lazy"
              decoding="async"
              onError={() => setPosterFailed(true)}
            />
          )}
          <div
            className={styles.posterOverlay}
            onClick={(e) => {
              e.stopPropagation();
              nav(`/detail/${m.id}`);
            }}
            role="presentation"
          >
            <button
              type="button"
              className={`${styles.posterOverlayIconBtn} ${styles.posterOverlaySelect}`}
              aria-label={selected ? "取消选中" : "选中"}
              onClick={(e) => {
                e.stopPropagation();
                onToggleSelect();
              }}
            >
              {selected ? <CheckOutlined /> : null}
            </button>
            <button
              type="button"
              className={styles.posterPlayFab}
              aria-label={`播放 ${m.title || "影片"}`}
              onClick={(e) => {
                e.stopPropagation();
                nav(`/player/${m.id}`);
              }}
            >
              <CaretRightOutlined />
            </button>
            <button
              type="button"
              className={`${styles.posterOverlayIconBtn} ${styles.posterOverlayEdit}`}
              aria-label="编辑"
              onClick={(e) => {
                e.stopPropagation();
                nav(`/detail/${m.id}`);
              }}
            >
              <EditOutlined />
            </button>
            <Dropdown menu={{ items: moreItems }} trigger={["click"]} placement="bottomRight">
              <button
                type="button"
                className={`${styles.posterOverlayIconBtn} ${styles.posterOverlayMore}`}
                aria-label="更多"
                onClick={(e) => e.stopPropagation()}
              >
                <EllipsisOutlined style={{ transform: "rotate(90deg)" }} />
              </button>
            </Dropdown>
          </div>
        </>
      </div>
      <div
        className={styles.posterCapMovie}
        role="presentation"
        onClick={(e) => {
          e.stopPropagation();
          nav(`/detail/${m.id}`);
        }}
      >
        <div className={styles.posterTitleOneLine}>{m.title || "未命名"}</div>
        {year ? <div className={styles.posterYearLine}>{year}</div> : null}
      </div>
    </div>
  );
}

function RecentAddedRow({
  sectionTitle,
  items,
  landscape,
  sectionKey,
  nav,
}: {
  sectionTitle: string;
  items: MediaItem[];
  landscape: boolean;
  sectionKey: string;
  nav: ReturnType<typeof useNavigate>;
}) {
  const scrollRef = useRef<HTMLDivElement | null>(null);
  const [showLeft, setShowLeft] = useState(false);
  const [showRight, setShowRight] = useState(false);
  const [selectedMovieIds, setSelectedMovieIds] = useState<Set<number>>(() => new Set());

  const toggleMovieSelect = (id: number) => {
    setSelectedMovieIds((prev) => {
      const next = new Set(prev);
      if (next.has(id)) next.delete(id);
      else next.add(id);
      return next;
    });
  };

  const updateArrows = () => {
    const el = scrollRef.current;
    if (!el) {
      setShowLeft(false);
      setShowRight(false);
      return;
    }
    const maxLeft = el.scrollWidth - el.clientWidth;
    setShowLeft(el.scrollLeft > 4);
    setShowRight(maxLeft > 4 && el.scrollLeft < maxLeft - 4);
  };

  const scrollBy = (delta: number) => {
    const el = scrollRef.current;
    if (!el) return;
    el.scrollBy({ left: delta, behavior: "smooth" });
  };

  useEffect(() => {
    updateArrows();
    const onResize = () => updateArrows();
    window.addEventListener("resize", onResize);
    return () => window.removeEventListener("resize", onResize);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [items.length]);

  return (
    <>
      <div className={styles.sectionHead}>
        <Title level={3} className={styles.title}>
          {sectionTitle}
        </Title>
        {showLeft || showRight ? (
          <div className={styles.historyHeadControls}>
            {showLeft ? (
              <button
                type="button"
                className={`${styles.carouselArrow} ${styles.carouselArrowInline}`}
                onClick={() => scrollBy(-340)}
                aria-label="向左滚动"
              >
                <LeftOutlined />
              </button>
            ) : null}
            {showRight ? (
              <button
                type="button"
                className={`${styles.carouselArrow} ${styles.carouselArrowInline}`}
                onClick={() => scrollBy(340)}
                aria-label="向右滚动"
              >
                <RightOutlined />
              </button>
            ) : null}
          </div>
        ) : null}
      </div>
      <div className={styles.carouselWrap}>
        <div
          ref={scrollRef}
          className={`${styles.rowScroll} ${styles.rowScrollNoBar}`}
          onScroll={updateArrows}
        >
        {items.map((m) =>
          !landscape && sectionKey === "movie" ? (
            <RecentMovieShelfCard
              key={m.id}
              m={m}
              nav={nav}
              selected={selectedMovieIds.has(m.id)}
              onToggleSelect={() => toggleMovieSelect(m.id)}
            />
          ) : (
            <div
              key={m.id}
              className={landscape ? `${styles.thumbPoster} ${styles.thumbPosterLandscape}` : styles.thumbPoster}
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
              <div className={landscape ? `${styles.posterBox} ${styles.posterBoxLandscape}` : styles.posterBox}>
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
                className={landscape ? `${styles.posterPlayBtn} ${styles.posterPlayBtnLandscape}` : styles.posterPlayBtn}
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
          )
        )}
        </div>
      </div>
    </>
  );
}

export default function HomePage() {
  const nav = useNavigate();
  const [loading, setLoading] = useState(true);
  const [libs, setLibs] = useState<Library[]>([]);
  const [history, setHistory] = useState<HistoryItem[]>([]);
  const [allRecent, setAllRecent] = useState<MediaItem[]>([]);
  const historyScrollRef = useRef<HTMLDivElement | null>(null);
  const [showHistoryLeft, setShowHistoryLeft] = useState(false);
  const [showHistoryRight, setShowHistoryRight] = useState(false);

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

  useEffect(() => {
    let cancelled = false;
    setLoading(true);
    // 分项处理：任一接口失败不应清空其它接口的成功数据（Promise.all 会全失败）
    void (async () => {
      const [libR, histR, mediaR] = await Promise.allSettled([
        fetchLibraries(),
        fetchUserHistory(24),
        fetchMedia(undefined, { sort: "created_desc", limit: 400 }),
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
        setAllRecent(Array.isArray(mediaR.value) ? mediaR.value : []);
      } else {
        setAllRecent([]);
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

  const libraryTypeById = useMemo(() => {
    const m = new Map<number, string>();
    for (const lib of libs) {
      m.set(lib.id, lib.type);
    }
    return m;
  }, [libs]);

  /** Prefer poster_url from recent list when the same media appears in「继续观看」. */
  const recentPosterById = useMemo(() => {
    const m = new Map<number, string>();
    for (const r of allRecent) {
      const u = (r.poster_url || "").trim();
      if (u) m.set(r.id, u);
    }
    return m;
  }, [allRecent]);

  const recentBySection = useMemo(() => {
    const out = new Map<string, MediaItem[]>();
    for (const sec of RECENT_SECTIONS) {
      out.set(sec.key, []);
    }
    for (const item of allRecent) {
      const t = libraryTypeById.get(item.library_id);
      if (!t) continue;
      for (const sec of RECENT_SECTIONS) {
        if (sec.libTypes.includes(t)) {
          const arr = out.get(sec.key)!;
          if (arr.length < 24) arr.push(item);
          break;
        }
      }
    }
    return out;
  }, [allRecent, libraryTypeById]);

  useEffect(() => {
    updateHistoryArrows();
    const onResize = () => {
      updateHistoryArrows();
    };
    window.addEventListener("resize", onResize);
    return () => window.removeEventListener("resize", onResize);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [history.length]);

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
                            : lib.type === "video"
                              ? "其他影片"
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
          {history.length > 0 && (showHistoryLeft || showHistoryRight) ? (
            <div className={styles.historyHeadControls}>
              {showHistoryLeft ? (
                <button
                  type="button"
                  className={`${styles.carouselArrow} ${styles.carouselArrowInline}`}
                  onClick={() => scrollHistoryBy(-340)}
                  aria-label="向左滚动"
                >
                  <LeftOutlined />
                </button>
              ) : null}
              {showHistoryRight ? (
                <button
                  type="button"
                  className={`${styles.carouselArrow} ${styles.carouselArrowInline}`}
                  onClick={() => scrollHistoryBy(340)}
                  aria-label="向右滚动"
                >
                  <RightOutlined />
                </button>
              ) : null}
            </div>
          ) : null}
        </div>
        {history.length === 0 ? (
          <div className={styles.emptyHint}>暂无播放记录，去「浏览媒体」中选一部开始观看吧。</div>
        ) : (
          <div className={styles.carouselWrap}>
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
                  <HistoryContinueCard
                    key={`${h.file_id}-${h.update_at}`}
                    h={h}
                    nav={nav}
                    thumbSrc={thumbSrc}
                    pct={pct}
                  />
                );
              })}
            </div>
          </div>
        )}
      </section>

      {RECENT_SECTIONS.filter((sec) => (recentBySection.get(sec.key) ?? []).length > 0).map((sec) => {
        const items = recentBySection.get(sec.key) ?? [];
        return (
          <section key={sec.key} className={styles.section}>
            <RecentAddedRow
              sectionTitle={`最近添加的${sec.title}`}
              items={items}
              landscape={sec.landscape}
              sectionKey={sec.key}
              nav={nav}
            />
          </section>
        );
      })}
    </div>
  );
}
