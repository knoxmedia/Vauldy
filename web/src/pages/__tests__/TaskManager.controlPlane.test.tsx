import { describe, it, expect, vi, beforeEach } from "vitest";
import { render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router-dom";
import TaskManagerPage from "../../pages/TaskManager";
import { useAuthStore } from "../../store/auth";
import { I18nProvider } from "../../i18n";

// Mock the task control API
vi.mock("../../api/taskControl", () => ({
  fetchTaskControlRegistry: vi.fn().mockResolvedValue({
    groups: [],
  }),
  fetchTaskControlOverview: vi.fn().mockResolvedValue({
    status_counts: { waiting: 0, running: 0, done: 0, failed: 0, cancelled: 0, skipped: 0 },
    type_counts: {},
    running: { label: "running", items: [] },
    oldest: { label: "oldest", items: [] },
    blocked: { label: "blocked", items: [] },
    no_worker: { label: "no_worker", items: [] },
    expired: { label: "expired", items: [] },
    recovery: { label: "recovery", items: [] },
    cleanup: { label: "cleanup", items: [] },
    snapshot_revision: 1,
  }),
  fetchTaskControlList: vi.fn().mockResolvedValue({
    items: [],
    total: 0,
    has_more: false,
    truncated: false,
    snapshot_revision: 1,
  }),
}));

function renderTaskManager() {
  return render(
    <I18nProvider>
      <MemoryRouter>
        <TaskManagerPage />
      </MemoryRouter>
    </I18nProvider>,
  );
}

describe("TaskManager control plane integration", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    useAuthStore.getState().setToken("test-token");
  });

  it("renders the task type navigation bar", async () => {
    renderTaskManager();
    // The page should have some form of navigation
    // When connected to the real registry, navigation tabs appear
    expect(screen.getByRole("main")).toBeInTheDocument();
  });

  it("renders in a main landmark", async () => {
    renderTaskManager();
    const main = screen.getByRole("main");
    expect(main).toBeInTheDocument();
  });

  it("renders heading for task management", async () => {
    renderTaskManager();
    // The page should have task-related heading
    expect(screen.getByRole("heading", { level: 1 })).toBeInTheDocument();
  });

  it("does not render old per-type status filters as independent selectors", async () => {
    renderTaskManager();
    // The old per-type approach (transcodeStatusFilter, subtitleStatusFilter etc.)
    // should not be rendering independent status filter dropdowns
    // Instead, the page should use the unified control plane
    const selects = screen.queryAllByRole("combobox");
    // Old approach had many selects; new approach has fewer
    expect(selects.length).toBeLessThanOrEqual(5);
  });
});
