import {
  ArrowLeftOutlined,
  EditOutlined,
  FileImageOutlined,
  MoreOutlined,
  StarFilled,
  StarOutlined,
  TeamOutlined,
} from "@ant-design/icons";
import type { MenuProps } from "antd";
import { Button, Dropdown, Empty, Spin, Tabs, Tag, Tooltip, Typography, message } from "antd";
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { useNavigate, useParams, useSearchParams } from "react-router-dom";
import {
  EpisodeRow,
  MediaDetail,
  SeasonSummary,
  SeriesDetail,
  SeriesSummary,
  addFavorite,
  addPlaylistItem,
  fetchFavoriteStatus,
  fetchMediaDetail,
  fetchSeasonEpisodes,
  fetchSeries,
  fetchSeriesPlayTarget,
  normalizeListPosterUrl,
  removeFavorite,
  seriesPosterSrc,
  type MediaMatchListUpdate,
} from "../api/client";
import AddToPlaylistModal from "../components/AddToPlaylistModal";
import MediaMatchModal from "../components/MediaMatchModal";
import { buildSeriesMenuItems } from "../components/seriesMenuItems";
import SeriesEditModal from "../components/SeriesEditModal";
import ToolbarPlayIcon from "../components/ToolbarPlayIcon";
import { readRecentPlaylists, rememberPlaylistAdded } from "../lib/recentPlaylists";
import {
  fetchSeriesEpisodeMediaOrder,
  pickPrimaryEpisodeMediaId,
} from "../lib/seriesEpisodeOrder";
import { storeSeriesPlaySession } from "../lib/seriesPlayback";
import { tGlobal, useT } from "../i18n";
import md from "./MediaDetail.module.css";
import styles from "./SeriesDetail.module.css";

function fmtDuration(sec?: number): string {
  if (!sec || sec <= 0) return "—";
  const h = Math.floor(sec / 3600);
  const m = Math.floor((sec % 3600) / 60);
  if (h > 0) return tGlobal("pages.series_detail.fmt_h_m", { h, m });
  return tGlobal("pages.series_detail.fmt_m", { m });
}

function formatMetaRating(r?: number) {
  if (r == null || Number.isNaN(r) || r <= 0) return null;
  if (r <= 10) return `${r.toFixed(1)}/10`;
  return `${Math.round(r)}%`;
}

function stringMetaField(v: unknown): string {
  if (typeof v === "string") return v.trim();
  if (Array.isArray(v)) {
    return v
      .map((x) => (typeof x === "string" ? x.trim() : ""))
      .filter(Boolean)
      .join("、");
  }
  return "";
}

function mergeSeriesMetaOverview(metaJSON: string | undefined, overview: string): string {
  let raw: Record<string, unknown> = {};
  if (metaJSON) {
    try {
      raw = JSON.parse(metaJSON) as Record<string, unknown>;
    } catch {
      raw = {};
    }
  }
  const scrape = ((raw.scrape as Record<string, unknown>) || {});
  scrape.overview = overview;
  raw.scrape = scrape;
  return JSON.stringify(raw);
}

function parseSeriesMeta(metaJSON?: string): {
  overview?: string;
  poster?: string;
  backdrop?: string;
  rating?: number;
  genres: string[];
  director: string[];
  cast: Array<{ name: string; role?: string; avatar?: string }>;
  country?: string;
  releaseDate?: string;
} {
  const empty = { genres: [] as string[], director: [] as string[], cast: [] as Array<{ name: string; role?: string; avatar?: string }> };
  if (!metaJSON) return empty;
  try {
    const root = JSON.parse(metaJSON) as { scrape?: Record<string, unknown> };
    const scrape = root.scrape;
    if (!scrape || typeof scrape !== "object") return empty;
    const extra = (scrape.extra as Record<string, unknown>) || {};
    const genresRaw = scrape.genres ?? extra.genres;
    const genres = Array.isArray(genresRaw)
      ? genresRaw.filter((x): x is string => typeof x === "string" && x.trim().length > 0)
      : [];
    const directorRaw = extra.director ?? extra.directors ?? extra.crew;
    const director = Array.isArray(directorRaw)
      ? directorRaw.filter((x): x is string => typeof x === "string" && x.trim().length > 0)
      : typeof directorRaw === "string" && directorRaw.trim()
        ? [directorRaw.trim()]
        : [];
    const actorsRaw = (extra.cast ?? extra.actors) as Array<Record<string, unknown>> | undefined;
    const cast = Array.isArray(actorsRaw)
      ? actorsRaw
          .map((x) => ({
            name: String(x.name || x.actor || "").trim(),
            role: x.role ? String(x.role) : x.character ? String(x.character) : "",
            avatar: stringMetaField(x.profile_path || x.avatar || x.image),
          }))
          .filter((x) => x.name.length > 0)
      : [];
    const country =
      stringMetaField(extra.country) ||
      stringMetaField(extra.origin) ||
      stringMetaField(extra.production_countries);
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
      releaseDate: typeof scrape.release_date === "string" ? scrape.release_date : undefined,
      genres,
      director,
      cast,
      country: country || undefined,
    };
  } catch {
    return empty;
  }
}

function pickPrimaryMediaId(ep: EpisodeRow): number | null {
  return pickPrimaryEpisodeMediaId(ep);
}

function episodeContainsMedia(ep: EpisodeRow, mediaId: number): boolean {
  return (ep.versions ?? []).some((v) => v.media_id === mediaId);
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
  const [searchParams] = useSearchParams();
  const playingMediaId = Number(searchParams.get("current_media_id"));
  const playingRowRef = useRef<HTMLDivElement | null>(null);
  const [detail, setDetail] = useState<SeriesDetail | null>(null);
  const [loading, setLoading] = useState(true);
  const [activeSeasonId, setActiveSeasonId] = useState<number | null>(null);
  const [episodes, setEpisodes] = useState<EpisodeRow[]>([]);
  const [epLoading, setEpLoading] = useState(false);
  const [playTarget, setPlayTarget] = useState<{ media_id: number; position: number } | null>(null);
  const [playBusy, setPlayBusy] = useState(false);
  const [overviewOpen, setOverviewOpen] = useState(false);
  const [posterBroken, setPosterBroken] = useState(false);
  const [editOpen, setEditOpen] = useState(false);
  const [favorited, setFavorited] = useState(false);
  const [allMediaIds, setAllMediaIds] = useState<number[]>([]);
  const [episodeOrder, setEpisodeOrder] = useState<number[]>([]);
  const [representativeMedia, setRepresentativeMedia] = useState<MediaDetail | null>(null);
  const [matchModalOpen, setMatchModalOpen] = useState(false);
  const [playlistModalOpen, setPlaylistModalOpen] = useState(false);
  const [recentPlaylistMenu, setRecentPlaylistMenu] = useState(readRecentPlaylists);
  const [brokenImages, setBrokenImages] = useState<Record<string, true>>({});

  const t = useT();

  const reloadSeries = useCallback(async () => {
    if (!seriesId || Number.isNaN(seriesId)) return;
    try {
      const data = await fetchSeries(seriesId);
      setDetail(data);
      setPosterBroken(false);
    } catch {
      message.error(t("pages.series_detail.refresh_failed"));
    }
  }, [seriesId, t]);

  useEffect(() => {
    if (!seriesId || Number.isNaN(seriesId)) return;
    let cancelled = false;
    (async () => {
      setLoading(true);
      try {
        const data = await fetchSeries(seriesId);
        if (cancelled) return;
        setDetail(data);
        setPosterBroken(false);
        const seasons = data.seasons ?? [];
        if (seasons.length > 0) {
          setActiveSeasonId(seasons[0].id);
        }
      } catch (e: unknown) {
        if (!cancelled) message.error((e as Error).message || t("pages.series_detail.load_failed"));
      } finally {
        if (!cancelled) setLoading(false);
      }
    })();
    return () => {
      cancelled = true;
    };
  }, [seriesId, t]);

  useEffect(() => {
    if (!seriesId || Number.isNaN(seriesId)) return;
    let cancelled = false;
    void fetchSeriesPlayTarget(seriesId)
      .then((target) => {
        if (!cancelled) setPlayTarget(target);
      })
      .catch(() => {
        if (!cancelled) setPlayTarget(null);
      });
    return () => {
      cancelled = true;
    };
  }, [seriesId]);

  useEffect(() => {
    const seasons = detail?.seasons ?? [];
    if (seasons.length === 0) {
      setAllMediaIds([]);
      setEpisodeOrder([]);
      return;
    }
    let cancelled = false;
    void (async () => {
      const order = await fetchSeriesEpisodeMediaOrder(seasons);
      if (!cancelled) {
        setEpisodeOrder(order);
        setAllMediaIds(order);
      }
    })();
    return () => {
      cancelled = true;
    };
  }, [detail?.seasons]);

  useEffect(() => {
    const mid = playTarget?.media_id ?? allMediaIds[0];
    if (!mid) {
      setRepresentativeMedia(null);
      setFavorited(false);
      return;
    }
    let cancelled = false;
    void Promise.allSettled([fetchMediaDetail(mid), fetchFavoriteStatus(mid)]).then(([d, fav]) => {
      if (cancelled) return;
      setRepresentativeMedia(d.status === "fulfilled" ? d.value : null);
      setFavorited(fav.status === "fulfilled" ? fav.value : false);
    });
    return () => {
      cancelled = true;
    };
  }, [playTarget, allMediaIds]);

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
        if (!cancelled) message.error((e as Error).message || t("pages.series_detail.load_episodes_failed"));
      } finally {
        if (!cancelled) setEpLoading(false);
      }
    })();
    return () => {
      cancelled = true;
    };
  }, [activeSeasonId]);

  useEffect(() => {
    const seasons = detail?.seasons ?? [];
    if (!Number.isFinite(playingMediaId) || playingMediaId <= 0 || seasons.length === 0) return;
    let cancelled = false;
    void (async () => {
      for (const season of seasons) {
        try {
          const items = await fetchSeasonEpisodes(season.id);
          if (items.some((ep) => episodeContainsMedia(ep, playingMediaId))) {
            if (!cancelled) setActiveSeasonId(season.id);
            break;
          }
        } catch {
          /* ignore season load errors */
        }
      }
    })();
    return () => {
      cancelled = true;
    };
  }, [playingMediaId, detail?.seasons]);

  useEffect(() => {
    if (!Number.isFinite(playingMediaId) || playingMediaId <= 0) return;
    playingRowRef.current?.scrollIntoView({ block: "nearest", behavior: "smooth" });
  }, [playingMediaId, episodes, activeSeasonId]);

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
  const totalEpisodes = useMemo(
    () => seasons.reduce((sum, s) => sum + (s.episode_count ?? 0), 0),
    [seasons],
  );
  const overview = meta.overview || t("pages.series_detail.no_overview");
  const overviewPreviewLen = 220;
  const overviewLong = overview.length > overviewPreviewLen;
  const overviewShown =
    overviewOpen || !overviewLong ? overview : `${overview.slice(0, overviewPreviewLen)}…`;
  const scoreText = formatMetaRating(meta.rating);
  const canResume = (playTarget?.position ?? 0) > 0;
  const castList = meta.cast.slice(0, 24);
  const yearStr =
    detail?.year && detail.year > 0
      ? String(detail.year)
      : meta.releaseDate && meta.releaseDate.length >= 4
        ? meta.releaseDate.slice(0, 4)
        : "—";

  function playEpisode(mediaId: number, orderOverride?: number[]) {
    const order = orderOverride ?? episodeOrder;
    if (order.length === 0 || !seriesId || Number.isNaN(seriesId)) {
      nav(`/player/${mediaId}`);
      return;
    }
    const index = order.indexOf(mediaId);
    storeSeriesPlaySession(seriesId, order);
    const idx = index >= 0 ? index : 0;
    nav(`/player/${mediaId}?series_id=${seriesId}&index=${idx}`);
  }

  async function handleSeriesPlay() {
    if (playBusy) return;
    setPlayBusy(true);
    try {
      const target = playTarget ?? (await fetchSeriesPlayTarget(seriesId));
      setPlayTarget(target);
      const order =
        episodeOrder.length > 0
          ? [...episodeOrder]
          : await fetchSeriesEpisodeMediaOrder(detail?.seasons ?? []);
      if (order.length > 0) setEpisodeOrder(order);
      const index = order.indexOf(target.media_id);
      storeSeriesPlaySession(seriesId, order);
      const pos = target.position > 0 ? `&t=${target.position}` : "";
      const idx = index >= 0 ? index : 0;
      nav(`/player/${target.media_id}?series_id=${seriesId}&index=${idx}${pos}`);
    } catch (e: unknown) {
      message.error((e as Error).message || t("pages.series_detail.cannot_play"));
    } finally {
      setPlayBusy(false);
    }
  }

  async function onToggleFavorite() {
    const mid = playTarget?.media_id ?? allMediaIds[0];
    if (!mid) {
      message.warning(t("pages.series_detail.no_favorite_target"));
      return;
    }
    try {
      if (favorited) {
        await removeFavorite(mid);
        setFavorited(false);
        message.success(t("pages.series_detail.unfavorited"));
      } else {
        await addFavorite(mid);
        setFavorited(true);
        message.success(t("pages.series_detail.favorited"));
      }
    } catch {
      message.error(t("pages.series_detail.favorite_failed"));
    }
  }

  function applySeriesUpdate(update: Partial<SeriesSummary> & { id: number; overview?: string }) {
    setDetail((prev) => {
      if (!prev || prev.id !== update.id) return prev;
      const next: SeriesDetail = {
        ...prev,
        title: update.title ?? prev.title,
        year: update.year ?? prev.year,
        poster: update.poster ?? prev.poster,
        poster_url: update.poster_url ?? update.poster ?? prev.poster_url,
      };
      if (update.overview !== undefined) {
        next.meta_json = mergeSeriesMetaOverview(prev.meta_json, update.overview);
      }
      return next;
    });
    if (update.overview !== undefined) {
      setOverviewOpen(false);
    }
    setPosterBroken(false);
  }

  const applyMatchUpdate = useCallback(
    (update: MediaMatchListUpdate) => {
      if (update.poster_url) {
        setDetail((prev) =>
          prev
            ? {
                ...prev,
                poster: update.poster_url,
                poster_url: update.poster_url,
              }
            : prev,
        );
        setPosterBroken(false);
      }
      void reloadSeries();
      const mid = playTarget?.media_id ?? allMediaIds[0];
      if (mid) {
        void fetchMediaDetail(mid).then(setRepresentativeMedia).catch(() => {});
      }
    },
    [reloadSeries, playTarget, allMediaIds],
  );

  const seriesMenu = useMemo((): MenuProps => {
    return buildSeriesMenuItems({
      scraped: Boolean(representativeMedia?.scraped),
      allMediaIds,
      onAddToPlaylist: () => setPlaylistModalOpen(true),
      onOpenMatch: () => setMatchModalOpen(true),
      afterUnmatch: () => void reloadSeries(),
      afterDelete: () => nav(`/browse?library_id=${detail?.library_id ?? ""}`),
      recentPlaylists: recentPlaylistMenu,
      onQuickAddToPlaylist: async (mediaIds, playlistId) => {
        try {
          for (const mid of mediaIds) {
            await addPlaylistItem(playlistId, mid);
          }
          const name =
            recentPlaylistMenu.find((p) => p.id === playlistId)?.name ??
            readRecentPlaylists().find((p) => p.id === playlistId)?.name ??
            t("pages.series_detail.playlist_fallback");
          message.success(t("pages.series_detail.added_to_playlist", { count: mediaIds.length, name }));
          rememberPlaylistAdded({ id: playlistId, name });
          setRecentPlaylistMenu(readRecentPlaylists());
        } catch {
          message.error(t("pages.series_detail.add_failed"));
        }
      },
    });
  }, [allMediaIds, representativeMedia?.scraped, detail?.library_id, recentPlaylistMenu, reloadSeries, nav, t]);

  if (loading) {
    return (
      <div className={md.loadingWrap}>
        <Spin size="large" />
      </div>
    );
  }

  if (!detail) {
    return <Empty description={t("pages.series_detail.not_found")} />;
  }

  const posterUrl = heroPoster && !posterBroken ? heroPoster : "";
  const editSeries: SeriesSummary = {
    id: detail.id,
    library_id: detail.library_id,
    title: detail.title,
    title_norm: detail.title_norm,
    year: detail.year,
    poster: detail.poster,
    poster_url: detail.poster_url ?? detail.poster,
    season_count: seasons.length,
    episode_count: totalEpisodes,
  };
  const matchMedia = representativeMedia ?? {
    id: playTarget?.media_id ?? allMediaIds[0] ?? 0,
    title: detail.title,
    year: detail.year,
    file_path: "",
  };

  return (
    <div className={md.page}>
      <div className={md.topBar}>
        <Button type="text" icon={<ArrowLeftOutlined />} onClick={() => nav(-1)} className={md.backBtn}>
          {t("pages.series_detail.back")}
        </Button>
      </div>

      <section className={md.block}>
        <div className={md.introCard}>
          <div className={md.introInner}>
            <div className={md.posterColumn}>
              <button
                type="button"
                className={md.posterPlayHit}
                onClick={() => void handleSeriesPlay()}
                disabled={playBusy}
                aria-label={canResume ? t("pages.series_detail.aria_continue") : t("pages.series_detail.aria_play")}
              >
                <div className={md.poster}>
                  {posterUrl ? (
                    <img
                      src={posterUrl}
                      alt=""
                      className={md.posterImg}
                      onError={() => setPosterBroken(true)}
                    />
                  ) : (
                    <div className={md.posterFallback}>
                      <FileImageOutlined />
                      <span>{t("pages.series_detail.no_poster")}</span>
                    </div>
                  )}
                  <div className={md.posterHoverOverlay} aria-hidden>
                    <div className={md.posterHoverPlayBtn}>
                      <span className={md.posterHoverPlayTriangle} />
                    </div>
                  </div>
                </div>
              </button>
            </div>
            <div className={md.introMain}>
              <Typography.Title level={2} className={md.title}>
                {detail.title || t("pages.series_detail.untitled_series")}
              </Typography.Title>
              <div className={md.directorLine}>
                <TeamOutlined /> {t("pages.series_detail.director_label", {
                  names: meta.director.length ? meta.director.join("、") : t("pages.series_detail.director_none"),
                })}
              </div>
              <div className={md.metaRow}>
                <span>{yearStr !== "—" ? t("pages.series_detail.year_unit", { year: yearStr }) : "—"}</span>
                <span className={md.metaDot}>·</span>
                <span>{t("pages.series_detail.season_count", { count: seasons.length })}</span>
                {totalEpisodes > 0 ? (
                  <>
                    <span className={md.metaDot}>·</span>
                    <span>{t("pages.series_detail.episodes_count", { count: totalEpisodes })}</span>
                  </>
                ) : null}
                {meta.country ? (
                  <>
                    <span className={md.metaDot}>·</span>
                    <span>{meta.country}</span>
                  </>
                ) : null}
              </div>
              {meta.genres.length > 0 ? (
                <div className={md.genreRow}>
                  {meta.genres.map((g) => (
                    <Tag key={g} className={md.genreTag}>
                      {g}
                    </Tag>
                  ))}
                </div>
              ) : null}
              {scoreText ? (
                <div className={md.scoreRow}>
                  <span className={md.criticScore}>{t("pages.series_detail.score_label", { score: scoreText })}</span>
                </div>
              ) : null}
              <div className={md.actionBar}>
                <Button
                  type="primary"
                  size="large"
                  icon={<ToolbarPlayIcon className={md.mediaDetailPlaySvg} />}
                  className={md.playBtn}
                  loading={playBusy}
                  onClick={() => void handleSeriesPlay()}
                >
                  {canResume ? t("pages.series_detail.btn_continue") : t("pages.series_detail.btn_play")}
                </Button>
                <Tooltip title={favorited ? t("pages.series_detail.tooltip_unfavorite") : t("pages.series_detail.tooltip_favorite")} placement="top">
                  <Button
                    type="default"
                    shape="circle"
                    size="large"
                    icon={favorited ? <StarFilled /> : <StarOutlined />}
                    aria-label={favorited ? t("pages.series_detail.tooltip_unfavorite") : t("pages.series_detail.tooltip_favorite")}
                    className={`${md.iconAction} ${md.iconActionCircle} ${md.iconActionFavorite}${
                      favorited ? ` ${md.iconActionFavorited}` : ""
                    }`}
                    onClick={() => void onToggleFavorite()}
                  />
                </Tooltip>
                <Tooltip title={t("pages.series_detail.tooltip_edit")} placement="top">
                  <Button
                    shape="circle"
                    size="large"
                    icon={<EditOutlined />}
                    aria-label={t("pages.series_detail.aria_edit")}
                    onClick={() => setEditOpen(true)}
                    className={`${md.iconAction} ${md.iconActionCircle}`}
                  />
                </Tooltip>
                <Dropdown menu={seriesMenu} trigger={["click"]}>
                  <Button
                    shape="circle"
                    size="large"
                    icon={<MoreOutlined />}
                    aria-label={t("pages.series_detail.aria_more")}
                    className={`${md.iconAction} ${md.iconActionCircle}`}
                  />
                </Dropdown>
              </div>
              <div className={md.overviewBlock}>
                <Typography.Paragraph className={md.overviewText}>{overviewShown}</Typography.Paragraph>
                {overviewLong ? (
                  <Button
                    type="link"
                    size="small"
                    className={md.expandLink}
                    onClick={() => setOverviewOpen(!overviewOpen)}
                  >
                    {overviewOpen ? t("pages.series_detail.collapse") : t("pages.series_detail.expand")}
                  </Button>
                ) : null}
              </div>
            </div>
          </div>
        </div>
      </section>

      {castList.length > 0 ? (
        <section className={md.block}>
          <Typography.Title level={4} className={md.blockTitle}>
            {t("pages.series_detail.section_cast")}
          </Typography.Title>
          <div className={md.shelfTrack}>
            <div className={md.castRow}>
              {castList.map((member, idx) => (
                <div key={`${member.name}-${idx}`} className={md.castCard}>
                  {member.avatar && !brokenImages[`actor-${idx}`] ? (
                    <img
                      src={member.avatar}
                      alt={member.name}
                      className={md.castAvatarImage}
                      onError={() => setBrokenImages((prev) => ({ ...prev, [`actor-${idx}`]: true }))}
                    />
                  ) : (
                    <div className={md.castAvatarEmpty}>{(member.name || "?").slice(0, 1).toUpperCase()}</div>
                  )}
                  <div className={md.castName}>{member.name}</div>
                  <div className={md.castRole}>{member.role || t("pages.series_detail.role_actor")}</div>
                </div>
              ))}
            </div>
          </div>
        </section>
      ) : null}

      <section className={md.block}>
        <Typography.Title level={4} className={md.blockTitle}>
          {t("pages.series_detail.section_episodes")}
        </Typography.Title>
        {seasons.length === 0 ? (
          <Empty description={t("pages.series_detail.no_seasons")} />
        ) : (
          <>
            <Tabs
              className={styles.seasonTabs}
              activeKey={String(activeSeasonId ?? seasons[0]?.id)}
              onChange={(key) => setActiveSeasonId(Number(key))}
              items={seasons.map((s: SeasonSummary) => ({
                key: String(s.id),
                label: s.name || t("pages.series_detail.season_default_name", { n: s.season_num }),
              }))}
            />
            {epLoading ? (
              <div className={styles.epLoading}>
                <Spin />
              </div>
            ) : episodes.length === 0 ? (
              <Empty description={t("pages.series_detail.no_episodes_in_season")} />
            ) : (
              <div className={styles.episodeList}>
                {episodes.map((ep) => {
                  const best = pickBestVersion(ep);
                  const mediaId = pickPrimaryMediaId(ep);
                  const isPlaying =
                    Number.isFinite(playingMediaId) &&
                    playingMediaId > 0 &&
                    episodeContainsMedia(ep, playingMediaId);
                  const epLabel = `E${String(ep.episode_num).padStart(2, "0")}`;
                  const versionCount = ep.versions?.length ?? 0;
                  const watched = (ep.versions ?? []).some((v) => v.completed === 1);
                  return (
                    <div
                      key={ep.id}
                      ref={isPlaying ? playingRowRef : undefined}
                      className={styles.episodeRow}
                      data-playing={isPlaying ? "" : undefined}
                    >
                      <div className={styles.episodeNum}>
                        {isPlaying ? <ToolbarPlayIcon className={styles.episodePlayingIcon} /> : epLabel}
                      </div>
                      <div className={styles.episodeMain}>
                        {mediaId ? (
                          <button
                            type="button"
                            className={styles.episodeTitle}
                            onClick={() => nav(`/detail/${mediaId}`)}
                          >
                            {ep.title || t("pages.series_detail.episode_default_title", { n: ep.episode_num })}
                            {isPlaying ? <span className={styles.episodePlayingLabel}>{t("pages.series_detail.now_playing_label")}</span> : null}
                          </button>
                        ) : (
                          <div className={styles.episodeTitleStatic}>
                            {ep.title || t("pages.series_detail.episode_default_title", { n: ep.episode_num })}
                          </div>
                        )}
                        <div className={styles.episodeMeta}>
                          {fmtDuration(best?.duration ?? ep.duration)}
                          {best?.width && best?.height ? ` · ${best.width}×${best.height}` : ""}
                          {versionCount > 1 ? t("pages.series_detail.versions_suffix", { count: versionCount }) : ""}
                          {watched ? ` · ${t("pages.media_detail.watched_label")}` : ""}
                        </div>
                        {versionCount > 1 ? (
                          <div className={styles.versionList}>
                            {ep.versions!.map((v) => (
                              <Button
                                key={v.media_id}
                                size="small"
                                type="link"
                                onClick={() => {
                                  const primaryId = pickPrimaryMediaId(ep);
                                  const slot = primaryId != null ? episodeOrder.indexOf(primaryId) : -1;
                                  const order =
                                    slot >= 0
                                      ? episodeOrder.map((id, i) => (i === slot ? v.media_id : id))
                                      : episodeOrder;
                                  playEpisode(v.media_id, order);
                                }}
                              >
                                {v.height ? `${v.height}p` : v.format || t("pages.series_detail.play_fallback")}
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
                            className={md.playBtn}
                            onClick={() => playEpisode(mediaId)}
                          >
                            {t("pages.series_detail.btn_play_short")}
                          </Button>
                        ) : (
                          <span className={styles.noMedia}>{t("pages.series_detail.no_associated_file")}</span>
                        )}
                      </div>
                    </div>
                  );
                })}
              </div>
            )}
          </>
        )}
      </section>

      <SeriesEditModal
        series={editOpen ? editSeries : null}
        open={editOpen}
        onClose={() => setEditOpen(false)}
        onSaved={applySeriesUpdate}
      />
      {playlistModalOpen && allMediaIds.length > 0 ? (
        <AddToPlaylistModal
          mediaIds={allMediaIds}
          open
          onClose={() => setPlaylistModalOpen(false)}
          onAdded={(pl) => {
            rememberPlaylistAdded(pl);
            setRecentPlaylistMenu(readRecentPlaylists());
          }}
        />
      ) : null}
      {matchMedia.id > 0 ? (
        <MediaMatchModal
          media={matchMedia}
          fixMatch={Boolean(representativeMedia?.scraped)}
          open={matchModalOpen}
          onClose={() => setMatchModalOpen(false)}
          onMatched={applyMatchUpdate}
        />
      ) : null}
    </div>
  );
}
