import { useCallback, useEffect, useState } from "react";
import { Drawer, Button, Space, Tag, Descriptions, Collapse, Spin, Tooltip, Popconfirm } from "antd";
import {
  StopOutlined,
  DeleteOutlined,
  ReloadOutlined,
  RiseOutlined,
  ForwardOutlined,
  UnlockOutlined,
  CloseOutlined,
} from "@ant-design/icons";
import type { DetailResult, ProjectionRow } from "../../api/taskControl";
import { fetchTaskControlDetail, fetchTaskControlActions } from "../../api/taskControl";
import { useT, tGlobal } from "../../i18n";

export interface TaskDetailDrawerProps {
  taskId: string | null;
  onClose: () => void;
  onActionSuccess?: (row: ProjectionRow) => void;
}

type PendingAction = "abort" | "remove" | "reset" | "run_now" | "skip" | "reopen" | null;

function statusColor(status: string): string {
  switch (status) {
    case "waiting": return "#49aa19";
    case "running": return "#1677ff";
    case "done": return "#52c41a";
    case "failed": return "#ff4d4f";
    case "cancelled": return "#faad14";
    case "skipped": return "#888";
    default: return "#d9d9d9";
  }
}

export function TaskDetailDrawer({ taskId, onClose, onActionSuccess }: TaskDetailDrawerProps) {
  const t = useT();
  const [detail, setDetail] = useState<DetailResult | null>(null);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [actionPending, setActionPending] = useState<"abort" | "remove" | "reset" | "run_now" | "skip" | "reopen" | null>(null);

  const load = useCallback(async () => {
    if (!taskId) return;
    setLoading(true);
    setError(null);
    try {
      const result = await fetchTaskControlDetail(taskId);
      if (result) {
        setDetail(result);
      } else {
        setError(tGlobal("tasks.control.detail_not_found"));
      }
    } catch {
      setError(tGlobal("tasks.control.detail_load_failed"));
    } finally {
      setLoading(false);
    }
  }, [taskId]);

  useEffect(() => {
    if (taskId) {
      void load();
    } else {
      setDetail(null);
    }
  }, [taskId, load]);

  const executeAction = useCallback(
    async (action: string) => {
      if (!taskId) return;
      setActionPending(action as PendingAction);
      try {
        const result = await fetchTaskControlActions(taskId, {
          action,
          reason: action,
        });
        if (result.row) {
          onActionSuccess?.(result.row);
          setDetail((prev) => prev ? { ...prev, row: result.row! } : prev);
        }
      } catch (err: unknown) {
        const ax = err as { response?: { data?: { message?: string } } };
        setError(ax.response?.data?.message || tGlobal("tasks.control.action_failed", { action }));
      } finally {
        setActionPending(null);
      }
    },
    [taskId, onActionSuccess],
  );

  const row = detail?.row;

  return (
    <Drawer
      title={t("tasks.control.drawer_title")}
      open={taskId !== null}
      onClose={onClose}
      width={520}
      closeIcon={<CloseOutlined />}
      extra={null}
    >
      {loading && (
        <div style={{ textAlign: "center", padding: 40 }}>
          <Spin />
        </div>
      )}

      {error && detail && (
        <div style={{ marginBottom: 16 }}>
          <Tag color="error">{error}</Tag>
        </div>
      )}

      {/* Action toolbar — placed in body to avoid header overlap */}
      <div
        style={{
          display: "flex",
          flexWrap: "wrap",
          gap: 4,
          padding: "0 0 12px 0",
          borderBottom: "1px solid #303030",
        }}
      >
        <Tooltip title={t("tasks.control.action_abort")}>
          <Popconfirm
            title={t("tasks.control.confirm_abort")}
            onConfirm={() => executeAction("abort")}
            okText={t("tasks.control.confirm")}
            cancelText={t("tasks.control.cancel")}
          >
            <Button size="small" icon={<StopOutlined />} disabled={actionPending !== null}>
              {t("tasks.control.action_abort")}
            </Button>
          </Popconfirm>
        </Tooltip>
        <Tooltip title={t("tasks.control.action_reset")}>
          <Popconfirm
            title={t("tasks.control.confirm_reset")}
            onConfirm={() => executeAction("reset")}
            okText={t("tasks.control.confirm")}
            cancelText={t("tasks.control.cancel")}
          >
            <Button size="small" icon={<ReloadOutlined />} disabled={actionPending !== null}>
              {t("tasks.control.action_reset")}
            </Button>
          </Popconfirm>
        </Tooltip>
        <Tooltip title={t("tasks.control.action_run_now")}>
          <Popconfirm
            title={t("tasks.control.confirm_run_now")}
            onConfirm={() => executeAction("run_now")}
            okText={t("tasks.control.confirm")}
            cancelText={t("tasks.control.cancel")}
          >
            <Button size="small" icon={<RiseOutlined />} disabled={actionPending !== null}>
              {t("tasks.control.action_run_now")}
            </Button>
          </Popconfirm>
        </Tooltip>
        <Tooltip title={t("tasks.control.action_skip")}>
          <Popconfirm
            title={t("tasks.control.confirm_skip")}
            onConfirm={() => executeAction("skip")}
            okText={t("tasks.control.confirm")}
            cancelText={t("tasks.control.cancel")}
          >
            <Button size="small" icon={<ForwardOutlined />} disabled={actionPending !== null}>
              {t("tasks.control.action_skip")}
            </Button>
          </Popconfirm>
        </Tooltip>
        <Tooltip title={t("tasks.control.action_remove")}>
          <Popconfirm
            title={t("tasks.control.confirm_remove")}
            onConfirm={() => executeAction("remove")}
            okText={t("tasks.control.confirm")}
            cancelText={t("tasks.control.cancel")}
          >
            <Button size="small" icon={<DeleteOutlined />} disabled={actionPending !== null}>
              {t("tasks.control.action_remove")}
            </Button>
          </Popconfirm>
        </Tooltip>
        <Tooltip title={t("tasks.control.action_reopen")}>
          <Popconfirm
            title={t("tasks.control.confirm_reopen")}
            onConfirm={() => executeAction("reopen")}
            okText={t("tasks.control.confirm")}
            cancelText={t("tasks.control.cancel")}
          >
            <Button size="small" icon={<UnlockOutlined />} disabled={actionPending !== null}>
              {t("tasks.control.action_reopen")}
            </Button>
          </Popconfirm>
        </Tooltip>
      </div>

      {detail && row && !loading && (
        <Space direction="vertical" size="middle" style={{ width: "100%" }}>
          <Descriptions column={1} size="small" bordered>
            <Descriptions.Item label={t("tasks.control.detail_task_id")}>
              <span style={{ fontFamily: "monospace", color: "#1677ff" }}>{row.task_id}</span>
            </Descriptions.Item>
            <Descriptions.Item label={t("tasks.control.detail_type")}>
              <Tag>{row.task_type}</Tag>
            </Descriptions.Item>
            <Descriptions.Item label={t("tasks.control.detail_family")}>
              {row.family}
            </Descriptions.Item>
            <Descriptions.Item label={t("tasks.control.detail_status")}>
              <Tag color={statusColor(row.normalized_status)}>
                {t(`tasks.control.status_${row.normalized_status}`)}
              </Tag>
              <span style={{ marginLeft: 8, color: "#666", fontSize: 12 }}>({row.raw_status})</span>
            </Descriptions.Item>
            <Descriptions.Item label={t("tasks.control.detail_revision")}>{row.revision}</Descriptions.Item>
            <Descriptions.Item label={t("tasks.control.detail_generation")}>{row.generation}</Descriptions.Item>
            <Descriptions.Item label={t("tasks.control.detail_retry_round")}>{row.retry_round}</Descriptions.Item>
            <Descriptions.Item label={t("tasks.control.detail_attempt")}>
              {row.attempt} / {row.max_attempts} {t("tasks.control.detail_max_attempts")}
            </Descriptions.Item>
            <Descriptions.Item label={t("tasks.control.detail_source_kind")}>{row.source_kind}</Descriptions.Item>
            <Descriptions.Item label={t("tasks.control.detail_source_id")}>
              <span style={{ fontFamily: "monospace" }}>{row.source_id}</span>
            </Descriptions.Item>
            <Descriptions.Item label={t("tasks.control.detail_media")}>
              {row.media_id ? (
                <span>
                  <span style={{ fontFamily: "monospace", color: "#1677ff" }}>#{row.media_id}</span>
                  {row.media_title && <span style={{ marginLeft: 8 }}>{row.media_title}</span>}
                  {row.media_file_path && (
                    <div style={{ fontSize: 12, color: "#888", marginTop: 2, wordBreak: "break-all" }}>
                      {row.media_file_path}
                    </div>
                  )}
                </span>
              ) : (
                <span style={{ color: "#555" }}>-</span>
              )}
            </Descriptions.Item>
            <Descriptions.Item label={t("tasks.control.detail_priority")}>
              {row.base_priority}
              {row.effective_priority !== row.base_priority && (
                <span style={{ color: "#1677ff" }}> → {row.effective_priority} {t("tasks.control.detail_effective_priority")}</span>
              )}
            </Descriptions.Item>
            {row.available_at && (
              <Descriptions.Item label={t("tasks.control.detail_available_at")}>
                {new Date(row.available_at).toLocaleString()}
              </Descriptions.Item>
            )}
            <Descriptions.Item label={t("tasks.control.detail_created_at")}>
              {new Date(row.created_at).toLocaleString()}
            </Descriptions.Item>
            <Descriptions.Item label={t("tasks.control.detail_updated_at")}>
              {new Date(row.updated_at).toLocaleString()}
            </Descriptions.Item>
          </Descriptions>

          {row.terminal_reason && (
            <Collapse
              size="small"
              items={[{
                key: "terminal",
                label: <span style={{ color: "#ff4d4f" }}>{t("tasks.control.detail_terminal_reason")}</span>,
                children: <span style={{ color: "#ff9999" }}>{row.terminal_reason}</span>,
              }]}
            />
          )}

          {row.removed_at && (
            <Collapse
              size="small"
              items={[{
                key: "removed",
                label: <span style={{ color: "#faad14" }}>{t("tasks.control.detail_removed")}</span>,
                children: (
                  <Space direction="vertical" size={2}>
                    <span style={{ fontSize: 12, color: "#aaa" }}>{t("tasks.control.detail_removed_at")}: {new Date(row.removed_at).toLocaleString()}</span>
                    {row.removed_by && <span style={{ fontSize: 12, color: "#aaa" }}>{t("tasks.control.detail_removed_by")}: {row.removed_by}</span>}
                    {row.remove_reason && <span style={{ fontSize: 12, color: "#aaa" }}>{t("tasks.control.detail_remove_reason")}: {row.remove_reason}</span>}
                  </Space>
                ),
              }]}
            />
          )}

          {row.owner_lease && (
            <Collapse
              size="small"
              items={[{
                key: "lease",
                label: <span style={{ color: "#1677ff" }}>{t("tasks.control.detail_owner_lease")}</span>,
                children: (
                  <Space direction="vertical" size={2}>
                    <span style={{ fontSize: 12, color: "#aaa" }}>{t("tasks.control.detail_owner")}: {row.owner_lease.owner}</span>
                    {row.owner_lease.lease_until && (
                      <span style={{ fontSize: 12, color: "#aaa" }}>{t("tasks.control.detail_lease_until")}: {new Date(row.owner_lease.lease_until).toLocaleString()}</span>
                    )}
                  </Space>
                ),
              }]}
            />
          )}

          {detail.attempts && detail.attempts.length > 0 && (
            <Collapse
              size="small"
              items={[{
                key: "attempts",
                label: `${t("tasks.control.detail_section_attempts")} (${detail.attempts.length})`,
                children: (
                  <Space direction="vertical" size={4} style={{ width: "100%" }}>
                    {detail.attempts.map((a, i) => (
                      <div key={i} style={{ fontSize: 12, color: "#888", padding: "4px 8px", borderBottom: "1px solid #1a1a1a" }}>
                        #{a.attempt}: <Tag style={{ margin: "0 4px" }}>{a.status}</Tag>
                        {a.error && <span style={{ color: "#ff4d4f" }}>{a.error}</span>}
                        {a.duration_secs != null && <span style={{ marginLeft: 8 }}>({a.duration_secs}s)</span>}
                      </div>
                    ))}
                  </Space>
                ),
              }]}
            />
          )}

          {detail.audit_events && detail.audit_events.length > 0 && (
            <Collapse
              size="small"
              items={[{
                key: "audit",
                label: `${t("tasks.control.detail_section_audit")} (${detail.audit_events.length})`,
                children: (
                  <Space direction="vertical" size={2} style={{ width: "100%" }}>
                    {detail.audit_events.map((e) => (
                      <div key={e.id} style={{ fontSize: 11, color: "#666", padding: "2px 8px", borderBottom: "1px solid #1a1a1a" }}>
                        [{e.created_at}] {e.action} @{e.actor_name}
                        {e.reason ? ` - ${e.reason}` : ""}
                      </div>
                    ))}
                  </Space>
                ),
              }]}
            />
          )}
        </Space>
      )}
    </Drawer>
  );
}
