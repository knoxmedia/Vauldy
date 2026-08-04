import { useCallback, useEffect, useRef, useState } from "react";
import { Table, Tag, Button, Select, Space, Popconfirm, Tooltip, message, Alert } from "antd";
import {
  StopOutlined,
  DeleteOutlined,
  ReloadOutlined,
  RiseOutlined,
  ForwardOutlined,
  UnlockOutlined,
  ClearOutlined,
} from "@ant-design/icons";
import type { ColumnsType } from "antd/es/table";
import type { TableRowSelection } from "antd/es/table/interface";
import type { ListResult, ProjectionRow, BatchResult } from "../../api/taskControl";
import { fetchTaskControlList, fetchTaskControlActions, fetchTaskControlBatch } from "../../api/taskControl";
import type { TaskControlFilter } from "../../lib/taskControlFilters";
import { useT, tGlobal } from "../../i18n";

export interface TaskListProps {
  taskType: string;
  filter?: TaskControlFilter;
  removed?: string;
  onSelectRow?: (taskId: string) => void;
  onActionSuccess?: () => void;
}

interface LoadState {
  loading: boolean;
  error: string | null;
  data: ListResult | null;
}

const STATUS_OPTIONS = [
  { value: "", label_key: "all_statuses" },
  { value: "waiting", label_key: "status_waiting" },
  { value: "running", label_key: "status_running" },
  { value: "done", label_key: "status_done" },
  { value: "failed", label_key: "status_failed" },
  { value: "cancelled", label_key: "status_cancelled" },
  { value: "skipped", label_key: "status_skipped" },
] as const;

const REMOVED_OPTIONS = [
  { value: "exclude", label_key: "active_only" },
  { value: "include", label_key: "include_removed" },
  { value: "only", label_key: "removed_only" },
] as const;

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

export function TaskList({ taskType, filter: extFilter, removed, onSelectRow, onActionSuccess }: TaskListProps) {
  const t = useT();

  const [state, setState] = useState<LoadState>({ loading: false, error: null, data: null });
  const [selectedRowKeys, setSelectedRowKeys] = useState<React.Key[]>([]);
  const [actionPending, setActionPending] = useState(false);
  const [localStatus, setLocalStatus] = useState<string>("");
  const [localRemoved, setLocalRemoved] = useState<string>(removed ?? "exclude");

  const cursorRef = useRef<string>("");
  const mountedRef = useRef(true);

  const load = useCallback(
    async (cursor: string, status?: string, removedVal?: string) => {
      setState((s) => ({ ...s, loading: true, error: null }));
      try {
        const result = await fetchTaskControlList({
          task_type: taskType,
          status: status || extFilter?.status,
          removed: removedVal ?? removed ?? extFilter?.removed ?? "exclude",
          library_id: extFilter?.library_id,
          generation: extFilter?.generation,
          cursor: cursor || undefined,
          limit: 50,
        });
        if (!mountedRef.current) return;
        setState({ loading: false, error: null, data: result });
        cursorRef.current = result.next_cursor ?? "";
      } catch {
        if (!mountedRef.current) return;
        setState((s) => ({ ...s, loading: false, error: tGlobal("tasks.control.load_failed") }));
      }
    },
    [taskType, extFilter, removed],
  );

  useEffect(() => {
    mountedRef.current = true;
    cursorRef.current = "";
    void load("");
    return () => { mountedRef.current = false; };
  }, [load]);

  const handleStatusChange = useCallback((val: string) => {
    setLocalStatus(val);
    cursorRef.current = "";
    void load("", val, localRemoved);
  }, [load, localRemoved]);

  const handleRemovedChange = useCallback((val: string) => {
    setLocalRemoved(val);
    cursorRef.current = "";
    void load("", localStatus, val);
  }, [load, localStatus]);

  const handleTableChange = useCallback(
    (pagination: { current?: number; pageSize?: number }) => {
      const page = pagination.current || 1;
      if (page === 1) {
        cursorRef.current = "";
        void load("");
      }
    },
    [load],
  );

  const executeSingleAction = useCallback(
    async (taskId: string, action: string, reason: string) => {
      setActionPending(true);
      try {
        await fetchTaskControlActions(taskId, { action, reason });
        message.success(tGlobal("tasks.control.action_success", { action }));
        onActionSuccess?.();
        void load(cursorRef.current, localStatus, localRemoved);
      } catch (err: unknown) {
        const ax = err as { response?: { status?: number; data?: { message?: string } } };
        if (ax.response?.status === 409) {
          message.warning(tGlobal("tasks.control.conflict"));
        } else {
          message.error(ax.response?.data?.message || tGlobal("tasks.control.action_failed", { action }));
        }
      } finally {
        setActionPending(false);
      }
    },
    [load, localStatus, localRemoved, onActionSuccess],
  );

  const executeBatchAction = useCallback(
    async (action: string) => {
      if (selectedRowKeys.length === 0) return;
      setActionPending(true);
      try {
        const batchParams = {
          operation_id: crypto.randomUUID(),
          action,
          reason: `batch ${action}`,
          items: selectedRowKeys.map((id) => ({ task_identity: String(id) })),
        };
        const result: BatchResult = await fetchTaskControlBatch(batchParams);
        message.info(
          `${tGlobal("tasks.control.batch_result_title")}: ${result.succeeded} ${tGlobal("tasks.control.batch_succeeded")}, ${result.failed} ${tGlobal("tasks.control.batch_failed")}`
        );
        setSelectedRowKeys([]);
        onActionSuccess?.();
        void load(cursorRef.current, localStatus, localRemoved);
      } catch (err: unknown) {
        const ax = err as { response?: { data?: { message?: string } } };
        message.error(ax.response?.data?.message || tGlobal("tasks.control.action_failed", { action }));
      } finally {
        setActionPending(false);
      }
    },
    [selectedRowKeys, load, localStatus, localRemoved, onActionSuccess],
  );

  const rowSelection: TableRowSelection<ProjectionRow> = {
    selectedRowKeys,
    onChange: (keys) => setSelectedRowKeys(keys),
    columnWidth: 40,
  };

  const columns: ColumnsType<ProjectionRow> = [
    {
      title: t("tasks.control.col_task_id"),
      dataIndex: "task_id",
      key: "task_id",
      width: 150,
      ellipsis: true,
      sorter: (a, b) => a.source_id - b.source_id,
      render: (v: string) => <span style={{ fontFamily: "monospace", fontSize: 12, color: "#1677ff" }}>{v}</span>,
    },
    {
      title: t("tasks.control.col_type"),
      dataIndex: "task_type",
      key: "task_type",
      width: 100,
      render: (v: string) => <Tag style={{ margin: 0 }}>{v}</Tag>,
    },
    {
      title: t("tasks.control.col_status"),
      dataIndex: "normalized_status",
      key: "status",
      width: 100,
      render: (v: string) => (
        <Tag color={statusColor(v)} style={{ margin: 0 }}>
          {t(`tasks.control.status_${v}`)}
        </Tag>
      ),
    },
    {
      title: t("tasks.control.col_priority"),
      key: "priority",
      width: 80,
      sorter: (a, b) => a.effective_priority - b.effective_priority,
      render: (_: unknown, r: ProjectionRow) => (
        <span style={{ color: "#888" }}>
          {r.base_priority}
          {r.effective_priority !== r.base_priority && (
            <span style={{ color: "#1677ff" }}>→{r.effective_priority}</span>
          )}
        </span>
      ),
    },
    {
      title: t("tasks.control.col_info"),
      key: "info",
      ellipsis: true,
      render: (_: unknown, r: ProjectionRow) => (
        <span style={{ color: "#888", fontSize: 12 }}>
          {r.terminal_reason
            ? r.terminal_reason
            : r.removed_at
              ? `${t("tasks.control.detail_removed")}: ${r.remove_reason || "?"}`
              : `Gen ${r.generation} · ${r.attempt}/${r.max_attempts}${r.retry_round > 0 ? ` · Retry ${r.retry_round}` : ""}`}
        </span>
      ),
    },
    {
      title: t("tasks.control.col_updated"),
      dataIndex: "updated_at",
      key: "updated_at",
      width: 150,
      sorter: (a, b) => new Date(a.updated_at).getTime() - new Date(b.updated_at).getTime(),
      render: (v: string) => <span style={{ color: "#888", fontSize: 12 }}>{v ? new Date(v).toLocaleString() : "-"}</span>,
    },
    {
      title: t("common.actions"),
      key: "actions",
      width: 180,
      fixed: "right",
      render: (_: unknown, r: ProjectionRow) => (
        <Space size={2}>
          <Tooltip title={t("tasks.control.action_abort")}>
            <Popconfirm
              title={t("tasks.control.confirm_abort")}
              onConfirm={() => executeSingleAction(r.task_id, "abort", "abort")}
              okText={t("tasks.control.confirm")}
              cancelText={t("tasks.control.cancel")}
              placement="topRight"
            >
              <Button size="small" icon={<StopOutlined />} disabled={actionPending} />
            </Popconfirm>
          </Tooltip>
          <Tooltip title={t("tasks.control.action_reset")}>
            <Popconfirm
              title={t("tasks.control.confirm_reset")}
              onConfirm={() => executeSingleAction(r.task_id, "reset", "reset")}
              okText={t("tasks.control.confirm")}
              cancelText={t("tasks.control.cancel")}
              placement="topRight"
            >
              <Button size="small" icon={<ReloadOutlined />} disabled={actionPending} />
            </Popconfirm>
          </Tooltip>
          <Tooltip title={t("tasks.control.action_run_now")}>
            <Popconfirm
              title={t("tasks.control.confirm_run_now")}
              onConfirm={() => executeSingleAction(r.task_id, "run_now", "run_now")}
              okText={t("tasks.control.confirm")}
              cancelText={t("tasks.control.cancel")}
              placement="topRight"
            >
              <Button size="small" icon={<RiseOutlined />} disabled={actionPending} />
            </Popconfirm>
          </Tooltip>
          <Tooltip title={t("tasks.control.action_skip")}>
            <Popconfirm
              title={t("tasks.control.confirm_skip")}
              onConfirm={() => executeSingleAction(r.task_id, "skip", "skip")}
              okText={t("tasks.control.confirm")}
              cancelText={t("tasks.control.cancel")}
              placement="topRight"
            >
              <Button size="small" icon={<ForwardOutlined />} disabled={actionPending} />
            </Popconfirm>
          </Tooltip>
          <Tooltip title={t("tasks.control.action_remove")}>
            <Popconfirm
              title={t("tasks.control.confirm_remove")}
              onConfirm={() => executeSingleAction(r.task_id, "remove", "remove")}
              okText={t("tasks.control.confirm")}
              cancelText={t("tasks.control.cancel")}
              placement="topRight"
            >
              <Button size="small" icon={<DeleteOutlined />} disabled={actionPending} />
            </Popconfirm>
          </Tooltip>
          <Tooltip title={t("tasks.control.action_reopen")}>
            <Popconfirm
              title={t("tasks.control.confirm_reopen")}
              onConfirm={() => executeSingleAction(r.task_id, "reopen", "reopen")}
              okText={t("tasks.control.confirm")}
              cancelText={t("tasks.control.cancel")}
              placement="topRight"
            >
              <Button size="small" icon={<UnlockOutlined />} disabled={actionPending} />
            </Popconfirm>
          </Tooltip>
        </Space>
      ),
    },
  ];

  const items = state.data?.items ?? [];
  const total = state.data?.total ?? 0;

  return (
    <div>
      {/* Top bar: filters + batch actions */}
      <div style={{ display: "flex", justifyContent: "space-between", alignItems: "center", marginBottom: 12, flexWrap: "wrap", gap: 8 }}>
        <Space size={8} wrap>
          <Select
            value={localStatus}
            onChange={handleStatusChange}
            style={{ width: 130 }}
            size="small"
            options={STATUS_OPTIONS.map((o) => ({ value: o.value, label: t(`tasks.control.${o.label_key}`) }))}
          />
          <Select
            value={localRemoved}
            onChange={handleRemovedChange}
            style={{ width: 150 }}
            size="small"
            options={REMOVED_OPTIONS.map((o) => ({ value: o.value, label: t(`tasks.control.${o.label_key}`) }))}
          />
        </Space>

        <div style={{ display: "flex", alignItems: "center", gap: 8 }}>
          <span style={{ color: "#888", fontSize: 13 }}>
            {selectedRowKeys.length > 0
              ? t("tasks.control.batch_selected", { count: String(selectedRowKeys.length) })
              : `${t("tasks.control.total")}: ${total}`}
          </span>
          {selectedRowKeys.length > 0 && (
            <Space size={4} wrap>
              <Tooltip title={t("tasks.control.batch_clear")}>
                <Button size="small" icon={<ClearOutlined />} onClick={() => setSelectedRowKeys([])} />
              </Tooltip>
              <Tooltip title={t("tasks.control.batch_abort")}>
                <Popconfirm
                  title={`${t("tasks.control.confirm_abort")} (${selectedRowKeys.length})`}
                  onConfirm={() => executeBatchAction("abort")}
                  okText={t("tasks.control.confirm")}
                  cancelText={t("tasks.control.cancel")}
                >
                  <Button size="small" icon={<StopOutlined />} loading={actionPending} />
                </Popconfirm>
              </Tooltip>
              <Tooltip title={t("tasks.control.batch_reset")}>
                <Popconfirm
                  title={`${t("tasks.control.confirm_reset")} (${selectedRowKeys.length})`}
                  onConfirm={() => executeBatchAction("reset")}
                  okText={t("tasks.control.confirm")}
                  cancelText={t("tasks.control.cancel")}
                >
                  <Button size="small" icon={<ReloadOutlined />} loading={actionPending} />
                </Popconfirm>
              </Tooltip>
              <Tooltip title={t("tasks.control.batch_skip")}>
                <Popconfirm
                  title={`${t("tasks.control.confirm_skip")} (${selectedRowKeys.length})`}
                  onConfirm={() => executeBatchAction("skip")}
                  okText={t("tasks.control.confirm")}
                  cancelText={t("tasks.control.cancel")}
                >
                  <Button size="small" icon={<ForwardOutlined />} loading={actionPending} />
                </Popconfirm>
              </Tooltip>
              <Tooltip title={t("tasks.control.batch_remove")}>
                <Popconfirm
                  title={`${t("tasks.control.confirm_remove")} (${selectedRowKeys.length})`}
                  onConfirm={() => executeBatchAction("remove")}
                  okText={t("tasks.control.confirm")}
                  cancelText={t("tasks.control.cancel")}
                >
                  <Button size="small" icon={<DeleteOutlined />} loading={actionPending} />
                </Popconfirm>
              </Tooltip>
              <Tooltip title={t("tasks.control.batch_run_now")}>
                <Popconfirm
                  title={`${t("tasks.control.confirm_run_now")} (${selectedRowKeys.length})`}
                  onConfirm={() => executeBatchAction("run_now")}
                  okText={t("tasks.control.confirm")}
                  cancelText={t("tasks.control.cancel")}
                >
                  <Button size="small" icon={<RiseOutlined />} loading={actionPending} />
                </Popconfirm>
              </Tooltip>
            </Space>
          )}
        </div>
      </div>

      {state.error && !state.data && (
        <Alert
          type="error"
          showIcon
          title={state.error}
          style={{ marginBottom: 12 }}
          action={<Button size="small" onClick={() => load("", localStatus, localRemoved)}>{t("tasks.control.retry")}</Button>}
        />
      )}

      <Table
        rowKey="task_id"
        dataSource={items}
        columns={columns}
        rowSelection={rowSelection}
        loading={state.loading}
        size="small"
        scroll={{ x: 900 }}
        pagination={{
          defaultPageSize: 50,
          showSizeChanger: true,
          pageSizeOptions: ["20", "50", "100"],
          showTotal: (t, range) => `${range[0]}-${range[1]} / ${t}`,
        }}
        onChange={handleTableChange}
        onRow={(record) => ({
          onClick: () => onSelectRow?.(record.task_id),
          style: { cursor: "pointer" },
        })}
        locale={{
          emptyText: t("tasks.control.no_tasks"),
        }}
      />
    </div>
  );
}
