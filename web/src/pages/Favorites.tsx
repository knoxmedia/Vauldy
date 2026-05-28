import {
  AppstoreOutlined,
  BarsOutlined,
  CaretRightOutlined,
  CheckOutlined,
  DownOutlined,
  EditOutlined,
  EllipsisOutlined,
  PictureOutlined,
  TableOutlined,
  UpOutlined,
} from "@ant-design/icons";
import type { MenuProps } from "antd";
import { Button, Dropdown, Empty, Pagination, Spin, message } from "antd";
import type { ComponentType } from "react";
import { useCallback, useEffect, useMemo, useState } from "react";
import { useNavigate } from "react-router-dom";
import { MediaItem, addPlaylistItem, fetchFavorites, removeFavorite } from "../api/client";
import AddToPlaylistModal from "../components/AddToPlaylistModal";
import MediaPosterImg from "../components/MediaPosterImg";
import { buildMediaMenuItems } from "../components/mediaMenuItems";
import { readRecentPlaylists, rememberPlaylistAdded } from "../lib/recentPlaylists";
import { useT, type TranslateFn } from "../i18n";
import styles from "./Favorites.module.css";

type ViewMode = "poster" | "thumb" | "list" | "table";
type SortField = "title" | "added";
type SortOrder = "asc" | "desc";

const FAVORITES_PREFS_KEY = "knox.favorites.prefs.v1";
const TABLE_PAGE_SIZE = 20;

type ViewModeEntry = { value: ViewMode; label: string; Icon: ComponentType };

function buildViewModes(t: TranslateFn): ViewModeEntry[] {
  return [
    { value: "poster", label: t("pages.browse.view_poster"), Icon: PictureOutlined },
    { value: "thumb", label: t("pages.browse.view_thumb"), Icon: AppstoreOutlined },
    { value: "list", label: t("pages.browse.view_list"), Icon: BarsOutlined },
    { value: "table", label: t("pages.browse.view_table"), Icon: TableOutlined },
  ];
}

function fmtDurationLocalized(sec: number, t: TranslateFn): string {
  if (sec == null || Number.isNaN(sec) || sec <= 0) return "—";
  const total = Math.floor(sec);
  const h = Math.floor(total / 3600);
  const m = Math.floor((total % 3600) / 60);
  if (h > 0) return t("pages.browse.fmt_h_m", { h, m });
  if (m > 0) return t("pages.browse.fmt_m", { m });
  return t("pages.browse.fmt_s", { s: total % 60 });
}

function readFavoritesPrefs(): {
  viewMode: ViewMode;
  sortField: SortField;
  sortOrder: SortOrder;
} | null {
  if (typeof window === "undefined") return null;
  try {
    const raw = window.localStorage.getItem(FAVORITES_PREFS_KEY);
    if (!raw) return null;
    const parsed = JSON.parse(raw) as {
      viewMode?: ViewMode;
      sortField?: SortField;
      sortOrder?: SortOrder;
    };
    const viewMode: ViewMode = ["poster", "thumb", "list", "table"].includes(String(parsed.viewMode))
      ? (parsed.viewMode as ViewMode)
      : "table";
    const sortField: SortField = ["title", "added"].includes(String(parsed.sortField))
      ? (parsed.sortField as SortField)
      : "added";
    const sortOrder: SortOrder = parsed.sortOrder === "asc" || parsed.sortOrder === "desc" ? parsed.sortOrder : "desc";
    return { viewMode, sortField, sortOrder };
  } catch {
    return null;
  }
}

type TableColSpec = { key: string; label: string; sortField: SortField; widthPx: number };

function buildTableColSpecs(t: TranslateFn): TableColSpec[] {
  return [
    { key: "title", label: t("pages.browse.col_title"), sortField: "title", widthPx: 0 },
    { key: "duration", label: t("pages.browse.col_duration"), sortField: "added", widthPx: 112 },
    { key: "quality", label: t("pages.browse.col_quality"), sortField: "added", widthPx: 104 },
    { key: "added", label: t("pages.browse.col_added"), sortField: "added", widthPx: 168 },
  ];
}

export default function FavoritesPage() {
  const nav = useNavigate();
  const t = useT();
  const VIEW_MODES = useMemo(() => buildViewModes(t), [t]);
  const TABLE_COL_SPECS = useMemo(() => buildTableColSpecs(t), [t]);
  const [rows, setRows] = useState<MediaItem[]>([]);
  const [loading, setLoading] = useState(false);
  const [viewMode, setViewMode] = useState<ViewMode>(() => readFavoritesPrefs()?.viewMode ?? "table");
  const [sortField, setSortField] = useState<SortField>(() => readFavoritesPrefs()?.sortField ?? "added");
  const [sortOrder, setSortOrder] = useState<SortOrder>(() => readFavoritesPrefs()?.sortOrder ?? "desc");
  const [viewModeMenuOpen, setViewModeMenuOpen] = useState(false);
  const [selectedIds, setSelectedIds] = useState<Set<number>>(() => new Set());
  const [tablePage, setTablePage] = useState(1);
  const [addToPlaylistMediaId, setAddToPlaylistMediaId] = useState<number | null>(null);
  const [recentPlaylistMenu, setRecentPlaylistMenu] = useState(readRecentPlaylists);

  const load = useCallback(async () => {
    setLoading(true);
    try {
      setRows(await fetchFavorites());
    } catch (e: unknown) {
      message.error((e as Error).message || t("pages.favorites.load_failed"));
      setRows([]);
    } finally {
      setLoading(false);
    }
  }, [t]);

  useEffect(() => {
    void load();
  }, [load]);

  useEffect(() => {
    if (typeof window === "undefined") return;
    window.localStorage.setItem(
      FAVORITES_PREFS_KEY,
      JSON.stringify({ viewMode, sortField, sortOrder })
    );
  }, [viewMode, sortField, sortOrder]);

  const sortedRows = [...rows].sort((a, b) => {
    const factor = sortOrder === "asc" ? 1 : -1;
    if (sortField === "title") {
      return (a.title ?? "").localeCompare(b.title ?? "", "zh-CN") * factor;
    }
    const timeA = a.created_at ? Date.parse(a.created_at) : 0;
    const timeB = b.created_at ? Date.parse(b.created_at) : 0;
    return (timeA - timeB) * factor;
  });

  const tableGridTemplate = (() => {
    const parts: string[] = ["40px"];
    for (const spec of TABLE_COL_SPECS) {
      parts.push(spec.widthPx ? `${spec.widthPx}px` : "minmax(160px, 1fr)");
    }
    parts.push("40px");
    return parts.join(" ");
  })();

  const pagedTableRows = sortedRows.slice((tablePage - 1) * TABLE_PAGE_SIZE, tablePage * TABLE_PAGE_SIZE);

  useEffect(() => {
    setTablePage(1);
  }, [sortedRows.length, viewMode]);

  function toggleSelect(id: number) {
    setSelectedIds((prev) => {
      const next = new Set(prev);
      if (next.has(id)) next.delete(id);
      else next.add(id);
      return next;
    });
  }

  const selectionCount = selectedIds.size;
  const bulkPick = selectionCount > 0;

  async function onUnfavorite(id: number) {
    try {
      await removeFavorite(id);
      message.success(t("pages.favorites.unfavorited"));
      void load();
    } catch (e: unknown) {
      message.error((e as Error).message || t("pages.favorites.operation_failed"));
    }
  }

  const isWatched = (r: MediaItem) => Boolean(r.last_play_at);

  const makeMenu = useCallback(
    (r: MediaItem): MenuProps =>
      buildMediaMenuItems(r, nav, {
        isWatched: isWatched(r),
        onAddToPlaylist: (mid) => setAddToPlaylistMediaId(mid),
        recentPlaylists: recentPlaylistMenu,
        onQuickAddToPlaylist: async (mid, pid) => {
          try {
            await addPlaylistItem(pid, mid);
            const name =
              recentPlaylistMenu.find((p) => p.id === pid)?.name ??
              readRecentPlaylists().find((p) => p.id === pid)?.name ??
              t("pages.favorites.playlist_fallback");
            message.success(t("pages.favorites.added_to_playlist", { name }));
            rememberPlaylistAdded({ id: pid, name });
            setRecentPlaylistMenu(readRecentPlaylists());
          } catch {
            message.error(t("pages.favorites.add_failed"));
          }
        },
        onUnfavorite: (id) => void onUnfavorite(id),
        afterToggleWatched: () => void load(),
      }),
    [nav, recentPlaylistMenu, load, t],
  );

  const addToPlaylistTarget = useMemo(
    () => (addToPlaylistMediaId != null ? rows.find((x) => x.id === addToPlaylistMediaId) : undefined),
    [addToPlaylistMediaId, rows],
  );

  const viewModeMenuItems: MenuProps["items"] = VIEW_MODES.map(({ value, label, Icon }) => ({
    key: value,
    icon: <Icon />,
    label,
  }));

  const CurrentViewIcon = VIEW_MODES.find((m) => m.value === viewMode)?.Icon ?? TableOutlined;
  const currentViewLabel = VIEW_MODES.find((m) => m.value === viewMode)?.label ?? t("pages.browse.table_fallback");

  return (
    <div style={{ padding: "16px 0 32px" }}>
      <div className={styles.topBar}>
        <div className={styles.topLeftTools} />
        <div className={styles.topRightTools}>
          <div className={styles.viewModePicker}>
            <span className={styles.viewModeCurrentIcon} title={currentViewLabel} aria-label={currentViewLabel}>
              <CurrentViewIcon />
            </span>
            <Dropdown
              open={viewModeMenuOpen}
              onOpenChange={setViewModeMenuOpen}
              menu={{
                items: viewModeMenuItems,
                selectedKeys: [viewMode],
                onClick: ({ key }) => {
                  setViewMode(key as ViewMode);
                  setViewModeMenuOpen(false);
                },
              }}
              trigger={["click"]}
              placement="bottomRight"
            >
              <Button
                type="text"
                size="small"
                icon={viewModeMenuOpen ? <UpOutlined /> : <DownOutlined />}
                aria-label={t("pages.browse.view_mode_aria")}
                aria-expanded={viewModeMenuOpen}
              />
            </Dropdown>
          </div>
        </div>
      </div>

      {loading ? (
        <div className={styles.loadingWrap}>
          <Spin />
        </div>
      ) : sortedRows.length === 0 ? (
        <Empty description={t("pages.favorites.empty")} />
      ) : viewMode === "table" ? (
        <div className={styles.browseTableWrap}>
          <div className={styles.browseTableHead}>
            <div className={styles.browseTableHeadRow} style={{ gridTemplateColumns: tableGridTemplate }}>
              <div className={styles.browseThGutter} />
              {TABLE_COL_SPECS.map((spec) => (
                <div
                  key={spec.key}
                  role="button"
                  tabIndex={0}
                  className={styles.browseTh}
                  onClick={() => {
                    if (sortField === spec.sortField) {
                      setSortOrder((o) => (o === "asc" ? "desc" : "asc"));
                    } else {
                      setSortField(spec.sortField);
                      setSortOrder("desc");
                    }
                  }}
                  onKeyDown={(e) => {
                    if (e.key === "Enter" || e.key === " ") {
                      e.preventDefault();
                      if (sortField === spec.sortField) {
                        setSortOrder((o) => (o === "asc" ? "desc" : "asc"));
                      } else {
                        setSortField(spec.sortField);
                        setSortOrder("desc");
                      }
                    }
                  }}
                >
                  <span>{spec.label}</span>
                </div>
              ))}
              <div className={styles.browseThActions} aria-hidden />
            </div>
          </div>
          <div className={styles.browseTableBody}>
            {pagedTableRows.map((r, idx) => {
              const globalIdx = (tablePage - 1) * TABLE_PAGE_SIZE + idx;
              const isSel = selectedIds.has(r.id);
              return (
                <div
                  key={r.id}
                  className={styles.browseTr}
                  style={{ gridTemplateColumns: tableGridTemplate }}
                  data-selected={isSel ? "" : undefined}
                  data-stripe={globalIdx % 2 === 1 ? "" : undefined}
                  data-bulk-pick={bulkPick ? "" : undefined}
                  onClick={() => {
                    if (!bulkPick) nav(`/detail/${r.id}`);
                  }}
                >
                  <div className={styles.browseTdGutter}>
                    <button
                      type="button"
                      className={styles.browseGutterSelect}
                      aria-label={isSel ? t("pages.browse.aria_deselect") : t("pages.browse.aria_select")}
                      data-selected={isSel ? "" : undefined}
                      onClick={(e) => {
                        e.stopPropagation();
                        toggleSelect(r.id);
                      }}
                    >
                      {isSel ? <CheckOutlined /> : null}
                    </button>
                  </div>
                  <div className={styles.browseTdTitle}>
                    {!bulkPick ? (
                      <button
                        type="button"
                        className={styles.browseRowPlay}
                        aria-label={t("pages.browse.aria_play")}
                        onClick={(e) => {
                          e.stopPropagation();
                          nav(`/player/${r.id}`);
                        }}
                      >
                        <CaretRightOutlined />
                      </button>
                    ) : null}
                    <span className={styles.browseTitleText}>{r.title || t("pages.favorites.untitled")}</span>
                  </div>
                  <div className={styles.browseTd}>{fmtDurationLocalized(r.duration, t)}</div>
                  <div className={styles.browseTd}>{r.width && r.height ? `${r.width}x${r.height}` : "—"}</div>
                  <div className={styles.browseTd}>{r.created_at ? r.created_at.replace("T", " ").slice(0, 19) : "—"}</div>
                  <div className={styles.browseTdActions}>
                    {!bulkPick ? (
                      <Dropdown
                        menu={makeMenu(r)}
                        trigger={["click"]}
                        placement="bottomRight"
                      >
                        <Button
                          type="text"
                          size="small"
                          className={styles.browseRowMoreBtn}
                          icon={<EllipsisOutlined rotate={90} />}
                          aria-label={t("pages.browse.aria_more")}
                          onClick={(e) => e.stopPropagation()}
                        />
                      </Dropdown>
                    ) : null}
                  </div>
                </div>
              );
            })}
          </div>
          {sortedRows.length > TABLE_PAGE_SIZE ? (
            <div className={styles.browseTablePagination}>
              <Pagination
                current={tablePage}
                pageSize={TABLE_PAGE_SIZE}
                total={sortedRows.length}
                onChange={(p) => setTablePage(p)}
                showSizeChanger={false}
                size="small"
              />
            </div>
          ) : null}
        </div>
      ) : viewMode === "list" ? (
        <div className={styles.listWrap}>
          {sortedRows.map((r) => {
            const isListSelected = selectedIds.has(r.id);
            return (
              <div
                key={r.id}
                className={styles.listRow}
                data-selected={isListSelected ? "" : undefined}
                data-bulk-pick={bulkPick ? "" : undefined}
              >
                <div className={styles.listSelectSlot}>
                  <button
                    type="button"
                    className={styles.listSelectBtn}
                    aria-label={isListSelected ? t("pages.browse.aria_deselect") : t("pages.browse.aria_select")}
                    aria-pressed={isListSelected}
                    data-selected={isListSelected ? "" : undefined}
                    onClick={(e) => {
                      e.stopPropagation();
                      toggleSelect(r.id);
                    }}
                  >
                    {isListSelected ? <CheckOutlined /> : null}
                  </button>
                </div>
                <div
                  className={styles.listRowMain}
                  tabIndex={0}
                  onClick={() => {
                    if (!bulkPick) nav(`/detail/${r.id}`);
                  }}
                  onKeyDown={(e) => {
                    if (e.key === "Enter" || e.key === " ") {
                      e.preventDefault();
                      if (bulkPick) toggleSelect(r.id);
                      else nav(`/detail/${r.id}`);
                    }
                  }}
                >
                  <div
                    className={styles.listPosterBlock}
                    onClick={
                      bulkPick
                        ? (e) => {
                            e.stopPropagation();
                            toggleSelect(r.id);
                          }
                        : undefined
                    }
                  >
                    <div
                      className={styles.listPosterInner}
                      data-selected={isListSelected ? "" : undefined}
                    >
                      <MediaPosterImg item={r} className={styles.listPosterImg} />
                      {!bulkPick ? (
                        <button
                          type="button"
                          className={styles.listPlayOverlay}
                          aria-label={t("pages.browse.aria_play")}
                          onClick={(e) => {
                            e.stopPropagation();
                            nav(`/player/${r.id}`);
                          }}
                        >
                          <span className={styles.listPlayCircle}>
                            <CaretRightOutlined />
                          </span>
                        </button>
                      ) : null}
                    </div>
                  </div>
                  <div className={styles.listInfo}>
                    <div className={styles.listTitle}>{r.title || t("pages.favorites.untitled")}</div>
                    <div className={styles.listMeta}>
                      {r.width && r.height ? `${r.width}x${r.height}` : "—"} · {fmtDurationLocalized(r.duration, t)}
                    </div>
                  </div>
                </div>
                {!bulkPick ? (
                  <div className={styles.listMoreSlot}>
                    <Dropdown
                      menu={makeMenu(r)}
                      trigger={["click"]}
                      placement="bottomRight"
                    >
                      <Button
                        type="text"
                        size="small"
                        className={styles.listMoreBtn}
                        icon={<EllipsisOutlined rotate={90} />}
                        aria-label={t("pages.browse.aria_more")}
                        onClick={(e) => e.stopPropagation()}
                      />
                    </Dropdown>
                  </div>
                ) : null}
              </div>
            );
          })}
        </div>
      ) : (
        <div className={viewMode === "poster" ? styles.posterGrid : styles.thumbGrid}>
          {sortedRows.map((r) => {
            const isCardSelected = selectedIds.has(r.id);
            const coverClass = viewMode === "poster" ? styles.posterImage : styles.thumbImage;
            return (
              <div key={r.id} className={viewMode === "poster" ? styles.posterCard : styles.thumbCard}>
                <div
                  className={coverClass}
                  data-selected={isCardSelected ? "" : undefined}
                  data-bulk-pick={bulkPick ? "" : undefined}
                  tabIndex={0}
                  onClick={(e) => {
                    if ((e.target as HTMLElement).closest("[data-browse-card-action]")) return;
                    if (bulkPick) {
                      toggleSelect(r.id);
                      return;
                    }
                    nav(`/detail/${r.id}`);
                  }}
                  onKeyDown={(e) => {
                    if (e.key === "Enter" || e.key === " ") {
                      e.preventDefault();
                      if (bulkPick) toggleSelect(r.id);
                      else nav(`/detail/${r.id}`);
                    }
                  }}
                >
                  <MediaPosterImg
                    item={r}
                    className={styles.gridCoverImg}
                    onLoadStart={(e) => {
                      e.currentTarget.parentElement?.removeAttribute("data-cover-loaded");
                    }}
                    onLoad={(e) => {
                      e.currentTarget.parentElement?.setAttribute("data-cover-loaded", "");
                    }}
                  />
                  <div className={styles.gridHoverShade} aria-hidden={bulkPick ? true : undefined}>
                    {!bulkPick ? (
                      <>
                        <button
                          type="button"
                          data-browse-card-action
                          className={`${styles.gridCornerBtn} ${styles.gridEditBtn}`}
                          aria-label={t("pages.browse.aria_edit")}
                          onClick={(e) => {
                            e.stopPropagation();
                            nav(`/detail/${r.id}`);
                          }}
                        >
                          <EditOutlined />
                        </button>
                        <button
                          type="button"
                          data-browse-card-action
                          className={styles.gridPlayBtn}
                          aria-label={t("pages.browse.aria_play")}
                          onClick={(e) => {
                            e.stopPropagation();
                            nav(`/player/${r.id}`);
                          }}
                        >
                          <CaretRightOutlined />
                        </button>
                        <div className={styles.gridMoreCorner} data-browse-card-action>
                          <Dropdown
                            menu={makeMenu(r)}
                            trigger={["click"]}
                            placement="bottomRight"
                          >
                            <Button
                              type="text"
                              size="small"
                              className={styles.gridMoreIconBtn}
                              icon={<EllipsisOutlined rotate={90} />}
                              aria-label={t("pages.browse.aria_more")}
                              onClick={(e) => e.stopPropagation()}
                            />
                          </Dropdown>
                        </div>
                      </>
                    ) : null}
                  </div>
                  <button
                    type="button"
                    data-browse-card-action
                    className={styles.gridSelectBtn}
                    data-selected={isCardSelected ? "" : undefined}
                    aria-label={isCardSelected ? t("pages.browse.aria_deselect") : t("pages.browse.aria_select")}
                    aria-pressed={isCardSelected}
                    onClick={(e) => {
                      e.stopPropagation();
                      toggleSelect(r.id);
                    }}
                  >
                    {isCardSelected ? <CheckOutlined /> : null}
                  </button>
                </div>
                <div className={styles.cardBody}>
                  <div className={styles.cardTitle}>{r.title || t("pages.favorites.untitled")}</div>
                </div>
              </div>
            );
          })}
        </div>
      )}
      {addToPlaylistMediaId != null && (
        <AddToPlaylistModal
          mediaIds={[addToPlaylistMediaId]}
          open
          defaultNewPlaylistName={addToPlaylistTarget?.title ?? ""}
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
