import { ArrowLeftOutlined, EnvironmentOutlined, PictureOutlined, SyncOutlined, TagOutlined } from "@ant-design/icons";
import { Button, Empty, Input, Progress, Select, Space, Spin, Tabs, Tooltip, message } from "antd";
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import {
  MediaItem,
  PhotoCategory,
  PhotoPlace,
  backfillPhotoLocations,
  enqueuePhotoLibraryClassify,
  fetchMedia,
  fetchPhotoCategories,
  fetchPhotoClassifyProgress,
  fetchPhotoLocationProgress,
  fetchPhotoPlaces,
} from "../api/client";
import { isAdminRole, useAuthStore } from "../store/auth";
import PhotoLightbox from "../components/PhotoLightbox";
import PhotoListView from "../components/PhotoListView";
import PhotoSmartClassify from "../components/PhotoSmartClassify";
import PhotoTimelineRail from "../components/PhotoTimelineRail";
import {
  buildTimelineMarks,
  filterPhotos,
  groupByMonth,
  type DrillDown,
  type LayoutMode,
  type MainTab,
  type SortMode,
} from "../lib/photoBrowseUtils";
import styles from "./PhotoBrowse.module.css";

type Props = {
  libraryId: number;
  libraryName?: string;
  onEmpty?: () => void;
};

export default function PhotoBrowse({ libraryId, libraryName, onEmpty }: Props) {
  const [rows, setRows] = useState<MediaItem[]>([]);
  const [categories, setCategories] = useState<PhotoCategory[]>([]);
  const [places, setPlaces] = useState<PhotoPlace[]>([]);
  const [mainTab, setMainTab] = useState<MainTab>("timeline");
  const [drillDown, setDrillDown] = useState<DrillDown | null>(null);
  const [loading, setLoading] = useState(false);
  const [sortMode, setSortMode] = useState<SortMode>("taken_desc");
  const [layoutMode, setLayoutMode] = useState<LayoutMode>("grid");
  const [q, setQ] = useState("");
  const [lightboxIndex, setLightboxIndex] = useState<number | null>(null);
  const [classifyProgress, setClassifyProgress] = useState<{ percent: number; pending: number } | null>(null);
  const [locationProgress, setLocationProgress] = useState<{ percent: number; pending: number } | null>(null);
  const [reclassifying, setReclassifying] = useState(false);
  const [backfillingPlaces, setBackfillingPlaces] = useState(false);
  const isAdmin = isAdminRole(useAuthStore((s) => s.role));
  const onEmptyRef = useRef(onEmpty);
  const onEmptyCalledRef = useRef(false);

  onEmptyRef.current = onEmpty;

  useEffect(() => {
    onEmptyCalledRef.current = false;
    setDrillDown(null);
    setMainTab("timeline");
  }, [libraryId]);

  const refreshMeta = useCallback(async () => {
    try {
      const [cats, pls, classifyProg, locationProg] = await Promise.all([
        fetchPhotoCategories(libraryId),
        fetchPhotoPlaces(libraryId),
        fetchPhotoClassifyProgress(libraryId),
        fetchPhotoLocationProgress(libraryId),
      ]);
      setCategories(cats);
      setPlaces(pls);
      setClassifyProgress({ percent: classifyProg.percent, pending: classifyProg.pending });
      setLocationProgress({ percent: locationProg.percent, pending: locationProg.pending });
    } catch {
      /* optional */
    }
  }, [libraryId]);

  const load = useCallback(async () => {
    setLoading(true);
    try {
      const items = await fetchMedia(libraryId, {
        sort: sortMode,
        limit: 5000,
        file_type: "image",
        photo_tag: drillDown && drillDown.section !== "place" ? drillDown.categoryId : undefined,
        photo_place: drillDown?.section === "place" ? drillDown.categoryId : undefined,
      });
      setRows(items);
      if (items.length === 0 && !drillDown && !onEmptyCalledRef.current) {
        onEmptyCalledRef.current = true;
        onEmptyRef.current?.();
      }
      await refreshMeta();
    } catch (e: unknown) {
      message.error((e as Error).message || "加载失败");
    } finally {
      setLoading(false);
    }
  }, [libraryId, sortMode, drillDown, refreshMeta]);

  useEffect(() => {
    void load();
  }, [load]);

  useEffect(() => {
    if (classifyProgress == null || classifyProgress.pending <= 0) return;
    const t = window.setInterval(() => {
      void refreshMeta().then(() => load());
    }, 8000);
    return () => window.clearInterval(t);
  }, [classifyProgress?.pending, refreshMeta, load]);

  useEffect(() => {
    if (locationProgress == null || locationProgress.pending <= 0) return;
    const t = window.setInterval(() => {
      void refreshMeta().then(() => load());
    }, 8000);
    return () => window.clearInterval(t);
  }, [locationProgress?.pending, refreshMeta, load]);

  const filtered = useMemo(() => filterPhotos(rows, q), [rows, q]);
  const months = useMemo(() => groupByMonth(filtered, sortMode), [filtered, sortMode]);
  const timelineMarks = useMemo(() => buildTimelineMarks(months), [months]);

  function openAt(id: number) {
    const idx = filtered.findIndex((r) => r.id === id);
    if (idx >= 0) setLightboxIndex(idx);
  }

  async function onBackfillPlaces() {
    setBackfillingPlaces(true);
    try {
      const { queued } = await backfillPhotoLocations(libraryId);
      if (queued > 0) {
        message.success(`已加入 ${queued} 张待解析 GPS 地点`);
      } else {
        message.info("当前没有可解析的图片");
      }
      await refreshMeta();
    } catch (e: unknown) {
      message.error((e as Error).message || "解析地点失败");
    } finally {
      setBackfillingPlaces(false);
    }
  }

  async function onReclassifyAll() {
    setReclassifying(true);
    try {
      const { queued } = await enqueuePhotoLibraryClassify(libraryId, true);
      if (queued > 0) {
        message.success(`已加入 ${queued} 张待重新分类`);
      } else {
        message.info("当前没有可分类的图片");
      }
      await refreshMeta();
    } catch (e: unknown) {
      message.error((e as Error).message || "提交分类任务失败");
    } finally {
      setReclassifying(false);
    }
  }

  function onSmartOpen(next: DrillDown) {
    setDrillDown(next);
  }

  function onDrillBack() {
    setDrillDown(null);
    setMainTab("smart");
  }

  const showTimelineRail = mainTab === "timeline" && !loading && filtered.length > 0 && layoutMode === "grid";
  const listTitle = drillDown?.title;

  return (
    <div className={styles.page}>
      {drillDown ? (
        <div className={styles.drillHeader}>
          <Button type="text" icon={<ArrowLeftOutlined />} onClick={onDrillBack}>
            返回
          </Button>
          <span className={styles.drillTitle}>{listTitle}</span>
        </div>
      ) : (
        <Tabs
          activeKey={mainTab}
          onChange={(k) => setMainTab(k as MainTab)}
          className={styles.mainTabs}
          items={[
            { key: "timeline", label: "时光轴" },
            { key: "smart", label: "智能分类" },
          ]}
        />
      )}

      <div className={styles.topBar}>
        <Space wrap>
          <PictureOutlined style={{ color: "rgba(255,255,255,0.65)" }} />
          <span className={styles.libraryName}>{libraryName || "图片库"}</span>
          <span className={styles.count}>{filtered.length} 张</span>
        </Space>
        <Space wrap>
          <Input.Search
            allowClear
            placeholder="搜索文件名或标签"
            value={q}
            onChange={(e) => setQ(e.target.value)}
            style={{ width: 240 }}
          />
          {(mainTab === "timeline" || drillDown) && (
            <>
              <Select<LayoutMode>
                size="small"
                value={layoutMode}
                onChange={setLayoutMode}
                options={[
                  { value: "grid", label: "网格" },
                  { value: "masonry", label: "瀑布流" },
                ]}
                style={{ width: 100 }}
              />
              <Select<SortMode>
                size="small"
                value={sortMode}
                onChange={setSortMode}
                options={[
                  { value: "taken_desc", label: "按拍摄日期" },
                  { value: "created_desc", label: "按导入日期" },
                ]}
                style={{ width: 130 }}
              />
            </>
          )}
          {isAdmin ? (
            <>
              <Tooltip title="解析 GPS 地点">
                <Button
                  type="text"
                  className={styles.reclassifyBtn}
                  icon={<EnvironmentOutlined />}
                  loading={backfillingPlaces}
                  aria-label="解析 GPS 地点"
                  onClick={() => void onBackfillPlaces()}
                />
              </Tooltip>
              <Tooltip title="全库重新分类">
                <Button
                  type="text"
                  className={styles.reclassifyBtn}
                  icon={<SyncOutlined />}
                  loading={reclassifying}
                  aria-label="全库重新分类"
                  onClick={() => void onReclassifyAll()}
                />
              </Tooltip>
            </>
          ) : null}
        </Space>
      </div>

      {classifyProgress != null && classifyProgress.pending > 0 ? (
        <div className={styles.progressBar}>
          <TagOutlined style={{ marginRight: 8 }} />
          <span>AI 分类进行中</span>
          <Progress percent={classifyProgress.percent} size="small" style={{ flex: 1, margin: "0 12px" }} />
          <span className={styles.progressHint}>剩余 {classifyProgress.pending} 张</span>
        </div>
      ) : null}

      {locationProgress != null && locationProgress.pending > 0 ? (
        <div className={styles.progressBar}>
          <EnvironmentOutlined style={{ marginRight: 8 }} />
          <span>GPS 地点解析进行中</span>
          <Progress percent={locationProgress.percent} size="small" style={{ flex: 1, margin: "0 12px" }} />
          <span className={styles.progressHint}>剩余 {locationProgress.pending} 张</span>
        </div>
      ) : null}

      {loading ? (
        <div className={styles.loadingWrap}>
          <Spin />
        </div>
      ) : mainTab === "smart" && !drillDown ? (
        <PhotoSmartClassify categories={categories} places={places} items={rows} onOpen={onSmartOpen} />
      ) : filtered.length === 0 ? (
        <Empty description={drillDown ? "该分类下暂无图片" : "暂无图片，请先扫描媒体库"} />
      ) : (
        <div className={styles.timelineLayout}>
          <div className={styles.timelineMain}>
            <PhotoListView
              items={filtered}
              layout={layoutMode}
              months={months}
              onOpen={openAt}
              showDateGroups={layoutMode === "grid"}
            />
          </div>
          {showTimelineRail ? <PhotoTimelineRail marks={timelineMarks} /> : null}
        </div>
      )}

      {lightboxIndex != null ? (
        <PhotoLightbox
          items={filtered}
          index={lightboxIndex}
          onClose={() => setLightboxIndex(null)}
          onChangeIndex={setLightboxIndex}
          onTagsUpdated={(id, tags) => {
            setRows((prev) => prev.map((r) => (r.id === id ? { ...r, photo_tags: tags } : r)));
            void refreshMeta();
          }}
        />
      ) : null}
    </div>
  );
}
