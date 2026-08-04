import {
  Button,
  Card,
  Popconfirm,
  Select,
  Space,
  Table,
  Tabs,
  Tag,
  Tooltip,
  message,
} from "antd";
import type { Key } from "react";
import {
  ClearOutlined,
  ClockCircleOutlined,
  DeleteOutlined,
  RedoOutlined,
  ReloadOutlined,
  RollbackOutlined,
  StopOutlined,
  ThunderboltOutlined,
} from "@ant-design/icons";
import { useEffect, useMemo, useState, type ReactNode } from "react";
import {
  cancelScanTask,
  cancelTranscodeTask,
  cancelEncryptTask,
  cleanupFailedTranscodeTasks,
  cancelSubtitleTask,
  cleanupFailedSubtitleTasks,
  cleanupSubtitleTasksBefore,
  deleteSubtitleTask,
  deleteEncryptTask,
  runNowSubtitleTask,
  cleanupFailedLyricTasks,
  cleanupLyricTasksBefore,
  fetchAtrackTasks,
  fetchEncryptTasks,
  fetchKeyframeTasks,
  fetchLyricTasks,
  fetchPreviewTasks,
  fetchScanTasks,
  fetchScrapeTasks,
  fetchSubtitleTasks,
  fetchTranscodeTasks,
  resetEncryptTask,
  retryAudioTrackExtraction,
  retryKeyframeExtraction,
  retryPreviewTask,
  retryLyricTask,
  retryTranscodeTask,
  retrySubtitleTask,
  batchSubtitleTasks,
  batchEncryptTasks,
  batchTranscodeTasks,
  batchLyricTasks,
  batchPreviewTasks,
  batchAtrackTasks,
  batchKeyframeTasks,
  type BatchTaskAction,
  type AtrackTask,
  type EncryptTask,
  type KeyframeTask,
  type LyricTask,
  type PreviewTask,
  type ScrapeTask,
  type ScanTask,
  type SubtitleTask,
  type TranscodeTask,
} from "../api/client";
import { formatServerDateTime } from "../lib/datetime";
import { matchesDisplayStatus, toDisplayStatus } from "../lib/taskDisplayStatus";
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
  type = "text",
}: {
  title: string;
  confirmTitle: string;
  icon: ReactNode;
  onConfirm: () => void;
  loading?: boolean;
  danger?: boolean;
  disabled?: boolean;
  type?: "primary" | "text" | "link" | "default";
}) {
  const button = (
    <Button
      type={type}
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
  const [previewTasks, setPreviewTasks] = useState<PreviewTask[]>([]);
  const [retryingPreview, setRetryingPreview] = useState<number | null>(null);
  const [scrapeTasks, setScrapeTasks] = useState<ScrapeTask[]>([]);
  const [scrapeLoading, setScrapeLoading] = useState(false);
  const [scanTasks, setScanTasks] = useState<ScanTask[]>([]);
  const [scanLoading, setScanLoading] = useState(false);
  const [cancellingScanId, setCancellingScanId] = useState<number | null>(null);
  const autoRefresh = true;
  const [activeTab, setActiveTab] = useState("transcode");
  const [transcodeStatusFilter, setTranscodeStatusFilter] = useState("all");
  const [scrapeStatusFilter, setScrapeStatusFilter] = useState("all");
  const [scanStatusFilter, setScanStatusFilter] = useState("all");
  const [previewStatusFilter, setPreviewStatusFilter] = useState("all");
  const [subtitleTasks, setSubtitleTasks] = useState<SubtitleTask[]>([]);
  const [subtitleLoading, setSubtitleLoading] = useState(false);
  const [subtitleStatusFilter, setSubtitleStatusFilter] = useState("all");
  const [retryingSubtitleId, setRetryingSubtitleId] = useState<number | null>(null);
  const [cancellingSubtitleId, setCancellingSubtitleId] = useState<number | null>(null);
  const [runNowSubtitleId, setRunNowSubtitleId] = useState<number | null>(null);
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
  const [encryptTasks, setEncryptTasks] = useState<EncryptTask[]>([]);
  const [encryptLoading, setEncryptLoading] = useState(false);
  const [encryptStatusFilter, setEncryptStatusFilter] = useState("all");
  const [cancellingEncryptId, setCancellingEncryptId] = useState<number | null>(null);
  const [resettingEncryptId, setResettingEncryptId] = useState<number | null>(null);
  const [deletingEncryptId, setDeletingEncryptId] = useState<number | null>(null);
  const [selectedTranscodeKeys, setSelectedTranscodeKeys] = useState<Key[]>([]);
  const [selectedSubtitleKeys, setSelectedSubtitleKeys] = useState<Key[]>([]);
  const [selectedLyricKeys, setSelectedLyricKeys] = useState<Key[]>([]);
  const [selectedEncryptKeys, setSelectedEncryptKeys] = useState<Key[]>([]);
  const [selectedPreviewKeys, setSelectedPreviewKeys] = useState<Key[]>([]);
  const [selectedAtrackKeys, setSelectedAtrackKeys] = useState<Key[]>([]);
  const [selectedKeyframeKeys, setSelectedKeyframeKeys] = useState<Key[]>([]);
  const [batchLoading, setBatchLoading] = useState(false);

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

  const loadEncryptTasks = async (silent = false) => {
    if (!silent) setEncryptLoading(true);
    try {
      setEncryptTasks(await fetchEncryptTasks(200));
    } catch {
      if (!silent) setEncryptTasks([]);
    } finally {
      if (!silent) setEncryptLoading(false);
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
    void loadEncryptTasks();
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
      if (activeTab === "encrypt") void loadEncryptTasks(true);
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
    () => previewTasks.filter((x) => matchesDisplayStatus("preview", x.status, previewStatusFilter)),
    [previewTasks, previewStatusFilter]
  );
  const filteredScan = useMemo(
    () => scanTasks.filter((x) => (scanStatusFilter === "all" ? true : x.status === scanStatusFilter)),
    [scanTasks, scanStatusFilter]
  );
  const filteredSubtitle = useMemo(
    () => subtitleTasks.filter((x) => matchesDisplayStatus("subtitle", x.status, subtitleStatusFilter)),
    [subtitleTasks, subtitleStatusFilter]
  );
  const filteredAtrack = useMemo(
    () => atrackTasks.filter((x) => matchesDisplayStatus("atrack", x.status, atrackStatusFilter)),
    [atrackTasks, atrackStatusFilter]
  );
  const filteredKeyframe = useMemo(
    () => keyframeTasks.filter((x) => matchesDisplayStatus("keyframe", x.status, keyframeStatusFilter)),
    [keyframeTasks, keyframeStatusFilter]
  );
  const filteredLyric = useMemo(
    () => lyricTasks.filter((x) => (lyricStatusFilter === "all" ? true : x.status === lyricStatusFilter)),
    [lyricTasks, lyricStatusFilter]
  );
  const filteredEncrypt = useMemo(
    () => encryptTasks.filter((x) => (encryptStatusFilter === "all" ? true : x.status === encryptStatusFilter)),
    [encryptTasks, encryptStatusFilter]
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
    if (tab === "subtitle") {
      return [
        ...commonAll,
        { value: "waiting", label: "waiting" },
        { value: "running", label: "running" },
        { value: "done", label: "done" },
        { value: "failed", label: "failed" },
        { value: "cancelled", label: "cancelled" },
      ];
    }
    if (tab === "lyric") {
      return [
        ...commonAll,
        { value: "pending", label: "pending" },
        { value: "running", label: "running" },
        { value: "done", label: "done" },
        { value: "failed", label: "failed" },
      ];
    }
    if (tab === "encrypt") {
      return [
        ...commonAll,
        { value: "waiting", label: "waiting" },
        { value: "running", label: "running" },
        { value: "done", label: "done" },
        { value: "failed", label: "failed" },
        { value: "cancelled", label: "cancelled" },
      ];
    }
    if (tab === "preview" || tab === "atrack" || tab === "keyframe") {
      return [
        ...commonAll,
        { value: "waiting", label: "waiting" },
        { value: "running", label: "running" },
        { value: "done", label: "done" },
        { value: "failed", label: "failed" },
        { value: "cancelled", label: "cancelled" },
      ];
    }
    return [
      ...commonAll,
      { value: "waiting", label: "waiting" },
      { value: "running", label: "running" },
      { value: "done", label: "done" },
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
        style={{ width: 120 }}
        onChange={onStatusChange}
        options={getStatusOptionsForTab(tab)}
      />
      <ActionIconButton
        title={t("pages.task_manager.refresh")}
        icon={<ReloadOutlined />}
        type="default"
        onClick={() => void onRefresh()}
      />
    </>
  );

  const runBatch = async (
    action: BatchTaskAction,
    selected: Key[],
    idOf: (key: Key) => number,
    runner: (action: BatchTaskAction, ids: number[]) => Promise<{ ok: number; failed: number }>,
    reload: () => Promise<void>,
    clear: () => void,
  ) => {
    const ids = selected.map(idOf).filter((n) => n > 0);
    if (ids.length === 0) {
      message.warning(t("pages.task_manager.batch_select_required"));
      return;
    }
    setBatchLoading(true);
    try {
      const res = await runner(action, ids);
      if (res.failed > 0) {
        message.warning(t("pages.task_manager.batch_partial", { ok: res.ok, failed: res.failed }));
      } else {
        message.success(t("pages.task_manager.batch_done", { n: res.ok }));
      }
      clear();
      await reload();
    } catch {
      message.error(t("pages.task_manager.batch_failed"));
    } finally {
      setBatchLoading(false);
    }
  };

  const renderBatchToolbar = (
    selected: Key[],
    onAction: (action: BatchTaskAction) => void,
  ) => (
    <>
      {selected.length > 0 ? (
        <Tag style={{ marginInlineEnd: 0 }}>
          {t("pages.task_manager.batch_selected", { n: selected.length })}
        </Tag>
      ) : null}
      <ActionIconButton
        title={t("pages.task_manager.batch_run_now")}
        icon={<ThunderboltOutlined />}
        type="default"
        disabled={selected.length === 0}
        loading={batchLoading}
        onClick={() => onAction("run_now")}
      />
      <ActionIconButton
        title={t("pages.task_manager.batch_retry")}
        icon={<RedoOutlined />}
        type="default"
        disabled={selected.length === 0}
        loading={batchLoading}
        onClick={() => onAction("retry")}
      />
      <ActionIconButton
        title={t("pages.task_manager.batch_stop")}
        icon={<StopOutlined />}
        type="default"
        disabled={selected.length === 0}
        loading={batchLoading}
        onClick={() => onAction("cancel")}
      />
      <ActionIconConfirmButton
        title={t("pages.task_manager.batch_delete")}
        confirmTitle={t("pages.task_manager.confirm_batch_delete")}
        icon={<DeleteOutlined />}
        type="default"
        danger
        disabled={selected.length === 0}
        loading={batchLoading}
        onConfirm={() => onAction("delete")}
      />
    </>
  );

  const toolbarDivider = (
    <div
      aria-hidden
      style={{
        width: 1,
        alignSelf: "stretch",
        minHeight: 22,
        marginInline: 4,
        background: "rgba(127,127,127,0.35)",
      }}
    />
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
                <Space size={4} wrap={false}>
                  {renderListHeaderControls("transcode", transcodeStatusFilter, setTranscodeStatusFilter, () => void loadTranscode())}
                  {toolbarDivider}
                  {renderBatchToolbar(selectedTranscodeKeys, (action) => {
                    void runBatch(
                      action,
                      selectedTranscodeKeys,
                      (k) => Number(k),
                      batchTranscodeTasks,
                      async () => { await loadTranscode(); },
                      () => setSelectedTranscodeKeys([]),
                    );
                  })}
                  <ActionIconConfirmButton
                    title={t("pages.task_manager.btn_cleanup_all_failed")}
                    confirmTitle={t("pages.task_manager.confirm_cleanup_all_failed")}
                    icon={<ClearOutlined />}
                    type="default"
                    danger
                    loading={cleaning}
                    onConfirm={() => {
                      setCleaning(true);
                      void cleanupFailedTranscodeTasks().then((n) => message.success(t("pages.task_manager.cleanup_done", { n }))).catch(() => message.error(t("pages.task_manager.cleanup_failed"))).finally(async () => {
                        setCleaning(false);
                        await loadTranscode();
                      });
                    }}
                  />
                </Space>
              }
            >
              <Table
                rowKey="id"
                loading={transcodeLoading}
                dataSource={filteredTranscode}
                rowSelection={{
                  selectedRowKeys: selectedTranscodeKeys,
                  onChange: setSelectedTranscodeKeys,
                }}
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
                <Space size={4} wrap={false}>
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
                <Space size={4} wrap={false}>
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
                <Space size={4} wrap={false}>
                  {renderListHeaderControls("subtitle", subtitleStatusFilter, setSubtitleStatusFilter, () => void loadSubtitleTasks())}
                  {toolbarDivider}
                  {renderBatchToolbar(selectedSubtitleKeys, (action) => {
                    void runBatch(
                      action,
                      selectedSubtitleKeys,
                      (k) => subtitleTasks.find((r) => r.id === Number(k))?.media_id ?? 0,
                      batchSubtitleTasks,
                      async () => { await loadSubtitleTasks(); },
                      () => setSelectedSubtitleKeys([]),
                    );
                  })}
                  <ActionIconConfirmButton
                    title={t("pages.task_manager.btn_cleanup_failed_records")}
                    confirmTitle={t("pages.task_manager.confirm_subtitle_cleanup_failed")}
                    icon={<ClearOutlined />}
                    type="default"
                    danger
                    loading={cleaningSubtitleFailed}
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
                  />
                  <ActionIconConfirmButton
                    title={t("pages.task_manager.btn_cleanup_30d_records")}
                    confirmTitle={t("pages.task_manager.confirm_subtitle_cleanup_old")}
                    icon={<ClockCircleOutlined />}
                    type="default"
                    loading={cleaningSubtitleOld}
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
                  />
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
                rowSelection={{
                  selectedRowKeys: selectedSubtitleKeys,
                  onChange: setSelectedSubtitleKeys,
                }}
                pagination={{ pageSize: 12 }}
                scroll={{ x: 1200 }}
                columns={[
                  { title: t("pages.task_manager.col_task_id"), dataIndex: "id", width: 80 },
                  { title: t("pages.task_manager.col_media_id"), dataIndex: "media_id", width: 90 },
                  { title: t("pages.task_manager.col_video_title"), dataIndex: "title", ellipsis: true },
                  { title: t("pages.task_manager.col_status"), dataIndex: "status", width: 100, render: (v: string) => {
                    const display = toDisplayStatus("subtitle", v);
                    const c = display === "done" ? "green" : display === "failed" ? "red" : display === "running" ? "processing" : "default";
                    return <Tag color={c}>{display}</Tag>;
                  } },
                  { title: t("pages.task_manager.col_note"), dataIndex: "message", ellipsis: true, render: (v?: string) => v || "-" },
                  { title: t("pages.task_manager.col_created_at"), dataIndex: "created_at", width: 170, render: fmtTaskTs },
                  { title: t("pages.task_manager.col_started_at"), dataIndex: "started_at", width: 170, render: fmtTaskTs },
                  { title: t("pages.task_manager.col_finished_at"), dataIndex: "finished_at", width: 170, render: fmtTaskTs },
                  {
                    title: t("pages.task_manager.col_actions"),
                    key: "subactions",
                    width: 130,
                    align: "center",
                    fixed: "right",
                    render: (_: unknown, r: SubtitleTask) => {
                      const s = toDisplayStatus("subtitle", r.status);
                      return (
                        <Space size={4}>
                          {s === "running" ? (
                            <ActionIconButton
                              title={t("pages.task_manager.tooltip_cancel_subtitle")}
                              icon={<StopOutlined />}
                              loading={cancellingSubtitleId === r.media_id}
                              onClick={() => {
                                setCancellingSubtitleId(r.media_id);
                                void cancelSubtitleTask(r.media_id)
                                  .then(() => message.success(t("pages.task_manager.cancel_requested")))
                                  .catch(() => message.error(t("pages.task_manager.task_cancel_failed")))
                                  .finally(async () => {
                                    setCancellingSubtitleId(null);
                                    await loadSubtitleTasks();
                                  });
                              }}
                            />
                          ) : null}
                          {s === "waiting" ? (
                            <ActionIconButton
                              title={t("pages.task_manager.tooltip_run_now_subtitle")}
                              icon={<ThunderboltOutlined />}
                              type="primary"
                              loading={runNowSubtitleId === r.media_id}
                              onClick={() => {
                                setRunNowSubtitleId(r.media_id);
                                void runNowSubtitleTask(r.media_id)
                                  .then(() => message.success(t("pages.task_manager.run_now_submitted")))
                                  .catch(() => message.error(t("pages.task_manager.run_now_failed")))
                                  .finally(async () => {
                                    setRunNowSubtitleId(null);
                                    await loadSubtitleTasks();
                                  });
                              }}
                            />
                          ) : null}
                          {s === "failed" || s === "cancelled" ? (
                            <ActionIconButton
                              title={t("pages.task_manager.tooltip_reprocess")}
                              icon={<RedoOutlined />}
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
                          ) : null}
                          {s === "waiting" || s === "failed" || s === "cancelled" ? (
                            <ActionIconConfirmButton
                              title={t("pages.task_manager.tooltip_delete")}
                              confirmTitle={t("pages.task_manager.confirm_subtitle_delete")}
                              icon={<DeleteOutlined />}
                              danger
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
                          ) : null}
                        </Space>
                      );
                    },
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
                <Space size={4} wrap={false}>
                  {renderListHeaderControls("lyric", lyricStatusFilter, setLyricStatusFilter, () => void loadLyricTasks())}
                  {toolbarDivider}
                  {renderBatchToolbar(selectedLyricKeys, (action) => {
                    void runBatch(
                      action,
                      selectedLyricKeys,
                      (k) => lyricTasks.find((r) => r.id === Number(k))?.media_id ?? 0,
                      batchLyricTasks,
                      async () => { await loadLyricTasks(); },
                      () => setSelectedLyricKeys([]),
                    );
                  })}
                  <ActionIconConfirmButton
                    title={t("pages.task_manager.btn_cleanup_failed_records")}
                    confirmTitle={t("pages.task_manager.confirm_lyric_cleanup_failed")}
                    icon={<ClearOutlined />}
                    type="default"
                    danger
                    loading={cleaningLyricFailed}
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
                  />
                  <ActionIconConfirmButton
                    title={t("pages.task_manager.btn_cleanup_30d_records")}
                    confirmTitle={t("pages.task_manager.confirm_subtitle_cleanup_old")}
                    icon={<ClockCircleOutlined />}
                    type="default"
                    loading={cleaningLyricOld}
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
                  />
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
                rowSelection={{
                  selectedRowKeys: selectedLyricKeys,
                  onChange: setSelectedLyricKeys,
                }}
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
                <Space size={4} wrap={false}>
                  {renderListHeaderControls("preview", previewStatusFilter, setPreviewStatusFilter, () => void loadPreview())}
                  {toolbarDivider}
                  {renderBatchToolbar(selectedPreviewKeys, (action) => {
                    void runBatch(
                      action,
                      selectedPreviewKeys,
                      (k) => Number(k),
                      batchPreviewTasks,
                      async () => { await loadPreview(); },
                      () => setSelectedPreviewKeys([]),
                    );
                  })}
                </Space>
              )}
            >
              <Table
                rowKey="media_id"
                dataSource={filteredPreview}
                rowSelection={{
                  selectedRowKeys: selectedPreviewKeys,
                  onChange: setSelectedPreviewKeys,
                }}
                pagination={{ pageSize: 10 }}
                columns={[
                  { title: t("pages.task_manager.col_media_id"), dataIndex: "media_id", width: 90 },
                  { title: t("pages.task_manager.col_title"), dataIndex: "title", ellipsis: true },
                  { title: t("pages.task_manager.col_status"), dataIndex: "status", width: 110, render: (v: string) => toDisplayStatus("preview", v) },
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
                <Space size={4} wrap={false}>
                  {renderListHeaderControls("atrack", atrackStatusFilter, setAtrackStatusFilter, () => void loadAtrackTasks())}
                  {toolbarDivider}
                  {renderBatchToolbar(selectedAtrackKeys, (action) => {
                    void runBatch(
                      action,
                      selectedAtrackKeys,
                      (k) => Number(k),
                      batchAtrackTasks,
                      async () => { await loadAtrackTasks(); },
                      () => setSelectedAtrackKeys([]),
                    );
                  })}
                </Space>
              )}
            >
              <Table
                rowKey="media_id"
                loading={atrackLoading}
                dataSource={filteredAtrack}
                rowSelection={{
                  selectedRowKeys: selectedAtrackKeys,
                  onChange: setSelectedAtrackKeys,
                }}
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
                <Space size={4} wrap={false}>
                  {renderListHeaderControls("keyframe", keyframeStatusFilter, setKeyframeStatusFilter, () => void loadKeyframeTasks())}
                  {toolbarDivider}
                  {renderBatchToolbar(selectedKeyframeKeys, (action) => {
                    void runBatch(
                      action,
                      selectedKeyframeKeys,
                      (k) => Number(k),
                      batchKeyframeTasks,
                      async () => { await loadKeyframeTasks(); },
                      () => setSelectedKeyframeKeys([]),
                    );
                  })}
                </Space>
              )}
            >
              <Table
                rowKey="media_id"
                loading={keyframeLoading}
                dataSource={filteredKeyframe}
                rowSelection={{
                  selectedRowKeys: selectedKeyframeKeys,
                  onChange: setSelectedKeyframeKeys,
                }}
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
        {
          key: "encrypt",
          label: t("pages.task_manager.tab_encrypt"),
          children: (
            <Card
              title={t("pages.task_manager.encrypt_card_title")}
              extra={(
                <Space size={4} wrap={false}>
                  {renderListHeaderControls("encrypt", encryptStatusFilter, setEncryptStatusFilter, () => void loadEncryptTasks())}
                  {toolbarDivider}
                  {renderBatchToolbar(selectedEncryptKeys, (action) => {
                    void runBatch(
                      action,
                      selectedEncryptKeys,
                      (k) => Number(k),
                      batchEncryptTasks,
                      async () => { await loadEncryptTasks(); },
                      () => setSelectedEncryptKeys([]),
                    );
                  })}
                </Space>
              )}
            >
              <div style={{ marginBottom: 8, color: "#888", fontSize: 12 }}>
                {t("pages.task_manager.encrypt_help")}
              </div>
              <Table
                rowKey="id"
                loading={encryptLoading}
                dataSource={filteredEncrypt}
                rowSelection={{
                  selectedRowKeys: selectedEncryptKeys,
                  onChange: setSelectedEncryptKeys,
                }}
                pagination={{ pageSize: 10 }}
                columns={[
                  { title: t("pages.task_manager.col_task_id"), dataIndex: "id", width: 80 },
                  { title: t("pages.task_manager.col_media_id"), dataIndex: "media_id", width: 90 },
                  { title: t("pages.task_manager.col_title"), dataIndex: "title", ellipsis: true },
                  { title: t("pages.task_manager.col_status"), dataIndex: "status", width: 110 },
                  { title: t("pages.task_manager.col_attempts"), dataIndex: "attempts", width: 90 },
                  { title: t("pages.task_manager.col_started_at"), dataIndex: "started_at", width: 170, render: fmtTaskTs },
                  { title: t("pages.task_manager.col_lease_until"), dataIndex: "lease_until", width: 170, render: fmtTaskTs },
                  { title: t("pages.task_manager.col_error_message"), dataIndex: "last_error", ellipsis: true, render: (v?: string) => v || "-" },
                  {
                    title: t("pages.task_manager.col_actions"),
                    key: "actions",
                    width: 130,
                    align: "center",
                    render: (_: unknown, r: EncryptTask) => (
                      <Space size={4}>
                        {r.status === "running" || r.status === "waiting" ? (
                          <ActionIconButton
                            title={t("pages.task_manager.tooltip_cancel_encrypt")}
                            icon={<StopOutlined />}
                            loading={cancellingEncryptId === r.id}
                            onClick={() => {
                              setCancellingEncryptId(r.id);
                              void cancelEncryptTask(r.id)
                                .then(() => message.success(t("pages.task_manager.cancel_requested")))
                                .catch(() => message.error(t("pages.task_manager.task_cancel_failed")))
                                .finally(async () => {
                                  setCancellingEncryptId(null);
                                  await loadEncryptTasks();
                                });
                            }}
                          />
                        ) : null}
                        {r.status === "failed" || r.status === "cancelled" || r.status === "running" ? (
                          <ActionIconConfirmButton
                            title={t("pages.task_manager.tooltip_reset")}
                            confirmTitle={t("pages.task_manager.confirm_encrypt_reset")}
                            icon={<RollbackOutlined />}
                            loading={resettingEncryptId === r.id}
                            onConfirm={() => {
                              setResettingEncryptId(r.id);
                              void resetEncryptTask(r.id)
                                .then(() => message.success(t("pages.task_manager.reset_success")))
                                .catch(() => message.error(t("pages.task_manager.reset_failed")))
                                .finally(async () => {
                                  setResettingEncryptId(null);
                                  await loadEncryptTasks();
                                });
                            }}
                          />
                        ) : null}
                        {r.status === "waiting" || r.status === "failed" || r.status === "cancelled" ? (
                          <ActionIconConfirmButton
                            title={t("pages.task_manager.tooltip_delete")}
                            confirmTitle={t("pages.task_manager.confirm_encrypt_delete")}
                            icon={<DeleteOutlined />}
                            danger
                            loading={deletingEncryptId === r.id}
                            onConfirm={() => {
                              setDeletingEncryptId(r.id);
                              void deleteEncryptTask(r.id)
                                .then(() => message.success(t("common.delete_success")))
                                .catch(() => message.error(t("common.delete_failed")))
                                .finally(async () => {
                                  setDeletingEncryptId(null);
                                  await loadEncryptTasks();
                                });
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
      ]}
      />
    </>
  );
}
