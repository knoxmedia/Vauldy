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
import { useCallback, useEffect, useMemo, useState } from "react";
import { useNavigate, useSearchParams } from "react-router-dom";
import {
  PLAYLIST_PLAY_SESSION_KEY,
  Playlist,
  PlaylistItem,
  deletePlaylist,
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

function storePlaylistPlaySession(playlistId: number, orderedItems: PlaylistItem[]) {
  sessionStorage.setItem(
    PLAYLIST_PLAY_SESSION_KEY,
    JSON.stringify({ playlistId, order: orderedItems.map((i) => i.media_id) })
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

function playlistKindLabel(items: PlaylistItem[]): string {
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
    if (c === "audio") return "音乐";
    if (c === "video") return "视频";
  }
  return "其他";
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
  const [searchParams] = useSearchParams();
  const currentMediaId = searchParams.get("current_media_id");

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
      message.error((e as Error).message || "加载失败");
    } finally {
      setListLoading(false);
    }
  }, []);

  useEffect(() => {
    void loadPlaylists();
  }, [loadPlaylists]);

  function openDetail(id: number) {
    setDetailLoading(true);
    setDetail(null);
    fetchPlaylist(id)
      .then((p) => {
        setDetail(p);
      })
      .catch((e: unknown) => message.error((e as Error).message || "加载失败"))
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
    storePlaylistPlaySession(detail.id, items);
    nav(`/player/${item.media_id}?playlist_id=${detail.id}&index=${index}`);
  }

  function playNextAfter(item: PlaylistItem) {
    if (!detail) return;
    const idx = displayItems.findIndex((i) => i.id === item.id);
    if (idx < 0) return;
    if (idx + 1 >= displayItems.length) {
      message.info("已是列表中的最后一项");
      return;
    }
    const nextIdx = idx + 1;
    const next = displayItems[nextIdx]!;
    storePlaylistPlaySession(detail.id, displayItems);
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
      title: "确认删除",
      content: `您确定要删除「${pl.name}」吗？此操作不能撤销。`,
      okText: "确定",
      cancelText: "取消",
      okButtonProps: { danger: true },
      centered: true,
      onOk: async () => {
        try {
          await deletePlaylist(pl.id);
          message.success("已删除");
          void loadPlaylists();
        } catch (err: unknown) {
          message.error((err as Error).message || "删除失败");
          throw err;
        }
      },
    });
  }

  async function handleDeleteItem(item: PlaylistItem) {
    if (!detail) return;
    try {
      await removePlaylistItem(detail.id, item.id);
      message.success("已移除");
      const updated = await fetchPlaylist(detail.id);
      setDetail(updated);
    } catch (e: unknown) {
      message.error((e as Error).message || "移除失败");
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
        message.error((e as Error).message || "排序保存失败");
      }
    },
    [detail, playbackMode, shuffleOrder]
  );

  function makeItemMenu(item: PlaylistItem): MenuProps {
    return {
      items: [
        {
          key: "play_next",
          label: "播放下一个",
          icon: <ToolbarPlayIcon className={styles.playlistMenuPlaySvg} />,
          onClick: () => playNextAfter(item),
        },
        { type: "divider" },
        {
          key: "remove",
          label: "删除",
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
    if (pl.first_media_id) return `/uploads/posters/${pl.first_media_id}.jpg`;
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
              新建播放列表
            </Button>
          </div>
        </div>

        {listLoading ? (
          <div className={styles.loadingWrap}><Spin /></div>
        ) : playlists.length === 0 ? (
          <Empty description="暂无播放列表，点击「新建播放列表」开始创建" />
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
                  <div className={styles.playlistCountBadge}>{pl.item_count} 个项目</div>
                  <div className={styles.playlistHoverShade}>
                    <button
                      type="button"
                      className={styles.playlistPlayBtn}
                      aria-label="播放"
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
                    aria-label="编辑"
                    onClick={(e) => void openEdit(pl, e)}
                  >
                    <EditOutlined />
                  </button>
                  <button
                    type="button"
                    className={styles.playlistDeleteBtn}
                    aria-label="删除"
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
        返回
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
                aria-label="播放"
                disabled={displayItems.length === 0}
                onClick={() => playFrom(0)}
              >
                <ToolbarPlayIcon className={styles.detailHeroPlayFabSvg} />
              </button>
            </div>
            <div className={styles.detailHeroMeta}>
              <h1 className={styles.detailHeroTitle}>{detail.name}</h1>
              <div className={styles.detailHeroSubtitle}>
                {detail.description?.trim() ? detail.description : "[无简介]"}
              </div>
              <div className={styles.detailHeroKind}>{playlistKindLabel(apiOrderedItems)}</div>
              <Rate disabled value={0} count={5} className={styles.detailHeroStars} />
              <div className={styles.detailToolbar}>
                <Tooltip title="播放" placement="bottom">
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
                      播放
                    </Button>
                  </span>
                </Tooltip>
                <Tooltip title="随机播放" placement="bottom">
                  <span className={styles.detailToolbarIconWrap}>
                    <Button
                      type="text"
                      icon={<ShufflePlayIcon className={styles.detailToolbarShuffleSvg} />}
                      className={styles.detailIconBtn}
                      data-active={playbackMode === "shuffle" ? "" : undefined}
                      onClick={() => setPlaybackMode((m) => (m === "shuffle" ? "ordered" : "shuffle"))}
                      aria-pressed={playbackMode === "shuffle"}
                      aria-label="随机播放"
                    />
                  </span>
                </Tooltip>
                <Tooltip title="添加到列表" placement="bottom">
                  <Button
                    type="text"
                    icon={<AddToListIcon className={styles.detailToolbarAddSvg} />}
                    className={styles.detailIconBtn}
                    aria-label="添加到列表"
                    onClick={() => {
                      message.info("请在「浏览」中选择内容，通过菜单「添加到播放列表」加入本列表");
                      nav("/browse");
                    }}
                  />
                </Tooltip>
                <Tooltip title="编辑" placement="bottom">
                  <Button
                    type="text"
                    icon={<EditOutlined />}
                    className={styles.detailIconBtn}
                    onClick={openEditFromDetail}
                    aria-label="编辑"
                  />
                </Tooltip>
                <Tooltip title="更多" placement="bottom">
                  <Dropdown
                    menu={{
                      items: [
                        {
                          key: "p_del",
                          danger: true,
                          label: "删除播放列表",
                          icon: <DeleteOutlined />,
                          onClick: () => {
                            Modal.confirm({
                              title: "确认删除",
                              content: `您确定要删除「${detail.name}」吗？此操作不能撤销。`,
                              okText: "确定",
                              okButtonProps: { danger: true },
                              cancelText: "取消",
                              centered: true,
                              onOk: async () => {
                                try {
                                  await deletePlaylist(detail.id);
                                  message.success("已删除");
                                  goBack();
                                } catch (err: unknown) {
                                  message.error((err as Error).message || "删除失败");
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
                    <Button type="text" icon={<MoreOutlined />} className={styles.detailIconBtn} aria-label="更多" />
                  </Dropdown>
                </Tooltip>
              </div>
            </div>
          </div>

          {displayItems.length === 0 ? (
            <Empty className={styles.detailEmpty} description="列表为空，从「浏览」添加媒体到播放列表" />
          ) : (
            <>
              <div className={styles.trackSectionHead}>{displayItems.length} 个视频</div>
              <div className={styles.trackList}>
                {displayItems.map((item, globalIdx) => {
                  return (
                    <div
                      key={item.id}
                      className={`${styles.trackRow} ${dragItemId === item.id ? styles.trackRowDragging : ""} ${
                        dragOverItemId === item.id && dragItemId !== item.id ? styles.trackRowDropTarget : ""
                      }`}
                      data-playing={currentMediaId && item.media_id === Number(currentMediaId) ? "" : undefined}
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
                        title="拖动排序"
                      >
                        <HolderOutlined />
                      </span>
                      <span className={styles.trackIndex}>{globalIdx + 1}</span>
                      <span className={styles.trackTitle}>{item.title || "未命名"}</span>
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
                            aria-label="更多"
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
