import { Dropdown, Popover, Progress, Spin, Tag, Typography, message } from "antd";
import type { MenuProps } from "antd";
import {
  CaretRightOutlined,
  CheckCircleOutlined,
  CheckOutlined,
  CloseOutlined,
  EditOutlined,
  EllipsisOutlined,
  FileImageOutlined,
  LeftOutlined,
  MoreOutlined,
  PlayCircleOutlined,
  RightOutlined,
  UnorderedListOutlined,
} from "@ant-design/icons";
import { useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState } from "react";
import { Link, useNavigate } from "react-router-dom";
import {
  Library,
  MediaItem,
  HistoryItem,
  addPlaylistItem,
  fetchLibraries,
  fetchMedia,
  fetchUserHistory,
  mediaPosterSrc,
  removePlayProgress,
} from "../api/client";
import AddToPlaylistModal from "../components/AddToPlaylistModal";
import { buildMediaMenuItems } from "../components/mediaMenuItems";
import { readRecentPlaylists, rememberPlaylistAdded } from "../lib/recentPlaylists";
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

function historyRowKey(h: HistoryItem): string {
  return String(h.media_id);
}

/** 继续观看：悬停遮罩与角标逻辑对齐「最近添加的电影」；批量点选同详情页 */
function HistoryContinueCard({
  h,
  nav,
  thumbSrc,
  pct,
  selected,
  onToggleSelect,
  bulkSelectMode,
  buildHomeMediaMenu,
}: {
  h: HistoryItem;
  nav: ReturnType<typeof useNavigate>;
  thumbSrc: string;
  pct: number;
  selected: boolean;
  onToggleSelect: () => void;
  bulkSelectMode: boolean;
  buildHomeMediaMenu: (mediaId: number, extra?: { isWatched?: boolean; fromContinueWatching?: boolean }) => MenuProps;
}) {
  const [posterFailed, setPosterFailed] = useState(false);
  const homeMediaMenu = useMemo(
    () =>
      buildHomeMediaMenu(h.media_id, {
        isWatched: h.completed === 1,
        fromContinueWatching: true,
      }),
    [h.media_id, h.completed, buildHomeMediaMenu],
  );

  return (
    <div
      className={`${styles.thumb169} ${selected ? styles.thumb169Selected : ""} ${bulkSelectMode ? styles.thumb169Bulk : ""}`}
      role="button"
      tabIndex={0}
      onClick={(e) => {
        if (bulkSelectMode && (e.target as HTMLElement).closest(`.${styles.thumb169Box}`)) return;
        nav(`/detail/${h.media_id}`);
      }}
      onKeyDown={(e) => {
        if (e.key === "Enter" || e.key === " ") {
          e.preventDefault();
          if (bulkSelectMode) onToggleSelect();
          else nav(`/detail/${h.media_id}`);
        }
      }}
    >
      <div
        className={styles.thumb169Box}
        role="presentation"
        onClick={(e) => {
          if (bulkSelectMode) {
            if ((e.target as HTMLElement).closest("[data-home-history-action]")) return;
            onToggleSelect();
            return;
          }
          nav(`/detail/${h.media_id}`);
        }}
      >
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
            if (bulkSelectMode) onToggleSelect();
            else nav(`/detail/${h.media_id}`);
          }}
          role="presentation"
        >
          <button
            type="button"
            data-home-history-action
            className={`${styles.posterOverlayIconBtn} ${styles.posterOverlaySelect}`}
            aria-label={selected ? "取消选中" : "选中"}
            onClick={(e) => {
              e.stopPropagation();
              onToggleSelect();
            }}
          >
            {selected ? <CheckOutlined /> : null}
          </button>
          {bulkSelectMode ? null : (
            <>
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
              <Dropdown menu={homeMediaMenu} trigger={["click"]} placement="bottomRight">
                <button
                  type="button"
                  className={`${styles.posterOverlayIconBtn} ${styles.posterOverlayMore}`}
                  aria-label="更多"
                  onClick={(e) => e.stopPropagation()}
                >
                  <EllipsisOutlined style={{ transform: "rotate(90deg)" }} />
                </button>
              </Dropdown>
            </>
          )}
        </div>
      </div>
      <div className={styles.progressBar}>
        <div className={styles.progressFill} style={{ width: `${pct}%` }} />
      </div>
      <div
        className={styles.thumb169Cap}
        role="presentation"
        onClick={(e) => {
          e.stopPropagation();
          nav(`/detail/${h.media_id}`);
        }}
      >
        <div className={styles.thumb169Title}>{h.title || "未命名"}</div>
        <div className={styles.thumb169Sub}>
          {pct}% · {formatYear(h.file_path)}
        </div>
        <div className={styles.thumb169Tags}>
          {h.completed === 1 ? <Tag color="green" style={{ marginInlineEnd: 0, flexShrink: 0 }}>已看完</Tag> : null}
          <Tag color="blue" style={{ marginInlineEnd: 0, flexShrink: 0 }}>播放 {h.play_count ?? 0} 次</Tag>
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
  bulkSelectMode,
  buildHomeMediaMenu,
}: {
  m: MediaItem;
  nav: ReturnType<typeof useNavigate>;
  selected: boolean;
  onToggleSelect: () => void;
  /** 已有选中项时：海报区点选切换，隐藏播放/编辑/更多 */
  bulkSelectMode: boolean;
  buildHomeMediaMenu: (mediaId: number, extra?: { isWatched?: boolean; fromContinueWatching?: boolean }) => MenuProps;
}) {
  const [posterFailed, setPosterFailed] = useState(false);
  const year = mediaReleaseYear(m);
  const homeMediaMenu = useMemo(
    () => buildHomeMediaMenu(m.id),
    [m.id, buildHomeMediaMenu],
  );

  return (
    <div
      className={`${styles.thumbPoster} ${styles.thumbPosterMovie} ${selected ? styles.thumbPosterMovieSelected : ""} ${
        bulkSelectMode ? styles.thumbPosterMovieBulk : ""
      }`}
      role="button"
      tabIndex={0}
      onClick={(e) => {
        if (bulkSelectMode && (e.target as HTMLElement).closest(`.${styles.posterBoxMovie}`)) return;
        nav(`/detail/${m.id}`);
      }}
      onKeyDown={(e) => {
        if (e.key === "Enter" || e.key === " ") {
          e.preventDefault();
          if (bulkSelectMode) onToggleSelect();
          else nav(`/detail/${m.id}`);
        }
      }}
    >
      <div
        className={`${styles.posterBox} ${styles.posterBoxMovie}`}
        role="presentation"
        onClick={(e) => {
          if (bulkSelectMode) {
            if ((e.target as HTMLElement).closest("[data-home-shelf-action]")) return;
            onToggleSelect();
            return;
          }
          nav(`/detail/${m.id}`);
        }}
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
              if (bulkSelectMode) onToggleSelect();
              else nav(`/detail/${m.id}`);
            }}
            role="presentation"
          >
            <button
              type="button"
              data-home-shelf-action
              className={`${styles.posterOverlayIconBtn} ${styles.posterOverlaySelect}`}
              aria-label={selected ? "取消选中" : "选中"}
              onClick={(e) => {
                e.stopPropagation();
                onToggleSelect();
              }}
            >
              {selected ? <CheckOutlined /> : null}
            </button>
            {bulkSelectMode ? null : (
              <>
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
                <Dropdown menu={homeMediaMenu} trigger={["click"]} placement="bottomRight">
                  <button
                    type="button"
                    className={`${styles.posterOverlayIconBtn} ${styles.posterOverlayMore}`}
                    aria-label="更多"
                    onClick={(e) => e.stopPropagation()}
                  >
                    <EllipsisOutlined style={{ transform: "rotate(90deg)" }} />
                  </button>
                </Dropdown>
              </>
            )}
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
  movieSelectedIds,
  onToggleMovieSelect,
  homeBulkActive,
  buildHomeMediaMenu,
}: {
  sectionTitle: string;
  items: MediaItem[];
  landscape: boolean;
  sectionKey: string;
  nav: ReturnType<typeof useNavigate>;
  movieSelectedIds: Set<number>;
  onToggleMovieSelect: (id: number) => void;
  /** 继续观看或最近添加电影任一侧有选中 */
  homeBulkActive: boolean;
  buildHomeMediaMenu: (mediaId: number, extra?: { isWatched?: boolean; fromContinueWatching?: boolean }) => MenuProps;
}) {
  const scrollRef = useRef<HTMLDivElement | null>(null);
  const [showLeft, setShowLeft] = useState(false);
  const [showRight, setShowRight] = useState(false);

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
              selected={movieSelectedIds.has(m.id)}
              onToggleSelect={() => onToggleMovieSelect(m.id)}
              bulkSelectMode={homeBulkActive}
              buildHomeMediaMenu={buildHomeMediaMenu}
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
  const [historySelectedKeys, setHistorySelectedKeys] = useState<Set<string>>(() => new Set());
  const [movieSelectedIds, setMovieSelectedIds] = useState<Set<number>>(() => new Set());
  const historyScrollRef = useRef<HTMLDivElement | null>(null);
  const libScrollRef = useRef<HTMLDivElement | null>(null);
  const [showHistoryLeft, setShowHistoryLeft] = useState(false);
  const [showHistoryRight, setShowHistoryRight] = useState(false);
  const [showLibLeft, setShowLibLeft] = useState(false);
  const [showLibRight, setShowLibRight] = useState(false);

  const toggleHistoryRow = useCallback((key: string) => {
    setHistorySelectedKeys((prev) => {
      const next = new Set(prev);
      if (next.has(key)) next.delete(key);
      else next.add(key);
      return next;
    });
  }, []);

  useEffect(() => {
    const valid = new Set(history.map(historyRowKey));
    setHistorySelectedKeys((prev) => {
      const next = new Set<string>();
      for (const k of prev) {
        if (valid.has(k)) next.add(k);
      }
      if (next.size === prev.size && [...next].every((k) => prev.has(k))) return prev;
      return next;
    });
  }, [history]);

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

  const updateLibArrows = () => {
    const el = libScrollRef.current;
    if (!el) {
      setShowLibLeft(false);
      setShowLibRight(false);
      return;
    }
    const maxLeft = el.scrollWidth - el.clientWidth;
    setShowLibLeft(el.scrollLeft > 4);
    setShowLibRight(maxLeft > 4 && el.scrollLeft < maxLeft - 4);
  };

  const scrollLibsBy = (delta: number) => {
    const el = libScrollRef.current;
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

  const movieShelfItems = recentBySection.get("movie") ?? [];

  const [addToPlaylistMediaId, setAddToPlaylistMediaId] = useState<number | null>(null);
  const [recentPlaylistMenu, setRecentPlaylistMenu] = useState(readRecentPlaylists);

  const buildHomeMediaMenu = useCallback(
    (mediaId: number, menuExtra?: { isWatched?: boolean; fromContinueWatching?: boolean }) =>
      buildMediaMenuItems({ id: mediaId }, nav, {
        ...menuExtra,
        onAddToPlaylist: (mid) => setAddToPlaylistMediaId(mid),
        recentPlaylists: recentPlaylistMenu,
        onQuickAddToPlaylist: async (mid, pid) => {
          try {
            await addPlaylistItem(pid, mid);
            const name =
              recentPlaylistMenu.find((p) => p.id === pid)?.name ??
              readRecentPlaylists().find((p) => p.id === pid)?.name ??
              "播放列表";
            message.success(`已添加到「${name}」`);
            rememberPlaylistAdded({ id: pid, name });
            setRecentPlaylistMenu(readRecentPlaylists());
          } catch {
            message.error("添加失败，可能已在列表中");
          }
        },
        onRemoveFromContinueWatching: menuExtra?.fromContinueWatching
          ? async () => {
              await removePlayProgress(mediaId);
              setHistory((prev) => prev.filter((h) => h.media_id !== mediaId));
              setHistorySelectedKeys((sel) => {
                const next = new Set(sel);
                next.delete(String(mediaId));
                return next;
              });
              message.success("已从继续观看移除");
            }
          : undefined,
      }),
    [nav, recentPlaylistMenu],
  );

  const addToPlaylistDefaultTitle = useMemo(() => {
    if (addToPlaylistMediaId == null) return "";
    const h = history.find((x) => x.media_id === addToPlaylistMediaId);
    if ((h?.title ?? "").trim()) return (h!.title ?? "").trim();
    const m = allRecent.find((x) => x.id === addToPlaylistMediaId);
    return (m?.title ?? "").trim();
  }, [addToPlaylistMediaId, history, allRecent]);

  const homeBulkCount = historySelectedKeys.size + movieSelectedIds.size;
  const homeBulkActive = homeBulkCount > 0;

  const [homeBulkDock, setHomeBulkDock] = useState({ left: 0, width: 0 });
  const measureHomeBulkDock = useCallback(() => {
    const shell = document.querySelector(".app-main-centered");
    if (!shell) return;
    const r = shell.getBoundingClientRect();
    setHomeBulkDock({ left: r.left, width: r.width });
  }, []);

  const toggleMovieSelect = useCallback((id: number) => {
    setMovieSelectedIds((prev) => {
      const next = new Set(prev);
      if (next.has(id)) next.delete(id);
      else next.add(id);
      return next;
    });
  }, []);

  const clearHomeBulkSelection = useCallback(() => {
    setHistorySelectedKeys(new Set());
    setMovieSelectedIds(new Set());
  }, []);

  const selectAllHomeBulkOrClear = useCallback(() => {
    const hk = history.map(historyRowKey);
    const mids = movieShelfItems.map((m) => m.id);
    const allHist = hk.length === 0 || hk.every((k) => historySelectedKeys.has(k));
    const allMov = mids.length === 0 || mids.every((id) => movieSelectedIds.has(id));
    const anyList = hk.length + mids.length > 0;
    const everything = anyList && allHist && allMov;
    if (everything) {
      setHistorySelectedKeys(new Set());
      setMovieSelectedIds(new Set());
    } else {
      setHistorySelectedKeys(new Set(hk));
      setMovieSelectedIds(new Set(mids));
    }
  }, [history, historySelectedKeys, movieShelfItems, movieSelectedIds]);

  const firstBulkPlayTarget = useMemo(() => {
    for (const h of history) {
      if (historySelectedKeys.has(historyRowKey(h))) return { kind: "history" as const, h };
    }
    for (const m of movieShelfItems) {
      if (movieSelectedIds.has(m.id)) return { kind: "movie" as const, id: m.id };
    }
    return undefined;
  }, [history, historySelectedKeys, movieShelfItems, movieSelectedIds]);

  const firstBulkDetailMediaId = useMemo(() => {
    const p = firstBulkPlayTarget;
    if (!p) return undefined;
    return p.kind === "history" ? p.h.media_id : p.id;
  }, [firstBulkPlayTarget]);

  const homeBulkListContent = useMemo(() => {
    const histLines = history
      .filter((h) => historySelectedKeys.has(historyRowKey(h)))
      .map((h) => ({ key: historyRowKey(h), text: `继续观看 · ${h.title || "未命名"}` }));
    const movLines = movieShelfItems
      .filter((m) => movieSelectedIds.has(m.id))
      .map((m) => ({ key: `m-${m.id}`, text: `最近添加 · ${m.title || "未命名"}` }));
    const lines = [...histLines, ...movLines];
    if (lines.length === 0) return <span className={styles.homeShelfBulkPopoverEmpty}>无</span>;
    return (
      <ul className={styles.homeShelfBulkPopoverList}>
        {lines.map((row) => (
          <li key={row.key}>{row.text}</li>
        ))}
      </ul>
    );
  }, [history, historySelectedKeys, movieShelfItems, movieSelectedIds]);

  const homeBulkMoreItems: MenuProps["items"] = useMemo(() => {
    const p = firstBulkPlayTarget;
    const detailId = firstBulkDetailMediaId;
    return [
      {
        key: "play1",
        label: "播放第一个",
        onClick: () => {
          if (p == null) return;
          if (p.kind === "history") nav(`/player/${p.h.media_id}?t=${p.h.position}`);
          else nav(`/player/${p.id}`);
        },
      },
      {
        key: "detail1",
        label: "查看第一个详情",
        onClick: () => {
          if (detailId != null) nav(`/detail/${detailId}`);
        },
      },
    ];
  }, [firstBulkPlayTarget, firstBulkDetailMediaId, nav]);

  useLayoutEffect(() => {
    if (!homeBulkActive) return;
    measureHomeBulkDock();
    const shell = document.querySelector(".app-main-centered");
    if (!shell) return;
    const ro = new ResizeObserver(() => measureHomeBulkDock());
    ro.observe(shell);
    window.addEventListener("resize", measureHomeBulkDock);
    return () => {
      ro.disconnect();
      window.removeEventListener("resize", measureHomeBulkDock);
    };
  }, [homeBulkActive, measureHomeBulkDock]);

  useEffect(() => {
    const valid = new Set(movieShelfItems.map((m) => m.id));
    setMovieSelectedIds((prev) => {
      const next = new Set<number>();
      for (const id of prev) {
        if (valid.has(id)) next.add(id);
      }
      if (next.size === prev.size && [...next].every((id) => prev.has(id))) return prev;
      return next;
    });
  }, [movieShelfItems]);

  useEffect(() => {
    updateHistoryArrows();
    const onResize = () => {
      updateHistoryArrows();
    };
    window.addEventListener("resize", onResize);
    return () => window.removeEventListener("resize", onResize);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [history.length]);

  useEffect(() => {
    if (loading) return;
    const id = requestAnimationFrame(() => {
      updateLibArrows();
    });
    const onResize = () => {
      updateLibArrows();
    };
    window.addEventListener("resize", onResize);
    return () => {
      cancelAnimationFrame(id);
      window.removeEventListener("resize", onResize);
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [libs.length, loading]);

  const homeBulkAllFullySelected = useMemo(() => {
    const hk = history.map(historyRowKey);
    const mids = movieShelfItems.map((m) => m.id);
    if (hk.length + mids.length === 0) return false;
    return (
      (hk.length === 0 || hk.every((k) => historySelectedKeys.has(k))) &&
      (mids.length === 0 || mids.every((id) => movieSelectedIds.has(id)))
    );
  }, [history, historySelectedKeys, movieShelfItems, movieSelectedIds]);

  const homeBulkSelectAllDisabled = history.length === 0 && movieShelfItems.length === 0;

  if (loading) {
    return (
      <div className={styles.page} style={{ display: "flex", justifyContent: "center", paddingTop: 80 }}>
        <Spin size="large" />
      </div>
    );
  }

  return (
    <div className={styles.page}>
      {homeBulkActive ? (
        <div
          className={styles.homeShelfBulkBar}
          role="toolbar"
          aria-label="首页批量操作"
          style={{
            top: 72,
            zIndex: 1000,
            left: homeBulkDock.left,
            width: homeBulkDock.width,
            maxWidth: homeBulkDock.width,
            opacity: homeBulkDock.width > 0 ? 1 : 0,
            pointerEvents: homeBulkDock.width > 0 ? "auto" : "none",
          }}
        >
          <div className={styles.homeShelfBulkLeft}>
            <CheckOutlined className={styles.homeShelfBulkOrangeMark} aria-hidden />
            <span className={styles.homeShelfBulkOrangeText}>已选择 {homeBulkCount} 个项目</span>
          </div>
          <div className={styles.homeShelfBulkCenter}>
            <button
              type="button"
              className={styles.homeShelfBulkIconBtn}
              aria-label="播放第一个"
              disabled={firstBulkPlayTarget == null}
              onClick={() => {
                const p = firstBulkPlayTarget;
                if (p == null) return;
                if (p.kind === "history") nav(`/player/${p.h.media_id}?t=${p.h.position}`);
                else nav(`/player/${p.id}`);
              }}
            >
              <PlayCircleOutlined />
            </button>
            <button
              type="button"
              className={styles.homeShelfBulkIconBtn}
              aria-label={homeBulkAllFullySelected ? "取消全选继续观看与最近电影" : "全选继续观看与最近电影"}
              disabled={homeBulkSelectAllDisabled}
              onClick={selectAllHomeBulkOrClear}
            >
              <CheckCircleOutlined />
            </button>
            <Popover content={homeBulkListContent} trigger="click" placement="bottom">
              <button type="button" className={styles.homeShelfBulkIconBtn} aria-label="已选列表">
                <UnorderedListOutlined />
              </button>
            </Popover>
            <Dropdown menu={{ items: homeBulkMoreItems }} trigger={["click"]} placement="bottomRight">
              <button type="button" className={styles.homeShelfBulkIconBtn} aria-label="更多">
                <MoreOutlined />
              </button>
            </Dropdown>
          </div>
          <button type="button" className={styles.homeShelfBulkCancel} onClick={clearHomeBulkSelection}>
            <CloseOutlined aria-hidden />
            <span>取消全选</span>
          </button>
        </div>
      ) : null}
      <section className={styles.section}>
        <div className={styles.sectionHead}>
          <Title level={3} className={styles.title}>
            媒体库
          </Title>
          {libs.length > 0 && (showLibLeft || showLibRight) ? (
            <div className={styles.historyHeadControls}>
              {showLibLeft ? (
                <button
                  type="button"
                  className={`${styles.carouselArrow} ${styles.carouselArrowInline}`}
                  onClick={() => scrollLibsBy(-340)}
                  aria-label="向左滚动"
                >
                  <LeftOutlined />
                </button>
              ) : null}
              {showLibRight ? (
                <button
                  type="button"
                  className={`${styles.carouselArrow} ${styles.carouselArrowInline}`}
                  onClick={() => scrollLibsBy(340)}
                  aria-label="向右滚动"
                >
                  <RightOutlined />
                </button>
              ) : null}
            </div>
          ) : null}
        </div>
        {libs.length === 0 ? (
          <div className={styles.emptyHint}>暂无媒体库，请由管理员在「媒体库」中添加。</div>
        ) : (
          <div className={styles.carouselWrap}>
            <div
              ref={libScrollRef}
              className={`${styles.rowScroll} ${styles.rowScrollNoBar}`}
              onScroll={updateLibArrows}
            >
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
                const rowKey = historyRowKey(h);
                return (
                  <HistoryContinueCard
                    key={rowKey}
                    h={h}
                    nav={nav}
                    thumbSrc={thumbSrc}
                    pct={pct}
                    selected={historySelectedKeys.has(rowKey)}
                    onToggleSelect={() => toggleHistoryRow(rowKey)}
                    bulkSelectMode={homeBulkActive}
                    buildHomeMediaMenu={buildHomeMediaMenu}
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
              movieSelectedIds={movieSelectedIds}
              onToggleMovieSelect={toggleMovieSelect}
              homeBulkActive={homeBulkActive}
              buildHomeMediaMenu={buildHomeMediaMenu}
            />
          </section>
        );
      })}
      {addToPlaylistMediaId != null && (
        <AddToPlaylistModal
          mediaIds={[addToPlaylistMediaId]}
          open
          defaultNewPlaylistName={addToPlaylistDefaultTitle}
          onClose={() => setAddToPlaylistMediaId(null)}
          onAdded={(pl) => {
            rememberPlaylistAdded(pl);
            setRecentPlaylistMenu(readRecentPlaylists());
          }}
        />
      )}
    </div>
  );
}
