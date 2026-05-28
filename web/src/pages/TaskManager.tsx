import {
  Button,
  Card,
  Form,
  Input,
  InputNumber,
  Modal,
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
  EditOutlined,
  RedoOutlined,
  RollbackOutlined,
  ThunderboltOutlined,
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
  cleanupFailedLyricTasks,
  cleanupLyricTasksBefore,
  createScheduledTask,
  deleteScheduledTask,
  fetchAtrackTasks,
  fetchKeyframeTasks,
  fetchLibraries,
  fetchLyricTasks,
  fetchPreviewTasks,
  fetchScheduledTasks,
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
  runScheduledTask,
  type AtrackTask,
  type KeyframeTask,
  type Library,
  type LyricTask,
  updateScheduledTask,
  type PreviewTask,
  type ScheduledTask,
  type ScrapeTask,
  type ScanTask,
  type SubtitleTask,
  type TranscodeTask,
} from "../api/client";
import { useT } from "../i18n";

type ScheduledTaskForm = {
  name: string;
  category: string;
  task_type: string;
  interval_min: number;
  enabled: boolean;
  library_id?: number;
  limit?: number;
  days?: number;
};

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
}: {
  title: string;
  confirmTitle: string;
  icon: ReactNode;
  onConfirm: () => void;
  loading?: boolean;
  danger?: boolean;
}) {
  return (
    <Popconfirm title={confirmTitle} onConfirm={onConfirm}>
      <Tooltip title={title}>
        <Button
          type="text"
          size="small"
          icon={icon}
          loading={loading}
          danger={danger}
          aria-label={title}
        />
      </Tooltip>
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
  const [scheduledTasks, setScheduledTasks] = useState<ScheduledTask[]>([]);
  const [scheduledLoading, setScheduledLoading] = useState(false);
  const [runningScheduledId, setRunningScheduledId] = useState<number | null>(null);
  const [creatingSchedule, setCreatingSchedule] = useState(false);
  const [updatingSchedule, setUpdatingSchedule] = useState(false);
  const [editingTask, setEditingTask] = useState<ScheduledTask | null>(null);
  const [createModalOpen, setCreateModalOpen] = useState(false);
  const [libraries, setLibraries] = useState<Library[]>([]);
  const [autoRefresh, setAutoRefresh] = useState(true);
  const [activeTab, setActiveTab] = useState("scheduled");
  const [scheduledStatusFilter, setScheduledStatusFilter] = useState("all");
  const [transcodeStatusFilter, setTranscodeStatusFilter] = useState("all");
  const [scrapeStatusFilter, setScrapeStatusFilter] = useState("all");
  const [scanStatusFilter, setScanStatusFilter] = useState("all");
  const [previewStatusFilter, setPreviewStatusFilter] = useState("all");
  const [subtitleTasks, setSubtitleTasks] = useState<SubtitleTask[]>([]);
  const [subtitleLoading, setSubtitleLoading] = useState(false);
  const [subtitleStatusFilter, setSubtitleStatusFilter] = useState("all");
  const [resettingSubtitleId, setResettingSubtitleId] = useState<number | null>(null);
  const [retryingSubtitleId, setRetryingSubtitleId] = useState<number | null>(null);
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
  const [form] = Form.useForm<ScheduledTaskForm>();
  const [editForm] = Form.useForm<ScheduledTaskForm>();
  const createTaskType = Form.useWatch("task_type", form);
  const editTaskType = Form.useWatch("task_type", editForm);

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

  const loadScheduled = async () => {
    setScheduledLoading(true);
    try {
      setScheduledTasks(await fetchScheduledTasks());
    } catch {
      setScheduledTasks([]);
    } finally {
      setScheduledLoading(false);
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

  const loadLibraries = async () => {
    try {
      setLibraries(await fetchLibraries());
    } catch {
      setLibraries([]);
    }
  };

  useEffect(() => {
    void loadTranscode();
    void loadPreview();
    void loadScrape();
    void loadScheduled();
    void loadScanTasks();
    void loadSubtitleTasks();
    void loadAtrackTasks();
    void loadKeyframeTasks();
    void loadLyricTasks();
    void loadLibraries();
  }, []);

  useEffect(() => {
    if (!autoRefresh) return;
    const timer = window.setInterval(() => {
      if (activeTab === "scheduled") void loadScheduled();
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

  useEffect(() => {
    if (createTaskType !== "library_scan" && createTaskType !== "subtitle_process") {
      form.setFieldValue("library_id", undefined);
    }
    if (createTaskType !== "scrape_run" && createTaskType !== "subtitle_process" && createTaskType !== "lyric_process") {
      form.setFieldValue("limit", undefined);
    }
    if (createTaskType !== "transcode_cleanup_failed_before" && createTaskType !== "activity_cleanup") {
      form.setFieldValue("days", undefined);
    }
  }, [createTaskType, form]);

  useEffect(() => {
    if (editTaskType !== "library_scan" && editTaskType !== "subtitle_process") {
      editForm.setFieldValue("library_id", undefined);
    }
    if (editTaskType !== "scrape_run" && editTaskType !== "subtitle_process" && editTaskType !== "lyric_process") {
      editForm.setFieldValue("limit", undefined);
    }
    if (editTaskType !== "transcode_cleanup_failed_before" && editTaskType !== "activity_cleanup") {
      editForm.setFieldValue("days", undefined);
    }
  }, [editTaskType, editForm]);

  const filteredScheduled = useMemo(
    () => scheduledTasks.filter((x) => (scheduledStatusFilter === "all" ? true : (x.last_status || "none") === scheduledStatusFilter)),
    [scheduledTasks, scheduledStatusFilter]
  );
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
    if (tab === "scheduled") {
      return [
        ...commonAll,
        { value: "none", label: "none" },
        { value: "done", label: "done" },
        { value: "failed", label: "failed" },
      ];
    }
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
  const libraryOptions = useMemo(
    () => libraries.map((lib) => ({ value: lib.id, label: `${lib.name} (#${lib.id})` })),
    [libraries]
  );

  const onCreateScheduled = async () => {
    try {
      const v = await form.validateFields();
      const payload: Record<string, unknown> = {};
      if (v.task_type === "library_scan" && v.library_id) payload.library_id = v.library_id;
      if (v.task_type === "scrape_run" && v.limit) payload.limit = v.limit;
      if (v.task_type === "subtitle_process") {
        if (v.library_id != null && Number(v.library_id) > 0) payload.library_id = Number(v.library_id);
        if (v.limit) payload.limit = v.limit;
      }
      if (v.task_type === "lyric_process" && v.limit) payload.limit = v.limit;
      if ((v.task_type === "transcode_cleanup_failed_before" || v.task_type === "activity_cleanup") && v.days) payload.days = v.days;
      setCreatingSchedule(true);
      await createScheduledTask({
        name: v.name,
        category: v.category,
        task_type: v.task_type,
        interval_min: v.interval_min,
        enabled: v.enabled ? 1 : 0,
        payload,
      });
      message.success(t("pages.task_manager.scheduled_created"));
      setCreateModalOpen(false);
      form.resetFields();
      await loadScheduled();
    } catch {
      message.error(t("pages.task_manager.scheduled_create_failed"));
    } finally {
      setCreatingSchedule(false);
    }
  };

  const fillEditForm = (task: ScheduledTask) => {
    const payload = task.payload || {};
    editForm.setFieldsValue({
      name: task.name,
      category: task.category || "media",
      task_type: task.task_type,
      interval_min: task.interval_min || 60,
      enabled: task.enabled === 1,
      library_id: Number(payload.library_id || 0) || undefined,
      limit: Number(payload.limit || 0) || undefined,
      days: Number(payload.days || 0) || undefined,
    });
    setEditingTask(task);
  };

  const onUpdateScheduled = async () => {
    if (!editingTask) return;
    try {
      const v = await editForm.validateFields();
      const payload: Record<string, unknown> = {};
      if (v.task_type === "library_scan" && v.library_id) payload.library_id = v.library_id;
      if (v.task_type === "scrape_run" && v.limit) payload.limit = v.limit;
      if (v.task_type === "subtitle_process") {
        if (v.library_id != null && Number(v.library_id) > 0) payload.library_id = Number(v.library_id);
        if (v.limit) payload.limit = v.limit;
      }
      if (v.task_type === "lyric_process" && v.limit) payload.limit = v.limit;
      if ((v.task_type === "transcode_cleanup_failed_before" || v.task_type === "activity_cleanup") && v.days) payload.days = v.days;
      setUpdatingSchedule(true);
      await updateScheduledTask(editingTask.id, {
        name: v.name,
        category: v.category,
        task_type: v.task_type,
        interval_min: v.interval_min,
        enabled: v.enabled ? 1 : 0,
        payload,
      });
      message.success(t("pages.task_manager.scheduled_updated"));
      setEditingTask(null);
      await loadScheduled();
    } catch {
      message.error(t("pages.task_manager.scheduled_update_failed"));
    } finally {
      setUpdatingSchedule(false);
    }
  };

  return (
    <>
      <Tabs
      activeKey={activeTab}
      onChange={setActiveTab}
      items={[
        {
          key: "scheduled",
          label: t("pages.task_manager.tab_scheduled"),
          children: (
            <Space direction="vertical" size="large" style={{ width: "100%" }}>
              <Card
                title={t("pages.task_manager.scheduled_card_title")}
                extra={(
                  <Space>
                    <Button
                      type="primary"
                      onClick={() => {
                        form.setFieldsValue({ category: "media", interval_min: 60, enabled: true, task_type: "library_scan" });
                        form.setFieldValue("library_id", undefined);
                        setCreateModalOpen(true);
                      }}
                    >
                      {t("pages.task_manager.create_scheduled_btn")}
                    </Button>
                    {renderListHeaderControls("scheduled", scheduledStatusFilter, setScheduledStatusFilter, loadScheduled)}
                  </Space>
                )}
              >
                <Table
                  rowKey="id"
                  loading={scheduledLoading}
                  dataSource={filteredScheduled}
                  pagination={{ pageSize: 10 }}
                  scroll={{ x: 1350 }}
                  columns={[
                    { title: "ID", dataIndex: "id", width: 70 },
                    {
                      title: t("pages.task_manager.col_name"),
                      dataIndex: "name",
                      width: 180,
                      ellipsis: true,
                      render: (v?: string) => (
                        <Tooltip title={v || "-"}>
                          <span
                            style={{
                              display: "inline-block",
                              maxWidth: "100%",
                              overflow: "hidden",
                              textOverflow: "ellipsis",
                              whiteSpace: "nowrap",
                              verticalAlign: "bottom",
                            }}
                          >
                            {v || "-"}
                          </span>
                        </Tooltip>
                      ),
                    },
                    { title: t("pages.task_manager.col_category"), dataIndex: "category", width: 110 },
                    { title: t("pages.task_manager.col_type"), dataIndex: "task_type", width: 220 },
                    { title: t("pages.task_manager.col_interval_min"), dataIndex: "interval_min", width: 90 },
                    { title: t("pages.task_manager.col_enabled"), dataIndex: "enabled", width: 80, render: (v: number) => (v === 1 ? <Tag color="green">{t("pages.task_manager.enabled_tag")}</Tag> : <Tag>{t("pages.task_manager.disabled_tag")}</Tag>) },
                    { title: t("pages.task_manager.col_last_status"), dataIndex: "last_status", width: 110, render: (v?: string) => v || "-" },
                    { title: t("pages.task_manager.col_last_run"), dataIndex: "last_run_at", width: 170, render: (v?: string) => v || "-" },
                    { title: t("pages.task_manager.col_last_message"), dataIndex: "last_message", ellipsis: true },
                    {
                      title: t("pages.task_manager.col_actions"),
                      key: "actions",
                      width: 120,
                      align: "center",
                      fixed: "right",
                      render: (_: unknown, r: ScheduledTask) => (
                        <Space size={4}>
                          <ActionIconButton
                            title={t("pages.task_manager.tooltip_run_now")}
                            icon={<ThunderboltOutlined />}
                            loading={runningScheduledId === r.id}
                            onClick={() => {
                              setRunningScheduledId(r.id);
                              void runScheduledTask(r.id).then(() => message.success(t("pages.task_manager.task_run_success"))).catch(() => message.error(t("pages.task_manager.task_run_failed"))).finally(async () => {
                                setRunningScheduledId(null);
                                await loadScheduled();
                              });
                            }}
                          />
                          <ActionIconButton
                            title={t("pages.task_manager.tooltip_edit")}
                            icon={<EditOutlined />}
                            onClick={() => fillEditForm(r)}
                          />
                          <ActionIconConfirmButton
                            title={t("pages.task_manager.tooltip_delete")}
                            confirmTitle={t("pages.task_manager.confirm_delete_task")}
                            icon={<DeleteOutlined />}
                            danger
                            onConfirm={() => void deleteScheduledTask(r.id).then(loadScheduled)}
                          />
                        </Space>
                      ),
                    },
                  ]}
                />
              </Card>
            </Space>
          ),
        },
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
                  { title: t("pages.task_manager.col_created_at"), dataIndex: "created_at", width: 180 },
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
                    { title: t("pages.task_manager.col_created_at"), dataIndex: "created_at", width: 180 },
                    { title: t("pages.task_manager.col_finished_at"), dataIndex: "finished_at", width: 180, render: (v?: string) => v || "-" },
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
                  { title: t("pages.task_manager.col_started_at"), dataIndex: "started_at", width: 180 },
                  { title: t("pages.task_manager.col_finished_at"), dataIndex: "finished_at", width: 180, render: (v?: string) => v || "-" },
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
                  { title: t("pages.task_manager.col_created_at"), dataIndex: "created_at", width: 170 },
                  { title: t("pages.task_manager.col_started_at"), dataIndex: "started_at", width: 170, render: (v?: string) => v || "-" },
                  { title: t("pages.task_manager.col_finished_at"), dataIndex: "finished_at", width: 170, render: (v?: string) => v || "-" },
                  {
                    title: t("pages.task_manager.col_actions"),
                    key: "subactions",
                    width: 90,
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
                  { title: t("pages.task_manager.col_created_at"), dataIndex: "created_at", width: 170 },
                  { title: t("pages.task_manager.col_started_at"), dataIndex: "started_at", width: 170, render: (v?: string) => v || "-" },
                  { title: t("pages.task_manager.col_finished_at"), dataIndex: "finished_at", width: 170, render: (v?: string) => v || "-" },
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
                  { title: t("pages.task_manager.col_updated_at"), dataIndex: "updated_at", width: 180 },
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
                  { title: t("pages.task_manager.col_updated_at"), dataIndex: "updated_at", width: 180 },
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
                  { title: t("pages.task_manager.col_updated_at"), dataIndex: "updated_at", width: 180 },
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
      <Modal
        title={t("pages.task_manager.scheduled_modal_create")}
        open={createModalOpen}
        onCancel={() => {
          setCreateModalOpen(false);
          form.resetFields();
        }}
        onOk={() => void onCreateScheduled()}
        confirmLoading={creatingSchedule}
      >
        <Form form={form} layout="vertical" initialValues={{ category: "media", interval_min: 60, enabled: true, task_type: "library_scan" }}>
          <Form.Item name="name" label={t("pages.task_manager.form_task_name")} rules={[{ required: true }]}><Input /></Form.Item>
          <Form.Item name="category" label={t("pages.task_manager.form_category")} rules={[{ required: true }]}>
            <Select options={[{ value: "media", label: t("pages.task_manager.category_media") }, { value: "maintenance", label: t("pages.task_manager.category_maintenance") }]} />
          </Form.Item>
          <Form.Item name="task_type" label={t("pages.task_manager.task_type_label")} rules={[{ required: true }]}>
            <Select
              options={[
                { value: "library_scan", label: t("pages.task_manager.task_type_library_scan") },
                { value: "subtitle_process", label: t("pages.task_manager.task_type_subtitle_process") },
                { value: "lyric_process", label: t("pages.task_manager.task_type_lyric_process") },
                { value: "scrape_run", label: t("pages.task_manager.task_type_scrape_run") },
                { value: "transcode_cleanup_failed_before", label: t("pages.task_manager.task_type_transcode_cleanup") },
                { value: "activity_cleanup", label: t("pages.task_manager.task_type_activity_cleanup") },
                { value: "db_optimize", label: t("pages.task_manager.task_type_db_optimize") },
              ]}
            />
          </Form.Item>
          <Form.Item name="interval_min" label={t("pages.task_manager.form_interval_minutes")} rules={[{ required: true }]}><InputNumber min={1} style={{ width: "100%" }} /></Form.Item>
          <Form.Item name="enabled" label={t("pages.task_manager.form_enabled")} valuePropName="checked"><Switch /></Form.Item>
          <Space wrap>
            {createTaskType === "library_scan" ? (
              <Form.Item name="library_id" label={t("pages.task_manager.form_library_scan")}>
                <Select
                  allowClear
                  showSearch
                  placeholder={t("pages.task_manager.form_library_pick")}
                  options={libraryOptions}
                  optionFilterProp="label"
                  style={{ width: 240 }}
                />
              </Form.Item>
            ) : null}
            {createTaskType === "scrape_run" ? (
              <Form.Item name="limit" label={t("pages.task_manager.form_limit_scrape")}><InputNumber min={1} max={200} /></Form.Item>
            ) : null}
            {createTaskType === "subtitle_process" ? (
              <>
                <Form.Item name="library_id" label={t("pages.task_manager.form_library_all_0")}>
                  <Select allowClear showSearch placeholder={t("pages.task_manager.form_library_all_placeholder")} options={[{ value: 0, label: t("pages.task_manager.form_library_all_label") }, ...libraryOptions]} optionFilterProp="label" style={{ width: 280 }} />
                </Form.Item>
                <Form.Item name="limit" label={t("pages.task_manager.form_limit_videos")}><InputNumber min={1} max={500} placeholder={t("pages.task_manager.form_limit_videos_default")} style={{ width: 200 }} /></Form.Item>
              </>
            ) : null}
            {createTaskType === "lyric_process" ? (
              <Form.Item name="limit" label={t("pages.task_manager.form_limit_audios")}><InputNumber min={1} max={200} placeholder={t("pages.task_manager.form_limit_audios_default")} style={{ width: 200 }} /></Form.Item>
            ) : null}
            {createTaskType === "transcode_cleanup_failed_before" || createTaskType === "activity_cleanup" ? (
              <Form.Item name="days" label={t("pages.task_manager.form_days_cleanup")}><InputNumber min={1} max={3650} /></Form.Item>
            ) : null}
          </Space>
        </Form>
      </Modal>
      <Modal
        title={t("pages.task_manager.scheduled_modal_edit")}
        open={!!editingTask}
        onCancel={() => setEditingTask(null)}
        onOk={() => void onUpdateScheduled()}
        confirmLoading={updatingSchedule}
      >
        <Form form={editForm} layout="vertical">
          <Form.Item name="name" label={t("pages.task_manager.form_task_name")} rules={[{ required: true }]}><Input /></Form.Item>
          <Form.Item name="category" label={t("pages.task_manager.form_category")} rules={[{ required: true }]}>
            <Select options={[{ value: "media", label: t("pages.task_manager.category_media") }, { value: "maintenance", label: t("pages.task_manager.category_maintenance") }]} />
          </Form.Item>
          <Form.Item name="task_type" label={t("pages.task_manager.task_type_label")} rules={[{ required: true }]}>
            <Select
              options={[
                { value: "library_scan", label: t("pages.task_manager.task_type_library_scan") },
                { value: "subtitle_process", label: t("pages.task_manager.task_type_subtitle_process") },
                { value: "lyric_process", label: t("pages.task_manager.task_type_lyric_process") },
                { value: "scrape_run", label: t("pages.task_manager.task_type_scrape_run") },
                { value: "transcode_cleanup_failed_before", label: t("pages.task_manager.task_type_transcode_cleanup") },
                { value: "activity_cleanup", label: t("pages.task_manager.task_type_activity_cleanup") },
                { value: "db_optimize", label: t("pages.task_manager.task_type_db_optimize") },
              ]}
            />
          </Form.Item>
          <Form.Item name="interval_min" label={t("pages.task_manager.form_interval_minutes")} rules={[{ required: true }]}><InputNumber min={1} style={{ width: "100%" }} /></Form.Item>
          <Form.Item name="enabled" label={t("pages.task_manager.form_enabled")} valuePropName="checked"><Switch /></Form.Item>
          <Space wrap>
            {editTaskType === "library_scan" ? (
              <Form.Item name="library_id" label={t("pages.task_manager.form_library_scan")}>
                <Select
                  allowClear
                  showSearch
                  placeholder={t("pages.task_manager.form_library_pick")}
                  options={libraryOptions}
                  optionFilterProp="label"
                  style={{ width: 240 }}
                />
              </Form.Item>
            ) : null}
            {editTaskType === "scrape_run" ? (
              <Form.Item name="limit" label={t("pages.task_manager.form_limit_scrape")}><InputNumber min={1} max={200} /></Form.Item>
            ) : null}
            {editTaskType === "subtitle_process" ? (
              <>
                <Form.Item name="library_id" label={t("pages.task_manager.form_library_all_0")}>
                  <Select allowClear showSearch placeholder={t("pages.task_manager.form_library_all_placeholder")} options={[{ value: 0, label: t("pages.task_manager.form_library_all_label") }, ...libraryOptions]} optionFilterProp="label" style={{ width: 280 }} />
                </Form.Item>
                <Form.Item name="limit" label={t("pages.task_manager.form_limit_videos")}><InputNumber min={1} max={500} placeholder={t("pages.task_manager.form_limit_videos_default")} style={{ width: 200 }} /></Form.Item>
              </>
            ) : null}
            {editTaskType === "lyric_process" ? (
              <Form.Item name="limit" label={t("pages.task_manager.form_limit_audios")}><InputNumber min={1} max={200} placeholder={t("pages.task_manager.form_limit_audios_default")} style={{ width: 200 }} /></Form.Item>
            ) : null}
            {editTaskType === "transcode_cleanup_failed_before" || editTaskType === "activity_cleanup" ? (
              <Form.Item name="days" label={t("pages.task_manager.form_days_cleanup")}><InputNumber min={1} max={3650} /></Form.Item>
            ) : null}
          </Space>
        </Form>
      </Modal>
    </>
  );
}
