import { CaretDownOutlined, CaretUpOutlined, LoadingOutlined } from "@ant-design/icons";
import { Dropdown, Spin } from "antd";
import type { MenuProps } from "antd";
import { useCallback, useEffect, useMemo, useState } from "react";
import { Link, useNavigate, useSearchParams } from "react-router-dom";
import {
  fetchAdminUsers,
  fetchLibraries,
  fetchMediaDetail,
  fetchPlaybackHistory,
  type AdminUser,
  type Library,
  type PlaybackHistoryItem,
  type PlaybackHistoryRange,
} from "../api/client";
import { formatServerDateTime, serverDateTimeToMillis } from "../lib/datetime";
import { useT, type TranslateFn } from "../i18n";
import { isAdminRole, useAuthStore } from "../store/auth";
import styles from "./PlaybackHistory.module.css";

type TimeRangeOption = { value: PlaybackHistoryRange; label: string };

function buildTimeRangeOptions(t: TranslateFn): TimeRangeOption[] {
  return [
    { value: "7d", label: t("pages.playback_history.range_7d") },
    { value: "30d", label: t("pages.playback_history.range_30d") },
    { value: "90d", label: t("pages.playback_history.range_90d") },
    { value: "1y", label: t("pages.playback_history.range_1y") },
    { value: "all", label: t("pages.playback_history.range_all") },
  ];
}

function libraryTypeLabel(libType: string, fileType: string, t: TranslateFn): string {
  switch (libType) {
    case "movie":
      return t("pages.playback_history.type_movie");
    case "tv":
      return t("pages.playback_history.type_tv");
    case "anime":
      return t("pages.playback_history.type_anime");
    case "music":
      return t("pages.playback_history.type_music");
    default:
      break;
  }
  if (fileType) return fileType;
  return "—";
}

function FilterDropdown({
  label,
  valueLabel,
  menu,
}: {
  label: string;
  valueLabel: string;
  menu: MenuProps;
}) {
  return (
    <Dropdown menu={menu} trigger={["click"]}>
      <span
        className={styles.filterSelect}
        role="button"
        tabIndex={0}
        style={{ cursor: "pointer", color: "rgba(255,255,255,0.65)" }}
      >
        {valueLabel || label}
        <CaretDownOutlined style={{ marginLeft: 6, fontSize: 10 }} />
      </span>
    </Dropdown>
  );
}

export default function PlaybackHistoryPage() {
  const nav = useNavigate();
  const [searchParams] = useSearchParams();
  const role = useAuthStore((s) => s.role);
  const admin = isAdminRole(role);
  const t = useT();
  const TIME_RANGE_OPTIONS = useMemo(() => buildTimeRangeOptions(t), [t]);

  const mediaIdParam = searchParams.get("media_id");
  const mediaId =
    mediaIdParam && !Number.isNaN(Number(mediaIdParam)) ? Number(mediaIdParam) : undefined;

  const [rows, setRows] = useState<PlaybackHistoryItem[]>([]);
  const [loading, setLoading] = useState(false);
  const [mediaTitle, setMediaTitle] = useState("");
  const [libs, setLibs] = useState<Library[]>([]);
  const [users, setUsers] = useState<AdminUser[]>([]);
  const [libraryId, setLibraryId] = useState<number | undefined>(undefined);
  const [userId, setUserId] = useState<number | undefined>(undefined);
  const [timeRange, setTimeRange] = useState<PlaybackHistoryRange>("all");
  const [sortDesc, setSortDesc] = useState(true);

  const load = useCallback(async () => {
    setLoading(true);
    try {
      const items = await fetchPlaybackHistory({
        limit: 500,
        media_id: mediaId,
        library_id: libraryId,
        user_id: admin ? userId : undefined,
        range: timeRange,
      });
      setRows(items);
    } catch {
      setRows([]);
    } finally {
      setLoading(false);
    }
  }, [admin, libraryId, mediaId, timeRange, userId]);

  useEffect(() => {
    void load();
  }, [load]);

  useEffect(() => {
    if (!mediaId) {
      setMediaTitle("");
      return;
    }
    void fetchMediaDetail(mediaId)
      .then((d) => setMediaTitle(d.title || d.file_id || String(mediaId)))
      .catch(() => setMediaTitle(String(mediaId)));
  }, [mediaId]);

  useEffect(() => {
    void fetchLibraries()
      .then(setLibs)
      .catch(() => setLibs([]));
  }, []);

  useEffect(() => {
    if (!admin) return;
    void fetchAdminUsers()
      .then(setUsers)
      .catch(() => setUsers([]));
  }, [admin]);

  const sortedRows = useMemo(() => {
    const list = [...rows];
    list.sort((a, b) => {
      const ta = serverDateTimeToMillis(a.played_at);
      const tb = serverDateTimeToMillis(b.played_at);
      const av = Number.isNaN(ta) ? 0 : ta;
      const bv = Number.isNaN(tb) ? 0 : tb;
      return sortDesc ? bv - av : av - bv;
    });
    return list;
  }, [rows, sortDesc]);

  const libraryLabel = libraryId
    ? libs.find((l) => l.id === libraryId)?.name ?? t("pages.playback_history.library_fallback")
    : t("pages.playback_history.all_libraries");

  const userLabel = userId
    ? users.find((u) => u.id === userId)?.username ?? t("pages.playback_history.user_fallback")
    : t("pages.playback_history.all_users");

  const timeLabel = TIME_RANGE_OPTIONS.find((o) => o.value === timeRange)?.label ?? t("pages.playback_history.all_time");

  const libraryMenu: MenuProps = {
    selectedKeys: [libraryId ? String(libraryId) : "all"],
    items: [
      {
        key: "all",
        label: t("pages.playback_history.all_libraries"),
        onClick: () => setLibraryId(undefined),
      },
      ...libs.map((l) => ({
        key: String(l.id),
        label: l.name,
        onClick: () => setLibraryId(l.id),
      })),
    ],
  };

  const userMenu: MenuProps = {
    selectedKeys: [userId ? String(userId) : "all"],
    items: [
      {
        key: "all",
        label: t("pages.playback_history.all_users"),
        onClick: () => setUserId(undefined),
      },
      ...users.map((u) => ({
        key: String(u.id),
        label: u.username,
        onClick: () => setUserId(u.id),
      })),
    ],
  };

  const timeMenu: MenuProps = {
    selectedKeys: [timeRange],
    items: TIME_RANGE_OPTIONS.map((o) => ({
      key: o.value,
      label: o.label,
      onClick: () => setTimeRange(o.value),
    })),
  };

  const showGlobalColumns = !mediaId;

  return (
    <div style={{ padding: "0 0 32px" }}>
      <div className={styles.headerBar}>
        {mediaId ? (
          <span
            className={styles.backLink}
            role="button"
            tabIndex={0}
            onClick={() => nav("/playback-history")}
            onKeyDown={(e) => {
              if (e.key === "Enter" || e.key === " ") nav("/playback-history");
            }}
          >
            {t("pages.playback_history.title")}
          </span>
        ) : (
          <h1 className={styles.pageTitle}>{t("pages.playback_history.title")}</h1>
        )}
        {mediaId && mediaTitle ? (
          <span className={styles.mediaTitle} title={mediaTitle}>
            {mediaTitle}
          </span>
        ) : null}
        <span className={styles.countBadge}>{sortedRows.length}</span>

        <div className={styles.filters}>
          {!mediaId ? (
            <FilterDropdown label={t("pages.playback_history.all_libraries")} valueLabel={libraryLabel} menu={libraryMenu} />
          ) : null}
          {admin && !mediaId ? (
            <FilterDropdown label={t("pages.playback_history.all_users")} valueLabel={userLabel} menu={userMenu} />
          ) : null}
          <FilterDropdown label={t("pages.playback_history.all_time")} valueLabel={timeLabel} menu={timeMenu} />
        </div>
      </div>

      {loading ? (
        <div className={styles.loadingWrap}>
          <Spin indicator={<LoadingOutlined spin />} />
        </div>
      ) : sortedRows.length === 0 ? (
        <div className={styles.emptyWrap}>{t("pages.playback_history.no_history")}</div>
      ) : (
        <div className={styles.tableWrap}>
          <table className={styles.table}>
            <thead>
              <tr>
                <th>{t("pages.playback_history.col_user")}</th>
                {showGlobalColumns ? <th>{t("pages.playback_history.col_type")}</th> : null}
                {showGlobalColumns ? <th>{t("pages.playback_history.col_title")}</th> : null}
                <th>{t("pages.playback_history.col_player")}</th>
                <th>{t("pages.playback_history.col_platform")}</th>
                <th
                  className={styles.sortable}
                  onClick={() => setSortDesc((s) => !s)}
                  aria-sort={sortDesc ? "descending" : "ascending"}
                >
                  {t("pages.playback_history.col_played_at")}
                  <span className={styles.sortIcon}>
                    {sortDesc ? <CaretDownOutlined /> : <CaretUpOutlined />}
                  </span>
                </th>
              </tr>
            </thead>
            <tbody>
              {sortedRows.map((r) => (
                <tr key={r.id}>
                  <td>{r.username || "—"}</td>
                  {showGlobalColumns ? (
                    <td>{libraryTypeLabel(r.library_type, r.file_type, t)}</td>
                  ) : null}
                  {showGlobalColumns ? (
                    <td className={styles.titleCell}>
                      {r.media_id > 0 ? (
                        <Link to={`/detail/${r.media_id}`} style={{ color: "inherit" }}>
                          {r.title || `#${r.media_id}`}
                        </Link>
                      ) : (
                        r.title || "—"
                      )}
                    </td>
                  ) : null}
                  <td>{r.player || "—"}</td>
                  <td>{r.platform || "—"}</td>
                  <td>{formatServerDateTime(r.played_at)}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </div>
  );
}
