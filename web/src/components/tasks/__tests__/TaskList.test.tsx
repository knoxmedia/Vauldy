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
});
