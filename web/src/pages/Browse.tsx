import {
  AppstoreOutlined,
  ArrowDownOutlined,
  ArrowUpOutlined,
  BarsOutlined,
  PictureOutlined,
  TableOutlined,
} from "@ant-design/icons";
import { Button, Empty, Segmented, Select, Space, Spin, Table, message } from "antd";
import { useEffect, useMemo, useState } from "react";
import { Link, useNavigate, useSearchParams } from "react-router-dom";
import { MediaItem, fetchMedia, mediaPosterSrc } from "../api/client";
import styles from "./Browse.module.css";

type ViewMode = "poster" | "thumb" | "list" | "table";
type SortField = "title" | "added" | "played" | "release_date" | "year" | "type" | "quality" | "bitrate";
type SortOrder = "asc" | "desc";
const BROWSE_PREFS_KEY = "knox.browse.prefs.v1";

function readBrowsePrefs(): { viewMode: ViewMode; sortField: SortField; sortOrder: SortOrder } | null {
  if (typeof window === "undefined") return null;
  try {
    const raw = window.localStorage.getItem(BROWSE_PREFS_KEY);
    if (!raw) return null;
    const parsed = JSON.parse(raw) as { viewMode?: ViewMode; sortField?: SortField; sortOrder?: SortOrder };
    const viewMode: ViewMode = ["poster", "thumb", "list", "table"].includes(String(parsed.viewMode))
      ? (parsed.viewMode as ViewMode)
      : "table";
    const sortField: SortField = ["title", "added", "played", "release_date", "year", "type", "quality", "bitrate"].includes(
      String(parsed.sortField)
    )
      ? (parsed.sortField as SortField)
      : "added";
    const sortOrder: SortOrder = parsed.sortOrder === "asc" || parsed.sortOrder === "desc" ? parsed.sortOrder : "desc";
    return { viewMode, sortField, sortOrder };
  } catch {
    return null;
  }
}

/** 浏览全部媒体：按库筛选、最近添加（原「我的媒体」列表能力） */
export default function BrowsePage() {
  const nav = useNavigate();
  const [searchParams] = useSearchParams();
  const libraryIdParam = searchParams.get("library_id");
  const sortParam = searchParams.get("sort");
  const qParam = searchParams.get("q")?.trim() ?? "";

  const libFromUrl =
    libraryIdParam && !Number.isNaN(Number(libraryIdParam))
      ? Number(libraryIdParam)
      : undefined;

  const [rows, setRows] = useState<MediaItem[]>([]);
  const [loading, setLoading] = useState(false);
  const [viewMode, setViewMode] = useState<ViewMode>(() => readBrowsePrefs()?.viewMode ?? "table");
  const [sortField, setSortField] = useState<SortField>(() => readBrowsePrefs()?.sortField ?? "added");
  const [sortOrder, setSortOrder] = useState<SortOrder>(() => readBrowsePrefs()?.sortOrder ?? "desc");

  async function load() {
    setLoading(true);
    try {
      const opts =
        sortParam === "recent"
          ? ({ sort: "created_desc" as const, limit: 200 })
          : undefined;
      setRows(await fetchMedia(libFromUrl, opts));
    } catch (e: unknown) {
      message.error((e as Error).message || "加载失败");
    } finally {
      setLoading(false);
    }
  }

  useEffect(() => {
    void load();
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [libFromUrl, sortParam]);

  useEffect(() => {
    if (sortParam === "recent") {
      setSortField("added");
      setSortOrder("desc");
    }
  }, [sortParam]);

  useEffect(() => {
    if (typeof window === "undefined") return;
    window.localStorage.setItem(
      BROWSE_PREFS_KEY,
      JSON.stringify({
        viewMode,
        sortField,
        sortOrder,
      })
    );
  }, [viewMode, sortField, sortOrder]);

  const displayRows = useMemo<MediaItem[]>(() => {
    if (!qParam) return rows;
    const q = qParam.toLowerCase();
    return rows.filter((r) => (r.title ?? "").toLowerCase().includes(q));
  }, [rows, qParam]);

  const sortedRows = useMemo<MediaItem[]>(() => {
    const list = [...displayRows];
    const factor = sortOrder === "asc" ? 1 : -1;

    const timeVal = (v?: string) => {
      if (!v) return 0;
      const t = Date.parse(v);
      return Number.isNaN(t) ? 0 : t;
    };
    const yearVal = (r: MediaItem) => {
      if ((r.year ?? 0) > 0) return r.year ?? 0;
      const m = (r.title ?? "").match(/(19|20)\d{2}/) || (r.file_path ?? "").match(/(19|20)\d{2}/);
      return m ? Number(m[0]) : 0;
    };
    const qualityVal = (r: MediaItem) => Math.max(r.width ?? 0, r.height ?? 0);

    list.sort((a, b) => {
      switch (sortField) {
        case "title":
          return (a.title ?? "").localeCompare(b.title ?? "", "zh-CN") * factor;
        case "added":
          return (timeVal(a.created_at) - timeVal(b.created_at)) * factor;
        case "played":
          return (timeVal(a.last_play_at) - timeVal(b.last_play_at)) * factor;
        case "release_date":
          return (timeVal(a.release_date) - timeVal(b.release_date)) * factor;
        case "year":
          return (yearVal(a) - yearVal(b)) * factor;
        case "type":
          return (a.file_type ?? "").localeCompare(b.file_type ?? "", "zh-CN") * factor;
        case "quality":
          return (qualityVal(a) - qualityVal(b)) * factor;
        case "bitrate":
          return ((a.bitrate ?? 0) - (b.bitrate ?? 0)) * factor;
        default:
          return 0;
      }
    });
    return list;
  }, [displayRows, sortField, sortOrder]);

  const fmtDate = (v?: string) => (v ? v.replace("T", " ").slice(0, 19) : "—");
  const fmtResolution = (r: MediaItem) => (r.width && r.height ? `${r.width}x${r.height}` : "—");
  const fmtBitrate = (v?: number) => (v && v > 0 ? `${Math.round(v / 1000)} kbps` : "—");
  const posterLike = (r: MediaItem) => r.file_type === "video";

  return (
    <div style={{ padding: "16px 0 32px" }}>
      <div className={styles.topBar}>
        <Space wrap className={styles.topLeftTools}>
          <Select<SortField>
            size="small"
            value={sortField}
            onChange={setSortField}
            options={[
              { value: "title", label: "按标题" },
              { value: "added", label: "按添加日期" },
              { value: "played", label: "按播放日期" },
              { value: "release_date", label: "按发行日期" },
              { value: "year", label: "按年份" },
              { value: "type", label: "按类型" },
              { value: "quality", label: "按清晰度" },
              { value: "bitrate", label: "按码率" },
            ]}
            style={{ width: 150 }}
          />
          <Button size="small" onClick={() => setSortOrder((s) => (s === "asc" ? "desc" : "asc"))}>
            {sortOrder === "asc" ? <ArrowUpOutlined /> : <ArrowDownOutlined />}
          </Button>
        </Space>
        <Space wrap className={styles.topRightTools}>
          <Segmented<ViewMode>
            size="small"
            value={viewMode}
            onChange={(v) => setViewMode(v as ViewMode)}
            options={[
              { value: "poster", icon: <PictureOutlined />, label: "海报" },
              { value: "thumb", icon: <AppstoreOutlined />, label: "缩略图" },
              { value: "list", icon: <BarsOutlined />, label: "列表" },
              { value: "table", icon: <TableOutlined />, label: "表格" },
            ]}
          />
        </Space>
      </div>

      {loading ? (
        <div className={styles.loadingWrap}>
          <Spin />
        </div>
      ) : sortedRows.length === 0 ? (
        <Empty description="暂无媒体" />
      ) : viewMode === "table" ? (
        <Table
          rowKey="id"
          loading={loading}
          dataSource={sortedRows}
          pagination={{ pageSize: 20 }}
          columns={[
            { title: "ID", dataIndex: "id", width: 70 },
            { title: "标题", dataIndex: "title" },
            { title: "类型", dataIndex: "file_type", width: 90 },
            { title: "添加日期", width: 170, render: (_, r) => fmtDate(r.created_at) },
            { title: "播放日期", width: 170, render: (_, r) => fmtDate(r.last_play_at) },
            { title: "发行日期", width: 130, render: (_, r) => r.release_date || "—" },
            { title: "年份", width: 90, render: (_, r) => r.year || "—" },
            { title: "清晰度", width: 110, render: (_, r) => fmtResolution(r) },
            { title: "码率", width: 110, render: (_, r) => fmtBitrate(r.bitrate) },
            { title: "操作", key: "op", width: 150, render: (_, r) => <Link to={`/detail/${r.id}`}>详情</Link> },
          ]}
        />
      ) : viewMode === "list" ? (
        <div className={styles.listWrap}>
          {sortedRows.map((r) => (
            <div key={r.id} className={styles.listItem}>
              <div className={styles.listMain} onClick={() => nav(`/detail/${r.id}`)} role="button" tabIndex={0}>
                <div className={styles.listTitle}>{r.title || "未命名"}</div>
                <div className={styles.listMeta}>
                  {r.file_type} · {fmtResolution(r)} · {fmtBitrate(r.bitrate)} · 添加 {fmtDate(r.created_at)}
                </div>
              </div>
              <Space>
                <Button size="small" onClick={() => nav(`/player/${r.id}`)}>播放</Button>
                <Button size="small" onClick={() => nav(`/detail/${r.id}`)}>详情</Button>
              </Space>
            </div>
          ))}
        </div>
      ) : (
        <div className={viewMode === "poster" ? styles.posterGrid : styles.thumbGrid}>
          {sortedRows.map((r) => (
            <div key={r.id} className={viewMode === "poster" ? styles.posterCard : styles.thumbCard}>
              <div
                className={viewMode === "poster" ? styles.posterImage : styles.thumbImage}
                onClick={() => nav(`/detail/${r.id}`)}
                role="button"
                tabIndex={0}
              >
                <img
                  className={styles.coverImg}
                  src={mediaPosterSrc(r)}
                  alt=""
                  loading="lazy"
                  decoding="async"
                  onError={(e) => {
                    e.currentTarget.style.display = "none";
                  }}
                />
                <span className={styles.mediaTypeBadge}>{posterLike(r) ? "VIDEO" : (r.file_type || "MEDIA").toUpperCase()}</span>
              </div>
              <div className={styles.cardBody}>
                <div className={styles.cardTitle}>{r.title || "未命名"}</div>
                <div className={styles.cardMeta}>
                  {r.year || "—"} · {fmtResolution(r)}
                </div>
                <Space size={6}>
                  <Button size="small" onClick={() => nav(`/player/${r.id}`)}>播放</Button>
                  <Button size="small" onClick={() => nav(`/detail/${r.id}`)}>详情</Button>
                </Space>
              </div>
            </div>
          ))}
        </div>
      )}
    </div>
  );
}
