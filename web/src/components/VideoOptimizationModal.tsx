import { Button, Checkbox, List, message, Modal, Progress, Select, Spin, Tag, Typography } from "antd";
import { CheckCircleOutlined, CloseCircleOutlined, ClockCircleOutlined, DeleteOutlined, LoadingOutlined, SyncOutlined } from "@ant-design/icons";
import { useCallback, useEffect, useMemo, useRef, useState } from "react";
import {
  type MediaOptimizationStatus,
  type OptimizedRendition,
  type Preset,
  fetchMediaOptimizationStatus,
  fetchPresets,
  createOptimizationTask,
  removeOptimizationRendition,
  batchRemoveOptimizationRenditions,
} from "../api/pretranscode";
import { useT } from "../i18n";

const { Text } = Typography;

interface VideoOptimizationModalProps {
  mediaId: number;
  mediaTitle?: string;
  open: boolean;
  onClose: () => void;
  onOptimized?: () => void;
}

function formatFileSize(bytes: number): string {
  if (bytes < 1024) return `${bytes} B`;
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KB`;
  if (bytes < 1024 * 1024 * 1024) return `${(bytes / (1024 * 1024)).toFixed(1)} MB`;
  return `${(bytes / (1024 * 1024 * 1024)).toFixed(2)} GB`;
}

export default function VideoOptimizationModal({
  mediaId,
  mediaTitle,
  open,
  onClose,
  onOptimized,
}: VideoOptimizationModalProps) {
  const t = useT();
  const [loading, setLoading] = useState(false);
  const [status, setStatus] = useState<MediaOptimizationStatus | null>(null);
  const [presets, setPresets] = useState<Preset[]>([]);
  const [selectedPresetId, setSelectedPresetId] = useState<number | null>(null);
  const [creating, setCreating] = useState(false);
  const [selectedRenditionIds, setSelectedRenditionIds] = useState<Set<number>>(new Set());
  const [removing, setRemoving] = useState(false);
  const pollTimerRef = useRef<ReturnType<typeof setInterval> | null>(null);

  const loadData = useCallback(async () => {
    setLoading(true);
    try {
      const [statusData, presetsData] = await Promise.all([
        fetchMediaOptimizationStatus(mediaId),
        fetchPresets(),
      ]);
      setStatus(statusData);
      setPresets(presetsData.filter((p) => p.is_enabled));
      if (presetsData.length > 0 && selectedPresetId === null) {
        setSelectedPresetId(presetsData[0].id);
      }
    } catch {
      message.error(t("common.loading_failed"));
    } finally {
      setLoading(false);
    }
  }, [mediaId, selectedPresetId, t]);

  // Lightweight status-only refresh for polling (no presets fetch, no loading spinner)
  const refreshStatus = useCallback(async () => {
    try {
      const statusData = await fetchMediaOptimizationStatus(mediaId);
      setStatus(statusData);
    } catch {
      // ignore polling errors
    }
  }, [mediaId]);

  useEffect(() => {
    if (open) {
      loadData();
      setSelectedRenditionIds(new Set());
    }
  }, [open, loadData]);

  // Poll for status updates when there are running tasks
  useEffect(() => {
    if (open && status && (status.running_tasks ?? []).length > 0) {
      // Start polling every 3 seconds
      pollTimerRef.current = setInterval(() => {
        refreshStatus();
      }, 3000);
      return () => {
        if (pollTimerRef.current) {
          clearInterval(pollTimerRef.current);
          pollTimerRef.current = null;
        }
      };
    } else {
      // Stop polling when no running tasks
      if (pollTimerRef.current) {
        clearInterval(pollTimerRef.current);
        pollTimerRef.current = null;
      }
    }
  }, [open, status, refreshStatus]);

  // Cleanup on unmount
  useEffect(() => {
    return () => {
      if (pollTimerRef.current) {
        clearInterval(pollTimerRef.current);
        pollTimerRef.current = null;
      }
    };
  }, []);

  const selectedPreset = useMemo(
    () => presets.find((p) => p.id === selectedPresetId),
    [presets, selectedPresetId]
  );

  const existingRenditionNames = useMemo(
    () => new Set((status?.optimized_renditions ?? []).map((r) => r.rendition_name)),
    [status]
  );

  const newRenditions = useMemo(() => {
    if (!selectedPreset?.renditions) return [];
    return selectedPreset.renditions.filter((r) => !existingRenditionNames.has(r.name));
  }, [selectedPreset, existingRenditionNames]);

  const hasActiveTask = useMemo(
    () => (status?.running_tasks ?? []).some((t) => ["waiting", "running", "paused"].includes(t.status)),
    [status]
  );

  const hasFailedTask = useMemo(
    () => (status?.running_tasks ?? []).some((t) => t.status === "failed"),
    [status]
  );

  const handleRemoveSingle = useCallback(
    async (rendition: OptimizedRendition) => {
      Modal.confirm({
        title: t("components.video_optimization_modal.remove_confirm_title"),
        centered: true,
        okText: t("components.video_optimization_modal.remove"),
        cancelText: t("common.cancel"),
        okButtonProps: { danger: true },
        content: t("components.video_optimization_modal.remove_confirm_content", {
          name: rendition.rendition_name,
        }),
        onOk: async () => {
          try {
            await removeOptimizationRendition(mediaId, rendition.rendition_job_id);
            message.success(t("components.video_optimization_modal.remove_success"));
            await loadData();
            onOptimized?.();
          } catch {
            message.error(t("components.video_optimization_modal.remove_failed"));
          }
        },
      });
    },
    [mediaId, t, loadData, onOptimized]
  );

  const handleBatchRemove = useCallback(async () => {
    if (selectedRenditionIds.size === 0) return;
    Modal.confirm({
      title: t("components.video_optimization_modal.batch_remove"),
      centered: true,
      okText: t("components.video_optimization_modal.remove"),
      cancelText: t("common.cancel"),
      okButtonProps: { danger: true },
      content: t("components.video_optimization_modal.batch_remove_confirm", {
        count: selectedRenditionIds.size,
      }),
      onOk: async () => {
        setRemoving(true);
        try {
          await batchRemoveOptimizationRenditions(mediaId, [...selectedRenditionIds]);
          message.success(t("components.video_optimization_modal.remove_success"));
          setSelectedRenditionIds(new Set());
          await loadData();
          onOptimized?.();
        } catch {
          message.error(t("components.video_optimization_modal.remove_failed"));
        } finally {
          setRemoving(false);
        }
      },
    });
  }, [mediaId, selectedRenditionIds, t, loadData, onOptimized]);

  const handleToggleSelect = useCallback(
    (id: number, checked: boolean) => {
      setSelectedRenditionIds((prev) => {
        const next = new Set(prev);
        if (checked) next.add(id);
        else next.delete(id);
        return next;
      });
    },
    []
  );

  const handleSelectAll = useCallback(() => {
    if (!status) return;
    const renditions = status.optimized_renditions ?? [];
    if (selectedRenditionIds.size === renditions.length) {
      setSelectedRenditionIds(new Set());
    } else {
      setSelectedRenditionIds(new Set(renditions.map((r) => r.rendition_job_id)));
    }
  }, [status, selectedRenditionIds]);

  const handleStartOptimization = useCallback(async () => {
    if (!selectedPresetId || hasActiveTask) return;
    setCreating(true);
    try {
      await createOptimizationTask(mediaId, selectedPresetId, true);
      message.success(t("components.video_optimization_modal.optimization_created"));
      await loadData();
      onOptimized?.();
    } catch {
      message.error(t("components.video_optimization_modal.optimization_failed"));
    } finally {
      setCreating(false);
    }
  }, [mediaId, selectedPresetId, hasActiveTask, t, loadData, onOptimized]);

  const allSelected = useMemo(
    () => status !== null && (status.optimized_renditions ?? []).length > 0 && selectedRenditionIds.size === (status.optimized_renditions ?? []).length,
    [status, selectedRenditionIds]
  );

  const canCreateOptimization = status?.optimization_available !== false;

  return (
    <Modal
      open={open}
      title={t("components.video_optimization_modal.title")}
      onCancel={onClose}
      footer={null}
      destroyOnClose
      width={640}
    >
      {mediaTitle && (
        <div style={{ marginBottom: 16, color: "#e6edf3", fontSize: 14 }}>
          {mediaTitle}
        </div>
      )}

      {loading ? (
        <div style={{ textAlign: "center", padding: "40px 0" }}>
          <Spin />
          <div style={{ marginTop: 8, color: "#8c8c8c" }}>
            {t("components.video_optimization_modal.loading")}
          </div>
        </div>
      ) : (
        <>
          {/* Optimized Renditions Section - only shown when there are renditions */}
          {status && (status.optimized_renditions ?? []).length > 0 && (
            <div style={{ marginBottom: 24 }}>
              <div style={{ display: "flex", justifyContent: "space-between", alignItems: "center", marginBottom: 12 }}>
                <Text strong style={{ color: "#e6edf3", fontSize: 14 }}>
                  {t("components.video_optimization_modal.optimized_renditions")}
                </Text>
                <div style={{ display: "flex", gap: 8 }}>
                  <Button size="small" onClick={handleSelectAll}>
                    {allSelected ? t("common.deselect_all") : t("common.select_all")}
                  </Button>
                  <Button
                    size="small"
                    danger
                    icon={<DeleteOutlined />}
                    disabled={selectedRenditionIds.size === 0}
                    loading={removing}
                    onClick={handleBatchRemove}
                  >
                    {t("components.video_optimization_modal.batch_remove")}
                  </Button>
                </div>
              </div>

              <List
                size="small"
                dataSource={status.optimized_renditions ?? []}
                style={{ background: "rgba(255,255,255,0.04)", borderRadius: 8, maxHeight: 240, overflowY: "auto" }}
                renderItem={(item) => (
                  <List.Item
                    style={{ padding: "8px 12px" }}
                    actions={[
                      <Button
                        key="remove"
                        type="text"
                        size="small"
                        danger
                        icon={<DeleteOutlined />}
                        onClick={() => handleRemoveSingle(item)}
                      >
                        {t("components.video_optimization_modal.remove")}
                      </Button>,
                    ]}
                  >
                    <Checkbox
                      checked={selectedRenditionIds.has(item.rendition_job_id)}
                      onChange={(e) => handleToggleSelect(item.rendition_job_id, e.target.checked)}
                      style={{ marginRight: 12 }}
                    />
                    <div style={{ flex: 1, display: "flex", alignItems: "center", gap: 12 }}>
                      <Tag color="blue" style={{ margin: 0, minWidth: 50, textAlign: "center" }}>
                        {item.rendition_name}
                      </Tag>
                      <Text style={{ color: "#8c8c8c", fontSize: 12 }}>
                        {item.resolution}
                      </Text>
                      <Text style={{ color: "#8c8c8c", fontSize: 12 }}>
                        {item.bitrate}
                      </Text>
                      <Text style={{ color: "#8c8c8c", fontSize: 12 }}>
                        {item.preset_name}
                      </Text>
                      <Text style={{ color: "#8c8c8c", fontSize: 12, marginLeft: "auto" }}>
                        {formatFileSize(item.file_size)}
                      </Text>
                    </div>
                  </List.Item>
                )}
              />
            </div>
          )}

          {/* Running Tasks */}
          {status && (status.running_tasks ?? []).length > 0 && (
            <div style={{ marginBottom: 24 }}>
              <Text strong style={{ color: "#e6edf3", fontSize: 14, display: "block", marginBottom: 12 }}>
                {t("components.video_optimization_modal.running_tasks")}
              </Text>
              {(status.running_tasks ?? []).map((task) => {
                const statusConfig: Record<string, { icon: React.ReactNode; color: string; label: string }> = {
                  running: { icon: <LoadingOutlined spin />, color: "#1890ff", label: t("components.video_optimization_modal.status_running") },
                  waiting: { icon: <ClockCircleOutlined />, color: "#faad14", label: t("components.video_optimization_modal.status_waiting") },
                  paused: { icon: <SyncOutlined />, color: "#8c8c8c", label: t("components.video_optimization_modal.status_paused") },
                  failed: { icon: <CloseCircleOutlined />, color: "#ff4d4f", label: t("components.video_optimization_modal.status_failed") },
                  done: { icon: <CheckCircleOutlined />, color: "#52c41a", label: t("components.video_optimization_modal.status_done") },
                };
                const config = statusConfig[task.status] ?? statusConfig.waiting;
                return (
                  <div
                    key={task.task_id}
                    style={{
                      padding: "12px",
                      background: "rgba(24, 144, 255, 0.06)",
                      borderRadius: 8,
                      marginBottom: 8,
                      border: `1px solid ${config.color}20`,
                    }}
                  >
                    <div style={{ display: "flex", alignItems: "center", gap: 8, marginBottom: 8 }}>
                      <span style={{ color: config.color, fontSize: 16 }}>{config.icon}</span>
                      <Text style={{ color: "#e6edf3", flex: 1 }}>{task.preset_name}</Text>
                      <Tag color={config.color} style={{ margin: 0 }}>
                        {config.label}
                      </Tag>
                    </div>
                    {task.status === "running" && (
                      <Progress
                        percent={task.progress}
                        size="small"
                        strokeColor="#1890ff"
                        trailColor="rgba(255,255,255,0.08)"
                        format={(percent) => <span style={{ color: "#e6edf3", fontSize: 12 }}>{percent}%</span>}
                      />
                    )}
                    {task.status === "failed" && task.error_message && (
                      <Text style={{ color: "#ff4d4f", fontSize: 12, display: "block", marginTop: 4 }}>
                        {task.error_message}
                      </Text>
                    )}
                  </div>
                );
              })}
            </div>
          )}

          {/* New Optimization Section */}
          <div style={{ borderTop: "1px solid rgba(255,255,255,0.08)", paddingTop: 16 }}>
            <Text strong style={{ color: "#e6edf3", fontSize: 14, display: "block", marginBottom: 12 }}>
              {t("components.video_optimization_modal.new_optimization")}
            </Text>

            {!canCreateOptimization && (
              <div style={{ marginBottom: 12, color: "#8c8c8c", fontSize: 13 }}>
                {t("components.video_optimization_modal.plaintext_required")}
              </div>
            )}

            <div style={{ display: "flex", gap: 12, alignItems: "center", marginBottom: 12 }}>
              <Text style={{ color: "#8c8c8c", whiteSpace: "nowrap" }}>
                {t("components.video_optimization_modal.select_template")}:
              </Text>
              <Select
                style={{ flex: 1 }}
                value={selectedPresetId}
                onChange={setSelectedPresetId}
                options={presets.map((p) => ({
                  value: p.id,
                  label: `${p.name} · ${p.output_format.toUpperCase()} · ${p.renditions?.length ?? 0}`,
                }))}
                placeholder={t("components.video_optimization_modal.select_template")}
              />
            </div>

            {selectedPreset && selectedPreset.renditions && selectedPreset.renditions.length > 0 && (
              <div style={{ marginBottom: 12 }}>
                <Text style={{ color: "#8c8c8c", fontSize: 12, display: "block", marginBottom: 4 }}>
                  {t("components.video_optimization_modal.will_add_renditions")}
                </Text>
                <div style={{ display: "flex", flexWrap: "wrap", gap: 8 }}>
                  {selectedPreset.renditions.map((r) => {
                    const exists = existingRenditionNames.has(r.name);
                    return (
                      <Tag
                        key={r.name}
                        color={exists ? "default" : "green"}
                        style={{ margin: 0 }}
                      >
                        {r.name} {exists
                          ? `(${t("components.video_optimization_modal.already_exists")})`
                          : `(${t("components.video_optimization_modal.new")})`}
                      </Tag>
                    );
                  })}
                </div>
              </div>
            )}

            {hasActiveTask && (
              <div style={{ marginBottom: 12, color: "#faad14", fontSize: 12 }}>
                {t("components.video_optimization_modal.task_running")}
              </div>
            )}

            <Button
              type="primary"
              block
              disabled={!canCreateOptimization || !selectedPresetId || hasActiveTask || (newRenditions.length === 0 && !hasFailedTask)}
              loading={creating}
              onClick={handleStartOptimization}
            >
              {t("components.video_optimization_modal.start_optimization")}
            </Button>
          </div>
        </>
      )}
    </Modal>
  );
}
