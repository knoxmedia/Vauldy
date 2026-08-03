import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen, fireEvent } from "@testing-library/react";
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
  });

  it("renders only true server actions", () => {
    render(<TaskActions taskId="test:1" actions={allActions} />);

    expect(screen.getByText("Abort")).toBeInTheDocument();
    expect(screen.getByText("Remove")).toBeInTheDocument();
    expect(screen.getByText("Reset")).toBeInTheDocument();
    expect(screen.getByText("Run Now")).toBeInTheDocument();
    expect(screen.queryByText("Skip")).not.toBeInTheDocument();
    expect(screen.queryByText("Reopen")).not.toBeInTheDocument();
  });

  it("returns null when no actions are allowed", () => {
    const { container } = render(
      <TaskActions taskId="test:1" actions={{ abort: false, remove: false, reset: false, run_now: false, skip: false, reopen: false }} />,
    );
    expect(container.innerHTML).toBe("");
  });

  it("shows reason prompt for remove action", () => {
    render(<TaskActions taskId="test:1" actions={allActions} />);

    fireEvent.click(screen.getByText("Remove"));
    expect(screen.getByPlaceholderText(/reason for remove/i)).toBeInTheDocument();
  });

  it("calls action API on confirm", async () => {
    mockFetchActions.mockResolvedValue({ status: "ok", action: "abort", task_id: "test:1" });
    render(<TaskActions taskId="test:1" actions={allActions} />);

    fireEvent.click(screen.getByText("Abort"));
    // Abort doesn't need a reason, so it should dispatch immediately
    await screen.findByText("Abort"); // Re-renders

    expect(mockFetchActions).toHaveBeenCalled();
  });
});
