import {
  DeleteOutlined,
  EditOutlined,
  EllipsisOutlined,
  FileAddOutlined,
  HolderOutlined,
  LeftOutlined,
  MoreOutlined,
  PlusOutlined,
} from "@ant-design/icons";
import type { MenuProps } from "antd";
import { Button, Dropdown, Empty, message, Modal, Rate, Spin, Tooltip } from "antd";
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import { useNavigate, useSearchParams } from "react-router-dom";
import {
  PLAYLIST_PLAY_SESSION_KEY,
  Playlist,
  PlaylistItem,
  deletePlaylist,
  derivedVideoPosterSrc,
  fetchPlaylist,
  fetchPlaylists,
  mediaPosterSrc,
  removePlaylistItem,
  reorderPlaylistItems,
} from "../api/client";
import AddToListIcon from "../components/AddToListIcon";
import PlaylistFormModal from "../components/PlaylistFormModal";
import ShufflePlayIcon from "../components/ShufflePlayIcon";
import ToolbarPlayIcon from "../components/ToolbarPlayIcon";
import { useT, type TranslateFn } from "../i18n";
import styles from "./Playlists.module.css";

type PlaybackMode = "ordered" | "shuffle";

function displayItemsFor(
  items: PlaylistItem[] | undefined,
  mode: PlaybackMode,
  shuffleOrder: number[]
): PlaylistItem[] {
  const base = items ?? [];
  if (mode !== "shuffle") return base;
  if (shuffleOrder.length !== base.length) return base;
  return shuffleOrder.map((i) => base[i]!);
}

function moveItemInList(list: PlaylistItem[], fromIdx: number, toIdx: number): PlaylistItem[] {
  const next = [...list];
  const [removed] = next.splice(fromIdx, 1);
  let insert = toIdx;
  if (fromIdx < toIdx) insert -= 1;
  next.splice(insert, 0, removed);
  return next;
}

function storePlaylistPlaySession(
  playlistId: number,
  orderedItems: PlaylistItem[],
  mode: PlaybackMode
) {
  sessionStorage.setItem(
    PLAYLIST_PLAY_SESSION_KEY,
    JSON.stringify({
      playlistId,
      order: orderedItems.map((i) => i.media_id),
      mode,
    })
  );
}

function fmtDurationShort(sec: number): string {
  if (sec == null || Number.isNaN(sec) || sec <= 0) return "0:00";
  const total = Math.floor(sec);
  const h = Math.floor(total / 3600);
  const m = Math.floor((total % 3600) / 60);
  const s = total % 60;
  if (h > 0) return `${h}:${String(m).padStart(2, "0")}:${String(s).padStart(2, "0")}`;
  return `${m}:${String(s).padStart(2, "0")}`;
}

function playlistKindLabel(items: PlaylistItem[], t: TranslateFn): string {
  if (items.length === 0) return "—";
  const toCat = (ft: string) => {
    const f = (ft || "").toLowerCase().trim();
    if (/^(mp3|flac|aac|m4a|wav|ogg|opus|wma)$/.test(f)) return "audio";
    if (/^(mp4|mkv|webm|avi|mov|m4v|ts)$/.test(f)) return "video";
    if (f.includes("audio")) return "audio";
    if (f.includes("video")) return "video";
    return "other";
  };
  const cats = new Set(items.map((i) => toCat(i.file_type)));
  if (cats.size === 1) {
    const c = [...cats][0]!;
    if (c === "audio") return t("pages.playlists.category_audio");
    if (c === "video") return t("pages.playlists.category_video");
  }
  return t("pages.playlists.category_other");
}

function detailHeroSrc(pl: Playlist, orderedItems: PlaylistItem[]): string {
  if (pl.poster_url) return pl.poster_url;
  if (pl.square_art_url) return pl.square_art_url;
  const first = orderedItems[0];
  if (first) return mediaPosterSrc({ id: first.media_id, poster_url: first.poster_url || "" });
  return "";
}

export default function PlaylistsPage() {
  const nav = useNavigate();
  const t = useT();
  const [searchParams] = useSearchParams();
  const currentMediaId = searchParams.get("current_media_id");
  const playingMediaId = currentMediaId ? Number(currentMediaId) : NaN;
  const playingRowRef = useRef<HTMLDivElement | null>(null);

  // ——— List view state ———
  const [playlists, setPlaylists] = useState<Playlist[]>([]);
  const [listLoading, setListLoading] = useState(false);

  // ——— Detail view state ———
  const [detail, setDetail] = useState<Playlist | null>(null);
  const [detailLoading, setDetailLoading] = useState(false);
  const [playbackMode, setPlaybackMode] = useState<PlaybackMode>("ordered");
  const [shuffleOrder, setShuffleOrder] = useState<number[]>([]);
  const [dragItemId, setDragItemId] = useState<number | null>(null);
  const [dragOverItemId, setDragOverItemId] = useState<number | null>(null);

  // ——— Form modal state ———
  const [formModalOpen, setFormModalOpen] = useState(false);
  const [editingPlaylist, setEditingPlaylist] = useState<Playlist | null>(null);

  const itemsKey = detail?.items?.map((i) => i.id).join(",") ?? "";

  useEffect(() => {
    if (playbackMode !== "shuffle") {
      setShuffleOrder([]);
      return;
    }
    const base = detail?.items ?? [];
    const n = base.length;
    if (n === 0) {
      setShuffleOrder([]);
      return;
    }
    const idx = Array.from({ length: n }, (_, i) => i);
    for (let i = n - 1; i > 0; i--) {
      const j = Math.floor(Math.random() * (i + 1));
      [idx[i], idx[j]] = [idx[j], idx[i]];
    }
    setShuffleOrder(idx);
  }, [playbackMode, itemsKey]);

  const displayItems = useMemo(
    () => displayItemsFor(detail?.items, playbackMode, shuffleOrder),
    [detail?.items, playbackMode, shuffleOrder]
  );

  useEffect(() => {
    setDragItemId(null);
    setDragOverItemId(null);
  }, [itemsKey]);

  const loadPlaylists = useCallback(async () => {
    setListLoading(true);
    try {
      setPlaylists(await fetchPlaylists());
    } catch (e: unknown) {
      message.error((e as Error).message || t("pages.playlists.load_failed"));
    } finally {
      setListLoading(false);
    }
  }, [t]);

  useEffect(() => {
    void loadPlaylists();
  }, [loadPlaylists]);

  const playlistIdParam = searchParams.get("playlist_id");

  useEffect(() => {
    if (!playlistIdParam || detailLoading) return;
    const pid = Number(playlistIdParam);
    if (!Number.isFinite(pid) || pid <= 0) return;
    if (detail?.id === pid) return;
    openDetail(pid);
  }, [playlistIdParam, detail?.id, detailLoading]);

  useEffect(() => {
    if (!Number.isFinite(playingMediaId) || playingMediaId <= 0) return;
    playingRowRef.current?.scrollIntoView({ block: "nearest", behavior: "smooth" });
  }, [playingMediaId, displayItems]);

  function openDetail(id: number) {
    setDetailLoading(true);
    setDetail(null);
    fetchPlaylist(id)
      .then((p) => {
        setDetail(p);
      })
      .catch((e: unknown) => message.error((e as Error).message || t("pages.playlists.load_failed")))
      .finally(() => setDetailLoading(false));
  }

  function goBack() {
    setDetail(null);
    void loadPlaylists();
  }

  const detailViewHeroSrc = useMemo(() => {
    if (detail == null) return "";
    return detailHeroSrc(detail, detail.items ?? []);
  }, [detail]);

  const [detailHeroImgFailed, setDetailHeroImgFailed] = useState(false);

  useEffect(() => {
    setDetailHeroImgFailed(false);
  }, [detailViewHeroSrc, detail?.id]);

  function playFrom(index: number) {
    const items = displayItems;
    if (items.length === 0 || !detail) return;
    const item = items[index];
    if (!item) return;
    storePlaylistPlaySession(detail.id, items, playbackMode);
    nav(`/player/${item.media_id}?playlist_id=${detail.id}&index=${index}`);
  }

  function playNextAfter(item: PlaylistItem) {
    if (!detail) return;
    const idx = displayItems.findIndex((i) => i.id === item.id);
    if (idx < 0) return;
    if (idx + 1 >= displayItems.length) {
      message.info(t("pages.playlists.last_item_warning"));
      return;
    }
    const nextIdx = idx + 1;
    const next = displayItems[nextIdx]!;
    storePlaylistPlaySession(detail.id, displayItems, playbackMode);
    nav(`/player/${next.media_id}?playlist_id=${detail.id}&index=${nextIdx}`);
  }

  // ——— Form handlers ———
  function openCreate() {
    setEditingPlaylist(null);
    setFormModalOpen(true);
  }

  function openEdit(pl: Playlist, e: React.MouseEvent) {
    e.stopPropagation();
    setEditingPlaylist(pl);
    setFormModalOpen(true);
  }

  function openEditFromDetail() {
    if (!detail) return;
    setEditingPlaylist(detail);
    setFormModalOpen(true);
  }

  async function handleSaved(saved: Playlist) {
    void loadPlaylists();
    if (detail?.id === saved.id) {
      setDetail(saved);
    }
  }

  function confirmDeletePlaylist(pl: Playlist, e: React.MouseEvent) {
    e.stopPropagation();
    Modal.confirm({
      title: t("pages.playlists.delete_confirm_title"),
      content: t("pages.playlists.delete_confirm_content", { name: pl.name }),
      okText: t("pages.playlists.ok_text"),
      cancelText: t("pages.playlists.cancel_text"),
      okButtonProps: { danger: true },
      centered: true,
      onOk: async () => {
        try {
          await deletePlaylist(pl.id);
          message.success(t("pages.playlists.deleted"));
          void loadPlaylists();
        } catch (err: unknown) {
          message.error((err as Error).message || t("pages.playlists.delete_failed"));
          throw err;
        }
      },
    });
  }

  async function handleDeleteItem(item: PlaylistItem) {
    if (!detail) return;
    try {
      await removePlaylistItem(detail.id, item.id);
      message.success(t("pages.playlists.removed"));
      const updated = await fetchPlaylist(detail.id);
      setDetail(updated);
    } catch (e: unknown) {
      message.error((e as Error).message || t("pages.playlists.remove_failed"));
    }
  }

  const handleReorderDrop = useCallback(
    async (fromId: number, toId: number) => {
      if (!detail || fromId === toId) return;
      const list = displayItemsFor(detail.items, playbackMode, shuffleOrder);
      const fromIdx = list.findIndex((i) => i.id === fromId);
      const toIdx = list.findIndex((i) => i.id === toId);
      if (fromIdx < 0 || toIdx < 0) return;
      const newList = moveItemInList(list, fromIdx, toIdx);
      const updatedWithSort = newList.map((it, i) => ({ ...it, sort_order: i }));
      const payload = updatedWithSort.map((it, i) => ({ id: it.id, sort_order: i }));
      const prevDetail = detail;
      setPlaybackMode("ordered");
      setDetail({ ...detail, items: updatedWithSort });
      try {
        await reorderPlaylistItems(detail.id, payload);
      } catch (e: unknown) {
        setDetail(prevDetail);
        message.error((e as Error).message || t("pages.playlists.sort_save_failed"));
      }
    },
    [detail, playbackMode, shuffleOrder, t]
  );

  function makeItemMenu(item: PlaylistItem): MenuProps {
    return {
      items: [
        {
          key: "play_next",
          label: t("pages.playlists.menu_play_next"),
          icon: <ToolbarPlayIcon className={styles.playlistMenuPlaySvg} />,
          onClick: () => playNextAfter(item),
        },
        { type: "divider" },
        {
          key: "remove",
          label: t("pages.playlists.menu_delete"),
          danger: true,
          onClick: () => void handleDeleteItem(item),
        },
      ],
    };
  }

  // ——— Playlist card poster ———
  function playlistCoverSrc(pl: Playlist): string {
    if (pl.poster_url) return pl.poster_url;
    if (pl.square_art_url) return pl.square_art_url;
    if (pl.first_media_id) return derivedVideoPosterSrc(pl.first_media_id);
    return "";
  }

  // ——— Render: list view ———
  if (!detail) {
    return (
      <div style={{ padding: "16px 0 32px" }}>
        <div className={styles.topBar}>
          <div className={styles.topLeft} />
          <div className={styles.topRight}>
            <Button
              type="primary"
              icon={<PlusOutlined />}
              className={styles.createBtn}
              onClick={openCreate}
            >
              {t("pages.playlists.create_btn")}
            </Button>
          </div>
        </div>

        {listLoading ? (
          <div className={styles.loadingWrap}><Spin /></div>
        ) : playlists.length === 0 ? (
          <Empty description={t("pages.playlists.empty_create_hint")} />
        ) : (
          <div className={styles.playlistGrid}>
            {playlists.map((pl) => (
              <div
                key={pl.id}
                className={styles.playlistCard}
                onClick={() => void openDetail(pl.id)}
              >
                <div className={styles.playlistImage}>
                  {playlistCoverSrc(pl) ? (
                    <img
                      className={styles.playlistCoverImg}
                      src={playlistCoverSrc(pl)}
                      alt=""
                      loading="lazy"
                      decoding="async"
                      onLoad={(e) => {
                        e.currentTarget.parentElement?.setAttribute("data-cover-loaded", "");
                      }}
                      onError={(ev) => {
                        ev.currentTarget.style.display = "none";
                      }}
                    />
                  ) : (
                    <div style={{ position: "absolute", inset: 0, display: "flex", alignItems: "center", justifyContent: "center", background: "linear-gradient(135deg, #2a2a3a 0%, #1a1a24 100%)" }}>
                      <FileAddOutlined style={{ fontSize: 40, color: "rgba(255,255,255,0.2)" }} />
                    </div>
                  )}
                  <div className={styles.playlistCountBadge}>{t("pages.playlists.item_count", { count: pl.item_count })}</div>
                  <div className={styles.playlistHoverShade}>
                    <button
                      type="button"
                      className={styles.playlistPlayBtn}
                      aria-label={t("pages.playlists.card_play_aria")}
                      onClick={(e) => {
                        e.stopPropagation();
                        if (pl.item_count > 0) void openDetail(pl.id);
                      }}
                    >
                      <ToolbarPlayIcon className={styles.playlistCardPlaySvg} />
                    </button>
                  </div>
                  <button
                    type="button"
                    className={styles.playlistEditBtn}
                    aria-label={t("pages.playlists.card_edit_aria")}
                    onClick={(e) => void openEdit(pl, e)}
                  >
                    <EditOutlined />
                  </button>
                  <button
                    type="button"
                    className={styles.playlistDeleteBtn}
                    aria-label={t("pages.playlists.card_delete_aria")}
                    onClick={(e) => confirmDeletePlaylist(pl, e)}
                  >
                    <DeleteOutlined />
                  </button>
                </div>
                <div className={styles.playlistCardBody}>
                  <div className={styles.playlistName}>{pl.name}</div>
                  {pl.description ? (
                    <div className={styles.playlistEmpty}>{pl.description}</div>
                  ) : null}
                </div>
              </div>
            ))}
          </div>
        )}

        <PlaylistFormModal
          open={formModalOpen}
          playlist={editingPlaylist}
          onClose={() => setFormModalOpen(false)}
          onSaved={handleSaved}
        />
      </div>
    );
  }

  // ——— Render: detail view ———
  const apiOrderedItems = detail.items ?? [];
  const canReorderTracks = displayItems.length > 1;

  return (
    <div className={styles.detailPage}>
      <Button type="text" icon={<LeftOutlined />} className={styles.detailBackLink} onClick={goBack}>
        {t("pages.playlists.back")}
      </Button>

      {detailLoading ? (
        <div className={styles.loadingWrap}>
          <Spin />
        </div>
      ) : (
        <>
          <div className={styles.detailHero}>
            <div className={styles.detailHeroArt}>
              {detailViewHeroSrc && !detailHeroImgFailed ? (
                <img
                  src={detailViewHeroSrc}
                  alt=""
                  className={styles.detailHeroImg}
                  decoding="async"
                  onError={() => setDetailHeroImgFailed(true)}
                />
              ) : (
                <div className={styles.detailHeroPlaceholder} aria-hidden />
              )}
              <button
                type="button"
                className={styles.detailHeroPlayFab}
                aria-label={t("pages.playlists.play_aria")}
                disabled={displayItems.length === 0}
                onClick={() => playFrom(0)}
              >
                <ToolbarPlayIcon className={styles.detailHeroPlayFabSvg} />
              </button>
            </div>
            <div className={styles.detailHeroMeta}>
              <h1 className={styles.detailHeroTitle}>{detail.name}</h1>
              <div className={styles.detailHeroSubtitle}>
                {detail.description?.trim() ? detail.description : t("pages.playlists.no_description")}
              </div>
              <div className={styles.detailHeroKind}>{playlistKindLabel(apiOrderedItems, t)}</div>
              <Rate disabled value={0} count={5} className={styles.detailHeroStars} />
              <div className={styles.detailToolbar}>
                <Tooltip title={t("pages.playlists.tooltip_play")} placement="bottom">
                  <span className={styles.detailToolbarIconWrap}>
                    <Button
                      type="primary"
                      icon={<ToolbarPlayIcon className={styles.detailToolbarPlaySvg} />}
                      className={styles.detailToolbarPlayBtn}
                      onClick={() => {
                        setPlaybackMode("ordered");
                        playFrom(0);
                      }}
                      disabled={displayItems.length === 0}
                    >
                      {t("pages.playlists.btn_play")}
                    </Button>
                  </span>
                </Tooltip>
                <Tooltip title={t("pages.playlists.tooltip_shuffle")} placement="bottom">
                  <span className={styles.detailToolbarIconWrap}>
                    <Button
                      type="text"
                      icon={<ShufflePlayIcon className={styles.detailToolbarShuffleSvg} />}
                      className={styles.detailIconBtn}
                      data-active={playbackMode === "shuffle" ? "" : undefined}
                      onClick={() => setPlaybackMode((m) => (m === "shuffle" ? "ordered" : "shuffle"))}
                      aria-pressed={playbackMode === "shuffle"}
                      aria-label={t("pages.playlists.btn_shuffle")}
                    />
                  </span>
                </Tooltip>
                <Tooltip title={t("pages.playlists.tooltip_add_to_list")} placement="bottom">
                  <Button
                    type="text"
                    icon={<AddToListIcon className={styles.detailToolbarAddSvg} />}
                    className={styles.detailIconBtn}
                    aria-label={t("pages.playlists.tooltip_add_to_list")}
                    onClick={() => {
                      message.info(t("pages.playlists.info_add_via_browse"));
                      nav("/browse");
                    }}
                  />
                </Tooltip>
                <Tooltip title={t("pages.playlists.tooltip_edit")} placement="bottom">
                  <Button
                    type="text"
                    icon={<EditOutlined />}
                    className={styles.detailIconBtn}
                    onClick={openEditFromDetail}
                    aria-label={t("pages.playlists.tooltip_edit")}
                  />
                </Tooltip>
                <Tooltip title={t("pages.playlists.tooltip_more")} placement="bottom">
                  <Dropdown
                    menu={{
                      items: [
                        {
                          key: "p_del",
                          danger: true,
                          label: t("pages.playlists.menu_delete_playlist"),
                          icon: <DeleteOutlined />,
                          onClick: () => {
                            Modal.confirm({
                              title: t("pages.playlists.delete_confirm_title"),
                              content: t("pages.playlists.delete_confirm_content", { name: detail.name }),
                              okText: t("pages.playlists.ok_text"),
                              okButtonProps: { danger: true },
                              cancelText: t("pages.playlists.cancel_text"),
                              centered: true,
                              onOk: async () => {
                                try {
                                  await deletePlaylist(detail.id);
                                  message.success(t("pages.playlists.deleted"));
                                  goBack();
                                } catch (err: unknown) {
                                  message.error((err as Error).message || t("pages.playlists.delete_failed"));
                                  throw err;
                                }
                              },
                            });
                          },
                        },
                      ],
                    }}
                    trigger={["click"]}
                    placement="bottomRight"
                  >
                    <Button type="text" icon={<MoreOutlined />} className={styles.detailIconBtn} aria-label={t("pages.playlists.tooltip_more")} />
                  </Dropdown>
                </Tooltip>
              </div>
            </div>
          </div>

          {displayItems.length === 0 ? (
            <Empty className={styles.detailEmpty} description={t("pages.playlists.empty_detail")} />
          ) : (
            <>
              <div className={styles.trackSectionHead}>{t("pages.playlists.video_count", { count: displayItems.length })}</div>
              <div className={styles.trackList}>
                {displayItems.map((item, globalIdx) => {
                  const isPlaying =
                    Number.isFinite(playingMediaId) && playingMediaId > 0 && item.media_id === playingMediaId;
                  return (
                    <div
                      key={item.id}
                      ref={isPlaying ? playingRowRef : undefined}
                      className={`${styles.trackRow} ${dragItemId === item.id ? styles.trackRowDragging : ""} ${
                        dragOverItemId === item.id && dragItemId !== item.id ? styles.trackRowDropTarget : ""
                      }`}
                      data-playing={isPlaying ? "" : undefined}
                      onClick={() => playFrom(globalIdx)}
                      onKeyDown={(e) => {
                        if (e.key === "Enter" || e.key === " ") {
                          e.preventDefault();
                          playFrom(globalIdx);
                        }
                      }}
                      onDragOver={(e) => {
                        if (dragItemId == null) return;
                        e.preventDefault();
                        e.dataTransfer.dropEffect = "move";
                        if (item.id !== dragItemId) setDragOverItemId(item.id);
                      }}
                      onDragLeave={(e) => {
                        if (!e.currentTarget.contains(e.relatedTarget as Node)) {
                          setDragOverItemId((cur) => (cur === item.id ? null : cur));
                        }
                      }}
                      onDrop={(e) => {
                        e.preventDefault();
                        const raw = e.dataTransfer.getData("text/plain");
                        const from = Number.parseInt(raw, 10);
                        if (Number.isFinite(from)) void handleReorderDrop(from, item.id);
                        setDragItemId(null);
                        setDragOverItemId(null);
                      }}
                      role="button"
                      tabIndex={0}
                    >
                      <span
                        className={styles.trackDragHandle}
                        draggable={canReorderTracks}
                        onDragStart={(e) => {
                          e.stopPropagation();
                          e.dataTransfer.setData("text/plain", String(item.id));
                          e.dataTransfer.effectAllowed = "move";
                          setDragItemId(item.id);
                        }}
                        onDragEnd={() => {
                          setDragItemId(null);
                          setDragOverItemId(null);
                        }}
                        onClick={(e) => e.stopPropagation()}
                        title={t("pages.playlists.drag_to_sort")}
                      >
                        <HolderOutlined />
                      </span>
                      <span className={styles.trackIndex}>
                        {isPlaying ? <ToolbarPlayIcon className={styles.trackPlayingIcon} /> : globalIdx + 1}
                      </span>
                      <span className={styles.trackTitle}>
                        {item.title || t("pages.playlists.untitled")}
                        {isPlaying ? <span className={styles.trackPlayingLabel}>{t("pages.playlists.now_playing_label")}</span> : null}
                      </span>
                      <span className={styles.trackDuration}>{fmtDurationShort(item.duration)}</span>
                      <span
                        className={styles.trackMore}
                        onClick={(e) => {
                          e.stopPropagation();
                        }}
                        onKeyDown={(e) => e.stopPropagation()}
                      >
                        <Dropdown menu={makeItemMenu(item)} trigger={["click"]} placement="bottomRight">
                          <Button
                            type="text"
                            size="small"
                            icon={<EllipsisOutlined rotate={90} />}
                            aria-label={t("pages.playlists.more_aria")}
                            onClick={(e) => e.stopPropagation()}
                          />
                        </Dropdown>
                      </span>
                    </div>
                  );
                })}
              </div>
            </>
          )}
        </>
      )}

      <PlaylistFormModal
        open={formModalOpen}
        playlist={editingPlaylist}
        onClose={() => setFormModalOpen(false)}
        onSaved={handleSaved}
      />
    </div>
  );
}
