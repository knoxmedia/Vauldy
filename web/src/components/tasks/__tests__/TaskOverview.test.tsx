import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen, fireEvent, waitFor } from "@testing-library/react";
import { TaskOverview } from "../TaskOverview";

const mockFetchOverview = vi.fn();
vi.mock("../../../api/taskControl", () => ({
  fetchTaskControlOverview: (...args: unknown[]) => mockFetchOverview(...args),
}));

function makeOverview(overrides = {}) {
  return {
    status_counts: { waiting: 3, running: 2, done: 5, failed: 1, cancelled: 1, skipped: 0 },
    type_counts: { poster: 3, transcode: 5, preview: 4 },
    running: { label: "running", items: [] },
    oldest: { label: "oldest", items: [] },
    blocked: { label: "blocked", items: [] },
    no_worker: { label: "no_worker", items: [] },
    expired: { label: "expired", items: [] },
    recovery: { label: "recovery", items: [] },
    cleanup: { label: "cleanup", items: [] },
    snapshot_revision: 10,
    ...overrides,
  };
}

describe("TaskOverview", () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  it("renders card titles when data loads", async () => {
    mockFetchOverview.mockResolvedValue(makeOverview());
    render(<TaskOverview />);

    // Card title "状态概览" (Status Summary) should appear
    await waitFor(() => {
      expect(screen.getByText(/状态概览/i)).toBeInTheDocument();
    });
  });

  it("renders type counts as clickable buttons", async () => {
    const onDrill = vi.fn();
    mockFetchOverview.mockResolvedValue(makeOverview());
    render(<TaskOverview onDrillDownType={onDrill} />);

    const btn = await screen.findByText(/poster/i, {}, { timeout: 5000 });
    fireEvent.click(btn);
    expect(onDrill).toHaveBeenCalledWith("poster");
  });

  it("hides type counts with zero count", async () => {
    mockFetchOverview.mockResolvedValue(
      makeOverview({ type_counts: { poster: 3, transcode: 0, preview: 4 } }),
    );
    render(<TaskOverview />);

    await screen.findByText(/poster/i, {}, { timeout: 5000 });
    expect(screen.queryByText(/transcode/i)).not.toBeInTheDocument();
    expect(screen.getByText(/preview/i)).toBeInTheDocument();
  });

  it("shows loading placeholder", async () => {
    mockFetchOverview.mockReturnValue(new Promise(() => {}));
    render(<TaskOverview />);

    await screen.findByText(/正在加载总览/i, {}, { timeout: 3000 });
  });

  it("shows error alert on failure", async () => {
    mockFetchOverview.mockRejectedValue(new Error("fail"));
    render(<TaskOverview />);

    await waitFor(() => {
      expect(screen.getByText(/加载任务失败/i)).toBeInTheDocument();
    }, { timeout: 5000 });
  });
});
