import { ArrowLeftOutlined, EnvironmentOutlined, PictureOutlined, SyncOutlined, TagOutlined, UserOutlined } from "@ant-design/icons";
import { Alert, Button, Empty, Input, Progress, Select, Space, Spin, Tabs, Tooltip, message } from "antd";
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
import { linkAbortSignal } from "../lib/abortSignal";
import styles from "./PhotoBrowse.module.css";

type Props = {
  libraryId: number;
  libraryName?: string;
  onEmpty?: () => void;
  signal?: AbortSignal;
};

export default function PhotoBrowse({ libraryId, libraryName, onEmpty, signal }: Props) {
  const t = useT();
  const [rows, setRows] = useState<MediaItem[]>([]);
  const [categories, setCategories] = useState<PhotoCategory[]>([]);
  const [places, setPlaces] = useState<PhotoPlace[]>([]);
  const [persons, setPersons] = useState<PhotoPerson[]>([]);
  const [mainTab, setMainTab] = useState<MainTab>("timeline");
  const [drillDown, setDrillDown] = useState<DrillDown | null>(null);
  const [rowsLoading, setRowsLoading] = useState(false);
  const [metaLoading, setMetaLoading] = useState(false);
  const [metaErrors, setMetaErrors] = useState<Partial<Record<MetadataKind, string>>>({});
  const [progressError, setProgressError] = useState<string | null>(null);
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
  type MetadataKind = "categories" | "places" | "persons";
  const taskPendingRef = useRef({ classify: 0, location: 0, face: 0 });
  const rowsGenerationRef = useRef(0);
  const metadataRootGenerationRef = useRef(0);
  const metadataRequestsRef = useRef<Partial<Record<MetadataKind, { rootGeneration: number; generation: number; controller: AbortController; cleanup: () => void; promise: Promise<void>; dirty: boolean }>>>({});
  const progressGenerationRef = useRef(0);
  const progressInFlightRef = useRef<{ generation: number; promise: Promise<void> } | null>(null);

  onEmptyRef.current = onEmpty;

  useEffect(() => {
    onEmptyCalledRef.current = false;
    setDrillDown(null);
    setMainTab("timeline");
    setRows([]);
  }, [libraryId, signal]);

  const refreshMetadataKind = useCallback((kind: MetadataKind): Promise<void> => {
    const rootGeneration = metadataRootGenerationRef.current;
    const existing = metadataRequestsRef.current[kind];
    if (existing?.rootGeneration === rootGeneration) {
      existing.dirty = true;
      return existing.promise;
    }
    const linked = linkAbortSignal(signal);
    const token = {
      rootGeneration,
      generation: (existing?.generation ?? 0) + 1,
      controller: linked.controller,
      cleanup: linked.cleanup,
      promise: Promise.resolve() as Promise<void>,
      dirty: false,
    };
    const fetchKind = () => {
      if (kind === "categories") return fetchPhotoCategories(libraryId, token.controller.signal);
      if (kind === "places") return fetchPhotoPlaces(libraryId, token.controller.signal);
      return fetchPhotoPersons(libraryId, token.controller.signal);
    };
    setMetaLoading(true);
    token.promise = (async () => {
      do {
        token.dirty = false;
        const [result] = await Promise.allSettled([fetchKind()]);
        if (token.controller.signal.aborted || token.rootGeneration !== metadataRootGenerationRef.current || metadataRequestsRef.current[kind] !== token) return;
        if (result.status === "fulfilled") {
          if (kind === "categories") setCategories(result.value as PhotoCategory[]);
          if (kind === "places") setPlaces(result.value as PhotoPlace[]);
          if (kind === "persons") setPersons(result.value as PhotoPerson[]);
          setMetaErrors((prev) => { const next = { ...prev }; delete next[kind]; return next; });
        } else {
          setMetaErrors((prev) => ({ ...prev, [kind]: `Photo metadata unavailable: ${kind}` }));
        }
      } while (token.dirty && !token.controller.signal.aborted && token.rootGeneration === metadataRootGenerationRef.current);
    })().finally(() => {
      token.cleanup();
      if (metadataRequestsRef.current[kind] === token) {
        delete metadataRequestsRef.current[kind];
        if (Object.keys(metadataRequestsRef.current).length === 0) setMetaLoading(false);
      }
    });
    metadataRequestsRef.current[kind] = token;
    return token.promise;
  }, [libraryId, signal]);

  const refreshMetadataKinds = useCallback(async (kinds: Set<MetadataKind>) => {
    await Promise.allSettled([...kinds].map((kind) => refreshMetadataKind(kind)));
  }, [refreshMetadataKind]);

  const refreshSmartMeta = useCallback(() => refreshMetadataKinds(new Set<MetadataKind>(["categories", "places", "persons"])), [refreshMetadataKinds]);

  const refreshProgress = useCallback((requestSignal = signal) => {
    const generation = progressGenerationRef.current;
    const current = progressInFlightRef.current;
    if (current?.generation === generation) return current.promise;
    setProgressError(null);
    const token = { generation, promise: Promise.resolve() as Promise<void> };
    token.promise = (async () => {
      const results = await Promise.allSettled([
        fetchPhotoClassifyProgress(libraryId, requestSignal),
        fetchPhotoLocationProgress(libraryId, requestSignal),
        fetchPhotoFaceProgress(libraryId, requestSignal),
      ]);
      if (requestSignal?.aborted || generation !== progressGenerationRef.current) return;
      const failures = ["classify", "location", "face"].filter((_, index) => results[index].status === "rejected");
      setProgressError(failures.length > 0 ? `Photo progress unavailable: ${failures.join(", ")}` : null);
      const prev = taskPendingRef.current;
      const next = { ...prev };
      const completed = new Set<MetadataKind>();
      const setters = [setClassifyProgress, setLocationProgress, setFaceProgress] as const;
      const keys = ["classify", "location", "face"] as const;
      const metadataKinds: MetadataKind[] = ["categories", "places", "persons"];
      results.forEach((result, index) => {
        if (result.status === "rejected") { setters[index](null); return; }
        const value = result.value;
        const key = keys[index];
        next[key] = value.pending;
        setters[index]({ percent: value.percent, pending: value.pending });
        if (prev[key] > 0 && value.pending <= 0) completed.add(metadataKinds[index]);
        if (key === "face" && prev.face > 0 && value.pending <= 0 && ("failed" in value ? value.failed : 0) > 0) message.warning(t("pages.photo_browse.face_partial_failed", { count: "failed" in value ? value.failed : 0 }));
      });
      taskPendingRef.current = next;
      if (completed.size > 0) await refreshMetadataKinds(completed);
    })().finally(() => {
      if (progressInFlightRef.current === token) progressInFlightRef.current = null;
    });
    progressInFlightRef.current = token;
    return token.promise;
  }, [libraryId, refreshMetadataKinds, signal, t]);

  const loadRows = useCallback(async (requestSignal = signal) => {
    const generation = ++rowsGenerationRef.current;
    setRowsLoading(true);
    try {
      const items = await fetchMedia(libraryId, {
        sort: sortMode, limit: 5000, file_type: "image",
        photo_tag: drillDown && drillDown.section !== "place" && drillDown.section !== "person" ? drillDown.categoryId : undefined,
        photo_place: drillDown?.section === "place" && !isPlaceAllDrill(drillDown) ? drillDown.categoryId : undefined,
        photo_person: drillDown?.section === "person" && !isPersonAllDrill(drillDown) ? drillDown.categoryId : undefined,
      }, requestSignal);
      if (requestSignal?.aborted || generation !== rowsGenerationRef.current) return;
      setRows(items);
      if (items.length === 0 && !drillDown && !onEmptyCalledRef.current) { onEmptyCalledRef.current = true; onEmptyRef.current?.(); }
    } catch (e: unknown) {
      if (!requestSignal?.aborted) message.error((e as Error).message || t("pages.photo_browse.load_failed"));
    } finally {
      if (generation === rowsGenerationRef.current) setRowsLoading(false);
    }
  }, [libraryId, sortMode, drillDown, signal, t]);

  useEffect(() => {
    const linked = linkAbortSignal(signal);
    void loadRows(linked.controller.signal);
    return () => { linked.controller.abort(); linked.cleanup(); };
  }, [loadRows, signal]);

  useEffect(() => {
    const progress = linkAbortSignal(signal);
    metadataRootGenerationRef.current++;
    for (const request of Object.values(metadataRequestsRef.current)) request?.controller.abort();
    metadataRequestsRef.current = {};
    setCategories([]); setPlaces([]); setPersons([]);
    setMetaLoading(false);
    progressGenerationRef.current++;
    progressInFlightRef.current = null;
    taskPendingRef.current = { classify: 0, location: 0, face: 0 };
    setClassifyProgress(null); setLocationProgress(null); setFaceProgress(null);
    setMetaErrors({}); setProgressError(null);
    void refreshSmartMeta();
    void refreshProgress(progress.controller.signal);
    return () => {
      metadataRootGenerationRef.current++;
      for (const request of Object.values(metadataRequestsRef.current)) request?.controller.abort();
      metadataRequestsRef.current = {};
      progressGenerationRef.current++;
      progress.controller.abort(); progress.cleanup();
    };
  }, [libraryId, refreshProgress, refreshSmartMeta, signal]);

  const anyTaskPending = (classifyProgress?.pending ?? 0) > 0 || (locationProgress?.pending ?? 0) > 0 || (faceProgress?.pending ?? 0) > 0;

  useEffect(() => {
    if (!anyTaskPending) return;
    const linked = linkAbortSignal(signal);
    let timer: number | undefined;
    const schedule = () => {
      timer = window.setTimeout(async () => {
        if (document.hidden) { schedule(); return; }
        await refreshProgress(linked.controller.signal);
        if (!linked.controller.signal.aborted && (taskPendingRef.current.classify > 0 || taskPendingRef.current.location > 0 || taskPendingRef.current.face > 0)) schedule();
      }, 8000);
    };
    schedule();
    return () => { if (timer !== undefined) window.clearTimeout(timer); linked.controller.abort(); linked.cleanup(); };
  }, [anyTaskPending, refreshProgress, signal]);

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

  const showTimelineRail = mainTab === "timeline" && !rowsLoading && filtered.length > 0;
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
          {metaLoading && mainTab === "smart" ? <Spin size="small" /> : null}
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

      {Object.values(metaErrors).map((error) => <Alert key={error} type="warning" showIcon message={error} />)}
      {progressError ? <Alert type="warning" showIcon message={progressError} /> : null}

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

      {rowsLoading && rows.length === 0 && !showShelfAll ? (
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
