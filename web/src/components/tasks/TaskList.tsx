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
  const [currentPage, setCurrentPage] = useState(1);
  const [pageSize, setPageSize] = useState(50);

  const cursorHistoryRef = useRef<Map<number, string>>(new Map([[1, ""]]));
  const currentPageRef = useRef(1);
  const pageSizeRef = useRef(50);
  const localStatusRef = useRef("");
  const localRemovedRef = useRef(removed ?? "exclude");
  const requestSequenceRef = useRef(0);
  const mountedRef = useRef(true);

  const load = useCallback(
    async (page: number, cursor: string, status?: string, removedVal?: string, limit?: number) => {
      const requestSequence = ++requestSequenceRef.current;
      setState((s) => ({ ...s, loading: true, error: null }));
      try {
        const result = await fetchTaskControlList({
          task_type: taskType,
          status: status || extFilter?.status,
          removed: removedVal ?? removed ?? extFilter?.removed ?? "exclude",
          library_id: extFilter?.library_id,
          generation: extFilter?.generation,
          ...(cursor ? { cursor } : {}),
          limit: limit ?? pageSizeRef.current,
        });
        if (!mountedRef.current || requestSequence !== requestSequenceRef.current) return;

        if (page > 1 && result.items.length === 0) {
          const previousPage = Math.max(1, page - 1);
          const previousCursor = cursorHistoryRef.current.get(previousPage) ?? "";
          currentPageRef.current = previousPage;
          setCurrentPage(previousPage);
          void load(previousPage, previousCursor, status, removedVal, limit);
          return;
        }

        setState({ loading: false, error: null, data: result });
        if (result.has_more && result.next_cursor) {
          cursorHistoryRef.current.set(page + 1, result.next_cursor);
        } else {
          for (const knownPage of cursorHistoryRef.current.keys()) {
            if (knownPage > page) cursorHistoryRef.current.delete(knownPage);
          }
        }
      } catch {
        if (!mountedRef.current || requestSequence !== requestSequenceRef.current) return;
        setState((s) => ({ ...s, loading: false, error: tGlobal("tasks.control.load_failed") }));
      }
    },
    [taskType, extFilter?.status, extFilter?.removed, extFilter?.library_id, extFilter?.generation, removed],
  );

  const resetPagination = useCallback((status?: string, removedVal?: string, limit?: number) => {
    cursorHistoryRef.current = new Map([[1, ""]]);
    currentPageRef.current = 1;
    setCurrentPage(1);
    setSelectedRowKeys([]);
    void load(1, "", status, removedVal, limit);
  }, [load]);

  useEffect(() => {
    mountedRef.current = true;
    if (removed !== undefined) {
      localRemovedRef.current = removed;
      setLocalRemoved(removed);
    }
    resetPagination(localStatusRef.current, localRemovedRef.current);
    return () => {
      mountedRef.current = false;
      requestSequenceRef.current++;
    };
  }, [resetPagination, removed]);

  const handleStatusChange = useCallback((val: string) => {
    localStatusRef.current = val;
    setLocalStatus(val);
    resetPagination(val, localRemovedRef.current);
  }, [resetPagination]);

  const handleRemovedChange = useCallback((val: string) => {
    localRemovedRef.current = val;
    setLocalRemoved(val);
    resetPagination(localStatusRef.current, val);
  }, [resetPagination]);

  const handleTableChange = useCallback(
    (pagination: { current?: number; pageSize?: number }) => {
      const nextPageSize = pagination.pageSize ?? pageSizeRef.current;
      if (nextPageSize !== pageSizeRef.current) {
        pageSizeRef.current = nextPageSize;
        setPageSize(nextPageSize);
        resetPagination(localStatusRef.current, localRemovedRef.current, nextPageSize);
        return;
      }

      const page = pagination.current ?? 1;
      if (page === currentPageRef.current) return;
      const cursor = cursorHistoryRef.current.get(page);
      if (cursor === undefined) return;
      currentPageRef.current = page;
      setCurrentPage(page);
      setSelectedRowKeys([]);
      void load(page, cursor, localStatusRef.current, localRemovedRef.current);
    },
    [load, resetPagination],
  );

  const executeSingleAction = useCallback(
    async (taskId: string, action: string, reason: string) => {
      setActionPending(true);
      try {
        await fetchTaskControlActions(taskId, { action, reason });
        message.success(tGlobal("tasks.control.action_success", { action }));
        onActionSuccess?.();
        if (action === "remove") {
          resetPagination(localStatusRef.current, localRemovedRef.current);
        } else {
          const page = currentPageRef.current;
          void load(page, cursorHistoryRef.current.get(page) ?? "", localStatusRef.current, localRemovedRef.current);
        }
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
    [load, resetPagination, onActionSuccess],
  );

  const executeBatchAction = useCallback(
    async (action: string) => {
      if (selectedRowKeys.length === 0) return;
      const selected = (state.data?.items ?? []).filter((row) => selectedRowKeys.includes(row.task_id));
      const allOrchestration = selected.length === selectedRowKeys.length && selected.every((row) => row.source_kind === "orchestration");
      const externalKinds = new Set(selected.map((row) => row.source_kind));
      const allExternal = selected.length === selectedRowKeys.length && externalKinds.size === 1
        && selected.every((row) => row.source_kind !== "orchestration" && row.allowed_actions?.[action as keyof ProjectionRow["allowed_actions"]]);
      if (!allOrchestration && !allExternal) return;

      setActionPending(true);
      try {
        let succeeded = 0;
        let failed = 0;
        const successfulIDs: React.Key[] = [];
        let firstError = "";
        if (allExternal) {
          for (const row of selected) {
            try {
              await fetchTaskControlActions(row.task_id, { action, reason: `batch ${action}` });
              succeeded++;
              successfulIDs.push(row.task_id);
            } catch (err: unknown) {
              failed++;
              const ax = err as { response?: { data?: { message?: string; error?: string } }; message?: string };
              firstError ||= ax.response?.data?.message || ax.response?.data?.error || ax.message || "";
            }
          }
          setSelectedRowKeys((keys) => keys.filter((key) => !successfulIDs.includes(key)));
        } else {
          const batchParams = {
            operation_id: crypto.randomUUID(),
            action,
            reason: `batch ${action}`,
            items: selected.map((row) => ({ task_identity: row.task_id })),
          };
          const result: BatchResult = await fetchTaskControlBatch(batchParams);
          succeeded = result.succeeded;
          failed = result.failed;
          setSelectedRowKeys([]);
        }
        const summary = `${tGlobal("tasks.control.batch_result_title")}: ${succeeded} ${tGlobal("tasks.control.batch_succeeded")}, ${failed} ${tGlobal("tasks.control.batch_failed")}`;
        if (failed > 0 && firstError) message.error(`${summary}: ${firstError}`);
        else message.info(summary);
        if (succeeded > 0) onActionSuccess?.();
        if (action === "remove" && succeeded > 0) {
          resetPagination(localStatusRef.current, localRemovedRef.current);
        } else {
          const page = currentPageRef.current;
          await load(page, cursorHistoryRef.current.get(page) ?? "", localStatusRef.current, localRemovedRef.current);
        }
      } catch (err: unknown) {
        const ax = err as { response?: { data?: { message?: string } } };
        message.error(ax.response?.data?.message || tGlobal("tasks.control.action_failed", { action }));
      } finally {
        setActionPending(false);
      }
    },
    [selectedRowKeys, state.data?.items, load, resetPagination, onActionSuccess],
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
      title: t("tasks.control.col_media"),
      key: "media",
      width: 240,
      ellipsis: true,
      render: (_: unknown, r: ProjectionRow) =>
        r.media_id ? (
          <span style={{ fontSize: 12, display: "inline-flex", alignItems: "center", gap: 6, maxWidth: "100%" }}>
            <span style={{ color: "#888", fontFamily: "monospace", flexShrink: 0 }}>#{r.media_id}</span>
            {r.media_title && (
              <span style={{ color: "#d9d9d9", overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap" }}>
                {r.media_title}
              </span>
            )}
          </span>
        ) : (
          <span style={{ color: "#555" }}>-</span>
        ),
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
          {r.allowed_actions?.abort && <Tooltip title={t("tasks.control.action_abort")}>
            <Popconfirm
              title={t("tasks.control.confirm_abort")}
              onConfirm={() => executeSingleAction(r.task_id, "abort", "abort")}
              okText={t("tasks.control.confirm")}
              cancelText={t("tasks.control.cancel")}
              placement="topRight"
            >
              <Button size="small" icon={<StopOutlined />} disabled={actionPending} />
            </Popconfirm>
          </Tooltip>}
          {r.allowed_actions?.reset && <Tooltip title={t("tasks.control.action_reset")}>
            <Popconfirm
              title={t("tasks.control.confirm_reset")}
              onConfirm={() => executeSingleAction(r.task_id, "reset", "reset")}
              okText={t("tasks.control.confirm")}
              cancelText={t("tasks.control.cancel")}
              placement="topRight"
            >
              <Button size="small" icon={<ReloadOutlined />} disabled={actionPending} />
            </Popconfirm>
          </Tooltip>}
          {r.allowed_actions?.run_now && <Tooltip title={t("tasks.control.action_run_now")}>
            <Popconfirm
              title={t("tasks.control.confirm_run_now")}
              onConfirm={() => executeSingleAction(r.task_id, "run_now", "run_now")}
              okText={t("tasks.control.confirm")}
              cancelText={t("tasks.control.cancel")}
              placement="topRight"
            >
              <Button size="small" icon={<RiseOutlined />} disabled={actionPending} />
            </Popconfirm>
          </Tooltip>}
          {r.allowed_actions?.skip && <Tooltip title={t("tasks.control.action_skip")}>
            <Popconfirm
              title={t("tasks.control.confirm_skip")}
              onConfirm={() => executeSingleAction(r.task_id, "skip", "skip")}
              okText={t("tasks.control.confirm")}
              cancelText={t("tasks.control.cancel")}
              placement="topRight"
            >
              <Button size="small" icon={<ForwardOutlined />} disabled={actionPending} />
            </Popconfirm>
          </Tooltip>}
          {r.allowed_actions?.remove && <Tooltip title={t("tasks.control.action_remove")}>
            <Popconfirm
              title={t("tasks.control.confirm_remove")}
              onConfirm={() => executeSingleAction(r.task_id, "remove", "remove")}
              okText={t("tasks.control.confirm")}
              cancelText={t("tasks.control.cancel")}
              placement="topRight"
            >
              <Button size="small" icon={<DeleteOutlined />} disabled={actionPending} />
            </Popconfirm>
          </Tooltip>}
          {r.allowed_actions?.reopen && <Tooltip title={t("tasks.control.action_reopen")}>
            <Popconfirm
              title={t("tasks.control.confirm_reopen")}
              onConfirm={() => executeSingleAction(r.task_id, "reopen", "reopen")}
              okText={t("tasks.control.confirm")}
              cancelText={t("tasks.control.cancel")}
              placement="topRight"
            >
              <Button size="small" icon={<UnlockOutlined />} disabled={actionPending} />
            </Popconfirm>
          </Tooltip>}
        </Space>
      ),
    },
  ];

  const items = state.data?.items ?? [];
  const total = state.data?.total ?? 0;
  const selectedRows = items.filter((row) => selectedRowKeys.includes(row.task_id));
  const batchAllowed = (action: keyof ProjectionRow["allowed_actions"]) => {
    if (selectedRows.length !== selectedRowKeys.length || selectedRows.length === 0) return false;
    const sourceKind = selectedRows[0].source_kind;
    if (!selectedRows.every((row) => row.source_kind === sourceKind)) return false;
    return selectedRows.every((row) => row.allowed_actions?.[action]);
  };

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
              {batchAllowed("abort") && <Tooltip title={t("tasks.control.batch_abort")}>
                <Popconfirm
                  title={`${t("tasks.control.confirm_abort")} (${selectedRowKeys.length})`}
                  onConfirm={() => executeBatchAction("abort")}
                  okText={t("tasks.control.confirm")}
                  cancelText={t("tasks.control.cancel")}
                >
                  <Button size="small" icon={<StopOutlined />} loading={actionPending} />
                </Popconfirm>
              </Tooltip>}
              {batchAllowed("reset") && <Tooltip title={t("tasks.control.batch_reset")}>
                <Popconfirm
                  title={`${t("tasks.control.confirm_reset")} (${selectedRowKeys.length})`}
                  onConfirm={() => executeBatchAction("reset")}
                  okText={t("tasks.control.confirm")}
                  cancelText={t("tasks.control.cancel")}
                >
                  <Button size="small" icon={<ReloadOutlined />} loading={actionPending} />
                </Popconfirm>
              </Tooltip>}
              {batchAllowed("skip") && <Tooltip title={t("tasks.control.batch_skip")}>
                <Popconfirm
                  title={`${t("tasks.control.confirm_skip")} (${selectedRowKeys.length})`}
                  onConfirm={() => executeBatchAction("skip")}
                  okText={t("tasks.control.confirm")}
                  cancelText={t("tasks.control.cancel")}
                >
                  <Button size="small" icon={<ForwardOutlined />} loading={actionPending} />
                </Popconfirm>
              </Tooltip>}
              {batchAllowed("remove") && <Tooltip title={t("tasks.control.batch_remove")}>
                <Popconfirm
                  title={`${t("tasks.control.confirm_remove")} (${selectedRowKeys.length})`}
                  onConfirm={() => executeBatchAction("remove")}
                  okText={t("tasks.control.confirm")}
                  cancelText={t("tasks.control.cancel")}
                >
                  <Button size="small" icon={<DeleteOutlined />} loading={actionPending} />
                </Popconfirm>
              </Tooltip>}
              {batchAllowed("run_now") && <Tooltip title={t("tasks.control.batch_run_now")}>
                <Popconfirm
                  title={`${t("tasks.control.confirm_run_now")} (${selectedRowKeys.length})`}
                  onConfirm={() => executeBatchAction("run_now")}
                  okText={t("tasks.control.confirm")}
                  cancelText={t("tasks.control.cancel")}
                >
                  <Button size="small" icon={<RiseOutlined />} loading={actionPending} />
                </Popconfirm>
              </Tooltip>}
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
          action={<Button size="small" onClick={() => {
            const page = currentPageRef.current;
            void load(page, cursorHistoryRef.current.get(page) ?? "", localStatusRef.current, localRemovedRef.current);
          }}>{t("tasks.control.retry")}</Button>}
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
          current: currentPage,
          total,
          pageSize,
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
