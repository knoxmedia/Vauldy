import { ArrowLeftOutlined, EnvironmentOutlined, PictureOutlined, SyncOutlined, TagOutlined, UserOutlined } from "@ant-design/icons";
import { Button, Empty, Input, Progress, Select, Space, Spin, Tabs, Tooltip, message } from "antd";
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import {
  MediaItem,
  PhotoCategory,
  PhotoPlace,
  PhotoPerson,
  backfillPhotoFaces,
  backfillPhotoLocations,
  enqueuePhotoLibraryClassify,
  fetchMedia,
  fetchPhotoCategories,
  fetchPhotoFaceProgress,
  fetchPhotoClassifyProgress,
  fetchPhotoLocationProgress,
  fetchPhotoPersons,
  fetchPhotoPlaces,
} from "../api/client";
import { isAdminRole, useAuthStore } from "../store/auth";
import PhotoLightbox from "../components/PhotoLightbox";
import PhotoListView from "../components/PhotoListView";
import PhotoPersonDrillTitle from "../components/PhotoPersonDrillTitle";
import PhotoSmartClassify, { PhotoPersonAllGrid, PhotoPlaceAllGrid } from "../components/PhotoSmartClassify";
import PhotoTimelineRail from "../components/PhotoTimelineRail";
import {
  buildTimelineMarks,
  filterPhotos,
  groupByMonth,
  isPersonAllDrill,
  isPlaceAllDrill,
  isShelfAllDrill,
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
  const [persons, setPersons] = useState<PhotoPerson[]>([]);
  const [mainTab, setMainTab] = useState<MainTab>("timeline");
  const [drillDown, setDrillDown] = useState<DrillDown | null>(null);
  const [loading, setLoading] = useState(false);
  const [sortMode, setSortMode] = useState<SortMode>("taken_desc");
  const [layoutMode, setLayoutMode] = useState<LayoutMode>("grid");
  const [q, setQ] = useState("");
  const [lightboxIndex, setLightboxIndex] = useState<number | null>(null);
  const [classifyProgress, setClassifyProgress] = useState<{ percent: number; pending: number } | null>(null);
  const [locationProgress, setLocationProgress] = useState<{ percent: number; pending: number } | null>(null);
  const [faceProgress, setFaceProgress] = useState<{ percent: number; pending: number } | null>(null);
  const [reclassifying, setReclassifying] = useState(false);
  const [backfillingPlaces, setBackfillingPlaces] = useState(false);
  const [backfillingFaces, setBackfillingFaces] = useState(false);
  const isAdmin = isAdminRole(useAuthStore((s) => s.role));
  const onEmptyRef = useRef(onEmpty);
  const onEmptyCalledRef = useRef(false);
  const taskPendingRef = useRef({ classify: 0, location: 0, face: 0 });

  onEmptyRef.current = onEmpty;

  useEffect(() => {
    onEmptyCalledRef.current = false;
    setDrillDown(null);
    setMainTab("timeline");
    setRows([]);
  }, [libraryId]);

  const refreshSmartMeta = useCallback(async () => {
    try {
      const [cats, pls, ppl] = await Promise.all([
        fetchPhotoCategories(libraryId),
        fetchPhotoPlaces(libraryId),
        fetchPhotoPersons(libraryId),
      ]);
      setCategories(cats);
      setPlaces(pls);
      setPersons(ppl);
    } catch {
      /* optional */
    }
  }, [libraryId]);

  const refreshProgress = useCallback(async () => {
    try {
      const [classifyProg, locationProg, faceProg] = await Promise.all([
        fetchPhotoClassifyProgress(libraryId),
        fetchPhotoLocationProgress(libraryId),
        fetchPhotoFaceProgress(libraryId),
      ]);
      const next = {
        classify: classifyProg.pending,
        location: locationProg.pending,
        face: faceProg.pending,
      };
      const prev = taskPendingRef.current;
      const taskFinished =
        (prev.classify > 0 && next.classify <= 0) ||
        (prev.location > 0 && next.location <= 0) ||
        (prev.face > 0 && next.face <= 0);
      taskPendingRef.current = next;

      setClassifyProgress({ percent: classifyProg.percent, pending: classifyProg.pending });
      setLocationProgress({ percent: locationProg.percent, pending: locationProg.pending });
      setFaceProgress({ percent: faceProg.percent, pending: faceProg.pending });

      if (taskFinished) {
        await refreshSmartMeta();
      }
    } catch {
      /* optional */
    }
  }, [libraryId, refreshSmartMeta]);

  const refreshMeta = useCallback(async () => {
    await Promise.all([refreshSmartMeta(), refreshProgress()]);
  }, [refreshSmartMeta, refreshProgress]);

  const load = useCallback(async () => {
    setLoading(true);
    try {
      const items = await fetchMedia(libraryId, {
        sort: sortMode,
        limit: 5000,
        file_type: "image",
        photo_tag: drillDown && drillDown.section !== "place" && drillDown.section !== "person" ? drillDown.categoryId : undefined,
        photo_place:
          drillDown?.section === "place" && !isPlaceAllDrill(drillDown) ? drillDown.categoryId : undefined,
        photo_person: drillDown?.section === "person" && !isPersonAllDrill(drillDown) ? drillDown.categoryId : undefined,
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

  const anyTaskPending =
    (classifyProgress?.pending ?? 0) > 0 ||
    (locationProgress?.pending ?? 0) > 0 ||
    (faceProgress?.pending ?? 0) > 0;

  useEffect(() => {
    if (!anyTaskPending) return;
    const t = window.setInterval(() => {
      void refreshProgress();
    }, 8000);
    return () => window.clearInterval(t);
  }, [anyTaskPending, refreshProgress]);

  const filtered = useMemo(() => filterPhotos(rows, q), [rows, q]);
  const months = useMemo(() => groupByMonth(filtered, sortMode), [filtered, sortMode]);
  const timelineMarks = useMemo(() => buildTimelineMarks(months), [months]);

  function openAt(id: number) {
    const idx = filtered.findIndex((r) => r.id === id);
    if (idx >= 0) setLightboxIndex(idx);
  }

  async function onBackfillFaces() {
    setBackfillingFaces(true);
    try {
      const { queued } = await backfillPhotoFaces(libraryId);
      if (queued > 0) {
        message.success(`已加入 ${queued} 张待人脸检测`);
      } else {
        message.info("当前没有可检测的图片");
      }
      await refreshProgress();
    } catch (e: unknown) {
      message.error((e as Error).message || "人脸检测失败");
    } finally {
      setBackfillingFaces(false);
    }
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
      await refreshProgress();
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
      await refreshProgress();
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

  const showTimelineRail = mainTab === "timeline" && !loading && filtered.length > 0;
  const showPersonAll = isPersonAllDrill(drillDown);
  const showPlaceAll = isPlaceAllDrill(drillDown);
  const showShelfAll = isShelfAllDrill(drillDown);
  const listTitle = drillDown?.title;

  return (
    <div className={styles.page}>
      {drillDown ? (
        <div className={styles.drillHeader}>
          <Button type="text" icon={<ArrowLeftOutlined />} onClick={onDrillBack}>
            返回
          </Button>
          {drillDown.section === "person" && !isPersonAllDrill(drillDown) ? (
            <PhotoPersonDrillTitle
              libraryId={libraryId}
              personId={drillDown.categoryId}
              name={listTitle || "未命名人物"}
              onRenamed={(name) => {
                setDrillDown({ ...drillDown, title: name });
                setPersons((prev) =>
                  prev.map((p) => (String(p.id) === drillDown.categoryId ? { ...p, name } : p)),
                );
              }}
            />
          ) : (
            <span className={styles.drillTitle}>{listTitle}</span>
          )}
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
          {isAdmin && mainTab !== "timeline" ? (
            <>
              <Tooltip title="人脸检测与聚类">
                <Button
                  type="text"
                  className={styles.reclassifyBtn}
                  icon={<UserOutlined />}
                  loading={backfillingFaces}
                  aria-label="人脸检测"
                  onClick={() => void onBackfillFaces()}
                />
              </Tooltip>
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
          <Input.Search
            allowClear
            placeholder="搜索文件名或标签"
            value={q}
            onChange={(e) => setQ(e.target.value)}
            style={{ width: 240 }}
          />
          {(mainTab === "timeline" || (drillDown && !showShelfAll)) && (
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

      {faceProgress != null && faceProgress.pending > 0 ? (
        <div className={styles.progressBar}>
          <UserOutlined style={{ marginRight: 8 }} />
          <span>人脸检测进行中</span>
          <Progress percent={faceProgress.percent} size="small" style={{ flex: 1, margin: "0 12px" }} />
          <span className={styles.progressHint}>剩余 {faceProgress.pending} 张</span>
        </div>
      ) : null}

      {loading && rows.length === 0 && !showShelfAll ? (
        <div className={styles.loadingWrap}>
          <Spin />
        </div>
      ) : showPersonAll ? (
        <PhotoPersonAllGrid persons={persons} onOpen={onSmartOpen} />
      ) : showPlaceAll ? (
        <PhotoPlaceAllGrid places={places} onOpen={onSmartOpen} />
      ) : mainTab === "smart" && !drillDown ? (
        <PhotoSmartClassify categories={categories} places={places} persons={persons} items={rows} onOpen={onSmartOpen} />
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
              showDateGroups
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
            void refreshSmartMeta();
          }}
        />
      ) : null}
    </div>
  );
}
