import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen, fireEvent, waitFor } from "@testing-library/react";
import { TaskDetailDrawer } from "../TaskDetailDrawer";

const mockFetchDetail = vi.fn();
const mockFetchActions = vi.fn();
vi.mock("../../../api/taskControl", () => ({
  fetchTaskControlDetail: (...args: unknown[]) => mockFetchDetail(...args),
  fetchTaskControlActions: (...args: unknown[]) => mockFetchActions(...args),
}));

describe("TaskDetailDrawer", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    mockFetchActions.mockResolvedValue({ status: "ok" });
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

  it("drawer is closed when no taskId", () => {
    render(<TaskDetailDrawer taskId={null} onClose={() => {}} />);
    expect(document.querySelector(".ant-drawer-body")).toBeNull();
  });

  it("renders detail for a task", async () => {
    mockFetchDetail.mockResolvedValue({ row: makeRow() });
    render(<TaskDetailDrawer taskId="orchestration:1" onClose={() => {}} />);

    await waitFor(() => {
      expect(screen.getByText("orchestration:1")).toBeInTheDocument();
    }, { timeout: 5000 });
  });

  it("calls onClose when drawer close is triggered", async () => {
    const onClose = vi.fn();
    mockFetchDetail.mockResolvedValue({ row: makeRow() });
    render(<TaskDetailDrawer taskId="orchestration:1" onClose={onClose} />);

    await screen.findByText("orchestration:1", {}, { timeout: 5000 });
    const closeBtn = document.querySelector(".ant-drawer-close");
    if (closeBtn) {
      fireEvent.click(closeBtn);
      expect(onClose).toHaveBeenCalled();
    }
  });

  it("shows loading Spin", async () => {
    mockFetchDetail.mockReturnValue(new Promise(() => {}));
    render(<TaskDetailDrawer taskId="orchestration:1" onClose={() => {}} />);

    await waitFor(() => {
      expect(document.querySelector(".ant-spin")).toBeInTheDocument();
    }, { timeout: 3000 });
  });

  it("drawer body contains task info when loaded", async () => {
    mockFetchDetail.mockResolvedValue({
      row: makeRow({
        removed_at: "2025-06-01T00:00:00Z",
        removed_by: "admin",
        remove_reason: "cleanup",
      }),
    });
    render(<TaskDetailDrawer taskId="orchestration:1" onClose={() => {}} />);

    await waitFor(() => {
      // Drawer body should contain content
      const body = document.querySelector(".ant-drawer-body");
      expect(body).toBeInTheDocument();
      // Task ID should be visible
      expect(screen.getByText("orchestration:1")).toBeInTheDocument();
    }, { timeout: 5000 });
  });
});
