import { Dropdown, Modal, Popover, Progress, Spin, Tag, Typography, message } from "antd";
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
  addFavoriteFolderItem,
  addPlaylistItem,
  createScrapeTasks,
  deleteMedia,
  extractAudioTrack,
  extractKeyframes,
  fetchLibraries,
  fetchUserHistory,
  markWatched,
  mediaLandscapeThumbSrc,
  mediaPosterSrc,
  musicMediaPosterSrc,
  recognizeMediaSubtitles,
  removePlayProgress,
  transcodeAsync,
} from "../api/client";
import { mediaItemsToMusicQueue } from "../lib/albumPlayback";
import { useMusicPlayerStore } from "../store/musicPlayer";
import AddToFavoriteFolderPickerModal from "../components/AddToFavoriteFolderPickerModal";
import AddToPlaylistModal from "../components/AddToPlaylistModal";
import MusicPosterPlaceholderIcon from "../components/MusicPosterPlaceholderIcon";
import PhotoLightbox from "../components/PhotoLightbox";
import { buildMediaMenuItems } from "../components/mediaMenuItems";
import {
  buildHomeRecentSections,
  CONTINUE_WATCHING_LIBRARY_TYPES,
  flattenHomeRecent,
  loadHomeRecentBySection,
} from "../lib/homeRecentSections";
import { useFavoriteFolderMenuRecents } from "../lib/useFavoriteFolderMenuRecents";
import { readRecentPlaylists, rememberPlaylistAdded } from "../lib/recentPlaylists";
import { useT, type TranslateFn } from "../i18n";
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
  t,
}: {
  h: HistoryItem;
  nav: ReturnType<typeof useNavigate>;
  thumbSrc: string;
  pct: number;
  selected: boolean;
  onToggleSelect: () => void;
  bulkSelectMode: boolean;
  buildHomeMediaMenu: (mediaId: number, extra?: { isWatched?: boolean; fromContinueWatching?: boolean }) => MenuProps;
  t: TranslateFn;
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
            aria-label={selected ? t("pages.home.aria_deselect") : t("pages.home.aria_select")}
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
                aria-label={t("pages.home.aria_play_video", { title: h.title || t("pages.home.movie_fallback") })}
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
                aria-label={t("pages.home.aria_edit")}
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
                  aria-label={t("pages.home.aria_more")}
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
        <div className={styles.thumb169Title}>{h.title || t("pages.home.untitled")}</div>
        <div className={styles.thumb169Sub}>
          {pct}% · {formatYear(h.file_path)}
        </div>
        <div className={styles.thumb169Tags}>
          {h.completed === 1 ? <Tag color="green" style={{ marginInlineEnd: 0, flexShrink: 0 }}>{t("pages.home.completed")}</Tag> : null}
          <Tag color="blue" style={{ marginInlineEnd: 0, flexShrink: 0 }}>{t("pages.home.play_times", { count: h.play_count ?? 0 })}</Tag>
        </div>
      </div>
    </div>
  );
}

function RecentShelfCard({
  m,
  nav,
  selected,
  onToggleSelect,
  bulkSelectMode,
  buildHomeMediaMenu,
  variant,
  layout = "portrait",
  shelfItems,
  shelfIndex,
  t,
}: {
  m: MediaItem;
  nav: ReturnType<typeof useNavigate>;
  selected: boolean;
  onToggleSelect: () => void;
  bulkSelectMode: boolean;
  buildHomeMediaMenu: (mediaId: number, extra?: { isWatched?: boolean; fromContinueWatching?: boolean }) => MenuProps;
  variant: "movie" | "music" | "video";
  layout?: "portrait" | "landscape";
  shelfItems: MediaItem[];
  shelfIndex: number;
  t: TranslateFn;
}) {
  const isLandscape = layout === "landscape";
  const [posterFailed, setPosterFailed] = useState(false);
  const [useLandscapePosterFallback, setUseLandscapePosterFallback] = useState(false);
  const year = variant === "movie" ? mediaReleaseYear(m) : "";
  const landscapePrimary = mediaLandscapeThumbSrc(m);
  const landscapeFallback = mediaPosterSrc(m);
  const posterSrc =
    variant === "music"
      ? musicMediaPosterSrc(m)
      : isLandscape
        ? (useLandscapePosterFallback ? landscapeFallback : landscapePrimary) || landscapeFallback
        : mediaPosterSrc(m);
  const showPosterImg = Boolean(posterSrc) && !posterFailed;
  const homeMediaMenu = useMemo(
    () => buildHomeMediaMenu(m.id),
    [m.id, buildHomeMediaMenu],
  );
  const showBulkSelect = variant === "movie" && bulkSelectMode;
  const showSelectButton = variant === "movie";

  const playShelfItem = () => {
    if (variant === "music") {
      const queue = mediaItemsToMusicQueue(shelfItems);
      if (queue.length === 0) return;
      useMusicPlayerStore.getState().playQueue(queue, Math.min(shelfIndex, queue.length - 1));
      return;
    }
    nav(`/player/${m.id}`);
  };

  const playAria =
    variant === "music"
      ? t("pages.music.aria_play_track", { title: m.title || t("pages.home.untitled") })
      : t("pages.home.aria_play_video", { title: m.title || t("pages.home.movie_fallback") });

  return (
    <div
      className={`${styles.thumbPoster} ${styles.thumbPosterMovie} ${isLandscape ? styles.thumbPosterMovieLandscape : ""} ${selected ? styles.thumbPosterMovieSelected : ""} ${
        showBulkSelect ? styles.thumbPosterMovieBulk : ""
      }`}
      role="button"
      tabIndex={0}
      onClick={(e) => {
        if (showBulkSelect && (e.target as HTMLElement).closest(`.${styles.posterBoxMovie}`)) return;
        nav(`/detail/${m.id}`);
      }}
      onKeyDown={(e) => {
        if (e.key === "Enter" || e.key === " ") {
          e.preventDefault();
          if (showBulkSelect) onToggleSelect();
          else nav(`/detail/${m.id}`);
        }
      }}
    >
      <div
        className={`${styles.posterBox} ${styles.posterBoxMovie} ${isLandscape ? styles.posterBoxMovieLandscape : ""} ${variant === "music" ? styles.posterBoxMovieMusic : ""}`}
        role="presentation"
        onClick={(e) => {
          if (showBulkSelect) {
            if ((e.target as HTMLElement).closest("[data-home-shelf-action]")) return;
            onToggleSelect();
            return;
          }
          nav(`/detail/${m.id}`);
        }}
      >
        <>
          {variant === "music" ? (
            <div className={styles.posterMusicPlaceholderIcon} aria-hidden>
              <MusicPosterPlaceholderIcon />
            </div>
          ) : null}
          {showPosterImg ? (
            <img
              className={`${styles.posterImgMovie} ${isLandscape || variant === "music" ? styles.posterImgMovieCover : ""}`}
              src={posterSrc!}
              alt=""
              loading="lazy"
              decoding="async"
              onError={() => {
                if (
                  isLandscape &&
                  landscapeFallback &&
                  !useLandscapePosterFallback &&
                  landscapePrimary &&
                  landscapePrimary !== landscapeFallback
                ) {
                  setUseLandscapePosterFallback(true);
                  return;
                }
                setPosterFailed(true);
              }}
            />
          ) : variant !== "music" ? (
            <div className={styles.posterEmptyMovieSolid} aria-hidden />
          ) : null}
          <div
            className={styles.posterOverlay}
            onClick={(e) => {
              e.stopPropagation();
              if (showBulkSelect) onToggleSelect();
              else nav(`/detail/${m.id}`);
            }}
            role="presentation"
          >
            {showSelectButton ? (
              <button
                type="button"
                data-home-shelf-action
                className={`${styles.posterOverlayIconBtn} ${styles.posterOverlaySelect}`}
                aria-label={selected ? t("pages.home.aria_deselect") : t("pages.home.aria_select")}
                onClick={(e) => {
                  e.stopPropagation();
                  onToggleSelect();
                }}
              >
                {selected ? <CheckOutlined /> : null}
              </button>
            ) : null}
            {!showBulkSelect ? (
              <>
                <button
                  type="button"
                  className={styles.posterPlayFab}
                  aria-label={playAria}
                  onClick={(e) => {
                    e.stopPropagation();
                    playShelfItem();
                  }}
                >
                  <CaretRightOutlined />
                </button>
                <button
                  type="button"
                  className={`${styles.posterOverlayIconBtn} ${styles.posterOverlayEdit}`}
                  aria-label={t("pages.home.aria_edit")}
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
                    aria-label={t("pages.home.aria_more")}
                    onClick={(e) => e.stopPropagation()}
                  >
                    <EllipsisOutlined style={{ transform: "rotate(90deg)" }} />
                  </button>
                </Dropdown>
              </>
            ) : null}
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
        <div className={styles.posterTitleOneLine}>{m.title || t("pages.home.untitled")}</div>
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
  onOpenPhotoPreview,
  t,
}: {
  sectionTitle: string;
  items: MediaItem[];
  landscape: boolean;
  sectionKey: string;
  nav: ReturnType<typeof useNavigate>;
  movieSelectedIds: Set<number>;
  onToggleMovieSelect: (id: number) => void;
  homeBulkActive: boolean;
  buildHomeMediaMenu: (mediaId: number, extra?: { isWatched?: boolean; fromContinueWatching?: boolean }) => MenuProps;
  onOpenPhotoPreview?: (items: MediaItem[], index: number) => void;
  t: TranslateFn;
}) {
  const openRecentItem = (m: MediaItem, index: number) => {
    if (sectionKey === "photo") {
      onOpenPhotoPreview?.(items, index);
      return;
    }
    if (sectionKey === "document") {
      nav(`/reader/${m.id}`);
      return;
    }
    nav(`/detail/${m.id}`);
  };

  const showPlayButton = sectionKey !== "photo" && sectionKey !== "document";
  const useShelfCard =
    sectionKey === "movie" ||
    sectionKey === "music" ||
    sectionKey === "tv" ||
    sectionKey === "anime" ||
    sectionKey === "other_video";
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
                aria-label={t("pages.home.scroll_left")}
              >
                <LeftOutlined />
              </button>
            ) : null}
            {showRight ? (
              <button
                type="button"
                className={`${styles.carouselArrow} ${styles.carouselArrowInline}`}
                onClick={() => scrollBy(340)}
                aria-label={t("pages.home.scroll_right")}
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
        {items.map((m, index) =>
          useShelfCard ? (
            <RecentShelfCard
              key={m.id}
              m={m}
              nav={nav}
              selected={sectionKey === "movie" && movieSelectedIds.has(m.id)}
              onToggleSelect={() => onToggleMovieSelect(m.id)}
              bulkSelectMode={sectionKey === "movie" && homeBulkActive}
              buildHomeMediaMenu={buildHomeMediaMenu}
              variant={sectionKey === "music" ? "music" : sectionKey === "movie" ? "movie" : "video"}
              layout={landscape ? "landscape" : "portrait"}
              shelfItems={items}
              shelfIndex={index}
              t={t}
            />
          ) : (
            <div
              key={m.id}
              className={landscape ? `${styles.thumbPoster} ${styles.thumbPosterLandscape}` : styles.thumbPoster}
              role="button"
              tabIndex={0}
              onClick={() => openRecentItem(m, index)}
              onKeyDown={(e) => {
                if (e.key === "Enter" || e.key === " ") {
                  e.preventDefault();
                  openRecentItem(m, index);
                }
              }}
            >
              <div className={landscape ? `${styles.posterBox} ${styles.posterBoxLandscape}` : styles.posterBox}>
                <img
                  className={styles.posterFillImg}
                  src={landscape ? mediaLandscapeThumbSrc(m) : mediaPosterSrc(m)}
                  alt=""
                  loading="lazy"
                  decoding="async"
                  onError={(e) => {
                    const img = e.currentTarget;
                    if (landscape) {
                      const fallback = mediaPosterSrc(m);
                      if (fallback && img.src !== fallback && !img.dataset.fallbackTried) {
                        img.dataset.fallbackTried = "1";
                        img.style.display = "";
                        img.src = fallback;
                        return;
                      }
                    }
                    img.style.display = "none";
                  }}
                />
                <div className={styles.posterEmpty}>
                  <FileImageOutlined />
                  <span>{t("pages.home.no_poster")}</span>
                </div>
              </div>
              {showPlayButton ? (
                <button
                  type="button"
                  className={landscape ? `${styles.posterPlayBtn} ${styles.posterPlayBtnLandscape}` : styles.posterPlayBtn}
                  onClick={(e) => {
                    e.stopPropagation();
                    nav(`/player/${m.id}`);
                  }}
                  aria-label={t("pages.home.aria_play_video", { title: m.title || t("pages.home.movie_fallback") })}
                >
                  <PlayCircleOutlined />
                </button>
              ) : null}
              <div className={styles.posterCap}>
                <div className={styles.posterTitle}>{m.title || t("pages.home.untitled")}</div>
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
  const t = useT();
  const RECENT_SECTIONS = useMemo(() => buildHomeRecentSections(t), [t]);
  const [loading, setLoading] = useState(true);
  const [libs, setLibs] = useState<Library[]>([]);
  const [history, setHistory] = useState<HistoryItem[]>([]);
  const [recentBySection, setRecentBySection] = useState<Map<string, MediaItem[]>>(() => new Map());
  const [photoLightbox, setPhotoLightbox] = useState<{ items: MediaItem[]; index: number } | null>(null);
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
      const [libR, histR] = await Promise.allSettled([
        fetchLibraries(),
        fetchUserHistory(24, { libraryTypes: CONTINUE_WATCHING_LIBRARY_TYPES }),
      ]);
      if (cancelled) return;
      const libsData = libR.status === "fulfilled" && Array.isArray(libR.value) ? libR.value : [];
      setLibs(libsData);
      if (histR.status === "fulfilled") {
        setHistory(histR.value.filter((h) => h.media_id > 0));
      } else {
        setHistory([]);
      }
      const sections = buildHomeRecentSections(t);
      const recentMap = libsData.length
        ? await loadHomeRecentBySection(libsData, sections)
        : new Map<string, MediaItem[]>();
      if (!cancelled) {
        setRecentBySection(recentMap);
        setLoading(false);
      }
    })();
    return () => {
      cancelled = true;
    };
  }, [t]);

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

  useEffect(() => {
    if (libs.length === 0) {
      setRecentBySection(new Map());
      return;
    }
    let cancelled = false;
    const refreshRecent = () => {
      void loadHomeRecentBySection(libs, RECENT_SECTIONS).then((map) => {
        if (!cancelled) setRecentBySection(map);
      });
    };
    refreshRecent();
    const timer = window.setInterval(refreshRecent, 10000);
    return () => {
      cancelled = true;
      window.clearInterval(timer);
    };
  }, [libs, RECENT_SECTIONS]);

  const allRecent = useMemo(() => flattenHomeRecent(recentBySection), [recentBySection]);

  /** Prefer poster_url from recent list when the same media appears in「继续观看」. */
  const recentPosterById = useMemo(() => {
    const m = new Map<number, string>();
    for (const r of allRecent) {
      const u = (r.poster_url || "").trim();
      if (u) m.set(r.id, u);
    }
    return m;
  }, [allRecent]);

  const movieShelfItems = recentBySection.get("movie") ?? [];

  const openPhotoPreview = useCallback((items: MediaItem[], index: number) => {
    setPhotoLightbox({ items, index });
  }, []);

  const [addToPlaylistMediaId, setAddToPlaylistMediaId] = useState<number | null>(null);
  const [addToFavoriteFolderMediaId, setAddToFavoriteFolderMediaId] = useState<number | null>(null);
  const [recentPlaylistMenu, setRecentPlaylistMenu] = useState(readRecentPlaylists);
  const { recentFavoriteFolders, rememberFolderMenuAdded } = useFavoriteFolderMenuRecents();

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
              t("pages.home.playlist_fallback");
            message.success(t("pages.home.added_to_playlist", { name }));
            rememberPlaylistAdded({ id: pid, name });
            setRecentPlaylistMenu(readRecentPlaylists());
          } catch {
            message.error(t("pages.home.add_failed"));
          }
        },
        onAddToFavoriteFolder: (mid) => setAddToFavoriteFolderMediaId(mid),
        recentFavoriteFolders,
        onQuickAddToFavoriteFolder: async (mid, folderId) => {
          try {
            await addFavoriteFolderItem(folderId, mid);
            const name =
              recentFavoriteFolders.find((f) => f.id === folderId)?.name ??
              t("components.media_menu.favorite_folder_fallback");
            message.success(t("components.add_to_favorite_folder_picker_modal.added_single", { name }));
            rememberFolderMenuAdded({ id: folderId, name });
          } catch {
            message.error(t("components.add_to_favorite_folder_picker_modal.add_failed_dup"));
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
              message.success(t("pages.home.removed_from_continue"));
            }
          : undefined,
      }),
    [nav, recentPlaylistMenu, recentFavoriteFolders, rememberFolderMenuAdded, t],
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

  const refreshHomeAfterBulk = useCallback(async () => {
    const [histR, recentR] = await Promise.allSettled([
      fetchUserHistory(24, { libraryTypes: CONTINUE_WATCHING_LIBRARY_TYPES }),
      libs.length > 0 ? loadHomeRecentBySection(libs, RECENT_SECTIONS) : Promise.resolve(new Map<string, MediaItem[]>()),
    ]);
    if (histR.status === "fulfilled") {
      setHistory(histR.value.filter((h) => h.media_id > 0));
    }
    if (recentR.status === "fulfilled") {
      setRecentBySection(recentR.value);
    }
  }, [libs, RECENT_SECTIONS]);

  const homeBulkSelectedMediaIds = useMemo(() => {
    const ids = new Set<number>();
    for (const h of history) {
      if (historySelectedKeys.has(historyRowKey(h))) ids.add(h.media_id);
    }
    for (const m of movieShelfItems) {
      if (movieSelectedIds.has(m.id)) ids.add(m.id);
    }
    return [...ids];
  }, [history, historySelectedKeys, movieShelfItems, movieSelectedIds]);

  async function bulkMarkHomeSelectedWatched(ids: number[]) {
    if (ids.length === 0) return;
    let ok = 0;
    let fail = 0;
    for (const id of ids) {
      try {
        await markWatched(id);
        ok++;
      } catch {
        fail++;
      }
    }
    if (ok > 0) {
      message.success(
        fail > 0 ? t("pages.browse.analyze_with_skip", { ok, fail }) : t("components.media_menu.marked_watched"),
      );
      await refreshHomeAfterBulk();
    } else {
      message.error(t("components.media_menu.operation_failed"));
    }
  }

  async function bulkRemoveHomeSelectedFromContinue(ids: number[]) {
    if (ids.length === 0) return;
    let ok = 0;
    for (const id of ids) {
      try {
        await removePlayProgress(id);
        ok++;
      } catch {
        /* skip items without progress */
      }
    }
    if (ok > 0) {
      setHistorySelectedKeys((sel) => {
        const next = new Set(sel);
        for (const id of ids) next.delete(String(id));
        return next;
      });
      message.success(t("pages.home.removed_from_continue"));
      await refreshHomeAfterBulk();
    } else {
      message.warning(t("pages.browse.remove_continue_none"));
    }
  }

  async function bulkRefreshHomeSelectedMetadata(ids: number[]) {
    if (ids.length === 0) return;
    try {
      await createScrapeTasks(ids);
      message.success(t("components.media_menu.scrape_task_created"));
    } catch {
      message.error(t("components.media_menu.operation_failed"));
    }
  }

  async function bulkTranscodeHomeSelected(ids: number[], mode: "analyze" | "optimize") {
    if (ids.length === 0) return;
    let ok = 0;
    let fail = 0;
    for (const id of ids) {
      try {
        await transcodeAsync(id, mode);
        ok++;
      } catch {
        fail++;
      }
    }
    const successKey =
      mode === "analyze" ? "components.media_menu.analyze_task_created" : "components.media_menu.optimize_task_created";
    if (ok > 0) {
      message.success(
        fail > 0 ? t("pages.browse.analyze_with_skip", { ok, fail }) : t(successKey),
      );
    } else {
      message.error(t("components.media_menu.operation_failed"));
    }
  }

  async function bulkRecognizeHomeSelectedSubtitles(ids: number[]) {
    if (ids.length === 0) return;
    let ok = 0;
    let fail = 0;
    for (const id of ids) {
      try {
        await recognizeMediaSubtitles(id);
        ok++;
      } catch {
        fail++;
      }
    }
    if (ok > 0) {
      message.success(
        fail > 0 ? t("pages.browse.analyze_with_skip", { ok, fail }) : t("components.media_menu.subtitle_task_created"),
      );
    } else {
      message.error(t("components.media_menu.operation_failed"));
    }
  }

  async function bulkExtractHomeSelectedAudio(ids: number[]) {
    if (ids.length === 0) return;
    let ok = 0;
    let fail = 0;
    for (const id of ids) {
      try {
        await extractAudioTrack(id);
        ok++;
      } catch {
        fail++;
      }
    }
    if (ok > 0) {
      message.success(
        fail > 0 ? t("pages.browse.analyze_with_skip", { ok, fail }) : t("components.media_menu.atrack_task_created"),
      );
    } else {
      message.error(t("components.media_menu.operation_failed"));
    }
  }

  async function bulkExtractHomeSelectedKeyframes(ids: number[]) {
    if (ids.length === 0) return;
    let ok = 0;
    let fail = 0;
    for (const id of ids) {
      try {
        await extractKeyframes(id);
        ok++;
      } catch {
        fail++;
      }
    }
    if (ok > 0) {
      message.success(
        fail > 0 ? t("pages.browse.analyze_with_skip", { ok, fail }) : t("components.media_menu.keyframe_task_created"),
      );
    } else {
      message.error(t("components.media_menu.operation_failed"));
    }
  }

  function bulkDeleteHomeSelected(ids: number[]) {
    if (ids.length === 0) return;
    Modal.confirm({
      title: t("pages.browse.bulk_delete_title", { count: ids.length }),
      centered: true,
      okText: t("components.media_menu.ok"),
      cancelText: t("components.media_menu.cancel"),
      okButtonProps: { danger: true },
      content: t("pages.browse.bulk_delete_confirm"),
      onOk: async () => {
        let ok = 0;
        let fail = 0;
        for (const id of ids) {
          try {
            await deleteMedia(id);
            ok++;
          } catch {
            fail++;
          }
        }
        clearHomeBulkSelection();
        await refreshHomeAfterBulk();
        if (ok > 0) {
          message.success(
            fail > 0
              ? t("pages.browse.bulk_deleted_with_skip", { ok, fail })
              : t("pages.browse.bulk_deleted", { ok }),
          );
        } else {
          message.error(t("components.media_menu.delete_failed"));
        }
      },
    });
  }

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
      .map((h) => ({ key: historyRowKey(h), text: t("pages.home.continue_label", { title: h.title || t("pages.home.untitled") }) }));
    const movLines = movieShelfItems
      .filter((m) => movieSelectedIds.has(m.id))
      .map((m) => ({ key: `m-${m.id}`, text: t("pages.home.recent_label", { title: m.title || t("pages.home.untitled") }) }));
    const lines = [...histLines, ...movLines];
    if (lines.length === 0) return <span className={styles.homeShelfBulkPopoverEmpty}>{t("pages.home.empty_bulk_list")}</span>;
    return (
      <ul className={styles.homeShelfBulkPopoverList}>
        {lines.map((row) => (
          <li key={row.key}>{row.text}</li>
        ))}
      </ul>
    );
  }, [history, historySelectedKeys, movieShelfItems, movieSelectedIds]);

  const homeBulkMoreItems: MenuProps["items"] = useMemo(
    () => [
      { key: "play1", label: t("pages.home.menu_play_first") },
      { key: "detail1", label: t("pages.home.menu_view_first_detail") },
      { type: "divider" },
      { key: "markWatched", label: t("components.media_menu.watched_mark_as_watched") },
      { key: "removeFromContinue", label: t("components.media_menu.remove_from_continue") },
      { type: "divider" },
      { key: "refreshMetadata", label: t("components.media_menu.refresh_metadata") },
      { key: "analyze", label: t("components.media_menu.analyze") },
      { key: "optimize", label: t("components.media_menu.optimize") },
      { type: "divider" },
      { key: "recognizeSubtitles", label: t("components.media_menu.recognize_subtitles") },
      { key: "extractAudio", label: t("components.media_menu.extract_audio") },
      { key: "extractKeyframes", label: t("components.media_menu.extract_keyframes") },
      { type: "divider" },
      { key: "delete", label: t("components.media_menu.delete"), danger: true },
    ],
    [t],
  );

  function onHomeBulkMoreMenuClick(key: string) {
    const ids = homeBulkSelectedMediaIds;
    switch (key) {
      case "play1": {
        const p = firstBulkPlayTarget;
        if (p == null) return;
        if (p.kind === "history") nav(`/player/${p.h.media_id}?t=${p.h.position}`);
        else nav(`/player/${p.id}`);
        break;
      }
      case "detail1": {
        if (firstBulkDetailMediaId != null) nav(`/detail/${firstBulkDetailMediaId}`);
        break;
      }
      case "markWatched":
        void bulkMarkHomeSelectedWatched(ids);
        break;
      case "removeFromContinue":
        void bulkRemoveHomeSelectedFromContinue(ids);
        break;
      case "refreshMetadata":
        void bulkRefreshHomeSelectedMetadata(ids);
        break;
      case "analyze":
        void bulkTranscodeHomeSelected(ids, "analyze");
        break;
      case "optimize":
        void bulkTranscodeHomeSelected(ids, "optimize");
        break;
      case "recognizeSubtitles":
        void bulkRecognizeHomeSelectedSubtitles(ids);
        break;
      case "extractAudio":
        void bulkExtractHomeSelectedAudio(ids);
        break;
      case "extractKeyframes":
        void bulkExtractHomeSelectedKeyframes(ids);
        break;
      case "delete":
        bulkDeleteHomeSelected(ids);
        break;
      default:
        break;
    }
  }

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
    const musicShelf = recentBySection.get("music") ?? [];
    const valid = new Set([...movieShelfItems, ...musicShelf].map((m) => m.id));
    setMovieSelectedIds((prev) => {
      const next = new Set<number>();
      for (const id of prev) {
        if (valid.has(id)) next.add(id);
      }
      if (next.size === prev.size && [...next].every((id) => prev.has(id))) return prev;
      return next;
    });
  }, [movieShelfItems, recentBySection]);

  useLayoutEffect(() => {
    if (loading || history.length === 0) {
      setShowHistoryLeft(false);
      setShowHistoryRight(false);
      return;
    }
    updateHistoryArrows();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [history.length, loading]);

  useEffect(() => {
    if (loading || history.length === 0) return;
    const el = historyScrollRef.current;
    if (!el) return;

    const run = () => updateHistoryArrows();
    const ro = new ResizeObserver(run);
    ro.observe(el);
    const onResize = () => run();
    window.addEventListener("resize", onResize);
    const t1 = window.setTimeout(run, 300);
    const t2 = window.setTimeout(run, 800);

    return () => {
      ro.disconnect();
      window.removeEventListener("resize", onResize);
      window.clearTimeout(t1);
      window.clearTimeout(t2);
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [history.length, loading]);

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
          aria-label={t("pages.home.aria_bulk_actions")}
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
            <span className={styles.homeShelfBulkOrangeText}>{t("pages.home.selected_count", { count: homeBulkCount })}</span>
          </div>
          <div className={styles.homeShelfBulkCenter}>
            <button
              type="button"
              className={styles.homeShelfBulkIconBtn}
              aria-label={t("pages.home.aria_play_first")}
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
              aria-label={homeBulkAllFullySelected ? t("pages.home.aria_deselect_all_recent") : t("pages.home.aria_select_all_recent")}
              disabled={homeBulkSelectAllDisabled}
              onClick={selectAllHomeBulkOrClear}
            >
              <CheckCircleOutlined />
            </button>
            <Popover content={homeBulkListContent} trigger="click" placement="bottom">
              <button type="button" className={styles.homeShelfBulkIconBtn} aria-label={t("pages.home.aria_selected_list")}>
                <UnorderedListOutlined />
              </button>
            </Popover>
            <Dropdown
              menu={{
                items: homeBulkMoreItems,
                onClick: ({ key, domEvent }) => {
                  domEvent.stopPropagation();
                  onHomeBulkMoreMenuClick(String(key));
                },
              }}
              trigger={["click"]}
              placement="bottomRight"
            >
              <button type="button" className={styles.homeShelfBulkIconBtn} aria-label={t("pages.home.aria_more_short")}>
                <MoreOutlined />
              </button>
            </Dropdown>
          </div>
          <button type="button" className={styles.homeShelfBulkCancel} onClick={clearHomeBulkSelection}>
            <CloseOutlined aria-hidden />
            <span>{t("pages.home.deselect_all")}</span>
          </button>
        </div>
      ) : null}
      <section className={styles.section}>
        <div className={styles.sectionHead}>
          <Title level={3} className={styles.title}>
            {t("pages.home.section_libraries")}
          </Title>
          {libs.length > 0 && (showLibLeft || showLibRight) ? (
            <div className={styles.historyHeadControls}>
              {showLibLeft ? (
                <button
                  type="button"
                  className={`${styles.carouselArrow} ${styles.carouselArrowInline}`}
                  onClick={() => scrollLibsBy(-320)}
                  aria-label={t("pages.home.scroll_left")}
                >
                  <LeftOutlined />
                </button>
              ) : null}
              {showLibRight ? (
                <button
                  type="button"
                  className={`${styles.carouselArrow} ${styles.carouselArrowInline}`}
                  onClick={() => scrollLibsBy(320)}
                  aria-label={t("pages.home.scroll_right")}
                >
                  <RightOutlined />
                </button>
              ) : null}
            </div>
          ) : null}
        </div>
        {libs.length === 0 ? (
          <div className={styles.emptyHint}>{t("pages.home.no_libraries_hint")}</div>
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
              const progressColor = percent < 50 ? "#13b6ff" : percent < 90 ? "#ed6d00" : "#52c41a";
              const typeLabel =
                lib.type === "movie"
                  ? t("pages.home.lib_type_movie")
                  : lib.type === "tv"
                    ? t("pages.home.lib_type_tv")
                    : lib.type === "anime"
                      ? t("pages.home.lib_type_anime")
                      : lib.type === "video"
                        ? t("pages.home.lib_type_other_video")
                        : lib.type;
              return (
                <Link
                  key={lib.id}
                  to={`/browse?library_id=${lib.id}`}
                  className={styles.libCard}
                >
                  <div className={styles.libPreviewWrap}>
                    {lib.preview_url ? (
                      <img
                        className={styles.libPreviewImg}
                        src={lib.preview_url}
                        alt=""
                        loading="lazy"
                        decoding="async"
                      />
                    ) : (
                      <div
                        className={styles.libPreviewFallback}
                        style={{ background: libGradient(lib.id, lib.type) }}
                      />
                    )}
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
                          <div className={styles.libScanTitle}>{t("pages.home.lib_scanning")}</div>
                          <div className={styles.libScanMeta}>
                            {total > 0
                              ? t("pages.home.lib_scan_progress", { processed, total, added: lib.scan_added_count ?? 0 })
                              : t("pages.home.lib_scan_processed", { processed, added: lib.scan_added_count ?? 0 })}
                          </div>
                        </div>
                      </div>
                    ) : null}
                  </div>
                  <div className={styles.libCardLabel}>{lib.name}</div>
                  <div className={styles.libCardMeta}>
                    {t("pages.home.lib_meta", { type: typeLabel, count: lib.media_count ?? 0 })}
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
            {t("pages.home.section_continue")}
          </Title>
          {history.length > 0 && (showHistoryLeft || showHistoryRight) ? (
            <div className={styles.historyHeadControls}>
              {showHistoryLeft ? (
                <button
                  type="button"
                  className={`${styles.carouselArrow} ${styles.carouselArrowInline}`}
                  onClick={() => scrollHistoryBy(-340)}
                  aria-label={t("pages.home.scroll_left")}
                >
                  <LeftOutlined />
                </button>
              ) : null}
              {showHistoryRight ? (
                <button
                  type="button"
                  className={`${styles.carouselArrow} ${styles.carouselArrowInline}`}
                  onClick={() => scrollHistoryBy(340)}
                  aria-label={t("pages.home.scroll_right")}
                >
                  <RightOutlined />
                </button>
              ) : null}
            </div>
          ) : null}
        </div>
        {history.length === 0 ? (
          <div className={styles.emptyHint}>{t("pages.home.no_continue_hint")}</div>
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
                    t={t}
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
              sectionTitle={t("pages.home.section_recent_added", { title: sec.title })}
              items={items}
              landscape={sec.landscape}
              sectionKey={sec.key}
              nav={nav}
              t={t}
              movieSelectedIds={movieSelectedIds}
              onToggleMovieSelect={toggleMovieSelect}
              homeBulkActive={homeBulkActive}
              buildHomeMediaMenu={buildHomeMediaMenu}
              onOpenPhotoPreview={openPhotoPreview}
            />
          </section>
        );
      })}
      {photoLightbox ? (
        <PhotoLightbox
          items={photoLightbox.items}
          index={photoLightbox.index}
          onClose={() => setPhotoLightbox(null)}
          onChangeIndex={(index) => setPhotoLightbox((prev) => (prev ? { ...prev, index } : null))}
        />
      ) : null}
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
      {addToFavoriteFolderMediaId != null && (
        <AddToFavoriteFolderPickerModal
          mediaId={addToFavoriteFolderMediaId}
          open
          onClose={() => setAddToFavoriteFolderMediaId(null)}
          onAdded={(folder) => rememberFolderMenuAdded(folder)}
        />
      )}
    </div>
  );
}
