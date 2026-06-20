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
import { useT } from "../i18n";
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
  const t = useT();
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

      if (prev.face > 0 && next.face <= 0 && (faceProg.failed ?? 0) > 0) {
        message.warning(t("pages.photo_browse.face_partial_failed", { count: faceProg.failed }));
      }

      if (taskFinished) {
        await refreshSmartMeta();
      }
    } catch {
      /* optional */
    }
  }, [libraryId, refreshSmartMeta, t]);

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
      message.error((e as Error).message || t("pages.photo_browse.load_failed"));
    } finally {
      setLoading(false);
    }
  }, [libraryId, sortMode, drillDown, refreshMeta, t]);

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
        message.success(t("pages.photo_browse.queued_faces", { count: queued }));
      } else {
        message.info(t("pages.photo_browse.no_faces"));
      }
      await refreshProgress();
    } catch (e: unknown) {
      message.error((e as Error).message || t("pages.photo_browse.face_failed"));
    } finally {
      setBackfillingFaces(false);
    }
  }

  async function onBackfillPlaces() {
    setBackfillingPlaces(true);
    try {
      const { queued } = await backfillPhotoLocations(libraryId);
      if (queued > 0) {
        message.success(t("pages.photo_browse.queued_places", { count: queued }));
      } else {
        message.info(t("pages.photo_browse.no_places"));
      }
      await refreshProgress();
    } catch (e: unknown) {
      message.error((e as Error).message || t("pages.photo_browse.geo_failed"));
    } finally {
      setBackfillingPlaces(false);
    }
  }

  async function onReclassifyAll() {
    setReclassifying(true);
    try {
      const { queued } = await enqueuePhotoLibraryClassify(libraryId, true);
      if (queued > 0) {
        message.success(t("pages.photo_browse.queued_classify", { count: queued }));
      } else {
        message.info(t("pages.photo_browse.no_classify"));
      }
      await refreshProgress();
    } catch (e: unknown) {
      message.error((e as Error).message || t("pages.photo_browse.classify_failed"));
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
            {t("pages.photo_browse.back")}
          </Button>
          {drillDown.section === "person" && !isPersonAllDrill(drillDown) ? (
            <PhotoPersonDrillTitle
              libraryId={libraryId}
              personId={drillDown.categoryId}
              name={listTitle || t("pages.photo_browse.unnamed_person")}
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
            { key: "timeline", label: t("pages.photo_browse.tab_timeline") },
            { key: "smart", label: t("pages.photo_browse.tab_smart") },
          ]}
        />
      )}

      <div className={styles.topBar}>
        <Space wrap>
          <PictureOutlined style={{ color: "rgba(255,255,255,0.65)" }} />
          <span className={styles.libraryName}>{libraryName || t("pages.photo_browse.library_fallback")}</span>
          <span className={styles.count}>{t("pages.photo_browse.count_photos", { count: filtered.length })}</span>
        </Space>
        <Space wrap>
          {isAdmin && mainTab !== "timeline" ? (
            <>
              <Tooltip title={t("pages.photo_browse.tooltip_face_detect")}>
                <Button
                  type="text"
                  className={styles.reclassifyBtn}
                  icon={<UserOutlined />}
                  loading={backfillingFaces}
                  aria-label={t("pages.photo_browse.aria_face_detect")}
                  onClick={() => void onBackfillFaces()}
                />
              </Tooltip>
              <Tooltip title={t("pages.photo_browse.tooltip_geo")}>
                <Button
                  type="text"
                  className={styles.reclassifyBtn}
                  icon={<EnvironmentOutlined />}
                  loading={backfillingPlaces}
                  aria-label={t("pages.photo_browse.aria_geo")}
                  onClick={() => void onBackfillPlaces()}
                />
              </Tooltip>
              <Tooltip title={t("pages.photo_browse.tooltip_reclassify")}>
                <Button
                  type="text"
                  className={styles.reclassifyBtn}
                  icon={<SyncOutlined />}
                  loading={reclassifying}
                  aria-label={t("pages.photo_browse.aria_reclassify")}
                  onClick={() => void onReclassifyAll()}
                />
              </Tooltip>
            </>
          ) : null}
          <Input.Search
            allowClear
            placeholder={t("pages.photo_browse.search_placeholder")}
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
                  { value: "grid", label: t("pages.photo_browse.layout_grid") },
                  { value: "masonry", label: t("pages.photo_browse.layout_masonry") },
                ]}
                style={{ width: 100 }}
              />
              <Select<SortMode>
                size="small"
                value={sortMode}
                onChange={setSortMode}
                options={[
                  { value: "taken_desc", label: t("pages.photo_browse.sort_taken") },
                  { value: "created_desc", label: t("pages.photo_browse.sort_created") },
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
          <span>{t("pages.photo_browse.ai_classify_in_progress")}</span>
          <Progress percent={classifyProgress.percent} size="small" style={{ flex: 1, margin: "0 12px" }} />
          <span className={styles.progressHint}>{t("pages.photo_browse.remaining_photos", { count: classifyProgress.pending })}</span>
        </div>
      ) : null}

      {locationProgress != null && locationProgress.pending > 0 ? (
        <div className={styles.progressBar}>
          <EnvironmentOutlined style={{ marginRight: 8 }} />
          <span>{t("pages.photo_browse.geo_in_progress")}</span>
          <Progress percent={locationProgress.percent} size="small" style={{ flex: 1, margin: "0 12px" }} />
          <span className={styles.progressHint}>{t("pages.photo_browse.remaining_photos", { count: locationProgress.pending })}</span>
        </div>
      ) : null}

      {faceProgress != null && faceProgress.pending > 0 ? (
        <div className={styles.progressBar}>
          <UserOutlined style={{ marginRight: 8 }} />
          <span>{t("pages.photo_browse.face_in_progress")}</span>
          <Progress percent={faceProgress.percent} size="small" style={{ flex: 1, margin: "0 12px" }} />
          <span className={styles.progressHint}>{t("pages.photo_browse.remaining_photos", { count: faceProgress.pending })}</span>
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
        <Empty description={drillDown ? t("pages.photo_browse.no_photos_in_category") : t("pages.photo_browse.no_photos_scan_first")} />
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
