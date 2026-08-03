import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen, fireEvent } from "@testing-library/react";
import { TaskDetailDrawer } from "../TaskDetailDrawer";

const mockFetchDetail = vi.fn();
vi.mock("../../../api/taskControl", () => ({
  fetchTaskControlDetail: (...args: unknown[]) => mockFetchDetail(...args),
}));

describe("TaskDetailDrawer", () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  function makeRow(overrides = {}) {
    return {
      task_id: "orchestration:1",
      source_kind: "orchestration",
      source_id: 1,
      task_type: "poster",
      family: "media_ingestion",
      normalized_status: "waiting",
      raw_status: "waiting",
      revision: 5,
      generation: 1,
      retry_round: 0,
      attempt: 1,
      max_attempts: 3,
      base_priority: 0,
      effective_priority: 0,
      created_at: new Date().toISOString(),
      updated_at: new Date().toISOString(),
      tombstone: false,
      ...overrides,
    };
  }

  it("returns null when no taskId", () => {
    const { container } = render(<TaskDetailDrawer taskId={null} onClose={() => {}} />);
    expect(container.innerHTML).toBe("");
  });

  it("renders detail for a task", async () => {
    mockFetchDetail.mockResolvedValue({ row: makeRow() });
    render(<TaskDetailDrawer taskId="orchestration:1" onClose={() => {}} />);

    expect(await screen.findByText("orchestration:1")).toBeInTheDocument();
  });

  it("calls onClose when close button is clicked", () => {
    const onClose = vi.fn();
    mockFetchDetail.mockResolvedValue({ row: makeRow() });
    render(<TaskDetailDrawer taskId="orchestration:1" onClose={onClose} />);

    // Find close button by aria-label
    const closeBtn = screen.getByRole("button", { name: /close/i });
    fireEvent.click(closeBtn);
    expect(onClose).toHaveBeenCalled();
  });

  it("shows loading state", () => {
    mockFetchDetail.mockReturnValue(new Promise(() => {}));
    render(<TaskDetailDrawer taskId="orchestration:1" onClose={() => {}} />);

    expect(screen.getByText(/loading/i)).toBeInTheDocument();
  });

  it("shows removed_at info when present", async () => {
    mockFetchDetail.mockResolvedValue({
      row: makeRow({
        removed_at: "2025-06-01T00:00:00Z",
        removed_by: "admin",
        remove_reason: "cleanup",
      }),
    });
    render(<TaskDetailDrawer taskId="orchestration:1" onClose={() => {}} />);

    expect(await screen.findByText(/removed/i)).toBeInTheDocument();
  });
});
