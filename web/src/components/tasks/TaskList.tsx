import { useCallback, useEffect, useRef, useState } from "react";
import type { ListResult } from "../../api/taskControl";
import { fetchTaskControlList } from "../../api/taskControl";
import type { TaskControlFilter } from "../../lib/taskControlFilters";

export interface TaskListProps {
  taskType: string;
  filter?: TaskControlFilter;
  removed?: string;
  onSelectRow?: (taskId: string) => void;
}

interface LoadState {
  loading: boolean;
  error: string | null;
  data: ListResult | null;
}

export function TaskList({ taskType, filter: extFilter, removed, onSelectRow }: TaskListProps) {
  const [state, setState] = useState<LoadState>({
    loading: false,
    error: null,
    data: null,
  });
  const cursorRef = useRef<string>("");
  const cursorHistory = useRef<string[]>([]);
  const mountedRef = useRef(true);

  const load = useCallback(
    async (cursor: string) => {
      setState((s) => ({ ...s, loading: true, error: null }));
      try {
        const result = await fetchTaskControlList({
          task_type: taskType,
          status: extFilter?.status,
          removed: removed ?? extFilter?.removed ?? "exclude",
          library_id: extFilter?.library_id,
          generation: extFilter?.generation,
          cursor: cursor || undefined,
          limit: 50,
        });
        if (!mountedRef.current) return;
        setState({ loading: false, error: null, data: result });
        cursorRef.current = result.next_cursor ?? "";
      } catch (err) {
        if (!mountedRef.current) return;
        setState((s) => ({
          ...s,
          loading: false,
          error: "query_failed",
        }));
      }
    },
    [taskType, extFilter, removed],
  );

  // Initial load
  useEffect(() => {
    mountedRef.current = true;
    cursorRef.current = "";
    cursorHistory.current = [];
    void load("");
    return () => {
      mountedRef.current = false;
    };
  }, [load]);

  const handleNext = useCallback(() => {
    if (!cursorRef.current) return;
    cursorHistory.current = [...cursorHistory.current, cursorRef.current];
    void load(cursorRef.current);
  }, [load]);

  const handlePrev = useCallback(() => {
    if (cursorHistory.current.length === 0) return;
    const prev = [...cursorHistory.current];
    prev.pop();
    cursorHistory.current = prev;
    void load(prev.length > 0 ? prev[prev.length - 1]! : "");
  }, [load]);

  const handleRetry = useCallback(() => {
    void load(cursorRef.current);
  }, [load]);

  if (state.loading && !state.data) {
    return (
      <div role="status" aria-label="Loading tasks" style={{ padding: 40, textAlign: "center", color: "#888" }}>
        Loading tasks...
      </div>
    );
  }

  if (state.error && !state.data) {
    return (
      <div style={{ padding: 40, textAlign: "center" }}>
        <p style={{ color: "#ff4d4f" }}>Failed to load tasks</p>
        <button
          onClick={handleRetry}
          style={{
            padding: "6px 16px",
            background: "#1677ff",
            color: "#fff",
            border: "none",
            borderRadius: 4,
            cursor: "pointer",
          }}
          aria-label="Retry"
        >
          Retry
        </button>
      </div>
    );
  }

  const items = state.data?.items ?? [];
  const total = state.data?.total ?? 0;
  const hasMore = state.data?.has_more ?? false;

  if (items.length === 0 && !state.loading) {
    return (
      <div style={{ padding: 40, textAlign: "center", color: "#888" }}>
        No tasks found
      </div>
    );
  }

  return (
    <div>
      <div
        style={{
          display: "flex",
          justifyContent: "space-between",
          alignItems: "center",
          marginBottom: 8,
          fontSize: 13,
          color: "#888",
        }}
      >
        <span>Total: {total}</span>
        <div style={{ display: "flex", gap: 8 }}>
          <button
            onClick={handlePrev}
            disabled={cursorHistory.current.length === 0 || state.loading}
            aria-label="Previous page"
            style={{
              padding: "4px 12px",
              background: "#1a1a1a",
              color: cursorHistory.current.length === 0 ? "#555" : "#d9d9d9",
              border: "1px solid #303030",
              borderRadius: 4,
              cursor: cursorHistory.current.length === 0 ? "not-allowed" : "pointer",
              fontSize: 13,
            }}
          >
            Previous
          </button>
          <button
            onClick={handleNext}
            disabled={!hasMore || state.loading}
            aria-label="Next page"
            style={{
              padding: "4px 12px",
              background: "#1a1a1a",
              color: !hasMore ? "#555" : "#d9d9d9",
              border: "1px solid #303030",
              borderRadius: 4,
              cursor: !hasMore ? "not-allowed" : "pointer",
              fontSize: 13,
            }}
          >
            Next
          </button>
        </div>
      </div>

      <div role="table" aria-label={`${taskType} task list`}>
        <div
          role="rowgroup"
          style={{
            display: "grid",
            gridTemplateColumns: "120px 100px 100px 1fr 160px",
            gap: 0,
            borderBottom: "1px solid #303030",
            padding: "6px 0",
            fontWeight: 600,
            fontSize: 12,
            color: "#888",
            textTransform: "uppercase",
          }}
        >
          <div role="columnheader">Task ID</div>
          <div role="columnheader">Type</div>
          <div role="columnheader">Status</div>
          <div role="columnheader">Info</div>
          <div role="columnheader">Updated</div>
        </div>

        {items.map((row) => (
          <div
            key={row.task_id}
            role="row"
            tabIndex={0}
            onClick={() => onSelectRow?.(row.task_id)}
            onKeyDown={(e) => {
              if (e.key === "Enter" || e.key === " ") {
                e.preventDefault();
                onSelectRow?.(row.task_id);
              }
            }}
            style={{
              display: "grid",
              gridTemplateColumns: "120px 100px 100px 1fr 160px",
              gap: 0,
              borderBottom: "1px solid #1a1a1a",
              padding: "8px 0",
              cursor: "pointer",
              fontSize: 13,
              transition: "background 0.15s",
            }}
            onMouseEnter={(e) => {
              (e.currentTarget as HTMLElement).style.background = "#111";
            }}
            onMouseLeave={(e) => {
              (e.currentTarget as HTMLElement).style.background = "transparent";
            }}
            aria-label={`Task ${row.task_id}, ${row.normalized_status}`}
          >
            <div role="cell" style={{ color: "#1677ff", fontFamily: "monospace" }}>
              {row.task_id}
            </div>
            <div role="cell">{row.task_type}</div>
            <div role="cell">
              <span
                style={{
                  display: "inline-block",
                  padding: "2px 8px",
                  borderRadius: 4,
                  fontSize: 11,
                  fontWeight: 500,
                  textTransform: "uppercase",
                  ...statusStyle(row.normalized_status),
                }}
              >
                {row.normalized_status}
              </span>
            </div>
            <div role="cell" style={{ color: "#888", overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap" }}>
              {row.terminal_reason
                ? `Error: ${row.terminal_reason}`
                : row.removed_at
                  ? `Removed: ${row.remove_reason || "unknown"}`
                  : row.retry_round > 0
                    ? `Retry round ${row.retry_round}`
                    : `Gen ${row.generation} · Attempt ${row.attempt}/${row.max_attempts}`}
            </div>
            <div role="cell" style={{ color: "#888", fontSize: 12 }}>
              {row.updated_at ? new Date(row.updated_at).toLocaleString() : "-"}
            </div>
          </div>
        ))}
      </div>

      {state.loading && (
        <div style={{ padding: 8, textAlign: "center", color: "#888", fontSize: 13 }}>
          Loading...
        </div>
      )}
    </div>
  );
}

function statusStyle(
  status: string,
): { background: string; color: string } {
  switch (status) {
    case "waiting":
      return { background: "#162312", color: "#49aa19" };
    case "running":
      return { background: "#111d2c", color: "#1677ff" };
    case "done":
      return { background: "#1a1a1a", color: "#52c41a" };
    case "failed":
      return { background: "#2c1618", color: "#ff4d4f" };
    case "cancelled":
      return { background: "#1a1a1a", color: "#faad14" };
    case "skipped":
      return { background: "#1a1a1a", color: "#888" };
    default:
      return { background: "#1a1a1a", color: "#d9d9d9" };
  }
}
