import {
  Button,
  Card,
  Popconfirm,
  Select,
  Space,
  Switch,
  Table,
  Tabs,
  Tag,
  Tooltip,
  message,
} from "antd";
import {
  DeleteOutlined,
  RedoOutlined,
  RollbackOutlined,
  StopOutlined,
  SyncOutlined,
} from "@ant-design/icons";
import { useEffect, useMemo, useState, type ReactNode } from "react";
import {
  cancelScanTask,
  cancelTranscodeTask,
  cleanupFailedTranscodeTasks,
  cleanupFailedTranscodeTasksBefore,
  cleanupFailedSubtitleTasks,
  cleanupSubtitleTasksBefore,
  deleteSubtitleTask,
  cleanupFailedLyricTasks,
  cleanupLyricTasksBefore,
  fetchAtrackTasks,
  fetchKeyframeTasks,
  fetchLyricTasks,
  fetchPreviewTasks,
  fetchScanTasks,
  fetchScrapeTasks,
  fetchSubtitleTasks,
  fetchTranscodeTasks,
  resetSubtitleTask,
  retryAudioTrackExtraction,
  retryKeyframeExtraction,
  retryPreviewTask,
  retryLyricTask,
  retryTranscodeTask,
  retrySubtitleTask,
  type AtrackTask,
  type KeyframeTask,
  type LyricTask,
  type PreviewTask,
  type ScrapeTask,
  type ScanTask,
  type SubtitleTask,
  type TranscodeTask,
} from "../api/client";
import { formatServerDateTime } from "../lib/datetime";
import { useT } from "../i18n";

function fmtTaskTs(v?: string) {
  return v ? formatServerDateTime(v) : "-";
}

function ActionIconButton({
  title,
  icon,
  onClick,
  loading,
  disabled,
  danger,
  type = "text",
}: {
  title: string;
  icon: ReactNode;
  onClick?: () => void;
  loading?: boolean;
  disabled?: boolean;
  danger?: boolean;
  type?: "primary" | "text" | "link" | "default";
}) {
  const button = (
    <Button
      type={type}
      size="small"
      icon={icon}
      onClick={onClick}
      loading={loading}
      disabled={disabled}
      danger={danger}
      aria-label={title}
    />
  );
  return (
    <Tooltip title={title}>
      {disabled ? <span>{button}</span> : button}
    </Tooltip>
  );
}

function ActionIconConfirmButton({
  title,
  confirmTitle,
  icon,
  onConfirm,
  loading,
  danger,
  disabled,
}: {
  title: string;
  confirmTitle: string;
  icon: ReactNode;
  onConfirm: () => void;
  loading?: boolean;
  danger?: boolean;
  disabled?: boolean;
}) {
  const button = (
    <Button
      type="text"
      size="small"
      icon={icon}
      loading={loading}
      danger={danger}
      disabled={disabled}
      aria-label={title}
    />
  );

  if (disabled) {
    return (
      <Tooltip title={title}>
        <span>{button}</span>
      </Tooltip>
    );
  }

  return (
    <Popconfirm title={confirmTitle} onConfirm={onConfirm}>
      <Tooltip title={title}>{button}</Tooltip>
    </Popconfirm>
  );
}

export default function TaskManagerPage() {
  const t = useT();
  const [transcodeTasks, setTranscodeTasks] = useState<TranscodeTask[]>([]);
  const [transcodeLoading, setTranscodeLoading] = useState(false);
  const [cleaning, setCleaning] = useState(false);
  const [cleaningOld, setCleaningOld] = useState(false);
  const [previewTasks, setPreviewTasks] = useState<PreviewTask[]>([]);
  const [retryingPreview, setRetryingPreview] = useState<number | null>(null);
  const [scrapeTasks, setScrapeTasks] = useState<ScrapeTask[]>([]);
  const [scrapeLoading, setScrapeLoading] = useState(false);
  const [scanTasks, setScanTasks] = useState<ScanTask[]>([]);
  const [scanLoading, setScanLoading] = useState(false);
  const [cancellingScanId, setCancellingScanId] = useState<number | null>(null);
  const [autoRefresh, setAutoRefresh] = useState(true);
  const [activeTab, setActiveTab] = useState("transcode");
  const [transcodeStatusFilter, setTranscodeStatusFilter] = useState("all");
  const [scrapeStatusFilter, setScrapeStatusFilter] = useState("all");
  const [scanStatusFilter, setScanStatusFilter] = useState("all");
  const [previewStatusFilter, setPreviewStatusFilter] = useState("all");
  const [subtitleTasks, setSubtitleTasks] = useState<SubtitleTask[]>([]);
  const [subtitleLoading, setSubtitleLoading] = useState(false);
  const [subtitleStatusFilter, setSubtitleStatusFilter] = useState("all");
  const [resettingSubtitleId, setResettingSubtitleId] = useState<number | null>(null);
  const [retryingSubtitleId, setRetryingSubtitleId] = useState<number | null>(null);
  const [deletingSubtitleId, setDeletingSubtitleId] = useState<number | null>(null);
  const [cleaningSubtitleFailed, setCleaningSubtitleFailed] = useState(false);
  const [cleaningSubtitleOld, setCleaningSubtitleOld] = useState(false);
  const [atrackTasks, setAtrackTasks] = useState<AtrackTask[]>([]);
  const [atrackLoading, setAtrackLoading] = useState(false);
  const [atrackStatusFilter, setAtrackStatusFilter] = useState("all");
  const [retryingAtrackId, setRetryingAtrackId] = useState<number | null>(null);
  const [keyframeTasks, setKeyframeTasks] = useState<KeyframeTask[]>([]);
  const [keyframeLoading, setKeyframeLoading] = useState(false);
  const [keyframeStatusFilter, setKeyframeStatusFilter] = useState("all");
  const [retryingKeyframeId, setRetryingKeyframeId] = useState<number | null>(null);
  const [lyricTasks, setLyricTasks] = useState<LyricTask[]>([]);
  const [lyricLoading, setLyricLoading] = useState(false);
  const [lyricStatusFilter, setLyricStatusFilter] = useState("all");
  const [retryingLyricId, setRetryingLyricId] = useState<number | null>(null);
  const [cleaningLyricFailed, setCleaningLyricFailed] = useState(false);
  const [cleaningLyricOld, setCleaningLyricOld] = useState(false);

  const loadTranscode = async (silent = false) => {
    if (!silent) setTranscodeLoading(true);
    try {
      setTranscodeTasks(await fetchTranscodeTasks(100));
    } catch {
      if (!silent) setTranscodeTasks([]);
    } finally {
      if (!silent) setTranscodeLoading(false);
    }
  };

  const loadPreview = async (silent = false) => {
    try {
      setPreviewTasks(await fetchPreviewTasks(200));
    } catch {
      if (!silent) setPreviewTasks([]);
    }
  };

  const loadScrape = async (silent = false) => {
    if (!silent) setScrapeLoading(true);
    try {
      setScrapeTasks(await fetchScrapeTasks(200));
    } catch {
      if (!silent) {
        setScrapeTasks([]);
      }
    } finally {
      if (!silent) setScrapeLoading(false);
    }
  };

  const loadScanTasks = async (silent = false) => {
    if (!silent) setScanLoading(true);
    try {
      setScanTasks(await fetchScanTasks(200));
    } catch {
      if (!silent) setScanTasks([]);
    } finally {
      if (!silent) setScanLoading(false);
    }
  };

  const loadSubtitleTasks = async (silent = false) => {
    if (!silent) setSubtitleLoading(true);
    try {
      setSubtitleTasks(await fetchSubtitleTasks(200));
    } catch {
      if (!silent) setSubtitleTasks([]);
    } finally {
      if (!silent) setSubtitleLoading(false);
    }
  };

  const loadAtrackTasks = async (silent = false) => {
    if (!silent) setAtrackLoading(true);
    try {
      setAtrackTasks(await fetchAtrackTasks(100));
    } catch {
      if (!silent) setAtrackTasks([]);
    } finally {
      if (!silent) setAtrackLoading(false);
    }
  };

  const loadKeyframeTasks = async (silent = false) => {
    if (!silent) setKeyframeLoading(true);
    try {
      setKeyframeTasks(await fetchKeyframeTasks(100));
    } catch {
      if (!silent) setKeyframeTasks([]);
    } finally {
      if (!silent) setKeyframeLoading(false);
    }
  };

  const loadLyricTasks = async (silent = false) => {
    if (!silent) setLyricLoading(true);
    try {
      setLyricTasks(await fetchLyricTasks(200));
    } catch {
      if (!silent) setLyricTasks([]);
    } finally {
      if (!silent) setLyricLoading(false);
    }
  };

  useEffect(() => {
    void loadTranscode();
    void loadPreview();
    void loadScrape();
    void loadScanTasks();
    void loadSubtitleTasks();
    void loadAtrackTasks();
    void loadKeyframeTasks();
    void loadLyricTasks();
  }, []);

  useEffect(() => {
    if (!autoRefresh) return;
    const timer = window.setInterval(() => {
      if (activeTab === "transcode") void loadTranscode(true);
      if (activeTab === "scrape") void loadScrape(true);
      if (activeTab === "preview") void loadPreview(true);
      if (activeTab === "scan") void loadScanTasks(true);
      if (activeTab === "subtitle") void loadSubtitleTasks(true);
      if (activeTab === "atrack") void loadAtrackTasks(true);
      if (activeTab === "keyframe") void loadKeyframeTasks(true);
      if (activeTab === "lyric") void loadLyricTasks(true);
    }, 10000);
    return () => window.clearInterval(timer);
  }, [autoRefresh, activeTab]);

  const filteredTranscode = useMemo(
    () => transcodeTasks.filter((x) => (transcodeStatusFilter === "all" ? true : x.status === transcodeStatusFilter)),
    [transcodeTasks, transcodeStatusFilter]
  );
  const filteredScrape = useMemo(
    () => scrapeTasks.filter((x) => (scrapeStatusFilter === "all" ? true : x.status === scrapeStatusFilter)),
    [scrapeTasks, scrapeStatusFilter]
  );
  const filteredPreview = useMemo(
    () => previewTasks.filter((x) => (previewStatusFilter === "all" ? true : x.status === previewStatusFilter)),
    [previewTasks, previewStatusFilter]
  );
  const filteredScan = useMemo(
    () => scanTasks.filter((x) => (scanStatusFilter === "all" ? true : x.status === scanStatusFilter)),
    [scanTasks, scanStatusFilter]
  );
  const filteredSubtitle = useMemo(
    () => subtitleTasks.filter((x) => (subtitleStatusFilter === "all" ? true : x.status === subtitleStatusFilter)),
    [subtitleTasks, subtitleStatusFilter]
  );
  const filteredAtrack = useMemo(
    () => atrackTasks.filter((x) => (atrackStatusFilter === "all" ? true : x.status === atrackStatusFilter)),
    [atrackTasks, atrackStatusFilter]
  );
  const filteredKeyframe = useMemo(
    () => keyframeTasks.filter((x) => (keyframeStatusFilter === "all" ? true : x.status === keyframeStatusFilter)),
    [keyframeTasks, keyframeStatusFilter]
  );
  const filteredLyric = useMemo(
    () => lyricTasks.filter((x) => (lyricStatusFilter === "all" ? true : x.status === lyricStatusFilter)),
    [lyricTasks, lyricStatusFilter]
  );
  const getStatusOptionsForTab = (tab: string) => {
    const commonAll = [{ value: "all", label: t("pages.task_manager.all_statuses") }];
    if (tab === "transcode") {
      return [
        ...commonAll,
        { value: "waiting", label: "waiting" },
        { value: "running", label: "running" },
        { value: "done", label: "done" },
        { value: "failed", label: "failed" },
        { value: "cancelled", label: "cancelled" },
      ];
    }
    if (tab === "scrape") {
      return [
        ...commonAll,
        { value: "waiting", label: "waiting" },
        { value: "running", label: "running" },
        { value: "done", label: "done" },
        { value: "failed", label: "failed" },
      ];
    }
    if (tab === "scan") {
      return [
        ...commonAll,
        { value: "running", label: "running" },
        { value: "done", label: "done" },
        { value: "failed", label: "failed" },
        { value: "cancelled", label: "cancelled" },
      ];
    }
    if (tab === "subtitle" || tab === "lyric") {
      return [
        ...commonAll,
        { value: "pending", label: "pending" },
        { value: "running", label: "running" },
        { value: "done", label: "done" },
        { value: "failed", label: "failed" },
      ];
    }
    return [
      ...commonAll,
      { value: "waiting", label: "waiting" },
      { value: "running", label: "running" },
      { value: "ready", label: "ready" },
      { value: "failed", label: "failed" },
    ];
  };

  const renderListHeaderControls = (
    tab: string,
    statusValue: string,
    onStatusChange: (v: string) => void,
    onRefresh: () => void
  ) => (
    <>
      <Select
        size="small"
        value={statusValue}
        style={{ width: 140 }}
        onChange={onStatusChange}
        options={getStatusOptionsForTab(tab)}
      />
      <Space size={4}>
        <span style={{ color: "#999" }}>{t("pages.task_manager.auto_refresh")}</span>
        <Switch size="small" checked={autoRefresh} onChange={setAutoRefresh} />
      </Space>
      <Button disabled={autoRefresh} onClick={() => void onRefresh()}>
        {t("pages.task_manager.refresh")}
      </Button>
    </>
  );

  return (
    <>
      <Tabs
      activeKey={activeTab}
      onChange={setActiveTab}
      items={[
        {
          key: "transcode",
          label: t("pages.task_manager.tab_transcode"),
          children: (
            <Card
              title={t("pages.task_manager.transcode_card_title")}
              extra={
                <Space>
                  <Popconfirm title={t("pages.task_manager.confirm_cleanup_7d")} onConfirm={() => {
                    setCleaningOld(true);
                    void cleanupFailedTranscodeTasksBefore(7).then((n) => message.success(t("pages.task_manager.cleanup_done", { n }))).catch(() => message.error(t("pages.task_manager.cleanup_failed"))).finally(async () => {
                      setCleaningOld(false);
                      await loadTranscode();
                    });
                  }}>
                    <Button loading={cleaningOld}>{t("pages.task_manager.btn_cleanup_7d")}</Button>
                  </Popconfirm>
                  <Popconfirm title={t("pages.task_manager.confirm_cleanup_all_failed")} onConfirm={() => {
                    setCleaning(true);
                    void cleanupFailedTranscodeTasks().then((n) => message.success(t("pages.task_manager.cleanup_done", { n }))).catch(() => message.error(t("pages.task_manager.cleanup_failed"))).finally(async () => {
                      setCleaning(false);
                      await loadTranscode();
                    });
                  }}>
                    <Button danger loading={cleaning}>{t("pages.task_manager.btn_cleanup_all_failed")}</Button>
                  </Popconfirm>
                  {renderListHeaderControls("transcode", transcodeStatusFilter, setTranscodeStatusFilter, () => void loadTranscode())}
                </Space>
              }
            >
              <Table
                rowKey="id"
                loading={transcodeLoading}
                dataSource={filteredTranscode}
                pagination={{ pageSize: 15 }}
                columns={[
                  { title: "ID", dataIndex: "id", width: 70 },
                  { title: "file_id", dataIndex: "file_id", ellipsis: true },
                  { title: "Pipeline", dataIndex: "pipeline_type", width: 110, render: (v?: string) => v || "-" },
                  { title: t("pages.task_manager.col_quality"), dataIndex: "quality", width: 90 },
                  { title: t("pages.task_manager.col_status"), dataIndex: "status", width: 100 },
                  { title: "DRM", dataIndex: "drm_status", width: 90, render: (v?: string) => v || "-" },
                  { title: "Cleanup", dataIndex: "source_cleanup_status", width: 110, render: (v?: string) => v || "-" },
                  { title: t("pages.task_manager.col_progress"), dataIndex: "progress", width: 80, render: (p: number) => `${p}%` },
                  { title: t("pages.task_manager.col_error"), dataIndex: "error_message", ellipsis: true, render: (v?: string) => v || "-" },
                  { title: t("pages.task_manager.col_created_at"), dataIndex: "created_at", width: 180, render: fmtTaskTs },
                  {
                    title: t("pages.task_manager.col_actions"),
                    key: "ops",
                    width: 90,
                    align: "center",
                    render: (_: unknown, r: TranscodeTask) => (
                      <Space size={4}>
                        {(r.status === "waiting" || r.status === "running") ? (
                          <ActionIconButton
                            title={t("pages.task_manager.tooltip_cancel_task")}
                            icon={<StopOutlined />}
                            onClick={() => {
                              void cancelTranscodeTask(r.id)
                                .then(() => message.success(t("pages.task_manager.task_cancelled")))
                                .then(loadTranscode)
                                .catch(() => message.error(t("pages.task_manager.task_cancel_failed")));
                            }}
                          />
                        ) : null}
                        {(r.status === "failed" || r.status === "cancelled") ? (
                          <ActionIconButton
                            title={t("pages.task_manager.tooltip_retry")}
                            icon={<RedoOutlined />}
                            type="primary"
                            onClick={() => {
                              void retryTranscodeTask(r.id)
                                .then(() => message.success(t("pages.task_manager.retry_submitted")))
                                .then(loadTranscode)
                                .catch(() => message.error(t("pages.task_manager.retry_failed")));
                            }}
                          />
                        ) : null}
                      </Space>
                    ),
                  },
                ]}
              />
            </Card>
          ),
        },
        {
          key: "scrape",
          label: t("pages.task_manager.tab_scrape"),
          children: (
            <Card
              loading={scrapeLoading}
              title={t("pages.task_manager.scrape_card_title")}
              extra={(
                <Space>
                  {renderListHeaderControls("scrape", scrapeStatusFilter, setScrapeStatusFilter, () => void loadScrape())}
                </Space>
              )}
            >
              <Space direction="vertical" style={{ width: "100%" }}>
                <Table
                  rowKey="id"
                  dataSource={filteredScrape}
                  pagination={{ pageSize: 10 }}
                  columns={[
                    { title: "ID", dataIndex: "id", width: 70 },
                    { title: t("pages.task_manager.col_media_id"), dataIndex: "media_id", width: 90 },
                    { title: t("pages.task_manager.col_title"), dataIndex: "title", ellipsis: true },
                    { title: t("pages.task_manager.col_source"), dataIndex: "source", width: 90 },
                    {
                      title: t("pages.task_manager.col_status"),
                      dataIndex: "status",
                      width: 100,
                      render: (v: string) => {
                        const c =
                          v === "done"
                            ? "green"
                            : v === "failed"
                              ? "red"
                              : v === "abandoned"
                                ? "error"
                                : v === "running"
                                  ? "processing"
                                  : "default";
                        const label =
                          v === "done"
                            ? t("pages.task_manager.status_done")
                            : v === "failed"
                              ? t("pages.task_manager.status_failed")
                              : v === "abandoned"
                                ? t("pages.task_manager.status_abandoned")
                                : v === "running"
                                  ? t("pages.task_manager.status_running")
                                  : v === "waiting"
                                    ? t("pages.task_manager.status_waiting")
                                    : v;
                        return <Tag color={c}>{label}</Tag>;
                      },
                    },
                    {
                      title: t("pages.task_manager.col_attempts"),
                      dataIndex: "fail_count",
                      width: 90,
                      render: (v: number | undefined) => (v && v > 0 ? v : "-"),
                    },
                    { title: t("pages.task_manager.col_progress"), dataIndex: "progress", width: 90, render: (v: number) => `${v}%` },
                    { title: t("pages.task_manager.col_message"), dataIndex: "message", ellipsis: true, render: (v?: string) => v || "-" },
                    { title: t("pages.task_manager.col_created_at"), dataIndex: "created_at", width: 180, render: fmtTaskTs },
                    { title: t("pages.task_manager.col_finished_at"), dataIndex: "finished_at", width: 180, render: fmtTaskTs },
                  ]}
                />
              </Space>
            </Card>
          ),
        },
        {
          key: "scan",
          label: t("pages.task_manager.tab_scan"),
          children: (
            <Card
              title={t("pages.task_manager.scan_card_title")}
              extra={(
                <Space>
                  {renderListHeaderControls("scan", scanStatusFilter, setScanStatusFilter, () => void loadScanTasks())}
                </Space>
              )}
            >
              <Table
                rowKey="id"
                loading={scanLoading}
                dataSource={filteredScan}
                pagination={{ pageSize: 10 }}
                scroll={{ x: 1250 }}
                columns={[
                  { title: t("pages.task_manager.col_task_id"), dataIndex: "id", width: 90 },
                  { title: t("pages.task_manager.col_library"), dataIndex: "library_name", ellipsis: true },
                  { title: t("pages.task_manager.col_status"), dataIndex: "status", width: 100 },
                  { title: t("pages.task_manager.col_source"), dataIndex: "source", width: 90 },
                  { title: t("pages.task_manager.col_processed"), dataIndex: "processed_count", width: 90 },
                  { title: t("pages.task_manager.col_added"), dataIndex: "added_count", width: 80 },
                  { title: t("pages.task_manager.col_started_at"), dataIndex: "started_at", width: 180, render: fmtTaskTs },
                  { title: t("pages.task_manager.col_finished_at"), dataIndex: "finished_at", width: 180, render: fmtTaskTs },
                  { title: t("pages.task_manager.col_error_message"), dataIndex: "error_message", ellipsis: true, render: (v?: string) => v || "-" },
                  {
                    title: t("pages.task_manager.col_actions"),
                    key: "actions",
                    width: 80,
                    align: "center",
                    fixed: "right",
                    render: (_: unknown, r: ScanTask) => (
                      <ActionIconButton
                        title={t("pages.task_manager.tooltip_cancel_scan")}
                        icon={<StopOutlined />}
                        disabled={r.status !== "running"}
                        loading={cancellingScanId === r.id}
                        onClick={() => {
                          setCancellingScanId(r.id);
                          void cancelScanTask(r.id)
                            .then(() => message.success(t("pages.task_manager.cancel_requested")))
                            .catch(() => message.error(t("pages.task_manager.task_cancel_failed")))
                            .finally(async () => {
                              setCancellingScanId(null);
                              await loadScanTasks();
                            });
                        }}
                      />
                    ),
                  },
                ]}
              />
            </Card>
          ),
        },
        {
          key: "subtitle",
          label: t("pages.task_manager.tab_subtitle"),
          children: (
            <Card
              title={t("pages.task_manager.subtitle_card_title")}
              extra={(
                <Space>
                  {renderListHeaderControls("subtitle", subtitleStatusFilter, setSubtitleStatusFilter, () => void loadSubtitleTasks())}
                  <Popconfirm
                    title={t("pages.task_manager.confirm_subtitle_cleanup_failed")}
                    onConfirm={() => {
                      setCleaningSubtitleFailed(true);
                      void cleanupFailedSubtitleTasks()
                        .then((n) => message.success(t("pages.task_manager.cleanup_done", { n })))
                        .catch(() => message.error(t("pages.task_manager.cleanup_failed")))
                        .finally(async () => {
                          setCleaningSubtitleFailed(false);
                          await loadSubtitleTasks();
                        });
                    }}
                  >
                    <Button loading={cleaningSubtitleFailed}>{t("pages.task_manager.btn_cleanup_failed_records")}</Button>
                  </Popconfirm>
                  <Popconfirm
                    title={t("pages.task_manager.confirm_subtitle_cleanup_old")}
                    onConfirm={() => {
                      setCleaningSubtitleOld(true);
                      void cleanupSubtitleTasksBefore(30)
                        .then((n) => message.success(t("pages.task_manager.cleanup_done", { n })))
                        .catch(() => message.error(t("pages.task_manager.cleanup_failed")))
                        .finally(async () => {
                          setCleaningSubtitleOld(false);
                          await loadSubtitleTasks();
                        });
                    }}
                  >
                    <Button loading={cleaningSubtitleOld}>{t("pages.task_manager.btn_cleanup_30d_records")}</Button>
                  </Popconfirm>
                </Space>
              )}
            >
              <div style={{ marginBottom: 12, color: "rgba(0,0,0,0.55)", fontSize: 13 }}>
                {t("pages.task_manager.subtitle_help")}
              </div>
              <Table
                rowKey="id"
                loading={subtitleLoading}
                dataSource={filteredSubtitle}
                pagination={{ pageSize: 12 }}
                scroll={{ x: 1200 }}
                columns={[
                  { title: t("pages.task_manager.col_task_id"), dataIndex: "id", width: 80 },
                  { title: t("pages.task_manager.col_media_id"), dataIndex: "media_id", width: 90 },
                  { title: t("pages.task_manager.col_video_title"), dataIndex: "title", ellipsis: true },
                  { title: t("pages.task_manager.col_status"), dataIndex: "status", width: 100, render: (v: string) => {
                    const c = v === "done" ? "green" : v === "failed" ? "red" : v === "running" ? "processing" : "default";
                    return <Tag color={c}>{v}</Tag>;
                  } },
                  { title: t("pages.task_manager.col_note"), dataIndex: "message", ellipsis: true, render: (v?: string) => v || "-" },
                  { title: t("pages.task_manager.col_created_at"), dataIndex: "created_at", width: 170, render: fmtTaskTs },
                  { title: t("pages.task_manager.col_started_at"), dataIndex: "started_at", width: 170, render: fmtTaskTs },
                  { title: t("pages.task_manager.col_finished_at"), dataIndex: "finished_at", width: 170, render: fmtTaskTs },
                  {
                    title: t("pages.task_manager.col_actions"),
                    key: "subactions",
                    width: 120,
                    align: "center",
                    fixed: "right",
                    render: (_: unknown, r: SubtitleTask) => (
                      <Space size={4}>
                        <ActionIconConfirmButton
                          title={t("pages.task_manager.tooltip_reset")}
                          confirmTitle={t("pages.task_manager.confirm_subtitle_reset")}
                          icon={<RollbackOutlined />}
                          loading={resettingSubtitleId === r.media_id}
                          onConfirm={() => {
                            setResettingSubtitleId(r.media_id);
                            void resetSubtitleTask(r.media_id)
                              .then(() => message.success(t("pages.task_manager.reset_success")))
                              .catch(() => message.error(t("pages.task_manager.reset_failed")))
                              .finally(async () => {
                                setResettingSubtitleId(null);
                                await loadSubtitleTasks();
                              });
                          }}
                        />
                        <ActionIconButton
                          title={t("pages.task_manager.tooltip_reprocess")}
                          icon={<SyncOutlined />}
                          type="primary"
                          loading={retryingSubtitleId === r.media_id}
                          onClick={() => {
                            setRetryingSubtitleId(r.media_id);
                            void retrySubtitleTask(r.media_id)
                              .then(() => message.success(t("pages.task_manager.retry_submitted")))
                              .catch(() => message.error(t("pages.task_manager.retry_failed")))
                              .finally(async () => {
                                setRetryingSubtitleId(null);
                                await loadSubtitleTasks();
                              });
                          }}
                        />
                        <ActionIconConfirmButton
                          title={t("pages.task_manager.tooltip_delete")}
                          confirmTitle={t("pages.task_manager.confirm_subtitle_delete")}
                          icon={<DeleteOutlined />}
                          danger
                          disabled={r.status === "running"}
                          loading={deletingSubtitleId === r.media_id}
                          onConfirm={() => {
                            setDeletingSubtitleId(r.media_id);
                            void deleteSubtitleTask(r.media_id)
                              .then(() => message.success(t("common.delete_success")))
                              .catch((err: unknown) => {
                                const msg = (err as { response?: { data?: { error?: string } } })?.response?.data?.error;
                                message.error(msg || t("common.delete_failed"));
                              })
                              .finally(async () => {
                                setDeletingSubtitleId(null);
                                await loadSubtitleTasks();
                              });
                          }}
                        />
                      </Space>
                    ),
                  },
                ]}
              />
            </Card>
          ),
        },
        {
          key: "lyric",
          label: t("pages.task_manager.tab_lyric"),
          children: (
            <Card
              title={t("pages.task_manager.lyric_card_title")}
              extra={(
                <Space>
                  {renderListHeaderControls("lyric", lyricStatusFilter, setLyricStatusFilter, () => void loadLyricTasks())}
                  <Popconfirm
                    title={t("pages.task_manager.confirm_lyric_cleanup_failed")}
                    onConfirm={() => {
                      setCleaningLyricFailed(true);
                      void cleanupFailedLyricTasks()
                        .then((n) => message.success(t("pages.task_manager.cleanup_done", { n })))
                        .catch(() => message.error(t("pages.task_manager.cleanup_failed")))
                        .finally(async () => {
                          setCleaningLyricFailed(false);
                          await loadLyricTasks();
                        });
                    }}
                  >
                    <Button loading={cleaningLyricFailed}>{t("pages.task_manager.btn_cleanup_failed_records")}</Button>
                  </Popconfirm>
                  <Popconfirm
                    title={t("pages.task_manager.confirm_subtitle_cleanup_old")}
                    onConfirm={() => {
                      setCleaningLyricOld(true);
                      void cleanupLyricTasksBefore(30)
                        .then((n) => message.success(t("pages.task_manager.cleanup_done", { n })))
                        .catch(() => message.error(t("pages.task_manager.cleanup_failed")))
                        .finally(async () => {
                          setCleaningLyricOld(false);
                          await loadLyricTasks();
                        });
                    }}
                  >
                    <Button loading={cleaningLyricOld}>{t("pages.task_manager.btn_cleanup_30d_records")}</Button>
                  </Popconfirm>
                </Space>
              )}
            >
              <div style={{ marginBottom: 12, color: "rgba(0,0,0,0.55)", fontSize: 13 }}>
                {t("pages.task_manager.lyric_help")}
              </div>
              <Table
                rowKey="id"
                loading={lyricLoading}
                dataSource={filteredLyric}
                pagination={{ pageSize: 12 }}
                scroll={{ x: 1200 }}
                columns={[
                  { title: t("pages.task_manager.col_task_id"), dataIndex: "id", width: 80 },
                  { title: t("pages.task_manager.col_media_id"), dataIndex: "media_id", width: 90 },
                  { title: t("pages.task_manager.col_track_title"), dataIndex: "title", ellipsis: true },
                  { title: t("pages.task_manager.col_status"), dataIndex: "status", width: 100, render: (v: string) => {
                    const c = v === "done" ? "green" : v === "failed" ? "red" : v === "running" ? "processing" : "default";
                    return <Tag color={c}>{v}</Tag>;
                  } },
                  { title: t("pages.task_manager.col_note"), dataIndex: "message", ellipsis: true, render: (v?: string) => v || "-" },
                  { title: "VTT", dataIndex: "vtt_path", ellipsis: true, render: (v?: string) => v || "-" },
                  { title: "LRC", dataIndex: "lrc_path", ellipsis: true, render: (v?: string) => v || "-" },
                  { title: t("pages.task_manager.col_created_at"), dataIndex: "created_at", width: 170, render: fmtTaskTs },
                  { title: t("pages.task_manager.col_started_at"), dataIndex: "started_at", width: 170, render: fmtTaskTs },
                  { title: t("pages.task_manager.col_finished_at"), dataIndex: "finished_at", width: 170, render: fmtTaskTs },
                  {
                    title: t("pages.task_manager.col_actions"),
                    key: "lyricactions",
                    width: 70,
                    align: "center",
                    fixed: "right",
                    render: (_: unknown, r: LyricTask) => (
                      <ActionIconButton
                        title={t("pages.task_manager.tooltip_retry")}
                        icon={<RedoOutlined />}
                        loading={retryingLyricId === r.media_id}
                        onClick={async () => {
                          setRetryingLyricId(r.media_id);
                          try {
                            await retryLyricTask(r.media_id);
                            message.success(t("pages.task_manager.reprocess_submitted"));
                            await loadLyricTasks();
                          } catch {
                            message.error(t("pages.task_manager.reprocess_failed"));
                          } finally {
                            setRetryingLyricId(null);
                          }
                        }}
                      />
                    ),
                  },
                ]}
              />
            </Card>
          ),
        },
        {
          key: "preview",
          label: t("pages.task_manager.tab_preview"),
          children: (
            <Card
              title={t("pages.task_manager.preview_card_title")}
              extra={(
                <Space>
                  {renderListHeaderControls("preview", previewStatusFilter, setPreviewStatusFilter, () => void loadPreview())}
                </Space>
              )}
            >
              <Table
                rowKey="media_id"
                dataSource={filteredPreview}
                pagination={{ pageSize: 10 }}
                columns={[
                  { title: t("pages.task_manager.col_media_id"), dataIndex: "media_id", width: 90 },
                  { title: t("pages.task_manager.col_title"), dataIndex: "title", ellipsis: true },
                  { title: t("pages.task_manager.col_status"), dataIndex: "status", width: 110 },
                  { title: t("pages.task_manager.col_interval_s"), dataIndex: "interval_sec", width: 90 },
                  { title: t("pages.task_manager.col_thumb_count"), dataIndex: "thumb_count", width: 100 },
                  { title: t("pages.task_manager.col_size"), key: "size", width: 120, render: (_: unknown, r: PreviewTask) => `${r.thumb_width}x${r.thumb_height}` },
                  { title: t("pages.task_manager.col_error_message"), dataIndex: "error_message", ellipsis: true, render: (v?: string) => v || "-" },
                  { title: t("pages.task_manager.col_updated_at"), dataIndex: "updated_at", width: 180, render: fmtTaskTs },
                  {
                    title: t("pages.task_manager.col_actions"),
                    key: "actions",
                    width: 70,
                    align: "center",
                    render: (_: unknown, r: PreviewTask) => (
                      <ActionIconButton
                        title={t("pages.task_manager.tooltip_retry")}
                        icon={<RedoOutlined />}
                        loading={retryingPreview === r.media_id}
                        onClick={() => {
                          setRetryingPreview(r.media_id);
                          void retryPreviewTask(r.media_id).then(() => message.success(t("pages.task_manager.trigger_retry_success"))).then(loadPreview).catch(() => message.error(t("pages.task_manager.retry_failed"))).finally(() => setRetryingPreview(null));
                        }}
                      />
                    ),
                  },
                ]}
              />
            </Card>
          ),
        },
        {
          key: "atrack",
          label: t("pages.task_manager.tab_atrack"),
          children: (
            <Card
              title={t("pages.task_manager.atrack_card_title")}
              extra={(
                <Space>
                  {renderListHeaderControls("atrack", atrackStatusFilter, setAtrackStatusFilter, () => void loadAtrackTasks())}
                </Space>
              )}
            >
              <Table
                rowKey="media_id"
                loading={atrackLoading}
                dataSource={filteredAtrack}
                pagination={{ pageSize: 10 }}
                columns={[
                  { title: t("pages.task_manager.col_media_id"), dataIndex: "media_id", width: 90 },
                  { title: t("pages.task_manager.col_title"), dataIndex: "title", ellipsis: true },
                  { title: t("pages.task_manager.col_status"), dataIndex: "status", width: 110 },
                  { title: t("pages.task_manager.col_output_dir"), dataIndex: "output_dir", ellipsis: true, render: (v?: string) => v || "-" },
                  { title: t("pages.task_manager.col_error_message"), dataIndex: "error_message", ellipsis: true, render: (v?: string) => v || "-" },
                  { title: t("pages.task_manager.col_updated_at"), dataIndex: "updated_at", width: 180, render: fmtTaskTs },
                  {
                    title: t("pages.task_manager.col_actions"),
                    key: "actions",
                    width: 70,
                    align: "center",
                    render: (_: unknown, r: AtrackTask) => (
                      <ActionIconButton
                        title={t("pages.task_manager.tooltip_retry")}
                        icon={<RedoOutlined />}
                        loading={retryingAtrackId === r.media_id}
                        onClick={async () => {
                          setRetryingAtrackId(r.media_id);
                          try {
                            await retryAudioTrackExtraction(r.media_id);
                            message.success(t("pages.task_manager.trigger_retry_success"));
                            await loadAtrackTasks();
                          } catch {
                            message.error(t("pages.task_manager.retry_failed"));
                          } finally {
                            setRetryingAtrackId(null);
                          }
                        }}
                      />
                    ),
                  },
                ]}
              />
            </Card>
          ),
        },
        {
          key: "keyframe",
          label: t("pages.task_manager.tab_keyframe"),
          children: (
            <Card
              title={t("pages.task_manager.keyframe_card_title")}
              extra={(
                <Space>
                  {renderListHeaderControls("keyframe", keyframeStatusFilter, setKeyframeStatusFilter, () => void loadKeyframeTasks())}
                </Space>
              )}
            >
              <Table
                rowKey="media_id"
                loading={keyframeLoading}
                dataSource={filteredKeyframe}
                pagination={{ pageSize: 10 }}
                columns={[
                  { title: t("pages.task_manager.col_media_id"), dataIndex: "media_id", width: 90 },
                  { title: t("pages.task_manager.col_title"), dataIndex: "title", ellipsis: true },
                  { title: t("pages.task_manager.col_status"), dataIndex: "status", width: 110 },
                  { title: t("pages.task_manager.col_keyframe_count"), dataIndex: "keyframe_count", width: 100 },
                  { title: t("pages.task_manager.col_output_dir"), dataIndex: "output_dir", ellipsis: true, render: (v?: string) => v || "-" },
                  { title: t("pages.task_manager.col_error_message"), dataIndex: "error_message", ellipsis: true, render: (v?: string) => v || "-" },
                  { title: t("pages.task_manager.col_updated_at"), dataIndex: "updated_at", width: 180, render: fmtTaskTs },
                  {
                    title: t("pages.task_manager.col_actions"),
                    key: "actions",
                    width: 70,
                    align: "center",
                    render: (_: unknown, r: KeyframeTask) => (
                      <ActionIconButton
                        title={t("pages.task_manager.tooltip_retry")}
                        icon={<RedoOutlined />}
                        loading={retryingKeyframeId === r.media_id}
                        onClick={async () => {
                          setRetryingKeyframeId(r.media_id);
                          try {
                            await retryKeyframeExtraction(r.media_id);
                            message.success(t("pages.task_manager.trigger_retry_success"));
                            await loadKeyframeTasks();
                          } catch {
                            message.error(t("pages.task_manager.retry_failed"));
                          } finally {
                            setRetryingKeyframeId(null);
                          }
                        }}
                      />
                    ),
                  },
                ]}
              />
            </Card>
          ),
        },
      ]}
      />
    </>
  );
}
