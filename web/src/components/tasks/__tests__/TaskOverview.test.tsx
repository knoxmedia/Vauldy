import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen, fireEvent } from "@testing-library/react";
import { TaskOverview } from "../TaskOverview";

const mockFetchOverview = vi.fn();
vi.mock("../../../api/taskControl", () => ({
  fetchTaskControlOverview: (...args: unknown[]) => mockFetchOverview(...args),
}));

describe("TaskOverview", () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

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

  it("renders status counts", async () => {
    mockFetchOverview.mockResolvedValue(makeOverview());
    render(<TaskOverview />);

    expect(await screen.findByText("Status Summary")).toBeInTheDocument();
    // Status counts render as numbers
    expect(screen.getByText("waiting")).toBeInTheDocument();
    expect(screen.getByText("running")).toBeInTheDocument();
    expect(screen.getByText("done")).toBeInTheDocument();
  });

  it("renders type counts as clickable buttons", async () => {
    const onDrill = vi.fn();
    mockFetchOverview.mockResolvedValue(makeOverview());
    render(<TaskOverview onDrillDownType={onDrill} />);

    const btn = await screen.findByRole("button", { name: /poster/i });
    fireEvent.click(btn);
    expect(onDrill).toHaveBeenCalledWith("poster");
  });

  it("shows loading state", () => {
    mockFetchOverview.mockReturnValue(new Promise(() => {}));
    render(<TaskOverview />);
    expect(screen.getByRole("status")).toBeInTheDocument();
  });

  it("shows error with retry", async () => {
    mockFetchOverview.mockRejectedValue(new Error("fail"));
    render(<TaskOverview />);

    expect(await screen.findByText(/failed to load/i)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /retry/i })).toBeInTheDocument();
  });

  it("renders section headings", async () => {
    mockFetchOverview.mockResolvedValue(makeOverview());
    render(<TaskOverview />);

    expect(await screen.findByText("Status Summary")).toBeInTheDocument();
    expect(screen.getByText("Task Types")).toBeInTheDocument();
  });
});
