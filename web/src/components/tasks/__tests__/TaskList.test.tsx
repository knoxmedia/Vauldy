import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen, fireEvent } from "@testing-library/react";
import { TaskList } from "../TaskList";
import type { ProjectionRow } from "../../../api/taskControl";

const mockFetchTaskControlList = vi.fn();

vi.mock("../../../api/taskControl", () => ({
  fetchTaskControlList: (...args: unknown[]) => mockFetchTaskControlList(...args),
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

    const total = await screen.findByText(/100/);
    expect(total).toBeInTheDocument();
  });

  it("shows next button when has_more is true", async () => {
    mockFetchTaskControlList.mockResolvedValue({
      items: [makeRow(1), makeRow(2), makeRow(3)],
      total: 50,
      has_more: true,
      truncated: false,
      snapshot_revision: 10,
      next_cursor: "next-cursor",
    });

    render(<TaskList {...defaultProps} />);

    const nextBtn = await screen.findByRole("button", { name: /next/i });
    expect(nextBtn).toBeInTheDocument();
    expect(nextBtn).not.toBeDisabled();
  });

  it("disables next button when has_more is false", async () => {
    mockFetchTaskControlList.mockResolvedValue({
      items: [makeRow(1)],
      total: 1,
      has_more: false,
      truncated: false,
      snapshot_revision: 10,
    });

    render(<TaskList {...defaultProps} />);

    const nextBtn = await screen.findByRole("button", { name: /next/i });
    expect(nextBtn).toBeDisabled();
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

    const emptyMsg = await screen.findByText(/no tasks/i);
    expect(emptyMsg).toBeInTheDocument();
  });

  it("shows loading state initially", () => {
    mockFetchTaskControlList.mockReturnValue(new Promise(() => {}));
    render(<TaskList {...defaultProps} />);
    expect(screen.getByRole("status")).toBeInTheDocument();
  });

  it("shows error state with retry button", async () => {
    mockFetchTaskControlList.mockRejectedValue(new Error("fail"));
    render(<TaskList {...defaultProps} />);
    const retryBtn = await screen.findByRole("button", { name: /retry/i });
    expect(retryBtn).toBeInTheDocument();
  });
});
