import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen, fireEvent, waitFor } from "@testing-library/react";
import { TaskActions } from "../TaskActions";

const mockFetchActions = vi.fn();
vi.mock("../../../api/taskControl", () => ({
  fetchTaskControlActions: (...args: unknown[]) => mockFetchActions(...args),
}));

describe("TaskActions", () => {
  const allActions = {
    abort: true,
    remove: true,
    reset: true,
    run_now: true,
    skip: false,
    reopen: false,
  };

  beforeEach(() => {
    vi.clearAllMocks();
    mockFetchActions.mockResolvedValue({ status: "ok" });
  });

  it("renders only true server actions", () => {
    render(<TaskActions taskId="test:1" actions={allActions} />);

    // Buttons rendered with i18n text
    expect(screen.getByText(/中止|action_abort/)).toBeInTheDocument();
    expect(screen.getByText(/移除|action_remove/)).toBeInTheDocument();
    expect(screen.getByText(/重置|action_reset/)).toBeInTheDocument();
    expect(screen.getByText(/立即运行|action_run_now/)).toBeInTheDocument();
    expect(screen.queryByText(/跳过|action_skip/)).toBeNull();
    expect(screen.queryByText(/重新打开|action_reopen/)).toBeNull();
  });

  it("returns null when no actions are allowed", () => {
    const { container } = render(
      <TaskActions taskId="test:1" actions={{ abort: false, remove: false, reset: false, run_now: false, skip: false, reopen: false }} />,
    );
    expect(container.innerHTML).toBe("");
  });

  it("shows reason prompt for remove action", async () => {
    render(<TaskActions taskId="test:1" actions={allActions} />);

    const removeBtn = screen.getByText(/移除|action_remove/);
    fireEvent.click(removeBtn);

    // Should show reason input field
    await waitFor(() => {
      const input = document.querySelector("input");
      expect(input).toBeInTheDocument();
    });
  });

  it("calls action API after popconfirm", async () => {
    mockFetchActions.mockResolvedValue({ status: "ok", action: "abort", task_id: "test:1" });
    render(<TaskActions taskId="test:1" actions={allActions} />);

    // Click "Abort" which now shows a Popconfirm
    const abortBtn = screen.getByText(/中止|action_abort/);
    fireEvent.click(abortBtn);

    // Find and click the OK button in the popover
    await waitFor(() => {
      const okBtn = document.querySelector(".ant-popconfirm .ant-btn-primary");
      expect(okBtn).toBeInTheDocument();
      fireEvent.click(okBtn!);
    });

    await waitFor(() => {
      expect(mockFetchActions).toHaveBeenCalled();
    });
  });
});
