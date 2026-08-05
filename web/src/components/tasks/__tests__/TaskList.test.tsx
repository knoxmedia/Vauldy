import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen, fireEvent, waitFor } from "@testing-library/react";
import { TaskList } from "../TaskList";
import type { ProjectionRow } from "../../../api/taskControl";

const mockFetchTaskControlList = vi.fn();
const mockFetchTaskControlActions = vi.fn();
const mockFetchTaskControlBatch = vi.fn();

vi.mock("../../../api/taskControl", () => ({
  fetchTaskControlList: (...args: unknown[]) => mockFetchTaskControlList(...args),
  fetchTaskControlActions: (...args: unknown[]) => mockFetchTaskControlActions(...args),
  fetchTaskControlBatch: (...args: unknown[]) => mockFetchTaskControlBatch(...args),
}));

function makeRow(id: number, overrides: Partial<ProjectionRow> = {}): ProjectionRow {
  return {
    task_id: `orchestration:${id}`,
    source_kind: "orchestration",
    source_id: id,
    task_type: "poster",
    family: "media_ingestion",
    normalized_status: "waiting",
    raw_status: "waiting",
    revision: id,
    generation: 1,
    retry_round: 0,
    attempt: 0,
    max_attempts: 3,
    base_priority: 0,
    effective_priority: 0,
    created_at: new Date().toISOString(),
    updated_at: new Date().toISOString(),
    tombstone: false,
    allowed_actions: { abort: false, remove: false, reset: false, run_now: false, skip: false, reopen: false },
    ...overrides,
  };
}

describe("TaskList", () => {
  const mockOnSelectRow = vi.fn();
  const defaultProps = {
    taskType: "poster",
    onSelectRow: mockOnSelectRow,
  };

  beforeEach(() => {
    vi.clearAllMocks();
    mockFetchTaskControlList.mockResolvedValue({
      items: [],
      total: 0,
      has_more: false,
      truncated: false,
      snapshot_revision: 10,
    });
    mockFetchTaskControlActions.mockResolvedValue({ status: "ok" });
    mockFetchTaskControlBatch.mockResolvedValue({
      operation_id: "test-op",
      action: "abort",
      results: [],
      total: 0,
      succeeded: 0,
      failed: 0,
      retryable: [],
    });
  });

  it("renders task rows with normalized status", async () => {
    mockFetchTaskControlList.mockResolvedValue({
      items: [makeRow(1), makeRow(2)],
      total: 2,
      has_more: false,
      truncated: false,
      snapshot_revision: 10,
    });

    render(<TaskList {...defaultProps} />);

    const row1 = await screen.findByText("orchestration:1");
    expect(row1).toBeInTheDocument();
    expect(screen.getByText("orchestration:2")).toBeInTheDocument();
  });

  it("shows total count", async () => {
    mockFetchTaskControlList.mockResolvedValue({
      items: [makeRow(1)],
      total: 100,
      has_more: true,
      truncated: false,
      snapshot_revision: 10,
      next_cursor: "next-cursor",
    });

    render(<TaskList {...defaultProps} />);

    await waitFor(() => {
      expect(screen.queryByText(/100/)).toBeInTheDocument();
    });
  });

  it("renders Ant Design table", async () => {
    mockFetchTaskControlList.mockResolvedValue({
      items: [makeRow(1), makeRow(2), makeRow(3)],
      total: 50,
      has_more: true,
      truncated: false,
      snapshot_revision: 10,
      next_cursor: "next-cursor",
    });

    render(<TaskList {...defaultProps} />);

    await screen.findByText("orchestration:1");

    const table = document.querySelector(".ant-table");
    expect(table).toBeInTheDocument();
  });

  it("calls onSelectRow when a row is clicked", async () => {
    mockFetchTaskControlList.mockResolvedValue({
      items: [makeRow(1)],
      total: 1,
      has_more: false,
      truncated: false,
      snapshot_revision: 10,
    });

    render(<TaskList {...defaultProps} />);

    const row = await screen.findByText("orchestration:1");
    fireEvent.click(row);

    expect(mockOnSelectRow).toHaveBeenCalledWith("orchestration:1");
  });

  it("shows empty state when no items", async () => {
    mockFetchTaskControlList.mockResolvedValue({
      items: [],
      total: 0,
      has_more: false,
      truncated: false,
      snapshot_revision: 1,
    });

    render(<TaskList {...defaultProps} />);

    await waitFor(() => {
      const emptyTable = document.querySelector(".ant-table-empty");
      expect(emptyTable).toBeInTheDocument();
    });
  });

  it("shows loading state on Table", async () => {
    mockFetchTaskControlList.mockReturnValue(new Promise(() => {}));
    render(<TaskList {...defaultProps} />);

    await waitFor(() => {
      const spin = document.querySelector(".ant-spin");
      expect(spin).toBeInTheDocument();
    });
  });

  it("shows error state with retry", async () => {
    mockFetchTaskControlList.mockRejectedValue(new Error("fail"));
    render(<TaskList {...defaultProps} />);

    await waitFor(() => {
      const alert = document.querySelector(".ant-alert-error");
      expect(alert).toBeInTheDocument();
    });
  });
  it("renders only server-allowed row actions", async () => {
    mockFetchTaskControlList.mockResolvedValue({ items: [makeRow(9, { source_kind: "scan_task", task_id: "scan_task:9", normalized_status: "running", raw_status: "running", allowed_actions: { abort: true, remove: false, reset: false, run_now: false, skip: false, reopen: false } })], total: 1, has_more: false, truncated: false, snapshot_revision: 1 });
    render(<TaskList {...defaultProps} />);
    await screen.findByText("scan_task:9");
    expect(document.querySelector(".anticon-stop")).toBeInTheDocument();
    expect(document.querySelector(".anticon-reload")).not.toBeInTheDocument();
  });

  it("clear-selection only deselects and uses the correct label", async () => {
    mockFetchTaskControlList.mockResolvedValue({ items: [makeRow(1)], total: 1, has_more: false, truncated: false, snapshot_revision: 1 });
    render(<TaskList {...defaultProps} />);
    await screen.findByText("orchestration:1");
    fireEvent.click(document.querySelector('tbody input[type="checkbox"]')!);
    const clear = document.querySelector(".anticon-clear")!.closest("button")!;
    fireEvent.mouseEnter(clear);
    expect(await screen.findByText("\u53d6\u6d88\u9009\u62e9")).toBeInTheDocument();
    fireEvent.click(clear);
    expect(mockFetchTaskControlActions).not.toHaveBeenCalled();
    expect(mockFetchTaskControlBatch).not.toHaveBeenCalled();
  });

  it("removes selected eligible external rows through single-action calls", async () => {
    const rows = [1, 2].map((id) => makeRow(id, {
      task_id: `transcode_task:${id}`, source_kind: "transcode_task", task_type: "pretranscode",
      normalized_status: "cancelled", raw_status: "cancelled",
      allowed_actions: { abort: false, remove: true, reset: false, run_now: false, skip: false, reopen: false },
    }));
    mockFetchTaskControlList.mockResolvedValue({ items: rows, total: 2, has_more: false, truncated: false, snapshot_revision: 1 });
    render(<TaskList {...defaultProps} taskType="pretranscode" />);
    await screen.findByText("transcode_task:1");
    fireEvent.click(document.querySelector('thead input[type="checkbox"]')!);
    const batchDelete = Array.from(document.querySelectorAll(".anticon-delete"))
      .map((icon) => icon.closest("button")!)
      .find((button) => !button.closest("tbody"))!;
    fireEvent.click(batchDelete);
    await waitFor(() => expect(document.querySelector(".ant-popconfirm-buttons .ant-btn-primary")).toBeInTheDocument());
    fireEvent.click(document.querySelector(".ant-popconfirm-buttons .ant-btn-primary")!);
    await waitFor(() => expect(mockFetchTaskControlActions).toHaveBeenCalledTimes(2));
    expect(mockFetchTaskControlActions).toHaveBeenNthCalledWith(1, "transcode_task:1", { action: "remove", reason: "batch remove" });
    expect(mockFetchTaskControlActions).toHaveBeenNthCalledWith(2, "transcode_task:2", { action: "remove", reason: "batch remove" });
    expect(mockFetchTaskControlBatch).not.toHaveBeenCalled();
    await waitFor(() => expect(mockFetchTaskControlList).toHaveBeenCalledTimes(2));
  });

  it("hides batch remove when an external selection is not removable", async () => {
    const removable = makeRow(1, { task_id: "transcode_task:1", source_kind: "transcode_task", normalized_status: "cancelled", raw_status: "cancelled", allowed_actions: { abort: false, remove: true, reset: false, run_now: false, skip: false, reopen: false } });
    const linked = makeRow(2, { task_id: "transcode_task:2", source_kind: "transcode_task", normalized_status: "cancelled", raw_status: "cancelled" });
    mockFetchTaskControlList.mockResolvedValue({ items: [removable, linked], total: 2, has_more: false, truncated: false, snapshot_revision: 1 });
    render(<TaskList {...defaultProps} taskType="pretranscode" />);
    await screen.findByText("transcode_task:1");
    document.querySelectorAll('tbody input[type="checkbox"]').forEach((box) => fireEvent.click(box));
    expect(document.querySelectorAll(".anticon-delete")).toHaveLength(1);
  });

});
