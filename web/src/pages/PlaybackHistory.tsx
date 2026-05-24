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
import { isAdminRole, useAuthStore } from "../store/auth";
import styles from "./PlaybackHistory.module.css";

type TimeRangeOption = { value: PlaybackHistoryRange; label: string };

const TIME_RANGE_OPTIONS: TimeRangeOption[] = [
  { value: "7d", label: "之前7天" },
  { value: "30d", label: "之前30天" },
  { value: "90d", label: "之前90天" },
  { value: "1y", label: "1年以内" },
  { value: "all", label: "所有时间" },
];

function libraryTypeLabel(libType: string, fileType: string): string {
  switch (libType) {
    case "movie":
      return "电影";
    case "tv":
      return "电视节目";
    case "anime":
      return "动漫";
    case "music":
      return "音乐";
    default:
      break;
  }
  if (fileType) return fileType;
  return "—";
}

function fmtPlayedAt(v?: string): string {
  if (!v) return "—";
  const normalized = v.includes("T") ? v : v.replace(" ", "T");
  const d = new Date(normalized);
  if (Number.isNaN(d.getTime())) return v.replace("T", " ").slice(0, 16);
  const months = ["Jan", "Feb", "Mar", "Apr", "May", "Jun", "Jul", "Aug", "Sep", "Oct", "Nov", "Dec"];
  const hh = String(d.getHours()).padStart(2, "0");
  const mm = String(d.getMinutes()).padStart(2, "0");
  return `${hh}:${mm} ${months[d.getMonth()]} ${d.getDate()}, ${d.getFullYear()}`;
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
      const ta = Date.parse(a.played_at.replace(" ", "T"));
      const tb = Date.parse(b.played_at.replace(" ", "T"));
      const av = Number.isNaN(ta) ? 0 : ta;
      const bv = Number.isNaN(tb) ? 0 : tb;
      return sortDesc ? bv - av : av - bv;
    });
    return list;
  }, [rows, sortDesc]);

  const libraryLabel = libraryId
    ? libs.find((l) => l.id === libraryId)?.name ?? "资料库"
    : "所有资料库";

  const userLabel = userId
    ? users.find((u) => u.id === userId)?.username ?? "用户"
    : "所有用户";

  const timeLabel = TIME_RANGE_OPTIONS.find((o) => o.value === timeRange)?.label ?? "所有时间";

  const libraryMenu: MenuProps = {
    selectedKeys: [libraryId ? String(libraryId) : "all"],
    items: [
      {
        key: "all",
        label: "所有资料库",
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
        label: "所有用户",
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
            播放历史
          </span>
        ) : (
          <h1 className={styles.pageTitle}>播放历史</h1>
        )}
        {mediaId && mediaTitle ? (
          <span className={styles.mediaTitle} title={mediaTitle}>
            {mediaTitle}
          </span>
        ) : null}
        <span className={styles.countBadge}>{sortedRows.length}</span>

        <div className={styles.filters}>
          {!mediaId ? (
            <FilterDropdown label="所有资料库" valueLabel={libraryLabel} menu={libraryMenu} />
          ) : null}
          {admin && !mediaId ? (
            <FilterDropdown label="所有用户" valueLabel={userLabel} menu={userMenu} />
          ) : null}
          <FilterDropdown label="所有时间" valueLabel={timeLabel} menu={timeMenu} />
        </div>
      </div>

      {loading ? (
        <div className={styles.loadingWrap}>
          <Spin indicator={<LoadingOutlined spin />} />
        </div>
      ) : sortedRows.length === 0 ? (
        <div className={styles.emptyWrap}>暂无播放记录</div>
      ) : (
        <div className={styles.tableWrap}>
          <table className={styles.table}>
            <thead>
              <tr>
                <th>用户</th>
                {showGlobalColumns ? <th>类型</th> : null}
                {showGlobalColumns ? <th>标题</th> : null}
                <th>播放器</th>
                <th>平台</th>
                <th
                  className={styles.sortable}
                  onClick={() => setSortDesc((s) => !s)}
                  aria-sort={sortDesc ? "descending" : "ascending"}
                >
                  已播放
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
                    <td>{libraryTypeLabel(r.library_type, r.file_type)}</td>
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
                  <td>{fmtPlayedAt(r.played_at)}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </div>
  );
}
