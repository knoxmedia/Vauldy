import {
  Button,
  Dropdown,
  Popover,
  Progress,
  Rate,
  Select,
  Spin,
  Statistic,
  Tag,
  Tooltip,
  Typography,
  message,
} from "antd";
import type { MenuProps } from "antd";
import {
  ArrowLeftOutlined,
  CloseOutlined,
  StarFilled,
  StarOutlined,
  CalendarOutlined,
  CheckCircleOutlined,
  CheckOutlined,
  ClockCircleOutlined,
  EditOutlined,
  EllipsisOutlined,
  MoreOutlined,
  UnorderedListOutlined,
  FileImageOutlined,
  LeftOutlined,
  RightOutlined,
  SoundOutlined,
  TeamOutlined,
  TranslationOutlined,
  VideoCameraOutlined,
} from "@ant-design/icons";
import {
  useCallback,
  useEffect,
  useId,
  useLayoutEffect,
  useMemo,
  useRef,
  useState,
} from "react";
import type { ReactNode } from "react";
import { Link, useNavigate, useParams } from "react-router-dom";
import MediaPosterImg from "../components/MediaPosterImg";
import {
  MediaDetail,
  HistoryItem,
  MediaItem,
  MediaStats,
  MediaSubtitleRow,
  addFavorite,
  addFavoriteFolderItem,
  addPlaylistItem,
  fetchFavoriteStatus,
  fetchMedia,
  fetchLibraries,
  fetchMediaDetail,
  fetchMediaStats,
  fetchMediaSubtitles,
  fetchUserHistory,
  isTVLibraryType,
  mediaDetailPosterSrc,
  authListPosterUrl,
  removeFavorite,
  savePlaybackProgress,
  type MediaMatchListUpdate,
} from "../api/client";
import AddToFavoriteFolderPickerModal from "../components/AddToFavoriteFolderPickerModal";
import AddToPlaylistModal from "../components/AddToPlaylistModal";
import MediaMatchModal from "../components/MediaMatchModal";
import { buildMediaMenuItems } from "../components/mediaMenuItems";
import { formatServerDateTime } from "../lib/datetime";
import { useFavoriteFolderMenuRecents } from "../lib/useFavoriteFolderMenuRecents";
import { readRecentPlaylists, rememberPlaylistAdded } from "../lib/recentPlaylists";
import ToolbarPlayIcon from "../components/ToolbarPlayIcon";
import { isAdminRole, useAuthStore } from "../store/auth";
import { tGlobal, useT } from "../i18n";
import styles from "./MediaDetail.module.css";

type AudioTrackInfo = {
  index: number;
  codec: string;
  channels: string;
  lang: string;
};

type ParsedMeta = {
  container?: string;
  videoCodec?: string;
  audioCodec?: string;
  videoProfile?: string;
  audioChannels?: string;
  audioLanguage?: string;
  audioTracks: AudioTrackInfo[];
  bitrate?: number;
  fps?: string;
  overview?: string;
  releaseDate?: string;
  rating?: number;
  director?: string[];
  genres: string[];
  certification?: string;
  poster?: string;
  backdrop?: string;
  logo?: string;
  cast?: Array<{ name: string; role?: string; avatar?: string }>;
  subtitleCodecs: string[];
};

/** 已观看：白实心圆，勾为镂空（透出按钮底色），与未观看的线框圆+勾区分 */
function WatchedSolidCutoutIcon({ className }: { className?: string }) {
  const maskId = `watch-cut-${useId().replace(/[^a-zA-Z0-9_-]+/g, "")}`;
  return (
    <svg
      className={className}
      viewBox="0 0 24 24"
      width="1em"
      height="1em"
      aria-hidden
      focusable="false"
    >
      <defs>
        <mask id={maskId}>
          <rect width="24" height="24" fill="white" />
          <path
            fill="black"
            d="M8.15 12.05 10.75 14.65 17.55 6.35 19.45 7.95 10.75 17.65 6.35 12.85z"
          />
        </mask>
      </defs>
      <circle cx="12" cy="12" r="9.35" fill="currentColor" mask={`url(#${maskId})`} />
    </svg>
  );
}

function parseMeta(metaJson?: string): ParsedMeta {
  if (!metaJson) return { subtitleCodecs: [], genres: [], audioTracks: [], director: [], cast: [] };
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
        genres?: string[];
        extra?: Record<string, unknown>;
      };
    };
    const out: ParsedMeta = {
      container: raw.format?.format_name || "",
      subtitleCodecs: [],
      genres: [],
      audioTracks: [],
      director: [],
      cast: [],
    };
    let audioIndex = 0;
    for (const st of raw.streams ?? []) {
      const type = (st.codec_type || "").toLowerCase();
      const codec = st.codec_name || "";
      if (type === "video" && !out.videoCodec) {
        out.videoCodec = codec;
        out.videoProfile = st.profile || "";
        out.fps = st.avg_frame_rate || "";
      }
      if (type === "audio" && codec) {
        const ch = st.channels != null ? String(st.channels) : "";
        const lang = (st.tags?.language || "").trim();
        if (!out.audioCodec) {
          out.audioCodec = codec;
          out.audioChannels = ch;
          out.audioLanguage = lang;
        }
        out.audioTracks.push({ index: audioIndex, codec, channels: ch, lang });
        audioIndex += 1;
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
      const sg = scrape.genres;
      if (Array.isArray(sg)) {
        out.genres = sg.filter((x): x is string => typeof x === "string" && x.trim().length > 0);
      }
      const extra = scrape.extra || {};
      const eg = extra.genres;
      if (Array.isArray(eg) && out.genres.length === 0) {
        out.genres = eg.filter((x): x is string => typeof x === "string" && x.trim().length > 0);
      }
      const certRaw =
        extra.certification ?? extra.rated ?? extra.mpaa_rating ?? extra.content_rating ?? extra.parental_rating;
      if (typeof certRaw === "string" && certRaw.trim()) {
        out.certification = certRaw.trim();
      }
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
    return { subtitleCodecs: [], genres: [], audioTracks: [], director: [], cast: [] };
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

/** Localized duration display (media info row). */
function fmtDurationLocalized(sec?: number) {
  if (!sec || sec <= 0) return "—";
  const h = Math.floor(sec / 3600);
  const m = Math.floor((sec % 3600) / 60);
  if (h > 0) return tGlobal("pages.media_detail.fmt_h_m", { h, m });
  return tGlobal("pages.media_detail.fmt_m", { m });
}

function videoTierLabel(height?: number) {
  if (!height || height <= 0) return "";
  if (height >= 2160) return "4K";
  if (height >= 1080) return "1080p";
  if (height >= 720) return "720p";
  if (height >= 480) return "480p";
  return `${height}p`;
}

function formatMetaRating(r?: number) {
  if (r == null || Number.isNaN(r) || r <= 0) return null;
  if (r <= 10) return `${r.toFixed(1)}/10`;
  return `${Math.round(r)}%`;
}

function audioTrackLabel(t: AudioTrackInfo) {
  const codec = t.codec.toUpperCase();
  const ch = t.channels ? tGlobal("pages.media_detail.channels_n", { n: t.channels }) : "";
  const lang = t.lang || tGlobal("pages.media_detail.lang_default");
  return ch ? `${lang} · ${codec} (${ch})` : `${lang} · ${codec}`;
}

function subtitleRowLabel(r: MediaSubtitleRow) {
  const lang = r.lang || "und";
  const extra = r.label ? ` · ${r.label}` : "";
  return `${lang}${extra}`;
}

const LS_AUDIO_KEY = (id: number) => `media-detail-audio-${id}`;
const LS_SUB_KEY = (id: number) => `media-detail-sub-${id}`;
const LS_STARS_KEY = (id: number) => `media-detail-stars-${id}`;

function fmtDate(v?: string) {
  if (!v) return "—";
  return v.length > 10 ? v.slice(0, 10) : v;
}

function fmtBitrate(v?: number) {
  if (!v || v <= 0) return "—";
  const kb = Math.round(v / 1000);
  return `${kb} kbps`;
}

function usesModernDetailLayout(t?: string) {
  const type = (t || "").trim().toLowerCase();
  return type === "movie" || isTVLibraryType(type);
}

function MediaHorizontalShelf({
  title,
  empty,
  trackClassName,
  hasContent,
  refreshKey,
  children,
}: {
  title: string;
  empty: ReactNode;
  trackClassName: string;
  hasContent: boolean;
  refreshKey: string;
  children: ReactNode;
}) {
  const trackRef = useRef<HTMLDivElement>(null);
  const [hasOverflow, setHasOverflow] = useState(false);
  const [canPrev, setCanPrev] = useState(false);
  const [canNext, setCanNext] = useState(false);

  const updateArrows = useCallback(() => {
    const el = trackRef.current;
    if (!el || !hasContent) {
      setHasOverflow(false);
      setCanPrev(false);
      setCanNext(false);
      return;
    }
    const { scrollLeft, scrollWidth, clientWidth } = el;
    const maxScroll = Math.max(0, scrollWidth - clientWidth);
    const overflow = maxScroll > 2;
    setHasOverflow(overflow);
    if (!overflow) {
      setCanPrev(false);
      setCanNext(false);
      return;
    }
    setCanPrev(scrollLeft > 2);
    setCanNext(scrollLeft < maxScroll - 2);
  }, [hasContent]);

  useLayoutEffect(() => {
    updateArrows();
  }, [refreshKey, hasContent, updateArrows]);

  useEffect(() => {
    const el = trackRef.current;
    if (!el) return;
    updateArrows();
    const onScroll = () => updateArrows();
    el.addEventListener("scroll", onScroll);
    const ro = new ResizeObserver(() => updateArrows());
    ro.observe(el);
    return () => {
      el.removeEventListener("scroll", onScroll);
      ro.disconnect();
    };
  }, [updateArrows]);

  /** 海报等子项晚加载后重算是否溢出 */
  useEffect(() => {
    if (!hasContent) return;
    const t1 = window.setTimeout(updateArrows, 300);
    const t2 = window.setTimeout(updateArrows, 800);
    return () => {
      window.clearTimeout(t1);
      window.clearTimeout(t2);
    };
  }, [hasContent, refreshKey, updateArrows]);

  const scroll = (dir: -1 | 1) => {
    const el = trackRef.current;
    if (!el) return;
    const step = Math.max(200, Math.floor(el.clientWidth * 0.82));
    el.scrollBy({ left: dir * step, behavior: "smooth" });
  };

  return (
    <section className={styles.block}>
      <div className={styles.shelfHead}>
        <Typography.Title level={4} className={styles.shelfTitle}>
          {title}
        </Typography.Title>
        {hasContent && hasOverflow ? (
          <div className={styles.shelfArrows} role="navigation" aria-label={tGlobal("pages.media_detail.shelf_aria", { title })}>
            <Button
              type="text"
              icon={<LeftOutlined />}
              disabled={!canPrev}
              onClick={() => scroll(-1)}
              className={styles.shelfArrowBtn}
              aria-label={tGlobal("pages.media_detail.scroll_left_aria")}
            />
            <Button
              type="text"
              icon={<RightOutlined />}
              disabled={!canNext}
              onClick={() => scroll(1)}
              className={styles.shelfArrowBtn}
              aria-label={tGlobal("pages.media_detail.scroll_right_aria")}
            />
          </div>
        ) : null}
      </div>
      <div className={styles.panelPlain}>{hasContent ? <div ref={trackRef} className={`${trackClassName} ${styles.shelfTrack}`}>{children}</div> : empty}</div>
    </section>
  );
}

function RelatedPoster({
  m,
  broken,
  onBroken,
}: {
  m: MediaItem;
  broken: boolean;
  onBroken: () => void;
}) {
  if (broken) {
    return <div className={styles.relatedPosterFallback}>{(m.title || "?").slice(0, 1)}</div>;
  }
  return (
    <MediaPosterImg
      item={m}
      className={styles.relatedPosterImg}
      onFinalError={() => onBroken()}
    />
  );
}

function RelatedMovieCard({
  m,
  nav,
  selected,
  onToggleSelect,
  posterBroken,
  onPosterError,
  bulkSelectMode,
}: {
  m: MediaItem;
  nav: ReturnType<typeof useNavigate>;
  selected: boolean;
  onToggleSelect: () => void;
  posterBroken: boolean;
  onPosterError: () => void;
  /** 已有选中项时：海报区仅做点选，不展示播放/编辑/更多 */
  bulkSelectMode: boolean;
}) {
  const relatedMediaMenu: MenuProps = useMemo(
    () => buildMediaMenuItems(m, nav),
    [m.id, nav],
  );

  return (
    <div
      className={`${styles.relatedCard} ${selected ? styles.relatedCardSelected : ""} ${
        bulkSelectMode ? styles.relatedCardBulkPick : ""
      }`}
      role="button"
      tabIndex={0}
      onClick={(e) => {
        if (bulkSelectMode && (e.target as HTMLElement).closest(`.${styles.relatedPosterWrap}`)) {
          return;
        }
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
      <div className={styles.relatedPosterWrap}>
        <RelatedPoster m={m} broken={posterBroken} onBroken={onPosterError} />
        <div
          className={styles.relatedPosterOverlay}
          onClick={(e) => {
            e.stopPropagation();
            if (bulkSelectMode) onToggleSelect();
            else nav(`/detail/${m.id}`);
          }}
          role="presentation"
        >
          <button
            type="button"
            className={`${styles.relatedOverlayBtn} ${styles.relatedOverlaySelect}`}
            aria-label={selected ? tGlobal("pages.media_detail.aria_deselect") : tGlobal("pages.media_detail.aria_select")}
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
                className={styles.relatedOverlayPlay}
                aria-label={tGlobal("pages.media_detail.aria_play_video", { title: m.title || tGlobal("pages.media_detail.video_fallback") })}
                onClick={(e) => {
                  e.stopPropagation();
                  nav(`/player/${m.id}`);
                }}
              >
                <ToolbarPlayIcon className={styles.relatedOverlayPlaySvg} />
              </button>
              <button
                type="button"
                className={`${styles.relatedOverlayBtn} ${styles.relatedOverlayEdit}`}
                aria-label={tGlobal("pages.media_detail.aria_edit")}
                onClick={(e) => {
                  e.stopPropagation();
                  nav(`/media-manager?media_id=${m.id}`);
                }}
              >
                <EditOutlined />
              </button>
              <Dropdown menu={relatedMediaMenu} trigger={["click"]} placement="bottomRight">
                <button
                  type="button"
                  className={`${styles.relatedOverlayBtn} ${styles.relatedOverlayMore}`}
                  aria-label={tGlobal("pages.media_detail.aria_more")}
                  onClick={(e) => e.stopPropagation()}
                >
                  <EllipsisOutlined style={{ transform: "rotate(90deg)" }} />
                </button>
              </Dropdown>
            </>
          )}
        </div>
      </div>
      <div className={styles.relatedTitle}>{m.title || tGlobal("pages.media_detail.untitled")}</div>
      <div className={styles.relatedYear}>
        {m.year != null && m.year > 0 ? String(m.year) : m.release_date?.slice(0, 4) || "—"}
      </div>
    </div>
  );
}

export default function MediaDetailPage() {
  const t = useT();
  const { id } = useParams();
  const nav = useNavigate();
  const role = useAuthStore((s) => s.role);
  const mediaId = Number(id || "");
  const [loading, setLoading] = useState(true);
  const [detail, setDetail] = useState<MediaDetail | null>(null);
  const [stats, setStats] = useState<MediaStats | null>(null);
  const [related, setRelated] = useState<MediaItem[]>([]);
  const [historyItem, setHistoryItem] = useState<HistoryItem | null>(null);
  const [brokenImages, setBrokenImages] = useState<Record<string, true>>({});
  const [favorited, setFavorited] = useState(false);
  const [subtitleRows, setSubtitleRows] = useState<MediaSubtitleRow[]>([]);
  const [markBusy, setMarkBusy] = useState(false);
  const [overviewOpen, setOverviewOpen] = useState(false);
  const [selectedAudioIdx, setSelectedAudioIdx] = useState(0);
  const [selectedSubId, setSelectedSubId] = useState<string>("off");
  const [userStars, setUserStars] = useState(0);
  const [isMovieLibrary, setIsMovieLibrary] = useState(false);
  const [libraryType, setLibraryType] = useState("");
  const [relatedSelectedIds, setRelatedSelectedIds] = useState<number[]>([]);
  /** 固定工具条与主内容列对齐（.app-main-centered 的视口 left/width） */
  const [relatedBulkDock, setRelatedBulkDock] = useState({ left: 0, width: 0 });
  const [playlistModalOpen, setPlaylistModalOpen] = useState(false);
  const [favoriteFolderModalOpen, setFavoriteFolderModalOpen] = useState(false);
  const [matchModalOpen, setMatchModalOpen] = useState(false);
  const [recentPlaylistMenu, setRecentPlaylistMenu] = useState(readRecentPlaylists);
  const { recentFavoriteFolders, rememberFolderMenuAdded } = useFavoriteFolderMenuRecents();
  const fileInfoRef = useRef<HTMLElement | null>(null);

  const measureRelatedBulkDock = useCallback(() => {
    const shell = document.querySelector(".app-main-centered");
    if (!shell) return;
    const r = shell.getBoundingClientRect();
    setRelatedBulkDock({ left: r.left, width: r.width });
  }, []);

  const toggleRelatedSelect = useCallback((id: number) => {
    setRelatedSelectedIds((prev) => (prev.includes(id) ? prev.filter((x) => x !== id) : [...prev, id]));
  }, []);

  const clearRelatedSelect = useCallback(() => setRelatedSelectedIds([]), []);

  const selectAllRelatedOrClear = useCallback(() => {
    const allIds = related.map((r) => r.id);
    if (allIds.length === 0) return;
    const allSelected = allIds.every((id) => relatedSelectedIds.includes(id));
    if (allSelected) setRelatedSelectedIds([]);
    else setRelatedSelectedIds(allIds);
  }, [related, relatedSelectedIds]);

  const relatedBulkListContent = useMemo(() => {
    const idToTitle = new Map(related.map((r) => [r.id, r.title || t("pages.media_detail.untitled")]));
    const lines = relatedSelectedIds.map((id) => idToTitle.get(id) || "—");
    if (lines.length === 0) {
      return <span className={styles.relatedBulkPopoverEmpty}>{t("pages.media_detail.empty_bulk")}</span>;
    }
    return (
      <ul className={styles.relatedBulkPopoverList}>
        {lines.map((label, i) => (
          <li key={`${relatedSelectedIds[i]}-${i}`}>{label}</li>
        ))}
      </ul>
    );
  }, [related, relatedSelectedIds, t]);

  const relatedBulkMoreItems: MenuProps["items"] = useMemo(
    () => [
      {
        key: "play1",
        label: t("pages.media_detail.menu_play_first"),
        onClick: () => {
          const f = relatedSelectedIds[0];
          if (f != null) nav(`/player/${f}`);
        },
      },
      {
        key: "detail1",
        label: t("pages.media_detail.menu_view_first_detail"),
        onClick: () => {
          const f = relatedSelectedIds[0];
          if (f != null) nav(`/detail/${f}`);
        },
      },
    ],
    [relatedSelectedIds, nav, t]
  );

  useEffect(() => {
    setRelatedSelectedIds([]);
  }, [mediaId]);

  useLayoutEffect(() => {
    if (relatedSelectedIds.length === 0 || !isMovieLibrary) return;
    measureRelatedBulkDock();
    const shell = document.querySelector(".app-main-centered");
    if (!shell) return;
    const ro = new ResizeObserver(() => measureRelatedBulkDock());
    ro.observe(shell);
    window.addEventListener("resize", measureRelatedBulkDock);
    return () => {
      ro.disconnect();
      window.removeEventListener("resize", measureRelatedBulkDock);
    };
  }, [relatedSelectedIds.length, isMovieLibrary, measureRelatedBulkDock]);

  useEffect(() => {
    if (!mediaId || Number.isNaN(mediaId)) {
      message.error(t("pages.media_detail.invalid_media_id"));
      nav("/browse", { replace: true });
      return;
    }
    setLoading(true);
    void Promise.allSettled([
      fetchMediaDetail(mediaId),
      fetchMediaStats(mediaId),
      fetchUserHistory(300),
      fetchFavoriteStatus(mediaId),
      fetchMediaSubtitles(mediaId),
    ]).then(async ([d, s, h, fav, subs]) => {
      if (d.status === "fulfilled") {
        setDetail(d.value);
        let movieLib = false;
        try {
          const libs = await fetchLibraries();
          const lib = libs.find((x) => x.id === d.value.library_id);
          setLibraryType(lib?.type ?? "");
          movieLib = usesModernDetailLayout(lib?.type);
        } catch {
          setLibraryType("");
          movieLib = false;
        }
        setIsMovieLibrary(movieLib);
        try {
          const libItems = await fetchMedia(d.value.library_id, { sort: "created_desc", limit: 40 });
          setRelated(libItems.filter((x) => x.id !== d.value.id).slice(0, 12));
        } catch {
          setRelated([]);
        }
      } else {
        message.error(t("pages.media_detail.load_failed"));
        setLibraryType("");
        setIsMovieLibrary(false);
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
      if (fav.status === "fulfilled") {
        setFavorited(fav.value);
      } else {
        setFavorited(false);
      }
      if (subs.status === "fulfilled") {
        setSubtitleRows(subs.value);
      } else {
        setSubtitleRows([]);
      }
      setLoading(false);
    });
  }, [mediaId, nav]);

  useEffect(() => {
    if (!mediaId || Number.isNaN(mediaId)) return;
    setOverviewOpen(false);
    try {
      const a = localStorage.getItem(LS_AUDIO_KEY(mediaId));
      if (a != null) setSelectedAudioIdx(Number(a) || 0);
      const su = localStorage.getItem(LS_SUB_KEY(mediaId));
      if (su != null) setSelectedSubId(su);
      const st = localStorage.getItem(LS_STARS_KEY(mediaId));
      if (st != null) setUserStars(Math.min(5, Math.max(0, Number(st) || 0)));
    } catch {
      /* ignore */
    }
  }, [mediaId]);

  const meta = useMemo(() => parseMeta(detail?.meta_json), [detail?.meta_json]);

  const subtitleOptionValues = useMemo(() => {
    const s = new Set<string>(["off"]);
    for (const r of subtitleRows) s.add(String(r.id));
    if (subtitleRows.length === 0) {
      meta.subtitleCodecs.forEach((_, i) => s.add(`embedded-${i}`));
    }
    return s;
  }, [subtitleRows, meta.subtitleCodecs]);

  useEffect(() => {
    if (!subtitleOptionValues.has(selectedSubId)) {
      setSelectedSubId("off");
    }
  }, [subtitleOptionValues, selectedSubId]);

  const avgPct = Math.round(stats?.avg_progress_percent ?? 0);
  const runtimeZh = fmtDurationLocalized(detail?.duration);
  const overview = meta.overview || t("pages.media_detail.no_overview");
  const overviewPreviewLen = 220;
  const overviewLong = overview.length > overviewPreviewLen;
  const overviewShown = overviewOpen || !overviewLong ? overview : `${overview.slice(0, overviewPreviewLen)}…`;
  const castList = meta.cast?.slice(0, 24) ?? [];
  const yearStr =
    detail?.year != null && detail.year > 0
      ? String(detail.year)
      : meta.releaseDate && meta.releaseDate.length >= 4
        ? meta.releaseDate.slice(0, 4)
        : detail?.release_date && detail.release_date.length >= 4
          ? detail.release_date.slice(0, 4)
          : detail?.created_at && /^\d{4}/.test(detail.created_at)
            ? detail.created_at.slice(0, 4)
            : "—";
  const tier = videoTierLabel(detail?.height);
  const videoFormatLine =
    tier && meta.videoCodec
      ? `${tier} (${meta.videoCodec.toUpperCase()})`
      : tier || meta.videoCodec?.toUpperCase() || "—";
  const scoreText = formatMetaRating(meta.rating);
  const audioOptions =
    meta.audioTracks.length > 0
      ? meta.audioTracks
      : meta.audioCodec
        ? [{ index: 0, codec: meta.audioCodec, channels: meta.audioChannels || "", lang: meta.audioLanguage || "" }]
        : [];
  const safeAudioIdx = Math.min(selectedAudioIdx, Math.max(0, audioOptions.length - 1));

  const subtitleSelectValue = subtitleOptionValues.has(selectedSubId) ? selectedSubId : "off";
  const resumeSeconds = historyItem?.position ?? 0;
  const playableDuration = detail?.duration ?? 0;
  const canResume = resumeSeconds > 0 && playableDuration > 0 && resumeSeconds < playableDuration - 8;
  const isCompleted = historyItem?.completed === 1;
  const showResumeActions = canResume && !isCompleted;
  const resumeTarget = `/player/${detail?.id}?t=${resumeSeconds}`;
  const playFromStartTarget = `/player/${detail?.id}`;
  const posterCandidate = detail?.id ? mediaDetailPosterSrc(detail, meta.poster) : "";
  const posterUrl = posterCandidate && !brokenImages.poster ? posterCandidate : "";
  const bannerUrl = meta.backdrop && !brokenImages.backdrop ? authListPosterUrl(meta.backdrop) : "";

  async function refreshHistory() {
    try {
      const h = await fetchUserHistory(300);
      setHistoryItem(h.find((x) => x.media_id === mediaId) || null);
    } catch {
      /* ignore */
    }
  }

  async function onToggleFavorite() {
    try {
      if (favorited) {
        await removeFavorite(mediaId);
        setFavorited(false);
        message.success(t("pages.media_detail.unfavorited"));
      } else {
        await addFavorite(mediaId);
        setFavorited(true);
        message.success(t("pages.media_detail.favorited"));
      }
    } catch {
      message.error(t("pages.media_detail.favorite_failed"));
    }
  }

  async function onToggleWatched() {
    const durationSec = detail?.duration ?? 0;
    if (!isCompleted && durationSec <= 0) {
      message.warning(t("pages.media_detail.mark_no_duration"));
      return;
    }
    setMarkBusy(true);
    try {
      if (isCompleted) {
        await savePlaybackProgress(mediaId, { position: 0, completed: 0 });
        message.success(t("pages.media_detail.marked_unwatched"));
      } else {
        await savePlaybackProgress(mediaId, { position: durationSec, completed: 1 });
        message.success(t("pages.media_detail.marked_watched"));
      }
      await refreshHistory();
    } catch (e: unknown) {
      const ax = e as { response?: { status?: number; data?: { error?: string } } };
      const status = ax.response?.status;
      const serverErr = ax.response?.data?.error;
      if (status === 401) {
        message.error(t("pages.media_detail.mark_need_login"));
      } else if (typeof serverErr === "string" && serverErr.trim()) {
        message.error(t("pages.media_detail.mark_failed_server", { msg: serverErr.trim() }));
      } else {
        message.error(t("pages.media_detail.mark_failed"));
      }
    } finally {
      setMarkBusy(false);
    }
  }

  function onEditClick() {
    if (isAdminRole(role)) {
      if (mediaId && !Number.isNaN(mediaId)) {
        nav(`/media-manager?media_id=${mediaId}`);
      } else {
        nav("/media-manager");
      }
    } else {
      message.info(t("pages.media_detail.edit_admin_only"));
    }
  }

  const reloadDetail = useCallback(async () => {
    if (!mediaId || Number.isNaN(mediaId)) return;
    try {
      const d = await fetchMediaDetail(mediaId);
      setDetail(d);
    } catch {
      message.error(t("pages.media_detail.refresh_failed"));
    }
  }, [mediaId, t]);

  const applyMatchUpdate = useCallback((update: MediaMatchListUpdate) => {
    setDetail((prev) =>
      prev && prev.id === update.id
        ? {
            ...prev,
            title: update.title || prev.title,
            poster_url: update.poster_url ?? prev.poster_url,
            year: update.year ?? prev.year,
            release_date: update.release_date ?? prev.release_date,
            scraped: update.scraped,
          }
        : prev,
    );
  }, []);

  const scrollToFileInfo = useCallback(() => {
    fileInfoRef.current?.scrollIntoView({ behavior: "smooth", block: "start" });
  }, []);

  const mediaActionMenu = useMemo((): MenuProps => {
    if (!detail) return { items: [] };
    return buildMediaMenuItems(
      { id: detail.id, file_path: detail.file_path, title: detail.title },
      nav,
      {
        preset: "detailMore",
        scraped: detail.scraped,
        onOpenMatch: () => setMatchModalOpen(true),
        afterUnmatch: () => void reloadDetail(),
        onAddToPlaylist: () => setPlaylistModalOpen(true),
        recentPlaylists: recentPlaylistMenu,
        onQuickAddToPlaylist: async (_mediaId, playlistId) => {
          try {
            await addPlaylistItem(playlistId, detail.id);
            const name =
              recentPlaylistMenu.find((p) => p.id === playlistId)?.name ??
              readRecentPlaylists().find((p) => p.id === playlistId)?.name ??
              t("pages.media_detail.playlist_fallback");
            message.success(t("pages.media_detail.added_to_playlist", { name }));
            rememberPlaylistAdded({ id: playlistId, name });
            setRecentPlaylistMenu(readRecentPlaylists());
          } catch {
            message.error(t("pages.media_detail.add_failed_dup"));
          }
        },
        onAddToFavoriteFolder: () => setFavoriteFolderModalOpen(true),
        recentFavoriteFolders,
        onQuickAddToFavoriteFolder: async (_mediaId, folderId) => {
          try {
            await addFavoriteFolderItem(folderId, detail.id);
            const name =
              recentFavoriteFolders.find((f) => f.id === folderId)?.name ??
              t("components.media_menu.favorite_folder_fallback");
            message.success(t("components.add_to_favorite_folder_picker_modal.added_single", { name }));
            rememberFolderMenuAdded({ id: folderId, name });
          } catch {
            message.error(t("components.add_to_favorite_folder_picker_modal.add_failed_dup"));
          }
        },
        onGetInfo: scrollToFileInfo,
      },
    );
  }, [detail, nav, recentPlaylistMenu, recentFavoriteFolders, rememberFolderMenuAdded, reloadDetail, scrollToFileInfo, t]);

  if (loading) {
    return (
      <div className={styles.loadingWrap}>
        <Spin size="large" />
      </div>
    );
  }
  if (!detail) return null;

  const logoUrlClassic = meta.logo && !brokenImages.logo ? authListPosterUrl(meta.logo) : "";
  const posterLetterClassic = (detail.title || "?").slice(0, 1).toUpperCase();
  const runtimeClassic = fmtSeconds(detail.duration);
  const castListClassic = meta.cast?.slice(0, 18) ?? [];

  if (!isMovieLibrary) {
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
              {t("pages.media_detail.back")}
            </Button>
          </div>

          <div className={styles.heroBody}>
            <div className={styles.poster}>
              {posterUrl ? (
                <img
                  src={posterUrl}
                  alt={t("pages.media_detail.poster_alt", { title: detail.title || t("pages.media_detail.video_fallback") })}
                  className={styles.posterImg}
                  onError={() => setBrokenImages((prev) => ({ ...prev, poster: true }))}
                />
              ) : (
                <div className={styles.posterFallback}>
                  <FileImageOutlined />
                  <span>{t("pages.media_detail.no_poster")}</span>
                </div>
              )}
            </div>
            <div className={styles.heroInfo}>
              <div className={styles.badges}>
                <Tag color="blue">{detail.file_type || "video"}</Tag>
                <Tag>{detail.width && detail.height ? `${detail.width}x${detail.height}` : "Unknown Res"}</Tag>
                <Tag>{meta.videoCodec?.toUpperCase() || "Video"}</Tag>
                <Tag>{meta.audioCodec?.toUpperCase() || "Audio"}</Tag>
                {historyItem?.completed === 1 ? <Tag color="green">{t("pages.media_detail.watched_tag")}</Tag> : null}
                {historyItem ? <Tag color="cyan">{t("pages.media_detail.play_count", { count: historyItem.play_count ?? 0 })}</Tag> : null}
              </div>
              <Typography.Title level={2} className={styles.title}>
                {detail.title || t("pages.media_detail.untitled_movie")}
              </Typography.Title>
              {logoUrlClassic ? (
                <img
                  src={logoUrlClassic}
                  alt={t("pages.media_detail.logo_alt", { title: detail.title || t("pages.media_detail.video_fallback") })}
                  className={styles.logoLayer}
                  onError={() => setBrokenImages((prev) => ({ ...prev, logo: true }))}
                />
              ) : (
                <div className={styles.logoText}>{posterLetterClassic}</div>
              )}
              <Typography.Text className={styles.subtitle}>
                {detail.original_title || detail.file_path}
              </Typography.Text>
              <div className={styles.infoChips}>
                <span>
                  <CalendarOutlined /> {fmtDate(meta.releaseDate || detail.created_at)}
                </span>
                <span>
                  <ClockCircleOutlined /> {runtimeClassic}
                </span>
                <span>
                  <VideoCameraOutlined /> {meta.container || detail.format || "container"}
                </span>
              </div>
              <div className={styles.actions}>
                {showResumeActions ? (
                  <>
                    <Button
                      type="primary"
                      size="large"
                      icon={<ToolbarPlayIcon className={styles.mediaDetailPlaySvg} />}
                      onClick={() => nav(resumeTarget)}
                    >
                      {t("pages.media_detail.btn_continue")}
                    </Button>
                    <Button size="large" type="default" style={{ opacity: 0.82 }} onClick={() => nav(playFromStartTarget)}>
                      {t("pages.media_detail.btn_play_from_start")}
                    </Button>
                  </>
                ) : (
                  <Button
                    type="primary"
                    size="large"
                    icon={<ToolbarPlayIcon className={styles.mediaDetailPlaySvg} />}
                    onClick={() => nav(playFromStartTarget)}
                  >
                    {t("pages.media_detail.btn_play")}
                  </Button>
                )}
                <Button size="large" onClick={() => nav(`/browse?library_id=${detail.library_id}`)}>
                  {t("pages.media_detail.back_to_library")}
                </Button>
              </div>
            </div>
          </div>
        </section>

        <section className={styles.overviewPanel}>
          <Typography.Title level={4}>{t("pages.media_detail.section_overview")}</Typography.Title>
          <Typography.Paragraph className={styles.overviewText}>{overview}</Typography.Paragraph>
        </section>

        <section className={styles.grid}>
          <div className={styles.panel}>
            <Typography.Title level={4}>{t("pages.media_detail.section_file_specs")}</Typography.Title>
            <div className={styles.specGrid}>
              <div className={styles.specItem}>
                <div className={styles.specLabel}>{t("pages.media_detail.spec_container")}</div>
                <div className={styles.specValue}>{meta.container || detail.format || "—"}</div>
              </div>
              <div className={styles.specItem}>
                <div className={styles.specLabel}>{t("pages.media_detail.spec_video")}</div>
                <div className={styles.specValue}>
                  {meta.videoCodec || "—"}
                  {meta.videoProfile ? ` / ${meta.videoProfile}` : ""}
                </div>
              </div>
              <div className={styles.specItem}>
                <div className={styles.specLabel}>{t("pages.media_detail.spec_audio")}</div>
                <div className={styles.specValue}>
                  {meta.audioCodec || "—"}
                  {meta.audioChannels ? ` / ${meta.audioChannels}ch` : ""}
                </div>
              </div>
              <div className={styles.specItem}>
                <div className={styles.specLabel}>{t("pages.media_detail.spec_subtitle")}</div>
                <div className={styles.specValue}>
                  {meta.subtitleCodecs.length ? meta.subtitleCodecs.join(", ") : t("pages.media_detail.no_embedded_subtitle")}
                </div>
              </div>
              <div className={styles.specItem}>
                <div className={styles.specLabel}>{t("pages.media_detail.spec_bitrate")}</div>
                <div className={styles.specValue}>{fmtBitrate(meta.bitrate || detail.bitrate)}</div>
              </div>
              <div className={styles.specItem}>
                <div className={styles.specLabel}>{t("pages.media_detail.spec_framerate")}</div>
                <div className={styles.specValue}>{meta.fps || "—"}</div>
              </div>
              <div className={styles.specItem}>
                <div className={styles.specLabel}>{t("pages.media_detail.spec_audio_lang")}</div>
                <div className={styles.specValue}>
                  <SoundOutlined /> {meta.audioLanguage || t("pages.media_detail.default_audio_track")}
                </div>
              </div>
            </div>
          </div>

          <div className={styles.panel}>
            <Typography.Title level={4}>{t("pages.media_detail.section_play_stats")}</Typography.Title>
            <div className={styles.stats}>
              <Statistic title={t("pages.media_detail.stat_watch_users")} value={stats?.watch_users ?? 0} />
              <Statistic title={t("pages.media_detail.stat_avg_duration")} value={fmtSeconds(Math.round(stats?.avg_position_seconds ?? 0))} />
              <Statistic title={t("pages.media_detail.stat_latest_watch")} value={formatServerDateTime(stats?.latest_watch_at)} />
            </div>
            <div className={styles.progressBox}>
              <div className={styles.specLabel}>{t("pages.media_detail.stat_avg_progress")}</div>
              <Progress percent={avgPct} strokeColor="#00b3ff" trailColor="rgba(255,255,255,0.18)" />
            </div>
            <div className={styles.progressBox}>
              <div className={styles.specLabel}>{t("pages.media_detail.stat_my_history")}</div>
              <div className={styles.historyTags}>
                <Tag color={historyItem?.completed === 1 ? "green" : "default"}>
                  {historyItem?.completed === 1 ? t("pages.media_detail.history_completed") : t("pages.media_detail.history_uncompleted")}
                </Tag>
                <Tag color="blue">{t("pages.media_detail.history_last_pos", { pos: fmtSeconds(historyItem?.position ?? 0) })}</Tag>
                <Tag color="cyan">{t("pages.media_detail.history_play_count", { count: historyItem?.play_count ?? 0 })}</Tag>
                <Tag>{t("pages.media_detail.history_start", { time: formatServerDateTime(historyItem?.play_start_at) })}</Tag>
                <Tag>{t("pages.media_detail.history_end", { time: formatServerDateTime(historyItem?.play_end_at) })}</Tag>
              </div>
            </div>
            <div className={styles.directorLine}>
              <TeamOutlined /> {t("pages.media_detail.director_label", {
                names: meta.director?.length ? meta.director.join(" / ") : t("pages.media_detail.director_none"),
              })}
            </div>
          </div>
        </section>

        <section className={styles.panel}>
          <Typography.Title level={4}>{t("pages.media_detail.section_cast")}</Typography.Title>
          {castListClassic.length === 0 ? (
            <div className={styles.empty}>{t("pages.media_detail.no_cast")}</div>
          ) : (
            <div className={styles.castRow}>
              {castListClassic.map((member, idx) => (
                <div key={`${member.name}-${idx}`} className={styles.castCard}>
                  {member.avatar && !brokenImages[`actor-${idx}`] ? (
                    <img
                      src={member.avatar}
                      alt={member.name}
                      className={styles.castAvatarClassic}
                      onError={() => setBrokenImages((prev) => ({ ...prev, [`actor-${idx}`]: true }))}
                    />
                  ) : (
                    <div className={styles.castAvatarEmptyClassic} />
                  )}
                  <div className={styles.castName}>{member.name}</div>
                  <div className={styles.castRole}>{member.role || t("pages.media_detail.role_actor")}</div>
                </div>
              ))}
            </div>
          )}
        </section>

        <section className={styles.panel}>
          <Typography.Title level={4}>{t("pages.media_detail.section_related")}</Typography.Title>
          {related.length === 0 ? (
            <div className={styles.empty}>{t("pages.media_detail.no_related")}</div>
          ) : (
            <div className={styles.relatedRow}>
              {related.slice(0, 8).map((m) => (
                <Link key={m.id} to={`/detail/${m.id}`} className={styles.relatedCard}>
                  <div className={styles.relatedPosterWrap}>
                    <RelatedPoster
                      m={m}
                      broken={!!brokenImages[`rel-${m.id}`]}
                      onBroken={() => setBrokenImages((prev) => ({ ...prev, [`rel-${m.id}`]: true }))}
                    />
                  </div>
                  <div className={styles.relatedTitle}>{m.title || tGlobal("pages.media_detail.untitled")}</div>
                </Link>
              ))}
            </div>
          )}
        </section>
      </div>
    );
  }

  return (
    <div className={styles.page}>
      {relatedSelectedIds.length > 0 ? (
        <div
          className={styles.relatedBulkBar}
          role="toolbar"
          aria-label={t("pages.media_detail.related_bulk_aria")}
          style={{
            left: relatedBulkDock.left,
            width: relatedBulkDock.width,
            maxWidth: relatedBulkDock.width,
            opacity: relatedBulkDock.width > 0 ? 1 : 0,
            pointerEvents: relatedBulkDock.width > 0 ? "auto" : "none",
          }}
        >
          <div className={styles.relatedBulkLeft}>
            <CheckOutlined className={styles.relatedBulkOrangeMark} aria-hidden />
            <span className={styles.relatedBulkOrangeText}>{t("pages.media_detail.selected_count", { count: relatedSelectedIds.length })}</span>
          </div>
          <div className={styles.relatedBulkCenter}>
            <button
              type="button"
              className={styles.relatedBulkIconBtn}
              aria-label={t("pages.media_detail.aria_play")}
              onClick={() => {
                const first = relatedSelectedIds[0];
                if (first != null) nav(`/player/${first}`);
              }}
            >
              <ToolbarPlayIcon className={styles.relatedBulkPlaySvg} />
            </button>
            <button
              type="button"
              className={styles.relatedBulkIconBtn}
              aria-label={
                related.length > 0 && related.every((r) => relatedSelectedIds.includes(r.id))
                  ? t("pages.media_detail.aria_deselect_all_related")
                  : t("pages.media_detail.aria_select_all_related")
              }
              onClick={selectAllRelatedOrClear}
            >
              <CheckCircleOutlined />
            </button>
            <Popover content={relatedBulkListContent} trigger="click" placement="bottom">
              <button type="button" className={styles.relatedBulkIconBtn} aria-label={t("pages.media_detail.aria_selected_list")}>
                <UnorderedListOutlined />
              </button>
            </Popover>
            <Dropdown menu={{ items: relatedBulkMoreItems }} trigger={["click"]} placement="bottomRight">
              <button type="button" className={styles.relatedBulkIconBtn} aria-label={t("pages.media_detail.aria_more_short")}>
                <MoreOutlined />
              </button>
            </Dropdown>
          </div>
          <button type="button" className={styles.relatedBulkCancel} onClick={clearRelatedSelect}>
            <CloseOutlined aria-hidden />
            <span>{t("pages.media_detail.deselect_all")}</span>
          </button>
        </div>
      ) : null}
      <div className={styles.topBar}>
        <Button type="text" icon={<ArrowLeftOutlined />} onClick={() => nav(-1)} className={styles.backBtn}>
          {t("pages.media_detail.back")}
        </Button>
      </div>

      <section className={styles.block}>
        <div className={styles.introCard}>
          <div className={styles.introInner}>
            <div className={styles.posterColumn}>
              <button
                type="button"
                className={styles.posterPlayHit}
                onClick={() => nav(showResumeActions ? resumeTarget : playFromStartTarget)}
                aria-label={showResumeActions ? t("pages.media_detail.aria_continue_play") : t("pages.media_detail.aria_play_short")}
              >
                <div className={styles.poster}>
                  {posterUrl ? (
                    <img
                      src={posterUrl}
                      alt=""
                      className={styles.posterImg}
                      onError={() => setBrokenImages((prev) => ({ ...prev, poster: true }))}
                    />
                  ) : (
                    <div className={styles.posterFallback}>
                      <FileImageOutlined />
                      <span>{t("pages.media_detail.no_poster")}</span>
                    </div>
                  )}
                  <div className={styles.posterHoverOverlay} aria-hidden>
                    <div className={styles.posterHoverPlayBtn}>
                      <span className={styles.posterHoverPlayTriangle} />
                    </div>
                  </div>
                </div>
              </button>
              {isCompleted ? (
                <div className={styles.posterWatchedBar} role="status" aria-label={t("pages.media_detail.aria_watched")}>
                  <CheckOutlined className={styles.posterWatchedIcon} />
                  <span>{t("pages.media_detail.watched_label")}</span>
                </div>
              ) : null}
            </div>
            <div className={styles.introMain}>
              <Typography.Title level={2} className={styles.title}>
                {detail.title || t("pages.media_detail.untitled_movie")}
              </Typography.Title>
              <div className={styles.directorLine}>
                <TeamOutlined /> {t("pages.media_detail.director_label_dot", {
                  names: meta.director?.length ? meta.director.join("、") : t("pages.media_detail.director_none"),
                })}
              </div>
              <div className={styles.metaRow}>
                <span>{yearStr !== "—" ? t("pages.media_detail.year_unit", { year: yearStr }) : "—"}</span>
                <span className={styles.metaDot}>·</span>
                <span>{runtimeZh}</span>
                {meta.certification ? (
                  <>
                    <span className={styles.metaDot}>·</span>
                    <span>{meta.certification}</span>
                  </>
                ) : null}
              </div>
              {meta.genres.length > 0 ? (
                <div className={styles.genreRow}>
                  {meta.genres.map((g) => (
                    <Tag key={g} className={styles.genreTag}>
                      {g}
                    </Tag>
                  ))}
                </div>
              ) : null}
              <div className={styles.scoreRow}>
                {scoreText ? (
                  <span className={styles.criticScore}>{t("pages.media_detail.score_label", { score: scoreText })}</span>
                ) : (
                  <span className={styles.criticScoreMuted}>{t("pages.media_detail.no_score")}</span>
                )}
                <span className={styles.rateLabel}>{t("pages.media_detail.my_rating")}</span>
                <Rate
                  allowHalf
                  value={userStars}
                  onChange={(v) => {
                    setUserStars(v);
                    try {
                      localStorage.setItem(LS_STARS_KEY(mediaId), String(v));
                    } catch {
                      /* ignore */
                    }
                  }}
                  className={styles.starRate}
                />
              </div>
              <div className={styles.actionBar}>
                {showResumeActions ? (
                  <Button
                    type="primary"
                    size="large"
                    icon={<ToolbarPlayIcon className={styles.mediaDetailPlaySvg} />}
                    className={styles.playBtn}
                    onClick={() => nav(resumeTarget)}
                  >
                    {t("pages.media_detail.btn_continue")}
                  </Button>
                ) : (
                  <Button
                    type="primary"
                    size="large"
                    icon={<ToolbarPlayIcon className={styles.mediaDetailPlaySvg} />}
                    className={styles.playBtn}
                    onClick={() => nav(playFromStartTarget)}
                  >
                    {t("pages.media_detail.btn_play")}
                  </Button>
                )}
                <Tooltip title={favorited ? t("pages.media_detail.tooltip_unfavorite") : t("pages.media_detail.tooltip_favorite")} placement="top">
                  <Button
                    type="default"
                    shape="circle"
                    size="large"
                    icon={favorited ? <StarFilled /> : <StarOutlined />}
                    aria-label={favorited ? t("pages.media_detail.tooltip_unfavorite") : t("pages.media_detail.tooltip_favorite")}
                    className={`${styles.iconAction} ${styles.iconActionCircle} ${styles.iconActionFavorite}${
                      favorited ? ` ${styles.iconActionFavorited}` : ""
                    }`}
                    onClick={() => void onToggleFavorite()}
                  />
                </Tooltip>
                <Tooltip title={isCompleted ? t("pages.media_detail.tooltip_mark_unwatched") : t("pages.media_detail.tooltip_mark_watched")} placement="top">
                  <Button
                    shape="circle"
                    size="large"
                    icon={
                      isCompleted ? (
                        <WatchedSolidCutoutIcon className={styles.iconWatchCutout} />
                      ) : (
                        <CheckCircleOutlined />
                      )
                    }
                    aria-label={isCompleted ? t("pages.media_detail.tooltip_mark_unwatched") : t("pages.media_detail.tooltip_mark_watched")}
                    loading={markBusy}
                    onClick={() => void onToggleWatched()}
                    className={`${styles.iconAction} ${styles.iconActionCircle} ${styles.iconActionWatch}`}
                  />
                </Tooltip>
                <Tooltip title={t("pages.media_detail.tooltip_edit")} placement="top">
                  <Button
                    shape="circle"
                    size="large"
                    icon={<EditOutlined />}
                    aria-label={t("pages.media_detail.aria_edit")}
                    onClick={onEditClick}
                    className={`${styles.iconAction} ${styles.iconActionCircle}`}
                  />
                </Tooltip>
                <Dropdown menu={mediaActionMenu} trigger={["click"]}>
                  <Button
                    shape="circle"
                    size="large"
                    icon={<MoreOutlined />}
                    aria-label={t("pages.media_detail.aria_more")}
                    className={`${styles.iconAction} ${styles.iconActionCircle}`}
                  />
                </Dropdown>
              </div>
              {detail.original_title ? (
                <Typography.Text type="secondary" className={styles.originalTitle}>
                  {detail.original_title}
                </Typography.Text>
              ) : null}
              <div className={styles.overviewBlock}>
                <Typography.Paragraph className={styles.overviewText}>{overviewShown}</Typography.Paragraph>
                {overviewLong ? (
                  <Button type="link" size="small" className={styles.expandLink} onClick={() => setOverviewOpen(!overviewOpen)}>
                    {overviewOpen ? t("pages.media_detail.collapse") : t("pages.media_detail.expand")}
                  </Button>
                ) : null}
              </div>
              <div className={styles.streamPickers}>
                <span className={styles.streamKey}>
                  <VideoCameraOutlined /> {t("pages.media_detail.section_video")}
                </span>
                <div className={styles.streamValueCell}>
                  <span className={styles.streamVal}>{videoFormatLine}</span>
                </div>
                <span className={styles.streamKey}>
                  <SoundOutlined /> {t("pages.media_detail.section_audio")}
                </span>
                <div className={styles.streamValueCell}>
                  <Select
                    variant="borderless"
                    size="middle"
                    className={styles.streamSelect}
                    styles={{ root: { padding: 0, margin: 0 }, content: { padding: 0, margin: 0 } }}
                    disabled={audioOptions.length === 0}
                    value={audioOptions.length ? safeAudioIdx : undefined}
                    placeholder={audioOptions.length ? undefined : t("pages.media_detail.no_audio_tracks")}
                    options={audioOptions.map((track) => ({ value: track.index, label: audioTrackLabel(track) }))}
                    onChange={(v) => {
                      setSelectedAudioIdx(v);
                      try {
                        localStorage.setItem(LS_AUDIO_KEY(mediaId), String(v));
                      } catch {
                        /* ignore */
                      }
                    }}
                  />
                </div>
                <span className={styles.streamKey}>
                  <TranslationOutlined /> {t("pages.media_detail.section_subtitle")}
                </span>
                <div className={styles.streamValueCell}>
                  <Select
                    variant="borderless"
                    size="middle"
                    className={styles.streamSelect}
                    styles={{ root: { padding: 0, margin: 0 }, content: { padding: 0, margin: 0 } }}
                    value={subtitleSelectValue}
                    options={[
                      { value: "off", label: t("pages.media_detail.subtitle_off") },
                      ...subtitleRows.map((r) => ({ value: String(r.id), label: subtitleRowLabel(r) })),
                      ...(meta.subtitleCodecs.length && subtitleRows.length === 0
                        ? meta.subtitleCodecs.map((c, i) => ({
                            value: `embedded-${i}`,
                            label: t("pages.media_detail.subtitle_embedded_prefix", { codec: c }),
                          }))
                        : []),
                    ]}
                    onChange={(v) => {
                      setSelectedSubId(v);
                      try {
                        localStorage.setItem(LS_SUB_KEY(mediaId), v);
                      } catch {
                        /* ignore */
                      }
                    }}
                  />
                </div>
              </div>
            </div>
          </div>
        </div>
      </section>

      {/* Playback statistics */}
      <section className={styles.block} ref={fileInfoRef}>
        <Typography.Title level={4} className={styles.blockTitle}>
          {t("pages.media_detail.stats_section_title")}
        </Typography.Title>
        <div className={styles.statsPanel}>
          <div className={styles.stats}>
            <Statistic title={t("pages.media_detail.stat_watch_users")} value={stats?.watch_users ?? 0} />
            <Statistic title={t("pages.media_detail.stat_avg_duration")} value={fmtSeconds(Math.round(stats?.avg_position_seconds ?? 0))} />
            <Statistic title={t("pages.media_detail.stat_latest_watch")} value={formatServerDateTime(stats?.latest_watch_at)} />
          </div>
          <div className={styles.progressBox}>
            <div className={styles.specLabel}>{t("pages.media_detail.stat_avg_progress")}</div>
            <Progress percent={avgPct} strokeColor="#ed6d00" trailColor="rgba(255,255,255,0.12)" />
          </div>
          <div className={styles.progressBox}>
            <div className={styles.specLabel}>{t("pages.media_detail.stat_my_history")}</div>
            <div className={styles.historyTags}>
              <Tag color={historyItem?.completed === 1 ? "green" : "default"}>
                {historyItem?.completed === 1 ? t("pages.media_detail.history_completed") : t("pages.media_detail.history_uncompleted")}
              </Tag>
              <Tag color="blue">{t("pages.media_detail.history_last_pos", { pos: fmtSeconds(historyItem?.position ?? 0) })}</Tag>
              <Tag color="cyan">{t("pages.media_detail.history_play_count", { count: historyItem?.play_count ?? 0 })}</Tag>
              <Tag>{t("pages.media_detail.history_start", { time: formatServerDateTime(historyItem?.play_start_at) })}</Tag>
              <Tag>{t("pages.media_detail.history_end", { time: formatServerDateTime(historyItem?.play_end_at) })}</Tag>
            </div>
          </div>
        </div>
      </section>

      <MediaHorizontalShelf
        title={t("pages.media_detail.section_cast_crew")}
        empty={<div className={styles.empty}>{t("pages.media_detail.no_cast")}</div>}
        trackClassName={styles.castRow}
        hasContent={castList.length > 0}
        refreshKey={castList.map((c) => c.name).join("\n")}
      >
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
              <div className={styles.castAvatarEmpty}>{(member.name || "?").slice(0, 1).toUpperCase()}</div>
            )}
            <div className={styles.castName}>{member.name}</div>
            <div className={styles.castRole}>{member.role || t("pages.media_detail.role_actor")}</div>
          </div>
        ))}
      </MediaHorizontalShelf>

      <MediaHorizontalShelf
        title={isTVLibraryType(libraryType) ? t("pages.media_detail.section_related_tv") : t("pages.media_detail.section_related_movie")}
        empty={<div className={styles.empty}>{t("pages.media_detail.no_related")}</div>}
        trackClassName={styles.relatedRow}
        hasContent={related.length > 0}
        refreshKey={related.map((r) => r.id).join(",")}
      >
        {related.map((m) => (
          <RelatedMovieCard
            key={m.id}
            m={m}
            nav={nav}
            selected={relatedSelectedIds.includes(m.id)}
            onToggleSelect={() => toggleRelatedSelect(m.id)}
            posterBroken={!!brokenImages[`rel-${m.id}`]}
            onPosterError={() => setBrokenImages((prev) => ({ ...prev, [`rel-${m.id}`]: true }))}
            bulkSelectMode={relatedSelectedIds.length > 0}
          />
        ))}
      </MediaHorizontalShelf>

      {detail && playlistModalOpen ? (
        <AddToPlaylistModal
          mediaIds={[detail.id]}
          open
          onClose={() => setPlaylistModalOpen(false)}
          onAdded={(pl) => {
            rememberPlaylistAdded(pl);
            setRecentPlaylistMenu(readRecentPlaylists());
          }}
        />
      ) : null}
      {detail && favoriteFolderModalOpen ? (
        <AddToFavoriteFolderPickerModal
          mediaId={detail.id}
          open
          onClose={() => setFavoriteFolderModalOpen(false)}
          onAdded={(folder) => rememberFolderMenuAdded(folder)}
        />
      ) : null}
      {detail ? (
        <MediaMatchModal
          media={detail}
          fixMatch={Boolean(detail.scraped)}
          open={matchModalOpen}
          onClose={() => setMatchModalOpen(false)}
          onMatched={applyMatchUpdate}
        />
      ) : null}
    </div>
  );
}
