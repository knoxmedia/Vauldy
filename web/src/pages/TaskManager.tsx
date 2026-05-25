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
  createScheduledTask,
  deleteScheduledTask,
  fetchAtrackTasks,
  fetchKeyframeTasks,
  fetchLibraries,
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
  retryTranscodeTask,
  retrySubtitleTask,
  runScheduledTask,
  type AtrackTask,
  type KeyframeTask,
  type Library,
  updateScheduledTask,
  type PreviewTask,
  type ScheduledTask,
  type ScrapeTask,
  type ScanTask,
  type SubtitleTask,
  type TranscodeTask,
} from "../api/client";

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
    }, 10000);
    return () => window.clearInterval(timer);
  }, [autoRefresh, activeTab]);

  useEffect(() => {
    if (createTaskType !== "library_scan" && createTaskType !== "subtitle_process") {
      form.setFieldValue("library_id", undefined);
    }
    if (createTaskType !== "scrape_run" && createTaskType !== "subtitle_process") {
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
    if (editTaskType !== "scrape_run" && editTaskType !== "subtitle_process") {
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
  const getStatusOptionsForTab = (tab: string) => {
    const commonAll = [{ value: "all", label: "全部状态" }];
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
    if (tab === "subtitle") {
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
        <span style={{ color: "#999" }}>自动刷新</span>
        <Switch size="small" checked={autoRefresh} onChange={setAutoRefresh} />
      </Space>
      <Button disabled={autoRefresh} onClick={() => void onRefresh()}>
        刷新
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
      message.success("定时任务已创建");
      setCreateModalOpen(false);
      form.resetFields();
      await loadScheduled();
    } catch {
      message.error("创建定时任务失败");
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
      message.success("定时任务已更新");
      setEditingTask(null);
      await loadScheduled();
    } catch {
      message.error("更新定时任务失败");
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
          label: "定时任务",
          children: (
            <Space direction="vertical" size="large" style={{ width: "100%" }}>
              <Card
                title="定时任务列表"
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
                      创建定时任务
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
                      title: "名称",
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
                    { title: "分类", dataIndex: "category", width: 110 },
                    { title: "类型", dataIndex: "task_type", width: 220 },
                    { title: "间隔(分)", dataIndex: "interval_min", width: 90 },
                    { title: "启用", dataIndex: "enabled", width: 80, render: (v: number) => (v === 1 ? <Tag color="green">启用</Tag> : <Tag>停用</Tag>) },
                    { title: "上次状态", dataIndex: "last_status", width: 110, render: (v?: string) => v || "-" },
                    { title: "上次执行", dataIndex: "last_run_at", width: 170, render: (v?: string) => v || "-" },
                    { title: "执行信息", dataIndex: "last_message", ellipsis: true },
                    {
                      title: "操作",
                      key: "actions",
                      width: 120,
                      align: "center",
                      fixed: "right",
                      render: (_: unknown, r: ScheduledTask) => (
                        <Space size={4}>
                          <ActionIconButton
                            title="立即执行"
                            icon={<ThunderboltOutlined />}
                            loading={runningScheduledId === r.id}
                            onClick={() => {
                              setRunningScheduledId(r.id);
                              void runScheduledTask(r.id).then(() => message.success("任务已执行")).catch(() => message.error("任务执行失败")).finally(async () => {
                                setRunningScheduledId(null);
                                await loadScheduled();
                              });
                            }}
                          />
                          <ActionIconButton
                            title="编辑"
                            icon={<EditOutlined />}
                            onClick={() => fillEditForm(r)}
                          />
                          <ActionIconConfirmButton
                            title="删除"
                            confirmTitle="确认删除该任务？"
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
          label: "转码任务",
          children: (
            <Card
              title="转码任务"
              extra={
                <Space>
                  <Popconfirm title="确认清理 7 天前失败任务？" onConfirm={() => {
                    setCleaningOld(true);
                    void cleanupFailedTranscodeTasksBefore(7).then((n) => message.success(`已清理 ${n} 条`)).catch(() => message.error("清理失败")).finally(async () => {
                      setCleaningOld(false);
                      await loadTranscode();
                    });
                  }}>
                    <Button loading={cleaningOld}>清理 7 天前失败任务</Button>
                  </Popconfirm>
                  <Popconfirm title="确认清理失败任务？" onConfirm={() => {
                    setCleaning(true);
                    void cleanupFailedTranscodeTasks().then((n) => message.success(`已清理 ${n} 条`)).catch(() => message.error("清理失败")).finally(async () => {
                      setCleaning(false);
                      await loadTranscode();
                    });
                  }}>
                    <Button danger loading={cleaning}>清理失败任务</Button>
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
                  { title: "清晰度", dataIndex: "quality", width: 90 },
                  { title: "状态", dataIndex: "status", width: 100 },
                  { title: "DRM", dataIndex: "drm_status", width: 90, render: (v?: string) => v || "-" },
                  { title: "Cleanup", dataIndex: "source_cleanup_status", width: 110, render: (v?: string) => v || "-" },
                  { title: "进度", dataIndex: "progress", width: 80, render: (p: number) => `${p}%` },
                  { title: "失败原因", dataIndex: "error_message", ellipsis: true, render: (v?: string) => v || "-" },
                  { title: "创建时间", dataIndex: "created_at", width: 180 },
                  {
                    title: "操作",
                    key: "ops",
                    width: 90,
                    align: "center",
                    render: (_: unknown, r: TranscodeTask) => (
                      <Space size={4}>
                        {(r.status === "waiting" || r.status === "running") ? (
                          <ActionIconButton
                            title="取消任务"
                            icon={<StopOutlined />}
                            onClick={() => {
                              void cancelTranscodeTask(r.id)
                                .then(() => message.success("已取消任务"))
                                .then(loadTranscode)
                                .catch(() => message.error("取消失败"));
                            }}
                          />
                        ) : null}
                        {(r.status === "failed" || r.status === "cancelled") ? (
                          <ActionIconButton
                            title="重试"
                            icon={<RedoOutlined />}
                            type="primary"
                            onClick={() => {
                              void retryTranscodeTask(r.id)
                                .then(() => message.success("已提交重试"))
                                .then(loadTranscode)
                                .catch(() => message.error("重试失败"));
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
          label: "刮削任务",
          children: (
            <Card
              loading={scrapeLoading}
              title="刮削任务管理"
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
                    { title: "媒体ID", dataIndex: "media_id", width: 90 },
                    { title: "标题", dataIndex: "title", ellipsis: true },
                    { title: "来源", dataIndex: "source", width: 90 },
                    {
                      title: "状态",
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
                            ? "完成"
                            : v === "failed"
                              ? "失败"
                              : v === "abandoned"
                                ? "已放弃"
                                : v === "running"
                                  ? "进行中"
                                  : v === "waiting"
                                    ? "等待"
                                    : v;
                        return <Tag color={c}>{label}</Tag>;
                      },
                    },
                    {
                      title: "失败次数",
                      dataIndex: "fail_count",
                      width: 90,
                      render: (v: number | undefined) => (v && v > 0 ? v : "-"),
                    },
                    { title: "进度", dataIndex: "progress", width: 90, render: (v: number) => `${v}%` },
                    { title: "结果/原因", dataIndex: "message", ellipsis: true, render: (v?: string) => v || "-" },
                    { title: "创建时间", dataIndex: "created_at", width: 180 },
                    { title: "完成时间", dataIndex: "finished_at", width: 180, render: (v?: string) => v || "-" },
                  ]}
                />
              </Space>
            </Card>
          ),
        },
        {
          key: "scan",
          label: "扫描任务",
          children: (
            <Card
              title="媒体库扫描任务"
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
                  { title: "任务ID", dataIndex: "id", width: 90 },
                  { title: "媒体库", dataIndex: "library_name", ellipsis: true },
                  { title: "状态", dataIndex: "status", width: 100 },
                  { title: "来源", dataIndex: "source", width: 90 },
                  { title: "已处理", dataIndex: "processed_count", width: 90 },
                  { title: "新增", dataIndex: "added_count", width: 80 },
                  { title: "开始时间", dataIndex: "started_at", width: 180 },
                  { title: "结束时间", dataIndex: "finished_at", width: 180, render: (v?: string) => v || "-" },
                  { title: "错误信息", dataIndex: "error_message", ellipsis: true, render: (v?: string) => v || "-" },
                  {
                    title: "操作",
                    key: "actions",
                    width: 80,
                    align: "center",
                    fixed: "right",
                    render: (_: unknown, r: ScanTask) => (
                      <ActionIconButton
                        title="取消扫描"
                        icon={<StopOutlined />}
                        disabled={r.status !== "running"}
                        loading={cancellingScanId === r.id}
                        onClick={() => {
                          setCancellingScanId(r.id);
                          void cancelScanTask(r.id)
                            .then(() => message.success("已请求取消"))
                            .catch(() => message.error("取消失败"))
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
          label: "字幕任务",
          children: (
            <Card
              title="字幕处理任务"
              extra={(
                <Space>
                  {renderListHeaderControls("subtitle", subtitleStatusFilter, setSubtitleStatusFilter, () => void loadSubtitleTasks())}
                  <Popconfirm
                    title="删除所有失败状态的字幕任务记录？（不删除已生成的字幕文件，仅任务表）"
                    onConfirm={() => {
                      setCleaningSubtitleFailed(true);
                      void cleanupFailedSubtitleTasks()
                        .then((n) => message.success(`已清理 ${n} 条`))
                        .catch(() => message.error("清理失败"))
                        .finally(async () => {
                          setCleaningSubtitleFailed(false);
                          await loadSubtitleTasks();
                        });
                    }}
                  >
                    <Button loading={cleaningSubtitleFailed}>清理失败记录</Button>
                  </Popconfirm>
                  <Popconfirm
                    title="删除 30 天前已完成或失败的任务记录？"
                    onConfirm={() => {
                      setCleaningSubtitleOld(true);
                      void cleanupSubtitleTasksBefore(30)
                        .then((n) => message.success(`已清理 ${n} 条`))
                        .catch(() => message.error("清理失败"))
                        .finally(async () => {
                          setCleaningSubtitleOld(false);
                          await loadSubtitleTasks();
                        });
                    }}
                  >
                    <Button loading={cleaningSubtitleOld}>清理 30 天前记录</Button>
                  </Popconfirm>
                </Space>
              )}
            >
              <div style={{ marginBottom: 12, color: "rgba(0,0,0,0.55)", fontSize: 13 }}>
                每个视频一条记录。在媒体详情中手动处理、媒体库扫描发现新视频（若开启 subtitle.auto_on_scan）、或定时任务「subtitle_process」批量处理时，会显示状态与完成时间。
              </div>
              <Table
                rowKey="id"
                loading={subtitleLoading}
                dataSource={filteredSubtitle}
                pagination={{ pageSize: 12 }}
                scroll={{ x: 1200 }}
                columns={[
                  { title: "任务ID", dataIndex: "id", width: 80 },
                  { title: "媒体ID", dataIndex: "media_id", width: 90 },
                  { title: "视频名称", dataIndex: "title", ellipsis: true },
                  { title: "状态", dataIndex: "status", width: 100, render: (v: string) => {
                    const c = v === "done" ? "green" : v === "failed" ? "red" : v === "running" ? "processing" : "default";
                    return <Tag color={c}>{v}</Tag>;
                  } },
                  { title: "备注", dataIndex: "message", ellipsis: true, render: (v?: string) => v || "-" },
                  { title: "创建时间", dataIndex: "created_at", width: 170 },
                  { title: "开始时间", dataIndex: "started_at", width: 170, render: (v?: string) => v || "-" },
                  { title: "完成时间", dataIndex: "finished_at", width: 170, render: (v?: string) => v || "-" },
                  {
                    title: "操作",
                    key: "subactions",
                    width: 90,
                    align: "center",
                    fixed: "right",
                    render: (_: unknown, r: SubtitleTask) => (
                      <Space size={4}>
                        <ActionIconConfirmButton
                          title="重置"
                          confirmTitle="将清除该媒体的字幕缓存与数据库记录，并标记为待处理。确定？"
                          icon={<RollbackOutlined />}
                          loading={resettingSubtitleId === r.media_id}
                          onConfirm={() => {
                            setResettingSubtitleId(r.media_id);
                            void resetSubtitleTask(r.media_id)
                              .then(() => message.success("已重置"))
                              .catch(() => message.error("重置失败"))
                              .finally(async () => {
                                setResettingSubtitleId(null);
                                await loadSubtitleTasks();
                              });
                          }}
                        />
                        <ActionIconButton
                          title="重新处理"
                          icon={<SyncOutlined />}
                          type="primary"
                          loading={retryingSubtitleId === r.media_id}
                          onClick={() => {
                            setRetryingSubtitleId(r.media_id);
                            void retrySubtitleTask(r.media_id)
                              .then(() => message.success("已提交重新处理"))
                              .catch(() => message.error("提交失败"))
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
          key: "preview",
          label: "进度条预览任务",
          children: (
            <Card
              title="进度条预览任务"
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
                  { title: "媒体ID", dataIndex: "media_id", width: 90 },
                  { title: "标题", dataIndex: "title", ellipsis: true },
                  { title: "状态", dataIndex: "status", width: 110 },
                  { title: "间隔(s)", dataIndex: "interval_sec", width: 90 },
                  { title: "缩略图数", dataIndex: "thumb_count", width: 100 },
                  { title: "尺寸", key: "size", width: 120, render: (_: unknown, r: PreviewTask) => `${r.thumb_width}x${r.thumb_height}` },
                  { title: "错误信息", dataIndex: "error_message", ellipsis: true, render: (v?: string) => v || "-" },
                  { title: "更新时间", dataIndex: "updated_at", width: 180 },
                  {
                    title: "操作",
                    key: "actions",
                    width: 70,
                    align: "center",
                    render: (_: unknown, r: PreviewTask) => (
                      <ActionIconButton
                        title="重试"
                        icon={<RedoOutlined />}
                        loading={retryingPreview === r.media_id}
                        onClick={() => {
                          setRetryingPreview(r.media_id);
                          void retryPreviewTask(r.media_id).then(() => message.success("已触发重试")).then(loadPreview).catch(() => message.error("重试失败")).finally(() => setRetryingPreview(null));
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
          label: "音轨提取任务",
          children: (
            <Card
              title="音轨提取任务"
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
                  { title: "媒体ID", dataIndex: "media_id", width: 90 },
                  { title: "标题", dataIndex: "title", ellipsis: true },
                  { title: "状态", dataIndex: "status", width: 110 },
                  { title: "输出目录", dataIndex: "output_dir", ellipsis: true, render: (v?: string) => v || "-" },
                  { title: "错误信息", dataIndex: "error_message", ellipsis: true, render: (v?: string) => v || "-" },
                  { title: "更新时间", dataIndex: "updated_at", width: 180 },
                  {
                    title: "操作",
                    key: "actions",
                    width: 70,
                    align: "center",
                    render: (_: unknown, r: AtrackTask) => (
                      <ActionIconButton
                        title="重试"
                        icon={<RedoOutlined />}
                        loading={retryingAtrackId === r.media_id}
                        onClick={async () => {
                          setRetryingAtrackId(r.media_id);
                          try {
                            await retryAudioTrackExtraction(r.media_id);
                            message.success("已触发重试");
                            await loadAtrackTasks();
                          } catch {
                            message.error("重试失败");
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
          label: "关键帧提取任务",
          children: (
            <Card
              title="关键帧提取任务"
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
                  { title: "媒体ID", dataIndex: "media_id", width: 90 },
                  { title: "标题", dataIndex: "title", ellipsis: true },
                  { title: "状态", dataIndex: "status", width: 110 },
                  { title: "关键帧数", dataIndex: "keyframe_count", width: 100 },
                  { title: "输出目录", dataIndex: "output_dir", ellipsis: true, render: (v?: string) => v || "-" },
                  { title: "错误信息", dataIndex: "error_message", ellipsis: true, render: (v?: string) => v || "-" },
                  { title: "更新时间", dataIndex: "updated_at", width: 180 },
                  {
                    title: "操作",
                    key: "actions",
                    width: 70,
                    align: "center",
                    render: (_: unknown, r: KeyframeTask) => (
                      <ActionIconButton
                        title="重试"
                        icon={<RedoOutlined />}
                        loading={retryingKeyframeId === r.media_id}
                        onClick={async () => {
                          setRetryingKeyframeId(r.media_id);
                          try {
                            await retryKeyframeExtraction(r.media_id);
                            message.success("已触发重试");
                            await loadKeyframeTasks();
                          } catch {
                            message.error("重试失败");
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
        title="创建定时任务"
        open={createModalOpen}
        onCancel={() => {
          setCreateModalOpen(false);
          form.resetFields();
        }}
        onOk={() => void onCreateScheduled()}
        confirmLoading={creatingSchedule}
      >
        <Form form={form} layout="vertical" initialValues={{ category: "media", interval_min: 60, enabled: true, task_type: "library_scan" }}>
          <Form.Item name="name" label="任务名称" rules={[{ required: true }]}><Input /></Form.Item>
          <Form.Item name="category" label="任务分类" rules={[{ required: true }]}>
            <Select options={[{ value: "media", label: "媒体库相关" }, { value: "maintenance", label: "系统维护" }]} />
          </Form.Item>
          <Form.Item name="task_type" label="任务类型" rules={[{ required: true }]}>
            <Select
              options={[
                { value: "library_scan", label: "定时扫描媒体库" },
                { value: "subtitle_process", label: "定时字幕处理（批量）" },
                { value: "scrape_run", label: "定时执行刮削任务" },
                { value: "transcode_cleanup_failed_before", label: "清理历史转码失败任务" },
                { value: "activity_cleanup", label: "清理活动日志" },
                { value: "db_optimize", label: "优化数据库" },
              ]}
            />
          </Form.Item>
          <Form.Item name="interval_min" label="执行间隔（分钟）" rules={[{ required: true }]}><InputNumber min={1} style={{ width: "100%" }} /></Form.Item>
          <Form.Item name="enabled" label="启用" valuePropName="checked"><Switch /></Form.Item>
          <Space wrap>
            {createTaskType === "library_scan" ? (
              <Form.Item name="library_id" label="媒体库（扫描任务）">
                <Select
                  allowClear
                  showSearch
                  placeholder="选择媒体库"
                  options={libraryOptions}
                  optionFilterProp="label"
                  style={{ width: 240 }}
                />
              </Form.Item>
            ) : null}
            {createTaskType === "scrape_run" ? (
              <Form.Item name="limit" label="处理条数（刮削任务）"><InputNumber min={1} max={200} /></Form.Item>
            ) : null}
            {createTaskType === "subtitle_process" ? (
              <>
                <Form.Item name="library_id" label="媒体库（0 表示全部）">
                  <Select allowClear showSearch placeholder="留空或 0 为全部库" options={[{ value: 0, label: "全部" }, ...libraryOptions]} optionFilterProp="label" style={{ width: 280 }} />
                </Form.Item>
                <Form.Item name="limit" label="每轮处理视频数量"><InputNumber min={1} max={500} placeholder="默认 50" style={{ width: 200 }} /></Form.Item>
              </>
            ) : null}
            {createTaskType === "transcode_cleanup_failed_before" || createTaskType === "activity_cleanup" ? (
              <Form.Item name="days" label="保留天数（清理任务）"><InputNumber min={1} max={3650} /></Form.Item>
            ) : null}
          </Space>
        </Form>
      </Modal>
      <Modal
        title="编辑定时任务"
        open={!!editingTask}
        onCancel={() => setEditingTask(null)}
        onOk={() => void onUpdateScheduled()}
        confirmLoading={updatingSchedule}
      >
        <Form form={editForm} layout="vertical">
          <Form.Item name="name" label="任务名称" rules={[{ required: true }]}><Input /></Form.Item>
          <Form.Item name="category" label="任务分类" rules={[{ required: true }]}>
            <Select options={[{ value: "media", label: "媒体库相关" }, { value: "maintenance", label: "系统维护" }]} />
          </Form.Item>
          <Form.Item name="task_type" label="任务类型" rules={[{ required: true }]}>
            <Select
              options={[
                { value: "library_scan", label: "定时扫描媒体库" },
                { value: "subtitle_process", label: "定时字幕处理（批量）" },
                { value: "scrape_run", label: "定时执行刮削任务" },
                { value: "transcode_cleanup_failed_before", label: "清理历史转码失败任务" },
                { value: "activity_cleanup", label: "清理活动日志" },
                { value: "db_optimize", label: "优化数据库" },
              ]}
            />
          </Form.Item>
          <Form.Item name="interval_min" label="执行间隔（分钟）" rules={[{ required: true }]}><InputNumber min={1} style={{ width: "100%" }} /></Form.Item>
          <Form.Item name="enabled" label="启用" valuePropName="checked"><Switch /></Form.Item>
          <Space wrap>
            {editTaskType === "library_scan" ? (
              <Form.Item name="library_id" label="媒体库（扫描任务）">
                <Select
                  allowClear
                  showSearch
                  placeholder="选择媒体库"
                  options={libraryOptions}
                  optionFilterProp="label"
                  style={{ width: 240 }}
                />
              </Form.Item>
            ) : null}
            {editTaskType === "scrape_run" ? (
              <Form.Item name="limit" label="处理条数（刮削任务）"><InputNumber min={1} max={200} /></Form.Item>
            ) : null}
            {editTaskType === "subtitle_process" ? (
              <>
                <Form.Item name="library_id" label="媒体库（0 表示全部）">
                  <Select allowClear showSearch placeholder="留空或 0 为全部库" options={[{ value: 0, label: "全部" }, ...libraryOptions]} optionFilterProp="label" style={{ width: 280 }} />
                </Form.Item>
                <Form.Item name="limit" label="每轮处理视频数量"><InputNumber min={1} max={500} placeholder="默认 50" style={{ width: 200 }} /></Form.Item>
              </>
            ) : null}
            {editTaskType === "transcode_cleanup_failed_before" || editTaskType === "activity_cleanup" ? (
              <Form.Item name="days" label="保留天数（清理任务）"><InputNumber min={1} max={3650} /></Form.Item>
            ) : null}
          </Space>
        </Form>
      </Modal>
    </>
  );
}
